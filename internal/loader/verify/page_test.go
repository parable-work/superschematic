package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestPagesNeedAPagedList: a @paginated operation, authored as IR, is a GET
// returning a list of one level, declares no argument named as a page
// parameter, is mounted by a generated router, answers JSON, and shares no
// name with its SDK iterator; a page on a type's field is refused.
func TestPagesNeedAPagedList(t *testing.T) {
	page := func() *ir.OperationPage { return &ir.OperationPage{DefaultLimit: 20, MaxLimit: 100} }
	list := ir.TypeRef{Name: "Item", IsArray: true}
	schema := ir.NewSchema("catalog", ir.SchemaKindAPI)
	schema.Types["Item"] = &ir.TypeDef{Name: "Item", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "String"}, Required: true},
		{Name: "tags", TypeRef: ir.TypeRef{Name: "String", IsArray: true}, Page: page()},
	}}
	schema.OperationSets = []*ir.OperationSet{
		{Name: "Catalog", Operations: []*ir.FieldDef{
			{Name: "listItems", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page(), Arguments: []*ir.ArgumentDef{
				{Name: "q", TypeRef: ir.TypeRef{Name: "String"}, IsQuery: true},
			}},
			{Name: "searchItems", HTTPMethod: "POST", TypeRef: list, Required: true, Page: page()},
			{Name: "listAny", TypeRef: list, Required: true, Page: page()},
			{Name: "getItem", HTTPMethod: "GET", TypeRef: ir.TypeRef{Name: "Item"}, Required: true, Page: page()},
			{Name: "listMaybe", HTTPMethod: "GET", TypeRef: list, Page: page()},
			{Name: "itemsByTag", HTTPMethod: "GET", TypeRef: ir.TypeRef{Name: "Item", IsArray: true, IsMap: true}, Required: true, Page: page()},
			{Name: "listGrid", HTTPMethod: "GET", TypeRef: ir.TypeRef{Name: "Item", IsArray: true, IsArrayOfArrays: true}, Required: true, Page: page()},
			{Name: "listPaged", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page(), Arguments: []*ir.ArgumentDef{
				{Name: "cursor", TypeRef: ir.TypeRef{Name: "String"}, IsQuery: true},
				{Name: "limit", TypeRef: ir.TypeRef{Name: "Integer"}, IsQuery: true},
				{Name: "page", TypeRef: ir.TypeRef{Name: "Integer"}, IsQuery: true},
			}},
			{Name: "listManual", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page(), ManualRouteRegistration: true},
			{Name: "listSealed", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page(), Encrypted: true},
			{Name: "listFiles", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page(), HTTPBinaryStream: &ir.HTTPBinaryStreamDef{ContentType: "application/zip"}},
			{Name: "listTags", HTTPMethod: "GET", TypeRef: list, Required: true},
		}},
		{Name: "Vault", Encrypted: true, Operations: []*ir.FieldDef{
			{Name: "listSecrets", HTTPMethod: "GET", TypeRef: list, Required: true, Page: page()},
		}},
		{Name: "CatalogExtras", Operations: []*ir.FieldDef{
			{Name: "listItemsAll", HTTPMethod: "GET", TypeRef: list, Required: true},
		}},
	}
	r := &Result{}
	checkPages(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"Item.tags: a page on a field; @paginated sits on an operation",
		"CatalogExtras.listItemsAll: an SDK names listItemsAll the paged Catalog.listItems's iterator; rename one of them",
		"Catalog.searchItems: @paginated needs a GET operation, not POST",
		"Catalog.listAny: @paginated needs a GET operation, not one without a method",
		"Catalog.getItem: @paginated needs a list result, not Item",
		"Catalog.listMaybe: @paginated needs a list result, not Nullable<Item[]>",
		"Catalog.itemsByTag: @paginated needs a list result, not Record<string, Item[]>",
		"Catalog.listGrid: @paginated needs a list of one level, not Item[][]",
		"Catalog.listPaged: @paginated adds the query parameters cursor and limit, and an implementation takes them as page; the operation declares an argument named cursor",
		"Catalog.listPaged: @paginated adds the query parameters cursor and limit, and an implementation takes them as page; the operation declares an argument named limit",
		"Catalog.listPaged: @paginated adds the query parameters cursor and limit, and an implementation takes them as page; the operation declares an argument named page",
		"Catalog.listManual: @paginated on a @manualRouteRegistration operation, which no generated router reads",
		"Catalog.listSealed: @paginated on an encrypted operation",
		"Catalog.listFiles: @paginated on an operation with @httpBinaryStream, which answers no JSON list",
		"Vault.listSecrets: @paginated on an encrypted operation",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}
