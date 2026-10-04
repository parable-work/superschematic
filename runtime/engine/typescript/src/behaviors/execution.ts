/*
Running a type's behaviors for one call on one instance: its guards, its
initializers, an operation and the operations it calls, its after-change
hooks and its field readers. The instance store creates one Execution per
create, read, update, delete or operation, inside the write transaction
for a write, and one per other instance a behavior reads or invokes. They
share the call's Chain: its principal, namespace and time, how deep calls
nest and which instances are being written. Everything here is synchronous
(D16): a function that returns a promise is a BehaviorError, which rolls
the write back.

A create checks the parameters it gives the type's behaviors against
their createParamsSchema (checkCreateParams), asks every guard, and
hands each initialize its own. Each function gets a context for its own
behavior (behavior.ts). A guard and a field reader get a view: read-only
columns and SQL, reads of other instances and their read-only
operations. initialize, afterChange and a writing operation get a
writable context, call(), references, and invoke of writing operations
on other instances and create; a read-only operation gets one whose
writes refuse and whose call() and invoke reach only read-only
operations. An operation's context also has update(), which changes the
instance's own fields with instances.update's checks and every guard,
and validateUpdate(). A called operation runs in a savepoint, so a
failure the caller catches leaves nothing of it behind.

A guard vetoes with a reason, or a Veto with a code its declaration lists
(vetoes) and details; a handler, initialize and afterChange throw a
BehaviorVetoError, held to the same list. A code the declaration does not
list is a BehaviorError, so a client can rely on the codes a describe
document lists. A call's preconditions, by behavior, are checked against
each behavior's preconditionSchema (checkPreconditions) and handed to
that behavior's guard as its request's precondition; a request a
behavior's own code makes carries none.

What a behavior reaches beyond its instance goes through the Reach, which
the instance store implements: reads, invokes, creates and references
there ask the access policy as the chain's principal (D16, amended), and
so does each statement of the behavior's SQL that names the relation over
the schema's instances (sql.instances(), storage.ts). A schema-level
operation runs in a SchemaExecution, with the same reach and no instance.
The runner's work, a reaction or a schedule run, runs in a WorkExecution:
a schema-level context whose invokes and creates write, as the runner's
principal, on a chain whose cause the events it writes record.
*/

import type { PermissionMatcher } from '@superschematic/http-runtime';

import type { Principal } from '../access.js';
import {
  BehaviorError,
  BehaviorVetoError,
  CreateParamsError,
  EngineError,
  InstanceValidationError,
  OperationParamsError,
  PreconditionsError,
  type SchemaIssue,
  type ValidationIssue,
  type Veto,
} from '../errors.js';
import type { EngineEvent, EventCause } from '../events/log.js';
import type { InstanceRecord } from '../instances/store.js';
import { isPlainObject, jsonEqual, mergePatch, setMember } from '../instances/patch.js';
import { pointer } from '../registry/document.js';
import { readOnlyIssue } from '../registry/validator.js';
import type { SqlValue } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import type { SqlMode } from './sql.js';
import type {
  BehaviorReactions,
  BehaviorSchedule,
  CreateInstanceOptions,
  FrozenJSON,
  GuardRequest,
  InstanceChange,
  InstanceContext,
  InstanceView,
  Instances,
  InstancesInvokeOptions,
  OperationContext,
  OperationHandler,
  ReadOptions,
  Reference,
  ReactionContext,
  ReferenceContext,
  References,
  ScheduleContext,
  SchemaContext,
  SchemaOperationHandler,
  Schemas,
  WorkContext,
  WritableColumns,
} from './behavior.js';
import type { BoundBehavior, Composition } from './composition.js';
import { deepFreeze, jsonCopy } from './json.js';
import { BehaviorRegistry, type OperationSpec, type RegisteredBehavior } from './registry.js';
import { BehaviorSql, DeletedColumns, InstanceColumns, synchronous, type InstanceRelation } from './storage.js';

/** How deep call(), invokes and reads of other instances may nest together; deeper is a cycle. */
export const MAX_CALL_DEPTH = 16;

/** The most ids one getMany reads. */
export const MAX_BATCH_READ = 500;

/** The prefix of each bound behavior's storage, by behavior name. */
export type Prefixes = ReadonlyMap<string, string>;

/** A call's preconditions, checked: each behavior's entry by its name, deep-frozen. */
export type Preconditions = ReadonlyMap<string, FrozenJSON>;

/** Checks an instance's own fields against the live version (registry/validator.ts). */
export interface InstanceValidator {
  validate(value: unknown): ValidationIssue[];
}

/** What running a version needs: its behaviors, their storage and its validator. */
export interface Runtime {
  readonly composition: Composition;
  readonly prefixes: Prefixes;
  readonly validator: InstanceValidator;
}

/**
 * One engine call across every instance it reaches: who acts, in which
 * namespace, at what time, how deep calls nest, which instances have a
 * write running, and for the runner's work, the cause the events it
 * writes record. The instance store makes one per public call, and the
 * runner one per event and per schedule run.
 */
export class Chain {
  private depth = 0;
  private readonly writes = new Set<string>();

  constructor(
    readonly principal: Principal,
    readonly namespace: string,
    /** The clock's time for the whole call. */
    readonly now: number,
    /** Answers a context's can(): whether the principal's permissions cover a required one. */
    readonly permissions: PermissionMatcher,
    /** What caused the call, for the runner's work; undefined for a caller's. */
    readonly cause?: EventCause
  ) {}

  /** nest runs fn one level deeper, refusing past MAX_CALL_DEPTH with a BehaviorError of behavior. */
  nest<T>(behavior: string, what: string, fn: () => T): T {
    if (this.depth >= MAX_CALL_DEPTH) {
      throw new BehaviorError(behavior, `${what}: calls nest more than ${MAX_CALL_DEPTH} deep`);
    }
    this.depth += 1;
    try {
      return fn();
    } finally {
      this.depth -= 1;
    }
  }

  /** writing reports whether an instance has a write running up the chain. */
  writing(schema: string, id: string): boolean {
    return this.writes.has(instanceKey(schema, id));
  }

  /** write runs fn as the write of an instance: while it runs, invoking a writing operation of it is a cycle. */
  write<T>(schema: string, id: string, fn: () => T): T {
    const key = instanceKey(schema, id);
    if (this.writes.has(key)) {
      return fn();
    }
    this.writes.add(key);
    try {
      return fn();
    } finally {
      this.writes.delete(key);
    }
  }
}

function instanceKey(schema: string, id: string): string {
  return `${schema}\u0000${id}`;
}

/** Where a reference starts: a behavior on an instance. */
export interface ReferenceSource {
  readonly schema: string;
  readonly id: string;
  readonly behavior: string;
}

/**
 * What a behavior reaches beyond its own instance, as the chain's
 * principal, in the chain's namespace. The instance store implements it;
 * each method asks the access policy what its doc says.
 */
export interface Reach {
  /** Reads instances of a schema, with the named behavior fields (every one when undefined); asks read. */
  read(chain: Chain, schema: string, ids: readonly string[], fields: readonly string[] | undefined): Map<string, InstanceRecord>;
  /**
   * Invokes an instance operation as instances.invoke does, with the
   * caller's preconditions (unchecked), inside the chain's transaction;
   * asks write or read.
   */
  invoke(chain: Chain, from: string, schema: string, id: string, operation: string, params: unknown, writes: boolean, preconditions: unknown): unknown;
  /** Invokes a schema-level operation as instances.invokeSchema does, inside the chain's transaction; asks write or read. */
  invokeSchema(chain: Chain, from: string, schema: string, operation: string, params: unknown, writes: boolean): unknown;
  /**
   * Creates an instance as instances.create does, inside the chain's
   * transaction, in a savepoint; asks write. data is a JSON object, and
   * behaviors, when given, a JSON object of create parameters by behavior
   * name; a read (writes false) is refused.
   */
  create(
    chain: Chain,
    from: string,
    schema: string,
    data: Record<string, unknown>,
    id: string | undefined,
    behaviors: Record<string, unknown> | undefined,
    writes: boolean
  ): InstanceRecord;
  /** Asks read on a schema, as a read of its instances does; throws forbidden on a refusal. */
  allowRead(chain: Chain, schema: string): void;
  /** The instance of an event as the log had it just before the event; asks read on its schema. */
  before(chain: Chain, from: string, event: EngineEvent): FrozenJSON | undefined;
  /** The config of a behavior a schema's live version composes, as the schema holds it; asks read unless the schema is own. */
  config(chain: Chain, own: string, schema: string, behavior: string): unknown;
  /** Whether the principal may read a schema. */
  readable(chain: Chain, schema: string): boolean;
  /** Records a reference; asks read on the target's schema and refuses a target that does not exist. */
  addReference(chain: Chain, source: ReferenceSource, target: Reference): void;
  /** Removes a reference; false when there was none. */
  removeReference(chain: Chain, source: ReferenceSource, target: Reference): boolean;
  /** The references a behavior recorded from an instance, in the order recorded. */
  listReferences(chain: Chain, source: ReferenceSource): Reference[];
  /** Asks the guardReference of every behavior that refers to an instance; the first veto throws. */
  guardReferences(chain: Chain, schema: string, id: string, request: GuardRequest): void;
}

/** The instance an execution runs for. */
export interface ExecutionTarget {
  readonly schema: string;
  /** The live version the call runs with. */
  readonly version: number;
  readonly id: string;
}

type ColumnsMode = 'reading' | 'writing' | 'a read-only operation';

export class Execution {
  private data: FrozenJSON;
  private deleted: Map<string, Record<string, SqlValue>> | undefined;

  constructor(
    private readonly storage: Storage,
    private readonly runtime: Runtime,
    private readonly chain: Chain,
    private readonly reach: Reach,
    private readonly target: ExecutionTarget,
    data: Record<string, unknown>,
    /** False for a read and a read-only operation: nothing may write. */
    private readonly writable: boolean
  ) {
    this.data = freezeCopy(data);
  }

  private get composition(): Composition {
    return this.runtime.composition;
  }

  /** setData replaces the instance's own fields the contexts see, after an update. */
  setData(data: Record<string, unknown>): void {
    this.data = freezeCopy(data);
  }

  /** current is the instance's own fields as the call has left them, deep-frozen. */
  current(): FrozenJSON {
    return this.data;
  }

  /**
   * guard asks every behavior's guard in list order, each with its own
   * entry of the preconditions as the request's precondition, then, for a
   * request that writes, the guardReference of every behavior on another
   * instance that refers to this one; the first veto throws
   * BehaviorVetoError.
   */
  guard(request: GuardRequest, writes = true, preconditions?: Preconditions): void {
    const action = request.kind === 'operation' ? request.operation : request.kind;
    for (const bound of this.composition.behaviors) {
      const guard = bound.behavior.implementation.guard;
      if (!guard) {
        continue;
      }
      const precondition = preconditions?.get(bound.behavior.name);
      const asked = precondition === undefined ? request : (Object.freeze({ ...request, precondition }) as GuardRequest);
      const answer: unknown = guard.call(bound.behavior.implementation, this.view(bound), asked);
      synchronous(bound.behavior.name, 'guard', answer);
      const veto = vetoOf(bound.behavior, 'a guard', answer);
      if (veto !== undefined) {
        throw new BehaviorVetoError(bound.behavior.name, action, this.target.schema, this.target.id, veto);
      }
    }
    if (writes) {
      this.reach.guardReferences(this.chain, this.target.schema, this.target.id, request);
    }
  }

  /**
   * initialize runs every behavior's initialize in list order, for a new
   * instance, each with its own entry of the create's parameters (checked
   * by checkCreateParams), {} when the create gives it none.
   */
  initialize(params: Readonly<Record<string, FrozenJSON>>): void {
    for (const bound of this.composition.behaviors) {
      const initialize = bound.behavior.implementation.initialize;
      if (initialize) {
        const own = Object.prototype.hasOwnProperty.call(params, bound.behavior.name) ? params[bound.behavior.name] : NO_PARAMS;
        declaredVetoes(this.composition, () =>
          synchronous(bound.behavior.name, 'initialize', initialize.call(bound.behavior.implementation, this.context(bound, true), own))
        );
      }
    }
  }

  /**
   * deleting records every behavior's columns before the instance's row
   * goes, for the contexts afterChange gets after a delete.
   */
  deleting(): void {
    const deleted = new Map<string, Record<string, SqlValue>>();
    for (const bound of this.composition.behaviors) {
      deleted.set(bound.behavior.name, this.columns(bound, 'reading').get());
    }
    this.deleted = deleted;
  }

  /** afterChange runs every behavior's afterChange in list order. */
  afterChange(change: InstanceChange): void {
    for (const bound of this.composition.behaviors) {
      const afterChange = bound.behavior.implementation.afterChange;
      if (afterChange) {
        declaredVetoes(this.composition, () =>
          synchronous(bound.behavior.name, 'afterChange', afterChange.call(bound.behavior.implementation, this.context(bound, true), change))
        );
      }
    }
  }

  /**
   * fields reads the behaviors' declared fields, every one or the ones
   * named: a JSON value by field name, none for undefined or null.
   */
  fields(only?: readonly string[]): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const bound of this.composition.behaviors) {
      const wanted = only === undefined ? bound.behavior.fields : bound.behavior.fields.filter((field) => only.includes(field.name));
      if (wanted.length === 0) {
        continue;
      }
      const view = this.view(bound);
      for (const field of wanted) {
        const value: unknown = field.read.call(bound.behavior.implementation.fields, view);
        synchronous(bound.behavior.name, `field ${field.name}`, value);
        if (value === undefined || value === null) {
          continue;
        }
        setMember(out, field.name, json(bound.behavior.name, `field ${field.name}`, value));
      }
    }
    return out;
  }

  /**
   * invoke runs an operation: every guard, with the caller's checked
   * preconditions, then its handler, then its result against
   * resultSchema. params are already checked (checkParams). A call() names
   * its behavior as caller and has no preconditions.
   */
  invoke(operation: OperationSpec, params: FrozenJSON, caller?: string, preconditions?: Preconditions): unknown {
    return this.chain.nest(operation.behavior.name, `operation ${operation.name}`, () => {
      const request = { kind: 'operation', behavior: operation.behavior.name, operation: operation.name, params, writes: operation.writes } as const;
      this.guard(caller === undefined ? request : { ...request, caller }, operation.writes, caller === undefined ? preconditions : undefined);
      const bound = this.composition.bound(operation.behavior.name) as BoundBehavior;
      const result: unknown = declaredVetoes(this.composition, () =>
        (operation.handler as OperationHandler<unknown>).call(
          bound.behavior.implementation.operations,
          this.operationContext(bound, operation.writes && this.writable),
          params
        )
      );
      return checkResult(operation, result);
    });
  }

  /**
   * view is a view of the instance for one of its behaviors: what a guard
   * and a field reader get, and what guardReference gets on the
   * referencing instance.
   */
  view(bound: BoundBehavior): InstanceView<unknown> {
    return this.frozen(this.viewMembers(bound, false));
  }

  /** referenceContext is a view whose instances.invoke also runs writing operations: afterReferenceChange's. */
  referenceContext(bound: BoundBehavior): ReferenceContext<unknown> {
    return this.frozen({ ...this.viewMembers(bound, true), writing: this.chain.writing(this.target.schema, this.target.id) });
  }

  private call(from: BoundBehavior, writable: boolean, behavior: string, name: string, params: unknown): unknown {
    if (this.deleted) {
      throw new BehaviorError(from.behavior.name, 'the instance is deleted; call() runs no operation on it');
    }
    const operation = this.composition.operations.get(name);
    if (!operation || operation.behavior.name !== behavior) {
      throw new EngineError('not_found', `${this.target.schema} has no operation ${name} of behavior ${behavior}`);
    }
    if (operation.scope === 'schema') {
      throw new EngineError('not_found', `${this.target.schema}'s ${name} is a schema-level operation; call() runs an operation of this instance`);
    }
    if (operation.writes && !writable) {
      throw new BehaviorError(from.behavior.name, `a read-only operation cannot call ${behavior}.${name}, which writes`);
    }
    const checked = checkParams(operation, params ?? {});
    if (!writable) {
      return this.invoke(operation, checked, from.behavior.name);
    }
    // The savepoint rolls back an update() the called operation made along
    // with the rest of it, so the fields the contexts see go back too.
    const data = this.data;
    try {
      return this.storage.transaction(() => this.invoke(operation, checked, from.behavior.name));
    } catch (error) {
      this.data = data;
      throw error;
    }
  }

  /**
   * update applies a merge patch to the instance's own fields for an
   * operation of bound: the checks and guards of instances.update, without
   * the access policy, which allowed the operation, and without an event,
   * which the operation appends.
   */
  private update(from: BoundBehavior, writable: boolean, patch: unknown): FrozenJSON {
    if (this.deleted) {
      throw new BehaviorError(from.behavior.name, 'the instance is deleted; update() changes nothing');
    }
    if (!writable) {
      throw new BehaviorError(from.behavior.name, 'a read-only operation cannot update the instance');
    }
    const { patch: copy, merged, issues } = this.merge(from, patch);
    if (issues.length > 0) {
      throw new InstanceValidationError(this.chain.namespace, this.target.schema, this.target.version, issues);
    }
    if (jsonEqual(merged, this.data)) {
      return this.data;
    }
    this.guard({ kind: 'update', patch: deepFreeze(copy), after: freezeCopy(merged), caller: from.behavior.name });
    this.storage.run('UPDATE engine_instances SET data = ? WHERE namespace = ? AND schema = ? AND id = ?', [
      JSON.stringify(merged),
      this.chain.namespace,
      this.target.schema,
      this.target.id,
    ]);
    this.setData(merged);
    return this.data;
  }

  // merge applies a behavior's merge patch to a copy of the instance's own
  // fields and lists what update() would refuse: a behavior's field, then
  // whatever the live version refuses in the result.
  private merge(from: BoundBehavior, patch: unknown): { patch: Record<string, unknown>; merged: Record<string, unknown>; issues: ValidationIssue[] } {
    const copied = jsonCopy(patch);
    if (!('value' in copied) || !isPlainObject(copied.value)) {
      throw new BehaviorError(from.behavior.name, 'update() takes a JSON merge patch of the instance: a JSON object');
    }
    const value = copied.value;
    const issues: ValidationIssue[] = [];
    for (const key of Object.keys(value)) {
      const owner = this.composition.fields.get(key);
      if (owner !== undefined) {
        issues.push(readOnlyIssue(key, owner.behavior.name));
      }
    }
    const merged = mergePatch(this.data, value) as Record<string, unknown>;
    return { patch: value, merged, issues: issues.length > 0 ? issues : this.runtime.validator.validate(merged) };
  }

  // frozen freezes a view or a context whose data reads the instance's own
  // fields as they are now, an operation's update() included.
  private frozen<T extends object>(value: T): T & { readonly data: FrozenJSON } {
    Object.defineProperty(value, 'data', { get: () => this.data, enumerable: true });
    return Object.freeze(value) as T & { readonly data: FrozenJSON };
  }

  private viewMembers(bound: BoundBehavior, invokeWrites: boolean) {
    const source: ReferenceSource = { schema: this.target.schema, id: this.target.id, behavior: bound.behavior.name };
    return {
      ...scopeMembers(this.chain, this.reach, bound, this.target.schema, this.target.version, invokeWrites),
      id: this.target.id,
      columns: this.columns(bound, 'reading'),
      sql: behaviorSql(this.storage, this.runtime, this.chain, this.reach, bound, this.target.schema, 'read'),
      references: Object.freeze({ list: () => this.reach.listReferences(this.chain, source) }),
    };
  }

  // context is a writable context when writes is true and the execution
  // may write; otherwise a read-only operation's.
  private context(bound: BoundBehavior, writes: boolean): InstanceContext<unknown> {
    return this.frozen(this.contextMembers(bound, writes && this.writable));
  }

  // operationContext is an operation handler's context: a context with
  // update() and validateUpdate().
  private operationContext(bound: BoundBehavior, writes: boolean): OperationContext<unknown> {
    const writable = writes && this.writable;
    return this.frozen({
      ...this.contextMembers(bound, writable),
      update: (patch: FrozenJSON) => this.update(bound, writable, patch),
      validateUpdate: (patch: FrozenJSON) => this.merge(bound, patch).issues,
    });
  }

  private contextMembers(bound: BoundBehavior, writable: boolean) {
    return {
      ...scopeMembers(this.chain, this.reach, bound, this.target.schema, this.target.version, writable),
      id: this.target.id,
      columns: this.columns(bound, writable ? 'writing' : 'a read-only operation'),
      sql: behaviorSql(this.storage, this.runtime, this.chain, this.reach, bound, this.target.schema, writable ? 'write' : 'read'),
      references: this.references(bound, writable),
      call: (behavior: string, operation: string, params?: FrozenJSON) => this.call(bound, writable, behavior, operation, params),
    };
  }

  // references is a context's handle on its behavior's references from the
  // instance: add and remove refuse in a read-only operation and after a
  // delete, when the engine drops them.
  private references(bound: BoundBehavior, writable: boolean): References {
    const name = bound.behavior.name;
    const source: ReferenceSource = { schema: this.target.schema, id: this.target.id, behavior: name };
    const writing = (what: string): void => {
      if (this.deleted) {
        throw new BehaviorError(name, `the instance is deleted; references.${what} changes nothing, and the engine drops its references`);
      }
      if (!writable) {
        throw new BehaviorError(name, `a read-only operation cannot ${what} a reference`);
      }
    };
    return Object.freeze({
      add: (schema: string, id: string, key = '') => {
        writing('add');
        this.reach.addReference(this.chain, source, reference(name, schema, id, key));
      },
      remove: (schema: string, id: string, key = '') => {
        writing('remove');
        return this.reach.removeReference(this.chain, source, reference(name, schema, id, key));
      },
      list: () => this.reach.listReferences(this.chain, source),
    });
  }

  private columns(bound: BoundBehavior, mode: ColumnsMode): WritableColumns {
    const values = this.deleted?.get(bound.behavior.name);
    if (values !== undefined) {
      return new DeletedColumns(bound.behavior.name, values);
    }
    const readOnly =
      mode === 'writing' ? undefined : mode === 'reading' ? 'a read cannot set columns' : 'a read-only operation cannot set columns';
    return new InstanceColumns(
      this.storage,
      bound.behavior.name,
      this.prefix(bound),
      bound.behavior.columns,
      [this.chain.namespace, this.target.schema, this.target.id],
      readOnly
    );
  }

  private prefix(bound: BoundBehavior): string {
    return prefixOf(this.runtime.prefixes, bound);
  }
}

/**
 * The runner's work on one schema (D16, amended): a reaction to one event
 * or one run of a schedule, for one behavior the live version composes,
 * with no instance and no event of its own. Its context is a schema-level
 * one whose invokes run writing operations; the chain carries the cause
 * their events record.
 */
export class WorkExecution {
  constructor(
    private readonly storage: Storage,
    private readonly runtime: Runtime,
    private readonly chain: Chain,
    private readonly reach: Reach,
    private readonly schema: string,
    private readonly version: number
  ) {}

  /** react hands one event to the behavior's reactions. */
  react(bound: BoundBehavior, reactions: BehaviorReactions<unknown>, event: EngineEvent): void {
    const name = bound.behavior.name;
    const context: ReactionContext<unknown> = Object.freeze({
      ...this.members(bound),
      before: (of: EngineEvent) => this.reach.before(this.chain, name, of),
    });
    const frozen = deepFreeze(JSON.parse(JSON.stringify(event)) as EngineEvent);
    this.chain.nest(name, 'react', () => {
      synchronous(name, 'react', reactions.react.call(reactions, context, frozen));
    });
  }

  /** schedule runs one schedule of the behavior once. */
  schedule(bound: BoundBehavior, schedule: string, spec: BehaviorSchedule<unknown>, previous: number | undefined): void {
    const name = bound.behavior.name;
    const context: ScheduleContext<unknown> = Object.freeze({ ...this.members(bound), schedule, previous });
    this.chain.nest(name, `schedule ${schedule}`, () => {
      synchronous(name, `schedule ${schedule}`, spec.run.call(spec, context));
    });
  }

  private members(bound: BoundBehavior): WorkContext<unknown> {
    return {
      ...scopeMembers(this.chain, this.reach, bound, this.schema, this.version, true),
      sql: behaviorSql(this.storage, this.runtime, this.chain, this.reach, bound, this.schema, 'read'),
    };
  }
}

/**
 * A schema-level operation's run: the operation's behavior on the schema
 * as a whole, with no instance, no instance guard and no event of its own.
 */
export class SchemaExecution {
  constructor(
    private readonly storage: Storage,
    private readonly runtime: Runtime,
    private readonly chain: Chain,
    private readonly reach: Reach,
    private readonly schema: string,
    private readonly version: number
  ) {}

  /** invoke runs the operation's handler, then checks its result. params are already checked. */
  invoke(operation: OperationSpec, params: FrozenJSON): unknown {
    return this.chain.nest(operation.behavior.name, `operation ${operation.name}`, () => {
      const bound = this.runtime.composition.bound(operation.behavior.name) as BoundBehavior;
      const context: SchemaContext<unknown> = Object.freeze({
        ...scopeMembers(this.chain, this.reach, bound, this.schema, this.version, operation.writes),
        sql: behaviorSql(this.storage, this.runtime, this.chain, this.reach, bound, this.schema, 'read'),
      });
      const result: unknown = declaredVetoes(this.runtime.composition, () =>
        (operation.handler as SchemaOperationHandler<unknown>).call(bound.behavior.implementation.schemaOperations, context, params)
      );
      return checkResult(operation, result);
    });
  }
}

// behaviorSql is the SQL a behavior's function gets in a call: its own
// tables, in a mode, and its own columns across the instances of the
// call's schema in the chain's namespace, each statement that names them
// asking read on the schema as the chain's principal.
function behaviorSql(storage: Storage, runtime: Runtime, chain: Chain, reach: Reach, bound: BoundBehavior, schema: string, mode: SqlMode): BehaviorSql {
  const relation: InstanceRelation = {
    namespace: chain.namespace,
    schema,
    columns: bound.behavior.columns,
    allow: () => reach.allowRead(chain, schema),
  };
  return new BehaviorSql(storage, bound.behavior.name, prefixOf(runtime.prefixes, bound), mode, relation);
}

// scopeMembers are what every function of a behavior gets: the behavior,
// its config, the call, can(), and its reach into other instances and
// schemas. invokeWrites lets instances.invoke run writing operations.
function scopeMembers(chain: Chain, reach: Reach, bound: BoundBehavior, schema: string, version: number, invokeWrites: boolean) {
  const name = bound.behavior.name;
  return {
    behavior: name,
    config: bound.config,
    namespace: chain.namespace,
    schema,
    version,
    principal: chain.principal,
    now: chain.now,
    can: (permission: string) => can(chain, name, permission),
    instances: instancesOf(chain, reach, name, invokeWrites),
    schemas: schemasOf(chain, reach, name, schema),
  };
}

function instancesOf(chain: Chain, reach: Reach, behavior: string, invokeWrites: boolean): Instances {
  const read = (schema: string, ids: readonly string[], options: ReadOptions | undefined, what: string): Map<string, InstanceRecord> => {
    checkName(behavior, what, 'schema', schema);
    const fields = fieldsOption(behavior, what, options);
    return chain.nest(behavior, what, () => reach.read(chain, schema, ids, fields));
  };
  return Object.freeze({
    get: (schema: string, id: string, options?: ReadOptions) => {
      checkName(behavior, 'instances.get', 'id', id);
      return read(schema, [id], options, 'instances.get').get(id);
    },
    getMany: (schema: string, ids: readonly string[], options?: ReadOptions) => {
      if (!Array.isArray(ids) || ids.some((id) => typeof id !== 'string')) {
        throw new BehaviorError(behavior, 'instances.getMany takes a list of instance ids');
      }
      const unique = [...new Set(ids)];
      if (unique.length > MAX_BATCH_READ) {
        throw new BehaviorError(behavior, `instances.getMany reads at most ${MAX_BATCH_READ} instances at once, not ${unique.length}`);
      }
      return read(schema, unique, options, 'instances.getMany');
    },
    invoke: (schema: string, id: string, operation: string, params?: FrozenJSON, options?: InstancesInvokeOptions) => {
      checkName(behavior, 'instances.invoke', 'schema', schema);
      checkName(behavior, 'instances.invoke', 'id', id);
      checkName(behavior, 'instances.invoke', 'operation', operation);
      if (options !== undefined && (typeof options !== 'object' || options === null)) {
        throw new BehaviorError(behavior, 'instances.invoke takes options { preconditions? }');
      }
      return reach.invoke(chain, behavior, schema, id, operation, params ?? {}, invokeWrites, options?.preconditions);
    },
    invokeSchema: (schema: string, operation: string, params?: FrozenJSON) => {
      checkName(behavior, 'instances.invokeSchema', 'schema', schema);
      checkName(behavior, 'instances.invokeSchema', 'operation', operation);
      return reach.invokeSchema(chain, behavior, schema, operation, params ?? {}, invokeWrites);
    },
    create: (schema: string, data: FrozenJSON, options?: CreateInstanceOptions) => {
      checkName(behavior, 'instances.create', 'schema', schema);
      if (options !== undefined && (typeof options !== 'object' || options === null)) {
        throw new BehaviorError(behavior, 'instances.create takes options { id?, behaviors? }');
      }
      if (options?.id !== undefined) {
        checkName(behavior, 'instances.create', 'id', options.id);
      }
      const copied = jsonCopy(data);
      if (!('value' in copied)) {
        throw new BehaviorError(behavior, `instances.create: the data is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
      }
      if (!isPlainObject(copied.value)) {
        throw new BehaviorError(behavior, "instances.create takes the instance's own fields: a JSON object");
      }
      const fields = copied.value;
      let params: Record<string, unknown> | undefined;
      if (options?.behaviors !== undefined) {
        const given = jsonCopy(options.behaviors);
        if (!('value' in given)) {
          throw new BehaviorError(behavior, `instances.create: behaviors is not JSON${given.path ? ` at ${given.path}` : ''}: ${given.problem}`);
        }
        if (!isPlainObject(given.value)) {
          throw new BehaviorError(behavior, "instances.create takes behaviors, the create's parameters by behavior name: a JSON object");
        }
        params = given.value;
      }
      return chain.nest(behavior, 'instances.create', () => reach.create(chain, behavior, schema, fields, options?.id, params, invokeWrites));
    },
  });
}

function schemasOf(chain: Chain, reach: Reach, behavior: string, own: string): Schemas {
  return Object.freeze({
    config: (schema: string, name: string) => {
      checkName(behavior, 'schemas.config', 'schema', schema);
      checkName(behavior, 'schemas.config', 'behavior', name);
      return reach.config(chain, own, schema, name) as FrozenJSON | undefined;
    },
    readable: (schema: string) => {
      checkName(behavior, 'schemas.readable', 'schema', schema);
      return reach.readable(chain, schema);
    },
  });
}

// can asks the deployment's matcher whether the principal holds one
// permission; only a literal true is yes.
function can(chain: Chain, behavior: string, permission: string): boolean {
  if (typeof permission !== 'string' || permission === '') {
    throw new BehaviorError(behavior, 'can() takes a permission: a non-empty string');
  }
  const answer: unknown = chain.permissions(chain.principal.permissions, [permission]);
  if (typeof answer === 'object' && answer !== null && typeof (answer as { then?: unknown }).then === 'function') {
    throw new TypeError('a permission matcher is synchronous: it returned a promise');
  }
  return answer === true;
}

function reference(behavior: string, schema: string, id: string, key: string): Reference {
  checkName(behavior, 'references', 'schema', schema);
  checkName(behavior, 'references', 'id', id);
  if (typeof key !== 'string') {
    throw new BehaviorError(behavior, 'a reference key is a string');
  }
  return { schema, id, key };
}

function checkName(behavior: string, what: string, name: string, value: unknown): void {
  if (typeof value !== 'string' || value === '') {
    throw new BehaviorError(behavior, `${what} takes ${name === 'id' ? 'an instance id' : `a ${name} name`}: a non-empty string`);
  }
}

function fieldsOption(behavior: string, what: string, options: ReadOptions | undefined): readonly string[] | undefined {
  const fields = options?.fields;
  if (fields === undefined) {
    return undefined;
  }
  if (!Array.isArray(fields) || fields.some((field) => typeof field !== 'string')) {
    throw new BehaviorError(behavior, `${what}: fields is a list of behavior field names`);
  }
  return fields;
}

/**
 * vetoOf reads a guard's answer: undefined to allow, or a veto, a reason
 * (a non-empty string) or a Veto whose code the behavior's declaration
 * lists and whose details are a JSON object, copied and deep-frozen.
 * Anything else is a BehaviorError.
 */
export function vetoOf(behavior: RegisteredBehavior, what: string, answer: unknown): Veto | undefined {
  if (answer === undefined || answer === null) {
    return undefined;
  }
  if (typeof answer === 'string' && answer !== '') {
    return { reason: answer };
  }
  const shape = `${what} returns a reason (a non-empty string) or { reason, code?, details? } to veto, or undefined to allow`;
  if (!isPlainObject(answer) || Object.keys(answer).some((key) => key !== 'reason' && key !== 'code' && key !== 'details')) {
    throw new BehaviorError(behavior.name, shape);
  }
  const { reason, code, details } = answer;
  if (typeof reason !== 'string' || reason === '') {
    throw new BehaviorError(behavior.name, shape);
  }
  checkVetoCode(behavior, code);
  if (details === undefined) {
    return code === undefined ? { reason } : { reason, code: code as string };
  }
  return { reason, ...(code === undefined ? {} : { code: code as string }), details: vetoDetails(behavior.name, details) };
}

// checkVetoCode holds a veto's code to one its behavior's declaration lists.
function checkVetoCode(behavior: RegisteredBehavior, code: unknown): void {
  if (code !== undefined && (typeof code !== 'string' || !behavior.vetoCodes.has(code))) {
    const listed = [...behavior.vetoCodes];
    throw new BehaviorError(
      behavior.name,
      `a veto's code ${JSON.stringify(code)} is not one its declaration lists (${listed.length > 0 ? listed.join(', ') : 'none'})`
    );
  }
}

// vetoDetails copies a veto's details, which are a JSON object.
function vetoDetails(behavior: string, details: unknown): Readonly<Record<string, unknown>> {
  const copied = jsonCopy(details);
  if (!('value' in copied) || !isPlainObject(copied.value)) {
    throw new BehaviorError(behavior, "a veto's details are a JSON object");
  }
  return deepFreeze(copied.value);
}

/**
 * declaredVetoes runs a behavior's code and holds a BehaviorVetoError it
 * throws to the codes its behavior declares, and its details to a JSON
 * object: the veto of a behavior of the composition whose code its
 * declaration does not list is a BehaviorError. A veto of a behavior of
 * another schema, from an invoke, was held to them where it was thrown.
 */
export function declaredVetoes<T>(composition: Composition, run: () => T): T {
  try {
    return run();
  } catch (error) {
    if (error instanceof BehaviorVetoError && (error.vetoCode !== undefined || error.vetoDetails !== undefined)) {
      const bound = composition.bound(error.behavior);
      if (bound) {
        checkVetoCode(bound.behavior, error.vetoCode);
        if (error.vetoDetails !== undefined) {
          vetoDetails(error.behavior, error.vetoDetails);
        }
      }
    }
    throw error;
  }
}

/**
 * checkPreconditions checks a call's preconditions against the type's
 * behaviors: a JSON object whose every member names a behavior the type
 * composes that declares a preconditionSchema, with an entry its schema
 * accepts. undefined, or no entry, is no preconditions. Every problem is
 * an issue of one PreconditionsError (invalid_argument).
 */
export function checkPreconditions(composition: Composition, schema: string, preconditions: unknown): Preconditions | undefined {
  if (preconditions === undefined) {
    return undefined;
  }
  const copied = jsonCopy(preconditions);
  if (!('value' in copied) || !isPlainObject(copied.value)) {
    throw new PreconditionsError(schema, [{ path: '', message: "the preconditions are a JSON object of each behavior's entry by its name" }]);
  }
  const issues: SchemaIssue[] = [];
  const checked = new Map<string, FrozenJSON>();
  for (const [name, entry] of Object.entries(copied.value)) {
    const at = pointer(name);
    const bound = composition.bound(name);
    if (!bound) {
      issues.push({ path: at, message: `${schema} composes no behavior ${name}` });
      continue;
    }
    const validate = bound.behavior.precondition;
    if (validate === undefined) {
      issues.push({ path: at, message: `behavior ${name} takes no precondition` });
      continue;
    }
    if (!validate(entry)) {
      issues.push(...BehaviorRegistry.issues(validate.errors).map((issue) => ({ path: `${at}${issue.path}`, message: issue.message })));
      continue;
    }
    checked.set(name, deepFreeze(entry as FrozenJSON));
  }
  if (issues.length > 0) {
    throw new PreconditionsError(schema, issues);
  }
  return checked.size > 0 ? checked : undefined;
}

function prefixOf(prefixes: Prefixes, bound: BoundBehavior): string {
  const prefix = prefixes.get(bound.behavior.name);
  if (prefix === undefined) {
    throw new Error(`behavior ${bound.behavior.name} has no storage in this file`);
  }
  return prefix;
}

// checkResult copies an operation's result and holds it to its resultSchema.
function checkResult(operation: OperationSpec, result: unknown): unknown {
  synchronous(operation.behavior.name, `operation ${operation.name}`, result);
  const value = json(operation.behavior.name, `operation ${operation.name} result`, result === undefined ? null : result);
  if (!operation.result(value)) {
    const detail = BehaviorRegistry.issues(operation.result.errors)
      .map((issue) => (issue.path ? `${issue.path} ${issue.message}` : issue.message))
      .join('; ');
    throw new BehaviorError(operation.behavior.name, `operation ${operation.name} returned a result its resultSchema refuses: ${detail}`);
  }
  return value;
}

/** What initialize gets from a create that gives its behavior no parameters. */
const NO_PARAMS: FrozenJSON = Object.freeze({});

/**
 * checkCreateParams copies the parameters a create gives the type's
 * behaviors and checks them: a JSON object by behavior name, each entry
 * for a behavior the type composes that declares a createParamsSchema,
 * which accepts it; a behavior with a createParamsSchema the create gives
 * nothing must accept {}. It returns the entries, deep-frozen: what every
 * guard is asked with and each initialize gets its own of. A refusal is a
 * CreateParamsError with every issue, at JSON pointers under /behaviors.
 */
export function checkCreateParams(composition: Composition, schema: string, behaviors: unknown): Readonly<Record<string, FrozenJSON>> {
  if (behaviors === undefined) {
    behaviors = {};
  }
  if (!isPlainObject(behaviors)) {
    throw new CreateParamsError(schema, [{ path: '/behaviors', message: "behaviors is the create's parameters by behavior name: a JSON object" }]);
  }
  const issues: SchemaIssue[] = [];
  const out: Record<string, FrozenJSON> = {};
  const check = (bound: BoundBehavior, at: string, entry: unknown): void => {
    const validate = bound.behavior.createParams as NonNullable<typeof bound.behavior.createParams>;
    if (!validate(entry)) {
      issues.push(...BehaviorRegistry.issues(validate.errors).map((issue) => ({ path: `${at}${issue.path}`, message: issue.message })));
    }
  };
  for (const [name, entry] of Object.entries(behaviors)) {
    if (entry === undefined) {
      continue;
    }
    const at = `/behaviors${pointer(name)}`;
    const bound = composition.bound(name);
    if (!bound) {
      const taking = composition.behaviors.filter((candidate) => candidate.behavior.createParams !== undefined).map((candidate) => candidate.behavior.name);
      issues.push({
        path: at,
        message: `${schema} does not compose behavior ${name} (its behaviors that take create parameters: ${taking.length > 0 ? taking.join(', ') : 'none'})`,
      });
      continue;
    }
    if (bound.behavior.createParams === undefined) {
      issues.push({ path: at, message: `behavior ${name} takes no create parameters` });
      continue;
    }
    const copied = jsonCopy(entry);
    if (!('value' in copied)) {
      issues.push({ path: `${at}${pathPointer(copied.path)}`, message: copied.problem });
      continue;
    }
    check(bound, at, copied.value);
    out[name] = copied.value as FrozenJSON;
  }
  const given = behaviors;
  for (const bound of composition.behaviors) {
    const name = bound.behavior.name;
    if (bound.behavior.createParams !== undefined && (!Object.prototype.hasOwnProperty.call(given, name) || given[name] === undefined)) {
      check(bound, `/behaviors${pointer(name)}`, {});
    }
  }
  if (issues.length > 0) {
    throw new CreateParamsError(schema, issues);
  }
  return deepFreeze(out);
}

/**
 * checkParams copies an operation's parameters, checks them against its
 * paramsSchema and returns them deep-frozen: the object every guard and the
 * handler get.
 */
export function checkParams(operation: OperationSpec, params: unknown): FrozenJSON {
  const copied = jsonCopy(params);
  if (!('value' in copied)) {
    throw new OperationParamsError(operation.behavior.name, operation.name, [{ path: pathPointer(copied.path), message: copied.problem }]);
  }
  if (!operation.params(copied.value)) {
    throw new OperationParamsError(operation.behavior.name, operation.name, BehaviorRegistry.issues(operation.params.errors));
  }
  return deepFreeze(copied.value as FrozenJSON);
}

function json(behavior: string, what: string, value: unknown): unknown {
  const copied = jsonCopy(value);
  if (!('value' in copied)) {
    throw new BehaviorError(behavior, `${what} is not JSON${copied.path ? ` at ${copied.path}` : ''}: ${copied.problem}`);
  }
  return copied.value;
}

function freezeCopy(data: Record<string, unknown>): FrozenJSON {
  return deepFreeze(JSON.parse(JSON.stringify(data)) as FrozenJSON);
}

// pathPointer turns a path such as `a.b[2]` into a JSON pointer, /a/b/2.
function pathPointer(path: string): string {
  if (path === '') {
    return '';
  }
  return `/${path
    .replace(/\[(\d+)\]/g, '.$1')
    .replace(/^\./, '')
    .split('.')
    .join('/')}`;
}
