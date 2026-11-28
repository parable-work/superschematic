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
)

//go:embed testdata/search_go.go
var searchGoTest string

// searchGoCases are the cases testdata/search_go.go runs: D155's "done
// when" in the Go repositories and the schema runtime's crosstable
// package (D187), the TypeScript repositories' cases (tsgen's searchCases)
// and the Rust ones' (D188).
var searchGoCases = []string{
	"two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables",
	"a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing",
	"a row's conversation and files are indexed with it, and an encrypted field never is",
	"an uploaded file's text the file processor extracts is indexed with its row, and a file the processor fails on ends failed (D154, D159, #603)",
	"an index row follows its row: an audience change and a delete at once, a change of text once the queue drains",
	"scope keeps a search to the app's filter",
	"embeddings re-rank the first hits, are kept while the text holds, and can be switched off",
}

// TestGoSearchEndToEnd generates fixture-search-db's Go types and ORM
// (D155, D187), the contract's terms sealed under a key its config names,
// writes testdata/search_go.go into the ORM's module, which requires the
// HTTP runtime for its model providers, and runs it, after go vet, over
// the ORM's CrossTables on the Postgres behaviorServerDatabaseEnv names,
// in a schema of its own with the database's create.sql applied: every
// case of searchGoCases must pass. It skips without the database and in
// -short mode.
func TestGoSearchEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go search across tables in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go search across tables against Postgres")
	}
	const db = "fixture-search-db"
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, db))
	if err != nil {
		t.Fatalf("load %s: %v", db, err)
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	if types == nil {
		types = map[string]any{}
		cfg.Outputs["types"] = types
	}
	types["go"] = map[string]any{"enabled": true}
	// A Go ORM opens with the keys of the Encrypted<T> fields, which an
	// API's @envVars class names.
	cfg.Outputs["sql"] = map[string]any{"dialects": []any{"postgres"}, "encryption": map[string]any{"key": "SearchKeys.SEARCH_DB_FIELD_KEYS"}}
	if _, err := Run(schema, cfg, Options{
		OutputRoot:       outputRoot,
		Paths:            paths,
		Naming:           naming.Default(),
		Clock:            codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		DependencyConfig: func(string) (*schemaconfig.SchemaConfig, bool) { return nil, false },
	}); err != nil {
		t.Fatalf("run %s: %v", db, err)
	}
	ormDir := ORMDir(outputRoot, db)
	if err := os.MkdirAll(filepath.Join(ormDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(ormDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(ormDir, "search_test.go"), []byte(searchGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	httpRuntime := naming.Default().HTTPRuntimeGoModule
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "SEARCH_TEST_DATABASE_URL=" + dsn}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + httpRuntime + "@v0.0.0-00010101000000-000000000000", "-replace=" + httpRuntime + "=" + paths.HTTPRuntimeGo},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "TestSearch", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		cmd.Env = append(cmd.Environ(), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated ORM module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range searchGoCases {
		if want := "--- PASS: TestSearch/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}
