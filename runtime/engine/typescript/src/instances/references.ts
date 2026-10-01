/*
The references behaviors record from one instance to another, in
engine_references (D16, amended). A reference is a behavior's, from an
instance (its source) to another instance of the same namespace (its
target), under a key the behavior chooses. The engine reads them by
target, to ask the referencing behaviors' guards before a change of the
target and to run their hooks after it, and by source, for the behavior's
own list and to drop them when the source is deleted. Nothing here asks
the access policy; the instance store does, before it calls in.
*/

import type { Reference } from '../behaviors/behavior.js';
import type { ReferenceSource } from '../behaviors/execution.js';
import type { Storage } from '../storage/storage.js';

/** A reference to an instance, as the target's change reads it: where it starts, and its key. */
export interface IncomingReference {
  readonly schema: string;
  readonly id: string;
  readonly behavior: string;
  readonly key: string;
}

export class ReferenceTable {
  constructor(private readonly storage: Storage) {}

  /** add records a reference; recording one that exists changes nothing. */
  add(namespace: string, source: ReferenceSource, target: Reference): void {
    this.storage.run(
      `INSERT INTO engine_references (namespace, target_schema, target_id, source_schema, source_id, behavior, key)
       VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`,
      [namespace, target.schema, target.id, source.schema, source.id, source.behavior, target.key]
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
        `SELECT target_schema, target_id, key FROM engine_references
         WHERE namespace = ? AND source_schema = ? AND source_id = ? AND behavior = ? ORDER BY rowid`,
        [namespace, source.schema, source.id, source.behavior]
      )
      .map((row) => ({ schema: String(row.target_schema), id: String(row.target_id), key: String(row.key) }));
  }

  /**
   * to lists the references to an instance from other instances, in the
   * order recorded. A reference from the instance to itself is left out:
   * its behavior's own guard and afterChange see the instance's changes.
   */
  to(namespace: string, schema: string, id: string): IncomingReference[] {
    return this.storage
      .all(
        `SELECT source_schema, source_id, behavior, key FROM engine_references
         WHERE namespace = ? AND target_schema = ? AND target_id = ? AND NOT (source_schema = ? AND source_id = ?)
         ORDER BY rowid`,
        [namespace, schema, id, schema, id]
      )
      .map((row) => ({ schema: String(row.source_schema), id: String(row.source_id), behavior: String(row.behavior), key: String(row.key) }));
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
