package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestClientVersionAndItsRefusal runs clientVersionProbe against a local
// server (D143): ClientConfig.client puts X-Client-Version on every call,
// a client without one sends no such header, a name or a version the
// header cannot carry is refused where the client is built, and a 426
// client_too_old raises ClientTooOldError with the client, its version and
// the minimum.
func TestClientVersionAndItsRefusal(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Python run in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	_, apiOutput := loadNestedArraysAPI(t, sdktest.AddCellOperation)
	sdkDir := filepath.Join(t.TempDir(), "sdk")
	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir)
	probe := exec.Command(python, "-c", fmt.Sprintf(clientVersionProbe, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// clientVersionProbe is formatted with the SDK directory, package and
// class. The label segment of grid.cell's path names what to answer: "ok"
// the cell, "old" the 426 client_too_old, "tls" a 426 of another code.
const clientVersionProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)
ClientConfig = sdk_package.ClientConfig
ClientIdentity = sdk_package.ClientIdentity

grid_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
seen = []


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        seen.append(self.headers.get("X-Client-Version"))
        label = self.path.split("/")[5]
        if label == "ok":
            status, content_type = 200, "application/json"
            body = {"data": "cell", "meta": {"requestId": "req-1"}}
        elif label == "old":
            status, content_type = 426, "application/problem+json"
            body = {
                "type": "about:blank", "title": "Upgrade Required", "status": 426,
                "detail": "cli 0.4.0 is older than 0.5.0", "code": "client_too_old", "requestId": "req-9",
                "details": {"client": "cli", "version": "0.4.0", "minimum": "0.5.0"},
            }
        else:
            status, content_type = 426, "application/problem+json"
            body = {"status": 426, "detail": "use TLS", "code": "upgrade_required"}
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
base_url = "http://127.0.0.1:%%d" %% server.server_address[1]

named = SDK(ClientConfig(base_url=base_url, client=ClientIdentity(name="acme-cli", version="0.5.0-rc.1+b7")))
named.grid.cell(grid_id, "ok")
# A header of the same name a call passes does not replace the client's.
named.grid.cell(grid_id, "ok", extra_headers={"X-Client-Version": "other/9.9.9"})
SDK(ClientConfig(base_url=base_url)).grid.cell(grid_id, "ok")
assert seen == ["acme-cli/0.5.0-rc.1+b7", "acme-cli/0.5.0-rc.1+b7", None], seen

for name, version in [
    ("", "1.0.0"),
    ("acme/cli", "1.0.0"),
    ("acme cli", "1.0.0"),
    ("-cli", "1.0.0"),
    ("cli", ""),
    ("cli", "1.0.0\r\nX-Other: 1"),
    ("cli", "1.0/0"),
    ("cli", "1.0.0\n"),
]:
    try:
        SDK(ClientConfig(base_url=base_url, client=ClientIdentity(name=name, version=version)))
    except sdk_package.SDKError as err:
        assert "client." in str(err), err
    else:
        raise AssertionError("client %%r/%%r was not refused" %% (name, version))

old = SDK(ClientConfig(base_url=base_url, client=ClientIdentity(name="cli", version="0.4.0")))
try:
    old.grid.cell(grid_id, "old")
except sdk_package.ClientTooOldError as err:
    assert isinstance(err, sdk_package.APIError)
    assert (err.client, err.version, err.minimum) == ("cli", "0.4.0", "0.5.0"), vars(err)
    assert (err.status_code, err.code, err.request_id) == (426, "client_too_old", "req-9"), vars(err)
    assert err.message == "cli 0.4.0 is older than 0.5.0", err.message
else:
    raise AssertionError("client_too_old was not raised")

# A 426 with another code is the plain error.
try:
    old.grid.cell(grid_id, "tls")
except sdk_package.ClientTooOldError:
    raise AssertionError("another 426 raised ClientTooOldError")
except sdk_package.APIError as err:
    assert (err.status_code, err.code) == (426, "upgrade_required"), vars(err)
else:
    raise AssertionError("426 was not raised")
server.shutdown()
`
