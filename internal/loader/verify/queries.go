package verify

import (
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// queryName is a query's function: a lower camel case identifier.
var queryName = regexp.MustCompile(`^[a-z][A-Za-z0-9]*$`)

// queryDialects are the dialects a query may name.
var queryDialects = []string{"postgres", "sqlite"}

// checkQueries checks a DB service's declared queries (D86) in every form,
// with no database: Postgres prepares each statement when `superschematic
// queries verify` runs, and the lock it writes holds what it described.
//
// A query's class holds no field. Its name is a lower camel case
// identifier no other query of the service takes. Its params and row are
// embedded structs of the schema, no table, whose fields hold values both
// ORMs bind and read (sqlutil.QueryValueProblem), the row's each from a
// column of its own. Its SQL is one statement whose :names are each a
// field of the params, which uses every one; its notNull names row fields;
// its orderBy's parameter is an enum of the schema, whose values are its
// options, each a fixed fragment; its page's key are required row fields
// of one value, and a paged query takes no orderBy; a query that returns
// one row, or one or none, is not paged and does not show it may return
// more (D95).
func checkQueries(schema *ir.Schema, r *Result) {
	var defs []*ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td.Role == ir.RoleQuery || td.Query != nil {
			defs = append(defs, td)
		}
	}
	if len(defs) == 0 {
		return
	}
	if schema.Kind != ir.SchemaKindDB {
		r.errorf(defs[0].Owner, "%s: @query declares a query of a database, and %s is a schema of kind %s", defs[0].Name, schema.Name, schema.Kind)
		return
	}
	before := len(r.Errors)
	names := map[string]string{}
	for _, td := range defs {
		def := td.Query
		if td.Role != ir.RoleQuery || def == nil {
			r.errorf(td.Owner, "%s has role %s and a query %v; a @query class has role %s and its query", td.Name, td.Role, def != nil, ir.RoleQuery)
			continue
		}
		where := "query " + def.Name
		if !queryName.MatchString(def.Name) {
			r.errorf(td.Owner, "%s: %s's name %q is no lower camel case identifier, as categoryTotals is; it is the query's function in both ORMs", td.Name, where, def.Name)
		}
		if other, taken := names[def.Name]; taken {
			r.errorf(td.Owner, "%s: two queries are named %s, on %s and %s; give one another name", td.Name, def.Name, other, td.Name)
		} else {
			names[def.Name] = td.Name
		}
		if len(td.Fields) > 0 {
			r.errorf(td.Owner, "%s: the class of %s holds no fields; its parameters are the fields of %s and its rows those of %s", td.Name, where, def.Params, def.Row)
		}
		for _, d := range def.Dialects {
			if !slices.Contains(queryDialects, d) {
				r.errorf(td.Owner, "%s: %s names the dialect %q; a query runs on postgres or sqlite", td.Name, where, d)
			}
		}
		if !def.RunsOn("postgres") {
			r.errorf(td.Owner, "%s: %s runs on %s alone; a query is verified and run on Postgres, so its dialects list postgres", td.Name, where, strings.Join(def.Dialects, ", "))
		}
		params := checkQueryType(schema, td, def.Params, "params", r)
		row := checkQueryType(schema, td, def.Row, "row", r)
		if params == nil || row == nil {
			continue
		}
		checkQueryRow(td, def, row, r)
		checkQuerySQL(schema, td, def, params, row, r)
	}
	if len(r.Errors) > before {
		return
	}
	// What the checks above hold, the model holds too: a failure here is a
	// gap in them.
	if _, err := sqlutil.QueriesOf(schema, nil); err != nil {
		r.errorf(defs[0].Owner, "%v", err)
	}
}

// checkQueryType checks the params or row type of query td, and returns it
// when it is one a query can take.
func checkQueryType(schema *ir.Schema, td *ir.TypeDef, name, what string, r *Result) *ir.TypeDef {
	where := "query " + td.Query.Name
	named := schema.Types[name]
	switch {
	case name == "":
		r.errorf(td.Owner, "%s: %s names no %s type", td.Name, where, what)
		return nil
	case named == nil:
		r.errorf(td.Owner, "%s: %s's %s type %s is not a type of the schema; declare it beside the query, as a class or an object type alias", td.Name, where, what, name)
		return nil
	case named.Role != ir.RoleEmbeddedStruct || named.IsTrait || named.Queue != nil || named.JsonField:
		r.errorf(named.Owner, "%s: it is the %s type of %s, so it is an embedded struct of its own: not a table, a trait, a queue's message or a @jsonField type", name, what, where)
		return nil
	case len(named.Indexes) > 0 || named.SQLName != "" || len(named.EnqueueOnChange) > 0 || named.Versioned || named.Optimistic || len(named.RawSQL) > 0:
		r.errorf(named.Owner, "%s: it is the %s type of %s, which no table stores; @index, @table, @enqueueOnChange, @versioned, @optimistic and @rawSql are a table's", name, what, where)
		return nil
	}
	ok := true
	for _, f := range named.Fields {
		if f == nil {
			continue
		}
		switch problem := sqlutil.QueryValueProblem(schema, f); {
		case f.Key || f.Unique || f.AutoGenerated || f.SearchField:
			r.errorf(named.Owner, "%s.%s: it is a field of the %s type of %s, which no table stores; @key, @unique, @searchField and AutoGenerate are a table's", name, f.Name, what, where)
			ok = false
		case problem != "":
			r.errorf(named.Owner, "%s.%s: it is a field of the %s type of %s, and is %s", name, f.Name, what, where, problem)
			ok = false
		case what == "params" && f.SQLName != "":
			r.errorf(named.Owner, "%s.%s: @column names the result column a row field reads; a parameter is :%s in the SQL", name, f.Name, f.Name)
			ok = false
		}
	}
	if !ok {
		return nil
	}
	return named
}

// checkQueryRow checks that each field of a query's row reads a column of
// its own.
func checkQueryRow(td *ir.TypeDef, def *ir.QueryDef, row *ir.TypeDef, r *Result) {
	columns := map[string]string{}
	for _, f := range row.Fields {
		if f == nil {
			continue
		}
		column := sqlutil.ColumnName(f)
		if other, taken := columns[column]; taken {
			r.errorf(row.Owner, "%s.%s: it reads the column %s of query %s, as %s does; name one of them another column with @column", def.Row, f.Name, column, def.Name, other)
			continue
		}
		columns[column] = f.Name
	}
	if len(columns) == 0 {
		r.errorf(td.Owner, "%s: the row type %s of query %s has no fields; a row holds a field per result column", td.Name, def.Row, def.Name)
	}
}

// checkQuerySQL checks a query's SQL against its params and row: the
// parameters it uses, its hints, its order and its page.
func checkQuerySQL(schema *ir.Schema, td *ir.TypeDef, def *ir.QueryDef, params, row *ir.TypeDef, r *Result) {
	where := "query " + def.Name
	scan, err := sqlutil.ScanQuery(def.SQL)
	if err != nil {
		r.errorf(td.Owner, "%s: %s: %v", td.Name, where, err)
		return
	}
	if strings.TrimSpace(def.SQL[:scan.End]) == "" {
		r.errorf(td.Owner, "%s: %s has no statement", td.Name, where)
		return
	}
	used := map[string]bool{}
	for _, use := range scan.Params {
		used[use.Name] = true
		if sqlutil.FieldNamed(params, use.Name) == nil {
			r.errorf(td.Owner, "%s: %s uses :%s, which is no field of its params type %s; declare it there, or spell it as a field", td.Name, where, use.Name, def.Params)
		}
	}
	for _, f := range params.Fields {
		if f != nil && !used[f.Name] {
			r.errorf(td.Owner, "%s: %s does not use its parameter %s; write :%s in its SQL, or drop the field from %s", td.Name, where, f.Name, f.Name, def.Params)
		}
	}
	for _, name := range append(append([]string(nil), def.NotNull...), scan.NotNull...) {
		if sqlutil.FieldNamed(row, name) == nil {
			r.errorf(td.Owner, "%s: %s says %s is never null, and its row type %s has no such field", td.Name, where, name, def.Row)
		}
	}
	if def.OrderBy != nil {
		checkQueryOrder(schema, td, def, params, scan, r)
	}
	if def.Page != nil {
		checkQueryPage(schema, td, def, row, r)
	}
	checkQueryReturns(td, def, scan, r)
}

// checkQueryReturns checks what a query's call answers (D95): many rows,
// one or one or none. A query of one row, or of one or none, is not paged,
// and its statement does not show it may return more than one row: a
// top-level GROUP BY, UNION, VALUES list of several rows or set-returning
// function (sqlutil.QueryScan.ManyRows). An author whose statement never
// returns more ends it with LIMIT 1, which says so.
func checkQueryReturns(td *ir.TypeDef, def *ir.QueryDef, scan sqlutil.QueryScan, r *Result) {
	where := "query " + def.Name
	returns := def.Returning()
	switch returns {
	case ir.QueryReturnsMany:
		return
	case ir.QueryReturnsOne, ir.QueryReturnsOptional:
	default:
		r.errorf(td.Owner, "%s: %s returns %q; a query returns %q, the rows, %q, the one row, or %q, the row or none", td.Name, where, def.Returns, ir.QueryReturnsMany, ir.QueryReturnsOne, ir.QueryReturnsOptional)
		return
	}
	if def.Page != nil {
		r.errorf(td.Owner, "%s: %s takes a page and returns %q; a paged query returns its rows a page at a time, so drop the page or the returns", td.Name, where, returns)
		return
	}
	if scan.ManyRows != "" {
		r.errorf(td.Owner, "%s: %s returns %q, and its SQL %s, so it may return more than one row; return %q, or end the statement with LIMIT 1 when it never returns more than one", td.Name, where, returns, scan.ManyRows, ir.QueryReturnsMany)
	}
}

// checkQueryOrder checks a query's orderBy: its parameter is an enum field
// of the params, the SQL writes it where ORDER BY's terms go and nowhere a
// value goes, each value of the enum is an option, each option a value of
// the enum and a fragment of no parameter and no statement of its own, and
// a parameter that may be null names its default.
func checkQueryOrder(schema *ir.Schema, td *ir.TypeDef, def *ir.QueryDef, params *ir.TypeDef, scan sqlutil.QueryScan, r *Result) {
	where := "query " + def.Name
	order := def.OrderBy
	f := sqlutil.FieldNamed(params, order.Param)
	if f == nil {
		r.errorf(td.Owner, "%s: %s orders by %s, which is no field of its params type %s", td.Name, where, order.Param, def.Params)
		return
	}
	enum := schema.Enums[f.TypeRef.Name]
	if enum == nil || f.TypeRef.IsArray || f.TypeRef.IsMap {
		r.errorf(td.Owner, "%s: %s orders by %s, which is of type %s; the order's parameter is an enum of the schema whose values are the options", td.Name, where, order.Param, f.TypeRef.Name)
		return
	}
	if def.Page != nil {
		r.errorf(td.Owner, "%s: %s takes an orderBy and a page; a page is read in the order of its key", td.Name, where)
	}
	values := map[string]bool{}
	for _, v := range enum.Values {
		value := v.SerializedAs
		if value == "" {
			value = v.Name
		}
		values[value] = true
		if _, ok := order.Options[value]; !ok {
			r.errorf(td.Owner, "%s: %s's order has no option for %s, a value of %s; give each value its fragment", td.Name, where, value, enum.Name)
		}
	}
	for _, value := range order.OptionValues() {
		if !values[value] {
			r.errorf(td.Owner, "%s: %s's order option %s is no value of %s", td.Name, where, value, enum.Name)
		}
		fragment, err := sqlutil.ScanQuery(order.Options[value])
		switch {
		case err != nil:
			r.errorf(td.Owner, "%s: %s's order option %s: %v", td.Name, where, value, err)
		case len(fragment.Params) > 0 || fragment.End < len(order.Options[value]):
			r.errorf(td.Owner, "%s: %s's order option %s is a fixed fragment of ORDER BY's terms, with no parameter and no semicolon", td.Name, where, value)
		}
	}
	if order.Default != "" && !values[order.Default] {
		r.errorf(td.Owner, "%s: %s's order default %s is no value of %s", td.Name, where, order.Default, enum.Name)
	}
	if !f.Required && order.Default == "" {
		r.errorf(td.Owner, "%s: %s orders by %s, which may be null; name the option a null picks, as default: %q", td.Name, where, order.Param, firstOption(order))
	}
	for _, use := range scan.Params {
		if use.Name != order.Param {
			continue
		}
		before := strings.ToUpper(strings.TrimSpace(def.SQL[:use.Start]))
		if !strings.HasSuffix(before, "BY") && !strings.HasSuffix(before, ",") {
			r.errorf(td.Owner, "%s: %s writes :%s where a value goes; the order's parameter stands where ORDER BY's terms go, as ORDER BY :%s", td.Name, where, order.Param, order.Param)
		}
	}
}

// firstOption is the first option of an order, by value, for an example in
// an error.
func firstOption(order *ir.QueryOrderBy) string {
	if values := order.OptionValues(); len(values) > 0 {
		return values[0]
	}
	return ""
}

// checkQueryPage checks a paged query's key: one or more required row
// fields, each one value a keyset compares, which tell its rows apart.
func checkQueryPage(schema *ir.Schema, td *ir.TypeDef, def *ir.QueryDef, row *ir.TypeDef, r *Result) {
	where := "query " + def.Name
	seen := map[string]bool{}
	for _, name := range def.Page.Key {
		f := sqlutil.FieldNamed(row, name)
		switch {
		case f == nil:
			r.errorf(td.Owner, "%s: %s pages by %s, which is no field of its row type %s", td.Name, where, name, def.Row)
		case seen[name]:
			r.errorf(td.Owner, "%s: %s pages by %s twice", td.Name, where, name)
		case !f.Required:
			r.errorf(td.Owner, "%s: %s pages by %s, which may be null; a page key is required, as a row's key is", td.Name, where, name)
		case f.TypeRef.IsArray || f.JsonField || f.TypeRef.Name == "Generic.JSON" || f.TypeRef.Name == ir.VectorScalarName || sqlutil.IsJSONFieldType(schema, f.TypeRef.Name):
			r.errorf(td.Owner, "%s: %s pages by %s, a list, a JSON value or a vector; a page key is one value that sorts", td.Name, where, name)
		}
		seen[name] = true
	}
}
