package ormgen

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The declared queries of a DB schema in Go (D86): queries.go gives each
// @query a method of Queries, db.Queries.<Name>(ctx, params) and the same on
// a transaction, which runs the statement sqlutil writes for it, the one
// the TypeScript runtime runs too (sqlutil.QueriesOf). A parameter binds as
// its column would (a UUID through ToUUID, a time as a time.Time); a row is
// read by its columns' names, each field scanned as a table's would be. A
// query that returns one row answers it, and one that returns one or none
// a pointer to it (D95).

// Query is a declared query as queries.go writes it.
type Query struct {
	// Name is the query's name, Method its method of Queries, Doc its
	// class's comment.
	Name   string
	Method string
	Doc    string
	// Params and Row are the Go types of its params and of a row.
	Params string
	Row    string
	// Const names the statement's constant and SQL is its Go literal: the
	// statement as it runs, or, with an order, its format string.
	Const string
	SQL   string
	// Args are the statements that append each parameter, in positional
	// order, to args.
	Args []string
	// RowFields read the row's fields.
	RowFields []QueryRowField
	// Order is the query's dynamic sort, Page its keyset paging.
	Order *QueryOrder
	Page  *QueryPage
	// Returns is what a call answers: "many", "one" or "optional" (D95).
	Returns string
}

// Result is the Go type a call of the query that is not paged returns
// beside its error: the rows, the one row, or a pointer to the row, nil
// when there is none (D95).
func (q Query) Result() string {
	switch q.Returns {
	case ir.QueryReturnsOne:
		return q.Row
	case ir.QueryReturnsOptional:
		return "*" + q.Row
	}
	return "[]" + q.Row
}

// Zero is the value of Result a call returns beside an error.
func (q Query) Zero() string {
	if q.Returns == ir.QueryReturnsOne {
		return q.Row + "{}"
	}
	return "nil"
}

// Answer names the function that makes a call's rows its Result, or ""
// for the rows themselves.
func (q Query) Answer() string {
	switch q.Returns {
	case ir.QueryReturnsOne:
		return "queryOne"
	case ir.QueryReturnsOptional:
		return "queryOptional"
	}
	return ""
}

// QueryRowField is a field of a query's row: its column, the variable a
// scan reads it into when that is not the field itself (Temp, a
// declaration), what the scan is given (Dest), and what moves Temp into the
// field after the scan (After).
type QueryRowField struct {
	Column string
	Temp   string
	Dest   string
	After  string
}

// QueryOrder is a query's dynamic sort: the map of its fragments by value,
// the Go expression of the value its parameter picks, and the values for
// an error.
type QueryOrder struct {
	Const   string
	Options []sqlutil.QueryOrderOption
	Value   string
	Values  string
}

// QueryPage is a paged query's statements, as format strings whose %d is
// the rows to read, and its key: the struct of a row's position, its
// fields, and the statements that append each to args.
type QueryPage struct {
	FirstConst, AfterConst string
	First, After           string
	KeyType                string
	Key                    []QueryKeyField
}

// QueryKeyField is a field of a paged query's key.
type QueryKeyField struct {
	GoName string
	GoType string
	Bind   string
}

// HasQueries reports whether the database declares queries, which
// queries.go runs.
func (o *ORMOutput) HasQueries() bool {
	return len(o.Queries) > 0
}

// queriesOf is what queries.go writes for schema's queries.
func queriesOf(schema *ir.Schema, scalars map[string]scalarLookup, unionNames map[string]bool, dependencies map[string]*ir.Schema) ([]Query, error) {
	models, err := sqlutil.QueriesOf(schema, dependencies)
	if err != nil {
		return nil, err
	}
	var out []Query
	for _, q := range models {
		method := codegen.ToPascalCase(q.Name)
		query := Query{
			Name:    q.Name,
			Method:  method,
			Params:  "types." + q.Params,
			Row:     "types." + q.Row,
			Const:   lowerFirstRune(q.Name) + "Query",
			Returns: q.Returns,
		}
		if td := schema.Types[q.Type]; td != nil {
			query.Doc = codegen.DocText(td.Description, td.Comment)
		}
		text := q.Text
		if q.OrderBy == nil {
			text = strings.ReplaceAll(text, "%%", "%")
		}
		query.SQL = strconv.Quote(text)
		for _, a := range q.Args {
			f := extractField(a.Def, schema, scalars, unionNames)
			bind, err := queryBind(f, a.Class, "params."+codegen.ToPascalCase(a.Name))
			if err != nil {
				return nil, fmt.Errorf("ormgen: query %s: parameter %s: %w", q.Name, a.Name, err)
			}
			query.Args = append(query.Args, "args = append(args, "+bind+")")
		}
		for _, rf := range q.RowFields {
			f := extractField(rf.Def, schema, scalars, unionNames)
			field, err := queryRowField(q.Name, f, rf)
			if err != nil {
				return nil, fmt.Errorf("ormgen: query %s: row field %s: %w", q.Name, rf.Name, err)
			}
			query.RowFields = append(query.RowFields, field)
		}
		if o := q.OrderBy; o != nil {
			f := extractField(o.Param.Def, schema, scalars, unionNames)
			value := "string(params." + codegen.ToPascalCase(o.Param.Name) + ")"
			if f.GoPointer() {
				value = "queryOrder(params." + codegen.ToPascalCase(o.Param.Name) + ", " + strconv.Quote(o.Default) + ")"
			}
			values := make([]string, len(o.Options))
			for i, option := range o.Options {
				values[i] = option.Value
			}
			sort.Strings(values)
			query.Order = &QueryOrder{Const: lowerFirstRune(q.Name) + "Orders", Options: o.Options, Value: value, Values: strings.Join(values, ", ")}
		}
		if p := q.Page; p != nil {
			page := &QueryPage{
				FirstConst: lowerFirstRune(q.Name) + "FirstPage",
				AfterConst: lowerFirstRune(q.Name) + "PageAfter",
				First:      strconv.Quote(p.First),
				After:      strconv.Quote(p.After),
				KeyType:    method + "PageKey",
			}
			for _, k := range p.Key {
				f := extractField(k.Def, schema, scalars, unionNames)
				goName := codegen.ToPascalCase(k.Name)
				bind, err := queryBind(f, k.Class, "after."+goName)
				if err != nil {
					return nil, fmt.Errorf("ormgen: query %s: page key %s: %w", q.Name, k.Name, err)
				}
				page.Key = append(page.Key, QueryKeyField{GoName: goName, GoType: f.ValueGoType(), Bind: "args = append(args, " + bind + ")"})
			}
			query.Page = page
		}
		out = append(out, query)
	}
	return out, nil
}

// lowerFirstRune is name with its first letter in lower case.
func lowerFirstRune(name string) string {
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

// queryValue is the Go expression that binds v, a value of the field's
// element type (not a pointer), as a column of its class takes it.
func queryValue(class sqlutil.ColumnClass, v string) (string, error) {
	switch {
	case class.Vector:
		return "queryVectorText(" + v + ")", nil
	case class.JSON || class.AnyJSON:
		return "", fmt.Errorf("a JSON value binds through queryJSON")
	case class.UUIDLike:
		return "[16]byte(" + v + ".ToUUID())", nil
	case class.DateTimeLike:
		return "time.Time(" + v + ")", nil
	case class.DateLike:
		return "queryDate(string(" + v + "))", nil
	case class.Decimal:
		return v, nil
	case class.IntLike:
		return "int64(" + v + ")", nil
	case class.FloatLike:
		return "float64(" + v + ")", nil
	case class.BoolLike:
		return "bool(" + v + ")", nil
	}
	return "string(" + v + ")", nil
}

// queryBind is the Go expression that binds the field, at expr, as its
// column would hold it: SQL NULL for a nil pointer, a nil list and the
// empty text of a Nullable<string>, whose Go type holds no null.
func queryBind(f Field, class sqlutil.ColumnClass, expr string) (string, error) {
	elem := strings.TrimPrefix(f.GoType, "*")
	switch {
	case class.JSON || class.AnyJSON:
		return "queryJSON(" + expr + ")", nil
	case class.Vector:
		return "queryVectorText(" + expr + ")", nil
	case f.IsArray:
		target, value := "string", ""
		switch {
		case class.UUIDLike:
			target = "[16]byte"
		case class.DateTimeLike:
			target = "time.Time"
		case class.Decimal, class.DateLike:
			target = "string"
			value = "string(v)"
		case class.IntLike:
			target = "int64"
		case class.FloatLike:
			target = "float64"
		case class.BoolLike:
			target = "bool"
		}
		if value == "" {
			var err error
			if value, err = queryValue(class, "v"); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("queryList(%s, func(v %s) %s { return %s })", expr, elem, target, value), nil
	case f.GoPointer():
		value, err := queryValue(class, "v")
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("queryPointer(%s, func(v %s) any { return %s })", expr, elem, value), nil
	case !f.IsRequired && f.GoType == "string":
		return "queryText(" + expr + ")", nil
	}
	return queryValue(class, expr)
}

// queryRowField is how a row reads the field rf, of Go field f.
func queryRowField(query string, f Field, rf sqlutil.QueryField) (QueryRowField, error) {
	goName := codegen.ToPascalCase(rf.Name)
	field := "row." + goName
	temp := "col" + goName
	class := rf.Class
	elem := strings.TrimPrefix(f.GoType, "*")
	fail := fmt.Sprintf("return nil, fmt.Errorf(\"query %s: column %s: %%w\", err)", query, rf.Column)
	out := QueryRowField{Column: rf.Column, Dest: "&" + field}
	assign := func(value string) string {
		if f.GoPointer() {
			return "v := " + value + "\n" + field + " = &v"
		}
		return field + " = " + value
	}
	switch {
	case class.JSON || class.AnyJSON:
		out.Temp, out.Dest = "var "+temp+" []byte", "&"+temp
		out.After = fmt.Sprintf("if %s != nil {\nif err := json.Unmarshal(%s, &%s); err != nil {\n%s\n}\n}", temp, temp, field, fail)
	case class.Vector:
		out.Temp, out.Dest = "var "+temp+" *string", "&"+temp
		out.After = fmt.Sprintf("if %s != nil {\nv, err := queryParseVector[%s](*%s)\nif err != nil {\n%s\n}\n%s = v\n}", temp, f.ValueGoType(), temp, fail, field)
	case f.IsArray:
		switch {
		case class.UUIDLike, class.Decimal:
			out.Temp, out.Dest = "var "+temp+" []string", "&"+temp
			out.After = fmt.Sprintf("if %s != nil {\nv, err := queryScanList[%s](%s)\nif err != nil {\n%s\n}\n%s = v\n}", temp, elem, temp, fail, field)
		case class.DateTimeLike:
			out.Temp, out.Dest = "var "+temp+" []time.Time", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(t time.Time) %s { return %s(t) })", field, temp, elem, elem)
		case class.DateLike:
			out.Temp, out.Dest = "var "+temp+" []time.Time", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(t time.Time) %s { return %s(t.Format(time.DateOnly)) })", field, temp, elem, elem)
		case class.IntLike:
			out.Temp, out.Dest = "var "+temp+" []int64", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(n int64) %s { return %s(n) })", field, temp, elem, elem)
		case class.FloatLike:
			out.Temp, out.Dest = "var "+temp+" []float64", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(n float64) %s { return %s(n) })", field, temp, elem, elem)
		case class.BoolLike:
			out.Temp, out.Dest = "var "+temp+" []bool", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(b bool) %s { return %s(b) })", field, temp, elem, elem)
		default:
			out.Temp, out.Dest = "var "+temp+" []string", "&"+temp
			out.After = fmt.Sprintf("%s = queryConvert(%s, func(s string) %s { return %s(s) })", field, temp, elem, elem)
		}
	case class.DateLike && !class.DateTimeLike:
		out.Temp, out.Dest = "var "+temp+" *time.Time", "&"+temp
		out.After = fmt.Sprintf("if %s != nil {\n%s\n}", temp, assign(elem+"("+temp+".Format(time.DateOnly))"))
	case !f.IsRequired && !f.GoPointer() && f.GoType == "string":
		// A Nullable<string> is a Go string, whose null is the empty text.
		out.Temp, out.Dest = "var "+temp+" *string", "&"+temp
		out.After = fmt.Sprintf("if %s != nil {\n%s = *%s\n}", temp, field, temp)
	}
	return out, nil
}
