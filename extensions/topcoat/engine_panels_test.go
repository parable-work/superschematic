package topcoat_test

import (
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
)

// TestTheEnginePanels builds fixture-engine-api's crate over
// fixture-engine-db, with the engine panels written as the engine pages'
// (topcoat.PanelsBeforeTheConsole), and runs cargo clippy, then cargo
// test, with enginePanelsTest: pages of the test hand engine_panels::panel
// canned describe documents and records, and Branches' a document the test
// seeds, and drop both before they return its view.
//
//   - Workflow shows the state by its display label, a button per labeled
//     move out of it, a form for the unlabeled ones, and none in a terminal
//     state.
//   - Approvals shows a gate's sign-offs against its count and who signed,
//     and offers the caller a sign-off, their withdraw, or why neither: the
//     creator, the gated state, or a caller whose permissions do not cover
//     the gate's.
//   - Branches shows the refs and the release log, a draft's history
//     newest first with its commit, merge, discard and release forms, the
//     diff of two commits with each entity's content before and after, and
//     the engine's refusal to a caller who may not read the document.
//   - A behavior with no panel of its own gets none.
func TestTheEnginePanels(t *testing.T) {
	topcoat.PanelsBeforeTheConsole(t)
	cargoTestCrateWith(t, engineService, enginePanelsTest, crateOptions{
		before: map[string]map[string]any{engineDatabase: engineDatabaseOutputs()},
	})
}

// enginePanelsTest is tests/app.rs of fixture-engine-api's Topcoat crate
// with the engine panels. The describe documents are trimmed from the
// engine's own for fixture-engine-db's Request and Document.
const enginePanelsTest = `use std::sync::Arc;

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_engine_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_engine_api_topcoat::api::{types, Implementations, NotesImplementation, NotesListNotesArgs};
use schemas_fixture_engine_api_topcoat::{engine, engine_panels, PageAuthenticator, RouterBuilderFixtureEngineApiExt, Setup};
use serde_json::{json, Map, Value};
use superschematic_engine::{CreateOptions, Engine, GetOptions, InstanceRecord, WriteOptions};
use topcoat::context::{Cx, app_context};
use topcoat::router::request::{headers, uri};
use topcoat::router::{page, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};
use topcoat::view::{view, View};

#[path = "support/enter.rs"]
mod enter;

const NAMESPACE: &str = "fixture-engine-db";

// Request's describe document, trimmed: its display, and its behaviors'
// configs, fields and operations.
const REQUEST_DESCRIBE: &str = r#"{
  "namespace": "fixture-engine-db",
  "name": "Request",
  "instanceType": "Request",
  "display": {
    "noun": "Request",
    "plural": "Requests",
    "titleField": "title",
    "states": {
      "approved": {"label": "Approved", "tone": "success"},
      "closed": {"label": "Closed", "tone": "muted"},
      "open": {"label": "Open", "tone": "active"},
      "rejected": {"label": "Rejected", "tone": "danger"}
    },
    "transitions": {"open": {"approved": "Approve", "rejected": "Reject"}}
  },
  "fields": [{"name": "title"}],
  "behaviors": [
    {
      "name": "Workflow",
      "config": {
        "outcomes": {"rejected": "failure"},
        "states": ["open", "approved", "rejected", "closed"],
        "transitions": [
          {"from": "open", "to": "approved"},
          {"from": "open", "to": "rejected"},
          {"from": "approved", "to": "closed"}
        ]
      },
      "fields": [{"name": "status"}, {"name": "enteredAt"}],
      "operations": ["transition"]
    },
    {
      "name": "Approvals",
      "config": {"gates": {"approved": {"count": 2, "permission": "requests.approve"}}},
      "fields": [{"name": "gates"}],
      "operations": ["signOff", "withdrawSignOff", "listSignOffs"]
    }
  ],
  "operations": [
    {"name": "transition", "behavior": "Workflow", "scope": "instance", "writes": true},
    {"name": "signOff", "behavior": "Approvals", "scope": "instance", "writes": true},
    {"name": "withdrawSignOff", "behavior": "Approvals", "scope": "instance", "writes": true},
    {"name": "listSignOffs", "behavior": "Approvals", "scope": "instance", "writes": false}
  ]
}"#;

// Document's describe document, trimmed.
const DOCUMENT_DESCRIBE: &str = r#"{
  "namespace": "fixture-engine-db",
  "name": "Document",
  "instanceType": "Document",
  "display": {"noun": "Document", "plural": "Documents", "titleField": "title"},
  "fields": [{"name": "title"}, {"name": "template"}],
  "behaviors": [
    {
      "name": "Branches",
      "config": {"kinds": {"section": {"type": "Section"}}},
      "fields": [{"name": "release"}],
      "operations": ["branch", "save", "commit", "seal", "merge", "rebase", "revert", "releaseCommit", "discard", "refs", "releases", "compose", "materialize", "released", "diff", "history"]
    }
  ]
}"#;

// What each subject holds: nobody nothing, a reader the types, and anyone
// else the types and the gate's permission too.
fn permissions_of(subject: &str) -> Vec<&'static str> {
    match subject {
        "nobody" => vec![],
        "reader" => vec!["Note", "Request", "Document"],
        _ => vec!["Note", "Request", "Document", "requests.approve"],
    }
}

// The caller the bearer token names.
fn bearer_of(headers: &http::HeaderMap) -> Option<Principal> {
    headers
        .get("authorization")
        .and_then(|value| value.to_str().ok())
        .and_then(|value| value.strip_prefix("Bearer "))
        .map(|subject| Principal::new(subject, permissions_of(subject)))
}

struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_of(&request.headers))
    }
}

struct PageBearer;

#[async_trait]
impl PageAuthenticator for PageBearer {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Ok(bearer_of(headers(cx)))
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

fn object(value: Value) -> Map<String, Value> {
    match value {
        Value::Object(map) => map,
        _ => Map::new(),
    }
}

// The query's value of name, empty when absent.
fn query_value(cx: &Cx, name: &str) -> String {
    uri(cx)
        .query()
        .unwrap_or_default()
        .split('&')
        .filter_map(|pair| pair.split_once('='))
        .find(|(key, _)| *key == name)
        .map(|(_, value)| value.to_owned())
        .unwrap_or_default()
}

// A Request ada created, in status, signed off for approved by signed.
fn request(status: &str, signed: &[&str]) -> InstanceRecord {
    let gates = if signed.is_empty() { json!({}) } else { json!({"approved": {"signedBy": signed}}) };
    InstanceRecord {
        namespace: NAMESPACE.to_owned(),
        schema: "Request".to_owned(),
        id: "r1".to_owned(),
        schema_namespace: NAMESPACE.to_owned(),
        version: 1,
        seq: 4,
        data: object(json!({"title": "Laptop"})),
        behaviors: object(json!({
            "Workflow": {"status": status, "enteredAt": 1_792_888_229_289_i64},
            "Approvals": {"gates": gates}
        })),
        created_at: 1_792_888_229_289,
        created_by: "ada".to_owned(),
        updated_at: 1_792_888_229_290,
        updated_by: "bob".to_owned(),
        value_refs: None,
    }
}

// The Workflow and Approvals panels of a canned Request whose status and
// sign-offs the query gives (?status=open&signed=bob,cy), and whether
// Comments, which has no panel of its own, gets one.
#[page("/panels/request")]
async fn request_panels(cx: &Cx) -> topcoat::Result<impl View> {
    let describe: Value = serde_json::from_str(REQUEST_DESCRIBE).expect("Request's describe document");
    let status = query_value(cx, "status");
    let signed = query_value(cx, "signed");
    let signed: Vec<&str> = signed.split(',').filter(|subject| !subject.is_empty()).collect();
    let record = request(&status, &signed);
    let workflow = engine_panels::panel(cx, "Workflow", &describe, &record).await;
    let approvals = engine_panels::panel(cx, "Approvals", &describe, &record).await;
    let comments = engine_panels::panel(cx, "Comments", &describe, &record).await;
    drop(record);
    drop(describe);
    let comments = if comments.is_none() { "Comments has no panel" } else { "Comments has a panel" };
    Ok(view! {
        <main>
            if let Some(panel) = workflow {
                (panel)
            }
            if let Some(panel) = approvals {
                (panel)
            }
            <p>(comments)</p>
        </main>
    })
}

// The Branches panel of the Document the query names (?id=), read as ada,
// shown to the page's caller.
#[page("/panels/document")]
async fn document_panel(cx: &Cx) -> topcoat::Result<impl View> {
    let setup: &Setup = app_context(cx);
    let engine = Arc::clone(&setup.engine);
    let id = query_value(cx, "id");
    let record = tokio::task::spawn_blocking(move || {
        let ada = superschematic_engine::Principal::new("ada", permissions_of("ada"));
        engine.instances().get(&ada, "Document", &id, GetOptions { namespace: Some(NAMESPACE), ..GetOptions::default() })
    })
    .await
    .expect("the read")
    .expect("the document's read")
    .expect("the document");
    let describe: Value = serde_json::from_str(DOCUMENT_DESCRIBE).expect("Document's describe document");
    let panel = engine_panels::panel(cx, "Branches", &describe, &record).await;
    drop(record);
    drop(describe);
    Ok(view! {
        <main>
            if let Some(panel) = panel {
                (panel)
            }
        </main>
    })
}

// What the test seeds: a document, its primary line and a draft, and the
// draft's three commits, the first tagged and released.
struct Seeded {
    document: String,
    main: String,
    draft: String,
    first: String,
    second: String,
    third: String,
    draft_version: i64,
}

fn call(engine: &Engine, id: &str, operation: &str, params: Value) -> Value {
    let ada = superschematic_engine::Principal::new("ada", permissions_of("ada"));
    let options = WriteOptions { namespace: Some(NAMESPACE), ..WriteOptions::default() };
    engine.instances().invoke(&ada, "Document", id, operation, &params, options).unwrap_or_else(|err| panic!("{operation}: {err}"))
}

// Seeds a Document: a draft of its primary line saves Intro and commits
// it tagged ("first"), adds Usage ("second"), then rewrites Intro
// ("third"); the first commit is released.
fn seed(engine: &Engine) -> Seeded {
    let ada = superschematic_engine::Principal::new("ada", permissions_of("ada"));
    let options = CreateOptions { namespace: Some(NAMESPACE), ..CreateOptions::default() };
    let document = engine.instances().create(&ada, "Document", &json!({"title": "Guide"}), options).expect("create a document").id;
    let refs = call(engine, &document, "refs", json!({}));
    let main = refs["items"][0]["id"].as_str().expect("the primary line").to_owned();
    let draft = call(engine, &document, "branch", json!({"name": "draft"}));
    let id = draft["id"].as_str().expect("the draft").to_owned();
    let saved = call(engine, &document, "save", json!({"ref": id, "version": draft["version"], "edits": {"section": {"upsert": [{"heading": "Intro", "body": "Hello"}]}}}));
    let intro = saved["saved"]["section"][0]["entity_key"].as_str().expect("Intro's key").to_owned();
    let first = call(engine, &document, "commit", json!({"ref": id, "version": saved["ref"]["version"], "message": "first", "tag": true}));
    let saved = call(engine, &document, "save", json!({"ref": id, "version": first["ref"]["version"], "edits": {"section": {"upsert": [{"heading": "Usage", "body": "Run it"}]}}}));
    let second = call(engine, &document, "commit", json!({"ref": id, "version": saved["ref"]["version"], "message": "second"}));
    let saved = call(engine, &document, "save", json!({"ref": id, "version": second["ref"]["version"], "edits": {"section": {"upsert": [{"entity_key": intro, "heading": "Intro", "body": "Hello there"}]}}}));
    let third = call(engine, &document, "commit", json!({"ref": id, "version": saved["ref"]["version"], "message": "third"}));
    let first_id = first["commit"]["id"].as_str().expect("the first commit").to_owned();
    call(engine, &document, "releaseCommit", json!({"commit": first_id, "version": 0}));
    Seeded {
        document,
        main,
        draft: id,
        first: first_id,
        second: second["commit"]["id"].as_str().expect("the second commit").to_owned(),
        third: third["commit"]["id"].as_str().expect("the third commit").to_owned(),
        draft_version: third["ref"]["version"].as_i64().expect("the draft's version"),
    }
}

fn app() -> (Router, Arc<Engine>) {
    let path = std::env::temp_dir().join(format!("topcoat-panels-{}-{:?}.db", std::process::id(), std::thread::current().id()));
    let _ = std::fs::remove_file(&path);
    let engine = engine::open(&path).expect("open the engine");
    let implementations = Implementations { notes: Arc::new(Notes), authenticator: Arc::new(Bearer) };
    let router = Router::builder().discover().fixture_engine_api(implementations, PageBearer, Arc::clone(&engine)).build();
    (router, engine)
}

// GETs uri as bearer, holds the page to the implicit-submission rule, and
// returns its status and its body, its quotes unescaped.
async fn get(router: &Router, uri: &str, bearer: Option<&str>) -> (StatusCode, String) {
    let mut request = http::Request::builder().method("GET").uri(uri);
    if let Some(bearer) = bearer {
        request = request.header("authorization", format!("Bearer {bearer}"));
    }
    let response = router.handle(request.body(Body::empty()).unwrap()).await;
    let status = response.status();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    enter::assert_enter_submits(uri, &body);
    (status, body.replace("&quot;", "\"").replace("&#34;", "\""))
}

fn has(body: &str, wants: &[&str]) {
    for want in wants {
        assert!(body.contains(want), "lacks {want}:\n{body}");
    }
}

fn lacks(body: &str, unwanted: &[&str]) {
    for unwanted in unwanted {
        assert!(!body.contains(unwanted), "has {unwanted}:\n{body}");
    }
}

#[tokio::test(flavor = "multi_thread")]
async fn the_workflow_panel_offers_the_moves_out_of_the_state() {
    let (router, _engine) = app();
    let (status, body) = get(&router, "/panels/request?status=open", Some("bob")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    has(&body, &[
        r#"data-behavior="Workflow""#,
        r#"data-state="open""#,
        r#"data-tone="active""#,
        ">Open</p>",
        r#"action="/console/engine/Request/r1/operations/transition""#,
        r#"name="expectedSeq" value="4""#,
        r#"name="to" value="approved""#,
        ">Approve</button>",
        r#"name="to" value="rejected""#,
        ">Reject</button>",
        "Comments has no panel",
    ]);
    lacks(&body, &["Move to"]);
    // The move out of approved is unlabeled: a form offers it by the label
    // of the state it enters.
    let (_, body) = get(&router, "/panels/request?status=approved", Some("bob")).await;
    has(&body, &[">Approved</p>", "Move to", r#"<option value="closed">Closed</option>"#, ">Move</button>"]);
    lacks(&body, &[">Approve</button>"]);
    let (_, body) = get(&router, "/panels/request?status=rejected", Some("bob")).await;
    has(&body, &[r#"data-tone="danger""#, "No move leaves this state."]);
    lacks(&body, &["operations/transition"]);
}

#[tokio::test(flavor = "multi_thread")]
async fn the_approvals_panel_offers_the_caller_their_sign_off() {
    let (router, _engine) = app();
    // cy may sign off: one of the two sign-offs is bob's.
    let (status, body) = get(&router, "/panels/request?status=open&signed=bob", Some("cy")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    has(&body, &[
        r#"data-behavior="Approvals""#,
        r#"<div class="ss-gate" data-state="approved">"#,
        "<h3>Approved</h3>",
        "1 of 2 sign-offs",
        "<li>bob</li>",
        r#"action="/console/engine/Request/r1/operations/signOff""#,
        r#"name="state" value="approved""#,
        r#"<textarea name="note""#,
        ">Sign off</button>",
    ]);
    lacks(&body, &["withdrawSignOff"]);
    // bob has signed, so withdraws.
    let (_, body) = get(&router, "/panels/request?status=open&signed=bob", Some("bob")).await;
    has(&body, &[r#"action="/console/engine/Request/r1/operations/withdrawSignOff""#, ">Withdraw my sign-off</button>"]);
    lacks(&body, &[">Sign off</button>"]);
    // ada created it, and the gate excludes its creator.
    let (_, body) = get(&router, "/panels/request?status=open", Some("ada")).await;
    has(&body, &["0 of 2 sign-offs", "No one has signed off.", "You created it, so you cannot sign off for this state."]);
    lacks(&body, &[">Sign off</button>"]);
    // A reader's permissions do not cover requests.approve; nor is there
    // a sign-off without a caller.
    for bearer in [Some("reader"), None] {
        let (_, body) = get(&router, "/panels/request?status=open&signed=bob,cy", bearer).await;
        has(&body, &["2 of 2 sign-offs", "<li>cy</li>"]);
        lacks(&body, &[">Sign off</button>", ">Withdraw my sign-off</button>"]);
    }
    // In the gated state, no sign-off is taken for it.
    let (_, body) = get(&router, "/panels/request?status=approved", Some("cy")).await;
    has(&body, &["It is in this state, so no sign-off is taken for it."]);
    lacks(&body, &[">Sign off</button>"]);
}

#[tokio::test(flavor = "multi_thread")]
async fn the_branches_panel_shows_the_refs_a_history_and_a_diff() {
    let (router, engine) = app();
    let seeded = {
        let engine = Arc::clone(&engine);
        tokio::task::spawn_blocking(move || seed(&engine)).await.expect("the seed")
    };
    let page = format!("/panels/document?id={}", seeded.document);
    let item = format!("/console/engine/Document/{}", seeded.document);
    let short = |id: &str| id.chars().take(8).collect::<String>();

    // The refs, primary first, the release log, and the primary line's
    // history, which is empty; the branch form branches from it.
    let (status, body) = get(&router, &page, Some("ada")).await;
    assert_eq!(status, StatusCode::OK, "{body}");
    has(&body, &[
        r#"data-behavior="Branches""#,
        "Release 1 is current.",
        &format!(r#"<a href="{item}?ref={}">main</a>"#, seeded.main),
        &format!(r#"<a href="{item}?ref={}">draft</a>"#, seeded.draft),
        "Primary line",
        "History of main",
        "It has no commits.",
        &format!("<code>{}</code>", short(&seeded.first)),
        &format!(r#"action="{item}/operations/branch""#),
        &format!(r#"<option value="{}" selected="">main</option>"#, seeded.main),
    ]);
    assert!(body.find(">main</a>").unwrap() < body.find(">draft</a>").unwrap(), "the primary line comes first:\n{body}");
    lacks(&body, &["operations/commit", "operations/discard"]);

    // The draft's history, newest first, with its writes.
    let (_, body) = get(&router, &format!("{page}&ref={}", seeded.draft), Some("ada")).await;
    has(&body, &[
        "History of draft",
        "<td>#1</td>",
        &format!(r#"action="{item}/operations/releaseCommit""#),
        &format!(r#"name="commit" value="{}""#, seeded.first),
        r#"name="version" value="1""#,
        &format!(r#"action="{item}/operations/commit""#),
        &format!(r#"name="ref" value="{}""#, seeded.draft),
        &format!(r#"name="version" value="{}""#, seeded.draft_version),
        r#"name="tag" value="true""#,
        &format!(r#"action="{item}/operations/merge""#),
        &format!(r#"name="target" value="{}""#, seeded.main),
        r#"name="targetVersion" value="1""#,
        ">Merge into main</button>",
        &format!(r#"action="{item}/operations/discard""#),
        ">Compare</button>",
        &format!(r#"<option value="{}" selected="">"#, seeded.second),
        &format!(r#"<option value="{}" selected="">"#, seeded.third),
    ]);
    let third = body.find("<td>third</td>").expect("the third commit");
    let second = body.find("<td>second</td>").expect("the second commit");
    let first = body.find("<td>first</td>").expect("the first commit");
    assert!(third < second && second < first, "newest first:\n{body}");

    // The diff of the first and the third commits: Usage added, Intro
    // rewritten.
    let (_, body) = get(&router, &format!("{page}&ref={}&from={}&to={}", seeded.draft, seeded.first, seeded.third), Some("ada")).await;
    has(&body, &[
        &format!("Changes from {} to {}", short(&seeded.first), short(&seeded.third)),
        &format!(r#"<option value="{}" selected="">"#, seeded.first),
        "<td><code>section/",
        "<td>Added</td>",
        r#"{"body":"Run it","heading":"Usage"}"#,
        "<td>Updated</td>",
        r#"<td><code>{"body":"Hello","heading":"Intro"}</code></td><td><code>{"body":"Hello there","heading":"Intro"}</code></td>"#,
    ]);

    // The engine refuses a caller who may not read the document; a page
    // with no caller reads nothing.
    let (_, body) = get(&router, &page, Some("nobody")).await;
    has(&body, &["nobody may not call refs (read) on Document"]);
    lacks(&body, &[">draft</a>"]);
    let (_, body) = get(&router, &page, None).await;
    has(&body, &["Sign in to see its branches."]);
}
`
