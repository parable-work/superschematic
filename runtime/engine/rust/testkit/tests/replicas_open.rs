//! Engines that open one database and publish to it at once, as replicas
//! of a server do when they start together or deploy a schema (D58):
//!
//! - Engines that open a fresh schema at once (or a fresh SQLite file)
//!   migrate it once between them: every one opens, the ledger records
//!   each migration once, and each sees what the others write.
//! - A replica that opens beside running ones, on a schema that is up to
//!   date, takes no lock: it waits for none of their writes in flight and
//!   holds none of their later writes behind it.
//! - Two publishes of one draft at once mint one version: one publishes,
//!   the other finds the draft published (`published: false`) or gone
//!   (`not_found`), and the version's storage and publish event are there
//!   once.
//! - Two engines on two schemas of a fresh database that publish Search at
//!   once both create the full-text search the database holds once (the
//!   `unaccent` extension, Postgres).
//!
//! Each case runs on Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL`
//! names one, in a schema of its own (the last in a database of its own),
//! and the ones a SQLite file can hold on SQLite too.

mod replicas_support;
mod support;

use std::sync::{Barrier, Mutex, PoisonError};
use std::thread;
use std::time::Duration;

use serde_json::{json, Value};

use replicas_support::{as_replica, pause, replica_name, set_busy_timeout, wait_for, Admin, Pause};
use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, PublishResult, SchemaTarget,
};
use support::{alice, count, fixtures, publish, schema_document, targets};

/// Engines that race.
const ENGINES: usize = 4;

fn item_document(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Item",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

fn create_in(
    engine: &Engine,
    namespace: Option<&str>,
    id: &str,
) -> superschematic_engine::Result<()> {
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({ "title": id }),
            CreateOptions {
                namespace,
                id: Some(id),
                ..Default::default()
            },
        )
        .map(|_| ())
}

#[test]
fn engines_that_open_a_fresh_storage_at_once_migrate_it_once() {
    for target in targets("open_at_once") {
        let start = Barrier::new(ENGINES);
        let engines: Vec<Engine> = thread::scope(|scope| {
            let handles: Vec<_> = (0..ENGINES)
                .map(|n| {
                    let (target, start) = (&target, &start);
                    scope.spawn(move || {
                        let mut options = target.workqueue_options();
                        as_replica(&mut options, &replica_name(&format!("opener{n}")), false);
                        start.wait();
                        Engine::open(options)
                    })
                })
                .collect();
            handles
                .into_iter()
                .map(|handle| handle.join().expect("an opener").expect("open"))
                .collect()
        });
        // The ledger holds each of the engine's migrations once, in order.
        let versions: Vec<u32> = engines[0]
            .db()
            .applied_migrations("engine")
            .expect("the ledger")
            .iter()
            .map(|row| row.version)
            .collect();
        assert!(!versions.is_empty());
        assert_eq!(
            versions,
            (1..=versions.len() as u32).collect::<Vec<_>>(),
            "{:?}",
            target.dialect
        );
        assert_eq!(
            count(
                &engines[0],
                "SELECT COUNT(*) FROM engine_migrations WHERE owner = 'engine'"
            ),
            versions.len() as i64
        );
        // Each works, and sees what the others wrote.
        publish(&engines[0], &item_document(json!([])), None);
        for (n, engine) in engines.iter().enumerate() {
            create_in(engine, None, &format!("i{n}")).expect("create");
        }
        for engine in &engines {
            assert_eq!(
                count(engine, "SELECT COUNT(*) FROM engine_instances"),
                ENGINES as i64,
                "{:?}",
                target.dialect
            );
        }
    }
}

#[test]
fn a_replica_that_opens_beside_running_ones_holds_none_of_their_writes_up() {
    for target in targets("open_beside_running") {
        if target.dialect != Dialect::Postgres {
            // One SQLite writer at a time: an open's migration check waits
            // for it on the busy timeout as any writer does.
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let paused = Pause::default();
        let hold = paused.hold();
        let behaviors = || fixtures().with(paused.clone());
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("holder"), false);
        let holder = Engine::open(options).expect("the holder");
        // The other replica gives up on a lock after 300 ms.
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("other"), false);
        set_busy_timeout(&mut options, Duration::from_millis(300));
        let other = Engine::open(options).expect("the other");
        // test.Counter keeps tables, so a replica that opens checks its
        // migrations as well as the engine's.
        let document = item_document(json!([{"name": "test.Counter"}, {"name": "test.Pause"}]));
        for namespace in ["a", "b"] {
            holder
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&holder, &document, Some(namespace));
            create_in(&holder, Some(namespace), "i1").expect("create");
        }
        let starter = replica_name("starter");
        thread::scope(|scope| {
            let held = scope.spawn(|| pause(&holder, &alice(), Some("a"), "Item", "i1", "held"));
            assert_eq!(hold.entered(), "held");
            // A writer of a holds its lock, and the catalog's shared.
            let opening = scope.spawn(|| {
                let mut options = target.options(behaviors());
                as_replica(&mut options, &starter, false);
                Engine::open(options)
            });
            wait_for("the new replica opens or waits", || {
                opening.is_finished() || admin.waiting(&starter)
            });
            let opened_beside = opening.is_finished();
            // The running replica's write in b goes on, behind no open.
            let beside = create_in(&other, Some("b"), "beside");
            hold.release();
            held.join().expect("the held call").expect("pause");
            let started = opening.join().expect("the opener").expect("open");
            beside.expect("a write beside the open, not busy");
            assert!(opened_beside, "the open waited for a's writer");
            create_in(&started, Some("a"), "after").expect("the new replica writes");
        });
    }
}

fn publish_result(result: &superschematic_engine::Result<PublishResult>) -> String {
    match result {
        Ok(published) => format!("published {} v{}", published.published, published.version),
        Err(Error::Engine(refused)) => format!("refused {}", refused.code().as_str()),
        Err(other) => format!("failed {other}"),
    }
}

#[test]
fn publishes_of_one_draft_at_once_mint_one_version() {
    for target in targets("publish_at_once") {
        let engines = [0, 1].map(|n| {
            let mut options = target.core_options();
            as_replica(&mut options, &replica_name(&format!("publisher{n}")), false);
            Engine::open(options).expect("open")
        });
        publish(&engines[0], &item_document(json!([])), None);
        create_in(&engines[0], None, "before").expect("create");
        // The next version composes Search, whose storage it creates.
        let next = item_document(json!([{"name": "Search", "config": {"fields": ["title"]}}]));
        let start = Barrier::new(ENGINES);
        let results: Mutex<Vec<superschematic_engine::Result<PublishResult>>> = Mutex::default();
        thread::scope(|scope| {
            for n in 0..ENGINES {
                let (engine, start, results, next) = (&engines[n % 2], &start, &results, &next);
                scope.spawn(move || {
                    start.wait();
                    let defined = engine.schemas().define(
                        &alice(),
                        next,
                        DefineOptions {
                            namespace: None,
                            source: None,
                        },
                    );
                    let published = defined.and_then(|_| {
                        engine
                            .schemas()
                            .publish(&alice(), "Item", SchemaTarget { namespace: None })
                    });
                    results
                        .lock()
                        .unwrap_or_else(PoisonError::into_inner)
                        .push(published);
                });
            }
        });
        let results = results.into_inner().unwrap_or_else(PoisonError::into_inner);
        let described: Vec<String> = results.iter().map(publish_result).collect();
        let what = format!("{:?}: {described:?}", target.dialect);
        assert_eq!(
            described
                .iter()
                .filter(|r| *r == "published true v2")
                .count(),
            1,
            "one publish minted v2: {what}"
        );
        for result in &results {
            match result {
                Ok(published) => assert_eq!(published.version, 2, "{what}"),
                Err(Error::Engine(refused)) => {
                    assert_eq!(refused.code(), EngineErrorCode::NotFound, "{what}");
                }
                Err(other) => panic!("a publish failed: {other}"),
            }
        }
        for engine in &engines {
            assert_eq!(
                count(
                    engine,
                    "SELECT COUNT(*) FROM engine_schemas WHERE name = 'Item' AND version > 0"
                ),
                2,
                "{what}"
            );
            assert_eq!(
                count(
                    engine,
                    "SELECT COUNT(*) FROM engine_schemas WHERE name = 'Item' AND version = 0"
                ),
                0,
                "no draft left: {what}"
            );
            assert_eq!(
                count(engine, "SELECT COUNT(*) FROM engine_events WHERE kind = 'publish' AND schema = 'Item' AND version = 2"),
                1,
                "{what}"
            );
            assert_eq!(
                count(
                    engine,
                    "SELECT COUNT(*) FROM engine_behaviors WHERE name = 'Search'"
                ),
                1,
                "{what}"
            );
        }
        // Both write with the new version, Search indexing each.
        for (n, engine) in engines.iter().enumerate() {
            create_in(engine, None, &format!("after{n}")).expect("create");
        }
        for n in 0..2 {
            let found = engines[1 - n]
                .instances()
                .invoke_schema(
                    &alice(),
                    "Item",
                    "search",
                    &json!({ "query": format!("after{n}") }),
                    None,
                )
                .expect("search");
            assert_eq!(found["items"].as_array().map(Vec::len), Some(1), "{what}");
        }
    }
}

// The database-wide case, on Postgres alone: a database of its own.
#[cfg(feature = "postgres")]
mod fresh_database {
    use super::*;

    // A database of the test's own on the Postgres the variable names, dropped
    // when it goes out of scope.
    struct OwnDatabase {
        admin_url: String,
        name: String,
        url: String,
    }

    impl OwnDatabase {
        fn create(url: &str, name: &str) -> OwnDatabase {
            let mut client = postgres::Client::connect(url, postgres::NoTls).expect("connect");
            client
                .batch_execute(&format!("CREATE DATABASE {name}"))
                .expect("create the database");
            // The URL's database is the path after the host, before any query.
            let (base, query) = url.split_once('?').unwrap_or((url, ""));
            let host = &base[..base.rfind('/').expect("a database in the URL")];
            let url = if query.is_empty() {
                format!("{host}/{name}")
            } else {
                format!("{host}/{name}?{query}")
            };
            OwnDatabase {
                admin_url: url_of(base, query),
                name: name.to_owned(),
                url,
            }
        }
    }

    fn url_of(base: &str, query: &str) -> String {
        if query.is_empty() {
            base.to_owned()
        } else {
            format!("{base}?{query}")
        }
    }

    impl Drop for OwnDatabase {
        fn drop(&mut self) {
            if let Ok(mut client) = postgres::Client::connect(&self.admin_url, postgres::NoTls) {
                let _ = client.batch_execute(&format!(
                    "DROP DATABASE IF EXISTS {} WITH (FORCE)",
                    self.name
                ));
            }
        }
    }

    #[test]
    fn two_schemas_of_a_fresh_database_create_its_full_text_search_at_once() {
        for target in targets("fresh_database_search") {
            if target.dialect != Dialect::Postgres {
                continue;
            }
            let database = OwnDatabase::create(
                target.url().expect("a URL"),
                &support::unique_name("engine_fresh"),
            );
            let start = Barrier::new(2);
            let notes = |name: &str| {
                let mut document = schema_document(
                    name,
                    json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
                    json!({}),
                );
                document["types"][name]["behaviors"] =
                    json!([{"name": "Search", "config": {"fields": ["title"]}}]);
                document
            };
            thread::scope(|scope| {
                let handles: Vec<_> = ["one", "two"]
                    .into_iter()
                    .map(|schema| {
                        let (database, start, notes) = (&database, &start, &notes);
                        scope.spawn(move || {
                            let mut options = superschematic_engine::EngineOptions::new(superschematic_engine::Storage::Postgres {
                                url: database.url.clone(),
                                options: superschematic_engine::storage::PostgresOptions {
                                    schema: schema.to_owned(),
                                    listen: false,
                                    ..Default::default()
                                },
                            });
                            options.policy = Some(superschematic_engine::Policy::allow_all());
                            options.behaviors = superschematic_engine_behaviors::core();
                            let engine = Engine::open(options).expect("open");
                            start.wait();
                            publish(&engine, &notes("Note"), None);
                            engine
                                .instances()
                                .create(
                                    &alice(),
                                    "Note",
                                    &json!({"title": format!("walnut {schema}")}),
                                    CreateOptions::default(),
                                )
                                .expect("create");
                            let found = engine
                                .instances()
                                .invoke_schema(&alice(), "Note", "search", &json!({"query": "walnut"}), None)
                                .expect("search");
                            assert_eq!(found["items"].as_array().map(Vec::len), Some(1), "{schema}");
                            count(&engine, "SELECT COUNT(*) FROM pg_catalog.pg_extension WHERE extname = 'unaccent'")
                        })
                    })
                    .collect();
                for handle in handles {
                    assert_eq!(handle.join().expect("an engine"), 1);
                }
            });
            drop(database);
        }
    }
}
