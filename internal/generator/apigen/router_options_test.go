package apigen_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
// from its default but app_error_context, which needs a runtime whose
// Respond takes a context (TestAppErrorContext).
var routerOptions = naming.GoAPIConfig{
	ValidationErrorCode:     "VALIDATION-1",
	PathParamError:          "{name} is not a path segment",
	ValidatorNames:          map[string]string{"minLength": "min_length", "maxLength": "max_length", "listMax": "list_max"},
	UndeclaredInputKeys:     naming.GoAPIKeep,
	InputTrailingData:       naming.GoAPIIgnore,
	OptionalInputEmptyBody:  naming.GoAPIObject,
	StrictInputRefusals:     naming.GoAPIDecoder,
	ManualRouteConstants:    true,
	BodyArguments:           naming.GoAPIStruct,
	QueryParamListEmptyItem: "{name} cannot contain empty values",
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
			if tc.options.ManualRouteConstants {
				manual, err := os.ReadFile(filepath.Join("testdata", "router_options_manual_test.go"))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(apiDir, "router_options_manual_test.go"), manual, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			runGoAPIModule(t, apiDir)
		})
	}
}

// TestAppErrorContext: under go_api.app_error_context RespondAppError takes
// the request's context and passes it to the runtime's Respond, and every
// call routes.go writes passes r.Context(). superschematic's runtime takes
// no context, so the module is not built here; without the key the module
// TestRouterOptionsAnswerTheirRequests builds calls the runtime without
// one.
func TestAppErrorContext(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join("testdata", "services", routerOptionsAPI))
	if err != nil {
		t.Fatal(err)
	}
	for _, on := range []bool{false, true} {
		names := naming.Default()
		names.GoAPI.AppErrorContext = on
		output, err := apigen.Generate(schema, apigen.Options{
			Provider:    sessionauth.Provider{},
			SchemaName:  routerOptionsAPI,
			ModulePath:  "example.com/schemas/api/" + routerOptionsAPI,
			TypesModule: "example.com/schemas/types/go/" + routerOptionsAPI,
			Naming:      names,
			Clock:       goModuleClock,
		})
		if err != nil {
			t.Fatal(err)
		}
		outDir := t.TempDir()
		if err := apigen.WriteAPI(output, outDir); err != nil {
			t.Fatal(err)
		}
		read := func(name string) string {
			data, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				t.Fatal(err)
			}
			return string(data)
		}
		errorsGo, routesGo := read("errors.go"), read("routes.go")
		signature, call, passing := "func RespondAppError(w http.ResponseWriter, logger *zap.Logger, err error) {", "runtimeapperror.Respond(w, logger, err)", 0
		calls := strings.Count(routesGo, "RespondAppError(w, ")
		if on {
			signature, call, passing = "func RespondAppError(w http.ResponseWriter, ctx context.Context, logger *zap.Logger, err error) {", "runtimeapperror.Respond(w, ctx, logger, err)", calls
		}
		if !strings.Contains(errorsGo, signature) || !strings.Contains(errorsGo, call) {
			t.Errorf("app_error_context %t: errors.go lacks %q and %q", on, signature, call)
		}
		if got := strings.Count(routesGo, "RespondAppError(w, r.Context(), "); calls == 0 || got != passing {
			t.Errorf("app_error_context %t: %d of the %d RespondAppError calls in routes.go pass r.Context(), want %d", on, got, calls, passing)
		}
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
