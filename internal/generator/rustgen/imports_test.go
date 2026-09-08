package rustgen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func TestResolveImportsSkipsLocalCollisionsAndTraits(t *testing.T) {
	db := ir.NewSchema("web-db", ir.SchemaKindDB)
	db.Types["SoftDeletable"] = &ir.TypeDef{
		Name:    "SoftDeletable",
		Role:    ir.RoleTrait,
		IsTrait: true,
		Fields:  []*ir.FieldDef{{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "string"}}},
	}
	db.Types["Tenant"] = &ir.TypeDef{
		Name: "Tenant",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	db.Types["DeviceRecord"] = &ir.TypeDef{
		Name: "DeviceRecord",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}

	api := ir.NewSchema("web-api", ir.SchemaKindAPI)
	api.Imports = []ir.Import{{
		Package: "@parable-platform/web-db",
		Types:   []string{"Tenant", "DeviceRecord", "SoftDeletable"},
	}}
	api.Types["Tenant"] = &ir.TypeDef{
		Name: "Tenant",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}

	resolved, err := resolveImports(api, Options{
		Dependencies: map[string]*ir.Schema{"web-db": db},
	})
	if err != nil {
		t.Fatalf("resolveImports: %v", err)
	}

	got := map[string]string{}
	for _, imp := range resolved.types {
		got[imp.Name] = imp.ImportAlias
	}

	if _, ok := got["DeviceRecord"]; !ok {
		t.Fatalf("expected DeviceRecord alias, got %#v", got)
	}
	if _, ok := got["Tenant"]; ok {
		t.Fatalf("local Tenant must not be aliased from web-db, got %#v", got)
	}
	if _, ok := got["SoftDeletable"]; ok {
		t.Fatalf("traits must not be aliased, got %#v", got)
	}
}
