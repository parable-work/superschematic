// Package behaviortypes types the fields, operations and create parameters
// a behavior adds to a type (D16), as the type's config narrows its
// declaration: Workflow's states, Links' names, Dependencies' blocker
// schemas, Timers' deadlines. It is the one narrowing the generators that
// render behaviors share: engine-client's TypeScript module (D49) and the
// types generator's <T>Behaviors in Go, TypeScript, Rust and Python.
//
// Narrow starts from a declaration's JSON Schemas and applies a core
// behavior's narrowing; a behavior without one, an extension's, keeps its
// declaration's shapes, and its fields hold any JSON. The engine narrows
// the create parameters again in TypeScript (createParamsSchema(config)),
// and its test suite holds the two to each other through
// runtime/engine/testdata/client_codegen_parity.json, which engineclientgen
// writes.
//
// TSRenderer renders a narrowed JSON Schema as a TypeScript type; Build
// models each type's behaviors' fields as structs and enums for the
// languages that need named types (Go, Rust, Python).
package behaviortypes

import (
	"encoding/json"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// JSONKey is the key a field's value has in an instance.
func JSONKey(field *ir.FieldDef) string {
	if field.JSONTag != "" {
		return field.JSONTag
	}
	return field.Name
}

// OwnField finds an own field of a type by its JSON key.
func OwnField(td *ir.TypeDef, key string) *ir.FieldDef {
	for _, field := range td.Fields {
		if field != nil && JSONKey(field) == key {
			return field
		}
	}
	return nil
}

// ScalarJSONType is the JSON type a scalar's values have: any, object or
// array for a scalar whose json_schema mapping says so (ir.ScalarDef's
// IsAnyJSON and StructuredJSONType), else its mapping or its primitive's.
func ScalarJSONType(scalar *ir.ScalarDef) string {
	if scalar.IsAnyJSON() {
		return "any"
	}
	if structured := scalar.StructuredJSONType(); structured != "" {
		return structured
	}
	switch mapped := scalar.TypeMappings["json_schema"]; mapped {
	case "number", "integer", "boolean", "string":
		return mapped
	case "":
	default:
		return "string"
	}
	switch scalar.LanguagePrimitive {
	case ir.LanguageNumber:
		return "number"
	case ir.LanguageBoolean:
		return "boolean"
	}
	return "string"
}

// decodeSchema decodes a declaration's JSON Schema into a tree the
// narrowings copy and change.
func decodeSchema(raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// SortedKeys lists a map's keys in order.
func SortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// An writes a name with its indefinite article: a Note, an Item. The
// article follows the name's first letter, which is what a type's name
// is read as.
func An(name string) string {
	if name != "" && strings.ContainsRune("AEIOUaeiou", rune(name[0])) {
		return "an " + name
	}
	return "a " + name
}

// WithoutDescription copies a schema's top node without its description,
// which the caller has written as the member's doc comment.
func WithoutDescription(schema any) any {
	node, ok := schema.(map[string]any)
	if !ok {
		return schema
	}
	out := make(map[string]any, len(node))
	for key, value := range node {
		if key != "description" {
			out[key] = value
		}
	}
	return out
}
