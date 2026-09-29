/*
Instances of a schema's instance type, keyed by namespace, schema name and
id. The key includes the namespace, so an id in one namespace says nothing
about another. An instance is written with the live version of its schema
(its own namespace's, or the shared one's) and validated by it; its row
records that version. update is a JSON merge patch (RFC 7386, patch.ts),
validated after the merge. Each write appends its event in the same
transaction (events/log.ts).

list pages through a schema's instances in creation order with an opaque
cursor. Each page is one indexed range read of at most the page size, so
a list never loads the whole table, and an instance created or deleted
while a client pages moves no other instance between pages.
*/

import { randomUUID } from 'node:crypto';

import { checkPrincipal, type Access, type Action, type Principal } from '../access.js';
import { EngineError, InstanceValidationError } from '../errors.js';
import { appendEvent, nextSeq } from '../events/log.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import type { SchemaCatalog, SchemaRecord } from '../registry/catalog.js';
import { checkSchemaName } from '../registry/document.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { isPlainObject, jsonEqual, mergePatch } from './patch.js';

/** An instance id: a letter or digit, then letters, digits, `.`, `_`, `:` and `-`, at most 256 characters. */
export const INSTANCE_ID = /^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$/;

/** A stored instance. */
export interface InstanceRecord {
  namespace: string;
  schema: string;
  id: string;
  /** The namespace that holds the schema: the instance's own, or the shared one. */
  schemaNamespace: string;
  /** The schema version the instance was last written with. */
  version: number;
  /** The per-instance sequence of its last event. */
  seq: number;
  data: Record<string, unknown>;
  createdAt: number;
  createdBy: string;
  updatedAt: number;
  updatedBy: string;
}

/** Where a call looks: a namespace, `default` when absent. */
export interface InstanceTarget {
  namespace?: string;
}

export interface CreateOptions extends InstanceTarget {
  /** The id; the engine's id generator makes one when absent. */
  id?: string;
}

export interface UpdateOptions extends InstanceTarget {
  /**
   * The sequence the caller last read. The update is refused with
   * seq_mismatch unless the instance is still at it, checked inside the
   * write transaction.
   */
  expectedSeq?: number;
}

export interface DeleteOptions extends InstanceTarget {
  /** As UpdateOptions.expectedSeq. */
  expectedSeq?: number;
}

export interface ListOptions extends InstanceTarget {
  /** At most this many instances, 50 by default and at most 500. */
  limit?: number;
  /** The `next` of the previous page. */
  cursor?: string;
}

/** One page of a list. */
export interface InstancePage {
  items: InstanceRecord[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

const COLUMNS =
  'position, namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by';

export class InstanceStore {
  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly catalog: SchemaCatalog,
    private readonly access: Access,
    private readonly ids: () => string,
    private readonly clock: () => number
  ) {}

  /** create validates data against the schema's live version and stores it under a new id. */
  create(principal: Principal, schema: string, data: unknown, options: CreateOptions = {}): InstanceRecord {
    const namespace = this.target(principal, 'write', schema, options);
    const id = options.id ?? this.ids();
    checkId(id);
    const now = this.clock();
    return this.storage.transaction(() => {
      const record = this.live(namespace, schema);
      this.validate(namespace, record, data);
      const json = JSON.stringify(data);
      const seq = nextSeq(this.storage, namespace, schema, id);
      const inserted = this.storage.run(
        `INSERT INTO engine_instances
           (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, id) DO NOTHING`,
        [namespace, schema, id, record.namespace, record.version as number, seq, json, now, principal.subject, now, principal.subject]
      );
      if (inserted.changes === 0) {
        throw new EngineError('conflict', `${schema} ${id} already exists in namespace ${namespace}`);
      }
      appendEvent(this.storage, {
        kind: 'create',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        actor: principal.subject,
        at: now,
        change: json,
      });
      return toInstance(this.row(namespace, schema, id) as Row);
    });
  }

  /** get returns an instance, or undefined when the namespace has none with the id. */
  get(principal: Principal, schema: string, id: string, options: InstanceTarget = {}): InstanceRecord | undefined {
    const namespace = this.target(principal, 'read', schema, options);
    this.live(namespace, schema);
    const row = this.row(namespace, schema, id);
    return row ? toInstance(row) : undefined;
  }

  /** list returns a page of a schema's instances in creation order. */
  list(principal: Principal, schema: string, options: ListOptions = {}): InstancePage {
    const namespace = this.target(principal, 'read', schema, options);
    const limit = pageSize(options.limit);
    const after = options.cursor === undefined ? 0 : decodeCursor(options.cursor);
    this.live(namespace, schema);
    const rows = this.storage.all(
      `SELECT ${COLUMNS} FROM engine_instances
       WHERE namespace = ? AND schema = ? AND position > ?
       ORDER BY position LIMIT ?`,
      [namespace, schema, after, limit + 1]
    );
    const items = rows.slice(0, limit);
    const next = rows.length > limit ? encodeCursor(Number(items[items.length - 1].position)) : null;
    return { items: items.map(toInstance), next };
  }

  /**
   * update applies a JSON merge patch to an instance and validates the
   * result against the live version. A patch that changes nothing writes
   * nothing. With expectedSeq, the instance must still be at that
   * sequence.
   */
  update(principal: Principal, schema: string, id: string, patch: unknown, options: UpdateOptions = {}): InstanceRecord {
    const namespace = this.target(principal, 'write', schema, options);
    checkExpectedSeq(options.expectedSeq);
    if (!isPlainObject(patch)) {
      throw new EngineError('invalid_argument', 'a merge patch of an instance is a JSON object');
    }
    const now = this.clock();
    return this.storage.transaction(() => {
      const record = this.live(namespace, schema);
      const row = this.existing(namespace, schema, id);
      matchSeq(row, options.expectedSeq);
      const current = JSON.parse(String(row.data)) as Record<string, unknown>;
      const merged = mergePatch(current, patch);
      this.validate(namespace, record, merged);
      if (jsonEqual(merged, current)) {
        return toInstance(row);
      }
      const seq = Number(row.seq) + 1;
      this.storage.run(
        `UPDATE engine_instances
         SET data = ?, version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
         WHERE namespace = ? AND schema = ? AND id = ?`,
        [JSON.stringify(merged), record.version as number, record.namespace, seq, now, principal.subject, namespace, schema, id]
      );
      appendEvent(this.storage, {
        kind: 'update',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        actor: principal.subject,
        at: now,
        change: JSON.stringify(patch),
      });
      return toInstance(this.row(namespace, schema, id) as Row);
    });
  }

  /**
   * delete removes an instance and appends its delete event; false when
   * there is none. With expectedSeq, the instance must still be at that
   * sequence.
   */
  delete(principal: Principal, schema: string, id: string, options: DeleteOptions = {}): boolean {
    const namespace = this.target(principal, 'write', schema, options);
    checkExpectedSeq(options.expectedSeq);
    const now = this.clock();
    return this.storage.transaction(() => {
      this.live(namespace, schema);
      const row = this.row(namespace, schema, id);
      if (!row) {
        return false;
      }
      matchSeq(row, options.expectedSeq);
      this.storage.run('DELETE FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?', [namespace, schema, id]);
      appendEvent(this.storage, {
        kind: 'delete',
        namespace,
        schema,
        instanceId: id,
        seq: Number(row.seq) + 1,
        version: Number(row.version),
        actor: principal.subject,
        at: now,
        change: null,
      });
      return true;
    });
  }

  private target(principal: Principal, action: Action, schema: string, options: InstanceTarget): string {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    checkSchemaName(schema);
    this.access.require(principal, action, namespace, schema);
    return namespace;
  }

  private live(namespace: string, schema: string): SchemaRecord {
    const record = this.catalog.find(schema, namespace, 'live');
    if (!record) {
      throw new EngineError('not_found', `schema ${schema} has no live version in namespace ${namespace}`);
    }
    return record;
  }

  private validate(namespace: string, record: SchemaRecord, data: unknown): void {
    const issues = this.catalog.validatorOf(record).validate(data);
    if (issues.length > 0) {
      throw new InstanceValidationError(namespace, record.name, record.version as number, issues);
    }
  }

  private row(namespace: string, schema: string, id: string): Row | undefined {
    return this.storage.get(`SELECT ${COLUMNS} FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?`, [
      namespace,
      schema,
      id,
    ]);
  }

  private existing(namespace: string, schema: string, id: string): Row {
    const row = this.row(namespace, schema, id);
    if (!row) {
      throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
    }
    return row;
  }
}

/** defaultIds makes random UUIDs. */
export function defaultIds(): string {
  return randomUUID();
}

function checkExpectedSeq(expectedSeq: number | undefined): void {
  if (expectedSeq !== undefined && (!Number.isSafeInteger(expectedSeq) || expectedSeq < 0)) {
    throw new EngineError('invalid_argument', `an expected sequence is a non-negative integer, got ${String(expectedSeq)}`);
  }
}

// An instance's sequence starts at 1, so an expected sequence of 0 matches
// no instance.
function matchSeq(row: Row, expectedSeq: number | undefined): void {
  if (expectedSeq !== undefined && Number(row.seq) !== expectedSeq) {
    throw new EngineError('seq_mismatch', `${String(row.schema)} ${String(row.id)} is no longer at sequence ${expectedSeq}`);
  }
}

function checkId(id: string): void {
  if (typeof id !== 'string' || !INSTANCE_ID.test(id)) {
    throw new EngineError('invalid_argument', `instance id "${String(id)}" must match ${INSTANCE_ID.source}`);
  }
}

// A cursor is the position of the last instance of a page, base64url
// encoded so callers treat it as opaque.
function encodeCursor(position: number): string {
  return Buffer.from(`after:${position}`, 'utf8').toString('base64url');
}

function decodeCursor(cursor: string): number {
  const match = typeof cursor === 'string' ? /^after:([0-9]{1,15})$/.exec(Buffer.from(cursor, 'base64url').toString('utf8')) : null;
  if (!match) {
    throw new EngineError('invalid_argument', 'the list cursor is not one this engine returned');
  }
  return Number(match[1]);
}

function toInstance(row: Row): InstanceRecord {
  return {
    namespace: String(row.namespace),
    schema: String(row.schema),
    id: String(row.id),
    schemaNamespace: String(row.schema_namespace),
    version: Number(row.version),
    seq: Number(row.seq),
    data: JSON.parse(String(row.data)) as Record<string, unknown>,
    createdAt: Number(row.created_at),
    createdBy: String(row.created_by),
    updatedAt: Number(row.updated_at),
    updatedBy: String(row.updated_by),
  };
}
