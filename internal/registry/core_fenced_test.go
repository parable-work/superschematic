package registry

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// applyFenced applies @fenced to a type as the TypeScript reader does: its
// one argument an evaluated object literal.
func applyFenced(t *testing.T, td *ir.TypeDef, args ...any) error {
	t.Helper()
	spec, ok := New(naming.Naming{}).Decorator("fenced", TargetType)
	if !ok {
		t.Fatal("@fenced is not registered")
	}
	return spec.Apply(Node{Type: td}, args, Site{})
}

// TestFencedIsASchemaPackageDecoratorOfADBSchema: @fenced comes from
// @superschematic/schema and stands in a DB schema alone, whose tables a
// fence's trigger watches (D119).
func TestFencedIsASchemaPackageDecoratorOfADBSchema(t *testing.T) {
	spec, ok := New(naming.Naming{}).Decorator("fenced", TargetType)
	if !ok {
		t.Fatal("@fenced is not registered")
	}
	if !reflect.DeepEqual(spec.Packages, []string{pkgSchema}) {
		t.Errorf("Packages = %v, want %s alone", spec.Packages, pkgSchema)
	}
	if !spec.AllowsKind(string(ir.SchemaKindDB)) {
		t.Error("@fenced is refused in a DB schema")
	}
	for _, kind := range []ir.SchemaKind{ir.SchemaKindAPI, ir.SchemaKindGeneral} {
		if spec.AllowsKind(string(kind)) {
			t.Errorf("@fenced is taken in a %s schema", kind)
		}
	}
	if got, want := spec.KindError(string(ir.SchemaKindAPI)), "@fenced is only allowed in DB schemas (this service is kind API)"; got != want {
		t.Errorf("KindError = %q, want %q", got, want)
	}
}

// TestFencedApplyReadsTheDeclaration: Apply records the field and the
// covered fields in the order written, a fence with no covers as its field
// alone, and each declaration of a class after the last (D119).
func TestFencedApplyReadsTheDeclaration(t *testing.T) {
	td := &ir.TypeDef{Name: "Job"}
	if err := applyFenced(t, td, map[string]any{"field": "version", "covers": []any{"priority", "label", "owner"}}); err != nil {
		t.Fatal(err)
	}
	if err := applyFenced(t, td, map[string]any{"field": "executionVersion", "covers": []any{"progress"}}); err != nil {
		t.Fatal(err)
	}
	want := []*ir.Fence{
		{Field: "version", Covers: []string{"priority", "label", "owner"}},
		{Field: "executionVersion", Covers: []string{"progress"}},
	}
	if !reflect.DeepEqual(td.Fences, want) {
		t.Errorf("Fences = %s", describeFences(td.Fences))
	}

	order := &ir.TypeDef{Name: "Order"}
	if err := applyFenced(t, order, map[string]any{"field": "version"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order.Fences, []*ir.Fence{{Field: "version"}}) || !order.Fences[0].CoversRow() {
		t.Errorf("Fences = %s", describeFences(order.Fences))
	}
}

func describeFences(fences []*ir.Fence) string {
	var b strings.Builder
	for _, f := range fences {
		b.WriteString(f.Field + "{" + strings.Join(f.Covers, ",") + "} ")
	}
	return b.String()
}

// TestFencedApplyRefusesAMalformedDeclaration: each shape Apply refuses,
// with the type and the field once it knows it, and what to write (D119).
// An empty covers is refused here, since the IR's JSON would leave it out.
func TestFencedApplyRefusesAMalformedDeclaration(t *testing.T) {
	valid := func() map[string]any {
		return map[string]any{"field": "version", "covers": []any{"label"}}
	}
	with := func(key string, value any) []any {
		obj := valid()
		obj[key] = value
		return []any{obj}
	}
	without := func(key string) []any {
		obj := valid()
		delete(obj, key)
		return []any{obj}
	}
	for _, tc := range []struct {
		name string
		args []any
		want string
	}{
		{"no argument", nil, "@fenced on Job takes one options object: @fenced<T>({ field, covers })"},
		{"two arguments", []any{valid(), valid()}, "@fenced on Job takes one options object"},
		{"a string", []any{"version"}, "@fenced on Job takes an options object: @fenced<T>({ field, covers })"},
		{"no field", without("field"), `@fenced on Job names no field; name the integer field every covered write advances, as field: "version"`},
		{"a field that is no name", with("field", 3.0), "@fenced on Job names no field"},
		{"an empty field", with("field", ""), "@fenced on Job names no field"},
		{"an unknown option", with("step", 2.0), `@fenced on Job.version has no option "step"; it takes field and covers`},
		{"covers that is no list", with("covers", "label"), `@fenced on Job.version: covers is a list of the fields of Job whose change the fence counts, as ["label", "owner"]; got label`},
		{"a null covers", with("covers", nil), "@fenced on Job.version: covers is a list of the fields of Job whose change the fence counts"},
		{"a covered field that is a number", with("covers", []any{"label", 2.0}), `@fenced on Job.version: covers lists 2; name a field of Job, as "label"`},
		{"an empty covers", with("covers", []any{}), "@fenced on Job.version: covers is empty, so the fence would count no write; list the fields whose change it counts, or leave covers out and it counts every update of the row"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := &ir.TypeDef{Name: "Job"}
			err := applyFenced(t, td, tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply = %v, want an error containing %q", err, tc.want)
			}
			if len(td.Fences) != 0 {
				t.Errorf("a refused declaration was recorded: %s", describeFences(td.Fences))
			}
			// An error of the options object points at it.
			var argErr *ArgError
			if len(tc.args) == 1 && (!errors.As(err, &argErr) || argErr.Index != 0) {
				t.Errorf("Apply = %T, want an ArgError at the options object", err)
			}
		})
	}
}
