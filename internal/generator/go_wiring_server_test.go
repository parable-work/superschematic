package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The Go server and workers of fixture-agent-runs-go-api, whose generated
// wiring builds the runtime over fixture-agent-runs-db (D190, D192, D187).
const (
	wiringStack  = "fixture-agent-runs-go-stack"
	wiringAPI    = "fixture-agent-runs-go-api"
	wiringDB     = "fixture-agent-runs-db"
	wiringServer = "fixture-agent-runs-go-api"
)

// wiringWorkers are the stack's worker deployables, by @worker class.
var wiringWorkers = map[string]string{
	"Notify":      wiringServer + "-notify",
	"Dispatch":    wiringServer + "-dispatch",
	"InboundRuns": wiringServer + "-inbound-runs",
	"IndexSearch": wiringServer + "-index-search",
}

// buildWiringStack builds fixture-agent-runs-db with its Go types,
// fixture-agent-runs-go-api and its stack, as build-all does, under
// repoRoot, and returns the output root.
func buildWiringStack(t *testing.T, repoRoot string) string {
	t.Helper()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for name, dir := range map[string]string{wiringDB: tsFixtures, wiringAPI: tsFixtures, wiringStack: depsFixtures} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	db := *configs[wiringDB]
	db.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	configs[wiringDB] = &db
	for _, service := range []string{wiringDB, wiringAPI, wiringStack} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot:     outputRoot,
			Paths:          testpaths.Local(t),
			Naming:         naming.Default(),
			Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			RepositoryRoot: repoRoot,
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
			LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
				if cfg, ok := configs[name]; ok {
					return cfg, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	return outputRoot
}

// TestGoServerWiresTheRuntime builds fixture-agent-runs-go-api and its
// stack: the server's main.go builds the API's Deps with the runtime wired
// (Wire, over NewWiring's options), establishes its routes' callers with
// the API package's Authenticator and serves its Routes beside its own;
// each worker of a queue the runtime handles runs the queue the API package
// builds for it and calls no NewWorkers; and every entrypoint compiles,
// over the scaffolded implementation.
func TestGoServerWiresTheRuntime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server's wiring in -short mode")
	}
	repoRoot := testpaths.TempDir(t)
	outputRoot := buildWiringStack(t, repoRoot)
	serverDir := servergen.ServerDir(outputRoot, wiringStack, wiringServer)
	main, err := os.ReadFile(filepath.Join(serverDir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fixtureAgentRunsGoApiWiring, err := fixtureagentrunsgoapiimpl.NewWiring(fixtureAgentRunsGoApiDeps)",
		"fixtureAgentRunsGoApiDeps, err = fixtureagentrunsgoapi.Wire(fixtureAgentRunsGoApiDeps, fixtureAgentRunsDb, fixtureAgentRunsGoApiWiring)",
		"fixtureAgentRunsGoApiAuthenticate := fixtureagentrunsgoapi.Authenticator(",
		"fixtureAgentRunsGoApiRouter.Use(fixtureAgentRunsGoApiAuthenticate)",
		"fixtureAgentRunsGoApiWired := fixtureagentrunsgoapi.Routes(",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.go lacks %s:\n%s", want, main)
		}
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	dirs := []string{serverDir}
	for class, deployable := range wiringWorkers {
		dir := servergen.ServerDir(outputRoot, wiringStack, deployable)
		worker, err := os.ReadFile(filepath.Join(dir, servergen.MainFile))
		if err != nil {
			t.Fatal(err)
		}
		want := "fixtureagentrunsgoapi." + class + "Queue(fixtureAgentRunsGoApiDeps, fixtureAgentRunsDb, fixtureAgentRunsGoApiWiring)"
		if !strings.Contains(string(worker), want) || strings.Contains(string(worker), ".NewWorkers(") {
			t.Errorf("the worker %s does not run the queue %s builds, or builds the implementation's workers:\n%s", class, want, worker)
		}
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		for _, args := range [][]string{{"mod", "tidy"}, {"vet", "."}} {
			goIn(t, dir, env, args...)
		}
	}
}
