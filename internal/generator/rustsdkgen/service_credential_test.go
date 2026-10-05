package rustsdkgen

// serviceCredentialSDKTest is tests/service_credential.rs of the
// fixture-nested-arrays-api SDK crate, which
// TestNestedArraysSDKCrateBuildsAndRuns builds (D37): every request carries
// the service credential in each configured header; a 401 with the code
// service_unauthorized, in an RFC 9457 problem or the Rust envelope, asks
// the source for a fresh token once and never runs the end-user refresh;
// any other 401 refreshes the end user and never asks for a fresh service
// token; a call spends at most one of each. RequestOptions::forward sends
// the forwarded end user, or none, instead of the configured token, and
// never refreshes. SDK_CRATE is replaced by the crate's module name.
const serviceCredentialSDKTest = `use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::sync::{mpsc, Arc, Mutex};
use std::thread;

use serde_json::json;
use SDK_CRATE::types;
use SDK_CRATE::{
    ClientConfig, FixtureNestedArraysApiSdk, RefreshTokenCallback, RequestOptions, SDKError,
    ServiceCredential, ServiceTokenSource,
};

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";
const SERVICE_REFUSAL: &str = r#"{"title":"Unauthorized","status":401,"detail":"Invalid service credential","code":"service_unauthorized"}"#;
const USER_REFUSAL: &str = r#"{"title":"Unauthorized","status":401,"detail":"Authentication required","code":"unauthorized"}"#;
const RUST_SERVICE_REFUSAL: &str = r#"{"error":{"code":"service_unauthorized","message":"Invalid service credential"}}"#;

/// A request's headers, names lowercased.
type Headers = Vec<(String, String)>;

/// Answers one request per reply, with its status and body, and sends each
/// request's headers back.
fn serve(replies: Vec<(u16, &'static str)>) -> (String, mpsc::Receiver<Headers>) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let (sender, receiver) = mpsc::channel();
    thread::spawn(move || {
        for (status, body) in replies {
            let (mut stream, _) = listener.accept().unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            let mut headers = Headers::new();
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                let header = header.trim_end();
                if header.is_empty() {
                    break;
                }
                if let Some((name, value)) = header.split_once(':') {
                    headers.push((name.to_ascii_lowercase(), value.trim().to_string()));
                }
            }
            sender.send(headers).unwrap();
            let payload = if status == 200 {
                json!({"data": "a", "meta": {"requestId": "req-1"}}).to_string()
            } else {
                body.to_string()
            };
            write!(
                stream,
                "HTTP/1.1 {} Status\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                status,
                payload.len(),
                payload
            )
            .unwrap();
        }
    });
    (base_url, receiver)
}

fn header<'a>(headers: &'a Headers, name: &str) -> Option<&'a str> {
    headers
        .iter()
        .find(|(key, _)| key.eq_ignore_ascii_case(name))
        .map(|(_, value)| value.as_str())
}

/// Each request's value of the header, in order.
fn values(requests: &mpsc::Receiver<Headers>, count: usize, name: &str) -> Vec<Option<String>> {
    (0..count)
        .map(|_| header(&requests.recv().unwrap(), name).map(str::to_string))
        .collect()
}

/// A service credential source that records each fresh flag and returns
/// service-1, service-2, ...
fn source(fresh: Arc<Mutex<Vec<bool>>>) -> ServiceTokenSource {
    Arc::new(move |is_fresh| {
        let fresh = fresh.clone();
        Box::pin(async move {
            let mut fresh = fresh.lock().unwrap();
            fresh.push(is_fresh);
            Ok(format!("service-{}", fresh.len()))
        })
    })
}

/// An end-user refresh that counts its calls.
fn refresher(calls: Arc<Mutex<usize>>) -> RefreshTokenCallback {
    Arc::new(move || {
        let calls = calls.clone();
        Box::pin(async move {
            *calls.lock().unwrap() += 1;
            Ok("user-refreshed".to_string())
        })
    })
}

struct Fixture {
    sdk: FixtureNestedArraysApiSdk,
    fresh: Arc<Mutex<Vec<bool>>>,
    refreshes: Arc<Mutex<usize>>,
}

fn fixture(base_url: String, headers: Vec<String>, service: bool) -> Fixture {
    let fresh = Arc::new(Mutex::new(Vec::new()));
    let refreshes = Arc::new(Mutex::new(0));
    let service_credential = service.then(|| ServiceCredential {
        token: source(fresh.clone()),
        headers,
    });
    let sdk = FixtureNestedArraysApiSdk::new(ClientConfig {
        auth_token: Some("alice".to_string()),
        refresh_auth_token: Some(refresher(refreshes.clone())),
        service_credential,
        ..ClientConfig::with_base_url(base_url, None, None)
    })
    .unwrap();
    Fixture { sdk, fresh, refreshes }
}

impl Fixture {
    async fn call(&self, options: Option<&RequestOptions>) -> Result<String, SDKError> {
        let id: types::IdentityUUID = GRID_ID.parse().unwrap();
        self.sdk.grid.cell(id, "a".to_string(), options).await
    }

    fn fresh(&self) -> Vec<bool> {
        self.fresh.lock().unwrap().clone()
    }

    fn refreshes(&self) -> usize {
        *self.refreshes.lock().unwrap()
    }
}

fn bearer(token: &str) -> Option<String> {
    Some(format!("Bearer {token}"))
}

#[tokio::test]
async fn every_request_carries_the_service_credential() {
    let (base_url, requests) = serve(vec![(200, ""), (200, "")]);
    let fixture = fixture(base_url, Vec::new(), true);
    fixture.call(None).await.unwrap();
    fixture.call(None).await.unwrap();
    let headers: Vec<Headers> = (0..2).map(|_| requests.recv().unwrap()).collect();
    for (i, headers) in headers.iter().enumerate() {
        assert_eq!(header(headers, "Service-Authorization").map(str::to_string), bearer(&format!("service-{}", i + 1)));
        assert_eq!(header(headers, "Authorization"), Some("Bearer alice"));
    }
    assert_eq!(fixture.fresh(), vec![false, false]);
}

#[tokio::test]
async fn each_configured_header_carries_it() {
    let (base_url, requests) = serve(vec![(200, "")]);
    let names = vec!["Service-Authorization".to_string(), "X-Serverless-Authorization".to_string()];
    let fixture = fixture(base_url, names, true);
    fixture.call(None).await.unwrap();
    let headers = requests.recv().unwrap();
    assert_eq!(header(&headers, "Service-Authorization"), Some("Bearer service-1"));
    assert_eq!(header(&headers, "X-Serverless-Authorization"), Some("Bearer service-1"));
}

#[tokio::test]
async fn a_service_refusal_asks_for_a_fresh_token_once() {
    for body in [SERVICE_REFUSAL, RUST_SERVICE_REFUSAL] {
        let (base_url, requests) = serve(vec![(401, body), (200, "")]);
        let fixture = fixture(base_url, Vec::new(), true);
        fixture.call(None).await.unwrap();
        assert_eq!(fixture.fresh(), vec![false, true], "{body}");
        assert_eq!(fixture.refreshes(), 0, "{body}");
        assert_eq!(values(&requests, 2, "Service-Authorization"), vec![bearer("service-1"), bearer("service-2")]);
    }
}

#[tokio::test]
async fn a_second_service_refusal_is_the_callers_error() {
    let (base_url, _requests) = serve(vec![(401, SERVICE_REFUSAL), (401, SERVICE_REFUSAL)]);
    let fixture = fixture(base_url, Vec::new(), true);
    let err = fixture.call(None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401));
    assert_eq!(err.error_code(), Some("service_unauthorized"));
    assert_eq!(fixture.fresh(), vec![false, true]);
    assert_eq!(fixture.refreshes(), 0);
}

#[tokio::test]
async fn an_end_user_refusal_never_asks_the_service_source() {
    for body in [USER_REFUSAL, r#"{"title":"Unauthorized"}"#] {
        let (base_url, requests) = serve(vec![(401, body), (200, "")]);
        let fixture = fixture(base_url, Vec::new(), true);
        fixture.call(None).await.unwrap();
        assert_eq!(fixture.refreshes(), 1, "{body}");
        assert_eq!(fixture.fresh(), vec![false, false], "{body}");
        assert_eq!(values(&requests, 2, "Authorization"), vec![bearer("alice"), bearer("user-refreshed")]);
    }
}

#[tokio::test]
async fn one_call_retries_each_credential_once() {
    let (base_url, _requests) = serve(vec![(401, SERVICE_REFUSAL), (401, USER_REFUSAL), (401, SERVICE_REFUSAL)]);
    let fixture = fixture(base_url, Vec::new(), true);
    let err = fixture.call(None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401));
    assert_eq!(fixture.fresh(), vec![false, true, false]);
    assert_eq!(fixture.refreshes(), 1);
}

#[tokio::test]
async fn without_a_service_credential_a_service_refusal_does_not_refresh_the_user() {
    let (base_url, _requests) = serve(vec![(401, SERVICE_REFUSAL)]);
    let fixture = fixture(base_url, Vec::new(), false);
    let err = fixture.call(None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401));
    assert_eq!(fixture.refreshes(), 0);
}

#[tokio::test]
async fn forward_sends_the_forwarded_end_user_and_never_refreshes() {
    let (base_url, requests) = serve(vec![(200, ""), (200, ""), (401, USER_REFUSAL), (401, SERVICE_REFUSAL), (200, "")]);
    let fixture = fixture(base_url, Vec::new(), true);
    fixture.call(Some(&RequestOptions::forward(Some("bob")))).await.unwrap();
    fixture.call(Some(&RequestOptions::forward(None))).await.unwrap();
    let err = fixture.call(Some(&RequestOptions::forward(Some("bob")))).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401));
    assert_eq!(fixture.refreshes(), 0);
    // A forwarded call still retries a refused service credential.
    let options = RequestOptions {
        timeout_ms: Some(5_000),
        ..RequestOptions::forward(Some("bob"))
    };
    fixture.call(Some(&options)).await.unwrap();
    assert_eq!(fixture.fresh(), vec![false, false, false, false, true]);
    assert_eq!(
        values(&requests, 5, "Authorization"),
        vec![bearer("bob"), None, bearer("bob"), bearer("bob"), bearer("bob")]
    );
}
`
