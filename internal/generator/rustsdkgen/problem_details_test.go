package rustsdkgen

// problemDetailsSDKTest is tests/problem_details.rs of the generated SDK
// crate of fixture-nested-arrays-api (TestNestedArraysSDKCrateBuildsAndRuns),
// with SDK_CRATE replaced by the crate's module name. A local server answers
// grid.getGrid with an RFC 9457 problem and an X-Request-ID header, a 429
// with Retry-After, and the {"error": {...}} body the Rust server answered
// with before it wrote problems; SDKError::Api carries what each says.
const problemDetailsSDKTest = `use std::io::{BufRead, BufReader, Read, Write};
use std::net::TcpListener;
use std::thread;

use serde_json::json;
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixtureNestedArraysApiSdk, SDKError};

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

/// Answers one request per canned response: a status line, extra header
/// lines and a JSON body.
fn serve(responses: Vec<(&'static str, &'static str, String)>) -> String {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    thread::spawn(move || {
        for (status, headers, body) in responses {
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
            let mut request_body = vec![0u8; length];
            reader.read_exact(&mut request_body).unwrap();
            write!(
                stream,
                "HTTP/1.1 {status}\r\n{headers}Content-Type: application/problem+json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
        }
    });
    base_url
}

#[tokio::test]
async fn an_error_carries_the_problem_and_its_headers() {
    let base_url = serve(vec![
        (
            "400 Bad Request",
            "X-Request-ID: req-42\r\n",
            json!({
                "type": "about:blank", "title": "Bad Request", "status": 400,
                "detail": "Request body does not match the declared input",
                "code": "bad_request", "requestId": "body-id",
                "errors": {"labels": [{"validator": "listMax", "message": "must contain at most 2 items"}]},
                "details": {"location": "body"}
            })
            .to_string(),
        ),
        (
            "429 Too Many Requests",
            "Retry-After: 7\r\n",
            json!({"detail": "Rate limit exceeded", "code": "too_many_requests", "requestId": "body-id"}).to_string(),
        ),
        ("404 Not Found", "", json!({"error": {"code": "not_found", "message": "grid not found"}}).to_string()),
    ]);
    let sdk = FixtureNestedArraysApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();

    let refused = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!(refused.status_code(), Some(400));
    assert_eq!(refused.error_code(), Some("bad_request"));
    assert_eq!(refused.request_id(), Some("req-42"), "the header wins over the body");
    assert_eq!(refused.retry_after(), None);
    assert_eq!(refused.validation_errors().unwrap()["labels"][0]["validator"], "listMax");
    let SDKError::Api { message, problem, .. } = &refused else {
        panic!("unexpected {refused:?}");
    };
    assert_eq!(message, "Request body does not match the declared input");
    assert_eq!(problem.details.as_ref().unwrap()["location"], "body");

    let limited = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!(limited.retry_after(), Some(7));
    assert_eq!(limited.request_id(), Some("body-id"));
    assert!(limited.is_transient());

    let missing = sdk.grid.get_grid(id, None).await.unwrap_err();
    assert_eq!(missing.error_code(), Some("not_found"));
    assert!(missing.to_string().contains("grid not found"), "{missing}");
    assert!(missing.validation_errors().is_none());
}
`
