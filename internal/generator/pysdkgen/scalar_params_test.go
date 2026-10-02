package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// scalarParamsAPI is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const scalarParamsAPI = "scalar-params-api"

// loadScalarParamsAPI loads scalar-params-api: path, query, GET and body
// arguments typed with Ordering.Rank and Generic.Probability, number
// scalars whose names give no hint of their type, next to Generic.Int64,
// Identity.UUID and the Shade enum.
func loadScalarParamsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", scalarParamsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", scalarParamsAPI, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: scalarParamsAPI,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestParameterTypesFollowScalarParseFlags pins the Python type of a path
// parameter, a scoped namespace's scope parameter, a query parameter, a
// scalar argument and the element of a list argument to apigen's parse
// flags, whatever the scalar's name: an integer scalar is int, a float
// float, a boolean bool, a UUID or date-time str. A Param without flags (a
// builtin, an enum) maps by name.
func TestParameterTypesFollowScalarParseFlags(t *testing.T) {
	for _, test := range []struct {
		param apigen.Param
		want  string
	}{
		{apigen.Param{Type: "Ordering.Rank", IsInt: true}, "int"},
		{apigen.Param{Type: "Generic.Int64", IsInt: true}, "int"},
		{apigen.Param{Type: "Generic.Probability", IsFloat: true}, "float"},
		{apigen.Param{Type: "Acme.Flag", IsBool: true}, "bool"},
		{apigen.Param{Type: "Identity.UUID", IsUUID: true}, "str"},
		{apigen.Param{Type: "Temporal.DateTime", IsDateTime: true}, "str"},
		{apigen.Param{Type: "string", IsString: true}, "str"},
		{apigen.Param{Type: "number"}, "float"},
		{apigen.Param{Type: "boolean"}, "bool"},
		{apigen.Param{Type: "Shade"}, "Shade"},
	} {
		t.Run(test.param.Type, func(t *testing.T) {
			test.param.Name = "value"
			list := test.param
			list.Name, list.IsArray = "values", true
			endpoint := convertEndpoint(apigen.EndpointInfo{
				Path: "/api/items/{value}", Method: "GET",
				PathParams:  []apigen.Param{test.param},
				QueryParams: []apigen.Param{test.param},
				ScalarArgs:  []apigen.Param{test.param, list},
			}, false, "")
			if got := endpoint.PathParams[0].PyType; got != test.want {
				t.Errorf("path parameter type = %s, want %s", got, test.want)
			}
			if got := endpoint.QueryParams[0].PyType; got != test.want {
				t.Errorf("query parameter type = %s, want %s", got, test.want)
			}
			if got := endpoint.ScalarArgs[0].PyType; got != test.want {
				t.Errorf("scalar argument type = %s, want %s", got, test.want)
			}
			if got := endpoint.ScalarArgs[1]; got.PyType != "list["+test.want+"]" || got.PyElementType != test.want {
				t.Errorf("list argument = %s of %s, want list[%s] of %s", got.PyType, got.PyElementType, test.want, test.want)
			}

			sdk, err := Generate(&apigen.APIOutput{
				SchemaName: "items-api",
				Endpoints: []apigen.EndpointInfo{{
					Name: "getItem", Namespace: "items", Path: "/api/items/{value}", Method: "GET",
					PathParams:       []apigen.Param{test.param},
					IsScopedEndpoint: true, ScopeParamName: "value",
				}},
			}, "", "", codegen.DefaultClock())
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if got := sdk.Namespaces[0].ScopeParamType; got != test.want {
				t.Errorf("scope parameter type = %s, want %s", got, test.want)
			}
		})
	}
}

// TestScalarParamsFollowScalarDefinitions: each parameter of
// scalar-params-api takes the Python type of the Go type its route parses,
// which apigen reads from the scalar's definition rather than its name:
// Ordering.Rank, an integer, is int where the route parses an int64, and
// Generic.Probability, a float, is float where it parses a float64, each
// alone and as the element of a list. Only the enum is imported from the
// types package.
func TestScalarParamsFollowScalarDefinitions(t *testing.T) {
	_, apiOutput := loadScalarParamsAPI(t)
	sdk, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sdk.Namespaces) != 1 {
		t.Fatalf("namespaces = %+v, want one", sdk.Namespaces)
	}
	namespace := sdk.Namespaces[0]
	if want := []string{"Shade"}; !reflect.DeepEqual(namespace.Imports, want) {
		t.Errorf("imports = %v, want %v", namespace.Imports, want)
	}

	goEndpoints := map[string]apigen.EndpointInfo{}
	for _, endpoint := range apiOutput.Endpoints {
		goEndpoints[toPythonIdentifier(endpoint.Name)] = endpoint
	}
	type param struct{ name, goType, pyType string }
	for _, test := range []struct {
		method                        string
		pathParams, queryParams, args []param
	}{
		{
			method:     "ranked_items",
			pathParams: []param{{"shelf", "types.IdentityUUID", "str"}, {"rank", "int64", "int"}},
			queryParams: []param{
				{"minScore", "float64", "float"},
				{"limit", "int64", "int"},
				{"shade", "types.Shade", "Shade"},
			},
			args: []param{
				{"ranks", "int64", "list[int]"},
				{"scores", "float64", "list[float]"},
				{"shades", "types.Shade", "list[Shade]"},
				{"maxScore", "float64", "float"},
			},
		},
		{
			method:     "rank_item",
			pathParams: []param{{"shelf", "types.IdentityUUID", "str"}},
			args: []param{
				{"rank", "int64", "int"},
				{"score", "float64", "float"},
				{"ranks", "int64", "list[int]"},
			},
		},
	} {
		t.Run(test.method, func(t *testing.T) {
			var endpoint *EndpointInfo
			for i := range namespace.Endpoints {
				if namespace.Endpoints[i].MethodName == test.method {
					endpoint = &namespace.Endpoints[i]
				}
			}
			if endpoint == nil {
				t.Fatalf("no endpoint %s in %+v", test.method, namespace.Endpoints)
			}
			goEndpoint := goEndpoints[test.method]

			var gotPath, gotQuery, gotArgs []param
			for i, p := range endpoint.PathParams {
				gotPath = append(gotPath, param{p.Name, goEndpoint.PathParams[i].GoType, p.PyType})
			}
			for i, p := range endpoint.QueryParams {
				gotQuery = append(gotQuery, param{p.Name, goEndpoint.QueryParams[i].GoType, p.PyType})
			}
			for i, p := range endpoint.ScalarArgs {
				gotArgs = append(gotArgs, param{p.Name, goEndpoint.ScalarArgs[i].GoType, p.PyType})
			}
			if !reflect.DeepEqual(gotPath, test.pathParams) {
				t.Errorf("path parameters = %v, want %v", gotPath, test.pathParams)
			}
			if !reflect.DeepEqual(gotQuery, test.queryParams) {
				t.Errorf("query parameters = %v, want %v", gotQuery, test.queryParams)
			}
			if !reflect.DeepEqual(gotArgs, test.args) {
				t.Errorf("scalar arguments = %v, want %v", gotArgs, test.args)
			}
		})
	}
}

// TestScalarParamsSDKSendsParsedTypes writes the Python types package and
// the Python SDK of scalar-params-api, byte-compiles both, and runs
// scalarParamsProbe against a local HTTP server: an int rank and a float
// score reach the path, the query string and the body in the shapes the Go
// route parses, and a string where the route parses a number fails
// validation at its argument before any request. The probe needs pydantic
// and skips without it.
func TestScalarParamsSDKSendsParsedTypes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadScalarParamsAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: scalarParamsAPI, Clock: nestedArraysClock})
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
	probe := exec.Command(python, "-c", fmt.Sprintf(scalarParamsProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// scalarParamsProbe is formatted with the types and SDK directories, the
// SDK package and the SDK class.
const scalarParamsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[4]q)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError

shelf_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
calls = []


class Handler(BaseHTTPRequestHandler):
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        url = urlsplit(self.path)
        calls.append((self.command, url.path, parse_qs(url.query), json.loads(raw) if raw else None))
        body = json.dumps({"data": ["a"], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_GET = do_PUT = _answer

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))

# Numbers reach the path and the query string as the route parses them:
# an integer rank and a float score.
sdk.shelf.ranked_items(
    shelf_id,
    3,
    min_score=0.5,
    limit=10,
    ranks=[1, 2],
    scores=[0.25, 1.5],
    max_score=0.75,
)
method, path, query, body = calls[-1]
assert (method, path, body) == ("GET", "/api/shelves/%%s/ranks/3" %% shelf_id, None), calls[-1]
assert query == {
    "minScore": ["0.5"],
    "limit": ["10"],
    "ranks": ["1,2"],
    "scores": ["0.25,1.5"],
    "maxScore": ["0.75"],
}, query

# Body arguments travel as JSON numbers.
sdk.shelf.rank_item(shelf_id, 4, score=0.75, ranks=[5])
assert calls[-1][3] == {"rank": 4, "score": 0.75, "ranks": [5]}, calls[-1]

# A string where the route parses a number fails before any request.
before = len(calls)
for call, field in [
    (lambda: sdk.shelf.ranked_items(shelf_id, 3, ranks=["1"]), "ranks[0]"),
    (lambda: sdk.shelf.ranked_items(shelf_id, 3, max_score="0.5"), "max_score"),
    (lambda: sdk.shelf.rank_item(shelf_id, "4"), "rank"),
]:
    try:
        call()
    except ValidationError as err:
        assert list(err.errors) == [field], err.errors
    else:
        raise AssertionError("expected a ValidationError for %%s" %% field)
assert len(calls) == before, calls[before:]
server.shutdown()
`
