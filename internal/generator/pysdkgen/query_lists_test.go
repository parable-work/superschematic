package pysdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// queryListsService is a schema local to this package's testdata, so its
// operations change no other generator's goldens.
const queryListsService = "query-lists-api"

// loadQueryListsAPI loads query-lists-api: list query parameters of an
// enum, a UUID scalar, an integer scalar, strings and booleans.
func loadQueryListsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", queryListsService))
	if err != nil {
		t.Fatalf("load %s: %v", queryListsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: queryListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

func generateQueryListsSDK(t *testing.T, apiOutput *apigen.APIOutput) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(sdkOutput.Namespaces) != 1 || len(sdkOutput.Namespaces[0].Endpoints) != 1 {
		t.Fatalf("namespaces = %+v", sdkOutput.Namespaces)
	}
	return sdkOutput
}

// TestListQueryParamsArePythonLists: a list query parameter is typed
// list[T] of the enum or of the element's Python type, not as one value,
// and the namespace carries the list helpers.
func TestListQueryParamsArePythonLists(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	namespace := generateQueryListsSDK(t, apiOutput).Namespaces[0]
	if !namespace.HasQueryLists {
		t.Error("HasQueryLists = false, want true")
	}
	endpoint := namespace.Endpoints[0]
	want := []struct {
		name, pyType, pyElementType string
		isArray                     bool
	}{
		{"ids", "list[str]", "str", true},
		{"shades", "list[Shade]", "Shade", true},
		{"ranks", "list[int]", "int", true},
		{"codes", "list[str]", "str", true},
		{"tags", "list[str]", "str", true},
		{"flags", "list[bool]", "bool", true},
		{"limit", "float", "", false},
	}
	if len(endpoint.QueryParams) != len(want) {
		t.Fatalf("query params = %+v", endpoint.QueryParams)
	}
	for i, w := range want {
		if got := endpoint.QueryParams[i]; got.Name != w.name || got.PyType != w.pyType || got.PyElementType != w.pyElementType || got.IsArray != w.isArray {
			t.Errorf("parameter %d = %s %s of %q (list %t), want %s %s of %q (list %t)",
				i, got.Name, got.PyType, got.PyElementType, got.IsArray, w.name, w.pyType, w.pyElementType, w.isArray)
		}
	}
	for _, declaration := range []string{"ids: list[str]", "shades: list[Shade] | None = None", "flags: list[bool] | None = None"} {
		if !hasMethodParam(endpoint, declaration) {
			t.Errorf("count_posts has no parameter %q: %+v", declaration, endpoint.MethodParams)
		}
	}
}

// TestFixtureAPIListTenantsTakesLists pins fixture-api's listTenants: a
// required UUID list and an optional enum list.
func TestFixtureAPIListTenantsTakesLists(t *testing.T) {
	sdkOutput, err := Generate(loadFixtureAPI(t), "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, namespace := range sdkOutput.Namespaces {
		for _, endpoint := range namespace.Endpoints {
			if endpoint.MethodName != "list_tenants" {
				continue
			}
			if !namespace.HasQueryLists {
				t.Errorf("namespace %s: HasQueryLists = false, want true", namespace.Name)
			}
			for _, declaration := range []string{"ids: list[str]", "statuses: list[TenantListStatus] | None = None"} {
				if !hasMethodParam(endpoint, declaration) {
					t.Errorf("list_tenants has no parameter %q: %+v", declaration, endpoint.MethodParams)
				}
			}
			return
		}
	}
	t.Fatal("fixture-api has no list_tenants")
}

func hasMethodParam(endpoint EndpointInfo, declaration string) bool {
	return slices.ContainsFunc(endpoint.MethodParams, func(param MethodParam) bool {
		return param.Declaration == declaration
	})
}

// TestWriteSDKGoldenQueryLists pins the namespace module of the Python SDK
// for query-lists-api. Regenerate with
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenQueryLists -update
func TestWriteSDKGoldenQueryLists(t *testing.T) {
	_, apiOutput := loadQueryListsAPI(t)
	sdkOutput := generateQueryListsSDK(t, apiOutput)
	outDir := t.TempDir()
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	name := filepath.Join(sdkOutput.PackageName, "namespaces", sdkOutput.Namespaces[0].ModuleName+".py")
	got, err := os.ReadFile(filepath.Join(outDir, name))
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "golden", queryListsService, name)
	if *update {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden %s (run with -update): %v", name, err)
	}
	if string(got) != string(want) {
		t.Errorf("%s differs from golden (run with -update to accept)", name)
	}
}

// TestQueryListsSDKImportsAndRuns writes the Python types package and the
// Python SDK of query-lists-api, byte-compiles both, and runs
// queryListsProbe against a local HTTP server: a list query parameter
// crosses the wire as one comma-separated value, an enum item by its
// serialized value, an empty list is left out, and a missing required
// list, a list out of its bounds or an item that fails a rule, its own
// validation or the comma-separated form is refused at its path before any
// request. The probe needs pydantic and skips without it.
func TestQueryListsSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, apiOutput := loadQueryListsAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: queryListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput := generateQueryListsSDK(t, apiOutput)
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
	probe := exec.Command(python, "-c", queryListsProbe,
		typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName,
		sdkOutput.Namespaces[0].AccessorName)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// queryListsProbe takes the types and SDK directories, the SDK package and
// class, the types package and the namespace accessor as arguments. The
// server answers every request with the success envelope around 3.
const queryListsProbe = `
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

types_dir, sdk_dir, sdk_name, sdk_class, types_name, accessor = sys.argv[1:]
sys.path.insert(0, types_dir)
sys.path.insert(0, sdk_dir)
sdk_package = __import__(sdk_name)
types_package = __import__(types_name)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError
Shade = types_package.Shade

ID_A = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
ID_B = "5d2f8a3c-1e40-4c1a-9b7e-0b9a4e1c6f2d"
queries = []


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        assert url.path == "/api/posts/count", self.path
        queries.append(parse_qs(url.query, keep_blank_values=True))
        body = b'{"data": 3, "meta": {"requestId": "req-1"}}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = getattr(sdk_package, sdk_class)(ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}"))
posts = getattr(sdk, accessor)


def refused(call, want):
    """Call, and check the first validator at each path of the ValidationError."""
    try:
        call()
    except ValidationError as err:
        got = {path: errors[0]["validator"] for path, errors in err.errors.items()}
        assert got == want, f"validators = {got}, want {want}"
    else:
        raise AssertionError(f"expected a ValidationError for {want}")


# Each list is one comma-separated value: an enum item by its serialized
# value, a bool as true or false.
count = posts.count_posts(
    [ID_A, ID_B],
    shades=[Shade.Light, "dark"],
    ranks=[1, 100],
    codes=["ab", "wxyz"],
    tags=["py", "sdk"],
    flags=[True, False],
    limit=20,
)
assert count == 3, count
want = {
    "ids": [f"{ID_A},{ID_B}"],
    "shades": ["light,dark"],
    "ranks": ["1,100"],
    "codes": ["ab,wxyz"],
    "tags": ["py,sdk"],
    "flags": ["true,false"],
    "limit": ["20"],
}
assert queries == [want], queries

# An empty list is left out, as the API refuses a present empty value, and
# listMin bounds only a list that is sent.
posts.count_posts((ID_A,), shades=[], codes=[])
assert queries[-1] == {"ids": [ID_A]}, queries[-1]

before = len(queries)
# A required list needs an item, and a list must be a list.
for ids in (None, [], ()):
    refused(lambda: posts.count_posts(ids), {"ids": "required"})
refused(lambda: posts.count_posts(f"{ID_A},{ID_B}"), {"ids": "type"})

# An item the comma-separated value cannot carry is refused at its index.
refused(lambda: posts.count_posts([ID_A, ""]), {"ids[1]": "required"})
refused(
    lambda: posts.count_posts([ID_A], tags=["", "a,b", " c", "ok", None]),
    {"tags[0]": "required", "tags[1]": "pattern", "tags[2]": "pattern", "tags[4]": "required"},
)

# listMin and listMax bound the list; each item is checked against the
# rules, then validated as the element type.
refused(lambda: posts.count_posts([ID_A], shades=[Shade.Light]), {"shades": "listMin"})
refused(
    lambda: posts.count_posts([ID_A], shades=[Shade.Light, Shade.Dark, Shade.Light, "dim"]),
    {"shades": "listMax", "shades[3]": "invalid"},
)
refused(lambda: posts.count_posts([ID_A], ranks=[0, 5, 101, "7"]), {"ranks[0]": "min", "ranks[2]": "max", "ranks[3]": "type"})
refused(
    lambda: posts.count_posts([ID_A], codes=["a", "abcde", "AB", "ab"]),
    {"codes[0]": "minLength", "codes[1]": "maxLength", "codes[2]": "pattern"},
)
refused(lambda: posts.count_posts([ID_A], flags=[True, "yes"]), {"flags[1]": "type"})
refused(lambda: posts.count_posts([ID_A], limit=51), {"limit": "max"})
assert len(queries) == before, queries[before:]
server.shutdown()
`
