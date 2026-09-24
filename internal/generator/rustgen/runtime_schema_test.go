package rustgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestRuntimeSchemaIncludesReachableImportedTypesAndRequiredFields: the
// document for a payload holds the payload and what it reaches, including
// an imported type and enum, and nothing it does not reach; requiredness is
// kept; the input schema is not changed; and writing no documents removes
// an earlier one.
func TestRuntimeSchemaIncludesReachableImportedTypesAndRequiredFields(t *testing.T) {
	dependency := ir.NewSchema("shared", ir.SchemaKindGeneral)
	dependency.Enums["Mode"] = &ir.EnumDef{Name: "Mode", Values: []ir.EnumValueDef{{Name: "CURRENT", SerializedAs: "current"}}}
	dependency.Types["Identity"] = &ir.TypeDef{Name: "Identity", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}}}
	dependency.Types["Unrelated"] = &ir.TypeDef{Name: "Unrelated", Role: ir.RoleEmbeddedStruct}
	schema := ir.NewSchema("example", ir.SchemaKindGeneral)
	schema.Imports = []ir.Import{{Package: "@schemas/shared", Types: []string{"Mode", "Identity"}}}
	schema.Types["Context"] = &ir.TypeDef{Name: "Context", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: []*ir.FieldDef{
		{Name: "principal", TypeRef: ir.TypeRef{Name: "Identity"}, Required: true},
		{Name: "initiator", TypeRef: ir.TypeRef{Name: "Identity"}},
		{Name: "mode", TypeRef: ir.TypeRef{Name: "Mode"}, Required: true},
		{Name: "label", TypeRef: ir.TypeRef{Name: "string"}},
	}}
	schema.Types["NotAPayload"] = &ir.TypeDef{Name: "NotAPayload", Role: ir.RoleEmbeddedStruct}

	artifacts, err := runtimeSchemas(schema, map[string]*ir.Schema{"shared": dependency})
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("artifacts = %d, want one per @jsonField type", len(artifacts))
	}
	var decoded ir.Schema
	if err := json.Unmarshal(artifacts["Context"], &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RootType != "Context" || len(decoded.Types) != 2 || decoded.Enums["Mode"] == nil || decoded.Types["Unrelated"] != nil {
		t.Fatalf("reachable definitions drifted: %s", artifacts["Context"])
	}
	if !decoded.Types["Context"].Fields[0].Required || decoded.Types["Context"].Fields[1].Required {
		t.Fatal("required principal and optional initiator drifted")
	}
	if schema.RootType != "" || len(schema.Types) != 2 {
		t.Fatal("artifact generation changed the generation input")
	}

	output := t.TempDir()
	if err := writeRuntimeSchemas(artifacts, output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "schemas", "Context.json")); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeSchemas(nil, output); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(output, "schemas")); !os.IsNotExist(err) {
		t.Fatal("a removed payload left a stale generated schema behind")
	}
}

func TestRuntimeSchemaRejectsUnknownReferencedTypes(t *testing.T) {
	schema := ir.NewSchema("example", ir.SchemaKindGeneral)
	schema.Types["Context"] = &ir.TypeDef{Name: "Context", JsonField: true, Fields: []*ir.FieldDef{{Name: "value", TypeRef: ir.TypeRef{Name: "Missing"}}}}
	_, err := runtimeSchemas(schema, nil)
	if err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Fatalf("a missing type must refuse generation, got %v", err)
	}
}

// TestRuntimeSchemaKeepsListsOfLists: a payload field that is a list of
// lists keeps isArrayOfArrays in its document, and the element type it
// names is reached like the element of a single list.
func TestRuntimeSchemaKeepsListsOfLists(t *testing.T) {
	schema := ir.NewSchema("example", ir.SchemaKindGeneral)
	schema.Enums["Shade"] = &ir.EnumDef{Name: "Shade", Values: []ir.EnumValueDef{{Name: "DARK", SerializedAs: "dark"}}}
	schema.Types["Point"] = &ir.TypeDef{Name: "Point", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "x", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}}
	schema.Types["Sketch"] = &ir.TypeDef{Name: "Sketch", Role: ir.RoleEmbeddedStruct, JsonField: true, Fields: []*ir.FieldDef{
		{Name: "polygons", TypeRef: ir.TypeRef{Name: "Point", IsArray: true, IsArrayOfArrays: true}, Required: true},
		{Name: "shades", TypeRef: ir.TypeRef{Name: "Shade", IsArray: true, IsArrayOfArrays: true}},
	}}

	artifacts, err := runtimeSchemas(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded ir.Schema
	if err := json.Unmarshal(artifacts["Sketch"], &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Types["Point"] == nil || decoded.Enums["Shade"] == nil {
		t.Fatalf("list-of-lists element types were not reached: %s", artifacts["Sketch"])
	}
	for _, field := range decoded.Types["Sketch"].Fields {
		if field.TypeRef.ArrayDepth() != 2 {
			t.Fatalf("%s depth = %d, want 2: %s", field.Name, field.TypeRef.ArrayDepth(), artifacts["Sketch"])
		}
	}
	if !strings.Contains(string(artifacts["Sketch"]), `"isArrayOfArrays": true`) {
		t.Fatalf("document does not carry isArrayOfArrays: %s", artifacts["Sketch"])
	}
}
