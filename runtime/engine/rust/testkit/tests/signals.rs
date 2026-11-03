//! Signals (D77): a message about an instance that the engine delivers to
//! the event streams open on it, as `event: signal` with no `id:`, and
//! keeps nowhere. A caller sends one of `read` or `write` access, asked of
//! the policy as `signal:<name>`, its data checked and its rate limited per
//! subject and instance; one of `operation` access only the behavior's
//! operations send, through `cx.signal`, after the call commits, in order,
//! and never when it rolls back, unlimited. A stream opened with signals
//! receives those its filters keep on schemas its principal may read; one
//! opened without receives none, and a replay never does. On SQLite and,
//! with the variable set, on Postgres.

mod support;

use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::api::{
    handle, start_stream_with, ApiOptions, Caller, EventStream, Pull, Request, StreamOptions,
};
use superschematic_engine::behaviors::{Every, Reactions, Schedule};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SignalSink;
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, Engine,
    EngineErrorCode, EventsAfter, Hooks, Policy, Principal, ReadEvents, Result, RunnerOptions,
    SendSignal, SignalOptions,
};
use support::{alice, code, defect, named, publish, targets, RecordingPolicy, Target};

type Seen = Arc<Mutex<Vec<Value>>>;

const PATIENCE: Duration = Duration::from_secs(20);

fn declaration() -> String {
    let none = json!({"type": "object", "additionalProperties": false});
    let texts = json!({"type": "object", "additionalProperties": false, "required": ["texts"],
                       "properties": {"texts": {"type": "array", "items": {"type": "string"}}}});
    json!({
        "name": "test.Talk",
        "operations": [
            {"name": "say", "paramsSchema": texts, "resultSchema": true, "writes": true, "beside": true},
            {"name": "trySay", "paramsSchema": texts, "resultSchema": true, "writes": true, "beside": true},
            {"name": "failSay", "paramsSchema": texts, "resultSchema": true, "writes": true, "beside": true},
            {"name": "mumble", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true},
            {"name": "garble", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true},
            {"name": "glance", "paramsSchema": none, "resultSchema": true}
        ],
        "signals": [
            {"name": "typing", "description": "Who is typing.", "access": "read",
             "paramsSchema": {"type": "object", "additionalProperties": false, "properties": {"thread": {"type": "integer"}}}},
            {"name": "nudge", "access": "write", "paramsSchema": true},
            {"name": "draft", "access": "operation",
             "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["text"], "properties": {"text": {"type": "string"}}}}
        ],
        "vetoes": [{"code": "refused"}]
    })
    .to_string()
}

// test.Talk sends its draft signal from its operations: `say` one per
// text, noting what the test's own sink had heard by then; `trySay` calls
// `failSay`, whose draft rolls back with its veto, then says its own;
// `mumble` sends an undeclared signal and `garble` a draft of bad data;
// `glance`, read-only, sends one too. Its reaction says "reacted" on each
// instance a caller creates, and its schedule says "interrupted" on Item
// a1, as the runner.
struct Talk {
    heard: Seen,
    before_commit: Arc<Mutex<Vec<usize>>>,
}

fn texts(params: &Value) -> Vec<String> {
    params["texts"]
        .as_array()
        .map(|texts| {
            texts
                .iter()
                .filter_map(Value::as_str)
                .map(str::to_owned)
                .collect()
        })
        .unwrap_or_default()
}

impl Behavior for Talk {
    type Config = Value;

    fn declaration(&self) -> &str {
        static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(declaration);
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            operations: vec![
                ("say", |talk, cx, params| {
                    let said = texts(params);
                    for text in &said {
                        cx.signal("draft", &json!({ "text": text }))?;
                    }
                    let heard = talk.heard.lock().unwrap().len();
                    talk.before_commit.lock().unwrap().push(heard);
                    Ok(json!(said.len()))
                }),
                ("trySay", |_, cx, params| {
                    let refused = cx.call("test.Talk", "failSay", params);
                    assert!(refused.is_err());
                    cx.signal("draft", &json!({"text": "kept"}))?;
                    Ok(Value::Null)
                }),
                ("failSay", |_, cx, params| {
                    for text in texts(params) {
                        cx.signal("draft", &json!({ "text": text }))?;
                    }
                    Err(superschematic_engine::Error::veto(
                        "test.Talk",
                        "failSay",
                        cx.schema(),
                        cx.id(),
                        Veto {
                            reason: "refused as asked".to_owned(),
                            code: Some("refused".to_owned()),
                            details: None,
                        },
                    ))
                }),
                ("mumble", |_, cx, _| {
                    cx.signal("whisper", &json!({}))?;
                    Ok(Value::Null)
                }),
                ("garble", |_, cx, _| {
                    cx.signal("draft", &json!({"text": 7}))?;
                    Ok(Value::Null)
                }),
                ("glance", |_, cx, _| {
                    cx.signal("draft", &json!({"text": "glanced"}))?;
                    Ok(Value::Null)
                }),
            ],
            reactions: Some(Reactions {
                watches: None,
                react: |_, cx, event| {
                    if event.kind == "create" && event.cause.is_none() {
                        cx.instances().invoke(
                            &event.schema,
                            event.instance_id.as_deref().unwrap_or_default(),
                            "say",
                            &json!({"texts": ["reacted"]}),
                            None,
                        )?;
                    }
                    Ok(())
                },
            }),
            schedules: vec![(
                "interrupt",
                Schedule {
                    every: Every::Ms(1_000),
                    run: |_, cx| {
                        if cx.schema() == "Item" {
                            cx.instances().invoke(
                                "Item",
                                "a1",
                                "say",
                                &json!({"texts": ["interrupted"]}),
                                None,
                            )?;
                        }
                        Ok(())
                    },
                },
            )],
            ..Hooks::default()
        }
    }
}

fn document(name: &str) -> Value {
    let mut document = support::schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "test.Talk"}]);
    document
}

/// An engine whose Item and Secret compose test.Talk, with items a1 and a2
/// and secret s1, a manual clock, the runner, and a sink of its own on the
/// storage's signals, which `heard` fills.
struct World {
    engine: Arc<Engine>,
    heard: Seen,
    before_commit: Arc<Mutex<Vec<usize>>>,
    _sink: Arc<SignalSink>,
}

fn opened(target: &Target, policy: Option<Policy>, per_second: Option<u32>) -> World {
    let heard = Seen::default();
    let before_commit = Arc::new(Mutex::new(Vec::new()));
    let talk = Talk {
        heard: Arc::clone(&heard),
        before_commit: Arc::clone(&before_commit),
    };
    let engine = target
        .open_with(BehaviorSet::new().with(talk), |options| {
            options.clock = Clock::Manual(1_000_000);
            options.runner = Some(RunnerOptions::new(Principal::new(
                "runner",
                Vec::<String>::new(),
            )));
            options.signals = SignalOptions { per_second };
            if let Some(policy) = policy {
                options.policy = Some(policy);
            }
        })
        .shared();
    let sink: Arc<SignalSink> = {
        let heard = Arc::clone(&heard);
        Arc::new(move |payload: &str| {
            heard
                .lock()
                .unwrap()
                .push(serde_json::from_str(payload).expect("a signal's JSON"));
        })
    };
    engine.db().signals().subscribe(&sink);
    publish(&engine, &document("Item"), None);
    publish(&engine, &document("Secret"), None);
    for (schema, id) in [("Item", "a1"), ("Item", "a2"), ("Secret", "s1")] {
        engine
            .instances()
            .create(
                &alice(),
                schema,
                &json!({"title": id}),
                CreateOptions {
                    id: Some(id),
                    ..CreateOptions::default()
                },
            )
            .unwrap();
    }
    World {
        engine,
        heard,
        before_commit,
        _sink: sink,
    }
}

fn world(target: &Target) -> World {
    opened(target, None, None)
}

impl World {
    fn send(&self, who: &Principal, schema: &str, id: &str, name: &str, data: Value) -> Result<()> {
        self.engine.signals().send(
            who,
            &SendSignal {
                namespace: None,
                schema,
                instance_id: id,
                behavior: "test.Talk",
                name,
                data: &data,
            },
        )
    }

    // say invokes an operation on a1 as alice: with texts for one that
    // takes them, and none for one that takes none.
    fn say(&self, operation: &str, said: &[&str]) -> Result<Value> {
        let params = if matches!(operation, "glance" | "mumble" | "garble") {
            json!({})
        } else {
            json!({ "texts": said })
        };
        self.engine.instances().invoke(
            &alice(),
            "Item",
            "a1",
            operation,
            &params,
            Default::default(),
        )
    }

    // stream opens a stream from the head as `who`, with the read's
    // filters, past its ready event.
    fn stream(&self, who: &Principal, read: ReadEvents<'_>, signals: bool) -> EventStream {
        let mut stream = start_stream_with(
            &self.engine,
            &StreamOptions {
                page_size: None,
                heartbeat_ms: Some(600_000),
            },
            who,
            &ReadEvents {
                after: Some(EventsAfter::Head),
                ..read
            },
            signals,
        )
        .expect("a stream");
        let mut opened = String::new();
        while !opened.contains("event: ready") {
            match stream.next(PATIENCE) {
                Pull::Data(bytes) => opened.push_str(&String::from_utf8(bytes).unwrap()),
                other => panic!("expected the stream to open, got {other:?}"),
            }
        }
        stream
    }

    fn heard(&self) -> Vec<Value> {
        self.heard.lock().unwrap().clone()
    }
}

// The messages a stream sends until `want` signals came, or, with `want`
// 0, until it has nothing for a moment: the signals, and the other
// messages' first lines.
fn pull(stream: &mut EventStream, want: usize) -> (Vec<Value>, Vec<String>) {
    let deadline = Instant::now() + PATIENCE;
    let (mut signals, mut others) = (Vec::new(), Vec::new());
    loop {
        assert!(
            Instant::now() < deadline,
            "{} of {want} signals: {signals:?}",
            signals.len()
        );
        let wait = if want == 0 {
            Duration::from_millis(150)
        } else {
            PATIENCE
        };
        match stream.next(wait) {
            Pull::Data(bytes) => {
                let text = String::from_utf8(bytes).unwrap();
                for message in text.split("\n\n").filter(|message| !message.is_empty()) {
                    if message.starts_with("event: signal\n") {
                        assert!(
                            !message.lines().any(|line| line.starts_with("id:")),
                            "a signal carries no id: {message}"
                        );
                        let data = message.strip_prefix("event: signal\ndata: ").unwrap();
                        signals.push(serde_json::from_str(data).unwrap());
                    } else {
                        others.push(message.lines().next().unwrap_or_default().to_owned());
                    }
                }
                if want > 0 && signals.len() >= want {
                    return (signals, others);
                }
            }
            Pull::Pending if want == 0 => return (signals, others),
            other => panic!("expected {want} signals, got {other:?} after {signals:?}"),
        }
    }
}

fn texts_of(signals: &[Value]) -> Vec<String> {
    signals
        .iter()
        .map(|signal| {
            format!(
                "{} {} {}",
                signal["instanceId"].as_str().unwrap(),
                signal["name"].as_str().unwrap(),
                signal["data"]
            )
        })
        .collect()
}

#[test]
fn a_stream_with_signals_receives_a_callers_and_one_without_receives_none() {
    for target in targets("a_stream_with_signals_receives") {
        let world = world(&target);
        let mut with = world.stream(&alice(), ReadEvents::default(), true);
        let mut without = world.stream(&alice(), ReadEvents::default(), false);
        world
            .send(&named("bob"), "Item", "a1", "typing", json!({"thread": 3}))
            .unwrap();
        let (signals, others) = pull(&mut with, 1);
        assert_eq!(
            signals,
            [
                json!({"namespace": "default", "schema": "Item", "instanceId": "a1", "behavior": "test.Talk",
                    "name": "typing", "data": {"thread": 3}, "from": "bob", "at": 1_000_000})
            ]
        );
        assert!(others.is_empty(), "{others:?}");
        // The wire's own order of members.
        assert_eq!(
            signals[0]
                .as_object()
                .unwrap()
                .keys()
                .map(String::as_str)
                .collect::<Vec<_>>(),
            [
                "namespace",
                "schema",
                "instanceId",
                "behavior",
                "name",
                "data",
                "from",
                "at"
            ]
        );
        // A write signal; data absent from a caller is {} at the route,
        // and any JSON its schema takes here.
        world
            .send(&named("bob"), "Item", "a1", "nudge", json!([1, "two"]))
            .unwrap();
        assert_eq!(texts_of(&pull(&mut with, 1).0), ["a1 nudge [1,\"two\"]"]);
        // Nothing is appended, and the stream without signals hears
        // nothing.
        assert_eq!(pull(&mut without, 0), (Vec::new(), Vec::new()));
        let head = world.engine.events().head().unwrap();
        world
            .send(&alice(), "Item", "a1", "typing", json!({}))
            .unwrap();
        assert_eq!(world.engine.events().head().unwrap(), head);
        assert_eq!(pull(&mut without, 0), (Vec::new(), Vec::new()));
        world.engine.shutdown();
    }
}

#[test]
fn a_streams_filters_keep_its_signals_and_kinds_do_not_apply() {
    for target in targets("a_streams_filters_keep_its_signals") {
        let world = world(&target);
        let mut one = world.stream(
            &alice(),
            ReadEvents {
                schema: Some("Item"),
                instance_id: Some("a2"),
                ..ReadEvents::default()
            },
            true,
        );
        let mut items = world.stream(
            &alice(),
            ReadEvents {
                schema: Some("Item"),
                kinds: Some(vec!["create".to_owned()]),
                exclude: Some(vec!["say".to_owned()]),
                beside: Some(false),
                ..ReadEvents::default()
            },
            true,
        );
        let mut other_behavior = world.stream(
            &alice(),
            ReadEvents {
                behaviors: Some(vec!["test.Other".to_owned()]),
                ..ReadEvents::default()
            },
            true,
        );
        let mut talk = world.stream(
            &alice(),
            ReadEvents {
                behaviors: Some(vec!["test.Talk".to_owned()]),
                ..ReadEvents::default()
            },
            true,
        );
        for (schema, id) in [("Item", "a1"), ("Secret", "s1"), ("Item", "a2")] {
            world
                .send(&alice(), schema, id, "typing", json!({}))
                .unwrap();
        }
        assert_eq!(texts_of(&pull(&mut one, 1).0), ["a2 typing {}"]);
        assert_eq!(
            texts_of(&pull(&mut items, 2).0),
            ["a1 typing {}", "a2 typing {}"]
        );
        assert_eq!(texts_of(&pull(&mut talk, 3).0).len(), 3);
        assert_eq!(pull(&mut other_behavior, 0).0, Vec::<Value>::new());
        assert_eq!(pull(&mut one, 0).0, Vec::<Value>::new());
        world.engine.shutdown();
    }
}

#[test]
fn a_send_asks_the_policy_its_access_and_a_stream_skips_a_schema_it_may_not_read() {
    for target in targets("a_send_asks_the_policy") {
        // dave may do nothing; carol may not read Secret.
        let policy = RecordingPolicy::new(|request| match request {
            AccessRequest::Schema {
                principal, schema, ..
            } => {
                principal.subject != "dave"
                    && !(principal.subject == "carol" && *schema == "Secret")
            }
            _ => true,
        });
        let asked = policy.asked.clone();
        let world = opened(&target, Some(Policy::custom(policy)), None);
        let mut carol = world.stream(&named("carol"), ReadEvents::default(), true);
        asked.lock().unwrap().clear();
        world
            .send(&alice(), "Item", "a1", "typing", json!({}))
            .unwrap();
        world
            .send(&alice(), "Item", "a1", "nudge", json!({}))
            .unwrap();
        let questions: Vec<Value> = asked.lock().unwrap().drain(..).collect();
        assert_eq!(
            questions,
            [
                json!({"principal": "alice", "action": "read", "namespace": "default", "schema": "Item", "operation": "signal:typing"}),
                json!({"principal": "alice", "action": "write", "namespace": "default", "schema": "Item", "operation": "signal:nudge"}),
            ]
        );
        let refused = world.send(&named("dave"), "Item", "a1", "typing", json!({}));
        assert_eq!(code(refused), EngineErrorCode::Forbidden);
        // carol reads Item's signals and never Secret's, asking the policy
        // once per schema.
        for _ in 0..2 {
            world
                .send(&alice(), "Secret", "s1", "typing", json!({}))
                .unwrap();
        }
        world
            .send(&alice(), "Item", "a2", "typing", json!({}))
            .unwrap();
        asked.lock().unwrap().clear();
        let (signals, _) = pull(&mut carol, 3);
        assert_eq!(
            texts_of(&signals),
            ["a1 typing {}", "a1 nudge {}", "a2 typing {}"]
        );
        let carol_asked: Vec<Value> = asked
            .lock()
            .unwrap()
            .iter()
            .filter(|question| question["principal"] == "carol")
            .cloned()
            .collect();
        assert_eq!(
            carol_asked,
            [
                json!({"principal": "carol", "action": "read", "namespace": "default", "schema": "Item"}),
                json!({"principal": "carol", "action": "read", "namespace": "default", "schema": "Secret"}),
            ]
        );
        world.engine.shutdown();
    }
}

#[test]
fn a_send_is_refused_what_it_cannot_send() {
    for target in targets("a_send_is_refused") {
        let world = world(&target);
        let bob = named("bob");
        // An operation's signal is not a caller's.
        let refused = world.send(&bob, "Item", "a1", "draft", json!({"text": "x"}));
        assert_eq!(code(refused), EngineErrorCode::Forbidden);
        // What does not exist.
        for (schema, id, name) in [
            ("Item", "a1", "whisper"),
            ("Item", "zz", "typing"),
            ("Nothing", "a1", "typing"),
        ] {
            let refused = world.send(&bob, schema, id, name, json!({}));
            assert_eq!(
                code(refused),
                EngineErrorCode::NotFound,
                "{schema} {id} {name}"
            );
        }
        let refused = world.engine.signals().send(
            &bob,
            &SendSignal {
                namespace: None,
                schema: "Item",
                instance_id: "a1",
                behavior: "test.Other",
                name: "typing",
                data: &json!({}),
            },
        );
        assert_eq!(code(refused), EngineErrorCode::NotFound);
        // Data its schema refuses, and data past 4096 bytes.
        let refused = world.send(&bob, "Item", "a1", "typing", json!({"thread": "x"}));
        let message = support::refusal(refused);
        assert_eq!(message.code(), EngineErrorCode::InvalidArgument);
        assert!(
            message.message().contains("/thread"),
            "{}",
            message.message()
        );
        let refused = world.send(&bob, "Item", "a1", "nudge", json!("x".repeat(4095)));
        assert_eq!(code(refused), EngineErrorCode::InvalidArgument);
        world
            .send(&bob, "Item", "a1", "nudge", json!("x".repeat(4094)))
            .unwrap();
        // An envelope past what a notification carries: a long subject.
        let long = named(&"b".repeat(4000));
        let refused = world.send(&long, "Item", "a1", "nudge", json!("x".repeat(4000)));
        let message = support::refusal(refused);
        assert_eq!(message.code(), EngineErrorCode::InvalidArgument);
        assert!(
            message.message().contains("more than a signal carries"),
            "{}",
            message.message()
        );
        assert_eq!(world.heard().len(), 1, "only the one sent");
        world.engine.shutdown();
    }
}

#[test]
fn a_caller_is_limited_per_subject_and_instance_and_answered_429() {
    for target in targets("a_caller_is_limited") {
        let world = opened(&target, None, Some(3));
        let bob = named("bob");
        for _ in 0..3 {
            world.send(&bob, "Item", "a1", "typing", json!({})).unwrap();
        }
        let refused = support::refusal(world.send(&bob, "Item", "a1", "typing", json!({})));
        assert_eq!(
            (refused.code(), refused.status()),
            (EngineErrorCode::RateLimited, 429)
        );
        // Another subject, and another instance, have their own.
        world
            .send(&named("carol"), "Item", "a1", "typing", json!({}))
            .unwrap();
        world.send(&bob, "Item", "a2", "typing", json!({})).unwrap();
        // Over HTTP: 429 with the problem, and 204 once a token is back.
        let request = |path: &str| Request {
            method: "POST".into(),
            path: path.into(),
            headers: vec![("content-type".into(), "application/json".into())],
            body: Some(b"{}".to_vec()),
            caller: Caller {
                principal: Some(bob.clone()),
                ..Caller::default()
            },
            request_id: "r1".into(),
            ..Request::default()
        };
        let path = "/namespaces/default/schemas/Item/instances/a1/signals/test.Talk/typing";
        let response = handle(&world.engine, &ApiOptions::default(), &request(path));
        assert_eq!(response.status, 429);
        let body = match &response.body {
            superschematic_engine::api::Body::Bytes(bytes) => {
                serde_json::from_slice::<Value>(bytes).unwrap()
            }
            other => panic!("{other:?}"),
        };
        assert_eq!(
            (&body["code"], &body["status"], &body["title"]),
            (
                &json!("rate_limited"),
                &json!(429),
                &json!("Too Many Requests")
            )
        );
        assert!(response
            .headers
            .iter()
            .any(|(name, value)| name == "retry-after" && value == "1"));
        world.engine.clock().advance(1_000).unwrap();
        let response = handle(&world.engine, &ApiOptions::default(), &request(path));
        assert_eq!(response.status, 204);
        assert!(matches!(
            response.body,
            superschematic_engine::api::Body::Empty
        ));
        // An operation's signals are not limited.
        let said: Vec<String> = (0..50).map(|n| n.to_string()).collect();
        let said: Vec<&str> = said.iter().map(String::as_str).collect();
        let before = world.heard().len();
        assert_eq!(world.say("say", &said).unwrap(), json!(50));
        assert_eq!(world.heard().len(), before + 50);
        world.engine.shutdown();
    }
}

#[test]
fn an_operations_signals_arrive_after_its_commit_in_order_and_never_from_a_rollback() {
    for target in targets("an_operations_signals_arrive_after_its_commit") {
        let world = world(&target);
        let mut stream = world.stream(&alice(), ReadEvents::default(), true);
        let seq = world
            .engine
            .instances()
            .get(&alice(), "Item", "a1", Default::default())
            .unwrap()
            .unwrap()
            .seq;
        assert_eq!(
            world.say("say", &["one", "two", "three"]).unwrap(),
            json!(3)
        );
        // None had arrived while the handler ran.
        assert_eq!(*world.before_commit.lock().unwrap(), [0]);
        let (signals, others) = pull(&mut stream, 3);
        assert_eq!(
            texts_of(&signals),
            [
                "a1 draft {\"text\":\"one\"}",
                "a1 draft {\"text\":\"two\"}",
                "a1 draft {\"text\":\"three\"}"
            ]
        );
        assert!(signals.iter().all(|signal| signal["from"] == "alice"));
        // The beside event, which carries an id, came with them.
        assert_eq!(others.len(), 1, "{others:?}");
        assert!(others[0].starts_with("id: "), "{others:?}");
        // A call that rolls back sends nothing; a call() that rolls back
        // drops its own and keeps its caller's.
        let refused = world.say("failSay", &["lost"]);
        assert_eq!(code(refused), EngineErrorCode::Vetoed);
        world.say("trySay", &["lost too"]).unwrap();
        let (signals, _) = pull(&mut stream, 1);
        assert_eq!(texts_of(&signals), ["a1 draft {\"text\":\"kept\"}"]);
        assert_eq!(pull(&mut stream, 0).0, Vec::<Value>::new());
        // A read-only operation sends too.
        world
            .say("glance", &[])
            .unwrap_or_else(|error| panic!("{error}"));
        assert_eq!(
            texts_of(&pull(&mut stream, 1).0),
            ["a1 draft {\"text\":\"glanced\"}"]
        );
        // A signal it does not declare, or data its schema refuses, is a
        // defect, which rolls the call back.
        let (behavior, message) = defect(world.say("mumble", &[]));
        assert_eq!(behavior, "test.Talk");
        assert!(message.contains("whisper"), "{message}");
        let (_, message) = defect(world.say("garble", &[]));
        assert!(message.contains("/text"), "{message}");
        assert_eq!(pull(&mut stream, 0).0, Vec::<Value>::new());
        // Beside, the instance kept its seq.
        let after = world
            .engine
            .instances()
            .get(&alice(), "Item", "a1", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(after.seq, seq);
        world.engine.shutdown();
    }
}

#[test]
fn a_schedule_and_a_reaction_invoke_a_beside_operation_whose_signals_follow_their_commit() {
    for target in targets("a_schedule_and_a_reaction_invoke_a_beside_operation") {
        let world = world(&target);
        let mut stream = world.stream(&alice(), ReadEvents::default(), true);
        // The reactions to the creates say "reacted" on each instance, and
        // the schedule, once due, "interrupted" on a1, each as the runner.
        let deadline = Instant::now() + PATIENCE;
        let mut handled = 0;
        loop {
            assert!(Instant::now() < deadline, "the schedule never ran");
            let pass = world.engine.runner().run_due().unwrap();
            handled += pass.handled;
            if pass.scheduled > 0 {
                break;
            }
            world.engine.clock().advance(1_000).unwrap();
        }
        assert!(handled >= 3, "{handled}");
        let now = world.engine.clock().now();
        let (signals, _) = pull(&mut stream, 4);
        let mut said = texts_of(&signals);
        said.sort();
        assert_eq!(
            said,
            [
                "a1 draft {\"text\":\"interrupted\"}",
                "a1 draft {\"text\":\"reacted\"}",
                "a2 draft {\"text\":\"reacted\"}",
                "s1 draft {\"text\":\"reacted\"}"
            ]
        );
        assert!(signals.iter().all(|signal| signal["from"] == "runner"));
        let interrupted = signals
            .iter()
            .find(|signal| signal["data"]["text"] == "interrupted")
            .unwrap();
        assert_eq!(interrupted["at"], json!(now));
        // Each appended its beside event as the runner, with its cause.
        let events = world
            .engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Item"),
                    instance_id: Some("a1"),
                    beside: Some(true),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events;
        let mut seen: Vec<String> = events
            .iter()
            .map(|event| {
                json!([event.seq, event.actor, event.change["params"], event.cause]).to_string()
            })
            .collect();
        seen.sort();
        let create = world
            .engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Item"),
                    instance_id: Some("a1"),
                    kinds: Some(vec!["create".to_owned()]),
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events[0]
            .cursor;
        assert_eq!(
            seen,
            [
                json!([null, "runner", {"texts": ["interrupted"]}, {"behavior": "test.Talk", "schedule": "interrupt", "depth": 1}]).to_string(),
                json!([null, "runner", {"texts": ["reacted"]}, {"behavior": "test.Talk", "event": create, "depth": 1}]).to_string(),
            ]
        );
        world.engine.shutdown();
    }
}

#[test]
fn a_reconnect_replays_events_and_never_a_signal() {
    for target in targets("a_reconnect_replays_events") {
        let world = world(&target);
        let mut live = world.stream(&alice(), ReadEvents::default(), true);
        world.say("say", &["live"]).unwrap();
        world
            .send(&alice(), "Item", "a1", "typing", json!({}))
            .unwrap();
        let (signals, others) = pull(&mut live, 2);
        assert_eq!(signals.len(), 2);
        let last_id = others
            .iter()
            .filter_map(|line| line.strip_prefix("id: "))
            .next_back()
            .map(|id| id.parse::<i64>().unwrap())
            .unwrap_or_else(|| live.resume_cursor());
        assert_eq!(live.resume_cursor(), last_id, "a signal moved no cursor");
        // A reconnect from the start replays every event and no signal,
        // and a signal sent while it replays waits for its ready event.
        let mut again = start_stream_with(
            &world.engine,
            &StreamOptions {
                page_size: Some(1),
                heartbeat_ms: Some(600_000),
            },
            &alice(),
            &ReadEvents {
                after: Some(EventsAfter::Cursor(0)),
                ..ReadEvents::default()
            },
            true,
        )
        .unwrap();
        world
            .send(&alice(), "Item", "a2", "typing", json!({}))
            .unwrap();
        let mut replayed = String::new();
        while !replayed.contains("event: ready") {
            match again.next(PATIENCE) {
                Pull::Data(bytes) => replayed.push_str(&String::from_utf8(bytes).unwrap()),
                other => panic!("{other:?}"),
            }
        }
        let (before_ready, after_ready) = replayed.split_once("event: ready").unwrap();
        assert!(!before_ready.contains("event: signal"), "{before_ready}");
        assert!(before_ready.contains("\"kind\":\"create\""));
        // It comes after the ready event, at once or at the next pull.
        if !after_ready.contains("event: signal") {
            let (signals, _) = pull(&mut again, 1);
            assert_eq!(texts_of(&signals), ["a2 typing {}"]);
        } else {
            assert!(
                after_ready.contains("\"instanceId\":\"a2\""),
                "{after_ready}"
            );
        }
        assert_eq!(again.resume_cursor(), world.engine.events().head().unwrap());
        world.engine.shutdown();
    }
}

#[test]
fn signals_wake_a_waiting_stream_without_a_read_of_the_log() {
    for target in targets("signals_wake_a_waiting_stream") {
        let world = world(&target);
        let mut stream = world.stream(&alice(), ReadEvents::default(), true);
        let engine = Arc::clone(&world.engine);
        let sender = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(50));
            engine
                .signals()
                .send(
                    &alice(),
                    &SendSignal {
                        namespace: None,
                        schema: "Item",
                        instance_id: "a1",
                        behavior: "test.Talk",
                        name: "typing",
                        data: &json!({}),
                    },
                )
                .unwrap();
        });
        // One pull, woken well before its heartbeat.
        let started = Instant::now();
        match stream.next(PATIENCE) {
            Pull::Data(bytes) => {
                let text = String::from_utf8(bytes).unwrap();
                assert!(text.starts_with("event: signal\n"), "{text}");
            }
            other => panic!("{other:?}"),
        }
        assert!(started.elapsed() < Duration::from_secs(10));
        sender.join().unwrap();
        world.engine.shutdown();
    }
}
