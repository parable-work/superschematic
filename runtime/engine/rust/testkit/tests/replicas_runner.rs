//! Two engines on one Postgres database, each with a runner, as two
//! replicas of a server open it (D58): the runner's lease, a session
//! advisory lock, lets one run at a time; the other takes over when the
//! first stops or its session ends (`pg_terminate_backend`, as a crash or
//! a lost connection would); and the work it does happens once even when
//! the two overlap, as they can for a moment when a lease's session ends
//! under a pass that is under way: a reaction's cursor and a schedule's
//! next run are read again under the namespace's lock, so a schedule
//! fires once for one due time, and Timers fires each deadline once.
//! Retention prunes on the runner that holds the lease alone, and an event
//! stream another replica serves, which it prunes past, ends, its resume
//! answered 410 `cursor_expired`.
//!
//! The lease is Postgres's: on SQLite one process writes the file and its
//! runner always holds it, so these cases run on Postgres alone, when
//! `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one, each in a schema of
//! its own.

mod replicas_support;
mod support;

use std::collections::BTreeMap;
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::mpsc;
use std::sync::{Arc, Mutex, PoisonError};
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use replicas_support::{as_replica, replica_name, set_busy_timeout, wait_for, Admin, PATIENCE};
use superschematic_engine::api::{
    handle, start_stream, ApiOptions, Body, Caller, Pull, Request, StreamOptions,
};
use superschematic_engine::storage::{lock_key, Dialect, Lock, SqlValue};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, Engine, EngineErrorCode, EngineEvent, EngineOptions,
    EventsAfter, GetOptions, Principal, ReadEvents, RetentionOptions, RunnerOptions,
};
use superschematic_engine_testkit::behaviors::{Ledger, LedgerConfig};
use support::{alice, code, publish, schema_document, targets, Target};

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// The runner's lease of the target's schema, as the engine names it.
fn lease_key(target: &Target) -> i64 {
    lock_key(&[
        "lease",
        target.schema().expect("a Postgres schema"),
        "runner",
    ])
}

fn ledger_document(name: &str) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "test.Ledger"}]);
    document
}

// Options for a replica with the runner, its sessions named `name`,
// listening, trying the lease every 50 ms.
fn replica_options(target: &Target, behaviors: BehaviorSet, name: &str) -> EngineOptions {
    let mut options = target.options(behaviors);
    options.runner = Some(RunnerOptions {
        lease_retry_ms: Some(50),
        ..RunnerOptions::new(runner())
    });
    as_replica(&mut options, name, true);
    options
}

fn create_in(engine: &Engine, schema: &str, namespace: Option<&str>, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": id }),
            CreateOptions {
                namespace,
                id: Some(id),
                ..Default::default()
            },
        )
        .unwrap_or_else(|error| panic!("create {id}: {error}"));
}

fn note_count(engine: &Engine, namespace: &str, id: &str) -> usize {
    engine
        .instances()
        .get(
            &alice(),
            "Order",
            id,
            GetOptions {
                namespace: Some(namespace),
                ..Default::default()
            },
        )
        .expect("get")
        .and_then(|record| record.behaviors.get("test.Ledger").cloned())
        .and_then(|fields| fields.get("notes").cloned())
        .and_then(|notes| notes.as_array().map(Vec::len))
        .unwrap_or(0)
}

// mark writes a note of the event on the instance it names.
fn mark(
    cx: &superschematic_engine::behaviors::ReactionCx<'_, LedgerConfig>,
    event: &EngineEvent,
) -> superschematic_engine::Result<()> {
    cx.instances().invoke(
        &event.schema,
        event.instance_id.as_deref().unwrap_or_default(),
        "mark",
        &json!({ "note": format!("{} {}", event.kind, event.cursor) }),
        None,
    )?;
    Ok(())
}

/// Who handled which instance's create, and how many reactions ran at
/// once at most, across both engines.
#[derive(Clone, Default)]
struct Handled {
    by: Arc<Mutex<Vec<(String, String)>>>,
    running: Arc<AtomicUsize>,
    most: Arc<AtomicUsize>,
}

impl Handled {
    fn of(&self, id: &str) -> Vec<String> {
        self.by
            .lock()
            .unwrap_or_else(PoisonError::into_inner)
            .iter()
            .filter(|(_, instance)| instance == id)
            .map(|(who, _)| who.clone())
            .collect()
    }

    // ledger is test.Ledger whose reactions note each caller's create as
    // `label` handled it, slowly enough that two runners would overlap.
    fn ledger(&self, label: &str) -> Ledger {
        let ledger = Ledger::default();
        let handled = self.clone();
        let label = label.to_owned();
        ledger.probe.on_react(move |cx, event| {
            if event.cause.is_some() {
                return Ok(());
            }
            let now = handled.running.fetch_add(1, Ordering::SeqCst) + 1;
            handled.most.fetch_max(now, Ordering::SeqCst);
            thread::sleep(Duration::from_millis(5));
            handled
                .by
                .lock()
                .unwrap_or_else(PoisonError::into_inner)
                .push((label.clone(), event.instance_id.clone().unwrap_or_default()));
            let marked = mark(cx, event);
            handled.running.fetch_sub(1, Ordering::SeqCst);
            marked
        });
        ledger
    }
}

#[test]
fn one_runner_runs_at_a_time_and_the_other_takes_over_when_it_stops_or_its_session_ends() {
    for target in targets("one_runner_at_a_time") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let key = lease_key(&target);
        let handled = Handled::default();
        let names = [replica_name("first"), replica_name("second")];
        let [first, second] = [0, 1].map(|n| {
            let label = ["first", "second"][n];
            let behaviors = BehaviorSet::new().with(handled.ledger(label));
            Engine::open(replica_options(&target, behaviors, &names[n]))
                .expect("open a replica")
                .shared()
        });
        for namespace in ["a", "b"] {
            first
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&first, &ledger_document("Order"), Some(namespace));
        }
        let holder = || admin.lock_holder_name(key);
        first.runner().start().expect("start");
        wait_for("the first takes the lease", || {
            holder().as_deref() == Some(names[0].as_str())
        });
        second.runner().start().expect("start");
        assert_eq!(
            code(second.runner().run_due()),
            EngineErrorCode::Busy,
            "run_due waits for the lease another process holds"
        );
        let namespace_of = |n: usize| if n.is_multiple_of(2) { "a" } else { "b" };
        let created = |engine: &Engine, from: usize, to: usize| {
            for n in from..to {
                create_in(engine, "Order", Some(namespace_of(n)), &format!("o{n}"));
            }
        };
        let all_handled =
            |to: usize| (0..to).all(|n| note_count(&first, namespace_of(n), &format!("o{n}")) == 1);
        created(&second, 0, 6);
        wait_for("the first runner's notes", || all_handled(6));
        for n in 0..6 {
            assert_eq!(handled.of(&format!("o{n}")), ["first"], "o{n}");
        }
        // The first's session ends, as its crash would: the second takes
        // the lease, and the first, finding its lease gone, stays idle.
        let retaken = hand_over(&admin, key, &names[0], &names[1]);
        if retaken > 0 {
            eprintln!("the first took its lease again {retaken} times before the second");
        }
        created(&first, 6, 12);
        wait_for("the second runner's notes", || all_handled(12));
        for n in 6..12 {
            assert_eq!(handled.of(&format!("o{n}")), ["second"], "o{n}");
        }
        assert_eq!(
            code(first.runner().run_due()),
            EngineErrorCode::Busy,
            "the first waits for the lease the second holds"
        );
        // The second stops: the first takes the lease back.
        second.runner().stop();
        wait_for("the first takes the lease back", || {
            holder().as_deref() == Some(names[0].as_str())
        });
        created(&second, 12, 14);
        wait_for("the first runner's notes again", || all_handled(14));
        for n in 12..14 {
            assert_eq!(handled.of(&format!("o{n}")), ["first"], "o{n}");
        }
        assert_eq!(
            handled.most.load(Ordering::SeqCst),
            1,
            "never two reactions at once"
        );
        assert!(!first.poisoned() && !second.poisoned());
        second.shutdown();
        first.shutdown();
    }
}

/// Ends the session of `from`, the replica that holds the lease `key`,
/// until `to` takes the lease. A runner whose lease's session ended tries
/// the lease again at its next wake, as the other does every
/// `lease_retry_ms`, and whichever tries first takes it: when the holder
/// is still at work, or its listener wakes it for a notice of its own last
/// commits, it is often its own runner that takes the lease again, with a
/// new session, which is one runner holding the lease as it should, not a
/// takeover. So each time `from` holds the lease again, its new session
/// ends too, until `to` is the first to try. Returns how many times `from`
/// took it again.
fn hand_over(admin: &Admin, key: i64, from: &str, to: &str) -> usize {
    let mut ended = admin.lock_holder(key).expect("a holder of the lease");
    assert!(admin.terminate_lock_holder(key), "the lease's session ends");
    let mut retaken = 0;
    let deadline = Instant::now() + PATIENCE;
    loop {
        if let Some(pid) = admin.lock_holder(key) {
            match admin.lock_holder_name(key).as_deref() {
                Some(name) if name == to => return retaken,
                Some(name) if name == from && pid != ended => {
                    retaken += 1;
                    ended = pid;
                    admin.terminate_lock_holder(key);
                }
                _ => {}
            }
        }
        assert!(
            Instant::now() < deadline,
            "the lease passes from {from} to {to}: still not done, {from} took it again {retaken} times"
        );
        thread::sleep(Duration::from_millis(10));
    }
}

// Two runners whose passes overlap: the namespace's lock held by a third
// session while the first's pass waits on it with a schedule due; the
// first's lease's session ended, so the second takes the lease and its
// pass waits on the same lock with the same schedule due; then the lock
// let go. What `run` does in each runner's pass, it does once.
fn overlapping_passes(
    target: &Target,
    first: &Engine,
    second: &Engine,
    names: &[String; 2],
    advance: impl Fn(),
) {
    let admin = Admin::new(target).expect("the administrator");
    let key = lease_key(target);
    advance();
    let blocker = target.db();
    let (release, released) = mpsc::channel::<()>();
    thread::scope(|scope| {
        let (held, holding) = mpsc::channel::<()>();
        let block = scope.spawn(move || {
            blocker.write(Lock::Namespace("default"), |_| {
                held.send(()).expect("say the lock is held");
                let _ = released.recv_timeout(PATIENCE);
                Ok::<_, superschematic_engine::storage::Error>(())
            })
        });
        holding.recv_timeout(PATIENCE).expect("the lock is held");
        let one = scope.spawn(|| first.runner().run_due());
        wait_for("the first's pass waits on the lock", || {
            admin.waiting(&names[0])
        });
        assert!(admin.terminate_lock_holder(key), "the first's lease ends");
        let two = scope.spawn(|| second.runner().run_due());
        wait_for("the second's pass waits on the lock", || {
            admin.waiting(&names[1])
        });
        release.send(()).expect("let the lock go");
        block
            .join()
            .expect("the blocker")
            .expect("the blocker's write");
        one.join().expect("the first's pass").expect("run_due");
        two.join().expect("the second's pass").expect("run_due");
    });
}

#[test]
fn a_schedule_runs_once_for_a_due_time_when_two_runners_passes_overlap() {
    for target in targets("a_schedule_runs_once") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let names = [replica_name("first"), replica_name("second")];
        let runs: Arc<Mutex<Vec<(String, i64)>>> = Arc::default();
        let [first, second] = [0, 1].map(|n| {
            let label = ["first", "second"][n];
            let ledger = Ledger::default();
            let runs = Arc::clone(&runs);
            ledger.probe.on_sweep(move |cx| {
                runs.lock()
                    .unwrap_or_else(PoisonError::into_inner)
                    .push((label.to_owned(), cx.now()));
                Ok(())
            });
            let mut options = replica_options(&target, BehaviorSet::new().with(ledger), &names[n]);
            options.clock = Clock::Manual(1_000_000);
            set_busy_timeout(&mut options, PATIENCE);
            Engine::open(options).expect("open a replica")
        });
        publish(&first, &ledger_document("Order"), None);
        // The first pass finds the schedule, due an interval later.
        first.runner().run_due().expect("the first pass");
        assert!(runs.lock().unwrap().is_empty(), "nothing due yet");
        overlapping_passes(&target, &first, &second, &names, || {
            for engine in [&first, &second] {
                engine.clock().set(1_060_000).expect("set the clock");
            }
        });
        let ran = runs.lock().unwrap().clone();
        assert_eq!(
            ran.iter().map(|(_, at)| *at).collect::<Vec<_>>(),
            [1_060_000],
            "one run for the due time: {ran:?}"
        );
        let schedule = second
            .runner()
            .status()
            .expect("status")
            .schedules
            .into_iter()
            .find(|schedule| schedule.behavior == "test.Ledger")
            .expect("the schedule");
        assert_eq!(
            (schedule.previous, schedule.next, schedule.failures),
            (Some(1_060_000), Some(1_120_000), 0)
        );
    }
}

fn ticket_document() -> Value {
    let mut document = schema_document(
        "Ticket",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Ticket"]["behaviors"] = json!([
        {"name": "Workflow", "config": {"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}},
        {"name": "Timers", "config": {
            "deadlines": {"close": {"start": {"create": true}, "afterMs": 1, "then": {"transition": "closed"}}},
            "sweepMs": 1000
        }}
    ]);
    document
}

// The fireTimer events of each ticket, by id.
fn fires(engine: &Engine) -> BTreeMap<String, usize> {
    let mut out = BTreeMap::new();
    for event in replicas_support::all_events(engine, None) {
        if event.kind == "operation" && event.change["operation"] == json!("fireTimer") {
            *out.entry(event.instance_id.clone().unwrap_or_default())
                .or_default() += 1;
        }
    }
    out
}

#[test]
fn timers_fires_each_deadline_once_across_a_handover_and_overlapping_passes() {
    for target in targets("timers_fire_once") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let key = lease_key(&target);
        let names = [replica_name("first"), replica_name("second")];
        // Started runners on the system's clock: a sweep a second.
        let [first, second] = [0, 1].map(|n| {
            let mut options = target.core_options();
            options.runner = Some(RunnerOptions {
                lease_retry_ms: Some(50),
                ..RunnerOptions::new(runner())
            });
            as_replica(&mut options, &names[n], true);
            Engine::open(options).expect("open a replica").shared()
        });
        publish(&first, &ticket_document(), None);
        first.runner().start().expect("start");
        wait_for("the first takes the lease", || {
            admin.lock_holder_name(key).as_deref() == Some(names[0].as_str())
        });
        second.runner().start().expect("start");
        for n in 0..10 {
            create_in(&second, "Ticket", None, &format!("t{n}"));
        }
        wait_for("the first's sweep fires them", || fires(&first).len() == 10);
        assert!(admin.terminate_lock_holder(key), "the first's lease ends");
        for n in 10..20 {
            create_in(&first, "Ticket", None, &format!("t{n}"));
        }
        wait_for("the second's sweep fires the rest", || {
            fires(&second).len() == 20
        });
        // Another sweep or two: nothing fires again.
        thread::sleep(Duration::from_millis(2_500));
        let fired = fires(&second);
        assert!(fired.values().all(|&count| count == 1), "{fired:?}");
        for n in 0..20 {
            let ticket = second
                .instances()
                .get(&alice(), "Ticket", &format!("t{n}"), GetOptions::default())
                .expect("get")
                .expect("the ticket");
            assert_eq!(ticket.behaviors["Workflow"]["status"], json!("closed"));
        }
        second.shutdown();
        first.shutdown();
        // Overlapping passes, on a manual clock: each deadline due fires
        // once, whichever pass reaches it.
        let target = targets("timers_fire_once_overlapping")
            .into_iter()
            .find(|target| target.dialect == Dialect::Postgres)
            .expect("a Postgres target");
        let names = [replica_name("first"), replica_name("second")];
        let [first, second] = [0, 1].map(|n| {
            let mut options = target.core_options();
            options.runner = Some(RunnerOptions::new(runner()));
            options.clock = Clock::Manual(1_000_000);
            set_busy_timeout(&mut options, PATIENCE);
            as_replica(&mut options, &names[n], false);
            Engine::open(options).expect("open a replica")
        });
        publish(&first, &ticket_document(), None);
        first.runner().run_due().expect("find the sweep");
        for n in 0..5 {
            create_in(&first, "Ticket", None, &format!("t{n}"));
        }
        overlapping_passes(&target, &first, &second, &names, || {
            for engine in [&first, &second] {
                engine.clock().set(1_002_000).expect("set the clock");
            }
        });
        let fired = fires(&second);
        assert_eq!(fired.len(), 5, "{fired:?}");
        assert!(fired.values().all(|&count| count == 1), "{fired:?}");
    }
}

// pruned_at is when the engine's own runner last finished pruning, None
// when it never has.
fn pruned_at(engine: &Engine) -> Option<i64> {
    engine
        .runner()
        .status()
        .expect("status")
        .retention
        .expect("the engine has retention")
        .previous
}

fn floor(engine: &Engine) -> i64 {
    engine.events().floor(None).expect("the floor")
}

#[test]
fn only_the_runner_that_holds_the_lease_prunes_and_a_stream_it_prunes_past_resumes_to_410() {
    for target in targets("retention_on_the_lease") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let key = lease_key(&target);
        let names = [replica_name("first"), replica_name("second")];
        // Both keep a namespace's newest two events and prune every second.
        let [first, second] = [0, 1].map(|n| {
            let mut options = replica_options(&target, BehaviorSet::new(), &names[n]);
            options.retention = Some(RetentionOptions {
                max_events: Some(2),
                every_ms: Some(1_000),
                ..RetentionOptions::default()
            });
            Engine::open(options).expect("open a replica").shared()
        });
        let document = schema_document(
            "Order",
            json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
            json!({}),
        );
        publish(&second, &document, None);
        // A stream the second serves, caught up at the head, which its
        // client then stops reading.
        let mut stream = start_stream(
            &second,
            &StreamOptions {
                page_size: Some(1),
                heartbeat_ms: Some(60_000),
            },
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Head),
                ..ReadEvents::default()
            },
        )
        .expect("a stream");
        let ready = second.events().head().expect("the head");
        for n in 0..6 {
            create_in(&second, "Order", None, &format!("o{n}"));
        }
        // Only the runner that holds the lease prunes: the first's.
        first.runner().start().expect("start");
        wait_for("the first takes the lease", || {
            admin.lock_holder_name(key).as_deref() == Some(names[0].as_str())
        });
        second.runner().start().expect("start");
        wait_for("the first prunes past the stream", || {
            floor(&second) > ready
        });
        // The pass ends a step after the batch that moved the floor.
        wait_for("the first's pass ends", || pruned_at(&first).is_some());
        assert_eq!(pruned_at(&second), None, "the second waits for the lease");
        let head = second.events().head().expect("the head");
        assert_eq!(floor(&second), head - 2, "the newest two are kept");

        // The stream retention passed ends, and its resume is 410
        // cursor_expired, with the floor and the head to start again from.
        let deadline = Instant::now() + PATIENCE;
        loop {
            match stream.next(Duration::from_millis(100)) {
                Pull::End => break,
                Pull::Data(bytes) => {
                    let text = String::from_utf8(bytes).expect("UTF-8");
                    assert!(
                        text.starts_with(": open") || text.starts_with("event: ready"),
                        "no event the runner pruned is sent: {text}"
                    );
                }
                Pull::Pending => {}
            }
            assert!(Instant::now() < deadline, "the stream did not end");
        }
        let resumed = handle(
            &second,
            &ApiOptions::default(),
            &Request {
                method: "GET".to_owned(),
                path: "/namespaces/default/events".to_owned(),
                headers: vec![
                    ("accept".to_owned(), "text/event-stream".to_owned()),
                    ("last-event-id".to_owned(), ready.to_string()),
                ],
                caller: Caller {
                    principal: Some(alice()),
                    ..Caller::default()
                },
                request_id: "r1".to_owned(),
                ..Request::default()
            },
        );
        assert_eq!(resumed.status, 410);
        assert!(!matches!(resumed.body, Body::Stream(_)));
        let problem: Value = serde_json::from_slice(resumed.bytes()).expect("a problem");
        assert_eq!(problem["code"], "cursor_expired", "{problem}");
        assert_eq!(
            problem["details"],
            json!({"after": ready, "floor": head - 2, "head": head}),
            "{problem}"
        );

        // The first stops: the second takes the lease and prunes from then.
        first.runner().stop();
        wait_for("the second takes the lease", || {
            admin.lock_holder_name(key).as_deref() == Some(names[1].as_str())
        });
        for n in 6..9 {
            create_in(&first, "Order", None, &format!("o{n}"));
        }
        let head = first.events().head().expect("the head");
        wait_for("the second prunes", || floor(&first) == head - 2);
        wait_for("the second's pass ends", || pruned_at(&second).is_some());
        assert!(!first.poisoned() && !second.poisoned());
        second.shutdown();
        first.shutdown();
    }
}

// events_kept is how many events of the default namespace the log holds.
fn events_kept(admin: &Admin) -> i64 {
    admin
        .db()
        .read(|tx| {
            tx.get(
                "SELECT COUNT(*) AS count FROM engine_events WHERE namespace = ?",
                &[SqlValue::from("default")],
            )
        })
        .expect("count the events")
        .expect("a count")
        .i64("count")
        .expect("an integer")
}

// pruned_so_far is the count of events engine_log_floors says retention
// pruned of the default namespace.
fn pruned_so_far(admin: &Admin) -> i64 {
    admin
        .db()
        .read(|tx| {
            tx.get(
                "SELECT pruned FROM engine_log_floors WHERE namespace = ?",
                &[SqlValue::from("default")],
            )
        })
        .expect("read the floor")
        .map_or(0, |row| row.i64("pruned").expect("an integer"))
}

fn retention_replica(target: &Target, name: &str, retention: RetentionOptions) -> Arc<Engine> {
    let mut options = replica_options(target, BehaviorSet::new(), name);
    options.retention = Some(retention);
    set_busy_timeout(&mut options, PATIENCE);
    Engine::open(options).expect("open a replica").shared()
}

fn order_schema() -> Value {
    schema_document(
        "Order",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    )
}

#[test]
fn a_prune_another_runner_overtook_never_moves_the_floor_back_nor_counts_twice() {
    for target in targets("retention_overtaken") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let names = [replica_name("first"), replica_name("second")];
        let first = retention_replica(
            &target,
            &names[0],
            RetentionOptions {
                max_events: Some(3),
                ..RetentionOptions::default()
            },
        );
        publish(&first, &order_schema(), None);
        for n in 0..10 {
            create_in(&first, "Order", None, &format!("o{n}"));
        }
        let head = first.events().head().expect("the head");
        let total = events_kept(&admin);
        // The first's prune chooses its batch from floor 0, then waits for
        // the namespace's lock, which a writer holds; inside it, another
        // runner's prune moves the floor past that batch, keeping the newest
        // two, as a pass of the next lease holder would at a handover.
        let blocker = target.db();
        let (release, released) = mpsc::channel::<()>();
        let overtaken = head - 2;
        thread::scope(|scope| {
            let (held, holding) = mpsc::channel::<()>();
            let block = scope.spawn(move || {
                blocker.write(Lock::Namespace("default"), |tx| {
                    held.send(()).expect("say the lock is held");
                    let _ = released.recv_timeout(PATIENCE);
                    tx.run(
                        "INSERT INTO engine_log_floors (namespace, floor, publish_floor, pruned) VALUES (?, ?, 0, ?)",
                        &[
                            SqlValue::from("default"),
                            SqlValue::Integer(overtaken),
                            SqlValue::Integer(total - 2),
                        ],
                    )?;
                    tx.run(
                        "DELETE FROM engine_events WHERE namespace = ? AND cursor <= ?",
                        &[SqlValue::from("default"), SqlValue::Integer(overtaken)],
                    )?;
                    Ok::<_, superschematic_engine::storage::Error>(())
                })
            });
            holding.recv_timeout(PATIENCE).expect("the lock is held");
            let pruning = scope.spawn(|| first.runner().prune());
            wait_for("the first's prune waits on the lock", || {
                admin.waiting(&names[0])
            });
            release.send(()).expect("let the lock go");
            block
                .join()
                .expect("the blocker")
                .expect("the blocker's write");
            pruning.join().expect("the prune").expect("prune");
        });
        // The first chose its batch again from the floor it found: the
        // newest two are already fewer than its three, so it pruned none.
        assert_eq!(floor(&first), overtaken, "the floor never moves back");
        assert_eq!(events_kept(&admin), 2);
        assert_eq!(pruned_so_far(&admin), total - 2, "nothing counted twice");
        // A resume from a cursor in what the other pruned is 410.
        let gone = first
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    after: Some(EventsAfter::Cursor(overtaken - 1)),
                    ..ReadEvents::default()
                },
            )
            .expect_err("before the floor");
        assert_eq!(code(Err::<(), _>(gone)), EngineErrorCode::CursorExpired);
        first.shutdown();
    }
}

#[test]
fn a_runner_that_takes_the_lease_again_counts_again_and_keeps_max_events() {
    for target in targets("retention_regained") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let key = lease_key(&target);
        let names = [replica_name("first"), replica_name("second")];
        let retention = RetentionOptions {
            max_events: Some(4),
            every_ms: Some(1_000),
            ..RetentionOptions::default()
        };
        let [first, second] =
            [0, 1].map(|n| retention_replica(&target, &names[n], retention.clone()));
        publish(&first, &order_schema(), None);
        let created = std::cell::Cell::new(0);
        let create = |engine: &Engine, many: usize| {
            for _ in 0..many {
                create_in(engine, "Order", None, &format!("o{}", created.get()));
                created.set(created.get() + 1);
            }
        };
        // Outside the lease: the first counts, the second prunes past what
        // it counted, and the first, counting from a floor that moved,
        // counts again.
        create(&first, 6);
        first.runner().prune().expect("prune");
        assert_eq!(events_kept(&admin), 4);
        create(&second, 3);
        second.runner().prune().expect("prune");
        assert_eq!(events_kept(&admin), 4);
        create(&first, 1);
        first.runner().prune().expect("prune");
        assert_eq!(events_kept(&admin), 4, "never fewer than maxEvents");

        // Through the lease: the first prunes, the second takes the lease
        // and prunes, the first takes it again and prunes.
        let takes = |engine: &Engine, name: &str| {
            engine.runner().start().expect("start");
            wait_for("the runner takes the lease", || {
                admin.lock_holder_name(key).as_deref() == Some(name)
            });
        };
        let settles = |what: &str| {
            wait_for(what, || events_kept(&admin) <= 4);
            assert_eq!(events_kept(&admin), 4, "{what}: never fewer than maxEvents");
        };
        create(&first, 5);
        takes(&first, &names[0]);
        settles("the first prunes");
        first.runner().stop();
        create(&second, 5);
        takes(&second, &names[1]);
        settles("the second prunes");
        second.runner().stop();
        create(&first, 2);
        takes(&first, &names[0]);
        settles("the first prunes again");
        thread::sleep(Duration::from_millis(1_500));
        assert_eq!(events_kept(&admin), 4, "another pass keeps them");
        first.runner().stop();
        second.shutdown();
        first.shutdown();
    }
}

/// Long, on Postgres: a writer creates orders through two namespaces while
/// the lease holder's session is ended every 300 ms, the lease passing back
/// and forth; every create is handled once. `ENGINE_SOAK_CALLS` orders
/// (200 unless set). `make engine-rust-soak` runs it.
#[test]
#[ignore = "a soak; make engine-rust-soak runs it"]
fn soak_reactions_through_lease_handovers() {
    for target in targets("soak_handovers") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let started = std::time::Instant::now();
        let admin = Admin::new(&target).expect("the administrator");
        let key = lease_key(&target);
        let handled = Handled::default();
        let names = [replica_name("first"), replica_name("second")];
        let engines = [0, 1].map(|n| {
            let label = ["first", "second"][n];
            let behaviors = BehaviorSet::new().with(handled.ledger(label));
            Engine::open(replica_options(&target, behaviors, &names[n]))
                .expect("open a replica")
                .shared()
        });
        for namespace in ["a", "b"] {
            engines[0]
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&engines[0], &ledger_document("Order"), Some(namespace));
        }
        for engine in &engines {
            engine.runner().start().expect("start");
        }
        let orders: usize = std::env::var("ENGINE_SOAK_CALLS")
            .ok()
            .and_then(|calls| calls.parse().ok())
            .unwrap_or(200);
        let namespace_of = |n: usize| if n.is_multiple_of(2) { "a" } else { "b" };
        let writing = std::sync::atomic::AtomicBool::new(true);
        let handovers = thread::scope(|scope| {
            let killer = scope.spawn(|| {
                let mut handovers = 0;
                while writing.load(Ordering::SeqCst) {
                    handovers += usize::from(admin.terminate_lock_holder(key));
                    thread::sleep(Duration::from_millis(300));
                }
                handovers
            });
            for n in 0..orders {
                create_in(
                    &engines[n % 2],
                    "Order",
                    Some(namespace_of(n)),
                    &format!("o{n}"),
                );
            }
            wait_for("every order handled", || {
                handled
                    .by
                    .lock()
                    .unwrap_or_else(PoisonError::into_inner)
                    .len()
                    >= orders
            });
            writing.store(false, Ordering::SeqCst);
            killer.join().expect("the killer")
        });
        // A moment for a pass that would handle one twice.
        thread::sleep(Duration::from_millis(500));
        for n in 0..orders {
            let id = format!("o{n}");
            assert_eq!(handled.of(&id).len(), 1, "{id}: {:?}", handled.of(&id));
            assert_eq!(note_count(&engines[0], namespace_of(n), &id), 1, "{id}");
        }
        for engine in &engines {
            engine.shutdown();
        }
        eprintln!(
            "soak handovers, {orders} orders, {handovers} handovers: {:?}",
            started.elapsed()
        );
    }
}
