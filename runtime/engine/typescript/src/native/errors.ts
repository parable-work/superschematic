/*
The addon's errors as the package's classes (D58). The addon throws an
Error whose name is the class (src/errors.ts), with its code and the
class's own members as details; restoreError rebuilds an instance of that
class, so `instanceof`, `code` and every member read as the class
declares them, the message the Rust engine wrote.
*/

import {
  BehaviorConfigError,
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
  SqliteError,
  UniqueConflictError,
  UnknownToolError,
  ValueTooLargeError,
  type EngineErrorCode,
} from '../errors.js';
import { Bridge } from './bridge.js';
import { deepFreeze } from './json.js';

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
 * What the engine does not do (D58): `bridge`, a behavior written in
 * TypeScript on an addon built without the test bridge, or a TypeScript
 * implementation asked for back; `storage`, raw SQL on Postgres, where the
 * engine's storage is no SQLite file; `validator`, the TypeScript
 * engine's SchemaValidator's own methods (normalize, validateType);
 * `driver`, a value driver written in TypeScript, where the engine's are
 * Rust ValueDrivers.
 */
export type UnsupportedGap = 'bridge' | 'storage' | 'validator' | 'driver';

/** A use of the engine's API the Rust engine does not have: what it is, and which gap. */
export class UnsupportedError extends Error {
  readonly gap: UnsupportedGap;

  constructor(what: string, gap: UnsupportedGap) {
    super(`${what} is not supported by the engine (${gap})`);
    this.name = 'UnsupportedError';
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
 * restoreError is the package's error for one the addon threw;
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
