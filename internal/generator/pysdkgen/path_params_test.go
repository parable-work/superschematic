package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestPathParamsSDKImportsAndRuns writes the Python SDK of
// fixture-nested-arrays-api, with grid.cell added, byte-compiles it and
// runs pathParamsProbe against a local HTTP server: each path value, one
// with %, /, ?, # or non-ASCII text among them, is sent as one path
// segment, percent-encoded once as urllib.parse.quote(value, safe="")
// writes it, and a server that decodes it once receives the value passed.
// The probe needs no types package: grid.cell takes and returns strings.
func TestPathParamsSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	_, apiOutput := loadNestedArraysAPI(t, sdktest.AddCellOperation)

	sdkDir := filepath.Join(t.TempDir(), "sdk")
	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir)
	if !sdkOutput.HasPathParams {
		t.Fatal("HasPathParams = false with grid.cell's path parameters")
	}
	compile := exec.Command(python, "-m", "compileall", "-q", sdkDir)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated SDK does not byte-compile: %v\n%s", err, out)
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(pathParamsProbe, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// pathParamsProbe is formatted with the SDK directory, package and class.
const pathParamsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer
from urllib.parse import unquote

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)
ClientConfig = sdk_package.ClientConfig

grid_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
paths = []


class Handler(BaseHTTPRequestHandler):
    # Answers with the label segment of the path decoded once, as every
    # server decodes it.
    def do_GET(self):
        paths.append(self.path)
        segments = self.path.split("/")
        label = unquote(segments[5], errors="strict") if len(segments) == 6 else None
        body = json.dumps({"data": label, "meta": {"requestId": "req-1"}}).encode()
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

# Each label and the path segment urllib.parse.quote(label, safe="") writes.
labels = [
    ("%%", "%%25"),
    ("a%%25b", "a%%2525b"),
    ("100%%", "100%%25"),
    ("x%%41y", "x%%2541y"),
    ("a/b", "a%%2Fb"),
    ("a b", "a%%20b"),
    ("café", "caf%%C3%%A9"),
    ("a?b", "a%%3Fb"),
    ("a#b", "a%%23b"),
    ("a+b", "a%%2Bb"),
]
for label, segment in labels:
    received = sdk.grid.cell(grid_id, label)
    assert paths[-1] == "/api/grids/%%s/cells/%%s" %% (grid_id, segment), (label, paths[-1])
    assert received == label, (label, received)
assert len(paths) == len(labels), paths
server.shutdown()
`
