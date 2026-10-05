package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestAnErrorCarriesTheProblemsCodeAndRequestID runs problemFieldsProbe
// against a local server that answers grid.cell with an RFC 9457 problem
// body: each error carries the status, the problem's detail as its
// message, its code and its request id (the body's requestId, else the
// X-Request-Id header), and a 429 its Retry-After, as the client SDKs
// guide says.
func TestAnErrorCarriesTheProblemsCodeAndRequestID(t *testing.T) {
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
	probe := exec.Command(python, "-c", fmt.Sprintf(problemFieldsProbe, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// problemFieldsProbe is formatted with the SDK directory, package and class.
// The label segment of grid.cell's path names the status to answer.
const problemFieldsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)
ClientConfig = sdk_package.ClientConfig

grid_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"


class Handler(BaseHTTPRequestHandler):
    # "header" answers a 500 whose request id is only in X-Request-Id.
    def do_GET(self):
        label = self.path.split("/")[5]
        status = 500 if label == "header" else int(label)
        problem = {
            "type": "about:blank",
            "title": "Refused",
            "status": status,
            "detail": "detail %%d" %% status,
            "code": "code_%%d" %% status,
        }
        if label != "header":
            problem["requestId"] = "req-%%d" %% status
        body = json.dumps(problem).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/problem+json")
        self.send_header("Content-Length", str(len(body)))
        if label == "header":
            self.send_header("X-Request-Id", "req-from-header")
        if status == 429:
            self.send_header("Retry-After", "7")
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1], max_rate_limit_retries=0))

for status, error_class in [
    (401, sdk_package.AuthenticationError),
    (403, sdk_package.AuthorizationError),
    (413, sdk_package.APIError),
    (429, sdk_package.RateLimitError),
    (504, sdk_package.APIError),
]:
    try:
        sdk.grid.cell(grid_id, str(status))
    except error_class as err:
        assert err.status_code == status, (status, err.status_code)
        assert err.message == "detail %%d" %% status, (status, err.message)
        assert err.code == "code_%%d" %% status, (status, err.code)
        assert err.request_id == "req-%%d" %% status, (status, err.request_id)
        if status == 429:
            assert err.retry_after == 7, err.retry_after
    else:
        raise AssertionError("%%d was not raised" %% status)

try:
    sdk.grid.cell(grid_id, "header")
except sdk_package.APIError as err:
    assert (err.status_code, err.code, err.request_id) == (500, "code_500", "req-from-header"), vars(err)
else:
    raise AssertionError("500 was not raised")
server.shutdown()
`
