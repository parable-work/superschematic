package apigen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestOpenAPITypeSchemaPreservesFieldValidationConstraints: a component
// schema carries the field's Validate<> bounds and the scalar's own pattern
// and lengths. On an array the value constraints go on items and only the
// list bounds stay on the array; on a map they go on the map values.
func TestOpenAPITypeSchemaPreservesFieldValidationConstraints(t *testing.T) {
	listMin, listMax := 5, 5
	minLength, maxLength := 1, 80
	schema := ir.NewSchema("fixture-api", ir.SchemaKindAPI)
	schema.Scalars["Example.Handle"] = &ir.ScalarDef{
		Name: "Example.Handle", LanguagePrimitive: ir.LanguageString,
		Pattern: "^[a-z][A-Za-z0-9]*$", MinLength: 1, MaxLength: 200,
	}
	schema.Types["ProfileInput"] = &ir.TypeDef{
		Name: "ProfileInput",
		Fields: []*ir.FieldDef{
			{
				Name: "profiles", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true,
				ValidateListMin: &listMin, ValidateListMax: &listMax,
			},
			{
				Name: "active", TypeRef: ir.TypeRef{Name: "string"}, Required: true,
				ValidateMinLength: &minLength, ValidateMaxLength: &maxLength,
			},
			{Name: "handle", TypeRef: ir.TypeRef{Name: "Example.Handle"}, Required: true},
			{Name: "handles", TypeRef: ir.TypeRef{Name: "Example.Handle", IsArray: true}, Required: true},
			{Name: "handleByName", TypeRef: ir.TypeRef{Name: "Example.Handle", IsMap: true}, Required: true},
		},
	}
	schemas := buildOpenAPISchemas(
		[]EndpointInfo{{InputType: "ProfileInput"}},
		map[string]string{}, map[string]string{}, buildOpenAPIScalarMap(schema, nil),
		schema, nil,
	)
	component := schemas["ProfileInput"].(map[string]interface{})
	properties := component["properties"].(map[string]interface{})

	profiles := properties["profiles"].(map[string]interface{})
	if profiles["minItems"] != 5 || profiles["maxItems"] != 5 {
		t.Fatalf("profile bounds = %#v, want minItems/maxItems 5", profiles)
	}
	active := properties["active"].(map[string]interface{})
	if active["minLength"] != 1 || active["maxLength"] != 80 {
		t.Fatalf("active bounds = %#v, want minLength 1 maxLength 80", active)
	}
	handle := properties["handle"].(map[string]interface{})
	if handle["pattern"] != "^[a-z][A-Za-z0-9]*$" || handle["minLength"] != 1 || handle["maxLength"] != 200 {
		t.Fatalf("handle constraints = %#v", handle)
	}
	handles := properties["handles"].(map[string]interface{})
	if _, misplaced := handles["pattern"]; misplaced {
		t.Fatalf("array wrapper has the scalar pattern: %#v", handles)
	}
	handleItems := handles["items"].(map[string]interface{})
	if handleItems["pattern"] != "^[a-z][A-Za-z0-9]*$" || handleItems["maxLength"] != 200 {
		t.Fatalf("array item constraints = %#v", handleItems)
	}
	byName := properties["handleByName"].(map[string]interface{})
	if _, misplaced := byName["pattern"]; misplaced {
		t.Fatalf("map wrapper has the scalar pattern: %#v", byName)
	}
	byNameValues := byName["additionalProperties"].(map[string]interface{})
	if byNameValues["pattern"] != "^[a-z][A-Za-z0-9]*$" {
		t.Fatalf("map value constraints = %#v", byNameValues)
	}
}

// TestAnyJSONScalarOpenAPISchemaAcceptsAnyRoot: a scalar whose json_schema
// type mapping is "any" renders as an empty Schema Object (every JSON value,
// null included) with its example parsed as JSON.
func TestAnyJSONScalarOpenAPISchemaAcceptsAnyRoot(t *testing.T) {
	irSchema := ir.NewSchema("json-contract", ir.SchemaKindGeneral)
	irSchema.Scalars["Generic.JSON"] = &ir.ScalarDef{
		Name:              "Generic.JSON",
		LanguagePrimitive: ir.LanguageString,
		TypeMappings:      map[string]string{"json_schema": "any"},
	}
	scalarMap := buildOpenAPIScalarMap(irSchema, nil)
	if scalarMap["Generic.JSON"] != "any" {
		t.Fatalf("scalar map = %q, want the any sentinel", scalarMap["Generic.JSON"])
	}
	schema := typeToOpenAPISchema(
		"Generic.JSON",
		map[string]string{"Generic.JSON": `{"k":1}`},
		map[string]string{"Generic.JSON": "Any JSON value"},
		scalarMap,
	)
	if _, exists := schema["type"]; exists {
		t.Fatalf("an any-JSON scalar must not constrain type: %#v", schema)
	}
	if got, ok := schema["example"].(map[string]interface{}); !ok || got["k"] != float64(1) {
		t.Fatalf("example = %#v, want the parsed object", schema["example"])
	}
}
