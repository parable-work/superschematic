//! Signals across replicas (D77): a signal one engine on a Postgres schema
//! sends, a caller's or an operation's, reaches a stream another engine on
//! the same schema serves, through the engine's own `pg_notify` channel and
//! the other's listener, at most once; an engine on another schema of the
//! same database hears none of it. Postgres only, when
//! `SUPERSCHEMATIC_ENGINE_TEST_DATABASE_URL` names one.

mod replicas_support;
mod support;

use std::time::{Duration, Instant};

use serde_json::{json, Value};

use replicas_support::{as_replica, replica_name, wait_for, PATIENCE};
use superschematic_engine::api::{start_stream_with, EventStream, Pull, StreamOptions};
use superschematic_engine::storage::Dialect;
use superschematic_engine::{
    BehaviorSet, CreateOptions, Engine, EventsAfter, ReadEvents, SendSignal,
};
use superschematic_engine_testkit::behaviors::Chatter;
use support::{alice, named, publish, schema_document, targets, Target};

fn open(target: &Target, name: &str, listen: bool) -> Engine {
    let mut options = target.options(BehaviorSet::new().with(Chatter));
    as_replica(&mut options, name, listen);
    Engine::open(options).expect("open a replica")
}

fn ready(engine: &Engine) -> EventStream {
    let mut stream = start_stream_with(
        engine,
        &StreamOptions {
            page_size: None,
            heartbeat_ms: Some(600_000),
        },
        &alice(),
        &ReadEvents {
            after: Some(EventsAfter::Head),
            ..ReadEvents::default()
        },
        true,
    )
    .expect("a stream");
    let mut opened = String::new();
    while !opened.contains("event: ready") {
        match stream.next(PATIENCE) {
            Pull::Data(bytes) => opened.push_str(&String::from_utf8(bytes).unwrap()),
            other => panic!("{other:?}"),
        }
    }
    stream
}

// The signals a stream sends within `wait`.
fn signals(stream: &mut EventStream, want: usize, wait: Duration) -> Vec<Value> {
    let deadline = Instant::now() + wait;
    let mut out = Vec::new();
    while out.len() < want && Instant::now() < deadline {
        if let Pull::Data(bytes) = stream.next(deadline.saturating_duration_since(Instant::now())) {
            for message in String::from_utf8(bytes).unwrap().split("\n\n") {
                if let Some(data) = message.strip_prefix("event: signal\ndata: ") {
                    out.push(serde_json::from_str(data).unwrap());
                }
            }
        }
    }
    out
}

#[test]
fn a_signal_sent_through_one_replica_reaches_a_stream_another_serves() {
    for target in targets("a_signal_reaches_another_replica") {
        if target.dialect != Dialect::Postgres {
            continue;
        }
        let sender = open(&target, &replica_name("sender"), true);
        let server = open(&target, &replica_name("server"), true).shared();
        let mut document = schema_document(
            "Item",
            json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
            json!({}),
        );
        document["types"]["Item"]["behaviors"] = json!([{"name": "test.Chatter"}]);
        publish(&sender, &document, None);
        sender
            .instances()
            .create(
                &alice(),
                "Item",
                &json!({"title": "Desk"}),
                CreateOptions {
                    id: Some("a1"),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
        wait_for("both listen", || {
            sender.db().notifier().listening() && server.db().notifier().listening()
        });
        let mut stream = ready(&server);
        // A caller's, then an operation's after its commit.
        sender
            .signals()
            .send(
                &named("bob"),
                &SendSignal {
                    namespace: None,
                    schema: "Item",
                    instance_id: "a1",
                    behavior: "test.Chatter",
                    name: "typing",
                    data: &json!({"thread": 1}),
                },
            )
            .unwrap();
        sender
            .instances()
            .invoke(
                &alice(),
                "Item",
                "a1",
                "draft",
                &json!({"text": "hello"}),
                Default::default(),
            )
            .unwrap();
        let heard = signals(&mut stream, 2, PATIENCE);
        let names: Vec<(&str, &str)> = heard
            .iter()
            .map(|signal| {
                (
                    signal["name"].as_str().unwrap(),
                    signal["from"].as_str().unwrap(),
                )
            })
            .collect();
        assert_eq!(names, [("typing", "bob"), ("draft", "alice")]);
        assert_eq!(heard[1]["data"], json!({"text": "hello"}));
        // At most once: nothing more comes.
        assert!(signals(&mut stream, 1, Duration::from_millis(300)).is_empty());
        // The sender's own stream hears its own once too, through its
        // listener.
        let sender = sender.shared();
        let mut own = ready(&sender);
        sender
            .signals()
            .send(
                &named("bob"),
                &SendSignal {
                    namespace: None,
                    schema: "Item",
                    instance_id: "a1",
                    behavior: "test.Chatter",
                    name: "nudge",
                    data: &json!(1),
                },
            )
            .unwrap();
        assert_eq!(signals(&mut own, 1, PATIENCE).len(), 1);
        assert_eq!(signals(&mut stream, 1, PATIENCE)[0]["name"], "nudge");
        assert!(signals(&mut own, 1, Duration::from_millis(300)).is_empty());
        server.shutdown();
        sender.shutdown();
    }
}

#[test]
fn an_engine_on_another_schema_of_the_database_hears_none() {
    let mut targets = targets("another_schema_hears_none");
    targets.retain(|target| target.dialect == Dialect::Postgres);
    let mut targets_two = support::targets("another_schema_hears_none_too");
    targets_two.retain(|target| target.dialect == Dialect::Postgres);
    for (one, two) in targets.iter().zip(&targets_two) {
        let sender = open(one, &replica_name("sender"), true);
        let other = open(two, &replica_name("other"), true).shared();
        let mut document = schema_document(
            "Item",
            json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
            json!({}),
        );
        document["types"]["Item"]["behaviors"] = json!([{"name": "test.Chatter"}]);
        publish(&sender, &document, None);
        publish(&other, &document, None);
        for engine in [&sender, &*other] {
            engine
                .instances()
                .create(
                    &alice(),
                    "Item",
                    &json!({"title": "Desk"}),
                    CreateOptions {
                        id: Some("a1"),
                        ..CreateOptions::default()
                    },
                )
                .unwrap();
        }
        wait_for("both listen", || {
            sender.db().notifier().listening() && other.db().notifier().listening()
        });
        let mut stream = ready(&other);
        sender
            .signals()
            .send(
                &alice(),
                &SendSignal {
                    namespace: None,
                    schema: "Item",
                    instance_id: "a1",
                    behavior: "test.Chatter",
                    name: "typing",
                    data: &json!({}),
                },
            )
            .unwrap();
        assert!(signals(&mut stream, 1, Duration::from_millis(500)).is_empty());
        other.shutdown();
    }
}
