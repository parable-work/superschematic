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
)

//go:embed testdata/references_rust.rs
var referencesRustTest string

// referencesRustCases are the cases testdata/references_rust.rs runs:
// D154's "done when" in the Rust repositories, the TypeScript
// repositories' cases (tsgen's referencesCases).
var referencesRustCases = []string{
	"a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks",
	"a ref block in a row's thread is a reference of the row, which the target lists",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it",
}

// TestRustReferencesEndToEnd builds fixture-references-db's Rust types
// (D154, D185) and runs testdata/references_rust.rs over its generated
// repositories' references() and attachments() on Postgres, in a schema
// of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the
// database's create.sql applied, through a bucket in memory that checks
// each PUT against the URL's signature: every case of
// referencesRustCases must run.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustReferencesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust repositories' references and attachments in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Rust repositories' references and attachments against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust repositories' references and attachments")
	}
	const db = "fixture-references-db"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
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
	// The Go ORM, which a DB service always generates, imports its Go
	// types.
	types["go"] = map[string]any{"enabled": true}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}

	typesDir := TypesDir(outputRoot, "rust", db)
	crateDir := filepath.Join(outputRoot, "references-rust")
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
		"Cargo.toml":          fmt.Sprintf(referencesRustManifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":          string(lock),
		"src/lib.rs":          "//! fixture-references-db's Rust repositories: see tests/.\n",
		"tests/references.rs": referencesRustTest,
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
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "REFERENCES_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/references.rs:\n%s", out)
	for _, name := range referencesRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// referencesRustManifest is the test crate's Cargo.toml, given the types
// crate's name and directory and the HTTP runtime's directory.
const referencesRustManifest = `# fixture-references-db's Rust repositories, its references and
# attachments end to end (D154, D185).
[package]
name = "references-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
ring = "0.17.14"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
types = { package = %q, path = %q, features = ["postgres"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
