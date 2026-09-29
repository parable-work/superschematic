// Under Node: import the built package and let init load its bundled wasm
// file by default (a file: URL, which Node's fetch does not read). The
// vectors run once, under bun (test/vectors.test.ts); this checks only that
// the package loads and runs in Node.
import assert from "node:assert/strict";
import { init, VersionGraphError } from "../dist/index.js";

const graph = await init();
const tables = {
  version: 2,
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
console.log("node: @superschematic/versiongraph loads and runs");
