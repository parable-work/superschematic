/*
Branches, the core's version graph on each instance (D32, over D17 and
D19). A schema that composes it makes each instance a graph's root: refs,
a primary line and drafts of it, over rows of the kinds the config names;
commits, merges, rebases and reverts; and a release pointer, whose history
is the release log. The root is never overlaid and is in no commit, so the
instance's own fields stay outside the graph.

The config names the kinds: `kinds: { <kind>: { type, parent?, order?,
singleton?, units?, retentionDays? } }`, each kind's content the fields of
another type of the schema, which parseConfig reads through
ConfigTarget.types, so the compatibility rule keeps them as it keeps the
instance type's. parseConfig derives the graph's descriptor (version 3)
from the config and the types: a kind's role and audit columns have fixed
names (ROLE_COLUMNS), a field may not take one, and each field's value
class follows graphdesc's rule for a compiled field: a primitive's from
its name, an enum's `enum`, a type's `json`, a catalog scalar's the
catalog's (BUILTIN_SCALAR_VALUE_CLASSES), a scalar the document defines
the one its JSON type gives, and each list level adds `[]`. A kind's
images exclude nothing and its actor column is updated_by. The core
checks the descriptor there, so a config it refuses fails the define.

The operations are of instance scope. branch, save, commit, seal, merge,
rebase, revert, release and discard write; refs, releases, compose,
materialize, released, diff and history read. Each takes what the version
graph engine's operation takes, refs and commits by id, and every write
through a ref its expected version; a ref or a commit that is not the
instance's is invalid_argument at the parameter that names it, as a
proposal Revisions does not hold is. Who may call one is the access
policy's call, asked write or read with the operation's name, and each
writing one appends the instance's operation event. save holds each row's
content to its kind's type with validate(type, value), and each value to
its column's value class, before the engine sees it; a merge or a rebase
holds each resolution's value to the same, and the row it leaves to the
type. discard refuses the primary line (primary_line), which every draft
branches from and merges into. The engine's refusals are vetoes with its
stable codes (VETOES), released before the instance's first release is
not_found, and refs and releases read the behavior's own tables, which
the engine has no operation for.

The graph's tables are the behavior's own: the SQLite adapter's fixed
layout (sqliteLayout) under sql.table's names, which its migration
creates, beside roots, which maps each root to its instance, and actors,
which maps each actor to its subject. The adapter reaches them through the
call's sql (clientOf): one statement at a time, on the behavior's own
tables, with no trigger and no transaction control, in the transaction of
the operation that runs it, so an invoked operation's savepoint rolls the
graph back with the rest. A graph is named by its namespace and schema, so
the tables hold every schema's graphs; a root's id is the version-5 UUID
of its instance's id in ROOT_NAMESPACE, since an instance id is any
string, and an actor the version-5 UUID of the principal's subject in
ACTOR_NAMESPACE, which the behavior records so its reads return
subjects. The engine's core is instantiated with initSync when the first
schema that composes Branches is composed.

initialize creates an instance's primary line, named by primary (main by
default). An instance created before its schema composed Branches gets
its primary line at its first write, in that write's transaction, through
afterChange: an update that changes it or a writing operation of another
behavior. A Branches operation records the root first too, but each
names a ref or a commit the instance has none of yet, so it is refused
and rolls the line back. Deleting an instance deletes its graph.

With sweep in the config, the sweep schedule runs on the schema: each run
discards the drafts idle past abandonAfter by invoking discard on each
instance, so each runs its guards and appends its event, then runs the
engine's sweep with abandoning off, which deletes the rows of refs
discarded past the grace, prunes history past each kind's retention and
writes missing snapshots: writes to the behavior's own tables that change
nothing an operation returns. Without sweep the schedule is off there.

configChange: a new version may add a kind, change a kind's fields as the
compatibility rule allows a field to change, and change a retention,
primary, snapshotEvery and sweep. Removing a kind, or changing a kind's
type, parent, order, singleton or a field's unit, is refused. Branches can
be added to a schema that has instances and cannot be removed from one:
the graphs would stay behind with nothing to delete them. The schema
epoch stays 0, since every version reads every stored row.
*/

import { createHash } from 'node:crypto';

import { BUILTIN_SCALARS, BUILTIN_SCALAR_VALUE_CLASSES } from '@superschematic/schema-runtime';
import { initSync, VersionGraphError, type VersionGraph } from '@superschematic/versiongraph';
import {
  CanonicalError,
  canonicalValue,
  EngineError as GraphError,
  InvalidTreeError,
  NotFoundError,
  SyncEngine,
  VersionConflictError,
  uuidCanonical,
  type Change,
  type Commit,
  type Conflict,
  type Edits,
  type KindEdits,
  type Resolution,
  type Tree,
  type TreeResult,
} from '@superschematic/versiongraph/engine';
import { SqliteAdapter, microsToDateTime, sqliteLayout, type SqliteClient } from '@superschematic/versiongraph/sqlite';

import { BehaviorVetoError, EngineError, OperationParamsError, type SchemaIssue, type ValidationIssue } from '../../errors.js';
import { jsonEqual } from '../../instances/patch.js';
import type { Row, SqlValue } from '../../storage/driver.js';
import {
  BehaviorConfigError,
  defineBehavior,
  type BehaviorScope,
  type ConfigTarget,
  type ConfigTypeField,
  type FrozenJSON,
  type InstanceContext,
  type SqlReader,
  type SqlWriter,
} from '../behavior.js';
import declaration from './declarations/Branches.behavior.json' with { type: 'json' };
import { DEFAULT_PAGE_SIZE } from '../../paging.js';
import { page, pageRequest } from '../paging.js';

/** The namespace an actor is the version-5 UUID of a principal's subject in. */
export const ACTOR_NAMESPACE = 'e5df4b2e-719d-4507-8d3a-9474ef4afe81';

/** The namespace a root's id is the version-5 UUID of its instance's id in. */
export const ROOT_NAMESPACE = 'cc634206-0f43-4e6c-a60f-65184f26131e';

/** The name of each instance's primary line when the config gives none. */
export const DEFAULT_PRIMARY = 'main';

/** A kind's role and audit columns, by name, with their value classes: a field of its type may not take one of these keys. */
export const ROLE_COLUMNS: Readonly<Record<string, string>> = Object.freeze({
  id: 'uuid',
  entity_key: 'uuid',
  ref_id: 'uuid',
  root_id: 'uuid',
  deleted_on_ref: 'boolean',
  _version: 'integer',
  created_at: 'dateTime',
  created_by: 'uuid',
  updated_at: 'dateTime',
  updated_by: 'uuid',
});

/** A field's conflict unit, as @conflictUnit sets it. */
export type BranchesUnit = 'atomic' | 'keyed' | 'jsonSchema' | 'excluded';

/** One kind of the graph, as the config gives it, with what parseConfig read of its type. */
export interface BranchesKind {
  /** The type of the schema document whose fields are the kind's content. */
  readonly type: string;
  readonly parent?: { readonly key: string; readonly of: string };
  readonly order?: string;
  readonly singleton: boolean;
  /** The units the config sets, by field; a field it does not name is atomic. */
  readonly units: Readonly<Record<string, BranchesUnit>>;
  readonly retentionDays?: number;
  /** The JSON keys of the type's fields, in the document's order: what a saved row holds beside entity_key. */
  readonly fields: readonly string[];
  /** Each field's value class, by JSON key. */
  readonly classes: Readonly<Record<string, string>>;
}

/** When the sweep runs on a schema, and what it keeps. */
export interface BranchesSweep {
  readonly intervalMs: number;
  readonly discardGrace?: number;
  readonly pruneBatch?: number;
  readonly abandonAfter?: number;
}

/** Branches' config, as parseConfig returns it. */
export interface BranchesConfig {
  readonly kinds: Readonly<Record<string, BranchesKind>>;
  readonly primary: string;
  readonly snapshotEvery: number;
  readonly sweep?: BranchesSweep;
  /** The graph's descriptor, version 3, as JSON text. */
  readonly descriptor: string;
}

/** A ref, as the operations return it. */
export interface BranchRef {
  readonly id: string;
  readonly name: string;
  readonly parent: string | null;
  readonly base: string | null;
  readonly head: string | null;
  readonly sealed: boolean;
  readonly discarded: boolean;
  readonly version: number;
  readonly createdAt: string;
  readonly createdBy: string;
  readonly updatedAt: string;
  readonly updatedBy: string;
}

/** A commit, as the operations return it. */
export interface BranchCommit {
  readonly id: string;
  readonly ref: string;
  readonly parent: string | null;
  readonly message: string;
  readonly sequence: number | null;
  readonly contentHash: string;
  readonly createdAt: string;
  readonly createdBy: string;
  readonly snapshot: boolean;
}

/** One version of the release pointer, as releases returns it. */
export interface BranchRelease {
  readonly version: number;
  readonly commit: string;
  readonly releasedAt: string;
  readonly releasedBy: string;
}

// The version graph engine's refusals a caller can meet, as vetoes with
// its stable codes, which the declaration lists.
const VETOES: ReadonlySet<string> = new Set(declaration.vetoes.map((veto) => veto.code));

// A primitive's value class, by the names the schema runtime reads.
const PRIMITIVE_CLASSES: Readonly<Record<string, string>> = {
  string: 'string',
  String: 'string',
  ID: 'string',
  Int: 'integer',
  number: 'number',
  Float: 'number',
  boolean: 'boolean',
  Boolean: 'boolean',
};

// The value class of a scalar the document defines, by the JSON type of its values.
const JSON_TYPE_CLASSES: Readonly<Record<string, string>> = {
  string: 'string',
  integer: 'integer',
  number: 'number',
  boolean: 'boolean',
  object: 'json',
  array: 'json',
  any: 'json',
};

// The columns of a kind no merge compares or hash covers: the root's and
// the audit columns but the author, updated_by, which conflicts report.
const EXCLUDED_ROLES = ['created_at', 'created_by', 'root_id', 'updated_at'];

const hasOwn = (object: object, key: string): boolean => Object.prototype.hasOwnProperty.call(object, key);

/** The version-5 UUID (RFC 9562) of name in namespace, in its canonical form. */
export function uuidV5(namespace: string, name: string): string {
  const bytes = createHash('sha1').update(Buffer.from(namespace.replace(/-/g, ''), 'hex')).update(name, 'utf8').digest().subarray(0, 16);
  bytes[6] = (bytes[6] & 0x0f) | 0x50;
  bytes[8] = (bytes[8] & 0x3f) | 0x80;
  const hex = bytes.toString('hex');
  return uuidCanonical(`${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`);
}

let instantiated: VersionGraph | undefined;

// graphCore is the version graph's core, instantiated synchronously the
// first time a schema that composes Branches is composed or run.
function graphCore(): VersionGraph {
  instantiated ??= initSync();
  return instantiated;
}

// valueClass is a field's value class, or undefined when no rule gives it
// one: a catalog scalar the layout has no class for (a vector, a point).
function valueClass(field: ConfigTypeField): string | undefined {
  let element: string | undefined;
  switch (field.kind) {
    case 'primitive':
      element = PRIMITIVE_CLASSES[field.type];
      break;
    case 'enum':
      element = 'enum';
      break;
    case 'type':
      element = 'json';
      break;
    case 'scalar': {
      // A catalog scalar is the catalog's whatever the document declares
      // for it, as the engine validates it; any other is the document's.
      const key = field.type.replace(/\./g, '_');
      element = hasOwn(BUILTIN_SCALARS, key)
        ? (BUILTIN_SCALAR_VALUE_CLASSES[key] as string | undefined)
        : JSON_TYPE_CLASSES[field.jsonType ?? ''];
      break;
    }
  }
  return element === undefined ? undefined : element + '[]'.repeat(field.depth);
}

interface RawKind {
  readonly type: string;
  readonly parent?: { readonly key: string; readonly of: string };
  readonly order?: string;
  readonly singleton?: boolean;
  readonly units?: Readonly<Record<string, BranchesUnit>>;
  readonly retentionDays?: number;
}

interface RawConfig {
  readonly kinds: Readonly<Record<string, RawKind>>;
  readonly primary?: string;
  readonly snapshotEvery?: number;
  readonly sweep?: BranchesSweep;
}

// kindOf reads a kind's type and checks the kind against it.
function kindOf(name: string, raw: RawKind, config: RawConfig, target: ConfigTarget): BranchesKind {
  const at = `kind ${name}`;
  const type = target.types.get(raw.type);
  if (type === undefined) {
    throw new BehaviorConfigError(
      `${at}: ${raw.type} is not a type of ${target.schema} besides ${target.type} (its types: ${target.types.names.join(', ') || 'none'})`
    );
  }
  const classes: Record<string, string> = {};
  for (const field of type.fields) {
    if (hasOwn(ROLE_COLUMNS, field.key)) {
      throw new BehaviorConfigError(
        `${at}: ${type.name}.${field.key} takes the name of a column every row of a kind has (${Object.keys(ROLE_COLUMNS).join(', ')})`
      );
    }
    const valueClassOf = valueClass(field);
    if (valueClassOf === undefined) {
      throw new BehaviorConfigError(`${at}: ${type.name}.${field.key} is of ${field.type}, which has no value class a graph's row can hold`);
    }
    classes[field.key] = valueClassOf;
  }
  const roleField = (role: string, key: string, want: string): void => {
    if (!hasOwn(classes, key)) {
      throw new BehaviorConfigError(`${at}: its ${role} ${key} is not a field of ${type.name}`);
    }
    if (classes[key] !== want) {
      throw new BehaviorConfigError(`${at}: its ${role} ${key} holds ${want === 'uuid' ? "a parent row's entity key, a UUID" : 'an integer'}, not a value of class ${classes[key]}`);
    }
  };
  if (raw.parent !== undefined) {
    if (!hasOwn(config.kinds, raw.parent.of)) {
      throw new BehaviorConfigError(`${at}: its parent ${raw.parent.of} is not a kind of the config (its kinds: ${Object.keys(config.kinds).sort().join(', ')})`);
    }
    roleField('parent key', raw.parent.key, 'uuid');
  }
  if (raw.order !== undefined) {
    roleField('order', raw.order, 'integer');
  }
  for (const [key, unit] of Object.entries(raw.units ?? {})) {
    if (!hasOwn(classes, key)) {
      throw new BehaviorConfigError(`${at}: units names ${key}, which is not a field of ${type.name}`);
    }
    if ((unit === 'keyed' || unit === 'jsonSchema') && classes[key] !== 'json') {
      throw new BehaviorConfigError(`${at}: a ${unit} unit merges a JSON object, and ${key} holds a value of class ${classes[key]}`);
    }
    if (unit === 'excluded' && (key === raw.order || key === raw.parent?.key)) {
      throw new BehaviorConfigError(`${at}: ${key} is its ${key === raw.order ? 'order' : 'parent key'}, which is content`);
    }
  }
  return {
    type: type.name,
    ...(raw.parent === undefined ? {} : { parent: { key: raw.parent.key, of: raw.parent.of } }),
    ...(raw.order === undefined ? {} : { order: raw.order }),
    singleton: raw.singleton === true,
    units: { ...(raw.units ?? {}) },
    ...(raw.retentionDays === undefined ? {} : { retentionDays: raw.retentionDays }),
    fields: type.fields.map((typeField) => typeField.key),
    classes,
  };
}

// descriptorOf is the graph's descriptor: each kind's fixed role and audit
// columns beside its type's fields, as graphdesc writes a compiled
// member's, with history that excludes nothing and names updated_by.
function descriptorOf(schema: string, kinds: Readonly<Record<string, BranchesKind>>): string {
  return JSON.stringify({
    version: 3,
    graph: schema,
    root: { table: schema, key: 'id' },
    refTable: 'ref',
    commitTable: 'commit',
    patchTable: 'patch',
    releaseTable: 'release',
    snapshotTable: 'snapshot_entry',
    kinds: Object.keys(kinds)
      .sort()
      .map((name) => {
        const kind = kinds[name];
        const units: Record<string, string> = {};
        const excluded = [...EXCLUDED_ROLES];
        for (const [field, unit] of Object.entries(kind.units)) {
          if (unit === 'keyed' || unit === 'jsonSchema') {
            units[field] = unit;
          } else if (unit === 'excluded') {
            excluded.push(field);
          }
        }
        return {
          kind: name,
          table: name,
          historyTable: `${name}_history`,
          key: 'entity_key',
          id: 'id',
          ref: 'ref_id',
          root: 'root_id',
          tombstone: 'deleted_on_ref',
          version: '_version',
          author: 'updated_by',
          ...(kind.parent === undefined ? {} : { parent: { key: kind.parent.key, kind: kind.parent.of } }),
          ...(kind.order === undefined ? {} : { order: kind.order }),
          ...(kind.singleton ? { singleton: true } : {}),
          ...(Object.keys(units).length === 0 ? {} : { units }),
          excluded: excluded.sort(),
          history: { ...(kind.retentionDays === undefined ? {} : { retentionDays: kind.retentionDays }), exclude: [], actor: 'updated_by' },
          columns: { ...ROLE_COLUMNS, ...kind.classes },
        };
      }),
  });
}

/** The tables the behavior reads itself, by its own names for them. */
interface Tables {
  readonly ref: string;
  readonly refHistory: string;
  readonly commit: string;
  readonly patch: string;
  readonly snapshot: string;
  readonly release: string;
  readonly releaseHistory: string;
  readonly member: string;
  readonly memberHistory: string;
  readonly roots: string;
  readonly actors: string;
}

function tablesOf(sql: SqlReader): Tables {
  return {
    ref: sql.table('ref'),
    refHistory: sql.table('ref_history'),
    commit: sql.table('commit'),
    patch: sql.table('patch'),
    snapshot: sql.table('snapshot_entry'),
    release: sql.table('release'),
    releaseHistory: sql.table('release_history'),
    member: sql.table('member'),
    memberHistory: sql.table('member_history'),
    roots: sql.table('roots'),
    actors: sql.table('actors'),
  };
}

// clientOf bridges the adapter's synchronous client to the call's sql,
// whose checks every statement the adapter runs passes: one statement, on
// the behavior's own tables, with no trigger and no transaction control.
// In a read run() refuses, and the adapter's reads run none.
function clientOf(sql: SqlReader): SqliteClient {
  return {
    run: (text, params = []) => (sql as SqlWriter).run(text, params as SqlValue[]),
    get: (text, params = []) => sql.get(text, params as SqlValue[]),
    all: (text, params = []) => sql.all(text, params as SqlValue[]),
  };
}

/** The graph of a call's schema in its namespace, as the behavior's own statements read it. */
interface Store {
  /** The graph's name: its namespace and schema. */
  readonly name: string;
  readonly tables: Tables;
  readonly sql: SqlReader;
}

/** A call's graph, with the engine over the call's sql. */
interface Graph extends Store {
  readonly engine: SyncEngine;
}

type Scope = BehaviorScope<BranchesConfig> & { readonly sql: SqlReader };

function storeOf(scope: Scope): Store {
  return { name: `${scope.namespace}/${scope.schema}`, tables: tablesOf(scope.sql), sql: scope.sql };
}

// graphOf is the graph of the call's schema in its namespace, over the
// call's sql, at the call's time.
function graphOf(scope: Scope): Graph {
  const store = storeOf(scope);
  const adapter = new SqliteAdapter(scope.config.descriptor, {
    graph: store.name,
    tableName: (local) => scope.sql.table(local),
    clock: () => scope.now * 1000,
    callerTransaction: true,
  });
  const engine = new SyncEngine(graphCore(), scope.config.descriptor, adapter.storage(clientOf(scope.sql)), {
    snapshotEvery: scope.config.snapshotEvery,
    schemaEpoch: 0,
  });
  return { ...store, engine };
}

/** rootOf is the root id of an instance. */
function rootOf(id: string): string {
  return uuidV5(ROOT_NAMESPACE, id);
}

// actorOf is the actor of the call's principal, recorded with its subject
// so a read returns the subject.
function actorOf(scope: Scope): string {
  const subject = scope.principal.subject;
  const actor = uuidV5(ACTOR_NAMESPACE, subject);
  (scope.sql as SqlWriter).run(`INSERT INTO ${scope.sql.table('actors')} (actor, subject) VALUES (?, ?) ON CONFLICT (actor) DO NOTHING`, [actor, subject]);
  return actor;
}

// ensureRoot records the instance as a root and creates its primary line,
// once: at its create, or at the first write of one created before its
// schema composed Branches. It returns the call's graph.
function ensureRoot(context: InstanceContext<BranchesConfig>): Graph {
  const store = storeOf(context);
  const root = rootOf(context.id);
  const known = context.sql.get(`SELECT 1 AS found FROM ${store.tables.roots} WHERE graph = ? AND root_id = ?`, [store.name, root]) !== undefined;
  const graph = graphOf(context);
  if (!known) {
    context.sql.run(`INSERT INTO ${store.tables.roots} (graph, root_id, id) VALUES (?, ?, ?)`, [store.name, root, context.id]);
    graph.engine.createPrimary(actorOf(context), root, context.config.primary);
  }
  return graph;
}

// isRoot reports whether the instance has been recorded as a root.
function isRoot(context: InstanceContext<BranchesConfig>): boolean {
  const store = storeOf(context);
  return context.sql.get(`SELECT 1 AS found FROM ${store.tables.roots} WHERE graph = ? AND root_id = ?`, [store.name, rootOf(context.id)]) !== undefined;
}

// canonicalID reads a UUID parameter, in either form, as its canonical form.
function canonicalID(operation: string, path: string, value: unknown): string {
  try {
    return uuidCanonical(String(value));
  } catch (error) {
    if (error instanceof CanonicalError) {
      throw new OperationParamsError('Branches', operation, [{ path, message: `${JSON.stringify(value)} is not a UUID` }]);
    }
    throw error;
  }
}

// ownRef reads a parameter that names a live ref of the instance's graph.
function ownRef(context: Scope & { readonly id: string }, graph: Store, operation: string, name: string, value: unknown): string {
  const id = canonicalID(operation, `/${name}`, value);
  const row = context.sql.get(`SELECT root_id FROM ${graph.tables.ref} WHERE graph = ? AND id = ? AND deleted_at IS NULL`, [graph.name, id]);
  if (row === undefined || row.root_id !== rootOf(context.id)) {
    throw new OperationParamsError('Branches', operation, [{ path: `/${name}`, message: `${context.schema} ${context.id} has no ref ${String(value)}` }]);
  }
  return id;
}

// ownCommit reads a parameter that names a commit of the instance's graph.
function ownCommit(context: Scope & { readonly id: string }, graph: Store, operation: string, name: string, value: unknown): string {
  const id = canonicalID(operation, `/${name}`, value);
  const row = context.sql.get(`SELECT root_id FROM ${graph.tables.commit} WHERE graph = ? AND id = ?`, [graph.name, id]);
  if (row === undefined || row.root_id !== rootOf(context.id)) {
    throw new OperationParamsError('Branches', operation, [{ path: `/${name}`, message: `${context.schema} ${context.id} has no commit ${String(value)}` }]);
  }
  return id;
}

// refusal turns what the version graph engine refuses into the refusal a
// caller of the operation gets: a veto with the engine's code, not_found,
// or invalid_argument for a resolution or a value it cannot read.
function refusal(context: Scope & { readonly id: string }, operation: string, error: unknown): unknown {
  const veto = (code: string, reason: string, details?: Record<string, unknown>) =>
    new BehaviorVetoError('Branches', operation, context.schema, context.id, { reason, code, ...(details === undefined ? {} : { details }) });
  if (error instanceof VersionConflictError) {
    return veto('version_conflict', 'the ref or the release pointer is no longer at the version the call names');
  }
  if (error instanceof InvalidTreeError) {
    return veto('invalid_tree', error.message, { findings: error.findings.map((finding) => ({ ...finding })) });
  }
  if (error instanceof NotFoundError) {
    return new EngineError('not_found', `${context.schema} ${context.id}: ${error.message}`);
  }
  if (error instanceof GraphError && VETOES.has(error.code)) {
    return veto(error.code, error.message);
  }
  if (error instanceof VersionGraphError && (error.code === 'invalid_resolution' || error.code === 'unmatched_resolution')) {
    return new OperationParamsError('Branches', operation, [{ path: '/resolutions', message: error.message }]);
  }
  if (error instanceof CanonicalError) {
    return new OperationParamsError('Branches', operation, [{ path: '', message: error.message }]);
  }
  return error;
}

// guarded runs an operation's work, turning the engine's refusals into the
// operation's.
function guarded<T>(context: Scope & { readonly id: string }, operation: string, work: () => T): T {
  try {
    return work();
  } catch (error) {
    throw refusal(context, operation, error);
  }
}

// subjectsOf maps actors to the subjects they were recorded for; an actor
// with no record maps to itself.
function subjectsOf(graph: Store, actors: Iterable<string>): (actor: unknown) => string {
  const wanted = [...new Set([...actors].filter((actor) => typeof actor === 'string'))];
  const subjects = new Map<string, string>();
  if (wanted.length > 0) {
    for (const row of graph.sql.all(`SELECT actor, subject FROM ${graph.tables.actors} WHERE actor IN (SELECT value FROM json_each(?))`, [
      JSON.stringify(wanted),
    ])) {
      subjects.set(String(row.actor), String(row.subject));
    }
  }
  return (actor) => (typeof actor === 'string' ? (subjects.get(actor) ?? actor) : String(actor));
}

const AUTHORS = ['created_by', 'updated_by'];

// rowsOf reads canonical rows, each actor as its subject.
function rowsOf(graph: Store, texts: readonly string[]): Array<Record<string, unknown>> {
  const rows = texts.map((text) => JSON.parse(text) as Record<string, unknown>);
  const subject = subjectsOf(
    graph,
    rows.flatMap((row) => AUTHORS.map((column) => row[column] as string))
  );
  for (const row of rows) {
    for (const column of AUTHORS) {
      if (typeof row[column] === 'string') {
        row[column] = subject(row[column]);
      }
    }
  }
  return rows;
}

// treeOf reads a tree, kind by kind, each actor as its subject.
function treeOf(graph: Store, tree: Tree): Record<string, Array<Record<string, unknown>>> {
  const out: Record<string, Array<Record<string, unknown>>> = {};
  for (const kind of Object.keys(tree).sort()) {
    out[kind] = rowsOf(graph, tree[kind]);
  }
  return out;
}

function treeResult(graph: Store, result: TreeResult): Record<string, unknown> {
  return { tree: treeOf(graph, result.tree), contentHash: result.contentHash, findings: result.findings.map((finding) => ({ ...finding })) };
}

const REF_COLUMNS = 'id, name, parent_ref_id, base_commit_id, head_commit_id, sealed_at, deleted_at, _version, created_at, created_by, updated_at, updated_by';

function refsOf(graph: Store, rows: readonly Row[]): BranchRef[] {
  const subject = subjectsOf(
    graph,
    rows.flatMap((row) => [String(row.created_by), String(row.updated_by)])
  );
  const optional = (value: SqlValue | undefined): string | null => (value === null || value === undefined ? null : String(value));
  return rows.map((row) => ({
    id: String(row.id),
    name: String(row.name),
    parent: optional(row.parent_ref_id),
    base: optional(row.base_commit_id),
    head: optional(row.head_commit_id),
    sealed: row.sealed_at !== null,
    discarded: row.deleted_at !== null,
    version: Number(row._version),
    createdAt: microsToDateTime(Number(row.created_at)),
    createdBy: subject(row.created_by),
    updatedAt: microsToDateTime(Number(row.updated_at)),
    updatedBy: subject(row.updated_by),
  }));
}

// refOf reads a ref of the graph as the operations return it.
function refOf(graph: Store, id: string): BranchRef {
  const row = graph.sql.get(`SELECT ${REF_COLUMNS} FROM ${graph.tables.ref} WHERE graph = ? AND id = ?`, [graph.name, id]) as Row;
  return refsOf(graph, [row])[0];
}

function commitsOf(graph: Store, commits: ReadonlyArray<Commit | null>): Array<BranchCommit | null> {
  const subject = subjectsOf(
    graph,
    commits.flatMap((commit) => (commit === null ? [] : [commit.createdBy]))
  );
  return commits.map((commit) =>
    commit === null
      ? null
      : {
          id: commit.id,
          ref: commit.ref,
          parent: commit.parent,
          message: commit.message,
          sequence: commit.sequence,
          contentHash: commit.contentHash,
          createdAt: commit.createdAt,
          createdBy: subject(commit.createdBy),
          snapshot: commit.snapshot,
        }
  );
}

function conflictsOf(graph: Store, conflicts: readonly Conflict[]): Array<Record<string, unknown>> {
  const author = (text: string | undefined): unknown => (text === undefined ? undefined : JSON.parse(text));
  const subject = subjectsOf(
    graph,
    conflicts.flatMap((conflict) => [author(conflict.oursAuthor), author(conflict.theirsAuthor)] as string[])
  );
  return conflicts.map((conflict) => {
    const out: Record<string, unknown> = { kind: conflict.kind, entityKey: conflict.entityKey, path: conflict.path };
    for (const side of ['base', 'ours', 'theirs'] as const) {
      if (conflict[side] !== undefined) {
        out[side] = JSON.parse(conflict[side]);
      }
    }
    for (const side of ['oursAuthor', 'theirsAuthor'] as const) {
      const value = author(conflict[side]);
      if (typeof value === 'string') {
        out[side] = subject(value);
      }
    }
    return out;
  });
}

// refsCursor reads refs' cursor: the key of the last ref of the page
// before, (whether it is a draft, its creation time, its id); the key
// before every ref's for the first page.
function refsCursor(cursor: string | undefined): [number, number, string] {
  if (cursor === undefined) {
    return [-1, 0, ''];
  }
  let key: unknown;
  try {
    key = JSON.parse(Buffer.from(cursor, 'base64url').toString('utf8'));
  } catch {
    key = undefined;
  }
  if (!Array.isArray(key) || key.length !== 3 || (key[0] !== 0 && key[0] !== 1) || !Number.isSafeInteger(key[1]) || typeof key[2] !== 'string') {
    throw new OperationParamsError('Branches', 'refs', [{ path: '/cursor', message: 'is not a cursor this operation returned' }]);
  }
  return key as [number, number, string];
}

// pointer turns a validation issue's path (`steps[0].name`) into a JSON
// pointer under base.
function pointer(base: string, path: string): string {
  let out = base;
  for (const match of path.matchAll(/([^.[\]]+)|\[(\d+)\]/g)) {
    out += `/${token(match[1] ?? match[2])}`;
  }
  return out;
}

// token escapes one reference token of a JSON pointer.
function token(key: string): string {
  return key.replace(/~/g, '~0').replace(/\//g, '~1');
}

// classIssues holds each field's value to its column's value class, as
// the adapter canonicalizes it, so a value no class rule reads is refused
// at its field rather than when the engine writes it.
function classIssues(kind: BranchesKind, content: Readonly<Record<string, unknown>>, at: string): SchemaIssue[] {
  const issues: SchemaIssue[] = [];
  for (const field of kind.fields) {
    if (!hasOwn(content, field) || content[field] === null || content[field] === undefined) {
      continue;
    }
    try {
      canonicalValue(kind.classes[field], JSON.stringify(content[field]));
    } catch (error) {
      if (!(error instanceof CanonicalError)) {
        throw error;
      }
      issues.push({ path: `${at}/${token(field)}`, message: `is no ${kind.classes[field]} value: ${error.detail}` });
    }
  }
  return issues;
}

// editsOf checks a save's edits and returns them as the engine takes them:
// each upsert row's content held to its kind's type, entity keys
// canonical, and each field the row leaves out null, so a row is its
// entity's whole content.
function editsOf(context: InstanceContext<BranchesConfig>, raw: FrozenJSON): Edits {
  const issues: SchemaIssue[] = [];
  const edits: Record<string, KindEdits> = {};
  const kinds = context.config.kinds;
  for (const [name, given] of Object.entries(raw as Record<string, { upsert?: unknown[]; delete?: unknown[]; unset?: unknown[] }>)) {
    const at = pointer('/edits', name);
    const kind = kinds[name];
    if (kind === undefined) {
      issues.push({ path: at, message: `${name} is not a kind of the graph (its kinds: ${Object.keys(kinds).sort().join(', ')})` });
      continue;
    }
    const upsert: string[] = [];
    (given.upsert ?? []).forEach((value, index) => {
      const rowAt = `${at}/upsert/${index}`;
      const { entity_key: key, ...content } = value as Record<string, unknown>;
      const refused = context.validate(kind.type, content) as ValidationIssue[];
      for (const issue of refused) {
        issues.push({ path: pointer(rowAt, issue.path), message: issue.message });
      }
      // A value the type accepts may still be one its column's class
      // refuses (1.5 of a scalar whose JSON type is integer).
      if (refused.length === 0) {
        issues.push(...classIssues(kind, content, rowAt));
      }
      const row: Record<string, unknown> = {};
      if (key !== undefined && key !== null) {
        try {
          row.entity_key = uuidCanonical(String(key));
        } catch {
          issues.push({ path: `${rowAt}/entity_key`, message: `${JSON.stringify(key)} is not a UUID` });
        }
      }
      for (const field of kind.fields) {
        row[field] = hasOwn(content, field) ? content[field] : null;
      }
      upsert.push(JSON.stringify(row));
    });
    const keys = (list: unknown[] | undefined, member: string): string[] =>
      (list ?? []).flatMap((key, index) => {
        try {
          return [uuidCanonical(String(key))];
        } catch {
          issues.push({ path: `${at}/${member}/${index}`, message: `${JSON.stringify(key)} is not a UUID` });
          return [];
        }
      });
    edits[name] = { upsert, delete: keys(given.delete, 'delete'), unset: keys(given.unset, 'unset') };
  }
  if (issues.length > 0) {
    throw new OperationParamsError('Branches', 'save', issues);
  }
  return edits;
}

/** A resolution that gives a value, which the merged row is held to its kind's type for. */
interface ValueResolution {
  /** Its index in the resolutions. */
  readonly index: number;
  readonly kind: string;
  readonly entityKey: string;
}

// resolutionsOf reads a merge's or a rebase's resolutions as the engine
// takes them. A value is held to the value class of the field it sets; the
// row it leaves is held to its kind's type once the engine has merged
// (checkResolved), so it returns the resolutions that give one.
function resolutionsOf(context: InstanceContext<BranchesConfig>, operation: string, raw: unknown): { resolutions: Resolution[]; values: ValueResolution[] } {
  const issues: SchemaIssue[] = [];
  const resolutions: Resolution[] = [];
  const values: ValueResolution[] = [];
  ((raw as Array<Record<string, unknown>> | undefined) ?? []).forEach((resolution, index) => {
    const at = `/resolutions/${index}`;
    const kind = String(resolution.kind);
    if (!hasOwn(context.config.kinds, kind)) {
      issues.push({ path: `${at}/kind`, message: `${kind} is not a kind of the graph` });
      return;
    }
    let entityKey: string;
    try {
      entityKey = uuidCanonical(String(resolution.entityKey));
    } catch {
      issues.push({ path: `${at}/entityKey`, message: `${JSON.stringify(resolution.entityKey)} is not a UUID` });
      return;
    }
    const path = String(resolution.path);
    const takes = hasOwn(resolution, 'take');
    if (takes === hasOwn(resolution, 'value')) {
      issues.push({ path: at, message: 'a resolution gives take or value, one of them' });
      return;
    }
    if (takes) {
      resolutions.push({ kind, entityKey, path, take: resolution.take as 'base' | 'ours' | 'theirs' });
      return;
    }
    const spec = context.config.kinds[kind];
    const value = resolution.value;
    // A whole entity (path "") is settled with take, which the core holds
    // to; a value sets one unit, a field or a key below one.
    const tokens = path === '' ? [] : path.slice(1).split('/').map((part) => part.replace(/~1/g, '/').replace(/~0/g, '~'));
    if (tokens.length === 1 && hasOwn(spec.classes, tokens[0])) {
      for (const issue of classIssues(spec, { [tokens[0]]: value }, `${at}/value`)) {
        issues.push({ ...issue, path: `${at}/value` });
      }
    }
    resolutions.push({ kind, entityKey, path, value: JSON.stringify(value) });
    values.push({ index, kind, entityKey });
  });
  if (issues.length > 0) {
    throw new OperationParamsError('Branches', operation, issues);
  }
  return { resolutions, values };
}

// checkResolved holds each entity a value resolution settled, as the ref
// now composes it, to its kind's type, as save holds a row: a resolution
// whose value leaves a row its type refuses is refused at its value, and
// the operation, with what it wrote, rolls back.
function checkResolved(context: InstanceContext<BranchesConfig>, graph: Graph, operation: string, ref: string, values: readonly ValueResolution[]): void {
  if (values.length === 0) {
    return;
  }
  const { tree } = graph.engine.compose(ref);
  const issues: SchemaIssue[] = [];
  for (const value of values) {
    const kind = context.config.kinds[value.kind];
    const text = (tree[value.kind] ?? []).find((row) => (JSON.parse(row) as Record<string, unknown>).entity_key === value.entityKey);
    if (text === undefined) {
      // The resolution deleted the entity.
      continue;
    }
    const row = JSON.parse(text) as Record<string, unknown>;
    const content = Object.fromEntries(kind.fields.map((field) => [field, row[field]]));
    for (const issue of context.validate(kind.type, content) as ValidationIssue[]) {
      issues.push({
        path: `/resolutions/${value.index}/value`,
        message: `${value.kind} ${value.entityKey}${issue.path === '' ? '' : ` ${issue.path}`}: ${issue.message}`,
      });
    }
  }
  if (issues.length > 0) {
    throw new OperationParamsError('Branches', operation, issues);
  }
}

// commitOptions reads a commit's message and tag from the parameters.
function commitOptions(params: FrozenJSON): { message?: string; tag?: boolean } {
  return {
    ...(params.message === undefined ? {} : { message: params.message as string }),
    ...(params.tag === undefined ? {} : { tag: params.tag as boolean }),
  };
}

// deleteGraph deletes an instance's graph: its history, rows, release
// pointer, commits and refs, and its record as a root. A ref's head and
// base point at its commits, and a commit at its ref, so the refs let go
// of their commits first.
function deleteGraph(context: InstanceContext<BranchesConfig>): void {
  const t = tablesOf(context.sql);
  const graph = `${context.namespace}/${context.schema}`;
  const root = rootOf(context.id);
  const run = (sql: string, params: SqlValue[]) => context.sql.run(sql, params);
  run(`DELETE FROM ${t.memberHistory} WHERE graph = ? AND json_extract(data, '$.root_id') = ?`, [graph, root]);
  run(`DELETE FROM ${t.refHistory} WHERE graph = ? AND id IN (SELECT id FROM ${t.ref} WHERE graph = ? AND root_id = ?)`, [graph, graph, root]);
  run(`DELETE FROM ${t.releaseHistory} WHERE graph = ? AND id IN (SELECT id FROM ${t.release} WHERE graph = ? AND root_id = ?)`, [graph, graph, root]);
  run(`DELETE FROM ${t.release} WHERE graph = ? AND root_id = ?`, [graph, root]);
  run(`DELETE FROM ${t.member} WHERE graph = ? AND root_id = ?`, [graph, root]);
  for (const table of [t.snapshot, t.patch]) {
    run(`DELETE FROM ${table} WHERE graph = ? AND commit_id IN (SELECT id FROM ${t.commit} WHERE graph = ? AND root_id = ?)`, [graph, graph, root]);
  }
  run(`UPDATE ${t.ref} SET head_commit_id = NULL, base_commit_id = NULL WHERE graph = ? AND root_id = ?`, [graph, root]);
  run(`DELETE FROM ${t.commit} WHERE graph = ? AND root_id = ?`, [graph, root]);
  run(`DELETE FROM ${t.ref} WHERE graph = ? AND root_id = ?`, [graph, root]);
  run(`DELETE FROM ${t.roots} WHERE graph = ? AND root_id = ?`, [graph, root]);
}

export const branches = defineBehavior<BranchesConfig>({
  declaration,

  parseConfig(config, target) {
    const raw = config as RawConfig;
    const kinds: Record<string, BranchesKind> = {};
    for (const name of Object.keys(raw.kinds).sort()) {
      kinds[name] = kindOf(name, raw.kinds[name], raw, target);
    }
    const descriptor = descriptorOf(target.schema, kinds);
    try {
      graphCore().run('validate', `{"descriptor":${descriptor},"tree":{}}`);
    } catch (error) {
      if (error instanceof VersionGraphError) {
        throw new BehaviorConfigError(`the graph's descriptor: ${error.message}`);
      }
      throw error;
    }
    return {
      kinds,
      primary: raw.primary ?? DEFAULT_PRIMARY,
      snapshotEvery: raw.snapshotEvery ?? 64,
      ...(raw.sweep === undefined ? {} : { sweep: { ...raw.sweep } }),
      descriptor,
    };
  },

  configChange(before, after) {
    if (before === undefined) {
      return undefined;
    }
    if (after === undefined) {
      return 'the graphs its instances root would stay behind with nothing to delete them';
    }
    for (const [name, kind] of Object.entries(before.kinds)) {
      const next = after.kinds[name];
      if (next === undefined) {
        return `it removes kind ${name}, whose rows the graphs hold`;
      }
      const changed = (what: string, from: unknown, to: unknown) => `kind ${name}: its ${what} changes from ${JSON.stringify(from ?? null)} to ${JSON.stringify(to ?? null)}`;
      if (next.type !== kind.type) {
        return changed('type', kind.type, next.type);
      }
      if (!jsonEqual(next.parent ?? null, kind.parent ?? null)) {
        return changed('parent', kind.parent, next.parent);
      }
      if (next.order !== kind.order) {
        return changed('order', kind.order, next.order);
      }
      if (next.singleton !== kind.singleton) {
        return changed('singleton', kind.singleton, next.singleton);
      }
      // A field the old type lacks held no value, so the new version may
      // give it any unit.
      for (const field of kind.fields) {
        const from = kind.units[field] ?? 'atomic';
        const to = next.units[field] ?? 'atomic';
        if (next.fields.includes(field) && from !== to) {
          return changed(`unit of ${field}`, from, to);
        }
      }
    }
    return undefined;
  },

  migrations: [
    {
      version: 1,
      name: 'the version graph layout, roots and actors',
      up(sql) {
        // A shipped migration never changes, and this one takes the
        // layout's statements from @superschematic/versiongraph as it runs.
        // test/core-branches.test.ts pins them, so a layout the adapter
        // changes fails there until a migration of its own brings a file
        // created at this one up to it.
        for (const statement of sqliteLayout((local) => sql.table(local))) {
          sql.run(statement);
        }
        sql.run(`CREATE TABLE ${sql.table('roots')} (
          graph   TEXT NOT NULL,
          root_id TEXT NOT NULL,
          id      TEXT NOT NULL,
          PRIMARY KEY (graph, root_id)
        ) STRICT`);
        sql.run(`CREATE TABLE ${sql.table('actors')} (
          actor   TEXT NOT NULL PRIMARY KEY,
          subject TEXT NOT NULL
        ) STRICT`);
      },
    },
  ],

  initialize(context) {
    ensureRoot(context);
  },

  operations: {
    branch(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'branch', () => {
        const from = ownRef(context, graph, 'branch', 'fromRef', params.fromRef);
        return refOf(graph, graph.engine.branch(actorOf(context), from, params.name as string).id);
      });
    },

    save(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'save', () => {
        const ref = ownRef(context, graph, 'save', 'ref', params.ref);
        const edits = editsOf(context, params.edits as FrozenJSON);
        const result = graph.engine.save(actorOf(context), ref, params.version as number, edits);
        const saved: Record<string, Array<Record<string, unknown>>> = {};
        for (const kind of Object.keys(result.saved)) {
          saved[kind] = rowsOf(graph, result.saved[kind]);
        }
        return { ref: refOf(graph, result.ref.id), saved };
      });
    },

    commit(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'commit', () => {
        const ref = ownRef(context, graph, 'commit', 'ref', params.ref);
        const result = graph.engine.commit(actorOf(context), ref, params.version as number, commitOptions(params));
        return { ref: refOf(graph, result.ref.id), commit: commitsOf(graph, [result.commit])[0] };
      });
    },

    seal(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'seal', () => {
        const ref = ownRef(context, graph, 'seal', 'ref', params.ref);
        const result = graph.engine.seal(actorOf(context), ref, params.version as number);
        return { ref: refOf(graph, result.ref.id), commit: commitsOf(graph, [result.commit])[0] };
      });
    },

    merge(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'merge', () => {
        const source = ownRef(context, graph, 'merge', 'source', params.source);
        const target = ownRef(context, graph, 'merge', 'target', params.target);
        const { resolutions, values } = resolutionsOf(context, 'merge', params.resolutions);
        const result = graph.engine.merge(actorOf(context), source, target, params.targetVersion as number, resolutions, commitOptions(params));
        if (result.conflicts.length === 0) {
          checkResolved(context, graph, 'merge', result.ref.id, values);
        }
        return { ref: refOf(graph, result.ref.id), commit: commitsOf(graph, [result.commit])[0], conflicts: conflictsOf(graph, result.conflicts) };
      });
    },

    rebase(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'rebase', () => {
        const draft = ownRef(context, graph, 'rebase', 'draft', params.draft);
        const { resolutions, values } = resolutionsOf(context, 'rebase', params.resolutions);
        const result = graph.engine.rebase(actorOf(context), draft, params.version as number, resolutions);
        if (result.conflicts.length === 0) {
          checkResolved(context, graph, 'rebase', result.ref.id, values);
        }
        return { ref: refOf(graph, result.ref.id), commit: commitsOf(graph, [result.commit])[0], conflicts: conflictsOf(graph, result.conflicts) };
      });
    },

    revert(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'revert', () => {
        const ref = ownRef(context, graph, 'revert', 'ref', params.ref);
        const toCommit = ownCommit(context, graph, 'revert', 'toCommit', params.toCommit);
        const result = graph.engine.revert(actorOf(context), ref, params.version as number, toCommit);
        return { ref: refOf(graph, result.ref.id), commit: commitsOf(graph, [result.commit])[0] };
      });
    },

    release(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'release', () => {
        const commit = ownCommit(context, graph, 'release', 'commit', params.commit);
        const release = graph.engine.release(actorOf(context), rootOf(context.id), commit, params.version as number);
        return { commit: release.commit, version: release.version };
      });
    },

    discard(context, params) {
      const graph = ensureRoot(context);
      return guarded(context, 'discard', () => {
        const ref = ownRef(context, graph, 'discard', 'ref', params.ref);
        // Every draft branches from the primary line and merges into it, and
        // the instance gets no other.
        if (refOf(graph, ref).parent === null) {
          throw new BehaviorVetoError('Branches', 'discard', context.schema, context.id, {
            reason: 'the primary line is not discarded: every draft branches from it and merges into it',
            code: 'primary_line',
          });
        }
        graph.engine.discard(actorOf(context), ref, params.version as number);
        return refOf(graph, ref);
      });
    },

    refs(context, params) {
      const limit = (params.limit as number | undefined) ?? DEFAULT_PAGE_SIZE;
      const after = refsCursor(params.cursor as string | undefined);
      const store = storeOf(context);
      // The primary line first, then the drafts in the order they were
      // created: a key every ref keeps for good, which a VACUUM that
      // renumbers rows does not move.
      const rows = context.sql.all(
        `SELECT ${REF_COLUMNS}, parent_ref_id IS NOT NULL AS draft FROM ${store.tables.ref}
         WHERE graph = ? AND root_id = ? AND deleted_at IS NULL
           AND (parent_ref_id IS NOT NULL, created_at, id) > (?, ?, ?)
         ORDER BY parent_ref_id IS NOT NULL, created_at, id LIMIT ?`,
        [store.name, rootOf(context.id), ...after, limit + 1]
      );
      const refs = refsOf(store, rows.slice(0, limit));
      const last = rows[limit - 1];
      return {
        items: refs,
        next: rows.length > limit ? Buffer.from(JSON.stringify([Number(last.draft), Number(last.created_at), String(last.id)]), 'utf8').toString('base64url') : null,
      };
    },

    releases(context, params) {
      const { limit, after } = pageRequest('Branches', 'releases', params);
      const store = storeOf(context);
      const rows = context.sql.all(
        `SELECT h._version AS version, h.data FROM ${store.tables.releaseHistory} AS h JOIN ${store.tables.release} AS r ON r.id = h.id
         WHERE r.graph = ? AND r.root_id = ? AND h._version > ?
         ORDER BY h._version LIMIT ?`,
        [store.name, rootOf(context.id), after, limit + 1]
      );
      const images = rows.map((row) => JSON.parse(String(row.data)) as Record<string, string>);
      const subject = subjectsOf(
        store,
        images.map((image) => image.updated_by)
      );
      const releases: BranchRelease[] = rows.map((row, index) => ({
        version: Number(row.version),
        commit: images[index].commit_id,
        releasedAt: images[index].updated_at,
        releasedBy: subject(images[index].updated_by),
      }));
      return page(releases, limit, (release) => release.version);
    },

    compose(context, params) {
      const graph = graphOf(context);
      return guarded(context, 'compose', () => treeResult(graph, graph.engine.compose(ownRef(context, graph, 'compose', 'ref', params.ref))));
    },

    materialize(context, params) {
      const graph = graphOf(context);
      return guarded(context, 'materialize', () =>
        treeResult(graph, graph.engine.materialize(ownCommit(context, graph, 'materialize', 'commit', params.commit)))
      );
    },

    released(context) {
      const graph = graphOf(context);
      const root = rootOf(context.id);
      if (context.sql.get(`SELECT 1 AS found FROM ${graph.tables.release} WHERE graph = ? AND root_id = ?`, [graph.name, root]) === undefined) {
        throw new EngineError('not_found', `${context.schema} ${context.id} has not been released`);
      }
      return guarded(context, 'released', () => {
        const result = graph.engine.released(root);
        return { release: { commit: result.release.commit, version: result.release.version }, ...treeResult(graph, result) };
      });
    },

    diff(context, params) {
      const graph = graphOf(context);
      return guarded(context, 'diff', () => {
        const from = ownCommit(context, graph, 'diff', 'from', params.from);
        const to = ownCommit(context, graph, 'diff', 'to', params.to);
        const changes: Change[] = graph.engine.diff(from, to);
        const rows = rowsOf(
          graph,
          changes.flatMap((change) => (change.row === undefined ? [] : [change.row]))
        );
        let next = 0;
        return {
          changes: changes.map((change) => ({
            kind: change.kind,
            entityKey: change.entityKey,
            operation: change.operation,
            ...(change.row === undefined ? {} : { row: rows[next++] }),
          })),
        };
      });
    },

    history(context, params) {
      const graph = graphOf(context);
      return guarded(context, 'history', () => ({ commits: commitsOf(graph, graph.engine.history(ownRef(context, graph, 'history', 'ref', params.ref))) }));
    },
  },

  afterChange(context, change) {
    switch (change.kind) {
      case 'update':
      case 'operation':
        // An instance created before its schema composed Branches gets its
        // primary line at its first write.
        if (!isRoot(context)) {
          ensureRoot(context);
        }
        return;
      case 'delete':
        deleteGraph(context);
    }
  },

  schedules: {
    sweep: {
      everyMs: (config) => config.sweep?.intervalMs ?? null,
      run(context) {
        const sweep = context.config.sweep;
        if (sweep === undefined) {
          return;
        }
        const graph = graphOf(context);
        if (sweep.abandonAfter !== undefined) {
          // Each idle draft is discarded through its instance's discard, so
          // its guards run and it appends its event; one a guard vetoes stays.
          const idle = context.sql.all(
            `SELECT r.id, r._version, o.id AS instance FROM ${graph.tables.ref} AS r
             JOIN ${graph.tables.roots} AS o ON o.graph = r.graph AND o.root_id = r.root_id
             WHERE r.graph = ? AND r.deleted_at IS NULL AND r.parent_ref_id IS NOT NULL AND r.updated_at < ?
             ORDER BY r.updated_at, r.id`,
            [graph.name, (context.now - sweep.abandonAfter) * 1000]
          );
          for (const draft of idle) {
            try {
              context.instances.invoke(context.schema, String(draft.instance), 'discard', { ref: String(draft.id), version: Number(draft._version) });
            } catch (error) {
              if (!(error instanceof BehaviorVetoError)) {
                throw error;
              }
            }
          }
        }
        graph.engine.sweep({
          actor: actorOf(context),
          ...(sweep.discardGrace === undefined ? {} : { discardGrace: sweep.discardGrace }),
          ...(sweep.pruneBatch === undefined ? {} : { pruneBatch: sweep.pruneBatch }),
          abandonAfter: 0,
        });
      },
    },
  },
});

