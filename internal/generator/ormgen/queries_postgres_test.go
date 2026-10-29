package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const queriesFixture = "fixture-queries-db"

// TestDeclaredQueriesOnPostgres generates fixture-queries-db's Go types and
// ORM (D86) and runs queriesORMTest in the module against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL: each declared query, on the
// pool and in a transaction, its params bound as their columns take them
// and its rows read by column name into the row type's fields, the
// dashboard's category totals, generate_series time buckets and paged
// transaction list among them, and the reports that share one filter, of
// which the totals return one row and a line of a name one or none (D95).
func TestDeclaredQueriesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, queriesFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "queries_test.go"), []byte(queriesORMTest), 0o644); err != nil {
		t.Fatal(err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		return
	}
	for _, test := range []string{"TestDeclaredQueriesOnPostgres", "TestOneRowQueriesOnPostgres", "TestDeclaredQueriesNoOp"} {
		if !strings.Contains(out, "--- PASS: "+test) {
			t.Fatalf("the generated ORM module did not run %s", test)
		}
	}
}

const queriesORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	types "example.com/schemas/types/go/fixture-queries-db"
)

func openQueries(t *testing.T) (context.Context, *Database) {
	t.Helper()
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("queries_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatalf("connect generated orm: %v", err)
	}
	return ctx, db
}

func at(t *testing.T, text string) types.TemporalDateTime {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatal(err)
	}
	return types.TemporalDateTime(parsed)
}

func same(a, b types.TemporalDateTime) bool { return time.Time(a).Equal(time.Time(b)) }

// TestDeclaredQueriesOnPostgres runs each of fixture-queries-db's queries
// over a small ledger, as the TypeScript repositories' queries.test.ts does.
func TestDeclaredQueriesOnPostgres(t *testing.T) {
	ctx, db := openQueries(t)
	checking, err := db.Account.CreateOne(ctx, &types.Account{Name: "Checking"})
	if err != nil {
		t.Fatal(err)
	}
	savings, err := db.Account.CreateOne(ctx, &types.Account{Name: "Savings"})
	if err != nil {
		t.Fatal(err)
	}
	food, err := db.Category.CreateOne(ctx, &types.Category{Name: "Food"})
	if err != nil {
		t.Fatal(err)
	}
	budget := types.GenericDecimal("400.50")
	since := types.TemporalDate("2026-01-01")
	groceries, err := db.Category.CreateOne(ctx, &types.Category{Name: "Groceries", Parent: food, Budget: &budget, BudgetSince: &since})
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]*types.LineItem{}
	for _, l := range []struct {
		key, date, name string
		amount          int64
		account         *types.Account
		category        *types.Category
	}{
		{"market", "2026-01-05T10:00:00Z", "Market", -4000, checking, groceries},
		{"cafe", "2026-01-10T09:00:00Z", "Cafe", -1500, checking, food},
		{"salary", "2026-01-15T08:00:00Z", "Salary", 250000, checking, nil},
		{"out", "2026-01-20T12:00:00Z", "To savings", -50000, checking, nil},
		{"in", "2026-01-21T12:00:00Z", "From checking", 50000, savings, nil},
	} {
		line, err := db.LineItem.CreateOne(ctx, &types.LineItem{Date: at(t, l.date), Name: l.name, Amount: types.FinanceSignedMoney(l.amount), Account: *l.account, Category: l.category})
		if err != nil {
			t.Fatalf("create %s: %v", l.key, err)
		}
		lines[l.key] = line
	}
	january := func() (types.TemporalDateTime, types.TemporalDateTime) {
		return at(t, "2026-01-01T00:00:00Z"), at(t, "2026-01-31T23:59:59Z")
	}

	start, end := january()
	totals, err := db.Queries.CategoryTotals(ctx, types.CategoryTotalsParams{Start: start, End: end})
	if err != nil {
		t.Fatal(err)
	}
	byCategory := map[string]types.CategoryTotalsRow{}
	for _, row := range totals {
		key := "none"
		if row.CategoryId != nil {
			key = row.CategoryId.String()
		}
		byCategory[key] = row
	}
	if got := byCategory[food.Id.String()]; got.Spent != -5500 || got.Earned != 0 || len(totals) != 2 {
		t.Fatalf("category totals = %+v", totals)
	}
	if got := byCategory["none"]; got.Spent != -50000 || got.Earned != 300000 {
		t.Fatalf("uncategorized totals = %+v", got)
	}
	onlySavings, err := db.Queries.CategoryTotals(ctx, types.CategoryTotalsParams{Start: start, End: end, AccountIds: []types.IdentityUUID{*savings.Id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlySavings) != 1 || onlySavings[0].CategoryId != nil || onlySavings[0].Earned != 50000 {
		t.Fatalf("totals of savings = %+v", onlySavings)
	}

	buckets, err := db.Queries.TimeBuckets(ctx, types.TimeBucketsParams{Unit: types.BucketUnit_Month, Start: at(t, "2026-01-01T00:00:00Z"), End: at(t, "2026-03-01T00:00:00Z")})
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 3 || !same(buckets[1].Bucket, at(t, "2026-02-01T00:00:00Z")) || buckets[0].Lines != 5 || buckets[2].Lines != 0 || buckets[0].Spent != -55500 {
		t.Fatalf("time buckets = %+v", buckets)
	}

	year := types.TransactionListParams{Start: at(t, "2026-01-01T00:00:00Z"), End: at(t, "2026-12-31T23:59:59Z")}
	var seen []string
	page := &QueryPageOptions[TransactionListPageKey]{Limit: 2}
	for range 5 {
		got, err := db.Queries.TransactionList(ctx, year, page)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range got.Rows {
			seen = append(seen, row.Name)
		}
		if got.Next == nil {
			break
		}
		page = &QueryPageOptions[TransactionListPageKey]{After: got.Next, Limit: 2}
	}
	if want := []string{"From checking", "To savings", "Salary", "Cafe", "Market"}; !reflect.DeepEqual(seen, want) {
		t.Fatalf("transaction list pages = %v, want %v", seen, want)
	}
	searched, err := db.Queries.TransactionList(ctx, types.TransactionListParams{Start: year.Start, End: year.End, Search: "MAR"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(searched.Rows) != 1 || searched.Rows[0].Name != "Market" || searched.Rows[0].CategoryName == nil || *searched.Rows[0].CategoryName != "Groceries" || searched.Rows[0].AccountName != "Checking" {
		t.Fatalf("search = %+v", searched.Rows)
	}
	minimum := types.FinanceSignedMoney(1)
	income, err := db.Queries.TransactionList(ctx, types.TransactionListParams{Start: year.Start, End: year.End, MinAmount: &minimum}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(income.Rows) != 2 || income.Rows[1].CategoryId != nil || income.Rows[1].CategoryName != nil {
		t.Fatalf("income = %+v", income.Rows)
	}

	largest := types.LineOrder_Largest
	ordered, err := db.Queries.AccountLines(ctx, types.AccountLinesParams{AccountId: *checking.Id, Order: &largest, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 2 || ordered[0].Name != "Salary" || ordered[1].Name != "To savings" {
		t.Fatalf("largest lines = %+v", ordered)
	}
	newest, err := db.Queries.AccountLines(ctx, types.AccountLinesParams{AccountId: *checking.Id, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(newest) != 4 || newest[0].Name != "To savings" {
		t.Fatalf("newest lines = %+v", newest)
	}
	injected := types.LineOrder("li.amount; DROP TABLE line_item")
	if _, err := db.Queries.AccountLines(ctx, types.AccountLinesParams{AccountId: *checking.Id, Order: &injected, Limit: 1}); err == nil || !strings.Contains(err.Error(), "orders by largest, name, newest") {
		t.Fatalf("an order no option names = %v", err)
	}

	budgets, err := db.Queries.Budgets(ctx, types.BudgetsParams{})
	if err != nil {
		t.Fatal(err)
	}
	if len(budgets) != 1 || budgets[0].Budget != "400.5" || budgets[0].BudgetSince == nil || *budgets[0].BudgetSince != "2026-01-01" || budgets[0].ParentId == nil || *budgets[0].ParentId != *food.Id {
		t.Fatalf("budgets = %+v", budgets)
	}

	candidates, err := db.Queries.TransferCandidates(ctx, types.TransferCandidatesParams{LineItemId: *lines["out"].Id, WindowDays: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].Id != *lines["in"].Id || candidates[0].DaysApart != 1 || candidates[0].AccountId != *savings.Id {
		t.Fatalf("transfer candidates = %+v", candidates)
	}

	// The same query in a transaction.
	err = db.Transaction(ctx, func(tx TxInterface) error {
		rows, err := tx.GetQueries().Budgets(ctx, types.BudgetsParams{ParentId: food.Id})
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].Id != *groceries.Id {
			return fmt.Errorf("budgets of food in a transaction = %+v", rows)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// And in a transaction of options and a savepoint nested in it (D89),
	// whose Tx holds the queries too.
	err = db.TransactionWith(ctx, TxOptions{ReadOnly: true}, func(tx TxInterface) error {
		if _, err := tx.GetQueries().Budgets(ctx, types.BudgetsParams{}); err != nil {
			return err
		}
		return tx.Transaction(ctx, func(nested TxInterface) error {
			rows, err := nested.GetQueries().Budgets(ctx, types.BudgetsParams{ParentId: food.Id})
			if err == nil && (len(rows) != 1 || rows[0].Id != *groceries.Id) {
				err = fmt.Errorf("budgets of food in a savepoint = %+v", rows)
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestOneRowQueriesOnPostgres runs the dashboard's reports, which share
// one filter, a string constant their SQL interpolates, one of them over
// the line_list projection's view; and the queries that return one row, or
// one or none, with none, one and more than one to read (D95).
func TestOneRowQueriesOnPostgres(t *testing.T) {
	ctx, db := openQueries(t)
	checking, err := db.Account.CreateOne(ctx, &types.Account{Name: "Checking"})
	if err != nil {
		t.Fatal(err)
	}
	food, err := db.Category.CreateOne(ctx, &types.Category{Name: "Food"})
	if err != nil {
		t.Fatal(err)
	}
	line := func(date, name string, amount int64, category *types.Category) *types.LineItem {
		t.Helper()
		created, err := db.LineItem.CreateOne(ctx, &types.LineItem{Date: at(t, date), Name: name, Amount: types.FinanceSignedMoney(amount), Account: *checking, Category: category})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return created
	}
	market := line("2026-01-05T10:00:00Z", "Market", -4000, food)
	line("2026-01-15T08:00:00Z", "Salary", 250000, nil)
	filter := types.DashboardFilter{Start: at(t, "2026-01-01T00:00:00Z"), End: at(t, "2026-01-31T23:59:59Z")}

	// The one row, of a filter that holds lines and of one that holds none.
	totals, err := db.Queries.DashboardTotals(ctx, filter)
	if err != nil || totals != (types.DashboardTotalsRow{Spent: -4000, Earned: 250000, Lines: 2}) {
		t.Fatalf("dashboard totals = %+v, %v", totals, err)
	}
	nothing := filter
	nothing.Search = "nothing holds this"
	if empty, err := db.Queries.DashboardTotals(ctx, nothing); err != nil || empty != (types.DashboardTotalsRow{}) {
		t.Fatalf("dashboard totals of no lines = %+v, %v", empty, err)
	}
	err = db.Transaction(ctx, func(tx TxInterface) error {
		inTx, err := tx.GetQueries().DashboardTotals(ctx, filter)
		if err == nil && inTx != totals {
			err = fmt.Errorf("dashboard totals in a transaction = %+v", inTx)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// The rows, through the same filter: grouped, and over the view.
	categories, err := db.Queries.DashboardCategories(ctx, filter)
	if err != nil || len(categories) != 2 || categories[0].Total != -4000 || categories[0].CategoryId == nil || *categories[0].CategoryId != *food.Id || categories[1].CategoryId != nil {
		t.Fatalf("dashboard categories = %+v, %v", categories, err)
	}
	listed, err := db.Queries.DashboardLines(ctx, filter)
	if err != nil || len(listed) != 2 || listed[0].Name != "Salary" || listed[0].CategoryName != nil || listed[1].AccountName != "Checking" || listed[1].CategoryName == nil || *listed[1].CategoryName != "Food" {
		t.Fatalf("dashboard lines = %+v, %v", listed, err)
	}

	// One row: ErrNotFound for none, ErrTooManyRows for more than one.
	account, err := db.Queries.AccountNamed(ctx, types.AccountNamedParams{Name: "Checking"})
	if err != nil || account.Id != *checking.Id {
		t.Fatalf("the account named Checking = %+v, %v", account, err)
	}
	if _, err := db.Queries.AccountNamed(ctx, types.AccountNamedParams{Name: "Nobody"}); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "query accountNamed returns one row, and its statement read none") {
		t.Fatalf("no account = %v", err)
	}
	for range 2 {
		if _, err := db.Account.CreateOne(ctx, &types.Account{Name: "Joint"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Queries.AccountNamed(ctx, types.AccountNamedParams{Name: "Joint"}); !errors.Is(err, ErrTooManyRows) || !strings.Contains(err.Error(), "query accountNamed returns one row, and its statement read 2") {
		t.Fatalf("two accounts = %v", err)
	}

	// One row or none: nil for none, ErrTooManyRows for more than one.
	matching := func(name string) types.MatchingLineParams {
		return types.MatchingLineParams{Start: filter.Start, End: filter.End, Name: name}
	}
	found, err := db.Queries.MatchingLine(ctx, matching("Market"))
	if err != nil || found == nil || found.Id != *market.Id || found.Amount != -4000 {
		t.Fatalf("the line named Market = %+v, %v", found, err)
	}
	if none, err := db.Queries.MatchingLine(ctx, matching("Rent")); none != nil || err != nil {
		t.Fatalf("no line named Rent = %+v, %v", none, err)
	}
	line("2026-01-20T08:00:00Z", "Coffee", -300, nil)
	line("2026-01-21T08:00:00Z", "Coffee", -350, nil)
	if _, err := db.Queries.MatchingLine(ctx, matching("Coffee")); !errors.Is(err, ErrTooManyRows) || !strings.Contains(err.Error(), "query matchingLine returns one row or none, and its statement read 2") {
		t.Fatalf("two lines named Coffee = %v", err)
	}
}

// TestDeclaredQueriesNoOp: the no-op database's queries read no rows, so
// one that returns one row fails as it does on none, and one that returns
// one or none answers nil (D95).
func TestDeclaredQueriesNoOp(t *testing.T) {
	queries := NewNoOpDatabase().GetQueries()
	rows, err := queries.Budgets(context.Background(), types.BudgetsParams{})
	if err != nil || len(rows) != 0 {
		t.Fatalf("no-op budgets = %v, %v", rows, err)
	}
	if _, err := queries.AccountNamed(context.Background(), types.AccountNamedParams{Name: "Checking"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no-op account named = %v", err)
	}
	if line, err := queries.MatchingLine(context.Background(), types.MatchingLineParams{Name: "Market"}); line != nil || err != nil {
		t.Fatalf("no-op matching line = %+v, %v", line, err)
	}
}
`
