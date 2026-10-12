package apigen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// locationArgsAPI is a schema local to apigen's testdata whose operation
// takes Geo.Location body arguments in every shape a route decodes.
const locationArgsAPI = "location-args-api"

func loadLocationArgsAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", locationArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", locationArgsAPI, err)
	}
	return schema
}

// TestWriteAPIGoldenLocationArgs pins routes.go of location-args-api: each
// Geo.Location body argument is built with bodyargs.CheckJSON and
// superscalar's check of the scalar, which routes.go imports from the
// scalar Go module as scalars. Regenerate with
// go test ./internal/generator/apigen -run TestWriteAPIGoldenLocationArgs -update
func TestWriteAPIGoldenLocationArgs(t *testing.T) {
	output, err := apigen.Generate(loadLocationArgsAPI(t), apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  locationArgsAPI,
		ModulePath:  "example.com/schemas/api/" + locationArgsAPI,
		TypesModule: "example.com/schemas/types/go/" + locationArgsAPI,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if !output.RoutesNeedScalars() {
		t.Error("RoutesNeedScalars() = false for Geo.Location body arguments")
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", locationArgsAPI), []string{"routes.go"})
}

// TestRoutesImportTheScalarModuleOnlyForAJSONObjectCheck: routes.go
// imports the scalar Go module only when a body argument is checked on its
// JSON, so raw-body-check-api, whose operations take input types, does
// not, and body-args-api, with Generic.StringMap arguments, does.
func TestRoutesImportTheScalarModuleOnlyForAJSONObjectCheck(t *testing.T) {
	for _, tc := range []struct {
		schema *ir.Schema
		name   string
		want   bool
	}{
		{loadRawBodyCheckAPI(t), rawBodyCheckAPI, false},
		{loadBodyArgsAPI(t), bodyArgsAPI, true},
		{loadLocationArgsAPI(t), locationArgsAPI, true},
	} {
		output, err := apigen.Generate(tc.schema, apigen.Options{
			Provider:   sessionauth.Provider{},
			SchemaName: tc.name,
			Clock:      goModuleClock,
		})
		if err != nil {
			t.Fatalf("%s: apigen.Generate: %v", tc.name, err)
		}
		if got := output.RoutesNeedScalars(); got != tc.want {
			t.Errorf("%s: RoutesNeedScalars() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestLocationArgsRoutesCheckTheirJSON generates the Go types and API
// modules of location-args-api, copies testdata/location_args_routes_test.go
// into the API module and runs it against the superscalar Go binding. A
// Geo.Location body argument, alone, in a list, a list of lists, a map or a
// map of lists, is checked on its own JSON as the generated types check
// one: an unknown, duplicate or missing key and a degree out of range are
// each refused at the value's path under the core's kind, and
// {"lat": 0, "lon": 0} is a location.
func TestLocationArgsRoutesCheckTheirJSON(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	apiDir := writeGoAPIModule(t, loadLocationArgsAPI(t), locationArgsAPI)
	test, err := os.ReadFile(filepath.Join("testdata", "location_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "location_args_routes_test.go"), test, 0o644); err != nil {
		t.Fatal(err)
	}
	runGoAPIModule(t, apiDir)
}
