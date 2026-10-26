package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// querySchema is a DB schema with a table and a query over it, which
// checkQueries accepts: its params pick an order, and its row reads two
// columns, one through @column.
func querySchema() *ir.Schema {
	field := func(name, typ string, required bool) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: typ}, Required: required}
	}
	key := field("id", "string", true)
	key.Key = true
	total := field("total", "number", true)
	total.SQLName = "sum_amount"
	return &ir.Schema{
		Name: "ledger-db",
		Kind: ir.SchemaKindDB,
		Enums: map[string]*ir.EnumDef{
			"Order": {Name: "Order", Values: []ir.EnumValueDef{{Name: "Name", SerializedAs: "name"}, {Name: "Amount", SerializedAs: "amount"}}},
		},
		Types: map[string]*ir.TypeDef{
			"Line": {Name: "Line", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{key, field("name", "string", true), field("amount", "number", true)}},
			"TotalsParams": {Name: "TotalsParams", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
				field("prefix", "string", false), field("order", "Order", true),
			}},
			"TotalsRow": {Name: "TotalsRow", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{field("name", "string", true), total}},
			"Totals": {Name: "Totals", Role: ir.RoleQuery, Query: &ir.QueryDef{
				Name:   "totals",
				Params: "TotalsParams",
				Row:    "TotalsRow",
				SQL:    "SELECT name, SUM(amount) AS sum_amount FROM line WHERE (:prefix::text IS NULL OR name LIKE :prefix || '%') GROUP BY name ORDER BY :order",
				NotNull: []string{"total"},
				OrderBy: &ir.QueryOrderBy{Param: "order", Options: map[string]string{"name": "name", "amount": "SUM(amount) DESC"}},
			}},
		},
	}
}

// TestCheckQueries: a query's declaration holds in every form, or the load
// fails naming the query and the fix (D86).
func TestCheckQueries(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(s *ir.Schema)
		want string
	}{
		{"a query that holds", func(*ir.Schema) {}, ""},
		{"a parameter no field declares", func(s *ir.Schema) {
			s.Types["Totals"].Query.SQL += " LIMIT :limit"
		}, "query totals uses :limit, which is no field of its params type TotalsParams"},
		{"a parameter the SQL does not use", func(s *ir.Schema) {
			s.Types["TotalsParams"].Fields = append(s.Types["TotalsParams"].Fields, &ir.FieldDef{Name: "since", TypeRef: ir.TypeRef{Name: "string"}})
		}, "query totals does not use its parameter since; write :since in its SQL"},
		{"a name in a string, a quoted identifier, a comment and a cast is no parameter", func(s *ir.Schema) {
			s.Types["Totals"].Query.SQL = `SELECT name, SUM(amount)::bigint AS sum_amount, ':x' AS "a:b" FROM line -- :y
WHERE (:prefix::text IS NULL OR name LIKE :prefix) /* :z */ GROUP BY name ORDER BY :order`
		}, ""},
		{"a positional parameter", func(s *ir.Schema) {
			s.Types["Totals"].Query.SQL = strings.Replace(s.Types["Totals"].Query.SQL, ":prefix::text", "$1::text", 1)
		}, "the SQL holds the positional parameter $1; name each parameter :field"},
		{"two statements", func(s *ir.Schema) {
			s.Types["Totals"].Query.SQL += "; DELETE FROM line"
		}, "the SQL holds more than one statement"},
		{"a name that is no identifier", func(s *ir.Schema) {
			s.Types["Totals"].Query.Name = "Totals-Report"
		}, `query Totals-Report's name "Totals-Report" is no lower camel case identifier`},
		{"a query class with fields", func(s *ir.Schema) {
			s.Types["Totals"].Fields = []*ir.FieldDef{{Name: "x", TypeRef: ir.TypeRef{Name: "string"}}}
		}, "the class of query totals holds no fields"},
		{"a row type that is a table", func(s *ir.Schema) {
			s.Types["Totals"].Query.Row = "Line"
		}, "Line: it is the row type of query totals, so it is an embedded struct of its own"},
		{"a row field of a table's type", func(s *ir.Schema) {
			s.Types["TotalsRow"].Fields = append(s.Types["TotalsRow"].Fields, &ir.FieldDef{Name: "line", TypeRef: ir.TypeRef{Name: "Line"}})
		}, "TotalsRow.line: it is a field of the row type of query totals, and is a relation"},
		{"a key on a params field", func(s *ir.Schema) {
			s.Types["TotalsParams"].Fields[0].Key = true
		}, "TotalsParams.prefix: it is a field of the params type of query totals, which no table stores"},
		{"@column on a parameter", func(s *ir.Schema) {
			s.Types["TotalsParams"].Fields[0].SQLName = "pre"
		}, "TotalsParams.prefix: @column names the result column a row field reads"},
		{"a row field the query does not have", func(s *ir.Schema) {
			s.Types["Totals"].Query.NotNull = []string{"count"}
		}, "query totals says count is never null, and its row type TotalsRow has no such field"},
		{"an order by a string", func(s *ir.Schema) {
			s.Types["TotalsParams"].Fields[1].TypeRef.Name = "string"
		}, "query totals orders by order, which is of type string; the order's parameter is an enum"},
		{"an order without an option for a value", func(s *ir.Schema) {
			delete(s.Types["Totals"].Query.OrderBy.Options, "amount")
		}, "query totals's order has no option for amount, a value of Order"},
		{"an order option no value names", func(s *ir.Schema) {
			s.Types["Totals"].Query.OrderBy.Options["date"] = "name DESC"
		}, "query totals's order option date is no value of Order"},
		{"an order fragment with a parameter", func(s *ir.Schema) {
			s.Types["Totals"].Query.OrderBy.Options["name"] = "name, :prefix"
		}, "query totals's order option name is a fixed fragment of ORDER BY's terms"},
		{"a nullable order without a default", func(s *ir.Schema) {
			s.Types["TotalsParams"].Fields[1].Required = false
		}, `query totals orders by order, which may be null; name the option a null picks, as default: "amount"`},
		{"an order where a value goes", func(s *ir.Schema) {
			s.Types["Totals"].Query.SQL = strings.Replace(s.Types["Totals"].Query.SQL, "ORDER BY :order", "ORDER BY name LIMIT :order", 1)
		}, "query totals writes :order where a value goes"},
		{"a page and an order", func(s *ir.Schema) {
			s.Types["Totals"].Query.Page = &ir.QueryPage{Key: []string{"name"}}
		}, "query totals takes an orderBy and a page"},
		{"a page by a nullable field", func(s *ir.Schema) {
			q := s.Types["Totals"].Query
			q.OrderBy = nil
			q.SQL = strings.Replace(q.SQL, " ORDER BY :order", "", 1)
			s.Types["TotalsParams"].Fields = s.Types["TotalsParams"].Fields[:1]
			s.Types["TotalsRow"].Fields[0].Required = false
			q.Page = &ir.QueryPage{Key: []string{"name", "nope"}}
		}, "query totals pages by name, which may be null"},
		{"a dialect that is no dialect", func(s *ir.Schema) {
			s.Types["Totals"].Query.Dialects = []string{"postgres", "mysql"}
		}, `query totals names the dialect "mysql"`},
		{"a query on sqlite alone", func(s *ir.Schema) {
			s.Types["Totals"].Query.Dialects = []string{"sqlite"}
		}, "query totals runs on sqlite alone; a query is verified and run on Postgres"},
		{"two queries of one name", func(s *ir.Schema) {
			other := *s.Types["Totals"]
			other.Name = "Totals2"
			query := *other.Query
			other.Query = &query
			s.Types["Totals2"] = &other
		}, "two queries are named totals, on Totals and Totals2"},
		{"a query in an API schema", func(s *ir.Schema) {
			s.Kind = ir.SchemaKindAPI
		}, "@query declares a query of a database, and ledger-db is a schema of kind API"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := querySchema()
			tc.edit(schema)
			r := &Result{}
			checkQueries(schema, r)
			err := r.Err()
			switch {
			case tc.want == "" && err != nil:
				t.Fatalf("%v", err)
			case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
				t.Fatalf("%v, want %q", err, tc.want)
			}
		})
	}
}

// TestColumnOnARowField: @column names the result column a query's row
// field reads (D86), and no table's.
func TestColumnOnARowField(t *testing.T) {
	r := &Result{}
	checkSQLNames(querySchema(), r)
	if err := r.Err(); err != nil {
		t.Fatalf("@column on a row field: %v", err)
	}
}
