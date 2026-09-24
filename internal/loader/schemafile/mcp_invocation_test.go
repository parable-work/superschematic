package schemafile

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// operationSetWithMCP is a single operation-set file whose one operation
// carries the given mcp record.
func operationSetWithMCP(mcp string) []byte {
	return []byte(`{
		"name": "OrderOperations",
		"kind": "OperationSet",
		"operations": [
			{"name": "deleteOrder", "typeRef": {"name": "Order"}, "httpMethod": "DELETE", "mcp": ` + mcp + `}
		]
	}`)
}

func decodedMCP(t *testing.T, reg *registry.Registry, mcp string) (*ir.OperationMCP, error) {
	t.Helper()
	doc, err := DecodeWith(operationSetWithMCP(mcp), "orders.schema.json", reg)
	if err != nil {
		return nil, err
	}
	return doc.OperationSets[0].Operations[0].MCP, nil
}

// TestDataFormsReadTheInvocationPolicy: the data forms accept the core
// policy key with its values, fill in the default for a visible tool that
// omits it, and leave a hidden record without one.
func TestDataFormsReadTheInvocationPolicy(t *testing.T) {
	for mcp, want := range map[string]ir.MCPInvocation{
		`{"handle": "delete_order", "hidden": false, "invocationPolicy": "ask"}`: {Key: "invocationPolicy", Value: "ask"},
		`{"handle": "delete_order", "hidden": false}`:                            {Key: "invocationPolicy", Value: "auto"},
		`{"hidden": true, "hiddenReason": "Staff only."}`:                        {},
	} {
		got, err := decodedMCP(t, nil, mcp)
		if err != nil {
			t.Fatalf("%s: %v", mcp, err)
		}
		if got.Invocation != want {
			t.Errorf("%s: Invocation = %+v, want %+v", mcp, got.Invocation, want)
		}
	}
	// The JSON Schema holds the key and the value to the registry's policy.
	for mcp, want := range map[string]string{
		`{"handle": "delete_order", "hidden": false, "invocationPolicy": "always"}`:                  "/operations/0/mcp/invocationPolicy",
		`{"handle": "delete_order", "hidden": false, "invocationPolicy": ["ask"]}`:                   "/operations/0/mcp/invocationPolicy",
		`{"handle": "delete_order", "hidden": false, "review": "always"}`:                            "additional properties 'review' not allowed",
		`{"handle": "delete_order", "hidden": false, "invocationPolicy": "ask", "review": "always"}`: "additional properties 'review' not allowed",
	} {
		if _, err := decodedMCP(t, nil, mcp); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", mcp, err, want)
		}
	}
	// A policy on a hidden record decodes; the shape rule the loader runs
	// after the merge (Schema.Validate) rejects it.
	got, err := decodedMCP(t, nil, `{"hidden": true, "hiddenReason": "Staff only.", "invocationPolicy": "ask"}`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ir.ValidateOperationMCP(got); err == nil || !strings.Contains(err.Error(), "a hidden operation must not declare invocationPolicy") {
		t.Fatalf("hidden record with a policy: err = %v", err)
	}
}

// TestDataFormsReadAnExtensionsInvocationPolicy: with an extension's policy
// registered the data forms take its key, values and default, and the core
// key is unknown.
func TestDataFormsReadAnExtensionsInvocationPolicy(t *testing.T) {
	reg := registry.New(naming.Default())
	if err := reg.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{
		Extension: "vendor", Key: "review", Values: []string{"never", "always"}, Default: "never",
	}); err != nil {
		t.Fatal(err)
	}

	data, err := DefinitionFor(reg)
	if err != nil {
		t.Fatal(err)
	}
	var root struct {
		Defs map[string]struct {
			Properties map[string]map[string]any `json:"properties"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	props := root.Defs["OperationMCP"].Properties
	if _, ok := props["invocationPolicy"]; ok {
		t.Fatal("the extension's JSON Schema still has the core key")
	}
	if enum, _ := props["review"]["enum"].([]any); len(enum) != 2 || enum[0] != "never" || enum[1] != "always" {
		t.Fatalf("review property = %v", props["review"])
	}

	got, err := decodedMCP(t, reg, `{"handle": "delete_order", "hidden": false, "review": "always"}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ir.MCPInvocation{Key: "review", Value: "always"}); got.Invocation != want {
		t.Fatalf("Invocation = %+v, want %+v", got.Invocation, want)
	}
	got, err = decodedMCP(t, reg, `{"handle": "delete_order", "hidden": false}`)
	if err != nil {
		t.Fatal(err)
	}
	if want := (ir.MCPInvocation{Key: "review", Value: "never"}); got.Invocation != want {
		t.Fatalf("default Invocation = %+v, want %+v", got.Invocation, want)
	}
	if _, err := decodedMCP(t, reg, `{"handle": "delete_order", "hidden": false, "invocationPolicy": "ask"}`); err == nil ||
		!strings.Contains(err.Error(), "additional properties 'invocationPolicy' not allowed") {
		t.Fatalf("core key under an extension's policy: err = %v", err)
	}
}
