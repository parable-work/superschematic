/*
The references behaviors record from one instance to another, in
engine_references (D16, amended). A reference is a behavior's, from an
instance (its source) to another instance of the same namespace (its
target), under a key the behavior chooses. The engine reads them by
target, to ask the referencing behaviors' guards before a change of the
target and to run their hooks after it, and by source, for the behavior's
own list and to drop them when the source is deleted. Nothing here asks
the access policy; the instance store does, before it calls in.

A reference hears every change of its target, or says what it hears
(hears): the delete alone ('delete'), or one value of the target, at a
JSON pointer into its data, when a change moves it, or moves it across a
number (crosses). hears and crosses are columns an index leads with,
after the target, so a change of the target reads only the references
that hear it: the ones that hear every change, and the ones on a value
the change moved, of those with crosses only the ones whose number lies
between the value before and after. A target with thousands of
references on one value, each with its own number, costs a write that
moves the value the references it crosses, not the thousands.
*/

import type { Reference, ReferenceHears } from '../behaviors/behavior.js';
import type { ReferenceSource } from '../behaviors/execution.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { isPlainObject, jsonEqual } from './patch.js';

/** A reference to an instance, as the target's change reads it: where it starts, its key, and what it hears. */
export interface IncomingReference {
  readonly schema: string;
  readonly id: string;
  readonly behavior: string;
  readonly key: string;
  readonly hears?: ReferenceHears;
}

/** One value of a target a change moved: its JSON pointer, and the value before and after, undefined where absent. */
export interface Move {
  readonly path: string;
  readonly before: unknown;
  readonly after: unknown;
}

// The most pointers one query names.
const CHUNK = 500;

const INCOMING = 'rowid AS position, source_schema, source_id, behavior, key, hears, crosses';

function hearsOf(row: Row): ReferenceHears | undefined {
  if (row.hears === null || row.hears === undefined) {
    return undefined;
  }
  if (row.hears === 'delete') {
    return 'delete';
  }
  return row.crosses === null || row.crosses === undefined ? { path: String(row.hears) } : { path: String(row.hears), crosses: Number(row.crosses) };
}

function columnsOf(hears: ReferenceHears | undefined): [string | null, number | null] {
  if (hears === undefined) {
    return [null, null];
  }
  if (hears === 'delete') {
    return ['delete', null];
  }
  return [hears.path, hears.crosses ?? null];
}

function incomingOf(row: Row): IncomingReference {
  const hears = hearsOf(row);
  return {
    schema: String(row.source_schema),
    id: String(row.source_id),
    behavior: String(row.behavior),
    key: String(row.key),
    ...(hears === undefined ? {} : { hears }),
  };
}

function isNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

function escape(key: string): string {
  return key.replace(/~/g, '~0').replace(/\//g, '~1');
}

// members lists the members of an object or the indexes of an array; none of anything else.
function members(value: unknown): string[] {
  if (isPlainObject(value)) {
    return Object.keys(value);
  }
  return Array.isArray(value) ? value.map((_, index) => String(index)) : [];
}

function member(value: unknown, key: string): unknown {
  return (isPlainObject(value) || Array.isArray(value)) && Object.prototype.hasOwnProperty.call(value, key)
    ? (value as Record<string, unknown>)[key]
    : undefined;
}

/**
 * moves lists every value a change moved, by JSON pointer, from the
 * members of the record down: each member that differs, and inside it each
 * of its own members that differs, where a side that holds no object or
 * array holds none of them.
 */
export function moves(before: unknown, after: unknown): Move[] {
  const out: Move[] = [];
  const walk = (was: unknown, now: unknown, at: string): void => {
    for (const key of new Set([...members(was), ...members(now)])) {
      const from = member(was, key);
      const to = member(now, key);
      if (jsonEqual(from, to)) {
        continue;
      }
      const path = `${at}/${escape(key)}`;
      out.push({ path, before: from, after: to });
      walk(from, to, path);
    }
  };
  walk(before, after, '');
  return out;
}

export class ReferenceTable {
  constructor(private readonly storage: Storage) {}

  /** add records a reference, or what one that exists hears now. */
  add(namespace: string, source: ReferenceSource, target: Reference): void {
    const [hears, crosses] = columnsOf(target.hears);
    this.storage.run(
      `INSERT INTO engine_references (namespace, target_schema, target_id, source_schema, source_id, behavior, key, hears, crosses)
       VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
       ON CONFLICT (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
       DO UPDATE SET hears = excluded.hears, crosses = excluded.crosses`,
      [namespace, target.schema, target.id, source.schema, source.id, source.behavior, target.key, hears, crosses]
    );
  }

  /** remove removes a reference; false when there was none. */
  remove(namespace: string, source: ReferenceSource, target: Reference): boolean {
    return (
      this.storage.run(
        `DELETE FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND source_schema = ? AND source_id = ? AND behavior = ? AND key = ?`,
        [namespace, target.schema, target.id, source.schema, source.id, source.behavior, target.key]
      ).changes > 0
    );
  }

  /** has reports whether a reference is recorded. */
  has(namespace: string, source: ReferenceSource, target: Reference): boolean {
    return (
      this.storage.get(
        `SELECT 1 AS found FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND source_schema = ? AND source_id = ? AND behavior = ? AND key = ?`,
        [namespace, target.schema, target.id, source.schema, source.id, source.behavior, target.key]
      ) !== undefined
    );
  }

  /** from lists a behavior's references from an instance, in the order recorded. */
  from(namespace: string, source: ReferenceSource): Reference[] {
    return this.storage
      .all(
        `SELECT target_schema, target_id, key, hears, crosses FROM engine_references
         WHERE namespace = ? AND source_schema = ? AND source_id = ? AND behavior = ? ORDER BY rowid`,
        [namespace, source.schema, source.id, source.behavior]
      )
      .map((row) => {
        const hears = hearsOf(row);
        return { schema: String(row.target_schema), id: String(row.target_id), key: String(row.key), ...(hears === undefined ? {} : { hears }) };
      });
  }

  /**
   * to lists every reference to an instance from other instances, in the
   * order recorded: the ones a delete asks and runs. A reference from the
   * instance to itself is left out: its behavior's own guard and
   * afterChange see the instance's changes.
   */
  to(namespace: string, schema: string, id: string): IncomingReference[] {
    return this.storage
      .all(
        `SELECT ${INCOMING} FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND NOT (source_schema = ? AND source_id = ?)
         ORDER BY rowid`,
        [namespace, schema, id, schema, id]
      )
      .map(incomingOf);
  }

  /** guarding lists, in the order recorded, the references to an instance that hear every change: the ones a change other than a delete asks. */
  guarding(namespace: string, schema: string, id: string): IncomingReference[] {
    return this.storage
      .all(
        `SELECT ${INCOMING} FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND hears IS NULL AND NOT (source_schema = ? AND source_id = ?)
         ORDER BY rowid`,
        [namespace, schema, id, schema, id]
      )
      .map(incomingOf);
  }

  /**
   * hearing lists, in the order recorded, the references to an instance
   * that hear a change other than a delete, given the values it moved:
   * every one that hears every change, and every one on a value it moved,
   * of those with crosses the ones it moved across, from one side of the
   * number to another, where not being a number is a side of its own.
   */
  hearing(namespace: string, schema: string, id: string, moved: readonly Move[]): IncomingReference[] {
    const target = [namespace, schema, id];
    const select = `SELECT ${INCOMING} FROM engine_references WHERE namespace = ? AND target_schema = ? AND target_id = ?`;
    const rows: Row[] = [...this.storage.all(`${select} AND hears IS NULL`, target)];
    // Every number lies between a number and a value that is none.
    const across: string[] = [];
    const along: string[] = [];
    for (const move of moved) {
      if (isNumber(move.before) && isNumber(move.after)) {
        rows.push(
          ...this.storage.all(`${select} AND hears = ? AND crosses > ? AND crosses <= ?`, [
            ...target,
            move.path,
            Math.min(move.before, move.after),
            Math.max(move.before, move.after),
          ])
        );
        along.push(move.path);
      } else if (isNumber(move.before) || isNumber(move.after)) {
        across.push(move.path);
      } else {
        along.push(move.path);
      }
    }
    for (let at = 0; at < across.length; at += CHUNK) {
      const paths = across.slice(at, at + CHUNK);
      rows.push(...this.storage.all(`${select} AND hears IN (${paths.map(() => '?').join(', ')})`, [...target, ...paths]));
    }
    for (let at = 0; at < along.length; at += CHUNK) {
      const paths = along.slice(at, at + CHUNK);
      rows.push(...this.storage.all(`${select} AND hears IN (${paths.map(() => '?').join(', ')}) AND crosses IS NULL`, [...target, ...paths]));
    }
    const seen = new Set<number>();
    const found: Row[] = [];
    for (const row of rows) {
      const position = Number(row.position);
      if (!seen.has(position) && !(row.source_schema === schema && row.source_id === id)) {
        seen.add(position);
        found.push(row);
      }
    }
    return found.sort((a, b) => Number(a.position) - Number(b.position)).map(incomingOf);
  }

  /** drop removes every reference from an instance: its source was deleted. */
  drop(namespace: string, schema: string, id: string): void {
    this.storage.run('DELETE FROM engine_references WHERE namespace = ? AND source_schema = ? AND source_id = ?', [namespace, schema, id]);
  }

  /** dropOne removes one incoming reference, which a behavior the source no longer composes left behind. */
  dropOne(namespace: string, schema: string, id: string, reference: IncomingReference): void {
    this.remove(namespace, { schema: reference.schema, id: reference.id, behavior: reference.behavior }, { schema, id, key: reference.key });
  }
}
