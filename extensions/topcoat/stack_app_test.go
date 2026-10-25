package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/registry"
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
// crate's fixture_app_api_with_parts and fixture_app_api_with_parts_and_api
// take them, converted into AppParts. Then cargo clippy and cargo test run
// appPartsTest: the program's serve binds parts.addr and serves the router
// fixture_app_api_with_parts builds there, its pages and no JSON API or
// engine routes; a user who signs in through the sign-in page is the
// caller of the pages, through the identity page authenticator over the
// parts' implementations; with the API too, a user the mounted JSON API
// signs in is, and the engine pages read the engine the parts hold, which
// a note created through the engine's routes is in.
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
		"fn fixture_app_api_with_parts_and_api(self, parts: impl Into<AppParts>) -> Self;",
		"set_up_parts(self, parts.into(), false)",
		"set_up_parts(self, parts.into(), true)",
		"let page_authenticator: Arc<dyn PageAuthenticator> = Arc::new(IdentityPageAuthenticator::of(&parts.implementations));",
		"set_up(router, parts.implementations, page_authenticator, parts.engine, api)",
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

// Signs in through the app's sign-in page, as a browser on the app's own
// site would, and answers the Cookie header that carries the session.
async fn sign_in_page(router: &Router, login: &str) -> String {
    let request = http::Request::builder()
        .method("POST")
        .uri("/console/sign-in")
        .header("host", "app.example.com")
        .header("content-type", "application/x-www-form-urlencoded")
        .header("sec-fetch-site", "same-origin")
        .body(Body::from(format!("login={}&password={}&next=%2Fconsole", login.replace('@', "%40"), PASSWORD.replace(' ', "+"))))
        .unwrap();
    let response = router.handle(request).await;
    assert_eq!(response.status(), StatusCode::SEE_OTHER);
    let cookie = response.headers().get("set-cookie").expect("the session cookie").to_str().unwrap();
    cookie.split(';').next().unwrap().to_string()
}

const NOTES: &str = "/engine/namespaces/fixture-app-db/schemas/Note/instances";

// The app serves its pages from the parts and mounts no JSON API or engine
// routes: a stack may expose the app and keep its API internal.
#[tokio::test(flavor = "multi_thread")]
async fn the_parts_set_up_the_pages_alone() {
    let parts = parts(SocketAddr::from(([127, 0, 0, 1], 0))).await;
    let router = Router::builder().discover().fixture_app_api_with_parts(parts).build();

    let credentials = json!({"login": "ada@example.com", "password": PASSWORD, "session": "cookie"});
    let reply = send(&router, "POST", "/api/auth/login", &[("sec-fetch-site", "same-origin")], Some(credentials)).await;
    assert_eq!(reply.status, StatusCode::NOT_FOUND, "{}", reply.body);
    let reply = send(&router, "GET", "/api/notes/count", &[], None).await;
    assert_eq!(reply.status, StatusCode::NOT_FOUND, "{}", reply.body);
    let reply = send(&router, "GET", NOTES, &[], None).await;
    assert_eq!(reply.status, StatusCode::NOT_FOUND, "{}", reply.body);

    // The pages call the API and the engine in-process, as the caller the
    // sign-in page's session gives them.
    let reply = send(&router, "GET", "/console/engine", &[], None).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    let ada = sign_in_page(&router, "ada@example.com").await;
    let reply = send(&router, "GET", "/console/engine", &[("cookie", &ada)], None).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("<a href=\"/console/engine/Note\">Notes</a>"), "{}", reply.body);
    let reply = send(&router, "GET", "/console/engine/Note", &[("cookie", &ada)], None).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
}

#[tokio::test(flavor = "multi_thread")]
async fn the_parts_set_up_the_pages_the_api_and_the_engine() {
    let parts = parts(SocketAddr::from(([127, 0, 0, 1], 0))).await;
    let engine = Arc::clone(&parts.engine);
    let router = Router::builder().discover().fixture_app_api_with_parts_and_api(parts).build();

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
    assert_eq!(status, "HTTP/1.1 404 Not Found");
    served.abort();
}
`

// appCopy copies fixture-app-api into a directory of the test's own, with
// no authDb, so its crate has no identity runtime and no engine, and its
// schema without the user sessions that need one; with auth off, its
// operations need no caller either.
func appCopy(t *testing.T, auth bool) string {
	t.Helper()
	dir := filepath.Join(testpaths.TempDir(t), appService)
	for _, file := range []string{"schema.config.yaml", filepath.Join("src", "app.schema.yaml")} {
		data, err := os.ReadFile(filepath.Join(serviceDir(appService), file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, cut := range []string{"authDb: fixture-app-db\n", "  - name: Account\n    userSessions: {}\n    operations: []\n"} {
			text = strings.ReplaceAll(text, cut, "")
		}
		if !auth {
			text = strings.ReplaceAll(text, "        auth: true\n", "")
		}
		path := filepath.Join(dir, file)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestAppPartsOfAnAPIWithoutIdentityOrEngineBuild builds the Topcoat crate
// of fixture-app-api's app with no identity runtime and no engine (D78):
// an API with auth of its own takes the app's page authenticator in
// fixture_app_api_with_parts and fixture_app_api_with_parts_and_api, as
// their doc's serve does, and one without auth takes the parts alone.
// AppParts then holds the implementations alone. cargo clippy and cargo
// test compile a program that calls each.
func TestAppPartsOfAnAPIWithoutIdentityOrEngineBuild(t *testing.T) {
	for _, c := range []struct {
		name string
		auth bool
		want []string
		test string
	}{
		{"own auth", true, []string{
			"fn fixture_app_api_with_parts(self, parts: impl Into<AppParts>, page_authenticator: impl PageAuthenticator) -> Self;",
			"///     let page_authenticator = MyPageAuthenticator::new(&parts.implementations);",
			"set_up_parts(self, parts.into(), Arc::new(page_authenticator), true)",
		}, ownAuthAppTest},
		{"no auth", false, []string{
			"fn fixture_app_api_with_parts(self, parts: impl Into<AppParts>) -> Self;",
			"set_up(router, parts.implementations, api)",
		}, noAuthAppTest},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := appCopy(t, c.auth)
			root := testpaths.TempDir(t)
			if _, err := buildDir(t, registry.DefaultNaming(), dir, root, pagesOutputs(), nil); err != nil {
				t.Fatalf("build: %v", err)
			}
			lib, err := os.ReadFile(filepath.Join(topcoat.Dir(root, appService), "src", "lib.rs"))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range append(c.want, "pub struct AppParts {\n    pub implementations: Implementations,\n}") {
				if !strings.Contains(string(lib), want) {
					t.Errorf("lib.rs lacks %s:\n%s", want, lib)
				}
			}
			if strings.Contains(string(lib), "IdentityPageAuthenticator") || strings.Contains(string(lib), "parts.engine") {
				t.Errorf("lib.rs of an API with no identity runtime or engine has one:\n%s", lib)
			}
			cargoTestCrateWith(t, appService, c.test, crateOptions{outputs: pagesOutputs(), dir: dir})
		})
	}
}

// ownAuthAppTest is the program of the app of an API with auth that is
// not the identity runtime's: it hands its own page authenticator over
// with the parts.
const ownAuthAppTest = `use async_trait::async_trait;
use schemas_fixture_app_api_topcoat::api::apps::BackofficeParts;
use schemas_fixture_app_api_topcoat::api::runtime::{ApiError, Principal};
use schemas_fixture_app_api_topcoat::{PageAuthenticator, RouterBuilderFixtureAppApiExt};
use topcoat::context::Cx;
use topcoat::router::{Router, RouterBuilderDiscoverExt};

struct NoOne;

#[async_trait]
impl PageAuthenticator for NoOne {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

fn pages(parts: BackofficeParts) -> Router {
    Router::builder().discover().fixture_app_api_with_parts(parts, NoOne).build()
}

fn pages_and_api(parts: BackofficeParts) -> Router {
    Router::builder().discover().fixture_app_api_with_parts_and_api(parts, NoOne).build()
}

#[test]
fn builds() {
    let _ = [pages as fn(BackofficeParts) -> Router, pages_and_api];
}
`

// noAuthAppTest is the program of the app of an API without auth: the
// parts alone set the crate up.
const noAuthAppTest = `use schemas_fixture_app_api_topcoat::api::apps::BackofficeParts;
use schemas_fixture_app_api_topcoat::RouterBuilderFixtureAppApiExt;
use topcoat::router::{Router, RouterBuilderDiscoverExt};

fn pages(parts: BackofficeParts) -> Router {
    Router::builder().discover().fixture_app_api_with_parts(parts).build()
}

fn pages_and_api(parts: BackofficeParts) -> Router {
    Router::builder().discover().fixture_app_api_with_parts_and_api(parts).build()
}

#[test]
fn builds() {
    let _ = [pages as fn(BackofficeParts) -> Router, pages_and_api];
}
`
