package rustsdkgen

import (
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAReferenceNamesItsOptionsByTheToolsName: the Rust SDK names the
// tool of list_items catalog.listItems, as the TypeScript SDK does
// (apigen.ToolName), and the reference whose options it is names that
// tool.
func TestAReferenceNamesItsOptionsByTheToolsName(t *testing.T) {
	api := ir.NewSchema("shop", ir.SchemaKindAPI)
	api.Types["ItemView"] = &ir.TypeDef{Name: "ItemView", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	api.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "list_items", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "ItemView", IsArray: true}, Required: true},
		{Name: "get_item", HTTPMethod: "GET", RestPath: "items/{itemId}", TypeRef: ir.TypeRef{Name: "ItemView"}, Required: true,
			Arguments: []*ir.ArgumentDef{{Name: "itemId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}}}},
	}}}
	clock := codegen.FixedClock(time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC))
	out, err := apigen.Generate(api, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: "shop", ModulePath: "example.com/a", TypesModule: "example.com/b", Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := Generate(out, "schemas-shop-sdk", "schemas-shop-types", clock)
	if err != nil {
		t.Fatal(err)
	}
	tools, err := GenerateTools(sdk, out, clock)
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
	for _, tool := range tools.Tools {
		for name, property := range tool.Parameters.Properties {
			if property.Reference != nil && (property.Reference.Options != "catalog.listItems" || !names[property.Reference.Options]) {
				t.Errorf("%s(%s) names options %q; want catalog.listItems, a tool", tool.Name, name, property.Reference.Options)
			}
		}
	}
}
