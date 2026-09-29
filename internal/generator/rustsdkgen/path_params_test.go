package rustsdkgen

// pathParamsSDKTest is tests/path_params.rs of the generated SDK crate of
// fixture-nested-arrays-api, with grid.cell added
// (TestNestedArraysSDKCrateBuildsAndRuns), and SDK_CRATE replaced by the
// crate's module name: each path value, one with %, /, ?, # or non-ASCII
// text among them, is sent as one path segment, percent-encoded once as
// encodeURIComponent writes it, and a server that decodes it once receives
// the value passed.
const pathParamsSDKTest = `use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::sync::mpsc;
use std::thread;

use percent_encoding::percent_decode_str;
use serde_json::{json, Value};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureNestedArraysApiSdk};

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

/// Answers count requests, each with the label segment of its path decoded
/// once, as every server decodes it, in the success envelope, and sends
/// each request's path back as it was sent.
fn serve(count: usize) -> (String, mpsc::Receiver<String>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for _ in 0..count {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let path = request_line.split_whitespace().nth(1).unwrap_or_default().to_string();
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                if header.trim_end().is_empty() {
                    break;
                }
            }
            let segments: Vec<&str> = path.split('/').collect();
            let label = match segments.as_slice() {
                [_, _, _, _, _, label] => json!(percent_decode_str(label).decode_utf8().unwrap()),
                _ => Value::Null,
            };
            sender.send(path.clone()).unwrap();
            let payload = json!({"data": label, "meta": {"requestId": "req-1"}}).to_string();
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
async fn each_path_value_is_one_segment_encoded_once() {
    // Each label and the path segment encodeURIComponent writes for it.
    let labels = [
        ("%", "%25"),
        ("a%25b", "a%2525b"),
        ("100%", "100%25"),
        ("x%41y", "x%2541y"),
        ("a/b", "a%2Fb"),
        ("a b", "a%20b"),
        ("caf\u{e9}", "caf%C3%A9"),
        ("a?b", "a%3Fb"),
        ("a#b", "a%23b"),
        ("a+b", "a%2Bb"),
    ];
    let (base_url, requests) = serve(labels.len());
    let sdk = FixtureNestedArraysApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();
    for (label, segment) in labels {
        let received = sdk.grid.cell(id, label.to_string(), None).await.unwrap();
        assert_eq!(requests.recv().unwrap(), format!("/api/grids/{id}/cells/{segment}"), "{label}");
        assert_eq!(received, label, "the server received {received:?} for {label:?}");
    }
}
`
