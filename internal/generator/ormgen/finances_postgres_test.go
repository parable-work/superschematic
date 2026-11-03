package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// financesFixture is the dashboard fixture both ORMs read (D90): line items
// on accounts at providers, under categories with parents, a business and
// a person, with notes, and the TransactionListRow projection.
const financesFixture = "fixture-finances-db"

// TestFinancesOnPostgres generates the ORM module for fixture-finances-db,
// then builds, vets and tests it. Its generated test, against the Postgres
// at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL (it skips without the
// database), reads the dashboard's transaction list (D90): a filter through
// a to-one relation, a category's parent, which leaves out a row whose
// category is null or soft-deleted; AnyOf, the category or its parent, and
// an AnyOf with a branch of no condition, which keeps every row; the whole
// list as one FindPage, a date range, a business, a person, an amount
// range and a search, ordered by date, two pages, each row with its
// account and provider, its category and parent, its business and its
// person loaded; a @hasMany include of an account's line items; and the
// TransactionListRow projection's FindPage, filtered on joined columns, its
// next page, GetOne, and FindMany with its count and without (D89). Then
// AllOf (D99): two OR groups ANDed, the category or its parent and the name
// or the description, which find what the four branches multiplied out
// find; AllOf in an AnyOf branch and in an AllOf; an empty AllOf and one of
// empty filters, which keep every row; an AnyOf branch of no condition in
// an AllOf, whose dropped value leaves the next filter's placeholders
// numbered right; UpdateMany and DeleteMany by an AllOf; and on the
// projection, AllOf, and SearchText over LineItem's search fields, alone,
// beside AnyOf and in FindPage.
func TestFinancesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	ormDir := generateORMModule(t, financesFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "finances_test.go"), []byte(financesORMTest), 0o644); err != nil {
		t.Fatalf("write finances test: %v", err)
	}
	out := runORMModule(t, ormDir)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" && !strings.Contains(out, "--- PASS: TestFinancesOnPostgres") {
		t.Fatalf("the generated ORM did not run TestFinancesOnPostgres:\n%s", out)
	}
}

var financesORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-finances-db"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestFinancesOnPostgres(t *testing.T) {` + strings.Replace(pageTestSetup, "PREFIX", "finances_orm", 1) + `
	alice, err := types.ParseIdentityUUID("TKbHsHDO6x9RCjbs5c2u4")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	day := func(d int) types.TemporalDateTime {
		return types.TemporalDateTime(time.Date(2026, 1, d, 12, 0, 0, 0, time.UTC))
	}

	ann, err := db.User.CreateOne(ctx, &types.User{Email: "ann@example.com", Name: "Ann", CreatedAt: day(1)})
	must(err)
	ben, err := db.User.CreateOne(ctx, &types.User{Email: "ben@example.com", Name: "Ben", CreatedAt: day(1)})
	must(err)
	bank, err := db.Provider.CreateOne(ctx, &types.Provider{Name: "First Bank", Slug: "first-bank"})
	must(err)
	shop, err := db.Business.CreateOne(ctx, &types.Business{Name: "Shop", Slug: "shop"})
	must(err)
	checking, err := db.Account.CreateOne(ctx, &types.Account{Name: "Checking", Slug: "checking", Provider: types.Provider{Id: bank.Id}, Owner: &types.User{Id: ann.Id}})
	must(err)
	category := func(name, slug string, parent *types.Category) *types.Category {
		t.Helper()
		c := &types.Category{Name: types.IdentityName(name), Slug: types.IdentitySlug(slug), Color: "#00aa00"}
		if parent != nil {
			c.Parent = &types.Category{Id: parent.Id}
		}
		created, err := db.Category.CreateOne(ctx, c)
		must(err)
		return created
	}
	food := category("Food", "food", nil)
	groceries := category("Groceries", "groceries", food)
	dining := category("Dining", "dining", food)
	travel := category("Travel", "travel", nil)
	retired := category("Retired", "retired", travel)

	line := func(d int, name string, amount int64, c *types.Category, business *types.Business, user *types.User) *types.LineItem {
		t.Helper()
		item := &types.LineItem{Date: day(d), Name: name, Amount: types.FinanceSignedMoney(amount), Account: types.Account{Id: checking.Id}, CreatedAt: day(d)}
		if c != nil {
			item.Category = &types.Category{Id: c.Id}
		}
		if business != nil {
			item.Business = &types.Business{Id: business.Id}
		}
		if user != nil {
			item.User = &types.User{Id: user.Id}
		}
		created, err := db.LineItem.CreateOne(ctx, item)
		must(err)
		return created
	}
	beans := line(5, "Coffee beans", -1250, groceries, shop, ann)
	lunch := line(6, "Lunch", -500, food, nil, ben)
	train := line(7, "Train", -9000, travel, nil, ann)
	dinner := line(8, "Dinner and coffee", -2000, dining, shop, ann)
	salary := line(9, "Salary", 10000, nil, nil, ann)
	old := line(10, "Old fare", -300, retired, nil, nil)
	_, err = db.Note.CreateOne(ctx, &types.Note{LineItem: types.LineItem{Id: beans.Id}, Content: "for the office", CreatedAt: day(5)})
	must(err)
	withdrawn, err := db.Note.CreateOne(ctx, &types.Note{LineItem: types.LineItem{Id: beans.Id}, Content: "withdrawn", CreatedAt: day(5)})
	must(err)
	must(db.Note.DeleteOne(ctx, *withdrawn.Id))
	must(db.Category.DeleteOne(ctx, *retired.Id))

	ids := func(rows []*types.LineItem) []string {
		var out []string
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}
	fields := LineItemFields{Id: true, Name: true, Date: true, Amount: true}
	find := func(filter *LineItemFilter) []string {
		t.Helper()
		rows, _, err := db.LineItem.FindMany(ctx, filter, &LineItemFindOptions{Fields: fields, OrderBy: []LineItemOrderBy{{Field: LineItemOrderByDate}}})
		must(err)
		return ids(rows)
	}
	in := func(cs ...*types.Category) *UUIDFilter {
		f := &UUIDFilter{}
		for _, c := range cs {
			f.In = append(f.In, *c.Id)
		}
		return f
	}

	// A filter through the relation keeps the rows whose category's parent
	// is food: not lunch, filed under food itself, nor salary, which has
	// no category.
	if got, want := find(&LineItemFilter{Category: &CategoryFilter{ParentID: in(food)}}), []string{"Coffee beans", "Dinner and coffee"}; !slices.Equal(got, want) {
		t.Errorf("under food's children: %v, want %v", got, want)
	}
	// A soft-deleted category is no live row the filter finds.
	if got := find(&LineItemFilter{Category: &CategoryFilter{ParentID: in(travel)}}); len(got) != 0 {
		t.Errorf("under travel's children: %v, want none: retired is deleted", got)
	}
	// The category or its parent: the dashboard's category filter.
	either := []LineItemFilter{{CategoryID: in(food)}, {Category: &CategoryFilter{ParentID: in(food)}}}
	if got, want := find(&LineItemFilter{AnyOf: either}), []string{"Coffee beans", "Lunch", "Dinner and coffee"}; !slices.Equal(got, want) {
		t.Errorf("food or under food: %v, want %v", got, want)
	}
	// A path two relations deep: the category's parent's name.
	if got, want := find(&LineItemFilter{Category: &CategoryFilter{Parent: &CategoryFilter{Name: &StringFilter{Eq: strPtr("Food")}}}}), []string{"Coffee beans", "Dinner and coffee"}; !slices.Equal(got, want) {
		t.Errorf("under a parent named Food: %v, want %v", got, want)
	}
	// A branch of no condition keeps every row, and its siblings' values go.
	if got := find(&LineItemFilter{AnyOf: []LineItemFilter{{CategoryID: in(travel)}, {}}, Amount: &Int64Filter{Lt: int64Ptr(0)}}); len(got) != 5 {
		t.Errorf("anything spent: %v, want every line but the salary", got)
	}

	// The dashboard's transaction list: one FindPage, its relations loaded.
	start, end := time.Time(day(1)), time.Time(day(31))
	search := "coffee"
	opts := &LineItemPageOptions{
		Fields: LineItemFields{
			Id: true, Date: true, Name: true, Description: true, Amount: true,
			AccountNested:  AccountFields{Id: true, Name: true, ProviderNested: ProviderFields{Id: true, Name: true}},
			CategoryNested: CategoryFields{Id: true, Name: true, Color: true, ParentNested: &CategoryFields{Id: true, Name: true}},
			BusinessNested: BusinessFields{Id: true, Name: true},
			UserNested:     UserFields{Id: true, Name: true},
		},
		Sort:  LineItemSort{Field: LineItemSortByDate, Desc: true},
		Limit: 1,
	}
	filter := &LineItemFilter{
		Date:       &DateTimeFilter{Gte: &start, Lt: &end},
		AnyOf:      either,
		BusinessID: &UUIDFilter{Eq: shop.Id},
		UserID:     &UUIDFilter{Eq: ann.Id},
		Amount:     &Int64Filter{Between: &Int64Range{Min: -5000, Max: 0}},
		SearchText: &search,
	}
	first, err := db.LineItem.FindPage(ctx, filter, opts)
	must(err)
	if len(first.Rows) != 1 || first.Rows[0].Name != "Dinner and coffee" || first.Next == nil {
		t.Fatalf("first page = %+v, want the dinner and a next key", first)
	}
	row := first.Rows[0]
	if row.Account.Name != "Checking" || row.Account.Provider.Name != "First Bank" {
		t.Errorf("the dinner's account = %+v, want Checking at First Bank", row.Account)
	}
	if row.Category == nil || row.Category.Name != "Dining" || row.Category.Parent == nil || row.Category.Parent.Name != "Food" {
		t.Errorf("the dinner's category = %+v, want Dining under Food", row.Category)
	}
	if row.Business == nil || row.Business.Name != "Shop" || row.User == nil || row.User.Name != "Ann" {
		t.Errorf("the dinner's business and person = %+v, %+v", row.Business, row.User)
	}
	opts.After = first.Next
	second, err := db.LineItem.FindPage(ctx, filter, opts)
	must(err)
	if len(second.Rows) != 1 || second.Rows[0].Name != "Coffee beans" || second.Next != nil {
		t.Fatalf("second page = %+v, want the beans and no next key", second)
	}
	if c := second.Rows[0].Category; c == nil || c.Name != "Groceries" || c.Parent == nil || c.Parent.Name != "Food" {
		t.Errorf("the beans' category = %+v, want Groceries under Food", c)
	}
	// A deleted category's line keeps its key, the relation's stub.
	got, err := db.LineItem.GetOne(ctx, *old.Id, &LineItemGetOptions{Fields: LineItemFields{Id: true, CategoryNested: CategoryFields{Id: true, Name: true}}})
	must(err)
	if got.Category == nil || got.Category.Name != "" || *got.Category.Id != *retired.Id {
		t.Errorf("the old fare's category = %+v, want the deleted category's key alone", got.Category)
	}

	// An account's line items, its @hasMany field.
	account, err := db.Account.GetOne(ctx, *checking.Id, &AccountGetOptions{Fields: AccountFields{Id: true, LineItemsNested: &LineItemFields{Id: true, Name: true}}})
	must(err)
	if len(account.LineItems) != 6 {
		t.Errorf("checking's line items = %d, want 6", len(account.LineItems))
	}

	// The flat list: the projection's page, filtered on joined columns.
	listed, err := db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{
		AnyOf:        []TransactionListRowFilter{{CategoryId: in(food)}, {ParentCategoryId: in(food)}},
		ProviderName: &StringFilter{Eq: strPtr("First Bank")},
	}, &TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 2})
	must(err)
	if len(listed.Rows) != 2 || listed.Next == nil {
		t.Fatalf("the list's first page = %+v, want two rows and a next key", listed)
	}
	top := listed.Rows[0]
	if top.Name != "Dinner and coffee" || top.CategoryName == nil || *top.CategoryName != "Dining" || top.ParentCategoryName == nil || *top.ParentCategoryName != "Food" || top.AccountName != "Checking" || top.UserName == nil || *top.UserName != "Ann" {
		t.Errorf("the list's first row = %+v", top)
	}
	rest, err := db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{
		AnyOf: []TransactionListRowFilter{{CategoryId: in(food)}, {ParentCategoryId: in(food)}},
	}, &TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 2, After: listed.Next})
	must(err)
	if len(rest.Rows) != 1 || rest.Rows[0].Name != "Coffee beans" || rest.Next != nil {
		t.Errorf("the list's second page = %+v, want the beans alone", rest)
	}
	one, err := db.TransactionListRow().GetOne(ctx, *salary.Id)
	must(err)
	if one.CategoryName != nil || one.Amount != 10000 || one.ProviderName != "First Bank" {
		t.Errorf("the salary's row = %+v", one)
	}
	all, total, err := db.TransactionListRow().FindMany(ctx, &TransactionListRowFilter{BusinessName: &StringFilter{IsNull: boolPtr(false)}}, &TransactionListRowFindOptions{OrderBy: []TransactionListRowOrderBy{{Field: TransactionListRowOrderByAmount}}})
	must(err)
	if total != 2 || len(all) != 2 || all[0].Name != "Dinner and coffee" {
		t.Errorf("the shop's rows = %d %+v", total, all)
	}
	// A projection's FindMany skips its count as a table's does (D89).
	uncounted, skipped, err := db.TransactionListRow().FindMany(ctx, nil, &TransactionListRowFindOptions{SkipCount: true})
	must(err)
	if skipped != -1 || len(uncounted) != 6 {
		t.Errorf("the list without its count = %d rows, total %d, want 6 rows and -1", len(uncounted), skipped)
	}
	if _, err := db.TransactionListRow().GetOne(ctx, *ben.Id); !errors.Is(err, ErrNotFound) {
		t.Errorf("a key no row holds = %v, want ErrNotFound", err)
	}
	must(db.Transaction(ctx, func(tx TxInterface) error {
		_, n, err := TransactionListRowIn(tx).FindMany(ctx, nil, nil)
		if err == nil && n != 6 {
			err = fmt.Errorf("the list holds %d rows in a transaction, want 6", n)
		}
		return err
	}))

	// Two OR groups ANDed (D99): the category is food or its parent is, and
	// the name or the description holds coffee. The lunch holds it in its
	// description, and so does the train, which is no food; the bread is
	// food without coffee.
	_, err = db.LineItem.UpdateOne(ctx, *lunch.Id, &LineItemUpdate{Description: strPtr("with a coffee")})
	must(err)
	_, err = db.LineItem.UpdateOne(ctx, *train.Id, &LineItemUpdate{Description: strPtr("coffee on board")})
	must(err)
	line(11, "Bread", -400, groceries, nil, ben)
	coffee := &StringFilter{ILike: strPtr("coffee")}
	holdsCoffee := []LineItemFilter{{Name: coffee}, {Description: coffee}}
	both := []LineItemFilter{{AnyOf: either}, {AnyOf: holdsCoffee}}
	foodWithCoffee := []string{"Coffee beans", "Lunch", "Dinner and coffee"}
	if got := find(&LineItemFilter{AllOf: both}); !slices.Equal(got, foodWithCoffee) {
		t.Errorf("food with coffee: %v, want %v", got, foodWithCoffee)
	}
	multiplied := []LineItemFilter{
		{CategoryID: in(food), Name: coffee},
		{CategoryID: in(food), Description: coffee},
		{Category: &CategoryFilter{ParentID: in(food)}, Name: coffee},
		{Category: &CategoryFilter{ParentID: in(food)}, Description: coffee},
	}
	if got := find(&LineItemFilter{AnyOf: multiplied}); !slices.Equal(got, foodWithCoffee) {
		t.Errorf("the four branches AllOf replaces: %v, want %v", got, foodWithCoffee)
	}
	// Nested: an AllOf in each AnyOf branch, and an AllOf in an AllOf.
	nested := &LineItemFilter{AnyOf: []LineItemFilter{
		{AllOf: []LineItemFilter{{CategoryID: in(travel)}, {AnyOf: holdsCoffee}}},
		{AllOf: []LineItemFilter{{AllOf: both}, {UserID: &UUIDFilter{Eq: ben.Id}}}},
	}}
	if got, want := find(nested), []string{"Lunch", "Train"}; !slices.Equal(got, want) {
		t.Errorf("travel with coffee, or Ben's food with coffee: %v, want %v", got, want)
	}
	// An empty AllOf, and one of filters of no condition, add none.
	if got := find(&LineItemFilter{AllOf: []LineItemFilter{}}); len(got) != 7 {
		t.Errorf("an empty AllOf: %v, want every line", got)
	}
	if got := find(&LineItemFilter{AllOf: []LineItemFilter{{}, {}}}); len(got) != 7 {
		t.Errorf("an AllOf of empty filters: %v, want every line", got)
	}
	// An AnyOf branch of no condition drops its siblings' values; the next
	// filter's placeholders follow the values kept.
	kept := &LineItemFilter{Amount: &Int64Filter{Lt: int64Ptr(0)}, AllOf: []LineItemFilter{{AnyOf: []LineItemFilter{{CategoryID: in(travel)}, {}}}, {AnyOf: holdsCoffee}}}
	if got, want := find(kept), []string{"Coffee beans", "Lunch", "Train", "Dinner and coffee"}; !slices.Equal(got, want) {
		t.Errorf("anything spent on coffee: %v, want %v", got, want)
	}
	// UpdateMany's placeholders follow its assignments'.
	n, err := db.LineItem.UpdateMany(ctx, &LineItemFilter{AllOf: both}, &LineItemUpdate{Description: strPtr("coffee, checked")})
	must(err)
	if n != 3 || !slices.Equal(find(&LineItemFilter{Description: &StringFilter{ILike: strPtr("checked")}}), foodWithCoffee) {
		t.Errorf("UpdateMany by AllOf updated %d rows, want the 3 lines of food with coffee", n)
	}

	// The projection: AllOf, and SearchText over LineItem's search fields.
	listNames := func(filter *TransactionListRowFilter) []string {
		t.Helper()
		rows, _, err := db.TransactionListRow().FindMany(ctx, filter, &TransactionListRowFindOptions{OrderBy: []TransactionListRowOrderBy{{Field: TransactionListRowOrderByDate}}})
		must(err)
		var out []string
		for _, row := range rows {
			out = append(out, row.Name)
		}
		return out
	}
	listCoffee := []TransactionListRowFilter{{Name: coffee}, {Description: coffee}}
	listFood := []TransactionListRowFilter{{CategoryId: in(food)}, {ParentCategoryId: in(food)}}
	if got := listNames(&TransactionListRowFilter{AllOf: []TransactionListRowFilter{{AnyOf: listFood}, {AnyOf: listCoffee}}}); !slices.Equal(got, foodWithCoffee) {
		t.Errorf("the list's food with coffee: %v, want %v", got, foodWithCoffee)
	}
	words := func(text string) *string { return &text }
	if got, want := listNames(&TransactionListRowFilter{SearchText: words("COFFEE")}), []string{"Coffee beans", "Lunch", "Train", "Dinner and coffee"}; !slices.Equal(got, want) {
		t.Errorf("the list's search for coffee: %v, want %v", got, want)
	}
	if got, want := listNames(&TransactionListRowFilter{SearchText: words("coffee board")}), []string{"Train"}; !slices.Equal(got, want) {
		t.Errorf("the list's search for two words: %v, want %v", got, want)
	}
	if got := listNames(&TransactionListRowFilter{SearchText: words("  ")}); len(got) != 7 {
		t.Errorf("the list's search for no word: %v, want every row", got)
	}
	searched, err := db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{SearchText: words("coffee"), AnyOf: listFood},
		&TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 2})
	must(err)
	if len(searched.Rows) != 2 || searched.Rows[0].Name != "Dinner and coffee" || searched.Rows[1].Name != "Lunch" || searched.Next == nil {
		t.Fatalf("the list's page of food with coffee = %+v", searched)
	}
	searched, err = db.TransactionListRow().FindPage(ctx, &TransactionListRowFilter{SearchText: words("coffee"), AnyOf: listFood},
		&TransactionListRowPageOptions{Sort: TransactionListRowSort{Field: TransactionListRowSortByDate, Desc: true}, Limit: 2, After: searched.Next})
	must(err)
	if len(searched.Rows) != 1 || searched.Rows[0].Name != "Coffee beans" || searched.Next != nil {
		t.Errorf("the list's next page of food with coffee = %+v", searched)
	}

	// DeleteMany by an AllOf: the notes of food lines whose content holds
	// office, a note being soft-deleted.
	deleted, err := db.Note.DeleteMany(ctx, &NoteFilter{AllOf: []NoteFilter{{LineItem: &LineItemFilter{AnyOf: either}}, {Content: &StringFilter{ILike: strPtr("office")}}}})
	must(err)
	if deleted != 1 {
		t.Errorf("DeleteMany by AllOf deleted %d notes, want the office note", deleted)
	}
	_ = []*types.LineItem{lunch, train, dinner}
}

func strPtr(s string) *string { return &s }
func int64Ptr(n int64) *int64 { return &n }
func boolPtr(b bool) *bool    { return &b }
`

// TestSettingBoundProjectionCompiles generates, builds and vets the ORM
// module for fixture-projection (D90), whose AppPreference view binds
// session settings and collapses by its key, slotKey: its repository reads
// an enum, a JSON value, a list and nullable columns as a table's does.
func TestSettingBoundProjectionCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	runORMModule(t, generateORMModule(t, "fixture-projection"))
}
