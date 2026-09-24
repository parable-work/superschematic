package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateOperationMCP(t *testing.T) {
	if err := ValidateOperationMCP(nil); err != nil {
		t.Fatalf("nil: %v", err)
	}
	tests := []struct {
		name string
		mcp  OperationMCP
		want string
	}{
		{name: "visible", mcp: OperationMCP{Handle: "get_order"}},
		{name: "visible with meta", mcp: OperationMCP{Handle: "get_order", Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://orders/detail"}}}},
		{name: "hidden", mcp: OperationMCP{Hidden: true, HiddenReason: "Browser upload only."}},
		{name: "missing handle", mcp: OperationMCP{}, want: "handle must be non-empty"},
		{name: "camel handle", mcp: OperationMCP{Handle: "getOrder"}, want: "lowercase snake_case"},
		{name: "leading digit", mcp: OperationMCP{Handle: "1_order"}, want: "lowercase snake_case"},
		{name: "double underscore", mcp: OperationMCP{Handle: "get__order"}, want: "lowercase snake_case"},
		{name: "long handle", mcp: OperationMCP{Handle: strings.Repeat("a", MCPHandleMaxLength+1)}, want: "at most 48 characters"},
		{name: "visible with reason", mcp: OperationMCP{Handle: "get_order", HiddenReason: "no"}, want: "must not declare a reason"},
		{name: "hidden without reason", mcp: OperationMCP{Hidden: true}, want: "reason must be non-empty"},
		{name: "padded reason", mcp: OperationMCP{Hidden: true, HiddenReason: " Browser only."}, want: "surrounding whitespace"},
		{name: "hidden with handle", mcp: OperationMCP{Hidden: true, HiddenReason: "Browser only.", Handle: "upload"}, want: "must not declare a handle"},
		{name: "hidden with meta", mcp: OperationMCP{Hidden: true, HiddenReason: "Browser only.", Meta: map[string]any{"a": 1.0}}, want: "must not declare _meta"},
		{name: "authored name", mcp: OperationMCP{Handle: "get_order", Name: "Get an order"}, want: "generated from @docs and @icon"},
		{name: "authored icon", mcp: OperationMCP{Handle: "get_order", Icon: &MCPIcon{Name: "receipt"}}, want: "generated from @docs and @icon"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateOperationMCP(&test.mcp)
			if test.want == "" {
				if err != nil {
					t.Fatalf("ValidateOperationMCP() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateOperationMCP() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateOperationIcon(t *testing.T) {
	for _, name := range []string{"receipt", "box-open", "Any Name"} {
		if err := ValidateOperationIcon(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for name, want := range map[string]string{"": "non-empty", " ": "non-empty", " receipt": "surrounding whitespace"} {
		if err := ValidateOperationIcon(name); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: error = %v, want %q", name, err, want)
		}
	}
}

func TestSchemaValidateChecksOperationMCP(t *testing.T) {
	s := NewSchema("shop", SchemaKindAPI)
	s.Types["Order"] = &TypeDef{Name: "Order", Role: RoleAPIView, Fields: []*FieldDef{
		{Name: "id", TypeRef: TypeRef{Name: "string"}, Required: true},
	}}
	docs := validOperationDocs()
	op := &FieldDef{Name: "getOrder", HTTPMethod: "GET", TypeRef: TypeRef{Name: "Order"}, Docs: &docs,
		MCP: &OperationMCP{Handle: "get_order"}, Icon: "receipt"}
	s.OperationSets = []*OperationSet{{Name: "OrderQueries", Operations: []*FieldDef{op}}}
	known := WithKnownExternals(map[string]bool{"string": true})
	if errs := s.Validate(known); len(errs) != 0 {
		t.Fatalf("valid schema: %v", errs)
	}

	for _, test := range []struct {
		name   string
		mutate func()
		want   string
	}{
		{name: "bad handle", mutate: func() { op.MCP = &OperationMCP{Handle: "Get"} }, want: "OrderQueries.getOrder has an invalid @mcp: handle"},
		{name: "visible without docs", mutate: func() { op.Docs = nil }, want: "OrderQueries.getOrder is a visible @mcp tool and must also declare @docs"},
		{name: "padded icon", mutate: func() { op.Icon = "receipt " }, want: "OrderQueries.getOrder has an invalid @icon"},
		{name: "mcp on a data field", mutate: func() {
			s.Types["Order"].Fields[0].MCP = &OperationMCP{Handle: "id"}
		}, want: "Order.id carries an MCP record"},
	} {
		t.Run(test.name, func(t *testing.T) {
			saved, savedField := *op, *s.Types["Order"].Fields[0]
			defer func() { *op, *s.Types["Order"].Fields[0] = saved, savedField }()
			test.mutate()
			errs := s.Validate(known)
			if len(errs) != 1 || !strings.Contains(errs[0].Error(), test.want) {
				t.Fatalf("errors = %v, want %q", errs, test.want)
			}
		})
	}

	// A hidden operation needs no @docs.
	op.Docs = nil
	op.MCP = &OperationMCP{Hidden: true, HiddenReason: "Browser upload only."}
	if errs := s.Validate(known); len(errs) != 0 {
		t.Fatalf("hidden without docs: %v", errs)
	}
}

// TestOperationMCPMarshal: hidden is always written; the generated fields and
// an absent icon are left out.
func TestOperationMCPMarshal(t *testing.T) {
	raw, err := json.Marshal(&OperationMCP{Handle: "get_order", Meta: map[string]any{"ui": map[string]any{"resourceUri": "ui://orders/detail"}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"handle":"get_order","hidden":false,"_meta":{"ui":{"resourceUri":"ui://orders/detail"}}}`; string(raw) != want {
		t.Fatalf("mcp JSON = %s\nwant       %s", raw, want)
	}
	raw, err = json.Marshal(&FieldDef{Name: "getOrder", TypeRef: TypeRef{Name: "Order"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"mcp"`) {
		t.Fatalf("operation without @mcp marshals an mcp key: %s", raw)
	}
}
