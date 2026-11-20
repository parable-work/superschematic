//! Development mode's store in a Postgres schema (D117): the same fixed
//! tables as SQLite's, in a schema of their own, created when the store
//! opens and never after. Each request runs in one transaction that takes
//! the schema's advisory lock first, so it holds the store's one writer
//! from its start, as an IMMEDIATE transaction holds SQLite's; a unit
//! applies in a savepoint, whole or not at all. Rows and sub-table rows
//! read in their keys' order by code point (`COLLATE "C"`), as SQLite's
//! do.

use std::collections::BTreeMap;

use postgres::types::ToSql;
use postgres::{Client, NoTls};
use serde_json::{Map, Value};
use superschematic_rules::{
    Code, Refusal, RowChange, SharedRow, SharedRowChange, SubRow, SubRowChange,
};
use superschematic_rules_handler::document::storage_key;
use superschematic_rules_handler::{
    Armed, Declarations, Draft, DueDeadline, HeldSubRow, QueuedChange, QueuedEvent, Store,
    StoreAnswer, StoreRead, StoreWrite, StoredRow, Unit, Version,
};

use crate::LAYOUT_VERSION;

/// The development layout in a schema of a Postgres database.
pub struct PgStore {
    client: Client,
    schema: String,
    sub_tables: BTreeMap<String, String>,
    open: bool,
}

fn failed(error: postgres::Error) -> Refusal {
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

enum Stop {
    Moved,
    Refused(Refusal),
}

impl From<postgres::Error> for Stop {
    fn from(error: postgres::Error) -> Self {
        Stop::Refused(failed(error))
    }
}

impl From<Refusal> for Stop {
    fn from(refusal: Refusal) -> Self {
        Stop::Refused(refusal)
    }
}

/// Whether a name may be a schema's: a letter or `_`, then letters,
/// digits and `_`, at most 63 bytes.
fn is_identifier(name: &str) -> bool {
    let mut chars = name.chars();
    name.len() <= 63
        && chars
            .next()
            .is_some_and(|c| c.is_ascii_alphabetic() || c == '_')
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_')
}

impl PgStore {
    /// The store in a schema of the database a URL names, its tables
    /// created when they are not there, for the behaviors `declarations`
    /// declares.
    pub fn open(url: &str, schema: &str, declarations: &Declarations) -> Result<Self, Refusal> {
        if !is_identifier(schema) {
            return Err(Refusal::invalid_argument(format!(
                "\"{schema}\" is no schema name: a letter or _, then letters, digits and _"
            )));
        }
        let mut client = Client::connect(url, NoTls).map_err(failed)?;
        let s = schema;
        client
            .batch_execute(&format!(
                "CREATE SCHEMA IF NOT EXISTS \"{s}\";
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_schema_versions (
                   schema TEXT NOT NULL, version BIGINT NOT NULL, document TEXT NOT NULL,
                   PRIMARY KEY (schema, version));
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_schema_drafts (
                   schema TEXT PRIMARY KEY, document TEXT NOT NULL);
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_instances (
                   schema TEXT NOT NULL, id TEXT NOT NULL, version BIGINT NOT NULL,
                   data TEXT NOT NULL, owned TEXT NOT NULL,
                   PRIMARY KEY (schema, id));
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_deadlines (
                   schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, name TEXT NOT NULL,
                   at BIGINT NOT NULL, due BIGINT NOT NULL, payload TEXT NOT NULL,
                   PRIMARY KEY (schema, id, use_id, name));
                 CREATE INDEX IF NOT EXISTS dev_deadlines_due ON \"{s}\".dev_deadlines (due);
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_events (
                   seq BIGSERIAL PRIMARY KEY, schema TEXT NOT NULL, id TEXT NOT NULL,
                   use_id TEXT NOT NULL, kind TEXT NOT NULL, at BIGINT NOT NULL, payload TEXT NOT NULL);
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_changes (
                   seq BIGSERIAL PRIMARY KEY, schema TEXT NOT NULL, id TEXT NOT NULL,
                   change TEXT NOT NULL);
                 CREATE TABLE IF NOT EXISTS \"{s}\".dev_shared (
                   schema TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL, key TEXT NOT NULL,
                   data TEXT NOT NULL, version BIGINT NOT NULL,
                   PRIMARY KEY (schema, use_id, tbl, key));"
            ))
            .map_err(failed)?;
        client
            .execute(
                &format!(
                    "INSERT INTO \"{s}\".dev_meta (key, value) VALUES ('layout', $1) ON CONFLICT (key) DO NOTHING"
                ),
                &[&LAYOUT_VERSION],
            )
            .map_err(failed)?;
        let mut sub_tables = BTreeMap::new();
        for declared in declarations
            .iter()
            .filter(|declared| declared.has_row_tables())
        {
            let table = format!("\"{s}\".\"dev_sub_{}\"", storage_key(&declared.name));
            client
                .batch_execute(&format!(
                    "CREATE TABLE IF NOT EXISTS {table} (
                       schema TEXT NOT NULL, id TEXT NOT NULL, use_id TEXT NOT NULL, tbl TEXT NOT NULL,
                       key TEXT NOT NULL, data TEXT NOT NULL,
                       PRIMARY KEY (schema, id, use_id, tbl, key));"
                ))
                .map_err(failed)?;
            sub_tables.insert(declared.name.clone(), table);
        }
        Ok(PgStore {
            client,
            schema: schema.to_owned(),
            sub_tables,
            open: false,
        })
    }

    /// Deletes every row of every table of the store: a fresh store in a
    /// schema that held one.
    pub fn clear(&mut self) -> Result<(), Refusal> {
        let s = &self.schema;
        let mut tables: Vec<String> = [
            "dev_schema_versions",
            "dev_schema_drafts",
            "dev_instances",
            "dev_deadlines",
            "dev_events",
            "dev_changes",
            "dev_shared",
        ]
        .iter()
        .map(|table| format!("\"{s}\".{table}"))
        .collect();
        tables.extend(self.sub_tables.values().cloned());
        self.client
            .batch_execute(&format!("TRUNCATE {}", tables.join(", ")))
            .map_err(failed)
    }

    /// Drops the store's schema and everything in it.
    pub fn drop_schema(mut self) -> Result<(), Refusal> {
        let s = self.schema.clone();
        self.client
            .batch_execute(&format!("DROP SCHEMA IF EXISTS \"{s}\" CASCADE"))
            .map_err(failed)
    }

    fn t(&self, table: &str) -> String {
        format!("\"{}\".{table}", self.schema)
    }

    fn sub_table(&self, behavior: &str) -> Result<String, Refusal> {
        self.sub_tables.get(behavior).cloned().ok_or_else(|| {
            Refusal::internal(format!(
                "the development store keeps no sub-table of {behavior}, which declares none"
            ))
        })
    }

    fn row(&mut self, type_name: &str, id: &str) -> Result<Option<StoredRow>, Refusal> {
        let sql = format!(
            "SELECT version, data, owned FROM {} WHERE schema = $1 AND id = $2",
            self.t("dev_instances")
        );
        let row = self
            .client
            .query_opt(&sql, &[&type_name, &id])
            .map_err(failed)?;
        row.map(|row| {
            Ok(StoredRow {
                id: id.to_owned(),
                version: row.get(0),
                data: parse(row.get(1))?,
                owned: parse(row.get(2))?,
            })
        })
        .transpose()
    }

    fn rows(&mut self, type_name: &str) -> Result<Vec<StoredRow>, Refusal> {
        let sql = format!(
            "SELECT id, version, data, owned FROM {} WHERE schema = $1 ORDER BY id COLLATE \"C\"",
            self.t("dev_instances")
        );
        let rows = self.client.query(&sql, &[&type_name]).map_err(failed)?;
        rows.iter()
            .map(|row| {
                Ok(StoredRow {
                    id: row.get(0),
                    version: row.get(1),
                    data: parse(row.get(2))?,
                    owned: parse(row.get(3))?,
                })
            })
            .collect()
    }

    /// A row's rows of one use's sub-table, in key order.
    pub fn sub_rows(
        &mut self,
        type_name: &str,
        id: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<SubRow>, Refusal> {
        let mut out = Vec::new();
        let tables: Vec<String> = self.sub_tables.values().cloned().collect();
        for sub_table in tables {
            let sql = format!(
                "SELECT key, data FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4"
            );
            for row in self
                .client
                .query(&sql, &[&type_name, &id, &use_id, &table])
                .map_err(failed)?
            {
                out.push(SubRow::new(row.get::<_, String>(0), parse(row.get(1))?));
            }
        }
        out.sort_by(|a, b| a.key.cmp(&b.key));
        Ok(out)
    }

    fn table_sub_rows(
        &mut self,
        type_name: &str,
        use_id: &str,
        table: &str,
    ) -> Result<Vec<HeldSubRow>, Refusal> {
        let mut out = Vec::new();
        let tables: Vec<String> = self.sub_tables.values().cloned().collect();
        for sub_table in tables {
            let sql = format!(
                "SELECT id, key, data FROM {sub_table} WHERE schema = $1 AND use_id = $2 AND tbl = $3"
            );
            for row in self
                .client
                .query(&sql, &[&type_name, &use_id, &table])
                .map_err(failed)?
            {
                out.push(HeldSubRow {
                    id: row.get(0),
                    row: SubRow::new(row.get::<_, String>(1), parse(row.get(2))?),
                });
            }
        }
        out.sort_by(|a, b| (&a.id, &a.row.key).cmp(&(&b.id, &b.row.key)));
        Ok(out)
    }

    /// The row of a key of a use's shared table, with its version.
    pub fn shared_row(
        &mut self,
        type_name: &str,
        use_id: &str,
        table: &str,
        key: &str,
    ) -> Result<Option<SharedRow>, Refusal> {
        let sql = format!(
            "SELECT data, version FROM {} WHERE schema = $1 AND use_id = $2 AND tbl = $3 AND key = $4",
            self.t("dev_shared")
        );
        self.client
            .query_opt(&sql, &[&type_name, &use_id, &table, &key])
            .map_err(failed)?
            .map(|row| Ok(SharedRow::new(key, parse(row.get(0))?, row.get(1))))
            .transpose()
    }

    /// A row's armed deadlines: each its use, name, time and payload, by
    /// use and name.
    pub fn armed(
        &mut self,
        type_name: &str,
        id: &str,
    ) -> Result<Vec<(String, String, i64, Value)>, Refusal> {
        let sql = format!(
            "SELECT use_id, name, at, payload FROM {} WHERE schema = $1 AND id = $2",
            self.t("dev_deadlines")
        );
        let mut out: Vec<(String, String, i64, Value)> = Vec::new();
        for row in self
            .client
            .query(&sql, &[&type_name, &id])
            .map_err(failed)?
        {
            out.push((row.get(0), row.get(1), row.get(2), parse(row.get(3))?));
        }
        out.sort_by(|a, b| (&a.0, &a.1).cmp(&(&b.0, &b.1)));
        Ok(out)
    }

    fn one(&mut self, sql: &str, values: &[&(dyn ToSql + Sync)]) -> Result<(), Stop> {
        if self.client.execute(sql, values)? == 1 {
            Ok(())
        } else {
            Err(Stop::Moved)
        }
    }

    fn drop_row(&mut self, type_name: &str, id: &str) -> Result<(), Stop> {
        let tables: Vec<String> = self.sub_tables.values().cloned().collect();
        for sub_table in tables {
            self.client.execute(
                &format!("DELETE FROM {sub_table} WHERE schema = $1 AND id = $2"),
                &[&type_name, &id],
            )?;
        }
        let sql = format!(
            "DELETE FROM {} WHERE schema = $1 AND id = $2",
            self.t("dev_deadlines")
        );
        self.client.execute(&sql, &[&type_name, &id])?;
        Ok(())
    }

    #[allow(clippy::cognitive_complexity)]
    fn apply_one(&mut self, write: &StoreWrite) -> Result<(), Stop> {
        let instances = self.t("dev_instances");
        let deadlines = self.t("dev_deadlines");
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
                self.client
                    .execute(&sql, &[type_name, &text(document)?])?;
            }
            StoreWrite::PutVersion {
                type_name,
                version,
                document,
            } => {
                let versions = self.t("dev_schema_versions");
                let live: i64 = self
                    .client
                    .query_one(
                        &format!(
                            "SELECT COALESCE(MAX(version), 0) FROM {versions} WHERE schema = $1"
                        ),
                        &[type_name],
                    )?
                    .get(0);
                if live != version - 1 {
                    return Err(Stop::Moved);
                }
                self.client.execute(
                    &format!(
                        "INSERT INTO {versions} (schema, version, document) VALUES ($1, $2, $3)"
                    ),
                    &[type_name, version, &text(document)?],
                )?;
                let drafts = self.t("dev_schema_drafts");
                self.client.execute(
                    &format!("DELETE FROM {drafts} WHERE schema = $1"),
                    &[type_name],
                )?;
            }
            StoreWrite::Insert {
                type_name,
                id,
                data,
                owned,
            } => {
                let held = self.client.query_opt(
                    &format!("SELECT 1 FROM {instances} WHERE schema = $1 AND id = $2"),
                    &[type_name, id],
                )?;
                if held.is_some() {
                    return Err(Stop::Refused(Refusal::new(
                        Code::Conflict,
                        format!("{type_name} {id} already exists"),
                    )));
                }
                self.client.execute(
                    &format!(
                        "INSERT INTO {instances} (schema, id, version, data, owned) VALUES ($1, $2, 1, $3, $4)"
                    ),
                    &[type_name, id, &text(data)?, &text(owned)?],
                )?;
            }
            StoreWrite::Update {
                type_name,
                id,
                expected,
                data,
                owned,
            } => self.one(
                &format!(
                    "UPDATE {instances} SET data = $1, owned = $2, version = version + 1
                     WHERE schema = $3 AND id = $4 AND version = $5"
                ),
                &[&text(data)?, &text(owned)?, type_name, id, expected],
            )?,
            StoreWrite::Rewrite {
                type_name,
                id,
                data,
                owned,
            } => self.one(
                &format!(
                    "UPDATE {instances} SET data = $1, owned = $2 WHERE schema = $3 AND id = $4"
                ),
                &[&text(data)?, &text(owned)?, type_name, id],
            )?,
            StoreWrite::Delete {
                type_name,
                id,
                expected,
            } => {
                match expected {
                    Some(expected) => self.one(
                        &format!(
                            "DELETE FROM {instances} WHERE schema = $1 AND id = $2 AND version = $3"
                        ),
                        &[type_name, id, expected],
                    )?,
                    None => self.one(
                        &format!("DELETE FROM {instances} WHERE schema = $1 AND id = $2"),
                        &[type_name, id],
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
                                "INSERT INTO {sub_table} (schema, id, use_id, tbl, key, data)
                                 VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING"
                            ),
                            &[type_name, id, use_id, table, key, &text(&values)?],
                        )?;
                    }
                    SubRowChange::Update { values } => {
                        let held = self.client.query_opt(
                            &format!(
                                "SELECT data FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4 AND key = $5"
                            ),
                            &[type_name, id, use_id, table, key],
                        )?;
                        let Some(held) = held else {
                            return Err(Stop::Moved);
                        };
                        let mut data: Map<String, Value> = parse(held.get(0))?;
                        for (name, value) in values {
                            if value.is_null() {
                                data.shift_remove(name);
                            } else {
                                data.insert(name.clone(), value.clone());
                            }
                        }
                        self.one(
                            &format!(
                                "UPDATE {sub_table} SET data = $1 WHERE schema = $2 AND id = $3 AND use_id = $4 AND tbl = $5 AND key = $6"
                            ),
                            &[&text(&data)?, type_name, id, use_id, table, key],
                        )?;
                    }
                    SubRowChange::Delete => self.one(
                        &format!(
                            "DELETE FROM {sub_table} WHERE schema = $1 AND id = $2 AND use_id = $3 AND tbl = $4 AND key = $5"
                        ),
                        &[type_name, id, use_id, table, key],
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
            } => {
                let shared = self.t("dev_shared");
                match change {
                    SharedRowChange::Insert { values } => self.one(
                        &format!(
                            "INSERT INTO {shared} (schema, use_id, tbl, key, data, version)
                             VALUES ($1, $2, $3, $4, $5, 1) ON CONFLICT DO NOTHING"
                        ),
                        &[type_name, use_id, table, key, &text(values)?],
                    )?,
                    SharedRowChange::Update {
                        expected_version,
                        values,
                    } => {
                        let Some(held) = self.shared_row(type_name, use_id, table, key)? else {
                            return Err(Stop::Moved);
                        };
                        let mut data = held.values;
                        for (name, value) in values {
                            if value.is_null() {
                                data.shift_remove(name);
                            } else {
                                data.insert(name.clone(), value.clone());
                            }
                        }
                        self.one(
                            &format!(
                                "UPDATE {shared} SET data = $1, version = version + 1
                                 WHERE schema = $2 AND use_id = $3 AND tbl = $4 AND key = $5 AND version = $6"
                            ),
                            &[&text(&data)?, type_name, use_id, table, key, expected_version],
                        )?;
                    }
                    other => {
                        return Err(Stop::Refused(Refusal::internal(format!(
                            "the development store takes no shared-table change of this kind: {other:?}"
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
                    self.client.execute(
                        &format!("DELETE FROM {sub_table} WHERE schema = $1 AND use_id = $2"),
                        &[type_name, use_id],
                    )?;
                }
                let sql = format!(
                    "DELETE FROM {} WHERE schema = $1 AND use_id = $2",
                    self.t("dev_shared")
                );
                self.client.execute(&sql, &[type_name, use_id])?;
            }
            StoreWrite::Arm {
                type_name,
                id,
                use_id,
                name,
                at,
                payload,
            } => {
                self.client.execute(
                    &format!(
                        "INSERT INTO {deadlines} (schema, id, use_id, name, at, due, payload)
                         VALUES ($1, $2, $3, $4, $5, $5, $6)
                         ON CONFLICT (schema, id, use_id, name)
                         DO UPDATE SET at = excluded.at, due = excluded.due, payload = excluded.payload"
                    ),
                    &[type_name, id, use_id, name, at, &text(payload)?],
                )?;
            }
            StoreWrite::Clear {
                type_name,
                id,
                use_id,
                name,
            } => {
                self.client.execute(
                    &format!(
                        "DELETE FROM {deadlines} WHERE schema = $1 AND id = $2 AND use_id = $3 AND name = $4"
                    ),
                    &[type_name, id, use_id, name],
                )?;
            }
            StoreWrite::ClearUse { type_name, use_id } => {
                self.client.execute(
                    &format!("DELETE FROM {deadlines} WHERE schema = $1 AND use_id = $2"),
                    &[type_name, use_id],
                )?;
            }
            StoreWrite::Spend {
                type_name,
                id,
                use_id,
                name,
                at,
            } => self.one(
                &format!(
                    "DELETE FROM {deadlines} WHERE schema = $1 AND id = $2 AND use_id = $3 AND name = $4 AND at = $5"
                ),
                &[type_name, id, use_id, name, at],
            )?,
            StoreWrite::Release {
                type_name,
                id,
                use_id,
                name,
                due,
            } => self.one(
                &format!(
                    "UPDATE {deadlines} SET due = $1 WHERE schema = $2 AND id = $3 AND use_id = $4 AND name = $5"
                ),
                &[due, type_name, id, use_id, name],
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
                self.client
                    .execute(&sql, &[type_name, id, use_id, kind, at, &text(payload)?])?;
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
                self.client
                    .execute(&sql, &[type_name, id, &text(change)?])?;
            }
            StoreWrite::Dequeue { seq } => {
                let sql = format!("DELETE FROM {} WHERE seq = $1", self.t("dev_changes"));
                self.one(&sql, &[seq])?;
            }
        }
        Ok(())
    }
}

impl Store for PgStore {
    fn due(&mut self, now: i64) -> Result<Vec<DueDeadline>, Refusal> {
        let sql = format!(
            "SELECT schema, id, use_id, name, at, payload FROM {} WHERE due <= $1
             ORDER BY due, schema COLLATE \"C\", id COLLATE \"C\", use_id COLLATE \"C\", name COLLATE \"C\"",
            self.t("dev_deadlines")
        );
        self.client
            .query(&sql, &[&now])
            .map_err(failed)?
            .iter()
            .map(|row| {
                Ok(DueDeadline {
                    type_name: row.get(0),
                    id: row.get(1),
                    use_id: row.get(2),
                    name: row.get(3),
                    at: row.get(4),
                    payload: parse(row.get(5))?,
                })
            })
            .collect()
    }

    fn queued(&mut self) -> Result<Vec<QueuedChange>, Refusal> {
        let sql = format!(
            "SELECT seq, schema, id, change FROM {} ORDER BY seq",
            self.t("dev_changes")
        );
        self.client
            .query(&sql, &[])
            .map_err(failed)?
            .iter()
            .map(|row| {
                Ok(QueuedChange {
                    seq: row.get(0),
                    type_name: row.get(1),
                    id: row.get(2),
                    change: parse::<RowChange>(row.get(3))?,
                })
            })
            .collect()
    }

    fn events(&mut self, after: i64) -> Result<Vec<QueuedEvent>, Refusal> {
        let sql = format!(
            "SELECT seq, schema, id, use_id, kind, at, payload FROM {} WHERE seq > $1 ORDER BY seq",
            self.t("dev_events")
        );
        self.client
            .query(&sql, &[&after])
            .map_err(failed)?
            .iter()
            .map(|row| {
                Ok(QueuedEvent {
                    seq: row.get(0),
                    type_name: row.get(1),
                    id: row.get(2),
                    use_id: row.get(3),
                    kind: row.get(4),
                    at: row.get(5),
                    payload: parse(row.get(6))?,
                })
            })
            .collect()
    }

    fn begin(&mut self) -> Result<(), Refusal> {
        self.client.batch_execute("BEGIN").map_err(failed)?;
        self.open = true;
        let key = format!("superschematic-dev:{}", self.schema);
        self.client
            .execute(
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
                &[&key],
            )
            .map_err(failed)?;
        Ok(())
    }

    fn commit(&mut self) -> Result<(), Refusal> {
        self.open = false;
        self.client.batch_execute("COMMIT").map_err(failed)
    }

    fn rollback(&mut self) -> Result<(), Refusal> {
        if !self.open {
            return Ok(());
        }
        self.open = false;
        self.client.batch_execute("ROLLBACK").map_err(failed)
    }

    fn read(&mut self, read: &StoreRead) -> Result<StoreAnswer, Refusal> {
        Ok(match read {
            StoreRead::Live => {
                let versions = self.t("dev_schema_versions");
                let sql = format!(
                    "SELECT schema, version, document FROM {versions} AS v
                     WHERE version = (SELECT MAX(version) FROM {versions} WHERE schema = v.schema)
                     ORDER BY schema COLLATE \"C\""
                );
                let rows = self.client.query(&sql, &[]).map_err(failed)?;
                StoreAnswer::Live(
                    rows.iter()
                        .map(|row| {
                            Ok(Version {
                                type_name: row.get(0),
                                version: row.get(1),
                                document: parse(row.get(2))?,
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
                let row = self.client.query_opt(&sql, &[type_name]).map_err(failed)?;
                StoreAnswer::Draft(
                    row.map(|row| {
                        Ok::<_, Refusal>(Draft {
                            type_name: type_name.clone(),
                            document: parse(row.get(0))?,
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
            StoreRead::SharedRow {
                type_name,
                use_id,
                table,
                key,
            } => StoreAnswer::SharedRow(self.shared_row(type_name, use_id, table, key)?),
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
                let row = self
                    .client
                    .query_opt(&sql, &[type_name, id, use_id, name])
                    .map_err(failed)?;
                StoreAnswer::Armed(
                    row.map(|row| {
                        Ok::<_, Refusal>(Armed {
                            at: row.get(0),
                            payload: parse(row.get(1))?,
                        })
                    })
                    .transpose()?,
                )
            }
        })
    }

    fn apply(&mut self, unit: &Unit) -> Result<bool, Refusal> {
        self.client
            .batch_execute("SAVEPOINT dev_unit")
            .map_err(failed)?;
        for write in &unit.writes {
            if let Err(stop) = self.apply_one(write) {
                self.client
                    .batch_execute("ROLLBACK TO SAVEPOINT dev_unit; RELEASE SAVEPOINT dev_unit")
                    .map_err(failed)?;
                return match stop {
                    Stop::Moved => Ok(false),
                    Stop::Refused(refusal) => Err(refusal),
                };
            }
        }
        self.client
            .batch_execute("RELEASE SAVEPOINT dev_unit")
            .map_err(failed)?;
        Ok(true)
    }
}
