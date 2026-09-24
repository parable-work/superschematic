package pysdkgen

import (
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite golden files")

const nestedArraysService = "fixture-nested-arrays-api"

var nestedArraysClock = codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

// loadNestedArraysAPI loads fixture-nested-arrays-api: an input type, a
// PUT body argument and a bare response that are arrays of arrays. With
// withPaint it adds grid.paint (sdktest.AddPaintOperation).
func loadNestedArraysAPI(t *testing.T, withPaint bool) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysService, err)
	}
	if withPaint {
		if err := sdktest.AddPaintOperation(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: nestedArraysService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

func writeNestedArraysSDK(t *testing.T, apiOutput *apigen.APIOutput, dir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, dir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	return sdkOutput
}

// TestWriteSDKGoldenNestedArrays pins every file of the Python SDK for
// fixture-nested-arrays-api: list[list[T]] arguments and responses and the
// inner-list and element checks of a list-of-lists argument. Regenerate
// with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenNestedArrays -update
func TestWriteSDKGoldenNestedArrays(t *testing.T) {
	_, apiOutput := loadNestedArraysAPI(t, false)
	outDir := t.TempDir()
	writeNestedArraysSDK(t, apiOutput, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", nestedArraysService))
}

// TestNestedArraysSDKImportsAndRuns writes the Python types package and the
// Python SDK of fixture-nested-arrays-api, with grid.paint added,
// byte-compiles both, and runs nestedArraysProbe against a local HTTP
// server: list-of-lists arguments and responses cross the wire as nested
// JSON arrays, list-of-lists models come back as models, and a null inner
// list or a bad element fails validation at its index path before any
// request. The probe needs pydantic and skips without it.
func TestNestedArraysSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadNestedArraysAPI(t, true)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: nestedArraysService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir)
	if sdkOutput.TypesPackage != typesOutput.PythonModuleName {
		t.Fatalf("SDK types package %q, types module %q", sdkOutput.TypesPackage, typesOutput.PythonModuleName)
	}

	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(nestedArraysProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// nestedArraysProbe is formatted with the types and SDK directories, the
// SDK package and class, and the types package.
const nestedArraysProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
types_package = __import__(%[5]q)
SDK = getattr(sdk_package, %[4]q)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError
Point = types_package.Point
Shade = types_package.Shade

grid_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
calls = []
responses = []


class Handler(BaseHTTPRequestHandler):
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        calls.append((self.command, self.path, json.loads(raw) if raw else None))
        body = json.dumps({"data": responses.pop(0), "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_PUT = do_POST = _answer

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))
view = {"id": grid_id, "labels": [["a", "b"], []], "shades": [["light"]], "polygons": [[{"x": 1, "y": 2}], []]}


def refused(call, fields):
    try:
        call()
    except ValidationError as err:
        assert sorted(err.errors) == fields, err.errors
    else:
        raise AssertionError("expected a ValidationError for %%s" %% fields)


# A list-of-lists body argument travels as nested JSON arrays.
responses.append(view)
result = sdk.grid.replace_labels(grid_id, [["a", "b"], []])
assert calls[-1] == ("PUT", "/api/grids/%%s/labels" %% grid_id, {"labels": [["a", "b"], []]}), calls[-1]
assert result.labels == [["a", "b"], []], result
assert [[(p.x, p.y) for p in row] for row in result.polygons] == [[(1, 2)], []], result

# A null inner list, a missing list and a bad element fail at their index path.
before = len(calls)
refused(lambda: sdk.grid.replace_labels(grid_id, [["a"], None]), ["labels[1]"])
refused(lambda: sdk.grid.replace_labels(grid_id, None), ["labels"])
refused(lambda: sdk.grid.replace_labels(grid_id, [["a", 7]]), ["labels[0][1]"])
refused(lambda: sdk.grid.paint(grid_id, [["light", "dim"]]), ["shades[0][1]"])
refused(lambda: sdk.grid.paint(grid_id, [["light"]], polygons=[[{"x": "far"}]]), ["polygons[0][0].x", "polygons[0][0].y"])
refused(lambda: sdk.grid.save_grid({"labels": [["a"], None], "shades": [], "polygons": []}), ["labels[1]"])
assert len(calls) == before, calls[before:]

# An input type with lists of lists goes through the types package.
responses.append(view)
sdk.grid.save_grid({"labels": [["a"], []], "shades": [["dark"]], "polygons": [[{"x": 3, "y": 4}]], "weights": [[0.5], []]})
assert calls[-1][2] == {"labels": [["a"], []], "shades": [["dark"]], "polygons": [[{"x": 3.0, "y": 4.0}]], "weights": [[0.5], []]}, calls[-1]

# A bare list[list[str]] response comes back as nested lists.
responses.append([["a", "b"], [], ["c"]])
labels = sdk.grid.grid_labels(grid_id, limit=2)
assert labels == [["a", "b"], [], ["c"]], labels
assert calls[-1][:2] == ("GET", "/api/grids/%%s/labels?limit=2" %% grid_id), calls[-1]

# A list-of-lists model response is coerced row by row.
responses.append([[{"x": 1, "y": 2}], []])
polygons = sdk.grid.paint(grid_id, [[Shade.Dark], []], polygons=[[{"x": 3, "y": 4}]])
assert calls[-1][2] == {"shades": [["dark"], []], "polygons": [[{"x": 3, "y": 4}]]}, calls[-1]
assert [[type(p) is Point for p in row] for row in polygons] == [[True], []], polygons
assert polygons[0][0].x == 1 and polygons[0][0].y == 2, polygons
server.shutdown()
`

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
