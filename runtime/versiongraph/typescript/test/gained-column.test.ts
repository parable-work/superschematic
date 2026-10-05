// A kind that gains a content column, end to end on each backend: commits
// written with the kind's original columns, then the column added (on
// Postgres, ALTER TABLE ... ADD COLUMN and a descriptor that declares it;
// on SQLite, the descriptor alone). The rows written before the gain read
// the column as null, and their history images lack it on both backends.
// The core reads a content column a row lacks as null, so the gain moves no
// comparison, patch or merge: a ref and its head commit hash the same, a
// save of a row as its base has it is nothing to commit, a side that never
// touched the column merges with one that sets it, a rebase of a draft
// branched before the gain moves it onto work written after it, an edit
// against a delete settled by taking the edit of a row written before the
// gain clears the column on the target, a revert to a commit written before
// the gain takes a value off the column, and both backends give the same
// trees and hashes.
import { expect, test } from "bun:test";
import { Database as BunDatabase } from "bun:sqlite";
import {
  Engine,
  errorCode,
  SyncEngine,
  uuidHyphenated,
  type CommitOptions,
  type CommitResult,
  type Edits,
  type MergeResult,
  type Ref,
  type Resolution,
  type SaveResult,
  type Tree,
  type TreeResult,
} from "../dist/engine.js";
import { init, initSync } from "../dist/index.js";
import { PostgresAdapter, pgPool } from "../dist/postgres.js";
import { SqliteAdapter, bunSqlite } from "../dist/sqlite.js";
import { descriptor, dsn, scratchSchema } from "./postgres.js";

type Awaitable<T> = T | Promise<T>;

/** The operations the test runs, as Engine (awaited) and SyncEngine (returned) both have them. */
interface Graph {
  createPrimary(actor: string, root: string, name: string): Awaitable<Ref>;
  branch(actor: string, fromRef: string, name: string): Awaitable<Ref>;
  save(actor: string, ref: string, version: number, edits: Edits): Awaitable<SaveResult>;
  commit(actor: string, ref: string, version: number, options?: CommitOptions): Awaitable<CommitResult>;
  merge(
    actor: string,
    source: string,
    target: string,
    targetVersion: number,
    resolutions?: readonly Resolution[],
    options?: CommitOptions,
  ): Awaitable<MergeResult>;
  rebase(actor: string, draft: string, version: number, resolutions?: readonly Resolution[]): Awaitable<MergeResult>;
  revert(actor: string, ref: string, version: number, toCommit: string): Awaitable<CommitResult>;
  compose(ref: string): Awaitable<TreeResult>;
  materialize(commit: string): Awaitable<TreeResult>;
}

/** A backend holding the fixture graph: its engine before the gain, and the gain itself. */
interface Backend {
  before: Graph;
  /** Adds utensil's color and returns an engine over the descriptor that declares it. */
  gain(): Promise<Graph>;
  close(): Promise<void>;
}

const cook = "Cook";
const bread = "Bread";
const options = { schemaEpoch: 1, snapshotEvery: 3 };

/** The fixture's descriptor with utensil's gained content column, color. */
const gained = (() => {
  const d = JSON.parse(descriptor) as { kinds: { kind: string; columns: Record<string, string> }[] };
  d.kinds.find((k) => k.kind === "utensil")!.columns["color"] = "string";
  return JSON.stringify(d);
})();

const core = initSync();

async function postgres(): Promise<Backend> {
  const scratch = await scratchSchema("vg_gain");
  await scratch.pool.query("INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $2::uuid)", [
    uuidHyphenated(bread),
    uuidHyphenated(cook),
  ]);
  const wasm = await init();
  const engine = (d: string) => new Engine(wasm, d, new PostgresAdapter(d).storage(pgPool(scratch.pool)), options);
  return {
    before: engine(descriptor),
    async gain() {
      await scratch.pool.query("ALTER TABLE utensil ADD COLUMN color TEXT");
      return engine(gained);
    },
    close: () => scratch.close(),
  };
}

async function sqlite(): Promise<Backend> {
  const db = new BunDatabase(":memory:");
  const client = bunSqlite(db);
  new SqliteAdapter(descriptor, { graph: "recipe" }).createTables(client);
  const engine = (d: string) => new SyncEngine(core, d, new SqliteAdapter(d, { graph: "recipe" }).storage(client), options);
  return {
    before: engine(descriptor),
    gain: async () => engine(gained),
    close: async () => db.close(),
  };
}

/** A utensil row as an upsert's JSON text. */
function utensil(key: string, name: string, extra: Record<string, unknown> = {}): string {
  return JSON.stringify({ entity_key: key, name, ...extra });
}

/** The code of the error fn throws or rejects with, or "ok". */
async function outcome(fn: () => Awaitable<unknown>): Promise<string> {
  try {
    await fn();
    return "ok";
  } catch (err) {
    return errorCode(err);
  }
}

/** A tree's rows as objects, for the core's typed contentHash. */
function rows(tree: Tree): Record<string, Record<string, unknown>[]> {
  return Object.fromEntries(Object.entries(tree).map(([kind, list]) => [kind, list.map((row) => JSON.parse(row) as Record<string, unknown>)]));
}

/** What a run observed, with each generated id replaced by a label in order of first appearance. */
type Observed = Record<string, unknown>;

/** Labels the ids a backend generated, so two backends' trees compare. */
function normalize(value: unknown, labels = new Map<string, string>()): unknown {
  if (Array.isArray(value)) {
    return value.map((item) => normalize(item, labels));
  }
  if (value !== null && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value).sort()) {
      const member = (value as Record<string, unknown>)[key];
      if ((key === "id" || key === "ref_id") && typeof member === "string") {
        if (!labels.has(member)) {
          labels.set(member, `#${labels.size}`);
        }
        out[key] = labels.get(member);
      } else {
        out[key] = normalize(member, labels);
      }
    }
    return out;
  }
  return value;
}

/** A tree read through an engine, as plain objects. */
function shown(result: TreeResult): Observed {
  return { tree: rows(result.tree), contentHash: result.contentHash };
}

/** Runs the gain on a backend, checking each rule, and returns what it observed. */
async function run(open: () => Promise<Backend>): Promise<Observed> {
  const backend = await open();
  try {
    const before = backend.before;
    let main = await before.createPrimary(cook, bread, "main");
    let draft = await before.branch(cook, main.id, "draft");
    draft = (await before.save(cook, draft.id, draft.version, { utensil: { upsert: [utensil("Whisk", "whisk"), utensil("Spoon", "spoon")] } })).ref;
    draft = (await before.commit(cook, draft.id, draft.version)).ref;
    const first = await before.merge(cook, draft.id, main.id, main.version, [], { tag: true });
    main = first.ref;
    const recorded = first.commit!;
    // Before the gain: a draft that a rebase moves later, a source that
    // renames the whisk, and a target beside it that deletes the whisk.
    let late = await before.branch(cook, main.id, "late");
    let source = await before.branch(cook, main.id, "source");
    source = (await before.save(cook, source.id, source.version, { utensil: { upsert: [utensil("Whisk", "whisk2")] } })).ref;
    source = (await before.commit(cook, source.id, source.version)).ref;
    let target = await before.branch(cook, main.id, "target");

    const graph = await backend.gain();
    const composed = await graph.compose(main.id);
    const materialized = await graph.materialize(main.head!);
    // Main's live rows hold color null; its head commit's images lack it.
    const live = rows(composed.tree)["utensil"]!;
    const images = rows(materialized.tree)["utensil"]!;
    expect(live.map((row) => row["color"])).toEqual([null, null]);
    expect(images.map((row) => "color" in row)).toEqual([false, false]);
    // A ref and its head commit hash the same.
    expect(composed.contentHash).toBe(materialized.contentHash);
    // The commit's recorded hash still equals its materialized tree's, under
    // the descriptor it was recorded with, since its images are as stored.
    // Under the gained descriptor color is content, null in each row, so the
    // same tree hashes as every tree written after the gain does.
    expect(core.contentHash({ descriptor: JSON.parse(descriptor), tree: rows(materialized.tree) }).contentHash).toBe(recorded.contentHash);

    // A save of a row as its base holds it is nothing to commit.
    let same = await graph.branch(cook, main.id, "same");
    same = (await graph.save(cook, same.id, same.version, { utensil: { upsert: [utensil("Whisk", "whisk")] } })).ref;
    expect(await outcome(() => graph.commit(cook, same.id, same.version))).toBe("nothing_to_commit");

    // rename never touches color; paint sets it on the whisk.
    let rename = await graph.branch(cook, main.id, "rename");
    rename = (await graph.save(cook, rename.id, rename.version, {
      utensil: { upsert: [utensil("Whisk", "big whisk"), utensil("Spoon", "big spoon")] },
    })).ref;
    rename = (await graph.commit(cook, rename.id, rename.version)).ref;
    let paint = await graph.branch(cook, main.id, "paint");
    paint = (await graph.save(cook, paint.id, paint.version, { utensil: { upsert: [utensil("Whisk", "whisk", { color: "red" })] } })).ref;
    paint = (await graph.commit(cook, paint.id, paint.version)).ref;
    const renamed = await graph.merge(cook, rename.id, main.id, main.version);
    expect(renamed.conflicts).toEqual([]);
    main = renamed.ref;
    // Paint's base lacks color, and main reads it as null: no conflict.
    const painted = await graph.merge(cook, paint.id, main.id, main.version);
    expect(painted.conflicts).toEqual([]);
    main = painted.ref;

    const head = await graph.materialize(main.head!);
    const after = await graph.compose(main.id);
    const whisk = rows(after.tree)["utensil"]!.find((row) => row["entity_key"] === "Whisk")!;
    expect([whisk["name"], whisk["color"]]).toEqual(["big whisk", "red"]);
    expect(after.contentHash).toBe(head.contentHash);
    // A commit written after the gain records its tree's hash.
    expect(painted.commit!.contentHash).toBe(head.contentHash);
    expect(renamed.commit!.contentHash).toBe((await graph.materialize(renamed.commit!.id)).contentHash);

    // A revert to the commit written before the gain: its images lack
    // color, which is null, so the spoon the draft painted loses its color
    // and the draft composes to that commit's tree.
    let undo = await graph.branch(cook, main.id, "undo");
    undo = (await graph.save(cook, undo.id, undo.version, { utensil: { upsert: [utensil("Spoon", "spoon", { color: "blue" })] } })).ref;
    const reverted = await graph.revert(cook, undo.id, undo.version, recorded.id);
    const undone = await graph.compose(undo.id);
    expect(rows(undone.tree)["utensil"]!.map((row) => [row["name"], row["color"]])).toEqual([
      ["spoon", null],
      ["whisk", null],
    ]);
    expect(undone.contentHash).toBe(materialized.contentHash);
    expect(reverted.commit!.contentHash).toBe(materialized.contentHash);

    // An edit against a delete, settled by taking the edit: the target
    // painted the whisk and deleted it, and the source's whisk is an image
    // from before the gain, which lacks color. The target's whisk is the
    // source's, color null, not the color its tombstone row held.
    target = (await graph.save(cook, target.id, target.version, { utensil: { upsert: [utensil("Whisk", "whisk", { color: "red" })] } })).ref;
    target = (await graph.save(cook, target.id, target.version, { utensil: { delete: ["Whisk"] } })).ref;
    const settled = await graph.merge(cook, source.id, target.id, target.version, [
      { kind: "utensil", entityKey: "Whisk", path: "", take: "theirs" },
    ]);
    expect(settled.conflicts).toEqual([]);
    const taken = await graph.compose(target.id);
    const takenWhisk = rows(taken.tree)["utensil"]!.find((row) => row["entity_key"] === "Whisk")!;
    expect([takenWhisk["name"], takenWhisk["color"]]).toEqual(["whisk2", null]);
    expect(settled.commit!.contentHash).toBe(taken.contentHash);
    expect((await graph.materialize(settled.commit!.id)).contentHash).toBe(taken.contentHash);

    // A rebase of the draft branched before the gain onto main's head: the
    // draft paints the spoon, main renamed it after the gain, and the base
    // lacks color, which main's rows hold null, so the two merge.
    late = (await graph.save(cook, late.id, late.version, { utensil: { upsert: [utensil("Spoon", "spoon", { color: "blue" })] } })).ref;
    const rebased = await graph.rebase(cook, late.id, late.version);
    expect(rebased.conflicts).toEqual([]);
    late = rebased.ref;
    const moved = await graph.compose(late.id);
    expect(rows(moved.tree)["utensil"]!.map((row) => [row["name"], row["color"]])).toEqual([
      ["big spoon", "blue"],
      ["big whisk", "red"],
    ]);
    expect(rebased.commit!.contentHash).toBe(moved.contentHash);
    expect((await graph.materialize(late.head!)).contentHash).toBe(moved.contentHash);
    expect(await outcome(() => graph.commit(cook, late.id, late.version))).toBe("nothing_to_commit");
    const landed = await graph.merge(cook, late.id, main.id, main.version);
    expect(landed.conflicts).toEqual([]);
    main = landed.ref;
    const final = await graph.compose(main.id);
    expect(final.contentHash).toBe((await graph.materialize(main.head!)).contentHash);
    expect(final.contentHash).toBe(moved.contentHash);

    return normalize({
      recorded: recorded.contentHash,
      composed: shown(composed),
      materialized: shown(materialized),
      renamed: shown(await graph.materialize(renamed.commit!.id)),
      painted: painted.commit!.contentHash,
      head: shown(head),
      after: shown(after),
      undone: shown(undone),
      taken: shown(taken),
      moved: shown(moved),
      final: shown(final),
    }) as Observed;
  } finally {
    await backend.close();
  }
}

const observed = new Map<string, Promise<Observed>>();

/** Each backend's run, once. */
function observe(name: "postgres" | "sqlite"): Promise<Observed> {
  let run_ = observed.get(name);
  if (run_ === undefined) {
    run_ = run(name === "postgres" ? postgres : sqlite);
    observed.set(name, run_);
  }
  return run_;
}

test("sqlite: after a kind gains a column, rows written before it compare, hash and merge as rows that hold it null", async () => {
  await observe("sqlite");
});

test.skipIf(dsn === "")("postgres: after a kind gains a column, rows written before it compare, hash and merge as rows that hold it null", async () => {
  await observe("postgres");
});

test.skipIf(dsn === "")("after a kind gains a column, Postgres and SQLite give the same trees and hashes", async () => {
  expect(await observe("sqlite")).toEqual(await observe("postgres"));
});
