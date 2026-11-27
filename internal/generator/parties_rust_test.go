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

//go:embed testdata/parties_rust.rs
var partiesRustTest string

// partiesRustCases are the cases testdata/parties_rust.rs runs: D157's
// "done when" in the Rust repositories and runtime (D193), the TypeScript
// repositories' cases (tsgen's partiesCases).
var partiesRustCases = []string{
	"a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached",
	"a text from an unknown number lands in triage",
	"the same text from a party whose grant has ended reaches nothing, until its identity is verified again",
	"email failing DMARC is treated as unknown, and an authenticated email threads to its party",
	"a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply",
	"two parties sharing an address are told apart by the thread, and held in triage otherwise",
	"sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP",
}

// rustRuntimeCrate builds db's Rust types (Rust and Go types enabled
// beside its config's, for postgres alone) under a temporary output root,
// writes a test crate of manifest (given the types crate's name and
// directory and the HTTP runtime's directory) with source as
// tests/<name>.rs and the database's create.sql, and runs cargo clippy and
// cargo test in it with env: the test's output.
func rustRuntimeCrate(t *testing.T, cargo, db, name, manifest, source string, env ...string) string {
	t.Helper()
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
	rustOnPostgresAlone(cfg)
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
	crateDir := filepath.Join(outputRoot, name+"-rust")
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
	for file, content := range map[string]string{
		"Cargo.toml":            fmt.Sprintf(manifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":            string(lock),
		"src/lib.rs":            "//! " + db + "'s Rust repositories and runtime: see tests/.\n",
		"tests/" + name + ".rs": source,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if keep := os.Getenv("RUST_E2E_KEEP"); keep != "" {
		_ = exec.Command("cp", "-R", crateDir, keep).Run()
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
		cmd.Env = append(append(os.Environ(), "CARGO_TARGET_DIR="+target), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	return string(out)
}

// TestRustPartiesEndToEnd builds fixture-parties-db's Rust types (D157,
// D193) and runs testdata/parties_rust.rs over its generated repositories
// and the runtime's orm::inbound on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied, with a stand-in SMS relay and mail relay as the channel
// adapters and a bucket in memory: the test's drain hands each run request
// to a stub handler that writes a report on the granted job as the party,
// as tsgen's parties.test.ts does, and every case of partiesRustCases must
// run. The relay's token never reaches the test's output.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustPartiesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust inbound path in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Rust inbound path against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust inbound path")
	}
	out := rustRuntimeCrate(t, cargo, "fixture-parties-db", "parties", partiesRustManifest, partiesRustTest, "PARTIES_TEST_DATABASE_URL="+dsn)
	t.Logf("tests/parties.rs:\n%s", out)
	for _, name := range partiesRustCases {
		if !strings.Contains(out, "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
	if strings.Contains(out, "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
}

// rustOnPostgresAlone builds cfg's DB service for postgres alone. Rust
// serves the notifier (D191), the parties and the agents (D193) on
// Postgres alone, and rustgen refuses them to the Rust types of a service
// that lists sqlite (D181). Their fixtures list sqlite for TypeScript's
// runs there (D189), so a Rust run of them leaves it out.
func rustOnPostgresAlone(cfg *schemaconfig.SchemaConfig) {
	sql, _ := cfg.Outputs["sql"].(map[string]any)
	if sql == nil {
		sql = map[string]any{}
		cfg.Outputs["sql"] = sql
	}
	sql["dialects"] = []any{"postgres"}
}

// partiesRustManifest is the test crate's Cargo.toml, given the types
// crate's name and directory and the HTTP runtime's directory.
const partiesRustManifest = `# fixture-parties-db's Rust repositories and inbound path end to end
# (D157, D193).
[package]
name = "parties-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
ring = "0.17.14"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
uuid = { version = "1", features = ["v4"] }
types = { package = %q, path = %q, features = ["postgres"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
