package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// loadQueryListsAPI loads query-lists-api (sdktest.LoadQueryListsService):
// list query parameters of an enum, a UUID scalar, an integer scalar,
// strings and booleans.
func loadQueryListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := sdktest.LoadQueryListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.QueryListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.QueryListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: sdktest.QueryListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writeQueryListsSDK writes the TypeScript SDK of query-lists-api into dir.
func writeQueryListsSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string) {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
}

// TestListQueryParamItemChecks: a list whose items are text (an enum, a
// UUID, a string) has each item checked before the request, as the
// comma-separated value cannot carry an empty item or one with a comma or
// surrounding space; a list of numbers is checked item by item only for
// its range. listMin bounds only a minimum above one, as an empty list is
// left out, and a list with no check has no validation block.
func TestListQueryParamItemChecks(t *testing.T) {
	_, apiOutput, parseable := loadQueryListsAPI(t)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	params := sdkOutput.Namespaces[0].Endpoints[0].QueryParams
	want := []struct {
		name                                             string
		itemIsText, items, checksMin, checksList, checks bool
	}{
		{"ids", true, true, false, true, true},
		{"shades", true, true, true, true, true},
		{"ranks", false, true, false, false, true},
		{"codes", true, true, false, false, true},
		{"tags", true, true, false, false, true},
		{"flags", false, false, false, false, false},
	}
	if len(params) != len(want)+1 {
		t.Fatalf("query params = %+v", params)
	}
	for i, w := range want {
		got := params[i]
		if got.Name != w.name || !got.IsArray || got.ItemIsText != w.itemIsText || got.ChecksItems() != w.items ||
			got.ChecksListMin() != w.checksMin || got.ChecksList() != w.checksList || got.Validates() != w.checks {
			t.Errorf("parameter %d = %s (text %t, items %t, listMin %t, list %t, validates %t), want %s (text %t, items %t, listMin %t, list %t, validates %t)",
				i, got.Name, got.ItemIsText, got.ChecksItems(), got.ChecksListMin(), got.ChecksList(), got.Validates(),
				w.name, w.itemIsText, w.items, w.checksMin, w.checksList, w.checks)
		}
	}
	if limit := params[len(want)]; limit.Name != "limit" || limit.IsArray || limit.ItemIsText || !limit.Validates() {
		t.Errorf("limit = %+v, want a validated scalar parameter", limit)
	}

	outDir := t.TempDir()
	writeQueryListsSDK(t, apiOutput, parseable, outDir)
	source, err := os.ReadFile(filepath.Join(outDir, "namespaces", "post.ts"))
	if err != nil {
		t.Fatalf("read namespace: %v", err)
	}
	for _, unwanted := range []string{
		"flagsValue",
		"tagsValue.length < 1",
		"ranksErrors",
		"tagsErrors",
	} {
		if strings.Contains(string(source), unwanted) {
			t.Errorf("namespace carries a check that bounds nothing: found %q", unwanted)
		}
	}
}

// TestWriteSDKGoldenQueryLists pins the namespace of the TypeScript SDK for
// query-lists-api. Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenQueryLists -update
func TestWriteSDKGoldenQueryLists(t *testing.T) {
	_, apiOutput, parseable := loadQueryListsAPI(t)
	outDir := t.TempDir()
	writeQueryListsSDK(t, apiOutput, parseable, outDir)
	compareGolden(t, outDir, filepath.Join("testdata", "golden", sdktest.QueryListsService), "namespaces/post.ts")
}

// TestQueryListsSDKCompilesAndRuns type-checks the TypeScript SDK of
// query-lists-api against its generated types package, then runs
// test_query_lists.js: a list is sent as one comma-separated value, an
// empty one is left out, and a required empty list or an item the value
// cannot carry fails validation at its path before any request.
func TestQueryListsSDKCompilesAndRuns(t *testing.T) {
	runInQueryListsSDK(t, "test_query_lists.js")
}

// runInQueryListsSDK writes the TypeScript SDK of query-lists-api with its
// types package, type-checks it, then runs the bun test file testFile, next
// to this file, with SDK_DIR set to the SDK package.
func runInQueryListsSDK(t *testing.T, testFile string) {
	t.Helper()
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

	schema, apiOutput, parseable := loadQueryListsAPI(t)
	typesDir := writeTypesPackage(t, bunPath, schema, sdktest.QueryListsService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", sdktest.QueryListsService)
	writeQueryListsSDK(t, apiOutput, parseable, sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), testFile))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", testFile, err, out)
	}
}

// writeTypesPackage writes the TypeScript types package of schema under
// tempRoot/types/typescript/<name>, below a Bun workspace root as a build
// lays it out, installs it once at that root and type-checks it. It
// returns the package's directory.
func writeTypesPackage(t *testing.T, bunPath string, schema *ir.Schema, name, tempRoot string) string {
	t.Helper()
	paths := testpaths.Local(t)
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: name, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	typesRoot := filepath.Join(tempRoot, "types", "typescript")
	typesDir := filepath.Join(typesRoot, name)
	if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
		t.Fatalf("set superscalar spec: %v", err)
	}
	if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	if err := (tsgen.WorkspaceRoot{OutputRoot: tempRoot, Paths: paths}).Write(); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = tempRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the types package (likely offline): %v\n%s", err, out))
	}
	build := exec.Command(bunPath, "x", "tsc")
	build.Dir = typesDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("types package does not type-check: %v\n%s", err, out)
	}
	return typesDir
}
