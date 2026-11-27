//! fixture-access-api's per-record access end to end in the Rust server
//! (D156, D181), run by TestRustAccessEndToEnd: the generated router over
//! fixture-access-db's Rust repositories on Postgres, in a schema of the
//! test's own at ACCESS_TEST_DATABASE_URL with the database's create.sql
//! applied. The cases are the TypeScript server's (tsrestgen's
//! access_runtime.test.ts), the calendar feed's among them (D152, D188),
//! which the runtime's orm::calendar routes serve beside the router.
//!
//! A request names its caller in its Authorization header: a grant's
//! token runs as the grant's holder (GrantAuthenticator);
//! `<subject>:<permissions>` is a user holding those permissions; and
//! `agent=<subject>/<for>:<permissions>` an agent run acting for the user
//! <for>. The router records the access log through Logged over the
//! database's @accessLog table.

use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use api::database_types::orm::postgres_definition;
use api::runtime::access::{
    access_log_of, active_grants, issue_grant, revoke_grant, ActiveGrantsFilter, EndsWhen,
    GrantAuthenticator, IssueGrant, Logged,
};
use api::runtime::orm::calendar::{calendar_routes, CalendarRoutes};
use api::runtime::orm::postgres::{deadpool_postgres, tokio_postgres, Repositories};
use api::runtime::orm::{AccessCaller, FieldKeyring, FindOptions, UpsertManyOptions, UpsertOptions, Values};
use api::runtime::postgres::Pool;
use api::runtime::{bearer_token, ActingFor, ApiError, Authenticator, Principal, PrincipalKind};
use api::{build_router, Handlers, Implementations};
use async_trait::async_trait;
use axum::body::Body;
use axum::Router;
use http::{Method, Request};
use http_body_util::BodyExt;
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

const READS: &str = "contracts.read,requests.read,requests.write";

/// The callers: an owner of folders and documents, another user, a third
/// the owner names, a party to a contract, an administrator of requests,
/// and an agent run.
struct People {
    owner_id: String,
    other_id: String,
    named_id: String,
    party_id: String,
    admin_id: String,
    run_id: String,
}

impl People {
    fn new() -> Self {
        Self {
            owner_id: uuid(0xa1),
            other_id: uuid(0xa2),
            named_id: uuid(0xa3),
            party_id: uuid(0xa4),
            admin_id: uuid(0xa5),
            run_id: uuid(0xb1),
        }
    }
    fn owner(&self) -> String {
        format!("{}:{READS}", self.owner_id)
    }
    fn other(&self) -> String {
        format!("{}:{READS}", self.other_id)
    }
    fn named(&self) -> String {
        format!("{}:{READS}", self.named_id)
    }
    fn party(&self) -> String {
        format!("{}:requests.read", self.party_id)
    }
    fn admin(&self) -> String {
        format!("{}:requests.read,requests.admin", self.admin_id)
    }
    fn revealer(&self) -> String {
        format!("{}:contracts.read,contracts.reveal", self.admin_id)
    }
    fn agent_for(&self, user: &str) -> String {
        format!("agent={}/{user}:{READS}", self.run_id)
    }
}

/// The user or the agent run the credential names; none without one.
struct Callers;

#[async_trait]
impl Authenticator for Callers {
    async fn authenticate(&self, request: &http::request::Parts) -> Result<Option<Principal>, ApiError> {
        let Some(token) = bearer_token(&request.headers) else {
            return Ok(None);
        };
        let (who, permissions) = token.split_once(':').unwrap_or((token, ""));
        let granted: Vec<&str> = permissions.split(',').filter(|p| !p.is_empty()).collect();
        if let Some(agent) = who.strip_prefix("agent=") {
            let (subject, for_whom) = agent.split_once('/').unwrap();
            return Ok(Some(
                Principal::new(subject, granted)
                    .of_kind(PrincipalKind::Agent)
                    .acting_for(ActingFor::user(for_whom)),
            ));
        }
        Ok(Some(Principal::new(who, granted)))
    }
}

/// A schema of its own holding fixture-access-db's tables, the pool whose
/// connections search it, and the connection that drops it.
struct Database {
    pool: Pool,
    admin: tokio_postgres::Client,
    schema: String,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

async fn database() -> Database {
    let url = std::env::var("ACCESS_TEST_DATABASE_URL").expect("ACCESS_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("access_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, admin, schema }
}

/// An answer: its status and its body.
struct Answer {
    status: u16,
    body: Value,
}

struct App {
    router: Router,
    pool: Pool,
    keys: Arc<FieldKeyring>,
}

impl App {
    /// A request to the app as the caller the credential names, or none.
    async fn call(&self, method: Method, path: &str, as_: Option<&str>, body: Option<Value>) -> Answer {
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
        let response = self.router.clone().oneshot(request.body(body).unwrap()).await.unwrap();
        let status = response.status().as_u16();
        let bytes = response.into_body().collect().await.unwrap().to_bytes();
        let body = if bytes.is_empty() { Value::Null } else { serde_json::from_slice(&bytes).unwrap() };
        Answer { status, body }
    }

    /// The data of a call that succeeds.
    async fn ok(&self, method: Method, path: &str, as_: &str, body: Option<Value>) -> Value {
        let answer = self.call(method.clone(), path, Some(as_), body).await;
        assert_eq!(answer.status, 200, "{method} {path} answered {}: {}", answer.status, answer.body);
        answer.body["data"].clone()
    }

    /// The status a call answers.
    async fn status(&self, method: Method, path: &str, as_: &str, body: Option<Value>) -> u16 {
        self.call(method, path, Some(as_), body).await.status
    }

    /// Every item of a list a caller sees.
    async fn items(&self, path: &str, as_: &str) -> Vec<Value> {
        let mut out = Vec::new();
        let mut next: Option<String> = None;
        loop {
            let url = match &next {
                None => path.to_owned(),
                Some(cursor) => format!("{path}{}cursor={cursor}", if path.contains('?') { '&' } else { '?' }),
            };
            let page = self.ok(Method::GET, &url, as_, None).await;
            out.extend(page["items"].as_array().cloned().unwrap_or_default());
            match page["next"].as_str() {
                Some(cursor) => next = Some(cursor.to_owned()),
                None => return out,
            }
        }
    }

    async fn titles(&self, path: &str, as_: &str) -> Vec<String> {
        let mut titles: Vec<String> = self
            .items(path, as_)
            .await
            .iter()
            .map(|row| row["title"].as_str().unwrap_or_default().to_owned())
            .collect();
        titles.sort();
        titles
    }

    /// The repositories as the system actor, which no caller's access
    /// holds, on a connection of the pool.
    async fn system<T>(&self, work: impl AsyncFnOnce(&Repositories<'_, deadpool_postgres::Client>) -> T) -> T {
        let client = self.pool.get().await.unwrap();
        let repositories = Repositories::new(&client, postgres_definition().unwrap())
            .unwrap()
            .as_system()
            .with_keys(&*self.keys);
        work(&repositories).await
    }

    /// The access log's rows, in the order they were written.
    async fn log(&self) -> Vec<Values> {
        self.system(async |db| {
            let options = FindOptions {
                limit: Some(1000),
                skip_count: true,
                order_by: vec![api::runtime::orm::OrderBy::asc("at")],
                ..FindOptions::default()
            };
            db.table("AccessEntry").unwrap().find_many(None, &options).await.unwrap().rows
        })
        .await
    }

    async fn log_of(&self, kind: &str) -> Vec<Values> {
        self.log().await.into_iter().filter(|row| row["kind"] == kind).collect()
    }
}

fn values(value: Value) -> Values {
    match value {
        Value::Object(members) => members,
        other => panic!("{other} is no object"),
    }
}

fn case(name: &str) {
    println!("case: {name}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn per_record_access() {
    let database = database().await;
    let keys = Arc::new(FieldKeyring::parse(KEYS).expect("the parity key"));
    let tables = api::tables::tables(database.pool.clone(), Arc::clone(&keys));
    let grants = GrantAuthenticator::new(tables.clone()).expect("the database holds grants and roles").with_inner(Arc::new(Callers));
    let log = access_log_of(&tables).expect("the definition").expect("the database holds an access log");
    let handlers = Handlers {
        authenticator: Arc::new(Logged::new(grants, Arc::new(log))),
    };
    let router = build_router(Implementations::new(handlers, database.pool.clone(), Arc::clone(&keys)));
    let app = App {
        router,
        pool: database.pool.clone(),
        keys,
    };
    let people = People::new();

    // The contracts, written by the system: contract A lists the party.
    let (contract_a, contract_b, unit_a, unit_b, request_a, request_b) = app
        .system(async |db| {
            let create = async |table: &str, row: Value| db.table(table).unwrap().create_one(&values(row)).await.unwrap();
            let a = create("Contract", json!({"name": "A", "terms": "Pay on the first"})).await;
            let b = create("Contract", json!({"name": "B", "terms": "Pay on the last"})).await;
            create("ContractParty", json!({"contractId": a["id"], "party": people.party_id})).await;
            let unit_a = create("Unit", json!({"name": "A1", "contractId": a["id"]})).await;
            let unit_b = create("Unit", json!({"name": "B1", "contractId": b["id"]})).await;
            let request_a = create("Request", json!({"unitId": unit_a["id"], "title": "Fix the door"})).await;
            let request_b = create("Request", json!({"unitId": unit_b["id"], "title": "Paint the hall"})).await;
            // A role a grant names: what a tenant may do, and the rows it
            // reaches from the contract a grant targets.
            create(
                "Role",
                json!({
                    "name": "tenant",
                    "permissions": ["requests.read", "requests.write", "contracts.read"],
                    "reaches": ["Unit:contract", "Request:unit.contract"],
                }),
            )
            .await;
            let id = |row: &Values| row["id"].as_str().unwrap().to_owned();
            (id(&a), id(&b), id(&unit_a), id(&unit_b), id(&request_a), id(&request_b))
        })
        .await;

    // A private row never appears to another user in a list, a read, a
    // search or a count.
    let mine = app
        .ok(Method::POST, "/api/folders", &people.owner(), Some(json!({"name": "Mine", "owner": people.owner_id, "audience": "private"})))
        .await;
    assert_eq!(mine["audience"], "private");
    let mine_id = mine["id"].as_str().unwrap().to_owned();
    let secret = app
        .ok(Method::POST, "/api/documents", &people.owner(), Some(json!({"folderId": mine_id, "title": "Secret plan"})))
        .await;
    // A document in a folder inherits its audience.
    assert_eq!(secret["audience"], "inherit");
    let secret_id = secret["id"].as_str().unwrap().to_owned();
    app.ok(Method::POST, "/api/documents", &people.owner(), Some(json!({"title": "Public plan", "audience": "everyone"}))).await;
    assert_eq!(app.titles("/api/documents", &people.owner()).await, ["Public plan", "Secret plan"]);
    assert_eq!(app.titles("/api/documents", &people.other()).await, ["Public plan"]);
    assert_eq!(app.titles("/api/documents?q=plan", &people.other()).await, ["Public plan"]);
    assert!(app.titles("/api/documents?q=secret", &people.other()).await.is_empty());
    let secret_path = format!("/api/documents/{secret_id}");
    assert_eq!(app.status(Method::GET, &secret_path, &people.other(), None).await, 404);
    assert_eq!(app.status(Method::GET, &secret_path, &people.owner(), None).await, 200);
    let folders: Vec<Value> = app.items("/api/folders", &people.other()).await;
    assert!(folders.iter().all(|folder| folder["name"] != "Mine"));
    // A write is held as a read is: the row is not there for another.
    assert_eq!(app.status(Method::PATCH, &secret_path, &people.other(), Some(json!({"title": "Mine now"}))).await, 404);
    // A count through repositories opened for a caller, as an
    // implementation opens its own, counts what the caller sees.
    let other = Principal::new(people.other_id.clone(), Vec::<String>::new());
    let counted = app
        .system(async |db| {
            let client = db.client();
            let held = Repositories::new(client, db.definition()).unwrap().with_access(AccessCaller::of(Some(&other)));
            let all = db.table("Document").unwrap().find_many(None, &FindOptions::default()).await.unwrap();
            let seen = held.table("Document").unwrap().find_many(None, &FindOptions::default()).await.unwrap();
            (all.total, seen.total)
        })
        .await;
    assert_eq!(counted, (Some(2), Some(1)));
    // An upsert, of one row or of many, through repositories that keep to a
    // caller is refused: its update could reach a row the caller does not
    // see (D156, D89).
    let refused = app
        .system(async |db| {
            let client = db.client();
            let held = Repositories::new(client, db.definition()).unwrap().with_access(AccessCaller::of(Some(&other)));
            let documents = held.table("Document").unwrap();
            let input = json!({"title": "Upserted"}).as_object().cloned().unwrap();
            let one = documents.upsert_one(&input, &["id"], &Values::new(), UpsertOptions::default()).await.unwrap_err().to_string();
            let many = documents.upsert_many(std::slice::from_ref(&input), &["id"], &["title"], UpsertManyOptions::default()).await.unwrap_err().to_string();
            (one, many)
        })
        .await;
    assert!(refused.0.contains("upsert_one of Document: the table's rows keep to the operation's caller (D156)"), "{}", refused.0);
    assert!(refused.1.contains("upsert_many of Document: the table's rows keep to the operation's caller (D156)"), "{}", refused.1);
    // The repository, outside any operation, reads every row, as a job
    // does.
    let read = app
        .system(async |db| db.table("Document").unwrap().get_one(&Value::from(secret_id.clone())).await.unwrap())
        .await;
    assert_eq!(read["title"], "Secret plan");
    case("a private row never appears to another user in a list, a read, a search or a count");

    // A named row is visible to its owner and to the principals it names.
    let shared = app
        .ok(
            Method::POST,
            "/api/documents",
            &people.owner(),
            Some(json!({"title": "Shared notes", "audience": "named", "audienceNamed": [people.named_id]})),
        )
        .await;
    let shared_path = format!("/api/documents/{}", shared["id"].as_str().unwrap());
    assert_eq!(app.status(Method::GET, &shared_path, &people.owner(), None).await, 200);
    assert_eq!(app.status(Method::GET, &shared_path, &people.named(), None).await, 200);
    assert_eq!(app.status(Method::GET, &shared_path, &people.other(), None).await, 404);
    assert!(app.titles("/api/documents", &people.named()).await.contains(&"Shared notes".to_owned()));
    assert!(!app.titles("/api/documents", &people.other()).await.contains(&"Shared notes".to_owned()));
    case("a named row is visible to its owner and to the principals it names");

    // A document inherits its folder's audience, and follows it when the
    // folder's changes.
    let team = app.ok(Method::POST, "/api/folders", &people.owner(), Some(json!({"name": "Team", "owner": people.owner_id}))).await;
    let team_path = format!("/api/folders/{}", team["id"].as_str().unwrap());
    let doc = app
        .ok(Method::POST, "/api/documents", &people.owner(), Some(json!({"folderId": team["id"], "title": "Team doc"})))
        .await;
    let doc_path = format!("/api/documents/{}", doc["id"].as_str().unwrap());
    assert_eq!(app.status(Method::GET, &doc_path, &people.other(), None).await, 200);
    // Only the folder's owner changes it: a row rule.
    assert_eq!(app.status(Method::PATCH, &team_path, &people.other(), Some(json!({"audience": "private"}))).await, 404);
    app.ok(Method::PATCH, &team_path, &people.owner(), Some(json!({"audience": "private"}))).await;
    assert_eq!(app.status(Method::GET, &doc_path, &people.other(), None).await, 404);
    assert_eq!(app.status(Method::GET, &doc_path, &people.owner(), None).await, 200);
    app.ok(Method::PATCH, &team_path, &people.owner(), Some(json!({"audience": "named", "audienceNamed": [people.other_id]}))).await;
    assert_eq!(app.status(Method::GET, &doc_path, &people.other(), None).await, 200);
    assert_eq!(app.status(Method::GET, &doc_path, &people.named(), None).await, 404);
    // A document that leaves its folder's audience keeps its own.
    app.ok(Method::PATCH, &doc_path, &people.owner(), Some(json!({"audience": "everyone"}))).await;
    app.ok(Method::PATCH, &team_path, &people.owner(), Some(json!({"audience": "private"}))).await;
    assert_eq!(app.status(Method::GET, &doc_path, &people.named(), None).await, 200);
    case("a document inherits its folder's audience, and follows it when the folder's changes");

    // An agent run sees what whom it acts for sees, and writes no
    // read-only row.
    let for_owner = people.agent_for(&people.owner_id);
    let for_other = people.agent_for(&people.other_id);
    let before = app.log_of("agentRead").await.len();
    assert_eq!(app.status(Method::GET, &secret_path, &for_owner, None).await, 200);
    assert_eq!(app.status(Method::GET, &secret_path, &for_other, None).await, 404);
    // Documents are read-only to agent runs unless a row says otherwise.
    assert_eq!(app.status(Method::PATCH, &secret_path, &for_owner, Some(json!({"title": "Edited"}))).await, 404);
    let open = app
        .ok(
            Method::POST,
            "/api/documents",
            &people.owner(),
            Some(json!({"title": "Agent may edit", "audience": "everyone", "agentAccess": "followRole"})),
        )
        .await;
    let edited = app
        .ok(
            Method::PATCH,
            &format!("/api/documents/{}", open["id"].as_str().unwrap()),
            &for_owner,
            Some(json!({"title": "Edited by a run"})),
        )
        .await;
    assert_eq!(edited["title"], "Edited by a run");
    let closed = app
        .ok(Method::POST, "/api/documents", &people.owner(), Some(json!({"title": "No agents", "audience": "everyone", "agentAccess": "none"})))
        .await;
    let closed_path = format!("/api/documents/{}", closed["id"].as_str().unwrap());
    assert_eq!(app.status(Method::GET, &closed_path, &for_owner, None).await, 404);
    assert_eq!(app.status(Method::GET, &closed_path, &people.owner(), None).await, 200);
    // Each read the run made is in the access log, with whom it acted for.
    let reads = app.log_of("agentRead").await;
    assert_eq!(reads.len(), before + 3);
    let last = reads.last().unwrap();
    assert_eq!(last["principal"], Value::from(people.run_id.clone()));
    assert_eq!(last["principalKind"], "agent");
    assert_eq!(last["actingFor"], Value::from(people.owner_id.clone()));
    assert_eq!(last["operation"], "getDocument");
    case("an agent run sees what whom it acts for sees, and writes no read-only row");

    // A row rule follows a path through the table's relations.
    assert_eq!(app.titles("/api/my/requests", &people.party()).await, ["Fix the door"]);
    assert!(app.titles("/api/my/requests", &people.other()).await.is_empty());
    assert_eq!(app.titles("/api/my/requests", &people.admin()).await, ["Fix the door", "Paint the hall"]);
    case("a row rule follows a path through the table's relations");

    // A party with a grant reaches only the rows its role's paths allow.
    let party = uuid(0xc1);
    let issued = app
        .system(async |db| {
            issue_grant(
                db,
                &IssueGrant {
                    holder: party.clone(),
                    holder_kind: PrincipalKind::Party,
                    role: "tenant".to_owned(),
                    target_type: "Contract".to_owned(),
                    target_id: contract_a.clone(),
                    starts_at: None,
                    ends_at: None,
                    ends_when: None,
                },
            )
            .await
            .unwrap()
        })
        .await;
    let token = issued.token.clone();
    assert!(token.starts_with("grant_"), "{token}");
    assert_eq!(app.titles("/api/requests", &token).await, ["Fix the door"]);
    assert_eq!(app.status(Method::GET, &format!("/api/requests/{request_a}"), &token, None).await, 200);
    assert_eq!(app.status(Method::GET, &format!("/api/requests/{request_b}"), &token, None).await, 404);
    let units: Vec<Value> = app.items("/api/units", &token).await;
    assert_eq!(units.iter().map(|unit| unit["name"].clone()).collect::<Vec<_>>(), [json!("A1")]);
    assert_eq!(app.status(Method::GET, &format!("/api/contracts/{contract_a}"), &token, None).await, 200);
    assert_eq!(app.status(Method::GET, &format!("/api/contracts/{contract_b}"), &token, None).await, 404);
    // A table the role's paths do not reach has no rows for it.
    assert!(app.items("/api/documents", &token).await.is_empty());
    // A row it makes is one its grant reaches, or no row at all.
    let made = app
        .ok(Method::POST, &format!("/api/units/{unit_a}/requests"), &token, Some(json!({"title": "Leaking tap"})))
        .await;
    assert_eq!(made["unitId"], Value::from(unit_a.clone()));
    assert_eq!(
        app.status(Method::POST, &format!("/api/units/{unit_b}/requests"), &token, Some(json!({"title": "Not mine"}))).await,
        403
    );
    let kept = app
        .system(async |db| {
            let filter = values(json!({"title": {"eq": "Not mine"}}));
            db.table("Request").unwrap().find_many(Some(&filter), &FindOptions::default()).await.unwrap().rows
        })
        .await;
    assert!(kept.is_empty(), "{kept:?}");
    // Each call is in the access log, by the grant's id.
    let calls: Vec<Values> = app.log_of("grantCall").await.into_iter().filter(|row| row["principal"] == party.as_str()).collect();
    assert_eq!(calls.len(), 9, "{calls:?}");
    assert_eq!(calls[0]["principalKind"], "party");
    assert_eq!(calls[0]["operation"], "listRequests");
    assert_eq!(calls[0]["credential"], issued.grant["id"]);
    case("a party with a grant reaches only the rows its role's paths allow");

    // An expired or revoked grant is refused on the very next call.
    let issue = async |holder: String, contract: &str, ends_at: Option<SystemTime>, ends_when: Option<EndsWhen>| {
        app.system(async |db| {
            issue_grant(
                db,
                &IssueGrant {
                    holder,
                    holder_kind: PrincipalKind::Party,
                    role: "tenant".to_owned(),
                    target_type: "Contract".to_owned(),
                    target_id: contract.to_owned(),
                    starts_at: None,
                    ends_at,
                    ends_when,
                },
            )
            .await
            .unwrap()
        })
        .await
    };
    // Revoked.
    let revoked = issue(uuid(0xc2), &contract_a, None, None).await;
    assert_eq!(app.status(Method::GET, "/api/requests", &revoked.token, None).await, 200);
    let revoked_id = revoked.grant["id"].as_str().unwrap().to_owned();
    app.system(async |db| revoke_grant(db.client(), db.definition(), &revoked_id, SystemTime::now()).await.unwrap())
        .await;
    assert_eq!(app.status(Method::GET, "/api/requests", &revoked.token, None).await, 401);
    // Ended by its date.
    let dated = issue(uuid(0xc3), &contract_a, Some(SystemTime::now() + Duration::from_millis(400)), None).await;
    assert_eq!(app.status(Method::GET, "/api/requests", &dated.token, None).await, 200);
    tokio::time::sleep(Duration::from_millis(500)).await;
    assert_eq!(app.status(Method::GET, "/api/requests", &dated.token, None).await, 401);
    // Ended by its target reaching a state.
    let closing = issue(
        uuid(0xc4),
        &contract_b,
        None,
        Some(EndsWhen {
            field: "status".to_owned(),
            state: "closed".to_owned(),
        }),
    )
    .await;
    assert_eq!(app.titles("/api/requests", &closing.token).await, ["Paint the hall"]);
    app.system(async |db| {
        let update = values(json!({"status": "closed"}));
        db.table("Contract").unwrap().update_one(&Value::from(contract_b.clone()), &update, &Default::default()).await.unwrap();
    })
    .await;
    assert_eq!(app.status(Method::GET, "/api/requests", &closing.token, None).await, 401);
    // An unknown token is refused as an ended one is.
    assert_eq!(app.status(Method::GET, "/api/requests", "grant_unknown", None).await, 401);
    // The read of the active grants holds only the first grant still.
    let active = app
        .system(async |db| active_grants(db.client(), db.definition(), &ActiveGrantsFilter::default()).await.unwrap())
        .await;
    assert_eq!(active.iter().map(|grant| grant["holder"].clone()).collect::<Vec<_>>(), [Value::from(party.clone())]);
    case("an expired or revoked grant is refused on the very next call");

    // A reveal opens the value for a holder of its permission and writes
    // an access-log row.
    let contract_path = format!("/api/contracts/{contract_a}");
    let masked = app.ok(Method::GET, &contract_path, &people.owner(), None).await;
    assert_eq!(masked["terms"], "[secret]");
    assert!(app.log_of("reveal").await.is_empty());
    let opened = app.ok(Method::GET, &contract_path, &people.revealer(), None).await;
    assert_eq!(opened["terms"], "Pay on the first");
    let reveals = app.log_of("reveal").await;
    assert_eq!(reveals.len(), 1, "{reveals:?}");
    assert_eq!(reveals[0]["principal"], Value::from(people.admin_id.clone()));
    assert_eq!(reveals[0]["principalKind"], "user");
    assert_eq!(reveals[0]["operation"], "getContract");
    assert_eq!(reveals[0]["targetType"], "Contract");
    assert_eq!(reveals[0]["targetId"], Value::from(contract_a.clone()));
    assert_eq!(reveals[0]["field"], "terms");
    // A grant's holder whose role holds no reveal sees it masked.
    assert_eq!(app.ok(Method::GET, &contract_path, &token, None).await["terms"], "[secret]");
    case("a reveal opens the value for a holder of its permission and writes an access-log row");

    // A calendar feed shows only the rows its token's principal reads,
    // busy or in full, and a rotated or revoked one is refused (D152).
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
    app.system(async |db| {
        db.table("Role").unwrap().create_one(&values(json!({"name": "calendarFeed", "permissions": ["documents.calendar"]}))).await.unwrap();
    })
    .await;
    let now = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs();
    let at = |seconds: u64| api::runtime::orm::calendar::iso(i64::try_from(seconds * 1000).unwrap());
    let window = format!("from={}&to={}", at(now - 86_400), at(now + 86_400));
    // The read: each caller's documents of the day, as each reads them.
    let read = async |as_: &str| -> Vec<String> {
        let (status, _, text) = send(Method::GET, &format!("/api/calendar?{window}"), Some(as_), None).await;
        assert_eq!(status, 200, "{text}");
        let body: Value = serde_json::from_str(&text).unwrap();
        body["data"]["entries"].as_array().unwrap().iter().map(|e| e["title"].as_str().unwrap().to_owned()).collect()
    };
    let owner_reads = read(&people.owner()).await;
    assert!(owner_reads.contains(&"Secret plan".to_owned()) && owner_reads.contains(&"Public plan".to_owned()), "{owner_reads:?}");
    let other_reads = read(&people.other()).await;
    assert!(other_reads.contains(&"Public plan".to_owned()));
    assert!(!other_reads.contains(&"Secret plan".to_owned()));
    // The other user's feed, in full, read by a client with no header.
    let issue = async |as_: &str, detail: &str| -> Value {
        let (status, _, text) = send(Method::POST, "/api/calendar/feeds", Some(as_), Some(json!({"role": "calendarFeed", "detail": detail}))).await;
        assert_eq!(status, 201, "{text}");
        serde_json::from_str::<Value>(&text).unwrap()["data"].clone()
    };
    let other_feed = issue(&people.other(), "full").await;
    assert!(other_feed["token"].as_str().unwrap().starts_with("grant_"));
    let other_path = other_feed["path"].as_str().unwrap().to_owned();
    let (status, kind, ics) = send(Method::GET, &other_path, None, None).await;
    assert_eq!(status, 200);
    assert!(kind.contains("text/calendar"), "{kind}");
    assert!(ics.contains("SUMMARY:Public plan"), "{ics}");
    assert!(!ics.contains("Secret plan"));
    // The owner's busy feed holds the owner's private document, as Busy
    // and a reference, and no title.
    let owner_feed = issue(&people.owner(), "busy").await;
    let (_, _, busy) = send(Method::GET, owner_feed["path"].as_str().unwrap(), None, None).await;
    assert!(!busy.contains("plan"), "{busy}");
    assert_eq!(busy.matches("BEGIN:VEVENT").count(), read(&people.owner()).await.len());
    let reference = busy.split("SUMMARY:Busy (").nth(1).and_then(|rest| rest.split(')').next()).unwrap();
    assert!(reference.len() == 8 && reference.chars().all(|c| "ABCDEFGHJKLMNPQRSTUVWXYZ23456789".contains(c)), "{reference}");
    // A feed's token is no credential of any other route.
    assert_eq!(app.status(Method::GET, "/api/documents", other_feed["token"].as_str().unwrap(), None).await, 401);
    // Rotated: the old URL is refused, the new one reads.
    let (status, _, text) = send(Method::POST, &format!("/api/calendar/feeds/{}/rotate", other_feed["id"].as_str().unwrap()), Some(&people.other()), None).await;
    assert_eq!(status, 200, "{text}");
    let rotated = serde_json::from_str::<Value>(&text).unwrap()["data"].clone();
    assert_eq!(send(Method::GET, &other_path, None, None).await.0, 401);
    let rotated_path = rotated["path"].as_str().unwrap().to_owned();
    assert_eq!(send(Method::GET, &rotated_path, None, None).await.0, 200);
    // Another user cannot revoke it; its holder can, and it is refused.
    let rotated_id = format!("/api/calendar/feeds/{}", rotated["id"].as_str().unwrap());
    assert_eq!(send(Method::DELETE, &rotated_id, Some(&people.owner()), None).await.0, 404);
    assert_eq!(send(Method::DELETE, &rotated_id, Some(&people.other()), None).await.0, 204);
    assert_eq!(send(Method::GET, &rotated_path, None, None).await.0, 401);
    assert_eq!(send(Method::GET, "/api/calendar/feed/grant_unknown.ics", None, None).await.0, 401);
    case("a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused");

    drop(app);
    database.admin.batch_execute(&format!("DROP SCHEMA IF EXISTS {} CASCADE", database.schema)).await.unwrap();
}
