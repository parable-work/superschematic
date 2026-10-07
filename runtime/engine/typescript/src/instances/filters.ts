/*
A list's equality filters (where) and a lookup's key (D16, amended: an
instance is found by a unique field, and a list filters by equality).

where is a JSON object of field values: a member keeps the instances whose
field holds the value, a list of values any of them, and the members
together keep the instances every one keeps. A field is one of the
instance type's own top-level fields that holds a string, a number or a
boolean (indexes.ts, scalarFields), or a field a behavior lets a list
filter on (BehaviorImplementation.filters), which the behavior keeps in
one of its columns: Workflow's status. A value is of the field's JSON
type; null is no value, and a list holds 1 to 100 of them.

A page keeps creation order (position) and reads in one of two ways:

- through an index whose every key a member names: an own index
  (indexes.ts) or a behavior's index on the filter's column. Each
  combination of the members' values is one indexed range read past the
  cursor, in position order, at most 100 of them, and the page merges
  them. A unique index answers each combination with one instance at
  most, so the whole answer is one page or a few.
- otherwise through the list index, as an unfiltered page reads,
  scanning at most FILTER_SCAN_ROWS instances past the cursor.

Either way a page reads a bounded number of rows, so a filter that keeps
few instances of many costs no more per page than a plain list, and the
members no index serves are tested in SQL on the rows read. A page holds
the instances read that every member keeps, up to its limit; next is the
position up to which every instance has been read, so a reader that goes
on from it misses none and reads none twice. A page can therefore hold
fewer instances than its limit, even none, while next is not null, as a
filtered page of the event log can.

A member on an own field no index covers also matches a value the row
holds by hash: a string whose JSON is longer than the value store's least
threshold is compared both as itself and by its hash, since the row may
hold it either way.

A lookup's key names the fields of one unique index, each with one value,
and reads the one instance whose fields hold them, through the index.
*/

import { indexName } from '../behaviors/storage.js';
import { EngineError } from '../errors.js';
import type { SqlValue } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { canonicalJSON, sha256Hex } from '../values/canonical.js';
import { MIN_VALUE_THRESHOLD } from '../values/store.js';
import { isPlainObject } from './patch.js';
import { indexExists, refHashExpression, schemaRows, valueExpression, type OwnIndex, type ScalarType } from './indexes.js';

/** The most values one member of where lists. */
export const MAX_FILTER_VALUES = 100;

/** The most combinations of values a page reads through one index. */
export const MAX_FILTER_RANGES = 100;

/**
 * The instances a filtered page reads past its cursor: every one it reads
 * when no index serves the filter, shared among its range reads when one
 * does (each reads at least two).
 */
export const FILTER_SCAN_ROWS = 1000;

/** A field a list may filter on. */
export interface Filterable {
  readonly key: string;
  readonly type: ScalarType;
  /** The behavior whose field it is; undefined for an own field. */
  readonly behavior?: string;
  /** For a behavior's field, the SQL name of the column that holds it. */
  readonly column?: string;
  /** For a behavior's field, the SQL name of the behavior's index on that column alone, when it has one. */
  readonly index?: string;
  /** For an own field, whether a row may hold a long value of it by hash: no own index covers it. */
  readonly spillable?: boolean;
}

/** A value a filter compares. */
export type FilterValue = string | number | boolean;

/** One member of where, checked. */
export interface WhereTerm {
  readonly filterable: Filterable;
  readonly values: readonly FilterValue[];
}

/** What a filtered page reads: the schema's rows in a namespace, its own indexes and the members. */
export interface FilterPlan {
  readonly namespace: string;
  readonly schema: string;
  /** The namespace that holds the schema. */
  readonly holder: string;
  readonly indexes: readonly OwnIndex[];
  readonly terms: readonly WhereTerm[];
}

/**
 * parseWhere checks a list's where against the fields a version filters
 * on and returns its members; an empty or absent where filters nothing.
 */
export function parseWhere(where: unknown, filterables: ReadonlyMap<string, Filterable>, schema: string): WhereTerm[] {
  if (where === undefined || where === null) {
    return [];
  }
  if (!isPlainObject(where)) {
    throw new EngineError('invalid_argument', `where is a JSON object of field values, by field`);
  }
  const terms: WhereTerm[] = [];
  for (const [key, given] of Object.entries(where)) {
    if (given === undefined) {
      continue;
    }
    const filterable = filterables.get(key);
    if (filterable === undefined) {
      throw new EngineError(
        'invalid_argument',
        `where names ${key}, which is not a field ${schema} filters on; it filters on ${filterables.size > 0 ? [...filterables.keys()].join(', ') : 'none'}: an own field that holds a string, a number or a boolean, or a field a behavior lets a list filter on`
      );
    }
    const values = Array.isArray(given) ? given : [given];
    if (Array.isArray(given) && (given.length === 0 || given.length > MAX_FILTER_VALUES)) {
      throw new EngineError('invalid_argument', `where.${key} lists 1 to ${MAX_FILTER_VALUES} values, got ${given.length}`);
    }
    const seen = new Set<string>();
    const kept: FilterValue[] = [];
    for (const value of values) {
      checkValue(`where.${key}`, filterable.type, value);
      const id = JSON.stringify(value);
      if (!seen.has(id)) {
        seen.add(id);
        kept.push(value as FilterValue);
      }
    }
    terms.push({ filterable, values: kept });
  }
  return terms;
}

/**
 * parseLookupKey checks a lookup's key: an object that names exactly the
 * fields of one unique index, each with one value of its type. It returns
 * the index and the values in its key order.
 */
export function parseLookupKey(
  key: unknown,
  indexes: readonly OwnIndex[],
  filterables: ReadonlyMap<string, Filterable>,
  schema: string
): { index: OwnIndex; values: FilterValue[] } {
  const unique = indexes.filter((index) => index.unique);
  if (unique.length === 0) {
    throw new EngineError('invalid_argument', `${schema} has no unique field to look an instance up by; find one with list and where`);
  }
  const choices = unique.map((index) => index.keys.join(' and ')).join('; ');
  if (!isPlainObject(key)) {
    throw new EngineError('invalid_argument', `a lookup key is a JSON object of the values of one unique index of ${schema}: ${choices}`);
  }
  const names = Object.keys(key).filter((name) => key[name] !== undefined);
  const index = unique.find((candidate) => candidate.keys.length === names.length && candidate.keys.every((name) => names.includes(name)));
  if (index === undefined) {
    throw new EngineError(
      'invalid_argument',
      `a lookup key names the fields of one unique index of ${schema} (${choices}), not ${names.length > 0 ? names.join(', ') : 'none'}`
    );
  }
  const values = index.keys.map((name) => {
    const value = key[name];
    checkValue(`key.${name}`, (filterables.get(name) as Filterable).type, value);
    return value as FilterValue;
  });
  return { index, values };
}

function checkValue(at: string, type: ScalarType, value: unknown): void {
  const fits =
    type === 'string'
      ? typeof value === 'string'
      : type === 'boolean'
        ? typeof value === 'boolean'
        : type === 'integer'
          ? Number.isSafeInteger(value)
          : typeof value === 'number' && Number.isFinite(value);
  if (!fits) {
    throw new EngineError(
      'invalid_argument',
      `${at} is ${type === 'integer' ? 'an integer' : `a ${type}`}${at.startsWith('where') ? ', or a list of them' : ''}, not ${value === null ? 'null' : JSON.stringify(value) ?? typeof value}`
    );
  }
}

/** bind is a value as SQL compares it: a boolean as JSON's json_extract gives it, 1 or 0. */
export function bind(value: FilterValue): SqlValue {
  return typeof value === 'boolean' ? (value ? 1 : 0) : value;
}

/**
 * lookupCondition is the condition on engine_instances of the instance a
 * unique index's values name; its parameters are the namespace, then the
 * values in key order.
 */
export function lookupCondition(holder: string, schema: string, index: OwnIndex): string {
  return `${schemaRows(holder, schema)} AND namespace = ? AND ${index.keys.map((key) => `${valueExpression(key)} = ?`).join(' AND ')}`;
}

// A range read: the rows of one combination of an index's values, or the
// list index's rows, past the cursor in position order. index names the
// index it reads (INDEXED BY), so the read is that range and no other
// plan, whatever SQLite's planner would choose.
interface Range {
  readonly index: string;
  readonly where: string;
  readonly params: readonly SqlValue[];
}

/**
 * filteredPage reads one page of a filter: the positions of the instances
 * it keeps, in order, at most limit, and the position the next page reads
 * on from, null after the last.
 */
export function filteredPage(storage: Storage, plan: FilterPlan, after: number, limit: number): { positions: number[]; next: number | null } {
  const driving = drivingRead(storage, plan);
  const residual = plan.terms.filter((term) => !driving.covers.has(term));
  const hit = residual.length === 0 ? { sql: '1', params: [] as SqlValue[] } : conjunction(residual);
  // Each range reads its share of FILTER_SCAN_ROWS, and at least two
  // rows, so a unique index's range, which holds one instance at most,
  // always reads to its end. With no member left to test, every row read
  // is kept, and a range needs no more than a page and one.
  const per = Math.max(2, Math.ceil(Math.max(FILTER_SCAN_ROWS, limit + 1) / driving.ranges.length));
  const read = residual.length === 0 ? Math.min(limit + 1, per) : per;
  // cut is the last position every range has read up to: a range that
  // filled its share may hold more past it.
  let cut = Number.POSITIVE_INFINITY;
  const rows: Array<{ position: number; hit: boolean }> = [];
  for (const range of driving.ranges) {
    const found = storage.all(
      `SELECT position, (${hit.sql}) AS hit FROM engine_instances INDEXED BY "${range.index}"
       WHERE ${range.where} AND position > ? ORDER BY position LIMIT ?`,
      [...hit.params, ...range.params, after, read]
    );
    if (found.length === read) {
      cut = Math.min(cut, Number(found[found.length - 1].position));
    }
    for (const row of found) {
      rows.push({ position: Number(row.position), hit: Number(row.hit) === 1 });
    }
  }
  rows.sort((a, b) => a.position - b.position);
  const kept: number[] = [];
  for (const row of rows) {
    if (row.position > cut) {
      break;
    }
    if (row.hit) {
      kept.push(row.position);
      if (kept.length > limit) {
        return { positions: kept.slice(0, limit), next: kept[limit - 1] };
      }
    }
  }
  return { positions: kept, next: Number.isFinite(cut) ? cut : null };
}

// drivingRead picks the index a page reads through: a unique own index
// first, then the one with the fewest combinations of values, of the own
// indexes whose every key a member names and the behaviors' indexes on a
// member's column; with none, the list index. It returns its range reads
// and the members it serves.
function drivingRead(storage: Storage, plan: FilterPlan): { ranges: Range[]; covers: ReadonlySet<WhereTerm> } {
  const byKey = new Map(plan.terms.filter((term) => term.filterable.behavior === undefined).map((term) => [term.filterable.key, term]));
  let best: { unique: boolean; terms: WhereTerm[]; combinations: number; ranges: () => Range[] } | undefined;
  const consider = (candidate: NonNullable<typeof best>): void => {
    if (candidate.combinations > MAX_FILTER_RANGES) {
      return;
    }
    if (
      best === undefined ||
      (candidate.unique && !best.unique) ||
      (candidate.unique === best.unique && candidate.combinations < best.combinations)
    ) {
      best = candidate;
    }
  };
  for (const index of plan.indexes) {
    const terms = index.keys.map((key) => byKey.get(key));
    if (terms.some((term) => term === undefined) || !indexExists(storage, index.name)) {
      continue;
    }
    const covered = terms as WhereTerm[];
    consider({
      unique: index.unique,
      terms: covered,
      combinations: covered.reduce((product, term) => product * term.values.length, 1),
      ranges: () =>
        combinations(covered.map((term) => term.values)).map((values) => ({
          index: index.name,
          where: `${schemaRows(plan.holder, plan.schema)} AND namespace = ? AND ${index.keys.map((key) => `${valueExpression(key)} = ?`).join(' AND ')}`,
          params: [plan.namespace, ...values.map(bind)],
        })),
    });
  }
  for (const term of plan.terms) {
    const { column, index } = term.filterable;
    if (column === undefined || index === undefined || !indexExists(storage, index)) {
      continue;
    }
    consider({
      unique: false,
      terms: [term],
      combinations: term.values.length,
      ranges: () =>
        term.values.map((value) => ({
          index,
          where: `namespace = ? AND schema = ? AND "${column}" = ?`,
          params: [plan.namespace, plan.schema, bind(value)],
        })),
    });
  }
  if (best === undefined) {
    return { ranges: [{ index: 'engine_instances_list', where: 'namespace = ? AND schema = ?', params: [plan.namespace, plan.schema] }], covers: new Set() };
  }
  return { ranges: best.ranges(), covers: new Set(best.terms) };
}

// combinations lists every choice of one value from each list.
function combinations(lists: ReadonlyArray<readonly FilterValue[]>): FilterValue[][] {
  return lists.reduce<FilterValue[][]>((partial, values) => partial.flatMap((prefix) => values.map((value) => [...prefix, value])), [[]]);
}

// conjunction is the SQL that tests every member on a row, with its
// parameters in order.
function conjunction(terms: readonly WhereTerm[]): { sql: string; params: SqlValue[] } {
  const parts: string[] = [];
  const params: SqlValue[] = [];
  for (const { filterable, values } of terms) {
    const expression = filterable.column !== undefined ? `"${filterable.column}"` : valueExpression(filterable.key);
    const alternatives = [`${expression} IN (${values.map(() => '?').join(', ')})`];
    params.push(...values.map(bind));
    if (filterable.spillable === true) {
      const hashes = values
        .filter((value): value is string => typeof value === 'string' && Buffer.byteLength(JSON.stringify(value), 'utf8') > MIN_VALUE_THRESHOLD)
        .map((value) => sha256Hex(canonicalJSON(value)));
      if (hashes.length > 0) {
        alternatives.push(`${refHashExpression(filterable.key)} IN (${hashes.map(() => '?').join(', ')})`);
        params.push(...hashes);
      }
    }
    parts.push(alternatives.length === 1 ? alternatives[0] : `(${alternatives.join(' OR ')})`);
  }
  return { sql: parts.join(' AND '), params };
}

/** A behavior on the type, as filterablesOf reads it: its name, its storage prefix and its filters. */
export interface FilteringBehavior {
  readonly name: string;
  readonly prefix: string;
  readonly filters: ReadonlyMap<string, { readonly column: string; readonly type: ScalarType; readonly index?: string }>;
}

/**
 * filterablesOf lists the fields a version's list filters on, by key: the
 * instance type's own scalar fields, then each behavior's filters, with
 * the SQL names of their columns and indexes.
 */
export function filterablesOf(
  own: ReadonlyMap<string, { readonly key: string; readonly type: ScalarType }>,
  indexes: readonly OwnIndex[],
  behaviors: readonly FilteringBehavior[]
): Map<string, Filterable> {
  const covered = new Set(indexes.flatMap((index) => index.keys));
  const found = new Map<string, Filterable>();
  for (const field of own.values()) {
    found.set(field.key, { key: field.key, type: field.type, spillable: !covered.has(field.key) });
  }
  for (const behavior of behaviors) {
    for (const [field, filter] of behavior.filters) {
      found.set(field, {
        key: field,
        type: filter.type,
        behavior: behavior.name,
        column: `${behavior.prefix}${filter.column}`,
        ...(filter.index === undefined ? {} : { index: indexName(behavior.prefix, filter.index) }),
      });
    }
  }
  return found;
}
