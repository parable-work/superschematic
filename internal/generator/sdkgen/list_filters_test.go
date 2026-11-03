package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	ir "github.com/parable-work/superschematic/ir"
)

// loadListFiltersAPI loads list-filters-api (sdktest.LoadListFiltersService):
// operations whose only query parameters with a check are lists, with no
// body argument and no input type.
func loadListFiltersAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := sdktest.LoadListFiltersService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.ListFiltersService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.ListFiltersService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: sdktest.ListFiltersService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writeListFiltersSDK writes the TypeScript SDK of list-filters-api into
// dir, with lazyValidation (D82) when lazy is set.
func writeListFiltersSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string, lazy bool) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkOutput.LazyValidation = lazy
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// listFiltersNamespaces are the namespaces of list-filters-api's SDK.
var listFiltersNamespaces = []string{"namespaces/entry.ts", "namespaces/tally.ts"}

// TestListFiltersDeclareTheHelpersTheyCall: a namespace whose only query
// parameters with a check are lists, of a scalar whose items are checked
// or a required one, with no body argument and no input type, declares
// newValidationErrors and setFieldErrors, which its method calls (D101):
// what the namespace declares and what its template calls are decided by
// one predicate, EndpointInfo.ValidatesQuery.
func TestListFiltersDeclareTheHelpersTheyCall(t *testing.T) {
	_, apiOutput, parseable := loadListFiltersAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sdkOutput.Namespaces) != 2 {
		t.Fatalf("namespaces = %d, want entry and tally", len(sdkOutput.Namespaces))
	}
	for _, ns := range sdkOutput.Namespaces {
		endpoint := ns.Endpoints[0]
		if endpoint.HasInput || len(endpoint.ScalarArgs) > 0 || endpoint.ValidatesArgs() || !endpoint.ValidatesQuery() {
			t.Errorf("%s.%s: input %t, body arguments %d, validates its arguments %t, its query %t; want only its query",
				ns.Name, endpoint.Name, endpoint.HasInput, len(endpoint.ScalarArgs), endpoint.ValidatesArgs(), endpoint.ValidatesQuery())
		}
		for _, param := range endpoint.QueryParams {
			if param.Validates() && !param.IsArray {
				t.Errorf("%s.%s: %s validates, but only the lists should", ns.Name, endpoint.Name, param.Name)
			}
		}
		for _, helper := range []string{"newValidationErrors", "setFieldErrors"} {
			if !slices.Contains(ns.Imports, helper) {
				t.Errorf("namespace %s does not declare %s, which its method calls: %v", ns.Name, helper, ns.Imports)
			}
		}
	}
}

// TestWriteSDKGoldenListFilters pins the namespaces of the TypeScript SDK
// for list-filters-api, without and with lazyValidation: each declares the
// helpers its query checks call (D101). Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenListFilters -update
func TestWriteSDKGoldenListFilters(t *testing.T) {
	_, apiOutput, parseable := loadListFiltersAPI(t)
	for _, lazy := range []bool{false, true} {
		outDir := t.TempDir()
		writeListFiltersSDK(t, apiOutput, parseable, outDir, lazy)
		for _, name := range listFiltersNamespaces {
			compareGolden(t, outDir, filepath.Join("testdata", "golden", listFiltersGolden(lazy)), name)
		}
	}
}

// listFiltersGolden is the directory under testdata/golden of
// list-filters-api's SDK, with lazyValidation when lazy is set.
func listFiltersGolden(lazy bool) string {
	if lazy {
		return sdktest.ListFiltersService + "-lazy"
	}
	return sdktest.ListFiltersService
}

// TestListFiltersSDKCompilesAndRuns type-checks the TypeScript SDK of
// list-filters-api, without and with lazyValidation, against its generated
// types package, then runs test_list_filters.js on each: a call without
// the lists reaches fetch, and a blank item in a list is refused at its
// path, categoryIds[0], before any request (D101).
func TestListFiltersSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	schema, apiOutput, parseable := loadListFiltersAPI(t)
	typesDir := writeTypesPackage(t, bunPath, schema, sdktest.ListFiltersService, tempRoot)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	for _, lazy := range []bool{false, true} {
		sdkDir := filepath.Join(tempRoot, "sdk", "typescript", sdktest.ListFiltersService)
		if lazy {
			// Outside the workspace's members, which take one package of
			// a name.
			sdkDir = filepath.Join(tempRoot, "sdk", "typescript-lazy", sdktest.ListFiltersService)
		}
		writeListFiltersSDK(t, apiOutput, parseable, sdkDir, lazy)
		if err := CompileSDK(sdkDir, typesDir); err != nil {
			t.Fatalf("the SDK (lazyValidation %t) does not type-check: %v", lazy, err)
		}
		run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_list_filters.js"))
		run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("test_list_filters.js (lazyValidation %t) failed: %v\n%s", lazy, err, out)
		}
	}
}
