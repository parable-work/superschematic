package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// stepsFixture is the DB service of D104's step 4's first wave: a Batch
// that implements Rollups over the steps that point at it, the rows its
// steps HasMany lists, and a Step that implements Dependencies, each beside
// its status Workflow.
const stepsFixture = "fixture-steps-db"

// stepsCheck is the file tsc checks the fixture's repositories against:
// Dependencies' operations under dependencies, each taking the row's key,
// a blocker's schema the table's own name, and listDependents left out;
// Rollups, which declares no operation, adds no member; a behavior's
// sub-table has no repository; and a table whose behaviors keep a sub-table
// has no upserts.
const stepsCheck = `import type { FixtureStepsDbOrm, StepRow } from './orm';

export async function block(orm: FixtureStepsDbOrm, id: StepRow['id'], by: StepRow['id']): Promise<boolean> {
  const added = await orm.step.dependencies.addBlocker(id, { id: by });
  const schema: 'Step' = added.schema;
  void schema;
  const page = await orm.step.dependencies.listBlockers(id, { limit: 10 });
  await orm.step.dependencies.removeBlocker(id, { id: by, schema: 'Step' });
  return added.open && page.next === null;
}

// @ts-expect-error listDependents is the engine's alone: a table leaves it out.
export const dependents = (orm: FixtureStepsDbOrm, id: StepRow['id']) => orm.step.dependencies.listDependents(id, {});
// @ts-expect-error a blocker names a table the config lists: Step alone.
export const stray = (orm: FixtureStepsDbOrm, id: StepRow['id']) => orm.step.dependencies.addBlocker(id, { id, schema: 'Batch' });
// @ts-expect-error Rollups declares no operation, so it has no member.
export const rollups = (orm: FixtureStepsDbOrm) => orm.batch.rollups;
// @ts-expect-error A table whose behaviors keep a sub-table has no upserts.
export const upsert = (orm: FixtureStepsDbOrm) => orm.step.upsertOne;
`

// stepsCases are the cases steps.test.ts runs on each dialect, by name
// without the dialect's suffix.
var stepsCases = []string{
	"addBlocker records the edge in the sub-table, and the gate holds the step's start until its blocker is done",
	"an edge that would close a cycle is refused",
	"listBlockers pages the edges in the order they were added, and removeBlocker takes one",
	"a batch's done waits for every step that points at it, not_held with the counts",
}

// TestStepsTablesEndToEnd generates fixture-steps-db's package, type-checks
// it against stepsCheck, applies its DDL and runs steps.test.ts on
// bun:sqlite, through the adapter over the layout orm.ts embeds, and on
// Postgres, through the generated repository (D104's step 4): Rollups and
// Dependencies with no code of their own in the generator.
func TestStepsTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, stepsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", stepsFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: stepsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", stepsFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, stepsFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "steps.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     stepsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"steps.check.ts": stepsCheck},
	}})
	dir := filepath.Join(typesRoot, stepsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "steps.junit.xml")
	run := exec.Command(bunPath, "test", "./steps.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = dir
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	postgres := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != ""
	if !postgres {
		delete(cases, "(unnamed)")
	}
	for _, dialect := range []string{"bun:sqlite", "Postgres"} {
		for _, name := range stepsCases {
			name += ", on " + dialect
			tc, ok := cases[name]
			switch {
			case !ok:
				t.Fatalf("bun did not run %q:\n%s", name, junit)
			case tc.Failure != nil:
				t.Fatalf("%q failed:\n%s", name, junit)
			case tc.Skipped != nil && (dialect != "Postgres" || postgres):
				t.Fatalf("%q was skipped:\n%s", name, junit)
			}
		}
	}
	if want := 2 * len(stepsCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of steps.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
}
