package rustrestgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustsdkgen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen/sdktest"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// cargoTestRoundtrip runs cargo test on the Rust API crate of schema with
// test, after withSDK, with SDK_CRATE replaced by the SDK crate's module
// name.
func cargoTestRoundtrip(t *testing.T, service string, schema *ir.Schema, testName, test string) {
	t.Helper()
	crate := strings.ReplaceAll(naming.Default().RustSDKCrate(service), "-", "_")
	cargoTestAPICrate(t, service, schema, testName, strings.ReplaceAll(test, "SDK_CRATE", crate), withSDK(schema, service))
}

// withSDK is a cargoTestAPICrate extra that writes the schema's Rust SDK
// crate beside the API crate, as a build lays them out, and makes it a
// dependency of the API crate, so an integration test serves build_router
// and calls it through the SDK.
func withSDK(schema *ir.Schema, service string) func(apiDir string, output *APIOutput) error {
	return func(apiDir string, output *APIOutput) error {
		api, err := apiOutputOf(schema, apiSource{}, Options{SchemaName: service, Clock: nestedArraysClock})
		if err != nil {
			return err
		}
		root := filepath.Dir(filepath.Dir(apiDir))
		sdkDir := filepath.Join(root, "sdk", "rust", service)
		sdk, err := rustsdkgen.Generate(api, naming.Default().RustSDKCrate(service), output.TypesCrate, nestedArraysClock)
		if err != nil {
			return err
		}
		if err := rustsdkgen.WriteSDKWithTools(sdk, api, sdkDir, output.TypesDir, nestedArraysClock); err != nil {
			return err
		}
		manifest, err := os.OpenFile(filepath.Join(apiDir, "Cargo.toml"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer manifest.Close()
		// The generated manifest ends in its [dependencies] table.
		if _, err := fmt.Fprintf(manifest, "%s = { path = %q }\n", sdk.CrateName, "../../sdk/rust/"+service); err != nil {
			return err
		}
		return nil
	}
}

// TestRustSDKCallsTheRustServer serves fixture-api's Rust router on a
// local socket and calls each mounted operation through its generated Rust
// SDK (fixtureAPIRoundtripTest): a call without a caller is a 401 problem
// and one without the permission a 403, both SDKError::Api with their
// code and the request's id; a UUID path parameter, a boolean query
// parameter, UUID and enum query lists, an input and a body argument cross
// the wire typed and come back in the result; and an implementation's
// problem reaches the SDK with its status, code and details.
func TestRustSDKCallsTheRustServer(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	withoutEncryption(schema)
	cargoTestRoundtrip(t, "fixture-api", schema, "sdk_roundtrip", fixtureAPIRoundtripTest)
}

// serveRouter is the Rust function every roundtrip test serves its router
// with: build_router on 127.0.0.1:0, answering its base URL.
const serveRouter = `async fn serve(implementations: Implementations) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, build_router(implementations)).await.unwrap() });
    base_url
}
`

// fixtureAPIRoundtripTest is tests/sdk_roundtrip.rs of fixture-api's API
// crate. Its authenticator reads ` + "`Bearer <subject>:<permission>,...`" + `.
const fixtureAPIRoundtripTest = `use std::sync::Arc;

use API_CRATE::{
    build_router, Implementations, SessionImplementation, TenantCreateTenantArgs, TenantGetTenantArgs,
    TenantImplementation, TenantListTenantsArgs, TenantUpdateSecretArgs,
};
use RUNTIME_CRATE::{bearer_token, ApiError, Authenticator, Principal, RequestContext};
use SDK_CRATE::namespaces::tenant::{GetTenantQueryParams, ListTenantsQueryParams, UpdateSecretInput};
use SDK_CRATE::{types, ClientConfig, FixtureApiSdk};
use async_trait::async_trait;
use axum::http::request::Parts;
use axum::http::StatusCode;
use serde_json::json;

struct Tokens;

#[async_trait]
impl Authenticator for Tokens {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_token(&request.headers).map(|token| {
            let (subject, permissions) = token.split_once(':').unwrap_or((token, ""));
            Principal::new(subject, permissions.split(',').filter(|p| !p.is_empty()))
        }))
    }
}

struct Tenants;

fn view(id: types::IdentityUUID, name: impl Into<String>, label: impl Into<String>) -> types::TenantView {
    types::TenantView { id, name: name.into(), user_count: 1.0, internal_debug_label: label.into() }
}

fn caller(ctx: &RequestContext) -> String {
    ctx.principal.as_ref().map(|principal| principal.subject.clone()).unwrap_or_default()
}

#[async_trait]
impl SessionImplementation for Tenants {
    async fn current_tenant(&self, ctx: RequestContext) -> Result<types::TenantView, ApiError> {
        Ok(view("1".parse().unwrap(), caller(&ctx), "me"))
    }
}

#[async_trait]
impl TenantImplementation for Tenants {
    async fn list_tenants(&self, _ctx: RequestContext, args: TenantListTenantsArgs) -> Result<Vec<types::TenantView>, ApiError> {
        let statuses: Vec<&str> = args.statuses.unwrap_or_default().iter().map(|status| status.as_str()).collect();
        Ok(args.ids.into_iter().map(|id| view(id, statuses.join(","), "listed")).collect())
    }
    async fn create_tenant(&self, ctx: RequestContext, args: TenantCreateTenantArgs) -> Result<types::TenantView, ApiError> {
        Ok(view("2".parse().unwrap(), args.input.name, format!("{} by {}", args.input.slug, caller(&ctx))))
    }
    async fn get_tenant(&self, _ctx: RequestContext, args: TenantGetTenantArgs) -> Result<types::TenantView, ApiError> {
        if args.include_archived {
            return Err(ApiError::new(StatusCode::GONE, "gone", "The tenant is archived")
                .with_details(json!({"id": args.id.to_string()})));
        }
        Ok(view(args.id, "found", "got"))
    }
    async fn update_secret(&self, ctx: RequestContext, args: TenantUpdateSecretArgs) -> Result<types::TenantView, ApiError> {
        if caller(&ctx) != args.id.to_string() {
            return Err(ApiError::forbidden("The caller does not own this tenant"));
        }
        Ok(view(args.id, args.secret, "updated"))
    }
}

` + serveRouter + `
async fn sdk(token: Option<&str>) -> FixtureApiSdk {
    let base_url = serve(Implementations {
        session: Arc::new(Tenants),
        tenant: Arc::new(Tenants),
        authenticator: Arc::new(Tokens),
    })
    .await;
    FixtureApiSdk::new(ClientConfig::with_base_url(base_url, token.map(str::to_string), None)).unwrap()
}

const TENANT: &str = "00000000-0000-4000-8000-0000000000ab";

#[tokio::test]
async fn a_refused_call_is_an_api_error_with_its_problem() {
    let err = sdk(None).await.session.current_tenant(None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(401), "{err}");
    assert_eq!(err.error_code(), Some("unauthorized"));
    assert!(err.request_id().is_some_and(|id| id.len() == 36), "{err:?}");

    let id: types::IdentityUUID = TENANT.parse().unwrap();
    let query = GetTenantQueryParams { include_archived: false };
    let err = sdk(Some("ana:tenants.write")).await.tenant.get_tenant(id, Some(&query), None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(403), "{err}");
    assert_eq!(err.error_code(), Some("forbidden"));

    let archived = GetTenantQueryParams { include_archived: true };
    let err = sdk(Some("ana:tenants")).await.tenant.get_tenant(id, Some(&archived), None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(410), "{err}");
    assert_eq!(err.error_code(), Some("gone"));
    assert_eq!(err.to_string(), "api request failed (status 410): The tenant is archived");
    assert_eq!(err.problem().unwrap().details, Some(json!({"id": id.to_string()})));
}

#[tokio::test]
async fn path_query_and_body_arguments_cross_typed() {
    let sdk = sdk(Some("ana:tenants")).await;
    assert_eq!(sdk.session.current_tenant(None).await.unwrap().name, "ana");

    let id: types::IdentityUUID = TENANT.parse().unwrap();
    let query = GetTenantQueryParams { include_archived: false };
    let found = sdk.tenant.get_tenant(id, Some(&query), None).await.unwrap();
    assert_eq!((found.id, found.name.as_str()), (id, "found"));

    let other: types::IdentityUUID = "00000000-0000-4000-8000-0000000000cd".parse().unwrap();
    let query = ListTenantsQueryParams {
        ids: vec![id, other],
        statuses: Some(vec![types::TenantListStatus::Active, types::TenantListStatus::Suspended]),
    };
    let listed = sdk.tenant.list_tenants(Some(&query), None).await.unwrap();
    assert_eq!(listed.iter().map(|tenant| tenant.id).collect::<Vec<_>>(), [id, other]);
    assert_eq!(listed[0].name, "active,suspended");

    let input = types::CreateTenantInput { name: "Acme".to_string(), slug: "acme".to_string() };
    let created = sdk.tenant.create_tenant(input, None).await.unwrap();
    assert_eq!((created.name.as_str(), created.internal_debug_label.as_str()), ("Acme", "acme by ana"));
}

// updateSecret is @requireOwnership: the router establishes the caller and
// the implementation checks that it owns the tenant.
#[tokio::test]
async fn a_body_argument_reaches_the_implementation() {
    let owner = "00000000-0000-4000-8000-0000000000ef";
    let id: types::IdentityUUID = owner.parse().unwrap();
    let token = format!("{id}:tenants.write");
    let updated = sdk(Some(&token)).await.tenant.update_secret(id, UpdateSecretInput { secret: "s3".to_string() }, None).await.unwrap();
    assert_eq!((updated.id, updated.name.as_str()), (id, "s3"));

    let err = sdk(Some("someone:tenants.write")).await.tenant.update_secret(id, UpdateSecretInput { secret: "s3".to_string() }, None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(403));
}
`

// TestRustSDKCallsTheRustServerWithListsOfLists serves
// fixture-nested-arrays-api's Rust router, with grid.paint, grid.cell and
// grid.placeOrder added, and calls it through its Rust SDK
// (nestedArraysRoundtripTest): list-of-lists arguments, inputs and results
// cross typed, each D23 path label reaches the implementation as the SDK
// sent it, encoded once and decoded once, an input with nested objects
// passes both sides' validation, and an operation the implementation does
// not implement is a 501 SDKError::Api.
func TestRustSDKCallsTheRustServerWithListsOfLists(t *testing.T) {
	schema := loadNestedArraysSchema(t, true)
	for _, add := range []func(*ir.Schema) error{sdktest.AddCellOperation, sdktest.AddPlaceOrderOperation} {
		if err := add(schema); err != nil {
			t.Fatal(err)
		}
	}
	cargoTestRoundtrip(t, nestedArraysService, schema, "sdk_roundtrip", nestedArraysRoundtripTest)
}

// nestedArraysRoundtripTest is tests/sdk_roundtrip.rs of
// fixture-nested-arrays-api's API crate.
const nestedArraysRoundtripTest = `use std::sync::Arc;

use API_CRATE::{
    build_router, GridCellArgs, GridGetGridArgs, GridGridLabelsArgs, GridImplementation, GridPaintArgs,
    GridPlaceOrderArgs, GridReplaceLabelsArgs, GridSaveGridArgs, Implementations,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use SDK_CRATE::namespaces::grid::{GridLabelsQueryParams, PaintInput, ReplaceLabelsInput};
use SDK_CRATE::{types, ClientConfig, FixtureNestedArraysApiSdk};
use async_trait::async_trait;

const GRID_ID: &str = "0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e40";

fn grid(id: types::IdentityUUID, labels: Vec<Vec<String>>) -> types::GridView {
    types::GridView { id, labels, shades: vec![], polygons: vec![], weights: None }
}

struct Echo;

#[async_trait]
impl GridImplementation for Echo {
    async fn save_grid(&self, _ctx: RequestContext, args: GridSaveGridArgs) -> Result<types::GridView, ApiError> {
        let input = args.input;
        Ok(types::GridView {
            id: GRID_ID.parse().unwrap(),
            labels: input.labels,
            shades: input.shades,
            polygons: input.polygons,
            weights: input.weights,
        })
    }
    async fn replace_labels(&self, _ctx: RequestContext, args: GridReplaceLabelsArgs) -> Result<types::GridView, ApiError> {
        Ok(grid(args.id, args.labels))
    }
    async fn paint(&self, _ctx: RequestContext, args: GridPaintArgs) -> Result<Vec<Vec<types::Point>>, ApiError> {
        assert_eq!(args.shades, vec![vec![types::Shade::Dark], vec![]]);
        Ok(args.polygons.unwrap_or_default())
    }
    async fn get_grid(&self, _ctx: RequestContext, _args: GridGetGridArgs) -> Result<types::GridView, ApiError> {
        Err(ApiError::not_implemented("get_grid is not implemented"))
    }
    async fn grid_labels(&self, _ctx: RequestContext, args: GridGridLabelsArgs) -> Result<Vec<Vec<String>>, ApiError> {
        let labels = vec![vec!["a".to_string(), "b".to_string()], vec![], vec!["c".to_string()]];
        Ok(labels.into_iter().take(args.limit.map_or(3, |limit| limit as usize)).collect())
    }
    async fn cell(&self, _ctx: RequestContext, args: GridCellArgs) -> Result<String, ApiError> {
        Ok(args.label)
    }
    async fn place_order(&self, _ctx: RequestContext, args: GridPlaceOrderArgs) -> Result<types::GridView, ApiError> {
        let products = args.input.lines.iter().map(|line| format!("{}x{}", line.product_id, line.quantity)).collect();
        let extras = args.input.extras.unwrap_or_default().into_keys().collect();
        Ok(grid(GRID_ID.parse().unwrap(), vec![products, vec![args.input.ship_to.postal_code], extras]))
    }
}

` + serveRouter + `
async fn sdk() -> FixtureNestedArraysApiSdk {
    let base_url = serve(Implementations { grid: Arc::new(Echo) }).await;
    FixtureNestedArraysApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn strings(rows: &[&[&str]]) -> Vec<Vec<String>> {
    rows.iter().map(|row| row.iter().map(|cell| cell.to_string()).collect()).collect()
}

#[tokio::test]
async fn lists_of_lists_cross_typed() {
    let sdk = sdk().await;
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();

    let stored = sdk.grid.replace_labels(id, ReplaceLabelsInput { labels: strings(&[&["a", "b"], &[]]) }, None).await.unwrap();
    assert_eq!((stored.id, stored.labels), (id, strings(&[&["a", "b"], &[]])));

    let labels = sdk.grid.grid_labels(id, Some(&GridLabelsQueryParams { limit: Some(2.0) }), None).await.unwrap();
    assert_eq!(labels, strings(&[&["a", "b"], &[]]));

    let polygons = vec![vec![types::Point { x: 3.0, y: 4.0 }], vec![]];
    let painted = sdk
        .grid
        .paint(id, PaintInput { shades: vec![vec![types::Shade::Dark], vec![]], polygons: Some(polygons.clone()) }, None)
        .await
        .unwrap();
    assert_eq!(painted, polygons);

    let input = types::SaveGridInput {
        labels: strings(&[&["a"], &[]]),
        shades: vec![vec![types::Shade::Light]],
        polygons: vec![vec![], vec![types::Point { x: 5.0, y: 6.0 }]],
        weights: Some(vec![vec![0.5], vec![]]),
    };
    let saved = sdk.grid.save_grid(input.clone(), None).await.unwrap();
    assert_eq!((saved.labels, saved.shades, saved.polygons, saved.weights), (input.labels, input.shades, input.polygons, input.weights));
}

// The D23 labels: each is one path segment the SDK encodes once and the
// router decodes once.
#[tokio::test]
async fn each_path_value_reaches_the_implementation_as_sent() {
    let sdk = sdk().await;
    let id: types::IdentityUUID = GRID_ID.parse().unwrap();
    for label in ["%", "a%25b", "100%", "x%41y", "a/b", "a b", "caf\u{e9}", "a?b", "a#b", "a+b"] {
        assert_eq!(sdk.grid.cell(id, label.to_string(), None).await.unwrap(), label);
    }
}

#[tokio::test]
async fn an_input_with_nested_objects_crosses_typed() {
    let order = types::PlaceOrderInput {
        lines: vec![types::OrderLine { product_id: "sku-1".to_string(), quantity: 2.0 }],
        ship_to: types::ShippingAddress { postal_code: "12345".to_string() },
        gift_codes: None,
        extras: Some([("gift".to_string(), Some(types::OrderLine { product_id: "sku-2".to_string(), quantity: 1.0 }))].into()),
    };
    let placed = sdk().await.grid.place_order(order, None).await.unwrap();
    assert_eq!(placed.labels, strings(&[&["sku-1x2"], &["12345"], &["gift"]]));
}

#[tokio::test]
async fn an_unimplemented_operation_is_a_501() {
    let err = sdk().await.grid.get_grid(GRID_ID.parse().unwrap(), None).await.unwrap_err();
    assert_eq!(err.status_code(), Some(501), "{err}");
    assert_eq!(err.error_code(), Some("not_implemented"));
}
`

// loadPostsAndNotes is sdktest's query-lists-api with optional-json-api's
// scalars, types and operations merged in, and note.touch added: a POST
// whose result is none.
func loadPostsAndNotes(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := sdktest.LoadQueryListsService()
	if err != nil {
		t.Fatal(err)
	}
	notes, err := sdktest.LoadOptionalJSONService()
	if err != nil {
		t.Fatal(err)
	}
	for name, scalar := range notes.Scalars {
		schema.Scalars[name] = scalar
	}
	if schema.Types == nil {
		schema.Types = map[string]*ir.TypeDef{}
	}
	for name, typeDef := range notes.Types {
		schema.Types[name] = typeDef
	}
	for _, set := range notes.OperationSets {
		if set.Name == "NoteMutations" {
			set.Operations = append(set.Operations, &ir.FieldDef{
				Name:       "touch",
				Comment:    "Touch a note; there is no result.",
				Arguments:  []*ir.ArgumentDef{{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true}},
				HTTPMethod: "POST",
				RestPath:   "notes/{id}/touch",
			})
		}
		schema.OperationSets = append(schema.OperationSets, set)
	}
	return schema
}

// TestRustSDKCallsTheRustServerWithQueryListsAndJSON serves the Rust
// router of query-lists-api with optional-json-api merged in
// (loadPostsAndNotes) and calls it through its Rust SDK
// (postsAndNotesRoundtripTest): list query parameters of an enum, a UUID,
// an integer scalar, strings and booleans arrive as the SDK sent them; an
// optional Generic.JSON body argument and input field keep null apart from
// absent, on a PUT, a DELETE and a POST; and an operation without a result
// answers null.
func TestRustSDKCallsTheRustServerWithQueryListsAndJSON(t *testing.T) {
	cargoTestRoundtrip(t, sdktest.QueryListsService, loadPostsAndNotes(t), "sdk_roundtrip", postsAndNotesRoundtripTest)
}

// postsAndNotesRoundtripTest is tests/sdk_roundtrip.rs of the merged API
// crate.
const postsAndNotesRoundtripTest = `use std::sync::{Arc, Mutex};

use API_CRATE::{
    build_router, Implementations, NoteAnnotateArgs, NoteImplementation, NoteRetractArgs, NoteReviseArgs, NoteTouchArgs,
    PostCountPostsArgs, PostImplementation,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use SDK_CRATE::namespaces::note::{AnnotateInput, RetractInput};
use SDK_CRATE::namespaces::post::CountPostsQueryParams;
use SDK_CRATE::{types, ClientConfig, QueryListsApiSdk};
use async_trait::async_trait;
use serde_json::{json, Value};

type Log = Arc<Mutex<Vec<String>>>;

struct Service {
    log: Log,
}

impl Service {
    fn record(&self, entry: String) {
        self.log.lock().unwrap().push(entry);
    }
}

#[async_trait]
impl PostImplementation for Service {
    async fn count_posts(&self, _ctx: RequestContext, args: PostCountPostsArgs) -> Result<f64, ApiError> {
        self.record(format!(
            "ids={:?} shades={:?} ranks={:?} codes={:?} tags={:?} flags={:?} limit={:?}",
            args.ids.iter().map(ToString::to_string).collect::<Vec<_>>(),
            args.shades.map(|shades| shades.iter().map(|shade| shade.as_str()).collect::<Vec<_>>()),
            args.ranks,
            args.codes,
            args.tags,
            args.flags,
            args.limit,
        ));
        Ok(7.0)
    }
}

// Optional extra: absent is None, null is Some(Value::Null).
fn extra(extra: &Option<Value>) -> String {
    match extra {
        None => "absent".to_string(),
        Some(value) => value.to_string(),
    }
}

#[async_trait]
impl NoteImplementation for Service {
    async fn annotate(&self, _ctx: RequestContext, args: NoteAnnotateArgs) -> Result<bool, ApiError> {
        self.record(format!("annotate {} {} {}", args.id, args.body, extra(&args.extra)));
        Ok(true)
    }
    async fn retract(&self, _ctx: RequestContext, args: NoteRetractArgs) -> Result<bool, ApiError> {
        self.record(format!("retract {} {} {}", args.id, args.body, extra(&args.extra)));
        Ok(true)
    }
    async fn revise(&self, _ctx: RequestContext, args: NoteReviseArgs) -> Result<bool, ApiError> {
        self.record(format!("revise {} {}", args.input.body, extra(&args.input.extra)));
        Ok(false)
    }
    async fn touch(&self, _ctx: RequestContext, args: NoteTouchArgs) -> Result<(), ApiError> {
        self.record(format!("touch {}", args.id));
        Ok(())
    }
}

` + serveRouter + `
async fn sdk(log: &Log) -> QueryListsApiSdk {
    let service = Arc::new(Service { log: log.clone() });
    let base_url = serve(Implementations { note: service.clone(), post: service }).await;
    QueryListsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn last(log: &Log) -> String {
    log.lock().unwrap().last().cloned().unwrap_or_default()
}

#[tokio::test]
async fn list_query_parameters_arrive_as_sent() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let id: types::IdentityUUID = "00000000-0000-4000-8000-000000000001".parse().unwrap();
    let query = CountPostsQueryParams {
        ids: vec![id],
        shades: Some(vec![types::Shade::Light, types::Shade::Dark]),
        ranks: Some(vec![1, 99]),
        codes: Some(vec!["ab".to_string(), "cde".to_string()]),
        tags: Some(vec!["x y".to_string()]),
        flags: Some(vec![true, false]),
        limit: Some(10.0),
    };
    assert_eq!(sdk.post.count_posts(Some(&query), None).await.unwrap(), 7.0);
    assert_eq!(
        last(&log),
        format!(r#"ids=["{id}"] shades=Some(["light", "dark"]) ranks=Some([1, 99]) codes=Some(["ab", "cde"]) tags=Some(["x y"]) flags=Some([true, false]) limit=Some(10.0)"#)
    );

    let query = CountPostsQueryParams { ids: vec![id], shades: None, ranks: None, codes: None, tags: None, flags: None, limit: None };
    sdk.post.count_posts(Some(&query), None).await.unwrap();
    assert_eq!(last(&log), format!(r#"ids=["{id}"] shades=None ranks=None codes=None tags=None flags=None limit=None"#));
}

#[tokio::test]
async fn an_optional_json_value_keeps_null_apart_from_absent() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let body = json!({"a": 1});
    sdk.note.annotate("n1".to_string(), AnnotateInput { body: body.clone(), extra: None }, None).await.unwrap();
    assert_eq!(last(&log), r#"annotate n1 {"a":1} absent"#);
    sdk.note.annotate("n1".to_string(), AnnotateInput { body: body.clone(), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(last(&log), r#"annotate n1 {"a":1} null"#);
    sdk.note.retract("n2".to_string(), RetractInput { body: json!([1, 2]), extra: Some(json!("why")) }, None).await.unwrap();
    assert_eq!(last(&log), r#"retract n2 [1,2] "why""#);
    sdk.note.retract("n2".to_string(), RetractInput { body: json!(false), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(last(&log), "retract n2 false null");

    assert!(!sdk.note.revise(types::NoteRevision { body: json!("text"), extra: None }, None).await.unwrap());
    assert_eq!(last(&log), r#"revise "text" absent"#);
    sdk.note.revise(types::NoteRevision { body: json!("text"), extra: Some(Value::Null) }, None).await.unwrap();
    assert_eq!(last(&log), r#"revise "text" null"#);
}

#[tokio::test]
async fn an_operation_without_a_result_answers_null() {
    let log = Log::default();
    let result = sdk(&log).await.note.touch("n3".to_string(), None).await.unwrap();
    assert_eq!(result, Value::Null);
    assert_eq!(last(&log), "touch n3");
}
`

// TestRustSDKCallsTheRustServerWithBodyArguments serves the Rust router of
// apigen's body-args-api and calls it through its Rust SDK
// (bodyArgsRoundtripTest): scalar, list, list-of-lists and map body
// arguments of primitives, scalars, enums, objects and JSON-valued scalars
// (Generic.JSON, Generic.StringMap, Embedding.Vector), on a PUT, a POST and
// a DELETE, and GET arguments of every scalar kind in the query, arrive at
// the implementation as the SDK sent them.
func TestRustSDKCallsTheRustServerWithBodyArguments(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join("..", "apigen", "testdata", "services", "body-args-api"))
	if err != nil {
		t.Fatalf("load body-args-api: %v", err)
	}
	cargoTestRoundtrip(t, "body-args-api", schema, "sdk_roundtrip", bodyArgsRoundtripTest)
}

// bodyArgsRoundtripTest is tests/sdk_roundtrip.rs of body-args-api's API
// crate. Each implementation records its arguments as JSON.
const bodyArgsRoundtripTest = `use std::collections::HashMap;
use std::sync::{Arc, Mutex};

use API_CRATE::{
    build_router, Implementations, TagFindTagsArgs, TagImplementation, TagNameShadesArgs, TagPinPointsArgs,
    TagPlacePointsArgs, TagRemoveTagsArgs, TagReviseDocumentArgs, TagSaveTagsArgs, TagSearchPostsArgs,
    TagSetFlagsArgs, TagStoreDocumentArgs, TagStoreEmbeddingArgs,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use SDK_CRATE::namespaces::tag::{
    FindTagsInput, FindTagsQueryParams, NameShadesInput, PinPointsInput, PlacePointsInput, RemoveTagsInput,
    SaveTagsInput, SearchPostsInput, SearchPostsQueryParams, SetFlagsInput, StoreDocumentInput, StoreEmbeddingInput,
};
use SDK_CRATE::{types, BodyArgsApiSdk, ClientConfig};
use async_trait::async_trait;
use serde_json::{json, Value};

type Log = Arc<Mutex<Vec<Value>>>;

struct Tags {
    log: Log,
}

impl Tags {
    fn record(&self, entry: Value) {
        self.log.lock().unwrap().push(entry);
    }
}

// An optional JSON value: absent, or the value null included.
fn kept(value: Option<Value>) -> Value {
    value.map_or(json!("absent"), |value| json!({"value": value}))
}

#[async_trait]
impl TagImplementation for Tags {
    async fn store_document(&self, _ctx: RequestContext, args: TagStoreDocumentArgs) -> Result<types::GenericJSON, ApiError> {
        self.record(json!({"document": args.document, "note": kept(args.note), "extras": args.extras, "grid": args.grid}));
        Ok(args.document)
    }
    async fn revise_document(&self, _ctx: RequestContext, args: TagReviseDocumentArgs) -> Result<types::GenericJSON, ApiError> {
        self.record(json!({"document": args.input.document, "note": args.input.note}));
        Ok(json!(true))
    }
    async fn store_embedding(&self, _ctx: RequestContext, args: TagStoreEmbeddingArgs) -> Result<types::GenericStringMap, ApiError> {
        self.record(json!({"vector": args.vector, "labelSets": args.label_sets, "vectorGrid": args.vector_grid}));
        Ok(args.labels)
    }
    async fn search_posts(&self, _ctx: RequestContext, args: TagSearchPostsArgs) -> Result<Vec<String>, ApiError> {
        self.record(json!({
            "tags": args.tags, "scores": args.scores, "ranks": args.ranks, "flags": args.flags,
            "related": args.related, "days": args.days, "codes": args.codes, "caption": args.caption,
            "limit": args.limit, "page": args.page, "pinned": args.pinned, "author": args.author, "since": args.since,
        }));
        Ok(vec!["found".to_string()])
    }
    async fn find_tags(&self, _ctx: RequestContext, args: TagFindTagsArgs) -> Result<Vec<String>, ApiError> {
        self.record(json!({"codes": args.codes, "pages": args.pages, "labels": args.labels, "ranks": args.ranks}));
        Ok(args.labels)
    }
    async fn set_flags(&self, _ctx: RequestContext, args: TagSetFlagsArgs) -> Result<bool, ApiError> {
        self.record(json!({
            "id": args.id, "pinned": args.pinned, "score": args.score, "caption": args.caption,
            "rank": args.rank, "related": args.related, "points": args.points,
        }));
        Ok(args.pinned)
    }
    async fn pin_points(&self, _ctx: RequestContext, args: TagPinPointsArgs) -> Result<bool, ApiError> {
        self.record(json!({"id": args.id, "grid": args.grid}));
        Ok(true)
    }
    async fn place_points(&self, _ctx: RequestContext, args: TagPlacePointsArgs) -> Result<bool, ApiError> {
        self.record(json!({"id": args.id, "pointByName": args.point_by_name}));
        Ok(true)
    }
    async fn name_shades(&self, _ctx: RequestContext, args: TagNameShadesArgs) -> Result<bool, ApiError> {
        self.record(json!({"id": args.id, "shadeByName": args.shade_by_name, "linksByLocale": args.links_by_locale}));
        Ok(true)
    }
    async fn remove_tags(&self, _ctx: RequestContext, args: TagRemoveTagsArgs) -> Result<Vec<String>, ApiError> {
        self.record(json!({
            "id": args.id, "labels": args.labels, "reason": args.reason,
            "requester": kept(args.requester), "shadeByLabel": args.shade_by_label,
        }));
        Ok(args.labels)
    }
    async fn save_tags(&self, _ctx: RequestContext, args: TagSaveTagsArgs) -> Result<Vec<String>, ApiError> {
        self.record(json!({
            "id": args.id, "labels": args.labels, "weights": args.weights, "ranks": args.ranks,
            "shades": args.shades, "links": args.links, "title": args.title, "priority": args.priority,
        }));
        Ok(args.labels)
    }
}

` + serveRouter + `
async fn sdk(log: &Log) -> BodyArgsApiSdk {
    let base_url = serve(Implementations { tag: Arc::new(Tags { log: log.clone() }) }).await;
    BodyArgsApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn last(log: &Log) -> Value {
    log.lock().unwrap().last().cloned().unwrap_or_default()
}

fn strings(items: &[&str]) -> Vec<String> {
    items.iter().map(|item| item.to_string()).collect()
}

const UUID: &str = "00000000-0000-4000-8000-000000000001";

#[tokio::test]
async fn scalar_and_list_body_arguments_arrive_as_sent() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let input = SaveTagsInput {
        labels: strings(&["a", "b,c"]),
        weights: Some(vec![0.5, 2.0]),
        ranks: Some(vec![3]),
        shades: Some(vec![types::Shade::Dark]),
        links: Some(vec!["https://example.com".to_string()]),
        title: Some("T".to_string()),
        priority: None,
    };
    assert_eq!(sdk.tag.save_tags("p1".to_string(), input, None).await.unwrap(), strings(&["a", "b,c"]));
    assert_eq!(
        last(&log),
        json!({"id": "p1", "labels": ["a", "b,c"], "weights": [0.5, 2.0], "ranks": [3], "shades": ["dark"],
               "links": ["https://example.com"], "title": "T", "priority": null})
    );

    let id: types::IdentityUUID = UUID.parse().unwrap();
    let input = SetFlagsInput {
        pinned: true,
        score: 9.5,
        caption: "hi".to_string(),
        rank: 4,
        related: Some(vec![id]),
        points: Some(vec![types::Point { x: 1.0, y: 2.0, pin_label: Some("pa".to_string()) }]),
    };
    assert!(sdk.tag.set_flags("p2".to_string(), input, None).await.unwrap());
    assert_eq!(
        last(&log),
        json!({"id": "p2", "pinned": true, "score": 9.5, "caption": "hi", "rank": 4, "related": [id.to_string()],
               "points": [{"x": 1.0, "y": 2.0, "pinLabel": "pa"}]})
    );

    let input = PinPointsInput { grid: vec![vec![types::Point { x: 0.0, y: 1.0, pin_label: None }], vec![]] };
    assert!(sdk.tag.pin_points("p3".to_string(), input, None).await.unwrap());
    assert_eq!(last(&log), json!({"id": "p3", "grid": [[{"x": 0.0, "y": 1.0}], []]}));
}

#[tokio::test]
async fn map_arguments_arrive_as_sent() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let input = NameShadesInput {
        shade_by_name: HashMap::from([("sky".to_string(), types::Shade::Light), ("sea".to_string(), types::Shade::Dark)]),
        links_by_locale: Some(HashMap::from([("en".to_string(), vec!["https://a.example".to_string()]), ("fr".to_string(), vec![])])),
    };
    assert!(sdk.tag.name_shades("p4".to_string(), input, None).await.unwrap());
    assert_eq!(
        last(&log),
        json!({"id": "p4", "shadeByName": {"sky": "light", "sea": "dark"}, "linksByLocale": {"en": ["https://a.example"], "fr": []}})
    );

    let input = PlacePointsInput { point_by_name: HashMap::from([("origin".to_string(), types::Point { x: 0.0, y: 0.0, pin_label: None })]) };
    assert!(sdk.tag.place_points("p5".to_string(), input, None).await.unwrap());
    assert_eq!(last(&log), json!({"id": "p5", "pointByName": {"origin": {"x": 0.0, "y": 0.0}}}));

    // A DELETE reads its arguments from the body too.
    let input = RemoveTagsInput {
        labels: strings(&["x"]),
        reason: Some("spam".to_string()),
        requester: Some(json!({"by": "ops"})),
        shade_by_label: Some(HashMap::from([("x".to_string(), types::Shade::Light)])),
    };
    assert_eq!(sdk.tag.remove_tags("p6".to_string(), input, None).await.unwrap(), strings(&["x"]));
    assert_eq!(
        last(&log),
        json!({"id": "p6", "labels": ["x"], "reason": "spam", "requester": {"value": {"by": "ops"}}, "shadeByLabel": {"x": "light"}})
    );
}

#[tokio::test]
async fn json_valued_arguments_arrive_as_sent() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let input = StoreDocumentInput {
        document: json!({"title": "t", "tags": [1, "two"]}),
        note: Some(Value::Null),
        extras: Some(vec![json!(1), json!("x")]),
        grid: Some(vec![vec![json!(true)], vec![]]),
    };
    assert_eq!(sdk.tag.store_document(input, None).await.unwrap(), json!({"title": "t", "tags": [1, "two"]}));
    assert_eq!(
        last(&log),
        json!({"document": {"title": "t", "tags": [1, "two"]}, "note": {"value": null}, "extras": [1, "x"], "grid": [[true], []]})
    );

    let input = StoreEmbeddingInput {
        labels: HashMap::from([("k".to_string(), "v".to_string())]),
        vector: Some(vec![0.25, 0.5]),
        label_sets: Some(vec![HashMap::new()]),
        vector_grid: Some(vec![vec![vec![1.0]], vec![]]),
    };
    let labels = sdk.tag.store_embedding(input, None).await.unwrap();
    assert_eq!(labels, HashMap::from([("k".to_string(), "v".to_string())]));
    assert_eq!(last(&log), json!({"vector": [0.25, 0.5], "labelSets": [{}], "vectorGrid": [[[1.0]], []]}));

    let revision = types::DocumentRevision { document: json!([1]), note: Some(Value::Null) };
    assert_eq!(sdk.tag.revise_document(revision, None).await.unwrap(), json!(true));
    assert_eq!(last(&log), json!({"document": [1], "note": null}));
}

#[tokio::test]
async fn get_arguments_of_every_kind_arrive_in_the_query() {
    let log = Log::default();
    let sdk = sdk(&log).await;
    let id: types::IdentityUUID = UUID.parse().unwrap();
    let since: types::TemporalDateTime = serde_json::from_value(json!("2026-01-02T03:04:05Z")).unwrap();
    let input = SearchPostsInput {
        scores: Some(vec![0.0, 10.0]),
        ranks: Some(vec![1, 2]),
        flags: Some(vec![false]),
        related: Some(vec![id]),
        days: Some(vec![since]),
        codes: Some(strings(&["ab", "cd"])),
        caption: Some("hey".to_string()),
        limit: Some(5.0),
        page: Some(2),
        pinned: Some(true),
        author: Some(id),
        since: Some(since),
    };
    let query = SearchPostsQueryParams { tags: Some(strings(&["aa", "bb"])) };
    assert_eq!(sdk.tag.search_posts(input, Some(&query), None).await.unwrap(), strings(&["found"]));
    assert_eq!(
        last(&log),
        json!({"tags": ["aa", "bb"], "scores": [0.0, 10.0], "ranks": [1, 2], "flags": [false], "related": [id.to_string()],
               "days": ["2026-01-02T03:04:05Z"], "codes": ["ab", "cd"], "caption": "hey", "limit": 5.0, "page": 2,
               "pinned": true, "author": id.to_string(), "since": "2026-01-02T03:04:05Z"})
    );

    let input = FindTagsInput { labels: strings(&["l1"]), ranks: None };
    let query = FindTagsQueryParams { codes: Some(strings(&["xy"])), pages: Some(vec![7]) };
    assert_eq!(sdk.tag.find_tags(input, Some(&query), None).await.unwrap(), strings(&["l1"]));
    assert_eq!(last(&log), json!({"codes": ["xy"], "pages": [7], "labels": ["l1"], "ranks": null}));
}
`
