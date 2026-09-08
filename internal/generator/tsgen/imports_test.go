package tsgen

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestResolveImportsNamedOnlyForDB: a single named DB import must not dump
// the entire secret-bearing catalog into the generated TypeScript package.
func TestResolveImportsNamedOnlyForDB(t *testing.T) {
	db := ir.NewSchema("web-db", ir.SchemaKindDB)
	db.Types["IngestToken"] = &ir.TypeDef{
		Name: "IngestToken",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "tokenHash", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
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
	db.Enums["TenantStatus"] = &ir.EnumDef{
		Name:   "TenantStatus",
		Values: []ir.EnumValueDef{{Name: "Active", SerializedAs: "active"}},
	}

	api := ir.NewSchema("web-api", ir.SchemaKindAPI)
	api.Imports = []ir.Import{{
		Package: "@parable-platform/web-db",
		Types:   []string{"IngestToken"},
	}}

	resolved, err := resolveImports(api, Options{
		Dependencies: map[string]*ir.Schema{"web-db": db},
	})
	if err != nil {
		t.Fatalf("resolveImports: %v", err)
	}

	got := map[string]bool{}
	for _, imp := range resolved.types {
		got[imp.Name] = true
	}
	if !got["IngestToken"] {
		t.Fatalf("expected IngestToken alias, got %#v", got)
	}
	if got["ApiToken"] {
		t.Fatalf("must not alias unimported ApiToken, got %#v", got)
	}
	if got["TenantStatus"] {
		t.Fatalf("must not alias unimported TenantStatus, got %#v", got)
	}
}

// TestResolveImportsExpandsGeneralCatalog: General dependencies keep the
// full catalog so API barrels still re-export error codes / shared enums
// that consumers import from web-api-types.
func TestResolveImportsExpandsGeneralCatalog(t *testing.T) {
	errors := ir.NewSchema("error-codes", ir.SchemaKindGeneral)
	errors.Enums["WebApiAuthError"] = &ir.EnumDef{
		Name:   "WebApiAuthError",
		Values: []ir.EnumValueDef{{Name: "NoAccount", SerializedAs: "no_account"}},
	}
	errors.Enums["WebApiArtifactError"] = &ir.EnumDef{
		Name:   "WebApiArtifactError",
		Values: []ir.EnumValueDef{{Name: "NotFound", SerializedAs: "not_found"}},
	}

	api := ir.NewSchema("web-api", ir.SchemaKindAPI)
	api.Imports = []ir.Import{{
		Package: "@parable-platform/error-codes",
		Types:   []string{"WebApiArtifactError"},
	}}

	resolved, err := resolveImports(api, Options{
		Dependencies: map[string]*ir.Schema{"error-codes": errors},
	})
	if err != nil {
		t.Fatalf("resolveImports: %v", err)
	}

	got := map[string]bool{}
	for _, imp := range resolved.types {
		got[imp.Name] = true
	}
	if !got["WebApiArtifactError"] {
		t.Fatalf("expected named import WebApiArtifactError, got %#v", got)
	}
	if !got["WebApiAuthError"] {
		t.Fatalf("General deps must expand catalog to include WebApiAuthError, got %#v", got)
	}
}
