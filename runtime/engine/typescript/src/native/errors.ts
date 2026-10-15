/*
The addon's errors as the TypeScript engine throws them (D58). The addon
throws an Error whose name is the TypeScript class, with its code and the
class's own members as details; restoreError rebuilds an instance of that
class, so `instanceof`, `code` and every member read as they do on the
TypeScript engine, the message the Rust engine wrote.
*/

import { BehaviorConfigError } from '../behaviors/behavior.js';
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
 * What the Rust engine does not do yet through this facade, and which
 * milestone brings it (runtime/engine/typescript/test/rust-pending.txt):
 * `bridge`, a behavior, policy, matcher, id source or value driver written
 * in TypeScript; `storage`, raw access to the engine's SQLite file;
 * `http`, engineApp and engineMcp over the engine's own routes; `watch`,
 * events.watch over the notifier.
 */
export type NativeGap = 'bridge' | 'storage' | 'http' | 'watch';

/** A use of the TypeScript engine's API the Rust engine's facade does not have yet. */
export class NativeUnsupportedError extends Error {
  readonly gap: NativeGap;

  constructor(what: string, gap: NativeGap) {
    super(`${what} is not on the Rust engine yet (${gap}; milestone 7b)`);
    this.name = 'NativeUnsupportedError';
    this.gap = gap;
  }
}

interface Thrown {
  name?: unknown;
  code?: unknown;
  message?: unknown;
  details?: unknown;
}

/** restoreError is the TypeScript engine's error for one the addon threw; anything else is itself. */
export function restoreError(thrown: unknown): unknown {
  if (!(thrown instanceof Error)) {
    return thrown;
  }
  const { name, code, details } = thrown as Thrown;
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
      // A veto without a code or details holds them as undefined, as the class does.
      return assign(error, { behavior: members.behavior, action: members.action, reason: members.reason, vetoCode: members.vetoCode, vetoDetails: members.vetoDetails });
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
