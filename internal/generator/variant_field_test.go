package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestVariantFieldFixturesBuild builds fixture-variant-field-db and
// fixture-variant-field-api as build-all does (D149): every output their
// configs turn on, the types of four languages, the database's SQL in both
// dialects and its ORMs, the API's server and its four SDKs, is written,
// the SQL holds the variant field's check, and the API's Go module, its
// types and the database's, builds and vets.
func TestVariantFieldFixturesBuild(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the variant field's build in -short mode")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{"fixture-variant-field-db", "fixture-variant-field-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
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
	sqlDir := SQLDir(outputRoot, "fixture-variant-field-db")
	for _, file := range []string{filepath.Join(sqlDir, "create.sql"), filepath.Join(sqlDir, SQLiteSubdir, "create.sql")} {
		text, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(text), "WHEN kind = 'verify'") && !strings.Contains(string(text), `WHEN "kind" = 'verify'`) {
			t.Errorf("%s holds no variant check:\n%s", file, text)
		}
	}
	apiDir := APIDir(outputRoot, "fixture-variant-field-api")
	for _, args := range [][]string{{"mod", "tidy"}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
