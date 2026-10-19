package sdkgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	ir "github.com/parable-work/superschematic/ir"
)

// snakeCaseCatalog is an API whose operations are named in snake_case:
// list_items lists ItemView, and get_item's itemId refers to it.
func snakeCaseCatalog() *ir.Schema {
	api := ir.NewSchema("shop", ir.SchemaKindAPI)
	api.Types["ItemView"] = &ir.TypeDef{Name: "ItemView", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	api.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "list_items", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "ItemView", IsArray: true}, Required: true},
		{Name: "get_item", HTTPMethod: "GET", RestPath: "items/{itemId}", TypeRef: ir.TypeRef{Name: "ItemView"}, Required: true,
			Arguments: []*ir.ArgumentDef{{Name: "itemId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}}}},
	}}}
	return api
}

// TestAReferenceNamesItsOptionsByTheToolsName: the tool of list_items is
// catalog.listItems, the name of the SDK's method, and the reference whose
// options it is names that tool (apigen.ToolName), not catalog.list_items,
// which no tool is.
func TestAReferenceNamesItsOptionsByTheToolsName(t *testing.T) {
	out, err := apigen.Generate(snakeCaseCatalog(), apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "shop", ModulePath: "example.com/a", TypesModule: "example.com/b", Clock: mcpClock})
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := Generate(out, nil, mcpClock)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := GenerateTools(sdk, out, mcpClock)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range tools.Tools {
		names[tool.Name] = true
	}
	if !names["catalog.listItems"] || !names["catalog.getItem"] {
		t.Fatalf("tools = %v, want catalog.listItems and catalog.getItem", names)
	}
	var options []string
	for _, tool := range tools.Tools {
		for name, property := range tool.Parameters.Properties {
			if property.Reference == nil {
				continue
			}
			options = append(options, property.Reference.Options)
			if !names[property.Reference.Options] {
				t.Errorf("%s(%s) names options %q, which is no tool of %v", tool.Name, name, property.Reference.Options, names)
			}
		}
	}
	if len(options) != 1 || options[0] != "catalog.listItems" {
		t.Errorf("options = %v, want [catalog.listItems]", options)
	}
}
