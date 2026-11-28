//! References and attachments over a dialect's host (D154, D185, #611):
//! the statements, locks and refusals of `orm::rowfeatures`, written once
//! in Postgres's words and parameters and run in the host's. The Postgres
//! host runs them as written; the SQLite host's [`Sql`] writes a parameter
//! `?n`, a cast `CAST(... AS TEXT)`, the clock as RFC 3339 text to the
//! millisecond, a ref block found through `json_each`, and no row or
//! advisory lock, since a SQLite transaction holds the database's one
//! write lock from its `BEGIN IMMEDIATE`, as the TypeScript runtime's
//! `rowfeatures.ts` and the Go runtime's `rowrefs` do.

use std::collections::{BTreeMap, BTreeSet, HashSet};
use std::future::Future;
use std::time::{Duration, SystemTime};

use serde_json::{json, Value};

use super::*;
use crate::bucket::{Bucket, SignedUrlOptions};
use crate::orm::{epoch_millis, rfc3339_millis, Definition, Error, Values};
use crate::uuid62::{parse_uuid, uuid_to_base62};

const DEFAULT_LIMIT: u32 = 100;
const MAX_LIMIT: u32 = 1000;
/// The most holders `referenced_by` reads of a row before it reads which
/// the reader sees.
const MAX_SCAN: u32 = 10_000;
/// The most keys one read of a repository asks for.
pub(super) const BATCH: usize = 500;
/// How long an upload's URL lives unless `begin` is told, and the longest
/// it may.
const DEFAULT_UPLOAD_SECONDS: u64 = 60 * 60;
const MAX_UPLOAD_SECONDS: u64 = 7 * 24 * 60 * 60;
/// How long after an upload's URL expires its never-completed file is
/// released.
const RELEASE_GRACE_SECONDS: u64 = 60 * 60;

/// The words of a statement that differ between Postgres and SQLite.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct Sql {
    sqlite: bool,
}

impl Sql {
    /// Postgres's words.
    #[cfg(feature = "postgres")]
    pub(crate) const POSTGRES: Sql = Sql { sqlite: false };
    /// SQLite's words.
    #[cfg(feature = "sqlite")]
    pub(crate) const SQLITE: Sql = Sql { sqlite: true };

    /// `text` with each `$n` a parameter of the dialect: `?n` on SQLite.
    /// A `$` that no digit follows, a JSON path's, is left as it is.
    pub(crate) fn statement(self, text: &str) -> String {
        if !self.sqlite {
            return text.to_owned();
        }
        let mut out = String::with_capacity(text.len());
        let mut chars = text.chars().peekable();
        while let Some(c) = chars.next() {
            if c == '$' && chars.peek().is_some_and(char::is_ascii_digit) {
                out.push('?');
            } else {
                out.push(c);
            }
        }
        out
    }

    /// `expr` as text, as a key or an id compares.
    pub(crate) fn text(self, expr: &str) -> String {
        if self.sqlite {
            format!("CAST({expr} AS TEXT)")
        } else {
            format!("{expr}::text")
        }
    }

    /// `expr` as an integer, as a count reads.
    pub(crate) fn int(self, expr: &str) -> String {
        if self.sqlite {
            format!("CAST({expr} AS INTEGER)")
        } else {
            format!("{expr}::int")
        }
    }

    /// The database's clock: on SQLite the RFC 3339 text to the
    /// millisecond its schema's defaults write, which orders as text.
    pub(crate) fn now(self) -> &'static str {
        if self.sqlite {
            "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
        } else {
            "now()"
        }
    }

    /// A share lock of the rows a read reads, none on SQLite.
    pub(crate) fn for_share(self) -> &'static str {
        if self.sqlite {
            ""
        } else {
            " FOR SHARE"
        }
    }

    /// Whether a statement may take an advisory lock: on SQLite the
    /// transaction holds the database's write lock already.
    pub(crate) fn locks(self) -> bool {
        !self.sqlite
    }
}

/// What the members run their statements through, a dialect's
/// repositories: their client, their definition, their session and the
/// readers of each table.
pub(crate) trait RowHost {
    /// The words the host's statements are written in.
    fn sql(&self) -> Sql;
    /// The database's definition.
    fn definition(&self) -> &Definition;
    /// Runs a statement written in the host's words, and reads each row's
    /// cells in order as JSON: text a string, a number a number, a
    /// Postgres boolean a boolean and SQLite's 1 or 0, NULL null.
    fn query(
        &self,
        sql: &str,
        args: &[Value],
    ) -> impl Future<Output = Result<Vec<Vec<Value>>, Error>>;
    /// Runs a statement that changes rows, and answers how many.
    fn execute(&self, sql: &str, args: &[Value]) -> impl Future<Output = Result<u64, Error>>;
    /// Whether the host's statements run in a transaction.
    fn in_transaction(&self) -> bool;
    /// The live rows of the table type `table` that `filter` finds and its
    /// reader sees, at most `limit`, through the table's own repository.
    fn find(
        &self,
        table: &str,
        filter: &Values,
        limit: u32,
    ) -> impl Future<Output = Result<Vec<Values>, Error>>;
    /// How many live rows of the table type `table` `filter` finds that its
    /// reader sees.
    fn count(&self, table: &str, filter: &Values) -> impl Future<Output = Result<u64, Error>>;
    /// The acting user the writes record.
    fn user(&self) -> Option<&str>;
    /// The subject of the caller the reads keep to, when they keep to one.
    fn subject(&self) -> Option<&str>;
}

/// A refusal of the declaration's rules: its code names which.
fn refused(code: &str, message: impl Into<String>, details: Value) -> Error {
    let details = match details {
        Value::Object(map) => Some(map),
        _ => None,
    };
    Error::refused(code, ROW_FEATURE_STATUS, message, details)
}

fn invalid(message: impl Into<String>) -> Error {
    refused("invalid", message, Value::Null)
}

/// A row's key as the database holds it: a UUID hyphenated.
pub(super) fn db_key(uuid: bool, key: &str) -> Result<String, Error> {
    if key.is_empty() {
        return Err(invalid(format!(
            "a row's key is a non-empty string, not {key:?}"
        )));
    }
    if !uuid {
        return Ok(key.to_owned());
    }
    parse_uuid(key)
        .map(|u| u.hyphenated().to_string())
        .ok_or_else(|| invalid(format!("{key:?} is no UUID")))
}

/// A row's key in its wire form: a UUID in base62.
fn wire_key(uuid: bool, text: &str) -> String {
    if uuid {
        parse_uuid(text).map_or_else(|| text.to_owned(), uuid_to_base62)
    } else {
        text.to_owned()
    }
}

pub(super) fn base62(text: &str) -> String {
    wire_key(true, text)
}

fn same_subject(a: &str, b: &str) -> bool {
    match (parse_uuid(a), parse_uuid(b)) {
        (Some(x), Some(y)) => x == y,
        _ => a == b,
    }
}

fn limit_of(limit: Option<u32>) -> Result<u32, Error> {
    match limit {
        None => Ok(DEFAULT_LIMIT),
        Some(limit) if (1..=MAX_LIMIT).contains(&limit) => Ok(limit),
        Some(limit) => Err(invalid(format!(
            "a limit is a whole number from 1 to {MAX_LIMIT}, not {limit}"
        ))),
    }
}

fn text(value: &Value) -> String {
    match value {
        Value::String(text) => text.clone(),
        Value::Null => String::new(),
        other => other.to_string(),
    }
}

fn optional(value: &Value) -> Option<String> {
    (!value.is_null()).then(|| text(value))
}

fn number(value: &Value) -> u64 {
    match value {
        Value::Number(n) => n.as_u64().unwrap_or(0),
        Value::String(s) => s.parse().unwrap_or(0),
        _ => 0,
    }
}

/// A boolean a row read holds: true on Postgres, 1 on SQLite.
fn truth(value: &Value) -> bool {
    value == &Value::Bool(true) || value.as_i64() == Some(1)
}

/// The host and what the members read through it, in its words.
pub(super) struct Host<H> {
    pub(super) inner: H,
}

impl<H: RowHost> Host<H> {
    fn sql(&self) -> Sql {
        self.inner.sql()
    }

    async fn rows(&self, sql: &str, args: &[Value]) -> Result<Vec<Vec<Value>>, Error> {
        self.inner
            .query(&self.inner.sql().statement(sql), args)
            .await
    }

    async fn row(&self, sql: &str, args: &[Value]) -> Result<Option<Vec<Value>>, Error> {
        Ok(self.rows(sql, args).await?.into_iter().next())
    }

    async fn execute(&self, sql: &str, args: &[Value]) -> Result<u64, Error> {
        self.inner
            .execute(&self.inner.sql().statement(sql), args)
            .await
    }

    /// The transaction a write that locks runs in, or the refusal of a
    /// connection outside one.
    fn transaction(&self, what: &str) -> Result<(), Error> {
        if self.inner.in_transaction() {
            Ok(())
        } else {
            Err(Error::Invalid(format!(
                "{what} takes a lock and writes more than one row (D154), so it runs in a transaction: open the repositories over one"
            )))
        }
    }

    /// Holds `name`'s advisory lock to the end of the transaction on
    /// Postgres; on SQLite the transaction holds the write lock.
    async fn lock(&self, name: &str) -> Result<(), Error> {
        if !self.sql().locks() {
            return Ok(());
        }
        self.execute(
            "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
            &[Value::from(name)],
        )
        .await
        .map(|_| ())
    }

    /// The keys of `keys` of the table type `table` the reader sees, in
    /// their wire form, through the table's own repository.
    async fn visible_keys(&self, table: &str, keys: &[String]) -> Result<HashSet<String>, Error> {
        let key = self
            .inner
            .definition()
            .table(table)?
            .key_column()
            .name
            .clone();
        let mut seen = HashSet::new();
        for batch in keys.chunks(BATCH) {
            let filter = Values::from_iter([(key.clone(), json!({ "in": batch }))]);
            let limit = u32::try_from(batch.len()).unwrap_or(u32::MAX);
            for row in self.inner.find(table, &filter, limit).await? {
                if let Some(value) = row.get(&key) {
                    seen.insert(text(value));
                }
            }
        }
        Ok(seen)
    }

    /// Not found unless the reader sees the row, as a read of a row it
    /// does not see answers.
    async fn must_see(&self, row: &RowRef, uuid: bool) -> Result<(), Error> {
        let key = wire_key(uuid, &db_key(uuid, &row.key)?);
        let column = self
            .inner
            .definition()
            .table(&row.table)?
            .key_column()
            .name
            .clone();
        let filter = Values::from_iter([(column, json!({ "eq": key }))]);
        if self.inner.count(&row.table, &filter).await? == 0 {
            return Err(Error::NotFound(format!(
                "{} {} is not there",
                row.table, row.key
            )));
        }
        Ok(())
    }

    /// The acting user the writes record, hyphenated, or null.
    fn user(&self) -> Value {
        self.inner
            .user()
            .and_then(parse_uuid)
            .map_or(Value::Null, |u| Value::from(u.hyphenated().to_string()))
    }
}

// ---- references ----

/// The reference operations over a host: what `References` of each
/// dialect runs.
pub(super) struct ReferencesIn<'d, H> {
    host: Host<H>,
    definition: &'d RowReferencesDefinition,
}

impl<'d, H: RowHost> ReferencesIn<'d, H> {
    pub(super) fn new(
        inner: H,
        definition: Option<&'d RowReferencesDefinition>,
    ) -> Result<Self, Error> {
        let definition = definition.ok_or_else(|| {
            Error::Invalid("the database keeps no references: no table declares @references or Comments (D154)".into())
        })?;
        Ok(Self {
            host: Host { inner },
            definition,
        })
    }

    fn columns(&self) -> String {
        let sql = self.host.sql();
        format!(
            r#"{} AS id, "target_table", "target_key", "role", {} AS added_by, "added_at""#,
            sql.text("id"),
            sql.text("added_by")
        )
    }

    fn holder(&self, table: &str) -> Result<&'d ReferencesHolder, Error> {
        self.definition.holders.get(table).ok_or_else(|| {
            refused(
                "not_a_holder",
                format!("{table} keeps no references: it is no @references table"),
                json!({ "table": table }),
            )
        })
    }

    fn target(&self, table: &str) -> Result<&'d ReferenceTarget, Error> {
        self.definition.targets.get(table).ok_or_else(|| {
            refused(
                "not_a_target",
                format!("{table} is no table a reference may point at"),
                json!({ "table": table }),
            )
        })
    }

    fn reference(&self, from: &RowRef, row: &[Value]) -> Reference {
        let table = text(&row[1]);
        let uuid = self.definition.targets.get(&table).is_some_and(|t| t.uuid);
        Reference {
            id: base62(&text(&row[0])),
            from: from.clone(),
            to: RowRef::new(table, wire_key(uuid, &text(&row[2]))),
            role: text(&row[3]),
            added_by: optional(&row[4]).map(|u| base62(&u)),
            added_at: text(&row[5]),
        }
    }

    async fn target_key(
        &self,
        target: &ReferenceTarget,
        key: &str,
    ) -> Result<Option<String>, Error> {
        let sql = format!(
            "SELECT {} AS key FROM {table} WHERE {key} = $1",
            self.host.sql().text(&target.key),
            key = target.key,
            table = target.table
        );
        Ok(self
            .host
            .row(&sql, &[Value::from(key)])
            .await?
            .map(|row| text(&row[0])))
    }

    fn wire(&self, row: &RowRef, uuid: bool) -> RowRef {
        RowRef::new(row.table.clone(), wire_key(uuid, &row.key))
    }

    pub(super) async fn add(
        &self,
        from: &RowRef,
        to: &RowRef,
        role: &str,
    ) -> Result<Reference, Error> {
        let holder = self.holder(&from.table)?;
        let target = self.target(&to.table)?;
        if let Some(tables) = &holder.tables {
            if !tables.contains(&to.table) {
                return Err(refused(
                    "table_not_allowed",
                    format!(
                        "{} points only at {}, and not at {}",
                        from.table,
                        tables.join(", "),
                        to.table
                    ),
                    json!({ "table": to.table, "allowed": tables }),
                ));
            }
        }
        if role.chars().count() > 128 {
            return Err(invalid(
                "a reference's role is a string of at most 128 characters",
            ));
        }
        let from_key = db_key(holder.uuid, &from.key)?;
        let to_key = db_key(target.uuid, &to.key)?;
        self.host.transaction("references().add")?;
        let h = &self.host;
        let columns = self.columns();
        h.must_see(from, holder.uuid).await?;
        h.must_see(to, target.uuid).await?;
        h.lock(&format!("references:{}:{from_key}", holder.table))
            .await?;
        let Some(key_text) = self.target_key(target, &to_key).await? else {
            return Err(Error::NotFound(format!(
                "{} {} is not there",
                to.table, to.key
            )));
        };
        let held = h
            .row(
                &format!(
                    r#"SELECT {columns} FROM {} WHERE {} = $1 AND "target_table" = $2 AND "target_key" = $3 AND "role" = $4"#,
                    holder.table, holder.row
                ),
                &[Value::from(from_key.clone()), Value::from(to.table.clone()), Value::from(key_text.clone()), Value::from(role)],
            )
            .await?;
        if let Some(held) = held {
            return Ok(self.reference(&self.wire(from, holder.uuid), &held));
        }
        let count = h
            .row(
                &format!(
                    "SELECT {} AS n FROM {} WHERE {} = $1",
                    h.sql().int("count(*)"),
                    holder.table,
                    holder.row
                ),
                &[Value::from(from_key.clone())],
            )
            .await?
            .map_or(0, |row| number(&row[0]));
        if count >= holder.max_per_row {
            return Err(refused(
                "too_many",
                format!(
                    "{} {} holds {count} references, the most it may",
                    from.table, from.key
                ),
                json!({ "max": holder.max_per_row }),
            ));
        }
        let inserted = h
            .row(
                &format!(
                    r#"INSERT INTO {} ({}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5) RETURNING {columns}"#,
                    holder.table, holder.row
                ),
                &[Value::from(from_key), Value::from(to.table.clone()), Value::from(key_text), Value::from(role), h.user()],
            )
            .await?
            .ok_or_else(|| Error::Database("the reference's insert returned no row".into()))?;
        Ok(self.reference(&self.wire(from, holder.uuid), &inserted))
    }

    pub(super) async fn remove(
        &self,
        from: &RowRef,
        to: &RowRef,
        role: Option<&str>,
    ) -> Result<u64, Error> {
        let holder = self.holder(&from.table)?;
        let target = self.target(&to.table)?;
        let from_key = db_key(holder.uuid, &from.key)?;
        let to_key = db_key(target.uuid, &to.key)?;
        self.host.must_see(from, holder.uuid).await?;
        let mut args = vec![
            Value::from(from_key),
            Value::from(to.table.clone()),
            Value::from(to_key),
        ];
        let mut role_condition = "";
        if let Some(role) = role {
            args.push(Value::from(role));
            role_condition = r#" AND "role" = $4"#;
        }
        self.host
            .execute(
                &format!(
                    r#"DELETE FROM {} WHERE {} = $1 AND "target_table" = $2 AND "target_key" = {}{role_condition}"#,
                    holder.table,
                    holder.row,
                    self.host.sql().text("$3")
                ),
                &args,
            )
            .await
    }

    pub(super) async fn of(&self, row: &RowRef) -> Result<Vec<Reference>, Error> {
        let holder = self.holder(&row.table)?;
        let from_key = db_key(holder.uuid, &row.key)?;
        self.host.must_see(row, holder.uuid).await?;
        let from = self.wire(row, holder.uuid);
        let rows = self
            .host
            .rows(
                &format!(
                    r#"SELECT {} FROM {} WHERE {} = $1 ORDER BY "added_at", id"#,
                    self.columns(),
                    holder.table,
                    holder.row
                ),
                &[Value::from(from_key)],
            )
            .await?;
        let references: Vec<Reference> = rows.iter().map(|r| self.reference(&from, r)).collect();
        let mut by_table: BTreeMap<&str, Vec<String>> = BTreeMap::new();
        for r in &references {
            by_table
                .entry(&r.to.table)
                .or_default()
                .push(r.to.key.clone());
        }
        let mut seen: BTreeMap<String, HashSet<String>> = BTreeMap::new();
        for (table, keys) in by_table {
            let keep = if self.definition.targets.contains_key(table) {
                self.host.visible_keys(table, &keys).await?
            } else {
                HashSet::new()
            };
            seen.insert(table.to_owned(), keep);
        }
        Ok(references
            .into_iter()
            .filter(|r| {
                seen.get(&r.to.table)
                    .is_some_and(|keys| keys.contains(&r.to.key))
            })
            .collect())
    }

    pub(super) async fn referenced_by(
        &self,
        row: &RowRef,
        limit: Option<u32>,
    ) -> Result<Backlinks, Error> {
        let limit = limit_of(limit)? as usize;
        let target = self.target(&row.table)?;
        let key = db_key(target.uuid, &row.key)?;
        let h = &self.host;
        let sql = h.sql();
        h.must_see(row, target.uuid).await?;
        let Some(key_text) = self.target_key(target, &key).await? else {
            return Err(Error::NotFound(format!(
                "{} {} is not there",
                row.table, row.key
            )));
        };
        struct Found {
            table: String,
            key: String,
            via: BacklinkVia,
            role: Option<String>,
            message: Option<i64>,
            at: i64,
        }
        let mut found = Vec::new();
        for (table, holder) in &self.definition.holders {
            if holder
                .tables
                .as_ref()
                .is_some_and(|t| !t.contains(&row.table))
            {
                continue;
            }
            let rows = h
                .rows(
                    &format!(
                        r#"SELECT {} AS holder, "role", "added_at" FROM {} WHERE "target_table" = $1 AND "target_key" = $2 ORDER BY "added_at" DESC LIMIT {MAX_SCAN}"#,
                        sql.text(&holder.row),
                        holder.table
                    ),
                    &[Value::from(row.table.clone()), Value::from(key_text.clone())],
                )
                .await?;
            for r in rows {
                found.push(Found {
                    table: table.clone(),
                    key: wire_key(holder.uuid, &text(&r[0])),
                    via: BacklinkVia::Reference,
                    role: Some(text(&r[1])),
                    message: None,
                    at: epoch_millis(&text(&r[2])).unwrap_or(0),
                });
            }
        }
        let ids = if target.uuid {
            vec![base62(&key_text), key_text.clone()]
        } else {
            vec![key_text.clone()]
        };
        for (table, source) in &self.definition.comments {
            // A ref block of a message names the row: on Postgres a block
            // the blocks contain, on SQLite one json_each finds.
            let (condition, probes): (String, Vec<Value>) = if sql.sqlite {
                let ids_in = (0..ids.len())
                    .map(|i| format!("${}", i + 2))
                    .collect::<Vec<_>>()
                    .join(", ");
                let mut probes = vec![Value::from(row.table.clone())];
                probes.extend(ids.iter().map(|id| Value::from(id.clone())));
                (
                    format!(
                        "EXISTS (SELECT 1 FROM json_each({}) AS block WHERE json_extract(block.value, '$.kind') = 'ref' AND json_extract(block.value, '$.schema') = $1 AND json_extract(block.value, '$.id') IN ({ids_in}))",
                        source.blocks
                    ),
                    probes,
                )
            } else {
                let probes: Vec<Value> = ids
                    .iter()
                    .map(|id| json!([{ "kind": "ref", "schema": row.table, "id": id }]))
                    .collect();
                let conditions = (1..=probes.len())
                    .map(|i| format!("{} @> ${i}::jsonb", source.blocks))
                    .collect::<Vec<_>>()
                    .join(" OR ");
                (format!("({conditions})"), probes)
            };
            let rows = h
                .rows(
                    &format!(
                        "SELECT {} AS holder, {} AS message FROM {} WHERE {condition} AND {} IN ('posted', 'interrupted') ORDER BY {} DESC LIMIT {MAX_SCAN}",
                        sql.text(&source.row),
                        source.id,
                        source.table,
                        source.state,
                        source.id
                    ),
                    &probes,
                )
                .await?;
            for r in rows {
                let message = match &r[1] {
                    Value::Number(n) => n.as_i64().unwrap_or(0),
                    other => text(other).parse().unwrap_or(0),
                };
                found.push(Found {
                    table: table.clone(),
                    key: wire_key(source.uuid, &text(&r[0])),
                    via: BacklinkVia::Comment,
                    role: None,
                    message: Some(message),
                    at: message,
                });
            }
        }
        // Which of them the reader sees, through each holder's repository.
        let mut by_table: BTreeMap<String, BTreeSet<String>> = BTreeMap::new();
        for f in &found {
            by_table
                .entry(f.table.clone())
                .or_default()
                .insert(f.key.clone());
        }
        let mut seen: BTreeMap<String, HashSet<String>> = BTreeMap::new();
        let (mut holders, mut visible) = (0u64, 0u64);
        for (table, keys) in by_table {
            let keys: Vec<String> = keys.into_iter().collect();
            let keep = h.visible_keys(&table, &keys).await?;
            holders += keys.len() as u64;
            visible += keys.iter().filter(|k| keep.contains(*k)).count() as u64;
            seen.insert(table, keep);
        }
        found.sort_by_key(|a| std::cmp::Reverse(a.at));
        let mut items = Vec::new();
        for f in found {
            if items.len() >= limit {
                break;
            }
            if !seen.get(&f.table).is_some_and(|keys| keys.contains(&f.key)) {
                continue;
            }
            items.push(Backlink {
                table: f.table,
                key: f.key,
                via: f.via,
                role: f.role,
                message: f.message,
            });
        }
        if let (Some(subject), Some(owner), Some(restricted)) =
            (h.inner.subject(), &target.owner, &target.restricted)
        {
            if holders > visible {
                let own = h
                    .row(
                        &format!(
                            "SELECT {} AS owner, {restricted} IS NOT NULL AS restricted FROM {} WHERE {} = $1",
                            sql.text(owner),
                            target.table,
                            target.key
                        ),
                        &[Value::from(key)],
                    )
                    .await?;
                if let Some(own) = own {
                    if truth(&own[1]) && same_subject(&text(&own[0]), subject) {
                        return Ok(Backlinks {
                            items,
                            hidden: Some(holders - visible),
                        });
                    }
                }
            }
        }
        Ok(Backlinks {
            items,
            hidden: None,
        })
    }
}

// ---- attachments ----

const ATTACHMENT_COLUMNS: [&str; 17] = [
    "id",
    "row_key",
    r#""name""#,
    r#""media_type""#,
    r#""size""#,
    r#""content_hash""#,
    r#""object_key""#,
    r#""caption""#,
    r#""state""#,
    r#""uploaded_by""#,
    r#""uploaded_at""#,
    r#""expires_at""#,
    r#""filed_from""#,
    r#""extraction""#,
    r#""extracted_text""#,
    r#""preview""#,
    r#""preview_object""#,
];

/// The columns a file's read reads, in order: its id, its row's key, its
/// size and its uploader as text.
fn attachment_columns(sql: Sql, holder: &AttachmentsHolder) -> String {
    ATTACHMENT_COLUMNS
        .iter()
        .map(|c| match *c {
            "id" => format!("{} AS id", sql.text("id")),
            "row_key" => format!("{} AS row_key", sql.text(&holder.row)),
            r#""size""# => format!("{} AS size", sql.text(c)),
            r#""uploaded_by""# => format!("{} AS uploaded_by", sql.text(c)),
            _ => (*c).to_owned(),
        })
        .collect::<Vec<_>>()
        .join(", ")
}

fn media_type_allowed(allowed: Option<&Vec<String>>, media_type: &str) -> bool {
    let Some(allowed) = allowed.filter(|a| !a.is_empty()) else {
        return true;
    };
    let family = media_type.split('/').next().unwrap_or("");
    allowed.iter().any(|one| {
        one == media_type || one == "*/*" || one.strip_suffix("/*").is_some_and(|f| f == family)
    })
}

fn check_name(name: &str) -> Result<(), Error> {
    let length = name.chars().count();
    if !(1..=255).contains(&length)
        || name
            .chars()
            .any(|c| c == '/' || c == '\\' || c.is_control())
        || name.trim().is_empty()
    {
        return Err(invalid(format!(
            "a file's name is 1 to 255 characters with no /, \\ or control character, not {name:?}"
        )));
    }
    Ok(())
}

fn media_type_ok(media_type: &str) -> bool {
    let part = |s: &str| {
        let b = s.as_bytes();
        !b.is_empty()
            && b.len() <= 127
            && (b[0].is_ascii_lowercase() || b[0].is_ascii_digit())
            && b.iter()
                .all(|c| c.is_ascii_lowercase() || c.is_ascii_digit() || b"!#$&^_.+-".contains(c))
    };
    media_type
        .split_once('/')
        .is_some_and(|(a, b)| part(a) && part(b))
}

/// What `begin` takes: the row the file goes to, its name, media type,
/// size and SHA-256, and its caption.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct BeginFile {
    pub to: RowRef,
    pub name: String,
    pub media_type: String,
    pub size: u64,
    pub sha256: String,
    pub caption: Option<String>,
}

/// What `file` takes: the file, the row it goes to, whether it moves, and
/// the role of the reference its row makes (`filed` unless set).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct FileOnto {
    pub file: FileRef,
    pub to: RowRef,
    pub r#move: bool,
    pub role: Option<String>,
}

fn hyphenated(id: &str) -> String {
    parse_uuid(id)
        .map(|u| u.hyphenated().to_string())
        .unwrap_or_default()
}

/// The attachment operations over a host: what `Attachments` of each
/// dialect runs.
pub(super) struct AttachmentsIn<'d, H> {
    host: Host<H>,
    definition: &'d AttachmentsDefinition,
}

impl<'d, H: RowHost> AttachmentsIn<'d, H> {
    pub(super) fn new(
        inner: H,
        definition: Option<&'d AttachmentsDefinition>,
    ) -> Result<Self, Error> {
        let definition = definition.ok_or_else(|| {
            Error::Invalid(
                "the database holds no files: no table declares @attachments (D154)".into(),
            )
        })?;
        Ok(Self {
            host: Host { inner },
            definition,
        })
    }

    fn holder(&self, table: &str) -> Result<&'d AttachmentsHolder, Error> {
        self.definition.holders.get(table).ok_or_else(|| {
            refused(
                "not_a_holder",
                format!("{table} holds no files: it is no @attachments table"),
                json!({ "table": table }),
            )
        })
    }

    fn columns(&self, holder: &AttachmentsHolder) -> String {
        attachment_columns(self.host.sql(), holder)
    }

    fn attachment(table: &str, holder: &AttachmentsHolder, row: &[Value]) -> Attachment {
        Attachment {
            id: base62(&text(&row[0])),
            table: table.to_owned(),
            key: wire_key(holder.uuid, &text(&row[1])),
            name: text(&row[2]),
            media_type: text(&row[3]),
            size: number(&row[4]),
            content_hash: text(&row[5]),
            object_key: text(&row[6]),
            caption: optional(&row[7]),
            state: if text(&row[8]) == "ready" {
                FileState::Ready
            } else {
                FileState::Pending
            },
            uploaded_by: optional(&row[9]).map(|u| base62(&u)),
            uploaded_at: text(&row[10]),
            expires_at: optional(&row[11]),
            filed_from: optional(&row[12]),
            extraction: ProcessingState::of(&text(&row[13])),
            extracted_text: optional(&row[14]),
            preview: ProcessingState::of(&text(&row[15])),
            preview_object: optional(&row[16]),
        }
    }

    /// The file, read as the system, once the reader sees its row; not
    /// found for either.
    async fn read(
        &self,
        file: &FileRef,
        lock_row: &str,
    ) -> Result<(&'d AttachmentsHolder, Attachment), Error> {
        let holder = self.holder(&file.table)?;
        let none = || Error::NotFound(format!("{} holds no file {}", file.table, file.id));
        let Some(id) = parse_uuid(&file.id) else {
            return Err(none());
        };
        let row = self
            .host
            .row(
                &format!(
                    "SELECT {} FROM {} WHERE id = $1{lock_row}",
                    self.columns(holder),
                    holder.table
                ),
                &[Value::from(id.hyphenated().to_string())],
            )
            .await?
            .ok_or_else(none)?;
        let found = Self::attachment(&file.table, holder, &row);
        if !self
            .host
            .visible_keys(&file.table, std::slice::from_ref(&found.key))
            .await?
            .contains(&found.key)
        {
            return Err(none());
        }
        Ok((holder, found))
    }

    /// Holds one more file of `size` to the row's bounds, under the row's
    /// lock: the declaration's media types and bounds.
    async fn admit(
        &self,
        holder: &AttachmentsHolder,
        row: &RowRef,
        media_type: &str,
        size: u64,
    ) -> Result<(), Error> {
        if !media_type_allowed(holder.media_types.as_ref(), media_type) {
            let allowed = holder.media_types.clone().unwrap_or_default();
            return Err(refused(
                "media_type",
                format!(
                    "{} holds files of {}, and not {media_type}",
                    row.table,
                    allowed.join(", ")
                ),
                json!({ "mediaType": media_type, "allowed": allowed }),
            ));
        }
        if size > holder.max_file_bytes {
            return Err(refused(
                "too_large",
                format!(
                    "a file of {} is at most {} bytes, and this one is {size}",
                    row.table, holder.max_file_bytes
                ),
                json!({ "size": size, "max": holder.max_file_bytes }),
            ));
        }
        let key = db_key(holder.uuid, &row.key)?;
        self.host
            .lock(&format!("attachments:{}:{key}", holder.table))
            .await?;
        let sql = self.host.sql();
        let held = self
            .host
            .row(
                &format!(
                    r#"SELECT {} AS files, {} AS bytes FROM {} WHERE {} = $1 AND ("state" = 'ready' OR "expires_at" > {})"#,
                    sql.int("count(*)"),
                    sql.text(r#"COALESCE(sum("size"), 0)"#),
                    holder.table,
                    holder.row,
                    sql.now()
                ),
                &[Value::from(key)],
            )
            .await?
            .unwrap_or_default();
        let files = held.first().map_or(0, number);
        let bytes = held.get(1).map_or(0, number);
        if files + 1 > holder.max_files {
            return Err(refused(
                "row_full",
                format!(
                    "{} {} holds {files} files, the most a row may",
                    row.table, row.key
                ),
                json!({ "files": files, "max": holder.max_files }),
            ));
        }
        if bytes + size > holder.max_row_bytes {
            return Err(refused(
                "row_full",
                format!(
                    "{} {}'s files hold {bytes} bytes, and {size} more would pass its {}",
                    row.table, row.key, holder.max_row_bytes
                ),
                json!({ "bytes": bytes, "size": size, "max": holder.max_row_bytes }),
            ));
        }
        Ok(())
    }

    /// The database's half of `begin`: the pending file and its release
    /// once its URL has expired, held to the declaration's bounds.
    pub(super) async fn begin_row(
        &self,
        input: &BeginFile,
        expires_in: Option<Duration>,
    ) -> Result<(Attachment, u64, SystemTime), Error> {
        let to = &input.to;
        let holder = self.holder(&to.table)?;
        check_name(&input.name)?;
        if !media_type_ok(&input.media_type) {
            return Err(invalid(format!(
                "a file's media type is type/subtype in lower case with no parameters, not {:?}",
                input.media_type
            )));
        }
        if input.sha256.len() != 64
            || !input
                .sha256
                .bytes()
                .all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b))
        {
            return Err(invalid(
                "a file's sha256 is the SHA-256 of its bytes, 64 lowercase hex digits",
            ));
        }
        let seconds = expires_in.map_or(DEFAULT_UPLOAD_SECONDS, |d| d.as_secs());
        if !(1..=MAX_UPLOAD_SECONDS).contains(&seconds) {
            return Err(invalid(format!(
                "an upload's URL lives 1 to {MAX_UPLOAD_SECONDS} seconds, not {seconds}"
            )));
        }
        self.host.transaction("attachments().begin")?;
        let object_key = format!("attachments/{}", uuid::Uuid::new_v4());
        let now = SystemTime::now();
        let expires_at = now + Duration::from_secs(seconds);
        self.host.must_see(to, holder.uuid).await?;
        self.admit(holder, to, &input.media_type, input.size)
            .await?;
        let row = self
            .host
            .row(
                &format!(
                    r#"INSERT INTO {} ({}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "expires_at")
             VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9) RETURNING {}"#,
                    holder.table,
                    holder.row,
                    self.columns(holder)
                ),
                &[
                    Value::from(db_key(holder.uuid, &to.key)?),
                    Value::from(input.name.clone()),
                    Value::from(input.media_type.clone()),
                    Value::from(input.size.to_string()),
                    Value::from(input.sha256.clone()),
                    Value::from(object_key.clone()),
                    input.caption.clone().map_or(Value::Null, Value::from),
                    self.host.user(),
                    Value::from(rfc3339_millis(expires_at)),
                ],
            )
            .await?
            .ok_or_else(|| Error::Database("the file's insert returned no row".into()))?;
        // An upload never completed is released once its URL has expired.
        self.host
            .execute(
                &format!(
                    r#"INSERT INTO {} ("object_key", "content_hash", "reason", "due_at") VALUES ($1, $2, 'expired', $3)"#,
                    holder.released
                ),
                &[
                    Value::from(object_key),
                    Value::from(input.sha256.clone()),
                    Value::from(rfc3339_millis(
                        expires_at + Duration::from_secs(RELEASE_GRACE_SECONDS),
                    )),
                ],
            )
            .await?;
        Ok((
            Self::attachment(&to.table, holder, &row),
            seconds,
            expires_at,
        ))
    }

    #[cfg(feature = "postgres")]
    pub(super) async fn begin(
        &self,
        input: &BeginFile,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<(Attachment, Upload), Error> {
        let (attachment, seconds, expires_at) = self.begin_row(input, expires_in).await?;
        let url = sign_upload(bucket, &attachment, seconds).await?;
        Ok((attachment, upload(url, &input.media_type, expires_at)))
    }

    /// The file `complete` checks, once it is an upload under way whose
    /// URL has not expired.
    pub(super) async fn pending_upload(
        &self,
        file: &FileRef,
    ) -> Result<(&'d AttachmentsHolder, Attachment), Error> {
        let (holder, found) = self.read(file, "").await?;
        if found.state != FileState::Pending {
            return Err(refused(
                "not_pending",
                format!("file {} is ready, not an upload under way", file.id),
                json!({ "state": "ready" }),
            ));
        }
        let expires = found.expires_at.as_deref().and_then(epoch_millis);
        let now = epoch_millis(&rfc3339_millis(SystemTime::now())).unwrap_or(0);
        if expires.is_none_or(|at| at <= now) {
            return Err(refused(
                "expired",
                format!(
                    "the upload of file {} expired at {}",
                    file.id,
                    found.expires_at.clone().unwrap_or_default()
                ),
                Value::Null,
            ));
        }
        Ok((holder, found))
    }

    /// The database's half of `complete`, once the upload's bytes were
    /// read: the file made ready when they are what `begin` declared, and
    /// removed, with the refusal, when they are not.
    pub(super) async fn complete_row(
        &self,
        file: &FileRef,
        holder: &AttachmentsHolder,
        found: &Attachment,
        uploaded: &Uploaded,
    ) -> Result<Attachment, Error> {
        let id = hyphenated(&found.id);
        if uploaded.size != found.size || uploaded.hash != found.content_hash {
            self.host
                .execute(
                    &format!(
                        r#"DELETE FROM {} WHERE id = $1 AND "state" = 'pending'"#,
                        holder.table
                    ),
                    &[Value::from(id)],
                )
                .await?;
            let hash = if uploaded.size > found.size {
                Value::Null
            } else {
                Value::from(uploaded.hash.clone())
            };
            return Err(refused(
                "mismatch",
                format!(
                    "the bytes uploaded for file {} are not the ones its upload declared",
                    file.id
                ),
                json!({
                    "size": { "declared": found.size, "uploaded": uploaded.size },
                    "sha256": { "declared": found.content_hash, "uploaded": hash },
                }),
            ));
        }
        let row = self
            .host
            .row(
                &format!(
                    r#"UPDATE {} SET "state" = 'ready', "expires_at" = NULL, "uploaded_at" = {}, "extraction" = 'pending' WHERE id = $1 AND "state" = 'pending' RETURNING {}"#,
                    holder.table,
                    self.host.sql().now(),
                    self.columns(holder)
                ),
                &[Value::from(id)],
            )
            .await?
            .ok_or_else(|| {
                refused(
                    "not_pending",
                    format!("file {} is no longer an upload under way", file.id),
                    Value::Null,
                )
            })?;
        Ok(Self::attachment(&file.table, holder, &row))
    }

    #[cfg(feature = "postgres")]
    pub(super) async fn complete(
        &self,
        file: &FileRef,
        bucket: &dyn Bucket,
    ) -> Result<Attachment, Error> {
        let (holder, found) = self.pending_upload(file).await?;
        let uploaded = read_upload(bucket, &found).await?;
        self.complete_row(file, holder, &found, &uploaded).await
    }

    pub(super) async fn list(&self, row: &RowRef) -> Result<Vec<Attachment>, Error> {
        let holder = self.holder(&row.table)?;
        self.host.must_see(row, holder.uuid).await?;
        let rows = self
            .host
            .rows(
                &format!(
                    r#"SELECT {} FROM {} WHERE {} = $1 AND "state" = 'ready' ORDER BY "uploaded_at", id"#,
                    self.columns(holder),
                    holder.table,
                    holder.row
                ),
                &[Value::from(db_key(holder.uuid, &row.key)?)],
            )
            .await?;
        Ok(rows
            .iter()
            .map(|r| Self::attachment(&row.table, holder, r))
            .collect())
    }

    pub(super) async fn get(&self, file: &FileRef) -> Result<Attachment, Error> {
        Ok(self.read(file, "").await?.1)
    }

    /// The ready file `download` signs.
    pub(super) async fn ready(
        &self,
        file: &FileRef,
    ) -> Result<(&'d AttachmentsHolder, Attachment), Error> {
        let (holder, found) = self.read(file, "").await?;
        if found.state != FileState::Ready {
            return Err(refused(
                "not_ready",
                format!("file {} is still being uploaded", file.id),
                Value::Null,
            ));
        }
        Ok((holder, found))
    }

    /// The file a first view asks the preview of.
    pub(super) async fn viewed(
        &self,
        holder: &AttachmentsHolder,
        file: &FileRef,
        found: Attachment,
    ) -> Result<Attachment, Error> {
        if found.preview != ProcessingState::None {
            return Ok(found);
        }
        let row = self
            .host
            .row(
                &format!(
                    r#"UPDATE {} SET "preview" = 'pending' WHERE id = $1 AND "preview" = 'none' RETURNING {}"#,
                    holder.table,
                    self.columns(holder)
                ),
                &[Value::from(hyphenated(&found.id))],
            )
            .await?;
        Ok(match row {
            Some(row) => Self::attachment(&file.table, holder, &row),
            None => found,
        })
    }

    #[cfg(feature = "postgres")]
    pub(super) async fn download(
        &self,
        file: &FileRef,
        bucket: &dyn Bucket,
        expires_in: Option<Duration>,
    ) -> Result<Download, Error> {
        let (holder, found) = self.ready(file).await?;
        let (url, expires_at) = sign_download(bucket, &found, expires_in).await?;
        let attachment = self.viewed(holder, file, found).await?;
        Ok(Download {
            url,
            expires_at,
            attachment,
        })
    }

    pub(super) async fn caption(
        &self,
        file: &FileRef,
        caption: Option<&str>,
    ) -> Result<Attachment, Error> {
        if caption.is_some_and(|c| c.chars().count() > 2000) {
            return Err(invalid(
                "a caption is a string of at most 2000 characters, or null",
            ));
        }
        let (holder, found) = self.read(file, "").await?;
        let row = self
            .host
            .row(
                &format!(
                    r#"UPDATE {} SET "caption" = $2 WHERE id = $1 RETURNING {}"#,
                    holder.table,
                    self.columns(holder)
                ),
                &[
                    Value::from(hyphenated(&found.id)),
                    caption.map_or(Value::Null, Value::from),
                ],
            )
            .await?
            .ok_or_else(|| Error::NotFound(format!("{} holds no file {}", file.table, file.id)))?;
        Ok(Self::attachment(&file.table, holder, &row))
    }

    pub(super) async fn detach(&self, file: &FileRef) -> Result<(), Error> {
        let (holder, found) = self.read(file, "").await?;
        self.host
            .execute(
                &format!("DELETE FROM {} WHERE id = $1", holder.table),
                &[Value::from(hyphenated(&found.id))],
            )
            .await?;
        Ok(())
    }

    pub(super) async fn file(&self, input: &FileOnto) -> Result<Attachment, Error> {
        let to = self.holder(&input.to.table)?;
        self.host.transaction("attachments().file")?;
        let sql = self.host.sql();
        let (from, found) = self.read(&input.file, sql.for_share()).await?;
        if found.state != FileState::Ready {
            return Err(refused(
                "not_ready",
                format!("file {} is still being uploaded", input.file.id),
                Value::Null,
            ));
        }
        self.host.must_see(&input.to, to.uuid).await?;
        self.admit(to, &input.to, &found.media_type, found.size)
            .await?;
        self.host
            .lock(&format!("attachment-object:{}", found.object_key))
            .await?;
        let filed_from = format!("{}:{}:{}", input.file.table, found.key, found.id);
        let id = hyphenated(&found.id);
        let row = self
            .host
            .row(
                &format!(
                    r#"INSERT INTO {to_table} ({to_row}, "name", "media_type", "size", "content_hash", "object_key", "caption", "state", "uploaded_by", "filed_from", "extraction", "extracted_text", "preview", "preview_object")
             SELECT $1, "name", "media_type", "size", "content_hash", "object_key", "caption", 'ready', $2, $3, "extraction", "extracted_text", "preview", "preview_object" FROM {from_table} WHERE id = $4
             RETURNING {columns}"#,
                    to_table = to.table,
                    to_row = to.row,
                    from_table = from.table,
                    columns = self.columns(to)
                ),
                &[Value::from(db_key(to.uuid, &input.to.key)?), self.host.user(), Value::from(filed_from), Value::from(id.clone())],
            )
            .await?
            .ok_or_else(|| Error::Database("the filed copy's insert returned no row".into()))?;
        if input.r#move {
            self.host
                .execute(
                    &format!("DELETE FROM {} WHERE id = $1", from.table),
                    &[Value::from(id)],
                )
                .await?;
        }
        // The row the file was posted on points at the row it went to, so
        // that row lists it in its backlinks.
        let references = self.host.inner.definition().references.as_ref();
        let holder = references.and_then(|r| r.holders.get(&input.file.table));
        let target = references.and_then(|r| r.targets.get(&input.to.table));
        if let (Some(holder), Some(target)) = (holder, target) {
            if holder
                .tables
                .as_ref()
                .is_none_or(|t| t.contains(&input.to.table))
            {
                let to_key = self
                    .host
                    .row(
                        &format!(
                            "SELECT {} AS key FROM {} WHERE {key} = $1",
                            sql.text(&target.key),
                            target.table,
                            key = target.key
                        ),
                        &[Value::from(db_key(target.uuid, &input.to.key)?)],
                    )
                    .await?
                    .map_or(Value::Null, |row| row[0].clone());
                self.host
                    .execute(
                        &format!(
                            r#"INSERT INTO {table} ({row}, "target_table", "target_key", "role", "added_by") VALUES ($1, $2, $3, $4, $5)
             ON CONFLICT ({row}, "target_table", "target_key", "role") DO NOTHING"#,
                            table = holder.table,
                            row = holder.row
                        ),
                        &[
                            Value::from(db_key(holder.uuid, &found.key)?),
                            Value::from(input.to.table.clone()),
                            to_key,
                            Value::from(input.role.clone().unwrap_or_else(|| "filed".into())),
                            self.host.user(),
                        ],
                    )
                    .await?;
            }
        }
        Ok(Self::attachment(&input.to.table, to, &row))
    }

    /// The database's half of `release`: the uploads never completed that
    /// named the object removed, and whether no file names it now.
    pub(super) async fn release_rows(&self, object_key: &str) -> Result<bool, Error> {
        if object_key.is_empty() {
            return Err(invalid("a released message names its object"));
        }
        self.host.transaction("attachments().release")?;
        self.host
            .lock(&format!("attachment-object:{object_key}"))
            .await?;
        let sql = self.host.sql();
        let mut held = 0;
        for holder in self.definition.holders.values() {
            self.host
                .execute(
                    &format!(
                        r#"DELETE FROM {} WHERE "object_key" = $1 AND "state" = 'pending' AND "expires_at" <= {}"#,
                        holder.table,
                        sql.now()
                    ),
                    &[Value::from(object_key)],
                )
                .await?;
            held += self
                .host
                .row(
                    &format!(
                        r#"SELECT {} AS n FROM {} WHERE "object_key" = $1"#,
                        sql.int("count(*)"),
                        holder.table
                    ),
                    &[Value::from(object_key)],
                )
                .await?
                .map_or(0, |row| number(&row[0]));
        }
        Ok(held == 0)
    }

    #[cfg(feature = "postgres")]
    pub(super) async fn release(
        &self,
        object_key: &str,
        bucket: &dyn Bucket,
    ) -> Result<bool, Error> {
        if !self.release_rows(object_key).await? {
            return Ok(false);
        }
        delete_objects(bucket, object_key).await?;
        Ok(true)
    }

    pub(super) async fn pending(
        &self,
        kind: ProcessingKind,
        limit: Option<u32>,
    ) -> Result<Vec<PendingFile>, Error> {
        let limit = limit_of(limit)?;
        if self.definition.holders.is_empty() {
            return Ok(Vec::new());
        }
        let column = match kind {
            ProcessingKind::Preview => r#""preview""#,
            ProcessingKind::Extraction => r#""extraction""#,
        };
        let unions = self
            .definition
            .holders
            .values()
            .map(|holder| {
                format!(
                    r#"SELECT "object_key", "media_type", "content_hash", "uploaded_at" FROM {} WHERE {column} = 'pending' AND "state" = 'ready'"#,
                    holder.table
                )
            })
            .collect::<Vec<_>>()
            .join(" UNION ALL ");
        let rows = self
            .host
            .rows(
                &format!(
                    r#"SELECT "object_key", min("media_type") AS media_type, min("content_hash") AS content_hash FROM ({unions}) AS files GROUP BY "object_key" ORDER BY min("uploaded_at") LIMIT {limit}"#
                ),
                &[],
            )
            .await?;
        Ok(rows
            .iter()
            .map(|r| PendingFile {
                object_key: text(&r[0]),
                media_type: text(&r[1]),
                content_hash: text(&r[2]),
            })
            .collect())
    }

    pub(super) async fn processed(
        &self,
        object_key: &str,
        extraction: Option<&Processed>,
        preview: Option<&Processed>,
    ) -> Result<u64, Error> {
        let mut sets = Vec::new();
        let mut args = vec![Value::from(object_key)];
        for (made, state, value) in [
            (extraction, r#""extraction""#, r#""extracted_text""#),
            (preview, r#""preview""#, r#""preview_object""#),
        ] {
            let Some(made) = made else { continue };
            if !matches!(made.state, ProcessingState::Done | ProcessingState::Failed) {
                return Err(invalid("what a processor made is done or failed"));
            }
            args.push(Value::from(made.state.as_str()));
            args.push(made.value.clone().map_or(Value::Null, Value::from));
            sets.push(format!("{state} = ${}", args.len() - 1));
            sets.push(format!("{value} = ${}", args.len()));
        }
        if sets.is_empty() {
            return Ok(0);
        }
        let mut n = 0;
        for holder in self.definition.holders.values() {
            n += self
                .host
                .execute(
                    &format!(
                        r#"UPDATE {} SET {} WHERE "object_key" = $1"#,
                        holder.table,
                        sets.join(", ")
                    ),
                    &args,
                )
                .await?;
        }
        Ok(n)
    }
}

// ---- the bucket's half ----

/// The URL a browser PUTs a file's bytes to, which lives `seconds`.
pub(super) async fn sign_upload(
    bucket: &dyn Bucket,
    file: &Attachment,
    seconds: u64,
) -> Result<String, Error> {
    bucket
        .signed_url(
            &file.object_key,
            SignedUrlOptions::put(Duration::from_secs(seconds))
                .with_content_type(file.media_type.clone()),
        )
        .await
        .map_err(|err| Error::Database(format!("sign the upload of file {}: {err:?}", file.id)))
}

/// What a browser sends its upload with.
pub(super) fn upload(url: String, media_type: &str, expires_at: SystemTime) -> Upload {
    Upload {
        url,
        method: "PUT".into(),
        headers: BTreeMap::from([("content-type".to_owned(), media_type.to_owned())]),
        expires_at: rfc3339_millis(expires_at),
    }
}

/// The size and SHA-256 of an upload's bytes, read no further than one
/// byte past the size its `begin` declared.
pub(super) struct Uploaded {
    size: u64,
    hash: String,
}

/// Reads an upload's bytes back from the bucket, hashing them as they
/// stream.
pub(super) async fn read_upload(
    bucket: &dyn Bucket,
    found: &Attachment,
) -> Result<Uploaded, Error> {
    let mut reader = match bucket.get(&found.object_key).await {
        Ok(reader) => reader,
        Err(err) if err.is_not_found() => {
            return Err(refused(
                "not_uploaded",
                format!("the bucket holds no bytes of file {} yet", found.id),
                Value::Null,
            ))
        }
        Err(err) => {
            return Err(Error::Database(format!(
                "read the upload of file {}: {err:?}",
                found.id
            )))
        }
    };
    let mut digest = ring::digest::Context::new(&ring::digest::SHA256);
    let mut size = 0u64;
    while let Some(chunk) = reader
        .chunk()
        .await
        .map_err(|err| Error::Database(format!("read the upload of file {}: {err:?}", found.id)))?
    {
        size += chunk.len() as u64;
        if size > found.size {
            break;
        }
        digest.update(&chunk);
    }
    let hash = digest
        .finish()
        .as_ref()
        .iter()
        .map(|b| format!("{b:02x}"))
        .collect();
    Ok(Uploaded { size, hash })
}

/// A URL that downloads a file's bytes until it expires (an hour unless
/// set), and when it does.
pub(super) async fn sign_download(
    bucket: &dyn Bucket,
    found: &Attachment,
    expires_in: Option<Duration>,
) -> Result<(String, String), Error> {
    let seconds = expires_in.map_or(DEFAULT_UPLOAD_SECONDS, |d| d.as_secs());
    let url = bucket
        .signed_url(
            &found.object_key,
            SignedUrlOptions::get(Duration::from_secs(seconds)),
        )
        .await
        .map_err(|err| {
            Error::Database(format!("sign the download of file {}: {err:?}", found.id))
        })?;
    Ok((
        url,
        rfc3339_millis(SystemTime::now() + Duration::from_secs(seconds)),
    ))
}

/// Deletes an object and its preview's, each gone already or not.
pub(super) async fn delete_objects(bucket: &dyn Bucket, object_key: &str) -> Result<(), Error> {
    for name in [object_key.to_owned(), preview_object_of(object_key)] {
        match bucket.delete(&name).await {
            Ok(()) => {}
            Err(err) if err.is_not_found() => {}
            Err(err) => return Err(Error::Database(format!("delete {name}: {err:?}"))),
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_media_type_is_held_to_the_declaration() {
        let allowed = vec!["text/*".to_owned(), "application/pdf".to_owned()];
        assert!(media_type_allowed(Some(&allowed), "text/plain"));
        assert!(media_type_allowed(Some(&allowed), "application/pdf"));
        assert!(!media_type_allowed(Some(&allowed), "application/zip"));
        assert!(media_type_allowed(None, "application/zip"));
        assert!(media_type_ok("application/octet-stream"));
        assert!(!media_type_ok("Text/plain"));
        assert!(!media_type_ok("text/plain; charset=utf-8"));
    }

    #[test]
    fn a_name_has_no_slash_or_control_character() {
        assert!(check_name("plan.txt").is_ok());
        for name in ["", "a/b", "a\\b", "a\u{7}", "  "] {
            assert!(check_name(name).is_err(), "{name:?}");
        }
    }

    #[test]
    fn a_key_is_held_hyphenated_and_read_in_base62() {
        let hyphenated = "0c7d7f4e-2f3a-4b6e-9c1d-2a3b4c5d6e7f";
        let wire = base62(hyphenated);
        assert_eq!(db_key(true, &wire).unwrap(), hyphenated);
        assert_eq!(db_key(false, "K-1").unwrap(), "K-1");
        assert!(db_key(true, "not a key!").is_err());
    }

    // D182 (#611): a statement in Postgres's words is SQLite's with each
    // parameter ?n, a cast CAST(... AS TEXT), its clock and no lock; a
    // JSON path's $ is left as it is.
    #[cfg(all(feature = "postgres", feature = "sqlite"))]
    #[test]
    fn a_statement_is_written_in_its_dialects_words() {
        let pg = Sql::POSTGRES;
        let lite = Sql::SQLITE;
        let text = r#"SELECT 1 FROM t WHERE a = $1 AND json_extract(b, '$.kind') IN ($2, $10)"#;
        assert_eq!(pg.statement(text), text);
        assert_eq!(
            lite.statement(text),
            r#"SELECT 1 FROM t WHERE a = ?1 AND json_extract(b, '$.kind') IN (?2, ?10)"#
        );
        assert_eq!(pg.text("id"), "id::text");
        assert_eq!(lite.text("id"), "CAST(id AS TEXT)");
        assert_eq!(lite.int("count(*)"), "CAST(count(*) AS INTEGER)");
        assert_eq!(pg.now(), "now()");
        assert_eq!(lite.now(), "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')");
        assert_eq!((pg.for_share(), lite.for_share()), (" FOR SHARE", ""));
        assert!(pg.locks() && !lite.locks());
        assert!(truth(&Value::Bool(true)) && truth(&Value::from(1)) && !truth(&Value::from(0)));
    }
}
