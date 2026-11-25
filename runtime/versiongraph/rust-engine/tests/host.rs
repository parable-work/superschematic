//! The fixed layout inside a synchronous transaction a host holds (D58), on
//! SQLite over plain rusqlite and on Postgres over the synchronous
//! `postgres` client: an engine with no storage of its own runs each
//! operation through `Engine::run_on` over a storage bound for the call,
//! inside the host's transaction, and the host's commit keeps the call's
//! writes and its rollback undoes them. The layout's released rows (D139)
//! and the reads an entity's history is built from (D138) are held to the
//! cases every adapter takes, in both dialects. The Postgres tests read
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
use superschematic_versiongraph_engine::storage::ReleasedWrite;
use superschematic_versiongraph_engine::{
    ready, CommitOptions, Edits, Engine, Error, KindEdits, Options as EngineOptions, Ref,
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

/// The fixed layout's adapter of graph `graph`, in `dialect`.
fn adapter_of(dialect: Dialect, graph: &str) -> Arc<FixedLayout> {
    let options = Options {
        graph: graph.to_owned(),
        ..Options::default()
    };
    Arc::new(
        FixedLayout::with_dialect(dialect, &support::descriptor(), options)
            .expect("the fixture's adapter"),
    )
}

/// Every released part of a graph, as the host reads them from the released
/// table under its default name. Each row's `data` is the canonical object
/// of the part's content columns alone, its members sorted, a column the
/// row lacked null.
fn released_parts(host: &mut dyn HostTx, graph: &str) -> Vec<support::ReleasedPart> {
    let args = [
        SqlValue::Text(graph.to_owned()),
        SqlValue::Text("part".to_owned()),
    ];
    host.query(
        "SELECT root_id, entity_key, commit_id, sequence, data FROM \"graph_released\" WHERE graph = ? AND kind = ?",
        &args,
    )
    .expect("read the released rows")
    .into_iter()
    .map(|row| match row.as_slice() {
        [SqlValue::Text(root), SqlValue::Text(key), SqlValue::Text(commit), SqlValue::Int(sequence), SqlValue::Text(data)] =>
        {
            let members: serde_json::Value = serde_json::from_str(data).expect("data is JSON");
            let text = |name: &str| members[name].as_str().map(str::to_owned);
            let part = support::ReleasedPart {
                root: root.clone(),
                key: key.clone(),
                heading: text("heading").expect("a heading"),
                rank: text("rank").expect("a rank"),
                parent: text("parent_key"),
                commit: commit.clone(),
                sequence: *sequence,
            };
            let canonical =
                json!({"heading": part.heading, "parent_key": part.parent, "rank": part.rank});
            assert_eq!(*data, canonical.to_string(), "the data of {key}");
            part
        }
        other => panic!("a released row is {other:?}"),
    })
    .collect()
}

/// Runs one `write_released` of an adapter in a transaction of its own
/// inside the host's.
fn write_released(
    adapter: &Arc<FixedLayout>,
    host: &mut dyn HostTx,
    kind: &str,
    write: ReleasedWrite,
) -> Result<(), Error> {
    let storage = adapter.bind(HostClient::new(host));
    ready(support::write_released(&storage, kind, write)).expect("one poll")
}

/// `write_released` on the fixed layout (D139), inside a host's
/// transaction: the cases every adapter is held to, each row's `data` the
/// canonical object of its content columns; then what the layout alone
/// refuses, a commit of another root and a commit of another graph; and
/// another graph's rows of the same root and kind, which a replace leaves.
fn released_rows(dialect: Dialect, host: &mut dyn HostTx) {
    fn commits(adapter: &Arc<FixedLayout>, host: &mut dyn HostTx) -> ([String; 2], String) {
        let storage = adapter.bind(HostClient::new(host));
        ready(support::released_commits(&storage))
            .expect("one poll")
            .expect("the commits")
    }
    let (recipe, menu) = (adapter_of(dialect, "recipe"), adapter_of(dialect, "menu"));
    // Another graph of the same tables, with a row of the same root, kind
    // and entity.
    let (menu_bread, _) = commits(&menu, host);
    let of_menu = |commit: &str| ReleasedWrite {
        root: "Bread".to_owned(),
        commit: commit.to_owned(),
        sequence: 1,
        replace: true,
        upsert: vec![json!({"entity_key": "Mix", "rank": "V", "heading": "Today's menu"})],
        ..ReleasedWrite::default()
    };
    write_released(&menu, host, "part", of_menu(&menu_bread[0]))
        .expect("write another graph's row");
    let menus = released_parts(host, "menu");
    assert_eq!(menus.len(), 1, "{menus:?}");

    let (bread, soup) = commits(&recipe, host);
    for case in support::released_cases(&bread, &soup) {
        let result = write_released(&recipe, host, case.kind, case.write.clone());
        support::check_released(&case, result, released_parts(host, "recipe"));
    }
    let left = released_parts(host, "recipe");
    let missing = "Missing".to_owned();
    for (what, adapter, commit) in [
        ("a commit of another root", &recipe, &soup),
        ("a commit of another graph", &menu, &bread[0]),
        ("a commit that does not exist", &recipe, &missing),
    ] {
        match write_released(adapter, host, "part", of_menu(commit)) {
            Err(error) => assert!(
                error.to_string().contains("is not a commit of root"),
                "{what}: {error}"
            ),
            Ok(()) => panic!("{what}: written"),
        }
    }
    assert_eq!(released_parts(host, "recipe"), left);
    assert_eq!(released_parts(host, "menu"), menus);
}

#[cfg(feature = "rusqlite")]
#[test]
fn write_released_rewrites_one_roots_rows_of_a_kind_on_sqlite() {
    use superschematic_versiongraph_engine::sqlite;
    use support::hosts::RusqliteHost;

    let mut connection = rusqlite::Connection::open_in_memory().expect("open a database");
    connection
        .execute_batch("PRAGMA foreign_keys = ON")
        .expect("foreign keys on");
    let name = &fixed::default_table_name;
    let released = sqlite::released_layout(name).expect("the released layout");
    for statement in sqlite::layout(name)
        .expect("the layout")
        .into_iter()
        .chain(released)
    {
        connection
            .execute_batch(&statement)
            .expect("create the layout");
    }
    let tx = connection.transaction().expect("the host's transaction");
    released_rows(Dialect::Sqlite, &mut RusqliteHost { connection: &tx });
    tx.commit().expect("commit");
}

#[test]
fn write_released_rewrites_one_roots_rows_of_a_kind_on_the_fixed_layout_on_postgres() {
    let Some(dsn) = support::database(
        "write_released_rewrites_one_roots_rows_of_a_kind_on_the_fixed_layout_on_postgres",
    ) else {
        return;
    };
    let schema = PostgresSchema::create(&dsn, "vg_rust_host");
    let mut client = schema.connect();
    // The released table's statement runs again without harm, as the
    // layout's do.
    let name = &fixed::default_table_name;
    let mut layout = fixed::layout(Dialect::Postgres, name).expect("the layout");
    layout.extend(fixed::released_layout(Dialect::Postgres, name).expect("the released layout"));
    for _ in 0..2 {
        let mut tx = client.transaction().expect("a transaction");
        for statement in &layout {
            tx.batch_execute(statement).expect("create the layout");
        }
        tx.commit().expect("commit the layout");
    }
    let mut tx = client.transaction().expect("the host's transaction");
    released_rows(
        Dialect::Postgres,
        &mut PostgresHost {
            transaction: &mut tx,
        },
    );
    tx.commit().expect("commit");
}

/// The fixed layout's two reads an entity's history is built from (D138),
/// inside a host's transaction: the cases every adapter is held to, over an
/// adapter whose clock moves at each read, so the refs it writes were
/// created at different times. They run in another graph of the same
/// tables first, whose lines of the same roots the second run does not
/// read.
fn entity_reads(dialect: Dialect, host: &mut dyn HostTx) {
    for graph in ["menu", "recipe"] {
        let now = std::sync::atomic::AtomicI64::new(1_800_000_000_000_000);
        let options = Options {
            graph: graph.to_owned(),
            clock: Some(Arc::new(move || {
                now.fetch_add(1_000_000, std::sync::atomic::Ordering::SeqCst)
            })),
            ..Options::default()
        };
        let adapter = Arc::new(
            FixedLayout::with_dialect(dialect, &support::descriptor(), options)
                .expect("the fixture's adapter"),
        );
        let storage = adapter.bind(HostClient::new(&mut *host));
        ready(support::check_entity_reads(&storage)).expect("one poll");
    }
}

#[cfg(feature = "rusqlite")]
#[test]
fn primary_refs_and_entity_patches_read_an_entitys_history_on_sqlite() {
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
    let tx = connection.transaction().expect("the host's transaction");
    entity_reads(Dialect::Sqlite, &mut RusqliteHost { connection: &tx });
    tx.commit().expect("commit");
}

#[test]
fn primary_refs_and_entity_patches_read_an_entitys_history_on_the_fixed_layout_on_postgres() {
    let Some(dsn) = support::database(
        "primary_refs_and_entity_patches_read_an_entitys_history_on_the_fixed_layout_on_postgres",
    ) else {
        return;
    };
    let schema = PostgresSchema::create(&dsn, "vg_rust_host");
    let mut client = schema.connect();
    let mut tx = client.transaction().expect("a transaction");
    for statement in
        fixed::layout(Dialect::Postgres, &fixed::default_table_name).expect("the layout")
    {
        tx.batch_execute(&statement).expect("create the layout");
    }
    tx.commit().expect("commit the layout");
    let mut tx = client.transaction().expect("the host's transaction");
    entity_reads(
        Dialect::Postgres,
        &mut PostgresHost {
            transaction: &mut tx,
        },
    );
    tx.commit().expect("commit");
}
