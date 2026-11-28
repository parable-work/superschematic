//! fixture-search-db's search across tables in the Rust runtime on a
//! SQLite file (D155, D186, #615), run by TestRustSearchOnSQLite: six
//! @searchable tables share one index, whose words are an FTS5 table its
//! triggers keep. The build's SQLite DDL is applied to a file of the test's
//! own, and the cases are TestRustSearchEndToEnd's on Postgres, through the
//! generated repositories and orm::search_tables::sqlite. drain() runs
//! what the queue's worker runs: each ready message of the SearchIndexing
//! queue, handed to index_search in a write of the file.

use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use axum::body::Body;
use http::Request;
use http_body_util::BodyExt;
use runtime::access::sqlite::{issue_grant, revoke_grant, GrantAuthenticator};
use runtime::access::IssueGrant;
use runtime::orm::search_tables::sqlite::{call_search_tool, index_search, reindex_search, search_routes, search_tables, SearchRoutes};
use runtime::orm::search_tables::{Embedder, SearchEmbeddings, SearchIndexing, SearchOptions, SearchRequest, SearchResult};
use runtime::bucket::{Bucket, BucketError, Bytes, ListOptions, Object, Page, PutOptions, PutResult, Reader, SignedUrlOptions, Stream};
use runtime::orm::file_processing::{process_files, FileProcessingOptions, FileProcessingRun};
use runtime::orm::rowfeatures::{BeginFile, FileRef, ProcessingState, RowRef};
use runtime::providers::{
    providers_from, ConfigValue, ExtractedText, FileInput, FileProcessor, Preview as FilePreview, PreviewOptions, ProviderDefinition, ProviderInstance, Providers,
    ProvidersOptions, RuntimeInterface,
};
use runtime::orm::sqlite::{execute_rows, query_rows, rusqlite, Database};
use runtime::orm::{AccessCaller, Caller, Error, FieldKeyring, FindOptions, Values, WriteOptions};
use runtime::{Authenticator, BoxError, Principal, PrincipalKind};
use serde_json::{json, Value};
use tower::ServiceExt;
use types::orm::sqlite::Repositories;
use types::orm::sqlite_definition;

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

/// Whom the repositories act and read as: a user, whose audience they
/// keep to, or the system, which reads every row.
#[derive(Clone)]
enum Who {
    User(String),
    System,
}

/// A model that embeds every text as about a door.
struct AllDoors;

#[async_trait]
impl Embedder for AllDoors {
    async fn embed(&self, texts: &[String]) -> Result<Vec<Vec<f64>>, BoxError> {
        Ok(texts.iter().map(|_| vec![1.0, 0.0]).collect())
    }
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
    keys: FieldKeyring,
    _dir: TempDir,
}

impl File {
    fn new() -> Self {
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let dir = std::env::temp_dir().join(format!("search-rust-sqlite-{nanos}"));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("search.db");
        let connection = rusqlite::Connection::open(&path).unwrap();
        connection.execute_batch(include_str!("../create.sql")).unwrap();
        drop(connection);
        Self { database: Arc::new(Database::open(&path).unwrap()), keys: FieldKeyring::parse(KEYS).unwrap(), _dir: TempDir(dir) }
    }

    /// Runs `work` in a write of the repositories as `who`, which commits
    /// what it kept.
    fn tx<T>(&self, who: &Who, work: impl FnOnce(&Repositories<'_>) -> Result<T, Error>) -> Result<T, Error> {
        self.database.write(|connection| {
            let repos = Repositories::new(connection)?.with_keys(&self.keys);
            let repos = match who {
                Who::System => repos.as_system(),
                Who::User(user) => repos
                    .acting(Some(user.clone()))
                    .with_caller(Caller { subject: user.clone(), permissions: Vec::new(), stands_in: false, ..Default::default() })
                    .with_access(AccessCaller::of(Some(&Principal::new(user.clone(), Vec::<String>::new())))),
            };
            work(&repos)
        })
    }

    fn create(&self, who: &Who, table: &str, row: Value) -> String {
        self.tx(who, |r| {
            let row = r.untyped().table(table)?.create_one(&values(row))?;
            Ok(row["id"].as_str().unwrap().to_owned())
        })
        .unwrap()
    }

    fn update(&self, who: &Who, table: &str, key: &str, set: Value) {
        self.tx(who, |r| r.untyped().table(table)?.update_one(&json!(key), &values(set), &WriteOptions::default()).map(|_| ())).unwrap();
    }

    fn delete(&self, table: &str, key: &str) {
        self.tx(&Who::System, |r| r.untyped().table(table)?.delete_one(&json!(key), &WriteOptions::default())).unwrap();
    }

    /// What the queue's worker runs: every ready message, indexed.
    fn drain(&self, options: &SearchOptions) -> usize {
        let definition = sqlite_definition().unwrap();
        let mut n = 0;
        loop {
            let indexed = self
                .database
                .write(|connection| {
                    let rows = query_rows(
                        connection,
                        "DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key",
                        &[],
                    )?;
                    for row in &rows {
                        let message = SearchIndexing { source_table: row[0].as_str().unwrap().to_owned(), source_key: row[1].as_str().unwrap().to_owned() };
                        index_search(connection, definition, &message, options)?;
                    }
                    Ok::<_, Error>(rows.len())
                })
                .unwrap();
            if indexed == 0 {
                return n;
            }
            n += indexed;
        }
    }

    fn sql(&self, statement: &str) -> u64 {
        self.database.write(|connection| execute_rows(connection, statement, &[])).unwrap()
    }

    fn count(&self, statement: &str) -> i64 {
        self.database.read(|connection| query_rows(connection, statement, &[])).unwrap()[0][0].as_i64().unwrap()
    }

    fn search(&self, principal: Option<&Principal>, request: SearchRequest, options: &SearchOptions) -> Result<SearchResult, Error> {
        self.database.read(|connection| search_tables(Repositories::new(connection)?.untyped(), principal, &request, options))
    }

    fn found(&self, principal: Option<&Principal>, q: &str, options: &SearchOptions, edit: impl FnOnce(&mut SearchRequest)) -> (SearchResult, Vec<String>) {
        let mut request = SearchRequest { q: q.to_owned(), limit: Some(100), ..SearchRequest::default() };
        edit(&mut request);
        let result = self.search(principal, request, options).unwrap();
        let keys = result.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        (result, keys)
    }

    fn keys(&self, principal: &Principal, q: &str) -> Vec<String> {
        self.found(Some(principal), q, &SearchOptions::default(), |_| {}).1
    }

    /// The rows of every @searchable table `principal`'s list reads show
    /// whose title or body holds `word`, as table:key, sorted.
    fn listed(&self, principal: &Principal, word: &str) -> Vec<String> {
        let mut out = Vec::new();
        self.database
            .read(|connection| {
                let repos = Repositories::new(connection)?.with_keys(&self.keys).with_access(AccessCaller::of(Some(principal)));
                for (table, fields) in INDEXED {
                    let options = FindOptions { limit: Some(1000), skip_count: true, ..FindOptions::default() };
                    for row in repos.untyped().table(table)?.find_many(None, &options)?.rows {
                        if fields.iter().any(|field| words_of(row.get(*field)).iter().any(|w| w == word)) {
                            out.push(format!("{table}:{}", row["id"].as_str().unwrap()));
                        }
                    }
                }
                Ok::<_, Error>(())
            })
            .unwrap();
        out.sort();
        out
    }
}

/// Runs `future` to its end on this thread: a pass, a begin or a
/// complete inside a write of the file, whose connection a task does not
/// hold across threads.
fn block_on<F: std::future::Future>(future: F) -> F::Output {
    tokio::task::block_in_place(|| tokio::runtime::Handle::current().block_on(future))
}

fn sorted(mut keys: Vec<String>) -> Vec<String> {
    keys.sort();
    keys
}

#[tokio::test(flavor = "multi_thread", worker_threads = 2)]
async fn search_across_tables_on_sqlite() {
    let db = File::new();
    let definition = sqlite_definition().unwrap();
    let alice_id = uuid(0xa1);
    let bob_id = uuid(0xb0);
    let party = "3cKSm7Pv2EM8Wl4z4XvMh1";
    let alice = Who::User(alice_id.clone());
    let system = Who::System;
    let (as_alice, as_bob) = (user(&alice_id), user(&bob_id));
    let none = SearchOptions::default();

    // Two folders: alice's own, and one everyone sees; a document in each.
    let private_folder = db.create(&alice, "Folder", json!({ "name": "Walnut private", "owner": alice_id, "audience": "private" }));
    let shared_folder = db.create(&alice, "Folder", json!({ "name": "Walnut shared", "owner": alice_id }));
    let private_doc = db.create(&alice, "Document", json!({ "folderId": private_folder, "title": "Walnut plan", "text": "The plan in brief." }));
    let shared_doc = db.create(&alice, "Document", json!({ "folderId": shared_folder, "title": "Memo", "text": "A walnut desk for the hall." }));
    // Two contracts, the first with a unit and a request about it.
    let contract_a = db.create(&system, "Contract", json!({ "name": "Walnut supply", "summary": "Timber for the year.", "terms": "Pay in obsidian." }));
    let contract_b = db.create(&system, "Contract", json!({ "name": "Bolts", "summary": "Bolts and a walnut trim." }));
    let unit_a = db.create(&system, "Unit", json!({ "name": "Walnut unit", "contractId": contract_a }));
    let unit_b = db.create(&system, "Unit", json!({ "name": "Annex", "notes": "Walnut floors.", "contractId": contract_b }));
    let request_a = db.create(&system, "Request", json!({ "unitId": unit_a, "title": "Walnut leak", "details": "Water under the sink." }));
    let request_b = db.create(&system, "Request", json!({ "unitId": unit_b, "title": "Hinge", "details": "A walnut door sticks." }));
    // Notes in two projects, one deleted.
    let note_p1 = db.create(&system, "Note", json!({ "project": "p1", "title": "Walnut idea", "text": "Oil it twice." }));
    let note_p2 = db.create(&system, "Note", json!({ "project": "p2", "title": "Pine idea", "text": "A walnut stain on pine." }));
    let note_gone = db.create(&system, "Note", json!({ "project": "p1", "title": "Walnut draft", "text": "Gone soon." }));
    db.delete("Note", &note_gone);
    // A role a party's grant names: the units and requests of its contract.
    db.create(&system, "Role", json!({ "name": "contract-party", "permissions": [], "reaches": ["Unit:contract", "Request:unit.contract"] }));
    assert!(db.drain(&none) > 0);

    case("two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables");
    {
        for principal in [&as_alice, &as_bob] {
            let (result, keys) = db.found(Some(principal), "walnut", &none, |_| {});
            assert_eq!(sorted(keys.clone()), db.listed(principal, "walnut"));
            assert_eq!(result.total, keys.len() as u64);
            let tables: std::collections::BTreeSet<&str> = result.items.iter().map(|hit| hit.table.as_str()).collect();
            assert!(tables.len() >= 5, "{tables:?}");
            // A page of three at a time reads the same rows, in the same
            // order, with the same total on every page.
            let mut paged = Vec::new();
            let mut offset = 0;
            loop {
                let page = db.search(Some(principal), SearchRequest { q: "Walnut".to_owned(), limit: Some(3), offset: Some(offset), ..SearchRequest::default() }, &none).unwrap();
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
        let alices = db.keys(&as_alice, "walnut");
        let bobs = db.keys(&as_bob, "walnut");
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
        let (result, _) = db.found(Some(&as_bob), "walnut", &none, |_| {});
        let hit = result.items.iter().find(|h| h.key == contract_b).unwrap();
        assert_eq!(hit.matched, "body");
        let matched: Vec<String> = hit.snippet.iter().filter(|p| p.matched).map(|p| p.text.to_lowercase()).collect();
        assert_eq!(matched, ["walnut"]);
        let title = result.items.iter().find(|h| h.key == contract_a).unwrap();
        assert_eq!((title.matched.as_str(), title.title.as_str()), ("title", "Walnut supply"));
        // Two words: a row holds both, in any case.
        assert_eq!(db.keys(&as_bob, "walnut SINK"), [format!("Request:{request_a}")]);
        // Words with no letter or digit match nothing; a quote in a word
        // is no FTS5 syntax.
        assert_eq!(db.found(Some(&as_bob), "-- ...", &none, |_| {}).0.total, 0);
        assert_eq!(db.found(Some(&as_bob), "\"walnut", &none, |_| {}).0.total, db.found(Some(&as_bob), "walnut", &none, |_| {}).0.total);
        // An anonymous caller sees what everyone may.
        let (_, anonymous) = db.found(None, "walnut", &none, |_| {});
        assert_eq!(sorted(anonymous), sorted(bobs));
    }

    case("a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing");
    {
        let issued = db
            .tx(&system, |r| {
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
            })
            .unwrap();
        let tables = types::orm::sqlite::tables(Arc::clone(&db.database));
        let authenticate: Arc<dyn Authenticator> = Arc::new(GrantAuthenticator::new(tables.clone()).unwrap());
        let parts = |token: &str| Request::get("/").header("authorization", format!("Bearer {token}")).body(()).unwrap().into_parts().0;
        let principal = authenticate.authenticate(&parts(&issued.token)).await.unwrap().unwrap();
        assert_eq!(principal.kind, PrincipalKind::Party);
        let (result, keys) = db.found(Some(&principal), "walnut", &none, |_| {});
        assert_eq!(sorted(keys.clone()), db.listed(&principal, "walnut"));
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
        let response = route.clone().oneshot(get("/api/search?q=walnut&limit=many", Some(&issued.token))).await.unwrap();
        assert_eq!(response.status(), 400);
        // The MCP tool, as the same principal.
        let args = json!({ "q": "walnut", "tables": ["Unit", "Request", "Folder"] });
        let called = db.database.read(|connection| call_search_tool(Repositories::new(connection)?.untyped(), Some(&principal), args.as_object().unwrap(), &none)).unwrap();
        let called: Vec<String> = called.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        assert_eq!(sorted(called), sorted(vec![format!("Request:{request_a}"), format!("Unit:{unit_a}")]));
        // An agent run's grant: its search keeps to what the grant reaches,
        // its principal built as the dispatcher builds it from the grant.
        let run = db
            .tx(&system, |r| {
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
            })
            .unwrap();
        assert!(authenticate.authenticate(&parts(&run.token)).await.is_err());
        let agent = Principal::new(uuid(0xa9), Vec::<String>::new())
            .of_kind(PrincipalKind::Agent)
            .with_grants(vec![runtime::GrantReach {
                id: run.grant["id"].as_str().unwrap().to_owned(),
                target_type: "Contract".to_owned(),
                target_id: contract_a.clone(),
                reaches: runtime::access::reaches_of(["Unit:contract", "Request:unit.contract"]),
            }]);
        let args = json!({ "q": "walnut" });
        let called = db.database.read(|connection| call_search_tool(Repositories::new(connection)?.untyped(), Some(&agent), args.as_object().unwrap(), &none)).unwrap();
        let called: Vec<String> = called.items.iter().map(|hit| format!("{}:{}", hit.table, hit.key)).collect();
        assert_eq!(sorted(called.clone()), sorted(keys.clone()));
        assert_eq!(sorted(called), db.listed(&agent, "walnut"));
        // Revoked, the very next call is refused.
        let id = issued.grant["id"].as_str().unwrap().to_owned();
        db.database.write(|connection| revoke_grant(connection, definition, &id, SystemTime::now())).unwrap();
        assert!(authenticate.authenticate(&parts(&issued.token)).await.is_err());
        let refused = route.clone().oneshot(get("/api/search?q=walnut", Some(&issued.token))).await.unwrap();
        assert_eq!(refused.status(), 401);
        assert_eq!(route.clone().oneshot(get("/api/search?q=walnut", None)).await.unwrap().status(), 401);
        let posted = Request::post("/api/search?q=walnut").header("authorization", format!("Bearer {}", run.token)).body(Body::empty()).unwrap();
        assert_eq!(route.oneshot(posted).await.unwrap().status(), 405);
    }

    case("a row's conversation and files are indexed with it, and an encrypted field never is");
    {
        db.tx(&alice, |r| r.untyped().table("Unit")?.operation("comments", "post", &json!(unit_b), &json!({ "body": "The sycamore by the gate needs a trim." }), &WriteOptions::default())).unwrap();
        db.sql(&format!(
            "INSERT INTO unit_attachments (id, unit_id, name, media_type, size, content_hash, object_key, state, extraction, extracted_text) VALUES ('{}', '{}', 'survey.pdf', 'application/pdf', 10, '{}', 'attachments/survey', 'ready', 'done', 'Juniper hedges line the drive.')",
            hyphen(&uuid(0xf1)),
            hyphen(&unit_b),
            "0".repeat(64)
        ));
        assert_eq!(db.drain(&none), 1);
        let (sycamore, _) = db.found(Some(&as_bob), "sycamore", &none, |_| {});
        assert_eq!(sycamore.items.iter().map(|h| format!("{}:{}", h.table, h.key)).collect::<Vec<_>>(), [format!("Unit:{unit_b}")]);
        assert_eq!(sycamore.items[0].matched, "comments");
        let matched: Vec<String> = sycamore.items[0].snippet.iter().filter(|p| p.matched).map(|p| p.text.to_lowercase()).collect();
        assert_eq!(matched, ["sycamore"]);
        let (juniper, _) = db.found(Some(&as_bob), "juniper", &none, |_| {});
        assert_eq!(juniper.items.iter().map(|h| format!("{}:{}", h.table, h.key)).collect::<Vec<_>>(), [format!("Unit:{unit_b}")]);
        assert_eq!(juniper.items[0].matched, "files");
        // The contract's terms are sealed at rest and never reach the index.
        assert_eq!(db.found(Some(&as_alice), "obsidian", &none, |_| {}).0.total, 0);
        assert_eq!(db.count("SELECT count(*) FROM search_index WHERE (title || ' ' || coalesce(body, '') || ' ' || coalesce(comments, '') || ' ' || coalesce(files, '')) LIKE '%obsidian%'"), 0);
    }

    case("an uploaded file's text the file processor extracts is indexed with its row, and a file the processor fails on ends failed");
    {
        let asked = Arc::new(AtomicUsize::new(0));
        let providers = file_processors(&asked);
        let bucket = MemoryBucket::default();
        // A browser's upload: begin, the PUT its URL takes, and complete,
        // which asks the file's text. A member that reaches the bucket
        // runs the file's statements when first polled, in the write.
        let upload = async |name: &str, media_type: &str, text: &str| {
            let begin = BeginFile { to: RowRef::new("Unit", &unit_a), name: name.to_owned(), media_type: media_type.to_owned(), size: text.len() as u64, sha256: sha256_hex(text.as_bytes()), caption: None };
            let (file, _) = db.tx(&system, |r| block_on(r.attachments()?.begin(&begin, &bucket, None))).unwrap();
            bucket.put(&file.object_key, runtime::bucket::Body::full(text.as_bytes().to_vec()), PutOptions::content_type(media_type)).await.unwrap();
            db.tx(&system, |r| block_on(r.attachments()?.complete(&FileRef::new("Unit", &file.id), &bucket))).unwrap()
        };
        let plan = upload("plan.txt", "text/plain", "Hawthorn rows along the north wall.").await;
        let survey = upload("survey.pdf", "application/pdf", "Rowan saplings by the shed.").await;
        assert_eq!(plan.extraction, ProcessingState::Pending);
        db.drain(&none);
        let pass = FileProcessingOptions::new(&providers, &bucket);
        let run = db.tx(&system, |r| block_on(process_files(&r.attachments()?, &pass))).unwrap();
        assert_eq!(run, FileProcessingRun { extracted: 1, previewed: 0, failed: 1 });
        let got = db.tx(&system, |r| r.attachments()?.get(&FileRef::new("Unit", &plan.id))).unwrap();
        assert_eq!((got.extraction, got.extracted_text.as_deref()), (ProcessingState::Done, Some("Hawthorn rows along the north wall.")));
        let got = db.tx(&system, |r| r.attachments()?.get(&FileRef::new("Unit", &survey.id))).unwrap();
        assert_eq!((got.extraction, got.extracted_text), (ProcessingState::Failed, None));
        // The write of the text enqueues the row, whose index row then
        // holds it: a word only in the file finds the unit.
        assert_eq!(db.drain(&none), 1);
        let (hawthorn, keys) = db.found(Some(&as_alice), "hawthorn", &none, |_| {});
        assert_eq!(keys, [format!("Unit:{unit_a}")]);
        assert_eq!(hawthorn.items[0].matched, "files");
        assert_eq!(db.found(Some(&as_alice), "rowan", &none, |_| {}).0.total, 0);
        // Nothing is pending: the next pass asks no processor anything.
        let before = asked.load(Ordering::SeqCst);
        let next = db.tx(&system, |r| block_on(process_files(&r.attachments()?, &pass))).unwrap();
        assert_eq!(next, FileProcessingRun::default());
        assert_eq!(asked.load(Ordering::SeqCst), before);
    }

    case("an index row follows its row: an audience change and a delete at once, a change of text once the queue drains");
    {
        assert!(db.keys(&as_bob, "walnut").contains(&format!("Folder:{shared_folder}")));
        // Alice keeps the shared folder to herself: its documents inherit
        // it. Neither shows to bob, with no drain, since the trigger copied
        // it.
        db.update(&alice, "Folder", &shared_folder, json!({ "audience": "private" }));
        let bobs = db.keys(&as_bob, "walnut");
        assert!(!bobs.contains(&format!("Folder:{shared_folder}")));
        assert!(!bobs.contains(&format!("Document:{shared_doc}")));
        assert_eq!(sorted(bobs), db.listed(&as_bob, "walnut"));
        db.update(&alice, "Folder", &shared_folder, json!({ "audience": "everyone" }));
        assert!(db.keys(&as_bob, "walnut").contains(&format!("Document:{shared_doc}")));
        // A delete takes the index row with it, at once.
        db.delete("Request", &request_b);
        assert!(!db.keys(&as_bob, "walnut").contains(&format!("Request:{request_b}")));
        // A change of text waits for the queue: found by its old words
        // until the worker indexes it again.
        db.update(&system, "Note", &note_p1, json!({ "title": "Chestnut idea" }));
        assert_eq!(db.found(Some(&as_bob), "chestnut", &none, |_| {}).0.total, 0);
        assert_eq!(db.drain(&none), 1);
        assert_eq!(db.keys(&as_bob, "chestnut"), [format!("Note:{note_p1}")]);
        // An update that changes no indexed field enqueues nothing.
        db.update(&system, "Unit", &unit_a, json!({ "contractId": contract_a }));
        assert_eq!(db.drain(&none), 0);
        // A table's index rows lost are built again by reindex_search, every
        // row of it, the soft-deleted one removed again.
        db.sql("DELETE FROM search_index WHERE source_table = 'Note'");
        assert_eq!(db.found(Some(&as_bob), "idea", &none, |_| {}).0.total, 0);
        let tables = ["Note".to_owned()];
        assert_eq!(db.database.write(|connection| reindex_search(connection, definition, Some(&tables), Some(2), &none)).unwrap(), 3);
        assert_eq!(sorted(db.keys(&as_bob, "idea")), sorted(vec![format!("Note:{note_p1}"), format!("Note:{note_p2}")]));
        assert_eq!(db.count("SELECT count(*) FROM search_index WHERE source_table = 'Note'"), 2);
    }

    case("scope keeps a search to the app's filter");
    {
        let scoped = |scope: &[&str]| scope.iter().map(|s| (*s).to_owned()).collect::<Vec<_>>();
        let (_, p1) = db.found(Some(&as_bob), "idea", &none, |r| r.scope = Some(scoped(&["p1"])));
        assert_eq!(p1, [format!("Note:{note_p1}")]);
        let (_, both) = db.found(Some(&as_bob), "idea", &none, |r| r.scope = Some(scoped(&["p1", "p2"])));
        assert_eq!(sorted(both), sorted(vec![format!("Note:{note_p1}"), format!("Note:{note_p2}")]));
        // A relation's key is a scope too, in its wire form.
        let (_, unit) = db.found(Some(&as_bob), "walnut", &none, |r| r.scope = Some(vec![unit_a.clone()]));
        assert_eq!(unit, [format!("Request:{request_a}")]);
        // tables keeps to tables, and one that is not @searchable is
        // refused.
        let (_, contracts) = db.found(Some(&as_bob), "walnut", &none, |r| r.tables = Some(vec!["Contract".to_owned()]));
        assert_eq!(sorted(contracts), sorted(vec![format!("Contract:{contract_a}"), format!("Contract:{contract_b}")]));
        let refused = db.search(Some(&as_bob), SearchRequest { q: "walnut".to_owned(), tables: Some(vec!["ContractParty".to_owned()]), ..SearchRequest::default() }, &none);
        assert!(matches!(refused, Err(Error::Invalid(_))), "{refused:?}");
    }

    case("embeddings are refused on SQLite, which ranks by the words");
    {
        let embeddings = SearchOptions { embeddings: Some(SearchEmbeddings { model: "stand-in-1".to_owned(), embedder: Arc::new(AllDoors), max_chars: None }), ..SearchOptions::default() };
        let vector = db.search(Some(&as_bob), SearchRequest { q: "walnut".to_owned(), vector: Some(vec![1.0, 0.0]), ..SearchRequest::default() }, &none);
        assert!(matches!(&vector, Err(Error::Invalid(message)) if message.contains("Postgres alone")), "{vector:?}");
        let options = db.search(Some(&as_bob), SearchRequest { q: "walnut".to_owned(), ..SearchRequest::default() }, &embeddings);
        assert!(matches!(options, Err(Error::Definition(_))), "{options:?}");
        let message = SearchIndexing { source_table: "Note".to_owned(), source_key: hyphen(&note_p2) };
        let indexed = db.database.write(|connection| index_search(connection, definition, &message, &embeddings));
        assert!(matches!(indexed, Err(Error::Definition(_))), "{indexed:?}");
        let (plain, _) = db.found(Some(&as_bob), "walnut", &none, |_| {});
        assert!(!plain.reranked && plain.total > 0);
        assert_eq!(db.count("SELECT count(*) FROM search_index WHERE embedding IS NOT NULL"), 0);
    }
}

/// A bucket in memory: each object's bytes and media type, by name.
#[derive(Default)]
struct MemoryBucket(std::sync::Mutex<Objects>);

/// Each object's bytes and media type, by name.
type Objects = std::collections::HashMap<String, (Vec<u8>, Option<String>)>;

/// One chunk, then the end.
struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BucketError>;
    fn poll_next(mut self: std::pin::Pin<&mut Self>, _: &mut std::task::Context<'_>) -> std::task::Poll<Option<Self::Item>> {
        std::task::Poll::Ready(self.0.take().map(Ok))
    }
}

#[async_trait]
impl Bucket for MemoryBucket {
    fn name(&self) -> &str {
        "unit-files"
    }

    async fn put(&self, name: &str, body: runtime::bucket::Body, options: PutOptions) -> Result<PutResult, BucketError> {
        let bytes = body.as_bytes().cloned().unwrap_or_default().to_vec();
        let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type: options.content_type.clone(), updated: None };
        self.0.lock().unwrap().insert(name.to_owned(), (bytes, options.content_type));
        Ok(PutResult { object, created: true })
    }

    async fn head(&self, name: &str) -> Result<Option<Object>, BucketError> {
        Ok(self.0.lock().unwrap().get(name).map(|(bytes, content_type)| Object { name: name.to_owned(), size: bytes.len() as u64, content_type: content_type.clone(), updated: None }))
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        let Some((bytes, content_type)) = self.0.lock().unwrap().get(name).cloned() else {
            return Err(BucketError::NotFound { bucket: "unit-files".into(), object: name.into() });
        };
        let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type, updated: None };
        Ok(Reader::new(object, Once(Some(Bytes::from(bytes)))))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        self.0.lock().unwrap().remove(name);
        Ok(())
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        Ok(Page::default())
    }

    async fn signed_url(&self, name: &str, _options: SignedUrlOptions) -> Result<String, BucketError> {
        Ok(format!("memory://unit-files/{name}"))
    }
}

/// The SHA-256 of `bytes`, 64 lowercase hex digits.
fn sha256_hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// A stub file processor of `types` (D159): a file's text is its bytes,
/// or with `fails` every call fails. It counts the calls into it.
struct StubProcessor {
    types: Vec<String>,
    fails: bool,
    asked: Arc<AtomicUsize>,
}

#[async_trait]
impl FileProcessor for StubProcessor {
    fn media_types(&self) -> Vec<String> {
        self.types.clone()
    }

    async fn extract_text(&self, file: &FileInput) -> Result<Option<ExtractedText>, BoxError> {
        self.asked.fetch_add(1, Ordering::SeqCst);
        if self.fails {
            return Err("the stub cannot read this file".into());
        }
        Ok(Some(ExtractedText { text: String::from_utf8_lossy(&file.bytes).into_owned(), ..ExtractedText::default() }))
    }

    async fn preview(&self, _file: &FileInput, _options: &PreviewOptions) -> Result<Option<FilePreview>, BoxError> {
        self.asked.fetch_add(1, Ordering::SeqCst);
        if self.fails {
            return Err("the stub cannot read this file".into());
        }
        Ok(None)
    }
}

/// The providers of two files slots: FILES a stub of plain text, and
/// MORE_FILES one of PDFs that fails.
fn file_processors(asked: &Arc<AtomicUsize>) -> Providers {
    let mut options = ProvidersOptions::default();
    for (slot, name, types, fails) in [("FILES", "plain-text", "text/plain", false), ("MORE_FILES", "broken-pdf", "application/pdf", true)] {
        options.slots.insert(slot.to_owned(), "files".to_owned());
        options.config.insert(slot.to_owned(), ConfigValue::Text(name.to_owned()));
        let asked = Arc::clone(asked);
        options.definitions.push(ProviderDefinition::new(name, RuntimeInterface::Files, move |_, _| {
            Ok(ProviderInstance::Files(Arc::new(StubProcessor { types: vec![types.to_owned()], fails, asked: Arc::clone(&asked) })))
        }));
    }
    providers_from(options).unwrap()
}
