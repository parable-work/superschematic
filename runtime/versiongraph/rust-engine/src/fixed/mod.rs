//! The fixed layout (D32): [`Storage`] and [`Tx`] over one layout of
//! tables, the same for every graph, in either of two dialects
//! ([`Dialect`]), so the engine keeps a graph in tables a caller's own
//! migrations create: in a SQLite file, or in Postgres without a trigger,
//! as a D16 behavior's tables are (D58). On SQLite it is the Rust port of
//! the TypeScript package's `sqlite.ts`, statement for statement, and
//! stores what it does, so a file one writes the other reads;
//! [`crate::sqlite`] is it under the SQLite adapter's names.
//! [`crate::postgres`] is the Postgres adapter over the tables sqlgen
//! generates per graph, whose triggers write history.
//!
//! The layout is nine tables (`STRICT` on SQLite): `ref`, `ref_history`,
//! `commit`, `patch`, `snapshot_entry`, `release`, `release_history`,
//! `member` and `member_history`, each named by a function the caller gives
//! ([`default_table_name`] puts `graph_` before each), as is every index.
//! Every row carries its graph's name, so one database holds several
//! graphs, and every statement is scoped to the adapter's graph. An id is
//! `TEXT` holding a UUID in its canonical form (base62); a version, a
//! sequence and a tombstone are `INTEGER` (`BIGINT` on Postgres); the times
//! of refs, commits, release pointers and history images are integer
//! microseconds since the Unix epoch, returned as canonical date-times. A
//! member row holds its kind and its role columns as columns and every
//! other column of the descriptor as one canonical JSON object, `data`,
//! stored as text in both dialects so it reads back as written. Foreign
//! keys check every edge inside the layout; there is no root table, so
//! nothing checks a root but the adapter, which refuses a write whose ref
//! or commit is another graph's or another root's.
//!
//! The layout has no trigger, so the adapter does what Postgres's triggers
//! do for sqlgen's tables, in the statements of the transaction that
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
//! The adapter reaches its database through [`Client`], a two-trait seam as
//! [`crate::postgres::Client`] is; on SQLite `crate::sqlite::Rusqlite` binds
//! rusqlite (the `rusqlite` feature, off by default). A host that holds a
//! synchronous transaction, as D58's engine holds a behavior's call,
//! implements [`HostTx`] over it, statements with `?` placeholders in and
//! rows of [`SqlValue`]s out, and [`HostClient`] makes that a client whose
//! futures are always ready; [`FixedLayout::bind`] binds it for the call
//! and [`crate::Engine::run_on`] runs each operation over it on the
//! calling thread. On SQLite a
//! transaction begins with `BEGIN IMMEDIATE`, which takes the file's write
//! lock, and inside a transaction the caller holds on the connection it is
//! a savepoint. Either way one writer holds the file, so [`Tx::lock_ref`]
//! reads a ref as [`Tx::read_ref`] does, [`Tx::next_sequence`] reads the
//! root's highest sequence plus 1, and [`Tx::sweep_lock`] is true. On
//! Postgres they do the same, so the caller keeps one writer per graph, as
//! D58's engine does with a lock on the namespace.
//!
//! On Postgres the statements are SQLite's but where the two databases word
//! them differently:
//!
//! - the layout's tables declare `BIGINT` for `INTEGER` and `TEXT COLLATE
//!   "C"`, which compares bytes as SQLite does, and are not `STRICT`; the
//!   ref's keys to its head and base commit are added once the commit table
//!   exists, since a Postgres key needs its table;
//! - a list argument, the pins of an image read and the commits of a patch
//!   read, is JSON text read with `jsonb_array_elements` where SQLite reads
//!   it with `json_each` and `json_extract`;
//! - a flag a read returns, whether a commit is snapshotted or tagged, is
//!   `CASE WHEN ... THEN 1 ELSE 0 END`, an integer as SQLite's is;
//! - a parameter Postgres would type `integer` is cast to `BIGINT`;
//! - a new ref whose name is taken inserts nothing (`ON CONFLICT ... DO
//!   NOTHING`), since an error aborts a Postgres transaction, where SQLite's
//!   unique index refuses it;
//! - a prune with no batch passes a null `LIMIT`, where SQLite takes -1;
//! - nothing checks the server's version or turns foreign keys on, which
//!   Postgres always checks.

mod client;
mod host;

use std::collections::{BTreeMap, BTreeSet, HashMap};
use std::fmt;
use std::sync::Arc;
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use async_trait::async_trait;
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::Value;

use crate::canonical;
use crate::sqlite::{check_version, SQLITE_CONSTRAINT_UNIQUE};
use crate::storage::{
    Commit, CommitNode, NewCommit, NewRef, Patch, Pin, Ref, RefUpdate, Release, ReleaseWrite,
    RowWrite, SnapshotEntry, Storage, Tx,
};
use crate::Error;
pub use client::{Client, ClientError, Conn, SqlValue};
pub use host::{HostClient, HostTx};

/// The database a [`FixedLayout`] runs on, which words a few of its
/// statements ([`crate::fixed`] lists them).
#[derive(Debug, Clone, Copy, Default, PartialEq, Eq, Hash)]
pub enum Dialect {
    /// SQLite, 3.37.0 or later, with `json_each` and `json_extract`: the
    /// TypeScript, Go and Python SQLite adapters' statements and file.
    #[default]
    Sqlite,
    /// Postgres, without a trigger: the layout as a D16 behavior's tables
    /// hold it on Postgres (D58).
    Postgres,
}

impl Dialect {
    /// What the adapter's errors begin with.
    fn label(self) -> &'static str {
        match self {
            Dialect::Sqlite => "sqlite",
            Dialect::Postgres => "fixed layout on postgres",
        }
    }
}

/// The widest time, in microseconds either side of the Unix epoch, a
/// transaction writes and a stored time reads as: 2^53 - 1, the integers a
/// JavaScript number holds exactly, so every adapter reads what another
/// writes. Every integer the adapter reads back is held to it, as the
/// TypeScript adapter holds them.
pub const MAX_MICROS: i64 = (1 << 53) - 1;

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

/// Configures a [`FixedLayout`].
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
fn name_of(
    dialect: Dialect,
    table_name: &dyn Fn(&str) -> String,
    local: &str,
) -> Result<String, Error> {
    let name = table_name(local);
    if name.is_empty() || name.contains('\0') {
        return Err(Error::Invalid(format!(
            "{}: the table name function named {local:?} {name:?}",
            dialect.label()
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
    fn new(dialect: Dialect, table_name: &dyn Fn(&str) -> String) -> Result<Self, Error> {
        let name = |local: &str| name_of(dialect, table_name, local);
        Ok(Tables {
            ref_: name("ref")?,
            ref_history: name("ref_history")?,
            commit: name("commit")?,
            patch: name("patch")?,
            snapshot: name("snapshot_entry")?,
            release: name("release")?,
            release_history: name("release_history")?,
            member: name("member")?,
            member_history: name("member_history")?,
        })
    }
}

/// The statements that create the layout in `dialect`, each table, index
/// and key under the name `table_name` gives it ([`default_table_name`] for
/// the default names), with no trigger and no transaction control, for a
/// caller that runs its own migrations. [`FixedLayout::create_tables`]
/// runs them, and each may run again: each is `CREATE TABLE IF NOT EXISTS`
/// or `CREATE [UNIQUE] INDEX IF NOT EXISTS`, but for four on Postgres that
/// drop and add the ref's keys to its base and head commits
/// (`ref_base_commit` and `ref_head_commit`, named as the tables are),
/// after the commit table, since Postgres declares a key only to a table
/// that exists and the commit table has a key to the ref table.
pub fn layout(dialect: Dialect, table_name: &dyn Fn(&str) -> String) -> Result<Vec<String>, Error> {
    let t = Tables::new(dialect, table_name)?;
    let index = |local: &str| name_of(dialect, table_name, local);
    // A text column compares bytes on Postgres, as every column does on
    // SQLite, so the layout's reads order and match the same in both.
    let (text, int, strict) = match dialect {
        Dialect::Sqlite => ("TEXT", "INTEGER", " STRICT"),
        Dialect::Postgres => ("TEXT COLLATE \"C\"", "BIGINT", ""),
    };
    let history = |table: &str| {
        format!(
            "CREATE TABLE IF NOT EXISTS {table} (\
             history_id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, id {text} NOT NULL, _version {int} NOT NULL, \
             operation {text} NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), data {text} NOT NULL, recorded_at {int} NOT NULL\
             ){strict}"
        )
    };
    // The ref's keys to its base and head commits: declared with the ref
    // table on SQLite, and added once the commit table exists on Postgres.
    let commit_key = |column: &str| match dialect {
        Dialect::Sqlite => format!("{column} {text} REFERENCES {} (id)", t.commit),
        Dialect::Postgres => format!("{column} {text}"),
    };
    let mut statements = vec![
        // A ref's head and a commit's ref point at each other. A ref is
        // written before any commit of it, and its head moves to a commit
        // only once the commit is written, so both keys are checked at once,
        // not deferred.
        format!(
            "CREATE TABLE IF NOT EXISTS {ref_} (\
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, root_id {text} NOT NULL, \
             parent_ref_id {text} REFERENCES {ref_} (id), {base}, \
             {head}, name {text} NOT NULL, sealed_at {int}, \
             created_at {int} NOT NULL, created_by {text} NOT NULL, updated_at {int} NOT NULL, updated_by {text} NOT NULL, \
             deleted_at {int}, deleted_by {text}, _version {int} NOT NULL\
             ){strict}",
            ref_ = t.ref_,
            base = commit_key("base_commit_id"),
            head = commit_key("head_commit_id"),
        ),
        // A root's live refs have distinct names: a name already taken is
        // this index's SQLITE_CONSTRAINT_UNIQUE, and on Postgres the
        // conflict a new ref's insert does nothing on.
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
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, root_id {text} NOT NULL, \
             ref_id {text} NOT NULL REFERENCES {ref_} (id), parent_commit_id {text} REFERENCES {commit} (id), \
             message {text}, schema_epoch {int} NOT NULL, content_hash {text} NOT NULL, sequence {int}, \
             created_at {int} NOT NULL, created_by {text} NOT NULL\
             ){strict}",
            ref_ = t.ref_,
            commit = t.commit
        ),
    ];
    if dialect == Dialect::Postgres {
        for (key, column) in [
            ("ref_base_commit", "base_commit_id"),
            ("ref_head_commit", "head_commit_id"),
        ] {
            let key = index(key)?;
            statements.push(format!(
                "ALTER TABLE {} DROP CONSTRAINT IF EXISTS {key}",
                t.ref_
            ));
            statements.push(format!(
                "ALTER TABLE {} ADD CONSTRAINT {key} FOREIGN KEY ({column}) REFERENCES {} (id)",
                t.ref_, t.commit
            ));
        }
    }
    statements.extend([
        format!(
            "CREATE UNIQUE INDEX IF NOT EXISTS {} ON {} (graph, root_id, sequence)",
            index("commit_sequence")?,
            t.commit
        ),
        format!(
            "CREATE TABLE IF NOT EXISTS {} (\
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, commit_id {text} NOT NULL REFERENCES {} (id), \
             entity_kind {text} NOT NULL, entity_key {text} NOT NULL, entity_id {text} NOT NULL, entity_version {int} NOT NULL, \
             operation {text} NOT NULL CHECK (operation IN ('ADD', 'UPDATE', 'DELETE'))\
             ){strict}",
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
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, commit_id {text} NOT NULL REFERENCES {} (id), \
             entity_kind {text} NOT NULL, entity_key {text} NOT NULL, entity_id {text} NOT NULL, entity_version {int} NOT NULL\
             ){strict}",
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
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, root_id {text} NOT NULL, \
             commit_id {text} NOT NULL REFERENCES {} (id), created_at {int} NOT NULL, created_by {text} NOT NULL, \
             updated_at {int} NOT NULL, updated_by {text} NOT NULL, _version {int} NOT NULL\
             ){strict}",
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
             id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, kind {text} NOT NULL, entity_key {text} NOT NULL, \
             ref_id {text} NOT NULL REFERENCES {} (id), root_id {text} NOT NULL, \
             tombstone {int} NOT NULL CHECK (tombstone IN (0, 1)), _version {int} NOT NULL, data {text} NOT NULL\
             ){strict}",
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
             history_id {text} NOT NULL PRIMARY KEY, graph {text} NOT NULL, kind {text} NOT NULL, id {text} NOT NULL, \
             _version {int} NOT NULL, operation {text} NOT NULL CHECK (operation IN ('INSERT', 'UPDATE', 'DELETE')), \
             data {text} NOT NULL, recorded_at {int} NOT NULL\
             ){strict}",
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
    ]);
    Ok(statements)
}

/// A time in whole microseconds since the Unix epoch as a canonical
/// date-time: UTC with `Z`, its fraction of a second without trailing zeros
/// and left out when zero. A time outside [`MAX_MICROS`] either side of the
/// epoch is refused, and so is a year outside 0000-9999.
pub fn micros_to_date_time(micros: i64) -> Result<String, Error> {
    date_time(Dialect::Sqlite, micros)
}

/// [`micros_to_date_time`], its errors naming `dialect`.
fn date_time(dialect: Dialect, micros: i64) -> Result<String, Error> {
    check_micros(dialect, micros, "a time of")?;
    canonical::date_time_of_micros(micros)
        .map_err(|e| Error::Invalid(format!("{}: {e}", dialect.label())))
}

/// Refuses a time outside [`MAX_MICROS`] either side of the epoch.
fn check_micros(dialect: Dialect, micros: i64, what: &str) -> Result<i64, Error> {
    if !(-MAX_MICROS..=MAX_MICROS).contains(&micros) {
        return Err(Error::Invalid(format!(
            "{}: {what} {micros} microseconds, outside the 2^53 - 1 either side of the Unix epoch a time holds",
            dialect.label()
        )));
    }
    Ok(micros)
}

/// Reads the clock once, refusing a time outside [`MAX_MICROS`].
fn read_clock(dialect: Dialect, clock: &Clock) -> Result<i64, Error> {
    check_micros(dialect, clock(), "the clock returned")
}

/// A client's error, with what the adapter was doing, keeping SQLite's
/// result code so callers can still tell it apart
/// ([`crate::sqlite::result_code`]).
pub(crate) fn failed(dialect: Dialect, what: &str, error: ClientError) -> Error {
    Error::Storage(Box::new(ClientError {
        code: error.code,
        source: format!("{}: {what}: {}", dialect.label(), error.source).into(),
    }))
}

/// One member kind's role columns, columns and history.
#[derive(Debug)]
struct Kind {
    /// The dialect of the adapter that reads it, which its errors name.
    dialect: Dialect,
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
                "{}: the {} kind has no column {column:?}",
                self.dialect.label(),
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

/// The 128 random bits of each new id, before the adapter sets its version
/// and variant: the system's random source, or a test's seeded one.
pub(crate) type IdBits = Arc<dyn Fn() -> Result<[u8; 16], Error> + Send + Sync>;

/// The system's random source.
fn system_bits(dialect: Dialect) -> Result<[u8; 16], Error> {
    let mut bytes = [0u8; 16];
    getrandom::fill(&mut bytes)
        .map_err(|e| Error::storage(format!("{}: generate an id: {e}", dialect.label())))?;
    Ok(bytes)
}

/// One graph's statements over the layout in one dialect, built from its
/// descriptor. Bind a [`Client`] with [`FixedLayout::storage`].
pub struct FixedLayout {
    dialect: Dialect,
    graph: String,
    tables: Tables,
    layout: Vec<String>,
    clock: Clock,
    ids: IdBits,
    kinds: HashMap<String, Kind>,
}

impl fmt::Debug for FixedLayout {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("FixedLayout")
            .field("dialect", &self.dialect)
            .field("graph", &self.graph)
            .field("tables", &self.tables)
            .field("kinds", &self.kinds)
            .finish_non_exhaustive()
    }
}

impl FixedLayout {
    /// Reads a graph's descriptor (version 3) for the layout on SQLite, the
    /// SQLite adapter's dialect: [`FixedLayout::with_dialect`] with
    /// [`Dialect::Sqlite`].
    pub fn new(descriptor: &str, options: Options) -> Result<Self, Error> {
        FixedLayout::with_dialect(Dialect::Sqlite, descriptor, options)
    }

    /// Reads a graph's descriptor (version 3) for the layout in `dialect`.
    /// The adapter reads only its kinds: each kind's role columns (the root
    /// among them), its columns' value classes and its history; the tables
    /// the descriptor names are the Postgres adapter's.
    pub fn with_dialect(
        dialect: Dialect,
        descriptor: &str,
        options: Options,
    ) -> Result<Self, Error> {
        let label = dialect.label();
        let d: Descriptor = serde_json::from_str(descriptor)
            .map_err(|e| Error::Invalid(format!("{label}: read the descriptor: {e}")))?;
        if d.version != 3 {
            return Err(Error::Invalid(format!(
                "{label}: descriptor version {}; this adapter reads version 3",
                d.version
            )));
        }
        if options.graph.is_empty() {
            return Err(Error::Invalid(format!(
                "{label}: an adapter needs its graph's name (Options::graph)"
            )));
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
                        "{label}: kind {:?} has no {role}",
                        k.kind
                    )));
                }
                if !k.columns.contains_key(column) {
                    return Err(Error::Invalid(format!(
                        "{label}: kind {:?} {role} column {column:?} is not in its columns",
                        k.kind
                    )));
                }
            }
            if k.columns[&k.tombstone] != canonical::BOOLEAN
                || k.columns[&k.version] != canonical::INTEGER
            {
                return Err(Error::Invalid(format!(
                    "{label}: kind {:?}: a tombstone is a boolean column and a version an integer one",
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
                    "{label}: kind {:?} has no history",
                    k.kind
                )));
            };
            if let Some(actor) = &actor {
                if !k.columns.contains_key(actor) {
                    return Err(Error::Invalid(format!(
                        "{label}: kind {:?} history actor column {actor:?} is not in its columns",
                        k.kind
                    )));
                }
            }
            let mut kind = Kind {
                dialect,
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
        Ok(FixedLayout {
            dialect,
            graph: options.graph,
            tables: Tables::new(dialect, table_name.as_ref())?,
            layout: layout(dialect, table_name.as_ref())?,
            clock: options.clock.unwrap_or_else(|| Arc::new(system_clock)),
            ids: Arc::new(move || system_bits(dialect)),
            kinds,
        })
    }

    /// The adapter, its ids' random bits drawn from `ids`: a test's seeded
    /// source, so a run writes the same file each time.
    #[cfg(all(test, feature = "rusqlite"))]
    pub(crate) fn with_ids(mut self, ids: IdBits) -> Self {
        self.ids = ids;
        self
    }

    /// A new id: a version-4 UUID in its canonical form.
    fn new_id(&self) -> Result<String, Error> {
        let mut bytes = (self.ids)()?;
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

    /// The layout's dialect.
    pub fn dialect(&self) -> Dialect {
        self.dialect
    }

    /// The statements that create the layout under the adapter's names, in
    /// its dialect ([`layout`]).
    pub fn layout(&self) -> &[String] {
        &self.layout
    }

    /// Creates the layout's tables and indexes where they are missing, in
    /// one transaction of the client: its own on the connection, or a
    /// savepoint in the caller's. On SQLite it refuses a SQLite older than
    /// [`crate::sqlite::MIN_SQLITE_VERSION`], or one whose JSON functions
    /// fail [`crate::sqlite::JSON_PROBE`], first.
    pub async fn create_tables<C: Client + ?Sized>(&self, client: &C) -> Result<(), Error> {
        let dialect = self.dialect;
        if dialect == Dialect::Sqlite {
            check_version(client).await?;
        }
        let mut conn = client
            .begin()
            .await
            .map_err(|e| failed(dialect, "begin", e))?;
        // A transaction reads the clock once it has begun, this one too,
        // though it writes no time, as the TypeScript adapter's does: a
        // seeded run reads each time in the same transaction in both.
        if let Err(error) = read_clock(dialect, &self.clock) {
            let _ = conn.rollback().await;
            return Err(error);
        }
        for statement in &self.layout {
            if let Err(error) = conn.execute(statement, &[]).await {
                // The statement's error is the one to report.
                let _ = conn.rollback().await;
                return Err(failed(dialect, "create the layout", error));
            }
        }
        conn.commit()
            .await
            .map_err(|e| failed(dialect, "commit", e))
    }

    /// Binds the adapter to a client. On SQLite it refuses a SQLite older
    /// than [`crate::sqlite::MIN_SQLITE_VERSION`], or one whose JSON
    /// functions fail [`crate::sqlite::JSON_PROBE`], and turns the
    /// connection's foreign keys on, which SQLite ignores inside a
    /// transaction, so bind it outside one: a connection whose foreign keys
    /// stay off is refused. On Postgres, which checks foreign keys always,
    /// it runs nothing.
    pub async fn storage<C: Client>(self: &Arc<Self>, client: C) -> Result<FixedStorage<C>, Error> {
        if self.dialect == Dialect::Sqlite {
            check_version(&client).await?;
            client
                .exec("PRAGMA foreign_keys = ON")
                .await
                .map_err(|e| failed(self.dialect, "turn foreign keys on", e))?;
            let rows = client
                .exec("PRAGMA foreign_keys")
                .await
                .map_err(|e| failed(self.dialect, "read foreign keys", e))?;
            if !matches!(
                rows.first().and_then(|row| row.first()),
                Some(SqlValue::Int(1))
            ) {
                return Err(Error::Invalid(
                    "sqlite: the connection's foreign keys would not turn on; bind the adapter outside a transaction"
                        .to_owned(),
                ));
            }
        }
        Ok(FixedStorage {
            adapter: Arc::clone(self),
            client,
        })
    }

    /// Binds the adapter to a client and runs nothing: for a client in a
    /// transaction the caller holds, as a [`HostClient`] is, whose
    /// connection the caller has checked (on SQLite: 3.37.0 or later, with
    /// `json_each` and `json_extract`, and foreign keys on) and whose
    /// transaction may refuse the checks' statements, as D16 refuses a
    /// behavior's `PRAGMA` and `sqlite_version`. Binding costs a reference
    /// count, so a host binds a storage for each call.
    pub fn bind<C: Client>(self: &Arc<Self>, client: C) -> FixedStorage<C> {
        FixedStorage {
            adapter: Arc::clone(self),
            client,
        }
    }

    fn kind(&self, name: &str) -> Result<&Kind, Error> {
        self.kinds.get(name).ok_or_else(|| {
            Error::Invalid(format!("{}: unknown kind {name:?}", self.dialect.label()))
        })
    }

    /// The SQL that tells whether the commit whose id is `id_sql` has a
    /// snapshot.
    fn has_snapshot(&self, id_sql: &str) -> String {
        format!(
            "EXISTS (SELECT 1 FROM {} AS s WHERE s.commit_id = {id_sql})",
            self.tables.snapshot
        )
    }

    /// A condition as the integer flag a read returns, 1 or 0: SQLite's
    /// condition is one already, and Postgres's a boolean.
    fn flag(&self, condition: &str) -> String {
        match self.dialect {
            Dialect::Sqlite => condition.to_owned(),
            Dialect::Postgres => format!("CASE WHEN {condition} THEN 1 ELSE 0 END"),
        }
    }

    /// A placeholder compared with an integer that no column types: cast to
    /// `BIGINT` on Postgres, which would type it `integer` and refuse an
    /// `int8` argument.
    fn bigint(&self, placeholder: &str) -> String {
        match self.dialect {
            Dialect::Sqlite => placeholder.to_owned(),
            Dialect::Postgres => format!("CAST({placeholder} AS BIGINT)"),
        }
    }

    /// A placeholder whose argument is a JSON array as text, typed `jsonb`
    /// on Postgres: it binds as text and is read as JSON.
    fn json_list(&self, placeholder: &str) -> String {
        format!("CAST(CAST({placeholder} AS TEXT) AS JSONB)")
    }
}

/// A [`FixedLayout`] bound to a [`Client`]: the graph's [`Storage`].
pub struct FixedStorage<C> {
    adapter: Arc<FixedLayout>,
    client: C,
}

impl<C> FixedStorage<C> {
    /// The bound client.
    pub fn client(&self) -> &C {
        &self.client
    }

    /// The adapter.
    pub fn adapter(&self) -> &Arc<FixedLayout> {
        &self.adapter
    }
}

#[async_trait]
impl<C: Client> Storage for FixedStorage<C> {
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
        let dialect = self.adapter.dialect;
        let conn = self
            .client
            .begin()
            .await
            .map_err(|e| failed(dialect, "begin", e))?;
        // Read once the transaction holds the write lock, so times order as
        // the writes do. A time out of range ends the transaction.
        let time = match read_clock(dialect, &self.adapter.clock) {
            Ok(time) => time,
            Err(error) => {
                let _ = conn.rollback().await;
                return Err(error);
            }
        };
        Ok(Box::new(FixedTx {
            a: &self.adapter,
            conn,
            time,
        }))
    }
}

/// One transaction's view of the graph, at the transaction's time.
struct FixedTx<'a> {
    a: &'a FixedLayout,
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
    dialect: Dialect,
    cells: std::slice::Iter<'r, SqlValue>,
    columns: std::slice::Iter<'r, &'static str>,
}

impl<'r> Cells<'r> {
    fn new(dialect: Dialect, row: &'r [SqlValue], columns: &'r [&'static str]) -> Self {
        Cells {
            dialect,
            cells: row.iter(),
            columns: columns.iter(),
        }
    }

    fn next(&mut self) -> Result<(&'r SqlValue, &'static str), Error> {
        let column = self.columns.next().copied().unwrap_or("a column");
        let cell = self.cells.next().ok_or_else(|| {
            Error::Invalid(format!(
                "{}: a row has too few columns",
                self.dialect.label()
            ))
        })?;
        Ok((cell, column))
    }

    fn optional_text(&mut self) -> Result<Option<String>, Error> {
        match self.next()? {
            (SqlValue::Null, _) => Ok(None),
            (SqlValue::Text(s), _) => Ok(Some(s.clone())),
            (other, column) => Err(Error::Invalid(format!(
                "{}: column {column} is {other:?}, not text",
                self.dialect.label()
            ))),
        }
    }

    fn text(&mut self) -> Result<String, Error> {
        match self.next()? {
            (SqlValue::Text(s), _) => Ok(s.clone()),
            (other, column) => Err(Error::Invalid(format!(
                "{}: column {column} is {other:?}, not text",
                self.dialect.label()
            ))),
        }
    }

    fn optional_int(&mut self) -> Result<Option<i64>, Error> {
        match self.next()? {
            (SqlValue::Null, _) => Ok(None),
            (SqlValue::Int(n), column) => in_range(self.dialect, *n, column).map(Some),
            (other, column) => Err(Error::Invalid(format!(
                "{}: column {column} is {other:?}, not an integer",
                self.dialect.label()
            ))),
        }
    }

    fn int(&mut self) -> Result<i64, Error> {
        match self.next()? {
            (SqlValue::Int(n), column) => in_range(self.dialect, *n, column),
            (other, column) => Err(Error::Invalid(format!(
                "{}: column {column} is {other:?}, not an integer",
                self.dialect.label()
            ))),
        }
    }
}

/// An integer the adapter reads back, refused outside [`MAX_MICROS`] either
/// side of zero, the integers a JavaScript number holds exactly, as the
/// TypeScript adapter refuses one.
fn in_range(dialect: Dialect, n: i64, column: &str) -> Result<i64, Error> {
    if !(-MAX_MICROS..=MAX_MICROS).contains(&n) {
        return Err(Error::Invalid(format!(
            "{}: column {column} is {n}, not an integer a JavaScript number holds exactly",
            dialect.label()
        )));
    }
    Ok(n)
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

fn scan_ref_row(d: Dialect, row: &[SqlValue]) -> Result<RefRow, Error> {
    let mut c = Cells::new(d, row, &REF_NAMES);
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
    fn image(&self, d: Dialect) -> Result<String, Error> {
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
            image.insert(column.to_owned(), json_time(d, value)?);
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
fn scan_release(d: Dialect, row: &[SqlValue]) -> Result<(Release, String), Error> {
    let mut c = Cells::new(d, row, &RELEASE_NAMES);
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
    image.insert("created_at".to_owned(), json_time(d, Some(created_at))?);
    image.insert("updated_at".to_owned(), json_time(d, Some(updated_at))?);
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

fn scan_commit(d: Dialect, row: &[SqlValue]) -> Result<Commit, Error> {
    let mut c = Cells::new(d, row, &COMMIT_NAMES);
    Ok(Commit {
        id: c.text()?,
        root: c.text()?,
        ref_id: c.text()?,
        parent: c.optional_text()?,
        message: c.optional_text()?.unwrap_or_default(),
        schema_epoch: c.int()?,
        content_hash: c.text()?,
        sequence: c.optional_int()?,
        created_at: date_time(d, c.int()?)?,
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

fn scan_member(d: Dialect, row: &[SqlValue]) -> Result<Member, Error> {
    let mut c = Cells::new(d, row, &MEMBER_NAMES);
    Ok(Member {
        id: c.text()?,
        key: c.text()?,
        ref_id: c.text()?,
        root: c.text()?,
        tombstone: c.int()? == 1,
        version: c.int()?,
        data: read_object(d, &c.text()?, "data")?,
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
fn read_object(d: Dialect, json: &str, column: &str) -> Result<BTreeMap<String, String>, Error> {
    let members: BTreeMap<String, Box<RawValue>> = serde_json::from_str(json).map_err(|e| {
        Error::Invalid(format!(
            "{}: column {column} does not hold a JSON object: {e}",
            d.label()
        ))
    })?;
    Ok(members
        .into_iter()
        .map(|(name, value)| (name, value.get().to_owned()))
        .collect())
}

/// Reads JSON text the adapter wrote or read as a value, keeping each
/// number's digits.
fn value_of(d: Dialect, json: &str) -> Result<Value, Error> {
    serde_json::from_str(json)
        .map_err(|e| Error::Invalid(format!("{}: reread a stored row: {e}", d.label())))
}

/// The JSON text of an optional string.
fn json_text(value: Option<&str>) -> String {
    value.map_or_else(|| "null".to_owned(), canonical::string_text)
}

/// The JSON text of an optional time, as a canonical date-time.
fn json_time(d: Dialect, micros: Option<i64>) -> Result<String, Error> {
    match micros {
        Some(micros) => Ok(canonical::string_text(&date_time(d, micros)?)),
        None => Ok("null".to_owned()),
    }
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
    match value_of(k.dialect, &text)? {
        Value::String(s) => Ok(s),
        _ => Err(Error::Invalid(format!(
            "{}: the {} column {column} holds a string",
            k.dialect.label(),
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

impl FixedTx<'_> {
    async fn query(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Vec<SqlValue>>, Error> {
        self.conn
            .query(sql, args)
            .await
            .map_err(|e| failed(self.a.dialect, what, e))
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
            .map_err(|e| failed(self.a.dialect, what, e))
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
                "{label}: {what}: {role}: ref {id} is not a ref of root {root} in graph {}",
                self.a.graph,
                label = self.a.dialect.label(),
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
                "{label}: {what}: {role}: commit {id} is not a commit of root {root} in graph {}",
                self.a.graph,
                label = self.a.dialect.label(),
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
                "{label}: {what}: {role}: commit {id} is not a commit of graph {}",
                self.a.graph,
                label = self.a.dialect.label(),
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
            self.a.new_id()?.into(),
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
            self.a.new_id()?.into(),
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
        rows.iter()
            .map(|row| scan_member(self.a.dialect, row))
            .collect()
    }

    async fn refs(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Ref>, Error> {
        let rows = self.query("read refs", sql, args).await?;
        rows.iter()
            .map(|row| scan_ref_row(self.a.dialect, row).map(|r| r.to_ref()))
            .collect()
    }

    async fn commits_of(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Commit>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.iter()
            .map(|row| scan_commit(self.a.dialect, row))
            .collect()
    }

    async fn upsert(&mut self, k: &Kind, write: RowWrite) -> Result<Value, Error> {
        let what = format!("write {} row", k.name);
        let Value::Object(row) = &write.row else {
            return Err(Error::Invalid(format!(
                "{label}: {what}: a {} row is a JSON object",
                k.name,
                label = self.a.dialect.label(),
            )));
        };
        // The row's other columns, each canonical. The adapter writes the
        // ref, the root and the tombstone, and never the id or the version.
        let mut given = BTreeMap::new();
        for (column, value) in row {
            if !k.columns.contains_key(column) {
                return Err(Error::Invalid(format!(
                    "{label}: {what}: the {} row has column {column:?}, which its descriptor does not declare",
                    k.name,
                    label = self.a.dialect.label(),
                )));
            }
            if k.is_role(column) {
                continue;
            }
            given.insert(column.clone(), canonical_of(k, column, value)?);
        }
        let key = match row.get(&k.key) {
            Some(value) if !value.is_null() => {
                match value_of(self.a.dialect, &canonical_of(k, &k.key, value)?)? {
                    Value::String(key) => Some(key),
                    _ => {
                        return Err(Error::Invalid(format!(
                            "{label}: {what}: the {} row's entity key is a string",
                            k.name,
                            label = self.a.dialect.label(),
                        )))
                    }
                }
            }
            _ => None,
        };
        let ref_id = role_value(k, &k.ref_column, &write.ref_id)?;
        let root = role_value(k, &k.root, &write.root)?;
        self.require_ref(&what, &ref_id, &root, "the row's ref")
            .await?;
        let time = Value::String(date_time(self.a.dialect, self.time)?);
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
                    "{label}: {what}: {changed} rows written",
                    label = self.a.dialect.label(),
                )));
            }
            let members = member_members(k, &stored);
            self.member_history(&what, k, &stored.id, stored.version, "UPDATE", &members)
                .await?;
            return value_of(self.a.dialect, &write_object(&members));
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
        // The row's id first, then a key the row lacks, as the TypeScript
        // adapter draws them, so seeded ids fall the same way in both.
        let id = role_value(k, &k.id, &self.a.new_id()?)?;
        let key = match key {
            Some(key) => key,
            None => role_value(k, &k.key, &self.a.new_id()?)?,
        };
        let stored = Member {
            id,
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
        value_of(self.a.dialect, &write_object(&members))
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
                "{label}: {what}: {changed} rows deleted",
                label = self.a.dialect.label(),
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
impl Tx for FixedTx<'_> {
    async fn commit(self: Box<Self>) -> Result<(), Error> {
        self.conn
            .commit()
            .await
            .map_err(|e| failed(self.a.dialect, "commit", e))
    }

    async fn rollback(self: Box<Self>) -> Result<(), Error> {
        self.conn
            .rollback()
            .await
            .map_err(|e| failed(self.a.dialect, "roll back", e))
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
        let id = self.a.new_id()?;
        // On Postgres a name already taken inserts nothing, rather than
        // failing on ref_live_name and aborting the transaction.
        let taken = match self.a.dialect {
            Dialect::Sqlite => "",
            Dialect::Postgres => {
                " ON CONFLICT (graph, root_id, name) WHERE deleted_at IS NULL DO NOTHING"
            }
        };
        let sql = format!(
            "INSERT INTO {} (id, graph, root_id, parent_ref_id, base_commit_id, head_commit_id, name, sealed_at, \
             created_at, created_by, updated_at, updated_by, deleted_at, deleted_by, _version) \
             VALUES (?1, ?2, ?3, ?4, ?5, NULL, ?6, NULL, ?7, ?8, ?7, ?8, NULL, NULL, 1){taken} RETURNING {REF_COLUMNS}",
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
            Err(e) => return Err(failed(self.a.dialect, what, e)),
        };
        let row = match (rows.first(), self.a.dialect) {
            (Some(row), _) => row,
            (None, Dialect::Postgres) => return Err(Error::NameTaken(new.name)),
            (None, Dialect::Sqlite) => {
                return Err(Error::Invalid(format!(
                    "{}: {what}: no row",
                    self.a.dialect.label()
                )))
            }
        };
        let row = scan_ref_row(self.a.dialect, row)?;
        let table = self.a.tables.ref_history.clone();
        self.history(what, &table, &id, 1, "INSERT", row.image(self.a.dialect)?)
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
            Some(row) => Ok(scan_ref_row(self.a.dialect, &row)?.to_ref()),
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
            let root = Cells::new(self.a.dialect, &row, &["root_id"]).text()?;
            if let Some(head) = &update.head {
                self.require_commit(what, head, &root, "the head").await?;
            }
            if let Some(base) = &update.base {
                self.require_commit(what, base, &root, "the base").await?;
            }
        }
        let sql = format!(
            "UPDATE {ref_table} SET head_commit_id = COALESCE(?3, head_commit_id), base_commit_id = COALESCE(?4, base_commit_id), \
             sealed_at = CASE WHEN {seal} = 1 THEN ?6 ELSE sealed_at END, updated_at = ?6, updated_by = ?7, _version = _version + 1 \
             WHERE graph = ?1 AND id = ?2 AND _version = ?8 RETURNING {REF_COLUMNS}",
            seal = self.a.bigint("?5")
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
        let row = scan_ref_row(self.a.dialect, &row)?;
        self.history(
            what,
            &history_table,
            &update.id,
            row.version,
            "UPDATE",
            row.image(self.a.dialect)?,
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
        let row = scan_ref_row(self.a.dialect, &row)?;
        self.history(
            what,
            &history_table,
            id,
            row.version,
            "UPDATE",
            row.image(self.a.dialect)?,
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
            .map(|m| value_of(self.a.dialect, &write_object(&member_members(k, m))))
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
        let sql = match a.dialect {
            Dialect::Sqlite => format!(
                "SELECT h.data FROM json_each(?3) AS p JOIN {} AS h \
                 ON h.id = json_extract(p.value, '$[0]') AND h._version = json_extract(p.value, '$[1]') \
                 WHERE h.graph = ?1 AND h.kind = ?2",
                a.tables.member_history
            ),
            Dialect::Postgres => format!(
                "SELECT h.data FROM jsonb_array_elements({}) AS p (value) JOIN {} AS h \
                 ON h.id = p.value ->> 0 AND h._version = CAST(p.value ->> 1 AS BIGINT) \
                 WHERE h.graph = ?1 AND h.kind = ?2",
                a.json_list("?3"),
                a.tables.member_history
            ),
        };
        let args = [
            self.graph(),
            k.name.as_str().into(),
            format!("[{}]", pin_list.join(",")).into(),
        ];
        let what = format!("read {} history", k.name);
        let rows = self.query(&what, &sql, &args).await?;
        rows.iter()
            .map(|row| {
                value_of(
                    self.a.dialect,
                    &Cells::new(self.a.dialect, row, &["data"]).text()?,
                )
            })
            .collect()
    }

    async fn read_commit(&mut self, id: &str) -> Result<Commit, Error> {
        let sql = format!(
            "SELECT {COMMIT_COLUMNS}, {} AS snapshot FROM {} AS c WHERE c.graph = ?1 AND c.id = ?2",
            self.a.flag(&self.a.has_snapshot("c.id")),
            self.a.tables.commit
        );
        let args = [self.graph(), id.into()];
        match self.first("read commit", &sql, &args).await? {
            Some(row) => scan_commit(self.a.dialect, &row),
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
        let id = self.a.new_id()?;
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
            created_at: date_time(self.a.dialect, self.time)?,
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
                self.a.new_id()?.into(),
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
             JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND chain.depth < {limit} AND NOT chain.snapshotted\
             ) SELECT {COMMIT_COLUMNS}, {snapshot} AS snapshot FROM chain JOIN {t} AS c ON c.id = chain.id ORDER BY chain.depth",
            limit = self.a.bigint("?3"),
            snapshot = self.a.flag("chain.snapshotted")
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
             JOIN chain ON c.id = chain.parent WHERE c.graph = ?1 AND c.ref_id = ?3 AND chain.depth < {limit}\
             ) SELECT {COMMIT_COLUMNS}, {snapshot} AS snapshot FROM chain JOIN {t} AS c ON c.id = chain.id ORDER BY chain.depth",
            limit = self.a.bigint("?4"),
            snapshot = self.a.flag(&self.a.has_snapshot("c.id"))
        );
        let limit = i64::try_from(limit).unwrap_or(i64::MAX);
        let args = [self.graph(), head.into(), ref_id.into(), limit.into()];
        self.commits_of("list commits", &sql, &args).await
    }

    async fn patches(&mut self, commits: &[String]) -> Result<Vec<Patch>, Error> {
        if commits.is_empty() {
            return Ok(Vec::new());
        }
        let list = match self.a.dialect {
            Dialect::Sqlite => "SELECT value FROM json_each(?2)".to_owned(),
            Dialect::Postgres => format!(
                "SELECT jsonb_array_elements_text({})",
                self.a.json_list("?2")
            ),
        };
        let sql = format!(
            "SELECT commit_id, entity_kind, entity_key, entity_id, entity_version, operation FROM {} \
             WHERE graph = ?1 AND commit_id IN ({list})",
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
                let mut c = Cells::new(self.a.dialect, row, &names);
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
            .ok_or_else(|| {
                Error::Invalid(format!("{}: no next sequence", self.a.dialect.label()))
            })?;
        Cells::new(self.a.dialect, &row, &["next"]).int()
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
                let mut c = Cells::new(self.a.dialect, row, &names);
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
                self.a.new_id()?.into(),
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
            "SELECT c.id, c.parent_commit_id, {} AS tagged, {} AS snapshot \
             FROM {} AS c WHERE c.graph = ?1",
            self.a.flag("c.sequence IS NOT NULL"),
            self.a.flag(&self.a.has_snapshot("c.id")),
            self.a.tables.commit
        );
        let args = [self.graph()];
        let rows = self.query("read the commits", &sql, &args).await?;
        let names = ["id", "parent_commit_id", "tagged", "snapshot"];
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(self.a.dialect, row, &names);
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
            Some(row) => Ok(scan_release(self.a.dialect, &row)?.0),
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
                self.a.new_id()?.into(),
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
        let (release, image) = scan_release(self.a.dialect, &row)?;
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
                "{label}: prune {} history: a batch is a whole number of images, 0 for no limit, not {batch_size}",
                k.name,
                label = self.a.dialect.label(),
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
        // No batch is no limit: -1 on SQLite, and a null on Postgres, which
        // refuses a negative one.
        let limit = match (batch_size, a.dialect) {
            (0, Dialect::Sqlite) => SqlValue::Int(-1),
            (0, Dialect::Postgres) => SqlValue::Null,
            (n, _) => SqlValue::Int(n),
        };
        let args = [self.graph(), k.name.as_str().into(), cutoff.into(), limit];
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
            match (refuse, FixedLayout::new(&descriptor.to_string(), options)) {
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

    /// The layout on Postgres is the layout on SQLite in Postgres's words:
    /// `BIGINT` for `INTEGER`, text that compares bytes, no `STRICT`, and the
    /// ref's keys to its commits added once the commit table exists.
    #[test]
    fn the_postgres_layout_is_the_sqlite_layout_in_postgres_words() {
        let sqlite = layout(Dialect::Sqlite, &default_table_name).expect("the SQLite layout");
        let postgres = layout(Dialect::Postgres, &default_table_name).expect("the Postgres layout");
        assert_eq!(postgres.len(), sqlite.len() + 4);
        for statement in &postgres {
            assert!(!statement.contains("STRICT"), "{statement}");
            assert!(!statement.contains("INTEGER"), "{statement}");
            assert_eq!(
                statement.matches("TEXT").count(),
                statement.matches("TEXT COLLATE \"C\"").count(),
                "{statement}"
            );
        }
        let keys: Vec<&String> = postgres
            .iter()
            .filter(|s| s.starts_with("ALTER TABLE"))
            .collect();
        assert_eq!(
            keys,
            [
                "ALTER TABLE \"graph_ref\" DROP CONSTRAINT IF EXISTS \"graph_ref_base_commit\"",
                "ALTER TABLE \"graph_ref\" ADD CONSTRAINT \"graph_ref_base_commit\" FOREIGN KEY (base_commit_id) \
                 REFERENCES \"graph_commit\" (id)",
                "ALTER TABLE \"graph_ref\" DROP CONSTRAINT IF EXISTS \"graph_ref_head_commit\"",
                "ALTER TABLE \"graph_ref\" ADD CONSTRAINT \"graph_ref_head_commit\" FOREIGN KEY (head_commit_id) \
                 REFERENCES \"graph_commit\" (id)",
            ]
        );
        let commit_table = postgres
            .iter()
            .position(|s| s.starts_with("CREATE TABLE IF NOT EXISTS \"graph_commit\""))
            .expect("the commit table");
        assert!(postgres
            .iter()
            .position(|s| s.starts_with("ALTER TABLE"))
            .is_some_and(|first| first > commit_table));
        // In SQLite's words again, each is SQLite's, but the ref table, whose
        // keys to its commits SQLite declares with it.
        let words: Vec<String> = postgres
            .iter()
            .filter(|s| !s.starts_with("ALTER TABLE"))
            .map(|s| {
                let s = s
                    .replace("TEXT COLLATE \"C\"", "TEXT")
                    .replace("BIGINT", "INTEGER");
                if s.starts_with("CREATE TABLE") {
                    format!("{s} STRICT")
                } else {
                    s
                }
            })
            .collect();
        let ref_table = sqlite[0].replace(" REFERENCES \"graph_commit\" (id)", "");
        assert_eq!(words[0], ref_table);
        assert_eq!(words[1..], sqlite[1..]);
    }

    #[test]
    fn times_are_canonical_date_times() {
        for (micros, want) in [
            (1_800_000_000_000_000, "2027-01-15T08:00:00Z"),
            (1_800_000_000_120_000, "2027-01-15T08:00:00.12Z"),
            (1_800_000_000_000_001, "2027-01-15T08:00:00.000001Z"),
            (0, "1970-01-01T00:00:00Z"),
            (-1, "1969-12-31T23:59:59.999999Z"),
        ] {
            assert_eq!(
                micros_to_date_time(micros).expect("a time"),
                want,
                "{micros}"
            );
        }
        for (micros, want) in [
            (-62_167_219_200_000_000, "0000-01-01T00:00:00Z"),
            (253_402_300_799_999_999, "9999-12-31T23:59:59.999999Z"),
        ] {
            let text = canonical::date_time_of_micros(micros).expect("a time");
            assert_eq!(text, want, "{micros}");
        }
    }

    /// A time holds 2^53 - 1 microseconds either side of the epoch, and a
    /// year from 0000 to 9999: past the first a time is refused before its
    /// date is read, and the year rule still holds for any time it reads.
    #[test]
    fn a_time_holds_2_to_the_53_microseconds_and_a_year_from_0000_to_9999() {
        for micros in [MAX_MICROS, -MAX_MICROS] {
            micros_to_date_time(micros).expect("in range");
        }
        assert_eq!(
            micros_to_date_time(MAX_MICROS).expect("in range"),
            "2255-06-05T23:47:34.740991Z"
        );
        for micros in [MAX_MICROS + 1, -MAX_MICROS - 1, i64::MAX, i64::MIN] {
            let error = micros_to_date_time(micros).expect_err("out of range");
            assert!(
                error.to_string().contains("outside the 2^53 - 1"),
                "{micros}: {error}"
            );
        }
        for micros in [-62_167_219_200_000_001, 253_402_300_800_000_000] {
            let error = canonical::date_time_of_micros(micros).expect_err("out of years");
            assert!(
                error.to_string().contains("outside the years 0000-9999"),
                "{micros}: {error}"
            );
        }
        canonical::date_time_of_micros(-62_167_219_200_000_000).expect("0000-01-01");
        canonical::date_time_of_micros(253_402_300_799_999_999).expect("9999-12-31");
    }
}
