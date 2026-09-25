package typegen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestImportedUnionReExportsWrapper: a field typed with a union from a
// dependency is a union field (a nilable interface when optional), and the
// generated module re-exports the dependency's <Union>Wrapper next to the
// union alias and validates the field through it.
func TestImportedUnionReExportsWrapper(t *testing.T) {
	dependency := ir.NewSchema("contracts", ir.SchemaKindGeneral)
	dependency.Types["CreatedRevision"] = &ir.TypeDef{Name: "CreatedRevision", Role: ir.RoleEmbeddedStruct}
	dependency.Unions["RevisionRef"] = &ir.UnionDef{Name: "RevisionRef", Types: []string{"CreatedRevision"}}

	consumer := ir.NewSchema("consumer", ir.SchemaKindGeneral)
	consumer.Imports = []ir.Import{{Package: "@schemas/contracts", Types: []string{"RevisionRef"}}}
	consumer.Types["Event"] = &ir.TypeDef{
		Name: "Event",
		Role: ir.RoleEmbeddedStruct,
		Fields: []*ir.FieldDef{
			{Name: "revision", TypeRef: ir.TypeRef{Name: "RevisionRef"}},
		},
	}

	output, err := Generate(consumer, Options{
		SchemaName:        "consumer",
		ModulePath:        "example.com/consumer",
		Dependencies:      map[string]*ir.Schema{"contracts": dependency},
		DependencyModules: map[string]string{"contracts": "example.com/contracts"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var revision *FieldInfo
	for i := range output.Types {
		if output.Types[i].Name != "Event" {
			continue
		}
		for j := range output.Types[i].Fields {
			if output.Types[i].Fields[j].Name == "revision" {
				revision = &output.Types[i].Fields[j]
			}
		}
	}
	if revision == nil || !revision.IsUnion || revision.GoType != "RevisionRef" {
		t.Fatalf("optional imported union field = %+v, want a nilable union interface", revision)
	}
	outDir := t.TempDir()
	if err := WriteTypes(output, outDir); err != nil {
		t.Fatalf("WriteTypes: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "types.go"))
	if err != nil {
		t.Fatalf("read types.go: %v", err)
	}
	if !strings.Contains(string(source), "type RevisionRefWrapper = contracts.RevisionRefWrapper") {
		t.Fatalf("imported union wrapper alias missing:\n%s", source)
	}
	// Validate hands the member to the dependency's wrapper.
	if !strings.Contains(string(source), "(contracts.RevisionRefWrapper{Value: value}).Validate()") {
		t.Fatalf("imported union field not validated through its wrapper:\n%s", source)
	}
}

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
