package servergen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// rustPoolWorkerGoldenRoot holds the Rust entrypoint of fixture-pool-api's
// behavior worker AccountRechecks (D104).
const rustPoolWorkerGoldenRoot = "testdata/golden/behavior-worker/rust-pool"

// rustPoolFixture is fixture-pool-api over fixture-pool-db (D104).
var rustPoolFixture = rustBehaviorFixture{
	api: "fixture-pool-api", db: "fixture-pool-db", stack: "pool-stack",
	databaseKey: "FIXTURE_POOL_DB_DATABASE", databaseGoName: "FixturePoolDbDatabase",
}

// TestRustPoolBehaviorWorkerEntrypoint: the entrypoint of fixture-pool-api's
// behavior worker AccountRechecks (D104), whose table's Pool emits a recheck
// event at each out account's due recheck, builds the API's Deps as a
// worker's does and calls the implementation's create_workers, then runs
// Account's deadline queue and its event queue, each with the handler the
// API crate's behaviors module holds, in a claim loop of the HTTP runtime's
// of its own, together: the event queue's handler hands each event to the
// implementation's Workers method of its use and kind. Account hears no
// change, so the worker runs no change queue. It depends on the
// implementation's mirror, as a worker's does. Its main.rs passes clippy
// against the API crate, the database's Rust types, the implementation's
// scaffold and the checkout's crates, unless in -short mode or without
// cargo. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustPoolBehaviorWorkerEntrypoint -update
func TestRustPoolBehaviorWorkerEntrypoint(t *testing.T) {
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
	pool := rustPoolFixture.apiCrate(t, out, paths)
	in := rustPoolFixture.workerInput(t, repoRoot, out, paths, pool, "AccountRechecks", "fixture-pool-api-account-rechecks")
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust AccountRechecks: %v", err)
	}
	if s.Kind != "worker" || s.Worker == nil || s.Worker.Behavior == nil || !s.Worker.Emits() {
		t.Fatalf("planned AccountRechecks as %s with worker %+v, want a behavior worker whose table emits events", s.Kind, s.Worker)
	}
	if got := s.Worker.QueueNames(); got != "AccountDeadline, AccountEvent" {
		t.Errorf("AccountRechecks runs %s, want Account's deadline queue and then its event queue", got)
	}
	implementation := false
	for _, d := range s.Dependencies {
		if strings.Contains(d.Name, "implementation") || strings.Contains(d.Name, "impl") {
			implementation = true
		}
	}
	if !implementation {
		t.Errorf("the behavior worker depends on no implementation, whose Workers take its events: %+v", s.Dependencies)
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
	compareGoldens(t, rustPoolWorkerGoldenRoot, files)
	main, err := os.ReadFile(filepath.Join(in.Dir, filepath.FromSlash(servergen.RustMainFile)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fixture_pool_api_impl::create_workers(fixture_pool_api_deps)",
		"let fixture_pool_api_deps = fixture_pool_api_api::Deps {",
		"fixture_pool_api_api::behaviors::account_rechecks_deadlines(&fixture_pool_db_pool)?",
		"fixture_pool_api_api::behaviors::account_rechecks_events(&fixture_pool_db_pool)?",
		"let workers = Arc::clone(&workers);",
		"worker::run_worker(events, run_events, options, shutdown.clone()),",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.rs does not contain %q:\n%s", want, main)
		}
	}
	for _, absent := range []string{"account_rechecks_changes", "It builds no Deps"} {
		if strings.Contains(string(main), absent) {
			t.Errorf("main.rs holds %q, and AccountRechecks runs no change queue and builds the Deps its event handlers take:\n%s", absent, main)
		}
	}

	if testing.Short() {
		return
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Log("cargo not available; main.rs is not compiled")
		return
	}
	checkPoolWorkerCompiles(t, cargo)
}

// checkPoolWorkerCompiles writes AccountRechecks' entrypoint as
// checkSchedulesWorkerCompiles writes ProblemWakeUps', with the
// implementation's scaffold in its mirror, whose create_workers its main.rs
// calls, and lints the entrypoint with clippy, warnings denied.
func checkPoolWorkerCompiles(t *testing.T, cargo string) {
	t.Helper()
	f := rustPoolFixture
	paths := testpaths.Local(t)
	names := naming.Default()
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	pool := f.apiCrate(t, out, paths)
	f.writeCrates(t, out, paths, pool)
	apiDir := apiDirOf(out, pool)
	// The scaffold in the implementation's directory, which the plan reads,
	// and in its mirror, which the entrypoint builds against.
	for _, dir := range []string{filepath.Join(repoRoot, "services", pool.output.SchemaName, "rust"), servergen.MirrorDir(out, pool.output.SchemaName)} {
		if _, err := rustrestgen.WriteImplementationScaffold(pool.output, dir, apiDir, paths, names); err != nil {
			t.Fatalf("write the implementation's scaffold: %v", err)
		}
	}
	in := f.workerInput(t, repoRoot, out, paths, pool, "AccountRechecks", "fixture-pool-api-account-rechecks")
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust: %v", err)
	}
	if err := servergen.WriteRust(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	appendManifest(t, in.Dir, "\n[workspace]\n\n"+testpaths.RustPatch(paths, names))
	cargoCommand(t, cargo, in.Dir, rustTargetDir(t), "clippy", "--quiet", "--", "-D", "warnings")
}
