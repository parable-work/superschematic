/*
Instances of a schema's instance type, keyed by namespace, schema name and
id. The key includes the namespace, so an id in one namespace says nothing
about another. An instance is written with the live version of its schema
(its own namespace's, or the shared one's) and validated by it; its row
records that version. update is a JSON merge patch (RFC 7386, patch.ts),
validated after the merge. Each write appends its event in the same
transaction (events/log.ts). A write in an archived namespace, a create,
an update, a delete or a writing operation, is refused
(namespace_archived) once the policy has allowed it; reads go on.

The behaviors of the live version run with every call (behaviors/): a
create and an update ask their validate about the fields the write would
store once the live version accepts them, whose issues refuse it as the
live version's do; a create checks the parameters it gives them, asks
their guards, then runs their initialize, each with its own parameters,
then their afterChange; an update and a delete ask their guards first,
each with its entry of the caller's preconditions, and run afterChange
after. The preconditions are checked against their schemas with the
call's other arguments, before the instance is read, so before any
validate, and only a guard judges what one asserts. A read adds the
fields they declare beside the instance's own, which are theirs to
change: a create or an update that sets one is refused (readOnly).
invoke calls one of their operations: it checks the parameters, asks the
policy for write or read as the operation's declaration says, and runs
every guard, then the handler, in the write transaction for an operation
that writes, which appends an operation event. invokeSchema calls a
schema-level operation, which has no instance. Anything that throws
rolls the whole call back.

Each call is one Chain (behaviors/execution.ts) across every instance its
behaviors reach. The store is their reach (D16, amended): it reads other
instances, invokes their operations and creates new ones as the chain's
principal, asking the policy each time, records the references behaviors
hold, asks the guards of the behaviors that refer to an instance before
it changes and runs their hooks after, each reference's as far as it
hears the change (instances/references.ts), and refuses a delete that
leaves a reference to the deleted instance behind. The runner's work reaches
through it too, on a chain whose cause each event it appends records.

list pages through a schema's instances in creation order with an opaque
cursor. Each page is one indexed range read of at most the page size, so
a list never loads the whole table, and an instance created or deleted
while a client pages moves no other instance between pages. Its where
keeps the instances whose fields hold the values it names, a page still
reading a bounded number of rows in creation order (filters.ts). lookup
reads the one instance whose unique fields hold a key's values, through
their index.

The indexes of the instance type's own fields (indexes.ts) refuse a
create or an update that would repeat a unique field's value in the
namespace (UniqueConflictError, conflict), and keep each field they
cover inline in the row, whatever its length.

A row keeps each own field whose JSON is longer than the value store's
threshold as a ref, the value stored once by hash (values/store.ts), and
so does each event a write appends. Every read puts the values back: get,
list, the behaviors' contexts and reads, validation, before(). A get or a
list that asks for valueRefs returns the refs instead, with the pointers
to them. A write reuses the ref of a member it leaves alone, so a write
that changes only small fields or a behavior's columns (a lease's
heartbeat) neither hashes nor rewrites a large value, and an operation
whose behaviors never read the instance's own fields never loads one.
*/

import { randomUUID } from 'node:crypto';

import type { PermissionMatcher } from '@superschematic/http-runtime';

import { checkPrincipal, type Access, type Principal, type SchemaAction } from '../access.js';
import type { BoundBehavior } from '../behaviors/composition.js';
import type { FrozenJSON, GuardRequest, InstanceChange, Reference, ValidationRequest } from '../behaviors/behavior.js';
import {
  Chain,
  Execution,
  SchemaExecution,
  checkCreateParams,
  checkParams,
  checkPreconditions,
  validationIssues,
  vetoOf,
  type Preconditions,
  type Reach,
  type ReferenceSource,
} from '../behaviors/execution.js';
import { deepFreeze } from '../behaviors/json.js';
import type { OperationSpec } from '../behaviors/registry.js';
import { synchronous } from '../behaviors/storage.js';
import { BehaviorError, BehaviorVetoError, CursorExpiredError, EngineError, InstanceValidationError, UniqueConflictError, type ValidationIssue } from '../errors.js';
import { actorOf, appendEvent, logFloor, logHead, nextSeq, type EngineEvent, type OperationChange } from '../events/log.js';
import { foldEvent, instanceBase } from '../events/retention.js';
import type { Namespaces } from '../namespaces.js';
import { pageSize } from '../paging.js';
import type { SchemaCatalog, SchemaRecord, VersionRuntime } from '../registry/catalog.js';
import { checkSchemaName } from '../registry/document.js';
import { readOnlyIssue } from '../registry/validator.js';
import { SQLITE_CONSTRAINT_UNIQUE, SqliteError, type Row } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import { diffPatch, isPlainObject, jsonEqual, mergePatch, setMember } from './patch.js';
import { joinStowed, refsOf, refsText, valuesOf, type Stowed, type ValueHolder, type ValueStore } from '../values/store.js';
import { ReferenceTable, moves, type IncomingReference, type Move } from './references.js';
import { bind, filteredPage, lookupCondition, parseLookupKey, parseWhere } from './filters.js';
import { failedIndex } from './indexes.js';

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
  /**
   * For a read that asked for valueRefs: the JSON pointers into data of
   * the own fields that hold a ref, `{ "$value": <hash>, "bytes": <n> }`,
   * in place of a value the value store holds. Absent when none does.
   */
  valueRefs?: string[];
}

/** Where a call looks: a namespace, `default` when absent. */
export interface InstanceTarget {
  namespace?: string;
}

/** What a get takes beside the schema and the id. */
export interface GetOptions extends InstanceTarget {
  /**
   * Return each own field the value store holds as its ref, with the
   * pointers to them in valueRefs, rather than its value, which a client
   * reads by its hash when it needs it.
   */
  valueRefs?: boolean;
}

export interface CreateOptions extends InstanceTarget {
  /** The id; the engine's id generator makes one when absent. */
  id?: string;
  /**
   * The parameters the create gives the type's behaviors, by behavior
   * name, each held to that behavior's createParamsSchema and handed to
   * its initialize: Links' links and Dependencies' blockers, say, which
   * then hold from the create on, in its transaction.
   */
  behaviors?: Record<string, unknown>;
}

export interface UpdateOptions extends InstanceTarget {
  /**
   * The sequence the caller last read. The update is refused with
   * seq_mismatch unless the instance is still at it, checked inside the
   * write transaction.
   */
  expectedSeq?: number;
  /**
   * Preconditions by behavior, such as `{ Lease: { token: 7 } }`: each
   * entry names a behavior the type composes that declares a
   * preconditionSchema, and holds what that schema accepts, or the call
   * is refused (PreconditionsError, invalid_argument). Each behavior's
   * guard gets its own entry as the request's precondition, and decides
   * what it asserts. A patch that changes nothing asks no guard.
   */
  preconditions?: Readonly<Record<string, unknown>>;
}

export interface DeleteOptions extends InstanceTarget {
  /** As UpdateOptions.expectedSeq. */
  expectedSeq?: number;
  /** As UpdateOptions.preconditions. */
  preconditions?: Readonly<Record<string, unknown>>;
}

export interface InvokeOptions extends InstanceTarget {
  /**
   * As UpdateOptions.expectedSeq: the operation is refused with
   * seq_mismatch unless the instance is still at this sequence, checked
   * before any guard runs, inside the write transaction of an operation
   * that writes.
   */
  expectedSeq?: number;
  /** As UpdateOptions.preconditions, handed to the guards the operation asks. */
  preconditions?: Readonly<Record<string, unknown>>;
}

/** Where a schema-level operation runs: a namespace, `default` when absent. */
export type InvokeSchemaOptions = InstanceTarget;

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
  /** As GetOptions.valueRefs. */
  valueRefs?: boolean;
  /**
   * Field values the instances hold, by field: a value, null for none
   * (absent or null), or a list of 1 to 100 of which the instance holds
   * one. A field is an own top-level field that holds a string, a number
   * or a boolean, or one a behavior lets a list filter on (Workflow's
   * status, or a member of a behavior's field, lease.holder). A filtered
   * page can hold fewer instances than limit, even none, while next is
   * not null.
   */
  where?: Readonly<Record<string, unknown>>;
}

/** One page of a list. */
export interface InstancePage {
  items: InstanceRecord[];
  /** The cursor of the next page; null after the last. */
  next: string | null;
}

const COLUMNS =
  'position, namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, value_refs';

/** How a read returns an instance. */
interface ReadMode {
  /** The behavior fields to read, every one when undefined. */
  readonly fields?: readonly string[];
  /** Refs in place of the values the value store holds, with valueRefs. */
  readonly valueRefs?: boolean;
  /** Values the caller may change, not the frozen ones the engine shares. */
  readonly copy: boolean;
}

export class InstanceStore {
  private readonly references: ReferenceTable;
  /**
   * What the behaviors of every call reach beyond their instance; the
   * engine's runner gives its work the same reach. Not for callers: each
   * method takes the chain it acts in.
   */
  readonly reach: Reach;

  constructor(
    private readonly storage: Storage,
    private readonly namespaces: Namespaces,
    private readonly catalog: SchemaCatalog,
    private readonly access: Access,
    private readonly ids: () => string,
    private readonly clock: () => number,
    /** Answers a behavior's can(); EngineOptions.permissionMatcher. */
    private readonly permissions: PermissionMatcher
  ) {
    this.references = new ReferenceTable(storage);
    this.reach = {
      read: (chain, schema, ids, fields) => this.readFor(chain, schema, ids, fields),
      invoke: (chain, from, schema, id, operation, params, writes, preconditions) =>
        this.invokeFor(chain, from, schema, id, operation, params, writes, preconditions),
      invokeSchema: (chain, from, schema, operation, params, writes) => this.invokeSchemaFor(chain, from, schema, operation, params, writes),
      create: (chain, from, schema, data, id, behaviors, writes) => this.createFor(chain, from, schema, data, id, behaviors, writes),
      allowRead: (chain, schema) => {
        checkSchemaName(schema);
        this.access.require(chain.principal, 'read', chain.namespace, schema);
      },
      before: (chain, from, event) => this.beforeFor(chain, from, event),
      config: (chain, own, schema, behavior) => this.configFor(chain, own, schema, behavior),
      readable: (chain, schema) => {
        checkSchemaName(schema);
        return this.access.allows(chain.principal, 'read', chain.namespace, schema);
      },
      addReference: (chain, source, target) => this.addReference(chain, source, target),
      removeReference: (chain, source, target) => this.references.remove(chain.namespace, source, target),
      listReferences: (chain, source) => this.references.from(chain.namespace, source),
      guardReferences: (chain, schema, id, request) => this.guardReferences(chain, schema, id, request),
      writeOwn: (chain, schema, id, data, changed) => this.writeOwn(chain.namespace, schema, id, data, changed),
    };
  }

  private get values(): ValueStore {
    return valuesOf(this.storage);
  }

  /**
   * create validates data against the schema's live version and the
   * behaviors' parameters against their createParamsSchema, and stores the
   * instance under a new id once every guard allows it.
   */
  create(principal: Principal, schema: string, data: unknown, options: CreateOptions = {}): InstanceRecord {
    const namespace = this.target(principal, 'write', schema, options);
    const id = options.id ?? this.ids();
    checkId(id);
    const chain = this.chain(principal, namespace);
    return this.storage.transaction(() => this.insert(chain, schema, id, data, options.behaviors));
  }

  // insert creates an instance in the chain's namespace inside the
  // chain's transaction: validate the data, against the live version and
  // then the behaviors' validate, and the behaviors' parameters, insert,
  // every guard, every initialize with its parameters, every afterChange,
  // then the create event, which records the chain's cause. The policy has
  // been asked.
  private insert(chain: Chain, schema: string, id: string, data: unknown, behaviors: unknown): InstanceRecord {
    const namespace = chain.namespace;
    const subject = chain.principal.subject;
    return chain.write(schema, id, () => {
      const record = this.live(namespace, schema);
      const runtime = this.catalog.runtimeOf(record);
      this.validate(namespace, record, runtime, data);
      this.validateBehaviors(chain, record, runtime, id, { kind: 'create', data: JSON.parse(JSON.stringify(data)) as FrozenJSON });
      const params = checkCreateParams(runtime.composition, schema, behaviors);
      const own = JSON.parse(JSON.stringify(data)) as Record<string, unknown>;
      const seq = nextSeq(this.storage, namespace, schema, id);
      const stowed = this.values.stow(own, '', undefined, runtime.inline);
      const inserted = this.unique(namespace, schema, runtime, own, () => this.storage.run(
        `INSERT INTO engine_instances
           (namespace, schema, id, schema_namespace, version, seq, data, created_at, created_by, updated_at, updated_by, value_refs)
         VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
         ON CONFLICT (namespace, schema, id) DO NOTHING`,
        [
          namespace,
          schema,
          id,
          record.namespace,
          record.version as number,
          seq,
          JSON.stringify(stowed.value),
          chain.now,
          subject,
          chain.now,
          subject,
          refsText(stowed.refs),
        ]
      ));
      if (inserted.changes === 0) {
        throw new EngineError('conflict', `${schema} ${id} already exists in namespace ${namespace}`);
      }
      if (stowed.hashes.size > 0) {
        this.values.hold(rowHolder(namespace, schema, id), stowed.hashes);
      }
      const execution = this.execution(chain, runtime, record, id, own, true);
      // Nothing refers to a new instance, so no guardReference is asked.
      execution.guard({ kind: 'create', data: execution.current(), behaviors: params }, false);
      execution.initialize(params);
      execution.afterChange({ kind: 'create' });
      const fields = execution.fields();
      const instance = toInstance(this.row(namespace, schema, id) as Row, own, fields);
      // The event's instance keeps the row's refs, and a large field of a
      // behavior's goes to the store as an own field does.
      const change = this.values.stow({ ...stowed.value, ...fields }, '', new Set(stowed.refs));
      appendEvent(this.storage, {
        kind: 'create',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        ...actorOf(chain.principal),
        at: chain.now,
        change: JSON.stringify(change.value),
        cause: chain.cause,
        values: change,
      });
      return instance;
    });
  }

  /**
   * get returns an instance, or undefined when the namespace has none with
   * the id. With valueRefs, each own field the value store holds is its
   * ref, and valueRefs lists them.
   */
  get(principal: Principal, schema: string, id: string, options: GetOptions = {}): InstanceRecord | undefined {
    const namespace = this.target(principal, 'read', schema, options);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const row = this.row(namespace, schema, id);
    return row ? this.read(this.chain(principal, namespace), runtime, record, row, { valueRefs: options.valueRefs === true, copy: true }) : undefined;
  }

  /**
   * list returns a page of a schema's instances in creation order; with
   * where, of the ones whose fields hold the values it names.
   */
  list(principal: Principal, schema: string, options: ListOptions = {}): InstancePage {
    const namespace = this.target(principal, 'read', schema, options);
    const limit = pageSize(options.limit);
    const after = options.cursor === undefined ? 0 : decodeCursor(options.cursor);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const chain = this.chain(principal, namespace);
    const terms = parseWhere(options.where, runtime.filters, schema);
    let items: Row[];
    let next: string | null;
    if (terms.length === 0) {
      const rows = this.storage.all(
        `SELECT ${COLUMNS} FROM engine_instances
         WHERE namespace = ? AND schema = ? AND position > ?
         ORDER BY position LIMIT ?`,
        [namespace, schema, after, limit + 1]
      );
      items = rows.slice(0, limit);
      next = rows.length > limit ? encodeCursor(Number(items[items.length - 1].position)) : null;
    } else {
      const page = filteredPage(this.storage, { namespace, schema, holder: record.namespace, indexes: runtime.indexes, terms }, after, limit);
      items =
        page.positions.length === 0
          ? []
          : this.storage.all(
              `SELECT ${COLUMNS} FROM engine_instances
               WHERE namespace = ? AND schema = ? AND position IN (${page.positions.map(() => '?').join(', ')})
               ORDER BY position`,
              [namespace, schema, ...page.positions]
            );
      next = page.next === null ? null : encodeCursor(page.next);
    }
    const mode: ReadMode = { valueRefs: options.valueRefs === true, copy: true };
    return { items: items.map((row) => this.read(chain, runtime, record, row, mode)), next };
  }

  /**
   * lookup returns the instance whose unique fields hold a key's values,
   * or undefined when the namespace has none. The key names the fields of
   * one unique index of the instance type, `{ slug: 'models/gpt' }` for a
   * @unique field, each with one value of its type; any other key is
   * invalid_argument. With valueRefs, as get.
   */
  lookup(principal: Principal, schema: string, key: unknown, options: GetOptions = {}): InstanceRecord | undefined {
    const namespace = this.target(principal, 'read', schema, options);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const { index, values } = parseLookupKey(key, runtime.indexes, runtime.filters, schema);
    const row = this.storage.get(`SELECT ${COLUMNS} FROM engine_instances WHERE ${lookupCondition(record.namespace, schema, index)} LIMIT 1`, [
      namespace,
      ...values.map(bind),
    ]);
    return row ? this.read(this.chain(principal, namespace), runtime, record, row, { valueRefs: options.valueRefs === true, copy: true }) : undefined;
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
    const chain = this.chain(principal, namespace);
    return this.storage.transaction(() =>
      chain.write(schema, id, () => {
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
        const preconditions = checkPreconditions(runtime.composition, schema, options.preconditions);
        const row = this.existing(namespace, schema, id);
        matchSeq(row, options.expectedSeq);
        // current and merged become the caller's record, so they are its
        // own copies; frozen is what the behaviors see of the row.
        const current = this.ownOf(row, true);
        const frozen = deepFreeze(this.ownOf(row, false));
        const merged = mergePatch(current, patch) as Record<string, unknown>;
        this.validate(namespace, record, runtime, merged);
        this.validateBehaviors(chain, record, runtime, id, {
          kind: 'update',
          before: frozen,
          after: JSON.parse(JSON.stringify(merged)) as FrozenJSON,
        });
        if (jsonEqual(merged, current)) {
          return this.read(chain, runtime, record, row, { copy: true });
        }
        const execution = this.execution(chain, runtime, record, id, () => frozen, true);
        const frozenPatch = deepFreeze(JSON.parse(JSON.stringify(patch)) as FrozenJSON);
        execution.guard({ kind: 'update', patch: frozenPatch, after: deepFreeze(JSON.parse(JSON.stringify(merged)) as FrozenJSON) }, true, preconditions);
        const before = execution.fields();
        const seq = Number(row.seq) + 1;
        this.writeOwn(namespace, schema, id, merged, new Set(Object.keys(patch)), runtime);
        this.storage.run(
          `UPDATE engine_instances
           SET version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
           WHERE namespace = ? AND schema = ? AND id = ?`,
          [record.version as number, record.namespace, seq, chain.now, principal.subject, namespace, schema, id]
        );
        execution.setData(merged);
        const change: InstanceChange = { kind: 'update', patch: frozenPatch, before: frozen };
        execution.afterChange(change);
        const after = execution.fields();
        const logged = this.values.stow({ ...frozenPatch, ...diffPatch(before, after) });
        appendEvent(this.storage, {
          kind: 'update',
          namespace,
          schema,
          instanceId: id,
          seq,
          version: record.version as number,
          ...actorOf(principal),
          at: chain.now,
          change: JSON.stringify(logged.value),
          cause: chain.cause,
          values: logged,
        });
        const updated = toInstance(this.row(namespace, schema, id) as Row, merged, after);
        this.referenced(chain, schema, id, change, moves({ ...current, ...before }, updated.data));
        return updated;
      })
    );
  }

  /**
   * delete removes an instance and appends its delete event; false when
   * there is none. With expectedSeq, the instance must still be at that
   * sequence. The references its behaviors recorded go with it; a
   * reference to it that a referencing behavior's hook leaves behind
   * refuses the delete (BehaviorError).
   */
  delete(principal: Principal, schema: string, id: string, options: DeleteOptions = {}): boolean {
    const namespace = this.target(principal, 'write', schema, options);
    checkExpectedSeq(options.expectedSeq);
    const chain = this.chain(principal, namespace);
    return this.storage.transaction(() =>
      chain.write(schema, id, () => {
        const record = this.live(namespace, schema);
        const runtime = this.catalog.runtimeOf(record);
        const preconditions = checkPreconditions(runtime.composition, schema, options.preconditions);
        const row = this.row(namespace, schema, id);
        if (!row) {
          return false;
        }
        matchSeq(row, options.expectedSeq);
        const execution = this.execution(chain, runtime, record, id, this.loader(row), true);
        execution.guard({ kind: 'delete' }, true, preconditions);
        execution.deleting();
        this.storage.run('DELETE FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?', [namespace, schema, id]);
        execution.afterChange({ kind: 'delete' });
        this.references.drop(namespace, schema, id);
        this.values.release(rowHolder(namespace, schema, id));
        appendEvent(this.storage, {
          kind: 'delete',
          namespace,
          schema,
          instanceId: id,
          seq: Number(row.seq) + 1,
          version: Number(row.version),
          ...actorOf(principal),
          at: chain.now,
          change: null,
          cause: chain.cause,
        });
        this.referenced(chain, schema, id, { kind: 'delete' }, []);
        return true;
      })
    );
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
   * that may read the schema, and forbidden to one that may not; so is a
   * schema-level operation, which invokeSchema calls. With expectedSeq,
   * the instance must still be at that sequence.
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
    const { record, runtime, spec } = this.operation(principal, namespace, schema, operation, 'instance');
    this.access.require(principal, spec.writes ? 'write' : 'read', namespace, schema, spec.name);
    if (spec.writes) {
      this.namespaces.requireWritable(namespace);
    }
    checkExpectedSeq(options.expectedSeq);
    const checked = checkParams(spec, params);
    const preconditions = checkPreconditions(runtime.composition, schema, options.preconditions);
    const chain = this.chain(principal, namespace);
    if (!spec.writes) {
      const row = this.existing(namespace, schema, id);
      matchSeq(row, options.expectedSeq);
      const execution = this.execution(chain, runtime, record, id, this.loader(row), false);
      return { result: execution.invoke(spec, checked, undefined, preconditions), seq: Number(row.seq) };
    }
    return this.storage.transaction(() => this.runOperation(chain, record, runtime, spec, id, checked, options.expectedSeq, preconditions));
  }

  /**
   * invokeSchema calls a schema-level operation, which has no instance,
   * and returns its result. It asks the policy as invoke does, runs the
   * handler in a transaction when the operation writes, and appends no
   * event: what it changes, it changes through the operations it invokes,
   * whose events record it. An instance operation is not_found here, as
   * one the schema does not have is.
   */
  invokeSchema(principal: Principal, schema: string, operation: string, params: unknown = {}, options: InvokeSchemaOptions = {}): unknown {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    checkSchemaName(schema);
    const { record, runtime, spec } = this.operation(principal, namespace, schema, operation, 'schema');
    this.access.require(principal, spec.writes ? 'write' : 'read', namespace, schema, spec.name);
    if (spec.writes) {
      this.namespaces.requireWritable(namespace);
    }
    const checked = checkParams(spec, params);
    return this.runSchemaOperation(this.chain(principal, namespace), record, runtime, spec, checked);
  }

  // runSchemaOperation runs a schema-level operation's handler, in a
  // transaction for a writing one: a savepoint inside a call's.
  private runSchemaOperation(chain: Chain, record: SchemaRecord, runtime: VersionRuntime, spec: OperationSpec, checked: FrozenJSON): unknown {
    const run = () => new SchemaExecution(this.storage, runtime, chain, this.reach, record.name, record.version as number).invoke(spec, checked);
    return spec.writes ? this.storage.transaction(run) : run();
  }

  // operation finds an operation of a schema's live version with a scope.
  // Without one, a principal that may read the schema learns there is
  // none, and one that may not is refused.
  private operation(
    principal: Principal,
    namespace: string,
    schema: string,
    operation: string,
    scope: 'instance' | 'schema'
  ): { record: SchemaRecord; runtime: VersionRuntime; spec: OperationSpec } {
    const record = this.catalog.find(schema, namespace, 'live');
    if (!record) {
      this.access.require(principal, 'read', namespace, schema);
      throw new EngineError('not_found', `schema ${schema} has no live version in namespace ${namespace}`);
    }
    const runtime = this.catalog.runtimeOf(record);
    const spec = typeof operation === 'string' ? runtime.composition.operations.get(operation) : undefined;
    if (!spec || spec.scope !== scope) {
      this.access.require(principal, 'read', namespace, schema);
      if (spec) {
        throw new EngineError(
          'not_found',
          scope === 'instance'
            ? `${schema}'s ${spec.name} is a schema-level operation: call it on the schema, with no instance`
            : `${schema}'s ${spec.name} is an instance operation: call it on an instance`
        );
      }
      const known = [...runtime.composition.operations.values()].filter((candidate) => candidate.scope === scope).map((candidate) => candidate.name);
      throw new EngineError(
        'not_found',
        `schema ${schema} has no ${scope === 'schema' ? 'schema-level ' : ''}operation ${String(operation)} (its behaviors' ${scope === 'schema' ? 'schema-level ' : ''}operations: ${known.length > 0 ? known.join(', ') : 'none'})`
      );
    }
    return { record, runtime, spec };
  }

  // runOperation runs a writing operation on an instance inside the call's
  // transaction: every guard, the handler, each afterChange, the next seq
  // and the operation event, then the hooks of the behaviors that refer to
  // the instance.
  private runOperation(
    chain: Chain,
    record: SchemaRecord,
    runtime: VersionRuntime,
    spec: OperationSpec,
    id: string,
    checked: FrozenJSON,
    expectedSeq: number | undefined,
    preconditions: Preconditions | undefined
  ): OperationOutcome {
    const namespace = chain.namespace;
    const schema = record.name;
    return chain.write(schema, id, () => {
      const row = this.existing(namespace, schema, id);
      matchSeq(row, expectedSeq);
      // The own fields load only when a behavior reads them, so an
      // operation that changes only its columns (a heartbeat) loads no
      // large value.
      const execution = this.execution(chain, runtime, record, id, this.loader(row), true);
      const before = execution.fields();
      const result = execution.invoke(spec, checked, undefined, preconditions);
      // An operation may change the instance's own fields through update();
      // afterChange gets them from before it, and the event carries the change.
      const own = execution.ownBefore();
      const ownAfter = own === undefined ? undefined : execution.current();
      const change: InstanceChange =
        own === undefined
          ? { kind: 'operation', behavior: spec.behavior.name, operation: spec.name, params: checked }
          : { kind: 'operation', behavior: spec.behavior.name, operation: spec.name, params: checked, before: own };
      execution.afterChange(change);
      const after = execution.fields();
      const seq = Number(row.seq) + 1;
      this.storage.run(
        `UPDATE engine_instances SET version = ?, schema_namespace = ?, seq = ?, updated_at = ?, updated_by = ?
         WHERE namespace = ? AND schema = ? AND id = ?`,
        [record.version as number, record.namespace, seq, chain.now, chain.principal.subject, namespace, schema, id]
      );
      const params = this.values.stow(checked, '/params');
      const patch = this.values.stow({ ...(own === undefined ? {} : diffPatch(own, ownAfter as FrozenJSON)), ...diffPatch(before, after) }, '/patch');
      const logged = joinStowed({ behavior: spec.behavior.name, operation: spec.name }, { params, patch });
      appendEvent(this.storage, {
        kind: 'operation',
        namespace,
        schema,
        instanceId: id,
        seq,
        version: record.version as number,
        ...actorOf(chain.principal),
        at: chain.now,
        change: JSON.stringify(logged.value satisfies Record<keyof OperationChange, unknown>),
        cause: chain.cause,
        values: logged,
      });
      this.referenced(chain, schema, id, change, own === undefined ? moves(before, after) : moves({ ...own, ...before }, { ...ownAfter, ...after }));
      return { result, seq };
    });
  }

  // readFor reads instances of a schema for a behavior, as the chain's
  // principal: read is asked once for the read.
  private readFor(chain: Chain, schema: string, ids: readonly string[], fields: readonly string[] | undefined): Map<string, InstanceRecord> {
    const namespace = chain.namespace;
    checkSchemaName(schema);
    this.access.require(chain.principal, 'read', namespace, schema);
    const record = this.live(namespace, schema);
    const runtime = this.catalog.runtimeOf(record);
    const found = new Map<string, InstanceRecord>();
    if (ids.length === 0) {
      return found;
    }
    const rows = this.storage.all(
      `SELECT ${COLUMNS} FROM engine_instances WHERE namespace = ? AND schema = ? AND id IN (${ids.map(() => '?').join(', ')})`,
      [namespace, schema, ...ids]
    );
    const byId = new Map(rows.map((row) => [String(row.id), row]));
    for (const id of ids) {
      const row = byId.get(id);
      if (row) {
        found.set(id, deepFreeze(this.read(chain, runtime, record, row, { fields, copy: false })));
      }
    }
    return found;
  }

  // invokeFor runs an instance operation a behavior invokes, as the
  // chain's principal, with the preconditions it gives, in the chain's
  // transaction: a savepoint for a writing one, which a write running up
  // the chain on the same instance refuses as a cycle.
  private invokeFor(
    chain: Chain,
    from: string,
    schema: string,
    id: string,
    operation: string,
    params: unknown,
    writes: boolean,
    given: unknown
  ): unknown {
    const namespace = chain.namespace;
    checkSchemaName(schema);
    const { record, runtime, spec } = this.operation(chain.principal, namespace, schema, operation, 'instance');
    if (spec.writes && !writes) {
      throw new BehaviorError(
        from,
        `a read cannot invoke ${spec.name} of ${schema}, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can`
      );
    }
    this.access.require(chain.principal, spec.writes ? 'write' : 'read', namespace, schema, spec.name);
    const checked = checkParams(spec, params);
    const preconditions = checkPreconditions(runtime.composition, schema, given);
    if (!spec.writes) {
      const row = this.existing(namespace, schema, id);
      return this.execution(chain, runtime, record, id, this.loader(row), false).invoke(spec, checked, undefined, preconditions);
    }
    if (chain.writing(schema, id)) {
      throw new BehaviorError(from, `invoking ${spec.name} of ${schema} ${id} is a cycle: a write of ${schema} ${id} is still running up this call`);
    }
    return this.storage.transaction(() => this.runOperation(chain, record, runtime, spec, id, checked, undefined, preconditions)).result;
  }

  // createFor creates an instance a behavior asks for, as the chain's
  // principal, who needs write on the schema, in the chain's transaction:
  // a savepoint, so a failure the behavior catches leaves nothing of it.
  // A read cannot create, and neither can a call up which the same
  // instance is being written (deleted, say).
  private createFor(
    chain: Chain,
    from: string,
    schema: string,
    data: Record<string, unknown>,
    id: string | undefined,
    behaviors: Record<string, unknown> | undefined,
    writes: boolean
  ): InstanceRecord {
    if (!writes) {
      throw new BehaviorError(
        from,
        `a read cannot create an instance of ${schema}; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can`
      );
    }
    checkSchemaName(schema);
    this.access.require(chain.principal, 'write', chain.namespace, schema);
    const instanceId = id ?? this.ids();
    checkId(instanceId);
    if (chain.writing(schema, instanceId)) {
      throw new BehaviorError(from, `creating ${schema} ${instanceId} is a cycle: a write of ${schema} ${instanceId} is still running up this call`);
    }
    return deepFreeze(this.storage.transaction(() => this.insert(chain, schema, instanceId, data, behaviors)));
  }

  // invokeSchemaFor runs a schema-level operation a behavior invokes, as
  // the chain's principal, in the chain's transaction; from a read, only a
  // read-only one.
  private invokeSchemaFor(chain: Chain, from: string, schema: string, operation: string, params: unknown, writes: boolean): unknown {
    const namespace = chain.namespace;
    checkSchemaName(schema);
    const { record, runtime, spec } = this.operation(chain.principal, namespace, schema, operation, 'schema');
    if (spec.writes && !writes) {
      throw new BehaviorError(
        from,
        `a read cannot invoke ${spec.name} of ${schema}, which writes; initialize, afterChange, afterReferenceChange, a writing operation and the runner's work can`
      );
    }
    this.access.require(chain.principal, spec.writes ? 'write' : 'read', namespace, schema, spec.name);
    return this.runSchemaOperation(chain, record, runtime, spec, checkParams(spec, params));
  }

  // beforeFor folds an instance's events before one of them into the
  // instance as the log had it then, as the chain's principal, who needs
  // read on the event's schema. The log records each change as a merge
  // patch of what a read returns (a create the whole instance, an update
  // its patch, an operation its patch), so folding them from its last
  // create gives it. Retention folds the events it prunes into the
  // instance's base, which the fold starts from; an event retention has
  // pruned is cursor_expired.
  private beforeFor(chain: Chain, from: string, event: EngineEvent): FrozenJSON | undefined {
    if (
      typeof event !== 'object' ||
      event === null ||
      typeof event.schema !== 'string' ||
      typeof event.instanceId !== 'string' ||
      typeof event.seq !== 'number' ||
      !Number.isSafeInteger(event.seq)
    ) {
      throw new BehaviorError(from, 'before() takes an instance event of the log');
    }
    if (event.namespace !== chain.namespace) {
      throw new BehaviorError(from, `before() reads the events of namespace ${chain.namespace}, not ${String(event.namespace)}`);
    }
    checkSchemaName(event.schema);
    this.access.require(chain.principal, 'read', chain.namespace, event.schema);
    const base = instanceBase(this.storage, chain.namespace, event.schema, event.instanceId);
    if (base !== undefined && event.seq <= base.seq) {
      const floor = logFloor(this.storage, this.namespaces, chain.namespace, false);
      throw new CursorExpiredError(chain.namespace, event.cursor, floor, logHead(this.storage));
    }
    const rows = this.storage.all(
      `SELECT kind, change, value_refs FROM engine_events INDEXED BY engine_events_instance
       WHERE namespace = ? AND schema = ? AND instance_id = ? AND seq < ? ORDER BY seq`,
      [chain.namespace, event.schema, event.instanceId, event.seq]
    );
    let data: unknown = base?.data;
    for (const row of rows) {
      // A change the log keeps in the value store gets its values back
      // before it folds in, since a later patch may merge into one.
      const change: unknown = row.change === null ? null : this.values.fill(JSON.parse(String(row.change)) as unknown, refsOf(row.value_refs));
      data = foldEvent(data, String(row.kind), change);
    }
    return data === undefined ? undefined : deepFreeze(data as FrozenJSON);
  }

  // configFor returns the config a schema's live version gives a
  // behavior, as the schema holds it; read is asked unless the schema is
  // the call's own.
  private configFor(chain: Chain, own: string, schema: string, behavior: string): unknown {
    const namespace = chain.namespace;
    checkSchemaName(schema);
    if (schema !== own) {
      this.access.require(chain.principal, 'read', namespace, schema);
    }
    const record = this.live(namespace, schema);
    const ref = ((record.document.types ?? {})[record.instanceType]?.behaviors ?? []).find((candidate) => candidate.name === behavior);
    return ref === undefined ? undefined : deepFreeze(ref.config === undefined ? {} : (JSON.parse(JSON.stringify(ref.config)) as unknown));
  }

  // addReference records a behavior's reference to an instance of the
  // chain's namespace, which must exist as the principal reads it.
  private addReference(chain: Chain, source: ReferenceSource, target: Reference): void {
    checkSchemaName(target.schema);
    this.access.require(chain.principal, 'read', chain.namespace, target.schema);
    if (!this.row(chain.namespace, target.schema, target.id)) {
      throw new EngineError('not_found', `${target.schema} ${target.id} does not exist in namespace ${chain.namespace}`);
    }
    this.references.add(chain.namespace, source, target);
  }

  // guardReferences asks the guardReference of each behavior that refers
  // to an instance, on its own instance, whoever the caller: before a
  // delete every reference, before another change the ones that hear
  // every change.
  private guardReferences(chain: Chain, schema: string, id: string, request: GuardRequest): void {
    const action = request.kind === 'operation' ? request.operation : request.kind;
    const asked = request.kind === 'delete' ? this.references.to(chain.namespace, schema, id) : this.references.guarding(chain.namespace, schema, id);
    for (const incoming of asked) {
      const source = this.source(chain, incoming);
      const implementation = source?.bound.behavior.implementation;
      if (!source || !implementation?.guardReference) {
        continue;
      }
      const answer = chain.nest(incoming.behavior, 'guardReference', () => {
        const answer: unknown = implementation.guardReference?.call(
          implementation,
          source.execution.view(source.bound),
          deepFreeze({ schema, id, key: incoming.key, ...(incoming.hears === undefined ? {} : { hears: incoming.hears }) }),
          request
        );
        synchronous(incoming.behavior, 'guardReference', answer);
        return answer;
      });
      const veto = vetoOf(source.bound.behavior, 'guardReference', answer);
      if (veto !== undefined) {
        throw new BehaviorVetoError(incoming.behavior, action, schema, id, veto);
      }
    }
  }

  // referenced runs the afterReferenceChange of each behavior that refers
  // to an instance, after the instance's change: after a delete every
  // reference, after another change the ones that hear the values it
  // moved. After a delete, a reference left behind by a behavior its
  // source still composes refuses the delete; one a behavior the source no
  // longer composes left is dropped.
  private referenced(chain: Chain, schema: string, id: string, change: InstanceChange, moved: readonly Move[]): void {
    const frozenChange = deepFreeze(change);
    const hearing = change.kind === 'delete' ? this.references.to(chain.namespace, schema, id) : this.references.hearing(chain.namespace, schema, id, moved);
    for (const incoming of hearing) {
      const target: Reference = deepFreeze({ schema, id, key: incoming.key, ...(incoming.hears === undefined ? {} : { hears: incoming.hears }) });
      // An earlier hook may have removed this reference.
      if (!this.references.has(chain.namespace, incoming, target)) {
        continue;
      }
      const source = this.source(chain, incoming);
      const implementation = source?.bound.behavior.implementation;
      if (!source || !implementation?.afterReferenceChange) {
        continue;
      }
      chain.nest(incoming.behavior, 'afterReferenceChange', () => {
        const result: unknown = implementation.afterReferenceChange?.call(
          implementation,
          source.execution.referenceContext(source.bound),
          target,
          frozenChange
        );
        synchronous(incoming.behavior, 'afterReferenceChange', result);
      });
    }
    if (change.kind !== 'delete') {
      return;
    }
    for (const incoming of this.references.to(chain.namespace, schema, id)) {
      if (!this.source(chain, incoming)) {
        this.references.dropOne(chain.namespace, schema, id, incoming);
        continue;
      }
      throw new BehaviorError(
        incoming.behavior,
        `${incoming.schema} ${incoming.id} still refers to ${schema} ${id}, which is deleted: afterReferenceChange must remove the reference, through an operation of ${incoming.schema} it invokes`
      );
    }
  }

  // source is the instance a reference starts at, read for its behavior's
  // guard or hook; undefined when its live version no longer composes the
  // behavior.
  private source(chain: Chain, incoming: IncomingReference): { bound: BoundBehavior; execution: Execution } | undefined {
    const record = this.catalog.find(incoming.schema, chain.namespace, 'live');
    if (!record) {
      return undefined;
    }
    const runtime = this.catalog.runtimeOf(record);
    const bound = runtime.composition.bound(incoming.behavior);
    const row = bound ? this.row(chain.namespace, incoming.schema, incoming.id) : undefined;
    if (!bound || !row) {
      return undefined;
    }
    return { bound, execution: this.execution(chain, runtime, record, incoming.id, this.loader(row), false) };
  }

  private chain(principal: Principal, namespace: string): Chain {
    return new Chain(principal, namespace, this.clock(), this.permissions);
  }

  // target resolves a call's namespace and asks the policy; a write is
  // then refused in an archived namespace.
  private target(principal: Principal, action: SchemaAction, schema: string, options: InstanceTarget): string {
    checkPrincipal(principal);
    const namespace = this.namespaces.resolve(options.namespace);
    checkSchemaName(schema);
    this.access.require(principal, action, namespace, schema);
    if (action === 'write') {
      this.namespaces.requireWritable(namespace);
    }
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

  // validateBehaviors asks each behavior's validate about the own fields a
  // write would store, which the live version accepts; their issues refuse
  // the write as the live version's do.
  private validateBehaviors(chain: Chain, record: SchemaRecord, runtime: VersionRuntime, id: string, request: ValidationRequest): void {
    const issues = validationIssues(runtime, chain, { schema: record.name, version: record.version as number, id }, request);
    if (issues.length > 0) {
      throw new InstanceValidationError(chain.namespace, record.name, record.version as number, issues);
    }
  }

  // execution runs the behaviors on an instance whose own fields are data,
  // or what a loader reads when a behavior first reads them.
  private execution(
    chain: Chain,
    runtime: VersionRuntime,
    record: SchemaRecord,
    id: string,
    data: Record<string, unknown> | (() => FrozenJSON),
    writable: boolean
  ): Execution {
    return new Execution(this.storage, runtime, chain, this.reach, { schema: record.name, version: record.version as number, id }, data, writable);
  }

  // read returns a stored instance with its behaviors' fields: every one,
  // or the ones named. Its own fields have their values back, copies when
  // the record goes to a caller, or with valueRefs the refs.
  private read(chain: Chain, runtime: VersionRuntime, record: SchemaRecord, row: Row, mode: ReadMode): InstanceRecord {
    const refs = mode.valueRefs === true ? refsOf(row.value_refs) : undefined;
    const own = mode.valueRefs === true ? (JSON.parse(String(row.data)) as Record<string, unknown>) : this.ownOf(row, mode.copy);
    const fields =
      runtime.composition.fields.size === 0 || mode.fields?.length === 0
        ? {}
        : this.execution(chain, runtime, record, String(row.id), this.loader(row), false).fields(mode.fields);
    const instance = toInstance(row, own, fields);
    return refs === undefined ? instance : { ...instance, valueRefs: refs };
  }

  // ownOf is a row's own fields with their values back: the ones the
  // engine shares, deep-frozen, or copies.
  private ownOf(row: Row, copy: boolean): Record<string, unknown> {
    return this.values.fill(JSON.parse(String(row.data)) as Record<string, unknown>, refsOf(row.value_refs), copy);
  }

  // loader reads a row's own fields for an execution, when a behavior
  // first reads them.
  private loader(row: Row): () => FrozenJSON {
    return () => this.ownOf(row, false);
  }

  // writeOwn stores an instance's own fields in its row, each large one by
  // hash but the ones an index covers. A member the write did not change
  // keeps the ref the row holds, so it is neither hashed nor written
  // again. A unique index the new values break refuses the write.
  private writeOwn(
    namespace: string,
    schema: string,
    id: string,
    data: Readonly<Record<string, unknown>>,
    changed: ReadonlySet<string>,
    runtime: VersionRuntime = this.catalog.runtimeOf(this.live(namespace, schema))
  ): void {
    const row = this.storage.get('SELECT data, value_refs FROM engine_instances WHERE namespace = ? AND schema = ? AND id = ?', [namespace, schema, id]);
    if (!row) {
      throw new EngineError('not_found', `${schema} ${id} does not exist in namespace ${namespace}`);
    }
    const stored = JSON.parse(String(row.data)) as Record<string, unknown>;
    const object: Record<string, unknown> = { ...data };
    const known = new Set<string>();
    const held = refsOf(row.value_refs) ?? [];
    for (const at of held) {
      const key = topKey(at);
      if (key !== undefined && !changed.has(key) && !runtime.inline.has(key) && Object.prototype.hasOwnProperty.call(data, key)) {
        setMember(object, key, stored[key]);
        known.add(at);
      }
    }
    const stowed: Stowed = this.values.stow(object, '', known, runtime.inline);
    this.unique(namespace, schema, runtime, data, () =>
      this.storage.run('UPDATE engine_instances SET data = ?, value_refs = ? WHERE namespace = ? AND schema = ? AND id = ?', [
        JSON.stringify(stowed.value),
        refsText(stowed.refs),
        namespace,
        schema,
        id,
      ])
    );
    // A row that held no value and holds none has no holds to change.
    if (held.length > 0 || stowed.hashes.size > 0) {
      this.values.hold(rowHolder(namespace, schema, id), stowed.hashes);
    }
  }

  // unique runs a write of an instance's row and turns SQLite's refusal of
  // it by a unique index of the version into the fields whose values
  // another instance of the namespace holds; any other failure passes.
  private unique<T>(namespace: string, schema: string, runtime: VersionRuntime, data: Readonly<Record<string, unknown>>, write: () => T): T {
    try {
      return write();
    } catch (error) {
      if (error instanceof SqliteError && error.code === SQLITE_CONSTRAINT_UNIQUE) {
        const name = failedIndex(error.message);
        const index = runtime.indexes.find((candidate) => candidate.name === name);
        if (index !== undefined) {
          throw new UniqueConflictError(
            namespace,
            schema,
            index.keys,
            index.keys.map((key) => data[key])
          );
        }
      }
      throw error;
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

function toInstance(row: Row, own: Readonly<Record<string, unknown>>, fields: Record<string, unknown>): InstanceRecord {
  return {
    namespace: String(row.namespace),
    schema: String(row.schema),
    id: String(row.id),
    schemaNamespace: String(row.schema_namespace),
    version: Number(row.version),
    seq: Number(row.seq),
    data: { ...own, ...fields },
    createdAt: Number(row.created_at),
    createdBy: String(row.created_by),
    updatedAt: Number(row.updated_at),
    updatedBy: String(row.updated_by),
  };
}

// rowHolder is who holds the values of an instance's row.
function rowHolder(namespace: string, schema: string, id: string): ValueHolder {
  return { namespace, schema, holder: 'instance', id, key: '' };
}

// topKey is the member a top-level JSON pointer names, or undefined for a
// deeper one.
function topKey(at: string): string | undefined {
  const match = /^\/([^/]*)$/.exec(at);
  return match === null ? undefined : match[1].replace(/~1/g, '/').replace(/~0/g, '~');
}
