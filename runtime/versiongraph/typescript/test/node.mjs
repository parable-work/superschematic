// Under Node: import the built package and let init, and initSync, load its
// bundled wasm file by default (a file: URL, which Node's fetch does not
// read). The vectors run once, under bun (test/vectors.test.ts); this checks
// that the package loads and runs in Node, and that none of its entries, the
// Postgres and SQLite adapters' included, loads the pg driver or a SQLite
// module: a resolve hook refuses them, so an import of one anywhere in the
// package's graph fails the script. Then it runs the SQLite adapter's own
// tests (test/sqlite-cases.ts, which Node loads by stripping its types) and
// the shared SQLite vectors' checks (test/sqlite-vectors-cases.ts) through
// node:sqlite.
import assert from "node:assert/strict";
import { mkdtempSync, rmSync } from "node:fs";
import { registerHooks } from "node:module";
import { tmpdir } from "node:os";
import { join } from "node:path";

const sqliteModules = new Set(["node:sqlite", "bun:sqlite"]);
let sqliteAllowed = false;
registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "pg" || specifier.startsWith("pg/")) {
      throw new Error(`the package imported ${specifier}`);
    }
    if (sqliteModules.has(specifier) && !sqliteAllowed) {
      throw new Error(`the package imported ${specifier}`);
    }
    return nextResolve(specifier, context);
  },
});
await assert.rejects(import("pg"), /the package imported pg/);
await assert.rejects(import("node:sqlite"), /the package imported node:sqlite/);

const { init, initSync, VersionGraphError } = await import("../dist/index.js");
const { Engine, SyncEngine } = await import("../dist/engine.js");
const { PostgresAdapter, pgPool } = await import("../dist/postgres.js");
const { SqliteAdapter, nodeSqlite } = await import("../dist/sqlite.js");
const { VersionGraphFacade } = await import("../dist/facade.js");
assert.equal(typeof SqliteAdapter, "function");
assert.equal(typeof nodeSqlite, "function");
assert.equal(typeof VersionGraphFacade, "function");

const graph = await init();
const tables = {
  version: 3,
  root: { table: "recipe", key: "id" },
  refTable: "recipe_ref",
  commitTable: "recipe_commit",
  patchTable: "recipe_patch",
  releaseTable: "recipe_release",
  snapshotTable: "recipe_snapshot_entry",
};
const descriptor = {
  ...tables,
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
      history: { exclude: [] },
      columns: { entity_key: "uuid", id: "uuid", ref: "uuid", deleted_on_ref: "boolean", _version: "integer", title: "string" },
    },
  ],
};
const tree = { step: [{ entity_key: "a", id: "1", ref: "r", _version: 1, title: "Chop" }] };

assert.deepEqual(graph.validate({ descriptor, tree }), { findings: [] });
assert.match(graph.contentHash({ descriptor, tree }).contentHash, /^[0-9a-f]{64}$/);
assert.throws(
  () => graph.validate({ descriptor: { ...tables, kinds: [] }, tree }),
  (error) => error instanceof VersionGraphError && error.code === "unknown_kind",
);
// The engine and the adapter build over the core without a driver.
const adapter = new PostgresAdapter({ ...descriptor, kinds: [{ ...descriptor.kinds[0], root: "ref", table: "step" }] });
const engine = new Engine(graph, descriptor, adapter.storage(pgPool({ connect: () => Promise.reject(new Error("no database")) })));
await assert.rejects(engine.compose("1"), /no database/);
// initSync reads the bundled file with Node's node:fs, and a SyncEngine
// builds over its core.
const syncGraph = initSync();
assert.deepEqual(syncGraph.validate({ descriptor, tree }), { findings: [] });
const syncEngine = new SyncEngine(syncGraph, descriptor, {
  transact: () => {
    throw new Error("no database");
  },
});
assert.throws(() => syncEngine.compose("1"), /no database/);
console.log("node: @superschematic/versiongraph loads and runs, and no entry loads pg or a SQLite module");

// The SQLite adapter's own tests, through node:sqlite.
sqliteAllowed = true;
const { DatabaseSync } = await import("node:sqlite");
const { cases } = await import("./sqlite-cases.ts");
const { nodeBinding } = await import("./sqlite.ts");
const binding = nodeBinding(DatabaseSync);
for (const c of cases) {
  const dir = mkdtempSync(join(tmpdir(), "vg-sqlite-"));
  try {
    c.run(binding, dir);
  } catch (err) {
    console.error(`node: ${binding.name}: ${c.name}`);
    throw err;
  } finally {
    rmSync(dir, { recursive: true, force: true });
  }
}
console.log(`node: ${cases.length} SQLite adapter cases pass through ${binding.name}`);

// The shared SQLite vectors' checks, through node:sqlite.
const { vectorCases } = await import("./sqlite-vectors-cases.ts");
for (const c of vectorCases) {
  try {
    c.run(binding);
  } catch (err) {
    console.error(`node: ${binding.name}: ${c.name}`);
    throw err;
  }
}
console.log(`node: ${vectorCases.length} SQLite vector checks pass through ${binding.name}`);
