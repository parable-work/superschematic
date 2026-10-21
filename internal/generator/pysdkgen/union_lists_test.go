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
// union_models_test.go's loadUnionModelsAPI adds a page, a model with union
// fields and a discriminated union instead.
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
		"draw_shape":      "Shape",
		"pay":             "str",
		"checkout":        "str",
		"draw_shapes":     "str",
		"shape_grid":      "str",
		"shapes_by_key":   "str",
		"probe":           "str",
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
// coerced to the model value by value, and a Shape to its member model, as
// a list's elements are, and a response that may be null is its model or
// member, or None for null. A value that neither reads, or data that is not
// the map it should be, raises SDKError at its key (data[b],
// data[mixed][1]). The probe needs pydantic and skips without it.
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
// checked to be its member model, then compared as plain data.
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
SDKError = sdk_package.SDKError

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


def refused(call, message):
    try:
        call()
    except SDKError as err:
        assert message in str(err), f"{message!r} not in {str(err)!r}"
    else:
        raise AssertionError(f"expected an SDKError with {message!r}")


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))

# A map and a map of lists of the union keep their keys, and each value or
# element is the member model the union picks.
shapes = sdk.shape.shapes_by_name()
assert isinstance(shapes, dict) and sorted(shapes) == ["a", "b"], shapes
assert type(shapes["a"]) is types.Circle and type(shapes["b"]) is types.Square, shapes
assert plain(shapes) == {"a": circle, "b": square}, shapes
groups = sdk.shape.shape_groups()
assert isinstance(groups, dict) and sorted(groups) == ["mixed", "none", "round"], groups
assert [type(item) for item in groups["round"]] == [types.Circle], groups
assert [type(item) for item in groups["mixed"]] == [types.Square, types.Circle], groups
assert plain(groups) == {"round": [circle], "mixed": [square, circle], "none": []}, groups

# A response of the union that may be null is its member, or None for null.
found_shape = sdk.shape.find_shape("c-1")
assert type(found_shape) is types.Circle and plain(found_shape) == circle, found_shape
assert sdk.shape.find_shape("none") is None
assert sdk.shape.find_shapes() is None
answers["/api/shape-search"] = [square, circle]
found_shapes = sdk.shape.find_shapes()
assert [type(item) for item in found_shapes] == [types.Square, types.Circle], found_shapes

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

# A map value that no member or model reads raises SDKError at its key, as
# a list's element does at its index, and so does one that may be null.
answers["/api/shape-index"] = {"a": circle, "b": "triangle"}
refused(sdk.shape.shapes_by_name, "response data[b] is not a valid Shape")
answers["/api/shape-groups"] = {"round": [circle], "mixed": [square, {"sides": 3}]}
refused(sdk.shape.shape_groups, "response data[mixed][1] is not a valid Shape")
answers["/api/circle-index"] = {"a": {"radius": "wide"}}
refused(sdk.shape.circles_by_name, "response data[a] is not a valid Circle")
answers["/api/shape-search/c-1"] = {"sides": 3}
refused(lambda: sdk.shape.find_shape("c-1"), "response data is not a valid Shape")
answers["/api/circle-search/c-1"] = {"radius": "wide"}
refused(lambda: sdk.shape.find_circle("c-1"), "response data is not a valid Circle")

# Data that is not the map, or a map value that is not the list, it should
# be raises SDKError too.
answers["/api/shape-index"] = [circle, square]
refused(sdk.shape.shapes_by_name, "expected response data to be a map of Shape")
answers["/api/shape-groups"] = [[circle]]
refused(sdk.shape.shape_groups, "expected response data to be a map of lists of Shape")
answers["/api/shape-groups"] = {"round": circle}
refused(sdk.shape.shape_groups, "expected response data[round] to be a list of Shape")
answers["/api/circle-groups"] = {"round": [circle, "wide"]}
refused(sdk.shape.circle_groups, "response data[round][1] is not a valid Circle")
server.shutdown()
`
