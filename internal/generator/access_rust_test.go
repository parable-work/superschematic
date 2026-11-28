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

//go:embed testdata/access_rust.rs
var accessRustTest string

// accessRustCases are the cases testdata/access_rust.rs runs, by name:
// D156's "done when" in the Rust server, the TypeScript server's cases
// (tsrestgen's accessCases), the calendar feed's (D152) among them since
// D188.
var accessRustCases = []string{
	"a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused",
	"a private row never appears to another user in a list, a read, a search or a count",
	"a named row is visible to its owner and to the principals it names",
	"a member of a nested team sees a row named for the outer team, and stops seeing it once removed",
	"a document inherits its folder's audience, and follows it when the folder's changes",
	"an agent run sees what whom it acts for sees, and writes no read-only row",
	"a row rule follows a path through the table's relations",
	"a party with a grant reaches only the rows its role's paths allow",
	"an expired or revoked grant is refused on the very next call",
	"a reveal opens the value for a holder of its permission and writes an access-log row",
}

// TestRustAccessEndToEnd builds fixture-access-db's Rust types and
// fixture-access-api as a Rust server (D156, D181), and serves its router
// over the Rust repositories on Postgres, in a schema of its own at
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL with the database's create.sql
// applied: testdata/access_rust.rs drives it through the router with the
// runtime's GrantAuthenticator and access log, and every case of
// accessRustCases must run, the calendar feed's through the runtime's
// orm::calendar routes over the database's @calendarSource (D152, D188).
// Its contract terms are sealed with a key the API's config holds, as no
// fixture declares one.
//
// The test needs cargo, which it skips without, the database, and runs
// outside -short.
func TestRustAccessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's per-record access in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Rust server's per-record access against Postgres")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's per-record access")
	}
	const db, api = "fixture-access-db", "fixture-access-api"
	const keyType, keyField = "AccessKeys", "ACCESS_FIELD_KEYS"
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
			// The Go ORM, which a DB service always generates, imports
			// its Go types.
			types["go"] = map[string]any{"enabled": true}
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{"enabled": true}
				cfg.Outputs["sql"] = sql
			}
			sql["encryption"] = map[string]any{"key": keyType + "." + keyField}
		case api:
			schema.Types[keyType] = &ir.TypeDef{
				Name:    keyType,
				Role:    ir.RoleEmbeddedStruct,
				EnvVars: true,
				Fields: []*ir.FieldDef{{
					Name:     keyField,
					TypeRef:  ir.TypeRef{Name: "string"},
					Required: true,
					Secret:   true,
				}},
			}
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageRust}
		}
		return schema, cfg
	}
	for _, service := range []string{db, api} {
		schema, cfg := load(service)
		result, err := Run(schema, cfg, Options{
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
		})
		if err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
		// Every operation is served: the row rule's path among them.
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "D156") || strings.Contains(warning, "implementation") {
				t.Errorf("%s: the build warned: %s", service, warning)
			}
		}
	}

	apiDir := APIDir(outputRoot, api)
	crateDir := filepath.Join(outputRoot, "server", "access")
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
		"Cargo.toml":      fmt.Sprintf(accessRustManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir)),
		"Cargo.lock":      string(lock),
		"src/lib.rs":      "//! The generated Rust server of fixture-access-api: see tests/.\n",
		"tests/access.rs": accessRustTest,
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
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target, "ACCESS_TEST_DATABASE_URL="+dsn)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
	}
	t.Logf("tests/access.rs:\n%s", out)
	for _, name := range accessRustCases {
		if !strings.Contains(string(out), "case: "+name+"\n") {
			t.Errorf("the case %q did not run:\n%s", name, out)
		}
	}
}

// accessRustManifest is the test crate's Cargo.toml, given the API crate's
// name and its directory.
const accessRustManifest = `# The generated Rust server of fixture-access-api, its per-record access
# end to end (D156, D181).
[package]
name = "access-rust"
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
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
