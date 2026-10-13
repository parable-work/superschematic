package generator

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// The engine fixture: a support desk's DB service whose customers and
// agents are tables and whose accounts and tickets are engine types, with
// Workflow, Comments, Timers and a Links from a ticket to its account.
const (
	engineFixture       = "fixture-engine-db"
	engineFixtureDir    = "../loader/testdata/services/fixture-engine-db-json"
	engineFixtureGolden = "testdata/engine-document"
	engineFixtureTables = "agent, customer"
)

// buildEngineFixture builds the fixture as build does, with a fixed clock,
// and returns the output root, the registry and the result.
func buildEngineFixture(t *testing.T) (string, *registry.Registry, *registry.Result) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatalf("load %s: %v", engineFixture, err)
	}
	reg := CoreRegistry(naming.Default())
	root := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC))
	result, err := Run(schema, cfg, Options{OutputRoot: root, Registry: reg, Clock: clock})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return root, reg, result
}

// checkEngineGoldens holds each output to its golden in
// testdata/engine-document, or rewrites the golden under -update.
func checkEngineGoldens(t *testing.T, goldens map[string][]byte) {
	t.Helper()
	for name, want := range goldens {
		path := filepath.Join(engineFixtureGolden, name)
		if *updateNamingGolden {
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (rewrite it with -update)", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale; rewrite it with -update", path)
		}
	}
}

// TestEngineTypesHaveNoTableOrRepository builds the engine fixture and
// holds the SQL and the ORM it writes to the goldens in
// testdata/engine-document: sql and orm leave the engine types out, so the
// DDL of both dialects creates the tables alone and the ORM has their
// repositories alone (D58). Rewrite them with:
//
//	go test ./internal/generator -run TestEngineTypesHaveNoTableOrRepository -update
func TestEngineTypesHaveNoTableOrRepository(t *testing.T) {
	root, _, _ := buildEngineFixture(t)
	goldens := map[string][]byte{}
	for name, path := range map[string]string{
		"create.sql.golden":        filepath.Join(SQLDir(root, engineFixture), "create.sql"),
		"sqlite-create.sql.golden": filepath.Join(SQLDir(root, engineFixture), SQLiteSubdir, "create.sql"),
		"orm-interfaces.go.golden": filepath.Join(ORMDir(root, engineFixture), "interfaces.go"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		goldens[name] = content
	}
	checkEngineGoldens(t, goldens)

	entries, err := os.ReadDir(ORMDir(root, engineFixture))
	if err != nil {
		t.Fatal(err)
	}
	var repositories []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "repository_") {
			repositories = append(repositories, entry.Name())
		}
	}
	if want := []string{"repository_agent.go", "repository_customer.go"}; !reflect.DeepEqual(repositories, want) {
		t.Errorf("the ORM's repositories = %v, want %v", repositories, want)
	}
}

// An engine type gets no table in a migration model either: the plan from
// an empty database, for Postgres and SQLite, creates the tables alone
// (D27, D58).
func TestMigrationModelsLeaveEngineTypesOut(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	outputs, err := ParseOutputs(cfg.Outputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, dialect := range []sqlmigrate.Dialect{sqlmigrate.Postgres, sqlmigrate.SQLite} {
		model, err := sqlmigrate.BuildModel(schema, SQLOptions(cfg, outputs, nil, naming.Default()), dialect)
		if err != nil {
			t.Fatalf("%s: BuildModel: %v", dialect, err)
		}
		var tables []string
		for _, table := range model.Tables {
			tables = append(tables, table.Name)
		}
		if got := strings.Join(tables, ", "); got != engineFixtureTables {
			t.Errorf("%s: tables = %s, want %s", dialect, got, engineFixtureTables)
		}
	}
}
