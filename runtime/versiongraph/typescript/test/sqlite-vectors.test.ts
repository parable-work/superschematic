// Runs the shared SQLite vectors' checks (test/sqlite-vectors-cases.ts)
// through each binding bun has: bun:sqlite, and node:sqlite, which bun ships
// too. With UPDATE_SQLITE_VECTORS=1 it first rewrites the vectors from the
// script, through bun:sqlite; review the diff.
import { test } from "bun:test";
import { Database as BunDatabase } from "bun:sqlite";
import { DatabaseSync } from "node:sqlite";
import { bunBinding, nodeBinding } from "./sqlite.ts";
import { vectorCases } from "./sqlite-vectors-cases.ts";
import { updating, writeVectors } from "./sqlite-vectors.ts";

const bun = bunBinding(BunDatabase);

if (updating) {
  writeVectors(() => bun.open(":memory:"));
}

for (const binding of [bun, nodeBinding(DatabaseSync)]) {
  for (const c of vectorCases) {
    test(`${binding.name}: ${c.name}`, () => {
      c.run(binding);
    });
  }
}
