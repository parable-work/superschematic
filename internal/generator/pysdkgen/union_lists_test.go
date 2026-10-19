package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	ir "github.com/parable-work/superschematic/ir"
)

// loadUnionListsAPI loads union-lists-api (sdktest.LoadUnionListsService)
// with sdktest.AddCircleOperations: the union Shape and the object Circle
// as maps, maps of lists and responses that may be null, among others.
func loadUnionListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	if err := sdktest.AddCircleOperations(schema); err != nil {
		t.Fatal(err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestUnionListsSDKOutputTypeHints: a map response is a dict of str keys
// to the output at its list levels, and one that may be null adds | None.
func TestUnionListsSDKOutputTypeHints(t *testing.T) {
	_, apiOutput := loadUnionListsAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := map[string]string{
		"list_shapes":     "list[Shape]",
		"shape_rows":      "list[list[Shape]]",
		"get_shape":       "Shape",
		"shapes_by_name":  "dict[str, Shape]",
		"shape_groups":    "dict[str, list[Shape]]",
		"find_shape":      "Shape | None",
		"find_shapes":     "list[Shape] | None",
		"circles_by_name": "dict[str, Circle]",
		"circle_groups":   "dict[str, list[Circle]]",
		"find_circle":     "Circle | None",
	}
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			w, ok := want[ep.MethodName]
			if !ok {
				t.Errorf("unexpected endpoint %s", ep.MethodName)
				continue
			}
			delete(want, ep.MethodName)
			if ep.OutputTypeHint != w {
				t.Errorf("%s returns %s, want %s", ep.MethodName, ep.OutputTypeHint, w)
			}
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestUnionListsSDKReadsMapsAndNulls writes the Python types package and
// the Python SDK of union-lists-api with sdktest.AddCircleOperations,
// byte-compiles both, and runs unionListsProbe against a local HTTP
// server: a map and a map of lists keep their keys, a Circle in one is
// coerced to the model value by value, and a response that may be null is
// None for null. The probe needs pydantic and skips without it.
func TestUnionListsSDKReadsMapsAndNulls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadUnionListsAPI(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(unionListsProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, sdkOutput.TypesPackage))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// unionListsProbe is formatted with the types and SDK directories, the SDK
// package, the SDK class and the types package. A value of the union is
// compared as plain data, the SDK not reading it into a member.
const unionListsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[4]q)
types = __import__(%[5]q)

circle = {"radius": 1}
square = {"side": 2}
answers = {
    "/api/shape-index": {"a": circle, "b": square},
    "/api/shape-groups": {"round": [circle], "mixed": [square, circle], "none": []},
    "/api/shape-search/c-1": circle,
    "/api/shape-search/none": None,
    "/api/shape-search": None,
    "/api/circle-index": {"a": circle},
    "/api/circle-groups": {"round": [circle, circle], "none": []},
    "/api/circle-search/c-1": circle,
    "/api/circle-search/none": None,
}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"data": answers[self.path], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def plain(value):
    if isinstance(value, dict):
        return {key: plain(item) for key, item in value.items()}
    if isinstance(value, list):
        return [plain(item) for item in value]
    if hasattr(value, "model_dump"):
        return value.model_dump()
    return value


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))

# A map and a map of lists of the union keep their keys.
shapes = sdk.shape.shapes_by_name()
assert plain(shapes) == {"a": circle, "b": square}, shapes
groups = sdk.shape.shape_groups()
assert plain(groups) == {"round": [circle], "mixed": [square, circle], "none": []}, groups

# A response that may be null is None for null.
assert plain(sdk.shape.find_shape("c-1")) == circle
assert sdk.shape.find_shape("none") is None
assert sdk.shape.find_shapes() is None

# A Circle in a map or a map of lists is coerced to the model value by value.
circles = sdk.shape.circles_by_name()
assert isinstance(circles, dict) and isinstance(circles["a"], types.Circle), circles
assert circles["a"].radius == 1, circles
circle_groups = sdk.shape.circle_groups()
assert isinstance(circle_groups, dict) and sorted(circle_groups) == ["none", "round"], circle_groups
assert all(isinstance(item, types.Circle) for item in circle_groups["round"]), circle_groups
assert len(circle_groups["round"]) == 2 and circle_groups["none"] == [], circle_groups
found = sdk.shape.find_circle("c-1")
assert isinstance(found, types.Circle) and found.radius == 1, found
assert sdk.shape.find_circle("none") is None
server.shutdown()
`
