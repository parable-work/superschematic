package typegen

import (
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// UnionReads is the structural predicate the module's unions pick their
// members by (codegen.ReadModel, D31 amended), as unions.go writes it:
// unionReadTypes, unionReadUnions and a check per scalar a field holds.
type UnionReads struct {
	TypesSource  string
	UnionsSource string
	ScalarChecks []UnionReadScalarCheck
}

// UnionReadScalarCheck is one scalar's check in unions.go: the value
// decodes into the scalar's Go type, and its validator, when the type has
// one, passes it.
type UnionReadScalarCheck struct {
	Func   string
	Symbol string
	// Validate is the call that validates value, the decoded scalar, or ""
	// when the Go type has no validator.
	Validate string
}

// buildUnionReads renders the read model for Go. A scalar field's check
// decodes the value into the scalar's Go type and runs the scalar's own
// validator, as types.go's Validate runs it; a scalar this module does not
// generate is checked by its JSON kind alone.
func buildUnionReads(model *codegen.ReadModel, output *ModuleOutput) *UnionReads {
	reads := &UnionReads{}
	valueChecks := map[string]bool{}
	for _, check := range output.ScalarValueChecks() {
		valueChecks[check.Symbol] = true
	}
	fieldOf := map[string]FieldInfo{}
	for _, typeInfo := range output.Types {
		for _, field := range typeInfo.Fields {
			if field.IsScalar && field.ScalarInfo != nil && !field.IsUnion {
				if _, seen := fieldOf[field.ScalarInfo.Name]; !seen {
					fieldOf[field.ScalarInfo.Name] = field
				}
			}
		}
	}
	checkFuncs := map[string]string{}
	for _, name := range model.ScalarNames() {
		field, ok := fieldOf[name]
		if !ok {
			continue
		}
		symbol := scalarSymbol(field)
		check := UnionReadScalarCheck{Func: "unionReadScalar" + symbol, Symbol: symbol}
		switch {
		case valueChecks[symbol]:
			check.Validate = "validate" + symbol + "Value(value, false)"
		case isValidatableScalarField(field):
			check.Validate = "value.Validate()"
		}
		checkFuncs[name] = check.Func
		reads.ScalarChecks = append(reads.ScalarChecks, check)
	}

	var b strings.Builder
	b.WriteString("map[string]unionReadType{\n")
	for _, t := range model.Types {
		b.WriteString(strconv.Quote(t.Name) + ": {strict: " + strconv.FormatBool(t.Strict) + ", fields: []unionReadField{\n")
		for _, f := range t.Fields {
			parts := []string{
				"name: " + strconv.Quote(f.Name),
				"required: " + strconv.FormatBool(f.Required),
				"depth: " + strconv.Itoa(f.Depth),
				"isMap: " + strconv.FormatBool(f.Map),
				"kind: " + strconv.Quote(f.Kind),
			}
			switch f.Kind {
			case codegen.ReadEnum:
				parts = append(parts, "values: "+goStringSlice(f.Values))
			case codegen.ReadScalar:
				parts = append(parts, "json: "+strconv.Quote(f.ScalarJSON))
				if fn := checkFuncs[f.Scalar]; fn != "" {
					parts = append(parts, "scalar: "+fn)
				}
			case codegen.ReadObject, codegen.ReadUnionKind:
				parts = append(parts, "ref: "+strconv.Quote(f.Ref))
			}
			b.WriteString("{" + strings.Join(parts, ", ") + "},\n")
		}
		b.WriteString("}},\n")
	}
	b.WriteString("}")
	reads.TypesSource = b.String()

	b.Reset()
	b.WriteString("map[string]unionReadUnion{\n")
	for _, u := range model.Unions {
		b.WriteString(strconv.Quote(u.Name) + ": {discriminator: " + strconv.Quote(u.Discriminator) + ", members: []unionReadMember{\n")
		for _, m := range u.Members {
			tags := make([]string, len(m.Tags))
			for i, tag := range m.Tags {
				tags[i] = "{" + strconv.Quote(tag.Field) + ", " + strconv.Quote(tag.Value) + "}"
			}
			b.WriteString("{typeName: " + strconv.Quote(m.Type) + ", tag: " + strconv.Quote(m.Tag) +
				", fields: " + goStringSlice(m.Fields) + ", tags: [][2]string{" + strings.Join(tags, ", ") + "}},\n")
		}
		b.WriteString("}},\n")
	}
	b.WriteString("}")
	reads.UnionsSource = b.String()
	return reads
}

// goStringSlice writes values as a Go []string literal.
func goStringSlice(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = strconv.Quote(v)
	}
	return "[]string{" + strings.Join(quoted, ", ") + "}"
}
