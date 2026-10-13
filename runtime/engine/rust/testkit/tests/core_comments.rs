//! Comments, the core's comments on an instance: the cases of
//! `test/core-comments.test.ts` (comment and its replies, listComments a
//! page at a time through the read path, commentCount, the access policy's
//! questions, events, delete, and its rule for a new version), on SQLite
//! and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, DefineOptions, Engine, Error, ListOptions, Policy, Principal, ReadEvents, SchemaTarget,
    WriteOptions,
};
use support::{
    alice, issue, issues_of, named, refusal, schema_document, targets, RecordingPolicy, Target,
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

fn bob() -> Principal {
    named("bob")
}

fn publish(engine: &Engine, document: &Value) -> Result<i64, Error> {
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
            superschematic_engine::CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

// published publishes Review with Comments and creates r1 and r2.
fn published(
    target: &Target,
    configure: impl FnOnce(&mut superschematic_engine::EngineOptions),
) -> Engine {
    let engine = target.open_core_with(configure);
    publish(&engine, &review_schema(json!([{"name": "Comments"}]))).expect("publish");
    create(&engine, "Q3 plan", "r1");
    create(&engine, "Q4 plan", "r2");
    engine
}

fn invoke(
    engine: &Engine,
    principal: &Principal,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        "Review",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn comment(engine: &Engine, id: &str, body: &str) -> Value {
    invoke(engine, &alice(), id, "comment", json!({ "body": body })).expect("comment")
}

fn get(engine: &Engine, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Review", id, Default::default())
        .expect("get")
        .expect("the instance")
}

#[test]
fn comment_adds_a_comment_or_a_reply_numbered_per_instance_and_comment_count_counts_them() {
    for target in targets("comments_count") {
        let engine = published(&target, |options| options.clock = Clock::Manual(1000));
        assert_eq!(
            get(&engine, "r1").behaviors["Comments"]["commentCount"],
            json!(0)
        );
        assert_eq!(
            comment(&engine, "r1", "Looks right."),
            json!({"id": 1, "body": "Looks right.", "createdBy": "alice", "createdAt": 1000})
        );
        engine.clock().set(2000).expect("set the clock");
        assert_eq!(
            invoke(
                &engine,
                &bob(),
                "r1",
                "comment",
                json!({"body": "Agreed.", "replyTo": 1})
            )
            .expect("reply"),
            json!({"id": 2, "replyTo": 1, "body": "Agreed.", "createdBy": "bob", "createdAt": 2000})
        );
        assert_eq!(comment(&engine, "r2", "First on r2.")["id"], json!(1));
        let r1 = get(&engine, "r1");
        assert_eq!(
            (Value::Object(r1.data), Value::Object(r1.behaviors)),
            (
                json!({"title": "Q3 plan"}),
                json!({"Comments": {"commentCount": 2}})
            )
        );
        assert_eq!(
            Value::Object(get(&engine, "r2").behaviors),
            json!({"Comments": {"commentCount": 1}})
        );
        let counts: Vec<Value> = engine
            .instances()
            .list(&alice(), "Review", ListOptions::default())
            .expect("list")
            .items
            .into_iter()
            .map(|item| item.behaviors["Comments"]["commentCount"].clone())
            .collect();
        assert_eq!(counts, [json!(2), json!(1)]);
    }
}

#[test]
fn a_reply_names_a_comment_of_the_same_instance_and_a_body_has_text() {
    for target in targets("comments_reply") {
        let engine = published(&target, |_| {});
        comment(&engine, "r1", "One.");
        comment(&engine, "r1", "Two.");
        let missing = refusal(invoke(
            &engine,
            &alice(),
            "r2",
            "comment",
            json!({"body": "Re.", "replyTo": 2}),
        ));
        assert_eq!(missing.name(), "OperationParamsError");
        assert_eq!(
            issues_of(&missing),
            [issue("/replyTo", "Review r2 has no comment 2")]
        );
        for params in [
            json!({"body": ""}),
            json!({"body": "  \n "}),
            json!({}),
            json!({"body": "x", "parentId": 1}),
            json!({"body": "x", "replyTo": 0}),
        ] {
            assert_eq!(
                refusal(invoke(&engine, &alice(), "r1", "comment", params)).name(),
                "OperationParamsError"
            );
        }
        assert_eq!(
            get(&engine, "r1").behaviors["Comments"]["commentCount"],
            json!(2)
        );
    }
}

#[test]
fn list_comments_pages_oldest_first_and_reads_through_no_write() {
    for target in targets("comments_list") {
        let engine = published(&target, |_| {});
        for body in ["a", "b", "c", "d", "e"] {
            comment(&engine, "r1", body);
        }
        let seq = get(&engine, "r1").seq;
        let list = |params: Value| {
            invoke(&engine, &alice(), "r1", "listComments", params).expect("listComments")
        };
        let bodies = |page: &Value| -> Vec<Value> {
            page["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|item| item["body"].clone())
                .collect()
        };
        let first = list(json!({"limit": 2}));
        assert_eq!(bodies(&first), [json!("a"), json!("b")]);
        let second = list(json!({"limit": 2, "cursor": first["next"]}));
        assert_eq!(bodies(&second), [json!("c"), json!("d")]);
        let last = list(json!({"limit": 2, "cursor": second["next"]}));
        assert_eq!(
            (bodies(&last).len(), last["next"].clone()),
            (1, Value::Null)
        );
        assert_eq!(bodies(&list(json!({}))).len(), 5);
        assert_eq!(
            invoke(&engine, &alice(), "r2", "listComments", json!({})).expect("r2"),
            json!({"items": [], "next": null})
        );
        let cursor = refusal(invoke(
            &engine,
            &alice(),
            "r1",
            "listComments",
            json!({"cursor": "bogus"}),
        ));
        assert_eq!(
            issues_of(&cursor),
            [issue("/cursor", "is not a cursor this operation returned")]
        );
        assert_eq!(
            refusal(invoke(
                &engine,
                &alice(),
                "r1",
                "listComments",
                json!({"limit": 501})
            ))
            .name(),
            "OperationParamsError"
        );
        // No event, no new sequence.
        assert_eq!(get(&engine, "r1").seq, seq);
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Review"),
                    instance_id: Some("r1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert_eq!(events.len(), 6);
    }
}

#[test]
fn the_policy_is_asked_for_write_to_comment_and_for_read_to_list() {
    for target in targets("comments_policy") {
        let policy = RecordingPolicy::new(|request| {
            request.principal().subject == "alice" || request.action() == "read"
        });
        let asked = policy.clone();
        let engine = published(&target, move |options| {
            options.policy = Some(Policy::custom(policy));
        });
        asked.take();
        comment(&engine, "r1", "Mine.");
        invoke(&engine, &bob(), "r1", "listComments", json!({})).expect("bob lists");
        let refused = refusal(invoke(
            &engine,
            &bob(),
            "r1",
            "comment",
            json!({"body": "Mine too."}),
        ));
        assert_eq!(
            refused.message(),
            "bob may not call comment (write) on Review in namespace default"
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
                (json!("alice"), json!("write"), json!("comment")),
                (json!("bob"), json!("read"), json!("listComments")),
                (json!("bob"), json!("write"), json!("comment")),
            ]
        );
    }
}

#[test]
fn a_comment_is_an_operation_event_and_deleting_the_instance_deletes_its_comments() {
    for target in targets("comments_delete") {
        let engine = published(&target, |_| {});
        comment(&engine, "r1", "Hello.");
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("Review"),
                    instance_id: Some("r1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert_eq!(
            events.last().map(|event| event.change.clone()),
            Some(json!({
                "behavior": "Comments",
                "operation": "comment",
                "params": {"body": "Hello."},
                "patch": {"behaviors": {"Comments": {"commentCount": 1}}}
            }))
        );
        assert!(engine
            .instances()
            .delete(&alice(), "Review", "r1", WriteOptions::default())
            .expect("delete"));
        create(&engine, "Q3 plan, again", "r1");
        assert_eq!(
            get(&engine, "r1").behaviors["Comments"]["commentCount"],
            json!(0)
        );
        assert_eq!(
            invoke(&engine, &alice(), "r1", "listComments", json!({})).expect("list"),
            json!({"items": [], "next": null})
        );
        assert_eq!(comment(&engine, "r1", "Fresh.")["id"], json!(1));
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
        let joined = get(&engine, "r1");
        assert_eq!(
            (Value::Object(joined.data), Value::Object(joined.behaviors)),
            (
                json!({"title": "Q3 plan"}),
                json!({"Comments": {"commentCount": 0}})
            )
        );
        comment(&engine, "r1", "Late, but here.");

        let removed = refusal(engine.schemas().define(
            &alice(),
            review_schema(json!([])),
            DefineOptions::default(),
        ));
        assert_eq!(removed.name(), "IncompatibleChangeError");
        assert!(
            removed.changes()[0].message.contains(
                "the comments its instances have would stay behind with nothing to delete them"
            ),
            "{}",
            removed.changes()[0].message
        );
    }
}
