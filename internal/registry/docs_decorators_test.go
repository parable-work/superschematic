package registry

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func applyDocs(t *testing.T, target DecoratorTarget, field *ir.FieldDef, args ...any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator("docs", target)
	if !ok {
		t.Fatalf("no @docs for %v", target)
	}
	return spec.Apply(Node{Field: field}, args, Site{})
}

func TestOperationDocsDecoratorWritesTheRecord(t *testing.T) {
	op := &ir.FieldDef{Name: "createReturn"}
	err := applyDocs(t, TargetOperation, op, map[string]any{
		"title":       "Create a return",
		"description": "Opens a return for one delivered order.",
		"capability":  "orders.returns.create",
		"lifecycle":   "deprecated",
		"visibility":  "public",
		"audience":    "shoppers",
		"replacement": "orders.returns.open",
		"sunset":      "2027-01-31",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := ir.OperationDocs{
		Title: "Create a return", Description: "Opens a return for one delivered order.",
		Capability: "orders.returns.create", Lifecycle: ir.DocsLifecycleDeprecated,
		Visibility: ir.DocsVisibilityPublic, Audience: "shoppers",
		MappingStatus: ir.DocsMappingStatusMapped,
		Replacement:   "orders.returns.open", Sunset: "2027-01-31",
	}
	if op.Docs == nil || !reflect.DeepEqual(*op.Docs, want) {
		t.Fatalf("Docs = %+v\nwant   %+v", op.Docs, want)
	}

	err = applyDocs(t, TargetOperation, op, map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "operation createReturn has more than one @docs decorator") {
		t.Fatalf("second @docs: %v", err)
	}
}

func TestOperationDocsDecoratorReadsGuidance(t *testing.T) {
	op := &ir.FieldDef{Name: "getOrder"}
	err := applyDocs(t, TargetOperation, op, map[string]any{
		"title": "Get an order", "description": "Returns one order.",
		"capability": "orders.get", "lifecycle": "active", "visibility": "internal",
		"useWhen":      "Use when you have an order identifier.",
		"doNotUseWhen": "Do not use to list orders.",
		"success":      "Returns the requested order.",
		"errors": []any{map[string]any{
			"code":             "order_not_found",
			"description":      "No order has that identifier.",
			"commonCorrection": "Take the identifier from a listOrders result.",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	d := op.Docs
	if d.UseWhen != "Use when you have an order identifier." || d.DoNotUseWhen != "Do not use to list orders." || d.Success != "Returns the requested order." {
		t.Fatalf("guidance = %+v", d)
	}
	want := ir.OperationDocsError{Code: "order_not_found", Description: "No order has that identifier.", CommonCorrection: "Take the identifier from a listOrders result."}
	if len(d.Errors) != 1 || d.Errors[0] != want {
		t.Fatalf("errors = %+v", d.Errors)
	}
}

func TestOperationDocsDecoratorRejectsBadConfig(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			"title": "Get an order", "description": "Returns one order.",
			"capability": "orders.get", "lifecycle": "active", "visibility": "internal",
		}
	}
	tests := []struct {
		name string
		args []any
		want string
	}{
		{name: "no argument", args: nil, want: "@docs takes exactly one config object"},
		{name: "not an object", args: []any{"Get an order"}, want: "@docs config must be an object literal"},
		{name: "unknown key", args: []any{func() map[string]any { c := base(); c["owner"] = "shop"; return c }()}, want: `@docs config has unknown key "owner"`},
		{name: "not a string", args: []any{func() map[string]any { c := base(); c["title"] = 3.0; return c }()}, want: "@docs title must be a string literal"},
		{name: "missing lifecycle", args: []any{func() map[string]any { c := base(); delete(c, "lifecycle"); return c }()}, want: `invalid @docs config: lifecycle ""`},
		{name: "bad mapping status", args: []any{func() map[string]any { c := base(); c["mappingStatus"] = "maybe"; return c }()}, want: `mappingStatus "maybe"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			op := &ir.FieldDef{Name: "getOrder"}
			err := applyDocs(t, TargetOperation, op, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if op.Docs != nil {
				t.Fatalf("a rejected @docs wrote %+v", op.Docs)
			}
		})
	}

	// Guidance: the errors list and its entries.
	for _, test := range []struct {
		name   string
		errors any
		want   string
	}{
		{name: "empty errors", errors: []any{}, want: "@docs errors must be a non-empty array of object literals"},
		{name: "errors not a list", errors: "order_not_found", want: "@docs errors must be a non-empty array of object literals"},
		{name: "entry not an object", errors: []any{"order_not_found"}, want: "@docs errors must be a non-empty array of object literals"},
		{name: "unknown entry key", errors: []any{map[string]any{"code": "x", "hint": "y"}}, want: `@docs errors has unknown key "hint"`},
		{name: "entry value not a string", errors: []any{map[string]any{"code": 404.0}}, want: "@docs errors code must be a string literal"},
		{name: "incomplete entry", errors: []any{map[string]any{"code": "order_not_found"}}, want: "invalid @docs config: errors description must be non-empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base()
			cfg["errors"] = test.errors
			err := applyDocs(t, TargetOperation, &ir.FieldDef{}, cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}

	// Errors inside the config object point the TypeScript diagnostic at
	// the argument.
	err := applyDocs(t, TargetOperation, &ir.FieldDef{}, map[string]any{"title": 1.0})
	var argErr *ArgError
	if !errors.As(err, &argErr) || argErr.Index != 0 {
		t.Fatalf("err = %#v, want an ArgError at index 0", err)
	}
}
