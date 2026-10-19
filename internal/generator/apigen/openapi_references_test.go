package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// generateRefsAPI is the API output of fixture-refs-api, whose fields and
// arguments refer to the catalog's items and whose listing has a search
// (D61, D62).
func generateRefsAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-refs-api"))
	if err != nil {
		t.Fatalf("load fixture-refs-api: %v", err)
	}
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-refs-api",
		ModulePath:  "example.com/schemas/api/fixture-refs-api",
		TypesModule: "example.com/schemas/types/go/fixture-refs-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

// TestRefsOpenAPIGolden pins the OpenAPI document of fixture-refs-api: each
// value that refers to an item gives its resolved reference under
// x-superschematic-reference, on a field's property (a basket line's
// itemId, inside a list of lines; the gift's sku, every member said), on
// a query parameter (ids, the lookup, on the list) and on a path
// parameter (getItem's itemId); the search argument's parameter says
// x-superschematic-search. Regenerate with:
// go test ./internal/generator/apigen -run TestRefsOpenAPIGolden -update
func TestRefsOpenAPIGolden(t *testing.T) {
	got := []byte(generateRefsAPI(t).OpenAPISpecRaw)
	goldenPath := filepath.Join("testdata", "golden", "fixture-refs-api", "openapi.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("openapi.json differs from %s; run with -update and review the diff\ngot:\n%s", goldenPath, got)
	}
}

// TestRefsOpenAPIPlacesEachKey reads the placements the golden pins, so a
// regenerated golden that drops one fails here.
func TestRefsOpenAPIPlacesEachKey(t *testing.T) {
	doc := decodeOpenAPI(t, generateRefsAPI(t).OpenAPISpecRaw)
	byID := map[string]any{
		"target": "#/components/schemas/ItemView", "options": "CatalogListItemsHandler",
		"value": "id", "label": "name", "lookup": "ids",
	}
	bySku := map[string]any{
		"target": "#/components/schemas/ItemView", "options": "CatalogListItemsHandler",
		"value": "sku", "label": "name",
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	property := func(component, name string) map[string]any {
		t.Helper()
		return schemas[component].(map[string]any)["properties"].(map[string]any)[name].(map[string]any)
	}
	parameter := func(path, method, name string) map[string]any {
		t.Helper()
		operation := doc["paths"].(map[string]any)[path].(map[string]any)[method].(map[string]any)
		for _, p := range operation["parameters"].([]any) {
			if p := p.(map[string]any); p["name"] == name {
				return p
			}
		}
		t.Fatalf("%s %s has no parameter %s", method, path, name)
		return nil
	}
	for _, tc := range []struct {
		where string
		got   map[string]any
		want  any
	}{
		{"BasketLine.itemId", property("BasketLine", "itemId"), byID},
		{"BasketInput.giftSku", property("BasketInput", "giftSku"), bySku},
		{"listItems(ids)", parameter("/api/items", "get", "ids"), byID},
		{"getItem(itemId)", parameter("/api/items/{itemId}", "get", "itemId"), byID},
	} {
		if !reflect.DeepEqual(tc.got[apigen.OpenAPIReferenceKey], tc.want) {
			t.Errorf("%s: %s = %v, want %v", tc.where, apigen.OpenAPIReferenceKey, tc.got[apigen.OpenAPIReferenceKey], tc.want)
		}
	}
	listItems := doc["paths"].(map[string]any)["/api/items"].(map[string]any)["get"].(map[string]any)
	if listItems["operationId"] != byID["options"] {
		t.Errorf("options names %v, but listItems' operationId is %v", byID["options"], listItems["operationId"])
	}
	if ids := parameter("/api/items", "get", "ids"); ids["schema"].(map[string]any)[apigen.OpenAPIReferenceKey] != nil {
		t.Error("listItems(ids): the reference is on the list's items too; it sits on the parameter")
	}
	if q := parameter("/api/items", "get", "q"); q[apigen.OpenAPISearchKey] != true || q[apigen.OpenAPIReferenceKey] != nil {
		t.Errorf("listItems(q) = %v, want %s true and no reference", q, apigen.OpenAPISearchKey)
	}
	if p := parameter("/api/items", "get", "inStock"); p[apigen.OpenAPISearchKey] != nil || p[apigen.OpenAPIReferenceKey] != nil {
		t.Errorf("listItems(inStock) carries a vendor key: %v", p)
	}
	if line := property("BasketLine", "quantity"); line[apigen.OpenAPIReferenceKey] != nil {
		t.Errorf("BasketLine.quantity refers: %v", line)
	}
}

// TestADerivedReferenceIsDocumentedAsAWrittenOne: a relation's key on a
// type with @source, which declares no RefersTo, refers to the API's type
// sourced from the relation's table (D61, amended), and the documents
// write it as they write the RefersTo an author could have written.
func TestADerivedReferenceIsDocumentedAsAWrittenOne(t *testing.T) {
	derived := generateShop(t, nil)
	written := generateShop(t, &ir.ReferenceDef{Type: "ProductView"})

	key := func(output *apigen.APIOutput) any {
		t.Helper()
		doc := decodeOpenAPI(t, output.OpenAPISpecRaw)
		line := doc["components"].(map[string]any)["schemas"].(map[string]any)["OrderLineView"].(map[string]any)
		return line["properties"].(map[string]any)["productId"].(map[string]any)[apigen.OpenAPIReferenceKey]
	}
	want := map[string]any{"target": "#/components/schemas/ProductView", "options": "CatalogListProductsHandler", "value": "id", "label": "name"}
	if got := key(derived); !reflect.DeepEqual(got, want) {
		t.Fatalf("the derived reference is documented as %v, want %v", got, want)
	}
	if got := key(written); !reflect.DeepEqual(got, want) {
		t.Fatalf("the written reference is documented as %v, want %v", got, want)
	}

	toolReference := func(output *apigen.APIOutput) *apigen.Reference {
		t.Helper()
		for _, field := range output.TypeFields["OrderLineView"] {
			if field.Name == "productId" {
				return field.Reference
			}
		}
		t.Fatal("OrderLineView has no productId")
		return nil
	}
	wantTool := &apigen.Reference{
		Target: "ProductView", OptionsOperationID: "CatalogListProductsHandler", OptionsTool: "catalog.listProducts",
		Value: "id", Label: "name",
	}
	if got := toolReference(derived); !reflect.DeepEqual(got, wantTool) {
		t.Fatalf("the derived reference the tools read is %+v, want %+v", got, wantTool)
	}
	if got := toolReference(written); !reflect.DeepEqual(got, wantTool) {
		t.Fatalf("the written reference the tools read is %+v, want %+v", got, wantTool)
	}
}

// TestADependencysReferenceIsHeldToTheFieldsType: an API returns a type of
// an API dependency (D66) whose fields say RefersTo. The reference is read
// against the API, whose operations its documents name, so it holds only
// when the value it resolves to there is of the field's type: shelfItemId,
// a string, does not refer to ItemView, whose id is an Identity.UUID, in
// the document or the tools, and itemId does.
func TestADependencysReferenceIsHeldToTheFieldsType(t *testing.T) {
	uuid := &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"go": "string"}}
	shelves := ir.NewSchema("shelves-api", ir.SchemaKindAPI)
	shelves.Scalars["Identity.UUID"] = uuid
	shelves.Types["ShelfLine"] = &ir.TypeDef{Name: "ShelfLine", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "itemId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}},
		{Name: "shelfItemId", TypeRef: ir.TypeRef{Name: "string"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}},
	}}

	api := ir.NewSchema("shop", ir.SchemaKindAPI)
	api.Scalars["Identity.UUID"] = uuid
	api.Imports = []ir.Import{{Package: "@schemas/shelves-api", Types: []string{"ShelfLine"}}}
	api.Types["ItemView"] = &ir.TypeDef{Name: "ItemView", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
	}}
	api.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "listItems", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "ItemView", IsArray: true}, Required: true},
		{Name: "listShelfLines", HTTPMethod: "GET", RestPath: "lines", TypeRef: ir.TypeRef{Name: "ShelfLine", IsArray: true}, Required: true},
	}}}
	output, err := apigen.Generate(api, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "shop",
		ModulePath:   "example.com/schemas/api/shop",
		TypesModule:  "example.com/schemas/types/go/shop",
		Dependencies: map[string]*ir.Schema{"shelves-api": shelves},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	doc := decodeOpenAPI(t, output.OpenAPISpecRaw)
	properties := doc["components"].(map[string]any)["schemas"].(map[string]any)["ShelfLine"].(map[string]any)["properties"].(map[string]any)
	tools := map[string]*apigen.Reference{}
	for _, field := range output.TypeFields["ShelfLine"] {
		tools[field.Name] = field.Reference
	}
	for name, refers := range map[string]bool{"itemId": true, "shelfItemId": false} {
		key := properties[name].(map[string]any)[apigen.OpenAPIReferenceKey]
		if (key != nil) != refers || (tools[name] != nil) != refers {
			t.Errorf("ShelfLine.%s: the document's key is %v and the tools' reference %+v; want a reference: %v", name, key, tools[name], refers)
		}
	}
}

// generateShop is the API output of a shop whose order line, sourced from
// shop-db's OrderLine, holds the key of its product relation, productId,
// with ref as its RefersTo (nil for none). ProductView is sourced from
// shop-db's Product, and listProducts lists it.
func generateShop(t *testing.T, ref *ir.ReferenceDef) *apigen.APIOutput {
	t.Helper()
	uuid := &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"go": "string"}}
	db := ir.NewSchema("shop-db", ir.SchemaKindDB)
	db.Scalars["Identity.UUID"] = uuid
	db.Types["Product"] = &ir.TypeDef{Name: "Product", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	db.Types["OrderLine"] = &ir.TypeDef{Name: "OrderLine", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		{Name: "product", TypeRef: ir.TypeRef{Name: "Product"}, Required: true, Relation: &ir.RelationDef{Type: "Product"}},
		{Name: "quantity", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}}

	api := ir.NewSchema("shop", ir.SchemaKindAPI)
	api.Scalars["Identity.UUID"] = uuid
	api.Types["OrderLineView"] = &ir.TypeDef{Name: "OrderLineView", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.OrderLine"}, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "productId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, References: ref},
		{Name: "quantity", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
	}}
	api.Types["ProductView"] = &ir.TypeDef{Name: "ProductView", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}, Display: &ir.TypeDisplay{TitleField: "name"}, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
	}}
	api.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "listProducts", HTTPMethod: "GET", RestPath: "products", TypeRef: ir.TypeRef{Name: "ProductView", IsArray: true}, Required: true},
		{Name: "listOrderLines", HTTPMethod: "GET", RestPath: "lines", TypeRef: ir.TypeRef{Name: "OrderLineView", IsArray: true}, Required: true},
	}}}

	output, err := apigen.Generate(api, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   "shop",
		ModulePath:   "example.com/schemas/api/shop",
		TypesModule:  "example.com/schemas/types/go/shop",
		Dependencies: map[string]*ir.Schema{"shop-db": db},
		Clock:        codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	return output
}

func decodeOpenAPI(t *testing.T, raw string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatalf("decode the OpenAPI document: %v", err)
	}
	return doc
}
