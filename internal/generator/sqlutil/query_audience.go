package sqlutil

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// An audience in a declared query (#735): a query that reads an @audience
// table marks where the table's audience goes, :audience(d) for the alias
// d it reads the table through (or the table's own name when it has
// none), and the ORM replaces the mark with the audience's conditions for
// the call's caller, as a repository's read keeps to them (D156). The
// query's text holds each mark as the directive %A{n}, n its index in
// QueryModel.Audience, beside the %s and %d of an order and a page;
// FillAudienceMarks replaces them, and both ORMs run it before the order's
// and the page's fill. A mark's parameters are numbered after every other
// of the statement, the page key's among them.
//
// Verification refuses a query that reads an @audience table, or a
// projection's view of one, with no mark for that reference
// (QueryModel.Unmarked), and QueriesOf refuses a mark whose alias is no
// reference of the query to an @audience table or view.

// QueryAudienceUse is one :audience(alias) mark in a query's SQL and the
// bytes it spans.
type QueryAudienceUse struct {
	// Alias is the alias as Postgres reads it: unquoted in lower case, a
	// quoted one as it is.
	Alias      string
	Start, End int
}

// QueryTableRef is a table, or a view, a query reads in a FROM, a JOIN, a
// FROM's list or an UPDATE: its name as Postgres reads it, the last of a
// qualified one, and the alias it is read through, its name when it has
// none.
type QueryTableRef struct {
	Name  string
	Alias string
}

// QueryAudienceMark is a mark of a query as both ORMs fill it: the alias,
// the @audience table's type, and the conditions' columns, qualified by
// the alias; or, for a view's alias, by the view's source table, and
// Probe, the view's key in the keys of its source's rows of the WHERE %s.
type QueryAudienceMark struct {
	Alias     string
	Table     string
	Effective string
	Agents    string
	Probe     string
}

// Directive is the mark's directive in the query's text.
func (m QueryAudienceMark) directive(i int) string {
	return "%A{" + strconv.Itoa(i) + "}"
}

// PrepareCondition is the condition verification prepares in place of the
// mark: the audience of a caller with no id, with an agent run's access, so
// Postgres checks the alias and its columns and binds no parameter.
func (m QueryAudienceMark) PrepareCondition() string {
	text := m.Effective + " IS NULL AND " + m.Agents + " <> 'none'"
	if m.Probe != "" {
		return fmt.Sprintf(m.Probe, " WHERE "+text)
	}
	return "(" + text + ")"
}

// FillAudienceMarks is text, a query's format string, with each %A{n}
// directive replaced by fill(n), its % doubled so the text stays a format
// string; a %% and every other directive are left as they are.
func FillAudienceMarks(text string, fill func(int) string) string {
	var b strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] != '%' || i+1 >= len(text) {
			b.WriteByte(text[i])
			continue
		}
		if text[i+1] == '%' {
			b.WriteString("%%")
			i++
			continue
		}
		if text[i+1] == 'A' && i+2 < len(text) && text[i+2] == '{' {
			if end := strings.IndexByte(text[i+3:], '}'); end >= 0 {
				if n, err := strconv.Atoi(text[i+3 : i+3+end]); err == nil {
					b.WriteString(strings.ReplaceAll(fill(n), "%", "%%"))
					i += 3 + end
					continue
				}
			}
		}
		b.WriteByte(text[i])
	}
	return b.String()
}

// scanAudienceMark reads a :audience mark whose name ends at i, the byte
// after "audience": its alias, between parentheses, and the byte past the
// closing one. ok is false where no parenthesis follows: the :audience of
// a parameter.
func scanAudienceMark(sql string, i int) (alias string, end int, ok bool, err error) {
	j := i
	for j < len(sql) && (sql[j] == ' ' || sql[j] == '\t') {
		j++
	}
	if j >= len(sql) || sql[j] != '(' {
		return "", i, false, nil
	}
	j++
	for j < len(sql) && isQuerySpace(sql[j]) {
		j++
	}
	switch {
	case j < len(sql) && sql[j] == '"':
		k := j + 1
		var name strings.Builder
		for ; k < len(sql); k++ {
			if sql[k] == '"' {
				if k+1 < len(sql) && sql[k+1] == '"' {
					name.WriteByte('"')
					k++
					continue
				}
				break
			}
			name.WriteByte(sql[k])
		}
		if k >= len(sql) {
			return "", i, true, fmt.Errorf("the :audience mark at byte %d has an unclosed quoted alias", i-len("audience")-1)
		}
		alias, j = name.String(), k+1
	case j < len(sql) && isQueryIdentStart(sql[j]):
		k := j
		for k < len(sql) && isSQLIdentByte(sql[k]) {
			k++
		}
		alias, j = strings.ToLower(sql[j:k]), k
	}
	for j < len(sql) && isQuerySpace(sql[j]) {
		j++
	}
	if alias == "" || j >= len(sql) || sql[j] != ')' {
		return "", i, true, fmt.Errorf("the :audience mark at byte %d names no alias; write :audience(d) for the alias d of an @audience table the query reads", i-len("audience")-1)
	}
	return alias, j + 1, true, nil
}

func isQuerySpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n' || ch == '\r'
}

// queryRefEnds end a FROM's list at its depth.
var queryRefEnds = map[string]bool{
	"WHERE": true, "GROUP": true, "HAVING": true, "WINDOW": true, "ORDER": true, "LIMIT": true, "OFFSET": true,
	"FETCH": true, "FOR": true, "UNION": true, "INTERSECT": true, "EXCEPT": true, "RETURNING": true,
	"SET": true, "SELECT": true, "VALUES": true,
}

// queryAliasStops are the words after a table's name that are no alias.
var queryAliasStops = map[string]bool{
	"JOIN": true, "LEFT": true, "RIGHT": true, "FULL": true, "INNER": true, "CROSS": true, "NATURAL": true,
	"OUTER": true, "TABLESAMPLE": true, "WITH": true, "AS": true, "ON": true, "USING": true,
}

// tableRefs are the tables words read: each name after FROM, JOIN, UPDATE
// or a comma of a FROM's list, and its alias. A FROM of a function's
// arguments (EXTRACT(x FROM y), IS DISTINCT FROM) is read as one too,
// which names a table only where a column is named as one.
func tableRefs(words []queryWord) []QueryTableRef {
	var out []QueryTableRef
	from := map[int]bool{}
	ref := func(j int) {
		for j < len(words) && (words[j].upper == "ONLY" || words[j].upper == "LATERAL") {
			j++
		}
		if j >= len(words) || words[j].raw == "" || words[j].upper == "LITERAL" {
			return
		}
		name := words[j].raw
		for j+2 < len(words) && words[j+1].upper == "." && words[j+2].raw != "" {
			j += 2
			name = words[j].raw
		}
		j++
		if j < len(words) && words[j].upper == "(" {
			// A function's rows, such as generate_series(...).
			return
		}
		if j < len(words) && words[j].upper == "AS" {
			j++
		}
		alias := name
		if j < len(words) && words[j].raw != "" && words[j].upper != "LITERAL" && !queryAliasStops[words[j].upper] && !queryRefEnds[words[j].upper] {
			alias = words[j].raw
		}
		out = append(out, QueryTableRef{Name: name, Alias: alias})
	}
	for i, w := range words {
		switch {
		case w.upper == "FROM":
			if i > 0 && words[i-1].upper == "DISTINCT" {
				continue
			}
			from[w.depth] = true
			ref(i + 1)
		case w.upper == "JOIN", w.upper == "UPDATE":
			ref(i + 1)
		case w.upper == ",":
			if from[w.depth] {
				ref(i + 1)
			}
		case w.upper == ")":
			delete(from, w.depth+1)
		case queryRefEnds[w.upper]:
			delete(from, w.depth)
		}
	}
	return out
}

// audienceReads are what an @audience table, or a projection's view of
// one, is to the marks of the queries of a schema, by the name a query
// reads it by.
type audienceRead struct {
	// Table is the @audience table's type, or the view's; Model its conditions' columns,
	// qualified by its table, and for a view of one, the view's probe.
	Table string
	Model AudienceModel
	// Key is a view's key column; empty for a table.
	Key string
	// Through is the @audience table, by its name, a view reads that no
	// mark can hold: one it joins, or the source of a view not keyed by
	// its key.
	Through string
}

// audienceReadsOf are the schema's @audience tables and the projections'
// views that read one, by name.
func audienceReadsOf(schema *ir.Schema, dependencies map[string]*ir.Schema, tables []TableModel) (map[string]audienceRead, error) {
	out := map[string]audienceRead{}
	tableTypes := map[string]string{}
	for _, t := range tables {
		tableTypes[t.Type] = t.Name
		if td := schema.Types[t.Type]; td != nil && td.Audience != nil {
			out[t.Name] = audienceRead{Table: t.Type, Model: *AudienceOf(td)}
		}
	}
	views, err := ViewsOf(schema, dependencies)
	if err != nil {
		return nil, err
	}
	viewed := map[string]bool{}
	for _, v := range views {
		if v.Audience != nil {
			viewed[v.Name] = true
			out[v.Name] = audienceRead{Table: v.Type, Model: *v.Audience, Key: v.Key}
		}
	}
	for _, td := range schema.Projections() {
		def := td.Projection
		if def == nil || viewed[def.Name] {
			continue
		}
		for _, typ := range append([]string{def.Source}, joinTypes(def)...) {
			if read, ok := out[tableTypes[typ]]; ok && read.Key == "" && read.Through == "" {
				out[def.Name] = audienceRead{Through: tableTypes[typ]}
				break
			}
		}
	}
	return out, nil
}

// audienceMarksOf resolves scan's marks against the query's references to
// reads, and lists the references no mark holds.
func audienceMarksOf(scan QueryScan, reads map[string]audienceRead) ([]QueryAudienceMark, []string, error) {
	aliases := map[string]audienceRead{}
	for _, ref := range scan.TableRefs {
		if read, ok := reads[ref.Name]; ok {
			aliases[ref.Alias] = read
		}
	}
	var marks []QueryAudienceMark
	marked := map[string]bool{}
	for _, use := range scan.Audience {
		read, ok := aliases[use.Alias]
		if !ok {
			return nil, nil, fmt.Errorf(":audience(%s) names %s, which is no alias of an @audience table the query reads in a FROM or a JOIN", use.Alias, use.Alias)
		}
		if read.Through != "" {
			return nil, nil, fmt.Errorf(":audience(%s) names a view that reads the @audience table %s through a join, which no mark holds; read %s itself and mark it", use.Alias, read.Through, read.Through)
		}
		mark := QueryAudienceMark{Alias: use.Alias, Table: read.Table}
		if read.Key == "" {
			mark.Effective = QuoteIdentifier(use.Alias) + "." + QuoteIdentifier(codegen.ToSnakeCase(ir.EffectiveAudienceField))
			mark.Agents = QuoteIdentifier(use.Alias) + "." + QuoteIdentifier(codegen.ToSnakeCase(ir.EffectiveAgentsField))
		} else {
			mark.Effective, mark.Agents = read.Model.Effective, read.Model.Agents
			// The view's key, through the alias, in its source's rows.
			_, rest, _ := strings.Cut(read.Model.Probe, " IN (")
			mark.Probe = QuoteIdentifier(use.Alias) + "." + QuoteIdentifier(read.Key) + " IN (" + rest
		}
		marks = append(marks, mark)
		marked[use.Alias] = true
	}
	var unmarked []string
	for _, ref := range scan.TableRefs {
		read, ok := reads[ref.Name]
		if !ok || marked[ref.Alias] {
			continue
		}
		what := ref.Name
		if ref.Alias != ref.Name {
			what += " (as " + ref.Alias + ")"
		}
		if read.Through != "" {
			what += ", a view of the @audience table " + read.Through + " no mark can hold"
		}
		unmarked = append(unmarked, what)
	}
	return marks, unmarked, nil
}
