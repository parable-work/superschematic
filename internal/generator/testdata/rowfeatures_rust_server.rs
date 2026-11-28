//! fixture-references-api's references and files end to end through the
//! generated Rust server (#723, D154), run by
//! TestRustRowFeaturesServerEndToEnd: the generated router over a pool of
//! a schema of the test's own at ROW_FEATURES_TEST_DATABASE_URL, or with
//! the sqlite feature over a SQLite file of its own, with
//! fixture-references-db's create.sql applied, behind a test
//! authenticator, with a bucket in memory whose uploads arrive as a
//! browser sends them: a PUT to the URL the route signed, checked against
//! the URL's signature and expiry before its bytes are stored. Each call
//! is a request to the router (runtime::handle); the rows each case starts
//! from are written as the system through the database's repositories.
//! The cases are the Go server's (rowfeatures_go_server.go).

use std::collections::{BTreeMap, HashMap};
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::{SystemTime, UNIX_EPOCH};

use api::runtime::{handle, ApiError, Authenticator, Principal, RequestContext};
use api::{build_router, Handlers, Implementations, TicketGetTicketArgs, TicketImplementation};
use async_trait::async_trait;
use axum::Router;
use bytes::Bytes;
use http::{Method, Request, StatusCode};
use runtime::bucket::{
    Body, Bucket, BucketError, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions, Stream,
};
use runtime::orm::rowfeatures::RowRef;
use runtime::orm::Values;
use serde_json::{json, Value};
use store::Store;

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

/// The UUID a key names, in either of its forms, as a number.
fn uuid_value(key: &str) -> Option<u128> {
    let hex: String = key.chars().filter(|c| *c != '-').collect();
    if key.contains('-') && hex.len() == 32 {
        return u128::from_str_radix(&hex, 16).ok();
    }
    let mut n = 0u128;
    for c in key.bytes() {
        n = n.checked_mul(62)?.checked_add(BASE62.iter().position(|b| *b == c)? as u128)?;
    }
    Some(n)
}

/// Whether two keys name one row: equal, or one UUID in either form.
fn same_key(a: &str, b: &str) -> bool {
    a == b || matches!((uuid_value(a), uuid_value(b)), (Some(x), Some(y)) if x == y)
}

fn hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes)
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

const BUCKET_KEY: &[u8] = b"reference-files-signing-key";

fn sign(method: &str, name: &str, expires: u64, content_type: &str) -> String {
    let key = ring::hmac::Key::new(ring::hmac::HMAC_SHA256, BUCKET_KEY);
    let tag = ring::hmac::sign(&key, format!("{method}\n{name}\n{expires}\n{content_type}").as_bytes());
    tag.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// Each object's bytes and content type, by name.
type Objects = HashMap<String, (Vec<u8>, Option<String>)>;

/// A bucket in memory that signs its URLs with an HMAC of the method, the
/// object, the expiry and the content type, and stores a PUT through one
/// only once its signature holds, as a provider checks a presigned PUT.
#[derive(Default)]
struct MemoryBucket {
    objects: Mutex<Objects>,
}

impl MemoryBucket {
    /// A browser's PUT of `bytes` to a signed URL: 403 for a signature
    /// that does not hold, or one past its expiry.
    fn put_signed(&self, url: &str, headers: &BTreeMap<String, String>, bytes: &[u8]) -> u16 {
        let Some(rest) = url.strip_prefix("memory://reference-files/") else {
            return 404;
        };
        let (name, query) = rest.split_once('?').unwrap();
        let params: HashMap<&str, &str> = query.split('&').filter_map(|p| p.split_once('=')).collect();
        let expires: u64 = params["expires"].parse().unwrap();
        let content_type = headers.get("content-type").map_or("", String::as_str);
        if params["method"] != "PUT" || sign("PUT", name, expires, content_type) != params["sig"] {
            return 403;
        }
        if SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() > expires {
            return 403;
        }
        self.objects
            .lock()
            .unwrap()
            .insert(name.to_owned(), (bytes.to_vec(), Some(content_type.to_owned())));
        200
    }

    fn len(&self) -> usize {
        self.objects.lock().unwrap().len()
    }
}

/// One chunk, then the end.
struct Once(Option<Bytes>);

impl Stream for Once {
    type Item = Result<Bytes, BucketError>;
    fn poll_next(mut self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        Poll::Ready(self.0.take().map(Ok))
    }
}

#[async_trait]
impl Bucket for MemoryBucket {
    fn name(&self) -> &str {
        "reference-files"
    }

    async fn put(&self, name: &str, body: Body, options: PutOptions) -> Result<PutResult, BucketError> {
        let mut objects = self.objects.lock().unwrap();
        let bytes = body.as_bytes().cloned().unwrap_or_default();
        let size = bytes.len() as u64;
        objects.insert(name.to_owned(), (bytes.to_vec(), options.content_type.clone()));
        let object = Object { name: name.to_owned(), size, content_type: options.content_type, updated: None };
        Ok(PutResult { object, created: true })
    }

    async fn head(&self, name: &str) -> Result<Option<Object>, BucketError> {
        Ok(self.objects.lock().unwrap().get(name).map(|(bytes, content_type)| Object {
            name: name.to_owned(),
            size: bytes.len() as u64,
            content_type: content_type.clone(),
            updated: None,
        }))
    }

    async fn get(&self, name: &str) -> Result<Reader, BucketError> {
        let Some((bytes, content_type)) = self.objects.lock().unwrap().get(name).cloned() else {
            return Err(BucketError::NotFound { bucket: "reference-files".into(), object: name.into() });
        };
        let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type, updated: None };
        Ok(Reader::new(object, Once(Some(Bytes::from(bytes)))))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        match self.objects.lock().unwrap().remove(name) {
            Some(_) => Ok(()),
            None => Err(BucketError::NotFound { bucket: "reference-files".into(), object: name.into() }),
        }
    }

    async fn list(&self, _options: ListOptions) -> Result<Page, BucketError> {
        Ok(Page::default())
    }

    async fn signed_url(&self, name: &str, options: SignedUrlOptions) -> Result<String, BucketError> {
        let expires = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() + options.expires.as_secs();
        let method = options.method.as_str();
        let content_type = options.content_type.unwrap_or_default();
        Ok(format!(
            "memory://reference-files/{name}?method={method}&expires={expires}&sig={}",
            sign(method, name, expires, &content_type)
        ))
    }
}

/// The test's authenticator: Authorization: Bearer
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

/// The project's implementation of getTicket, the one operation no
/// generated handler serves, which the cases do not call.
struct Tickets;

#[async_trait]
impl TicketImplementation for Tickets {
    async fn get_ticket(&self, _ctx: RequestContext, _args: TicketGetTicketArgs) -> Result<api::types::Ticket, ApiError> {
        Err(ApiError::not_implemented("the cases read no ticket"))
    }
}

/// The database the router serves the operations from, and what writes
/// the rows each case starts from, as the system.
#[cfg(not(feature = "sqlite"))]
mod store {
    use std::time::{SystemTime, UNIX_EPOCH};

    use runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
    use runtime::orm::rowfeatures::RowRef;
    use serde_json::Value;
    use types::orm::postgres::Repositories;

    /// A schema of its own holding fixture-references-db's tables, and the
    /// pool whose connections search it.
    pub struct Store {
        pub db: deadpool_postgres::Pool,
        admin: tokio_postgres::Client,
        schema: String,
    }

    async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
        let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
        tokio::spawn(connection);
        client
    }

    impl Store {
        pub async fn open() -> Self {
            let url = std::env::var("ROW_FEATURES_TEST_DATABASE_URL").expect("ROW_FEATURES_TEST_DATABASE_URL names the database");
            let config: tokio_postgres::Config = url.parse().unwrap();
            let admin = connect(&config).await;
            let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
            let schema = format!("row_features_rust_{nanos}");
            admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
            let mut scoped = config;
            scoped.options(format!("-c search_path={schema},public"));
            let client = connect(&scoped).await;
            client.batch_execute(include_str!("../create.sql")).await.unwrap();
            let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
            let db = deadpool_postgres::Pool::builder(manager).max_size(8).build().unwrap();
            Self { db, admin, schema }
        }

        /// A row of `table` with `row`'s members, in a transaction, as a
        /// table that composes behaviors is written.
        pub async fn row(&self, table: &str, row: &runtime::orm::Values) -> Value {
            let mut client = self.db.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            let created = {
                let repos = Repositories::new(&tx).unwrap().as_system();
                repos.untyped().table(table).unwrap().create_one(row).await.unwrap()
            };
            tx.commit().await.unwrap();
            created["id"].clone()
        }

        /// `from` points at `to` with `role`.
        pub async fn reference(&self, from: &RowRef, to: &RowRef, role: &str) {
            let mut client = self.db.get().await.unwrap();
            let tx = client.transaction().await.unwrap();
            {
                let repos = Repositories::new(&tx).unwrap().as_system();
                repos.references().unwrap().add(from, to, role).await.unwrap();
            }
            tx.commit().await.unwrap();
        }

        pub async fn close(self) {
            let Store { db, admin, schema } = self;
            db.close();
            admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
        }
    }
}

/// The database the router serves the operations from, and what writes
/// the rows each case starts from, as the system.
#[cfg(feature = "sqlite")]
mod store {
    use std::sync::Arc;
    use std::time::{SystemTime, UNIX_EPOCH};

    use runtime::orm::rowfeatures::RowRef;
    use runtime::orm::sqlite::{rusqlite, Database};
    use serde_json::Value;
    use types::orm::sqlite::Repositories;

    /// A SQLite file of its own holding fixture-references-db's tables,
    /// removed with its directory when the cases end.
    pub struct Store {
        pub db: Arc<Database>,
        dir: std::path::PathBuf,
    }

    impl Store {
        pub async fn open() -> Self {
            let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
            let dir = std::env::temp_dir().join(format!("row-features-rust-sqlite-{nanos}"));
            std::fs::create_dir_all(&dir).unwrap();
            let path = dir.join("references.db");
            let connection = rusqlite::Connection::open(&path).unwrap();
            connection.execute_batch(include_str!("../create.sql")).unwrap();
            drop(connection);
            Self { db: Arc::new(Database::open(&path).unwrap()), dir }
        }

        /// A row of `table` with `row`'s members, in a write transaction.
        pub async fn row(&self, table: &str, row: &runtime::orm::Values) -> Value {
            let created = self
                .db
                .write(|connection| Repositories::new(connection)?.as_system().untyped().table(table)?.create_one(row))
                .unwrap();
            created["id"].clone()
        }

        /// `from` points at `to` with `role`.
        pub async fn reference(&self, from: &RowRef, to: &RowRef, role: &str) {
            self.db
                .write(|connection| Repositories::new(connection)?.as_system().references()?.add(from, to, role))
                .unwrap();
        }

        pub async fn close(self) {
            let Store { db, dir } = self;
            drop(db);
            let _ = std::fs::remove_dir_all(dir);
        }
    }
}

/// The store, the bucket and the router over both.
struct Server {
    store: Store,
    bucket: Arc<MemoryBucket>,
    router: Router,
}

async fn server() -> Server {
    let store = Store::open().await;
    let bucket = Arc::new(MemoryBucket::default());
    let handlers = Handlers { ticket: Arc::new(Tickets), authenticator: Arc::new(Bearer) };
    let router = build_router(Implementations::new(handlers, store.db.clone(), bucket.clone()));
    Server { store, bucket, router }
}

fn values(value: Value) -> Values {
    value.as_object().unwrap().clone()
}

impl Server {
    /// A request to the router as `token`'s caller, and its answer: the
    /// status, and the envelope's data or the problem.
    async fn call(&self, token: &str, method: Method, path: &str, body: Option<Value>) -> (StatusCode, Value) {
        let mut request = Request::builder().method(method).uri(path).header("accept", "application/json, text/event-stream");
        if !token.is_empty() {
            request = request.header("authorization", format!("Bearer {token}"));
        }
        if body.is_some() {
            request = request.header("content-type", "application/json");
        }
        let body = body.map_or_else(Bytes::new, |body| Bytes::from(body.to_string()));
        let response = handle(&self.router, request.body(body).unwrap()).await;
        let status = response.status();
        let text = String::from_utf8(response.body().to_vec()).unwrap();
        let text = text.strip_prefix("event: message\ndata: ").map_or(text.as_str(), str::trim_end);
        let answered: Value = serde_json::from_str(text).unwrap_or(Value::String(text.to_owned()));
        if status.is_success() && answered.get("data").is_some() {
            return (status, answered["data"].clone());
        }
        (status, answered)
    }

    async fn ok(&self, token: &str, method: Method, path: &str, body: Option<Value>) -> Value {
        let (status, answered) = self.call(token, method.clone(), path, body).await;
        assert_eq!(status, StatusCode::OK, "{method} {path}: {answered}");
        answered
    }

    /// A row of `table` with `row`'s members, written as the system.
    async fn row(&self, table: &str, row: Value) -> String {
        self.store.row(table, &values(row)).await.as_str().unwrap().to_owned()
    }

    async fn ticket(&self, title: &str) -> String {
        self.row("Ticket", json!({ "title": title })).await
    }

    async fn note(&self, title: &str, owner: &str, audience: &str) -> String {
        self.row("Note", json!({ "title": title, "owner": owner, "audience": audience, "createdAt": "2026-11-26T12:00:00Z" })).await
    }

    /// `from` points at `to` with `role`, written as the system.
    async fn reference(&self, from: (&str, &str), to: (&str, &str), role: &str) {
        let (from, to) = (RowRef::new(from.0, from.1), RowRef::new(to.0, to.1));
        self.store.reference(&from, &to, role).await;
    }

    /// Begins an upload to a row through its route and PUTs `bytes` to
    /// the URL it signed, as a browser would; answers the pending file.
    async fn upload(&self, token: &str, path: &str, name: &str, bytes: &[u8], declared: &str) -> Value {
        let begun = self
            .ok(token, Method::POST, &format!("{path}/files"), Some(json!({ "name": name, "mediaType": "text/plain", "size": bytes.len(), "sha256": declared })))
            .await;
        assert_eq!(begun["attachment"]["state"], "pending", "{begun}");
        assert_eq!(begun["upload"]["method"], "PUT", "{begun}");
        let headers: BTreeMap<String, String> = serde_json::from_value(begun["upload"]["headers"].clone()).unwrap();
        let url = begun["upload"]["url"].as_str().unwrap();
        assert_eq!(self.bucket.put_signed(url, &headers, bytes), 200, "the PUT to {url}");
        begun["attachment"].clone()
    }

    /// A JSON-RPC message to the MCP endpoint, and the reply.
    async fn rpc(&self, token: &str, method: &str, params: Value) -> Value {
        let message = json!({"jsonrpc": "2.0", "id": 1, "method": method, "params": params});
        let (status, reply) = self.call(token, Method::POST, "/api/mcp", Some(message)).await;
        assert_eq!(status, StatusCode::OK, "{reply}");
        reply
    }

    async fn drop(self) {
        self.store.close().await;
    }
}

fn case(name: &str) {
    println!("case: {name}");
}

#[tokio::test(flavor = "multi_thread")]
async fn row_features() {
    let s = server().await;
    let (alice, bob, carol) = (uuid(0xa1), uuid(0xb2), uuid(0xc3));

    case("a file posted on one row is filed onto a second, which lists the first in its backlinks");
    {
        let thread = s.ticket("Quarterly plan").await;
        let other = s.note("Planning notes", &alice, "everyone").await;
        let body = b"The plan, in brief.";
        let ticket = format!("/api/ticket/{thread}");
        let pending = s.upload(&alice, &ticket, "plan.txt", body, &hex(body)).await;
        let id = pending["id"].as_str().unwrap();
        let file = s.ok(&alice, Method::POST, &format!("{ticket}/files/{id}/complete"), None).await;
        assert_eq!(file["state"], "ready", "{file}");
        assert_eq!(file["size"], body.len(), "{file}");
        assert_eq!(file["contentHash"], hex(body), "{file}");
        let listed = s.ok(&alice, Method::GET, &format!("{ticket}/files"), None).await;
        assert_eq!(listed.as_array().map(Vec::len), Some(1), "{listed}");
        let objects = s.bucket.len();
        let filed = s
            .ok(&alice, Method::POST, &format!("{ticket}/files/{id}/file"), Some(json!({ "to": { "table": "Note", "key": other } })))
            .await;
        assert_eq!(filed["table"], "Note", "{filed}");
        assert!(same_key(filed["key"].as_str().unwrap(), &other), "{filed}");
        assert_eq!(filed["objectKey"], file["objectKey"], "the copy names the same object: {filed}");
        assert_eq!(s.bucket.len(), objects, "no second upload");
        let note = format!("/api/note/{other}");
        let files = s.ok(&alice, Method::GET, &format!("{note}/files"), None).await;
        assert_eq!(files.as_array().map(Vec::len), Some(1), "{files}");
        assert_eq!(files[0]["id"], filed["id"], "{files}");
        let backlinks = s.ok(&alice, Method::GET, &format!("{note}/backlinks"), None).await;
        let items = backlinks["items"].as_array().unwrap();
        assert_eq!(items.len(), 1, "{backlinks}");
        assert_eq!(items[0]["table"], "Ticket", "{backlinks}");
        assert!(same_key(items[0]["key"].as_str().unwrap(), &thread), "{backlinks}");
        assert_eq!(items[0]["via"], "reference", "{backlinks}");
        assert_eq!(items[0]["role"], "filed", "{backlinks}");
        assert!(backlinks.get("hidden").is_none_or(Value::is_null), "{backlinks}");
        let filed_id = filed["id"].as_str().unwrap();
        let viewed = s.ok(&alice, Method::GET, &format!("{note}/files/{filed_id}"), None).await;
        let object = file["objectKey"].as_str().unwrap();
        assert!(viewed["url"].as_str().unwrap().starts_with(&format!("memory://reference-files/{object}?")), "{viewed}");
        assert_eq!(viewed["attachment"]["id"], filed["id"], "{viewed}");
    }

    case("a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count");
    {
        let shared = s.ticket("Shared ticket").await;
        let hidden = s.note("Bob's private note", &bob, "private").await;
        let quiet = s.note("Alice's private note", &alice, "private").await;
        let open = s.note("Alice's open note", &alice, "everyone").await;
        s.reference(("Note", &quiet), ("Note", &hidden), "").await;
        s.reference(("Note", &open), ("Note", &hidden), "see also").await;
        s.reference(("Note", &open), ("Ticket", &shared), "").await;
        s.reference(("Note", &quiet), ("Ticket", &shared), "").await;
        let refs = s.ok(&carol, Method::GET, &format!("/api/note/{open}/references"), None).await;
        let refs = refs.as_array().unwrap();
        assert_eq!(refs.len(), 1, "carol reads the open note's reference to the ticket alone: {refs:?}");
        assert_eq!(refs[0]["to"]["table"], "Ticket");
        assert!(same_key(refs[0]["to"]["key"].as_str().unwrap(), &shared));
        let seen = s.ok(&carol, Method::GET, &format!("/api/ticket/{shared}/backlinks"), None).await;
        let items = seen["items"].as_array().unwrap();
        assert_eq!(items.len(), 1, "{seen}");
        assert!(same_key(items[0]["key"].as_str().unwrap(), &open), "{seen}");
        assert!(seen.get("hidden").is_none_or(Value::is_null), "no count for carol: {seen}");
        let (status, problem) = s.call(&carol, Method::GET, &format!("/api/note/{hidden}/backlinks"), None).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "carol's backlinks of bob's note: {problem}");
        let (status, problem) = s
            .call(&carol, Method::POST, &format!("/api/note/{open}/references"), Some(json!({ "to": { "table": "Note", "key": hidden } })))
            .await;
        assert_eq!(status, StatusCode::NOT_FOUND, "carol's reference to bob's note: {problem}");
        let owned = s.ok(&bob, Method::GET, &format!("/api/note/{hidden}/backlinks"), None).await;
        let items = owned["items"].as_array().unwrap();
        assert_eq!(items.len(), 1, "{owned}");
        assert!(same_key(items[0]["key"].as_str().unwrap(), &open), "{owned}");
        assert_eq!(owned["hidden"], 1, "bob hears of alice's private note: {owned}");
    }

    case("a reference is added, listed and removed through the routes");
    {
        let (from, to) = (s.ticket("Login fails").await, s.ticket("Session store").await);
        let path = format!("/api/ticket/{from}/references");
        let added = s.ok(&alice, Method::POST, &path, Some(json!({ "to": { "table": "Ticket", "key": to }, "role": "blocks" }))).await;
        assert_eq!(added["role"], "blocks", "{added}");
        assert!(same_key(added["to"]["key"].as_str().unwrap(), &to), "{added}");
        assert!(same_key(added["addedBy"].as_str().unwrap(), &alice), "the reference records its caller: {added}");
        let listed = s.ok(&alice, Method::GET, &path, None).await;
        assert_eq!(listed.as_array().map(Vec::len), Some(1), "{listed}");
        assert_eq!(listed[0]["id"], added["id"], "{listed}");
        let removed = s.ok(&alice, Method::POST, &format!("{path}/remove"), Some(json!({ "to": { "table": "Ticket", "key": to } }))).await;
        assert_eq!(removed, json!({ "removed": 1 }));
        let listed = s.ok(&alice, Method::GET, &path, None).await;
        assert_eq!(listed, json!([]));
    }

    case("an upload whose bytes differ is refused, and a file of another row is not found");
    {
        let row = s.ticket("Bounds").await;
        let ticket = format!("/api/ticket/{row}");
        let wrong = s.upload(&alice, &ticket, "a.txt", b"other bytes", &hex(b"declared bytes")).await;
        let wrong_id = wrong["id"].as_str().unwrap();
        let (status, problem) = s.call(&alice, Method::POST, &format!("{ticket}/files/{wrong_id}/complete"), None).await;
        assert_eq!(status, StatusCode::UNPROCESSABLE_ENTITY, "{problem}");
        assert_eq!(problem["code"], "mismatch", "{problem}");
        assert_eq!(problem["details"]["sha256"]["declared"], hex(b"declared bytes"), "{problem}");
        // The refused upload's file is removed, and stays removed.
        let (status, problem) = s.call(&alice, Method::POST, &format!("{ticket}/files/{wrong_id}/complete"), None).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "a removed upload: {problem}");
        let owner = s.ticket("Owner").await;
        let owned = format!("/api/ticket/{owner}");
        let body = b"owned bytes";
        let pending = s.upload(&alice, &owned, "owned.txt", body, &hex(body)).await;
        let id = pending["id"].as_str().unwrap();
        s.ok(&alice, Method::POST, &format!("{owned}/files/{id}/complete"), None).await;
        let (status, problem) = s.call(&alice, Method::GET, &format!("{ticket}/files/{id}"), None).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "another row's file: {problem}");
        let (status, problem) = s.call(&alice, Method::DELETE, &format!("{ticket}/files/{id}"), None).await;
        assert_eq!(status, StatusCode::NOT_FOUND, "another row's file, removed: {problem}");
        let removed = s.ok(&alice, Method::DELETE, &format!("{owned}/files/{id}"), None).await;
        assert_eq!(removed, json!(true));
        let files = s.ok(&alice, Method::GET, &format!("{owned}/files"), None).await;
        assert_eq!(files, json!([]));
    }

    case("a permission the set names for an operation replaces the class's");
    {
        let owned = s.note("Typed files", &alice, "everyone").await;
        let body = b"note bytes";
        let path = format!("/api/note/{owned}/files");
        let input = json!({ "name": "n.txt", "mediaType": "text/plain", "size": body.len(), "sha256": hex(body) });
        let (status, problem) = s.call(&bob, Method::POST, &path, Some(input.clone())).await;
        assert_eq!(status, StatusCode::FORBIDDEN, "bob's upload: {problem}");
        s.ok(&format!("{alice}:notes.upload"), Method::POST, &path, Some(input.clone())).await;
        let (status, problem) = s.call("", Method::POST, &path, Some(input)).await;
        assert_eq!(status, StatusCode::UNAUTHORIZED, "an upload with no caller: {problem}");
    }

    case("the MCP endpoint lists the reads and addReference as tools, and a call runs the route as its caller");
    {
        let tools = s.rpc(&alice, "tools/list", json!({})).await;
        let mut names: Vec<&str> = tools["result"]["tools"].as_array().unwrap().iter().map(|tool| tool["name"].as_str().unwrap()).collect();
        names.sort_unstable();
        assert_eq!(
            names,
            [
                "note_add_reference",
                "note_attachments",
                "note_backlinks",
                "note_references",
                "ticket_add_reference",
                "ticket_attachments",
                "ticket_backlinks",
                "ticket_references"
            ],
            "{tools}"
        );
        let hidden = s.note("Bob's other note", &bob, "private").await;
        let open = s.note("An open note", &alice, "everyone").await;
        s.reference(("Note", &open), ("Note", &hidden), "").await;
        let reply = s.rpc(&carol, "tools/call", json!({ "name": "note_backlinks", "arguments": { "id": hidden } })).await;
        assert_eq!(reply["result"]["isError"], true, "carol cannot read bob's note through the tool: {reply}");
        let reply = s.rpc(&bob, "tools/call", json!({ "name": "note_backlinks", "arguments": { "id": hidden } })).await;
        let result = &reply["result"];
        assert_ne!(result["isError"], true, "{reply}");
        assert_eq!(result["structuredContent"]["items"].as_array().map(Vec::len), Some(1), "{reply}");
    }

    s.drop().await;
}
