package parse

import (
	"strconv"

	"github.com/parable-work/superschematic/ir"
)

// applyDefault parses FieldDef.Default into a value of the field's primitive
// type. Returns (value, ok). ok=false means the default could not be parsed
// and the caller should emit a {Validator: "default"} ValidationError.
//
// Defaults on object-typed fields, inputs, and arrays are intentionally NOT
// applied here in Phase C -- those defaults are not used in current schemas.
// Callers should skip applyDefault when the resolved kind is "type", "input",
// or the field is an array.
func applyDefault(field *ir.FieldDef, scalar *ir.ScalarDef) (any, bool) {
	if field.Default == nil {
		return nil, false
	}
	raw := *field.Default

	primitive := ""
	if scalar != nil {
		primitive = scalar.Primitive
	} else {
		primitive = primitiveForBuiltin(field.TypeRef.Name)
	}

	switch primitive {
	case "Int":
		i, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, false
		}
		return i, true
	case "Float":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, false
		}
		return f, true
	case "Boolean":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, false
		}
		return b, true
	default:
		return raw, true
	}
}

// primitiveForBuiltin returns the primitive identifier matching a built-in
// name: a GraphQL scalar's, or the IR's number, boolean and string, whose
// default text stands for a number, a boolean and the text itself (D14,
// amended: the loader checks defaults and examples by the validators'
// rules). Empty string is returned for other names so callers know to
// consult ScalarDef instead.
func primitiveForBuiltin(name string) string {
	switch name {
	case "Int":
		return "Int"
	case "Float", "number":
		return "Float"
	case "Boolean", "boolean":
		return "Boolean"
	case "String", "ID", "string":
		return "String"
	default:
		return ""
	}
}
