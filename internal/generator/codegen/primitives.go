package codegen

import (
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
