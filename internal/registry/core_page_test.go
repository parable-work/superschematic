package registry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// @paginated from @superschematic/api, on an operation of an API schema:
// its argument is optional, Args holds its shape, and Apply writes
// FieldDef.Page with both limits filled in, once per operation.
func TestPaginatedDecorator(t *testing.T) {
	reg := New(naming.Naming{})
	spec, ok := reg.Decorator("paginated", TargetOperation)
	if !ok || !spec.DeclaredIn(pkgAPI) || !spec.AllowsKind(string(ir.SchemaKindAPI)) || spec.AllowsKind(string(ir.SchemaKindGeneral)) || spec.Args == nil {
		t.Fatalf("@paginated spec = %+v", spec)
	}

	for _, tc := range []struct {
		arg  string // "" for @paginated()
		want ir.OperationPage
	}{
		{"", ir.OperationPage{DefaultLimit: 20, MaxLimit: 100}},
		{`{}`, ir.OperationPage{DefaultLimit: 20, MaxLimit: 100}},
		{`{"maxLimit": 10}`, ir.OperationPage{DefaultLimit: 10, MaxLimit: 10}},
		{`{"defaultLimit": 50}`, ir.OperationPage{DefaultLimit: 50, MaxLimit: 100}},
		{`{"defaultLimit": 2, "maxLimit": 5}`, ir.OperationPage{DefaultLimit: 2, MaxLimit: 5}},
		{`{"defaultLimit": 1000, "maxLimit": 1000}`, ir.OperationPage{DefaultLimit: 1000, MaxLimit: 1000}},
	} {
		var args []any
		if tc.arg != "" {
			var value any
			if err := json.Unmarshal([]byte(tc.arg), &value); err != nil {
				t.Fatal(err)
			}
			args = []any{value}
		}
		if err := spec.ValidateArgs(args); err != nil {
			t.Fatalf("ValidateArgs(%s): %v", tc.arg, err)
		}
		op := &ir.FieldDef{Name: "listItems"}
		if err := spec.Apply(Node{Field: op}, args, Site{}); err != nil {
			t.Fatalf("Apply(%s): %v", tc.arg, err)
		}
		if op.Page == nil || *op.Page != tc.want {
			t.Errorf("@paginated(%s) Page = %+v, want %+v", tc.arg, op.Page, tc.want)
		}
	}

	op := &ir.FieldDef{Name: "listItems"}
	if err := spec.Apply(Node{Field: op}, nil, Site{}); err != nil {
		t.Fatal(err)
	}
	if err := spec.Apply(Node{Field: op}, nil, Site{}); err == nil || err.Error() != "operation listItems has more than one @paginated decorator" {
		t.Fatalf("second @paginated: %v", err)
	}

	err := spec.Apply(Node{Field: &ir.FieldDef{Name: "listItems"}}, []any{map[string]any{"defaultLimit": 50.0, "maxLimit": 10.0}}, Site{})
	var argErr *ArgError
	if err == nil || err.Error() != "@paginated defaultLimit 50 is more than its maxLimit 10" || !asArgError(err, &argErr) || argErr.Index != 0 {
		t.Fatalf("a default above the max: %v", err)
	}

	for _, bad := range []string{
		`{"maxLimit": 0}`,
		`{"maxLimit": 1001}`,
		`{"defaultLimit": 0}`,
		`{"defaultLimit": 2.5}`,
		`{"maxLimit": "10"}`,
		`{"pageSize": 10}`,
		`null`,
		`10`,
	} {
		var value any
		if err := json.Unmarshal([]byte(bad), &value); err != nil {
			t.Fatal(err)
		}
		if err := spec.ValidateArgs([]any{value}); err == nil || !strings.HasPrefix(err.Error(), "@paginated argument:") {
			t.Errorf("ValidateArgs(%s) = %v", bad, err)
		}
	}
	if err := spec.ValidateArgs([]any{map[string]any{}, map[string]any{}}); err == nil || err.Error() != "@paginated takes at most one argument" {
		t.Errorf("two arguments: %v", err)
	}
}

func asArgError(err error, target **ArgError) bool {
	argErr, ok := err.(*ArgError)
	if ok {
		*target = argErr
	}
	return ok
}
