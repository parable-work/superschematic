//! The SQLite storage adapter (D32): [`Storage`] and [`Tx`] over one fixed
//! layout of tables, the same for every graph, so the engine keeps a graph
//! in a SQLite file. It is the Rust port of the TypeScript package's
//! `sqlite.ts`, statement for statement, and stores what it does, so a file
//! one writes the other reads. [`crate::postgres`] is its Postgres
//! counterpart, over the tables sqlgen generates per graph.
//!
//! The layout is nine `STRICT` tables: `ref`, `ref_history`, `commit`,
//! `patch`, `snapshot_entry`, `release`, `release_history`, `member` and
//! `member_history`, each named by a function the caller gives
//! ([`default_table_name`] puts `graph_` before each), as is every index.
//! Every row carries its graph's name, so one file holds several graphs,
//! and every statement is scoped to the adapter's graph. An id is `TEXT`
//! holding a UUID in its canonical form (base62); a version, a sequence and
//! a tombstone are `INTEGER`; the times of refs, commits, release pointers
//! and history images are `INTEGER` microseconds since the Unix epoch,
//! returned as canonical date-times. A member row holds its kind and its
//! role columns as columns and every other column of the descriptor as one
//! canonical JSON object, `data`. Foreign keys check every edge inside the
//! layout; there is no root table, so nothing checks a root but the
//! adapter, which refuses a write whose ref or commit is another graph's or
//! another root's.
//!
//! SQLite has no triggers that can assign `NEW`, so the adapter does what
//! Postgres's triggers do, in the statements of the transaction that
//! changes a row: it sets `_version` (1 on an insert, the old version plus
//! 1 on an update), writes the row's image at its new version on an insert
//! or an update, and on a delete writes the row's image at the old version
//! plus 1 with the kind's history actor column set to the delete's actor.
//! An image leaves out the kind's history-excluded columns, and reads back
//! as it was stored, while a live row reads with every column its kind
//! declares. Refs and release pointers keep history too. Every value it
//! writes is canonicalized by its class first ([`crate::canonical`]), so a
//! read returns what is stored and needs no rules of its own.
//!
//! The adapter reaches SQLite through [`Client`], a two-trait seam as
//! [`crate::postgres::Client`] is; `Rusqlite` binds rusqlite (the
//! `rusqlite` feature, off by default). A transaction begins with
//! `BEGIN IMMEDIATE`, which takes the file's write lock, and inside a
//! transaction the caller holds on the connection it is a savepoint. Either
//! way one writer holds the file, so [`Tx::lock_ref`] reads a ref as
//! [`Tx::read_ref`] does, [`Tx::next_sequence`] reads the root's highest
//! sequence plus 1, and [`Tx::sweep_lock`] is true.

mod client;

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::fmt;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::Value;

use crate::canonical;
use crate::storage::{
    Commit, CommitNode, NewCommit, NewRef, Patch, Pin, Ref, RefUpdate, Release, ReleaseWrite,
    RowWrite, SnapshotEntry, Storage, Tx,
};
use crate::Error;
#[cfg(feature = "rusqlite")]
pub use client::Rusqlite;
pub use client::{Client, ClientError, Conn, SqlValue};

/// `SQLITE_BUSY`: another connection holds the lock past the busy timeout.
pub const SQLITE_BUSY: i32 = 5;

/// `SQLITE_CONSTRAINT_UNIQUE`: a unique index refused a row.
pub const SQLITE_CONSTRAINT_UNIQUE: i32 = 2067;

/// The oldest SQLite the layout and the statements run on, as
/// `(major, minor, patch)`: 3.38.0, from which `json_each` and
/// `json_extract` are part of SQLite itself rather than an extension a
/// build may leave out. The layout's `STRICT` tables need 3.37.0, and
/// `RETURNING` 3.35.0.
pub const MIN_SQLITE_VERSION: (u32, u32, u32) = (3, 38, 0);

/// The local names of the layout's tables, in the order the layout creates
/// them.
pub const TABLES: [&str; 9] = [
    "ref",
    "ref_history",
    "commit",
    "patch",
    "snapshot_entry",
    "release",
    "release_history",
    "member",
    "member_history",
];

/// Names a table or an index of the layout from its local name (`"ref"`,
/// `"member_entity"`).
pub type TableName = Arc<dyn Fn(&str) -> String + Send + Sync>;

/// The time a transaction writes, in whole microseconds since the Unix
/// epoch.
pub type Clock = Arc<dyn Fn() -> i64 + Send + Sync>;

/// The default name of each table and index: `graph_` and its local name.
pub fn default_table_name(local: &str) -> String {
    format!("graph_{local}")
}

/// Configures an [`Adapter`].
#[derive(Clone, Default)]
pub struct Options {
    /// The graph's name, which every row of the graph carries. Required, and
    /// not empty.
    pub graph: String,
    /// Names each table and index; [`default_table_name`] when `None`.
    pub table_name: Option<TableName>,
    /// The time a transaction writes, in whole microseconds since the Unix
    /// epoch. A transaction reads it once, when it has begun: after
    /// `BEGIN IMMEDIATE` has taken the write lock, so times order as the
    /// writes do. `None` is the system clock.
    pub clock: Option<Clock>,
}

impl fmt::Debug for Options {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Options")
            .field("graph", &self.graph)
            .field(
                "table_name",
                &self.table_name.as_ref().map(|_| "a function"),
            )
            .field("clock", &self.clock.as_ref().map(|_| "a function"))
            .finish()
    }
}

/// The system clock in microseconds.
fn system_clock() -> i64 {
    SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map_or(0, |d| i64::try_from(d.as_micros()).unwrap_or(i64::MAX))
}

// The audit columns the adapter writes on every member row when a kind has
// them.
const CREATED_AT: &str = "created_at";
const CREATED_BY: &str = "created_by";
const UPDATED_AT: &str = "updated_at";
const UPDATED_BY: &str = "updated_by";

const MICROS_PER_DAY: i64 = 86_400_000_000;

/// Quotes an identifier.
fn quote(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// Names one table or index, refusing a name function that gives no name.
fn name_of(table_name: &dyn Fn(&str) -> String, local: &str) -> Result<String, Error> {
    let name = table_name(local);
    if name.is_empty() || name.contains('\0') {
        return Err(Error::Invalid(format!(
            "sqlite: the table name function named {local:?} {name:?}"
        )));
    }
    Ok(quote(&name))
}

/// The layout's tables, each quoted, under one name function.
#[derive(Debug)]
struct Tables {
    ref_: String,
    ref_history: String,
    commit: String,
    patch: String,
    snapshot: String,
    release: String,
    release_history: String,
    member: String,
    member_history: String,
}

impl Tables {
    fn new(table_name: &dyn Fn(&str) -> String) -> Result<Self, Error> {
        Ok(Tables {
            ref_: name_of(table_name, "ref")?,
            ref_history: name_of(table_name, "ref_history")?,
            commit: name_of(table_name, "commit")?,
            patch: name_of(table_name, "patch")?,
            snapshot: name_of(table_name, "snapshot_entry")?,
            release: name_of(table_name, "release")?,
            release_history: name_of(table_name, "release_history")?,
            member: name_of(table_name, "member")?,
            member_history: name_of(table_name, "member_history")?,
        })
    }
}

/// The statements that create the layout, each table and index under the
/// name `table_name` gives it ([`default_table_name`] for the default
/// names): one statement each, `CREATE TABLE IF NOT EXISTS` or
/// `CREATE [UNIQUE] INDEX IF NOT EXISTS`, with no trigger and no
/// transaction control, for a caller that runs its own migrations.
/// [`Adapter::create_tables`] runs them.
pub fn layout(table_name: &dyn Fn(&str) -> String) -> Result<Vec<String>, Error> {
    let t = Tables::new(table_name)?;
    let index = |local: &str| name_of(table_name, local);
    let history = |table: &str| {
        format!(
            "CREATE TABLE IF NOT EXISTS {table} (\
             history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, id TEXT NOT NULL, _version INTEGER NOT NULL, \
             operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), data TEXT NOT NULL, recorded_at INTEGER NOT NULL\
             ) STRICT"
        )
    };
    Ok(vec![
        // A ref's head and a commit's ref point at each other. A ref is
        // written before any commit of it, and its head moves to a commit
        // only once the commit is written, so both keys are checked at once,
        // not deferred.
        format!(
            "CREATE TABLE IF NOT EXISTS {ref_} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, \
             parent_ref_id TEXT REFERENCES {ref_} (id), base_commit_id TEXT REFERENCES {commit} (id), \
             head_commit_id TEXT REFERENCES {commit} (id), name TEXT NOT NULL, sealed_at INTEGER, \
             created_at INTEGER NOT NULL, created_by TEXT NOT NULL, updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, \
             deleted_at INTEGER, deleted_by TEXT, _version INTEGER NOT NULL\
             ) STRICT",
            ref_ = t.ref_,
            commit = t.commit
        ),
        // A root's live refs have distinct names: a name already taken is
        // this index's SQLITE_CONSTRAINT_UNIQUE.
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (graph, root_id, name) WHERE deleted_at IS NULL",
            index("ref_live_name")?,
            t.ref_
        ),
        history(&t.ref_history),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (id, _version)",
            index("ref_history_version")?,
            t.ref_history
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {commit} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, \
             ref_id TEXT NOT NULL REFERENCES {ref_} (id), parent_commit_id TEXT REFERENCES {commit} (id), \
             message TEXT, schema_epoch INTEGER NOT NULL, content_hash TEXT NOT NULL, sequence INTEGER, \
             created_at INTEGER NOT NULL, created_by TEXT NOT NULL\
             ) STRICT",
            ref_ = t.ref_,
            commit = t.commit
        ),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (graph, root_id, sequence)",
            index("commit_sequence")?,
            t.commit
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES {} (id), \
             entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL, \
             operation TEXT NOT NULL CHECK (operation IN ('ADD', 'UPDATE', 'DELETE'))\
             ) STRICT",
            t.patch, t.commit
        ),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (commit_id, entity_kind, entity_key)",
            index("patch_entity")?,
            t.patch
        ),
        format!(
            "CREATE INDEX IF NOT EXISTS {} ON {} (entity_id, entity_version)",
            index("patch_pin")?,
            t.patch
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, commit_id TEXT NOT NULL REFERENCES {} (id), \
             entity_kind TEXT NOT NULL, entity_key TEXT NOT NULL, entity_id TEXT NOT NULL, entity_version INTEGER NOT NULL\
             ) STRICT",
            t.snapshot, t.commit
        ),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (commit_id, entity_kind, entity_key)",
            index("snapshot_entry_entity")?,
            t.snapshot
        ),
        format!(
            "CREATE INDEX IF NOT EXISTS {} ON {} (entity_id, entity_version)",
            index("snapshot_entry_pin")?,
            t.snapshot
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, root_id TEXT NOT NULL, \
             commit_id TEXT NOT NULL REFERENCES {} (id), created_at INTEGER NOT NULL, created_by TEXT NOT NULL, \
             updated_at INTEGER NOT NULL, updated_by TEXT NOT NULL, _version INTEGER NOT NULL\
             ) STRICT",
            t.release, t.commit
        ),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (graph, root_id)",
            index("release_root")?,
            t.release
        ),
        history(&t.release_history),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (id, _version)",
            index("release_history_version")?,
            t.release_history
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, entity_key TEXT NOT NULL, \
             ref_id TEXT NOT NULL REFERENCES {} (id), root_id TEXT NOT NULL, \
             tombstone INTEGER NOT NULL CHECK (tombstone IN (0, 1)), _version INTEGER NOT NULL, data TEXT NOT NULL\
             ) STRICT",
            t.member, t.ref_
        ),
        // Unique on the entity key of a kind on a ref, declared as (graph,
        // kind, ref_id, entity_key) so it also serves a read of a ref's rows
        // of a kind.
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (graph, kind, ref_id, entity_key)",
            index("member_entity")?,
            t.member
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             history_id TEXT NOT NULL PRIMARY KEY, graph TEXT NOT NULL, kind TEXT NOT NULL, id TEXT NOT NULL, \
             _version INTEGER NOT NULL, operation TEXT NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), \
             data TEXT NOT NULL, recorded_at INTEGER NOT NULL\
             ) STRICT",
            t.member_history
        ),
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (id, _version)",
            index("member_history_version")?,
            t.member_history
        ),
        format!(
            "CREATE INDEX IF NOT EXISTS {} ON {} (graph, kind, recorded_at)",
            index("member_history_recorded")?,
            t.member_history
        ),
    ])
}

/// A time in whole microseconds since the Unix epoch as a canonical
/// date-time: UTC with `Z`, its fraction of a second without trailing zeros
/// and left out when zero. A year outside 0000-9999 is refused.
pub fn micros_to_date_time(micros: i64) -> Result<String, Error> {
    canonical::date_time_of_micros(micros).map_err(|e| Error::Invalid(format!("sqlite: {e}")))
}

/// Parses SQLite's version text (`3.46.1`) as `(major, minor, patch)`.
fn parse_version(text: &str) -> Option<(u32, u32, u32)> {
    let mut parts = text.trim().split('.');
    let major = parts.next()?.parse().ok()?;
    let minor = parts.next()?.parse().ok()?;
    let patch = match parts.next() {
        Some(patch) => patch.parse().ok()?,
        None => 0,
    };
    Some((major, minor, patch))
}

/// Refuses a SQLite older than [`MIN_SQLITE_VERSION`], with a clear error.
async fn check_version<C: Client + ?Sized>(client: &C) -> Result<(), Error> {
    let rows = client
        .exec("SELECT sqlite_version()")
        .await
        .map_err(|e| failed("read the SQLite version", e))?;
    let text = match rows.first().and_then(|row| row.first()) {
        Some(SqlValue::Text(text)) => text.clone(),
        other => {
            return Err(Error::Invalid(format!(
                "sqlite: sqlite_version() returned {other:?}, not a version"
            )))
        }
    };
    let Some(version) = parse_version(&text) else {
        return Err(Error::Invalid(format!(
            "sqlite: sqlite_version() returned {text:?}, not a version"
        )));
    };
    if version < MIN_SQLITE_VERSION {
        let (major, minor, patch) = MIN_SQLITE_VERSION;
        return Err(Error::Invalid(format!(
            "sqlite: the connection runs SQLite {text}, and the adapter needs {major}.{minor}.{patch} or later \
             (STRICT tables, RETURNING, and json_each and json_extract built in)"
        )));
    }
    Ok(())
}

/// SQLite's extended result code of an error the adapter or its client
/// returned (`SQLITE_BUSY` is 5), when SQLite gave one.
pub fn result_code(error: &Error) -> Option<i32> {
    match error {
        Error::Storage(error) => error.downcast_ref::<ClientError>().and_then(|e| e.code),
        _ => None,
    }
}

/// A client's error, with what the adapter was doing, keeping SQLite's
/// result code so callers can still tell it apart ([`result_code`]).
fn failed(what: &str, error: ClientError) -> Error {
    Error::Storage(Box::new(ClientError {
        code: error.code,
        source: format!("sqlite: {what}: {}", error.source).into(),
    }))
}

/// One member kind's role columns, columns and history.
#[derive(Debug)]
struct Kind {
    name: String,
    key: String,
    id: String,
    ref_column: String,
    root: String,
    tombstone: String,
    version: String,
    columns: BTreeMap<String, String>,
    /// Every declared column but the role ones: what `data` holds, in name
    /// order.
    data: Vec<String>,
    exclude: BTreeSet<String>,
    actor: Option<String>,
    retention_days: Option<i64>,
}

impl Kind {
    fn is_role(&self, column: &str) -> bool {
        [
            &self.id,
            &self.version,
            &self.ref_column,
            &self.root,
            &self.tombstone,
            &self.key,
        ]
        .iter()
        .any(|role| role.as_str() == column)
    }

    fn class(&self, column: &str) -> Result<&str, Error> {
        self.columns.get(column).map(String::as_str).ok_or_else(|| {
            Error::Invalid(format!(
                "sqlite: the {} kind has no column {column:?}",
                self.name
            ))
        })
    }
}

/// What the adapter reads of a graph descriptor.
#[derive(Deserialize)]
struct Descriptor {
    #[serde(default)]
    version: i64,
    #[serde(default)]
    kinds: Vec<DescriptorKind>,
}

#[derive(Deserialize)]
struct DescriptorKind {
    kind: String,
    #[serde(default)]
    key: String,
    #[serde(default)]
    id: String,
    #[serde(default, rename = "ref")]
    ref_column: String,
    #[serde(default)]
    root: String,
    #[serde(default)]
    tombstone: String,
    #[serde(default)]
    version: String,
    #[serde(default)]
    history: Option<DescriptorHistory>,
    #[serde(default)]
    columns: BTreeMap<String, String>,
}

#[derive(Deserialize)]
struct DescriptorHistory {
    #[serde(default, rename = "retentionDays")]
    retention_days: Option<i64>,
    #[serde(default)]
    exclude: Option<Vec<String>>,
    #[serde(default)]
    actor: Option<String>,
}

/// One graph's statements over the layout, built from its descriptor. Bind a
/// [`Client`] with [`Adapter::storage`].
pub struct Adapter {
    graph: String,
    tables: Tables,
    layout: Vec<String>,
    clock: Clock,
    kinds: HashMap<String, Kind>,
}

impl fmt::Debug for Adapter {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Adapter")
            .field("graph", &self.graph)
            .field("tables", &self.tables)
            .field("kinds", &self.kinds)
            .finish_non_exhaustive()
    }
}

impl Adapter {
    /// Reads a graph's descriptor (version 3). The adapter reads only its
    /// kinds: each kind's role columns (the root among them), its columns'
    /// value classes and its history; the tables the descriptor names are
    /// the Postgres adapter's.
    pub fn new(descriptor: &str, options: Options) -> Result<Self, Error> {
        let d: Descriptor = serde_json::from_str(descriptor)
            .map_err(|e| Error::Invalid(format!("sqlite: read the descriptor: {e}")))?;
        if d.version != 3 {
            return Err(Error::Invalid(format!(
                "sqlite: descriptor version {}; this adapter reads version 3",
                d.version
            )));
        }
        if options.graph.is_empty() {
            return Err(Error::Invalid(
                "sqlite: an adapter needs its graph's name (Options::graph)".to_owned(),
            ));
        }
        let table_name: TableName = options
            .table_name
            .unwrap_or_else(|| Arc::new(default_table_name));
        let mut kinds = HashMap::new();
        for k in d.kinds {
            for (role, column) in [
                ("key", &k.key),
                ("id", &k.id),
                ("ref", &k.ref_column),
                ("root", &k.root),
                ("tombstone", &k.tombstone),
                ("version", &k.version),
            ] {
                if column.is_empty() {
                    return Err(Error::Invalid(format!(
                        "sqlite: kind {:?} has no {role}",
                        k.kind
                    )));
                }
                if !k.columns.contains_key(column) {
                    return Err(Error::Invalid(format!(
                        "sqlite: kind {:?} {role} column {column:?} is not in its columns",
                        k.kind
                    )));
                }
            }
            if k.columns[&k.tombstone] != canonical::BOOLEAN
                || k.columns[&k.version] != canonical::INTEGER
            {
                return Err(Error::Invalid(format!(
                    "sqlite: kind {:?}: a tombstone is a boolean column and a version an integer one",
                    k.kind
                )));
            }
            let Some(DescriptorHistory {
                retention_days,
                exclude: Some(exclude),
                actor,
            }) = k.history
            else {
                return Err(Error::Invalid(format!(
                    "sqlite: kind {:?} has no history",
                    k.kind
                )));
            };
            if let Some(actor) = &actor {
                if !k.columns.contains_key(actor) {
                    return Err(Error::Invalid(format!(
                        "sqlite: kind {:?} history actor column {actor:?} is not in its columns",
                        k.kind
                    )));
                }
            }
            let mut kind = Kind {
                name: k.kind.clone(),
                key: k.key,
                id: k.id,
                ref_column: k.ref_column,
                root: k.root,
                tombstone: k.tombstone,
                version: k.version,
                columns: k.columns,
                data: Vec::new(),
                exclude: exclude.into_iter().collect(),
                actor,
                retention_days,
            };
            kind.data = kind
                .columns
                .keys()
                .filter(|column| !kind.is_role(column))
                .cloned()
                .collect();
            kinds.insert(k.kind, kind);
        }
        Ok(Adapter {
            graph: options.graph,
            tables: Tables::new(table_name.as_ref())?,
            layout: layout(table_name.as_ref())?,
            clock: options.clock.unwrap_or_else(|| Arc::new(system_clock)),
            kinds,
        })
    }

    /// The statements that create the layout under the adapter's names
    /// ([`layout`]).
    pub fn layout(&self) -> &[String] {
        &self.layout
    }

    /// Creates the layout's tables and indexes where they are missing, in
    /// one transaction of the client: its own on the connection, or a
    /// savepoint in the caller's. It refuses a SQLite older than
    /// [`MIN_SQLITE_VERSION`] first.
    pub async fn create_tables<C: Client + ?Sized>(&self, client: &C) -> Result<(), Error> {
        check_version(client).await?;
        let mut conn = client.begin().await.map_err(|e| failed("begin", e))?;
        for statement in &self.layout {
            if let Err(error) = conn.execute(statement, &[]).await {
                // The statement's error is the one to report.
                let _ = conn.rollback().await;
                return Err(failed("create the layout", error));
            }
        }
        conn.commit().await.map_err(|e| failed("commit", e))
    }

    /// Binds the adapter to a client. It refuses a SQLite older than
    /// [`MIN_SQLITE_VERSION`], and turns the connection's foreign keys on,
    /// which SQLite ignores inside a transaction, so bind it outside one: a
    /// connection whose foreign keys stay off is refused.
    pub async fn storage<C: Client + 'static>(
        self: &Arc<Self>,
        client: C,
    ) -> Result<SqliteStorage<C>, Error> {
        check_version(&client).await?;
        client
            .exec("PRAGMA foreign_keys = ON")
            .await
            .map_err(|e| failed("turn foreign keys on", e))?;
        let rows = client
            .exec("PRAGMA foreign_keys")
            .await
            .map_err(|e| failed("read foreign keys", e))?;
        if !matches!(
            rows.first().and_then(|row| row.first()),
            Some(SqlValue::Int(1))
        ) {
            return Err(Error::Invalid(
                "sqlite: the connection's foreign keys would not turn on; bind the adapter outside a transaction"
                    .to_owned(),
            ));
        }
        Ok(SqliteStorage {
            adapter: Arc::clone(self),
            client,
        })
    }

    fn kind(&self, name: &str) -> Result<&Kind, Error> {
        self.kinds
            .get(name)
            .ok_or_else(|| Error::Invalid(format!("sqlite: unknown kind {name:?}")))
    }

    /// The SQL that tells whether the commit whose id is `id_sql` has a
    /// snapshot.
    fn has_snapshot(&self, id_sql: &str) -> String {
        format!(
            "EXISTS (SELECT 1 FROM {} AS s WHERE s.commit_id = {id_sql})",
            self.tables.snapshot
        )
    }
}

/// An [`Adapter`] bound to a [`Client`]: the graph's [`Storage`].
pub struct SqliteStorage<C> {
    adapter: Arc<Adapter>,
    client: C,
}

impl<C> SqliteStorage<C> {
    /// The bound client.
    pub fn client(&self) -> &C {
        &self.client
    }

    /// The adapter.
    pub fn adapter(&self) -> &Arc<Adapter> {
        &self.adapter
    }
}

#[async_trait]
impl<C: Client + 'static> Storage for SqliteStorage<C> {
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
        let conn = self.client.begin().await.map_err(|e| failed("begin", e))?;
        // Read once the transaction holds the write lock, so times order as
        // the writes do.
        let time = (self.adapter.clock)();
        Ok(Box::new(SqliteTx {
            a: &self.adapter,
            conn,
            time,
        }))
    }
}

/// One transaction's view of the graph, at the transaction's time.
struct SqliteTx<'a> {
    a: &'a Adapter,
    conn: Box<dyn Conn + 'a>,
    time: i64,
}

// The columns each table's reads select, in the layout's names.
const REF_COLUMNS: &str = "id, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, created_at, created_by, \
     updated_at, updated_by, deleted_at, deleted_by, _version";
const COMMIT_COLUMNS: &str =
    "c.id, c.root_id, c.ref_id, c.parent_commit_id, c.message, c.schema_epoch, c.content_hash, \
     c.sequence, c.created_at, c.created_by";
const RELEASE_COLUMNS: &str =
    "id, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version";
const MEMBER_COLUMNS: &str = "id, entity_key, ref_id, root_id, tombstone, _version, data";

/// Reads one row's columns, by position.
struct Cells<'r> {
    cells: std::slice::Iter<'r, SqlValue>,
    columns: std::slice::Iter<'r, &'static str>,
}

impl<'r> Cells<'r> {
    fn new(row: &'r [SqlValue], columns: &'r [&'static str]) -> Self {
        Cells {
            cells: row.iter(),
            columns: columns.iter(),
        }
    }

    fn next(&mut self) -> Result<(&'r SqlValue, &'static str), Error> {
        let column = self.columns.next().copied().unwrap_or("a column");
        let cell = self
            .cells
            .next()
            .ok_or_else(|| Error::Invalid("sqlite: a row has too few columns".to_owned()))?;
        Ok((cell, column))
    }

    fn optional_text(&mut self) -> Result<Option<String>, Error> {
        match self.next()? {
            (SqlValue::Null, _) => Ok(None),
            (SqlValue::Text(s), _) => Ok(Some(s.clone())),
            (other, column) => Err(Error::Invalid(format!(
                "sqlite: column {column} is {other:?}, not text"
            ))),
        }
    }

    fn text(&mut self) -> Result<String, Error> {
        match self.next()? {
            (SqlValue::Text(s), _) => Ok(s.clone()),
            (other, column) => Err(Error::Invalid(format!(
                "sqlite: column {column} is {other:?}, not text"
            ))),
        }
    }

    fn optional_int(&mut self) -> Result<Option<i64>, Error> {
        match self.next()? {
            (SqlValue::Null, _) => Ok(None),
            (SqlValue::Int(n), _) => Ok(Some(*n)),
            (other, column) => Err(Error::Invalid(format!(
                "sqlite: column {column} is {other:?}, not an integer"
            ))),
        }
    }

    fn int(&mut self) -> Result<i64, Error> {
        match self.next()? {
            (SqlValue::Int(n), _) => Ok(*n),
            (other, column) => Err(Error::Invalid(format!(
                "sqlite: column {column} is {other:?}, not an integer"
            ))),
        }
    }
}

const REF_NAMES: [&str; 14] = [
    "id",
    "root_id",
    "parent_ref_id",
    "base_commit_id",
    "head_commit_id",
    "name",
    "sealed_at",
    "created_at",
    "created_by",
    "updated_at",
    "updated_by",
    "deleted_at",
    "deleted_by",
    "_version",
];

/// A ref row as the layout holds it.
struct RefRow {
    id: String,
    root: String,
    parent: Option<String>,
    base: Option<String>,
    head: Option<String>,
    name: String,
    sealed_at: Option<i64>,
    created_at: i64,
    created_by: String,
    updated_at: i64,
    updated_by: String,
    deleted_at: Option<i64>,
    deleted_by: Option<String>,
    version: i64,
}

fn scan_ref_row(row: &[SqlValue]) -> Result<RefRow, Error> {
    let mut c = Cells::new(row, &REF_NAMES);
    Ok(RefRow {
        id: c.text()?,
        root: c.text()?,
        parent: c.optional_text()?,
        base: c.optional_text()?,
        head: c.optional_text()?,
        name: c.text()?,
        sealed_at: c.optional_int()?,
        created_at: c.int()?,
        created_by: c.text()?,
        updated_at: c.int()?,
        updated_by: c.text()?,
        deleted_at: c.optional_int()?,
        deleted_by: c.optional_text()?,
        version: c.int()?,
    })
}

impl RefRow {
    fn to_ref(&self) -> Ref {
        Ref {
            id: self.id.clone(),
            root: self.root.clone(),
            parent: self.parent.clone(),
            base: self.base.clone(),
            head: self.head.clone(),
            name: self.name.clone(),
            sealed: self.sealed_at.is_some(),
            discarded: self.deleted_at.is_some(),
            version: self.version,
        }
    }

    /// The ref's history image: each of its columns, an id in its canonical
    /// form and a time as a canonical date-time.
    fn image(&self) -> Result<String, Error> {
        let mut image = BTreeMap::new();
        for (column, value) in [
            ("id", Some(&self.id)),
            ("root_id", Some(&self.root)),
            ("parent_ref_id", self.parent.as_ref()),
            ("base_commit_id", self.base.as_ref()),
            ("head_commit_id", self.head.as_ref()),
            ("name", Some(&self.name)),
            ("created_by", Some(&self.created_by)),
            ("updated_by", Some(&self.updated_by)),
            ("deleted_by", self.deleted_by.as_ref()),
        ] {
            image.insert(column.to_owned(), json_text(value.map(String::as_str)));
        }
        for (column, value) in [
            ("sealed_at", self.sealed_at),
            ("created_at", Some(self.created_at)),
            ("updated_at", Some(self.updated_at)),
            ("deleted_at", self.deleted_at),
        ] {
            image.insert(column.to_owned(), json_time(value)?);
        }
        image.insert("_version".to_owned(), self.version.to_string());
        Ok(write_object(&image))
    }
}

const RELEASE_NAMES: [&str; 8] = [
    "id",
    "root_id",
    "commit_id",
    "created_at",
    "created_by",
    "updated_at",
    "updated_by",
    "_version",
];

/// A release pointer's row, and its history image, as [`RefRow::image`]
/// writes a ref's.
fn scan_release(row: &[SqlValue]) -> Result<(Release, String), Error> {
    let mut c = Cells::new(row, &RELEASE_NAMES);
    let (id, root, commit) = (c.text()?, c.text()?, c.text()?);
    let (created_at, created_by) = (c.int()?, c.text()?);
    let (updated_at, updated_by) = (c.int()?, c.text()?);
    let version = c.int()?;
    let mut image = BTreeMap::new();
    for (column, value) in [
        ("id", &id),
        ("root_id", &root),
        ("commit_id", &commit),
        ("created_by", &created_by),
        ("updated_by", &updated_by),
    ] {
        image.insert(column.to_owned(), json_text(Some(value)));
    }
    image.insert("created_at".to_owned(), json_time(Some(created_at))?);
    image.insert("updated_at".to_owned(), json_time(Some(updated_at))?);
    image.insert("_version".to_owned(), version.to_string());
    let release = Release {
        id,
        root,
        commit,
        version,
    };
    Ok((release, write_object(&image)))
}

const COMMIT_NAMES: [&str; 11] = [
    "id",
    "root_id",
    "ref_id",
    "parent_commit_id",
    "message",
    "schema_epoch",
    "content_hash",
    "sequence",
    "created_at",
    "created_by",
    "snapshot",
];

fn scan_commit(row: &[SqlValue]) -> Result<Commit, Error> {
    let mut c = Cells::new(row, &COMMIT_NAMES);
    Ok(Commit {
        id: c.text()?,
        root: c.text()?,
        ref_id: c.text()?,
        parent: c.optional_text()?,
        message: c.optional_text()?.unwrap_or_default(),
        schema_epoch: c.int()?,
        content_hash: c.text()?,
        sequence: c.optional_int()?,
        created_at: micros_to_date_time(c.int()?)?,
        created_by: c.text()?,
        snapshot: c.int()? == 1,
    })
}

const MEMBER_NAMES: [&str; 7] = [
    "id",
    "entity_key",
    "ref_id",
    "root_id",
    "tombstone",
    "_version",
    "data",
];

/// A member row as the layout holds it: its role columns, and its other
/// columns as JSON text.
struct Member {
    id: String,
    key: String,
    ref_id: String,
    root: String,
    tombstone: bool,
    version: i64,
    data: BTreeMap<String, String>,
}

fn scan_member(row: &[SqlValue]) -> Result<Member, Error> {
    let mut c = Cells::new(row, &MEMBER_NAMES);
    Ok(Member {
        id: c.text()?,
        key: c.text()?,
        ref_id: c.text()?,
        root: c.text()?,
        tombstone: c.int()? == 1,
        version: c.int()?,
        data: read_object(&c.text()?, "data")?,
    })
}

/// A member's canonical row's members: its role columns under the
/// descriptor's names, and every other column the kind declares, null where
/// the stored row lacks it, as a Postgres row has a column added after it
/// was written. A stored column the kind no longer declares is kept as
/// stored.
fn member_members(k: &Kind, m: &Member) -> BTreeMap<String, String> {
    let mut members = m.data.clone();
    for column in &k.data {
        members
            .entry(column.clone())
            .or_insert_with(|| "null".to_owned());
    }
    members.insert(k.id.clone(), canonical::string_text(&m.id));
    members.insert(k.key.clone(), canonical::string_text(&m.key));
    members.insert(k.ref_column.clone(), canonical::string_text(&m.ref_id));
    members.insert(k.root.clone(), canonical::string_text(&m.root));
    members.insert(
        k.tombstone.clone(),
        if m.tombstone { "true" } else { "false" }.to_owned(),
    );
    members.insert(k.version.clone(), m.version.to_string());
    members
}

/// Writes a JSON object of members, each already JSON text, sorted by name.
fn write_object(members: &BTreeMap<String, String>) -> String {
    let mut out = String::from("{");
    for (i, (name, value)) in members.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        out.push_str(&canonical::string_text(name));
        out.push(':');
        out.push_str(value);
    }
    out.push('}');
    out
}

/// Reads a JSON object column, each member as its JSON text, as stored.
fn read_object(json: &str, column: &str) -> Result<BTreeMap<String, String>, Error> {
    let members: BTreeMap<String, Box<RawValue>> = serde_json::from_str(json).map_err(|e| {
        Error::Invalid(format!(
            "sqlite: column {column} does not hold a JSON object: {e}"
        ))
    })?;
    Ok(members
        .into_iter()
        .map(|(name, value)| (name, value.get().to_owned()))
        .collect())
}

/// Reads JSON text the adapter wrote or read as a value, keeping each
/// number's digits.
fn value_of(json: &str) -> Result<Value, Error> {
    serde_json::from_str(json)
        .map_err(|e| Error::Invalid(format!("sqlite: reread a stored row: {e}")))
}

/// The JSON text of an optional string.
fn json_text(value: Option<&str>) -> String {
    value.map_or_else(|| "null".to_owned(), canonical::string_text)
}

/// The JSON text of an optional time, as a canonical date-time.
fn json_time(micros: Option<i64>) -> Result<String, Error> {
    match micros {
        Some(micros) => Ok(canonical::string_text(&micros_to_date_time(micros)?)),
        None => Ok("null".to_owned()),
    }
}

/// A new id: a version-4 UUID in its canonical form.
fn new_id() -> Result<String, Error> {
    let mut bytes = [0u8; 16];
    getrandom::fill(&mut bytes)
        .map_err(|e| Error::storage(format!("sqlite: generate an id: {e}")))?;
    bytes[6] = (bytes[6] & 0x0f) | 0x40;
    bytes[8] = (bytes[8] & 0x3f) | 0x80;
    let hex = format!("{:032x}", u128::from_be_bytes(bytes));
    let hyphenated = format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    );
    Ok(canonical::uuid(&hyphenated)?)
}

/// The canonical text of a value of a column's class, with the column named
/// in its error.
fn canonical_of(k: &Kind, column: &str, value: &Value) -> Result<String, Error> {
    let class = k.class(column)?;
    canonical::postgres_value(class, value).map_err(|mut e| {
        e.column = column.to_owned();
        e.message = format!("{} row: {}", k.name, e.message);
        Error::Canonical(e)
    })
}

/// The canonical value of a column's class, given as a string and decoded:
/// an id, a key, an actor.
fn role_value(k: &Kind, column: &str, value: &str) -> Result<String, Error> {
    let text = canonical_of(k, column, &Value::String(value.to_owned()))?;
    match value_of(&text)? {
        Value::String(s) => Ok(s),
        _ => Err(Error::Invalid(format!(
            "sqlite: the {} column {column} holds a string",
            k.name
        ))),
    }
}

/// A duration in whole microseconds.
fn micros(d: Duration) -> i64 {
    i64::try_from(d.as_micros()).unwrap_or(i64::MAX)
}

/// A JSON array of strings, as canonical JSON writes it.
fn string_list(items: &[String]) -> String {
    let texts: Vec<String> = items.iter().map(|s| canonical::string_text(s)).collect();
    format!("[{}]", texts.join(","))
}

impl SqliteTx<'_> {
    async fn query(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, Error> {
        self.conn
            .query(sql, args)
            .await
            .map_err(|e| failed(what, e))
    }

    async fn first(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Option<Vec<SqlValue>>, Error> {
        Ok(self.query(what, sql, args).await?.into_iter().next())
    }

    async fn execute(&mut self, what: &str, sql: &str, args: &[SqlValue]) -> Result<u64, Error> {
        self.conn
            .execute(sql, args)
            .await
            .map_err(|e| failed(what, e))
    }

    fn graph(&self) -> SqlValue {
        SqlValue::Text(self.a.graph.clone())
    }

    /// Refuses a write whose ref is not one of this graph's refs of `root`.
    async fn require_ref(
        &mut self,
        what: &str,
        id: &str,
        root: &str,
        role: &str,
    ) -> Result<(), Error> {
        let sql = format!(
            "SELECT 1 AS found FROM {} WHERE graph = ?1 AND id = ?2 AND root_id = ?3",
            self.a.tables.ref_
        );
        let args = [self.graph(), id.into(), root.into()];
        if self.first(what, &sql, &args).await?.is_none() {
            return Err(Error::Invalid(format!(
                "sqlite: {what}: {role}: ref {id} is not a ref of root {root} in graph {}",
                self.a.graph
            )));
        }
        Ok(())
    }

    /// Refuses a write whose commit is not one of this graph's commits of
    /// `root`.
    async fn require_commit(
        &mut self,
        what: &str,
        id: &str,
        root: &str,
        role: &str,
    ) -> Result<(), Error> {
        let sql = format!(
            "SELECT 1 AS found FROM {} WHERE graph = ?1 AND id = ?2 AND root_id = ?3",
            self.a.tables.commit
        );
        let args = [self.graph(), id.into(), root.into()];
        if self.first(what, &sql, &args).await?.is_none() {
            return Err(Error::Invalid(format!(
                "sqlite: {what}: {role}: commit {id} is not a commit of root {root} in graph {}",
                self.a.graph
            )));
        }
        Ok(())
    }

    /// Refuses a commit of another graph, whatever its root, before writing
    /// its patches or its snapshot.
    async fn require_own_commit(&mut self, what: &str, id: &str, role: &str) -> Result<(), Error> {
        let sql = format!(
            "SELECT 1 AS found FROM {} WHERE graph = ?1 AND id = ?2",
            self.a.tables.commit
        );
        let args = [self.graph(), id.into()];
        if self.first(what, &sql, &args).await?.is_none() {
            return Err(Error::Invalid(format!(
                "sqlite: {what}: {role}: commit {id} is not a commit of graph {}",
                self.a.graph
            )));
        }
        Ok(())
    }

    /// Writes a ref's or a release pointer's history image.
    async fn history(
        &mut self,
        what: &str,
        table: &str,
        id: &str,
        version: i64,
        operation: &str,
        data: String,
    ) -> Result<(), Error> {
        let sql = format!(
            "INSERT INTO {table} (history_id, graph, id, _version, operation, data, recorded_at) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)"
        );
        let args = [
            new_id()?.into(),
            self.graph(),
            id.into(),
            version.into(),
            operation.into(),
            data.into(),
            self.time.into(),
        ];
        self.execute(what, &sql, &args).await?;
        Ok(())
    }

    /// Writes a member's history image: its canonical row's members less the
    /// kind's history-excluded columns.
    async fn member_history(
        &mut self,
        what: &str,
        k: &Kind,
        id: &str,
        version: i64,
        operation: &str,
        members: &BTreeMap<String, String>,
    ) -> Result<(), Error> {
        let mut image = members.clone();
        for column in &k.exclude {
            image.remove(column);
        }
        let sql = format!(
            "INSERT INTO {} (history_id, graph, kind, id, _version, operation, data, recorded_at) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            self.a.tables.member_history
        );
        let args = [
            new_id()?.into(),
            self.graph(),
            k.name.as_str().into(),
            id.into(),
            version.into(),
            operation.into(),
            write_object(&image).into(),
            self.time.into(),
        ];
        self.execute(what, &sql, &args).await?;
        Ok(())
    }

    async fn members(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Member>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.iter().map(|row| scan_member(row)).collect()
    }

    async fn refs(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Ref>, Error> {
        let rows = self.query("read refs", sql, args).await?;
        rows.iter()
            .map(|row| scan_ref_row(row).map(|r| r.to_ref()))
            .collect()
    }

    async fn commits_of(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Commit>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.iter().map(|row| scan_commit(row)).collect()
    }

    async fn upsert(&mut self, k: &Kind, write: RowWrite) -> Result<Value, Error> {
        let what = format!("write {} row", k.name);
        let Value::Object(row) = &write.row else {
            return Err(Error::Invalid(format!(
                "sqlite: {what}: a {} row is a JSON object",
                k.name
            )));
        };
        // The row's other columns, each canonical. The adapter writes the
        // ref, the root and the tombstone, and never the id or the version.
        let mut given = BTreeMap::new();
        for (column, value) in row {
            if !k.columns.contains_key(column) {
                return Err(Error::Invalid(format!(
                    "sqlite: {what}: the {} row has column {column:?}, which its descriptor does not declare",
                    k.name
                )));
            }
            if k.is_role(column) {
                continue;
            }
            given.insert(column.clone(), canonical_of(k, column, value)?);
        }
        let key = match row.get(&k.key) {
            Some(value) if !value.is_null() => match value_of(&canonical_of(k, &k.key, value)?)? {
                Value::String(key) => Some(key),
                _ => {
                    return Err(Error::Invalid(format!(
                        "sqlite: {what}: the {} row's entity key is a string",
                        k.name
                    )))
                }
            },
            _ => None,
        };
        let ref_id = role_value(k, &k.ref_column, &write.ref_id)?;
        let root = role_value(k, &k.root, &write.root)?;
        self.require_ref(&what, &ref_id, &root, "the row's ref")
            .await?;
        let time = Value::String(micros_to_date_time(self.time)?);
        let actor = Value::String(write.actor.clone());
        let member_table = self.a.tables.member.clone();
        let existing = match &key {
            None => None,
            Some(key) => {
                let sql = format!(
                    "SELECT {MEMBER_COLUMNS} FROM {member_table} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4"
                );
                let args = [
                    self.graph(),
                    k.name.as_str().into(),
                    ref_id.as_str().into(),
                    key.as_str().into(),
                ];
                self.members(&what, &sql, &args).await?.into_iter().next()
            }
        };
        if let Some(existing) = existing {
            // A column the row lacks keeps its stored value; the entity key,
            // the root and the creation audit stay as the row was first
            // written.
            given.remove(CREATED_AT);
            given.remove(CREATED_BY);
            for (column, value) in [(UPDATED_AT, &time), (UPDATED_BY, &actor)] {
                if k.columns.contains_key(column) {
                    given.insert(column.to_owned(), canonical_of(k, column, value)?);
                }
            }
            let mut data = existing.data.clone();
            data.extend(given);
            // A column the kind gained after the row was written is null, as
            // a Postgres row has it.
            for column in &k.data {
                data.entry(column.clone())
                    .or_insert_with(|| "null".to_owned());
            }
            let stored = Member {
                tombstone: write.tombstone,
                version: existing.version + 1,
                data,
                ..existing
            };
            let sql = format!(
                "UPDATE {member_table} SET tombstone = ?3, _version = ?4, data = ?5 WHERE graph = ?1 AND id = ?2 AND _version = ?6"
            );
            let args = [
                self.graph(),
                stored.id.as_str().into(),
                i64::from(stored.tombstone).into(),
                stored.version.into(),
                write_object(&stored.data).into(),
                (stored.version - 1).into(),
            ];
            let changed = self.execute(&what, &sql, &args).await?;
            if changed != 1 {
                return Err(Error::Invalid(format!(
                    "sqlite: {what}: {changed} rows written"
                )));
            }
            let members = member_members(k, &stored);
            self.member_history(&what, k, &stored.id, stored.version, "UPDATE", &members)
                .await?;
            return value_of(&write_object(&members));
        }
        // A column the row lacks holds its default, null; the audit columns
        // hold the write's actor and time.
        for (column, value) in [
            (CREATED_AT, &time),
            (UPDATED_AT, &time),
            (CREATED_BY, &actor),
            (UPDATED_BY, &actor),
        ] {
            if k.columns.contains_key(column) {
                given.insert(column.to_owned(), canonical_of(k, column, value)?);
            }
        }
        let data: BTreeMap<String, String> = k
            .data
            .iter()
            .map(|column| {
                let value = given.get(column).cloned();
                (column.clone(), value.unwrap_or_else(|| "null".to_owned()))
            })
            .collect();
        let key = match key {
            Some(key) => key,
            None => role_value(k, &k.key, &new_id()?)?,
        };
        let stored = Member {
            id: role_value(k, &k.id, &new_id()?)?,
            key,
            ref_id,
            root,
            tombstone: write.tombstone,
            version: 1,
            data,
        };
        let sql = format!(
            "INSERT INTO {member_table} (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, 1, ?8)"
        );
        let args = [
            stored.id.as_str().into(),
            self.graph(),
            k.name.as_str().into(),
            stored.key.as_str().into(),
            stored.ref_id.as_str().into(),
            stored.root.as_str().into(),
            i64::from(stored.tombstone).into(),
            write_object(&stored.data).into(),
        ];
        self.execute(&what, &sql, &args).await?;
        let members = member_members(k, &stored);
        self.member_history(&what, k, &stored.id, 1, "INSERT", &members)
            .await?;
        value_of(&write_object(&members))
    }

    /// Hard-deletes a member row and writes its DELETE image: the row at its
    /// version plus 1, with the kind's history actor column, when it has
    /// one, set to `actor`.
    async fn remove(&mut self, what: &str, k: &Kind, m: &Member, actor: &str) -> Result<(), Error> {
        let sql = format!(
            "DELETE FROM {} WHERE graph = ?1 AND id = ?2",
            self.a.tables.member
        );
        let args = [self.graph(), m.id.as_str().into()];
        let changed = self.execute(what, &sql, &args).await?;
        if changed != 1 {
            return Err(Error::Invalid(format!(
                "sqlite: {what}: {changed} rows deleted"
            )));
        }
        let mut members = member_members(k, m);
        members.insert(k.version.clone(), (m.version + 1).to_string());
        if let Some(column) = &k.actor {
            let value = canonical_of(k, column, &Value::String(actor.to_owned()))?;
            members.insert(column.clone(), value);
        }
        self.member_history(what, k, &m.id, m.version + 1, "DELETE", &members)
            .await
    }

    /// An entity a patch or a snapshot entry pins: its kind's name and its
    /// key and id in their canonical forms.
    fn pinned(
        &self,
        kind: &str,
        entity_key: &str,
        entity_id: &str,
    ) -> Result<(String, String, String), Error> {
        let k = self.a.kind(kind)?;
        Ok((
            k.name.clone(),
            role_value(k, &k.key, entity_key)?,
            role_value(k, &k.id, entity_id)?,
        ))
    }
}

#[async_trait]
impl Tx for SqliteTx<'_> {
    async fn commit(self: Box<Self>) -> Result<(), Error> {
        self.conn.commit().await.map_err(|e| failed("commit", e))
    }

    async fn rollback(self: Box<Self>) -> Result<(), Error> {
        self.conn
            .rollback()
            .await
            .map_err(|e| failed("roll back", e))
    }

    async fn create_ref(&mut self, new: NewRef) -> Result<Ref, Error> {
        let what = "create ref";
        if let Some(parent) = &new.parent {
            self.require_ref(what, parent, &new.root, "the parent")
                .await?;
        }
        if let Some(base) = &new.base {
            self.require_commit(what, base, &new.root, "the base")
                .await?;
        }
        let id = new_id()?;
        let sql = format!(
            "INSERT INTO {} (id, graph, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, \
             created_at, created_by, updated_at, updated_by, deleted_at, deleted_by, _version) \
             VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, NULL, ?7, ?8, ?7, ?8, NULL, NULL, 1) RETURNING {REF_COLUMNS}",
            self.a.tables.ref_
        );
        let args = [
            id.as_str().into(),
            self.graph(),
            new.root.as_str().into(),
            new.parent.clone().into(),
            new.base.clone().into(),
            new.name.as_str().into(),
            self.time.into(),
            new.actor.as_str().into(),
        ];
        let rows = match self.conn.query(&sql, &args).await {
            Ok(rows) => rows,
            Err(e) if e.code == Some(SQLITE_CONSTRAINT_UNIQUE) => {
                return Err(Error::NameTaken(new.name))
            }
            Err(e) => return Err(failed(what, e)),
        };
        let row = rows
            .first()
            .ok_or_else(|| Error::Invalid(format!("sqlite: {what}: no row")))?;
        let row = scan_ref_row(row)?;
        let table = self.a.tables.ref_history.clone();
        self.history(what, &table, &id, 1, "INSERT", row.image()?)
            .await?;
        Ok(row.to_ref())
    }

    async fn read_ref(&mut self, id: &str) -> Result<Ref, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} WHERE graph = ?1 AND id = ?2",
            self.a.tables.ref_
        );
        let args = [self.graph(), id.into()];
        match self.first("read ref", &sql, &args).await? {
            Some(row) => Ok(scan_ref_row(&row)?.to_ref()),
            None => Err(Error::NotFound),
        }
    }

    /// `read_ref`: the one writer the file's write lock lets in orders every
    /// write, so a ref needs no lock of its own.
    async fn lock_ref(&mut self, id: &str) -> Result<Ref, Error> {
        self.read_ref(id).await
    }

    async fn update_ref(&mut self, update: RefUpdate) -> Result<Ref, Error> {
        let what = "update ref";
        let t = &self.a.tables;
        let (ref_table, history_table) = (t.ref_.clone(), t.ref_history.clone());
        if update.head.is_some() || update.base.is_some() {
            let sql = format!("SELECT root_id FROM {ref_table} WHERE graph = ?1 AND id = ?2");
            let args = [self.graph(), update.id.as_str().into()];
            let Some(row) = self.first(what, &sql, &args).await? else {
                return Err(Error::VersionConflict);
            };
            let root = Cells::new(&row, &["root_id"]).text()?;
            if let Some(head) = &update.head {
                self.require_commit(what, head, &root, "the head").await?;
            }
            if let Some(base) = &update.base {
                self.require_commit(what, base, &root, "the base").await?;
            }
        }
        let sql = format!(
            "UPDATE {ref_table} SET head_commit_id = COALESCE(?3, head_commit_id), base_commit_id = COALESCE(?4, base_commit_id), \
             sealed_at = CASE WHEN ?5 = 1 THEN ?6 ELSE sealed_at END, updated_at = ?6, updated_by = ?7, _version = _version + 1 \
             WHERE graph = ?1 AND id = ?2 AND _version = ?8 RETURNING {REF_COLUMNS}"
        );
        let args = [
            self.graph(),
            update.id.as_str().into(),
            update.head.clone().into(),
            update.base.clone().into(),
            i64::from(update.seal).into(),
            self.time.into(),
            update.actor.as_str().into(),
            update.version.into(),
        ];
        let Some(row) = self.first(what, &sql, &args).await? else {
            return Err(Error::VersionConflict);
        };
        let row = scan_ref_row(&row)?;
        self.history(
            what,
            &history_table,
            &update.id,
            row.version,
            "UPDATE",
            row.image()?,
        )
        .await?;
        Ok(row.to_ref())
    }

    async fn discard_ref(&mut self, id: &str, version: i64, actor: &str) -> Result<(), Error> {
        let what = "discard ref";
        let t = &self.a.tables;
        let (ref_table, history_table) = (t.ref_.clone(), t.ref_history.clone());
        let sql = format!(
            "UPDATE {ref_table} SET deleted_at = ?3, deleted_by = ?4, _version = _version + 1 \
             WHERE graph = ?1 AND id = ?2 AND _version = ?5 AND deleted_at IS NULL RETURNING {REF_COLUMNS}"
        );
        let args = [
            self.graph(),
            id.into(),
            self.time.into(),
            actor.into(),
            version.into(),
        ];
        let Some(row) = self.first(what, &sql, &args).await? else {
            return Err(Error::VersionConflict);
        };
        let row = scan_ref_row(&row)?;
        self.history(
            what,
            &history_table,
            id,
            row.version,
            "UPDATE",
            row.image()?,
        )
        .await
    }

    async fn rows(&mut self, kind: &str, ref_id: &str) -> Result<Vec<Value>, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        let what = format!("read {} rows", k.name);
        let sql = format!(
            "SELECT {MEMBER_COLUMNS} FROM {} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3",
            a.tables.member
        );
        let args = [self.graph(), k.name.as_str().into(), ref_id.into()];
        self.members(&what, &sql, &args)
            .await?
            .iter()
            .map(|m| value_of(&write_object(&member_members(k, m))))
            .collect()
    }

    async fn upsert_row(&mut self, kind: &str, write: RowWrite) -> Result<Value, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        self.upsert(k, write).await
    }

    async fn remove_row(
        &mut self,
        kind: &str,
        ref_id: &str,
        entity_key: &str,
        actor: &str,
    ) -> Result<bool, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        let what = format!("remove the {} row", k.name);
        let key = role_value(k, &k.key, entity_key)?;
        let sql = format!(
            "SELECT {MEMBER_COLUMNS} FROM {} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3 AND entity_key = ?4",
            a.tables.member
        );
        let args = [
            self.graph(),
            k.name.as_str().into(),
            ref_id.into(),
            key.into(),
        ];
        let found = self.members(&what, &sql, &args).await?;
        for m in &found {
            self.remove(&what, k, m, actor).await?;
        }
        Ok(!found.is_empty())
    }

    /// Each pinned image as it was stored, as a Postgres history image
    /// reads: one taken before its kind gained a column lacks it, and the
    /// core reads a content column a row lacks as null.
    async fn images(&mut self, kind: &str, pins: &[Pin]) -> Result<Vec<Value>, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        if pins.is_empty() {
            return Ok(Vec::new());
        }
        let pin_list: Vec<String> = pins
            .iter()
            .map(|pin| format!("[{},{}]", canonical::string_text(&pin.id), pin.version))
            .collect();
        let sql = format!(
            "SELECT h.data FROM json_each(?3) AS p JOIN {} AS h \
             ON h.id = json_extract(p.value, '$[0]') AND h._version = json_extract(p.value, '$[1]') \
             WHERE h.graph = ?1 AND h.kind = ?2",
            a.tables.member_history
        );
        let args = [
            self.graph(),
            k.name.as_str().into(),
            format!("[{}]", pin_list.join(",")).into(),
        ];
        let what = format!("read {} history", k.name);
        let rows = self.query(&what, &sql, &args).await?;
        rows.iter()
            .map(|row| value_of(&Cells::new(row, &["data"]).text()?))
            .collect()
    }

    async fn read_commit(&mut self, id: &str) -> Result<Commit, Error> {
        let sql = format!(
            "SELECT {COMMIT_COLUMNS}, {} AS snapshot FROM {} AS c WHERE c.graph = ?1 AND c.id = ?2",
            self.a.has_snapshot("c.id"),
            self.a.tables.commit
        );
        let args = [self.graph(), id.into()];
        match self.first("read commit", &sql, &args).await? {
            Some(row) => scan_commit(&row),
            None => Err(Error::NotFound),
        }
    }

    async fn insert_commit(&mut self, commit: NewCommit) -> Result<Commit, Error> {
        let what = "write commit";
        self.require_ref(what, &commit.ref_id, &commit.root, "the commit's ref")
            .await?;
        if let Some(parent) = &commit.parent {
            self.require_commit(what, parent, &commit.root, "the commit's parent")
                .await?;
        }
        let id = new_id()?;
        let sql = format!(
            "INSERT INTO {} (id, graph, root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, sequence, created_at, created_by) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11)",
            self.a.tables.commit
        );
        let message = (!commit.message.is_empty()).then(|| commit.message.clone());
        let args = [
            id.as_str().into(),
            self.graph(),
            commit.root.as_str().into(),
            commit.ref_id.as_str().into(),
            commit.parent.clone().into(),
            message.into(),
            commit.schema_epoch.into(),
            commit.content_hash.as_str().into(),
            commit.sequence.into(),
            self.time.into(),
            commit.actor.as_str().into(),
        ];
        self.execute(what, &sql, &args).await?;
        Ok(Commit {
            id,
            root: commit.root,
            ref_id: commit.ref_id,
            parent: commit.parent,
            message: commit.message,
            schema_epoch: commit.schema_epoch,
            content_hash: commit.content_hash,
            sequence: commit.sequence,
            created_at: micros_to_date_time(self.time)?,
            created_by: commit.actor,
            snapshot: false,
        })
    }

    async fn insert_patches(&mut self, commit: &str, patches: &[Patch]) -> Result<(), Error> {
        let what = "write patches";
        self.require_own_commit(what, commit, "the patches' commit")
            .await?;
        let sql = format!(
            "INSERT INTO {} (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version, operation) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8)",
            self.a.tables.patch
        );
        for p in patches {
            let (kind, key, id) = self.pinned(&p.kind, &p.entity_key, &p.entity_id)?;
            let args = [
                new_id()?.into(),
                self.graph(),
                commit.into(),
                kind.into(),
                key.into(),
                id.into(),
                p.entity_version.into(),
                p.operation.as_str().into(),
            ];
            self.execute(what, &sql, &args).await?;
        }
        Ok(())
    }

    async fn walk(&mut self, commit: &str, limit: usize) -> Result<Vec<Commit>, Error> {
        // The walk carries each commit's snapshot flag, and goes no further
        // than the first commit that has one.
        let t = &self.a.tables.commit;
        let has = self.a.has_snapshot("c.id");
        let sql = format!(
            "WITH RECURSIVE chain (id, parent, depth, snapshotted) AS (\
             SELECT c.id, c.parent_commit_id, 1, {has} FROM {t} AS c WHERE c.graph = ?1 AND c.id = ?2 \
             UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1, {has} FROM {t} AS c \
             JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND chain.depth < ?3 AND NOT chain.snapshotted\
             ) SELECT {COMMIT_COLUMNS}, chain.snapshotted AS snapshot FROM chain JOIN {t} AS c ON c.id = chain.id ORDER BY chain.depth"
        );
        let limit = i64::try_from(limit).unwrap_or(i64::MAX);
        let args = [self.graph(), commit.into(), limit.into()];
        self.commits_of("walk commits", &sql, &args).await
    }

    async fn ref_commits(
        &mut self,
        ref_id: &str,
        head: &str,
        limit: usize,
    ) -> Result<Vec<Commit>, Error> {
        let t = &self.a.tables.commit;
        let sql = format!(
            "WITH RECURSIVE chain (id, parent, depth) AS (\
             SELECT c.id, c.parent_commit_id, 1 FROM {t} AS c WHERE c.graph = ?1 AND c.id = ?2 AND c.ref_id = ?3 \
             UNION ALL SELECT c.id, c.parent_commit_id, chain.depth + 1 FROM {t} AS c \
             JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND c.ref_id = ?3 AND chain.depth < ?4\
             ) SELECT {COMMIT_COLUMNS}, {} AS snapshot FROM chain JOIN {t} AS c ON c.id = chain.id ORDER BY chain.depth",
            self.a.has_snapshot("c.id")
        );
        let limit = i64::try_from(limit).unwrap_or(i64::MAX);
        let args = [self.graph(), head.into(), ref_id.into(), limit.into()];
        self.commits_of("list commits", &sql, &args).await
    }

    async fn patches(&mut self, commits: &[String]) -> Result<Vec<Patch>, Error> {
        if commits.is_empty() {
            return Ok(Vec::new());
        }
        let sql = format!(
            "SELECT commit_id, entity_kind, entity_key, entity_id, entity_version, operation FROM {} \
             WHERE graph = ?1 AND commit_id IN (SELECT value FROM json_each(?2))",
            self.a.tables.patch
        );
        let args = [self.graph(), string_list(commits).into()];
        let rows = self.query("read patches", &sql, &args).await?;
        let names = [
            "commit_id",
            "entity_kind",
            "entity_key",
            "entity_id",
            "entity_version",
            "operation",
        ];
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row, &names);
                Ok(Patch {
                    commit: c.text()?,
                    kind: c.text()?,
                    entity_key: c.text()?,
                    entity_id: c.text()?,
                    entity_version: c.int()?,
                    operation: c.text()?,
                })
            })
            .collect()
    }

    /// The root's highest sequence plus one: the file's one writer orders
    /// every tagger, so the root needs no lock of its own.
    async fn next_sequence(&mut self, root: &str) -> Result<i64, Error> {
        let sql = format!(
            "SELECT COALESCE(MAX(sequence), 0) + 1 AS next FROM {} WHERE graph = ?1 AND root_id = ?2",
            self.a.tables.commit
        );
        let args = [self.graph(), root.into()];
        let row = self
            .first("read the next sequence", &sql, &args)
            .await?
            .ok_or_else(|| Error::Invalid("sqlite: no next sequence".to_owned()))?;
        Cells::new(&row, &["next"]).int()
    }

    async fn snapshot(&mut self, commit: &str) -> Result<Vec<SnapshotEntry>, Error> {
        let sql = format!(
            "SELECT entity_kind, entity_key, entity_id, entity_version FROM {} WHERE graph = ?1 AND commit_id = ?2",
            self.a.tables.snapshot
        );
        let args = [self.graph(), commit.into()];
        let rows = self.query("read the snapshot", &sql, &args).await?;
        let names = ["entity_kind", "entity_key", "entity_id", "entity_version"];
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row, &names);
                Ok(SnapshotEntry {
                    kind: c.text()?,
                    entity_key: c.text()?,
                    entity_id: c.text()?,
                    entity_version: c.int()?,
                })
            })
            .collect()
    }

    async fn insert_snapshot(
        &mut self,
        commit: &str,
        entries: &[SnapshotEntry],
    ) -> Result<(), Error> {
        if entries.is_empty() {
            return Ok(());
        }
        let what = "write the snapshot";
        self.require_own_commit(what, commit, "the snapshot's commit")
            .await?;
        let sql = format!(
            "INSERT INTO {} (id, graph, commit_id, entity_kind, entity_key, entity_id, entity_version) \
             VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?7)",
            self.a.tables.snapshot
        );
        for e in entries {
            let (kind, key, id) = self.pinned(&e.kind, &e.entity_key, &e.entity_id)?;
            let args = [
                new_id()?.into(),
                self.graph(),
                commit.into(),
                kind.into(),
                key.into(),
                id.into(),
                e.entity_version.into(),
            ];
            self.execute(what, &sql, &args).await?;
        }
        Ok(())
    }

    async fn commits(&mut self) -> Result<Vec<CommitNode>, Error> {
        let sql = format!(
            "SELECT c.id, c.parent_commit_id, c.sequence IS NOT NULL AS tagged, {} AS snapshot \
             FROM {} AS c WHERE c.graph = ?1",
            self.a.has_snapshot("c.id"),
            self.a.tables.commit
        );
        let args = [self.graph()];
        let rows = self.query("read the commits", &sql, &args).await?;
        let names = ["id", "parent_commit_id", "tagged", "snapshot"];
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row, &names);
                Ok(CommitNode {
                    id: c.text()?,
                    parent: c.optional_text()?,
                    tagged: c.int()? == 1,
                    snapshot: c.int()? == 1,
                })
            })
            .collect()
    }

    async fn read_release(&mut self, root: &str) -> Result<Release, Error> {
        let sql = format!(
            "SELECT {RELEASE_COLUMNS} FROM {} WHERE graph = ?1 AND root_id = ?2",
            self.a.tables.release
        );
        let args = [self.graph(), root.into()];
        match self.first("read the release", &sql, &args).await? {
            Some(row) => Ok(scan_release(&row)?.0),
            None => Err(Error::NotFound),
        }
    }

    async fn write_release(&mut self, write: ReleaseWrite) -> Result<Release, Error> {
        let what = "write the release";
        let t = &self.a.tables;
        let (release_table, history_table) = (t.release.clone(), t.release_history.clone());
        self.require_commit(what, &write.commit, &write.root, "the release's commit")
            .await?;
        let row = if write.version == 0 {
            // A root's first pointer. Another first pointer of the root holds
            // its slot, as a move at a stale version would.
            let sql = format!(
                "INSERT INTO {release_table} (id, graph, root_id, commit_id, created_at, created_by, updated_at, updated_by, _version) \
                 VALUES (?1, ?2, ?3, ?4, ?5, ?6, ?5, ?6, 1) ON CONFLICT (graph, root_id) DO NOTHING RETURNING {RELEASE_COLUMNS}"
            );
            let args = [
                new_id()?.into(),
                self.graph(),
                write.root.as_str().into(),
                write.commit.as_str().into(),
                self.time.into(),
                write.actor.as_str().into(),
            ];
            self.first(what, &sql, &args).await?
        } else {
            let sql = format!(
                "UPDATE {release_table} SET commit_id = ?3, updated_at = ?4, updated_by = ?5, _version = _version + 1 \
                 WHERE graph = ?1 AND root_id = ?2 AND _version = ?6 RETURNING {RELEASE_COLUMNS}"
            );
            let args = [
                self.graph(),
                write.root.as_str().into(),
                write.commit.as_str().into(),
                self.time.into(),
                write.actor.as_str().into(),
                write.version.into(),
            ];
            self.first(what, &sql, &args).await?
        };
        let Some(row) = row else {
            return Err(Error::VersionConflict);
        };
        let (release, image) = scan_release(&row)?;
        let operation = if write.version == 0 {
            "INSERT"
        } else {
            "UPDATE"
        };
        self.history(
            what,
            &history_table,
            &release.id,
            release.version,
            operation,
            image,
        )
        .await?;
        Ok(release)
    }

    async fn prune(
        &mut self,
        kind: &str,
        retention_days: i64,
        batch_size: i64,
    ) -> Result<i64, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        // A kind declared without retentionDays keeps its history.
        let Some(declared) = k.retention_days else {
            return Ok(0);
        };
        if batch_size < 0 {
            return Err(Error::Invalid(format!(
                "sqlite: prune {} history: a batch is a whole number of images, 0 for no limit, not {batch_size}",
                k.name
            )));
        }
        let days = if retention_days != 0 {
            retention_days
        } else {
            declared
        };
        let t = &a.tables;
        // Older than the retention, not the newest image of its row, and
        // pinned by no patch and no snapshot; the oldest first.
        let sql = format!(
            "DELETE FROM {history} WHERE history_id IN (\
             SELECT h.history_id FROM {history} AS h WHERE h.graph = ?1 AND h.kind = ?2 AND h.recorded_at < ?3 \
             AND EXISTS (SELECT 1 FROM {history} AS newer WHERE newer.id = h.id AND newer._version > h._version) \
             AND NOT EXISTS (SELECT 1 FROM {patch} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) \
             AND NOT EXISTS (SELECT 1 FROM {snapshot} AS pin WHERE pin.graph = h.graph AND pin.entity_id = h.id AND pin.entity_version = h._version) \
             ORDER BY h.recorded_at, h.id, h._version LIMIT ?4)",
            history = t.member_history,
            patch = t.patch,
            snapshot = t.snapshot
        );
        let cutoff = self
            .time
            .saturating_sub(days.saturating_mul(MICROS_PER_DAY));
        let args = [
            self.graph(),
            k.name.as_str().into(),
            cutoff.into(),
            if batch_size == 0 { -1 } else { batch_size }.into(),
        ];
        let n = self
            .execute(&format!("prune {} history", k.name), &sql, &args)
            .await?;
        Ok(i64::try_from(n).unwrap_or(i64::MAX))
    }

    async fn discarded_refs(&mut self, grace: Duration) -> Result<Vec<Ref>, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} WHERE graph = ?1 AND deleted_at IS NOT NULL AND deleted_at < ?2 ORDER BY deleted_at, id",
            self.a.tables.ref_
        );
        let args = [self.graph(), self.time.saturating_sub(micros(grace)).into()];
        self.refs(&sql, &args).await
    }

    async fn idle_drafts(&mut self, idle: Duration) -> Result<Vec<Ref>, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} \
             WHERE graph = ?1 AND deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < ?2 ORDER BY updated_at, id",
            self.a.tables.ref_
        );
        let args = [self.graph(), self.time.saturating_sub(micros(idle)).into()];
        self.refs(&sql, &args).await
    }

    async fn remove_ref_rows(
        &mut self,
        kind: &str,
        ref_id: &str,
        actor: &str,
    ) -> Result<i64, Error> {
        let a = self.a;
        let k = a.kind(kind)?;
        let what = format!("remove the {} rows of a ref", k.name);
        let sql = format!(
            "SELECT {MEMBER_COLUMNS} FROM {} WHERE graph = ?1 AND kind = ?2 AND ref_id = ?3",
            a.tables.member
        );
        let args = [self.graph(), k.name.as_str().into(), ref_id.into()];
        let found = self.members(&what, &sql, &args).await?;
        for m in &found {
            self.remove(&what, k, m, actor).await?;
        }
        Ok(i64::try_from(found.len()).unwrap_or(i64::MAX))
    }

    /// True: the one writer the file's write lock lets in is the only
    /// sweeper there can be.
    async fn sweep_lock(&mut self) -> Result<bool, Error> {
        Ok(true)
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> Value {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../testdata/fixture/recipe.json"
        );
        serde_json::from_str(&std::fs::read_to_string(path).expect("read the fixture"))
            .expect("the fixture is JSON")
    }

    fn graph() -> Options {
        Options {
            graph: "recipe".to_owned(),
            ..Options::default()
        }
    }

    /// The adapter takes the fixture's descriptor and refuses what it cannot
    /// run: a descriptor of another version, no graph, a kind without a role
    /// column or whose role column its columns lack, a tombstone that is not
    /// boolean, a kind without history, and an actor column the kind lacks.
    #[test]
    fn new_refuses_what_it_cannot_run() {
        type Edit = fn(&mut Value, &mut Options);
        let cases: [(&str, Edit, &str); 8] = [
            ("the fixture's descriptor", |_, _| {}, ""),
            (
                "a descriptor of version 2",
                |d, _| d["version"] = 2.into(),
                "reads version 3",
            ),
            (
                "no graph",
                |_, o| o.graph = String::new(),
                "needs its graph's name",
            ),
            (
                "a kind without a root column",
                |d, _| {
                    d["kinds"][0]
                        .as_object_mut()
                        .expect("a kind")
                        .remove("root");
                },
                "has no root",
            ),
            (
                "a role column missing from the kind's columns",
                |d, _| {
                    d["kinds"][0]["columns"]
                        .as_object_mut()
                        .expect("columns")
                        .remove("_version");
                },
                "version column \"_version\" is not in its columns",
            ),
            (
                "a tombstone that is not boolean",
                |d, _| d["kinds"][0]["columns"]["deleted_on_ref"] = "integer".into(),
                "a tombstone is a boolean column",
            ),
            (
                "a kind without history",
                |d, _| {
                    d["kinds"][0]
                        .as_object_mut()
                        .expect("a kind")
                        .remove("history");
                },
                "has no history",
            ),
            (
                "an actor column the kind lacks",
                |d, _| d["kinds"][0]["history"]["actor"] = "updated_by".into(),
                "history actor column \"updated_by\" is not in its columns",
            ),
        ];
        for (name, edit, refuse) in cases {
            let (mut descriptor, mut options) = (fixture(), graph());
            edit(&mut descriptor, &mut options);
            match (refuse, Adapter::new(&descriptor.to_string(), options)) {
                ("", Err(error)) => panic!("{name}: refused: {error}"),
                ("", Ok(_)) => {}
                (_, Ok(_)) => panic!("{name}: accepted, want it refused with {refuse:?}"),
                (_, Err(error)) => assert!(
                    error.to_string().contains(refuse),
                    "{name}: {error}, want {refuse:?}"
                ),
            }
        }
    }

    #[test]
    fn times_are_canonical_date_times() {
        for (micros, want) in [
            (1_800_000_000_000_000, "2027-01-15T08:00:00Z"),
            (1_800_000_000_120_000, "2027-01-15T08:00:00.12Z"),
            (1_800_000_000_000_001, "2027-01-15T08:00:00.000001Z"),
            (0, "1970-01-01T00:00:00Z"),
            (-1, "1969-12-31T23:59:59.999999Z"),
            (-62_167_219_200_000_000, "0000-01-01T00:00:00Z"),
            (253_402_300_799_999_999, "9999-12-31T23:59:59.999999Z"),
        ] {
            assert_eq!(
                micros_to_date_time(micros).expect("a time"),
                want,
                "{micros}"
            );
        }
        for micros in [-62_167_219_200_000_001, 253_402_300_800_000_000, i64::MAX] {
            let error = micros_to_date_time(micros).expect_err("out of range");
            assert!(
                error.to_string().contains("outside the years 0000-9999"),
                "{micros}: {error}"
            );
        }
    }

    /// A client that reports one SQLite version and runs nothing else.
    struct Version(&'static str);

    #[async_trait]
    impl Client for Version {
        async fn begin(&self) -> Result<Box<dyn Conn + '_>, ClientError> {
            Err(ClientError::new("no transactions here"))
        }

        async fn exec(&self, sql: &str) -> Result<Vec<Vec<SqlValue>>, ClientError> {
            assert_eq!(sql, "SELECT sqlite_version()");
            Ok(vec![vec![SqlValue::Text(self.0.to_owned())]])
        }
    }

    /// The adapter refuses a SQLite older than the layout and its statements
    /// need, before it runs anything, with a clear error, and takes any
    /// version from 3.38.0, compared as numbers.
    #[tokio::test]
    async fn the_adapter_refuses_an_older_sqlite() {
        let adapter = Arc::new(Adapter::new(&fixture().to_string(), graph()).expect("adapter"));
        for old in ["3.37.2", "3.9.0", "2.8.17"] {
            let error = check_version(&Version(old)).await.expect_err(old);
            assert!(
                error.to_string().contains(&format!(
                    "runs SQLite {old}, and the adapter needs 3.38.0 or later"
                )),
                "{old}: {error}"
            );
            let refused = adapter.create_tables(&Version(old)).await.expect_err(old);
            assert!(refused.to_string().contains("needs 3.38.0"), "{refused}");
            let refused = adapter.storage(Version(old)).await.map(|_| ());
            assert!(
                refused
                    .as_ref()
                    .is_err_and(|e| e.to_string().contains("needs 3.38.0")),
                "{refused:?}"
            );
        }
        for new in ["3.38.0", "3.38", "3.46.1", "3.100.0", "4.0.0"] {
            check_version(&Version(new)).await.expect(new);
        }
        for garbage in ["", "three", "3.x.0"] {
            let error = check_version(&Version(garbage)).await.expect_err(garbage);
            assert!(
                error.to_string().contains("not a version"),
                "{garbage:?}: {error}"
            );
        }
    }
}
