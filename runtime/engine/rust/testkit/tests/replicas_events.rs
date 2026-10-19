//! Two engines on one database, as two replicas of a server open it
//! (D58): the event log the one's writers append, read on the other.
//!
//! - A reader on one engine sees every event either engine commits in its
//!   namespace once, in cursor order, while writers race in two
//!   namespaces and some of their writes roll back (on Postgres those
//!   leave gaps in the identity: a reader is held to the order, not to the
//!   numbers).
//! - A read from `head` beside a write in flight misses none of that
//!   write's events (Postgres): the head is global, so a write in another
//!   namespace can commit a cursor past one the write in flight drew, and
//!   the engine reads the head once the namespace's writers are done.
//! - A commit on one engine wakes the other's waiters and streams through
//!   its listener (Postgres), and a stream goes on through a listener whose
//!   session ends (`pg_terminate_backend`): the listener reconnects, wakes
//!   its waiters for what it may have missed, and they catch up by reading.
//! - Without a listener at all (a replica opened with `listen: false`, or
//!   a second process on one SQLite file) a stream catches up by reading at
//!   each heartbeat.
//!
//! Each case runs on Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL`
//! names one, in a schema of its own, and the ones a SQLite file can hold
//! on SQLite too.

mod replicas_support;
mod support;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc;
use std::sync::{Mutex, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use replicas_support::{
    all_events, as_replica, pause, replica_name, retrying, wait_for, Admin, Pause, PATIENCE,
};
use superschematic_engine::api::{start_stream, EventStream, Pull, StreamOptions};
use superschematic_engine::storage::{Dialect, SqlValue};
use superschematic_engine::{
    BehaviorSet, CreateOptions, Engine, EngineEvent, Error, EventsAfter, ReadEvents, WriteOptions,
};
use support::{alice, fixtures, publish, schema_document, targets, Target};

/// Writing threads, alternating the two engines, and the writes each
/// makes.
const WRITERS: usize = 4;
const WRITES: usize = 24;

/// Times a write is tried while the storage is busy.
const TRIES: usize = 200;

fn item_document(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Item",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

fn create_in(engine: &Engine, namespace: Option<&str>, id: &str) {
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
        .unwrap_or_else(|error| panic!("create {id}: {error}"));
}

// open opens a replica of the target with the behaviors given, its
// sessions named `name`, listening or not.
fn open(target: &Target, behaviors: BehaviorSet, name: &str, listen: bool) -> Engine {
    let mut options = target.options(behaviors);
    as_replica(&mut options, name, listen);
    Engine::open(options).expect("open a replica")
}

// The cursors of a list of events are all different and rise.
fn assert_rising(events: &[EngineEvent], what: &str) {
    for pair in events.windows(2) {
        assert!(
            pair[0].cursor < pair[1].cursor,
            "{what}: cursor {} then {}",
            pair[0].cursor,
            pair[1].cursor
        );
    }
}

#[test]
fn a_reader_on_one_engine_sees_every_event_both_commit_once_and_in_order() {
    for target in targets("a_reader_sees_every_event") {
        let paused = Pause::default();
        // A write that appends an event, then fails: it rolls back, and on
        // Postgres leaves a gap in the identity.
        paused.on_pause(|cx, label| {
            if label == "gap" {
                cx.instances()
                    .create("Item", &json!({"title": "rolled back"}), None, None)?;
                return Err(Error::behavior("test.Pause", "the write rolls back"));
            }
            Ok(())
        });
        let behaviors = || fixtures().with(paused.clone());
        let engines = [
            open(&target, behaviors(), &replica_name("writer"), false),
            open(&target, behaviors(), &replica_name("reader"), true),
        ];
        let document = item_document(json!([{"name": "test.Counter"}, {"name": "test.Pause"}]));
        for namespace in ["a", "b"] {
            engines[0]
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&engines[0], &document, Some(namespace));
            create_in(&engines[0], Some(namespace), "i1");
        }
        let done = AtomicBool::new(false);
        let read: [Vec<EngineEvent>; 2] = thread::scope(|scope| {
            let reader = &engines[1];
            let done = &done;
            // A reader per namespace, on the second engine, in small pages,
            // each going on from the last page's next.
            let readers: Vec<_> = ["a", "b"]
                .into_iter()
                .map(|namespace| {
                    scope.spawn(move || {
                        let mut seen = Vec::new();
                        let mut cursor = 0;
                        loop {
                            let finished = done.load(Ordering::SeqCst);
                            let page = reader
                                .events()
                                .watch(
                                    &alice(),
                                    &ReadEvents {
                                        namespace: Some(namespace),
                                        after: Some(EventsAfter::Cursor(cursor)),
                                        limit: Some(7),
                                        ..ReadEvents::default()
                                    },
                                    Duration::from_millis(100),
                                )
                                .expect("watch");
                            cursor = page.next;
                            let empty = page.events.is_empty() && !page.more;
                            seen.extend(page.events);
                            // Once the writers are done, read on to the end.
                            if finished && empty {
                                return seen;
                            }
                        }
                    })
                })
                .collect();
            let writers: Vec<_> = (0..WRITERS)
                .map(|thread| {
                    let engine = &engines[thread % 2];
                    scope.spawn(move || {
                        for n in 0..WRITES {
                            let namespace = if (thread + n) % 2 == 0 { "a" } else { "b" };
                            if n % 5 == 4 {
                                let (failed, _) = retrying(TRIES, || {
                                    pause(engine, &alice(), Some(namespace), "Item", "i1", "gap")
                                });
                                assert!(
                                    matches!(failed, Err(Error::Behavior(_))),
                                    "the write that rolls back: {failed:?}"
                                );
                                continue;
                            }
                            retrying(TRIES, || {
                                engine.instances().invoke(
                                    &alice(),
                                    "Item",
                                    "i1",
                                    "increment",
                                    &json!({}),
                                    WriteOptions {
                                        namespace: Some(namespace),
                                        ..Default::default()
                                    },
                                )
                            })
                            .0
                            .expect("increment");
                        }
                    })
                })
                .collect();
            for writer in writers {
                writer.join().expect("a writer");
            }
            done.store(true, Ordering::SeqCst);
            let mut readers = readers
                .into_iter()
                .map(|reader| reader.join().expect("a reader"));
            [readers.next().unwrap(), readers.next().unwrap()]
        });
        for (namespace, seen) in ["a", "b"].into_iter().zip(&read) {
            let what = format!("{:?} namespace {namespace}", target.dialect);
            // What the reader saw as it went is the log as the writer reads
            // it once the writes are done: every event once, in order.
            assert_eq!(seen, &all_events(&engines[0], Some(namespace)), "{what}");
            assert_rising(seen, &what);
            let parity = usize::from(namespace == "b");
            let expected = (0..WRITERS)
                .flat_map(|thread| (0..WRITES).map(move |n| (thread, n)))
                .filter(|(thread, n)| n % 5 != 4 && (thread + n) % 2 == parity)
                .count();
            let increments = seen
                .iter()
                .filter(|event| event.kind == "operation")
                .count();
            assert_eq!(
                increments, expected,
                "{what}: one event per increment, none for a write that rolled back"
            );
            let seqs: Vec<i64> = seen
                .iter()
                .filter(|event| event.instance_id.as_deref() == Some("i1"))
                .map(|event| event.seq.expect("a seq"))
                .collect();
            assert_eq!(
                seqs,
                (1..=increments as i64 + 1).collect::<Vec<_>>(),
                "{what}"
            );
        }
        if target.dialect == Dialect::Postgres {
            // The writes that rolled back drew cursors no event kept.
            let cursors: Vec<i64> = read.iter().flatten().map(|event| event.cursor).collect();
            let span = cursors.iter().max().unwrap() - cursors.iter().min().unwrap() + 1;
            assert!(span > cursors.len() as i64, "the identity has gaps");
        }
    }
}

#[test]
fn a_read_from_head_beside_a_write_in_flight_misses_none_of_its_events() {
    for target in targets("a_read_from_head") {
        if target.dialect != Dialect::Postgres {
            // One SQLite writer at a time: no write commits beside another.
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let paused = Pause::default();
        let (inside, entered) = mpsc::channel::<()>();
        let (release, released) = mpsc::channel::<()>();
        let inside = Mutex::new(inside);
        let released = Mutex::new(released);
        // The held write creates an instance, which draws a cursor, then
        // waits; its own operation event comes after.
        paused.on_pause(move |cx, _| {
            cx.instances()
                .create("Item", &json!({"title": "inside"}), Some("made"), None)?;
            let _ = inside
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .send(());
            let _ = released
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .recv_timeout(PATIENCE);
            Ok(())
        });
        let behaviors = || BehaviorSet::new().with(paused.clone());
        let holder = open(&target, behaviors(), &replica_name("holder"), false);
        let reader_name = replica_name("reader");
        let reader = open(&target, behaviors(), &reader_name, false);
        let document = item_document(json!([{"name": "test.Pause"}]));
        for namespace in ["a", "b"] {
            holder
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&holder, &document, Some(namespace));
            create_in(&holder, Some(namespace), "i1");
        }
        let next = thread::scope(|scope| {
            let held = scope.spawn(|| pause(&holder, &alice(), Some("a"), "Item", "i1", "held"));
            entered.recv_timeout(PATIENCE).expect("the held write");
            // b's write commits a cursor past the one a's write drew.
            create_in(&reader, Some("b"), "past");
            let head = scope.spawn(|| {
                reader
                    .events()
                    .read(
                        &alice(),
                        &ReadEvents {
                            namespace: Some("a"),
                            after: Some(EventsAfter::Head),
                            ..ReadEvents::default()
                        },
                    )
                    .expect("a read from head")
                    .next
            });
            // The read waits for a's writer.
            wait_for("the read from head waits for a's writer", || {
                head.is_finished()
                    || !admin
                        .sessions(
                            "application_name = ? AND wait_event_type = 'Lock'",
                            &[SqlValue::from(reader_name.as_str())],
                        )
                        .is_empty()
            });
            let waited = !head.is_finished();
            release.send(()).expect("release the held write");
            held.join().expect("the held write").expect("pause");
            (head.join().expect("the read from head"), waited)
        });
        let (next, waited) = next;
        // The held write's events are both before the head or both after
        // it, never split by it: before, since the read waited for it.
        let cursor_of = |kind: &str, id: &str| {
            all_events(&reader, Some("a"))
                .into_iter()
                .find(|event| event.kind == kind && event.instance_id.as_deref() == Some(id))
                .unwrap_or_else(|| panic!("the {kind} of {id}"))
                .cursor
        };
        let (made, operated) = (cursor_of("create", "made"), cursor_of("operation", "i1"));
        assert!(
            made <= next && operated <= next,
            "the head {next} is past the held write's create ({made}) and operation ({operated})"
        );
        // A write after the read is past the head, and a reader going on
        // from it sees it.
        create_in(&reader, Some("a"), "after");
        let page = reader
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    namespace: Some("a"),
                    after: Some(EventsAfter::Cursor(next)),
                    ..ReadEvents::default()
                },
            )
            .expect("a read on from the head");
        let seen: Vec<(String, Option<String>)> = page
            .events
            .iter()
            .map(|event| (event.kind.clone(), event.instance_id.clone()))
            .collect();
        assert_eq!(seen, [("create".to_owned(), Some("after".to_owned()))]);
        assert!(waited, "the read from head waited for a's writer");
    }
}

// The events in a stream's frames: each `data:` message's event, its
// cursor and instance, the ready event and bare ids left out.
fn frame_events(frames: &str) -> Vec<(i64, Option<String>)> {
    frames
        .split("\n\n")
        .filter(|message| !message.lines().any(|line| line.starts_with("event:")))
        .filter_map(|message| {
            let data = message
                .lines()
                .find_map(|line| line.strip_prefix("data: "))?;
            let event: Value = serde_json::from_str(data).expect("an event's JSON");
            Some((
                event["cursor"].as_i64().expect("a cursor"),
                event["instanceId"].as_str().map(str::to_owned),
            ))
        })
        .collect()
}

// A stream of the default namespace from its head on a shared engine,
// opened and past its ready event.
fn ready_stream(engine: &Engine, heartbeat_ms: u64) -> EventStream {
    let mut stream = start_stream(
        engine,
        &StreamOptions {
            page_size: None,
            heartbeat_ms: Some(heartbeat_ms),
        },
        &alice(),
        &ReadEvents {
            after: Some(EventsAfter::Head),
            ..ReadEvents::default()
        },
    )
    .expect("a stream");
    for expected in [": open", "event: ready"] {
        match stream.next(PATIENCE) {
            Pull::Data(bytes) => {
                let text = String::from_utf8(bytes).expect("UTF-8");
                assert!(text.starts_with(expected), "{text}");
            }
            other => panic!("expected {expected}, got {other:?}"),
        }
    }
    stream
}

// pull_until pulls a stream until its frames hold an event of `id`, and
// returns the events they held; each pull waits up to `wait`, and the
// whole up to PATIENCE.
fn pull_until(stream: &mut EventStream, id: &str, wait: Duration) -> Vec<(i64, Option<String>)> {
    let deadline = Instant::now() + PATIENCE;
    let mut seen = Vec::new();
    loop {
        assert!(Instant::now() < deadline, "no event of {id} in {seen:?}");
        match stream.next(wait) {
            Pull::Data(bytes) => {
                seen.extend(frame_events(&String::from_utf8(bytes).expect("UTF-8")));
                if seen
                    .iter()
                    .any(|(_, instance)| instance.as_deref() == Some(id))
                {
                    return seen;
                }
            }
            Pull::Pending => panic!("the stream was not woken for {id}"),
            Pull::End => panic!("the stream ended"),
        }
    }
}

#[test]
fn a_commit_on_one_engine_wakes_the_others_waiters_and_streams_through_its_listener() {
    for target in targets("a_commit_wakes_the_other") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let writer = open(&target, BehaviorSet::new(), &replica_name("writer"), false);
        let listener = open(&target, BehaviorSet::new(), &replica_name("listener"), true).shared();
        publish(&writer, &item_document(json!([])), None);
        wait_for("the listener listens", || {
            listener.db().notifier().listening()
        });
        // A waiter on the epoch wakes for the other engine's commit, well
        // before its timeout.
        let epoch = listener.events().epoch();
        create_in(&writer, None, "woken");
        assert!(
            listener.events().wait(epoch, PATIENCE) > epoch,
            "the waiter woke"
        );
        // So does a stream, whose heartbeat is far past the pull's wait: a
        // pull that is not woken comes back pending.
        let mut stream = ready_stream(&listener, 600_000);
        thread::scope(|scope| {
            scope.spawn(|| {
                thread::sleep(Duration::from_millis(50));
                create_in(&writer, None, "streamed");
            });
            let seen = pull_until(&mut stream, "streamed", PATIENCE);
            assert_eq!(seen.len(), 1, "{seen:?}");
        });
        listener.shutdown();
    }
}

#[test]
fn a_stream_goes_on_through_a_dropped_listener_and_catches_up_by_reading() {
    for target in targets("a_stream_through_a_dropped_listener") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let writer = open(&target, BehaviorSet::new(), &replica_name("writer"), false);
        let name = replica_name("listener");
        let listener = open(&target, BehaviorSet::new(), &name, true).shared();
        publish(&writer, &item_document(json!([])), None);
        let listening = || {
            listener.db().notifier().listening()
                && admin
                    .sessions(
                        "application_name = ? AND query LIKE 'LISTEN %'",
                        &[SqlValue::from(name.as_str())],
                    )
                    .len()
                    == 1
        };
        wait_for("the listener listens", listening);
        let mut stream = ready_stream(&listener, 600_000);
        let mut seen = Vec::new();
        for round in 0..3 {
            // The listener's session ends, and a commit follows at once,
            // likely before the listener is back: its reconnect wakes the
            // stream, which reads what it missed.
            assert_eq!(admin.terminate_listener(&name), 1, "round {round}");
            let missed = format!("missed{round}");
            create_in(&writer, None, &missed);
            seen.extend(pull_until(&mut stream, &missed, PATIENCE));
            // Back, it wakes the stream for the next commit as before.
            wait_for("the listener is back", listening);
            let heard = format!("heard{round}");
            create_in(&writer, None, &heard);
            seen.extend(pull_until(&mut stream, &heard, PATIENCE));
        }
        // Every event once, in order, as the log holds them.
        let log: Vec<(i64, Option<String>)> = all_events(&writer, None)
            .into_iter()
            .filter(|event| event.kind == "create")
            .map(|event| (event.cursor, event.instance_id))
            .collect();
        assert_eq!(seen, log);
        listener.shutdown();
    }
}

#[test]
fn without_a_listener_a_stream_catches_up_by_reading_at_each_heartbeat() {
    for target in targets("without_a_listener") {
        // No listener on Postgres; on SQLite, a second engine on the file,
        // whose own commits alone move its notifier.
        let writer = open(&target, BehaviorSet::new(), &replica_name("writer"), false);
        let reader = open(&target, BehaviorSet::new(), &replica_name("reader"), false).shared();
        publish(&writer, &item_document(json!([])), None);
        assert!(!reader.db().notifier().listening());
        let mut stream = ready_stream(&reader, 50);
        let mut seen = BTreeMap::new();
        for n in 0..3 {
            let id = format!("polled{n}");
            create_in(&writer, None, &id);
            // A pull returns at each heartbeat; the one after reads.
            let deadline = Instant::now() + PATIENCE;
            while !seen
                .values()
                .any(|instance: &Option<String>| instance.as_deref() == Some(id.as_str()))
            {
                assert!(Instant::now() < deadline, "{:?}: no {id}", target.dialect);
                if let Pull::Data(bytes) = stream.next(PATIENCE) {
                    seen.extend(frame_events(&String::from_utf8(bytes).expect("UTF-8")));
                }
            }
        }
        assert_eq!(seen.len(), 3, "{:?}: {seen:?}", target.dialect);
        reader.shutdown();
    }
}

/// Calls each soak thread makes: `ENGINE_SOAK_CALLS`, 200 unless set.
fn soak_calls() -> usize {
    std::env::var("ENGINE_SOAK_CALLS")
        .ok()
        .and_then(|calls| calls.parse().ok())
        .unwrap_or(200)
}

/// Long, on Postgres: sixteen writers on two engines through four
/// namespaces, a stream per namespace on a third engine, and its listener's
/// session ended every 200 ms; each stream sees its namespace's log, every
/// event once and in order. `make engine-rust-soak` runs it.
#[test]
#[ignore = "a soak; make engine-rust-soak runs it"]
fn soak_streams_through_racing_writers_and_dropped_listeners() {
    const NAMESPACES: [&str; 4] = ["a", "b", "c", "d"];
    for target in targets("soak_streams") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let started = Instant::now();
        let admin = Admin::new(&target).expect("the administrator");
        let writers = [0, 1].map(|n| {
            open(
                &target,
                fixtures(),
                &replica_name(&format!("writer{n}")),
                false,
            )
        });
        let name = replica_name("listener");
        let listener = open(&target, fixtures(), &name, true).shared();
        let document = item_document(json!([{"name": "test.Counter"}]));
        for namespace in NAMESPACES {
            writers[0]
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&writers[0], &document, Some(namespace));
            create_in(&writers[0], Some(namespace), "i1");
        }
        let done = AtomicBool::new(false);
        let calls = soak_calls();
        let seen: Vec<Vec<i64>> = thread::scope(|scope| {
            let done = &done;
            let streams: Vec<_> = NAMESPACES
                .iter()
                .map(|namespace| {
                    let listener = &listener;
                    scope.spawn(move || {
                        let mut stream = start_stream(
                            listener,
                            &StreamOptions {
                                page_size: Some(25),
                                heartbeat_ms: Some(1_000),
                            },
                            &alice(),
                            &ReadEvents {
                                namespace: Some(namespace),
                                ..ReadEvents::default()
                            },
                        )
                        .expect("a stream");
                        let mut cursors = Vec::new();
                        let mut idle = 0;
                        loop {
                            match stream.next(Duration::from_millis(500)) {
                                Pull::Data(bytes) => {
                                    let text = String::from_utf8(bytes).expect("UTF-8");
                                    let events = frame_events(&text);
                                    idle = if events.is_empty() { idle + 1 } else { 0 };
                                    cursors.extend(events.into_iter().map(|(cursor, _)| cursor));
                                }
                                Pull::Pending => idle += 1,
                                Pull::End => panic!("the stream ended"),
                            }
                            if done.load(Ordering::SeqCst) && idle >= 3 {
                                return cursors;
                            }
                        }
                    })
                })
                .collect();
            let killer = scope.spawn(|| {
                let mut ended = 0;
                while !done.load(Ordering::SeqCst) {
                    ended += admin.terminate_listener(&name);
                    thread::sleep(Duration::from_millis(200));
                }
                ended
            });
            let threads: Vec<_> = (0..16)
                .map(|thread| {
                    let engine = &writers[thread % 2];
                    scope.spawn(move || {
                        for n in 0..calls {
                            let namespace = NAMESPACES[(thread + n) % NAMESPACES.len()];
                            retrying(TRIES, || {
                                engine.instances().invoke(
                                    &alice(),
                                    "Item",
                                    "i1",
                                    "increment",
                                    &json!({}),
                                    WriteOptions {
                                        namespace: Some(namespace),
                                        ..Default::default()
                                    },
                                )
                            })
                            .0
                            .expect("increment");
                        }
                    })
                })
                .collect();
            for thread in threads {
                thread.join().expect("a writer");
            }
            done.store(true, Ordering::SeqCst);
            let ended = killer.join().expect("the killer");
            assert!(ended > 0, "the listener's session ended");
            streams
                .into_iter()
                .map(|stream| stream.join().expect("a stream"))
                .collect()
        });
        for (namespace, cursors) in NAMESPACES.iter().zip(&seen) {
            let log: Vec<i64> = all_events(&writers[0], Some(namespace))
                .iter()
                .map(|event| event.cursor)
                .collect();
            assert_eq!(cursors, &log, "namespace {namespace}");
        }
        listener.shutdown();
        eprintln!(
            "soak streams, 16 threads x {calls} calls: {:?}",
            started.elapsed()
        );
    }
}
