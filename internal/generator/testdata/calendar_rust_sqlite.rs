//! fixture-access-db's calendar in the Rust runtime on a SQLite file
//! (D152, D156, D186, #615), run by TestRustCalendarOnSQLite: the build's
//! SQLite DDL applied to a file of the test's own, the documents' calendar
//! source read through orm::calendar::sqlite as each reader, and its feed
//! served by orm::calendar::sqlite::calendar_routes over the file, as
//! TestRustAccessEndToEnd's feed case serves it on Postgres. A document is
//! on the calendar on the day it was made, as its reader sees it: its
//! audience, a JSON array the file's triggers keep, and a party's grant
//! hold on the calendar as on a list read.

use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use axum::body::Body;
use http::{Method, Request};
use http_body_util::BodyExt;
use runtime::access::sqlite::{authenticate_grant, issue_grant, GrantAuthenticator};
use runtime::access::IssueGrant;
use runtime::mcp::{CallOutcome, EndpointTool};
use runtime::orm::calendar::sqlite::{calendar_routes, read_calendar, CalendarRoutes, CalendarTool};
use runtime::orm::calendar::{iso, CalendarOptions, CalendarRead};
use runtime::orm::sqlite::{rusqlite, Database, Repositories};
use runtime::orm::{AccessCaller, Error, FieldKeyring, FindOptions, Values};
use runtime::{ActingFor, ApiError, Authenticator, Principal, PrincipalKind};
use serde_json::{json, Value};
use tower::ServiceExt;

/// The parity vectors' key (runtime/schema/testdata/fieldcrypt_parity.json).
const KEYS: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=";

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// The UUID 00000000-0000-4000-8000-<n> in its wire form, base62.
fn uuid(n: u128) -> String {
    let mut number = (0x4000u128 << 64) | (0x8000u128 << 48) | n;
    let mut digits = Vec::new();
    while number > 0 {
        digits.push(BASE62[(number % 62) as usize]);
        number /= 62;
    }
    digits.reverse();
    String::from_utf8(digits).unwrap()
}

fn values(value: Value) -> Values {
    value.as_object().unwrap().clone()
}

fn case(name: &str) {
    println!("case: {name}");
}

/// Whom the repositories read and write as.
#[derive(Clone)]
enum Who {
    System,
    As(Box<Principal>),
}

fn user(id: &str) -> Principal {
    Principal::new(id, Vec::<String>::new())
}

/// A directory of the test's own, removed when it ends.
struct TempDir(std::path::PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

struct File {
    database: Arc<Database>,
    keys: Arc<FieldKeyring>,
    _dir: TempDir,
}

impl File {
    fn new() -> Self {
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let dir = std::env::temp_dir().join(format!("calendar-rust-sqlite-{nanos}"));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("access.db");
        let connection = rusqlite::Connection::open(&path).unwrap();
        connection.execute_batch(include_str!("../create.sql")).unwrap();
        drop(connection);
        Self {
            database: Arc::new(Database::open(&path).unwrap()),
            keys: Arc::new(FieldKeyring::parse(KEYS).expect("the parity key")),
            _dir: TempDir(dir),
        }
    }

    fn tx<T>(&self, who: &Who, work: impl FnOnce(&Repositories<'_>) -> Result<T, Error>) -> Result<T, Error> {
        let definition = types::orm::sqlite_definition()?;
        self.database.write(|connection| {
            let repositories = Repositories::new(connection, definition)?.with_keys(self.keys.as_ref());
            let repositories = match who {
                Who::System => repositories.as_system(),
                Who::As(principal) => repositories
                    .acting(runtime::orm::routes::acting(Some(principal.as_ref())))
                    .with_access(AccessCaller::of(Some(principal.as_ref()))),
            };
            work(&repositories)
        })
    }

    fn create(&self, who: &Who, table: &str, row: Value) -> Values {
        self.tx(who, |r| r.table(table)?.create_one(&values(row))).unwrap()
    }

    /// The titles of the documents `principal`'s list read shows, sorted.
    fn listed(&self, principal: &Principal) -> Vec<String> {
        let mut titles: Vec<String> = self
            .tx(&Who::As(Box::new(principal.clone())), |r| r.table("Document")?.find_many(None, &FindOptions::default()))
            .unwrap()
            .rows
            .iter()
            .map(|row| row["title"].as_str().unwrap().to_owned())
            .collect();
        titles.sort();
        titles
    }

    /// The titles of the calendar's entries `principal` reads today,
    /// sorted.
    fn calendar(&self, principal: &Principal) -> Vec<String> {
        let definition = types::orm::sqlite_definition().unwrap();
        let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64;
        let read = CalendarRead { from: iso(now - 86_400_000), to: iso(now + 86_400_000), ..CalendarRead::default() };
        let calendar = self
            .database
            .read(|connection| read_calendar(&Repositories::new(connection, definition)?, Some(principal), &read, &CalendarOptions::default()))
            .unwrap();
        for entry in &calendar.entries {
            assert_eq!((entry.source.as_str(), entry.kind.as_str(), entry.table.as_str(), entry.all_day), ("document", "document", "Document", false), "{entry:?}");
        }
        let mut titles: Vec<String> = calendar.entries.iter().map(|e| e.title.clone()).collect();
        titles.sort();
        titles
    }
}

/// The caller a request's bearer names, a user.
struct Callers;

#[async_trait]
impl Authenticator for Callers {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        Ok(runtime::bearer_token(&request.headers).map(user))
    }
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_calendar_on_sqlite() {
    let file = File::new();
    let definition = types::orm::sqlite_definition().unwrap();
    let (owner_id, other_id, named_id) = (uuid(0xa1), uuid(0xa2), uuid(0xa3));
    let (owner, other, named) = (user(&owner_id), user(&other_id), user(&named_id));
    let as_owner = Who::As(Box::new(owner.clone()));
    let system = Who::System;

    // A private folder of the owner's with a secret plan, a public plan, a
    // document named to one other user, and a shared folder whose
    // documents a party's grant reaches.
    let mine = file.create(&as_owner, "Folder", json!({"name": "Mine", "owner": owner_id, "audience": "private"}));
    file.create(&as_owner, "Document", json!({"folderId": mine["id"], "title": "Secret plan"}));
    file.create(&as_owner, "Document", json!({"title": "Public plan", "audience": "everyone"}));
    file.create(&as_owner, "Document", json!({"title": "Shared notes", "audience": "named", "audienceNamed": [named_id]}));
    let shared = file.create(&as_owner, "Folder", json!({"name": "Shared", "owner": owner_id, "audience": "everyone"}));
    file.create(&as_owner, "Document", json!({"folderId": shared["id"], "title": "Shared folder plan"}));
    file.create(&as_owner, "Document", json!({"title": "No agents", "audience": "everyone", "agentAccess": "none"}));

    case("each reader's calendar holds exactly the rows its list reads show");
    let agent = Principal::new(uuid(0xb1), Vec::<String>::new()).of_kind(PrincipalKind::Agent).acting_for(ActingFor::user(owner_id.clone()));
    for (reader, principal) in [("owner", &owner), ("other", &other), ("named", &named), ("an agent run", &agent)] {
        let listed = file.listed(principal);
        assert!(!listed.is_empty(), "{reader} lists nothing");
        assert_eq!(file.calendar(principal), listed, "{reader}'s calendar");
    }
    assert!(file.calendar(&owner).contains(&"Secret plan".to_owned()));
    assert!(!file.calendar(&other).contains(&"Secret plan".to_owned()));
    assert!(file.calendar(&named).contains(&"Shared notes".to_owned()));
    assert!(!file.calendar(&other).contains(&"Shared notes".to_owned()));
    assert!(!file.calendar(&agent).contains(&"No agents".to_owned()));
    // A source the caller lacks the permission of is left out.
    let options = CalendarOptions { permissions: [("document".to_owned(), "documents.calendar".to_owned())].into() };
    let read = CalendarRead { from: iso(0), to: iso(4_102_444_800_000), ..CalendarRead::default() };
    let held = file.database.read(|connection| read_calendar(&Repositories::new(connection, definition)?, Some(&owner), &read, &options)).unwrap();
    assert!(held.entries.is_empty(), "{:?}", held.entries);

    case("a party's grant reads the calendar through its role's paths alone");
    file.create(&system, "Role", json!({"name": "folder-reader", "permissions": ["documents.read"], "reaches": ["Document:folder"]}));
    let party = uuid(0xc1);
    let issued = file
        .tx(&system, |r| {
            issue_grant(
                r,
                &IssueGrant {
                    holder: party.clone(),
                    holder_kind: PrincipalKind::Party,
                    role: "folder-reader".to_owned(),
                    target_type: "Folder".to_owned(),
                    target_id: shared["id"].as_str().unwrap().to_owned(),
                    starts_at: None,
                    ends_at: None,
                    ends_when: None,
                },
            )
        })
        .unwrap();
    let holder = file.database.read(|connection| authenticate_grant(connection, definition, &issued.token, SystemTime::now())).unwrap();
    assert_eq!(holder.kind, PrincipalKind::Party);
    assert_eq!(file.listed(&holder), ["Shared folder plan"]);
    assert_eq!(file.calendar(&holder), ["Shared folder plan"]);

    // The feed's routes over the file.
    let tables = types::orm::sqlite::tables(Arc::clone(&file.database)).with_keys(file.keys.clone());
    let calendar = calendar_routes(CalendarRoutes::new(tables.clone(), Arc::new(Callers)));
    let send = async |method: Method, path: &str, as_: Option<&str>, body: Option<Value>| -> (u16, String, String) {
        let mut request = Request::builder().method(method).uri(path);
        if let Some(as_) = as_ {
            request = request.header(http::header::AUTHORIZATION, format!("Bearer {as_}"));
        }
        let body = match body {
            Some(body) => {
                request = request.header(http::header::CONTENT_TYPE, "application/json");
                Body::from(body.to_string())
            }
            None => Body::empty(),
        };
        let response = calendar.clone().oneshot(request.body(body).unwrap()).await.unwrap();
        let status = response.status().as_u16();
        let kind = response.headers().get(http::header::CONTENT_TYPE).and_then(|v| v.to_str().ok()).unwrap_or_default().to_owned();
        let bytes = response.into_body().collect().await.unwrap().to_bytes();
        (status, kind, String::from_utf8(bytes.to_vec()).unwrap())
    };
    file.create(&system, "Role", json!({"name": "calendarFeed", "permissions": ["documents.calendar"]}));
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_millis() as i64;
    let window = format!("from={}&to={}", iso(now - 86_400_000), iso(now + 86_400_000));
    let read = async |as_: &str| -> Vec<String> {
        let (status, _, text) = send(Method::GET, &format!("/api/calendar?{window}"), Some(as_), None).await;
        assert_eq!(status, 200, "{text}");
        let body: Value = serde_json::from_str(&text).unwrap();
        let mut titles: Vec<String> = body["data"]["entries"].as_array().unwrap().iter().map(|e| e["title"].as_str().unwrap().to_owned()).collect();
        titles.sort();
        titles
    };
    assert_eq!(read(&owner_id).await, file.listed(&owner));
    assert_eq!(read(&other_id).await, file.listed(&other));
    // The calendar as the MCP endpoint's tool over the file (#725), run as
    // the endpoint's caller: the same entries as the route's.
    let tool = CalendarTool { tables: tables.clone(), options: CalendarOptions::default() };
    let arguments = json!({"from": iso(now - 86_400_000), "to": iso(now + 86_400_000)}).as_object().unwrap().clone();
    for principal in [&owner, &other] {
        let CallOutcome::Result(called) = tool.call(principal, &arguments).await.unwrap() else {
            panic!("the calendar tool answered a problem");
        };
        let mut titles: Vec<String> = called["entries"].as_array().unwrap().iter().map(|e| e["title"].as_str().unwrap().to_owned()).collect();
        titles.sort();
        assert_eq!(titles, file.listed(principal));
    }
    let refused = tool.call(&owner, &serde_json::Map::new()).await.unwrap();
    assert!(matches!(refused, CallOutcome::Problem(_)), "a call with no window is a problem");
    assert_eq!(send(Method::GET, &format!("/api/calendar?{window}"), None, None).await.0, 401);
    assert_eq!(send(Method::GET, "/api/calendar", Some(&owner_id), None).await.0, 400);
    let issue = async |as_: &str, body: Value| -> Value {
        let (status, _, text) = send(Method::POST, "/api/calendar/feeds", Some(as_), Some(body)).await;
        assert_eq!(status, 201, "{text}");
        serde_json::from_str::<Value>(&text).unwrap()["data"].clone()
    };

    case("a feed shows only the rows its token's principal reads, busy or in full, and its token is no credential elsewhere");
    let other_feed = issue(&other_id, json!({"role": "calendarFeed", "detail": "full"})).await;
    assert!(other_feed["token"].as_str().unwrap().starts_with("grant_"));
    let other_path = other_feed["path"].as_str().unwrap().to_owned();
    let (status, kind, ics) = send(Method::GET, &other_path, None, None).await;
    assert_eq!(status, 200, "{ics}");
    assert!(kind.contains("text/calendar"), "{kind}");
    assert!(ics.contains("SUMMARY:Public plan"), "{ics}");
    assert!(!ics.contains("Secret plan") && !ics.contains("Shared notes"), "{ics}");
    assert_eq!(ics.matches("BEGIN:VEVENT").count(), file.listed(&other).len());
    // The owner's busy feed holds the owner's private document, as Busy
    // and a reference, and no title.
    let owner_feed = issue(&owner_id, json!({"role": "calendarFeed", "detail": "busy"})).await;
    let (_, _, busy) = send(Method::GET, owner_feed["path"].as_str().unwrap(), None, None).await;
    assert!(!busy.contains("plan"), "{busy}");
    assert_eq!(busy.matches("BEGIN:VEVENT").count(), file.listed(&owner).len());
    let reference = busy.split("SUMMARY:Busy (").nth(1).and_then(|rest| rest.split(')').next()).unwrap();
    assert!(reference.len() == 8 && reference.chars().all(|c| "ABCDEFGHJKLMNPQRSTUVWXYZ23456789".contains(c)), "{reference}");
    // A feed's token is no credential of any other route: the grants'
    // authenticator refuses it.
    let grants = GrantAuthenticator::new(tables.clone()).unwrap();
    let parts = http::Request::builder()
        .uri("/api/documents")
        .header(http::header::AUTHORIZATION, format!("Bearer {}", other_feed["token"].as_str().unwrap()))
        .body(())
        .unwrap()
        .into_parts()
        .0;
    let refused = grants.authenticate(&parts).await;
    assert_eq!(refused.err().map(|err| err.status.as_u16()), Some(401));

    case("a rotated, revoked, expired or unknown feed is refused at the next fetch");
    // Rotated: the old URL is refused, the new one reads.
    let (status, _, text) = send(Method::POST, &format!("/api/calendar/feeds/{}/rotate", other_feed["id"].as_str().unwrap()), Some(&other_id), None).await;
    assert_eq!(status, 200, "{text}");
    let rotated = serde_json::from_str::<Value>(&text).unwrap()["data"].clone();
    assert_eq!(rotated["endsAt"], other_feed["endsAt"]);
    assert_eq!(send(Method::GET, &other_path, None, None).await.0, 401);
    let rotated_path = rotated["path"].as_str().unwrap().to_owned();
    assert_eq!(send(Method::GET, &rotated_path, None, None).await.0, 200);
    // Another user cannot rotate or revoke it; its holder can, and it is
    // refused.
    let rotated_id = format!("/api/calendar/feeds/{}", rotated["id"].as_str().unwrap());
    assert_eq!(send(Method::POST, &format!("{rotated_id}/rotate"), Some(&owner_id), None).await.0, 404);
    assert_eq!(send(Method::DELETE, &rotated_id, Some(&owner_id), None).await.0, 404);
    assert_eq!(send(Method::DELETE, &rotated_id, Some(&other_id), None).await.0, 204);
    assert_eq!(send(Method::GET, &rotated_path, None, None).await.0, 401);
    assert_eq!(send(Method::DELETE, &rotated_id, Some(&other_id), None).await.0, 404);
    // Expired: a feed of half a second.
    let brief = issue(&named_id, json!({"role": "calendarFeed", "detail": "full", "days": 0.5 / 86_400.0})).await;
    let brief_path = brief["path"].as_str().unwrap().to_owned();
    assert_eq!(send(Method::GET, &brief_path, None, None).await.0, 200);
    tokio::time::sleep(Duration::from_millis(700)).await;
    let (status, _, text) = send(Method::GET, &brief_path, None, None).await;
    assert_eq!(status, 401, "{text}");
    assert!(text.contains("ended"), "{text}");
    assert_eq!(send(Method::GET, "/api/calendar/feed/grant_unknown.ics", None, None).await.0, 401);
    assert_eq!(send(Method::GET, "/api/calendar/feed/unknown.ics", None, None).await.0, 401);
    assert_eq!(send(Method::GET, "/api/calendar/nowhere", Some(&owner_id), None).await.0, 404);
}
