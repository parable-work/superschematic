//! The generated Rust server of fixture-tracker-api over a SQLite file,
//! with a comment's body an `Encrypted<T>` column and the view's body its
//! `@decrypted` member (D85, D116): the router's handlers seal the column
//! on a create and open it for the view, with the keyring
//! `tables::field_keys` reads from the API's config.

use std::sync::Arc;

use api::runtime::orm::sqlite::{rusqlite, Database};
use api::runtime::orm::{FieldCell, FieldKeyring};
use api::runtime::{ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Handlers, Implementations, TeamsImplementation, TeamsSummarizeTeamArgs};
use async_trait::async_trait;
use axum::body::Body;
use axum::Router;
use http::{Method, Request, StatusCode};
use http_body_util::BodyExt;
use serde_json::{json, Value};
use tower::ServiceExt;

const ADA: &str = "11111111-1111-4111-8111-111111111111:teams.read,teams.write,issues.read,issues.write";

/// The parity vectors' key (runtime/schema/testdata/fieldcrypt_parity.json).
const KEYS: &str = "AQIDBAUGBwgJCgsMDQ4PEBESExQVFhcYGRobHB0eHyA=";

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

/// summarizeTeam, the one operation no generated handler serves, which no
/// case calls.
struct Teams;

#[async_trait]
impl TeamsImplementation for Teams {
    async fn summarize_team(
        &self,
        _ctx: RequestContext,
        _args: TeamsSummarizeTeamArgs,
    ) -> Result<api::types::TeamSummary, ApiError> {
        Err(ApiError::not_implemented("teams.summarize_team is not implemented"))
    }
}

async fn call(router: &Router, method: Method, path: &str, body: Option<Value>) -> Value {
    let mut request = Request::builder()
        .method(method)
        .uri(format!("/api{path}"))
        .header(http::header::AUTHORIZATION, format!("Bearer {ADA}"));
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
    let body: Value = serde_json::from_slice(&bytes).expect("a JSON body");
    assert_eq!(status, StatusCode::OK, "{body}");
    body["data"].clone()
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn a_table_operation_seals_and_opens_an_encrypted_column() {
    // The keyring as the entrypoint reads it: the API's config holds the
    // database's key secret, and tables::field_keys its keyring.
    std::env::set_var("TRACKER_FIELD_KEYS", KEYS);
    let dir = std::env::temp_dir().join(format!("tracker-encrypted-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).expect("create the directory");
    let path = dir.join("tracker.sqlite");
    std::env::set_var("FIXTURE_TRACKER_DB_DATABASE_URL", format!("sqlite:{}", path.display()));
    rusqlite::Connection::open(&path)
        .and_then(|connection| connection.execute_batch(include_str!("../create.sql")))
        .expect("create the tables");
    let db = Arc::new(Database::open(&path).expect("open the file"));
    let config = api::config::load_env_config().expect("the API's config");
    let keys = api::tables::field_keys(&config).expect("the keyring of the config's secret");
    let handlers = Handlers {
        teams: Arc::new(Teams),
        authenticator: Arc::new(Bearer),
    };
    let router = build_router(Implementations::new(handlers, Arc::clone(&db), Arc::clone(&keys)));

    let team = call(&router, Method::POST, "/teams", Some(json!({"key": "core", "name": "Core"}))).await;
    let team = team["id"].as_str().expect("the team's id").to_owned();
    let issue = call(
        &router,
        Method::POST,
        &format!("/teams/{team}/issues"),
        Some(json!({"title": "Crash on save"})),
    )
    .await;
    let issue = issue["id"].as_str().expect("the issue's id").to_owned();

    // A create answers the body it was given, the view's @decrypted
    // member, and the file holds its envelope, sealed for the row.
    let comments = format!("/issues/{issue}/comments");
    let created = call(&router, Method::POST, &comments, Some(json!({"body": "Seen on 2.3"}))).await;
    assert_eq!(created["body"], "Seen on 2.3");
    let (row, envelope): (String, String) = db
        .read(|connection| {
            connection
                .query_row("SELECT id, body FROM comment", [], |row| Ok((row.get(0)?, row.get(1)?)))
                .map_err(|err| api::runtime::orm::sqlite::failure("read the comment", &err))
        })
        .expect("read the comment");
    assert!(keys.is_current(&envelope), "{envelope}");
    assert!(!envelope.contains("Seen"));
    let cell = FieldCell {
        table: "Comment",
        field: "body",
        row: &row,
    };
    assert_eq!(keys.open(cell, &envelope).expect("open the envelope"), "Seen on 2.3");

    // A list opens it for each row's view.
    let listed = call(&router, Method::GET, &comments, None).await;
    let items = listed["items"].as_array().expect("a page's items");
    assert_eq!(items.len(), 1);
    assert_eq!(items[0]["body"], "Seen on 2.3");
    assert_eq!(items[0]["id"], created["id"]);

    drop(router);
    let _ = std::fs::remove_dir_all(&dir);
}
