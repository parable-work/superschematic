package pygen

import (
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// UnionReads is the structural predicate the module's unions pick their
// members by (codegen.ReadModel, D31 amended), as unions.py writes it:
// _READ_TYPES and _READ_UNIONS, and the scalars a scalar field's check
// reads through a pydantic TypeAdapter.
type UnionReads struct {
	ScalarSymbols []string
	TypesSource   string
	UnionsSource  string
}

// buildUnionReads renders the read model for Python. A scalar field's check
// first holds the value to the scalar's JSON kind, so pydantic coerces
// nothing, then reads it as the module's alias of the scalar; a scalar the
// module does not generate is held to its JSON kind alone.
func buildUnionReads(model *codegen.ReadModel, scalars []codegen.ScalarInfo) *UnionReads {
	symbols := map[string]string{}
	for _, s := range scalars {
		if symbol := pythonScalarSymbol(s); symbol != "" {
			symbols[s.Name] = symbol
		}
	}
	reads := &UnionReads{}
	seen := map[string]bool{}
	for _, name := range model.ScalarNames() {
		if symbol := symbols[name]; symbol != "" && !seen[symbol] {
			seen[symbol] = true
			reads.ScalarSymbols = append(reads.ScalarSymbols, symbol)
		}
	}

	var b strings.Builder
	b.WriteString("{\n")
	for _, t := range model.Types {
		b.WriteString("    " + strconv.Quote(t.Name) + ": _ReadType(\n        strict=" + pyBool(t.Strict) + ",\n        fields=(\n")
		for _, f := range t.Fields {
			parts := []string{
				"name=" + strconv.Quote(f.Name),
				"required=" + pyBool(f.Required),
				"depth=" + strconv.Itoa(f.Depth),
				"is_map=" + pyBool(f.Map),
				"kind=" + strconv.Quote(f.Kind),
			}
			switch f.Kind {
			case codegen.ReadEnum:
				parts = append(parts, "values=("+pyStrings(f.Values)+")")
			case codegen.ReadScalar:
				parts = append(parts, "json="+strconv.Quote(f.ScalarJSON))
				if symbol := symbols[f.Scalar]; symbol != "" {
					parts = append(parts, "scalar="+symbol)
				}
			case codegen.ReadObject, codegen.ReadUnionKind:
				parts = append(parts, "ref="+strconv.Quote(f.Ref))
			}
			b.WriteString("            _ReadField(" + strings.Join(parts, ", ") + "),\n")
		}
		b.WriteString("        ),\n    ),\n")
	}
	b.WriteString("}")
	reads.TypesSource = b.String()

	b.Reset()
	b.WriteString("{\n")
	for _, u := range model.Unions {
		discriminator := "None"
		if u.IsDiscriminated() {
			discriminator = strconv.Quote(u.Discriminator)
		}
		b.WriteString("    " + strconv.Quote(u.Name) + ": _ReadUnion(\n        discriminator=" + discriminator + ",\n        members=(\n")
		for _, m := range u.Members {
			tags := make([]string, len(m.Tags))
			for i, tag := range m.Tags {
				tags[i] = "(" + strconv.Quote(tag.Field) + ", " + strconv.Quote(tag.Value) + ")"
			}
			b.WriteString("            _ReadMember(type_name=" + strconv.Quote(m.Type) + ", tag=" + strconv.Quote(m.Tag) +
				", fields=(" + pyStrings(m.Fields) + "), tags=(" + strings.Join(append(tags, ""), ", ") + ")),\n")
		}
		b.WriteString("        ),\n    ),\n")
	}
	b.WriteString("}")
	reads.UnionsSource = b.String()
	return reads
}

func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

// pyStrings writes values as the items of a Python tuple of strings, each
// followed by a comma so a single one stays a tuple.
func pyStrings(values []string) string {
	var b strings.Builder
	for i, v := range values {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(strconv.Quote(v) + ",")
	}
	return b.String()
}
