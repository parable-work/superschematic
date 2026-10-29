package sqlutil_test

import (
	"path/filepath"
	"slices"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
)

// financesTables models fixture-finances-db's tables and its views.
func financesTables(t *testing.T) (map[string]*TableModel, []TableModel) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-finances-db"))
	if err != nil {
		t.Fatal(err)
	}
	tables, err := TablesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]*TableModel{}
	for i := range tables {
		out[tables[i].Type] = &tables[i]
	}
	views, err := ViewsOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	return out, views
}

// TestRelationsOfAFinanceSchema holds the relations both ORMs include and
// filter through (D90): a table's to-one relations in column order, each a
// probe of the target's keys that leaves a soft-deletable target's deleted
// rows out, and its one-to-many ones, a @hasMany field first, then the
// references no field holds, named for their source in the plural, and by
// the field too where a source points at the table twice.
func TestRelationsOfAFinanceSchema(t *testing.T) {
	tables, _ := financesTables(t)
	line := tables["LineItem"]
	var fields []string
	for _, rel := range line.ToOne {
		fields = append(fields, rel.Field)
	}
	if want := []string{"account", "category", "subCategory", "business", "user"}; !slices.Equal(fields, want) {
		t.Fatalf("LineItem's to-one relations = %v, want %v", fields, want)
	}
	account, category := line.ToOneOf("account"), line.ToOneOf("category")
	if account.Optional || account.Nullable || account.TargetLive != "" {
		t.Errorf("LineItem.account = %+v, want one row: required, and Account keeps no deleted rows", account)
	}
	if !category.Optional || !category.Nullable || category.TargetLive != "category.deleted_at IS NULL" {
		t.Errorf("LineItem.category = %+v, want one or none, its deleted rows left out", category)
	}
	if category.Probe != "line_item.category_id IN (SELECT category.id FROM category%s)" {
		t.Errorf("LineItem.category's probe = %q", category.Probe)
	}
	if parent := tables["Category"].ToOneOf("parent"); parent.Probe != "category.parent_id IN (SELECT category.id FROM category%s)" {
		t.Errorf("Category.parent's probe = %q, which reads its own level's rows", parent.Probe)
	}
	many := func(table string) []string {
		var names []string
		for _, rel := range tables[table].ToMany {
			names = append(names, rel.Name)
		}
		return names
	}
	if got, want := many("LineItem"), []string{"notes"}; !slices.Equal(got, want) {
		t.Errorf("LineItem's one-to-many relations = %v, want %v", got, want)
	}
	if got, want := many("Category"), []string{"categories", "lineItemsByCategory", "lineItemsBySubCategory"}; !slices.Equal(got, want) {
		t.Errorf("Category's one-to-many relations = %v, want %v", got, want)
	}
	// Account's line items are its @hasMany field, which holds the
	// reference of LineItem.account: no second name for them.
	if got, want := many("Account"), []string{"lineItems"}; !slices.Equal(got, want) {
		t.Errorf("Account's one-to-many relations = %v, want %v", got, want)
	}
	if lines := tables["Account"].ToManyNamed("lineItems"); !lines.HasMany || lines.Field != "account" || lines.Column != "account_id" {
		t.Errorf("Account.lineItems = %+v", lines)
	}
	s := TableStatementsOf(line)
	if s.SelectColumn != `"line_item"."%s"` {
		t.Errorf("LineItem's selectColumn = %q", s.SelectColumn)
	}
	if c := PostgresClauses(); c.Or != " OR " || c.Group != "(%s)" {
		t.Errorf("the anyOf clauses = %q and %q", c.Or, c.Group)
	}
}

// TestViewsOfAProjection holds a projection as the ORMs read it (D90): its
// view beside the tables, its columns in the declaration's order named in
// snake case, filtered and sorted as a table's columns of their types,
// keyed by the column that reads its source's key, and its statements the
// reads TableStatementsOf writes over the view; and its search over its
// source's search column (D99), one probe of the source's keys.
func TestViewsOfAProjection(t *testing.T) {
	tables, views := financesTables(t)
	if len(views) != 1 || views[0].Type != "TransactionListRow" {
		t.Fatalf("views = %+v, want TransactionListRow", views)
	}
	view := views[0]
	if view.Name != "transaction_list" || view.Quoted != "transaction_list" || view.Key != "id" || view.KeyField != "id" {
		t.Errorf("the view = %s (%s), keyed by %s", view.Name, view.Quoted, view.Key)
	}
	kinds := map[string]FilterKind{}
	sorts := map[string]bool{}
	for _, c := range view.Columns {
		kinds[c.Field] = c.Filter
		sorts[c.Field] = c.Sortable
	}
	for field, want := range map[string]FilterKind{"id": FilterUUID, "date": FilterDateTime, "amount": FilterInt, "accountName": FilterString, "parentCategoryId": FilterUUID} {
		if kinds[field] != want {
			t.Errorf("%s filters as %q, want %q", field, kinds[field], want)
		}
	}
	if !sorts["date"] || !sorts["amount"] || sorts["categoryName"] {
		t.Errorf("the view sorts by %v: a nullable column is no sort", sorts)
	}
	if c := view.Column("parent_category_name"); c == nil || c.Qualified != "transaction_list.parent_category_name" || c.Required {
		t.Errorf("parentCategoryName's column = %+v", c)
	}
	s := TableStatementsOf(&view)
	if s.GetOne != `SELECT "id", "date", "name", "description", "amount", "account_id", "account_name", "provider_name", "category_id", "category_name", "category_color", "parent_category_id", "parent_category_name", "business_id", "business_name", "user_id", "user_name" FROM transaction_list WHERE id = $1` {
		t.Errorf("the view's getOne = %q", s.GetOne)
	}
	if s.Page != "SELECT %s FROM transaction_list%s%s LIMIT %d" {
		t.Errorf("the view's page = %q", s.Page)
	}
	if line := tables["LineItem"]; !line.Search || line.SearchColumn != "line_item.search_text" || line.SearchProbe != "" {
		t.Errorf("LineItem's search = %v %q %q, want its own column and no probe", line.Search, line.SearchColumn, line.SearchProbe)
	}
	if !view.Search || view.SearchColumn != "line_item.search_text" {
		t.Errorf("the view's search = %v %q, want LineItem's column", view.Search, view.SearchColumn)
	}
	if view.SearchProbe != "transaction_list.id IN (SELECT line_item.id FROM line_item%s)" {
		t.Errorf("the view's search probe = %q", view.SearchProbe)
	}
	if got := SearchConditionOf(&view); got != `line_item.search_text ILIKE $%d ESCAPE '\'` {
		t.Errorf("a word of the view's search = %q", got)
	}
}
