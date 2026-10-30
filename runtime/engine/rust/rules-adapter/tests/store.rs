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
    Caller, CreateOptions, Error, ExecuteOptions, Host, Owned, Refusal, Row,
};

use common::{iso, p, Param};

/// A database of a dialect, holding the tables of `common::store_ddl`.
enum Db {
    #[cfg(feature = "sqlite")]
    Sqlite(rusqlite::Connection),
    #[cfg(feature = "postgres")]
    Postgres(tokio_postgres::Client),
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
        let own = object(own);
        let options = CreateOptions {
            id: id.map(str::to_owned),
            ..CreateOptions::default()
        };
        in_transaction!(
            self,
            |tx| superschematic_rules_adapter::sqlite::run_create(
                host,
                &tx,
                type_name,
                own,
                alice(),
                &options
            ),
            superschematic_rules_adapter::postgres::run_create(
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
        "rules: the behaviors' layout has no type Invoice; it holds Booking, Item, Loud, Order, Ticket, Timed"
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
/// outbox event or a deadline is refused as internal with nothing written;
/// a prepared create applies nothing.
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
        Refusal::internal("rules: create of Loud writes outbox events, which the adapter takes once each has a table of its own (D103's order of the work, step 3)")
    );
    assert!(db.ids("loud").await.is_empty());
    let timed = refusal(db.create(&host, "Timed", json!({}), Some("d1")).await);
    assert!(
        timed.message.contains("create of Timed writes deadlines"),
        "{}",
        timed.message
    );
    assert!(db.ids("timed").await.is_empty());

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
