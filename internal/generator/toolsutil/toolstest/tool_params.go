package toolstest

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ToneValues are the values of the Tone enum of fixture-tool-params-api.
var ToneValues = []any{"warm", "cool"}

// CheckToolParamsSchema checks the tool arguments of fixture-tool-params-api
// in a tools/schema.json: an enum lists its values wherever it appears (a
// path, query or body argument, an input field, an item of a list or of a
// list of lists, a map value, a field of a nested object or of a union
// member), a nullable enum also lists null, and a map body argument is an
// object whose additionalProperties is the value schema.
func CheckToolParamsSchema(t *testing.T, document []byte) {
	t.Helper()
	var manifest struct {
		Tools []struct {
			Name       string         `json:"name"`
			Parameters map[string]any `json:"parameters"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(document, &manifest); err != nil {
		t.Fatalf("decode tools/schema.json: %v", err)
	}
	tools := make(map[string]map[string]any, len(manifest.Tools))
	for _, tool := range manifest.Tools {
		tools[tool.Name] = tool.Parameters
	}
	at := func(path string) map[string]any {
		t.Helper()
		segments := strings.Split(path, "/")
		parameters, ok := tools[segments[0]]
		if !ok {
			t.Fatalf("no tool %s", segments[0])
		}
		current := child(t, path, parameters, "properties")
		for _, segment := range segments[1:] {
			current = child(t, path, current, segment)
		}
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s is not an object: %v", path, current)
		}
		return object
	}

	for _, path := range []string{
		"paint.paint/tone",
		"paint.paint/tones/items",
		"paint.paint/toneRows/items/items",
		"paint.paint/maybeTones/items",
		"paint.paint/toneByName/additionalProperties",
		"paint.paint/tonesByName/additionalProperties/items",
		"paint.paint/swatch/properties/tone",
		"paint.paint/swatch/properties/alternates/items",
		"paint.paint/maybeSwatch/properties/tone",
		"paint.paint/swatches/items/properties/tone",
		"paint.paint/swatchRows/items/items/properties/tone",
		"paint.paint/swatchByName/additionalProperties/properties/tone",
		"paint.paint/fill/oneOf/0/properties/tone",
		"paint.setTone/tone",
		"paint.nameTones/toneByName/additionalProperties",
		"paint.listPaint/tone",
		"paint.listPaint/tones/items",
		"paint.paintByTone/tone",
	} {
		property := at(path)
		if got := property["enum"]; !reflect.DeepEqual(got, ToneValues) {
			t.Errorf("%s: enum = %v, want %v", path, got, ToneValues)
		}
		// The description stays what it was; a path parameter's names it.
		description := "A Tone value"
		if path == "paint.paintByTone/tone" {
			description = "tone parameter"
		}
		if property["type"] != "string" || property["description"] != description {
			t.Errorf("%s: type %v, description %v; want string, %s", path, property["type"], property["description"], description)
		}
	}
	// Not required, so nullable: null is one of the values.
	if got, want := at("paint.paint/maybeTone")["enum"], append(append([]any(nil), ToneValues...), nil); !reflect.DeepEqual(got, want) {
		t.Errorf("paint.paint/maybeTone: enum = %v, want %v", got, want)
	}

	for path, value := range map[string]map[string]any{
		"paint.nameTones/toneByName": {"type": "string", "description": "A Tone value", "enum": ToneValues},
		"paint.setLabels/labelsByLocale": {
			"type": "array", "description": "Array of string values",
			"items": map[string]any{"type": "string", "description": "A string value"},
		},
	} {
		property := at(path)
		if property["type"] != "object" {
			t.Errorf("%s: type = %v, want object", path, property["type"])
		}
		if got := property["additionalProperties"]; !reflect.DeepEqual(got, value) {
			t.Errorf("%s: additionalProperties = %v, want %v", path, got, value)
		}
	}
}

// child is parent[segment] of an object, or the element at index segment
// of a list.
func child(t *testing.T, path string, parent any, segment string) any {
	t.Helper()
	var next any
	switch typed := parent.(type) {
	case map[string]any:
		next = typed[segment]
	case []any:
		if index, err := strconv.Atoi(segment); err == nil && index >= 0 && index < len(typed) {
			next = typed[index]
		}
	}
	if next == nil {
		t.Fatalf("%s: nothing at %q in %v", path, segment, parent)
	}
	return next
}
