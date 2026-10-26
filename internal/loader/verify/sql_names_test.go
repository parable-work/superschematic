package verify

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// namingSchema has LineItem, a table, and Category, which it relates to,
// edited by edit.
func namingSchema(edit func(s *ir.Schema)) *ir.Schema {
	str := ir.TypeRef{Name: "string"}
	schema := ir.NewSchema("ledger-db", ir.SchemaKindDB)
	schema.Types["Category"] = &ir.TypeDef{Name: "Category", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "label", TypeRef: str, Required: true},
	}}
	schema.Types["LineItem"] = &ir.TypeDef{Name: "LineItem", Role: ir.RoleDBTable, SQLName: "lineItem", Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: str, Required: true, Key: true},
		{Name: "memo", TypeRef: str, SQLName: "memoText"},
		{Name: "category", TypeRef: ir.TypeRef{Name: "Category"}, SQLName: "categoryId"},
	}}
	if edit != nil {
		edit(schema)
	}
	return schema
}

func namingErrors(schema *ir.Schema) []string {
	r := &Result{}
	checkSQLNames(schema, r)
	checkProjections(schema, r)
	return errorStrings(r)
}

// TestSQLNamesLoad: @table on a table and @column on its fields, a
// relation's foreign key among them, load (D85).
func TestSQLNamesLoad(t *testing.T) {
	if errs := namingErrors(namingSchema(nil)); len(errs) != 0 {
		t.Fatalf("errors = %v", errs)
	}
	aliased := namingSchema(func(s *ir.Schema) { s.Types["LineItem"].Fields[1].ProjectedFrom = "base.memo" })
	if errs := namingErrors(aliased); !slices.ContainsFunc(errs, func(e string) bool {
		return strings.Contains(e, "LineItem.memo: @column of an alias.field or a function is only valid on a @projection class; a table's field takes a column name, a SQL identifier with no dot")
	}) {
		t.Errorf("a table field's alias.field: errors = %v", errs)
	}
}

// TestSQLNamesRefusals: an override where the generators do not name a
// table or a column through sqlutil, and two names that collide, fail the
// load, naming the fix (D85).
func TestSQLNamesRefusals(t *testing.T) {
	str := ir.TypeRef{Name: "string"}
	for _, tc := range []struct {
		name string
		edit func(s *ir.Schema)
		want string
	}{
		{"two tables of one name", func(s *ir.Schema) { s.Types["Category"].SQLName = "lineItem" },
			"LineItem: its table is lineItem, the table of Category; give one of them another name with @table"},
		{"two columns of one name", func(s *ir.Schema) { s.Types["LineItem"].Fields[1].SQLName = "categoryId" },
			"LineItem.category: its column is categoryId, the column of LineItem.memo; give one of them another name with @column"},
		{"the version column", func(s *ir.Schema) {
			s.Types["LineItem"].Optimistic = true
			s.Types["LineItem"].Fields[1].SQLName = "_version"
		}, "LineItem.memo: its column is _version, the column a @versioned or @optimistic table keeps its version in"},
		{"a generated id", func(s *ir.Schema) {
			s.Types["LineItem"].Fields[0].Key = false
			s.Types["LineItem"].Fields[0].Name = "code"
			s.Types["LineItem"].Fields[1].SQLName = "id"
		}, "LineItem.memo: its column is id, the column the generator adds a table without a @key"},
		{"a list relation", func(s *ir.Schema) {
			s.Types["Category"].Fields = append(s.Types["Category"].Fields, &ir.FieldDef{Name: "lines", TypeRef: ir.TypeRef{Name: "LineItem", IsArray: true}, HasMany: true, SQLName: "lineIds"})
		}, "Category.lines: @column names a column, and a list relation has none"},
		{"a queue", func(s *ir.Schema) {
			s.Types["Posted"] = &ir.TypeDef{Name: "Posted", Role: ir.RoleEmbeddedStruct, Queue: &ir.QueueDef{}, SQLName: "postedQueue"}
		}, "Posted: @table names a table, and Posted is a @queue, whose table is named after it (support follows)"},
		{"a version graph", func(s *ir.Schema) {
			s.Types["Category"].VersionGraph = &ir.VersionGraphConfig{}
			s.Types["Category"].SQLName = "cat"
		},
			"a version graph's runtimes name its tables themselves (support follows)"},
		{"an engine type", func(s *ir.Schema) {
			s.Types["Category"].Behaviors = []ir.BehaviorRef{{Name: "Workflow"}}
			s.Types["Category"].Fields[1].SQLName = "labelText"
		}, "Category.label: @column names a column the ORMs keep, and Category: it composes behaviors"},
		{"a @jsonField type", func(s *ir.Schema) {
			s.Types["Address"] = &ir.TypeDef{Name: "Address", Role: ir.RoleDBTable, JsonField: true, SQLName: "address", Fields: []*ir.FieldDef{{Name: "line", TypeRef: str}}}
		}, "Address: @table names a table, and Address is none"},
		{"an API", func(s *ir.Schema) { s.Kind = ir.SchemaKindAPI },
			"LineItem: @table names a DB table, and ledger-db is a schema of kind API"},
		{"a projection column", func(s *ir.Schema) {
			s.Types["Report"] = &ir.TypeDef{Name: "Report", Role: ir.RoleProjection, Fields: []*ir.FieldDef{{Name: "label", TypeRef: str, SQLName: "labelText"}}}
		}, "Report.label: a projection column's @column names the alias.field it reads, such as base.label"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := namingErrors(namingSchema(tc.edit))
			if !slices.ContainsFunc(errs, func(e string) bool { return strings.Contains(e, tc.want) }) {
				t.Fatalf("errors = %v, want one that says %q", errs, tc.want)
			}
		})
	}
}
