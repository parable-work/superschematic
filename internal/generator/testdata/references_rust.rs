//! fixture-references-db end to end in the Rust repositories (D154, D185),
//! run by TestRustReferencesEndToEnd: tickets with a conversation,
//! references and files; notes with an audience, references to tickets and
//! notes, and files of a few media types. The generated create.sql is
//! applied in a schema of the test's own at REFERENCES_TEST_DATABASE_URL,
//! and the cases are the TypeScript repositories' (tsgen's
//! references.test.ts), through the generated repositories' references()
//! and attachments(), over a bucket in memory whose signed URL a PUT is
//! checked against before its bytes are stored, as a provider checks one.

use std::collections::HashMap;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use runtime::bucket::{
    Body, Bucket, BucketError, Bytes, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions, Stream,
};
use runtime::orm::postgres::{deadpool_postgres, tokio_postgres};
use runtime::orm::rowfeatures::{
    attachment_block, Backlink, BacklinkVia, Backlinks, BeginFile, FileOnto, FileRef, FileState,
    ProcessingKind, ProcessingState, Processed, RowRef,
};
use runtime::orm::{AccessCaller, Caller, Error, Values, WriteOptions};
use runtime::Principal;
use serde_json::{json, Value};
use types::orm::postgres::Repositories;

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

fn hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes)
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect()
}

const BUCKET_KEY: &[u8] = b"records-files-signing-key";

/// A bucket in memory that signs its URLs with an HMAC of the method, the
/// object, the expiry and the content type, and stores a PUT through one
/// only once its signature holds, as a provider checks a presigned PUT.
/// Each object's bytes and content type, by name.
type Objects = HashMap<String, (Vec<u8>, Option<String>)>;

#[derive(Default)]
struct MemoryBucket {
    objects: Mutex<Objects>,
}

fn sign(method: &str, name: &str, expires: u64, content_type: &str) -> String {
    let key = ring::hmac::Key::new(ring::hmac::HMAC_SHA256, BUCKET_KEY);
    let tag = ring::hmac::sign(&key, format!("{method}\n{name}\n{expires}\n{content_type}").as_bytes());
    tag.as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

impl MemoryBucket {
    /// A browser's PUT of `bytes` to a signed URL: 403 for a signature
    /// that does not hold, or one past its expiry.
    fn put_signed(&self, url: &str, headers: &std::collections::BTreeMap<String, String>, bytes: &[u8]) -> u16 {
        let Some(rest) = url.strip_prefix("memory://records-files/") else {
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

    fn has(&self, name: &str) -> bool {
        self.objects.lock().unwrap().contains_key(name)
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
        "records-files"
    }

    async fn put(&self, name: &str, body: Body, options: PutOptions) -> Result<PutResult, BucketError> {
        let mut objects = self.objects.lock().unwrap();
        if let Some((bytes, content_type)) = objects.get(name).filter(|_| options.if_absent) {
            let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type: content_type.clone(), updated: None };
            return Ok(PutResult { object, created: false });
        }
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
            return Err(BucketError::NotFound { bucket: "records-files".into(), object: name.into() });
        };
        let object = Object { name: name.to_owned(), size: bytes.len() as u64, content_type, updated: None };
        Ok(Reader::new(object, Once(Some(Bytes::from(bytes)))))
    }

    async fn delete(&self, name: &str) -> Result<(), BucketError> {
        match self.objects.lock().unwrap().remove(name) {
            Some(_) => Ok(()),
            None => Err(BucketError::NotFound { bucket: "records-files".into(), object: name.into() }),
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
            "memory://records-files/{name}?method={method}&expires={expires}&sig={}",
            sign(method, name, expires, &content_type)
        ))
    }
}

/// A schema of its own holding fixture-references-db's tables, and the
/// pool whose connections search it.
struct Database {
    pool: deadpool_postgres::Pool,
    admin: tokio_postgres::Client,
    schema: String,
    bucket: Arc<MemoryBucket>,
}

async fn connect(config: &tokio_postgres::Config) -> tokio_postgres::Client {
    let (client, connection) = config.connect(tokio_postgres::NoTls).await.unwrap();
    tokio::spawn(connection);
    client
}

async fn database() -> Database {
    let url = std::env::var("REFERENCES_TEST_DATABASE_URL").expect("REFERENCES_TEST_DATABASE_URL names the database");
    let config: tokio_postgres::Config = url.parse().unwrap();
    let admin = connect(&config).await;
    let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
    let schema = format!("references_rust_{nanos}");
    admin.batch_execute(&format!("CREATE SCHEMA {schema}")).await.unwrap();
    let mut scoped = config;
    scoped.options(format!("-c search_path={schema},public"));
    let client = connect(&scoped).await;
    client.batch_execute(include_str!("../create.sql")).await.unwrap();
    let manager = deadpool_postgres::Manager::new(scoped, tokio_postgres::NoTls);
    let pool = deadpool_postgres::Pool::builder(manager).max_size(8).build().unwrap();
    Database { pool, admin, schema, bucket: Arc::new(MemoryBucket::default()) }
}

/// Whom the repositories act and read as: a user, whose audience and
/// grants they keep to, or the system, which reads every row.
#[derive(Clone)]
enum Who {
    User(String),
    System,
}

type Repos<'a> = Repositories<'a, deadpool_postgres::Transaction<'a>>;

impl Database {
    /// Runs `work` in a transaction of the repositories as `who`, and
    /// commits what it kept; a refused write's transaction keeps nothing.
    async fn tx<T>(&self, who: &Who, work: impl AsyncFnOnce(&Repos<'_>) -> T) -> T {
        let mut client = self.pool.get().await.unwrap();
        let tx = client.transaction().await.unwrap();
        let repos = Repositories::new(&tx).unwrap();
        let repos = match who {
            Who::System => repos.as_system(),
            Who::User(user) => repos
                .acting(Some(user.clone()))
                .with_caller(Caller { subject: user.clone(), permissions: Vec::new(), stands_in: false })
                .with_access(AccessCaller::of(Some(&Principal::new(user.clone(), Vec::<String>::new())))),
        };
        let out = work(&repos).await;
        drop(repos);
        let _ = tx.commit().await;
        out
    }

    async fn ticket(&self, who: &Who, title: &str) -> String {
        self.tx(who, async |r| {
            let row = r.untyped().table("Ticket")?.create_one(&values(json!({ "title": title }))).await?;
            Ok::<_, Error>(row["id"].as_str().unwrap().to_owned())
        })
        .await
        .unwrap()
    }

    async fn note(&self, title: &str, owner: &str, audience: &str) -> String {
        self.tx(&Who::System, async |r| {
            let row = r
                .untyped()
                .table("Note")?
                .create_one(&values(json!({ "title": title, "owner": owner, "audience": audience, "createdAt": "2026-11-26T12:00:00Z" })))
                .await?;
            Ok::<_, Error>(row["id"].as_str().unwrap().to_owned())
        })
        .await
        .unwrap()
    }

    async fn post(&self, who: &Who, ticket: &str, body: &str, blocks: Value) -> Value {
        self.tx(who, async |r| {
            r.untyped()
                .table("Ticket")?
                .operation("comments", "post", &json!(ticket), &json!({ "body": body, "blocks": blocks }), &WriteOptions::default())
                .await
        })
        .await
        .unwrap()
    }

    /// Uploads bytes to a row as a browser would: begin, a PUT to the
    /// signed URL; answers the pending file.
    #[allow(clippy::too_many_arguments)]
    async fn upload(&self, who: &Who, table: &str, key: &str, name: &str, media_type: &str, bytes: &[u8], declared: &str) -> runtime::orm::rowfeatures::Attachment {
        let bucket = self.bucket.clone();
        let (file, upload) = self
            .tx(who, async |r| {
                r.attachments()?
                    .begin(&begin(table, key, name, media_type, bytes.len() as u64, declared), bucket.as_ref(), None)
                    .await
            })
            .await
            .unwrap();
        assert_eq!(file.state, FileState::Pending);
        assert_eq!(self.bucket.put_signed(&upload.url, &upload.headers, bytes), 200);
        file
    }

    async fn complete(&self, who: &Who, table: &str, id: &str) -> Result<runtime::orm::rowfeatures::Attachment, Error> {
        let bucket = self.bucket.clone();
        self.tx(who, async |r| r.attachments()?.complete(&FileRef::new(table, id), bucket.as_ref()).await).await
    }

    async fn release(&self, object_key: &str) -> bool {
        let bucket = self.bucket.clone();
        self.tx(&Who::System, async |r| r.attachments()?.release(object_key, bucket.as_ref()).await).await.unwrap()
    }

    async fn rows(&self, sql: &str, args: &[&str]) -> Vec<Vec<Option<String>>> {
        let client = self.pool.get().await.unwrap();
        let params: Vec<&(dyn tokio_postgres::types::ToSql + Sync)> =
            args.iter().map(|a| a as &(dyn tokio_postgres::types::ToSql + Sync)).collect();
        client
            .query(sql, &params)
            .await
            .unwrap()
            .iter()
            .map(|row| (0..row.len()).map(|i| row.get::<_, Option<String>>(i)).collect())
            .collect()
    }

    async fn drop(self) {
        let Database { pool, admin, schema, .. } = self;
        pool.close();
        admin.batch_execute(&format!("DROP SCHEMA {schema} CASCADE")).await.unwrap();
    }
}

fn values(value: Value) -> Values {
    value.as_object().unwrap().clone()
}

fn begin(table: &str, key: &str, name: &str, media_type: &str, size: u64, sha256: &str) -> BeginFile {
    BeginFile {
        to: RowRef::new(table, key),
        name: name.to_owned(),
        media_type: media_type.to_owned(),
        size,
        sha256: sha256.to_owned(),
        caption: None,
    }
}

/// The code of a references or attachments refusal.
fn code(err: &Error) -> &str {
    match err {
        Error::Refused(refusal) => {
            assert_eq!(refusal.status, 422, "{err}");
            &refusal.code
        }
        other => panic!("not a refusal: {other}"),
    }
}

fn not_found<T: std::fmt::Debug>(result: Result<T, Error>) {
    assert!(matches!(result, Err(Error::NotFound(_))), "{result:?}");
}

fn reference(table: &str, key: &str, role: &str) -> Backlink {
    Backlink { table: table.into(), key: key.into(), via: BacklinkVia::Reference, role: Some(role.into()), message: None }
}

fn case(name: &str) {
    println!("case: {name}");
}

#[tokio::test(flavor = "multi_thread", worker_threads = 4)]
async fn references_and_attachments_on_postgres() {
    let db = database().await;
    let alice = Who::User(uuid(0xa1));
    let bob_id = uuid(0xb0);
    let alice_id = uuid(0xa1);
    let bob = Who::User(bob_id.clone());
    let carol = Who::User(uuid(0xc0));
    let bucket = db.bucket.clone();

    case("a file posted on one row's thread is filed onto a second row, which lists the first in its backlinks");
    {
        let thread = db.ticket(&alice, "Quarterly plan").await;
        let other = db.note("Planning notes", &alice_id, "everyone").await;
        let bytes = b"The plan, in brief.";
        let pending = db.upload(&alice, "Ticket", &thread, "plan.txt", "text/plain", bytes, &hex(bytes)).await;
        // A signature the bucket did not make is refused.
        let (_, upload) = db
            .tx(&alice, async |r| {
                r.attachments()?.begin(&begin("Ticket", &thread, "x.txt", "text/plain", 1, &hex(b"x")), bucket.as_ref(), None).await
            })
            .await
            .unwrap();
        let wrong = std::collections::BTreeMap::from([("content-type".to_owned(), "text/html".to_owned())]);
        assert_eq!(db.bucket.put_signed(&upload.url, &wrong, b"x"), 403);
        let file = db.complete(&alice, "Ticket", &pending.id).await.unwrap();
        assert_eq!((file.state, file.size, file.content_hash.as_str(), file.extraction), (FileState::Ready, bytes.len() as u64, hex(bytes).as_str(), ProcessingState::Pending));
        // Posted on the ticket's thread as an attachment block.
        let block = serde_json::to_value(attachment_block(&file)).unwrap();
        let posted = db.post(&alice, &thread, "The plan.", json!([block])).await;
        assert_eq!(
            posted["blocks"][0],
            json!({ "kind": "attachment", "uri": format!("attachment:{}", file.id), "name": "plan.txt", "mediaType": "text/plain", "bytes": bytes.len(), "sha256": hex(bytes) })
        );
        // Filed onto the note: the same object, no second upload.
        let objects = db.bucket.len();
        let filed = db
            .tx(&alice, async |r| {
                r.attachments()?
                    .file(&FileOnto { file: FileRef::new("Ticket", &file.id), to: RowRef::new("Note", &other), r#move: false, role: None })
                    .await
            })
            .await
            .unwrap();
        assert_eq!(
            (filed.table.as_str(), filed.key.as_str(), filed.object_key.as_str(), filed.filed_from.clone()),
            ("Note", other.as_str(), file.object_key.as_str(), Some(format!("Ticket:{thread}:{}", file.id)))
        );
        assert_eq!(db.bucket.len(), objects);
        let listed = db.tx(&alice, async |r| r.attachments()?.list(&RowRef::new("Note", &other)).await).await.unwrap();
        assert_eq!(listed.iter().map(|f| f.id.as_str()).collect::<Vec<_>>(), [filed.id.as_str()]);
        let listed = db.tx(&alice, async |r| r.attachments()?.list(&RowRef::new("Ticket", &thread)).await).await.unwrap();
        assert_eq!(listed.iter().map(|f| f.id.as_str()).collect::<Vec<_>>(), [file.id.as_str()]);
        // The note lists the ticket in its backlinks.
        let backlinks = db.tx(&alice, async |r| r.references()?.referenced_by(&RowRef::new("Note", &other), None).await).await.unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Ticket", &thread, "filed")], hidden: None });
        // A download is a signed GET, and asks the file's preview on its
        // first view.
        let viewed = db
            .tx(&alice, async |r| r.attachments()?.download(&FileRef::new("Note", &filed.id), bucket.as_ref(), None).await)
            .await
            .unwrap();
        assert!(viewed.url.contains(&format!("/{}?method=GET", file.object_key)), "{}", viewed.url);
        assert_eq!(viewed.attachment.preview, ProcessingState::Pending);
        // A processor's work lands on every file that names the object.
        let pending = db.tx(&alice, async |r| r.attachments()?.pending(ProcessingKind::Extraction, None).await).await.unwrap();
        assert!(pending.iter().any(|p| p.object_key == file.object_key && p.media_type == "text/plain" && p.content_hash == hex(bytes)), "{pending:?}");
        let processed = db
            .tx(&alice, async |r| {
                r.attachments()?
                    .processed(&file.object_key, Some(&Processed { state: ProcessingState::Done, value: Some("The plan, in brief.".into()) }), None)
                    .await
            })
            .await
            .unwrap();
        assert_eq!(processed, 2);
        let read = db.tx(&alice, async |r| r.attachments()?.get(&FileRef::new("Note", &filed.id)).await).await.unwrap();
        assert_eq!((read.extraction, read.extracted_text.as_deref()), (ProcessingState::Done, Some("The plan, in brief.")));
    }

    case("a ref block in a row's thread is a reference of the row, which the target lists");
    {
        let from = db.ticket(&alice, "Login fails").await;
        let to = db.ticket(&alice, "Session store").await;
        db.post(&alice, &from, "See the session store.", json!([{ "kind": "ref", "schema": "Ticket", "id": to }])).await;
        let backlinks = db.tx(&alice, async |r| r.references()?.referenced_by(&RowRef::new("Ticket", &to), None).await).await.unwrap();
        assert_eq!(backlinks.items, [Backlink { table: "Ticket".into(), key: from.clone(), via: BacklinkVia::Comment, role: None, message: Some(1) }]);
    }

    case("a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count");
    {
        let shared = db.ticket(&alice, "Shared ticket").await;
        let hidden = db.note("Bob's private note", &bob_id, "private").await;
        let quiet = db.note("Alice's private note", &alice_id, "private").await;
        let open = db.note("Alice's open note", &alice_id, "everyone").await;
        for (from, to, role) in [
            (RowRef::new("Note", &quiet), RowRef::new("Note", &hidden), ""),
            (RowRef::new("Note", &open), RowRef::new("Note", &hidden), "see also"),
            (RowRef::new("Note", &open), RowRef::new("Ticket", &shared), ""),
            (RowRef::new("Note", &quiet), RowRef::new("Ticket", &shared), ""),
        ] {
            db.tx(&Who::System, async |r| r.references()?.add(&from, &to, role).await).await.unwrap();
        }
        // Carol reads the open note's references: the private note is left
        // out.
        let of = db.tx(&carol, async |r| r.references()?.of(&RowRef::new("Note", &open)).await).await.unwrap();
        assert_eq!(of.iter().map(|r| r.to.clone()).collect::<Vec<_>>(), [RowRef::new("Ticket", &shared)]);
        // The ticket's backlinks hold the open note alone, and no count.
        let backlinks = db.tx(&carol, async |r| r.references()?.referenced_by(&RowRef::new("Ticket", &shared), None).await).await.unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Note", &open, "")], hidden: None });
        // The private note is not there for her, and neither is a way to
        // point at it.
        not_found(db.tx(&carol, async |r| r.references()?.referenced_by(&RowRef::new("Note", &hidden), None).await).await);
        not_found(db.tx(&carol, async |r| r.references()?.add(&RowRef::new("Note", &open), &RowRef::new("Note", &hidden), "").await).await);
        // Bob owns the private note: he sees the open note, and how many he
        // cannot.
        let backlinks = db.tx(&bob, async |r| r.references()?.referenced_by(&RowRef::new("Note", &hidden), None).await).await.unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Note", &open, "see also")], hidden: Some(1) });
        // Alice sees her own two notes point at the ticket.
        let backlinks = db.tx(&alice, async |r| r.references()?.referenced_by(&RowRef::new("Ticket", &shared), None).await).await.unwrap();
        let mut keys: Vec<String> = backlinks.items.iter().map(|b| b.key.clone()).collect();
        keys.sort();
        let mut want = vec![open.clone(), quiet.clone()];
        want.sort();
        assert_eq!(keys, want);
        // A note points at tickets and notes alone; a ticket at any table.
        let other = db.ticket(&alice, "Elsewhere").await;
        let added = db.tx(&alice, async |r| r.references()?.add(&RowRef::new("Ticket", &other), &RowRef::new("Ticket", &shared), "").await).await.unwrap();
        assert_eq!(added.role, "");
        // A second add of the same target and role is the one there.
        let again = db.tx(&alice, async |r| r.references()?.add(&RowRef::new("Ticket", &other), &RowRef::new("Ticket", &shared), "").await).await.unwrap();
        assert_eq!(again.id, added.id);
    }

    case("a target's delete clears the references to it and tells each row that held one");
    {
        let target = db.ticket(&alice, "Short-lived").await;
        let holder = db.note("Pointing note", &alice_id, "everyone").await;
        db.tx(&alice, async |r| r.references()?.add(&RowRef::new("Note", &holder), &RowRef::new("Ticket", &target), "blocks").await).await.unwrap();
        db.tx(&alice, async |r| r.untyped().table("Ticket")?.delete_one(&json!(target), &WriteOptions::default()).await).await.unwrap();
        let of = db.tx(&alice, async |r| r.references()?.of(&RowRef::new("Note", &holder)).await).await.unwrap();
        assert!(of.is_empty(), "{of:?}");
        let cleared = db
            .rows("SELECT holder_table, holder_key::text, target_table, role FROM reference_cleared_queue WHERE target_key = $1", &[&hyphen(&target)])
            .await;
        assert_eq!(cleared, [vec![Some("Note".into()), Some(hyphen(&holder)), Some("Ticket".into()), Some("blocks".into())]]);
    }

    case("an upload's bytes are held to what it declared, a row to its bounds, and an object is deleted once no file names it");
    {
        let row = db.ticket(&alice, "Bounds").await;
        // Bytes that are not the ones declared are refused, and the file
        // goes.
        let wrong = db.upload(&alice, "Ticket", &row, "a.txt", "text/plain", b"other bytes", &hex(b"declared bytes")).await;
        let refused = db.complete(&alice, "Ticket", &wrong.id).await.unwrap_err();
        assert_eq!(code(&refused), "mismatch");
        not_found(db.tx(&alice, async |r| r.attachments()?.get(&FileRef::new("Ticket", &wrong.id)).await).await);
        assert!(db.release(&wrong.object_key).await);
        assert!(!db.bucket.has(&wrong.object_key));
        // A file past a ticket's 1 MiB, and a note's media type, are
        // refused.
        let big = db
            .tx(&alice, async |r| {
                r.attachments()?.begin(&begin("Ticket", &row, "big.bin", "application/octet-stream", 2 * 1024 * 1024, &hex(b"")), bucket.as_ref(), None).await
            })
            .await
            .unwrap_err();
        assert_eq!(code(&big), "too_large");
        let owned = db.note("Typed files", &alice_id, "everyone").await;
        let zip = db
            .tx(&alice, async |r| r.attachments()?.begin(&begin("Note", &owned, "a.zip", "application/zip", 10, &hex(b"")), bucket.as_ref(), None).await)
            .await
            .unwrap_err();
        assert_eq!(code(&zip), "media_type");
        // Four files of 1 MiB fill a ticket's 4 MiB: a fifth is refused.
        for i in 0..4 {
            db.tx(&alice, async |r| {
                r.attachments()?
                    .begin(&begin("Ticket", &row, &format!("p{i}.bin"), "application/octet-stream", 1024 * 1024, &hex(format!("{i}").as_bytes())), bucket.as_ref(), None)
                    .await
            })
            .await
            .unwrap();
        }
        let full = db
            .tx(&alice, async |r| {
                r.attachments()?.begin(&begin("Ticket", &row, "p5.bin", "application/octet-stream", 1, &hex(b"5")), bucket.as_ref(), None).await
            })
            .await
            .unwrap_err();
        assert_eq!(code(&full), "row_full");
        // A file filed onto a note keeps its object until the last file
        // goes.
        let owner = db.ticket(&alice, "Owner").await;
        let bytes = b"shared bytes";
        let uploaded = db.upload(&alice, "Ticket", &owner, "shared.txt", "text/plain", bytes, &hex(bytes)).await;
        let file = db.complete(&alice, "Ticket", &uploaded.id).await.unwrap();
        let moved = db
            .tx(&alice, async |r| {
                r.attachments()?
                    .file(&FileOnto { file: FileRef::new("Ticket", &file.id), to: RowRef::new("Note", &owned), r#move: true, role: None })
                    .await
            })
            .await
            .unwrap();
        not_found(db.tx(&alice, async |r| r.attachments()?.get(&FileRef::new("Ticket", &file.id)).await).await);
        assert!(!db.release(&file.object_key).await);
        db.tx(&alice, async |r| r.attachments()?.detach(&FileRef::new("Note", &moved.id)).await).await.unwrap();
        let released = db.rows("SELECT reason FROM attachment_released_queue WHERE object_key = $1 ORDER BY created_at", &[&file.object_key]).await;
        assert_eq!(released, [vec![Some("expired".into())], vec![Some("detached".into())], vec![Some("detached".into())]]);
        assert!(db.release(&file.object_key).await);
        assert!(!db.bucket.has(&file.object_key));
        // A write that takes a lock runs in a transaction.
        let client = db.pool.get().await.unwrap();
        let outside = Repositories::new(&client).unwrap().as_system();
        let err = outside.attachments().unwrap().release(&file.object_key, bucket.as_ref()).await.unwrap_err();
        assert!(matches!(err, Error::Invalid(_)), "{err}");
    }

    db.drop().await;
}
