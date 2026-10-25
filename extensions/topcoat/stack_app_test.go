package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The extension's fixtures of a stack app (D78): fixture-app-api, whose
// users are the core user model's in its authDb, fixture-app-db, which is
// an engine database too, as acme's shop-db is, and which declares the app
// Backoffice.
const (
	appService  = "fixture-app-api"
	appDatabase = "fixture-app-db"
)

// TestTheCrateServesAStackApp builds fixture-app-api's crates with the
// pages on: the API crate declares apps::BackofficeParts, and the Topcoat
// crate's fixture_app_api_with_parts takes them, converted into AppParts.
// Then cargo clippy and cargo test run appPartsTest: the program's serve
// binds parts.addr and serves the router fixture_app_api_with_parts builds
// there; a user the mounted JSON API signs in is the caller of the pages,
// through the identity page authenticator over the parts'
// implementations; and the engine pages read the engine the parts hold,
// which a note created through the engine's routes is in.
func TestTheCrateServesAStackApp(t *testing.T) {
	root := testpaths.TempDir(t)
	if _, err := buildService(t, appDatabase, root, engineDatabaseOutputs()); err != nil {
		t.Fatalf("build %s: %v", appDatabase, err)
	}
	if _, err := buildService(t, appService, root, pagesOutputs()); err != nil {
		t.Fatalf("build %s: %v", appService, err)
	}
	lib, err := os.ReadFile(filepath.Join(topcoat.Dir(root, appService), "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"fn fixture_app_api_with_parts(self, parts: impl Into<AppParts>) -> Self;",
		"let page_authenticator = IdentityPageAuthenticator::of(&parts.implementations);",
		"set_up(self, parts.implementations, Arc::new(page_authenticator), parts.engine, true)",
		"impl From<api::apps::BackofficeParts> for AppParts {",
	} {
		if !strings.Contains(string(lib), want) {
			t.Errorf("lib.rs lacks %s:\n%s", want, lib)
		}
	}
	// An API that declares no app gets no method of it.
	if _, err := buildService(t, engineDatabase, root, engineDatabaseOutputs()); err != nil {
		t.Fatalf("build %s: %v", engineDatabase, err)
	}
	if _, err := buildService(t, engineService, root, pagesOutputs()); err != nil {
		t.Fatalf("build %s: %v", engineService, err)
	}
	other, err := os.ReadFile(filepath.Join(topcoat.Dir(root, engineService), "src", "lib.rs"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(other), "_with_parts") || strings.Contains(string(other), "AppParts") {
		t.Errorf("the crate of %s, which declares no app, has a method of one:\n%s", engineService, other)
	}

	ddl, err := os.ReadFile("../../runtime/http/testdata/identity/sqlite/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	cargoTestCrateWith(t, appService, appPartsTest, crateOptions{
		outputs:  pagesOutputs(),
		features: []string{"identity-sqlite"},
		devDependencies: `rusqlite = { version = "0.40.2", features = ["bundled"] }
tracing = "0.1.41"
`,
		files:  map[string]string{"identity.sql": string(ddl)},
		before: map[string]map[string]any{appDatabase: engineDatabaseOutputs()},
	})
}

// appPartsTest is tests/app.rs of fixture-app-api's Topcoat crate: the
// parts an app's entrypoint builds, as it builds them on a stack, but for
// the identity store, on SQLite, and a pool that never connects; and the
// app's program, serve, as a project writes it.
const appPartsTest = `use std::io::{Read, Write};
use std::net::{SocketAddr, TcpListener as StdListener, TcpStream};
use std::sync::Arc;
use std::time::SystemTime;

use async_trait::async_trait;
use schemas_fixture_app_api_topcoat::api::apps::BackofficeParts;
use schemas_fixture_app_api_topcoat::api::config::EnvConfig;
use schemas_fixture_app_api_topcoat::api::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Rusqlite, SqlStore, Store,
};
use schemas_fixture_app_api_topcoat::api::runtime::stackconfig::{Cors, Database};
use schemas_fixture_app_api_topcoat::api::runtime::{postgres, ApiError, RequestContext};
use schemas_fixture_app_api_topcoat::api::{identity, types, Deps, Handlers, Implementations, NotesImplementation};
use schemas_fixture_app_api_topcoat::{engine, RouterBuilderFixtureAppApiExt};
use serde_json::json;
use superschematic_engine::background::{Background, BackgroundOptions};
use superschematic_engine::EngineOptions;
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
const PASSWORD: &str = "correct horse";

struct Notes;

#[async_trait]
impl NotesImplementation for Notes {
    async fn note_count(&self, _ctx: RequestContext) -> Result<types::NoteCount, ApiError> {
        Ok(types::NoteCount { count: 0.0 })
    }

    async fn list_notes(&self, _ctx: RequestContext) -> Result<Vec<types::NoteTitle>, ApiError> {
        Ok(Vec::new())
    }
}

// The program of app Backoffice, as a project writes it: it binds the
// address its parts name, then serves the router the crate sets up from
// them.
async fn serve(parts: BackofficeParts) -> std::io::Result<()> {
    let listener = tokio::net::TcpListener::bind(parts.addr).await?;
    let router = Router::builder().discover().fixture_app_api_with_parts(parts).build();
    topcoat::serve(listener, router).await
}

fn temp(name: &str) -> std::path::PathBuf {
    let path = std::env::temp_dir().join(format!("topcoat-app-{}-{:?}-{name}", std::process::id(), std::thread::current().id()));
    let _ = std::fs::remove_file(&path);
    path
}

// The parts of Backoffice on addr, as its entrypoint builds them: the
// API's Deps, its implementations with the identity runtime's
// authenticator, over users Ada, who holds Note, and Grace, who holds
// nothing, and the engine of fixture-app-db, open, its document deployed.
async fn parts(addr: SocketAddr) -> BackofficeParts {
    let connection = rusqlite::Connection::open_in_memory().unwrap();
    connection.execute_batch(include_str!("identity.sql")).unwrap();
    let store: Arc<SqlStore> = Arc::new(identity::store(Rusqlite::new(connection).unwrap()).unwrap());
    let config = Config::parse(CONFIG.as_bytes()).unwrap();
    let hash = hash_password(PASSWORD, config.argon2_params()).unwrap();
    let user = |login: &str, name: &str| NewUser {
        login: login.to_string(),
        name: name.to_string(),
        password_hash: hash.clone(),
        at: SystemTime::now(),
    };
    let ada = store.create_user(user("ada@example.com", "Ada")).await.unwrap();
    store.create_user(user("grace@example.com", "Grace")).await.unwrap();
    let role = store.create_role("notes", &["Note".to_string()]).await.unwrap();
    store.grant_role(&ada.id, &role.id, SystemTime::now()).await.unwrap();
    let service = Arc::new(identity::service(store, config.clone()).unwrap());

    let env = EnvConfig {
        // A pool connects when it is first used, and nothing here uses it.
        fixture_app_db_database: Database::Url("postgres://app@127.0.0.1:1/never".to_string()),
        fixture_app_api_cors: Cors::default(),
        fixture_app_api_identity: config,
    };
    let db = postgres::connect(&env.fixture_app_db_database).unwrap();
    // The Deps' engine opens in the background, on a file of its own: the
    // pages call the one the parts hold.
    let background_path = temp("background.db").to_string_lossy().into_owned();
    let background = Background::open(
        move || EngineOptions::from_json(&json!({ "path": background_path })),
        BackgroundOptions::default(),
    );
    let deps = Deps { config: env, db, engine: background, span: tracing::Span::none() };
    let handlers = Handlers { notes: Arc::new(Notes) };
    let implementations = Implementations::new(handlers, Arc::new(IdentityAuthenticator::new(service)));
    let opened = tokio::task::spawn_blocking(|| engine::open(temp("engine.db")).expect("open the engine")).await.unwrap();
    BackofficeParts { deps, implementations, engine: opened, addr }
}

struct Reply {
    status: StatusCode,
    cookies: Vec<String>,
    body: String,
}

async fn send(router: &Router, method: &str, uri: &str, headers: &[(&str, &str)], body: Option<serde_json::Value>) -> Reply {
    let mut request = http::Request::builder().method(method).uri(uri).header("host", "app.example.com");
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    let body = body.map_or_else(Body::empty, |body| Body::from(body.to_string()));
    let response = router.handle(request.body(body).unwrap()).await;
    let (status, response_headers) = (response.status(), response.headers().clone());
    let cookies = response_headers.get_all("set-cookie").iter().map(|value| value.to_str().unwrap().to_string()).collect();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    if response_headers.get("content-type").is_some_and(|value| value.to_str().unwrap().starts_with("text/html")) {
        enter::check_page(uri, &response_headers, &body);
    }
    Reply { status, cookies, body }
}

// Signs in through the mounted JSON API with a cookie session, and answers
// the Cookie header that carries it.
async fn sign_in(router: &Router, login: &str) -> String {
    let credentials = json!({"login": login, "password": PASSWORD, "session": "cookie"});
    let reply = send(router, "POST", "/api/auth/login", &[("sec-fetch-site", "same-origin")], Some(credentials)).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    reply.cookies.first().expect("the session cookie").split(';').next().unwrap().to_string()
}

const NOTES: &str = "/engine/namespaces/fixture-app-db/schemas/Note/instances";

#[tokio::test(flavor = "multi_thread")]
async fn the_parts_set_up_the_pages_the_api_and_the_engine() {
    let parts = parts(SocketAddr::from(([127, 0, 0, 1], 0))).await;
    let engine = Arc::clone(&parts.engine);
    let router = Router::builder().discover().fixture_app_api_with_parts(parts).build();

    // The pages ask for a caller, whom the API's session gives them.
    let reply = send(&router, "GET", "/console/engine", &[], None).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    let ada = sign_in(&router, "ada@example.com").await;
    let reply = send(&router, "GET", "/console/engine", &[("cookie", &ada)], None).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("<a href=\"/console/engine/Note\">Notes</a>"), "{}", reply.body);

    // A note created through the engine's routes, as Ada, is in the engine
    // the parts hold, and its page shows it.
    let reply = send(
        &router,
        "POST",
        NOTES,
        &[("cookie", &ada), ("sec-fetch-site", "same-origin"), ("content-type", "application/json")],
        Some(json!({"id": "n1", "data": {"title": "Hello"}})),
    )
    .await;
    assert_eq!(reply.status, StatusCode::CREATED, "{}", reply.body);
    let held = tokio::task::spawn_blocking(move || {
        let principal = superschematic_engine::Principal::new("ada", ["Note"]);
        let target = superschematic_engine::SchemaTarget { namespace: Some("fixture-app-db") };
        engine.schemas().live(&principal, "Note", target).map(|live| live.is_some())
    })
    .await
    .unwrap()
    .unwrap();
    assert!(held, "the parts' engine has no live Note");
    let reply = send(&router, "GET", "/console/engine/Note/n1", &[("cookie", &ada)], None).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("<div data-field=\"title\"><dt>Title</dt><dd>Hello</dd></div>"), "{}", reply.body);

    // Grace, who holds no Note, is refused it by the engine's policy.
    let grace = sign_in(&router, "grace@example.com").await;
    let reply = send(&router, "GET", "/console/engine/Note/n1", &[("cookie", &grace)], None).await;
    assert_eq!(reply.status, StatusCode::FORBIDDEN, "{}", reply.body);
}

// A GET of path on addr over a socket of its own, which answers the status
// line.
fn get(addr: SocketAddr, path: &str) -> String {
    let mut stream = TcpStream::connect(addr).expect("connect to the app");
    write!(stream, "GET {path} HTTP/1.1\r\nHost: {addr}\r\nConnection: close\r\n\r\n").unwrap();
    let mut answer = String::new();
    stream.read_to_string(&mut answer).unwrap();
    answer.lines().next().unwrap_or_default().to_string()
}

#[tokio::test(flavor = "multi_thread")]
async fn serve_listens_on_the_parts_address() {
    let addr = StdListener::bind("127.0.0.1:0").unwrap().local_addr().unwrap();
    let served = tokio::spawn(serve(parts(addr).await));
    let status = tokio::task::spawn_blocking(move || {
        for _ in 0..100 {
            if TcpStream::connect(addr).is_ok() {
                return get(addr, "/console/engine");
            }
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
        panic!("the app does not listen on {addr}");
    })
    .await
    .unwrap();
    assert_eq!(status, "HTTP/1.1 401 Unauthorized");
    let status = tokio::task::spawn_blocking(move || get(addr, "/api/notes/count")).await.unwrap();
    assert_eq!(status, "HTTP/1.1 401 Unauthorized");
    served.abort();
}
`
