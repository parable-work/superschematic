package tswriter

import (
	"fmt"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// emitProjection renders a @projection<Source>({...}) class with its @join
// decorators and @column fields. Every IR form of a row rule has an
// authoring form; a rule that mixes forms is a writer error rather than a
// silently narrowed declaration.
func (e *emitter) emitProjection(def *ir.TypeDef) {
	p := def.Projection
	if p == nil {
		e.failf("type %s: Projection role without a projection declaration", def.Name)
		return
	}
	if def.Extends != "" || len(def.Implements) > 0 || def.RawHeritage != nil || def.IsTrait ||
		def.TraitConfig != nil || def.Source != nil || len(def.Indexes) > 0 || def.JsonField ||
		def.Versioned || def.EnvVars || def.DenyUnknownFields || def.StrictJSON {
		e.failf("type %s: a projection class carries only @projection and @join", def.Name)
		return
	}
	e.body.WriteString("\n")
	e.comment("", def.Comment)

	fmt.Fprintf(&e.body, "@%s<%s>({\n", e.use("projection"), e.renderTypeName(p.Source, def.Name))
	fmt.Fprintf(&e.body, "  pool: %s,\n  name: %s,\n  migration: %s,\n", quote(p.Pool), quote(p.Name), quote(p.Migration))
	if len(p.Predicates) > 0 {
		e.body.WriteString("  where: [\n")
		for _, pred := range p.Predicates {
			fmt.Fprintf(&e.body, "    %s,\n", e.projectionPredicateExpr(def.Name, pred))
		}
		e.body.WriteString("  ],\n")
	}
	if c := p.Collapse; c != nil {
		e.body.WriteString("  collapse: {\n")
		fmt.Fprintf(&e.body, "    by: [%s],\n", quotedList(c.By))
		if len(c.Order) > 0 {
			e.body.WriteString("    order: [\n")
			for _, term := range c.Order {
				fmt.Fprintf(&e.body, "      %s,\n", projectionOrderExpr(term))
			}
			e.body.WriteString("    ],\n")
		}
		e.body.WriteString("  },\n")
	}
	e.body.WriteString("})\n")
	for _, j := range p.Joins {
		on := make([]string, 0, len(j.On))
		for _, key := range j.On {
			on = append(on, fmt.Sprintf("%s: %s", quote(key.Left), quote(key.Right)))
		}
		kind := ""
		if j.Kind != "" && j.Kind != "inner" {
			kind = ", " + quote(j.Kind)
		}
		fmt.Fprintf(&e.body, "@%s<%s>(%s, { %s }%s)\n", e.use("join"), e.renderTypeName(j.Type, def.Name), quote(j.Alias), strings.Join(on, ", "), kind)
	}

	fmt.Fprintf(&e.body, "export abstract class %s {\n", e.ident(def.Name, "type"))
	first := true
	for _, fd := range def.Fields {
		fieldOwner := fmt.Sprintf("%s.%s", def.Name, fd.Name)
		plain := *fd
		plain.ProjectedFrom = ""
		plain.ProjectedFunction = nil
		e.checkStructField(&plain, fieldOwner)
		if !first {
			e.body.WriteString("\n")
		}
		first = false
		e.comment("  ", fd.Comment)
		switch call := fd.ProjectedFunction; {
		case call != nil:
			if fd.ProjectedFrom != "" {
				e.failf("%s: @column carries both a source and a function", fieldOwner)
			}
			fmt.Fprintf(&e.body, "  @%s({ function: %s, args: [%s] })\n", e.use("column"), quote(call.Function), quotedList(call.Args))
		case fd.ProjectedFrom != "":
			fmt.Fprintf(&e.body, "  @%s(%s)\n", e.use("column"), quote(fd.ProjectedFrom))
		}
		fmt.Fprintf(&e.body, "  %s: %s;\n", e.ident(fd.Name, "field"), e.fieldTypeExpr(&plain, fieldOwner, structField))
	}
	e.body.WriteString("}\n")
}

// projectionPredicateExpr renders one where rule in its authoring form.
func (e *emitter) projectionPredicateExpr(owner string, pred *ir.ProjectionPredicate) string {
	var parts []string
	switch {
	case pred.IsLiteral():
		if pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 || pred.Function != "" {
			e.failf("%s: literal rule on %s also carries a setting binding", owner, pred.Column)
		}
		parts = append(parts, fmt.Sprintf("column: %s", quote(pred.Column)))
		switch {
		case pred.IsNull:
			parts = append(parts, "isNull: true")
		case pred.NotNull:
			parts = append(parts, "notNull: true")
		case pred.Equals.String != nil:
			parts = append(parts, fmt.Sprintf("equals: %s", quote(*pred.Equals.String)))
		case pred.Equals.Bool != nil:
			parts = append(parts, fmt.Sprintf("equals: %t", *pred.Equals.Bool))
		case pred.Equals.Number != nil:
			parts = append(parts, fmt.Sprintf("equals: %s", strconv.FormatFloat(*pred.Equals.Number, 'f', -1, 64)))
		default:
			e.failf("%s: equals rule on %s has no literal", owner, pred.Column)
		}
	case pred.Function != "":
		if pred.Column != "" || pred.Setting != "" || pred.Optional || len(pred.AnyOf) > 0 {
			e.failf("%s: function rule %s also carries a column binding", owner, pred.Function)
		}
		parts = append(parts,
			fmt.Sprintf("function: %s", quote(pred.Function)),
			fmt.Sprintf("args: [%s]", quotedList(pred.Args)),
			fmt.Sprintf("requiredSettings: [%s]", quotedList(pred.RequiredSettings)))
	case len(pred.AnyOf) > 0:
		if pred.Column != "" || pred.Setting != "" || pred.Optional {
			e.failf("%s: anyOf rule also carries a column binding", owner)
		}
		alts := make([]string, 0, len(pred.AnyOf))
		for _, alt := range pred.AnyOf {
			if alt.When != nil || len(alt.AnyOf) > 0 || alt.Function != "" || alt.IsLiteral() {
				e.failf("%s: anyOf alternative on %s carries more than column, setting and optional", owner, alt.Column)
			}
			alts = append(alts, e.projectionPredicateExpr(owner, alt))
		}
		parts = append(parts, fmt.Sprintf("anyOf: [%s]", strings.Join(alts, ", ")))
	default:
		if len(pred.Args) > 0 || len(pred.RequiredSettings) > 0 {
			e.failf("%s: args and requiredSettings on %s require function", owner, pred.Column)
		}
		parts = append(parts, fmt.Sprintf("column: %s", quote(pred.Column)), fmt.Sprintf("setting: %s", quote(pred.Setting)))
		if pred.Optional {
			parts = append(parts, "optional: true")
		}
	}
	if pred.When != nil {
		parts = append(parts, fmt.Sprintf("when: { column: %s, equals: %s }", quote(pred.When.Column), quote(pred.When.Equals)))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// projectionOrderExpr renders one collapse.order term.
func projectionOrderExpr(term *ir.ProjectionOrder) string {
	parts := []string{fmt.Sprintf("column: %s", quote(term.Column))}
	if len(term.Rank) > 0 {
		parts = append(parts, fmt.Sprintf("rank: [%s]", quotedList(term.Rank)))
	} else {
		if term.Direction != "" {
			parts = append(parts, fmt.Sprintf("direction: %s", quote(term.Direction)))
		}
		if term.Nulls != "" {
			parts = append(parts, fmt.Sprintf("nulls: %s", quote(term.Nulls)))
		}
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = quote(v)
	}
	return strings.Join(quoted, ", ")
}
