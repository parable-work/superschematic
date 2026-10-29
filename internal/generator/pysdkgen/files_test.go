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

const filesService = "fixture-files-api"

func filesSDK(t *testing.T, dir string) *SDKOutput {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, filesService))
	if err != nil {
		t.Fatalf("load %s: %v", filesService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: filesService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, dir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	return sdkOutput
}

// TestAFileDownloadIsADownloadedFile: a FileDownload result (D81) is a
// method that returns the client's DownloadedFile, which filesProbe reads
// from a local server: the bytes, the filename Content-Disposition offers
// (filename* before filename) and the media type, and a refusal raises as
// any call's does.
func TestAFileDownloadIsADownloadedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Python run in -short mode")
	}
	dir := filepath.Join(t.TempDir(), "sdk")
	sdkOutput := filesSDK(t, dir)
	var methods []string
	for _, ns := range sdkOutput.Namespaces {
		for _, endpoint := range ns.Endpoints {
			if endpoint.FileDownload {
				methods = append(methods, endpoint.MethodName+" -> "+endpoint.OutputTypeHint)
			}
		}
	}
	if got := strings.Join(methods, ", "); got != "export_statement -> DownloadedFile, original_statement -> DownloadedFile" {
		t.Fatalf("download methods = %s", got)
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(filesProbe, dir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// filesProbe is formatted with the SDK directory, package and class.
const filesProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)

account = "1VgEh72lXvTXkH"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/api/statements/%%s/export" %% account:
            body = b"date,amount\n2026-03-01,-12.50\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/csv; charset=utf-8")
            self.send_header("Content-Disposition", "attachment; filename=\"M_rz.csv\"; filename*=UTF-8''M%%C3%%A4rz.csv")
        elif self.path == "/api/statements/%%s/original" %% account:
            body = bytes([0, 1, 2, 255])
            self.send_response(200)
            self.send_header("Content-Type", "application/octet-stream")
            self.send_header("Content-Disposition", "attachment")
        else:
            body = json.dumps({"type": "about:blank", "title": "Not Found", "status": 404, "detail": "No statement", "code": "not_found"}).encode()
            self.send_response(404)
            self.send_header("Content-Type", "application/problem+json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(sdk_package.ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))

exported = sdk.statements.export_statement(account)
assert isinstance(exported, sdk_package.DownloadedFile), exported
assert exported.content == b"date,amount\n2026-03-01,-12.50\n", exported.content
assert exported.filename == "M\u00e4rz.csv", exported.filename
assert exported.content_type == "text/csv; charset=utf-8", exported.content_type

original = sdk.statements.original_statement(account)
assert original.content == bytes([0, 1, 2, 255]) and original.filename is None, original

try:
    sdk.statements.export_statement("1VgEh72lXvTXkJ")
except sdk_package.APIError as err:
    assert (err.status_code, err.code) == (404, "not_found"), vars(err)
else:
    raise AssertionError("404 was not raised")
server.shutdown()
`
