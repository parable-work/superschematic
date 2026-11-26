package typegen

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// FieldTypes returns the Go type of each field of the type named typeName
// of schema, as its Go types module declares the field, by field name:
// what a package that imports the types module names a field's value with,
// as a Go deriver's typed methods do (D146). Each name the types module
// declares itself, a struct, an enum, a union or a scalar's type, is
// qualified with prefix (`fixturedb.Category`). A type that names another
// package is refused: the package that names it imports the types module
// alone.
func FieldTypes(schema *ir.Schema, opts Options, typeName, prefix string) (map[string]string, error) {
	output, err := Generate(schema, opts)
	if err != nil {
		return nil, err
	}
	local := map[string]bool{}
	for _, t := range output.Types {
		local[t.Name] = true
	}
	for _, e := range output.Enums {
		local[e.Name] = true
	}
	for _, u := range output.Unions {
		local[u.Name] = true
	}
	for _, s := range output.Scalars {
		local[s.Name] = true
	}
	for _, t := range output.Types {
		if t.Name != typeName || t.IsInput {
			continue
		}
		out := map[string]string{}
		for _, f := range t.Fields {
			qualified, err := qualifyGoType(f.GoType, local, prefix)
			if err != nil {
				return nil, fmt.Errorf("typegen: %s.%s: %w", typeName, f.Name, err)
			}
			out[f.Name] = qualified
		}
		return out, nil
	}
	return nil, fmt.Errorf("typegen: %s declares no type %s that its Go types write as a struct", schema.Name, typeName)
}

// qualifyGoType prefixes each name of goType that local holds, and refuses
// a name another package qualifies.
func qualifyGoType(goType string, local map[string]bool, prefix string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(goType); {
		c := goType[i]
		if !isGoIdentByte(c) {
			b.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(goType) && isGoIdentByte(goType[j]) {
			j++
		}
		word := goType[i:j]
		if j < len(goType) && goType[j] == '.' {
			return "", fmt.Errorf("its Go type, %s, names package %s", goType, word)
		}
		if local[word] {
			b.WriteString(prefix)
		}
		b.WriteString(word)
		i = j
	}
	return b.String(), nil
}

// isGoIdentByte reports whether c may be part of a Go identifier.
func isGoIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
