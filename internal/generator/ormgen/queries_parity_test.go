package ormgen

import (
	"os"
	"testing"
)

// TestQueriesParityOfGoAndTypeScriptOnPostgres runs queriesParityScript, a
// script of fixture-queries-db's declared queries (D86), through both ORMs
// on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, and requires
// the same statements, as TestTableParityOfGoAndTypeScriptOnPostgres does:
// a plain query, a list parameter null and set, a query an enum orders,
// both ways, and a paged query's first page and the page after a key, in a
// transaction too.
func TestQueriesParityOfGoAndTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript queries against one Postgres")
	}
	runTableParity(t, queriesFixture, queriesParityScript, queriesParityORMTest, "TestQueriesParity", nil)
}

// queriesParityScript is the TypeScript side, recorded as tableParityScript
// records its calls.
const queriesParityScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'queries_parity_ts_' + process.pid + '_' + Date.now();
const admin = new pg.Client({ connectionString: dsn });
await admin.connect();
await admin.query('CREATE SCHEMA ' + schema);
const base = new pg.Pool({ connectionString: dsn, options: '-c search_path=' + schema + ',public' });
try {
  await base.query(readFileSync(createSQL!, 'utf8'));
  const calls: { name: string; statements: string[] }[] = [];
  const record = (q: OrmQueryable): OrmQueryable => ({
    query(config) {
      calls[calls.length - 1]!.statements.push(config.text);
      return q.query(config as never) as never;
    },
  });
  const pool: OrmPool = {
    ...record(base as unknown as OrmQueryable),
    async connect() {
      const client = await base.connect();
      return { ...record(client as unknown as OrmQueryable), release: (err?: Error | boolean) => client.release(err) };
    },
  };
  const call = async <T>(name: string, fn: () => Promise<T>): Promise<T> => {
    calls.push({ name, statements: [] });
    return fn();
  };
  const orm = createOrm(pool);
  const start = new Date('2026-01-01T00:00:00Z');
  const end = new Date('2026-12-31T23:59:59Z');
  const account = await call('createAccount', () => orm.account.createOne({ name: 'Checking' as never }));
  const one = await call('createLine', () => orm.lineItem.createOne({ date: new Date('2026-01-05T10:00:00Z'), name: 'Market', amount: -4000 as never, accountId: account.id }));
  await call('createOther', () => orm.lineItem.createOne({ date: new Date('2026-01-06T10:00:00Z'), name: 'Cafe', amount: 4000 as never, accountId: account.id }));
  await call('categoryTotals', () => orm.queries.categoryTotals({ start, end, accountIds: null }));
  await call('categoryTotalsOfAccounts', () => orm.queries.categoryTotals({ start, end, accountIds: [account.id] }));
  await call('timeBuckets', () => orm.queries.timeBuckets({ unit: 'week' as never, start, end: new Date('2026-02-01T00:00:00Z') }));
  const first = await call('firstPage', () => orm.queries.transactionList({ start, end, search: null, minAmount: null }, { limit: 1 }));
  await call('pageAfter', () => orm.queries.transactionList({ start, end, search: 'a', minAmount: -5000 as never }, { after: first.next, limit: 1 }));
  await call('accountLinesLargest', () => orm.queries.accountLines({ accountId: account.id, order: 'largest' as never, limit: 5 as never }));
  await call('accountLinesDefault', () => orm.queries.accountLines({ accountId: account.id, order: null, limit: 5 as never }));
  await call('budgets', () => orm.queries.budgets({ parentId: null }));
  await call('transferCandidates', () => orm.queries.transferCandidates({ lineItemId: one.id, windowDays: 3 as never }));
  await call('budgetsInTransaction', () => orm.transaction(tx => tx.queries.budgets({ parentId: account.id })));
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// queriesParityORMTest is the Go side: the same calls through the Go ORM
// over a pool whose tracer records each statement.
const queriesParityORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-queries-db"
)

type parityCall struct {
	Name       string   ` + "`json:\"name\"`" + `
	Statements []string ` + "`json:\"statements\"`" + `
}

type recorder struct {
	mu    sync.Mutex
	calls []parityCall
}

func (r *recorder) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) > 0 {
		r.calls[len(r.calls)-1].Statements = append(r.calls[len(r.calls)-1].Statements, data.SQL)
	}
	return ctx
}

func (r *recorder) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (r *recorder) call(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, parityCall{Name: name})
}

func TestQueriesParity(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	out := os.Getenv("TABLE_PARITY_OUT")
	if dsn == "" || out == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL and TABLE_PARITY_OUT to run the parity script")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("queries_parity_go_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	rec := &recorder{}
	cfg.ConnConfig.Tracer = rec
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rec.calls[len(rec.calls)-1].Name, err)
		}
	}
	at := func(text string) types.TemporalDateTime {
		parsed, err := time.Parse(time.RFC3339, text)
		must(err)
		return types.TemporalDateTime(parsed)
	}
	start, end := at("2026-01-01T00:00:00Z"), at("2026-12-31T23:59:59Z")

	rec.call("createAccount")
	account, err := db.Account.CreateOne(ctx, &types.Account{Name: "Checking"})
	must(err)
	rec.call("createLine")
	one, err := db.LineItem.CreateOne(ctx, &types.LineItem{Date: at("2026-01-05T10:00:00Z"), Name: "Market", Amount: -4000, Account: *account})
	must(err)
	rec.call("createOther")
	_, err = db.LineItem.CreateOne(ctx, &types.LineItem{Date: at("2026-01-06T10:00:00Z"), Name: "Cafe", Amount: 4000, Account: *account})
	must(err)
	rec.call("categoryTotals")
	_, err = db.Queries.CategoryTotals(ctx, types.CategoryTotalsParams{Start: start, End: end})
	must(err)
	rec.call("categoryTotalsOfAccounts")
	_, err = db.Queries.CategoryTotals(ctx, types.CategoryTotalsParams{Start: start, End: end, AccountIds: []types.IdentityUUID{*account.Id}})
	must(err)
	rec.call("timeBuckets")
	_, err = db.Queries.TimeBuckets(ctx, types.TimeBucketsParams{Unit: types.BucketUnit_Week, Start: start, End: at("2026-02-01T00:00:00Z")})
	must(err)
	rec.call("firstPage")
	first, err := db.Queries.TransactionList(ctx, types.TransactionListParams{Start: start, End: end}, &QueryPageOptions[TransactionListPageKey]{Limit: 1})
	must(err)
	rec.call("pageAfter")
	minimum := types.FinanceSignedMoney(-5000)
	_, err = db.Queries.TransactionList(ctx, types.TransactionListParams{Start: start, End: end, Search: "a", MinAmount: &minimum}, &QueryPageOptions[TransactionListPageKey]{After: first.Next, Limit: 1})
	must(err)
	rec.call("accountLinesLargest")
	largest := types.LineOrder_Largest
	_, err = db.Queries.AccountLines(ctx, types.AccountLinesParams{AccountId: *account.Id, Order: &largest, Limit: 5})
	must(err)
	rec.call("accountLinesDefault")
	_, err = db.Queries.AccountLines(ctx, types.AccountLinesParams{AccountId: *account.Id, Limit: 5})
	must(err)
	rec.call("budgets")
	_, err = db.Queries.Budgets(ctx, types.BudgetsParams{})
	must(err)
	rec.call("transferCandidates")
	_, err = db.Queries.TransferCandidates(ctx, types.TransferCandidatesParams{LineItemId: *one.Id, WindowDays: 3})
	must(err)
	rec.call("budgetsInTransaction")
	must(db.Transaction(ctx, func(tx TxInterface) error {
		_, err := tx.GetQueries().Budgets(ctx, types.BudgetsParams{ParentId: account.Id})
		return err
	}))

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
