package rustgen

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// FieldTypes returns the Rust type of each field of the type named
// typeName of schema, as its types crate declares the field when it holds
// a value, without the Option an optional field takes, by field name: what
// a crate that depends on the types crate names a field's value with, as a
// deriver's typed methods do (D74). Each name the types crate declares
// itself, a struct, an enum, a union or a scalar's alias, which it exports
// from its root, is qualified with prefix (`database_types::Category`); a
// path the crate names another crate by stays as it is.
func FieldTypes(schema *ir.Schema, typeName, prefix string) (map[string]string, error) {
	extraction := codegen.ExtractionConfig{
		Language:            "rust",
		PrimitiveMapper:     inferRustType,
		FieldTypeMapper:     fieldTypeMapperRust,
		FieldNameMapper:     toRustFieldName,
		ScalarPostProcessor: refineRustScalarType,
	}
	scalars := codegen.ExtractScalars(schema, extraction)
	types := codegen.ExtractTypes(schema, scalars, extraction, localObjectRoles...)
	local := map[string]bool{}
	for _, t := range types {
		local[t.Name] = true
	}
	for name := range schema.Enums {
		local[name] = true
	}
	for name := range schema.Unions {
		local[name] = true
	}
	for _, s := range convertScalars(scalars, "") {
		local[s.Name] = true
	}
	for _, t := range types {
		if t.Name != typeName {
			continue
		}
		out := map[string]string{}
		for _, f := range t.Fields {
			out[f.Name] = qualifyRustType(f.TargetType, local, prefix)
		}
		return out, nil
	}
	return nil, fmt.Errorf("rustgen: %s declares no type %s that its Rust types write as a struct", schema.Name, typeName)
}

// qualifyRustType prefixes each name of rustType that local holds and no
// path qualifies.
func qualifyRustType(rustType string, local map[string]bool, prefix string) string {
	var b strings.Builder
	for i := 0; i < len(rustType); {
		if !isIdentByte(rustType[i]) {
			b.WriteByte(rustType[i])
			i++
			continue
		}
		j := i
		for j < len(rustType) && isIdentByte(rustType[j]) {
			j++
		}
		word := rustType[i:j]
		qualified := strings.HasSuffix(b.String(), "::")
		if local[word] && !qualified {
			b.WriteString(prefix)
		}
		b.WriteString(word)
		i = j
	}
	return b.String()
}

// isIdentByte reports whether c may be part of a Rust identifier.
func isIdentByte(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}
