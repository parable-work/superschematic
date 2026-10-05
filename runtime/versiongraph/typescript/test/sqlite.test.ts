// Runs every case of the SQLite adapter's own tests (test/sqlite-cases.ts)
// through each binding bun has: bun:sqlite, and node:sqlite, which bun ships
// too. test/node.mjs runs them through node:sqlite under Node.
import { test } from "bun:test";
import { Database as BunDatabase } from "bun:sqlite";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { DatabaseSync } from "node:sqlite";
import { cases } from "./sqlite-cases.ts";
import { bunBinding, nodeBinding } from "./sqlite.ts";

for (const binding of [bunBinding(BunDatabase), nodeBinding(DatabaseSync)]) {
  for (const c of cases) {
    test(`${binding.name}: ${c.name}`, () => {
      const dir = mkdtempSync(join(tmpdir(), "vg-sqlite-"));
      try {
        c.run(binding, dir);
      } finally {
        rmSync(dir, { recursive: true, force: true });
      }
    });
  }
}
