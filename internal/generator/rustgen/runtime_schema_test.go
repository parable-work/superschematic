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
