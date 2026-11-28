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
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed testdata/rowfeatures_go_server.go
var rowFeaturesGoServerTest string

// rowFeaturesGoCases are the cases testdata/rowfeatures_go_server.go runs:
// D154's "done when" through the generated Go server's routes (#723).
var rowFeaturesGoCases = []string{
	"a file uploaded straight to the bucket through the signed URL is filed onto a second row, which lists the first in its backlinks",
	"a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count",
	"a reference is added, listed and removed through the routes",
	"an upload whose bytes differ is refused, and a file of another row is not found",
	"a permission the set names for an operation replaces the class's",
}

// TestRowFeaturesGoServerEndToEnd builds fixture-references-db's Go types
// and ORM and fixture-references-api's Go API module and SDK, writes
// testdata/rowfeatures_go_server.go into the API module and runs it, after
// go vet, over the generated routes on the Postgres
// behaviorServerDatabaseEnv names, in a schema of its own with the
// database's create.sql applied, through a bucket in memory that checks
// each upload's PUT against the URL's signature: every case of
// rowFeaturesGoCases must pass. It skips without the database and in
// -short mode.
func TestRowFeaturesGoServerEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's references and files in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's references and files against Postgres")
	}
	const db, api = "fixture-references-db", "fixture-references-api"
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{db, api} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if service == db {
			// The database's Go types and ORM, beside its TypeScript ones,
			// on Postgres alone.
			types, _ := cfg.Outputs["types"].(map[string]any)
			types["go"] = map[string]any{"enabled": true}
			cfg.Outputs["sql"] = map[string]any{"dialects": []any{"postgres"}}
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	apiDir := APIDir(outputRoot, api)
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, db), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "rowfeatures_server_test.go"), []byte(rowFeaturesGoServerTest), 0o644); err != nil {
		t.Fatal(err)
	}
	names := naming.Default()
	sdkModule := names.GoSDKModule(api)
	dbTypes := names.GoTypesModule(db)
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t), "ROW_FEATURES_TEST_DATABASE_URL=" + dsn}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/" + api,
			"-require=" + dbTypes + "@v0.0.0-00010101000000-000000000000"},
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "-run", "TestRowFeatures", "./..."},
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
	for _, name := range rowFeaturesGoCases {
		if want := "--- PASS: TestRowFeatures/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}
