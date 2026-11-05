package servergen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// rustSeatsFixture is fixture-seats-api over fixture-seats-db (D104).
var rustSeatsFixture = rustBehaviorFixture{
	api: "fixture-seats-api", db: "fixture-seats-db", stack: "seats-stack",
	databaseKey: "FIXTURE_SEATS_DB_DATABASE", databaseGoName: "FixtureSeatsDbDatabase",
}

// TestRustSeatsBehaviorWorkerEntrypoint: the entrypoint of
// fixture-seats-api's behavior worker SandboxDeadlines (D104) opens a pool
// on the API's database and runs Sandbox's deadline queue, whose stale
// deadline moves a sandbox no observation has reached to unreachable, with
// the handler the API crate's behaviors module holds, in a claim loop of
// the HTTP runtime's, as JobDeadlines' runs Job's: a behavior only the
// core implements, on a field of a table, plans as any other. Its main.rs
// passes clippy against the API crate, the database's Rust types and the
// checkout's crates, unless in -short mode or without cargo.
func TestRustSeatsBehaviorWorkerEntrypoint(t *testing.T) {
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
	seats := rustSeatsFixture.apiCrate(t, out, paths)
	in := rustSeatsFixture.workerInput(t, repoRoot, out, paths, seats, "SandboxDeadlines", "fixture-seats-api-sandbox-deadlines")
	s, err := servergen.PlanRust(in)
	if err != nil {
		t.Fatalf("PlanRust SandboxDeadlines: %v", err)
	}
	if s.Kind != "worker" || s.Worker == nil || s.Worker.Behavior == nil {
		t.Fatalf("planned SandboxDeadlines as %s with worker %+v, want a behavior worker", s.Kind, s.Worker)
	}
	if got := s.Worker.QueueNames(); got != "SandboxDeadline" {
		t.Errorf("SandboxDeadlines runs %s, want Sandbox's deadline queue alone", got)
	}
	if err := servergen.WriteRust(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(in.Dir, filepath.FromSlash(servergen.RustMainFile)))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fixture_seats_api_api::behaviors::sandbox_deadlines_deadlines(&fixture_seats_db_pool)?",
		"fixture_seats_api_api::behaviors::run_sandbox_deadlines_deadline(&pool, claimed).await",
	} {
		if !strings.Contains(string(main), want) {
			t.Errorf("main.rs does not contain %q:\n%s", want, main)
		}
	}
	for _, absent := range []string{"create_workers", "Deps {", "changes("} {
		if strings.Contains(string(main), absent) {
			t.Errorf("main.rs holds %s, which SandboxDeadlines has none of:\n%s", absent, main)
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
	rustSeatsFixture.checkWorkerCompiles(t, cargo, "SandboxDeadlines", "fixture-seats-api-sandbox-deadlines")
}
