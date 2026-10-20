package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	ir "github.com/parable-work/superschematic/ir"
)

// loadUnionListsAPI loads union-lists-api (sdktest.LoadUnionListsService):
// the union Shape as a list, a list of lists, one value, a map, a map of
// lists, and one value or a list that may be null, none paged. Each mutate
// changes the schema before the API is generated.
func loadUnionListsAPI(t *testing.T, mutate ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	for _, m := range mutate {
		if err := m(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writeUnionListsSDK writes the TypeScript SDK of union-lists-api into dir.
func writeUnionListsSDK(t *testing.T, apiOutput *apigen.APIOutput, parseable map[string]bool, dir string) {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
}

// TestWriteSDKGoldenUnionLists pins the namespace of the TypeScript SDK for
// union-lists-api, whose listShapes has a description of two lines.
// Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenUnionLists -update
func TestWriteSDKGoldenUnionLists(t *testing.T) {
	_, apiOutput, parseable := loadUnionListsAPI(t)
	outDir := t.TempDir()
	writeUnionListsSDK(t, apiOutput, parseable, outDir)
	compareGolden(t, outDir, filepath.Join("testdata", "golden", sdktest.UnionListsService), "namespaces/shape.ts")
}

// TestUnionListsSDKOutputTypes: a map response is a Record of the output
// at its list levels, and one that may be null adds | null, for the union
// and for Circle (sdktest.AddCircleOperations) alike.
func TestUnionListsSDKOutputTypes(t *testing.T) {
	_, apiOutput, parseable := loadUnionListsAPI(t, sdktest.AddCircleOperations)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := map[string]string{
		"listShapes":    "Shape[]",
		"shapeRows":     "Shape[][]",
		"getShape":      "Shape",
		"shapesByName":  "Record<string, Shape>",
		"shapeGroups":   "Record<string, Shape[]>",
		"findShape":     "Shape | null",
		"findShapes":    "Shape[] | null",
		"circlesByName": "Record<string, Circle>",
		"circleGroups":  "Record<string, Circle[]>",
		"findCircle":    "Circle | null",
	}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			w, ok := want[ep.Name]
			if !ok {
				t.Errorf("unexpected endpoint %s", ep.Name)
				continue
			}
			delete(want, ep.Name)
			if got := ep.TSOutputType(); got != w {
				t.Errorf("%s returns %s, want %s", ep.Name, got, w)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestUnionListsSDKCompilesAndRuns type-checks the TypeScript SDK of
// union-lists-api, with sdktest.AddCircleOperations, against its generated
// types package, then runs test_union_lists.js: a list, a list of lists,
// one value, a map and a map of lists of the union reach the caller as the
// server sent them, as do a map and a map of lists of Circle, parsed value
// by value, and a response that may be null is null when the server
// answers null.
func TestUnionListsSDKCompilesAndRuns(t *testing.T) {
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

	schema, apiOutput, parseable := loadUnionListsAPI(t, sdktest.AddCircleOperations)
	typesDir := writeTypesPackage(t, bunPath, schema, sdktest.UnionListsService, tempRoot)
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", sdktest.UnionListsService)
	writeUnionListsSDK(t, apiOutput, parseable, sdkDir)
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_union_lists.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("test_union_lists.js failed: %v\n%s", err, out)
	}
}
