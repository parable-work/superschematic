// The SQLite adapter's own rules, which the scenarios do not reach or reach
// only in passing: the version fences of refs and release pointers, a taken
// name and a discarded ref's name free again, the history it writes, STRICT
// tables and foreign keys, two graphs in one file, the name function, the
// clock, its ids, the write lock a second connection waits on, the caller's
// transaction, D16's rules for a behavior's SQL, the canonical vectors as a
// round trip, and the bindings. Each case takes the binding it opens
// databases with and a directory for the files it needs; test/sqlite.test.ts
// runs every case through bun:sqlite and node:sqlite under bun, and
// test/node.mjs through node:sqlite under Node.
import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { sqlRefusal } from "../../../engine/typescript/src/behaviors/sql.ts";
import {
  CanonicalError,
  canonicalRow,
  canonicalValue,
  errorCode,
  NameTakenError,
  NotFoundError,
  parseJson,
  stringifyJson,
  SyncEngine,
  uuidCanonical,
  uuidHyphenated,
  VersionConflictError,
  type JsonObject,
  type Ref,
  type SyncStorage,
  type SyncTx,
} from "../dist/engine.js";
import { initSync } from "../dist/index.js";
import {
  defaultTableName,
  SqliteAdapter,
  SqliteError,
  sqliteLayout,
  sqliteTables,
  type SqliteClient,
  type SqliteOptions,
  type SqliteRow,
} from "../dist/sqlite.js";
import { behaviorPrefix, behaviorTable, checkedClient, inCallerTransaction, type Binding, type Database, type Ran } from "./sqlite.ts";

/** One rule of the adapter, run through a binding with a directory for its files. */
export interface Case {
  name: string;
  run(binding: Binding, dir: string): void;
}

const fixture = new URL("../../testdata/fixture/recipe.json", import.meta.url);

/** The scenarios' graph descriptor, fixture-version-graph-db's Recipe graph, as JSON text. */
const descriptor = readFileSync(fixture, "utf8");

const core = initSync();

/** Each kind's columns' value classes, as the fixture declares them. */
const columns = new Map(
  (JSON.parse(descriptor) as { kinds: { kind: string; columns: Record<string, string> }[] }).kinds.map((k) => [k.kind, k.columns]),
);

/** Asserts a row the adapter returned is a canonical row of its kind: the canonical rules leave it as it is, members sorted. */
function assertCanonicalRow(kind: string, row: string): void {
  assert.equal(canonicalRow(columns.get(kind)!, row), row, `a ${kind} row is canonical: ${row}`);
}

const graph = "recipe";
const cook = "Cook";
const bread = "Bread";

/** A database with the layout, and the adapter's storage over it. */
interface Setup {
  db: Database;
  client: SqliteClient;
  adapter: SqliteAdapter;
  storage: SyncStorage;
  engine: SyncEngine;
}

function setup(binding: Binding, options: Partial<SqliteOptions> = {}, path = ":memory:"): Setup {
  const db = binding.open(path);
  const adapter = new SqliteAdapter(descriptor, { graph, ...options });
  adapter.createTables(db.client);
  const storage = adapter.storage(db.client);
  const engine = new SyncEngine(core, descriptor, storage, { schemaEpoch: 1, snapshotEvery: 3 });
  return { db, client: db.client, adapter, storage, engine };
}

/** Runs fn over each setup, closing their databases however it ends. */
function using(setups: Setup[], fn: () => void): void {
  try {
    fn();
  } finally {
    for (const s of setups) {
      s.db.close();
    }
  }
}

/** A JSON object's member, as its JSON text. */
function member(row: string, name: string): string | undefined {
  const value = (parseJson(row) as JsonObject).get(name);
  return value === undefined ? undefined : stringifyJson(value);
}

/** The error fn throws; it fails when fn does not throw. */
function thrown(fn: () => unknown): unknown {
  try {
    fn();
  } catch (err) {
    return err;
  }
  assert.fail("expected an error");
}

/** A step row of the fixture, as an upsert's JSON text. */
function stepRow(key: string | null, instruction: string, extra: Record<string, unknown> = {}): string {
  return JSON.stringify({ entity_key: key, position: 1, instruction, timings: {}, ...extra });
}

function history(client: SqliteClient, table: string, id: string): SqliteRow[] {
  return client.all(`SELECT _version, operation, data, recorded_at FROM ${table} WHERE id = ?1 ORDER BY _version`, [id]);
}

/** A canonical id: base62 of a version-4 UUID. */
function assertCanonicalID(id: unknown, what: string): void {
  assert.equal(typeof id, "string", `${what} is text`);
  assert.equal(uuidCanonical(id as string), id, `${what} ${String(id)} is in its canonical form`);
  const hex = uuidHyphenated(id as string);
  assert.match(hex, /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/, `${what} ${String(id)} is a version-4 UUID`);
}

/** A ref and a tagged commit on it, written through the adapter, for the cases that need a commit. */
function refAndCommit(tx: SyncTx, root = bread, name = "main"): { ref: Ref; commit: string } {
  const ref = tx.createRef({ root, parent: null, base: null, name, actor: cook });
  const commit = tx.insertCommit({
    root,
    ref: ref.id,
    parent: null,
    message: "",
    schemaEpoch: 1,
    contentHash: "0".repeat(64),
    sequence: tx.nextSequence(root),
    actor: cook,
  }).id;
  return { ref, commit };
}

export const cases: Case[] = [
  {
    name: "a ref's version fences its update and its discard, and a refused discard leaves the transaction usable",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const ref = s.storage.transact((tx) => tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook }));
        assert.equal(ref.version, 1);
        const moved = s.storage.transact((tx) => tx.updateRef({ id: ref.id, version: 1, head: null, base: null, seal: false, actor: cook }));
        assert.equal(moved.version, 2);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.updateRef({ id: ref.id, version: 1, head: null, base: null, seal: true, actor: cook }))) instanceof VersionConflictError);
        assert.equal(s.storage.transact((tx) => tx.readRef(ref.id)).sealed, false);
        const draft = s.storage.transact((tx) => {
          assert.ok(thrown(() => tx.discardRef(ref.id, 1, cook)) instanceof VersionConflictError);
          // The transaction goes on after the refused discard, and commits.
          return tx.createRef({ root: bread, parent: ref.id, base: null, name: "draft", actor: cook });
        });
        assert.equal(s.storage.transact((tx) => tx.readRef(draft.id)).name, "draft");
        s.storage.transact((tx) => tx.discardRef(ref.id, 2, cook));
        const discarded = s.storage.transact((tx) => tx.readRef(ref.id));
        assert.deepEqual([discarded.discarded, discarded.version], [true, 3]);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.discardRef(ref.id, 3, cook))) instanceof VersionConflictError);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.readRef("Missing"))) instanceof NotFoundError);
      });
    },
  },
  {
    name: "a release pointer's version fences its first write and every move",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const { commit } = s.storage.transact((tx) => refAndCommit(tx));
        const first = s.storage.transact((tx) => tx.writeRelease({ root: bread, commit, version: 0, actor: cook }));
        assert.equal(first.version, 1);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.writeRelease({ root: bread, commit, version: 0, actor: cook }))) instanceof VersionConflictError);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.writeRelease({ root: bread, commit, version: 2, actor: cook }))) instanceof VersionConflictError);
        const moved = s.storage.transact((tx) => tx.writeRelease({ root: bread, commit, version: 1, actor: "Baker" }));
        assert.deepEqual([moved.id, moved.version], [first.id, 2]);
        assert.equal(s.storage.transact((tx) => tx.readRelease(bread)).version, 2);
        assert.ok(thrown(() => s.storage.transact((tx) => tx.readRelease("Soup"))) instanceof NotFoundError);
        // The pointer's history is the release log: each write's image at its version.
        const log = history(s.client, '"graph_release_history"', first.id);
        assert.deepEqual(
          log.map((row) => [row["_version"], row["operation"], member(row["data"] as string, "updated_by")]),
          [
            [1, "INSERT", '"Cook"'],
            [2, "UPDATE", '"Baker"'],
          ],
        );
      });
    },
  },
  {
    name: "a root's live ref names are distinct, and a discarded ref's name is free again",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        const taken = thrown(() => s.engine.createPrimary(cook, bread, "main"));
        assert.ok(taken instanceof NameTakenError);
        assert.equal(errorCode(taken), "name_taken");
        const draft = s.engine.branch(cook, main.id, "draft");
        assert.equal(errorCode(thrown(() => s.engine.branch(cook, main.id, "draft"))), "name_taken");
        // Another root takes the name.
        s.engine.createPrimary(cook, "Soup", "main");
        s.engine.discard(cook, draft.id, draft.version);
        assert.equal(s.engine.branch(cook, main.id, "draft").version, 1);
      });
    },
  },
  {
    name: "history: a member's versions and images, a delete's actor, and the columns history leaves out",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        const draft = s.engine.branch(cook, main.id, "draft");
        const first = s.engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow("Mix", "Mix", { scratch: "note to self" })] } });
        const second = s.engine.save("Baker", draft.id, first.ref.version, { step: { upsert: [stepRow("Mix", "Mix well")] } });
        const row = second.saved["step"]![0]!;
        assert.equal(member(row, "_version"), "2");
        // A column the update leaves out keeps its value on the live row.
        assert.equal(member(row, "scratch"), '"note to self"');
        const id = JSON.parse(member(row, "id")!) as string;
        s.engine.save("Janitor", draft.id, second.ref.version, { step: { unset: ["Mix"] } });
        const images = history(s.client, '"graph_member_history"', id);
        assert.deepEqual(
          images.map((image) => [image["_version"], image["operation"]]),
          [
            [1, "INSERT"],
            [2, "UPDATE"],
            [3, "DELETE"],
          ],
        );
        for (const image of images) {
          assertCanonicalRow("step", image["data"] as string);
          assert.equal(member(image["data"] as string, "scratch"), undefined, "an image leaves scratch out");
          assert.equal(member(image["data"] as string, "_version"), String(image["_version"]));
        }
        // An update's image is the row as stored, less scratch.
        const updated = parseJson(row) as JsonObject;
        updated.delete("scratch");
        assert.equal(images[1]!["data"], stringifyJson(updated));
        // The delete's image is the row at its version plus 1, naming the
        // delete's actor in the kind's actor column, updated_by.
        const deleted = parseJson(row) as JsonObject;
        deleted.delete("scratch");
        deleted.set("_version", parseJson("3"));
        deleted.set("updated_by", "Janitor");
        assert.equal(images[2]!["data"], stringifyJson(deleted));
        assert.equal(s.storage.transact((tx) => tx.rows("step", draft.id)).length, 0);
        // A kind with no actor column keeps the row's values in its delete's image.
        const whisk = s.engine.save(cook, draft.id, s.storage.transact((tx) => tx.readRef(draft.id)).version, {
          utensil: { upsert: ['{"entity_key": "Whisk", "name": "whisk"}'] },
        });
        const utensil = whisk.saved["utensil"]![0]!;
        s.engine.save("Janitor", draft.id, whisk.ref.version, { utensil: { unset: ["Whisk"] } });
        const gone = history(s.client, '"graph_member_history"', JSON.parse(member(utensil, "id")!) as string);
        const kept = parseJson(utensil) as JsonObject;
        kept.set("_version", parseJson("2"));
        assert.equal(gone[1]!["data"], stringifyJson(kept));
        // A ref's history: its insert and each update at its version, the discard's naming its actor.
        s.engine.discard("Janitor", draft.id, s.storage.transact((tx) => tx.readRef(draft.id)).version);
        const refImages = history(s.client, '"graph_ref_history"', draft.id);
        assert.deepEqual(
          refImages.map((image) => [image["_version"], image["operation"]]),
          [1, 2, 3, 4, 5, 6, 7].map((version) => [version, version === 1 ? "INSERT" : "UPDATE"]),
        );
        const last = refImages.at(-1)!["data"] as string;
        assert.equal(member(last, "deleted_by"), '"Janitor"');
        assert.equal(member(last, "_version"), "7");
        assert.match(member(last, "deleted_at")!, /^"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z"$/);
      });
    },
  },
  {
    name: "prune deletes a kind's images past its retention but each row's newest and every pinned one, at most a batch, and nothing of a kind without retention",
    run(binding) {
      let now = 1_800_000_000_000_000;
      const s = setup(binding, { clock: () => now });
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        let draft = s.engine.branch(cook, main.id, "draft");
        for (const instruction of ["Knead", "Knead well", "Knead hard"]) {
          draft = s.engine.save(cook, draft.id, draft.version, {
            step: { upsert: [stepRow("Knead", instruction)] },
            utensil: { upsert: [`{"entity_key": "Whisk", "name": ${JSON.stringify(instruction)}}`] },
          }).ref;
        }
        // The commit pins version 3 of each; versions 1 and 2 are unpinned.
        draft = s.engine.commit(cook, draft.id, draft.version).ref;
        draft = s.engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow("Knead", "Knead softly")] } }).ref;
        const prune = (kind: string, days: number, batch: number) => s.storage.transact((tx) => tx.prune(kind, days, batch));
        const versions = (kind: string) =>
          s.client.all(`SELECT _version FROM "graph_member_history" WHERE kind = ?1 ORDER BY _version`, [kind]).map((r) => r["_version"]);
        // Within the step kind's 365 days, nothing goes.
        now += 364 * 86_400_000_000;
        assert.equal(prune("step", 0, 0), 0);
        // An argument other than 0 is the retention, in days.
        assert.equal(prune("step", 400, 0), 0);
        now += 2 * 86_400_000_000;
        assert.equal(prune("step", 400, 0), 0);
        // Past the declared 365 days: versions 1 and 2, a batch at a time.
        assert.equal(prune("step", 0, 1), 1);
        assert.deepEqual(versions("step"), [2, 3, 4]);
        assert.equal(prune("step", 0, 0), 1);
        assert.deepEqual(versions("step"), [3, 4], "the pinned version 3 and the newest, version 4, stay");
        assert.equal(prune("step", 1, 0), 0);
        // utensil declares no retention, so nothing of it goes, whatever the argument.
        assert.equal(prune("utensil", 0, 0), 0);
        assert.equal(prune("utensil", 1, 0), 0);
        assert.deepEqual(versions("utensil"), [1, 2, 3]);
        assert.throws(() => prune("step", 0, -1), /a batch is a whole number/);
      });
    },
  },
  {
    name: "the adapter writes a row's ref, root, tombstone, actor and time, never its id or version, and keeps or defaults what it lacks",
    run(binding) {
      const s = setup(binding, { clock: () => 1_800_000_000_000_000 });
      using([s], () => {
        const { ref } = s.storage.transact((tx) => refAndCommit(tx));
        const write = (row: string, tombstone = false, actor = cook) =>
          s.storage.transact((tx) => tx.upsertRow("step", { ref: ref.id, root: bread, row, tombstone, actor }));
        const inserted = write(
          JSON.stringify({
            entity_key: "Mix",
            id: "Elsewhere",
            _version: 7,
            ref_id: "Other",
            recipe_id: "Soup",
            deleted_on_ref: true,
            created_at: "2000-01-01T00:00:00Z",
            created_by: "Somebody",
            position: 1,
            instruction: "Mix",
            timings: {},
          }),
        );
        assertCanonicalRow("step", inserted);
        assert.notEqual(member(inserted, "id"), '"Elsewhere"');
        assert.equal(member(inserted, "_version"), "1");
        assert.equal(member(inserted, "ref_id"), JSON.stringify(ref.id));
        assert.equal(member(inserted, "recipe_id"), '"Bread"');
        assert.equal(member(inserted, "deleted_on_ref"), "false");
        assert.equal(member(inserted, "created_at"), '"2027-01-15T08:00:00Z"');
        assert.equal(member(inserted, "created_by"), '"Cook"');
        assert.equal(member(inserted, "updated_by"), '"Cook"');
        // A column the insert lacks holds null.
        assert.equal(member(inserted, "scratch"), "null");
        const updated = write(JSON.stringify({ entity_key: "Mix", instruction: "Stir", created_by: "Somebody" }), true, "Baker");
        assertCanonicalRow("step", updated);
        const [read] = s.storage.transact((tx) => tx.rows("step", ref.id));
        assert.equal(read, updated);
        assert.equal(member(updated, "id"), member(inserted, "id"));
        assert.equal(member(updated, "_version"), "2");
        assert.equal(member(updated, "deleted_on_ref"), "true");
        // A column the update lacks keeps its value, and the creation audit stays.
        assert.equal(member(updated, "position"), "1");
        assert.equal(member(updated, "created_by"), '"Cook"');
        assert.equal(member(updated, "updated_by"), '"Baker"');
        // A row without an entity key is a new entity.
        const fresh = write(stepRow(null, "Rest"));
        assert.notEqual(member(fresh, "entity_key"), member(inserted, "entity_key"));
        assertCanonicalID(JSON.parse(member(fresh, "entity_key")!), "a generated entity key");
        // A column the descriptor does not declare is refused.
        assert.match(String(thrown(() => write(JSON.stringify({ entity_key: "Mix", flavour: "salt" })))), /does not declare/);
      });
    },
  },
  {
    name: "after a kind gains a column, its old rows read it as null, as Postgres's ADD COLUMN gives them, and its old images read as stored",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        const draft = s.engine.branch(cook, main.id, "draft");
        const saved = s.engine.save(cook, draft.id, draft.version, {
          step: { upsert: [stepRow("Mix", "Mix")] },
          utensil: { upsert: ['{"entity_key": "Whisk", "name": "whisk"}'] },
        });
        const committed = s.engine.commit(cook, draft.id, saved.ref.version);
        // The schema's next version: utensil gains color, and step gains
        // memo, which its history leaves out.
        type Doc = { kinds: { kind: string; columns: Record<string, string>; excluded: string[]; history: { exclude: string[] } }[] };
        const d = JSON.parse(descriptor) as Doc;
        const kind = (name: string) => d.kinds.find((k) => k.kind === name)!;
        kind("utensil").columns["color"] = "string";
        kind("step").columns["memo"] = "string";
        kind("step").excluded.push("memo");
        kind("step").history.exclude.push("memo");
        const next = JSON.stringify(d);
        const storage = new SqliteAdapter(next, { graph }).storage(s.client);
        const engine = new SyncEngine(core, next, storage, { schemaEpoch: 1, snapshotEvery: 3 });
        const [whisk] = storage.transact((tx) => tx.rows("utensil", draft.id));
        const [mix] = storage.transact((tx) => tx.rows("step", draft.id));
        assert.equal(member(whisk!, "color"), "null");
        assert.equal(member(mix!, "memo"), "null");
        assert.equal(canonicalRow(kind("utensil").columns, whisk!), whisk);
        // An image reads as it was stored, without the gained column, as a
        // Postgres history image does.
        const id = (row: string) => JSON.parse(member(row, "id")!) as string;
        const [whiskImage] = storage.transact((tx) => tx.images("utensil", [{ id: id(whisk!), version: 1 }]));
        const [mixImage] = storage.transact((tx) => tx.images("step", [{ id: id(mix!), version: 1 }]));
        const storedImage = s.client.get(`SELECT data FROM "graph_member_history" WHERE id = ?1 AND _version = 1`, [id(whisk!)]);
        assert.equal(whiskImage, storedImage?.["data"]);
        assert.equal(member(whiskImage!, "color"), undefined);
        assert.equal(canonicalRow(kind("utensil").columns, whiskImage!), whiskImage);
        assert.equal(member(mixImage!, "memo"), undefined);
        assert.equal(member(mixImage!, "scratch"), undefined);
        // The commit's tree lacks color, and the core reads it as null: it
        // hashes as the draft's live rows, which hold it null, and as a tree
        // whose row holds it null.
        const tree = engine.materialize(committed.commit!.id);
        const [read] = tree.tree["utensil"]!;
        assert.equal(member(read!, "color"), undefined);
        assert.equal(tree.contentHash, engine.compose(draft.id).contentHash);
        const hash = (row: Record<string, unknown>) =>
          core.contentHash({ descriptor: d as never, tree: { step: tree.tree["step"]!.map((r) => JSON.parse(r) as Record<string, unknown>), utensil: [row] } }).contentHash;
        assert.equal(tree.contentHash, hash({ ...(JSON.parse(read!) as Record<string, unknown>), color: null }));
        // An update of the old row stores the gained column, null, and its image carries it.
        const updated = engine.save(cook, draft.id, committed.ref.version, { utensil: { upsert: ['{"entity_key": "Whisk", "name": "big whisk"}'] } });
        assert.equal(member(updated.saved["utensil"]![0]!, "color"), "null");
        const stored = s.client.get(`SELECT data FROM "graph_member" WHERE id = ?1`, [id(whisk!)]);
        assert.equal(member(stored?.["data"] as string, "color"), "null");
        const [updatedImage] = storage.transact((tx) => tx.images("utensil", [{ id: id(whisk!), version: 2 }]));
        assert.equal(member(updatedImage!, "color"), "null");
        assert.equal(member(updatedImage!, "name"), '"big whisk"');
      });
    },
  },
  {
    name: "a unique index backs each key the adapter's own reads keep unique",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const { ref, commit } = s.storage.transact((tx) => refAndCommit(tx));
        let n = 0;
        const fresh = () => `Row${n++}`;
        const twice: [string, () => string][] = [
          ["a commit's entity in its patches", () => `INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES ('${fresh()}', 'recipe', '${commit}', 'step', 'Mix', 'B', 1, 'ADD')`],
          ["a commit's entity in its snapshot", () => `INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES ('${fresh()}', 'recipe', '${commit}', 'step', 'Mix', 'B', 1)`],
          ["an entity of a kind on a ref", () => `INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES ('${fresh()}', 'recipe', 'step', 'Mix', '${ref.id}', 'Bread', 0, 1, '{}')`],
          ["a member's image at a version", () => `INSERT INTO "graph_member_history" (history_id, graph, kind, id, _version, operation, data, recorded_at) VALUES ('${fresh()}', 'recipe', 'step', 'Same', 1, 'INSERT', '{}', 0)`],
          ["a ref's image at a version", () => `INSERT INTO "graph_ref_history" (history_id, graph, id, _version, operation, data, recorded_at) VALUES ('${fresh()}', 'recipe', 'Same', 1, 'INSERT', '{}', 0)`],
          ["a pointer's image at a version", () => `INSERT INTO "graph_release_history" (history_id, graph, id, _version, operation, data, recorded_at) VALUES ('${fresh()}', 'recipe', 'Same', 1, 'INSERT', '{}', 0)`],
          ["a root's release pointer", () => `INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES ('${fresh()}', 'recipe', 'Soup', '${commit}', 0, 'Cook', 0, 'Cook', 1)`],
          ["a root's sequence", () => `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, sequence, created_at, created_by) VALUES ('${fresh()}', 'recipe', 'Bread', '${ref.id}', 1, 'h', 7, 0, 'Cook')`],
        ];
        for (const [what, insert] of twice) {
          s.client.run(insert());
          const err = thrown(() => s.client.run(insert()));
          assert.ok(err instanceof SqliteError, `${what}: ${String(err)}`);
          assert.equal(err.code, 2067, what);
        }
      });
    },
  },
  {
    name: "every table is STRICT: a value of the wrong type is refused, not stored",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const err = thrown(() =>
          s.client.run(
            `INSERT INTO "graph_ref" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, _version) ` +
              `VALUES ('A', 'recipe', 'Bread', 'main', 'today', 'Cook', 0, 'Cook', 1)`,
          ),
        );
        assert.ok(err instanceof SqliteError, String(err));
        // SQLITE_CONSTRAINT_DATATYPE.
        assert.equal(err.code, 3091);
        for (const table of sqliteTables) {
          const sql = s.client.get(`SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?1`, [defaultTableName(table)]);
          assert.match(String(sql?.["sql"]), /\) STRICT$/, `${table} is STRICT`);
        }
      });
    },
  },
  {
    name: "foreign keys check each of the layout's edges on a connection the adapter binds",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const { ref, commit } = s.storage.transact((tx) => refAndCommit(tx));
        let n = 0;
        const fresh = () => `Row${n++}`;
        // Each edge as an insert of a row that names the target, which holds
        // a valid row of every other column.
        const edges: [string, string, (target: string) => string][] = [
          ["a ref's parent", ref.id, (t) => `INSERT INTO "graph_ref" (id, graph, root_id, parent_ref_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('${fresh()}', 'recipe', 'Bread', '${t}', '${fresh()}', 0, 'Cook', 0, 'Cook', 1)`],
          ["a ref's base", commit, (t) => `INSERT INTO "graph_ref" (id, graph, root_id, base_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('${fresh()}', 'recipe', 'Bread', '${t}', '${fresh()}', 0, 'Cook', 0, 'Cook', 1)`],
          ["a ref's head", commit, (t) => `INSERT INTO "graph_ref" (id, graph, root_id, head_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('${fresh()}', 'recipe', 'Bread', '${t}', '${fresh()}', 0, 'Cook', 0, 'Cook', 1)`],
          ["a commit's ref", ref.id, (t) => `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, schema_epoch, content_hash, created_at, created_by) VALUES ('${fresh()}', 'recipe', 'Bread', '${t}', 1, 'h', 0, 'Cook')`],
          ["a commit's parent", commit, (t) => `INSERT INTO "graph_commit" (id, graph, root_id, ref_id, parent_commit_id, schema_epoch, content_hash, created_at, created_by) VALUES ('${fresh()}', 'recipe', 'Bread', '${ref.id}', '${t}', 1, 'h', 0, 'Cook')`],
          ["a patch's commit", commit, (t) => `INSERT INTO "graph_patch" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES ('${fresh()}', 'recipe', '${t}', 'step', '${fresh()}', 'B', 1, 'ADD')`],
          ["a snapshot entry's commit", commit, (t) => `INSERT INTO "graph_snapshot_entry" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES ('${fresh()}', 'recipe', '${t}', 'step', '${fresh()}', 'B', 1)`],
          ["a release pointer's commit", commit, (t) => `INSERT INTO "graph_release" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES ('${fresh()}', 'recipe', '${fresh()}', '${t}', 0, 'Cook', 0, 'Cook', 1)`],
          ["a member's ref", ref.id, (t) => `INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES ('${fresh()}', 'recipe', 'step', '${fresh()}', '${t}', 'Bread', 0, 1, '{}')`],
        ];
        for (const [edge, target, insert] of edges) {
          s.client.run(insert(target));
          const err = thrown(() => s.client.run(insert("Missing")));
          assert.ok(err instanceof SqliteError, `${edge}: ${String(err)}`);
          // SQLITE_CONSTRAINT_FOREIGNKEY.
          assert.equal(err.code, 787, edge);
        }
      });
    },
  },
  {
    name: "the adapter refuses a write whose ref or commit is another graph's or another root's",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const other = new SqliteAdapter(descriptor, { graph: "menu" }).storage(s.client);
        const mine = s.storage.transact((tx) => refAndCommit(tx));
        const soup = s.storage.transact((tx) => refAndCommit(tx, "Soup"));
        const theirs = other.transact((tx) => refAndCommit(tx));
        const refuses = (what: string, pattern: RegExp, fn: (tx: SyncTx) => unknown) =>
          assert.match(String(thrown(() => s.storage.transact(fn))), pattern, what);
        const newCommit = (ref: string, parent: string | null) => ({
          root: bread,
          ref,
          parent,
          message: "",
          schemaEpoch: 1,
          contentHash: "0".repeat(64),
          sequence: null,
          actor: cook,
        });
        const patch = { commit: "", kind: "step", entityKey: "Mix", entityId: "Row", entityVersion: 1, operation: "ADD" as const };
        const entry = { kind: "step", entityKey: "Mix", entityId: "Row", entityVersion: 1 };
        const cases: [string, string, string][] = [
          ["another graph's", theirs.ref.id, theirs.commit],
          ["another root's", soup.ref.id, soup.commit],
        ];
        for (const [whose, ref, commit] of cases) {
          refuses(`createRef with ${whose} parent`, /is not a ref of root/, (tx) => tx.createRef({ root: bread, parent: ref, base: null, name: "x", actor: cook }));
          refuses(`createRef with ${whose} base`, /is not a commit of root/, (tx) => tx.createRef({ root: bread, parent: null, base: commit, name: "x", actor: cook }));
          refuses(`updateRef to ${whose} head`, /is not a commit of root/, (tx) => tx.updateRef({ id: mine.ref.id, version: 1, head: commit, base: null, seal: false, actor: cook }));
          refuses(`updateRef to ${whose} base`, /is not a commit of root/, (tx) => tx.updateRef({ id: mine.ref.id, version: 1, head: null, base: commit, seal: false, actor: cook }));
          refuses(`insertCommit on ${whose} ref`, /is not a ref of root/, (tx) => tx.insertCommit(newCommit(ref, null)));
          refuses(`insertCommit after ${whose} commit`, /is not a commit of root/, (tx) => tx.insertCommit(newCommit(mine.ref.id, commit)));
          refuses(`writeRelease of ${whose} commit`, /is not a commit of root/, (tx) => tx.writeRelease({ root: bread, commit, version: 0, actor: cook }));
          refuses(`upsertRow on ${whose} ref`, /is not a ref of root/, (tx) =>
            tx.upsertRow("step", { ref, root: bread, row: stepRow("Mix", "Mix"), tombstone: false, actor: cook }),
          );
        }
        refuses("insertPatches of another graph's commit", /is not a commit of graph/, (tx) => tx.insertPatches(theirs.commit, [patch]));
        refuses("insertSnapshot of another graph's commit", /is not a commit of graph/, (tx) => tx.insertSnapshot(theirs.commit, [entry]));
        // The graph's own refs and commits of the root are taken.
        s.storage.transact((tx) => {
          tx.createRef({ root: bread, parent: mine.ref.id, base: mine.commit, name: "draft", actor: cook });
          tx.updateRef({ id: mine.ref.id, version: 1, head: mine.commit, base: mine.commit, seal: false, actor: cook });
          tx.insertCommit(newCommit(mine.ref.id, mine.commit));
          tx.writeRelease({ root: bread, commit: mine.commit, version: 0, actor: cook });
          tx.upsertRow("step", { ref: mine.ref.id, root: bread, row: stepRow("Mix", "Mix"), tombstone: false, actor: cook });
          tx.insertPatches(mine.commit, [patch]);
          tx.insertSnapshot(mine.commit, [entry]);
        });
      });
    },
  },
  {
    name: "two graphs in one file keep apart: each reads, names, sequences, releases and prunes only its own",
    run(binding) {
      let now = 1_800_000_000_000_000;
      const clock = () => now;
      const a = setup(binding, { clock });
      using([a], () => {
        // Graph menu keeps a day of step history, where recipe keeps 365.
        const d = JSON.parse(descriptor) as { kinds: { kind: string; history: { retentionDays?: number } }[] };
        d.kinds.find((k) => k.kind === "step")!.history.retentionDays = 1;
        const menu = JSON.stringify(d);
        const b = new SqliteAdapter(menu, { graph: "menu", clock }).storage(a.client);
        const bEngine = new SyncEngine(core, menu, b, { schemaEpoch: 1, snapshotEvery: 3 });
        const main = a.engine.createPrimary(cook, bread, "main");
        // The same root and name in the other graph is not taken.
        const other = bEngine.createPrimary(cook, bread, "main");
        assert.ok(thrown(() => bEngine.compose(main.id)) instanceof NotFoundError);
        // Each graph saves a step three times on a change set, commits it
        // tagged, and merges it into its primary line, tagged.
        const work = (engine: SyncEngine, primary: Ref) => {
          let draft = engine.branch(cook, primary.id, "draft");
          for (const instruction of ["Mix", "Mix well", "Mix hard"]) {
            draft = engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow("Mix", instruction)] } }).ref;
          }
          const committed = engine.commit(cook, draft.id, draft.version, { tag: true });
          const merged = engine.merge(cook, draft.id, primary.id, primary.version, [], { tag: true });
          return { draft: committed.ref, commit: committed.commit!, merged: merged.commit! };
        };
        const aw = work(a.engine, main);
        assert.deepEqual(b.transact((tx) => tx.rows("step", aw.draft.id)), []);
        assert.equal(b.transact((tx) => tx.commits()).length, 0);
        assert.ok(thrown(() => bEngine.materialize(aw.commit.id)) instanceof NotFoundError);
        assert.ok(thrown(() => bEngine.release(cook, bread, aw.commit.id, 0)) instanceof NotFoundError);
        assert.ok(thrown(() => bEngine.history(aw.draft.id)) instanceof NotFoundError);
        assert.ok(thrown(() => bEngine.branch(cook, aw.draft.id, "x")) instanceof NotFoundError);
        assert.equal(b.transact((tx) => tx.nextSequence(bread)), 1);
        // Both graphs tag the same root, each numbering its own tags from 1.
        const bw = work(bEngine, other);
        assert.deepEqual([aw.commit.sequence, aw.merged.sequence, bw.commit.sequence, bw.merged.sequence], [1, 2, 1, 2]);
        // A first pointer in each graph, then a move of menu's while
        // recipe's is at the same version, then a move of recipe's.
        a.engine.release(cook, bread, aw.commit.id, 0);
        assert.ok(thrown(() => bEngine.released(bread)) instanceof NotFoundError);
        bEngine.release(cook, bread, bw.commit.id, 0);
        bEngine.release(cook, bread, bw.merged.id, 1);
        a.engine.release(cook, bread, aw.merged.id, 1);
        const released = [a.engine.released(bread).release, bEngine.released(bread).release];
        assert.deepEqual(
          released.map((r) => [r.commit, r.version]),
          [
            [aw.merged.id, 2],
            [bw.merged.id, 2],
          ],
        );
        // A write through one graph's ref from the other is refused.
        const refused = thrown(() =>
          b.transact((tx) => tx.upsertRow("step", { ref: aw.draft.id, root: bread, row: stepRow("Mix", "Mix"), tombstone: false, actor: cook })),
        );
        assert.match(String(refused), /is not a ref of root/);
        // Thirty days on, menu's sweep prunes its own step images past its
        // day, and none of recipe's, which keeps 365.
        const images = (graph: string) => a.client.get(`SELECT count(*) AS n FROM "graph_member_history" WHERE graph = ?1`, [graph])?.["n"];
        const kept = images("recipe");
        now += 30 * 86_400_000_000;
        assert.deepEqual(bEngine.sweep({ actor: cook }).pruned, { step: 2 });
        assert.equal(images("recipe"), kept);
        assert.deepEqual(a.engine.sweep({ actor: cook }).pruned, {});
        assert.equal(bEngine.compose(other.id).contentHash, a.engine.compose(main.id).contentHash);
      });
    },
  },
  {
    name: "the name function names every table and index",
    run(binding) {
      const tableName = (name: string) => `vg_${name}`;
      const s = setup(binding, { tableName });
      using([s], () => {
        const objects = s.client.all(`SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY name`);
        const tables = objects.filter((o) => o["type"] === "table").map((o) => o["name"]);
        assert.deepEqual(tables, sqliteTables.map(tableName).sort());
        for (const o of objects) {
          assert.match(String(o["name"]), /^vg_[a-z_]+$/, `${String(o["type"])} ${String(o["name"])} is named by the function`);
          assert.match(String(o["tbl_name"]), /^vg_/);
        }
        assert.ok(objects.some((o) => o["type"] === "index"));
        const main = s.engine.createPrimary(cook, bread, "main");
        const draft = s.engine.branch(cook, main.id, "draft");
        s.engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow("Mix", "Mix")] } });
        assert.equal(s.client.get(`SELECT count(*) AS n FROM "vg_member"`)?.["n"], 1);
        // The layout's statements name each object with the function, and the default's start with graph_.
        for (const statement of sqliteLayout()) {
          assert.match(statement, /^CREATE (UNIQUE )?(TABLE|INDEX) IF NOT EXISTS "graph_[a-z_]+" /);
        }
      });
    },
  },
  {
    name: "the clock: a transaction reads it once, and every write in it has its time",
    run(binding) {
      let calls = 0;
      const start = 1_800_000_000_000_000;
      const clock = () => start + ++calls * 1_000_001;
      const s = setup(binding, { clock });
      using([s], () => {
        calls = 0;
        const main = s.engine.createPrimary(cook, bread, "main");
        const draft = s.engine.branch(cook, main.id, "draft");
        const saved = s.engine.save(cook, draft.id, draft.version, {
          step: { upsert: [stepRow("Mix", "Mix"), stepRow("Rest", "Rest")] },
        });
        assert.equal(calls, 3, "one read of the clock per transaction");
        const at = (n: number) => start + n * 1_000_001;
        for (const row of saved.saved["step"]!) {
          assert.equal(member(row, "created_at"), '"2027-01-15T08:00:03.000003Z"');
          assert.equal(member(row, "updated_at"), '"2027-01-15T08:00:03.000003Z"');
        }
        const recorded = s.client.all(`SELECT DISTINCT recorded_at FROM "graph_member_history"`);
        assert.deepEqual(recorded.map((r) => r["recorded_at"]), [at(3)]);
        const refRow = s.client.get(`SELECT created_at, updated_at FROM "graph_ref" WHERE id = ?1`, [draft.id]);
        assert.deepEqual([refRow?.["created_at"], refRow?.["updated_at"]], [at(2), at(3)]);
        const committed = s.engine.commit(cook, draft.id, saved.ref.version);
        assert.equal(calls, 4);
        assert.equal(committed.commit!.createdAt, "2027-01-15T08:00:04.000004Z");
        // A transaction inside another is a savepoint at the outer one's time.
        const inner = s.storage.transact((tx) => {
          tx.createRef({ root: "Soup", parent: null, base: null, name: "outer", actor: cook });
          return s.storage.transact((nested) => nested.createRef({ root: "Soup", parent: null, base: null, name: "inner", actor: cook }));
        });
        assert.equal(calls, 5);
        assert.equal(s.client.get(`SELECT created_at FROM "graph_ref" WHERE id = ?1`, [inner.id])?.["created_at"], at(5));
        assert.match(String(thrown(() => new SqliteAdapter(descriptor, { graph, clock: () => 1.5 }).storage(s.client).transact((tx) => tx.readRef(main.id)))), /not a whole number of microseconds/);
      });
    },
  },
  {
    name: "a transaction reads the clock once the write lock is held",
    run(binding, dir) {
      const path = join(dir, "clock.sqlite");
      const second = binding.open(path);
      try {
        second.client.exec!("PRAGMA busy_timeout = 0");
        // Whether another connection is kept from the write lock when the clock is read.
        const held: boolean[] = [];
        const clock = () => {
          try {
            second.client.exec!("BEGIN IMMEDIATE");
            second.client.exec!("ROLLBACK");
            held.push(false);
          } catch (err) {
            held.push(err instanceof SqliteError && err.code === 5);
          }
          return 1_800_000_000_000_000;
        };
        const s = setup(binding, { clock }, path);
        using([s], () => {
          s.storage.transact((tx) => tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook }));
          s.engine.createPrimary(cook, "Soup", "main");
          assert.deepEqual(held, [true, true, true]);
        });
      } finally {
        second.close();
      }
    },
  },
  {
    name: "in the caller's transaction each transaction reads the clock once, and one whose function returns a promise is refused",
    run(binding) {
      const db = binding.open(":memory:");
      try {
        let calls = 0;
        const adapter = new SqliteAdapter(descriptor, { graph, callerTransaction: true, clock: () => 1_800_000_000_000_000 + ++calls });
        inCallerTransaction(db.client, () => adapter.createTables(db.client));
        const storage = adapter.storage(db.client);
        calls = 0;
        const [first, second] = inCallerTransaction(db.client, () =>
          storage.transact((tx) => [
            tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook }),
            tx.createRef({ root: "Soup", parent: null, base: null, name: "main", actor: cook }),
          ]),
        );
        assert.equal(calls, 1);
        const times = db.client.all(`SELECT DISTINCT created_at FROM "graph_ref" WHERE id IN (?1, ?2)`, [first!.id, second!.id]);
        assert.deepEqual(times.map((row) => row["created_at"]), [1_800_000_000_000_001]);
        inCallerTransaction(db.client, () => storage.transact((tx) => tx.readRef(first!.id)));
        assert.equal(calls, 2);
        assert.match(String(thrown(() => inCallerTransaction(db.client, () => storage.transact(() => Promise.resolve(1))))), /its function returned a promise/);
      } finally {
        db.close();
      }
    },
  },
  {
    name: "storage refuses a connection whose foreign keys would not turn on",
    run(binding) {
      const db = binding.open(":memory:");
      try {
        const adapter = new SqliteAdapter(descriptor, { graph });
        adapter.createTables(db.client);
        db.client.exec!("PRAGMA foreign_keys = OFF");
        // SQLite ignores the pragma inside a transaction.
        db.client.exec!("BEGIN");
        assert.match(String(thrown(() => adapter.storage(db.client))), /foreign keys would not turn on/);
        db.client.exec!("ROLLBACK");
        adapter.storage(db.client);
        assert.equal(db.client.get("PRAGMA foreign_keys")?.["foreign_keys"], 1);
      } finally {
        db.close();
      }
    },
  },
  {
    name: "prune: an image exactly its retention old stays and one a microsecond older goes, and the oldest goes first",
    run(binding) {
      const start = 1_800_000_000_000_000;
      let now = start;
      const s = setup(binding, { clock: () => now });
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        let draft = s.engine.branch(cook, main.id, "draft");
        for (const instruction of ["Mix", "Mix well"]) {
          draft = s.engine.save(cook, draft.id, draft.version, {
            step: { upsert: [stepRow("Mix", instruction), stepRow("Rest", instruction), stepRow("Bake", instruction)] },
          }).ref;
        }
        const prune = (batch: number) => s.storage.transact((tx) => tx.prune("step", 0, batch));
        const left = () =>
          s.client
            .all(`SELECT id FROM "graph_member_history" WHERE kind = 'step' AND _version = 1`)
            .map((row) => row["id"] as string)
            .sort();
        // Each row's version 1 is prunable once it is past 365 days old, and
        // not at exactly 365 days.
        now = start + 365 * 86_400_000_000;
        assert.equal(prune(0), 0);
        // Make the row with the greatest id the oldest by a microsecond more
        // than the next, so oldest first and id order disagree.
        const ids = left();
        s.client.run(`UPDATE "graph_member_history" SET recorded_at = recorded_at - 2 WHERE id = ?1 AND _version = 1`, [ids[2]!]);
        s.client.run(`UPDATE "graph_member_history" SET recorded_at = recorded_at - 1 WHERE id = ?1 AND _version = 1`, [ids[1]!]);
        assert.equal(prune(1), 1);
        assert.deepEqual(left(), [ids[0], ids[1]], "the oldest went first");
        assert.equal(prune(0), 1);
        assert.deepEqual(left(), [ids[0]], "an image exactly 365 days old stays");
        now += 1;
        assert.equal(prune(0), 1);
        assert.deepEqual(left(), []);
      });
    },
  },
  {
    name: "a commit's time is a canonical date-time: no trailing zeros in its fraction, and no fraction when it is zero",
    run(binding) {
      let now = 1_800_000_000_120_000;
      const s = setup(binding, { clock: () => now });
      using([s], () => {
        const ref = s.storage.transact((tx) => tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook }));
        const commit = () =>
          s.storage.transact((tx) =>
            tx.insertCommit({ root: bread, ref: ref.id, parent: null, message: "", schemaEpoch: 1, contentHash: "0".repeat(64), sequence: null, actor: cook }),
          );
        assert.equal(commit().createdAt, "2027-01-15T08:00:00.12Z");
        now = 1_800_000_000_000_000;
        const whole = commit();
        assert.equal(whole.createdAt, "2027-01-15T08:00:00Z");
        assert.equal(s.storage.transact((tx) => tx.readCommit(whole.id)).createdAt, "2027-01-15T08:00:00Z");
        now = 1_800_000_000_000_001;
        assert.equal(commit().createdAt, "2027-01-15T08:00:00.000001Z");
      });
    },
  },
  {
    name: "every id the adapter writes is a version-4 UUID in its canonical form",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const main = s.engine.createPrimary(cook, bread, "main");
        const draft = s.engine.branch(cook, main.id, "draft");
        const saved = s.engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow(null, "Mix")] } });
        const committed = s.engine.commit(cook, draft.id, saved.ref.version);
        const merged = s.engine.merge(cook, draft.id, main.id, main.version, [], { tag: true });
        s.engine.release(cook, bread, merged.commit!.id, 0);
        const ids: string[] = [];
        const columns: [string, string[]][] = [
          ["ref", ["id"]],
          ["ref_history", ["history_id"]],
          ["commit", ["id"]],
          ["patch", ["id"]],
          ["snapshot_entry", ["id"]],
          ["release", ["id"]],
          ["release_history", ["history_id"]],
          ["member", ["id", "entity_key"]],
          ["member_history", ["history_id"]],
        ];
        for (const [table, names] of columns) {
          const rows = s.client.all(`SELECT ${names.join(", ")} FROM "graph_${table}"`);
          assert.ok(rows.length > 0, `${table} has rows`);
          for (const row of rows) {
            for (const name of names) {
              assertCanonicalID(row[name], `${table}.${name}`);
              ids.push(row[name] as string);
            }
          }
        }
        assert.equal(new Set(ids).size, ids.length - 1, "every id is new but the entity key two rows share");
        assertCanonicalID(committed.commit!.id, "a commit's id");
      });
    },
  },
  {
    name: "a second connection's BEGIN IMMEDIATE waits for the write lock, and then fails busy",
    run(binding, dir) {
      const path = join(dir, "lock.sqlite");
      const a = setup(binding, {}, path);
      const second = binding.open(path);
      using([a], () => {
        try {
          second.client.exec!("PRAGMA busy_timeout = 300");
          const b = new SqliteAdapter(descriptor, { graph }).storage(second.client);
          let waited = 0;
          let busy: unknown;
          let ran = false;
          a.storage.transact((tx) => {
            tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook });
            const started = Date.now();
            busy = thrown(() =>
              b.transact((other) => {
                ran = true;
                return other.readRef("Missing");
              }),
            );
            waited = Date.now() - started;
          });
          assert.ok(busy instanceof SqliteError, String(busy));
          assert.equal(busy.code, 5, "SQLITE_BUSY");
          assert.ok(waited >= 250, `waited ${waited}ms for the lock`);
          // The transaction begins by taking the write lock, so even one that
          // would only read never starts.
          assert.equal(ran, false);
          // Once the first transaction commits, the second connection writes, and reads what the first wrote.
          b.transact((other) => other.createRef({ root: "Soup", parent: null, base: null, name: "main", actor: cook }));
          assert.equal(second.client.get(`SELECT count(*) AS n FROM "graph_ref"`)?.["n"], 2);
        } finally {
          second.close();
        }
      });
    },
  },
  {
    name: "a transaction that throws rolls back, and one inside another is a savepoint that rolls back alone",
    run(binding) {
      const s = setup(binding);
      using([s], () => {
        const create = (tx: SyncTx, name: string) => tx.createRef({ root: bread, parent: null, base: null, name, actor: cook });
        assert.match(
          String(
            thrown(() =>
              s.storage.transact((tx) => {
                create(tx, "gone");
                throw new Error("rolled back");
              }),
            ),
          ),
          /rolled back/,
        );
        s.storage.transact((tx) => {
          create(tx, "kept");
          assert.ok(thrown(() => s.storage.transact((nested) => (create(nested, "inner"), create(nested, "kept")))) instanceof NameTakenError);
          create(tx, "after");
        });
        const names = s.client.all(`SELECT name FROM "graph_ref" ORDER BY name`).map((row) => row["name"]);
        assert.deepEqual(names, ["after", "kept"]);
        assert.match(String(thrown(() => s.storage.transact(() => Promise.resolve(1)))), /its function returned a promise/);
        assert.deepEqual(s.client.all(`SELECT name FROM "graph_ref" ORDER BY name`).map((row) => row["name"]), ["after", "kept"]);
      });
    },
  },
  {
    name: "in the caller's transaction the adapter issues no transaction control, and the caller's rollback undoes its writes",
    run(binding) {
      const db = binding.open(":memory:");
      try {
        db.client.exec!("PRAGMA foreign_keys = ON");
        const ran: Ran[] = [];
        const adapter = new SqliteAdapter(descriptor, { graph, callerTransaction: true, tableName: behaviorTable });
        // A client with no exec, as a behavior's sql has none.
        inCallerTransaction(db.client, () => adapter.createTables(checkedClient(db.client, "migrate", ran)));
        const storage = adapter.storage(checkedClient(db.client, "write", ran));
        const engine = new SyncEngine(core, descriptor, storage, { schemaEpoch: 1, snapshotEvery: 3 });
        const main = inCallerTransaction(db.client, () => engine.createPrimary(cook, bread, "main"));
        const draft = inCallerTransaction(db.client, () => engine.branch(cook, main.id, "draft"));
        const saved = inCallerTransaction(db.client, () =>
          engine.save(cook, draft.id, draft.version, { step: { upsert: [stepRow("Mix", "Mix")] }, utensil: { upsert: ['{"entity_key": "Whisk", "name": "whisk"}'] } }),
        );
        const committed = inCallerTransaction(db.client, () => engine.commit(cook, draft.id, saved.ref.version));
        const merged = inCallerTransaction(db.client, () => engine.merge(cook, draft.id, main.id, main.version, [], { tag: true }));
        inCallerTransaction(db.client, () => engine.release(cook, bread, merged.commit!.id, 0));
        inCallerTransaction(db.client, () => engine.sweep({ actor: cook }));
        // Reads run as a behavior's read: only SELECT.
        const reader = new SyncEngine(core, descriptor, adapter.storage(checkedClient(db.client, "read", ran)), { schemaEpoch: 1, snapshotEvery: 3 });
        inCallerTransaction(db.client, () => {
          reader.compose(draft.id);
          reader.materialize(committed.commit!.id);
          reader.released(bread);
          reader.history(draft.id);
          reader.diff(committed.commit!.id, merged.commit!.id);
        });
        assert.ok(ran.length > 0);
        for (const { sql } of ran) {
          assert.doesNotMatch(sql, /^\s*(BEGIN|COMMIT|END|ROLLBACK|SAVEPOINT|RELEASE|PRAGMA)\b/i);
        }
        // The caller rolls back, and the graph's writes go with it.
        db.client.exec!("BEGIN IMMEDIATE");
        const dropped = engine.branch(cook, main.id, "dropped");
        db.client.exec!("ROLLBACK");
        assert.ok(thrown(() => inCallerTransaction(db.client, () => engine.compose(dropped.id))) instanceof NotFoundError);
        assert.equal(db.client.get(`SELECT count(*) AS n FROM "${behaviorTable("ref")}"`)?.["n"], 2);
      } finally {
        db.close();
      }
    },
  },
  {
    name: "the layout's statements pass D16's checks for a behavior's migration, and create only the behavior's own tables and indexes",
    run(binding) {
      const statements = sqliteLayout(behaviorTable);
      assert.equal(statements.length, new Set(statements).size);
      for (const statement of statements) {
        assert.equal(sqlRefusal(statement, behaviorPrefix, "migrate"), undefined, statement);
      }
      const db = binding.open(":memory:");
      try {
        for (const statement of statements) {
          db.client.run(statement);
        }
        const own = (name: string) => name.startsWith(behaviorPrefix) && !name.startsWith(behaviorPrefix + "_");
        for (const object of db.client.all(`SELECT type, name, tbl_name FROM sqlite_schema`)) {
          const name = String(object["name"]);
          const named = object["type"] === "index" && name.startsWith("sqlite_autoindex_") ? own(String(object["tbl_name"])) : own(name);
          assert.ok((object["type"] === "table" || object["type"] === "index") && named && own(String(object["tbl_name"])), name);
        }
      } finally {
        db.close();
      }
    },
  },
  {
    name: "every canonical vector reads back as the canonical value written, live and from history, and its Postgres form is stored canonical",
    run(binding) {
      const vectors = new URL("../../testdata/canonical/", import.meta.url);
      type ValueCase = { name: string; class: string; postgres: string; canonical?: string };
      type RowCase = { name: string; columns: Record<string, string>; postgres: string; canonical?: string };
      const values: ValueCase[] = [];
      const rows: RowCase[] = [];
      for (const file of readdirSync(vectors).filter((name) => name.endsWith(".json")).sort()) {
        const doc = JSON.parse(readFileSync(new URL(file, vectors), "utf8")) as { cases?: ValueCase[]; rows?: RowCase[] };
        values.push(...(doc.cases ?? []));
        rows.push(...(doc.rows ?? []));
      }
      assert.ok(values.length > 0 && rows.length > 0);
      // A kind per case, whose role columns are named apart from its own.
      const roles = { key: "vg_key", id: "vg_id", ref: "vg_ref", root: "vg_root", tombstone: "vg_tombstone", version: "vg_version" };
      const roleColumns = { vg_key: "uuid", vg_id: "uuid", vg_ref: "uuid", vg_root: "uuid", vg_tombstone: "boolean", vg_version: "integer" };
      // Input forms the canonical rules read, for each list class, beside
      // the vectors' Postgres forms: each is stored canonical, as
      // canonicalValue gives it, or refused.
      const inputs: [string, string][] = [
        ["string[]", '["a", 1]'],
        ["string[][]", '[["a"], [2]]'],
        ["integer[]", "[-0, 12]"],
        ["integer[][]", "[[-0, 7], []]"],
        ["integer[][]", "[[1.5]]"],
        ["number[]", "[1e2, -0]"],
        ["number[][]", "[[1.50e1]]"],
        ["boolean[]", '["true"]'],
        ["boolean[][]", '[[true], ["true"]]'],
        ["uuid[][]", '[["5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"]]'],
        ["dateTime[]", '["2026-09-01T12:30:00+02:30"]'],
        ["dateTime[][]", '[["2026-09-01T12:30:00.10+02:30"]]'],
        ["date[]", '["2026-02-30"]'],
        ["date[]", '["2026-09-01", "nope"]'],
        ["date[][]", '[["2026-02-29"]]'],
        ["time[]", '["2:30 pm"]'],
        ["duration[]", '["1 day 02:00:00"]'],
        ["enum[]", "[1]"],
        ["enum[][]", '[["again"], [1]]'],
        ["json[]", '[{"b": 1.50, "a": [2.0]}]'],
        ["json[][]", '[[{"b": 1, "a": 0}]]'],
      ];
      const kindOf = (kind: string, columns: Record<string, string>) => ({ kind, ...roles, history: { exclude: [] }, columns: { ...roleColumns, ...columns } });
      const kinds = [
        ...values.map((c, i) => kindOf(`value${i}`, { v: c.class })),
        ...values.map((c, i) => kindOf(`postgres${i}`, { v: c.class })),
        ...rows.map((c, i) => kindOf(`row${i}`, c.columns)),
        ...rows.map((c, i) => kindOf(`postgresRow${i}`, c.columns)),
        ...inputs.map(([valueClass], i) => kindOf(`input${i}`, { v: valueClass })),
      ];
      const db = binding.open(":memory:");
      try {
        const adapter = new SqliteAdapter(JSON.stringify({ version: 3, kinds }), { graph: "vectors" });
        adapter.createTables(db.client);
        const storage = adapter.storage(db.client);
        const ref = storage.transact((tx) => tx.createRef({ root: bread, parent: null, base: null, name: "main", actor: cook }));
        const write = (kind: string, row: string) =>
          storage.transact((tx) => tx.upsertRow(kind, { ref: ref.id, root: bread, row, tombstone: false, actor: cook }));
        const stored = (kind: string) => db.client.get(`SELECT id, data FROM "graph_member" WHERE kind = ?1`, [kind]);
        values.forEach((c, i) => {
          const kind = `value${i}`;
          if (c.canonical === undefined) {
            // A value its class refuses is not stored.
            assert.ok(thrown(() => write(kind, `{"v":${c.postgres}}`)) instanceof CanonicalError, `${c.class}/${c.name}`);
            assert.equal(stored(kind), undefined);
            return;
          }
          const written = write(kind, `{"v":${c.canonical}}`);
          const row = stored(kind)!;
          assert.equal(row["data"], `{"v":${c.canonical}}`, `${c.class}/${c.name} is stored canonical`);
          write(`postgres${i}`, `{"v":${c.postgres}}`);
          assert.equal(stored(`postgres${i}`)?.["data"], `{"v":${c.canonical}}`, `${c.class}/${c.name}'s Postgres form is stored canonical`);
          const [read] = storage.transact((tx) => tx.rows(kind, ref.id));
          const [image] = storage.transact((tx) => tx.images(kind, [{ id: row["id"] as string, version: 1 }]));
          for (const text of [written, read!, image!]) {
            assert.ok(text.startsWith(`{"v":${c.canonical},"vg_id":`), `${c.class}/${c.name}: ${text}`);
          }
        });
        rows.forEach((c, i) => {
          const kind = `row${i}`;
          if (c.canonical === undefined) {
            assert.ok(thrown(() => write(kind, c.postgres)) !== undefined);
            assert.equal(stored(kind), undefined);
            return;
          }
          write(kind, c.canonical);
          write(`postgresRow${i}`, c.postgres);
          // Every declared column, the ones the row lacks as null.
          const canonical = parseJson(c.canonical) as JsonObject;
          const expected = Object.keys(c.columns)
            .sort()
            .map((column) => JSON.stringify(column) + ":" + (canonical.has(column) ? stringifyJson(canonical.get(column)!) : "null"))
            .join(",");
          assert.equal(stored(kind)?.["data"], `{${expected}}`, c.name);
          assert.equal(stored(`postgresRow${i}`)?.["data"], `{${expected}}`, `${c.name}: its Postgres form`);
        });
        inputs.forEach(([valueClass, input], i) => {
          const kind = `input${i}`;
          let want: string | undefined;
          try {
            want = canonicalValue(valueClass, input);
          } catch {
            want = undefined;
          }
          if (want === undefined) {
            assert.ok(thrown(() => write(kind, `{"v":${input}}`)) instanceof CanonicalError, `${valueClass} ${input} is refused`);
            assert.equal(stored(kind), undefined);
            return;
          }
          assert.notEqual(stringifyJson(parseJson(input)), want, `${valueClass} ${input} is not already canonical`);
          write(kind, `{"v":${input}}`);
          assert.equal(stored(kind)?.["data"], `{"v":${want}}`, `${valueClass} ${input} is stored canonical`);
        });
      } finally {
        db.close();
      }
    },
  },
  {
    name: "the binding returns plain rows and undefined for no row, and throws SQLite's extended result code",
    run(binding) {
      const db = binding.open(":memory:");
      try {
        db.client.run("CREATE TABLE t (a TEXT NOT NULL UNIQUE, b INTEGER) STRICT");
        assert.equal(Number(db.client.run("INSERT INTO t (a, b) VALUES (?1, ?2)", ["x", 1]).changes), 1);
        const row = db.client.get("SELECT a, b, NULL AS c FROM t WHERE a = ?1", ["x"]);
        assert.equal(Object.getPrototypeOf(row), Object.prototype);
        assert.deepEqual(row, { a: "x", b: 1, c: null });
        assert.equal(db.client.get("SELECT a FROM t WHERE a = ?1", ["y"]), undefined);
        assert.ok(db.client.all("SELECT a FROM t").every((r) => Object.getPrototypeOf(r) === Object.prototype));
        const unique = thrown(() => db.client.run("INSERT INTO t (a, b) VALUES (?1, ?2)", ["x", 2]));
        assert.ok(unique instanceof SqliteError, String(unique));
        assert.equal(unique.code, 2067);
        assert.match(unique.message, /UNIQUE constraint failed/);
        const syntax = thrown(() => db.client.all("SELEC 1"));
        assert.ok(syntax instanceof SqliteError, String(syntax));
        assert.equal(syntax.code, 1);
      } finally {
        db.close();
      }
    },
  },
  {
    name: "new SqliteAdapter refuses what it cannot run",
    run() {
      type Doc = { [key: string]: unknown; kinds: { [key: string]: unknown; columns: Record<string, string> }[] };
      const refusals: [string, (d: Doc) => void, Partial<SqliteOptions>, string][] = [
        ["the fixture's descriptor", () => {}, {}, ""],
        ["a descriptor of version 2", (d) => (d.version = 2), {}, "reads version 3"],
        ["no graph", () => {}, { graph: "" }, "needs its graph's name"],
        ["a kind without a root column", (d) => delete d.kinds[0]!["root"], {}, "has no root"],
        ["a role column missing from the kind's columns", (d) => delete d.kinds[0]!.columns["_version"], {}, 'version column "_version" is not in its columns'],
        ["a tombstone that is not boolean", (d) => (d.kinds[0]!.columns["deleted_on_ref"] = "integer"), {}, "a tombstone is a boolean column"],
        ["a kind without history", (d) => delete d.kinds[0]!["history"], {}, "has no history"],
      ];
      for (const [name, edit, options, refuse] of refusals) {
        const d = JSON.parse(descriptor) as Doc;
        edit(d);
        const build = () => new SqliteAdapter(JSON.stringify(d), { graph, ...options });
        if (refuse === "") {
          build();
        } else {
          assert.throws(build, new RegExp(refuse.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")), name);
        }
      }
    },
  },
];
