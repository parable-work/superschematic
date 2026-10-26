package tsgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// includeCheck is a file of fixture-finances-db's package that tsc checks
// (D90): a read's rows hold what its options select and include, each
// relation typed by its kind, one, one or null, or a list, and nested
// includes typed in turn; a filter reaches through a relation and ORs
// filters; and a relation, a member or an option the table does not have
// is refused.
const includeCheck = `import type { FixtureFinancesDbOrm, LineItemPageKey, LineItemRow, LineItemSortField, TransactionListRow } from './orm';

declare const orm: FixtureFinancesDbOrm;
declare const ids: readonly string[];
declare const sortField: LineItemSortField;
declare const desc: boolean | undefined;
declare const after: LineItemPageKey | null;

// A read that selects and includes nothing reads the row type, as before.
export async function plain(): Promise<LineItemRow[]> {
  const one: LineItemRow = await orm.lineItem.getOne(ids[0] as never);
  const page = await orm.lineItem.findPage({ userId: { eq: one.userId as never } }, { sort: { field: sortField, desc }, after, limit: 10 });
  const kept: LineItemRow[] = page.rows;
  return orm.transaction(async tx => {
    const { rows } = await tx.lineItem.findMany({ accountId: { eq: one.accountId } }, { limit: 1000, orderBy: [{ field: 'date', desc: true }] });
    return [...kept, ...rows];
  });
}

export async function dashboard(): Promise<void> {
  const page = await orm.lineItem.findPage(
    {
      date: { gte: new Date('2026-01-01'), lt: new Date('2026-02-01') },
      anyOf: [{ categoryId: { in: ids as never } }, { category: { parentId: { in: ids as never } } }],
      amount: { between: ['-100' as never, '0' as never] },
      searchText: 'coffee',
    },
    {
      include: { account: { include: { provider: true } }, category: { include: { parent: true } }, business: true, user: true, notes: true },
      sort: { field: 'date', desc: true },
      limit: 50,
    }
  );
  const row = page.rows[0]!;
  // Account is required and keeps no deleted rows: one row.
  const provider: string = row.account.provider.name;
  // A category may be unset, or soft-deleted: one or null.
  const parent: string | undefined = row.category?.parent?.name;
  const color: string | undefined = row.category?.color;
  const business: string | undefined = row.business?.name;
  const notes: string[] = row.notes.map(note => note.content);
  // @ts-expect-error A category may be null.
  const unchecked: string = row.category.color;
  // @ts-expect-error The account's relations load only when included.
  void row.account.business;
  void [provider, parent, color, business, notes, unchecked, page.next];

  const picked = await orm.lineItem.findMany({ business: { slug: { eq: 'shop' as never } } }, { select: ['id', 'date', 'amount'], limit: 10 });
  const first = picked.rows[0]!;
  const kept: [string, Date] = [first.id, first.date];
  // @ts-expect-error select leaves name out of the row.
  void first.name;
  void kept;

  const one = await orm.account.getOne(ids[0] as never, { include: { lineItems: { select: ['id', 'amount'] } } });
  const amounts: unknown[] = one.lineItems.map(line => line.amount);
  // @ts-expect-error The line items' select leaves name out.
  void one.lineItems[0]!.name;
  void amounts;

  const children = await orm.category.findOne({ slug: { eq: 'food' as never } }, { include: { categories: true, lineItemsBySubCategory: true } });
  void children?.categories.length;
  void children?.lineItemsBySubCategory.length;

  // @ts-expect-error LineItem has no relation vendor.
  await orm.lineItem.findMany(null, { include: { category: true, vendor: true } });
  // @ts-expect-error select names LineItem's members.
  await orm.lineItem.findMany(null, { select: ['vendor'] });
  // @ts-expect-error A misspelled option is refused.
  await orm.lineItem.findMany(null, { limt: 10 });

  const flat = await orm.transactionListRow.findPage(
    { parentCategoryId: { in: ids as never }, accountName: { ilike: 'card' }, anyOf: [{ categoryName: { eq: 'Food' as never } }, { businessId: { isNull: false } }] },
    { sort: { field: 'date', desc: true }, limit: 25 }
  );
  const listed: TransactionListRow[] = flat.rows;
  const providerName: string = listed[0]!.providerName;
  const parentName: string | null = listed[0]!.parentCategoryName;
  void [providerName, parentName, flat.next];
}
`

// TestIncludeAndSelectTypeTheRows type-checks fixture-finances-db's
// package with includeCheck (D90). It skips without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestIncludeAndSelectTypeTheRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-finances-db"))
	if err != nil {
		t.Fatal(err)
	}
	buildTSPackages(t, []tsPackageCase{{name: "fixture-finances-db", schema: schema, extra: map[string]string{"include.check.ts": includeCheck}}})
}
