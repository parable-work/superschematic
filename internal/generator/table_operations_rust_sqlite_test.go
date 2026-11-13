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

//go:embed testdata/table_operations_sqlite.rs
var tableOperationsSQLiteTest string

// TestTableOperationsRustServerOnSQLite builds fixture-tracker-db with the
// sqlite dialect listed first and fixture-tracker-api as a Rust server
// over it (D116): the API's Deps.db is then the database's SQLite file,
// and its table operations are served from the Rust repositories on
// SQLite. It runs the generated router, with no socket under it, over a
// file the build's SQLite DDL creates: each kind of operation, the path's
// parents, a list's page across a column's nulls, its search and its
// negated filter, an update's absent member and its null, the soft
// delete, the refusals and a row rule.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestTableOperationsRustServerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's table operations on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's table operations on SQLite")
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
		case "fixture-tracker-db":
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{"enabled": true}
				cfg.Outputs["sql"] = sql
			}
			sql["dialects"] = []any{"sqlite", "postgres"}
		case "fixture-tracker-api":
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
	}
	for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
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

	apiDir := APIDir(outputRoot, "fixture-tracker-api")
	ddl := filepath.Join(SQLDir(outputRoot, "fixture-tracker-db"), SQLiteSubdir, "create.sql")
	if _, err := os.Stat(ddl); err != nil {
		t.Fatalf("the build wrote no SQLite DDL for fixture-tracker-db: %v", err)
	}
	crateDir := filepath.Join(outputRoot, "server", "tracker-sqlite")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	create, err := os.ReadFile(ddl)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":       fmt.Sprintf(tableOperationsSQLiteManifest, naming.Default().RustAPICrate("fixture-tracker-api"), filepath.ToSlash(apiDir)),
		"Cargo.lock":       string(lock),
		"create.sql":       string(create),
		"src/lib.rs":       "//! The generated Rust server of fixture-tracker-api on SQLite: see tests/.\n",
		"tests/tracker.rs": tableOperationsSQLiteTest,
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

// tableOperationsSQLiteManifest is the test crate's Cargo.toml, given the
// API crate's name and its directory.
const tableOperationsSQLiteManifest = `# The generated Rust server of fixture-tracker-api on a SQLite file (D116).
[package]
name = "tracker-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

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
