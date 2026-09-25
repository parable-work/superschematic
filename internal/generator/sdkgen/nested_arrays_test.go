package sdkgen

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

var nestedArraysClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// loadNestedArraysAPI loads fixture-nested-arrays-api: an input type, a
// PUT body argument and a bare response that are arrays of arrays. With
// withPaint it adds grid.paint (sdktest.AddPaintOperation).
func loadNestedArraysAPI(t *testing.T, withPaint bool) (*ir.Schema, *apigen.APIOutput, map[string]bool) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatalf("load fixture-nested-arrays-api: %v", err)
	}
	if withPaint {
		if err := sdktest.AddPaintOperation(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: "fixture-nested-arrays-api",
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-nested-arrays-api", Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	return schema, apiOutput, tsgen.ParseableTypeNames(tsOutput)
}

// writeNestedArraysSDK writes the TypeScript SDK of
// fixture-nested-arrays-api into dir.
func writeNestedArraysSDK(t *testing.T, dir string) {
	t.Helper()
	_, apiOutput, parseable := loadNestedArraysAPI(t, false)
	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, dir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
}

// TestWriteSDKGoldenNestedArrays pins every file of the TypeScript SDK for
// fixture-nested-arrays-api: string[][] responses and body arguments, the
// inner-list and element checks of a list-of-lists argument, and tool
// schemas whose items nest. Regenerate with:
// go test ./internal/generator/sdkgen -run TestWriteSDKGoldenNestedArrays -update
func TestWriteSDKGoldenNestedArrays(t *testing.T) {
	outDir := t.TempDir()
	writeNestedArraysSDK(t, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", "fixture-nested-arrays-api"))
}

// TestNestedArraysToolSchemasMatchToolsutil: the tool documents carry the
// argument schemas, digests and return schemas the shared tool-schema
// builder pins for fixture-nested-arrays-api.
func TestNestedArraysToolSchemasMatchToolsutil(t *testing.T) {
	outDir := t.TempDir()
	writeNestedArraysSDK(t, outDir)

	type toolSchema struct {
		Name              string          `json:"name"`
		InputSchemaDigest string          `json:"inputSchemaDigest"`
		Parameters        json.RawMessage `json:"parameters"`
		Returns           json.RawMessage `json:"returns"`
	}
	var want []toolSchema
	readJSON(t, filepath.Join("..", "toolsutil", "testdata", "golden", "fixture-nested-arrays-api", "tools.json"), &want)
	var got struct {
		Tools []toolSchema `json:"tools"`
	}
	readJSON(t, filepath.Join(outDir, "tools", "schema.json"), &got)

	byName := map[string]toolSchema{}
	for _, tool := range got.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != len(want) {
		t.Fatalf("tools/schema.json has %d tools, toolsutil pins %d", len(byName), len(want))
	}
	for _, expected := range want {
		tool, ok := byName[expected.Name]
		if !ok {
			t.Errorf("tools/schema.json misses %s", expected.Name)
			continue
		}
		if tool.InputSchemaDigest != expected.InputSchemaDigest {
			t.Errorf("%s: inputSchemaDigest %s, toolsutil pins %s", expected.Name, tool.InputSchemaDigest, expected.InputSchemaDigest)
		}
		if !jsonEqual(t, tool.Returns, expected.Returns) {
			t.Errorf("%s: returns %s, toolsutil pins %s", expected.Name, tool.Returns, expected.Returns)
		}
	}
}

// TestNestedArraysSDKCompilesAndRuns type-checks the TypeScript SDK of
// fixture-nested-arrays-api, with grid.paint added, against its generated
// types package, then runs test_nested_arrays.js: list-of-lists arguments
// and responses cross an injected fetch as nested arrays, and a null inner
// list or a bad element fails validation at its index path before any
// request.
func TestNestedArraysSDKCompilesAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}

	schema, apiOutput, parseable := loadNestedArraysAPI(t, true)
	tsOutput, err := tsgen.Generate(schema, tsgen.Options{SchemaName: "fixture-nested-arrays-api", Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	// The types package sits under a Bun workspace root, as a build lays
	// it out, and installs once at that root.
	typesRoot := filepath.Join(tempRoot, "types", "typescript")
	typesDir := filepath.Join(typesRoot, "fixture-nested-arrays-api")
	if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
		t.Fatalf("set superscalar spec: %v", err)
	}
	if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}
	if err := tsgen.WriteWorkspaceRoot(typesRoot, naming.Naming{}); err != nil {
		t.Fatalf("write workspace root: %v", err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = typesRoot
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for the types package (likely offline): %v\n%s", err, out))
	}
	build := exec.Command(bunPath, "x", "tsc")
	build.Dir = typesDir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("types package does not type-check: %v\n%s", err, out)
	}

	sdkOutput, err := Generate(apiOutput, parseable, nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", "fixture-nested-arrays-api")
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if err := CompileSDK(sdkDir, typesDir); err != nil {
		t.Fatalf("generated SDK does not type-check: %v", err)
	}
	typecheckTools(t, bunPath, sdkDir)

	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve current test file path")
	}
	run := exec.Command(bunPath, "test", filepath.Join(filepath.Dir(currentFile), "test_nested_arrays.js"))
	run.Env = append(os.Environ(), "SDK_DIR="+sdkDir)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("nested arrays SDK runtime test failed: %v\n%s", err, out)
	}
}

func readJSON(t *testing.T, path string, target any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

// jsonEqual compares two JSON documents by value.
func jsonEqual(t *testing.T, a, b json.RawMessage) bool {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(a, &left); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &right); err != nil {
		t.Fatal(err)
	}
	encodedLeft, _ := json.Marshal(left)
	encodedRight, _ := json.Marshal(right)
	return string(encodedLeft) == string(encodedRight)
}

// compareGoldenTree compares every file under outDir with the file at the
// same path under goldenDir, and fails on a golden file the output no
// longer writes. With -update it rewrites goldenDir from outDir.
func compareGoldenTree(t *testing.T, outDir, goldenDir string) {
	t.Helper()
	got := listFiles(t, outDir)
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
		for _, name := range got {
			data, err := os.ReadFile(filepath.Join(outDir, name))
			if err != nil {
				t.Fatal(err)
			}
			goldenPath := filepath.Join(goldenDir, name)
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	want := listFiles(t, goldenDir)
	if len(want) == 0 {
		t.Fatalf("no golden files under %s (run with -update)", goldenDir)
	}
	wantSet := map[string]bool{}
	for _, name := range want {
		wantSet[name] = true
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("%s has no golden file (run with -update)", name)
			continue
		}
		delete(wantSet, name)
		gotData, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatal(err)
		}
		wantData, err := os.ReadFile(filepath.Join(goldenDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(gotData) != string(wantData) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
	for name := range wantSet {
		t.Errorf("golden %s is no longer written", name)
	}
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var names []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && path == root {
				return filepath.SkipDir
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	return names
}
