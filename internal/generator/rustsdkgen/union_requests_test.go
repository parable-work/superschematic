package rustsdkgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestUnionArgumentsRustSDK generates the Rust types and SDK crates of
// union-lists-api and runs unionRequestsSDKTest in the SDK crate, which
// calls ShapeMutations with the arguments of union_requests.json's
// requests (sdktest.UnionRequestsPath), decoded into their Rust types,
// against a local server that records what it is sent. A request whose
// arguments the Rust types cannot hold (a value no member reads) is
// skipped; the requests say only those whose sdk is "sends" and whose
// status is 400 may be. Every other one is sent as the request's body, or
// refused before sending with the member's errors at the argument's paths,
// as its sdk says: the types crate's validate_<union> checks a union
// argument and an input's union field.
func TestUnionArgumentsRustSDK(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, err := sdktest.LoadUnionListsService()
	if err != nil {
		t.Fatal(err)
	}
	requests, err := sdktest.LoadUnionRequests()
	if err != nil {
		t.Fatal(err)
	}
	requestsPath, err := sdktest.UnionRequestsPath()
	if err != nil {
		t.Fatal(err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: sdktest.UnionListsService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", sdktest.UnionListsService)
	sdkDir := filepath.Join(root, "sdk", "rust", sdktest.UnionListsService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: sdktest.UnionListsService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	names := naming.Default()
	sdkOutput, err := Generate(apiOutput, names.RustSDKCrate(sdktest.UnionListsService), names.RustTypesCrate(sdktest.UnionListsService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	if !sdkOutput.ChecksUnions {
		t.Fatal("ChecksUnions = false, want the union arguments checked")
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, names))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "union_requests.rs"), strings.ReplaceAll(unionRequestsSDKTest, "SDK_CRATE", crate))
	outcomesPath := filepath.Join(t.TempDir(), "outcomes.json")
	t.Setenv("UNION_REQUESTS", requestsPath)
	t.Setenv("UNION_OUTCOMES", outcomesPath)
	cargoClippyAndTest(t, cargoPath, sdkDir)

	data, err := os.ReadFile(outcomesPath)
	if err != nil {
		t.Fatal(err)
	}
	var outcomes map[string]struct {
		Skipped  bool            `json:"skipped"`
		Sent     json.RawMessage `json:"sent"`
		Expected json.RawMessage `json:"expected"`
		Error    string          `json:"error"`
	}
	if err := json.Unmarshal(data, &outcomes); err != nil {
		t.Fatal(err)
	}
	for _, request := range requests {
		outcome, ok := outcomes[request.Name]
		switch {
		case !ok:
			t.Errorf("%s: no outcome", request.Name)
		case outcome.Skipped:
			if !request.SDKSends() || request.Status != 400 {
				t.Errorf("%s: the Rust types cannot hold its arguments", request.Name)
			}
		case request.SDKSends():
			// A typed SDK sends the arguments as its Rust types hold them.
			var got, want any
			if json.Unmarshal(outcome.Sent, &got) != nil || json.Unmarshal(outcome.Expected, &want) != nil {
				t.Errorf("%s: sent %s (%s), want %s", request.Name, outcome.Sent, outcome.Error, outcome.Expected)
				continue
			}
			gotJSON, _ := json.Marshal(got)
			wantJSON, _ := json.Marshal(want)
			if string(gotJSON) != string(wantJSON) {
				t.Errorf("%s: sent %s, want %s", request.Name, gotJSON, wantJSON)
			}
		default:
			want, err := request.SDKErrors()
			if err != nil {
				t.Fatal(err)
			}
			if got := refusalErrors(outcome.Error); got.String() != want.String() {
				t.Errorf("%s: refused with %q, want %s", request.Name, outcome.Error, want)
			}
		}
	}
}

// refusalErrors reads the field errors of the SDK's refusal,
// "input validation failed: path: rule; path: rule", as FieldErrors.
func refusalErrors(message string) sdktest.FieldErrors {
	errors := sdktest.FieldErrors{}
	failures, ok := strings.CutPrefix(message, "invalid sdk configuration: input validation failed: ")
	if !ok {
		return errors
	}
	for _, failure := range strings.Split(failures, "; ") {
		path, rule, _ := strings.Cut(failure, ": ")
		errors[path] = append(errors[path], rule)
	}
	return errors
}

// unionRequestsSDKTest is tests/union_requests.rs of the generated SDK
// crate. For each request of $UNION_REQUESTS it decodes the body into the
// operation's input, calls the operation against a server that answers
// the request's data and records, in $UNION_OUTCOMES by the request's
// name, whether it skipped the request, the body the server was sent and
// the error the call returned.
const unionRequestsSDKTest = `use std::collections::HashMap;
use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::namespaces::shape::{DrawShapeInput, DrawShapesInput, PayInput, ShapeGridInput, ShapesByKeyInput};
use SDK_CRATE::types::{Checkout, Payment, Probe, Shape};
use SDK_CRATE::{ClientConfig, UnionListsApiSdk};

/// Serves one request: it records the body it is sent and answers data.
fn serve(data: Value, sent: Arc<Mutex<Option<Value>>>) -> UnionListsApiSdk {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    thread::spawn(move || {
        for stream in listener.incoming() {
            let mut stream = stream.unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut line = String::new();
            reader.read_line(&mut line).unwrap();
            let mut length = 0;
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                let header = header.trim_end();
                if header.is_empty() {
                    break;
                }
                if let Some((name, value)) = header.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0; length];
            reader.read_exact(&mut body).unwrap();
            *sent.lock().unwrap() = serde_json::from_slice(&body).ok();
            let payload = json!({"data": data, "meta": {"requestId": "req-1"}}).to_string();
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                payload.len(),
                payload
            )
            .unwrap();
        }
    });
    UnionListsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

#[tokio::test]
async fn union_requests() {
    let file: Value = serde_json::from_str(
        &std::fs::read_to_string(std::env::var("UNION_REQUESTS").unwrap()).unwrap(),
    )
    .unwrap();
    let mut outcomes: HashMap<String, Value> = HashMap::new();
    for request in file["requests"].as_array().unwrap() {
        let name = request["name"].as_str().unwrap().to_string();
        let body = request["body"].clone();
        let data = if request["status"] == 200 { request["data"].clone() } else { Value::Null };
        let sent = Arc::new(Mutex::new(None));
        let sdk = serve(data, sent.clone());
        // Each call's arguments, as the SDK serializes them, beside its result.
        let result: Option<(Value, Result<(), String>)> = match request["operation"].as_str().unwrap() {
            "drawShape" => match serde_json::from_value::<Shape>(body["shape"].clone()) {
                Ok(shape) => {
                    let input = DrawShapeInput { shape };
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.draw_shape(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "pay" => match serde_json::from_value::<Payment>(body["payment"].clone()) {
                Ok(payment) => {
                    let input = PayInput { payment };
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.pay(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "checkout" => match serde_json::from_value::<Checkout>(body.clone()) {
                Ok(input) => {
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.checkout(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "probe" => match serde_json::from_value::<Probe>(body.clone()) {
                Ok(input) => {
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.probe(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "drawShapes" => match serde_json::from_value::<Vec<Shape>>(body["shapes"].clone()) {
                Ok(shapes) => {
                    let input = DrawShapesInput { shapes };
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.draw_shapes(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "shapeGrid" => match serde_json::from_value::<Vec<Vec<Shape>>>(body["rows"].clone()) {
                Ok(rows) => {
                    let input = ShapeGridInput { rows };
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.shape_grid(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            "shapesByKey" => match serde_json::from_value::<HashMap<String, Shape>>(body["byKey"].clone()) {
                Ok(by_key) => {
                    let input = ShapesByKeyInput { by_key };
                    let expected = serde_json::to_value(&input).unwrap();
                    Some((expected, sdk.shape.shapes_by_key(input, None).await.map(|_| ()).map_err(|e| e.to_string())))
                }
                Err(_) => None,
            },
            other => panic!("{name}: unknown operation {other}"),
        };
        let outcome = match result {
            None => json!({"skipped": true}),
            Some((expected, result)) => json!({
                "sent": sent.lock().unwrap().clone(),
                "expected": expected,
                "error": result.err(),
            }),
        };
        outcomes.insert(name, outcome);
    }
    std::fs::write(
        std::env::var("UNION_OUTCOMES").unwrap(),
        serde_json::to_vec(&outcomes).unwrap(),
    )
    .unwrap();
}
`
