package rustrestgen

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

const webhooksService = "fixture-webhooks-api"

// TestWriteRustAPIGoldenWebhooks pins the crate for fixture-webhooks-api:
// webhook.receiveStripeEvent (@hmacVerified stripe),
// webhook.receiveGithubEvent (@hmacVerified github),
// webhook.receiveRawGithubEvent (@hmacVerified github,
// @manualRouteRegistration) and event.getEvent, which is not a webhook.
// Implementations needs a verifier for both providers, the manual
// operation's included, and build_router wraps each mounted webhook route in
// webhook_verified, outside the route's RouteControls: stripe's
// @rateLimit and github's @requirePermission. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenWebhooks -update
func TestWriteRustAPIGoldenWebhooks(t *testing.T) {
	output := generateRustAPI(t, webhooksService, false, "", nil)
	if want := []string{"github", "stripe"}; !slices.Equal(output.WebhookProviders, want) {
		t.Errorf("WebhookProviders = %v, want %v", output.WebhookProviders, want)
	}
	generated := writeGoldenAPI(t, webhooksService, output)
	router := generated["src/router.rs"]
	for _, want := range []string{
		"let route = RouteControls::new()\n        .rate_limit(1)\n        .apply(post(handle_webhook_receive_stripe_event));\n    router = router.route(\n        \"/api/webhooks/stripe\",\n        webhook_verified(\n            route,\n            Arc::clone(&state.implementations.webhook_verifiers[\"stripe\"]),",
		"let route = RouteControls::new()\n        .authorize(Arc::clone(&state.implementations.authenticator), &[\"webhooks.receive\"])\n        .apply(post(handle_webhook_receive_github_event));\n    router = router.route(\n        \"/api/webhooks/github\",\n        webhook_verified(\n            route,\n            Arc::clone(&state.implementations.webhook_verifiers[\"github\"]),",
		`router = router.route("/api/events/{id}", get(handle_event_get_event));`,
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs missing %q", want)
		}
	}
}

// TestWebhooksAPICrateBuildsAndRoutes runs cargo test on the Rust API crate
// of fixture-webhooks-api with webhooksRouterTest: build_router panics
// without a verifier for each provider, a provider's verifier runs before
// the rate limit, the permission check, the handler and its body extractor,
// the handler receives the body the verifier read, the manual route runs
// the verifier once the service wraps it in webhook_verified, and a route
// that is not a webhook runs none.
func TestWebhooksAPICrateBuildsAndRoutes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, webhooksService))
	if err != nil {
		t.Fatalf("load %s: %v", webhooksService, err)
	}
	cargoTestAPICrate(t, webhooksService, schema, "webhooks", webhooksRouterTest)
}

// webhooksRouterTest is tests/webhooks.rs of the generated API crate, with
// API_CRATE and RUNTIME_CRATE replaced by the crates' module names. Each
// provider's verifier compares a header with a keyed FNV-1a hash of the
// body, standing in for the HMAC a provider sends, and records that it ran.
const webhooksRouterTest = `use std::collections::HashMap;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::{Arc, Mutex};

use API_CRATE::{
    build_router, webhook_verified, EventImplementation, Implementations, WebhookImplementation, WebhookVerifier,
};
use RUNTIME_CRATE::{bearer_token, error_response, ApiError, Authenticator, Principal, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::extract::Request;
use axum::http::request::Parts;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::{IntoResponse, Response};
use axum::routing::post;
use axum::Router;
use serde_json::{json, Value};
use tower::ServiceExt;

type Log = Arc<Mutex<Vec<String>>>;

fn signature(secret: &str, body: &[u8]) -> String {
    let mut hash: u64 = 0xcbf29ce484222325;
    for byte in secret.as_bytes().iter().chain(body) {
        hash ^= u64::from(*byte);
        hash = hash.wrapping_mul(0x100000001b3);
    }
    format!("{hash:016x}")
}

struct Signed {
    provider: &'static str,
    header: &'static str,
    log: Log,
}

#[async_trait]
impl WebhookVerifier for Signed {
    async fn verify(&self, request: Request, next: Next) -> Response {
        self.log.lock().unwrap().push(format!("verify {}", self.provider));
        let (parts, body) = request.into_parts();
        let Ok(bytes) = to_bytes(body, 1 << 20).await else {
            return error_response(ApiError::bad_request("The body could not be read")).into_response();
        };
        let sent = parts.headers.get(self.header).and_then(|value| value.to_str().ok());
        if sent != Some(signature(self.provider, &bytes).as_str()) {
            return error_response(ApiError::unauthorized("The webhook signature does not match")).into_response();
        }
        next.run(Request::from_parts(parts, Body::from(bytes))).await
    }
}

// Tokens takes the bearer token as the caller's subject, holding
// webhooks.receive unless the token is "outsider".
struct Tokens {
    log: Log,
}

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.log.lock().unwrap().push("authenticate".to_string());
        Ok(bearer_token(&request.headers).map(|token| {
            let permissions: &[&str] = if token == "outsider" { &[] } else { &["webhooks.receive"] };
            Principal::new(token, permissions.iter().copied())
        }))
    }
}

struct Events {
    log: Log,
}

#[async_trait]
impl EventImplementation for Events {
    async fn get_event(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        self.log.lock().unwrap().push("get_event".to_string());
        Ok(json!({"id": ctx.path_params.get("id"), "received": true}))
    }
}

#[async_trait]
impl WebhookImplementation for Events {
    async fn receive_stripe_event(&self, _ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        self.log.lock().unwrap().push(format!("receive_stripe_event {payload}"));
        Ok(json!({"id": payload["id"], "received": true}))
    }
    async fn receive_github_event(&self, ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        let caller = ctx.principal.map(|principal| principal.subject).unwrap_or_default();
        self.log.lock().unwrap().push(format!("receive_github_event {payload} from {caller}"));
        Ok(json!({"id": payload["id"], "received": true}))
    }
}

fn verifier(provider: &'static str, log: &Log) -> Arc<dyn WebhookVerifier> {
    let header = if provider == "stripe" { "stripe-signature" } else { "x-hub-signature-256" };
    Arc::new(Signed { provider, header, log: log.clone() })
}

fn implementations(log: &Log, providers: &[&'static str]) -> Implementations {
    let events = Arc::new(Events { log: log.clone() });
    Implementations {
        event: events.clone(),
        webhook: events,
        authenticator: Arc::new(Tokens { log: log.clone() }),
        webhook_verifiers: providers.iter().map(|provider| (provider.to_string(), verifier(provider, log))).collect::<HashMap<_, _>>(),
    }
}

fn router(log: &Log) -> Router {
    build_router(implementations(log, &["stripe", "github"]))
}

// send answers the status and the JSON body, or null for an empty body.
async fn send(router: Router, method: &str, uri: &str, body: &str, signed_by: Option<&str>, token: Option<&str>) -> (StatusCode, Value) {
    let mut request = axum::http::Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some(provider) = signed_by {
        let header = if provider == "stripe" { "stripe-signature" } else { "x-hub-signature-256" };
        request = request.header(header, signature(provider, body.as_bytes()));
    }
    if let Some(token) = token {
        request = request.header("authorization", format!("Bearer {token}"));
    }
    let response = router.oneshot(request.body(Body::from(body.to_string())).unwrap()).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    if bytes.is_empty() {
        return (status, Value::Null);
    }
    (status, serde_json::from_slice(&bytes).unwrap_or(Value::Null))
}

fn entries(log: &Log) -> Vec<String> {
    log.lock().unwrap().clone()
}

#[test]
fn build_router_panics_without_a_verifier_for_each_provider() {
    let log = Log::default();
    let missing = implementations(&log, &["stripe"]);
    assert_eq!(
        missing.validate_implementations(),
        Err("Implementations.webhook_verifiers for provider github is required".to_string())
    );
    let panic = catch_unwind(AssertUnwindSafe(|| build_router(missing))).unwrap_err();
    let message = panic.downcast_ref::<String>().cloned().unwrap_or_default();
    assert_eq!(message, "Implementations.webhook_verifiers for provider github is required");
    assert!(implementations(&log, &["stripe", "github"]).validate_implementations().is_ok());
}

#[tokio::test]
async fn a_signed_event_reaches_the_handler_with_the_body_the_verifier_read() {
    let log = Log::default();
    let body = r#"{"id":"evt_1","type":"invoice.paid"}"#;
    let (status, envelope) = send(router(&log), "POST", "/api/webhooks/stripe", body, Some("stripe"), None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], json!({"id": "evt_1", "received": true}));
    assert_eq!(
        entries(&log),
        vec!["verify stripe".to_string(), r#"receive_stripe_event {"id":"evt_1","type":"invoice.paid"}"#.to_string()]
    );
}

// An unsigned request is refused before the handler, and before its Json
// extractor would refuse a body that is not JSON.
#[tokio::test]
async fn an_unsigned_event_is_refused_before_the_handler_and_its_extractor() {
    let log = Log::default();
    for (path, body, signed_by) in [
        ("/api/webhooks/stripe", r#"{"id":"evt_2"}"#, None),
        ("/api/webhooks/stripe", "not json", None),
        ("/api/webhooks/github", r#"{"id":"d1","action":"opened"}"#, Some("stripe")),
    ] {
        let (status, envelope) = send(router(&log), "POST", path, body, signed_by, Some("octocat")).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED, "{path} {body}");
        assert_eq!(envelope["code"], "unauthorized", "{path} {body}");
    }
    assert_eq!(entries(&log), vec!["verify stripe", "verify stripe", "verify github"]);

    let (status, _) = send(router(&log), "POST", "/api/webhooks/github", r#"{"id":"d1","action":"opened"}"#, Some("github"), Some("octocat")).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(entries(&log).last().unwrap(), r#"receive_github_event {"action":"opened","id":"d1"} from octocat"#);
}

// stripe is @rateLimit({ requestsPerMinute: 1 }): an unsigned request is
// refused by the verifier and takes no token from the bucket.
#[tokio::test]
async fn the_verifier_runs_before_the_rate_limit() {
    let log = Log::default();
    let router = router(&log);
    let body = r#"{"id":"evt_3","type":"invoice.paid"}"#;
    let (status, _) = send(router.clone(), "POST", "/api/webhooks/stripe", body, None, None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED);
    let (status, _) = send(router.clone(), "POST", "/api/webhooks/stripe", body, Some("stripe"), None).await;
    assert_eq!(status, StatusCode::OK);
    let (status, envelope) = send(router.clone(), "POST", "/api/webhooks/stripe", body, Some("stripe"), None).await;
    assert_eq!(status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(envelope["code"], "too_many_requests");
    let (status, _) = send(router, "POST", "/api/webhooks/stripe", body, None, None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED);
    assert_eq!(
        entries(&log),
        vec!["verify stripe", "verify stripe", r#"receive_stripe_event {"id":"evt_3","type":"invoice.paid"}"#, "verify stripe", "verify stripe"]
    );
}

// github is @requirePermission(["webhooks.receive"]): the verifier runs
// first, so an unsigned request costs no authentication.
#[tokio::test]
async fn the_verifier_runs_before_the_permission_check() {
    let log = Log::default();
    let body = r#"{"id":"d2","action":"closed"}"#;
    let (status, _) = send(router(&log), "POST", "/api/webhooks/github", body, None, Some("octocat")).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED);
    assert_eq!(entries(&log), vec!["verify github"]);

    let (status, envelope) = send(router(&log), "POST", "/api/webhooks/github", body, Some("github"), None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED);
    assert_eq!(envelope["detail"], "Authentication required");
    let (status, envelope) = send(router(&log), "POST", "/api/webhooks/github", body, Some("github"), Some("outsider")).await;
    assert_eq!(status, StatusCode::FORBIDDEN);
    assert_eq!(envelope["code"], "forbidden");
    assert_eq!(entries(&log), vec!["verify github", "verify github", "authenticate", "verify github", "authenticate"]);
}

#[tokio::test]
async fn a_route_that_is_not_a_webhook_runs_no_verifier() {
    let log = Log::default();
    let (status, envelope) = send(router(&log), "GET", "/api/events/evt_1", "", None, None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(envelope["data"], json!({"id": "evt_1", "received": true}));
    assert_eq!(entries(&log), vec!["get_event"]);

    let (status, _) = send(router(&log), "GET", "/api/webhooks/stripe", "", None, None).await;
    assert_eq!(status, StatusCode::METHOD_NOT_ALLOWED);
    assert_eq!(entries(&log), vec!["get_event"]);
}

// webhook.receiveRawGithubEvent is @manualRouteRegistration: build_router
// does not mount it, and the service's route runs the github verifier once
// it is wrapped in webhook_verified.
#[tokio::test]
async fn the_service_wraps_its_manual_route_in_webhook_verified() {
    let log = Log::default();
    let body = r#"{"payload":"ping"}"#;
    let (status, _) = send(router(&log), "POST", "/api/webhooks/github/raw", body, Some("github"), None).await;
    assert_eq!(status, StatusCode::NOT_FOUND);

    let raw = webhook_verified(post(|body: String| async move { format!(r#"{{"raw":{body}}}"#) }), verifier("github", &log));
    let mounted = || router(&log).route("/api/webhooks/github/raw", raw.clone());
    let (status, received) = send(mounted(), "POST", "/api/webhooks/github/raw", body, Some("github"), None).await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(received, json!({"raw": {"payload": "ping"}}));
    let (status, _) = send(mounted(), "POST", "/api/webhooks/github/raw", body, None, None).await;
    assert_eq!(status, StatusCode::UNAUTHORIZED);
    assert_eq!(entries(&log), vec!["verify github", "verify github"]);
}
`
