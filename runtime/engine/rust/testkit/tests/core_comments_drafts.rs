//! Comments' streamed replies (D77, batch 2): begin, appendDraft, finish
//! and interrupt, every refusal in its order; the composing and
//! interrupted states, postedAt and the composing field; maxDrafts and
//! composeTimeoutMs, an empty append as a keepalive, and the schedule
//! interrupting idle drafts on the runner's clock; batch 1's operations as
//! batch 2 amends them; the draft signal reaching a stream opened with
//! signals after the commit, its offsets in code points, typing and
//! viewing from a caller, and a reader that rebuilds the body from a read
//! and the signals; and migration 3 on a conversation migration 2 holds.
//! On SQLite and, with the variable set, on Postgres.

mod support;

use std::sync::Arc;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::api::{start_stream_with, EventStream, Pull, StreamOptions};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineErrorCode, EngineEvent, EventsAfter, Hooks, Principal, ReadEvents, Result, RunnerOptions,
    SchemaTarget, SendSignal, WriteOptions,
};
use support::{
    alice, code, issue, issues_of, named, refusal, schema_document, targets, veto, Target,
};

const PATIENCE: Duration = Duration::from_secs(20);

fn review_schema(config: Value) -> Value {
    let mut document = schema_document(
        "Review",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Review"]["behaviors"] = if config == json!({}) {
        json!([{"name": "Comments"}])
    } else {
        json!([{"name": "Comments", "config": config}])
    };
    document
}

fn bob() -> Principal {
    named("bob")
}

fn carol() -> Principal {
    named("carol")
}

fn moderator() -> Principal {
    Principal::new("mo", ["comments.moderate"])
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

fn publish(engine: &Engine, config: Value) {
    engine
        .schemas()
        .define(&alice(), review_schema(config), DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), "Review", SchemaTarget::default())
        .expect("publish");
}

fn create(engine: &Engine, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Review",
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

/// An engine whose Review composes Comments with the config given, with a
/// manual clock at 1000, the runner acting as `runner`, and r1 and r2.
struct World {
    engine: Arc<Engine>,
}

fn world(target: &Target, config: Value) -> World {
    let engine = target
        .open_core_with(|options| {
            options.clock = Clock::Manual(1000);
            options.runner = Some(RunnerOptions::new(runner()));
        })
        .shared();
    publish(&engine, config);
    create(&engine, "r1");
    create(&engine, "r2");
    World { engine }
}

impl World {
    fn call(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.engine.instances().invoke(
            who,
            "Review",
            "r1",
            operation,
            &params,
            WriteOptions::default(),
        )
    }

    fn ok(&self, who: &Principal, operation: &str, params: Value) -> Value {
        self.call(who, operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    fn begin(&self, who: &Principal, params: Value) -> i64 {
        self.ok(who, "begin", params)["id"].as_i64().expect("an id")
    }

    fn append(&self, who: &Principal, message: i64, text: &str) -> Value {
        self.ok(
            who,
            "appendDraft",
            json!({ "message": message, "text": text }),
        )
    }

    fn say(&self, who: &Principal, body: &str) -> i64 {
        self.ok(who, "post", json!({ "body": body }))["id"]
            .as_i64()
            .expect("an id")
    }

    fn message(&self, id: i64) -> Value {
        self.ok(&alice(), "getMessage", json!({ "message": id }))
    }

    fn fields(&self) -> Value {
        self.engine
            .instances()
            .get(&alice(), "Review", "r1", Default::default())
            .expect("get")
            .expect("r1")
            .behaviors["Comments"]
            .clone()
    }

    fn set_clock(&self, now: i64) {
        self.engine.clock().set(now).expect("set the clock");
    }

    fn events(&self) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Review"),
                    instance_id: Some("r1"),
                    beside: Some(true),
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
    }

    fn drafts(&self) -> i64 {
        support::count(
            &self.engine,
            "SELECT COUNT(*) FROM bhv_comments__comment_drafts",
        )
    }

    fn send(&self, who: &Principal, name: &str, data: Value) -> Result<()> {
        self.engine.signals().send(
            who,
            &SendSignal {
                namespace: None,
                schema: "Review",
                instance_id: "r1",
                behavior: "Comments",
                name,
                data: &data,
            },
        )
    }

    // stream opens a stream on r1 from the head as `who`, past its ready
    // event.
    fn stream(&self, who: &Principal, signals: bool) -> EventStream {
        let mut stream = start_stream_with(
            &self.engine,
            &StreamOptions {
                page_size: None,
                heartbeat_ms: Some(600_000),
            },
            who,
            &ReadEvents {
                schema: Some("Review"),
                instance_id: Some("r1"),
                after: Some(EventsAfter::Head),
                ..ReadEvents::default()
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
}

/// A message of the stream: a signal, or an event with its JSON.
#[derive(Debug, Clone)]
enum Heard {
    Signal(Value),
    Event(Value),
}

// pull reads the stream until `want` messages came, or, with `want` 0,
// until it has nothing for a moment.
fn pull(stream: &mut EventStream, want: usize) -> Vec<Heard> {
    let deadline = Instant::now() + PATIENCE;
    let mut heard = Vec::new();
    loop {
        assert!(
            Instant::now() < deadline,
            "{} of {want}: {heard:?}",
            heard.len()
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
                    if let Some(data) = message.strip_prefix("event: signal\ndata: ") {
                        heard.push(Heard::Signal(serde_json::from_str(data).unwrap()));
                    } else if let Some(data) =
                        message.lines().find_map(|line| line.strip_prefix("data: "))
                    {
                        heard.push(Heard::Event(serde_json::from_str(data).unwrap()));
                    }
                }
                if want > 0 && heard.len() >= want {
                    return heard;
                }
            }
            Pull::Pending if want == 0 => return heard,
            other => panic!("expected {want} messages, got {other:?} after {heard:?}"),
        }
    }
}

fn signals_of(heard: &[Heard]) -> Vec<Value> {
    heard
        .iter()
        .filter_map(|one| match one {
            Heard::Signal(signal) => Some(signal.clone()),
            Heard::Event(_) => None,
        })
        .collect()
}

// vetoed is a veto's code and details.
fn vetoed<T: std::fmt::Debug>(result: Result<T>) -> (String, Value) {
    let (behavior, _, _, code, details) = veto(result);
    assert_eq!(behavior, "Comments");
    (code.expect("a code"), details.unwrap_or(Value::Null))
}

fn invalid<T: std::fmt::Debug>(result: Result<T>) -> Vec<(String, String)> {
    let refused = refusal(result);
    assert_eq!(refused.name(), "OperationParamsError", "{refused:?}");
    issues_of(&refused)
}

#[test]
fn begin_holds_a_place_appends_build_the_draft_and_finish_posts_it() {
    for target in targets("comments_drafts_begin") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Before.");
        world.set_clock(2000);
        let begun = world.ok(&bob(), "begin", json!({}));
        assert_eq!(
            begun,
            json!({"id": 2, "author": "bob", "state": "composing", "body": "", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 2000, "edits": 0, "pinned": false, "replies": 0, "participants": []})
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 1, "lastMessageAt": 1000, "threads": {"open": 1, "resolved": 0}, "composing": ["bob"]})
        );
        world.set_clock(2500);
        assert_eq!(
            world.append(&bob(), 2, "Héllo, "),
            json!({"message": 2, "offset": 0, "length": 7})
        );
        assert_eq!(
            world.append(&bob(), 2, "wörld 🙂"),
            json!({"message": 2, "offset": 7, "length": 14})
        );
        // A read holds the draft so far, in its place.
        assert_eq!(world.message(2)["body"], json!("Héllo, wörld 🙂"));
        let page = world.ok(&alice(), "listMessages", json!({}));
        assert_eq!(page["items"][1]["state"], json!("composing"));
        assert_eq!(page["items"][1]["body"], json!("Héllo, wörld 🙂"));
        // Another message after it keeps the order of the ids.
        world.set_clock(3000);
        assert_eq!(world.say(&carol(), "After."), 3);
        world.set_clock(4000);
        let finished = world.ok(
            &bob(),
            "finish",
            json!({"message": 2, "mentions": ["alice"]}),
        );
        assert_eq!(
            finished,
            json!({"id": 2, "author": "bob", "state": "posted", "body": "Héllo, wörld 🙂", "blocks": [], "mentions": ["alice"], "reactions": {},
                   "createdAt": 2000, "postedAt": 4000, "edits": 0, "pinned": false, "replies": 0, "participants": ["bob"]})
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 3, "lastMessageAt": 4000, "threads": {"open": 3, "resolved": 0}, "composing": []})
        );
        assert_eq!(world.drafts(), 0);
        // A reply streamed into a thread: the thread counts it once it
        // stands, and its postedAt is the thread's lastReplyAt.
        world.set_clock(5000);
        let reply = world.begin(
            &carol(),
            json!({"thread": 1, "quotes": 1, "clientId": "c-1"}),
        );
        assert_eq!(world.message(1)["replies"], json!(0));
        assert_eq!(world.message(1)["participants"], json!(["alice"]));
        world.append(&carol(), reply, "Draft text.");
        world.set_clock(6000);
        let finished = world.ok(
            &carol(),
            "finish",
            json!({"message": reply, "body": "Replaced.", "blocks": [{"kind": "action", "title": "Ran it", "status": "succeeded"}]}),
        );
        assert_eq!(
            finished,
            json!({"id": 4, "thread": 1, "quotes": 1, "author": "carol", "state": "posted", "body": "Replaced.",
                   "blocks": [{"kind": "action", "title": "Ran it", "status": "succeeded"}], "mentions": [], "reactions": {},
                   "createdAt": 5000, "postedAt": 6000, "edits": 0, "pinned": false})
        );
        let top = world.message(1);
        assert_eq!(
            (&top["replies"], &top["lastReplyAt"], &top["participants"]),
            (&json!(1), &json!(6000), &json!(["carol", "alice"]))
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 4, "lastMessageAt": 6000, "threads": {"open": 3, "resolved": 0}, "composing": []})
        );
        // begin, finish and interrupt are logged beside the instance;
        // appendDraft appends nothing.
        let events: Vec<(Option<i64>, bool, Value, String)> = world
            .events()
            .iter()
            .map(|event| {
                (
                    event.seq,
                    event.beside,
                    event.change["operation"].clone(),
                    event.actor.clone(),
                )
            })
            .collect();
        assert_eq!(
            events,
            [
                (None, true, json!("post"), "alice".to_owned()),
                (None, true, json!("begin"), "bob".to_owned()),
                (None, true, json!("post"), "carol".to_owned()),
                (None, true, json!("finish"), "bob".to_owned()),
                (None, true, json!("begin"), "carol".to_owned()),
                (None, true, json!("finish"), "carol".to_owned()),
            ]
        );
        // The finish event carries the whole body.
        assert_eq!(
            world.events()[3].change["result"]["body"],
            json!("Héllo, wörld 🙂")
        );
        assert_eq!(
            world
                .engine
                .instances()
                .get(&alice(), "Review", "r1", Default::default())
                .unwrap()
                .unwrap()
                .seq,
            1
        );
    }
}

#[test]
fn begin_checks_in_order_holds_max_drafts_and_shares_a_retry_with_post() {
    for target in targets("comments_drafts_begin_refusals") {
        let world = world(&target, json!({"maxDrafts": 2}));
        world.say(&alice(), "Top.");
        let reply = world.ok(&alice(), "post", json!({"body": "Reply.", "thread": 1}))["id"]
            .as_i64()
            .unwrap();
        let gone = world.ok(&alice(), "post", json!({"body": "Gone.", "thread": 1}))["id"]
            .as_i64()
            .unwrap();
        world.ok(&alice(), "deleteMessage", json!({"message": gone}));
        let open = world.begin(&carol(), json!({}));
        let open_reply = world.begin(&carol(), json!({"thread": 1}));
        assert_eq!((reply, gone, open, open_reply), (2, 3, 4, 5));

        assert_eq!(
            invalid(world.call(&bob(), "begin", json!({"quotes": 1}))),
            [issue("/quotes", "needs thread")]
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 99}))),
            ("no_message".to_owned(), json!({"message": 99}))
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 2}))),
            (
                "not_a_thread".to_owned(),
                json!({"message": 2, "thread": 1})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": open}))),
            ("composing".to_owned(), json!({"message": open}))
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 1, "quotes": 98}))),
            ("no_message".to_owned(), json!({"message": 98}))
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 1, "quotes": open}))),
            (
                "not_in_thread".to_owned(),
                json!({"message": open, "thread": 1})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 1, "quotes": gone}))),
            ("deleted".to_owned(), json!({"message": gone}))
        );
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({"thread": 1, "quotes": open_reply}))),
            ("composing".to_owned(), json!({"message": open_reply}))
        );
        // post meets the same composing vetoes.
        assert_eq!(
            vetoed(world.call(&bob(), "post", json!({"body": "x", "thread": open}))),
            ("composing".to_owned(), json!({"message": open}))
        );
        assert_eq!(
            vetoed(world.call(
                &bob(),
                "post",
                json!({"body": "x", "thread": 1, "quotes": open_reply})
            )),
            ("composing".to_owned(), json!({"message": open_reply}))
        );
        // carol has two composing; the third is refused, after what the
        // call names.
        assert_eq!(
            vetoed(world.call(&carol(), "begin", json!({}))),
            (
                "too_many_drafts".to_owned(),
                json!({"composing": 2, "max": 2})
            )
        );
        assert_eq!(
            vetoed(world.call(&carol(), "begin", json!({"thread": 99}))),
            ("no_message".to_owned(), json!({"message": 99}))
        );
        // Another subject has its own count.
        let bobs = world.ok(&bob(), "begin", json!({"clientId": "b-1"}));
        assert_eq!(bobs["id"], json!(6));
        // A retry returns the message as it is now and appends nothing,
        // past maxDrafts too, and post and begin share the caller's keys.
        world.begin(&bob(), json!({}));
        let events = world.events().len();
        world.append(&bob(), 6, "so far");
        let retried = world.ok(&bob(), "begin", json!({"clientId": "b-1", "thread": 1}));
        assert_eq!(
            (retried["id"].clone(), retried["body"].clone()),
            (json!(6), json!("so far"))
        );
        assert_eq!(
            world.ok(&bob(), "post", json!({"body": "Other.", "clientId": "b-1"}))["state"],
            json!("composing")
        );
        let posted = world.ok(
            &alice(),
            "post",
            json!({"body": "Mine.", "clientId": "a-1"}),
        );
        assert_eq!(
            world.ok(&alice(), "begin", json!({"clientId": "a-1"})),
            posted
        );
        assert_eq!(world.events().len(), events + 1);
        assert_eq!(world.fields()["composing"], json!(["carol", "bob"]));
        // Lowering maxDrafts leaves the drafts and refuses the next begin.
        world.ok(
            &carol(),
            "finish",
            json!({"message": open, "body": "Done."}),
        );
        publish(&world.engine, json!({"maxDrafts": 1}));
        assert_eq!(
            vetoed(world.call(&bob(), "begin", json!({}))),
            (
                "too_many_drafts".to_owned(),
                json!({"composing": 2, "max": 1})
            )
        );
        assert_eq!(world.message(6)["state"], json!("composing"));
    }
}

#[test]
fn append_draft_and_finish_check_in_order() {
    for target in targets("comments_drafts_append_refusals") {
        let world = world(
            &target,
            json!({"maxBodyLength": 10, "blocks": ["action"], "moderatePermission": "comments.moderate"}),
        );
        let id = world.begin(&bob(), json!({}));
        // 1999 quotes write as 3998 bytes and two more: the most.
        let quotes = "\"".repeat(1999);
        let refused = world.call(
            &bob(),
            "appendDraft",
            json!({"message": id, "text": format!("{quotes}\"")}),
        );
        assert_eq!(
            invalid(refused),
            [issue("/text", "is more than 4000 bytes as a JSON string")]
        );
        assert_eq!(
            invalid(world.call(
                &bob(),
                "appendDraft",
                json!({"message": id, "text": "é".repeat(2000)})
            )),
            [issue("/text", "is more than 4000 bytes as a JSON string")]
        );
        // Past the bytes, the draft's length: quotes is 1999 code points.
        assert_eq!(
            vetoed(world.call(
                &bob(),
                "appendDraft",
                json!({"message": id, "text": quotes})
            )),
            (
                "draft_too_long".to_owned(),
                json!({"message": id, "length": 0, "append": 1999, "maxBodyLength": 10})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "appendDraft", json!({"message": 9, "text": "x"}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        assert_eq!(
            vetoed(world.call(&carol(), "appendDraft", json!({"message": id, "text": "x"}))),
            (
                "not_author".to_owned(),
                json!({"message": id, "author": "bob"})
            )
        );
        // A moderator writes no one's draft either.
        assert_eq!(
            vetoed(world.call(
                &moderator(),
                "appendDraft",
                json!({"message": id, "text": "x"})
            )),
            (
                "not_author".to_owned(),
                json!({"message": id, "author": "bob"})
            )
        );
        world.append(&bob(), id, "🙂🙂🙂🙂🙂");
        assert_eq!(
            vetoed(world.call(
                &bob(),
                "appendDraft",
                json!({"message": id, "text": "123456"})
            )),
            (
                "draft_too_long".to_owned(),
                json!({"message": id, "length": 5, "append": 6, "maxBodyLength": 10})
            )
        );
        assert_eq!(
            world.append(&bob(), id, "12345"),
            json!({"message": id, "offset": 5, "length": 10})
        );

        // finish.
        assert_eq!(
            invalid(world.call(
                &bob(),
                "finish",
                json!({"message": id, "body": "12345678901"})
            )),
            [issue("/body", "must NOT have more than 10 characters")]
        );
        let big = json!({"kind": "action", "title": "Big", "status": "running", "data": {"x": "y".repeat(8200)}});
        assert_eq!(
            invalid(world.call(&bob(), "finish", json!({"message": id, "blocks": [big]}))),
            [issue("/blocks/0/data", "is more than 8192 bytes of JSON")]
        );
        assert_eq!(
            vetoed(world.call(&bob(), "finish", json!({"message": 9}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        assert_eq!(
            vetoed(world.call(&carol(), "finish", json!({"message": id}))),
            (
                "not_author".to_owned(),
                json!({"message": id, "author": "bob"})
            )
        );
        assert_eq!(
            vetoed(world.call(
                &bob(),
                "finish",
                json!({"message": id, "blocks": [{"kind": "ref", "schema": "Review", "id": "r2"}]})
            )),
            (
                "block_not_allowed".to_owned(),
                json!({"block": 0, "kind": "ref", "allowed": ["action"]})
            )
        );
        // A draft with no text: whitespace, or none at all.
        let blank = world.begin(&bob(), json!({}));
        world.append(&bob(), blank, " \n\u{a0}\t");
        assert_eq!(
            invalid(world.call(&bob(), "finish", json!({"message": blank}))),
            [issue("/body", "is required: the draft has no text")]
        );
        let empty = world.begin(&bob(), json!({}));
        assert_eq!(
            invalid(world.call(&bob(), "finish", json!({"message": empty}))),
            [issue("/body", "is required: the draft has no text")]
        );
        assert_eq!(
            world.ok(
                &bob(),
                "finish",
                json!({"message": empty, "body": "Given."})
            )["body"],
            json!("Given.")
        );
        // The config narrowed since the appends: the draft is too long to
        // post as it is, and a body in its place is not.
        publish(
            &world.engine,
            json!({"maxBodyLength": 8, "blocks": ["action"], "moderatePermission": "comments.moderate", "maxDrafts": 5}),
        );
        assert_eq!(
            invalid(world.call(&bob(), "finish", json!({"message": id}))),
            [issue("/body", "must NOT have more than 8 characters")]
        );
        let posted = world.ok(&bob(), "finish", json!({"message": id, "body": "Short."}));
        assert_eq!(
            (&posted["state"], &posted["body"]),
            (&json!("posted"), &json!("Short."))
        );
        // A retry of a finish that committed is not_composing, posted.
        assert_eq!(
            vetoed(world.call(&bob(), "finish", json!({"message": id}))),
            (
                "not_composing".to_owned(),
                json!({"message": id, "state": "posted"})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "appendDraft", json!({"message": id, "text": "x"}))),
            (
                "not_composing".to_owned(),
                json!({"message": id, "state": "posted"})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "interrupt", json!({"message": id}))),
            (
                "not_composing".to_owned(),
                json!({"message": id, "state": "posted"})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "interrupt", json!({"message": 99}))),
            ("no_message".to_owned(), json!({"message": 99}))
        );
        // A refused call wrote nothing: the blank draft holds its one chunk.
        assert_eq!(world.drafts(), 1);
    }
}

#[test]
fn interrupt_keeps_what_was_written_and_only_its_author_or_a_moderator_stops_it_early() {
    for target in targets("comments_drafts_interrupt") {
        let world = world(
            &target,
            json!({"composeTimeoutMs": 5000, "moderatePermission": "comments.moderate", "editWindowMs": 60000}),
        );
        world.say(&alice(), "Top.");
        let reply = world.begin(&bob(), json!({"thread": 1}));
        world.append(&bob(), reply, "Half a th");
        world.set_clock(3000);
        // Not idle yet: another may not stop it.
        assert_eq!(
            vetoed(world.call(&carol(), "interrupt", json!({"message": reply}))),
            (
                "not_idle".to_owned(),
                json!({"message": reply, "author": "bob", "lastAppendAt": 1000, "composeTimeoutMs": 5000})
            )
        );
        // Its author may at any time, and keeps the draft.
        let interrupted = world.ok(&bob(), "interrupt", json!({"message": reply}));
        assert_eq!(
            interrupted,
            json!({"id": 2, "thread": 1, "author": "bob", "state": "interrupted", "body": "Half a th", "blocks": [], "mentions": [],
                   "reactions": {}, "createdAt": 1000, "postedAt": 3000, "edits": 0, "pinned": false})
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 2, "lastMessageAt": 3000, "threads": {"open": 1, "resolved": 0}, "composing": []})
        );
        let top = world.message(1);
        assert_eq!(
            (&top["replies"], &top["lastReplyAt"], &top["participants"]),
            (&json!(1), &json!(3000), &json!(["bob", "alice"]))
        );
        // An interrupted message stands: it takes replies, quotes,
        // reactions and pins.
        world.ok(
            &carol(),
            "post",
            json!({"body": "Quoting.", "thread": 1, "quotes": reply}),
        );
        world.ok(&carol(), "react", json!({"message": reply, "emoji": "👀"}));
        world.ok(&carol(), "pin", json!({"message": reply}));
        // A moderator's edit leaves it interrupted; its author's posts it,
        // and the window counts from its postedAt.
        let edited = world.ok(
            &moderator(),
            "edit",
            json!({"message": reply, "mentions": ["bob"]}),
        );
        assert_eq!(edited["state"], json!("interrupted"));
        world.set_clock(63_000);
        let edited = world.ok(
            &bob(),
            "edit",
            json!({"message": reply, "body": "Half a thought."}),
        );
        assert_eq!(
            (&edited["state"], &edited["postedAt"], &edited["edits"]),
            (&json!("posted"), &json!(3000), &json!(2))
        );
        world.set_clock(63_001);
        assert_eq!(
            vetoed(world.call(&bob(), "edit", json!({"message": reply, "body": "Late."}))),
            (
                "edit_window_passed".to_owned(),
                json!({"message": reply, "postedAt": 3000, "editWindowMs": 60000})
            )
        );
        // An empty draft interrupted is deleted, and never stood.
        let empty = world.begin(&carol(), json!({}));
        let deleted = world.ok(&moderator(), "interrupt", json!({"message": empty}));
        assert_eq!(
            deleted,
            json!({"id": empty, "author": "carol", "state": "deleted", "body": "", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 63_001, "edits": 0, "pinned": false, "replies": 0, "participants": []})
        );
        assert_eq!(world.fields()["messages"], json!(3));
        // Idle, anyone the policy lets write may stop it.
        let idle = world.begin(&carol(), json!({}));
        world.append(&carol(), idle, "Gone quiet");
        world.set_clock(68_000);
        assert_eq!(
            vetoed(world.call(&bob(), "interrupt", json!({"message": idle}))).0,
            "not_idle"
        );
        world.set_clock(68_001);
        assert_eq!(
            world.ok(&bob(), "interrupt", json!({"message": idle}))["state"],
            json!("interrupted")
        );
        assert_eq!(world.drafts(), 0);
        let actors: Vec<(String, Value)> = world
            .events()
            .iter()
            .filter(|event| event.change["operation"] == "interrupt")
            .map(|event| (event.actor.clone(), event.change["result"]["state"].clone()))
            .collect();
        assert_eq!(
            actors,
            [
                ("bob".to_owned(), json!("interrupted")),
                ("mo".to_owned(), json!("deleted")),
                ("bob".to_owned(), json!("interrupted")),
            ]
        );
    }
}

#[test]
fn an_empty_append_keeps_a_draft_from_going_idle_and_sends_nothing() {
    for target in targets("comments_drafts_keepalive") {
        let world = world(&target, json!({"composeTimeoutMs": 5000}));
        let mut stream = world.stream(&alice(), true);
        let id = world.begin(&bob(), json!({}));
        world.set_clock(5000);
        assert_eq!(
            world.append(&bob(), id, ""),
            json!({"message": id, "offset": 0, "length": 0})
        );
        assert_eq!(world.drafts(), 0);
        world.set_clock(9000);
        assert_eq!(
            vetoed(world.call(&carol(), "interrupt", json!({"message": id}))),
            (
                "not_idle".to_owned(),
                json!({"message": id, "author": "bob", "lastAppendAt": 5000, "composeTimeoutMs": 5000})
            )
        );
        // The begin's event and nothing else: no signal, no event.
        let heard = pull(&mut stream, 0);
        assert_eq!(heard.len(), 1, "{heard:?}");
        assert!(signals_of(&heard).is_empty(), "{heard:?}");
        // An append to an idle message is still allowed, and wakes it.
        world.set_clock(20_000);
        world.append(&bob(), id, "Back.");
        assert_eq!(
            vetoed(world.call(&carol(), "interrupt", json!({"message": id}))).0,
            "not_idle"
        );
        world.engine.shutdown();
    }
}

#[test]
fn the_schedule_interrupts_idle_drafts_on_the_runners_clock() {
    for target in targets("comments_drafts_schedule") {
        let world = world(&target, json!({"composeTimeoutMs": 5000}));
        let written = world.begin(&bob(), json!({}));
        world.append(&bob(), written, "Something");
        let empty = world.begin(&carol(), json!({}));
        let busy = world.begin(&alice(), json!({}));
        // Another instance's idle draft is swept in the same run.
        world
            .engine
            .instances()
            .invoke(
                &bob(),
                "Review",
                "r2",
                "begin",
                &json!({}),
                WriteOptions::default(),
            )
            .unwrap();
        world.set_clock(7000);
        world.append(&alice(), busy, "Still going");
        let deadline = Instant::now() + PATIENCE;
        loop {
            assert!(Instant::now() < deadline, "the schedule never interrupted");
            world.engine.runner().run_due().unwrap();
            if world.message(written)["state"] != "composing" {
                break;
            }
            world.engine.clock().advance(1250).unwrap();
        }
        let now = world.engine.clock().now();
        assert!(now < 12_000, "{now}");
        let written_record = world.message(written);
        assert_eq!(
            (
                &written_record["state"],
                &written_record["body"],
                &written_record["postedAt"]
            ),
            (&json!("interrupted"), &json!("Something"), &json!(now))
        );
        assert_eq!(world.message(empty)["state"], json!("deleted"));
        assert!(world.message(empty).get("postedAt").is_none());
        assert_eq!(world.message(busy)["state"], json!("composing"));
        assert_eq!(world.fields()["composing"], json!(["alice"]));
        assert_eq!(world.fields()["messages"], json!(1));
        let r2 = world
            .engine
            .instances()
            .invoke(
                &alice(),
                "Review",
                "r2",
                "getMessage",
                &json!({"message": 1}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(r2["state"], json!("deleted"));
        // Each interrupt is a logged event of the runner's, caused by the
        // schedule.
        let interrupts: Vec<Value> = world
            .events()
            .iter()
            .filter(|event| event.change["operation"] == "interrupt")
            .map(|event| json!([event.actor, event.change["params"], event.cause]))
            .collect();
        assert_eq!(
            interrupts,
            [
                json!(["runner", {"message": written}, {"behavior": "Comments", "schedule": "interrupt", "depth": 1}]),
                json!(["runner", {"message": empty}, {"behavior": "Comments", "schedule": "interrupt", "depth": 1}]),
            ]
        );
        // The busy one goes once it is idle too.
        world.set_clock(13_000);
        let deadline = Instant::now() + PATIENCE;
        while world.message(busy)["state"] == "composing" {
            assert!(Instant::now() < deadline, "the schedule never interrupted");
            world.engine.runner().run_due().unwrap();
            world.engine.clock().advance(1250).unwrap();
        }
        assert_eq!(world.message(busy)["body"], json!("Still going"));
        world.engine.shutdown();
    }
}

#[test]
fn batch_1s_operations_refuse_a_composing_message_and_delete_and_purge_drop_its_draft() {
    for target in targets("comments_drafts_amended") {
        let world = world(
            &target,
            json!({"moderatePermission": "comments.moderate", "resolvePermission": "comments.moderate"}),
        );
        let top = world.begin(&bob(), json!({}));
        world.append(&bob(), top, "Draft.");
        for (operation, params) in [
            ("edit", json!({"message": top, "body": "x"})),
            ("react", json!({"message": top, "emoji": "x"})),
            ("unreact", json!({"message": top, "emoji": "x"})),
            ("pin", json!({"message": top})),
            ("resolve", json!({"thread": top})),
            ("reopen", json!({"thread": top})),
        ] {
            assert_eq!(
                vetoed(world.call(&bob(), operation, params)),
                ("composing".to_owned(), json!({"message": top})),
                "{operation}"
            );
        }
        assert_eq!(
            vetoed(world.call(&bob(), "unpin", json!({"message": top}))),
            ("not_pinned".to_owned(), json!({"message": top}))
        );
        // listMessages may name a composing thread: it has no replies.
        let page = world.ok(&alice(), "listMessages", json!({"thread": top}));
        assert_eq!(page["items"], json!([]));
        assert_eq!(page["root"]["state"], json!("composing"));
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": top}))["items"],
            json!([])
        );
        // Its author deletes it to drop the draft: no count moves.
        let dropped = world.ok(&bob(), "deleteMessage", json!({"message": top}));
        assert_eq!(
            dropped,
            json!({"id": top, "author": "bob", "state": "deleted", "body": "", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 1000, "edits": 0, "pinned": false, "replies": 0, "participants": []})
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": []})
        );
        assert_eq!(world.drafts(), 0);
        // A moderator deletes another's.
        let other = world.begin(&carol(), json!({}));
        world.append(&carol(), other, "x");
        assert_eq!(
            vetoed(world.call(&bob(), "deleteMessage", json!({"message": other}))),
            (
                "not_author".to_owned(),
                json!({"message": other, "author": "carol"})
            )
        );
        world.ok(&moderator(), "deleteMessage", json!({"message": other}));
        assert_eq!(world.drafts(), 0);
        // A message deleted after it stood keeps its postedAt.
        world.set_clock(2000);
        let thread = world.say(&alice(), "Thread.");
        world.set_clock(3000);
        let kept = world.ok(&alice(), "deleteMessage", json!({"message": thread}));
        assert_eq!(
            (&kept["createdAt"], &kept["postedAt"]),
            (&json!(2000), &json!(2000))
        );
        // Purging a thread purges its composing replies with their drafts.
        let thread = world.say(&alice(), "Another.");
        let streaming = world.begin(&carol(), json!({"thread": thread}));
        world.append(&carol(), streaming, "Mid-reply");
        world.ok(
            &carol(),
            "post",
            json!({"body": "Posted.", "thread": thread}),
        );
        assert_eq!(world.fields()["composing"], json!(["carol"]));
        let purged = world.ok(&moderator(), "purge", json!({"message": thread}));
        assert_eq!(
            purged,
            json!({"message": thread, "purged": [thread, streaming, thread + 2]})
        );
        assert_eq!(world.drafts(), 0);
        assert_eq!(world.fields()["composing"], json!([]));
        assert_eq!(world.fields()["messages"], json!(0));
        // Deleting the instance deletes its drafts.
        let last = world.begin(&carol(), json!({}));
        world.append(&carol(), last, "Lost with the instance.");
        assert_eq!(world.drafts(), 1);
        world
            .engine
            .instances()
            .delete(&alice(), "Review", "r1", WriteOptions::default())
            .unwrap();
        assert_eq!(world.drafts(), 0);
    }
}

#[test]
fn the_composing_field_names_each_writer_once_by_their_earliest_draft() {
    for target in targets("comments_drafts_composing_field") {
        let world = world(&target, json!({}));
        let carols = world.begin(&carol(), json!({}));
        world.begin(&bob(), json!({}));
        world.begin(&carol(), json!({}));
        assert_eq!(world.fields()["composing"], json!(["carol", "bob"]));
        world.ok(&carol(), "interrupt", json!({"message": carols}));
        assert_eq!(world.fields()["composing"], json!(["bob", "carol"]));
    }
}

#[test]
fn draft_signals_reach_a_stream_with_signals_after_the_commit_in_code_points() {
    for target in targets("comments_drafts_signals") {
        let world = world(&target, json!({"maxBodyLength": 20}));
        let mut with = world.stream(&alice(), true);
        let mut without = world.stream(&alice(), false);
        let id = world.begin(&bob(), json!({}));
        world.set_clock(2000);
        world.append(&bob(), id, "Hé");
        world.append(&bob(), id, "");
        world.append(&bob(), id, "🙂 ok");
        world.append(&bob(), id, "\"q\"\n");
        // A refused append sends nothing.
        world
            .call(
                &bob(),
                "appendDraft",
                json!({"message": id, "text": "x".repeat(20)}),
            )
            .unwrap_err();
        let heard = pull(&mut with, 4);
        let heard = [heard, pull(&mut with, 0)].concat();
        let signals = signals_of(&heard);
        assert_eq!(
            signals,
            [
                json!({"namespace": "default", "schema": "Review", "instanceId": "r1", "behavior": "Comments", "name": "draft",
                       "data": {"message": id, "offset": 0, "text": "Hé"}, "from": "bob", "at": 2000}),
                json!({"namespace": "default", "schema": "Review", "instanceId": "r1", "behavior": "Comments", "name": "draft",
                       "data": {"message": id, "offset": 2, "text": "🙂 ok"}, "from": "bob", "at": 2000}),
                json!({"namespace": "default", "schema": "Review", "instanceId": "r1", "behavior": "Comments", "name": "draft",
                       "data": {"message": id, "offset": 6, "text": "\"q\"\n"}, "from": "bob", "at": 2000}),
            ]
        );
        // The data's members in the contract's order.
        assert_eq!(
            signals[0]["data"]
                .as_object()
                .unwrap()
                .keys()
                .map(String::as_str)
                .collect::<Vec<_>>(),
            ["message", "offset", "text"]
        );
        // The begin's event came, and nothing else.
        assert_eq!(heard.len() - signals.len(), 1, "{heard:?}");
        // A caller sends typing and viewing; draft is the operation's.
        world
            .send(&carol(), "typing", json!({"thread": 7}))
            .unwrap();
        world.send(&carol(), "viewing", json!({})).unwrap();
        assert_eq!(
            code(world.send(
                &carol(),
                "draft",
                json!({"message": id, "offset": 10, "text": "forged"})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(world.send(&carol(), "typing", json!({"thread": 0}))),
            EngineErrorCode::InvalidArgument
        );
        let presence: Vec<(Value, Value, Value)> = signals_of(&pull(&mut with, 2))
            .into_iter()
            .map(|signal| {
                (
                    signal["name"].clone(),
                    signal["data"].clone(),
                    signal["from"].clone(),
                )
            })
            .collect();
        assert_eq!(
            presence,
            [
                (json!("typing"), json!({"thread": 7}), json!("carol")),
                (json!("viewing"), json!({}), json!("carol")),
            ]
        );
        assert!(signals_of(&pull(&mut without, 0)).is_empty());
        world.engine.shutdown();
    }
}

// A reader that follows a streamed message as "What a reader does with
// draft" says: it holds each composing message's draft and its length in
// code points, appends a signal at its length, ignores one below it, reads
// the message again for one past it, and takes the record from a finish
// event.
#[derive(Default)]
struct Reader {
    // (draft, its length in code points), while the reader holds the
    // message as composing.
    held: Option<(String, i64)>,
    body: Option<String>,
    reads: usize,
}

impl Reader {
    fn hold(&mut self, record: &Value) {
        let body = record["body"].as_str().unwrap().to_owned();
        if record["state"] == "composing" {
            let length = body.chars().count() as i64;
            self.held = Some((body, length));
        } else {
            self.held = None;
            self.body = Some(body);
        }
    }

    fn heard(&mut self, world: &World, heard: &Heard) {
        match heard {
            Heard::Signal(signal) => {
                let data = &signal["data"];
                let offset = data["offset"].as_i64().unwrap();
                let text = data["text"].as_str().unwrap();
                let Some((draft, length)) = &mut self.held else {
                    return;
                };
                if offset == *length {
                    draft.push_str(text);
                    *length += text.chars().count() as i64;
                    return;
                }
                if offset > *length {
                    self.reads += 1;
                    let record = world.message(data["message"].as_i64().unwrap());
                    self.hold(&record);
                }
            }
            Heard::Event(event) => {
                if event["change"]["operation"] == "finish" {
                    self.hold(&event["change"]["result"]);
                }
            }
        }
    }
}

#[test]
fn a_reader_rebuilds_the_body_from_a_read_and_the_draft_signals() {
    for target in targets("comments_drafts_reader") {
        let world = world(&target, json!({}));
        let id = world.begin(&bob(), json!({}));
        world.append(&bob(), id, "Once ");
        // The reader opens the stream with signals, then reads.
        let mut stream = world.stream(&carol(), true);
        world.append(&bob(), id, "upon ");
        let mut reader = Reader::default();
        reader.hold(&world.message(id));
        assert_eq!(reader.held, Some(("Once upon ".to_owned(), 10)));
        for text in ["a time, ", "naïve ", "🐉s ", "", "slept."] {
            world.append(&bob(), id, text);
        }
        // "upon " arrives after the read, and is ignored; the rest append.
        for one in &pull(&mut stream, 5) {
            reader.heard(&world, one);
        }
        assert_eq!(
            reader.held,
            Some(("Once upon a time, naïve 🐉s slept.".to_owned(), 33))
        );
        // Then the finish event, which carries the body.
        let finished = world.ok(&bob(), "finish", json!({"message": id}));
        for one in &pull(&mut stream, 1) {
            reader.heard(&world, one);
        }
        assert_eq!(reader.held, None);
        assert_eq!(reader.body.as_deref(), finished["body"].as_str());
        assert_eq!(
            finished["body"],
            json!("Once upon a time, naïve 🐉s slept.")
        );
        assert_eq!(reader.reads, 0);
        // A reader that missed a signal reads the message again.
        let id = world.begin(&bob(), json!({}));
        let mut late = Reader::default();
        late.hold(&world.message(id));
        world.append(&bob(), id, "lost ");
        world.append(&bob(), id, "kept");
        // It misses the first signal.
        for one in &pull(&mut stream, 3) {
            if let Heard::Signal(signal) = one {
                if signal["data"]["text"] != "lost " {
                    late.heard(&world, one);
                }
            }
        }
        assert_eq!(late.reads, 1);
        assert_eq!(late.held, Some(("lost kept".to_owned(), 9)));
        world.engine.shutdown();
    }
}

// Comments at migration 2, as batch 1 shipped it: its tables, and `seed`,
// which writes a batch 1 message as batch 1's post and deleteMessage left
// it.
#[derive(Clone, Copy)]
struct CommentsAtTwo;

impl Behavior for CommentsAtTwo {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Comments", "fields": [{"name": "messages"}], "operations": [
            {"name": "seed", "writes": true, "resultSchema": true, "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"],
                "properties": {"body": {"type": "string"}, "thread": {"type": "integer"}, "deleted": {"type": "boolean"}}}}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let mut migrations = superschematic_engine_behaviors::Comments.hooks().migrations;
        migrations.truncate(2);
        assert_eq!(
            migrations
                .iter()
                .map(|step| step.version)
                .collect::<Vec<_>>(),
            [1, 2]
        );
        Hooks {
            migrations,
            operations: vec![("seed", |_, cx, params| {
                let key = [
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from(cx.id()),
                ];
                let conversations = cx.sql().table("comment_conversations")?;
                let last = cx
                    .sql()
                    .get(
                        format!("SELECT last_message FROM {conversations} WHERE namespace = ? AND schema = ? AND id = ?"),
                        &key,
                    )?
                    .map(|row| row.i64("last_message"))
                    .transpose()?;
                let id = last.unwrap_or(0) + 1;
                let thread = params.get("thread").and_then(Value::as_i64);
                let deleted = params.get("deleted").and_then(Value::as_bool) == Some(true);
                let messages = cx.sql().table("comment_messages")?;
                let mut values = key.to_vec();
                values.extend([
                    SqlValue::Integer(id),
                    thread.map_or(SqlValue::Null, SqlValue::Integer),
                    SqlValue::from(cx.principal().subject.as_str()),
                    SqlValue::from(if deleted { "deleted" } else { "posted" }),
                    SqlValue::from(if deleted {
                        ""
                    } else {
                        params["body"].as_str().unwrap_or_default()
                    }),
                    SqlValue::Integer(cx.now()),
                ]);
                cx.sql().run(
                    format!(
                        "INSERT INTO {messages} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at)
                         VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, '[]', '[]', ?, NULL, 0, 0, NULL, NULL, NULL)"
                    ),
                    &values,
                )?;
                let posted = i64::from(!deleted);
                let open = i64::from(!deleted && thread.is_none());
                let mut values = key.to_vec();
                if last.is_none() {
                    values.extend([
                        SqlValue::Integer(id),
                        if deleted {
                            SqlValue::Null
                        } else {
                            SqlValue::Integer(cx.now())
                        },
                        SqlValue::Integer(open),
                    ]);
                    cx.sql().run(
                        format!("INSERT INTO {conversations} (namespace, schema, id, last_message, last_reaction, last_message_at, open_threads, resolved_threads) VALUES (?, ?, ?, ?, 0, ?, ?, 0)"),
                        &values,
                    )?;
                } else {
                    let mut params = vec![
                        SqlValue::Integer(id),
                        if deleted {
                            SqlValue::Null
                        } else {
                            SqlValue::Integer(cx.now())
                        },
                        SqlValue::Integer(open),
                    ];
                    params.extend(key);
                    cx.sql().run(
                        format!("UPDATE {conversations} SET last_message = ?, last_message_at = COALESCE(?, last_message_at), open_threads = open_threads + ? WHERE namespace = ? AND schema = ? AND id = ?"),
                        &params,
                    )?;
                }
                let count = cx.columns().get()?.i64("count")?;
                cx.columns()
                    .set(&[("count", SqlValue::Integer(count + posted))])?;
                Ok(json!({ "id": id }))
            })],
            fields: vec![("messages", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn migration_3_adds_drafts_to_a_conversation_migration_2_holds() {
    for target in targets("comments_drafts_migrate") {
        let mut at_two = target.options(BehaviorSet::new().with(CommentsAtTwo));
        at_two.clock = Clock::Manual(1000);
        let engine = Engine::open(at_two).expect("open at migration 2");
        publish(&engine, json!({}));
        create(&engine, "r1");
        let seed = |who: &Principal, at: i64, params: Value| {
            engine.clock().set(at).unwrap();
            engine
                .instances()
                .invoke(
                    who,
                    "Review",
                    "r1",
                    "seed",
                    &params,
                    WriteOptions::default(),
                )
                .expect("seed");
        };
        seed(&alice(), 1001, json!({"body": "One."}));
        seed(&bob(), 1002, json!({"body": "Two.", "thread": 1}));
        seed(
            &carol(),
            1003,
            json!({"body": "Three.", "thread": 1, "deleted": true}),
        );
        seed(&carol(), 1004, json!({"body": "Four."}));
        drop(engine);

        let engine = target
            .open_core_with(|options| options.clock = Clock::Manual(5000))
            .shared();
        let r1 = |who: &Principal, operation: &str, params: Value| {
            engine.instances().invoke(
                who,
                "Review",
                "r1",
                operation,
                &params,
                WriteOptions::default(),
            )
        };
        let thread = r1(&alice(), "listMessages", json!({"thread": 1})).unwrap();
        assert_eq!(
            thread["root"],
            json!({"id": 1, "author": "alice", "state": "posted", "body": "One.", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 1001, "postedAt": 1001, "edits": 0, "pinned": false, "replies": 1, "lastReplyAt": 1002, "participants": ["bob", "alice"]})
        );
        // Every message stood from its createdAt, the deleted one too.
        let posted: Vec<(Value, Value)> = thread["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| (item["state"].clone(), item["postedAt"].clone()))
            .collect();
        assert_eq!(
            posted,
            [
                (json!("posted"), json!(1002)),
                (json!("deleted"), json!(1003))
            ]
        );
        let fields = engine
            .instances()
            .get(&alice(), "Review", "r1", Default::default())
            .unwrap()
            .unwrap()
            .behaviors["Comments"]
            .clone();
        assert_eq!(
            fields,
            json!({"messages": 3, "lastMessageAt": 1004, "threads": {"open": 2, "resolved": 0}, "composing": []})
        );
        // The edit window counts from the backfilled postedAt.
        publish(&engine, json!({"editWindowMs": 3998}));
        assert_eq!(
            vetoed(r1(&alice(), "edit", json!({"message": 1, "body": "One!"}))),
            (
                "edit_window_passed".to_owned(),
                json!({"message": 1, "postedAt": 1001, "editWindowMs": 3998})
            )
        );
        r1(&carol(), "edit", json!({"message": 4, "body": "Four!"})).unwrap();
        // The conversation streams on from its greatest id.
        assert_eq!(
            r1(&bob(), "begin", json!({"thread": 4})).unwrap()["id"],
            json!(5)
        );
        assert_eq!(
            r1(&bob(), "appendDraft", json!({"message": 5, "text": "Five"})).unwrap()["length"],
            json!(4)
        );
        let tables = support::tables_of(&engine, "bhv_comments__");
        for name in [
            "bhv_comments__comment_drafts",
            "bhv_comments__comment_messages_composing",
            "bhv_comments__comment_messages_idle",
        ] {
            assert!(tables.contains(&name.to_owned()), "{name}: {tables:?}");
        }
        let ledger: Vec<i64> = target
            .db()
            .read(|tx| {
                tx.all("SELECT version FROM engine_migrations WHERE owner = 'Comments' ORDER BY version", &[])?
                    .iter()
                    .map(|row| row.i64("version"))
                    .collect::<superschematic_engine::storage::Result<Vec<_>>>()
            })
            .unwrap();
        assert_eq!(ledger, [1, 2, 3]);
        engine.shutdown();
    }
}
