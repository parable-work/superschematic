//! The queue client on SQLite (D53, amended; section 8.8 of
//! `docs/stack-model.md`), with the `sqlite` feature: the twin of the Go
//! ORM's `queues_sqlite.go` and of the TypeScript runtime's
//! `queue-sqlite.ts`, for a database whose DB service lists the sqlite
//! dialect. It runs the statements on SQLite a queue's descriptor carries
//! (`sqliteStatements`), over rusqlite and the SQLite it bundles, so a
//! message one language enqueues another claims alike.
//!
//! [`enqueue_sqlite`] writes a message through the caller's connection,
//! inside its transaction when it holds one (a rusqlite `Transaction` is a
//! `Connection`), so the message commits with the writes that caused it. A
//! [`SqliteQueue`] claims, completes, fails, extends, releases and prunes
//! messages for a worker, on a connection of its own, each call on tokio's
//! blocking pool, since rusqlite's calls block and a statement may wait for
//! another connection's write lock. SQLite has one writer at a time, so each
//! is one statement, which holds the write lock from its first read: a
//! claim is one `UPDATE ... RETURNING` of the due messages, oldest first,
//! and two workers that claim at once take different messages. The lease,
//! the attempts, the backoff, the dead messages and the retention are
//! Postgres's. Times are the database's clock, in RFC 3339 in UTC to the
//! millisecond.
//!
//! A message crosses to its row through its serde form, as on Postgres, each
//! field as the descriptor marks it: a JSON value as its JSON text; a list
//! as a JSON array; a UUID, base62 or hyphenated, as its lowercase
//! hyphenated text; a timestamp in RFC 3339 in UTC, its fraction kept; a
//! boolean as 1 or 0 (true or false in a list), read back as a boolean; and
//! a duration as its canonical text. A member a message leaves out or sets
//! to null takes its column's default, and an enum field with a schema
//! default writes that default; a NULL column leaves its member out.

use std::fmt;
use std::marker::PhantomData;
use std::path::Path;
use std::sync::{Arc, Mutex, PoisonError};
use std::time::Duration;

use rusqlite::types::{Value as SqlValue, ValueRef};
use rusqlite::{params_from_iter, Connection, OptionalExtension};
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::{Map, Value};

use super::duration::{format_duration, parse_duration};
use super::{unset, Claim, QueueDefinition, QueueError, QueueField, QueueStatements, CLAIM_LOST};
use crate::civil::{from_days, to_days};
use crate::error::chain;
use crate::uuid62::parse_uuid;

/// How long a statement waits for another connection's write lock.
pub const BUSY_TIMEOUT: Duration = Duration::from_secs(5);

/// The statements on SQLite of `queue`, which a database that lists the
/// sqlite dialect carries.
fn sqlite_statements(queue: &QueueDefinition) -> Result<&QueueStatements, QueueError> {
    queue.sqlite_statements.as_ref().ok_or_else(|| {
        QueueError::Definition(format!(
            "queue {} carries no statements on SQLite: its DB service lists no sqlite dialect, or its message holds a field the SQLite queue client cannot hold; see its types crate's queues.rs",
            queue.name
        ))
    })
}

/// A rusqlite error, with its causes, after `context`.
fn failed(context: &str, err: &rusqlite::Error) -> QueueError {
    QueueError::Failed(format!("{context}: {}", chain(err)))
}

/// Writes `message` into `queue` through `connection`, the caller's: in
/// its transaction when it holds one (pass a rusqlite `Transaction`), so
/// the message is due once the transaction commits, and never if it rolls
/// back. Each member the message sets writes its column; one it leaves
/// unset takes the column's default, and an enum with a schema default
/// writes that default.
pub fn enqueue_sqlite<M>(
    connection: &Connection,
    queue: &QueueDefinition,
    message: &M,
) -> Result<(), QueueError>
where
    M: Serialize + ?Sized,
{
    let statements = sqlite_statements(queue)?;
    let context = || format!("enqueue on {}", queue.table);
    let members = match serde_json::to_value(message) {
        Ok(Value::Object(members)) => members,
        Ok(other) => {
            return Err(QueueError::Failed(format!(
                "{}: the message is {other}, not an object",
                context()
            )))
        }
        Err(err) => return Err(QueueError::failed(&context(), &err)),
    };
    let mut columns = Vec::new();
    let mut values = Vec::new();
    for field in &queue.fields {
        let value = match members.get(&field.name) {
            Some(value) if !unset(Some(value), field) => value.clone(),
            _ => match &field.default {
                Some(default) => Value::String(default.clone()),
                None => continue,
            },
        };
        let column = column_value(&value, field).map_err(|err| {
            QueueError::Failed(format!("{}: field {}: {err}", context(), field.name))
        })?;
        columns.push(field.column.as_str());
        values.push(column);
    }
    let sql = if columns.is_empty() {
        format!("{} DEFAULT VALUES", statements.insert)
    } else {
        let placeholders: Vec<String> = (1..=columns.len()).map(|i| format!("?{i}")).collect();
        format!(
            "{} ({}) VALUES ({})",
            statements.insert,
            columns.join(", "),
            placeholders.join(", ")
        )
    };
    connection
        .prepare_cached(&sql)
        .and_then(|mut statement| statement.execute(params_from_iter(values)))
        .map_err(|err| failed(&context(), &err))?;
    Ok(())
}

/// The claims of a queue on a SQLite file, which a worker makes, its
/// messages read as `M`, over a connection of its own.
pub struct SqliteQueue<M> {
    connection: Arc<Mutex<Connection>>,
    definition: Arc<QueueDefinition>,
    message: PhantomData<fn() -> M>,
}

impl<M> Clone for SqliteQueue<M> {
    fn clone(&self) -> Self {
        Self {
            connection: Arc::clone(&self.connection),
            definition: Arc::clone(&self.definition),
            message: PhantomData,
        }
    }
}

impl<M> fmt::Debug for SqliteQueue<M> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("SqliteQueue")
            .field("name", &self.definition.name)
            .field("table", &self.definition.table)
            .finish_non_exhaustive()
    }
}

impl<M: DeserializeOwned + Send + 'static> SqliteQueue<M> {
    /// The claims of the queue `definition` describes, over `connection`.
    /// It refuses a descriptor with no statements on SQLite.
    pub fn new(connection: Connection, definition: QueueDefinition) -> Result<Self, QueueError> {
        sqlite_statements(&definition)?;
        Ok(Self {
            connection: Arc::new(Mutex::new(connection)),
            definition: Arc::new(definition),
            message: PhantomData,
        })
    }

    /// The claims of the queue `definition` describes on the SQLite file
    /// at `path`, which it opens with foreign keys on and a statement
    /// waiting up to [`BUSY_TIMEOUT`] for another connection's lock.
    pub fn open(path: impl AsRef<Path>, definition: QueueDefinition) -> Result<Self, QueueError> {
        let context = format!("open the SQLite database of {}", definition.table);
        let connection = Connection::open(path).map_err(|err| failed(&context, &err))?;
        connection
            .busy_timeout(BUSY_TIMEOUT)
            .and_then(|()| connection.execute_batch("PRAGMA foreign_keys = ON"))
            .map_err(|err| failed(&context, &err))?;
        Self::new(connection, definition)
    }

    /// The queue's descriptor.
    pub fn definition(&self) -> &QueueDefinition {
        &self.definition
    }

    /// The `@queue` class.
    pub fn name(&self) -> &str {
        &self.definition.name
    }

    /// How long a claim holds a message unless extended: the queue's
    /// lease.
    pub fn lease(&self) -> Duration {
        self.definition.lease()
    }

    /// Runs `work` on the connection on tokio's blocking pool.
    async fn with<T: Send + 'static>(
        &self,
        context: String,
        work: impl FnOnce(&Connection, &QueueDefinition, &str) -> Result<T, QueueError> + Send + 'static,
    ) -> Result<T, QueueError> {
        let connection = Arc::clone(&self.connection);
        let definition = Arc::clone(&self.definition);
        let task_context = context.clone();
        tokio::task::spawn_blocking(move || {
            let connection = connection.lock().unwrap_or_else(PoisonError::into_inner);
            work(&connection, &definition, &task_context)
        })
        .await
        .map_err(|err| QueueError::Failed(format!("{context}: {err}")))?
    }

    /// Marks dead each message whose last attempt's claim expired, then
    /// claims up to `n` due messages for `lease`, and reads each. A message
    /// that does not read as `M` fails the claim.
    pub async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<M>>, QueueError> {
        if n == 0 {
            return Ok(Vec::new());
        }
        let context = format!("claim on {}", self.definition.table);
        self.with(context, move |connection, definition, context| {
            let statements = sqlite_statements(definition)?;
            connection
                .prepare_cached(&statements.expire)
                .and_then(|mut s| s.execute([i64::from(definition.retries)]))
                .map_err(|err| failed(context, &err))?;
            let mut statement = connection
                .prepare_cached(&statements.claim)
                .map_err(|err| failed(context, &err))?;
            let n = i64::try_from(n).unwrap_or(i64::MAX);
            let mut rows = statement
                .query(rusqlite::params![n, lease.as_secs_f64()])
                .map_err(|err| failed(context, &err))?;
            let mut claims = Vec::new();
            while let Some(row) = rows.next().map_err(|err| failed(context, &err))? {
                claims.push(claim_of(definition, row).map_err(|err| match err {
                    ClaimRead::Sql(err) => failed(context, &err),
                    ClaimRead::Message(message) => QueueError::Failed(message),
                })?);
            }
            Ok(claims)
        })
        .await
    }

    /// Marks the message `id`, which `token` claims, done.
    pub async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.update("complete", |s| &s.complete, id, token, None)
            .await
    }

    /// Records `cause` on the message `id`, which `token` claims, and makes
    /// it due again after the backoff, doubled for each attempt before and
    /// capped at the maximum backoff, or dead when its retries are spent.
    /// Returns whether the message is dead.
    pub async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        let context = format!("fail message {id} on {}", self.definition.table);
        let (id, token, cause) = (id.to_string(), token.to_string(), cause.to_string());
        self.with(context, move |connection, definition, context| {
            let statements = sqlite_statements(definition)?;
            let state: Option<String> = connection
                .prepare_cached(&statements.fail)
                .and_then(|mut s| {
                    s.query_row(
                        rusqlite::params![
                            id,
                            token,
                            i64::from(definition.retries),
                            i64::try_from(definition.backoff_seconds).unwrap_or(i64::MAX),
                            cause,
                            i64::try_from(definition.max_backoff_seconds).unwrap_or(i64::MAX),
                        ],
                        |row| row.get(0),
                    )
                    .optional()
                })
                .map_err(|err| failed(context, &err))?;
            match state {
                None => Err(QueueError::ClaimLost(format!("{context}: {CLAIM_LOST}"))),
                Some(state) => Ok(state == "dead"),
            }
        })
        .await
    }

    /// Holds the message `id`, which `token` claims, for `lease` from now.
    pub async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        self.update(
            "extend",
            |s| &s.extend,
            id,
            token,
            Some(SqlValue::Real(lease.as_secs_f64())),
        )
        .await
    }

    /// Gives the message `id`, which `token` claims, back, due at once, its
    /// attempt not counted: its handler did not finish, and did not fail.
    pub async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        self.update("release", |s| &s.release, id, token, None)
            .await
    }

    /// Deletes up to `limit` messages done for the queue's retention or
    /// longer, the oldest first, and returns how many it deleted. It never
    /// deletes a dead message.
    pub async fn prune(&self, limit: usize) -> Result<u64, QueueError> {
        if limit == 0 {
            return Ok(0);
        }
        let context = format!("prune {}", self.definition.table);
        self.with(context, move |connection, definition, context| {
            let statements = sqlite_statements(definition)?;
            let deleted = connection
                .prepare_cached(&statements.prune)
                .and_then(|mut s| {
                    s.execute(rusqlite::params![
                        i64::try_from(definition.retain_seconds).unwrap_or(i64::MAX),
                        i64::try_from(limit).unwrap_or(i64::MAX),
                    ])
                })
                .map_err(|err| failed(context, &err))?;
            Ok(u64::try_from(deleted).unwrap_or(0))
        })
        .await
    }

    /// Runs the statement `which` picks, which changes the message `id`
    /// while `token` claims it, and refuses a change of no row as a lost
    /// claim.
    async fn update(
        &self,
        what: &str,
        which: fn(&QueueStatements) -> &String,
        id: &str,
        token: &str,
        extra: Option<SqlValue>,
    ) -> Result<(), QueueError> {
        let context = format!("{what} message {id} on {}", self.definition.table);
        let params: Vec<SqlValue> = [
            SqlValue::Text(id.to_string()),
            SqlValue::Text(token.to_string()),
        ]
        .into_iter()
        .chain(extra)
        .collect();
        self.with(context, move |connection, definition, context| {
            let statements = sqlite_statements(definition)?;
            let changed = connection
                .prepare_cached(which(statements))
                .and_then(|mut s| s.execute(params_from_iter(params)))
                .map_err(|err| failed(context, &err))?;
            if changed == 0 {
                return Err(QueueError::ClaimLost(format!("{context}: {CLAIM_LOST}")));
            }
            Ok(())
        })
        .await
    }
}

/// Why a claimed row did not read: a column SQLite would not give, or a
/// message that does not read as its type.
enum ClaimRead {
    Sql(rusqlite::Error),
    Message(String),
}

/// A claimed row, `[id, token, attempt, ...the message's columns]`, as its
/// claim.
fn claim_of<M: DeserializeOwned>(
    definition: &QueueDefinition,
    row: &rusqlite::Row<'_>,
) -> Result<Claim<M>, ClaimRead> {
    let id: String = row.get(0).map_err(ClaimRead::Sql)?;
    let token: String = row.get(1).map_err(ClaimRead::Sql)?;
    let attempt: i64 = row.get(2).map_err(ClaimRead::Sql)?;
    let table = &definition.table;
    let mut members = Map::new();
    for (i, field) in definition.fields.iter().enumerate() {
        let column = row.get_ref(3 + i).map_err(ClaimRead::Sql)?;
        let value = member_value(column, field).map_err(|err| {
            ClaimRead::Message(format!(
                "message {id} of {table}: field {}: {err}",
                field.name
            ))
        })?;
        if let Some(value) = value {
            members.insert(field.name.clone(), value);
        }
    }
    let message = serde_json::from_value(Value::Object(members))
        .map_err(|err| ClaimRead::Message(format!("message {id} of {table}: {err}")))?;
    Ok(Claim {
        attempt: u32::try_from(attempt).unwrap_or(0),
        id,
        token,
        message,
    })
}

/// What a member a message sets writes to its column on SQLite.
pub(crate) fn column_value(value: &Value, field: &QueueField) -> Result<SqlValue, String> {
    if field.json {
        return Ok(SqlValue::Text(value.to_string()));
    }
    if field.list {
        let Value::Array(elements) = value else {
            return Err(format!("{value} is no list"));
        };
        let mut out = Vec::with_capacity(elements.len());
        for (i, element) in elements.iter().enumerate() {
            out.push(element_value(element, field).map_err(|err| format!("element {i}: {err}"))?);
        }
        return Ok(SqlValue::Text(Value::Array(out).to_string()));
    }
    if field.point {
        return Err("SQLite has no storage for a location".to_string());
    }
    Ok(match element_value(value, field)? {
        Value::Null => SqlValue::Null,
        Value::Bool(b) => SqlValue::Integer(i64::from(b)),
        Value::Number(number) => match (number.as_i64(), number.as_f64()) {
            (Some(i), _) => SqlValue::Integer(i),
            (None, Some(f)) => SqlValue::Real(f),
            (None, None) => return Err(format!("{number} is no number SQLite holds")),
        },
        Value::String(text) => SqlValue::Text(text),
        other => SqlValue::Text(other.to_string()),
    })
}

/// A value, or an element of a list, as SQLite holds it: a UUID's lowercase
/// hyphenated text, a timestamp in UTC, a duration's canonical text, or the
/// value.
fn element_value(value: &Value, field: &QueueField) -> Result<Value, String> {
    match value {
        Value::String(text) if field.uuid => parse_uuid(text)
            .map(|uuid| Value::String(uuid.hyphenated().to_string()))
            .ok_or_else(|| format!("{text:?} is no UUID")),
        Value::String(text) if field.timestamp => utc_timestamp(text).map(Value::String),
        Value::String(text) if field.interval => {
            Ok(Value::String(format_duration(parse_duration(text)?)))
        }
        Value::Bool(_) => Ok(value.clone()),
        _ if field.boolean => Err(format!("{value} is no boolean")),
        _ => Ok(value.clone()),
    }
}

/// A column's value as the message's member: the JSON a JSON column or a
/// list holds, a boolean, a duration's canonical text, or the value; `None`
/// for NULL.
pub(crate) fn member_value(
    column: ValueRef<'_>,
    field: &QueueField,
) -> Result<Option<Value>, String> {
    let text = |bytes: &[u8]| String::from_utf8(bytes.to_vec()).map_err(|err| err.to_string());
    Ok(Some(match column {
        ValueRef::Null => return Ok(None),
        ValueRef::Text(bytes) if field.json || field.list => serde_json::from_slice(bytes)
            .map_err(|err| format!("the column holds no JSON: {err}"))?,
        _ if field.json || field.list => return Err("the column holds no JSON text".to_string()),
        ValueRef::Integer(i) if field.boolean => Value::Bool(i != 0),
        _ if field.boolean => return Err("the column holds no boolean".to_string()),
        ValueRef::Text(bytes) if field.interval => {
            Value::String(format_duration(parse_duration(&text(bytes)?)?))
        }
        ValueRef::Integer(i) => Value::from(i),
        ValueRef::Real(f) => serde_json::Number::from_f64(f)
            .map(Value::Number)
            .ok_or_else(|| format!("{f} is no JSON number"))?,
        ValueRef::Text(bytes) => Value::String(text(bytes)?),
        ValueRef::Blob(bytes) => Value::Array(bytes.iter().map(|b| Value::from(*b)).collect()),
    }))
}

/// A timestamp's RFC 3339 text in UTC, its fraction kept, as the Go and
/// TypeScript clients write it: `2026-10-11T09:15:00.125+02:00` is
/// `2026-10-11T07:15:00.125Z`.
pub(crate) fn utc_timestamp(text: &str) -> Result<String, String> {
    let refused = || format!("{text:?} is no RFC 3339 timestamp");
    let b = text.as_bytes();
    if b.len() < 20
        || b[4] != b'-'
        || b[7] != b'-'
        || !matches!(b[10], b'T' | b't' | b' ')
        || b[13] != b':'
        || b[16] != b':'
    {
        return Err(refused());
    }
    let number = |range: std::ops::Range<usize>| -> Result<i64, String> {
        let digits = text.get(range).ok_or_else(refused)?;
        if !digits.bytes().all(|c| c.is_ascii_digit()) {
            return Err(refused());
        }
        digits.parse().map_err(|_| refused())
    };
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
    let (hour, minute, second) = (number(11..13)?, number(14..16)?, number(17..19)?);
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
    {
        return Err(refused());
    }
    let mut rest = &text[19..];
    let mut fraction = "";
    if let Some(after) = rest.strip_prefix('.') {
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 {
            return Err(refused());
        }
        fraction = &rest[..=digits];
        rest = &after[digits..];
    }
    let offset = match rest.as_bytes() {
        [b'Z' | b'z'] => 0,
        [sign @ (b'+' | b'-'), h1, h2, b':', m1, m2] | [sign @ (b'+' | b'-'), h1, h2, m1, m2] => {
            let digits = [*h1, *h2, *m1, *m2];
            if !digits.iter().all(u8::is_ascii_digit) {
                return Err(refused());
            }
            let value = |pair: &[u8]| i64::from(pair[0] - b'0') * 10 + i64::from(pair[1] - b'0');
            let minutes = value(&digits[..2]) * 60 + value(&digits[2..]);
            if *sign == b'+' {
                minutes * 60
            } else {
                -minutes * 60
            }
        }
        _ => return Err(refused()),
    };
    let seconds = to_days(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second - offset;
    let (year, month, day) = from_days(seconds.div_euclid(86_400));
    let of_day = seconds.rem_euclid(86_400);
    Ok(format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}{fraction}Z",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn field(marks: &str) -> QueueField {
        QueueField {
            name: "f".into(),
            column: "f".into(),
            json: marks.contains("json"),
            list: marks.contains("list"),
            default: None,
            point: marks.contains("point"),
            interval: marks.contains("interval"),
            uuid: marks.contains("uuid"),
            timestamp: marks.contains("timestamp"),
            boolean: marks.contains("boolean"),
        }
    }

    #[test]
    fn a_timestamp_is_rfc_3339_in_utc_its_fraction_kept() {
        assert_eq!(
            utc_timestamp("2026-10-11T09:15:00.123456+02:00").unwrap(),
            "2026-10-11T07:15:00.123456Z"
        );
        assert_eq!(
            utc_timestamp("2026-10-11T00:15:00-01:30").unwrap(),
            "2026-10-11T01:45:00Z"
        );
        assert_eq!(
            utc_timestamp("2026-12-31T23:30:00-01:00").unwrap(),
            "2027-01-01T00:30:00Z"
        );
        assert_eq!(
            utc_timestamp("2026-10-11T09:15:00.5z").unwrap(),
            "2026-10-11T09:15:00.5Z"
        );
        assert!(utc_timestamp("yesterday").is_err());
        assert!(utc_timestamp("2026-10-11T09:15:00.Z").is_err());
    }

    #[test]
    fn each_mark_converts_its_column_both_ways() {
        let uuid = Value::from("3Nf1lmBDcdDKfsC9ccR4DG");
        assert_eq!(
            column_value(&uuid, &field("uuid")).unwrap(),
            SqlValue::Text("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e".into())
        );
        assert_eq!(
            column_value(&Value::from(vec![uuid]), &field("list uuid")).unwrap(),
            SqlValue::Text(r#"["6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e"]"#.into())
        );
        assert_eq!(
            column_value(&Value::from(true), &field("boolean")).unwrap(),
            SqlValue::Integer(1)
        );
        assert!(column_value(&Value::from("yes"), &field("boolean")).is_err());
        assert_eq!(
            member_value(ValueRef::Integer(0), &field("boolean")).unwrap(),
            Some(Value::from(false))
        );
        assert_eq!(
            column_value(&Value::from("1500ms"), &field("interval")).unwrap(),
            SqlValue::Text("1.5s".into())
        );
        assert_eq!(
            member_value(ValueRef::Text("250µs".as_bytes()), &field("interval")).unwrap(),
            Some(Value::from("250us"))
        );
        assert_eq!(
            column_value(&serde_json::json!({"a": [1]}), &field("json")).unwrap(),
            SqlValue::Text(r#"{"a":[1]}"#.into())
        );
        assert_eq!(
            member_value(ValueRef::Text(br#"{"a":[1]}"#), &field("json")).unwrap(),
            Some(serde_json::json!({"a": [1]}))
        );
        assert_eq!(member_value(ValueRef::Null, &field("")).unwrap(), None);
        assert!(column_value(&serde_json::json!({"lat": 1, "lon": 2}), &field("point")).is_err());
    }
}
