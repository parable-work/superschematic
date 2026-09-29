package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const dbWithoutGoTypesError = "schema config for fixture-db: kind DB needs outputs.types.go: the Go ORM, which the DB kind always generates, imports the Go types"

// The DB kind always generates the Go ORM, which imports the schema's Go
// types, so a DB schema without outputs.types.go fails the run before
// anything is written.
func TestRunRefusesADBSchemaWithoutGoTypes(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Outputs["types"] = map[string]any{
		LangGo:         map[string]any{"enabled": false},
		LangTypeScript: map[string]any{"enabled": true},
	}
	out := t.TempDir()
	_, err = Run(schema, cfg, Options{OutputRoot: out, Naming: naming.Default()})
	if err == nil || err.Error() != dbWithoutGoTypesError {
		t.Fatalf("Run error = %v, want %q", err, dbWithoutGoTypesError)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("the refused run wrote %v", entries)
	}
}

// ExpectedOutputDirs refuses the same config, so build-all fails at
// discovery. A kind that runs no ORM needs no Go types.
func TestExpectedOutputDirsRefusesADBSchemaWithoutGoTypes(t *testing.T) {
	cfg := &schemaconfig.SchemaConfig{
		Name:    "fixture-db",
		Kind:    ir.SchemaKindDB,
		Outputs: map[string]any{"types": map[string]any{LangTypeScript: map[string]any{"enabled": true}}},
	}
	if _, err := ExpectedOutputDirs(t.TempDir(), cfg, "", nil); err == nil || err.Error() != dbWithoutGoTypesError {
		t.Fatalf("ExpectedOutputDirs error = %v, want %q", err, dbWithoutGoTypesError)
	}

	cfg.Kind = ir.SchemaKindGeneral
	if _, err := ExpectedOutputDirs(t.TempDir(), cfg, "", nil); err != nil {
		t.Fatalf("a General schema with TypeScript types only: %v", err)
	}
}

// The rule follows the ORM generator, not the DB kind's name: an extension
// kind whose pipeline runs it needs the Go types too.
func TestExpectedOutputDirsRefusesAnyKindThatRunsTheORMWithoutGoTypes(t *testing.T) {
	reg := registry.New(naming.Default())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterKind(registry.KindSpec{Name: "Ledger", Extension: "acme", Pipeline: []string{"sql", ormGenerator, typesGenerator}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	cfg := &schemaconfig.SchemaConfig{Name: "books", Kind: "Ledger", Outputs: map[string]any{}}
	want := "schema config for books: kind Ledger needs outputs.types.go: the Go ORM, which the Ledger kind always generates, imports the Go types"
	if _, err := ExpectedOutputDirs(t.TempDir(), cfg, "", reg); err == nil || err.Error() != want {
		t.Fatalf("ExpectedOutputDirs error = %v, want %q", err, want)
	}

	cfg.Outputs["types"] = map[string]any{LangGo: map[string]any{"enabled": true}}
	if _, err := ExpectedOutputDirs(t.TempDir(), cfg, "", reg); err != nil {
		t.Fatalf("with Go types: %v", err)
	}
}
