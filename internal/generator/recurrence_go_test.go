package generator

import (
	_ "embed"
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

//go:embed testdata/recurrence_go.go
var recurrenceGoTest string

// recurrenceGoCases are the cases testdata/recurrence_go.go runs, by name:
// D152's "done when" in the Go server (D187), the TypeScript server's
// cases (tsrestgen's recurrenceCases) and the Rust one's (D188).
var recurrenceGoCases = []string{
	"setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines",
	"a rule change leaves the open occurrence untouched and replans the ones that have not opened",
	"the remind deadline fires on the worker, records the reminder and emits the opening and the reminder",
	"complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it",
	"without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400",
	"the calendar merges a contract end date and a filing occurrences, each dated as its source says",
	"a raw SQL write of a filing holiday calendar is heard by the worker, which plans again",
	"a recurrence given at create is set in the create, its occurrences planned and its deadlines armed, and held to filings.manage",
	"a holiday calendar whose weekend is Friday and Saturday moves a due date off Saturday to the Sunday",
	"a write of a contract end date replans the relative recurrence of its filings with no call of the app",
}

// recurrenceGoServices loads fixture-recurrence-db and
// fixture-recurrence-api for a Go server: the database's Go types on, the
// API served in Go, and the Rust outputs, which a Go server does not
// read, off. dialects, when set, are the database's.
func recurrenceGoServices(t *testing.T, dialects []any) func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
	return func(service string) (*ir.Schema, *schemaconfig.SchemaConfig) {
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
		types["go"] = map[string]any{"enabled": true}
		delete(types, "rust")
		if sdk, ok := cfg.Outputs["sdk"].(map[string]any); ok {
			delete(sdk, "rust")
		}
		switch service {
		case "fixture-recurrence-db":
			if dialects != nil {
				cfg.Outputs["sql"] = map[string]any{"dialects": dialects}
			}
		case "fixture-recurrence-api":
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": APILanguageGo}
		}
		return schema, cfg
	}
}

func runRecurrenceGo(t *testing.T, outputRoot string, dialects []any) error {
	t.Helper()
	load := recurrenceGoServices(t, dialects)
	for _, service := range []string{"fixture-recurrence-db", "fixture-recurrence-api"} {
		schema, cfg := load(service)
		_, err := Run(schema, cfg, Options{
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
		})
		if err != nil {
			if service == "fixture-recurrence-db" {
				t.Fatalf("run %s: %v", service, err)
			}
			return err
		}
	}
	return nil
}

// TestGoRecurrenceEndToEnd builds fixture-recurrence-db's Go types and
// ORM and fixture-recurrence-api as a Go server (D152, D187), then writes
// testdata/recurrence_go.go into the API's module and runs it, after go
// vet, on the Postgres behaviorServerDatabaseEnv names, in a schema of its
// own with the database's create.sql applied: it calls Filing's
// Recurrence operations through the generated routes, runs the behavior
// worker FilingDeadlines' generated queues in process, and reads the
// calendar through the schema runtime's crosstable over the ORM, and
// every case of recurrenceGoCases must pass. It skips without the
// database, without the core's archive, and in -short mode.
func TestGoRecurrenceEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's Recurrence in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's Recurrence against Postgres")
	}
	outputRoot := t.TempDir()
	if err := runRecurrenceGo(t, outputRoot, nil); err != nil {
		t.Fatalf("run fixture-recurrence-api: %v", err)
	}
	apiDir := APIDir(outputRoot, "fixture-recurrence-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-recurrence-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "recurrence_test.go"), []byte(recurrenceGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "TestRecurrence", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range recurrenceGoCases {
		if want := "--- PASS: TestRecurrence/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}

// TestAGoServerOnSQLiteBuildsRecurrenceAndTheCalendar: with sqlite listed
// first in fixture-recurrence-db's dialects, the API's Go server is built,
// as the behaviors' core runs Recurrence in the SQLite repositories'
// transactions and the SQLite database's CrossTables reads the calendar
// (#615), and outside -short mode its module vets; a Rust server is still
// refused them there (D188). The cases themselves run on a SQLite file in
// ormgen's TestTheGoReadsAcrossTablesOnSQLite.
func TestAGoServerOnSQLiteBuildsRecurrenceAndTheCalendar(t *testing.T) {
	outputRoot := t.TempDir()
	if err := runRecurrenceGo(t, outputRoot, []any{"sqlite", "postgres"}); err != nil {
		t.Fatalf("the Go server on SQLite: %v", err)
	}
	if testing.Short() {
		return
	}
	apiDir := APIDir(outputRoot, "fixture-recurrence-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
