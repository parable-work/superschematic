package pysdkgen

import (
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	ir "github.com/parable-work/superschematic/ir"
)

// loadUnionModelsAPI loads union-lists-api (sdktest.LoadUnionListsService),
// the union Shape as a list, a list of lists, one value, a map, a map of
// lists, and one value or a list that may be null, with
// addUnionReadOperations' operations beside them. union_lists_test.go's
// loadUnionListsAPI adds sdktest.AddCircleOperations instead.
func loadUnionModelsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.UnionListsService, err)
	}
	addUnionReadOperations(schema)
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// addUnionReadOperations adds the other places a union reaches a Python
// caller to union-lists-api's ShapeQueries: pageShapes, a page of Shape
// (two to a page, at most five); getDrawing, a Drawing whose fields hold a
// Shape and a list of them; and getMarker, one Marker, a union of Pin and
// Flag, which every member's kind discriminates. The edit stays here, so
// the Go, TypeScript and Rust goldens of the fixture do not change.
func addUnionReadOperations(schema *ir.Schema) {
	pin, flag := "pin", "flag"
	marker := func(name string, kind *string) *ir.TypeDef {
		return &ir.TypeDef{
			Name: name,
			Role: ir.RoleAPIView,
			Fields: []*ir.FieldDef{
				{Name: "kind", TypeRef: ir.TypeRef{Name: "string"}, Required: true, InternalMetadata: true, Default: kind},
				{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			},
		}
	}
	schema.Types["Pin"] = marker("Pin", &pin)
	schema.Types["Flag"] = marker("Flag", &flag)
	schema.Unions["Marker"] = &ir.UnionDef{Name: "Marker", Types: []string{"Pin", "Flag"}}
	schema.Types["Drawing"] = &ir.TypeDef{
		Name: "Drawing",
		Role: ir.RoleAPIView,
		Fields: []*ir.FieldDef{
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			{Name: "shape", TypeRef: ir.TypeRef{Name: "Shape"}, Required: true},
			{Name: "shapes", TypeRef: ir.TypeRef{Name: "Shape", IsArray: true}, Required: true},
		},
	}
	byID := func(name, comment string, output ir.TypeRef, path string) *ir.FieldDef {
		return &ir.FieldDef{
			Name:       name,
			Comment:    comment,
			TypeRef:    output,
			Required:   true,
			Arguments:  []*ir.ArgumentDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
			HTTPMethod: "GET",
			RestPath:   path,
		}
	}
	for _, set := range schema.OperationSets {
		if set.Name != "ShapeQueries" {
			continue
		}
		set.Operations = append(set.Operations,
			&ir.FieldDef{
				Name:       "pageShapes",
				Comment:    "Every shape, a page at a time.",
				TypeRef:    ir.TypeRef{Name: "Shape", IsArray: true},
				Required:   true,
				HTTPMethod: "GET",
				RestPath:   "shape-pages",
				Page:       &ir.OperationPage{DefaultLimit: 2, MaxLimit: 5},
			},
			byID("getDrawing", "One drawing, by its id.", ir.TypeRef{Name: "Drawing"}, "drawings/{id}"),
			byID("getMarker", "One marker, by its id.", ir.TypeRef{Name: "Marker"}, "markers/{id}"),
		)
	}
}

func generateUnionModelsSDK(t *testing.T, apiOutput *apigen.APIOutput) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return sdkOutput
}

// TestUnionOutputsAreMarked: every operation that answers with a union, in
// a list, a list of lists, a page, a map, a map of lists or alone, and one
// that may be null, is OutputIsUnion, and the namespace lists those unions;
// a model whose fields hold the union is not one, since its
// from_dict_non_strict reads them.
func TestUnionOutputsAreMarked(t *testing.T) {
	_, apiOutput := loadUnionModelsAPI(t)
	sdkOutput := generateUnionModelsSDK(t, apiOutput)
	if len(sdkOutput.Namespaces) != 1 {
		t.Fatalf("namespaces = %+v", sdkOutput.Namespaces)
	}
	namespace := sdkOutput.Namespaces[0]
	if want := []string{"Marker", "Shape"}; !slices.Equal(namespace.UnionOutputTypes, want) {
		t.Errorf("UnionOutputTypes = %v, want %v", namespace.UnionOutputTypes, want)
	}
	if want := []string{"Payment", "Shape"}; !slices.Equal(namespace.UnionArgumentTypes, want) {
		t.Errorf("UnionArgumentTypes = %v, want %v", namespace.UnionArgumentTypes, want)
	}
	want := map[string]struct {
		union bool
		model string
	}{
		"list_shapes":    {true, "Shape"},
		"shape_rows":     {true, "Shape"},
		"get_shape":      {true, "Shape"},
		"shapes_by_name": {true, "Shape"},
		"shape_groups":   {true, "Shape"},
		"find_shape":     {true, "Shape"},
		"find_shapes":    {true, "Shape"},
		"page_shapes":    {true, "Shape"},
		"get_marker":     {true, "Marker"},
		"get_drawing":    {false, "Drawing"},
		"draw_shape":     {true, "Shape"},
		"pay":            {false, ""},
		"checkout":       {false, ""},
	}
	for _, endpoint := range namespace.Endpoints {
		w, ok := want[endpoint.MethodName]
		if !ok {
			t.Errorf("unexpected endpoint %s", endpoint.MethodName)
			continue
		}
		delete(want, endpoint.MethodName)
		if endpoint.OutputIsUnion != w.union || endpoint.OutputModelName != w.model {
			t.Errorf("%s = (union %v, %q), want (%v, %q)", endpoint.MethodName, endpoint.OutputIsUnion, endpoint.OutputModelName, w.union, w.model)
		}
	}
	if len(want) > 0 {
		t.Errorf("missing endpoints %v", want)
	}
}

// TestWriteSDKGoldenUnionLists pins every file of the Python SDK for
// union-lists-api with addUnionReadOperations' operations. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenUnionLists -update
func TestWriteSDKGoldenUnionLists(t *testing.T) {
	_, apiOutput := loadUnionModelsAPI(t)
	outDir := t.TempDir()
	if err := WriteSDK(generateUnionModelsSDK(t, apiOutput), outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", sdktest.UnionListsService))
}

// TestUnionListsSDKImportsAndRuns writes the Python types package and the
// Python SDK of union-lists-api, byte-compiles both, and runs
// unionModelsProbe against a local server: a list, a list of lists, one
// value and a page of the union come back as its member models, the
// discriminated Marker as the member its kind names, and a model's union
// fields as members; a value no member reads, or data that is not the list
// it should be, raises SDKError at its path. The probe needs pydantic and
// skips without it.
func TestUnionListsSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadUnionModelsAPI(t)

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
	sdkOutput := generateUnionModelsSDK(t, apiOutput)
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
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
	probe := exec.Command(python, "-c", unionModelsProbe,
		typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// unionModelsProbe takes the types and SDK directories, the SDK package and
// class and the types package as arguments. Its server answers each path
// with the data in answers, in the success envelope.
const unionModelsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

types_dir, sdk_dir, sdk_name, sdk_class, types_name = sys.argv[1:]
sys.path.insert(0, types_dir)
sys.path.insert(0, sdk_dir)
sdk_package = __import__(sdk_name)
types_package = __import__(types_name)
ClientConfig = sdk_package.ClientConfig
Page = sdk_package.Page
SDKError = sdk_package.SDKError
Circle = types_package.Circle
Square = types_package.Square
Drawing = types_package.Drawing
Flag = types_package.Flag

circle = {"radius": 1}
square = {"side": 2}
# Neither Circle nor Square reads a string or a triangle.
triangle = {"sides": 3}
answers = {}
requests = []


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        requests.append((url.path, parse_qs(url.query)))
        data = answers[url.path]
        if callable(data):
            data = data(parse_qs(url.query))
        body = json.dumps({"data": data, "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
SDK = getattr(sdk_package, sdk_class)
shape = SDK(ClientConfig(base_url="http://127.0.0.1:%d" % server.server_address[1])).shape


def described(value):
    if isinstance(value, list):
        return [described(item) for item in value]
    if type(value) is Circle:
        return ("circle", value.radius)
    if type(value) is Square:
        return ("square", value.side)
    raise AssertionError(f"{value!r} is not a Circle or a Square")


def refused(call, message):
    try:
        call()
    except SDKError as err:
        assert message in str(err), f"{message!r} not in {str(err)!r}"
    else:
        raise AssertionError(f"expected an SDKError with {message!r}")


# A list, a list of lists and one value of the union are its members.
answers["/api/shapes"] = [circle, square]
answers["/api/shapes/rows"] = [[circle], [square, circle], []]
answers["/api/shapes/s-1"] = square
assert described(shape.list_shapes()) == [("circle", 1), ("square", 2)]
assert described(shape.shape_rows()) == [[("circle", 1)], [("square", 2), ("circle", 1)], []]
assert described(shape.get_shape("s-1")) == ("square", 2)

# A page's items are members, and so is every item <method>_all yields.
answers["/api/shape-pages"] = lambda query: (
    {"items": [square], "next": None} if query.get("cursor") == ["Mg"] else {"items": [circle, square], "next": "Mg"}
)
page = shape.page_shapes(limit=2)
assert type(page) is Page and page.next == "Mg", page
assert described(page.items) == [("circle", 1), ("square", 2)]
assert described(list(shape.page_shapes_all(limit=2))) == [("circle", 1), ("square", 2), ("square", 2)]

# A model's union fields are members.
answers["/api/drawings/d-1"] = {"title": "two", "shape": circle, "shapes": [square, circle]}
drawing = shape.get_drawing("d-1")
assert type(drawing) is Drawing and drawing.title == "two", drawing
assert described(drawing.shape) == ("circle", 1)
assert described(drawing.shapes) == [("square", 2), ("circle", 1)]

# A discriminated union is the member its kind names.
answers["/api/markers/m-1"] = {"kind": "flag", "label": "finish"}
marker = shape.get_marker("m-1")
assert type(marker) is Flag and marker.kind == "flag" and marker.label == "finish", marker

# A value no member reads raises SDKError at its path.
answers["/api/shapes"] = [circle, "triangle"]
refused(shape.list_shapes, "response data[1] is not a valid Shape")
answers["/api/shapes/rows"] = [[circle], [square, triangle]]
refused(shape.shape_rows, "response data[1][1] is not a valid Shape")
answers["/api/shapes/s-1"] = triangle
refused(lambda: shape.get_shape("s-1"), "response data is not a valid Shape")
answers["/api/shape-pages"] = {"items": [circle, triangle], "next": None}
refused(shape.page_shapes, "response data.items[1] is not a valid Shape")
answers["/api/markers/m-1"] = {"kind": "star", "label": "finish"}
refused(lambda: shape.get_marker("m-1"), "response data is not a valid Marker")
answers["/api/drawings/d-1"] = {"title": "two", "shape": circle, "shapes": [triangle]}
refused(lambda: shape.get_drawing("d-1"), "response data is not a valid Drawing")

# Data that is not the list it should be raises SDKError too.
answers["/api/shapes"] = circle
refused(shape.list_shapes, "expected response data to be a list of Shape")
answers["/api/shapes/rows"] = [circle]
refused(shape.shape_rows, "expected response data[0] to be a list of Shape")
answers["/api/shapes/rows"] = circle
refused(shape.shape_rows, "expected response data to be a list of lists of Shape")
server.shutdown()
`
