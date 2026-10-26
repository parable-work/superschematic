package sqlutil

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// A declared query (@query, D86) as both ORMs run it. The SQL names each
// parameter :field; ScanQuery finds them outside strings, quoted
// identifiers, dollar-quoted bodies and comments, and past a :: cast, and
// QueryOf rewrites each to a positional one, numbered by first use, so the
// Go ORM and the TypeScript runtime send the same statement. The statements
// are format strings, as a table's are (TableStatementsOf): %% is a literal
// %, the %s of a query with an orderBy its order's fragment, and the %d of
// a paged query's statements the rows it reads.

// QueryParamUse is one :name in a query's SQL and the bytes it spans.
type QueryParamUse struct {
	Name       string
	Start, End int
}

// QueryScan is what ScanQuery finds in a query's SQL.
type QueryScan struct {
	// Params are the :name uses, in order.
	Params []QueryParamUse
	// NotNull are the fields a `@notNull a, b` comment names.
	NotNull []string
	// OuterTables are the tables a LEFT or FULL JOIN joins, as Postgres
	// names them (an unquoted name in lower case), whose columns a row may
	// hold as null though their columns are NOT NULL. AllOuter is a query
	// with a RIGHT or FULL JOIN, which may null every table's columns.
	OuterTables []string
	AllOuter    bool
	// Identifiers are the names the SQL writes outside strings and
	// comments, as Postgres reads them: an unquoted one in lower case, a
	// quoted one as it is.
	Identifiers []string
	// End is where the statement ends: the length of the SQL, or the offset
	// of its trailing semicolon.
	End int
}

// notNullHint matches a comment's `@notNull a, b` hint.
var notNullHint = regexp.MustCompile(`@notNull\s+([A-Za-z_][A-Za-z0-9_]*(?:\s*,\s*[A-Za-z_][A-Za-z0-9_]*)*)`)

// queryWord is a word of a query outside strings and comments, as join
// detection reads it: an identifier in upper case with its raw text, a
// quoted identifier, ".", "(" or a parameter.
type queryWord struct {
	upper string
	raw   string
}

// ScanQuery reads a query's SQL: its parameters, its @notNull hints and its
// outer joins. It refuses a positional parameter ($1), an unclosed string,
// identifier, body or comment, and a second statement; one trailing
// semicolon is allowed and left out of End.
func ScanQuery(sql string) (QueryScan, error) {
	scan := QueryScan{End: len(sql)}
	var words []queryWord
	semicolon := -1
	// statement refuses a token past the semicolon that ends the statement.
	statement := func() error {
		if semicolon >= 0 {
			return fmt.Errorf("the SQL holds more than one statement (a ; at byte %d); a query is one statement", semicolon)
		}
		return nil
	}
	for i := 0; i < len(sql); {
		ch := sql[i]
		switch {
		case ch == ':' && i+1 < len(sql) && sql[i+1] == ':':
			i += 2
			continue
		case ch == ':' && i+1 < len(sql) && isQueryIdentStart(sql[i+1]):
			if err := statement(); err != nil {
				return scan, err
			}
			j := i + 1
			for j < len(sql) && isSQLIdentByte(sql[j]) {
				j++
			}
			scan.Params = append(scan.Params, QueryParamUse{Name: sql[i+1 : j], Start: i, End: j})
			words = append(words, queryWord{upper: ":"})
			i = j
			continue
		case ch == '$' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' && (i == 0 || !isSQLIdentByte(sql[i-1])):
			j := i + 1
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				j++
			}
			return scan, fmt.Errorf("the SQL holds the positional parameter %s; name each parameter :field, a field of the params type", sql[i:j])
		case isSQLIdentByte(ch):
			if err := statement(); err != nil {
				return scan, err
			}
			j := i
			for j < len(sql) && isSQLIdentByte(sql[j]) {
				j++
			}
			words = append(words, queryWord{upper: strings.ToUpper(sql[i:j]), raw: strings.ToLower(sql[i:j])})
			i = j
			continue
		case ch == ';':
			if semicolon < 0 {
				semicolon = i
				scan.End = i
			}
			i++
			continue
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-', ch == '/' && i+1 < len(sql) && sql[i+1] == '*':
			next, _, err := skipSQLToken(sql, i)
			if err != nil {
				return scan, err
			}
			for _, m := range notNullHint.FindAllStringSubmatch(sql[i:next], -1) {
				for _, name := range strings.Split(m[1], ",") {
					scan.NotNull = append(scan.NotNull, strings.TrimSpace(name))
				}
			}
			i = next
			continue
		case ch == '\'' || ch == '"' || ch == '$':
			next, _, err := skipSQLToken(sql, i)
			if err != nil {
				return scan, err
			}
			if next > i+1 {
				if err := statement(); err != nil {
					return scan, err
				}
				if ch == '"' {
					words = append(words, queryWord{upper: "IDENT", raw: strings.ReplaceAll(sql[i+1:next-1], `""`, `"`)})
				} else {
					words = append(words, queryWord{upper: "LITERAL"})
				}
				i = next
				continue
			}
		case ch == '.' || ch == '(':
			words = append(words, queryWord{upper: string(ch)})
		case ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r':
			if err := statement(); err != nil {
				return scan, err
			}
		}
		i++
	}
	scan.OuterTables, scan.AllOuter = outerJoins(words)
	for _, w := range words {
		if w.raw != "" && w.upper != "LITERAL" {
			scan.Identifiers = append(scan.Identifiers, w.raw)
		}
	}
	return scan, nil
}

// isQueryIdentStart reports whether ch may start a parameter's name.
func isQueryIdentStart(ch byte) bool {
	return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z'
}

// outerJoins reads the tables a LEFT or FULL JOIN joins, by the last name
// of each table reference, and whether a RIGHT or FULL JOIN makes every
// table outer.
func outerJoins(words []queryWord) ([]string, bool) {
	var tables []string
	all := false
	for i, w := range words {
		if w.upper != "JOIN" {
			continue
		}
		kind := ""
		for j := i - 1; j >= 0 && j >= i-2; j-- {
			switch words[j].upper {
			case "OUTER":
				continue
			case "LEFT", "RIGHT", "FULL":
				kind = words[j].upper
			}
			break
		}
		if kind == "RIGHT" || kind == "FULL" {
			all = true
		}
		if kind != "LEFT" && kind != "FULL" {
			continue
		}
		j := i + 1
		if j < len(words) && words[j].upper == "LATERAL" {
			j++
		}
		name := ""
		for ; j < len(words); j++ {
			if words[j].raw == "" || words[j].upper == "LITERAL" {
				break
			}
			name = words[j].raw
			if j+1 >= len(words) || words[j+1].upper != "." {
				break
			}
			j++
		}
		if name != "" {
			tables = append(tables, name)
		}
	}
	return tables, all
}

// QueryField is a field of a query's params or row type as both ORMs bind
// or read it.
type QueryField struct {
	// Name is the field, the member of the params or the row.
	Name string
	// Column is the result column a row field reads, unquoted: its
	// @column, else its name in snake case. Empty for a parameter.
	Column string
	// Def is the field.
	Def *ir.FieldDef
	// Class is what the value holds, as a column of its type would.
	Class ColumnClass
	// SQLType is the SQL type a column of the field has, as the DDL maps
	// it (ColumnType): what verification holds the type Postgres describes
	// to.
	SQLType string
	// Nullable is a field that may be null: Nullable<T> in the schema. A
	// row field that is not nullable is one the query must show is never
	// null.
	Nullable bool
	// Position is a parameter's $n, by first use; 0 for a row field and for
	// the order's parameter.
	Position int
}

// QueryModel is a query as both ORMs run it.
type QueryModel struct {
	// Name is the query's function, Type its class, Params and Row the
	// names of its types.
	Name   string
	Type   string
	Params string
	Row    string

	// Args are the params' fields the statement binds, in positional order;
	// the order's parameter is not one of them.
	Args []QueryField
	// RowFields are the row's fields, in declaration order.
	RowFields []QueryField

	// Text is the statement: a format string whose %s is the order's
	// fragment when the query has an OrderBy.
	Text string

	// NotNull are the row fields the query vouches are never null, by
	// name: the notNull option's and the SQL's @notNull hints.
	NotNull map[string]bool
	// OuterTables and AllOuter are ScanQuery's.
	OuterTables []string
	AllOuter    bool

	// Tables are the shapes of the tables of the schema the SQL names,
	// sorted: each table's name and its columns' names, types and
	// nullability, which a lock's hash covers, so a change to a column the
	// query may read is a query to verify again.
	Tables []string

	OrderBy *QueryOrderModel
	Page    *QueryPageModel
}

// QueryOrderModel is a query's dynamic sort: the parameter and its options,
// sorted by value.
type QueryOrderModel struct {
	Param   QueryField
	Options []QueryOrderOption
	Default string
}

// QueryOrderOption is a value of the order's enum and its fragment.
type QueryOrderOption struct {
	Value string
	SQL   string
}

// QueryPageModel is a paged query's statements: First reads the first page
// and After the page after a key, each the query's statement in a subquery
// ordered by Key, whose %d is the rows to read (a page and one more, to
// tell whether another follows). After binds the key's values after the
// query's own parameters.
type QueryPageModel struct {
	Key   []QueryField
	Desc  bool
	First string
	After string
}

// QueryPageAlias is the alias a paged query's subquery is read through.
const QueryPageAlias = "q"

// QueryStatement is one statement verification prepares for a query: the
// query's text, with an order's option filled in, or a page's statement.
type QueryStatement struct {
	// Label says which, for an error: "", "order date", "first page".
	Label string
	SQL   string
}

// Statements are the statements of the query as Postgres runs them, each
// order option's and each page's, its format strings filled with a page
// of DefaultLimit rows.
func (q *QueryModel) Statements() []QueryStatement {
	switch {
	case q.OrderBy != nil:
		out := make([]QueryStatement, 0, len(q.OrderBy.Options))
		for _, option := range q.OrderBy.Options {
			out = append(out, QueryStatement{Label: "order " + option.Value, SQL: fmt.Sprintf(q.Text, option.SQL)})
		}
		return out
	case q.Page != nil:
		return []QueryStatement{
			{Label: "first page", SQL: fmt.Sprintf(q.Page.First, DefaultLimit+1)},
			{Label: "page after a key", SQL: fmt.Sprintf(q.Page.After, DefaultLimit+1)},
		}
	}
	return []QueryStatement{{SQL: strings.ReplaceAll(q.Text, "%%", "%")}}
}

// QueriesOf models every query of schema (ir.Schema.Queries), in query
// name order, its types' imported enums and unions read from
// dependencies.
func QueriesOf(schema *ir.Schema, dependencies map[string]*ir.Schema) ([]QueryModel, error) {
	defs := schema.Queries()
	if len(defs) == 0 {
		return nil, nil
	}
	scalars := ScalarClasses(schema)
	sqlTypes := ScalarSQLTypes(schema)
	enums := EnumNames(schema, dependencies)
	unions := UnionNames(schema, dependencies)
	tables, err := TablesOf(schema, dependencies)
	if err != nil {
		return nil, err
	}
	shapes := map[string]string{}
	for _, t := range tables {
		columns := make([]string, len(t.Columns))
		for i, c := range t.Columns {
			nullability := "NULL"
			if c.Required || c.Key {
				nullability = "NOT NULL"
			}
			columns[i] = c.Name + " " + strings.TrimSpace(c.Type+" "+c.Class.SQLType) + " " + nullability
		}
		shapes[t.Name] = t.Name + "(" + strings.Join(columns, ", ") + ")"
	}
	out := make([]QueryModel, 0, len(defs))
	for _, td := range defs {
		q, err := queryOf(schema, td, scalars, sqlTypes, enums, unions)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", td.Query.Name, err)
		}
		scan, err := ScanQuery(td.Query.SQL)
		if err != nil {
			return nil, fmt.Errorf("query %s: %w", td.Query.Name, err)
		}
		named := map[string]bool{}
		for _, name := range scan.Identifiers {
			if shape, ok := shapes[name]; ok && !named[name] {
				named[name] = true
				q.Tables = append(q.Tables, shape)
			}
		}
		sort.Strings(q.Tables)
		out = append(out, q)
	}
	return out, nil
}

func queryOf(schema *ir.Schema, td *ir.TypeDef, scalars map[string]ScalarClass, sqlTypes map[string]string, enums, unions map[string]bool) (QueryModel, error) {
	def := td.Query
	q := QueryModel{Name: def.Name, Type: td.Name, Params: def.Params, Row: def.Row, NotNull: map[string]bool{}}
	params := schema.Types[def.Params]
	row := schema.Types[def.Row]
	if params == nil || row == nil {
		return q, fmt.Errorf("its params type %s or row type %s is not a type of the schema", def.Params, def.Row)
	}
	field := func(f *ir.FieldDef) QueryField {
		scalar, isScalar := scalars[f.TypeRef.Name]
		class := ClassifyColumn(f, schema, scalar, isScalar, enums[f.TypeRef.Name], unions[f.TypeRef.Name])
		class.SQLType = ElementType(f.TypeRef.Name, sqlTypes)
		return QueryField{Name: f.Name, Def: f, Class: class, SQLType: ColumnType(schema, f, sqlTypes), Nullable: !f.Required}
	}
	for _, f := range row.Fields {
		rf := field(f)
		rf.Column = ColumnName(f)
		q.RowFields = append(q.RowFields, rf)
	}

	scan, err := ScanQuery(def.SQL)
	if err != nil {
		return q, err
	}
	q.OuterTables, q.AllOuter = scan.OuterTables, scan.AllOuter
	for _, name := range append(append([]string(nil), def.NotNull...), scan.NotNull...) {
		q.NotNull[name] = true
	}

	orderParam := ""
	if def.OrderBy != nil {
		orderParam = def.OrderBy.Param
	}
	positions := map[string]int{}
	var b strings.Builder
	at := 0
	for _, use := range scan.Params {
		if use.Start >= scan.End {
			break
		}
		b.WriteString(strings.ReplaceAll(def.SQL[at:use.Start], "%", "%%"))
		at = use.End
		pf := FieldNamed(params, use.Name)
		if pf == nil {
			return q, fmt.Errorf(":%s is no field of its params type %s", use.Name, def.Params)
		}
		if use.Name == orderParam {
			b.WriteString("%s")
			continue
		}
		n, ok := positions[use.Name]
		if !ok {
			n = len(positions) + 1
			positions[use.Name] = n
			arg := field(pf)
			arg.Position = n
			q.Args = append(q.Args, arg)
		}
		fmt.Fprintf(&b, "$%d", n)
	}
	b.WriteString(strings.ReplaceAll(def.SQL[at:scan.End], "%", "%%"))
	q.Text = strings.TrimSpace(b.String())

	if def.OrderBy != nil {
		pf := FieldNamed(params, def.OrderBy.Param)
		if pf == nil {
			return q, fmt.Errorf("its orderBy param %s is no field of its params type %s", def.OrderBy.Param, def.Params)
		}
		order := &QueryOrderModel{Param: field(pf), Default: def.OrderBy.Default}
		for _, value := range def.OrderBy.OptionValues() {
			order.Options = append(order.Options, QueryOrderOption{Value: value, SQL: def.OrderBy.Options[value]})
		}
		q.OrderBy = order
	}
	if def.Page != nil {
		page := &QueryPageModel{Desc: def.Page.Desc}
		for _, name := range def.Page.Key {
			found := false
			for _, rf := range q.RowFields {
				if rf.Name == name {
					page.Key = append(page.Key, rf)
					found = true
				}
			}
			if !found {
				return q, fmt.Errorf("its page key %s is no field of its row type %s", name, def.Row)
			}
		}
		page.First, page.After = queryPageStatements(q.Text, page.Key, len(q.Args), page.Desc)
		q.Page = page
	}
	return q, nil
}

// queryPageStatements are a paged query's statements over text, the
// query's statement, whose args parameters come first: the first page, and
// the page after a key, whose values bind next.
func queryPageStatements(text string, key []QueryField, args int, desc bool) (first, after string) {
	comparison, direction := ">", "ASC"
	if desc {
		comparison, direction = "<", "DESC"
	}
	columns := make([]string, len(key))
	order := make([]string, len(key))
	values := make([]string, len(key))
	for i, f := range key {
		columns[i] = QueryPageAlias + "." + QuoteIdentifier(f.Column)
		order[i] = columns[i] + " " + direction
		values[i] = fmt.Sprintf("$%d", args+i+1)
	}
	// The subquery ends on a line of its own, so a comment on the query's
	// last line ends before it.
	from := "SELECT * FROM (" + text + "\n) AS " + QueryPageAlias
	orderBy := " ORDER BY " + strings.Join(order, ", ") + " LIMIT %d"
	first = from + orderBy
	after = from + " WHERE (" + strings.Join(columns, ", ") + ") " + comparison + " (" + strings.Join(values, ", ") + ")" + orderBy
	return first, after
}

// QueryValueProblem is why a field of a query's params or row type cannot
// cross between a query and its language types, or "". Both ORMs bind and
// read a scalar, an enum, a list of either, a JSON value (Generic.JSON, a
// @jsonField type, JsonField<T>) and a vector; a POINT, an INTERVAL, a map,
// a list of lists, a union, an object type, a relation and an
// Encrypted<T> field are left for later.
func QueryValueProblem(schema *ir.Schema, f *ir.FieldDef) string {
	ref := f.TypeRef
	target := schema.Types[ref.Name]
	_, union := schema.Unions[ref.Name]
	sqlType := strings.ToUpper(ColumnType(schema, f, ScalarSQLTypes(schema)))
	json := f.JsonField || (target != nil && target.JsonField) || ref.Name == "Generic.JSON"
	switch {
	case f.EncryptedAtRest:
		return "an Encrypted<T> field; a query reads and binds a column's value, never a sealed one"
	case f.Relation != nil || f.HasMany || f.ManyToMany || (target != nil && target.Role == ir.RoleDBTable && !target.JsonField && !f.JsonField):
		return "a relation; a query's field holds the key's scalar, such as Identity.UUID"
	case ref.IsMap:
		return "a map, which no query codec reads yet; hold it as Generic.JSON"
	case ref.IsArrayOfArrays:
		return "a list of lists, which no query codec reads yet; hold it as Generic.JSON"
	case union:
		return "a union, which no query codec reads yet"
	case target != nil && !target.JsonField && !f.JsonField:
		return "an object type; mark the type @jsonField to read and bind it as JSON"
	case ref.IsArray && (json || ref.Name == ir.VectorScalarName):
		return "a list of JSON values or vectors, which no query codec reads yet; hold it as Generic.JSON"
	case ref.Name == ir.VectorScalarName && f.ValidateDimensions == nil:
		return "an Embedding.Vector without dimensions; give it Validate<Embedding.Vector, { dimensions: n }>"
	case strings.HasPrefix(sqlType, "POINT"), strings.HasPrefix(sqlType, "INTERVAL"), strings.HasPrefix(sqlType, "GEOGRAPHY"), strings.HasPrefix(sqlType, "GEOMETRY"):
		return "a " + strings.TrimSuffix(sqlType, "[]") + " value, which no query codec reads yet"
	}
	return ""
}
