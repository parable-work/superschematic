//! The Postgres storage adapter (D19). It builds its statements at run time
//! from a graph's descriptor (version 2), which names the graph's tables,
//! each kind's role columns and every column's value class, and it returns
//! every row as a canonical row through [`crate::canonical`].
//!
//! The graph's own tables have the columns the loader gives them (D17, D19):
//! a ref's `root_id`, `parent_ref_id`, `base_commit_id`, `head_commit_id`,
//! `name`, `sealed_at`, audit and soft-delete columns and `_version`; a
//! commit's `root_id`, `ref_id`, `parent_commit_id`, `message`,
//! `schema_epoch`, `content_hash`, `sequence`, `created_at` and
//! `created_by`; a patch's `commit_id`, `entity_kind`, `entity_key`,
//! `entity_id`, `entity_version` and `operation`; a snapshot entry's
//! `commit_id`, `entity_kind`, `entity_key`, `entity_id` and
//! `entity_version`; and a release pointer's `root_id`, `commit_id`, audit
//! columns and `_version`. A history table keys its images on the kind's id
//! and version columns and holds each in `data`.
//!
//! The adapter reaches Postgres through [`Client`]; [`TokioPostgres`] binds
//! tokio-postgres. Every row it reads is `to_jsonb` of the row, or the
//! history image, as text, which the canonical rules read as the Go adapter's
//! do.

mod client;
mod values;

use std::collections::{BTreeMap, HashMap};
use std::sync::Arc;
use std::time::Duration;

use async_trait::async_trait;
use serde::Deserialize;
use serde_json::{Map, Value};

use crate::canonical;
use crate::storage::{
    Commit, CommitNode, NewCommit, NewRef, Patch, Pin, Ref, RefUpdate, Release, ReleaseWrite,
    RowWrite, SnapshotEntry, Storage, Tx,
};
use crate::Error;
#[cfg(feature = "tokio-postgres")]
pub use client::TokioPostgres;
pub use client::{Client, ClientError, Conn, SqlValue};
use values::{canonical_uuid, input_value, optional_canonical_uuid, optional_uuid_text, uuid_text};

/// The transaction-local setting a versioned table's history trigger reads a
/// hard delete's actor from, unless the schema's `history_actor_setting`
/// naming key names another.
pub const DEFAULT_HISTORY_ACTOR_SETTING: &str = "superschematic.history_actor_id";

// The audit columns the adapter writes on every row when a kind has them.
const CREATED_AT: &str = "created_at";
const CREATED_BY: &str = "created_by";
const UPDATED_AT: &str = "updated_at";
const UPDATED_BY: &str = "updated_by";

/// Configures an [`Adapter`].
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct Options {
    /// The setting the history triggers read a hard delete's actor from:
    /// the schema's `history_actor_setting` naming key. Empty is
    /// [`DEFAULT_HISTORY_ACTOR_SETTING`].
    pub history_actor_setting: String,
}

/// One graph's statements. Bind a [`Client`] with [`Adapter::storage`].
#[derive(Debug)]
pub struct Adapter {
    actor_setting: String,
    sweep_key: String,
    ref_table: String,
    commit_table: String,
    patch_table: String,
    release_table: String,
    snapshot_table: String,
    root_table: String,
    root_key: String,
    kinds: HashMap<String, Kind>,
}

/// One member kind's tables and columns.
#[derive(Debug)]
struct Kind {
    name: String,
    table: String,
    history_table: String,
    key: String,
    id: String,
    ref_column: String,
    root: String,
    tombstone: String,
    version: String,
    columns: BTreeMap<String, String>,
}

/// What the adapter reads of a graph descriptor.
#[derive(Deserialize)]
struct Descriptor {
    #[serde(default)]
    version: i64,
    #[serde(default)]
    root: DescriptorRoot,
    #[serde(default, rename = "refTable")]
    ref_table: String,
    #[serde(default, rename = "commitTable")]
    commit_table: String,
    #[serde(default, rename = "patchTable")]
    patch_table: String,
    #[serde(default, rename = "releaseTable")]
    release_table: String,
    #[serde(default, rename = "snapshotTable")]
    snapshot_table: String,
    #[serde(default)]
    kinds: Vec<DescriptorKind>,
}

#[derive(Deserialize, Default)]
struct DescriptorRoot {
    #[serde(default)]
    table: String,
    #[serde(default)]
    key: String,
}

#[derive(Deserialize)]
struct DescriptorKind {
    kind: String,
    #[serde(default)]
    table: String,
    #[serde(default, rename = "historyTable")]
    history_table: String,
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
    columns: BTreeMap<String, String>,
}

/// Quotes an identifier.
fn quote(name: &str) -> String {
    format!("\"{}\"", name.replace('"', "\"\""))
}

/// Quotes a string as a SQL literal.
fn literal(s: &str) -> String {
    format!("'{}'", s.replace('\'', "''"))
}

fn failed(what: &str, error: ClientError) -> Error {
    Error::Storage(format!("postgres: {what}: {error}").into())
}

impl Adapter {
    /// Reads a graph's descriptor. The core checks the rest of it; the
    /// adapter needs its tables, each kind's role columns, the root column
    /// among them, and each kind's columns.
    pub fn new(descriptor: &str, options: Options) -> Result<Self, Error> {
        let d: Descriptor = serde_json::from_str(descriptor)
            .map_err(|e| Error::Invalid(format!("postgres: read the descriptor: {e}")))?;
        if d.version != 2 {
            return Err(Error::Invalid(format!(
                "postgres: descriptor version {}; this adapter reads version 2",
                d.version
            )));
        }
        for (member, value) in [
            ("root table", &d.root.table),
            ("root key", &d.root.key),
            ("refTable", &d.ref_table),
            ("commitTable", &d.commit_table),
            ("patchTable", &d.patch_table),
            ("releaseTable", &d.release_table),
            ("snapshotTable", &d.snapshot_table),
        ] {
            if value.is_empty() {
                return Err(Error::Invalid(format!(
                    "postgres: the descriptor's {member} is empty"
                )));
            }
        }
        let mut kinds = HashMap::new();
        for k in d.kinds {
            for (role, column) in [
                ("table", &k.table),
                ("historyTable", &k.history_table),
                ("key", &k.key),
                ("id", &k.id),
                ("ref", &k.ref_column),
                ("root", &k.root),
                ("tombstone", &k.tombstone),
                ("version", &k.version),
            ] {
                if column.is_empty() {
                    return Err(Error::Invalid(format!(
                        "postgres: kind {:?} has no {role}",
                        k.kind
                    )));
                }
                if role != "table" && role != "historyTable" && !k.columns.contains_key(column) {
                    return Err(Error::Invalid(format!(
                        "postgres: kind {:?} {role} column {column:?} is not in its columns",
                        k.kind
                    )));
                }
            }
            kinds.insert(
                k.kind.clone(),
                Kind {
                    name: k.kind,
                    table: k.table,
                    history_table: k.history_table,
                    key: k.key,
                    id: k.id,
                    ref_column: k.ref_column,
                    root: k.root,
                    tombstone: k.tombstone,
                    version: k.version,
                    columns: k.columns,
                },
            );
        }
        let actor_setting = if options.history_actor_setting.is_empty() {
            DEFAULT_HISTORY_ACTOR_SETTING.to_owned()
        } else {
            options.history_actor_setting
        };
        Ok(Adapter {
            actor_setting,
            sweep_key: format!("superschematic.versiongraph.sweep:{}", d.ref_table),
            ref_table: quote(&d.ref_table),
            commit_table: quote(&d.commit_table),
            patch_table: quote(&d.patch_table),
            release_table: quote(&d.release_table),
            snapshot_table: quote(&d.snapshot_table),
            root_table: quote(&d.root.table),
            root_key: quote(&d.root.key),
            kinds,
        })
    }

    /// Binds the adapter to a client.
    pub fn storage<C: Client + 'static>(self: &Arc<Self>, client: C) -> PostgresStorage<C> {
        PostgresStorage {
            adapter: Arc::clone(self),
            client,
        }
    }

    fn kind(&self, name: &str) -> Result<&Kind, Error> {
        self.kinds
            .get(name)
            .ok_or_else(|| Error::Invalid(format!("postgres: unknown kind {name:?}")))
    }

    /// The SQL that tells whether the commit whose id is `id_sql` has a
    /// snapshot.
    fn has_snapshot(&self, id_sql: &str) -> String {
        format!(
            "EXISTS (SELECT 1 FROM {} AS s WHERE s.commit_id = {id_sql})",
            self.snapshot_table
        )
    }
}

/// An [`Adapter`] bound to a [`Client`]: the graph's [`Storage`].
pub struct PostgresStorage<C> {
    adapter: Arc<Adapter>,
    client: C,
}

impl<C> PostgresStorage<C> {
    /// The bound client.
    pub fn client(&self) -> &C {
        &self.client
    }
}

#[async_trait]
impl<C: Client + 'static> Storage for PostgresStorage<C> {
    async fn begin(&self) -> Result<Box<dyn Tx + '_>, Error> {
        let conn = self.client.begin().await.map_err(|e| failed("begin", e))?;
        Ok(Box::new(PgTx {
            a: &self.adapter,
            conn,
        }))
    }
}

struct PgTx<'a> {
    a: &'a Adapter,
    conn: Box<dyn Conn + 'a>,
}

/// A ref's graph columns, followed by whether it is sealed and discarded
/// and its version.
const REF_COLUMNS: &str = "id::text, root_id::text, COALESCE(parent_ref_id::text, ''), COALESCE(base_commit_id::text, ''), \
     COALESCE(head_commit_id::text, ''), \"name\", sealed_at IS NOT NULL, deleted_at IS NOT NULL, _version";

/// A commit's columns, followed by whether it has a snapshot.
const COMMIT_COLUMNS: &str = "id::text, root_id::text, ref_id::text, COALESCE(parent_commit_id::text, ''), COALESCE(message, ''), \
     schema_epoch, content_hash, COALESCE(\"sequence\"::text, ''), to_jsonb(created_at)::text, created_by::text";

/// A release pointer's columns.
const RELEASE_COLUMNS: &str = "id::text, root_id::text, commit_id::text, _version";

/// Reads one row's columns.
struct Cells<'r> {
    cells: std::slice::Iter<'r, SqlValue>,
}

impl<'r> Cells<'r> {
    fn new(row: &'r [SqlValue]) -> Self {
        Cells { cells: row.iter() }
    }

    fn next(&mut self) -> Result<&'r SqlValue, Error> {
        self.cells
            .next()
            .ok_or_else(|| Error::Invalid("postgres: a row has too few columns".to_owned()))
    }

    fn text(&mut self) -> Result<String, Error> {
        match self.next()? {
            SqlValue::Text(s) => Ok(s.clone()),
            other => Err(Error::Invalid(format!("postgres: {other:?} is not a text"))),
        }
    }

    fn int(&mut self) -> Result<i64, Error> {
        match self.next()? {
            SqlValue::Int(n) => Ok(*n),
            other => Err(Error::Invalid(format!(
                "postgres: {other:?} is not an integer"
            ))),
        }
    }

    fn boolean(&mut self) -> Result<bool, Error> {
        match self.next()? {
            SqlValue::Bool(b) => Ok(*b),
            other => Err(Error::Invalid(format!(
                "postgres: {other:?} is not a boolean"
            ))),
        }
    }
}

fn scan_ref(row: &[SqlValue]) -> Result<Ref, Error> {
    let mut c = Cells::new(row);
    Ok(Ref {
        id: canonical_uuid(&c.text()?)?,
        root: canonical_uuid(&c.text()?)?,
        parent: optional_canonical_uuid(&c.text()?)?,
        base: optional_canonical_uuid(&c.text()?)?,
        head: optional_canonical_uuid(&c.text()?)?,
        name: c.text()?,
        sealed: c.boolean()?,
        discarded: c.boolean()?,
        version: c.int()?,
    })
}

fn scan_commit(row: &[SqlValue]) -> Result<Commit, Error> {
    let mut c = Cells::new(row);
    let id = canonical_uuid(&c.text()?)?;
    let root = canonical_uuid(&c.text()?)?;
    let ref_id = canonical_uuid(&c.text()?)?;
    let parent = optional_canonical_uuid(&c.text()?)?;
    let message = c.text()?;
    let schema_epoch = c.int()?;
    let content_hash = c.text()?;
    let sequence = match c.text()?.as_str() {
        "" => None,
        text => Some(
            text.parse()
                .map_err(|e| Error::Invalid(format!("postgres: sequence {text:?}: {e}")))?,
        ),
    };
    let created_at = canonical::postgres(canonical::DATE_TIME, &c.text()?)?;
    let created_at = created_at.trim_matches('"').to_owned();
    let created_by = canonical_uuid(&c.text()?)?;
    let snapshot = c.boolean()?;
    Ok(Commit {
        id,
        root,
        ref_id,
        parent,
        message,
        schema_epoch,
        content_hash,
        sequence,
        created_at,
        created_by,
        snapshot,
    })
}

fn scan_release(row: &[SqlValue]) -> Result<Release, Error> {
    let mut c = Cells::new(row);
    Ok(Release {
        id: canonical_uuid(&c.text()?)?,
        root: canonical_uuid(&c.text()?)?,
        commit: canonical_uuid(&c.text()?)?,
        version: c.int()?,
    })
}

/// Whether the client error is Postgres's unique_violation.
fn is_unique_violation(error: &ClientError) -> bool {
    error.code.as_deref() == Some("23505")
}

impl PgTx<'_> {
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

    async fn execute(&mut self, what: &str, sql: &str, args: &[SqlValue]) -> Result<u64, Error> {
        self.conn
            .execute(sql, args)
            .await
            .map_err(|e| failed(what, e))
    }

    /// Runs a statement whose one column is a row's JSON as text, and
    /// returns each as the canonical row of `kind`.
    async fn kind_rows(
        &mut self,
        what: &str,
        kind: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Value>, Error> {
        let k = self.a.kind(kind)?;
        let (name, columns) = (k.name.clone(), k.columns.clone());
        let rows = self.query(what, sql, args).await?;
        rows.iter()
            .map(|row| {
                let text = Cells::new(row).text()?;
                let canonical = canonical::postgres_row(&columns, &text).map_err(|mut e| {
                    e.message = format!("{name} row: {}", e.message);
                    Error::Canonical(e)
                })?;
                serde_json::from_str(&canonical)
                    .map_err(|e| Error::Invalid(format!("postgres: reread a canonical row: {e}")))
            })
            .collect()
    }

    async fn scan_one_ref(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Option<Ref>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.first().map(|row| scan_ref(row)).transpose()
    }

    async fn scan_refs(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Ref>, Error> {
        let rows = self.query("read refs", sql, args).await?;
        rows.iter().map(|row| scan_ref(row)).collect()
    }

    async fn scan_commits(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Vec<Commit>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.iter().map(|row| scan_commit(row)).collect()
    }

    async fn scan_one_release(
        &mut self,
        what: &str,
        sql: &str,
        args: &[SqlValue],
    ) -> Result<Option<Release>, Error> {
        let rows = self.query(what, sql, args).await?;
        rows.first().map(|row| scan_release(row)).transpose()
    }

    async fn read_ref_locked(&mut self, id: &str, lock: &str) -> Result<Ref, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} WHERE id = $1::uuid{lock}",
            self.a.ref_table
        );
        self.scan_one_ref("read ref", &sql, &[uuid_text(id)?.into()])
            .await?
            .ok_or(Error::NotFound)
    }

    /// Clears the history actor setting a delete set for its statement.
    async fn clear_history_actor(&mut self) -> Result<(), Error> {
        let setting = self.a.actor_setting.clone();
        self.execute(
            "clear the history actor",
            "SELECT set_config($1, '', true)",
            &[setting.into()],
        )
        .await?;
        Ok(())
    }
}

/// A duration in whole microseconds, as a statement multiplies an interval
/// of one microsecond by it.
fn micros(d: Duration) -> i64 {
    i64::try_from(d.as_micros()).unwrap_or(i64::MAX)
}

#[async_trait]
impl Tx for PgTx<'_> {
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
        let args = [
            uuid_text(&new.root)?.into(),
            optional_uuid_text(new.parent.as_deref())?.into(),
            optional_uuid_text(new.base.as_deref())?.into(),
            new.name.clone().into(),
            uuid_text(&new.actor)?.into(),
        ];
        let sql = format!(
            "INSERT INTO {} (root_id, parent_ref_id, base_commit_id, \"name\", created_by, updated_by) \
             VALUES ($1::uuid, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5::uuid, $5::uuid) RETURNING {REF_COLUMNS}",
            self.a.ref_table
        );
        match self.conn.query(&sql, &args).await {
            Ok(rows) => rows
                .first()
                .map(|row| scan_ref(row))
                .transpose()?
                .ok_or_else(|| Error::Invalid("postgres: create ref: no row".to_owned())),
            Err(e) if is_unique_violation(&e) => Err(Error::NameTaken(new.name)),
            Err(e) => Err(failed("create ref", e)),
        }
    }

    async fn read_ref(&mut self, id: &str) -> Result<Ref, Error> {
        self.read_ref_locked(id, "").await
    }

    async fn lock_ref(&mut self, id: &str) -> Result<Ref, Error> {
        self.read_ref_locked(id, " FOR UPDATE").await
    }

    async fn update_ref(&mut self, update: RefUpdate) -> Result<Ref, Error> {
        let args = [
            uuid_text(&update.id)?.into(),
            update.version.into(),
            optional_uuid_text(update.head.as_deref())?.into(),
            update.seal.into(),
            uuid_text(&update.actor)?.into(),
            optional_uuid_text(update.base.as_deref())?.into(),
        ];
        let sql = format!(
            "UPDATE {} SET head_commit_id = COALESCE(NULLIF($3, '')::uuid, head_commit_id), \
             base_commit_id = COALESCE(NULLIF($6, '')::uuid, base_commit_id), \
             sealed_at = CASE WHEN $4::boolean THEN now() ELSE sealed_at END, updated_at = now(), updated_by = $5::uuid \
             WHERE id = $1::uuid AND _version = $2 RETURNING {REF_COLUMNS}",
            self.a.ref_table
        );
        self.scan_one_ref("update ref", &sql, &args)
            .await?
            .ok_or(Error::VersionConflict)
    }

    async fn discard_ref(&mut self, id: &str, version: i64, actor: &str) -> Result<(), Error> {
        let sql = format!(
            "UPDATE {} SET deleted_at = now(), deleted_by = $3::uuid \
             WHERE id = $1::uuid AND _version = $2 AND deleted_at IS NULL",
            self.a.ref_table
        );
        let n = self
            .execute(
                "discard ref",
                &sql,
                &[
                    uuid_text(id)?.into(),
                    version.into(),
                    uuid_text(actor)?.into(),
                ],
            )
            .await?;
        if n == 0 {
            return Err(Error::VersionConflict);
        }
        Ok(())
    }

    async fn rows(&mut self, kind: &str, ref_id: &str) -> Result<Vec<Value>, Error> {
        let k = self.a.kind(kind)?;
        let sql = format!(
            "SELECT to_jsonb(t)::text FROM {} AS t WHERE t.{} = $1::uuid",
            quote(&k.table),
            quote(&k.ref_column)
        );
        let what = format!("read {} rows", k.name);
        self.kind_rows(&what, kind, &sql, &[uuid_text(ref_id)?.into()])
            .await
    }

    async fn upsert_row(&mut self, kind: &str, write: RowWrite) -> Result<Value, Error> {
        let k = self.a.kind(kind)?;
        let Value::Object(mut members) = write.row else {
            return Err(Error::Invalid(format!(
                "postgres: a {} row is a JSON object",
                k.name
            )));
        };
        members.remove(&k.id);
        members.remove(&k.version);
        if members.get(&k.key).is_some_and(Value::is_null) {
            members.remove(&k.key);
        }
        members.insert(k.tombstone.clone(), Value::Bool(write.tombstone));
        members.insert(k.ref_column.clone(), Value::String(write.ref_id));
        members.insert(k.root.clone(), Value::String(write.root));
        for column in [CREATED_BY, UPDATED_BY] {
            if k.columns.contains_key(column) {
                members.insert(column.to_owned(), Value::String(write.actor.clone()));
            }
        }
        let mut input = Map::new();
        let mut columns: Vec<String> = Vec::with_capacity(members.len() + 2);
        for (column, value) in &members {
            let Some(class) = k.columns.get(column) else {
                return Err(Error::Invalid(format!(
                    "postgres: the {} row has column {column:?}, which its descriptor does not declare",
                    k.name
                )));
            };
            let value = input_value(class, value).map_err(|e| {
                Error::Invalid(format!("postgres: {} column {column}: {e}", k.name))
            })?;
            input.insert(column.clone(), value);
            columns.push(column.clone());
        }
        for column in [CREATED_AT, UPDATED_AT] {
            if k.columns.contains_key(column) && !input.contains_key(column) {
                columns.push(column.to_owned());
            }
        }
        columns.sort();
        let input = serde_json::to_string(&Value::Object(input))
            .map_err(|e| Error::Invalid(format!("postgres: encode a {} row: {e}", k.name)))?;

        let (mut names, mut values, mut updates) = (Vec::new(), Vec::new(), Vec::new());
        for column in &columns {
            let q = quote(column);
            let class = k.columns[column].as_str();
            names.push(q.clone());
            if column == CREATED_AT || column == UPDATED_AT {
                values.push("now()".to_owned());
            } else if class == canonical::JSON || class.ends_with("[][]") {
                // A JSONB column takes the member itself, so a JSON null is
                // the value null, which a required column holds, rather than
                // SQL NULL, which jsonb_populate_record reads it as.
                values.push(format!("($1::jsonb -> {})", literal(column)));
            } else {
                values.push(format!("r.{q}"));
            }
            let kept = [
                k.key.as_str(),
                k.ref_column.as_str(),
                k.root.as_str(),
                CREATED_AT,
                CREATED_BY,
            ];
            if !kept.contains(&column.as_str()) {
                // The conflict key, the root and the creation audit stay as
                // the row was first written.
                updates.push(format!("{q} = EXCLUDED.{q}"));
            }
        }
        let table = quote(&k.table);
        let sql = format!(
            "INSERT INTO {table} AS t ({}) SELECT {} FROM jsonb_populate_record(NULL::{table}, $1::jsonb) AS r \
             ON CONFLICT ({}, {}) DO UPDATE SET {} RETURNING to_jsonb(t)::text",
            names.join(", "),
            values.join(", "),
            quote(&k.key),
            quote(&k.ref_column),
            updates.join(", ")
        );
        let what = format!("write {} row", k.name);
        let mut rows = self.kind_rows(&what, kind, &sql, &[input.into()]).await?;
        if rows.len() != 1 {
            return Err(Error::Invalid(format!(
                "postgres: {what}: {} rows written",
                rows.len()
            )));
        }
        Ok(rows.remove(0))
    }

    async fn remove_row(
        &mut self,
        kind: &str,
        ref_id: &str,
        entity_key: &str,
        actor: &str,
    ) -> Result<bool, Error> {
        let k = self.a.kind(kind)?;
        let sql = format!(
            "WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) \
             DELETE FROM {} USING history_actor WHERE {} = $3::uuid AND {} = $4::uuid",
            quote(&k.table),
            quote(&k.key),
            quote(&k.ref_column)
        );
        let what = format!("remove the {} row", k.name);
        let args = [
            self.a.actor_setting.clone().into(),
            uuid_text(actor)?.into(),
            uuid_text(entity_key)?.into(),
            uuid_text(ref_id)?.into(),
        ];
        let n = self.execute(&what, &sql, &args).await?;
        self.clear_history_actor().await?;
        Ok(n > 0)
    }

    async fn images(&mut self, kind: &str, pins: &[Pin]) -> Result<Vec<Value>, Error> {
        let k = self.a.kind(kind)?;
        if pins.is_empty() {
            return Ok(Vec::new());
        }
        let ids = pins
            .iter()
            .map(|pin| uuid_text(&pin.id))
            .collect::<Result<Vec<_>, _>>()?;
        let versions = pins.iter().map(|pin| pin.version).collect();
        let sql = format!(
            "SELECT h.data::text FROM {} AS h \
             JOIN unnest($1::text[]::uuid[], $2::bigint[]) AS p(id, version) ON h.{} = p.id AND h.{} = p.version",
            quote(&k.history_table),
            quote(&k.id),
            quote(&k.version)
        );
        let what = format!("read {} history", k.name);
        self.kind_rows(
            &what,
            kind,
            &sql,
            &[SqlValue::TextList(ids), SqlValue::IntList(versions)],
        )
        .await
    }

    async fn read_commit(&mut self, id: &str) -> Result<Commit, Error> {
        let sql = format!(
            "SELECT {COMMIT_COLUMNS}, {} FROM {} AS c WHERE id = $1::uuid",
            self.a.has_snapshot("c.id"),
            self.a.commit_table
        );
        let mut commits = self
            .scan_commits("read commit", &sql, &[uuid_text(id)?.into()])
            .await?;
        if commits.is_empty() {
            return Err(Error::NotFound);
        }
        Ok(commits.remove(0))
    }

    async fn insert_commit(&mut self, commit: NewCommit) -> Result<Commit, Error> {
        let sequence = commit.sequence.map_or(SqlValue::Null, SqlValue::Int);
        let args = [
            uuid_text(&commit.root)?.into(),
            uuid_text(&commit.ref_id)?.into(),
            optional_uuid_text(commit.parent.as_deref())?.into(),
            commit.message.clone().into(),
            commit.schema_epoch.into(),
            commit.content_hash.clone().into(),
            sequence,
            uuid_text(&commit.actor)?.into(),
        ];
        let sql = format!(
            "INSERT INTO {} (root_id, ref_id, parent_commit_id, message, schema_epoch, content_hash, \"sequence\", created_by) \
             VALUES ($1::uuid, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, ''), $5, $6, $7::bigint, $8::uuid) \
             RETURNING {COMMIT_COLUMNS}, false",
            self.a.commit_table
        );
        let mut commits = self.scan_commits("write commit", &sql, &args).await?;
        if commits.is_empty() {
            return Err(Error::Invalid("postgres: write commit: no row".to_owned()));
        }
        Ok(commits.remove(0))
    }

    async fn insert_patches(&mut self, commit: &str, patches: &[Patch]) -> Result<(), Error> {
        let rows = patches
            .iter()
            .map(|p| {
                Ok(serde_json::json!({
                    "kind": p.kind,
                    "key": uuid_text(&p.entity_key)?,
                    "id": uuid_text(&p.entity_id)?,
                    "version": p.entity_version,
                    "op": p.operation,
                }))
            })
            .collect::<Result<Vec<_>, Error>>()?;
        let sql = format!(
            "INSERT INTO {} (commit_id, entity_kind, entity_key, entity_id, entity_version, operation) \
             SELECT $1::uuid, p.kind, p.key::uuid, p.id::uuid, p.version, p.op \
             FROM jsonb_to_recordset($2::jsonb) AS p(kind text, key text, id text, version bigint, op text)",
            self.a.patch_table
        );
        self.execute(
            "write patches",
            &sql,
            &[
                uuid_text(commit)?.into(),
                Value::Array(rows).to_string().into(),
            ],
        )
        .await?;
        Ok(())
    }

    async fn walk(&mut self, commit: &str, limit: usize) -> Result<Vec<Commit>, Error> {
        // The walk carries each commit's snapshot flag, and goes no further
        // than the first commit that has one.
        let t = &self.a.commit_table;
        let sql = format!(
            "WITH RECURSIVE chain AS (\
             SELECT c.*, 1 AS depth, {has} AS snapshotted FROM {t} AS c WHERE c.id = $1::uuid \
             UNION ALL SELECT c.*, chain.depth + 1, {has} FROM {t} AS c \
             JOIN chain ON c.id = chain.parent_commit_id WHERE chain.depth < $2 AND NOT chain.snapshotted\
             ) SELECT {COMMIT_COLUMNS}, snapshotted FROM chain ORDER BY depth",
            has = self.a.has_snapshot("c.id"),
        );
        let limit = i64::try_from(limit).unwrap_or(i64::MAX);
        self.scan_commits(
            "walk commits",
            &sql,
            &[uuid_text(commit)?.into(), limit.into()],
        )
        .await
    }

    async fn ref_commits(
        &mut self,
        ref_id: &str,
        head: &str,
        limit: usize,
    ) -> Result<Vec<Commit>, Error> {
        let t = &self.a.commit_table;
        let sql = format!(
            "WITH RECURSIVE chain AS (\
             SELECT c.*, 1 AS depth FROM {t} AS c WHERE c.id = $1::uuid AND c.ref_id = $2::uuid \
             UNION ALL SELECT c.*, chain.depth + 1 FROM {t} AS c \
             JOIN chain ON c.id = chain.parent_commit_id WHERE c.ref_id = $2::uuid AND chain.depth < $3\
             ) SELECT {COMMIT_COLUMNS}, {} FROM chain ORDER BY depth",
            self.a.has_snapshot("chain.id"),
        );
        let limit = i64::try_from(limit).unwrap_or(i64::MAX);
        self.scan_commits(
            "list commits",
            &sql,
            &[
                uuid_text(head)?.into(),
                uuid_text(ref_id)?.into(),
                limit.into(),
            ],
        )
        .await
    }

    async fn patches(&mut self, commits: &[String]) -> Result<Vec<Patch>, Error> {
        if commits.is_empty() {
            return Ok(Vec::new());
        }
        let ids = commits
            .iter()
            .map(|id| uuid_text(id))
            .collect::<Result<Vec<_>, _>>()?;
        let sql = format!(
            "SELECT commit_id::text, entity_kind, entity_key::text, entity_id::text, entity_version, operation \
             FROM {} WHERE commit_id = ANY($1::text[]::uuid[])",
            self.a.patch_table
        );
        let rows = self
            .query("read patches", &sql, &[SqlValue::TextList(ids)])
            .await?;
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row);
                Ok(Patch {
                    commit: canonical_uuid(&c.text()?)?,
                    kind: c.text()?,
                    entity_key: canonical_uuid(&c.text()?)?,
                    entity_id: canonical_uuid(&c.text()?)?,
                    entity_version: c.int()?,
                    operation: c.text()?,
                })
            })
            .collect()
    }

    async fn next_sequence(&mut self, root: &str) -> Result<i64, Error> {
        let root = uuid_text(root)?;
        // FOR NO KEY UPDATE conflicts with itself and not with the key-share
        // locks that inserting a row that references the root takes.
        let lock = format!(
            "SELECT 1 FROM {} WHERE {} = $1::uuid FOR NO KEY UPDATE",
            self.a.root_table, self.a.root_key
        );
        self.execute("lock the root", &lock, &[root.clone().into()])
            .await?;
        let sql = format!(
            "SELECT COALESCE(MAX(\"sequence\"), 0) + 1 FROM {} WHERE root_id = $1::uuid",
            self.a.commit_table
        );
        let rows = self
            .query("read the next sequence", &sql, &[root.into()])
            .await?;
        let row = rows
            .first()
            .ok_or_else(|| Error::Invalid("postgres: no next sequence".to_owned()))?;
        Cells::new(row).int()
    }

    async fn snapshot(&mut self, commit: &str) -> Result<Vec<SnapshotEntry>, Error> {
        let sql = format!(
            "SELECT entity_kind, entity_key::text, entity_id::text, entity_version FROM {} WHERE commit_id = $1::uuid",
            self.a.snapshot_table
        );
        let rows = self
            .query("read the snapshot", &sql, &[uuid_text(commit)?.into()])
            .await?;
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row);
                Ok(SnapshotEntry {
                    kind: c.text()?,
                    entity_key: canonical_uuid(&c.text()?)?,
                    entity_id: canonical_uuid(&c.text()?)?,
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
        let rows = entries
            .iter()
            .map(|e| {
                Ok(serde_json::json!({
                    "kind": e.kind,
                    "key": uuid_text(&e.entity_key)?,
                    "id": uuid_text(&e.entity_id)?,
                    "version": e.entity_version,
                }))
            })
            .collect::<Result<Vec<_>, Error>>()?;
        let sql = format!(
            "INSERT INTO {} (commit_id, entity_kind, entity_key, entity_id, entity_version) \
             SELECT $1::uuid, e.kind, e.key::uuid, e.id::uuid, e.version \
             FROM jsonb_to_recordset($2::jsonb) AS e(kind text, key text, id text, version bigint)",
            self.a.snapshot_table
        );
        self.execute(
            "write the snapshot",
            &sql,
            &[
                uuid_text(commit)?.into(),
                Value::Array(rows).to_string().into(),
            ],
        )
        .await?;
        Ok(())
    }

    async fn commits(&mut self) -> Result<Vec<CommitNode>, Error> {
        let sql = format!(
            "SELECT c.id::text, COALESCE(c.parent_commit_id::text, ''), c.\"sequence\" IS NOT NULL, {} FROM {} AS c",
            self.a.has_snapshot("c.id"),
            self.a.commit_table
        );
        let rows = self.query("read the commits", &sql, &[]).await?;
        rows.iter()
            .map(|row| {
                let mut c = Cells::new(row);
                Ok(CommitNode {
                    id: canonical_uuid(&c.text()?)?,
                    parent: optional_canonical_uuid(&c.text()?)?,
                    tagged: c.boolean()?,
                    snapshot: c.boolean()?,
                })
            })
            .collect()
    }

    async fn read_release(&mut self, root: &str) -> Result<Release, Error> {
        let sql = format!(
            "SELECT {RELEASE_COLUMNS} FROM {} WHERE root_id = $1::uuid",
            self.a.release_table
        );
        self.scan_one_release("read the release", &sql, &[uuid_text(root)?.into()])
            .await?
            .ok_or(Error::NotFound)
    }

    async fn write_release(&mut self, write: ReleaseWrite) -> Result<Release, Error> {
        let mut args = vec![
            uuid_text(&write.root)?.into(),
            uuid_text(&write.commit)?.into(),
            uuid_text(&write.actor)?.into(),
        ];
        let sql = if write.version == 0 {
            // A root's first pointer. Another writer's first pointer takes the
            // root's slot, as a move at a stale version would.
            format!(
                "INSERT INTO {} (root_id, commit_id, created_by, updated_by) \
                 VALUES ($1::uuid, $2::uuid, $3::uuid, $3::uuid) ON CONFLICT (root_id) DO NOTHING RETURNING {RELEASE_COLUMNS}",
                self.a.release_table
            )
        } else {
            args.push(write.version.into());
            format!(
                "UPDATE {} SET commit_id = $2::uuid, updated_at = now(), updated_by = $3::uuid \
                 WHERE root_id = $1::uuid AND _version = $4 RETURNING {RELEASE_COLUMNS}",
                self.a.release_table
            )
        };
        self.scan_one_release("write the release", &sql, &args)
            .await?
            .ok_or(Error::VersionConflict)
    }

    async fn prune(
        &mut self,
        kind: &str,
        retention_days: i64,
        batch_size: i64,
    ) -> Result<i64, Error> {
        let k = self.a.kind(kind)?;
        let name = k.name.clone();
        // A kind declared without retentionDays has no prune function, and
        // keeps its history.
        let function = quote(&format!("{}_prune_history", k.table));
        let rows = self
            .query(
                &format!("find the {name} prune function"),
                "SELECT to_regproc($1) IS NOT NULL",
                &[function.clone().into()],
            )
            .await?;
        let exists = match rows.first() {
            Some(row) => Cells::new(row).boolean()?,
            None => false,
        };
        if !exists {
            return Ok(0);
        }
        // The prune function's retention_days defaults to the kind's declared
        // retention, which a retention of 0 keeps.
        let (sql, args) = if retention_days == 0 {
            (
                format!("SELECT {function}(max_rows => NULLIF($1, 0)::integer)"),
                vec![batch_size.into()],
            )
        } else {
            (
                format!(
                    "SELECT {function}(max_rows => NULLIF($2, 0)::integer, retention_days => $1::integer)"
                ),
                vec![retention_days.into(), batch_size.into()],
            )
        };
        let rows = self
            .query(&format!("prune {name} history"), &sql, &args)
            .await?;
        match rows.first() {
            Some(row) => Cells::new(row).int(),
            None => Ok(0),
        }
    }

    async fn discarded_refs(&mut self, grace: Duration) -> Result<Vec<Ref>, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} \
             WHERE deleted_at IS NOT NULL AND deleted_at < now() - $1::bigint * interval '1 microsecond' ORDER BY deleted_at, id",
            self.a.ref_table
        );
        self.scan_refs(&sql, &[micros(grace).into()]).await
    }

    async fn idle_drafts(&mut self, idle: Duration) -> Result<Vec<Ref>, Error> {
        let sql = format!(
            "SELECT {REF_COLUMNS} FROM {} \
             WHERE deleted_at IS NULL AND parent_ref_id IS NOT NULL AND updated_at < now() - $1::bigint * interval '1 microsecond' \
             ORDER BY updated_at, id",
            self.a.ref_table
        );
        self.scan_refs(&sql, &[micros(idle).into()]).await
    }

    async fn remove_ref_rows(
        &mut self,
        kind: &str,
        ref_id: &str,
        actor: &str,
    ) -> Result<i64, Error> {
        let k = self.a.kind(kind)?;
        let sql = format!(
            "WITH history_actor AS MATERIALIZED (SELECT set_config($1, $2, true) AS _history_actor) \
             DELETE FROM {} USING history_actor WHERE {} = $3::uuid",
            quote(&k.table),
            quote(&k.ref_column)
        );
        let what = format!("remove the {} rows of a ref", k.name);
        let args = [
            self.a.actor_setting.clone().into(),
            uuid_text(actor)?.into(),
            uuid_text(ref_id)?.into(),
        ];
        let n = self.execute(&what, &sql, &args).await?;
        self.clear_history_actor().await?;
        Ok(i64::try_from(n).unwrap_or(i64::MAX))
    }

    async fn sweep_lock(&mut self) -> Result<bool, Error> {
        let key = self.a.sweep_key.clone();
        let rows = self
            .query(
                "take the sweep lock",
                "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))",
                &[key.into()],
            )
            .await?;
        match rows.first() {
            Some(row) => Cells::new(row).boolean(),
            None => Ok(false),
        }
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

    /// The adapter takes the fixture's descriptor and refuses one that
    /// leaves out what it builds statements from: a graph table, a kind's
    /// root column, or a role column the kind's columns do not declare.
    #[test]
    fn new_refuses_a_descriptor_without_what_it_builds_from() {
        type Edit = fn(&mut Value);
        let cases: [(&str, Edit, &str); 9] = [
            ("the fixture's descriptor", |_| {}, ""),
            (
                "an empty refTable",
                |d| d["refTable"] = "".into(),
                "refTable is empty",
            ),
            (
                "an empty commitTable",
                |d| d["commitTable"] = "".into(),
                "commitTable is empty",
            ),
            (
                "an empty patchTable",
                |d| d["patchTable"] = "".into(),
                "patchTable is empty",
            ),
            (
                "an empty releaseTable",
                |d| d["releaseTable"] = "".into(),
                "releaseTable is empty",
            ),
            (
                "an empty snapshotTable",
                |d| d["snapshotTable"] = "".into(),
                "snapshotTable is empty",
            ),
            (
                "a kind without a root column",
                |d| {
                    d["kinds"][0]
                        .as_object_mut()
                        .expect("a kind")
                        .remove("root");
                },
                "has no root",
            ),
            (
                "a role column missing from the kind's columns",
                |d| {
                    d["kinds"][0]["columns"]
                        .as_object_mut()
                        .expect("columns")
                        .remove("_version");
                },
                "version column \"_version\" is not in its columns",
            ),
            (
                "version 1",
                |d| d["version"] = 1.into(),
                "this adapter reads version 2",
            ),
        ];
        for (name, edit, refuse) in cases {
            let mut descriptor = fixture();
            edit(&mut descriptor);
            let result = Adapter::new(&descriptor.to_string(), Options::default());
            match (refuse, result) {
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
}
