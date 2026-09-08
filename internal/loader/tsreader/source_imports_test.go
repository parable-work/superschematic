package tsreader

import (
	"path/filepath"
	"testing"
)

// TestSourceImportIsCompileTimeOnly: @source(External) verifies via
// ExternalTypes / SourceRef but must not enter schema.Imports (codegen deps).
// schema.config dependencies are not required for the source package.
func TestSourceImportIsCompileTimeOnly(t *testing.T) {
	schema, cfg, _, err := LoadServiceWithConfig(filepath.Join("testdata", "services", "fixture-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	view := schema.Types["TenantView"]
	if view == nil || view.Source == nil {
		t.Fatal("TenantView.@source missing")
	}
	if view.Source.Target != "fixture-db.Tenant" {
		t.Fatalf("Source.Target = %q, want fixture-db.Tenant", view.Source.Target)
	}

	for _, imp := range schema.Imports {
		if imp.Package == "@parable-platform/fixture-db" {
			t.Fatalf("fixture-db must not appear in schema.Imports (codegen); got %+v", schema.Imports)
		}
	}

	for _, dep := range cfg.Dependencies {
		if dep.Name == "fixture-db" {
			t.Fatalf("fixture-api schema.config must not list fixture-db for @source-only; got %+v", cfg.Dependencies)
		}
	}
}
