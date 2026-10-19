//! Two engines on one database, each with a pool of its own, as two
//! replicas of a server open it (D58): the writers of one namespace take
//! turns on its advisory lock, so racing writes lose no update and repeat
//! no seq or cursor, a unique index and an id hold under racing creates,
//! Numbering's counter never repeats, Budget's reservations never pass a
//! limit and Queue never claims one job twice; and writers of different
//! namespaces do not wait for each other.
//!
//! Each case runs on Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL`
//! names one, in a schema of its own, and the racing cases on SQLite too,
//! where the two engines are two connections to one file and its one
//! writer serializes them. A call the storage refuses as `busy` is tried
//! again, as a client of the 503 would; any other failure fails the test.
//! The racing cases run small here; `soak_two_engines_racing_writes`
//! (ignored, `make engine-rust-soak`) runs them long, with more threads
//! than either pool has connections.

mod replicas_support;
mod support;

use std::collections::BTreeSet;
use std::thread;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use replicas_support::{
    all_events, as_replica, pause, replica_name, retrying, set_busy_timeout, Pause,
};
use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    BehaviorSet, CreateOptions, Engine, EngineErrorCode, EngineEvent, Error, Principal,
    WriteOptions,
};
use support::{alice, publish, refusal, schema_document, targets, Target};

/// How hard a case races: threads, alternating the two engines, and the
/// calls each makes.
#[derive(Clone, Copy, Debug)]
struct Load {
    threads: usize,
    calls: usize,
}

/// The racing cases' load in CI: deterministic in what it checks, and
/// quick.
const LOAD: Load = Load {
    threads: 8,
    calls: 20,
};

/// The soak's load: eight threads on each engine, twice either pool's
/// connections, and `ENGINE_SOAK_CALLS` calls each (200 unless set).
fn soak_load() -> Load {
    Load {
        threads: 16,
        calls: std::env::var("ENGINE_SOAK_CALLS")
            .ok()
            .and_then(|calls| calls.parse().ok())
            .unwrap_or(200),
    }
}

/// Times a call is tried while the storage is busy.
const TRIES: usize = 200;

fn principal(subject: &str) -> Principal {
    Principal::new(subject, Vec::<String>::new())
}

// replicas opens two engines, each with a pool of its own, with the
// options `options` makes for each (a target's).
fn replicas(test: &str, options: impl Fn() -> superschematic_engine::EngineOptions) -> [Engine; 2] {
    [0, 1].map(|_| {
        let mut options = options();
        as_replica(&mut options, &replica_name(test), false);
        Engine::open(options).expect("open a replica")
    })
}

// race runs `call` on the load's threads, alternating the engines, `calls`
// times each with the thread's number and the call's, and returns every
// answer, each tried again while busy.
fn race<T: Send>(
    engines: &[Engine; 2],
    threads: usize,
    calls: usize,
    call: impl Fn(&Engine, usize, usize) -> superschematic_engine::Result<T> + Sync,
) -> Vec<superschematic_engine::Result<T>> {
    thread::scope(|scope| {
        let call = &call;
        let handles: Vec<_> = (0..threads)
            .map(|thread| {
                let engine = &engines[thread % 2];
                scope.spawn(move || {
                    (0..calls)
                        .map(|n| retrying(TRIES, || call(engine, thread, n)).0)
                        .collect::<Vec<_>>()
                })
            })
            .collect();
        handles
            .into_iter()
            .flat_map(|handle| handle.join().expect("a racing thread"))
            .collect()
    })
}

fn item_document(behaviors: Value, fields: Value) -> Value {
    let mut all = vec![json!({"name": "title", "typeRef": {"name": "string"}, "required": true})];
    all.extend(fields.as_array().cloned().unwrap_or_default());
    let mut document = schema_document("Item", Value::Array(all), json!({}));
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

fn create(
    engine: &Engine,
    schema: &str,
    id: &str,
    data: Value,
) -> superschematic_engine::Result<()> {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .map(|_| ())
}

fn record(engine: &Engine, schema: &str, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), schema, id, Default::default())
        .expect("get")
        .expect("the instance")
}

// list reads every instance of a schema, page by page.
fn list(engine: &Engine, schema: &str) -> Vec<superschematic_engine::InstanceRecord> {
    let mut out = Vec::new();
    let mut cursor: Option<String> = None;
    loop {
        let page = engine
            .instances()
            .list(
                &alice(),
                schema,
                superschematic_engine::ListOptions {
                    limit: Some(500),
                    cursor: cursor.as_deref(),
                    ..Default::default()
                },
            )
            .expect("list");
        out.extend(page.items);
        match page.next {
            Some(next) => cursor = Some(next),
            None => return out,
        }
    }
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
fn racing_increments_on_two_engines_lose_no_update_and_repeat_no_seq_or_cursor() {
    for target in targets("racing_increments") {
        increments(&target, LOAD);
    }
}

fn increments(target: &Target, load: Load) {
    let engines = replicas("increments", || target.options(support::fixtures()));
    publish(
        &engines[0],
        &item_document(json!([{"name": "test.Counter"}]), json!([])),
        None,
    );
    create(&engines[1], "Item", "i1", json!({"title": "shared"})).expect("create");
    let answers = race(&engines, load.threads, load.calls, |engine, _, _| {
        engine.instances().invoke(
            &alice(),
            "Item",
            "i1",
            "increment",
            &json!({}),
            WriteOptions::default(),
        )
    });
    // Each increment read the count the one before it wrote: every
    // count from 1 to the number of calls, once.
    let mut counts: Vec<i64> = answers
        .into_iter()
        .map(|answer| {
            answer.expect("increment")["count"]
                .as_i64()
                .expect("a count")
        })
        .collect();
    counts.sort_unstable();
    let total = (load.threads * load.calls) as i64;
    assert_eq!(
        counts,
        (1..=total).collect::<Vec<_>>(),
        "{:?}",
        target.dialect
    );
    for engine in &engines {
        let read = record(engine, "Item", "i1");
        assert_eq!(read.behaviors["test.Counter"]["count"], json!(total));
        assert_eq!(read.seq, total + 1, "the create and each increment");
    }
    let history = engines[0]
        .instances()
        .invoke(
            &alice(),
            "Item",
            "i1",
            "history",
            &json!({}),
            WriteOptions::default(),
        )
        .expect("history");
    assert_eq!(
        history.as_array().map(Vec::len),
        Some(load.threads * load.calls)
    );
    // One event per write, its seq one past the one before, its cursor
    // past every earlier event's, read alike from either engine.
    let events = all_events(&engines[1], None);
    assert_rising(&events, "the log");
    let seqs: Vec<i64> = events
        .iter()
        .filter(|event| event.instance_id.as_deref() == Some("i1"))
        .map(|event| event.seq.expect("an instance event's seq"))
        .collect();
    assert_eq!(seqs, (1..=total + 1).collect::<Vec<_>>());
    assert_eq!(all_events(&engines[0], None), events);
}

#[test]
fn racing_creates_on_two_engines_hold_an_id_and_a_unique_field() {
    for target in targets("racing_creates") {
        creates(&target, LOAD);
    }
}

fn creates(target: &Target, load: Load) {
    let engines = replicas("creates", || target.options(BehaviorSet::new()));
    publish(
        &engines[0],
        &item_document(
            json!([]),
            json!([{"name": "slug", "typeRef": {"name": "string"}, "unique": true}]),
        ),
        None,
    );
    // Every thread creates the same ids: each is created once, and
    // every other create of it is a conflict.
    let answers = race(
        &engines,
        load.threads,
        load.calls / 4,
        |engine, thread, n| {
            create(
                engine,
                "Item",
                &format!("same-{n}"),
                json!({"title": format!("{thread}"), "slug": format!("slug-{thread}-{n}")}),
            )
        },
    );
    let each = load.calls / 4;
    let created = answers.iter().filter(|answer| answer.is_ok()).count();
    assert_eq!(created, each, "{:?}", target.dialect);
    for answer in answers.into_iter().filter(Result::is_err) {
        assert_eq!(refusal(answer).code(), EngineErrorCode::Conflict);
    }
    // Every thread creates its own ids with slugs the others use too:
    // each slug is held by one instance, every other create of it a
    // conflict naming the field.
    let answers = race(
        &engines,
        load.threads,
        load.calls / 4,
        |engine, thread, n| {
            create(
                engine,
                "Item",
                &format!("own-{thread}-{n}"),
                json!({"title": "own", "slug": format!("shared-{n}")}),
            )
        },
    );
    assert_eq!(answers.iter().filter(|answer| answer.is_ok()).count(), each);
    for answer in answers.into_iter().filter(Result::is_err) {
        let refused = refusal(answer);
        assert_eq!(refused.code(), EngineErrorCode::Conflict, "{refused}");
        assert!(refused.message().contains("slug"), "{refused}");
    }
    let listed = list(&engines[1], "Item");
    assert_eq!(listed.len(), 2 * each);
    let slugs: BTreeSet<String> = listed
        .iter()
        .map(|item| item.data["slug"].as_str().expect("a slug").to_owned())
        .collect();
    assert_eq!(slugs.len(), 2 * each, "no slug twice");
}

#[test]
fn racing_creates_on_two_engines_never_repeat_a_number() {
    for target in targets("racing_numbers") {
        numbers(&target, LOAD);
    }
}

fn numbers(target: &Target, load: Load) {
    let engines = replicas("numbers", || target.core_options());
    publish(
        &engines[0],
        &item_document(
            json!([{"name": "Numbering", "config": {"format": "No. {seq}"}}]),
            json!([]),
        ),
        None,
    );
    let answers = race(
        &engines,
        load.threads,
        load.calls / 2,
        |engine, thread, n| {
            create(
                engine,
                "Item",
                &format!("i-{thread}-{n}"),
                json!({"title": "numbered"}),
            )
        },
    );
    assert!(answers.iter().all(Result::is_ok), "{answers:?}");
    let mut numbers: Vec<i64> = list(&engines[0], "Item")
        .iter()
        .map(|item| {
            item.behaviors["Numbering"]["seq"]
                .as_i64()
                .expect("a number")
        })
        .collect();
    numbers.sort_unstable();
    let total = (load.threads * (load.calls / 2)) as i64;
    assert_eq!(
        numbers,
        (1..=total).collect::<Vec<_>>(),
        "every number once, with no gap: {:?}",
        target.dialect
    );
}

fn budget_document(name: &str, behaviors: Value) -> Value {
    json!({"kind": "General", "name": name, "types": {name: {
        "name": name,
        "role": "EmbeddedStruct",
        "behaviors": behaviors,
        "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]
    }}})
}

#[test]
fn racing_reservations_on_two_engines_never_pass_a_budgets_limit() {
    for target in targets("racing_reservations") {
        reservations(&target, LOAD);
    }
}

fn reservations(target: &Target, load: Load) {
    let engines = replicas("reservations", || target.workqueue_options());
    let first = &engines[0];
    publish(
        first,
        &budget_document(
            "Pool",
            json!([{"name": "Budget", "config": {"meters": {"cpu": {"limit": 100}}}}]),
        ),
        None,
    );
    publish(
        first,
        &budget_document(
            "Run",
            json!([
                {"name": "Links", "config": {"links": {"pool": {"schema": "Pool"}}}},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "pool"}}}}
            ]),
        ),
        None,
    );
    publish(
        first,
        &budget_document(
            "Step",
            json!([
                {"name": "Links", "config": {"links": {"run": {"schema": "Run"}}}},
                {"name": "Budget", "config": {"meters": {"cpu": {"scope": "run", "reserve": 30}}}}
            ]),
        ),
        None,
    );
    create(first, "Pool", "p1", json!({"title": "shared"})).expect("pool");
    create(first, "Run", "r1", json!({"title": "nightly"})).expect("run");
    let link = |schema: &str, id: &str, name: &str, to: &str| {
        first
            .instances()
            .invoke(
                &alice(),
                schema,
                id,
                "link",
                &json!({"name": name, "id": to}),
                WriteOptions::default(),
            )
            .expect("link");
    };
    link("Run", "r1", "pool", "p1");
    for thread in 0..load.threads {
        let id = format!("s{thread}");
        create(first, "Step", &id, json!({"title": id})).expect("step");
        link("Step", &id, "run", "r1");
    }
    // Each thread reserves on its own step, all drawing on the pool's
    // 100: three reservations of 30 fit, and no more.
    let answers = race(&engines, load.threads, 1, |engine, thread, _| {
        engine.instances().invoke(
            &principal(&format!("worker-{thread}")),
            "Step",
            &format!("s{thread}"),
            "reserve",
            &json!({}),
            WriteOptions::default(),
        )
    });
    let reserved = answers.iter().filter(|answer| answer.is_ok()).count();
    assert_eq!(reserved, 3, "{:?}: {answers:?}", target.dialect);
    for answer in answers {
        match answer {
            Ok(value) => assert_eq!(value, json!({"reserved": {"cpu": 30}})),
            Err(error) => {
                let (behavior, _, _, code, _) = support::veto(Err::<(), Error>(error));
                assert_eq!(
                    (behavior.as_str(), code.as_deref()),
                    ("Budget", Some("over_limit"))
                );
            }
        }
    }
    for engine in &engines {
        let pool = record(engine, "Pool", "p1");
        assert_eq!(
            pool.behaviors["Budget"]["meters"]["cpu"],
            json!({"used": 0, "reserved": 90, "limit": 100, "remaining": 10})
        );
    }
}

fn job_flow() -> Value {
    json!({
        "states": ["queued", "running", "done", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"},
            {"from": "running", "to": "failed"},
            {"from": "queued", "to": "failed"}
        ]
    })
}

fn jobs_document() -> Value {
    json!({"kind": "General", "name": "Job", "types": {"Job": {
        "name": "Job",
        "role": "EmbeddedStruct",
        "behaviors": [
            {"name": "Workflow", "config": job_flow()},
            {"name": "Lease", "config": {"onExpiry": {"transition": "queued", "from": ["running"]}}},
            {"name": "Queue", "config": {"claim": {"from": ["queued"], "to": "running"}, "priorityField": "priority"}}
        ],
        "fields": [
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "priority", "typeRef": {"name": "Generic.Int64"}}
        ]
    }}})
}

#[test]
fn racing_claims_on_two_engines_never_claim_one_job_twice() {
    for target in targets("racing_claims") {
        claims(&target, LOAD);
    }
}

fn claims(target: &Target, load: Load) {
    let engines = replicas("claims", || target.workqueue_options());
    publish(&engines[0], &jobs_document(), None);
    let jobs = load.threads * load.calls / 4;
    for n in 0..jobs {
        create(
            &engines[n % 2],
            "Job",
            &format!("j{n}"),
            json!({"title": format!("job {n}"), "priority": n % 5}),
        )
        .expect("job");
    }
    // Each thread is a worker of its own, claiming the next job until
    // none is left.
    let claimed: Vec<(String, String)> = thread::scope(|scope| {
        let handles: Vec<_> = (0..load.threads)
            .map(|thread| {
                let engine = &engines[thread % 2];
                scope.spawn(move || {
                    let worker = principal(&format!("worker-{thread}"));
                    let mut mine = Vec::new();
                    loop {
                        let (answer, _) = retrying(TRIES, || {
                            engine.instances().invoke_schema(
                                &worker,
                                "Job",
                                "claimNext",
                                &json!({}),
                                None,
                            )
                        });
                        let answer = answer.expect("claimNext");
                        let Some(id) = answer["claimed"]["id"].as_str() else {
                            return mine;
                        };
                        mine.push((id.to_owned(), worker.subject.clone()));
                    }
                })
            })
            .collect();
        handles
            .into_iter()
            .flat_map(|handle| handle.join().expect("a worker"))
            .collect()
    });
    assert_eq!(claimed.len(), jobs, "{:?}: {claimed:?}", target.dialect);
    let ids: BTreeSet<&str> = claimed.iter().map(|(id, _)| id.as_str()).collect();
    assert_eq!(ids.len(), jobs, "no job claimed twice");
    for (id, worker) in &claimed {
        let job = record(&engines[1], "Job", id);
        assert_eq!(job.behaviors["Lease"]["holder"], json!(worker), "{id}");
        assert_eq!(
            job.behaviors["Workflow"]["status"],
            json!("running"),
            "{id}"
        );
    }
}

#[test]
fn writers_of_different_namespaces_on_two_engines_do_not_wait_for_each_other() {
    for target in targets("namespaces_do_not_wait") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let paused = Pause::default();
        let hold = paused.hold();
        let behaviors = || BehaviorSet::new().with(paused.clone());
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("holder"), false);
        let holder = Engine::open(options).expect("the holder");
        // The other replica gives up on a lock after 300 ms.
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("other"), false);
        set_busy_timeout(&mut options, Duration::from_millis(300));
        let other = Engine::open(options).expect("the other");
        let document = item_document(json!([{"name": "test.Pause"}]), json!([]));
        for namespace in ["a", "b"] {
            holder
                .namespaces()
                .create(&alice(), namespace)
                .expect("namespace");
            publish(&holder, &document, Some(namespace));
            holder
                .instances()
                .create(
                    &alice(),
                    "Item",
                    &json!({"title": namespace}),
                    CreateOptions {
                        namespace: Some(namespace),
                        id: Some("i1"),
                        ..Default::default()
                    },
                )
                .expect("create");
        }
        thread::scope(|scope| {
            let held = scope.spawn(|| pause(&holder, &alice(), Some("a"), "Item", "i1", "held"));
            assert_eq!(hold.entered(), "held");
            // Namespace a's writer holds its lock: b's writes go on at once.
            let started = Instant::now();
            for n in 0..5 {
                other
                    .instances()
                    .create(
                        &alice(),
                        "Item",
                        &json!({"title": "beside"}),
                        CreateOptions {
                            namespace: Some("b"),
                            id: Some(&format!("b{n}")),
                            ..Default::default()
                        },
                    )
                    .expect("a write in another namespace");
            }
            assert!(
                started.elapsed() < Duration::from_secs(10),
                "b's writes waited {:?}",
                started.elapsed()
            );
            // A write in a waits for a's writer, and past the busy timeout
            // is busy, having written nothing.
            let refused = refusal(other.instances().create(
                &alice(),
                "Item",
                &json!({"title": "behind"}),
                CreateOptions {
                    namespace: Some("a"),
                    id: Some("a2"),
                    ..Default::default()
                },
            ));
            assert_eq!(refused.code(), EngineErrorCode::Busy, "{refused}");
            hold.release();
            held.join().expect("the held call").expect("pause");
        });
        let a = all_events(&other, Some("a"));
        assert!(
            a.iter()
                .all(|event| event.instance_id.as_deref() != Some("a2")),
            "the busy write wrote nothing"
        );
        // Once a's writer commits, the other's write in a goes through.
        other
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "after"}),
                CreateOptions {
                    namespace: Some("a"),
                    id: Some("a2"),
                    ..Default::default()
                },
            )
            .expect("a write once the lock is free");
        assert!(!holder.poisoned() && !other.poisoned());
    }
}

/// A racing case, run at a load.
type Case = fn(&Target, Load);

/// The racing cases long, on Postgres alone, each in a schema of its own:
/// eight threads on each engine, twice either pool's connections,
/// `ENGINE_SOAK_CALLS` calls each (200 unless set). `make engine-rust-soak`
/// runs it.
#[test]
#[ignore = "a soak; make engine-rust-soak runs it"]
fn soak_two_engines_racing_writes() {
    let load = soak_load();
    let cases: [(&str, Case); 5] = [
        ("increments", increments),
        ("creates", creates),
        ("numbers", numbers),
        ("reservations", reservations),
        ("claims", claims),
    ];
    for (name, case) in cases {
        for target in targets(name) {
            if target.dialect != Dialect::Postgres {
                continue;
            }
            let started = Instant::now();
            case(&target, load);
            eprintln!("soak {name} {load:?}: {:?}", started.elapsed());
        }
    }
}
