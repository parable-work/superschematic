/*
The addon's errors as the TypeScript engine throws them (D58). The addon
throws an Error whose name is the TypeScript class, with its code and the
class's own members as details; restoreError rebuilds an instance of that
class, so `instanceof`, `code` and every member read as they do on the
TypeScript engine, the message the Rust engine wrote.
*/

import { BehaviorConfigError } from '../behaviors/behavior.js';
import { deepFreeze } from '../behaviors/json.js';
import {
  BehaviorError,
  BehaviorVetoError,
  CreateParamsError,
  CursorExpiredError,
  EngineError,
  IncompatibleChangeError,
  InstanceValidationError,
  OperationParamsError,
  PreconditionsError,
  SchemaDocumentError,
  UniqueConflictError,
  ValueTooLargeError,
  type EngineErrorCode,
} from '../errors.js';
import { SqliteError } from '../storage/driver.js';
import { UnknownToolError } from '../tools/catalog.js';
import { Bridge } from './bridge.js';

// The EngineError classes by name; each takes its members from details.
const REFUSALS: Readonly<Record<string, { prototype: EngineError }>> = {
  EngineError,
  CursorExpiredError,
  ValueTooLargeError,
  SchemaDocumentError,
  IncompatibleChangeError,
  UniqueConflictError,
  InstanceValidationError,
  BehaviorVetoError,
  PreconditionsError,
  OperationParamsError,
  CreateParamsError,
  UnknownToolError,
};

/**
 * What the Rust engine does not do through this facade
 * (runtime/engine/typescript/test/rust-pending.txt): `bridge`, a behavior
 * written in TypeScript on an addon built without the test bridge;
 * `storage`, raw access to the engine's SQLite file; `ids`, an id source
 * written in TypeScript; `validator`, the TypeScript SchemaValidator;
 * `driver`, a value driver written in TypeScript, which the Rust engine
 * takes as config (D58).
 */
export type NativeGap = 'bridge' | 'storage' | 'ids' | 'validator' | 'driver';

/** A use of the TypeScript engine's API the Rust engine's facade does not have yet. */
export class NativeUnsupportedError extends Error {
  readonly gap: NativeGap;

  constructor(what: string, gap: NativeGap) {
    super(`${what} is not on the Rust engine yet (${gap})`);
    this.name = 'NativeUnsupportedError';
    this.gap = gap;
  }
}

interface Thrown {
  name?: unknown;
  code?: unknown;
  message?: unknown;
  details?: unknown;
  rustToken?: unknown;
}

/** What a behavior written in TypeScript threw, by the token the bridge parked it under. */
export type ParkedValues = (token: number) => { value: unknown } | undefined;

/**
 * restoreError is the TypeScript engine's error for one the addon threw;
 * anything else is itself. A HostError is what a behavior written in
 * TypeScript threw, the very value, which parked holds (src/native/bridge.ts).
 * An error a behavior's context method threw keeps the token of the Rust
 * error it stands for, so a behavior that rethrows it hands that back.
 */
export function restoreError(thrown: unknown, parked?: ParkedValues): unknown {
  const restored = restore(thrown, parked);
  const token = (thrown as Thrown | undefined)?.rustToken;
  if (token !== undefined && restored !== thrown) {
    Bridge.markRust(restored, token);
  } else if (token !== undefined) {
    Bridge.markRust(thrown, token);
  }
  return restored;
}

function restore(thrown: unknown, parked: ParkedValues | undefined): unknown {
  if (!(thrown instanceof Error)) {
    return thrown;
  }
  const { name, code, details } = thrown as Thrown;
  if (name === 'HostError') {
    const token = (details as { hostToken?: unknown } | undefined)?.hostToken;
    const value = typeof token === 'number' ? parked?.(token) : undefined;
    if (value !== undefined) {
      return value.value;
    }
    return new Error(thrown.message);
  }
  const text = thrown.message;
  const members = typeof details === 'object' && details !== null ? (details as Record<string, unknown>) : {};
  switch (name) {
    case 'TypeError':
      return new TypeError(text);
    case 'BehaviorConfigError':
      return new BehaviorConfigError(text);
    case 'SqliteError':
      return new SqliteError(text, typeof code === 'number' ? code : Number(code));
    case 'BehaviorError':
      return assign(rebrand(new Error(text), BehaviorError.prototype, 'BehaviorError'), { behavior: members.behavior });
  }
  const refusal = typeof name === 'string' ? REFUSALS[name] : undefined;
  if (refusal !== undefined) {
    const error = rebrand(new EngineError(code as EngineErrorCode, text), refusal.prototype, name as string);
    if (name === 'BehaviorVetoError') {
      // A veto without a code or details holds them as undefined, as the
      // class does; its details are deep-frozen, as the engine gives them.
      return assign(error, {
        behavior: members.behavior,
        action: members.action,
        reason: members.reason,
        vetoCode: members.vetoCode,
        vetoDetails: deepFreeze(members.vetoDetails),
      });
    }
    return assign(error, members);
  }
  const error = new Error(text);
  if (typeof name === 'string' && name !== 'Error') {
    error.name = name;
  }
  if (code !== undefined) {
    assign(error, { code });
  }
  return error;
}

// rebrand makes an error an instance of a class it was not constructed
// as, under that class's name: the members are the addon's, not a
// constructor's, whose message would be its own.
function rebrand<T extends Error>(error: Error, prototype: T, name: string): T {
  Object.setPrototypeOf(error, prototype);
  error.name = name;
  return error as T;
}

function assign<T extends Error>(error: T, members: Record<string, unknown>): T {
  Object.assign(error, members);
  return error;
}
