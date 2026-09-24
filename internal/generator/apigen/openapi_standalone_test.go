package apigen

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// TestTypeOpenAPISchemaRewritesRecursiveRootReferences: a reference to the
// root type becomes "#", other components move under definitions, and the
// result compiles and validates, rejecting an unknown field of a strict
// type reached through the recursive reference.
func TestTypeOpenAPISchemaRewritesRecursiveRootReferences(t *testing.T) {
	schema := ir.NewSchema("recursive-contracts", ir.SchemaKindGeneral)
	schema.Types["Node"] = &ir.TypeDef{
		Name:       "Node",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "children", TypeRef: ir.TypeRef{Name: "Node", IsArray: true}},
			{Name: "detail", TypeRef: ir.TypeRef{Name: "Detail"}, Required: true},
		},
	}
	schema.Types["Detail"] = &ir.TypeDef{
		Name:       "Detail",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "ancestors", TypeRef: ir.TypeRef{Name: "Node", IsArray: true}},
			{Name: "subdetails", TypeRef: ir.TypeRef{Name: "Detail", IsArray: true}},
		},
	}

	standalone, err := TypeOpenAPISchema("Node", schema, nil)
	if err != nil {
		t.Fatal(err)
	}

	properties := standalone["properties"].(map[string]interface{})
	children := properties["children"].(map[string]interface{})
	if ref := children["items"].(map[string]interface{})["$ref"]; ref != "#" {
		t.Fatalf("recursive root ref = %q, want #", ref)
	}
	if ref := properties["detail"].(map[string]interface{})["$ref"]; ref != "#/definitions/Detail" {
		t.Fatalf("retained definition ref = %q, want #/definitions/Detail", ref)
	}

	definitions := standalone["definitions"].(map[string]interface{})
	if _, exists := definitions["Node"]; exists {
		t.Fatal("promoted root must not remain under definitions")
	}
	detailProperties := definitions["Detail"].(map[string]interface{})["properties"].(map[string]interface{})
	ancestors := detailProperties["ancestors"].(map[string]interface{})
	if ref := ancestors["items"].(map[string]interface{})["$ref"]; ref != "#" {
		t.Fatalf("definition-to-root ref = %q, want #", ref)
	}
	subdetails := detailProperties["subdetails"].(map[string]interface{})
	if ref := subdetails["items"].(map[string]interface{})["$ref"]; ref != "#/definitions/Detail" {
		t.Fatalf("definition self-ref = %q, want #/definitions/Detail", ref)
	}

	encoded, err := json.Marshal(standalone)
	if err != nil {
		t.Fatal(err)
	}
	var document interface{}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("superschematic://node.json", document); err != nil {
		t.Fatal(err)
	}
	compiled, err := compiler.Compile("superschematic://node.json")
	if err != nil {
		t.Fatalf("compile standalone recursive schema: %v", err)
	}
	valid := map[string]interface{}{
		"children": []interface{}{
			map[string]interface{}{"detail": map[string]interface{}{"label": "child"}},
		},
		"detail": map[string]interface{}{
			"label":      "root",
			"ancestors":  []interface{}{},
			"subdetails": []interface{}{map[string]interface{}{"label": "nested"}},
		},
	}
	if err := compiled.Validate(valid); err != nil {
		t.Fatalf("validate recursive payload: %v", err)
	}
	invalid := map[string]interface{}{
		"children": []interface{}{
			map[string]interface{}{
				"detail":     map[string]interface{}{"label": "child"},
				"unexpected": true,
			},
		},
		"detail": map[string]interface{}{"label": "root"},
	}
	if err := compiled.Validate(invalid); err == nil {
		t.Fatal("recursive root ref accepted an unknown child field")
	}
}

// TestTypeOpenAPISchemaAppliesScalarConstraintsToMapValues: scalar
// constraints land on map values, and a string-map scalar's values are
// typed through its type mappings.
func TestTypeOpenAPISchemaAppliesScalarConstraintsToMapValues(t *testing.T) {
	schema := ir.NewSchema("map-contracts", ir.SchemaKindGeneral)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{
		Name:              "Identity.UUID",
		LanguagePrimitive: ir.LanguageString,
		Pattern:           "^[a-z0-9-]+$",
		Format:            "uuid",
		TypeMappings:      map[string]string{"json_schema": "string", "go": "UUID"},
	}
	schema.Scalars["Generic.StringMap"] = &ir.ScalarDef{
		Name:              "Generic.StringMap",
		LanguagePrimitive: ir.LanguageString,
		TypeMappings: map[string]string{
			"json_schema": "object",
			"go":          "GenericStringMap",
			"python":      "Dict[str, str]",
		},
	}
	schema.Types["Envelope"] = &ir.TypeDef{
		Name:       "Envelope",
		Role:       ir.RoleEmbeddedStruct,
		StrictJSON: true,
		Fields: []*ir.FieldDef{
			{Name: "locales", TypeRef: ir.TypeRef{Name: "Generic.StringMap", IsMap: true}, Required: true},
			{Name: "ids", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsMap: true}, Required: true},
			{Name: "batches", TypeRef: ir.TypeRef{Name: "Generic.StringMap", IsMap: true, IsArray: true}, Required: true},
		},
	}

	standalone, err := TypeOpenAPISchema("Envelope", schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	properties := standalone["properties"].(map[string]interface{})

	locales := properties["locales"].(map[string]interface{})
	localeValues, ok := locales["additionalProperties"].(map[string]interface{})
	if !ok || localeValues["type"] != "object" {
		t.Fatalf("map[string]Generic.StringMap collapsed to %#v", locales)
	}
	localeValueItems, ok := localeValues["additionalProperties"].(map[string]interface{})
	if !ok || localeValueItems["type"] != "string" {
		t.Fatalf("Generic.StringMap values = %#v", localeValues)
	}

	ids := properties["ids"].(map[string]interface{})
	idValues, ok := ids["additionalProperties"].(map[string]interface{})
	if !ok || idValues["format"] != "uuid" || idValues["pattern"] != "^[a-z0-9-]+$" {
		t.Fatalf("map[string]Identity.UUID constraints = %#v", ids)
	}

	batches := properties["batches"].(map[string]interface{})
	batchArray, ok := batches["additionalProperties"].(map[string]interface{})
	if !ok || batchArray["type"] != "array" {
		t.Fatalf("map[string][]Generic.StringMap = %#v", batches)
	}
	batchItems, ok := batchArray["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("map[string][]Generic.StringMap items = %#v", batchArray)
	}
	batchValueItems, ok := batchItems["additionalProperties"].(map[string]interface{})
	if !ok || batchValueItems["type"] != "string" {
		t.Fatalf("map[string][]Generic.StringMap values = %#v", batchItems)
	}
}

func TestTypeOpenAPISchemaRejectsUnknownType(t *testing.T) {
	if _, err := TypeOpenAPISchema("Missing", ir.NewSchema("contracts", ir.SchemaKindGeneral), nil); err == nil {
		t.Fatal("an unknown type must be an error")
	}
}
