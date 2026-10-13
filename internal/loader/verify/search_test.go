package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func searchSchema(args ...*ir.ArgumentDef) *ir.Schema {
	s := ir.NewSchema("catalog", ir.SchemaKindAPI)
	s.Scalars["Identity.Slug"] = &ir.ScalarDef{Name: "Identity.Slug", LanguagePrimitive: ir.LanguageString}
	s.Scalars["Generic.Int64"] = &ir.ScalarDef{Name: "Generic.Int64", LanguagePrimitive: ir.LanguageNumber}
	s.Scalars["Generic.JSON"] = &ir.ScalarDef{Name: "Generic.JSON", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"json_schema": "any"}}
	s.Enums["Colour"] = &ir.EnumDef{Name: "Colour"}
	s.OperationSets = []*ir.OperationSet{{Name: "Catalog", Operations: []*ir.FieldDef{
		{Name: "listItems", HTTPMethod: "GET", RestPath: "items", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Arguments: args},
	}}}
	return s
}

func searchErrors(schema *ir.Schema) []string {
	r := &Result{}
	checkSearches(schema, r)
	return errorStrings(r)
}

func TestSearchVerifies(t *testing.T) {
	for _, arg := range []*ir.ArgumentDef{
		{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Search: true},
		{Name: "q", TypeRef: ir.TypeRef{Name: "Identity.Slug"}, IsQuery: true, Search: true},
	} {
		schema := searchSchema(arg, &ir.ArgumentDef{Name: "tag", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true})
		if got := searchErrors(schema); len(got) != 0 {
			t.Errorf("%s: errors = %q", arg.TypeRef.Name, got)
		}
	}
}

func TestSearchRefused(t *testing.T) {
	ten := "ten"
	q := func(edit func(*ir.ArgumentDef)) *ir.ArgumentDef {
		arg := &ir.ArgumentDef{Name: "q", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true, Search: true}
		edit(arg)
		return arg
	}
	for _, tc := range []struct {
		name   string
		args   []*ir.ArgumentDef
		method string
		want   []string
	}{
		{"not a query parameter", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.IsQuery = false })}, "GET",
			[]string{"Catalog.listItems: search argument q must be a query parameter of a GET operation"}},
		{"on a POST", []*ir.ArgumentDef{q(func(*ir.ArgumentDef) {})}, "POST",
			[]string{"Catalog.listItems: search argument q must be a query parameter of a GET operation"}},
		{"required", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.Required = true })}, "GET",
			[]string{"Catalog.listItems: search argument q must be optional"}},
		{"a default", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.Default = &ten })}, "GET",
			[]string{"Catalog.listItems: search argument q has a default; a search left out searches nothing"}},
		{"a list", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.IsArray = true })}, "GET",
			[]string{"Catalog.listItems: search argument q holds string[]; a search is text"}},
		{"a map", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.IsMap = true })}, "GET",
			[]string{"Catalog.listItems: search argument q holds a map of string; a search is text"}},
		{"a number", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.Name = "number" })}, "GET",
			[]string{"Catalog.listItems: search argument q holds number; a search is text"}},
		{"a boolean", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.Name = "boolean" })}, "GET",
			[]string{"Catalog.listItems: search argument q holds boolean; a search is text"}},
		{"an enum", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.Name = "Colour" })}, "GET",
			[]string{"Catalog.listItems: search argument q holds Colour; a search is text"}},
		{"a number scalar", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.Name = "Generic.Int64" })}, "GET",
			[]string{"Catalog.listItems: search argument q holds Generic.Int64; a search is text"}},
		{"a JSON scalar", []*ir.ArgumentDef{q(func(a *ir.ArgumentDef) { a.TypeRef.Name = "Generic.JSON" })}, "GET",
			[]string{"Catalog.listItems: search argument q holds Generic.JSON; a search is text"}},
		{"two", []*ir.ArgumentDef{q(func(*ir.ArgumentDef) {}), q(func(a *ir.ArgumentDef) { a.Name = "text" })}, "GET",
			[]string{"Catalog.listItems: has two search arguments, q and text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := searchSchema(tc.args...)
			schema.OperationSets[0].Operations[0].HTTPMethod = tc.method
			if got := searchErrors(schema); !slices.Equal(got, tc.want) {
				t.Errorf("errors = %q, want %q", got, tc.want)
			}
		})
	}
}
