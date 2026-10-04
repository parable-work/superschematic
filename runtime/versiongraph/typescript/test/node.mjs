// Under Node: import the built package and let init load its bundled wasm
// file by default (a file: URL, which Node's fetch does not read). The
// vectors run once, under bun (test/vectors.test.ts); this checks only that
// the package loads and runs in Node, and that none of its entries, the
// Postgres adapter's included, loads the pg driver: a resolve hook refuses
// it, so an import of pg anywhere in the package's graph fails the script.
import assert from "node:assert/strict";
import { registerHooks } from "node:module";

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "pg" || specifier.startsWith("pg/")) {
      throw new Error(`the package imported ${specifier}`);
    }
    return nextResolve(specifier, context);
  },
});
await assert.rejects(import("pg"), /the package imported pg/);

const { init, VersionGraphError } = await import("../dist/index.js");
const { Engine } = await import("../dist/engine.js");
const { PostgresAdapter, pgPool } = await import("../dist/postgres.js");
const { VersionGraphFacade } = await import("../dist/facade.js");
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
console.log("node: @superschematic/versiongraph loads and runs, and no entry loads pg");
