//! The client version parity vectors (D143):
//! `runtime/http/testdata/client_version_parity.json`, which the Go,
//! TypeScript and Rust runtimes each answer with their own reader of
//! `MIN_CLIENT_VERSIONS` and their own semantic version order. `versions`
//! holds pairs and how they compare, `settings` what a setting reads as or
//! that it is refused, and `decisions` whether a request's
//! `X-Client-Version` is turned away. This answers each with
//! `client_version::compare`, `Minimums::parse` and `Minimums::check`, and
//! then sends a request through `require_client_version` around a handler:
//! a client under its minimum is answered 426 with the problem
//! `client_too_old` and never reaches the handler. A member the harness
//! does not know fails it.

use axum::body::Body;
use axum::routing::get;
use axum::Router;
use http::{Request, StatusCode};
use serde::Deserialize;
use serde_json::{json, Value};
use std::cmp::Ordering;
use std::collections::BTreeMap;
use std::path::Path;
use std::sync::atomic::{AtomicUsize, Ordering as AtomicOrdering};
use std::sync::Arc;
use superschematic_http_runtime::client_version::{self, require_client_version, Minimums, TooOld};
use superschematic_http_runtime::request_ids;
use tower::ServiceExt;

const VECTORS: &str = "../testdata/client_version_parity.json";

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ParityFile {
    #[allow(dead_code)]
    description: String,
    versions: Vec<VersionVector>,
    settings: Vec<SettingVector>,
    decisions: Vec<DecisionVector>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct VersionVector {
    name: String,
    a: String,
    b: String,
    compare: Option<i8>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct SettingVector {
    name: String,
    setting: String,
    minimums: Option<BTreeMap<String, String>>,
    #[serde(default)]
    refused: bool,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields, rename_all = "camelCase")]
struct DecisionVector {
    name: String,
    setting: String,
    header: Option<String>,
    too_old: Option<WantTooOld>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct WantTooOld {
    client: String,
    version: String,
    minimum: String,
}

fn vectors() -> ParityFile {
    let path = Path::new(env!("CARGO_MANIFEST_DIR")).join(VECTORS);
    let data = std::fs::read(&path).unwrap_or_else(|err| panic!("{}: {err}", path.display()));
    let file: ParityFile = serde_json::from_slice(&data).expect("client_version_parity.json");
    assert!(
        !file.versions.is_empty() && !file.settings.is_empty() && !file.decisions.is_empty(),
        "a section holds no vector"
    );
    file
}

fn sign(order: Ordering) -> i8 {
    match order {
        Ordering::Less => -1,
        Ordering::Equal => 0,
        Ordering::Greater => 1,
    }
}

#[test]
fn the_shared_version_and_setting_vectors() {
    let file = vectors();
    let mut failures = Vec::new();
    for vector in &file.versions {
        let got = client_version::compare(&vector.a, &vector.b).map(sign);
        if got != vector.compare {
            failures.push(format!(
                "{}: compare({:?}, {:?}) = {got:?}, want {:?}",
                vector.name, vector.a, vector.b, vector.compare
            ));
        }
        // The order is antisymmetric.
        let back = client_version::compare(&vector.b, &vector.a).map(sign);
        if back != vector.compare.map(|order| -order) {
            failures.push(format!("{}: the reverse compares {back:?}", vector.name));
        }
    }
    for vector in &file.settings {
        match (Minimums::parse(&vector.setting), vector.refused) {
            (Err(err), true) => {
                if !err.to_string().starts_with(client_version::SETTING) {
                    failures.push(format!(
                        "{}: the refusal {err} does not name the setting",
                        vector.name
                    ));
                }
            }
            (Ok(minimums), false) => {
                let want = vector.minimums.clone().unwrap_or_default();
                if minimums.listed() != want || minimums.is_empty() != want.is_empty() {
                    failures.push(format!(
                        "{}: read {:?}, want {want:?}",
                        vector.name,
                        minimums.listed()
                    ));
                }
            }
            (Ok(minimums), true) => failures.push(format!(
                "{}: read {:?}, want a refusal",
                vector.name,
                minimums.listed()
            )),
            (Err(err), false) => failures.push(format!("{}: refused: {err}", vector.name)),
        }
    }
    for vector in &file.decisions {
        let minimums = Minimums::parse(&vector.setting).expect("a decision's setting");
        let got = minimums.check(vector.header.as_deref());
        let want = vector.too_old.as_ref().map(|want| TooOld {
            client: want.client.clone(),
            version: want.version.clone(),
            minimum: want.minimum.clone(),
        });
        if got != want {
            failures.push(format!(
                "{}: check({:?}) = {got:?}, want {want:?}",
                vector.name, vector.header
            ));
        }
    }
    assert!(
        failures.is_empty(),
        "{} vectors differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

/// Each decision through the middleware, inside `request_ids` as the
/// generated router layers them.
#[tokio::test]
async fn the_middleware_turns_away_what_the_vectors_say() {
    for vector in &vectors().decisions {
        let minimums = Minimums::parse(&vector.setting).expect("a decision's setting");
        let reached = Arc::new(AtomicUsize::new(0));
        let counter = Arc::clone(&reached);
        let router = Router::new()
            .route(
                "/api/carts",
                get(move || {
                    counter.fetch_add(1, AtomicOrdering::SeqCst);
                    async { "carts" }
                }),
            )
            .layer(axum::middleware::from_fn_with_state(
                Arc::new(minimums),
                require_client_version,
            ))
            .layer(axum::middleware::from_fn(request_ids));
        let mut request = Request::builder()
            .uri("/api/carts")
            .header("x-request-id", "req-1");
        if let Some(header) = &vector.header {
            request = request.header("x-client-version", header);
        }
        let response = router
            .oneshot(request.body(Body::empty()).unwrap())
            .await
            .unwrap();
        let status = response.status();
        let content_type = response
            .headers()
            .get("content-type")
            .and_then(|value| value.to_str().ok())
            .unwrap_or_default()
            .to_owned();
        let body = axum::body::to_bytes(response.into_body(), 1 << 20)
            .await
            .unwrap();
        match &vector.too_old {
            None => {
                assert_eq!(status, StatusCode::OK, "{}", vector.name);
                assert_eq!(reached.load(AtomicOrdering::SeqCst), 1, "{}", vector.name);
            }
            Some(want) => {
                assert_eq!(status, StatusCode::UPGRADE_REQUIRED, "{}", vector.name);
                assert_eq!(
                    reached.load(AtomicOrdering::SeqCst),
                    0,
                    "{}: the handler ran",
                    vector.name
                );
                assert_eq!(content_type, "application/problem+json", "{}", vector.name);
                let problem: Value = serde_json::from_slice(&body).unwrap();
                assert_eq!(
                    problem,
                    json!({
                        "type": "about:blank",
                        "title": "Upgrade Required",
                        "status": 426,
                        "detail": format!(
                            "{client} {version} is older than {minimum}, the oldest version of {client} this API serves. Upgrade {client}.",
                            client = want.client, version = want.version, minimum = want.minimum
                        ),
                        "code": "client_too_old",
                        "requestId": "req-1",
                        "details": {"client": want.client, "version": want.version, "minimum": want.minimum},
                    }),
                    "{}",
                    vector.name
                );
            }
        }
    }
}

#[test]
fn an_unset_setting_lists_no_client() {
    let minimums = Minimums::default();
    assert!(minimums.is_empty());
    assert_eq!(minimums.check(Some("cli/0.0.1")), None);
    assert_eq!(Minimums::parse("").unwrap(), minimums);
}
