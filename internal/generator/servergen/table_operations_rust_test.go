package servergen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The Rust table operations fixture (D106, D116): fixture-tracker-api
// built as a Rust API over fixture-tracker-db, and tracker-rust-stack,
// which serves it from its default server on the local target.
const (
	// rustTablesStackRoot holds tracker-rust-stack.
	rustTablesStackRoot = "testdata/tables/rust/services"
	rustTablesStack     = "tracker-rust-stack"
	rustTablesServer    = "fixture-tracker-api"
	rustTablesDB        = "fixture-tracker-db"
	// rustTablesGoldenRoot holds the server's files as the output root
	// lays them out, by the database's first dialect.
	rustTablesGoldenRoot = "testdata/golden/tables/rust"
)

// rustTablesOrder is the order a build-all builds the fixture in.
var rustTablesOrder = []string{rustTablesDB, rustTablesServer, rustTablesStack}

// loadRustTablesFixture loads the fixture with the core registry and
// stacktest's fake target, the database's DB service listing dialects in
// order: the API's Deps.db is the client of the first (D116).
func loadRustTablesFixture(t *testing.T, dialects ...string) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range rustTablesOrder {
		root := tsBehaviorsServicesRoot
		if name == rustTablesStack {
			root = rustTablesStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	listed := make([]any, len(dialects))
	for i, dialect := range dialects {
		listed[i] = dialect
	}
	f.configs[rustTablesDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": listed},
	}
	f.configs[rustTablesServer].Outputs = map[string]any{
		"types": map[string]any{"rust": enabled},
		"api":   map[string]any{"enabled": true, "language": "RUST"},
	}
	return f
}

// TestRustTableOperationsServerGolden: the Rust server of
// fixture-tracker-api builds its Implementations with its database, from
// whose repositories the router serves the table operations (D106, D116):
// a pool it connects for a postgres-first database, and the SQLite file it
// opens for a sqlite-first one. Its main.rs is laid out as rustfmt lays it
// out. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustTableOperationsServerGolden -update
func TestRustTableOperationsServerGolden(t *testing.T) {
	for _, c := range []struct {
		name     string
		dialects []string
		wants    []string
	}{
		{"postgres", []string{"postgres"}, []string{"connect(", "fixture_tracker_db_pool.clone()", `features = ["logging", "otel", "postgres"]`}},
		{"sqlite", []string{"sqlite", "postgres"}, []string{"open_file(", "sqlite::connect(database)", "Arc<SqliteFile>", `"sqlite"`}},
	} {
		t.Run(c.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			f := loadRustTablesFixture(t, c.dialects...)
			f.build(t, repoRoot, rustFakePaths(repoRoot), rustTablesOrder...)
			out := filepath.Join(repoRoot, "schemas", "dist")
			dir := servergen.ServerDir(out, rustTablesStack, rustTablesServer)
			files := map[string]string{}
			for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile} {
				files[filepath.Join("server", rustTablesStack, rustTablesServer, filepath.FromSlash(file))] = filepath.Join(dir, filepath.FromSlash(file))
			}
			compareGoldens(t, filepath.Join(rustTablesGoldenRoot, c.name), files)

			source := filepath.Join(dir, filepath.FromSlash(servergen.RustMainFile))
			main, err := os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			manifest, err := os.ReadFile(filepath.Join(dir, servergen.RustManifestFile))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range c.wants {
				if !strings.Contains(string(main), want) && !strings.Contains(string(manifest), want) {
					t.Errorf("the server's main.rs and Cargo.toml lack %s", want)
				}
			}
			if !strings.Contains(string(main), "Implementations::new(") {
				t.Errorf("main.rs builds no Implementations:\n%s", main)
			}
			if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
				if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
					t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
				}
			}
		})
	}
}

// TestRustTableOperationsServerBuilds: the fixture's servers build in the
// output root's workspace with the implementation's scaffold and pass
// clippy, over a Postgres pool and over a SQLite file (D116).
func TestRustTableOperationsServerBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo clippy in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	paths := testpaths.Local(t)
	target := rustTargetDir(t)
	for _, dialects := range [][]string{{"postgres"}, {"sqlite", "postgres"}} {
		t.Run(dialects[0], func(t *testing.T) {
			repoRoot := testpaths.TempDir(t)
			f := loadRustTablesFixture(t, dialects...)
			f.build(t, repoRoot, paths, rustTablesOrder...)
			out := filepath.Join(repoRoot, "schemas", "dist")
			cmd := exec.Command(cargo, "clippy", "--workspace", "--quiet", "--", "-D", "warnings")
			cmd.Dir = out
			cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("cargo clippy in %s: %v\n%s", out, err, output)
			}
		})
	}
}
