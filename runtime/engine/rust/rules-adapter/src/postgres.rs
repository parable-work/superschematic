//! The adapter over Postgres (D103), through tokio-postgres: every function
//! takes the client the caller's transaction runs on (a
//! [`tokio_postgres::Transaction`], or any [`GenericClient`] between
//! `BEGIN` and `COMMIT`, since a change set applies in a savepoint) and
//! runs the core's commands in it, beside the caller's other writes.
//!
//! A lock key is the transaction's advisory lock,
//! `pg_advisory_xact_lock(hashtextextended(key, 0))`, which it holds until
//! it ends, taken before the reads it guards. A change set applies in the
//! savepoint `rules_apply`: a row that moved, a check that fails or a
//! statement the database refuses rolls back to it and releases it, so the
//! caller's transaction goes on.
//!
//! Every value binds as text, as node-pg binds it, so Postgres reads each
//! into the type of the column it meets (a timestamp's ISO text into a
//! `TIMESTAMPTZ`, a UUID's hyphenated text into a `UUID` or a `TEXT`, an
//! object's JSON into a `JSONB`); a row's columns read as `to_jsonb`'s
//! JSON, whatever their types, and its version as the integer it is.

use std::error::Error as StdError;

use bytes::BytesMut;
use serde_json::{Map, Value};
use tokio_postgres::types::{to_sql_checked, Format, FromSql, IsNull, ToSql, Type};
use tokio_postgres::GenericClient;

use crate::convert::{internal, Bind, Cell};
use crate::run::{self, Dialect, Statement, Store};
use crate::{Caller, Command, CreateOptions, Error, ExecuteOptions, Executed, Host, Owned, Row};

/// The savepoint a change set applies in, named apart from a repository's
/// nested transactions.
const APPLY_SAVEPOINT: &str = "rules_apply";

/// Runs one command on a type of the layout to its outcome in `client`'s
/// transaction: answers its reads, takes its locks, applies its change set
/// whole with each row fenced on its version, and runs it again from fresh
/// state when a row moved, up to [`crate::MAX_ATTEMPTS`] times.
///
/// A command with no request id gets one of the host's, kept for every
/// run; a create with no id gets one when its key is a UUID or a string.
/// The core's refusal, a `busy` one after the last attempt, a `conflict`
/// for a create of a key the table holds, and an `internal` one for a type
/// the layout lacks come back as [`Error::Refused`]; a failure of the
/// database as [`Error::Database`].
pub async fn execute<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    command: Command,
    options: &ExecuteOptions,
) -> Result<Executed, Error> {
    run::execute(host, &PgStore { client }, type_name, command, options).await
}

/// Runs an operation of a use on a row and returns its result; null
/// params are `{}`.
#[allow(clippy::too_many_arguments)]
pub async fn run_operation<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    key: &str,
    use_id: &str,
    operation: &str,
    params: Value,
    caller: Caller,
) -> Result<Value, Error> {
    let store = PgStore { client };
    run::run_operation(
        host, &store, type_name, key, use_id, operation, params, caller,
    )
    .await
}

/// Creates a row through the core, its uses' create parameters,
/// validation, guards and initializers run, and returns its key: the
/// options' id, or one the adapter made.
pub async fn run_create<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<String, Error> {
    run::run_create(host, &PgStore { client }, type_name, own, caller, options).await
}

/// Updates a row's own fields through the core with a merge patch; a patch
/// that names an owned field is refused (`invalid_instance`).
pub async fn run_update<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    key: &str,
    patch: Map<String, Value>,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    run::run_update(
        host,
        &PgStore { client },
        type_name,
        key,
        patch,
        caller,
        options,
    )
    .await
}

/// Deletes a row through the core, its uses' guards run; the options'
/// `soft_delete` stamps it deleted in place of deleting it.
pub async fn run_delete<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    key: &str,
    caller: Caller,
    options: &ExecuteOptions,
) -> Result<(), Error> {
    run::run_delete(host, &PgStore { client }, type_name, key, caller, options).await
}

/// Runs a create of a row through the core to its change set and applies
/// nothing: what an upsert's insert binds, so the row it inserts holds
/// what a create would have given it. It returns the owned fields the
/// change set gives the row, or the core's refusal; a create with no id
/// runs with none. A change set that writes anything but the one row
/// refuses it as `internal`.
pub async fn prepare_create<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    own: Map<String, Value>,
    caller: Caller,
    options: &CreateOptions,
) -> Result<Owned, Error> {
    run::prepare_create(host, &PgStore { client }, type_name, own, caller, options).await
}

/// Reads a row of a type by its key as the core reads it: its own and
/// owned fields, each column with no value absent, and its version;
/// `None` when the table holds no such row, or none that is live.
pub async fn read_row<C: GenericClient + Sync>(
    host: &Host<'_>,
    client: &C,
    type_name: &str,
    key: &str,
) -> Result<Option<Row>, Error> {
    run::read_row(host, &PgStore { client }, type_name, key).await
}

/// The store over a client in a transaction.
pub(crate) struct PgStore<'c, C> {
    pub(crate) client: &'c C,
}

impl<C: GenericClient + Sync> PgStore<'_, C> {
    async fn applied(&self, statement: &Statement) -> Result<bool, tokio_postgres::Error> {
        let values = texts(&statement.values);
        let params = params(&values);
        if statement.check {
            let text = format!("SELECT ({}) AS held", statement.text);
            let row = self.client.query_one(&text, &params).await?;
            return Ok(row.try_get::<_, Option<bool>>(0)? == Some(true));
        }
        let changed = self.client.execute(&statement.text, &params).await?;
        Ok(statement.expect.is_none_or(|expect| changed == expect))
    }

    /// Rolls a unit back to its savepoint and releases it. A rollback that
    /// fails (a broken connection) is let go: the caller's transaction is
    /// lost either way, and the error that undid the unit says why.
    async fn undo(&self) {
        let undo =
            format!("ROLLBACK TO SAVEPOINT {APPLY_SAVEPOINT}; RELEASE SAVEPOINT {APPLY_SAVEPOINT}");
        let _ = self.client.batch_execute(&undo).await;
    }
}

impl<C: GenericClient + Sync> Store for PgStore<'_, C> {
    fn dialect(&self) -> Dialect {
        Dialect::Postgres
    }

    fn fenced(&self) -> bool {
        false
    }

    async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
        let values = texts(values);
        let rows = self.client.query(text, &params(&values)).await?;
        rows.iter()
            .map(|row| (0..row.len()).map(|i| cell(row, i)).collect())
            .collect()
    }

    async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
        if statements.is_empty() {
            return Ok(true);
        }
        // A savepoint, so a moved row or a failed statement undoes the unit
        // and leaves the caller's transaction going on.
        self.client
            .batch_execute(&format!("SAVEPOINT {APPLY_SAVEPOINT}"))
            .await?;
        for statement in statements {
            match self.applied(statement).await {
                Ok(true) => {}
                Ok(false) => {
                    self.undo().await;
                    return Ok(false);
                }
                Err(error) => {
                    self.undo().await;
                    return Err(error.into());
                }
            }
        }
        self.client
            .batch_execute(&format!("RELEASE SAVEPOINT {APPLY_SAVEPOINT}"))
            .await?;
        Ok(true)
    }

    async fn lock(&self, key: &str) -> Result<(), Error> {
        let key = Text(Some(key.to_owned()));
        self.client
            .execute(
                "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))",
                &[&key],
            )
            .await?;
        Ok(())
    }
}

/// A row's column as a cell, by the column's type: the JSON `to_jsonb`
/// gives, an integer, a boolean, a float or text.
fn cell(row: &tokio_postgres::Row, i: usize) -> Result<Cell, Error> {
    let kind = row.columns()[i].type_().clone();
    let cell = match kind {
        Type::JSON | Type::JSONB => row
            .try_get::<_, Option<RawJson<'_>>>(i)?
            .map(|raw| serde_json::from_str(raw.0).map(Cell::Json))
            .transpose()
            .map_err(|error| {
                internal(format!(
                    "Postgres gave JSON the adapter cannot read: {error}"
                ))
            })?,
        Type::INT2 => row
            .try_get::<_, Option<i16>>(i)?
            .map(|n| Cell::Int(n.into())),
        Type::INT4 => row
            .try_get::<_, Option<i32>>(i)?
            .map(|n| Cell::Int(n.into())),
        Type::INT8 => row.try_get::<_, Option<i64>>(i)?.map(Cell::Int),
        Type::BOOL => row.try_get::<_, Option<bool>>(i)?.map(Cell::Bool),
        Type::FLOAT4 => row
            .try_get::<_, Option<f32>>(i)?
            .map(|x| Cell::Real(x.into())),
        Type::FLOAT8 => row.try_get::<_, Option<f64>>(i)?.map(Cell::Real),
        Type::TEXT | Type::VARCHAR | Type::BPCHAR | Type::NAME | Type::UNKNOWN => {
            row.try_get::<_, Option<String>>(i)?.map(Cell::Text)
        }
        other => {
            return Err(internal(format!(
                "the adapter reads no column of type {}",
                other.name()
            ))
            .into())
        }
    };
    Ok(cell.unwrap_or(Cell::Null))
}

/// The values of a statement as the text each binds as.
fn texts(values: &[Bind]) -> Vec<Text> {
    values.iter().map(|value| Text(value.text())).collect()
}

fn params(values: &[Text]) -> Vec<&(dyn ToSql + Sync)> {
    values
        .iter()
        .map(|value| value as &(dyn ToSql + Sync))
        .collect()
}

/// A parameter sent as text, which Postgres reads into whatever type it
/// meets, or NULL.
#[derive(Debug)]
struct Text(Option<String>);

impl ToSql for Text {
    fn to_sql(
        &self,
        _ty: &Type,
        out: &mut BytesMut,
    ) -> Result<IsNull, Box<dyn StdError + Sync + Send>> {
        match &self.0 {
            Some(text) => {
                out.extend_from_slice(text.as_bytes());
                Ok(IsNull::No)
            }
            None => Ok(IsNull::Yes),
        }
    }

    fn accepts(_ty: &Type) -> bool {
        true
    }

    fn encode_format(&self, _ty: &Type) -> Format {
        Format::Text
    }

    to_sql_checked!();
}

/// A `json` or `jsonb` column's text, as the binary format carries it.
struct RawJson<'a>(&'a str);

impl<'a> FromSql<'a> for RawJson<'a> {
    fn from_sql(ty: &Type, raw: &'a [u8]) -> Result<Self, Box<dyn StdError + Sync + Send>> {
        let raw = match (ty, raw.split_first()) {
            (&Type::JSONB, Some((1, rest))) => rest,
            (&Type::JSONB, _) => {
                return Err("a jsonb value of a version the adapter does not read".into())
            }
            _ => raw,
        };
        Ok(RawJson(std::str::from_utf8(raw)?))
    }

    fn accepts(ty: &Type) -> bool {
        matches!(*ty, Type::JSON | Type::JSONB)
    }
}

#[cfg(test)]
mod tests {
    //! The Postgres store on the database `SUPERSCHEMATIC_RULES_TEST_DATABASE_URL`
    //! names, in a schema of each test's own: its unit and its lock, and the
    //! host loop over a store that moves the row under a command through the
    //! same transaction, so a moved row and a soft delete made under a
    //! command are each deterministic. Skipped, saying so, without the
    //! variable.

    use std::future::Future;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::time::{SystemTime, UNIX_EPOCH};

    use serde_json::json;
    use tokio_postgres::{Client, NoTls, Transaction};

    use super::*;
    use crate::tests::{alice, both, db_layout, own, refusal, transition, KEY, KEY_COLUMN};
    use crate::{Code, TypeSpec, MAX_ATTEMPTS};
    use superschematic_rules::UseSpec;

    /// A server awaits the adapter's futures on any task, so each is Send.
    #[test]
    fn its_futures_are_send() {
        fn send<T: Send>(_: &T) {}
        fn check(
            host: &Host<'_>,
            client: &tokio_postgres::Client,
            tx: &tokio_postgres::Transaction<'_>,
        ) {
            let options = ExecuteOptions::default();
            let command = Command::new(Caller::default(), 0, crate::Action::Define);
            send(&execute(host, client, "Order", command.clone(), &options));
            send(&execute(host, tx, "Order", command, &options));
            send(&run_operation(
                host,
                tx,
                "Order",
                "o1",
                "status",
                "transition",
                Value::Null,
                Caller::default(),
            ));
            send(&read_row(host, tx, "Order", "o1"));
        }
        let _ = check;
    }

    async fn connect(url: &str, schema: Option<&str>) -> Client {
        let (client, connection) = tokio_postgres::connect(url, NoTls)
            .await
            .expect("Postgres connects");
        tokio::spawn(async move {
            let _ = connection.await;
        });
        if let Some(schema) = schema {
            client
                .batch_execute(&format!("SET search_path TO \"{schema}\""))
                .await
                .expect("the search path is set");
        }
        client
    }

    static SCHEMAS: AtomicUsize = AtomicUsize::new(0);

    /// Runs body with the database's URL and a schema of its own holding
    /// the tables of crate::tests::ddl, dropped after whether body passes
    /// or panics; skipped, saying so, without the variable.
    async fn on_postgres<F, Fut>(body: F)
    where
        F: FnOnce(String, String) -> Fut,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let url = match std::env::var("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL") {
            Ok(url) if !url.is_empty() => url,
            _ => {
                println!("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the behaviors' adapter on Postgres");
                return;
            }
        };
        let nanos = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .map(|since| since.as_nanos())
            .unwrap_or(0);
        let schema = format!(
            "rules_rs_{}_{nanos}_{}",
            std::process::id(),
            SCHEMAS.fetch_add(1, Ordering::SeqCst)
        );
        let admin = connect(&url, None).await;
        admin
            .batch_execute(&format!("CREATE SCHEMA \"{schema}\""))
            .await
            .expect("the schema is made");
        let tables = connect(&url, Some(&schema)).await;
        for ddl in crate::tests::ddl(true) {
            tables.batch_execute(&ddl).await.expect("the table is made");
        }
        let outcome = tokio::spawn(body(url, schema.clone())).await;
        admin
            .batch_execute(&format!("DROP SCHEMA \"{schema}\" CASCADE"))
            .await
            .expect("the schema is dropped");
        if let Err(error) = outcome {
            std::panic::resume_unwind(error.into_panic());
        }
    }

    fn write(text: &str, values: Vec<Bind>) -> Statement {
        Statement {
            text: text.into(),
            values,
            expect: Some(1),
            check: false,
        }
    }

    async fn title(tx: &Transaction<'_>, table: &str) -> (String, i64) {
        let row = tx
            .query_one(
                &format!("SELECT title, _version FROM \"{table}\" WHERE id = $1"),
                &[&KEY_COLUMN],
            )
            .await
            .expect("the row reads");
        (row.get(0), row.get(1))
    }

    /// Whether the unit's savepoint is gone, asked inside a savepoint of the
    /// test's own so the refused RELEASE leaves the transaction going on.
    async fn released(tx: &Transaction<'_>) -> bool {
        tx.batch_execute("SAVEPOINT probe")
            .await
            .expect("a savepoint");
        let gone = tx
            .batch_execute("RELEASE SAVEPOINT rules_apply")
            .await
            .is_err();
        tx.batch_execute("ROLLBACK TO SAVEPOINT probe; RELEASE SAVEPOINT probe")
            .await
            .expect("the probe ends");
        gone
    }

    #[tokio::test]
    async fn a_unit_applies_in_a_savepoint_it_releases_and_one_that_fails_rolls_back_leaving_the_transaction_going_on(
    ) {
        on_postgres(|url, schema| async move {
            let mut client = connect(&url, Some(&schema)).await;
            let tx = client.transaction().await.expect("a transaction");
            tx.execute(
                "INSERT INTO toy (id, title) VALUES ($1, 'a')",
                &[&KEY_COLUMN],
            )
            .await
            .expect("a row");
            let store = PgStore { client: &tx };
            let retitle = |to: &str, id: &str| {
                write(
                    &format!("UPDATE toy SET title = '{to}' WHERE id = $1"),
                    vec![Bind::Text(id.into())],
                )
            };

            assert!(store
                .apply(&[retitle("b", KEY_COLUMN)])
                .await
                .expect("applied"));
            assert!(released(&tx).await);
            assert_eq!(title(&tx, "toy").await, ("b".into(), 2));

            assert!(!store
                .apply(&[retitle("c", KEY_COLUMN), retitle("c", "none")])
                .await
                .expect("moved"));
            assert!(released(&tx).await);
            assert_eq!(title(&tx, "toy").await.0, "b");

            let failed = store
                .apply(&[
                    retitle("d", KEY_COLUMN),
                    write("INSERT INTO nowhere VALUES (1)", Vec::new()),
                ])
                .await;
            assert!(matches!(failed, Err(Error::Database(_))), "{failed:?}");
            assert!(released(&tx).await);
            assert_eq!(title(&tx, "toy").await.0, "b");

            let found = |id: &str| Statement {
                text: "EXISTS (SELECT 1 FROM toy WHERE id = $1)".into(),
                values: vec![Bind::Text(id.into())],
                expect: None,
                check: true,
            };
            assert!(!store
                .apply(&[retitle("e", KEY_COLUMN), found("none")])
                .await
                .expect("moved"));
            assert_eq!(title(&tx, "toy").await.0, "b");
            assert!(store
                .apply(&[found(KEY_COLUMN), retitle("f", KEY_COLUMN)])
                .await
                .expect("applied"));
            assert_eq!(title(&tx, "toy").await.0, "f");

            // An empty unit runs no statement: in a transaction a failure
            // aborted, any statement would be refused.
            assert!(tx.batch_execute("SELECT 1 / 0").await.is_err());
            assert!(store.apply(&[]).await.expect("applied"));
            tx.rollback().await.expect("the caller rolls back");
        })
        .await;
    }

    async fn taken_elsewhere(other: &Client, key: &str) -> bool {
        let row = other
            .query_one(
                "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))",
                &[&key],
            )
            .await
            .expect("the lock is tried");
        !row.get::<_, bool>(0)
    }

    #[tokio::test]
    async fn a_lock_is_the_transactions_advisory_lock_on_the_key_which_another_connection_cannot_take_until_it_ends(
    ) {
        on_postgres(|url, schema| async move {
            let mut client = connect(&url, Some(&schema)).await;
            let other = connect(&url, Some(&schema)).await;
            // An advisory lock is the whole database's, so each key is the
            // schema's own: another test holding a key of one name would
            // make a free one look taken.
            let sku = format!("Stock/sku-1/{schema}");
            let tx = client.transaction().await.expect("a transaction");
            PgStore { client: &tx }.lock(&sku).await.expect("locked");
            assert!(taken_elsewhere(&other, &sku).await);
            tx.commit().await.expect("committed");
            assert!(!taken_elsewhere(&other, &sku).await);

            // The toy's create takes its lock before it decides.
            let stock = format!("Stock/s1/{schema}");
            let core = both();
            let mut layout = db_layout();
            if let Some(toy) = layout.types.get_mut("Toy") {
                toy.spec =
                    TypeSpec::new("Toy").with(UseSpec::on_type("Toy", json!({"lock": stock})));
            }
            let host = Host::new(&core, &layout);
            let tx = client.transaction().await.expect("a transaction");
            let options = CreateOptions {
                id: Some(KEY.into()),
                ..CreateOptions::default()
            };
            run_create(
                &host,
                &tx,
                "Toy",
                own(json!({"title": "Desk"})),
                alice(),
                &options,
            )
            .await
            .expect("created");
            assert!(taken_elsewhere(&other, &stock).await);
            tx.commit().await.expect("committed");
            assert!(!taken_elsewhere(&other, &stock).await);
        })
        .await;
    }

    /// A store whose first `moves` applies each find the row moved first, by
    /// a write through the same transaction, as when another command's
    /// write lands between the read and the apply.
    struct Moving<'c, C> {
        inner: PgStore<'c, C>,
        write: String,
        values: Vec<Bind>,
        moves: usize,
        applies: AtomicUsize,
    }

    impl<C: GenericClient + Sync> Store for Moving<'_, C> {
        fn dialect(&self) -> Dialect {
            self.inner.dialect()
        }

        fn fenced(&self) -> bool {
            self.inner.fenced()
        }

        async fn query(&self, text: &str, values: &[Bind]) -> Result<Vec<Vec<Cell>>, Error> {
            self.inner.query(text, values).await
        }

        async fn apply(&self, statements: &[Statement]) -> Result<bool, Error> {
            if self.applies.fetch_add(1, Ordering::SeqCst) < self.moves {
                let values = texts(&self.values);
                self.inner
                    .client
                    .execute(&self.write, &params(&values))
                    .await?;
            }
            self.inner.apply(statements).await
        }

        async fn lock(&self, key: &str) -> Result<(), Error> {
            self.inner.lock(key).await
        }
    }

    #[tokio::test]
    async fn a_row_that_moved_runs_the_command_again_from_fresh_state_and_one_that_keeps_moving_is_busy_with_nothing_written(
    ) {
        on_postgres(|url, schema| async move {
            let (core, layout) = (both(), db_layout());
            let host = Host::new(&core, &layout).with_clock(|| 5000);
            let mut client = connect(&url, Some(&schema)).await;
            let tx = client.transaction().await.expect("a transaction");
            let options = CreateOptions {
                id: Some(KEY.into()),
                ..CreateOptions::default()
            };
            run_create(
                &host,
                &tx,
                "Order",
                own(json!({"title": "Desk"})),
                alice(),
                &options,
            )
            .await
            .expect("created");
            let bump =
                r#"UPDATE "order" SET title = title || '!', _version = _version + 1 WHERE id = $1"#;
            let moving = |moves: usize| Moving {
                inner: PgStore { client: &tx },
                write: bump.into(),
                values: vec![Bind::Text(KEY_COLUMN.into())],
                moves,
                applies: AtomicUsize::new(0),
            };

            let executed = run::execute(
                &host,
                &moving(1),
                "Order",
                transition("paid"),
                &ExecuteOptions::default(),
            )
            .await
            .expect("applied");
            assert_eq!(executed.attempts, 2);
            assert_eq!(executed.result, json!({"from": "draft", "to": "paid"}));
            // The other write and the command's: both kept, each bumping
            // the version once (the trigger sets what the command sets).
            assert_eq!(title(&tx, "order").await, ("Desk!".into(), 3));

            let mut ship = transition("shipped");
            ship.caller = Caller::new("sam", ["orders.ship"]);
            let refused = refusal(
                run::execute(
                    &host,
                    &moving(MAX_ATTEMPTS),
                    "Order",
                    ship,
                    &ExecuteOptions::default(),
                )
                .await,
            );
            assert_eq!((refused.code, refused.status), (Code::Busy, 503));
            assert!(
                refused
                    .message
                    .contains("it ran 8 times and nothing was written"),
                "{}",
                refused.message
            );
            let status: String = tx
                .query_one(
                    r#"SELECT status FROM "order" WHERE id = $1"#,
                    &[&KEY_COLUMN],
                )
                .await
                .expect("the row reads")
                .get(0);
            assert_eq!(status, "paid");
            assert_eq!(
                title(&tx, "order").await,
                (
                    format!("Desk{}", "!".repeat(1 + MAX_ATTEMPTS)),
                    3 + MAX_ATTEMPTS as i64
                )
            );
            tx.commit().await.expect("committed");
        })
        .await;
    }

    #[tokio::test]
    async fn a_soft_delete_made_under_a_command_moves_the_row_to_not_found() {
        on_postgres(|url, schema| async move {
            let (core, layout) = (both(), db_layout());
            let host = Host::new(&core, &layout);
            let mut client = connect(&url, Some(&schema)).await;
            let tx = client.transaction().await.expect("a transaction");
            let options = CreateOptions {
                id: Some(KEY.into()),
                ..CreateOptions::default()
            };
            run_create(
                &host,
                &tx,
                "Ticket",
                own(json!({"title": "Leak"})),
                alice(),
                &options,
            )
            .await
            .expect("created");
            let deleting = Moving {
                inner: PgStore { client: &tx },
                write: "UPDATE ticket SET deleted_at = $1, _version = _version + 1 WHERE id = $2"
                    .into(),
                values: vec![
                    Bind::Text("2026-10-30T00:00:00.000Z".into()),
                    Bind::Text(KEY_COLUMN.into()),
                ],
                moves: 1,
                applies: AtomicUsize::new(0),
            };
            let command =
                crate::tests::command(crate::tests::invoke("transition", json!({"to": "paid"})));
            let refused = refusal(
                run::execute(
                    &host,
                    &deleting,
                    "Ticket",
                    command,
                    &ExecuteOptions::default(),
                )
                .await,
            );
            assert_eq!(refused.code, Code::NotFound);
            assert_eq!(deleting.applies.load(Ordering::SeqCst), 1);
            let row = tx
                .query_one(
                    "SELECT status, _version FROM ticket WHERE id = $1",
                    &[&KEY_COLUMN],
                )
                .await
                .expect("the row reads");
            assert_eq!(
                (row.get::<_, String>(0).as_str(), row.get::<_, i64>(1)),
                ("draft", 2)
            );
            assert!(read_row(&host, &tx, "Ticket", KEY)
                .await
                .expect("read")
                .is_none());
            tx.commit().await.expect("committed");
        })
        .await;
    }
}
