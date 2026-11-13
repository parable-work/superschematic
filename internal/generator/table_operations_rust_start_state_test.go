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

//go:embed testdata/table_operations_start_state.rs
var tableOperationsStartStateTest string

// TestTableOperationsRustServerWritesABehaviorTable builds
// fixture-workflow-db and fixture-workflow-view-api as a Rust server, once
// with the database's sqlite dialect listed first and once on Postgres
// (D116), and runs the generated router, with no socket under it, over the
// DDL the build writes: placeOrder, a @tableOperation whose body names the
// state its row starts in (D109), creates an Order through the behaviors'
// core (D103), which the database's Rust types bind, and makes its
// Workflow's own transition in the create's transaction, as the caller.
// The cases are the ones the TypeScript server is held to
// (tsrestgen's TestGeneratedStartStateCreateEndToEnd).
//
// The test needs cargo, which it skips without, and runs outside -short;
// the Postgres half needs the database behaviorServerDatabaseEnv names.
func TestTableOperationsRustServerWritesABehaviorTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's behavior table in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's behavior table")
	}
	paths := testpaths.Local(t)
	for _, dialect := range []string{"sqlite", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			if dialect == "postgres" && os.Getenv(behaviorServerDatabaseEnv) == "" {
				t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's behavior table against Postgres")
			}
			runStartStateRustServer(t, cargo, paths, dialect)
		})
	}
}

// runStartStateRustServer builds the two fixtures as a Rust server whose
// database's first dialect is dialect, and runs the start state's cases
// over its router.
func runStartStateRustServer(t *testing.T, cargo string, paths naming.LocalPaths, dialect string) {
	const db, api = "fixture-workflow-db", "fixture-workflow-view-api"
	outputRoot := testpaths.TempDir(t)
	configure := func(service string, cfg *schemaconfig.SchemaConfig) {
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		switch service {
		case db:
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{"enabled": true}
				cfg.Outputs["sql"] = sql
			}
			if dialect == "sqlite" {
				sql["dialects"] = []any{"sqlite", "postgres"}
			}
		case api:
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
	}
	for _, service := range []string{db, api} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		configure(service, cfg)
		result, err := Run(schema, cfg, Options{
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
		})
		if err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
		// Every table operation of the API is served: none stays a method
		// with a warning.
		for _, warning := range result.Warnings {
			t.Errorf("%s: the build warned: %s", service, warning)
		}
	}

	apiDir := APIDir(outputRoot, api)
	crateDir := filepath.Join(outputRoot, "server", "start-state")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ddl := filepath.Join(crateDir, "create.sql")
	if dialect == "sqlite" {
		create, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
		if err != nil {
			t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
		}
		if err := os.WriteFile(ddl, create, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), ddl)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":           fmt.Sprintf(tableOperationsStartStateManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir)),
		"Cargo.lock":           string(lock),
		"src/lib.rs":           "//! The generated Rust server of fixture-workflow-view-api: see tests/.\n",
		"tests/start_state.rs": tableOperationsStartStateTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	test := []string{"test", "--quiet"}
	if dialect == "sqlite" {
		test = append(test, "--features", "sqlite")
	}
	for _, args := range [][]string{
		{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
		test,
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

// tableOperationsStartStateManifest is the test crate's Cargo.toml, given
// the API crate's name and its directory. Its sqlite feature says the
// API's database is a SQLite file.
const tableOperationsStartStateManifest = `# The generated Rust server of fixture-workflow-view-api (D109, D116).
[package]
name = "start-state"
version = "0.0.0"
edition = "2021"
publish = false

[features]
sqlite = []

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
tower = { version = "0.5", features = ["util"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`

// TestRustTypesWarnOfBehaviorTablesTheyBindNoCoreFor: a build of the Rust
// types of a database with a table that composes behaviors, under a naming
// file that names neither of the unpublished crates the repositories run
// the behaviors' core through, warns of the tables whose writes are then
// refused, and how to bind the core (D103, D116); with both named it warns
// of nothing.
func TestRustTypesWarnOfBehaviorTablesTheyBindNoCoreFor(t *testing.T) {
	const db = "fixture-workflow-db"
	build := func(paths naming.LocalPaths) []string {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
		if err != nil {
			t.Fatalf("load %s: %v", db, err)
		}
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		result, err := Run(schema, cfg, Options{
			OutputRoot: testpaths.TempDir(t),
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
		if err != nil {
			t.Fatalf("run %s: %v", db, err)
		}
		return result.Warnings
	}
	paths := testpaths.Local(t)
	if warnings := build(paths); len(warnings) > 0 {
		t.Errorf("a build that names both crates warned: %v", warnings)
	}
	paths.RulesAdapterRust = ""
	warnings := build(paths)
	const want = "the Rust repositories of Order and Shipment write no row: each composes behaviors (D103), which they run through superschematic-rules-adapter and superschematic-rules-behaviors"
	if len(warnings) != 1 || !strings.Contains(warnings[0], want) || !strings.Contains(warnings[0], "[paths] rules_adapter_rust and rules_behaviors_rust") {
		t.Errorf("the build's warnings = %q, want one that holds %q", warnings, want)
	}
}
