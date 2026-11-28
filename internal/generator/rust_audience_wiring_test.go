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
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRustAPIWiresTheAudienceQueue is TestGoAPIWiresTheAudienceQueue for a
// Rust API (#734): fixture-audience-queue-api built in Rust over
// fixture-audience-queue-db, as build-all builds it. The API crate's
// src/wiring.rs runs the @worker of the pages' @audience queue through
// orm::resolve_audience (Wired::run_audience_resolving), so Workers has no
// method for it, and the crate passes clippy with warnings denied. It
// needs cargo and runs outside -short.
func TestRustAPIWiresTheAudienceQueue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Rust API's audience queue wiring in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the Rust API's audience queue wiring")
	}
	const db, api = "fixture-audience-queue-db", "fixture-audience-queue-api"
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{db, api} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	configs[api].Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
	configs[api].Outputs["types"] = map[string]any{"rust": map[string]any{"enabled": true}}
	for _, service := range []string{db, api} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot:     outputRoot,
			Paths:          testpaths.Local(t),
			Naming:         naming.Default(),
			Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			RepositoryRoot: repoRoot,
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
				if cfg, ok := configs[name]; ok {
					return cfg, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	apiDir := APIDir(outputRoot, api)
	wiring, err := os.ReadFile(filepath.Join(apiDir, "src", rustrestgen.WiringFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub async fn run_audience_resolving(",
		"PostgresQueue::<AudienceResolving>::new(self.tables.pool().clone(), definition)",
		"resolve_audience(&client, definition, &message).await?;",
	} {
		if !strings.Contains(string(wiring), want) {
			t.Errorf("src/wiring.rs lacks %s:\n%s", want, wiring)
		}
	}
	deps, err := os.ReadFile(filepath.Join(apiDir, "src", "deps.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(deps), "async fn resolve_audience(") {
		t.Errorf("Workers has a method for the audience queue's worker:\n%s", deps)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(repoRoot, "target")
	}
	cmd := exec.Command(cargo, "clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings")
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo clippy of %s: %v\n%s", api, err, out)
	}

	// The generated worker on Postgres: testdata/audience_wiring_rust.rs, a
	// crate of the test's own over the API crate, changes a folder's
	// audience and runs Wired::run_audience_resolving until its pages
	// follow.
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Log("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is unset: the generated worker's run on Postgres is skipped")
		return
	}
	crateDir := filepath.Join(repoRoot, "audience-wiring")
	if err := os.MkdirAll(filepath.Join(crateDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	lock, err := os.ReadFile(filepath.Join(testpaths.Local(t).HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":        fmt.Sprintf(audienceWiringManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir)),
		"Cargo.lock":        string(lock),
		"src/lib.rs":        "//! The generated audience worker of fixture-audience-queue-api: see tests/.\n",
		"tests/audience.rs": audienceWiringTest,
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(crateDir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := exec.Command(cargo, "test", "--quiet", "--", "--nocapture")
	run.Dir = crateDir
	run.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "AUDIENCE_CREATE_SQL="+filepath.Join(SQLDir(outputRoot, db), "create.sql"),
		// The API's config names its database, which the wiring's pool is.
		"FIXTURE_AUDIENCE_QUEUE_DB_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("cargo test of the generated audience worker: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the generated audience worker ran on Postgres") {
		t.Fatalf("the generated audience worker did not run on Postgres:\n%s", out)
	}
	t.Logf("cargo test:\n%s", out)
}

// audienceWiringTest is the test crate's test, the API crate's generated
// worker over Postgres.
//
//go:embed testdata/audience_wiring_rust.rs
var audienceWiringTest string

// audienceWiringManifest is the test crate's Cargo.toml, given the API
// crate's name and its directory.
const audienceWiringManifest = `# The generated audience worker of fixture-audience-queue-api on Postgres (#734).
[package]
name = "audience-wiring"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
