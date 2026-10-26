//! Comments, the core's conversation on an instance (D77, batch 1): the
//! cases of `test/core-comments.test.ts` and more. post and its replies,
//! quotes, blocks and retries; edit, deleteMessage and purge; reactions,
//! pins, resolve and reopen; listMessages' four anchors, getMessage,
//! listEdits and listReactions; the record's thread members and the three
//! volatile fields; every refusal in its order; every write beside the
//! instance, which keeps its seq and appends an event of no sequence; the
//! policy's questions; delete; the rule for a new version; and migration 2
//! moving version 1's comments into threads. On SQLite and, with the
//! variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::behaviors::{BehaviorMigration, ColumnSpec, TableSpec};
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, DefineOptions, Engine,
    EngineErrorCode, EngineEvent, EngineOptions, Error, Hooks, InstanceRecord, ListOptions, Policy,
    Principal, PrincipalService, ReadEvents, Result, SchemaTarget, WriteOptions,
};
use support::{
    alice, code, issue, issues_of, named, refusal, schema_document, tables_of, targets, veto,
    RecordingPolicy, Target,
};

fn review_schema(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Review",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    if behaviors.as_array().is_some_and(|list| !list.is_empty()) {
        document["types"]["Review"]["behaviors"] = behaviors;
    }
    document
}

fn comments(config: Value) -> Value {
    json!([{"name": "Comments", "config": config}])
}

fn bob() -> Principal {
    named("bob")
}

fn carol() -> Principal {
    named("carol")
}

fn moderator() -> Principal {
    Principal::new(
        "mo",
        ["comments.moderate", "comments.pin", "comments.resolve"],
    )
}

fn publish(engine: &Engine, document: &Value) -> std::result::Result<i64, Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())?;
    Ok(engine
        .schemas()
        .publish(&alice(), "Review", SchemaTarget::default())?
        .version)
}

fn create(engine: &Engine, title: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Review",
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

/// An engine whose Review composes Comments with the config given, with a
/// manual clock at 1000, and r1 and r2 created by alice.
struct World {
    engine: Engine,
}

fn world_with(target: &Target, config: Value, configure: impl FnOnce(&mut EngineOptions)) -> World {
    let engine = target.open_core_with(|options| {
        options.clock = Clock::Manual(1000);
        configure(options);
    });
    publish(&engine, &review_schema(comments(config))).expect("publish");
    create(&engine, "Q3 plan", "r1");
    create(&engine, "Q4 plan", "r2");
    World { engine }
}

fn world(target: &Target, config: Value) -> World {
    world_with(target, config, |_| {})
}

impl World {
    fn on(&self, who: &Principal, id: &str, operation: &str, params: Value) -> Result<Value> {
        self.engine.instances().invoke(
            who,
            "Review",
            id,
            operation,
            &params,
            WriteOptions::default(),
        )
    }

    fn call(&self, who: &Principal, operation: &str, params: Value) -> Result<Value> {
        self.on(who, "r1", operation, params)
    }

    fn ok(&self, who: &Principal, operation: &str, params: Value) -> Value {
        self.call(who, operation, params)
            .unwrap_or_else(|error| panic!("{operation}: {error}"))
    }

    fn post(&self, who: &Principal, params: Value) -> Value {
        self.ok(who, "post", params)
    }

    fn say(&self, who: &Principal, body: &str) -> i64 {
        self.post(who, json!({ "body": body }))["id"]
            .as_i64()
            .expect("an id")
    }

    fn reply(&self, who: &Principal, thread: i64, body: &str) -> i64 {
        self.post(who, json!({ "body": body, "thread": thread }))["id"]
            .as_i64()
            .expect("an id")
    }

    fn get(&self, id: &str) -> InstanceRecord {
        self.engine
            .instances()
            .get(&alice(), "Review", id, Default::default())
            .expect("get")
            .expect("the instance")
    }

    fn fields(&self) -> Value {
        self.get("r1").behaviors["Comments"].clone()
    }

    fn message(&self, id: i64) -> Value {
        self.ok(&alice(), "getMessage", json!({ "message": id }))
    }

    fn set_clock(&self, now: i64) {
        self.engine.clock().set(now).expect("set the clock");
    }

    fn events(&self, beside: Option<bool>) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Review"),
                    instance_id: Some("r1"),
                    beside,
                    limit: Some(500),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events
    }
}

// vetoed is a veto's code and details.
fn vetoed<T: std::fmt::Debug>(result: Result<T>) -> (String, Value) {
    let (behavior, _, _, code, details) = veto(result);
    assert_eq!(behavior, "Comments");
    (code.expect("a code"), details.unwrap_or(Value::Null))
}

fn ids(page: &Value) -> Vec<i64> {
    page["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|item| item["id"].as_i64().expect("an id"))
        .collect()
}

#[test]
fn post_numbers_messages_per_instance_and_the_fields_count_them() {
    for target in targets("comments_post") {
        let world = world(&target, json!({}));
        assert_eq!(
            world.fields(),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            world.post(&alice(), json!({"body": "Looks right."})),
            json!({"id": 1, "author": "alice", "state": "posted", "body": "Looks right.", "blocks": [], "mentions": [],
                   "reactions": {}, "createdAt": 1000, "postedAt": 1000, "stand": 1, "edits": 0, "pinned": false, "replies": 0, "participants": ["alice"]})
        );
        world.set_clock(2000);
        assert_eq!(
            world.post(
                &bob(),
                json!({"body": "Agreed.", "thread": 1, "mentions": ["alice", "role:leads"]})
            ),
            json!({"id": 2, "thread": 1, "author": "bob", "state": "posted", "body": "Agreed.", "blocks": [],
                   "mentions": ["alice", "role:leads"], "reactions": {}, "createdAt": 2000, "postedAt": 2000, "stand": 2, "edits": 0, "pinned": false})
        );
        world.set_clock(3000);
        // A service acting for an end user posts as the user, through it.
        let through = carol().with_service(PrincipalService {
            deployable: "inbox".to_owned(),
            serves: Vec::new(),
            subject: "svc-inbox".to_owned(),
            stands_in: false,
        });
        assert_eq!(
            world.post(
                &through,
                json!({"body": "Quoting bob.", "thread": 1, "quotes": 2})
            )["via"],
            json!("inbox")
        );
        assert_eq!(
            world.message(3)["quotes"],
            json!(2),
            "a reply quotes a reply of its thread"
        );
        assert_eq!(world.say(&alice(), "Second thread."), 4);
        assert_eq!(
            world
                .on(&alice(), "r2", "post", json!({"body": "First on r2."}))
                .unwrap()["id"],
            json!(1)
        );
        let top = world.message(1);
        assert_eq!(
            (&top["replies"], &top["lastReplyAt"], &top["participants"]),
            (&json!(2), &json!(3000), &json!(["carol", "bob", "alice"]))
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 4, "lastMessageAt": 3000, "threads": {"open": 2, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            world.get("r2").behaviors["Comments"],
            json!({"messages": 1, "lastMessageAt": 3000, "threads": {"open": 1, "resolved": 0}, "composing": [], "unread": 0})
        );
        let counts: Vec<Value> = world
            .engine
            .instances()
            .list(&alice(), "Review", ListOptions::default())
            .expect("list")
            .items
            .into_iter()
            .map(|item| item.behaviors["Comments"]["messages"].clone())
            .collect();
        assert_eq!(counts, [json!(4), json!(1)]);
    }
}

#[test]
fn participants_are_the_latest_ten_authors_of_posted_messages() {
    for target in targets("comments_participants") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Roll call.");
        for n in 0..12 {
            world.reply(&named(&format!("p{n:02}")), 1, "Here.");
        }
        world.reply(&named("p03"), 1, "Here again.");
        let top = world.message(1);
        assert_eq!(top["replies"], json!(13));
        assert_eq!(
            top["participants"],
            json!(["p03", "p11", "p10", "p09", "p08", "p07", "p06", "p05", "p04", "p02"])
        );
        // A deleted top-level message's author is in it only by a reply.
        world.ok(&alice(), "deleteMessage", json!({"message": 1}));
        let deleted = world.message(1);
        assert_eq!(
            (
                &deleted["state"],
                &deleted["replies"],
                &deleted["participants"][9]
            ),
            (&json!("deleted"), &json!(13), &json!("p02"))
        );
        world.reply(&alice(), 1, "Still here.");
        assert_eq!(world.message(1)["participants"][0], json!("alice"));
    }
}

#[test]
fn post_checks_in_order_and_a_retry_with_its_client_id_posts_nothing() {
    for target in targets("comments_post_checks") {
        let world = world(&target, json!({"maxBodyLength": 10}));
        world
            .engine
            .instances()
            .create(
                &alice(),
                "Review",
                &json!({"title": "Linked"}),
                CreateOptions {
                    id: Some("r3"),
                    ..Default::default()
                },
            )
            .unwrap();
        let first = world.post(&alice(), json!({"body": "Hi.", "clientId": "c-1"}));
        let head = world.engine.events().head().unwrap();
        // 1: a retry returns the message as it is now, whatever the params.
        assert_eq!(
            world.post(
                &alice(),
                json!({"body": "Different, and too long.", "clientId": "c-1"})
            ),
            first
        );
        assert_eq!(world.engine.events().head().unwrap(), head, "no event");
        // Another author's clientId is its own.
        assert_eq!(
            world.post(&bob(), json!({"body": "Hi.", "clientId": "c-1"}))["id"],
            json!(2)
        );
        let refused = |params: Value| refusal(world.call(&alice(), "post", params));
        let invalid = refused(json!({"body": "Eleven code"}));
        assert_eq!(invalid.name(), "OperationParamsError");
        assert_eq!(
            issues_of(&invalid),
            [issue("/body", "must NOT have more than 10 characters")]
        );
        // Code points, not bytes.
        world.post(&alice(), json!({"body": "ééééééééé€"}));
        assert_eq!(
            issues_of(&refused(json!({"body": "x", "quotes": 1}))),
            [issue("/quotes", "needs thread")]
        );
        let big = "x".repeat(8200);
        assert_eq!(
            issues_of(&refused(json!({"body": "x", "blocks": [
                {"kind": "action", "title": "Small", "status": "running", "data": {}},
                {"kind": "action", "title": "Big", "status": "failed", "data": {"log": big}}
            ]}))),
            [issue("/blocks/1/data", "is more than 8192 bytes of JSON")]
        );
        for params in [
            json!({"body": ""}),
            json!({"body": "  \n "}),
            json!({}),
            json!({"body": "x", "replyTo": 1}),
            json!({"body": "x", "thread": 0}),
            json!({"body": "x", "mentions": ["a", "a"]}),
            json!({"body": "x", "mentions": ["has space"]}),
            json!({"body": "x", "blocks": [{"kind": "code"}]}),
        ] {
            assert_eq!(refused(params).name(), "OperationParamsError");
        }
        world.reply(&bob(), 1, "Reply.");
        assert_eq!(
            vetoed(world.call(&alice(), "post", json!({"body": "x", "thread": 9}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        assert_eq!(
            vetoed(world.call(&alice(), "post", json!({"body": "x", "thread": 4}))),
            (
                "not_a_thread".to_owned(),
                json!({"message": 4, "thread": 1})
            )
        );
        assert_eq!(
            vetoed(world.call(
                &alice(),
                "post",
                json!({"body": "x", "thread": 1, "quotes": 9})
            )),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        assert_eq!(
            vetoed(world.call(
                &alice(),
                "post",
                json!({"body": "x", "thread": 1, "quotes": 2})
            )),
            (
                "not_in_thread".to_owned(),
                json!({"message": 2, "thread": 1})
            )
        );
        world.ok(&bob(), "deleteMessage", json!({"message": 4}));
        assert_eq!(
            vetoed(world.call(
                &alice(),
                "post",
                json!({"body": "x", "thread": 1, "quotes": 4})
            )),
            ("deleted".to_owned(), json!({"message": 4}))
        );
        // A thread checks before blocks; a ref names an instance the
        // caller reads, and one refusal covers every other case.
        for (block, schema, id) in [
            (
                json!({"kind": "ref", "schema": "Review", "id": "r9"}),
                "Review",
                "r9",
            ),
            (
                json!({"kind": "ref", "schema": "Nothing", "id": "x"}),
                "Nothing",
                "x",
            ),
        ] {
            assert_eq!(
                vetoed(world.call(
                    &alice(),
                    "post",
                    json!({"body": "x", "blocks": [
                        {"kind": "action", "title": "Run", "status": "running"}, block
                    ]})
                )),
                (
                    "ref_not_found".to_owned(),
                    json!({"block": 1, "schema": schema, "id": id})
                )
            );
        }
        let blocks = json!([
            {"kind": "ref", "schema": "Review", "id": "r3"},
            {"kind": "action", "title": "Deploy", "status": "succeeded", "data": {"url": "https://x", "n": 1.50}}
        ]);
        assert_eq!(
            world.post(&alice(), json!({"body": "See r3.", "blocks": blocks}))["blocks"],
            blocks,
            "stored and returned as given"
        );
        // Posting in a deleted top-level message's thread is allowed.
        world.ok(&alice(), "deleteMessage", json!({"message": 1}));
        world.reply(&carol(), 1, "Late.");
        assert_eq!(world.fields()["messages"], json!(4));
    }
}

#[test]
fn the_config_limits_block_kinds_and_a_ref_is_read_as_the_caller() {
    for target in targets("comments_blocks") {
        let policy = RecordingPolicy::new(|request| {
            request.schema() != Some("Secret") || request.principal().subject == "alice"
        });
        let world = world_with(&target, json!({"blocks": ["ref"]}), move |options| {
            options.policy = Some(Policy::custom(policy));
        });
        let mut secret = schema_document(
            "Secret",
            json!([{"name": "title", "typeRef": {"name": "string"}}]),
            json!({}),
        );
        secret["name"] = json!("Secret");
        world
            .engine
            .schemas()
            .define(&alice(), &secret, DefineOptions::default())
            .unwrap();
        world
            .engine
            .schemas()
            .publish(&alice(), "Secret", SchemaTarget::default())
            .unwrap();
        world
            .engine
            .instances()
            .create(
                &alice(),
                "Secret",
                &json!({"title": "s"}),
                CreateOptions {
                    id: Some("s1"),
                    ..Default::default()
                },
            )
            .unwrap();
        let block = json!({"kind": "ref", "schema": "Secret", "id": "s1"});
        world.post(&alice(), json!({"body": "Mine.", "blocks": [block]}));
        assert_eq!(
            vetoed(world.call(&bob(), "post", json!({"body": "Peek.", "blocks": [block]}))),
            (
                "ref_not_found".to_owned(),
                json!({"block": 0, "schema": "Secret", "id": "s1"})
            )
        );
        assert_eq!(
            vetoed(world.call(
                &bob(),
                "post",
                json!({"body": "Act.", "blocks": [
                    {"kind": "ref", "schema": "Review", "id": "r2"},
                    {"kind": "action", "title": "Run", "status": "running"}
                ]})
            )),
            (
                "block_not_allowed".to_owned(),
                json!({"block": 1, "kind": "action", "allowed": ["ref"]})
            )
        );
        // An edit that keeps a ref the message holds does not read it again.
        world.ok(
            &alice(),
            "edit",
            json!({"message": 1, "body": "Mine, edited."}),
        );
        assert_eq!(
            world.ok(&alice(), "edit", json!({"message": 1, "blocks": [block, {"kind": "ref", "schema": "Review", "id": "r2"}]}))["blocks"][1]["id"],
            json!("r2")
        );
    }
    for target in targets("comments_no_blocks") {
        let world = world(&target, json!({"blocks": []}));
        assert_eq!(
            vetoed(world.call(
                &alice(),
                "post",
                json!({"body": "x", "blocks": [{"kind": "ref", "schema": "Review", "id": "r2"}]})
            )),
            (
                "block_not_allowed".to_owned(),
                json!({"block": 0, "kind": "ref", "allowed": []})
            )
        );
        world.post(&alice(), json!({"body": "x", "blocks": []}));
    }
}

#[test]
fn edit_replaces_what_it_gives_and_list_edits_reads_what_each_edit_replaced() {
    for target in targets("comments_edit") {
        let world = world(
            &target,
            json!({"editWindowMs": 60000, "moderatePermission": "comments.moderate"}),
        );
        world.post(&alice(), json!({"body": "Draft.", "mentions": ["bob"]}));
        assert_eq!(
            issues_of(&refusal(world.call(
                &alice(),
                "edit",
                json!({"message": 1})
            ))),
            [issue("", "names none of body, blocks and mentions")]
        );
        world.set_clock(2000);
        let edited = world.ok(&alice(), "edit", json!({"message": 1, "body": "Final."}));
        assert_eq!(
            (
                &edited["body"],
                &edited["mentions"],
                &edited["editedAt"],
                &edited["edits"]
            ),
            (&json!("Final."), &json!(["bob"]), &json!(2000), &json!(1))
        );
        world.set_clock(3000);
        // An edit to the same values is still an edit.
        world.ok(
            &alice(),
            "edit",
            json!({"message": 1, "body": "Final.", "mentions": []}),
        );
        assert_eq!(world.message(1)["edits"], json!(2));
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 1})),
            json!({"items": [
                {"edit": 1, "body": "Draft.", "blocks": [], "mentions": ["bob"], "by": "alice", "at": 2000},
                {"edit": 2, "body": "Final.", "blocks": [], "mentions": ["bob"], "by": "alice", "at": 3000}
            ], "next": null})
        );
        let page = world.ok(&alice(), "listEdits", json!({"message": 1, "limit": 1}));
        assert_eq!(page["items"][0]["edit"], json!(1));
        assert_eq!(
            world.ok(
                &alice(),
                "listEdits",
                json!({"message": 1, "limit": 1, "cursor": page["next"]})
            )["items"][0]["edit"],
            json!(2)
        );
        assert_eq!(
            issues_of(&refusal(world.call(
                &alice(),
                "listEdits",
                json!({"message": 1, "cursor": "bogus"})
            ))),
            [issue("/cursor", "is not a cursor this operation returned")]
        );
        assert_eq!(
            vetoed(world.call(&bob(), "edit", json!({"message": 1, "body": "Mine now."}))),
            (
                "not_author".to_owned(),
                json!({"message": 1, "author": "alice"})
            )
        );
        // A moderator edits another's, past the window too.
        world.set_clock(100_000);
        assert_eq!(
            vetoed(world.call(&alice(), "edit", json!({"message": 1, "body": "Late."}))),
            (
                "edit_window_passed".to_owned(),
                json!({"message": 1, "postedAt": 1000, "editWindowMs": 60000})
            )
        );
        assert_eq!(
            world.ok(
                &moderator(),
                "edit",
                json!({"message": 1, "body": "Moderated."})
            )["body"],
            json!("Moderated.")
        );
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 1}))["items"][2]["by"],
            json!("mo")
        );
        assert_eq!(
            vetoed(world.call(&alice(), "edit", json!({"message": 7, "body": "x"}))),
            ("no_message".to_owned(), json!({"message": 7}))
        );
        assert_eq!(
            vetoed(world.call(&alice(), "listEdits", json!({"message": 7}))),
            ("no_message".to_owned(), json!({"message": 7}))
        );
        world.ok(&alice(), "deleteMessage", json!({"message": 1}));
        assert_eq!(
            vetoed(world.call(&alice(), "edit", json!({"message": 1, "body": "x"}))),
            ("deleted".to_owned(), json!({"message": 1}))
        );
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 1})),
            json!({"items": [], "next": null})
        );
    }
}

#[test]
fn a_message_takes_at_most_500_edits() {
    for target in targets("comments_too_many_edits") {
        let world = world(&target, json!({}));
        world.say(&alice(), "v0");
        for n in 1..=500 {
            world.ok(
                &alice(),
                "edit",
                json!({"message": 1, "body": format!("v{n}")}),
            );
        }
        assert_eq!(
            vetoed(world.call(&alice(), "edit", json!({"message": 1, "body": "one more"}))),
            (
                "too_many_edits".to_owned(),
                json!({"message": 1, "max": 500})
            )
        );
        let page = world.ok(&alice(), "listEdits", json!({"message": 1, "limit": 500}));
        assert_eq!(
            (page["items"].as_array().unwrap().len(), &page["next"]),
            (500, &Value::Null)
        );
    }
}

#[test]
fn delete_message_keeps_its_place_and_purge_removes_a_thread_for_good() {
    for target in targets("comments_delete_purge") {
        let world = world(&target, json!({"moderatePermission": "comments.moderate"}));
        world.post(&alice(), json!({"body": "Top.", "clientId": "t"}));
        world.reply(&bob(), 1, "Reply.");
        world.post(
            &carol(),
            json!({"body": "Quoting.", "thread": 1, "quotes": 2}),
        );
        world.ok(&bob(), "react", json!({"message": 1, "emoji": ":+1:"}));
        world.ok(&alice(), "pin", json!({"message": 1}));
        world.ok(
            &alice(),
            "edit",
            json!({"message": 1, "body": "Top, edited."}),
        );
        world.set_clock(5000);
        world.say(&bob(), "Other thread.");
        assert_eq!(
            vetoed(world.call(&bob(), "deleteMessage", json!({"message": 1}))),
            (
                "not_author".to_owned(),
                json!({"message": 1, "author": "alice"})
            )
        );
        assert_eq!(
            world.ok(&alice(), "deleteMessage", json!({"message": 1})),
            json!({"id": 1, "author": "alice", "state": "deleted", "body": "", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 1000, "postedAt": 1000, "stand": 1, "edits": 0, "pinned": false, "replies": 2, "lastReplyAt": 1000, "participants": ["carol", "bob"]})
        );
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 1})),
            json!({"items": [], "next": null})
        );
        // A retry of its post returns the deleted message.
        assert_eq!(
            world.post(&alice(), json!({"body": "Top.", "clientId": "t"}))["state"],
            json!("deleted")
        );
        assert_eq!(
            vetoed(world.call(&alice(), "deleteMessage", json!({"message": 1}))),
            ("deleted".to_owned(), json!({"message": 1}))
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 3, "lastMessageAt": 5000, "threads": {"open": 1, "resolved": 0}, "composing": [], "unread": 1})
        );
        // A moderator deletes another's.
        world.ok(&moderator(), "deleteMessage", json!({"message": 4}));
        assert_eq!(
            world.fields(),
            json!({"messages": 2, "lastMessageAt": 1000, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            refusal(world.call(&bob(), "purge", json!({"message": 1}))).message(),
            "bob may not purge on Review r1: it needs permission comments.moderate"
        );
        assert_eq!(
            code(world.call(&bob(), "purge", json!({"message": 1}))),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "purge", json!({"message": 9}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        assert_eq!(
            world.ok(&moderator(), "purge", json!({"message": 1})),
            json!({"message": 1, "purged": [1, 2, 3]})
        );
        assert_eq!(
            world.events(Some(true)).last().unwrap().change["result"],
            json!({"message": 1, "purged": [1, 2, 3]})
        );
        assert_eq!(
            world.fields(),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0})
        );
        for operation in ["getMessage", "deleteMessage", "purge"] {
            assert_eq!(
                vetoed(world.call(&moderator(), operation, json!({"message": 2}))).0,
                "no_message"
            );
        }
        // Ids are not reused, and a purged message's clientId is free.
        assert_eq!(
            world.post(&alice(), json!({"body": "Again.", "clientId": "t"}))["id"],
            json!(5)
        );
        // Purging a reply leaves another reply's quotes as it was.
        world.reply(&bob(), 5, "Reply.");
        world.post(
            &carol(),
            json!({"body": "Quote.", "thread": 5, "quotes": 6}),
        );
        assert_eq!(
            world.ok(&moderator(), "purge", json!({"message": 6})),
            json!({"message": 6, "purged": [6]})
        );
        assert_eq!(world.message(7)["quotes"], json!(6));
        assert_eq!(world.message(5)["replies"], json!(1));
        assert_eq!(world.fields()["messages"], json!(2));
    }
    for target in targets("comments_purge_unconfigured") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Mine.");
        assert_eq!(
            vetoed(world.call(&moderator(), "purge", json!({"message": 1}))),
            (
                "not_configured".to_owned(),
                json!({"permission": "moderatePermission"})
            )
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "deleteMessage", json!({"message": 1}))),
            (
                "not_author".to_owned(),
                json!({"message": 1, "author": "alice"})
            )
        );
    }
}

#[test]
fn reactions_count_by_emoji_in_the_order_they_were_first_given() {
    for target in targets("comments_reactions") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Ship it?");
        world.set_clock(2000);
        world.ok(&bob(), "react", json!({"message": 1, "emoji": "👍"}));
        world.set_clock(3000);
        world.ok(&carol(), "react", json!({"message": 1, "emoji": ":tada:"}));
        let after = world.ok(&alice(), "react", json!({"message": 1, "emoji": "👍"}));
        assert_eq!(
            after["reactions"],
            json!({"👍": {"count": 2, "mine": true}, ":tada:": {"count": 1, "mine": false}})
        );
        // An event's mine is its actor's.
        assert_eq!(
            world.events(Some(true)).last().unwrap().change["result"]["reactions"]["👍"],
            json!({"count": 2, "mine": true})
        );
        assert_eq!(
            world.message(1)["reactions"][":tada:"],
            json!({"count": 1, "mine": false})
        );
        assert_eq!(
            vetoed(world.call(&bob(), "react", json!({"message": 1, "emoji": "👍"}))),
            (
                "already_reacted".to_owned(),
                json!({"message": 1, "emoji": "👍"})
            )
        );
        assert_eq!(
            vetoed(world.call(&bob(), "unreact", json!({"message": 1, "emoji": ":tada:"}))),
            (
                "not_reacted".to_owned(),
                json!({"message": 1, "emoji": ":tada:"})
            )
        );
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": 1, "emoji": "👍"})
            ),
            json!({"items": [{"subject": "bob", "at": 2000}, {"subject": "alice", "at": 3000}], "next": null})
        );
        let page = world.ok(
            &alice(),
            "listReactions",
            json!({"message": 1, "emoji": "👍", "limit": 1}),
        );
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": 1, "emoji": "👍", "limit": 1, "cursor": page["next"]})
            )["items"],
            json!([{"subject": "alice", "at": 3000}])
        );
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": 1, "emoji": "🎉"})
            ),
            json!({"items": [], "next": null})
        );
        // The order follows each emoji's first reaction still held.
        world.ok(&bob(), "unreact", json!({"message": 1, "emoji": "👍"}));
        world.ok(&alice(), "unreact", json!({"message": 1, "emoji": "👍"}));
        world.ok(&bob(), "react", json!({"message": 1, "emoji": "👍"}));
        assert_eq!(
            world.message(1)["reactions"],
            json!({":tada:": {"count": 1, "mine": false}, "👍": {"count": 1, "mine": false}})
        );
        for n in 0..18 {
            world.ok(
                &alice(),
                "react",
                json!({"message": 1, "emoji": format!("e{n}")}),
            );
        }
        assert_eq!(
            vetoed(world.call(&alice(), "react", json!({"message": 1, "emoji": "e18"}))),
            (
                "too_many_reactions".to_owned(),
                json!({"message": 1, "max": 20})
            )
        );
        world.ok(&carol(), "react", json!({"message": 1, "emoji": "e0"}));
        assert_eq!(
            vetoed(world.call(&alice(), "react", json!({"message": 9, "emoji": "x"}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
        world.ok(&alice(), "deleteMessage", json!({"message": 1}));
        for operation in ["react", "unreact"] {
            assert_eq!(
                vetoed(world.call(&bob(), operation, json!({"message": 1, "emoji": "👍"}))),
                ("deleted".to_owned(), json!({"message": 1}))
            );
        }
        assert_eq!(
            world.ok(
                &alice(),
                "listReactions",
                json!({"message": 1, "emoji": "👍"})
            ),
            json!({"items": [], "next": null})
        );
    }
}

#[test]
fn pins_and_their_permission() {
    for target in targets("comments_pins") {
        let world = world(&target, json!({"pinPermission": "comments.pin"}));
        world.say(&alice(), "Top.");
        world.reply(&bob(), 1, "Reply.");
        assert_eq!(
            refusal(world.call(&alice(), "pin", json!({"message": 2}))).message(),
            "alice may not pin on Review r1: it needs permission comments.pin"
        );
        // The permission is checked before the message.
        assert_eq!(
            code(world.call(&alice(), "unpin", json!({"message": 9}))),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            world.ok(&moderator(), "pin", json!({"message": 2}))["pinned"],
            json!(true)
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "pin", json!({"message": 2}))),
            ("already_pinned".to_owned(), json!({"message": 2}))
        );
        assert_eq!(
            world.ok(&moderator(), "unpin", json!({"message": 2}))["pinned"],
            json!(false)
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "unpin", json!({"message": 2}))),
            ("not_pinned".to_owned(), json!({"message": 2}))
        );
        world.ok(&moderator(), "pin", json!({"message": 1}));
        world.ok(&alice(), "deleteMessage", json!({"message": 1}));
        assert_eq!(
            vetoed(world.call(&moderator(), "pin", json!({"message": 1}))),
            ("deleted".to_owned(), json!({"message": 1}))
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "unpin", json!({"message": 1}))),
            ("not_pinned".to_owned(), json!({"message": 1}))
        );
        assert_eq!(
            vetoed(world.call(&moderator(), "pin", json!({"message": 9}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
    }
    for target in targets("comments_pins_open") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Top.");
        assert_eq!(
            world.ok(&bob(), "pin", json!({"message": 1}))["pinned"],
            json!(true)
        );
    }
}

#[test]
fn resolve_and_reopen_a_thread_and_the_threads_field_counts_them() {
    for target in targets("comments_resolve") {
        let world = world(&target, json!({"resolvePermission": "comments.resolve"}));
        world.say(&alice(), "Fix the header.");
        world.reply(&bob(), 1, "Done.");
        world.say(&bob(), "And the footer.");
        world.set_clock(4000);
        let resolved = world.ok(&alice(), "resolve", json!({"thread": 1}));
        assert_eq!(resolved["resolved"], json!({"by": "alice", "at": 4000}));
        assert_eq!(world.fields()["threads"], json!({"open": 1, "resolved": 1}));
        assert_eq!(
            vetoed(world.call(&alice(), "resolve", json!({"thread": 1}))),
            (
                "already_resolved".to_owned(),
                json!({"thread": 1, "by": "alice", "at": 4000})
            )
        );
        // A reply to a resolved thread leaves it resolved.
        world.reply(&carol(), 1, "Thanks.");
        assert_eq!(
            world.message(1)["resolved"],
            json!({"by": "alice", "at": 4000})
        );
        assert_eq!(
            vetoed(world.call(&alice(), "resolve", json!({"thread": 2}))),
            (
                "not_a_thread".to_owned(),
                json!({"message": 2, "thread": 1})
            )
        );
        assert_eq!(
            vetoed(world.call(&alice(), "resolve", json!({"thread": 3}))),
            (
                "not_author".to_owned(),
                json!({"message": 3, "author": "bob"})
            )
        );
        world.ok(&moderator(), "resolve", json!({"thread": 3}));
        assert_eq!(world.fields()["threads"], json!({"open": 0, "resolved": 2}));
        assert_eq!(
            vetoed(world.call(&carol(), "reopen", json!({"thread": 1}))),
            (
                "not_author".to_owned(),
                json!({"message": 1, "author": "alice"})
            )
        );
        let reopened = world.ok(&alice(), "reopen", json!({"thread": 1}));
        assert!(reopened.get("resolved").is_none());
        assert_eq!(
            vetoed(world.call(&alice(), "reopen", json!({"thread": 1}))),
            ("not_resolved".to_owned(), json!({"thread": 1}))
        );
        // A deleted resolved thread leaves the resolved count.
        world.ok(&bob(), "deleteMessage", json!({"message": 3}));
        assert_eq!(world.fields()["threads"], json!({"open": 1, "resolved": 0}));
        assert_eq!(
            vetoed(world.call(&moderator(), "reopen", json!({"thread": 3}))),
            ("deleted".to_owned(), json!({"message": 3}))
        );
        assert_eq!(
            vetoed(world.call(&alice(), "resolve", json!({"thread": 9}))),
            ("no_message".to_owned(), json!({"message": 9}))
        );
    }
}

#[test]
fn list_messages_pages_by_its_four_anchors() {
    for target in targets("comments_list") {
        let world = world(&target, json!({}));
        // Top-level 1..10, with a thread of replies on 3.
        let mut replies = Vec::new();
        for n in 1..=10 {
            let id = world.say(&alice(), &format!("m{n}"));
            if n == 3 {
                for r in 0..4 {
                    replies.push(world.reply(&bob(), id, &format!("r{r}")));
                }
            }
        }
        let list = |params: Value| world.ok(&alice(), "listMessages", params);
        let tops: Vec<i64> = [1, 2, 3, 8, 9, 10, 11, 12, 13, 14].to_vec();
        assert_eq!(replies, [4, 5, 6, 7]);
        let all = list(json!({}));
        assert_eq!(
            (ids(&all), &all["before"], &all["after"]),
            (tops.clone(), &Value::Null, &Value::Null)
        );
        assert!(all.get("root").is_none());
        let latest = list(json!({"limit": 3}));
        assert_eq!(
            (ids(&latest), &latest["before"], &latest["after"]),
            (vec![12, 13, 14], &json!(12), &Value::Null)
        );
        let older = list(json!({"limit": 3, "before": 12}));
        assert_eq!(
            (ids(&older), &older["before"], &older["after"]),
            (vec![9, 10, 11], &json!(9), &json!(11))
        );
        let oldest = list(json!({"limit": 3, "before": 2}));
        assert_eq!(
            (ids(&oldest), &oldest["before"], &oldest["after"]),
            (vec![1], &Value::Null, &json!(1))
        );
        let newer = list(json!({"limit": 3, "after": 3}));
        assert_eq!(
            (ids(&newer), &newer["before"], &newer["after"]),
            (vec![8, 9, 10], &json!(8), &json!(10))
        );
        // Bounds, not references: 5 is a reply, 99 is nothing.
        assert_eq!(ids(&list(json!({"limit": 2, "after": 5}))), [8, 9]);
        let past = list(json!({"after": 99}));
        assert_eq!(
            (ids(&past), &past["before"], &past["after"]),
            (vec![], &Value::Null, &Value::Null)
        );
        let around = list(json!({"limit": 4, "around": 9}));
        assert_eq!(
            (ids(&around), &around["before"], &around["after"]),
            (vec![8, 9, 10, 11], &json!(8), &json!(11))
        );
        // A side with fewer is not filled from the other.
        assert_eq!(ids(&list(json!({"limit": 5, "around": 1}))), [1, 2, 3]);
        assert_eq!(ids(&list(json!({"limit": 1, "around": 13}))), [13]);
        let thread = list(json!({"thread": 3, "limit": 2}));
        assert_eq!(
            (ids(&thread), &thread["before"], &thread["after"]),
            (vec![6, 7], &json!(6), &Value::Null)
        );
        assert_eq!(thread["root"]["id"], json!(3));
        assert_eq!(thread["root"]["replies"], json!(4));
        assert_eq!(
            ids(&list(json!({"thread": 3, "around": 5, "limit": 3}))),
            [4, 5, 6]
        );
        // A deleted message is listed; a purged one is not.
        world.ok(&alice(), "deleteMessage", json!({"message": 2}));
        assert_eq!(
            list(json!({"limit": 3, "before": 3}))["items"][1]["state"],
            json!("deleted")
        );
        list_refusals(&world);
    }
}

// list_refusals is what listMessages refuses on the conversation
// list_messages_pages_by_its_four_anchors posts: top-level 1, 2, 3 and
// 8..14, and replies 4..7 in thread 3.
fn list_refusals(world: &World) {
    for (params, path, message) in [
        (
            json!({"around": 2, "before": 3}),
            "/before",
            "must not be given with around",
        ),
        (
            json!({"around": 2, "after": 3}),
            "/after",
            "must not be given with around",
        ),
        (
            json!({"before": 2, "after": 3}),
            "/after",
            "must not be given with before",
        ),
        (
            json!({"around": 2, "before": 3, "after": 4}),
            "/before",
            "must not be given with around",
        ),
    ] {
        assert_eq!(
            issues_of(&refusal(world.call(&alice(), "listMessages", params))),
            [issue(path, message)]
        );
    }
    assert_eq!(
        vetoed(world.call(&alice(), "listMessages", json!({"thread": 99}))),
        ("no_message".to_owned(), json!({"message": 99}))
    );
    assert_eq!(
        vetoed(world.call(&alice(), "listMessages", json!({"thread": 4}))),
        (
            "not_a_thread".to_owned(),
            json!({"message": 4, "thread": 3})
        )
    );
    for params in [
        json!({"around": 4}),
        json!({"around": 99}),
        json!({"thread": 3, "around": 8}),
    ] {
        assert_eq!(
            vetoed(world.call(&alice(), "listMessages", params.clone())).0,
            "no_message",
            "{params}"
        );
    }
    assert_eq!(
        refusal(world.call(&alice(), "listMessages", json!({"limit": 101}))).name(),
        "OperationParamsError"
    );
    assert_eq!(
        world.on(&alice(), "r2", "listMessages", json!({})).unwrap(),
        json!({"items": [], "before": null, "after": null})
    );
}

#[test]
fn every_write_is_beside_the_instance_and_appends_an_event_of_no_sequence() {
    for target in targets("comments_beside") {
        let world = world(&target, json!({"moderatePermission": "comments.moderate"}));
        let before = world.get("r1");
        let posted = world.post(&bob(), json!({"body": "Hello."}));
        world.reply(&alice(), 1, "Hi.");
        world.ok(&bob(), "edit", json!({"message": 1, "body": "Hello!"}));
        world.ok(&alice(), "react", json!({"message": 1, "emoji": "👋"}));
        world.ok(&alice(), "unreact", json!({"message": 1, "emoji": "👋"}));
        world.ok(&alice(), "pin", json!({"message": 1}));
        world.ok(&alice(), "unpin", json!({"message": 1}));
        world.ok(&bob(), "resolve", json!({"thread": 1}));
        world.ok(&bob(), "reopen", json!({"thread": 1}));
        world.ok(&alice(), "deleteMessage", json!({"message": 2}));
        world.ok(&moderator(), "purge", json!({"message": 2}));
        // Reads append nothing.
        world.ok(&alice(), "listMessages", json!({}));
        world.ok(&alice(), "getMessage", json!({"message": 1}));
        world.ok(&alice(), "listEdits", json!({"message": 1}));
        world.ok(
            &alice(),
            "listReactions",
            json!({"message": 1, "emoji": "👋"}),
        );
        let after = world.get("r1");
        assert_eq!(
            (
                after.seq,
                after.updated_at,
                after.updated_by.as_str(),
                &after.data
            ),
            (
                before.seq,
                before.updated_at,
                before.updated_by.as_str(),
                &before.data
            )
        );
        let events = world.events(None);
        let operations: Vec<(Option<i64>, bool, Value)> = events
            .iter()
            .skip(1)
            .map(|event| (event.seq, event.beside, event.change["operation"].clone()))
            .collect();
        let expected: Vec<(Option<i64>, bool, Value)> = [
            "post",
            "post",
            "edit",
            "react",
            "unreact",
            "pin",
            "unpin",
            "resolve",
            "reopen",
            "deleteMessage",
            "purge",
        ]
        .into_iter()
        .map(|operation| (None, true, json!(operation)))
        .collect();
        assert_eq!(operations, expected);
        assert_eq!(
            (
                events[1].kind.as_str(),
                events[1].actor.as_str(),
                &events[1].change
            ),
            (
                "operation",
                "bob",
                &json!({"behavior": "Comments", "operation": "post", "params": {"body": "Hello."}, "result": posted})
            )
        );
        // The sequenced events replay to the instance: none carries a
        // move of the volatile fields.
        assert_eq!(world.events(Some(false)).len(), 1);
        world
            .engine
            .instances()
            .update(
                &alice(),
                "Review",
                "r1",
                &json!({"title": "Q3 plan, v2"}),
                WriteOptions::default(),
            )
            .unwrap();
        let update = world.events(Some(false)).pop().unwrap();
        assert_eq!(update.seq, Some(2));
        assert!(
            update.change["patch"].get("behaviors").is_none(),
            "{}",
            update.change
        );
        // If-Match is checked as for a read: the tag read before a post is
        // still good after it.
        world
            .engine
            .instances()
            .invoke(
                &alice(),
                "Review",
                "r1",
                "post",
                &json!({"body": "Tagged."}),
                WriteOptions {
                    expected_seq: Some(2),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(
            code(world.engine.instances().invoke(
                &alice(),
                "Review",
                "r1",
                "post",
                &json!({"body": "Stale."}),
                WriteOptions {
                    expected_seq: Some(1),
                    ..Default::default()
                },
            )),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(world.get("r1").seq, 2);
    }
}

#[test]
fn the_policy_is_asked_for_write_to_post_and_for_read_to_list() {
    for target in targets("comments_policy") {
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice" || request.action() == "read"
        });
        let asked = policy.clone();
        let world = world_with(&target, json!({}), move |options| {
            options.policy = Some(Policy::custom(policy));
        });
        asked.take();
        world.say(&alice(), "Mine.");
        world.ok(&bob(), "listMessages", json!({}));
        world.ok(&bob(), "getMessage", json!({"message": 1}));
        let refused = refusal(world.call(&bob(), "react", json!({"message": 1, "emoji": "x"})));
        assert_eq!(
            refused.message(),
            "bob may not call react (write) on Review in namespace default"
        );
        let questions: Vec<(Value, Value, Value)> = asked
            .take()
            .into_iter()
            .map(|question| {
                (
                    question["principal"].clone(),
                    question["action"].clone(),
                    question["operation"].clone(),
                )
            })
            .collect();
        assert_eq!(
            questions,
            [
                (json!("alice"), json!("write"), json!("post")),
                (json!("bob"), json!("read"), json!("listMessages")),
                (json!("bob"), json!("read"), json!("getMessage")),
                (json!("bob"), json!("write"), json!("react")),
            ]
        );
    }
}

// A re-created instance numbers its messages on from the deleted one's,
// so no id is reused across the lives the instance's events span.
#[test]
fn deleting_the_instance_deletes_its_conversation_and_keeps_its_numbering() {
    for target in targets("comments_delete_instance") {
        let world = world(&target, json!({}));
        world.say(&alice(), "Hello.");
        world.reply(&bob(), 1, "Hi.");
        world.ok(&bob(), "react", json!({"message": 1, "emoji": "x"}));
        world.ok(&alice(), "edit", json!({"message": 1, "body": "Hello!"}));
        assert!(world
            .engine
            .instances()
            .delete(&alice(), "Review", "r1", WriteOptions::default())
            .expect("delete"));
        create(&world.engine, "Q3 plan, again", "r1");
        assert_eq!(
            world.fields(),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            world.ok(&alice(), "listMessages", json!({})),
            json!({"items": [], "before": null, "after": null})
        );
        assert_eq!(
            vetoed(world.call(&alice(), "getMessage", json!({"message": 1}))),
            ("no_message".to_owned(), json!({"message": 1}))
        );
        assert_eq!(world.say(&alice(), "Fresh."), 3);
        assert_eq!(
            world.fields(),
            json!({"messages": 1, "lastMessageAt": 1000, "threads": {"open": 1, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(world.message(3)["reactions"], json!({}));
        assert_eq!(
            world.ok(&alice(), "listEdits", json!({"message": 3}))["items"],
            json!([])
        );
        assert_eq!(ids(&world.ok(&alice(), "listMessages", json!({}))), vec![3]);
        assert_eq!(
            world.on(&alice(), "r2", "listMessages", json!({})).unwrap()["items"],
            json!([])
        );
    }
}

#[test]
fn comments_joins_a_schema_that_has_instances_and_does_not_leave_one() {
    for target in targets("comments_join") {
        let engine = target.open_core();
        assert_eq!(publish(&engine, &review_schema(json!([]))).expect("v1"), 1);
        create(&engine, "Q3 plan", "r1");
        assert_eq!(
            publish(&engine, &review_schema(json!([{"name": "Comments"}]))).expect("v2"),
            2
        );
        let joined = engine
            .instances()
            .get(&alice(), "Review", "r1", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            (Value::Object(joined.data), Value::Object(joined.behaviors)),
            (
                json!({"title": "Q3 plan"}),
                json!({"Comments": {"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0}})
            )
        );
        engine
            .instances()
            .invoke(
                &alice(),
                "Review",
                "r1",
                "post",
                &json!({"body": "Late, but here."}),
                WriteOptions::default(),
            )
            .unwrap();
        // A config change applies to the next call.
        publish(
            &engine,
            &review_schema(comments(json!({"maxBodyLength": 3}))),
        )
        .expect("v3");
        assert_eq!(
            refusal(engine.instances().invoke(
                &alice(),
                "Review",
                "r1",
                "post",
                &json!({"body": "Long"}),
                WriteOptions::default()
            ))
            .name(),
            "OperationParamsError"
        );
        let removed = refusal(engine.schemas().define(
            &alice(),
            review_schema(json!([])),
            DefineOptions::default(),
        ));
        assert_eq!(removed.name(), "IncompatibleChangeError");
        assert!(
            removed.changes()[0].message.contains(
                "the messages its instances have would stay behind with nothing to delete them"
            ),
            "{}",
            removed.changes()[0].message
        );
    }
}

// Comments as version 1 shipped it: migration 1 alone, and comment, which
// adds a comment as a reply to any comment of the instance.
#[derive(Clone, Copy)]
struct CommentsV1;

impl Behavior for CommentsV1 {
    type Config = Value;

    fn declaration(&self) -> &str {
        r#"{"name": "Comments", "fields": [{"name": "commentCount"}], "operations": [
            {"name": "comment", "writes": true, "resultSchema": true, "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["body"],
                "properties": {"body": {"type": "string"}, "replyTo": {"type": "integer"}}}}]}"#
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "comments")
                .column("count", ColumnSpec::integer().not_null().default(0))
                .table(
                    TableSpec::new("comments")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("comment", ColumnSpec::integer().not_null())
                        .column("reply_to", ColumnSpec::integer())
                        .column("body", ColumnSpec::text().not_null())
                        .column("created_by", ColumnSpec::text().not_null())
                        .column("created_at", ColumnSpec::integer().not_null())
                        .primary_key(&["namespace", "schema", "id", "comment"]),
                )],
            operations: vec![("comment", |_, cx, params| {
                let table = cx.sql().table("comments")?;
                let key = [
                    SqlValue::from(cx.namespace()),
                    SqlValue::from(cx.schema()),
                    SqlValue::from(cx.id()),
                ];
                let count = cx.columns().get()?.i64("count")?;
                let mut values = key.to_vec();
                values.extend([
                    SqlValue::Integer(count + 1),
                    params
                        .get("replyTo")
                        .and_then(Value::as_i64)
                        .map_or(SqlValue::Null, SqlValue::Integer),
                    SqlValue::from(params["body"].as_str().unwrap_or_default()),
                    SqlValue::from(cx.principal().subject.as_str()),
                    SqlValue::Integer(cx.now()),
                ]);
                cx.sql().run(
                    format!("INSERT INTO {table} (namespace, schema, id, comment, reply_to, body, created_by, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)"),
                    &values,
                )?;
                cx.columns()
                    .set(&[("count", SqlValue::Integer(count + 1))])?;
                Ok(json!({"id": count + 1}))
            })],
            fields: vec![("commentCount", |_, view| {
                Ok(Some(Value::from(view.columns().get()?.i64("count")?)))
            })],
            ..Hooks::default()
        }
    }
}

#[test]
fn migration_2_moves_version_1s_comments_into_threads_one_level_deep() {
    for target in targets("comments_migrate") {
        let mut v1 = target.options(BehaviorSet::new().with(CommentsV1));
        v1.clock = Clock::Manual(1000);
        let engine = Engine::open(v1).expect("open on version 1");
        publish(&engine, &review_schema(json!([{"name": "Comments"}]))).expect("publish");
        for id in ["r1", "r2", "r3"] {
            create(&engine, id, id);
        }
        let comment = |who: &Principal, id: &str, at: i64, params: Value| {
            engine.clock().set(at).unwrap();
            engine
                .instances()
                .invoke(
                    who,
                    "Review",
                    id,
                    "comment",
                    &params,
                    WriteOptions::default(),
                )
                .expect("comment");
        };
        // r1: 1 <- 2 <- 3 <- 4, 1 <- 5, 6, 6 <- 7, and 8 naming no comment.
        comment(&alice(), "r1", 1001, json!({"body": "One."}));
        comment(&bob(), "r1", 1002, json!({"body": "Two.", "replyTo": 1}));
        comment(
            &carol(),
            "r1",
            1003,
            json!({"body": "Three.", "replyTo": 2}),
        );
        comment(&alice(), "r1", 1004, json!({"body": "Four.", "replyTo": 3}));
        comment(&bob(), "r1", 1005, json!({"body": "Five.", "replyTo": 1}));
        comment(&carol(), "r1", 1006, json!({"body": "Six."}));
        comment(
            &alice(),
            "r1",
            1007,
            json!({"body": "Seven.", "replyTo": 6}),
        );
        comment(&bob(), "r1", 1008, json!({"body": "Eight.", "replyTo": 99}));
        comment(&alice(), "r2", 1009, json!({"body": "Only."}));
        drop(engine);

        let engine = target.open_core_with(|options| options.clock = Clock::Manual(5000));
        let r1 = |operation: &str, params: Value| {
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Review",
                    "r1",
                    operation,
                    &params,
                    WriteOptions::default(),
                )
                .unwrap_or_else(|error| panic!("{operation}: {error}"))
        };
        let top = r1("listMessages", json!({}));
        assert_eq!(ids(&top), [1, 6, 8]);
        assert_eq!(
            top["items"][0],
            json!({"id": 1, "author": "alice", "state": "posted", "body": "One.", "blocks": [], "mentions": [], "reactions": {},
                   "createdAt": 1001, "postedAt": 1001, "stand": 1, "edits": 0, "pinned": false, "replies": 4, "lastReplyAt": 1005, "participants": ["bob", "alice", "carol"]})
        );
        let thread = r1("listMessages", json!({"thread": 1}));
        let shape: Vec<(Value, Value)> = thread["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| {
                (
                    item["id"].clone(),
                    item.get("quotes").cloned().unwrap_or(Value::Null),
                )
            })
            .collect();
        assert_eq!(
            shape,
            [
                (json!(2), Value::Null),
                (json!(3), json!(2)),
                (json!(4), json!(3)),
                (json!(5), Value::Null)
            ]
        );
        assert_eq!(r1("getMessage", json!({"message": 7}))["thread"], json!(6));
        assert!(r1("getMessage", json!({"message": 8}))
            .get("thread")
            .is_none());
        let fields = |id: &str| {
            engine
                .instances()
                .get(&alice(), "Review", id, Default::default())
                .unwrap()
                .unwrap()
                .behaviors["Comments"]
                .clone()
        };
        assert_eq!(
            fields("r1"),
            json!({"messages": 8, "lastMessageAt": 1008, "threads": {"open": 3, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            fields("r2"),
            json!({"messages": 1, "lastMessageAt": 1009, "threads": {"open": 1, "resolved": 0}, "composing": [], "unread": 0})
        );
        assert_eq!(
            fields("r3"),
            json!({"messages": 0, "threads": {"open": 0, "resolved": 0}, "composing": [], "unread": 0})
        );
        // The conversation goes on from the greatest id.
        assert_eq!(r1("post", json!({"body": "Nine."}))["id"], json!(9));
        assert_eq!(
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Review",
                    "r3",
                    "post",
                    &json!({"body": "First."}),
                    WriteOptions::default()
                )
                .unwrap()["id"],
            json!(1)
        );
        let tables = tables_of(&engine, "bhv_comments__");
        assert!(
            !tables.contains(&"bhv_comments__comments".to_owned()),
            "{tables:?}"
        );
        assert!(
            tables.contains(&"bhv_comments__comment_messages".to_owned()),
            "{tables:?}"
        );
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
    }
}
