package topcoat_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/topcoat"
	"github.com/parable-work/superschematic/internal/testpaths"
	"github.com/parable-work/superschematic/registry"
)

// signUpOutputs are pagesOutputs whose pages.identity asks for the sign-up
// page.
func signUpOutputs() map[string]any {
	outputs := rustOutputs()
	outputs["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "identity": map[string]any{"signUp": true}}}
	return outputs
}

// identityPagesCopy is a copy of fixture-identity-pages-api whose schema
// edit changes.
func identityPagesCopy(t *testing.T, edit func(schema string) string) string {
	t.Helper()
	dir := filepath.Join(testpaths.TempDir(t), identityPagesService)
	for _, file := range []string{"schema.config.yaml", filepath.Join("src", "notes.schema.yaml")} {
		data, err := os.ReadFile(filepath.Join(serviceDir(identityPagesService), file))
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
		writeFile(t, filepath.Join(dir, file), text)
	}
	return dir
}

// TestTheIdentityPagesAreWrittenAsDeclared builds fixture-identity-pages-api
// and copies of it: the identity pages are written with the pages, and
// sign-up only when pages.identity.signUp or the API's
// @userSessions({ register: true }) asks for it, the build log saying why
// it is left out; pages.identity false writes none; and a resource whose
// pages take one of their paths leaves them out, which the log says. The
// pages that sign in, sign up and change a password take the rate limit
// of the API's route that does the same, or, on an API without it, one of
// their own at the route's rate.
func TestTheIdentityPagesAreWrittenAsDeclared(t *testing.T) {
	noSignUp := "  - topcoat: no sign-up page under /console: neither pages.identity.signUp nor the API's @userSessions({ register: true }) opens registration\n"
	build := func(dir string, outputs map[string]any) (log string, pages string, ok bool) {
		t.Helper()
		var out strings.Builder
		root := testpaths.TempDir(t)
		if _, err := buildDir(t, registry.DefaultNaming(), dir, root, outputs, &out); err != nil {
			t.Fatalf("build %s: %v", dir, err)
		}
		data, err := os.ReadFile(filepath.Join(topcoat.Dir(root, identityPagesService), "src", "identity_pages.rs"))
		return out.String(), string(data), err == nil
	}
	fixture := serviceDir(identityPagesService)
	signUp := `#[page(POST "/console/sign-up")]`

	log, pages, ok := build(fixture, pagesOutputs())
	if !ok || strings.Contains(pages, signUp) || !strings.Contains(log, noSignUp) {
		t.Errorf("the default pages: identity pages written %v, sign-up in them %v, log:\n%s", ok, strings.Contains(pages, signUp), log)
	}
	if strings.Contains(log, "the identity pages are not built yet") {
		t.Errorf("the log still says the identity pages are not built:\n%s", log)
	}
	for _, want := range []string{
		"const SIGN_IN_LIMIT: &OperationInfo = &crate::api::operations::ACCOUNT_LOGIN;",
		"const CHANGE_PASSWORD_LIMIT: &OperationInfo = &crate::api::operations::ACCOUNT_CHANGE_PASSWORD;",
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("the default pages lack %s", want)
		}
	}
	log, pages, ok = build(fixture, signUpOutputs())
	if !ok || !strings.Contains(pages, signUp) || strings.Contains(log, "no sign-up page") {
		t.Errorf("pages.identity { signUp: true }: identity pages written %v, sign-up in them %v, log:\n%s", ok, strings.Contains(pages, signUp), log)
	}
	own := `const SIGN_UP_LIMIT: &OperationInfo = &OperationInfo { namespace: "topcoat", name: "signUp", method: "POST", path: "/console/sign-up", requires_auth: false, permissions: &[], require_ownership: false, manual: false, rate_limit: Some(5), service: None };`
	if !strings.Contains(pages, own) {
		t.Errorf("sign-up without the API's register route lacks its own limit %s", own)
	}
	noLogin := identityPagesCopy(t, func(schema string) string {
		return strings.Replace(schema, "userSessions: {}", "userSessions: { noLogin: true }", 1)
	})
	_, pages, _ = build(noLogin, pagesOutputs())
	for _, want := range []string{
		`const SIGN_IN_LIMIT: &OperationInfo = &OperationInfo { namespace: "topcoat", name: "signIn", method: "POST", path: "/console/sign-in", requires_auth: false, permissions: &[], require_ownership: false, manual: false, rate_limit: Some(10), service: None };`,
		`const CHANGE_PASSWORD_LIMIT: &OperationInfo = &OperationInfo { namespace: "topcoat", name: "changePassword", method: "POST", path: "/console/account", requires_auth: false, permissions: &[], require_ownership: false, manual: false, rate_limit: Some(10), service: None };`,
	} {
		if !strings.Contains(pages, want) {
			t.Errorf("@userSessions({ login: false }): the pages lack their own limit %s", want)
		}
	}
	registers := identityPagesCopy(t, func(schema string) string {
		return strings.Replace(schema, "userSessions: {}", "userSessions: { register: true }", 1)
	})
	log, pages, ok = build(registers, pagesOutputs())
	if !ok || !strings.Contains(pages, signUp) || strings.Contains(log, "no sign-up page") {
		t.Errorf("@userSessions({ register: true }): identity pages written %v, sign-up in them %v, log:\n%s", ok, strings.Contains(pages, signUp), log)
	}
	if want := "const SIGN_UP_LIMIT: &OperationInfo = &crate::api::operations::ACCOUNT_REGISTER;"; !strings.Contains(pages, want) {
		t.Errorf("@userSessions({ register: true }): the pages lack %s", want)
	}

	off := rustOutputs()
	off["topcoat"] = map[string]any{"enabled": true, "pages": map[string]any{"enabled": true, "identity": false}}
	if log, _, ok := build(fixture, off); ok || strings.Contains(log, "no sign-up page") {
		t.Errorf("pages.identity false: identity pages written %v, log:\n%s", ok, log)
	}

	taken := identityPagesCopy(t, func(schema string) string {
		return strings.ReplaceAll(schema, "restPath: notes", "restPath: users")
	})
	want := "  - topcoat: no sign-in, account, users or roles pages under /console: the pages of users are at /console/users, where the identity pages would be\n"
	if log, _, ok := build(taken, pagesOutputs()); ok || !strings.Contains(log, want) {
		t.Errorf("a resource at /console/users: identity pages written %v, log:\n%s", ok, log)
	}
}

// TestTheConsoleSignsInAndAdministers builds fixture-identity-pages-api's
// crates over the authDb's SQLite DDL, without and with pages.identity's
// signUp, and runs identityPagesAppTest through Router::handle, holding
// every answer to the HTML implicit-submission rule (enterHelper): a
// caller refused 401 signs in on the generated page and comes back to
// next, and a next off the site goes to the index; a wrong password is 401
// with its message, and no password renders again; signing out ends the
// session and clears its cookie; another site's sign-in is refused; a
// password change ends the caller's other sessions; sign-up is served only
// with the flag, a taken login 409; an admin adds users and roles, grants
// and revokes a role, disables, enables and sets a password, and edits
// and deletes a role, a malformed permission 422 and one the admin does
// not hold 403 at the form; a member is refused the users and roles
// pages (403), whose links their nav does not show; and the 11th sign-in
// or password change in a minute, or the 6th sign-up, is refused 429 with
// Retry-After. The crate with sign-up is built over a copy of the fixture
// whose @userSessions has login: false, so its pages take their own
// limiters; the one without takes the API's routes', which the JSON login
// shares.
func TestTheConsoleSignsInAndAdministers(t *testing.T) {
	ddl, err := os.ReadFile("../../runtime/http/testdata/identity/sqlite/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	noLogin := identityPagesCopy(t, func(schema string) string {
		return strings.Replace(schema, "userSessions: {}", "userSessions: { noLogin: true }", 1)
	})
	for _, variant := range []struct {
		name     string
		dir      string
		outputs  map[string]any
		signUp   string
		apiLogin string
	}{
		{"without sign-up", "", pagesOutputs(), "false", "true"},
		{"with sign-up, without the API's login", noLogin, signUpOutputs(), "true", "false"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			consts := "\nconst SIGN_UP: bool = " + variant.signUp + ";\nconst API_LOGIN: bool = " + variant.apiLogin + ";\n"
			cargoTestCrateWith(t, identityPagesService, identityPagesAppTest+consts, crateOptions{
				dir:             variant.dir,
				features:        []string{"identity-sqlite"},
				devDependencies: `rusqlite = { version = "0.40.2", features = ["bundled"] }` + "\n",
				files:           map[string]string{"identity.sql": string(ddl)},
				outputs:         variant.outputs,
			})
		})
	}
}

// identityPagesAppTest is tests/app.rs of fixture-identity-pages-api's
// Topcoat crate, beside tests/identity.sql, the DDL of
// fixture-user-model-db that the identity runtimes' stores run against
// (runtime/http/testdata). The test appends SIGN_UP, whether the crate was
// built with the sign-up page, and API_LOGIN, whether its API serves the
// login and changePassword routes.
const identityPagesAppTest = `use std::sync::Arc;
use std::time::SystemTime;

use async_trait::async_trait;
use schemas_fixture_identity_pages_api_topcoat::api::runtime::identity::{
    hash_password, Config, IdentityAuthenticator, NewUser, Rusqlite, SqlStore, Store, HOST_COOKIE_NAME,
};
use schemas_fixture_identity_pages_api_topcoat::api::runtime::{ApiError, RequestContext};
use schemas_fixture_identity_pages_api_topcoat::api::{
    identity, types, Implementations, NoteGetNoteArgs, NoteImplementation, NoteListNotesArgs,
};
use schemas_fixture_identity_pages_api_topcoat::{IdentityPageAuthenticator, RouterBuilderFixtureIdentityPagesApiExt};
use topcoat::router::{to_bytes, Body, Router, RouterBuilderDiscoverExt, StatusCode};

#[path = "support/enter.rs"]
mod enter;

const CONFIG: &str = r#"{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}"#;
const PASSWORD: &str = "correct horse";

struct Notes;

fn note() -> types::Note {
    serde_json::from_value(serde_json::json!({"id": "0b7f0c62-5d38-4c0a-9b1f-2a8e6f3c4d51", "title": "Shopping"})).unwrap()
}

#[async_trait]
impl NoteImplementation for Notes {
    async fn list_notes(&self, _ctx: RequestContext, _args: NoteListNotesArgs) -> Result<Vec<types::Note>, ApiError> {
        Ok(vec![note()])
    }

    async fn get_note(&self, _ctx: RequestContext, _args: NoteGetNoteArgs) -> Result<types::Note, ApiError> {
        Ok(note())
    }
}

// The API over a SQLite database of the authDb's tables: an administrator
// whose role holds the identity permissions and the notes', and a member
// holding none.
async fn app() -> Router {
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
    let admin = store.create_user(user("admin@example.com", "Admin")).await.unwrap();
    store.create_user(user("member@example.com", "Member")).await.unwrap();
    let role = store.create_role("admin", &["identity".to_string(), "notes".to_string()]).await.unwrap();
    store.grant_role(&admin.id, &role.id, SystemTime::now()).await.unwrap();
    let service = Arc::new(identity::service(store, config).unwrap());
    let implementations = Implementations { note: Arc::new(Notes), authenticator: Arc::new(IdentityAuthenticator::new(service)) };
    let pages = IdentityPageAuthenticator::of(&implementations);
    Router::builder().discover().fixture_identity_pages_api(implementations, pages).build()
}

struct Reply {
    status: StatusCode,
    location: Option<String>,
    retry_after: Option<String>,
    cookies: Vec<String>,
    body: String,
}

impl Reply {
    // The session cookie the reply sets, as a Cookie header carries it.
    fn session(&self) -> String {
        let cookie = self.cookies.iter().find(|cookie| cookie.starts_with(&format!("{HOST_COOKIE_NAME}="))).expect("the session cookie");
        cookie.split(';').next().unwrap().to_string()
    }

    fn clears(&self) -> bool {
        let cleared = format!("{HOST_COOKIE_NAME}=; Path=/; Max-Age=0");
        self.cookies.iter().any(|cookie| cookie.starts_with(&cleared))
    }
}

// Sends a request as a browser on the app's own site would: a post's form
// urlencoded, with Sec-Fetch-Site same-origin; and holds every answer to
// the HTML implicit-submission rule.
async fn send(router: &Router, method: &str, uri: &str, cookie: Option<&str>, form: &[(&str, &str)]) -> Reply {
    let mut request = http::Request::builder().method(method).uri(uri).header("host", "app.example.com");
    if let Some(cookie) = cookie {
        request = request.header("cookie", cookie);
    }
    let body = if method == "POST" {
        request = request.header("content-type", "application/x-www-form-urlencoded").header("sec-fetch-site", "same-origin");
        let encoded: Vec<String> = form.iter().map(|(name, value)| format!("{}={}", encode(name), encode(value))).collect();
        Body::from(encoded.join("&"))
    } else {
        Body::empty()
    };
    let response = router.handle(request.body(body).unwrap()).await;
    let (status, headers) = (response.status(), response.headers().clone());
    let location = headers.get("location").map(|value| value.to_str().unwrap().to_string());
    let retry_after = headers.get("retry-after").map(|value| value.to_str().unwrap().to_string());
    let cookies = headers.get_all("set-cookie").iter().map(|value| value.to_str().unwrap().to_string()).collect();
    let bytes = to_bytes(response.into_body(), usize::MAX).await.unwrap();
    let body = String::from_utf8(bytes.to_vec()).unwrap();
    enter::check_page(uri, &headers, &body);
    Reply { status, location, retry_after, cookies, body }
}

// Signs in through the API's JSON login route, with a wrong password: its
// status.
async fn json_login(router: &Router) -> StatusCode {
    let request = http::Request::builder()
        .method("POST")
        .uri("/api/auth/login")
        .header("host", "app.example.com")
        .header("content-type", "application/json")
        .body(Body::from(r#"{"login": "member@example.com", "password": "wrong horse battery"}"#))
        .unwrap();
    router.handle(request).await.status()
}

// A refusal past a rate limit: 429, Retry-After, the message above the
// form, and no cookie.
fn assert_limited(reply: &Reply) {
    assert_eq!(reply.status, StatusCode::TOO_MANY_REQUESTS, "{}", reply.body);
    assert!(reply.retry_after.as_deref().is_some_and(|seconds| seconds.parse::<u64>().is_ok_and(|seconds| seconds > 0)), "{:?}", reply.retry_after);
    assert!(reply.body.contains(r#"<p class="form-error">Too many attempts: try again in "#), "{}", reply.body);
    assert!(reply.cookies.is_empty(), "{:?}", reply.cookies);
}

fn encode(value: &str) -> String {
    value
        .bytes()
        .map(|byte| if byte.is_ascii_alphanumeric() || b"-._~".contains(&byte) { char::from(byte).to_string() } else { format!("%{byte:02X}") })
        .collect()
}

async fn sign_in(router: &Router, login: &str, password: &str) -> String {
    let reply = send(router, "POST", "/console/sign-in", None, &[("login", login), ("password", password), ("next", "/console")]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    reply.session()
}

// The nav: its links and buttons, between its tags.
fn nav(body: &str) -> &str {
    let start = body.find(r#"<nav class="ss-nav""#).expect("the nav");
    let end = start + body[start..].find("</nav>").unwrap();
    &body[start..end]
}

// The href of the link whose text is label.
fn href_of<'a>(body: &'a str, label: &str) -> &'a str {
    let end = body.find(&format!("\">{label}</a>")).unwrap_or_else(|| panic!("a link to {label} in {body}"));
    let start = body[..end].rfind("href=\"").unwrap() + "href=\"".len();
    &body[start..end]
}

#[tokio::test]
async fn a_caller_signs_in_back_to_next_and_out() {
    let router = app().await;

    // A page that needs a caller links to the generated sign-in page,
    // which comes back to it.
    let reply = send(&router, "GET", "/console/notes", None, &[]).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED);
    assert!(reply.body.contains(r#"href="/console/sign-in?next=%2Fconsole%2Fnotes""#), "{}", reply.body);

    // The signed-out index links to signing in once, besides the nav's.
    let index = send(&router, "GET", "/console", None, &[]).await;
    let main = &index.body[index.body.find("</nav>").expect("the nav")..];
    assert_eq!(main.matches(">Sign in</a>").count(), 1, "{}", index.body);
    assert!(main.contains(r#"class="ss-sign-in""#), "{}", index.body);
    assert!(nav(&index.body).contains(">Sign in</a>"), "{}", index.body);
    let reply = send(&router, "GET", "/console/sign-in?next=%2Fconsole%2Fnotes", None, &[]).await;
    assert_eq!(reply.status, StatusCode::OK);
    assert!(reply.body.contains(r#"<input type="hidden" name="next" value="/console/notes">"#), "{}", reply.body);
    assert!(reply.body.contains(r#"type="password""#), "{}", reply.body);
    assert!(nav(&reply.body).contains(r#"href="/console/sign-in""#), "{}", reply.body);
    assert!(!nav(&reply.body).contains("Account"), "{}", reply.body);

    // A wrong password is 401 with the runtime's message; the form keeps
    // the login and never the password.
    let wrong = [("login", "member@example.com"), ("password", "wrong horse battery"), ("next", "/console/notes")];
    let reply = send(&router, "POST", "/console/sign-in", None, &wrong).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    assert!(reply.body.contains("Invalid login or password"), "{}", reply.body);
    assert!(reply.body.contains(r#"value="member@example.com""#), "{}", reply.body);
    assert!(!reply.body.contains("wrong horse battery"), "{}", reply.body);
    assert!(reply.cookies.is_empty(), "{:?}", reply.cookies);
    let reply = send(&router, "POST", "/console/sign-in", None, &[("login", ""), ("password", "")]).await;
    assert_eq!(reply.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", reply.body);
    assert!(reply.body.contains("login is required"), "{}", reply.body);

    // The right one sets the session cookie and goes on to next.
    let right = [("login", "member@example.com"), ("password", PASSWORD), ("next", "/console/notes")];
    let reply = send(&router, "POST", "/console/sign-in", None, &right).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some("/console/notes"));
    let member = reply.session();
    let reply = send(&router, "GET", "/console/notes", Some(&member), &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("Shopping"), "{}", reply.body);
    let links = nav(&reply.body);
    assert!(links.contains(r#"href="/console/account""#), "{links}");
    assert!(links.contains(r#"<form method="post" action="/console/sign-out"><button type="submit">Sign out</button></form>"#), "{links}");

    // A next off this site goes to the index.
    for next in ["https://evil.example.com/", "//evil.example.com/", "/\\evil.example.com", "notes"] {
        let reply = send(&router, "POST", "/console/sign-in", None, &[("login", "member@example.com"), ("password", PASSWORD), ("next", next)]).await;
        assert_eq!(reply.status, StatusCode::SEE_OTHER, "{next}: {}", reply.body);
        assert_eq!(reply.location.as_deref(), Some("/console"), "{next}");
    }

    // Signing out ends the session and clears its cookie.
    let reply = send(&router, "POST", "/console/sign-out", Some(&member), &[]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some("/console"));
    assert!(reply.clears(), "{:?}", reply.cookies);
    let reply = send(&router, "GET", "/console/account", Some(&member), &[]).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    assert!(reply.body.contains(r#"href="/console/sign-in?next=%2Fconsole%2Faccount""#), "{}", reply.body);
}

// Sign-in meets the login route's rate limit, 10 a minute per client: the
// 11th is refused, its right password unchecked. Where the API serves the
// route, the page takes the route's limiter, so its JSON login and the
// page share one budget.
#[tokio::test]
async fn the_eleventh_sign_in_in_a_minute_is_refused() {
    let router = app().await;
    let wrong = [("login", "member@example.com"), ("password", "wrong horse battery")];
    let json = if API_LOGIN { 4 } else { 0 };
    for _ in 0..json {
        assert_eq!(json_login(&router).await, StatusCode::UNAUTHORIZED);
    }
    for _ in json..10 {
        let reply = send(&router, "POST", "/console/sign-in", None, &wrong).await;
        assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    }
    let right = [("login", "member@example.com"), ("password", PASSWORD), ("next", "/console/notes")];
    let reply = send(&router, "POST", "/console/sign-in", None, &right).await;
    assert_limited(&reply);
    assert!(reply.body.contains(r#"value="member@example.com""#) && !reply.body.contains(PASSWORD), "{}", reply.body);
    if API_LOGIN {
        assert_eq!(json_login(&router).await, StatusCode::TOO_MANY_REQUESTS);
    }
}

// Changing the password meets the changePassword route's rate limit, 10 a
// minute per client.
#[tokio::test]
async fn the_eleventh_password_change_in_a_minute_is_refused() {
    let router = app().await;
    let member = sign_in(&router, "member@example.com", PASSWORD).await;
    for _ in 0..10 {
        let reply = send(&router, "POST", "/console/account", Some(&member), &[("current", "not the password"), ("password", "a new password")]).await;
        assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    }
    let reply = send(&router, "POST", "/console/account", Some(&member), &[("current", PASSWORD), ("password", "a new password")]).await;
    assert_limited(&reply);
    assert!(reply.body.contains("<h1>Account</h1>"), "{}", reply.body);
    sign_in(&router, "member@example.com", PASSWORD).await;
}

// Signing up meets the register route's rate limit, 5 a minute per client.
#[tokio::test]
async fn the_sixth_sign_up_in_a_minute_is_refused() {
    if !SIGN_UP {
        return;
    }
    let router = app().await;
    for at in 0..5 {
        let login = format!("new{at}@example.com");
        let reply = send(&router, "POST", "/console/sign-up", None, &[("login", &login), ("password", PASSWORD)]).await;
        assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    }
    let reply = send(&router, "POST", "/console/sign-up", None, &[("login", "new5@example.com"), ("password", PASSWORD)]).await;
    assert_limited(&reply);
}

#[tokio::test]
async fn another_sites_sign_in_is_refused() {
    let router = app().await;
    let request = http::Request::builder()
        .method("POST")
        .uri("/console/sign-in")
        .header("host", "app.example.com")
        .header("content-type", "application/x-www-form-urlencoded")
        .header("origin", "https://evil.example.com")
        .header("sec-fetch-site", "cross-site")
        .body(Body::from(format!("login=member%40example.com&password={}", encode(PASSWORD))))
        .unwrap();
    let response = router.handle(request).await;
    assert_eq!(response.status(), StatusCode::FORBIDDEN);
    assert!(response.headers().get_all("set-cookie").iter().next().is_none());
}

#[tokio::test]
async fn changing_the_password_ends_the_other_sessions() {
    let router = app().await;
    let here = sign_in(&router, "member@example.com", PASSWORD).await;
    let there = sign_in(&router, "member@example.com", PASSWORD).await;

    let reply = send(&router, "GET", "/console/account", Some(&here), &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("member@example.com") && reply.body.contains("Member"), "{}", reply.body);
    assert!(reply.body.contains("Changing your password signs you out of every other session."), "{}", reply.body);
    assert!(reply.body.contains(r#"<span aria-current="page">Account</span>"#), "{}", reply.body);

    let reply = send(&router, "POST", "/console/account", Some(&here), &[("current", "not the password"), ("password", "a new password")]).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    assert!(reply.body.contains("Invalid login or password"), "{}", reply.body);
    assert!(!reply.body.contains("not the password") && !reply.body.contains("a new password"), "{}", reply.body);
    let reply = send(&router, "POST", "/console/account", Some(&here), &[("current", PASSWORD), ("password", "short")]).await;
    assert_eq!(reply.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", reply.body);
    assert!(reply.body.contains("length 5 below minimum 8"), "{}", reply.body);

    let reply = send(&router, "POST", "/console/account", Some(&here), &[("current", PASSWORD), ("password", "a new password")]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some("/console/account"));
    assert_eq!(send(&router, "GET", "/console/account", Some(&here), &[]).await.status, StatusCode::OK);
    let reply = send(&router, "GET", "/console/account", Some(&there), &[]).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    sign_in(&router, "member@example.com", "a new password").await;
}

#[tokio::test]
async fn sign_up_is_served_only_with_the_flag() {
    let router = app().await;
    let reply = send(&router, "GET", "/console/sign-up", None, &[]).await;
    let index = send(&router, "GET", "/console", None, &[]).await;
    if !SIGN_UP {
        assert_eq!(reply.status, StatusCode::NOT_FOUND, "{}", reply.body);
        assert!(!index.body.contains("sign-up"), "{}", index.body);
        return;
    }
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(nav(&index.body).contains(r#"href="/console/sign-up""#), "{}", index.body);

    let taken = [("login", "member@example.com"), ("name", "Again"), ("password", PASSWORD), ("next", "/console/account")];
    let reply = send(&router, "POST", "/console/sign-up", None, &taken).await;
    assert_eq!(reply.status, StatusCode::CONFLICT, "{}", reply.body);
    assert!(reply.body.contains("The login is taken"), "{}", reply.body);
    assert!(!reply.body.contains(PASSWORD), "{}", reply.body);

    let new = [("login", "new@example.com"), ("name", "Newcomer"), ("password", PASSWORD), ("next", "/console/account")];
    let reply = send(&router, "POST", "/console/sign-up", None, &new).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some("/console/account"));
    let reply = send(&router, "GET", "/console/account", Some(&reply.session()), &[]).await;
    assert!(reply.body.contains("Newcomer"), "{}", reply.body);
}

#[tokio::test]
async fn an_admin_administers_users_and_roles() {
    let router = app().await;
    let admin = sign_in(&router, "admin@example.com", PASSWORD).await;
    let admin = Some(admin.as_str());

    let index = send(&router, "GET", "/console", admin, &[]).await;
    for link in [r#"href="/console/users""#, r#"href="/console/roles""#, r#"href="/console/account""#] {
        assert!(nav(&index.body).contains(link), "{link} in {}", index.body);
        assert!(index.body.contains(&format!("<li><a {link}>")), "{link} in the index: {}", index.body);
    }

    // A user added, then found in the list.
    let reply = send(&router, "GET", "/console/users", admin, &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("member@example.com") && reply.body.contains(r#"href="/console/users/new""#), "{}", reply.body);
    assert_eq!(send(&router, "GET", "/console/users/new", admin, &[]).await.status, StatusCode::OK);
    let taken = [("login", "member@example.com"), ("name", "Again"), ("password", PASSWORD)];
    let reply = send(&router, "POST", "/console/users/new", admin, &taken).await;
    assert_eq!(reply.status, StatusCode::CONFLICT, "{}", reply.body);
    assert!(reply.body.contains("The login is taken") && !reply.body.contains(PASSWORD), "{}", reply.body);
    let reply = send(&router, "POST", "/console/users/new", admin, &[("login", "editor@example.com"), ("name", "Edith"), ("password", PASSWORD)]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    let user = reply.location.clone().unwrap();
    assert!(user.starts_with("/console/users/"), "{user}");
    let reply = send(&router, "GET", &user, admin, &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("<h1>Edith</h1>") && reply.body.contains("Active"), "{}", reply.body);
    assert!(reply.body.contains(r#"<a href="/console/users">Users</a>"#) && reply.body.contains(r#"<span aria-current="page">Edith</span>"#), "{}", reply.body);

    // A role added: a malformed permission (422) and one the admin does
    // not hold (403) are refused at the permissions.
    let reply = send(&router, "POST", "/console/roles/new", admin, &[("name", "editor"), ("permissions", "notes.read\nnot a permission")]).await;
    assert_eq!(reply.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", reply.body);
    assert!(reply.body.contains("not a permission</p>") && reply.body.contains("notes.read\nnot a permission</textarea>"), "{}", reply.body);
    let reply = send(&router, "POST", "/console/roles/new", admin, &[("name", "editor"), ("permissions", "orders.read")]).await;
    assert_eq!(reply.status, StatusCode::FORBIDDEN, "{}", reply.body);
    assert!(reply.body.contains("No one grants a permission they do not hold: orders.read"), "{}", reply.body);
    let reply = send(&router, "POST", "/console/roles/new", admin, &[("name", ""), ("permissions", "")]).await;
    assert_eq!(reply.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", reply.body);
    assert!(reply.body.contains("name is required"), "{}", reply.body);
    let reply = send(&router, "POST", "/console/roles/new", admin, &[("name", "editor"), ("permissions", "notes.read\n notes.write \n")]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some("/console/roles"));
    let reply = send(&router, "GET", "/console/roles", admin, &[]).await;
    assert!(reply.body.contains("notes.read, notes.write"), "{}", reply.body);
    let role = href_of(&reply.body, "editor").strip_prefix("/console/roles/").unwrap().to_string();

    // Granted, then revoked.
    let reply = send(&router, "POST", &format!("{user}/grant"), admin, &[("role", &role)]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(reply.location.as_deref(), Some(user.as_str()));
    let reply = send(&router, "GET", &user, admin, &[]).await;
    assert!(reply.body.contains(r#"aria-label="Revoke editor""#), "{}", reply.body);
    let reply = send(&router, "POST", &format!("{user}/revoke"), admin, &[("role", &role)]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    let reply = send(&router, "GET", &user, admin, &[]).await;
    assert!(reply.body.contains("The user has no roles."), "{}", reply.body);

    // Disabled: the user cannot sign in; enabled again, they can.
    assert_eq!(send(&router, "POST", &format!("{user}/disable"), admin, &[]).await.status, StatusCode::SEE_OTHER);
    let reply = send(&router, "GET", &user, admin, &[]).await;
    assert!(reply.body.contains("Disabled") && reply.body.contains("Enable the user"), "{}", reply.body);
    let reply = send(&router, "POST", "/console/sign-in", None, &[("login", "editor@example.com"), ("password", PASSWORD)]).await;
    assert_eq!(reply.status, StatusCode::UNAUTHORIZED, "{}", reply.body);
    assert_eq!(send(&router, "POST", &format!("{user}/enable"), admin, &[]).await.status, StatusCode::SEE_OTHER);
    sign_in(&router, "editor@example.com", PASSWORD).await;

    // A password set ends the user's sessions; a short one is refused at
    // its control.
    let edith = sign_in(&router, "editor@example.com", PASSWORD).await;
    let reply = send(&router, "POST", &format!("{user}/password"), admin, &[("password", "short")]).await;
    assert_eq!(reply.status, StatusCode::UNPROCESSABLE_ENTITY, "{}", reply.body);
    assert!(reply.body.contains("length 5 below minimum 8") && reply.body.contains("<h1>Edith</h1>"), "{}", reply.body);
    let reply = send(&router, "POST", &format!("{user}/password"), admin, &[("password", "another password")]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    assert_eq!(send(&router, "GET", "/console/account", Some(&edith), &[]).await.status, StatusCode::UNAUTHORIZED);
    sign_in(&router, "editor@example.com", "another password").await;

    // The role edited, then deleted.
    let page = format!("/console/roles/{role}");
    let reply = send(&router, "GET", &page, admin, &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains(r#"value="editor""#) && reply.body.contains("notes.read\nnotes.write</textarea>"), "{}", reply.body);
    let reply = send(&router, "POST", &page, admin, &[("name", "admin"), ("permissions", "notes.read")]).await;
    assert_eq!(reply.status, StatusCode::CONFLICT, "{}", reply.body);
    assert!(reply.body.contains("The role name is taken"), "{}", reply.body);
    let reply = send(&router, "POST", &page, admin, &[("name", "editors"), ("permissions", "notes.read")]).await;
    assert_eq!(reply.status, StatusCode::SEE_OTHER, "{}", reply.body);
    let reply = send(&router, "GET", &format!("{page}/delete"), admin, &[]).await;
    assert_eq!(reply.status, StatusCode::OK, "{}", reply.body);
    assert!(reply.body.contains("Delete the role editors?") && reply.body.contains(r#"<span aria-current="page">Delete</span>"#), "{}", reply.body);
    assert_eq!(send(&router, "POST", &format!("{page}/delete"), admin, &[]).await.status, StatusCode::SEE_OTHER);
    let reply = send(&router, "GET", "/console/roles", admin, &[]).await;
    assert!(!reply.body.contains("editors"), "{}", reply.body);
    assert_eq!(send(&router, "GET", &page, admin, &[]).await.status, StatusCode::NOT_FOUND);
}

#[tokio::test]
async fn a_member_is_refused_administration() {
    let router = app().await;
    let member = sign_in(&router, "member@example.com", PASSWORD).await;
    let member = Some(member.as_str());

    let index = send(&router, "GET", "/console", member, &[]).await;
    let links = nav(&index.body);
    assert!(!links.contains("/console/users") && !links.contains("/console/roles"), "{links}");
    assert!(links.contains(r#"href="/console/account""#), "{links}");

    for page in ["/console/users", "/console/users/new", "/console/roles", "/console/roles/new"] {
        let reply = send(&router, "GET", page, member, &[]).await;
        assert_eq!(reply.status, StatusCode::FORBIDDEN, "{page}: {}", reply.body);
        assert!(reply.body.contains("Insufficient permissions"), "{page}: {}", reply.body);
    }
    let reply = send(&router, "POST", "/console/users/new", member, &[("login", "x@example.com"), ("password", PASSWORD)]).await;
    assert_eq!(reply.status, StatusCode::FORBIDDEN, "{}", reply.body);
    assert!(!reply.body.contains(PASSWORD), "{}", reply.body);
    let reply = send(&router, "POST", "/console/roles/new", member, &[("name", "mine"), ("permissions", "")]).await;
    assert_eq!(reply.status, StatusCode::FORBIDDEN, "{}", reply.body);
}
`

// TestTheEngineAndIdentityPagesBuildTogether builds copies of
// fixture-engine-api and fixture-engine-db in which the database holds the
// core user model's tables too (fixture-user-model-db's) and is the API's
// authDb, and the API has an @userSessions set, as acme's shop-db and
// shop-orders are, so the crate has both the engine pages and the
// identity pages. It runs cargo clippy with warnings denied, then cargo
// test: the nav and the trail that join them compile.
func TestTheEngineAndIdentityPagesBuildTogether(t *testing.T) {
	parent := testpaths.TempDir(t)
	copyService := func(from, name string, edit func(file, text string) string) {
		t.Helper()
		err := filepath.WalkDir(from, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(from, path)
			if err != nil {
				return err
			}
			writeFile(t, filepath.Join(parent, name, rel), edit(filepath.ToSlash(rel), string(data)))
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	copyService(serviceDir(engineDatabase), engineDatabase, func(_, text string) string { return text })
	users, err := os.ReadFile(filepath.Join(serviceDir("fixture-user-model-db"), "src", "users.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	// The user model's tables, in the database's name, without the scalar
	// its own schema declares.
	var model map[string]any
	if err := json.Unmarshal(users, &model); err != nil {
		t.Fatal(err)
	}
	model["name"] = engineDatabase
	delete(model["scalars"].(map[string]any), "Identity.UUID")
	users, err = json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(parent, engineDatabase, "src", "users.schema.json"), string(users))
	copyService(serviceDir(engineService), engineService, func(file, text string) string {
		edited := text
		switch file {
		case "schema.config.yaml":
			edited = strings.Replace(text, "kind: API\n", "kind: API\nauthDb: "+engineDatabase+"\n", 1)
		case "src/notes.schema.yaml":
			edited = strings.Replace(text, "operationSets:\n", "operationSets:\n  - name: Account\n    userSessions: {}\n    operations: []\n", 1)
		}
		if edited == text {
			t.Fatalf("the edit of %s changed nothing", file)
		}
		return edited
	})
	dir := filepath.Join(parent, engineService)
	root := testpaths.TempDir(t)
	if _, err := buildIn(t, buildSpec{names: registry.DefaultNaming(), dir: filepath.Join(parent, engineDatabase), root: root, outputs: engineDatabaseOutputs()}); err != nil {
		t.Fatalf("build %s: %v", engineDatabase, err)
	}
	var log strings.Builder
	if _, err := buildDir(t, registry.DefaultNaming(), dir, root, pagesOutputs(), &log); err != nil {
		t.Fatalf("build %s: %v", dir, err)
	}
	for _, file := range []string{"engine_pages.rs", "identity_pages.rs"} {
		if _, err := os.Stat(filepath.Join(topcoat.Dir(root, engineService), "src", file)); err != nil {
			t.Fatalf("the crate has no %s: %v\n%s", file, err, log.String())
		}
	}
	cargoTestCrateWith(t, engineService, "#[test]\nfn builds() {}\n", crateOptions{
		dir:          dir,
		dependencies: []string{engineDatabase},
		depOutputs:   map[string]map[string]any{engineDatabase: engineDatabaseOutputs()},
		features:     []string{"identity-sqlite"},
	})
}
