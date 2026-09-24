package registry

import (
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func projectionSpec(t *testing.T, name string, target DecoratorTarget) DecoratorSpec {
	t.Helper()
	spec, ok := New(naming.Default()).Decorator(name, target)
	if !ok {
		t.Fatalf("@%s is not registered for %s", name, target)
	}
	return spec
}

// TestProjectionDecoratorsAreCoreDBDecoratorsWithTypeArguments: @projection
// and @join take one type argument each and @column none; all three come
// from @superschematic/db and are DB-only.
func TestProjectionDecoratorsAreCoreDBDecoratorsWithTypeArguments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		target   DecoratorTarget
		typeArgs int
	}{
		{"projection", TargetType, 1},
		{"join", TargetType, 1},
		{"column", TargetField, 0},
	} {
		spec := projectionSpec(t, tc.name, tc.target)
		if spec.TypeArgs() != tc.typeArgs || !spec.DeclaredIn(pkgDB) || spec.Extension != "" {
			t.Errorf("@%s: typeArgs %d, packages %v, extension %q", tc.name, spec.TypeArgs(), spec.Packages, spec.Extension)
		}
		if !spec.AllowsKind("DB") || spec.AllowsKind("API") || spec.AllowsKind("General") {
			t.Errorf("@%s must be DB-only, kinds %v", tc.name, spec.Kinds)
		}
	}
}

// TestJoinAboveProjectionKeepsTheJoin: decorators apply in source order, so
// a @join written above the @projection lands on a placeholder the
// @projection completes rather than replaces.
func TestJoinAboveProjectionKeepsTheJoin(t *testing.T) {
	td := &ir.TypeDef{Name: "View", Role: ir.RoleDBTable}
	join := projectionSpec(t, "join", TargetType)
	projection := projectionSpec(t, "projection", TargetType)
	if err := join.Apply(Node{Type: td}, []any{"Channel", "channel", map[string]any{"channel.id": "base.channel"}, "left"}, Site{}); err != nil {
		t.Fatal(err)
	}
	opts := map[string]any{"pool": "app", "name": "view", "migration": "20260902120000"}
	if err := projection.Apply(Node{Type: td}, []any{"Preference", opts}, Site{}); err != nil {
		t.Fatal(err)
	}
	if td.Role != ir.RoleProjection || td.Projection == nil || td.Projection.Source != "Preference" {
		t.Fatalf("projection not applied: role %s, %+v", td.Role, td.Projection)
	}
	if len(td.Projection.Joins) != 1 || td.Projection.Joins[0].Alias != "channel" || td.Projection.Joins[0].Kind != "left" {
		t.Fatalf("joins = %+v, want the join declared above", td.Projection.Joins)
	}
	err := projection.Apply(Node{Type: td}, []any{"Preference", opts}, Site{})
	if err == nil || !strings.Contains(err.Error(), "one @projection") {
		t.Fatalf("second @projection: %v", err)
	}
}

// TestProjectionOptionErrorsPointAtTheOptions: shape errors in the options
// object are ArgErrors on the value argument (index 1, after the resolved
// type argument), so the frontend points the diagnostic at the object.
func TestProjectionOptionErrorsPointAtTheOptions(t *testing.T) {
	projection := projectionSpec(t, "projection", TargetType)
	for _, tc := range []struct {
		name string
		opts map[string]any
		want string
	}{
		{"unknown key", map[string]any{"pool": "app", "name": "v", "migration": "1", "scope": "x"}, `unknown key "scope"`},
		{"missing name", map[string]any{"pool": "app", "migration": "1"}, "requires a non-empty name"},
		{"where is not a list", map[string]any{"pool": "app", "name": "v", "migration": "1", "where": "x"}, "where must be an array"},
		{"binding without a setting", map[string]any{"pool": "app", "name": "v", "migration": "1", "where": []any{map[string]any{"column": "base.a"}}}, "requires column and setting"},
		{"anyOf with a guard inside", map[string]any{"pool": "app", "name": "v", "migration": "1", "where": []any{map[string]any{"anyOf": []any{
			map[string]any{"column": "base.a", "setting": "app.a", "when": map[string]any{"column": "base.b", "equals": "x"}},
			map[string]any{"column": "base.b", "setting": "app.b"},
		}}}}, "anyOf entries carry only column, setting and optional"},
		{"isNull false", map[string]any{"pool": "app", "name": "v", "migration": "1", "where": []any{map[string]any{"column": "base.a", "isNull": false}}}, "must be the literal true"},
		{"equals an object", map[string]any{"pool": "app", "name": "v", "migration": "1", "where": []any{map[string]any{"column": "base.a", "equals": map[string]any{}}}}, "string, number or boolean literal"},
		{"collapse without by", map[string]any{"pool": "app", "name": "v", "migration": "1", "collapse": map[string]any{}}, "collapse requires by"},
		{"order without column", map[string]any{"pool": "app", "name": "v", "migration": "1", "collapse": map[string]any{"by": []any{"base.a"}, "order": []any{map[string]any{"rank": []any{"x"}}}}}, "requires column"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := projection.Apply(Node{Type: &ir.TypeDef{Name: "View"}}, []any{"Preference", tc.opts}, Site{})
			var argErr *ArgError
			if !errors.As(err, &argErr) || argErr.Index != 1 || !strings.Contains(argErr.Msg, tc.want) {
				t.Fatalf("want an ArgError at index 1 containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestColumnDecorator(t *testing.T) {
	column := projectionSpec(t, "column", TargetField)
	fd := &ir.FieldDef{Name: "handle"}
	if err := column.Apply(Node{Field: fd}, []any{"channel.handle"}, Site{}); err != nil || fd.ProjectedFrom != "channel.handle" {
		t.Fatalf("string @column: %v, %+v", err, fd)
	}
	if err := column.Apply(Node{Field: fd}, []any{"base.handle"}, Site{}); err == nil || !strings.Contains(err.Error(), "one @column") {
		t.Fatalf("second @column: %v", err)
	}
	computed := &ir.FieldDef{Name: "key"}
	call := map[string]any{"function": "app.key_of", "args": []any{"base.id"}}
	if err := column.Apply(Node{Field: computed}, []any{call}, Site{}); err != nil ||
		computed.ProjectedFunction == nil || computed.ProjectedFunction.Function != "app.key_of" || computed.ProjectedFunction.Args[0] != "base.id" {
		t.Fatalf("function @column: %v, %+v", err, computed)
	}
	for _, bad := range []any{"", map[string]any{"sql": "TRUE"}, map[string]any{"function": "app.f", "args": []any{}}, 42.0} {
		if err := column.Apply(Node{Field: &ir.FieldDef{}}, []any{bad}, Site{}); err == nil {
			t.Errorf("@column(%v) accepted", bad)
		}
	}
}
