package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAppDecorator: @app records an app in Schema.Apps with the extensions
// it names (D78), refuses a second class of one name, and refuses what an
// app's class may not hold.
func TestAppDecorator(t *testing.T) {
	spec, ok := New(naming.Naming{}).Decorator("app", TargetType)
	if !ok {
		t.Fatal("no core @app")
	}
	if !reflect.DeepEqual(spec.Kinds, []string{string(ir.SchemaKindAPI)}) || !reflect.DeepEqual(spec.Packages, []string{pkgAPI}) {
		t.Errorf("@app is for %v from %v, want API schemas from %s", spec.Kinds, spec.Packages, pkgAPI)
	}
	schema := ir.NewSchema("shop-stock", ir.SchemaKindAPI)
	apply := func(td *ir.TypeDef, args ...any) error {
		return spec.Apply(Node{Schema: schema, Type: td}, args, Site{})
	}
	if err := apply(&ir.TypeDef{Name: "Backoffice", Comment: "The staff's console."}, map[string]any{"extensions": []any{"topcoat"}}); err != nil {
		t.Fatal(err)
	}
	if err := apply(&ir.TypeDef{Name: "Kiosk"}); err != nil {
		t.Fatal(err)
	}
	want := []*ir.App{{Name: "Backoffice", Comment: "The staff's console.", Extensions: []string{"topcoat"}}, {Name: "Kiosk"}}
	if !reflect.DeepEqual(schema.Apps, want) {
		t.Errorf("Apps = %+v, want %+v", schema.Apps, want)
	}
	for _, tc := range []struct {
		td   *ir.TypeDef
		args []any
		want string
	}{
		{&ir.TypeDef{Name: "Backoffice"}, nil, "@app class Backoffice is declared twice"},
		{&ir.TypeDef{Name: "F", Fields: []*ir.FieldDef{{Name: "port"}}}, nil, "@app class F has fields"},
		{&ir.TypeDef{Name: "E", Extends: "Base"}, nil, "@app class E extends or implements another class"},
		{&ir.TypeDef{Name: "A"}, []any{map[string]any{}, map[string]any{}}, "@app takes one options object"},
		{&ir.TypeDef{Name: "A"}, []any{"topcoat"}, "@app takes an options object: { extensions }"},
		{&ir.TypeDef{Name: "A"}, []any{map[string]any{"extensions": "topcoat"}}, "@app extensions is a list"},
		{&ir.TypeDef{Name: "A"}, []any{map[string]any{"extensions": []any{1.0}}}, "@app extensions lists extensions by name"},
		{&ir.TypeDef{Name: "A"}, []any{map[string]any{"pages": true}}, `@app has no option "pages"`},
		{&ir.TypeDef{Name: "A"}, []any{map[string]any{"extensions": []any{""}}}, "@app A: names an extension with no name"},
	} {
		if err := apply(tc.td, tc.args...); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("@app on %s with %v = %v, want %q", tc.td.Name, tc.args, err, tc.want)
		}
	}
}
