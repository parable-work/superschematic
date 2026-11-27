package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/recurrence_rust.rs
var recurrenceRustTest string

// recurrenceRustCases are the cases testdata/recurrence_rust.rs runs, by
// name: D152's "done when" in the Rust server (D188), the TypeScript
// server's cases (tsrestgen's recurrenceCases).
var recurrenceRustCases = []string{
	"setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines",
	"a rule change leaves the open occurrence untouched and replans the ones that have not opened",
	"the remind deadline fires on the worker, records the reminder and emits the opening and the reminder",
	"complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it",
	"without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400",
	"the calendar merges a contract end date and a filing occurrences, each dated as its source says",
	"a raw SQL write of a filing holiday calendar is heard by the worker, which plans again",
}

// TestRustRecurrenceEndToEnd builds fixture-recurrence-db's Rust types and
// fixture-recurrence-api as a Rust server (D152, D188), and serves its
// router over the Rust repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied: testdata/recurrence_rust.rs calls Filing's Recurrence
// operations through the router, runs the behavior worker FilingDeadlines'
// generated queues in process, and reads the calendar through the
// runtime's orm::calendar, and every case of recurrenceRustCases must run.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustRecurrenceEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's Recurrence in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's Recurrence against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's Recurrence")
	}
	const db, api = "fixture-recurrence-db", "fixture-recurrence-api"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	load := func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		if service == api {
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
		return schema, cfg
	}
	for _, service := range []string{db, api} {
		schema, cfg := load(service)
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				schema, _ := load(name)
				return schema, nil
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg := load(name)
				return cfg, true
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, api)
	crateDir := filepath.Join(outputRoot, "server", "recurrence")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.sql"))
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":          fmt.Sprintf(recurrenceRustManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir)),
		"Cargo.lock":          string(lock),
		"src/lib.rs":          "//! The generated Rust server of fixture-recurrence-api: see tests/.\n",
		"tests/recurrence.rs": recurrenceRustTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	var out []byte
	for _, args := range [][]string{
		{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "RECURRENCE_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/recurrence.rs:\n%s", out)
	for _, name := range recurrenceRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// TestARustServerOnSQLiteRefusesRecurrenceAndTheCalendar: with sqlite
// listed first in fixture-recurrence-db's dialects, the API's Rust server
// would run on the database's SQLite file, and Recurrence's operations and
// worker and the calendar are served on Postgres alone (D188), so the
// build is refused, naming both, rather than serving them on a file the
// Rust adapter and orm::calendar do not read.
func TestARustServerOnSQLiteRefusesRecurrenceAndTheCalendar(t *testing.T) {
	const db, api = "fixture-recurrence-db", "fixture-recurrence-api"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	load := func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		switch service {
		case db:
			cfg.Outputs["sql"] = map[string]any{"dialects": []any{"sqlite", "postgres"}}
		case api:
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
		return schema, cfg
	}
	var err error
	for _, service := range []string{db, api} {
		schema, cfg := load(service)
		_, err = Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				schema, _ := load(name)
				return schema, nil
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg := load(name)
				return cfg, true
			},
		})
		if service == db && err != nil {
			t.Fatalf("run %s: %v", db, err)
		}
	}
	if err == nil {
		t.Fatal("the Rust server on SQLite was built")
	}
	for _, want := range []string{"lists sqlite first", "Contract: @calendarSource", "Filing: Recurrence", "Filing: @calendarSource", "D188"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal lacks %q: %v", want, err)
		}
	}
}

// recurrenceRustManifest is the test crate's Cargo.toml, given the API
// crate's name and its directory.
const recurrenceRustManifest = `# The generated Rust server of fixture-recurrence-api, Recurrence and the
# calendar end to end (D152, D188).
[package]
name = "recurrence-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
tower = { version = "0.5", features = ["util"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
