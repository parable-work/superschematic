package apigen_test

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
)

// These tests load fixtures through the loader, which imports the registry,
// which imports apigen for *apigen.APIOutput; an in-package test would be an
// import cycle, so they run as an external test package.

var update = flag.Bool("update", false, "rewrite golden files")

const fixturesDir = "../../loader/tsreader/testdata/services"

func generateFixtureAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	return generateFixtureAPIWith(t, sessionauth.Provider{})
}

func generateFixtureAPIWith(t *testing.T, provider apigen.AuthProvider) *apigen.APIOutput {
	t.Helper()

	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	output, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       provider,
		SchemaName:     "fixture-api",
		ModulePath:     "example.com/schemas/api/fixture-api",
		TypesModule:    "example.com/schemas/types/go/fixture-api",
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if output == nil {
		t.Fatal("expected API output for fixture-api")
	}
	return output
}

// TestWriteAPIGoldenSessionProvider generates the API module for fixture-api
// with the core session provider and compares every emitted file against its
// golden copy: the auth half of context.go, middleware.go and routes.go comes
// from the generic session snippets and the OpenAPI document has no
// tenant header. The same module under the Parable provider is golden-tested
// in utils/parable-schematic/ext/auth. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenSessionProvider -update
func TestWriteAPIGoldenSessionProvider(t *testing.T) {
	checkFixtureAPIGolden(t, sessionauth.Provider{}, "fixture-api-session")
}

func checkFixtureAPIGolden(t *testing.T, provider apigen.AuthProvider, golden string) {
	t.Helper()
	output := generateFixtureAPIWith(t, provider)

	outDir := t.TempDir()
	repoRoot, err := filepath.Abs("../../../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	scalarLibPath := filepath.Join(repoRoot, "utils", "parable-scalars")
	distAPIDir := filepath.Join(repoRoot, "platform-schemas", "dist", "api", "fixture-api")
	if err := apigen.SetReplacePaths(output, scalarLibPath, distAPIDir); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	files := []string{
		"go.mod",
		"interfaces.go",
		"routes.go",
		"middleware.go",
		"openapi.go",
		"openapi.json",
		"index.go",
		"rapidoc.go",
		"errors.go",
		"response.go",
		"context.go",
		"constants.go",
	}

	entries, err := os.ReadDir(outDir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != len(files) {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("expected %d output files, got %d: %v", len(files), len(entries), names)
	}

	goldenDir := filepath.Join("testdata", "golden", golden)
	for _, name := range files {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}

		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatalf("create golden dir: %v", err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatalf("write golden %s: %v", name, err)
			}
			continue
		}

		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

func TestGeneratedRoutesValidateImplementationResponses(t *testing.T) {
	output := generateFixtureAPI(t)
	outDir := t.TempDir()

	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
	if err != nil {
		t.Fatalf("read generated routes: %v", err)
	}

	content := string(routes)
	if !strings.Contains(content, "Invalid response from implementation") {
		t.Fatal("generated handlers must reject invalid route-impl responses")
	}
	if !strings.Contains(content, "Validate output") {
		t.Fatal("generated handlers must run output validation")
	}
}

func TestGenerateFixtureAPIShape(t *testing.T) {
	output := generateFixtureAPI(t)

	if !output.IsPublic {
		t.Error("fixture-api is public")
	}
	if output.UpstreamSchema != "fixture-db" {
		t.Errorf("UpstreamSchema = %q, want fixture-db", output.UpstreamSchema)
	}
	if len(output.Endpoints) != 6 {
		t.Fatalf("expected 6 endpoints, got %d", len(output.Endpoints))
	}
	if len(output.Namespaces) != 2 || output.Namespaces[0] != "session" || output.Namespaces[1] != "tenant" {
		t.Fatalf("expected [session tenant] namespace, got %v", output.Namespaces)
	}
	if !output.HasAuth {
		t.Error("expected HasAuth for fixture-api")
	}
	if !output.HasEncryptedEndpoints {
		t.Error("expected HasEncryptedEndpoints for TenantMutations")
	}
	if output.UUIDTypeExpr != "types.IdentityUUID" {
		t.Errorf("UUIDTypeExpr = %q, want types.IdentityUUID", output.UUIDTypeExpr)
	}

	manualCount := 0
	for _, ep := range output.Endpoints {
		if ep.ManualRouteRegistration {
			manualCount++
		}
	}
	if manualCount != 1 {
		t.Errorf("expected 1 manual-route endpoint, got %d", manualCount)
	}

	foundAuthOnlyEndpoint := false
	for _, ep := range output.Endpoints {
		if ep.Namespace == "session" && ep.Name == "currentTenant" {
			foundAuthOnlyEndpoint = true
			if !ep.RequiresAuth {
				t.Error("method-level @auth-only endpoint should require auth")
			}
			if len(ep.RequiredPerms) != 0 {
				t.Errorf("auth-only endpoint RequiredPerms = %v, want none", ep.RequiredPerms)
			}
		}
	}
	if !foundAuthOnlyEndpoint {
		t.Error("expected currentTenant auth-only endpoint")
	}
}
