package generator

import (
	"os"
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

// TestARustServerOverAHeldDatabaseChecksItsBehaviorOperationsReach builds
// fixture-audience-behaviors-db and fixture-audience-behaviors-api, a Rust
// server of the Task's behavior operations, as build-all does (D156, D181):
// the database's rows keep to a caller, so the server's crate depends on
// the database's Rust types with their Postgres repositories, re-exports
// them, and each behavior operation checks the caller's reach of its row
// through them before the core runs. rustrestgen's
// TestRustBehaviorOperationsKeepToTheCallerOnPostgres runs such a crate.
func TestARustServerOverAHeldDatabaseChecksItsBehaviorOperationsReach(t *testing.T) {
	const db, api = "fixture-audience-behaviors-db", "fixture-audience-behaviors-api"
	outputRoot := testpaths.TempDir(t)
	load := func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
		t.Helper()
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if service == db {
			// Rust's types of an audience are Postgres's (D182): the
			// database is built for Postgres alone, with the Go types the
			// DB kind's Go ORM imports.
			cfg.Outputs["types"] = map[string]any{"rust": map[string]any{"enabled": true}, "go": map[string]any{"enabled": true}}
			cfg.Outputs["sql"] = map[string]any{"dialects": []any{"postgres"}}
		}
		return schema, cfg
	}
	for _, service := range []string{db, api} {
		schema, cfg := load(service)
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      testpaths.Local(t),
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
	read := func(file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(APIDir(outputRoot, api), file))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	crate := naming.Default().RustTypesCrate(db)
	for file, want := range map[string]string{
		"Cargo.toml":       crate + ` = { path = "`,
		"src/lib.rs":       "as database_types;",
		"src/behaviors.rs": "reach(&tx, ctx, type_name, &key, use_id, operation).await?;",
	} {
		if !strings.Contains(read(file), want) {
			t.Errorf("%s lacks %q:\n%s", file, want, read(file))
		}
	}
	if manifest := read("Cargo.toml"); !strings.Contains(manifest, `features = ["postgres"]`) || !strings.Contains(manifest, crate) {
		t.Errorf("Cargo.toml depends on no Postgres repositories of %s:\n%s", db, manifest)
	}
}
