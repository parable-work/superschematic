package rustsdkgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestNestedArraysSDKCrateBuildsAndRuns generates the Rust types crate and
// the Rust SDK crate of fixture-nested-arrays-api, with grid.paint added,
// into a temp tree laid out as a build writes it, and runs cargo test on
// the SDK crate with nestedArraysSDKTest: Vec<Vec<T>> arguments and
// responses cross a local HTTP server as nested JSON arrays, and an input
// type with lists of lists passes the SDK's schema validation. The types
// crate resolves superscalar from the checkout scripts/superscalar-dep.sh
// stands up. CARGO_TARGET_DIR is honored when set.
func TestNestedArraysSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadNestedArraysAPI(t, true)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", nestedArraysService)
	sdkDir := filepath.Join(root, "sdk", "rust", nestedArraysService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: nestedArraysService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput := writeNestedArraysSDK(t, apiOutput, sdkDir, typesDir)
	if sdkOutput.TypesCrate != typesOutput.CrateName {
		t.Fatalf("SDK types crate %q, types crate %q", sdkOutput.TypesCrate, typesOutput.CrateName)
	}

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

[patch.crates-io]
superscalar = { path = "`+filepath.ToSlash(paths.ScalarRust)+`" }
`)
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "nested_arrays.rs"), strings.ReplaceAll(nestedArraysSDKTest, "SDK_CRATE", crate))

	cmd := exec.Command(cargoPath, "test", "--quiet")
	cmd.Dir = sdkDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+cargoTargetDir(t))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test on the generated SDK crate: %v\n%s", err, out)
	}
}

// cargoTargetDir is CARGO_TARGET_DIR when set, so repeated local runs reuse
// one build, and a temp directory otherwise.
func cargoTargetDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("CARGO_TARGET_DIR"); dir != "" {
		return dir
	}
	return filepath.Join(t.TempDir(), "target")
}

func appendToFile(t *testing.T, path, text string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nestedArraysSDKTest is tests/nested_arrays.rs of the generated SDK crate,
// with SDK_CRATE replaced by the crate's module name.
const nestedArraysSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use serde_json::{json, Value};
use SDK_CRATE::namespaces::grid::{GridLabelsQueryParams, PaintInput, ReplaceLabelsInput};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureNestedArraysApiSdk};

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

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

fn strings(rows: &[&[&str]]) -> Vec<Vec<String>> {
    rows.iter().map(|row| row.iter().map(|cell| cell.to_string()).collect()).collect()
}

#[tokio::test]
async fn lists_of_lists_cross_the_wire() {
    let view = json!({"id": GRID_ID, "labels": [["a", "b"], []], "shades": [["light"]], "polygons": [[{"x": 1.0, "y": 2.0}], []]});
    let (base_url, requests) = serve(vec![
        view.clone(),
        json!([["a", "b"], [], ["c"]]),
        json!([[{"x": 1.0, "y": 2.0}], []]),
        view,
    ]);
    let sdk = FixtureNestedArraysApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();

    let stored = sdk
        .grid
        .replace_labels(id.clone(), ReplaceLabelsInput { labels: strings(&[&["a", "b"], &[]]) }, None)
        .await
        .unwrap();
    let (method, path, body) = requests.recv().unwrap();
    assert_eq!(method, "PUT");
    assert_eq!(path, format!("/api/grids/{}/labels", id));
    assert_eq!(body, json!({"labels": [["a", "b"], []]}));
    assert_eq!(stored.labels, strings(&[&["a", "b"], &[]]));
    assert_eq!(stored.polygons, vec![vec![types::Point { x: 1.0, y: 2.0 }], vec![]]);

    let labels = sdk
        .grid
        .grid_labels(id.clone(), Some(&GridLabelsQueryParams { limit: Some(2.0) }), None)
        .await
        .unwrap();
    let (method, path, _) = requests.recv().unwrap();
    assert_eq!(method, "GET");
    assert_eq!(path, format!("/api/grids/{}/labels?limit=2", id));
    assert_eq!(labels, strings(&[&["a", "b"], &[], &["c"]]));

    let polygons = sdk
        .grid
        .paint(
            id.clone(),
            PaintInput {
                shades: vec![vec![types::Shade::Dark], vec![]],
                polygons: Some(vec![vec![types::Point { x: 3.0, y: 4.0 }]]),
            },
            None,
        )
        .await
        .unwrap();
    let (_, _, body) = requests.recv().unwrap();
    assert_eq!(body, json!({"shades": [["dark"], []], "polygons": [[{"x": 3.0, "y": 4.0}]]}));
    assert_eq!(polygons, vec![vec![types::Point { x: 1.0, y: 2.0 }], vec![]]);

    // SaveGridInput passes the SDK's schema validation, whose items nest.
    sdk.grid
        .save_grid(
            types::SaveGridInput {
                labels: strings(&[&["a"], &[]]),
                shades: vec![vec![types::Shade::Light]],
                polygons: vec![vec![], vec![types::Point { x: 5.0, y: 6.0 }]],
                weights: Some(vec![vec![0.5], vec![]]),
            },
            None,
        )
        .await
        .unwrap();
    let (_, _, body) = requests.recv().unwrap();
    assert_eq!(
        body,
        json!({"labels": [["a"], []], "shades": [["light"]], "polygons": [[], [{"x": 5.0, "y": 6.0}]], "weights": [[0.5], []]})
    );
}

#[test]
fn a_null_inner_list_does_not_decode() {
    assert!(serde_json::from_value::<Vec<Vec<String>>>(json!([["a"], null])).is_err());
}
`
