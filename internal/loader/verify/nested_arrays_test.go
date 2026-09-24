package verify

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

var nestedRef = ir.TypeRef{Name: "Identity.UUID", IsArray: true, IsArrayOfArrays: true}

func nestedArrayErrors(schema *ir.Schema) []string {
	r := &Result{}
	checkArraysOfArrays(schema, r)
	msgs := make([]string, 0, len(r.Errors))
	for _, d := range r.Errors {
		msgs = append(msgs, d.Msg)
	}
	return msgs
}

// TestArraysOfArraysAcceptedOnDataFields: plain fields of a DB table, an
// API view, an API input and a General type, and @jsonField columns, accept
// T[][].
func TestArraysOfArraysAcceptedOnDataFields(t *testing.T) {
	for _, role := range []ir.Role{ir.RoleDBTable, ir.RoleAPIView, ir.RoleAPIInput, ir.RoleEmbeddedStruct} {
		schema := ir.NewSchema("svc", ir.SchemaKindDB)
		schema.Types["Grid"] = &ir.TypeDef{Name: "Grid", Role: role, Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
			{Name: "cells", TypeRef: nestedRef, Required: true},
			{Name: "stored", TypeRef: nestedRef, JsonField: true},
		}, Indexes: []ir.IndexDef{{Keys: []string{"id"}}}}
		if msgs := nestedArrayErrors(schema); len(msgs) != 0 {
			t.Errorf("role %s: %v", role, msgs)
		}
	}
}

func TestArraysOfArraysRejectedOnTypeFields(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(td *ir.TypeDef, fd *ir.FieldDef)
		want   string
	}{
		{"env config", func(td *ir.TypeDef, _ *ir.FieldDef) { td.EnvVars = true }, "Grid.cells: env config fields cannot be arrays of arrays"},
		{"hasMany", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.HasMany = true }, "Grid.cells: a relation (hasMany, manyToMany or relation) cannot be an array of arrays"},
		{"manyToMany", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.ManyToMany = true }, "Grid.cells: a relation (hasMany, manyToMany or relation) cannot be an array of arrays"},
		{"relation", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.Relation = &ir.RelationDef{} }, "Grid.cells: a relation (hasMany, manyToMany or relation) cannot be an array of arrays"},
		{"key", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.Key = true }, "Grid.cells: an indexed field (@key, @unique or @searchField) cannot be an array of arrays"},
		{"unique", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.Unique = true }, "Grid.cells: an indexed field (@key, @unique or @searchField) cannot be an array of arrays"},
		{"searchField", func(_ *ir.TypeDef, fd *ir.FieldDef) { fd.SearchField = true }, "Grid.cells: an indexed field (@key, @unique or @searchField) cannot be an array of arrays"},
		{"index key", func(td *ir.TypeDef, _ *ir.FieldDef) {
			td.Indexes = []ir.IndexDef{{Keys: []string{"id", "cells"}, Unique: true}}
		}, `Grid: @index key "cells" is an array of arrays, which cannot be an index column`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("svc", ir.SchemaKindDB)
			cells := &ir.FieldDef{Name: "cells", TypeRef: nestedRef, Required: true}
			td := &ir.TypeDef{Name: "Grid", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true},
				cells,
			}}
			schema.Types[td.Name] = td
			tc.mutate(td, cells)
			msgs := nestedArrayErrors(schema)
			if len(msgs) != 1 || msgs[0] != tc.want {
				t.Fatalf("errors = %q, want [%q]", msgs, tc.want)
			}
		})
	}
}

// TestArraysOfArraysOperationArguments: a request body carries T[][]; a
// query or path parameter, or any argument of an operation that has no body,
// does not. The response accepts it.
func TestArraysOfArraysOperationArguments(t *testing.T) {
	cases := []struct {
		name string
		op   ir.FieldDef
		want string
	}{
		{"body argument of a POST", ir.FieldDef{HTTPMethod: "POST", RestPath: "grids"}, ""},
		{"body argument of a PATCH with a path id", ir.FieldDef{HTTPMethod: "patch", RestPath: "grids/{id}"}, ""},
		{"query parameter", ir.FieldDef{HTTPMethod: "POST"}, `GridOps.save: query parameter "cells" cannot be an array of arrays`},
		{"operation-wide query mapping", ir.FieldDef{HTTPMethod: "POST", ParamType: "query"}, `GridOps.save: query parameter "cells" cannot be an array of arrays`},
		{"path parameter", ir.FieldDef{HTTPMethod: "PUT", RestPath: "grids/{cells}"}, `GridOps.save: path parameter "cells" cannot be an array of arrays`},
		{"GET argument", ir.FieldDef{HTTPMethod: "GET", RestPath: "grids"}, `GridOps.save: argument "cells" is an array of arrays, which only a request body carries`},
		{"no method", ir.FieldDef{}, `GridOps.save: argument "cells" is an array of arrays, which only a request body carries`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := tc.op
			op.Name = "save"
			op.TypeRef = nestedRef
			arg := &ir.ArgumentDef{Name: "cells", TypeRef: nestedRef, Required: true}
			if tc.name == "query parameter" {
				arg.IsQuery = true
			}
			op.Arguments = []*ir.ArgumentDef{
				{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				arg,
			}
			schema := ir.NewSchema("svc", ir.SchemaKindAPI)
			schema.OperationSets = []*ir.OperationSet{{Name: "GridOps", Operations: []*ir.FieldDef{&op}}}
			msgs := nestedArrayErrors(schema)
			if tc.want == "" {
				if len(msgs) != 0 {
					t.Fatalf("errors = %q, want none", msgs)
				}
				return
			}
			if len(msgs) != 1 || !strings.HasPrefix(msgs[0], tc.want) {
				t.Fatalf("errors = %q, want one starting %q", msgs, tc.want)
			}
		})
	}
}

// TestArraysOfArraysProjection: a projection cannot declare a T[][] column,
// read one through @column, join on one or filter on one.
func TestArraysOfArraysProjection(t *testing.T) {
	withGrid := func(schema *ir.Schema) {
		schema.Types["Preference"].Fields = append(schema.Types["Preference"].Fields,
			&ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "Identity.Name", IsArray: true, IsArrayOfArrays: true}, Required: true})
		schema.Types["Channel"].Fields = append(schema.Types["Channel"].Fields,
			&ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "Identity.Name", IsArray: true, IsArrayOfArrays: true}, Required: true})
	}
	cases := []struct {
		name   string
		mutate func(schema *ir.Schema, td *ir.TypeDef)
		want   string
	}{
		{"declared column", func(schema *ir.Schema, td *ir.TypeDef) {
			withGrid(schema)
			td.Fields = append(td.Fields, &ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "Identity.Name", IsArray: true, IsArrayOfArrays: true}, Required: true})
		}, "AppPreference.grid: projection columns cannot be arrays of arrays"},
		{"source column", func(schema *ir.Schema, td *ir.TypeDef) {
			withGrid(schema)
			td.Fields = append(td.Fields, &ir.FieldDef{Name: "grid", TypeRef: ir.TypeRef{Name: "Identity.Name", IsArray: true}, Required: true})
		}, `AppPreference.grid: @column "base.grid": Preference.grid is an array of arrays, which a projection cannot read`},
		{"join key", func(schema *ir.Schema, td *ir.TypeDef) {
			withGrid(schema)
			td.Projection.Joins[0].On = append(td.Projection.Joins[0].On, &ir.ProjectionJoinKey{Left: "channel.grid", Right: "base.grid"})
		}, `AppPreference: @join channel on "channel.grid": Channel.grid is an array of arrays, which a projection cannot read`},
		{"row rule", func(schema *ir.Schema, td *ir.TypeDef) {
			withGrid(schema)
			td.Projection.Predicates = append(td.Projection.Predicates, &ir.ProjectionPredicate{Column: "base.grid", NotNull: true})
		}, `AppPreference: @projection where[1] column "base.grid": Preference.grid is an array of arrays, which a projection cannot read`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgs := projectionErrors(t, projectionSchema(tc.mutate))
			if !containsMessage(msgs, tc.want) {
				t.Fatalf("errors = %q, want %q", msgs, tc.want)
			}
		})
	}
	if msgs := projectionErrors(t, projectionSchema(func(schema *ir.Schema, _ *ir.TypeDef) { withGrid(schema) })); len(msgs) != 0 {
		t.Fatalf("a table with an unread T[][] column: %q", msgs)
	}
}

// TestArraysOfArraysSourceProjection: an @source view keeps the list depth
// of its source field.
func TestArraysOfArraysSourceProjection(t *testing.T) {
	source := ir.TypeRef{Name: "Identity.Name", IsArray: true, IsArrayOfArrays: true}
	if !typeRefsCompatible(nil, Input{}, source, source) {
		t.Fatal("T[][] view of a T[][] source is incompatible")
	}
	flat := ir.TypeRef{Name: "Identity.Name", IsArray: true}
	if typeRefsCompatible(nil, Input{}, flat, source) || typeRefsCompatible(nil, Input{}, source, flat) {
		t.Fatal("T[] and T[][] are compatible")
	}
	if got := typeRefString(source); got != "Identity.Name[][]" {
		t.Fatalf("typeRefString = %q", got)
	}
}
