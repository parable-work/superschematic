//! A publish that adds a behavior, and so runs its migrations (its tables,
//! and on the instances table its columns), on one engine while another
//! engine writes instances of the schema, as one replica deploys a version
//! while the others serve (D58): Search (its tables and its full-text
//! index), Branches (the version graph's layout) and the work queue's
//! Lease (columns of its own on `engine_instances`; Queue cannot join a
//! schema with instances).
//!
//! The writers race the publish with creates and updates. Every write
//! commits, some after waiting for the publish's lock (refused `busy` and
//! tried again as a client of the 503 would), none half against one
//! version and half against the other: each instance's events carry one
//! seq after another, a write the publish has returned before is the new
//! version's, and the new behavior holds what each such write gave it
//! (Search finds it, Branches and Lease read their fields on it). The
//! writers' connections prepared their statements before
//! the tables changed, and run them after.
//!
//! A replica whose build does not run the behavior the new version
//! composes (an older one, mid-deploy) refuses that version's writes as
//! `unavailable`, writing nothing, and the newer replica writes on.
//!
//! Each case runs on SQLite, two connections to one file, and on Postgres
//! when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, in a schema of
//! its own.

mod replicas_support;
mod support;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::{Mutex, PoisonError};
use std::thread;

use serde_json::{json, Value};

use replicas_support::{all_events, as_replica, replica_name, retrying, wait_for};
use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, GetOptions, InstanceRecord, SchemaTarget, WriteOptions,
};
use support::{alice, publish, targets, Target};

/// Writing threads, on the writer's engine, and the writes each makes after
/// the publish returns.
const WRITERS: usize = 4;
const AFTER: usize = 3;

/// Times a write is tried while the storage is busy.
const TRIES: usize = 500;

fn flow() -> Value {
    json!({
        "states": ["queued", "running", "done"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"}
        ]
    })
}

// A Job schema whose instance type composes Workflow and `more`, with a
// title and a Note type for Branches' kind.
fn jobs(more: &[Value]) -> Value {
    let mut behaviors = vec![json!({"name": "Workflow", "config": flow()})];
    behaviors.extend(more.iter().cloned());
    json!({"kind": "General", "name": "Job", "types": {
        "Job": {
            "name": "Job",
            "role": "EmbeddedStruct",
            "behaviors": behaviors,
            "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
        },
        "Note": {
            "name": "Note",
            "role": "EmbeddedStruct",
            "fields": [{"name": "text", "typeRef": {"name": "string"}, "required": true}]
        }
    }})
}

/// What a case adds, and what it checks of an instance the new version
/// wrote.
struct Case {
    name: &'static str,
    adds: Vec<Value>,
    holds: fn(&Engine, &str, &InstanceRecord),
}

fn cases() -> Vec<Case> {
    vec![
        Case {
            name: "Search",
            adds: vec![json!({"name": "Search", "config": {"fields": ["title"]}})],
            holds: |engine, namespace, record| {
                let found = engine
                    .instances()
                    .invoke_schema(
                        &alice(),
                        "Job",
                        "search",
                        &json!({ "query": record.data["title"] }),
                        Some(namespace),
                    )
                    .expect("search");
                let ids: Vec<&str> = found["items"]
                    .as_array()
                    .expect("items")
                    .iter()
                    .filter_map(|hit| hit["id"].as_str())
                    .collect();
                assert_eq!(ids, [record.id.as_str()], "Search finds {}", record.id);
            },
        },
        Case {
            name: "Branches",
            adds: vec![
                json!({"name": "Branches", "config": {"kinds": {"note": {"type": "Note"}}}}),
            ],
            holds: |_, _, record| {
                assert!(
                    record.behaviors.get("Branches").is_some(),
                    "Branches' fields on {}: {:?}",
                    record.id,
                    record.behaviors
                );
            },
        },
        // Queue cannot join a schema with instances; Lease can.
        Case {
            name: "Lease",
            adds: vec![json!({"name": "Lease"})],
            holds: |_, _, record| {
                assert_eq!(
                    record.behaviors["Lease"]["active"],
                    json!(false),
                    "Lease's fields on {}: {:?}",
                    record.id,
                    record.behaviors
                );
            },
        },
    ]
}

#[test]
fn a_publish_that_adds_a_behavior_beside_another_engines_writes_leaves_every_write_whole() {
    for target in targets("a_publish_beside_writes") {
        for case in cases() {
            publish_beside_writes(&target, &case);
        }
    }
}

fn publish_beside_writes(target: &Target, case: &Case) {
    let what = format!("{:?} {}", target.dialect, case.name);
    let open = |name: &str| {
        let mut options = target.workqueue_options();
        as_replica(&mut options, &replica_name(name), false);
        Engine::open(options).expect("open a replica")
    };
    let (publisher, writer) = (open("publisher"), open("writer"));
    // Each case in a namespace of its own.
    let namespace = case.name.to_lowercase().replace(' ', "-");
    publisher
        .namespaces()
        .create(&alice(), &namespace)
        .expect("namespace");
    let namespace = namespace.as_str();
    publish(&publisher, &jobs(&[]), Some(namespace));
    let written = AtomicUsize::new(0);
    let published = AtomicBool::new(false);
    // Each instance a writer created, and whether the publish had returned
    // before its create.
    let created: Mutex<Vec<(String, bool)>> = Mutex::default();
    thread::scope(|scope| {
        // The writers stop once the publish has returned and they have
        // written after it, or as soon as this thread fails.
        let _stop = StopOnDrop(&published);
        for thread in 0..WRITERS {
            let (writer, written, published, created) = (&writer, &written, &published, &created);
            scope.spawn(move || {
                let mut after = 0;
                for n in 0.. {
                    let late = published.load(Ordering::SeqCst);
                    if late {
                        after += 1;
                        if after > AFTER {
                            return;
                        }
                    }
                    let id = format!("j{thread}_{n}");
                    let options = WriteOptions {
                        namespace: Some(namespace),
                        ..Default::default()
                    };
                    retrying(TRIES, || {
                        writer.instances().create(
                            &alice(),
                            "Job",
                            &json!({ "title": format!("first{thread}x{n}") }),
                            CreateOptions {
                                namespace: Some(namespace),
                                id: Some(&id),
                                ..Default::default()
                            },
                        )
                    })
                    .0
                    .unwrap_or_else(|error| panic!("create {id}: {error}"));
                    retrying(TRIES, || {
                        writer.instances().update(
                            &alice(),
                            "Job",
                            &id,
                            &json!({ "title": format!("title{thread}x{n}") }),
                            options,
                        )
                    })
                    .0
                    .unwrap_or_else(|error| panic!("update {id}: {error}"));
                    created
                        .lock()
                        .unwrap_or_else(PoisonError::into_inner)
                        .push((id, late));
                    written.fetch_add(1, Ordering::SeqCst);
                }
            });
        }
        // Once the writers are under way, the publish adds the behavior.
        wait_for("the writers write", || {
            written.load(Ordering::SeqCst) >= WRITERS * 2
        });
        let mut next = jobs(&case.adds);
        next["name"] = json!("Job");
        retrying(TRIES, || {
            publisher.schemas().define(
                &alice(),
                &next,
                DefineOptions {
                    namespace: Some(namespace),
                    source: None,
                },
            )
        })
        .0
        .unwrap_or_else(|error| panic!("{what}: define: {error}"));
        let result = retrying(TRIES, || {
            publisher.schemas().publish(
                &alice(),
                "Job",
                SchemaTarget {
                    namespace: Some(namespace),
                },
            )
        })
        .0
        .unwrap_or_else(|error| panic!("{what}: publish: {error}"));
        assert_eq!((result.version, result.published), (2, true), "{what}");
        published.store(true, Ordering::SeqCst);
    });
    let created = created.into_inner().unwrap_or_else(PoisonError::into_inner);
    let get_in = |engine: &Engine, id: &str| {
        engine
            .instances()
            .get(
                &alice(),
                "Job",
                id,
                GetOptions {
                    namespace: Some(namespace),
                    ..Default::default()
                },
            )
            .expect("get")
            .unwrap_or_else(|| panic!("no {id}"))
    };
    // Each instance's events: its create and its update, one seq after
    // the other, each of one version.
    let mut by_instance: BTreeMap<String, Vec<(i64, Option<i64>)>> = BTreeMap::new();
    for event in all_events(&writer, Some(namespace)) {
        if let Some(id) = event.instance_id.clone() {
            by_instance
                .entry(id)
                .or_default()
                .push((event.seq.expect("a seq"), event.version));
        }
    }
    let mut late = 0;
    for (id, after_publish) in &created {
        let record = get_in(&writer, id);
        assert_eq!(record, get_in(&publisher, id), "{what}: {id} alike on both");
        let events = &by_instance[id];
        assert_eq!(
            events.iter().map(|(seq, _)| *seq).collect::<Vec<_>>(),
            [1, 2],
            "{what}: {id}"
        );
        assert_eq!(events[1].1, Some(record.version), "{what}: {id}");
        if *after_publish {
            late += 1;
            assert_eq!(
                events
                    .iter()
                    .map(|(_, version)| *version)
                    .collect::<Vec<_>>(),
                [Some(2), Some(2)],
                "{what}: {id} written after the publish"
            );
            (case.holds)(&publisher, namespace, &record);
        }
    }
    assert_eq!(late, WRITERS * AFTER, "{what}");
}

// StopOnDrop sets its flag when dropped, as a failing test unwinds too.
struct StopOnDrop<'a>(&'a AtomicBool);

impl Drop for StopOnDrop<'_> {
    fn drop(&mut self) {
        self.0.store(true, Ordering::SeqCst);
    }
}

#[test]
fn an_engine_that_does_not_run_a_published_behavior_refuses_its_versions_writes_whole() {
    for target in targets("an_engine_without_the_behavior") {
        // The publisher runs the work queue's behaviors, the other replica
        // the core's alone, as an older build would in a rolling deploy.
        let mut options = target.workqueue_options();
        as_replica(&mut options, &replica_name("newer"), false);
        let newer = Engine::open(options).expect("the newer replica");
        let mut options = target.core_options();
        as_replica(&mut options, &replica_name("older"), false);
        let older = Engine::open(options).expect("the older replica");
        publish(&newer, &jobs(&[]), None);
        let create = |engine: &Engine, id: &str| {
            engine.instances().create(
                &alice(),
                "Job",
                &json!({ "title": id }),
                CreateOptions {
                    id: Some(id),
                    ..Default::default()
                },
            )
        };
        create(&older, "before").expect("a write of version 1");
        publish(&newer, &jobs(&[json!({"name": "Lease"})]), None);
        let refused = support::refusal(create(&older, "after"));
        assert_eq!(
            refused.code(),
            superschematic_engine::EngineErrorCode::Unavailable,
            "{:?}: {refused}",
            target.dialect
        );
        let refused = support::refusal(older.instances().update(
            &alice(),
            "Job",
            "before",
            &json!({"title": "changed"}),
            WriteOptions::default(),
        ));
        assert_eq!(
            refused.code(),
            superschematic_engine::EngineErrorCode::Unavailable,
            "{:?}: {refused}",
            target.dialect
        );
        // Nothing of either was written; the newer replica writes on.
        let events = all_events(&newer, None);
        assert!(
            events
                .iter()
                .all(|event| event.instance_id.as_deref() != Some("after")),
            "{:?}",
            target.dialect
        );
        assert_eq!(
            newer
                .instances()
                .get(&alice(), "Job", "before", GetOptions::default())
                .expect("get")
                .expect("before")
                .data["title"],
            json!("before")
        );
        create(&newer, "after").expect("the newer replica writes version 2");
        assert!(!older.poisoned() && !newer.poisoned());
    }
}
