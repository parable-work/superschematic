package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
)

const streamService = "fixture-stream-api"

// TestABinaryStreamIsReadAsItArrives: an @httpBinaryStream operation (D167)
// is a method that returns the client's BinaryStream, which streamProbe
// reads from a local server that streams an Arrow IPC body in chunks with
// a header: the whole body, the header and the declared media type. A
// refusal raises the APIError of the problem, a success of another media
// type raises SDKError, and a close before the end ends the request.
func TestABinaryStreamIsReadAsItArrives(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Python run in -short mode")
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, streamService))
	if err != nil {
		t.Fatalf("load %s: %v", streamService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: streamService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := filepath.Join(t.TempDir(), "sdk")
	if err := WriteSDK(sdkOutput, dir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	var methods []string
	for _, ns := range sdkOutput.Namespaces {
		for _, endpoint := range ns.Endpoints {
			methods = append(methods, endpoint.MethodName+" -> "+endpoint.OutputTypeHint)
		}
	}
	if got := strings.Join(methods, ", "); got != "describe_report -> ReportSummary, export_report -> BinaryStream" {
		t.Fatalf("methods = %s", got)
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(streamProbe, dir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// streamProbe is formatted with the SDK directory, package and class.
const streamProbe = `
import json
import sys
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)

ARROW = "application/vnd.apache.arrow.stream"

# Shaped like an Arrow IPC stream: each message starts with the 0xFFFFFFFF
# continuation marker, which no JSON decoder reads, and the stream ends with
# the end marker.
body = b"".join(b"\xff\xff\xff\xff" + bytes([i, 0, 0, 0]) + bytes([i]) * 1024 for i in range(64)) + b"\xff\xff\xff\xff\x00\x00\x00\x00"

state = {"mode": "stream", "requests": [], "ended": threading.Event()}


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if self.path == "/api/reports/r-1":
            self.answer(200, "application/json", json.dumps({"data": {"reportId": "r-1", "rows": 64}, "meta": {"requestId": "req-0"}}).encode())
        else:
            self.answer(404, "text/plain", b"not found")

    def do_POST(self):
        sent = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        state["requests"].append({"path": self.path, "accept": self.headers["Accept"], "body": sent})
        mode = state["mode"]
        if mode == "refuse":
            problem = {"type": "about:blank", "title": "Bad Request", "status": 400, "detail": "the report has no such column", "code": "unknown_column"}
            self.answer(400, "application/problem+json", json.dumps(problem).encode())
            return
        if mode == "json":
            self.answer(200, "application/json", json.dumps({"data": {}, "meta": {"requestId": "req-2"}}).encode())
            return
        self.send_response(200)
        self.send_header("Content-Type", ARROW)
        self.send_header("X-Export-Id", "export-7")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        try:
            if mode == "endless":
                while True:
                    self.chunk(b"\xff" * 4096)
                    time.sleep(0.01)
            for start in range(0, len(body), 4096):
                self.chunk(body[start:start + 4096])
            self.wfile.write(b"0\r\n\r\n")
        except (BrokenPipeError, ConnectionResetError):
            state["ended"].set()
        self.close_connection = True

    def chunk(self, data):
        self.wfile.write(b"%%x\r\n%%s\r\n" %% (len(data), data))
        self.wfile.flush()

    def answer(self, status, content_type, data):
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))
export_input = {"columns": ["region", "total"]}

stream = sdk.reports.export_report("r-1", export_input, format="arrow")
assert isinstance(stream, sdk_package.BinaryStream), stream
with stream:
    read = b"".join(stream)
assert read == body, (len(read), len(body))
assert stream.content_type == ARROW and stream.status_code == 200, (stream.content_type, stream.status_code)
assert stream.headers["X-Export-Id"] == "export-7", stream.headers
sent = state["requests"][0]
assert sent["path"] == "/api/reports/r-1/export?format=arrow", sent
assert sent["accept"] == ARROW + ", application/problem+json", sent
assert sent["body"] == export_input, sent

# The JSON operation beside it still reads the envelope. Without the types
# package the SDK hands back the data as it decoded it.
summary = sdk.reports.describe_report("r-1")
assert summary == {"reportId": "r-1", "rows": 64}, summary

state["mode"] = "refuse"
try:
    sdk.reports.export_report("r-1", export_input)
except sdk_package.APIError as err:
    assert (err.status_code, err.code, err.message) == (400, "unknown_column", "the report has no such column"), vars(err)
else:
    raise AssertionError("400 was not raised")

state["mode"] = "json"
try:
    sdk.reports.export_report("r-1", export_input)
except sdk_package.SDKError as err:
    assert "expected " + ARROW + " in streamed success response" in str(err), err
else:
    raise AssertionError("a JSON success was read as the stream")

# A close before the end returns at once and ends the request: the
# handler's next write fails.
state["mode"] = "endless"
stream = sdk.reports.export_report("r-1", export_input)
assert len(stream.read(8192)) == 8192
started = time.monotonic()
stream.close()
assert time.monotonic() - started < 2, "close did not return"
assert state["ended"].wait(5), "the handler's request did not end after close"
server.shutdown()
`
