package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestVersionGraphFacadeOnPostgres generates fixture-version-graph-db's
// TypeScript package, whose versiongraph/recipe.ts is the Recipe graph's
// typed facade over the TypeScript engine, type-checks it (tsc, through
// buildTSPackages), and runs versionGraphFacadeTest in it with bun against
// the Postgres at SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL: a value of
// every class through the facade and back, and the operations D19 adds
// (a merge's message and tag, a release and a rollback, released, a rebase
// with a conflict and without, and a sweep) through its typed methods. It
// is the TypeScript counterpart of the ORM generator's
// TestVersionGraphShellOnPostgres.
func TestVersionGraphFacadeOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the TypeScript version graph facade against Postgres")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-version-graph-db"))
	if err != nil {
		t.Fatalf("load fixture-version-graph-db: %v", err)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{name: "fixture-version-graph-db", schema: schema}})
	dir := filepath.Join(typesRoot, "fixture-version-graph-db")

	paths := testpaths.Local(t)
	root := testpaths.RepoRoot(t)
	source := strings.NewReplacer(
		"PG_MODULE", filepath.Join(paths.VersionGraphTypeScript, "node_modules", "pg", "lib", "index.js"),
		"CREATE_SQL", filepath.Join(root, "runtime", "versiongraph", "testdata", "fixture", "create.sql"),
	).Replace(versionGraphFacadeTest)
	if err := os.WriteFile(filepath.Join(dir, "graph_facade.test.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	report := filepath.Join(dir, "graph_facade.junit.xml")
	run := exec.Command(bunPath, "test", "./graph_facade.test.ts", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the facade test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	// Each test ran and passed: a testcase with no failure and no skip.
	for _, name := range []string{"the facade keeps every class", "the facade merges, releases, rebases and sweeps"} {
		at := strings.Index(string(junit), `<testcase name="`+name+`"`)
		if at < 0 {
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		}
		testcase := string(junit[at:])
		if end := strings.Index(testcase, "</testcase>"); end >= 0 {
			testcase = testcase[:end]
		} else if end := strings.Index(testcase, "/>"); end >= 0 {
			testcase = testcase[:end]
		}
		if strings.Contains(testcase, "<failure") || strings.Contains(testcase, "<skipped") {
			t.Fatalf("%q did not pass:\n%s", name, testcase)
		}
	}
	t.Logf("bun ran the facade tests:\n%s", out)
}

// versionGraphFacadeTest drives the generated RecipeGraph with bun. PG_MODULE
// and CREATE_SQL are replaced with the pg driver the version-graph package
// installs and the fixture's DDL.
const versionGraphFacadeTest = `import { afterAll, beforeAll, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import pg from "PG_MODULE";
import { pgPool } from "@superschematic/versiongraph/postgres";
import { NotTaggedError, NoParentError } from "@superschematic/versiongraph/facade";
import { RecipeGraph, RecipeGraphDescriptor, RecipeGraphSnapshotEvery } from "./versiongraph";
import { RecipeEntityKind, RecipePatchOperation, Verdict, type Step, type Tasting } from "./types";

const dsn = process.env.SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL!;
const schema = "vg_facade_ts_" + process.pid + "_" + Date.now();
const actor = "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90";
const admin = new pg.Client({ connectionString: dsn });
let pool: pg.Pool;
let roots = 0;

beforeAll(async () => {
  await admin.connect();
  try {
    await admin.query("CREATE EXTENSION IF NOT EXISTS pgcrypto SCHEMA public");
  } catch (err) {
    if ((err as { code?: string }).code !== "23505") throw err;
  }
  await admin.query("CREATE SCHEMA " + schema);
  pool = new pg.Pool({ connectionString: dsn, options: "-c search_path=" + schema + ",public" });
  await pool.query(readFileSync("CREATE_SQL", "utf8"));
});

afterAll(async () => {
  await pool.end();
  await admin.query("DROP SCHEMA " + schema + " CASCADE");
  await admin.end();
});

// A new recipe root, as hyphenated text and in its canonical form.
async function recipe(): Promise<string> {
  const id = "00000000-0000-4000-8000-" + String(++roots).padStart(12, "0");
  await pool.query("INSERT INTO recipe (id, title, created_by) VALUES ($1::uuid, 'Bread', $2::uuid)", [id, actor]);
  return id;
}

// Saves a value of every class a descriptor names through the facade,
// commits it and reads it back from the save and from the commit. Each
// field comes back as the typed value of its canonical form: the same
// value, an instant in UTC, a time of day as HH:MM:SS, a duration in the
// scalar core's form and a UUID in base62.
test("the facade keeps every class", async () => {
  const g = new RecipeGraph(pgPool(pool), { actor });
  const root = await recipe();
  const main = await g.createPrimary(root, "main");
  const draft = await g.branch(main.id, "tastings");
  const input = {
    taster: "0e7d4b1a-3c2f-4a6e-8b9d-1f2e3d4c5b6a",
    salty: true,
    score: 4.5,
    servings: 9007199254740991,
    tastedOn: "2026-09-01",
    tastedAt: new Date("2026-09-01T12:30:00.25+02:00"),
    servedAt: "18:30",
    rested: "1.5ms",
    verdict: Verdict.Tweak,
    remarks: { crumb: "open", crust: [1, 2.5] },
    tags: ["sour", 'a "quoted" tag'],
    helpers: [actor],
    bites: [[1, 2], [3]],
  } as Tasting;
  const want: Record<string, unknown> = {
    ...input,
    taster: "RL9PXyoT57JGFsG8bqhP0",
    tastedAt: new Date("2026-09-01T10:30:00.25Z"),
    servedAt: "18:30:00",
    rested: "1500us",
    helpers: ["2tLrGjz6ktIRCukXDsqykS"],
  };
  const saved = await g.save(draft.id, draft.version, { tasting: { upsert: [input] } });
  const committed = await g.commit(draft.id, saved.ref.version);
  expect(committed.commit).not.toBeNull();
  const tree = await g.materialize(committed.commit!.id);
  expect(tree.contentHash).toBe(committed.commit!.contentHash);
  expect([saved.saved.tasting.length, tree.tasting.length]).toEqual([1, 1]);
  for (const [what, got] of [["saved", saved.saved.tasting[0]!], ["materialized", tree.tasting[0]!]] as const) {
    for (const field of Object.keys(want)) {
      expect({ what, field, value: (got as unknown as Record<string, unknown>)[field] }).toEqual({ what, field, value: want[field] });
    }
    expect(got.tastedAt).toBeInstanceOf(Date);
    expect(got.recipe.id).toBe(base62(root));
    expect(typeof got.entityKey).toBe("string");
    expect(got._version).toBe(1);
  }
});

// The operations D19 adds, through the typed facade: a merge's message and
// tag, the release pointer and a rollback that writes no member rows,
// released, a conflicting and a clean rebase with typed conflicts, a diff
// with typed kinds and operations, history, and a sweep that writes as its
// own actor.
test("the facade merges, releases, rebases and sweeps", async () => {
  const g = new RecipeGraph(pgPool(pool)).withActor(actor);
  const root = await recipe();
  const main = await g.createPrimary(root, "main");
  const step = (instruction: string, entityKey?: string) =>
    ({ entityKey, position: 1, instruction, timings: { knead: 10 } }) as unknown as Step;

  const first = await g.branch(main.id, "first");
  const saved = await g.save(first.id, first.version, { step: { upsert: [step("Mix")] } });
  const key = saved.saved.step[0]!.entityKey!;
  await g.commit(first.id, saved.ref.version);
  const v1 = await g.merge(first.id, main.id, main.version, [], { message: "first", tag: true });
  expect(v1.commit?.message).toBe("first");
  expect(v1.commit?.sequence).toBe(1);
  await expect(g.save(main.id, v1.ref.version, {})).rejects.toMatchObject({ code: "primary_merge_only" });

  const second = await g.branch(main.id, "second");
  const edited = await g.save(second.id, second.version, { step: { upsert: [step("Mix well", key)] } });
  await g.commit(second.id, edited.ref.version);
  const v2 = await g.merge(second.id, main.id, v1.ref.version, [], { tag: true });
  const untagged = await g.branch(main.id, "untagged");
  const u = await g.save(untagged.id, untagged.version, { step: { upsert: [step("Mix gently", key)] } });
  const uCommit = await g.commit(untagged.id, u.ref.version);
  await expect(g.release(root, uCommit.commit!.id, 0)).rejects.toBeInstanceOf(NotTaggedError);

  const release = await g.release(root, v2.commit!.id, 0);
  expect((await g.released(root)).tree.step.map((s) => s.instruction)).toEqual(["Mix well"]);
  const rolledBack = await g.release(root, v1.commit!.id, release.version);
  const released = await g.released(root);
  expect(released.release.commit).toBe(v1.commit!.id);
  expect(released.release.version).toBe(rolledBack.version);
  expect(released.tree.step.map((s) => s.instruction)).toEqual(["Mix"]);
  expect((await g.compose(main.id)).step.map((s) => s.instruction)).toEqual(["Mix well"]);

  const changes = await g.diff(v1.commit!.id, v2.commit!.id);
  expect(changes.map((c) => [c.kind, c.entityKey, c.operation])).toEqual([[RecipeEntityKind.Step, key, RecipePatchOperation.Update]]);
  expect(JSON.parse(changes[0]!.row!).instruction).toBe("Mix well");
  expect((await g.history(main.id)).map((c) => c.id)).toEqual([v2.commit!.id, v1.commit!.id]);

  // untagged branched from v2; main moves on, and untagged rebases onto it.
  const third = await g.branch(main.id, "third");
  const t = await g.save(third.id, third.version, { step: { upsert: [step("Mix hard", key)] } });
  await g.commit(third.id, t.ref.version);
  const mainNow = await g.merge(third.id, main.id, v2.ref.version);
  expect(mainNow.commit).not.toBeNull();
  await expect(g.rebase(main.id, mainNow.ref.version)).rejects.toBeInstanceOf(NoParentError);
  const conflicted = await g.rebase(untagged.id, uCommit.ref.version);
  expect(conflicted.commit).toBeNull();
  expect(conflicted.conflicts.map((c) => [c.kind, c.entityKey, c.path, c.ours, c.theirs])).toEqual([
    [RecipeEntityKind.Step, key, "/instruction", '"Mix gently"', '"Mix hard"'],
  ]);
  const rebased = await g.rebase(untagged.id, uCommit.ref.version, [
    { kind: RecipeEntityKind.Step, entityKey: key, path: "/instruction", take: "ours" },
  ]);
  expect(rebased.conflicts).toEqual([]);
  expect(rebased.ref.base).toBe(mainNow.commit!.id);
  expect((await g.compose(untagged.id)).step.map((s) => s.instruction)).toEqual(["Mix gently"]);

  // A sweep writes as its own actor: the rows it collects from a
  // discarded change set record it, and so does a change set it abandons.
  const janitor = "7a1d2c3b-4e5f-4a6b-8c7d-9e0f1a2b3c4d";
  await expect(g.sweep({ actor: "" })).rejects.toMatchObject({ code: "no_actor" });
  const dropped = await g.branch(main.id, "dropped");
  const garnish = await g.save(dropped.id, dropped.version, { step: { upsert: [step("Garnish")] } });
  await g.discard(dropped.id, garnish.ref.version);
  await pool.query("UPDATE recipe_ref SET deleted_at = now() - interval '8 days' WHERE id = $1::uuid", [hyphenated(dropped.id)]);
  const report = await g.sweep({ actor: janitor });
  expect([report.skipped, report.collectedRefs, report.collectedRows.step]).toEqual([false, 1, 1]);
  const last = await pool.query(
    "SELECT operation, data->>'updated_by' AS actor FROM step_history WHERE id = $1::uuid ORDER BY _version DESC LIMIT 1",
    [hyphenated(garnish.saved.step[0]!.id!)],
  );
  expect([last.rows[0].operation, last.rows[0].actor]).toEqual(["DELETE", janitor]);
  const idle = await g.branch(main.id, "idle");
  await pool.query("UPDATE recipe_ref SET updated_at = now() - interval '3 days' WHERE id = $1::uuid", [hyphenated(idle.id)]);
  const abandoned = await g.sweep({ actor: janitor, abandonAfter: 48 * 60 * 60 * 1000 });
  const discarder = await pool.query("SELECT deleted_by::text AS actor FROM recipe_ref WHERE id = $1::uuid", [hyphenated(idle.id)]);
  expect([abandoned.abandoned, discarder.rows[0].actor]).toEqual([1, janitor]);

  // The sweeper runs a pass at once and stops with its signal.
  const controller = new AbortController();
  const passes: unknown[] = [];
  const stopped = g.runSweeper(3_600_000, { actor: janitor }, (report, err) => {
    passes.push(err ?? report?.skipped);
    controller.abort(new Error("stop"));
  }, controller.signal);
  await expect(stopped).rejects.toThrow("stop");
  expect(passes).toEqual([false]);
  expect(RecipeGraphSnapshotEvery).toBe(3);
  expect(RecipeGraphDescriptor.graph).toBe("recipe");
});

// A UUID given hyphenated in its canonical form, base62.
function base62(id: string): string {
  const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let n = BigInt("0x" + id.replace(/-/g, ""));
  let out = "";
  while (n > 0n) {
    out = alphabet[Number(n % 62n)] + out;
    n /= 62n;
  }
  return out || "0";
}

function hyphenated(id: string): string {
  const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";
  let n = 0n;
  for (const c of id) n = n * 62n + BigInt(alphabet.indexOf(c));
  const hex = n.toString(16).padStart(32, "0");
  return hex.slice(0, 8) + "-" + hex.slice(8, 12) + "-" + hex.slice(12, 16) + "-" + hex.slice(16, 20) + "-" + hex.slice(20);
}
`
