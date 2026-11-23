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

// paymentsFixture is the DB service whose Order takes payments over its
// own total and whose Refund refunds one, each implementing Payments beside
// its status Workflow (D70, D103's step 4).
const paymentsFixture = "fixture-payments-db"

// paymentsCheck is the file tsc checks the fixture's repositories against:
// Payments' operations under payments, each taking the row's key, and
// listPaymentActions on the table as a whole, taking none; a table whose
// behaviors keep sub-tables has no upserts; and Payments' sub-tables and
// its shared table in the layout.
const paymentsCheck = `import type { FixturePaymentsDbOrm, OrderRow, RefundRow } from './orm';
import { fixturePaymentsDbRules } from './orm';

export async function pay(orm: FixturePaymentsDbOrm, id: OrderRow['id'], refund: RefundRow['id']): Promise<unknown> {
  const opened = await orm.order.payments.openPayment(id, { provider: 'acmepay', idempotencyKey: 'k1' });
  await orm.order.payments.capturePayment(id, { attempt: opened.attempt });
  const waiting = await orm.order.payments.listPaymentActions({ provider: 'acmepay', limit: 10 });
  const settled = await orm.refund.payments.settleRefundFor(refund, { of: { schema: 'Order', id }, refund: 'rf_1', status: 'succeeded', amount: 100 });
  return [opened.amount, waiting.items.map(item => item.kind), waiting.next, settled.state];
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixturePaymentsDbOrm) => orm.order.upsertOne;
// @ts-expect-error listPaymentActions takes no key.
export const keyed = (orm: FixturePaymentsDbOrm, id: OrderRow['id']) => orm.order.payments.listPaymentActions(id, { provider: 'acmepay' });
export const tables: readonly string[] = fixturePaymentsDbRules.types.Order.uses[0]!.tables!.map(table => table.name);
export const shared: readonly string[] = fixturePaymentsDbRules.types.Order.uses[0]!.shared!.map(table => table.name);
`

// paymentsCases are the cases payments.test.ts runs on each dialect, by
// name without the dialect's suffix.
var paymentsCases = []string{
	"an order placed waits until its payment is captured, and an event applies once",
	"a refund approved asks its order for its amount, and the adapter's outcome settles both rows",
}

// TestPaymentsTablesEndToEnd holds the generator's layout and Payments'
// owned columns, sub-tables and shared table to the HTTP runtime's adapter
// (D70, D103's step 4). It loads fixture-payments-db through the TypeScript
// reader, generates its TypeScript package with the core's registry,
// type-checks it against paymentsCheck, and runs
// testdata/orm/payments.test.ts in it with bun on each dialect: on
// bun:sqlite, over the SQLite create.sql D27 plans, the adapter run on the
// layout the generated orm.ts embeds; on Postgres, over the generated
// create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository. A
// refund's move into approved calls refundFor on the order its relation
// points at, and the adapter's outcome on the order calls settleRefundFor
// on the refund, in the one transaction. Every case must pass on
// bun:sqlite, and on Postgres when the database is set; it skips without
// bun unless SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestPaymentsTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, paymentsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", paymentsFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: paymentsFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", paymentsFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, paymentsFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "payments.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     paymentsFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"payments.check.ts": paymentsCheck},
	}})
	dir := filepath.Join(typesRoot, paymentsFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "payments.junit.xml")
	run := exec.Command(bunPath, "test", "./payments.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range paymentsCases {
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
	if want := 2 * len(paymentsCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of payments.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
