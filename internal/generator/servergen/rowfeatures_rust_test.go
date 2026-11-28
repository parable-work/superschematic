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
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The Rust row feature operations fixture (#723): fixture-references-api
// built as a Rust API over fixture-references-db, its files in
// fixture-references-files, and references-rust-stack, which serves it
// from its default server on the local target.
const (
	rustRowFeaturesStackRoot  = "testdata/rowfeatures/rust/services"
	rustRowFeaturesStack      = "references-rust-stack"
	rustRowFeaturesServer     = "fixture-references-api"
	rustRowFeaturesDB         = "fixture-references-db"
	rustRowFeaturesBucket     = "fixture-references-files"
	rustRowFeaturesGoldenRoot = "testdata/golden/rowfeatures/rust"
)

// rustRowFeaturesOrder is the order a build-all builds the fixture in.
var rustRowFeaturesOrder = []string{rustRowFeaturesDB, rustRowFeaturesBucket, rustRowFeaturesServer, rustRowFeaturesStack}

// loadRustRowFeaturesFixture loads the fixture with the core registry and
// stacktest's fake target, the database on Postgres with its Rust types.
func loadRustRowFeaturesFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range rustRowFeaturesOrder {
		root := tsBehaviorsServicesRoot
		if name == rustRowFeaturesStack {
			root = rustRowFeaturesStackRoot
		}
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	f.configs[rustRowFeaturesDB].Outputs = map[string]any{
		"types": map[string]any{"go": enabled, "rust": enabled},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	f.configs[rustRowFeaturesServer].Outputs = map[string]any{
		"types": map[string]any{"rust": enabled},
		"api":   map[string]any{"enabled": true, "language": "RUST"},
	}
	return f
}

// TestRustRowFeaturesServerGolden: the Rust server of
// fixture-references-api builds its Implementations with the pool of its
// database, through whose repositories the router runs the row feature
// operations, and the bucket that holds their files, which it opens for
// Deps too (#723). Its main.rs is laid out as rustfmt lays it out.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestRustRowFeaturesServerGolden -update
func TestRustRowFeaturesServerGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadRustRowFeaturesFixture(t)
	f.build(t, repoRoot, rustFakePaths(repoRoot), rustRowFeaturesOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	dir := servergen.ServerDir(out, rustRowFeaturesStack, rustRowFeaturesServer)
	files := map[string]string{}
	for _, file := range []string{servergen.RustManifestFile, servergen.RustMainFile} {
		files[filepath.Join("server", rustRowFeaturesStack, rustRowFeaturesServer, filepath.FromSlash(file))] = filepath.Join(dir, filepath.FromSlash(file))
	}
	compareGoldens(t, rustRowFeaturesGoldenRoot, files)

	source := filepath.Join(dir, filepath.FromSlash(servergen.RustMainFile))
	main, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Implementations::new(", "fixture_references_db_pool.clone()", "Arc::clone(&fixture_references_files_bucket)"} {
		if !strings.Contains(string(main), want) {
			t.Errorf("the server's main.rs lacks %s", want)
		}
	}
	if rustfmt, err := exec.LookPath("rustfmt"); err == nil {
		if diff, err := exec.Command(rustfmt, "--edition", "2021", "--check", source).CombinedOutput(); err != nil {
			t.Errorf("rustfmt lays out %s otherwise: %v\n%s", source, err, diff)
		}
	}
}
