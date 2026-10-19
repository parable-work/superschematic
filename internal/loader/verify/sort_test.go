package verify

import (
	"reflect"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// sortSchema is an API whose Orders.listOrders pages OrderView, sorted by
// placedAt and totalCents, placedAt by default. edit changes the sort
// argument, the page and the item before the check runs.
func sortSchema(edit func(op *ir.FieldDef, sort *ir.ArgumentDef, item *ir.TypeDef)) *ir.Schema {
	s := ir.NewSchema("orders", ir.SchemaKindAPI)
	s.Scalars["Identity.UUID"] = &ir.ScalarDef{Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString}
	s.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString}
	s.Scalars["Generic.Int64"] = &ir.ScalarDef{Name: "Generic.Int64", LanguagePrimitive: ir.LanguageNumber}
	s.Enums["OrderStatus"] = &ir.EnumDef{Name: "OrderStatus"}
	item := &ir.TypeDef{Name: "OrderView", Role: ir.RoleAPIView, Fields: []*ir.FieldDef{
		{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		{Name: "placedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
		{Name: "totalCents", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true},
		{Name: "number", TypeRef: ir.TypeRef{Name: "integer"}, Required: true},
		{Name: "note", TypeRef: ir.TypeRef{Name: "string"}},
		{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}, Required: true},
		{Name: "gift", TypeRef: ir.TypeRef{Name: "boolean"}, Required: true},
		{Name: "tags", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
		{Name: "code", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
	}}
	s.Types["OrderView"] = item
	sort := &ir.ArgumentDef{Name: "sort", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Sort: []string{"placedAt", "totalCents"}}
	op := &ir.FieldDef{
		Name: "listOrders", HTTPMethod: "GET", RestPath: "orders", TypeRef: ir.TypeRef{Name: "OrderView", IsArray: true}, Required: true,
		Arguments: []*ir.ArgumentDef{{Name: "status", TypeRef: ir.TypeRef{Name: "OrderStatus"}, IsQuery: true}, sort},
		Page:      &ir.OperationPage{DefaultLimit: 20, MaxLimit: 100, DefaultSort: "placedAt"},
	}
	s.OperationSets = []*ir.OperationSet{{Name: "Orders", Operations: []*ir.FieldDef{op}}}
	if edit != nil {
		edit(op, sort, item)
	}
	return s
}

func sortFindings(schema *ir.Schema, in Input) (errs, warnings []string) {
	r := &Result{}
	checkSorts(schema, in, r)
	for _, d := range r.Warnings {
		warnings = append(warnings, d.Error())
	}
	return errorStrings(r), warnings
}

func TestSortVerifies(t *testing.T) {
	for name, edit := range map[string]func(*ir.FieldDef, *ir.ArgumentDef, *ir.TypeDef){
		"as declared":           nil,
		"descending by default": func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) { op.Page.DefaultSort = "-totalCents" },
		"by an integer and a text": func(op *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) {
			s.Sort = []string{"number", "id"}
			op.Page.DefaultSort = "-id"
		},
		"an unsorted page": func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) {
			op.Arguments = op.Arguments[:1]
			op.Page.DefaultSort = ""
		},
		"an operation without a page": func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) {
			op.Arguments = op.Arguments[:1]
			op.Page = nil
		},
	} {
		errs, warnings := sortFindings(sortSchema(edit), Input{})
		if len(errs) != 0 || len(warnings) != 0 {
			t.Errorf("%s: errors %q, warnings %q", name, errs, warnings)
		}
	}
}

// TestSortsNeedAPagedList holds a SortBy argument to its page, its item and
// the defaultSort, one case per message, in every form (D60, amended).
func TestSortsNeedAPagedList(t *testing.T) {
	placed := "placedAt"
	for _, tc := range []struct {
		name string
		edit func(op *ir.FieldDef, sort *ir.ArgumentDef, item *ir.TypeDef)
		want []string
	}{
		{"no page", func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) { op.Page = nil },
			[]string{"Orders.listOrders: sort argument sort orders a page, and the operation has no @paginated"}},
		{"two", func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) {
			op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: "order", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Sort: []string{"number"}})
		}, []string{"Orders.listOrders: has two sort arguments, sort and order"}},
		{"not a query parameter", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.IsQuery = false },
			[]string{"Orders.listOrders: sort argument sort must be an optional query parameter"}},
		{"required", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Required = true },
			[]string{"Orders.listOrders: sort argument sort must be an optional query parameter"}},
		{"a default", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Default = &placed },
			[]string{"Orders.listOrders: sort argument sort has a default; @paginated's defaultSort is the order of a request that sends none"}},
		{"a number", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.TypeRef = ir.TypeRef{Name: "number"} },
			[]string{"Orders.listOrders: sort argument sort holds number; a sort's value is text"}},
		{"a list of text", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.TypeRef.IsArray = true },
			[]string{"Orders.listOrders: sort argument sort holds string[]; a sort's value is text"}},
		{"no field", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{} },
			[]string{"Orders.listOrders: sort argument sort names no field"}},
		{"a field twice", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) {
			s.Sort = []string{"placedAt", "totalCents", "placedAt"}
		},
			[]string{"Orders.listOrders: sort argument sort names placedAt twice"}},
		{"a field the item does not declare", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "shippedAt"} },
			[]string{"Orders.listOrders: sort argument sort names shippedAt, which OrderView does not declare"}},
		{"a list", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "tags"} },
			[]string{"Orders.listOrders: sort argument sort names OrderView.tags, which holds string[]; a page is sorted by one text, number or time"}},
		{"an enum", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "status"} },
			[]string{"Orders.listOrders: sort argument sort names OrderView.status, which holds OrderStatus; a page is sorted by one text, number or time"}},
		{"a boolean", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "gift"} },
			[]string{"Orders.listOrders: sort argument sort names OrderView.gift, which holds boolean; a page is sorted by one text, number or time"}},
		{"an object", func(_ *ir.FieldDef, s *ir.ArgumentDef, item *ir.TypeDef) {
			item.Fields = append(item.Fields, &ir.FieldDef{Name: "self", TypeRef: ir.TypeRef{Name: "OrderView"}, Required: true})
			s.Sort = []string{"placedAt", "self"}
		}, []string{"Orders.listOrders: sort argument sort names OrderView.self, which holds OrderView; a page is sorted by one text, number or time"}},
		{"a map", func(_ *ir.FieldDef, s *ir.ArgumentDef, item *ir.TypeDef) {
			item.Fields = append(item.Fields, &ir.FieldDef{Name: "counts", TypeRef: ir.TypeRef{Name: "integer", IsMap: true}, Required: true})
			s.Sort = []string{"placedAt", "counts"}
		}, []string{"Orders.listOrders: sort argument sort names OrderView.counts, which holds a map of integer; a page is sorted by one text, number or time"}},
		{"a nullable field", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "note"} },
			[]string{"Orders.listOrders: sort argument sort names OrderView.note, which is Nullable; a page's order has no place for a null"}},
		{"a secret field", func(_ *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) { s.Sort = []string{"placedAt", "code"} },
			[]string{"Orders.listOrders: sort argument sort names OrderView.code, which is Secret; a page's order would show it"}},
		{"an item without an id", func(_ *ir.FieldDef, _ *ir.ArgumentDef, item *ir.TypeDef) { item.Fields = item.Fields[1:] },
			[]string{"Orders.listOrders: a sort breaks ties by id, and OrderView declares no id"}},
		{"a list of ids", func(_ *ir.FieldDef, _ *ir.ArgumentDef, item *ir.TypeDef) { item.Fields[0].TypeRef.IsArray = true },
			[]string{"Orders.listOrders: a sort breaks ties by id, and OrderView.id holds Identity.UUID[]; it must be one required value"}},
		{"a nullable id", func(_ *ir.FieldDef, _ *ir.ArgumentDef, item *ir.TypeDef) { item.Fields[0].Required = false },
			[]string{"Orders.listOrders: a sort breaks ties by id, and OrderView.id holds Nullable<Identity.UUID>; it must be one required value"}},
		{"a list of a primitive", func(op *ir.FieldDef, s *ir.ArgumentDef, _ *ir.TypeDef) {
			op.TypeRef = ir.TypeRef{Name: "string", IsArray: true}
			s.Sort = []string{"placedAt"}
		}, []string{"Orders.listOrders: sort argument sort names placedAt, which string does not declare"}},
		{"no defaultSort", func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) { op.Page.DefaultSort = "" },
			[]string{"Orders.listOrders: sort argument sort needs @paginated's defaultSort, the order of a request that sends none"}},
		{"a defaultSort without a sort", func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) { op.Arguments = op.Arguments[:1] },
			[]string{"Orders.listOrders: @paginated defaultSort placedAt orders a sort argument, and the operation declares none (SortBy)"}},
		{"a defaultSort of another field", func(op *ir.FieldDef, _ *ir.ArgumentDef, _ *ir.TypeDef) { op.Page.DefaultSort = "-number" },
			[]string{"Orders.listOrders: @paginated defaultSort -number is not one of sort's values: placedAt, -placedAt, totalCents, -totalCents"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs, warnings := sortFindings(sortSchema(tc.edit), Input{})
			if !reflect.DeepEqual(errs, tc.want) || len(warnings) != 0 {
				t.Fatalf("errors = %q, warnings %q\nwant %q", errs, warnings, tc.want)
			}
		})
	}
}

// TestASortByAVirtualFieldWarns: a sort field that is @virtual on an item
// sourced from a table is a warning, since no column of the table holds
// it, and nothing on an item without @source, which an implementation
// sorts itself (D60, amended).
func TestASortByAVirtualFieldWarns(t *testing.T) {
	virtual := func(_ *ir.FieldDef, _ *ir.ArgumentDef, item *ir.TypeDef) {
		item.Fields[2].Virtual = true
		item.Source = &ir.SourceRef{Target: "shop-db.Order"}
	}
	errs, warnings := sortFindings(sortSchema(virtual), Input{})
	want := []string{"Orders.listOrders: sort argument sort names OrderView.totalCents, which is @virtual: no column of shop-db.Order holds it, so the table cannot order by it"}
	if len(errs) != 0 || !reflect.DeepEqual(warnings, want) {
		t.Fatalf("errors %q, warnings %q; want the warning %q", errs, warnings, want)
	}
	errs, warnings = sortFindings(sortSchema(func(op *ir.FieldDef, s *ir.ArgumentDef, item *ir.TypeDef) {
		virtual(op, s, item)
		item.Source = nil
	}), Input{})
	if len(errs) != 0 || len(warnings) != 0 {
		t.Fatalf("an item without @source: errors %q, warnings %q", errs, warnings)
	}
}

// A sort of a dependency's item reads the item from the dependency (D66),
// as ir.LookupScopes finds it; an imported item the loader did not read
// is not checked field by field.
func TestASortOfADependencysItem(t *testing.T) {
	schema := sortSchema(nil)
	item := schema.Types["OrderView"]
	delete(schema.Types, "OrderView")
	schema.Imports = []ir.Import{{Package: "@schemas/orders-types", Types: []string{"OrderView"}}}
	dep := ir.NewSchema("orders-types", ir.SchemaKindGeneral)
	dep.Types["OrderView"] = item
	dep.Scalars = schema.Scalars

	if errs, _ := sortFindings(schema, Input{DependencySchemas: map[string]*ir.Schema{"orders-types": dep}}); len(errs) != 0 {
		t.Fatalf("a dependency's item: %q", errs)
	}
	schema.OperationSets[0].Operations[0].Arguments[1].Sort = []string{"placedAt", "status"}
	errs, _ := sortFindings(schema, Input{DependencySchemas: map[string]*ir.Schema{"orders-types": dep}})
	if want := []string{"Orders.listOrders: sort argument sort names OrderView.status, which holds OrderStatus; a page is sorted by one text, number or time"}; !reflect.DeepEqual(errs, want) {
		t.Fatalf("a dependency's item's enum: %q, want %q", errs, want)
	}
	if errs, _ := sortFindings(schema, Input{}); len(errs) != 0 {
		t.Fatalf("an item the loader did not read: %q", errs)
	}
}
