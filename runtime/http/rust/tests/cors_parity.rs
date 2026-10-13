//! The CORS parity vectors (D55, section 8.10 of `docs/stack-model.md`):
//! `runtime/http/testdata/cors_parity.json`, which the Go runtime writes
//! (`go test ./cors -run TestWriteParityVectors -update` in
//! `runtime/http/go`) and the Go, TypeScript and Rust runtimes each decide
//! with their own CORS check. Each vector holds an API's policy, a
//! browser's request, and the decision: whether it is a preflight, whether
//! the server answers it itself, and the headers. This decides each with
//! `Policy::decide`, then sends it through `SiteCorsLayer` around a
//! handler that answers 200: a preflight the layer answers is 204 with the
//! headers and never reaches the handler, and any other request reaches it
//! and its answer carries them. A member the harness does not know fails
//! it.
//!
//! A file written before D55's amendment has no `answer`; the harness then
//! holds the layer to the amendment's rule, that it answers a preflight
//! whose origin the policy lists, the one its headers allow.

use axum::body::Body;
use axum::routing::any;
use axum::Router;
use http::{HeaderMap, HeaderName, HeaderValue, Method, StatusCode};
use serde::Deserialize;
use std::collections::{BTreeMap, BTreeSet};
use std::path::Path;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use superschematic_http_runtime::cors::{Api, Policy, SiteCorsLayer};
use tower::ServiceExt;

const VECTORS: &str = "../testdata/cors_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    comment: String,
    vectors: Vec<Vector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Vector {
    name: String,
    policy: VectorPolicy,
    request: VectorRequest,
    want: Want,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct VectorPolicy {
    origins: Vec<String>,
    methods: Vec<String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct VectorRequest {
    method: String,
    headers: BTreeMap<String, String>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Want {
    preflight: bool,
    answer: Option<bool>,
    headers: BTreeMap<String, String>,
}

impl Want {
    fn answer(&self) -> bool {
        self.answer
            .unwrap_or(self.preflight && self.headers.contains_key("Access-Control-Allow-Origin"))
    }
}

fn headers_of(request: &VectorRequest) -> HeaderMap {
    let mut headers = HeaderMap::new();
    for (name, value) in &request.headers {
        headers.insert(
            HeaderName::from_bytes(name.as_bytes()).unwrap(),
            HeaderValue::from_str(value).unwrap(),
        );
    }
    headers
}

/// What the vector's request gets from `SiteCorsLayer` around a handler
/// that answers 200, and whether it reached the handler.
async fn through_the_layer(policy: Policy, vector: &Vector) -> (StatusCode, HeaderMap, bool) {
    let reached = Arc::new(AtomicBool::new(false));
    let seen = Arc::clone(&reached);
    let app = Router::new()
        .route(
            "/api/products",
            any(move || {
                seen.store(true, Ordering::SeqCst);
                async { "ok" }
            }),
        )
        .layer(SiteCorsLayer::new(vec![Api::any(policy)]));
    let mut request = http::Request::builder()
        .method(vector.request.method.as_str())
        .uri("http://shop-api.acme.dev/api/products")
        .body(Body::empty())
        .unwrap();
    *request.headers_mut() = headers_of(&vector.request);
    let response = app.oneshot(request).await.unwrap();
    (
        response.status(),
        response.headers().clone(),
        reached.load(Ordering::SeqCst),
    )
}

async fn check(vector: &Vector) -> Option<String> {
    let policy = Policy {
        origins: vector.policy.origins.clone(),
        methods: vector.policy.methods.clone(),
    };
    let method = Method::from_bytes(vector.request.method.as_bytes()).unwrap();
    let decision = policy.decide(&method, &headers_of(&vector.request));
    let headers: BTreeMap<String, String> = decision
        .headers
        .iter()
        .map(|(name, value)| (name.to_string(), value.clone()))
        .collect();
    let want_answer = vector.want.answer();
    if decision.preflight != vector.want.preflight
        || decision.answer != want_answer
        || headers != vector.want.headers
    {
        return Some(format!(
            "decided preflight {} answer {} {headers:?}\n  want    preflight {} answer {want_answer} {:?}",
            decision.preflight, decision.answer, vector.want.preflight, vector.want.headers
        ));
    }

    let (status, answer, reached) = through_the_layer(policy, vector).await;
    let want_status = if want_answer {
        StatusCode::NO_CONTENT
    } else {
        StatusCode::OK
    };
    if status != want_status || reached == want_answer {
        return Some(format!(
            "the layer answered {status}, reaching the handler {reached}; want {want_status}"
        ));
    }
    for (name, value) in &vector.want.headers {
        let got = answer.get(name.as_str()).map(|v| v.to_str().unwrap());
        if got != Some(value.as_str()) {
            return Some(format!("the layer's {name} is {got:?}; want {value:?}"));
        }
    }
    let allowed = answer.contains_key("access-control-allow-origin");
    if allowed
        != vector
            .want
            .headers
            .contains_key("Access-Control-Allow-Origin")
    {
        return Some(format!(
            "the layer's answer has Access-Control-Allow-Origin: {allowed}"
        ));
    }
    None
}

#[tokio::test]
async fn the_shared_cors_vectors() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data = std::fs::read(&path).unwrap_or_else(|err| {
        panic!(
            "{}: {err}; the Go runtime writes it \
             (cd runtime/http/go && go test ./cors -run TestWriteParityVectors -update)",
            path.display()
        )
    });
    let file: ParityFile = serde_json::from_slice(&data).expect("cors_parity.json");
    assert!(!file.vectors.is_empty(), "no vectors");
    let names: BTreeSet<&str> = file.vectors.iter().map(|v| v.name.as_str()).collect();
    assert_eq!(names.len(), file.vectors.len(), "a vector name repeats");
    let mut failures = Vec::new();
    for vector in &file.vectors {
        if let Some(failure) = check(vector).await {
            failures.push(format!("{}: {failure}", vector.name));
        }
    }
    assert!(
        failures.is_empty(),
        "{} of {} vectors differ:\n{}",
        failures.len(),
        file.vectors.len(),
        failures.join("\n")
    );
}
