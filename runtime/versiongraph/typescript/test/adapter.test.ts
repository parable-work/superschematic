// The Postgres adapter's own rules, which the scenarios do not reach: what
// its constructor refuses, a row with more members than a table has
// columns, a prune that keeps pinned images, a sweep lock
// that one transaction holds at a time, a ref lock another transaction
// waits for, the version fences of updateRef and discardRef, the released
// rows writeReleased rewrites, and pgClient's savepoint over a transaction
// the caller holds. The TypeScript counterpart of the Go module's
// postgres_test.go.
import { afterEach, expect, test } from "bun:test";
import { uuidCanonical, uuidHyphenated, VersionConflictError } from "../dist/engine.js";
import type { Ref, Storage } from "../dist/engine.js";
import { PostgresAdapter, pgClient, pgPool } from "../dist/postgres.js";
import type { Client, Conn } from "../dist/postgres.js";
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
  ["a descriptor of version 2", (d) => (d.version = 2), "reads version 3"],
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

// noStatements is a Client whose transactions fail the test on any
// statement.
const noStatements: Client = {
  transact: (fn) => {
    const conn: Conn = {
      query: (sql) => {
        throw new Error(`ran ${sql}`);
      },
    };
    return fn(conn);
  },
};

// A row with more members than a Postgres table has columns is refused
// before any statement runs. No kind a Postgres table holds declares that
// many columns, so the adapter refuses the row for a column its descriptor
// does not declare; the Go and Python adapters refuse it by count first.
test("upsertRow refuses more columns than a table", async () => {
  const members: Record<string, number> = {};
  for (let i = 0; i <= 1600; i++) {
    members[`c${i}`] = i;
  }
  const s = new PostgresAdapter(descriptor).storage(noStatements);
  const row = JSON.stringify(members);
  await expect(
    s.transact((tx) => tx.upsertRow("step", { ref: "1", root: "1", row, tombstone: false, actor: "2" })),
  ).rejects.toThrow("which its descriptor does not declare");
});

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
const soup = "00000000-0000-0000-0000-000000000003";
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

// While one transaction holds a ref's lock, another transaction's lockRef
// of it waits (here past its lock_timeout, with lock_not_available) while
// readRef does not, and once the first ends the lock is free.
test.skipIf(dsn === "")("lockRef waits for the holder", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  const a = new PostgresAdapter(descriptor);
  const holder = a.storage(pgPool(pool));
  const ref = await holder.transact((tx) => tx.createRef({ root: "1", parent: null, base: null, name: "main", actor: "2" }));
  const conn = await pool.connect();
  try {
    await conn.query("SET lock_timeout = '200ms'");
    const other = a.storage(pgClient(conn));
    const lock = () => other.transact((tx) => tx.lockRef(ref.id));
    await holder.transact(async (tx) => {
      await tx.lockRef(ref.id);
      await expect(lock()).rejects.toMatchObject({ code: "55P03" });
      expect((await other.transact((t) => t.readRef(ref.id))).id).toBe(ref.id);
    });
    expect((await lock()).id).toBe(ref.id);
  } finally {
    conn.release();
  }
});

// updateRef at the ref's version moves it to the next version, and
// updateRef at the version it had before rejects with VersionConflictError
// and changes nothing.
test.skipIf(dsn === "")("updateRef fences its version", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  const s = new PostgresAdapter(descriptor).storage(pgPool(pool));
  await s.transact(async (tx) => {
    const ref = await tx.createRef({ root: "1", parent: null, base: null, name: "main", actor: "2" });
    const update = { id: ref.id, version: ref.version, head: null, base: null, seal: false, actor: "2" };
    const moved = await tx.updateRef(update);
    expect(moved.version).toBe(ref.version + 1);
    await expect(tx.updateRef({ ...update, seal: true })).rejects.toBeInstanceOf(VersionConflictError);
    const now = await tx.readRef(ref.id);
    expect([now.version, now.sealed]).toEqual([moved.version, false]);
  });
});

// discardRef of a ref already discarded, even at its current version,
// rejects with VersionConflictError and keeps who discarded it first.
test.skipIf(dsn === "")("discardRef refuses a discarded ref", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  const s = new PostgresAdapter(descriptor).storage(pgPool(pool));
  await s.transact(async (tx) => {
    const ref = await tx.createRef({ root: "1", parent: null, base: null, name: "main", actor: "2" });
    await tx.discardRef(ref.id, ref.version, "2");
    const discarded = await tx.readRef(ref.id);
    expect(discarded.discarded).toBe(true);
    await expect(tx.discardRef(ref.id, discarded.version, "3")).rejects.toBeInstanceOf(VersionConflictError);
  });
  const by = await pool.query("SELECT deleted_by::text AS by FROM recipe_ref");
  expect(by.rows.map((r) => r.by)).toEqual(["00000000-0000-0000-0000-000000000002"]);
});

// writeReleased rewrites one kind's rows of one root in the kind's released
// table. With replace it clears the root's rows first; it deletes the
// entities it is given, then writes each row by entity key, with the
// release's commit and sequence and the kind's content columns alone, so a
// row it does not name keeps what an earlier release wrote. Another root's
// rows stay, and a kind that keeps no released table is refused before any
// statement runs.
test.skipIf(dsn === "")("writeReleased rewrites a root's released rows of a kind", async () => {
  const { pool } = await scratch();
  await pool.query(insertRoot);
  await pool.query(`INSERT INTO recipe (id, title, created_by) VALUES ('${soup}', 'Soup', '00000000-0000-0000-0000-000000000002')`);
  const s = new PostgresAdapter(descriptor).storage(pgPool(pool));
  const actor = "2";
  // Two tagged commits of a root, on a primary line of it.
  const commitsOf = (root: string) =>
    s.transact(async (tx) => {
      const ref = await tx.createRef({ root, parent: null, base: null, name: "main", actor });
      const ids: string[] = [];
      for (const parent of [null, 0]) {
        const commit = await tx.insertCommit({
          root,
          ref: ref.id,
          parent: parent === null ? null : ids[parent]!,
          message: "",
          schemaEpoch: 0,
          contentHash: "h",
          sequence: await tx.nextSequence(root),
          actor,
        });
        ids.push(commit.id);
      }
      return ids;
    });
  const [first, second] = await commitsOf("1");
  const [stock] = await commitsOf("3");
  // A row as the engine hands it over: the canonical row of a tree, role
  // columns and all.
  const part = (key: string, heading: string, parent: string | null = null): string =>
    JSON.stringify({ _version: 3, deleted_on_ref: false, entity_key: key, heading, id: "Row", parent_key: parent, rank: "V", recipe_id: "1", ref_id: "Ref" });
  const rows = async (): Promise<unknown[][]> => {
    const result = await pool.query(
      "SELECT recipe_id::text AS root, entity_key::text AS key, parent_key::text AS parent, rank, heading, commit_id::text AS commit, sequence::text AS sequence " +
        "FROM part_released ORDER BY recipe_id, heading",
    );
    return result.rows.map((r) => [
      uuidCanonical(r.root),
      uuidCanonical(r.key),
      r.parent === null ? null : uuidCanonical(r.parent),
      r.rank,
      r.heading,
      uuidCanonical(r.commit),
      r.sequence,
    ]);
  };
  const write = (storage: Storage, kind: string, root: string, commit: string, sequence: number, replace: boolean, upsert: string[], del: string[] = []) =>
    storage.transact((tx) => tx.writeReleased(kind, { root, commit, sequence, replace, upsert, delete: del }));

  // A first release replaces nothing and writes the tree; a row that lacks
  // its parent holds none.
  await write(s, "part", "1", first!, 1, true, [part("Mix", "Mix"), part("Bake", "Bake", "Mix"), '{"entity_key": "Rest", "heading": "Rest", "rank": "k"}']);
  await write(s, "part", "3", stock!, 1, true, [part("Mix", "Stock")]);
  const soupRows = [["3", "Mix", null, "V", "Stock", stock, "1"]];
  expect(await rows()).toEqual([
    ["1", "Bake", "Mix", "V", "Bake", first, "1"],
    ["1", "Mix", null, "V", "Mix", first, "1"],
    ["1", "Rest", null, "k", "Rest", first, "1"],
    ...soupRows,
  ]);
  // Each row has an id of its own, which an upsert keeps.
  const ids = async () => (await pool.query("SELECT id::text AS id FROM part_released WHERE heading LIKE 'Mix%' AND recipe_id = $1::uuid", [root])).rows[0].id;
  const mixID = await ids();

  // A later release: an upsert of a row the root has takes the new commit,
  // sequence and columns, one of a new entity is added, a delete removes
  // its row and one of an entity the root lacks does nothing. An entity
  // key given hyphenated is the same entity.
  await write(s, "part", "1", second!, 2, false, [part("Mix", "Mix well"), part(uuidHyphenated("Knead"), "Knead")], ["Bake", "Missing"]);
  const later = [
    ["1", "Knead", null, "V", "Knead", second, "2"],
    ["1", "Mix", null, "V", "Mix well", second, "2"],
    ["1", "Rest", null, "k", "Rest", first, "1"],
    ...soupRows,
  ];
  expect(await rows()).toEqual(later);
  expect(await ids()).toBe(mixID);

  // What it refuses, each leaving every row as it was: a kind that keeps
  // no released table, before any statement; an unknown kind; a commit the
  // graph does not hold, after the replace that came before it; a row that
  // is no object; and a row without a column the table requires.
  const missing = "00000000-0000-0000-0000-00000000000f";
  await expect(write(new PostgresAdapter(descriptor).storage(noStatements), "step", "1", second!, 2, true, [])).rejects.toThrow(
    'postgres: kind "step" keeps no released table',
  );
  await expect(write(s, "crumb", "1", second!, 2, true, [])).rejects.toThrow('postgres: unknown kind "crumb"');
  await expect(write(s, "part", "1", missing, 2, true, [part("Mix", "Mix")])).rejects.toMatchObject({ code: "23503" });
  await expect(write(s, "part", "1", second!, 2, true, ["[]"])).rejects.toThrow("postgres: a released part row is a JSON object");
  await expect(write(s, "part", "1", second!, 2, true, ['{"entity_key": "Mix", "rank": "V"}'])).rejects.toMatchObject({ code: "23502" });
  await expect(write(s, "part", "1", second!, 2, false, ['{"entity_key": "Mix", "rank": 7}'])).rejects.toThrow("postgres: released part column rank");
  expect(await rows()).toEqual(later);

  // A replace with no rows clears the root's rows, and no other root's.
  await write(s, "part", "1", second!, 2, true, []);
  expect(await rows()).toEqual(soupRows);
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
