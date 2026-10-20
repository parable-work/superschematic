package verify

import (
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTheSearchTextColumnIsSqlgens: the check names the column sqlgen adds.
func TestTheSearchTextColumnIsSqlgens(t *testing.T) {
	if searchTextColumn != sqlgen.SearchTextColumn {
		t.Fatalf("searchTextColumn = %q, sqlgen.SearchTextColumn = %q", searchTextColumn, sqlgen.SearchTextColumn)
	}
}

// searchColumnSchema has Review, a table whose title is a search field, with
// the extra fields given.
func searchColumnSchema(fields ...*ir.FieldDef) *ir.Schema {
	str := ir.TypeRef{Name: "string"}
	schema := ir.NewSchema("shop-db", ir.SchemaKindDB)
	schema.Types["Product"] = &ir.TypeDef{Name: "Product", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
	}}
	schema.Types["Review"] = &ir.TypeDef{Name: "Review", Role: ir.RoleDBTable, Fields: append([]*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "title", TypeRef: str, Required: true, SearchField: true},
	}, fields...)}
	return schema
}

func searchColumnErrors(schema *ir.Schema) []string {
	r := &Result{}
	checkSearchColumns(schema, r)
	return errorStrings(r)
}

// TestASearchTextColumnBesideSearchFieldsIsRefused: a field whose column is
// search_text, on a table with search fields, would be a second column of
// the name @searchField generates, and the ORM filter's second SearchText.
func TestASearchTextColumnBesideSearchFieldsIsRefused(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	for _, tc := range []struct {
		name  string
		field *ir.FieldDef
		want  []string
	}{
		{"searchText", &ir.FieldDef{Name: "searchText", TypeRef: str},
			[]string{"Review.searchText: its column is search_text, the column @searchField generates"}},
		{"search_text", &ir.FieldDef{Name: "search_text", TypeRef: str},
			[]string{"Review.search_text: its column is search_text, the column @searchField generates"}},
		{"a search field itself", &ir.FieldDef{Name: "searchText", TypeRef: str, SearchField: true},
			[]string{"Review.searchText: its column is search_text, the column @searchField generates"}},
		{"a @jsonField", &ir.FieldDef{Name: "searchText", TypeRef: ir.TypeRef{Name: "Product"}, JsonField: true},
			[]string{"Review.searchText: its column is search_text, the column @searchField generates"}},
		// A to-one relation's column is search_text_id.
		{"a relation", &ir.FieldDef{Name: "searchText", TypeRef: ir.TypeRef{Name: "Product"}, Relation: &ir.RelationDef{Type: "Product"}}, nil},
		{"another name", &ir.FieldDef{Name: "searchTexts", TypeRef: str}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := searchColumnErrors(searchColumnSchema(tc.field)); !slices.Equal(got, tc.want) {
				t.Errorf("errors = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestASearchTextColumnWithoutSearchFieldsVerifies: a table without search
// fields has no generated search_text, so its own field may take the name.
// A search field on a relation is no column of its own, as sqlgen counts
// search fields, so it does not make the table searched.
func TestASearchTextColumnWithoutSearchFieldsVerifies(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	schema := searchColumnSchema()
	schema.Types["Note"] = &ir.TypeDef{Name: "Note", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "product", TypeRef: ir.TypeRef{Name: "Product"}, Relation: &ir.RelationDef{Type: "Product"}, SearchField: true},
		{Name: "searchText", TypeRef: str},
	}}
	if got := searchColumnErrors(schema); len(got) != 0 {
		t.Errorf("errors = %q, want none", got)
	}
}

// TestASearchTextColumnIsRefusedOnTheTableThatExtendsIt: a base class gets
// no table; its fields are copied onto each table that extends it, which is
// where the clash is.
func TestASearchTextColumnIsRefusedOnTheTableThatExtendsIt(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	schema := ir.NewSchema("shop-db", ir.SchemaKindDB)
	base := []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "searchText", TypeRef: str},
	}
	schema.Types["Document"] = &ir.TypeDef{Name: "Document", Role: ir.RoleDBTable, Fields: base}
	schema.Types["Article"] = &ir.TypeDef{Name: "Article", Role: ir.RoleDBTable, Extends: "Document", Fields: append(slices.Clone(base),
		&ir.FieldDef{Name: "body", TypeRef: str, Required: true, SearchField: true})}
	want := []string{"Article.searchText: its column is search_text, the column @searchField generates"}
	if got := searchColumnErrors(schema); !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}
}

// TestRunRefusesASearchTextColumn: the full pass runs the check.
func TestRunRefusesASearchTextColumn(t *testing.T) {
	schema := searchColumnSchema(&ir.FieldDef{Name: "searchText", TypeRef: ir.TypeRef{Name: "string"}})
	want := "Review.searchText: its column is search_text, the column @searchField generates"
	if got := errorStrings(Run(schema, Input{})); !slices.Contains(got, want) {
		t.Errorf("Run errors = %q, want them to hold %q", got, want)
	}
}
