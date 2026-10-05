// The shared SQLite vectors (runtime/versiongraph/testdata/sqlite): the
// layout's statements, a database the TypeScript adapter wrote, as SQL text,
// and what that database reads back as. Every language's SQLite adapter is
// held to them, so a file one adapter writes reads the same in another's.
//
// writeDatabase is the script that writes the database: SyncEngine over the
// adapter, with the scenario fixture's descriptor, a fixed clock, and ids
// from a seeded generator in place of crypto.randomUUID while it runs, so a
// rerun writes the same file. dumpDatabase writes a database as the SQL
// text typescript.sql holds, loadDatabase loads such text, and readVectors
// reads a loaded database as typescript.json holds it. The tests are in
// test/sqlite-vectors.test.ts, and test/node.mjs runs them under Node.
//
// It imports no SQLite module, so it loads under bun and Node alike; under
// Node it loads with Node's type stripping, so it uses no syntax that needs
// compiling.
import assert from "node:assert/strict";
import { readFileSync, writeFileSync } from "node:fs";
import {
  compareCodePoints,
  errorCode,
  isJsonObject,
  parseJson,
  SyncEngine,
  writeJsonString,
  type Commit,
  type Edits,
  type Finding,
  type MergeResult,
  type Ref,
  type Release,
  type SyncTx,
  type Tree,
  type TreeResult,
} from "../dist/engine.js";
import { initSync } from "../dist/index.js";
import {
  defaultTableName,
  SqliteAdapter,
  sqliteLayout,
  sqliteTables,
  type SqliteClient,
  type SqliteValue,
} from "../dist/sqlite.js";

const vectors = new URL("../../testdata/sqlite/", import.meta.url);

/** layout.json: sqliteLayout() under the default names. */
export const layoutFile = new URL("layout.json", vectors);

/** typescript.sql: the database the script writes, as SQL text. */
export const sqlFile = new URL("typescript.sql", vectors);

/** typescript.json: what typescript.sql reads back as. */
export const readsFile = new URL("typescript.json", vectors);

/** Set to 1, the tests rewrite the vectors instead of comparing with them. */
export const updating = process.env["UPDATE_SQLITE_VECTORS"] === "1";

/** The scenario fixture's descriptor, fixture-version-graph-db's Recipe graph, as JSON text. */
export const descriptor = readFileSync(new URL("../../testdata/fixture/recipe.json", import.meta.url), "utf8");

/** The engine options the script writes with, as the scenarios run; a reader needs the schema epoch. */
export const engineOptions = { schemaEpoch: 1, snapshotEvery: 3 };

const kinds = (JSON.parse(descriptor) as { kinds: { kind: string; key: string }[] }).kinds
  .map((k) => ({ name: k.kind, key: k.key }))
  .sort((a, b) => compareCodePoints(a.name, b.name));

const core = initSync();

// The script's actors and its root, each a UUID in its canonical form.
const cook = "Cook";
const ann = "Ann";
const bread = "Bread";

/** The time of the script's first transaction: 2026-10-05T09:00:00Z, in microseconds. */
const firstTime = Date.UTC(2026, 9, 5, 9, 0, 0) * 1000;

/** How far the clock moves at each read: 1.250005 s, so times carry microseconds and trimmed zeros. */
const clockStep = 1_250_005;

/** The seed of the script's ids. */
const idSeed = 0x5eed_d32an;

const mask64 = (1n << 64n) - 1n;

/** Version-4 UUIDs, hyphenated, from a splitmix64 generator seeded with seed. */
function seededUUIDs(seed: bigint): () => string {
  let state = seed;
  const next = (): bigint => {
    state = (state + 0x9e3779b97f4a7c15n) & mask64;
    let z = state;
    z = ((z ^ (z >> 30n)) * 0xbf58476d1ce4e5b9n) & mask64;
    z = ((z ^ (z >> 27n)) * 0x94d049bb133111ebn) & mask64;
    return z ^ (z >> 31n);
  };
  return () => {
    let n = (next() << 64n) | next();
    n = (n & ~(0xfn << 76n)) | (0x4n << 76n); // version 4
    n = (n & ~(0x3n << 62n)) | (0x2n << 62n); // variant 10
    const hex = n.toString(16).padStart(32, "0");
    return `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}`;
  };
}

/** Runs fn with crypto.randomUUID giving the seeded ids, and puts it back however fn ends. */
function withSeededIDs<T>(fn: () => T): T {
  const crypto = globalThis.crypto;
  const own = Object.getOwnPropertyDescriptor(crypto, "randomUUID");
  Object.defineProperty(crypto, "randomUUID", { value: seededUUIDs(idSeed), configurable: true, writable: true });
  try {
    return fn();
  } finally {
    if (own !== undefined) {
      Object.defineProperty(crypto, "randomUUID", own);
    } else {
      delete (crypto as { randomUUID?: unknown }).randomUUID;
    }
  }
}

/** A clock that reads firstTime, then moves clockStep at each read. */
function fixedClock(): () => number {
  let now = firstTime;
  return () => {
    const time = now;
    now += clockStep;
    return time;
  };
}

/** A merge that wrote a commit, with no conflicts. */
function merged(result: MergeResult): { ref: Ref; commit: Commit } {
  assert.deepEqual(result.conflicts, [], "the script's merges have no conflicts");
  assert.notEqual(result.commit, null, "the script's merges write a commit");
  return { ref: result.ref, commit: result.commit! };
}

// The tastings: a value of every class the descriptor names, the first in
// canonical form and the second in the schema runtime's other forms, as the
// canonical_rows scenario writes them, the first with text outside ASCII
// too. Each is JSON text, so the integer wider than a double keeps its
// digits.
const firstTasting =
  '{"entity_key":"First","taster":"Ann","salty":true,"score":4.5,"servings":9007199254740993,' +
  '"tasted_on":"2026-09-01","tasted_at":"2026-09-01T10:00:00.12Z","served_at":"18:30:00","rested":"1h30m0s",' +
  '"verdict":"again","remarks":{"crust":[1,2.50],"crumb":"open"},"tags":["sour","a \\"quoted\\" tag","crème brûlée 🍞"],' +
  '"helpers":["Bob","Cy"],"bites":[[1,2],[3]]}';
const secondTasting =
  '{"entity_key":"00000000-0000-0000-0000-000000000002","taster":"00000000-0000-0000-0000-00000000000a","salty":false,' +
  '"score":1e21,"servings":-3,"tasted_on":"2026-02-28","tasted_at":"2026-09-01T12:30:00+02:30","served_at":"2:30 pm",' +
  '"rested":"-1m30.5s","verdict":"never","remarks":null,"tags":[],"helpers":["00000000-0000-0000-0000-00000000003d"],"bites":[]}';

const row = (columns: Record<string, unknown>): string => JSON.stringify(columns);

/**
 * The script: writes two graphs into the database client holds, "recipe"
 * and "menu", both of root Bread. In recipe: a primary line; a change set
 * with a row of every kind (a tasting of every value class, a utensil whose
 * key the adapter generates, a note without its optional parent), committed,
 * merged as a tagged commit, released and then sealed; a second change set
 * that updates a step with a partial row, tombstones an ingredient, adds a
 * step and unsets it as another actor, commits twice and merges as a second
 * tagged commit, which the release moves to, then saves work it does not
 * commit; and a draft that saves and is discarded. In menu: a primary line,
 * a change set, a tagged merge and a release.
 */
export function writeDatabase(client: SqliteClient): void {
  withSeededIDs(() => {
    const clock = fixedClock();
    const recipe = new SqliteAdapter(descriptor, { graph: "recipe", clock });
    recipe.createTables(client);
    const g = new SyncEngine(core, descriptor, recipe.storage(client), engineOptions);

    let main = g.createPrimary(cook, bread, "main");
    let first = g.branch(cook, main.id, "first");
    const everyKind: Edits = {
      cover: { upsert: [row({ entity_key: "Cover", photo_url: "https://example.com/bread.jpg" })] },
      ingredient: {
        upsert: [
          row({ entity_key: "Flour", step_key: "Knead", quantity: "500 g", substitutes: [{ name: "spelt", ratio: 1 }] }),
          row({ entity_key: "Salt", step_key: "Knead", quantity: "10 g", substitutes: null }),
        ],
      },
      note: {
        upsert: [row({ entity_key: "Note", body: "Proof overnight\tif you can" }), row({ entity_key: "Reply", body: "Agreed", reply_to: "Note" })],
      },
      step: {
        upsert: [
          row({ entity_key: "Knead", position: 1, instruction: "Knead for ten minutes", timings: { knead: "10m" }, scratch: "floury" }),
          row({ entity_key: "Bake", position: 2, instruction: "Bake at 230 C", timings: { bake: "35m", preheat: "30m" } }),
        ],
      },
      tasting: { upsert: [firstTasting, secondTasting] },
      utensil: { upsert: [row({ name: "Bowl" })] },
    };
    first = g.save(cook, first.id, first.version, everyKind).ref;
    first = g.commit(cook, first.id, first.version, { message: "first draft" }).ref;
    const m1 = merged(g.merge(cook, first.id, main.id, main.version, [], { message: "first", tag: true }));
    main = m1.ref;
    const release = g.release(cook, bread, m1.commit.id, 0);
    g.seal(cook, first.id, first.version);

    let second = g.branch(ann, main.id, "second");
    second = g.save(ann, second.id, second.version, {
      ingredient: { delete: ["Salt"] },
      step: {
        upsert: [
          row({ entity_key: "Knead", position: 1, instruction: "Knead for twelve minutes", timings: { knead: "12m", rest: "5m" }, scratch: "sticky" }),
          row({ entity_key: "Proof", position: 3, instruction: "Proof for an hour", timings: { proof: "1h" } }),
        ],
      },
    }).ref;
    second = g.commit(ann, second.id, second.version, { message: "second draft" }).ref;
    // A partial row keeps the columns it lacks; the unset removes the
    // change set's own Proof row, as Cook, which the DELETE image names.
    second = g.save(cook, second.id, second.version, {
      step: { upsert: [row({ entity_key: "Knead", instruction: "Knead until smooth" })], unset: ["Proof"] },
    }).ref;
    second = g.commit(cook, second.id, second.version).ref;
    const m2 = merged(g.merge(ann, second.id, main.id, main.version, [], { message: "second", tag: true }));
    main = m2.ref;
    g.release(ann, bread, m2.commit.id, release.version);
    // Work the change set does not commit: a partial row on insert stores
    // null for each column it lacks.
    g.save(ann, second.id, second.version, { tasting: { upsert: [row({ entity_key: "First", score: 5 })] } });

    let scrap = g.branch(cook, main.id, "scrap");
    scrap = g.save(cook, scrap.id, scrap.version, {
      cover: { delete: ["Cover"] },
      utensil: { upsert: [row({ entity_key: "Whisk", name: "Whisk" })] },
    }).ref;
    g.discard(cook, scrap.id, scrap.version);

    const menu = new SqliteAdapter(descriptor, { graph: "menu", clock });
    const h = new SyncEngine(core, descriptor, menu.storage(client), engineOptions);
    const lunch = h.createPrimary(cook, bread, "main");
    let today = h.branch(cook, lunch.id, "today");
    today = h.save(cook, today.id, today.version, {
      step: { upsert: [row({ entity_key: "Knead", position: 1, instruction: "Slice", timings: {} })] },
      utensil: { upsert: [row({ entity_key: "Knife", name: "Bread knife" })] },
    }).ref;
    today = h.commit(cook, today.id, today.version, { message: "today" }).ref;
    const served = merged(h.merge(cook, today.id, lunch.id, lunch.version, [], { message: "lunch", tag: true }));
    h.release(cook, bread, served.commit.id, 0);
  });
}

/** Quotes an identifier. */
function quote(name: string): string {
  return '"' + name.replace(/"/g, '""') + '"';
}

/** Whether a character splits a line for some reader: a control character, NEL, or a line or paragraph separator. */
const lineBreak = /[\u0000-\u001f\u007f\u0085\u2028\u2029]/;

/** A value as an SQL literal: text quoted with '' doubled, an integer as written, NULL. */
function literal(value: SqliteValue | undefined, where: string): string {
  if (value === null) {
    return "NULL";
  }
  if (typeof value === "string") {
    assert.doesNotMatch(value, lineBreak, `${where} holds no line break`);
    return "'" + value.replace(/'/g, "''") + "'";
  }
  if (typeof value === "bigint") {
    return value.toString();
  }
  if (typeof value === "number" && Number.isSafeInteger(value)) {
    return String(value);
  }
  throw new Error(`${where}: the layout holds text, integers and NULL only, not ${String(value)}`);
}

/**
 * The database as SQL text: the layout's statements, then one INSERT per row
 * of each table, the tables in the layout's order and each table's rows by
 * primary key (id, or history_id in a history table) in byte order. One
 * statement per line, each ending in a semicolon.
 */
export function dumpDatabase(client: SqliteClient): string {
  const lines = sqliteLayout().map((statement) => statement + ";");
  for (const local of sqliteTables) {
    const name = defaultTableName(local);
    const columns = client
      .all("SELECT name FROM pragma_table_info(?1) ORDER BY cid", [name])
      .map((r) => r["name"] as string);
    const key = local.endsWith("_history") ? "history_id" : "id";
    for (const r of client.all(`SELECT * FROM ${quote(name)} ORDER BY ${key}`)) {
      const values = columns.map((column) => literal(r[column], `${name}.${column}`));
      lines.push(`INSERT INTO ${quote(name)} (${columns.join(", ")}) VALUES (${values.join(", ")});`);
    }
  }
  return lines.join("\n") + "\n";
}

/**
 * Loads SQL text dumpDatabase wrote into an empty database: foreign keys off
 * while it loads, since a ref and its head commit name each other, then
 * every key checked.
 */
export function loadDatabase(client: SqliteClient, sql: string): void {
  client.exec!("PRAGMA foreign_keys = OFF");
  for (const statement of sql.split("\n")) {
    if (statement !== "") {
      client.run(statement);
    }
  }
  assert.deepEqual(client.all("PRAGMA foreign_key_check"), [], "every foreign key of the loaded file holds");
}

// What the reads file holds: JSON, with each row the adapter or the engine
// returns kept as its exact text.

/** JSON text written into the file as it is. */
class Raw {
  readonly text: string;
  constructor(text: string) {
    this.text = text;
  }
}

type Out = null | boolean | number | string | Raw | Out[] | { [name: string]: Out };

/** Writes a value with two spaces of indentation, each Raw on one line as it is. */
function pretty(value: Out, indent = ""): string {
  if (value instanceof Raw) {
    return value.text;
  }
  if (value === null || typeof value === "boolean") {
    return String(value);
  }
  if (typeof value === "number") {
    assert.ok(Number.isSafeInteger(value), `${value} is an integer`);
    return String(value);
  }
  if (typeof value === "string") {
    return writeJsonString(value);
  }
  const inner = indent + "  ";
  if (Array.isArray(value)) {
    return value.length === 0 ? "[]" : "[\n" + value.map((v) => inner + pretty(v, inner)).join(",\n") + "\n" + indent + "]";
  }
  const names = Object.keys(value);
  return names.length === 0
    ? "{}"
    : "{\n" + names.map((n) => inner + writeJsonString(n) + ": " + pretty(value[n]!, inner)).join(",\n") + "\n" + indent + "}";
}

const byCodePoint = (a: string, b: string): number => compareCodePoints(a, b);

function refOut(r: Ref): Out {
  return {
    id: r.id,
    root: r.root,
    parent: r.parent,
    base: r.base,
    head: r.head,
    name: r.name,
    sealed: r.sealed,
    discarded: r.discarded,
    version: r.version,
  };
}

function commitOut(c: Commit): Out {
  return {
    id: c.id,
    root: c.root,
    ref: c.ref,
    parent: c.parent,
    message: c.message,
    schemaEpoch: c.schemaEpoch,
    contentHash: c.contentHash,
    sequence: c.sequence,
    createdAt: c.createdAt,
    createdBy: c.createdBy,
    snapshot: c.snapshot,
  };
}

function releaseOut(r: Release): Out {
  return { id: r.id, root: r.root, commit: r.commit, version: r.version };
}

function findingOut(f: Finding): Out {
  const out: { [name: string]: Out } = { code: f.code, kind: f.kind };
  if (f.entityKey !== undefined) {
    out["entityKey"] = f.entityKey;
  }
  out["message"] = f.message;
  return out;
}

/** A tree: its kinds by name, each kind's rows in the order the engine gives them. */
function treeOut(tree: Tree): Out {
  const out: { [name: string]: Out } = {};
  for (const kind of Object.keys(tree).sort(byCodePoint)) {
    out[kind] = tree[kind]!.map((r) => new Raw(r));
  }
  return out;
}

function treeResultOut(t: TreeResult): { [name: string]: Out } {
  return { tree: treeOut(t.tree), contentHash: t.contentHash, findings: t.findings.map(findingOut) };
}

/** An engine read's value, or {"error": code} for an engine error. */
function attempt(fn: () => Out): Out {
  try {
    return fn();
  } catch (err) {
    const code = errorCode(err);
    if (code === "") {
      throw err;
    }
    return { error: code };
  }
}

/** A row's member, which must be a string: its entity key. */
function keyOf(text: string, column: string): string {
  const parsed = parseJson(text);
  assert.ok(isJsonObject(parsed), "a row is a JSON object");
  const key = parsed.get(column);
  assert.equal(typeof key, "string", `a row's ${column} is a string`);
  return key as string;
}

/** Ids of a table's rows in a graph, in code point order. */
function idsOf(client: SqliteClient, local: string, graph: string, column = "id"): string[] {
  return [
    ...new Set(
      client.all(`SELECT ${column} AS id FROM ${quote(defaultTableName(local))} WHERE graph = ?1`, [graph]).map((r) => r["id"] as string),
    ),
  ].sort(byCodePoint);
}

/** One graph's reads, through an adapter opened with its name. */
function readGraph(client: SqliteClient, graph: string): Out {
  const storage = new SqliteAdapter(descriptor, { graph }).storage(client);
  const engine = new SyncEngine(core, descriptor, storage, engineOptions);
  const read = <T>(fn: (tx: SyncTx) => T): T => storage.transact(fn);

  const refs = idsOf(client, "ref", graph).map((id): Out => {
    const ref = read((tx) => tx.readRef(id));
    const rows: { [name: string]: Out } = {};
    for (const k of kinds) {
      rows[k.name] = read((tx) => tx.rows(k.name, id))
        .map((r) => ({ key: keyOf(r, k.key), row: r }))
        .sort((a, b) => compareCodePoints(a.key, b.key))
        .map((r) => new Raw(r.row));
    }
    return {
      id,
      readRef: refOut(ref),
      rows,
      compose: attempt(() => treeResultOut(engine.compose(id))),
      history: attempt(() => engine.history(id).map(commitOut)),
    };
  });

  const byEntity = <T extends { kind: string; entityKey: string }>(a: T, b: T): number =>
    compareCodePoints(a.kind, b.kind) || compareCodePoints(a.entityKey, b.entityKey);
  const commits = idsOf(client, "commit", graph).map((id): Out => ({
    id,
    readCommit: commitOut(read((tx) => tx.readCommit(id))),
    materialize: attempt(() => treeResultOut(engine.materialize(id))),
    patches: read((tx) => tx.patches([id]))
      .sort(byEntity)
      .map((p) => ({
        commit: p.commit,
        kind: p.kind,
        entityKey: p.entityKey,
        entityId: p.entityId,
        entityVersion: p.entityVersion,
        operation: p.operation,
      })),
    snapshot: read((tx) => tx.snapshot(id))
      .sort(byEntity)
      .map((e) => ({ kind: e.kind, entityKey: e.entityKey, entityId: e.entityId, entityVersion: e.entityVersion })),
  }));

  const roots = idsOf(client, "ref", graph, "root_id").map((root): Out => ({
    root,
    released: attempt(() => {
      const r = engine.released(root);
      return { release: releaseOut(r.release), ...treeResultOut(r) };
    }),
  }));

  const pins = client
    .all(`SELECT kind, id, _version FROM ${quote(defaultTableName("member_history"))} WHERE graph = ?1`, [graph])
    .map((r) => ({ kind: r["kind"] as string, id: r["id"] as string, version: Number(r["_version"]) }))
    .sort((a, b) => compareCodePoints(a.kind, b.kind) || compareCodePoints(a.id, b.id) || a.version - b.version);
  const images = pins.map((pin): Out => {
    const found = read((tx) => tx.images(pin.kind, [{ id: pin.id, version: pin.version }]));
    assert.equal(found.length, 1, `history holds ${pin.kind} ${pin.id} at ${pin.version}`);
    return { kind: pin.kind, id: pin.id, version: pin.version, image: new Raw(found[0]!) };
  });

  return { graph, refs, commits, roots, images };
}

/** What a database reads back as, through an adapter opened with each graph's name, as typescript.json holds it. */
export function readVectors(client: SqliteClient): string {
  const graphs = [...new Set(client.all(`SELECT graph FROM ${quote(defaultTableName("ref"))}`).map((r) => r["graph"] as string))].sort(
    byCodePoint,
  );
  return pretty({ graphs: graphs.map((graph) => readGraph(client, graph)) }) + "\n";
}

/** layout.json's text: sqliteLayout() under the default names. */
export function layoutText(): string {
  return pretty({ statements: sqliteLayout() }) + "\n";
}

/** Rewrites the vectors: the layout, and the script's database and its reads, through clients open opens. */
export function writeVectors(open: () => { client: SqliteClient; close(): void }): void {
  writeFileSync(layoutFile, layoutText());
  const written = open();
  let sql: string;
  try {
    writeDatabase(written.client);
    sql = dumpDatabase(written.client);
  } finally {
    written.close();
  }
  const loaded = open();
  try {
    loadDatabase(loaded.client, sql);
    writeFileSync(sqlFile, sql);
    writeFileSync(readsFile, readVectors(loaded.client));
  } finally {
    loaded.close();
  }
}
