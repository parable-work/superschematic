package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A field or an argument without a reference marshals without the key,
// so the IR of every schema written before RefersTo existed is the same
// bytes; with one, references sits right after relation on a field and
// after encrypted on an argument, then search, its members in order and
// the unsaid ones left out, and it reads back the same through JSON and
// YAML.
func TestReference_KeyPositionOmissionAndRoundTrip(t *testing.T) {
	field := &FieldDef{Name: "itemId", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, Relation: &RelationDef{Type: "Item"}, HasMany: false, Secret: true}
	arg := &ArgumentDef{Name: "ids", TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}, IsQuery: true, Encrypted: true}
	for _, v := range []any{field, arg} {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(`"references"`)) || bytes.Contains(data, []byte(`"search"`)) {
			t.Fatalf("a value without a reference or a search wrote the key: %s", data)
		}
	}

	field.References = &ReferenceDef{Type: "ItemView", Options: "Catalog.listItems", Value: "id", Label: "name"}
	arg.References = &ReferenceDef{Type: "ItemView"}
	arg.Search = true
	for _, tc := range []struct {
		value any
		want  string
	}{
		{field, `"relation":{"type":"Item"},"references":{"type":"ItemView","options":"Catalog.listItems","value":"id","label":"name"},"secret":true`},
		{arg, `"encrypted":true,"references":{"type":"ItemView"},"search":true}`},
	} {
		data, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(data, []byte(tc.want)) {
			t.Fatalf("marshaled = %s\nwant it to contain %s", data, tc.want)
		}
		back := reflect.New(reflect.TypeOf(tc.value).Elem()).Interface()
		if err := json.Unmarshal(data, back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, tc.value) {
			t.Fatalf("JSON round trip = %+v, want %+v", back, tc.value)
		}
		text, err := yaml.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		fromYAML := reflect.New(reflect.TypeOf(tc.value).Elem()).Interface()
		if err := yaml.Unmarshal(text, fromYAML); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(fromYAML, tc.value) {
			t.Fatalf("YAML round trip = %+v, want %+v\n%s", fromYAML, tc.value, text)
		}
	}
}

// catalogSchema is an API whose Catalog set lists ItemView, with a search,
// an ids lookup and a filter, and reads one item by its path argument.
func catalogSchema() *Schema {
	s := NewSchema("catalog", SchemaKindAPI)
	s.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID", LanguagePrimitive: LanguageString}
	s.Scalars["Identity.Slug"] = &ScalarDef{Name: "Identity.Slug", LanguagePrimitive: LanguageString}
	s.Scalars["Generic.JSON"] = &ScalarDef{Name: "Generic.JSON", LanguagePrimitive: LanguageString, TypeMappings: map[string]string{"json_schema": "any"}}
	s.Types["ItemView"] = &TypeDef{Name: "ItemView", Role: RoleAPIView, Display: &TypeDisplay{TitleField: "name"}, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "name", TypeRef: TypeRef{Name: "string"}, Required: true},
		{Name: "sku", TypeRef: TypeRef{Name: "Identity.Slug"}, Required: true},
		{Name: "tags", TypeRef: TypeRef{Name: "string", IsArray: true}},
		{Name: "price", TypeRef: TypeRef{Name: "number"}},
		{Name: "secretName", TypeRef: TypeRef{Name: "string"}, Secret: true},
		{Name: "hiddenName", TypeRef: TypeRef{Name: "string"}, UIHidden: true},
		{Name: "extra", TypeRef: TypeRef{Name: "Generic.JSON"}},
	}}
	s.Types["NoteView"] = &TypeDef{Name: "NoteView", Role: RoleAPIView, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
	}}
	s.Types["Tagged"] = &TypeDef{Name: "Tagged", Role: RoleTrait, IsTrait: true}
	s.Enums["Colour"] = &EnumDef{Name: "Colour"}
	s.Unions["Shape"] = &UnionDef{Name: "Shape", Types: []string{"ItemView"}}
	ids := &ArgumentDef{Name: "ids", TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}, IsQuery: true, References: &ReferenceDef{Type: "ItemView"}}
	s.OperationSets = []*OperationSet{{Name: "Catalog", Operations: []*FieldDef{
		{Name: "listItems", HTTPMethod: "GET", RestPath: "items", TypeRef: TypeRef{Name: "ItemView", IsArray: true}, Required: true, Arguments: []*ArgumentDef{
			{Name: "q", TypeRef: TypeRef{Name: "string"}, IsQuery: true, Search: true},
			ids,
			{Name: "inStock", TypeRef: TypeRef{Name: "boolean"}, IsQuery: true},
		}},
		{Name: "getItem", HTTPMethod: "GET", RestPath: "items/{itemId}", TypeRef: TypeRef{Name: "ItemView"}, Required: true, Arguments: []*ArgumentDef{
			{Name: "itemId", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, References: &ReferenceDef{Type: "ItemView"}},
		}},
		{Name: "listNotes", HTTPMethod: "GET", RestPath: "items/{itemId}/notes", TypeRef: TypeRef{Name: "NoteView", IsArray: true}, Arguments: []*ArgumentDef{
			{Name: "itemId", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		}},
		{Name: "searchNotes", HTTPMethod: "GET", RestPath: "notes", TypeRef: TypeRef{Name: "NoteView", IsArray: true}, Arguments: []*ArgumentDef{
			{Name: "q", TypeRef: TypeRef{Name: "string"}, IsQuery: true, Required: true},
		}},
		{Name: "exportNotes", HTTPMethod: "GET", RestPath: "notes/export", TypeRef: TypeRef{Name: "NoteView", IsArray: true}, ManualRouteRegistration: true},
		{Name: "notesFromHook", HTTPMethod: "GET", RestPath: "notes/hook", TypeRef: TypeRef{Name: "NoteView", IsArray: true}, Webhook: true},
		{Name: "reindexNotes", HTTPMethod: "GET", RestPath: "notes/reindex", TypeRef: TypeRef{Name: "NoteView", IsArray: true}, ServiceCallers: &ServiceCallers{Mode: ServiceCallersRequire}},
		{Name: "placeItem", HTTPMethod: "POST", RestPath: "items", TypeRef: TypeRef{Name: "ItemView"}},
		{Name: "itemGrid", HTTPMethod: "GET", RestPath: "grid", TypeRef: TypeRef{Name: "ItemView", IsArray: true, IsArrayOfArrays: true}},
	}}}
	return s
}

func TestResolveReference(t *testing.T) {
	schema := catalogSchema()
	catalog := schema.OperationSets[0]
	item := schema.Types["ItemView"]

	got, err := ResolveReference(schema, nil, &ReferenceDef{Type: "ItemView"})
	if err != nil {
		t.Fatal(err)
	}
	want := ResolvedReference{
		Target: item, TargetName: "ItemView", OptionsSet: catalog, Options: catalog.Operations[0],
		Value: item.Fields[0], Label: item.Fields[1], Lookup: catalog.Operations[0].Arguments[1],
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unsaid members: got %+v, want %+v", got, want)
	}

	got, err = ResolveReference(schema, nil, &ReferenceDef{Type: "ItemView", Options: "Catalog.listItems", Value: "sku", Label: "sku"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != item.Fields[2] || got.Label != item.Fields[2] || got.Lookup != nil || got.Options != catalog.Operations[0] {
		t.Fatalf("by sku: got %+v, want value and label sku, no lookup (ids refers by id)", got)
	}

	// A target without a titleField has no label, and its one list that a
	// UI may call is its options; the others list it with a required
	// argument, by hand, for a webhook or for services alone.
	schema.OperationSets[0].Operations = append(schema.OperationSets[0].Operations,
		&FieldDef{Name: "allNotes", HTTPMethod: "GET", RestPath: "notes/all", TypeRef: TypeRef{Name: "NoteView", IsArray: true}})
	got, err = ResolveReference(schema, nil, &ReferenceDef{Type: "NoteView"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Options.Name != "allNotes" || got.Label != nil || got.Lookup != nil {
		t.Fatalf("notes: got %+v", got)
	}
	schema.OperationSets[0].Operations = schema.OperationSets[0].Operations[:len(schema.OperationSets[0].Operations)-1]

	// A dependency's type, found by name.
	dep := NewSchema("catalog-types", SchemaKindGeneral)
	dep.Types["ShelfView"] = &TypeDef{Name: "ShelfView", Role: RoleEmbeddedStruct, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "title", TypeRef: TypeRef{Name: "Shelf.Title"}},
	}}
	dep.Scalars["Shelf.Title"] = &ScalarDef{Name: "Shelf.Title", LanguagePrimitive: LanguageString}
	shelves := &FieldDef{Name: "listShelves", HTTPMethod: "GET", RestPath: "shelves", TypeRef: TypeRef{Name: "ShelfView", IsArray: true}}
	schema.OperationSets = append(schema.OperationSets, &OperationSet{Name: "Shelves", Operations: []*FieldDef{shelves}})
	got, err = ResolveReference(schema, map[string]*Schema{"catalog-types": dep}, &ReferenceDef{Type: "ShelfView", Label: "title"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != dep.Types["ShelfView"] || got.TargetService != "catalog-types" || got.Options != shelves || got.Label != dep.Types["ShelfView"].Fields[1] {
		t.Fatalf("a dependency's type: got %+v", got)
	}
}

// A dependency's type a reference names is found through the import that
// names it, before the dependencies' types by name (D61, amended): a
// General and an API dependency both declare ShelfView, and the schema
// imports it from the API, whose name sorts last. Without that import the
// first dependency by name declares it, as before.
func TestResolveReferenceThroughTheImport(t *testing.T) {
	shelf := func(service string, kind SchemaKind) *Schema {
		dep := NewSchema(service, kind)
		dep.Types["ShelfView"] = &TypeDef{Name: "ShelfView", Role: RoleAPIView, Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "name", TypeRef: TypeRef{Name: "string"}, Required: true},
		}}
		return dep
	}
	general, api := shelf("catalog-types", SchemaKindGeneral), shelf("shelves-api", SchemaKindAPI)
	dependencies := map[string]*Schema{"catalog-types": general, "shelves-api": api}

	schema := catalogSchema()
	schema.OperationSets = append(schema.OperationSets, &OperationSet{Name: "Shelves", Operations: []*FieldDef{
		{Name: "listShelves", HTTPMethod: "GET", RestPath: "shelves", TypeRef: TypeRef{Name: "ShelfView", IsArray: true}}}})
	schema.Imports = []Import{
		{Package: "@schemas/catalog-types", Types: []string{"Colourway"}},
		{Package: "@schemas/shelves-api", Types: []string{"ShelfView"}},
	}
	for _, tc := range []struct {
		name    string
		imports []Import
		want    *TypeDef
		service string
	}{
		{"the import names the API", schema.Imports, api.Types["ShelfView"], "shelves-api"},
		{"no import names it", nil, general.Types["ShelfView"], "catalog-types"},
		{"the import names no dependency", []Import{{Package: "@schemas/elsewhere", Types: []string{"ShelfView"}}}, general.Types["ShelfView"], "catalog-types"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema.Imports = tc.imports
			got, err := ResolveReference(schema, dependencies, &ReferenceDef{Type: "ShelfView"})
			if err != nil {
				t.Fatal(err)
			}
			if got.Target != tc.want || got.TargetService != tc.service {
				t.Fatalf("target = %p from %q, want %p from %q", got.Target, got.TargetService, tc.want, tc.service)
			}
		})
	}

	// The schema's own type comes first, whatever its imports say.
	schema.Imports = []Import{{Package: "@schemas/shelves-api", Types: []string{"ItemView"}}}
	api.Types["ItemView"] = &TypeDef{Name: "ItemView", Role: RoleAPIView, Fields: []*FieldDef{{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}}}}
	got, err := ResolveReference(schema, dependencies, &ReferenceDef{Type: "ItemView"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Target != schema.Types["ItemView"] || got.TargetService != "" {
		t.Fatalf("own type: target from %q, want the schema's own", got.TargetService)
	}
}

func TestServiceNameForPackage(t *testing.T) {
	for in, want := range map[string]string{"@schemas/web-db": "web-db", "web-db": "web-db", "@a/b/c": "c", "": ""} {
		if got := ServiceNameForPackage(in); got != want {
			t.Errorf("ServiceNameForPackage(%q) = %q, want %q", in, got, want)
		}
	}
}

// shopSchemas is an API, shop, whose OrderLineView is @source(shop-db.OrderLine)
// and holds productId, the key of OrderLine.product, a Relation to Product;
// its ProductView is @source(shop-db.Product) and listProducts lists it.
// The DB dependency, shop-db, is the second value.
func shopSchemas() (*Schema, map[string]*Schema) {
	db := NewSchema("shop-db", SchemaKindDB)
	db.Types["Product"] = &TypeDef{Name: "Product", Role: RoleDBTable, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		{Name: "name", TypeRef: TypeRef{Name: "string"}, Required: true},
	}}
	db.Types["OrderLine"] = &TypeDef{Name: "OrderLine", Role: RoleDBTable, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
		{Name: "product", TypeRef: TypeRef{Name: "Product"}, Required: true, Relation: &RelationDef{Type: "Product"}},
		{Name: "reviews", TypeRef: TypeRef{Name: "Review", IsArray: true}, HasMany: true},
		{Name: "tags", TypeRef: TypeRef{Name: "Tag", IsArray: true}, ManyToMany: true},
		{Name: "quantity", TypeRef: TypeRef{Name: "number"}, Required: true},
	}}

	api := NewSchema("shop", SchemaKindAPI)
	api.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID", LanguagePrimitive: LanguageString}
	api.Types["OrderLineView"] = &TypeDef{Name: "OrderLineView", Role: RoleAPIView, Source: &SourceRef{Target: "shop-db.OrderLine"}, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "productId", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "quantity", TypeRef: TypeRef{Name: "number"}, Required: true},
	}}
	api.Types["ProductView"] = &TypeDef{Name: "ProductView", Role: RoleAPIView, Source: &SourceRef{Target: "shop-db.Product"}, Display: &TypeDisplay{TitleField: "name"}, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "name", TypeRef: TypeRef{Name: "string"}, Required: true},
	}}
	api.OperationSets = []*OperationSet{{Name: "Catalog", Operations: []*FieldDef{
		{Name: "listProducts", HTTPMethod: "GET", RestPath: "products", TypeRef: TypeRef{Name: "ProductView", IsArray: true}, Required: true},
		{Name: "listOrderLines", HTTPMethod: "GET", RestPath: "lines", TypeRef: TypeRef{Name: "OrderLineView", IsArray: true}, Required: true},
	}}}
	return api, map[string]*Schema{"shop-db": db}
}

// FieldReference gives a field's RefersTo as written, or, on a type with
// @source, the reference a relation's key derives (D61, amended): the
// API's one type whose @source is the relation's type, when an operation
// lists it. Anything else derives nothing.
func TestFieldReference(t *testing.T) {
	type want struct {
		ref     *ReferenceDef
		derived bool
	}
	productCard := func(api *Schema) {
		api.Types["ProductCard"] = &TypeDef{Name: "ProductCard", Role: RoleAPIView, Source: &SourceRef{Target: "shop-db.Product"}, Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true},
		}}
		api.OperationSets[0].Operations = append(api.OperationSets[0].Operations,
			&FieldDef{Name: "listProductCards", HTTPMethod: "GET", RestPath: "cards", TypeRef: TypeRef{Name: "ProductCard", IsArray: true}})
	}
	key := func(api *Schema) *FieldDef { return api.Types["OrderLineView"].Fields[1] }
	for _, tc := range []struct {
		name  string
		edit  func(api *Schema, db *Schema)
		field string
		want  want
	}{
		{"a derived key", nil, "productId", want{&ReferenceDef{Type: "ProductView"}, true}},
		{"a derived key that is @virtual", func(api, _ *Schema) { key(api).Virtual = true }, "productId", want{&ReferenceDef{Type: "ProductView"}, true}},
		{"an unqualified @source of the API's own types", func(api, db *Schema) {
			api.Types["OrderLine"], api.Types["Product"] = db.Types["OrderLine"], db.Types["Product"]
			api.Types["OrderLineView"].Source.Target = "OrderLine"
			api.Types["ProductView"].Source.Target = "shop.Product"
		}, "productId", want{&ReferenceDef{Type: "ProductView"}, true}},
		{"an explicit target wins, with its members as written", func(api, _ *Schema) {
			productCard(api)
			key(api).References = &ReferenceDef{Type: "ProductCard", Options: "listProductCards"}
		}, "productId", want{&ReferenceDef{Type: "ProductCard", Options: "listProductCards"}, false}},
		{"an explicit target that does not resolve stands as written", func(api, _ *Schema) {
			key(api).References = &ReferenceDef{Type: "Basket"}
		}, "productId", want{&ReferenceDef{Type: "Basket"}, false}},
		{"no candidate", func(api, _ *Schema) {
			delete(api.Types, "ProductView")
			api.OperationSets[0].Operations = api.OperationSets[0].Operations[1:]
		}, "productId", want{}},
		{"a candidate sourced from another service's Product", func(api, _ *Schema) { api.Types["ProductView"].Source.Target = "catalog-db.Product" }, "productId", want{}},
		{"two candidates", func(api, _ *Schema) { productCard(api) }, "productId", want{}},
		{"an unlisted target", func(api, _ *Schema) { api.OperationSets[0].Operations = api.OperationSets[0].Operations[1:] }, "productId", want{}},
		{"a target listed twice", func(api, _ *Schema) {
			api.OperationSets[0].Operations = append(api.OperationSets[0].Operations,
				&FieldDef{Name: "browse", HTTPMethod: "GET", RestPath: "browse", TypeRef: TypeRef{Name: "ProductView", IsArray: true}})
		}, "productId", want{}},
		{"a target whose id is another type", func(api, _ *Schema) { key(api).TypeRef.Name = "string" }, "productId", want{}},
		{"a field named for the relation", func(api, _ *Schema) { key(api).Name = "product" }, "product", want{}},
		{"a HasMany", func(api, _ *Schema) { key(api).Name = "reviewsId" }, "reviewsId", want{}},
		{"a many-to-many", func(api, _ *Schema) { key(api).Name = "tagsId" }, "tagsId", want{}},
		{"a source field that is no relation", func(api, _ *Schema) { key(api).Name = "quantityId" }, "quantityId", want{}},
		{"a source that declares the key itself", func(_, db *Schema) {
			line := db.Types["OrderLine"]
			line.Fields = append(line.Fields, &FieldDef{Name: "productId", TypeRef: TypeRef{Name: "Identity.UUID"}})
		}, "productId", want{}},
		{"a list key", func(api, _ *Schema) { key(api).TypeRef.IsArray = true }, "productId", want{}},
		{"a holder without @source", func(api, _ *Schema) { api.Types["OrderLineView"].Source = nil }, "productId", want{}},
		{"a source that is not at hand", func(api, _ *Schema) { api.Types["OrderLineView"].Source.Target = "other-db.OrderLine" }, "productId", want{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, deps := shopSchemas()
			if tc.edit != nil {
				tc.edit(api, deps["shop-db"])
			}
			holder := api.Types["OrderLineView"]
			field := typeField(holder, tc.field)
			if field == nil {
				t.Fatalf("OrderLineView has no field %s", tc.field)
			}
			ref, derived := FieldReference(api, deps, holder, field)
			if !reflect.DeepEqual(ref, tc.want.ref) || derived != tc.want.derived {
				t.Fatalf("FieldReference = %+v, %v; want %+v, %v", ref, derived, tc.want.ref, tc.want.derived)
			}
			if field.References != nil && ref != field.References {
				t.Fatalf("a written reference came back as a copy")
			}
			if tc.want.derived && field.References != nil {
				t.Fatalf("a derived reference was written into the field")
			}
		})
	}
}

func TestRelationKey(t *testing.T) {
	_, deps := shopSchemas()
	line := deps["shop-db"].Types["OrderLine"]
	relation, ok := RelationKey(line, &FieldDef{Name: "productId"})
	if !ok || relation != line.Fields[1] {
		t.Fatalf("RelationKey(productId) = %+v, %v; want OrderLine.product", relation, ok)
	}
	for _, name := range []string{"product", "Id", "id", "reviewsId", "tagsId", "quantityId", "productID", "customerId"} {
		if relation, ok := RelationKey(line, &FieldDef{Name: name}); ok {
			t.Errorf("RelationKey(%s) = %s, want none", name, relation.Name)
		}
	}
	if _, ok := RelationKey(nil, &FieldDef{Name: "productId"}); ok {
		t.Error("RelationKey with no source found a relation")
	}
}

func TestSourcedBy(t *testing.T) {
	api, _ := shopSchemas()
	api.Types["ProductCard"] = &TypeDef{Name: "ProductCard", Source: &SourceRef{Target: "shop-db.Product"}}
	api.Types["ProductTrait"] = &TypeDef{Name: "ProductTrait", IsTrait: true, Role: RoleTrait, Source: &SourceRef{Target: "shop-db.Product"}}
	api.Types["Local"] = &TypeDef{Name: "Local", Source: &SourceRef{Target: "Draft"}}
	names := func(types []*TypeDef) []string {
		var out []string
		for _, td := range types {
			out = append(out, td.Name)
		}
		return out
	}
	for target, want := range map[string][]string{
		"shop-db.Product":   {"ProductCard", "ProductView"},
		"shop-db.OrderLine": {"OrderLineView"},
		"shop-db.Review":    nil,
		"Draft":             {"Local"},
		"shop.Draft":        {"Local"},
	} {
		if got := names(SourcedBy(api, target)); !reflect.DeepEqual(got, want) {
			t.Errorf("SourcedBy(%s) = %v, want %v", target, got, want)
		}
	}
}

// DerivedReferences lists the references FieldReference derives, beside
// ReferencesOf, which lists only the written ones and stays as it was.
func TestDerivedReferences(t *testing.T) {
	api, deps := shopSchemas()
	api.Types["BasketLine"] = &TypeDef{Name: "BasketLine", Role: RoleAPIInput, Fields: []*FieldDef{
		{Name: "productId", TypeRef: TypeRef{Name: "Identity.UUID"}, References: &ReferenceDef{Type: "ProductView"}},
	}}
	if written := ReferencesOf(api); len(written) != 1 || written[0].Holder != "BasketLine.productId" || written[0].Derived {
		t.Fatalf("ReferencesOf = %+v, want BasketLine.productId alone, written", written)
	}
	got := DerivedReferences(api, deps)
	line := api.Types["OrderLineView"]
	want := []HeldReference{{Holder: "OrderLineView.productId", Type: line, Field: line.Fields[1], Ref: &ReferenceDef{Type: "ProductView"}, TypeRef: line.Fields[1].TypeRef, Derived: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DerivedReferences = %+v, want %+v", got, want)
	}

	line.Fields[1].References = &ReferenceDef{Type: "ProductView"}
	if got := DerivedReferences(api, deps); len(got) != 0 {
		t.Fatalf("a written reference was listed as derived: %+v", got)
	}
	api.Kind = SchemaKindGeneral
	line.Fields[1].References = nil
	if got := DerivedReferences(api, deps); len(got) != 0 {
		t.Fatalf("a General schema derived %+v", got)
	}
}

func TestResolveReferenceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edit  func(*Schema)
		ref   ReferenceDef
		wants string
	}{
		{"not an API", func(s *Schema) { s.Kind = SchemaKindGeneral }, ReferenceDef{Type: "ItemView"}, "RefersTo is an API's; catalog is a General schema"},
		{"no type", nil, ReferenceDef{}, "RefersTo names no type"},
		{"unknown type", nil, ReferenceDef{Type: "Basket"}, "RefersTo names Basket, which is not a type of catalog or its dependencies"},
		{"an enum", nil, ReferenceDef{Type: "Colour"}, "RefersTo names Colour, an enum; a reference names an object type"},
		{"a union", nil, ReferenceDef{Type: "Shape"}, "RefersTo names Shape, a union; a reference names an object type"},
		{"a scalar", nil, ReferenceDef{Type: "Identity.UUID"}, "RefersTo names Identity.UUID, a scalar; a reference names an object type"},
		{"a trait", nil, ReferenceDef{Type: "Tagged"}, "RefersTo names Tagged, a trait; a reference names an object type"},
		{"no value field", nil, ReferenceDef{Type: "ItemView", Value: "code"}, "value code is not a field of ItemView (fields: id, name, sku, tags, price, secretName, hiddenName, extra)"},
		{"a list value", nil, ReferenceDef{Type: "ItemView", Value: "tags"}, "value tags of ItemView holds string[]; a reference's value is one value, not a list or a map"},
		{"no label field", nil, ReferenceDef{Type: "ItemView", Label: "title"}, "label title is not a field of ItemView (fields:"},
		{"a number label", nil, ReferenceDef{Type: "ItemView", Label: "price"}, "label price of ItemView holds number; a label is one text value: a string, or a scalar whose values are strings"},
		{"a list label", nil, ReferenceDef{Type: "ItemView", Label: "tags"}, "label tags of ItemView holds string[]; a label is one text value"},
		{"a JSON label", nil, ReferenceDef{Type: "ItemView", Label: "extra"}, "label extra of ItemView holds Generic.JSON; a label is one text value"},
		{"a secret label", nil, ReferenceDef{Type: "ItemView", Label: "secretName"}, "label secretName of ItemView is secret"},
		{"a hidden label", nil, ReferenceDef{Type: "ItemView", Label: "hiddenName"}, "label hiddenName of ItemView is @uiHidden"},
		{"no options", func(s *Schema) { s.OperationSets[0].Operations[0].HTTPMethod = "POST" }, ReferenceDef{Type: "ItemView"},
			"no operation of catalog lists ItemView (a GET returning ItemView[] with no path argument and no required one, that an end user may call); declare one or name it with options"},
		{"no eligible options", nil, ReferenceDef{Type: "NoteView"},
			"no operation of catalog lists NoteView (a GET returning NoteView[] with no path argument and no required one, that an end user may call)"},
		{"two options", func(s *Schema) {
			s.OperationSets = append(s.OperationSets, &OperationSet{Name: "Shop", Operations: []*FieldDef{
				{Name: "browse", HTTPMethod: "GET", TypeRef: TypeRef{Name: "ItemView", IsArray: true}}}})
		}, ReferenceDef{Type: "ItemView"}, "ItemView is listed by Catalog.listItems and Shop.browse; name one with options"},
		{"unknown options", nil, ReferenceDef{Type: "ItemView", Options: "browse"}, "options browse is not an operation of catalog"},
		{"unknown set", nil, ReferenceDef{Type: "ItemView", Options: "Shop.listItems"}, "options Shop.listItems is not an operation of catalog"},
		{"an ambiguous name", func(s *Schema) {
			s.OperationSets = append(s.OperationSets, &OperationSet{Name: "Shop", Operations: []*FieldDef{
				{Name: "listItems", HTTPMethod: "GET", TypeRef: TypeRef{Name: "ItemView", IsArray: true}}}})
		}, ReferenceDef{Type: "ItemView", Options: "listItems"}, "options listItems names an operation of Catalog and of Shop; name one as Catalog.listItems"},
		{"options of one item", nil, ReferenceDef{Type: "ItemView", Options: "getItem"}, "options getItem does not list ItemView: it is GET items/{itemId} returning ItemView"},
		{"options of another type", nil, ReferenceDef{Type: "ItemView", Options: "searchNotes"}, "options searchNotes does not list ItemView: it is GET notes returning NoteView[]"},
		{"options of a POST", nil, ReferenceDef{Type: "ItemView", Options: "placeItem"}, "options placeItem does not list ItemView: it is POST items returning ItemView"},
		{"options of lists of lists", nil, ReferenceDef{Type: "ItemView", Options: "itemGrid"}, "options itemGrid does not list ItemView: it is GET grid returning ItemView[][]"},
		{"options with a path argument", nil, ReferenceDef{Type: "NoteView", Options: "listNotes"}, "options listNotes has the path argument itemId"},
		{"options with a required argument", nil, ReferenceDef{Type: "NoteView", Options: "searchNotes"}, "options searchNotes has the required argument q"},
		{"options by hand", nil, ReferenceDef{Type: "NoteView", Options: "exportNotes"}, "options exportNotes is @manualRouteRegistration"},
		{"options for a webhook", nil, ReferenceDef{Type: "NoteView", Options: "notesFromHook"}, "options notesFromHook is a @webhook"},
		{"options for services", nil, ReferenceDef{Type: "NoteView", Options: "Catalog.reindexNotes"}, "options Catalog.reindexNotes admits only service callers (@requireService)"},
		{"options for services by the set", func(s *Schema) {
			s.OperationSets[0].ServiceCallers = &ServiceCallers{Mode: ServiceCallersRequire}
		}, ReferenceDef{Type: "ItemView", Options: "listItems"}, "options listItems admits only service callers (@requireService)"},
		{"two lookups", func(s *Schema) {
			op := s.OperationSets[0].Operations[0]
			op.Arguments = append(op.Arguments, &ArgumentDef{Name: "only", TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}, IsQuery: true, References: &ReferenceDef{Type: "ItemView", Value: "id"}})
		}, ReferenceDef{Type: "ItemView"}, "options Catalog.listItems has two lookups, ids and only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := catalogSchema()
			if tc.edit != nil {
				tc.edit(schema)
			}
			ref := tc.ref
			_, err := ResolveReference(schema, nil, &ref)
			if err == nil || !strings.HasPrefix(err.Error(), tc.wants) {
				t.Fatalf("ResolveReference = %v, want %q", err, tc.wants)
			}
		})
	}
}

// A lookup is a list query argument that is optional and refers to the
// same target by the same value; any other argument is none. (A required
// one makes the operation no options at all.)
func TestResolveReferenceLookup(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  ArgumentDef
		want bool
	}{
		{"the lookup", ArgumentDef{IsQuery: true, TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}, References: &ReferenceDef{Type: "ItemView", Value: "id"}}, true},
		{"one value", ArgumentDef{IsQuery: true, TypeRef: TypeRef{Name: "Identity.UUID"}, References: &ReferenceDef{Type: "ItemView"}}, false},
		{"another value", ArgumentDef{IsQuery: true, TypeRef: TypeRef{Name: "Identity.Slug", IsArray: true}, References: &ReferenceDef{Type: "ItemView", Value: "sku"}}, false},
		{"another target", ArgumentDef{IsQuery: true, TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}, References: &ReferenceDef{Type: "NoteView"}}, false},
		{"no reference", ArgumentDef{IsQuery: true, TypeRef: TypeRef{Name: "Identity.UUID", IsArray: true}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := catalogSchema()
			op := schema.OperationSets[0].Operations[0]
			arg := tc.arg
			arg.Name = "pick"
			op.Arguments = []*ArgumentDef{&arg}
			got, err := ResolveReference(schema, nil, &ReferenceDef{Type: "ItemView"})
			if err != nil {
				t.Fatal(err)
			}
			if (got.Lookup == &arg) != tc.want {
				t.Fatalf("Lookup = %+v, want the argument: %v", got.Lookup, tc.want)
			}
		})
	}
}

func TestReferencesOf(t *testing.T) {
	schema := catalogSchema()
	schema.Types["BasketLine"] = &TypeDef{Name: "BasketLine", Role: RoleAPIInput, Fields: []*FieldDef{
		{Name: "itemId", TypeRef: TypeRef{Name: "Identity.UUID"}, References: &ReferenceDef{Type: "ItemView"}},
		{Name: "quantity", TypeRef: TypeRef{Name: "number"}},
	}}
	var holders []string
	for _, held := range ReferencesOf(schema) {
		holders = append(holders, held.Holder)
	}
	want := []string{"BasketLine.itemId", "Catalog.listItems(ids)", "Catalog.getItem(itemId)"}
	if !reflect.DeepEqual(holders, want) {
		t.Fatalf("holders = %v, want %v", holders, want)
	}
}

func TestTextValue(t *testing.T) {
	schema := catalogSchema()
	for _, tc := range []struct {
		ref  TypeRef
		want bool
	}{
		{TypeRef{Name: "string"}, true},
		{TypeRef{Name: "Identity.Slug"}, true},
		{TypeRef{Name: "string", IsArray: true}, false},
		{TypeRef{Name: "string", IsMap: true}, false},
		{TypeRef{Name: "number"}, false},
		{TypeRef{Name: "boolean"}, false},
		{TypeRef{Name: "Generic.JSON"}, false},
		{TypeRef{Name: "ItemView"}, false},
		{TypeRef{Name: "Colour"}, false},
	} {
		if got := TextValue(schema, tc.ref); got != tc.want {
			t.Errorf("TextValue(%+v) = %v, want %v", tc.ref, got, tc.want)
		}
	}
}
