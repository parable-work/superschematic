package verify

import (
	"reflect"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// filterSchema is sortSchema's API with a filter on listOrders. edit
// changes the operation, its filter and the item before the check runs.
func filterSchema(fields []string, edit func(op *ir.FieldDef, item *ir.TypeDef)) *ir.Schema {
	return sortSchema(func(op *ir.FieldDef, _ *ir.ArgumentDef, item *ir.TypeDef) {
		op.Filter = &ir.OperationFilter{Argument: "filter"}
		for _, name := range fields {
			op.Filter.Fields = append(op.Filter.Fields, &ir.FilterField{Name: name})
		}
		item.Fields = append(item.Fields,
			&ir.FieldDef{Name: "dueOn", TypeRef: ir.TypeRef{Name: "Temporal.Date"}},
			&ir.FieldDef{Name: "weight", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			&ir.FieldDef{Name: "amount", TypeRef: ir.TypeRef{Name: "Generic.Decimal"}, Required: true},
			&ir.FieldDef{Name: "wait", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}, Required: true},
			&ir.FieldDef{Name: "customer", TypeRef: ir.TypeRef{Name: "CustomerView"}, Required: true},
			&ir.FieldDef{Name: "labels", TypeRef: ir.TypeRef{Name: "string", IsMap: true}, Required: true},
			&ir.FieldDef{Name: "createdBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			&ir.FieldDef{Name: "createdAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, Required: true},
			&ir.FieldDef{Name: "total", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true, Virtual: true},
			&ir.FieldDef{Name: "card", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Decrypted: true},
		)
		if edit != nil {
			edit(op, item)
		}
	})
}

func filterFindings(schema *ir.Schema, in Input) []string {
	r := &Result{}
	schema.Scalars["Temporal.Date"] = &ir.ScalarDef{Name: "Temporal.Date", LanguagePrimitive: ir.LanguageString}
	schema.Scalars["Generic.Decimal"] = &ir.ScalarDef{Name: "Generic.Decimal", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "NUMERIC"}}
	schema.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	schema.Types["CustomerView"] = &ir.TypeDef{Name: "CustomerView", Role: ir.RoleAPIView}
	checkFilters(schema, in, r)
	return errorStrings(r)
}

// TestFilterFillsEachFieldsKindAndOperators: the loader gives each field
// of a FilterBy the kind of its item's field, the operators of that kind,
// isNull on a field that may be null, and the field's type (D106).
func TestFilterFillsEachFieldsKindAndOperators(t *testing.T) {
	ordered := []string{"eq", "in", "gt", "gte", "lt", "lte"}
	want := []*ir.FilterField{
		{Name: "id", Kind: "uuid", Operators: []string{"eq", "in"}, TypeRef: &ir.TypeRef{Name: "Identity.UUID"}},
		{Name: "placedAt", Kind: "datetime", Operators: ordered, TypeRef: &ir.TypeRef{Name: "Temporal.DateTime"}},
		{Name: "totalCents", Kind: "int", Operators: ordered, TypeRef: &ir.TypeRef{Name: "Generic.Int64"}},
		{Name: "note", Kind: "string", Operators: []string{"eq", "in", "contains", "isNull"}, TypeRef: &ir.TypeRef{Name: "string"}},
		{Name: "status", Kind: "enum", Operators: []string{"eq", "in"}, TypeRef: &ir.TypeRef{Name: "OrderStatus"}},
		{Name: "gift", Kind: "bool", Operators: []string{"eq", "in"}, TypeRef: &ir.TypeRef{Name: "boolean"}},
		{Name: "dueOn", Kind: "date", Operators: append(append([]string{}, ordered...), "isNull"), TypeRef: &ir.TypeRef{Name: "Temporal.Date"}},
		{Name: "weight", Kind: "float", Operators: ordered, TypeRef: &ir.TypeRef{Name: "number"}},
		{Name: "amount", Kind: "decimal", Operators: ordered, TypeRef: &ir.TypeRef{Name: "Generic.Decimal"}},
		{Name: "createdAt", Kind: "datetime", Operators: ordered, TypeRef: &ir.TypeRef{Name: "Temporal.DateTime"}},
	}
	names := make([]string, len(want))
	for i, field := range want {
		names[i] = field.Name
	}
	schema := filterSchema(names, nil)
	if errs := filterFindings(schema, Input{}); len(errs) != 0 {
		t.Fatalf("errors %q", errs)
	}
	got := schema.OperationSets[0].Operations[0].Filter.Fields
	if !reflect.DeepEqual(got, want) {
		for i := range got {
			if i < len(want) && !reflect.DeepEqual(got[i], want[i]) {
				t.Errorf("field %s = %+v, want %+v", want[i].Name, *got[i], *want[i])
			}
		}
		t.Fatalf("filter fields differ")
	}

	// A hand-written list filters by a field its implementation fills, and
	// by who created a row; a list over a table's view cannot (below).
	schema = filterSchema([]string{"total", "createdBy"}, nil)
	if errs := filterFindings(schema, Input{}); len(errs) != 0 {
		t.Fatalf("an item that is no @source view: errors %q", errs)
	}
}

// TestFiltersAreHeldToTheirListAndItem holds a FilterBy to its operation
// and its item, one case per message, in every form (D106).
func TestFiltersAreHeldToTheirListAndItem(t *testing.T) {
	sourced := func(_ *ir.FieldDef, item *ir.TypeDef) { item.Source = &ir.SourceRef{Target: "shop-db.Order"} }
	for _, test := range []struct {
		name   string
		fields []string
		edit   func(op *ir.FieldDef, item *ir.TypeDef)
		want   []string
	}{
		{"no field", nil, nil, []string{"Orders.listOrders: filter argument filter names no field"}},
		{"a POST", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.HTTPMethod = "POST" },
			[]string{"Orders.listOrders: filter argument filter filters a list, and the operation is not a GET whose result is a list"}},
		{"one item", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.TypeRef.IsArray = false },
			[]string{"Orders.listOrders: filter argument filter filters a list, and the operation is not a GET whose result is a list"}},
		{"another argument's name", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.Filter.Argument = "status" },
			[]string{"Orders.listOrders: filter argument status has the name of another argument"}},
		{"an argument named filter", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) {
			op.Filter.Argument = "where"
			op.Arguments[0].Name = "filter"
		}, []string{"Orders.listOrders: argument filter has the name of the query parameter the filter where travels under, filter[field]; give it another name"}},
		{"a page's parameter", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.Filter.Argument = "page" },
			[]string{"Orders.listOrders: filter argument page has the name of a page's parameter (cursor, limit, page)"}},
		{"a field twice", []string{"status", "status"}, nil, []string{"Orders.listOrders: filter argument filter names status twice"}},
		{"an undeclared field", []string{"colour"}, nil, []string{"Orders.listOrders: filter argument filter names colour, which OrderView does not declare"}},
		{"a list", []string{"tags"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.tags, which holds string[]; a filter compares one value"}},
		{"a map", []string{"labels"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.labels, which holds a map of string; a filter compares one value"}},
		{"a secret", []string{"code"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.code, which is Secret; a filter would show it"}},
		{"a decrypted field", []string{"card"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.card, which is @decrypted: its column holds ciphertext, which no filter compares"}},
		{"an object", []string{"customer"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.customer, which holds CustomerView; a filter compares one text, number, time or boolean"}},
		{"a duration", []string{"wait"}, nil, []string{"Orders.listOrders: filter argument filter names OrderView.wait, which holds Temporal.Duration; a filter compares one text, number, time or boolean"}},
		{"a virtual field of a view", []string{"total"}, sourced,
			[]string{"Orders.listOrders: filter argument filter names OrderView.total, which is @virtual: no column of shop-db.Order holds it, so the table cannot filter by it"}},
		{"an audit field of a view", []string{"createdBy"}, sourced,
			[]string{"Orders.listOrders: filter argument filter names OrderView.createdBy, an audit field the repositories keep no filter for"}},
		{"another kind written", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.Filter.Fields[0].Kind = "string" },
			[]string{"Orders.listOrders: filter argument filter gives status the kind string, and OrderView.status is enum; leave the kind out, the loader fills it"}},
		{"other operators written", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.Filter.Fields[0].Operators = []string{"eq", "contains"} },
			[]string{"Orders.listOrders: filter argument filter gives status the operators eq, contains, and a field of kind enum takes eq, in; leave them out, the loader fills them"}},
		{"another type written", []string{"status"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.Filter.Fields[0].TypeRef = &ir.TypeRef{Name: "string"} },
			[]string{"Orders.listOrders: filter argument filter gives status the type string, and OrderView.status holds OrderStatus; leave the type out, the loader fills it"}},
		{"an item that is no object", []string{"id"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.TypeRef.Name = "OrderStatus" },
			[]string{"Orders.listOrders: filter argument filter names id, which OrderStatus does not declare"}},
	} {
		if got := filterFindings(filterSchema(test.fields, test.edit), Input{}); !reflect.DeepEqual(got, test.want) {
			t.Errorf("%s:\n got %q\nwant %q", test.name, got, test.want)
		}
	}

	// A view's createdAt filters, as its column does.
	if errs := filterFindings(filterSchema([]string{"createdAt"}, sourced), Input{}); len(errs) != 0 {
		t.Errorf("a view's createdAt: errors %q", errs)
	}

	// An item of a dependency the loader did not read cannot be held to.
	schema := filterSchema([]string{"id"}, func(op *ir.FieldDef, _ *ir.TypeDef) { op.TypeRef.Name = "CatalogItem" })
	schema.Imports = []ir.Import{{Package: "@acme/catalog-api", Types: []string{"CatalogItem"}}}
	want := []string{"Orders.listOrders: filter argument filter filters CatalogItem, which the loader could not read from the service that declares it; a filter's fields are held to their item"}
	if got := filterFindings(schema, Input{}); !reflect.DeepEqual(got, want) {
		t.Errorf("an unread item:\n got %q\nwant %q", got, want)
	}
}
