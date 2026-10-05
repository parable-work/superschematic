package pysdkgen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestServiceCredentialSDKRuns writes the Python SDK of
// fixture-nested-arrays-api, with grid.cell added, and runs
// serviceCredentialProbe against a local HTTP server (D37): every request
// carries the service credential in each configured header; a 401 with the
// code service_unauthorized, in an RFC 9457 problem or the legacy envelope,
// asks the source for a fresh token once and never runs the end-user
// refresh; any other 401 refreshes the end user and never asks for a fresh
// service token; a call spends at most one of each. The Python SDK has no
// forward option, since no server is written in Python.
func TestServiceCredentialSDKRuns(t *testing.T) {
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
	probe := exec.Command(python, "-B", "-c", fmt.Sprintf(serviceCredentialProbe, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// serviceCredentialProbe is formatted with the SDK directory, package and
// class.
const serviceCredentialProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)
ClientConfig = sdk_package.ClientConfig
ServiceCredential = sdk_package.ServiceCredential
AuthenticationError = sdk_package.AuthenticationError

grid_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
OK = (200, {"data": "a", "meta": {"requestId": "req-1"}})
SERVICE_REFUSAL = (401, {"title": "Unauthorized", "status": 401, "detail": "Invalid service credential", "code": "service_unauthorized"})
USER_REFUSAL = (401, {"title": "Unauthorized", "status": 401, "detail": "Authentication required", "code": "unauthorized"})
LEGACY_SERVICE_REFUSAL = (401, {"error": {"code": "service_unauthorized", "message": "Invalid service credential"}})

replies = []
requests = []


class Handler(BaseHTTPRequestHandler):
    # Answers each request with the next reply and records its headers,
    # names lowercased.
    def do_GET(self):
        requests.append({name.lower(): value for name, value in self.headers.items()})
        status, payload = replies.pop(0)
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
base_url = "http://127.0.0.1:%%d" %% server.server_address[1]


class Fixture:
    def __init__(self, answers, headers=("Service-Authorization",), service=True):
        replies[:] = answers
        requests.clear()
        self.fresh = []
        self.refreshes = 0
        credential = ServiceCredential(token=self.token, headers=headers) if service else None
        self.sdk = SDK(ClientConfig(
            base_url=base_url,
            auth_token="alice",
            auth_token_refresher=self.refresh,
            service_credential=credential,
        ))

    def token(self, fresh):
        self.fresh.append(fresh)
        return "service-%%d" %% len(self.fresh)

    def refresh(self):
        self.refreshes += 1
        return "user-refreshed"

    def call(self):
        return self.sdk.grid.cell(grid_id, "a")

    def refused(self):
        try:
            self.call()
        except AuthenticationError as err:
            assert err.status_code == 401, err.status_code
            return err
        raise AssertionError("expected an AuthenticationError")


def header(name):
    return [request.get(name.lower()) for request in requests]


# Every request carries the service credential in Service-Authorization.
fixture = Fixture([OK, OK])
fixture.call()
fixture.call()
assert header("Service-Authorization") == ["Bearer service-1", "Bearer service-2"], requests
assert header("Authorization") == ["Bearer alice", "Bearer alice"], requests
assert fixture.fresh == [False, False], fixture.fresh

# Each configured header carries it.
fixture = Fixture([OK], headers=["Service-Authorization", "X-Serverless-Authorization"])
fixture.call()
assert header("Service-Authorization") == ["Bearer service-1"], requests
assert header("X-Serverless-Authorization") == ["Bearer service-1"], requests

# A service refusal asks for a fresh token once and does not refresh the user.
for refusal in (SERVICE_REFUSAL, LEGACY_SERVICE_REFUSAL):
    fixture = Fixture([refusal, OK])
    assert fixture.call() == "a"
    assert fixture.fresh == [False, True], (refusal, fixture.fresh)
    assert fixture.refreshes == 0, refusal
    assert header("Service-Authorization") == ["Bearer service-1", "Bearer service-2"], requests

# A second service refusal is the caller's error.
fixture = Fixture([SERVICE_REFUSAL, SERVICE_REFUSAL])
err = fixture.refused()
assert err.payload["code"] == "service_unauthorized", err.payload
assert fixture.fresh == [False, True], fixture.fresh
assert fixture.refreshes == 0
assert len(requests) == 2, requests

# An end-user refusal refreshes the user and never asks for a fresh service token.
for refusal in (USER_REFUSAL, (401, {"title": "Unauthorized"})):
    fixture = Fixture([refusal, OK])
    assert fixture.call() == "a"
    assert fixture.refreshes == 1, refusal
    assert fixture.fresh == [False, False], (refusal, fixture.fresh)
    assert header("Authorization") == ["Bearer alice", "Bearer user-refreshed"], requests

# One call retries the service credential once and refreshes the user once.
fixture = Fixture([SERVICE_REFUSAL, USER_REFUSAL, SERVICE_REFUSAL])
fixture.refused()
assert fixture.fresh == [False, True, False], fixture.fresh
assert fixture.refreshes == 1
assert len(requests) == 3, requests

# Without a service credential, a service refusal does not refresh the user.
fixture = Fixture([SERVICE_REFUSAL], service=False)
fixture.refused()
assert fixture.refreshes == 0
assert len(requests) == 1, requests
assert header("Service-Authorization") == [None], requests

server.shutdown()
`
