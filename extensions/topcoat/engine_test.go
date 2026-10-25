package topcoat_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// engineService is the extension's own fixture over an engine database
// (D58), fixture-engine-db, whose Note composes Comments.
const (
	engineService  = "fixture-engine-api"
	engineDatabase = "fixture-engine-db"
)

// engineDatabaseOutputs are fixture-engine-db's outputs: its Rust types,
// which hold its engine document, and the Go types its ORM imports.
func engineDatabaseOutputs() map[string]any {
	return map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}, "rust": map[string]any{"enabled": true}}}
}

// TestTheCrateMountsTheEngineOfAnEngineDatabase: the crate of an API whose
// database is an engine database depends on the engine's crate with its
// axum and sqlite features, the core's behaviors and the database's Rust
// types, writes src/engine.rs, whose open deploys the database's engine
// document on a SQLite file, and its setup method takes the engine and
// mounts its routes at /engine/{*rest} (D58).
func TestTheCrateMountsTheEngineOfAnEngineDatabase(t *testing.T) {
	root := testpaths.TempDir(t)
	if _, err := buildService(t, engineDatabase, root, engineDatabaseOutputs()); err != nil {
		t.Fatalf("build %s: %v", engineDatabase, err)
	}
	if _, err := buildService(t, engineService, root, rustOutputs()); err != nil {
		t.Fatalf("build %s: %v", engineService, err)
	}
	dir := topcoat.Dir(root, engineService)
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	manifest := read("Cargo.toml")
	for _, want := range []string{
		`superschematic-engine = { path = "`, `", default-features = false, features = ["axum", "sqlite"] }`,
		`superschematic-engine-behaviors = { path = "`,
		`schemas-fixture-engine-db-types = { path = "../../types/rust/fixture-engine-db" }`,
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("Cargo.toml lacks %s:\n%s", want, manifest)
		}
	}
	lib := read("src/lib.rs")
	for _, want := range []string{
		"pub mod engine;",
		"engine: Arc<superschematic_engine::Engine>) -> Self {",
		`.route(TowerRoute::any("/engine/{*rest}", engine_routes))`,
	} {
		if !strings.Contains(lib, want) {
			t.Errorf("lib.rs lacks %s:\n%s", want, lib)
		}
	}
	engine := read("src/engine.rs")
	for _, want := range []string{
		`pub const POLICY: &str = r#""permission""#;`,
		"options.behaviors = superschematic_engine_behaviors::core();",
		"engine.deploy(&schemas, Some(&namespace))?;",
	} {
		if !strings.Contains(engine, want) {
			t.Errorf("engine.rs lacks %s:\n%s", want, engine)
		}
	}
}

// TestTheEngineServesInATopcoatApp builds fixture-engine-api's crate over
// fixture-engine-db's Rust types and runs cargo clippy, then cargo test,
// with engineAppTest: the app opens the engine on a SQLite file, which
// deploys Note, and mounts its routes, where the API's authenticator's
// caller holding Note creates a note and reads it back with its comments,
// one holding nothing is refused by the permission policy, and a request
// with no caller is 401; a page reads the engine in-process from the app
// context.
func TestTheEngineServesInATopcoatApp(t *testing.T) {
	cargoTestCrateWith(t, engineService, engineAppTest, crateOptions{
		outputs: rustOutputs(),
		before:  map[string]map[string]any{engineDatabase: engineDatabaseOutputs()},
	})
}

// engineAppTest is tests/app.rs of fixture-engine-api's Topcoat crate.
const engineAppTest = `use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_engine_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_engine_api_topcoat::api::{types, Implementations, NotesImplementation, NotesListNotesArgs};
use schemas_fixture_engine_api_topcoat::{engine, PageAuthenticator, RouterBuilderFixtureEngineApiExt, Setup};
use topcoat::context::{Cx, app_context};
use topcoat::router::{page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

// The API's authenticator: the bearer token names the subject, who holds
// Note unless they are nobody.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        let subject = request
            .headers
            .get("authorization")
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "));
        Ok(subject.map(|subject| {
            let permissions: Vec<&str> = if subject == "nobody" { vec![] } else { vec!["Note"] };
            Principal::new(subject, permissions)
        }))
    }
}

struct NoPages;

#[async_trait]
impl PageAuthenticator for NoPages {
    async fn principal(&self, _cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

struct Notes;

#[async_trait]
impl NotesImplementation for Notes {
    async fn note_count(&self, _ctx: RequestContext) -> Result<types::NoteCount, ApiError> {
        Ok(types::NoteCount { count: 0.0 })
    }

    async fn list_notes(&self, _ctx: RequestContext, _args: NotesListNotesArgs) -> Result<Vec<types::NoteTitle>, ApiError> {
        Ok(Vec::new())
    }
}

// A page that reads the engine in-process, off the async threads: the
// version of Note the open deployed.
#[page("/notes/schema")]
async fn note_schema(cx: &Cx) -> topcoat::Result<impl View> {
    let setup: &Setup = app_context(cx);
    let engine = Arc::clone(&setup.engine);
    let principal = superschematic_engine::Principal::new("ada", ["Note"]);
    let target = superschematic_engine::SchemaTarget { namespace: Some("fixture-engine-db") };
    let version = tokio::task::spawn_blocking(move || engine.schemas().live(&principal, "Note", target).map(|live| live.and_then(|live| live.version)))
        .await
        .expect("the read")
        .expect("Note's live version");
    let said = format!("Note version {}", version.unwrap_or_default());
    Ok(view! { <p>(said)</p> })
}

fn app() -> Router {
    let path = std::env::temp_dir().join(format!("topcoat-engine-{}-{:?}.db", std::process::id(), std::thread::current().id()));
    let _ = std::fs::remove_file(&path);
    let engine = engine::open(&path).expect("open the engine");
    let implementations = Implementations { notes: Arc::new(Notes), authenticator: Arc::new(Bearer) };
    Router::builder().discover().fixture_engine_api(implementations, NoPages, engine).build()
}

async fn send(router: &Router, method: &str, uri: &str, bearer: Option<&str>, body: &str) -> (StatusCode, String) {
    let mut request = http::Request::builder().method(method).uri(uri);
    if let Some(bearer) = bearer {
        request = request.header("authorization", format!("Bearer {bearer}"));
    }
    if !body.is_empty() {
        request = request.header("content-type", "application/json");
    }
    let response = router.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    (status, String::from_utf8(bytes.to_vec()).unwrap())
}

const NOTES: &str = "/engine/namespaces/fixture-engine-db/schemas/Note/instances";

#[tokio::test(flavor = "multi_thread")]
async fn the_engine_serves_at_its_mount_behind_the_apis_authenticator() {
    let router = app();
    let (status, body) = send(&router, "POST", NOTES, Some("ada"), r#"{"id": "n1", "data": {"title": "Hello"}}"#).await;
    assert_eq!(status, StatusCode::CREATED, "{body}");
    assert!(body.contains(r#""createdBy":"ada""#), "{body}");
    let (status, body) = send(&router, "GET", &format!("{NOTES}/n1"), Some("ada"), "").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains(r#""title":"Hello""#) && body.contains(r#""Comments":{"messages":0,"threads":{"open":0,"resolved":0}}"#), "{body}");
    let (status, body) = send(&router, "GET", &format!("{NOTES}/n1"), Some("nobody"), "").await;
    assert_eq!(status, StatusCode::FORBIDDEN, "{body}");
    let (status, body) = send(&router, "GET", &format!("{NOTES}/n1"), None, "").await;
    assert_eq!(status, StatusCode::UNAUTHORIZED, "{body}");
    let (status, body) = send(&router, "GET", "/notes/schema", None, "").await;
    assert_eq!(status, StatusCode::OK, "{body}");
    assert!(body.contains("Note version 1"), "{body}");
}
`
