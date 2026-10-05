// Record and replay: the proof that SyncEngine runs the operations Engine
// runs (D32). replayed builds an Engine over storage that records each
// operation's transactions: every storage call in each, its method and
// arguments, and its outcome, the value it returned or the error it threw.
// After each operation it runs the same operation, with the same arguments,
// through a SyncEngine over storage that replays the recording. Each call the
// SyncEngine makes must be the recorded one, method and arguments, in the
// recorded order, and returns the recorded value or throws the recorded
// error; no recorded call or transaction may be left over. The SyncEngine's
// result, or its error, must equal the Engine's. Any difference throws, so
// the step that ran the operation fails with it, whatever error the step
// expects.
import { isDeepStrictEqual } from "node:util";
import {
  Engine,
  errorCode,
  SyncEngine,
  type EngineOptions,
  type Storage,
  type SyncStorage,
  type SyncTx,
  type Tx,
} from "../dist/engine.js";
import { initSync, type VersionGraph } from "../dist/index.js";

/** A call's or an operation's outcome: the value it returned, or the error it threw. */
type Outcome = { value: unknown } | { error: unknown };

interface RecordedCall {
  method: string;
  args: unknown[];
  outcome: Outcome;
}

interface RecordedTransaction {
  calls: RecordedCall[];
  /** True when the operation's own function threw, rolling the transaction back. */
  threw: boolean;
  /** The error the transaction failed with, from the operation or from the storage. */
  failed?: { error: unknown };
}

/** A difference between the two engines. */
export class ReplayMismatch extends Error {
  constructor(message: string) {
    super(message);
    this.name = "ReplayMismatch";
  }
}

/** The SyncEngine replays run on a core initSync instantiated, which init's must match. */
const syncCore = initSync();

/**
 * Engine's operations: every method but its copies and runSweeper, which
 * runs sweep. A SyncEngine that lacks one fails its replay.
 */
const operations = new Set(
  Object.getOwnPropertyNames(Engine.prototype).filter(
    (name) => !["constructor", "withStorage", "withWalkCeiling", "runSweeper"].includes(name),
  ),
);

/**
 * new Engine(core, descriptor, storage, options), with each operation
 * replayed through a SyncEngine of the same descriptor and options.
 */
export function replayed(core: VersionGraph, descriptor: string, storage: Storage, options: EngineOptions): Engine {
  const recorder = new Recorder(storage);
  return replaying(
    new Engine(core, descriptor, recorder, options),
    new SyncEngine(syncCore, descriptor, new Replay([]), options),
    recorder,
  );
}

function replaying(engine: Engine, sync: SyncEngine, recorder: Recorder): Engine {
  return new Proxy(engine, {
    get(target, property) {
      if (property === "withWalkCeiling") {
        return (n: number) => replaying(target.withWalkCeiling(n), sync.withWalkCeiling(n), recorder);
      }
      const value = Reflect.get(target, property) as unknown;
      if (typeof value !== "function") {
        return value;
      }
      const method = value as (...args: unknown[]) => unknown;
      if (typeof property !== "string" || !operations.has(property)) {
        // The engine's private fields need the engine as this.
        return method.bind(target);
      }
      return async (...args: unknown[]) => {
        const given = structuredClone(args);
        recorder.start();
        let want: Outcome;
        try {
          want = { value: await method.apply(target, args) };
        } catch (error) {
          want = { error };
        }
        const replay = new Replay(recorder.stop());
        const replayer = sync.withStorage(replay) as unknown as Record<string, (...args: unknown[]) => unknown>;
        let got: Outcome;
        try {
          got = { value: replayer[property]!(...given) };
        } catch (error) {
          got = { error };
        }
        replay.finish(property);
        compare(property, want, got);
        if ("error" in want) {
          throw want.error;
        }
        return want.value;
      };
    },
  });
}

/** Records each transaction of an operation, from start to stop. */
class Recorder implements Storage {
  readonly #inner: Storage;
  #transactions: RecordedTransaction[] | undefined;

  constructor(inner: Storage) {
    this.#inner = inner;
  }

  start(): void {
    this.#transactions = [];
  }

  stop(): RecordedTransaction[] {
    const transactions = this.#transactions ?? [];
    this.#transactions = undefined;
    return transactions;
  }

  async transact<T>(fn: (tx: Tx) => Promise<T>): Promise<T> {
    const recorded: RecordedTransaction = { calls: [], threw: false };
    this.#transactions?.push(recorded);
    try {
      return await this.#inner.transact(async (tx) => {
        try {
          return await fn(recording(tx, recorded.calls));
        } catch (err) {
          recorded.threw = true;
          throw err;
        }
      });
    } catch (error) {
      recorded.failed = { error };
      throw error;
    }
  }
}

/** tx, recording each call and its outcome. A value is copied as it returns, before the engine can change it. */
function recording(tx: Tx, calls: RecordedCall[]): Tx {
  return new Proxy(tx, {
    get(target, property) {
      const value = Reflect.get(target, property) as unknown;
      if (typeof value !== "function" || typeof property !== "string") {
        return value;
      }
      const method = value as (...args: unknown[]) => Promise<unknown>;
      return async (...args: unknown[]) => {
        const call: RecordedCall = { method: property, args: structuredClone(args), outcome: { value: undefined } };
        calls.push(call);
        try {
          const result = await method.apply(target, args);
          call.outcome = { value: structuredClone(result) };
          return result;
        } catch (error) {
          call.outcome = { error };
          throw error;
        }
      };
    },
  });
}

/** Replays recorded transactions to a SyncEngine, refusing any call but the next recorded one. */
class Replay implements SyncStorage {
  readonly #transactions: RecordedTransaction[];
  #next = 0;
  #problem: ReplayMismatch | undefined;

  constructor(transactions: RecordedTransaction[]) {
    this.#transactions = transactions;
  }

  #mismatch(message: string): ReplayMismatch {
    this.#problem ??= new ReplayMismatch(message);
    return this.#problem;
  }

  transact<T>(fn: (tx: SyncTx) => T): T {
    const at = this.#next++;
    const recorded = this.#transactions[at];
    if (recorded === undefined) {
      throw this.#mismatch(`the SyncEngine opened transaction ${at + 1}, and the Engine opened ${this.#transactions.length}`);
    }
    let made = 0;
    const tx = new Proxy({} as SyncTx, {
      get: (_, method) => {
        return (...args: unknown[]) => {
          const call = recorded.calls[made++];
          const name = `transaction ${at + 1}, call ${made}`;
          if (call === undefined) {
            throw this.#mismatch(
              `${name}: the SyncEngine called ${callText(String(method), args)}, past the Engine's ${recorded.calls.length} calls`,
            );
          }
          if (call.method !== method || !isDeepStrictEqual(call.args, args)) {
            throw this.#mismatch(
              `${name}: the SyncEngine called ${callText(String(method), args)}, the Engine ${callText(call.method, call.args)}`,
            );
          }
          if ("error" in call.outcome) {
            throw call.outcome.error;
          }
          return call.outcome.value;
        };
      },
    });
    const left = () => {
      if (made !== recorded.calls.length) {
        throw this.#mismatch(
          `transaction ${at + 1}: the SyncEngine made ${made} calls, and the Engine ${recorded.calls.length}`,
        );
      }
    };
    let value: T;
    try {
      value = fn(tx);
    } catch (err) {
      left();
      throw err;
    }
    left();
    if (recorded.threw) {
      throw this.#mismatch(`transaction ${at + 1}: the SyncEngine's operation returned, and the Engine's threw`);
    }
    if (recorded.failed !== undefined) {
      // The storage failed the transaction after the operation returned.
      throw recorded.failed.error;
    }
    return value;
  }

  /** Throws the first difference, or the transactions the SyncEngine left unopened. */
  finish(operation: string): void {
    if (this.#problem !== undefined) {
      throw new ReplayMismatch(`${operation}: ${this.#problem.message}`);
    }
    if (this.#next !== this.#transactions.length) {
      throw new ReplayMismatch(
        `${operation}: the SyncEngine opened ${this.#next} transactions, and the Engine ${this.#transactions.length}`,
      );
    }
  }
}

function compare(operation: string, want: Outcome, got: Outcome): void {
  const fail = (message: string): never => {
    throw new ReplayMismatch(`${operation}: ${message}`);
  };
  if ("value" in got && typeof (got.value as { then?: unknown } | null | undefined)?.then === "function") {
    fail("the SyncEngine returned a promise");
  }
  if ("error" in want) {
    if (!("error" in got)) {
      fail(`the Engine threw ${String(want.error)}, and the SyncEngine returned ${show(got.value)}`);
    } else if (!sameError(want.error, got.error)) {
      fail(`the Engine threw ${String(want.error)}, and the SyncEngine ${String(got.error)}`);
    }
    return;
  }
  if ("error" in got) {
    fail(`the Engine returned ${show(want.value)}, and the SyncEngine threw ${String(got.error)}`);
  } else if (!isDeepStrictEqual(want.value, got.value)) {
    fail(`the Engine returned ${show(want.value)}, and the SyncEngine ${show(got.value)}`);
  }
}

/** The same error: one class, message and code, and the same own members (an InvalidTreeError's findings, say). */
function sameError(a: unknown, b: unknown): boolean {
  if (!(a instanceof Error) || !(b instanceof Error)) {
    return isDeepStrictEqual(a, b);
  }
  return (
    Object.getPrototypeOf(a) === Object.getPrototypeOf(b) &&
    a.message === b.message &&
    errorCode(a) === errorCode(b) &&
    isDeepStrictEqual({ ...a }, { ...b })
  );
}

function show(value: unknown): string {
  return value === undefined ? "undefined" : JSON.stringify(value);
}

function callText(method: string, args: readonly unknown[]): string {
  return `${method}(${args.map(show).join(", ")})`;
}
