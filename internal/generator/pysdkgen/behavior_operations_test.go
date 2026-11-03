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
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowService is the API whose @behaviorOperations sets the loader
// fills from fixture-workflow-db's tables (D103).
const workflowService = "fixture-workflow-api"

// loadWorkflowAPI loads fixture-workflow-api and its database, and
// generates its API output.
func loadWorkflowAPI(t *testing.T) (*ir.Schema, *ir.Schema, *apigen.APIOutput) {
	t.Helper()
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(workflowService), load("fixture-workflow-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   workflowService,
		Dependencies: map[string]*ir.Schema{"fixture-workflow-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

func writeWorkflowSDK(t *testing.T, apiOutput *apigen.APIOutput, dir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, dir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	return sdkOutput
}

// TestBehaviorOperationsSDKGolden pins every file of the Python SDK for
// fixture-workflow-api: each behavior use's operations are methods of a
// class of their own, a property of the namespace named for the use,
// sdk.order.status.transition, and the namespace has no flat method for
// them. Regenerate with:
// go test ./internal/generator/pysdkgen -run TestBehaviorOperationsSDKGolden -update
func TestBehaviorOperationsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadWorkflowAPI(t)
	outDir := t.TempDir()
	sdkOutput := writeWorkflowSDK(t, apiOutput, outDir)
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", workflowService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			if strings.HasSuffix(ep.MethodName, "transition") {
				t.Errorf("%s holds the flat method %s", ns.ClassName, ep.MethodName)
			}
		}
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.AccessorName+"."+member.FieldName+" "+member.ClassName+"."+ep.MethodName)
			}
		}
	}
	want := "order.status OrderStatusOperations.transition; shipment.payment_status ShipmentPaymentStatusOperations.transition; shipment.status ShipmentStatusOperations.transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}
}

// TestBehaviorOperationsSDKImportsAndRuns writes the Python types packages
// of fixture-workflow-api and its database and the API's Python SDK,
// byte-compiles them, and runs behaviorOperationsProbe against a local
// HTTP server: sdk.order.status.transition posts the input to the route
// under the key and answers the result as its model, and an unknown state
// fails validation before any request. The probe needs pydantic and skips
// without it.
func TestBehaviorOperationsSDKImportsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	schema, db, apiOutput := loadWorkflowAPI(t)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	reg := registry.New(naming.Default())
	var apiTypes *pygen.ModuleOutput
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-workflow-db", db, nil},
		{workflowService, schema, map[string]*ir.Schema{"fixture-workflow-db": db}},
	} {
		output, err := pygen.Generate(tc.schema, pygen.Options{SchemaName: tc.name, Dependencies: tc.deps, Registry: reg, Clock: nestedArraysClock})
		if err != nil {
			t.Fatalf("pygen.Generate %s: %v", tc.name, err)
		}
		if err := pygen.WriteTypes(output, filepath.Join(typesDir, tc.name)); err != nil {
			t.Fatalf("pygen.WriteTypes %s: %v", tc.name, err)
		}
		apiTypes = output
	}
	sdkDir := filepath.Join(root, "sdk")
	sdkOutput := writeWorkflowSDK(t, apiOutput, sdkDir)
	if sdkOutput.TypesPackage != apiTypes.PythonModuleName {
		t.Fatalf("SDK types package %q, types module %q", sdkOutput.TypesPackage, apiTypes.PythonModuleName)
	}

	compile := exec.Command(python, "-m", "compileall", "-q", root)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	paths := []string{sdkDir, filepath.Join(typesDir, workflowService), filepath.Join(typesDir, "fixture-workflow-db")}
	probe := exec.Command(python, "-c", fmt.Sprintf(behaviorOperationsProbe, sdkOutput.PackageName, sdkOutput.SDKClassName, apiTypes.PythonModuleName))
	probe.Env = append(os.Environ(), "PYTHONPATH="+strings.Join(paths, string(os.PathListSeparator)))
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
}

// behaviorOperationsProbe is formatted with the SDK package and class and
// the API's types package, all on PYTHONPATH.
const behaviorOperationsProbe = `
import json
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

sdk_package = __import__(%[1]q)
types_package = __import__(%[3]q)
SDK = getattr(sdk_package, %[2]q)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError

order_id = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40"
answers = {
    "/api/order/" + order_id + "/status/transition": {"from": "draft", "to": "paid"},
    "/api/shipment/" + order_id + "/payment-status/transition": {"from": "paid", "to": "refunded"},
}
calls = []
tokens = []


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        calls.append((self.command, self.path, json.loads(raw) if raw else None))
        tokens.append(self.headers.get("Authorization"))
        body = json.dumps({"data": answers[self.path], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
# Every route needs a caller (@requirePermission on OrderActions, @auth on
# ShipmentActions), so the client sends the token it is given.
sdk = SDK(ClientConfig(base_url=f"http://127.0.0.1:{server.server_address[1]}", auth_token="kim"))

try:
    sdk.order.status.transition(order_id, {"to": "lost"})
except ValidationError as err:
    assert sorted(err.errors) == ["to"], err.errors
else:
    raise AssertionError("an unknown state was sent")
assert calls == [], calls

moved = sdk.order.status.transition(order_id, types_package.OrderStatusTransitionInput(to=types_package.OrderStatusState.Paid))
assert calls[-1] == ("POST", "/api/order/" + order_id + "/status/transition", {"to": "paid"}), calls[-1]
assert type(moved) is types_package.OrderStatusTransitionResult, moved
assert moved.from_ == types_package.OrderStatusState.Draft and moved.to == types_package.OrderStatusState.Paid, moved

refunded = sdk.shipment.payment_status.transition(order_id, {"to": "refunded"})
assert calls[-1][1] == "/api/shipment/" + order_id + "/payment-status/transition", calls[-1]
assert refunded.to == types_package.ShipmentPaymentStatusState.Refunded, refunded
assert tokens == ["Bearer kim", "Bearer kim"], tokens
`
