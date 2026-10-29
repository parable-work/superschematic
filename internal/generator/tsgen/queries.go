package tsgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The declared queries of a DB schema in TypeScript (D86): orm.ts gives
// each @query a typed function of its params, orm.queries.<name>(params)
// and tx.queries.<name>(params), and carries its statement as data, with
// the codec's descriptor of each parameter and each row field, which the
// HTTP runtime's queriesOf runs as the Go ORM's Queries runs the same
// statement (sqlutil.QueriesOf).

// ORMQuery is a declared query as orm.ts writes it.
type ORMQuery struct {
	// Name is the query's function, Doc its class's comment.
	Name string
	Doc  string
	// ParamsType and RowType are the TypeScript types of its params and of
	// a row, from the package's ./types entry.
	ParamsType string
	RowType    string
	// Text is the statement, a format string (sqlutil.QueryModel.Text).
	Text string
	// Params are the parameters in positional order, Row the row's fields,
	// each with the codec's flags.
	Params []ORMColumn
	Row    []ORMColumn

	OrderBy *ORMQueryOrder
	Page    *ORMQueryPage

	// Returns is what a call answers: "many", "one" or "optional" (D95).
	Returns string
}

// ResultType is the TypeScript type a call of the query resolves to: its
// rows, its one row, or its row or null (D95). A paged query's is its
// page's.
func (q ORMQuery) ResultType() string {
	switch {
	case q.Page != nil:
		return "RowPage<" + q.RowType + ", " + q.Page.KeyType + ">"
	case q.Returns == ir.QueryReturnsOne:
		return q.RowType
	case q.Returns == ir.QueryReturnsOptional:
		return q.RowType + " | null"
	}
	return q.RowType + "[]"
}

// ReturnsOne reports whether a call answers one row, or one or none,
// rather than the rows, which the runtime is told.
func (q ORMQuery) ReturnsOne() bool {
	return q.Returns == ir.QueryReturnsOne || q.Returns == ir.QueryReturnsOptional
}

// ORMQueryOrder is a query's dynamic sort.
type ORMQueryOrder struct {
	Param   string
	Options []sqlutil.QueryOrderOption
	Default string
}

// ORMQueryPage is a paged query's statements and its key.
type ORMQueryPage struct {
	// KeyType names the interface of a row's position in the order.
	KeyType string
	Key     []ORMQueryKey
	First   string
	After   string
}

// ORMQueryKey is a member of a paged query's key and its TypeScript type.
type ORMQueryKey struct {
	Member string
	Type   string
}

// QueriesConst is the name of the queries' definitions in orm.ts.
func (o *ORMInfo) QueriesConst() string {
	return lowerFirst(o.Name) + "Queries"
}

// typeScriptQueries is what orm.ts writes for schema's queries, their
// types from types and known (typeScriptORM's).
func typeScriptQueries(schema *ir.Schema, opts Options, types map[string]*TypeInfo, known map[string]bool) ([]ORMQuery, error) {
	models, err := sqlutil.QueriesOf(schema, opts.Dependencies)
	if err != nil {
		return nil, err
	}
	var out []ORMQuery
	for _, q := range models {
		query := ORMQuery{
			Name:       q.Name,
			ParamsType: qualifyTypes(q.Params, known),
			RowType:    qualifyTypes(q.Row, known),
			Text:       q.Text,
			Returns:    q.Returns,
		}
		if td := schema.Types[q.Type]; td != nil {
			query.Doc = codegen.DocText(td.Description, td.Comment)
		}
		for _, a := range q.Args {
			column, err := queryColumn(a, a.Name)
			if err != nil {
				return nil, fmt.Errorf("tsgen: query %s: %w", q.Name, err)
			}
			query.Params = append(query.Params, column)
		}
		for _, f := range q.RowFields {
			column, err := queryColumn(f, sqlutil.QuoteIdentifier(f.Column))
			if err != nil {
				return nil, fmt.Errorf("tsgen: query %s: %w", q.Name, err)
			}
			query.Row = append(query.Row, column)
		}
		if o := q.OrderBy; o != nil {
			query.OrderBy = &ORMQueryOrder{Param: o.Param.Name, Options: o.Options, Default: o.Default}
		}
		if p := q.Page; p != nil {
			page := &ORMQueryPage{KeyType: codegen.ToPascalCase(q.Name) + "QueryPageKey", First: p.First, After: p.After}
			info := types[q.Row]
			for _, k := range p.Key {
				t := qualifyTypes(fieldType(info, k.Name), known)
				if t == "" {
					return nil, fmt.Errorf("tsgen: query %s: page key %s has no TypeScript type", q.Name, k.Name)
				}
				page.Key = append(page.Key, ORMQueryKey{Member: k.Name, Type: t})
			}
			query.Page = page
		}
		out = append(out, query)
	}
	return out, nil
}

// queryColumn is a query's parameter or row field as the codec reads it,
// the way ormColumn classifies a table's column.
func queryColumn(f sqlutil.QueryField, column string) (ORMColumn, error) {
	cl := f.Class
	c := ORMColumn{
		Member:    f.Name,
		Field:     f.Name,
		Column:    column,
		JSON:      cl.JSON || cl.AnyJSON,
		List:      cl.List,
		UUID:      cl.UUIDLike && !cl.JSON,
		Timestamp: cl.DateTimeLike && !cl.JSON,
		Date:      cl.DateLike && !cl.DateTimeLike && !cl.JSON,
		Int64:     cl.IntLike && !cl.JSON,
		Numeric:   strings.HasPrefix(strings.ToUpper(cl.SQLType), "NUMERIC") || strings.HasPrefix(strings.ToUpper(cl.SQLType), "DECIMAL"),
		Vector:    cl.Vector,
		Boolean:   cl.BoolLike && !cl.JSON,
		Required:  !f.Nullable,
	}
	if paths := codecPaths(c); len(paths) > 1 {
		return c, fmt.Errorf("%s would decode as %s, and the codec reads a value one way", f.Name, strings.Join(paths, " and as "))
	}
	return c, nil
}

// QueryFlags are the codec's flags of a query's parameter or row field, as
// orm.ts writes them: ", uuid: true" and the like.
func (c ORMColumn) QueryFlags() string {
	var b strings.Builder
	flag := func(on bool, name string) {
		if on {
			b.WriteString(", " + name + ": true")
		}
	}
	flag(c.JSON, "json")
	flag(c.List, "list")
	flag(c.UUID, "uuid")
	flag(c.Timestamp, "timestamp")
	flag(c.Date, "date")
	flag(c.Int64, "int64")
	flag(c.Numeric, "numeric")
	flag(c.Vector, "vector")
	flag(c.Boolean, "boolean")
	flag(c.Required, "required")
	return b.String()
}
