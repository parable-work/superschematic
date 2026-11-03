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

// The TypeScript behavior worker fixture (D103): fixture-timers-api, whose
// @behaviorWorker TicketTimers runs fixture-timers-db's Ticket's deadlines,
// built as a TypeScript API, and timers-ts-stack, which places it on the
// local target.
const (
	tsTimersStack  = "timers-ts-stack"
	tsTimersAPI    = "fixture-timers-api"
	tsTimersDB     = "fixture-timers-db"
	tsTimersWorker = "fixture-timers-api-ticket-timers"
)

// tsTimersOrder is the order a build-all builds the fixture in.
var tsTimersOrder = []string{tsTimersDB, tsTimersAPI, tsTimersStack}

// loadTypeScriptTimersFixture loads the fixture with the core registry and
// stacktest's fake target, the configs narrowed as
// loadTypeScriptBehaviorsFixture narrows fixture-workflow's: the
// database's TypeScript types and DDL, with the Go types a DB service
// always generates, and the API's TypeScript types and server.
func loadTypeScriptTimersFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsTimersOrder {
		root := tsBehaviorsServicesRoot
		if name == tsTimersStack {
			root = tsBehaviorsStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsTimersDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres", "sqlite"}},
	}
	f.configs[tsTimersAPI].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT"},
	}
	return f
}

// TestTypeScriptBehaviorWorkerEntrypoint: the TypeScript worker of a
// @behaviorWorker (D103), fixture-timers-api-ticket-timers, opens its
// database's repositories and runs each queue the API's package builds
// for it (ticketTimersQueues) under the runtime's runWorker, together; it
// builds no Deps and calls no createWorkers. Its package depends on the
// behaviors' core, which orm.ts imports, and its Dockerfile builds the
// core's WebAssembly module in a stage of its own, as the server's does.
// Its main is held to a golden, and, but in -short mode, type-checks in
// the output root's workspace with the API's scaffolded implementation,
// which has no method for the worker. An ordinary worker's main is held
// byte for byte by its own goldens. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptBehaviorWorkerEntrypoint -update
func TestTypeScriptBehaviorWorkerEntrypoint(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptTimersFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tsTimersOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsTimersStack, tsTimersWorker)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile} {
		files[filepath.Join("server", tsTimersStack, tsTimersWorker, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsBehaviorsGoldenRoot, files)

	read := func(file string) string {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	main := read(servergen.TypeScriptMainFile)
	for _, want := range []string{"fixtureTimersApiApi.ticketTimersQueues(fixtureTimersDbPool, orm,", "createFixtureTimersDbOrm(fixtureTimersDbPool)", "workerConcurrency(2)", "runWorker("} {
		if !strings.Contains(main, want) {
			t.Errorf("main.ts has no %s:\n%s", want, main)
		}
	}
	for _, unwanted := range []string{".createWorkers(", "Deps = {"} {
		if strings.Contains(main, unwanted) {
			t.Errorf("main.ts has %s, which a behavior worker builds none of:\n%s", unwanted, main)
		}
	}
	if pkg := read(servergen.TypeScriptPackageFile); !strings.Contains(pkg, `"@superschematic/rules": "*"`) {
		t.Errorf("the package does not depend on the behaviors' core:\n%s", pkg)
	}
	if dockerfile := read(servergen.DockerFile); !strings.Contains(dockerfile, "FROM rust:${RUST_VERSION}-trixie AS rules") {
		t.Errorf("the Dockerfile builds no core:\n%s", dockerfile)
	}
	if testing.Short() {
		return
	}

	// The worker type-checks over the real runtime, the core's package and
	// the generated packages, as bun installs them in the output root.
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
	f = loadTypeScriptTimersFixture(t)
	f.build(t, realRoot, paths, tsTimersOrder...)
	realOut := filepath.Join(realRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = realOut
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, checked := range []string{servergen.ServerDir(realOut, tsTimersStack, tsTimersWorker), naming.Default().TypeScriptImplementationDir(realRoot, tsTimersAPI)} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = checked
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", checked, err, output)
		}
	}
}
