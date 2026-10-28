package ir

import "sort"

// RoleQuery marks a @query declaration in a DB schema (D86): a named SQL
// statement the DB service declares, whose parameters and rows are two
// other types of the schema. The class is only where the query is written:
// it is never a table and never a language type, and it holds no field.
// Its Params and Row types are embedded structs, which every language's
// types declare, and the ORMs run the statement as a typed function.
const RoleQuery Role = "Query"

// QueryDef is the @query declaration of a RoleQuery type (D86).
type QueryDef struct {
	// Name is the query's function: categoryTotals is
	// orm.queries.categoryTotals in TypeScript and Queries.CategoryTotals in
	// Go. A lower camel case identifier, unique in the service.
	Name string `json:"name" yaml:"name"`

	// Params names the embedded struct whose fields are the query's
	// parameters, each used in SQL as :field.
	Params string `json:"params" yaml:"params"`

	// Row names the embedded struct whose fields are the query's result
	// columns, each read from the column named after it (its @column, else
	// its name in snake case).
	Row string `json:"row" yaml:"row"`

	// SQL is the statement as written: one statement, its parameters named
	// :field. The ORMs run it with each rewritten to a positional one.
	SQL string `json:"sql" yaml:"sql"`

	// NotNull names the Row fields the query vouches are never null though
	// Postgres cannot tell: an aggregate's COALESCE, a computed column. A
	// `-- @notNull field` comment in SQL says the same.
	NotNull []string `json:"notNull,omitempty" yaml:"notNull,omitempty"`

	// OrderBy maps an enum parameter to fixed ORDER BY fragments. Nil for a
	// query whose order is its SQL's.
	OrderBy *QueryOrderBy `json:"orderBy,omitempty" yaml:"orderBy,omitempty"`

	// Page reads the rows a page at a time by a keyset of Row fields, as
	// findPage does (D60). Nil reads every row.
	Page *QueryPage `json:"page,omitempty" yaml:"page,omitempty"`

	// Returns is what a call answers (D95): QueryReturnsMany, the rows, as
	// an empty one does; QueryReturnsOne, the one row, which fails when the
	// statement reads none or more than one; QueryReturnsOptional, the row
	// or none, which fails when it reads more than one.
	Returns string `json:"returns,omitempty" yaml:"returns,omitempty"`

	// Dialects lists the dialects the query runs on. Empty runs it on every
	// dialect the service is built for.
	Dialects []string `json:"dialects,omitempty" yaml:"dialects,omitempty"`
}

// QueryOrderBy is a query's safe dynamic sort: the parameter Param, an enum
// of the schema, picks one of Options, each a fixed ORDER BY fragment the
// statement holds where :Param stands. No caller text reaches the SQL.
type QueryOrderBy struct {
	// Param is the Params field that picks the order.
	Param string `json:"param" yaml:"param"`

	// Options maps each value of the enum to its fragment, such as
	// `li."date" DESC, li.id DESC`.
	Options map[string]string `json:"options" yaml:"options"`

	// Default is the option a null Param picks; empty when Param is
	// required.
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
}

// QueryPage pages a query's rows by a keyset (D60): the rows come in the
// order of Key, and the page after a row holds the rows past its key.
type QueryPage struct {
	// Key names the Row fields the rows are ordered and paged by, which
	// together tell every row apart: ["date", "id"].
	Key []string `json:"key" yaml:"key"`

	// Desc orders by the key descending.
	Desc bool `json:"desc,omitempty" yaml:"desc,omitempty"`
}

// What a call of a query answers (QueryDef.Returns, D95).
const (
	QueryReturnsMany     = "many"
	QueryReturnsOne      = "one"
	QueryReturnsOptional = "optional"
)

// Returning is what a call of the query answers: its Returns, else
// QueryReturnsMany.
func (q *QueryDef) Returning() string {
	if q.Returns == "" {
		return QueryReturnsMany
	}
	return q.Returns
}

// RunsOn reports whether the query runs on dialect.
func (q *QueryDef) RunsOn(dialect string) bool {
	if len(q.Dialects) == 0 {
		return true
	}
	for _, d := range q.Dialects {
		if d == dialect {
			return true
		}
	}
	return false
}

// OptionValues are the values of the order's options, sorted.
func (o *QueryOrderBy) OptionValues() []string {
	values := make([]string, 0, len(o.Options))
	for value := range o.Options {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// Queries returns the schema's RoleQuery types, sorted by query name.
func (s *Schema) Queries() []*TypeDef {
	var defs []*TypeDef
	for _, name := range sortedStringMapKeys(s.Types) {
		if td := s.Types[name]; td != nil && td.Role == RoleQuery && td.Query != nil {
			defs = append(defs, td)
		}
	}
	sort.SliceStable(defs, func(i, j int) bool { return defs[i].Query.Name < defs[j].Query.Name })
	return defs
}
