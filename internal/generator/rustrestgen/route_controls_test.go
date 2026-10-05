package rustrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// withRouteControlEdits adds what fixture-api lacks for the behaviour test:
// @bodyLimit({ megabytes: 1 }) on tenant.createTenant, a JSON body route
// (the set's @bodyLimit is on TenantQueries, whose routes are GETs), and
// @requirePermission(["tenants.admin"]) on the manual tenant.customHandler,
// so build_router's doc lists controls for the service to apply.
func withRouteControlEdits(schema *ir.Schema) {
	one := 1
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			switch op.Name {
			case "createTenant":
				op.Middleware = &ir.MiddlewareConfig{BodyLimit: &one}
			case "customHandler":
				op.Permissions = []string{"tenants.admin"}
			}
		}
	}
}

// TestRouteControlsOfAManualRouteAreListed checks build_router's doc for a
// @manualRouteRegistration operation that needs a caller: the service
// applies the same RouteControls build_router would, with its own clone of
// the authenticator.
func TestRouteControlsOfAManualRouteAreListed(t *testing.T) {
	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	output := generateRustAPI(t, "fixture-api", true, "fixture-db", dbSchema, withoutEncryption, withRouteControlEdits)
	if !output.HasAuth || !output.HasControls {
		t.Fatalf("HasAuth = %v, HasControls = %v, want both", output.HasAuth, output.HasControls)
	}
	outDir := t.TempDir()
	output.RuntimeDepPath = "../runtime"
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	router, err := os.ReadFile(filepath.Join(outDir, "src", "router.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"/// - `POST /api/tenant/custom-handler` (tenant.customHandler): apply its controls,\n" +
			"///   `RouteControls::new().authorize(authenticator, &[\"tenants.admin\"]).apply(route)`\n",
		"    let route = RouteControls::new()\n" +
			"        .body_limit_megabytes(1)\n" +
			"        .authorize(Arc::clone(&state.implementations.authenticator), &[\"tenants.write\"])\n" +
			"        .apply(post(handle_tenant_create_tenant));\n",
	} {
		if !strings.Contains(string(router), want) {
			t.Errorf("router.rs missing %q", want)
		}
	}
}

// TestDirectivesBelowOneAreNone checks that a @rateLimit, @bodyLimit or
// @timeout below 1 adds no control, as the TypeScript server reads them.
func TestDirectivesBelowOneAreNone(t *testing.T) {
	zero, negative := 0, -5
	output := generateRustAPI(t, "fixture-multiword-api", false, "", nil, func(schema *ir.Schema) {
		for _, set := range schema.OperationSets {
			set.Middleware = &ir.MiddlewareConfig{RateLimit: &zero, BodyLimit: &negative, Timeout: &zero}
		}
	})
	if output.HasControls || output.HasAuth {
		t.Fatalf("HasControls = %v, HasAuth = %v, want neither", output.HasControls, output.HasAuth)
	}
	for _, endpoint := range output.Endpoints {
		if endpoint.HasControls() {
			t.Errorf("%s.%s has controls: %+v", endpoint.Namespace, endpoint.Name, endpoint)
		}
	}
}

// TestRouteControlsAPICrateBuildsAndRoutes runs cargo test on the Rust API
// crate of fixture-api (withRouteControlEdits) with routeControlsRouterTest:
// a route that needs a caller answers 401 without one and 403 when the
// caller holds none of its permissions, an Authenticator's own permits
// replaces the matching rule, @requireOwnership leaves ownership to the
// implementation, @rateLimit answers 429 with Retry-After per client,
// @bodyLimit answers 413, @timeout answers 504, the cheap refusals come
// before the permission check, and the service applies a manual route's
// controls with RouteControls.
func TestRouteControlsAPICrateBuildsAndRoutes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	withoutEncryption(schema)
	withRouteControlEdits(schema)
	cargoTestAPICrate(t, "fixture-api", schema, "route_controls", routeControlsRouterTest)
}

// routeControlsRouterTest is tests/route_controls.rs of the generated API
// crate, with API_CRATE and RUNTIME_CRATE replaced by the crates' module
// names. Its authenticator reads `Bearer <subject>:<permission>,...`.
const routeControlsRouterTest = `use std::net::IpAddr;
use std::sync::{Arc, Mutex};
use std::time::Duration;

use API_CRATE::{build_router, Implementations, SessionImplementation, TenantImplementation};
use RUNTIME_CRATE::{
    bearer_token, has_any_permission, ApiError, Authenticator, ClientIp, Principal, RequestContext, RouteControls,
};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::request::Parts;
use axum::http::StatusCode;
use axum::routing::post;
use axum::{Extension, Router};
use serde_json::{json, Value};
use tower::ServiceExt;

type Log = Arc<Mutex<Vec<String>>>;

struct Tokens {
    log: Log,
}

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.log.lock().unwrap().push("authenticate".to_string());
        Ok(bearer_token(&request.headers).map(|token| {
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
        }))
    }
}

// RootTokens is a project vocabulary with a root permission that covers
// every other.
struct RootTokens(Tokens);

#[async_trait]
impl Authenticator for RootTokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.0.authenticate(request).await
    }

    fn permits(&self, held: &[String], required: &[String]) -> bool {
        held.iter().any(|permission| permission == "root") || has_any_permission(held, required)
    }
}

struct Tenants {
    log: Log,
}

fn caller(ctx: &RequestContext) -> Option<String> {
    ctx.principal.as_ref().map(|principal| principal.subject.clone())
}

#[async_trait]
impl SessionImplementation for Tenants {
    async fn current_tenant(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        Ok(json!({"caller": caller(&ctx)}))
    }
}

#[async_trait]
impl TenantImplementation for Tenants {
    async fn list_tenants(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        self.log.lock().unwrap().push("list_tenants".to_string());
        Ok(json!({"caller": caller(&ctx)}))
    }
    async fn create_tenant(&self, ctx: RequestContext, payload: Value) -> Result<Value, ApiError> {
        self.log.lock().unwrap().push("create_tenant".to_string());
        Ok(json!({"name": payload["name"], "caller": caller(&ctx)}))
    }
    async fn get_tenant(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        let id = ctx.path_params.get("id").cloned().unwrap_or_default();
        self.log.lock().unwrap().push(format!("get_tenant {id}"));
        if id == "slow" {
            tokio::time::sleep(Duration::from_secs(10)).await;
        }
        Ok(json!({"id": id, "caller": caller(&ctx)}))
    }
    // @requireOwnership: the router has established the caller; owning the
    // tenant is this implementation's check.
    async fn update_secret(&self, ctx: RequestContext, _payload: Value) -> Result<Value, ApiError> {
        let id = ctx.path_params.get("id").cloned().unwrap_or_default();
        if caller(&ctx).as_deref() != Some(id.as_str()) {
            return Err(ApiError::forbidden("The caller does not own this tenant"));
        }
        Ok(json!({"id": id}))
    }
}

fn implementations(log: &Log) -> Implementations {
    let tenants = Arc::new(Tenants { log: log.clone() });
    Implementations {
        session: tenants.clone(),
        tenant: tenants,
        authenticator: Arc::new(Tokens { log: log.clone() }),
    }
}

fn router(log: &Log) -> Router {
    build_router(implementations(log))
}

struct Sent {
    status: StatusCode,
    retry_after: Option<String>,
    body: Value,
}

async fn send(router: &Router, method: &str, uri: &str, token: Option<&str>, body: Body, client: Option<[u8; 4]>) -> Sent {
    let mut request = axum::http::Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some(token) = token {
        request = request.header("authorization", format!("Bearer {token}"));
    }
    let mut request = request.body(body).unwrap();
    if let Some(client) = client {
        request.extensions_mut().insert(ClientIp(IpAddr::from(client)));
    }
    let response = router.clone().oneshot(request).await.unwrap();
    let status = response.status();
    let retry_after = response.headers().get("retry-after").map(|value| value.to_str().unwrap().to_string());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = serde_json::from_slice(&bytes).unwrap_or(Value::Null);
    Sent { status, retry_after, body }
}

async fn get(router: &Router, uri: &str, token: Option<&str>) -> Sent {
    send(router, "GET", uri, token, Body::empty(), None).await
}

/// A refusal is an RFC 9457 problem with the code and detail, naming the
/// request it refuses.
fn assert_problem(body: &Value, code: &str, detail: &str) {
    assert_eq!(body["type"], "about:blank", "{body}");
    assert_eq!(body["code"], code, "{body}");
    assert_eq!(body["detail"], detail, "{body}");
    assert!(body["requestId"].as_str().is_some_and(|id| !id.is_empty()), "{body}");
}

fn count(log: &Log, entry: &str) -> usize {
    log.lock().unwrap().iter().filter(|logged| *logged == entry).count()
}

#[tokio::test]
async fn an_auth_route_answers_401_without_a_caller() {
    let log = Log::default();
    let router = router(&log);
    let sent = get(&router, "/api/auth/me", None).await;
    assert_eq!(sent.status, StatusCode::UNAUTHORIZED);
    assert_problem(&sent.body, "unauthorized", "Authentication required");

    let sent = get(&router, "/api/auth/me", Some("ana")).await;
    assert_eq!(sent.status, StatusCode::OK);
    assert_eq!(sent.body["data"], json!({"caller": "ana"}));
}

#[tokio::test]
async fn a_permission_route_answers_403_to_a_caller_without_one_of_its_permissions() {
    let log = Log::default();
    let router = router(&log);
    assert_eq!(get(&router, "/api/tenants/t1", None).await.status, StatusCode::UNAUTHORIZED);

    let sent = get(&router, "/api/tenants/t1", Some("ana:tenants.write")).await;
    assert_eq!(sent.status, StatusCode::FORBIDDEN);
    assert_problem(&sent.body, "forbidden", "Insufficient permissions");

    // tenants covers tenants.read.
    let sent = get(&router, "/api/tenants/t1", Some("ana:tenants")).await;
    assert_eq!(sent.status, StatusCode::OK);
    assert_eq!(sent.body["data"], json!({"id": "t1", "caller": "ana"}));

    let create = |token| send(&router, "POST", "/api/tenants", token, Body::from(r#"{"name":"Acme","slug":"acme"}"#), None);
    assert_eq!(create(Some("ana:tenants.read")).await.status, StatusCode::FORBIDDEN);
    let sent = create(Some("ana:tenants.write")).await;
    assert_eq!(sent.status, StatusCode::OK);
    assert_eq!(sent.body["data"], json!({"name": "Acme", "caller": "ana"}));
    assert_eq!(count(&log, "create_tenant"), 1);
}

#[tokio::test]
async fn the_authenticator_can_replace_the_permission_rule() {
    let log = Log::default();
    let mut implementations = implementations(&log);
    implementations.authenticator = Arc::new(RootTokens(Tokens { log: log.clone() }));
    let router = build_router(implementations);
    assert_eq!(get(&router, "/api/tenants/t1", Some("ops:root")).await.status, StatusCode::OK);
    assert_eq!(get(&router, "/api/tenants/t1", Some("ops:billing")).await.status, StatusCode::FORBIDDEN);
}

#[tokio::test]
async fn require_ownership_needs_a_caller_and_leaves_ownership_to_the_implementation() {
    let log = Log::default();
    let router = router(&log);
    let patch = |token| send(&router, "PATCH", "/api/tenants/t1", token, Body::from(r#"{"secret":"s"}"#), None);
    assert_eq!(patch(None).await.status, StatusCode::UNAUTHORIZED);
    let sent = patch(Some("t2:tenants.write")).await;
    assert_eq!(sent.status, StatusCode::FORBIDDEN);
    assert_eq!(sent.body["detail"], "The caller does not own this tenant");
    assert_eq!(patch(Some("t1:tenants.write")).await.status, StatusCode::OK);
}

// TenantQueries is @rateLimit({ requestsPerMinute: 60 }): each route has its
// own limiter, counting each client by its ClientIp.
#[tokio::test]
async fn past_the_rate_limit_a_client_gets_429_with_retry_after() {
    let log = Log::default();
    let router = router(&log);
    let list = |client| send(&router, "GET", "/api/tenants?ids=t1", Some("ana:tenants.read"), Body::empty(), Some(client));
    for _ in 0..60 {
        assert_eq!(list([198, 51, 100, 1]).await.status, StatusCode::OK);
    }
    let sent = list([198, 51, 100, 1]).await;
    assert_eq!(sent.status, StatusCode::TOO_MANY_REQUESTS);
    assert_problem(&sent.body, "too_many_requests", "Too Many Requests");
    let retry_after: u64 = sent.retry_after.expect("Retry-After").parse().unwrap();
    assert!((1..=60).contains(&retry_after), "{retry_after}");
    // The refused request cost no authentication.
    assert_eq!(count(&log, "authenticate"), 60);

    assert_eq!(list([198, 51, 100, 2]).await.status, StatusCode::OK);
    let other_route = send(&router, "GET", "/api/tenants/t1", Some("ana:tenants.read"), Body::empty(), Some([198, 51, 100, 1])).await;
    assert_eq!(other_route.status, StatusCode::OK);
}

#[tokio::test]
async fn a_body_over_the_limit_answers_413_before_the_permission_check() {
    let log = Log::default();
    let router = router(&log);
    let over = format!(r#"{{"name":"{}","slug":"big"}}"#, "x".repeat(1024 * 1024));
    let sent = send(&router, "POST", "/api/tenants", None, Body::from(over.clone()), None).await;
    assert_eq!(sent.status, StatusCode::PAYLOAD_TOO_LARGE);
    assert_eq!(sent.body["code"], "payload_too_large");
    let sent = send(&router, "POST", "/api/tenants", Some("ana:tenants.write"), Body::from(over), None).await;
    assert_eq!(sent.status, StatusCode::PAYLOAD_TOO_LARGE);
    assert_eq!(count(&log, "authenticate"), 0);

    let under = format!(r#"{{"name":"{}","slug":"big"}}"#, "x".repeat(1024 * 1024 - 64));
    let sent = send(&router, "POST", "/api/tenants", Some("ana:tenants.write"), Body::from(under), None).await;
    assert_eq!(sent.status, StatusCode::OK);
}

// getTenant has the set's rate limit and body limit, a permission and a
// timeout: the rate limit refuses before the body limit, which refuses
// before the permission check.
#[tokio::test]
async fn the_rate_limit_comes_before_the_body_limit_and_both_before_the_permission_check() {
    let log = Log::default();
    let router = router(&log);
    let over = || Body::from("x".repeat(1024 * 1024 + 1));
    let sent = send(&router, "GET", "/api/tenants/t1", None, over(), None).await;
    assert_eq!(sent.status, StatusCode::PAYLOAD_TOO_LARGE);
    for _ in 0..59 {
        assert_eq!(get(&router, "/api/tenants/t1", None).await.status, StatusCode::UNAUTHORIZED);
    }
    let sent = send(&router, "GET", "/api/tenants/t1", None, over(), None).await;
    assert_eq!(sent.status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(count(&log, "authenticate"), 59);
}

#[tokio::test(start_paused = true)]
async fn a_route_past_its_timeout_answers_504() {
    let log = Log::default();
    let router = router(&log);
    let sent = get(&router, "/api/tenants/slow", Some("ana:tenants.read")).await;
    assert_eq!(sent.status, StatusCode::GATEWAY_TIMEOUT);
    assert_problem(&sent.body, "gateway_timeout", "Gateway Timeout");
    assert_eq!(count(&log, "get_tenant slow"), 1);
    assert_eq!(get(&router, "/api/tenants/t1", Some("ana:tenants.read")).await.status, StatusCode::OK);
}

// tenant.customHandler is @manualRouteRegistration and (in this test)
// @requirePermission(["tenants.admin"]): build_router does not mount it, and
// the service applies the controls build_router's doc lists.
#[tokio::test]
async fn the_service_applies_a_manual_routes_controls() {
    let log = Log::default();
    let implementations = implementations(&log);
    let authenticator = Arc::clone(&implementations.authenticator);
    let custom = || send_custom(build_router(implementations.clone()), Arc::clone(&authenticator));
    assert_eq!(post_custom(router(&log), None).await, StatusCode::NOT_FOUND);
    assert_eq!(post_custom(custom(), None).await, StatusCode::UNAUTHORIZED);
    assert_eq!(post_custom(custom(), Some("ana:tenants.write")).await, StatusCode::FORBIDDEN);
    assert_eq!(post_custom(custom(), Some("ana:tenants")).await, StatusCode::OK);
}

fn send_custom(router: Router, authenticator: Arc<dyn Authenticator>) -> Router {
    let handler = post(|Extension(principal): Extension<Principal>| async move { principal.subject });
    router.route(
        "/api/tenant/custom-handler",
        RouteControls::new().authorize(authenticator, &["tenants.admin"]).apply(handler),
    )
}

async fn post_custom(router: Router, token: Option<&str>) -> StatusCode {
    send(&router, "POST", "/api/tenant/custom-handler", token, Body::empty(), None).await.status
}
`
