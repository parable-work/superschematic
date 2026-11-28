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

//go:embed testdata/parties_go.go
var partiesGoTest string

// partiesGoCases are the cases testdata/parties_go.go runs: D157's "done
// when" in the Go repositories and the HTTP runtime's inbound package
// (D192), the TypeScript repositories' cases (tsgen's partiesCases) and
// the Rust ones' (D193).
var partiesGoCases = []string{
	"a text with a photo from a party with a grant becomes a row on the granted record, with the photo attached",
	"a text from an unknown number lands in triage",
	"the same text from a party whose grant has ended reaches nothing, until its identity is verified again",
	"email failing DMARC is treated as unknown, and an authenticated email threads to its party",
	"a party with no grant is on its timeline and in triage, and a user's text is the notifier's reply",
	"two parties sharing an address are told apart by the thread, and held in triage otherwise",
	"sending to a party takes the permission, keeps to the rate limit, waits for approval when asked, and stops after STOP",
}

// goRuntimeModule generates db's Go types and ORM under a temporary output
// root, writes source as <name>_test.go into the ORM's module with the
// database's create.sql, requires the HTTP runtime and modules (each
// module path to its directory), and runs go vet and go test -run run in
// it with env: the test's output.
func goRuntimeModule(t *testing.T, db, name, source, run string, modules map[string]string, env ...string) string {
	t.Helper()
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
	if err := os.WriteFile(filepath.Join(ormDir, name+"_test.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	httpRuntime := naming.Default().HTTPRuntimeGoModule
	env = append([]string{"CGO_LDFLAGS=" + archiveLDFlags(t)}, env...)
	edit := []string{"mod", "edit", "-require=" + httpRuntime + "@v0.0.0-00010101000000-000000000000", "-replace=" + httpRuntime + "=" + paths.HTTPRuntimeGo}
	// A source that runs on SQLite (#618) reads the SQLite DDL the build
	// wrote, through the driver its module requires.
	if strings.Contains(source, `"modernc.org/sqlite"`) {
		ddl, err := os.ReadFile(filepath.Join(SQLDir(outputRoot, db), SQLiteSubdir, "create.sql"))
		if err != nil {
			t.Fatalf("the build wrote no SQLite DDL for %s: %v", db, err)
		}
		if err := os.WriteFile(filepath.Join(ormDir, "testdata", "sqlite.sql"), ddl, 0o644); err != nil {
			t.Fatal(err)
		}
		edit = append(edit, "-require=modernc.org/sqlite@"+sqliteDriverVersion)
	}
	for _, path := range sortedModuleKeys(modules) {
		edit = append(edit, "-require="+path+"@v0.0.0-00010101000000-000000000000", "-replace="+path+"="+modules[path])
	}
	var out []byte
	for _, args := range [][]string{
		edit,
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", run, "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = ormDir
		cmd.Env = append(cmd.Environ(), env...)
		out, err = cmd.CombinedOutput()
		if err != nil {
			if keep := os.Getenv("GO_E2E_KEEP"); keep != "" {
				_ = exec.Command("cp", "-R", ormDir, keep).Run()
			}
			t.Fatalf("go %s in the generated ORM module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	return string(out)
}

// TestGoPartiesEndToEnd generates fixture-parties-db's Go types and ORM
// (D157, D192), writes testdata/parties_go.go into the ORM's module, which
// requires the HTTP runtime for its inbound, notify and providers
// packages, and runs it, after go vet, over the ORM's Orchestration on the
// Postgres behaviorServerDatabaseEnv names, in a schema of its own with the
// database's create.sql applied, with a stand-in SMS relay and mail relay
// as the channel adapters and a bucket in memory: the test's drain hands
// each run request to a stub handler that writes a report on the granted
// job as the party, as tsgen's parties.test.ts does, and every case of
// partiesGoCases must pass. The relay's token never reaches the test's
// output. It skips without the database and in -short mode.
func TestGoPartiesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go inbound path in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go inbound path against Postgres")
	}
	out := goRuntimeModule(t, "fixture-parties-db", "parties", partiesGoTest, "TestParties", nil, "PARTIES_TEST_DATABASE_URL="+dsn)
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range partiesGoCases {
		if want := "--- PASS: TestParties/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
	if strings.Contains(out, "relay-token") {
		t.Errorf("a relay's token reached the test's output:\n%s", out)
	}
}
