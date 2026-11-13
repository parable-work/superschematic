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

// rustSQLiteWorkerGoldenRoot holds the Rust entrypoints of behavior
// workers over a SQLite file (D116), a directory per worker.
const rustSQLiteWorkerGoldenRoot = "testdata/golden/behavior-worker/rust-sqlite"

// onSQLite sets the crate's Deps on its database's SQLite file, as the
// dispatch layer sets them for a database whose DB service lists sqlite
// first (D116).
func (f rustBehaviorFixture) onSQLite(t *testing.T, crate rustTimers) rustTimers {
	t.Helper()
	deps := *crate.output.Deps
	deps.SQLite = true
	if err := crate.output.SetDeps(deps); err != nil {
		t.Fatalf("set the Deps of %s on a SQLite file: %v", f.api, err)
	}
	return crate
}

// rustBehaviorWorkerOnSQLite is a behavior worker whose entrypoint the
// tests plan over a SQLite file: its fixture, its class, its deployable,
// and whether its table's behaviors emit events, which go to the
// implementation's Workers.
type rustBehaviorWorkerOnSQLite struct {
	fixture      rustBehaviorFixture
	name, server string
	emits        bool
	wants        []string
}

var rustBehaviorWorkersOnSQLite = []rustBehaviorWorkerOnSQLite{
	{
		fixture: rustTimersFixture, name: "TicketTimers", server: "fixture-timers-api-ticket-timers",
		wants: []string{
			"use superschematic_http_runtime::orm::sqlite::{self, Database as SqliteFile};",
			"use superschematic_http_runtime::worker::{self, DrainOptions};",
			"let fixture_timers_db_pool = open_file(",
			"let queues = fixture_timers_api_api::behaviors::ticket_timers_worker()?;",
			".map(|_| worker::drain::open(fixture_timers_db_pool.path()))",
			"let wake = Arc::clone(fixture_timers_db_pool.wake());",
			"worker::run_drain(Arc::new(queues), connections, Some(wake), options, shutdown).await?;",
		},
	},
	{
		fixture: rustSchedulesFixture, name: "ProblemWakeUps", server: "fixture-schedules-api-problem-wake-ups", emits: true,
		wants: []string{
			"fixture_schedules_api_impl::create_workers(fixture_schedules_api_deps)",
			"db: fixture_schedules_db_pool.clone(),",
			"let queues = fixture_schedules_api_api::behaviors::problem_wake_ups_worker(workers)?;",
			"worker::run_drain(Arc::new(queues), connections, Some(wake), options, shutdown).await?;",
		},
	},
}

// TestRustBehaviorWorkerEntrypointOnSQLite: over a database whose DB
// service lists sqlite first (D116), the entrypoint of a behavior worker
// opens the database's SQLite file and runs the HTTP runtime's loop over
// the drain of the worker the API crate's behaviors module builds, on
// connections of the worker's own. TicketTimers builds no Deps and
// depends on no implementation; ProblemWakeUps, whose table's Schedules
// emits events, builds the API's Deps over the file and hands the
// implementation's Workers to the worker. Each main.rs is laid out as
// rustfmt lays it out, and passes clippy against the API crate, the
// database's Rust types and the checkout's crates, unless in -short mode or
// without cargo. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustBehaviorWorkerEntrypointOnSQLite -update
func TestRustBehaviorWorkerEntrypointOnSQLite(t *testing.T) {
	for _, w := range rustBehaviorWorkersOnSQLite {
		t.Run(w.name, func(t *testing.T) {
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
			crate := w.fixture.onSQLite(t, w.fixture.apiCrate(t, out, paths))
			in := w.fixture.workerInput(t, repoRoot, out, paths, crate, w.name, w.server)
			s, err := servergen.PlanRust(in)
			if err != nil {
				t.Fatalf("PlanRust: %v", err)
			}
			if s.Kind != "worker" || s.Worker == nil || s.Worker.Behavior == nil || !s.Worker.OnSQLite() || s.Worker.Emits() != w.emits {
				t.Fatalf("planned %s with worker %+v, want a behavior worker on a SQLite file", s.Kind, s.Worker)
			}
			if len(s.Databases) != 0 || len(s.Files) != 1 {
				t.Errorf("the worker opens %d pools and %d files, want its database's file alone", len(s.Databases), len(s.Files))
			}
			if err := servergen.WriteRust(s, in.Dir); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{}
			for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile} {
				files[filepath.FromSlash(file)] = filepath.Join(in.Dir, filepath.FromSlash(file))
			}
			compareGoldens(t, filepath.Join(rustSQLiteWorkerGoldenRoot, w.server), files)
			source := filepath.Join(in.Dir, filepath.FromSlash(servergen.RustMainFile))
			main, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := os.ReadFile(filepath.Join(in.Dir, servergen.RustManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(manifest), `features = ["logging", "otel", "sqlite"]`) {
				t.Errorf("Cargo.toml does not turn on the runtime's sqlite feature alone of its databases':\n%s", manifest)
			}
			for _, want := range w.wants {
				if !strings.Contains(string(main), want) {
					t.Errorf("main.rs does not contain %q:\n%s", want, main)
				}
			}
			for _, absent := range []string{"postgres", "run_worker", "PostgresQueue", "_deadlines(", "tokio::join!"} {
				if strings.Contains(string(main), absent) {
					t.Errorf("main.rs holds %s, which a behavior worker on a SQLite file has none of:\n%s", absent, main)
				}
			}
			if !w.emits && (strings.Contains(string(main), "create_workers") || strings.Contains(string(main), "Deps {")) {
				t.Errorf("main.rs builds Deps or workers, which a behavior worker of a table that emits no events has none of:\n%s", main)
			}
			if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
				if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
					t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
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
			w.checkCompiles(t, cargo)
		})
	}
}

// checkCompiles writes the worker's entrypoint over a SQLite file, the API
// crate it runs and the Rust types both read under a repository of their
// own with this checkout's runtimes, with the implementation's scaffold
// when its events go to the implementation's Workers, and lints the
// entrypoint with clippy, warnings denied.
func (w rustBehaviorWorkerOnSQLite) checkCompiles(t *testing.T, cargo string) {
	t.Helper()
	paths := testpaths.Local(t)
	names := naming.Default()
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	crate := w.fixture.onSQLite(t, w.fixture.apiCrate(t, out, paths))
	w.fixture.writeCrates(t, out, paths, crate)
	if w.emits {
		// The scaffold in the implementation's directory, which the plan
		// reads, and in its mirror, which the entrypoint builds against.
		apiDir := apiDirOf(out, crate)
		for _, dir := range []string{filepath.Join(repoRoot, "services", crate.output.SchemaName, "rust"), servergen.MirrorDir(out, crate.output.SchemaName)} {
			if _, err := rustrestgen.WriteImplementationScaffold(crate.output, dir, apiDir, paths, names); err != nil {
				t.Fatalf("write the implementation's scaffold: %v", err)
			}
		}
	}
	in := w.fixture.workerInput(t, repoRoot, out, paths, crate, w.name, w.server)
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

// TestARustAppOnASQLiteFileIsRefused: a Rust server, job and worker open a
// database's SQLite file (D116), and an app, whose program takes the
// database as a pool, does not yet, so its entrypoint is refused, naming
// the database.
func TestARustAppOnASQLiteFileIsRefused(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	paths := rustFakePaths(repoRoot)
	paths.RulesAdapterRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-adapter")
	paths.RulesBehaviorsRust = filepath.Join(repoRoot, "runtime", "engine", "rust", "rules-behaviors")
	f := rustTimersFixture
	crate := f.onSQLite(t, f.apiCrate(t, out, paths))
	crate.output.Apps = []rustrestgen.AppInfo{{Name: "Desk"}}
	in := f.workerInput(t, repoRoot, out, paths, crate, "TicketTimers", "fixture-timers-api-desk")
	in.Worker = nil
	in.App = &servergen.AppInput{Name: "Desk", Implementation: servergen.RustImplementation{Crate: "desk", Lib: "desk"}}
	_, err := servergen.PlanRust(in)
	want := "stack timers-stack: app fixture-timers-api-desk serves fixture-timers-api on fixture-timers-db, whose DB service lists sqlite first; a Rust server, job and worker open a SQLite file, and a deriver, an engine's runner and an app do not yet (D116)"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("PlanRust = %v, want %q", err, want)
	}
}
