// The Postgres adapter's own rules, which the scenarios do not reach: what
// its constructor refuses, a prune that keeps pinned images, a sweep lock
// that one transaction holds at a time, and pgClient's savepoint over a
// transaction the caller holds. The TypeScript counterpart of the Go
// module's postgres_test.go.
import { afterEach, expect, test } from "bun:test";
import type { Ref } from "../dist/engine.js";
import { PostgresAdapter, pgClient, pgPool } from "../dist/postgres.js";
import { descriptor, dsn, scratchSchema, type Scratch } from "./postgres.js";

type Doc = { [key: string]: unknown; kinds: { [key: string]: unknown; columns: Record<string, string> }[] };

const refusals: [string, (d: Doc) => void, string][] = [
  ["the fixture's descriptor", () => {}, ""],
  ["an empty refTable", (d) => (d.refTable = ""), "refTable is empty"],
  ["an empty commitTable", (d) => (d.commitTable = ""), "commitTable is empty"],
  ["an empty patchTable", (d) => (d.patchTable = ""), "patchTable is empty"],
  ["an empty releaseTable", (d) => (d.releaseTable = ""), "releaseTable is empty"],
  ["an empty snapshotTable", (d) => (d.snapshotTable = ""), "snapshotTable is empty"],
  ["a kind without a root column", (d) => delete d.kinds[0]!.root, "has no root"],
  [
    "a role column missing from the kind's columns",
    (d) => delete d.kinds[0]!.columns._version,
    'version column "_version" is not in its columns',
  ],
  ["a descriptor of another version", (d) => (d.version = 1), "reads version 2"],
];

for (const [name, edit, refuse] of refusals) {
  test(`new PostgresAdapter: ${name}`, () => {
    const d = JSON.parse(descriptor) as Doc;
    edit(d);
    const text = JSON.stringify(d);
    if (refuse === "") {
      expect(() => new PostgresAdapter(text)).not.toThrow();
    } else {
      expect(() => new PostgresAdapter(text)).toThrow(refuse);
    }
  });
}

const scratches: Scratch[] = [];
afterEach(async () => {
  for (const scratch of scratches.splice(0)) {
    await scratch.close();
  }
});

async function scratch(): Promise<Scratch> {
  const s = await scratchSchema("vg_adapter_ts");
  scratches.push(s);
  return s;
}

const root = "00000000-0000-0000-0000-000000000001";
const insertRoot = `INSERT INTO recipe (id, title, created_by) VALUES ('${root}', 'Bread', '00000000-0000-0000-0000-000000000002')`;

// A prune past every image's retention deletes the superseded image no
// commit pins and keeps the pinned one, and a kind declared without
// retentionDays has no prune function and prunes nothing.
test.skipIf(dsn === "")("prune keeps pinned images", async () => {
  const { pool } = await scratch();
  const s = new PostgresAdapter(descriptor).storage(pgPool(pool));
  await pool.query(insertRoot);
  const actor = "2";
  await s.transact(async (tx) => {
    const ref = await tx.createRef({ root: "1", parent: null, base: null, name: "main", actor });
    let pinned = "";
    for (const instruction of ["Mix", "Mix well", "Mix gently"]) {
      const row = JSON.stringify({ entity_key: "Mix", position: 1, instruction, timings: {} });
      const stored = await tx.upsertRow("step", { ref: ref.id, root: ref.root, row, tombstone: false, actor });
      if (instruction === "Mix well") {
        pinned = stored;
      }
    }
    await tx.upsertRow("cover", { ref: ref.id, root: ref.root, row: '{"photo_url": "a.jpg"}', tombstone: false, actor });
    const { id, _version } = JSON.parse(pinned) as { id: string; _version: number };
    const commit = await tx.insertCommit({
      root: ref.root,
      ref: ref.id,
      parent: null,
      message: "",
      schemaEpoch: 0,
      contentHash: "h",
      sequence: null,
      actor,
    });
    await tx.insertPatches(commit.id, [
      { commit: "", kind: "step", entityKey: "Mix", entityId: id, entityVersion: _version, operation: "ADD" },
    ]);
  });
  for (const table of ["step_history", "cover_history"]) {
    await pool.query(`UPDATE ${table} SET recorded_at = now() - interval '400 days'`);
  }
  const [steps, covers] = await s.transact(async (tx) => [await tx.prune("step", 365, 0), await tx.prune("cover", 365, 0)]);
  expect([steps, covers]).toEqual([1, 0]);
  const history = await pool.query("SELECT data->>'instruction' AS instruction FROM step_history ORDER BY _version");
  expect(history.rows.map((row) => row.instruction)).toEqual(["Mix well", "Mix gently"]);
});

// While one transaction holds the graph's sweep lock, another does not get
// it and does not wait; once the first ends, the lock is free.
test.skipIf(dsn === "")("the sweep lock is held by one transaction", async () => {
  const { pool } = await scratch();
  const a = new PostgresAdapter(descriptor);
  const holder = a.storage(pgPool(pool));
  const other = a.storage(pgPool(pool));
  const take = () => other.transact((tx) => tx.sweepLock());
  await holder.transact(async (tx) => {
    expect(await tx.sweepLock()).toBe(true);
    expect(await take()).toBe(false);
  });
  expect(await take()).toBe(true);
});

// Bound to a transaction the caller holds with savepoint set, the adapter's
// transaction is a savepoint inside it, so what it writes rolls back with
// the caller's transaction; a failed one rolls back to its savepoint and
// leaves the caller's transaction usable.
test.skipIf(dsn === "")("pgClient runs as a savepoint of the caller's transaction", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  const conn = await pool.connect();
  try {
    await conn.query("BEGIN");
    const s = new PostgresAdapter(descriptor).storage(pgClient(conn, { savepoint: true }));
    const created: Ref = await s.transact((tx) =>
      tx.createRef({ root: "1", parent: null, base: null, name: "main", actor: "2" }),
    );
    expect(created.name).toBe("main");
    await expect(
      s.transact((tx) => tx.createRef({ root: "1", parent: null, base: null, name: "main", actor: "2" })),
    ).rejects.toMatchObject({ code: "name_taken" });
    const inside = await conn.query("SELECT count(*)::int AS n FROM recipe_ref");
    expect(inside.rows[0].n).toBe(1);
    await conn.query("ROLLBACK");
  } finally {
    conn.release();
  }
  const after = await pool.query("SELECT count(*)::int AS n FROM recipe_ref");
  expect(after.rows[0].n).toBe(0);
});

// pgClient's transactions over one connection run one at a time: two
// asked for together do not interleave their statements.
test.skipIf(dsn === "")("pgClient runs its transactions one at a time", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  const conn = await pool.connect();
  try {
    const s = new PostgresAdapter(descriptor).storage(pgClient(conn));
    const order: string[] = [];
    const slow = s.transact(async (tx) => {
      order.push("first begins");
      await tx.createRef({ root: "1", parent: null, base: null, name: "first", actor: "2" });
      await new Promise((resolve) => setTimeout(resolve, 50));
      order.push("first ends");
    });
    const fast = s.transact(async (tx) => {
      order.push("second begins");
      await tx.createRef({ root: "1", parent: null, base: null, name: "second", actor: "2" });
      order.push("second ends");
    });
    await Promise.all([slow, fast]);
    expect(order).toEqual(["first begins", "first ends", "second begins", "second ends"]);
  } finally {
    conn.release();
  }
});
