package tsgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSourceLineageDoesNotPublishWebDBTypes encodes PARABLE-970 for the real
// API package names: @source-style compile-time lineage (SourceRef present,
// web-db absent from schema.Imports) must not put web-db-types into
// package.json or re-export DB validators from the barrel.
func TestSourceLineageDoesNotPublishWebDBTypes(t *testing.T) {
	db := ir.NewSchema("web-db", ir.SchemaKindDB)
	db.Types["IngestToken"] = &ir.TypeDef{
		Name: "IngestToken",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
			{Name: "tokenHash", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
			{Name: "routingPlaintext", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true},
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

	for _, apiName := range []string{"web-api", "web-admin-api"} {
		t.Run(apiName, func(t *testing.T) {
			api := ir.NewSchema(apiName, ir.SchemaKindAPI)
			// Compile-time lineage only: SourceRef set, no schema.Imports entry
			// for web-db (matches tsreader @source / authDb behavior).
			api.Types["IngestTokenInfo"] = &ir.TypeDef{
				Name:   "IngestTokenInfo",
				Role:   ir.RoleAPIView,
				Source: &ir.SourceRef{Target: "web-db.IngestToken"},
				Fields: []*ir.FieldDef{
					{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true},
				},
			}

			output, err := Generate(api, Options{
				SchemaName: apiName,
				Dependencies: map[string]*ir.Schema{
					"web-db": db,
				},
				DependencyPackages: map[string]string{
					"web-db": naming.Default().NpmTypesPackage("web-db"),
				},
				Clock: codegen.FixedClock(time.Unix(0, 0).UTC()),
			})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}

			wantPkg := naming.Default().NpmTypesPackage(apiName)
			if output.PackageName != wantPkg {
				t.Fatalf("PackageName = %q, want %q", output.PackageName, wantPkg)
			}
			for _, dep := range output.PackageDependencies {
				if dep.Name == "@parable-platform/web-db-types" {
					t.Fatalf("PackageDependencies must omit web-db-types, got %#v", output.PackageDependencies)
				}
			}
			for _, imp := range output.ImportedTypes {
				if imp.ImportPackage == "@parable-platform/web-db-types" {
					t.Fatalf("ImportedTypes must omit web-db-types, got %#v", output.ImportedTypes)
				}
			}

			outDir := t.TempDir()
			if err := WriteTypes(output, outDir); err != nil {
				t.Fatalf("WriteTypes: %v", err)
			}

			pkgJSON, err := os.ReadFile(filepath.Join(outDir, "package.json"))
			if err != nil {
				t.Fatalf("read package.json: %v", err)
			}
			var pkg struct {
				Name         string            `json:"name"`
				Dependencies map[string]string `json:"dependencies"`
			}
			if err := json.Unmarshal(pkgJSON, &pkg); err != nil {
				t.Fatalf("parse package.json: %v\n%s", err, pkgJSON)
			}
			if pkg.Name != wantPkg {
				t.Fatalf("package.json name = %q, want %q", pkg.Name, wantPkg)
			}
			if _, ok := pkg.Dependencies["@parable-platform/web-db-types"]; ok {
				t.Fatalf("package.json must omit @parable-platform/web-db-types, got %#v", pkg.Dependencies)
			}

			indexTS, err := os.ReadFile(filepath.Join(outDir, "index.ts"))
			if err != nil {
				t.Fatalf("read index.ts: %v", err)
			}
			if strings.Contains(string(indexTS), "web-db-types") {
				t.Fatalf("barrel must not reference web-db-types:\n%s", indexTS)
			}
			if strings.Contains(string(indexTS), "web-db-types/validators") {
				t.Fatalf("barrel must not re-export web-db validators:\n%s", indexTS)
			}

			typesTS, err := os.ReadFile(filepath.Join(outDir, "types", "types.ts"))
			if err != nil {
				t.Fatalf("read types/types.ts: %v", err)
			}
			if strings.Contains(string(typesTS), "web-db-types") {
				t.Fatalf("types.ts must not alias from web-db-types:\n%s", typesTS)
			}
			if strings.Contains(string(typesTS), "tokenHash") || strings.Contains(string(typesTS), "routingPlaintext") {
				t.Fatalf("wire view must not emit secret DB fields:\n%s", typesTS)
			}
		})
	}
}
