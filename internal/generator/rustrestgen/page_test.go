package rustrestgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// pagedService is the core fixture whose lists page (D60): catalog.listItems
// pages ItemView two to a page, up to five, beside two query arguments, and
// catalog.listTags pages strings by the default limits with no argument.
const pagedService = "fixture-paged-api"

// TestWriteRustAPIPagedGolden pins the crate of fixture-paged-api. A paged
// operation's Args carry page, which the router reads after its query
// arguments by the Args' PAGE and check holds to it, and its method returns
// runtime::page::Page of the list's element; an operation that does not
// page is as before. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIPagedGolden -update
func TestWriteRustAPIPagedGolden(t *testing.T) {
	output := generateRustAPI(t, pagedService, false, "", nil)
	if !output.HasPages() {
		t.Fatal("HasPages is false for fixture-paged-api")
	}
	pages := map[string]string{}
	for _, endpoint := range output.Endpoints {
		if endpoint.Page != nil {
			pages[endpoint.Name] = endpoint.OutputRustType
			if !endpoint.HasArgs() {
				t.Errorf("%s pages but has no Args", endpoint.Name)
			}
		}
	}
	want := map[string]string{
		"listItems": "runtime::page::Page<types::ItemView>",
		"listTags":  "runtime::page::Page<String>",
	}
	if len(pages) != len(want) || pages["listItems"] != want["listItems"] || pages["listTags"] != want["listTags"] {
		t.Errorf("paged results = %v, want %v", pages, want)
	}

	generated := writeGoldenAPI(t, pagedService, output)
	for file, wants := range map[string][]string{
		"src/interfaces.rs": {
			"use crate::runtime;\n",
			"    pub page: runtime::page::PageRequest,\n}",
			"async fn list_items(&self, ctx: RequestContext, args: CatalogListItemsArgs) -> Result<runtime::page::Page<types::ItemView>, ApiError>;",
			"async fn list_tags(&self, ctx: RequestContext, args: CatalogListTagsArgs) -> Result<runtime::page::Page<String>, ApiError>;",
			"async fn get_item(&self, ctx: RequestContext, args: CatalogGetItemArgs) -> Result<types::ItemView, ApiError>;",
		},
		"src/router.rs": {
			"pub const PAGE: PageSpec = PageSpec::new(2, 5);",
			"pub const PAGE: PageSpec = PageSpec::new(20, 100);",
			"        page: CatalogListItemsArgs::PAGE.query(query)?,\n",
			"        Self::PAGE.check(&self.page)?;\n",
			"fn decode_catalog_list_tags(query: &QueryValues) -> Result<CatalogListTagsArgs, ApiError> {",
		},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s does not contain %q", file, want)
			}
		}
	}
}

// TestPagedScaffoldsNameTheRuntime: a paged method's scaffolds, the
// namespace's and the implementation crate's (D57), return the page by the
// API crate's runtime re-export, which they import; a namespace without a
// paged method does not.
func TestPagedScaffoldsNameTheRuntime(t *testing.T) {
	output := generateRustAPI(t, pagedService, false, "", nil)
	dir := t.TempDir()
	if _, err := WriteScaffolds(output, dir); err != nil {
		t.Fatal(err)
	}
	read := func(path string) string {
		t.Helper()
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(text)
	}
	const use = "use schemas_fixture_paged_api_api::runtime;\n"
	for path, wants := range map[string][]string{
		filepath.Join(dir, "catalog", "implementation.rs"): {use, "async fn list_items(&self, ctx: RequestContext, args: CatalogListItemsArgs) -> Result<runtime::page::Page<types::ItemView>, ApiError> {"},
		filepath.Join(dir, "catalog", "list_items.rs"):     {use, ") -> Result<runtime::page::Page<types::ItemView>, ApiError> {"},
		filepath.Join(dir, "catalog", "list_tags.rs"):      {use, "    _args: CatalogListTagsArgs,\n) -> Result<runtime::page::Page<String>, ApiError> {"},
	} {
		text := read(path)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s does not contain %q:\n%s", path, want, text)
			}
		}
	}
	if text := read(filepath.Join(dir, "catalog", "get_item.rs")); strings.Contains(text, "::runtime;") {
		t.Errorf("get_item.rs, which does not page, names the runtime:\n%s", text)
	}

	if err := output.SetDeps(DepsInfo{}); err != nil {
		t.Fatal(err)
	}
	implDir := t.TempDir()
	if _, err := WriteImplementationScaffold(output, implDir, t.TempDir(), naming.LocalPaths{HTTPRuntimeRust: "/repo/runtime/http/rust"}, naming.Default()); err != nil {
		t.Fatal(err)
	}
	lib := read(filepath.Join(implDir, ImplementationLib))
	for _, want := range []string{use, "-> Result<runtime::page::Page<types::ItemView>, ApiError> {"} {
		if !strings.Contains(lib, want) {
			t.Errorf("the implementation's lib.rs does not contain %q:\n%s", want, lib)
		}
	}
}

// TestPagedListServesAPageAtATime runs cargo test on fixture-paged-api's
// crate with pagedRouterTest, with the namespace's scaffolds and the
// implementation crate's lib.rs (D57) compiled beside it: the router
// serves five items two to a page, each page's next reading back as the
// cursor of the one after it and the last page's null; a limit past the
// maximum or below 1, and a cursor that is not of the alphabet, are the 400
// Args::check refuses the same arguments with; a cursor the implementation
// cannot read is the 400 "cursor is not one this operation returned"; and
// the in-process call, through the operation's OperationInfo and check,
// returns the page the router answers.
func TestPagedListServesAPageAtATime(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	cargoTestAPICrate(t, pagedService, schema, "paged", pagedRouterTest, func(apiDir string, output *APIOutput) error {
		if _, err := WriteScaffolds(output, filepath.Join(apiDir, "scaffolds")); err != nil {
			return err
		}
		// The implementation crate's lib.rs needs the crate's Deps.
		if err := output.SetDeps(DepsInfo{}); err != nil {
			return err
		}
		if err := WriteAPI(output, apiDir); err != nil {
			return err
		}
		_, err := WriteImplementationScaffold(output, filepath.Join(apiDir, "implementation"), apiDir, naming.LocalPaths{HTTPRuntimeRust: filepath.Join(apiDir, "runtime")}, naming.Default())
		return err
	})
}

// pagedRouterTest is tests/paged.rs of fixture-paged-api's crate, with
// API_CRATE and RUNTIME_CRATE replaced by the crates' module names. Its
// catalog holds five items in id order; a cursor is the last item's id.
const pagedRouterTest = `#[path = "../scaffolds/mod.rs"]
mod scaffolds;

// The implementation crate's lib.rs, whose Handlers hold what no test
// reads.
#[allow(dead_code)]
#[path = "../implementation/src/lib.rs"]
mod implementation;

use std::sync::Arc;

use API_CRATE::operations::{CATALOG_LIST_ITEMS, CATALOG_LIST_TAGS};
use API_CRATE::runtime::page::{refused_cursor, Cursor, Page, PageRequest};
use API_CRATE::{
    build_router, types, CatalogGetItemArgs, CatalogImplementation, CatalogListItemsArgs, CatalogListTagsArgs,
    CatalogPlaceBasketArgs, Deps, Implementations,
};
use RUNTIME_CRATE::{ApiError, RequestContext};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use serde_json::{json, Value};
use tower::ServiceExt;

const IDS: [&str; 5] = [
    "00000000-0000-4000-8000-000000000001",
    "00000000-0000-4000-8000-000000000002",
    "00000000-0000-4000-8000-000000000003",
    "00000000-0000-4000-8000-000000000004",
    "00000000-0000-4000-8000-000000000005",
];

fn item(index: usize) -> types::ItemView {
    serde_json::from_value(json!({
        "id": IDS[index],
        "name": format!("Item {}", index + 1),
        "sku": format!("item-{}", index + 1),
        "inStock": index.is_multiple_of(2),
    }))
    .unwrap()
}

struct Catalog;

#[async_trait]
impl CatalogImplementation for Catalog {
    async fn list_items(&self, _ctx: RequestContext, args: CatalogListItemsArgs) -> Result<Page<types::ItemView>, ApiError> {
        // The position is the last item's id; a cursor of another shape is
        // refused by decode, and one naming no item by the implementation.
        let after = match &args.page.cursor {
            None => None,
            Some(cursor) => {
                let id: String = cursor.decode()?;
                Some((0..IDS.len()).position(|index| item(index).id.to_string() == id).ok_or_else(refused_cursor)?)
            }
        };
        let rows: Vec<types::ItemView> = (0..IDS.len())
            .filter(|index| after.is_none_or(|after| *index > after))
            .map(item)
            .filter(|item| args.in_stock.is_none_or(|in_stock| item.in_stock == in_stock))
            .take(usize::try_from(args.page.limit).unwrap() + 1)
            .collect();
        Ok(Page::from_rows(rows, args.page.limit, |item| item.id.to_string()))
    }
    async fn get_item(&self, _ctx: RequestContext, _args: CatalogGetItemArgs) -> Result<types::ItemView, ApiError> {
        Ok(item(0))
    }
    async fn list_tags(&self, _ctx: RequestContext, args: CatalogListTagsArgs) -> Result<Page<String>, ApiError> {
        assert_eq!(args.page, PageRequest::first(20));
        Ok(Page::last(vec!["red".to_owned(), "blue".to_owned()]))
    }
    async fn place_basket(&self, _ctx: RequestContext, _args: CatalogPlaceBasketArgs) -> Result<types::BasketView, ApiError> {
        Err(ApiError::not_implemented("place_basket"))
    }
}

fn implementations() -> Implementations {
    Implementations { catalog: Arc::new(Catalog) }
}

async fn get(implementations: Implementations, uri: &str) -> (StatusCode, Value) {
    let request = Request::get(uri).body(Body::empty()).unwrap();
    let response = build_router(implementations).oneshot(request).await.unwrap();
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, serde_json::from_slice(&bytes).unwrap())
}

// The in-process call of catalog.listItems: the caller admitted by the
// route's rule (it needs none), the arguments checked, then the
// implementation called with the context the route would build.
async fn list_in_process(args: CatalogListItemsArgs) -> Result<Page<types::ItemView>, ApiError> {
    const { assert!(!CATALOG_LIST_ITEMS.requires_auth) };
    args.check()?;
    implementations().catalog.list_items(CATALOG_LIST_ITEMS.context(None), args).await
}

fn args(page: PageRequest) -> CatalogListItemsArgs {
    CatalogListItemsArgs { ids: None, in_stock: None, page }
}

// The ids of a page's items, as the wire writes them.
fn ids(page: &Value) -> Vec<Value> {
    page["items"].as_array().unwrap().iter().map(|item| item["id"].clone()).collect()
}

// The ids of the catalog's items from start to end, as the wire writes them.
fn wire_ids(range: std::ops::Range<usize>) -> Vec<Value> {
    range.map(|index| serde_json::to_value(item(index)).unwrap()["id"].clone()).collect()
}

// The problem the router answers, as status, code, detail and details,
// beside the same members of the in-process refusal.
fn same_refusal(status: StatusCode, body: &Value, refused: ApiError) {
    assert_eq!(status, StatusCode::BAD_REQUEST, "{body}");
    assert_eq!(refused.status, status);
    let refused = json!({"code": refused.code, "detail": refused.message, "details": refused.details.as_deref()});
    assert_eq!(refused, json!({"code": body["code"], "detail": body["detail"], "details": body["details"]}));
}

#[tokio::test]
async fn a_list_pages_by_its_cursor_to_the_last_page() {
    let (status, body) = get(implementations(), "/api/items").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let first = &body["data"];
    assert_eq!(ids(first), wire_ids(0..2));
    let next = first["next"].as_str().unwrap().to_owned();
    assert_eq!(Cursor::parse(&next).unwrap().decode::<String>().unwrap(), item(1).id.to_string());
    assert_eq!(body["meta"].as_object().unwrap().keys().collect::<Vec<_>>(), ["requestId"]);

    // The in-process call returns the page the router answers.
    let page = list_in_process(args(CatalogListItemsArgs::PAGE.first())).await.unwrap();
    assert_eq!(serde_json::to_value(&page).unwrap(), *first);

    let (status, body) = get(implementations(), &format!("/api/items?cursor={next}")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    let second = &body["data"];
    assert_eq!(ids(second), wire_ids(2..4));
    let page = list_in_process(args(PageRequest::after(page.next.unwrap(), 2))).await.unwrap();
    assert_eq!(serde_json::to_value(&page).unwrap(), *second);

    let last_cursor = second["next"].as_str().unwrap();
    let (status, body) = get(implementations(), &format!("/api/items?cursor={last_cursor}&limit=5")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"], json!({"items": [serde_json::to_value(item(4)).unwrap()], "next": null}));
    let page = list_in_process(args(PageRequest::after(page.next.unwrap(), 5))).await.unwrap();
    assert_eq!(page, Page::last(vec![item(4)]));

    // The whole list at the maximum, and a filter applied per page.
    let (_, body) = get(implementations(), "/api/items?limit=5").await;
    assert_eq!((ids(&body["data"]), &body["data"]["next"]), (wire_ids(0..5), &Value::Null));
    let (_, body) = get(implementations(), "/api/items?inStock=true&limit=1").await;
    assert_eq!(ids(&body["data"]), wire_ids(0..1));
    let next = body["data"]["next"].as_str().unwrap().to_owned();
    let (_, body) = get(implementations(), &format!("/api/items?inStock=true&limit=1&cursor={next}")).await;
    assert_eq!(ids(&body["data"]), wire_ids(2..3));

    // A page of a primitive, at the default limits, with no argument.
    let (status, body) = get(implementations(), "/api/tags").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert_eq!(body["data"], json!({"items": ["red", "blue"], "next": null}));
    assert_eq!(CatalogListTagsArgs::PAGE.first(), PageRequest::first(20));
    assert_eq!(CATALOG_LIST_TAGS.path, "/api/tags");
}

#[tokio::test]
async fn a_limit_out_of_bounds_is_refused_as_check_refuses_it() {
    let (status, body) = get(implementations(), "/api/items?limit=6").await;
    assert_eq!(body["detail"], "Invalid query parameter limit: must be at most 5");
    assert_eq!(body["details"]["parameter"], "limit");
    same_refusal(status, &body, args(PageRequest::first(6)).check().unwrap_err());
    same_refusal(status, &body, list_in_process(args(PageRequest::first(6))).await.unwrap_err());

    let (status, body) = get(implementations(), "/api/items?limit=0").await;
    same_refusal(status, &body, args(PageRequest::first(0)).check().unwrap_err());

    let (status, body) = get(implementations(), "/api/tags?limit=101").await;
    same_refusal(status, &body, CatalogListTagsArgs { page: PageRequest::first(101) }.check().unwrap_err());
    let (status, body) = get(implementations(), "/api/items?limit=2.5").await;
    assert_eq!((status, body["details"]["reason"].as_str()), (StatusCode::BAD_REQUEST, Some("expected an integer")));

    // A query argument is checked before the page, by the router and check.
    let six = [IDS[0]; 6].join(",");
    let (status, body) = get(implementations(), &format!("/api/items?ids={six}&limit=6")).await;
    assert_eq!(body["details"]["parameter"], "ids");
    let ids = vec![serde_json::from_value(json!(IDS[0])).unwrap(); 6];
    let refused = CatalogListItemsArgs { ids: Some(ids), in_stock: None, page: PageRequest::first(6) }.check().unwrap_err();
    same_refusal(status, &body, refused);
}

#[tokio::test]
async fn a_cursor_this_operation_did_not_return_is_refused() {
    // Of the alphabet, but JSON of another shape: the implementation's
    // decode refuses it, in a request and in-process alike.
    let (status, body) = get(implementations(), "/api/items?cursor=e30").await;
    assert_eq!(body["detail"], "cursor is not one this operation returned");
    assert_eq!(body["details"]["parameter"], "cursor");
    let forged = Cursor::parse("e30").unwrap();
    same_refusal(status, &body, list_in_process(args(PageRequest::after(forged, 2))).await.unwrap_err());

    // A key of the right type that names no item.
    let unknown = Cursor::encode("no such item");
    let (status, body) = get(implementations(), &format!("/api/items?cursor={unknown}")).await;
    same_refusal(status, &body, refused_cursor());

    // Not of the alphabet: the router's reader and check refuse it alike.
    let (status, body) = get(implementations(), "/api/items?cursor=e30%3D").await;
    assert_eq!(body["details"]["reason"], "does not match the required pattern");
    let padded: Cursor = serde_json::from_value(json!("e30=")).unwrap();
    same_refusal(status, &body, args(PageRequest::after(padded, 2)).check().unwrap_err());
}

#[tokio::test]
async fn the_scaffolds_return_the_page_type() {
    let scaffolded = Implementations { catalog: Arc::new(scaffolds::catalog::Implementation::new()) };
    let (status, body) = get(scaffolded, "/api/items").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");

    let deps = Deps { span: tracing::Span::none() };
    let handlers = implementation::create(deps).await.unwrap();
    let (status, body) = get(Implementations::new(handlers), "/api/tags").await;
    assert_eq!(status, StatusCode::NOT_IMPLEMENTED, "{body}");
}
`
