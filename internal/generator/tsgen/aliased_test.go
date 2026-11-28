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

// aliasedFixture is the DB service whose Company and Tag carry Aliased on
// a field (D147), Company also Mergeable.
const aliasedFixture = "fixture-aliased-db"

// aliasedCheck is the file tsc checks the fixture's repositories against:
// a create gives the name, an update sets none, rename, addAlias and
// removeAlias take the row's key and resolve none; a table whose behaviors
// keep sub-tables has no upserts, and its aliases and names no repository;
// and Company's merge holds the steps that move them.
const aliasedCheck = `import type { CompanyRow, FixtureAliasedDbOrm } from './orm';
import { companyTable, fixtureAliasedDbRules } from './orm';

export async function rename(orm: FixtureAliasedDbOrm, id: CompanyRow['id']): Promise<string> {
  const created = await orm.company.createOne({ name: 'Acme', city: 'Oslo' });
  const out = await orm.company.name.rename(created.id, { name: 'Acme Group' });
  const alias: string | undefined = out.alias;
  await orm.company.name.addAlias(id, { alias: 'Acme Co' });
  await orm.company.name.removeAlias(id, { alias: 'Acme Co' });
  const found = await orm.company.name.resolve({ name: 'acme' });
  return (found.row?.key ?? '') + (alias ?? '') + orm.tag.label.resolve.name;
}

export const name = (row: CompanyRow): string => row.name;

// @ts-expect-error A create gives the name.
export const nameless = (orm: FixtureAliasedDbOrm) => orm.company.createOne({ city: 'Oslo' });
// @ts-expect-error No update sets the name: only rename does.
export const renamed = (orm: FixtureAliasedDbOrm, id: CompanyRow['id']) => orm.company.updateOne(id, { name: 'Globex' });
// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureAliasedDbOrm) => orm.company.upsertOne;
// @ts-expect-error A behavior's shared table has no repository.
export const names = (orm: FixtureAliasedDbOrm) => orm.companyNameNames;
export const steps: readonly string[] = companyTable.merge!.steps.map(step => step.source + '.' + step.field);
export const table: string = fixtureAliasedDbRules.types.Company.uses[0]!.shared![0]!.table;
`

// aliasedCases are the cases aliased.test.ts runs on each dialect, by name
// without the dialect's suffix; aliasedPostgresCases those it runs on
// Postgres alone, where the generated repositories merge.
var (
	aliasedCases = []string{
		"a name another row holds, as its name or an alias, is refused naming nothing of that row",
		"rename keeps the old name as an alias, and resolve finds the row a name or an alias names",
		"a removed alias, and a deleted row's names, are freed for another row",
		"a tag reads its label as a slug",
	}
	aliasedPostgresCases = []string{
		"a merge moves the aliases of the row merged away, its name among them, to the row that stays",
	}
)

// TestAliasedTablesEndToEnd holds the generator's layout, its sub-table of
// aliases and shared table of names to the HTTP runtime's adapter (D147).
// It loads fixture-aliased-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// aliasedCheck, and runs testdata/orm/aliased.test.ts in it with bun on
// each dialect: on bun:sqlite, over the SQLite create.sql D27 plans, the
// adapter run on the layout the generated orm.ts embeds; on Postgres, over
// the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, whose
// mergeInto moves the aliases. Every case must pass on bun:sqlite, and on
// Postgres when the database is set; it skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestAliasedTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, aliasedFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", aliasedFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: aliasedFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", aliasedFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, aliasedFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "aliased.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     aliasedFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"aliased.check.ts": aliasedCheck},
	}})
	dir := filepath.Join(typesRoot, aliasedFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "aliased.junit.xml")
	run := exec.Command(bunPath, "test", "./aliased.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
	check := func(name string, ran bool) {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil:
			t.Fatalf("%q failed:\n%s", name, junit)
		case tc.Skipped != nil && ran:
			t.Fatalf("%q was skipped:\n%s", name, junit)
		}
	}
	for _, dialect := range []string{"bun:sqlite", "Postgres"} {
		for _, name := range aliasedCases {
			check(name+", on "+dialect, dialect != "Postgres" || postgres)
		}
		for _, name := range aliasedPostgresCases {
			check(name+", on "+dialect, dialect == "Postgres" && postgres)
		}
	}
	if want := 2 * (len(aliasedCases) + len(aliasedPostgresCases)); len(cases) != want {
		t.Fatalf("bun ran %d cases of aliased.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
