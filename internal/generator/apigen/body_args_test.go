package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// bodyArgsAPI is a schema local to apigen's testdata, so its operations
// change no other generator's goldens.
const bodyArgsAPI = "body-args-api"

var goModuleClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

func loadBodyArgsAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", bodyArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsAPI, err)
	}
	return schema
}

// TestWriteAPIGoldenBodyArgs pins routes.go and openapi.json of
// body-args-api: each body argument is built once per route as a
// bodyargs.Arg (its JSON kind, required, list bounds, the scalar's rules,
// then the argument's) and decoded by bodyargs.Value, List or ListOfLists.
// Regenerate with
// go test ./internal/generator/apigen -run TestWriteAPIGoldenBodyArgs -update
func TestWriteAPIGoldenBodyArgs(t *testing.T) {
	output, err := apigen.Generate(loadBodyArgsAPI(t), apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  bodyArgsAPI,
		ModulePath:  "example.com/schemas/api/" + bodyArgsAPI,
		TypesModule: "example.com/schemas/types/go/" + bodyArgsAPI,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", bodyArgsAPI), []string{"routes.go", "openapi.json"})
}

// TestBodyArgsRoutesApplyTheListRules generates the Go types and API
// modules of body-args-api into a temp tree laid out as a build writes it,
// copies testdata/body_args_routes_test.go into the API module and runs it.
// The routes read each body argument from its JSON value: a null list
// element is required at name[i], an element or value of the wrong JSON
// type is type, [] satisfies a required list and an absent one is
// required, listMin and listMax bound the list, the scalar's own rules and
// the argument's apply to each element named by the rule (D14), an enum
// element outside the enum is enum, a required builtin value must be
// present, and a Generic.JSON argument is any JSON value but null.
func TestBodyArgsRoutesApplyTheListRules(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	apiDir := writeGoAPIModule(t, loadBodyArgsAPI(t), bodyArgsAPI)
	test, err := os.ReadFile(filepath.Join("testdata", "body_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "body_args_routes_test.go"), test, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}

// writeGoAPIModule generates the Go types module and the Go API module of
// schema into a temp tree laid out as a build writes it and returns the
// API module's directory.
func writeGoAPIModule(t *testing.T, schema *ir.Schema, service string) string {
	t.Helper()
	typesModule := "example.com/schemas/types/go/" + service
	paths := testpaths.Local(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", service)
	apiDir := filepath.Join(root, "api", service)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: service,
		ModulePath: typesModule,
		Clock:      goModuleClock,
	})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}

	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  service,
		ModulePath:  "example.com/schemas/api/" + service,
		TypesModule: typesModule,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if err := apigen.SetReplacePaths(apiOutput, paths, apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(apiOutput, apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	return apiDir
}

// runGoAPIModule runs go mod tidy, go build, go vet and go test in a
// generated API module and logs the test output.
func runGoAPIModule(t *testing.T, apiDir string) {
	t.Helper()
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"build", "./..."},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" {
			t.Logf("generated API tests:\n%s", out)
		}
	}
}
