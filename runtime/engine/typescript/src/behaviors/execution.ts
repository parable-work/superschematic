/*
Running a type's behaviors for one call on one instance: its guards, its
initializers, an operation and the operations it calls, its after-change
hooks and its field readers. The instance store creates one Execution per
create, read, update, delete or operation, inside the write transaction
for a write. Everything here is synchronous (D16): a function that returns
a promise is a BehaviorError, which rolls the write back.

Each function gets a context for its own behavior (behavior.ts). A guard
and a field reader get a view: read-only columns and SQL. initialize,
afterChange and a writing operation get a writable context and call(); a
read-only operation gets one whose writes refuse and whose call() reaches
only read-only operations. A called operation runs in a savepoint, so a
failure the caller catches leaves nothing of it behind.
*/

import type { Principal } from '../access.js';
import { BehaviorError, BehaviorVetoError, EngineError, OperationParamsError } from '../errors.js';
import { setMember } from '../instances/patch.js';
import type { SqlValue } from '../storage/driver.js';
import type { Storage } from '../storage/storage.js';
import type { FrozenJSON, GuardRequest, InstanceChange, InstanceContext, InstanceView, WritableColumns } from './behavior.js';
import type { BoundBehavior, Composition } from './composition.js';
import { deepFreeze, jsonCopy } from './json.js';
import { BehaviorRegistry, type OperationSpec } from './registry.js';
import { BehaviorSql, DeletedColumns, InstanceColumns, synchronous } from './storage.js';

/** The call an execution runs for. */
export interface CallScope {
  readonly namespace: string;
  readonly schema: string;
  readonly version: number;
  readonly principal: Principal;
  /** The clock's time for the whole call. */
  readonly now: number;
}

/** How deep call() may nest; deeper is a cycle between behaviors. */
export const MAX_CALL_DEPTH = 16;

/** The prefix of each bound behavior's storage, by behavior name. */
export type Prefixes = ReadonlyMap<string, string>;

export class Execution {
  private data: FrozenJSON;
  private depth = 0;
  private deleted: Map<string, Record<string, SqlValue>> | undefined;

  constructor(
    private readonly storage: Storage,
    private readonly composition: Composition,
    private readonly prefixes: Prefixes,
    private readonly scope: CallScope,
    private readonly id: string,
    data: Record<string, unknown>,
    /** False for a read and a read-only operation: nothing may write. */
    private readonly writable: boolean
  ) {
    this.data = freezeCopy(data);
  }

  /** setData replaces the instance's own fields the contexts see, after an update. */
  setData(data: Record<string, unknown>): void {
    this.data = freezeCopy(data);
  }

  /** guard asks every behavior's guard in list order; the first veto throws BehaviorVetoError. */
  guard(request: GuardRequest): void {
    const action = request.kind === 'operation' ? request.operation : request.kind;
    for (const bound of this.composition.behaviors) {
      const guard = bound.behavior.implementation.guard;
      if (!guard) {
        continue;
      }
      const answer: unknown = guard.call(bound.behavior.implementation, this.view(bound), request);
      synchronous(bound.behavior.name, 'guard', answer);
      if (answer === undefined || answer === null) {
        continue;
      }
      if (typeof answer !== 'string' || answer === '') {
        throw new BehaviorError(bound.behavior.name, 'a guard returns a reason (a non-empty string) to veto, or undefined to allow');
      }
      throw new BehaviorVetoError(bound.behavior.name, action, this.scope.schema, this.id, answer);
    }
  }

  /** initialize runs every behavior's initialize in list order, for a new instance. */
  initialize(): void {
    for (const bound of this.composition.behaviors) {
      const initialize = bound.behavior.implementation.initialize;
      if (initialize) {
        synchronous(bound.behavior.name, 'initialize', initialize.call(bound.behavior.implementation, this.context(bound, true)));
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
        synchronous(bound.behavior.name, 'afterChange', afterChange.call(bound.behavior.implementation, this.context(bound, true), change));
      }
    }
  }

  /** fields reads every behavior's declared fields: a JSON value by field name, none for undefined or null. */
  fields(): Record<string, unknown> {
    const out: Record<string, unknown> = {};
    for (const bound of this.composition.behaviors) {
      if (bound.behavior.fields.length === 0) {
        continue;
      }
      const view = this.view(bound);
      for (const field of bound.behavior.fields) {
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
   * invoke runs an operation: every guard, then its handler, then its
   * result against resultSchema. params are already checked (checkParams).
   */
  invoke(operation: OperationSpec, params: FrozenJSON, caller?: string): unknown {
    if (this.depth >= MAX_CALL_DEPTH) {
      throw new BehaviorError(operation.behavior.name, `operation ${operation.name}: calls nest more than ${MAX_CALL_DEPTH} deep`);
    }
    this.depth += 1;
    try {
      this.guard(
        caller === undefined
          ? { kind: 'operation', behavior: operation.behavior.name, operation: operation.name, params }
          : { kind: 'operation', behavior: operation.behavior.name, operation: operation.name, params, caller }
      );
      const bound = this.composition.bound(operation.behavior.name) as BoundBehavior;
      const result: unknown = operation.handler.call(
        bound.behavior.implementation.operations,
        this.context(bound, operation.writes && this.writable),
        params
      );
      synchronous(operation.behavior.name, `operation ${operation.name}`, result);
      const value = json(operation.behavior.name, `operation ${operation.name} result`, result === undefined ? null : result);
      if (!operation.result(value)) {
        const detail = BehaviorRegistry.issues(operation.result.errors)
          .map((issue) => (issue.path ? `${issue.path} ${issue.message}` : issue.message))
          .join('; ');
        throw new BehaviorError(
          operation.behavior.name,
          `operation ${operation.name} returned a result its resultSchema refuses: ${detail}`
        );
      }
      return value;
    } finally {
      this.depth -= 1;
    }
  }

  private call(from: BoundBehavior, writable: boolean, behavior: string, name: string, params: unknown): unknown {
    if (this.deleted) {
      throw new BehaviorError(from.behavior.name, 'the instance is deleted; call() runs no operation on it');
    }
    const operation = this.composition.operations.get(name);
    if (!operation || operation.behavior.name !== behavior) {
      throw new EngineError('not_found', `${this.scope.schema} has no operation ${name} of behavior ${behavior}`);
    }
    if (operation.writes && !writable) {
      throw new BehaviorError(from.behavior.name, `a read-only operation cannot call ${behavior}.${name}, which writes`);
    }
    const checked = checkParams(operation, params ?? {});
    if (!writable) {
      return this.invoke(operation, checked, from.behavior.name);
    }
    return this.storage.transaction(() => this.invoke(operation, checked, from.behavior.name));
  }

  private scopeFor(bound: BoundBehavior) {
    return {
      behavior: bound.behavior.name,
      config: bound.config,
      namespace: this.scope.namespace,
      schema: this.scope.schema,
      version: this.scope.version,
      principal: this.scope.principal,
      now: this.scope.now,
      id: this.id,
      data: this.data,
    };
  }

  private view(bound: BoundBehavior): InstanceView<unknown> {
    return Object.freeze({
      ...this.scopeFor(bound),
      columns: this.columns(bound, 'reading'),
      sql: new BehaviorSql(this.storage, bound.behavior.name, this.prefix(bound), 'read'),
    });
  }

  // context is a writable context when writes is true and the execution
  // may write; otherwise a read-only operation's.
  private context(bound: BoundBehavior, writes: boolean): InstanceContext<unknown> {
    const writable = writes && this.writable;
    return Object.freeze({
      ...this.scopeFor(bound),
      columns: this.columns(bound, writable ? 'writing' : 'a read-only operation'),
      sql: new BehaviorSql(this.storage, bound.behavior.name, this.prefix(bound), writable ? 'write' : 'read'),
      call: (behavior: string, operation: string, params?: FrozenJSON) => this.call(bound, writable, behavior, operation, params),
    });
  }

  private columns(bound: BoundBehavior, mode: 'reading' | 'writing' | 'a read-only operation'): WritableColumns {
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
      [this.scope.namespace, this.scope.schema, this.id],
      readOnly
    );
  }

  private prefix(bound: BoundBehavior): string {
    const prefix = this.prefixes.get(bound.behavior.name);
    if (prefix === undefined) {
      throw new Error(`behavior ${bound.behavior.name} has no storage in this file`);
    }
    return prefix;
  }
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
