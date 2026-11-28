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

//go:embed testdata/comments_inbox_rust.rs
var commentsInboxRustTest string

// TestRustCommentsInboxEndToEnd builds fixture-comments-inbox-db's Rust
// types (#713) and runs testdata/comments_inbox_rust.rs over its generated
// repositories and the runtime's orm::notify on Postgres, in a schema of
// its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's
// create.sql applied: a ticket's Comments emits its mentions and replies,
// which the test hands from the table's event queue to the notifier, whose
// routes reach the subjects the events name who may read the ticket;
// markRead, follow and view run on the table; and a message posted as an
// agent for a party over SMS records its for and its via. Every case of
// commentsInboxGoCases, the TypeScript and Go repositories' cases, must
// run.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustCommentsInboxEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping a table's conversation in the Rust notifier in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run a table's conversation in the Rust notifier against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping a table's conversation in the Rust notifier")
	}
	const db = "fixture-comments-inbox-db"
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
	crateDir := filepath.Join(outputRoot, "comments-inbox-rust")
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
		"Cargo.toml":              fmt.Sprintf(commentsInboxRustManifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":              string(lock),
		"src/lib.rs":              "//! fixture-comments-inbox-db's Rust repositories and notifier: see tests/.\n",
		"tests/comments_inbox.rs": commentsInboxRustTest,
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
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "COMMENTS_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/comments_inbox.rs:\n%s", out)
	for _, name := range commentsInboxGoCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// commentsInboxRustManifest is the test crate's Cargo.toml, given the
// types crate's name and directory and the HTTP runtime's directory.
const commentsInboxRustManifest = `# fixture-comments-inbox-db's Rust repositories and notifier end to end
# (#713).
[package]
name = "comments-inbox-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
types = { package = %q, path = %q, features = ["postgres"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
