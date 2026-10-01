package pysdkgen

import (
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
// argument, and returns the document.
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
			if !slices.Equal(namespace.JSONValueTypes, []string{"GenericJSON"}) {
				t.Errorf("namespace %s JSONValueTypes = %v, want [GenericJSON]", namespace.Name, namespace.JSONValueTypes)
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

// TestGenericJSONArgumentsReachTheGoServer generates the Go types module
// and the Go API module of body-args-api, and its Python types package and
// Python SDK, into one temp tree. It copies apigen's route test into the
// API module for its implementation, and runs pythonSDKServerTest beside
// it: the Python SDK sends an object, an array, numbers, booleans and a
// string as each Generic.JSON argument of storeDocument, and the
// implementation receives each as that JSON value. A value that is no JSON
// value, and None where null is not a value, are refused before the
// request. The probe needs pydantic and skips without it.
func TestGenericJSONArgumentsReachTheGoServer(t *testing.T) {
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
	apiDir := filepath.Join(root, "api", bodyArgsAPI)
	pyTypesDir := filepath.Join(root, "types", "python", bodyArgsAPI)
	pySDKDir := filepath.Join(root, "sdk", "python", bodyArgsAPI)

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
	if err := apigen.SetReplacePaths(apiOutput, paths, apiDir); err != nil {
		t.Fatalf("apigen.SetReplacePaths: %v", err)
	}
	if err := apigen.WriteAPI(apiOutput, apiDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	pyTypesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: bodyArgsAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(pyTypesOutput, pyTypesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, pySDKDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}

	routesTest, err := os.ReadFile(filepath.Join("..", "apigen", "testdata", "body_args_routes_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	probe := fmt.Sprintf(genericJSONProbe, pyTypesDir, pySDKDir, sdkOutput.PackageName, sdkOutput.SDKClassName)
	for name, content := range map[string]string{
		"body_args_routes_test.go": string(routesTest),
		"python_sdk_test.go":       fmt.Sprintf(pythonSDKServerTest, python, probe, "["+strings.Join(genericJSONValues, ",")+"]"),
	} {
		if err := os.WriteFile(filepath.Join(apiDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"test", "-count=1", "-run", "^TestThePythonSDK", "-v", "./..."},
	} {
		// No cmd.Env: exec then sets PWD to cmd.Dir, which keeps the
		// module's relative replace paths valid under a symlinked temp dir.
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
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
// uses. It is formatted with the Python interpreter, genericJSONProbe and
// genericJSONValues as one JSON array.
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
	types "example.com/schemas/types/go/body-args-api"
)

func TestThePythonSDKSendsEachGenericJSONArgumentAsItsJSONValue(t *testing.T) {
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

	const values = %[3]q
	var sent []json.RawMessage
	if err := json.Unmarshal([]byte(values), &sent); err != nil {
		t.Fatal(err)
	}
	probe := exec.Command(%[1]q, "-c", %[2]q, server.URL, values)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %%v\n%%s", err, out)
	}
	if len(received) != len(sent) {
		t.Fatalf("%%d calls reached the implementation, want %%d: a refused value must not be sent", len(received), len(sent))
	}
	for i, raw := range sent {
		value, args := string(raw), received[i]
		for name, want := range map[string]string{
			"document": value,
			"note":     value,
			"extras":   "[" + value + "]",
			"grid":     "[[" + value + "]]",
		} {
			if !jsonEqual(t, args[name], want) {
				t.Errorf("%%s: %%s reached the implementation as %%v, want %%s", value, name, args[name], want)
			}
		}
		if _, ok := args["document"].(types.GenericJSON); !ok {
			t.Errorf("%%s: document is a %%T, want types.GenericJSON", value, args["document"])
		}
	}
}
`

// genericJSONProbe is formatted with the Python types and SDK directories,
// the SDK package and the SDK class. It takes the server's URL and a JSON
// array of values, sends each as every Generic.JSON argument of
// store_document and checks the document comes back as it was sent. Then
// it checks that values the route would refuse are refused before the
// request.
const genericJSONProbe = `
import json
import sys

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[4]q)
sdk = SDK(sdk_package.ClientConfig(base_url=sys.argv[1]))


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
    ({"document": 1, "extras": [None]}, "extras"),
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
