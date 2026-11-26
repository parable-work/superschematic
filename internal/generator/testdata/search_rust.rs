//! fixture-search-db end to end in the Rust repositories and runtime
//! (D155, D188), run by TestRustSearchEndToEnd: six @searchable tables
//! share one search index. The generated create.sql is applied in a schema
//! of the test's own at SEARCH_TEST_DATABASE_URL, and the cases are the
//! TypeScript repositories' (tsgen's search.test.ts), through the
//! generated repositories and the runtime's orm::search_tables. drain()
//! runs what the queue's worker runs: each ready message of the
//! SearchIndexing queue, handed to index_search.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use axum::body::Body;
use http::Request;
use http_body_util::BodyExt;
use runtime::access::{issue_grant, revoke_grant, GrantAuthenticator, IssueGrant};
use runtime::orm::postgres::{deadpool_postgres, tokio_postgres, Tables};
use runtime::orm::search_tables::{
    call_search_tool, index_search, search_embeddings_of, search_routes, search_tables, Embedder,
    SearchEmbeddings, SearchIndexing, SearchOptions, SearchRequest, SearchResult, SearchRoutes,
};
use runtime::providers::{ModelCompletion, ModelEmbedRequest, ModelEmbedding, ModelProvider, ModelTurn};
use runtime::orm::{AccessCaller, Caller, Error, FieldKeyring, FindOptions, Values, WriteOptions};
use runtime::{Authenticator, BoxError, Principal, PrincipalKind};
use serde_json::{json, Value};
use tower::ServiceExt;
use types::orm::postgres::Repositories;
use types::orm::postgres_definition;

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

/// A base62 UUID's hyphenated text, as the database writes it.
fn hyphen(base62: &str) -> String {
    let mut n = 0u128;
    for c in base62.bytes() {
        n = n * 62 + BASE62.iter().position(|b| *b == c).unwrap() as u128;
    }
    let h = format!("{n:032x}");
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], &h[12..16], &h[16..20], &h[20..])
}

fn values(value: Value) -> Values {
    value.as_object().cloned().unwrap()
}

fn case(name: &str) {
    println!("case: {name}");
}

fn user(subject: &str) -> Principal {
    Principal::new(subject, Vec::<String>::new())
}

/// The fields each table's index row's title and body are of.
const INDEXED: &[(&str, &[&str])] = &[
    ("Folder", &["name"]),
    ("Document", &["title", "text"]),
    ("Contract", &["name", "summary"]),
    ("Unit", &["name", "notes"]),
    ("Request", &["title", "details", "resolution"]),
    ("Note", &["title", "text"]),
];

fn words_of(value: Option<&Value>) -> Vec<String> {
    let text = match value {
        Some(Value::String(text)) => text.to_lowercase(),
        _ => String::new(),
    };
    text.split(|c: char| !c.is_alphanumeric()).map(str::to_owned).collect()
}

/// A schema of its own holding fixture-search-db's tables, and the pool
/// whose connections search it.
struct Database {
    pool: deadpool_postgres::Pool,
    admin: tokio_postgres::Client,
    schema: String,
    keys: FieldKeyring,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

async fn database() -> Database {
    let url = std::env::var("SEARCH_TEST_DATABASE_URL").expect("SEARCH_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("search_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, admin, schema, keys: FieldKeyring::parse(KEYS).unwrap() }
}

/// Whom the repositories act and read as: a user, whose audience they
/// keep to, or the system, which reads every row.
#[derive(Clone)]
enum Who {
    User(String),
    System,
}

type Repos<'a> = Repositories<'a, deadpool_postgres::Transaction<'a>>;

/// A stand-in model provider (D159): a text's vector says whether it is
/// about a door, counting the texts it embeds.
struct Doors(Arc<AtomicUsize>);

#[async_trait]
impl ModelProvider for Doors {
    async fn complete(&self, _: &ModelTurn) -> Result<ModelCompletion, BoxError> {
        Err("the stand-in completes nothing".into())
    }

    async fn embed(&self, request: &ModelEmbedRequest) -> Result<Option<ModelEmbedding>, BoxError> {
        assert_eq!(request.model.as_deref(), Some("stand-in-1"));
        self.0.fetch_add(request.texts.len(), Ordering::SeqCst);
        let vectors = request.texts.iter().map(|t| if t.to_lowercase().contains("door") { vec![1.0, 0.0] } else { vec![0.0, 1.0] }).collect();
        Ok(Some(ModelEmbedding { vectors, usage: None }))
    }
}

/// A model provider that embeds nothing, as one that leaves `embed` out.
struct Talker;

#[async_trait]
impl ModelProvider for Talker {
    async fn complete(&self, _: &ModelTurn) -> Result<ModelCompletion, BoxError> {
        Err("the stand-in completes nothing".into())
    }
}

/// A model that embeds every text as about a door.
struct AllDoors;

#[async_trait]
impl Embedder for AllDoors {
    async fn embed(&self, texts: &[String]) -> Result<Vec<Vec<f64>>, BoxError> {
        Ok(texts.iter().map(|_| vec![1.0, 0.0]).collect())
    }
}

/// A model that is down.
struct Down;

#[async_trait]
impl Embedder for Down {
    async fn embed(&self, _: &[String]) -> Result<Vec<Vec<f64>>, BoxError> {
        Err("down".into())
    }
}

fn embeddings(embedder: Arc<dyn Embedder>) -> SearchOptions {
    SearchOptions { embeddings: Some(SearchEmbeddings { model: "stand-in-1".to_owned(), embedder, max_chars: None }), ..SearchOptions::default() }
}

impl Database {
    /// Runs `work` in a transaction of the repositories as `who`, and
    /// commits what it kept.
    async fn tx<T>(&self, who: &Who, work: impl AsyncFnOnce(&Repos<'_>) -> T) -> T {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let repos = Repositories::new(&tx).unwrap().with_keys(&self.keys);
        let repos = match who {
            Who::System => repos.as_system(),
            Who::User(user) => repos
                .acting(Some(user.clone()))
                .with_caller(Caller { subject: user.clone(), permissions: Vec::new(), stands_in: false })
                .with_access(AccessCaller::of(Some(&Principal::new(user.clone(), Vec::<String>::new())))),
        };
        let out = work(&repos).await;
        drop(repos);
        tx.commit().await.unwrap();
        out
    }

    async fn create(&self, who: &Who, table: &str, row: Value) -> String {
        self.tx(who, async |r| {
            let row = r.untyped().table(table)?.create_one(&values(row)).await?;
            Ok::<_, Error>(row["id"].as_str().unwrap().to_owned())
        })
        .await
        .unwrap()
    }

    async fn update(&self, who: &Who, table: &str, key: &str, set: Value) {
        self.tx(who, async |r| r.untyped().table(table)?.update_one(&json!(key), &values(set), &WriteOptions::default()).await.map(|_| ()))
            .await
            .unwrap();
    }

    async fn delete(&self, table: &str, key: &str) {
        self.tx(&Who::System, async |r| r.untyped().table(table)?.delete_one(&json!(key), &WriteOptions::default()).await)
            .await
            .unwrap();
    }

    /// What the queue's worker runs: every ready message, indexed.
    async fn drain(&self, options: &SearchOptions) -> usize {
        let definition = postgres_definition().unwrap();
        let mut client = self.pool.get().await.unwrap();
        let mut n = 0;
        loop {
            let rows = client
                .query(
                    "DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key",
                    &[],
                )
                .await
                .unwrap();
            if rows.is_empty() {
                return n;
            }
            for row in &rows {
                let message = SearchIndexing { source_table: row.get(0), source_key: row.get(1) };
                index_search(&mut client, definition, &message, options).await.unwrap();
            }
            n += rows.len();
        }
    }

    async fn sql(&self, statement: &str) -> u64 {
        let client = self.pool.get().await.unwrap();
        client.execute(statement, &[]).await.unwrap()
    }

    async fn search(&self, principal: Option<&Principal>, request: SearchRequest, options: &SearchOptions) -> Result<SearchResult, Error> {
        let client = self.pool.get().await.unwrap();
        let repos = Repositories::new(&client).unwrap();
        search_tables(repos.untyped(), principal, &request, options).await
    }

    async fn found(&self, principal: Option<&Principal>, q: &str, options: &SearchOptions, edit: impl FnOnce(&mut SearchRequest)) -> (SearchResult, Vec<String>) {
        let mut request = SearchRequest { q: q.to_owned(), limit: Some(100), ..SearchRequest::default() };
        edit(&mut request);
        let result = self.search(principal, request, options).await.unwrap();
        let keys = result.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        (result, keys)
    }

    async fn keys(&self, principal: &Principal, q: &str) -> Vec<String> {
        self.found(Some(principal), q, &SearchOptions::default(), |_| {}).await.1
    }

    /// The rows of every @searchable table `principal`'s list reads show
    /// whose title or body holds `word`, as table:key, sorted.
    async fn listed(&self, principal: &Principal, word: &str) -> Vec<String> {
        let client = self.pool.get().await.unwrap();
        let repos = Repositories::new(&client).unwrap().with_keys(&self.keys).with_access(AccessCaller::of(Some(principal)));
        let mut out = Vec::new();
        for (table, fields) in INDEXED {
            let options = FindOptions { limit: Some(1000), skip_count: true, ..FindOptions::default() };
            for row in repos.untyped().table(table).unwrap().find_many(None, &options).await.unwrap().rows {
                if fields.iter().any(|field| words_of(row.get(*field)).iter().any(|w| w == word)) {
                    out.push(format!("{table}:{}", row["id"].as_str().unwrap()));
                }
            }
        }
        out.sort();
        out
    }

    async fn drop_schema(&self) {
        self.admin.batch_execute(&format!("DROP SCHEMA IF EXISTS {} CASCADE", self.schema)).await.unwrap();
    }
}

fn sorted(mut keys: Vec<String>) -> Vec<String> {
    keys.sort();
    keys
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn search_across_tables_on_postgres() {
    let db = database().await;
    let alice_id = uuid(0xa1);
    let bob_id = uuid(0xb0);
    let party = "3cKSm7Pv2EM8Wl4z4XvMh1";
    let alice = Who::User(alice_id.clone());
    let system = Who::System;
    let (as_alice, as_bob) = (user(&alice_id), user(&bob_id));
    let none = SearchOptions::default();

    // Two folders: alice's own, and one everyone sees; a document in each.
    let private_folder = db.create(&alice, "Folder", json!({ "name": "Walnut private", "owner": alice_id, "audience": "private" })).await;
    let shared_folder = db.create(&alice, "Folder", json!({ "name": "Walnut shared", "owner": alice_id })).await;
    let private_doc = db.create(&alice, "Document", json!({ "folderId": private_folder, "title": "Walnut plan", "text": "The plan in brief." })).await;
    let shared_doc = db.create(&alice, "Document", json!({ "folderId": shared_folder, "title": "Memo", "text": "A walnut desk for the hall." })).await;
    // Two contracts, the first with a unit and a request about it.
    let contract_a = db.create(&system, "Contract", json!({ "name": "Walnut supply", "summary": "Timber for the year.", "terms": "Pay in obsidian." })).await;
    let contract_b = db.create(&system, "Contract", json!({ "name": "Bolts", "summary": "Bolts and a walnut trim." })).await;
    let unit_a = db.create(&system, "Unit", json!({ "name": "Walnut unit", "contractId": contract_a })).await;
    let unit_b = db.create(&system, "Unit", json!({ "name": "Annex", "notes": "Walnut floors.", "contractId": contract_b })).await;
    let request_a = db.create(&system, "Request", json!({ "unitId": unit_a, "title": "Walnut leak", "details": "Water under the sink." })).await;
    let request_b = db.create(&system, "Request", json!({ "unitId": unit_b, "title": "Hinge", "details": "A walnut door sticks." })).await;
    // Notes in two projects, one deleted.
    let note_p1 = db.create(&system, "Note", json!({ "project": "p1", "title": "Walnut idea", "text": "Oil it twice." })).await;
    let note_p2 = db.create(&system, "Note", json!({ "project": "p2", "title": "Pine idea", "text": "A walnut stain on pine." })).await;
    let note_gone = db.create(&system, "Note", json!({ "project": "p1", "title": "Walnut draft", "text": "Gone soon." })).await;
    db.delete("Note", &note_gone).await;
    // A role a party's grant names: the units and requests of its contract.
    db.create(&system, "Role", json!({ "name": "contract-party", "permissions": [], "reaches": ["Unit:contract", "Request:unit.contract"] })).await;
    assert!(db.drain(&none).await > 0);

    case("two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables");
    {
        for principal in [&as_alice, &as_bob] {
            let (result, keys) = db.found(Some(principal), "walnut", &none, |_| {}).await;
            assert_eq!(sorted(keys.clone()), db.listed(principal, "walnut").await);
            assert_eq!(result.total, keys.len() as u64);
            let tables: std::collections::BTreeSet<&str> = result.items.iter().map(|hit| hit.table.as_str()).collect();
            assert!(tables.len() >= 5, "{tables:?}");
            // A page of three at a time reads the same rows, in the same
            // order, with the same total on every page.
            let mut paged = Vec::new();
            let mut offset = 0;
            loop {
                let page = db
                    .search(Some(principal), SearchRequest { q: "Walnut".to_owned(), limit: Some(3), offset: Some(offset), ..SearchRequest::default() }, &none)
                    .await
                    .unwrap();
                assert_eq!(page.total, result.total);
                paged.extend(page.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)));
                let places: Vec<u64> = page.items.iter().map(|hit| hit.place).collect();
                let want: Vec<u64> = (0..page.items.len() as u64).map(|i| offset as u64 + i + 1).collect();
                assert_eq!(places, want);
                if !page.more {
                    break;
                }
                offset += 3;
            }
            assert_eq!(paged, keys);
        }
        let alices = db.keys(&as_alice, "walnut").await;
        let bobs = db.keys(&as_bob, "walnut").await;
        assert!(alices.contains(&format!("Folder:{private_folder}")));
        assert!(alices.contains(&format!("Document:{private_doc}")));
        assert!(!bobs.contains(&format!("Folder:{private_folder}")));
        assert!(!bobs.contains(&format!("Document:{private_doc}")));
        assert!(bobs.contains(&format!("Document:{shared_doc}")));
        assert_eq!(alices.len() - bobs.len(), 2);
        // A soft-deleted note is in no one's search.
        assert!(!alices.contains(&format!("Note:{note_gone}")));
        // A match in a title ranks above one in a body alone.
        let place = |key: &str| bobs.iter().position(|k| k == key).unwrap();
        assert!(place(&format!("Contract:{contract_a}")) < place(&format!("Contract:{contract_b}")));
        let (result, _) = db.found(Some(&as_bob), "walnut", &none, |_| {}).await;
        let hit = result.items.iter().find(|h| h.key == contract_b).unwrap();
        assert_eq!(hit.matched, "body");
        let matched: Vec<String> = hit.snippet.iter().filter(|p| p.matched).map(|p| p.text.to_lowercase()).collect();
        assert_eq!(matched, ["walnut"]);
        // Two words: a row holds both.
        assert_eq!(db.keys(&as_bob, "walnut sink").await, [format!("Request:{request_a}")]);
        // An anonymous caller sees what everyone may.
        let (_, anonymous) = db.found(None, "walnut", &none, |_| {}).await;
        assert_eq!(sorted(anonymous), sorted(bobs));
    }

    case("a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing");
    {
        let issued = db
            .tx(&system, async |r| {
                issue_grant(
                    r.untyped(),
                    &IssueGrant {
                        holder: party.to_owned(),
                        holder_kind: PrincipalKind::Party,
                        role: "contract-party".to_owned(),
                        target_type: "Contract".to_owned(),
                        target_id: contract_a.clone(),
                        starts_at: None,
                        ends_at: None,
                        ends_when: None,
                    },
                )
                .await
            })
            .await
            .unwrap();
        let tables = Tables::new(db.pool.clone(), postgres_definition);
        let authenticate: Arc<dyn Authenticator> = Arc::new(GrantAuthenticator::new(tables.clone()).unwrap());
        let parts = |token: &str| Request::get("/").header("authorization", format!("Bearer {token}")).body(()).unwrap().into_parts().0;
        let principal = authenticate.authenticate(&parts(&issued.token)).await.unwrap().unwrap();
        assert_eq!(principal.kind, PrincipalKind::Party);
        let (result, keys) = db.found(Some(&principal), "walnut", &none, |_| {}).await;
        assert_eq!(sorted(keys.clone()), db.listed(&principal, "walnut").await);
        assert_eq!(sorted(keys.clone()), sorted(vec![format!("Contract:{contract_a}"), format!("Request:{request_a}"), format!("Unit:{unit_a}")]));
        assert_eq!(result.total, 3);
        // The route, as the grant's holder.
        let route = search_routes(SearchRoutes::new(tables.clone(), authenticate.clone()));
        let get = |path: &str, token: Option<&str>| {
            let mut request = Request::get(path);
            if let Some(token) = token {
                request = request.header("authorization", format!("Bearer {token}"));
            }
            request.body(Body::empty()).unwrap()
        };
        let response = route.clone().oneshot(get("/api/search?q=walnut&limit=2", Some(&issued.token))).await.unwrap();
        assert_eq!(response.status(), 200);
        let body: Value = serde_json::from_slice(&response.into_body().collect().await.unwrap().to_bytes()).unwrap();
        assert_eq!(body["data"]["total"], 3);
        assert_eq!(body["data"]["more"], true);
        let routed: Vec<String> = body["data"]["items"].as_array().unwrap().iter().map(|hit| format!("{}:{}", hit["table"].as_str().unwrap(), hit["key"].as_str().unwrap())).collect();
        assert_eq!(routed, keys[..2]);
        // The MCP tool, as the same principal.
        let client = db.pool.get().await.unwrap();
        let repos = Repositories::new(&client).unwrap();
        let args = json!({ "q": "walnut", "tables": ["Unit", "Request", "Folder"] });
        let called = call_search_tool(repos.untyped(), Some(&principal), args.as_object().unwrap(), &none).await.unwrap();
        let called: Vec<String> = called.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        assert_eq!(sorted(called), sorted(vec![format!("Request:{request_a}"), format!("Unit:{unit_a}")]));
        // An agent run's token is a grant too (kind agent): its search keeps
        // to what the grant reaches, as the MCP endpoint answers it.
        let run = db
            .tx(&system, async |r| {
                issue_grant(
                    r.untyped(),
                    &IssueGrant {
                        holder: uuid(0xa9),
                        holder_kind: PrincipalKind::Agent,
                        role: "contract-party".to_owned(),
                        target_type: "Contract".to_owned(),
                        target_id: contract_a.clone(),
                        starts_at: None,
                        ends_at: None,
                        ends_when: None,
                    },
                )
                .await
            })
            .await
            .unwrap();
        let agent = authenticate.authenticate(&parts(&run.token)).await.unwrap().unwrap();
        assert_eq!(agent.kind, PrincipalKind::Agent);
        let args = json!({ "q": "walnut" });
        let called = call_search_tool(repos.untyped(), Some(&agent), args.as_object().unwrap(), &none).await.unwrap();
        let called: Vec<String> = called.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        assert_eq!(sorted(called.clone()), sorted(keys.clone()));
        assert_eq!(sorted(called), db.listed(&agent, "walnut").await);
        // Revoked, the very next call is refused, and no principal finds
        // nothing.
        let id = issued.grant["id"].as_str().unwrap().to_owned();
        revoke_grant(&client, postgres_definition().unwrap(), &id, SystemTime::now()).await.unwrap();
        assert!(authenticate.authenticate(&parts(&issued.token)).await.is_err());
        let refused = route.clone().oneshot(get("/api/search?q=walnut", Some(&issued.token))).await.unwrap();
        assert_eq!(refused.status(), 401);
        let anonymous = search_routes(SearchRoutes::new(tables, Arc::new(GrantAuthenticator::new(Tables::new(db.pool.clone(), postgres_definition)).unwrap())));
        assert_eq!(anonymous.oneshot(get("/api/search?q=walnut", None)).await.unwrap().status(), 401);
    }

    case("a row's conversation and files are indexed with it, and an encrypted field never is");
    {
        db.tx(&alice, async |r| {
            r.untyped()
                .table("Unit")?
                .operation("comments", "post", &json!(unit_b), &json!({ "body": "The sycamore by the gate needs a trim." }), &WriteOptions::default())
                .await
        })
        .await
        .unwrap();
        db.sql(&format!(
            "INSERT INTO unit_attachments (unit_id, name, media_type, size, content_hash, object_key, state, extraction, extracted_text) VALUES ('{}', 'survey.pdf', 'application/pdf', 10, '{}', 'attachments/survey', 'ready', 'done', 'Juniper hedges line the drive.')",
            hyphen(&unit_b),
            "0".repeat(64)
        ))
        .await;
        assert_eq!(db.drain(&none).await, 1);
        let (sycamore, _) = db.found(Some(&as_bob), "sycamore", &none, |_| {}).await;
        assert_eq!(sycamore.items.iter().map(|h| format!("{}:{}", h.table, h.key)).collect::<Vec<_>>(), [format!("Unit:{unit_b}")]);
        assert_eq!(sycamore.items[0].matched, "comments");
        let (juniper, _) = db.found(Some(&as_bob), "juniper", &none, |_| {}).await;
        assert_eq!(juniper.items.iter().map(|h| format!("{}:{}", h.table, h.key)).collect::<Vec<_>>(), [format!("Unit:{unit_b}")]);
        assert_eq!(juniper.items[0].matched, "files");
        // The contract's terms are sealed at rest and never reach the index.
        assert_eq!(db.found(Some(&as_alice), "obsidian", &none, |_| {}).await.0.total, 0);
        let client = db.pool.get().await.unwrap();
        let row = client
            .query_one("SELECT count(*)::int AS n FROM search_index WHERE concat_ws(' ', title, body, comments, files) ILIKE '%obsidian%'", &[])
            .await
            .unwrap();
        assert_eq!(row.get::<_, i32>(0), 0);
    }

    case("an index row follows its row: an audience change and a delete at once, a change of text once the queue drains");
    {
        assert!(db.keys(&as_bob, "walnut").await.contains(&format!("Folder:{shared_folder}")));
        // Alice keeps the shared folder to herself: its documents inherit
        // it. Neither shows to bob, with no drain, since the trigger copied
        // it.
        db.update(&alice, "Folder", &shared_folder, json!({ "audience": "private" })).await;
        let bobs = db.keys(&as_bob, "walnut").await;
        assert!(!bobs.contains(&format!("Folder:{shared_folder}")));
        assert!(!bobs.contains(&format!("Document:{shared_doc}")));
        assert_eq!(sorted(bobs), db.listed(&as_bob, "walnut").await);
        db.update(&alice, "Folder", &shared_folder, json!({ "audience": "everyone" })).await;
        assert!(db.keys(&as_bob, "walnut").await.contains(&format!("Document:{shared_doc}")));
        // A delete takes the index row with it, at once.
        db.delete("Request", &request_b).await;
        assert!(!db.keys(&as_bob, "walnut").await.contains(&format!("Request:{request_b}")));
        // A change of text waits for the queue: found by its old words
        // until the worker indexes it again.
        db.update(&system, "Note", &note_p1, json!({ "title": "Chestnut idea" })).await;
        assert_eq!(db.found(Some(&as_bob), "chestnut", &none, |_| {}).await.0.total, 0);
        assert_eq!(db.drain(&none).await, 1);
        assert_eq!(db.keys(&as_bob, "chestnut").await, [format!("Note:{note_p1}")]);
        // An update that changes no indexed field enqueues nothing.
        db.update(&system, "Unit", &unit_a, json!({ "contractId": contract_a })).await;
        assert_eq!(db.drain(&none).await, 0);
    }

    case("scope keeps a search to the app's filter");
    {
        let scoped = |scope: &[&str]| scope.iter().map(|s| (*s).to_owned()).collect::<Vec<_>>();
        let (_, p1) = db.found(Some(&as_bob), "idea", &none, |r| r.scope = Some(scoped(&["p1"]))).await;
        assert_eq!(p1, [format!("Note:{note_p1}")]);
        let (_, both) = db.found(Some(&as_bob), "idea", &none, |r| r.scope = Some(scoped(&["p1", "p2"]))).await;
        assert_eq!(sorted(both), sorted(vec![format!("Note:{note_p1}"), format!("Note:{note_p2}")]));
        // A relation's key is a scope too, in its wire form.
        let (_, unit) = db.found(Some(&as_bob), "walnut", &none, |r| r.scope = Some(vec![unit_a.clone()])).await;
        assert_eq!(unit, [format!("Request:{request_a}")]);
        // tables keeps to tables, and one that is not @searchable is
        // refused.
        let (_, contracts) = db.found(Some(&as_bob), "walnut", &none, |r| r.tables = Some(vec!["Contract".to_owned()])).await;
        assert_eq!(sorted(contracts), sorted(vec![format!("Contract:{contract_a}"), format!("Contract:{contract_b}")]));
        let refused = db.search(Some(&as_bob), SearchRequest { q: "walnut".to_owned(), tables: Some(vec!["ContractParty".to_owned()]), ..SearchRequest::default() }, &none).await;
        assert!(matches!(refused, Err(Error::Invalid(_))), "{refused:?}");
    }

    case("embeddings re-rank the first hits, are kept while the text holds, and can be switched off");
    {
        // Embeddings through a model provider that embeds; one that embeds
        // nothing is refused.
        let calls = Arc::new(AtomicUsize::new(0));
        let doors = SearchOptions {
            embeddings: Some(search_embeddings_of(Arc::new(Doors(calls.clone())), "stand-in-1", None).await.unwrap()),
            ..SearchOptions::default()
        };
        assert!(matches!(search_embeddings_of(Arc::new(Talker), "stand-in-1", None).await, Err(Error::Invalid(_))));
        let every_row = "INSERT INTO search_indexing_queue (source_table, source_key) SELECT source_table, source_key FROM search_index";
        db.sql(every_row).await;
        db.drain(&doors).await;
        let client = db.pool.get().await.unwrap();
        let counts = client
            .query_one("SELECT count(*)::int, (count(*) FILTER (WHERE embedding_model = 'stand-in-1'))::int FROM search_index", &[])
            .await
            .unwrap();
        assert_eq!(counts.get::<_, i32>(1), counts.get::<_, i32>(0));
        // A row whose text did not change keeps its embedding: no model
        // call.
        let before = calls.load(Ordering::SeqCst);
        db.sql(every_row).await;
        db.drain(&doors).await;
        assert_eq!(calls.load(Ordering::SeqCst), before);
        // A change of text clears its embedding, until it is embedded again.
        db.update(&system, "Contract", &contract_b, json!({ "summary": "Bolts, a walnut trim and a door." })).await;
        db.drain(&none).await;
        let stored = async || -> Option<Vec<f64>> {
            client
                .query_one("SELECT embedding FROM search_index WHERE source_table = 'Contract' AND source_key = $1", &[&hyphen(&contract_b)])
                .await
                .unwrap()
                .get(0)
        };
        assert_eq!(stored().await, None);
        db.sql(&format!("INSERT INTO search_indexing_queue (source_table, source_key) VALUES ('Contract', '{}')", hyphen(&contract_b))).await;
        db.drain(&doors).await;
        assert_eq!(stored().await, Some(vec![1.0, 0.0]));

        // The text alone ranks the body-only match low; a query about
        // doors lifts it, over the same rows and the same total.
        let (plain, plain_keys) = db.found(Some(&as_bob), "walnut", &none, |_| {}).await;
        assert!(!plain.reranked);
        let (lifted, lifted_keys) = db.found(Some(&as_bob), "walnut", &doors, |r| r.vector = Some(vec![1.0, 0.0])).await;
        assert!(lifted.reranked);
        assert_eq!(lifted.total, plain.total);
        assert_eq!(sorted(lifted_keys.clone()), sorted(plain_keys.clone()));
        let place = |keys: &[String]| keys.iter().position(|k| *k == format!("Contract:{contract_b}")).unwrap();
        assert!(place(&lifted_keys) < place(&plain_keys));
        // With embeddings on and no vector, the query is embedded: the same.
        let (_, embedded) = db.found(Some(&as_bob), "walnut", &embeddings(Arc::new(AllDoors)), |_| {}).await;
        assert_eq!(embedded, lifted_keys);
        // A model that fails leaves the text order; switched off, the text
        // order.
        let (failing, failing_keys) = db.found(Some(&as_bob), "walnut", &embeddings(Arc::new(Down)), |_| {}).await;
        assert!(!failing.reranked);
        assert_eq!(failing_keys, plain_keys);
        assert_eq!(db.keys(&as_bob, "walnut").await, plain_keys);
        // A vector with no embeddings to say its model is refused.
        let refused = db.search(Some(&as_bob), SearchRequest { q: "walnut".to_owned(), vector: Some(vec![1.0, 0.0]), ..SearchRequest::default() }, &none).await;
        assert!(matches!(refused, Err(Error::Invalid(_))), "{refused:?}");
    }

    db.drop_schema().await;
}
