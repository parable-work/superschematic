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

// calendarFixture is the DB service whose Desk implements Calendar and
// whose Booking implements Holds beside its status Workflow (D70, D103's
// step 4).
const calendarFixture = "fixture-calendar-db"

// calendarCheck is the file tsc checks the fixture's repositories against:
// Calendar's operations under calendar, the contract Supply's among them,
// each taking the row's key, and findAvailability on the table as a whole,
// taking none; Holds' under holds; a table whose behaviors keep sub-tables
// has no upserts; and Calendar's sub-tables in the layout.
const calendarCheck = `import type { FixtureCalendarDbOrm, BookingRow, DeskRow } from './orm';
import { fixtureCalendarDbRules } from './orm';

export async function book(orm: FixtureCalendarDbOrm, id: DeskRow['id'], booking: BookingRow['id']): Promise<unknown> {
  await orm.desk.calendar.addClosure(id, { startMs: 0, endMs: 60000, reason: 'training' });
  await orm.desk.calendar.checkHold(id, { holder: { schema: 'Booking', id: booking }, hold: 'slot', item: 0, interval: { startMs: 0, endMs: 60000 } });
  const found = await orm.desk.calendar.findAvailability({ startMs: 0, endMs: 3600000, durationMs: 900000 });
  const taken = await orm.booking.holds.retakeHolds(booking, {});
  return [found.slots.map(slot => slot.id), taken.items.map(item => item.hold)];
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureCalendarDbOrm) => orm.desk.upsertOne;
export const tables: readonly string[] = fixtureCalendarDbRules.types.Desk.uses[0]!.tables!.map(table => table.name);
`

// calendarCases are the cases calendar.test.ts runs on each dialect, by
// name without the dialect's suffix.
var calendarCases = []string{
	"a booking entering held takes its slot, and booking it commits it",
	"two bookings of the one place do not both hold it",
}

// TestCalendarTablesEndToEnd holds the generator's layout and Calendar's
// and Holds' sub-tables to the HTTP runtime's adapter (D70, D103's step 4).
// It loads fixture-calendar-db through the TypeScript reader, generates its
// TypeScript package with the core's registry, type-checks it against
// calendarCheck, and runs testdata/orm/calendar.test.ts in it with bun on
// each dialect: on bun:sqlite, over the SQLite create.sql D27 plans, the
// adapter run on the layout the generated orm.ts embeds, a take's reads of
// the desk's bookings under its lock held to its row's version; on
// Postgres, over the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, a take
// under the desk's advisory lock. Every case must pass on bun:sqlite, and on
// Postgres when the database is set; it skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestCalendarTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, calendarFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", calendarFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: calendarFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", calendarFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, calendarFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "calendar.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     calendarFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"calendar.check.ts": calendarCheck},
	}})
	dir := filepath.Join(typesRoot, calendarFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "calendar.junit.xml")
	run := exec.Command(bunPath, "test", "./calendar.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range calendarCases {
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
	if want := 2 * len(calendarCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of calendar.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
