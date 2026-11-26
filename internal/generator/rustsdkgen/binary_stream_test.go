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
	"github.com/parable-work/superschematic/internal/testpaths"
)

const streamService = "fixture-stream-api"

// TestBinaryStreamSDKCrateBuildsAndRuns writes the Rust types crate and SDK
// crate of fixture-stream-api, then runs binaryStreamSDKTest with cargo:
// exportReport, an @httpBinaryStream operation (D167), against a local
// server that streams an Arrow IPC body in chunks with a header. The method
// returns the stream unread, a refusal is SDKError::Api, a success of
// another media type SDKError::Contract, and a drop before the end ends the
// request.
func TestBinaryStreamSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, streamService))
	if err != nil {
		t.Fatalf("load %s: %v", streamService, err)
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{Provider: sessionauth.Provider{}, SchemaName: streamService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", streamService)
	sdkDir := filepath.Join(root, "sdk", "rust", streamService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: streamService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(streamService), naming.Default().RustTypesCrate(streamService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt", "time"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	writeFile(t, filepath.Join(sdkDir, "tests", "binary_stream.rs"), strings.ReplaceAll(binaryStreamSDKTest, "SDK_CRATE", crate))
	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// binaryStreamSDKTest is tests/binary_stream.rs of the generated SDK crate
// of fixture-stream-api, with SDK_CRATE replaced by the crate's module name.
const binaryStreamSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};
use std::sync::mpsc;
use std::thread;
use std::time::Duration;

use serde_json::Value;
use SDK_CRATE::namespaces::reports::ExportReportQueryParams;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureStreamApiSdk, SDKError};

const ARROW: &str = "application/vnd.apache.arrow.stream";

/// Shaped like an Arrow IPC stream: each message starts with the 0xFFFFFFFF
/// continuation marker, which no JSON decoder reads, and the stream ends
/// with the end marker.
fn arrow_body() -> Vec<u8> {
    let mut body = Vec::new();
    for i in 0..64u8 {
        body.extend_from_slice(&[0xff, 0xff, 0xff, 0xff, i, 0, 0, 0]);
        body.extend(std::iter::repeat_n(i, 1024));
    }
    body.extend_from_slice(&[0xff, 0xff, 0xff, 0xff, 0, 0, 0, 0]);
    body
}

/// What a request sent: its request line, its Accept header and its body.
struct Sent {
    line: String,
    accept: String,
    body: Value,
}

fn read_request(stream: &TcpStream) -> Sent {
    let mut reader = BufReader::new(stream.try_clone().unwrap());
    let mut line = String::new();
    reader.read_line(&mut line).unwrap();
    let (mut length, mut accept) = (0usize, String::new());
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
            } else if name.eq_ignore_ascii_case("accept") {
                accept = value.trim().to_string();
            }
        }
    }
    let mut body = vec![0u8; length];
    reader.read_exact(&mut body).unwrap();
    Sent { line: line.trim_end().to_string(), accept, body: serde_json::from_slice(&body).unwrap_or(Value::Null) }
}

/// Answers one request with handle, on a thread, and returns the base URL
/// and what the request sent.
fn serve(handle: impl FnOnce(&mut TcpStream) + Send + 'static) -> (String, mpsc::Receiver<Sent>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sent_tx, sent_rx) = mpsc::channel();
    thread::spawn(move || {
        let (mut stream, _) = listener.accept().unwrap();
        sent_tx.send(read_request(&stream)).unwrap();
        handle(&mut stream);
    });
    (base_url, sent_rx)
}

fn write_chunk(stream: &mut TcpStream, data: &[u8]) -> std::io::Result<()> {
    write!(stream, "{:x}\r\n", data.len())?;
    stream.write_all(data)?;
    stream.write_all(b"\r\n")?;
    stream.flush()
}

fn stream_head(stream: &mut TcpStream) {
    write!(stream, "HTTP/1.1 200 OK\r\nContent-Type: {ARROW}\r\nX-Export-Id: export-7\r\nTransfer-Encoding: chunked\r\nConnection: close\r\n\r\n").unwrap();
}

fn sdk(base_url: String) -> FixtureStreamApiSdk {
    FixtureStreamApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn input() -> types::ExportReportInput {
    types::ExportReportInput { columns: vec!["region".to_string(), "total".to_string()] }
}

#[tokio::test]
async fn the_method_returns_the_streamed_body_unread() {
    let want = arrow_body();
    let body = want.clone();
    let (base_url, sent) = serve(move |stream| {
        stream_head(stream);
        for chunk in body.chunks(4096) {
            write_chunk(stream, chunk).unwrap();
        }
        stream.write_all(b"0\r\n\r\n").unwrap();
    });
    let query = ExportReportQueryParams { format: Some("arrow".to_string()) };
    let mut stream = sdk(base_url).reports.export_report("r-1".to_string(), input(), Some(&query), None).await.unwrap();
    assert_eq!(stream.content_type, ARROW);
    assert_eq!(stream.response.headers()["x-export-id"], "export-7");
    let mut read = Vec::new();
    while let Some(chunk) = stream.response.chunk().await.unwrap() {
        read.extend_from_slice(&chunk);
    }
    assert_eq!(read, want);
    let sent = sent.recv().unwrap();
    assert_eq!(sent.line, "POST /api/reports/r-1/export?format=arrow HTTP/1.1");
    assert_eq!(sent.accept, format!("{ARROW}, application/problem+json"));
    assert_eq!(sent.body, serde_json::json!({"columns": ["region", "total"]}));
}

#[tokio::test]
async fn a_refusal_is_the_api_error() {
    let (base_url, _sent) = serve(|stream| {
        let body = r#"{"type":"about:blank","title":"Bad Request","status":400,"detail":"the report has no such column","code":"unknown_column"}"#;
        write!(stream, "HTTP/1.1 400 Bad Request\r\nContent-Type: application/problem+json\r\nX-Request-ID: req-1\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len()).unwrap();
    });
    let refused = sdk(base_url).reports.export_report("r-1".to_string(), input(), None, None).await.unwrap_err();
    assert_eq!(refused.status_code(), Some(400));
    assert_eq!(refused.error_code(), Some("unknown_column"));
    assert_eq!(refused.request_id(), Some("req-1"));
    assert!(matches!(refused, SDKError::Api { ref message, .. } if message == "the report has no such column"), "{refused:?}");
}

#[tokio::test]
async fn a_success_of_another_media_type_is_a_contract_error() {
    let (base_url, _sent) = serve(|stream| {
        let body = r#"{"data":{},"meta":{"requestId":"req-2"}}"#;
        write!(stream, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}", body.len()).unwrap();
    });
    let err = sdk(base_url).reports.export_report("r-1".to_string(), input(), None, None).await.unwrap_err();
    assert!(matches!(err, SDKError::Contract(ref message) if message.contains("expected application/vnd.apache.arrow.stream in streamed success response")), "{err:?}");
}

#[tokio::test]
async fn a_drop_before_the_end_ends_the_request() {
    let (ended_tx, ended_rx) = mpsc::channel();
    let (base_url, _sent) = serve(move |stream| {
        stream_head(stream);
        // Writes until the client's side of the connection is gone.
        while write_chunk(stream, &[0xff; 4096]).is_ok() {
            thread::sleep(Duration::from_millis(5));
        }
        let _ = ended_tx.send(());
    });
    let mut stream = sdk(base_url).reports.export_report("r-1".to_string(), input(), None, None).await.unwrap();
    assert!(stream.response.chunk().await.unwrap().is_some());
    drop(stream);
    // The runtime closes the connection, so wait on it, not on the thread.
    for _ in 0..1000 {
        if ended_rx.try_recv().is_ok() {
            return;
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    panic!("the server's request did not end after the drop");
}
`
