package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func validOperationDocs() OperationDocs {
	return OperationDocs{
		Title:         "Create a return",
		Description:   "Opens a return for one delivered order.",
		Capability:    "orders.returns.create",
		Lifecycle:     DocsLifecycleActive,
		Visibility:    DocsVisibilityPreview,
		MappingStatus: DocsMappingStatusMapped,
		Sunset:        "2026-12-31",
	}
}

func TestValidateOperationDocs(t *testing.T) {
	valid := validOperationDocs()
	if err := ValidateOperationDocs(&valid); err != nil {
		t.Fatalf("valid docs: %v", err)
	}
	if err := ValidateOperationDocs(nil); err != nil {
		t.Fatalf("nil docs: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*OperationDocs)
		want   string
	}{
		{name: "title", mutate: func(d *OperationDocs) { d.Title = "" }, want: "title must be non-empty"},
		{name: "padded title", mutate: func(d *OperationDocs) { d.Title = " Create a return" }, want: "title must not contain surrounding whitespace"},
		{name: "description", mutate: func(d *OperationDocs) { d.Description = " " }, want: "description must be non-empty"},
		{name: "capability", mutate: func(d *OperationDocs) { d.Capability = "CreateReturn" }, want: "dot-separated lowercase segments"},
		{name: "one segment", mutate: func(d *OperationDocs) { d.Capability = "returns" }, want: "dot-separated lowercase segments"},
		{name: "lifecycle", mutate: func(d *OperationDocs) { d.Lifecycle = "legacy" }, want: "lifecycle"},
		{name: "visibility", mutate: func(d *OperationDocs) { d.Visibility = "hidden" }, want: "public, internal, or preview"},
		{name: "mapping", mutate: func(d *OperationDocs) { d.MappingStatus = "unknown" }, want: "mappingStatus"},
		{name: "replacement", mutate: func(d *OperationDocs) { d.Replacement = "  " }, want: "replacement must be non-empty"},
		{name: "sunset", mutate: func(d *OperationDocs) { d.Sunset = "December 31" }, want: "YYYY-MM-DD"},
		{name: "blank audience", mutate: func(d *OperationDocs) { d.Audience = " " }, want: "audience must be non-empty"},
		{name: "padded audience", mutate: func(d *OperationDocs) { d.Audience = "staff " }, want: "audience must not contain surrounding whitespace"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := validOperationDocs()
			test.mutate(&candidate)
			err := ValidateOperationDocs(&candidate)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOperationDocs() error = %v, want %q", err, test.want)
			}
		})
	}
}

// TestValidateOperationDocsAcceptsAnyAudience: the core has no audience
// vocabulary. A value set is an extension's check.
func TestValidateOperationDocsAcceptsAnyAudience(t *testing.T) {
	for _, audience := range []DocsAudience{"", "staff", "partner-integrations", "Anyone at all"} {
		docs := validOperationDocs()
		docs.Audience = audience
		if err := ValidateOperationDocs(&docs); err != nil {
			t.Errorf("audience %q: %v", audience, err)
		}
	}
}

func TestSchemaValidateChecksOperationDocs(t *testing.T) {
	s := NewSchema("shop", SchemaKindAPI)
	s.Types["Order"] = &TypeDef{Name: "Order", Role: RoleAPIView, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "string"}, Required: true},
	}}
	docs := validOperationDocs()
	s.OperationSets = []*OperationSet{{Name: "OrderQueries", Operations: []*FieldDef{
		{Name: "getOrder", HTTPMethod: "GET", TypeRef: TypeRef{Name: "Order"}, Docs: &docs},
	}}}
	known := WithKnownExternals(map[string]bool{"string": true})
	if errs := s.Validate(known); len(errs) != 0 {
		t.Fatalf("valid schema: %v", errs)
	}

	docs.Capability = "Orders"
	errs := s.Validate(known)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "OrderQueries.getOrder has invalid docs: capability") {
		t.Fatalf("invalid operation docs: %v", errs)
	}

	docs.Capability = "orders.get"
	s.Types["Order"].Fields[0].Docs = &docs
	errs = s.Validate(known)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "Order.id carries operation docs") {
		t.Fatalf("docs on a data field: %v", errs)
	}
}

// TestOperationDocsMarshalsWithoutEmptyOptionals: an operation without
// @docs has no docs key, and an unset audience, replacement or sunset is
// left out.
func TestOperationDocsMarshalsWithoutEmptyOptionals(t *testing.T) {
	raw, err := json.Marshal(&FieldDef{Name: "getOrder", TypeRef: TypeRef{Name: "Order"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"docs"`) {
		t.Fatalf("operation without docs marshals a docs key: %s", raw)
	}
	docs := validOperationDocs()
	docs.Sunset = ""
	raw, err = json.Marshal(&docs)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"title":"Create a return","description":"Opens a return for one delivered order.","capability":"orders.returns.create","lifecycle":"active","visibility":"preview","mappingStatus":"mapped"}`
	if string(raw) != want {
		t.Fatalf("docs JSON = %s\nwant       %s", raw, want)
	}
}
