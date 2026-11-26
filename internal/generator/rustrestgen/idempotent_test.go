package rustrestgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/mcptools"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	idempotentAPI = "fixture-idempotent-api"
	idempotentDB  = "fixture-idempotent-db"
)

// idempotentFixture is fixture-idempotent-api, whose placeOrder and
// cancelOrder are @idempotent, the table fixture-idempotent-db, its
// database, declares for their keys, and that table's SQLite DDL as the
// SQL generator writes it (D140).
func idempotentFixture(t *testing.T) (*ir.Schema, *ir.IdempotencyTable, string) {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, idempotentAPI))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentAPI, err)
	}
	db, err := loader.LoadService(filepath.Join(fixturesDir, idempotentDB))
	if err != nil {
		t.Fatalf("load %s: %v", idempotentDB, err)
	}
	table, err := sqlutil.IdempotencyTableOf(db)
	if err != nil {
		t.Fatal(err)
	}
	model, err := sqlmigrate.BuildModel(db, sqlgen.Options{SchemaName: idempotentDB}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	return schema, table, ddl
}

// TestIdempotentRoutesPlan: each @idempotent route keeps its keys after
// the permission check and around its timeout, by its operation's name,
// ttl and required; the crate states the table's names as
// IDEMPOTENCY_TABLE; and Implementations holds the store, which
// Implementations::new builds over Deps.db, on Postgres or on a SQLite
// file (D140).
func TestIdempotentRoutesPlan(t *testing.T) {
	schema, table, _ := idempotentFixture(t)
	output, err := generateFrom(schema, apiSource{idempotencyTable: table}, Options{SchemaName: idempotentAPI, Clock: nestedArraysClock})
	if err != nil {
		t.Fatal(err)
	}
	if !output.HasIdempotent() || output.IdempotencyTable != table {
		t.Fatalf("HasIdempotent %t, table %+v", output.HasIdempotent(), output.IdempotencyTable)
	}
	calls := map[string]string{}
	for _, endpoint := range output.Endpoints {
		calls[endpoint.Name] = strings.Join(endpoint.ControlCalls("limits", "authenticator", "Arc::clone(&state.implementations.service_authenticator)"), "")
	}
	for name, want := range map[string]string{
		"placeOrder":  `.idempotent(Arc::clone(&state.implementations.idempotency_store), "orders.placeOrder", 3600, false).timeout_seconds(30)`,
		"cancelOrder": `.idempotent(Arc::clone(&state.implementations.idempotency_store), "orders.cancelOrder", 86400, true)`,
		"listOrders":  ``,
	} {
		if calls[name] != want {
			t.Errorf("%s's controls = %s, want %s", name, calls[name], want)
		}
	}
	if literal := output.IdempotencyTableLiteral(); !strings.HasPrefix(literal, "runtime::idempotency::Table {\n    table: \"idempotency_key\",\n    columns: runtime::idempotency::Columns {\n        lookup: \"lookup\",\n") ||
		!strings.Contains(literal, "        request_hash: \"request_hash\",\n") || !strings.HasSuffix(literal, "        expires_at: \"expires_at\",\n    },\n}") {
		t.Errorf("the table's literal:\n%s", literal)
	}
	if err := output.SetDeps(DepsInfo{}); err == nil || !strings.Contains(err.Error(), "@idempotent operations, whose keys its database keeps (D140), and its Deps names no database") {
		t.Errorf("Deps without a database: %v", err)
	}
	for _, sqlite := range []bool{false, true} {
		if err := output.SetDeps(DepsInfo{Database: idempotentDB, SQLite: sqlite}); err != nil {
			t.Fatal(err)
		}
		want := "superschematic_http_runtime::idempotency::PostgresStore"
		if sqlite {
			want = "superschematic_http_runtime::idempotency::SqliteStore"
		}
		if got := output.IdempotencyStoreType(); got != want {
			t.Errorf("sqlite %t: the store is %s, want %s", sqlite, got, want)
		}
		if signature := output.NewSignature(); !strings.Contains(signature, "db: "+output.DatabaseType()) {
			t.Errorf("sqlite %t: Implementations::new takes no db:\n%s", sqlite, signature)
		}
	}
	// An MCP check (D98) keeps no key: the run's call does.
	for _, endpoint := range output.Endpoints {
		if strings.Contains(strings.Join(endpoint.CheckControlCalls("authenticator", "Arc::clone(&state.implementations.service_authenticator)"), ""), "idempotent") {
			t.Errorf("%s's check keeps a key", endpoint.Name)
		}
	}
	// The Rust tool table marks each @idempotent operation's tool, so the
	// endpoint takes the call's key in an argument (D140).
	info := &MCPInfo{Endpoint: &mcptools.Endpoint{Tools: []mcptools.Tool{
		{Name: "place_order", Operation: "orders.placeOrder", Arguments: []mcptools.ToolArgument{}},
		{Name: "cancel_order", Operation: "orders.cancelOrder", Arguments: []mcptools.ToolArgument{}},
		{Name: "list_orders", Operation: "orders.listOrders", Arguments: []mcptools.ToolArgument{}},
	}}, Idempotent: map[string]*ir.IdempotentConfig{}}
	for _, endpoint := range output.Endpoints {
		if endpoint.Idempotent != nil {
			info.Idempotent[endpoint.OperationKey()] = endpoint.Idempotent
		}
	}
	literal, err := info.ToolsRust()
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name       string
		Idempotent *struct{ Required bool }
	}
	if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimPrefix(literal, `r#"`), `"#`)), &tools); err != nil {
		t.Fatalf("the table is no JSON: %v\n%s", err, literal)
	}
	if len(tools) != 3 || tools[0].Idempotent == nil || tools[0].Idempotent.Required || tools[1].Idempotent == nil || !tools[1].Idempotent.Required || tools[2].Idempotent != nil {
		t.Errorf("the table's idempotent tools:\n%s", literal)
	}
}

// TestIdempotentRoutesEndToEnd runs cargo test on the Rust API crate of
// fixture-idempotent-api beside its Rust SDK (idempotentRouterTest), with
// its keys in a SQLite file made from the generated DDL of
// fixture-idempotent-db, through the runtime's SQLite store over
// IDEMPOTENCY_TABLE: each @idempotent route runs its implementation once
// per key and answers a repeat what the first request was, the SDK sends
// one key a call, its own or the one its options name, and a refusal of a
// key is the problem the Go and TypeScript routers answer (D140).
func TestIdempotentRoutesEndToEnd(t *testing.T) {
	schema, table, ddl := idempotentFixture(t)
	paths := testpaths.Local(t)
	runtime := naming.Default().HTTPRuntimeRustCrate + " = { path = " + strconv.Quote(filepath.ToSlash(paths.HTTPRuntimeRust)) + `, features = ["sqlite"] }`
	writeDDL := func(apiDir string, _ *APIOutput) error {
		return os.WriteFile(filepath.Join(apiDir, "idempotency.sql"), []byte(ddl), 0o644)
	}
	crate := strings.ReplaceAll(naming.Default().RustSDKCrate(idempotentAPI), "-", "_")
	cargoTestAPICrateWith(t, idempotentAPI, schema, "idempotent", strings.ReplaceAll(idempotentRouterTest, "SDK_CRATE", crate),
		cargoOptions{idempotencyTable: table, devDependencies: runtime},
		writeDDL, withSDKOf(schema, idempotentAPI, apiSource{idempotencyTable: table}))
}

// idempotentRouterTest is tests/idempotent.rs of fixture-idempotent-api's
// API crate.
const idempotentRouterTest = `use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};

use API_CRATE::{
    build_router, types, Implementations, OrdersCancelOrderArgs, OrdersImplementation, OrdersPlaceOrderArgs,
    IDEMPOTENCY_TABLE,
};
use RUNTIME_CRATE::idempotency::{SqliteStore, Store};
use RUNTIME_CRATE::orm::sqlite::{rusqlite, Database};
use RUNTIME_CRATE::{ApiError, RequestContext};
use SDK_CRATE::types::PlaceOrderInput;
use SDK_CRATE::{ClientConfig, FixtureIdempotentApiSdk, RequestOptions};
use async_trait::async_trait;
use axum::body::{to_bytes, Body};
use axum::http::{Request, StatusCode};
use axum::Router;
use serde_json::Value;
use tower::ServiceExt;

#[derive(Default)]
struct Orders {
    placed: Mutex<Vec<types::Order>>,
    runs: AtomicUsize,
}

#[async_trait]
impl OrdersImplementation for Orders {
    async fn place_order(&self, _ctx: RequestContext, args: OrdersPlaceOrderArgs) -> Result<types::Order, ApiError> {
        self.runs.fetch_add(1, Ordering::SeqCst);
        let mut placed = self.placed.lock().unwrap();
        let order = types::Order {
            number: (placed.len() + 1) as f64,
            sku: args.input.sku,
            quantity: args.input.quantity,
            cancelled: false,
        };
        placed.push(order.clone());
        Ok(order)
    }

    async fn cancel_order(&self, _ctx: RequestContext, args: OrdersCancelOrderArgs) -> Result<types::Order, ApiError> {
        self.runs.fetch_add(1, Ordering::SeqCst);
        let mut placed = self.placed.lock().unwrap();
        let Some(order) = placed.iter_mut().find(|order| order.number == args.number) else {
            return Err(ApiError::not_found("no such order"));
        };
        order.cancelled = true;
        Ok(order.clone())
    }

    async fn list_orders(&self, _ctx: RequestContext) -> Result<Vec<types::Order>, ApiError> {
        Ok(self.placed.lock().unwrap().clone())
    }
}

/// The keys' SQLite file, made from the generated DDL, and the runtime's
/// store over it on the table the crate names.
fn store() -> (Arc<Database>, Arc<dyn Store>) {
    let dir = std::env::temp_dir().join(format!("idempotent-{}-{:?}", std::process::id(), std::thread::current().id()));
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.join("keys.sqlite");
    let _ = std::fs::remove_file(&path);
    let ddl = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/idempotency.sql")).unwrap();
    rusqlite::Connection::open(&path).and_then(|connection| connection.execute_batch(&ddl)).unwrap();
    let db = Arc::new(Database::open(&path).unwrap());
    (Arc::clone(&db), Arc::new(SqliteStore::new(db, &IDEMPOTENCY_TABLE)))
}

fn router(orders: Arc<Orders>, store: Arc<dyn Store>) -> Router {
    build_router(Implementations { orders, idempotency_store: store })
}

async fn send(router: &Router, method: &str, uri: &str, key: Option<&str>, body: &str) -> (StatusCode, bool, Value) {
    let mut request = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some(key) = key {
        request = request.header("idempotency-key", key);
    }
    let response = router.clone().oneshot(request.body(Body::from(body.to_string())).unwrap()).await.unwrap();
    let status = response.status();
    let replay = response.headers().get("idempotent-replay").is_some_and(|value| value == "true");
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, replay, serde_json::from_slice(&bytes).unwrap_or(Value::Null))
}

#[tokio::test(flavor = "multi_thread")]
async fn a_repeated_call_answers_the_first_calls_answer() {
    let orders = Arc::new(Orders::default());
    let (db, store) = store();
    let router = router(Arc::clone(&orders), store);
    let order = r#"{"sku": "tea", "quantity": 2}"#;

    let (status, replay, first) = send(&router, "POST", "/api/orders", Some("order-7"), order).await;
    assert_eq!((status, replay), (StatusCode::OK, false), "{first}");
    assert_eq!(first["data"]["number"], 1.0);
    let (status, replay, again) = send(&router, "POST", "/api/orders", Some("order-7"), order).await;
    assert_eq!((status, replay), (StatusCode::OK, true), "{again}");
    assert_eq!(again, first);
    assert_eq!(orders.runs.load(Ordering::SeqCst), 1);

    // The key with another body is another request: 422.
    let (status, _, reused) = send(&router, "POST", "/api/orders", Some("order-7"), r#"{"sku": "tea", "quantity": 3}"#).await;
    assert_eq!((status, reused["code"].as_str()), (StatusCode::UNPROCESSABLE_ENTITY, Some("idempotency_key_reused")));
    // Without a key placeOrder runs each time; cancelOrder requires one.
    let (_, replay, second) = send(&router, "POST", "/api/orders", None, order).await;
    assert_eq!((replay, &second["data"]["number"]), (false, &Value::from(2.0)));
    let (status, _, refused) = send(&router, "DELETE", "/api/orders/1", None, "").await;
    assert_eq!((status, refused["code"].as_str()), (StatusCode::BAD_REQUEST, Some("idempotency_key_required")));
    let long = "k".repeat(256);
    let (status, _, refused) = send(&router, "DELETE", "/api/orders/1", Some(&long), "").await;
    assert_eq!(status, StatusCode::BAD_REQUEST, "{refused}");
    assert_eq!(orders.runs.load(Ordering::SeqCst), 2);

    // A refusal under 500 is kept and replayed: the order does not exist.
    let (status, replay, missing) = send(&router, "DELETE", "/api/orders/9", Some("cancel-9"), "").await;
    assert_eq!((status, replay), (StatusCode::NOT_FOUND, false), "{missing}");
    let (status, replay, _) = send(&router, "DELETE", "/api/orders/9", Some("cancel-9"), "").await;
    assert_eq!((status, replay), (StatusCode::NOT_FOUND, true));
    assert_eq!(orders.runs.load(Ordering::SeqCst), 3);

    // The keys are rows of the generated table: placeOrder's and cancelOrder's.
    let rows: Vec<(String, String, f64)> = db
        .read(|connection| -> Result<_, RUNTIME_CRATE::BoxError> {
            let mut statement = connection.prepare("SELECT operation, state, status FROM idempotency_key ORDER BY operation")?;
            let rows = statement
                .query_map([], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)))?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .unwrap();
    assert_eq!(
        rows,
        vec![
            ("orders.cancelOrder".to_string(), "done".to_string(), 404.0),
            ("orders.placeOrder".to_string(), "done".to_string(), 200.0),
        ]
    );
}

async fn serve(router: Router) -> String {
    let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
    let base_url = format!("http://{}", listener.local_addr().unwrap());
    tokio::spawn(async move { axum::serve(listener, router).await.unwrap() });
    base_url
}

#[tokio::test(flavor = "multi_thread")]
async fn the_sdk_sends_one_key_a_call() {
    let orders = Arc::new(Orders::default());
    let (db, store) = store();
    let base_url = serve(router(Arc::clone(&orders), store)).await;
    let sdk = FixtureIdempotentApiSdk::new(ClientConfig::with_base_url(base_url, None, None)).unwrap();
    let input = || PlaceOrderInput { sku: "tea".to_string(), quantity: 2.0 };

    // The caller's key: a repeat of the call is answered the first order.
    let keyed = RequestOptions { idempotency_key: Some("order-1".to_string()), ..RequestOptions::default() };
    let first = sdk.orders.place_order(input(), Some(&keyed)).await.unwrap();
    let again = sdk.orders.place_order(input(), Some(&keyed)).await.unwrap();
    assert_eq!((first.number, again.number), (1.0, 1.0));
    assert_eq!(orders.runs.load(Ordering::SeqCst), 1);

    // Without one, each call makes its own: two calls are two orders.
    let second = sdk.orders.place_order(input(), None).await.unwrap();
    let third = sdk.orders.place_order(input(), None).await.unwrap();
    assert_eq!((second.number, third.number), (2.0, 3.0));
    // cancelOrder requires a key, which the SDK always sends.
    let cancelled = sdk.orders.cancel_order(2.0, None).await.unwrap();
    assert!(cancelled.cancelled);
    // listOrders is not @idempotent: it sends none, and keeps no row.
    assert_eq!(sdk.orders.list_orders(Some(&keyed)).await.unwrap().len(), 3);

    let keys: Vec<(String, String)> = db
        .read(|connection| -> Result<_, RUNTIME_CRATE::BoxError> {
            let mut statement = connection.prepare("SELECT operation, key FROM idempotency_key ORDER BY operation, key")?;
            let rows = statement
                .query_map([], |row| Ok((row.get(0)?, row.get(1)?)))?
                .collect::<Result<Vec<_>, _>>()?;
            Ok(rows)
        })
        .unwrap();
    assert_eq!(keys.len(), 4, "{keys:?}");
    assert!(keys.contains(&("orders.placeOrder".to_string(), "order-1".to_string())), "{keys:?}");
    let uuid = |key: &str| {
        key.len() == 36 && key.as_bytes()[14] == b'4' && key.split('-').map(str::len).collect::<Vec<_>>() == [8, 4, 4, 4, 12]
    };
    assert_eq!(keys.iter().filter(|(_, key)| uuid(key)).count(), 3, "{keys:?}");
}
`
