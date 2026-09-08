package typegen

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
	db.Types["TenantConnector"] = &ir.TypeDef{
		Name: "TenantConnector",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "tenant", TypeRef: ir.TypeRef{Name: "Tenant"}, Required: true},
		},
	}
	db.Types["ApiToken"] = &ir.TypeDef{
		Name: "ApiToken",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "tokenHash", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
		},
	}

	api := ir.NewSchema("web-api", ir.SchemaKindAPI)
	api.Imports = []ir.Import{{
		Package: "@schemas/web-db",
		Types:   []string{"TenantConnector", "SoftDeletable"},
	}}
	// API-owned Tenant view must win over the nested web-db.Tenant relation.
	api.Types["Tenant"] = &ir.TypeDef{
		Name: "Tenant",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}
	api.Types["TenantConnectorHistoryView"] = &ir.TypeDef{
		Name:   "TenantConnectorHistoryView",
		Role:   ir.RoleAPIView,
		Source: &ir.SourceRef{Target: "web-db.TenantConnector"},
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
		},
	}

	resolved, err := resolveImports(api, Options{
		Dependencies: map[string]*ir.Schema{"web-db": db},
		ModulePath:   "example.com/schemas/types/go/web-api",
	})
	if err != nil {
		t.Fatalf("resolveImports: %v", err)
	}

	got := map[string]string{}
	for _, imp := range resolved.types {
		got[imp.Name] = imp.ImportAlias
	}

	if _, ok := got["TenantConnector"]; !ok {
		t.Fatalf("expected TenantConnector alias, got %#v", got)
	}
	if _, ok := got["Tenant"]; ok {
		t.Fatalf("local Tenant must not be aliased from web-db, got %#v", got)
	}
	if _, ok := got["SoftDeletable"]; ok {
		t.Fatalf("traits must not be aliased, got %#v", got)
	}
	if _, ok := got["ApiToken"]; ok {
		t.Fatalf("DB deps must stay named-only; ApiToken was not imported, got %#v", got)
	}
}
