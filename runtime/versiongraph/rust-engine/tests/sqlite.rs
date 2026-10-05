//! The SQLite adapter's own rules, which the scenarios do not reach or reach
//! only in passing: the version fences of refs and release pointers, a taken
//! name and a discarded ref's name free again, the history it writes, STRICT
//! tables and foreign keys, two graphs in one file, the name function, the
//! clock, its ids, the write lock a second connection waits on, a dropped
//! transaction, the caller's transaction, the canonical vectors as a round
//! trip, and the rusqlite binding: the Rust port of the TypeScript package's
//! test/sqlite-cases.ts. Then the shared SQLite vectors (testdata/sqlite):
//! the layout, and a database the TypeScript adapter wrote, read back
//! through this adapter. Every case runs on SQLite through rusqlite and
//! needs no server.

use std::collections::{BTreeMap, BTreeSet};
use std::path::PathBuf;
use std::sync::atomic::{AtomicI64, AtomicU64, AtomicUsize, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant, SystemTime, UNIX_EPOCH};

use regex::Regex;
use rusqlite::types::Value as Cell;
use rusqlite::{Connection, ToSql};
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::{json, Value};
use superschematic_versiongraph::Op;
use superschematic_versiongraph_engine::canonical;
use superschematic_versiongraph_engine::sqlite::{self, Client, Rusqlite, SqlValue, SqliteStorage};
use superschematic_versiongraph_engine::storage::{
    NewCommit, NewRef, Patch, Pin, RefUpdate, ReleaseWrite, RowWrite, SnapshotEntry, Storage, Tx,
};
use superschematic_versiongraph_engine::{
    Commit, CommitOptions, Edits, Engine, Error, KindEdits, Options, Ref, SweepOptions, TreeResult,
};

const GRAPH: &str = "recipe";
const COOK: &str = "Cook";
const BREAD: &str = "Bread";
const SOUP: &str = "Soup";
const DAY: i64 = 86_400_000_000;
/// 2027-01-15T08:00:00Z, in microseconds.
const START: i64 = 1_800_000_000_000_000;

fn testdata() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../testdata")
}

/// The scenarios' graph descriptor, fixture-version-graph-db's Recipe graph.
fn descriptor() -> String {
    std::fs::read_to_string(testdata().join("fixture/recipe.json"))
        .expect("read the fixture's descriptor")
}

fn descriptor_value() -> Value {
    serde_json::from_str(&descriptor()).expect("the descriptor is JSON")
}

/// A kind's columns' value classes, as the fixture declares them.
fn columns(kind: &str) -> BTreeMap<String, String> {
    let d = descriptor_value();
    let k = d["kinds"]
        .as_array()
        .expect("kinds")
        .iter()
        .find(|k| k["kind"] == kind)
        .unwrap_or_else(|| panic!("no kind {kind}"))
        .clone();
    serde_json::from_value(k["columns"].clone()).expect("columns")
}

type Store = SqliteStorage<Arc<Rusqlite>>;

/// A database with the layout, and the adapter's storage and an engine over
/// it.
struct Setup {
    client: Arc<Rusqlite>,
    storage: Arc<Store>,
    engine: Engine,
}

fn options() -> sqlite::Options {
    sqlite::Options {
        graph: GRAPH.to_owned(),
        ..sqlite::Options::default()
    }
}

fn clocked(clock: impl Fn() -> i64 + Send + Sync + 'static) -> sqlite::Options {
    sqlite::Options {
        clock: Some(Arc::new(clock)),
        ..options()
    }
}

/// A clock a test moves, through the returned handle.
fn moving_clock(start: i64) -> (Arc<AtomicI64>, sqlite::Options) {
    let now = Arc::new(AtomicI64::new(start));
    let read = Arc::clone(&now);
    (now, clocked(move || read.load(Ordering::SeqCst)))
}

fn memory() -> Connection {
    Connection::open_in_memory().expect("open an in-memory database")
}

fn engine_over(descriptor: &str, storage: Arc<dyn Storage>) -> Engine {
    Engine::new(
        descriptor,
        storage,
        Options {
            schema_epoch: 1,
            snapshot_every: 3,
            ..Options::default()
        },
    )
    .expect("the engine")
}

async fn setup_on(options: sqlite::Options, connection: Connection) -> Setup {
    let client = Arc::new(Rusqlite::new(connection));
    let adapter =
        Arc::new(sqlite::Adapter::new(&descriptor(), options).expect("the fixture's adapter"));
    adapter
        .create_tables(&client)
        .await
        .expect("create the layout");
    let storage = Arc::new(
        adapter
            .storage(Arc::clone(&client))
            .await
            .expect("bind the client"),
    );
    let engine = engine_over(&descriptor(), storage.clone());
    Setup {
        client,
        storage,
        engine,
    }
}

async fn setup(options: sqlite::Options) -> Setup {
    setup_on(options, memory()).await
}

/// A graph's storage over a client another graph's storage may share.
async fn graph_over(
    client: &Arc<Rusqlite>,
    descriptor: &str,
    options: sqlite::Options,
) -> Arc<Store> {
    let adapter = Arc::new(sqlite::Adapter::new(descriptor, options).expect("the adapter"));
    Arc::new(
        adapter
            .storage(Arc::clone(client))
            .await
            .expect("bind the client"),
    )
}

/// A database file of its own in the system's temporary directory, removed
/// with the value.
struct TempFile(PathBuf);

impl TempFile {
    fn new(name: &str) -> TempFile {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let n = NEXT.fetch_add(1, Ordering::Relaxed);
        TempFile(std::env::temp_dir().join(format!(
            "vg-sqlite-{name}-{nanos}-{}-{n}.sqlite",
            std::process::id()
        )))
    }

    fn open(&self) -> Connection {
        Connection::open(&self.0).expect("open the database file")
    }
}

impl Drop for TempFile {
    fn drop(&mut self) {
        for suffix in ["", "-journal", "-wal", "-shm"] {
            let mut path = self.0.clone().into_os_string();
            path.push(suffix);
            let _ = std::fs::remove_file(path);
        }
    }
}

/// Runs one call in a transaction of its own, committing it when the call
/// succeeds and rolling it back when it fails, and returns the call's
/// result.
macro_rules! transact {
    ($storage:expr, |$tx:ident| $body:expr) => {{
        let mut $tx = $storage.begin().await.expect("begin");
        let result = $body;
        match &result {
            Ok(_) => $tx.commit().await.expect("commit"),
            Err(_) => $tx.rollback().await.expect("roll back"),
        }
        result
    }};
}

/// Runs a statement on the client's connection and returns its rows.
async fn select(client: &Rusqlite, sql: &str, params: &[&dyn ToSql]) -> Vec<Vec<Cell>> {
    let connection = client.connection().await;
    let mut statement = connection
        .prepare(sql)
        .unwrap_or_else(|e| panic!("prepare {sql}: {e}"));
    let columns = statement.column_count();
    let rows = statement
        .query_map(params, |row| {
            (0..columns)
                .map(|i| row.get::<_, Cell>(i))
                .collect::<Result<Vec<_>, _>>()
        })
        .unwrap_or_else(|e| panic!("run {sql}: {e}"));
    rows.collect::<Result<Vec<_>, _>>()
        .unwrap_or_else(|e| panic!("read {sql}: {e}"))
}

/// Runs a statement that returns no rows on the client's connection.
async fn run_sql(client: &Rusqlite, sql: &str) -> Result<usize, rusqlite::Error> {
    client.connection().await.execute(sql, [])
}

fn code(error: &rusqlite::Error) -> Option<i32> {
    match error {
        rusqlite::Error::SqliteFailure(failure, _) => Some(failure.extended_code),
        _ => None,
    }
}

fn text(cell: &Cell) -> String {
    match cell {
        Cell::Text(s) => s.clone(),
        other => panic!("{other:?} is not text"),
    }
}

fn int(cell: &Cell) -> i64 {
    match cell {
        Cell::Integer(n) => *n,
        other => panic!("{other:?} is not an integer"),
    }
}

fn parse(json: &str) -> Value {
    serde_json::from_str(json).unwrap_or_else(|e| panic!("parse {json}: {e}"))
}

/// A history table's images of a row: each one's version, operation and
/// data, in version order.
async fn history(client: &Rusqlite, table: &str, id: &str) -> Vec<(i64, String, Value)> {
    let sql =
        format!("SELECT _version, operation, data FROM {table} WHERE id = ?1 ORDER BY _version");
    select(client, &sql, &[&id])
        .await
        .iter()
        .map(|row| (int(&row[0]), text(&row[1]), parse(&text(&row[2]))))
        .collect()
}

/// Asserts a row the adapter stored is a canonical row of its kind: the
/// canonical rules leave it as it is, members sorted.
fn assert_canonical_row(kind: &str, row: &str) {
    let canonical = canonical::postgres_row(&columns(kind), row).expect("a canonical row");
    assert_eq!(canonical, row, "a {kind} row is canonical");
}

/// A canonical id: base62 of a version-4 UUID.
fn assert_canonical_id(id: &str, what: &str) {
    assert_eq!(
        canonical::uuid(id).expect("a UUID"),
        id,
        "{what} {id} is in its canonical form"
    );
    let hex = canonical::uuid_hyphenated(id).expect("a UUID");
    let v4 = Regex::new(r"^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$")
        .expect("pattern");
    assert!(v4.is_match(&hex), "{what} {id} ({hex}) is a version-4 UUID");
}

fn new_ref(root: &str, parent: Option<&str>, name: &str) -> NewRef {
    NewRef {
        root: root.to_owned(),
        parent: parent.map(str::to_owned),
        base: None,
        name: name.to_owned(),
        actor: COOK.to_owned(),
    }
}

fn new_commit(root: &str, ref_id: &str, parent: Option<&str>, sequence: Option<i64>) -> NewCommit {
    NewCommit {
        root: root.to_owned(),
        ref_id: ref_id.to_owned(),
        parent: parent.map(str::to_owned),
        message: String::new(),
        schema_epoch: 1,
        content_hash: "0".repeat(64),
        sequence,
        actor: COOK.to_owned(),
    }
}

/// A ref and a tagged commit on it, written through the adapter, for the
/// cases that need a commit.
async fn ref_and_commit(tx: &mut (dyn Tx + '_), root: &str, name: &str) -> (Ref, String) {
    let r = tx
        .create_ref(new_ref(root, None, name))
        .await
        .expect("create the ref");
    let sequence = tx.next_sequence(root).await.expect("the next sequence");
    let commit = tx
        .insert_commit(new_commit(root, &r.id, None, Some(sequence)))
        .await
        .expect("write the commit");
    (r, commit.id)
}

/// `ref_and_commit` in a transaction of its own.
async fn committed_ref(storage: &Store, root: &str) -> (Ref, String) {
    let mut tx = storage.begin().await.expect("begin");
    let made = ref_and_commit(&mut *tx, root, "main").await;
    tx.commit().await.expect("commit");
    made
}

/// A step row of the fixture.
fn step_row(key: Option<&str>, instruction: &str, extra: Value) -> Value {
    let mut row =
        json!({"entity_key": key, "position": 1, "instruction": instruction, "timings": {}});
    if let (Value::Object(row), Value::Object(extra)) = (&mut row, extra) {
        row.extend(extra);
    }
    row
}

fn upserts(kinds: &[(&str, Vec<Value>)]) -> Edits {
    kinds
        .iter()
        .map(|(kind, rows)| {
            let edits = KindEdits {
                upsert: rows.clone(),
                ..KindEdits::default()
            };
            ((*kind).to_owned(), edits)
        })
        .collect()
}

fn unsets(kind: &str, keys: &[&str]) -> Edits {
    let edits = KindEdits {
        unset: keys.iter().map(|k| (*k).to_owned()).collect(),
        ..KindEdits::default()
    };
    BTreeMap::from([(kind.to_owned(), edits)])
}

fn id_of(row: &Value) -> String {
    row["id"].as_str().expect("an id").to_owned()
}

async fn read_ref(storage: &Store, id: &str) -> Ref {
    transact!(storage, |tx| tx.read_ref(id).await).expect("read the ref")
}

/// Asserts a result is an error whose message holds `pattern`.
fn refused<T: std::fmt::Debug>(what: &str, pattern: &str, result: &Result<T, Error>) {
    match result {
        Err(error) if error.to_string().contains(pattern) => {}
        other => panic!("{what}: {other:?}, want an error with {pattern:?}"),
    }
}

#[tokio::test]
async fn a_refs_version_fences_its_update_and_its_discard() {
    let s = setup(options()).await;
    let r = transact!(s.storage, |tx| tx
        .create_ref(new_ref(BREAD, None, "main"))
        .await)
    .expect("create the ref");
    assert_eq!(r.version, 1);
    let update = |version: i64, seal: bool| RefUpdate {
        id: r.id.clone(),
        version,
        seal,
        actor: COOK.to_owned(),
        ..RefUpdate::default()
    };
    let moved = transact!(s.storage, |tx| tx.update_ref(update(1, false)).await).expect("move");
    assert_eq!(moved.version, 2);
    let stale = transact!(s.storage, |tx| tx.update_ref(update(1, true)).await);
    assert!(matches!(stale, Err(Error::VersionConflict)), "{stale:?}");
    assert!(!read_ref(&s.storage, &r.id).await.sealed);
    // A refused discard leaves the transaction usable, and it commits.
    let mut tx = s.storage.begin().await.expect("begin");
    let stale = tx.discard_ref(&r.id, 1, COOK).await;
    assert!(matches!(stale, Err(Error::VersionConflict)), "{stale:?}");
    let draft = tx
        .create_ref(new_ref(BREAD, Some(&r.id), "draft"))
        .await
        .expect("create the draft after the refused discard");
    tx.commit().await.expect("commit");
    assert_eq!(read_ref(&s.storage, &draft.id).await.name, "draft");
    transact!(s.storage, |tx| tx.discard_ref(&r.id, 2, COOK).await).expect("discard");
    let discarded = read_ref(&s.storage, &r.id).await;
    assert_eq!((discarded.discarded, discarded.version), (true, 3));
    let again = transact!(s.storage, |tx| tx.discard_ref(&r.id, 3, COOK).await);
    assert!(matches!(again, Err(Error::VersionConflict)), "{again:?}");
    let missing = transact!(s.storage, |tx| tx.read_ref("Missing").await);
    assert!(matches!(missing, Err(Error::NotFound)), "{missing:?}");
}

#[tokio::test]
async fn a_release_pointers_version_fences_its_first_write_and_every_move() {
    let s = setup(options()).await;
    let (_, commit) = committed_ref(&s.storage, BREAD).await;
    let write = |version: i64, actor: &str| ReleaseWrite {
        root: BREAD.to_owned(),
        commit: commit.clone(),
        version,
        actor: actor.to_owned(),
    };
    let first = transact!(s.storage, |tx| tx.write_release(write(0, COOK)).await).expect("first");
    assert_eq!(first.version, 1);
    for version in [0, 2] {
        let stale = transact!(s.storage, |tx| tx.write_release(write(version, COOK)).await);
        assert!(
            matches!(stale, Err(Error::VersionConflict)),
            "version {version}: {stale:?}"
        );
    }
    let moved = transact!(s.storage, |tx| tx.write_release(write(1, "Baker")).await).expect("move");
    assert_eq!((moved.id.as_str(), moved.version), (first.id.as_str(), 2));
    let read = transact!(s.storage, |tx| tx.read_release(BREAD).await).expect("read");
    assert_eq!(read.version, 2);
    let none = transact!(s.storage, |tx| tx.read_release(SOUP).await);
    assert!(matches!(none, Err(Error::NotFound)), "{none:?}");
    // The pointer's history is the release log: each write's image at its
    // version.
    let log: Vec<(i64, String, Value)> = history(&s.client, "\"graph_release_history\"", &first.id)
        .await
        .into_iter()
        .map(|(version, operation, data)| (version, operation, data["updated_by"].clone()))
        .collect();
    assert_eq!(
        log,
        vec![
            (1, "INSERT".to_owned(), json!("Cook")),
            (2, "UPDATE".to_owned(), json!("Baker")),
        ]
    );
}

#[tokio::test]
async fn a_roots_live_ref_names_are_distinct_and_a_discarded_refs_name_is_free_again() {
    let s = setup(options()).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let taken = s.engine.create_primary(COOK, BREAD, "main").await;
    assert!(matches!(&taken, Err(Error::NameTaken(_))), "{taken:?}");
    assert_eq!(taken.unwrap_err().code(), Some("name_taken"));
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    let again = s.engine.branch(COOK, &main.id, "draft").await;
    assert_eq!(again.map(|r| r.id).unwrap_err().code(), Some("name_taken"));
    // Another root takes the name.
    s.engine
        .create_primary(COOK, SOUP, "main")
        .await
        .expect("another root's main");
    s.engine
        .discard(COOK, &draft.id, draft.version)
        .await
        .expect("discard");
    let reborn = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("the name is free again");
    assert_eq!(reborn.version, 1);
}

#[tokio::test]
async fn history_holds_a_members_versions_and_images_a_deletes_actor_and_leaves_out_excluded_columns(
) {
    let s = setup(options()).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    let first = s
        .engine
        .save(
            COOK,
            &draft.id,
            draft.version,
            &upserts(&[(
                "step",
                vec![step_row(
                    Some("Mix"),
                    "Mix",
                    json!({"scratch": "note to self"}),
                )],
            )]),
        )
        .await
        .expect("save");
    let second = s
        .engine
        .save(
            "Baker",
            &draft.id,
            first.ref_.version,
            &upserts(&[("step", vec![step_row(Some("Mix"), "Mix well", json!({}))])]),
        )
        .await
        .expect("save again");
    let row = second.saved["step"][0].clone();
    assert_eq!(row["_version"], json!(2));
    // A column the update leaves out keeps its value on the live row.
    assert_eq!(row["scratch"], json!("note to self"));
    let id = id_of(&row);
    s.engine
        .save(
            "Janitor",
            &draft.id,
            second.ref_.version,
            &unsets("step", &["Mix"]),
        )
        .await
        .expect("unset");
    let images = history(&s.client, "\"graph_member_history\"", &id).await;
    let ops: Vec<(i64, &str)> = images.iter().map(|(v, op, _)| (*v, op.as_str())).collect();
    assert_eq!(ops, vec![(1, "INSERT"), (2, "UPDATE"), (3, "DELETE")]);
    let stored = select(
        &s.client,
        "SELECT data FROM \"graph_member_history\" WHERE id = ?1 ORDER BY _version",
        &[&id],
    )
    .await;
    for (row, (version, _, image)) in stored.iter().zip(&images) {
        assert_canonical_row("step", &text(&row[0]));
        assert!(
            image.get("scratch").is_none(),
            "an image leaves scratch out"
        );
        assert_eq!(image["_version"], json!(*version));
    }
    // An update's image is the row as stored, less scratch.
    let mut updated = row.clone();
    updated.as_object_mut().expect("a row").remove("scratch");
    assert_eq!(images[1].2, updated);
    // The delete's image is the row at its version plus 1, naming the
    // delete's actor in the kind's actor column, updated_by.
    let mut deleted = updated.clone();
    deleted["_version"] = json!(3);
    deleted["updated_by"] = json!("Janitor");
    assert_eq!(images[2].2, deleted);
    let rows = transact!(s.storage, |tx| tx.rows("step", &draft.id).await).expect("rows");
    assert!(rows.is_empty());
    // A kind with no actor column keeps the row's values in its delete's
    // image.
    let version = read_ref(&s.storage, &draft.id).await.version;
    let whisk = s
        .engine
        .save(
            COOK,
            &draft.id,
            version,
            &upserts(&[(
                "utensil",
                vec![json!({"entity_key": "Whisk", "name": "whisk"})],
            )]),
        )
        .await
        .expect("save a utensil");
    let utensil = whisk.saved["utensil"][0].clone();
    s.engine
        .save(
            "Janitor",
            &draft.id,
            whisk.ref_.version,
            &unsets("utensil", &["Whisk"]),
        )
        .await
        .expect("unset the utensil");
    let gone = history(&s.client, "\"graph_member_history\"", &id_of(&utensil)).await;
    let mut kept = utensil.clone();
    kept["_version"] = json!(2);
    assert_eq!(gone[1].2, kept);
    // A ref's history: its insert and each update at its version, the
    // discard's naming its actor.
    let version = read_ref(&s.storage, &draft.id).await.version;
    s.engine
        .discard("Janitor", &draft.id, version)
        .await
        .expect("discard");
    let images = history(&s.client, "\"graph_ref_history\"", &draft.id).await;
    let ops: Vec<(i64, &str)> = images.iter().map(|(v, op, _)| (*v, op.as_str())).collect();
    let want: Vec<(i64, &str)> = (1..=7)
        .map(|v| (v, if v == 1 { "INSERT" } else { "UPDATE" }))
        .collect();
    assert_eq!(ops, want);
    let last = &images.last().expect("an image").2;
    assert_eq!(last["deleted_by"], json!("Janitor"));
    assert_eq!(last["_version"], json!(7));
    let date_time = Regex::new(r"^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+Z$").expect("pattern");
    assert!(
        date_time.is_match(last["deleted_at"].as_str().expect("a time")),
        "{last}"
    );
}

async fn prune(s: &Setup, kind: &str, days: i64, batch: i64) -> Result<i64, Error> {
    transact!(s.storage, |tx| tx.prune(kind, days, batch).await)
}

async fn image_versions(s: &Setup, kind: &str) -> Vec<i64> {
    select(
        &s.client,
        "SELECT _version FROM \"graph_member_history\" WHERE kind = ?1 ORDER BY _version",
        &[&kind],
    )
    .await
    .iter()
    .map(|row| int(&row[0]))
    .collect()
}

#[tokio::test]
async fn prune_deletes_images_past_retention_but_the_newest_and_the_pinned_at_most_a_batch() {
    let (now, options) = moving_clock(START);
    let s = setup(options).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let mut draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    for instruction in ["Knead", "Knead well", "Knead hard"] {
        draft = s
            .engine
            .save(
                COOK,
                &draft.id,
                draft.version,
                &upserts(&[
                    (
                        "step",
                        vec![step_row(Some("Knead"), instruction, json!({}))],
                    ),
                    (
                        "utensil",
                        vec![json!({"entity_key": "Whisk", "name": instruction})],
                    ),
                ]),
            )
            .await
            .expect("save")
            .ref_;
    }
    // The commit pins version 3 of each; versions 1 and 2 are unpinned.
    draft = s
        .engine
        .commit(COOK, &draft.id, draft.version, &CommitOptions::default())
        .await
        .expect("commit")
        .ref_;
    s.engine
        .save(
            COOK,
            &draft.id,
            draft.version,
            &upserts(&[(
                "step",
                vec![step_row(Some("Knead"), "Knead softly", json!({}))],
            )]),
        )
        .await
        .expect("save after the commit");
    // Within the step kind's 365 days, nothing goes.
    now.fetch_add(364 * DAY, Ordering::SeqCst);
    assert_eq!(prune(&s, "step", 0, 0).await.expect("prune"), 0);
    // An argument other than 0 is the retention, in days.
    assert_eq!(prune(&s, "step", 400, 0).await.expect("prune"), 0);
    now.fetch_add(2 * DAY, Ordering::SeqCst);
    assert_eq!(prune(&s, "step", 400, 0).await.expect("prune"), 0);
    // Past the declared 365 days: versions 1 and 2, a batch at a time.
    assert_eq!(prune(&s, "step", 0, 1).await.expect("prune"), 1);
    assert_eq!(image_versions(&s, "step").await, vec![2, 3, 4]);
    assert_eq!(prune(&s, "step", 0, 0).await.expect("prune"), 1);
    assert_eq!(
        image_versions(&s, "step").await,
        vec![3, 4],
        "the pinned version 3 and the newest, version 4, stay"
    );
    assert_eq!(prune(&s, "step", 1, 0).await.expect("prune"), 0);
    // utensil declares no retention, so nothing of it goes, whatever the
    // argument.
    assert_eq!(prune(&s, "utensil", 0, 0).await.expect("prune"), 0);
    assert_eq!(prune(&s, "utensil", 1, 0).await.expect("prune"), 0);
    assert_eq!(image_versions(&s, "utensil").await, vec![1, 2, 3]);
    refused(
        "a negative batch",
        "a batch is a whole number",
        &prune(&s, "step", 0, -1).await,
    );
}

async fn version_ones(s: &Setup) -> Vec<String> {
    let mut ids: Vec<String> = select(
        &s.client,
        "SELECT id FROM \"graph_member_history\" WHERE kind = 'step' AND _version = 1",
        &[],
    )
    .await
    .iter()
    .map(|row| text(&row[0]))
    .collect();
    ids.sort();
    ids
}

#[tokio::test]
async fn prune_keeps_an_image_exactly_its_retention_old_and_takes_the_oldest_first() {
    let (now, options) = moving_clock(START);
    let s = setup(options).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let mut draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    for instruction in ["Mix", "Mix well"] {
        let rows = ["Mix", "Rest", "Bake", "Cool"]
            .iter()
            .map(|key| step_row(Some(key), instruction, json!({})))
            .collect();
        draft = s
            .engine
            .save(COOK, &draft.id, draft.version, &upserts(&[("step", rows)]))
            .await
            .expect("save")
            .ref_;
    }
    // Each row's version 1 is prunable once it is past 365 days old, and not
    // at exactly 365 days.
    now.store(START + 365 * DAY, Ordering::SeqCst);
    assert_eq!(prune(&s, "step", 0, 0).await.expect("prune"), 0);
    // Age three of the four images by a microsecond or more, so the second
    // id is the oldest and the fourth the next: oldest first and either id
    // order disagree. The third stays exactly 365 days old.
    let ids = version_ones(&s).await;
    for (id, by) in [(&ids[1], 3), (&ids[3], 2), (&ids[0], 1)] {
        let sql = format!(
            "UPDATE \"graph_member_history\" SET recorded_at = recorded_at - {by} WHERE id = '{id}' AND _version = 1"
        );
        run_sql(&s.client, &sql).await.expect("age an image");
    }
    let left = |kept: &[usize]| kept.iter().map(|&i| ids[i].clone()).collect::<Vec<_>>();
    assert_eq!(prune(&s, "step", 0, 1).await.expect("prune"), 1);
    assert_eq!(
        version_ones(&s).await,
        left(&[0, 2, 3]),
        "the oldest went first"
    );
    assert_eq!(prune(&s, "step", 0, 1).await.expect("prune"), 1);
    assert_eq!(
        version_ones(&s).await,
        left(&[0, 2]),
        "then the next oldest"
    );
    assert_eq!(prune(&s, "step", 0, 0).await.expect("prune"), 1);
    assert_eq!(
        version_ones(&s).await,
        left(&[2]),
        "an image exactly 365 days old stays"
    );
    now.fetch_add(1, Ordering::SeqCst);
    assert_eq!(prune(&s, "step", 0, 0).await.expect("prune"), 1);
    assert!(version_ones(&s).await.is_empty());
}

#[tokio::test]
async fn the_adapter_writes_a_rows_roles_actor_and_time_never_its_id_or_version_and_keeps_or_nulls_what_it_lacks(
) {
    let s = setup(clocked(|| START)).await;
    let (r, _) = committed_ref(&s.storage, BREAD).await;
    let write = |row: Value, tombstone: bool, actor: &str| RowWrite {
        ref_id: r.id.clone(),
        root: BREAD.to_owned(),
        row,
        tombstone,
        actor: actor.to_owned(),
    };
    let row = json!({
        "entity_key": "Mix", "id": "Elsewhere", "_version": 7, "ref_id": "Other",
        "recipe_id": "Soup", "deleted_on_ref": true, "created_at": "2000-01-01T00:00:00Z",
        "created_by": "Somebody", "position": 1, "instruction": "Mix", "timings": {},
    });
    let inserted = transact!(s.storage, |tx| tx
        .upsert_row("step", write(row, false, COOK))
        .await)
    .expect("insert");
    assert_ne!(inserted["id"], json!("Elsewhere"));
    assert_eq!(inserted["_version"], json!(1));
    assert_eq!(inserted["ref_id"], json!(r.id));
    assert_eq!(inserted["recipe_id"], json!("Bread"));
    assert_eq!(inserted["deleted_on_ref"], json!(false));
    assert_eq!(inserted["created_at"], json!("2027-01-15T08:00:00Z"));
    assert_eq!(inserted["created_by"], json!("Cook"));
    assert_eq!(inserted["updated_by"], json!("Cook"));
    // A column the insert lacks holds null.
    assert_eq!(inserted.get("scratch"), Some(&Value::Null));
    let row = json!({"entity_key": "Mix", "instruction": "Stir", "created_by": "Somebody"});
    let updated = transact!(s.storage, |tx| tx
        .upsert_row("step", write(row, true, "Baker"))
        .await)
    .expect("update");
    let read = transact!(s.storage, |tx| tx.rows("step", &r.id).await).expect("rows");
    assert_eq!(read, vec![updated.clone()]);
    assert_eq!(updated["id"], inserted["id"]);
    assert_eq!(updated["_version"], json!(2));
    assert_eq!(updated["deleted_on_ref"], json!(true));
    // A column the update lacks keeps its value, and the creation audit
    // stays.
    assert_eq!(updated["position"], json!(1));
    assert_eq!(updated["created_by"], json!("Cook"));
    assert_eq!(updated["updated_by"], json!("Baker"));
    // What the adapter stores is canonical: a live row's data and every
    // image.
    let stored = select(
        &s.client,
        "SELECT data FROM \"graph_member\" UNION ALL SELECT data FROM \"graph_member_history\"",
        &[],
    )
    .await;
    assert_eq!(stored.len(), 3);
    for row in stored {
        assert_canonical_row("step", &text(&row[0]));
    }
    // A row without an entity key is a new entity.
    let fresh = transact!(s.storage, |tx| tx
        .upsert_row(
            "step",
            write(step_row(None, "Rest", json!({})), false, COOK)
        )
        .await)
    .expect("a new entity");
    assert_ne!(fresh["entity_key"], inserted["entity_key"]);
    assert_canonical_id(
        fresh["entity_key"].as_str().expect("a key"),
        "a generated entity key",
    );
    // A column the descriptor does not declare is refused, and so is a kind
    // it does not declare.
    let row = json!({"entity_key": "Mix", "flavour": "salt"});
    let undeclared = transact!(s.storage, |tx| tx
        .upsert_row("step", write(row, false, COOK))
        .await);
    refused("an undeclared column", "does not declare", &undeclared);
    let unknown = transact!(s.storage, |tx| tx
        .upsert_row("garnish", write(json!({}), false, COOK))
        .await);
    refused("an unknown kind", "unknown kind \"garnish\"", &unknown);
}

/// The core's content hash of a tree under a descriptor.
fn content_hash(descriptor: &Value, tree: Value) -> String {
    let input = json!({"descriptor": descriptor, "tree": tree}).to_string();
    let out =
        superschematic_versiongraph::run(Op::ContentHash, input.as_bytes()).expect("content_hash");
    out["contentHash"].as_str().expect("a hash").to_owned()
}

#[tokio::test]
async fn after_a_kind_gains_a_column_its_old_rows_read_it_null_and_its_old_images_read_as_stored() {
    let s = setup(options()).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    let saved = s
        .engine
        .save(
            COOK,
            &draft.id,
            draft.version,
            &upserts(&[
                ("step", vec![step_row(Some("Mix"), "Mix", json!({}))]),
                (
                    "utensil",
                    vec![json!({"entity_key": "Whisk", "name": "whisk"})],
                ),
            ]),
        )
        .await
        .expect("save");
    let committed = s
        .engine
        .commit(
            COOK,
            &draft.id,
            saved.ref_.version,
            &CommitOptions::default(),
        )
        .await
        .expect("commit");
    // The schema's next version: utensil gains color, and step gains memo,
    // which its history leaves out.
    let mut d = descriptor_value();
    for k in d["kinds"].as_array_mut().expect("kinds") {
        if k["kind"] == "utensil" {
            k["columns"]["color"] = json!("string");
        }
        if k["kind"] == "step" {
            k["columns"]["memo"] = json!("string");
            k["excluded"]
                .as_array_mut()
                .expect("excluded")
                .push(json!("memo"));
            k["history"]["exclude"]
                .as_array_mut()
                .expect("exclude")
                .push(json!("memo"));
        }
    }
    let next = d.to_string();
    let storage = graph_over(&s.client, &next, options()).await;
    let engine = engine_over(&next, storage.clone());
    let whisk =
        transact!(storage, |tx| tx.rows("utensil", &draft.id).await).expect("rows")[0].clone();
    let mix = transact!(storage, |tx| tx.rows("step", &draft.id).await).expect("rows")[0].clone();
    assert_eq!(whisk.get("color"), Some(&Value::Null));
    assert_eq!(mix.get("memo"), Some(&Value::Null));
    // An image reads as it was stored, without the gained column, as a
    // Postgres history image does.
    let pin = |row: &Value, version: i64| Pin {
        id: id_of(row),
        version,
    };
    let whisk_image =
        transact!(storage, |tx| tx.images("utensil", &[pin(&whisk, 1)]).await).expect("images");
    let mix_image =
        transact!(storage, |tx| tx.images("step", &[pin(&mix, 1)]).await).expect("images");
    let stored = select(
        &s.client,
        "SELECT data FROM \"graph_member_history\" WHERE id = ?1 AND _version = 1",
        &[&id_of(&whisk)],
    )
    .await;
    assert_eq!(whisk_image, vec![parse(&text(&stored[0][0]))]);
    assert!(whisk_image[0].get("color").is_none());
    assert!(mix_image[0].get("memo").is_none());
    assert!(mix_image[0].get("scratch").is_none());
    // The commit's tree lacks color, and the core reads it as null: it hashes
    // as the draft's live rows, which hold it null, and as a tree whose row
    // holds it null.
    let commit_id = committed.commit.expect("a commit").id;
    let tree = engine.materialize(&commit_id).await.expect("materialize");
    let read = tree.tree["utensil"][0].clone();
    assert!(read.get("color").is_none());
    let composed = engine.compose(&draft.id).await.expect("compose");
    assert_eq!(tree.content_hash, composed.content_hash);
    let mut with_null = read.clone();
    with_null["color"] = Value::Null;
    let hash = content_hash(
        &d,
        json!({"step": tree.tree["step"].clone(), "utensil": [with_null]}),
    );
    assert_eq!(tree.content_hash, hash);
    // An update of the old row stores the gained column, null, and its image
    // carries it.
    let updated = engine
        .save(
            COOK,
            &draft.id,
            committed.ref_.version,
            &upserts(&[(
                "utensil",
                vec![json!({"entity_key": "Whisk", "name": "big whisk"})],
            )]),
        )
        .await
        .expect("update");
    assert_eq!(updated.saved["utensil"][0].get("color"), Some(&Value::Null));
    let data = select(
        &s.client,
        "SELECT data FROM \"graph_member\" WHERE id = ?1",
        &[&id_of(&whisk)],
    )
    .await;
    assert_eq!(parse(&text(&data[0][0])).get("color"), Some(&Value::Null));
    let image =
        transact!(storage, |tx| tx.images("utensil", &[pin(&whisk, 2)]).await).expect("images");
    assert_eq!(image[0].get("color"), Some(&Value::Null));
    assert_eq!(image[0]["name"], json!("big whisk"));
}

#[tokio::test]
async fn a_unique_index_backs_each_key_the_adapters_own_reads_keep_unique() {
    let s = setup(options()).await;
    let (r, commit) = committed_ref(&s.storage, BREAD).await;
    let n = AtomicUsize::new(0);
    let fresh = || format!("Row{}", n.fetch_add(1, Ordering::Relaxed));
    let id = &r.id;
    type Insert<'a> = Box<dyn Fn() -> String + 'a>;
    let twice: Vec<(&str, Insert<'_>)> = vec![
        (
            "a commit's entity in its patches",
            Box::new(|| {
                format!("INSERT INTO \"graph_patch\" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES ('{}', 'recipe', '{commit}', 'step', 'Mix', 'B', 1, 'ADD')", fresh())
            }),
        ),
        (
            "a commit's entity in its snapshot",
            Box::new(|| {
                format!("INSERT INTO \"graph_snapshot_entry\" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES ('{}', 'recipe', '{commit}', 'step', 'Mix', 'B', 1)", fresh())
            }),
        ),
        (
            "an entity of a kind on a ref",
            Box::new(|| {
                format!("INSERT INTO \"graph_member\" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES ('{}', 'recipe', 'step', 'Mix', '{id}', 'Bread', 0, 1, '{{}}')", fresh())
            }),
        ),
        (
            "a member's image at a version",
            Box::new(|| {
                format!("INSERT INTO \"graph_member_history\" (history_id, graph, kind, id, _version, operation, data, recorded_at) VALUES ('{}', 'recipe', 'step', 'Same', 1, 'INSERT', '{{}}', 0)", fresh())
            }),
        ),
        (
            "a ref's image at a version",
            Box::new(|| {
                format!("INSERT INTO \"graph_ref_history\" (history_id, graph, id, _version, operation, data, recorded_at) VALUES ('{}', 'recipe', 'Same', 1, 'INSERT', '{{}}', 0)", fresh())
            }),
        ),
        (
            "a pointer's image at a version",
            Box::new(|| {
                format!("INSERT INTO \"graph_release_history\" (history_id, graph, id, _version, operation, data, recorded_at) VALUES ('{}', 'recipe', 'Same', 1, 'INSERT', '{{}}', 0)", fresh())
            }),
        ),
        (
            "a root's release pointer",
            Box::new(|| {
                format!("INSERT INTO \"graph_release\" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES ('{}', 'recipe', 'Soup', '{commit}', 0, 'Cook', 0, 'Cook', 1)", fresh())
            }),
        ),
        (
            "a root's sequence",
            Box::new(|| {
                format!("INSERT INTO \"graph_commit\" (id, graph, root_id, ref_id, schema_epoch, content_hash, sequence, created_at, created_by) VALUES ('{}', 'recipe', 'Bread', '{id}', 1, 'h', 7, 0, 'Cook')", fresh())
            }),
        ),
    ];
    for (what, insert) in &twice {
        run_sql(&s.client, &insert())
            .await
            .unwrap_or_else(|e| panic!("{what}: {e}"));
        let error = run_sql(&s.client, &insert()).await.expect_err(what);
        assert_eq!(
            code(&error),
            Some(sqlite::SQLITE_CONSTRAINT_UNIQUE),
            "{what}: {error}"
        );
    }
}

#[tokio::test]
async fn every_table_is_strict_and_refuses_a_value_of_the_wrong_type() {
    let s = setup(options()).await;
    let error = run_sql(
        &s.client,
        "INSERT INTO \"graph_ref\" (id, graph, root_id, name, created_at, created_by, updated_at, updated_by, _version) \
         VALUES ('A', 'recipe', 'Bread', 'main', 'today', 'Cook', 0, 'Cook', 1)",
    )
    .await
    .expect_err("a text time is refused");
    // SQLITE_CONSTRAINT_DATATYPE.
    assert_eq!(code(&error), Some(3091), "{error}");
    for table in sqlite::TABLES {
        let name = sqlite::default_table_name(table);
        let sql = select(
            &s.client,
            "SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?1",
            &[&name],
        )
        .await;
        assert!(text(&sql[0][0]).ends_with(") STRICT"), "{table} is STRICT");
    }
}

#[tokio::test]
async fn foreign_keys_check_each_of_the_layouts_edges_on_a_connection_the_adapter_binds() {
    let s = setup(options()).await;
    let (r, commit) = committed_ref(&s.storage, BREAD).await;
    let n = AtomicUsize::new(0);
    let fresh = || format!("Row{}", n.fetch_add(1, Ordering::Relaxed));
    let ref_id = r.id.as_str();
    // Each edge as an insert of a row that names the target, which holds a
    // valid row of every other column.
    type Insert<'a> = Box<dyn Fn(&str) -> String + 'a>;
    let edges: Vec<(&str, &str, Insert<'_>)> = vec![
        (
            "a ref's parent",
            ref_id,
            Box::new(|t| {
                format!("INSERT INTO \"graph_ref\" (id, graph, root_id, parent_ref_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('{}', 'recipe', 'Bread', '{t}', '{}', 0, 'Cook', 0, 'Cook', 1)", fresh(), fresh())
            }),
        ),
        (
            "a ref's base",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_ref\" (id, graph, root_id, base_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('{}', 'recipe', 'Bread', '{t}', '{}', 0, 'Cook', 0, 'Cook', 1)", fresh(), fresh())
            }),
        ),
        (
            "a ref's head",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_ref\" (id, graph, root_id, head_commit_id, name, created_at, created_by, updated_at, updated_by, _version) VALUES ('{}', 'recipe', 'Bread', '{t}', '{}', 0, 'Cook', 0, 'Cook', 1)", fresh(), fresh())
            }),
        ),
        (
            "a commit's ref",
            ref_id,
            Box::new(|t| {
                format!("INSERT INTO \"graph_commit\" (id, graph, root_id, ref_id, schema_epoch, content_hash, created_at, created_by) VALUES ('{}', 'recipe', 'Bread', '{t}', 1, 'h', 0, 'Cook')", fresh())
            }),
        ),
        (
            "a commit's parent",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_commit\" (id, graph, root_id, ref_id, parent_commit_id, schema_epoch, content_hash, created_at, created_by) VALUES ('{}', 'recipe', 'Bread', '{ref_id}', '{t}', 1, 'h', 0, 'Cook')", fresh())
            }),
        ),
        (
            "a patch's commit",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_patch\" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) VALUES ('{}', 'recipe', '{t}', 'step', '{}', 'B', 1, 'ADD')", fresh(), fresh())
            }),
        ),
        (
            "a snapshot entry's commit",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_snapshot_entry\" (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) VALUES ('{}', 'recipe', '{t}', 'step', '{}', 'B', 1)", fresh(), fresh())
            }),
        ),
        (
            "a release pointer's commit",
            &commit,
            Box::new(|t| {
                format!("INSERT INTO \"graph_release\" (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) VALUES ('{}', 'recipe', '{}', '{t}', 0, 'Cook', 0, 'Cook', 1)", fresh(), fresh())
            }),
        ),
        (
            "a member's ref",
            ref_id,
            Box::new(|t| {
                format!("INSERT INTO \"graph_member\" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES ('{}', 'recipe', 'step', '{}', '{t}', 'Bread', 0, 1, '{{}}')", fresh(), fresh())
            }),
        ),
    ];
    for (edge, target, insert) in &edges {
        run_sql(&s.client, &insert(target))
            .await
            .unwrap_or_else(|e| panic!("{edge}: {e}"));
        let error = run_sql(&s.client, &insert("Missing"))
            .await
            .expect_err(edge);
        // SQLITE_CONSTRAINT_FOREIGNKEY.
        assert_eq!(code(&error), Some(787), "{edge}: {error}");
    }
}

#[tokio::test]
async fn the_adapter_refuses_a_write_whose_ref_or_commit_is_another_graphs_or_another_roots() {
    let s = setup(options()).await;
    let menu = sqlite::Options {
        graph: "menu".to_owned(),
        ..sqlite::Options::default()
    };
    let other = graph_over(&s.client, &descriptor(), menu).await;
    let mine = committed_ref(&s.storage, BREAD).await;
    let soup = committed_ref(&s.storage, SOUP).await;
    let theirs = committed_ref(&other, BREAD).await;
    let patch = Patch {
        kind: "step".to_owned(),
        entity_key: "Mix".to_owned(),
        entity_id: "Row".to_owned(),
        entity_version: 1,
        operation: "ADD".to_owned(),
        ..Patch::default()
    };
    let entry = SnapshotEntry {
        kind: "step".to_owned(),
        entity_key: "Mix".to_owned(),
        entity_id: "Row".to_owned(),
        entity_version: 1,
    };
    let row_write = |ref_id: &str| RowWrite {
        ref_id: ref_id.to_owned(),
        root: BREAD.to_owned(),
        row: step_row(Some("Mix"), "Mix", json!({})),
        tombstone: false,
        actor: COOK.to_owned(),
    };
    let update = |head: Option<&str>, base: Option<&str>| RefUpdate {
        id: mine.0.id.clone(),
        version: 1,
        head: head.map(str::to_owned),
        base: base.map(str::to_owned),
        seal: false,
        actor: COOK.to_owned(),
    };
    let release = |commit: &str| ReleaseWrite {
        root: BREAD.to_owned(),
        commit: commit.to_owned(),
        version: 0,
        actor: COOK.to_owned(),
    };
    for (whose, (r, commit)) in [("another graph's", &theirs), ("another root's", &soup)] {
        let (ref_id, commit) = (r.id.as_str(), commit.as_str());
        let mut based = new_ref(BREAD, None, "x");
        based.base = Some(commit.to_owned());
        refused(
            &format!("create_ref with {whose} parent"),
            "is not a ref of root",
            &transact!(s.storage, |tx| tx
                .create_ref(new_ref(BREAD, Some(ref_id), "x"))
                .await),
        );
        refused(
            &format!("create_ref with {whose} base"),
            "is not a commit of root",
            &transact!(s.storage, |tx| tx.create_ref(based.clone()).await),
        );
        refused(
            &format!("update_ref to {whose} head"),
            "is not a commit of root",
            &transact!(s.storage, |tx| tx
                .update_ref(update(Some(commit), None))
                .await),
        );
        refused(
            &format!("update_ref to {whose} base"),
            "is not a commit of root",
            &transact!(s.storage, |tx| tx
                .update_ref(update(None, Some(commit)))
                .await),
        );
        refused(
            &format!("insert_commit on {whose} ref"),
            "is not a ref of root",
            &transact!(s.storage, |tx| tx
                .insert_commit(new_commit(BREAD, ref_id, None, None))
                .await),
        );
        refused(
            &format!("insert_commit after {whose} commit"),
            "is not a commit of root",
            &transact!(s.storage, |tx| tx
                .insert_commit(new_commit(BREAD, &mine.0.id, Some(commit), None))
                .await),
        );
        refused(
            &format!("write_release of {whose} commit"),
            "is not a commit of root",
            &transact!(s.storage, |tx| tx.write_release(release(commit)).await),
        );
        refused(
            &format!("upsert_row on {whose} ref"),
            "is not a ref of root",
            &transact!(s.storage, |tx| tx
                .upsert_row("step", row_write(ref_id))
                .await),
        );
    }
    refused(
        "insert_patches of another graph's commit",
        "is not a commit of graph",
        &transact!(s.storage, |tx| tx
            .insert_patches(&theirs.1, std::slice::from_ref(&patch))
            .await),
    );
    refused(
        "insert_snapshot of another graph's commit",
        "is not a commit of graph",
        &transact!(s.storage, |tx| tx
            .insert_snapshot(&theirs.1, std::slice::from_ref(&entry))
            .await),
    );
    // The graph's own refs and commits of the root are taken.
    let mut tx = s.storage.begin().await.expect("begin");
    let mut draft = new_ref(BREAD, Some(&mine.0.id), "draft");
    draft.base = Some(mine.1.clone());
    tx.create_ref(draft).await.expect("create_ref");
    tx.update_ref(update(Some(&mine.1), Some(&mine.1)))
        .await
        .expect("update_ref");
    tx.insert_commit(new_commit(BREAD, &mine.0.id, Some(&mine.1), None))
        .await
        .expect("insert_commit");
    tx.write_release(release(&mine.1))
        .await
        .expect("write_release");
    tx.upsert_row("step", row_write(&mine.0.id))
        .await
        .expect("upsert_row");
    tx.insert_patches(&mine.1, &[patch])
        .await
        .expect("insert_patches");
    tx.insert_snapshot(&mine.1, &[entry])
        .await
        .expect("insert_snapshot");
    tx.commit().await.expect("commit");
}

/// A change set's work in one graph, as the two-graph case does it: three
/// saves of a step, a tagged commit, and a tagged merge into the primary
/// line. It returns the change set and the commit and the merge.
async fn work(
    engine: &Engine,
    primary: &Ref,
) -> (
    Ref,
    superschematic_versiongraph_engine::Commit,
    superschematic_versiongraph_engine::Commit,
) {
    let mut draft = engine
        .branch(COOK, &primary.id, "draft")
        .await
        .expect("draft");
    for instruction in ["Mix", "Mix well", "Mix hard"] {
        draft = engine
            .save(
                COOK,
                &draft.id,
                draft.version,
                &upserts(&[("step", vec![step_row(Some("Mix"), instruction, json!({}))])]),
            )
            .await
            .expect("save")
            .ref_;
    }
    let tagged = CommitOptions {
        tag: true,
        ..CommitOptions::default()
    };
    let committed = engine
        .commit(COOK, &draft.id, draft.version, &tagged)
        .await
        .expect("commit");
    let merged = engine
        .merge(COOK, &draft.id, &primary.id, primary.version, &[], &tagged)
        .await
        .expect("merge");
    (
        committed.ref_,
        committed.commit.expect("a commit"),
        merged.commit.expect("a merge"),
    )
}

async fn count(client: &Rusqlite, sql: &str, params: &[&dyn ToSql]) -> i64 {
    int(&select(client, sql, params).await[0][0])
}

#[tokio::test]
async fn two_graphs_in_one_file_each_read_name_sequence_release_prune_and_sweep_only_their_own() {
    let (now, clock_options) = moving_clock(START);
    let a = setup(clock_options.clone()).await;
    // Graph menu keeps a day of step history, where recipe keeps 365.
    let mut d = descriptor_value();
    for k in d["kinds"].as_array_mut().expect("kinds") {
        if k["kind"] == "step" {
            k["history"]["retentionDays"] = json!(1);
        }
    }
    let menu = d.to_string();
    let b = graph_over(
        &a.client,
        &menu,
        sqlite::Options {
            graph: "menu".to_owned(),
            ..clock_options
        },
    )
    .await;
    let b_engine = engine_over(&menu, b.clone());
    let main = a
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    // The same root and name in the other graph is not taken.
    let other = b_engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("menu's main");
    assert!(matches!(
        b_engine.compose(&main.id).await,
        Err(Error::NotFound)
    ));
    let (a_draft, a_commit, a_merge) = work(&a.engine, &main).await;
    assert!(transact!(b, |tx| tx.rows("step", &a_draft.id).await)
        .expect("rows")
        .is_empty());
    assert!(transact!(b, |tx| tx.commits().await)
        .expect("commits")
        .is_empty());
    assert!(matches!(
        b_engine.materialize(&a_commit.id).await,
        Err(Error::NotFound)
    ));
    assert!(matches!(
        b_engine.release(COOK, BREAD, &a_commit.id, 0).await,
        Err(Error::NotFound)
    ));
    assert!(matches!(
        b_engine.history(&a_draft.id).await,
        Err(Error::NotFound)
    ));
    assert!(matches!(
        b_engine.branch(COOK, &a_draft.id, "x").await,
        Err(Error::NotFound)
    ));
    assert_eq!(
        transact!(b, |tx| tx.next_sequence(BREAD).await).expect("sequence"),
        1
    );
    // Both graphs tag the same root, each numbering its own tags from 1.
    let (b_draft, b_commit, b_merge) = work(&b_engine, &other).await;
    assert_eq!(
        [
            a_commit.sequence,
            a_merge.sequence,
            b_commit.sequence,
            b_merge.sequence
        ],
        [Some(1), Some(2), Some(1), Some(2)]
    );
    // A first pointer in each graph, then a move of menu's while recipe's is
    // at the same version, then a move of recipe's.
    a.engine
        .release(COOK, BREAD, &a_commit.id, 0)
        .await
        .expect("release");
    assert!(matches!(
        b_engine.released(BREAD).await,
        Err(Error::NotFound)
    ));
    b_engine
        .release(COOK, BREAD, &b_commit.id, 0)
        .await
        .expect("release");
    b_engine
        .release(COOK, BREAD, &b_merge.id, 1)
        .await
        .expect("move");
    a.engine
        .release(COOK, BREAD, &a_merge.id, 1)
        .await
        .expect("move");
    let released = [
        a.engine.released(BREAD).await.expect("released").release,
        b_engine.released(BREAD).await.expect("released").release,
    ];
    assert_eq!(
        released.map(|r| (r.commit, r.version)),
        [(a_merge.id.clone(), 2), (b_merge.id.clone(), 2)]
    );
    // A write through one graph's ref from the other is refused.
    let write = RowWrite {
        ref_id: a_draft.id.clone(),
        root: BREAD.to_owned(),
        row: step_row(Some("Mix"), "Mix", json!({})),
        tombstone: false,
        actor: COOK.to_owned(),
    };
    refused(
        "a write through recipe's ref from menu",
        "is not a ref of root",
        &transact!(b, |tx| tx.upsert_row("step", write.clone()).await),
    );
    // Thirty days on, menu's sweep prunes its own step images past its day,
    // and none of recipe's, which keeps 365.
    let images = "SELECT count(*) FROM \"graph_member_history\" WHERE graph = ?1";
    let kept = count(&a.client, images, &[&"recipe"]).await;
    now.fetch_add(30 * DAY, Ordering::SeqCst);
    let sweep = SweepOptions {
        actor: COOK.to_owned(),
        ..SweepOptions::default()
    };
    let swept = b_engine.sweep(&sweep).await.expect("sweep");
    assert_eq!(swept.pruned, BTreeMap::from([("step".to_owned(), 2)]));
    assert_eq!(count(&a.client, images, &[&"recipe"]).await, kept);
    assert!(a
        .engine
        .sweep(&sweep)
        .await
        .expect("sweep")
        .pruned
        .is_empty());
    assert_eq!(
        b_engine
            .compose(&other.id)
            .await
            .expect("compose")
            .content_hash,
        a.engine
            .compose(&main.id)
            .await
            .expect("compose")
            .content_hash
    );
    // Each graph discards its draft, and a week and a day on, past the
    // grace, menu's sweep collects its own draft's rows and leaves recipe's,
    // which recipe's own sweep collects.
    let rows_of = "SELECT count(*) FROM \"graph_member\" WHERE graph = ?1 AND ref_id = ?2";
    a.engine
        .discard(COOK, &a_draft.id, a_draft.version)
        .await
        .expect("discard");
    b_engine
        .discard(COOK, &b_draft.id, b_draft.version)
        .await
        .expect("discard");
    assert_eq!(
        count(&a.client, rows_of, &[&"recipe", &a_draft.id]).await,
        1
    );
    assert_eq!(count(&a.client, rows_of, &[&"menu", &b_draft.id]).await, 1);
    now.fetch_add(8 * DAY, Ordering::SeqCst);
    let swept = b_engine.sweep(&sweep).await.expect("sweep");
    assert_eq!(
        (swept.collected_refs, swept.collected_rows),
        (1, BTreeMap::from([("step".to_owned(), 1)]))
    );
    assert_eq!(
        count(&a.client, rows_of, &[&"recipe", &a_draft.id]).await,
        1
    );
    assert_eq!(count(&a.client, rows_of, &[&"menu", &b_draft.id]).await, 0);
    let own = a.engine.sweep(&sweep).await.expect("sweep");
    assert_eq!(
        (own.collected_refs, own.collected_rows),
        (1, BTreeMap::from([("step".to_owned(), 1)]))
    );
    assert_eq!(
        count(&a.client, rows_of, &[&"recipe", &a_draft.id]).await,
        0
    );
}

#[tokio::test]
async fn the_name_function_names_every_table_and_index() {
    let s = setup(sqlite::Options {
        table_name: Some(Arc::new(|local: &str| format!("vg_{local}"))),
        ..options()
    })
    .await;
    let objects = select(
        &s.client,
        "SELECT type, name, tbl_name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY name",
        &[],
    )
    .await;
    let tables: Vec<String> = objects
        .iter()
        .filter(|o| text(&o[0]) == "table")
        .map(|o| text(&o[1]))
        .collect();
    let mut want: Vec<String> = sqlite::TABLES.iter().map(|t| format!("vg_{t}")).collect();
    want.sort();
    assert_eq!(tables, want);
    let named = Regex::new("^vg_[a-z_]+$").expect("pattern");
    for o in &objects {
        assert!(
            named.is_match(&text(&o[1])),
            "{} {} is named by the function",
            text(&o[0]),
            text(&o[1])
        );
        assert!(text(&o[2]).starts_with("vg_"));
    }
    assert!(objects.iter().any(|o| text(&o[0]) == "index"));
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    s.engine
        .save(
            COOK,
            &draft.id,
            draft.version,
            &upserts(&[("step", vec![step_row(Some("Mix"), "Mix", json!({}))])]),
        )
        .await
        .expect("save");
    assert_eq!(
        count(&s.client, "SELECT count(*) FROM \"vg_member\"", &[]).await,
        1
    );
    // The layout's statements name each object with the function, and the
    // default's start with graph_.
    let statement = Regex::new(r#"^CREATE (UNIQUE )?(TABLE|INDEX) IF NOT EXISTS "graph_[a-z_]+" "#)
        .expect("pattern");
    let statements = sqlite::layout(&sqlite::default_table_name).expect("the layout");
    for sql in &statements {
        assert!(statement.is_match(sql), "{sql}");
    }
    let distinct: BTreeSet<&String> = statements.iter().collect();
    assert_eq!(distinct.len(), statements.len(), "no statement twice");
    // A name function that gives no name is refused.
    for bad in ["", "a\0b"] {
        refused(
            &format!("a table named {bad:?}"),
            "the table name function named",
            &sqlite::layout(&|_: &str| bad.to_owned()),
        );
    }
}

#[tokio::test]
async fn a_transaction_reads_the_clock_once_and_every_write_in_it_has_its_time() {
    let calls = Arc::new(AtomicI64::new(0));
    let counted = Arc::clone(&calls);
    let at = |n: i64| START + n * 1_000_001;
    let s = setup(clocked(move || {
        at(counted.fetch_add(1, Ordering::SeqCst) + 1)
    }))
    .await;
    calls.store(0, Ordering::SeqCst);
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    let rows = vec![
        step_row(Some("Mix"), "Mix", json!({})),
        step_row(Some("Rest"), "Rest", json!({})),
    ];
    let saved = s
        .engine
        .save(COOK, &draft.id, draft.version, &upserts(&[("step", rows)]))
        .await
        .expect("save");
    assert_eq!(
        calls.load(Ordering::SeqCst),
        3,
        "one read of the clock per transaction"
    );
    for row in &saved.saved["step"] {
        assert_eq!(row["created_at"], json!("2027-01-15T08:00:03.000003Z"));
        assert_eq!(row["updated_at"], json!("2027-01-15T08:00:03.000003Z"));
    }
    let recorded = select(
        &s.client,
        "SELECT DISTINCT recorded_at FROM \"graph_member_history\"",
        &[],
    )
    .await;
    assert_eq!(recorded, vec![vec![Cell::Integer(at(3))]]);
    let times = select(
        &s.client,
        "SELECT created_at, updated_at FROM \"graph_ref\" WHERE id = ?1",
        &[&draft.id],
    )
    .await;
    assert_eq!(
        times,
        vec![vec![Cell::Integer(at(2)), Cell::Integer(at(3))]]
    );
    let committed = s
        .engine
        .commit(
            COOK,
            &draft.id,
            saved.ref_.version,
            &CommitOptions::default(),
        )
        .await
        .expect("commit");
    assert_eq!(calls.load(Ordering::SeqCst), 4);
    assert_eq!(
        committed.commit.expect("a commit").created_at,
        "2027-01-15T08:00:04.000004Z"
    );
}

#[tokio::test]
async fn a_transaction_reads_the_clock_once_the_write_lock_is_held() {
    let file = TempFile::new("clock");
    let second = file.open();
    second.busy_timeout(Duration::ZERO).expect("a busy timeout");
    let second = Arc::new(Mutex::new(second));
    // Whether another connection is kept from the write lock when the clock
    // is read.
    let held = Arc::new(Mutex::new(Vec::new()));
    let (probe, seen) = (Arc::clone(&second), Arc::clone(&held));
    let clock = move || {
        let connection = probe.lock().expect("the second connection");
        let kept = match connection.execute_batch("BEGIN IMMEDIATE") {
            Ok(()) => {
                connection.execute_batch("ROLLBACK").expect("roll back");
                false
            }
            Err(error) => code(&error) == Some(sqlite::SQLITE_BUSY),
        };
        seen.lock().expect("held").push(kept);
        START
    };
    let s = setup_on(clocked(clock), file.open()).await;
    transact!(s.storage, |tx| tx
        .create_ref(new_ref(BREAD, None, "main"))
        .await)
    .expect("create");
    s.engine
        .create_primary(COOK, SOUP, "main")
        .await
        .expect("main");
    assert_eq!(*held.lock().expect("held"), vec![true, true]);
}

#[tokio::test]
async fn a_second_connections_begin_immediate_waits_for_the_write_lock_and_then_fails_busy() {
    let file = TempFile::new("lock");
    let a = setup_on(options(), file.open()).await;
    let connection = file.open();
    connection
        .busy_timeout(Duration::from_millis(300))
        .expect("a busy timeout");
    let b = graph_over(
        &Arc::new(Rusqlite::new(connection)),
        &descriptor(),
        options(),
    )
    .await;
    let mut tx = a.storage.begin().await.expect("begin");
    tx.create_ref(new_ref(BREAD, None, "main"))
        .await
        .expect("create");
    let started = Instant::now();
    // The transaction begins by taking the write lock, so even one that would
    // only read never starts.
    let busy = b.begin().await.map(|_| ());
    let waited = started.elapsed();
    tx.commit().await.expect("commit");
    let error = busy.expect_err("the second connection is busy");
    assert_eq!(
        sqlite::result_code(&error),
        Some(sqlite::SQLITE_BUSY),
        "{error}"
    );
    assert!(
        waited >= Duration::from_millis(250),
        "waited {waited:?} for the lock"
    );
    // Once the first transaction commits, the second connection writes, and
    // reads what the first wrote.
    transact!(b, |tx| tx.create_ref(new_ref(SOUP, None, "main")).await).expect("create");
    assert_eq!(
        count(&a.client, "SELECT count(*) FROM \"graph_ref\"", &[]).await,
        2
    );
}

#[tokio::test]
async fn a_dropped_transaction_rolls_back_at_once_and_frees_the_write_lock() {
    let file = TempFile::new("dropped");
    let s = setup_on(options(), file.open()).await;
    let other = file.open();
    other.busy_timeout(Duration::ZERO).expect("a busy timeout");
    {
        let mut tx = s.storage.begin().await.expect("begin");
        tx.create_ref(new_ref(BREAD, None, "dropped"))
            .await
            .expect("create");
        // Dropped without a commit or a rollback, as a cancelled operation's
        // transaction is.
    }
    other
        .execute_batch("BEGIN IMMEDIATE; ROLLBACK")
        .expect("the write lock is free");
    assert!(ref_names(&s.client).await.is_empty());
    // The binding begins again as usual.
    transact!(s.storage, |tx| tx
        .create_ref(new_ref(BREAD, None, "kept"))
        .await)
    .expect("create");
    assert_eq!(ref_names(&s.client).await, vec!["kept"]);
}

async fn ref_names(client: &Rusqlite) -> Vec<String> {
    select(client, "SELECT name FROM \"graph_ref\" ORDER BY name", &[])
        .await
        .iter()
        .map(|row| text(&row[0]))
        .collect()
}

#[tokio::test]
async fn a_transaction_that_fails_rolls_back_and_one_inside_the_callers_is_a_savepoint_that_rolls_back_alone(
) {
    let calls = Arc::new(AtomicI64::new(0));
    let counted = Arc::clone(&calls);
    let s = setup(clocked(move || {
        START + counted.fetch_add(1, Ordering::SeqCst) + 1
    }))
    .await;
    // A transaction the engine rolls back writes nothing.
    let mut tx = s.storage.begin().await.expect("begin");
    tx.create_ref(new_ref(BREAD, None, "gone"))
        .await
        .expect("create");
    tx.rollback().await.expect("roll back");
    assert!(ref_names(&s.client).await.is_empty());
    // Inside a transaction the caller holds, each transaction is a savepoint
    // that reads the clock once: one that fails rolls back alone, and the
    // caller's commit keeps the rest.
    s.client
        .connection()
        .await
        .execute_batch("BEGIN")
        .expect("the caller's transaction");
    calls.store(0, Ordering::SeqCst);
    let mut tx = s.storage.begin().await.expect("a savepoint");
    let first = tx
        .create_ref(new_ref(BREAD, None, "kept"))
        .await
        .expect("create");
    let second = tx
        .create_ref(new_ref(SOUP, None, "kept"))
        .await
        .expect("create");
    tx.commit().await.expect("release the savepoint");
    assert_eq!(calls.load(Ordering::SeqCst), 1);
    let times = select(
        &s.client,
        "SELECT DISTINCT created_at FROM \"graph_ref\" WHERE id IN (?1, ?2)",
        &[&first.id, &second.id],
    )
    .await;
    assert_eq!(times, vec![vec![Cell::Integer(START + 1)]]);
    let mut tx = s.storage.begin().await.expect("a savepoint");
    tx.create_ref(new_ref(BREAD, None, "inner"))
        .await
        .expect("create");
    let taken = tx.create_ref(new_ref(BREAD, None, "kept")).await;
    assert!(matches!(taken, Err(Error::NameTaken(_))), "{taken:?}");
    tx.rollback().await.expect("roll back to the savepoint");
    assert_eq!(calls.load(Ordering::SeqCst), 2);
    let after = s
        .engine
        .create_primary(COOK, BREAD, "after")
        .await
        .expect("an operation in the caller's transaction");
    assert!(
        !s.client.connection().await.is_autocommit(),
        "the caller's transaction is still open"
    );
    s.client
        .connection()
        .await
        .execute_batch("COMMIT")
        .expect("commit the caller's transaction");
    assert_eq!(ref_names(&s.client).await, vec!["after", "kept", "kept"]);
    // The caller's rollback undoes the graph's writes.
    s.client
        .connection()
        .await
        .execute_batch("BEGIN IMMEDIATE")
        .expect("the caller's transaction");
    let dropped = s
        .engine
        .branch(COOK, &after.id, "dropped")
        .await
        .expect("branch");
    s.client
        .connection()
        .await
        .execute_batch("ROLLBACK")
        .expect("roll back the caller's transaction");
    assert!(matches!(
        s.engine.compose(&dropped.id).await,
        Err(Error::NotFound)
    ));
    assert_eq!(ref_names(&s.client).await, vec!["after", "kept", "kept"]);
}

#[tokio::test]
async fn storage_refuses_a_connection_whose_foreign_keys_would_not_turn_on() {
    let client = Arc::new(Rusqlite::new(memory()));
    let adapter = Arc::new(sqlite::Adapter::new(&descriptor(), options()).expect("the adapter"));
    adapter
        .create_tables(&client)
        .await
        .expect("create the layout");
    // SQLite ignores the pragma inside a transaction.
    client
        .connection()
        .await
        .execute_batch("PRAGMA foreign_keys = OFF; BEGIN")
        .expect("a transaction with foreign keys off");
    refused(
        "a connection in a transaction with foreign keys off",
        "foreign keys would not turn on",
        &adapter.storage(Arc::clone(&client)).await.map(|_| ()),
    );
    client
        .connection()
        .await
        .execute_batch("ROLLBACK")
        .expect("roll back");
    adapter
        .storage(Arc::clone(&client))
        .await
        .expect("bind outside a transaction");
    let on = select(&client, "PRAGMA foreign_keys", &[]).await;
    assert_eq!(on, vec![vec![Cell::Integer(1)]]);
}

#[tokio::test]
async fn a_commits_time_is_a_canonical_date_time() {
    let (now, options) = moving_clock(START + 120_000);
    let s = setup(options).await;
    let r = transact!(s.storage, |tx| tx
        .create_ref(new_ref(BREAD, None, "main"))
        .await)
    .expect("create");
    let commit = || async {
        transact!(s.storage, |tx| tx
            .insert_commit(new_commit(BREAD, &r.id, None, None))
            .await)
        .expect("commit")
    };
    assert_eq!(commit().await.created_at, "2027-01-15T08:00:00.12Z");
    now.store(START, Ordering::SeqCst);
    let whole = commit().await;
    assert_eq!(whole.created_at, "2027-01-15T08:00:00Z");
    let read = transact!(s.storage, |tx| tx.read_commit(&whole.id).await).expect("read");
    assert_eq!(read.created_at, "2027-01-15T08:00:00Z");
    now.store(START + 1, Ordering::SeqCst);
    assert_eq!(commit().await.created_at, "2027-01-15T08:00:00.000001Z");
}

#[tokio::test]
async fn every_id_the_adapter_writes_is_a_version_4_uuid_in_its_canonical_form() {
    let s = setup(options()).await;
    let main = s
        .engine
        .create_primary(COOK, BREAD, "main")
        .await
        .expect("main");
    let draft = s
        .engine
        .branch(COOK, &main.id, "draft")
        .await
        .expect("draft");
    let saved = s
        .engine
        .save(
            COOK,
            &draft.id,
            draft.version,
            &upserts(&[("step", vec![step_row(None, "Mix", json!({}))])]),
        )
        .await
        .expect("save");
    let committed = s
        .engine
        .commit(
            COOK,
            &draft.id,
            saved.ref_.version,
            &CommitOptions::default(),
        )
        .await
        .expect("commit");
    let tagged = CommitOptions {
        tag: true,
        ..CommitOptions::default()
    };
    let merged = s
        .engine
        .merge(COOK, &draft.id, &main.id, main.version, &[], &tagged)
        .await
        .expect("merge");
    s.engine
        .release(COOK, BREAD, &merged.commit.expect("a merge").id, 0)
        .await
        .expect("release");
    let mut ids = Vec::new();
    for (table, names) in [
        ("ref", vec!["id"]),
        ("ref_history", vec!["history_id"]),
        ("commit", vec!["id"]),
        ("patch", vec!["id"]),
        ("snapshot_entry", vec!["id"]),
        ("release", vec!["id"]),
        ("release_history", vec!["history_id"]),
        ("member", vec!["id", "entity_key"]),
        ("member_history", vec!["history_id"]),
    ] {
        let rows = select(
            &s.client,
            &format!("SELECT {} FROM \"graph_{table}\"", names.join(", ")),
            &[],
        )
        .await;
        assert!(!rows.is_empty(), "{table} has rows");
        for row in rows {
            for (name, cell) in names.iter().zip(&row) {
                let id = text(cell);
                assert_canonical_id(&id, &format!("{table}.{name}"));
                ids.push(id);
            }
        }
    }
    let distinct: BTreeSet<&String> = ids.iter().collect();
    assert_eq!(
        distinct.len(),
        ids.len() - 1,
        "every id is new but the entity key two rows share"
    );
    assert_canonical_id(&committed.commit.expect("a commit").id, "a commit's id");
}

/// One canonical value vector.
#[derive(Deserialize)]
struct ValueCase {
    name: String,
    class: String,
    postgres: String,
    #[serde(default)]
    canonical: Option<String>,
}

/// One canonical row vector.
#[derive(Deserialize)]
struct RowCase {
    name: String,
    columns: BTreeMap<String, String>,
    postgres: String,
    #[serde(default)]
    canonical: Option<String>,
}

#[derive(Deserialize)]
struct VectorFile {
    #[serde(default)]
    cases: Vec<ValueCase>,
    #[serde(default)]
    rows: Vec<RowCase>,
}

/// A kind of the vectors' graph, whose role columns are named apart from
/// its own.
fn vector_kind(kind: String, columns: &BTreeMap<String, String>) -> Value {
    let mut all = json!({
        "vg_key": "uuid", "vg_id": "uuid", "vg_ref": "uuid", "vg_root": "uuid",
        "vg_tombstone": "boolean", "vg_version": "integer",
    });
    for (column, class) in columns {
        all[column] = json!(class);
    }
    json!({
        "kind": kind, "key": "vg_key", "id": "vg_id", "ref": "vg_ref", "root": "vg_root",
        "tombstone": "vg_tombstone", "version": "vg_version", "history": {"exclude": []}, "columns": all,
    })
}

/// The vectors' graph: its client, its storage and a ref to write on.
struct Vectors {
    client: Arc<Rusqlite>,
    storage: Store,
    ref_id: String,
}

impl Vectors {
    async fn write(&self, kind: &str, row: &str) -> Result<Value, Error> {
        let write = RowWrite {
            ref_id: self.ref_id.clone(),
            root: BREAD.to_owned(),
            row: parse(row),
            tombstone: false,
            actor: COOK.to_owned(),
        };
        transact!(self.storage, |tx| tx.upsert_row(kind, write).await)
    }

    /// The kind's stored row: its id and its data.
    async fn stored(&self, kind: &str) -> Option<(String, String)> {
        select(
            &self.client,
            "SELECT id, data FROM \"graph_member\" WHERE kind = ?1",
            &[&kind],
        )
        .await
        .first()
        .map(|row| (text(&row[0]), text(&row[1])))
    }
}

/// JSON text without the whitespace between its tokens.
fn compact(json: &str) -> String {
    let (mut out, mut in_string, mut escaped) = (String::new(), false, false);
    for c in json.chars() {
        if in_string {
            in_string = escaped || c != '"';
            escaped = !escaped && c == '\\';
        } else if c == '"' {
            in_string = true;
        } else if c.is_whitespace() {
            continue;
        }
        out.push(c);
    }
    out
}

#[tokio::test]
async fn every_canonical_vector_reads_back_as_the_canonical_value_written_live_and_from_history() {
    let dir = testdata().join("canonical");
    let mut files: Vec<PathBuf> = std::fs::read_dir(&dir)
        .expect("read the vectors")
        .map(|entry| entry.expect("an entry").path())
        .filter(|path| path.extension().is_some_and(|ext| ext == "json"))
        .collect();
    files.sort();
    let (mut values, mut rows) = (Vec::new(), Vec::new());
    for file in files {
        let doc: VectorFile =
            serde_json::from_str(&std::fs::read_to_string(&file).expect("read a vector file"))
                .unwrap_or_else(|e| panic!("{}: {e}", file.display()));
        values.extend(doc.cases);
        rows.extend(doc.rows);
    }
    assert!(!values.is_empty() && !rows.is_empty());
    // Input forms the canonical rules read, for each list class, beside the
    // vectors' Postgres forms: each is stored canonical, as the rules give
    // it, or refused.
    let inputs: [(&str, &str); 21] = [
        ("string[]", r#"["a", 1]"#),
        ("string[][]", r#"[["a"], [2]]"#),
        ("integer[]", "[-0, 12]"),
        ("integer[][]", "[[-0, 7], []]"),
        ("integer[][]", "[[1.5]]"),
        ("number[]", "[1e2, -0]"),
        ("number[][]", "[[1.50e1]]"),
        ("boolean[]", r#"["true"]"#),
        ("boolean[][]", r#"[[true], ["true"]]"#),
        ("uuid[][]", r#"[["5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"]]"#),
        ("dateTime[]", r#"["2026-09-01T12:30:00+02:30"]"#),
        ("dateTime[][]", r#"[["2026-09-01T12:30:00.10+02:30"]]"#),
        ("date[]", r#"["2026-02-30"]"#),
        ("date[]", r#"["2026-09-01", "nope"]"#),
        ("date[][]", r#"[["2026-02-29"]]"#),
        ("time[]", r#"["2:30 pm"]"#),
        ("duration[]", r#"["1 day 02:00:00"]"#),
        ("enum[]", "[1]"),
        ("enum[][]", r#"[["again"], [1]]"#),
        ("json[]", r#"[{"b": 1.50, "a": [2.0]}]"#),
        ("json[][]", r#"[[{"b": 1, "a": 0}]]"#),
    ];
    let v = |class: &str| BTreeMap::from([("v".to_owned(), class.to_owned())]);
    let mut kinds = Vec::new();
    for (i, c) in values.iter().enumerate() {
        kinds.push(vector_kind(format!("value{i}"), &v(&c.class)));
        kinds.push(vector_kind(format!("postgres{i}"), &v(&c.class)));
    }
    for (i, c) in rows.iter().enumerate() {
        kinds.push(vector_kind(format!("row{i}"), &c.columns));
        kinds.push(vector_kind(format!("postgresRow{i}"), &c.columns));
    }
    for (i, (class, _)) in inputs.iter().enumerate() {
        kinds.push(vector_kind(format!("input{i}"), &v(class)));
    }
    let descriptor = json!({"version": 3, "kinds": kinds}).to_string();
    let client = Arc::new(Rusqlite::new(memory()));
    let graph = sqlite::Options {
        graph: "vectors".to_owned(),
        ..sqlite::Options::default()
    };
    let adapter = Arc::new(sqlite::Adapter::new(&descriptor, graph).expect("the vectors' adapter"));
    adapter
        .create_tables(&client)
        .await
        .expect("create the layout");
    let storage = adapter.storage(Arc::clone(&client)).await.expect("bind");
    let r = transact!(storage, |tx| tx
        .create_ref(new_ref(BREAD, None, "main"))
        .await)
    .expect("create");
    let g = Vectors {
        client,
        storage,
        ref_id: r.id,
    };
    for (i, c) in values.iter().enumerate() {
        let what = format!("{}/{}", c.class, c.name);
        let kind = format!("value{i}");
        let Some(canonical) = &c.canonical else {
            // A value its class refuses is not stored.
            let refused = g.write(&kind, &format!(r#"{{"v":{}}}"#, c.postgres)).await;
            assert!(
                matches!(refused, Err(Error::Canonical(_))),
                "{what}: {refused:?}"
            );
            assert!(g.stored(&kind).await.is_none(), "{what}");
            continue;
        };
        let written = g
            .write(&kind, &format!(r#"{{"v":{canonical}}}"#))
            .await
            .unwrap_or_else(|e| panic!("{what}: {e}"));
        let (id, data) = g.stored(&kind).await.expect("stored");
        assert_eq!(
            data,
            format!(r#"{{"v":{canonical}}}"#),
            "{what} is stored canonical"
        );
        let postgres = format!("postgres{i}");
        g.write(&postgres, &format!(r#"{{"v":{}}}"#, c.postgres))
            .await
            .unwrap_or_else(|e| panic!("{what}'s Postgres form: {e}"));
        assert_eq!(
            g.stored(&postgres).await.expect("stored").1,
            format!(r#"{{"v":{canonical}}}"#),
            "{what}'s Postgres form is stored canonical"
        );
        let read = transact!(g.storage, |tx| tx.rows(&kind, &g.ref_id).await).expect("rows");
        let pin = Pin {
            id: id.clone(),
            version: 1,
        };
        let image = transact!(g.storage, |tx| tx.images(&kind, &[pin]).await).expect("images");
        for got in [&written, &read[0], &image[0]] {
            assert_eq!(
                serde_json::to_string(&got["v"]).expect("text"),
                *canonical,
                "{what}: {got}"
            );
        }
    }
    for (i, c) in rows.iter().enumerate() {
        let kind = format!("row{i}");
        let Some(canonical) = &c.canonical else {
            assert!(g.write(&kind, &c.postgres).await.is_err(), "{}", c.name);
            assert!(g.stored(&kind).await.is_none(), "{}", c.name);
            continue;
        };
        g.write(&kind, canonical)
            .await
            .unwrap_or_else(|e| panic!("{}: {e}", c.name));
        let postgres = format!("postgresRow{i}");
        g.write(&postgres, &c.postgres)
            .await
            .unwrap_or_else(|e| panic!("{}'s Postgres form: {e}", c.name));
        // Every declared column, the ones the row lacks as null.
        let members: BTreeMap<String, Box<RawValue>> =
            serde_json::from_str(canonical).expect("a canonical row");
        let expected: Vec<String> = c
            .columns
            .keys()
            .map(|column| {
                let value = members.get(column).map_or("null", |v| v.get());
                format!("{}:{value}", serde_json::to_string(column).expect("a name"))
            })
            .collect();
        let expected = format!("{{{}}}", expected.join(","));
        assert_eq!(
            g.stored(&kind).await.expect("stored").1,
            expected,
            "{}",
            c.name
        );
        assert_eq!(
            g.stored(&postgres).await.expect("stored").1,
            expected,
            "{}: its Postgres form",
            c.name
        );
    }
    for (i, (class, input)) in inputs.iter().enumerate() {
        let kind = format!("input{i}");
        let Ok(want) = canonical::postgres(class, input) else {
            let refused = g.write(&kind, &format!(r#"{{"v":{input}}}"#)).await;
            assert!(
                matches!(refused, Err(Error::Canonical(_))),
                "{class} {input}: {refused:?}"
            );
            assert!(g.stored(&kind).await.is_none(), "{class} {input}");
            continue;
        };
        assert_ne!(
            compact(input),
            want,
            "{class} {input} is not already canonical"
        );
        g.write(&kind, &format!(r#"{{"v":{input}}}"#))
            .await
            .unwrap_or_else(|e| panic!("{class} {input}: {e}"));
        assert_eq!(
            g.stored(&kind).await.expect("stored").1,
            format!(r#"{{"v":{want}}}"#),
            "{class} {input} is stored canonical"
        );
    }
}

#[tokio::test]
async fn the_binding_returns_rows_by_position_and_sqlites_extended_result_code() {
    let client = Rusqlite::new(memory());
    let mut conn = client.begin().await.expect("begin");
    conn.execute(
        "CREATE TABLE t (a TEXT NOT NULL UNIQUE, b INTEGER) STRICT",
        &[],
    )
    .await
    .expect("create a table");
    let changed = conn
        .execute(
            "INSERT INTO t (a, b) VALUES (?1, ?2)",
            &["x".into(), 1.into()],
        )
        .await
        .expect("insert");
    assert_eq!(changed, 1);
    let rows = conn
        .query("SELECT a, b, NULL AS c FROM t WHERE a = ?1", &["x".into()])
        .await
        .expect("select");
    assert_eq!(
        rows,
        vec![vec![
            SqlValue::Text("x".to_owned()),
            SqlValue::Int(1),
            SqlValue::Null
        ]]
    );
    let none = conn
        .query("SELECT a FROM t WHERE a = ?1", &["y".into()])
        .await
        .expect("select");
    assert!(none.is_empty());
    let unique = conn
        .execute(
            "INSERT INTO t (a, b) VALUES (?1, ?2)",
            &["x".into(), 2.into()],
        )
        .await
        .expect_err("a second x");
    assert_eq!(
        unique.code,
        Some(sqlite::SQLITE_CONSTRAINT_UNIQUE),
        "{unique}"
    );
    assert!(
        unique.to_string().contains("UNIQUE constraint failed"),
        "{unique}"
    );
    let syntax = conn
        .query("SELEC 1", &[])
        .await
        .expect_err("a syntax error");
    assert_eq!(syntax.code, Some(1), "{syntax}");
    conn.commit().await.expect("commit");
    let version = client.exec("SELECT sqlite_version()").await.expect("exec");
    assert!(
        matches!(&version[..], [row] if matches!(&row[..], [SqlValue::Text(_)])),
        "{version:?}"
    );
}

/// The layout's statements under the default names equal layout.json's,
/// string for string.
#[test]
fn the_layout_is_the_shared_vectors_layout() {
    #[derive(Deserialize)]
    struct Layout {
        statements: Vec<String>,
    }
    let text =
        std::fs::read_to_string(testdata().join("sqlite/layout.json")).expect("read layout.json");
    let want: Layout = serde_json::from_str(&text).expect("layout.json");
    let got = sqlite::layout(&sqlite::default_table_name).expect("the layout");
    assert_eq!(got, want.statements);
}

/// A database the TypeScript adapter wrote, loaded with foreign keys off.
async fn typescript_database() -> Arc<Rusqlite> {
    let sql = std::fs::read_to_string(testdata().join("sqlite/typescript.sql"))
        .expect("read typescript.sql");
    let connection = memory();
    connection
        .execute_batch("PRAGMA foreign_keys = OFF")
        .expect("foreign keys off");
    for statement in sql.lines().filter(|line| !line.is_empty()) {
        connection
            .execute_batch(statement)
            .unwrap_or_else(|e| panic!("load {statement}: {e}"));
    }
    connection
        .execute_batch("PRAGMA foreign_keys = ON")
        .expect("foreign keys on");
    let broken: Vec<String> = connection
        .prepare("PRAGMA foreign_key_check")
        .expect("prepare")
        .query_map([], |row| row.get::<_, String>(0))
        .expect("check")
        .collect::<Result<_, _>>()
        .expect("read");
    assert!(broken.is_empty(), "foreign keys broken in {broken:?}");
    Arc::new(Rusqlite::new(connection))
}

fn ref_json(r: &Ref) -> Value {
    json!({"id": r.id, "root": r.root, "parent": r.parent, "base": r.base, "head": r.head, "name": r.name,
           "sealed": r.sealed, "discarded": r.discarded, "version": r.version})
}

fn commit_json(c: &Commit) -> Value {
    json!({"id": c.id, "root": c.root, "ref": c.ref_id, "parent": c.parent, "message": c.message,
           "schemaEpoch": c.schema_epoch, "contentHash": c.content_hash, "sequence": c.sequence,
           "createdAt": c.created_at, "createdBy": c.created_by, "snapshot": c.snapshot})
}

fn tree_json(t: &TreeResult) -> Value {
    json!({"tree": t.tree, "contentHash": t.content_hash, "findings": t.findings})
}

/// An engine read as the vectors write it: its value, or its error's code.
fn read_json<T>(result: Result<T, Error>, json: impl Fn(&T) -> Value) -> Value {
    match result {
        Ok(value) => json(&value),
        Err(error) => json!({"error": error.code().unwrap_or_else(|| panic!("{error}"))}),
    }
}

/// Compares what a read returned with the vectors, naming the read.
fn same(failures: &mut Vec<String>, what: &str, got: Value, want: &Value) {
    if got != *want {
        failures.push(format!("{what}:\n  got  {got}\n  want {want}"));
    }
}

/// Every read typescript.json lists, through this adapter and the engine over
/// it, over the database the TypeScript adapter wrote, returns what it lists;
/// and through each graph's adapter nothing of another graph reads.
#[tokio::test]
async fn a_database_the_typescript_adapter_wrote_reads_back_as_the_shared_vectors_say() {
    let text = std::fs::read_to_string(testdata().join("sqlite/typescript.json"))
        .expect("read typescript.json");
    let vectors: Value = serde_json::from_str(&text).expect("typescript.json");
    let client = typescript_database().await;
    let kinds: Vec<String> = descriptor_value()["kinds"]
        .as_array()
        .expect("kinds")
        .iter()
        .map(|k| k["kind"].as_str().expect("a kind").to_owned())
        .collect();
    let graphs = vectors["graphs"].as_array().expect("graphs");
    assert!(graphs.len() >= 2, "the vectors hold two graphs");
    let mut failures = Vec::new();
    for g in graphs {
        let name = g["graph"].as_str().expect("a graph's name");
        for list in ["refs", "commits", "roots", "images"] {
            let entries = g[list].as_array().expect("a list");
            assert!(!entries.is_empty(), "graph {name} lists {list}");
        }
        let storage = graph_over(
            &client,
            &descriptor(),
            sqlite::Options {
                graph: name.to_owned(),
                ..sqlite::Options::default()
            },
        )
        .await;
        let engine = engine_over(&descriptor(), storage.clone());
        for r in g["refs"].as_array().expect("refs") {
            let id = r["id"].as_str().expect("an id");
            let at = format!("{name} ref {id}");
            let read = transact!(storage, |tx| tx.read_ref(id).await);
            same(
                &mut failures,
                &format!("{at} readRef"),
                read_json(read, ref_json),
                &r["readRef"],
            );
            for kind in &kinds {
                let mut rows = transact!(storage, |tx| tx.rows(kind, id).await).expect("rows");
                rows.sort_by(|a, b| a["entity_key"].as_str().cmp(&b["entity_key"].as_str()));
                same(
                    &mut failures,
                    &format!("{at} rows {kind}"),
                    Value::Array(rows),
                    &r["rows"][kind],
                );
            }
            same(
                &mut failures,
                &format!("{at} compose"),
                read_json(engine.compose(id).await, tree_json),
                &r["compose"],
            );
            let history = engine.history(id).await;
            same(
                &mut failures,
                &format!("{at} history"),
                read_json(history, |h| {
                    Value::Array(h.iter().map(commit_json).collect())
                }),
                &r["history"],
            );
        }
        for c in g["commits"].as_array().expect("commits") {
            let id = c["id"].as_str().expect("an id");
            let at = format!("{name} commit {id}");
            let read = transact!(storage, |tx| tx.read_commit(id).await);
            same(
                &mut failures,
                &format!("{at} readCommit"),
                read_json(read, commit_json),
                &c["readCommit"],
            );
            same(
                &mut failures,
                &format!("{at} materialize"),
                read_json(engine.materialize(id).await, tree_json),
                &c["materialize"],
            );
            let mut patches =
                transact!(storage, |tx| tx.patches(&[id.to_owned()]).await).expect("patches");
            patches.sort_by(|a, b| (&a.kind, &a.entity_key).cmp(&(&b.kind, &b.entity_key)));
            let patches: Vec<Value> = patches
                .iter()
                .map(|p| json!({"commit": p.commit, "kind": p.kind, "entityKey": p.entity_key, "entityId": p.entity_id, "entityVersion": p.entity_version, "operation": p.operation}))
                .collect();
            same(
                &mut failures,
                &format!("{at} patches"),
                Value::Array(patches),
                &c["patches"],
            );
            let mut entries = transact!(storage, |tx| tx.snapshot(id).await).expect("snapshot");
            entries.sort_by(|a, b| (&a.kind, &a.entity_key).cmp(&(&b.kind, &b.entity_key)));
            let entries: Vec<Value> = entries
                .iter()
                .map(|e| json!({"kind": e.kind, "entityKey": e.entity_key, "entityId": e.entity_id, "entityVersion": e.entity_version}))
                .collect();
            same(
                &mut failures,
                &format!("{at} snapshot"),
                Value::Array(entries),
                &c["snapshot"],
            );
        }
        for root in g["roots"].as_array().expect("roots") {
            let id = root["root"].as_str().expect("a root");
            let released = engine.released(id).await;
            let got = read_json(released, |r| {
                json!({"release": {"id": r.release.id, "root": r.release.root, "commit": r.release.commit, "version": r.release.version},
                       "tree": r.tree.tree, "contentHash": r.tree.content_hash, "findings": r.tree.findings})
            });
            same(
                &mut failures,
                &format!("{name} root {id} released"),
                got,
                &root["released"],
            );
        }
        for image in g["images"].as_array().expect("images") {
            let (kind, id) = (
                image["kind"].as_str().expect("a kind"),
                image["id"].as_str().expect("an id"),
            );
            let pin = Pin {
                id: id.to_owned(),
                version: image["version"].as_i64().expect("a version"),
            };
            let got = transact!(storage, |tx| tx.images(kind, &[pin]).await).expect("images");
            same(
                &mut failures,
                &format!("{name} image {kind} {id}"),
                Value::Array(got),
                &json!([image["image"]]),
            );
        }
        // Nothing of another graph reads through this graph's adapter.
        for other in graphs.iter().filter(|o| o["graph"] != g["graph"]) {
            let theirs = other["graph"].as_str().expect("a graph's name");
            for r in other["refs"].as_array().expect("refs") {
                let id = r["id"].as_str().expect("an id");
                let at = format!("{theirs} ref {id} through {name}");
                let read = transact!(storage, |tx| tx.read_ref(id).await);
                same(
                    &mut failures,
                    &format!("{at} readRef"),
                    read_json(read, ref_json),
                    &json!({"error": "not_found"}),
                );
                same(
                    &mut failures,
                    &format!("{at} compose"),
                    read_json(engine.compose(id).await, tree_json),
                    &json!({"error": "not_found"}),
                );
                for kind in &kinds {
                    let rows = transact!(storage, |tx| tx.rows(kind, id).await).expect("rows");
                    same(
                        &mut failures,
                        &format!("{at} rows {kind}"),
                        Value::Array(rows),
                        &json!([]),
                    );
                }
            }
            for c in other["commits"].as_array().expect("commits") {
                let id = c["id"].as_str().expect("an id");
                let at = format!("{theirs} commit {id} through {name}");
                let read = transact!(storage, |tx| tx.read_commit(id).await);
                same(
                    &mut failures,
                    &format!("{at} readCommit"),
                    read_json(read, commit_json),
                    &json!({"error": "not_found"}),
                );
                same(
                    &mut failures,
                    &format!("{at} materialize"),
                    read_json(engine.materialize(id).await, tree_json),
                    &json!({"error": "not_found"}),
                );
                let patches =
                    transact!(storage, |tx| tx.patches(&[id.to_owned()]).await).expect("patches");
                assert!(patches.is_empty(), "{at} patches: {patches:?}");
                let entries = transact!(storage, |tx| tx.snapshot(id).await).expect("snapshot");
                assert!(entries.is_empty(), "{at} snapshot: {entries:?}");
            }
            for image in other["images"].as_array().expect("images") {
                let (kind, id) = (
                    image["kind"].as_str().expect("a kind"),
                    image["id"].as_str().expect("an id"),
                );
                let pin = Pin {
                    id: id.to_owned(),
                    version: image["version"].as_i64().expect("a version"),
                };
                let got = transact!(storage, |tx| tx.images(kind, &[pin]).await).expect("images");
                assert!(
                    got.is_empty(),
                    "{theirs} image {kind} {id} through {name}: {got:?}"
                );
            }
        }
    }
    assert!(
        failures.is_empty(),
        "{} reads differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}
