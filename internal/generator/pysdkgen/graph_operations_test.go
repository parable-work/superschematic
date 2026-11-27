package pysdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// graphService is the API whose @graphOperations set the loader fills
// with the operations of fixture-version-graph-db's Recipe graph (D138),
// and graphDB its database.
const (
	graphService = "fixture-graph-api"
	graphDB      = "fixture-version-graph-db"
)

// TestGraphOperationsSDKImportsAndRuns writes the Python types packages
// of fixture-graph-api and its database, whose package holds the Recipe
// graph's facade, and the API's Python SDK, byte-compiles them, and runs
// graphOperationsProbe against a local HTTP server: the types the loader
// adds for a @graphOperations set (D138) import and validate, a save sends
// each kind's edits as the route reads them, and a branch, a save, a tree
// and a diff answer their models. The probe needs pydantic and skips
// without it.
func TestGraphOperationsSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, graphDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, graphService), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   graphService,
		Dependencies: map[string]*ir.Schema{graphDB: db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	var apiTypes *pygen.ModuleOutput
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{graphDB, db, nil},
		{graphService, schema, map[string]*ir.Schema{graphDB: db}},
	} {
		output, err := pygen.Generate(tc.schema, pygen.Options{SchemaName: tc.name, Dependencies: tc.deps, Registry: reg, Clock: nestedArraysClock})
		if err != nil {
			t.Fatalf("pygen.Generate %s: %v", tc.name, err)
		}
		if err := pygen.WriteTypes(output, filepath.Join(typesDir, tc.name)); err != nil {
			t.Fatalf("pygen.WriteTypes %s: %v", tc.name, err)
		}
		apiTypes = output
	}
	sdkDir := filepath.Join(root, "sdk")
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	if sdkOutput.TypesPackage != apiTypes.PythonModuleName {
		t.Fatalf("SDK types package %q, types module %q", sdkOutput.TypesPackage, apiTypes.PythonModuleName)
	}

	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	paths := []string{sdkDir, filepath.Join(typesDir, graphService), filepath.Join(typesDir, graphDB)}
	probe := exec.Command(python, "-c", fmt.Sprintf(graphOperationsProbe, sdkOutput.PackageName, sdkOutput.SDKClassName, apiTypes.PythonModuleName))
	probe.Env = append(os.Environ(), "PYTHONPATH="+strings.Join(paths, string(os.PathListSeparator)))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// graphOperationsProbe is formatted with the SDK package and class and
// the API's types package, all on PYTHONPATH.
const graphOperationsProbe = `
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sdk_package = __import__(%[1]q)
types_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[2]q)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError

recipe = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
draft = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
part_key = "2d3e4f5a-6b7c-4d8e-9f0a-1b2c3d4e5f60"
commit = "3e4f5a6b-7c8d-4e9f-8a1b-2c3d4e5f6071"


def ref(version):
    return {"id": draft, "root": recipe, "parent": commit, "name": "draft", "sealed": False, "discarded": False, "version": version}


def rows(parts):
    return {"cover": [], "ingredient": [], "note": [], "part": parts, "step": [], "tasting": [], "utensil": []}


part = {"entityKey": part_key, "rank": "V", "heading": "Intro"}
tree = dict(rows([part]), contentHash="abc", findings=[{"code": "missing_parent", "kind": "part", "entityKey": part_key, "message": "no parent"}])
row = json.dumps({"heading": "Intro"})
answers = {
    ("POST", "/api/recipes/" + recipe + "/refs"): ref(1),
    ("PUT", "/api/recipes/" + recipe + "/refs/" + draft + "/edits"): {"ref": ref(2), "saved": rows([part])},
    ("GET", "/api/recipes/" + recipe + "/refs/" + draft + "/tree"): tree,
    ("GET", "/api/recipes/" + recipe + "/diff"): [{"kind": "part", "entityKey": part_key, "operation": "ADD", "row": row}],
}
calls = []


class Handler(BaseHTTPRequestHandler):
    def answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        calls.append((self.command, self.path, json.loads(raw) if raw else None))
        body = json.dumps({"data": answers[(self.command, self.path.split("?")[0])], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_POST = do_PUT = answer

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
# Every route of the set needs a caller (@requirePermission on
# RecipeOperations), so the client sends the token it is given.
sdk = SDK(ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}", auth_token="ann"))

branched = sdk.recipe.branch(recipe, types_package.RecipeGraphBranchInput(name="draft"))
assert calls[-1] == ("POST", "/api/recipes/" + recipe + "/refs", {"name": "draft"}), calls[-1]
assert type(branched) is types_package.RecipeGraphRef, branched
assert branched.version == 1 and branched.head is None and branched.sealed is False, branched

# A row the edits hold is validated as its kind's input before any request.
try:
    sdk.recipe.save(recipe, draft, {"version": 1, "edits": {"part": {"upsert": [{"rank": "V"}]}}})
except ValidationError as err:
    assert err.errors, err
else:
    raise AssertionError("a part without its heading was sent")
assert len(calls) == 1, calls

saved = sdk.recipe.save(recipe, draft, {"version": 1, "edits": {"part": {"upsert": [{"rank": "V", "heading": "Intro"}]}, "step": {"delete": [part_key]}}})
method, path, body = calls[-1]
assert (method, path) == ("PUT", "/api/recipes/" + recipe + "/refs/" + draft + "/edits"), calls[-1]
assert body["version"] == 1, body
assert body["edits"]["part"]["upsert"] == [{"rank": "V", "heading": "Intro"}], body
assert body["edits"]["step"]["delete"] == [part_key], body
assert "cover" not in body["edits"], body
assert type(saved) is types_package.RecipeGraphSaveResult, saved
assert saved.ref.version == 2 and saved.saved.part[0].heading == "Intro", saved

composed = sdk.recipe.compose(recipe, draft)
assert calls[-1][:2] == ("GET", "/api/recipes/" + recipe + "/refs/" + draft + "/tree"), calls[-1]
assert type(composed) is types_package.RecipeGraphTree, composed
assert composed.content_hash == "abc" and composed.findings[0].code == "missing_parent", composed
assert composed.part[0].entity_key == part_key, composed.part

changes = sdk.recipe.diff(recipe, commit, commit)
assert calls[-1][1] == "/api/recipes/" + recipe + "/diff?from=" + commit + "&to=" + commit, calls[-1]
assert type(changes[0]) is types_package.RecipeGraphChange, changes
assert changes[0].kind == "part" and changes[0].operation == "ADD", changes
assert changes[0].row == row, changes
`
