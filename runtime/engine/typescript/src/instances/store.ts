/*
Instances of a schema's instance type, keyed by namespace, schema name and
id. The key includes the namespace, so an id in one namespace says nothing
about another. An instance is written with the live version of its schema
(its own namespace's, or the shared one's) and validated by it; its row
records that version. update is a JSON merge patch (RFC 7386, patch.ts),
validated after the merge. Each write appends its event in the same
transaction (events/log.ts).

The behaviors of the live version run with every call (behaviors/): a
create runs their initialize, then their afterChange; an update and a
delete ask their guards first and run afterChange after; a read adds the
fields they declare beside the instance's own, which are theirs to change:
a create or an update that sets one is refused (readOnly). invoke calls one
of their operations: it checks the parameters, asks the policy for write
or read as the operation's declaration says, and runs every guard, then
the handler, in the write transaction for an operation that writes, which
appends an operation event. Anything that throws rolls the whole call back.

list pages through a schema's instances in creation order with an opaque
cursor. Each page is one indexed range read of at most the page size, so
a list never loads the whole table, and an instance created or deleted
while a client pages moves no other instance between pages.
*/

import { randomUUID } from 'node:crypto';

import { checkPrincipal, type Access, type Action, type Principal } from '../access.js';
import type { FrozenJSON } from '../behaviors/behavior.js';
import { Execution, checkParams } from '../behaviors/execution.js';
import { deepFreeze } from '../behaviors/json.js';
import { EngineError, InstanceValidationError, type ValidationIssue } from '../errors.js';
import { appendEvent, nextSeq, type OperationChange } from '../events/log.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import type { SchemaCatalog, SchemaRecord, VersionRuntime } from '../registry/catalog.js';
import { checkSchemaName } from '../registry/document.js';
import { readOnlyIssue } from '../registry/validator.js';
import type { Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { isPlainObject, jsonEqual, mergePatch, setMember } from './patch.js';

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
  /**
   * The instance: its own fields as stored, then each field its behaviors
   * declare that has a value, in the type's behavior order.
   */
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

export interface InvokeOptions extends InstanceTarget {
  /**
   * As UpdateOptions.expectedSeq: the operation is refused with
   * seq_mismatch unless the instance is still at this sequence, checked
   * before any guard runs, inside the write transaction of an operation
   * that writes.
   */
  expectedSeq?: number;
}

/** What an operation returns, with the instance's sequence after it: its entity tag. */
export interface OperationOutcome {
  result: unknown;
  seq: number;
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
      const runtime = this.catalog.runtimeOf(record);
      this.validate(namespace, record, runtime, data);
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
      const execution = this.execution(runtime, record, namespace, principal, now, id, JSON.parse(json) as Record<string, unknown>, true);
      execution.initialize();
      execution.afterChange({ kind: 'create' });
      const instance = toInstance(this.row(namespace, schema, id) as Row, execution.fields());
      appendEvent(this.storage, {
        kind: 'create',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        actor: principal.subject,
        at: now,
        change: JSON.stringify(instance.data),
      });
      return instance;
    });
  }

  /** get returns an instance, or undefined when the namespace has none with the id. */
  get(principal: Principal, schema: string, id: string, options: InstanceTarget = {}): InstanceRecord | undefined {
    const namespace = this.target(principal, 'read', schema, options);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const row = this.row(namespace, schema, id);
    return row ? this.read(runtime, record, namespace, principal, this.clock(), row) : undefined;
  }

  /** list returns a page of a schema's instances in creation order. */
  list(principal: Principal, schema: string, options: ListOptions = {}): InstancePage {
    const namespace = this.target(principal, 'read', schema, options);
    const limit = pageSize(options.limit);
    const after = options.cursor === undefined ? 0 : decodeCursor(options.cursor);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const now = this.clock();
    const rows = this.storage.all(
      `SELECT ${COLUMNS} FROM engine_instances
       WHERE namespace = ? AND schema = ? AND position > ?
       ORDER BY position LIMIT ?`,
      [namespace, schema, after, limit + 1]
    );
    const items = rows.slice(0, limit);
    const next = rows.length > limit ? encodeCursor(Number(items[items.length - 1].position)) : null;
    return { items: items.map((row) => this.read(runtime, record, namespace, principal, now, row)), next };
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
      const runtime = this.catalog.runtimeOf(record);
      const readOnly: ValidationIssue[] = [];
      for (const [key, value] of Object.entries(patch)) {
        const behavior = runtime.composition.fields.get(key);
        if (behavior !== undefined && value !== undefined) {
          readOnly.push(readOnlyIssue(key, behavior.behavior.name));
        }
      }
      if (readOnly.length > 0) {
        throw new InstanceValidationError(namespace, schema, record.version as number, readOnly);
      }
      const row = this.existing(namespace, schema, id);
      matchSeq(row, options.expectedSeq);
      const current = JSON.parse(String(row.data)) as Record<string, unknown>;
      const merged = mergePatch(current, patch) as Record<string, unknown>;
      this.validate(namespace, record, runtime, merged);
      if (jsonEqual(merged, current)) {
        return this.read(runtime, record, namespace, principal, now, row);
      }
      const execution = this.execution(runtime, record, namespace, principal, now, id, current, true);
      const frozenPatch = deepFreeze(JSON.parse(JSON.stringify(patch)) as FrozenJSON);
      execution.guard({ kind: 'update', patch: frozenPatch, after: deepFreeze(JSON.parse(JSON.stringify(merged)) as FrozenJSON) });
      const before = execution.fields();
      const seq = Number(row.seq) + 1;
      this.storage.run(
        `UPDATE engine_instances
         SET data = ?, version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
         WHERE namespace = ? AND schema = ? AND id = ?`,
        [JSON.stringify(merged), record.version as number, record.namespace, seq, now, principal.subject, namespace, schema, id]
      );
      execution.setData(merged);
      execution.afterChange({ kind: 'update', patch: frozenPatch, before: deepFreeze(current) });
      const after = execution.fields();
      appendEvent(this.storage, {
        kind: 'update',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        actor: principal.subject,
        at: now,
        change: JSON.stringify({ ...frozenPatch, ...fieldPatch(before, after) }),
      });
      return toInstance(this.row(namespace, schema, id) as Row, after);
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
      const record = this.live(namespace, schema);
      const runtime = this.catalog.runtimeOf(record);
      const row = this.row(namespace, schema, id);
      if (!row) {
        return false;
      }
      matchSeq(row, options.expectedSeq);
      const execution = this.execution(runtime, record, namespace, principal, now, id, JSON.parse(String(row.data)) as Record<string, unknown>, true);
      execution.guard({ kind: 'delete' });
      execution.deleting();
      this.storage.run('DELETE FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?', [namespace, schema, id]);
      execution.afterChange({ kind: 'delete' });
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

  /**
   * invoke calls a behavior operation on an instance and returns its
   * result. It asks the policy for write when the operation's declaration
   * says it writes, and read otherwise, with the operation's name; an
   * operation that writes runs in a transaction and appends an operation
   * event, which moves the instance's seq, its entity tag, as an update
   * does. It moves it even when no field changes, since the engine cannot
   * see what the operation changed in its behavior's own tables. A schema
   * or operation the namespace does not have is not_found to a principal
   * that may read the schema, and forbidden to one that may not. With
   * expectedSeq, the instance must still be at that sequence.
   */
  invoke(principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options: InvokeOptions = {}): unknown {
    return this.operate(principal, schema, id, operation, params, options).result;
  }

  /**
   * operate is invoke, returning the result with the instance's sequence
   * after the call: the next one for an operation that writes, the one it
   * read for an operation that does not.
   */
  operate(principal: Principal, schema: string, id: string, operation: string, params: unknown = {}, options: InvokeOptions = {}): OperationOutcome {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    checkSchemaName(schema);
    const record = this.catalog.find(schema, namespace, 'live');
    if (!record) {
      this.access.require(principal, 'read', namespace, schema);
      throw new EngineError('not_found', `schema ${schema} has no live version in namespace ${namespace}`);
    }
    const runtime = this.catalog.runtimeOf(record);
    const spec = typeof operation === 'string' ? runtime.composition.operations.get(operation) : undefined;
    if (!spec) {
      this.access.require(principal, 'read', namespace, schema);
      const known = [...runtime.composition.operations.keys()];
      throw new EngineError(
        'not_found',
        `schema ${schema} has no operation ${String(operation)} (its behaviors' operations: ${known.length > 0 ? known.join(', ') : 'none'})`
      );
    }
    this.access.require(principal, spec.writes ? 'write' : 'read', namespace, schema, spec.name);
    checkExpectedSeq(options.expectedSeq);
    const checked = checkParams(spec, params);
    const now = this.clock();
    if (!spec.writes) {
      const row = this.existing(namespace, schema, id);
      matchSeq(row, options.expectedSeq);
      const result = this.execution(runtime, record, namespace, principal, now, id, JSON.parse(String(row.data)) as Record<string, unknown>, false).invoke(
        spec,
        checked
      );
      return { result, seq: Number(row.seq) };
    }
    return this.storage.transaction(() => {
      const row = this.existing(namespace, schema, id);
      matchSeq(row, options.expectedSeq);
      const execution = this.execution(runtime, record, namespace, principal, now, id, JSON.parse(String(row.data)) as Record<string, unknown>, true);
      const before = execution.fields();
      const result = execution.invoke(spec, checked);
      execution.afterChange({ kind: 'operation', behavior: spec.behavior.name, operation: spec.name, params: checked });
      const after = execution.fields();
      const seq = Number(row.seq) + 1;
      this.storage.run(
        `UPDATE engine_instances SET version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
         WHERE namespace = ? AND schema = ? AND id = ?`,
        [record.version as number, record.namespace, seq, now, principal.subject, namespace, schema, id]
      );
      const change: OperationChange = { behavior: spec.behavior.name, operation: spec.name, params: checked, patch: fieldPatch(before, after) };
      appendEvent(this.storage, {
        kind: 'operation',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        actor: principal.subject,
        at: now,
        change: JSON.stringify(change),
      });
      return { result, seq };
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

  private validate(namespace: string, record: SchemaRecord, runtime: VersionRuntime, data: unknown): void {
    const issues = runtime.validator.validate(data);
    if (issues.length > 0) {
      throw new InstanceValidationError(namespace, record.name, record.version as number, issues);
    }
  }

  private execution(
    runtime: VersionRuntime,
    record: SchemaRecord,
    namespace: string,
    principal: Principal,
    now: number,
    id: string,
    data: Record<string, unknown>,
    writable: boolean
  ): Execution {
    return new Execution(
      this.storage,
      runtime.composition,
      runtime.prefixes,
      { namespace, schema: record.name, version: record.version as number, principal, now },
      id,
      data,
      writable
    );
  }

  // read returns a stored instance with its behaviors' fields.
  private read(runtime: VersionRuntime, record: SchemaRecord, namespace: string, principal: Principal, now: number, row: Row): InstanceRecord {
    if (runtime.composition.fields.size === 0) {
      return toInstance(row, {});
    }
    const data = JSON.parse(String(row.data)) as Record<string, unknown>;
    return toInstance(row, this.execution(runtime, record, namespace, principal, now, String(row.id), data, false).fields());
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

// fieldPatch is the merge patch from one reading of an instance's behavior
// fields to another: each changed or new field, and null for one that has
// no value any more.
function fieldPatch(before: Record<string, unknown>, after: Record<string, unknown>): Record<string, unknown> {
  const patch: Record<string, unknown> = {};
  for (const [field, value] of Object.entries(after)) {
    if (!Object.prototype.hasOwnProperty.call(before, field) || !jsonEqual(before[field], value)) {
      setMember(patch, field, value);
    }
  }
  for (const field of Object.keys(before)) {
    if (!Object.prototype.hasOwnProperty.call(after, field)) {
      setMember(patch, field, null);
    }
  }
  return patch;
}

function toInstance(row: Row, fields: Record<string, unknown>): InstanceRecord {
  return {
    namespace: String(row.namespace),
    schema: String(row.schema),
    id: String(row.id),
    schemaNamespace: String(row.schema_namespace),
    version: Number(row.version),
    seq: Number(row.seq),
    data: { ...(JSON.parse(String(row.data)) as Record<string, unknown>), ...fields },
    createdAt: Number(row.created_at),
    createdBy: String(row.created_by),
    updatedAt: Number(row.updated_at),
    updatedBy: String(row.updated_by),
  };
}
