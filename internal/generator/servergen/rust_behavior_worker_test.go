package servergen_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// rustBehaviorWorkerGoldenRoot holds the Rust entrypoint of
// fixture-timers-api's behavior worker TicketTimers (D103).
const rustBehaviorWorkerGoldenRoot = "testdata/golden/behavior-worker/rust"

// rustTimers is fixture-timers-api's Rust crate as a build of an output
// root makes it, with what a test that compiles it writes beside it.
type rustTimers struct {
	output *rustrestgen.APIOutput
	routes *apigen.APIOutput
	config *envgen.ConfigOutput
	api    *ir.Schema
	db     *ir.Schema
	reg    *registry.Registry
}

// rustTimersAPI is fixture-timers-api's Rust crate as a build of the
// output root out makes it: its Deps holding a pool of fixture-timers-db,
// whose Rust types it re-exports for its behavior worker's queue, and the
// layout and the crates of paths its behaviors run with; its EnvConfig
// holding the database's field.
func rustTimersAPI(t *testing.T, out string, paths naming.LocalPaths) rustTimers {
	t.Helper()
	const fixtures = "../../loader/tsreader/testdata/services"
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixtures, "fixture-timers-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	schema, err := loader.LoadService(filepath.Join(fixtures, "fixture-timers-api"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	routes, err := apigen.Generate(schema, apigen.Options{
		SchemaName:     schema.Name,
		UpstreamSchema: db.Name,
		UpstreamIR:     db,
		Provider:       sessionauth.Provider{},
		Clock:          clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	apiDir := generator.APIDir(out, schema.Name)
	output, err := rustrestgen.Generate(schema, routes, rustrestgen.Options{
		SchemaName:   schema.Name,
		Dependencies: map[string]*ir.Schema{db.Name: db},
		TypesCrate:   naming.Default().RustTypesCrate(schema.Name),
		TypesDir:     generator.TypesDir(out, "rust", schema.Name),
		OutputDir:    apiDir,
		Clock:        clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.Of(db, reg)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	behaviors := &rustrestgen.DepsBehaviors{Layout: string(encoded)}
	if behaviors.AdapterPath, err = naming.RelPath(apiDir, paths.RulesAdapterRust); err != nil {
		t.Fatal(err)
	}
	if behaviors.BehaviorsPath, err = naming.RelPath(apiDir, paths.RulesBehaviorsRust); err != nil {
		t.Fatal(err)
	}
	typesPath, err := naming.RelPath(apiDir, generator.TypesDir(out, "rust", db.Name))
	if err != nil {
		t.Fatal(err)
	}
	if err := output.SetDeps(rustrestgen.DepsInfo{
		Database:           db.Name,
		DatabaseTypesCrate: naming.Default().RustTypesCrate(db.Name),
		DatabaseTypesPath:  typesPath,
		Behaviors:          behaviors,
	}); err != nil {
		t.Fatal(err)
	}
	output.HasEnvConfig = true
	config := &envgen.ConfigOutput{
		SchemaName: schema.Name,
		EnvConfig:  true,
		Derived:    []envgen.DerivedField{{Key: "FIXTURE_TIMERS_DB_DATABASE", GoName: "FixtureTimersDbDatabase", Kind: ir.EdgeSQL, Service: db.Name}},
	}
	return rustTimers{output: output, routes: routes, config: config, api: schema, db: db, reg: reg}
}

// behaviorWorkerInput is the plan's input of TicketTimers' entrypoint in
// the stack timers-stack, under the output root out of the repository at
// repoRoot.
func behaviorWorkerInput(t *testing.T, repoRoot, out string, paths naming.LocalPaths, timers rustTimers) servergen.RustInput {
	t.Helper()
	impl, err := servergen.ReadRustImplementation(naming.Default(), timers.output.SchemaName, filepath.Join(repoRoot, "services", timers.output.SchemaName, "rust"))
	if err != nil {
		t.Fatal(err)
	}
	return servergen.RustInput{
		Stack:      "timers-stack",
		Server:     "fixture-timers-api-ticket-timers",
		Dir:        servergen.ServerDir(out, "timers-stack", "fixture-timers-api-ticket-timers"),
		OutputRoot: out,
		APIs: []servergen.RustAPIInput{{
			Output:         timers.output,
			Routes:         timers.routes,
			Config:         timers.config,
			Dir:            generator.APIDir(out, timers.output.SchemaName),
			Implementation: impl,
		}},
		Naming:         naming.Default(),
		RepositoryRoot: repoRoot,
		Paths:          paths,
		Worker:         &servergen.WorkerInput{Name: "TicketTimers"},
	}
}

// TestRustBehaviorWorkerEntrypoint: the entrypoint of a behavior worker
// (D103), fixture-timers-api's TicketTimers, opens a pool on the API's
// database and runs Ticket's deadline queue and its change queue, each with
// the handler the API crate's behaviors module holds, in a claim loop of
// the HTTP runtime's of its own, together; it
// builds no Deps, calls no create_workers and depends on no
// implementation, and its image's context takes in the Cargo workspace of
// the rules crates the API crate depends on by path. Its main.rs passes
// clippy against the API crate, the database's Rust types and the
// checkout's crates, unless in -short mode or without cargo. Regenerate
// with:
//
//	go test ./internal/generator/servergen -run TestRustBehaviorWorkerEntrypoint -update
func TestRustBehaviorWorkerEntrypoint(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustFakePaths(repoRoot)
	paths.RulesAdapterRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-adapter")
	paths.RulesBehaviorsRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-behaviors")
	dir := filepath.Join(repoRoot, "runtime", "engine", "rust")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[workspace]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	timers := rustTimersAPI(t, out, paths)
	in := behaviorWorkerInput(t, repoRoot, out, paths, timers)
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust: %v", err)
	}
	if s.Kind != "worker" || s.Worker == nil || s.Worker.Behavior == nil || s.Worker.Concurrency != 2 {
		t.Fatalf("planned %s with worker %+v, want TicketTimers a behavior worker of concurrency 2", s.Kind, s.Worker)
	}
	for _, d := range s.Dependencies {
		if strings.Contains(d.Name, "impl") {
			t.Errorf("the behavior worker depends on %s, an implementation it calls nothing of", d.Name)
		}
	}
	if s.Docker == nil {
		t.Fatalf("no Dockerfile: %s", s.NoDocker)
	}
	if err := servergen.WriteRust(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
		files[filepath.FromSlash(file)] = filepath.Join(in.Dir, filepath.FromSlash(file))
	}
	compareGoldens(t, rustBehaviorWorkerGoldenRoot, files)
	main, err := os.ReadFile(filepath.Join(in.Dir, filepath.FromSlash(servergen.RustMainFile)))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Worker.QueueNames(); got != "TicketDeadline, TicketChange" {
		t.Errorf("TicketTimers runs %s, want Ticket's deadline queue and then its change queue", got)
	}
	for _, want := range []string{
		"fixture_timers_api_api::behaviors::ticket_timers_deadlines(&fixture_timers_db_pool)?",
		"fixture_timers_api_api::behaviors::run_ticket_timers_deadline(&pool, claimed).await",
		"fixture_timers_api_api::behaviors::ticket_timers_changes(&fixture_timers_db_pool)?",
		"fixture_timers_api_api::behaviors::run_ticket_timers_change(&pool, claimed).await",
		"worker::run_worker(deadlines, run_deadlines, options, shutdown.clone()),",
		"worker::run_worker(changes, run_changes, options, shutdown.clone()),",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.rs does not contain %q:\n%s", want, main)
		}
	}
	for _, absent := range []string{"create_workers", "Deps {", "PostgresQueue"} {
		if strings.Contains(string(main), absent) {
			t.Errorf("main.rs holds %s, which a behavior worker has none of:\n%s", absent, main)
		}
	}
	ignore, err := os.ReadFile(filepath.Join(in.Dir, servergen.DockerIgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), "!runtime/engine/rust\n") {
		t.Errorf("the image's context does not take in the rules crates' workspace:\n%s", ignore)
	}

	if testing.Short() {
		return
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Log("cargo not available; main.rs is not compiled")
		return
	}
	checkRustBehaviorWorkerCompiles(t, cargo)
}

// checkRustBehaviorWorkerCompiles writes TicketTimers' entrypoint, the
// API crate it runs and the Rust types both read under a repository of
// their own with this checkout's runtimes, and lints the entrypoint with
// clippy, warnings denied.
func checkRustBehaviorWorkerCompiles(t *testing.T, cargo string) {
	t.Helper()
	paths := testpaths.Local(t)
	names := naming.Default()
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	timers := rustTimersAPI(t, out, paths)
	for _, service := range []struct {
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{{timers.db, nil}, {timers.api, map[string]*ir.Schema{timers.db.Name: timers.db}}} {
		crates := map[string]string{}
		for name := range service.deps {
			crates[name] = names.RustTypesCrate(name)
		}
		types, err := rustgen.Generate(service.schema, rustgen.Options{
			SchemaName:       service.schema.Name,
			Dependencies:     service.deps,
			DependencyCrates: crates,
			Registry:         timers.reg,
			Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
		if err != nil {
			t.Fatalf("rustgen.Generate %s: %v", service.schema.Name, err)
		}
		dir := generator.TypesDir(out, "rust", service.schema.Name)
		if err := rustgen.WriteTypes(types, dir); err != nil {
			t.Fatalf("rustgen.WriteTypes %s: %v", service.schema.Name, err)
		}
	}
	apiDir := generator.APIDir(out, timers.output.SchemaName)
	if err := rustrestgen.SetReplacePaths(timers.output, paths, apiDir); err != nil {
		t.Fatal(err)
	}
	if err := rustrestgen.WriteAPI(timers.output, apiDir); err != nil {
		t.Fatal(err)
	}
	if err := envgen.WriteRustConfig(timers.config, apiDir); err != nil {
		t.Fatal(err)
	}
	in := behaviorWorkerInput(t, repoRoot, out, paths, timers)
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust: %v", err)
	}
	if err := servergen.WriteRust(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	// The entrypoint is a member of the output root's workspace, which a
	// build writes; here it is a workspace of its own.
	appendManifest(t, in.Dir, "\n[workspace]\n\n"+testpaths.RustPatch(paths, names))
	cargoCommand(t, cargo, in.Dir, rustTargetDir(t), "clippy", "--quiet", "--", "-D", "warnings")
}

// appendManifest appends text to the Cargo.toml in dir.
func appendManifest(t *testing.T, dir, text string) {
	t.Helper()
	manifest := filepath.Join(dir, "Cargo.toml")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, append(data, []byte(text)...), 0o644); err != nil {
		t.Fatal(err)
	}
}
