package rustrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

const serviceAuthAPI = "fixture-service-auth-api"

// TestWriteRustAPIServiceAuthGolden pins the crate for
// fixture-service-auth-api, whose operations carry each service clause
// (D37): Implementations has a service_authenticator; a route with a clause
// runs require_service or allow_service, and every other route
// identify_service, between the body limit and the permission check; an
// @allowService handler takes its principal as an Option; and every handler
// puts the service caller on ctx.service_caller.
func TestWriteRustAPIServiceAuthGolden(t *testing.T) {
	output := generateRustAPI(t, serviceAuthAPI, false, "", nil)
	if !output.HasServiceCallers {
		t.Fatal("HasServiceCallers = false, want true")
	}
	generated := writeGoldenAPI(t, serviceAuthAPI, output)

	interfaces := generated["src/interfaces.rs"]
	for _, want := range []string{
		"use superschematic_http_runtime::{ApiError, Authenticator, RequestContext, ServiceAuthenticator};",
		"    pub service_authenticator: Arc<dyn ServiceAuthenticator>,\n",
	} {
		if !strings.Contains(interfaces, want) {
			t.Errorf("interfaces.rs missing %q", want)
		}
	}

	router := generated["src/router.rs"]
	for _, want := range []string{
		"    let route = RouteControls::new()\n" +
			"        .require_service(Arc::clone(&state.implementations.service_authenticator), &[\"fixture-service-caller-api\"])\n" +
			"        .authorize(Arc::clone(&state.implementations.authenticator), &[\"stock.reserve\"])\n" +
			"        .apply(post(handle_stock_reserve_stock));\n",
		"    let route = RouteControls::new()\n" +
			"        .allow_service(Arc::clone(&state.implementations.service_authenticator), &[\"fixture-service-caller-api\"])\n" +
			"        .authorize(Arc::clone(&state.implementations.authenticator), &[\"stock.write\"])\n" +
			"        .apply(post(handle_stock_release_reservation));\n",
		"    let route = RouteControls::new()\n" +
			"        .require_service(Arc::clone(&state.implementations.service_authenticator), &[])\n" +
			"        .apply(post(handle_stock_reindex_stock));\n",
		"    let route = RouteControls::new()\n" +
			"        .identify_service(Arc::clone(&state.implementations.service_authenticator))\n" +
			"        .apply(get(handle_sync_sync_status));\n",
		"    principal: Option<Extension<Principal>>,\n    service_caller: Option<Extension<ServiceCaller>>,\n",
		"    ctx.principal = principal.map(|Extension(principal)| principal);\n",
		"    ctx.service_caller = service_caller.map(|Extension(caller)| caller);\n",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs missing %q", want)
		}
	}
	// Only the three @allowService routes take an optional principal.
	if got := strings.Count(router, "principal: Option<Extension<Principal>>"); got != 3 {
		t.Errorf("router.rs has %d optional principals, want 3 (releaseReservation, syncMyStock, listReservations)", got)
	}
	if got, want := strings.Count(router, "service_caller: Option<Extension<ServiceCaller>>"), len(output.Endpoints); got != want {
		t.Errorf("router.rs extracts the service caller in %d handlers, want %d", got, want)
	}
}

// TestTheAllowServiceScaffoldSaysThePrincipalMayBeAbsent: the scaffold of an
// @allowService operation tells the implementation that a listed service
// may call with no end user.
func TestTheAllowServiceScaffoldSaysThePrincipalMayBeAbsent(t *testing.T) {
	output := generateRustAPI(t, serviceAuthAPI, false, "", nil)
	dir := t.TempDir()
	if _, err := WriteScaffolds(output, dir); err != nil {
		t.Fatalf("write scaffolds: %v", err)
	}
	const note = "// @allowService: a listed service may call with no end user; then\n" +
		"    // ctx.principal is None and ctx.service_caller names the service.\n"
	for file, want := range map[string]bool{
		"release_reservation.rs": true,
		"reserve_stock.rs":       false,
		"get_reservation.rs":     false,
	} {
		source, err := os.ReadFile(filepath.Join(dir, "stock", file))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(source), note); got != want {
			t.Errorf("stock/%s has the @allowService note: %v, want %v", file, got, want)
		}
	}
}

// TestASchemaWithoutAServiceClauseHasNoServiceStep: no field, no import and
// no service call in a crate whose schema declares no service clause; the
// other goldens pin it byte for byte.
func TestASchemaWithoutAServiceClauseHasNoServiceStep(t *testing.T) {
	output := generateFixtureAPIRust(t)
	if output.HasServiceCallers {
		t.Fatal("fixture-api HasServiceCallers = true, want false")
	}
	for _, endpoint := range append(output.Endpoints, output.ManualEndpoints...) {
		if endpoint.ServiceStep || endpoint.ServiceCallers != nil {
			t.Errorf("%s.%s has a service step", endpoint.Namespace, endpoint.Name)
		}
		for _, call := range endpoint.ControlCalls("authenticator", "service_authenticator") {
			if strings.Contains(call, "service") {
				t.Errorf("%s.%s calls %s", endpoint.Namespace, endpoint.Name, call)
			}
		}
	}
}

// TestTheServiceCallOfAManualRouteIsListed: build_router's doc gives a
// @manualRouteRegistration operation's service call with the rest of its
// controls, so the service applies the same step build_router would.
func TestTheServiceCallOfAManualRouteIsListed(t *testing.T) {
	output := generateRustAPI(t, serviceAuthAPI, false, "", nil, func(schema *ir.Schema) {
		for _, set := range schema.OperationSets {
			for _, op := range set.Operations {
				switch op.Name {
				case "syncMyStock", "syncStatus":
					op.ManualRouteRegistration = true
				}
			}
		}
	})
	outDir := t.TempDir()
	output.RuntimeDepPath = "../runtime"
	if err := WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(outDir, "src", "router.rs"))
	if err != nil {
		t.Fatal(err)
	}
	router := string(source)
	for _, want := range []string{
		"/// - `POST /api/sync/stock/mine` (sync.syncMyStock): apply its controls,\n" +
			"///   `RouteControls::new().allow_service(service_authenticator, &[]).authorize(authenticator, &[]).apply(route)`\n",
		"/// - `GET /api/sync/status` (sync.syncStatus): apply its controls,\n" +
			"///   `RouteControls::new().identify_service(service_authenticator).apply(route)`\n",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.rs missing %q", want)
		}
	}
}

// withServiceAuthEdits adds what fixture-service-auth-api lacks for the
// order test: @bodyLimit({ megabytes: 1 }) on stock.reserveStock,
// @rateLimit({ requestsPerMinute: 1 }) on stock.reindexStock, and a
// "partner" @hmacVerified provider on sync.syncStatus, a route without a
// service clause.
func withServiceAuthEdits(schema *ir.Schema) {
	one := 1
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			switch op.Name {
			case "reserveStock":
				op.Middleware = &ir.MiddlewareConfig{BodyLimit: &one}
			case "reindexStock":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &one}
			case "syncStatus":
				op.HMACVerifiedProvider = "partner"
			}
		}
	}
}

// TestServiceAuthAPICrateBuildsAndRoutes runs cargo test on the Rust API
// crate of fixture-service-auth-api (withServiceAuthEdits) with
// serviceAuthRouterTest: @requireService refuses a request without a
// caller (401 service_unauthorized) and one from does not list (403
// service_forbidden), as problems that name the request; with a user
// clause it also needs the forwarded end user; @allowService admits a
// listed caller with no end user and no principal, and sends any other
// request to the end-user step; a route without a clause reports a caller
// and refuses a credential that does not verify; the webhook verifier, the
// rate limit and the body limit run before the service step, which runs
// before the end-user step; and the caller reaches the implementation in
// ctx.service_caller.
func TestServiceAuthAPICrateBuildsAndRoutes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatalf("load %s: %v", serviceAuthAPI, err)
	}
	withServiceAuthEdits(schema)
	cargoTestAPICrate(t, serviceAuthAPI, schema, "service_auth", serviceAuthRouterTest)
}

// serviceAuthRouterTest is tests/service_auth.rs of the generated API
// crate, with API_CRATE and RUNTIME_CRATE replaced by the crates' module
// names. End users send "Authorization: Bearer <subject>:<permission>,...";
// services send "Service-Authorization: Bearer <deployable>:<api>,...", or
// "Bearer bad" for a credential that does not verify. Each method answers
// "<caller deployable>/<user subject>" in the id of its result.
const serviceAuthRouterTest = `use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use API_CRATE::{
    build_router, types, Implementations, LedgerImplementation, StockGetReservationArgs, StockImplementation,
    StockReleaseReservationArgs, StockReserveStockArgs, SyncImplementation, WebhookVerifier,
};
use RUNTIME_CRATE::{
    bearer_token, error_response, ApiError, Authenticator, Principal, RequestContext, ServiceAuthenticator,
    ServiceCaller,
};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::extract::Request;
use axum::http::request::Parts;
use axum::http::StatusCode;
use axum::middleware::Next;
use axum::response::Response;
use axum::Router;
use serde_json::{json, Value};
use tower::ServiceExt;

type Log = Arc<Mutex<Vec<String>>>;

const CALLER_API: &str = "fixture-service-caller-api";

struct Users {
    log: Log,
}

#[async_trait]
impl Authenticator for Users {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        self.log.lock().unwrap().push("user".to_string());
        Ok(bearer_token(&request.headers).map(|token| {
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
        }))
    }
}

struct Services {
    log: Log,
}

#[async_trait]
impl ServiceAuthenticator for Services {
    async fn authenticate(&self, request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        self.log.lock().unwrap().push("service".to_string());
        let Some(value) = request.headers.get("service-authorization") else {
            return Ok(None);
        };
        let token = value.to_str().unwrap().strip_prefix("Bearer ").unwrap_or_default();
        if token == "bad" {
            return Err(ApiError::service_unauthorized("Invalid service credential"));
        }
        let (deployable, serves) = token.split_once(':').unwrap_or((token, ""));
        Ok(Some(ServiceCaller::new(
            deployable,
            serves.split(',').filter(|api| !api.is_empty()),
            format!("sa-{deployable}"),
        )))
    }
}

// The partner's verifier accepts a request whose x-signature is "ok".
struct Partner {
    log: Log,
}

#[async_trait]
impl WebhookVerifier for Partner {
    async fn verify(&self, request: Request, next: Next) -> Response {
        self.log.lock().unwrap().push("verify".to_string());
        if request.headers().get("x-signature").is_some_and(|value| value == "ok") {
            next.run(request).await
        } else {
            error_response(ApiError::unauthorized("The signature does not verify"))
        }
    }
}

struct Stock {
    log: Log,
}

// Who called: the service caller's deployable and the end user's subject.
fn who(ctx: &RequestContext) -> String {
    let caller = ctx.service_caller.as_ref().map_or("-", |caller| caller.deployable.as_str());
    let user = ctx.principal.as_ref().map_or("-", |principal| principal.subject.as_str());
    format!("{caller}/{user}")
}

fn reservation(ctx: &RequestContext, sku: &str) -> types::Reservation {
    serde_json::from_value(json!({"id": who(ctx), "sku": sku, "held": true})).unwrap()
}

fn run(ctx: &RequestContext) -> types::StockRun {
    serde_json::from_value(json!({"id": who(ctx), "done": true})).unwrap()
}

impl Stock {
    fn handled(&self) {
        self.log.lock().unwrap().push("handler".to_string());
    }
}

#[async_trait]
impl LedgerImplementation for Stock {
    async fn list_reservations(&self, ctx: RequestContext) -> Result<Vec<types::Reservation>, ApiError> {
        self.handled();
        Ok(vec![reservation(&ctx, "listed")])
    }
}

#[async_trait]
impl StockImplementation for Stock {
    async fn reindex_stock(&self, ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        self.handled();
        Ok(run(&ctx))
    }
    async fn reserve_stock(&self, ctx: RequestContext, args: StockReserveStockArgs) -> Result<types::Reservation, ApiError> {
        self.handled();
        Ok(reservation(&ctx, &args.sku))
    }
    async fn get_reservation(&self, ctx: RequestContext, args: StockGetReservationArgs) -> Result<types::Reservation, ApiError> {
        self.handled();
        Ok(reservation(&ctx, &args.id))
    }
    async fn release_reservation(&self, ctx: RequestContext, args: StockReleaseReservationArgs) -> Result<types::Reservation, ApiError> {
        self.handled();
        Ok(reservation(&ctx, &args.id))
    }
}

#[async_trait]
impl SyncImplementation for Stock {
    async fn sync_status(&self, ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        self.handled();
        Ok(run(&ctx))
    }
    async fn sync_stock(&self, ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        self.handled();
        Ok(run(&ctx))
    }
    async fn sync_my_stock(&self, ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        self.handled();
        Ok(run(&ctx))
    }
}

fn router(log: &Log) -> Router {
    let stock = Arc::new(Stock { log: log.clone() });
    let mut webhook_verifiers: HashMap<String, Arc<dyn WebhookVerifier>> = HashMap::new();
    webhook_verifiers.insert("partner".to_string(), Arc::new(Partner { log: log.clone() }));
    build_router(Implementations {
        ledger: stock.clone(),
        stock: stock.clone(),
        sync: stock,
        authenticator: Arc::new(Users { log: log.clone() }),
        service_authenticator: Arc::new(Services { log: log.clone() }),
        webhook_verifiers,
    })
}

struct Sent {
    status: StatusCode,
    body: Value,
}

async fn send(
    router: &Router,
    method: &str,
    uri: &str,
    service: Option<&str>,
    user: Option<&str>,
    headers: &[(&str, &str)],
    body: Body,
) -> Sent {
    let mut request = axum::http::Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some(token) = service {
        request = request.header("service-authorization", format!("Bearer {token}"));
    }
    if let Some(token) = user {
        request = request.header("authorization", format!("Bearer {token}"));
    }
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let response = router.clone().oneshot(request.body(body).unwrap()).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    Sent { status, body: serde_json::from_slice(&bytes).unwrap_or(Value::Null) }
}

async fn post(router: &Router, uri: &str, service: Option<&str>, user: Option<&str>) -> Sent {
    send(router, "POST", uri, service, user, &[], Body::from(r#"{"sku":"s1"}"#)).await
}

async fn get(router: &Router, uri: &str, service: Option<&str>, user: Option<&str>) -> Sent {
    send(router, "GET", uri, service, user, &[], Body::empty()).await
}

/// A refusal is an RFC 9457 problem with the code and detail, naming the
/// request it refuses, as an end-user refusal is.
fn assert_problem(sent: &Sent, status: StatusCode, code: &str, detail: &str) {
    assert_eq!(sent.status, status, "{}", sent.body);
    assert_eq!(sent.body["type"], "about:blank", "{}", sent.body);
    assert_eq!(sent.body["code"], code, "{}", sent.body);
    assert_eq!(sent.body["detail"], detail, "{}", sent.body);
    assert!(sent.body["requestId"].as_str().is_some_and(|id| !id.is_empty()), "{}", sent.body);
}

fn answered(sent: &Sent, who: &str) {
    assert_eq!(sent.status, StatusCode::OK, "{}", sent.body);
    let data = &sent.body["data"];
    let id = if data.is_array() { &data[0]["id"] } else { &data["id"] };
    assert_eq!(id, who, "{}", sent.body);
}

fn listed() -> String {
    format!("orders:{CALLER_API}")
}

fn logged(log: &Log) -> Vec<String> {
    std::mem::take(&mut *log.lock().unwrap())
}

#[tokio::test]
async fn require_service_admits_only_a_listed_caller() {
    let log = Log::default();
    let router = router(&log);
    let uri = "/api/sync/stock";
    assert_problem(&post(&router, uri, None, None).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Service credential required");
    assert_problem(&post(&router, uri, Some("bad"), None).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Invalid service credential");
    assert_problem(&post(&router, uri, Some("billing:shop-billing"), None).await, StatusCode::FORBIDDEN, "service_forbidden", "Service not permitted");
    // An end user alone is no caller.
    assert_problem(&post(&router, uri, None, Some("ana")).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Service credential required");
    logged(&log);
    answered(&post(&router, uri, Some(&listed()), None).await, "orders/-");
    assert_eq!(logged(&log), ["service", "handler"]);

    // Without from, every caller is listed.
    answered(&post(&router, "/api/stock/reindex", Some("billing:shop-billing"), None).await, "billing/-");
}

#[tokio::test]
async fn require_service_with_a_user_clause_needs_the_forwarded_end_user() {
    let log = Log::default();
    let router = router(&log);
    let uri = "/api/stock/reservations";
    assert_problem(&post(&router, uri, Some(&listed()), None).await, StatusCode::UNAUTHORIZED, "unauthorized", "Authentication required");
    assert_problem(&post(&router, uri, Some(&listed()), Some("ana:stock.write")).await, StatusCode::FORBIDDEN, "forbidden", "Insufficient permissions");
    answered(&post(&router, uri, Some(&listed()), Some("ana:stock.reserve")).await, "orders/ana");

    // A refused service credential answers before the end user is looked at.
    logged(&log);
    assert_problem(&post(&router, uri, None, Some("ana:stock.reserve")).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Service credential required");
    assert_problem(&post(&router, uri, Some("bad"), Some("nobody")).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Invalid service credential");
    assert_eq!(logged(&log), ["service", "service"]);
}

#[tokio::test]
async fn allow_service_admits_a_listed_caller_without_a_principal() {
    let log = Log::default();
    let router = router(&log);
    let uri = "/api/stock/reservations/r1/release";
    logged(&log);
    answered(&post(&router, uri, Some(&listed()), None).await, "orders/-");
    assert_eq!(logged(&log), ["service", "handler"], "a listed caller skips the end-user step");
    // A listed caller stands in even for a forwarded end user without the permission.
    answered(&post(&router, uri, Some(&listed()), Some("bob:stock.read")).await, "orders/-");

    // Anyone else goes through the end-user step.
    assert_problem(&post(&router, uri, Some("billing:shop-billing"), None).await, StatusCode::UNAUTHORIZED, "unauthorized", "Authentication required");
    answered(&post(&router, uri, Some("billing:shop-billing"), Some("ana:stock.write")).await, "billing/ana");
    assert_problem(&post(&router, uri, None, Some("bob:stock.read")).await, StatusCode::FORBIDDEN, "forbidden", "Insufficient permissions");
    answered(&post(&router, uri, None, Some("ana:stock")).await, "-/ana");
    assert_problem(&post(&router, uri, Some("bad"), Some("ana:stock.write")).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Invalid service credential");

    // The set's @allowService with an Authenticated set, and an operation's own without from.
    answered(&get(&router, "/api/ledger/reservations", Some(&listed()), None).await, "orders/-");
    assert_problem(&get(&router, "/api/ledger/reservations", Some("billing:shop-billing"), None).await, StatusCode::UNAUTHORIZED, "unauthorized", "Authentication required");
    answered(&post(&router, "/api/sync/stock/mine", Some("billing:shop-billing"), None).await, "billing/-");
}

#[tokio::test]
async fn a_route_without_a_rule_reports_a_caller_and_refuses_a_bad_credential() {
    let log = Log::default();
    let router = router(&log);
    let uri = "/api/stock/reservations/r1";
    answered(&get(&router, uri, Some(&listed()), Some("ana")).await, "orders/ana");
    answered(&get(&router, uri, None, Some("ana")).await, "-/ana");
    assert_problem(&get(&router, uri, Some("bad"), Some("ana")).await, StatusCode::UNAUTHORIZED, "service_unauthorized", "Invalid service credential");
    assert_problem(&get(&router, uri, Some(&listed()), None).await, StatusCode::UNAUTHORIZED, "unauthorized", "Authentication required");
}

// sync.syncStatus is a public webhook route (withServiceAuthEdits): its
// verifier runs before the service step.
async fn sync_status(router: &Router, service: Option<&str>, signed: bool) -> Sent {
    let headers: &[(&str, &str)] = if signed { &[("x-signature", "ok")] } else { &[] };
    send(router, "GET", "/api/sync/status", service, None, headers, Body::empty()).await
}

#[tokio::test]
async fn the_webhook_verifier_runs_before_the_service_step() {
    let log = Log::default();
    let router = router(&log);
    assert_eq!(sync_status(&router, Some("bad"), false).await.status, StatusCode::UNAUTHORIZED);
    assert_eq!(logged(&log), ["verify"]);
    let sent = sync_status(&router, Some("bad"), true).await;
    assert_problem(&sent, StatusCode::UNAUTHORIZED, "service_unauthorized", "Invalid service credential");
    assert_eq!(logged(&log), ["verify", "service"]);
    answered(&sync_status(&router, Some(&listed()), true).await, "orders/-");
    answered(&sync_status(&router, None, true).await, "-/-");
}

// stock.reindexStock is @rateLimit 1 and stock.reserveStock @bodyLimit 1
// (withServiceAuthEdits): both refuse before the service step runs.
#[tokio::test]
async fn the_rate_limit_and_the_body_limit_run_before_the_service_step() {
    let log = Log::default();
    let router = router(&log);
    assert_eq!(post(&router, "/api/stock/reindex", None, None).await.status, StatusCode::UNAUTHORIZED);
    logged(&log);
    let sent = post(&router, "/api/stock/reindex", Some(&listed()), None).await;
    assert_eq!(sent.status, StatusCode::TOO_MANY_REQUESTS);
    assert_eq!(sent.body["code"], "too_many_requests");

    let over = format!(r#"{{"sku":"{}"}}"#, "x".repeat(1024 * 1024));
    let sent = send(&router, "POST", "/api/stock/reservations", Some(&listed()), Some("ana:stock.reserve"), &[], Body::from(over)).await;
    assert_eq!(sent.status, StatusCode::PAYLOAD_TOO_LARGE);
    assert_eq!(sent.body["code"], "payload_too_large");
    assert!(logged(&log).is_empty(), "neither refusal ran the service step");
}
`
