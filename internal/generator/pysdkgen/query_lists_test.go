package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// bodyArgsService is apigen's body-args-api fixture: its searchPosts is a
// GET with a @query list (tags) beside list scalar arguments.
const bodyArgsService = "body-args-api"

func loadBodyArgsAPI(t *testing.T) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", bodyArgsService))
	if err != nil {
		t.Fatalf("load %s: %v", bodyArgsService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: bodyArgsService,
		Clock:      fixtureClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// TestWriteSDKGoldenQueryLists pins the namespace modules of the Python SDK
// for body-args-api: a @query list is typed list[T], its bounds and each
// element are checked, and it is sent as one comma-separated value, as a
// list scalar argument of a GET is. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestWriteSDKGoldenQueryLists -update
func TestWriteSDKGoldenQueryLists(t *testing.T) {
	_, apiOutput := loadBodyArgsAPI(t)
	outDir := t.TempDir()
	sdkOutput := writeFixtureSDK(t, apiOutput, outDir)
	compareGoldenTree(t, filepath.Join(outDir, sdkOutput.PackageName, "namespaces"), filepath.Join("testdata", "golden", bodyArgsService, "namespaces"))
}

// TestQueryListsSDKImportsAndRuns runs a probe against a local HTTP server
// for two SDKs: body-args-api, with its types package, whose optional
// @query list of strings has a list bound and an element minLength, and
// fixture-api, without one, whose listTenants takes a required @query list
// and an optional one. Each list goes out as ?name=a,b, the form the routes
// read, an empty optional list is left out, since a route reads no item as
// an absent list, and a bad list or element fails before any request. So
// does a string, which the parameter took before it was a list. The probe
// needs pydantic and skips without it.
func TestQueryListsSDKImportsAndRuns(t *testing.T) {
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

	t.Run(bodyArgsService, func(t *testing.T) {
		schema, apiOutput := loadBodyArgsAPI(t)
		root := t.TempDir()
		typesDir := filepath.Join(root, "types")
		sdkDir := filepath.Join(root, "sdk")
		typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: bodyArgsService, Clock: fixtureClock})
		if err != nil {
			t.Fatalf("pygen.Generate: %v", err)
		}
		if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
			t.Fatalf("pygen.WriteTypes: %v", err)
		}
		sdkOutput := writeFixtureSDK(t, apiOutput, sdkDir)
		runQueryListsProbe(t, python, root, sdkOutput, []string{typesDir, sdkDir}, bodyArgsQueryListsProbe)
	})

	t.Run("fixture-api", func(t *testing.T) {
		root := t.TempDir()
		sdkOutput := writeFixtureSDK(t, loadFixtureAPI(t), root)
		runQueryListsProbe(t, python, root, sdkOutput, []string{root}, fixtureAPIQueryListsProbe)
	})
}

// runQueryListsProbe byte-compiles root and runs queryListsProbePrelude,
// then probe, with paths on sys.path.
func runQueryListsProbe(t *testing.T, python, root string, sdkOutput *SDKOutput, paths []string, probe string) {
	t.Helper()
	if out, err := exec.Command(python, "-m", "compileall", "-q", root).CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	sysPath := ""
	for _, path := range paths {
		sysPath += fmt.Sprintf("sys.path.insert(0, %q)\n", path)
	}
	script := fmt.Sprintf(queryListsProbePrelude, sysPath, sdkOutput.PackageName, sdkOutput.SDKClassName) + probe
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// queryListsProbePrelude is formatted with the sys.path lines, the SDK
// package and the SDK class. Its server answers every GET with an empty
// list and records the path and the parsed query of each request.
const queryListsProbePrelude = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import parse_qs, urlsplit

%s
sdk_package = __import__(%q)
ValidationError = sdk_package.ValidationError
requests = []


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        url = urlsplit(self.path)
        requests.append((url.path, parse_qs(url.query, keep_blank_values=True)))
        body = json.dumps({"data": [], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
config = sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1], auth_token="token")
sdk = getattr(sdk_package, %q)(config)


def refused(call, field, validator):
    before = len(requests)
    try:
        call()
    except ValidationError as err:
        assert [e["validator"] for e in err.errors.get(field, [])] == [validator], err.errors
    else:
        raise AssertionError("expected %%s at %%s" %% (validator, field))
    assert len(requests) == before, requests[before:]
`

// bodyArgsQueryListsProbe runs after queryListsProbePrelude against the
// body-args-api SDK. tags is an optional list of strings, at most two, each
// of two or more characters.
const bodyArgsQueryListsProbe = `
# A @query list is one comma-separated value, as is a list scalar argument.
sdk.tag.search_posts(tags=["ab", "cd"], scores=[1.5, 2.25])
assert requests[-1] == ("/api/posts/search", {"tags": ["ab,cd"], "scores": ["1.5,2.25"]}), requests[-1]

# An empty optional list is left out, as None is.
sdk.tag.search_posts(tags=[])
assert requests[-1] == ("/api/posts/search", {}), requests[-1]

# listMax bounds the list; each element is checked for its type and minLength.
refused(lambda: sdk.tag.search_posts(tags=["ab", "cd", "ef"]), "tags", "listMax")
refused(lambda: sdk.tag.search_posts(tags=["ab", "c"]), "tags", "minLength")
refused(lambda: sdk.tag.search_posts(tags=["ab", 7]), "tags", "type")

# A string is not split into one item per character.
refused(lambda: sdk.tag.search_posts(tags="abcd"), "tags", "type")
server.shutdown()
`

// fixtureAPIQueryListsProbe runs after queryListsProbePrelude against the
// fixture-api SDK. ids is a required list of UUIDs, statuses an optional
// list of an enum.
const fixtureAPIQueryListsProbe = `
ids = ["0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", "5d1c7a2e-3b4f-4e6a-8c9d-1a2b3c4d5e6f"]

sdk.tenant.list_tenants(ids, statuses=["active", "suspended"])
assert requests[-1] == ("/api/tenants", {"ids": [",".join(ids)], "statuses": ["active,suspended"]}), requests[-1]

# A required list needs an item: the route reads none as absent.
refused(lambda: sdk.tenant.list_tenants([]), "ids", "required")
refused(lambda: sdk.tenant.list_tenants(None), "ids", "required")
refused(lambda: sdk.tenant.list_tenants([ids[0], 7]), "ids", "type")
refused(lambda: sdk.tenant.list_tenants(ids[0]), "ids", "type")
refused(lambda: sdk.tenant.list_tenants(ids, statuses="active"), "statuses", "type")
server.shutdown()
`
