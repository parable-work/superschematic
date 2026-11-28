//! The development layout over either database (D117): its tables, the
//! statements that read and write them, and how a store's reads, writes
//! and units run over them, written once for SQLite's [`SqliteStore`] and
//! Postgres's `PgStore`. A store is a connection ([`Db`]) and its
//! [`Layout`]: the tables' names in its database, and where the dialects
//! part, a key's order by code point and the DDL's integer types.
//!
//! A statement is written with Postgres's `$n` placeholders; SQLite reads
//! them as its own `?n`, numbered alike. Every value a statement takes is
//! bound ([`Arg`]), and every column a store reads is TEXT or a whole
//! number ([`Cells`]).
//!
//! [`SqliteStore`]: crate::SqliteStore

use std::collections::BTreeMap;

use serde_json::{Map, Value};
use superschematic_rules::{
    Code, Refusal, RowChange, SharedRow, SharedRowChange, SubRow, SubRowChange,
};
use superschematic_rules_handler::document::storage_key;
use superschematic_rules_handler::{
    Armed, Declarations, Draft, DueDeadline, HeldSubRow, QueuedChange, QueuedEvent, StoreAnswer,
    StoreRead, StoreWrite, StoredRow, Unit, Version,
};

/// The layout's fixed tables, but `dev_meta`, which a fresh store empties.
#[cfg(feature = "postgres")]
const TABLES: [&str; 7] = [
    "dev_schema_versions",
    "dev_schema_drafts",
    "dev_instances",
    "dev_deadlines",
    "dev_events",
    "dev_changes",
    "dev_shared",
];

/// A value a statement binds.
pub(crate) enum Arg<'a> {
    Text(&'a str),
    Owned(String),
    Int(i64),
}

impl<'a> From<&'a str> for Arg<'a> {
    fn from(text: &'a str) -> Self {
        Arg::Text(text)
    }
}

impl<'a> From<&'a String> for Arg<'a> {
    fn from(text: &'a String) -> Self {
        Arg::Text(text)
    }
}

impl From<String> for Arg<'_> {
    fn from(text: String) -> Self {
        Arg::Owned(text)
    }
}

impl From<i64> for Arg<'_> {
    fn from(n: i64) -> Self {
        Arg::Int(n)
    }
}

impl From<&i64> for Arg<'_> {
    fn from(n: &i64) -> Self {
        Arg::Int(*n)
    }
}

/// The values a statement binds, in its placeholders' order.
macro_rules! args {
    ($($arg:expr),* $(,)?) => {
        &[$(Arg::from($arg)),*]
    };
}

/// A column's value as a store reads it.
pub(crate) enum Cell {
    Null,
    Int(i64),
    Text(String),
}

/// A row a query read, its columns in the statement's order.
pub(crate) struct Cells(pub(crate) Vec<Cell>);

impl Cells {
    fn cell(&self, at: usize) -> Result<&Cell, Refusal> {
        self.0
            .get(at)
            .ok_or_else(|| Refusal::internal(format!("the development store read no column {at}")))
    }

    /// A TEXT column's value.
    fn text(&self, at: usize) -> Result<String, Refusal> {
        match self.cell(at)? {
            Cell::Text(text) => Ok(text.clone()),
            _ => Err(Refusal::internal(format!(
                "the development store holds no text in column {at}"
            ))),
        }
    }

    /// A whole number column's value.
    fn int(&self, at: usize) -> Result<i64, Refusal> {
        match self.cell(at)? {
            Cell::Int(n) => Ok(*n),
            _ => Err(Refusal::internal(format!(
                "the development store holds no whole number in column {at}"
            ))),
        }
    }

    /// A TEXT column's JSON, as its type.
    fn json<T: serde::de::DeserializeOwned>(&self, at: usize) -> Result<T, Refusal> {
        match self.cell(at)? {
            Cell::Text(text) => parse(text),
            _ => Err(Refusal::internal(format!(
                "the development store holds no JSON in column {at}"
            ))),
        }
    }
}

/// A connection a store runs its statements over, each failure the store's
/// refusal.
pub(crate) trait Db {
    /// Runs a statement and gives the rows it changed.
    fn execute(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<u64, Refusal>;
    /// Runs a query and gives its rows.
    fn query(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<Vec<Cells>, Refusal>;
    /// Runs statements that bind nothing.
    fn batch(&mut self, sql: &str) -> Result<(), Refusal>;

    /// A query's first row, if any.
    fn query_opt(&mut self, sql: &str, args: &[Arg<'_>]) -> Result<Option<Cells>, Refusal> {
        Ok(self.query(sql, args)?.into_iter().next())
    }
}

/// A value's JSON text.
pub(crate) fn text(value: &impl serde::Serialize) -> Result<String, Refusal> {
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

impl From<Refusal> for Stop {
    fn from(refusal: Refusal) -> Self {
        Stop::Refused(refusal)
    }
}

/// A sub-table's or a shared table's row with a change's values merged
/// into it: a null removes a value.
fn merged(mut data: Map<String, Value>, values: &Map<String, Value>) -> Map<String, Value> {
    for (name, value) in values {
        if value.is_null() {
            data.shift_remove(name);
        } else {
            data.insert(name.clone(), value.clone());
        }
    }
    data
}

/// The development layout's tables in one database.
pub(crate) struct Layout {
    /// A table's name as a statement names it: `dev_instances` on SQLite,
    /// `"<schema>".dev_instances` on Postgres.
    prefix: String,
    /// What follows a key in an ORDER BY to order it by code point:
    /// nothing on SQLite, whose BINARY collation does, and `COLLATE "C"` on
    /// Postgres.
    collate: &'static str,
    /// The DDL's whole number type and its sequence's column.
    int: &'static str,
    seq: &'static str,
    /// The behaviors whose shared sub-tables the store holds, by name: each
    /// one's table, quoted as a statement names it.
    sub_tables: BTreeMap<String, String>,
}

impl Layout {
    /// The layout in a SQLite database, for the behaviors `declarations`
    /// declares.
    pub(crate) fn sqlite(declarations: &Declarations) -> Self {
        Self::new(
            String::new(),
            "",
            "INTEGER",
            "INTEGER PRIMARY KEY AUTOINCREMENT",
            declarations,
        )
    }

    /// The layout in a schema of a Postgres database, its name an
    /// identifier.
    #[cfg(feature = "postgres")]
    pub(crate) fn postgres(schema: &str, declarations: &Declarations) -> Self {
        Self::new(
            format!("\"{schema}\"."),
            " COLLATE \"C\"",
            "BIGINT",
            "BIGSERIAL PRIMARY KEY",
            declarations,
        )
    }

    fn new(
        prefix: String,
        collate: &'static str,
        int: &'static str,
        seq: &'static str,
        declarations: &Declarations,
    ) -> Self {
        let sub_tables = declarations
            .iter()
            .filter(|declared| declared.has_row_tables())
            .map(|declared| {
                let table = format!("{prefix}\"dev_sub_{}\"", storage_key(&declared.name));
                (declared.name.clone(), table)
            })
            .collect();
        Layout {
            prefix,
            collate,
            int,
            seq,
            sub_tables,
        }
    }

    fn t(&self, table: &str) -> String {
        format!("{}{table}", self.prefix)
    }

    /// The DDL of the fixed tables, each created when it is not there.
    pub(crate) fn create_tables(&self) -> String {
        let (int, seq) = (self.int, self.seq);
        let t = |table: &str| self.t(table);
        format!(
            "CREATE TABLE IF NOT EXISTS {meta} (key TEXT PRIMARY KEY, value TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS {versions} (
               schema TEXT NOT NULL, version {int} NOT NULL, document TEXT NOT NULL,
               PRIMARY KEY (schema, version));
             CREATE TABLE IF NOT EXISTS {drafts} (
               schema TEXT PRIMARY KEY, document TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS {instances} (
               schema TEXT NOT NULL, id TEXT NOT NULL, version {int} NOT NULL,
               data TEXT NOT NULL, owned TEXT NOT NULL,
               PRIMARY KEY (schema, id));
             CREATE TABLE IF NOT EXISTS {deadlines} (
               schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL,
               at {int} NOT NULL, due {int} NOT NULL, payload TEXT NOT NULL,
               PRIMARY KEY (schema, id, use_id, name));
             CREATE INDEX IF NOT EXISTS dev_deadlines_due ON {deadlines} (due);
             CREATE TABLE IF NOT EXISTS {events} (
               seq {seq}, schema TEXT NOT NULL, id TEXT NOT NULL,
               use_id TEXT NOT NULL, kind TEXT NOT NULL, at {int} NOT NULL, payload TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS {changes} (
               seq {seq}, schema TEXT NOT NULL, id TEXT NOT NULL,
               change TEXT NOT NULL);
             CREATE TABLE IF NOT EXISTS {shared} (
               schema TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL, key TEXT NOT NULL,
               data TEXT NOT NULL, version {int} NOT NULL,
               PRIMARY KEY (schema, use_id, tbl, key));",
            meta = t("dev_meta"),
            versions = t("dev_schema_versions"),
            drafts = t("dev_schema_drafts"),
            instances = t("dev_instances"),
            deadlines = t("dev_deadlines"),
            events = t("dev_events"),
            changes = t("dev_changes"),
            shared = t("dev_shared"),
        )
    }

    /// The statement that records the layout's version, `$1`, in a store
    /// that holds none.
    pub(crate) fn record_version(&self) -> String {
        format!(
            "INSERT INTO {} (key, value) VALUES ('layout', $1) ON CONFLICT (key) DO NOTHING",
            self.t("dev_meta")
        )
    }

    /// The DDL of each behavior's shared sub-table.
    pub(crate) fn create_sub_tables(&self) -> impl Iterator<Item = String> + '_ {
        self.sub_tables.values().map(|table| {
            format!(
                "CREATE TABLE IF NOT EXISTS {table} (
                   schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL,
                   key TEXT NOT NULL, data TEXT NOT NULL,
                   PRIMARY KEY (schema, id, use_id, tbl, key));"
            )
        })
    }

    /// Every table but `dev_meta`, as a statement names it.
    #[cfg(feature = "postgres")]
    pub(crate) fn tables(&self) -> Vec<String> {
        let mut tables: Vec<String> = TABLES.iter().map(|table| self.t(table)).collect();
        tables.extend(self.sub_tables.values().cloned());
        tables
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

    fn row(
        &self,
        db: &mut impl Db,
        type_name: &str,
        id: &str,
    ) -> Result<Option<StoredRow>, Refusal> {
        let sql = format!(
            "SELECT version, data, owned FROM {} WHERE schema = $1 AND id = $2",
            self.t("dev_instances")
        );
        db.query_opt(&sql, args![type_name, id])?
            .map(|row| {
                Ok(StoredRow {
                    id: id.to_owned(),
                    version: row.int(0)?,
                    data: row.json(1)?,
                    owned: row.json(2)?,
                })
            })
            .transpose()
    }

    fn rows(&self, db: &mut impl Db, type_name: &str) -> Result<Vec<StoredRow>, Refusal> {
        let sql = format!(
            "SELECT id, version, data, owned FROM {} WHERE schema = $1 ORDER BY id{}",
            self.t("dev_instances"),
            self.collate
        );
        db.query(&sql, args![type_name])?
            .iter()
            .map(|row| {
                Ok(StoredRow {
                    id: row.text(0)?,
                    version: row.int(1)?,
                    data: row.json(2)?,
                    owned: row.json(3)?,
                })
            })
            .collect()
    }

    /// A row's rows of one use's sub-table, in key order.
    pub(crate) fn sub_rows(
        &self,
        db: &mut impl Db,
        type_name: &str,
        id: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<SubRow>, Refusal> {
        let mut out = Vec::new();
        for sub_table in self.sub_tables.values() {
            let sql = format!(
                "SELECT key, data FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4"
            );
            for row in db.query(&sql, args![type_name, id, use_id, table])? {
                out.push(SubRow::new(row.text(0)?, row.json(1)?));
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(out)
    }

    fn table_sub_rows(
        &self,
        db: &mut impl Db,
        type_name: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<HeldSubRow>, Refusal> {
        let mut out = Vec::new();
        for sub_table in self.sub_tables.values() {
            let sql = format!(
                "SELECT id, key, data FROM {sub_table} WHERE schema = $1 AND use_id = $2 AND tbl = $3"
            );
            for row in db.query(&sql, args![type_name, use_id, table])? {
                out.push(HeldSubRow {
                    id: row.text(0)?,
                    row: SubRow::new(row.text(1)?, row.json(2)?),
                });
            }
        }
        out.sort_by(|a, b| (&a.id, &a.row.key).cmp(&(&b.id, &b.row.key)));
        Ok(out)
    }

    /// The row of a key of a use's shared table, with its version.
    pub(crate) fn shared_row(
        &self,
        db: &mut impl Db,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Result<Option<SharedRow>, Refusal> {
        let sql = format!(
            "SELECT data, version FROM {} WHERE schema = $1 AND use_id = $2 AND tbl = $3 AND key = $4",
            self.t("dev_shared")
        );
        db.query_opt(&sql, args![type_name, use_id, table, key])?
            .map(|row| Ok(SharedRow::new(key, row.json(0)?, row.int(1)?)))
            .transpose()
    }

    /// A row's armed deadlines: each its use, name, time and payload, by
    /// use and name.
    pub(crate) fn armed(
        &self,
        db: &mut impl Db,
        type_name: &str,
        id: &str,
    ) -> Result<Vec<(String, String, i64, Value)>, Refusal> {
        let sql = format!(
            "SELECT use_id, name, at, payload FROM {} WHERE schema = $1 AND id = $2
             ORDER BY use_id{c}, name{c}",
            self.t("dev_deadlines"),
            c = self.collate
        );
        db.query(&sql, args![type_name, id])?
            .iter()
            .map(|row| Ok((row.text(0)?, row.text(1)?, row.int(2)?, row.json(3)?)))
            .collect()
    }

    // one runs a write and holds it to change one row.
    fn one(&self, db: &mut impl Db, sql: &str, args: &[Arg<'_>]) -> Result<(), Stop> {
        if db.execute(sql, args)? == 1 {
            Ok(())
        } else {
            Err(Stop::Moved)
        }
    }

    fn drop_row(&self, db: &mut impl Db, type_name: &str, id: &str) -> Result<(), Stop> {
        for sub_table in self.sub_tables.values() {
            db.execute(
                &format!("DELETE FROM {sub_table} WHERE schema = $1 AND id = $2"),
                args![type_name, id],
            )?;
        }
        let sql = format!(
            "DELETE FROM {} WHERE schema = $1 AND id = $2",
            self.t("dev_deadlines")
        );
        db.execute(&sql, args![type_name, id])?;
        Ok(())
    }

    #[allow(clippy::cognitive_complexity)]
    fn apply_one(&self, db: &mut impl Db, write: &StoreWrite) -> Result<(), Stop> {
        let instances = self.t("dev_instances");
        let deadlines = self.t("dev_deadlines");
        let shared = self.t("dev_shared");
        match write {
            StoreWrite::PutDraft {
                type_name,
                document,
            } => {
                let sql = format!(
                    "INSERT INTO {} (schema, document) VALUES ($1, $2)
                     ON CONFLICT (schema) DO UPDATE SET document = excluded.document",
                    self.t("dev_schema_drafts")
                );
                db.execute(&sql, args![type_name, text(document)?])?;
            }
            StoreWrite::PutVersion {
                type_name,
                version,
                document,
            } => {
                let versions = self.t("dev_schema_versions");
                let live = db
                    .query_opt(
                        &format!(
                            "SELECT COALESCE(MAX(version), 0) FROM {versions} WHERE schema = $1"
                        ),
                        args![type_name],
                    )?
                    .map_or(Ok(0), |row| row.int(0))?;
                if live != version - 1 {
                    return Err(Stop::Moved);
                }
                db.execute(
                    &format!(
                        "INSERT INTO {versions} (schema, version, document) VALUES ($1, $2, $3)"
                    ),
                    args![type_name, version, text(document)?],
                )?;
                let drafts = self.t("dev_schema_drafts");
                db.execute(
                    &format!("DELETE FROM {drafts} WHERE schema = $1"),
                    args![type_name],
                )?;
            }
            StoreWrite::Insert {
                type_name,
                id,
                data,
                owned,
            } => {
                let held = db.query_opt(
                    &format!("SELECT 1 FROM {instances} WHERE schema = $1 AND id = $2"),
                    args![type_name, id],
                )?;
                if held.is_some() {
                    return Err(Stop::Refused(Refusal::new(
                        Code::Conflict,
                        format!("{type_name} {id} already exists"),
                    )));
                }
                db.execute(
                    &format!(
                        "INSERT INTO {instances} (schema, id, version, data, owned) VALUES ($1, $2, 1, $3, $4)"
                    ),
                    args![type_name, id, text(data)?, text(owned)?],
                )?;
            }
            StoreWrite::Update {
                type_name,
                id,
                expected,
                data,
                owned,
            } => self.one(
                db,
                &format!(
                    "UPDATE {instances} SET data = $1, owned = $2, version = version + 1
                     WHERE schema = $3 AND id = $4 AND version = $5"
                ),
                args![text(data)?, text(owned)?, type_name, id, expected],
            )?,
            StoreWrite::Rewrite {
                type_name,
                id,
                data,
                owned,
            } => self.one(
                db,
                &format!(
                    "UPDATE {instances} SET data = $1, owned = $2 WHERE schema = $3 AND id = $4"
                ),
                args![text(data)?, text(owned)?, type_name, id],
            )?,
            StoreWrite::Delete {
                type_name,
                id,
                expected,
            } => {
                match expected {
                    Some(expected) => self.one(
                        db,
                        &format!(
                            "DELETE FROM {instances} WHERE schema = $1 AND id = $2 AND version = $3"
                        ),
                        args![type_name, id, expected],
                    )?,
                    None => self.one(
                        db,
                        &format!("DELETE FROM {instances} WHERE schema = $1 AND id = $2"),
                        args![type_name, id],
                    )?,
                }
                self.drop_row(db, type_name, id)?;
            }
            StoreWrite::Gone { type_name, id } => {
                if self.row(db, type_name, id)?.is_none() {
                    self.drop_row(db, type_name, id)?;
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
                            db,
                            &format!(
                                "INSERT INTO {sub_table} (schema, id, use_id, tbl, key, data)
                                 VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING"
                            ),
                            args![type_name, id, use_id, table, key, text(&values)?],
                        )?;
                    }
                    SubRowChange::Update { values } => {
                        let held = db.query_opt(
                            &format!(
                                "SELECT data FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4 AND key = $5"
                            ),
                            args![type_name, id, use_id, table, key],
                        )?;
                        let Some(held) = held else {
                            return Err(Stop::Moved);
                        };
                        let data = merged(held.json(0)?, values);
                        self.one(
                            db,
                            &format!(
                                "UPDATE {sub_table} SET data = $1 WHERE schema = $2 AND id = $3 AND use_id = $4 AND tbl = $5 AND key = $6"
                            ),
                            args![text(&data)?, type_name, id, use_id, table, key],
                        )?;
                    }
                    SubRowChange::Delete => self.one(
                        db,
                        &format!(
                            "DELETE FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4 AND key = $5"
                        ),
                        args![type_name, id, use_id, table, key],
                    )?,
                    other => {
                        return Err(Stop::Refused(Refusal::internal(format!(
                            "the development store takes no sub-table change of this kind: {other:?}"
                        ))))
                    }
                }
            }
            StoreWrite::SharedRow {
                type_name,
                use_id,
                table,
                key,
                change,
            } => match change {
                SharedRowChange::Insert { values } => self.one(
                    db,
                    &format!(
                        "INSERT INTO {shared} (schema, use_id, tbl, key, data, version)
                         VALUES ($1, $2, $3, $4, $5, 1) ON CONFLICT DO NOTHING"
                    ),
                    args![type_name, use_id, table, key, text(values)?],
                )?,
                SharedRowChange::Update {
                    expected_version,
                    values,
                } => {
                    let Some(held) = self.shared_row(db, type_name, use_id, table, key)? else {
                        return Err(Stop::Moved);
                    };
                    let data = merged(held.values, values);
                    self.one(
                        db,
                        &format!(
                            "UPDATE {shared} SET data = $1, version = version + 1
                             WHERE schema = $2 AND use_id = $3 AND tbl = $4 AND key = $5 AND version = $6"
                        ),
                        args![text(&data)?, type_name, use_id, table, key, expected_version],
                    )?;
                }
                // A delete held to its version (D147): one a writer moved or
                // deleted first deletes nothing, and the command runs again.
                SharedRowChange::Delete { expected_version } => self.one(
                    db,
                    &format!(
                        "DELETE FROM {shared}
                         WHERE schema = $1 AND use_id = $2 AND tbl = $3 AND key = $4 AND version = $5"
                    ),
                    args![type_name, use_id, table, key, expected_version],
                )?,
                other => {
                    return Err(Stop::Refused(Refusal::internal(format!(
                        "the development store takes no shared-table change of this kind: {other:?}"
                    ))))
                }
            },
            StoreWrite::DropUse {
                type_name,
                behavior,
                use_id,
            } => {
                if let Ok(sub_table) = self.sub_table(behavior) {
                    db.execute(
                        &format!("DELETE FROM {sub_table} WHERE schema = $1 AND use_id = $2"),
                        args![type_name, use_id],
                    )?;
                }
                db.execute(
                    &format!("DELETE FROM {shared} WHERE schema = $1 AND use_id = $2"),
                    args![type_name, use_id],
                )?;
            }
            StoreWrite::Arm {
                type_name,
                id,
                use_id,
                name,
                at,
                payload,
            } => {
                db.execute(
                    &format!(
                        "INSERT INTO {deadlines} (schema, id, use_id, name, at, due, payload)
                         VALUES ($1, $2, $3, $4, $5, $5, $6)
                         ON CONFLICT (schema, id, use_id, name)
                         DO UPDATE SET at = excluded.at, due = excluded.due, payload = excluded.payload"
                    ),
                    args![type_name, id, use_id, name, at, text(payload)?],
                )?;
            }
            StoreWrite::Clear {
                type_name,
                id,
                use_id,
                name,
            } => {
                db.execute(
                    &format!(
                        "DELETE FROM {deadlines} WHERE schema = $1 AND id = $2 AND use_id = $3 AND name = $4"
                    ),
                    args![type_name, id, use_id, name],
                )?;
            }
            StoreWrite::ClearUse { type_name, use_id } => {
                db.execute(
                    &format!("DELETE FROM {deadlines} WHERE schema = $1 AND use_id = $2"),
                    args![type_name, use_id],
                )?;
            }
            StoreWrite::Spend {
                type_name,
                id,
                use_id,
                name,
                at,
            } => self.one(
                db,
                &format!(
                    "DELETE FROM {deadlines} WHERE schema = $1 AND id = $2 AND use_id = $3 AND name = $4 AND at = $5"
                ),
                args![type_name, id, use_id, name, at],
            )?,
            StoreWrite::Release {
                type_name,
                id,
                use_id,
                name,
                due,
            } => self.one(
                db,
                &format!(
                    "UPDATE {deadlines} SET due = $1 WHERE schema = $2 AND id = $3 AND use_id = $4 AND name = $5"
                ),
                args![due, type_name, id, use_id, name],
            )?,
            StoreWrite::Event {
                type_name,
                id,
                use_id,
                kind,
                at,
                payload,
            } => {
                let sql = format!(
                    "INSERT INTO {} (schema, id, use_id, kind, at, payload) VALUES ($1, $2, $3, $4, $5, $6)",
                    self.t("dev_events")
                );
                db.execute(&sql, args![type_name, id, use_id, kind, at, text(payload)?])?;
            }
            StoreWrite::Change {
                type_name,
                id,
                change,
            } => {
                let sql = format!(
                    "INSERT INTO {} (schema, id, change) VALUES ($1, $2, $3)",
                    self.t("dev_changes")
                );
                db.execute(&sql, args![type_name, id, text(change)?])?;
            }
            StoreWrite::Dequeue { seq } => {
                let sql = format!("DELETE FROM {} WHERE seq = $1", self.t("dev_changes"));
                self.one(db, &sql, args![seq])?;
            }
        }
        Ok(())
    }

    /// The deadlines due at `now`, in the order they fell due.
    pub(crate) fn due(&self, db: &mut impl Db, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        let sql = format!(
            "SELECT schema, id, use_id, name, at, payload FROM {} WHERE due <= $1
             ORDER BY due, schema{c}, id{c}, use_id{c}, name{c}",
            self.t("dev_deadlines"),
            c = self.collate
        );
        db.query(&sql, args![now])?
            .iter()
            .map(|row| {
                Ok(DueDeadline {
                    type_name: row.text(0)?,
                    id: row.text(1)?,
                    use_id: row.text(2)?,
                    name: row.text(3)?,
                    at: row.int(4)?,
                    payload: row.json(5)?,
                })
            })
            .collect()
    }

    /// The change queue, in the order written.
    pub(crate) fn queued(&self, db: &mut impl Db) -> Result<Vec<QueuedChange>, Refusal> {
        let sql = format!(
            "SELECT seq, schema, id, change FROM {} ORDER BY seq",
            self.t("dev_changes")
        );
        db.query(&sql, args![])?
            .iter()
            .map(|row| {
                Ok(QueuedChange {
                    seq: row.int(0)?,
                    type_name: row.text(1)?,
                    id: row.text(2)?,
                    change: row.json::<RowChange>(3)?,
                })
            })
            .collect()
    }

    /// The event queue after `after`, in the order written.
    pub(crate) fn events(&self, db: &mut impl Db, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        let sql = format!(
            "SELECT seq, schema, id, use_id, kind, at, payload FROM {} WHERE seq > $1 ORDER BY seq",
            self.t("dev_events")
        );
        db.query(&sql, args![after])?
            .iter()
            .map(|row| {
                Ok(QueuedEvent {
                    seq: row.int(0)?,
                    type_name: row.text(1)?,
                    id: row.text(2)?,
                    use_id: row.text(3)?,
                    kind: row.text(4)?,
                    at: row.int(5)?,
                    payload: row.json(6)?,
                })
            })
            .collect()
    }

    /// A store's answer to a read.
    pub(crate) fn read(&self, db: &mut impl Db, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        Ok(match read {
            StoreRead::Live => {
                let versions = self.t("dev_schema_versions");
                let sql = format!(
                    "SELECT schema, version, document FROM {versions} AS v
                     WHERE version = (SELECT MAX(version) FROM {versions} WHERE schema = v.schema)
                     ORDER BY schema{}",
                    self.collate
                );
                StoreAnswer::Live(
                    db.query(&sql, args![])?
                        .iter()
                        .map(|row| {
                            Ok(Version {
                                type_name: row.text(0)?,
                                version: row.int(1)?,
                                document: row.json(2)?,
                            })
                        })
                        .collect::<Result<_, Refusal>>()?,
                )
            }
            StoreRead::Draft { type_name } => {
                let sql = format!(
                    "SELECT document FROM {} WHERE schema = $1",
                    self.t("dev_schema_drafts")
                );
                StoreAnswer::Draft(
                    db.query_opt(&sql, args![type_name])?
                        .map(|row| {
                            Ok::<_, Refusal>(Draft {
                                type_name: type_name.clone(),
                                document: row.json(0)?,
                            })
                        })
                        .transpose()?,
                )
            }
            StoreRead::Row { type_name, id } => StoreAnswer::Row(self.row(db, type_name, id)?),
            StoreRead::Rows { type_name } => StoreAnswer::Rows(self.rows(db, type_name)?),
            StoreRead::SubRows {
                type_name,
                id,
                use_id,
                table,
            } => StoreAnswer::SubRows(self.sub_rows(db, type_name, id, use_id, table)?),
            StoreRead::TableSubRows {
                type_name,
                use_id,
                table,
            } => StoreAnswer::TableSubRows(self.table_sub_rows(db, type_name, use_id, table)?),
            StoreRead::SharedRow {
                type_name,
                use_id,
                table,
                key,
            } => StoreAnswer::SharedRow(self.shared_row(db, type_name, use_id, table, key)?),
            StoreRead::Armed {
                type_name,
                id,
                use_id,
                name,
            } => {
                let sql = format!(
                    "SELECT at, payload FROM {} WHERE schema = $1 AND id = $2 AND use_id = $3 AND name = $4",
                    self.t("dev_deadlines")
                );
                StoreAnswer::Armed(
                    db.query_opt(&sql, args![type_name, id, use_id, name])?
                        .map(|row| {
                            Ok::<_, Refusal>(Armed {
                                at: row.int(0)?,
                                payload: row.json(1)?,
                            })
                        })
                        .transpose()?,
                )
            }
            StoreRead::Page { .. } | StoreRead::Holders { .. } | StoreRead::Reached { .. } => {
                return Err(superschematic_rules_handler::store::unqueried(read))
            }
        })
    }

    /// Applies a unit in a savepoint, whole or not at all: false when one
    /// of its fences did not hold, and nothing applied.
    pub(crate) fn apply(&self, db: &mut impl Db, unit: &Unit) -> Result<bool, Refusal> {
        db.batch("SAVEPOINT dev_unit")?;
        for write in &unit.writes {
            if let Err(stop) = self.apply_one(db, write) {
                db.batch("ROLLBACK TO SAVEPOINT dev_unit; RELEASE SAVEPOINT dev_unit")?;
                return match stop {
                    Stop::Moved => Ok(false),
                    Stop::Refused(refusal) => Err(refusal),
                };
            }
        }
        db.batch("RELEASE SAVEPOINT dev_unit")?;
        Ok(true)
    }
}
