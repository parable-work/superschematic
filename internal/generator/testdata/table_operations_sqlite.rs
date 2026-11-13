//! The generated Rust server of fixture-tracker-api over a SQLite file
//! (D116): its router, with no socket under it, serving the table
//! operations from the Rust repositories of fixture-tracker-db on SQLite.

use std::sync::Arc;

use api::database_types::orm::sqlite::Repositories;
use api::database_types::orm::{Equals, FindOptions, IssueFilter, Text};
use api::runtime::orm::sqlite::{rusqlite, Database};
use api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Api, Handlers, Implementations, TeamsImplementation, TeamsSummarizeTeamArgs};
use async_trait::async_trait;
use axum::body::Body;
use axum::Router;
use http::{Method, Request, StatusCode};
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

const ADA: &str = "11111111-1111-4111-8111-111111111111";
const BEN: &str = "22222222-2222-4222-8222-222222222222";
const EVERY: &str = ":teams.read,teams.write,issues.read,issues.write,members.read,members.write,notes.read,notes.write";

/// The scenario's authenticator: Authorization: Bearer
/// <subject>[:<permission>[,...]] is that subject, holding those
/// permissions; no header, no caller.
struct Bearer;

#[async_trait]
impl Authenticator for Bearer {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let token = request
            .headers
            .get(http::header::AUTHORIZATION)
            .and_then(|value| value.to_str().ok())
            .and_then(|value| value.strip_prefix("Bearer "))
            .filter(|token| !token.is_empty());
        Ok(token.map(|token| {
            let (subject, held) = token.split_once(':').unwrap_or((token, ""));
            let permissions: Vec<&str> = held.split(',').filter(|p| !p.is_empty()).collect();
            Principal::new(subject, permissions)
        }))
    }
}

/// The project's implementation of summarizeTeam, the one operation no
/// generated handler serves: the team's open issues, counted through the
/// repositories on a reading connection of the file.
struct Teams {
    db: Arc<Database>,
}

#[async_trait]
impl TeamsImplementation for Teams {
    async fn summarize_team(
        &self,
        _ctx: RequestContext,
        args: TeamsSummarizeTeamArgs,
    ) -> Result<api::types::TeamSummary, ApiError> {
        let team = args.id.clone();
        let open = self
            .db
            .read_async(move |connection| {
                let found = Repositories::new(connection)?.issue()?.find_many(
                    Some(&IssueFilter {
                        team_id: Some(Equals::eq(team)),
                        status: Some(Text::eq(api::database_types::IssueStatus::Open)),
                        ..Default::default()
                    }),
                    &FindOptions::default(),
                )?;
                Ok::<_, ApiError>(found.total.unwrap_or(0))
            })
            .await?;
        Ok(api::types::TeamSummary {
            team_id: args.id,
            open_issues: open as f64,
        })
    }
}

/// The router over a file of the test's own, holding the build's SQLite
/// DDL, and the directory that holds the file.
fn server() -> (Router, std::path::PathBuf) {
    let (implementations, dir) = implementations("router");
    (build_router(implementations), dir)
}

/// The implementations over a file of the test's own, holding the build's
/// SQLite DDL, and the directory that holds the file.
fn implementations(name: &str) -> (Implementations, std::path::PathBuf) {
    let dir = std::env::temp_dir().join(format!(
        "tracker-sqlite-{name}-{}-{:?}",
        std::process::id(),
        std::thread::current().id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create the directory");
    let path = dir.join("tracker.sqlite");
    rusqlite::Connection::open(&path)
        .and_then(|connection| connection.execute_batch(include_str!("../create.sql")))
        .expect("create the tables");
    let db = Arc::new(Database::open(&path).expect("open the file"));
    let handlers = Handlers {
        teams: Arc::new(Teams { db: Arc::clone(&db) }),
        authenticator: Arc::new(Bearer),
    };
    (Implementations::new(handlers, db), dir)
}

struct Reply {
    status: StatusCode,
    body: Value,
}

impl Reply {
    fn data(&self) -> &Value {
        assert_eq!(self.status, StatusCode::OK, "{}", self.body);
        &self.body["data"]
    }

    fn refused(&self, status: u16, detail: &str) {
        assert_eq!(self.status.as_u16(), status, "{}", self.body);
        assert_eq!(self.body["detail"], detail, "{}", self.body);
    }
}

async fn call(router: &Router, token: &str, method: Method, path: &str, body: Option<Value>) -> Reply {
    let mut request = Request::builder().method(method).uri(format!("/api{path}"));
    if !token.is_empty() {
        request = request.header(http::header::AUTHORIZATION, format!("Bearer {token}"));
    }
    let body = match body {
        Some(body) => {
            request = request.header(http::header::CONTENT_TYPE, "application/json");
            Body::from(body.to_string())
        }
        None => Body::empty(),
    };
    let response = router
        .clone()
        .oneshot(request.body(body).expect("a request"))
        .await
        .expect("the router answers");
    let status = response.status();
    let bytes = response.into_body().collect().await.expect("a body").to_bytes();
    let body = if bytes.is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(&bytes).expect("a JSON body")
    };
    Reply { status, body }
}

fn titles(page: &Value) -> Vec<String> {
    page["items"]
        .as_array()
        .expect("a page's items")
        .iter()
        .map(|item| item["title"].as_str().expect("a title").to_owned())
        .collect()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn the_router_serves_its_tables_from_a_sqlite_file() {
    let (router, dir) = server();
    let ada = format!("{ADA}{EVERY}");
    let ben = format!("{BEN}{EVERY}");
    let get = |path: String| call(&router, &ada, Method::GET, Box::leak(path.into_boxed_str()), None);
    let post = |path: String, body: Value| call(&router, &ada, Method::POST, Box::leak(path.into_boxed_str()), Some(body));
    let patch = |path: String, body: Value| call(&router, &ada, Method::PATCH, Box::leak(path.into_boxed_str()), Some(body));

    // A team is created, read and listed; a second of one key conflicts.
    let core = post("/teams".into(), json!({"key": "core", "name": "Core"})).await.data().clone();
    assert_eq!((core["key"].as_str(), core["name"].as_str()), (Some("core"), Some("Core")));
    let team = core["id"].as_str().expect("the team's id").to_owned();
    assert_eq!(get(format!("/teams/{team}")).await.data(), &core);
    post("/teams".into(), json!({"key": "core", "name": "Again"}))
        .await
        .refused(409, "Team with that key already exists");
    get("/teams/33333333-3333-4333-8333-333333333333".into())
        .await
        .refused(404, "Team not found");
    assert_eq!(get("/teams".into()).await.data()["items"].as_array().map(Vec::len), Some(1));

    // An issue is created under its team with the table's defaults and
    // the caller's stamp; a parent that does not exist is not found.
    let issues = format!("/teams/{team}/issues");
    let crash = post(issues.clone(), json!({"title": "Crash on save", "priority": 3, "dueOn": "2026-03-01"}))
        .await
        .data()
        .clone();
    assert_eq!(crash["status"], "open");
    assert_eq!(crash["teamId"], core["id"]);
    assert_eq!(crash["updatedAt"], Value::Null);
    post(issues.clone(), json!({"title": "Fix typo"})).await.data();
    post(issues.clone(), json!({"title": "Slow query", "dueOn": "2026-02-01"})).await.data();
    post("/teams/33333333-3333-4333-8333-333333333333/issues".into(), json!({"title": "Lost"}))
        .await
        .refused(404, "Team not found");

    // A list pages by cursor in its sort, a column's nulls last either
    // way, searches its search fields and negates a filter with a null
    // kept (D112).
    for (sort, want) in [
        ("dueOn", vec!["Slow query", "Crash on save", "Fix typo"]),
        ("-dueOn", vec!["Crash on save", "Slow query", "Fix typo"]),
        ("title", vec!["Crash on save", "Fix typo", "Slow query"]),
    ] {
        let mut seen = Vec::new();
        let mut cursor = String::new();
        loop {
            let page = get(format!("{issues}?sort={sort}&limit=2{cursor}")).await.data().clone();
            seen.extend(titles(&page));
            match page["next"].as_str() {
                Some(next) => cursor = format!("&cursor={next}"),
                None => break,
            }
        }
        assert_eq!(seen, want, "sort={sort}");
    }
    let searched = get(format!("{issues}?q=slow")).await.data().clone();
    assert_eq!(titles(&searched), vec!["Slow query"]);
    let negated = get(format!("{issues}?sort=title&filter[dueOn][ne]=2026-03-01")).await.data().clone();
    assert_eq!(titles(&negated), vec!["Fix typo", "Slow query"]);
    get(format!("{issues}?cursor=bm9wZQ")).await.refused(400, "cursor is not one this operation returned");

    // An update sets the members the body holds, tells a member left out
    // from a null, and refuses a null a column does not take.
    let issue = format!("/issues/{}", crash["id"].as_str().expect("the issue's id"));
    let updated = patch(issue.clone(), json!({"title": "Crash on every save", "dueOn": null})).await.data().clone();
    assert_eq!(updated["title"], "Crash on every save");
    assert_eq!(updated["dueOn"], Value::Null);
    assert_eq!(updated["priority"], 3);
    assert_ne!(updated["updatedAt"], Value::Null);
    let untouched = patch(issue.clone(), json!({})).await.data().clone();
    assert_eq!(untouched, updated);
    let refused = patch(issue.clone(), json!({"title": null})).await;
    assert_eq!(refused.status, StatusCode::BAD_REQUEST);
    assert_eq!(refused.body["errors"]["title"][0]["validator"], "required");

    // The operation the project writes reaches its implementation, which
    // reads through the repositories.
    let summary = get(format!("/teams/{team}/summary")).await.data().clone();
    assert_eq!(summary["openIssues"], 3.0);

    // A delete keeps the row, stamped, and every operation leaves it out.
    let deleted = call(&router, &ada, Method::DELETE, &issue, None).await.data().clone();
    assert_eq!(deleted["id"], crash["id"]);
    get(issue.clone()).await.refused(404, "Issue not found");
    assert_eq!(titles(get(format!("{issues}?sort=title")).await.data()), vec!["Fix typo", "Slow query"]);

    // A route requires its caller, and a row rule keeps each caller to the
    // rows that name them (D108).
    call(&router, "", Method::GET, "/teams", None)
        .await
        .refused(401, "Authentication required");
    let issue = get(format!("{issues}?sort=title")).await.data()["items"][0]["id"]
        .as_str()
        .expect("an issue's id")
        .to_owned();
    let notes = format!("/issues/{issue}/notes");
    let note = post(notes.clone(), json!({"text": "mine"})).await.data().clone();
    let path = format!("/notes/{}", note["id"].as_str().expect("the note's id"));
    assert_eq!(get(path.clone()).await.data(), &note);
    call(&router, &ben, Method::GET, &path, None)
        .await
        .refused(404, "Note not found");
    let listed = call(&router, &ben, Method::GET, "/notes", None).await;
    assert_eq!(listed.data()["items"].as_array().map(Vec::len), Some(0));
    let mine = get("/notes".into()).await.data().clone();
    assert_eq!(mine["items"].as_array().map(Vec::len), Some(1));

    let _ = std::fs::remove_dir_all(dir);
}

/// A request to the API with no transport under it (D116), answered on
/// the calling thread: no socket, no router of the host's, and no async
/// runtime, since nothing the API awaits over a SQLite file needs one.
fn ask(api: &Api, token: &str, method: Method, path: &str, body: Option<Value>) -> Reply {
    let mut request = Request::builder().method(method).uri(format!("/api{path}"));
    if !token.is_empty() {
        request = request.header(http::header::AUTHORIZATION, format!("Bearer {token}"));
    }
    let body = match body {
        Some(body) => {
            request = request.header(http::header::CONTENT_TYPE, "application/json");
            axum::body::Bytes::from(body.to_string())
        }
        None => axum::body::Bytes::new(),
    };
    let response = api::runtime::block_on(api.handle(request.body(body).expect("a request")));
    let status = response.status();
    let body = if response.body().is_empty() {
        Value::Null
    } else {
        serde_json::from_slice(response.body()).expect("a JSON body")
    };
    Reply { status, body }
}

#[test]
fn the_api_answers_a_request_with_no_transport_and_no_runtime_under_it() {
    assert!(
        tokio::runtime::Handle::try_current().is_err(),
        "the test is to run with no async runtime"
    );
    let (implementations, dir) = implementations("api");
    let api = Api::new(implementations);
    let ada = format!("{ADA}{EVERY}");

    // A generated table handler writes and reads the file.
    let core = ask(&api, &ada, Method::POST, "/teams", Some(json!({"key": "core", "name": "Core"})))
        .data()
        .clone();
    let team = core["id"].as_str().expect("the team's id").to_owned();
    assert_eq!(ask(&api, &ada, Method::GET, &format!("/teams/{team}"), None).data(), &core);
    ask(&api, &ada, Method::POST, &format!("/teams/{team}/issues"), Some(json!({"title": "Crash on save"}))).data();

    // The project's own implementation runs, through the repositories.
    let summary = ask(&api, &ada, Method::GET, &format!("/teams/{team}/summary"), None);
    assert_eq!(summary.data()["openIssues"], 1.0, "{}", summary.body);

    // What the router refuses, `handle` refuses the same way: no caller,
    // a body that does not decode, a row that is not there and a path no
    // route has.
    ask(&api, "", Method::GET, "/teams", None).refused(401, "Authentication required");
    let malformed = ask(&api, &ada, Method::POST, "/teams", Some(json!({"key": 7})));
    assert_eq!(malformed.status, StatusCode::BAD_REQUEST, "{}", malformed.body);
    ask(&api, &ada, Method::GET, "/teams/33333333-3333-4333-8333-333333333333", None).refused(404, "Team not found");
    let unrouted = ask(&api, &ada, Method::GET, "/nowhere", None);
    assert_eq!(unrouted.status, StatusCode::NOT_FOUND, "{}", unrouted.body);
    assert!(Api::UNROUTED.is_empty());

    // A clone of the API serves the same file.
    let again = api.clone();
    assert_eq!(ask(&again, &ada, Method::GET, &format!("/teams/{team}"), None).data(), &core);

    let _ = std::fs::remove_dir_all(dir);
}
