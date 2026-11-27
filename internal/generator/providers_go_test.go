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

//go:embed testdata/providers_go.go
var providersGoTest string

// providersGoCases are the cases testdata/providers_go.go runs: D159's
// "done when" in a generated Go server (D184), over the Go modules of
// examples/runtime-providers' providers, and the Deps that hold its
// database's references and attachments (D154).
var providersGoCases = []string{
	"the credential provider and a channel adapter run with no change to core",
	"swapping one SMS provider for another is only a config change",
	"a config that names an unknown provider, one of another interface, or leaves a setting unset is refused at start",
	"each call into a provider runs as its own principal, which the ORM and the routes hold to",
	"a sandbox, a model and a file processor of Go run through their slots",
	"the server's Deps hold the database's references and attachments",
}

// TestGoProvidersEndToEnd builds fixture-references-db and
// fixture-providers-go-api as build-all does, then writes
// testdata/providers_go.go into the generated API module and runs it,
// after go vet: the providers.go apigen writes for the API's slots, over
// the provider modules its config lists, which go.mod replaces with their
// directories, on the Postgres behaviorServerDatabaseEnv names, in a schema
// of its own. Every case of providersGoCases must pass. It skips without
// the database and in -short mode.
func TestGoProvidersEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's providers in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's providers against Postgres")
	}
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{"fixture-references-db", "fixture-providers-go-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if service == "fixture-references-db" {
			types, _ := cfg.Outputs["types"].(map[string]any)
			if types == nil {
				types = map[string]any{}
				cfg.Outputs["types"] = types
			}
			types["go"] = map[string]any{"enabled": true}
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          testpaths.Local(t),
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	apiDir := APIDir(outputRoot, "fixture-providers-go-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-references-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "providers_test.go"), []byte(providersGoTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
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
	for _, name := range providersGoCases {
		if want := "--- PASS: TestProviders/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}
