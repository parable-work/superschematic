package rustrestgen

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestPrincipalRateLimitAPICrateRoutes runs cargo test on the Rust API
// crate of fixture-service-auth-api with getReservation limited to two
// requests a minute per principal and syncStatus to one per client IP
// (D131): Implementations takes a principal_rate_limit_store; the
// principal route counts each caller its permission check admits, answers
// 429 with Retry-After past the limit, counts nothing for a request the
// check refuses, and answers 503 when the store fails; the ip route counts
// per client in process.
func TestPrincipalRateLimitAPICrateRoutes(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatalf("load %s: %v", serviceAuthAPI, err)
	}
	two, one := 2, 1
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			switch op.Name {
			case "getReservation":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &two, RateLimitKey: ir.RateLimitKeyPrincipal}
			case "syncStatus":
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: ir.RateLimitKeyIP}
			}
		}
	}
	cargoTestAPICrate(t, serviceAuthAPI, schema, "principal_rate_limit", principalRateLimitRouterTest)
}

// TestRustRouterRefusesAnExtensionRateLimitKey: an extension's principal
// key reads a principal only its Go readers know, so the Rust router
// refuses it rather than count per another principal (D131).
func TestRustRouterRefusesAnExtensionRateLimitKey(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, serviceAuthAPI))
	if err != nil {
		t.Fatal(err)
	}
	one := 1
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "getReservation" {
				op.Middleware = &ir.MiddlewareConfig{RateLimit: &one, RateLimitKey: "org"}
			}
		}
	}
	api, err := apigen.Generate(schema, apigen.Options{
		SchemaName:    serviceAuthAPI,
		Provider:      sessionauth.Provider{},
		RateLimitKeys: []apigen.RateLimitKey{{Name: "org", GoPrincipal: `func(r *gohttp.Request) string { return r.Header.Get("X-Org") }`}},
		Clock:         nestedArraysClock,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(schema, api, Options{SchemaName: serviceAuthAPI, Clock: nestedArraysClock})
	if err == nil || !strings.Contains(err.Error(), `@rateLimit key "org" is an extension's`) {
		t.Fatalf("err = %v, want a refusal of the extension key", err)
	}
}

// principalRateLimitRouterTest is tests/principal_rate_limit.rs of the
// generated API crate, with API_CRATE and RUNTIME_CRATE replaced by the
// crates' module names. End users send "Authorization: Bearer <subject>".
const principalRateLimitRouterTest = `use std::collections::HashMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use API_CRATE::{
    build_router, types, Implementations, LedgerImplementation, StockGetReservationArgs, StockImplementation,
    StockReleaseReservationArgs, StockReserveStockArgs, SyncImplementation,
};
use RUNTIME_CRATE::{
    bearer_token, ApiError, Authenticator, Principal, PrincipalRateLimitStore, RateLimitDecision, RequestContext,
    ServiceAuthenticator, ServiceCaller,
};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::request::Parts;
use axum::http::StatusCode;
use axum::Router;
use serde_json::{json, Value};
use tower::ServiceExt;

struct Users;

#[async_trait]
impl Authenticator for Users {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_token(&request.headers).map(|token| Principal::new(token, Vec::<String>::new())))
    }
}

struct NoServices;

#[async_trait]
impl ServiceAuthenticator for NoServices {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<ServiceCaller>, ApiError> {
        Ok(None)
    }
}

#[derive(Default)]
struct Counts {
    counts: Mutex<HashMap<(String, String), u32>>,
    fail: AtomicBool,
}

#[async_trait]
impl PrincipalRateLimitStore for Counts {
    async fn take(
        &self,
        bucket: &str,
        principal: &str,
        limit: u32,
        _window: Duration,
    ) -> Result<RateLimitDecision, Box<dyn std::error::Error + Send + Sync>> {
        if self.fail.load(Ordering::SeqCst) {
            return Err("store down".into());
        }
        let mut counts = self.counts.lock().unwrap();
        let count = counts.entry((bucket.to_string(), principal.to_string())).or_default();
        *count += 1;
        Ok(RateLimitDecision { allowed: *count <= limit, retry_after: Duration::from_secs(30) })
    }
}

struct Stock;

fn reservation(id: &str) -> types::Reservation {
    serde_json::from_value(json!({"id": id, "sku": "s1", "held": true})).unwrap()
}

fn run() -> types::StockRun {
    serde_json::from_value(json!({"id": "run", "done": true})).unwrap()
}

#[async_trait]
impl LedgerImplementation for Stock {
    async fn list_reservations(&self, _ctx: RequestContext) -> Result<Vec<types::Reservation>, ApiError> {
        Ok(vec![])
    }
}

#[async_trait]
impl StockImplementation for Stock {
    async fn reindex_stock(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(run())
    }
    async fn reserve_stock(&self, _ctx: RequestContext, _args: StockReserveStockArgs) -> Result<types::Reservation, ApiError> {
        Ok(reservation("r"))
    }
    async fn get_reservation(&self, _ctx: RequestContext, args: StockGetReservationArgs) -> Result<types::Reservation, ApiError> {
        Ok(reservation(&args.id))
    }
    async fn release_reservation(&self, _ctx: RequestContext, args: StockReleaseReservationArgs) -> Result<types::Reservation, ApiError> {
        Ok(reservation(&args.id))
    }
}

#[async_trait]
impl SyncImplementation for Stock {
    async fn sync_status(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(run())
    }
    async fn sync_stock(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(run())
    }
    async fn sync_my_stock(&self, _ctx: RequestContext) -> Result<types::StockRun, ApiError> {
        Ok(run())
    }
}

fn router(store: Arc<Counts>) -> Router {
    let stock = Arc::new(Stock);
    build_router(Implementations {
        ledger: stock.clone(),
        stock: stock.clone(),
        sync: stock,
        authenticator: Arc::new(Users),
        principal_rate_limit_store: store,
        service_authenticator: Arc::new(NoServices),
    })
}

async fn get(router: &Router, uri: &str, user: Option<&str>) -> (StatusCode, Option<String>, Value) {
    let mut request = axum::http::Request::builder().method("GET").uri(uri);
    if let Some(token) = user {
        request = request.header("authorization", format!("Bearer {token}"));
    }
    let response = router.clone().oneshot(request.body(Body::empty()).unwrap()).await.unwrap();
    let status = response.status();
    let retry_after = response.headers().get("retry-after").map(|value| value.to_str().unwrap().to_string());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, retry_after, serde_json::from_slice(&bytes).unwrap_or(Value::Null))
}

#[tokio::test]
async fn a_principal_route_counts_each_caller_after_the_permission_check() {
    let store = Arc::new(Counts::default());
    let router = router(store.clone());
    for _ in 0..2 {
        assert_eq!(get(&router, "/api/stock/reservations/r1", Some("ana")).await.0, StatusCode::OK);
    }
    let (status, retry_after, body) = get(&router, "/api/stock/reservations/r1", Some("ana")).await;
    assert_eq!((status, retry_after.as_deref()), (StatusCode::TOO_MANY_REQUESTS, Some("30")), "{body}");
    assert_eq!(body["code"], "too_many_requests");
    assert_eq!(get(&router, "/api/stock/reservations/r1", Some("ben")).await.0, StatusCode::OK);

    assert_eq!(get(&router, "/api/stock/reservations/r1", None).await.0, StatusCode::UNAUTHORIZED);
    let buckets: Vec<(String, String)> = store.counts.lock().unwrap().keys().cloned().collect();
    assert_eq!(buckets.len(), 2, "{buckets:?}");
    assert!(buckets.iter().all(|(bucket, _)| bucket == "GET /api/stock/reservations/{id}"), "{buckets:?}");

    store.fail.store(true, Ordering::SeqCst);
    let (status, _, body) = get(&router, "/api/stock/reservations/r1", Some("ben")).await;
    assert_eq!((status, body["code"].as_str()), (StatusCode::SERVICE_UNAVAILABLE, Some("rate_limit_unavailable")));
}

#[tokio::test]
async fn an_ip_route_counts_per_client_in_process() {
    let store = Arc::new(Counts::default());
    let router = router(store.clone());
    assert_eq!(get(&router, "/api/sync/status", None).await.0, StatusCode::OK);
    assert_eq!(get(&router, "/api/sync/status", None).await.0, StatusCode::TOO_MANY_REQUESTS);
    assert!(store.counts.lock().unwrap().is_empty());
}
`
