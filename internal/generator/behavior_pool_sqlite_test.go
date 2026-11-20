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

//go:embed testdata/behavior_pool_sqlite.rs
var behaviorPoolSQLiteTest string

// TestRustPoolWorkerRunsAnEventOnSQLite builds fixture-pool-db with the
// sqlite dialect listed first and fixture-pool-api as a Rust API over it
// (D104, D116): its behavior worker AccountRechecks is then a worker a host
// drains, its deadline queue's and its event queue's, and its Workers
// method takes each recheck event Pool emits. Over a file the build's
// SQLite DDL creates, with no async runtime, an account a markOut put out
// is rechecked by a drain given a time past its outUntil: the deadline runs
// through the core, the event it emits is written to the event queue in
// the deadline's unit, and the same drain hands the event to the Workers
// method in a unit of its own, whose reportRoom of the account's room, a
// behavior operation through the event's run, commits with the event's
// spending. A Workers method that fails rolls its unit back, and the event
// comes again.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustPoolWorkerRunsAnEventOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust behavior worker's events on SQLite in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust behavior worker's events on SQLite")
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
		case "fixture-pool-db":
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{}
				cfg.Outputs["sql"] = sql
			}
			sql["dialects"] = []any{"sqlite", "postgres"}
		case "fixture-pool-api":
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
			delete(cfg.Outputs, "sdk")
		}
	}
	for _, service := range []string{"fixture-pool-db", "fixture-pool-api"} {
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

	apiDir := APIDir(outputRoot, "fixture-pool-api")
	ddl := filepath.Join(SQLDir(outputRoot, "fixture-pool-db"), SQLiteSubdir, "create.sql")
	create, err := os.ReadFile(ddl)
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for fixture-pool-db: %v", err)
	}
	crateDir := filepath.Join(outputRoot, "server", "pool-sqlite")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"Cargo.toml":    fmt.Sprintf(behaviorPoolSQLiteManifest, naming.Default().RustAPICrate("fixture-pool-api"), filepath.ToSlash(apiDir), filepath.ToSlash(paths.RulesAdapterRust), filepath.ToSlash(paths.RulesBehaviorsRust)),
		"create.sql":    string(create),
		"src/lib.rs":    "//! The generated Rust API of fixture-pool-api on SQLite: see tests/.\n",
		"tests/pool.rs": behaviorPoolSQLiteTest,
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
		{"test", "--quiet", "--", "--nocapture"},
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

// behaviorPoolSQLiteManifest is the test crate's Cargo.toml, given the API
// crate's name and its directory, and the directories of the rules adapter
// and of the behaviors' crate.
const behaviorPoolSQLiteManifest = `# The generated Rust API of fixture-pool-api on a SQLite file (D104, D116).
[package]
name = "pool-sqlite"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
serde_json = "1.0.149"
api = { package = %q, path = %q }
superschematic-rules-adapter = { path = %q, features = ["sqlite"] }
superschematic-rules-behaviors = { path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
