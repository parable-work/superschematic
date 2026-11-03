package rustsdkgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
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
		ModulePath:   "example.com/schemas/api/" + workflowService,
		TypesModule:  "example.com/schemas/types/go/" + workflowService,
		Dependencies: map[string]*ir.Schema{"fixture-workflow-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, db, apiOutput
}

// writeWorkflowSDK writes the Rust SDK crate of fixture-workflow-api to
// sdkDir against the types crate in typesDir.
func writeWorkflowSDK(t *testing.T, apiOutput *apigen.APIOutput, sdkDir, typesDir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(workflowService), naming.Default().RustTypesCrate(workflowService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// TestBehaviorOperationsSDKGolden pins every file of the Rust SDK for
// fixture-workflow-api, its tool documents included: each behavior use's
// operations are methods of a struct of their own, a field of the
// namespace named for the use, order.status.transition, and the tool
// documents name the tool order.status.transition and its method
// status.transition. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestBehaviorOperationsSDKGolden -update
func TestBehaviorOperationsSDKGolden(t *testing.T) {
	_, _, apiOutput := loadWorkflowAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", workflowService)
	sdkOutput := writeWorkflowSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", workflowService))
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", workflowService))

	var got []string
	for _, ns := range sdkOutput.Namespaces {
		for _, ep := range ns.Endpoints {
			if strings.HasSuffix(ep.MethodName, "transition") {
				t.Errorf("%s holds the flat method %s", ns.StructName, ep.MethodName)
			}
		}
		for _, member := range ns.Members {
			for _, ep := range member.Endpoints {
				got = append(got, ns.FieldName+"."+member.FieldName+" "+member.StructName+"::"+ep.MethodName)
			}
		}
	}
	want := "order.status OrderStatusOperations::transition; shipment.payment_status ShipmentPaymentStatusOperations::transition; shipment.status ShipmentStatusOperations::transition"
	if strings.Join(got, "; ") != want {
		t.Errorf("members = %v, want %s", got, want)
	}

	tools, err := GenerateTools(sdkOutput, apiOutput, nestedArraysClock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name+" "+tool.MethodName)
	}
	if want := "order.getOrder get_order; order.status.transition status.transition; shipment.paymentStatus.transition payment_status.transition; shipment.status.transition status.transition"; strings.Join(names, "; ") != want {
		t.Errorf("tools = %v, want %s", names, want)
	}
}

// TestBehaviorOperationsSDKCrateBuildsAndCalls builds the Rust SDK crate of
// fixture-workflow-api over the types crates of the API and its database,
// with clippy's warnings denied, and runs a test in it against a local
// server: order.status.transition posts the input to the route under the
// key and returns the result, typed.
func TestBehaviorOperationsSDKCrateBuildsAndCalls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, db, apiOutput := loadWorkflowAPI(t)
	root := testpaths.TempDir(t)
	reg := registry.New(naming.Default())
	for _, tc := range []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{"fixture-workflow-db", db, nil},
		{workflowService, schema, map[string]*ir.Schema{"fixture-workflow-db": db}},
	} {
		crates := map[string]string{}
		for dep := range tc.deps {
			crates[dep] = naming.Default().RustTypesCrate(dep)
		}
		output, err := rustgen.Generate(tc.schema, rustgen.Options{
			SchemaName: tc.name, Dependencies: tc.deps, DependencyCrates: crates, Registry: reg, Clock: nestedArraysClock,
		})
		if err != nil {
			t.Fatalf("rustgen.Generate %s: %v", tc.name, err)
		}
		if err := rustgen.WriteTypes(output, filepath.Join(root, "types", "rust", tc.name)); err != nil {
			t.Fatalf("rustgen.WriteTypes %s: %v", tc.name, err)
		}
	}
	sdkDir := filepath.Join(root, "sdk", "rust", workflowService)
	sdkOutput := writeWorkflowSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", workflowService))
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "behavior_operations.rs"), strings.ReplaceAll(behaviorOperationsSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// behaviorOperationsSDKTest is tests/behavior_operations.rs of the
// generated SDK crate, with SDK_CRATE replaced by the crate's module name.
const behaviorOperationsSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureWorkflowApiSdk};

const ORDER_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

/// Answers one request per canned value with the success envelope around
/// it, and sends each request's method, path and JSON body back.
fn serve(responses: Vec<Value>) -> (String, mpsc::Receiver<(String, String, Value)>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for data in responses {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let mut parts = request_line.split_whitespace();
            let method = parts.next().unwrap_or_default().to_string();
            let path = parts.next().unwrap_or_default().to_string();
            let mut length = 0usize;
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
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            let body = if body.is_empty() { Value::Null } else { serde_json::from_slice(&body).unwrap() };
            sender.send((method, path, body)).unwrap();
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
    (base_url, receiver)
}

#[tokio::test]
async fn a_transition_is_a_method_of_its_field() {
    let (base_url, requests) = serve(vec![
        json!({"from": "draft", "to": "paid"}),
        json!({"from": "paid", "to": "refunded"}),
    ]);
    let sdk = FixtureWorkflowApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let id: types::IdentityUUID = ORDER_ID.parse().unwrap();

    let moved = sdk
        .order
        .status
        .transition(id, types::OrderStatusTransitionInput { to: types::OrderStatusState::Paid }, None)
        .await
        .unwrap();
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!(method, "POST");
    assert_eq!(path, format!("/api/order/{}/status/transition", id));
    assert_eq!(body, json!({"to": "paid"}));
    assert_eq!(moved.from, types::OrderStatusState::Draft);
    assert_eq!(moved.to, types::OrderStatusState::Paid);

    let refunded = sdk
        .shipment
        .payment_status
        .transition(id, types::ShipmentPaymentStatusTransitionInput { to: types::ShipmentPaymentStatusState::Refunded }, None)
        .await
        .unwrap();
    let (_, path, _) = requests.recv().unwrap();
    assert_eq!(path, format!("/api/shipment/{}/payment-status/transition", id));
    assert_eq!(refunded.to, types::ShipmentPaymentStatusState::Refunded);
}
`
