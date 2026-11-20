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

// revisionsFixture is the DB service whose tables implement Revisions
// (D16, D103's step 4): a Policy with a review step and a Note without.
const revisionsFixture = "fixture-revisions-db"

// revisionsCheck is the file tsc checks the fixture's repositories
// against: Revisions' operations under revisions, taking the row's key,
// propose its patch and approve its proposal's number; a table whose
// behaviors keep sub-tables has no upserts; and the sub-tables in the
// layout.
const revisionsCheck = `import type { FixtureRevisionsDbOrm, PolicyRow } from './orm';
import { fixtureRevisionsDbRules } from './orm';

export async function review(orm: FixtureRevisionsDbOrm, id: PolicyRow['id']): Promise<unknown> {
  await orm.policy.revisions.propose(id, { patch: { body: 'Ten days.' } });
  await orm.policy.revisions.approve(id, { proposal: 1 });
  return orm.note.revisions.listRevisions(id, {});
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureRevisionsDbOrm) => orm.policy.upsertOne;
export const tables: readonly string[] = fixtureRevisionsDbRules.types.Policy.uses[0]!.tables!.map(table => table.name);
`

// revisionsCases are the cases revisions.test.ts runs on each dialect, by
// name without the dialect's suffix.
var revisionsCases = []string{
	"a create keeps revision 1 and each change of the own fields the next, rows of the use's sub-table",
	"an approval writes the proposal's patch to the row beside its audit stamp and records the revision it made",
	"an approval without the review permission is refused and its proposal stays pending",
}

// TestRevisionsTablesEndToEnd holds the generator's layout and Revisions'
// sub-tables to the HTTP runtime's adapter (D103's step 4). It loads
// fixture-revisions-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// revisionsCheck, and runs testdata/orm/revisions.test.ts in it with bun on
// each dialect: on bun:sqlite, over the SQLite create.sql D27 plans, the
// adapter run on the layout the generated orm.ts embeds; on Postgres, over
// the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, whose
// operations stamp updatedAt beside an approval's write of the own fields.
// Every case must pass on bun:sqlite, and on Postgres when the database is
// set; it skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says
// the gate must run.
func TestRevisionsTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, revisionsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", revisionsFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: revisionsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", revisionsFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, revisionsFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "revisions.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     revisionsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"revisions.check.ts": revisionsCheck},
	}})
	dir := filepath.Join(typesRoot, revisionsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "revisions.junit.xml")
	run := exec.Command(bunPath, "test", "./revisions.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range revisionsCases {
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
	if want := 2 * len(revisionsCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of revisions.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
