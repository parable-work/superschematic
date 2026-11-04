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

// The TypeScript behavior workers of D104's tables: fixture-agents-api,
// whose @behaviorWorker JobDeadlines runs fixture-agents-db's Job's
// deadlines (a lease's expiry, the end of a backoff) and SessionPresence
// each session's miss, built as a TypeScript API, and agents-ts-stack,
// which places it on the local target.
const (
	tsAgentsStack  = "agents-ts-stack"
	tsAgentsAPI    = "fixture-agents-api"
	tsAgentsDB     = "fixture-agents-db"
	tsAgentsWorker = "fixture-agents-api-job-deadlines"
)

// tsAgentsOrder is the order a build-all builds the fixture in.
var tsAgentsOrder = []string{tsAgentsDB, tsAgentsAPI, tsAgentsStack}

// loadTypeScriptAgentsFixture loads the fixture with the core registry and
// stacktest's fake target, the configs narrowed as
// loadTypeScriptTimersFixture narrows fixture-timers': the database's
// TypeScript types and DDL, with the Go types a DB service always
// generates, and the API's TypeScript types and server.
func loadTypeScriptAgentsFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsAgentsOrder {
		root := tsBehaviorsServicesRoot
		if name == tsAgentsStack {
			root = tsBehaviorsStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[tsAgentsDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "typescript": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres", "sqlite"}},
	}
	f.configs[tsAgentsAPI].Outputs = map[string]any{
		"types": map[string]any{"typescript": enabled},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT"},
	}
	return f
}

// TestTypeScriptAgentsBehaviorWorkerEntrypoint: the TypeScript worker of
// JobDeadlines (D104), fixture-agents-api-job-deadlines, opens its
// database's repositories and runs the queue the API's package builds for
// it (jobDeadlinesQueues: Job's deadline queue, which its Lease's expiry
// and its Retries' then arm) under the runtime's runWorker, as Timers'
// worker does; it builds no Deps and calls no createWorkers. Its main is
// held to a golden, and, but in -short mode, type-checks in the output
// root's workspace with the API's scaffolded implementation, which has no
// method for either worker. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptAgentsBehaviorWorkerEntrypoint -update
func TestTypeScriptAgentsBehaviorWorkerEntrypoint(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptAgentsFixture(t)
	f.build(t, repoRoot, tsBehaviorsFakePaths(repoRoot), tsAgentsOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, tsAgentsStack, tsAgentsWorker)
	files := map[string]string{}
	for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.DockerFile} {
		files[filepath.Join("server", tsAgentsStack, tsAgentsWorker, file)] = filepath.Join(dir, file)
	}
	compareGoldens(t, tsBehaviorsGoldenRoot, files)

	main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fixtureAgentsApiApi.jobDeadlinesQueues(fixtureAgentsDbPool, orm,", "createFixtureAgentsDbOrm(fixtureAgentsDbPool)", "runWorker("} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.ts has no %s:\n%s", want, main)
		}
	}
	for _, unwanted := range []string{".createWorkers(", "Deps = {", "sessionPresenceQueues"} {
		if strings.Contains(string(main), unwanted) {
			t.Errorf("main.ts has %s, which JobDeadlines' worker builds none of:\n%s", unwanted, main)
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
	f = loadTypeScriptAgentsFixture(t)
	f.build(t, realRoot, paths, tsAgentsOrder...)
	realOut := filepath.Join(realRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = realOut
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, checked := range []string{servergen.ServerDir(realOut, tsAgentsStack, tsAgentsWorker), naming.Default().TypeScriptImplementationDir(realRoot, tsAgentsAPI)} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = checked
		if output, err := check.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", checked, err, output)
		}
	}
}
