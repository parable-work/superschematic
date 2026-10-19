package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// keySchema is an API, shop, whose OrderLineView is @source(shop-db.OrderLine)
// and holds productId, the key of OrderLine.product, a Relation to Product,
// with no @virtual; ProductView is @source(shop-db.Product) and
// listProducts lists it. The externals are shop-db's OrderLine and
// Product, as the TypeScript reader flattens them.
func keySchema() (*ir.Schema, Input) {
	uuid := ir.TypeRef{Name: "Identity.UUID"}
	schema := ir.NewSchema("shop", ir.SchemaKindAPI)
	schema.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	schema.Types["OrderLineView"] = &ir.TypeDef{Name: "OrderLineView", Owner: "src/orders.schema.ts", Role: ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "shop-db.OrderLine"}, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true},
			{Name: "productId", TypeRef: uuid, Required: true},
		}}
	schema.Types["ProductView"] = &ir.TypeDef{Name: "ProductView", Owner: "src/orders.schema.ts", Role: ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "shop-db.Product"}, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true},
		}}
	schema.Types["SupplierView"] = &ir.TypeDef{Name: "SupplierView", Owner: "src/orders.schema.ts", Role: ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "shop-db.Supplier"}, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true},
		}}
	schema.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "listProducts", HTTPMethod: "GET", RestPath: "products", TypeRef: ir.TypeRef{Name: "ProductView", IsArray: true}, Required: true},
	}}}
	in := Input{ExternalTypes: map[string]*ir.TypeDef{
		"shop-db.OrderLine": {Name: "OrderLine", Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true, Key: true},
			{Name: "product", TypeRef: ir.TypeRef{Name: "Product"}, Required: true, Relation: &ir.RelationDef{Type: "Product"}},
		}},
		"shop-db.Product": {Name: "Product", Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true, Key: true},
		}},
		"shop-db.Supplier": {Name: "Supplier", Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: uuid, Required: true, Key: true},
		}},
	}}
	return schema, in
}

func sourceDiagnostics(schema *ir.Schema, in Input) (errs, warnings []string) {
	r := &Result{}
	checkSourceProjections(schema, in, r)
	for _, d := range r.Warnings {
		warnings = append(warnings, d.Error())
	}
	return errorStrings(r), warnings
}

// A relation's key needs no @virtual: it reads the relation's foreign key,
// is not recorded as virtual, and with one @source(Product) type it refers
// to that type (D61, amended). A @virtual key is still a key, and stays
// virtual.
func TestARelationsKeyNeedsNoVirtual(t *testing.T) {
	schema, in := keySchema()
	errs, warnings := sourceDiagnostics(schema, in)
	if len(errs) != 0 || len(warnings) != 0 {
		t.Fatalf("errors = %q, warnings = %q", errs, warnings)
	}
	line := schema.Types["OrderLineView"]
	if len(line.Source.Virtual) != 0 || !slices.Equal(line.Source.OmittedFromSource, []string{"product"}) {
		t.Fatalf("source = %+v, want no virtual field and product omitted", line.Source)
	}
	deps := map[string]*ir.Schema{"shop-db": {Name: "shop-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"OrderLine": in.ExternalTypes["shop-db.OrderLine"], "Product": in.ExternalTypes["shop-db.Product"],
	}}}
	if ref, derived := ir.FieldReference(schema, deps, line, line.Fields[1]); !derived || ref.Type != "ProductView" {
		t.Fatalf("FieldReference = %+v, %v; want ProductView, derived", ref, derived)
	}

	line.Fields[1].Virtual = true
	if errs, warnings := sourceDiagnostics(schema, in); len(errs) != 0 || len(warnings) != 0 {
		t.Fatalf("a @virtual key: errors = %q, warnings = %q", errs, warnings)
	}
	if !slices.Equal(line.Source.Virtual, []string{"productId"}) {
		t.Fatalf("virtual = %v, want [productId]", line.Source.Virtual)
	}

	// A field named for no relation is no key, and still needs @virtual.
	line.Fields[1] = &ir.FieldDef{Name: "supplierId", TypeRef: ir.TypeRef{Name: "Identity.UUID"}}
	if errs, _ := sourceDiagnostics(schema, in); !slices.Equal(errs, []string{
		"src/orders.schema.ts: OrderLineView.supplierId is not in source type shop-db.OrderLine and is not marked @virtual",
	}) {
		t.Fatalf("no relation: %q", errs)
	}
}

// Each refusal of a relation's key, and the warning when two types are
// @source of the related table, in the loader's words (D61, amended).
func TestARelationsKeyRefusalsAndWarning(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*ir.Schema, Input)
		errs     []string
		warnings []string
	}{
		{"a key of another type", func(s *ir.Schema, _ Input) {
			s.Types["OrderLineView"].Fields[1].TypeRef = ir.TypeRef{Name: "string"}
		}, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product, a Relation to Product, whose id is Identity.UUID, not string"}, nil},
		{"a @virtual key of another type", func(s *ir.Schema, _ Input) {
			key := s.Types["OrderLineView"].Fields[1]
			key.TypeRef, key.Virtual = ir.TypeRef{Name: "string"}, true
		}, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product, a Relation to Product, whose id is Identity.UUID, not string"}, nil},
		{"a list key", func(s *ir.Schema, _ Input) {
			s.Types["OrderLineView"].Fields[1].TypeRef.IsArray = true
		}, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product, a Relation to Product; a key is one value, not a list or a map"}, nil},
		{"a map key", func(s *ir.Schema, _ Input) {
			s.Types["OrderLineView"].Fields[1].TypeRef.IsMap = true
		}, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product, a Relation to Product; a key is one value, not a list or a map"}, nil},
		{"a RefersTo to another table's type", func(s *ir.Schema, _ Input) {
			s.Types["OrderLineView"].Fields[1].References = &ir.ReferenceDef{Type: "SupplierView"}
		}, []string{"src/orders.schema.ts: OrderLineView.productId: RefersTo names SupplierView, which is @source(shop-db.Supplier), but the field reads the key of shop-db.OrderLine.product, a Relation to Product"}, nil},
		{"two types sourced from the related table", func(s *ir.Schema, _ Input) {
			s.Types["ProductCard"] = &ir.TypeDef{Name: "ProductCard", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}}
		}, nil, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product and refers to nothing: ProductCard and ProductView are both @source(shop-db.Product); name one with RefersTo"}},
		{"three types sourced from the related table", func(s *ir.Schema, _ Input) {
			s.Types["ProductCard"] = &ir.TypeDef{Name: "ProductCard", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}}
			s.Types["ProductRow"] = &ir.TypeDef{Name: "ProductRow", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}}
		}, nil, []string{"src/orders.schema.ts: OrderLineView.productId reads the key of shop-db.OrderLine.product and refers to nothing: ProductCard, ProductRow and ProductView are all @source(shop-db.Product); name one with RefersTo"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, in := keySchema()
			tc.edit(schema, in)
			errs, warnings := sourceDiagnostics(schema, in)
			if !slices.Equal(errs, tc.errs) || !slices.Equal(warnings, tc.warnings) {
				t.Fatalf("errors =\n%q\nwant\n%q\nwarnings =\n%q\nwant\n%q", errs, tc.errs, warnings, tc.warnings)
			}
		})
	}
}

// What the rules leave alone: a RefersTo that wins, to a type of the
// related table or to a type with no @source; two sourced types when the
// key names one; a related type that is not at hand, whose id the check
// cannot read; and a General schema, which holds no reference.
func TestARelationsKeyAccepted(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Schema, Input)
	}{
		{"a RefersTo to a type of the related table", func(s *ir.Schema, _ Input) {
			s.Types["ProductCard"] = &ir.TypeDef{Name: "ProductCard", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}}
			s.Types["OrderLineView"].Fields[1].References = &ir.ReferenceDef{Type: "ProductCard"}
		}},
		{"a RefersTo to a type with no @source", func(s *ir.Schema, _ Input) {
			s.Types["Pick"] = &ir.TypeDef{Name: "Pick", Role: ir.RoleAPIView}
			s.Types["OrderLineView"].Fields[1].References = &ir.ReferenceDef{Type: "Pick"}
		}},
		{"a related type not at hand", func(s *ir.Schema, in Input) {
			delete(in.ExternalTypes, "shop-db.Product")
			delete(s.Types, "ProductView")
			s.OperationSets = nil
			s.Types["OrderLineView"].Fields[1].TypeRef = ir.TypeRef{Name: "string"}
		}},
		{"a General schema", func(s *ir.Schema, _ Input) {
			s.Kind = ir.SchemaKindGeneral
			s.Types["ProductCard"] = &ir.TypeDef{Name: "ProductCard", Role: ir.RoleAPIView, Source: &ir.SourceRef{Target: "shop-db.Product"}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, in := keySchema()
			tc.edit(schema, in)
			if errs, warnings := sourceDiagnostics(schema, in); len(errs) != 0 || len(warnings) != 0 {
				t.Fatalf("errors = %q, warnings = %q", errs, warnings)
			}
		})
	}
}
