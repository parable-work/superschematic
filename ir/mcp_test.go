package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
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
		{name: "visible with policy", mcp: OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Key: "invocationPolicy", Value: "ask"}}},
		{name: "policy without key", mcp: OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Value: "ask"}}, want: `invocation policy "ask" has no key`},
		{name: "policy without value", mcp: OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Key: "invocationPolicy"}}, want: "invocationPolicy must be non-empty"},
		{name: "policy under a record key", mcp: OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Key: "hidden", Value: "ask"}}, want: `invocation policy key "hidden" is a key @mcp already uses`},
		{name: "hidden with policy", mcp: OperationMCP{Hidden: true, HiddenReason: "Browser only.", Invocation: MCPInvocation{Key: "review", Value: "never"}}, want: "a hidden operation must not declare review"},
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

// TestOperationMCPWritesItsPolicyAfterHiddenReason: the policy is written
// under its own key between hiddenReason and _meta, in JSON and in YAML.
func TestOperationMCPWritesItsPolicyAfterHiddenReason(t *testing.T) {
	record := &OperationMCP{
		Handle:      "delete_order",
		Invocation:  MCPInvocation{Key: "review", Value: "always"},
		Meta:        map[string]any{"ui": "x"},
		Name:        "Delete an order",
		Description: "Deletes one order.",
		Icon:        &MCPIcon{Name: "trash"},
	}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"handle":"delete_order","hidden":false,"review":"always","_meta":{"ui":"x"},"name":"Delete an order","description":"Deletes one order.","icon":{"name":"trash"}}`
	if string(raw) != want {
		t.Fatalf("mcp JSON = %s\nwant       %s", raw, want)
	}
	out, err := yaml.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	wantYAML := "handle: delete_order\nhidden: false\nreview: always\n_meta:\n    ui: x\nname: Delete an order\ndescription: Deletes one order.\nicon:\n    name: trash\n"
	if string(out) != wantYAML {
		t.Fatalf("mcp YAML =\n%s\nwant\n%s", out, wantYAML)
	}

	// A record with only the keys before the policy.
	raw, err = json.Marshal(&OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Key: "invocationPolicy", Value: "auto"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"handle":"get_order","hidden":false,"invocationPolicy":"auto"}`; string(raw) != want {
		t.Fatalf("mcp JSON = %s\nwant       %s", raw, want)
	}

	if _, err := json.Marshal(&OperationMCP{Handle: "get_order", Invocation: MCPInvocation{Value: "ask"}}); err == nil || !strings.Contains(err.Error(), `mcp invocation policy "ask" has no key`) {
		t.Fatalf("a policy without a key: err = %v", err)
	}
}

// plainOperationMCP is OperationMCP without its policy and its custom
// encoding: what the struct tags alone write.
type plainOperationMCP struct {
	Handle       string         `json:"handle,omitempty"`
	Hidden       bool           `json:"hidden"`
	HiddenReason string         `json:"hiddenReason,omitempty"`
	Meta         map[string]any `json:"_meta,omitempty"`
	Name         string         `json:"name,omitempty"`
	Description  string         `json:"description,omitempty"`
	Icon         *MCPIcon       `json:"icon,omitempty"`
}

// TestOperationMCPEncodesLikeItsTags: without a policy the custom encoding
// writes what the struct tags write, HTML escaping included, whichever way
// the calling encoder is configured.
func TestOperationMCPEncodesLikeItsTags(t *testing.T) {
	record := OperationMCP{
		Handle: "a<b", Meta: map[string]any{"z": "&", "a": []any{"<x>", 1.5}},
		Name: "A & B", Description: "x > y", Icon: &MCPIcon{Name: "i", Family: "f"},
	}
	plain := plainOperationMCP{Handle: record.Handle, Meta: record.Meta, Name: record.Name, Description: record.Description, Icon: record.Icon}
	for _, escape := range []bool{true, false} {
		encode := func(v any) string {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(escape)
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]any{"mcp": v}); err != nil {
				t.Fatal(err)
			}
			return buf.String()
		}
		if got, want := encode(record), encode(plain); got != want {
			t.Fatalf("escapeHTML=%v:\ngot  %s\nwant %s", escape, got, want)
		}
	}
}

func TestOperationMCPDecodesItsPolicy(t *testing.T) {
	want := OperationMCP{Handle: "delete_order", Invocation: MCPInvocation{Key: "review", Value: "always"}, Meta: map[string]any{"ui": "x"}}
	var fromJSON OperationMCP
	if err := json.Unmarshal([]byte(`{"review":"always","handle":"delete_order","hidden":false,"_meta":{"ui":"x"}}`), &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON, want) {
		t.Fatalf("from JSON = %+v, want %+v", fromJSON, want)
	}
	var fromYAML OperationMCP
	if err := yaml.Unmarshal([]byte("handle: delete_order\nreview: always\n_meta:\n  ui: x\n"), &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML, want) {
		t.Fatalf("from YAML = %+v, want %+v", fromYAML, want)
	}

	// Round trips, through a FieldDef as the IR holds it.
	for _, record := range []*OperationMCP{&want, {Hidden: true, HiddenReason: "Staff only."}, {Handle: "get_order"}} {
		field := FieldDef{Name: "op", TypeRef: TypeRef{Name: "Order"}, MCP: record}
		raw, err := json.Marshal(field)
		if err != nil {
			t.Fatal(err)
		}
		var back FieldDef
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back.MCP, record) {
			t.Fatalf("JSON round trip of %+v gave %+v", record, back.MCP)
		}
		out, err := yaml.Marshal(field)
		if err != nil {
			t.Fatal(err)
		}
		var backYAML FieldDef
		if err := yaml.Unmarshal(out, &backYAML); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(backYAML.MCP, record) {
			t.Fatalf("YAML round trip of %+v gave %+v", record, backYAML.MCP)
		}
	}

	for _, test := range []struct {
		name, json, want string
	}{
		{"two extra keys", `{"handle":"a","hidden":false,"review":"x","confirm":"y"}`, `mcp record has unknown keys ["confirm" "review"]`},
		{"reason is not an IR key", `{"hidden":true,"reason":"x"}`, `mcp record has unknown key "reason"`},
		{"policy not a string", `{"handle":"a","hidden":false,"review":true}`, "mcp review must be a string"},
		{"bad key", `{"handle":"a","hidden":false,"x-review":"y"}`, `mcp record has unknown key "x-review"`},
		{"bad record key", `{"handle":1,"hidden":false}`, "cannot unmarshal number"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var record OperationMCP
			if err := json.Unmarshal([]byte(test.json), &record); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
	var record OperationMCP
	if err := yaml.Unmarshal([]byte("handle: a\nreview: [x]\n"), &record); err == nil || !strings.Contains(err.Error(), "mcp review must be a string") {
		t.Fatalf("YAML policy not a string: err = %v", err)
	}
}
