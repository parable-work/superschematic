package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// refsSchema is an API whose Catalog lists ItemView with an ids lookup,
// and whose BasketLine names an item by id.
func refsSchema() *ir.Schema {
	s := ir.NewSchema("catalog", ir.SchemaKindAPI)
	s.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	s.Scalars["Identity.Slug"] = &ir.ScalarDef{Name: "Identity.Slug", LanguagePrimitive: ir.LanguageString}
	s.Types["ItemView"] = &ir.TypeDef{Name: "ItemView", Owner: "src/catalog.schema.json", Role: ir.RoleAPIView,
		Display: &ir.TypeDisplay{TitleField: "name"}, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "sku", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, Required: true},
		}}
	s.Types["BasketLine"] = &ir.TypeDef{Name: "BasketLine", Owner: "src/catalog.schema.json", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "itemId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}},
		{Name: "itemIds", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsArray: true}, References: &ir.ReferenceDef{Type: "ItemView"}},
		{Name: "giftSku", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, References: &ir.ReferenceDef{Type: "ItemView", Options: "Catalog.listItems", Value: "sku", Label: "name"}},
	}}
	s.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "listItems", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "ItemView", IsArray: true}, Required: true, Arguments: []*ir.ArgumentDef{
			{Name: "ids", TypeRef: ir.TypeRef{Name: "Identity.UUID", IsArray: true}, IsQuery: true, References: &ir.ReferenceDef{Type: "ItemView"}},
		}},
		{Name: "getItem", HTTPMethod: "GET", RestPath: "items/{itemId}", TypeRef: ir.TypeRef{Name: "ItemView"}, Required: true, Arguments: []*ir.ArgumentDef{
			{Name: "itemId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, References: &ir.ReferenceDef{Type: "ItemView"}},
		}},
	}}}
	return s
}

func referenceErrors(schema *ir.Schema, in Input) []string {
	r := &Result{}
	checkReferences(schema, in, r)
	return errorStrings(r)
}

func TestReferencesVerify(t *testing.T) {
	if got := referenceErrors(refsSchema(), Input{}); len(got) != 0 {
		t.Fatalf("errors = %q", got)
	}
}

// TestReferencesRefused: each rule a reference breaks, with its holder
// named as Type.field or Set.operation(argument), and the file of a
// type's field.
func TestReferencesRefused(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Schema)
		want []string
	}{
		{"outside an API", func(s *ir.Schema) { s.Kind = ir.SchemaKindGeneral }, []string{
			"src/catalog.schema.json: BasketLine.itemId: RefersTo is an API's; catalog is a General schema",
			"src/catalog.schema.json: BasketLine.itemIds: RefersTo is an API's; catalog is a General schema",
			"src/catalog.schema.json: BasketLine.giftSku: RefersTo is an API's; catalog is a General schema",
			"Catalog.listItems(ids): RefersTo is an API's; catalog is a General schema",
			"Catalog.getItem(itemId): RefersTo is an API's; catalog is a General schema",
		}},
		{"on a result", func(s *ir.Schema) {
			s.OperationSets[0].Operations[1].References = &ir.ReferenceDef{Type: "ItemView"}
		}, []string{"Catalog.getItem: RefersTo on an operation's result; a reference sits on a field or an argument"}},
		{"on a map", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[1].TypeRef = ir.TypeRef{Name: "Identity.UUID", IsMap: true}
		}, []string{"src/catalog.schema.json: BasketLine.itemIds: RefersTo on a map; a reference holds one value or a list of them"}},
		{"on a list of lists", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[1].TypeRef = ir.TypeRef{Name: "Identity.UUID", IsArray: true, IsArrayOfArrays: true}
		}, []string{"src/catalog.schema.json: BasketLine.itemIds: RefersTo on a list of lists"}},
		{"an unknown target", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[0].References.Type = "Product"
		}, []string{"src/catalog.schema.json: BasketLine.itemId: RefersTo names Product, which is not a type of catalog or its dependencies"}},
		{"an enum", func(s *ir.Schema) {
			s.Enums["Colour"] = &ir.EnumDef{Name: "Colour"}
			s.OperationSets[0].Operations[0].Arguments[0].References.Type = "Colour"
		}, []string{"Catalog.listItems(ids): RefersTo names Colour, an enum; a reference names an object type"}},
		{"a value of another type", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[0].References.Value = "sku"
		}, []string{"src/catalog.schema.json: BasketLine.itemId: is Identity.UUID, but ItemView.sku, its value, is Identity.Slug"}},
		{"a list's element of another type", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[1].TypeRef.Name = "string"
		}, []string{"src/catalog.schema.json: BasketLine.itemIds: is string, but ItemView.id, its value, is Identity.UUID"}},
		{"no options", func(s *ir.Schema) {
			s.OperationSets[0].Operations[0].HTTPMethod = "POST"
		}, []string{
			"src/catalog.schema.json: BasketLine.itemId: no operation of catalog lists ItemView (a GET returning ItemView[] with no path argument and no required one, that an end user may call); declare one or name it with options",
			"src/catalog.schema.json: BasketLine.itemIds: no operation of catalog lists ItemView (a GET returning ItemView[] with no path argument and no required one, that an end user may call); declare one or name it with options",
			"src/catalog.schema.json: BasketLine.giftSku: options Catalog.listItems does not list ItemView: it is POST items returning ItemView[]",
			"Catalog.listItems(ids): no operation of catalog lists ItemView (a GET returning ItemView[] with no path argument and no required one, that an end user may call); declare one or name it with options",
			"Catalog.getItem(itemId): no operation of catalog lists ItemView (a GET returning ItemView[] with no path argument and no required one, that an end user may call); declare one or name it with options",
		}},
		{"a label that is no text", func(s *ir.Schema) {
			s.Types["BasketLine"].Fields[2].References.Label = "id"
			s.Scalars["Identity.UUID"].TypeMappings = map[string]string{"json_schema": "any"}
		}, []string{"src/catalog.schema.json: BasketLine.giftSku: label id of ItemView holds Identity.UUID; a label is one text value: a string, or a scalar whose values are strings"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := refsSchema()
			tc.edit(schema)
			if got := referenceErrors(schema, Input{}); !slices.Equal(got, tc.want) {
				t.Errorf("errors =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// A reference may name a dependency's type, which the loader reads into
// Input.DependencySchemas; a field a local base class contributes is
// reported once, on the base.
func TestReferencesToADependencyAndInheritedFields(t *testing.T) {
	schema := refsSchema()
	shelf := &ir.TypeDef{Name: "ShelfView", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
	}}
	dep := ir.NewSchema("catalog-types", ir.SchemaKindGeneral)
	dep.Types["ShelfView"] = shelf
	schema.OperationSets = append(schema.OperationSets, &ir.OperationSet{Name: "Shelves", Operations: []*ir.FieldDef{
		{Name: "listShelves", HTTPMethod: "GET", RestPath: "shelves", TypeRef: ir.TypeRef{Name: "ShelfView", IsArray: true}},
	}})
	line := schema.Types["BasketLine"]
	line.Fields = append(line.Fields, &ir.FieldDef{Name: "shelfId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, References: &ir.ReferenceDef{Type: "ShelfView"}})
	if got := referenceErrors(schema, Input{}); !slices.Equal(got, []string{
		"src/catalog.schema.json: BasketLine.shelfId: RefersTo names ShelfView, which is not a type of catalog or its dependencies",
	}) {
		t.Fatalf("without the dependency: %q", got)
	}
	if got := referenceErrors(schema, Input{DependencySchemas: map[string]*ir.Schema{"catalog-types": dep}}); len(got) != 0 {
		t.Fatalf("with the dependency: %q", got)
	}

	line.Fields[0].References.Value = "sku"
	schema.Types["GiftLine"] = &ir.TypeDef{Name: "GiftLine", Owner: "src/catalog.schema.json", Role: ir.RoleAPIInput, Fields: []*ir.FieldDef{
		{Name: "itemId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, InheritedFrom: "BasketLine", References: line.Fields[0].References},
	}}
	if got := referenceErrors(schema, Input{DependencySchemas: map[string]*ir.Schema{"catalog-types": dep}}); !slices.Equal(got, []string{
		"src/catalog.schema.json: BasketLine.itemId: is Identity.UUID, but ItemView.sku, its value, is Identity.Slug",
	}) {
		t.Fatalf("inherited: %q", got)
	}
}
