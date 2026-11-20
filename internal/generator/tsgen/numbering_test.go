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

// numberingFixture is the DB service whose tables carry Numbering on fields
// (D56, D103's step 4): an Invoice numbered per customer and per UTC year,
// and a Ticket whose number and reference count apart.
const numberingFixture = "fixture-numbering-db"

// numberingCheck is the file tsc checks the fixture's repositories against:
// assignNumber under number and reference, taking the row's key and
// answering the number and its label; each label string | null on a row; a
// table whose behaviors keep a shared table has no upserts, and the shared
// table no repository; and the shared tables in the layout.
const numberingCheck = `import type { FixtureNumberingDbOrm, InvoiceRow, TicketRow } from './orm';
import { fixtureNumberingDbRules } from './orm';

export async function backfill(orm: FixtureNumberingDbOrm, id: InvoiceRow['id'], ticket: TicketRow['id']): Promise<string> {
  const out = await orm.invoice.number.assignNumber(id, {});
  const seq: number = out.seq;
  void seq;
  const reference = await orm.ticket.reference.assignNumber(ticket, {});
  return out.label + reference.label;
}

export const label = (row: InvoiceRow): string | null => row.number;

// @ts-expect-error A table whose behaviors keep a shared table has no upserts.
export const upsert = (orm: FixtureNumberingDbOrm) => orm.invoice.upsertOne;
// @ts-expect-error A behavior's shared table has no repository.
export const counts = (orm: FixtureNumberingDbOrm) => orm.invoiceNumberCounts;
// @ts-expect-error The number is the behavior's: no create sets it.
export const stamped = (orm: FixtureNumberingDbOrm) => orm.ticket.createOne({ subject: 'x', number: 'T-9' });
export const table: string = fixtureNumberingDbRules.types.Invoice.uses[0]!.shared![0]!.table;
`

// numberingCases are the cases numbering.test.ts runs on each dialect, by
// name without the dialect's suffix.
var numberingCases = []string{
	"a create takes the next number of its customer's count of the year, a row of the shared table",
	"a ticket's number and reference count apart, each in its own shared table",
	"a delete leaves the count where it was: a gap",
	"assignNumber numbers a row made past the repository in the year of its createdAt, and on a numbered row writes nothing",
	"creates that number at once take one number each",
}

// TestNumberingTablesEndToEnd holds the generator's layout and its shared
// tables to the HTTP runtime's adapter (D103's step 4). It loads
// fixture-numbering-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// numberingCheck, and runs testdata/orm/numbering.test.ts in it with bun on
// each dialect: on bun:sqlite, over the SQLite create.sql D27 plans, the
// adapter run on the layout the generated orm.ts embeds; on Postgres, over
// the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository. Every
// case must pass on bun:sqlite, and on Postgres when the database is set;
// it skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the
// gate must run.
func TestNumberingTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, numberingFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", numberingFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: numberingFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", numberingFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, numberingFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "numbering.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     numberingFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"numbering.check.ts": numberingCheck},
	}})
	dir := filepath.Join(typesRoot, numberingFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "numbering.junit.xml")
	run := exec.Command(bunPath, "test", "./numbering.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range numberingCases {
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
	if want := 2 * len(numberingCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of numbering.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
