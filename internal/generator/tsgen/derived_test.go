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

// derivedProvenanceFixture is the DB service whose tables implement Derived and
// Provenance (D73, D103's step 4): a Ticket a model triages under review
// and an Article a model summarizes.
const derivedProvenanceFixture = "fixture-derived-provenance-db"

// derivedCheck is the file tsc checks the fixture's repositories against:
// Derived's pulls and settles under derived, operations of the table as a
// whole that take no key, applyDerived and rederive taking the row's key,
// Provenance's accept and reject under provenance; a table whose
// behaviors keep sub-tables has no upserts; each derived field's schema in
// the layout; and a table's derived operations as the DerivedTable a
// deriver's loop runs over (D146).
const derivedCheck = `import type { DerivedTable } from '@superschematic/http-runtime/deriver';
import type { FixtureDerivedProvenanceDbOrm, TicketRow } from './orm';
import { fixtureDerivedProvenanceDbRules } from './orm';

export const derivedTables = (orm: FixtureDerivedProvenanceDbOrm): Record<string, DerivedTable> => ({ Ticket: orm.ticket.derived, Article: orm.article.derived });

export async function triage(orm: FixtureDerivedProvenanceDbOrm, id: TicketRow['id']): Promise<unknown> {
  await orm.ticket.derived.staleDerivations({ field: 'category', limit: 10 });
  await orm.ticket.derived.rederive(id, { fields: ['priority'] });
  return orm.ticket.provenance.accept(id, { fields: ['category'] });
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureDerivedProvenanceDbOrm) => orm.ticket.upsertOne;
export const tables: readonly string[] = fixtureDerivedProvenanceDbRules.types.Ticket.uses[0]!.tables!.map(table => table.name);
export const output: unknown = fixtureDerivedProvenanceDbRules.types.Ticket.spec.fields!.find(field => field.name === 'category')!.schema;
`

// derivedCases are the cases derived.test.ts runs on each dialect, by name
// without the dialect's suffix.
var derivedCases = []string{
	"a deriver pulls a ticket's stale fields and settles them as own fields, the unsure one proposed until a reviewer accepts it",
	"a value the field's enum refuses counts an attempt and writes no own field",
	"a deriver's loop hands each stale field to a model and settles its answers",
}

// TestDerivedTablesEndToEnd holds the generator's layout, each derived
// field's schema in it, and Derived's and Provenance's sub-tables and owned
// column to the HTTP runtime's adapter (D73, D103's step 4). It loads
// fixture-derived-provenance-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// derivedCheck, and runs testdata/orm/derived.test.ts in it with bun on
// each dialect: on bun:sqlite, over the SQLite create.sql D27 plans, the
// adapter run on the layout the generated orm.ts embeds; on Postgres, over
// the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, over
// whose derived operations the runtime's runDeriver runs with a model that
// is not one (D146). Every
// case must pass on bun:sqlite, and on Postgres when the database is set;
// it skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the
// gate must run.
func TestDerivedTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, derivedProvenanceFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", derivedProvenanceFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: derivedProvenanceFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", derivedProvenanceFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, derivedProvenanceFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "derived.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     derivedProvenanceFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"derived.check.ts": derivedCheck},
	}})
	dir := filepath.Join(typesRoot, derivedProvenanceFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "derived.junit.xml")
	run := exec.Command(bunPath, "test", "./derived.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range derivedCases {
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
	if want := 2 * len(derivedCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of derived.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
