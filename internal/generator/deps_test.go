package generator

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The deps fixtures: deps-db, and deps-orders and deps-catalog, two APIs
// that call each other. deps-orders takes its database from its one
// DB-kind dependency and has an @envVars type; deps-catalog names its
// database as authDb and has none.
const depsFixtures = "testdata/services"

const depsGoldenDir = "testdata/deps/golden"

// depsStep is one stage of one service's build.
type depsStep struct {
	service string
	stage   registry.BuildStage
}

// depsSteps are the steps the build plan orders the fixtures in (as
// buildplan.Steps does, which this package cannot import): deps-catalog
// calls deps-orders, which comes after it, so its API server builds once
// deps-orders' SDK has.
var depsSteps = []depsStep{
	{"deps-db", registry.StageAll},
	{"deps-catalog", registry.StageBase},
	{"deps-orders", registry.StageAll},
	{"deps-catalog", registry.StageServer},
}

// buildDeps runs the fixtures' generators by depsSteps into outputRoot,
// scaffolding the implementations under implementationRoot when it is set.
func buildDeps(t *testing.T, outputRoot, implementationRoot string, paths naming.LocalPaths) {
	t.Helper()
	configs := map[string]*schemaconfig.SchemaConfig{}
	schemas := map[string]*ir.Schema{}
	for _, name := range []string{"deps-db", "deps-catalog", "deps-orders"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	for _, step := range depsSteps {
		if _, err := Run(schemas[step.service], configs[step.service], Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			Stage:              step.stage,
			ImplementationRoot: implementationRoot,
		}); err != nil {
			t.Fatalf("run %s (%s): %v", step.service, step.stage, err)
		}
	}
}

// TestDerivedFieldsAndDepsGolden: each API's config holds a database
// field and a service field per call beside its settings, values-schema.json
// marks them derived with their variables, Deps holds the config, the ORM
// and a client per call, and the scaffold implements every method with a
// not-implemented error. Regenerate with:
//
//	go test ./internal/generator -run TestDerivedFieldsAndDepsGolden -update
func TestDerivedFieldsAndDepsGolden(t *testing.T) {
	outputRoot := t.TempDir()
	repoRoot := t.TempDir()
	buildDeps(t, outputRoot, repoRoot, naming.LocalPaths{})

	files := map[string]string{}
	for _, service := range []string{"deps-orders", "deps-catalog"} {
		for _, file := range []string{"config.go", "deps.go", "go.mod", "values-schema.json"} {
			files[filepath.Join("api", service, file)] = filepath.Join(APIDir(outputRoot, service), file)
		}
		files[filepath.Join("go", service, apigen.ImplementationFile)] = filepath.Join(naming.Default().GoImplementationDir(repoRoot, service), apigen.ImplementationFile)
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(depsGoldenDir, rel)
		if *updateNamingGolden {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

// TestDepsAndScaffoldCompile: the two generated APIs, each importing the
// other's SDK, compile, and so do their scaffolded implementations, whose
// New is the generated Constructor. A test in the implementation module
// sets the variables ir.DerivedVariables encodes a resolved environment's
// values in, loads them with LoadEnvConfig, builds the implementation from
// Deps and calls a method, which answers 501.
func TestDepsAndScaffoldCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	root := t.TempDir()
	outputRoot := filepath.Join(root, "schemas", "dist")
	buildDeps(t, outputRoot, root, paths)

	goCommand := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("go", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
		}
	}
	for _, service := range []string{"deps-orders", "deps-catalog"} {
		for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
			goCommand(APIDir(outputRoot, service), args...)
		}
	}

	names := naming.Default()
	implDir := filepath.Join(root, "go")
	var gomod strings.Builder
	gomod.WriteString("module example.com/impl\n\ngo 1.26.4\n")
	local := map[string]string{
		names.GoAPIModule("deps-orders"):    APIDir(outputRoot, "deps-orders"),
		names.GoAPIModule("deps-catalog"):   APIDir(outputRoot, "deps-catalog"),
		names.GoSDKModule("deps-orders"):    SDKDir(outputRoot, "go", "deps-orders"),
		names.GoSDKModule("deps-catalog"):   SDKDir(outputRoot, "go", "deps-catalog"),
		names.GoORMModule("deps-db"):        ORMDir(outputRoot, "deps-db"),
		names.GoTypesModule("deps-db"):      TypesDir(outputRoot, "go", "deps-db"),
		names.GoTypesModule("deps-orders"):  TypesDir(outputRoot, "go", "deps-orders"),
		names.GoTypesModule("deps-catalog"): TypesDir(outputRoot, "go", "deps-catalog"),
		names.HTTPRuntimeGoModule:           paths.HTTPRuntimeGo,
		names.SchemaRuntimeGoModule:         paths.SchemaRuntimeGo,
		names.SchemaIRGoModule:              paths.SchemaIR,
		names.ScalarGoModule:                paths.ScalarGo,
	}
	modules := make([]string, 0, len(local))
	for module := range local {
		modules = append(modules, module)
	}
	sort.Strings(modules)
	for _, module := range modules {
		if strings.HasPrefix(module, names.GoModuleRoot) {
			fmt.Fprintf(&gomod, "require %s v0.0.0-00010101000000-000000000000\n", module)
		}
		fmt.Fprintf(&gomod, "replace %s => %s\n", module, local[module])
	}
	if err := os.WriteFile(filepath.Join(implDir, "go.mod"), []byte(gomod.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	var setenv strings.Builder
	for field, value := range map[string]any{
		"DEPS_DB_DATABASE": ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
			Instance: "acme:us-central1:shop", Database: "deps_db", User: "deps-orders@acme.iam",
		}},
		"DEPS_CATALOG_SERVICE": ir.ServiceEndpoint{URL: "https://deps-catalog.run.app", Credential: &ir.ServiceCredential{
			Source: ir.CredentialGoogleIDToken, Audience: "https://deps-catalog.run.app",
			Headers: []string{ir.ServiceAuthorizationHeader, "X-Serverless-Authorization"},
		}},
	} {
		vars, err := ir.DerivedVariables(field, value)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vars {
			fmt.Fprintf(&setenv, "\tt.Setenv(%q, %q)\n", v.Name, v.Value)
		}
	}
	test := `package depsorders_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	api "example.com/schemas/api/deps-orders"
	impl "example.com/impl/deps-orders"
)

func TestTheImplementationBuildsFromDeps(t *testing.T) {
` + setenv.String() + `
	cfg, err := api.LoadEnvConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.FulfillmentRegion != "us" {
		t.Errorf("setting FULFILLMENT_REGION = %q, want its default us", cfg.FulfillmentRegion)
	}
	if db := cfg.DepsDbDatabase.CloudSQL; db == nil || db.Instance != "acme:us-central1:shop" || db.User != "deps-orders@acme.iam" {
		t.Errorf("DepsDbDatabase = %+v", cfg.DepsDbDatabase)
	}
	if svc := cfg.DepsCatalogService; svc.URL != "https://deps-catalog.run.app" || svc.Credential == nil || len(svc.Credential.Headers) != 2 {
		t.Errorf("DepsCatalogService = %+v", svc)
	}
	impls, err := impl.New(api.Deps{Config: *cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := impls.ValidateImplementations(); err != nil {
		t.Fatal(err)
	}
	_, err = impls.Order.GetOrder(context.Background(), "o-1")
	var appErr *api.AppError
	if !errors.As(err, &appErr) || appErr.HTTPStatus() != http.StatusNotImplemented {
		t.Errorf("GetOrder: %v, want a 501 not-implemented error", err)
	}
}
`
	if err := os.WriteFile(filepath.Join(implDir, "deps-orders", "deps_test.go"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}, {"test", "./..."}} {
		goCommand(implDir, args...)
	}
}

// TestTheScaffoldNeverOverwrites: a build writes the implementation only
// while its package is missing, and never touches a package that holds a
// Go file, whether the scaffold's own or another.
func TestTheScaffoldNeverOverwrites(t *testing.T) {
	outputRoot := t.TempDir()
	repoRoot := t.TempDir()
	buildDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	dir := naming.Default().GoImplementationDir(repoRoot, "deps-orders")
	file := filepath.Join(dir, apigen.ImplementationFile)
	edited := []byte("package depsorders\n\n// The engineer's code.\n")
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		t.Fatal(err)
	}

	buildDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	if got, err := os.ReadFile(file); err != nil || string(got) != string(edited) {
		t.Fatalf("a second build rewrote %s: %q, %v", file, got, err)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.go"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	buildDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a build scaffolded into a package that holds orders.go: %v", err)
	}

}

// TestDepsRefusesACalleeWithoutAGoSDK: Deps holds a Go SDK client per
// call, so a callee whose config the build has and that generates no Go
// SDK is refused, naming the switch to turn on.
func TestDepsRefusesACalleeWithoutAGoSDK(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, "deps-orders"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, catalogCfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, "deps-catalog"))
	if err != nil {
		t.Fatal(err)
	}
	db, dbCfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, "deps-db"))
	if err != nil {
		t.Fatal(err)
	}
	noSDK := *catalogCfg
	noSDK.Outputs = map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}, "api": map[string]any{"enabled": true}}
	schemas := map[string]*ir.Schema{"deps-catalog": catalog, "deps-db": db}
	configs := map[string]*schemaconfig.SchemaConfig{"deps-catalog": &noSDK, "deps-db": dbCfg}
	_, err = Run(schema, cfg, Options{
		OutputRoot:     t.TempDir(),
		LoadDependency: func(name string) (*ir.Schema, error) { return schemas[name], nil },
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			c, ok := configs[name]
			return c, ok
		},
	})
	if err == nil || !strings.Contains(err.Error(), "the Go server of deps-orders holds a client of deps-catalog in its Deps, and deps-catalog generates none; enable outputs.sdk.go in deps-catalog's config") {
		t.Fatalf("Run = %v, want the refusal of a callee without a Go SDK", err)
	}
}
