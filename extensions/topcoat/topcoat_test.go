package topcoat_test

import (
	"bytes"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"
)

var update = flag.Bool("update", false, "rewrite golden files")

const fixtures = "../../internal/loader/tsreader/testdata/services"

// rustOutputs are a fixture's outputs with the server in Rust and the
// Topcoat crate on.
func rustOutputs() map[string]any {
	return map[string]any{
		"types":   map[string]any{"rust": map[string]any{"enabled": true}},
		"api":     map[string]any{"enabled": true, "language": "RUST"},
		"topcoat": map[string]any{"enabled": true},
	}
}

// build runs fixture-api's pipeline with the extension linked, into
// outputRoot, with outputs as its config's outputs block.
func build(t *testing.T, outputRoot string, outputs map[string]any) (*registry.Result, error) {
	t.Helper()
	return buildService(t, "fixture-api", outputRoot, outputs)
}

// buildService runs a fixture's pipeline with the extension linked. Its
// encrypted operations are made plain: the Rust router has no decryption
// step.
func buildService(t *testing.T, name, outputRoot string, outputs map[string]any) (*registry.Result, error) {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), topcoat.Extension{})
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	service := filepath.Join(fixtures, name)
	schema, cfg, err := loader.LoadServiceWithConfig(service, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	for _, set := range schema.OperationSets {
		set.Encrypted = false
		for _, op := range set.Operations {
			op.Encrypted = false
			for _, arg := range op.Arguments {
				arg.Encrypted = false
			}
		}
	}
	cfg.Outputs = outputs
	return registry.Generate(schema, cfg, registry.Options{
		OutputRoot:  outputRoot,
		ServicePath: service,
		Paths:       testpaths.Local(t),
		Naming:      registry.DefaultNaming(),
		Registry:    reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(fixtures, name), loader.WithRegistry(reg))
		},
	})
}

// TestTheCrateIsWrittenOnlyBesideARustServer builds fixture-api with
// outputs.topcoat on: beside a Rust server the crate is written; beside a
// Go server, or with the section off, the output is skipped with its
// reason; a key the section does not declare is refused.
func TestTheCrateIsWrittenOnlyBesideARustServer(t *testing.T) {
	root := testpaths.TempDir(t)
	result, err := build(t, root, rustOutputs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if got, want := result.Outputs[topcoat.OutputKey], topcoat.Dir(root, "fixture-api"); got != want {
		t.Errorf("topcoat output in %q, want %q", got, want)
	}

	goServer := rustOutputs()
	goServer["types"] = map[string]any{"go": map[string]any{"enabled": true}}
	goServer["api"] = map[string]any{"enabled": true}
	result, err = build(t, testpaths.TempDir(t), goServer)
	if err != nil {
		t.Fatalf("build with a Go server: %v", err)
	}
	if _, written := result.Outputs[topcoat.OutputKey]; written {
		t.Error("a Go server got a Topcoat crate")
	}
	if !skipped(result, "the API server is not Rust") {
		t.Errorf("a Go server's build did not say why it skipped the crate: %v", result.Skipped)
	}

	off := rustOutputs()
	off["topcoat"] = map[string]any{"enabled": false}
	if result, err = build(t, testpaths.TempDir(t), off); err != nil || !skipped(result, "outputs.topcoat.enabled is false") {
		t.Errorf("outputs.topcoat off: %v, skipped %v", err, result.Skipped)
	}

	unknown := rustOutputs()
	unknown["topcoat"] = map[string]any{"enabled": true, "pages": true}
	if _, err := build(t, testpaths.TempDir(t), unknown); err == nil || !strings.Contains(err.Error(), "pages") {
		t.Errorf("an undeclared key in outputs.topcoat: %v", err)
	}
}

func skipped(result *registry.Result, reason string) bool {
	for _, skip := range result.Skipped {
		if strings.Contains(skip, reason) {
			return true
		}
	}
	return false
}

// TestGolden compares the crates written for fixture-api, whose
// operations need a caller, and fixture-nested-arrays-api, whose operations
// need none and whose result nests records in lists of lists, with
// testdata/golden/<service>; -update rewrites them.
func TestGolden(t *testing.T) {
	for _, service := range []string{"fixture-api", "fixture-nested-arrays-api"} {
		root := testpaths.TempDir(t)
		if _, err := buildService(t, service, root, rustOutputs()); err != nil {
			t.Fatalf("build %s: %v", service, err)
		}
		dir := topcoat.Dir(root, service)
		golden := filepath.Join("testdata", "golden", service)
		for _, file := range []string{"Cargo.toml", "src/lib.rs", "src/operations.rs", "src/records.rs", "src/wire.rs"} {
			got, err := os.ReadFile(filepath.Join(dir, file))
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(golden, file)
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s/%s differs from golden (run with -update to accept)", service, file)
			}
		}
	}
}

// TestRecordsOff builds with outputs.topcoat.records false: the crate has
// no records module.
func TestRecordsOff(t *testing.T) {
	root := testpaths.TempDir(t)
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "records": false}
	if _, err := build(t, root, outputs); err != nil {
		t.Fatalf("build: %v", err)
	}
	lib, err := os.ReadFile(filepath.Join(topcoat.Dir(root, "fixture-api"), "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(lib), "mod records") {
		t.Error("lib.rs declares records with outputs.topcoat.records false")
	}
	if _, err := os.Stat(filepath.Join(topcoat.Dir(root, "fixture-api"), "src", "records.rs")); !os.IsNotExist(err) {
		t.Errorf("records.rs written with records false: %v", err)
	}
}

// TestTheCrateServesATopcoatApp builds fixture-api's types, API and
// Topcoat crates and runs cargo clippy, then cargo test, on the Topcoat
// crate with topcoatAppTest: a Topcoat app mounts the JSON API, its pages
// call operations in-process by their routes' rules (401, 403, the
// router's 400, success), a guard admits a caller, and a record mirrors a
// result type without its @uiHidden field.
func TestTheCrateServesATopcoatApp(t *testing.T) {
	cargoTestCrate(t, "fixture-api", topcoatAppTest)
}

// TestACrateWithoutCallersBuilds does the same for
// fixture-nested-arrays-api with nestedRecordsTest: its operations need no
// caller, so the app's setup takes no PageAuthenticator, and its result's
// record holds lists of lists of nested records, an enum's strings and an
// optional list read from the API's JSON.
func TestACrateWithoutCallersBuilds(t *testing.T) {
	cargoTestCrate(t, "fixture-nested-arrays-api", nestedRecordsTest)
}

// cargoTestCrate builds service's crates with the Topcoat crate on, adds
// test as tests/app.rs of the Topcoat crate, and runs cargo clippy with
// warnings denied, then cargo test, on it.
func cargoTestCrate(t *testing.T, service, test string) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available")
	}
	root := testpaths.TempDir(t)
	if _, err := buildService(t, service, root, rustOutputs()); err != nil {
		t.Fatalf("build %s: %v", service, err)
	}
	dir := topcoat.Dir(root, service)
	manifest, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		t.Fatal(err)
	}
	manifest = append(manifest, []byte(`
[dev-dependencies]
async-trait = "0.1.89"
http = "1"
tokio = { version = "1.52.2", features = ["macros", "rt-multi-thread"] }
`)...)
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tests", "app.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	target := os.Getenv("CARGO_TARGET_DIR")
	if target == "" {
		target = filepath.Join(t.TempDir(), "target")
	}
	for _, args := range [][]string{
		{"clippy", "--quiet", "--all-targets", "--", "-D", "warnings"},
		{"test", "--quiet"},
	} {
		cmd := exec.Command(cargo, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+target)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("cargo %s on the Topcoat crate of %s: %v\n%s", args[0], service, err, out)
		}
	}
}

// TestPublicImportsOnly holds the extension to the D10 promise: it imports
// the public packages and the IR, never an internal package of the core.
func TestPublicImportsOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/parable-work/superschematic/internal") {
				t.Errorf("%s imports %s; an extension uses the public packages only", file, path)
			}
		}
	}
}

// topcoatAppTest is tests/app.rs of fixture-api's Topcoat crate.
const topcoatAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_api_topcoat::api::{
    types, Implementations, SessionImplementation, TenantCreateTenantArgs, TenantGetTenantArgs, TenantImplementation,
    TenantListTenantsArgs, TenantUpdateSecretArgs,
};
use schemas_fixture_api_topcoat::records::TenantViewRecord;
use schemas_fixture_api_topcoat::{operations, PageAuthenticator, RouterBuilderFixtureApiExt};
use topcoat::context::Cx;
use topcoat::router::{page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

// The API's own authenticator, for requests to the mounted JSON API.
struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// The page's caller: the same one for every page of the app.
struct Caller(Option<Principal>);

#[async_trait]
impl PageAuthenticator for Caller {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(self.0.clone())
    }
}

struct Tenants;

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

fn tenant(name: &str, label: &str) -> types::TenantView {
    types::TenantView { id: uuid("1"), name: name.to_string(), user_count: 3.0, internal_debug_label: label.to_string() }
}

#[async_trait]
impl SessionImplementation for Tenants {
    async fn current_tenant(&self, _ctx: RequestContext) -> Result<types::TenantView, ApiError> {
        Ok(tenant("me", ""))
    }
}

#[async_trait]
impl TenantImplementation for Tenants {
    async fn list_tenants(&self, _ctx: RequestContext, _args: TenantListTenantsArgs) -> Result<Vec<types::TenantView>, ApiError> {
        Ok(vec![])
    }
    async fn create_tenant(&self, ctx: RequestContext, args: TenantCreateTenantArgs) -> Result<types::TenantView, ApiError> {
        let caller = ctx.principal.map(|principal| principal.subject).unwrap_or_default();
        Ok(tenant(&args.input.name, &caller))
    }
    async fn get_tenant(&self, _ctx: RequestContext, _args: TenantGetTenantArgs) -> Result<types::TenantView, ApiError> {
        Ok(tenant("got", ""))
    }
    async fn update_secret(&self, _ctx: RequestContext, _args: TenantUpdateSecretArgs) -> Result<types::TenantView, ApiError> {
        Ok(tenant("updated", ""))
    }
}

fn outcome(result: Result<types::TenantView, ApiError>) -> String {
    match result {
        Ok(view) => format!("created {} by {}", view.name, view.internal_debug_label),
        Err(err) => err.to_string(),
    }
}

async fn create(cx: &Cx, name: &str) -> String {
    let args = TenantCreateTenantArgs { input: types::CreateTenantInput { name: name.to_string(), slug: "acme".to_string() } };
    outcome(operations::tenant_create_tenant(cx, args).await)
}

#[page(POST "/tenants")]
async fn create_tenant(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create(cx, "Acme").await;
    Ok(view! { <p>(said)</p> })
}

#[page(POST "/tenants/short")]
async fn create_short_tenant(cx: &Cx) -> topcoat::Result<impl View> {
    let said = create(cx, "a").await;
    Ok(view! { <p>(said)</p> })
}

#[page("/tenants/readable")]
async fn readable(cx: &Cx) -> topcoat::Result<impl View> {
    let said = match operations::can_tenant_list_tenants(cx).await {
        Ok(caller) => format!("readable by {}", caller.map(|caller| caller.subject).unwrap_or_default()),
        Err(err) => err.to_string(),
    };
    Ok(view! { <p>(said)</p> })
}

fn app(caller: Option<Principal>) -> Router {
    let implementations =
        Implementations { session: Arc::new(Tenants), tenant: Arc::new(Tenants), authenticator: Arc::new(NoRequests) };
    Router::builder()
        .page(create_tenant)
        .page(create_short_tenant)
        .page(readable)
        .fixture_api(implementations, Caller(caller))
        .build()
}

async fn send(router: &Router, method: &str, uri: &str) -> (StatusCode, String) {
    let request = http::Request::builder().method(method).uri(uri).body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

#[tokio::test]
async fn a_page_calls_an_operation_by_its_route_rules() {
    let (status, body) = send(&app(None), "POST", "/tenants").await;
    assert_eq!(status, StatusCode::OK);
    assert!(body.contains("401 unauthorized: Authentication required"), "{body}");

    let reader = Some(Principal::new("ada", ["tenants.read"]));
    let (_, body) = send(&app(reader.clone()), "POST", "/tenants").await;
    assert!(body.contains("403 forbidden: Insufficient permissions"), "{body}");

    let writer = Some(Principal::new("ada", ["tenants"]));
    let (_, body) = send(&app(writer.clone()), "POST", "/tenants/short").await;
    assert!(body.contains("400 bad_request: Request body does not match the declared input"), "{body}");

    let (_, body) = send(&app(writer), "POST", "/tenants").await;
    assert!(body.contains("created Acme by ada"), "{body}");

    let (_, body) = send(&app(reader), "GET", "/tenants/readable").await;
    assert!(body.contains("readable by ada"), "{body}");
    let (_, body) = send(&app(None), "GET", "/tenants/readable").await;
    assert!(body.contains("401 unauthorized"), "{body}");
}

#[tokio::test]
async fn the_json_api_is_mounted() {
    let (status, body) = send(&app(None), "GET", "/api/openapi.json").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains("\"openapi\""), "{body}");
    let (status, body) = send(&app(None), "GET", "/api/tenants?ids=1").await;
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
}

#[test]
fn a_record_mirrors_a_result_without_its_hidden_field() {
    let record = TenantViewRecord::from(tenant("Acme", "internal"));
    assert_eq!(
        record,
        TenantViewRecord { id: uuid("1").to_string(), name: "Acme".to_string(), user_count: 3.0 }
    );
}
`

// nestedRecordsTest is tests/app.rs of fixture-nested-arrays-api's Topcoat
// crate.
const nestedRecordsTest = `use std::sync::Arc;

use async_trait::async_trait;
use schemas_fixture_nested_arrays_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_nested_arrays_api_topcoat::api::{
    types, GridGetGridArgs, GridGridLabelsArgs, GridImplementation, GridReplaceLabelsArgs, GridSaveGridArgs, Implementations,
};
use schemas_fixture_nested_arrays_api_topcoat::records::{GridViewRecord, PointRecord};
use schemas_fixture_nested_arrays_api_topcoat::{operations, RouterBuilderFixtureNestedArraysApiExt};
use topcoat::context::Cx;
use topcoat::router::{page, to_bytes, Body, Router, StatusCode};
use topcoat::view::{view, View};

struct Grids;

fn grid() -> types::GridView {
    serde_json::from_value(serde_json::json!({
        "id": "1",
        "labels": [["a", "b"], []],
        "shades": [["dark"]],
        "polygons": [[{"x": 1.0, "y": 2.0}], []],
        "weights": null,
    }))
    .unwrap()
}

#[async_trait]
impl GridImplementation for Grids {
    async fn save_grid(&self, _ctx: RequestContext, _args: GridSaveGridArgs) -> Result<types::GridView, ApiError> {
        Ok(grid())
    }
    async fn replace_labels(&self, _ctx: RequestContext, _args: GridReplaceLabelsArgs) -> Result<types::GridView, ApiError> {
        Ok(grid())
    }
    async fn get_grid(&self, ctx: RequestContext, _args: GridGetGridArgs) -> Result<types::GridView, ApiError> {
        assert!(ctx.principal.is_none());
        Ok(grid())
    }
    async fn grid_labels(&self, _ctx: RequestContext, _args: GridGridLabelsArgs) -> Result<Vec<Vec<String>>, ApiError> {
        Ok(vec![])
    }
}

#[page("/grid")]
async fn show_grid(cx: &Cx) -> topcoat::Result<impl View> {
    let args = GridGetGridArgs { id: grid().id };
    let record = GridViewRecord::from(operations::grid_get_grid(cx, args).await?);
    let first = record.labels.first().cloned().unwrap_or_default().join(",");
    Ok(view! { <p>(first)</p> })
}

#[tokio::test]
async fn a_page_calls_an_operation_that_needs_no_caller() {
    let router = Router::builder().page(show_grid).fixture_nested_arrays_api(Implementations { grid: Arc::new(Grids) }).build();
    let request = http::Request::builder().uri("/grid").body(Body::empty()).unwrap();
    let response = router.handle(request).await;
    assert_eq!(response.status(), StatusCode::OK);
    let body = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    assert!(String::from_utf8(body.to_vec()).unwrap().contains("<p>a,b</p>"));
}

#[test]
fn a_record_holds_nested_lists_of_records() {
    let record = GridViewRecord::from(grid());
    assert_eq!(record.labels, vec![vec!["a".to_string(), "b".to_string()], vec![]]);
    assert_eq!(record.shades, vec![vec!["dark".to_string()]]);
    assert_eq!(record.polygons, vec![vec![PointRecord { x: 1.0, y: 2.0 }], vec![]]);
    assert_eq!(record.weights, None);
}
`
