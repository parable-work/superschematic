package tsgen

import (
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// UnionReads is the structural predicate every union of the package picks
// its member by (codegen.ReadModel, D31 amended), as validators/unions.ts
// writes it: READ_TYPES and READ_UNIONS, the scalar validators a scalar
// field's check imports, and the source of the two tables.
type UnionReads struct {
	ScalarImports []ModuleImport
	TypesSource   string
	UnionsSource  string
}

// buildUnionReads renders the read model for TypeScript. A scalar field's
// check is the package's own validate<Scalar>, the scalar's format; a
// scalar of a dependency's type that the package does not generate is
// checked by its JSON kind alone.
func buildUnionReads(model *codegen.ReadModel, scalars []ScalarInfo) *UnionReads {
	byName := make(map[string]ScalarInfo, len(scalars))
	for _, s := range scalars {
		byName[s.Name] = s
	}
	reads := &UnionReads{}
	var imports []NestedImport
	for _, name := range model.ScalarNames() {
		if s, ok := byName[name]; ok && strings.TrimSpace(s.Symbol) != "" {
			imports = append(imports, NestedImport{Name: "validate" + strings.TrimSpace(s.Symbol), Module: "./scalars/" + s.Module})
		}
	}
	reads.ScalarImports = groupImports(imports)

	var b strings.Builder
	b.WriteString("{\n")
	for _, t := range model.Types {
		b.WriteString("  " + strconv.Quote(t.Name) + ": {\n")
		b.WriteString("    strict: " + strconv.FormatBool(t.Strict) + ",\n")
		b.WriteString("    fields: [\n")
		for _, f := range t.Fields {
			parts := []string{
				"name: " + strconv.Quote(f.Name),
				"required: " + strconv.FormatBool(f.Required),
				"depth: " + strconv.Itoa(f.Depth),
				"map: " + strconv.FormatBool(f.Map),
				"kind: " + strconv.Quote(f.Kind),
			}
			switch f.Kind {
			case codegen.ReadEnum:
				parts = append(parts, "values: "+tsStringArray(f.Values))
			case codegen.ReadScalar:
				parts = append(parts, "json: "+strconv.Quote(f.ScalarJSON))
				if s, ok := byName[f.Scalar]; ok && strings.TrimSpace(s.Symbol) != "" {
					parts = append(parts, "scalar: (value: unknown) => validate"+strings.TrimSpace(s.Symbol)+"(value as never)[0]")
				}
			case codegen.ReadObject, codegen.ReadUnionKind:
				parts = append(parts, "ref: "+strconv.Quote(f.Ref))
			}
			b.WriteString("      { " + strings.Join(parts, ", ") + " },\n")
		}
		b.WriteString("    ],\n  },\n")
	}
	b.WriteString("}")
	reads.TypesSource = b.String()

	b.Reset()
	b.WriteString("{\n")
	for _, u := range model.Unions {
		b.WriteString("  " + strconv.Quote(u.Name) + ": {\n")
		if u.IsDiscriminated() {
			b.WriteString("    discriminator: " + strconv.Quote(u.Discriminator) + ",\n")
		}
		b.WriteString("    members: [\n")
		for _, m := range u.Members {
			parts := []string{"type: " + strconv.Quote(m.Type)}
			if u.IsDiscriminated() {
				parts = append(parts, "tag: "+strconv.Quote(m.Tag))
			} else {
				parts = append(parts, "fields: "+tsStringArray(m.Fields))
				tags := make([]string, len(m.Tags))
				for i, tag := range m.Tags {
					tags[i] = "[" + strconv.Quote(tag.Field) + ", " + strconv.Quote(tag.Value) + "]"
				}
				parts = append(parts, "tags: ["+strings.Join(tags, ", ")+"]")
			}
			b.WriteString("      { " + strings.Join(parts, ", ") + " },\n")
		}
		b.WriteString("    ],\n  },\n")
	}
	b.WriteString("}")
	reads.UnionsSource = b.String()
	return reads
}

// tsStringArray writes values as a TypeScript array literal of strings.
func tsStringArray(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
