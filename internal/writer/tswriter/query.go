package tswriter

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// emitQuery renders a RoleQuery type (D86) as the abstract class
// @query<Params, Row>({ ... }) decorates, with no fields. Its params and row
// types are embedded structs, which a DB schema writes as object type
// aliases, and which @query names as its type arguments.
func (e *emitter) emitQuery(def *ir.TypeDef) {
	q := def.Query
	if q == nil {
		e.failf("type %s: Query role without a query declaration", def.Name)
		return
	}
	if len(def.Fields) > 0 || def.Extends != "" || len(def.Implements) > 0 || def.RawHeritage != nil || def.User != nil || def.UserRole != nil ||
		def.IsTrait || def.TraitConfig != nil || def.Source != nil || len(def.Indexes) > 0 || len(def.RawSQL) > 0 || def.JsonField ||
		def.Versioned || def.Optimistic || def.EnvVars || def.DenyUnknownFields || def.StrictJSON || len(def.Behaviors) > 0 || def.SQLName != "" {
		e.failf("type %s: a query class carries @query alone, and no fields", def.Name)
		return
	}
	e.body.WriteString("\n")
	e.comment("", def.Comment)
	fmt.Fprintf(&e.body, "@%s<%s, %s>(%s)\n", e.use("query"), e.renderTypeName(q.Params, def.Name), e.renderTypeName(q.Row, def.Name), queryArgs(def.Name, q))
	fmt.Fprintf(&e.body, "export abstract class %s {}\n", e.ident(def.Name, "type"))
}

// queryArgs renders @query's options object, its SQL as a template literal
// when it spans lines and holds nothing a template literal reads.
func queryArgs(className string, q *ir.QueryDef) string {
	sql := q.SQL
	literal := quote(sql)
	if strings.Contains(sql, "\n") && !strings.ContainsAny(sql, "`\\") && !strings.Contains(sql, "${") {
		literal = "`" + sql + "`"
	}
	var parts []string
	if q.Name != lowerFirst(className) {
		parts = append(parts, "name: "+quote(q.Name))
	}
	parts = append(parts, "sql: "+literal)
	if len(q.NotNull) > 0 {
		parts = append(parts, "notNull: ["+quotedList(q.NotNull)+"]")
	}
	if o := q.OrderBy; o != nil {
		options := make([]string, 0, len(o.Options))
		for _, value := range o.OptionValues() {
			options = append(options, quote(value)+": "+quote(o.Options[value]))
		}
		order := "orderBy: { param: " + quote(o.Param) + ", options: { " + strings.Join(options, ", ") + " }"
		if o.Default != "" {
			order += ", default: " + quote(o.Default)
		}
		parts = append(parts, order+" }")
	}
	if p := q.Page; p != nil {
		page := "page: { key: [" + quotedList(p.Key) + "]"
		if p.Desc {
			page += ", desc: true"
		}
		parts = append(parts, page+" }")
	}
	if returns := q.Returning(); returns != ir.QueryReturnsMany {
		parts = append(parts, "returns: "+quote(returns))
	}
	if len(q.Dialects) > 0 {
		parts = append(parts, "dialects: ["+quotedList(q.Dialects)+"]")
	}
	return "{\n  " + strings.Join(parts, ",\n  ") + ",\n}"
}

// lowerFirst is name with its first letter in lower case, as a query's
// default name is its class's.
func lowerFirst(name string) string {
	if name == "" || name[0] < 'A' || name[0] > 'Z' {
		return name
	}
	return string(name[0]+'a'-'A') + name[1:]
}
