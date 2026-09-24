package apigen_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	ir "github.com/parable-work/superschematic/ir"
)

func generateMCPWithHooks(t *testing.T, hooks ...apigen.ToolHook) (*apigen.APIOutput, error) {
	t.Helper()
	return apigen.Generate(loadMCPFixture(t), apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  "fixture-mcp",
		ModulePath:  "example.com/schemas/api/fixture-mcp",
		TypesModule: "example.com/schemas/types/go/fixture-mcp",
		ToolHooks:   hooks,
	})
}

// TestToolHooksEditKeysAndRecords: without hooks the output carries the
// core keys; hooks run in order, see every operation with the schema's
// FieldDef and edit the resolved records the endpoints keep.
func TestToolHooksEditKeysAndRecords(t *testing.T) {
	plain, err := generateMCPWithHooks(t)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plain.ToolKeys, apigen.DefaultToolKeys()) {
		t.Fatalf("ToolKeys without hooks = %+v", plain.ToolKeys)
	}

	var order []string
	first := apigen.ToolHook{Name: "first", Edit: func(schema *ir.Schema, tools *apigen.ToolSet) error {
		order = append(order, "first")
		if schema.Name != "fixture-mcp" || len(tools.Tools) != len(plain.Endpoints) {
			return errors.New("unexpected input")
		}
		tools.Keys.Scalar = "x-vendor-scalar"
		for _, tool := range tools.Tools {
			if tool.Operation == nil {
				return errors.New("a tool without its operation")
			}
			if tool.MCP != nil && tool.MCP.Icon != nil {
				tool.MCP.Icon.Family = "line"
			}
		}
		return nil
	}}
	second := apigen.ToolHook{Name: "second", Edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
		order = append(order, "second")
		if tools.Keys.Scalar != "x-vendor-scalar" {
			return errors.New("the first hook's keys are not visible")
		}
		tools.Keys.Parameters = []apigen.ToolKeyValue{{Key: "x-vendor-version", Value: 1}}
		return nil
	}}
	output, err := generateMCPWithHooks(t, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("hook order = %v", order)
	}
	want := apigen.ToolKeys{Scalar: "x-vendor-scalar", Guidance: apigen.DefaultToolGuidanceKey, Parameters: []apigen.ToolKeyValue{{Key: "x-vendor-version", Value: 1}}}
	if !reflect.DeepEqual(output.ToolKeys, want) {
		t.Fatalf("ToolKeys = %+v, want %+v", output.ToolKeys, want)
	}
	if icon := endpointNamed(t, output, "getOrder").MCP.Icon; icon.Family != "line" {
		t.Fatalf("getOrder icon = %+v", icon)
	}
	schema := loadMCPFixture(t)
	if operationIn(schema, "getOrder").MCP.Icon != nil {
		t.Fatal("the hook's edit reached a fresh load of the schema")
	}
}

func TestToolHookErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*ir.Schema, *apigen.ToolSet) error
		want string
	}{
		{name: "hook error", edit: func(*ir.Schema, *apigen.ToolSet) error { return errors.New("no") }, want: "apigen: tool hook vendor: no"},
		{name: "dropped tool", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Tools = tools.Tools[1:]
			return nil
		}, want: "a hook edits tools, it does not add or remove them"},
		{name: "empty key", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Keys.Parameters = []apigen.ToolKeyValue{{Value: 1}}
			return nil
		}, want: "a tool parameter key is empty"},
		{name: "core key", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Keys.Parameters = []apigen.ToolKeyValue{{Key: "properties", Value: 1}}
			return nil
		}, want: `tool parameter key "properties" is written by the core`},
		{name: "duplicate key", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Keys.Parameters = []apigen.ToolKeyValue{{Key: "x-v", Value: 1}, {Key: "x-v", Value: 2}}
			return nil
		}, want: `tool parameter key "x-v" is listed twice`},
		{name: "value not JSON", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			tools.Keys.Parameters = []apigen.ToolKeyValue{{Key: "x-v", Value: func() {}}}
			return nil
		}, want: `tool parameter key "x-v": value does not encode as JSON`},
		{name: "collision after the hook", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			for _, tool := range tools.Tools {
				if tool.MCP != nil && !tool.MCP.Hidden {
					tool.MCP.Handle = "same"
				}
			}
			return nil
		}, want: `apigen: MCP handle collision: "same"`},
		{name: "policy value after the hook", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			for _, tool := range tools.Tools {
				if tool.MCP != nil && !tool.MCP.Hidden {
					tool.MCP.Invocation.Value = "always"
				}
			}
			return nil
		}, want: `apigen: visible @mcp tool order.listOrders after the tool hooks ran: invocationPolicy "always" is not one of "auto", "ask"`},
		{name: "policy dropped by the hook", edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
			for _, tool := range tools.Tools {
				if tool.MCP != nil && !tool.MCP.Hidden {
					tool.MCP.Invocation = ir.MCPInvocation{}
				}
			}
			return nil
		}, want: "apigen: visible @mcp tool order.listOrders has no invocationPolicy after the tool hooks ran"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := generateMCPWithHooks(t, apigen.ToolHook{Name: "vendor", Edit: test.edit})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

// TestToolTypeFieldsCarryShapeAndBounds: TypeFields holds every type a tool
// schema expands, with each field's map shape and Validate<> bounds, and
// every schema scalar carries its canonical name.
func TestToolTypeFieldsCarryShapeAndBounds(t *testing.T) {
	output, err := generateMCPWithHooks(t)
	if err != nil {
		t.Fatal(err)
	}
	address, ok := output.TypeFields["Address"]
	if !ok || len(address) != 3 || address[2].Name != "postalCode" || address[2].Required {
		t.Fatalf("TypeFields[Address] = %+v", address)
	}
	for _, field := range output.TypeFields["ReturnRequest"] {
		if field.Name == "labels" && !field.IsMap {
			t.Fatalf("ReturnRequest.labels is not a map: %+v", field)
		}
		if field.Name == "reason" && (field.ValidateMinLength == nil || *field.ValidateMinLength != 1) {
			t.Fatalf("ReturnRequest.reason lost its bounds: %+v", field)
		}
	}
	if output.Scalars["Identity.UUID"].CanonicalName != "Identity.UUID" || output.Scalars["string"].CanonicalName != "" {
		t.Fatalf("scalar canonical names = %q, %q", output.Scalars["Identity.UUID"].CanonicalName, output.Scalars["string"].CanonicalName)
	}
}

// TestToolHookMayChangeAPolicyValue: a hook may move a tool to another value
// of the build's policy.
func TestToolHookMayChangeAPolicyValue(t *testing.T) {
	askAll := apigen.ToolHook{Name: "askAll", Edit: func(_ *ir.Schema, tools *apigen.ToolSet) error {
		for _, tool := range tools.Tools {
			if tool.MCP != nil && !tool.MCP.Hidden {
				tool.MCP.Invocation.Value = apigen.ToolInvocationAsk
			}
		}
		return nil
	}}
	output, err := generateMCPWithHooks(t, askAll)
	if err != nil {
		t.Fatal(err)
	}
	if got := endpointNamed(t, output, "getOrder").MCP.Invocation.Value; got != "ask" {
		t.Fatalf("getOrder policy = %q, want ask", got)
	}
}

func TestInvalidToolInvocationOption(t *testing.T) {
	_, err := apigen.Generate(loadMCPFixture(t), apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     "fixture-mcp",
		ToolInvocation: apigen.ToolInvocationPolicy{Key: "review", Values: []string{"a"}, Default: "b"},
	})
	if want := `apigen: invocation policy review default "b" is not one of "a"`; err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
}
