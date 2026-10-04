// initSync and SyncEngine (D32), where the record and replay of every
// scenario (test/replay.ts) does not reach: each source initSync takes, an
// error a storage call throws back into an operation and caught where the
// operation catches it, under both drivers, and a SyncEngine's plain values
// and errors. None of it needs a database.
import { expect, spyOn, test } from "bun:test";
import { readFileSync } from "node:fs";
import {
  Engine,
  NoActorError,
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

// An argument the operation refuses throws before a transaction begins:
// synchronously from a SyncEngine, as a rejection from an Engine.
test("a refused argument opens no transaction, under both drivers", async () => {
  const syncTransactions = { count: 0 };
  const sync = new SyncEngine(initSync(), descriptor, syncStorage({}, [], syncTransactions));
  expect(() => sync.createPrimary("", root, "main")).toThrow(NoActorError);
  expect(() => sync.commit(actor, "not a uuid!", 1)).toThrow("engine: id");
  const asyncTransactions = { count: 0 };
  const engine = new Engine(await init(), descriptor, asyncStorage({}, [], asyncTransactions));
  const pending = engine.createPrimary("", root, "main");
  expect(pending).toBeInstanceOf(Promise);
  await expect(pending).rejects.toBeInstanceOf(NoActorError);
  expect([syncTransactions.count, asyncTransactions.count]).toEqual([0, 0]);
});

// SyncEngine offers each of Engine's operations, which the replay of every
// scenario runs; Engine's runSweeper loops on a timer and is Engine's alone.
test("SyncEngine has every method of Engine but runSweeper", () => {
  const methods = (prototype: object) =>
    Object.getOwnPropertyNames(prototype)
      .filter((name) => name !== "constructor")
      .sort();
  expect(methods(SyncEngine.prototype)).toEqual(methods(Engine.prototype).filter((name) => name !== "runSweeper"));
  expect("runSweeper" in SyncEngine.prototype).toBe(false);
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
