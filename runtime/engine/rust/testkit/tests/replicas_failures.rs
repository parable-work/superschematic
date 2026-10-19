//! What a replica of a server meets on Postgres besides another replica's
//! writes, and how the engine answers it (D58):
//!
//! - A session the server ends inside a call's transaction (a failover, a
//!   proxy, `pg_terminate_backend`): the call is `unavailable` and wrote
//!   nothing; the lock it held is free at once for the other replicas;
//!   the pool drops the connection and the next call runs.
//! - Idle connections the server ended (a restart): the pool finds each
//!   closed at BEGIN and takes another, so no call fails.
//! - A connection lost while the transaction commits: `unavailable`,
//!   saying the outcome is unknown.
//! - A replica frozen inside a transaction (paused, cut off without the
//!   network telling): the server ends its session once it has sat idle
//!   there past `idle_in_transaction_session_timeout` (60 s unless
//!   configured), and its lock goes with it.
//! - A statement the server's `statement_timeout` cancels, a lock wait past
//!   `lock_timeout`, a deadlock and a serialization failure: `busy`, with
//!   nothing written and, over HTTP, `Retry-After`.
//! - None of these poisons an engine: only a panic inside a transaction
//!   does (one in the access policy, say), and then that engine alone,
//!   whose locks go with its rolled-back transaction.
//!
//! Each case runs on Postgres when `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL`
//! names one, in a schema of its own; the poisoning case on SQLite too.

mod replicas_support;
mod support;

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Barrier};
use std::thread;
use std::time::Duration;

use serde_json::{json, Value};

use replicas_support::{as_replica, pause, replica_name, set_busy_timeout, Admin, Pause};
use superschematic_engine::storage::{Db, Dialect, Error as StorageError, Lock, SqlValue};
use superschematic_engine::{
    BehaviorSet, CreateOptions, Engine, EngineErrorCode, EngineOptions, Error, GetOptions, Policy,
    Storage,
};
use support::{alice, publish, refusal, schema_document, targets, RecordingPolicy};

fn item_document(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Item",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Item"]["behaviors"] = behaviors;
    document
}

fn create(engine: &Engine, id: &str) -> superschematic_engine::Result<()> {
    engine
        .instances()
        .create(
            &alice(),
            "Item",
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .map(|_| ())
}

fn exists(engine: &Engine, id: &str) -> bool {
    engine
        .instances()
        .get(&alice(), "Item", id, GetOptions::default())
        .expect("get")
        .is_some()
}

// Adds a setting every session of a replica runs with (`-c name=value`).
fn with_setting(options: &mut EngineOptions, setting: &str) {
    if let Storage::Postgres { url, .. } = &mut options.storage {
        let joiner = if url.contains('?') { '&' } else { '?' };
        let encoded = format!("-c {setting}")
            .replace('%', "%25")
            .replace(' ', "%20")
            .replace('=', "%3D");
        url.push_str(&format!("{joiner}options={encoded}"));
    }
}

#[test]
fn a_session_lost_inside_a_call_fails_it_as_unavailable_frees_its_lock_and_the_pool_goes_on() {
    for target in targets("a_session_lost_inside_a_call") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let paused = Pause::default();
        let hold = paused.hold();
        let behaviors = || BehaviorSet::new().with(paused.clone());
        let holder_name = replica_name("holder");
        let mut options = target.options(behaviors());
        as_replica(&mut options, &holder_name, false);
        let holder = Engine::open(options).expect("the holder");
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("other"), false);
        set_busy_timeout(&mut options, Duration::from_millis(300));
        let other = Engine::open(options).expect("the other");
        publish(
            &holder,
            &item_document(json!([{"name": "test.Pause"}])),
            None,
        );
        create(&holder, "i1").expect("create");
        thread::scope(|scope| {
            let held = scope.spawn(|| pause(&holder, &alice(), None, "Item", "i1", "held"));
            assert_eq!(hold.entered(), "held");
            assert_eq!(admin.terminate_in_transaction(&holder_name), 1);
            // The namespace's lock went with the session: the other
            // replica's write goes through before the held call returns.
            create(&other, "beside").expect("a write once the session is gone");
            hold.release();
            let refused = refusal(held.join().expect("the held call"));
            assert_eq!(refused.code(), EngineErrorCode::Unavailable, "{refused}");
            assert!(
                refused.message().contains("nothing was written"),
                "{refused}"
            );
        });
        // The held call wrote nothing: i1 has its create alone.
        let i1 = holder
            .instances()
            .get(&alice(), "Item", "i1", GetOptions::default())
            .expect("get")
            .expect("i1");
        assert_eq!(i1.seq, 1);
        // The pool dropped the connection; the next calls run.
        for n in 0..3 {
            create(&holder, &format!("after{n}")).expect("a call after the loss");
        }
        assert!(!holder.poisoned() && !other.poisoned());
    }
}

#[test]
fn a_replica_frozen_inside_a_transaction_loses_its_session_and_lock_after_the_idle_timeout() {
    for target in targets("frozen_inside_a_transaction") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let paused = Pause::default();
        let hold = paused.hold();
        let behaviors = || BehaviorSet::new().with(paused.clone());
        // The holder's sessions may sit idle in a transaction 500 ms; its
        // held call waits there far longer, as a frozen process would.
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("frozen"), false);
        if let Storage::Postgres { options, .. } = &mut options.storage {
            options.idle_in_transaction_timeout = Some(Duration::from_millis(500));
        }
        let frozen = Engine::open(options).expect("the frozen replica");
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("waiting"), false);
        set_busy_timeout(&mut options, Duration::from_secs(20));
        let waiting = Engine::open(options).expect("the waiting replica");
        publish(
            &waiting,
            &item_document(json!([{"name": "test.Pause"}])),
            None,
        );
        create(&waiting, "i1").expect("create");
        thread::scope(|scope| {
            let held = scope.spawn(|| pause(&frozen, &alice(), None, "Item", "i1", "held"));
            assert_eq!(hold.entered(), "held");
            // The waiting replica's write in the namespace waits for the
            // frozen one's lock, which the server lets go of once it ends
            // the idle session.
            let started = std::time::Instant::now();
            create(&waiting, "after").expect("a write once the server ends the frozen session");
            assert!(
                started.elapsed() < Duration::from_secs(15),
                "waited {:?}",
                started.elapsed()
            );
            hold.release();
            let refused = refusal(held.join().expect("the held call"));
            assert_eq!(refused.code(), EngineErrorCode::Unavailable, "{refused}");
        });
        assert_eq!(
            frozen
                .instances()
                .get(&alice(), "Item", "i1", GetOptions::default())
                .expect("get")
                .expect("i1")
                .seq,
            1,
            "the frozen call wrote nothing"
        );
        create(&frozen, "thawed").expect("the frozen replica's next call");
        assert!(!frozen.poisoned() && !waiting.poisoned());
    }
}

#[test]
fn a_pool_whose_idle_connections_the_server_ended_fails_no_call() {
    for target in targets("idle_connections_ended") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let admin = Admin::new(&target).expect("the administrator");
        let name = replica_name("restarted");
        let mut options = target.options(BehaviorSet::new());
        as_replica(&mut options, &name, false);
        let engine = Engine::open(options).expect("open");
        publish(&engine, &item_document(json!([])), None);
        // Every connection of the pool opened, then left idle.
        let start = Barrier::new(4);
        thread::scope(|scope| {
            for n in 0..4 {
                let (engine, start) = (&engine, &start);
                scope.spawn(move || {
                    start.wait();
                    create(engine, &format!("warm{n}")).expect("create");
                });
            }
        });
        assert!(admin.terminate_replica(&name) >= 1, "the sessions end");
        for n in 0..8 {
            create(&engine, &format!("after{n}")).unwrap_or_else(|error| {
                panic!("call {n} after the server ended the sessions: {error}")
            });
        }
        assert!(exists(&engine, "after7"));
        assert!(!engine.poisoned());
    }
}

#[test]
fn a_connection_lost_while_a_call_commits_is_unavailable_and_says_its_outcome_is_unknown() {
    for target in targets("lost_at_commit") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let engine = target.open(BehaviorSet::new());
        publish(&engine, &item_document(json!([])), None);
        // A deferred trigger that ends its own session as the transaction
        // that appended the doomed instance's event commits.
        target
            .db()
            .write(Lock::Catalog, |tx| {
                tx.exec(
                    "CREATE FUNCTION end_my_session() RETURNS trigger LANGUAGE plpgsql AS $$
                     BEGIN PERFORM pg_terminate_backend(pg_backend_pid()); RETURN NULL; END $$;
                     CREATE CONSTRAINT TRIGGER end_at_commit AFTER INSERT ON engine_events
                       DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
                       WHEN (NEW.instance_id = 'doomed') EXECUTE FUNCTION end_my_session()",
                )
            })
            .expect("the trigger");
        let refused = refusal(create(&engine, "doomed"));
        assert_eq!(refused.code(), EngineErrorCode::Unavailable, "{refused}");
        assert!(
            refused
                .message()
                .contains("whether it was written is unknown"),
            "{refused}"
        );
        // Here the session ended before the commit did, so nothing was.
        assert!(!exists(&engine, "doomed"));
        create(&engine, "fine").expect("the next call");
        assert!(!engine.poisoned());
    }
}

#[test]
fn a_statement_the_server_times_out_is_busy_and_writes_nothing() {
    for target in targets("statement_timeout") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let paused = Pause::default();
        let hold = paused.hold();
        let behaviors = || BehaviorSet::new().with(paused.clone());
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("holder"), false);
        let holder = Engine::open(options).expect("the holder");
        // The other replica's sessions cancel a statement after 300 ms,
        // well before its lock timeout.
        let mut options = target.options(behaviors());
        as_replica(&mut options, &replica_name("timed"), false);
        set_busy_timeout(&mut options, Duration::from_secs(20));
        with_setting(&mut options, "statement_timeout=300");
        let timed = Engine::open(options).expect("the timed replica");
        publish(
            &holder,
            &item_document(json!([{"name": "test.Pause"}])),
            None,
        );
        create(&holder, "i1").expect("create");
        thread::scope(|scope| {
            let held = scope.spawn(|| pause(&holder, &alice(), None, "Item", "i1", "held"));
            assert_eq!(hold.entered(), "held");
            let refused = refusal(create(&timed, "waited"));
            assert_eq!(refused.code(), EngineErrorCode::Busy, "{refused}");
            assert!(refused.message().contains("statement timeout"), "{refused}");
            hold.release();
            held.join().expect("the held call").expect("pause");
        });
        assert!(!exists(&holder, "waited"));
        create(&timed, "waited").expect("the write once the lock is free");
        assert!(!holder.poisoned() && !timed.poisoned());
    }
}

// The engine's answer to a storage failure: its refusal's code, or None.
fn engine_code(error: StorageError) -> Option<EngineErrorCode> {
    Error::from(error).code()
}

#[test]
fn deadlocks_serialization_failures_and_cancels_are_busy_and_a_lost_session_unavailable() {
    for target in targets("database_failures") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let db = target.db();
        db.write(Lock::Catalog, |tx| {
            tx.exec("CREATE TABLE scratch (k INTEGER PRIMARY KEY, v INTEGER NOT NULL); INSERT INTO scratch VALUES (1, 0), (2, 0)")
        })
        .expect("the scratch table");
        // Each failure the server raises, read as the storage and the
        // engine read it.
        for (state, busy, connection) in [
            ("40001", true, false),
            ("40P01", true, false),
            ("55P03", true, false),
            ("57014", true, false),
            ("57P01", false, true),
            ("08006", false, true),
            ("22012", false, false),
        ] {
            let failed = db
                .write(Lock::Namespace("a"), |tx| {
                    tx.exec(format!(
                        "DO $$ BEGIN RAISE EXCEPTION 'raised' USING ERRCODE = '{state}'; END $$"
                    ))
                })
                .expect_err(state);
            assert_eq!(failed.is_busy(), busy, "{state}: {failed:?}");
            assert_eq!(failed.is_connection(), connection, "{state}: {failed:?}");
            let code = engine_code(failed);
            let expected = if busy {
                Some(EngineErrorCode::Busy)
            } else if connection {
                Some(EngineErrorCode::Unavailable)
            } else {
                None
            };
            assert_eq!(code, expected, "{state}");
        }
        // A real deadlock between two writers of two namespaces, each
        // updating the rows the other updates next, in the other order:
        // one is busy, the other commits.
        let dbs = [target.db(), target.db()];
        let start = Arc::new(Barrier::new(2));
        let results: Vec<Result<(), StorageError>> = thread::scope(|scope| {
            let handles: Vec<_> = [("a", 1, 2), ("b", 2, 1)]
                .into_iter()
                .zip(&dbs)
                .map(
                    |((namespace, first, second), db): ((&str, i64, i64), &Db)| {
                        let start = Arc::clone(&start);
                        scope.spawn(move || {
                            db.write(Lock::Namespace(namespace), |tx| {
                                tx.run(
                                    "UPDATE scratch SET v = v + 1 WHERE k = ?",
                                    &[SqlValue::Integer(first)],
                                )?;
                                start.wait();
                                tx.run(
                                    "UPDATE scratch SET v = v + 1 WHERE k = ?",
                                    &[SqlValue::Integer(second)],
                                )?;
                                Ok(())
                            })
                        })
                    },
                )
                .collect();
            handles
                .into_iter()
                .map(|handle| handle.join().expect("a writer"))
                .collect()
        });
        let busy = results
            .iter()
            .filter(|result| matches!(result, Err(error) if error.is_busy()))
            .count();
        let committed = results.iter().filter(|result| result.is_ok()).count();
        assert_eq!((busy, committed), (1, 1), "{results:?}");
        let sum = db
            .read(|tx| {
                tx.get("SELECT SUM(v)::bigint AS sum FROM scratch", &[])?
                    .expect("a row")
                    .i64("sum")
            })
            .expect("the sum");
        assert_eq!(sum, 2, "the one that committed wrote both rows");
    }
}

#[test]
fn only_a_panic_inside_a_transaction_poisons_and_that_engine_alone_whose_lock_goes() {
    for target in targets("a_panic_poisons_one") {
        let panicking = Arc::new(AtomicBool::new(false));
        let policy = {
            let panicking = Arc::clone(&panicking);
            RecordingPolicy::new(move |_| {
                if panicking.load(Ordering::SeqCst) {
                    panic!("the policy fell over");
                }
                true
            })
        };
        let mut options = target.options(BehaviorSet::new());
        options.policy = Some(Policy::custom(policy));
        as_replica(&mut options, &replica_name("faulty"), false);
        let faulty = Engine::open(options).expect("the faulty replica");
        let mut options = target.options(BehaviorSet::new());
        as_replica(&mut options, &replica_name("sound"), false);
        set_busy_timeout(&mut options, Duration::from_secs(2));
        let sound = Engine::open(options).expect("the sound replica");
        publish(&sound, &item_document(json!([])), None);
        panicking.store(true, Ordering::SeqCst);
        let failed = create(&faulty, "panicked").expect_err("the panic");
        assert!(
            matches!(&failed, Error::Internal(message) if message.contains("panicked inside a transaction")),
            "{:?}: {failed:?}",
            target.dialect
        );
        assert!(faulty.poisoned());
        panicking.store(false, Ordering::SeqCst);
        assert!(
            matches!(create(&faulty, "later"), Err(Error::Internal(message)) if message.contains("refuses every call")),
            "a poisoned engine refuses every later call"
        );
        // The other replica is not poisoned, and its writes in the same
        // namespace do not wait: the faulty one's transaction rolled back.
        create(&sound, "beside").expect("a write beside the poisoned replica");
        assert!(!exists(&sound, "panicked"));
        assert!(!sound.poisoned());
    }
}
