package rustrestgen

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestScaffoldsPlugIntoTheRouterThatServesOpenAPI writes fixture-api's
// scaffolds beside its crate and runs cargo test on
// openapiScaffoldsRouterTest, which mounts them as an integration test
// crate does a service's: the scaffold of each namespace implements its
// trait, Implementations is built from them, and build_router answers a
// scaffolded route 501 and serves the OpenAPI document and its page,
// which build_router_with's options restate or turn off. tenant has four
// operations, so its implementation.rs must hold one impl for all of them,
// each taking its operation's Args and returning its result type.
func TestScaffoldsPlugIntoTheRouterThatServesOpenAPI(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	withoutEncryption(schema)
	cargoTestAPICrate(t, "fixture-api", schema, "openapi_scaffolds", openapiScaffoldsRouterTest, func(apiDir string, output *APIOutput) error {
		_, err := WriteScaffolds(output, filepath.Join(apiDir, "scaffolds"))
		return err
	})
}

// TestScaffoldLayout pins the files WriteScaffolds writes and that a
// second run keeps them: a module root, and per namespace (its snake_case
// name) a mod.rs, implementation.rs and a file per mounted operation.
func TestScaffoldLayout(t *testing.T) {
	output := generateMultiwordAPIRust(t)
	dir := t.TempDir()
	first, err := WriteScaffolds(output, dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, path := range first.Generated {
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, filepath.ToSlash(rel))
	}
	want := []string{
		"README.md",
		"mod.rs",
		"pool_search/get_index.rs",
		"pool_search/implementation.rs",
		"pool_search/mod.rs",
		"pool_search/rebuild_index.rs",
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("generated %v, want %v", got, want)
	}
	root, err := os.ReadFile(filepath.Join(dir, "mod.rs"))
	if err != nil || !strings.Contains(string(root), "pub mod pool_search;") {
		t.Errorf("mod.rs = %q, %v; want pub mod pool_search", root, err)
	}
	impl, err := os.ReadFile(filepath.Join(dir, "pool_search", "implementation.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use schemas_fixture_multiword_api_api::types;",
		"use schemas_fixture_multiword_api_api::{PoolSearchImplementation, PoolSearchRebuildIndexArgs, PoolSearchGetIndexArgs};",
		"async fn get_index(&self, ctx: RequestContext, args: PoolSearchGetIndexArgs) -> Result<types::PoolSearchIndex, ApiError> {",
		"super::get_index::get_index(self, ctx, args).await",
		"super::rebuild_index::rebuild_index(self, ctx, args).await",
	} {
		if !strings.Contains(string(impl), want) {
			t.Errorf("implementation.rs missing %q", want)
		}
	}
	operation, err := os.ReadFile(filepath.Join(dir, "pool_search", "get_index.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"use schemas_fixture_multiword_api_api::PoolSearchGetIndexArgs;",
		"    _args: PoolSearchGetIndexArgs,\n) -> Result<types::PoolSearchIndex, ApiError> {",
	} {
		if !strings.Contains(string(operation), want) {
			t.Errorf("get_index.rs missing %q", want)
		}
	}
	second, err := WriteScaffolds(output, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Generated) != 0 || len(second.Skipped) != len(want) {
		t.Errorf("second run generated %v, skipped %d; want none generated, %d skipped", second.Generated, len(second.Skipped), len(want))
	}
}

// openapiScaffoldsRouterTest is tests/openapi_scaffolds.rs of fixture-api's
// crate, with API_CRATE and RUNTIME_CRATE replaced by the crates' module
// names.
const openapiScaffoldsRouterTest = `#[path = "../scaffolds/mod.rs"]
mod scaffolds;

use std::sync::Arc;

use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use axum::Router;
use http::request::Parts;
use serde_json::Value;
use tower::ServiceExt;
use API_CRATE::{build_router, build_router_with, Implementations, RouterOptions};
use RUNTIME_CRATE::{ApiError, Authenticator, Principal};

struct Tenants;

#[async_trait]
impl Authenticator for Tenants {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(Some(Principal::new("tester", ["tenants"])))
    }
}

fn implementations() -> Implementations {
    Implementations {
        session: Arc::new(scaffolds::session::Implementation::new()),
        tenant: Arc::new(scaffolds::tenant::Implementation::new()),
        authenticator: Arc::new(Tenants),
    }
}

async fn get(router: Router, path: &str) -> (StatusCode, String, String) {
    let response = router
        .oneshot(Request::get(path).body(Body::empty()).unwrap())
        .await
        .unwrap();
    let status = response.status();
    let content_type = response
        .headers()
        .get("content-type")
        .map(|value| value.to_str().unwrap().to_owned())
        .unwrap_or_default();
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, content_type, String::from_utf8(body.to_vec()).unwrap())
}

#[tokio::test]
async fn a_scaffolded_route_is_not_implemented() {
    let (status, _, body) = get(build_router(implementations()), "/api/tenants/abc?includeArchived=true").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");
    assert!(body.contains("get_tenant is not implemented"), "{body}");
}

#[tokio::test]
async fn the_router_serves_the_openapi_document_and_its_page() {
    let (status, content_type, body) = get(build_router(implementations()), "/api/openapi.json").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(content_type, "application/json");
    let document: Value = serde_json::from_str(&body).unwrap();
    assert_eq!(document["info"]["version"], "1.0.0");
    assert_eq!(document["servers"][0]["url"], "http://localhost:8080");
    assert!(document["paths"]["/api/tenants/{id}"]["get"].is_object(), "{body}");

    let (status, content_type, body) = get(build_router(implementations()), "/api/docs").await;
    assert_eq!(status, StatusCode::OK);
    assert_eq!(content_type, "text/html; charset=utf-8");
    assert!(body.contains("<rapi-doc"));

    let options = RouterOptions {
        openapi_version: "2.4.0".to_owned(),
        openapi_base_url: "https://api.example".to_owned(),
        ..RouterOptions::default()
    };
    let (_, _, body) = get(build_router_with(implementations(), options), "/api/openapi.json").await;
    let document: Value = serde_json::from_str(&body).unwrap();
    assert_eq!(document["info"]["version"], "2.4.0");
    assert_eq!(document["servers"][0]["url"], "https://api.example");

    let off = RouterOptions {
        serve_openapi: false,
        ..RouterOptions::default()
    };
    let (status, _, _) = get(build_router_with(implementations(), off), "/api/openapi.json").await;
    assert_eq!(status, StatusCode::NOT_FOUND);
}
`
