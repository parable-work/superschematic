/*
The indexes of a schema's instance type over its own fields (D16,
amended: an instance is found by a unique field, and a list filters by
equality). `@unique` and `@key` on a field, and each entry of the type's
`indexes` (`@index`), become one SQLite index on engine_instances: led by
the instance's namespace, then the value of each key,
json_extract(data, '$."<key>"'), partial on the schema's rows in the
namespace that holds it and on a value in every key. A unique one is
UNIQUE, so SQLite refuses a write that would give a second instance of a
namespace the same values, and the store reports which fields
(UniqueConflictError, conflict). The index is named by a digest of the
holder, the schema and its keys (engine_unique_<16 hex>,
engine_index_<16 hex>), so publish finds it again without a table of its
own.

A key is a top-level own field whose value is a string, a number or a
boolean: an enum, a primitive or a scalar whose JSON type is one of
those. A list, an object or a JSON value has no one value an index can
compare, so define refuses @unique, @key and @index on one, and on any
type but the instance type, which is the only one that holds instances.
A key whose JSON key holds `"` or `\` is refused too: SQLite's JSON path
has no escape for them.

Absent and null are no value. An instance that lacks any key of an index
is not in it, so any number of instances may lack a unique field, as SQL
lets several NULLs through a UNIQUE column, and a merge patch's null
removes the member anyway.

A field an index covers is never stored by hash in the instance's row:
the value store keeps it inline whatever its length (the inline set the
store passes to stow), so the index and a lookup compare the value
itself. An event still stores it by hash. publish puts back inline any
value a row holds by hash under a key a new index covers, before it
creates the index.

publish drops the indexes the live version has and the new one lacks,
and creates the new one's that the file lacks. A unique index whose
stored instances already share values refuses the publish
(IncompatibleChangeError) and creates nothing: @unique narrows what the
schema accepts across instances, which the compatibility rule decides
against the instances stored. Engine migration 10 creates the indexes of
every live version published before it.
*/

import { createHash } from 'node:crypto';

import type { Document, FieldDef } from '@superschematic/schema-ir/schema-file';

import { jsonKey, pointer, type SchemaModel } from '../registry/document.js';
import type { SchemaIssue } from '../errors.js';
import type { Storage } from '../storage/storage.js';
import { FieldSchemas } from '../tools/schema.js';
import { refHash, refsOf, refsText, valuesOf } from '../values/store.js';

/** The JSON type of a value an index compares: a field's, or a behavior filter's. */
export type ScalarType = 'string' | 'number' | 'integer' | 'boolean';

const SCALAR_TYPES: ReadonlySet<string> = new Set(['string', 'number', 'integer', 'boolean']);

/** An index of the instance type's own fields. */
export interface OwnIndex {
  /** Its SQL name: engine_unique_<digest> or engine_index_<digest>. */
  readonly name: string;
  readonly unique: boolean;
  /** The JSON keys of its fields, in order. */
  readonly keys: readonly string[];
  /** The names of its fields, in order, for messages. */
  readonly fields: readonly string[];
}

/** A top-level own field an index or a filter compares. */
export interface ScalarField {
  readonly key: string;
  readonly name: string;
  readonly type: ScalarType;
}

/**
 * scalarFields lists the instance type's own fields whose value is a
 * string, a number or a boolean, by JSON key: the ones an index may cover
 * and a list may filter on. A field whose JSON key a JSON path cannot
 * name is left out.
 */
export function scalarFields(document: Document, instanceType: string): Map<string, ScalarField> {
  const found = new Map<string, ScalarField>();
  const properties = new FieldSchemas(document).object(instanceType).properties;
  for (const field of (document.types ?? {})[instanceType]?.fields ?? []) {
    const key = jsonKey(field);
    const type = scalarTypeOf(field, properties.get(key)?.type);
    if (type !== undefined && keyIssue(key) === undefined) {
      found.set(key, { key, name: field.name, type });
    }
  }
  return found;
}

function scalarTypeOf(field: FieldDef, type: string | undefined): ScalarType | undefined {
  if (field.typeRef.isArray || field.typeRef.isMap || type === undefined || !SCALAR_TYPES.has(type)) {
    return undefined;
  }
  return type as ScalarType;
}

/** keyIssue says why a JSON path cannot name a key, or undefined when it can. */
export function keyIssue(key: string): string | undefined {
  return /["\\\u0000-\u001f]/.test(key) ? `its JSON key ${JSON.stringify(key)} holds a quote, a backslash or a control character, which a JSON path cannot name` : undefined;
}

/**
 * indexIssues lists what the engine refuses of a document's @unique, @key
 * and @index: one on a type other than the instance type, on a field that
 * holds no string, number or boolean, or naming a field the instance type
 * does not have.
 */
export function indexIssues(document: Document, instanceType: string): SchemaIssue[] {
  const issues: SchemaIssue[] = [];
  const types = document.types ?? {};
  const scalars = scalarFields(document, instanceType);
  for (const [typeName, type] of Object.entries(types)) {
    const own = typeName === instanceType;
    (type.fields ?? []).forEach((field, index) => {
      for (const decorator of ['unique', 'key'] as const) {
        if (field[decorator] !== true) {
          continue;
        }
        const at = `${pointer('types', typeName)}/fields/${index}/${decorator}`;
        const label = `field ${typeName}.${field.name} is @${decorator}`;
        if (!own) {
          issues.push({ path: at, message: `${label}, and the engine enforces @${decorator} on the instance type's own fields (${instanceType}) only` });
          continue;
        }
        const problem = keyIssue(jsonKey(field));
        if (problem !== undefined) {
          issues.push({ path: at, message: `${label}, and ${problem}` });
        } else if (!scalars.has(jsonKey(field))) {
          issues.push({
            path: at,
            message: `${label}, and a unique field holds a string, a number or a boolean: a list, an object or a JSON value has no one value to compare`,
          });
        }
      }
    });
    (type.indexes ?? []).forEach((declared, index) => {
      const at = `${pointer('types', typeName)}/indexes/${index}`;
      if (!own) {
        issues.push({ path: at, message: `type ${typeName} declares an index, and the engine indexes the instance type's own fields (${instanceType}) only` });
        return;
      }
      const keys = declared.keys ?? [];
      if (keys.length === 0) {
        issues.push({ path: `${at}/keys`, message: 'an index lists at least one field' });
      }
      if (new Set(keys).size !== keys.length) {
        issues.push({ path: `${at}/keys`, message: 'an index lists a field twice' });
      }
      keys.forEach((name, position) => {
        const field = (type.fields ?? []).find((candidate) => candidate.name === name);
        if (field === undefined) {
          issues.push({ path: `${at}/keys/${position}`, message: `the index names ${name}, which is not a field of ${instanceType}` });
          return;
        }
        const problem = keyIssue(jsonKey(field));
        if (problem !== undefined) {
          issues.push({ path: `${at}/keys/${position}`, message: `field ${instanceType}.${name}: ${problem}` });
        } else if (!scalars.has(jsonKey(field))) {
          issues.push({
            path: `${at}/keys/${position}`,
            message: `field ${instanceType}.${name} holds no string, number or boolean, and an index compares one value per field`,
          });
        }
      });
    });
  }
  return issues;
}

/**
 * ownIndexes lists the indexes of a version's instance type, named for
 * the namespace that holds the schema: one per @unique or @key field,
 * then one per entry of indexes, each set of keys once, a unique one
 * winning over a plain one on the same keys. Only what the engine takes
 * counts: a field or an entry indexIssues refuses, which a version
 * published before engine migration 10 may hold, is left out.
 */
export function ownIndexes(model: Pick<SchemaModel, 'name' | 'instanceType' | 'document'>, holder: string): OwnIndex[] {
  const type = (model.document.types ?? {})[model.instanceType];
  if (type === undefined) {
    return [];
  }
  const scalars = scalarFields(model.document, model.instanceType);
  const declared: Array<{ unique: boolean; fields: FieldDef[] }> = [];
  for (const field of type.fields ?? []) {
    if ((field.unique === true || field.key === true) && scalars.has(jsonKey(field))) {
      declared.push({ unique: true, fields: [field] });
    }
  }
  for (const entry of type.indexes ?? []) {
    const fields = (entry.keys ?? []).map((name) => (type.fields ?? []).find((field) => field.name === name));
    if (fields.length === 0 || fields.some((field) => field === undefined || !scalars.has(jsonKey(field))) || new Set(entry.keys).size !== fields.length) {
      continue;
    }
    declared.push({ unique: entry.unique === true, fields: fields as FieldDef[] });
  }
  const byKeys = new Map<string, OwnIndex>();
  for (const { unique, fields } of declared) {
    const keys = fields.map(jsonKey);
    const id = JSON.stringify(keys);
    const known = byKeys.get(id);
    if (known !== undefined && (known.unique || !unique)) {
      continue;
    }
    byKeys.set(id, { name: indexSqlName(holder, model.name, unique, keys), unique, keys, fields: fields.map((field) => field.name) });
  }
  return [...byKeys.values()];
}

// indexSqlName names an index by a digest of what it covers.
function indexSqlName(holder: string, schema: string, unique: boolean, keys: readonly string[]): string {
  const digest = createHash('sha256')
    .update([holder, schema, unique ? 'unique' : 'index', ...keys].join('\u0000'), 'utf8')
    .digest('hex')
    .slice(0, 16);
  return `${unique ? 'engine_unique' : 'engine_index'}_${digest}`;
}

/** sqlString writes a value as an SQL string literal. */
export function sqlString(value: string): string {
  return `'${value.replace(/'/g, "''")}'`;
}

/** valueExpression is the SQL expression of an own field's value in engine_instances.data. */
export function valueExpression(key: string): string {
  return `json_extract(data, ${sqlString(`$."${key}"`)})`;
}

/** refHashExpression is the SQL expression of the hash a ref under an own field names. */
export function refHashExpression(key: string): string {
  return `json_extract(data, ${sqlString(`$."${key}"."$value"`)})`;
}

/**
 * schemaRows is the condition on engine_instances an own index is partial
 * on, as SQL with literals, so a query that repeats it reaches the index:
 * the schema's rows in the namespace that holds it. Both are names the
 * engine checked (SCHEMA_NAME, NAMESPACE_NAME).
 */
export function schemaRows(holder: string, schema: string): string {
  return `schema = ${sqlString(schema)} AND schema_namespace = ${sqlString(holder)}`;
}

function createIndexSql(index: OwnIndex, holder: string, schema: string): string {
  const values = index.keys.map(valueExpression);
  return `CREATE ${index.unique ? 'UNIQUE ' : ''}INDEX "${index.name}" ON engine_instances (namespace, ${values.join(', ')})
    WHERE ${schemaRows(holder, schema)} AND ${values.map((value) => `${value} IS NOT NULL`).join(' AND ')}`;
}

/** indexExists reports whether the file holds an index of the name. */
export function indexExists(storage: Storage, name: string): boolean {
  return storage.get("SELECT 1 AS found FROM sqlite_master WHERE type = 'index' AND name = ?", [name]) !== undefined;
}

/** Instances of one namespace that share the values of a unique index. */
export interface UniqueClash {
  readonly index: OwnIndex;
  readonly namespace: string;
  /** The values they share, one per key. */
  readonly values: readonly unknown[];
  /** How many share them. */
  readonly count: number;
}

/**
 * uniqueClashes lists, for each unique index of after that before lacks,
 * up to three sets of values instances of the schema share, by namespace.
 * It reads the rows as stored: a value a row holds by hash compares as its
 * ref, so two instances that hold the same long value by hash clash.
 */
export function uniqueClashes(storage: Storage, holder: string, schema: string, before: readonly OwnIndex[], after: readonly OwnIndex[]): UniqueClash[] {
  const known = new Set(before.map((index) => index.name));
  return after.filter((index) => index.unique && !known.has(index.name)).flatMap((index) => clashesOf(storage, holder, schema, index));
}

function clashesOf(storage: Storage, holder: string, schema: string, index: OwnIndex): UniqueClash[] {
  const values = index.keys.map(valueExpression);
  const rows = storage.all(
    `SELECT namespace, ${values.map((value, position) => `${value} AS v${position}`).join(', ')}, COUNT(*) AS n
     FROM engine_instances
     WHERE ${schemaRows(holder, schema)} AND ${values.map((value) => `${value} IS NOT NULL`).join(' AND ')}
     GROUP BY namespace, ${values.join(', ')} HAVING COUNT(*) > 1
     ORDER BY namespace LIMIT 3`
  );
  return rows.map((row) => ({
    index,
    namespace: String(row.namespace),
    values: index.keys.map((_, position) => row[`v${position}`]),
    count: Number(row.n),
  }));
}

/**
 * syncOwnIndexes brings the file's indexes of a schema in line with a
 * version being published: it drops each index of before that after
 * lacks and creates each of after's that the file lacks, once the values
 * under its keys are inline in every row. It returns the clashes of a
 * unique index the stored instances break, and then creates nothing; the
 * caller refuses the publish, whose transaction rolls the rest back.
 */
export function syncOwnIndexes(storage: Storage, holder: string, schema: string, before: readonly OwnIndex[], after: readonly OwnIndex[]): UniqueClash[] {
  const kept = new Set(after.map((index) => index.name));
  for (const index of before) {
    if (!kept.has(index.name)) {
      storage.exec(`DROP INDEX IF EXISTS "${index.name}"`);
    }
  }
  const missing = after.filter((index) => !indexExists(storage, index.name));
  if (missing.length === 0) {
    return [];
  }
  inlineValues(storage, holder, schema, new Set(missing.flatMap((index) => index.keys)));
  const clashes = missing.filter((index) => index.unique).flatMap((index) => clashesOf(storage, holder, schema, index));
  if (clashes.length > 0) {
    return clashes;
  }
  for (const index of missing) {
    storage.exec(createIndexSql(index, holder, schema));
  }
  return [];
}

// inlineValues puts back, in every row of the schema, the value of each
// key the row holds by hash: an index compares values, and a row of a
// field an index covers keeps its value inline from now on. Only those
// members change; the row keeps its other refs and holds their values.
function inlineValues(storage: Storage, holder: string, schema: string, keys: ReadonlySet<string>): void {
  const pointers = new Set([...keys].map((key) => pointer(key)));
  const values = valuesOf(storage);
  const rows = storage.all(
    `SELECT namespace, id, data, value_refs FROM engine_instances WHERE ${schemaRows(holder, schema)} AND value_refs IS NOT NULL`
  );
  for (const row of rows) {
    const refs = refsOf(row.value_refs) ?? [];
    const filled = refs.filter((at) => pointers.has(at));
    if (filled.length === 0) {
      continue;
    }
    const kept = refs.filter((at) => !pointers.has(at));
    const data = values.fill(JSON.parse(String(row.data)) as Record<string, unknown>, filled, true);
    storage.run('UPDATE engine_instances SET data = ?, value_refs = ? WHERE namespace = ? AND schema = ? AND id = ?', [
      JSON.stringify(data),
      refsText(kept),
      String(row.namespace),
      schema,
      String(row.id),
    ]);
    values.hold(
      { namespace: String(row.namespace), schema, holder: 'instance', id: String(row.id), key: '' },
      new Set(kept.map((at) => refHash(memberAt(data, at)) as string))
    );
  }
}

// memberAt is the member a top-level pointer names.
function memberAt(data: Readonly<Record<string, unknown>>, at: string): unknown {
  return data[at.slice(1).replace(/~1/g, '/').replace(/~0/g, '~')];
}

/**
 * clashMessage says what a clash is, naming its namespace and values only
 * where holder, the namespace that publishes, holds the instances.
 */
export function clashMessage(clash: UniqueClash, holder: string, instanceType: string): string {
  const what =
    clash.index.fields.length === 1
      ? `field ${instanceType}.${clash.index.fields[0]} becomes unique`
      : `the unique index on ${joinAnd(clash.index.fields)} of ${instanceType} is added`;
  if (clash.namespace !== holder) {
    return `${what}, and instances of a namespace that looks names up in ${holder} share a value`;
  }
  const values = clash.index.fields.map((field, position) => `${field} ${shown(clash.values[position])}`);
  return `${what}, and ${clash.count} instances in namespace ${clash.namespace} hold ${joinAnd(values)}`;
}

/** shown writes a value for a message, cut short past 80 characters. */
export function shown(value: unknown): string {
  const text = typeof value === 'string' && value.startsWith('{"$value"') ? 'a long value stored by hash' : JSON.stringify(value);
  return text.length > 80 ? `${text.slice(0, 77)}...` : text;
}

function joinAnd(items: readonly string[]): string {
  return items.length < 2 ? items.join('') : `${items.slice(0, -1).join(', ')} and ${items[items.length - 1]}`;
}

/** UNIQUE_FAILED reads the index a unique constraint failure names. */
const UNIQUE_FAILED = /UNIQUE constraint failed: index '([A-Za-z0-9_]+)'/;

/** failedIndex is the name of the index a SQLite unique constraint failure names, or undefined. */
export function failedIndex(message: string): string | undefined {
  return UNIQUE_FAILED.exec(message)?.[1];
}
