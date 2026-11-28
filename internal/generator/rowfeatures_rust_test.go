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

//go:embed testdata/rowfeatures_rust_server.rs
var rowFeaturesRustServerTest string

// rowFeaturesRustCases are the cases testdata/rowfeatures_rust_server.rs
// runs: D154's "done when" through the generated Rust server's routes
// (#723), the Go server's cases (rowFeaturesGoCases) and its MCP tools.
var rowFeaturesRustCases = []string{
	"a file posted on one row is filed onto a second, which lists the first in its backlinks",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a reference is added, listed and removed through the routes",
	"an upload whose bytes differ is refused, and a file of another row is not found",
	"a permission the set names for an operation replaces the class's",
	"the MCP endpoint lists the reads and addReference as tools, and a call runs the route as its caller",
}

// rowFeaturesRustConfig sets a fixture's config as the test builds it:
// Rust types beside its own, fixture-references-db on dialects alone, and
// fixture-references-api a Rust API with its MCP endpoint.
func rowFeaturesRustConfig(service string, cfg *schemaconfig.SchemaConfig, dialects []any) {
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["rust"] = map[string]any{"enabled": true}
	switch service {
	case "fixture-references-db":
		// The Go ORM, which a DB service always generates, imports its Go
		// types.
		types["go"] = map[string]any{"enabled": true}
		cfg.Outputs["sql"] = map[string]any{"dialects": dialects}
	case "fixture-references-api":
		cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust, "mcp": true}
	}
}

// TestRustRowFeaturesServerEndToEnd builds fixture-references-db's Rust
// types and fixture-references-api as a Rust API, and runs
// testdata/rowfeatures_rust_server.rs, after clippy, over the generated
// router (#723): each row feature operation runs through the handler of
// src/rowfeatures.rs over the database's repositories, on the Postgres
// behaviorServerDatabaseEnv names, in a schema of its own with the
// database's create.sql applied, through a bucket in memory that checks
// each upload's PUT against the URL's signature. Every case of
// rowFeaturesRustCases must run, the build warns of no
// @rowFeatureOperations set, and the trait holds getTicket alone.
//
// The test skips without the database, without cargo and in -short mode.
func TestRustRowFeaturesServerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's references and files in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's references and files against Postgres")
	}
	runRustRowFeatures(t, "postgres", dsn)
}

// TestRustRowFeaturesServerOnSQLite runs the same cases over a database
// that lists sqlite first: the generated router runs each operation
// through the SQLite repositories of a file of the test's own, on the
// file's writing connection (#723). It needs cargo, which it skips
// without, and no database server.
func TestRustRowFeaturesServerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's references and files on SQLite in -short mode")
	}
	runRustRowFeatures(t, "sqlite", "")
}

// runRustRowFeatures builds the fixture with dialect first and runs the
// cases over it, on the Postgres dsn names for postgres.
func runRustRowFeatures(t *testing.T, dialect, dsn string) {
	t.Helper()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's references and files")
	}
	dialects := []any{"postgres"}
	if dialect == "sqlite" {
		dialects = []any{"sqlite", "postgres"}
	}
	const db, api = "fixture-references-db", "fixture-references-api"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	for _, service := range []string{db, api} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		rowFeaturesRustConfig(service, cfg, dialects)
		result, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      clock,
			LoadDependency: func(name string) (*ir.Schema, error) {
				return loader.LoadService(filepath.Join(tsFixtures, name))
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
				if err != nil {
					return nil, false
				}
				rowFeaturesRustConfig(name, cfg, dialects)
				return cfg, true
			},
		})
		if err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "@rowFeatureOperations") {
				t.Errorf("the Rust build warned of a row feature operation: %s", warning)
			}
		}
	}
	apiCrate := APIDir(outputRoot, api)
	interfaces, err := os.ReadFile(filepath.Join(apiCrate, "src", "interfaces.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(interfaces), "async fn get_ticket(") || strings.Contains(string(interfaces), "async fn backlinks(") || strings.Contains(string(interfaces), "NoteImplementation") {
		t.Errorf("the Rust trait holds other than get_ticket:\n%s", interfaces)
	}

	crateDir := filepath.Join(outputRoot, "rowfeatures-rust")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if dialect == "sqlite" {
		ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
		if err != nil {
			t.Fatalf("the SQLite DDL: %v", err)
		}
		if err := os.WriteFile(filepath.Join(crateDir, "create.sql"), ddl, 0o644); err != nil {
			t.Fatal(err)
		}
	} else {
		pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.sql"))
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	names := naming.Default()
	manifest := fmt.Sprintf(rowFeaturesRustManifest,
		names.RustAPICrate(api), filepath.ToSlash(apiCrate),
		names.RustTypesCrate(db), filepath.ToSlash(TypesDir(outputRoot, "rust", db)), dialect,
		filepath.ToSlash(paths.HTTPRuntimeRust), dialect)
	for name, content := range map[string]string{
		"Cargo.toml":           manifest,
		"Cargo.lock":           string(lock),
		"src/lib.rs":           "//! fixture-references-api's generated Rust server: see tests/.\n",
		"tests/rowfeatures.rs": rowFeaturesRustServerTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	var features []string
	if dialect == "sqlite" {
		features = []string{"--features", "sqlite"}
	}
	var out []byte
	for _, args := range [][]string{
		append(append([]string{"clippy", "--quiet", "--all-targets"}, features...), "--", "-D", "warnings"),
		append(append([]string{"test", "--quiet"}, features...), "--", "--nocapture"),
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "ROW_FEATURES_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/rowfeatures.rs on %s:\n%s", dialect, out)
	for _, name := range rowFeaturesRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// rowFeaturesRustManifest is the test crate's Cargo.toml, given the API
// crate's name and directory, the database's types crate's name,
// directory and dialect, and the HTTP runtime's directory and dialect.
// Its sqlite feature runs the cases over a SQLite file.
const rowFeaturesRustManifest = `# fixture-references-api's generated Rust server, its references and
# files end to end (#723, D154).
[package]
name = "rowfeatures-rust"
version = "0.0.0"
edition = "2021"
publish = false

[features]
sqlite = []

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
bytes = "1.12.1"
http = "1"
ring = "0.17.14"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
api = { package = %q, path = %q }
types = { package = %q, path = %q, features = [%q] }
runtime = { package = "superschematic-http-runtime", path = %q, features = [%q] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
