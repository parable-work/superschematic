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
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestOptionalJSONSDKCrateSendsNullApartFromAbsent generates the Rust types
// crate and the Rust SDK crate of optional-json-api
// (sdktest.LoadOptionalJSONService) and runs optionalJSONSDKTest on them:
// an optional Generic.JSON body argument or input type field that is
// Some(Value::Null) is sent as null, one that is None is not sent, and a
// response's null reads back as Some(Value::Null). The body arguments of a
// DELETE are sent in the body, as those of a PUT are.
func TestOptionalJSONSDKCrateSendsNullApartFromAbsent(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	service := sdktest.OptionalJSONService
	schema, err := sdktest.LoadOptionalJSONService()
	if err != nil {
		t.Fatalf("load %s: %v", service, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: service, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", service)
	sdkDir := filepath.Join(root, "sdk", "rust", service)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: service, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(service), naming.Default().RustTypesCrate(service), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

[patch.crates-io]
superscalar = { path = "`+filepath.ToSlash(paths.ScalarRust)+`" }
`)
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "optional_json.rs"), strings.ReplaceAll(optionalJSONSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// optionalJSONSDKTest is tests/optional_json.rs of the generated SDK crate,
// with SDK_CRATE replaced by the crate's module name.
const optionalJSONSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::namespaces::note::{AnnotateInput, RetractInput};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, OptionalJsonApiSdk};

/// Answers each request with the success envelope around true, and sends
/// each request's JSON body back.
fn serve(requests: usize) -> (String, mpsc::Receiver<Value>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for _ in 0..requests {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut length = 0usize;
            loop {
                let mut line = String::new();
                reader.read_line(&mut line).unwrap();
                let line = line.trim_end();
                if line.is_empty() {
                    break;
                }
                if let Some((name, value)) = line.split_once(':') {
                    if name.eq_ignore_ascii_case("content-length") {
                        length = value.trim().parse().unwrap();
                    }
                }
            }
            let mut body = vec![0u8; length];
            reader.read_exact(&mut body).unwrap();
            sender.send(serde_json::from_slice(&body).unwrap()).unwrap();
            let payload = json!({"data": true, "meta": {"requestId": "req-1"}}).to_string();
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
async fn an_optional_json_value_is_sent_as_null_or_left_out() {
    let (base_url, bodies) = serve(6);
    let sdk = OptionalJsonApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();

    sdk.note.annotate("n1".to_string(), AnnotateInput { body: json!({"a": 1}), extra: None }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": {"a": 1}}));
    sdk.note.annotate("n1".to_string(), AnnotateInput { body: json!({"a": 1}), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": {"a": 1}, "extra": null}));

    sdk.note.retract("n1".to_string(), RetractInput { body: json!({"a": 1}), extra: None }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": {"a": 1}}));
    sdk.note.retract("n1".to_string(), RetractInput { body: json!({"a": 1}), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": {"a": 1}, "extra": null}));

    sdk.note.revise(types::NoteRevision { body: json!("text"), extra: None }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": "text"}));
    sdk.note.revise(types::NoteRevision { body: json!("text"), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(bodies.recv().unwrap(), json!({"body": "text", "extra": null}));
}

#[test]
fn an_optional_json_field_reads_null_apart_from_absent() {
    let absent: types::NoteRevision = serde_json::from_value(json!({"body": 1})).unwrap();
    assert_eq!(absent.extra, None);
    let nulled: types::NoteRevision = serde_json::from_value(json!({"body": 1, "extra": null})).unwrap();
    assert_eq!(nulled.extra, Some(Value::Null));
}
`
