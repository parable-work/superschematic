package topcoat_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
)

// triageService is the loader's fixture whose @triageOperations set serves
// the inbound path's triage of fixture-parties-db (#746), and triageDB its
// database, whose Rust types its types name (the party table, Contact).
const (
	triageService = "fixture-triage-api"
	triageDB      = "fixture-parties-db"
)

// triageDBOutputs are fixture-parties-db's outputs as the Topcoat crate's
// build reads them: its Rust types (and its Go types, which the Go ORM a
// DB service always writes imports), on Postgres alone, since the Rust types
// of a database's conversations are Postgres's (D193).
func triageDBOutputs() map[string]map[string]any {
	return map[string]map[string]any{triageDB: {
		"types": map[string]any{"rust": map[string]any{"enabled": true}, "go": map[string]any{"enabled": true}},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}}
}

// TestTheTriageIsAConsolePage builds fixture-triage-api's crate with the
// pages on (#746): its triage operations describe a resource of their own,
// the held messages, at /console/triage, with a list filtered by channel
// that pages, an item's page with its detail and its files, and the settle
// and dismiss forms on it, the settle's party a picker of the contacts
// listContacts lists and its trust a select; the nav offers the list to a
// caller the list's guard admits, the triage permission's holder.
func TestTheTriageIsAConsolePage(t *testing.T) {
	files, log := readCrate(t, registry.DefaultNaming(), triageService, pagesOutputs())
	pages := files["pages.rs"]
	for _, want := range []string{
		`pub const RESOURCES: &[&str] = &[`,
		`let link = Link { href: "/console/triage", label: "Held messages", icon: Some("inbox") };`,
		`#[page("/console/triage")]`,
		`pub async fn triage_list_page(cx: &Cx, Form(filter): Form<TriageTriageArgsForm>)`,
		`#[page("/console/triage/{triage_id}")]`,
		`#[page(POST "/console/triage/{triage_id}/dismiss")]`,
		`#[page(POST "/console/triage/{triage_id}/settle")]`,
		`references::triage_settle_onto_args_form_choices(cx, form, choices.clone())`,
		`<button type="submit">"Settle onto a party"</button>`,
		`<button type="submit">"Dismiss"</button>`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("fixture-triage-api's pages.rs lacks %s", want)
		}
	}
	views := files["views.rs"]
	for _, want := range []string{
		`triage_attachment_table(rows: record.attachments, choices: choices.under("attachments"))`,
		`<dt>"From"</dt>`,
		`<dt>"Received"</dt>`,
	} {
		if !strings.Contains(views, want) {
			t.Errorf("fixture-triage-api's views.rs lacks %s", want)
		}
	}
	if forms := files["forms.rs"]; !strings.Contains(forms, `"Trust the sender's address"`) || !strings.Contains(forms, `<option value="verified"`) {
		t.Errorf("fixture-triage-api's forms.rs has no trust select:\n%s", forms)
	}
	if strings.Contains(log, "no page") && strings.Contains(log, "triage") {
		t.Errorf("the build log leaves a triage operation out of the pages:\n%s", log)
	}
}

// TestTheTriagePagesServeATopcoatApp builds fixture-triage-api's crates
// with the pages on, over fixture-parties-db's Rust types, and runs cargo
// clippy, then cargo test, with triageAppTest: a staff member who holds
// parties.triage finds the held messages in the nav and lists them, a page
// at a time and by channel; a clerk without it is offered no list and is
// refused it (403); a held message's page shows its sender, channel, body
// and files, with the settle form, whose party is a select of the contacts,
// and the dismiss form; settling posts through the operation and goes back
// to the message, now settled onto the contact and out of the list; a
// settle of a message no longer held is the operation's 409 at the form;
// and dismissing takes a message out of the list. The implementation is the
// test's, in memory: a Rust server keeps the triage operations its
// implementation's (#746).
func TestTheTriagePagesServeATopcoatApp(t *testing.T) {
	cargoTestCrateWith(t, triageService, triageAppTest, crateOptions{
		outputs:      pagesOutputs(),
		dependencies: []string{triageDB},
		depOutputs:   triageDBOutputs(),
		devDependencies: `serde_json = "1.0.149"
`,
	})
}

const triageAppTest = `use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use http::request::Parts;
use schemas_fixture_triage_api_topcoat::api::runtime::page::{Cursor, Page};
use schemas_fixture_triage_api_topcoat::api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use schemas_fixture_triage_api_topcoat::api::{
    types, ContactImplementation, Implementations, TriageDismissArgs, TriageImplementation, TriageSettleOntoArgs,
    TriageTriageArgs, TriageTriageMessageArgs,
};
use schemas_fixture_triage_api_topcoat::pages::Settings;
use schemas_fixture_triage_api_topcoat::{PageAuthenticator, RouterBuilderFixtureTriageApiExt};
use topcoat::context::Cx;
use topcoat::router::request::parts;
use topcoat::router::{header, to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const FAY: &str = "8d1f6c9e-0000-4000-8000-000000000001";
const DEE: &str = "8d1f6c9e-0000-4000-8000-000000000002";
const GATE: &str = "8d1f6c9e-0000-4000-8000-000000000101";
const HELLO: &str = "8d1f6c9e-0000-4000-8000-000000000102";
const MAIL: &str = "8d1f6c9e-0000-4000-8000-000000000103";

fn uuid(text: &str) -> types::IdentityUUID {
    serde_json::from_value(serde_json::json!(text)).unwrap()
}

// An id as the API's JSON writes it, which a row's link holds.
fn wire(text: &str) -> String {
    serde_json::to_value(uuid(text)).unwrap().as_str().unwrap().to_owned()
}

fn held(id: &str, channel: &str, from: &str, body: &str, at: &str, files: serde_json::Value) -> types::TriageMessage {
    serde_json::from_value(serde_json::json!({
        "id": id, "channel": channel, "slot": channel.to_uppercase(), "fromAddress": from, "body": body,
        "at": at, "state": "open", "disposition": "unknown", "attachments": files,
    }))
    .unwrap()
}

// The held messages and the contacts, in memory, as the inbound path holds
// them: newest first.
struct Store {
    messages: Mutex<Vec<types::TriageMessage>>,
    contacts: Vec<types::ContactView>,
}

impl Store {
    fn new() -> Arc<Self> {
        Arc::new(Store {
            messages: Mutex::new(vec![
                held(MAIL, "email", "someone@example.test", "Who is this?", "2026-11-28T10:02:00Z", serde_json::json!([])),
                held(HELLO, "sms", "+15550100043", "Hello?", "2026-11-28T10:01:00Z", serde_json::json!([])),
                held(GATE, "sms", "+15550100041", "Gate fixed", "2026-11-28T10:00:00Z", serde_json::json!([
                    { "id": "file-1", "name": "gate.png", "mediaType": "image/png", "size": 2048 }
                ])),
            ]),
            contacts: vec![
                types::ContactView { id: uuid(FAY), name: "Fay".to_owned() },
                types::ContactView { id: uuid(DEE), name: "Dee".to_owned() },
            ],
        })
    }
}

struct Triage(Arc<Store>);

#[async_trait]
impl ContactImplementation for Triage {
    async fn list_contacts(&self, _ctx: RequestContext) -> Result<Vec<types::ContactView>, ApiError> {
        Ok(self.0.contacts.clone())
    }
}

#[async_trait]
impl TriageImplementation for Triage {
    async fn triage(&self, _ctx: RequestContext, args: TriageTriageArgs) -> Result<Page<types::TriageMessage>, ApiError> {
        let offset: usize = match &args.page.cursor {
            Some(cursor) => cursor.decode()?,
            None => 0,
        };
        let limit = usize::try_from(args.page.limit).unwrap_or(1);
        let messages = self.0.messages.lock().unwrap();
        let open: Vec<types::TriageMessage> = messages
            .iter()
            .filter(|m| m.state == types::TriageState::Open && args.channel.as_ref().is_none_or(|c| &m.channel == c))
            .skip(offset)
            .take(limit + 1)
            .cloned()
            .collect();
        if open.len() > limit {
            return Ok(Page::more(open[..limit].to_vec(), Cursor::encode(&(offset + limit))?));
        }
        Ok(Page::last(open))
    }
    async fn triage_message(&self, _ctx: RequestContext, args: TriageTriageMessageArgs) -> Result<types::TriageMessage, ApiError> {
        let messages = self.0.messages.lock().unwrap();
        messages.iter().find(|m| m.id == args.message).cloned().ok_or_else(|| ApiError::not_found("no such held message"))
    }
    async fn dismiss(&self, ctx: RequestContext, args: TriageDismissArgs) -> Result<types::TriageMessage, ApiError> {
        let mut messages = self.0.messages.lock().unwrap();
        let message = messages.iter_mut().find(|m| m.id == args.message).ok_or_else(|| ApiError::not_found("no such held message"))?;
        if message.state != types::TriageState::Open {
            return Err(ApiError::conflict("The message is no longer held in triage"));
        }
        message.state = types::TriageState::Closed;
        message.closed_by = ctx.principal.map(|p| p.subject);
        Ok(message.clone())
    }
    async fn settle_onto(&self, ctx: RequestContext, args: TriageSettleOntoArgs) -> Result<types::TriageSettled, ApiError> {
        if !self.0.contacts.iter().any(|c| c.id == args.input.party) {
            return Err(ApiError::not_found("no such contact"));
        }
        let mut messages = self.0.messages.lock().unwrap();
        let message = messages.iter_mut().find(|m| m.id == args.message).ok_or_else(|| ApiError::not_found("no such held message"))?;
        if message.state != types::TriageState::Open {
            return Err(ApiError::conflict("The message is no longer held in triage"));
        }
        message.party_table = Some("Contact".to_owned());
        message.party = Some(args.input.party);
        let outcome = match args.input.trust {
            types::TriageTrust::Entered => {
                message.state = types::TriageState::Closed;
                message.trust = Some("entered".to_owned());
                message.disposition = Some("run".to_owned());
                message.closed_by = ctx.principal.map(|p| p.subject);
                types::TriageOutcome::Run
            }
            types::TriageTrust::Verified => {
                message.disposition = Some("verifying".to_owned());
                types::TriageOutcome::Verifying
            }
        };
        Ok(types::TriageSettled { outcome, message: args.message, party: args.input.party, grant: None })
    }
}

// The JSON API's callers: none, since these tests drive the pages.
struct NoRequests;

#[async_trait]
impl Authenticator for NoRequests {
    async fn authenticate(&self, _request: &Parts) -> Result<Option<Principal>, ApiError> {
        Ok(None)
    }
}

// A page's caller, by the x-caller header: the staff, who settle triage
// and read the contacts, a clerk, who reads the contacts alone, or none.
struct Callers;

#[async_trait]
impl PageAuthenticator for Callers {
    async fn principal(&self, cx: &Cx) -> Result<Option<Principal>, ApiError> {
        let caller = parts(cx).headers.get("x-caller").and_then(|value| value.to_str().ok()).unwrap_or_default();
        let permissions: &[&str] = match caller {
            "staff" => &["parties.triage", "contacts.read"],
            "clerk" => &["contacts.read"],
            _ => return Ok(None),
        };
        Ok(Some(Principal::new(caller, permissions.iter().copied())))
    }
}

fn app(store: &Arc<Store>) -> Router {
    let implementations = Implementations {
        contact: Arc::new(Triage(Arc::clone(store))),
        triage: Arc::new(Triage(Arc::clone(store))),
        authenticator: Arc::new(NoRequests),
    };
    Router::builder()
        .discover()
        .with(Settings::new().title("Office").sign_in("/sign-in"))
        .fixture_triage_api(implementations, Callers)
        .build()
}

struct Answer {
    status: StatusCode,
    html: String,
    location: Option<String>,
}

async fn send(router: &Router, method: &str, uri: &str, caller: &str, body: &str) -> Answer {
    let mut request = http::Request::builder().method(method).uri(uri);
    if !caller.is_empty() {
        request = request.header("x-caller", caller);
    }
    if method == "POST" {
        request = request.header(header::CONTENT_TYPE, "application/x-www-form-urlencoded");
    }
    let response = router.handle(request.body(Body::from(body.to_owned())).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let location = headers.get(header::LOCATION).map(|value| value.to_str().unwrap().to_owned());
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let html = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &html);
    Answer { status, html, location }
}

async fn get(router: &Router, uri: &str, caller: &str) -> Answer {
    send(router, "GET", uri, caller, "").await
}

async fn post(router: &Router, uri: &str, caller: &str, body: &str) -> Answer {
    send(router, "POST", uri, caller, body).await
}

#[tokio::test]
async fn the_held_messages_are_offered_and_listed_to_the_triage_permission_alone() {
    let app = app(&Store::new());
    let index = get(&app, "/console", "staff").await;
    assert_eq!(index.status, StatusCode::OK, "{}", index.html);
    assert!(index.html.contains(r#"<a href="/console/triage"><span class="ss-icon" data-icon="inbox" aria-hidden="true"></span>Held messages</a>"#), "{}", index.html);

    let list = get(&app, "/console/triage", "staff").await;
    assert_eq!(list.status, StatusCode::OK, "{}", list.html);
    assert!(list.html.contains("<h1>Held messages</h1>"), "{}", list.html);
    for (id, from) in [(MAIL, "someone@example.test"), (HELLO, "+15550100043"), (GATE, "+15550100041")] {
        let link = format!(r#"<a href="/console/triage/{}">{from}</a>"#, wire(id));
        assert!(list.html.contains(&link), "the list lacks {link}: {}", list.html);
    }
    // The filter is the channel.
    assert!(list.html.contains(r#"name="channel""#), "{}", list.html);

    // A clerk is offered no list, and is refused it and a message's page.
    let index = get(&app, "/console", "clerk").await;
    assert!(!index.html.contains("/console/triage"), "a clerk is offered the triage: {}", index.html);
    let refused = get(&app, "/console/triage", "clerk").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
    assert!(!refused.html.contains("Gate fixed"), "{}", refused.html);
    let refused = get(&app, &format!("/console/triage/{GATE}"), "clerk").await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
    // Signed out, the list asks for a sign-in.
    let refused = get(&app, "/console/triage", "").await;
    assert_eq!(refused.status, StatusCode::UNAUTHORIZED, "{}", refused.html);
}

#[tokio::test]
async fn the_held_messages_page_and_filter_by_channel() {
    let app = app(&Store::new());
    let first = get(&app, "/console/triage?limit=2", "staff").await;
    assert_eq!(first.status, StatusCode::OK, "{}", first.html);
    assert!(first.html.contains("someone@example.test") && first.html.contains("+15550100043"), "{}", first.html);
    assert!(!first.html.contains("+15550100041"), "{}", first.html);
    let next = first.html.split(r#"<a rel="next" href=""#).nth(1).unwrap_or_default();
    let next = next.split('"').next().unwrap_or_default().replace("&amp;", "&");
    assert!(next.starts_with("/console/triage?"), "no next page's link: {}", first.html);
    let second = get(&app, &next, "staff").await;
    assert_eq!(second.status, StatusCode::OK, "{}", second.html);
    assert!(second.html.contains("+15550100041") && !second.html.contains("someone@example.test"), "{}", second.html);

    let texts = get(&app, "/console/triage?channel=email", "staff").await;
    assert_eq!(texts.status, StatusCode::OK, "{}", texts.html);
    assert!(texts.html.contains("someone@example.test") && !texts.html.contains("+15550100043"), "{}", texts.html);
}

#[tokio::test]
async fn a_held_message_shows_its_sender_its_channel_its_body_and_its_files_and_settles_onto_a_contact() {
    let store = Store::new();
    let app = app(&store);
    let item = get(&app, &format!("/console/triage/{GATE}"), "staff").await;
    assert_eq!(item.status, StatusCode::OK, "{}", item.html);
    for want in [
        "<h1>+15550100041</h1>".to_owned(),
        r#"<div data-field="fromAddress"><dt>From</dt><dd>+15550100041</dd></div>"#.to_owned(),
        r#"<div data-field="channel"><dt>Channel</dt><dd>sms</dd></div>"#.to_owned(),
        r#"<div data-field="body"><dt>Body</dt><dd>Gate fixed</dd></div>"#.to_owned(),
        "gate.png".to_owned(),
        "image/png".to_owned(),
        format!(r#"<form method="post" action="/console/triage/{GATE}/settle" class="ss-action" data-action="settle">"#),
        format!(r#"<form method="post" action="/console/triage/{GATE}/dismiss" class="ss-action" data-action="dismiss">"#),
        // The party is a select of the contacts listContacts lists.
        format!(r#"<option value="{}">Fay</option>"#, wire(FAY)),
        format!(r#"<option value="{}">Dee</option>"#, wire(DEE)),
        r#"<option value="verified""#.to_owned(),
        "Trust the sender's address".to_owned(),
    ] {
        assert!(item.html.contains(&want), "the message's page lacks {want}: {}", item.html);
    }

    let settled = post(&app, &format!("/console/triage/{GATE}/settle"), "staff", &format!("party={}&trust=entered", wire(FAY))).await;
    assert_eq!(settled.status, StatusCode::SEE_OTHER, "{}", settled.html);
    assert_eq!(settled.location.as_deref(), Some(format!("/console/triage/{GATE}").as_str()));
    {
        let messages = store.messages.lock().unwrap();
        let gate = messages.iter().find(|m| m.id == uuid(GATE)).unwrap();
        assert_eq!(gate.party, Some(uuid(FAY)));
        assert_eq!(gate.state, types::TriageState::Closed);
        assert_eq!(gate.closed_by.as_deref(), Some("staff"));
    }
    let shown = get(&app, &format!("/console/triage/{GATE}"), "staff").await;
    assert!(shown.html.contains(r#"<data value="closed">"#), "{}", shown.html);
    let list = get(&app, "/console/triage", "staff").await;
    assert!(!list.html.contains("+15550100041"), "a settled message is still listed: {}", list.html);

    // A settle of a message no longer held is the operation's 409, at the form.
    let again = post(&app, &format!("/console/triage/{GATE}/settle"), "staff", &format!("party={}&trust=entered", wire(DEE))).await;
    assert_eq!(again.status, StatusCode::CONFLICT, "{}", again.html);
    assert!(again.html.contains("The message is no longer held in triage"), "{}", again.html);

    // A clerk may not settle.
    let refused = post(&app, &format!("/console/triage/{HELLO}/settle"), "clerk", &format!("party={}&trust=entered", wire(FAY))).await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
}

#[tokio::test]
async fn dismissing_a_held_message_takes_it_out_of_the_list() {
    let store = Store::new();
    let app = app(&store);
    let dismissed = post(&app, &format!("/console/triage/{HELLO}/dismiss"), "staff", &format!("message={HELLO}")).await;
    assert_eq!(dismissed.status, StatusCode::SEE_OTHER, "{}", dismissed.html);
    let list = get(&app, "/console/triage", "staff").await;
    assert!(!list.html.contains("+15550100043"), "a dismissed message is still listed: {}", list.html);
    let refused = post(&app, &format!("/console/triage/{MAIL}/dismiss"), "clerk", &format!("message={MAIL}")).await;
    assert_eq!(refused.status, StatusCode::FORBIDDEN, "{}", refused.html);
}
`
