package servergen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The TypeScript behavior worker of D104's Pool: fixture-pool-api, whose
// @behaviorWorker AccountRechecks runs fixture-pool-db's Account's
// rechecks and the events they emit, built as a TypeScript API that serves
// Account's pick, an operation of the table as a whole, beside its row
// operations, and pool-ts-stack, which places it on the local target. Its
// table's behaviors emit events, whose handlers are the implementation's
// Workers methods, so its worker builds the API's Deps and Workers as a D80
// worker's does.
const (
	tsPoolStack  = "pool-ts-stack"
	tsPoolAPI    = "fixture-pool-api"
	tsPoolDB     = "fixture-pool-db"
	tsPoolWorker = "fixture-pool-api-account-rechecks"
)

// tsPoolOrder is the order a build-all builds the fixture in.
var tsPoolOrder = []string{tsPoolDB, tsPoolAPI, tsPoolStack}

// loadTypeScriptPoolFixture loads the fixture with the core registry and
// stacktest's fake target, the configs narrowed as
// loadTypeScriptSchedulesFixture narrows fixture-schedules': the database's
// TypeScript types and DDL, with the Go types a DB service always
// generates, and the API's TypeScript types and server.
func loadTypeScriptPoolFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsPoolOrder {
		root := tsBehaviorsServicesRoot
		if name == tsPoolStack {
			root = tsBehaviorsStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsPoolDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres", "sqlite"}},
	}
	f.configs[tsPoolAPI].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT"},
	}
	return f
}

// TestTypeScriptPoolBehaviorWorkerEntrypoint: the TypeScript worker of
// AccountRechecks (D104), fixture-pool-api-account-rechecks, builds the
// API's Deps and calls the implementation's createWorkers, as a D80
// worker's does, since its table's behaviors emit events whose handlers are
// the Workers' methods; then it opens its database's repositories and runs
// the queues the API's package builds for it (accountRechecksQueues:
// Account's deadline and event queues, the events handed to the workers)
// under the runtime's runWorker. Its main is held to a golden, and, but in
// -short mode, type-checks in the output root's workspace with the API's
// scaffolded implementation, whose createWorkers stubs the event's method.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptPoolBehaviorWorkerEntrypoint -update
func TestTypeScriptPoolBehaviorWorkerEntrypoint(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptPoolFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tsPoolOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsPoolStack, tsPoolWorker)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile} {
		files[filepath.Join("server", tsPoolStack, tsPoolWorker, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsBehaviorsGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"const fixturePoolApiDeps: fixturePoolApiApi.Deps = {",
		"fixturePoolApiImpl.createWorkers(fixturePoolApiDeps)",
		"fixturePoolApiApi.accountRechecksQueues(fixturePoolDbPool, orm, workers,",
		"createFixturePoolDbOrm(fixturePoolDbPool)",
		"runWorker(",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.ts has no %s:\n%s", want, main)
		}
	}
	if testing.Short() {
		return
	}

	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	testpaths.BuildRulesPackage(t, bun, paths)
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f = loadTypeScriptPoolFixture(t)
	f.build(t, realRoot, paths, tsPoolOrder...)
	realOut := filepath.Join(realRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = realOut
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, checked := range []string{servergen.ServerDir(realOut, tsPoolStack, tsPoolWorker), naming.Default().TypeScriptImplementationDir(realRoot, tsPoolAPI)} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = checked
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", checked, err, output)
		}
	}
}
