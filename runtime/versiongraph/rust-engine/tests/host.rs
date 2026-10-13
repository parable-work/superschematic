//! The fixed layout inside a synchronous transaction a host holds (D58), on
//! SQLite over plain rusqlite and on Postgres over the synchronous
//! `postgres` client: an engine with no storage of its own runs each
//! operation through `Engine::run_on` over a storage bound for the call,
//! inside the host's transaction, and the host's commit keeps the call's
//! writes and its rollback undoes them. The Postgres tests read
//! SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL and skip without it.
//!
//! The synchronous `postgres` client blocks on a runtime of its own, so
//! these tests run on no tokio runtime.

mod support;

use std::sync::Arc;

use serde_json::json;
use superschematic_versiongraph_engine::fixed::{
    self, ClientError, Dialect, FixedLayout, HostClient, HostTx, Options, SqlValue,
};
use superschematic_versiongraph_engine::{
    CommitOptions, Edits, Engine, Error, KindEdits, Options as EngineOptions, Ref,
};

use support::hosts::{PostgresHost, PostgresSchema};

const COOK: &str = "Cook";
const BREAD: &str = "Bread";

fn adapter(dialect: Dialect) -> Arc<FixedLayout> {
    let options = Options {
        graph: "recipe".to_owned(),
        ..Options::default()
    };
    Arc::new(
        FixedLayout::with_dialect(dialect, &support::descriptor(), options)
            .expect("the fixture's adapter"),
    )
}

fn engine() -> Engine {
    Engine::unbound(
        &support::descriptor(),
        EngineOptions {
            schema_epoch: support::FIXTURE_SCHEMA_EPOCH,
            snapshot_every: support::FIXTURE_SNAPSHOT_EVERY,
            ..EngineOptions::default()
        },
    )
    .expect("the fixture's engine")
}

fn step(key: &str, position: i64) -> Edits {
    let edits = KindEdits {
        upsert: vec![
            json!({"entity_key": key, "position": position, "instruction": key, "timings": {}}),
        ],
        ..KindEdits::default()
    };
    Edits::from([("step".to_owned(), edits)])
}

/// A host that forwards statements and bounds no operation, so each runs
/// in the host's transaction itself, with no transaction control.
struct NoSavepoints<H>(H);

impl<H: HostTx> HostTx for NoSavepoints<H> {
    fn query(&mut self, sql: &str, args: &[SqlValue]) -> Result<Vec<Vec<SqlValue>>, ClientError> {
        self.0.query(sql, args)
    }

    fn execute(&mut self, sql: &str, args: &[SqlValue]) -> Result<u64, ClientError> {
        self.0.execute(sql, args)
    }
}

/// The first call: a primary line, a second of the same name refused
/// (rolled back alone in the operation's savepoint, the host's transaction
/// going on), a change set, a save and a commit.
fn first_call(adapter: &Arc<FixedLayout>, engine: &Engine, host: &mut dyn HostTx) -> (Ref, Ref) {
    let storage = adapter.bind(HostClient::new(host));
    let main = engine
        .run_on(&storage, |e| e.create_primary(COOK, BREAD, "main"))
        .expect("create the primary line");
    let taken = engine.run_on(&storage, |e| e.create_primary(COOK, BREAD, "main"));
    assert!(matches!(taken, Err(Error::NameTaken(_))), "{taken:?}");
    let draft = engine
        .run_on(&storage, |e| e.branch(COOK, &main.id, "draft"))
        .expect("branch");
    let edits = step("Mix", 1);
    let saved = engine
        .run_on(&storage, |e| e.save(COOK, &draft.id, draft.version, &edits))
        .expect("save");
    let options = CommitOptions {
        message: "mix".to_owned(),
        tag: true,
    };
    let committed = engine
        .run_on(&storage, |e| {
            e.commit(COOK, &draft.id, saved.ref_.version, &options)
        })
        .expect("commit");
    let commit = committed.commit.expect("a commit");
    assert_eq!(commit.sequence, Some(1));
    (main, committed.ref_)
}

/// The second call, which the host rolls back: a change set of its own.
fn second_call(adapter: &Arc<FixedLayout>, engine: &Engine, host: &mut dyn HostTx, main: &Ref) {
    let storage = adapter.bind(HostClient::new(host));
    engine
        .run_on(&storage, |e| e.branch(COOK, &main.id, "dropped"))
        .expect("branch");
}

/// The third call reads what the first wrote and the second did not.
fn third_call(adapter: &Arc<FixedLayout>, engine: &Engine, host: &mut dyn HostTx, draft: &Ref) {
    let storage = adapter.bind(HostClient::new(host));
    let tree = engine
        .run_on(&storage, |e| e.compose(&draft.id))
        .expect("compose");
    assert_eq!(tree.tree["step"].len(), 1, "{:?}", tree.tree);
    assert_eq!(tree.tree["step"][0]["instruction"], "Mix");
    let history = engine
        .run_on(&storage, |e| e.history(&draft.id))
        .expect("history");
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].message, "mix");
    assert_eq!(history[0].content_hash, tree.content_hash);
    let materialized = engine
        .run_on(&storage, |e| e.materialize(&history[0].id))
        .expect("materialize");
    assert_eq!(materialized.content_hash, tree.content_hash);
}

/// The names of the graph's refs, as the host reads them.
fn ref_names(host: &mut dyn HostTx) -> Vec<String> {
    host.query("SELECT name FROM \"graph_ref\" ORDER BY name", &[])
        .expect("read the refs")
        .into_iter()
        .map(|row| match row.as_slice() {
            [SqlValue::Text(name)] => name.clone(),
            other => panic!("a ref's name is {other:?}"),
        })
        .collect()
}

#[cfg(feature = "rusqlite")]
#[test]
fn a_hosts_transaction_on_sqlite_runs_each_operation_inside_it() {
    use superschematic_versiongraph_engine::sqlite;
    use support::hosts::RusqliteHost;

    let mut connection = rusqlite::Connection::open_in_memory().expect("open a database");
    connection
        .execute_batch("PRAGMA foreign_keys = ON")
        .expect("foreign keys on");
    for statement in sqlite::layout(&fixed::default_table_name).expect("the layout") {
        connection
            .execute_batch(&statement)
            .expect("create the layout");
    }
    let (adapter, engine) = (adapter(Dialect::Sqlite), engine());
    let tx = connection.transaction().expect("the host's transaction");
    let (main, draft) = first_call(&adapter, &engine, &mut RusqliteHost { connection: &tx });
    tx.commit().expect("commit the first call");
    let tx = connection.transaction().expect("the host's transaction");
    second_call(
        &adapter,
        &engine,
        &mut RusqliteHost { connection: &tx },
        &main,
    );
    tx.rollback().expect("roll back the second call");
    let tx = connection.transaction().expect("the host's transaction");
    let mut host = RusqliteHost { connection: &tx };
    third_call(&adapter, &engine, &mut host, &draft);
    assert_eq!(ref_names(&mut host), ["draft", "main"]);
    // Without savepoints, a call whose operation fails on a taken name
    // writes nothing of it, and the call goes on.
    let mut host = NoSavepoints(RusqliteHost { connection: &tx });
    let storage = adapter.bind(HostClient::new(&mut host));
    let taken = engine.run_on(&storage, |e| e.create_primary(COOK, BREAD, "main"));
    assert!(matches!(taken, Err(Error::NameTaken(_))), "{taken:?}");
    engine
        .run_on(&storage, |e| e.create_primary(COOK, "Soup", "main"))
        .expect("the call goes on");
    assert_eq!(ref_names(&mut host), ["draft", "main", "main"]);
}

#[test]
fn a_hosts_transaction_on_postgres_runs_each_operation_inside_it() {
    let Some(dsn) =
        support::database("a_hosts_transaction_on_postgres_runs_each_operation_inside_it")
    else {
        return;
    };
    let schema = PostgresSchema::create(&dsn, "vg_rust_host");
    let mut client = schema.connect();
    // The layout's statements run again without harm, its keys dropped and
    // added again.
    let layout = fixed::layout(Dialect::Postgres, &fixed::default_table_name).expect("the layout");
    for _ in 0..2 {
        let mut tx = client.transaction().expect("a transaction");
        for statement in &layout {
            tx.batch_execute(statement).expect("create the layout");
        }
        tx.commit().expect("commit the layout");
    }
    let (adapter, engine) = (adapter(Dialect::Postgres), engine());
    let mut tx = client.transaction().expect("the host's transaction");
    let (main, draft) = first_call(
        &adapter,
        &engine,
        &mut PostgresHost {
            transaction: &mut tx,
        },
    );
    tx.commit().expect("commit the first call");
    let mut tx = client.transaction().expect("the host's transaction");
    second_call(
        &adapter,
        &engine,
        &mut PostgresHost {
            transaction: &mut tx,
        },
        &main,
    );
    tx.rollback().expect("roll back the second call");
    let mut tx = client.transaction().expect("the host's transaction");
    let mut host = PostgresHost {
        transaction: &mut tx,
    };
    third_call(&adapter, &engine, &mut host, &draft);
    assert_eq!(ref_names(&mut host), ["draft", "main"]);
    // Without savepoints, a call whose operation finds a name taken writes
    // nothing of it and leaves the transaction usable: the insert does
    // nothing on the conflict rather than fail, which would abort it.
    let mut host = NoSavepoints(host);
    let storage = adapter.bind(HostClient::new(&mut host));
    let taken = engine.run_on(&storage, |e| e.create_primary(COOK, BREAD, "main"));
    assert!(matches!(taken, Err(Error::NameTaken(_))), "{taken:?}");
    engine
        .run_on(&storage, |e| e.create_primary(COOK, "Soup", "main"))
        .expect("the call goes on");
    assert_eq!(ref_names(&mut host), ["draft", "main", "main"]);
    tx.commit().expect("commit the third call");
}
