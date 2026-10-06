package rustrestgen

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestArgsCheckRefusesWhatTheRouterRefuses runs cargo test on fixture-api's
// API crate with argsCheckTest: arguments a caller builds that break a list
// bound of a query argument or a rule of the input type are refused by the
// Args struct's check with the problem the router answers a request that
// carries them, and arguments the router accepts pass; an operation's
// OperationInfo admits a caller as its route does, and its context carries
// the route and the caller to the implementation.
func TestArgsCheckRefusesWhatTheRouterRefuses(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	withoutEncryption(schema)
	cargoTestAPICrate(t, "fixture-api", schema, "args_check", argsCheckTest)
}

// argsCheckTest is tests/args_check.rs of the generated API crate, with
// API_CRATE and RUNTIME_CRATE replaced by the crates' module names. Its
// authenticator admits every request as a caller holding "tenants".
const argsCheckTest = `use std::sync::Arc;

use API_CRATE::operations::{ALL, TENANT_CREATE_TENANT, TENANT_CUSTOM_HANDLER, TENANT_LIST_TENANTS};
use API_CRATE::{
    build_router, types, Implementations, SessionImplementation, TenantCreateTenantArgs, TenantGetTenantArgs,
    TenantImplementation, TenantListTenantsArgs, TenantUpdateSecretArgs,
};
use RUNTIME_CRATE::{ApiError, Authenticator, Principal, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::request::Parts;
use axum::http::{Method, Request, StatusCode};
use serde_json::{json, Value};
use tower::ServiceExt;

struct Everyone;

#[async_trait]
impl Authenticator for Everyone {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("u", ["tenants"])))
    }
}

struct Tenants;

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(json!(text)).unwrap()
}

fn view(name: &str) -> types::TenantView {
    types::TenantView { id: uuid("1"), name: name.to_string(), user_count: 0.0, internal_debug_label: String::new() }
}

#[async_trait]
impl SessionImplementation for Tenants {
    async fn current_tenant(&self, _ctx: RequestContext) -> Result<types::TenantView, ApiError> {
        Ok(view("me"))
    }
}

#[async_trait]
impl TenantImplementation for Tenants {
    async fn list_tenants(&self, _ctx: RequestContext, args: TenantListTenantsArgs) -> Result<Vec<types::TenantView>, ApiError> {
        Ok(args.ids.iter().map(|_| view("listed")).collect())
    }
    async fn create_tenant(&self, ctx: RequestContext, args: TenantCreateTenantArgs) -> Result<types::TenantView, ApiError> {
        let caller = ctx.principal.map(|principal| principal.subject).unwrap_or_default();
        Ok(types::TenantView { internal_debug_label: caller, ..view(&args.input.name) })
    }
    async fn get_tenant(&self, _ctx: RequestContext, _args: TenantGetTenantArgs) -> Result<types::TenantView, ApiError> {
        Ok(view("got"))
    }
    async fn update_secret(&self, _ctx: RequestContext, _args: TenantUpdateSecretArgs) -> Result<types::TenantView, ApiError> {
        Ok(view("updated"))
    }
}

fn implementations() -> Implementations {
    Implementations { session: Arc::new(Tenants), tenant: Arc::new(Tenants), authenticator: Arc::new(Everyone) }
}

async fn call(method: &str, uri: String, body: Option<Value>) -> (StatusCode, Value) {
    let request = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    let request = match body {
        Some(body) => request.body(Body::from(body.to_string())).unwrap(),
        None => request.body(Body::empty()).unwrap(),
    };
    let response = build_router(implementations()).oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, serde_json::from_slice(&bytes).unwrap())
}

// The problem the router answers, as status, code, detail, details and
// errors, beside the same members of check's refusal.
fn same_refusal(status: StatusCode, body: &Value, refused: ApiError) {
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    let problem = |err: &ApiError| {
        json!({
            "code": err.code,
            "detail": err.message,
            "details": err.details.as_deref(),
            "errors": err.errors.as_deref(),
        })
    };
    let routed = json!({
        "code": body["code"],
        "detail": body["detail"],
        "details": body.get("details"),
        "errors": body.get("errors"),
    });
    assert_eq!(refused.status, status);
    assert_eq!(problem(&refused), routed);
}

fn ids(count: usize) -> Vec<types::IdentityUUID> {
    (0..count).map(|i| uuid(&(i + 1).to_string())).collect()
}

fn query_list(ids: &[types::IdentityUUID]) -> String {
    ids.iter().map(ToString::to_string).collect::<Vec<_>>().join(",")
}

#[tokio::test]
async fn a_query_list_bound_is_checked_as_the_router_checks_it() {
    let too_many = ids(101);
    let (status, body) = call("GET", format!("/api/tenants?ids={}", query_list(&too_many)), None).await;
    let args = TenantListTenantsArgs { ids: too_many, statuses: None };
    same_refusal(status, &body, args.check().unwrap_err());

    let one = ids(1);
    let statuses = vec![types::TenantListStatus::Active; 11];
    let (status, body) = call(
        "GET",
        format!("/api/tenants?ids={}&statuses={}", query_list(&one), ["active"; 11].join(",")),
        None,
    )
    .await;
    let args = TenantListTenantsArgs { ids: one.clone(), statuses: Some(statuses) };
    same_refusal(status, &body, args.check().unwrap_err());

    let (status, body) = call("GET", format!("/api/tenants?ids={}&statuses=active", query_list(&one)), None).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let args = TenantListTenantsArgs { ids: one, statuses: Some(vec![types::TenantListStatus::Active]) };
    args.check().unwrap();
}

#[tokio::test]
async fn an_input_is_checked_by_its_type_as_the_router_checks_a_body() {
    let input = types::CreateTenantInput { name: "a".to_string(), slug: "acme".to_string() };
    let (status, body) = call("POST", "/api/tenants".to_string(), Some(serde_json::to_value(&input).unwrap())).await;
    let refused = TenantCreateTenantArgs { input }.check().unwrap_err();
    assert_eq!(refused.errors.as_deref().unwrap()["name"][0]["validator"], "minLength");
    same_refusal(status, &body, refused);

    let input = types::CreateTenantInput { name: "Acme".to_string(), slug: "acme".to_string() };
    let (status, body) = call("POST", "/api/tenants".to_string(), Some(serde_json::to_value(&input).unwrap())).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    TenantCreateTenantArgs { input }.check().unwrap();
}

// An in-process caller of tenant.createTenant: its caller admitted by the
// route's rule, its arguments checked, then the implementation called with
// the context the route would build.
async fn create_in_process(caller: Option<Principal>, name: &str) -> Result<types::TenantView, ApiError> {
    let implementations = implementations();
    let caller = TENANT_CREATE_TENANT.admit(implementations.authenticator.as_ref(), caller)?;
    let args = TenantCreateTenantArgs { input: types::CreateTenantInput { name: name.to_string(), slug: "acme".to_string() } };
    args.check()?;
    implementations.tenant.create_tenant(TENANT_CREATE_TENANT.context(caller), args).await
}

#[tokio::test]
async fn an_operation_runs_in_process_by_its_route_rules() {
    assert_eq!(TENANT_CREATE_TENANT.method, "POST");
    assert_eq!(TENANT_CREATE_TENANT.path, "/api/tenants");
    assert_eq!(TENANT_CREATE_TENANT.permissions, ["tenants.write"]);
    assert_eq!(TENANT_LIST_TENANTS.permissions, ["tenants.read"]);
    assert_eq!(TENANT_CUSTOM_HANDLER.path, "/api/tenant/custom-handler");
    assert_eq!(ALL.iter().filter(|operation| operation.manual).count(), 1);
    assert_eq!(ALL.len(), 6);

    let err = create_in_process(None, "Acme").await.unwrap_err();
    assert_eq!((err.status, err.code.as_str()), (StatusCode::UNAUTHORIZED, "unauthorized"));
    let err = create_in_process(Some(Principal::new("u", ["tenants.read"])), "Acme").await.unwrap_err();
    assert_eq!((err.status, err.code.as_str()), (StatusCode::FORBIDDEN, "forbidden"));
    assert_eq!(err.to_string(), "403 forbidden: Insufficient permissions");
    let err = create_in_process(Some(Principal::new("u", ["tenants"])), "a").await.unwrap_err();
    assert_eq!(err.status, StatusCode::BAD_REQUEST);

    let created = create_in_process(Some(Principal::new("u", ["tenants.write"])), "Acme").await.unwrap();
    assert_eq!((created.name.as_str(), created.internal_debug_label.as_str()), ("Acme", "u"));
    assert_eq!(TENANT_CREATE_TENANT.context(None).method, Method::POST);
}
`
