package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/behavior_timers_sqlite.rs
var behaviorTimersSQLiteTest string

// TestRustTimersWorkerOnSQLite builds fixture-timers-db with the sqlite
// dialect listed first and fixture-timers-api as a Rust API over it
// (D116): the API's Deps.db is then the database's SQLite file, its
// behavior worker TicketTimers a worker a host drains, and its behavior
// operations run on the file's writing connection. Over a file the build's
// SQLite DDL creates, with no async runtime, it creates tickets through
// the core, drains the worker, and calls an operation through the
// generated router: a drain runs the write a create's trigger enqueued and
// answers when the ticket's deadline falls due; a raw write another
// process made is heard at the next drain; the deadline runs once it is
// due, its message spent with its change set, and the write it made is
// heard in the same drain; a deadline runs at the time its drain is given;
// and an operation's commit wakes the worker, where a refused one's does
// not.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustTimersWorkerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust behavior worker on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust behavior worker on SQLite")
	}
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	configure := func(service string, cfg *schemaconfig.SchemaConfig) {
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		switch service {
		case "fixture-timers-db":
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{}
				cfg.Outputs["sql"] = sql
			}
			sql["dialects"] = []any{"sqlite", "postgres"}
		case "fixture-timers-api":
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
	}
	for _, service := range []string{"fixture-timers-db", "fixture-timers-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		configure(service, cfg)
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				return loader.LoadService(filepath.Join(tsFixtures, name))
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
				if err != nil {
					return nil, false
				}
				configure(name, cfg)
				return cfg, true
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-timers-api")
	ddl := filepath.Join(SQLDir(outputRoot, "fixture-timers-db"), SQLiteSubdir, "create.sql")
	create, err := os.ReadFile(ddl)
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for fixture-timers-db: %v", err)
	}
	crateDir := filepath.Join(outputRoot, "server", "timers-sqlite")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"Cargo.toml":      fmt.Sprintf(behaviorTimersSQLiteManifest, naming.Default().RustAPICrate("fixture-timers-api"), filepath.ToSlash(apiDir), filepath.ToSlash(paths.RulesAdapterRust), filepath.ToSlash(paths.RulesBehaviorsRust)),
		"create.sql":      string(create),
		"src/lib.rs":      "//! The generated Rust API of fixture-timers-api on SQLite: see tests/.\n",
		"tests/timers.rs": behaviorTimersSQLiteTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	for _, args := range [][]string{
		{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
		{"test", "--quiet"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
		t.Logf("cargo %s:\n%s", args[0], out)
	}
}

// behaviorTimersSQLiteManifest is the test crate's Cargo.toml, given the
// API crate's name and its directory, and the directories of the rules
// adapter and of the behaviors' crate.
const behaviorTimersSQLiteManifest = `# The generated Rust API of fixture-timers-api on a SQLite file (D116).
[package]
name = "timers-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
bytes = "1.12.1"
http = "1"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["rt"] }
api = { package = %q, path = %q }
superschematic-rules-adapter = { path = %q, features = ["sqlite"] }
superschematic-rules-behaviors = { path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
