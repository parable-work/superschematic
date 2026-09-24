package sqlgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestArraysOfArraysAreJSONB: every T[][] column of fixture-nested-arrays-db
// is JSONB, whatever T is (a primitive, an enum, a @jsonField type), keeps
// its nullability, and gets no array default.
func TestArraysOfArraysAreJSONB(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-db"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays-db: %v", err)
	}
	output, err := Generate(schema, Options{SchemaName: "fixture-nested-arrays-db"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	var board *Table
	for i := range output.Tables {
		if output.Tables[i].Name == "board" {
			board = &output.Tables[i]
		}
	}
	if board == nil {
		t.Fatalf("no board table in %+v", output.Tables)
	}
	columns := map[string]Column{}
	for _, col := range board.Columns {
		columns[col.Name] = col
	}
	for name, nullable := range map[string]bool{"labels": false, "states": false, "walls": false, "scores": true} {
		col, ok := columns[name]
		if !ok {
			t.Errorf("board has no %s column", name)
			continue
		}
		if col.Type != "JSONB" || col.Nullable != nullable || col.DefaultValue != "" {
			t.Errorf("%s = %+v, want JSONB, nullable %v, no default", name, col, nullable)
		}
	}
	if len(output.JoinTables) != 0 || len(board.ForeignKeys) != 0 {
		t.Errorf("arrays of arrays made relations: join tables %+v, foreign keys %+v", output.JoinTables, board.ForeignKeys)
	}
}

// TestColumnTypeByArrayDepth: a scalar list stays a native Postgres array
// and a list of lists of the same scalar is JSONB.
func TestColumnTypeByArrayDepth(t *testing.T) {
	scalars := map[string]string{"Identity.UUID": "UUID"}
	for _, tc := range []struct {
		typeRef ir.TypeRef
		want    string
	}{
		{ir.TypeRef{Name: "Identity.UUID"}, "UUID"},
		{ir.TypeRef{Name: "Identity.UUID", IsArray: true}, "UUID[]"},
		{ir.TypeRef{Name: "Identity.UUID", IsArray: true, IsArrayOfArrays: true}, "JSONB"},
		{ir.TypeRef{Name: "string", IsArray: true}, "TEXT[]"},
		{ir.TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}, "JSONB"},
	} {
		if got := columnType(&ir.FieldDef{Name: "f", TypeRef: tc.typeRef}, scalars); got != tc.want {
			t.Errorf("columnType(%+v) = %q, want %q", tc.typeRef, got, tc.want)
		}
	}
}

// TestArrayOfArraysOfTableTypeFails: a list of lists of a table type has no
// relational meaning and is refused with its own error, not the @hasMany
// hint a flat list of a table type gets.
func TestArrayOfArraysOfTableTypeFails(t *testing.T) {
	schema := ir.NewSchema("synthetic", ir.SchemaKindDB)
	schema.Types["A"] = &ir.TypeDef{
		Name: "A",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "grid", TypeRef: ir.TypeRef{Name: "B", IsArray: true, IsArrayOfArrays: true}, Required: true},
		},
	}
	schema.Types["B"] = &ir.TypeDef{
		Name:   "B",
		Role:   ir.RoleDBTable,
		Fields: []*ir.FieldDef{{Name: "name", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
	}

	_, err := Generate(schema, Options{SchemaName: "synthetic"})
	if err == nil || !strings.Contains(err.Error(), "A.grid is an array of arrays of table type B, which cannot be a relation") {
		t.Fatalf("err = %v", err)
	}
}
