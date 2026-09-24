package ir

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestTypeRefArrayDepth(t *testing.T) {
	cases := []struct {
		ref  TypeRef
		want int
	}{
		{TypeRef{Name: "string"}, 0},
		{TypeRef{Name: "string", IsArray: true}, 1},
		{TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}, 2},
		{TypeRef{Name: "string", IsMap: true}, 0},
		{TypeRef{Name: "string", IsMap: true, IsArray: true}, 1},
	}
	for _, tc := range cases {
		if got := tc.ref.ArrayDepth(); got != tc.want {
			t.Errorf("%+v.ArrayDepth() = %d, want %d", tc.ref, got, tc.want)
		}
	}
}

// The key is omitempty and sits right after isArray, so IR written for a
// schema without arrays of arrays is byte-identical to what it was before
// the field existed.
func TestTypeRefArrayOfArraysWireForm(t *testing.T) {
	cases := []struct {
		ref      TypeRef
		wantJSON string
		wantYAML string
	}{
		{
			TypeRef{Name: "Identity.UUID", IsArray: true},
			`{"name":"Identity.UUID","isArray":true}`,
			"name: Identity.UUID\nisArray: true\n",
		},
		{
			TypeRef{Name: "Identity.UUID", IsArray: true, IsArrayOfArrays: true},
			`{"name":"Identity.UUID","isArray":true,"isArrayOfArrays":true}`,
			"name: Identity.UUID\nisArray: true\nisArrayOfArrays: true\n",
		},
		{
			TypeRef{Name: "Cell", IsArray: true, IsArrayOfArrays: true, ElemNonNull: true},
			`{"name":"Cell","isArray":true,"isArrayOfArrays":true,"elemNonNull":true}`,
			"name: Cell\nisArray: true\nisArrayOfArrays: true\nelemNonNull: true\n",
		},
	}
	for _, tc := range cases {
		gotJSON, err := json.Marshal(tc.ref)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotJSON) != tc.wantJSON {
			t.Errorf("JSON = %s, want %s", gotJSON, tc.wantJSON)
		}
		var fromJSON TypeRef
		if err := json.Unmarshal(gotJSON, &fromJSON); err != nil {
			t.Fatal(err)
		}
		if fromJSON != tc.ref {
			t.Errorf("JSON round trip = %+v, want %+v", fromJSON, tc.ref)
		}

		gotYAML, err := yaml.Marshal(tc.ref)
		if err != nil {
			t.Fatal(err)
		}
		if string(gotYAML) != tc.wantYAML {
			t.Errorf("YAML = %q, want %q", gotYAML, tc.wantYAML)
		}
		var fromYAML TypeRef
		if err := yaml.Unmarshal(gotYAML, &fromYAML); err != nil {
			t.Fatal(err)
		}
		if fromYAML != tc.ref {
			t.Errorf("YAML round trip = %+v, want %+v", fromYAML, tc.ref)
		}
	}
}

func TestValidateArrayOfArraysInvariants(t *testing.T) {
	s := newTestSchema()
	s.Types["Grid"] = &TypeDef{
		Name: "Grid",
		Role: RoleAPIView,
		Fields: []*FieldDef{
			{Name: "cells", TypeRef: TypeRef{Name: "Status", IsArray: true, IsArrayOfArrays: true}, Required: true},
		},
	}
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("valid T[][] field: %v", errs)
	}

	s.Types["Grid"].Fields[0].TypeRef = TypeRef{Name: "Status", IsArrayOfArrays: true}
	assertOneError(t, s.Validate(), "Grid.cells: isArrayOfArrays requires isArray")

	s.Types["Grid"].Fields[0].TypeRef = TypeRef{Name: "Status", IsArray: true, IsArrayOfArrays: true, IsMap: true}
	assertOneError(t, s.Validate(), "Grid.cells: a map value cannot be an array of arrays")

	s.Types["Grid"].Fields[0].TypeRef = TypeRef{Name: "Status", IsArray: true}
	s.OperationSets = []*OperationSet{{
		Name: "GridQueries",
		Operations: []*FieldDef{{
			Name:    "grid",
			TypeRef: TypeRef{Name: "Grid", IsArrayOfArrays: true},
			Arguments: []*ArgumentDef{
				{Name: "rows", TypeRef: TypeRef{Name: "Status", IsArrayOfArrays: true, IsArray: true, IsMap: true}},
			},
		}},
	}}
	errs := s.Validate()
	if len(errs) != 2 ||
		!strings.Contains(errs[0].Error(), "GridQueries.grid: isArrayOfArrays requires isArray") ||
		!strings.Contains(errs[1].Error(), `GridQueries.grid argument "rows": a map value cannot be an array of arrays`) {
		t.Fatalf("operation errors = %v", errs)
	}
}

func assertOneError(t *testing.T, errs []error, want string) {
	t.Helper()
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), want) {
		t.Fatalf("errors = %v, want one containing %q", errs, want)
	}
}

func TestFindArrayOfArrays(t *testing.T) {
	var nilSchema *Schema
	if where, found := nilSchema.FindArrayOfArrays(); found {
		t.Fatalf("nil schema: found %q", where)
	}

	s := newTestSchema()
	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name: "tags", TypeRef: TypeRef{Name: "string", IsArray: true},
	})
	if where, found := s.FindArrayOfArrays(); found {
		t.Fatalf("schema without T[][]: found %q", where)
	}

	nested := TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}
	s.OperationSets = []*OperationSet{{
		Name: "UserQueries",
		Operations: []*FieldDef{
			{Name: "list", TypeRef: TypeRef{Name: "User", IsArray: true}},
			{Name: "search", TypeRef: TypeRef{Name: "User"}, Arguments: []*ArgumentDef{{Name: "terms", TypeRef: nested}}},
			{Name: "matrix", TypeRef: nested},
		},
	}}
	assertFound(t, s, "UserQueries.search(terms)")

	s.Types["User"].Fields = append(s.Types["User"].Fields, &FieldDef{
		Name:      "scores",
		TypeRef:   TypeRef{Name: "number"},
		Arguments: []*ArgumentDef{{Name: "window", TypeRef: nested}},
	})
	assertFound(t, s, "User.scores(window)")

	// Types are visited in name order, before operation sets.
	s.Types["Board"] = &TypeDef{Name: "Board", Role: RoleAPIView, Fields: []*FieldDef{{Name: "cells", TypeRef: nested}}}
	assertFound(t, s, "Board.cells")

	s.Inputs["AInput"] = &TypeDef{Name: "AInput", Role: RoleAPIInput, Fields: []*FieldDef{{Name: "rows", TypeRef: nested}}}
	assertFound(t, s, "Board.cells")
}

func assertFound(t *testing.T, s *Schema, want string) {
	t.Helper()
	where, found := s.FindArrayOfArrays()
	if !found || where != want {
		t.Fatalf("FindArrayOfArrays() = %q, %v; want %q", where, found, want)
	}
}
