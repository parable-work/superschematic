package pysdkgen

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// bodyArgsAPI is apigen's body-args-api. Its storeDocument takes a
// required, an optional, a list and a list of lists Generic.JSON body
// argument, and returns the document. Its storeEmbedding takes the same of
// Generic.StringMap and Embedding.Vector, and returns the labels.
const bodyArgsAPI = "body-args-api"

func loadBodyArgsAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsAPI))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsAPI, err)
	}
	return schema
}

// TestGenericJSONBodyArgumentsAreJSONValues: each Generic.JSON body
// argument of storeDocument is typed as the types package's GenericJSON,
// the type pygen gives a Generic.JSON field, alone, in a list and in a list
// of lists. The namespace imports GenericJSON, and the optional single one
// still defaults to UNSET.
func TestGenericJSONBodyArgumentsAreJSONValues(t *testing.T) {
	apiOutput, err := apigen.Generate(loadBodyArgsAPI(t), apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsAPI,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdk, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, namespace := range sdk.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			if endpoint.MethodName != "store_document" {
				continue
			}
			want := map[string]string{
				"document": "GenericJSON",
				"note":     "GenericJSON",
				"extras":   "list[GenericJSON]",
				"grid":     "list[list[GenericJSON]]",
			}
			for _, arg := range endpoint.ScalarArgs {
				if arg.PyType != want[arg.PyName] || !arg.IsAnyJSON {
					t.Errorf("%s: type %s, IsAnyJSON %t; want %s, true", arg.PyName, arg.PyType, arg.IsAnyJSON, want[arg.PyName])
				}
			}
			for _, declaration := range []string{
				"document: GenericJSON",
				"note: GenericJSON | None | Unset = UNSET",
				"extras: list[GenericJSON] | None = None",
				"grid: list[list[GenericJSON]] | None = None",
			} {
				if !hasMethodParam(endpoint, declaration) {
					t.Errorf("store_document has no parameter %q: %+v", declaration, endpoint.MethodParams)
				}
			}
			if !slices.Contains(namespace.Imports, "GenericJSON") || slices.Contains(namespace.Imports, "Generic.JSON") {
				t.Errorf("namespace %s imports %v, want GenericJSON", namespace.Name, namespace.Imports)
			}
			if !slices.Contains(namespace.JSONValueTypes, "GenericJSON") {
				t.Errorf("namespace %s JSONValueTypes = %v, want GenericJSON among them", namespace.Name, namespace.JSONValueTypes)
			}
			return
		}
	}
	t.Fatal("body-args-api has no store_document")
}

// TestGenericJSONInTheQueryStringStaysAStr: a GET sends its scalar
// arguments in the query string, where a Generic.JSON argument stays a
// str, the parameter's text, and the same argument of a POST is a JSON
// value.
func TestGenericJSONInTheQueryStringStaysAStr(t *testing.T) {
	param := apigen.Param{Name: "params", Type: "Generic.JSON", Required: true}
	for method, want := range map[string]string{"GET": "str", "POST": "GenericJSON"} {
		endpoint := apigen.EndpointInfo{Path: "/api/previews", Method: method, ScalarArgs: []apigen.Param{param}}
		if method != "GET" {
			endpoint.BodyArgs = []apigen.BodyArg{{Param: param, Kind: "Any", AnyJSON: true}}
		}
		if got := convertEndpoint(endpoint, false, "").ScalarArgs[0]; got.PyType != want || got.IsAnyJSON != (method != "GET") {
			t.Errorf("%s: type %s, IsAnyJSON %t; want %s", method, got.PyType, got.IsAnyJSON, want)
		}
	}
}

// TestGenericJSONArgumentsReachTheGoServer runs genericJSONProbe against
// the generated routes of body-args-api (writeBodyArgsModules): the Python
// SDK sends an object, an array, numbers, booleans and a string as each
// Generic.JSON argument of storeDocument, and the implementation receives
// each as that JSON value. A value that is no JSON value, and None where
// null is not a value, are refused before the request.
func TestGenericJSONArgumentsReachTheGoServer(t *testing.T) {
	modules := writeBodyArgsModules(t)
	want := make([]map[string]string, 0, len(genericJSONValues))
	for _, value := range genericJSONValues {
		want = append(want, map[string]string{
			"document": value,
			"note":     value,
			"extras":   "[" + value + "]",
			"grid":     "[[" + value + "]]",
		})
	}
	modules.runPythonSDK(t, genericJSONProbe, "["+strings.Join(genericJSONValues, ",")+"]", want)
}

// bodyArgsModules is body-args-api generated into one temp tree: the Go
// types module and the Go API module, and the Python types package and the
// Python SDK.
type bodyArgsModules struct {
	python     string
	apiDir     string
	pyTypesDir string
	pySDKDir   string
	sdk        *SDKOutput
}

// writeBodyArgsModules generates body-args-api's modules and copies
// apigen's route test into the API module, for its implementation. It
// skips in -short mode, and without a compatible Python with pydantic.
func writeBodyArgsModules(t *testing.T) bodyArgsModules {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	schema := loadBodyArgsAPI(t)
	paths := testpaths.Local(t)
	typesModule := "example.com/schemas/types/go/" + bodyArgsAPI

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", bodyArgsAPI)
	modules := bodyArgsModules{
		python:     python,
		apiDir:     filepath.Join(root, "api", bodyArgsAPI),
		pyTypesDir: filepath.Join(root, "types", "python", bodyArgsAPI),
		pySDKDir:   filepath.Join(root, "sdk", "python", bodyArgsAPI),
	}

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: bodyArgsAPI, ModulePath: typesModule, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("typegen.Generate: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("typegen.SetReplacePaths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("typegen.WriteTypes: %v", err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:    sessionauth.Provider{},
		SchemaName:  bodyArgsAPI,
		ModulePath:  "example.com/schemas/api/" + bodyArgsAPI,
		TypesModule: typesModule,
		Clock:       nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	if err := apigen.SetReplacePaths(apiOutput, paths, modules.apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(apiOutput, modules.apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	pyTypesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(pyTypesOutput, modules.pyTypesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	modules.sdk, err = Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(modules.sdk, modules.pySDKDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}

	routesTest, err := os.ReadFile(filepath.Join("..", "apigen", "testdata", "body_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modules.apiDir, "body_args_routes_test.go"), routesTest, 0o644); err != nil {
		t.Fatal(err)
	}
	return modules
}

// runPythonSDK runs pythonSDKServerTest in the API module: the probe, after
// probeHeader, runs against the generated routes with arg as its second
// argument, and each call that reaches the implementation must carry, in
// order, the arguments of want (wire name to JSON value). A call the probe
// expects refused must not reach it.
func (m bodyArgsModules) runPythonSDK(t *testing.T, probe, arg string, want []map[string]string) {
	t.Helper()
	calls := make([]map[string]json.RawMessage, 0, len(want))
	for _, args := range want {
		call := map[string]json.RawMessage{}
		for name, value := range args {
			call[name] = json.RawMessage(value)
		}
		calls = append(calls, call)
	}
	wantJSON, err := json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	header := fmt.Sprintf(probeHeader, m.pyTypesDir, m.pySDKDir, m.sdk.PackageName, m.sdk.SDKClassName)
	serverTest := fmt.Sprintf(pythonSDKServerTest, m.python, header+probe, arg, string(wantJSON))
	if err := os.WriteFile(filepath.Join(m.apiDir, "python_sdk_test.go"), []byte(serverTest), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"test", "-count=1", "-run", "^TestThePythonSDK", "-v", "./..."},
	} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
		cmd := exec.Command("go", args...)
		cmd.Dir = m.apiDir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
		if args[0] == "test" {
			t.Logf("generated API tests:\n%s", out)
		}
	}
}

// genericJSONValues are what the Python SDK sends as each Generic.JSON
// argument of storeDocument, one call each: an object, an array, numbers,
// booleans and a string.
var genericJSONValues = []string{`{"a":[1,{"b":null}],"c":"d"}`, `[1,"two",false,null]`, `3.5`, `0`, `true`, `false`, `"text"`}

// pythonSDKServerTest runs in the generated API module of body-args-api
// beside apigen's route test, whose tags implementation and jsonEqual it
// uses. It is formatted with the Python interpreter, the probe, the
// probe's second argument, and the calls that must reach the
// implementation as a JSON array of their arguments.
const pythonSDKServerTest = `package bodyargsapi_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	api "example.com/schemas/api/body-args-api"
)

func TestThePythonSDKSendsEachArgumentAsItsJSONValue(t *testing.T) {
	impl := &tags{}
	router := chi.NewRouter()
	if err := api.RegisterRoutes(router, api.Config{
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Tag: impl},
	}); err != nil {
		t.Fatal(err)
	}
	// received holds the arguments of each call that reached the
	// implementation, in order.
	var received []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls := impl.calls
		router.ServeHTTP(w, r)
		if impl.calls > calls {
			received = append(received, impl.last)
		}
	}))
	t.Cleanup(server.Close)

	probe := exec.Command(%[1]q, "-c", %[2]q, server.URL, %[3]q)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %%v\n%%s", err, out)
	}
	var want []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(%[4]q), &want); err != nil {
		t.Fatal(err)
	}
	if len(received) != len(want) {
		t.Fatalf("%%d calls reached the implementation, want %%d: a refused value must not be sent", len(received), len(want))
	}
	for i, args := range want {
		for name, value := range args {
			if !jsonEqual(t, received[i][name], string(value)) {
				t.Errorf("call %%d: %%s reached the implementation as %%v, want %%s", i, name, received[i][name], value)
			}
		}
	}
}
`

// probeHeader starts every probe. It is formatted with the Python types
// and SDK directories, the SDK package and the SDK class, and binds
// sdk_package and sdk, a client of the server whose URL is the probe's
// first argument.
const probeHeader = `
import json
import sys

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[4]q)
sdk = SDK(sdk_package.ClientConfig(base_url=sys.argv[1]))
`

// genericJSONProbe takes a JSON array of values, sends each as every
// Generic.JSON argument of store_document and checks the document comes
// back as it was sent. Then it checks that values the route would refuse
// are refused before the request.
const genericJSONProbe = `

def same(got, want):
    return json.dumps(got, sort_keys=True) == json.dumps(want, sort_keys=True)


for value in json.loads(sys.argv[2]):
    got = sdk.tag.store_document(value, note=value, extras=[value], grid=[[value]])
    assert same(got, value), (value, got)

# None where null is not a value, and a value JSON cannot hold, are refused
# at their path; none reaches the server.
for kwargs, field in [
    ({"document": None}, "document"),
    ({"document": float("nan")}, "document"),
    ({"document": {1: "a"}}, "document"),
    ({"document": 1, "extras": [1, None]}, "extras[1]"),
    ({"document": 1, "grid": [[None]]}, "grid[0][0]"),
    ({"document": 1, "note": {"a": float("inf")}}, "note"),
]:
    try:
        sdk.tag.store_document(**kwargs)
    except sdk_package.ValidationError as err:
        assert field in err.errors, (kwargs, err.errors)
    else:
        raise AssertionError(f"{kwargs} was sent")
`
