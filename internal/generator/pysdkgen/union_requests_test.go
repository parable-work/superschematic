package pysdkgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/pygen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
)

// TestUnionArgumentsPythonSDK writes the Python types package and SDK of
// union-lists-api and runs unionRequestsProbe, which calls ShapeMutations
// with the arguments of union_requests.json's requests
// (sdktest.UnionRequestsPath) against a local server that records what it
// is sent. pydantic reads an argument as the types do, so one no member
// reads is refused before sending; the requests say only those whose sdk
// is "sends" and whose status is 400 may be. Every other one is sent as
// the request's body, or refused before sending with the member's errors
// at the argument's paths, as its sdk says. The probe needs pydantic and
// skips without it.
func TestUnionArgumentsPythonSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	python, err := findCompatiblePython()
	if err != nil {
		t.Skipf("no compatible python: %v", err)
	}
	if err := exec.Command(python, "-c", "import pydantic").Run(); err != nil {
		t.Skip("pydantic not available; skipping the Python SDK probe")
	}
	schema, apiOutput := loadUnionModelsAPI(t)
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	typesDir := filepath.Join(root, "types")
	sdkDir := filepath.Join(root, "sdk")
	typesOutput, err := pygen.Generate(schema, pygen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("pygen.Generate: %v", err)
	}
	if err := pygen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("pygen.WriteTypes: %v", err)
	}
	sdkOutput := generateUnionModelsSDK(t, apiOutput)
	if err := WriteSDK(sdkOutput, sdkDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	outcomesPath := filepath.Join(t.TempDir(), "outcomes.json")
	probe := exec.Command(python, "-c", unionRequestsProbe,
		typesDir, sdkDir, sdkOutput.PackageName, sdkOutput.SDKClassName, requestsPath, outcomesPath)
	if out, err := probe.CombinedOutput(); err != nil {
		t.Fatalf("Python SDK probe failed: %v\n%s", err, out)
	}
	data, err := os.ReadFile(outcomesPath)
	if err != nil {
		t.Fatal(err)
	}
	var outcomes map[string]struct {
		Refused bool            `json:"refused"`
		Sent    json.RawMessage `json:"sent"`
		Errors  json.RawMessage `json:"errors"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(data, &outcomes); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		outcome, ok := outcomes[request.Name]
		if !ok {
			t.Errorf("%s: no outcome", request.Name)
			continue
		}
		if outcome.Error != "" {
			t.Errorf("%s: %s", request.Name, outcome.Error)
			continue
		}
		if request.SDKSends() {
			switch {
			case outcome.Refused && request.Status == 400:
				// pydantic refuses a value no member reads.
			case outcome.Refused:
				t.Errorf("%s: refused with %s, want it sent", request.Name, outcome.Errors)
			default:
				var got, want any
				if json.Unmarshal(outcome.Sent, &got) != nil || json.Unmarshal(request.Body, &want) != nil {
					t.Errorf("%s: sent %s, want %s", request.Name, outcome.Sent, request.Body)
					continue
				}
				// The SDK sends the arguments as its models hold them: a
				// default filled in, a key no field declares dropped, but
				// no value changed, so nothing is coerced.
				if !sentAsGiven(got, want) {
					t.Errorf("%s: sent %s, want %s", request.Name, outcome.Sent, request.Body)
				}
			}
			continue
		}
		want, err := request.SDKErrors()
		if err != nil {
			t.Fatal(err)
		}
		flat, err := sdktest.FlattenFieldErrors(outcome.Errors)
		if err != nil {
			t.Fatal(err)
		}
		// The Python SDK names an argument by its Python name (by_key);
		// the wire name is the requests'.
		got := sdktest.FieldErrors{}
		for path, rules := range flat {
			got[wireArgumentPath(path)] = rules
		}
		if !outcome.Refused || got.String() != want.String() {
			t.Errorf("%s: refused %v with %s, want %s (sent %s)", request.Name, outcome.Refused, got, want, outcome.Sent)
		}
	}
}

// wireArgumentPath writes the argument a Python SDK error path starts
// with, its snake_case Python name, by its wire name, in camelCase.
func wireArgumentPath(path string) string {
	end := strings.IndexAny(path, "[.")
	if end < 0 {
		end = len(path)
	}
	parts := strings.Split(path[:end], "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "") + path[end:]
}

// sentAsGiven reports whether sent holds the values given does: an object's
// keys both hold are sent as given, a key only one holds is a default the
// models filled in or a key they dropped, and lists and other values are
// equal.
func sentAsGiven(sent, given any) bool {
	switch s := sent.(type) {
	case map[string]any:
		g, ok := given.(map[string]any)
		if !ok {
			return false
		}
		for key, value := range s {
			if gv, both := g[key]; both && !sentAsGiven(value, gv) {
				return false
			}
		}
		return true
	case []any:
		g, ok := given.([]any)
		if !ok || len(g) != len(s) {
			return false
		}
		for i := range s {
			if !sentAsGiven(s[i], g[i]) {
				return false
			}
		}
		return true
	}
	// A number decodes as a float64 and text as a string: "1" is not 1.
	return reflect.DeepEqual(sent, given)
}

// unionRequestsProbe takes the types and SDK directories, the SDK package
// and class, union_requests.json and the outcomes file as arguments. It
// calls each request's operation with the body's arguments and records
// whether the SDK refused them, with the field errors of its
// ValidationError, or what it sent.
const unionRequestsProbe = `
import json
import sys
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

types_dir, sdk_dir, sdk_name, sdk_class, requests_path, outcomes_path = sys.argv[1:]
sys.path.insert(0, types_dir)
sys.path.insert(0, sdk_dir)
sdk_package = __import__(sdk_name)
ClientConfig = sdk_package.ClientConfig
ValidationError = sdk_package.ValidationError

sent = []
answer = [None]


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        sent.append(json.loads(self.rfile.read(length)))
        body = json.dumps({"data": answer[0], "meta": {"requestId": "req-1"}}).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


server = HTTPServer(("127.0.0.1", 0), Handler)
threading.Thread(target=server.serve_forever, daemon=True).start()
shape = getattr(sdk_package, sdk_class)(ClientConfig(base_url="http://127.0.0.1:%d" % server.server_address[1])).shape

with open(requests_path) as f:
    requests = json.load(f)["requests"]
outcomes = {}
for request in requests:
    sent.clear()
    answer[0] = request.get("data") if request["status"] == 200 else None
    body = request["body"]
    calls = {
        "drawShape": lambda: shape.draw_shape(body["shape"]),
        "pay": lambda: shape.pay(body["payment"]),
        "checkout": lambda: shape.checkout(body),
        "drawShapes": lambda: shape.draw_shapes(body["shapes"]),
        "shapeGrid": lambda: shape.shape_grid(body["rows"]),
        "shapesByKey": lambda: shape.shapes_by_key(body["byKey"]),
        "probe": lambda: shape.probe(body),
    }
    try:
        calls[request["operation"]]()
        outcomes[request["name"]] = {"sent": sent[0] if sent else None}
    except ValidationError as err:
        outcomes[request["name"]] = {"refused": True, "errors": err.errors}
    except Exception as err:
        outcomes[request["name"]] = {"error": f"{type(err).__name__}: {err}"}
server.shutdown()
with open(outcomes_path, "w") as f:
    json.dump(outcomes, f)
`
