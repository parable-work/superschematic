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

// workflowFixture is the DB service whose tables' fields are behaviors
// (D103), written as field types in orders.schema.ts: an Order whose
// status is a Workflow with a permission on a transition, and a Shipment
// with two, its status and its paymentStatus, the second's config read with
// typeof from a constant declared as const.
const workflowFixture = "fixture-workflow-db"

// workflowCheck is the file tsc checks the fixture's repositories against:
// a row holds each owned field, typed by its use's config; a create and an
// update take none; and each field's operations sit under it, their
// parameters held to the config's states.
const workflowCheck = `import type { FixtureWorkflowDbOrm, OrderCreate, OrderRow, OrderUpdate, ShipmentRow, ShipmentUpdate } from './orm';
import { fixtureWorkflowDbRules } from './orm';

export async function ship(orm: FixtureWorkflowDbOrm, id: OrderRow['id']): Promise<'draft' | 'paid' | 'shipped'> {
  const moved = await orm.order.status.transition(id, { to: 'paid' });
  await orm.order.status.transition(id, { to: 'shipped' }, { caller: { subject: 'sam', permissions: ['orders.ship'] } });
  // @ts-expect-error Order's status has no state lost.
  await orm.order.status.transition(id, { to: 'lost' });
  return moved.to;
}

export async function pay(orm: FixtureWorkflowDbOrm, id: ShipmentRow['id']): Promise<'unpaid' | 'paid' | 'refunded'> {
  const moved = await orm.shipment.paymentStatus.transition(id, { to: 'paid' });
  // @ts-expect-error paymentStatus's states are its own.
  await orm.shipment.paymentStatus.transition(id, { to: 'sent' });
  // @ts-expect-error Order has no paymentStatus.
  await orm.order.paymentStatus.transition(id, { to: 'paid' });
  return moved.from;
}

export const state: OrderRow['status'] = 'draft';
// @ts-expect-error A row's status is one of its states.
export const lost: OrderRow['status'] = 'lost';
export const entered: OrderRow['statusEnteredAt'] = null;
export const create: OrderCreate = { title: 'Desk' };
// @ts-expect-error A create sets no owned field.
export const owned: OrderCreate = { title: 'Desk', status: 'paid' };
// @ts-expect-error An update sets no owned field.
export const patch: OrderUpdate = { status: 'paid' };
// @ts-expect-error Nor the field a transition stamps.
export const stamp: OrderUpdate = { statusEnteredAt: null };
// @ts-expect-error Nor a second Workflow's.
export const payment: ShipmentUpdate = { paymentStatus: 'paid' };
export const table: string = fixtureWorkflowDbRules.types.Order.table;
`

// workflowCases are the cases workflow.test.ts runs on each dialect, by
// name without the dialect's suffix.
var workflowCases = []string{
	"a create runs the core: each status starts in its initial state, stamped when it entered it, at version 1",
	"a field's typed operation moves it along a transition and bumps the version; the other field stays",
	"a move the config has no transition for is vetoed in the engine's words, and the row stays",
	"a transition that names a permission is forbidden to a caller who lacks it, in the engine's words, and runs for one who holds it",
	"an update takes no owned field: it writes the own fields, and one that names an owned field is refused",
	"a raw SQL write of an owned column is never refused, and moves the row's version",
	"a write that moves the row under a command runs the command again from fresh state",
	"a row that moves under every run is refused as busy after 8 runs, and nothing is written",
}

// workflowPostgresCases are the cases workflow.test.ts runs through the
// generated repository on Postgres alone, by their whole names: the
// repositories run on Postgres, and the adapter has no upsert of its own.
var workflowPostgresCases = []string{
	"an upsert by a unique key inserts a new row with what the core's create gives it, and updates a held row's own fields, its states kept",
}

// TestWorkflowTablesEndToEnd holds the generator's layout to the HTTP
// runtime's adapter (D103). It loads fixture-workflow-db through the
// TypeScript reader, generates its TypeScript package with the core's
// registry, type-checks it against workflowCheck (buildTSPackages, over
// the local http runtime and @superschematic/rules), and runs
// testdata/orm/workflow.test.ts in it with bun on each dialect: on
// bun:sqlite, over the SQLite create.sql D27 plans (sqlmigrate, as the sql
// generator writes sqlite/create.sql), the adapter run on the layout the
// generated orm.ts embeds; on Postgres, over the generated create.sql in a
// schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the
// generated repository, and its upserts (workflowPostgresCases). Every
// case must pass on bun:sqlite, and on Postgres when the database is set;
// it skips without bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the
// gate must run.
func TestWorkflowTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, workflowFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", workflowFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: workflowFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", workflowFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, workflowFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "workflow.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}

	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     workflowFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"workflow.check.ts": workflowCheck},
	}})
	// Written once the package type-checks: bun runs the test without a
	// check of its own, and tsc has no bun:test.
	dir := filepath.Join(typesRoot, workflowFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "workflow.junit.xml")
	run := exec.Command(bunPath, "test", "./workflow.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range workflowCases {
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
	for _, name := range workflowPostgresCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Fatalf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil:
			t.Fatalf("%q failed:\n%s", name, junit)
		case tc.Skipped != nil && postgres:
			t.Fatalf("%q was skipped:\n%s", name, junit)
		}
	}
	if want := 2*len(workflowCases) + len(workflowPostgresCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of workflow.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
