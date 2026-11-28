//! fixture-references-db's references and attachments in the Rust
//! repositories on a SQLite file (D154, D182, #611), run by
//! TestRustReferencesOnSQLite: the build's SQLite DDL applied to a file of
//! the test's own, and TestRustReferencesEndToEnd's cases on Postgres held
//! to the repositories' references() and attachments() over it, through a
//! bucket in memory whose signed URL a PUT is checked against. A target's
//! delete clears its references and a file's delete releases its object
//! by the file's triggers, whatever writes the rows.
//!
//! Each call is polled once: the SQLite repositories never wait, and the
//! bucket answers at once.

use std::collections::{BTreeMap, HashMap};
use std::future::Future;
use std::pin::{pin, Pin};
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll, Waker};
use std::time::{SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use runtime::bucket::{
    Body, Bucket, BucketError, Bytes, ListOptions, Object, Page, PutOptions, PutResult, Reader,
    SignedUrlOptions, Stream,
};
use runtime::orm::rowfeatures::{Attachment, Backlink, BacklinkVia, Backlinks, BeginFile, FileOnto, FileRef, FileState, RowRef};
use runtime::orm::sqlite::{rusqlite, Database};
use runtime::orm::{AccessCaller, Caller, Error, Values, WriteOptions};
use runtime::Principal;
use serde_json::{json, Value};
use types::orm::sqlite::Repositories;

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

/// A base62 UUID's hyphenated text, as the file holds it.
fn hyphen(base62: &str) -> String {
    let mut n = 0u128;
    for c in base62.bytes() {
        n = n * 62 + BASE62.iter().position(|b| *b == c).unwrap() as u128;
    }
    let h = format!("{n:032x}");
    format!("{}-{}-{}-{}-{}", &h[0..8], &h[8..12], &h[12..16], &h[16..20], &h[20..])
}

fn hex(bytes: &[u8]) -> String {
    ring::digest::digest(&ring::digest::SHA256, bytes).as_ref().iter().map(|b| format!("{b:02x}")).collect()
}

/// The answer of work that never waits.
fn ready<T>(work: impl Future<Output = T>) -> T {
    match pin!(work).as_mut().poll(&mut Context::from_waker(Waker::noop())) {
        Poll::Ready(out) => out,
        Poll::Pending => panic!("the work waited"),
    }
}

const BUCKET_KEY: &[u8] = b"records-files-signing-key";

/// Each object's bytes and content type, by name.
type Objects = HashMap<String, (Vec<u8>, Option<String>)>;

/// A bucket in memory that signs its URLs with an HMAC of the method, the
/// object, the expiry and the content type, and stores a PUT through one
/// only once its signature holds.
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
    /// A browser's PUT of `bytes` to a signed URL: 403 for a signature that
    /// does not hold.
    fn put_signed(&self, url: &str, headers: &BTreeMap<String, String>, bytes: &[u8]) -> u16 {
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
        self.objects.lock().unwrap().insert(name.to_owned(), (bytes.to_vec(), Some(content_type.to_owned())));
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
        let bytes = body.as_bytes().cloned().unwrap_or_default();
        let size = bytes.len() as u64;
        self.objects.lock().unwrap().insert(name.to_owned(), (bytes.to_vec(), options.content_type.clone()));
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
        Ok(format!("memory://records-files/{name}?method={method}&expires={expires}&sig={}", sign(method, name, expires, &content_type)))
    }
}

/// A directory of the test's own, removed when it ends.
struct TempDir(std::path::PathBuf);

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// Whom the repositories act and read as: a user, whose audience they keep
/// to, or the system, which reads every row.
#[derive(Clone)]
enum Who {
    User(String),
    System,
}

struct File {
    database: Database,
    bucket: Arc<MemoryBucket>,
    _dir: TempDir,
}

impl File {
    fn new() -> Self {
        let nanos = SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_nanos();
        let dir = std::env::temp_dir().join(format!("references-rust-sqlite-{nanos}"));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("references.db");
        let connection = rusqlite::Connection::open(&path).unwrap();
        connection.execute_batch(include_str!("../create.sql")).unwrap();
        drop(connection);
        Self { database: Database::open(&path).unwrap(), bucket: Arc::new(MemoryBucket::default()), _dir: TempDir(dir) }
    }

    /// Runs `work` in a write transaction of the repositories as `who`,
    /// which commits when it answers and rolls back when it fails.
    fn tx<T>(&self, who: &Who, work: impl FnOnce(&Repositories<'_>) -> Result<T, Error>) -> Result<T, Error> {
        self.database.write(|connection| {
            let repos = Repositories::new(connection)?;
            let repos = match who {
                Who::System => repos.as_system(),
                Who::User(user) => repos
                    .acting(Some(user.clone()))
                    .with_caller(Caller { subject: user.clone(), ..Default::default() })
                    .with_access(AccessCaller::of(Some(&Principal::new(user.clone(), Vec::<String>::new())))),
            };
            work(&repos)
        })
    }

    fn ticket(&self, who: &Who, title: &str) -> String {
        self.tx(who, |r| Ok(r.untyped().table("Ticket")?.create_one(&values(json!({ "title": title })))?["id"].as_str().unwrap().to_owned()))
            .unwrap()
    }

    fn note(&self, title: &str, owner: &str, audience: &str) -> String {
        self.tx(&Who::System, |r| {
            let row = r
                .untyped()
                .table("Note")?
                .create_one(&values(json!({ "title": title, "owner": owner, "audience": audience, "createdAt": "2026-11-26T12:00:00Z" })))?;
            Ok(row["id"].as_str().unwrap().to_owned())
        })
        .unwrap()
    }

    /// Uploads bytes to a row as a browser would: begin, then a PUT to the
    /// signed URL; answers the pending file.
    #[allow(clippy::too_many_arguments)]
    fn upload(&self, who: &Who, table: &str, key: &str, name: &str, media_type: &str, bytes: &[u8], declared: &str) -> Attachment {
        let bucket = self.bucket.clone();
        let (file, upload) = self
            .tx(who, |r| ready(r.attachments()?.begin(&begin(table, key, name, media_type, bytes.len() as u64, declared), bucket.as_ref(), None)))
            .unwrap();
        assert_eq!(file.state, FileState::Pending);
        assert_eq!(self.bucket.put_signed(&upload.url, &upload.headers, bytes), 200);
        file
    }

    /// Completes an upload, and commits what it wrote whatever it answers:
    /// a refused upload's file is removed, as the Postgres test's
    /// transaction keeps it.
    fn complete(&self, who: &Who, table: &str, id: &str) -> Result<Attachment, Error> {
        let bucket = self.bucket.clone();
        self.tx(who, |r| Ok(ready(r.attachments()?.complete(&FileRef::new(table, id), bucket.as_ref()))))?
    }

    fn release(&self, object_key: &str) -> bool {
        let bucket = self.bucket.clone();
        self.tx(&Who::System, |r| ready(r.attachments()?.release(object_key, bucket.as_ref()))).unwrap()
    }

    /// The rows a statement reads, each cell its text.
    fn rows(&self, sql: &str, arg: &str) -> Vec<Vec<Option<String>>> {
        self.database
            .read(|connection| {
                let mut statement = connection.prepare(sql).unwrap();
                let columns = statement.column_count();
                let rows = statement
                    .query_map([arg], |row| (0..columns).map(|i| row.get::<_, Option<String>>(i)).collect::<Result<Vec<_>, _>>())
                    .unwrap()
                    .collect::<Result<Vec<_>, _>>()
                    .unwrap();
                Ok::<_, Error>(rows)
            })
            .unwrap()
    }
}

fn values(value: Value) -> Values {
    value.as_object().unwrap().clone()
}

fn begin(table: &str, key: &str, name: &str, media_type: &str, size: u64, sha256: &str) -> BeginFile {
    BeginFile { to: RowRef::new(table, key), name: name.to_owned(), media_type: media_type.to_owned(), size, sha256: sha256.to_owned(), caption: None }
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

#[test]
fn references_and_attachments_on_sqlite() {
    let db = File::new();
    let alice_id = uuid(0xa1);
    let bob_id = uuid(0xb0);
    let alice = Who::User(alice_id.clone());
    let bob = Who::User(bob_id.clone());
    let carol = Who::User(uuid(0xc0));
    let bucket = db.bucket.clone();

    {
        let thread = db.ticket(&alice, "Quarterly plan");
        let other = db.note("Planning notes", &alice_id, "everyone");
        let bytes = b"The plan, in brief.";
        let pending = db.upload(&alice, "Ticket", &thread, "plan.txt", "text/plain", bytes, &hex(bytes));
        // A signature the bucket did not make is refused.
        let (_, upload) = db
            .tx(&alice, |r| ready(r.attachments()?.begin(&begin("Ticket", &thread, "x.txt", "text/plain", 1, &hex(b"x")), bucket.as_ref(), None)))
            .unwrap();
        let wrong = BTreeMap::from([("content-type".to_owned(), "text/html".to_owned())]);
        assert_eq!(db.bucket.put_signed(&upload.url, &wrong, b"x"), 403);
        let file = db.complete(&alice, "Ticket", &pending.id).unwrap();
        assert_eq!((file.state, file.size, file.content_hash.as_str()), (FileState::Ready, bytes.len() as u64, hex(bytes).as_str()));
        // Filed onto the note: the same object, no second upload.
        let objects = db.bucket.len();
        let filed = db
            .tx(&alice, |r| r.attachments()?.file(&FileOnto { file: FileRef::new("Ticket", &file.id), to: RowRef::new("Note", &other), r#move: false, role: None }))
            .unwrap();
        assert_eq!(
            (filed.table.as_str(), filed.key.as_str(), filed.object_key.as_str(), filed.filed_from.clone()),
            ("Note", other.as_str(), file.object_key.as_str(), Some(format!("Ticket:{thread}:{}", file.id)))
        );
        assert_eq!(db.bucket.len(), objects);
        let listed = db.tx(&alice, |r| r.attachments()?.list(&RowRef::new("Note", &other))).unwrap();
        assert_eq!(listed.iter().map(|f| f.id.as_str()).collect::<Vec<_>>(), [filed.id.as_str()]);
        let listed = db.tx(&alice, |r| r.attachments()?.list(&RowRef::new("Ticket", &thread))).unwrap();
        assert_eq!(listed.iter().map(|f| f.id.as_str()).collect::<Vec<_>>(), [file.id.as_str()]);
        // The note lists the ticket in its backlinks.
        let backlinks = db.tx(&alice, |r| r.references()?.referenced_by(&RowRef::new("Note", &other), None)).unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Ticket", &thread, "filed")], hidden: None });
        // A download is a signed GET.
        let viewed = db.tx(&alice, |r| ready(r.attachments()?.download(&FileRef::new("Note", &filed.id), bucket.as_ref(), None))).unwrap();
        assert!(viewed.url.contains(&format!("/{}?method=GET", file.object_key)), "{}", viewed.url);
        case("a file posted on one row is filed onto a second row, the same object, which lists the first in its backlinks");
    }

    {
        let shared = db.ticket(&alice, "Shared ticket");
        let hidden = db.note("Bob's private note", &bob_id, "private");
        let quiet = db.note("Alice's private note", &alice_id, "private");
        let open = db.note("Alice's open note", &alice_id, "everyone");
        for (from, to, role) in [
            (RowRef::new("Note", &quiet), RowRef::new("Note", &hidden), ""),
            (RowRef::new("Note", &open), RowRef::new("Note", &hidden), "see also"),
            (RowRef::new("Note", &open), RowRef::new("Ticket", &shared), ""),
            (RowRef::new("Note", &quiet), RowRef::new("Ticket", &shared), ""),
        ] {
            db.tx(&Who::System, |r| r.references()?.add(&from, &to, role)).unwrap();
        }
        // Carol reads the open note's references: the private note is left
        // out.
        let of = db.tx(&carol, |r| r.references()?.of(&RowRef::new("Note", &open))).unwrap();
        assert_eq!(of.iter().map(|r| r.to.clone()).collect::<Vec<_>>(), [RowRef::new("Ticket", &shared)]);
        // The ticket's backlinks hold the open note alone, and no count.
        let backlinks = db.tx(&carol, |r| r.references()?.referenced_by(&RowRef::new("Ticket", &shared), None)).unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Note", &open, "")], hidden: None });
        // The private note is not there for her, and neither is a way to
        // point at it.
        not_found(db.tx(&carol, |r| r.references()?.referenced_by(&RowRef::new("Note", &hidden), None)));
        not_found(db.tx(&carol, |r| r.references()?.add(&RowRef::new("Note", &open), &RowRef::new("Note", &hidden), "")));
        // Bob owns the private note: he sees the open note, and how many he
        // cannot.
        let backlinks = db.tx(&bob, |r| r.references()?.referenced_by(&RowRef::new("Note", &hidden), None)).unwrap();
        assert_eq!(backlinks, Backlinks { items: vec![reference("Note", &open, "see also")], hidden: Some(1) });
        // Alice sees her own two notes point at the ticket.
        let backlinks = db.tx(&alice, |r| r.references()?.referenced_by(&RowRef::new("Ticket", &shared), None)).unwrap();
        let mut keys: Vec<String> = backlinks.items.iter().map(|b| b.key.clone()).collect();
        keys.sort();
        let mut want = vec![open.clone(), quiet.clone()];
        want.sort();
        assert_eq!(keys, want);
        // A second add of the same target and role is the one there.
        let other = db.ticket(&alice, "Elsewhere");
        let added = db.tx(&alice, |r| r.references()?.add(&RowRef::new("Ticket", &other), &RowRef::new("Ticket", &shared), "")).unwrap();
        let again = db.tx(&alice, |r| r.references()?.add(&RowRef::new("Ticket", &other), &RowRef::new("Ticket", &shared), "")).unwrap();
        assert_eq!(again.id, added.id);
        // A remove takes it away.
        let removed = db.tx(&alice, |r| r.references()?.remove(&RowRef::new("Ticket", &other), &RowRef::new("Ticket", &shared), None)).unwrap();
        assert_eq!(removed, 1);
        case("a caller who cannot read a referenced row sees neither the reference nor a count, and its owner sees a count");
    }

    {
        let target = db.ticket(&alice, "Short-lived");
        let holder = db.note("Pointing note", &alice_id, "everyone");
        db.tx(&alice, |r| r.references()?.add(&RowRef::new("Note", &holder), &RowRef::new("Ticket", &target), "blocks")).unwrap();
        db.tx(&alice, |r| r.untyped().table("Ticket")?.delete_one(&json!(target), &WriteOptions::default())).unwrap();
        let of = db.tx(&alice, |r| r.references()?.of(&RowRef::new("Note", &holder))).unwrap();
        assert!(of.is_empty(), "{of:?}");
        let cleared = db.rows("SELECT holder_table, holder_key, target_table, role FROM reference_cleared_queue WHERE target_key = ?1", &hyphen(&target));
        assert_eq!(cleared, [vec![Some("Note".into()), Some(hyphen(&holder)), Some("Ticket".into()), Some("blocks".into())]]);
        case("a target's delete clears the references to it and tells each row that held one");
    }

    {
        let row = db.ticket(&alice, "Bounds");
        // Bytes that are not the ones declared are refused, and the file
        // goes.
        let wrong = db.upload(&alice, "Ticket", &row, "a.txt", "text/plain", b"other bytes", &hex(b"declared bytes"));
        let refused = db.complete(&alice, "Ticket", &wrong.id).unwrap_err();
        assert_eq!(code(&refused), "mismatch");
        not_found(db.tx(&alice, |r| r.attachments()?.get(&FileRef::new("Ticket", &wrong.id))));
        assert!(db.release(&wrong.object_key));
        assert!(!db.bucket.has(&wrong.object_key));
        // A file past a ticket's 1 MiB, and a note's media type, are
        // refused.
        let big = db
            .tx(&alice, |r| ready(r.attachments()?.begin(&begin("Ticket", &row, "big.bin", "application/octet-stream", 2 * 1024 * 1024, &hex(b"")), bucket.as_ref(), None)))
            .unwrap_err();
        assert_eq!(code(&big), "too_large");
        let owned = db.note("Typed files", &alice_id, "everyone");
        let zip = db
            .tx(&alice, |r| ready(r.attachments()?.begin(&begin("Note", &owned, "a.zip", "application/zip", 10, &hex(b"")), bucket.as_ref(), None)))
            .unwrap_err();
        assert_eq!(code(&zip), "media_type");
        // A file moved onto a note keeps its object until the last file
        // goes.
        let owner = db.ticket(&alice, "Owner");
        let bytes = b"shared bytes";
        let uploaded = db.upload(&alice, "Ticket", &owner, "shared.txt", "text/plain", bytes, &hex(bytes));
        let file = db.complete(&alice, "Ticket", &uploaded.id).unwrap();
        let moved = db
            .tx(&alice, |r| r.attachments()?.file(&FileOnto { file: FileRef::new("Ticket", &file.id), to: RowRef::new("Note", &owned), r#move: true, role: None }))
            .unwrap();
        not_found(db.tx(&alice, |r| r.attachments()?.get(&FileRef::new("Ticket", &file.id))));
        assert!(!db.release(&file.object_key));
        assert!(db.bucket.has(&file.object_key));
        db.tx(&alice, |r| r.attachments()?.detach(&FileRef::new("Note", &moved.id))).unwrap();
        let released = db.rows("SELECT reason FROM attachment_released_queue WHERE object_key = ?1 ORDER BY created_at, reason DESC", &file.object_key);
        assert_eq!(released, [vec![Some("expired".into())], vec![Some("detached".into())], vec![Some("detached".into())]]);
        assert!(db.release(&file.object_key));
        assert!(!db.bucket.has(&file.object_key));
        case("an upload's bytes are held to what it declared, and an object is deleted once no file names it");
    }
}
