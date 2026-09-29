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
	"github.com/parable-work/superschematic/internal/loader"
)

// TestInputRulesRefusedBeforeRequest writes the Python types package and
// the Python SDK of fixture-nested-arrays-api with grid.placeOrder added
// (sdktest.AddPlaceOrderOperation), and runs inputRulesProbe against a
// local HTTP server: an input that breaks a schema rule pydantic does not
// check is refused with a ValidationError before any request, with the
// rule names and paths the Go and TypeScript SDKs report (lines,
// lines[1].quantity, shipTo.postalCode, giftCodes[1], and a map value's
// extras.gift.quantity as TypeScript names it), and a valid input is
// sent. The probe needs pydantic and skips without it.
func TestInputRulesRefusedBeforeRequest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Python SDK probe in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}

	schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
	if err != nil {
		t.Fatalf("load %s: %v", nestedArraysService, err)
	}
	if err := sdktest.AddPlaceOrderOperation(schema); err != nil {
		t.Fatal(err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: nestedArraysService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

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

	probe := exec.Command(python, "-B", "-c", fmt.Sprintf(inputRulesProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, typesOutput.PythonModuleName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// inputRulesProbe is formatted with the types and SDK directories, the SDK
// package and class, and the types package.
const inputRulesProbe = `
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
PlaceOrderInput = types_package.PlaceOrderInput

view = {"id": "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40", "labels": [], "shades": [], "polygons": []}
calls = []


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        calls.append((self.command, self.path, json.loads(self.rfile.read(length))))
        body = json.dumps({"data": view, "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))


def refused(input_data):
    try:
        sdk.grid.place_order(input_data)
    except ValidationError as err:
        return {key: [e["validator"] for e in entries] for key, entries in err.errors.items()}
    raise AssertionError("expected a ValidationError for %%r" %% (input_data,))


line = {"productId": "sku-1", "quantity": 2}
ship_to = {"postalCode": "12345"}

# The input's own rules, keyed by wire name.
empty = refused({"lines": [], "shipTo": ship_to})
assert empty == {"lines": ["listMin"]}, empty
assert refused({"lines": [line] * 4, "shipTo": ship_to}) == {"lines": ["listMax"]}
codes = refused({"lines": [line], "shipTo": ship_to, "giftCodes": ["ok", "far too long"]})
assert codes == {"giftCodes[1]": ["maxLength"]}, codes

# A nested object's rules, under the path that reached it; a map value's
# under field.key, as TypeScript and pydantic's own errors name it.
nested = refused({
    "lines": [line, {"productId": "ab", "quantity": 0}],
    "shipTo": {"postalCode": "abc"},
    "extras": {"gift": {"productId": "sku-2", "quantity": 100}},
})
assert nested == {
    "lines[1].productId": ["minLength"],
    "lines[1].quantity": ["min"],
    "shipTo.postalCode": ["pattern"],
    "extras.gift.quantity": ["max"],
}, nested

# A model the caller built is checked the same way.
built = PlaceOrderInput.model_validate({"lines": [], "shipTo": ship_to})
assert refused(built) == {"lines": ["listMin"]}
assert calls == [], calls

# A valid input is sent.
sdk.grid.place_order({"lines": [line], "shipTo": ship_to, "giftCodes": ["ok"]})
assert len(calls) == 1, calls
method, path, body = calls[0]
assert (method, path) == ("POST", "/api/orders"), calls[0]
assert body["lines"] == [{"productId": "sku-1", "quantity": 2}], body
assert body["shipTo"] == ship_to and body["giftCodes"] == ["ok"], body
server.shutdown()
`
