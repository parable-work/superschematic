package nestedguard

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func nestedSchema(name string) *ir.Schema {
	schema := ir.NewSchema(name, ir.SchemaKindGeneral)
	schema.Types["Grid"] = &ir.TypeDef{Name: "Grid", Role: ir.RoleEmbeddedStruct, Fields: []*ir.FieldDef{
		{Name: "cells", TypeRef: ir.TypeRef{Name: "string", IsArray: true, IsArrayOfArrays: true}},
	}}
	return schema
}

func TestCheck(t *testing.T) {
	flat := ir.NewSchema("flat", ir.SchemaKindGeneral)
	if err := Check("typegen", flat); err != nil {
		t.Fatalf("flat schema: %v", err)
	}
	var none *ir.Schema
	if err := Check("typegen", none); err != nil {
		t.Fatalf("nil schema: %v", err)
	}
	err := Check("typegen", nestedSchema("svc"))
	if err == nil || err.Error() != "typegen does not support arrays of arrays yet (Grid.cells)" {
		t.Fatalf("err = %v", err)
	}
}

func TestCheckWithDependencies(t *testing.T) {
	flat := ir.NewSchema("api", ir.SchemaKindAPI)
	deps := map[string]*ir.Schema{"b-db": nestedSchema("b-db"), "a-general": ir.NewSchema("a-general", ir.SchemaKindGeneral), "c-nil": nil}
	err := CheckWithDependencies("apigen", flat, deps)
	if err == nil || err.Error() != "apigen does not support arrays of arrays yet (b-db Grid.cells)" {
		t.Fatalf("err = %v", err)
	}
	if err := CheckWithDependencies("apigen", flat, nil); err != nil {
		t.Fatalf("no dependencies: %v", err)
	}
}
