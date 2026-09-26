// Package jsonshape tells a JSON object from a JSON array among the Go
// values the runtime receives, for the scalars whose value is one
// (ir.ScalarDef.StructuredJSONType).
package jsonshape

import (
	"reflect"

	"github.com/parable-work/superschematic/ir"
)

// Of returns ir.JSONSchemaObjectType for a map with string keys (what
// encoding/json decodes an object into, or a typed map a caller built),
// ir.JSONSchemaArrayType for a slice or array other than []byte (which
// encoding/json writes as a string), and "" for anything else, nil
// included.
func Of(value any) string {
	switch value.(type) {
	case map[string]any:
		return ir.JSONSchemaObjectType
	case []any:
		return ir.JSONSchemaArrayType
	case nil, []byte:
		return ""
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Map:
		if rv.Type().Key().Kind() == reflect.String {
			return ir.JSONSchemaObjectType
		}
	case reflect.Slice, reflect.Array:
		return ir.JSONSchemaArrayType
	}
	return ""
}

// Noun is how a validation message names a shape: "a JSON object" or "a
// JSON array".
func Noun(shape string) string {
	if shape == ir.JSONSchemaArrayType {
		return "a JSON array"
	}
	return "a JSON object"
}
