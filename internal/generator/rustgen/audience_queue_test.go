package rustgen

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/orm/audience_queue_rules.rs
var audienceQueueRulesTest string

// audienceQueueRulesCases are the cases audience_queue_rules.rs runs, by
// name, each of which it prints as it starts it, as tsgen's
// TestAudienceQueueEndToEnd and ormgen's TestAudienceQueueOnPostgres hold
// the TypeScript and the Go repositories to them.
var audienceQueueRulesCases = []string{
	"a folder's change of audience commits without its pages, enqueued once on the folder's queue",
	"a page read right after the worker drains the queue shows the folder's new audience",
}

// TestRepositoriesResolveAnAudienceThroughItsQueue runs
// fixture-audience-queue-db's folders and pages on Postgres (D156, #734):
// a folder's change of audience commits without its pages and enqueues the
// folder once, a page keeps the audience it had until the queue's worker
// runs orm::resolve_audience for the message, and a page read right after
// the worker drains the queue shows the folder's new audience. The page's
// table definition carries the statement resolve_audience runs.
//
// It needs cargo, which it skips without, and the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, and runs outside -short.
func TestRepositoriesResolveAnAudienceThroughItsQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the audience queue's cargo test in -short mode")
	}
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Rust repositories' audience queue against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the audience queue's repositories")
	}
	const fixture = "fixture-audience-queue-db"
	paths := testpaths.Local(t)
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	root := testpaths.TempDir(t)
	typesDir := filepath.Join(root, "types", "rust", fixture)
	output, err := Generate(schema, Options{SchemaName: fixture, Registry: reg})
	if err != nil {
		t.Fatalf("generate rust types: %v", err)
	}
	if err := SetLocalPaths(output, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, typesDir); err != nil {
		t.Fatal(err)
	}
	orm, err := os.ReadFile(filepath.Join(typesDir, "src", ORMFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(orm), `\"resolve\":`) && !strings.Contains(string(orm), `"resolve":`) {
		t.Errorf("src/%s's definition carries no page's resolve statement", ORMFile)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: fixture})
	if err != nil {
		t.Fatalf("generate the Postgres DDL: %v", err)
	}
	crateDir := filepath.Join(root, "server", "audience-queue")
	for _, dir := range []string{"src", "tests", "postgres"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := sqlgen.WriteDDL(ddl, filepath.Join(crateDir, "postgres")); err != nil {
		t.Fatalf("write the Postgres DDL: %v", err)
	}
	postgresDDL := filepath.Join(crateDir, "postgres", "create.sql")
	pgtest.WriteCreateSQL(t, postgresDDL, postgresDDL)
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for file, content := range map[string]string{
		"Cargo.toml":                    fmt.Sprintf(audienceQueueManifest, output.CrateName, filepath.ToSlash(typesDir), runtimeDependency(true, paths.HTTPRuntimeRust)),
		"Cargo.lock":                    string(lock),
		"src/lib.rs":                    "//! The audience queue's repositories: see tests/.\n",
		"tests/audience_queue_rules.rs": audienceQueueRulesTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(root, "target")
	}
	test := exec.Command(cargo, "test", "--quiet", "--", "--nocapture")
	test.Dir = crateDir
	test.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	out, err := test.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test in %s: %v\n%s", crateDir, err, out)
	}
	if !strings.Contains(string(out), "the repositories ran on Postgres") {
		t.Errorf("the Postgres cases did not run:\n%s", out)
	}
	for _, name := range audienceQueueRulesCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// audienceQueueManifest is the test crate's Cargo.toml, given the types
// crate's name, its directory and its dependency on the HTTP runtime.
const audienceQueueManifest = `# The repositories of a database whose pages inherit their folder's
# audience through a queue (D156), over its generated Rust types, on
# Postgres.
[package]
name = "rustgen-audience-queue"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
serde = "1.0.228"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt"] }
db = { package = %q, path = %q, features = ["postgres"] }
%s
# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
