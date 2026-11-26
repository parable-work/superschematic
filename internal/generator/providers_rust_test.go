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

//go:embed testdata/providers_rust.rs
var providersRustTest string

// providersRustCases are the cases testdata/providers_rust.rs runs: D159's
// "done when" in the generated Rust server, with a stub provider of each
// interface from examples/runtime-providers/rust.
var providersRustCases = []string{
	"the provider crates the API lists run in the server with no change to core, each as its own principal",
	"swapping one SMS provider for another is only a config change",
	"a credential reaches the runner alone, and the record holds its reference and time",
	"a config that names an unknown provider or leaves a setting unset is refused at start, naming no value",
	"a provider that calls the app is authenticated as itself, with its role alone",
}

// TestRustProvidersEndToEnd builds fixture-providers-db's Rust types and
// fixture-providers-api as a Rust server (D159, D185), whose config lists
// the provider crates of examples/runtime-providers/rust and names a slot
// per interface, and runs testdata/providers_rust.rs over it on Postgres,
// in a schema of its own at SUPERSCHEMATIC_RULES_TEST_DATABASE_URL: the
// API crate's providers module reads the providers an environment names,
// Deps holds them, a channel adapter records its deliveries through the
// repositories as its own principal, and the router serves them to a
// provider that calls the app with its key. Every case of
// providersRustCases must run.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustProvidersEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's runtime providers in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's runtime providers against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's runtime providers")
	}
	const db, api = "fixture-providers-db", "fixture-providers-api"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	load := func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		switch service {
		case db:
			types["go"] = map[string]any{"enabled": true}
		case api:
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
		return schema, cfg
	}
	for _, service := range []string{db, api} {
		schema, cfg := load(service)
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				schema, _ := load(name)
				return schema, nil
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg := load(name)
				return cfg, true
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, api)
	providers, err := os.ReadFile(filepath.Join(apiDir, "src", "providers.rs"))
	if err != nil {
		t.Fatalf("the API crate's providers: %v", err)
	}
	for _, want := range []string{`("SMS", "channel")`, `sms_relay_a::definition()`, `set("SMS_TOKEN", config.sms_token.to_string(), true);`} {
		if !strings.Contains(string(providers), want) {
			t.Errorf("src/providers.rs lacks %s:\n%s", want, providers)
		}
	}
	crateDir := filepath.Join(outputRoot, "server", "providers")
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
	examples := filepath.Join(testpaths.RepoRoot(t), "examples", "runtime-providers", "rust")
	for name, content := range map[string]string{
		"Cargo.toml": fmt.Sprintf(providersRustManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir),
			naming.Default().RustTypesCrate(db), filepath.ToSlash(TypesDir(outputRoot, "rust", db)),
			filepath.ToSlash(filepath.Join(examples, "sms-relay-a")), filepath.ToSlash(filepath.Join(examples, "sms-relay-b")),
			filepath.ToSlash(filepath.Join(examples, "vault-broker"))),
		"Cargo.lock":         string(lock),
		"src/lib.rs":         "//! The generated Rust server of fixture-providers-api: see tests/.\n",
		"tests/providers.rs": providersRustTest,
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
		{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet", "--", "--nocapture"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "PROVIDERS_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/providers.rs:\n%s", out)
	for _, name := range providersRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// providersRustManifest is the test crate's Cargo.toml, given the API
// crate's name and directory and the directories of the provider crates
// the test reads the stubs of.
const providersRustManifest = `# The generated Rust server of fixture-providers-api, its runtime
# providers end to end (D159, D185).
[package]
name = "providers-rust"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread", "time"] }
tower = { version = "0.5", features = ["util"] }
tracing = { version = "0.1.41", default-features = false, features = ["std"] }
api = { package = %q, path = %q }
types = { package = %q, path = %q, features = ["postgres"] }
sms-relay-a = { path = %q }
sms-relay-b = { path = %q }
vault-broker = { path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
