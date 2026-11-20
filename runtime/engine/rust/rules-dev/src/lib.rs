//! Development mode's store over SQLite (D117): the development layout of
//! `superschematic-rules-handler` in a SQLite file, or in a database in
//! memory that goes with its process, and a host that serves the handler's
//! requests over it in one process.
//!
//! The layout is fixed, so a publish writes rows and never a table: the
//! store creates its tables when it opens, and none after.
//!
//! | Table | What it holds |
//! |-------|---------------|
//! | `dev_schema_versions` | each type's published versions: the type (`schema`), the number, the schema document |
//! | `dev_schema_drafts` | each type's version defined and not published yet |
//! | `dev_instances` | the rows: the type (`schema`), the id, the version, the own fields as one JSON column (`data`) and the owned fields as another (`owned`) |
//! | `dev_sub_<behavior>` | one shared sub-table per behavior that declares sub-tables (`dev_sub_timers`), keyed by type, id, use, table and key, each row's values a JSON column |
//! | `dev_deadlines` | the armed deadlines: the row, the use, the name, the time armed and when due |
//! | `dev_events` | the event queue (D104), in the order written |
//! | `dev_changes` | the change queue (D103): each heard write, run after the commit |
//!
//! Each request runs in one transaction begun IMMEDIATE, so it holds the
//! database's one writer from its start: every lock a behavior asks for is
//! held from the request's start, and a request reads what the one before
//! it committed. A unit applies in a savepoint, whole or not at all.

use std::collections::BTreeMap;
use std::path::Path;
use std::time::{SystemTime, UNIX_EPOCH};

use rusqlite::{params, Connection, OptionalExtension, ToSql};
use serde_json::{Map, Value};
use superschematic_rules::{Caller, Code, Core, Refusal, RowChange, SubRow, SubRowChange};
use superschematic_rules_handler::document::storage_key;
use superschematic_rules_handler::{
    run_due, serve, Armed, Context, Declarations, Draft, DueDeadline, Handler, HeldSubRow,
    QueuedChange, QueuedEvent, RanDue, Request, Store, StoreAnswer, StoreRead, StoreWrite,
    StoredRow, Unit, Version,
};

/// The version of the layout this crate writes, kept in `dev_meta`.
pub const LAYOUT_VERSION: &str = "1";

/// The development layout in a SQLite database.
pub struct SqliteStore {
    conn: Connection,
    /// The behaviors whose shared sub-tables the store holds, by name: each
    /// one's table.
    sub_tables: BTreeMap<String, String>,
}

fn failed(error: rusqlite::Error) -> Refusal {
    if let rusqlite::Error::SqliteFailure(failure, _) = &error {
        if failure.code == rusqlite::ErrorCode::DatabaseBusy {
            return Refusal::new(
                Code::Busy,
                format!("the development store is busy with another writer: {error}"),
            );
        }
    }
    Refusal::internal(format!("the development store failed: {error}"))
}

fn text(value: &impl serde::Serialize) -> Result<String, Refusal> {
    serde_json::to_string(value)
        .map_err(|error| Refusal::internal(format!("a value is no JSON: {error}")))
}

fn parse<T: serde::de::DeserializeOwned>(text: &str) -> Result<T, Refusal> {
    serde_json::from_str(text)
        .map_err(|error| Refusal::internal(format!("the development store holds no JSON: {error}")))
}

/// Why a unit stopped: a fence that did not hold, or a refusal.
enum Stop {
    Moved,
    Refused(Refusal),
}

impl From<rusqlite::Error> for Stop {
    fn from(error: rusqlite::Error) -> Self {
        Stop::Refused(failed(error))
    }
}

impl From<Refusal> for Stop {
    fn from(refusal: Refusal) -> Self {
        Stop::Refused(refusal)
    }
}

impl SqliteStore {
    /// The store in a SQLite file, its tables created when they are not
    /// there, for the behaviors `declarations` declares.
    pub fn open(path: impl AsRef<Path>, declarations: &Declarations) -> rusqlite::Result<Self> {
        Self::over(Connection::open(path)?, declarations)
    }

    /// The store in a SQLite database in memory, which goes with it.
    pub fn memory(declarations: &Declarations) -> rusqlite::Result<Self> {
        Self::over(Connection::open_in_memory()?, declarations)
    }

    /// The store over a connection, its tables created when they are not
    /// there.
    pub fn over(conn: Connection, declarations: &Declarations) -> rusqlite::Result<Self> {
        conn.busy_timeout(std::time::Duration::from_secs(5))?;
        conn.execute_batch(
            "CREATE TABLE IF NOT EXISTS dev_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_schema_versions (
               schema TEXT NOT NULL, version INTEGER NOT NULL, document TEXT NOT NULL,
               PRIMARY KEY (schema, version));
             CREATE TABLE IF NOT EXISTS dev_schema_drafts (
               schema TEXT PRIMARY KEY, document TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_instances (
               schema TEXT NOT NULL, id TEXT NOT NULL, version INTEGER NOT NULL,
               data TEXT NOT NULL, owned TEXT NOT NULL,
               PRIMARY KEY (schema, id));
             CREATE TABLE IF NOT EXISTS dev_deadlines (
               schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL,
               at INTEGER NOT NULL, due INTEGER NOT NULL, payload TEXT NOT NULL,
               PRIMARY KEY (schema, id, use_id, name));
             CREATE INDEX IF NOT EXISTS dev_deadlines_due ON dev_deadlines (due);
             CREATE TABLE IF NOT EXISTS dev_events (
               seq INTEGER PRIMARY KEY AUTOINCREMENT, schema TEXT NOT NULL, id TEXT NOT NULL,
               use_id TEXT NOT NULL, kind TEXT NOT NULL, at INTEGER NOT NULL, payload TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS dev_changes (
               seq INTEGER PRIMARY KEY AUTOINCREMENT, schema TEXT NOT NULL, id TEXT NOT NULL,
               change TEXT NOT NULL);",
        )?;
        conn.execute(
            "INSERT INTO dev_meta (key, value) VALUES ('layout', ?1) ON CONFLICT (key) DO NOTHING",
            [LAYOUT_VERSION],
        )?;
        let mut sub_tables = BTreeMap::new();
        for declared in declarations
            .iter()
            .filter(|declared| !declared.tables.is_empty())
        {
            let table = format!("dev_sub_{}", storage_key(&declared.name));
            conn.execute_batch(&format!(
                "CREATE TABLE IF NOT EXISTS \"{table}\" (
                   schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL,
                   key TEXT NOT NULL, data TEXT NOT NULL,
                   PRIMARY KEY (schema, id, use_id, tbl, key));"
            ))?;
            sub_tables.insert(declared.name.clone(), table);
        }
        Ok(SqliteStore { conn, sub_tables })
    }

    /// The connection, for a test's or an operator's own SQL.
    pub fn connection(&self) -> &Connection {
        &self.conn
    }

    fn sub_table(&self, behavior: &str) -> Result<&str, Refusal> {
        self.sub_tables
            .get(behavior)
            .map(String::as_str)
            .ok_or_else(|| {
                Refusal::internal(format!(
                    "the development store keeps no sub-table of {behavior}, which declares none"
                ))
            })
    }

    fn row(&self, type_name: &str, id: &str) -> Result<Option<StoredRow>, Refusal> {
        self.conn
            .query_row(
                "SELECT version, data, owned FROM dev_instances WHERE schema = ?1 AND id = ?2",
                params![type_name, id],
                |row| {
                    Ok((
                        row.get::<_, i64>(0)?,
                        row.get::<_, String>(1)?,
                        row.get::<_, String>(2)?,
                    ))
                },
            )
            .optional()
            .map_err(failed)?
            .map(|(version, data, owned)| {
                Ok(StoredRow {
                    id: id.to_owned(),
                    version,
                    data: parse(&data)?,
                    owned: parse(&owned)?,
                })
            })
            .transpose()
    }

    fn rows(&self, type_name: &str) -> Result<Vec<StoredRow>, Refusal> {
        let mut statement = self
            .conn
            .prepare_cached(
                "SELECT id, version, data, owned FROM dev_instances WHERE schema = ?1 ORDER BY id",
            )
            .map_err(failed)?;
        let rows = statement
            .query_map([type_name], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, i64>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })
            .map_err(failed)?;
        let mut out = Vec::new();
        for row in rows {
            let (id, version, data, owned) = row.map_err(failed)?;
            out.push(StoredRow {
                id,
                version,
                data: parse(&data)?,
                owned: parse(&owned)?,
            });
        }
        Ok(out)
    }

    /// A row's rows of one use's sub-table, in key order.
    pub fn sub_rows(
        &self,
        type_name: &str,
        id: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<SubRow>, Refusal> {
        let mut out = Vec::new();
        for sub_table in self.sub_tables.values() {
            let mut statement = self
                .conn
                .prepare_cached(&format!(
                    "SELECT key, data FROM \"{sub_table}\" WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND tbl = ?4 ORDER BY key"
                ))
                .map_err(failed)?;
            let rows = statement
                .query_map(params![type_name, id, use_id, table], |row| {
                    Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
                })
                .map_err(failed)?;
            for row in rows {
                let (key, data) = row.map_err(failed)?;
                out.push(SubRow::new(key, parse(&data)?));
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(out)
    }

    fn table_sub_rows(
        &self,
        type_name: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<HeldSubRow>, Refusal> {
        let mut out = Vec::new();
        for sub_table in self.sub_tables.values() {
            let mut statement = self
                .conn
                .prepare_cached(&format!(
                    "SELECT id, key, data FROM \"{sub_table}\" WHERE schema = ?1 AND use_id = ?2 AND tbl = ?3 ORDER BY id, key"
                ))
                .map_err(failed)?;
            let rows = statement
                .query_map(params![type_name, use_id, table], |row| {
                    Ok((
                        row.get::<_, String>(0)?,
                        row.get::<_, String>(1)?,
                        row.get::<_, String>(2)?,
                    ))
                })
                .map_err(failed)?;
            for row in rows {
                let (id, key, data) = row.map_err(failed)?;
                out.push(HeldSubRow {
                    id,
                    row: SubRow::new(key, parse(&data)?),
                });
            }
        }
        out.sort_by(|a, b| (&a.id, &a.row.key).cmp(&(&b.id, &b.row.key)));
        Ok(out)
    }

    /// A row's armed deadlines: each its use, name and time, by use and
    /// name.
    pub fn armed(
        &self,
        type_name: &str,
        id: &str,
    ) -> Result<Vec<(String, String, i64, Value)>, Refusal> {
        let mut statement = self
            .conn
            .prepare_cached(
                "SELECT use_id, name, at, payload FROM dev_deadlines WHERE schema = ?1 AND id = ?2 ORDER BY use_id, name",
            )
            .map_err(failed)?;
        let rows = statement
            .query_map(params![type_name, id], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, i64>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })
            .map_err(failed)?;
        let mut out = Vec::new();
        for row in rows {
            let (use_id, name, at, payload) = row.map_err(failed)?;
            out.push((use_id, name, at, parse(&payload)?));
        }
        Ok(out)
    }

    // changed runs a write and holds it to change one row.
    fn one(&self, sql: &str, values: &[&dyn ToSql]) -> Result<(), Stop> {
        if self.conn.execute(sql, values)? == 1 {
            Ok(())
        } else {
            Err(Stop::Moved)
        }
    }

    fn drop_row(&self, type_name: &str, id: &str) -> Result<(), Stop> {
        for sub_table in self.sub_tables.values() {
            self.conn.execute(
                &format!("DELETE FROM \"{sub_table}\" WHERE schema = ?1 AND id = ?2"),
                params![type_name, id],
            )?;
        }
        self.conn.execute(
            "DELETE FROM dev_deadlines WHERE schema = ?1 AND id = ?2",
            params![type_name, id],
        )?;
        Ok(())
    }

    fn apply_one(&self, write: &StoreWrite) -> Result<(), Stop> {
        match write {
            StoreWrite::PutDraft {
                type_name,
                document,
            } => {
                self.conn.execute(
                    "INSERT INTO dev_schema_drafts (schema, document) VALUES (?1, ?2)
                     ON CONFLICT (schema) DO UPDATE SET document = excluded.document",
                    params![type_name, text(document)?],
                )?;
            }
            StoreWrite::PutVersion {
                type_name,
                version,
                document,
            } => {
                let live: i64 = self.conn.query_row(
                    "SELECT COALESCE(MAX(version), 0) FROM dev_schema_versions WHERE schema = ?1",
                    [type_name],
                    |row| row.get(0),
                )?;
                if live != version - 1 {
                    return Err(Stop::Moved);
                }
                self.conn.execute(
                    "INSERT INTO dev_schema_versions (schema, version, document) VALUES (?1, ?2, ?3)",
                    params![type_name, version, text(document)?],
                )?;
                self.conn.execute(
                    "DELETE FROM dev_schema_drafts WHERE schema = ?1",
                    [type_name],
                )?;
            }
            StoreWrite::Insert {
                type_name,
                id,
                data,
                owned,
            } => {
                let held: Option<i64> = self
                    .conn
                    .query_row(
                        "SELECT 1 FROM dev_instances WHERE schema = ?1 AND id = ?2",
                        params![type_name, id],
                        |row| row.get(0),
                    )
                    .optional()?;
                if held.is_some() {
                    return Err(Stop::Refused(Refusal::new(
                        Code::Conflict,
                        format!("{type_name} {id} already exists"),
                    )));
                }
                self.conn.execute(
                    "INSERT INTO dev_instances (schema, id, version, data, owned) VALUES (?1, ?2, 1, ?3, ?4)",
                    params![type_name, id, text(data)?, text(owned)?],
                )?;
            }
            StoreWrite::Update {
                type_name,
                id,
                expected,
                data,
                owned,
            } => self.one(
                "UPDATE dev_instances SET data = ?1, owned = ?2, version = version + 1
                 WHERE schema = ?3 AND id = ?4 AND version = ?5",
                params![text(data)?, text(owned)?, type_name, id, expected],
            )?,
            StoreWrite::Rewrite {
                type_name,
                id,
                data,
                owned,
            } => self.one(
                "UPDATE dev_instances SET data = ?1, owned = ?2 WHERE schema = ?3 AND id = ?4",
                params![text(data)?, text(owned)?, type_name, id],
            )?,
            StoreWrite::Delete {
                type_name,
                id,
                expected,
            } => {
                match expected {
                    Some(expected) => self.one(
                        "DELETE FROM dev_instances WHERE schema = ?1 AND id = ?2 AND version = ?3",
                        params![type_name, id, expected],
                    )?,
                    None => self.one(
                        "DELETE FROM dev_instances WHERE schema = ?1 AND id = ?2",
                        params![type_name, id],
                    )?,
                }
                self.drop_row(type_name, id)?;
            }
            StoreWrite::Gone { type_name, id } => {
                if self.row(type_name, id)?.is_none() {
                    self.drop_row(type_name, id)?;
                }
            }
            StoreWrite::SubRow {
                type_name,
                id,
                behavior,
                use_id,
                table,
                key,
                change,
            } => {
                let sub_table = self.sub_table(behavior)?;
                match change {
                    SubRowChange::Insert { values } => {
                        let values: Map<String, Value> = values
                            .iter()
                            .filter(|(_, value)| !value.is_null())
                            .map(|(name, value)| (name.clone(), value.clone()))
                            .collect();
                        self.one(
                            &format!(
                                "INSERT INTO \"{sub_table}\" (schema, id, use_id, tbl, key, data)
                                 VALUES (?1, ?2, ?3, ?4, ?5, ?6) ON CONFLICT DO NOTHING"
                            ),
                            params![type_name, id, use_id, table, key, text(&values)?],
                        )?;
                    }
                    SubRowChange::Update { values } => {
                        let held: Option<String> = self
                            .conn
                            .query_row(
                                &format!(
                                    "SELECT data FROM \"{sub_table}\" WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND tbl = ?4 AND key = ?5"
                                ),
                                params![type_name, id, use_id, table, key],
                                |row| row.get(0),
                            )
                            .optional()?;
                        let Some(held) = held else {
                            return Err(Stop::Moved);
                        };
                        let mut data: Map<String, Value> = parse(&held)?;
                        for (name, value) in values {
                            if value.is_null() {
                                data.shift_remove(name);
                            } else {
                                data.insert(name.clone(), value.clone());
                            }
                        }
                        self.one(
                            &format!(
                                "UPDATE \"{sub_table}\" SET data = ?1 WHERE schema = ?2 AND id = ?3 AND use_id = ?4 AND tbl = ?5 AND key = ?6"
                            ),
                            params![text(&data)?, type_name, id, use_id, table, key],
                        )?;
                    }
                    SubRowChange::Delete => self.one(
                        &format!(
                            "DELETE FROM \"{sub_table}\" WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND tbl = ?4 AND key = ?5"
                        ),
                        params![type_name, id, use_id, table, key],
                    )?,
                    other => {
                        return Err(Stop::Refused(Refusal::internal(format!(
                            "the development store takes no sub-table change of this kind: {other:?}"
                        ))))
                    }
                }
            }
            StoreWrite::DropUse {
                type_name,
                behavior,
                use_id,
            } => {
                if let Ok(sub_table) = self.sub_table(behavior) {
                    self.conn.execute(
                        &format!("DELETE FROM \"{sub_table}\" WHERE schema = ?1 AND use_id = ?2"),
                        params![type_name, use_id],
                    )?;
                }
            }
            StoreWrite::Arm {
                type_name,
                id,
                use_id,
                name,
                at,
                payload,
            } => {
                self.conn.execute(
                    "INSERT INTO dev_deadlines (schema, id, use_id, name, at, due, payload)
                     VALUES (?1, ?2, ?3, ?4, ?5, ?5, ?6)
                     ON CONFLICT (schema, id, use_id, name)
                     DO UPDATE SET at = excluded.at, due = excluded.due, payload = excluded.payload",
                    params![type_name, id, use_id, name, at, text(payload)?],
                )?;
            }
            StoreWrite::Clear {
                type_name,
                id,
                use_id,
                name,
            } => {
                self.conn.execute(
                    "DELETE FROM dev_deadlines WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND name = ?4",
                    params![type_name, id, use_id, name],
                )?;
            }
            StoreWrite::ClearUse { type_name, use_id } => {
                self.conn.execute(
                    "DELETE FROM dev_deadlines WHERE schema = ?1 AND use_id = ?2",
                    params![type_name, use_id],
                )?;
            }
            StoreWrite::Spend {
                type_name,
                id,
                use_id,
                name,
                at,
            } => self.one(
                "DELETE FROM dev_deadlines WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND name = ?4 AND at = ?5",
                params![type_name, id, use_id, name, at],
            )?,
            StoreWrite::Release {
                type_name,
                id,
                use_id,
                name,
                due,
            } => self.one(
                "UPDATE dev_deadlines SET due = ?1 WHERE schema = ?2 AND id = ?3 AND use_id = ?4 AND name = ?5",
                params![due, type_name, id, use_id, name],
            )?,
            StoreWrite::Event {
                type_name,
                id,
                use_id,
                kind,
                at,
                payload,
            } => {
                self.conn.execute(
                    "INSERT INTO dev_events (schema, id, use_id, kind, at, payload) VALUES (?1, ?2, ?3, ?4, ?5, ?6)",
                    params![type_name, id, use_id, kind, at, text(payload)?],
                )?;
            }
            StoreWrite::Change {
                type_name,
                id,
                change,
            } => {
                self.conn.execute(
                    "INSERT INTO dev_changes (schema, id, change) VALUES (?1, ?2, ?3)",
                    params![type_name, id, text(change)?],
                )?;
            }
            StoreWrite::Dequeue { seq } => {
                self.one("DELETE FROM dev_changes WHERE seq = ?1", params![seq])?;
            }
        }
        Ok(())
    }
}

impl Store for SqliteStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        let mut statement = self
            .conn
            .prepare_cached(
                "SELECT schema, id, use_id, name, at, payload FROM dev_deadlines WHERE due <= ?1 ORDER BY due, schema, id, use_id, name",
            )
            .map_err(failed)?;
        let rows = statement
            .query_map([now], |row| {
                Ok((
                    row.get::<_, String>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                    row.get::<_, i64>(4)?,
                    row.get::<_, String>(5)?,
                ))
            })
            .map_err(failed)?;
        let mut out = Vec::new();
        for row in rows {
            let (type_name, id, use_id, name, at, payload) = row.map_err(failed)?;
            out.push(DueDeadline {
                type_name,
                id,
                use_id,
                name,
                at,
                payload: parse(&payload)?,
            });
        }
        Ok(out)
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        let mut statement = self
            .conn
            .prepare_cached("SELECT seq, schema, id, change FROM dev_changes ORDER BY seq")
            .map_err(failed)?;
        let rows = statement
            .query_map([], |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                ))
            })
            .map_err(failed)?;
        let mut out = Vec::new();
        for row in rows {
            let (seq, type_name, id, change) = row.map_err(failed)?;
            out.push(QueuedChange {
                seq,
                type_name,
                id,
                change: parse::<RowChange>(&change)?,
            });
        }
        Ok(out)
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        let mut statement = self
            .conn
            .prepare_cached(
                "SELECT seq, schema, id, use_id, kind, at, payload FROM dev_events WHERE seq > ?1 ORDER BY seq",
            )
            .map_err(failed)?;
        let rows = statement
            .query_map([after], |row| {
                Ok((
                    row.get::<_, i64>(0)?,
                    row.get::<_, String>(1)?,
                    row.get::<_, String>(2)?,
                    row.get::<_, String>(3)?,
                    row.get::<_, String>(4)?,
                    row.get::<_, i64>(5)?,
                    row.get::<_, String>(6)?,
                ))
            })
            .map_err(failed)?;
        let mut out = Vec::new();
        for row in rows {
            let (seq, type_name, id, use_id, kind, at, payload) = row.map_err(failed)?;
            out.push(QueuedEvent {
                seq,
                type_name,
                id,
                use_id,
                kind,
                at,
                payload: parse(&payload)?,
            });
        }
        Ok(out)
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.conn.execute_batch("BEGIN IMMEDIATE").map_err(failed)
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.conn.execute_batch("COMMIT").map_err(failed)
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if self.conn.is_autocommit() {
            return Ok(());
        }
        self.conn.execute_batch("ROLLBACK").map_err(failed)
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        Ok(match read {
            StoreRead::Live => {
                let mut statement = self
                    .conn
                    .prepare_cached(
                        "SELECT schema, version, document FROM dev_schema_versions AS v
                         WHERE version = (SELECT MAX(version) FROM dev_schema_versions WHERE schema = v.schema)
                         ORDER BY schema",
                    )
                    .map_err(failed)?;
                let rows = statement
                    .query_map([], |row| {
                        Ok((
                            row.get::<_, String>(0)?,
                            row.get::<_, i64>(1)?,
                            row.get::<_, String>(2)?,
                        ))
                    })
                    .map_err(failed)?;
                let mut versions = Vec::new();
                for row in rows {
                    let (type_name, version, document) = row.map_err(failed)?;
                    versions.push(Version {
                        type_name,
                        version,
                        document: parse(&document)?,
                    });
                }
                StoreAnswer::Live(versions)
            }
            StoreRead::Draft { type_name } => {
                let document: Option<String> = self
                    .conn
                    .query_row(
                        "SELECT document FROM dev_schema_drafts WHERE schema = ?1",
                        [type_name],
                        |row| row.get(0),
                    )
                    .optional()
                    .map_err(failed)?;
                StoreAnswer::Draft(
                    document
                        .map(|document| {
                            Ok::<_, Refusal>(Draft {
                                type_name: type_name.clone(),
                                document: parse(&document)?,
                            })
                        })
                        .transpose()?,
                )
            }
            StoreRead::Row { type_name, id } => StoreAnswer::Row(self.row(type_name, id)?),
            StoreRead::Rows { type_name } => StoreAnswer::Rows(self.rows(type_name)?),
            StoreRead::SubRows {
                type_name,
                id,
                use_id,
                table,
            } => StoreAnswer::SubRows(self.sub_rows(type_name, id, use_id, table)?),
            StoreRead::TableSubRows {
                type_name,
                use_id,
                table,
            } => StoreAnswer::TableSubRows(self.table_sub_rows(type_name, use_id, table)?),
            StoreRead::Armed {
                type_name,
                id,
                use_id,
                name,
            } => {
                let armed: Option<(i64, String)> = self
                    .conn
                    .query_row(
                        "SELECT at, payload FROM dev_deadlines WHERE schema = ?1 AND id = ?2 AND use_id = ?3 AND name = ?4",
                        params![type_name, id, use_id, name],
                        |row| Ok((row.get(0)?, row.get(1)?)),
                    )
                    .optional()
                    .map_err(failed)?;
                StoreAnswer::Armed(
                    armed
                        .map(|(at, payload)| {
                            Ok::<_, Refusal>(Armed {
                                at,
                                payload: parse(&payload)?,
                            })
                        })
                        .transpose()?,
                )
            }
        })
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        self.conn
            .execute_batch("SAVEPOINT dev_unit")
            .map_err(failed)?;
        for write in &unit.writes {
            match self.apply_one(write) {
                Ok(()) => {}
                Err(stop) => {
                    self.conn
                        .execute_batch("ROLLBACK TO dev_unit; RELEASE dev_unit")
                        .map_err(failed)?;
                    return match stop {
                        Stop::Moved => Ok(false),
                        Stop::Refused(refusal) => Err(refusal),
                    };
                }
            }
        }
        self.conn
            .execute_batch("RELEASE dev_unit")
            .map_err(failed)?;
        Ok(true)
    }
}

/// When the host runs its queues: after each request, as a worker that
/// claims a message the moment it is written would, or only when asked.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Default)]
pub enum Drain {
    #[default]
    AfterEachRequest,
    WhenAsked,
}

/// The development host: the handler over a SQLite store in one process,
/// with the core, the behaviors' declarations, a clock, and the caller the
/// runner runs the queues as.
pub struct Dev {
    core: Core,
    declarations: Declarations,
    store: SqliteStore,
    clock: Box<dyn Fn() -> i64 + Send>,
    runner: Caller,
    drain: Drain,
}

/// The system's time in epoch milliseconds.
fn system_millis() -> i64 {
    match SystemTime::now().duration_since(UNIX_EPOCH) {
        Ok(since) => i64::try_from(since.as_millis()).unwrap_or(i64::MAX),
        Err(before) => -i64::try_from(before.duration().as_millis()).unwrap_or(i64::MAX),
    }
}

impl Dev {
    /// The host over a SQLite file, running the behaviors ported onto the
    /// core.
    pub fn open(path: impl AsRef<Path>) -> rusqlite::Result<Self> {
        let declarations = Declarations::ported();
        let store = SqliteStore::open(path, &declarations)?;
        Ok(Self::over(
            superschematic_rules_behaviors::core(),
            declarations,
            store,
        ))
    }

    /// The host over a SQLite database in memory, which goes with it.
    pub fn memory() -> rusqlite::Result<Self> {
        let declarations = Declarations::ported();
        let store = SqliteStore::memory(&declarations)?;
        Ok(Self::over(
            superschematic_rules_behaviors::core(),
            declarations,
            store,
        ))
    }

    /// The host over a core, the declarations of its behaviors and a store
    /// opened with them.
    pub fn over(core: Core, declarations: Declarations, store: SqliteStore) -> Self {
        Dev {
            core,
            declarations,
            store,
            clock: Box::new(system_millis),
            runner: Caller::new("development-runner", Vec::<String>::new()),
            drain: Drain::default(),
        }
    }

    /// The host with a clock of its own, in epoch milliseconds.
    pub fn with_clock(mut self, clock: impl Fn() -> i64 + Send + 'static) -> Self {
        self.clock = Box::new(clock);
        self
    }

    /// The host with the caller its runner runs the queues as.
    pub fn with_runner(mut self, runner: Caller) -> Self {
        self.runner = runner;
        self
    }

    /// The host with when it runs its queues.
    pub fn with_drain(mut self, drain: Drain) -> Self {
        self.drain = drain;
        self
    }

    pub fn store(&mut self) -> &mut SqliteStore {
        &mut self.store
    }

    pub fn core(&self) -> &Core {
        &self.core
    }

    pub fn declarations(&self) -> &Declarations {
        &self.declarations
    }

    /// Serves one request as a caller, then, unless the host drains when
    /// asked, runs the queues.
    pub fn request(&mut self, caller: &Caller, request: &Request) -> Result<Value, Refusal> {
        let context = Context {
            caller: caller.clone(),
            now: (self.clock)(),
            request: uuid::Uuid::new_v4().to_string(),
            id: uuid::Uuid::new_v4().as_u128(),
        };
        let handler = Handler::new(&self.core, &self.declarations);
        let response = serve(&handler, &mut self.store, request, &context);
        if self.drain == Drain::AfterEachRequest {
            self.run_due()?;
        }
        response
    }

    /// Runs the change queue and the deadlines due by the host's clock, as
    /// its runner.
    pub fn run_due(&mut self) -> Result<Vec<RanDue>, Refusal> {
        let handler = Handler::new(&self.core, &self.declarations);
        let mut fresh = || {
            (
                uuid::Uuid::new_v4().to_string(),
                uuid::Uuid::new_v4().as_u128(),
            )
        };
        run_due(
            &handler,
            &mut self.store,
            (self.clock)(),
            &self.runner,
            &mut fresh,
        )
    }
}
