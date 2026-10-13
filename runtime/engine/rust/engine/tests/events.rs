//! The event log, the cases of `test/events.test.ts` that need no
//! behavior: one event per write, appended in the write's transaction,
//! never changed, read in pages from a cursor or from the head, kept to a
//! namespace and the shared publishes it reaches, with its arguments
//! checked; on SQLite and, with the variable set, on Postgres.
//!
//! The TypeScript engine's watchers were callbacks in its one process; here
//! a waiter blocks on the storage's notifier ([`Events::wait`],
//! [`Events::watch`]), which a commit in any process moves on Postgres, so
//! the watcher cases are written for that. Two cases are the Rust engine's
//! own: a reader paging one namespace while writers in it, in another
//! namespace and in the shared one commit at once, on two engines (two
//! processes on Postgres), sees every event of its namespace once and in
//! order, which a Postgres identity drawn before its commit would break
//! without the per-namespace writer lock (D58); and a watch on one engine
//! wakes for another's commit.
//!
//! milestone 5: the kinds, behaviors and exclude filters over operation
//! events (test.Counter, test.Tally).

mod support;

use std::collections::BTreeSet;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::Arc;
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::storage::{Lock, SqlValue};
use superschematic_engine::{
    AccessPolicy, AccessRequest, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode,
    EngineEvent, EventPage, EventsAfter, NamespaceOptions, Policy, Principal, ReadEvents,
    SchemaTarget, WriteOptions,
};
use support::{alice, code, order_document, refusal, schema_document, targets, with};

fn bob() -> Principal {
    Principal::new("bob", Vec::<String>::new())
}

fn note_document() -> Value {
    schema_document(
        "Note",
        json!([{"name": "body", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

fn define(engine: &Engine, document: &Value, namespace: Option<&str>) {
    engine
        .schemas()
        .define(
            &alice(),
            document,
            DefineOptions {
                namespace,
                source: None,
            },
        )
        .unwrap();
}

fn publish(engine: &Engine, name: &str, namespace: Option<&str>) {
    engine
        .schemas()
        .publish(&alice(), name, SchemaTarget { namespace })
        .unwrap();
}

fn create(engine: &Engine, schema: &str, data: Value, id: &str, namespace: Option<&str>) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                namespace,
                behaviors: None,
            },
        )
        .unwrap();
}

fn read(engine: &Engine, principal: &Principal, options: ReadEvents<'_>) -> EventPage {
    engine.events().read(principal, &options).unwrap()
}

fn all(engine: &Engine) -> Vec<EngineEvent> {
    read(engine, &alice(), ReadEvents::default()).events
}

fn shape(
    event: &EngineEvent,
) -> (
    String,
    String,
    String,
    Option<String>,
    Option<i64>,
    Option<i64>,
    String,
) {
    (
        event.kind.clone(),
        event.namespace.clone(),
        event.schema.clone(),
        event.instance_id.clone(),
        event.seq,
        event.version,
        event.actor.clone(),
    )
}

fn options(names: &[&str], shared: Option<&str>) -> NamespaceOptions {
    NamespaceOptions {
        names: names.iter().map(|name| (*name).to_owned()).collect(),
        shared: shared.map(str::to_owned),
    }
}

#[test]
fn each_write_appends_one_event_with_its_namespace_schema_instance_sequence_version_actor_time_and_change(
) {
    for target in targets("each_write_appends_one_event") {
        let engine = target.open_with(|options| options.clock = Clock::Manual(101));
        engine.clock().set(102).unwrap();
        let defined = engine
            .schemas()
            .define(&alice(), order_document(), DefineOptions::default())
            .unwrap();
        engine.clock().set(103).unwrap();
        publish(&engine, "Order", None);
        engine.clock().set(104).unwrap();
        create(
            &engine,
            "Order",
            json!({"title": "Desk", "quantity": 1}),
            "o1",
            None,
        );
        engine.clock().set(105).unwrap();
        engine
            .instances()
            .update(
                &bob(),
                "Order",
                "o1",
                &json!({"quantity": null, "status": "open"}),
                WriteOptions::default(),
            )
            .unwrap();
        engine.clock().set(106).unwrap();
        engine
            .instances()
            .delete(&bob(), "Order", "o1", WriteOptions::default())
            .unwrap();

        let EventPage { events, next, more } = read(&engine, &alice(), ReadEvents::default());
        let s = |text: &str| text.to_owned();
        assert_eq!(
            events.iter().map(shape).collect::<Vec<_>>(),
            [
                (
                    s("define"),
                    s("default"),
                    s("Order"),
                    None,
                    None,
                    None,
                    s("alice")
                ),
                (
                    s("publish"),
                    s("default"),
                    s("Order"),
                    None,
                    None,
                    Some(1),
                    s("alice")
                ),
                (
                    s("create"),
                    s("default"),
                    s("Order"),
                    Some(s("o1")),
                    Some(1),
                    Some(1),
                    s("alice")
                ),
                (
                    s("update"),
                    s("default"),
                    s("Order"),
                    Some(s("o1")),
                    Some(2),
                    Some(1),
                    s("bob")
                ),
                (
                    s("delete"),
                    s("default"),
                    s("Order"),
                    Some(s("o1")),
                    Some(3),
                    Some(1),
                    s("bob")
                ),
            ]
        );
        // A define carries the draft's hash, not its document; the publish
        // carries the document it made live.
        assert_eq!(
            events
                .iter()
                .map(|event| event.change.clone())
                .collect::<Vec<_>>(),
            [
                json!({"hash": defined.hash}),
                defined.document.clone(),
                json!({"data": {"title": "Desk", "quantity": 1}, "behaviors": {}}),
                json!({"data": {"quantity": null, "status": "open"}}),
                Value::Null,
            ]
        );
        assert!(
            events.iter().all(|event| event.service.is_none()),
            "no service made these calls"
        );
        let cursors: Vec<i64> = events.iter().map(|event| event.cursor).collect();
        assert!(cursors.windows(2).all(|pair| pair[0] < pair[1]));
        assert_eq!(
            events.iter().map(|event| event.at).collect::<Vec<_>>(),
            [102, 103, 104, 105, 106]
        );
        assert_eq!((next, more), (cursors[4], false));
        // The event as a client reads it.
        assert_eq!(
            serde_json::to_value(&events[3]).unwrap(),
            json!({"cursor": cursors[3], "kind": "update", "namespace": "default", "schema": "Order", "instanceId": "o1", "seq": 2, "version": 1, "actor": "bob", "at": 105, "change": {"data": {"quantity": null, "status": "open"}}})
        );
        // The log holds what JSON.stringify writes.
        assert_eq!(
            support::texts(
                engine.db(),
                "SELECT change FROM engine_events WHERE kind = 'update'"
            ),
            [r#"{"data":{"quantity":null,"status":"open"}}"#]
        );
    }
}

// carol may read but not define or publish; dave may define and read
// nothing; alice may do everything.
struct ReadersAndDefiners;

impl AccessPolicy for ReadersAndDefiners {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        let subject = request.principal().subject.as_str();
        subject == "alice"
            || (subject == "carol" && request.action() == "read")
            || (subject == "dave" && request.action() == "define")
    }
}

#[test]
fn a_define_appends_an_event_with_the_draft_hash_each_time_which_a_reader_of_the_schema_reads() {
    for target in targets("a_define_appends_an_event_with_the_draft_hash_each_time") {
        let engine = target.open_with(|engine| {
            engine.policy = Some(Policy::custom(ReadersAndDefiners));
            engine.namespaces = options(&["default", "common"], Some("common"));
        });
        let carol = Principal::new("carol", Vec::<String>::new());
        let dave = Principal::new("dave", Vec::<String>::new());
        let first = engine
            .schemas()
            .define(&dave, order_document(), DefineOptions::default())
            .unwrap();
        let second = engine
            .schemas()
            .define(
                &dave,
                with(&order_document(), "description", json!("Orders.")),
                DefineOptions::default(),
            )
            .unwrap();
        define(&engine, &note_document(), Some("common"));
        assert_ne!(first.hash, second.hash);

        let defines = read(&engine, &carol, ReadEvents::default()).events;
        assert_eq!(
            defines
                .iter()
                .map(|event| (event.kind.as_str(), event.schema.as_str(), event.version, event.actor.as_str(), event.change.clone()))
                .collect::<Vec<_>>(),
            [
                ("define", "Order", None, "dave", json!({"hash": first.hash})),
                ("define", "Order", None, "dave", json!({"hash": second.hash})),
            ],
            "a draft in the shared namespace changes nothing this namespace reaches until it is published"
        );
        assert_eq!(
            engine
                .schemas()
                .draft(&carol, "Order", SchemaTarget::default())
                .unwrap()
                .unwrap()
                .hash,
            second.hash
        );
        assert_eq!(
            code(engine.events().read(
                &dave,
                &ReadEvents {
                    schema: Some("Order"),
                    ..ReadEvents::default()
                }
            )),
            EngineErrorCode::Forbidden
        );
        assert!(
            read(&engine, &dave, ReadEvents::default())
                .events
                .is_empty(),
            "defining is not reading"
        );
        assert_eq!(
            read(
                &engine,
                &alice(),
                ReadEvents {
                    namespace: Some("common"),
                    ..ReadEvents::default()
                }
            )
            .events
            .iter()
            .map(|event| (event.kind.as_str(), event.schema.as_str()))
            .collect::<Vec<_>>(),
            [("define", "Note")]
        );
        // A refused define appends nothing.
        assert!(engine
            .schemas()
            .define(
                &dave,
                json!({"kind": "General", "name": "Order", "types": {}}),
                DefineOptions::default()
            )
            .is_err());
        assert!(engine
            .schemas()
            .define(&carol, order_document(), DefineOptions::default())
            .is_err());
        assert_eq!(read(&engine, &carol, ReadEvents::default()).events.len(), 2);
    }
}

#[test]
fn a_delete_keeps_the_events_before_it_and_a_re_created_id_continues_its_sequence() {
    for target in targets("a_delete_keeps_the_events_before_it") {
        let engine = target.open();
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        engine
            .instances()
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .unwrap();
        create(&engine, "Order", json!({"title": "Chair"}), "o1", None);
        let one = |after: Option<EventsAfter>, limit: Option<i64>| ReadEvents {
            schema: Some("Order"),
            instance_id: Some("o1"),
            after,
            limit,
            ..ReadEvents::default()
        };
        let history = read(&engine, &alice(), one(None, None)).events;
        assert_eq!(
            history
                .iter()
                .map(|event| (event.kind.as_str(), event.seq))
                .collect::<Vec<_>>(),
            [
                ("create", Some(1)),
                ("update", Some(2)),
                ("delete", Some(3)),
                ("create", Some(4))
            ]
        );
        let later = read(
            &engine,
            &alice(),
            one(Some(EventsAfter::Cursor(history[1].cursor)), Some(1)),
        );
        assert_eq!(
            later
                .events
                .iter()
                .map(|event| event.seq)
                .collect::<Vec<_>>(),
            [Some(3)]
        );
        assert_eq!((later.next, later.more), (history[2].cursor, true));
    }
}

#[test]
fn a_refused_write_appends_no_event() {
    for target in targets("a_refused_write_appends_no_event") {
        let engine = target.open();
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        let before = all(&engine).len();
        let instances = engine.instances();
        assert!(instances
            .create(
                &alice(),
                "Order",
                &json!({"title": 3}),
                CreateOptions::default()
            )
            .is_err());
        assert!(instances
            .create(
                &alice(),
                "Order",
                &json!({"title": "Desk"}),
                CreateOptions {
                    id: Some("o1"),
                    ..CreateOptions::default()
                }
            )
            .is_err());
        assert!(instances
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": null}),
                WriteOptions::default()
            )
            .is_err());
        assert_eq!(all(&engine).len(), before);
        // A draft that matches the live version is still a define; the
        // publish that then mints nothing appends nothing.
        define(&engine, &order_document(), None);
        assert_eq!(all(&engine).len(), before + 1);
        assert!(
            !engine
                .schemas()
                .publish(&alice(), "Order", SchemaTarget::default())
                .unwrap()
                .published
        );
        assert_eq!(all(&engine).len(), before + 1);
    }
}

#[test]
fn the_log_is_append_only() {
    for target in targets("the_log_is_append_only") {
        let engine = target.open();
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        for sql in [
            "UPDATE engine_events SET actor = 'mallory'",
            "DELETE FROM engine_events",
        ] {
            let refused = engine
                .db()
                .write(Lock::Catalog, |tx| tx.run(sql, &[]).map(|_| ()))
                .unwrap_err();
            assert!(
                refused.to_string().contains("engine_events is append-only"),
                "{refused}"
            );
        }
        assert_eq!(all(&engine)[0].actor, "alice");
    }
}

#[test]
fn reading_is_paged_from_a_cursor() {
    for target in targets("reading_is_paged_from_a_cursor") {
        let engine = target.open();
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        for id in ["o1", "o2", "o3", "o4"] {
            create(&engine, "Order", json!({"title": id}), id, None);
        }
        let page = |after: i64| {
            read(
                &engine,
                &alice(),
                ReadEvents {
                    after: Some(EventsAfter::Cursor(after)),
                    limit: Some(2),
                    ..ReadEvents::default()
                },
            )
        };
        let first = read(
            &engine,
            &alice(),
            ReadEvents {
                limit: Some(2),
                ..ReadEvents::default()
            },
        );
        assert_eq!(
            first
                .events
                .iter()
                .map(|event| event.kind.as_str())
                .collect::<Vec<_>>(),
            ["define", "publish"]
        );
        assert!(first.more);
        let ids = |page: &EventPage| {
            page.events
                .iter()
                .map(|event| event.instance_id.clone().unwrap_or_default())
                .collect::<Vec<_>>()
        };
        let second = page(first.next);
        assert_eq!(ids(&second), ["o1", "o2"]);
        assert!(second.more);
        let third = page(second.next);
        assert_eq!(ids(&third), ["o3", "o4"]);
        assert!(!third.more);
        let end = read(
            &engine,
            &alice(),
            ReadEvents {
                after: Some(EventsAfter::Cursor(third.next)),
                ..ReadEvents::default()
            },
        );
        assert_eq!(
            end,
            EventPage {
                events: Vec::new(),
                next: third.next,
                more: false
            }
        );
        create(&engine, "Order", json!({"title": "o5"}), "o5", None);
        let after = read(
            &engine,
            &alice(),
            ReadEvents {
                after: Some(EventsAfter::Cursor(third.next)),
                ..ReadEvents::default()
            },
        );
        assert_eq!(ids(&after), ["o5"]);
    }
}

#[test]
fn reads_keep_to_a_namespace_and_the_shared_publishes_it_reaches_and_filter_by_schema_and_instance()
{
    for target in targets("reads_keep_to_a_namespace_and_the_shared_publishes_it_reaches") {
        let engine = target
            .open_with(|engine| engine.namespaces = options(&["east", "common"], Some("common")));
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        define(&engine, &note_document(), Some("common"));
        publish(&engine, "Note", Some("common"));
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        create(&engine, "Order", json!({"title": "Lamp"}), "o2", None);
        create(&engine, "Note", json!({"body": "hello"}), "n1", None);
        create(&engine, "Note", json!({"body": "east"}), "n1", Some("east"));
        let listed = |options: ReadEvents<'_>| {
            read(&engine, &alice(), options)
                .events
                .iter()
                .map(|event| {
                    (
                        event.kind.clone(),
                        event.namespace.clone(),
                        event.schema.clone(),
                        event.instance_id.clone(),
                    )
                })
                .collect::<Vec<_>>()
        };
        let row = |kind: &str, namespace: &str, schema: &str, id: Option<&str>| {
            (
                kind.to_owned(),
                namespace.to_owned(),
                schema.to_owned(),
                id.map(str::to_owned),
            )
        };
        // A publish is logged in the namespace that holds the schema, and
        // every namespace that looks names up there reads it too.
        assert_eq!(
            listed(ReadEvents::default()),
            [
                row("define", "default", "Order", None),
                row("publish", "default", "Order", None),
                row("publish", "common", "Note", None),
                row("create", "default", "Order", Some("o1")),
                row("create", "default", "Order", Some("o2")),
                row("create", "default", "Note", Some("n1")),
            ]
        );
        assert_eq!(
            listed(ReadEvents {
                namespace: Some("common"),
                ..ReadEvents::default()
            }),
            [
                row("define", "common", "Note", None),
                row("publish", "common", "Note", None)
            ]
        );
        assert_eq!(
            listed(ReadEvents {
                namespace: Some("east"),
                ..ReadEvents::default()
            }),
            [
                row("publish", "common", "Note", None),
                row("create", "east", "Note", Some("n1"))
            ]
        );
        assert_eq!(
            listed(ReadEvents {
                namespace: Some("east"),
                schema: Some("Note"),
                ..ReadEvents::default()
            }),
            [
                row("publish", "common", "Note", None),
                row("create", "east", "Note", Some("n1"))
            ]
        );
        assert!(listed(ReadEvents {
            namespace: Some("east"),
            schema: Some("Order"),
            ..ReadEvents::default()
        })
        .is_empty());
        assert_eq!(
            listed(ReadEvents {
                schema: Some("Order"),
                instance_id: Some("o2"),
                ..ReadEvents::default()
            }),
            [row("create", "default", "Order", Some("o2"))]
        );
    }
}

#[test]
fn a_read_pages_through_a_namespace_and_the_shared_publishes_in_cursor_order() {
    for target in targets("a_read_pages_through_a_namespace_and_the_shared_publishes") {
        let engine = target
            .open_with(|engine| engine.namespaces = options(&["east", "common"], Some("common")));
        define(&engine, &note_document(), Some("common"));
        publish(&engine, "Note", Some("common"));
        // The shared namespace's own instances are not the other
        // namespaces' events.
        create(
            &engine,
            "Note",
            json!({"body": "shared"}),
            "c1",
            Some("common"),
        );
        create(&engine, "Note", json!({"body": "one"}), "n1", Some("east"));
        define(
            &engine,
            &with(&note_document(), "description", json!("second")),
            Some("common"),
        );
        publish(&engine, "Note", Some("common"));
        create(&engine, "Note", json!({"body": "two"}), "n2", Some("east"));
        create(
            &engine,
            "Note",
            json!({"body": "three"}),
            "n3",
            Some("east"),
        );

        let mut seen = Vec::new();
        let mut after = 0;
        for _ in 0..10 {
            let page = read(
                &engine,
                &alice(),
                ReadEvents {
                    namespace: Some("east"),
                    after: Some(EventsAfter::Cursor(after)),
                    limit: Some(2),
                    ..ReadEvents::default()
                },
            );
            assert!(page.events.len() <= 2);
            seen.extend(page.events.iter().map(|event| {
                (
                    event.kind.clone(),
                    event
                        .instance_id
                        .clone()
                        .map_or_else(|| json!(event.version), Value::from),
                )
            }));
            after = page.next;
            if !page.more {
                break;
            }
        }
        assert_eq!(
            seen,
            [
                ("publish".to_owned(), json!(1)),
                ("create".to_owned(), json!("n1")),
                ("publish".to_owned(), json!(2)),
                ("create".to_owned(), json!("n2")),
                ("create".to_owned(), json!("n3")),
            ]
        );
    }
}

#[test]
fn a_commit_that_appends_events_moves_the_epoch_after_it_commits_and_a_rolled_back_one_does_not() {
    for target in targets("a_commit_that_appends_events_moves_the_epoch") {
        let engine = target.open();
        let events = engine.events();
        let start = events.epoch();
        define(&engine, &order_document(), None);
        assert_eq!(
            events.epoch(),
            start + 1,
            "a draft appends its define event"
        );
        publish(&engine, "Order", None);
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        assert_eq!(events.epoch(), start + 3);
        // A refused write commits nothing, and moves nothing.
        assert!(engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": 3}),
                CreateOptions::default()
            )
            .is_err());
        assert!(engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": null}),
                WriteOptions::default()
            )
            .is_err());
        assert_eq!(events.epoch(), start + 3);
        // An epoch already passed answers at once; one not yet passed
        // waits out its timeout.
        assert_eq!(events.wait(start, Duration::from_secs(5)), start + 3);
        let waited = Instant::now();
        assert_eq!(events.wait(start + 3, Duration::from_millis(50)), start + 3);
        assert!(waited.elapsed() >= Duration::from_millis(50));
    }
}

#[test]
fn a_watch_returns_the_events_past_its_cursor_as_soon_as_a_commit_appends_them() {
    for target in targets("a_watch_returns_the_events_past_its_cursor") {
        let engine = Arc::new(Engine::open(target.listening_options()).unwrap());
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        // Nothing past the head: the watch waits out its timeout.
        let empty = engine
            .events()
            .watch(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Head),
                    ..ReadEvents::default()
                },
                Duration::from_millis(50),
            )
            .unwrap();
        assert!(empty.events.is_empty());
        let head = engine.events().head().unwrap();
        assert_eq!(empty.next, head);
        // A watch from the head wakes for the next commit, from another
        // thread.
        let watcher = {
            let engine = Arc::clone(&engine);
            thread::spawn(move || {
                engine
                    .events()
                    .watch(
                        &alice(),
                        &ReadEvents {
                            after: Some(EventsAfter::Head),
                            ..ReadEvents::default()
                        },
                        Duration::from_secs(20),
                    )
                    .unwrap()
            })
        };
        thread::sleep(Duration::from_millis(100));
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        let woke = watcher.join().unwrap();
        assert_eq!(
            woke.events
                .iter()
                .map(|event| (event.kind.as_str(), event.instance_id.as_deref()))
                .collect::<Vec<_>>(),
            [("create", Some("o1"))]
        );
        // A watch from a cursor the log has passed reads at once.
        let past = engine
            .events()
            .watch(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Cursor(head)),
                    ..ReadEvents::default()
                },
                Duration::from_secs(20),
            )
            .unwrap();
        assert_eq!(past.events.len(), 1);
    }
}

#[test]
fn a_watch_on_one_engine_wakes_for_another_engines_commit() {
    // On Postgres two engines are two processes of a server, and the
    // commit's pg_notify reaches the other's listener; on SQLite one
    // process writes the file, so this needs Postgres.
    for target in targets("a_watch_on_one_engine_wakes_for_another_engines_commit") {
        if target.dialect == superschematic_engine::storage::Dialect::Sqlite {
            continue;
        }
        let writer = Engine::open(target.listening_options()).unwrap();
        define(&writer, &order_document(), None);
        publish(&writer, "Order", None);
        let reader = Arc::new(Engine::open(target.listening_options()).unwrap());
        let watcher = {
            let reader = Arc::clone(&reader);
            thread::spawn(move || {
                reader
                    .events()
                    .watch(
                        &alice(),
                        &ReadEvents {
                            after: Some(EventsAfter::Head),
                            ..ReadEvents::default()
                        },
                        Duration::from_secs(20),
                    )
                    .unwrap()
            })
        };
        thread::sleep(Duration::from_millis(200));
        create(&writer, "Order", json!({"title": "Desk"}), "o1", None);
        let woke = watcher.join().unwrap();
        assert_eq!(woke.events.len(), 1, "{woke:?}");
    }
}

#[test]
fn a_reader_of_a_namespace_sees_every_event_once_in_order_while_writers_in_it_in_another_and_in_the_shared_one_commit(
) {
    for target in targets("a_reader_of_a_namespace_sees_every_event_once_in_order") {
        let configure = |options: &mut superschematic_engine::EngineOptions| {
            options.namespaces = NamespaceOptions {
                names: vec!["east".into(), "west".into(), "common".into()],
                shared: Some("common".into()),
            }
        };
        let open = || {
            let mut options = target.options();
            configure(&mut options);
            Arc::new(Engine::open(options).unwrap())
        };
        let first = open();
        let second = open();
        for namespace in ["east", "west"] {
            define(&first, &order_document(), Some(namespace));
            publish(&first, "Order", Some(namespace));
        }
        define(&first, &note_document(), Some("common"));
        publish(&first, "Note", Some("common"));
        let start = read(
            &first,
            &alice(),
            ReadEvents {
                namespace: Some("east"),
                ..ReadEvents::default()
            },
        )
        .next;

        const EACH: usize = 40;
        const PUBLISHES: usize = 5;
        let writer = |engine: Arc<Engine>, namespace: &'static str, prefix: &'static str| {
            thread::spawn(move || {
                for index in 0..EACH {
                    create(
                        &engine,
                        "Order",
                        json!({"title": prefix}),
                        &format!("{prefix}{index}"),
                        Some(namespace),
                    );
                }
            })
        };
        let done = Arc::new(AtomicBool::new(false));
        let reader = {
            let engine = Arc::clone(&second);
            let done = Arc::clone(&done);
            thread::spawn(move || {
                let mut seen: Vec<(i64, String, Option<String>)> = Vec::new();
                let mut after = start;
                loop {
                    let finished = done.load(Ordering::SeqCst);
                    let page = engine
                        .events()
                        .read(
                            &alice(),
                            &ReadEvents {
                                namespace: Some("east"),
                                after: Some(EventsAfter::Cursor(after)),
                                limit: Some(7),
                                ..ReadEvents::default()
                            },
                        )
                        .unwrap();
                    seen.extend(page.events.iter().map(|event| {
                        (event.cursor, event.kind.clone(), event.instance_id.clone())
                    }));
                    after = page.next;
                    if finished && !page.more {
                        return seen;
                    }
                }
            })
        };
        let writers = vec![
            writer(Arc::clone(&first), "east", "a"),
            writer(Arc::clone(&second), "east", "b"),
            writer(Arc::clone(&second), "west", "w"),
            {
                let engine = Arc::clone(&first);
                thread::spawn(move || {
                    for index in 0..PUBLISHES {
                        define(
                            &engine,
                            &with(&note_document(), "description", json!(format!("v{index}"))),
                            Some("common"),
                        );
                        publish(&engine, "Note", Some("common"));
                    }
                })
            },
        ];
        for handle in writers {
            handle.join().unwrap();
        }
        done.store(true, Ordering::SeqCst);
        let seen = reader.join().unwrap();
        // Every event of east, and every publish of the shared namespace,
        // once, in cursor order.
        assert!(
            seen.windows(2).all(|pair| pair[0].0 < pair[1].0),
            "out of order: {seen:?}"
        );
        let created: BTreeSet<String> = seen.iter().filter_map(|(_, _, id)| id.clone()).collect();
        assert_eq!(
            created.len(),
            2 * EACH,
            "{} creates seen of {}",
            created.len(),
            2 * EACH
        );
        assert!(created
            .iter()
            .all(|id| id.starts_with('a') || id.starts_with('b')));
        assert_eq!(
            seen.iter().filter(|(_, kind, _)| kind == "publish").count(),
            PUBLISHES
        );
        assert_eq!(seen.len(), 2 * EACH + PUBLISHES);
        // What a later read from the same start returns is what the reader
        // saw as the writers committed.
        let mut again = Vec::new();
        let mut after = start;
        loop {
            let page = read(
                &second,
                &alice(),
                ReadEvents {
                    namespace: Some("east"),
                    after: Some(EventsAfter::Cursor(after)),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            );
            again.extend(
                page.events
                    .iter()
                    .map(|event| (event.cursor, event.kind.clone(), event.instance_id.clone())),
            );
            after = page.next;
            if !page.more {
                break;
            }
        }
        assert_eq!(again, seen);
    }
}

#[test]
fn a_read_checks_its_arguments() {
    for target in targets("a_read_checks_its_arguments") {
        let engine = target.open();
        let strings = |list: &[&str]| {
            Some(
                list.iter()
                    .map(|item| (*item).to_owned())
                    .collect::<Vec<_>>(),
            )
        };
        let cases: Vec<(ReadEvents<'_>, &str)> = vec![
            (ReadEvents { after: Some(EventsAfter::Cursor(-1)), ..ReadEvents::default() }, "an event cursor is a non-negative integer or head, got -1"),
            (ReadEvents { limit: Some(0), ..ReadEvents::default() }, "a page size is an integer from 1 to 500, got 0"),
            (ReadEvents { limit: Some(501), ..ReadEvents::default() }, "a page size is an integer from 1 to 500, got 501"),
            (ReadEvents { instance_id: Some("o1"), ..ReadEvents::default() }, "reading one instance's events needs its schema"),
            (ReadEvents { after: Some(EventsAfter::Head), instance_id: Some("o1"), ..ReadEvents::default() }, "reading one instance's events needs its schema"),
            (ReadEvents { schema: Some("not a name"), ..ReadEvents::default() }, "schema name \"not a name\" must match ^[A-Za-z][A-Za-z0-9_-]{0,127}$"),
            (ReadEvents { kinds: strings(&[]), ..ReadEvents::default() }, "kinds lists no event kinds, so it would keep no event; leave it out to keep every one"),
            (ReadEvents { kinds: strings(&["created"]), ..ReadEvents::default() }, "kinds: \"created\" is not one of create, update, delete, operation, publish, define"),
            (ReadEvents { behaviors: strings(&[]), ..ReadEvents::default() }, "behaviors lists no behavior names, so it would keep no event; leave it out to keep every one"),
            (ReadEvents { behaviors: strings(&["not a name"]), ..ReadEvents::default() }, "behaviors: \"not a name\" is not a behavior name"),
            (ReadEvents { exclude: strings(&["Heartbeat"]), ..ReadEvents::default() }, "exclude: \"Heartbeat\" is not an operation name, camelCase"),
        ];
        for (options, message) in cases {
            let refused = refusal(engine.events().read(&alice(), &options));
            assert_eq!(
                (refused.code(), refused.message()),
                (EngineErrorCode::InvalidArgument, message),
                "{options:?}"
            );
        }
        assert_eq!(
            code(engine.events().read(
                &alice(),
                &ReadEvents {
                    namespace: Some("east"),
                    ..ReadEvents::default()
                }
            )),
            EngineErrorCode::UnknownNamespace
        );
        assert_eq!(
            read(
                &engine,
                &alice(),
                ReadEvents {
                    exclude: strings(&[]),
                    ..ReadEvents::default()
                }
            ),
            EventPage {
                events: Vec::new(),
                next: 0,
                more: false
            },
            "an empty exclude drops nothing"
        );
    }
}

// Allows every question about Order, and none about anything else.
struct OrdersOnly;

impl AccessPolicy for OrdersOnly {
    fn allows(&self, request: &AccessRequest<'_>) -> bool {
        request.schema() == Some("Order")
    }
}

#[test]
fn a_read_from_the_head_returns_no_events_and_the_cursor_of_the_last_one() {
    for target in targets("a_read_from_the_head_returns_no_events") {
        let engine = target.open_with(|engine| engine.policy = Some(Policy::custom(OrdersOnly)));
        let head = |options: ReadEvents<'_>| {
            read(
                &engine,
                &alice(),
                ReadEvents {
                    after: Some(EventsAfter::Head),
                    ..options
                },
            )
        };
        assert_eq!(
            head(ReadEvents::default()),
            EventPage {
                events: Vec::new(),
                next: 0,
                more: false
            }
        );
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        create(&engine, "Order", json!({"title": "Desk"}), "o1", None);
        let last = engine.events().head().unwrap();
        assert_eq!(last, read(&engine, &alice(), ReadEvents::default()).next);
        // The head is the whole log's, whatever the filters keep.
        assert_eq!(
            head(ReadEvents {
                schema: Some("Order"),
                kinds: Some(vec!["delete".into()]),
                ..ReadEvents::default()
            }),
            EventPage {
                events: Vec::new(),
                next: last,
                more: false
            }
        );
        // It still asks what any read asks.
        assert_eq!(
            code(engine.events().read(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Head),
                    schema: Some("Note"),
                    ..ReadEvents::default()
                }
            )),
            EngineErrorCode::Forbidden
        );
        engine
            .instances()
            .update(
                &alice(),
                "Order",
                "o1",
                &json!({"title": "Lamp"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            read(
                &engine,
                &alice(),
                ReadEvents {
                    after: Some(EventsAfter::Cursor(last)),
                    ..ReadEvents::default()
                }
            )
            .events
            .iter()
            .map(|event| (event.kind.as_str(), event.instance_id.as_deref()))
            .collect::<Vec<_>>(),
            [("update", Some("o1"))]
        );
    }
}

#[test]
fn a_read_past_what_retention_pruned_is_cursor_expired_and_one_from_the_start_starts_at_the_floor()
{
    // Retention runs on the runner (milestone 6); a storage the TypeScript
    // engine pruned holds floors, which the log reads already.
    for target in targets("a_read_past_what_retention_pruned_is_cursor_expired") {
        let engine = target.open();
        define(&engine, &order_document(), None);
        publish(&engine, "Order", None);
        for id in ["o1", "o2", "o3"] {
            create(&engine, "Order", json!({"title": id}), id, None);
        }
        let cursors: Vec<i64> = all(&engine).iter().map(|event| event.cursor).collect();
        let floor = cursors[2];
        engine
            .db()
            .write(Lock::Catalog, |tx| {
                tx.run(
                    "INSERT INTO engine_log_floors (namespace, floor, publish_floor, pruned) VALUES ('default', ?, 0, 3)",
                    &[SqlValue::Integer(floor)],
                )?;
                tx.run("DELETE FROM engine_events WHERE namespace = 'default' AND cursor <= ?", &[SqlValue::Integer(floor)])?;
                Ok::<_, superschematic_engine::storage::Error>(())
            })
            .unwrap();
        assert_eq!(engine.events().floor(None).unwrap(), floor);
        let refused = refusal(engine.events().read(
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Cursor(cursors[0])),
                ..ReadEvents::default()
            },
        ));
        assert_eq!(refused.name(), "CursorExpiredError");
        assert_eq!(
            refused.details().unwrap(),
            json!({"after": cursors[0], "floor": floor, "head": cursors[4]})
        );
        let kept = read(&engine, &alice(), ReadEvents::default());
        assert_eq!(
            kept.events
                .iter()
                .map(|event| event.cursor)
                .collect::<Vec<_>>(),
            &cursors[3..]
        );
        assert_eq!(engine.events().head().unwrap(), cursors[4]);
    }
}
