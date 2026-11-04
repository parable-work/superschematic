//! The adapter through its public functions on a real database of each
//! dialect, rusqlite and Postgres (a transaction per command, committed
//! when it applies and rolled back when it is refused), on tables written
//! by hand as sqlgen writes one that composes Workflow on `status`: the
//! owned columns `status` and `status_entered_at` beside the own `title`,
//! and `@optimistic`'s `_version` with its trigger. The cases of the HTTP
//! runtime's `src/rules-store.test.ts`; the Postgres ones are skipped,
//! saying so, without `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`. What needs a
//! row moved between a command's read and its apply is the crate's own
//! tests', over a store that moves it.

#![cfg(any(feature = "sqlite", feature = "postgres"))]

mod common;

use serde_json::{json, Map, Value};
use superschematic_rules::Code;
use superschematic_rules_adapter::{
    Caller, ChangeRun, Claim, CreateOptions, Deadline, DeadlineRun, Error, ExecuteOptions, Host,
    Layout, Owned, Refusal, Row, SubRow, TableChange,
};

use common::{iso, p, Param};

/// A database of a dialect, holding the tables of `common::store_ddl`.
enum Db {
    #[cfg(feature = "sqlite")]
    Sqlite(rusqlite::Connection),
    #[cfg(feature = "postgres")]
    Postgres(tokio_postgres::Client),
}

/// A message of a change queue as the trigger wrote it (D103): its id, the
/// row's key, the write, its time, its images' JSON text, and its state.
#[derive(Debug)]
struct Written {
    id: String,
    row_key: String,
    write: String,
    at: i64,
    before: Option<String>,
    after: Option<String>,
    state: String,
}

impl Written {
    /// The message as a worker hands it to the adapter: its images the
    /// JSON Postgres's `jsonb` holds, or SQLite's text as it is.
    fn change(&self, postgres: bool) -> TableChange {
        let image = |text: &Option<String>| match text {
            None => Value::Null,
            Some(text) if postgres => serde_json::from_str(text).expect("an image of JSON"),
            Some(text) => Value::String(text.clone()),
        };
        TableChange {
            key: self.row_key.clone(),
            write: self.write.clone(),
            at: self.at,
            before: image(&self.before),
            after: image(&self.after),
        }
    }
}

/// A message of a queue as a release leaves it: its state, its attempts,
/// whether a claim's token is on it, and how many seconds from the
/// database's now it falls due.
#[derive(Debug)]
struct Queued {
    state: String,
    attempts: i64,
    token: bool,
    due_in: f64,
}

/// A row of Order's shape as the database holds it, its timestamp as ISO
/// text.
#[derive(Debug, PartialEq)]
struct Stored {
    title: String,
    status: Option<String>,
    entered_at: Option<String>,
    version: i64,
}

/// Runs a call of the adapter's in a transaction of its own on a
/// connection, committing it when the call returns Ok and rolling it back
/// otherwise; the call is the dialect's function applied to the host and
/// the transaction.
macro_rules! in_transaction {
    ($db:expr, |$host_tx:ident| $sqlite:expr, $postgres:expr) => {
        match $db {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let $host_tx = conn.transaction().expect("a transaction");
                let outcome = $sqlite;
                if outcome.is_ok() {
                    $host_tx.commit().expect("the transaction commits");
                }
                outcome
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let $host_tx = client.transaction().await.expect("a transaction");
                let outcome = $postgres;
                if outcome.is_ok() {
                    $host_tx.commit().await.expect("the transaction commits");
                } else {
                    $host_tx
                        .rollback()
                        .await
                        .expect("the transaction rolls back");
                }
                outcome
            }
        }
    };
}

impl Db {
    fn postgres(&self) -> bool {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(_) => false,
            #[cfg(feature = "postgres")]
            Db::Postgres(_) => true,
        }
    }

    async fn exec(&mut self, text: &str, values: &[Param]) -> u64 {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                conn.execute(
                    text,
                    rusqlite::params_from_iter(common::sqlite_params(values)),
                )
                .unwrap_or_else(|error| panic!("{text}: {error}")) as u64
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let texts = common::pg::texts(values);
                client
                    .execute(text, &common::pg::refs(&texts))
                    .await
                    .unwrap_or_else(|error| panic!("{text}: {error}"))
            }
        }
    }

    /// The ids a table holds, in order.
    async fn ids(&mut self, table: &str) -> Vec<String> {
        let text = format!("SELECT id FROM \"{table}\" ORDER BY id");
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let ids = statement
                    .query_map([], |row| row.get(0))
                    .expect("the read runs");
                ids.collect::<Result<_, _>>().expect("the ids read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query(&text, &[])
                .await
                .expect("the read runs")
                .iter()
                .map(|row| row.get(0))
                .collect(),
        }
    }

    /// The row as the table holds it.
    async fn stored(&mut self, table: &str, id: &str) -> Option<Stored> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let text = format!("SELECT title, status, status_entered_at, _version FROM \"{table}\" WHERE id = ?1");
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let mut rows = statement.query([id]).expect("the read runs");
                let row = rows.next().expect("a row reads")?;
                Some(Stored {
                    title: row.get(0).expect("title"),
                    status: row.get(1).expect("status"),
                    entered_at: row.get(2).expect("status_entered_at"),
                    version: row.get(3).expect("_version"),
                })
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let text = format!(
                    "SELECT title, status, to_char(status_entered_at AT TIME ZONE 'UTC', 'YYYY-MM-DD\"T\"HH24:MI:SS.MS\"Z\"'), _version FROM \"{table}\" WHERE id = $1"
                );
                let row = client
                    .query_opt(&text, &[&id])
                    .await
                    .expect("the read runs")?;
                Some(Stored {
                    title: row.get(0),
                    status: row.get(1),
                    entered_at: row.get(2),
                    version: row.get(3),
                })
            }
        }
    }

    async fn create(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        own: Value,
        id: Option<&str>,
    ) -> Result<String, Error> {
        let options = CreateOptions {
            id: id.map(str::to_owned),
            ..CreateOptions::default()
        };
        self.create_with(host, type_name, own, &options).await
    }

    /// A create as alice with what it takes beside its own fields.
    async fn create_with(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        own: Value,
        options: &CreateOptions,
    ) -> Result<String, Error> {
        let own = object(own);
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_create(
                host,
                &tx,
                type_name,
                own,
                alice(),
                options
            ),
            superschematic_rules_adapter::postgres::run_create(
                host,
                &tx,
                type_name,
                own,
                alice(),
                options
            )
            .await
        )
    }

    /// Runs an operation of a use on a row as a caller.
    #[allow(clippy::too_many_arguments)]
    async fn operate(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        use_id: &str,
        operation: &str,
        params: Value,
        caller: Caller,
    ) -> Result<Value, Error> {
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_operation(
                host, &tx, type_name, key, use_id, operation, params, caller
            ),
            superschematic_rules_adapter::postgres::run_operation(
                host, &tx, type_name, key, use_id, operation, params, caller
            )
            .await
        )
    }

    /// An update of a row's own fields as a caller.
    async fn update_as(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        patch: Value,
        caller: Caller,
    ) -> Result<(), Error> {
        let patch = object(patch);
        let options = ExecuteOptions::default();
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_update(
                host, &tx, type_name, key, patch, caller, &options
            ),
            superschematic_rules_adapter::postgres::run_update(
                host, &tx, type_name, key, patch, caller, &options
            )
            .await
        )
    }

    async fn transition(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        to: &str,
        caller: Caller,
    ) -> Result<Value, Error> {
        let params = json!({ "to": to });
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_operation(
                host,
                &tx,
                type_name,
                key,
                "status",
                "transition",
                params,
                caller
            ),
            superschematic_rules_adapter::postgres::run_operation(
                host,
                &tx,
                type_name,
                key,
                "status",
                "transition",
                params,
                caller
            )
            .await
        )
    }

    async fn update(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        patch: Value,
    ) -> Result<(), Error> {
        let patch = object(patch);
        let options = ExecuteOptions::default();
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_update(
                host,
                &tx,
                type_name,
                key,
                patch,
                alice(),
                &options
            ),
            superschematic_rules_adapter::postgres::run_update(
                host,
                &tx,
                type_name,
                key,
                patch,
                alice(),
                &options
            )
            .await
        )
    }

    async fn delete(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        soft: Option<Value>,
    ) -> Result<(), Error> {
        let options = ExecuteOptions {
            own_columns: None,
            soft_delete: soft.map(object),
        };
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_delete(
                host,
                &tx,
                type_name,
                key,
                alice(),
                &options
            ),
            superschematic_rules_adapter::postgres::run_delete(
                host,
                &tx,
                type_name,
                key,
                alice(),
                &options
            )
            .await
        )
    }

    async fn read(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
    ) -> Result<Option<Row>, Error> {
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::read_row(host, &tx, type_name, key),
            superschematic_rules_adapter::postgres::read_row(host, &tx, type_name, key).await
        )
    }

    async fn prepare(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        own: Value,
    ) -> Result<Owned, Error> {
        let own = object(own);
        let options = CreateOptions::default();
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::prepare_create(
                host,
                &tx,
                type_name,
                own,
                alice(),
                &options
            ),
            superschematic_rules_adapter::postgres::prepare_create(
                host,
                &tx,
                type_name,
                own,
                alice(),
                &options
            )
            .await
        )
    }

    /// Runs a due deadline in a transaction of its own, with a claim or
    /// none.
    async fn run_deadline(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        deadline: &Deadline,
        claim: Option<&Claim>,
    ) -> Result<DeadlineRun, Error> {
        let worker = || Caller::new("stock-api/Expiry", Vec::<String>::new());
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_deadline(
                host,
                &tx,
                type_name,
                deadline,
                worker(),
                claim
            ),
            superschematic_rules_adapter::postgres::run_deadline(
                host,
                &tx,
                type_name,
                deadline,
                worker(),
                claim
            )
            .await
        )
    }

    /// Claims a row's messages of a queue as a worker's claim does, and
    /// answers the claim of the first.
    async fn claim(&mut self, queue: &str, row_key: &str) -> Claim {
        let postgres = self.postgres();
        let (token, cast) = if postgres {
            ("gen_random_uuid()", "::text")
        } else {
            ("lower(hex(randomblob(16)))", "")
        };
        let text = format!(
            "UPDATE \"{queue}\" SET state = 'claimed', attempts = attempts + 1, claim_token = {token} WHERE row_key = {} AND state = 'ready' RETURNING id{cast}, claim_token{cast}",
            p(postgres, 1)
        );
        let (id, token): (String, String) = match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => conn
                .query_row(&text, [row_key], |row| Ok((row.get(0)?, row.get(1)?)))
                .expect("a message is claimed"),
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let row = client
                    .query_one(&text, &[&row_key])
                    .await
                    .expect("a message is claimed");
                (row.get(0), row.get(1))
            }
        };
        Claim { id, token }
    }

    /// A row's messages of a queue: each its name, its time and its state,
    /// by time.
    async fn messages(&mut self, queue: &str, row_key: &str) -> Vec<(String, i64, String)> {
        let text = format!(
            "SELECT name, at, state FROM \"{queue}\" WHERE row_key = {} ORDER BY at",
            p(self.postgres(), 1)
        );
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let rows = statement
                    .query_map([row_key], |row| Ok((row.get(0)?, row.get(1)?, row.get(2)?)))
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query(&text, &[&row_key])
                .await
                .expect("the read runs")
                .iter()
                .map(|row| (row.get(0), row.get(1), row.get(2)))
                .collect(),
        }
    }

    /// The writes a row's messages of a change queue's table name, sorted:
    /// what the table's change trigger heard of it.
    async fn heard(&mut self, table: &str, row_key: &str) -> Vec<String> {
        let text = format!(
            "SELECT \"write\" FROM \"{table}\" WHERE row_key = {} ORDER BY \"write\"",
            p(self.postgres(), 1)
        );
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let rows = statement
                    .query_map([row_key], |row| row.get(0))
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query(&text, &[&row_key])
                .await
                .expect("the read runs")
                .iter()
                .map(|row| row.get(0))
                .collect(),
        }
    }

    /// A row's rows of a use's sub-table, read in a transaction of its own.
    async fn sub_rows(
        &mut self,
        host: &Host<'_>,
        type_name: &str,
        key: &str,
        use_id: &str,
        table: &str,
    ) -> Vec<SubRow> {
        let read: Result<Vec<SubRow>, Error> = in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::read_sub_rows(
                host, &tx, type_name, key, use_id, table
            ),
            superschematic_rules_adapter::postgres::read_sub_rows(
                host, &tx, type_name, key, use_id, table
            )
            .await
        );
        read.expect("the sub-table rows read")
    }

    /// A row's messages of Probe's change queue, in the order they fall
    /// due.
    async fn written(&mut self, row_key: &str) -> Vec<Written> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let text = "SELECT id, row_key, \"write\", at, \"before\", \"after\", state FROM probe_change_queue WHERE row_key = ?1 ORDER BY due_at, at, rowid";
                let mut statement = conn.prepare(text).expect("the read prepares");
                let rows = statement
                    .query_map([row_key], |row| {
                        Ok(Written {
                            id: row.get(0)?,
                            row_key: row.get(1)?,
                            write: row.get(2)?,
                            at: row.get(3)?,
                            before: row.get(4)?,
                            after: row.get(5)?,
                            state: row.get(6)?,
                        })
                    })
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let text = "SELECT id::text, row_key, \"write\", at, \"before\"::text, \"after\"::text, state FROM probe_change_queue WHERE row_key = $1 ORDER BY due_at, at";
                client
                    .query(text, &[&row_key])
                    .await
                    .expect("the read runs")
                    .iter()
                    .map(|row| Written {
                        id: row.get(0),
                        row_key: row.get(1),
                        write: row.get(2),
                        at: row.get(3),
                        before: row.get(4),
                        after: row.get(5),
                        state: row.get(6),
                    })
                    .collect()
            }
        }
    }

    /// Claims a message of Probe's change queue by its id, as a worker's
    /// claim does, and answers the claim.
    async fn claim_change(&mut self, id: &str) -> Claim {
        let postgres = self.postgres();
        let (token, cast) = if postgres {
            ("gen_random_uuid()", "::text")
        } else {
            ("lower(hex(randomblob(16)))", "")
        };
        let text = format!(
            "UPDATE probe_change_queue SET state = 'claimed', attempts = attempts + 1, claim_token = {token} WHERE id{cast} = {} AND state = 'ready' RETURNING id{cast}, claim_token{cast}",
            p(postgres, 1)
        );
        let (id, token): (String, String) = match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => conn
                .query_row(&text, [id], |row| Ok((row.get(0)?, row.get(1)?)))
                .expect("the message is claimed"),
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let row = client
                    .query_one(&text, &[&id])
                    .await
                    .expect("the message is claimed");
                (row.get(0), row.get(1))
            }
        };
        Claim { id, token }
    }

    /// A message's state and whether a claim's token is on it, by its id.
    async fn change_state(&mut self, id: &str) -> Option<(String, bool)> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => conn
                .query_row(
                    "SELECT state, claim_token IS NOT NULL FROM probe_change_queue WHERE id = ?1",
                    [id],
                    |row| Ok((row.get(0)?, row.get(1)?)),
                )
                .ok(),
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => client
                .query_opt(
                    "SELECT state, claim_token IS NOT NULL FROM probe_change_queue WHERE id::text = $1",
                    &[&id],
                )
                .await
                .expect("the read runs")
                .map(|row| (row.get(0), row.get(1))),
        }
    }

    /// Runs a change in a transaction of its own, with a claim or none, as
    /// a behavior worker of Probe.
    async fn run_change(
        &mut self,
        host: &Host<'_>,
        change: &TableChange,
        claim: Option<&Claim>,
    ) -> Result<ChangeRun, Error> {
        let worker = || Caller::new("probe-api/ProbeWorker", Vec::<String>::new());
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_change(
                host,
                &tx,
                "Probe",
                change,
                worker(),
                claim
            ),
            superschematic_rules_adapter::postgres::run_change(
                host,
                &tx,
                "Probe",
                change,
                worker(),
                claim
            )
            .await
        )
    }

    /// A row's messages of a queue as a release leaves them: each its
    /// state, its attempts, whether it holds a claim's token, and how many
    /// seconds from the database's now it falls due.
    async fn queued(&mut self, queue: &str, row_key: &str) -> Vec<Queued> {
        match self {
            #[cfg(feature = "sqlite")]
            Db::Sqlite(conn) => {
                let text = format!(
                    "SELECT state, attempts, claim_token IS NOT NULL, (julianday(due_at) - julianday('now')) * 86400.0 FROM \"{queue}\" WHERE row_key = ?1"
                );
                let mut statement = conn.prepare(&text).expect("the read prepares");
                let rows = statement
                    .query_map([row_key], |row| {
                        Ok(Queued {
                            state: row.get(0)?,
                            attempts: row.get(1)?,
                            token: row.get(2)?,
                            due_in: row.get(3)?,
                        })
                    })
                    .expect("the read runs");
                rows.collect::<Result<_, _>>().expect("the messages read")
            }
            #[cfg(feature = "postgres")]
            Db::Postgres(client) => {
                let text = format!(
                    "SELECT state, attempts::bigint, claim_token IS NOT NULL, extract(epoch FROM due_at - now())::float8 FROM \"{queue}\" WHERE row_key = $1"
                );
                client
                    .query(&text, &[&row_key])
                    .await
                    .expect("the read runs")
                    .iter()
                    .map(|row| Queued {
                        state: row.get(0),
                        attempts: row.get(1),
                        token: row.get(2),
                        due_in: row.get(3),
                    })
                    .collect()
            }
        }
    }

    /// A transition the core vetoes, then one it takes, in one transaction:
    /// the first's failure leaves the caller's transaction going on.
    async fn vetoed_then_moved(&mut self, host: &Host<'_>, key: &str) -> Result<Value, Error> {
        let (veto, move_on) = (json!({"to": "shipped"}), json!({"to": "paid"}));
        in_transaction!(
            self,
            |tx| {
                let vetoed = superschematic_rules_adapter::sqlite::run_operation(
                    host,
                    &tx,
                    "Order",
                    key,
                    "status",
                    "transition",
                    veto,
                    alice(),
                );
                assert_eq!(code(vetoed), Code::Vetoed);
                superschematic_rules_adapter::sqlite::run_operation(
                    host,
                    &tx,
                    "Order",
                    key,
                    "status",
                    "transition",
                    move_on,
                    alice(),
                )
            },
            {
                let vetoed = superschematic_rules_adapter::postgres::run_operation(
                    host,
                    &tx,
                    "Order",
                    key,
                    "status",
                    "transition",
                    veto,
                    alice(),
                )
                .await;
                assert_eq!(code(vetoed), Code::Vetoed);
                superschematic_rules_adapter::postgres::run_operation(
                    host,
                    &tx,
                    "Order",
                    key,
                    "status",
                    "transition",
                    move_on,
                    alice(),
                )
                .await
            }
        )
    }
}

fn object(value: Value) -> Map<String, Value> {
    value.as_object().cloned().unwrap_or_default()
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn shipper() -> Caller {
    Caller::new("sam", ["orders.ship"])
}

fn refusal<T: std::fmt::Debug>(result: Result<T, Error>) -> Refusal {
    match result {
        Err(Error::Refused(refusal)) => *refusal,
        other => panic!("not refused: {other:?}"),
    }
}

fn code<T: std::fmt::Debug>(result: Result<T, Error>) -> Code {
    refusal(result).code
}

#[cfg(feature = "sqlite")]
async fn on_sqlite<F, Fut>(case: F)
where
    F: FnOnce(Db) -> Fut,
    Fut: std::future::Future<Output = ()>,
{
    let conn = rusqlite::Connection::open_in_memory().expect("SQLite opens");
    for ddl in common::store_ddl(false) {
        conn.execute_batch(&ddl).expect("the table is made");
    }
    case(Db::Sqlite(conn)).await;
}

#[cfg(feature = "postgres")]
async fn on_postgres<F, Fut>(case: F)
where
    F: FnOnce(Db) -> Fut + Send + 'static,
    Fut: std::future::Future<Output = ()> + Send + 'static,
{
    let Some(url) = common::pg::dsn(
        "set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the behaviors' adapter on Postgres",
    ) else {
        return;
    };
    common::pg::on_schema(url, |schema| async move {
        let client = schema.client().await;
        for ddl in common::store_ddl(true) {
            client.batch_execute(&ddl).await.expect("the table is made");
        }
        case(Db::Postgres(client)).await;
    })
    .await;
}

/// The case on each dialect: `on_sqlite` and `on_postgres` in a module of
/// its name.
macro_rules! on_each_dialect {
    ($case:ident) => {
        mod $case {
            #[cfg(feature = "sqlite")]
            #[tokio::test]
            async fn on_sqlite() {
                super::on_sqlite(super::$case).await;
            }

            #[cfg(feature = "postgres")]
            #[tokio::test]
            async fn on_postgres() {
                super::on_postgres(super::$case).await;
            }
        }
    };
}

on_each_dialect!(a_create_initializes_the_status_and_a_transition_moves_it);
on_each_dialect!(a_veto_and_a_missing_permission_write_nothing);
on_each_dialect!(a_raw_write_of_an_owned_column_is_never_refused_and_the_core_reads_it);
on_each_dialect!(an_update_writes_own_fields_and_refuses_owned_ones);
on_each_dialect!(a_held_key_is_a_conflict_and_a_deleted_row_is_not_found);
on_each_dialect!(a_refused_command_leaves_the_callers_transaction_going_on);
on_each_dialect!(a_create_with_no_id_gets_a_uuid_key_and_a_type_the_layout_lacks_is_internal);
on_each_dialect!(soft_deletable_rows);
on_each_dialect!(rules_across_rows_and_writes_taken_later);
on_each_dialect!(a_row_read_holds_the_audit_fields);
on_each_dialect!(a_sub_table_with_no_queue_and_own_columns_read_as_the_core_holds_them);

/// Creates Order o1, titled Desk, at `at`.
async fn desk(
    db: &mut Db,
    core: &superschematic_rules::Core,
    layout: &superschematic_rules_adapter::Layout,
    at: i64,
) {
    let host = Host::new(core, layout).with_clock(move || at);
    let key = db
        .create(&host, "Order", json!({"title": "Desk"}), Some("o1"))
        .await
        .expect("created");
    assert_eq!(key, "o1");
}

/// A create runs Workflow's initializer: the owned columns at the
/// command's time, at version 1. A transition writes the status and when
/// it was entered, and moves the version on.
async fn a_create_initializes_the_status_and_a_transition_moves_it(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let created = Stored {
        title: "Desk".into(),
        status: Some("draft".into()),
        entered_at: Some(iso(1_001_000)),
        version: 1,
    };
    assert_eq!(db.stored("order", "o1").await, Some(created));
    let host = Host::new(&core, &layout).with_clock(|| 1_002_000);
    let row = db
        .read(&host, "Order", "o1")
        .await
        .expect("read")
        .expect("a row");
    assert_eq!(
        serde_json::to_value(row).expect("json"),
        json!({"own": {"title": "Desk"}, "owned": {"status": {"status": "draft", "enteredAt": 1_001_000}}, "version": 1})
    );
    let moved = db
        .transition(&host, "Order", "o1", "paid", alice())
        .await
        .expect("moved");
    assert_eq!(moved, json!({"from": "draft", "to": "paid"}));
    let paid = Stored {
        title: "Desk".into(),
        status: Some("paid".into()),
        entered_at: Some(iso(1_002_000)),
        version: 2,
    };
    assert_eq!(db.stored("order", "o1").await, Some(paid));
}

/// A veto and a permission the caller lacks refuse, whole, and write
/// nothing.
async fn a_veto_and_a_missing_permission_write_nothing(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let host = Host::new(&core, &layout).with_clock(|| 1_002_000);
    db.transition(&host, "Order", "o1", "paid", alice())
        .await
        .expect("moved");
    let before = db.stored("order", "o1").await;
    let vetoed = refusal(db.transition(&host, "Order", "o1", "draft", alice()).await);
    assert_eq!(
        (vetoed.code, vetoed.status, vetoed.class.as_str()),
        (Code::Vetoed, 409, "BehaviorVetoError")
    );
    assert_eq!(
        vetoed.message,
        "behavior Workflow vetoes transition of Order o1: no transition leads from paid to draft; from paid it can move to shipped"
    );
    let forbidden = refusal(
        db.transition(&host, "Order", "o1", "shipped", alice())
            .await,
    );
    assert_eq!(
        (forbidden.code, forbidden.status, forbidden.class.as_str()),
        (Code::Forbidden, 403, "EngineError")
    );
    assert_eq!(db.stored("order", "o1").await, before);
    // The caller who holds the permission moves it.
    let shipped = db
        .transition(&host, "Order", "o1", "shipped", shipper())
        .await
        .expect("moved");
    assert_eq!(shipped, json!({"from": "paid", "to": "shipped"}));
}

/// A raw SQL write of an owned column is accepted and moves the version,
/// and the core reads what it wrote: the escape hatch D103 keeps open.
async fn a_raw_write_of_an_owned_column_is_never_refused_and_the_core_reads_it(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let raw = format!(
        "UPDATE \"order\" SET status = 'shipped', status_entered_at = NULL WHERE id = {}",
        p(db.postgres(), 1)
    );
    assert_eq!(db.exec(&raw, &[Param::text("o1")]).await, 1);
    let host = Host::new(&core, &layout);
    let row = db
        .read(&host, "Order", "o1")
        .await
        .expect("read")
        .expect("a row");
    assert_eq!(
        serde_json::to_value(&row.owned).expect("json"),
        json!({"status": {"status": "shipped"}})
    );
    assert_eq!(row.version, 2);
    // shipped is terminal: no transition leaves it.
    let terminal = refusal(db.transition(&host, "Order", "o1", "paid", shipper()).await);
    assert_eq!(terminal.code, Code::Vetoed);
    assert_eq!(
        terminal.message,
        "behavior Workflow vetoes transition of Order o1: shipped is a terminal state: no transition leaves it"
    );
}

/// An update writes the own fields and keeps the states; one that names an
/// owned field is refused, the anchor in the core's words and another owned
/// field in the adapter's, and writes nothing.
async fn an_update_writes_own_fields_and_refuses_owned_ones(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let host = Host::new(&core, &layout);
    db.update(&host, "Order", "o1", json!({"title": "Lamp"}))
        .await
        .expect("updated");
    let lamp = db.stored("order", "o1").await.expect("a row");
    assert_eq!(
        (lamp.title.as_str(), lamp.status.as_deref(), lamp.version),
        ("Lamp", Some("draft"), 2)
    );
    let anchor = refusal(
        db.update(&host, "Order", "o1", json!({"status": "paid"}))
            .await,
    );
    assert_eq!((anchor.code, anchor.status), (Code::InvalidInstance, 422));
    let entered = refusal(
        db.update(&host, "Order", "o1", json!({"statusEnteredAt": null}))
            .await,
    );
    assert_eq!(
        entered.message,
        "Order: statusEnteredAt: statusEnteredAt is behavior Workflow's field: only its operations change it"
    );
    assert_eq!(db.stored("order", "o1").await.expect("a row").version, 2);
}

/// A create of a key the table holds is conflict, a delete takes the row,
/// and a command on a row that is gone is not_found.
async fn a_held_key_is_a_conflict_and_a_deleted_row_is_not_found(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let host = Host::new(&core, &layout);
    let again = refusal(
        db.create(&host, "Order", json!({"title": "Again"}), Some("o1"))
            .await,
    );
    assert_eq!(
        again,
        Refusal::new(Code::Conflict, "Order o1 already exists")
    );
    db.delete(&host, "Order", "o1", None)
        .await
        .expect("deleted");
    assert_eq!(db.stored("order", "o1").await, None);
    let gone = refusal(db.delete(&host, "Order", "o1", None).await);
    assert_eq!(
        (gone.code, gone.status, gone.message.as_str()),
        (Code::NotFound, 404, "Order o1 does not exist")
    );
}

/// A command the core refuses leaves the caller's transaction going on:
/// the next command in it applies.
async fn a_refused_command_leaves_the_callers_transaction_going_on(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    desk(&mut db, &core, &layout, 1_001_000).await;
    let host = Host::new(&core, &layout);
    let moved = db.vetoed_then_moved(&host, "o1").await.expect("moved");
    assert_eq!(moved, json!({"from": "draft", "to": "paid"}));
    assert_eq!(
        db.stored("order", "o1")
            .await
            .expect("a row")
            .status
            .as_deref(),
        Some("paid")
    );
}

/// A UUID subject in its two forms: base62, a principal's subject and the
/// core's value of a UUID field, and hyphenated, as its column holds it.
const DANA: (&str, &str) = (
    "TMXp8QPR6yGvKuV2GZQ10",
    "0f8fad5b-d9cb-469f-a165-70867728950e",
);
const ERIN: (&str, &str) = (
    "3n9R8RlRyqVJBUQUv6toL1",
    "7c9e6679-7425-40de-944b-e07fc1f90ae7",
);

/// A row read holds the audit fields the layout names among its own fields
/// (D104), a UUID in base62 as an own UUID column is and a NULL one absent,
/// and no command writes one: a Workflow transition whose by names
/// createdBy is made by the subject the repository stamped it with and
/// refused to any other as not_principal, one whose by names an own field
/// by the subject it holds, and a repeat answered unchanged leaves the row
/// as it was, its version and when its status was entered, and queues no
/// change message.
async fn a_row_read_holds_the_audit_fields(mut db: Db) {
    let postgres = db.postgres();
    for ddl in common::question_ddl(postgres) {
        db.exec(&ddl, &[]).await;
    }
    let core = common::core();
    let layout = Layout::new([common::question_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 1_001_000);
    ask_stamped_and_unstamped(&mut db, &host).await;
    let host = Host::new(&core, &layout).with_clock(|| 1_002_000);
    moved_by_the_subject_a_field_holds(&mut db, &host).await;
    let later = Host::new(&core, &layout).with_clock(|| 1_003_000);
    a_repeat_writes_nothing(&mut db, &later).await;

    // An update that names the audit field is refused, as one of a field
    // the layout has no own column for, and the stamp stays.
    let refused = refusal(
        db.update(&host, "Question", "q1", json!({"createdBy": ERIN.0}))
            .await,
    );
    assert_eq!(
        (refused.code, refused.message.as_str()),
        (
            Code::Internal,
            "rules: Question has no own field createdBy in the behaviors' layout"
        )
    );
    let row = read_question(&mut db, &host, "q1").await;
    assert_eq!(row.own.get("createdBy"), Some(&json!(DANA.0)));
}

/// A question by its key, which the table holds.
async fn read_question(db: &mut Db, host: &Host<'_>, key: &str) -> Row {
    db.read(host, "Question", key)
        .await
        .expect("read")
        .expect("a row")
}

/// Creates q1, which a repository's create stamps with its caller beside
/// the own columns it encodes, and q2, which nothing stamps: q1's read
/// holds createdBy in base62 among its own fields, and q2's none.
async fn ask_stamped_and_unstamped(db: &mut Db, host: &Host<'_>) {
    let stamped = CreateOptions {
        id: Some("q1".into()),
        execute: ExecuteOptions {
            own_columns: Some(object(json!({
                "prompt": "Which table?",
                "asked_of": ERIN.1,
                "created_by": DANA.1
            }))),
            soft_delete: None,
        },
        ..CreateOptions::default()
    };
    let asked = json!({"prompt": "Which table?", "askedOf": ERIN.0});
    db.create_with(host, "Question", asked.clone(), &stamped)
        .await
        .expect("created");
    let row = read_question(db, host, "q1").await;
    assert_eq!(
        serde_json::to_value(&row.own).expect("json"),
        json!({"prompt": "Which table?", "askedOf": ERIN.0, "createdBy": DANA.0})
    );
    db.create(host, "Question", asked, Some("q2"))
        .await
        .expect("created");
    let unstamped = read_question(db, host, "q2").await;
    assert_eq!(
        serde_json::to_value(&unstamped.own).expect("json"),
        json!({"prompt": "Which table?", "askedOf": ERIN.0})
    );
}

fn subject(subject: &str) -> Caller {
    Caller::new(subject, Vec::<String>::new())
}

/// The veto code of a vetoed command.
fn veto_code<T: std::fmt::Debug>(result: Result<T, Error>) -> Option<Value> {
    let refused = refusal(result);
    assert_eq!(refused.code, Code::Vetoed, "{refused:?}");
    refused
        .details
        .as_ref()
        .and_then(|details| details.get("vetoCode"))
        .cloned()
}

/// q1's withdrawal is made by the subject its createdBy holds alone, and
/// q2, which holds none, is withdrawn by no one; q2's answer is made by the
/// subject its askedOf holds.
async fn moved_by_the_subject_a_field_holds(db: &mut Db, host: &Host<'_>) {
    let not_principal = Some(json!("not_principal"));
    let refused = db
        .transition(host, "Question", "q1", "withdrawn", subject(ERIN.0))
        .await;
    assert_eq!(veto_code(refused), not_principal);
    // A field with no value names no one.
    let refused = db
        .transition(host, "Question", "q2", "withdrawn", subject(DANA.0))
        .await;
    assert_eq!(veto_code(refused), not_principal);
    let withdrawn = db
        .transition(host, "Question", "q1", "withdrawn", subject(DANA.0))
        .await
        .expect("moved");
    assert_eq!(withdrawn, json!({"from": "open", "to": "withdrawn"}));
    let answered = db
        .transition(host, "Question", "q2", "answered", subject(ERIN.0))
        .await
        .expect("moved");
    assert_eq!(answered, json!({"from": "open", "to": "answered"}));
}

/// A move of q2 to the state it is in, by any caller, answers unchanged
/// and writes nothing: its version and enteredAt stay, and the table's
/// change trigger, which heard its create and its answer, hears nothing
/// more.
async fn a_repeat_writes_nothing(db: &mut Db, host: &Host<'_>) {
    let before = read_question(db, host, "q2").await;
    assert_eq!(before.version, 2);
    let heard = db.heard("question_change_queue", "q2").await;
    assert_eq!(heard, ["insert", "update"]);
    let repeated = db
        .transition(host, "Question", "q2", "answered", subject(DANA.0))
        .await
        .expect("answered");
    assert_eq!(
        repeated,
        json!({"from": "answered", "to": "answered", "unchanged": true})
    );
    let after = read_question(db, host, "q2").await;
    assert_eq!(
        (after.version, &after.owned),
        (before.version, &before.owned)
    );
    assert_eq!(
        serde_json::to_value(&after.owned).expect("json"),
        json!({"status": {"status": "answered", "enteredAt": 1_002_000}})
    );
    assert_eq!(db.heard("question_change_queue", "q2").await, heard);
}

/// Task (D104), whose Approvals keeps a sub-table and which arms no
/// deadline and hears no change, so it has neither queue. Its own columns
/// read back as the core holds them, whatever the dialect stores, and
/// Constants' one-way veto names the values so read; its `createdBy`,
/// which StateRules' `by` and a gate that excludes the creator read, is
/// among its own fields; and its sign-offs live in the sub-table, a hard
/// delete deleting them.
async fn a_sub_table_with_no_queue_and_own_columns_read_as_the_core_holds_them(mut db: Db) {
    let postgres = db.postgres();
    for ddl in common::task_ddl(postgres) {
        db.exec(&ddl, &[]).await;
    }
    let core = common::core();
    let layout = Layout::new([common::task_table()]);
    let host = Host::new(&core, &layout).with_clock(|| 1_001_000);
    let given = json!({
        "title": "Launch", "owner": "bob", "reviewer": "carol", "plan": "First plan",
        "urgent": false, "attempts": 0, "data": {"tags": ["a"]}
    });
    let mut columns = object(given.clone());
    columns.insert("created_by".into(), json!("alice"));
    let stamped = CreateOptions {
        id: Some("t1".into()),
        execute: ExecuteOptions {
            own_columns: Some(columns),
            soft_delete: None,
        },
        ..CreateOptions::default()
    };
    db.create_with(&host, "Task", given, &stamped)
        .await
        .expect("created");
    let task = read_task(&mut db, &host).await;
    assert_eq!(
        serde_json::to_value(&task.own).expect("json").to_string(),
        json!({
            "title": "Launch", "owner": "bob", "reviewer": "carol", "plan": "First plan",
            "urgent": false, "attempts": 0, "data": {"tags": ["a"]}, "createdBy": "alice"
        })
        .to_string()
    );
    own_columns_read_back_as_the_update_gave_them(&mut db, &host).await;
    a_move_back_names_the_values_as_the_core_read_them(&mut db, &host).await;
    let later = Host::new(&core, &layout).with_clock(|| 1_002_000);
    sign_offs_live_in_the_sub_table(&mut db, &host, &later).await;

    // A hard delete deletes the row's sub-table rows with it.
    db.delete(&host, "Task", "t1", None).await.expect("deleted");
    assert!(db.read(&host, "Task", "t1").await.expect("read").is_none());
    assert!(sign_offs_of(&mut db, &host).await.is_empty());
}

/// Task t1, which the table holds.
async fn read_task(db: &mut Db, host: &Host<'_>) -> Row {
    db.read(host, "Task", "t1")
        .await
        .expect("read")
        .expect("a row")
}

/// An own field of a row as its JSON text; `None` for one with no value.
fn own_text(row: &Row, field: &str) -> Option<String> {
    row.own.get(field).map(Value::to_string)
}

/// Each own column of t1 reads back as the update gave it: a boolean as a
/// boolean (SQLite's 1), a number as its digits (SQLite's `REAL` 2.0 as
/// 2), and a JSON column's list and string each as it was; and its
/// creator, whom StateRules' `by` names by `createdBy`, clears the plan it
/// freezes.
async fn own_columns_read_back_as_the_update_gave_them(db: &mut Db, host: &Host<'_>) {
    let patch = json!({"urgent": true, "attempts": 2, "data": ["x", 1]});
    db.update_as(host, "Task", "t1", patch, subject("bob"))
        .await
        .expect("saved");
    db.update_as(host, "Task", "t1", json!({"plan": null}), alice())
        .await
        .expect("saved");
    let task = read_task(db, host).await;
    assert_eq!(own_text(&task, "urgent").as_deref(), Some("true"));
    assert_eq!(own_text(&task, "attempts").as_deref(), Some("2"));
    assert_eq!(own_text(&task, "data").as_deref(), Some(r#"["x",1]"#));
    assert_eq!(own_text(&task, "plan"), None);
    db.update_as(host, "Task", "t1", json!({"data": "5"}), subject("bob"))
        .await
        .expect("saved");
    let task = read_task(db, host).await;
    assert_eq!(own_text(&task, "data").as_deref(), Some(r#""5""#));
}

/// A move of a one-way field back is Constants' veto, naming the values as
/// the core read them from t1's columns: a number and a boolean as they
/// are, and a date-time, which the layout converts to epoch milliseconds,
/// as the ISO 8601 text the table's API gives it in (D104).
async fn a_move_back_names_the_values_as_the_core_read_them(db: &mut Db, host: &Host<'_>) {
    db.update_as(
        host,
        "Task",
        "t1",
        json!({"due": 1_769_990_400_000_i64}),
        subject("bob"),
    )
    .await
    .expect("saved");
    for (patch, field, current, attempted, moved) in [
        (
            json!({"attempts": 1.5}),
            "attempts",
            json!(2),
            json!(1.5),
            "grows: an update may not move it from 2 to 1.5",
        ),
        (
            json!({"urgent": false}),
            "urgent",
            json!(true),
            json!(false),
            "turns true: an update may not move it from true to false",
        ),
        (
            json!({"due": 1_769_904_000_000_i64}),
            "due",
            json!("2026-02-02T00:00:00.000Z"),
            json!("2026-02-01T00:00:00.000Z"),
            "grows: an update may not move it from \"2026-02-02T00:00:00.000Z\" to \"2026-02-01T00:00:00.000Z\"",
        ),
    ] {
        let refused = refusal(
            db.update_as(host, "Task", "t1", patch, subject("bob"))
                .await,
        );
        assert_eq!(
            refused.message,
            format!("behavior Constants vetoes update of Task t1: Task t1's {field} only {moved}; only a caller with tasks.admin may move it back")
        );
        let details = refused.details.as_ref().expect("details");
        assert_eq!(details.get("vetoCode"), Some(&json!("one_way")));
        assert_eq!(
            details.get("vetoDetails").map(Value::to_string),
            Some(
                json!({"field": field, "direction": "up", "current": current, "attempted": attempted})
                    .to_string()
            )
        );
    }
}

/// carol, who reviews tasks.
fn reviewer() -> Caller {
    Caller::new("carol", ["tasks.review"])
}

/// An operation of t1's Approvals as a caller.
async fn approvals(
    db: &mut Db,
    host: &Host<'_>,
    operation: &str,
    params: Value,
    caller: Caller,
) -> Result<Value, Error> {
    db.operate(host, "Task", "t1", "Approvals", operation, params, caller)
        .await
}

/// t1's sign-offs as its sub-table holds them, in key order.
async fn sign_offs_of(db: &mut Db, host: &Host<'_>) -> Vec<SubRow> {
    db.sub_rows(host, "Task", "t1", "Approvals", "signOffs")
        .await
}

/// A sign-off of carol's as the sub-table holds it: its number, its state,
/// when it was given and when it was consumed.
fn carols(number: &str, state: &str, consumed: Option<i64>) -> Value {
    let mut values = object(json!({"state": state, "subject": "carol", "at": 1_001_000}));
    if let Some(at) = consumed {
        values.insert("consumedAt".into(), json!(at));
    }
    json!({"key": number, "values": values})
}

/// The creator, read from `createdBy`, signs off on no gate. A sign-off
/// inserts its row of the sub-table and moves the row's version, which the
/// pinned gate's names; then the rest of [`sign_offs_are_read_withdrawn_and_consumed`].
async fn sign_offs_live_in_the_sub_table(db: &mut Db, host: &Host<'_>, later: &Host<'_>) {
    let creator = Caller::new("alice", ["tasks.review"]);
    let refused = approvals(db, host, "signOff", json!({"state": "proving"}), creator).await;
    assert_eq!(veto_code(refused), Some(json!("creator_cannot_sign")));

    let before = read_task(db, host).await.version;
    let signed = approvals(db, host, "signOff", json!({"state": "proving"}), reviewer())
        .await
        .expect("signed");
    assert_eq!(
        signed,
        json!({"state": "proving", "subject": "carol", "at": 1_001_000, "signedBy": ["carol"], "count": 1})
    );
    let version = read_task(db, host).await.version;
    assert_eq!(version, before + 1);
    let params = json!({"state": "done", "version": before});
    let stale = refusal(approvals(db, host, "signOff", params, reviewer()).await);
    let details = stale.details.as_ref().expect("details");
    assert_eq!(details.get("vetoCode"), Some(&json!("stale")));
    assert_eq!(
        details.get("vetoDetails"),
        Some(&json!({"version": before, "current": version}))
    );
    let params = json!({"state": "done", "version": version});
    approvals(db, host, "signOff", params, reviewer())
        .await
        .expect("signed");
    assert_eq!(
        json!(sign_offs_of(db, host).await),
        json!([
            carols("0000000001", "proving", None),
            carols("0000000002", "done", None)
        ])
    );
    sign_offs_are_read_withdrawn_and_consumed(db, host, later).await;
}

/// `listSignOffs` writes nothing and leaves the version; a withdrawal
/// deletes its row, and the next sign-off takes the number after the
/// greatest; and an update of the proof, at `later`, consumes the done
/// gate's sign-off, whose resetOn names it, by an update of its row, and
/// keeps the proving gate's, whose resetOn is [].
async fn sign_offs_are_read_withdrawn_and_consumed(db: &mut Db, host: &Host<'_>, later: &Host<'_>) {
    let version = read_task(db, host).await.version;
    let listed = approvals(db, host, "listSignOffs", json!({}), reviewer())
        .await
        .expect("listed");
    assert_eq!(
        listed,
        json!({"items": [
            {"state": "proving", "subject": "carol", "at": 1_001_000},
            {"state": "done", "subject": "carol", "at": 1_001_000}
        ], "next": null})
    );
    assert_eq!(read_task(db, host).await.version, version);

    let params = json!({"state": "proving"});
    approvals(db, host, "withdrawSignOff", params.clone(), reviewer())
        .await
        .expect("withdrawn");
    assert_eq!(
        json!(sign_offs_of(db, host).await),
        json!([carols("0000000002", "done", None)])
    );
    approvals(db, host, "signOff", params, reviewer())
        .await
        .expect("signed");

    let patch = json!({"proof": "Screenshots"});
    db.update_as(later, "Task", "t1", patch, subject("bob"))
        .await
        .expect("saved");
    assert_eq!(
        json!(sign_offs_of(db, host).await),
        json!([
            carols("0000000002", "done", Some(1_002_000)),
            carols("0000000003", "proving", None)
        ])
    );
}

/// A create with no id of a UUID-keyed table gets a UUID's wire form, which
/// its column holds hyphenated; a type the layout lacks is the adapter's
/// internal refusal.
async fn a_create_with_no_id_gets_a_uuid_key_and_a_type_the_layout_lacks_is_internal(mut db: Db) {
    let (core, layout) = (common::core(), common::store_layout());
    let host = Host::new(&core, &layout);
    let key = db
        .create(&host, "Item", json!({"title": "Bolt"}), None)
        .await
        .expect("created");
    assert!(
        (1..=22).contains(&key.len()) && key.chars().all(|c| c.is_ascii_alphanumeric()),
        "{key}"
    );
    let ids = db.ids("item").await;
    assert_eq!(ids.len(), 1);
    assert_eq!(ids[0].len(), 36);
    let row = db
        .read(&host, "Item", &key)
        .await
        .expect("read")
        .expect("a row");
    assert_eq!(row.owned["status"]["status"], json!("draft"));

    let unknown = refusal(db.read(&host, "Invoice", "i1").await);
    assert_eq!(
        (unknown.code, unknown.status, unknown.class.as_str()),
        (Code::Internal, 500, "Error")
    );
    assert_eq!(
        unknown.message,
        "rules: the behaviors' layout has no type Invoice; it holds Booking, Held, Item, Loud, Order, Probe, Ticket, Timed"
    );
}

/// A soft-deletable table: a delete through the core stamps the live row in
/// place of deleting it, after which the core holds it not_found, and a row
/// another stamps deleted is not_found as well.
async fn soft_deletable_rows(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 2_000_000);
    db.create(&host, "Ticket", json!({"title": "Leak"}), Some("t1"))
        .await
        .expect("created");
    db.delete(
        &host,
        "Ticket",
        "t1",
        Some(json!({"deleted_at": iso(2_000_000)})),
    )
    .await
    .expect("deleted");
    let stamped = db.stored("ticket", "t1").await.expect("the row stays");
    assert_eq!(
        (stamped.status.as_deref(), stamped.version),
        (Some("draft"), 2)
    );
    assert!(db
        .read(&host, "Ticket", "t1")
        .await
        .expect("read")
        .is_none());
    assert_eq!(
        code(db.transition(&host, "Ticket", "t1", "paid", alice()).await),
        Code::NotFound
    );
    assert_eq!(
        code(
            db.delete(
                &host,
                "Ticket",
                "t1",
                Some(json!({"deleted_at": iso(2_000_000)}))
            )
            .await
        ),
        Code::NotFound
    );

    db.create(&host, "Ticket", json!({"title": "Drip"}), Some("t2"))
        .await
        .expect("created");
    let postgres = db.postgres();
    let soft = format!(
        "UPDATE ticket SET deleted_at = {}, _version = _version + 1 WHERE id = {}",
        p(postgres, 1),
        p(postgres, 2)
    );
    assert_eq!(
        db.exec(&soft, &[Param::text(iso(2_000_000)), Param::text("t2")])
            .await,
        1
    );
    assert_eq!(
        code(db.transition(&host, "Ticket", "t2", "paid", alice()).await),
        Code::NotFound
    );
    assert_eq!(
        db.stored("ticket", "t2")
            .await
            .expect("the row stays")
            .status
            .as_deref(),
        Some("draft")
    );
    // A key a live row does not hold is a create's to take, the deleted row
    // included: a create probes every row.
    assert_eq!(
        code(
            db.create(&host, "Ticket", json!({"title": "Again"}), Some("t2"))
                .await
        ),
        Code::Conflict
    );
}

/// A rule across rows decides under its lock, and of two creates that each
/// take one room the second is refused; a create whose change set holds an
/// outbox event is refused as internal with nothing written; one that arms
/// a deadline writes its message; a prepared create applies nothing.
async fn rules_across_rows_and_writes_taken_later(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 3_000_000);
    // A create that decides under a lock prepares on a store that holds
    // its locks, which hold for the upsert that follows; one whose locks
    // are fenced could check its read only in a unit of its own.
    let prepared = db.prepare(&host, "Booking", json!({})).await;
    if db.postgres() {
        assert_eq!(prepared.expect("prepared"), Owned::new());
    } else {
        assert_eq!(
            refusal(prepared).message,
            "rules: an upsert of Booking binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own"
        );
    }
    assert!(db.ids("booking").await.is_empty());

    db.create(&host, "Booking", json!({}), Some("b1"))
        .await
        .expect("created");
    let booked = refusal(db.create(&host, "Booking", json!({}), Some("b2")).await);
    assert_eq!(booked, Refusal::new(Code::Conflict, "room r1 is booked"));
    assert_eq!(db.ids("booking").await, ["b1"]);

    let loud = refusal(db.create(&host, "Loud", json!({}), Some("l1")).await);
    assert_eq!(
        loud,
        Refusal::internal("rules: create of Loud writes outbox events of a behavior's own, which the adapter does not take yet: a table's change queue holds its writes, and the event stream comes later (D103)")
    );
    assert!(db.ids("loud").await.is_empty());
    db.create(&host, "Timed", json!({}), Some("d1"))
        .await
        .expect("created");
    assert_eq!(
        db.messages("timed_deadline_queue", "d1").await,
        [("expire".to_owned(), 3_001_000, "ready".to_owned())]
    );

    let owned = db
        .prepare(&host, "Order", json!({"title": "Desk"}))
        .await
        .expect("prepared");
    assert_eq!(
        serde_json::to_value(owned).expect("json"),
        json!({"status": {"status": "draft", "enteredAt": 3_000_000}})
    );
    assert!(db.ids("order").await.is_empty());
}

/// Timed's deadline `expire` of a row, armed at 3_001_000 with `payload`.
fn expire(key: &str, payload: Value) -> Deadline {
    Deadline {
        key: key.into(),
        use_id: "OneRoom".into(),
        name: "expire".into(),
        at: 3_001_000,
        payload,
    }
}

/// The queue Timed's deadlines are armed in.
const TIMED_QUEUE: &str = "timed_deadline_queue";

/// A deadline (D103): a create arms its message; a run with a claim whose
/// token is not the message's runs nothing and leaves it claimed; one with
/// the message's claim runs it and spends the message in its commit, and
/// the same claim again finds it spent.
async fn a_deadline_runs_on_its_claim_and_a_lost_claim_runs_nothing(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 3_000_000);
    db.create(&host, "Timed", json!({}), Some("d1"))
        .await
        .expect("created");
    let claim = db.claim(TIMED_QUEUE, "d1").await;
    let stranger = Claim {
        token: if db.postgres() {
            "00000000-0000-4000-8000-000000000000".into()
        } else {
            "0".into()
        },
        ..claim.clone()
    };
    let lost = db
        .run_deadline(&host, "Timed", &expire("d1", Value::Null), Some(&stranger))
        .await
        .expect("ran nothing");
    assert!(matches!(lost, DeadlineRun::ClaimLost), "{lost:?}");
    assert_eq!(
        db.messages(TIMED_QUEUE, "d1").await,
        [("expire".to_owned(), 3_001_000, "claimed".to_owned())]
    );
    let ran = db
        .run_deadline(&host, "Timed", &expire("d1", Value::Null), Some(&claim))
        .await
        .expect("ran");
    let DeadlineRun::Ran(executed) = ran else {
        panic!("not run: {ran:?}");
    };
    assert_eq!(executed.attempts, 1);
    assert!(db.messages(TIMED_QUEUE, "d1").await.is_empty());
    let again = db
        .run_deadline(&host, "Timed", &expire("d1", Value::Null), Some(&claim))
        .await
        .expect("ran nothing");
    assert!(matches!(again, DeadlineRun::ClaimLost), "{again:?}");
}

/// A deadline that arms itself again survives the spend of the one it ran,
/// armed a second after the time it ran at, not the host's lagging clock's;
/// without a claim a deadline runs only while it is armed at its time; a
/// row a raw SQL delete removed leaves its message, which a run spends and
/// reports gone; and a delete through the core deletes the row's messages
/// (D103).
async fn a_deadline_armed_again_survives_its_spend_and_a_rows_go_with_it(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 3_000_000);
    db.create(&host, "Timed", json!({}), Some("d2"))
        .await
        .expect("created");
    let claim = db.claim(TIMED_QUEUE, "d2").await;
    db.run_deadline(
        &host,
        "Timed",
        &expire("d2", json!({"again": true})),
        Some(&claim),
    )
    .await
    .expect("ran");
    assert_eq!(
        db.messages(TIMED_QUEUE, "d2").await,
        [("expire".to_owned(), 3_002_000, "ready".to_owned())]
    );
    let refused = refusal(
        db.run_deadline(&host, "Timed", &expire("d2", Value::Null), None)
            .await,
    );
    assert_eq!(
        (refused.code, refused.message.as_str()),
        (
            Code::NotFound,
            "Timed d2 has no deadline OneRoom.expire armed at 3001000"
        )
    );

    db.exec("DELETE FROM timed WHERE id = 'd2'", &[]).await;
    let claim = db.claim(TIMED_QUEUE, "d2").await;
    let rearmed = Deadline {
        at: 3_002_000,
        ..expire("d2", Value::Null)
    };
    let gone = db
        .run_deadline(&host, "Timed", &rearmed, Some(&claim))
        .await
        .expect("ran nothing");
    assert!(
        matches!(&gone, DeadlineRun::Gone(refusal) if refusal.code == Code::NotFound),
        "{gone:?}"
    );
    assert!(db.messages(TIMED_QUEUE, "d2").await.is_empty());

    db.create(&host, "Timed", json!({}), Some("d3"))
        .await
        .expect("created");
    db.delete(&host, "Timed", "d3", None)
        .await
        .expect("deleted");
    assert!(db.messages(TIMED_QUEUE, "d3").await.is_empty());
}

/// The queue Held's deadlines are armed in.
const HELD_QUEUE: &str = "held_deadline_queue";

/// A fire a guard vetoes (D103): with a claim, the run releases the claimed
/// message, ready again and due in a minute, the claim's attempt given
/// back and its token gone, and leaves the deadline's sub-table row
/// pending, as it was; a second release of the same claim finds it lost.
/// Hold, the tests' toy, vetoes every fireTimer of Held's Timers.
async fn a_vetoed_fire_is_released_counting_no_attempt(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 3_000_000);
    db.create(&host, "Held", json!({}), Some("h1"))
        .await
        .expect("created");
    let pending = db
        .sub_rows(&host, "Held", "h1", "Timers", "deadlines")
        .await;
    assert_eq!(pending.len(), 1, "{pending:?}");
    assert_eq!(pending[0].values.get("fired"), Some(&json!(false)));
    let before = db.queued(HELD_QUEUE, "h1").await;
    assert_eq!(before.len(), 1, "{before:?}");
    let claim = db.claim(HELD_QUEUE, "h1").await;
    let expire = Deadline {
        key: "h1".into(),
        use_id: "Timers".into(),
        name: "expire".into(),
        at: 3_001_000,
        payload: Value::Null,
    };
    let run = db
        .run_deadline(&host, "Held", &expire, Some(&claim))
        .await
        .expect("ran nothing");
    assert_eq!((run.ran(), run.why()), (false, "vetoed"));
    let DeadlineRun::Vetoed(veto) = &run else {
        panic!("not released: {run:?}");
    };
    assert_eq!(
        (veto.code, veto.status, veto.class.as_str()),
        (Code::Vetoed, 409, "BehaviorVetoError")
    );
    let details = veto.details.as_ref().expect("the veto's details");
    assert_eq!(
        (details.get("behavior"), details.get("vetoCode")),
        (Some(&json!("Hold")), Some(&json!("held")))
    );
    let released = db.queued(HELD_QUEUE, "h1").await;
    let [message] = released.as_slice() else {
        panic!("one message: {released:?}");
    };
    assert_eq!(
        (message.state.as_str(), message.attempts, message.token),
        ("ready", before[0].attempts, false)
    );
    assert!(
        (59.0..=61.0).contains(&message.due_in),
        "due in {}s",
        message.due_in
    );
    assert_eq!(
        db.sub_rows(&host, "Held", "h1", "Timers", "deadlines")
            .await,
        pending
    );
    let again = db
        .run_deadline(&host, "Held", &expire, Some(&claim))
        .await
        .expect("ran nothing");
    assert!(matches!(again, DeadlineRun::ClaimLost), "{again:?}");
}

/// Probe rows' keys, in their columns' hyphenated form.
const P1: &str = "00000000-0000-4000-8000-0000000000a1";
const P2: &str = "00000000-0000-4000-8000-0000000000a2";
const P3: &str = "00000000-0000-4000-8000-0000000000a3";
const P4: &str = "00000000-0000-4000-8000-0000000000a4";

/// A UUID an own column holds, hyphenated, and its wire form.
const OWNER: (&str, &str) = (
    "db360f43-5530-c407-56da-9cd8e0c971dd",
    "6fe1XoZ0UJpMNNdUOSMC2X",
);

/// The change Echo heard last on a Probe row, as the core got it.
async fn echoed(db: &mut Db, host: &Host<'_>, key: &str) -> Value {
    let row = db
        .read(host, "Probe", key)
        .await
        .expect("read")
        .expect("a row");
    let mark = row.owned["Echo"]["heard"]
        .as_str()
        .expect("a mark")
        .to_owned();
    serde_json::from_str(&mark).expect("the mark is JSON")
}

/// Inserts Probe's row P1 with SQL, past the adapter, in the database's
/// session time zone of +05:30 on Postgres, and answers the one message
/// the trigger wrote, an insert, whose image holds the offset there.
async fn insert_raw(db: &mut Db) -> Written {
    let postgres = db.postgres();
    if postgres {
        db.exec("SET TIME ZONE 'Asia/Kolkata'", &[]).await;
    }
    let insert = format!(
        "INSERT INTO probe (id, title, due, owner_id, done, data) VALUES ({}, 'Desk', {}, {}, {}, {})",
        p(postgres, 1),
        p(postgres, 2),
        p(postgres, 3),
        if postgres { "true" } else { "1" },
        p(postgres, 4)
    );
    let values = [
        Param::text(P1),
        Param::text(iso(1234)),
        Param::text(OWNER.0),
        Param::text(r#"{"a":1}"#),
    ];
    assert_eq!(db.exec(&insert, &values).await, 1);
    let mut written = db.written(P1).await;
    assert_eq!(written.len(), 1, "one message: {written:?}");
    let message = written.remove(0);
    assert_eq!(
        (
            message.write.as_str(),
            message.state.as_str(),
            message.before.as_deref()
        ),
        ("insert", "ready", None)
    );
    if postgres {
        let after = message.after.as_deref().unwrap_or_default();
        assert!(after.contains("+05:30"), "{after}");
    }
    message
}

/// After P1's insert ran, its message is spent, and Echo's mark is a write
/// of its own, which the trigger wrote and whose run writes nothing more.
async fn its_own_write_writes_nothing_more(db: &mut Db, host: &Host<'_>) {
    let written = db.written(P1).await;
    let [marked] = written.as_slice() else {
        panic!("one message: {written:?}");
    };
    assert_eq!(marked.write, "update");
    let version = db
        .read(host, "Probe", P1)
        .await
        .expect("read")
        .expect("a row")
        .version;
    let claim = db.claim_change(&marked.id).await;
    let ChangeRun::Ran(executed) = db
        .run_change(host, &marked.change(db.postgres()), Some(&claim))
        .await
        .expect("ran")
    else {
        panic!("not run");
    };
    assert!(executed.changes.rows.is_empty(), "{executed:?}");
    assert!(db.written(P1).await.is_empty());
    let after = db.read(host, "Probe", P1).await.expect("read");
    assert_eq!(after.map(|row| row.version), Some(version));
}

/// A raw SQL insert is heard (D103): the table's trigger writes a message
/// of its change queue, its image the JSON its dialect writes (Postgres a
/// timestamp with the session's offset, SQLite a boolean 1 and JSON as
/// text); a worker's run of it on its claim hands the core the row's own
/// fields converted as a row read converts them, Timers arms the create
/// deadline from the write's time, and the message is spent in the same
/// commit. The write the run made is heard in turn and writes nothing more;
/// the first claim again finds its message gone.
async fn a_raw_write_is_heard_through_the_change_queue_its_images_converted_as_its_dialect_wrote_them(
    mut db: Db,
) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 9_000_000);
    let postgres = db.postgres();
    let message = insert_raw(&mut db).await;
    let at = message.at;
    let claim = db.claim_change(&message.id).await;
    let change = message.change(postgres);
    let run = db
        .run_change(&host, &change, Some(&claim))
        .await
        .expect("ran");
    assert_eq!((run.ran(), run.why()), (true, ""));
    assert_eq!(
        echoed(&mut db, &host, P1).await,
        json!({
            "write": "insert",
            "at": at,
            "after": {"own": {"title": "Desk", "due": 1234, "ownerId": OWNER.1, "done": true, "data": {"a": 1}}, "owned": {}}
        })
    );
    assert_eq!(
        db.messages("probe_deadline_queue", P1).await,
        [("expire".to_owned(), at + 1000, "ready".to_owned())]
    );
    its_own_write_writes_nothing_more(&mut db, &host).await;
    let lost = db
        .run_change(&host, &change, Some(&claim))
        .await
        .expect("ran nothing");
    assert!(matches!(lost, ChangeRun::ClaimLost), "{lost:?}");
    assert_eq!(lost.why(), "claim lost");
}

/// A change of a row that is gone (D103): a soft-deleted row's deadlines
/// go and its sub-table rows stay, a row deleted for good loses both, and
/// a row a change names as deleted that is still there keeps everything.
async fn a_change_of_a_row_that_is_gone_clears_its_deadlines_and_a_soft_deleted_row_keeps_its_sub_table_rows(
    mut db: Db,
) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 9_000_000);
    let postgres = db.postgres();
    db.create(&host, "Probe", json!({"title": "Desk"}), Some(P2))
        .await
        .expect("created");
    assert_eq!(
        db.messages("probe_deadline_queue", P2).await,
        [("expire".to_owned(), 9_001_000, "ready".to_owned())]
    );
    let soft = format!(
        "UPDATE probe SET deleted_at = {} WHERE id = {}",
        p(postgres, 1),
        p(postgres, 2)
    );
    db.exec(&soft, &[Param::text(iso(9_100_000)), Param::text(P2)])
        .await;
    let change = |write: &str, key: &str| TableChange {
        key: key.into(),
        write: write.into(),
        at: 9_200_000,
        before: Value::Null,
        after: Value::Null,
    };
    let ChangeRun::Ran(executed) = db
        .run_change(&host, &change("update", P2), None)
        .await
        .expect("ran")
    else {
        panic!("not run");
    };
    let key = executed.key.clone().expect("the row's key");
    assert_eq!(
        serde_json::to_value(&executed.changes.rows).expect("json"),
        json!([{"kind": "gone", "type": "Probe", "key": key}])
    );
    assert!(db.messages("probe_deadline_queue", P2).await.is_empty());
    assert_eq!(
        db.sub_rows(&host, "Probe", P2, "Timers", "deadlines")
            .await
            .len(),
        1
    );

    // Deleted for good, its trigger's message runs: its sub-table rows go
    // too, which SQLite keeps without its foreign keys enforced.
    db.exec(
        &format!("DELETE FROM probe WHERE id = {}", p(postgres, 1)),
        &[Param::text(P2)],
    )
    .await;
    let written = db.written(P2).await;
    let deleted = written
        .iter()
        .find(|message| message.write == "delete")
        .expect("the delete's message");
    db.run_change(&host, &deleted.change(postgres), None)
        .await
        .expect("ran");
    assert!(db
        .sub_rows(&host, "Probe", P2, "Timers", "deadlines")
        .await
        .is_empty());

    // A row that is there keeps its deadlines and sub-table rows whatever
    // a change names.
    db.create(&host, "Probe", json!({"title": "Lamp"}), Some(P3))
        .await
        .expect("created");
    let ChangeRun::Ran(executed) = db
        .run_change(&host, &change("delete", P3), None)
        .await
        .expect("ran")
    else {
        panic!("not run");
    };
    assert!(
        !executed
            .changes
            .rows
            .iter()
            .any(|write| matches!(write, superschematic_rules::RowWrite::Gone { .. })),
        "{executed:?}"
    );
    assert_eq!(db.messages("probe_deadline_queue", P3).await.len(), 1);
    assert_eq!(
        db.sub_rows(&host, "Probe", P3, "Timers", "deadlines")
            .await
            .len(),
        1
    );
}

/// A change the adapter refuses rolls its transaction back and leaves its
/// message claimed, for the claim loop to fail and retry (D103).
async fn a_refused_change_leaves_its_message_claimed(mut db: Db) {
    let core = common::core();
    let layout = common::store_layout();
    let host = Host::new(&core, &layout).with_clock(|| 9_000_000);
    db.create(&host, "Probe", json!({"title": "Desk"}), Some(P4))
        .await
        .expect("created");
    let written = db.written(P4).await;
    let [message] = written.as_slice() else {
        panic!("one message: {written:?}");
    };
    let claim = db.claim_change(&message.id).await;
    let tampered = TableChange {
        write: "upsert".into(),
        ..message.change(db.postgres())
    };
    let refused = refusal(db.run_change(&host, &tampered, Some(&claim)).await);
    assert_eq!(refused.code, superschematic_rules::Code::Internal);
    assert!(
        refused
            .message
            .ends_with("is a write of upsert, which is no insert, update or delete"),
        "{}",
        refused.message
    );
    assert_eq!(
        db.change_state(&message.id).await,
        Some(("claimed".to_owned(), true))
    );
}

on_each_dialect!(
    a_raw_write_is_heard_through_the_change_queue_its_images_converted_as_its_dialect_wrote_them
);
on_each_dialect!(a_change_of_a_row_that_is_gone_clears_its_deadlines_and_a_soft_deleted_row_keeps_its_sub_table_rows);
on_each_dialect!(a_refused_change_leaves_its_message_claimed);
on_each_dialect!(a_deadline_runs_on_its_claim_and_a_lost_claim_runs_nothing);
on_each_dialect!(a_deadline_armed_again_survives_its_spend_and_a_rows_go_with_it);
on_each_dialect!(a_vetoed_fire_is_released_counting_no_attempt);

/// Of two creates that each take one room, in transactions of their own on
/// two connections to one SQLite file, the second waits for the first and
/// reads its row: each transaction begins IMMEDIATE, as the store asks, so
/// the second's BEGIN waits for the one writer the first holds.
#[cfg(feature = "sqlite")]
#[test]
fn two_commands_that_take_one_lock_on_sqlite_write_one_row() {
    use rusqlite::TransactionBehavior::Immediate;

    let file = std::sync::Arc::new(common::SqliteFile::new());
    let mut conn = file.open();
    for ddl in common::store_ddl(false) {
        conn.execute_batch(&ddl).expect("the table is made");
    }
    let booking = |conn: &rusqlite::Connection, id: &str| {
        let (core, layout) = (common::core(), common::store_layout());
        let host = Host::new(&core, &layout);
        let options = CreateOptions {
            id: Some(id.into()),
            ..CreateOptions::default()
        };
        superschematic_rules_adapter::sqlite::run_create(
            &host,
            conn,
            "Booking",
            Map::new(),
            alice(),
            &options,
        )
    };
    let first = conn
        .transaction_with_behavior(Immediate)
        .expect("a transaction");
    booking(&first, "b1").expect("created");
    let (ran, done) = std::sync::mpsc::channel();
    let second = std::thread::spawn({
        let file = file.clone();
        move || {
            let mut conn = file.open();
            let tx = conn
                .transaction_with_behavior(Immediate)
                .expect("a transaction");
            let outcome = booking(&tx, "b2");
            let _ = ran.send(());
            outcome
        }
    });
    assert!(
        done.recv_timeout(std::time::Duration::from_millis(300))
            .is_err(),
        "the second command ran past the writer the first holds"
    );
    first.commit().expect("the first commits");
    let booked = refusal(second.join().expect("the second finishes"));
    assert_eq!(booked, Refusal::new(Code::Conflict, "room r1 is booked"));
    let ids: Vec<String> = conn
        .prepare("SELECT id FROM booking ORDER BY id")
        .expect("the read prepares")
        .query_map([], |row| row.get(0))
        .expect("the read runs")
        .collect::<Result<_, _>>()
        .expect("the ids read");
    assert_eq!(ids, ["b1"]);
}

/// On Postgres a rule's lock is the transaction's advisory lock, which no
/// other connection takes until the transaction ends.
#[cfg(feature = "postgres")]
#[tokio::test]
async fn a_rules_lock_on_postgres_is_held_until_the_transaction_ends() {
    let Some(url) = common::pg::dsn(
        "set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the behaviors' adapter on Postgres",
    ) else {
        return;
    };
    common::pg::on_schema(url, |schema| async move {
        let mut client = schema.client().await;
        for ddl in common::store_ddl(true) {
            client.batch_execute(&ddl).await.expect("the table is made");
        }
        let other = schema.client().await;
        let core = common::core();
        let room = common::room();
        let layout = common::store_layout_in(&room);
        let host = Host::new(&core, &layout);
        let tx = client.transaction().await.expect("a transaction");
        let options = CreateOptions {
            id: Some("b1".into()),
            ..CreateOptions::default()
        };
        superschematic_rules_adapter::postgres::run_create(
            &host,
            &tx,
            "Booking",
            Map::new(),
            alice(),
            &options,
        )
        .await
        .expect("created");
        let take = "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))";
        let taken: bool = other.query_one(take, &[&room]).await.expect("tried").get(0);
        assert!(
            !taken,
            "another connection took the lock the transaction holds"
        );
        tx.commit().await.expect("committed");
        let taken: bool = other.query_one(take, &[&room]).await.expect("tried").get(0);
        assert!(taken, "the lock outlived its transaction");
    })
    .await;
}
