package ormgen

import (
	"os"
	"testing"
)

// TestReadParityOfGoAndTypeScriptOnPostgres runs readParityScript, the
// reads of fixture-finances-db (D90), through both ORMs on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, and requires the same
// statements, call by call, as TestTableParityOfGoAndTypeScriptOnPostgres
// does: a filter through a to-one relation and one two relations deep, a
// relation's filter of no member, anyOf and an anyOf with a branch of no
// condition, beside a search and in an UpdateMany; select; the dashboard's
// page with its relations included, nested, the one-to-many @hasMany
// include, and a to-one include on GetOne and FindOne; and the
// TransactionListRow projection's GetOne, FindOne, FindMany and FindPage,
// filtered on joined columns, and its next page. Each call's include in
// TypeScript names what its Go Fields select, each relation's columns and
// the nested relations.
func TestReadParityOfGoAndTypeScriptOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Go and TypeScript repositories against one Postgres")
	}
	runTableParity(t, financesFixture, readParityScript, readParityORMTest, "TestReadParity", nil)
}

// readParityScript is the TypeScript side: the reads, recorded as
// tableParityScript records its calls.
const readParityScript = `import { readFileSync, writeFileSync } from 'node:fs';
import pg from 'pg';
import type { OrmPool, OrmQueryable } from '@superschematic/http-runtime/postgres';
import { createOrm } from '@TYPES@/orm';

const [createSQL, out] = process.argv.slice(2);
const dsn = process.env.SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL!;
const schema = 'read_parity_ts_' + process.pid + '_' + Date.now();
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
  const orm = createOrm(pool, 'TKbHsHDO6x9RCjbs5c2u4');
  const day = (d: number) => new Date(Date.UTC(2026, 0, d, 12));
  const n = (s: string) => s as never;

  const ann = await call('createAnn', () => orm.user.createOne({ email: 'ann@example.com', name: n('Ann'), createdAt: day(1) }));
  const bank = await call('createBank', () => orm.provider.createOne({ name: n('First Bank'), slug: n('first-bank') }));
  const shop = await call('createShop', () => orm.business.createOne({ name: n('Shop'), slug: n('shop') }));
  const checking = await call('createChecking', () => orm.account.createOne({ name: n('Checking'), slug: n('checking'), providerId: bank.id, ownerId: ann.id }));
  const food = await call('createFood', () => orm.category.createOne({ name: n('Food'), slug: n('food'), color: '#00aa00' }));
  const dining = await call('createDining', () => orm.category.createOne({ name: n('Dining'), slug: n('dining'), color: '#00aa00', parentId: food.id }));
  const groceries = await call('createGroceries', () => orm.category.createOne({ name: n('Groceries'), slug: n('groceries'), color: '#00aa00', parentId: food.id }));
  const beans = await call('createBeans', () =>
    orm.lineItem.createOne({ date: day(5), name: 'Coffee beans', amount: n(-1250), accountId: checking.id, categoryId: groceries.id, businessId: shop.id, userId: ann.id, createdAt: day(5) })
  );
  await call('createDinner', () =>
    orm.lineItem.createOne({ date: day(8), name: 'Dinner and coffee', amount: n(-2000), accountId: checking.id, categoryId: dining.id, businessId: shop.id, userId: ann.id, createdAt: day(8) })
  );
  await call('createSalary', () => orm.lineItem.createOne({ date: day(9), name: 'Salary', amount: n(10000), accountId: checking.id, userId: ann.id, createdAt: day(9) }));

  const either = [{ categoryId: { in: [food.id] } }, { category: { parentId: { in: [food.id] } } }];
  await call('findThroughCategory', () => orm.lineItem.findMany({ category: { parentId: { in: [food.id] } } }, { select: ['id', 'name'], orderBy: [{ field: 'date' }] }));
  await call('findThroughParent', () => orm.lineItem.findMany({ category: { parent: { name: { eq: n('Food') } } }, amount: { lt: n(0) } }, { select: ['id', 'name'] }));
  await call('findWithCategory', () => orm.lineItem.findOne({ category: {} }, { select: ['id', 'name'] }));
  await call('findEither', () => orm.lineItem.findMany({ anyOf: either, searchText: 'coffee', userId: { eq: ann.id } }, { select: ['id', 'name'], limit: 10 }));
  await call('findEitherOrAll', () => orm.lineItem.findMany({ anyOf: [{ categoryId: { in: [food.id] } }, {}], amount: { lt: n(0) } }, { select: ['id'] }));
  await call('updateThroughCategory', () => orm.lineItem.updateMany({ anyOf: either, category: { name: { eq: n('Dining') } } }, { description: 'out' }));
  const page = await call('pageDashboard', () =>
    orm.lineItem.findPage(
      { date: { gte: day(1), lt: day(31) }, anyOf: either, businessId: { eq: shop.id }, amount: { between: [n(-5000), n(0)] }, searchText: 'coffee' },
      {
        select: ['id', 'date', 'name', 'description', 'amount'],
        include: {
          account: { select: ['id', 'name'], include: { provider: { select: ['id', 'name'] } } },
          category: { select: ['id', 'name', 'color'], include: { parent: { select: ['id', 'name'] } } },
          business: { select: ['id', 'name'] },
          user: { select: ['id', 'name'] },
        },
        sort: { field: 'date', desc: true },
        limit: 1,
      }
    )
  );
  await call('pageDashboardAfter', () =>
    orm.lineItem.findPage(
      { anyOf: either },
      { select: ['id', 'name'], include: { category: { select: ['id', 'name'] } }, sort: { field: 'date', desc: true }, limit: 1, after: page.next }
    )
  );
  await call('getWithCategory', () => orm.lineItem.getOne(beans.id, { select: ['id'], include: { category: { select: ['id', 'name'] } } }));
  await call('findOneWithAccount', () => orm.lineItem.findOne({ name: { eq: 'Salary' } }, { select: ['id', 'name'], include: { account: { select: ['id', 'name'] } } }));
  await call('getAccountLines', () => orm.account.getOne(checking.id, { select: ['id'], include: { lineItems: { select: ['id', 'name'] } } }));
  await call('findAccountsLines', () => orm.account.findMany(null, { select: ['id', 'name'], include: { lineItems: { select: ['id', 'name'] } } }));
  await call('deleteDining', () => orm.category.deleteOne(dining.id));
  await call('findThroughDeleted', () => orm.lineItem.findMany({ category: { name: { eq: n('Dining') } } }, { select: ['id'] }));

  const listed = await call('pageList', () =>
    orm.transactionListRow.findPage(
      { anyOf: [{ categoryId: { in: [food.id] } }, { parentCategoryId: { in: [food.id] } }], providerName: { eq: n('First Bank') }, amount: { lt: n(0) } },
      { sort: { field: 'date', desc: true }, limit: 1 }
    )
  );
  await call('pageListAfter', () => orm.transactionListRow.findPage({ businessName: { isNull: false } }, { sort: { field: 'date', desc: true }, limit: 1, after: listed.next }));
  await call('pageListByKey', () => orm.transactionListRow.findPage(null, { limit: 2 }));
  await call('getListed', () => orm.transactionListRow.getOne(beans.id));
  await call('findListed', () => orm.transactionListRow.findOne({ userName: { ilike: 'an' } }, { orderBy: [{ field: 'amount', desc: true }] }));
  await call('findManyListed', () => orm.transactionListRow.findMany({ categoryName: { isNull: true } }, { orderBy: [{ field: 'date' }], limit: 5, offset: 0 }));
  writeFileSync(out!, JSON.stringify(calls, null, 2));
} finally {
  await base.end();
  await admin.query('DROP SCHEMA ' + schema + ' CASCADE');
  await admin.end();
}
`

// readParityORMTest is the Go side: the same calls through the Go ORM over
// a pool whose tracer records each statement.
const readParityORMTest = `package orm

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
	types "example.com/schemas/types/go/fixture-finances-db"
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

func TestReadParity(t *testing.T) {
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
	schema := fmt.Sprintf("read_parity_go_%d", time.Now().UnixNano())
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
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rec.calls[len(rec.calls)-1].Name, err)
		}
	}
	day := func(d int) types.TemporalDateTime {
		return types.TemporalDateTime(time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC))
	}
	str := func(s string) *string { return &s }
	i64 := func(n int64) *int64 { return &n }
	no := false
	yes := true
	_ = yes

	rec.call("createAnn")
	ann, err := db.User.CreateOne(ctx, &types.User{Email: "ann@example.com", Name: "Ann", CreatedAt: day(1)})
	must(err)
	rec.call("createBank")
	bank, err := db.Provider.CreateOne(ctx, &types.Provider{Name: "First Bank", Slug: "first-bank"})
	must(err)
	rec.call("createShop")
	shop, err := db.Business.CreateOne(ctx, &types.Business{Name: "Shop", Slug: "shop"})
	must(err)
	rec.call("createChecking")
	checking, err := db.Account.CreateOne(ctx, &types.Account{Name: "Checking", Slug: "checking", Provider: types.Provider{Id: bank.Id}, Owner: &types.User{Id: ann.Id}})
	must(err)
	rec.call("createFood")
	food, err := db.Category.CreateOne(ctx, &types.Category{Name: "Food", Slug: "food", Color: "#00aa00"})
	must(err)
	rec.call("createDining")
	dining, err := db.Category.CreateOne(ctx, &types.Category{Name: "Dining", Slug: "dining", Color: "#00aa00", Parent: &types.Category{Id: food.Id}})
	must(err)
	rec.call("createGroceries")
	groceries, err := db.Category.CreateOne(ctx, &types.Category{Name: "Groceries", Slug: "groceries", Color: "#00aa00", Parent: &types.Category{Id: food.Id}})
	must(err)
	rec.call("createBeans")
	beans, err := db.LineItem.CreateOne(ctx, &types.LineItem{Date: day(5), Name: "Coffee beans", Amount: -1250, Account: types.Account{Id: checking.Id}, Category: &types.Category{Id: groceries.Id}, Business: &types.Business{Id: shop.Id}, User: &types.User{Id: ann.Id}, CreatedAt: day(5)})
	must(err)
	rec.call("createDinner")
	_, err = db.LineItem.CreateOne(ctx, &types.LineItem{Date: day(8), Name: "Dinner and coffee", Amount: -2000, Account: types.Account{Id: checking.Id}, Category: &types.Category{Id: dining.Id}, Business: &types.Business{Id: shop.Id}, User: &types.User{Id: ann.Id}, CreatedAt: day(8)})
	must(err)
	rec.call("createSalary")
	_, err = db.LineItem.CreateOne(ctx, &types.LineItem{Date: day(9), Name: "Salary", Amount: 10000, Account: types.Account{Id: checking.Id}, User: &types.User{Id: ann.Id}, CreatedAt: day(9)})
	must(err)

	inFood := &UUIDFilter{In: []types.IdentityUUID{*food.Id}}
	either := []LineItemFilter{{CategoryID: inFood}, {Category: &CategoryFilter{ParentID: inFood}}}
	idName := LineItemFields{Id: true, Name: true}
	rec.call("findThroughCategory")
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{Category: &CategoryFilter{ParentID: inFood}}, &LineItemFindOptions{Fields: idName, OrderBy: []LineItemOrderBy{{Field: LineItemOrderByDate}}})
	must(err)
	rec.call("findThroughParent")
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{Amount: &Int64Filter{Lt: i64(0)}, Category: &CategoryFilter{Parent: &CategoryFilter{Name: &StringFilter{Eq: str("Food")}}}}, &LineItemFindOptions{Fields: idName})
	must(err)
	rec.call("findWithCategory")
	_, err = db.LineItem.FindOne(ctx, &LineItemFilter{Category: &CategoryFilter{}}, &LineItemFindOptions{Fields: idName})
	must(err)
	rec.call("findEither")
	coffee := "coffee"
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{AnyOf: either, SearchText: &coffee, UserID: &UUIDFilter{Eq: ann.Id}}, &LineItemFindOptions{Fields: idName, Limit: 10})
	must(err)
	rec.call("findEitherOrAll")
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{AnyOf: []LineItemFilter{{CategoryID: inFood}, {}}, Amount: &Int64Filter{Lt: i64(0)}}, &LineItemFindOptions{Fields: LineItemFields{Id: true}})
	must(err)
	rec.call("updateThroughCategory")
	out2 := "out"
	_, err = db.LineItem.UpdateMany(ctx, &LineItemFilter{AnyOf: either, Category: &CategoryFilter{Name: &StringFilter{Eq: str("Dining")}}}, &LineItemUpdate{Description: &out2})
	must(err)
	rec.call("pageDashboard")
	start, end := time.Time(day(1)), time.Time(day(31))
	page, err := db.LineItem.FindPage(ctx, &LineItemFilter{
		Date:       &DateTimeFilter{Gte: &start, Lt: &end},
		AnyOf:      either,
		BusinessID: &UUIDFilter{Eq: shop.Id},
		Amount:     &Int64Filter{Between: &Int64Range{Min: -5000, Max: 0}},
		SearchText: &coffee,
	}, &LineItemPageOptions{
		Fields: LineItemFields{
			Id: true, Date: true, Name: true, Description: true, Amount: true,
			AccountNested:  AccountFields{Id: true, Name: true, ProviderNested: ProviderFields{Id: true, Name: true}},
			CategoryNested: CategoryFields{Id: true, Name: true, Color: true, ParentNested: &CategoryFields{Id: true, Name: true}},
			BusinessNested: BusinessFields{Id: true, Name: true},
			UserNested:     UserFields{Id: true, Name: true},
		},
		Sort:  LineItemSort{Field: LineItemSortByDate, Desc: true},
		Limit: 1,
	})
	must(err)
	rec.call("pageDashboardAfter")
	_, err = db.LineItem.FindPage(ctx, &LineItemFilter{AnyOf: either}, &LineItemPageOptions{
		Fields: LineItemFields{Id: true, Name: true, CategoryNested: CategoryFields{Id: true, Name: true}},
		Sort:   LineItemSort{Field: LineItemSortByDate, Desc: true}, Limit: 1, After: page.Next,
	})
	must(err)
	rec.call("getWithCategory")
	_, err = db.LineItem.GetOne(ctx, *beans.Id, &LineItemGetOptions{Fields: LineItemFields{Id: true, CategoryNested: CategoryFields{Id: true, Name: true}}})
	must(err)
	rec.call("findOneWithAccount")
	_, err = db.LineItem.FindOne(ctx, &LineItemFilter{Name: &StringFilter{Eq: str("Salary")}}, &LineItemFindOptions{Fields: LineItemFields{Id: true, Name: true, AccountNested: AccountFields{Id: true, Name: true}}})
	must(err)
	rec.call("getAccountLines")
	_, err = db.Account.GetOne(ctx, *checking.Id, &AccountGetOptions{Fields: AccountFields{Id: true, LineItemsNested: &LineItemFields{Id: true, Name: true}}})
	must(err)
	rec.call("findAccountsLines")
	_, _, err = db.Account.FindMany(ctx, nil, &AccountFindOptions{Fields: AccountFields{Id: true, Name: true, LineItemsNested: &LineItemFields{Id: true, Name: true}}})
	must(err)
	rec.call("deleteDining")
	must(db.Category.DeleteOne(ctx, *dining.Id))
	rec.call("findThroughDeleted")
	_, _, err = db.LineItem.FindMany(ctx, &LineItemFilter{Category: &CategoryFilter{Name: &StringFilter{Eq: str("Dining")}}}, &LineItemFindOptions{Fields: LineItemFields{Id: true}})
	must(err)

	listEither := []TransactionListRowFilter{{CategoryId: inFood}, {ParentCategoryId: inFood}}
	rec.call("pageList")
	listed, err := db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{AnyOf: listEither, ProviderName: &StringFilter{Eq: str("First Bank")}, Amount: &Int64Filter{Lt: i64(0)}},
		&TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 1})
	must(err)
	rec.call("pageListAfter")
	_, err = db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{BusinessName: &StringFilter{IsNull: &no}},
		&TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 1, After: listed.Next})
	must(err)
	rec.call("pageListByKey")
	_, err = db.TransactionListRow().FindPage(ctx, nil, &TransactionListRowPageOptions{Limit: 2})
	must(err)
	rec.call("getListed")
	_, err = db.TransactionListRow().GetOne(ctx, *beans.Id)
	must(err)
	rec.call("findListed")
	_, err = db.TransactionListRow().FindOne(ctx, &TransactionListRowFilter{UserName: &StringFilter{ILike: str("an")}}, &TransactionListRowFindOptions{OrderBy: []TransactionListRowOrderBy{{Field: TransactionListRowOrderByAmount, Desc: true}}})
	must(err)
	rec.call("findManyListed")
	_, _, err = db.TransactionListRow().FindMany(ctx, &TransactionListRowFilter{CategoryName: &StringFilter{IsNull: &yes}}, &TransactionListRowFindOptions{OrderBy: []TransactionListRowOrderBy{{Field: TransactionListRowOrderByDate}}, Limit: 5})
	must(err)

	data, err := json.MarshalIndent(rec.calls, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
`
