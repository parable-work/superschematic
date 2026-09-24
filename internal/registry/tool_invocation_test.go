package registry

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// reviewPolicy is an extension's invocation policy: its own key, three
// values and a default that is not the first.
var reviewPolicy = ToolInvocationPolicy{
	Extension: "vendor",
	Key:       "review",
	Values:    []string{"never", "on-write", "always"},
	Default:   "on-write",
}

type policyExtension struct {
	name   string
	policy ToolInvocationPolicy
}

func (e policyExtension) Name() string { return e.name }

func (e policyExtension) Register(r *Registry) error {
	return r.RegisterToolInvocationPolicy(e.policy)
}

func TestToolInvocationPolicyDefaultsToTheCore(t *testing.T) {
	got := New(naming.Default()).ToolInvocationPolicy()
	want := ToolInvocationPolicy{Key: "invocationPolicy", Values: []string{"auto", "ask"}, Default: "auto"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolInvocationPolicy() = %+v, want %+v", got, want)
	}
}

func TestRegisterToolInvocationPolicy(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterToolInvocationPolicy(reviewPolicy); err != nil {
		t.Fatal(err)
	}
	got := reg.ToolInvocationPolicy()
	if !reflect.DeepEqual(got, reviewPolicy) {
		t.Fatalf("ToolInvocationPolicy() = %+v, want %+v", got, reviewPolicy)
	}
	got.Values[0] = "changed"
	if reg.ToolInvocationPolicy().Values[0] != "never" {
		t.Fatal("ToolInvocationPolicy() shares its Values with the registry")
	}
	if !slices.Contains(reg.Extensions(), "vendor") {
		t.Fatalf("Extensions() = %v, want the policy's extension", reg.Extensions())
	}
	finalizeWithCoreGenerators(t, reg)
	err := reg.RegisterToolInvocationPolicy(ToolInvocationPolicy{Extension: "late", Key: "late", Values: []string{"a"}, Default: "a"})
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v, want an after-Finalize error", err)
	}
}

func TestRegisterToolInvocationPolicyRejectsInvalid(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy ToolInvocationPolicy
		want   string
	}{
		{"no extension", ToolInvocationPolicy{Key: "review", Values: []string{"a"}, Default: "a"}, `tool invocation policy "review" names no extension`},
		{"empty key", ToolInvocationPolicy{Extension: "x", Values: []string{"a"}, Default: "a"}, `invocation policy key "" must be a letter`},
		{"hyphenated key", ToolInvocationPolicy{Extension: "x", Key: "on-call", Values: []string{"a"}, Default: "a"}, `invocation policy key "on-call" must be a letter`},
		{"record key", ToolInvocationPolicy{Extension: "x", Key: "handle", Values: []string{"a"}, Default: "a"}, `invocation policy key "handle" is a key @mcp already uses`},
		{"authoring key", ToolInvocationPolicy{Extension: "x", Key: "reason", Values: []string{"a"}, Default: "a"}, `invocation policy key "reason" is a key @mcp already uses`},
		{"no values", ToolInvocationPolicy{Extension: "x", Key: "review", Default: "a"}, "invocation policy review lists no values"},
		{"quoted value", ToolInvocationPolicy{Extension: "x", Key: "review", Values: []string{"it's"}, Default: "it's"}, `invocation policy review value "it's" must be lowercase`},
		{"repeated value", ToolInvocationPolicy{Extension: "x", Key: "review", Values: []string{"a", "a"}, Default: "a"}, `invocation policy review lists "a" twice`},
		{"default not a value", ToolInvocationPolicy{Extension: "x", Key: "review", Values: []string{"a", "b"}, Default: "c"}, `invocation policy review default "c" is not one of "a", "b"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			reg := New(naming.Default())
			err := reg.RegisterToolInvocationPolicy(test.policy)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if got := reg.ToolInvocationPolicy(); got.Key != "invocationPolicy" {
				t.Fatalf("a refused registration installed %+v", got)
			}
		})
	}
}

// TestConflictingToolInvocationPoliciesFailAssembly: two extensions that
// each register a policy fail Use, and Finalize reports the same error, so
// the registry is never used with either.
func TestConflictingToolInvocationPoliciesFailAssembly(t *testing.T) {
	reg := New(naming.Default())
	other := reviewPolicy
	other.Extension, other.Key = "other", "confirm"
	err := reg.Use(policyExtension{"vendor", reviewPolicy}, policyExtension{"other", other})
	want := `extension vendor registered the tool invocation policy "review"; extension other cannot register "confirm" as well`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Use() = %v, want %q", err, want)
	}
	if ferr := reg.Finalize(); ferr == nil || ferr.Error() != err.Error() {
		t.Fatalf("Finalize() = %v, want the Use error", ferr)
	}

	// The same policy registered twice is still two registrations.
	reg = New(naming.Default())
	if err := reg.Use(policyExtension{"vendor", reviewPolicy}, policyExtension{"vendor2", reviewPolicy}); err == nil {
		t.Fatal("a second registration of the same policy: want error")
	}
}

func TestMCPDecoratorAppliesTheRegisteredPolicy(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterToolInvocationPolicy(reviewPolicy); err != nil {
		t.Fatal(err)
	}
	spec, _ := reg.Decorator("mcp", TargetOperation)
	apply := func(cfg map[string]any) (*ir.FieldDef, error) {
		op := &ir.FieldDef{Name: "deleteOrder"}
		return op, spec.Apply(Node{Field: op}, []any{cfg}, Site{})
	}

	op, err := apply(map[string]any{"handle": "delete_order", "review": "always"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (ir.MCPInvocation{Key: "review", Value: "always"}); op.MCP.Invocation != want {
		t.Fatalf("Invocation = %+v, want %+v", op.MCP.Invocation, want)
	}
	op, err = apply(map[string]any{"handle": "get_order"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (ir.MCPInvocation{Key: "review", Value: "on-write"}); op.MCP.Invocation != want {
		t.Fatalf("default Invocation = %+v, want %+v", op.MCP.Invocation, want)
	}

	for name, test := range map[string]struct {
		args map[string]any
		want string
	}{
		"bad value":    {map[string]any{"handle": "get_order", "review": "sometimes"}, `invalid @mcp config: review "sometimes" is not one of "never", "on-write", "always"`},
		"not a string": {map[string]any{"handle": "get_order", "review": true}, "@mcp review must be a string literal"},
		"core key":     {map[string]any{"handle": "get_order", "invocationPolicy": "ask"}, `@mcp config has unknown key "invocationPolicy"; this build's invocation policy key is "review"`},
		"hidden":       {map[string]any{"hidden": true, "reason": "Staff only.", "review": "never"}, "invalid @mcp config: a hidden operation must not declare review"},
	} {
		t.Run(name, func(t *testing.T) {
			op, err := apply(test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			var argErr *ArgError
			if !errors.As(err, &argErr) || argErr.Index != 0 {
				t.Fatalf("err = %#v, want an ArgError at index 0", err)
			}
			if op.MCP != nil {
				t.Fatalf("a rejected @mcp wrote %+v", op.MCP)
			}
		})
	}
}
