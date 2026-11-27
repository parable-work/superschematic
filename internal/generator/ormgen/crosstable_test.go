package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// crossTablesOutput is the ORM of svc, written, and the directory it is
// in.
func crossTablesOutput(t *testing.T, svc string) (*ORMOutput, string) {
	t.Helper()
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, svc), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", svc, err)
	}
	output, err := Generate(schema, Options{
		SchemaName:  svc,
		ModulePath:  "example.com/schemas/orm/" + svc,
		TypesModule: "example.com/schemas/types/go/" + svc,
		Registry:    reg,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate %s: %v", svc, err)
	}
	dir := t.TempDir()
	if err := WriteORM(output, dir); err != nil {
		t.Fatalf("write %s: %v", svc, err)
	}
	return output, dir
}

// TestWriteORMGoldenCrossTables pins crosstable.go (D152, D155, D187): the
// calendar sources of fixture-recurrence-db (a contract's end date and a
// filing's occurrences) and fixture-access-go-db (a document's day, held
// to its audience, with the grants a feed is), and fixture-search-db's
// search index, its definitions tsgen's, member for member. A database
// with neither writes no crosstable.go, and requires the schema runtime
// for nothing else. Regenerate with
// go test ./internal/generator/ormgen -run TestWriteORMGoldenCrossTables -update
func TestWriteORMGoldenCrossTables(t *testing.T) {
	for _, svc := range []string{"fixture-recurrence-db", "fixture-access-go-db", "fixture-search-db"} {
		t.Run(svc, func(t *testing.T) {
			output, dir := crossTablesOutput(t, svc)
			if output.CrossTables == nil || !output.RequiresSchemaRuntime() {
				t.Fatalf("%s: CrossTables %v", svc, output.CrossTables)
			}
			got, err := os.ReadFile(filepath.Join(dir, "crosstable.go"))
			if err != nil {
				t.Fatal(err)
			}
			golden := filepath.Join("testdata", "golden", svc, "crosstable.go")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil || string(got) != string(want) {
				t.Errorf("crosstable.go of %s differs from its golden (run with -update to accept): %v", svc, err)
			}
		})
	}
	output, dir := crossTablesOutput(t, "fixture-db")
	if output.CrossTables != nil {
		t.Errorf("fixture-db's CrossTables: %+v", output.CrossTables)
	}
	if _, err := os.Stat(filepath.Join(dir, "crosstable.go")); !os.IsNotExist(err) {
		t.Errorf("fixture-db wrote crosstable.go: %v", err)
	}
}

// TestARecurrenceTargetKeepsItsJSONImport: fixture-recurrence-db's
// Contract, a Recurrence's relative target whose writes the core runs,
// has no use, deadline, change or event of its own, and its repository
// keeps encoding/json, which it imports for the behaviors, in use; Filing,
// which has them, needs no such line (D187).
func TestARecurrenceTargetKeepsItsJSONImport(t *testing.T) {
	_, dir := crossTablesOutput(t, "fixture-recurrence-db")
	for file, want := range map[string]bool{"repository_contract.go": true, "repository_filing.go": false} {
		text, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(text), "var _ = json.Marshal"); got != want {
			t.Errorf("%s keeps json in use: %t, want %t", file, got, want)
		}
	}
}
