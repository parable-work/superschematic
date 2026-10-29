// Shared helpers for parsing @default values into language-typed literals.
package codegen

import (
	"strconv"

	ir "github.com/parable-work/superschematic/ir"
)

// EnumLookup reports whether a type name refers to an enum visible from the
// current module (either declared locally or imported from a dependency).
// Per-language generators construct an EnumLookup from their list of local +
// imported enums and pass it into ClassifyDefault.
type EnumLookup func(typeName string) bool

// DefaultLiteralKind classifies how the raw @default value string should be
// parsed and rendered as a typed literal.
type DefaultLiteralKind int

const (
	// DefaultLiteralUnknown means the field type cannot carry a default
	// literal in the current pipeline (array, map, union, object reference,
	// or a scalar without a recognised primitive). Generators skip emission.
	DefaultLiteralUnknown DefaultLiteralKind = iota
	DefaultLiteralBool
	DefaultLiteralInt
	DefaultLiteralFloat
	DefaultLiteralString
	DefaultLiteralEnum
	DefaultLiteralEmptyArray
)

// DefaultNow is the V of Default<T, V> that makes a required date, time or
// timestamp field of a DB table take the time of the insert (D88): its
// column's DEFAULT is CURRENT_TIMESTAMP, CURRENT_DATE or CURRENT_TIME. The
// database fills it in, so it is no decoder's default.
const DefaultNow = "now"

// IsDefaultNow reports whether value, the default of a single field of the
// named type, is DefaultNow on a date, time or timestamp scalar.
func IsDefaultNow(typeName string, value *string) bool {
	if value == nil || *value != DefaultNow {
		return false
	}
	traits := GuessScalarTraits(typeName)
	return traits.IsDateTimeLike || traits.IsDateLike || traits.IsTimeLike
}

// ClassifyDefault determines the literal kind for a field's default value.
// Fields with no default, or whose shape cannot be expressed as a single
// scalar literal (arrays, maps, unions, relations, object refs, or scalars
// without a recognised primitive), return DefaultLiteralUnknown, as does
// DefaultNow, which the database fills in.
func ClassifyDefault(field FieldInfo, enumLookup EnumLookup) DefaultLiteralKind {
	if field.Default == nil || (!field.IsArray && !field.IsMap && IsDefaultNow(field.Type, field.Default)) {
		return DefaultLiteralUnknown
	}
	if field.IsArray {
		if *field.Default == "[]" {
			return DefaultLiteralEmptyArray
		}
		return DefaultLiteralUnknown
	}
	if field.IsMap || field.IsUnion || field.IsRelation {
		return DefaultLiteralUnknown
	}

	if enumLookup != nil && enumLookup(field.Type) {
		return DefaultLiteralEnum
	}

	switch fieldPrimitive(field) {
	case ir.LanguageBoolean:
		return DefaultLiteralBool
	case ir.LanguageNumber:
		// The schema's number primitive carries no int/float split; classify
		// by the literal itself so generators can render integers cleanly.
		if _, err := strconv.ParseInt(*field.Default, 10, 64); err == nil {
			return DefaultLiteralInt
		}
		return DefaultLiteralFloat
	case ir.LanguageString:
		return DefaultLiteralString
	}

	return DefaultLiteralUnknown
}

// fieldPrimitive returns the host-language primitive for the field, or ""
// if the field's type is an object, union, or scalar without a primitive.
func fieldPrimitive(field FieldInfo) ir.LanguagePrimitive {
	if field.IsScalar && field.ScalarInfo != nil && field.ScalarInfo.Primitive != "" {
		return field.ScalarInfo.Primitive
	}
	return PrimitiveOf(field.Type)
}
