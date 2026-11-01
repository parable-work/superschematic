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

// timersFixture is the DB service whose tables implement Timers (D103) in
// tickets.schema.ts: a Ticket whose deadlines read and move its status, a
// Workflow, and an Invoice with two Workflows, whose Timers names the one
// it reads (status: "payment"), its config read with typeof from a
// constant declared as const.
const timersFixture = "fixture-timers-db"

// timersCheck is the file tsc checks the fixture's repositories against:
// a use the table implements has its operations under the behavior's
// member, typed from its declaration as the config narrows it (the
// deadlines' names, a fire's outcome); a table whose behaviors arm
// deadlines runs a due one and has no upserts; and a behavior's sub-table
// has no repository.
const timersCheck = `import type { FixtureTimersDbOrm, TicketRow, InvoiceRow } from './orm';
import { fixtureTimersDbRules } from './orm';

export async function fire(orm: FixtureTimersDbOrm, id: TicketRow['id']): Promise<boolean> {
  const fired = await orm.ticket.timers.fireTimer(id, { deadline: 'respond' });
  const outcome: 'moved' | 'skipped' | 'vetoed' | 'forbidden' | undefined = fired.outcome;
  void outcome;
  const reset: number = (await orm.ticket.timers.resetTimer(id, { deadline: 'idle' })).dueAt;
  await orm.ticket.timers.snoozeTimer(id, { deadline: 'remind', dueAt: reset + 60_000 }, { caller: { subject: 'sam', permissions: ['tickets.timers'] } });
  // @ts-expect-error Ticket's Timers names no deadline late.
  await orm.ticket.timers.fireTimer(id, { deadline: 'late' });
  return fired.fired;
}

export async function overdue(orm: FixtureTimersDbOrm, id: InvoiceRow['id']): Promise<void> {
  await orm.invoice.timers.fireTimer(id, { deadline: 'overdue' });
  // @ts-expect-error Invoice's Timers names no deadline respond.
  await orm.invoice.timers.fireTimer(id, { deadline: 'respond' });
}

export async function due(orm: FixtureTimersDbOrm, id: TicketRow['id']): Promise<boolean> {
  const run = await orm.ticket.runDeadline({ rowKey: id, useId: 'Timers', name: 'respond', at: 0, payload: null }, { claim: { id: 'm', token: 't' } });
  return run.ran;
}

// @ts-expect-error A table whose behaviors arm deadlines has no upserts.
export const upsert = (orm: FixtureTimersDbOrm) => orm.ticket.upsertOne;
// @ts-expect-error A behavior's sub-table has no repository.
export const subTable = (orm: FixtureTimersDbOrm) => orm.ticketTimersDeadlines;
export const queue: string = fixtureTimersDbRules.types.Ticket.deadlines!.table;
`

// timersCases are the cases timers.test.ts runs on each dialect, by name
// without the dialect's suffix: cases 1, 3 and 7 of D103's Timers
// scenario, which the servers run too, and runDeadline with and without a
// claim, a lost claim, and a deleted row's message completed as gone.
var timersCases = []string{
	"a create arms the deadline: its sub-table row and its queue message with its due time",
	"a status that leaves the state cancels the deadline by until",
	"a due deadline runs through runDeadline without a claim, moves the status and records the outcome",
	"a deadline a worker claimed runs, and its unit completes the message",
	"a claim a status change cancelled is lost: the deadline never fires on its old arming",
	"a row deleted past the repository leaves a message the run completes as gone",
	"deleting the row removes its deadlines",
}

// TestTimersTablesEndToEnd holds the generator's layout, its sub-table and
// its deadline queue to the HTTP runtime's adapter (D103). It loads
// fixture-timers-db through the TypeScript reader, generates its TypeScript
// package with the core's registry, type-checks it against timersCheck
// (buildTSPackages, over the local http runtime and @superschematic/rules),
// and runs testdata/orm/timers.test.ts in it with bun on each dialect: on
// bun:sqlite, over the SQLite create.sql D27 plans, the adapter run on the
// layout the generated orm.ts embeds; on Postgres, over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository. Every
// case must pass on bun:sqlite, and on Postgres when the database is set;
// it skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the
// gate must run.
func TestTimersTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, timersFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", timersFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: timersFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", timersFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, timersFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "timers.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}

	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     timersFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"timers.check.ts": timersCheck},
	}})
	// Written once the package type-checks: bun runs the test without a
	// check of its own, and tsc has no bun:test.
	dir := filepath.Join(typesRoot, timersFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "timers.junit.xml")
	run := exec.Command(bunPath, "test", "./timers.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		// bun reports a skipped describe's hooks as a skipped case of no
		// name beside its tests.
		delete(cases, "(unnamed)")
	}
	for _, dialect := range []string{"bun:sqlite", "Postgres"} {
		for _, name := range timersCases {
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
	if want := 2 * len(timersCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of timers.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
