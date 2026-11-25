package apigen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// routerOptionsAPI is a schema local to apigen's testdata: a route of each
// kind of request the [go_api] table of the naming file changes.
const routerOptionsAPI = "router-options-api"

// routerOptions is the [go_api] table the option run sets: every key away
// from its default.
var routerOptions = naming.GoAPIConfig{
	ValidationErrorCode: "VALIDATION-1",
	PathParamError:      "{name} is not a path segment",
}

// TestRouterOptionsAnswerTheirRequests generates router-options-api twice,
// with the [go_api] table at its defaults and with every key set, and runs
// testdata/router_options_routes_test.go in each module. The file sends
// the same requests to both and checks each answer against the one its
// table gives.
func TestRouterOptionsAnswerTheirRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	test, err := os.ReadFile(filepath.Join("testdata", "router_options_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		options naming.GoAPIConfig
	}{
		{"defaults", naming.GoAPIConfig{}},
		{"options", routerOptions},
	} {
		t.Run(tc.name, func(t *testing.T) {
			apiDir := writeRouterOptionsModule(t, tc.options)
			if err := os.WriteFile(filepath.Join(apiDir, "router_options_routes_test.go"), test, 0o644); err != nil {
				t.Fatal(err)
			}
			mode := fmt.Sprintf("package routeroptionsapi_test\n\nconst optionsSet = %t\n", tc.name == "options")
			if err := os.WriteFile(filepath.Join(apiDir, "router_options_mode_test.go"), []byte(mode), 0o644); err != nil {
				t.Fatal(err)
			}
			runGoAPIModule(t, apiDir)
		})
	}
}

// writeRouterOptionsModule generates the Go types module and the Go API
// module of router-options-api with the [go_api] table options into a temp
// tree laid out as a build writes it and returns the API module's
// directory.
func writeRouterOptionsModule(t *testing.T, options naming.GoAPIConfig) string {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", routerOptionsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", routerOptionsAPI, err)
	}
	typesModule := "example.com/schemas/types/go/" + routerOptionsAPI
	paths := testpaths.Local(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", routerOptionsAPI)
	apiDir := filepath.Join(root, "api", routerOptionsAPI)

	typesOutput, err := typegen.Generate(schema, typegen.Options{
		SchemaName: routerOptionsAPI,
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

	names := naming.Default()
	names.GoAPI = options
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  routerOptionsAPI,
		ModulePath:  "example.com/schemas/api/" + routerOptionsAPI,
		TypesModule: typesModule,
		Naming:      names,
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
