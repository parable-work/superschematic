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

//go:embed testdata/references_go.go
var referencesGoTest string

// referencesGoCases are the cases testdata/references_go.go runs: D154's
// "done when" in the Go repositories (D184), the TypeScript repositories'
// cases (tsgen's referencesCases) and the Rust ones'.
var referencesGoCases = []string{
	"a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks",
	"a ref block in a row's thread is a reference of the row, which the target lists",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a target's delete clears the references to it and tells each row that held one",
	"an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it",
}

// TestGoReferencesEndToEnd generates fixture-references-db's Go types and
// ORM (D154, D184), writes testdata/references_go.go into the ORM's module
// and runs it, after go vet, over the ORM's References and Attachments on
// the Postgres behaviorServerDatabaseEnv names, in a schema of its own
// with the database's create.sql applied, through a bucket in memory that
// checks each upload's PUT against the URL's signature: every case of
// referencesGoCases must pass. It skips without the database and in
// -short mode.
func TestGoReferencesEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go repositories' references and attachments in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go repositories' references and attachments against Postgres")
	}
	const db = "fixture-references-db"
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
		Paths:            testpaths.Local(t),
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
	if err := os.WriteFile(filepath.Join(ormDir, "references_test.go"), []byte(referencesGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "REFERENCES_TEST_DATABASE_URL=" + dsn}
	var out []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "TestReferences", "./..."},
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
	for _, name := range referencesGoCases {
		if want := "--- PASS: TestReferences/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}
