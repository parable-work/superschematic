package generator

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

//go:embed testdata/store_backend_rust.rs
var storeBackendRust string

// rustRuntimeCrate builds db's Rust types (Rust and Go types enabled
// beside its config's) under a temporary output root, writes a test crate
// of manifest (given the types crate's name and directory and the HTTP
// runtime's directory) with source as tests/<name>.rs, the database's
// Postgres create.sql and SQLite create.sqlite.sql, and the store helper
// (testdata/store_backend_rust.rs) as tests/backend/mod.rs, runs cargo
// clippy, and runs cargo test on a SQLite file (#618) and, with dsn, on
// Postgres (RUST_E2E_STORE): each run's output, the Postgres one empty
// without dsn. env is given to both.
func rustRuntimeCrate(t *testing.T, cargo, db, name, manifest, source, dsn string, env ...string) (onSQLite, onPostgres string) {
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
	for _, dir := range []string{"src", "tests/backend"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(crateDir, "create.sql"))
	ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for file, content := range map[string]string{
		"Cargo.toml":             fmt.Sprintf(manifest, naming.Default().RustTypesCrate(db), filepath.ToSlash(typesDir), filepath.ToSlash(paths.HTTPRuntimeRust)),
		"Cargo.lock":             string(lock),
		"create.sqlite.sql":      string(ddl),
		"src/lib.rs":             "//! " + db + "'s Rust repositories and runtime: see tests/.\n",
		"tests/" + name + ".rs":  source,
		"tests/backend/mod.rs":   storeBackendRust,
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
	run := func(extra []string, args ...string) string {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(append(append(os.Environ(), "CARGO_TARGET_DIR="+target), env...), extra...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s (%v): %v\n%s", args[0], crateDir, extra, err, out)
		}
		return string(out)
	}
	run(nil, "clippy", "--quiet", "--all-targets", "--", "-D", "warnings")
	onSQLite = run([]string{"RUST_E2E_STORE=sqlite"}, "test", "--quiet", "--", "--nocapture")
	if dsn != "" {
		onPostgres = run([]string{"RUST_E2E_STORE=postgres", "RUST_E2E_DATABASE_URL=" + dsn}, "test", "--quiet", "--", "--nocapture")
	} else {
		t.Log("set " + behaviorServerDatabaseEnv + " to run the cases on Postgres too and hold the two runs' answers equal")
	}
	return onSQLite, onPostgres
}

// checkRustRuns holds each run's output to cases, every one of which must
// run, and the Postgres run's answer lines to the SQLite run's (#618): the
// same recipients, states, refusals and reads on both databases.
func checkRustRuns(t *testing.T, name string, cases []string, onSQLite, onPostgres string) {
	t.Helper()
	for store, out := range map[string]string{"sqlite": onSQLite, "postgres": onPostgres} {
		if out == "" {
			continue
		}
		t.Logf("tests/%s.rs on %s:\n%s", name, store, out)
		for _, c := range cases {
			if !strings.Contains(out, "case: "+c+"\n") {
				t.Errorf("on %s the case %q did not run:\n%s", store, c, out)
			}
		}
	}
	answers := func(out string) []string {
		var lines []string
		for _, line := range strings.Split(out, "\n") {
			if strings.HasPrefix(line, "answer: ") {
				lines = append(lines, line)
			}
		}
		return lines
	}
	if len(answers(onSQLite)) == 0 {
		t.Errorf("tests/%s.rs printed no answers on SQLite", name)
	}
	if onPostgres != "" && !slices.Equal(answers(onSQLite), answers(onPostgres)) {
		t.Errorf("tests/%s.rs answered differently on SQLite and on Postgres:\nsqlite:\n%s\npostgres:\n%s", name, strings.Join(answers(onSQLite), "\n"), strings.Join(answers(onPostgres), "\n"))
	}
}

// TestRustPartiesEndToEnd builds fixture-parties-db's Rust types (D157,
// D193, #618) and runs testdata/parties_rust.rs over its generated
// repositories and the runtime's orm::inbound on a SQLite file with the
// build's SQLite DDL applied and, with
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL, on Postgres in a schema of its
// own with create.sql applied, with a stand-in SMS relay and mail relay as
// the channel adapters and a bucket in memory: the test's drain hands each
// run request to a stub handler that writes a report on the granted job as
// the party, as tsgen's parties.test.ts does. Every case of
// partiesRustCases must run on each, and their answers must be the same.
// The relay's token never reaches the test's output.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestRustPartiesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust inbound path in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust inbound path")
	}
	onSQLite, onPostgres := rustRuntimeCrate(t, cargo, "fixture-parties-db", "parties", partiesRustManifest, partiesRustTest, os.Getenv(behaviorServerDatabaseEnv))
	checkRustRuns(t, "parties", partiesRustCases, onSQLite, onPostgres)
	for _, out := range []string{onSQLite, onPostgres} {
		if strings.Contains(out, "relay-token") {
			t.Errorf("a relay's token reached the test's output:\n%s", out)
		}
	}
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
types = { package = %q, path = %q, features = ["postgres", "sqlite"] }
runtime = { package = "superschematic-http-runtime", path = %q, features = ["postgres", "sqlite"] }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
