// initSync and SyncEngine (D32), where the record and replay of every
// scenario (test/replay.ts) does not reach: each source initSync takes, an
// error a storage call throws back into an operation and caught where the
// operation catches it, under both drivers, a SyncEngine's plain values and
// errors, its refusal of storage that returns promises, and every
// operation's refusal of an actor or an id before a transaction begins.
// None of it needs a database.
import { expect, spyOn, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  Engine,
  NotFoundError,
  SyncEngine,
  VersionConflictError,
  type Ref,
  type Storage,
  type SyncStorage,
  type SyncTx,
  type Tx,
} from "../dist/engine.js";
import { init, initSync, type TreeInput, type VersionGraph } from "../dist/index.js";
import { descriptor } from "./postgres.js";

const wasmBytes = readFileSync(new URL("../dist/superschematic_versiongraph.wasm", import.meta.url));

// The smallest valid module: the magic number and version, nothing else.
const emptyModule = new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]);

const input: TreeInput = {
  descriptor: {
    version: 2,
    root: { table: "recipe", key: "id" },
    refTable: "recipe_ref",
    commitTable: "recipe_commit",
    patchTable: "recipe_patch",
    releaseTable: "recipe_release",
    snapshotTable: "recipe_snapshot_entry",
    kinds: [
      {
        kind: "step",
        table: "step",
        historyTable: "step_history",
        key: "entity_key",
        id: "id",
        ref: "ref",
        tombstone: "deleted_on_ref",
        version: "_version",
        singleton: true,
        columns: { entity_key: "uuid", id: "uuid", ref: "uuid", _version: "integer", title: "string", deleted_on_ref: "boolean" },
      },
    ],
  },
  tree: {
    step: [
      { entity_key: "a", id: "1", ref: "r", _version: 1, title: "Chop" },
      { entity_key: "b", id: "2", ref: "r", _version: 1, title: "Boil" },
    ],
  },
};

/** The core runs: validate finds the second row of a singleton kind. */
function expectWorks(graph: VersionGraph): void {
  expect(graph.validate(input).findings.map((finding) => finding.code)).toEqual(["singleton"]);
}

test("initSync with no source reads the bundled file", () => {
  expectWorks(initSync());
});

test("initSync takes bytes: a Buffer, an ArrayBuffer, a typed array and a DataView", () => {
  const buffer = wasmBytes.buffer.slice(wasmBytes.byteOffset, wasmBytes.byteOffset + wasmBytes.byteLength);
  expectWorks(initSync(wasmBytes));
  expectWorks(initSync(buffer));
  expectWorks(initSync(new Uint8Array(buffer)));
  expectWorks(initSync(new DataView(buffer)));
});

// A view is compiled as its own bytes, not its whole buffer: here the module
// sits between other bytes of a larger buffer.
test("initSync takes a view at an offset of a larger buffer", () => {
  const larger = new Uint8Array(16 + wasmBytes.byteLength + 16).fill(0xff);
  larger.set(wasmBytes, 16);
  expectWorks(initSync(larger.subarray(16, 16 + wasmBytes.byteLength)));
  expectWorks(initSync(new DataView(larger.buffer, 16, wasmBytes.byteLength)));
});

test("initSync takes a compiled module", () => {
  expectWorks(initSync(new WebAssembly.Module(wasmBytes)));
});

// A module without the core's exports is refused, as bytes and compiled,
// which shows initSync instantiates the source it is given and not the
// bundled file.
test("initSync instantiates the source it is given", () => {
  expect(() => initSync(emptyModule)).toThrow("does not export memory");
  expect(() => initSync(new WebAssembly.Module(emptyModule))).toThrow("does not export memory");
});

test("initSync refuses a source that is neither bytes nor a module", () => {
  const url = new URL("../dist/superschematic_versiongraph.wasm", import.meta.url);
  expect(() => initSync(url as never)).toThrow("initSync takes the wasm module's bytes or a compiled WebAssembly.Module");
  expect(() => initSync(url.href as never)).toThrow(TypeError);
});

// Where the runtime has no node:fs to read the bundled file with, as in a
// browser, initSync with no source throws and names what to pass instead; a
// source it is given needs no file system.
test("initSync with no source throws where it cannot read the bundled file", () => {
  const spy = spyOn(process, "getBuiltinModule").mockImplementation((() => undefined) as never);
  try {
    expect(() => initSync()).toThrow("initSync has no source and cannot read file:");
    expect(() => initSync()).toThrow("pass the module's bytes or a WebAssembly.Module, or call init");
    expectWorks(initSync(wasmBytes));
  } finally {
    spy.mockRestore();
  }
});

test("initSync takes init's options: parse and stringify replace the JSON codec", () => {
  const seen: string[] = [];
  const graph = initSync(wasmBytes, {
    stringify: (value) => {
      const text = JSON.stringify(value);
      seen.push(text);
      return text;
    },
    parse: (text) => ({ parsed: text }),
  });
  expect(graph.contentHash(input) as unknown).toEqual({ parsed: graph.run("content_hash", JSON.stringify(input)) });
  expect(seen).toEqual([JSON.stringify(input)]);
});

test("initSync's core gives init's results", async () => {
  const graph = await init(wasmBytes);
  expect(initSync().contentHash(input)).toEqual(graph.contentHash(input));
});

const actor = "Cook";
const root = "Bread";

/**
 * A synchronous transaction that answers the calls handlers names and logs
 * each call; any other call throws.
 */
function fakeTx(handlers: Partial<SyncTx>, log: string[]): SyncTx {
  return new Proxy({} as SyncTx, {
    get(_, method) {
      if (typeof method !== "string" || method === "then") {
        return undefined;
      }
      return (...args: unknown[]) => {
        log.push(method);
        const handler = (handlers as Record<string, ((...args: unknown[]) => unknown) | undefined>)[method];
        if (handler === undefined) {
          throw new Error(`the test answers no ${method}`);
        }
        return handler(...args);
      };
    },
  });
}

/** SyncStorage over fakeTx, counting its transactions. */
function syncStorage(handlers: Partial<SyncTx>, log: string[], transactions = { count: 0 }): SyncStorage {
  return {
    transact: (fn) => {
      transactions.count++;
      return fn(fakeTx(handlers, log));
    },
  };
}

/** Storage over fakeTx, each call returning a promise of the handler's value. */
function asyncStorage(handlers: Partial<SyncTx>, log: string[], transactions = { count: 0 }): Storage {
  return {
    transact: async (fn) => {
      transactions.count++;
      const tx = fakeTx(handlers, log) as unknown as Record<string, (...args: unknown[]) => unknown>;
      return fn(
        new Proxy({} as Tx, {
          get: (_, method) =>
            typeof method !== "string" || method === "then" ? undefined : async (...args: unknown[]) => tx[method]!(...args),
        }),
      );
    },
  };
}

function ref(id: string, version: number): Ref {
  return { id, root: "Bread", parent: "Main", base: null, head: null, name: id, sealed: false, discarded: false, version };
}

const kinds = (JSON.parse(descriptor) as { kinds: { kind: string }[] }).kinds.length;

/**
 * A sweep that finds two idle change sets. Discarding the first throws
 * VersionConflictError, as when a write moved it after idleDrafts read it,
 * and the sweep catches that and goes on to the second, then to the rest of
 * the pass.
 */
function sweepOfAMovedRef(): { handlers: Partial<SyncTx>; discarded: string[] } {
  const discarded: string[] = [];
  return {
    discarded,
    handlers: {
      sweepLock: () => true,
      idleDrafts: () => [ref("Moved", 3), ref("Idle", 2)],
      discardRef: (id) => {
        if (id === "Moved") {
          throw new VersionConflictError();
        }
        discarded.push(id);
      },
      discardedRefs: () => [],
      prune: () => 0,
      commits: () => [],
    },
  };
}

const sweepCalls = ["sweepLock", "idleDrafts", "discardRef", "discardRef", "discardedRefs", ...Array(kinds).fill("prune"), "commits"];
const sweptReport = { skipped: false, abandoned: 1, collectedRefs: 0, collectedRows: {}, pruned: {}, snapshots: 0 };

test("SyncEngine throws a call's error into the sweep, which catches a moved ref's VersionConflictError", () => {
  const core = initSync();
  const { handlers, discarded } = sweepOfAMovedRef();
  const log: string[] = [];
  const engine = new SyncEngine(core, descriptor, syncStorage(handlers, log));
  expect(engine.sweep({ actor, abandonAfter: 1000 })).toEqual(sweptReport);
  expect(discarded).toEqual(["Idle"]);
  expect(log).toEqual(sweepCalls);
});

test("Engine throws a call's rejection into the sweep, which catches a moved ref's VersionConflictError", async () => {
  const core = await init();
  const { handlers, discarded } = sweepOfAMovedRef();
  const log: string[] = [];
  const engine = new Engine(core, descriptor, asyncStorage(handlers, log));
  expect(await engine.sweep({ actor, abandonAfter: 1000 })).toEqual(sweptReport);
  expect(discarded).toEqual(["Idle"]);
  expect(log).toEqual(sweepCalls);
});

// An error the operation does not catch leaves it at the call that threw,
// and comes out of the engine unchanged.
test("an error the sweep does not catch ends it, under both drivers", async () => {
  const gone = new NotFoundError("gone");
  const handlers: Partial<SyncTx> = {
    sweepLock: () => true,
    idleDrafts: () => [ref("Gone", 3), ref("Idle", 2)],
    discardRef: () => {
      throw gone;
    },
  };
  const syncLog: string[] = [];
  const sync = new SyncEngine(initSync(), descriptor, syncStorage(handlers, syncLog));
  expect(() => sync.sweep({ actor, abandonAfter: 1000 })).toThrow(gone);
  const asyncLog: string[] = [];
  const engine = new Engine(await init(), descriptor, asyncStorage(handlers, asyncLog));
  await expect(engine.sweep({ actor, abandonAfter: 1000 })).rejects.toBe(gone);
  expect(syncLog).toEqual(["sweepLock", "idleDrafts", "discardRef"]);
  expect(asyncLog).toEqual(syncLog);
});

test("SyncEngine returns plain values", () => {
  const created = ref("Main", 1);
  const log: string[] = [];
  const engine = new SyncEngine(
    initSync(),
    descriptor,
    syncStorage(
      {
        createRef: () => created,
        lockRef: () => created,
        discardRef: () => undefined,
        sweepLock: () => false,
      },
      log,
    ),
  );
  const main = engine.createPrimary(actor, root, "main");
  expect(main).not.toBeInstanceOf(Promise);
  expect(main).toBe(created);
  const discarded: unknown = engine.discard(actor, main.id, main.version);
  expect(discarded).toBeUndefined();
  const report = engine.sweep({ actor });
  expect(report).not.toBeInstanceOf(Promise);
  expect(report.skipped).toBe(true);
  expect(log).toEqual(["createRef", "lockRef", "discardRef", "sweepLock"]);
});

// Each operation refuses an empty actor or an id that is not a UUID before
// a transaction begins: an Engine with a rejected promise, never a throw,
// and a SyncEngine with a throw.
const refusals: Record<string, { args: unknown[]; error: string }> = {
  createPrimary: { args: ["", root, "main"], error: "NoActorError" },
  branch: { args: ["", "Main", "draft"], error: "NoActorError" },
  save: { args: ["", "Draft", 1, {}], error: "NoActorError" },
  commit: { args: ["", "Draft", 1], error: "NoActorError" },
  seal: { args: ["", "Draft", 1], error: "NoActorError" },
  merge: { args: ["", "Draft", "Main", 1], error: "NoActorError" },
  rebase: { args: ["", "Draft", 1], error: "NoActorError" },
  revert: { args: ["", "Draft", 1, "Tagged"], error: "NoActorError" },
  release: { args: ["", root, "Tagged", 0], error: "NoActorError" },
  released: { args: ["not a uuid!"], error: "CanonicalError" },
  materialize: { args: ["not a uuid!"], error: "CanonicalError" },
  compose: { args: ["not a uuid!"], error: "CanonicalError" },
  diff: { args: ["Tagged", "not a uuid!"], error: "CanonicalError" },
  history: { args: ["not a uuid!"], error: "CanonicalError" },
  discard: { args: ["", "Draft", 1], error: "NoActorError" },
  sweep: { args: [{ actor: "" }], error: "NoActorError" },
};

type Operations = Record<string, (...args: unknown[]) => unknown>;

test("a refused actor or id opens no transaction: Engine rejects and SyncEngine throws, for every operation", async () => {
  const operations = Object.getOwnPropertyNames(Engine.prototype).filter(
    (name) => !["constructor", "withStorage", "withWalkCeiling", "runSweeper"].includes(name),
  );
  expect(Object.keys(refusals).sort()).toEqual(operations.sort());
  const syncTransactions = { count: 0 };
  const sync = new SyncEngine(initSync(), descriptor, syncStorage({}, [], syncTransactions)) as unknown as Operations;
  const asyncTransactions = { count: 0 };
  const engine = new Engine(await init(), descriptor, asyncStorage({}, [], asyncTransactions)) as unknown as Operations;
  const outcomes: Record<string, string> = {};
  for (const [name, { args }] of Object.entries(refusals)) {
    let pending: unknown;
    try {
      pending = engine[name]!(...args);
    } catch (err) {
      outcomes[name] = `Engine threw ${(err as Error).name}`;
      continue;
    }
    if (!(pending instanceof Promise)) {
      outcomes[name] = "Engine returned no promise";
      continue;
    }
    const rejected = await pending.then(
      () => "nothing",
      (err: Error) => err.name,
    );
    let thrown = "nothing";
    try {
      sync[name]!(...args);
    } catch (err) {
      thrown = (err as Error).name;
    }
    outcomes[name] = `Engine rejects with ${rejected}, SyncEngine throws ${thrown}`;
  }
  expect(outcomes).toEqual(
    Object.fromEntries(
      Object.entries(refusals).map(([name, { error }]) => [name, `Engine rejects with ${error}, SyncEngine throws ${error}`]),
    ),
  );
  expect([syncTransactions.count, asyncTransactions.count]).toEqual([0, 0]);
});

// A SyncTx must return its values, not promises of them. Its methods with no
// value return undefined, so tsc refuses an async one (each @ts-expect-error
// below fails the type check if it ever accepts one), and a SyncEngine
// refuses a promise from any method, or from transact, with a TypeError that
// names it, ending the operation there.
test("SyncEngine refuses a SyncTx method that returns a promise", () => {
  const log: string[] = [];
  const engine = new SyncEngine(
    initSync(),
    descriptor,
    syncStorage(
      {
        lockRef: () => ref("Draft", 1),
        // @ts-expect-error An async function does not type-check as a SyncTx method with no value.
        discardRef: async () => undefined,
      },
      log,
    ),
  );
  expect(() => engine.discard(actor, "Draft", 1)).toThrow(
    new TypeError("engine: SyncTx.discardRef returned a promise; a SyncEngine needs synchronous storage"),
  );
  expect(log).toEqual(["lockRef", "discardRef"]);
});

test("SyncEngine refuses a promise from a SyncTx method with a value, so a sweep does not take a promise for its lock", () => {
  const log: string[] = [];
  const engine = new SyncEngine(
    initSync(),
    descriptor,
    syncStorage(
      {
        // @ts-expect-error An async function does not type-check as a SyncTx method that returns a boolean.
        sweepLock: async () => true,
        idleDrafts: () => [],
        discardedRefs: () => [],
        prune: () => 0,
        commits: () => [],
      },
      log,
    ),
  );
  expect(() => engine.sweep({ actor, abandonAfter: 1000 })).toThrow(
    new TypeError("engine: SyncTx.sweepLock returned a promise; a SyncEngine needs synchronous storage"),
  );
  expect(log).toEqual(["sweepLock"]);
});

// Any thenable is a promise to the guard, not only a Promise: an object or a
// function with a callable then.
test("SyncEngine refuses any thenable a SyncTx method returns", () => {
  const thenableObject = { then: () => undefined };
  const thenableFunction = Object.assign(() => undefined, { then: () => undefined });
  for (const thenable of [thenableObject, thenableFunction]) {
    const log: string[] = [];
    const engine = new SyncEngine(initSync(), descriptor, syncStorage({ readRef: () => thenable as unknown as Ref }, log));
    expect(() => engine.compose("Draft")).toThrow(
      new TypeError("engine: SyncTx.readRef returned a promise; a SyncEngine needs synchronous storage"),
    );
    expect(log).toEqual(["readRef"]);
  }
});

test("SyncEngine refuses a SyncStorage whose transact returns a promise", () => {
  const created = ref("Main", 1);
  const log: string[] = [];
  const storage: SyncStorage = {
    // @ts-expect-error An async transact does not type-check as SyncStorage's.
    transact: async (fn) => fn(fakeTx({ createRef: () => created }, log)),
  };
  const engine = new SyncEngine(initSync(), descriptor, storage);
  expect(() => engine.createPrimary(actor, root, "main")).toThrow(
    new TypeError("engine: SyncStorage.transact returned a promise; a SyncEngine needs synchronous storage"),
  );
  expect(log).toEqual(["createRef"]);
});

// A storage whose transact awaits before calling its function would otherwise
// run the operation after the SyncEngine has refused it.
test("SyncEngine refuses a SyncStorage that calls its function after transact returned, so the write does not land", async () => {
  const created = ref("Main", 1);
  const log: string[] = [];
  let late: Promise<unknown> | undefined;
  const storage = {
    transact: (fn: (tx: SyncTx) => unknown) => {
      late = (async () => {
        await Promise.resolve();
        return fn(fakeTx({ createRef: () => created }, log));
      })();
      return late;
    },
  } as unknown as SyncStorage;
  const engine = new SyncEngine(initSync(), descriptor, storage);
  expect(() => engine.createPrimary(actor, root, "main")).toThrow(
    new TypeError("engine: SyncStorage.transact returned a promise; a SyncEngine needs synchronous storage"),
  );
  await expect(late).rejects.toThrow(
    new TypeError(
      "engine: SyncStorage.transact called its function after it returned; a SyncEngine needs synchronous storage",
    ),
  );
  expect(log).toEqual([]);
});

// Each SyncTx method with no value returns undefined, so tsc refuses an async
// function for any of them; each @ts-expect-error fails the type check if it
// ever accepts one.
test("SyncTx's methods with no value refuse an async function", () => {
  const handlers: Partial<SyncTx> = {
    lockRef: () => ref("Draft", 1),
    // @ts-expect-error An async function does not type-check as SyncTx.discardRef.
    discardRef: async () => undefined,
    // @ts-expect-error An async function does not type-check as SyncTx.insertPatches.
    insertPatches: async () => undefined,
    // @ts-expect-error An async function does not type-check as SyncTx.insertSnapshot.
    insertSnapshot: async () => undefined,
  };
  const engine = new SyncEngine(initSync(), descriptor, syncStorage(handlers, []));
  expect(() => engine.discard(actor, "Draft", 1)).toThrow(
    new TypeError("engine: SyncTx.discardRef returned a promise; a SyncEngine needs synchronous storage"),
  );
});

test("SyncEngine's copies keep its storage and options", () => {
  const log: string[] = [];
  const walked: number[] = [];
  const engine = new SyncEngine(initSync(), descriptor, syncStorage({}, []), { walkCeiling: 7 });
  const walk = (_commit: string, limit: number) => {
    walked.push(limit);
    return [];
  };
  const copy = engine.withStorage(syncStorage({ walk }, log));
  expect(() => copy.materialize("Tagged")).toThrow(NotFoundError);
  expect(() => copy.withWalkCeiling(3).materialize("Tagged")).toThrow(NotFoundError);
  expect(() => copy.withWalkCeiling(0).materialize("Tagged")).toThrow(NotFoundError);
  expect(walked).toEqual([7, 3, 4096]);
  expect(log).toEqual(["walk", "walk", "walk"]);
});
