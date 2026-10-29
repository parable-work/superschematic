// fixture-queries-db's declared queries on Postgres (D86): a household
// ledger's dashboard reports as @query declarations, each a typed function
// of its params whose rows decode through the codec, on the pool and in a
// transaction: the category totals rolled up to the parent, generate_series
// time buckets, the transaction list paged by a keyset, a list in an order
// an enum picks, budgets with exact decimals and dates, and the transfer
// matcher's candidates. The dashboard's four reports share one filter, a
// string constant their SQL interpolates, one of them over the line_list
// projection's view; its totals return one row, and a line of a name one
// row or none (D95).
import { afterAll, beforeAll, describe, expect, test } from 'bun:test';
import { hyperdrivePool, withHyperdrive } from '@superschematic/http-runtime/hyperdrive';
import { NotFoundError, TooManyRowsError, fill } from '@superschematic/http-runtime/postgres';
import { createOrm, fixtureQueriesDbQueries, type FixtureQueriesDbOrm, type TransactionListQueryPageKey } from './orm';
import type { BucketUnit, DashboardFilter, FinanceSignedMoney, GenericDecimal, GenericInt64, IdentityName, IdentityUUID, LineOrder, TemporalDate } from './types';
import { database, dsn, recording, type TestDatabase } from './harness';

const name = (text: string) => text as IdentityName;
const money = (n: number) => n as FinanceSignedMoney;
const count = (n: number) => n as GenericInt64;
const at = (text: string) => new Date(text);

let db: TestDatabase;
let orm: FixtureQueriesDbOrm;
let statements: string[];
const ids: Record<string, IdentityUUID> = {};

beforeAll(async () => {
  db = await database('ts_orm_queries');
  const recorded = recording(db.pool);
  statements = recorded.statements;
  orm = createOrm(recorded.pool);
  ids.checking = (await orm.account.createOne({ name: name('Checking') })).id;
  ids.savings = (await orm.account.createOne({ name: name('Savings') })).id;
  ids.food = (await orm.category.createOne({ name: name('Food') })).id;
  ids.groceries = (await orm.category.createOne({
    name: name('Groceries'),
    parentId: ids.food,
    budget: '400.50' as GenericDecimal,
    budgetSince: '2026-01-01' as TemporalDate,
  })).id;
  ids.rent = (await orm.category.createOne({ name: name('Rent'), budget: '1200' as GenericDecimal })).id;
  const line = async (key: string, date: string, text: string, amount: number, account: string, category?: string) => {
    ids[key] = (await orm.lineItem.createOne({ date: at(date), name: text, amount: money(amount), accountId: ids[account]!, categoryId: category ? ids[category] : null })).id;
  };
  await line('market', '2026-01-05T10:00:00Z', 'Market', -4000, 'checking', 'groceries');
  await line('cafe', '2026-01-10T09:00:00Z', 'Cafe', -1500, 'checking', 'food');
  await line('salary', '2026-01-15T08:00:00Z', 'Salary', 250000, 'checking');
  await line('out', '2026-01-20T12:00:00Z', 'To savings', -50000, 'checking');
  await line('in', '2026-01-21T12:00:00Z', 'From checking', 50000, 'savings');
  await line('rentPaid', '2026-02-03T07:00:00Z', 'Rent', -120000, 'savings', 'rent');
});

afterAll(async () => {
  await db?.drop();
});

const january = { start: at('2026-01-01T00:00:00Z'), end: at('2026-01-31T23:59:59Z') };

describe.skipIf(dsn === '')('declared queries', () => {
  test('categoryTotals rolls a sub-category up to its parent, over a range and some accounts, its rows typed and decoded', async () => {
    const rows = await orm.queries.categoryTotals({ ...january, accountIds: null });
    const byCategory = new Map(rows.map(r => [r.categoryId ?? null, r]));
    expect(byCategory.get(ids.food!)).toEqual({ categoryId: ids.food!, spent: -5500, earned: 0 });
    expect(byCategory.get(null)).toEqual({ categoryId: null, spent: -50000, earned: 300000 });
    expect(rows.length).toBe(2);
    // A list of base62 ids binds hyphenated, as a uuid[].
    expect(await orm.queries.categoryTotals({ ...january, accountIds: [ids.savings!] })).toEqual([{ categoryId: null, spent: 0, earned: 50000 }]);
  });

  test('timeBuckets reads every bucket of a range through generate_series, an empty one with zeros', async () => {
    const rows = await orm.queries.timeBuckets({ unit: 'month' as BucketUnit, start: at('2026-01-01T00:00:00Z'), end: at('2026-03-01T00:00:00Z') });
    expect(rows.map(r => r.bucket.toISOString())).toEqual(['2026-01-01T00:00:00.000Z', '2026-02-01T00:00:00.000Z', '2026-03-01T00:00:00.000Z']);
    expect(rows.map(r => [r.spent, r.earned, r.lines])).toEqual([
      [-55500, 300000, count(5)],
      [-120000, 0, count(1)],
      [0, 0, count(0)],
    ]);
    const weeks = await orm.queries.timeBuckets({ unit: 'week' as BucketUnit, ...january });
    expect(weeks.length).toBe(5);
    expect(weeks.every(w => w.bucket instanceof Date)).toBe(true);
  });

  test('transactionList pages newest first by its key, filters by its optional params, and answers the next key until the last page', async () => {
    const year = { start: at('2026-01-01T00:00:00Z'), end: at('2026-12-31T23:59:59Z'), search: null, minAmount: null };
    const seen: string[] = [];
    let after: TransactionListQueryPageKey | null = null;
    for (let pages = 0; pages < 5; pages++) {
      const page = await orm.queries.transactionList(year, { after, limit: 2 });
      seen.push(...page.rows.map(r => r.name));
      if (!page.next) break;
      expect(page.next).toEqual({ date: page.rows[1]!.date, id: page.rows[1]!.id });
      after = page.next;
    }
    expect(seen).toEqual(['Rent', 'From checking', 'To savings', 'Salary', 'Cafe', 'Market']);
    const first = await orm.queries.transactionList(year);
    expect(first.next).toBeNull();
    expect(first.rows[0]).toEqual({
      id: ids.rentPaid!,
      date: at('2026-02-03T07:00:00Z'),
      name: 'Rent',
      amount: -120000,
      accountName: name('Savings'),
      categoryId: ids.rent!,
      categoryName: name('Rent'),
    });
    expect(first.rows.find(r => r.name === 'Salary')).toMatchObject({ categoryId: null, categoryName: null });
    // The search's %, a literal of the SQL, and a null filter beside a set one.
    const market = await orm.queries.transactionList({ ...year, search: 'MAR' });
    expect(market.rows.map(r => r.name)).toEqual(['Market']);
    const income = await orm.queries.transactionList({ ...year, minAmount: money(1) });
    expect(income.rows.map(r => r.name)).toEqual(['From checking', 'Salary']);
  });

  test('accountLines orders by the fragment its enum names, the default when null, and refuses any other value', async () => {
    const largest = await orm.queries.accountLines({ accountId: ids.checking!, order: 'largest' as LineOrder, limit: count(2) });
    expect(largest.map(r => r.name)).toEqual(['Salary', 'To savings']);
    const newest = await orm.queries.accountLines({ accountId: ids.checking!, order: null, limit: count(10) });
    expect(newest.map(r => r.name)).toEqual(['To savings', 'Salary', 'Cafe', 'Market']);
    const byName = await orm.queries.accountLines({ accountId: ids.checking!, order: 'name' as LineOrder, limit: count(10) });
    expect(byName.map(r => r.name)).toEqual(['Cafe', 'Market', 'Salary', 'To savings']);
    const before = statements.length;
    await expect(orm.queries.accountLines({ accountId: ids.checking!, order: 'li.amount; DROP TABLE line_item' as LineOrder, limit: count(1) })).rejects.toThrow(
      'orm: query accountLines orders by largest, name, newest, not "li.amount; DROP TABLE line_item"'
    );
    expect(statements.length).toBe(before);
  });

  test('budgets read an exact decimal and a date as the wire holds them, and an optional parent', async () => {
    expect(await orm.queries.budgets({ parentId: null })).toEqual([
      { id: ids.groceries!, name: name('Groceries'), budget: '400.5' as GenericDecimal, budgetSince: '2026-01-01' as TemporalDate, parentId: ids.food! },
      { id: ids.rent!, name: name('Rent'), budget: '1200' as GenericDecimal, budgetSince: null, parentId: null },
    ]);
    expect((await orm.queries.budgets({ parentId: ids.food! })).map(r => r.name)).toEqual([name('Groceries')]);
  });

  test('transferCandidates finds the other side of a move within the window', async () => {
    const candidates = await orm.queries.transferCandidates({ lineItemId: ids.out!, windowDays: count(3) });
    expect(candidates).toEqual([{ id: ids.in!, accountId: ids.savings!, amount: 50000, date: at('2026-01-21T12:00:00Z'), daysApart: 1 }]);
    expect(await orm.queries.transferCandidates({ lineItemId: ids.out!, windowDays: count(0) })).toEqual([]);
  });

  test('a query runs in a transaction, and sends the statement sqlutil writes, its parameters positional', async () => {
    const before = statements.length;
    const rows = await orm.transaction(tx => tx.queries.budgets({ parentId: ids.food! }));
    expect(rows.map(r => r.id)).toEqual([ids.groceries!]);
    expect(statements.slice(before)).toEqual(['BEGIN', fill(fixtureQueriesDbQueries.budgets!.text), 'COMMIT']);
    expect(fixtureQueriesDbQueries.budgets!.text).toContain('($1::uuid IS NULL OR c.parent_id = $1)');
    // A transaction of options and a savepoint nested in it hold the queries too (D89).
    const nested = await orm.transaction(tx => tx.transaction(inner => inner.queries.budgets({ parentId: ids.food! })), { readOnly: true });
    expect(nested.map(r => r.id)).toEqual([ids.groceries!]);
    // And over a Hyperdrive pool, as a Worker opens its ORM (D92): on the
    // scope's pool and in a savepoint of a transaction of options.
    const url = new URL(dsn);
    url.searchParams.set('options', `-c search_path=${db.schema},public`);
    const worker = createOrm(hyperdrivePool({ connectionString: url.toString() }));
    const overHyperdrive = await withHyperdrive(async () => {
      const onPool = await worker.queries.budgets({ parentId: ids.food! });
      const inSavepoint = await worker.transaction(tx => tx.transaction(inner => inner.queries.budgets({ parentId: ids.food! })), { readOnly: true });
      return [...onPool, ...inSavepoint];
    });
    expect(overHyperdrive.map(r => r.id)).toEqual([ids.groceries!, ids.groceries!]);
  });

  const dashboard: DashboardFilter = { ...january, accountIds: null, categoryIds: null, search: null };

  test('the dashboard reports share one filter, each statement holding it in place, its parameters numbered alike', async () => {
    const filter = 'li."date" BETWEEN $1 AND $2\n      AND ($3::uuid[] IS NULL OR li.account_id = ANY($3))';
    for (const report of ['dashboardTotals', 'dashboardCategories', 'dashboardLines', 'matchingLine']) {
      expect(fixtureQueriesDbQueries[report]!.text).toContain(filter);
      expect(fixtureQueriesDbQueries[report]!.text).toContain("($5::text IS NULL OR li.\"name\" ILIKE '%%' || $5 || '%%')");
    }
    expect(await orm.queries.dashboardCategories(dashboard)).toEqual([
      { categoryId: ids.groceries!, total: -4000, lines: count(1) },
      { categoryId: ids.food!, total: -1500, lines: count(1) },
      { categoryId: null, total: 250000, lines: count(3) },
    ]);
    expect(await orm.queries.dashboardCategories({ ...dashboard, categoryIds: [ids.food!] })).toEqual([{ categoryId: ids.food!, total: -1500, lines: count(1) }]);
  });

  test('dashboardLines reads the line_list projection through the filter, its required columns never null', async () => {
    const rows = await orm.queries.dashboardLines(dashboard);
    expect(rows.map(r => [r.name, r.accountName, r.categoryName])).toEqual([
      ['From checking', name('Savings'), null],
      ['To savings', name('Checking'), null],
      ['Salary', name('Checking'), null],
      ['Cafe', name('Checking'), name('Food')],
      ['Market', name('Checking'), name('Groceries')],
    ]);
    expect(await orm.queries.dashboardLines({ ...dashboard, search: 'MAR' })).toEqual([
      { id: ids.market!, date: at('2026-01-05T10:00:00Z'), name: 'Market', accountName: name('Checking'), categoryName: name('Groceries') },
    ]);
  });

  test('dashboardTotals returns its one row, which an empty filter still reads, on the pool and in a transaction', async () => {
    const totals = await orm.queries.dashboardTotals(dashboard);
    expect(totals).toEqual({ spent: money(-55500), earned: money(300000), lines: count(5) });
    expect(await orm.queries.dashboardTotals({ ...dashboard, accountIds: [ids.savings!] })).toEqual({ spent: money(0), earned: money(50000), lines: count(1) });
    expect(await orm.queries.dashboardTotals({ ...dashboard, search: 'nothing holds this' })).toEqual({ spent: money(0), earned: money(0), lines: count(0) });
    expect(await orm.transaction(tx => tx.queries.dashboardTotals(dashboard))).toEqual(totals);
  });

  test('a query that returns one row fails on none, as getOne does, and on more than one', async () => {
    expect(await orm.queries.accountNamed({ name: name('Checking') })).toEqual({ id: ids.checking!, name: name('Checking') });
    await expect(orm.queries.accountNamed({ name: name('Nobody') })).rejects.toThrow(NotFoundError);
    await expect(orm.queries.accountNamed({ name: name('Nobody') })).rejects.toThrow('orm: query accountNamed returns one row, and its statement read none');
    await orm.account.createOne({ name: name('Joint') });
    await orm.account.createOne({ name: name('Joint') });
    await expect(orm.queries.accountNamed({ name: name('Joint') })).rejects.toThrow(TooManyRowsError);
    await expect(orm.queries.accountNamed({ name: name('Joint') })).rejects.toThrow('orm: query accountNamed returns one row, and its statement read 2');
  });

  test('a query that returns one row or none answers null for none, and fails on more than one', async () => {
    expect(await orm.queries.matchingLine({ ...dashboard, name: 'Market' })).toEqual({ id: ids.market!, date: at('2026-01-05T10:00:00Z'), amount: money(-4000) });
    expect(await orm.queries.matchingLine({ ...dashboard, name: 'Rent' })).toBeNull();
    const march = { ...dashboard, start: at('2027-03-01T00:00:00Z'), end: at('2027-03-31T23:59:59Z') };
    await orm.lineItem.createOne({ date: at('2027-03-02T08:00:00Z'), name: 'Coffee', amount: money(-300), accountId: ids.savings! });
    expect((await orm.queries.matchingLine({ ...march, name: 'Coffee' }))?.amount).toBe(money(-300));
    await orm.lineItem.createOne({ date: at('2027-03-09T08:00:00Z'), name: 'Coffee', amount: money(-350), accountId: ids.savings! });
    await expect(orm.queries.matchingLine({ ...march, name: 'Coffee' })).rejects.toThrow(TooManyRowsError);
    await expect(orm.queries.matchingLine({ ...march, name: 'Coffee' })).rejects.toThrow('orm: query matchingLine returns one row or none, and its statement read 2');
  });
});
