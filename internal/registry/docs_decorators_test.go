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

func applyField(t *testing.T, name string, field *ir.FieldDef, args ...any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator(name, TargetField)
	if !ok {
		t.Fatalf("no field decorator @%s", name)
	}
	if !spec.DeclaredIn(pkgSchema) {
		t.Fatalf("@%s on a field is not declared in %s", name, pkgSchema)
	}
	return spec.Apply(Node{Field: field}, args, Site{})
}

func TestFieldPresentationDecoratorsWriteTheField(t *testing.T) {
	f := &ir.FieldDef{Name: "region"}
	if err := applyField(t, "docs", f, map[string]any{"title": "Region"}); err != nil {
		t.Fatal(err)
	}
	if err := applyField(t, "purpose", f, "Where orders **ship** from."); err != nil {
		t.Fatal(err)
	}
	if err := applyField(t, "icon", f, "globe"); err != nil {
		t.Fatal(err)
	}
	if f.Title != "Region" || f.Purpose != "Where orders **ship** from." || f.Icon != "globe" {
		t.Fatalf("field = %+v", f)
	}
	// Any icon name: an icon set is an extension's check.
	if err := applyField(t, "icon", &ir.FieldDef{}, "Not A Glyph"); err != nil {
		t.Fatalf("core rejected an icon name: %v", err)
	}
	for _, name := range []string{"docs", "purpose", "icon"} {
		args := []any{"again"}
		if name == "docs" {
			args = []any{map[string]any{"title": "Again"}}
		}
		err := applyField(t, name, f, args...)
		if err == nil || !strings.Contains(err.Error(), "field region has more than one @"+name+" decorator") {
			t.Errorf("second @%s: %v", name, err)
		}
	}
}

func TestFieldPresentationDecoratorsRejectBadArguments(t *testing.T) {
	tests := []struct {
		decorator string
		args      []any
		want      string
	}{
		{"docs", nil, "field @docs takes exactly one config object"},
		{"docs", []any{"Region"}, "field @docs config must contain only title"},
		{"docs", []any{map[string]any{"title": "Region", "description": "x"}}, "field @docs config must contain only title"},
		{"docs", []any{map[string]any{"title": " "}}, "field @docs title must be a non-empty string literal"},
		{"docs", []any{map[string]any{"title": 1.0}}, "field @docs title must be a non-empty string literal"},
		{"purpose", nil, "@purpose takes exactly one string argument"},
		{"purpose", []any{""}, "@purpose takes a non-empty string literal"},
		{"icon", []any{"globe", "solid"}, "@icon takes exactly one string argument"},
		{"icon", []any{true}, "@icon takes a non-empty string literal"},
	}
	for _, test := range tests {
		f := &ir.FieldDef{Name: "region"}
		err := applyField(t, test.decorator, f, test.args...)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("@%s%v: err = %v, want %q", test.decorator, test.args, err, test.want)
		}
		if f.Title != "" || f.Purpose != "" || f.Icon != "" {
			t.Errorf("@%s%v wrote %+v", test.decorator, test.args, f)
		}
	}
}

func TestOperationDocsDecoratorReadsReplay(t *testing.T) {
	op := &ir.FieldDef{Name: "openReturn"}
	err := applyDocs(t, TargetOperation, op, map[string]any{
		"title": "Open a return", "description": "Opens a return.",
		"capability": "orders.returns.open", "lifecycle": "active", "visibility": "public",
		"replayMode":             "idempotent",
		"idempotencyKeyPointers": []any{"/requestId"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Docs.ReplayMode != ir.DocsReplayModeIdempotent || !reflect.DeepEqual(op.Docs.IdempotencyKeyPointers, []string{"/requestId"}) {
		t.Fatalf("replay = %q %v", op.Docs.ReplayMode, op.Docs.IdempotencyKeyPointers)
	}

	for _, test := range []struct {
		name string
		key  string
		val  any
		want string
	}{
		{name: "empty pointers", key: "idempotencyKeyPointers", val: []any{}, want: "@docs idempotencyKeyPointers must be a non-empty array of string literals"},
		{name: "pointer not a string", key: "expectedRevisionPointers", val: []any{1.0}, want: "@docs expectedRevisionPointers must be a non-empty array of string literals"},
		{name: "mode without pointer", key: "replayMode", val: "compare_and_swap", want: "invalid @docs config: compare_and_swap replayMode requires expectedRevisionPointers"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := map[string]any{
				"title": "Open a return", "description": "Opens a return.",
				"capability": "orders.returns.open", "lifecycle": "active", "visibility": "public",
				test.key: test.val,
			}
			err := applyDocs(t, TargetOperation, &ir.FieldDef{}, cfg)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func applyOperation(t *testing.T, name string, op *ir.FieldDef, args ...any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator(name, TargetOperation)
	if !ok {
		t.Fatalf("no operation decorator @%s", name)
	}
	if !spec.DeclaredIn(pkgAPI) {
		t.Fatalf("@%s on an operation is not declared in %s", name, pkgAPI)
	}
	return spec.Apply(Node{Field: op}, args, Site{})
}

func TestMCPDecoratorWritesTheRecord(t *testing.T) {
	op := &ir.FieldDef{Name: "getOrder"}
	meta := map[string]any{"ui": map[string]any{"resourceUri": "ui://orders/detail"}}
	if err := applyOperation(t, "mcp", op, map[string]any{"handle": "get_order", "_meta": meta}); err != nil {
		t.Fatal(err)
	}
	if want := (&ir.OperationMCP{Handle: "get_order", Meta: meta}); !reflect.DeepEqual(op.MCP, want) {
		t.Fatalf("MCP = %+v, want %+v", op.MCP, want)
	}
	err := applyOperation(t, "mcp", op, map[string]any{"handle": "get_order"})
	if err == nil || !strings.Contains(err.Error(), "operation getOrder has more than one @mcp decorator") {
		t.Fatalf("second @mcp: %v", err)
	}

	hidden := &ir.FieldDef{Name: "uploadReceipt"}
	if err := applyOperation(t, "mcp", hidden, map[string]any{"hidden": true, "reason": "Browser upload only."}); err != nil {
		t.Fatal(err)
	}
	if want := (&ir.OperationMCP{Hidden: true, HiddenReason: "Browser upload only."}); !reflect.DeepEqual(hidden.MCP, want) {
		t.Fatalf("MCP = %+v, want %+v", hidden.MCP, want)
	}
}

func TestMCPDecoratorRejectsBadConfig(t *testing.T) {
	for _, test := range []struct {
		name string
		args []any
		want string
	}{
		{name: "no argument", want: "@mcp takes exactly one config object"},
		{name: "not an object", args: []any{"get_order"}, want: "@mcp config must be an object literal"},
		{name: "unknown key", args: []any{map[string]any{"handle": "get_order", "policy": "ask"}}, want: `@mcp config has unknown key "policy"`},
		{name: "handle not a string", args: []any{map[string]any{"handle": 1.0}}, want: "@mcp handle must be a string literal"},
		{name: "hidden not a bool", args: []any{map[string]any{"hidden": "yes", "reason": "x"}}, want: "@mcp hidden must be a boolean literal"},
		{name: "meta not an object", args: []any{map[string]any{"handle": "get_order", "_meta": "x"}}, want: "@mcp _meta must be an object literal"},
		{name: "bad handle", args: []any{map[string]any{"handle": "getOrder"}}, want: "invalid @mcp config: handle \"getOrder\" must be lowercase snake_case"},
		{name: "hidden without reason", args: []any{map[string]any{"hidden": true}}, want: "invalid @mcp config: reason must be non-empty"},
	} {
		t.Run(test.name, func(t *testing.T) {
			op := &ir.FieldDef{Name: "getOrder"}
			err := applyOperation(t, "mcp", op, test.args...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if op.MCP != nil {
				t.Fatalf("a rejected @mcp wrote %+v", op.MCP)
			}
		})
	}
	err := applyOperation(t, "mcp", &ir.FieldDef{}, map[string]any{"handle": "Get"})
	var argErr *ArgError
	if !errors.As(err, &argErr) || argErr.Index != 0 {
		t.Fatalf("err = %#v, want an ArgError at index 0", err)
	}
}

func TestOperationIconDecorator(t *testing.T) {
	op := &ir.FieldDef{Name: "getOrder"}
	if err := applyOperation(t, "icon", op, "receipt"); err != nil {
		t.Fatal(err)
	}
	if op.Icon != "receipt" {
		t.Fatalf("Icon = %q", op.Icon)
	}
	// Any glyph name: an icon set is an extension's check.
	if err := applyOperation(t, "icon", &ir.FieldDef{}, "Any Glyph"); err != nil {
		t.Fatalf("core rejected an icon name: %v", err)
	}
	for _, test := range []struct {
		op   *ir.FieldDef
		args []any
		want string
	}{
		{op, []any{"box"}, "operation getOrder has more than one @icon decorator"},
		{&ir.FieldDef{}, nil, "@icon takes exactly one string argument"},
		{&ir.FieldDef{}, []any{true}, "@icon takes a string literal"},
		{&ir.FieldDef{}, []any{" receipt"}, "invalid @icon: icon name must not contain surrounding whitespace"},
	} {
		if err := applyOperation(t, "icon", test.op, test.args...); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("@icon%v: err = %v, want %q", test.args, err, test.want)
		}
	}
}
