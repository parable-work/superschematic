package tsreader

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

// TestLoadServiceGolden walks every fixture service and compares the produced
// IR against its golden JSON. Regenerate with: go test ./internal/loader/tsreader -run TestLoadServiceGolden -update
func TestLoadServiceGolden(t *testing.T) {
	services := []string{
		"fixture-db",
		"fixture-api",
		"fixture-general",
		"fixture-authdb-import",
		"fixture-json-config",
		"fixture-traits",
		"fixture-docs",
		"fixture-projection",
	}
	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			schema, _, err := LoadService(filepath.Join("testdata", "services", svc))
			if err != nil {
				t.Fatalf("LoadService(%s): %v", svc, err)
			}
			got, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join("testdata", "golden", svc+".golden.json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden file (run with -update): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("IR mismatch for %s; run with -update and review the diff\ngot:\n%s", svc, got)
			}
		})
	}
}

// TestReadServiceConfig pins the config-only read path sentinel emission
// uses to learn a sibling's name and kind without a full load.
func TestReadServiceConfig(t *testing.T) {
	cfg, err := ReadServiceConfig(filepath.Join("testdata", "services", "fixture-db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "fixture-db" {
		t.Errorf("Name = %q, want fixture-db", cfg.Name)
	}
	if cfg.Kind != "DB" {
		t.Errorf("Kind = %q, want DB", cfg.Kind)
	}
}

// TestAuthDBFromImportedSentinel covers authDb wired to a service sentinel
// imported from another service's generated service.generated.ts.
func TestAuthDBFromImportedSentinel(t *testing.T) {
	_, cfg, _, err := LoadServiceWithConfig(filepath.Join("testdata", "services", "fixture-authdb-import"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuthDB != "fixture-db" {
		t.Errorf("AuthDB = %q, want fixture-db", cfg.AuthDB)
	}
	if len(cfg.Dependencies) != 1 || cfg.Dependencies[0].Name != "fixture-db" {
		t.Errorf("Dependencies = %+v, want one entry for fixture-db", cfg.Dependencies)
	}
}

func TestMethodLevelAuthDecoratorSurvivesOperationSetDefault(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-api"))
	if err != nil {
		t.Fatal(err)
	}

	var currentTenantAuth bool
	found := false
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "currentTenant" {
				found = true
				currentTenantAuth = op.Auth
			}
		}
	}
	if !found {
		t.Fatal("currentTenant operation not found")
	}
	if !currentTenantAuth {
		t.Fatal("method-level @auth was not preserved")
	}
}

// TestLoadServiceRoundTrip ensures the produced IR survives a JSON round trip
// (marshal -> unmarshal -> marshal) without loss.
func TestLoadServiceRoundTrip(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	decoded := schema
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatal(err)
	}
	second, err := json.Marshal(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("IR does not round-trip through JSON")
	}
}

func TestProgramCacheMatchesPerServiceLoads(t *testing.T) {
	services := []string{
		"fixture-db",
		"fixture-api",
		"fixture-general",
		"fixture-authdb-import",
		"fixture-json-config",
		"fixture-traits",
		"fixture-docs",
		"fixture-projection",
	}
	serviceDirs := make([]string, 0, len(services))
	for _, svc := range services {
		serviceDirs = append(serviceDirs, filepath.Join("testdata", "services", svc))
	}
	cache := NewProgramCache(serviceDirs)
	t.Cleanup(cache.Close)

	for _, svc := range services {
		t.Run(svc, func(t *testing.T) {
			serviceDir := filepath.Join("testdata", "services", svc)
			want, _, err := LoadService(serviceDir)
			if err != nil {
				t.Fatalf("LoadService(%s): %v", svc, err)
			}
			got, _, _, err := LoadServiceWithConfig(serviceDir, WithProgramCache(cache))
			if err != nil {
				t.Fatalf("LoadServiceWithConfig(%s, shared): %v", svc, err)
			}

			wantJSON, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			gotJSON, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotJSON) != string(wantJSON) {
				t.Fatalf("shared program IR differs from per-service load")
			}
		})
	}
}
