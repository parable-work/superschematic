//! Two engines on one Postgres database, each with a runner, as two
//! replicas of a server open it (D58): the runner's lease, a session
//! advisory lock, lets one run at a time; the other takes over when the
//! first stops or its session ends (`pg_terminate_backend`, as a crash or
//! a lost connection would); and the work it does happens once even when
//! the two overlap, as they can for a moment when a lease's session ends
//! under a pass that is under way: a reaction's cursor and a schedule's
//! next run are read again under the namespace's lock, so a schedule
//! fires once for one due time, and Timers fires each deadline once.
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
use std::time::Duration;

use serde_json::{json, Value};

use replicas_support::{as_replica, replica_name, set_busy_timeout, wait_for, Admin, PATIENCE};
use superschematic_engine::storage::{lock_key, Dialect, Lock};
use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, Engine, EngineErrorCode, EngineEvent, EngineOptions,
    GetOptions, Principal, RunnerOptions,
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
        assert!(admin.terminate_lock_holder(key), "the lease's session ends");
        wait_for("the second takes the lease", || {
            holder().as_deref() == Some(names[1].as_str())
        });
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
