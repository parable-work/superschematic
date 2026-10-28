package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// returnsCheck is a file of fixture-queries-db's package that tsc checks
// (D95): a query that returns one row resolves to it, one that returns one
// or none to it or null, and one of many rows to its rows, on the ORM and
// in a transaction, each over the dashboard's shared filter.
const returnsCheck = `import type { FixtureQueriesDbOrm } from './orm';
import type { DashboardCategoriesRow, DashboardFilter, DashboardLinesRow, DashboardTotalsRow, FinanceSignedMoney, MatchingLineRow } from './types';

declare const orm: FixtureQueriesDbOrm;
declare const filter: DashboardFilter;

export async function dashboard(): Promise<void> {
  // The one row, with no [[ ]] and no !.
  const totals = await orm.queries.dashboardTotals(filter);
  const spent: FinanceSignedMoney = totals.spent;
  const inTransaction: DashboardTotalsRow = await orm.transaction(tx => tx.queries.dashboardTotals(filter));
  // @ts-expect-error One row is no list of rows.
  const listed: DashboardTotalsRow[] = totals;

  // The row or null.
  const line = await orm.queries.matchingLine({ ...filter, name: 'Market' });
  // @ts-expect-error A matching line may be null.
  void line.amount;
  const amount: FinanceSignedMoney | undefined = line?.amount;
  const kept: MatchingLineRow | null = line;
  const account = await orm.queries.accountNamed({ name: 'Checking' as never });
  const accountId: string = account.id;

  // The rows, as before.
  const categories: DashboardCategoriesRow[] = await orm.queries.dashboardCategories(filter);
  const lines: DashboardLinesRow[] = await orm.queries.dashboardLines(filter);
  void [spent, inTransaction, listed, amount, kept, accountId, categories, lines];
}
`

// TestOneRowQueriesTypeTheirResult type-checks fixture-queries-db's
// package with returnsCheck (D95). It skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestOneRowQueriesTypeTheirResult(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queries-db"))
	if err != nil {
		t.Fatal(err)
	}
	buildTSPackages(t, []tsPackageCase{{name: "fixture-queries-db", schema: schema, extra: map[string]string{"returns.check.ts": returnsCheck}}})
}
