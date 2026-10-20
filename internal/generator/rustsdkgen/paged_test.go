package rustsdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// pagedService is the core fixture whose lists page by a cursor (D60).
const pagedService = "fixture-paged-api"

// pageVectors is the page vectors the runtimes are held to, which the
// crate test's stub server pages by.
const pageVectors = "../../../runtime/http/testdata/page_parity.json"

// loadPagedAPI loads fixture-paged-api: listItems pages ItemView two to a
// page, at most five, beside a list and a boolean query parameter;
// listTags pages strings with the default limits; getItem and placeBasket
// do not page. Each edit changes the schema before the API is generated.
func loadPagedAPI(t *testing.T, edits ...func(*ir.Schema) error) (*ir.Schema, *apigen.APIOutput) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, pagedService))
	if err != nil {
		t.Fatalf("load %s: %v", pagedService, err)
	}
	for _, edit := range edits {
		if err := edit(schema); err != nil {
			t.Fatal(err)
		}
	}
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:   sessionauth.Provider{},
		SchemaName: pagedService,
		Clock:      nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return schema, apiOutput
}

// addShelfTagsOperation adds catalog.listShelfTags to fixture-paged-api: a
// page of strings at a path value that is a number, beside a required
// query parameter, so its <method>_all copies the path value for each page
// and its query struct has no default.
func addShelfTagsOperation(schema *ir.Schema) error {
	for _, set := range schema.OperationSets {
		if set.Name != "Catalog" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:     "listShelfTags",
			Comment:  "The tags of a shelf that start with a prefix.",
			TypeRef:  ir.TypeRef{Name: "string", IsArray: true},
			Required: true,
			Arguments: []*ir.ArgumentDef{
				{Name: "shelf", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
				{Name: "prefix", TypeRef: ir.TypeRef{Name: "string"}, Required: true, IsQuery: true},
			},
			HTTPMethod: "GET",
			RestPath:   "shelves/{shelf}/tags",
			Page:       &ir.OperationPage{DefaultLimit: 2, MaxLimit: 5},
		})
		return nil
	}
	return fmt.Errorf("schema %s has no Catalog operation set", schema.Name)
}

// writePagedSDK writes the Rust SDK crate of fixture-paged-api to sdkDir
// against the types crate in typesDir.
func writePagedSDK(t *testing.T, apiOutput *apigen.APIOutput, sdkDir, typesDir string) *SDKOutput {
	t.Helper()
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(pagedService), naming.Default().RustTypesCrate(pagedService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	return sdkOutput
}

// TestPagedMethodsReturnAPageAndAPager: a paged operation's query struct
// ends with cursor and limit, checked by the bounds the route reads them
// with, its method returns runtime::Page<T> and <method>_all returns a
// Pager of T; an operation that does not page is unchanged. A sorted
// page's sort (D60, amended) is one of its own query fields, an optional
// String, before cursor and limit.
func TestPagedMethodsReturnAPageAndAPager(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sdkOutput.HasPagedEndpoints {
		t.Error("HasPagedEndpoints = false, want true")
	}
	endpoints := map[string]EndpointInfo{}
	for _, endpoint := range sdkOutput.Namespaces[0].Endpoints {
		endpoints[endpoint.MethodName] = endpoint
	}
	for _, tc := range []struct {
		method, output, item string
		params               []string
		maxLimit             float64
	}{
		{"list_items", "runtime::Page<types::ItemView>", "types::ItemView", []string{"ids", "inStock", "sort", "cursor", "limit"}, 5},
		{"list_tags", "runtime::Page<String>", "String", []string{"cursor", "limit"}, 100},
	} {
		endpoint := endpoints[tc.method]
		if endpoint.Page == nil || endpoint.OutputRustType != tc.output || endpoint.PageItemRustType != tc.item ||
			endpoint.AllMethodName != tc.method+"_all" || !endpoint.QueryDerivesDefault || endpoint.SupportsListAll {
			t.Errorf("%s: page %v, output %q, item %q, all %q, default %t, offset _all %t", tc.method, endpoint.Page,
				endpoint.OutputRustType, endpoint.PageItemRustType, endpoint.AllMethodName, endpoint.QueryDerivesDefault, endpoint.SupportsListAll)
		}
		var names []string
		for _, param := range endpoint.QueryParams {
			names = append(names, param.Name)
		}
		if strings.Join(names, ",") != strings.Join(tc.params, ",") {
			t.Errorf("%s query = %v, want %v", tc.method, names, tc.params)
			continue
		}
		cursor, limit := endpoint.QueryParams[len(names)-2], endpoint.QueryParams[len(names)-1]
		if cursor.RustType != "String" || cursor.Required || cursor.ValidateMinLength != nil || !cursor.EmptyIsAbsent ||
			*cursor.ValidateMaxLength != ir.PageCursorMaxLength || cursor.ValidatePattern != ir.PageCursorPattern {
			t.Errorf("%s cursor = %+v", tc.method, cursor)
		}
		if limit.RustType != "i64" || limit.Required || *limit.ValidateMin != 1 || *limit.ValidateMax != tc.maxLimit {
			t.Errorf("%s limit = %+v", tc.method, limit)
		}
	}
	if sort := endpoints["list_items"].QueryParams[2]; sort.RustType != "String" || sort.Required {
		t.Errorf("list_items sort = %+v", sort)
	}
	for _, method := range []string{"get_item", "place_basket"} {
		if endpoint := endpoints[method]; endpoint.Page != nil || endpoint.AllMethodName != "" || endpoint.HasQueryParams {
			t.Errorf("%s pages: %+v", method, endpoint)
		}
	}
}

// TestListAllPaginationSkipsPagedOperations: the offset _all, inferred from
// limit and offset arguments, never fires on a paged operation, which has
// its own _all following next, even beside arguments that would make it.
func TestListAllPaginationSkipsPagedOperations(t *testing.T) {
	endpoint := paginatedListEndpoint()
	endpoint.Page = &apigen.PageInfo{DefaultLimit: 20, MaxLimit: 100, Operation: "Taps.tenantConnectorTaps"}
	converted := convertEndpoint(endpoint, false, "", nil)
	if converted.SupportsListAll || converted.ListAllPageSize != 0 {
		t.Fatalf("a paged operation got the offset _all: %+v", converted)
	}
	if converted.AllMethodName != "tenant_connector_taps_all" {
		t.Fatalf("AllMethodName = %q", converted.AllMethodName)
	}
}

// TestWriteSDKGoldenPaged pins every file of the Rust SDK crate for
// fixture-paged-api: Page and Pager in src/runtime.rs and their export,
// the paged methods and their _all, and tool documents whose returns are
// the page. Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestWriteSDKGoldenPaged -update
func TestWriteSDKGoldenPaged(t *testing.T) {
	_, apiOutput := loadPagedAPI(t)
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "rust", pagedService)
	writePagedSDK(t, apiOutput, sdkDir, filepath.Join(root, "types", "rust", pagedService))
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", pagedService))
}

// TestPagedSDKCrateBuildsAndRuns generates the Rust types crate and the
// Rust SDK crate of fixture-paged-api, with listShelfTags
// (addShelfTagsOperation), into a temp tree laid out as a build writes it, checks the SDK crate with cargo clippy and runs cargo test on
// it with pagedSDKTest, against a stub server that pages as the runtimes
// do (D60), its cursors and pages held to page_parity.json, and sorts
// listItems (D60, amended), so a pager is seen keeping its sort.
// CARGO_TARGET_DIR is honored when set.
func TestPagedSDKCrateBuildsAndRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust SDK build")
	}
	paths := testpaths.Local(t)
	schema, apiOutput := loadPagedAPI(t, addShelfTagsOperation)

	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "rust", pagedService)
	sdkDir := filepath.Join(root, "sdk", "rust", pagedService)
	typesOutput, err := rustgen.Generate(schema, rustgen.Options{SchemaName: pagedService, Clock: nestedArraysClock})
	if err != nil {
		t.Fatalf("rustgen.Generate: %v", err)
	}
	if err := rustgen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("rustgen.WriteTypes: %v", err)
	}
	sdkOutput := writePagedSDK(t, apiOutput, sdkDir, typesDir)
	if sdkOutput.TypesCrate != typesOutput.CrateName {
		t.Fatalf("SDK types crate %q, types crate %q", sdkOutput.TypesCrate, typesOutput.CrateName)
	}
	vectors, err := filepath.Abs(pageVectors)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(vectors); err != nil {
		t.Fatal(err)
	}

	appendToFile(t, filepath.Join(sdkDir, "Cargo.toml"), `
[dev-dependencies]
tokio = { version = "1", features = ["macros", "rt"] }

`+testpaths.RustPatch(paths, naming.Default()))
	crate := strings.ReplaceAll(sdkOutput.CrateName, "-", "_")
	test := strings.NewReplacer("SDK_CRATE", crate, "VECTORS_PATH", filepath.ToSlash(vectors)).Replace(pagedSDKTest)
	writeFile(t, filepath.Join(sdkDir, "tests", "paged.rs"), test)

	cargoClippyAndTest(t, cargoPath, sdkDir)
}

// pagedSDKTest is tests/paged.rs of the generated SDK crate, with SDK_CRATE
// replaced by the crate's module name and VECTORS_PATH by the path of
// page_parity.json.
const pagedSDKTest = `use std::io::{BufRead, BufReader, Write};
use std::net::TcpListener;
use std::sync::{Arc, Mutex};
use std::thread;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use base64::Engine;
use serde_json::{json, Value};
use SDK_CRATE::errors::SDKError;
use SDK_CRATE::namespaces::catalog::{ListItemsQueryParams, ListShelfTagsQueryParams, ListTagsQueryParams};
use SDK_CRATE::types;
use SDK_CRATE::{ClientConfig, FixturePagedApiSdk, Page, Pager};

const VECTORS: &str = "VECTORS_PATH";
/// The base64url of text that is not JSON: a cursor the route reads and
/// the operation refuses.
const NOT_JSON: &str = "bm90IGpzb24";
const NAMES: [&str; 5] = ["Item 0", "Item 1", "Item 2", "Item 3", "Item 4"];
const TAGS: [&str; 3] = ["blue", "green", "red"];

/// A cursor as the runtimes write one: the unpadded base64url of the key's
/// compact JSON.
fn encode_cursor(key: &Value) -> String {
    URL_SAFE_NO_PAD.encode(serde_json::to_string(key).unwrap())
}

fn decode_cursor(cursor: &str) -> Option<Value> {
    serde_json::from_slice(&URL_SAFE_NO_PAD.decode(cursor).ok()?).ok()
}

/// Page::from_rows: limit of up to limit + 1 rows, next the key of the
/// last one kept when there were more.
fn from_rows(rows: &[Value], limit: usize, key: impl Fn(&Value) -> Value) -> Value {
    let items = &rows[..rows.len().min(limit)];
    let next = match items.last() {
        Some(last) if rows.len() > limit => json!(encode_cursor(&key(last))),
        _ => Value::Null,
    };
    json!({"items": items, "next": next})
}

fn vectors() -> Value {
    serde_json::from_str(&std::fs::read_to_string(VECTORS).unwrap()).unwrap()
}

fn item(n: usize) -> Value {
    json!({
        "id": format!("0b9a4e1c-6f2d-4c1a-9b7e-2d5f8a3c1e4{n}"),
        "name": NAMES[n],
        "sku": format!("item-{n}"),
        "inStock": n.is_multiple_of(2),
    })
}

fn key_of(item: &Value) -> Value {
    json!({"id": item["id"]})
}

type Requests = Arc<Mutex<Vec<(String, Vec<(String, String)>)>>>;

/// The stub: what it was asked, and the next it answers instead of its
/// own when set.
struct Stub {
    requests: Requests,
    next_override: Arc<Mutex<Option<String>>>,
}

impl Stub {
    fn take(&self) -> Vec<(String, Vec<(String, String)>)> {
        std::mem::take(&mut *self.requests.lock().unwrap())
    }

    fn count(&self) -> usize {
        self.requests.lock().unwrap().len()
    }
}

/// Pages /api/items five items, by the cursor of the last item's id, in the
/// order its sort asks (ascending by name or sku, which agree, or
/// descending after a -), /api/shelves/{shelf}/tags two tags of the
/// prefix, by the cursor of the
/// last tag, and /api/tags three tags, answering the cursor it was sent
/// after the first page. A cursor it cannot read is the operation's 400.
fn serve() -> (String, Stub) {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    let stub = Stub {
        requests: Arc::new(Mutex::new(Vec::new())),
        next_override: Arc::new(Mutex::new(None)),
    };
    let (requests, next_override) = (stub.requests.clone(), stub.next_override.clone());
    let refused = vectors()["refused"]
        .as_array()
        .unwrap()
        .iter()
        .find(|vector| vector["cursor"] == NOT_JSON)
        .unwrap()["problem"]
        .clone();
    thread::spawn(move || {
        let items: Vec<Value> = (0..NAMES.len()).map(item).collect();
        for stream in listener.incoming() {
            let mut stream = stream.unwrap();
            let mut reader = BufReader::new(stream.try_clone().unwrap());
            let mut request_line = String::new();
            reader.read_line(&mut request_line).unwrap();
            loop {
                let mut header = String::new();
                reader.read_line(&mut header).unwrap();
                if header.trim_end().is_empty() {
                    break;
                }
            }
            let target = request_line.split_whitespace().nth(1).unwrap_or_default().to_string();
            let url = reqwest::Url::parse(&format!("http://sdk.test{}", target)).unwrap();
            let query: Vec<(String, String)> = url.query_pairs().into_owned().collect();
            requests.lock().unwrap().push((url.path().to_string(), query.clone()));
            let param = |name: &str| query.iter().find(|(key, _)| key == name).map(|(_, value)| value.clone());
            let cursor = param("cursor");
            let (status, payload) = if url.path() == "/api/items" {
                let mut items = items.clone();
                if param("sort").is_some_and(|sort| sort.starts_with('-')) {
                    items.reverse();
                }
                let limit: usize = param("limit").map_or(2, |limit| limit.parse().unwrap());
                let start = match &cursor {
                    None => Some(0),
                    Some(cursor) => decode_cursor(cursor)
                        .and_then(|key| items.iter().position(|item| key_of(item) == key))
                        .map(|index| index + 1),
                };
                match start {
                    None => {
                        let mut problem = refused.clone();
                        problem["type"] = json!("about:blank");
                        problem["title"] = json!("Bad Request");
                        ("400 Bad Request", problem)
                    }
                    Some(start) => {
                        let end = items.len().min(start + limit + 1);
                        let mut page = from_rows(&items[start..end], limit, key_of);
                        if let Some(next) = next_override.lock().unwrap().clone() {
                            page["next"] = json!(next);
                        }
                        ("200 OK", json!({"data": page, "meta": {"requestId": "req-1"}}))
                    }
                }
            } else if url.path().starts_with("/api/shelves/") {
                let prefix = param("prefix").unwrap();
                let tags: Vec<Value> = (1..=2).map(|n| json!(format!("{prefix}-{n}"))).collect();
                let limit: usize = param("limit").map_or(2, |limit| limit.parse().unwrap());
                let start = cursor
                    .and_then(|cursor| decode_cursor(&cursor))
                    .and_then(|key| tags.iter().position(|tag| *tag == key))
                    .map_or(0, |index| index + 1);
                let page = from_rows(&tags[start..], limit, |tag| tag.clone());
                ("200 OK", json!({"data": page, "meta": {"requestId": "req-1"}}))
            } else {
                assert_eq!(url.path(), "/api/tags");
                let page = match &cursor {
                    None => json!({"items": TAGS[..2], "next": "MQ"}),
                    Some(cursor) => json!({"items": TAGS[2..], "next": cursor}),
                };
                ("200 OK", json!({"data": page, "meta": {"requestId": "req-1"}}))
            };
            let body = payload.to_string();
            write!(
                stream,
                "HTTP/1.1 {}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{}",
                status,
                body.len(),
                body
            )
            .unwrap();
        }
    });
    (base_url, stub)
}

fn sdk(base_url: String) -> FixturePagedApiSdk {
    FixturePagedApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap()
}

fn names(items: &[types::ItemView]) -> Vec<String> {
    items.iter().map(|item| item.name.clone()).collect()
}

fn pairs(items: &[(&str, &str)]) -> Vec<(String, String)> {
    items.iter().map(|(key, value)| (key.to_string(), value.to_string())).collect()
}

fn cursor_after(n: usize) -> String {
    encode_cursor(&key_of(&item(n)))
}

/// Collects what a pager yields: its items until an error or the end, and
/// the error.
async fn drain<T>(pager: &mut Pager<'_, T>) -> (Vec<T>, Option<SDKError>) {
    let mut items = Vec::new();
    while let Some(next) = pager.next().await {
        match next {
            Ok(item) => items.push(item),
            Err(err) => {
                assert!(pager.next().await.is_none(), "a pager yields nothing after an error");
                return (items, Some(err));
            }
        }
    }
    (items, None)
}

fn is_send<T: Send>(_: &T) {}

#[test]
fn the_stub_pages_as_the_runtimes_do() {
    let vectors = vectors();
    for vector in vectors["cursors"].as_array().unwrap() {
        let key: Value = serde_json::from_str(vector["keyJSON"].as_str().unwrap()).unwrap();
        assert_eq!(encode_cursor(&key), vector["cursor"].as_str().unwrap(), "{}", vector["name"]);
        assert_eq!(decode_cursor(vector["cursor"].as_str().unwrap()), Some(key), "{}", vector["name"]);
    }
    for vector in vectors["fromRows"].as_array().unwrap() {
        let rows = vector["rows"].as_array().unwrap();
        let limit = vector["limit"].as_u64().unwrap() as usize;
        assert_eq!(from_rows(rows, limit, |row| row.clone()), vector["page"], "{}", vector["name"]);
    }
}

#[tokio::test]
async fn a_page_its_next_and_the_last() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);

    // The first page: no cursor or limit sent, the route's default of 2.
    let first: Page<types::ItemView> = sdk.catalog.list_items(None, None).await.unwrap();
    assert_eq!(stub.take(), vec![("/api/items".to_string(), vec![])]);
    assert_eq!(names(&first.items), NAMES[..2]);
    assert_eq!(first.next, Some(cursor_after(1)));

    // The next is asked with the last page's next, beside the same filters.
    let query = ListItemsQueryParams {
        in_stock: Some(true),
        cursor: first.next.clone(),
        limit: Some(2),
        ..Default::default()
    };
    let second = sdk.catalog.list_items(Some(&query), None).await.unwrap();
    assert_eq!(
        stub.take(),
        vec![(
            "/api/items".to_string(),
            pairs(&[("inStock", "true"), ("cursor", &cursor_after(1)), ("limit", "2")])
        )]
    );
    assert_eq!(names(&second.items), NAMES[2..4]);

    // The last page's next is null.
    let query = ListItemsQueryParams {
        cursor: second.next.clone(),
        limit: Some(5),
        ..Default::default()
    };
    let last = sdk.catalog.list_items(Some(&query), None).await.unwrap();
    assert_eq!(names(&last.items), NAMES[4..]);
    assert_eq!(last.next, None);

    let tags = sdk.catalog.list_tags(None, None).await.unwrap();
    assert_eq!(tags, Page { items: vec!["blue".to_string(), "green".to_string()], next: Some("MQ".to_string()) });
}

#[tokio::test]
async fn the_pager_asks_for_each_page_when_its_items_are_reached() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    let query = ListItemsQueryParams {
        in_stock: Some(false),
        limit: Some(2),
        ..Default::default()
    };
    let mut pager = sdk.catalog.list_items_all(Some(&query), None);
    is_send(&pager);
    assert_eq!(stub.count(), 0, "a pager asks nothing until it is iterated");
    let mut taken = Vec::new();
    for asked in [1, 1, 2] {
        taken.push(pager.next().await.unwrap().unwrap());
        assert_eq!(stub.count(), asked);
    }
    let (rest, err) = drain(&mut pager).await;
    assert!(err.is_none(), "{:?}", err);
    taken.extend(rest);
    assert_eq!(names(&taken), NAMES);
    assert!(pager.next().await.is_none());
    let asked: Vec<_> = stub.take().into_iter().map(|(_, query)| query).collect();
    assert_eq!(
        asked,
        vec![
            pairs(&[("inStock", "false"), ("limit", "2")]),
            pairs(&[("inStock", "false"), ("cursor", &cursor_after(1)), ("limit", "2")]),
            pairs(&[("inStock", "false"), ("cursor", &cursor_after(3)), ("limit", "2")]),
        ]
    );

    // It starts from the cursor it is given, and needs no query.
    let query = ListItemsQueryParams {
        cursor: Some(cursor_after(3)),
        ..Default::default()
    };
    let (items, err) = drain(&mut sdk.catalog.list_items_all(Some(&query), None)).await;
    assert!(err.is_none(), "{:?}", err);
    assert_eq!(names(&items), NAMES[4..]);
    let (items, _) = drain(&mut sdk.catalog.list_items_all(None, None)).await;
    assert_eq!(names(&items), NAMES);
}

#[tokio::test]
async fn a_pager_keeps_its_sort() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    let query = ListItemsQueryParams {
        sort: Some("-name".to_string()),
        limit: Some(2),
        ..Default::default()
    };
    let (items, err) = drain(&mut sdk.catalog.list_items_all(Some(&query), None)).await;
    assert!(err.is_none(), "{:?}", err);
    let mut reversed = NAMES.to_vec();
    reversed.reverse();
    assert_eq!(names(&items), reversed);
    let asked: Vec<_> = stub.take().into_iter().map(|(_, query)| query).collect();
    assert_eq!(
        asked,
        vec![
            pairs(&[("sort", "-name"), ("limit", "2")]),
            pairs(&[("sort", "-name"), ("cursor", &cursor_after(3)), ("limit", "2")]),
            pairs(&[("sort", "-name"), ("cursor", &cursor_after(1)), ("limit", "2")]),
        ]
    );
}

#[tokio::test]
async fn an_error_ends_the_pager_where_it_is_reached() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    *stub.next_override.lock().unwrap() = Some(NOT_JSON.to_string());
    let (items, err) = drain(&mut sdk.catalog.list_items_all(None, None)).await;
    assert_eq!(names(&items), NAMES[..2]);
    match err {
        Some(SDKError::Api { status_code, message, code, .. }) => {
            assert_eq!(status_code, 400);
            assert_eq!(message, "cursor is not one this operation returned");
            assert_eq!(code.as_deref(), Some("bad_request"));
        }
        other => panic!("expected the refused cursor's 400, got {:?}", other),
    }
    assert_eq!(stub.count(), 2);
}

#[tokio::test]
async fn a_repeated_cursor_ends_the_pager_after_its_page() {
    let (base_url, _stub) = serve();
    let sdk = sdk(base_url);
    let (tags, err) = drain(&mut sdk.catalog.list_tags_all(Some(&ListTagsQueryParams::default()), None)).await;
    assert_eq!(tags, TAGS);
    match err {
        Some(SDKError::Contract(message)) => assert_eq!(message, "list_tags returned the cursor it was sent"),
        other => panic!("expected a contract error, got {:?}", other),
    }
}

#[tokio::test]
async fn a_pager_at_a_path_value_beside_a_required_query() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    let query = ListShelfTagsQueryParams {
        prefix: "new".to_string(),
        cursor: None,
        limit: Some(1),
    };
    let (tags, err) = drain(&mut sdk.catalog.list_shelf_tags_all(3.0, Some(&query), None)).await;
    assert!(err.is_none(), "{:?}", err);
    assert_eq!(tags, ["new-1", "new-2"]);
    assert_eq!(
        stub.take(),
        vec![
            ("/api/shelves/3/tags".to_string(), pairs(&[("prefix", "new"), ("limit", "1")])),
            (
                "/api/shelves/3/tags".to_string(),
                pairs(&[("prefix", "new"), ("cursor", &encode_cursor(&json!("new-1"))), ("limit", "1")])
            ),
        ]
    );

    // Without its required query, the pager yields the method's refusal.
    let (tags, err) = drain(&mut sdk.catalog.list_shelf_tags_all(3.0, None, None)).await;
    assert!(tags.is_empty());
    assert!(matches!(err, Some(SDKError::Config(message)) if message == "query parameters are required"));
    assert_eq!(stub.count(), 0);
}

#[tokio::test]
async fn an_empty_cursor_is_the_first_page() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    let query = ListItemsQueryParams {
        cursor: Some(String::new()),
        ..Default::default()
    };
    let first = sdk.catalog.list_items(Some(&query), None).await.unwrap();
    assert_eq!(names(&first.items), NAMES[..2]);
    assert_eq!(stub.take(), vec![("/api/items".to_string(), vec![])]);
    let (items, err) = drain(&mut sdk.catalog.list_items_all(Some(&query), None)).await;
    assert!(err.is_none(), "{:?}", err);
    assert_eq!(names(&items), NAMES);
}

#[tokio::test]
async fn a_cursor_or_limit_the_route_refuses_fails_before_the_request() {
    let (base_url, stub) = serve();
    let sdk = sdk(base_url);
    for (cursor, limit, message) in [
        (None, Some(0), "limit must be at least 1"),
        (None, Some(6), "limit must be at most 5"),
        (Some("e30="), None, "cursor has an invalid format"),
    ] {
        let query = ListItemsQueryParams {
            cursor: cursor.map(str::to_string),
            limit,
            ..Default::default()
        };
        match sdk.catalog.list_items(Some(&query), None).await {
            Err(SDKError::Config(got)) => assert_eq!(got, message),
            other => panic!("expected {:?}, got {:?}", message, other),
        }
        let (items, err) = drain(&mut sdk.catalog.list_items_all(Some(&query), None)).await;
        assert!(items.is_empty());
        assert!(matches!(err, Some(SDKError::Config(got)) if got == message));
    }
    let too_long = ListTagsQueryParams {
        cursor: Some("A".repeat(1025)),
        limit: None,
    };
    match sdk.catalog.list_tags(Some(&too_long), None).await {
        Err(SDKError::Config(got)) => assert_eq!(got, "cursor must be at most 1024 characters"),
        other => panic!("expected the cursor's length, got {:?}", other),
    }
    assert_eq!(stub.count(), 0, "a refused query must send no request");
}
`
