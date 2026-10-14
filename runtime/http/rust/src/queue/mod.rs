//! The queue client of a Rust API (D53, D65; section 8.8 of
//! `docs/stack-model.md`), the twin of the Go ORM's `queues.go` and of the
//! TypeScript runtime's `queue.ts`. There is no Rust ORM, so a DB service's
//! Rust types crate carries each of its queues as a descriptor, a JSON
//! constant in its `queues.rs` that [`QueueDefinition::parse`] reads: the
//! table, the retries, backoff and lease, the column each field of the
//! message writes, and the statements on Postgres, which the generator
//! writes from the one source the Go ORM's and the TypeScript types' come
//! from (`sqlutil.PostgresQueueStatements`). So a message one language
//! enqueues another claims, completes, fails, extends and releases alike.
//!
//! [`enqueue`] writes a message in the caller's transaction, so it commits
//! with the writes that caused it, or not at all. A [`PostgresQueue`]
//! claims, completes, fails, extends and releases messages for a worker,
//! over the pool: a claim first marks dead each message whose last
//! attempt's claim expired, then takes up to n messages that are ready and
//! due, or claimed past their claim's expiry, with `FOR UPDATE SKIP
//! LOCKED`, and gives each a new claim token and one more attempt. Each
//! change but the claim refuses a token that no longer claims the message
//! with [`QueueError::ClaimLost`]. Delivery is at least once, and times are
//! the database's clock.
//!
//! A message crosses to its row through its serde form, as the TypeScript
//! client's crosses through its object: each field of the descriptor is the
//! member of the message's JSON named for it, and its column is written and
//! read as the `codec` module's documentation says. A member a message
//! leaves out or sets to null takes its column's default, and an enum
//! field with a schema default writes that default, as the Go ORM writes
//! them; a NULL column leaves its member out of the message.

mod codec;

use std::borrow::Cow;
use std::fmt;
use std::marker::PhantomData;
use std::sync::Arc;
use std::time::Duration;

use deadpool_postgres::GenericClient;
use serde::de::DeserializeOwned;
use serde::{Deserialize, Serialize};
use serde_json::{Map, Value};
use tokio_postgres::types::ToSql;
use tokio_postgres::Row;

use crate::postgres::{chain, Pool};
use codec::{is_json, param_text, Column, Text};

/// What a refused change says of a token that no longer claims its
/// message, as Go's `ErrClaimLost` and TypeScript's `ClaimLostError` say it.
pub const CLAIM_LOST: &str =
    "the claim on the message is lost: it expired, and another worker may hold the message";

/// What a queue refused or could not do. Each message names the queue's
/// table, and for a change the message's ID, as Go's and TypeScript's do.
#[derive(Clone, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum QueueError {
    /// A descriptor that does not parse, or that a queue cannot run.
    Definition(String),
    /// The token no longer claims the message: its claim expired, and
    /// another worker may hold it. Complete, fail, extend and release
    /// return it, as Go's return `ErrClaimLost`.
    ClaimLost(String),
    /// The database failed or refused a statement, or a message could not
    /// be written to its row or read from it. The message holds the chain
    /// of causes.
    Failed(String),
}

impl QueueError {
    /// Whether the token no longer claims the message.
    pub fn is_claim_lost(&self) -> bool {
        matches!(self, QueueError::ClaimLost(_))
    }

    fn failed(context: &str, err: &(dyn std::error::Error + 'static)) -> Self {
        QueueError::Failed(format!("{context}: {}", chain(err)))
    }
}

impl fmt::Display for QueueError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            QueueError::Definition(message)
            | QueueError::ClaimLost(message)
            | QueueError::Failed(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for QueueError {}

/// A field of a queue's message and the column it writes, in the
/// descriptor's member names, TypeScript's `QueueField`.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct QueueField {
    /// The message's member: the field's name in the schema, which its
    /// Rust type serializes it as.
    pub name: String,
    /// Its column, quoted for SQL where it must be.
    pub column: String,
    /// The column holds the value as JSON.
    #[serde(default)]
    pub json: bool,
    /// The column holds a list as a Postgres array.
    #[serde(default)]
    pub list: bool,
    /// The schema's default of an enum, which a message that leaves the
    /// member unset writes.
    #[serde(default)]
    pub default: Option<String>,
}

/// A queue's statements on Postgres, TypeScript's `QueueStatements`:
/// `insert` begins an enqueue; `expire` marks dead each message whose last
/// attempt's claim expired (`$1` the retries); `claim` claims up to `$1` due
/// messages for `$2` seconds; `complete`, `fail`, `extend` and `release`
/// change message `$1` while token `$2` claims it.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
pub struct QueueStatements {
    pub insert: String,
    pub expire: String,
    pub claim: String,
    pub complete: String,
    pub fail: String,
    pub extend: String,
    pub release: String,
}

/// A queue of a database, as its Rust types crate's `queues.rs` describes
/// it, in TypeScript's `QueueDefinition`'s member names.
#[derive(Clone, Debug, PartialEq, Eq, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct QueueDefinition {
    /// The `@queue` class.
    pub name: String,
    /// Its table, quoted for SQL where it must be.
    pub table: String,
    /// How many times a message whose handler fails is handled again
    /// before it is dead.
    pub retries: u32,
    /// How long a failed message waits before it is due again.
    pub backoff_seconds: u64,
    /// How long a claim holds a message unless its worker extends it.
    pub lease_seconds: u64,
    /// The message's fields, in the order a claim returns their columns.
    pub fields: Vec<QueueField>,
    pub statements: QueueStatements,
}

impl QueueDefinition {
    /// Reads a descriptor: the JSON a Rust types crate's `queues.rs` holds.
    /// It refuses one that is not a descriptor, or whose queue cannot run:
    /// no name, table or statement, a field without its name or column, or
    /// a lease of zero.
    pub fn parse(json: &str) -> Result<Self, QueueError> {
        let definition: QueueDefinition = serde_json::from_str(json)
            .map_err(|err| QueueError::Definition(format!("a queue descriptor: {err}")))?;
        definition.check()?;
        Ok(definition)
    }

    fn check(&self) -> Result<(), QueueError> {
        let refuse = |problem: String| {
            Err(QueueError::Definition(format!(
                "the descriptor of queue {}: {problem}",
                if self.name.is_empty() {
                    "<unnamed>"
                } else {
                    &self.name
                }
            )))
        };
        if self.name.is_empty() {
            return refuse("it has no name".to_string());
        }
        if self.table.is_empty() {
            return refuse("it has no table".to_string());
        }
        if self.lease_seconds == 0 {
            return refuse("its lease is 0 s; it is positive".to_string());
        }
        for (i, field) in self.fields.iter().enumerate() {
            if field.name.is_empty() || field.column.is_empty() {
                return refuse(format!("field {i} lacks its name or its column"));
            }
        }
        let s = &self.statements;
        let statements = [
            ("insert", &s.insert),
            ("expire", &s.expire),
            ("claim", &s.claim),
            ("complete", &s.complete),
            ("fail", &s.fail),
            ("extend", &s.extend),
            ("release", &s.release),
        ];
        for (name, sql) in statements {
            if sql.trim().is_empty() {
                return refuse(format!("its {name} statement is empty"));
            }
        }
        Ok(())
    }

    /// How long a claim holds a message unless extended.
    pub fn lease(&self) -> Duration {
        Duration::from_secs(self.lease_seconds)
    }

    /// How long a failed message waits before it is due again.
    pub fn backoff(&self) -> Duration {
        Duration::from_secs(self.backoff_seconds)
    }
}

/// A claimed message: its ID, the token of the claim, which attempt this
/// is, from 1, and the message.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Claim<M> {
    pub id: String,
    pub token: String,
    pub attempt: u32,
    pub message: M,
}

/// Whether a message leaves a member unset: absent or null, or an enum
/// with a default set to the empty string, as Go reads its zero value.
fn unset(value: Option<&Value>, field: &QueueField) -> bool {
    match value {
        None | Some(Value::Null) => true,
        Some(Value::String(text)) => field.default.is_some() && text.is_empty(),
        Some(_) => false,
    }
}

/// Writes `message` into `queue` in the transaction `client` runs, the
/// caller's: the message is due once the transaction commits, and never if
/// it rolls back. `client` is a deadpool `Transaction` (or a pool's
/// `Object` outside one, which commits the message alone). Each member the
/// message sets writes its column; one it leaves unset takes the column's
/// default, and an enum with a schema default writes that default. The
/// statement is prepared once per connection, in the pool's statement
/// cache.
pub async fn enqueue<C, M>(
    client: &C,
    queue: &QueueDefinition,
    message: &M,
) -> Result<(), QueueError>
where
    C: GenericClient,
    M: Serialize + ?Sized,
{
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
    let mut fields: Vec<(&QueueField, Cow<'_, Value>)> = Vec::new();
    for field in &queue.fields {
        match members.get(&field.name) {
            Some(value) if !unset(Some(value), field) => fields.push((field, Cow::Borrowed(value))),
            _ => {
                if let Some(default) = &field.default {
                    fields.push((field, Cow::Owned(Value::String(default.clone()))));
                }
            }
        }
    }
    let sql = if fields.is_empty() {
        format!("{} DEFAULT VALUES", queue.statements.insert)
    } else {
        let columns: Vec<&str> = fields
            .iter()
            .map(|(field, _)| field.column.as_str())
            .collect();
        let placeholders: Vec<String> = (1..=fields.len()).map(|i| format!("${i}")).collect();
        format!(
            "{} ({}) VALUES ({})",
            queue.statements.insert,
            columns.join(", "),
            placeholders.join(", ")
        )
    };
    let statement = client
        .prepare_cached(&sql)
        .await
        .map_err(|err| QueueError::failed(&context(), &err))?;
    if statement.params().len() != fields.len() {
        return Err(QueueError::Failed(format!(
            "{}: the insert takes {} values, and the message sets {}",
            context(),
            statement.params().len(),
            fields.len()
        )));
    }
    let mut params = Vec::with_capacity(fields.len());
    for ((field, value), ty) in fields.iter().zip(statement.params()) {
        let text = param_text(value, ty, field.json).map_err(|err| {
            QueueError::Failed(format!("{}: field {}: {err}", context(), field.name))
        })?;
        params.push(Text(text));
    }
    let refs: Vec<&(dyn ToSql + Sync)> = params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
    client
        .execute(&statement, &refs)
        .await
        .map_err(|err| QueueError::failed(&context(), &err))?;
    Ok(())
}

/// The claims of a queue over a pool, which a worker makes, its messages
/// read as `M`. Each runs the descriptor's statement on a connection of the
/// pool, prepared once per connection.
pub struct PostgresQueue<M> {
    pool: Pool,
    definition: Arc<QueueDefinition>,
    message: PhantomData<fn() -> M>,
}

impl<M> Clone for PostgresQueue<M> {
    fn clone(&self) -> Self {
        Self {
            pool: self.pool.clone(),
            definition: Arc::clone(&self.definition),
            message: PhantomData,
        }
    }
}

impl<M> fmt::Debug for PostgresQueue<M> {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("PostgresQueue")
            .field("name", &self.definition.name)
            .field("table", &self.definition.table)
            .finish_non_exhaustive()
    }
}

impl<M: DeserializeOwned> PostgresQueue<M> {
    /// The claims of the queue `definition` describes, over `pool`.
    pub fn new(pool: Pool, definition: QueueDefinition) -> Self {
        Self {
            pool,
            definition: Arc::new(definition),
            message: PhantomData,
        }
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

    /// Marks dead each message whose last attempt's claim expired, then
    /// claims up to `n` due messages for `lease`, and reads each. A message
    /// that does not read as `M` fails the claim, as a row Go's ORM cannot
    /// scan does.
    pub async fn claim(&self, n: usize, lease: Duration) -> Result<Vec<Claim<M>>, QueueError> {
        if n == 0 {
            return Ok(Vec::new());
        }
        let context = format!("claim on {}", self.definition.table);
        let client = self.client(&context).await?;
        let statements = &self.definition.statements;
        let expire = client
            .prepare_cached(&statements.expire)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        client
            .execute(&expire, &[&Text::of(self.definition.retries)])
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        let claim = client
            .prepare_cached(&statements.claim)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        let rows = client
            .query(&claim, &[&Text::of(n), &Text::of(lease.as_secs_f64())])
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        rows.iter().map(|row| self.claim_of(row)).collect()
    }

    /// A claimed row, `[id, token, attempt, ...the message's columns]`, as
    /// its claim.
    fn claim_of(&self, row: &Row) -> Result<Claim<M>, QueueError> {
        let table = &self.definition.table;
        let read =
            |err: tokio_postgres::Error| QueueError::failed(&format!("claim on {table}"), &err);
        let id: String = row.try_get(0).map_err(read)?;
        let token: String = row.try_get(1).map_err(read)?;
        let attempt: i32 = row.try_get(2).map_err(read)?;
        let message_error =
            |what: String| QueueError::Failed(format!("message {id} of {table}: {what}"));
        let mut members = Map::new();
        for (i, field) in self.definition.fields.iter().enumerate() {
            let column: Option<Column> = row
                .try_get(3 + i)
                .map_err(|err| message_error(format!("field {}: {}", field.name, chain(&err))))?;
            let Some(Column(mut value)) = column else {
                continue;
            };
            // A JSON field in a column that is not JSON holds its JSON text.
            if field.json && !is_json(row.columns()[3 + i].type_()) {
                if let Value::String(text) = &value {
                    value = serde_json::from_str(text).map_err(|err| {
                        message_error(format!("field {} holds no JSON: {err}", field.name))
                    })?;
                }
            }
            if !value.is_null() {
                members.insert(field.name.clone(), value);
            }
        }
        let message = serde_json::from_value(Value::Object(members))
            .map_err(|err| message_error(err.to_string()))?;
        Ok(Claim {
            attempt: u32::try_from(attempt).unwrap_or(0),
            id,
            token,
            message,
        })
    }

    /// Marks the message `id`, which `token` claims, done.
    pub async fn complete(&self, id: &str, token: &str) -> Result<(), QueueError> {
        let sql = &self.definition.statements.complete;
        self.update("complete", sql, id, token, None).await
    }

    /// Records `cause` on the message `id`, which `token` claims, and makes
    /// it due again after the backoff, or dead when its retries are spent.
    /// Returns whether the message is dead.
    pub async fn fail(&self, id: &str, token: &str, cause: &str) -> Result<bool, QueueError> {
        let context = format!("fail message {id} on {}", self.definition.table);
        let client = self.client(&context).await?;
        let statement = client
            .prepare_cached(&self.definition.statements.fail)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        let params = [
            Text::of(id),
            Text::of(token),
            Text::of(self.definition.retries),
            Text::of(self.definition.backoff_seconds),
            Text::of(cause),
        ];
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        let row = client
            .query_opt(&statement, &refs)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        let Some(row) = row else {
            return Err(QueueError::ClaimLost(format!("{context}: {CLAIM_LOST}")));
        };
        let state: String = row
            .try_get(0)
            .map_err(|err| QueueError::failed(&context, &err))?;
        Ok(state == "dead")
    }

    /// Holds the message `id`, which `token` claims, for `lease` from now.
    pub async fn extend(&self, id: &str, token: &str, lease: Duration) -> Result<(), QueueError> {
        let sql = &self.definition.statements.extend;
        self.update(
            "extend",
            sql,
            id,
            token,
            Some(Text::of(lease.as_secs_f64())),
        )
        .await
    }

    /// Gives the message `id`, which `token` claims, back, due at once, its
    /// attempt not counted: its handler did not finish, and did not fail.
    pub async fn release(&self, id: &str, token: &str) -> Result<(), QueueError> {
        let sql = &self.definition.statements.release;
        self.update("release", sql, id, token, None).await
    }

    /// A connection of the pool.
    async fn client(&self, context: &str) -> Result<deadpool_postgres::Object, QueueError> {
        self.pool
            .get()
            .await
            .map_err(|err| QueueError::failed(context, &err))
    }

    /// Runs `sql`, which changes the message `id` while `token` claims it,
    /// and refuses a change of no row as a lost claim.
    async fn update(
        &self,
        what: &str,
        sql: &str,
        id: &str,
        token: &str,
        extra: Option<Text>,
    ) -> Result<(), QueueError> {
        let context = format!("{what} message {id} on {}", self.definition.table);
        let client = self.client(&context).await?;
        let statement = client
            .prepare_cached(sql)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        let params: Vec<Text> = [Text::of(id), Text::of(token)]
            .into_iter()
            .chain(extra)
            .collect();
        let refs: Vec<&(dyn ToSql + Sync)> =
            params.iter().map(|p| p as &(dyn ToSql + Sync)).collect();
        let changed = client
            .execute(&statement, &refs)
            .await
            .map_err(|err| QueueError::failed(&context, &err))?;
        if changed == 0 {
            return Err(QueueError::ClaimLost(format!("{context}: {CLAIM_LOST}")));
        }
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const DESCRIPTOR: &str = r#"{
        "name": "Ping",
        "table": "ping_queue",
        "retries": 5,
        "backoffSeconds": 30,
        "leaseSeconds": 60,
        "fields": [
            { "name": "sentAt", "column": "sent_at" },
            { "name": "priority", "column": "priority", "default": "low" },
            { "name": "shipTo", "column": "ship_to", "json": true },
            { "name": "tags", "column": "tags", "list": true }
        ],
        "statements": {
            "insert": "INSERT INTO ping_queue",
            "expire": "UPDATE ping_queue ...",
            "claim": "WITH due AS (...) UPDATE ...",
            "complete": "UPDATE ... complete",
            "fail": "UPDATE ... fail",
            "extend": "UPDATE ... extend",
            "release": "UPDATE ... release"
        }
    }"#;

    #[test]
    fn a_descriptor_reads_in_typescripts_member_names() {
        let def = QueueDefinition::parse(DESCRIPTOR).unwrap();
        assert_eq!(def.name, "Ping");
        assert_eq!(def.table, "ping_queue");
        assert_eq!(
            (def.retries, def.backoff(), def.lease()),
            (5, Duration::from_secs(30), Duration::from_secs(60))
        );
        assert_eq!(
            def.fields,
            [
                QueueField {
                    name: "sentAt".into(),
                    column: "sent_at".into(),
                    json: false,
                    list: false,
                    default: None
                },
                QueueField {
                    name: "priority".into(),
                    column: "priority".into(),
                    json: false,
                    list: false,
                    default: Some("low".into())
                },
                QueueField {
                    name: "shipTo".into(),
                    column: "ship_to".into(),
                    json: true,
                    list: false,
                    default: None
                },
                QueueField {
                    name: "tags".into(),
                    column: "tags".into(),
                    json: false,
                    list: true,
                    default: None
                },
            ]
        );
        assert_eq!(def.statements.insert, "INSERT INTO ping_queue");
    }

    #[test]
    fn a_descriptor_a_queue_cannot_run_is_refused() {
        let edit = |from: &str, to: &str| {
            QueueDefinition::parse(&DESCRIPTOR.replace(from, to)).unwrap_err()
        };
        assert_eq!(
            edit(r#""leaseSeconds": 60"#, r#""leaseSeconds": 0"#).to_string(),
            "the descriptor of queue Ping: its lease is 0 s; it is positive"
        );
        assert_eq!(
            edit(r#""release": "UPDATE ... release""#, r#""release": " ""#).to_string(),
            "the descriptor of queue Ping: its release statement is empty"
        );
        assert_eq!(
            edit(r#""column": "sent_at""#, r#""column": """#).to_string(),
            "the descriptor of queue Ping: field 0 lacks its name or its column"
        );
        assert!(matches!(
            edit(r#""retries": 5"#, r#""retries": -1"#),
            QueueError::Definition(_)
        ));
        let missing = edit(r#""table": "ping_queue","#, "");
        assert!(
            missing
                .to_string()
                .starts_with("a queue descriptor: missing field `table`"),
            "{missing}"
        );
        assert!(!missing.is_claim_lost());
        assert!(QueueError::ClaimLost(CLAIM_LOST.into()).is_claim_lost());
    }

    #[test]
    fn a_member_is_unset_when_absent_null_or_an_empty_enum_with_a_default() {
        let def = QueueDefinition::parse(DESCRIPTOR).unwrap();
        let (plain, with_default) = (&def.fields[0], &def.fields[1]);
        assert!(unset(None, plain));
        assert!(unset(Some(&Value::Null), plain));
        assert!(!unset(Some(&Value::from("")), plain));
        assert!(unset(Some(&Value::from("")), with_default));
        assert!(!unset(Some(&Value::from("high")), with_default));
        assert!(!unset(Some(&Value::from(false)), plain));
    }
}
