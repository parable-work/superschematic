package apigen

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// TestOpenAPIRequiredNullAdmitsOnlyNull validates payloads against the
// OpenAPI component of a type with a RequiredNull field under the OpenAPI
// 3.0.3 reading of nullable (D130): the field stays required, null is its
// only valid value, and the field adds no component of its own.
func TestOpenAPIRequiredNullAdmitsOnlyNull(t *testing.T) {
	schema := ir.NewSchema("shipments", ir.SchemaKindAPI)
	schema.Types["VoidShipmentInput"] = &ir.TypeDef{
		Name:       "VoidShipmentInput",
		Role:       ir.RoleAPIInput,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "voidedAt", Required: true, RequiredNull: true, Comment: "The time is not known."},
			{Name: "reason", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	schemas, _ := buildOpenAPISchemas(
		[]EndpointInfo{{OutputType: "VoidShipmentInput"}},
		nil,
		nil,
		buildOpenAPIScalarMap(schema, nil),
		schema,
		nil,
	)
	for name := range schemas {
		if name != "VoidShipmentInput" && name != problemDetailsSchemaName && name != "ResponseMeta" {
			t.Fatalf("component %s, want VoidShipmentInput and the standard ones only", name)
		}
	}
	input := schemas["VoidShipmentInput"].(map[string]interface{})
	if required := input["required"].([]string); len(required) != 2 || required[0] != "voidedAt" {
		t.Fatalf("required = %v, want voidedAt and reason", required)
	}
	voidedAt := input["properties"].(map[string]interface{})["voidedAt"].(map[string]interface{})
	if voidedAt["description"] != "The time is not known." {
		t.Fatalf("voidedAt = %v, want its description", voidedAt)
	}
	raw, err := json.Marshal(map[string]interface{}{"components": map[string]interface{}{"schemas": schemas}})
	if err != nil {
		t.Fatal(err)
	}
	var doc interface{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("superschematic://openapi.json", openAPI30ToJSONSchema(doc)); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("superschematic://openapi.json#/components/schemas/VoidShipmentInput")
	if err != nil {
		t.Fatal(err)
	}
	for payload, valid := range map[string]bool{
		`{"voidedAt":null,"reason":"lost"}`:         true,
		`{"reason":"lost"}`:                         false,
		`{"voidedAt":"2026-01-01","reason":"lost"}`: false,
		`{"voidedAt":"","reason":"lost"}`:           false,
		`{"voidedAt":0,"reason":"lost"}`:            false,
		`{"voidedAt":false,"reason":"lost"}`:        false,
		`{"voidedAt":{},"reason":"lost"}`:           false,
		`{"voidedAt":[],"reason":"lost"}`:           false,
	} {
		var value interface{}
		if err := json.Unmarshal([]byte(payload), &value); err != nil {
			t.Fatal(err)
		}
		if err := compiled.Validate(value); (err == nil) != valid {
			t.Errorf("payload %s valid = %v, want %v (%v)", payload, err == nil, valid, err)
		}
	}
}
