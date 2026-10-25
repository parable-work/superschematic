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

// TestTheConsolePagesTheEngine: fixture-engine-api's pages, on, page the
// engine of fixture-engine-db under /console/engine (D44, amended). Its
// crate passes cargo clippy with warnings denied, and enginePagesAppTest,
// through Router::handle and with every page held to testdata/enter.rs,
// shows the types and the nav's link to a reader and none to a caller
// without a permission, pages a list forward and back, shows an instance's
// own fields and its Comments in the generic panel, and runs its
// operations' forms: a comment posted (303), a stale expectedSeq refused
// (409), a parameter the form or the engine refuses at its control (422), a
// read's result on the page, a write sent by GET (405), and a missing
// instance or operation (404). A panel's form from operation_form fills in
// its values and holds its hidden ones, and posts without a seq. With
// fixture-engine-db's engine hiding Approvals.listSignOffs, an operation
// the policy refuses is 403, one the deployment hides 404, and one the
// engine hides from MCP for its handle's length alone runs; and a post
// another site sent is refused (403) with the app's origin policy off. The
// build log says why a crate with pages and an engine has no engine pages.
func TestTheConsolePagesTheEngine(t *testing.T) {
	t.Run("build log", func(t *testing.T) {
		// logOf builds the service in dir with the pages as pages, and
		// gives its build log and whether engine_pages.rs was written.
		logOf := func(dir string, pages map[string]any) (string, bool) {
			t.Helper()
			outputs := rustOutputs()
			outputs["topcoat"] = map[string]any{"enabled": true, "pages": pages}
			var log strings.Builder
			root := testpaths.TempDir(t)
			if _, err := buildDir(t, registry.DefaultNaming(), dir, root, outputs, &log); err != nil {
				t.Fatalf("build %s with pages %v: %v", dir, pages, err)
			}
			_, err := os.Stat(filepath.Join(topcoat.Dir(root, engineService), "src", "engine_pages.rs"))
			return log.String(), err == nil
		}
		on := map[string]any{"enabled": true}
		if log, written := logOf(serviceDir(engineService), on); !written || strings.Contains(log, "no engine pages") {
			t.Errorf("the engine pages, on by default: written %v, log:\n%s", written, log)
		}
		if log, written := logOf(serviceDir(engineService), map[string]any{"enabled": true, "engine": false}); written || strings.Contains(log, "no engine pages") {
			t.Errorf("pages.engine false: written %v, log:\n%s", written, log)
		}
		for _, c := range []struct {
			name  string
			edit  func(string) string
			pages map[string]any
			want  string
		}{
			{
				"no authenticator",
				func(schema string) string { return strings.ReplaceAll(schema, "auth: true", "auth: false") },
				on,
				"  - topcoat: no engine pages at /console/engine: no route of the API authenticates a caller, and a page calls the engine only as its caller, whom the engine's policy admits\n",
			},
			{
				"a collection of the API's own",
				func(schema string) string {
					return strings.Replace(schema, "restPath: notes\n", "restPath: engine\n", 1)
				},
				on,
				"  - topcoat: no engine pages at /console/engine: the API's own collection engine is served there\n",
			},
			{
				"under the engine's routes",
				nil,
				map[string]any{"enabled": true, "prefix": "/engine"},
				"  - topcoat: no engine pages at /engine/engine: the path is under /engine, where the crate mounts the engine's own routes\n",
			},
		} {
			dir := serviceDir(engineService)
			if c.edit != nil {
				dir = engineCopy(t, c.edit)
			}
			if log, written := logOf(dir, c.pages); written || !strings.Contains(log, c.want) {
				t.Errorf("%s: written %v, want %q in the log:\n%s", c.name, written, c.want, log)
			}
		}
	})
	t.Run("pages", func(t *testing.T) {
		cargoTestCrateWith(t, engineService, enginePagesAppTest, crateOptions{
			before:  map[string]map[string]any{engineDatabase: engineDatabaseHiding("Approvals.listSignOffs")},
			appends: map[string]string{"src/engine_pages.rs": panelFormPage + panelShowsTest},
		})
	})
}

// engineDatabaseHiding are fixture-engine-db's outputs with its engine's
// tools hiding operations (outputs.engine.tools.hidden), by
// <Behavior>.<operation>.
func engineDatabaseHiding(hidden ...string) map[string]any {
	names := make([]any, len(hidden))
	for i, name := range hidden {
		names[i] = name
	}
	outputs := engineDatabaseOutputs()
	outputs["engine"] = map[string]any{"tools": map[string]any{"hidden": names}}
	return outputs
}

// engineCopy is a copy of fixture-engine-api whose schema edit changes.
func engineCopy(t *testing.T, edit func(schema string) string) string {
	t.Helper()
	dir := filepath.Join(testpaths.TempDir(t), engineService)
	for _, file := range []string{"schema.config.yaml", filepath.Join("src", "notes.schema.yaml")} {
		data, err := os.ReadFile(filepath.Join(serviceDir(engineService), file))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if strings.HasSuffix(file, "notes.schema.yaml") {
			edited := edit(text)
			if edited == text {
				t.Fatal("the edit changed nothing")
			}
			text = edited
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

// panelFormPage is a page a panel could be, appended to engine_pages.rs:
// it renders Comments' comment form with operation_form, its body filled
// in and its replyTo a hidden input.
const panelFormPage = `
#[page("/console/engine/Note/{engine_id}/panel-form")]
pub async fn panel_form_page(cx: &Cx) -> topcoat::Result<impl View> {
    let id = path_param::<EngineId>(cx).to_owned();
    let describe = described(cx, "Note").await.map_err(|_| topcoat::router::error::not_found())?.clone();
    let comment = operations(cx, &describe).await.into_iter().find(|operation| operation.name == "comment").expect("comment");
    let values = vec![("body".to_owned(), "Filled".to_owned()), ("replyTo".to_owned(), "1".to_owned())];
    Ok(view! {
        operation_form(schema: "Note".to_owned(), id: Some(id), seq: None, operation: comment, values: values, hidden: vec!["replyTo".to_owned()])
    })
}
`

// panelShowsTest, appended to engine_pages.rs, holds panel_shows to the
// panels: a core panel owns every operation of its behavior, a commerce
// panel those commerce_shows names, and the instance page lists the rest.
const panelShowsTest = `
#[cfg(test)]
mod panel_shows_tests {
    use super::panel_shows;

    #[test]
    fn a_panel_owns_the_operations_it_shows_and_the_page_lists_the_rest() {
        for (behavior, operation) in [
            ("Workflow", "transition"),
            ("Approvals", "signOff"),
            ("Branches", "commit"),
            ("Stock", "receiveStock"),
            ("Stock", "adjustStock"),
            ("Stock", "countStock"),
            ("Stock", "listStockMovements"),
            ("Calendar", "listBookings"),
            ("Payments", "refundPayment"),
        ] {
            assert!(panel_shows(behavior, operation), "{behavior}.{operation} is the panel's");
        }
        for (behavior, operation) in [
            ("Stock", "holdFor"),
            ("Calendar", "setOpeningHours"),
            ("Calendar", "addClosure"),
            ("Calendar", "removeClosure"),
            ("Holds", "retakeHolds"),
            ("Payments", "capturePayment"),
            ("Payments", "voidPayment"),
        ] {
            assert!(!panel_shows(behavior, operation), "{behavior}.{operation} is listed");
        }
    }
}
`

// enginePagesAppTest is tests/app.rs of fixture-engine-api's Topcoat crate
// with its pages on: the pages' caller is the API's, from the bearer token,
// and ada holds Note, which the engine's permission policy asks of a
// reader and a writer of notes, reader Note.read alone, clerk and boss
// what a SupplierContractAmendmentRequest and a Request ask, and nobody
// nothing.
// Each app's engine also holds SupplierContractAmendmentRequest, Request
// under a name that makes the MCP handles of its Approvals tools too long,
// and its engine document hides Approvals.listSignOffs. A second test
// checks that an operation the engine hides from MCP for its handle alone
// is offered, one the deployment hides is not there (404), and one the
// policy refuses is refused (403); a third, with the app's origin policy
// off, that a write another site sent is refused (403).
const enginePagesAppTest = `use std::sync::Arc;
use std::sync::atomic::{AtomicUsize, Ordering};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_engine_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_engine_api_topcoat::api::{types, Implementations, NotesImplementation, NotesListNotesArgs};
use schemas_fixture_engine_api_topcoat::{engine, PageAuthenticator, RouterBuilderFixtureEngineApiExt};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{header, to_bytes, Body, OriginPolicy, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

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
            let permissions: Vec<&str> = match subject {
                "nobody" => vec![],
                "reader" => vec!["Note.read"],
                "clerk" | "boss" => vec!["SupplierContractAmendmentRequest", "Request", "requests.approve"],
                _ => vec!["Note"],
            };
            Principal::new(subject, permissions)
        }))
    }
}

// A page's caller is the API's, from the same token.
struct Pages;

#[async_trait]
impl PageAuthenticator for Pages {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        Bearer.authenticate(parts(cx)).await
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

fn app() -> Router {
    app_with(OriginPolicy::new())
}

// The app with origins as its origin policy, its engine holding
// SupplierContractAmendmentRequest (long_type) beside the document's types.
fn app_with(origins: OriginPolicy) -> Router {
    static APPS: AtomicUsize = AtomicUsize::new(0);
    let n = APPS.fetch_add(1, Ordering::SeqCst);
    let path = std::env::temp_dir().join(format!("topcoat-engine-pages-{}-{n}.db", std::process::id()));
    let _ = std::fs::remove_file(&path);
    let engine = engine::open(&path).expect("open the engine");
    engine.deploy(&[long_type()], Some("fixture-engine-db")).expect("deploy SupplierContractAmendmentRequest");
    let implementations = Implementations { notes: Arc::new(Notes), authenticator: Arc::new(Bearer) };
    Router::builder().origin_policy(origins).discover().fixture_engine_api(implementations, Pages, engine).build()
}

// Request's schema document as SupplierContractAmendmentRequest: the MCP
// handles of its withdrawSignOff and listSignOffs tools
// (supplier_contract_amendment_request_withdraw_sign_off) are longer than
// MCP's 48 characters, so the engine hides them from MCP.
fn long_type() -> serde_json::Value {
    let document: serde_json::Value = serde_json::from_str(schemas_fixture_engine_db_types::ENGINE_DOCUMENT).expect("the engine document");
    let request = document["schemas"].as_array().into_iter().flatten().find(|schema| schema["name"] == "Request").expect("Request's schema");
    serde_json::from_str(&request.to_string().replace("\"Request\"", "\"SupplierContractAmendmentRequest\"")).expect("the renamed schema")
}

struct Answer {
    status: StatusCode,
    body: String,
    location: Option<String>,
}

// Sends a request as bearer, a form's pairs to a page and JSON to the
// engine's routes, and holds every HTML page to the enter rule.
async fn send(router: &Router, method: &str, uri: &str, bearer: Option<&str>, body: &str) -> Answer {
    send_with(router, method, uri, bearer, &[], body).await
}

// send with headers besides.
async fn send_with(router: &Router, method: &str, uri: &str, bearer: Option<&str>, headers: &[(&str, &str)], body: &str) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri);
    if let Some(bearer) = bearer {
        request = request.header("authorization", format!("Bearer {bearer}"));
    }
    for (name, value) in headers {
        request = request.header(*name, *value);
    }
    if !body.is_empty() {
        let kind = if uri.starts_with("/console") { "application/x-www-form-urlencoded" } else { "application/json" };
        request = request.header(header::CONTENT_TYPE, kind);
    }
    let response = router.handle(request.body(Body::from(body.to_string())).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let location = headers.get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &body);
    Answer { status, body, location }
}

async fn page(router: &Router, uri: &str, bearer: Option<&str>, want: StatusCode) -> String {
    let answer = send(router, "GET", uri, bearer, "").await;
    assert_eq!(answer.status, want, "GET {uri} as {bearer:?}:\n{}", answer.body);
    answer.body
}

// The href of the pager's link rel, as a request sends it.
fn link(html: &str, rel: &str) -> String {
    let start = format!("rel=\"{rel}\" href=\"");
    let at = html.find(&start).unwrap_or_else(|| panic!("no {rel} link in:\n{html}")) + start.len();
    html[at..at + html[at..].find('"').unwrap()].replace("&amp;", "&")
}

// The seq an instance's page puts in its writes' forms.
fn seq(html: &str) -> i64 {
    let start = "name=\"expectedSeq\" value=\"";
    let at = html.find(start).unwrap_or_else(|| panic!("no seq in:\n{html}")) + start.len();
    html[at..at + html[at..].find('"').unwrap()].parse().unwrap()
}

const NOTES: &str = "/engine/namespaces/fixture-engine-db/schemas/Note/instances";
const NOTE: &str = "/console/engine/Note/n1";
const COMMENT: &str = "/console/engine/Note/n1/operations/comment";

#[tokio::test(flavor = "multi_thread")]
async fn the_console_pages_the_engine() {
    let router = app();
    for (id, title) in [("n1", "Hello"), ("n2", "Second"), ("n3", "Third")] {
        let body = format!("{{\"id\": \"{id}\", \"data\": {{\"title\": \"{title}\"}}}}");
        let answer = send(&router, "POST", NOTES, Some("ada"), &body).await;
        assert_eq!(answer.status, StatusCode::CREATED, "{}", answer.body);
    }

    // The nav and the index link the engine's types for a reader.
    let index = page(&router, "/console", Some("ada"), StatusCode::OK).await;
    assert!(index.contains("<a href=\"/console/engine\">Engine</a>"), "{index}");
    let types = page(&router, "/console/engine", Some("ada"), StatusCode::OK).await;
    assert!(types.contains("<h1>Engine</h1>") && types.contains("<a href=\"/console/engine/Note\">Note</a>"), "{types}");
    assert!(types.contains("A note, which collects comments."), "{types}");

    // Not for a caller with no permission, whom the policy refuses a
    // note; nor for no caller, who is asked to sign in.
    let index = page(&router, "/console", Some("nobody"), StatusCode::OK).await;
    assert!(!index.contains("/console/engine"), "{index}");
    let types = page(&router, "/console/engine", Some("nobody"), StatusCode::OK).await;
    assert!(types.contains("There is no type here you may read."), "{types}");
    page(&router, "/console/engine/Note", Some("nobody"), StatusCode::FORBIDDEN).await;
    page(&router, NOTE, Some("nobody"), StatusCode::FORBIDDEN).await;
    page(&router, "/console/engine", None, StatusCode::UNAUTHORIZED).await;

    // A list pages forward by the engine's cursor, and back by its back.
    let first = page(&router, "/console/engine/Note?limit=2", Some("ada"), StatusCode::OK).await;
    for id in ["n1", "n2"] {
        assert!(first.contains(&format!("<a href=\"/console/engine/Note/{id}\">{id}</a>")), "{id}: {first}");
    }
    assert!(!first.contains("/console/engine/Note/n3") && !first.contains("rel=\"prev\""), "{first}");
    let next = link(&first, "next");
    assert!(next.starts_with("/console/engine/Note?limit=2&cursor="), "{next}");
    let second = page(&router, &next, Some("ada"), StatusCode::OK).await;
    assert!(second.contains("/console/engine/Note/n3") && !second.contains("/console/engine/Note/n1\""), "{second}");
    assert!(!second.contains("rel=\"next\""), "{second}");
    let again = page(&router, &link(&second, "prev"), Some("ada"), StatusCode::OK).await;
    assert!(again.contains("/console/engine/Note/n1\"") && again.contains("/console/engine/Note/n2\""), "{again}");
    page(&router, "/console/engine/Note?limit=many", Some("ada"), StatusCode::BAD_REQUEST).await;

    // An instance's page: its own fields, then Comments in the generic
    // panel, its fields and its operations' forms, after the trail.
    let note = page(&router, NOTE, Some("ada"), StatusCode::OK).await;
    for want in [
        "<h1>Note n1</h1>",
        "<a href=\"/console/engine\">Engine</a>",
        "<a href=\"/console/engine/Note\">Note</a>",
        "<span aria-current=\"page\">Note n1</span>",
        "<div data-field=\"title\"><dt>Title</dt><dd>Hello</dd></div>",
        "<section class=\"ss-panel\" data-behavior=\"Comments\">",
        "<div data-field=\"Comments.commentCount\"><dt>Comment count</dt><dd>0</dd></div>",
        "<form method=\"post\" action=\"/console/engine/Note/n1/operations/comment\" class=\"ss-form\">",
        "<form method=\"get\" action=\"/console/engine/Note/n1/operations/listComments\" class=\"ss-form\">",
        "<input id=\"engine-comment-replyTo\" name=\"replyTo\" type=\"number\" step=\"1\" min=\"1\"",
    ] {
        assert!(note.contains(want), "{want}:\n{note}");
    }
    let read_at = seq(&note);

    // A comment posted goes back to the note, which counts it.
    let answer = send(&router, "POST", COMMENT, Some("ada"), &format!("body=First+comment&expectedSeq={read_at}")).await;
    assert_eq!(answer.status, StatusCode::SEE_OTHER, "{}", answer.body);
    assert_eq!(answer.location.as_deref(), Some(NOTE));
    let note = page(&router, NOTE, Some("ada"), StatusCode::OK).await;
    assert!(note.contains("<dt>Comment count</dt><dd>1</dd>"), "{note}");
    let now = seq(&note);
    assert!(now > read_at, "{now} {read_at}");

    // A form sent from a page read before is refused, its message the
    // form's, and the form keeps what was sent.
    let answer = send(&router, "POST", COMMENT, Some("ada"), &format!("body=Late&expectedSeq={read_at}")).await;
    assert_eq!(answer.status, StatusCode::CONFLICT, "{}", answer.body);
    assert!(answer.body.contains("<p class=\"form-error\">") && answer.body.contains("value=\"Late\""), "{}", answer.body);

    // What the form or the engine refuses is at its control.
    for (sent, want) in [
        ("body=Reply&replyTo=99", "Note n1 has no comment 99"),
        ("body=&replyTo=1", "Enter a value"),
        ("body=Reply&replyTo=first", "Enter a whole number"),
    ] {
        let answer = send(&router, "POST", COMMENT, Some("ada"), &format!("{sent}&expectedSeq={now}")).await;
        assert_eq!(answer.status, StatusCode::UNPROCESSABLE_ENTITY, "{sent}: {}", answer.body);
        assert!(answer.body.contains(&format!("<p class=\"field-error\">{want}</p>")), "{sent}: {}", answer.body);
        assert!(answer.body.contains("aria-invalid=\"true\""), "{sent}: {}", answer.body);
    }

    // A read runs from its GET form, its result on the page.
    let read = page(&router, "/console/engine/Note/n1/operations/listComments?limit=10", Some("ada"), StatusCode::OK).await;
    assert!(read.contains("<section class=\"ss-result\" aria-label=\"Result\">") && read.contains("<td>First comment</td>"), "{read}");
    assert!(read.contains("<span aria-current=\"page\">List comments</span>"), "{read}");
    page(&router, "/console/engine/Note/n1/operations/comment?body=x", Some("ada"), StatusCode::METHOD_NOT_ALLOWED).await;

    // A panel's form fills in its values, and holds the hidden ones.
    let form = page(&router, "/console/engine/Note/n1/panel-form", Some("ada"), StatusCode::OK).await;
    for want in [
        "<form method=\"post\" action=\"/console/engine/Note/n1/operations/comment\" class=\"ss-form\">",
        "name=\"body\" type=\"text\" required=\"\" minlength=\"1\" maxlength=\"10000\" value=\"Filled\"",
        "<input type=\"hidden\" name=\"replyTo\" value=\"1\">",
    ] {
        assert!(form.contains(want), "{want}:\n{form}");
    }
    assert!(!form.contains("expectedSeq"), "{form}");
    let answer = send(&router, "POST", COMMENT, Some("ada"), "body=Without+a+seq").await;
    assert_eq!(answer.status, StatusCode::SEE_OTHER, "{}", answer.body);

    // What is not there is 404.
    page(&router, "/console/engine/Note/n9", Some("ada"), StatusCode::NOT_FOUND).await;
    let answer = send(&router, "POST", "/console/engine/Note/n1/operations/nothing", Some("ada"), "body=x").await;
    assert_eq!(answer.status, StatusCode::NOT_FOUND, "{}", answer.body);
}

const AMENDMENTS: &str = "/engine/namespaces/fixture-engine-db/schemas/SupplierContractAmendmentRequest/instances";
const AMENDMENT: &str = "/console/engine/SupplierContractAmendmentRequest/r1";

#[tokio::test(flavor = "multi_thread")]
async fn the_console_offers_what_the_deployment_and_the_policy_allow() {
    let router = app();
    let answer = send(&router, "POST", NOTES, Some("ada"), r#"{"id": "n1", "data": {"title": "Hello"}}"#).await;
    assert_eq!(answer.status, StatusCode::CREATED, "{}", answer.body);

    // A reader of notes lists a note's comments and has no comment form;
    // the comment they post anyway is the policy's refusal (403), while an
    // operation there is not stays 404.
    let note = page(&router, NOTE, Some("reader"), StatusCode::OK).await;
    assert!(note.contains("/operations/listComments") && !note.contains("/operations/comment\""), "{note}");
    let answer = send(&router, "POST", COMMENT, Some("reader"), "body=Hi").await;
    assert_eq!(answer.status, StatusCode::FORBIDDEN, "{}", answer.body);
    assert!(answer.body.contains("the access policy refuses reader write on Note (comment)"), "{}", answer.body);
    let answer = send(&router, "POST", "/console/engine/Note/n1/operations/nothing", Some("reader"), "body=x").await;
    assert_eq!(answer.status, StatusCode::NOT_FOUND, "{}", answer.body);

    // The engine hides withdrawSignOff from MCP for its handle's length,
    // which is no reason to keep it from the console. Its creator, boss,
    // may not sign it off, so clerk does.
    let answer = send(&router, "POST", AMENDMENTS, Some("boss"), r#"{"id": "r1", "data": {"title": "Amend the contract"}}"#).await;
    assert_eq!(answer.status, StatusCode::CREATED, "{}", answer.body);
    page(&router, AMENDMENT, Some("clerk"), StatusCode::OK).await;
    for operation in ["signOff", "withdrawSignOff"] {
        let answer = send(&router, "POST", &format!("{AMENDMENT}/operations/{operation}"), Some("clerk"), "state=approved").await;
        assert_eq!(answer.status, StatusCode::SEE_OTHER, "{operation}: {}", answer.body);
        assert_eq!(answer.location.as_deref(), Some(AMENDMENT));
    }

    // listSignOffs, which the deployment hides, is not there: on a type
    // whose handle is too long too, and on Request, whose is not.
    page(&router, &format!("{AMENDMENT}/operations/listSignOffs"), Some("clerk"), StatusCode::NOT_FOUND).await;
    let answer = send(&router, "POST", "/engine/namespaces/fixture-engine-db/schemas/Request/instances", Some("clerk"), r#"{"id": "q1", "data": {"title": "Buy chairs"}}"#).await;
    assert_eq!(answer.status, StatusCode::CREATED, "{}", answer.body);
    let request = page(&router, "/console/engine/Request/q1", Some("clerk"), StatusCode::OK).await;
    assert!(!request.contains("/operations/listSignOffs"), "{request}");
    page(&router, "/console/engine/Request/q1/operations/listSignOffs", Some("clerk"), StatusCode::NOT_FOUND).await;
}

#[tokio::test(flavor = "multi_thread")]
async fn a_write_another_site_sent_is_refused() {
    // The app's origin policy is off, so the refusal is the engine pages'.
    let router = app_with(OriginPolicy::dangerous_disable());
    let answer = send(&router, "POST", NOTES, Some("ada"), r#"{"id": "n1", "data": {"title": "Hello"}}"#).await;
    assert_eq!(answer.status, StatusCode::CREATED, "{}", answer.body);
    for headers in [
        [("origin", "https://evil.example"), ("sec-fetch-site", "cross-site")],
        [("origin", "https://evil.example"), ("host", "console.example")],
        [("origin", "null"), ("host", "console.example")],
    ] {
        let answer = send_with(&router, "POST", COMMENT, Some("ada"), &headers, "body=Forged").await;
        assert_eq!(answer.status, StatusCode::FORBIDDEN, "{headers:?}: {}", answer.body);
    }
    // A read is no write, and a post from the page's own origin passes.
    let read = send_with(&router, "GET", "/console/engine/Note/n1/operations/listComments", Some("ada"), &[("origin", "https://evil.example"), ("sec-fetch-site", "cross-site")], "").await;
    assert_eq!(read.status, StatusCode::OK, "{}", read.body);
    for headers in [
        [("sec-fetch-site", "same-origin"), ("origin", "https://console.example")],
        [("origin", "https://console.example"), ("host", "console.example")],
    ] {
        let answer = send_with(&router, "POST", COMMENT, Some("ada"), &headers, "body=Mine").await;
        assert_eq!(answer.status, StatusCode::SEE_OTHER, "{headers:?}: {}", answer.body);
    }
    let note = page(&router, NOTE, Some("ada"), StatusCode::OK).await;
    assert!(note.contains("<dt>Comment count</dt><dd>2</dd>"), "{note}");
}
`
