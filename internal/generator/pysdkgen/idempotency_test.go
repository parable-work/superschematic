package pysdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestIdempotentOperationsSendOneKeyACall writes the Python SDK of
// fixture-idempotent-api and runs idempotencyProbe against a local server
// (D140): a method of an @idempotent operation takes idempotency_key and
// sends it as Idempotency-Key, or a UUID it makes for each call, the same
// on each send of the call; a method of any other operation takes none and
// sends none.
func TestIdempotentOperationsSendOneKeyACall(t *testing.T) {
	schema, table, err := sdktest.LoadIdempotentService()
	if err != nil {
		t.Fatalf("load %s: %v", sdktest.IdempotentService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:         sessionauth.Provider{},
		SchemaName:       sdktest.IdempotentService,
		Clock:            nestedArraysClock,
		IdempotencyTable: table,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	sdkDir := filepath.Join(t.TempDir(), "sdk")
	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir)

	raw, err := os.ReadFile(filepath.Join(sdkDir, sdkOutput.PackageName, "namespaces", "orders.py"))
	if err != nil {
		t.Fatal(err)
	}
	orders := string(raw)
	for _, want := range []string{
		"\nimport uuid\n",
		"def place_order(self, input_data: PlaceOrderInput, idempotency_key: str | None = None, timeout_seconds",
		"idempotency_key: str | None = None, timeout_seconds: float | None = None, extra_headers: Mapping[str, str] | None = None) -> Order:\n        # One Idempotency-Key for the call (D140)",
		"def list_orders(self, timeout_seconds: float | None = None,",
	} {
		if !strings.Contains(orders, want) {
			t.Errorf("orders.py lacks %q:\n%s", want, orders)
		}
	}
	if got := strings.Count(orders, "idempotency_key: str | None = None"); got != 2 {
		t.Errorf("%d methods take idempotency_key, want place_order and cancel_order:\n%s", got, orders)
	}

	if testing.Short() {
		t.Skip("skipping the Python run in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	probe := exec.Command(python, "-c", fmt.Sprintf(idempotencyProbe, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// idempotencyProbe is formatted with the SDK directory, package and class.
// Its server records each request's method and Idempotency-Key, and
// answers the first send of an order of "busy" 429, so the client sends it
// again.
const idempotencyProbe = `
import json
import re
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sys.path.insert(0, %[1]q)
sdk_package = __import__(%[2]q)
SDK = getattr(sdk_package, %[3]q)
ClientConfig = sdk_package.ClientConfig

seen = []
busy = []
order = {"number": 1, "sku": "tea", "quantity": 2, "cancelled": False}


class Handler(BaseHTTPRequestHandler):
    def answer(self):
        seen.append((self.command, self.headers.get_all("Idempotency-Key") or []))
        length = int(self.headers.get("Content-Length") or 0)
        sent = self.rfile.read(length).decode() if length else ""
        status, body, retry = 200, {"data": [] if self.command == "GET" else order, "meta": {"requestId": "req-1"}}, None
        if "busy" in sent and not busy:
            busy.append(True)
            status, body, retry = 429, {"status": 429, "detail": "slow down"}, "1"
        raw = json.dumps(body).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        if retry:
            self.send_header("Retry-After", retry)
        self.end_headers()
        self.wfile.write(raw)

    do_GET = do_POST = do_DELETE = answer

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
sdk = SDK(ClientConfig(base_url="http://127.0.0.1:%%d" %% server.server_address[1]))
tea = {"sku": "tea", "quantity": 2}

# Each call makes its own UUID.
sdk.orders.place_order(tea)
sdk.orders.place_order(tea)
# The key a call names, on a POST and on a DELETE.
sdk.orders.place_order(tea, idempotency_key="order-7")
sdk.orders.cancel_order(1, idempotency_key="order-7")
# An operation that is not @idempotent takes none and sends none.
sdk.orders.list_orders()
try:
    sdk.orders.list_orders(idempotency_key="order-7")
except TypeError:
    pass
else:
    raise AssertionError("list_orders took an idempotency_key")
# The send after a 429 carries the key of the first.
sdk.orders.place_order({"sku": "busy", "quantity": 1})
# The key a call names replaces one among its extra headers.
sdk.orders.place_order(tea, idempotency_key="order-8", extra_headers={"Idempotency-Key": "other", "X-Trace": "t"})
server.shutdown()

uuid4 = re.compile(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
assert len(seen) == 8, seen
assert [len(keys) for _, keys in seen] == [1, 1, 1, 1, 0, 1, 1, 1], seen
first, second = seen[0][1][0], seen[1][1][0]
assert uuid4.match(first) and uuid4.match(second) and first != second, (first, second)
assert seen[2] == ("POST", ["order-7"]) and seen[3] == ("DELETE", ["order-7"]), seen
assert seen[4] == ("GET", []), seen
assert uuid4.match(seen[5][1][0]) and seen[5][1] == seen[6][1] and seen[5][1][0] != first, seen
assert seen[7] == ("POST", ["order-8"]), seen
`
