package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/ormgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSessionProviderAPIDependsOnGenericRuntimeOnly generates the types, ORM
// and API modules for the fixture services into a temp tree mirroring the
// dist layout, wires the real scalar-lib runtime, and runs go build and go
// vet on the API module. It is also the W7 proof that the core session
// provider emits an API module whose dependency closure holds the generic
// http-runtime session package and none of the Parable runtime packages
// (docs/extension-model.md section 8.2). The Parable provider's module is
// compiled by every service that builds against platform-schemas/dist.
func TestSessionProviderAPIDependsOnGenericRuntimeOnly(t *testing.T) {
	apiDir := buildFixtureAPI(t, sessionauth.Provider{})

	list := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./...")
	list.Dir = apiDir
	out, err := list.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	deps := strings.Split(strings.TrimSpace(string(out)), "\n")
	runtimeModule := naming.Default().HTTPRuntimeGoModule
	sawSession := false
	for _, dep := range deps {
		if !strings.HasPrefix(dep, runtimeModule+"/") {
			continue
		}
		switch strings.TrimPrefix(dep, runtimeModule+"/") {
		case "session":
			sawSession = true
		case "authmw", "authz":
			t.Errorf("session-provider API depends on Parable runtime package %s", dep)
		}
		// requestctx is allowed: its logger, client-IP and CheckContext half is
		// generic and the core context.tmpl uses it; its authctx.go half is
		// Parable's and moves out with the W12 shim module.
	}
	if !sawSession {
		t.Errorf("session-provider API does not depend on %s/session; runtime deps: %v", runtimeModule, deps)
	}

	// The emitted source itself carries no Parable auth vocabulary. (The
	// fixture schema has its own Tenant table and TenantView type, so the
	// check names the Parable auth identifiers, not the word.)
	sources, err := filepath.Glob(filepath.Join(apiDir, "*.go"))
	if err != nil {
		t.Fatalf("glob generated sources: %v", err)
	}
	banned := []string{
		"runtimeauthz", "runtimeauthmw", "scalars.Permission", "Impersonat", "compat_web_api",
		"TenantCache", "TenantResolutionMiddleware", "TenantPermissionsMiddleware", "RequireTenantPermissions", "X-Tenant",
	}
	for _, path := range sources {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, banned := range banned {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s: session-provider output mentions %q", filepath.Base(path), banned)
			}
		}
	}
}

// buildFixtureAPI generates fixture-db types and ORM and the fixture-api
// types and API module with provider into a temp dist-shaped tree, runs go
// mod tidy, go build and go vet on the API module, and returns its directory.
func buildFixtureAPI(t *testing.T, provider apigen.AuthProvider) string {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	scalarLib, err := filepath.Abs("../../../../parable-scalars")
	if err != nil {
		t.Fatalf("resolve scalar-lib path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(scalarLib, "go")); err != nil {
		t.Skipf("scalar-lib runtime not available: %v", err)
	}
	// The generators derive the schema-ir, schema-runtime, http-runtime and
	// ptr replace paths from scalar-lib's parent, which was utils/psgen until
	// #6109 moved scalar-lib to utils/parable-scalars; the dist modules only
	// build because the consuming services carry their own replaces. This
	// tree builds the generated module standalone, so point the replaces at
	// the real directories.
	psgenRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve psgen root: %v", err)
	}
	relTo := func(from, target string) string {
		rel, err := filepath.Rel(from, target)
		if err != nil {
			t.Fatalf("relative path %s -> %s: %v", from, target, err)
		}
		return filepath.ToSlash(rel)
	}
	schemaIR := filepath.Join(psgenRoot, "schema-ir", "go")

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	dbTypesModule := "github.com/parable-platform/platform-schemas/types/go/fixture-db"
	apiTypesModule := "github.com/parable-platform/platform-schemas/types/go/fixture-api"

	tempRoot := t.TempDir()
	dbTypesDir := filepath.Join(tempRoot, "types", "go", "fixture-db")
	apiTypesDir := filepath.Join(tempRoot, "types", "go", "fixture-api")
	ormDir := filepath.Join(tempRoot, "orm", "fixture-db")
	apiDir := filepath.Join(tempRoot, "api", "fixture-api")

	dbTypesOutput, err := typegen.Generate(dbSchema, typegen.Options{
		SchemaName: "fixture-db",
		ModulePath: dbTypesModule,
		Clock:      fixedClock,
	})
	if err != nil {
		t.Fatalf("generate fixture-db types: %v", err)
	}
	if err := typegen.SetReplacePaths(dbTypesOutput, scalarLib, dbTypesDir); err != nil {
		t.Fatalf("set fixture-db types replace paths: %v", err)
	}
	dbTypesOutput.SchemaIRReplacePath = relTo(dbTypesDir, schemaIR)
	if err := typegen.WriteTypes(dbTypesOutput, dbTypesDir); err != nil {
		t.Fatalf("write fixture-db types: %v", err)
	}

	apiTypesOutput, err := typegen.Generate(apiSchema, typegen.Options{
		SchemaName: "fixture-api",
		ModulePath: apiTypesModule,
		Dependencies: map[string]*ir.Schema{
			"fixture-db": dbSchema,
		},
		DependencyModules: map[string]string{
			"fixture-db": dbTypesModule,
		},
		Clock: fixedClock,
	})
	if err != nil {
		t.Fatalf("generate fixture-api types: %v", err)
	}
	if err := typegen.SetReplacePaths(apiTypesOutput, scalarLib, apiTypesDir); err != nil {
		t.Fatalf("set fixture-api types replace paths: %v", err)
	}
	apiTypesOutput.SchemaIRReplacePath = relTo(apiTypesDir, schemaIR)
	if err := typegen.WriteTypes(apiTypesOutput, apiTypesDir); err != nil {
		t.Fatalf("write fixture-api types: %v", err)
	}

	ormOutput, err := ormgen.Generate(dbSchema, ormgen.Options{
		SchemaName:  "fixture-db",
		ModulePath:  "github.com/parable-platform/platform-schemas/orm/fixture-db",
		TypesModule: dbTypesModule,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if err := ormgen.SetReplacePaths(ormOutput, scalarLib, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	ormOutput.SchemaIRReplacePath = relTo(ormDir, schemaIR)
	if err := ormgen.WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	apiOutput, err := apigen.Generate(apiSchema, apigen.Options{
		Provider:       provider,
		SchemaName:     "fixture-api",
		ModulePath:     "github.com/parable-platform/platform-schemas/api/fixture-api",
		TypesModule:    apiTypesModule,
		IsPublic:       true,
		UpstreamSchema: "fixture-db",
		UpstreamIR:     dbSchema,
		Clock:          fixedClock,
	})
	if err != nil {
		t.Fatalf("generate api: %v", err)
	}
	if apiOutput == nil {
		t.Fatal("expected API output for fixture-api")
	}
	if err := apigen.SetReplacePaths(apiOutput, scalarLib, apiDir); err != nil {
		t.Fatalf("set api replace paths: %v", err)
	}
	apiOutput.SchemaIRReplacePath = relTo(apiDir, schemaIR)
	apiOutput.SchemaRuntimeReplacePath = relTo(apiDir, filepath.Join(psgenRoot, "schema-runtime", "go"))
	apiOutput.HTTPRuntimeReplacePath = relTo(apiDir, filepath.Join(psgenRoot, "http-runtime", "go"))
	apiOutput.PtrReplacePath = relTo(apiDir, filepath.Join(psgenRoot, "..", "..", "services", "pkg", "ptr"))
	if err := apigen.WriteAPI(apiOutput, apiDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = apiDir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}

	build := exec.Command("go", "build", "./...")
	build.Dir = apiDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Errorf("generated API module does not compile: %v\n%s", err, out)
	}

	vet := exec.Command("go", "vet", "./...")
	vet.Dir = apiDir
	if out, err := vet.CombinedOutput(); err != nil {
		t.Errorf("generated API module fails go vet: %v\n%s", err, out)
	}
	return apiDir
}
