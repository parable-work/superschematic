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

// commerceFixture is the DB service whose Inventory implements Stock and
// whose Checkout implements Holds beside its status Workflow (D70, D103's
// step 4).
const commerceFixture = "fixture-commerce-db"

// commerceCheck is the file tsc checks the fixture's repositories against:
// Stock's operations under stock, the contract Supply's among them, each
// taking the row's key; Holds' under holds; a hold's name narrowed to the
// config's; a table whose behaviors keep sub-tables has no upserts; and
// the sub-tables in the layout.
const commerceCheck = `import type { FixtureCommerceDbOrm, CheckoutRow, InventoryRow } from './orm';
import { fixtureCommerceDbRules } from './orm';

export async function sell(orm: FixtureCommerceDbOrm, id: InventoryRow['id'], checkout: CheckoutRow['id']): Promise<unknown> {
  await orm.inventory.stock.receiveStock(id, { quantity: 3, reason: 'delivery' });
  await orm.inventory.stock.checkHold(id, { holder: { schema: 'Checkout', id: checkout }, hold: 'lines', item: 0, quantity: 1 });
  const taken = await orm.checkout.holds.retakeHolds(checkout, {});
  const holds: ('lines' | 'wrap')[] = taken.items.map(item => item.hold);
  return holds;
}

// @ts-expect-error A table whose behaviors keep sub-tables has no upserts.
export const upsert = (orm: FixtureCommerceDbOrm) => orm.inventory.upsertOne;
export const tables: readonly string[] = fixtureCommerceDbRules.types.Inventory.uses[0]!.tables!.map(table => table.name);
`

// commerceCases are the cases commerce.test.ts runs on each dialect, by
// name without the dialect's suffix.
var commerceCases = []string{
	"a checkout entering held takes its lines at their price, and placing commits them",
	"two checkouts for the last unit do not both hold it",
}

// TestCommerceTablesEndToEnd holds the generator's layout and Stock's and
// Holds' owned columns and sub-tables to the HTTP runtime's adapter (D70,
// D103's step 4). It loads fixture-commerce-db through the TypeScript
// reader, generates its TypeScript package with the core's registry,
// type-checks it against commerceCheck, and runs testdata/orm/commerce.test.ts
// in it with bun on each dialect: on bun:sqlite, over the SQLite create.sql
// D27 plans, the adapter run on the layout the generated orm.ts embeds, a
// take's reads under the inventory's lock held to its row's version; on
// Postgres, over the generated create.sql in a schema of its own at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, the generated repository, a take
// under the inventory's advisory lock. Every case must pass on bun:sqlite,
// and on Postgres when the database is set; it skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestCommerceTablesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, commerceFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commerceFixture, err)
	}
	model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: commerceFixture}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatalf("plan %s on SQLite: %v", commerceFixture, err)
	}
	sqliteSQL, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"create.sql":        createSQLOf(t, schema, commerceFixture),
		"create.sqlite.sql": sqliteSQL,
	}
	for _, name := range []string{"harness.ts", "commerce.test.ts"} {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     commerceFixture,
		schema:   schema,
		registry: reg,
		extra:    map[string]string{"commerce.check.ts": commerceCheck},
	}})
	dir := filepath.Join(typesRoot, commerceFixture)
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	report := filepath.Join(dir, "commerce.junit.xml")
	run := exec.Command(bunPath, "test", "./commerce.test.ts", "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
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
		for _, name := range commerceCases {
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
	if want := 2 * len(commerceCases); len(cases) != want {
		t.Fatalf("bun ran %d cases of commerce.test.ts, and the test names %d:\n%s", len(cases), want, junit)
	}
	if !postgres {
		t.Logf("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is not set: the cases ran on bun:sqlite alone")
	}
	t.Logf("bun ran the end-to-end test:\n%s", out)
}
