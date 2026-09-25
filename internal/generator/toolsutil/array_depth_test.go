package toolsutil

import (
	"encoding/json"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestFieldArrayOfArraysSchema: a T[][] field renders as an array of arrays
// of T. Its value constraints go on the innermost items, its list bounds on
// the outer array, and only the outer array is nullable when the field is
// optional: an inner list is never null.
func TestFieldArrayOfArraysSchema(t *testing.T) {
	minimum, maximum := 0.0, 9.0
	minLength := 2
	listMin, listMax := 1, 4
	field := apigen.Param{
		Name: "cells", Type: "string", IsArray: true, IsArrayOfArrays: true,
		ValidateMin: &minimum, ValidateMax: &maximum, ValidateMinLength: &minLength,
		ValidateListMin: &listMin, ValidateListMax: &listMax, ValidatePattern: "^[a-z]+$",
	}
	got := FieldToJSONSchemaProperty(field, map[string]apigen.ScalarJSONSchemaInfo{"string": {Type: "string"}}, nil, nil, nil, nil)
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"description":"Array of arrays of string values","minItems":1,"maxItems":4,` +
		`"items":{"type":"array","description":"Array of string values",` +
		`"items":{"type":"string","pattern":"^[a-z]+$","minLength":2,"minimum":0,"maximum":9}},"type":["array","null"]}`
	if string(encoded) != want {
		t.Fatalf("schema =\n%s\nwant\n%s", encoded, want)
	}
}

// TestFieldArrayOfArraysOfObjects: the innermost items of a T[][] of an
// object type expand to the object's closed schema.
func TestFieldArrayOfArraysOfObjects(t *testing.T) {
	fields := map[string][]apigen.Param{"Point": {{Name: "x", Type: "number", Required: true}}}
	got := FieldToJSONSchemaProperty(
		apigen.Param{Name: "polygons", Type: "Point", Required: true, IsArray: true, IsArrayOfArrays: true},
		map[string]apigen.ScalarJSONSchemaInfo{"number": {Type: "number"}}, fields, nil, nil, nil,
	)
	if got.Type != "array" || got.Nullable || got.Items == nil || got.Items.Type != "array" || got.Items.Nullable {
		t.Fatalf("polygons = %#v, want a required array of arrays", got)
	}
	point := got.Items.Items
	if point == nil || point.Type != "object" || !isClosedObject(*point) || point.Properties["x"].Type != "number" {
		t.Fatalf("polygon item = %#v, want the closed Point object", point)
	}
}

// TestScalarArgumentListShapes: a body argument renders at the list depth
// its ToolScalarArg carries, and a plain one as before, nullable when
// optional.
func TestScalarArgumentListShapes(t *testing.T) {
	scalars := map[string]apigen.ScalarJSONSchemaInfo{"string": {Type: "string"}}
	schema := BuildParametersSchema(nil, nil, false, "", nil, nil, nil, []ToolScalarArg{
		{Name: "label", Type: "string", Required: true},
		{Name: "note", Type: "string"},
		{Name: "tags", Type: "string", Required: true, IsArray: true},
		{Name: "rows", Type: "string", IsArray: true, IsArrayOfArrays: true},
	}, false, scalars, byName, apigen.ToolKeys{})
	encoded, err := json.Marshal(schema.Properties)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"label":{"type":"string"},` +
		`"note":{"type":["string","null"]},` +
		`"rows":{"description":"Array of arrays of string values","items":{"type":"array","description":"Array of string values","items":{"type":"string"}},"type":["array","null"]},` +
		`"tags":{"type":"array","description":"Array of string values","items":{"type":"string"}}}`
	if string(encoded) != want {
		t.Fatalf("properties =\n%s\nwant\n%s", encoded, want)
	}
	if len(schema.Required) != 2 || schema.Required[0] != "label" || schema.Required[1] != "tags" {
		t.Fatalf("required = %v, want [label tags]", schema.Required)
	}
}

// TestScalarArgumentMapsAndEnums: a map body argument is an object whose
// additionalProperties is the value schema, a list included, and an enum
// lists its values as a path parameter, as a body argument and inside a
// map, with null when it is optional.
func TestScalarArgumentMapsAndEnums(t *testing.T) {
	scalars := map[string]apigen.ScalarJSONSchemaInfo{"string": {Type: "string"}}
	enums := map[string]apigen.ToolEnumInfo{"Tone": {Values: []string{"warm", "cool"}}}
	schema := BuildParametersSchema(
		[]ToolPathParam{{Name: "tone", Type: "Tone"}}, nil, false, "", nil, nil, enums,
		[]ToolScalarArg{
			{Name: "labels", Type: "string", Required: true, IsMap: true, IsArray: true},
			{Name: "toneByName", Type: "Tone", Required: true, IsMap: true},
			{Name: "maybeTone", Type: "Tone"},
		}, false, scalars, byName, apigen.ToolKeys{})
	encoded, err := json.Marshal(schema.Properties)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"labels":{"additionalProperties":{"type":"array","description":"Array of string values","items":{"type":"string"}},"type":"object","description":"Map of string values"},` +
		`"maybeTone":{"description":"A Tone value","type":["string","null"],"enum":["warm","cool",null]},` +
		`"tone":{"type":"string","description":"tone parameter","enum":["warm","cool"]},` +
		`"toneByName":{"additionalProperties":{"type":"string","description":"A Tone value","enum":["warm","cool"]},"type":"object","description":"Map of Tone values"}}`
	if string(encoded) != want {
		t.Fatalf("properties =\n%s\nwant\n%s", encoded, want)
	}
}

// TestReturnSchemaDepths: a return schema nests one array level per list
// level, and BuildReturnSchema is depth 0 or 1 of it.
func TestReturnSchemaDepths(t *testing.T) {
	scalars := map[string]apigen.ScalarJSONSchemaInfo{"string": {Type: "string", Description: "A string"}}
	for _, tc := range []struct {
		depth int
		want  string
	}{
		{0, `{"type":"string","description":"A string"}`},
		{1, `{"type":"array","description":"Array of string","items":{"type":"string","description":"A string"}}`},
		{2, `{"type":"array","description":"Array of arrays of string","items":{"type":"array","description":"Array of string","items":{"type":"string","description":"A string"}}}`},
	} {
		encoded, err := json.Marshal(BuildReturnSchemaAtDepth("string", tc.depth, scalars))
		if err != nil {
			t.Fatal(err)
		}
		if string(encoded) != tc.want {
			t.Fatalf("depth %d =\n%s\nwant\n%s", tc.depth, encoded, tc.want)
		}
	}
	for isArray, depth := range map[bool]int{false: 0, true: 1} {
		legacy, _ := json.Marshal(BuildReturnSchema("string", isArray, scalars))
		atDepth, _ := json.Marshal(BuildReturnSchemaAtDepth("string", depth, scalars))
		if string(legacy) != string(atDepth) {
			t.Fatalf("BuildReturnSchema(isArray=%v) = %s, want %s", isArray, legacy, atDepth)
		}
	}
}
