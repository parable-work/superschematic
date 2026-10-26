//! Comments' read state, follows, deliveries and inbox (D77, batch 3):
//! markers that count stands, so a streamed reply begun before a message
//! read and finished after it is unread; markRead and follow, which ask
//! read and append no event; the unread field, by reader; who follows
//! without asking; deliveries per reason, deduped and never to the author,
//! as edits, deletes and purges move them; claims and settles across two
//! schemas, one unreadable, with their drops, refusals, backoff and two
//! concurrent claimers; the inbox's kinds, order, cursor and excerpt; the
//! schedule prune on the runner's clock; and migration 4 on a
//! conversation migration 3 holds. On SQLite and, with the variable set,
//! on Postgres.

mod support;

use std::collections::BTreeSet;
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde_json::{json, Value};

use superschematic_engine::behaviors::paging::base64url_encode;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::tools::ToolTarget;
use superschematic_engine::{
    AccessRequest, Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, DefineOptions,
    Engine, EngineErrorCode, EngineEvent, Hooks, NamespaceCall, Policy, Principal, ReadEvents,
    Result, RunnerOptions, SchemaTarget, WriteOptions,
};
use support::{
    alice, code, issue, issues_of, named, refusal, schema_document, targets, veto, RecordingPolicy,
    Target,
};

const PATIENCE: Duration = Duration::from_secs(20);

fn bob() -> Principal {
    named("bob")
}

fn carol() -> Principal {
    named("carol")
}

fn dave() -> Principal {
    named("dave")
}

fn runner() -> Principal {
    Principal::new("runner", Vec::<String>::new())
}

/// Claims Review's deliveries.
fn notifier() -> Principal {
    Principal::new("notifier", ["comments.notify"])
}

/// Claims Review's and Memo's deliveries; the policy lets it read Memo.
fn dispatcher() -> Principal {
    Principal::new("dispatcher", ["comments.notify", "memo.notify"])
}

/// Holds both permissions, and the policy lets it read no Memo.
fn blinkered() -> Principal {
    Principal::new("blinkered", ["comments.notify", "memo.notify"])
}

fn schema_of(name: &str, config: &Value) -> Value {
    let mut document = schema_document(
        name,
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"][name]["behaviors"] = json!([{"name": "Comments", "config": config}]);
    document
}

fn publish(engine: &Engine, name: &str, config: &Value) {
    engine
        .schemas()
        .define(&alice(), schema_of(name, config), DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), name, SchemaTarget::default())
        .expect("publish");
}

fn create(engine: &Engine, schema: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            schema,
            &json!({ "title": id }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

/// Review's config unless a test gives its own.
fn review_config() -> Value {
    json!({"deliverPermission": "comments.notify", "retryBackoffMs": 1000, "maxAttempts": 3, "moderatePermission": "comments.moderate"})
}

/// An engine whose Review (r1, r2) and Memo (m1) compose Comments, with a
/// manual clock at 1000, the runner acting as `runner`, and a policy that
/// lets `blinkered` and dave read no Memo, `outsider` read no schema,
/// `rita` only read, and everyone else do anything.
struct World {
    engine: Arc<Engine>,
    policy: RecordingPolicy,
}

fn world(target: &Target, review: Value) -> World {
    let policy = RecordingPolicy::new(|request: &AccessRequest<'_>| {
        let subject = request.principal().subject.as_str();
        if subject == "rita" {
            return request.action() == "read";
        }
        if subject == "outsider" {
            return request.schema().is_none();
        }
        !((subject == "blinkered" || subject == "dave") && request.schema() == Some("Memo"))
    });
    let recorded = policy.clone();
    let engine = target
        .open_core_with(|options| {
            options.clock = Clock::Manual(1000);
            options.runner = Some(RunnerOptions::new(runner()));
            options.policy = Some(Policy::custom(recorded));
        })
        .shared();
    publish(&engine, "Review", &review);
    publish(
        &engine,
        "Memo",
        &json!({"deliverPermission": "memo.notify"}),
    );
    create(&engine, "Review", "r1");
    create(&engine, "Review", "r2");
    create(&engine, "Memo", "m1");
    World { engine, policy }
}

impl World {
    fn on(
        &self,
        who: &Principal,
        schema: &str,
        id: &str,
        operation: &str,
        params: Value,
    ) -> Result<Value> {
        self.engine
            .instances()
            .invoke(who, schema, id, operation, &params, WriteOptions::default())
    }

    fn call(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.on(who, "Review", "r1", operation, params)
    }

    fn ok(&self, who: &Principal, operation: &str, params: Value) -> Value {
        self.call(who, operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    fn post(&self, who: &Principal, params: Value) -> i64 {
        self.ok(who, "post", params)["id"].as_i64().expect("an id")
    }

    fn say(&self, who: &Principal, body: &str) -> i64 {
        self.post(who, json!({ "body": body }))
    }

    fn reply(&self, who: &Principal, thread: i64, body: &str) -> i64 {
        self.post(who, json!({ "body": body, "thread": thread }))
    }

    fn message(&self, id: i64) -> Value {
        self.ok(&alice(), "getMessage", json!({ "message": id }))
    }

    fn unread(&self, who: &Principal) -> Value {
        self.engine
            .instances()
            .get(who, "Review", "r1", Default::default())
            .expect("get")
            .expect("r1")
            .behaviors["Comments"]["unread"]
            .clone()
    }

    fn ns(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.engine.behaviors().invoke(
            who,
            &NamespaceCall {
                namespace: None,
                behavior: "Comments",
                operation,
                params: Some(&params),
            },
        )
    }

    fn claim(&self, who: &Principal, params: Value) -> Value {
        self.ns(who, "claimDeliveries", params)
            .unwrap_or_else(|error| panic!("claimDeliveries: {error}"))
    }

    fn settle(&self, who: &Principal, params: Value) -> Value {
        self.ns(who, "settleDeliveries", params)
            .unwrap_or_else(|error| panic!("settleDeliveries: {error}"))
    }

    fn inbox(&self, who: &Principal, params: Value) -> Value {
        self.ns(who, "inbox", params)
            .unwrap_or_else(|error| panic!("inbox: {error}"))
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

    /// The deliveries, each `[schema, id, message, subject, mention,
    /// reply, state, attempts, error]`, sorted.
    fn deliveries(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, message, subject, mention, reply, state, attempts, error FROM bhv_comments__comment_deliveries",
        )
    }

    /// The follows, each `[schema, id, thread, subject, followed,
    /// read_up_to]`, sorted.
    fn follows(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, thread, subject, followed, read_up_to FROM bhv_comments__comment_follows",
        )
    }

    /// The conversation markers, each `[schema, id, subject, up_to]`,
    /// sorted.
    fn reads(&self) -> Vec<Value> {
        rows(
            &self.engine,
            "SELECT schema, id, subject, up_to FROM bhv_comments__comment_reads",
        )
    }
}

/// Every row a statement returns, each an array of its values, sorted by
/// their JSON.
fn rows(engine: &Engine, sql: &str) -> Vec<Value> {
    let mut out: Vec<Value> = engine
        .db()
        .read(|tx| {
            Ok::<_, superschematic_engine::storage::Error>(
                tx.all(sql, &[])?
                    .iter()
                    .map(|row| {
                        Value::Array(
                            row.values()
                                .iter()
                                .map(|value| match value {
                                    SqlValue::Null => Value::Null,
                                    SqlValue::Integer(number) => Value::from(*number),
                                    SqlValue::Text(text) => Value::from(text.as_str()),
                                    other => Value::from(other.to_string()),
                                })
                                .collect(),
                        )
                    })
                    .collect(),
            )
        })
        .expect("read");
    out.sort_by_key(Value::to_string);
    out
}

/// `expected`, sorted as [`rows`] sorts.
fn sorted(mut expected: Vec<Value>) -> Vec<Value> {
    expected.sort_by_key(Value::to_string);
    expected
}

/// A delivery's id, as claimDeliveries writes it.
fn delivery_id(schema: &str, id: &str, message: i64, subject: &str) -> String {
    base64url_encode(
        serde_json::to_string(&json!([schema, id, message, subject]))
            .unwrap()
            .as_bytes(),
    )
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

/// Each item's `[subject, message, attempt]`, in order.
fn claimed(claim: &Value) -> Vec<Value> {
    claim["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|item| json!([item["subject"], item["message"], item["attempt"]]))
        .collect()
}

fn ids_of(claim: &Value) -> Vec<String> {
    claim["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|item| item["id"].as_str().unwrap().to_owned())
        .collect()
}

/// Each inbox item's `[kind, schema, instanceId, message, thread, unread]`.
fn listed(page: &Value) -> Vec<Value> {
    page["items"]
        .as_array()
        .unwrap()
        .iter()
        .map(|item| {
            json!([
                item["kind"],
                item["schema"],
                item["instanceId"],
                item["message"],
                item.get("thread").cloned().unwrap_or(Value::Null),
                item["unread"]
            ])
        })
        .collect()
}

#[test]
fn markers_count_stands_so_a_reply_finished_after_a_read_is_unread() {
    for target in targets("comments_inbox_markers") {
        let world = world(&target, review_config());
        assert_eq!(world.say(&alice(), "One."), 1);
        let streamed = world.ok(&bob(), "begin", json!({}))["id"].as_i64().unwrap();
        assert_eq!(world.say(&carol(), "Three."), 3);
        assert_eq!(world.message(1)["stand"], json!(1));
        assert!(world.message(streamed).get("stand").is_none());
        assert_eq!(world.message(3)["stand"], json!(2));
        // alice posted top-level, so she counts from her own message;
        // dave has never read the conversation, so nothing is unread.
        assert_eq!(world.unread(&alice()), json!(1));
        assert_eq!(world.unread(&dave()), json!(0));
        assert_eq!(
            world.ok(&alice(), "listMessages", json!({}))["readUpTo"],
            json!(1)
        );
        assert!(world
            .ok(&dave(), "listMessages", json!({}))
            .get("readUpTo")
            .is_none());
        let before = world.events().len();
        assert_eq!(
            world.ok(&alice(), "markRead", json!({"upTo": 3})),
            json!({"readUpTo": 2, "unread": 0})
        );
        // bob's reply began before what alice read, and stands after it.
        world.ok(
            &bob(),
            "finish",
            json!({"message": streamed, "body": "Two."}),
        );
        assert_eq!(world.message(streamed)["stand"], json!(3));
        assert_eq!(world.unread(&alice()), json!(1));
        // The marker never moves back.
        assert_eq!(
            world.ok(&alice(), "markRead", json!({"upTo": 1})),
            json!({"readUpTo": 2, "unread": 1})
        );
        assert_eq!(
            world.ok(&alice(), "markRead", json!({"upTo": 99})),
            json!({"readUpTo": 3, "unread": 0})
        );
        // upTo past every message marks only what stands.
        let late = world.ok(&dave(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        assert_eq!(
            world.ok(&alice(), "markRead", json!({"upTo": 99})),
            json!({"readUpTo": 3, "unread": 0})
        );
        world.ok(&dave(), "finish", json!({"message": late, "body": "Four."}));
        assert_eq!(world.unread(&alice()), json!(1));
        assert_eq!(
            world.ok(&alice(), "listMessages", json!({}))["readUpTo"],
            json!(3)
        );
        // Each reader counts others' messages from its own marker.
        assert_eq!(world.unread(&bob()), json!(1));
        assert_eq!(world.unread(&carol()), json!(2));
        assert_eq!(world.unread(&dave()), json!(0));
        thread_markers_and_their_refusals(&world, before);
        world.engine.shutdown();
    }
}

// thread_markers_and_their_refusals is the markers test's second half:
// thread markers, and markRead's and follow's refusals.
fn thread_markers_and_their_refusals(world: &World, before: usize) {
    // Thread markers count a thread's replies.
    let begun = world.ok(&bob(), "begin", json!({"thread": 1}))["id"]
        .as_i64()
        .unwrap();
    let reply = world.reply(&carol(), 1, "A reply.");
    assert_eq!(
        world.ok(&alice(), "markRead", json!({"thread": 1, "upTo": reply})),
        json!({"readUpTo": 5, "unread": 0})
    );
    world.ok(&bob(), "finish", json!({"message": begun, "body": "Late."}));
    let thread = world.ok(&alice(), "listMessages", json!({"thread": 1}));
    assert_eq!(thread["readUpTo"], json!(5));
    assert_eq!(
        thread["items"][0]["stand"],
        json!(6),
        "the reply begun first stands last"
    );
    assert_eq!(
        world.ok(&alice(), "markRead", json!({"thread": 1, "upTo": 4})),
        json!({"readUpTo": 5, "unread": 1})
    );
    // Replies do not count in the field.
    assert_eq!(world.unread(&alice()), json!(1));
    // A thread names a top-level message.
    for (operation, params) in [
        ("markRead", json!({"thread": reply, "upTo": 1})),
        ("follow", json!({"thread": reply, "on": true})),
    ] {
        assert_eq!(
            vetoed(world.call(&alice(), operation, params)),
            (
                "not_a_thread".to_owned(),
                json!({"message": reply, "thread": 1})
            )
        );
    }
    assert_eq!(
        vetoed(world.call(&alice(), "markRead", json!({"thread": 99, "upTo": 1}))),
        ("no_message".to_owned(), json!({"message": 99}))
    );
    assert_eq!(
        vetoed(world.call(&alice(), "follow", json!({"thread": 99, "on": true}))),
        ("no_message".to_owned(), json!({"message": 99}))
    );
    // markRead and follow append no event; the posts and finishes did.
    let events = world.events();
    assert!(events[before..].iter().all(|event| !["markRead", "follow"]
        .contains(&event.change["operation"].as_str().unwrap_or_default())));
}

#[test]
fn mark_read_and_follow_ask_read_so_a_reader_may_call_them() {
    for target in targets("comments_inbox_asks_read") {
        let world = world(&target, review_config());
        world.say(&alice(), "One.");
        let rita = Principal::new("rita", Vec::<String>::new());
        world.policy.take();
        assert_eq!(
            world.ok(&rita, "markRead", json!({"upTo": 1})),
            json!({"readUpTo": 1, "unread": 0})
        );
        assert_eq!(
            world.ok(&rita, "follow", json!({"thread": 1, "on": true})),
            json!({"thread": 1, "following": true})
        );
        let asked: Vec<Value> = world
            .policy
            .take()
            .into_iter()
            .filter(|question| question["principal"] == json!("rita"))
            .map(|question| json!([question["action"], question["operation"]]))
            .collect();
        assert!(asked.contains(&json!(["read", "markRead"])), "{asked:?}");
        assert!(asked.contains(&json!(["read", "follow"])), "{asked:?}");
        assert!(
            !asked.iter().any(|question| question[0] == "write"),
            "{asked:?}"
        );
        assert_eq!(
            code(world.call(&rita, "post", json!({"body": "No."}))),
            EngineErrorCode::Forbidden
        );
        // The inbox reads; a claim writes, which the policy refuses her.
        assert_eq!(
            listed(&world.inbox(&rita, json!({}))),
            [json!(["conversation", "Review", "r1", 1, null, 0])]
        );
        assert_eq!(
            code(world.ns(&rita, "claimDeliveries", json!({}))),
            EngineErrorCode::Forbidden
        );
        world.engine.shutdown();
    }
}

#[test]
fn authors_and_the_mentioned_follow_without_asking_and_a_group_never() {
    for target in targets("comments_inbox_follows") {
        let world = world(&target, review_config());
        // A top-level mention starts the thread marker at its stand.
        world.post(
            &alice(),
            json!({"body": "Top.", "mentions": ["bob", "role:leads", "alice"]}),
        );
        // begin follows nothing; finishing does, and a reply's mention
        // starts one below its stand, so the reply counts.
        let begun = world.ok(&carol(), "begin", json!({"thread": 1}))["id"]
            .as_i64()
            .unwrap();
        assert_eq!(
            world.follows(),
            sorted(vec![
                json!(["Review", "r1", 1, "alice", 1, 1]),
                json!(["Review", "r1", 1, "bob", 1, 1]),
            ])
        );
        world.ok(
            &carol(),
            "finish",
            json!({"message": begun, "body": "Reply.", "mentions": ["dave"]}),
        );
        assert_eq!(
            world.follows(),
            sorted(vec![
                json!(["Review", "r1", 1, "alice", 1, 1]),
                json!(["Review", "r1", 1, "bob", 1, 1]),
                json!(["Review", "r1", 1, "carol", 1, 2]),
                json!(["Review", "r1", 1, "dave", 1, 1]),
            ])
        );
        assert_eq!(world.reads(), vec![json!(["Review", "r1", "alice", 1])]);
        // follow off sticks until the next mention or post.
        assert_eq!(
            world.ok(&bob(), "follow", json!({"thread": 1, "on": false})),
            json!({"thread": 1, "following": false})
        );
        assert_eq!(
            world.ok(&bob(), "follow", json!({"thread": 1, "on": false})),
            json!({"thread": 1, "following": false})
        );
        world.reply(&dave(), 1, "Three.");
        assert!(world
            .follows()
            .contains(&json!(["Review", "r1", 1, "bob", 0, 1])));
        world.post(
            &alice(),
            json!({"body": "Four.", "thread": 1, "mentions": ["bob"]}),
        );
        assert!(world
            .follows()
            .contains(&json!(["Review", "r1", 1, "bob", 1, 3])));
        world.ok(&bob(), "follow", json!({"thread": 1, "on": false}));
        world.reply(&bob(), 1, "Five.");
        assert!(world
            .follows()
            .contains(&json!(["Review", "r1", 1, "bob", 1, 5])));
        // A follow counts from the conversation's latest stand.
        assert_eq!(
            world.ok(&named("erin"), "follow", json!({"thread": 1, "on": true})),
            json!({"thread": 1, "following": true})
        );
        assert_eq!(
            world.ok(&named("erin"), "follow", json!({"thread": 1, "on": true})),
            json!({"thread": 1, "following": true})
        );
        // An interrupt makes the author follow, and the runner never.
        let frank = named("frank");
        let cut = world.ok(&frank, "begin", json!({"thread": 1}))["id"]
            .as_i64()
            .unwrap();
        world.ok(
            &frank,
            "appendDraft",
            json!({"message": cut, "text": "Half"}),
        );
        world.set_clock(1000 + 60_000);
        assert_eq!(
            world.ok(&runner(), "interrupt", json!({"message": cut}))["state"],
            json!("interrupted")
        );
        // A composing or deleted top-level message may be followed.
        let open = world.ok(&carol(), "begin", json!({}))["id"]
            .as_i64()
            .unwrap();
        world.ok(&dave(), "follow", json!({"thread": open, "on": true}));
        assert_eq!(
            world.follows(),
            sorted(vec![
                json!(["Review", "r1", 1, "alice", 1, 4]),
                json!(["Review", "r1", 1, "bob", 1, 5]),
                json!(["Review", "r1", 1, "carol", 1, 2]),
                json!(["Review", "r1", 1, "dave", 1, 3]),
                json!(["Review", "r1", 1, "erin", 1, 5]),
                json!(["Review", "r1", 1, "frank", 1, 6]),
                json!(["Review", "r1", open, "dave", 1, 6]),
            ])
        );
        world.engine.shutdown();
    }
}

#[test]
fn a_standing_message_makes_one_delivery_per_subject_never_to_its_author() {
    for target in targets("comments_inbox_deliveries") {
        let world = world(&target, review_config());
        world.post(
            &alice(),
            json!({"body": "Top.", "mentions": ["bob", "role:leads", "alice"], "clientId": "c"}),
        );
        // A retry writes nothing, deliveries included.
        world.post(
            &alice(),
            json!({"body": "Top.", "mentions": ["bob", "role:leads", "alice"], "clientId": "c"}),
        );
        assert_eq!(
            world.deliveries(),
            sorted(vec![
                json!(["Review", "r1", 1, "bob", 1, 0, "pending", 0, null]),
                json!(["Review", "r1", 1, "role:leads", 1, 0, "pending", 0, null]),
            ])
        );
        // One row with both reasons.
        world.post(
            &bob(),
            json!({"body": "Two.", "thread": 1, "mentions": ["alice"]}),
        );
        world.reply(&carol(), 1, "Three.");
        // An interrupted reply reaches the thread's followers.
        let cut = world.ok(&dave(), "begin", json!({"thread": 1}))["id"]
            .as_i64()
            .unwrap();
        world.ok(
            &dave(),
            "appendDraft",
            json!({"message": cut, "text": "Half"}),
        );
        world.ok(&dave(), "interrupt", json!({"message": cut}));
        // A composing message, and one deleted while composing, make none.
        let dropped = world.ok(&dave(), "begin", json!({"thread": 1}))["id"]
            .as_i64()
            .unwrap();
        world.ok(&dave(), "interrupt", json!({"message": dropped}));
        assert_eq!(
            world.deliveries(),
            sorted(vec![
                json!(["Review", "r1", 1, "bob", 1, 0, "pending", 0, null]),
                json!(["Review", "r1", 1, "role:leads", 1, 0, "pending", 0, null]),
                json!(["Review", "r1", 2, "alice", 1, 1, "pending", 0, null]),
                json!(["Review", "r1", 3, "alice", 0, 1, "pending", 0, null]),
                json!(["Review", "r1", 3, "bob", 0, 1, "pending", 0, null]),
                json!(["Review", "r1", 4, "alice", 0, 1, "pending", 0, null]),
                json!(["Review", "r1", 4, "bob", 0, 1, "pending", 0, null]),
                json!(["Review", "r1", 4, "carol", 0, 1, "pending", 0, null]),
            ])
        );
        // An edit that adds a mention returns a sent row to pending.
        let claim = world.claim(&notifier(), json!({"subjects": ["bob"]}));
        assert_eq!(
            claimed(&claim),
            [
                json!(["bob", 1, 1]),
                json!(["bob", 3, 1]),
                json!(["bob", 4, 1])
            ]
        );
        world.settle(
            &notifier(),
            json!({"token": claim["token"], "ids": ids_of(&claim), "outcome": "sent"}),
        );
        world.ok(
            &carol(),
            "edit",
            json!({"message": 3, "mentions": ["bob", "erin"]}),
        );
        let row = |message: i64, subject: &str| -> Value {
            world
                .deliveries()
                .into_iter()
                .find(|row| row[2] == json!(message) && row[3] == json!(subject))
                .unwrap_or(Value::Null)
        };
        assert_eq!(
            row(3, "bob"),
            json!(["Review", "r1", 3, "bob", 1, 1, "pending", 0, null])
        );
        assert_eq!(
            row(3, "erin"),
            json!(["Review", "r1", 3, "erin", 1, 0, "pending", 0, null])
        );
        assert!(world
            .follows()
            .contains(&json!(["Review", "r1", 1, "erin", 1, 2])));
        // One that removes it: a pending row with no other reason goes, one
        // with a reply keeps it.
        world.ok(&carol(), "edit", json!({"message": 3, "mentions": []}));
        assert_eq!(
            row(3, "bob"),
            json!(["Review", "r1", 3, "bob", 0, 1, "pending", 0, null])
        );
        assert_eq!(row(3, "erin"), Value::Null);
        // A claimed row is left for its claimer.
        world.ok(
            &carol(),
            "edit",
            json!({"message": 3, "mentions": ["erin"]}),
        );
        world.claim(&notifier(), json!({"subjects": ["erin"]}));
        world.ok(&carol(), "edit", json!({"message": 3, "mentions": []}));
        assert_eq!(
            row(3, "erin"),
            json!(["Review", "r1", 3, "erin", 1, 0, "claimed", 1, null])
        );
        // A moderator's edit leaves out the author, not the editor.
        let moderator = Principal::new("mo", ["comments.moderate"]);
        world.ok(
            &moderator,
            "edit",
            json!({"message": 3, "mentions": ["carol", "mo"]}),
        );
        assert_eq!(row(3, "carol"), Value::Null);
        assert_eq!(
            row(3, "mo"),
            json!(["Review", "r1", 3, "mo", 1, 0, "pending", 0, null])
        );
        world.engine.shutdown();
    }
}

#[test]
fn claims_take_due_deliveries_in_order_and_drop_what_needs_no_sending() {
    for target in targets("comments_inbox_claims") {
        let world = world(&target, review_config());
        world.post(
            &alice(),
            json!({"body": "One.", "mentions": ["agent:ada", "bob", "agent:bea"]}),
        );
        world.set_clock(1500);
        world
            .on(
                &alice(),
                "Memo",
                "m1",
                "post",
                json!({"body": "Memo.", "mentions": ["agent:ada"]}),
            )
            .unwrap();
        world.set_clock(2000);
        world.post(&alice(), json!({"body": "Two.", "mentions": ["bob"]}));
        // Refusals, in order.
        assert_eq!(
            invalid(world.ns(
                &notifier(),
                "claimDeliveries",
                json!({"subjects": ["bob"], "prefix": "agent:"})
            )),
            vec![issue("/prefix", "must not be given with subjects")]
        );
        let outsider = Principal::new("outsider", Vec::<String>::new());
        assert_eq!(
            vetoed(world.ns(&outsider, "claimDeliveries", json!({}))),
            (
                "not_configured".to_owned(),
                json!({"permission": "deliverPermission"})
            )
        );
        let refused = refusal(world.ns(&bob(), "claimDeliveries", json!({})));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not claimDeliveries in namespace default: it needs permission comments.notify, memo.notify"
            )
        );
        // The notifier claims only Review's; agents by prefix.
        let first = world.claim(&notifier(), json!({"prefix": "agent:"}));
        assert_eq!(
            claimed(&first),
            [json!(["agent:ada", 1, 1]), json!(["agent:bea", 1, 1])]
        );
        assert_eq!(first["expiresAt"], json!(2000 + 60_000));
        let item = &first["items"][0];
        assert_eq!(
            item,
            &json!({"id": delivery_id("Review", "r1", 1, "agent:ada"), "schema": "Review", "instanceId": "r1", "message": 1,
                    "subject": "agent:ada", "reasons": ["mention"], "author": "alice", "state": "posted", "postedAt": 1000,
                    "body": "One.", "truncated": false, "attempt": 1})
        );
        // A second claim takes nothing the first holds.
        assert_eq!(
            world.claim(&notifier(), json!({"prefix": "agent:"})),
            json!({"token": null, "expiresAt": null, "items": []})
        );
        assert_eq!(
            claimed(&world.claim(&notifier(), json!({"subjects": ["bob"], "limit": 1}))),
            [json!(["bob", 1, 1])]
        );
        assert_eq!(
            claimed(&world.claim(&notifier(), json!({"subjects": ["bob"]}))),
            [json!(["bob", 2, 1])]
        );
        // A schema the claimer may not read is never claimed; one it may
        // read whose permission it holds is.
        assert_eq!(
            world.claim(&blinkered(), json!({"prefix": "agent:"}))["items"],
            json!([])
        );
        let memo = world.claim(&dispatcher(), json!({"prefix": "agent:"}));
        assert_eq!(
            memo["items"][0]["id"],
            json!(delivery_id("Memo", "m1", 1, "agent:ada"))
        );
        // A lapsed claim is taken again, one attempt higher, then dropped
        // at maxAttempts.
        world.set_clock(2000 + 60_000);
        assert_eq!(
            claimed(&world.claim(&notifier(), json!({"prefix": "agent:"}))),
            [json!(["agent:ada", 1, 2]), json!(["agent:bea", 1, 2])]
        );
        world.set_clock(2000 + 120_000);
        assert_eq!(
            claimed(&world.claim(&notifier(), json!({"prefix": "agent:"}))),
            [json!(["agent:ada", 1, 3]), json!(["agent:bea", 1, 3])]
        );
        world.set_clock(2000 + 180_000);
        assert_eq!(
            world.claim(&notifier(), json!({"prefix": "agent:"}))["items"],
            json!([])
        );
        let state = |message: i64, subject: &str| -> (Value, Value) {
            let row = world
                .deliveries()
                .into_iter()
                .find(|row| {
                    row[0] == "Review" && row[2] == json!(message) && row[3] == json!(subject)
                })
                .unwrap();
            (row[6].clone(), row[8].clone())
        };
        assert_eq!(
            state(1, "agent:ada"),
            (json!("dropped"), json!("claim_lapsed"))
        );
        // A delivery its subject has read is dropped, not claimed.
        world.post(&alice(), json!({"body": "Three.", "mentions": ["carol"]}));
        world.ok(&carol(), "markRead", json!({"upTo": 3}));
        assert_eq!(
            world.claim(&notifier(), json!({"subjects": ["carol"]}))["items"],
            json!([])
        );
        assert_eq!(state(3, "carol"), (json!("dropped"), json!("read")));
        // One whose message went while it was claimed is dropped once the
        // claim lapses.
        let now = world.engine.clock().now();
        world.post(&alice(), json!({"body": "Four.", "mentions": ["dave"]}));
        assert_eq!(
            claimed(&world.claim(&notifier(), json!({"subjects": ["dave"], "ttlMs": 1000}))),
            [json!(["dave", 4, 1])]
        );
        world.ok(&alice(), "deleteMessage", json!({"message": 4}));
        assert_eq!(state(4, "dave"), (json!("claimed"), Value::Null));
        world.set_clock(now + 1000);
        assert_eq!(
            world.claim(&notifier(), json!({"subjects": ["dave"]}))["items"],
            json!([])
        );
        assert_eq!(state(4, "dave"), (json!("dropped"), json!("message_gone")));
        world.engine.shutdown();
    }
}

#[test]
fn two_claimers_at_once_never_claim_one_delivery_twice() {
    for target in targets("comments_inbox_concurrent") {
        let world = world(&target, review_config());
        for at in 0..30 {
            world.post(
                &alice(),
                json!({"body": format!("Message {at}."), "mentions": ["agent:x"]}),
            );
        }
        let taken: Vec<Vec<String>> = std::thread::scope(|scope| {
            let claimers: Vec<_> = (0..2)
                .map(|_| {
                    let world = &world;
                    scope.spawn(move || {
                        let mut mine = Vec::new();
                        loop {
                            let claim =
                                world.claim(&notifier(), json!({"prefix": "agent:", "limit": 3}));
                            if claim["items"].as_array().unwrap().is_empty() {
                                return mine;
                            }
                            let ids = ids_of(&claim);
                            world.settle(
                                &notifier(),
                                json!({"token": claim["token"], "ids": ids, "outcome": "sent"}),
                            );
                            mine.extend(ids);
                        }
                    })
                })
                .collect();
            claimers
                .into_iter()
                .map(|claimer| claimer.join().unwrap())
                .collect()
        });
        let all: Vec<&String> = taken.iter().flatten().collect();
        let distinct: BTreeSet<&String> = all.iter().copied().collect();
        assert_eq!((all.len(), distinct.len()), (30, 30));
        assert!(world
            .deliveries()
            .iter()
            .all(|row| row[6] == json!("sent") && row[7] == json!(1)));
        world.engine.shutdown();
    }
}

#[test]
fn settles_check_in_order_back_off_and_drop_at_max_attempts() {
    for target in targets("comments_inbox_settles") {
        let world = world(&target, review_config());
        world.post(
            &alice(),
            json!({"body": "One.", "mentions": ["bob", "carol", "dave"]}),
        );
        world
            .on(
                &alice(),
                "Memo",
                "m1",
                "post",
                json!({"body": "Memo.", "mentions": ["bob"]}),
            )
            .unwrap();
        let claim = world.claim(&notifier(), json!({"subjects": ["bob", "carol"]}));
        let token = claim["token"].as_str().unwrap().to_owned();
        let bob_id = delivery_id("Review", "r1", 1, "bob");
        let carol_id = delivery_id("Review", "r1", 1, "carol");
        let dave_id = delivery_id("Review", "r1", 1, "dave");
        assert_eq!(ids_of(&claim), [bob_id.clone(), carol_id.clone()]);
        // 1: an error with sent.
        assert_eq!(
            invalid(world.ns(
                &bob(),
                "settleDeliveries",
                json!({"token": token, "ids": [bob_id], "outcome": "sent", "error": "no"})
            )),
            vec![issue("/error", "must not be given with outcome sent")]
        );
        // 2 and 3: as a claim's.
        assert_eq!(
            vetoed(world.ns(
                &Principal::new("outsider", Vec::<String>::new()),
                "settleDeliveries",
                json!({"token": token, "ids": [bob_id], "outcome": "sent"})
            )),
            (
                "not_configured".to_owned(),
                json!({"permission": "deliverPermission"})
            )
        );
        let refused = refusal(world.ns(
            &bob(),
            "settleDeliveries",
            json!({"token": token, "ids": [bob_id], "outcome": "sent"}),
        ));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "bob may not settleDeliveries in namespace default: it needs permission comments.notify, memo.notify"
            )
        );
        // 4: ids that decode to nothing, name no row, or a row of a schema
        // the caller may not settle.
        let memo_id = delivery_id("Memo", "m1", 1, "bob");
        let unknown = delivery_id("Review", "r1", 99, "bob");
        let shaped = base64url_encode(br#"["Review","r1",0,"bob"]"#);
        assert_eq!(
            vetoed(world.ns(
                &notifier(),
                "settleDeliveries",
                json!({"token": token, "ids": ["nope", bob_id, unknown, memo_id, shaped], "outcome": "sent"})
            )),
            (
                "unknown_delivery".to_owned(),
                json!({"ids": ["nope", unknown, memo_id, shaped]})
            )
        );
        // 5: a token no row holds.
        assert_eq!(
            vetoed(world.ns(
                &notifier(),
                "settleDeliveries",
                json!({"token": "stale", "ids": [bob_id], "outcome": "sent"})
            )),
            ("token_stale".to_owned(), json!({"token": "stale"}))
        );
        // 6: a row that does not hold it; nothing settles.
        assert_eq!(
            vetoed(world.ns(
                &notifier(),
                "settleDeliveries",
                json!({"token": token, "ids": [bob_id, dave_id], "outcome": "sent"})
            )),
            (
                "not_claimed".to_owned(),
                json!({"token": token, "ids": [dave_id]})
            )
        );
        assert!(world
            .deliveries()
            .iter()
            .filter(|row| row[0] == "Review" && (row[3] == "bob" || row[3] == "carol"))
            .all(|row| row[6] == "claimed"));
        // A claim that lapsed and still holds its token settles.
        world.set_clock(1000 + 60_000);
        assert_eq!(
            world.settle(
                &notifier(),
                json!({"token": token, "ids": [bob_id], "outcome": "sent"})
            ),
            json!({"items": [{"id": bob_id, "state": "sent", "attempts": 1}]})
        );
        // Failures back off, doubling, and the last attempt's drops.
        let now = 1000 + 60_000;
        assert_eq!(
            world.settle(
                &notifier(),
                json!({"token": token, "ids": [carol_id], "outcome": "failed", "error": "smtp 451"})
            ),
            json!({"items": [{"id": carol_id, "state": "failed", "attempts": 1, "nextAttemptAt": now + 1000}]})
        );
        assert_eq!(
            world.claim(&notifier(), json!({"subjects": ["carol"]}))["items"],
            json!([])
        );
        world.set_clock(now + 1000);
        let again = world.claim(&notifier(), json!({"subjects": ["carol"]}));
        assert_eq!(claimed(&again), [json!(["carol", 1, 2])]);
        assert_eq!(
            world.settle(
                &notifier(),
                json!({"token": again["token"], "ids": [carol_id], "outcome": "failed"})
            ),
            json!({"items": [{"id": carol_id, "state": "failed", "attempts": 2, "nextAttemptAt": now + 1000 + 2000}]})
        );
        world.set_clock(now + 3000);
        let last = world.claim(&notifier(), json!({"subjects": ["carol"]}));
        assert_eq!(claimed(&last), [json!(["carol", 1, 3])]);
        assert_eq!(
            world.settle(
                &notifier(),
                json!({"token": last["token"], "ids": [carol_id], "outcome": "failed", "error": "smtp 550"})
            ),
            json!({"items": [{"id": carol_id, "state": "dropped", "attempts": 3}]})
        );
        let dropped = world
            .deliveries()
            .into_iter()
            .find(|row| row[0] == "Review" && row[3] == "carol")
            .unwrap();
        assert_eq!(
            dropped,
            json!(["Review", "r1", 1, "carol", 1, 0, "dropped", 3, "smtp 550"])
        );
        // A settled token is stale.
        assert_eq!(
            vetoed(world.ns(
                &notifier(),
                "settleDeliveries",
                json!({"token": token, "ids": [bob_id], "outcome": "sent"})
            ))
            .0,
            "token_stale"
        );
        // Dropped by the notifier, with its reason.
        let claim = world.claim(&notifier(), json!({"subjects": ["dave"]}));
        assert_eq!(
            world.settle(
                &notifier(),
                json!({"token": claim["token"], "ids": [dave_id], "outcome": "dropped", "error": "opted out"})
            ),
            json!({"items": [{"id": dave_id, "state": "dropped", "attempts": 1}]})
        );
        world.engine.shutdown();
    }
}

#[test]
fn the_inbox_lists_mentions_threads_and_conversations_newest_first() {
    for target in targets("comments_inbox_inbox") {
        let world = world(&target, review_config());
        let r2 = |who: &Principal, operation: &str, params: Value| {
            world
                .on(who, "Review", "r2", operation, params)
                .unwrap_or_else(|error| panic!("{operation}: {error}"))
        };
        world.post(
            &alice(),
            json!({"body": "Look, carol and dave.", "mentions": ["carol", "dave"]}),
        );
        world.set_clock(2000);
        r2(&carol(), "post", json!({"body": "Mine."}));
        world.set_clock(3000);
        r2(&bob(), "post", json!({"body": "First reply.", "thread": 1}));
        // At one postedAt, a mention before a thread before a conversation.
        world.set_clock(4000);
        r2(
            &bob(),
            "post",
            json!({"body": "Second reply, carol.", "thread": 1, "mentions": ["carol"]}),
        );
        r2(&bob(), "post", json!({"body": "Top again."}));
        world.set_clock(5000);
        let long = format!("Line one,\n\n  then\ttwo.  {}", "x".repeat(250));
        world
            .on(
                &alice(),
                "Memo",
                "m1",
                "post",
                json!({"body": long, "mentions": ["carol", "dave"]}),
            )
            .unwrap();
        let everything = world.inbox(&carol(), json!({}));
        assert_eq!(everything["next"], Value::Null);
        assert_eq!(
            listed(&everything),
            [
                json!(["mention", "Memo", "m1", 1, null, 1]),
                json!(["mention", "Review", "r2", 3, 1, 1]),
                json!(["thread", "Review", "r2", 3, 1, 2]),
                json!(["conversation", "Review", "r2", 4, null, 1]),
                json!(["mention", "Review", "r1", 1, null, 1]),
            ]
        );
        let excerpt = format!("Line one, then two. {}…", "x".repeat(179));
        assert_eq!(
            everything["items"][0],
            json!({"kind": "mention", "schema": "Memo", "instanceId": "m1", "message": 1, "author": "alice",
                   "postedAt": 5000, "excerpt": excerpt, "unread": 1})
        );
        assert_eq!(
            everything["items"][2],
            json!({"kind": "thread", "schema": "Review", "instanceId": "r2", "message": 3, "thread": 1, "author": "bob",
                   "postedAt": 4000, "excerpt": "Second reply, carol.", "unread": 2})
        );
        // Paged by the cursor, two at a time.
        let mut pages = Vec::new();
        let mut cursor = Value::Null;
        loop {
            let mut params = json!({"limit": 2});
            if !cursor.is_null() {
                params["cursor"] = cursor.clone();
            }
            let page = world.inbox(&carol(), params);
            pages.push(listed(&page));
            cursor = page["next"].clone();
            if cursor.is_null() {
                break;
            }
        }
        assert_eq!(pages.concat(), listed(&everything));
        assert_eq!(pages.iter().map(Vec::len).collect::<Vec<_>>(), [2, 2, 1]);
        // Reading the thread finishes its mention and its item.
        assert_eq!(
            r2(&carol(), "markRead", json!({"thread": 1, "upTo": 3})),
            json!({"readUpTo": 3, "unread": 0})
        );
        assert_eq!(
            listed(&world.inbox(&carol(), json!({"unread": true}))),
            [
                json!(["mention", "Memo", "m1", 1, null, 1]),
                json!(["conversation", "Review", "r2", 4, null, 1]),
                json!(["mention", "Review", "r1", 1, null, 1]),
            ]
        );
        assert_eq!(
            listed(&world.inbox(&carol(), json!({"unread": false}))),
            [
                json!(["mention", "Review", "r2", 3, 1, 0]),
                json!(["thread", "Review", "r2", 3, 1, 0]),
            ]
        );
        assert_eq!(
            listed(&world.inbox(
                &carol(),
                json!({"kinds": ["thread", "conversation"], "unread": true})
            )),
            [json!(["conversation", "Review", "r2", 4, null, 1])]
        );
        // dave reads no Memo, so its rows never reach him; posting
        // top-level after the mention reads it.
        assert_eq!(
            listed(&world.inbox(&dave(), json!({}))),
            [json!(["mention", "Review", "r1", 1, null, 1])]
        );
        world.say(&dave(), "Read it.");
        assert_eq!(
            listed(&world.inbox(&dave(), json!({}))),
            [
                json!(["mention", "Review", "r1", 1, null, 0]),
                json!(["conversation", "Review", "r1", 1, null, 0]),
            ]
        );
        // A cursor this operation did not write.
        for cursor in [
            "nope".to_owned(),
            base64url_encode(br#"[1,"mention","Review","r1"]"#),
            base64url_encode(br#"[1,"other","Review","r1",1]"#),
            base64url_encode(br#"[1,"mention","Review","r1",0]"#),
        ] {
            assert_eq!(
                invalid(world.ns(&carol(), "inbox", json!({"cursor": cursor}))),
                vec![issue("/cursor", "is not a cursor this operation returned")]
            );
        }
        // Its MCP tool is the behavior's.
        let manifest = world
            .engine
            .tools()
            .manifest(&carol(), ToolTarget::default())
            .unwrap();
        let names: Vec<&str> = manifest["tools"]
            .as_array()
            .unwrap()
            .iter()
            .filter_map(|tool| tool["name"].as_str())
            .collect();
        for name in [
            "comments.inbox",
            "comments.claimDeliveries",
            "comments.settleDeliveries",
        ] {
            assert!(names.contains(&name), "{name}: {names:?}");
        }
        world.engine.shutdown();
    }
}

#[test]
fn deletes_and_purges_keep_only_claimed_deliveries_and_the_instance_takes_every_row() {
    for target in targets("comments_inbox_deletes") {
        let world = world(&target, review_config());
        world.say(&alice(), "Top.");
        let states = |message: i64| -> Vec<Value> {
            world
                .deliveries()
                .into_iter()
                .filter(|row| row[0] == "Review" && row[1] == "r1" && row[2] == json!(message))
                .map(|row| json!([row[3], row[6]]))
                .collect()
        };
        // A reply with a delivery in each state.
        let each = |body: &str| -> i64 {
            let id = world.post(
                &bob(),
                json!({"body": body, "thread": 1, "mentions": ["claimed", "failed", "sent", "dropped", "pending"]}),
            );
            for (subject, outcome) in [
                ("claimed", None),
                ("failed", Some("failed")),
                ("sent", Some("sent")),
                ("dropped", Some("dropped")),
            ] {
                let claim = world.claim(&notifier(), json!({"subjects": [subject]}));
                if let Some(outcome) = outcome {
                    world.settle(
                        &notifier(),
                        json!({"token": claim["token"], "ids": ids_of(&claim), "outcome": outcome}),
                    );
                }
            }
            id
        };
        let deleted = each("Deleted.");
        assert_eq!(
            states(deleted),
            sorted(vec![
                json!(["alice", "pending"]),
                json!(["claimed", "claimed"]),
                json!(["dropped", "dropped"]),
                json!(["failed", "failed"]),
                json!(["pending", "pending"]),
                json!(["sent", "sent"]),
            ])
        );
        world.ok(&bob(), "deleteMessage", json!({"message": deleted}));
        assert_eq!(states(deleted), [json!(["claimed", "claimed"])]);
        // Its stand stays, and so do follows and markers.
        assert_eq!(world.message(deleted)["stand"], json!(2));
        let purged = each("Purged.");
        let moderator = Principal::new("mo", ["comments.moderate"]);
        let follows_before = world.follows().len();
        assert!(follows_before > 0);
        world.ok(&moderator, "purge", json!({"message": 1}));
        assert_eq!(states(purged), [json!(["claimed", "claimed"])]);
        assert_eq!(states(deleted), [json!(["claimed", "claimed"])]);
        assert_eq!(world.follows(), Vec::<Value>::new());
        assert_eq!(world.reads(), vec![json!(["Review", "r1", "alice", 1])]);
        // The instance's delete takes every row, claimed ones too, and a
        // re-created instance numbers its stands on.
        let claim = world.claim(&notifier(), json!({"subjects": ["claimed"]}));
        assert!(claim["items"].as_array().unwrap().is_empty());
        world
            .engine
            .instances()
            .delete(&alice(), "Review", "r1", WriteOptions::default())
            .unwrap();
        assert!(world.deliveries().iter().all(|row| row[1] != "r1"));
        assert_eq!(world.reads(), Vec::<Value>::new());
        create(&world.engine, "Review", "r1");
        let id = world.say(&alice(), "Again.");
        assert_eq!(world.message(id)["stand"], json!(4));
        world.engine.shutdown();
    }
}

#[test]
fn the_schedule_prune_removes_deliveries_past_retention_on_the_runners_clock() {
    for target in targets("comments_inbox_prune") {
        let mut config = review_config();
        config["deliveryRetentionMs"] = json!(86_400_000);
        let world = world(&target, config);
        // The runner finds the schedule, which runs an interval later.
        world
            .engine
            .runner()
            .run_due()
            .expect("the runner's first pass");
        world.post(
            &alice(),
            json!({"body": "One.", "mentions": ["pending", "sent", "failed", "held"]}),
        );
        for (subject, outcome) in [("sent", "sent"), ("failed", "failed")] {
            let claim = world.claim(&notifier(), json!({"subjects": [subject]}));
            world.settle(
                &notifier(),
                json!({"token": claim["token"], "ids": ids_of(&claim), "outcome": outcome}),
            );
        }
        world.set_clock(1000 + 86_400_000 / 2);
        world.post(&bob(), json!({"body": "Younger.", "mentions": ["alice"]}));
        // Past retention, a live claim holds its row.
        world.set_clock(1000 + 86_400_000 + 1);
        world.claim(&notifier(), json!({"subjects": ["held"], "ttlMs": 600_000}));
        world.engine.runner().run_due().unwrap();
        let subjects = |world: &World| -> Vec<Value> {
            world
                .deliveries()
                .into_iter()
                .map(|row| json!([row[2], row[3]]))
                .collect()
        };
        assert_eq!(subjects(&world), [json!([1, "held"]), json!([2, "alice"])]);
        // Once the claim lapses, the next run takes it.
        let deadline = Instant::now() + PATIENCE;
        while subjects(&world).len() > 1 {
            assert!(Instant::now() < deadline, "the schedule never pruned");
            world.engine.clock().advance(600_000).unwrap();
            world.engine.runner().run_due().unwrap();
        }
        assert_eq!(subjects(&world), [json!([2, "alice"])]);
        world.engine.shutdown();
    }
}

// Comments at migration 3, as batch 2 shipped it: its tables, and `seed`,
// which writes a message as batch 2's post, begin and deleteMessage left
// it.
#[derive(Clone, Copy)]
struct CommentsAtThree;

impl Behavior for CommentsAtThree {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Comments", "fields": [{"name": "messages"}], "operations": [
            {"name": "seed", "writes": true, "resultSchema": true, "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"],
                "properties": {"body": {"type": "string"}, "thread": {"type": "integer"}, "state": {"enum": ["posted", "composing", "deleted"]}}}}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        let mut migrations = superschematic_engine_behaviors::Comments.hooks().migrations;
        migrations.truncate(3);
        assert_eq!(
            migrations
                .iter()
                .map(|step| step.version)
                .collect::<Vec<_>>(),
            [1, 2, 3]
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
                let state = params
                    .get("state")
                    .and_then(Value::as_str)
                    .unwrap_or("posted");
                let composing = state == "composing";
                let messages = cx.sql().table("comment_messages")?;
                let mut values = key.to_vec();
                values.extend([
                    SqlValue::Integer(id),
                    thread.map_or(SqlValue::Null, SqlValue::Integer),
                    SqlValue::from(cx.principal().subject.as_str()),
                    SqlValue::from(state),
                    SqlValue::from(if state == "posted" {
                        params["body"].as_str().unwrap_or_default()
                    } else {
                        ""
                    }),
                    SqlValue::Integer(cx.now()),
                    if composing {
                        SqlValue::Null
                    } else {
                        SqlValue::Integer(cx.now())
                    },
                    if composing {
                        SqlValue::Integer(0)
                    } else {
                        SqlValue::Null
                    },
                    if composing {
                        SqlValue::Integer(cx.now())
                    } else {
                        SqlValue::Null
                    },
                ]);
                cx.sql().run(
                    format!(
                        "INSERT INTO {messages} (namespace, schema, id, message, thread, quotes, author, via, state, body, blocks, mentions, created_at, edited_at, edits, pinned, client_id, resolved_by, resolved_at, posted_at, draft_length, last_append_at)
                         VALUES (?, ?, ?, ?, ?, NULL, ?, NULL, ?, ?, '[]', '[]', ?, NULL, 0, 0, NULL, NULL, NULL, ?, ?, ?)"
                    ),
                    &values,
                )?;
                let posted = i64::from(state == "posted");
                let open = i64::from(state == "posted" && thread.is_none());
                let mut values = key.to_vec();
                if last.is_none() {
                    values.extend([SqlValue::Integer(id), SqlValue::Integer(open)]);
                    cx.sql().run(
                        format!("INSERT INTO {conversations} (namespace, schema, id, last_message, last_reaction, last_message_at, open_threads, resolved_threads) VALUES (?, ?, ?, ?, 0, NULL, ?, 0)"),
                        &values,
                    )?;
                } else {
                    let mut params = vec![SqlValue::Integer(id), SqlValue::Integer(open)];
                    params.extend(key);
                    cx.sql().run(
                        format!("UPDATE {conversations} SET last_message = ?, open_threads = open_threads + ? WHERE namespace = ? AND schema = ? AND id = ?"),
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
fn migration_4_numbers_the_stands_of_a_conversation_migration_3_holds() {
    for target in targets("comments_inbox_migrate") {
        let mut at_three = target.options(BehaviorSet::new().with(CommentsAtThree));
        at_three.clock = Clock::Manual(1000);
        let engine = Engine::open(at_three).expect("open at migration 3");
        publish(&engine, "Review", &json!({}));
        create(&engine, "Review", "r1");
        let seed = |who: &Principal, params: Value| {
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
        seed(&alice(), json!({"body": "One."}));
        seed(&bob(), json!({"body": "", "state": "composing"}));
        seed(&bob(), json!({"body": "Three.", "thread": 1}));
        seed(
            &carol(),
            json!({"body": "", "thread": 1, "state": "deleted"}),
        );
        seed(&carol(), json!({"body": "Five."}));
        drop(engine);

        let engine = target
            .open_core_with(|options| {
                options.clock = Clock::Manual(5000);
                options.policy = Some(Policy::allow_all());
            })
            .shared();
        let world = World {
            engine: Arc::clone(&engine),
            policy: RecordingPolicy::new(|_| true),
        };
        let stands: Vec<Value> = (1..=5)
            .map(|id| {
                world
                    .message(id)
                    .get("stand")
                    .cloned()
                    .unwrap_or(Value::Null)
            })
            .collect();
        assert_eq!(
            stands,
            [json!(1), Value::Null, json!(3), json!(4), json!(5)]
        );
        // Nobody has a marker yet, so nothing is unread.
        assert_eq!(world.unread(&alice()), json!(0));
        assert_eq!(world.follows(), Vec::<Value>::new());
        // New stands come after the old ones; the composing message stands
        // when it finishes.
        assert_eq!(world.say(&dave(), "Six."), 6);
        assert_eq!(world.message(6)["stand"], json!(6));
        world.ok(
            &bob(),
            "finish",
            json!({"message": 2, "body": "Two.", "mentions": ["alice"]}),
        );
        assert_eq!(world.message(2)["stand"], json!(7));
        assert_eq!(
            world.ok(&alice(), "markRead", json!({"upTo": 1})),
            json!({"readUpTo": 1, "unread": 3})
        );
        assert_eq!(
            world.deliveries(),
            [json!([
                "Review", "r1", 2, "alice", 1, 0, "pending", 0, null
            ])]
        );
        let tables = support::tables_of(&engine, "bhv_comments__");
        for name in [
            "bhv_comments__comment_reads",
            "bhv_comments__comment_follows",
            "bhv_comments__comment_deliveries",
            "bhv_comments__comment_messages_stand",
            "bhv_comments__comment_deliveries_due",
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
        assert_eq!(ledger, [1, 2, 3, 4]);
        engine.shutdown();
    }
}
