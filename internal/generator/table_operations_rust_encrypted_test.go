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
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/table_operations_encrypted.rs
var tableOperationsEncryptedTest string

// TestTableOperationsRustServerSealsAnEncryptedColumn builds
// fixture-tracker-db and fixture-tracker-api as a Rust server on a SQLite
// file, with a comment's body an Encrypted<T> column and the view's body
// its @decrypted member (D85, D116): the two marks are set on the loaded
// schemas, the database's config names its key secret, and the API's
// @envVars class holds it, since no fixture declares the three. The
// generated crate takes the keyring in Implementations::new, its
// tables::field_keys reads it from the API's config, and the router's
// handlers seal the column on a create and open it for the view, over the
// SQLite DDL the build writes.
//
// The test needs cargo, which it skips without, and runs outside -short.
func TestTableOperationsRustServerSealsAnEncryptedColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Rust server's encrypted column in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not found, skipping the generated Rust server's encrypted column")
	}
	const db, api = "fixture-tracker-db", "fixture-tracker-api"
	const keyType, keyField = "TrackerKeys", "TRACKER_FIELD_KEYS"
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	field := func(schema *ir.Schema, typeName, name string) *ir.FieldDef {
		t.Helper()
		td := schema.Types[typeName]
		if td == nil {
			td = schema.Inputs[typeName]
		}
		if td != nil {
			for _, fd := range td.Fields {
				if fd != nil && fd.Name == name {
					return fd
				}
			}
		}
		t.Fatalf("%s has no %s.%s", schema.Name, typeName, name)
		return nil
	}
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
			body := field(schema, "Comment", "body")
			body.EncryptedAtRest, body.Secret = true, true
			sql, _ := cfg.Outputs["sql"].(map[string]any)
			if sql == nil {
				sql = map[string]any{"enabled": true}
				cfg.Outputs["sql"] = sql
			}
			sql["dialects"] = []any{"sqlite", "postgres"}
			sql["encryption"] = map[string]any{"key": keyType + "." + keyField}
		case api:
			field(schema, "CommentView", "body").Decrypted = true
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
		// A view with a @decrypted member is served, with no warning that
		// leaves it to the project.
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "@decrypted") {
				t.Errorf("%s: the build warned: %s", service, warning)
			}
		}
	}

	apiDir := APIDir(outputRoot, api)
	tables, err := os.ReadFile(filepath.Join(apiDir, "src", "tables.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub fn field_keys(",
		"FieldKeyring::parse(&config.tracker_field_keys)",
		".with_keys(field_keys)",
	} {
		if !strings.Contains(string(tables), want) {
			t.Errorf("src/tables.rs lacks %q", want)
		}
	}
	create, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
	}
	crateDir := filepath.Join(outputRoot, "server", "tracker-encrypted")
	for _, dir := range []string{"src", "tests"} {
		if err := os.MkdirAll(filepath.Join(crateDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	lock, err := os.ReadFile(filepath.Join(paths.HTTPRuntimeRust, "Cargo.lock"))
	if err != nil {
		t.Fatalf("the HTTP runtime's lockfile: %v", err)
	}
	for name, content := range map[string]string{
		"Cargo.toml":         fmt.Sprintf(tableOperationsEncryptedManifest, naming.Default().RustAPICrate(api), filepath.ToSlash(apiDir)),
		"Cargo.lock":         string(lock),
		"create.sql":         string(create),
		"src/lib.rs":         "//! The generated Rust server of fixture-tracker-api with an encrypted column: see tests/.\n",
		"tests/encrypted.rs": tableOperationsEncryptedTest,
	} {
		if err := os.WriteFile(filepath.Join(crateDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(outputRoot, "target")
	}
	for _, args := range [][]string{
		{"clippy", "--quiet", "--manifest-path", filepath.Join(apiDir, "Cargo.toml"), "--", "-D", "warnings"},
		{"test", "--quiet"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = crateDir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cargo %s in %s: %v\n%s", args[0], crateDir, err, out)
		}
		t.Logf("cargo %s:\n%s", args[0], out)
	}
}

// tableOperationsEncryptedManifest is the test crate's Cargo.toml, given
// the API crate's name and its directory.
const tableOperationsEncryptedManifest = `# The generated Rust server of fixture-tracker-api with an Encrypted<T>
# column (D85, D116).
[package]
name = "tracker-encrypted"
version = "0.0.0"
edition = "2021"
publish = false

[dev-dependencies]
async-trait = "0.1.89"
axum = "0.8.9"
http = "1"
http-body-util = "0.1.3"
serde_json = "1.0.149"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
tower = { version = "0.5", features = ["util"] }
api = { package = %q, path = %q }

# Its own workspace, so no manifest above the test's directory claims it.
[workspace]
`
