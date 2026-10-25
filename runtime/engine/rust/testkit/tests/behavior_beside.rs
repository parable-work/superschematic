//! An operation beside the instance (D77): it writes only its behavior's
//! own tables and columns, so the instance keeps its seq, its ETag, its
//! updatedAt and updatedBy; it asks its own behavior's guard and no other,
//! so an active Lease does not veto it; a logged one appends an operation
//! event of no sequence whose change carries its result, which replay
//! skips and the `beside` filter drops, and an unlogged one appends none.
//! Its handler reaching past its own storage refuses the call, which rolls
//! it back. On SQLite and, with the variable set, on Postgres.

mod support;

use std::sync::{Arc, Mutex};

use serde_json::{json, Value};

use superschematic_engine::behaviors::{
    BehaviorMigration, ColumnSpec, Reactions, SqlReader, SqlWriter, TableSpec,
};
use superschematic_engine::errors::Veto;
use superschematic_engine::storage::SqlValue;
use superschematic_engine::{
    Behavior, BehaviorSet, Clock, ConfigTarget, CreateOptions, Engine, EngineErrorCode,
    EngineEvent, EngineOptions, GuardRequest, Hooks, InstanceRecord, MetaSchema, Principal,
    ReadEvents, Result, RetentionOptions, RunnerOptions, ValueOptions, WriteOptions,
};
use superschematic_engine_testkit::open_meta_schema;
use support::{alice, code, defect, named, publish, targets, veto, Target};

type Seen = Arc<Mutex<Vec<Value>>>;

const NONE: &str = r#"{"type": "object", "additionalProperties": false}"#;
const BODY: &str = r#"{"type": "object", "additionalProperties": false, "required": ["body"], "properties": {"body": {"type": "string"}}}"#;

fn chat_declaration(fields: Value, extra: &[Value]) -> String {
    let none: Value = serde_json::from_str(NONE).unwrap();
    let body: Value = serde_json::from_str(BODY).unwrap();
    let mut operations = vec![
        json!({"name": "post", "paramsSchema": body, "resultSchema": {"type": "object"}, "writes": true, "beside": true}),
        json!({"name": "markRead", "paramsSchema": {"type": "object", "additionalProperties": false, "required": ["upTo"], "properties": {"upTo": {"type": "integer"}}}, "resultSchema": true, "writes": true, "beside": true, "log": false}),
        json!({"name": "quiet", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "retitle", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "sneak", "paramsSchema": body, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "poke", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "echo", "paramsSchema": body, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "spawn", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "dodge", "paramsSchema": none, "resultSchema": true, "writes": true, "beside": true}),
        json!({"name": "bump", "paramsSchema": none, "resultSchema": true, "writes": true}),
        json!({"name": "peek", "paramsSchema": none, "resultSchema": true}),
    ];
    operations.extend(extra.iter().cloned());
    json!({
        "name": "test.Chat",
        "fields": fields,
        "operations": operations,
        "vetoes": [{"code": "muted"}]
    })
    .to_string()
}

// test.Chat holds messages in a table of its own and a read marker in a
// column of its own, both beside the instance; its guard mutes a post of
// "muted", and its reactions record the events they hear.
struct Chat {
    heard: Seen,
}

impl Behavior for Chat {
    type Config = Value;

    fn declaration(&self) -> &str {
        static DECLARATION: std::sync::LazyLock<String> = std::sync::LazyLock::new(|| {
            chat_declaration(
                json!([{"name": "messages", "volatile": true}, {"name": "readUpTo", "volatile": true}]),
                &[],
            )
        });
        &DECLARATION
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks {
            migrations: vec![BehaviorMigration::new(1, "messages")
                .column("read_up_to", ColumnSpec::integer().not_null().default(0))
                .table(
                    TableSpec::new("message")
                        .column("namespace", ColumnSpec::text().not_null())
                        .column("schema", ColumnSpec::text().not_null())
                        .column("id", ColumnSpec::text().not_null())
                        .column("n", ColumnSpec::integer().not_null())
                        .column("body", ColumnSpec::text().not_null())
                        .primary_key(&["namespace", "schema", "id", "n"]),
                )],
            guard: Some(|_, _, request| {
                if let GuardRequest::Operation {
                    operation: "post",
                    params,
                    ..
                } = request
                {
                    if params["body"] == "muted" {
                        return Ok(Some(Veto {
                            reason: "the conversation is muted".to_owned(),
                            code: Some("muted".to_owned()),
                            details: None,
                        }));
                    }
                }
                Ok(None)
            }),
            operations: vec![
                ("post", |_, cx, params| {
                    let body = params["body"].as_str().unwrap_or_default();
                    post(&cx.sql(), cx.namespace(), cx.schema(), cx.id(), body)
                }),
                ("markRead", |_, cx, params| {
                    cx.columns().set(&[(
                        "read_up_to",
                        SqlValue::Integer(params["upTo"].as_i64().unwrap_or_default()),
                    )])?;
                    Ok(Value::Null)
                }),
                ("quiet", |_, cx, _| {
                    cx.unchanged();
                    Ok(json!("nothing"))
                }),
                ("retitle", |_, cx, _| {
                    cx.update(&json!({"title": "Retitled"}))?;
                    Ok(Value::Null)
                }),
                ("sneak", |_, cx, params| {
                    // The post is rolled back with the call, which the
                    // refused update() refuses though the handler goes on.
                    let body = params["body"].as_str().unwrap_or_default();
                    post(&cx.sql(), cx.namespace(), cx.schema(), cx.id(), body)?;
                    let _ = cx.update(&json!({"title": "Sneaked"}));
                    Ok(Value::Null)
                }),
                ("poke", |_, cx, _| cx.call("test.Chat", "bump", &json!({}))),
                ("echo", |_, cx, params| {
                    cx.call("test.Chat", "post", &json!({"body": params["body"]}))?;
                    cx.call("test.Chat", "peek", &json!({}))
                }),
                ("spawn", |_, cx, _| {
                    cx.instances()
                        .create("Item", &json!({"title": "Spawned"}), None, None)?;
                    Ok(Value::Null)
                }),
                ("dodge", |_, cx, _| {
                    // The refusal stays this handler's: the read-only call
                    // after it neither takes it nor fails for it.
                    let _ = cx.update(&json!({"title": "Dodged"}));
                    let peeked = cx.call("test.Chat", "peek", &json!({}));
                    Ok(json!(peeked.is_ok()))
                }),
                ("bump", |_, cx, _| {
                    cx.update(&json!({"title": "Bumped"}))?;
                    Ok(Value::Null)
                }),
                ("peek", |_, cx, _| {
                    Ok(json!(count(
                        &cx.sql(),
                        cx.namespace(),
                        cx.schema(),
                        cx.id()
                    )?))
                }),
            ],
            fields: vec![
                ("messages", |_, view| {
                    Ok(Some(json!(count(
                        &view.sql(),
                        view.namespace(),
                        view.schema(),
                        view.id()
                    )?)))
                }),
                ("readUpTo", |_, view| {
                    Ok(Some(json!(view.columns().get()?.i64("read_up_to")?)))
                }),
            ],
            reactions: Some(Reactions {
                watches: None,
                react: |chat, cx, event| {
                    let before = cx.before(event)?;
                    chat.heard.lock().unwrap().push(json!({
                        "kind": event.kind,
                        "seq": event.seq,
                        "beside": event.beside,
                        "operation": event.change.get("operation"),
                        "before": before.map(|was| was.data["title"].clone()),
                    }));
                    Ok(())
                },
            }),
            ..Hooks::default()
        }
    }
}

fn post(sql: &SqlWriter<'_>, namespace: &str, schema: &str, id: &str, body: &str) -> Result<Value> {
    let n = count(sql, namespace, schema, id)? + 1;
    let table = sql.table("message")?;
    sql.run(
        format!("INSERT INTO {table} (namespace, schema, id, n, body) VALUES (?, ?, ?, ?, ?)"),
        &[
            namespace.into(),
            schema.into(),
            id.into(),
            SqlValue::Integer(n),
            body.into(),
        ],
    )?;
    Ok(json!({"message": n, "body": body}))
}

fn count(sql: &SqlReader<'_>, namespace: &str, schema: &str, id: &str) -> Result<i64> {
    let table = sql.table("message")?;
    let row = sql.get(
        format!(
            "SELECT COUNT(*) AS count FROM {table} WHERE namespace = ? AND schema = ? AND id = ?"
        ),
        &[namespace.into(), schema.into(), id.into()],
    )?;
    Ok(row.map_or(Ok(0), |row| row.i64("count"))?)
}

fn chat(heard: &Seen) -> Chat {
    Chat {
        heard: Arc::clone(heard),
    }
}

fn runner() -> RunnerOptions {
    RunnerOptions::new(Principal::new("runner", Vec::<String>::new()))
}

/// An engine whose Item composes test.Chat (and the behaviors given before
/// it), with the runner, a 1 KiB value threshold and a manual clock, and
/// item a1 created by alice.
struct World {
    engine: Engine,
    heard: Seen,
}

type Configure = Box<dyn FnOnce(&mut EngineOptions)>;

fn world_with(
    target: &Target,
    behaviors: Value,
    more: Configure,
    open: impl FnOnce(&Target, Chat, Configure) -> Engine,
) -> World {
    let heard: Seen = Seen::default();
    let engine = open(
        target,
        chat(&heard),
        Box::new(move |options| {
            options.clock = Clock::Manual(1_000_000);
            options.runner = Some(runner());
            options.values = ValueOptions {
                threshold_bytes: Some(1024),
                ..ValueOptions::default()
            };
            more(options);
        }),
    );
    publish(&engine, &support::item_document(behaviors, json!([])), None);
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
    World { engine, heard }
}

fn opened(target: &Target, more: Configure) -> World {
    world_with(
        target,
        json!([{"name": "test.Chat"}]),
        more,
        |target, chat, configure| target.open_with(BehaviorSet::new().with(chat), configure),
    )
}

fn world(target: &Target) -> World {
    opened(target, Box::new(|_| {}))
}

impl World {
    fn invoke_as(
        &self,
        who: &Principal,
        operation: &str,
        params: Value,
        options: WriteOptions<'_>,
    ) -> Result<Value> {
        self.engine
            .instances()
            .invoke(who, "Item", "a1", operation, &params, options)
    }

    fn invoke(&self, operation: &str, params: Value) -> Result<Value> {
        self.invoke_as(&alice(), operation, params, WriteOptions::default())
    }

    fn get(&self) -> InstanceRecord {
        self.engine
            .instances()
            .get(&alice(), "Item", "a1", Default::default())
            .unwrap()
            .unwrap()
    }

    fn events(&self, beside: Option<bool>) -> Vec<EngineEvent> {
        self.engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Item"),
                    instance_id: Some("a1"),
                    beside,
                    ..ReadEvents::default()
                },
            )
            .unwrap()
            .events
    }
}

#[test]
fn a_beside_operation_keeps_the_instances_seq_and_appends_an_event_of_no_sequence() {
    for target in targets("a_beside_operation_keeps_the_seq") {
        let world = world(&target);
        let before = world.get();
        let bob = named("bob");
        let outcome = world
            .engine
            .instances()
            .operate(
                &bob,
                "Item",
                "a1",
                "post",
                &json!({"body": "hello"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(outcome.result, json!({"message": 1, "body": "hello"}));
        assert_eq!(outcome.seq, 1, "the instance's sequence, which it read");
        let after = world.get();
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
        assert_eq!(
            after.behaviors["test.Chat"],
            json!({"messages": 1, "readUpTo": 0})
        );
        let events = world.events(None);
        assert_eq!(events.len(), 2);
        let event = &events[1];
        assert_eq!(
            (
                event.kind.as_str(),
                event.seq,
                event.beside,
                event.actor.as_str()
            ),
            ("operation", None, true, "bob")
        );
        assert_eq!(
            event.change,
            json!({"behavior": "test.Chat", "operation": "post", "params": {"body": "hello"}, "result": {"message": 1, "body": "hello"}})
        );
        let mut serialized = serde_json::to_value(event).unwrap();
        serialized.as_object_mut().unwrap().remove("at");
        assert_eq!(
            serialized,
            json!({"cursor": event.cursor, "kind": "operation", "namespace": "default", "schema": "Item", "instanceId": "a1", "seq": null, "beside": true, "version": 1, "actor": "bob",
                   "change": {"behavior": "test.Chat", "operation": "post", "params": {"body": "hello"}, "result": {"message": 1, "body": "hello"}}})
        );
        // Every other event says nothing of beside.
        assert!(serde_json::to_value(&events[0])
            .unwrap()
            .get("beside")
            .is_none());
        // The next sequenced write takes the next sequence.
        world.invoke("bump", json!({})).unwrap();
        assert_eq!(world.get().seq, 2);
        let seqs: Vec<Option<i64>> = world.events(None).iter().map(|event| event.seq).collect();
        assert_eq!(seqs, [Some(1), None, Some(2)], "in log order");
    }
}

#[test]
fn the_beside_filter_drops_beside_events_or_keeps_only_them() {
    for target in targets("the_beside_filter") {
        let world = world(&target);
        world.invoke("post", json!({"body": "one"})).unwrap();
        world.invoke("bump", json!({})).unwrap();
        world.invoke("post", json!({"body": "two"})).unwrap();
        let kinds = |beside: Option<bool>| -> Vec<(String, Option<i64>)> {
            world
                .events(beside)
                .into_iter()
                .map(|event| (event.kind, event.seq))
                .collect()
        };
        assert_eq!(
            kinds(Some(false)),
            [
                ("create".to_owned(), Some(1)),
                ("operation".to_owned(), Some(2))
            ]
        );
        assert_eq!(
            kinds(Some(true)),
            [
                ("operation".to_owned(), None),
                ("operation".to_owned(), None)
            ]
        );
        assert_eq!(kinds(None).len(), 4);
        // The filter reads the namespace's log too, and combines with the
        // others.
        let page = world
            .engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    kinds: Some(vec!["operation".to_owned()]),
                    beside: Some(false),
                    ..ReadEvents::default()
                },
            )
            .unwrap();
        assert_eq!(
            page.events
                .iter()
                .map(|event| event.change["operation"].clone())
                .collect::<Vec<_>>(),
            [json!("bump")]
        );
        // Replaying the sequenced events gives the instance.
        let mut instance = json!({});
        for event in world.events(Some(false)) {
            instance = if event.kind == "create" {
                event.change
            } else {
                superschematic_engine::instances::patch::merge_patch(
                    &instance,
                    &event.change["patch"],
                )
            };
        }
        assert_eq!(instance["data"], json!({"title": "Bumped"}));
    }
}

#[test]
fn an_unlogged_beside_operation_appends_no_event_and_one_that_changed_nothing_none() {
    for target in targets("an_unlogged_beside_operation") {
        let world = world(&target);
        let head = world.engine.events().head().unwrap();
        assert_eq!(
            world.invoke("markRead", json!({"upTo": 4})).unwrap(),
            Value::Null
        );
        assert_eq!(world.invoke("quiet", json!({})).unwrap(), json!("nothing"));
        assert_eq!(world.engine.events().head().unwrap(), head);
        let after = world.get();
        assert_eq!(after.seq, 1);
        assert_eq!(after.behaviors["test.Chat"]["readUpTo"], json!(4));
    }
}

#[test]
fn if_match_is_checked_as_for_a_read_only_operation() {
    for target in targets("beside_if_match") {
        let world = world(&target);
        let expect = |seq: i64| WriteOptions {
            expected_seq: Some(seq),
            ..WriteOptions::default()
        };
        world
            .invoke_as(&alice(), "post", json!({"body": "one"}), expect(1))
            .unwrap();
        // The tag read before the post is still good.
        world
            .invoke_as(&alice(), "post", json!({"body": "two"}), expect(1))
            .unwrap();
        assert_eq!(
            code(world.invoke_as(&alice(), "post", json!({"body": "three"}), expect(2))),
            EngineErrorCode::SeqMismatch
        );
        assert_eq!(world.get().behaviors["test.Chat"]["messages"], json!(2));
    }
}

#[test]
fn a_beside_operation_asks_its_own_guard_and_no_lease_vetoes_it() {
    for target in targets("a_beside_operation_and_a_lease") {
        let world = world_with(
            &target,
            json!([{"name": "Lease"}, {"name": "test.Chat"}]),
            Box::new(|_| {}),
            |target, chat, configure| {
                target.open_workqueue_with(|options| {
                    options.behaviors = std::mem::take(&mut options.behaviors).with(chat);
                    options.meta_schema = Some(MetaSchema::Document(
                        open_meta_schema(&support::repository()).expect("the open meta-schema"),
                    ));
                    configure(options);
                })
            },
        );
        let wren = named("wren");
        world
            .invoke_as(&wren, "acquire", json!({}), WriteOptions::default())
            .unwrap();
        // Another principal's writing operation is the holder's alone...
        let (behavior, _, _, code, _) = veto(world.invoke("bump", json!({})));
        assert_eq!(
            (behavior.as_str(), code.as_deref()),
            ("Lease", Some("held_by_another"))
        );
        // ...but a post beside the instance is anyone's, and a read marker.
        world
            .invoke("post", json!({"body": "how is it going?"}))
            .unwrap();
        world.invoke("markRead", json!({"upTo": 1})).unwrap();
        let seq = world.get().seq;
        assert_eq!(seq, 2, "the acquire's, and no post's");
        // Its own behavior's guard still runs.
        let (behavior, action, _, code, _) = veto(world.invoke("post", json!({"body": "muted"})));
        assert_eq!(
            (behavior.as_str(), action.as_str(), code.as_deref()),
            ("test.Chat", "post", Some("muted"))
        );
        // A precondition for another behavior, which no guard would read,
        // is refused, whatever its entry says, and the post goes nowhere.
        let head = world.engine.events().head().unwrap();
        for token in [99, 1] {
            let preconditions = json!({"Lease": {"token": token}});
            let refused = world.invoke_as(
                &alice(),
                "post",
                json!({"body": "later"}),
                WriteOptions {
                    preconditions: Some(&preconditions),
                    ..WriteOptions::default()
                },
            );
            let error = support::refusal(refused);
            assert_eq!(error.code(), EngineErrorCode::InvalidArgument, "{error}");
            let issues = support::issues_of(&error);
            assert_eq!(issues.len(), 1, "{issues:?}");
            assert_eq!(issues[0].0, "/Lease");
            assert!(
                issues[0]
                    .1
                    .contains("takes no precondition for behavior Lease"),
                "{issues:?}"
            );
        }
        assert_eq!(world.get().behaviors["test.Chat"]["messages"], json!(1));
        assert_eq!(world.engine.events().head().unwrap(), head);
    }
}

#[test]
fn a_handler_that_reaches_past_its_own_storage_is_refused_and_rolled_back() {
    for target in targets("a_beside_handler_refused") {
        let world = world(&target);
        let head = world.engine.events().head().unwrap();
        let (behavior, message) = defect(world.invoke("retitle", json!({})));
        assert_eq!(behavior, "test.Chat");
        assert!(
            message.contains("operation retitle is beside the instance: it cannot update the instance (update())"),
            "{message}"
        );
        // A handler that goes on past the refusal is refused all the same,
        // and what it wrote goes with the call.
        let (_, message) = defect(world.invoke("sneak", json!({"body": "hidden"})));
        assert!(
            message
                .contains("operation sneak is beside the instance: it cannot update the instance"),
            "{message}"
        );
        // A nested call does not take the refusal of the handler that
        // makes it, so the read-only call succeeds and the call that
        // caught its refused update() is refused all the same, as its own.
        let (behavior, message) = defect(world.invoke("dodge", json!({})));
        assert_eq!(behavior, "test.Chat");
        assert!(
            message
                .contains("operation dodge is beside the instance: it cannot update the instance"),
            "{message}"
        );
        let (_, message) = defect(world.invoke("poke", json!({})));
        assert!(
            message.contains(
                "it cannot call test.Chat.bump, which writes and is not beside the instance"
            ),
            "{message}"
        );
        let (_, message) = defect(world.invoke("spawn", json!({})));
        assert!(
            message.contains("it cannot create an instance of Item"),
            "{message}"
        );
        let after = world.get();
        assert_eq!(
            (after.seq, &after.data, &after.behaviors["test.Chat"]),
            (
                1,
                &json!({"title": "Desk"}).as_object().unwrap().clone(),
                &json!({"messages": 0, "readUpTo": 0})
            )
        );
        assert_eq!(world.engine.events().head().unwrap(), head);
        assert_eq!(
            world
                .engine
                .instances()
                .list(&alice(), "Item", Default::default())
                .unwrap()
                .items
                .len(),
            1,
            "no instance was created"
        );
        // A beside operation may call another beside operation, and a
        // read-only one; the call appends one event.
        assert_eq!(
            world.invoke("echo", json!({"body": "again"})).unwrap(),
            json!(1)
        );
        let events = world.events(Some(true));
        assert_eq!(events.len(), 1);
        assert_eq!(
            events[0].change,
            json!({"behavior": "test.Chat", "operation": "echo", "params": {"body": "again"}, "result": 1})
        );
    }
}

#[test]
fn a_large_member_of_a_beside_event_is_a_ref() {
    for target in targets("a_large_member_of_a_beside_event") {
        let world = world(&target);
        let body = "x".repeat(2048);
        world.invoke("post", json!({"body": body})).unwrap();
        let event = world.events(Some(true)).remove(0);
        assert_eq!(
            event.value_refs,
            Some(vec!["/params/body".to_owned(), "/result".to_owned()])
        );
        assert!(event.change["params"]["body"]["$value"].is_string());
        assert!(event.change["result"]["$value"].is_string());
    }
}

#[test]
fn a_behaviors_own_react_hears_beside_events_and_retention_folds_past_them() {
    for target in targets("beside_react_and_retention") {
        let world = world(&target);
        world.invoke("post", json!({"body": "one"})).unwrap();
        world.invoke("bump", json!({})).unwrap();
        world.invoke("post", json!({"body": "two"})).unwrap();
        world.engine.runner().run_due().unwrap();
        assert_eq!(
            *world.heard.lock().unwrap(),
            [
                json!({"kind": "create", "seq": 1, "beside": false, "operation": null, "before": null}),
                json!({"kind": "operation", "seq": null, "beside": true, "operation": "post", "before": "Desk"}),
                json!({"kind": "operation", "seq": 2, "beside": false, "operation": "bump", "before": "Desk"}),
                json!({"kind": "operation", "seq": null, "beside": true, "operation": "post", "before": "Bumped"}),
            ]
        );
    }
}

// test.Muddled declares a beside operation beside a field that is not
// volatile, or another the rule refuses.
struct Muddled(&'static str);

impl Behavior for Muddled {
    type Config = Value;

    fn declaration(&self) -> &str {
        self.0
    }

    fn parse_config(&self, raw: &Value, _: &ConfigTarget<'_>) -> Result<Value> {
        Ok(raw.clone())
    }

    fn hooks(&self) -> Hooks<Self> {
        Hooks::default()
    }
}

#[test]
fn a_declaration_the_beside_rule_refuses_does_not_register() {
    for target in targets("beside_registration") {
        for (declaration, refused) in [
            (
                r#"{"name": "test.Muddled", "fields": [{"name": "count"}], "operations": [{"name": "post", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "beside": true}]}"#,
                "behavior test.Muddled cannot register: its declaration: field count is not volatile, and operation post is beside the instance: a behavior with a beside operation declares every field volatile, since no event records its moves",
            ),
            (
                r#"{"name": "test.Muddled", "operations": [{"name": "post", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "beside": true}]}"#,
                "behavior test.Muddled cannot register: its declaration: operation post is beside the instance and does not write: a beside operation declares \"writes\": true",
            ),
            (
                r#"{"name": "test.Muddled", "operations": [{"name": "post", "paramsSchema": {"type": "object", "additionalProperties": false}, "resultSchema": true, "writes": true, "log": false}]}"#,
                "behavior test.Muddled cannot register: its declaration: operation post sets \"log\": false and is not beside the instance: only a beside operation appends no event",
            ),
        ] {
            let opened =
                Engine::open(target.options(BehaviorSet::new().with(Muddled(declaration))));
            match opened {
                Err(superschematic_engine::Error::Usage(message)) => {
                    assert_eq!(message, refused)
                }
                other => panic!("expected a refusal, got {:?}", other.err()),
            }
        }
    }
}

#[test]
fn retention_folds_an_instances_events_past_its_beside_ones() {
    for target in targets("beside_retention") {
        let world = opened(
            &target,
            Box::new(|options| {
                options.retention = Some(RetentionOptions {
                    max_events: Some(1),
                    ..RetentionOptions::default()
                });
            }),
        );
        world.invoke("bump", json!({})).unwrap();
        world.invoke("post", json!({"body": "one"})).unwrap();
        world.invoke("post", json!({"body": "two"})).unwrap();
        // The reactions hear every event, and retention prunes all but the
        // last: the define, the publish, the create, the bump and the
        // first post.
        world.engine.runner().run_due().unwrap();
        world.engine.runner().prune().unwrap();
        assert_eq!(world.engine.events().floor(None).unwrap(), 5);
        // The base is the instance after the bump, at its sequence: the
        // next write takes the one after it, and a read from the floor
        // holds the last post.
        let kept = world.events(None);
        assert_eq!(
            kept.iter().map(|event| event.seq).collect::<Vec<_>>(),
            [None]
        );
        world.invoke("bump", json!({})).unwrap();
        assert_eq!(world.get().seq, 3);
        assert_eq!(world.get().behaviors["test.Chat"]["messages"], json!(2));
    }
}
