package loader

import (
	"strings"
	"testing"
)

// assertLoad loads dir and checks the outcome: a clean load when wantError
// is empty, otherwise a failure whose message contains wantError.
func assertLoad(t *testing.T, dir, wantError string) {
	t.Helper()
	_, err := LoadService(dir)
	if wantError == "" {
		if err != nil {
			t.Fatalf("LoadService: %v", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("LoadService accepted the schema, want an error containing %q", wantError)
	}
	if !strings.Contains(err.Error(), wantError) {
		t.Fatalf("error %q does not contain %q", err, wantError)
	}
}

// TestLoadServiceCompositeDefaultChecksListElements: a field's own length,
// pattern and range rules apply to every element of a T[] value and every
// innermost element of a T[][] value in a composite default, as they do in
// every validator (D12, amended). listMin and listMax bound the outer list.
func TestLoadServiceCompositeDefaultChecksListElements(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError string
	}{
		{
			name:  "valid elements",
			value: `{"labels": ["abc", "caf\u00e9"], "scores": [0, 10], "grid": [["ab"], ["cd", "ef"]]}`,
		},
		{
			name:      "string element over the field maxLength",
			value:     `{"labels": ["abc", "caf\u00e9s"], "scores": [0], "grid": [["ab"]]}`,
			wantError: "ListDefaults.labels[1] must contain at most 4 characters",
		},
		{
			name:      "string element under the field minLength",
			value:     `{"labels": ["ab"], "scores": [0], "grid": [["ab"]]}`,
			wantError: "ListDefaults.labels[0] must contain at least 3 characters",
		},
		{
			name:      "number element over the field max",
			value:     `{"labels": ["abc"], "scores": [1, 11], "grid": [["ab"]]}`,
			wantError: "ListDefaults.scores[1] must be at most 10",
		},
		{
			name:      "innermost element that breaks the field pattern",
			value:     `{"labels": ["abc"], "scores": [0], "grid": [["ab"], ["cd", "E1"]]}`,
			wantError: "ListDefaults.grid[1][1] does not match its validation pattern",
		},
		{
			name:      "listMax bounds the outer list of a list of lists",
			value:     `{"labels": ["abc"], "scores": [0], "grid": [["ab"], ["cd"], ["ef"]]}`,
			wantError: "ListDefaults.grid must contain at most 2 items",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json": minimalConfig,
				"src/lists.fixture.schema.json": `{
					"types": {
						"ListDefaults": {
							"name": "ListDefaults",
							"role": "EmbeddedStruct",
							"fields": [
								{"name": "labels", "typeRef": {"name": "string", "isArray": true}, "required": true, "validateMinLength": 3, "validateMaxLength": 4},
								{"name": "scores", "typeRef": {"name": "number", "isArray": true}, "required": true, "validateMin": 0, "validateMax": 10},
								{"name": "grid", "typeRef": {"name": "string", "isArray": true, "isArrayOfArrays": true}, "required": true, "validatePattern": "^[a-z]+$", "validateListMax": 2}
							]
						}
					}
				}`,
				"src/list-defaults.platform-default.json": `{"type": "ListDefaults", "value": ` + tt.value + `}`,
			})
			assertLoad(t, dir, tt.wantError)
		})
	}
}

// TestLoadServiceChecksFieldDefaults: a field's or an argument's default
// meets the scalar's rules and the field's own rules, as the value would in
// every validator; lengths count code points (D14, amended). A list default
// is a JSON array, bounded by listMin and listMax, with each element checked.
func TestLoadServiceChecksFieldDefaults(t *testing.T) {
	tests := []struct {
		name      string
		field     string
		wantError string
	}{
		{
			name:  "scalar default within its lengths",
			field: `{"name": "code", "typeRef": {"name": "Acme.Code"}, "default": "caf\u00e9"}`,
		},
		{
			name:      "scalar default over its maxLength",
			field:     `{"name": "code", "typeRef": {"name": "Acme.Code"}, "default": "caf\u00e9s"}`,
			wantError: "Widget.code: default must contain at most 4 characters",
		},
		{
			name:      "scalar default that breaks its pattern",
			field:     `{"name": "code", "typeRef": {"name": "Acme.Code"}, "default": "AB-1"}`,
			wantError: `Widget.code: default does not match scalar "Acme.Code"`,
		},
		{
			name:  "integer scalar default within its range",
			field: `{"name": "rank", "typeRef": {"name": "Acme.Rank"}, "default": "5"}`,
		},
		{
			name:      "integer scalar default over its maximum",
			field:     `{"name": "rank", "typeRef": {"name": "Acme.Rank"}, "default": "6"}`,
			wantError: "Widget.rank: default must be at most 5",
		},
		{
			name:      "integer scalar default that is not an integer",
			field:     `{"name": "rank", "typeRef": {"name": "Acme.Rank"}, "default": "2.5"}`,
			wantError: "Widget.rank: default must be an integer",
		},
		{
			name:  "string default within the field maxLength",
			field: `{"name": "label", "typeRef": {"name": "string"}, "validateMaxLength": 4, "default": "caf\u00e9"}`,
		},
		{
			name:      "string default over the field maxLength",
			field:     `{"name": "label", "typeRef": {"name": "string"}, "validateMaxLength": 4, "default": "caf\u00e9s"}`,
			wantError: "Widget.label: default must contain at most 4 characters",
		},
		{
			name:      "string default that breaks the field pattern",
			field:     `{"name": "label", "typeRef": {"name": "string"}, "validatePattern": "^[a-z]+$", "default": "Label"}`,
			wantError: "Widget.label: default does not match its validation pattern",
		},
		{
			name:  "number default within the field range",
			field: `{"name": "ratio", "typeRef": {"name": "number"}, "validateMin": 0, "validateMax": 1, "default": "0.5"}`,
		},
		{
			name:      "number default under the field min",
			field:     `{"name": "ratio", "typeRef": {"name": "number"}, "validateMin": 0, "validateMax": 1, "default": "-0.5"}`,
			wantError: "Widget.ratio: default must be at least 0",
		},
		{
			name:      "number default that is not a number",
			field:     `{"name": "ratio", "typeRef": {"name": "number"}, "default": "half"}`,
			wantError: "Widget.ratio: default must be a number",
		},
		{
			name:      "boolean default that is not a boolean",
			field:     `{"name": "enabled", "typeRef": {"name": "boolean"}, "default": "yes"}`,
			wantError: "Widget.enabled: default must be a boolean",
		},
		{
			name:  "enum default that is a member",
			field: `{"name": "stage", "typeRef": {"name": "Stage"}, "default": "draft"}`,
		},
		{
			name:      "enum default that is not a member",
			field:     `{"name": "stage", "typeRef": {"name": "Stage"}, "default": "archived"}`,
			wantError: `Widget.stage: default has invalid Stage value "archived"`,
		},
		{
			name:  "empty list default",
			field: `{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "validateMaxLength": 4, "default": "[]"}`,
		},
		{
			name:      "empty list default under listMin",
			field:     `{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "validateListMin": 1, "default": "[]"}`,
			wantError: "Widget.tags: default must contain at least 1 items",
		},
		{
			name:      "list default element over the field maxLength",
			field:     `{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "validateMaxLength": 4, "default": "[\"abc\", \"caf\u00e9s\"]"}`,
			wantError: "Widget.tags: default[1] must contain at most 4 characters",
		},
		{
			name:      "list default that is not a JSON array",
			field:     `{"name": "tags", "typeRef": {"name": "string", "isArray": true}, "default": "abc"}`,
			wantError: "Widget.tags: default must be a JSON array",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json": minimalConfig,
				"src/widget.fixture.schema.json": `{
					"scalars": {
						"Acme.Code": {"name": "Acme.Code", "languagePrimitive": "string", "minLength": 3, "maxLength": 4, "pattern": "^[a-z\u00e9]+$"},
						"Acme.Rank": {"name": "Acme.Rank", "languagePrimitive": "number", "primitive": "Int", "minimum": 1, "maximum": 5}
					},
					"enums": {
						"Stage": {"name": "Stage", "values": [{"name": "Draft", "serializedAs": "draft"}, {"name": "Live", "serializedAs": "live"}]}
					},
					"types": {
						"Widget": {
							"name": "Widget",
							"role": "EmbeddedStruct",
							"fields": [` + tt.field + `]
						}
					}
				}`,
			})
			assertLoad(t, dir, tt.wantError)
		})
	}
}

// TestLoadServiceChecksArgumentDefaults: an operation argument's default is
// checked as a field's is.
func TestLoadServiceChecksArgumentDefaults(t *testing.T) {
	tests := []struct {
		name      string
		value     string
		wantError string
	}{
		{name: "default within the argument max", value: "50"},
		{name: "default over the argument max", value: "500", wantError: "WidgetQueries.listWidgets(limit): default must be at most 100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json": `{"name": "temp-service", "kind": "API", "outputs": {}}`,
				"src/widget.fixture.schema.json": `{
					"types": {
						"Widget": {
							"name": "Widget",
							"role": "EmbeddedStruct",
							"fields": [{"name": "label", "typeRef": {"name": "string"}, "required": true}]
						}
					},
					"operationSets": [{
						"name": "WidgetQueries",
						"operations": [{
							"name": "listWidgets",
							"typeRef": {"name": "Widget", "isArray": true},
							"required": true,
							"arguments": [{"name": "limit", "typeRef": {"name": "number"}, "isQuery": true, "validateMin": 1, "validateMax": 100, "default": "` + tt.value + `"}],
							"httpMethod": "GET",
							"restPath": "widgets"
						}]
					}]
				}`,
			})
			assertLoad(t, dir, tt.wantError)
		})
	}
}

// TestLoadServiceChecksScalarExamples: a scalar's example meets the
// scalar's own lengths, pattern and range. An example that breaks them
// fails the load (D14, amended).
func TestLoadServiceChecksScalarExamples(t *testing.T) {
	tests := []struct {
		name      string
		scalar    string
		wantError string
	}{
		{
			name:   "string example within its lengths",
			scalar: `{"name": "Acme.Code", "languagePrimitive": "string", "minLength": 3, "maxLength": 4, "example": "caf\u00e9"}`,
		},
		{
			name:      "string example over its maxLength",
			scalar:    `{"name": "Acme.Code", "languagePrimitive": "string", "minLength": 3, "maxLength": 4, "example": "caf\u00e9s"}`,
			wantError: "scalar Acme.Code: example must contain at most 4 characters",
		},
		{
			name:      "string example that breaks its pattern",
			scalar:    `{"name": "Acme.Code", "languagePrimitive": "string", "pattern": "^[a-z]+$", "example": "ABC"}`,
			wantError: `scalar Acme.Code: example does not match scalar "Acme.Code"`,
		},
		{
			name:   "integer example within its range",
			scalar: `{"name": "Acme.Code", "languagePrimitive": "number", "primitive": "Int", "minimum": 1, "maximum": 5, "example": "3"}`,
		},
		{
			name:      "integer example under its minimum",
			scalar:    `{"name": "Acme.Code", "languagePrimitive": "number", "primitive": "Int", "minimum": 1, "maximum": 5, "example": "0"}`,
			wantError: "scalar Acme.Code: example must be at least 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json": minimalConfig,
				"src/example.fixture.schema.json": `{
					"scalars": {"Acme.Code": ` + tt.scalar + `},
					"types": {
						"Widget": {
							"name": "Widget",
							"role": "EmbeddedStruct",
							"fields": [{"name": "code", "typeRef": {"name": "Acme.Code"}, "required": true}]
						}
					}
				}`,
			})
			assertLoad(t, dir, tt.wantError)
		})
	}
}
