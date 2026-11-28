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
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/behavior_timers_sqlite_go.go
var behaviorTimersSQLiteGoTest string

// behaviorTimersSQLiteGoCases are the cases behaviorTimersSQLiteGoTest
// runs, by name.
var behaviorTimersSQLiteGoCases = []string{
	"a due deadline the worker runs moves the status and records the outcome",
	"in drain mode the worker stops once its queues are idle",
}

// TestTimersGoWorkerOnSQLite builds fixture-timers-db with the sqlite
// dialect listed first and fixture-timers-api as a Go API over it (#717):
// the API's TicketTimersQueues takes the ORM's SQLiteDatabase and claims
// Ticket's deadline and change queues on its file. In the generated API
// module, over a file the build's SQLite DDL creates, the queues run a
// due deadline through the behaviors' core and spend its message, and in
// drain mode stop once idle, with no slot and no next due time. It then
// plans the behavior worker's entrypoint, which opens the SQLite file, and
// vets it. It skips in -short mode.
func TestTimersGoWorkerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go behavior worker on SQLite in -short mode")
	}
	paths := testpaths.Local(t)
	outputRoot := testpaths.TempDir(t)
	configure := func(service string, cfg *schemaconfig.SchemaConfig) {
		if service != "fixture-timers-db" {
			return
		}
		sql, _ := cfg.Outputs["sql"].(map[string]any)
		if sql == nil {
			sql = map[string]any{}
			cfg.Outputs["sql"] = sql
		}
		sql["dialects"] = []any{"sqlite", "postgres"}
	}
	schemas := map[string]*ir.Schema{}
	for _, service := range []string{"fixture-timers-db", "fixture-timers-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		configure(service, cfg)
		schemas[service] = schema
		if _, err := Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				return loader.LoadService(filepath.Join(tsFixtures, name))
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
				if err != nil {
					return nil, false
				}
				configure(name, cfg)
				return cfg, true
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-timers-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, "fixture-timers-db"), SQLiteSubdir, "create.sql"))
	if err != nil {
		t.Fatalf("the build wrote no SQLite DDL for fixture-timers-db: %v", err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "testdata", "sqlite.sql"), ddl, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_timers_sqlite_test.go"), []byte(behaviorTimersSQLiteGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=modernc.org/sqlite@" + sqliteDriverVersion},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "^TestTimersOnSQLite$", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range behaviorTimersSQLiteGoCases {
		if want := "--- PASS: TestTimersOnSQLite/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetBehaviorWorkerEntrypointOn(t, outputRoot, paths, schemas["fixture-timers-api"], schemas["fixture-timers-db"], env, "timers-stack", "TicketTimers", true)
	main, err := os.ReadFile(filepath.Join(servergen.ServerDir(outputRoot, "timers-stack", "fixture-timers-api-ticket-timers"), servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), "sqlitefile.Open(") {
		t.Errorf("the behavior worker's entrypoint does not open the SQLite file:\n%s", main)
	}
}
