package codegen

import (
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// Language primitive type-reference names. Fields in the schema language may
// be typed directly with a host-language primitive ("email: string") instead
// of a semantic scalar; these are the only bare primitives the IR carries.
// There are no GraphQL builtins (String/Int/Float/Boolean/ID) in v2.
const (
	PrimitiveString  = "string"
	PrimitiveNumber  = "number"
	PrimitiveBoolean = "boolean"
)

// IsLanguagePrimitive reports whether a TypeRef name is a bare language
// primitive rather than a named definition.
func IsLanguagePrimitive(name string) bool {
	switch name {
	case PrimitiveString, PrimitiveNumber, PrimitiveBoolean:
		return true
	}
	return false
}

// PrimitiveOf returns the LanguagePrimitive for a bare primitive TypeRef
// name, or "" when the name is not a primitive.
func PrimitiveOf(name string) ir.LanguagePrimitive {
	switch name {
	case PrimitiveString:
		return ir.LanguageString
	case PrimitiveNumber:
		return ir.LanguageNumber
	case PrimitiveBoolean:
		return ir.LanguageBoolean
	}
	return ""
}

// GoOptionalBoolIsPointer reports whether generated Go types represent a
// boolean field as *bool: a single, non-required boolean with no declared
// default. encoding/json's omitempty drops a false bool, so only a pointer
// keeps an explicit false distinct from an absent value. typegen, ormgen and
// envgen share this rule so a type, its repository and its env loader agree
// on the Go type.
func GoOptionalBoolIsPointer(typeName string, required, isArray, isMap bool, def *string) bool {
	return typeName == PrimitiveBoolean && !required && !isArray && !isMap && def == nil
}

// GoOptionalIntegerScalarIsPointer reports whether generated Go output types
// represent an integer-like scalar field (Generic.Int64, Temporal.Seconds,
// Network.Port and the other scalars whose Go type is an integer) as a
// pointer: a single, non-required field with no declared default. omitempty
// drops a 0 integer, so only a pointer keeps an explicit 0 distinct from an
// absent value. With a declared default the field stays a value type. The
// caller has already established that the field is an integer-like scalar.
// typegen, ormgen and envgen share this rule.
func GoOptionalIntegerScalarIsPointer(required, isArray, isMap bool, def *string) bool {
	return !required && !isArray && !isMap && def == nil
}

// GoOptionalShapes is how the generated Go types hold a single optional
// field of a primitive or an integer-like scalar: the naming file's
// go.optional_zero (D161). The zero value is the core's rule above.
// typegen, ormgen and envgen ask it, so a type, its repository and its
// env loader agree on the Go type.
type GoOptionalShapes struct {
	// KeepZero is go.optional_zero = "keep": a Go type keeps every value
	// a payload states. A field whose value type would lose an explicit
	// zero to omitempty, or read an explicit null as its default, is a
	// pointer instead:
	//
	//   - every optional scalar, with or without a default;
	//   - an optional boolean or number without a default;
	//   - an optional boolean, number or string whose default is not the
	//     Go zero value, outside an @envVars class. An explicit null then
	//     decodes to nil, an absent key to a pointer to the default.
	//
	// An @envVars field with a default stays a value: its loader reads
	// text, and an unset variable is absent, never null. A value whose
	// default is not its zero value drops omitempty instead
	// (GoKeepsZeroValue).
	KeepZero bool
}

// PrimitiveIsPointer reports whether a boolean, number or string field is
// *bool, *float64 or *string. Under the core's rule only a boolean without
// a default is (GoOptionalBoolIsPointer).
func (s GoOptionalShapes) PrimitiveIsPointer(typeName string, required, isArray, isMap bool, def *string, envVars bool) bool {
	if !s.KeepZero {
		return GoOptionalBoolIsPointer(typeName, required, isArray, isMap, def)
	}
	if required || isArray || isMap {
		return false
	}
	switch typeName {
	case PrimitiveBoolean, PrimitiveNumber:
		if def == nil {
			return true
		}
	case PrimitiveString:
		if def == nil {
			return false
		}
	default:
		return false
	}
	return !envVars && !primitiveDefaultIsZero(typeName, *def)
}

// IntegerScalarIsPointer reports whether an optional integer-like scalar
// field keeps the pointer every optional scalar has. Under the core's rule
// one with a declared default is a value, unless its null is distinct
// (ir.FieldDef.DistinctNull).
func (s GoOptionalShapes) IntegerScalarIsPointer(required, isArray, isMap bool, def *string, distinctNull bool) bool {
	if s.KeepZero || distinctNull {
		return !required && !isArray && !isMap
	}
	return GoOptionalIntegerScalarIsPointer(required, isArray, isMap, def)
}

// primitiveDefaultIsZero reports whether a boolean, number or string
// default is the Go zero value: false, 0 or "". A boolean or number
// default that does not parse counts as zero, so the field keeps its
// value type.
func primitiveDefaultIsZero(typeName, def string) bool {
	trimmed := strings.TrimSpace(def)
	switch typeName {
	case PrimitiveBoolean:
		return !strings.EqualFold(trimmed, "true")
	case PrimitiveNumber:
		value, err := strconv.ParseFloat(trimmed, 64)
		return err != nil || value == 0
	}
	return def == ""
}
