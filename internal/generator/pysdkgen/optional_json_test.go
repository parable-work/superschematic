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
)

// TestOptionalJSONSDKSendsNullApartFromAbsent writes the Python types
// package and the Python SDK of optional-json-api
// (sdktest.LoadOptionalJSONService), byte-compiles both, and runs
// optionalJSONProbe against a local HTTP server: an optional Generic.JSON
// body argument or input type field set to None is sent as null, and one
// left out is not sent. The body arguments of a DELETE are sent in the
// body, as those of a PUT are. The probe needs pydantic and skips without
// it.
func TestOptionalJSONSDKSendsNullApartFromAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	service := sdktest.OptionalJSONService
	schema, err := sdktest.LoadOptionalJSONService()
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: service, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: service, Clock: nestedArraysClock})
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
	probe := exec.Command(python, "-c", fmt.Sprintf(optionalJSONProbe, typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, sdkOutput.TypesPackage))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// optionalJSONProbe is formatted with the types and SDK directories, the
// SDK package, the SDK class and the types package.
const optionalJSONProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sys.path.insert(0, %[2]q)
sdk_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[4]q)
types = __import__(%[5]q)
bodies = []


class Handler(BaseHTTPRequestHandler):
    def _answer(self):
        length = int(self.headers.get("Content-Length") or 0)
        bodies.append(json.loads(self.rfile.read(length)) if length else None)
        body = json.dumps({"data": True, "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    do_POST = do_PUT = do_DELETE = _answer

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))

# A body argument: left out, it is not sent; None sends null.
sdk.note.annotate("n1", "text")
assert bodies[-1] == {"body": "text"}, bodies[-1]
sdk.note.annotate("n1", "text", extra=None)
assert bodies[-1] == {"body": "text", "extra": None}, bodies[-1]
sdk.note.annotate("n1", "text", extra="more")
assert bodies[-1] == {"body": "text", "extra": "more"}, bodies[-1]

# A DELETE sends its body arguments in the body, as a PUT does.
sdk.note.retract("n1", {"a": 1})
assert bodies[-1] == {"body": {"a": 1}}, bodies[-1]
sdk.note.retract("n1", {"a": 1}, extra=None)
assert bodies[-1] == {"body": {"a": 1}, "extra": None}, bodies[-1]

# An input type field: unset, it is not sent; set to None, it is null, from
# a model and from a dict alike.
for revision, want in [
    (types.NoteRevision(body={"a": 1}), {"body": {"a": 1}}),
    (types.NoteRevision(body={"a": 1}, extra=None), {"body": {"a": 1}, "extra": None}),
    ({"body": {"a": 1}}, {"body": {"a": 1}}),
    ({"body": {"a": 1}, "extra": None}, {"body": {"a": 1}, "extra": None}),
    (types.NoteRevision(body={"a": 1}, extra={"b": None}), {"body": {"a": 1}, "extra": {"b": None}}),
]:
    sdk.note.revise(revision)
    assert bodies[-1] == want, (revision, bodies[-1], want)
server.shutdown()
`
