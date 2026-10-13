//! Approvals, the core's sign-offs on the moves of the type's Workflow
//! (D56): the cases of `test/core-approvals.test.ts` (the gate on a
//! transition, signOff with its permission and its vetoes,
//! withdrawSignOff, the creator's exclusion and from, the Approvals.gates
//! field and its events, what consumes a sign-off: a change of the own
//! fields, an update's or an operation's `update()`, and entering the gated
//! state; listSignOffs, its config, its rule for a new version, deletes),
//! and `test/core-behaviors.test.ts`'s expenses document, on SQLite and,
//! with the variable set, on Postgres.
//!
//! The TypeScript file's operation `update()` is Revisions' approve (wave
//! 2a); here test.Recorder's `update()` stands in for it, and the approval
//! itself waits for Revisions. The move a Reactions rule makes on the
//! runner waits for Reactions (wave 3) and the runner (milestone 6). The
//! guidance, which milestone 6's describe document shows, is held to the
//! TypeScript text in `behaviors/tests/guidance.rs`.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error,
    InstanceRecord, Principal, ReadEvents, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::core_fixtures::Recorder;
use support::{alice, issues_of, refusal, repository, schema_document, targets, veto, Target};

fn principal(subject: &str, permissions: &[&str]) -> Principal {
    Principal::new(subject, permissions.iter().copied())
}

fn ann() -> Principal {
    principal("ann", &["docs.approve"])
}

fn bob() -> Principal {
    principal("bob", &["docs.approve"])
}

fn pat() -> Principal {
    principal("pat", &["docs.publish"])
}

fn wes() -> Principal {
    principal("wes", &[])
}

/// The core's behaviors, as the TypeScript engine registers them.
fn core() -> BehaviorSet {
    superschematic_engine_behaviors::core().with(superschematic_engine_behaviors::Approvals)
}

// A Doc moves from draft to review, then to approved or straight to
// published; approved goes back to review or on to published.
fn flow() -> Value {
    json!({
        "states": ["draft", "review", "approved", "published"],
        "transitions": [
            {"from": "draft", "to": "review"},
            {"from": "review", "to": "draft"},
            {"from": "review", "to": "approved"},
            {"from": "review", "to": "published"},
            {"from": "approved", "to": "review"},
            {"from": "approved", "to": "published"}
        ]
    })
}

// Two approvers before approved; a publisher before published, but only
// from approved.
fn gates() -> Value {
    json!({
        "approved": {"count": 2, "permission": "docs.approve"},
        "published": {"count": 1, "permission": "docs.publish", "from": ["approved"]}
    })
}

fn standard() -> Vec<Value> {
    vec![
        json!({"name": "Workflow", "config": flow()}),
        json!({"name": "Approvals", "config": {"gates": gates()}}),
    ]
}

fn doc_schema(behaviors: &[Value]) -> Value {
    let mut document = schema_document(
        "Doc",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    if !behaviors.is_empty() {
        document["types"]["Doc"]["behaviors"] = Value::Array(behaviors.to_vec());
    }
    document
}

fn open_with(target: &Target, clock: Clock, behaviors: BehaviorSet) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors;
        options.clock = clock;
    })
}

fn publish(engine: &Engine, behaviors: &[Value]) -> i64 {
    engine
        .schemas()
        .define(&alice(), doc_schema(behaviors), DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), "Doc", SchemaTarget::default())
        .expect("publish")
        .version
}

fn create(engine: &Engine, who: &Principal, title: &str, id: &str) -> InstanceRecord {
    engine
        .instances()
        .create(
            who,
            "Doc",
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create")
}

// published opens an engine with the Doc schema and d1, which alice
// created, in draft.
fn published(target: &Target, behaviors: &[Value], clock: Clock) -> Engine {
    published_with(target, behaviors, clock, core())
}

fn published_with(target: &Target, behaviors: &[Value], clock: Clock, set: BehaviorSet) -> Engine {
    let engine = open_with(target, clock, set);
    publish(&engine, behaviors);
    create(&engine, &alice(), "Plan", "d1");
    engine
}

fn invoke_on(
    engine: &Engine,
    who: &Principal,
    id: &str,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine
        .instances()
        .invoke(who, "Doc", id, operation, &params, WriteOptions::default())
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    invoke_on(engine, who, "d1", operation, params)
}

fn read(engine: &Engine, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Doc", id, Default::default())
        .expect("get")
        .expect("the instance")
}

fn gates_of(engine: &Engine, id: &str) -> Value {
    read(engine, id).behaviors["Approvals"]["gates"].clone()
}

fn status_of(engine: &Engine, id: &str) -> Value {
    read(engine, id).behaviors["Workflow"]["status"].clone()
}

fn last_patch(engine: &Engine, id: &str) -> Value {
    let events = engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Doc"),
                instance_id: Some(id),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events;
    let event = events.last().expect("an event");
    if event.kind == "operation" {
        event.change["patch"].clone()
    } else {
        event.change.clone()
    }
}

fn sign_offs_on(engine: &Engine, params: Value, id: &str) -> Value {
    invoke_on(engine, &alice(), id, "listSignOffs", params).expect("listSignOffs")
}

fn sign_offs(engine: &Engine) -> Vec<Value> {
    sign_offs_on(engine, json!({}), "d1")["items"]
        .as_array()
        .expect("items")
        .clone()
}

// subjects_consumed pairs each sign-off's subject with its consumedAt.
fn subjects_consumed(items: &[Value]) -> Vec<(Value, Value)> {
    items
        .iter()
        .map(|item| (item["subject"].clone(), item["consumedAt"].clone()))
        .collect()
}

/// The behavior, action, reason, code and details of the veto a call meets.
fn vetoed<T: std::fmt::Debug>(result: Result<T, Error>) -> (String, String, String, String, Value) {
    let (behavior, action, reason, code, details) = veto(result);
    (
        behavior,
        action,
        reason,
        code.unwrap_or_default(),
        details.unwrap_or(Value::Null),
    )
}

#[test]
fn a_move_into_a_gated_state_waits_for_its_count_of_sign_offs() {
    for target in targets("approvals_gate") {
        let engine = published(&target, &standard(), Clock::Manual(100));
        assert_eq!(
            read(&engine, "d1").behaviors["Approvals"],
            json!({"gates": {}})
        );
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");

        let missing = vetoed(invoke(
            &engine,
            &alice(),
            "transition",
            json!({"to": "approved"}),
        ));
        assert_eq!(
            missing,
            (
                "Approvals".to_owned(),
                "transition".to_owned(),
                "Doc d1 cannot move to approved with 0 of the 2 sign-offs it needs".to_owned(),
                "approvals_missing".to_owned(),
                json!({"state": "approved", "required": 2, "count": 0})
            )
        );

        engine.clock().set(200).expect("set");
        assert_eq!(
            invoke(
                &engine,
                &ann(),
                "signOff",
                json!({"state": "approved", "note": "Reads well."})
            )
            .expect("signOff"),
            json!({"state": "approved", "subject": "ann", "note": "Reads well.", "at": 200, "signedBy": ["ann"], "count": 2})
        );
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann"]}})
        );
        assert_eq!(
            last_patch(&engine, "d1"),
            json!({"behaviors": {"Approvals": {"gates": {"approved": {"signedBy": ["ann"]}}}}})
        );
        // One sign-off is not two.
        assert_eq!(
            vetoed(invoke(
                &engine,
                &alice(),
                "transition",
                json!({"to": "approved"})
            ))
            .4,
            json!({"state": "approved", "required": 2, "count": 1})
        );

        engine.clock().set(300).expect("set");
        assert_eq!(
            invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff"),
            json!({"state": "approved", "subject": "bob", "at": 300, "signedBy": ["ann", "bob"], "count": 2})
        );
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann", "bob"]}})
        );

        engine.clock().set(400).expect("set");
        assert_eq!(
            invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved"),
            json!({"from": "review", "to": "approved"})
        );
        // Entering the state consumed its sign-offs, in the transition's
        // event.
        assert_eq!(status_of(&engine, "d1"), "approved");
        assert_eq!(gates_of(&engine, "d1"), json!({}));
        assert_eq!(
            last_patch(&engine, "d1"),
            json!({"behaviors": {"Workflow": {"status": "approved", "enteredAt": 400}, "Approvals": {"gates": {"approved": null}}}})
        );
        assert_eq!(
            sign_offs(&engine),
            [
                json!({"state": "approved", "subject": "ann", "note": "Reads well.", "at": 200, "consumedAt": 400}),
                json!({"state": "approved", "subject": "bob", "at": 300, "consumedAt": 400})
            ]
        );
    }
}

#[test]
fn sign_off_needs_the_gates_permission_and_each_of_its_vetoes_carries_its_code() {
    for target in targets("approvals_sign_off") {
        let engine = published(&target, &standard(), Clock::System);
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");

        for operation in ["signOff", "withdrawSignOff"] {
            let refused = vetoed(invoke(
                &engine,
                &ann(),
                operation,
                json!({"state": "draft"}),
            ));
            assert_eq!(
                (
                    refused.0.as_str(),
                    refused.1.as_str(),
                    refused.3.as_str(),
                    refused.2.as_str()
                ),
                (
                    "Approvals",
                    operation,
                    "no_gate",
                    "draft is not a state Doc's Approvals gates (it gates approved, published)"
                )
            );
        }

        let seq = read(&engine, "d1").seq;
        let forbidden = refusal(invoke(
            &engine,
            &wes(),
            "signOff",
            json!({"state": "approved"}),
        ));
        assert_eq!(
            (forbidden.code(), forbidden.message()),
            (
                EngineErrorCode::Forbidden,
                "wes may not sign off on Doc d1 for approved: it needs permission docs.approve"
            )
        );
        // pat holds the publish permission, not the approve one.
        assert_eq!(
            refusal(invoke(
                &engine,
                &pat(),
                "signOff",
                json!({"state": "approved"})
            ))
            .code(),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            (read(&engine, "d1").seq, gates_of(&engine, "d1")),
            (seq, json!({}))
        );

        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        let twice = vetoed(invoke(
            &engine,
            &ann(),
            "signOff",
            json!({"state": "approved"}),
        ));
        assert_eq!(
            (twice.3.as_str(), twice.2.as_str()),
            (
                "already_signed",
                "ann has signed off on Doc d1 for approved already"
            )
        );

        let unsigned = vetoed(invoke(
            &engine,
            &bob(),
            "withdrawSignOff",
            json!({"state": "approved"}),
        ));
        assert_eq!(
            (unsigned.3.as_str(), unsigned.2.as_str()),
            (
                "not_signed",
                "bob has no sign-off on Doc d1 for approved to withdraw"
            )
        );

        // A withdrawal takes the caller's own back, and it is gone; the
        // caller may sign again.
        let withdrawn = invoke(
            &engine,
            &ann(),
            "withdrawSignOff",
            json!({"state": "approved"}),
        )
        .expect("withdrawSignOff");
        assert_eq!(
            (
                withdrawn["state"].clone(),
                withdrawn["subject"].clone(),
                withdrawn["at"].is_i64()
            ),
            (json!("approved"), json!("ann"), true)
        );
        assert_eq!(gates_of(&engine, "d1"), json!({}));
        assert_eq!(
            last_patch(&engine, "d1"),
            json!({"behaviors": {"Approvals": {"gates": {"approved": null}}}})
        );
        assert!(sign_offs(&engine).is_empty());
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");

        // The instance's creator cannot sign: ann creates d2.
        create(&engine, &ann(), "Ann draft", "d2");
        let creator = vetoed(invoke_on(
            &engine,
            &ann(),
            "d2",
            "signOff",
            json!({"state": "approved"}),
        ));
        assert_eq!(
            (creator.3.as_str(), creator.2.as_str()),
            (
                "creator_cannot_sign",
                "ann created Doc d2, and its gate on approved takes no sign-off from its creator"
            )
        );
        assert_eq!(
            invoke_on(
                &engine,
                &bob(),
                "d2",
                "signOff",
                json!({"state": "approved"})
            )
            .expect("signOff")["subject"],
            "bob"
        );

        // Nobody signs for the state the instance is in.
        invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved");
        let in_state = vetoed(invoke(
            &engine,
            &ann(),
            "signOff",
            json!({"state": "approved"}),
        ));
        assert_eq!(
            (in_state.3.as_str(), in_state.2.as_str()),
            (
                "in_state",
                "Doc d1 is approved already; a sign-off for it waits until it has left"
            )
        );
    }
}

#[test]
fn from_limits_a_gate_and_exclude_creator_false_lets_the_creator_sign() {
    for target in targets("approvals_from") {
        let engine = published(&target, &standard(), Clock::Manual(7));
        // published is gated from approved alone: from review it needs none.
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        assert_eq!(
            invoke(&engine, &alice(), "transition", json!({"to": "published"})).expect("published"),
            json!({"from": "review", "to": "published"})
        );

        create(&engine, &alice(), "Second", "d2");
        let on = |who: &Principal, operation: &str, params: Value| {
            invoke_on(&engine, who, "d2", operation, params)
        };
        on(&alice(), "transition", json!({"to": "review"})).expect("review");
        on(&ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        on(&bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        on(&alice(), "transition", json!({"to": "approved"})).expect("approved");
        assert_eq!(
            vetoed(on(&alice(), "transition", json!({"to": "published"}))).4,
            json!({"state": "published", "required": 1, "count": 0})
        );
        assert_eq!(
            on(&pat(), "signOff", json!({"state": "published"})).expect("signOff"),
            json!({"state": "published", "subject": "pat", "at": 7, "signedBy": ["pat"], "count": 1})
        );
        on(&alice(), "transition", json!({"to": "published"})).expect("published");
        assert_eq!(
            (status_of(&engine, "d2"), gates_of(&engine, "d2")),
            (json!("published"), json!({}))
        );
    }
    for target in targets("approvals_creator") {
        let open = published(
            &target,
            &[
                json!({"name": "Workflow", "config": flow()}),
                json!({"name": "Approvals", "config": {"gates": {"approved": {"count": 1, "permission": "docs.approve", "excludeCreator": false}}}}),
            ],
            Clock::System,
        );
        create(&open, &ann(), "Ann draft", "d2");
        invoke_on(&open, &ann(), "d2", "signOff", json!({"state": "approved"})).expect("signOff");
        invoke_on(&open, &ann(), "d2", "transition", json!({"to": "review"})).expect("review");
        assert_eq!(
            invoke_on(&open, &ann(), "d2", "transition", json!({"to": "approved"}))
                .expect("approved"),
            json!({"from": "review", "to": "approved"})
        );
    }
}

#[test]
fn a_move_workflow_refuses_meets_workflows_veto_whichever_the_type_lists_first() {
    for target in targets("approvals_workflow_first") {
        let engine = published(
            &target,
            &[
                json!({"name": "Approvals", "config": {"gates": gates()}}),
                json!({"name": "Workflow", "config": flow()}),
            ],
            Clock::System,
        );
        let refusal_of = |to: &str| {
            let refused = vetoed(invoke(&engine, &alice(), "transition", json!({ "to": to })));
            (refused.0, refused.3)
        };
        let pair = |behavior: &str, code: &str| (behavior.to_owned(), code.to_owned());
        // No transition leads from draft to approved.
        assert_eq!(
            refusal_of("approved"),
            pair("Workflow", "transition_not_allowed")
        );
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        assert_eq!(
            refusal_of("approved"),
            pair("Approvals", "approvals_missing")
        );
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved");
        assert_eq!(refusal_of("approved"), pair("Workflow", "already_in_state"));
    }
}

#[test]
fn a_change_of_the_own_fields_consumes_every_gates_sign_offs_and_nothing_else_does() {
    for target in targets("approvals_reset") {
        // test.Recorder needs the open meta-schema.
        let engine = target.open_with(core().with(Recorder), |options| {
            options.clock = Clock::Manual(10);
        });
        publish(
            &engine,
            &[
                json!({"name": "Workflow", "config": flow()}),
                json!({"name": "Approvals", "config": {"gates": gates()}}),
                json!({"name": "test.Recorder"}),
                json!({"name": "Comments"}),
            ],
        );
        create(&engine, &alice(), "Plan", "d1");
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &pat(), "signOff", json!({"state": "published"})).expect("signOff");
        let held = json!({"approved": {"signedBy": ["ann"]}, "published": {"signedBy": ["pat"]}});

        // An operation that changes no own field, and an update that changes
        // nothing, leave them.
        invoke(&engine, &wes(), "comment", json!({"body": "Looks close."})).expect("comment");
        engine
            .instances()
            .update(
                &alice(),
                "Doc",
                "d1",
                &json!({"title": "Plan"}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(gates_of(&engine, "d1"), held);

        engine.clock().set(20).expect("set");
        engine
            .instances()
            .update(
                &alice(),
                "Doc",
                "d1",
                &json!({"body": "The plan, in full."}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(gates_of(&engine, "d1"), json!({}));
        assert_eq!(
            last_patch(&engine, "d1"),
            json!({"data": {"body": "The plan, in full."}, "behaviors": {"Approvals": {"gates": {"approved": null, "published": null}}}})
        );
        assert_eq!(
            subjects_consumed(&sign_offs(&engine)),
            [(json!("ann"), json!(20)), (json!("pat"), json!(20))]
        );

        // A sign-off given again counts; an operation's update() consumes it
        // (Revisions' approve in the TypeScript file, which waits for wave
        // 2a).
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann"]}})
        );
        engine.clock().set(30).expect("set");
        invoke(
            &engine,
            &wes(),
            "record",
            json!({"patch": {"body": "Shorter."}}),
        )
        .expect("record");
        assert_eq!(
            (
                read(&engine, "d1").data["body"].clone(),
                gates_of(&engine, "d1")
            ),
            (json!("Shorter."), json!({}))
        );
        assert_eq!(sign_offs(&engine).last().expect("one")["consumedAt"], 30);
        // An operation that writes no own field keeps them.
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(
            &engine,
            &wes(),
            "record",
            json!({"patch": {"body": "Shorter."}}),
        )
        .expect("record");
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann"]}})
        );
    }
    for target in targets("approvals_kept") {
        // resetOnChange false keeps them through a change of the own fields;
        // entering the gated state still consumes its own.
        let kept = published(
            &target,
            &[
                json!({"name": "Workflow", "config": flow()}),
                json!({"name": "Approvals", "config": {"gates": gates(), "resetOnChange": false}}),
            ],
            Clock::System,
        );
        invoke(&kept, &alice(), "transition", json!({"to": "review"})).expect("review");
        invoke(&kept, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&kept, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&kept, &pat(), "signOff", json!({"state": "published"})).expect("signOff");
        kept.instances()
            .update(
                &alice(),
                "Doc",
                "d1",
                &json!({"body": "Changed after."}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(
            gates_of(&kept, "d1"),
            json!({"approved": {"signedBy": ["ann", "bob"]}, "published": {"signedBy": ["pat"]}})
        );
        invoke(&kept, &alice(), "transition", json!({"to": "approved"})).expect("approved");
        assert_eq!(
            gates_of(&kept, "d1"),
            json!({"published": {"signedBy": ["pat"]}})
        );
        invoke(&kept, &alice(), "transition", json!({"to": "published"})).expect("published");
        assert_eq!(gates_of(&kept, "d1"), json!({}));
    }
}

#[test]
fn entering_a_gated_state_consumes_its_sign_offs_so_a_return_to_it_is_signed_again() {
    for target in targets("approvals_return") {
        let engine = published(&target, &standard(), Clock::System);
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved");
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        // Back in review, approved waits for a fresh round, which the same
        // principals may sign.
        assert_eq!(
            vetoed(invoke(
                &engine,
                &alice(),
                "transition",
                json!({"to": "approved"})
            ))
            .4,
            json!({"state": "approved", "required": 2, "count": 0})
        );
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved");
        assert_eq!(
            sign_offs(&engine)
                .iter()
                .map(|item| (item["subject"].clone(), item.get("consumedAt").is_some()))
                .collect::<Vec<_>>(),
            [
                (json!("ann"), true),
                (json!("bob"), true),
                (json!("ann"), true),
                (json!("bob"), true)
            ]
        );
        // The move a Reactions rule makes on the runner waits for Reactions
        // (wave 3) and the runner (milestone 6).
    }
}

#[test]
fn list_sign_offs_pages_oldest_first_filters_by_state_and_writes_nothing() {
    for target in targets("approvals_list") {
        let engine = published(&target, &standard(), Clock::System);
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        invoke(
            &engine,
            &ann(),
            "signOff",
            json!({"state": "approved", "note": "First."}),
        )
        .expect("signOff");
        invoke(&engine, &pat(), "signOff", json!({"state": "published"})).expect("signOff");
        invoke(&engine, &bob(), "signOff", json!({"state": "approved"})).expect("signOff");
        engine
            .instances()
            .update(
                &alice(),
                "Doc",
                "d1",
                &json!({"body": "Changed."}),
                WriteOptions::default(),
            )
            .expect("update");
        invoke(
            &engine,
            &ann(),
            "signOff",
            json!({"state": "approved", "note": "Again."}),
        )
        .expect("signOff");
        let seq = read(&engine, "d1").seq;

        let first = sign_offs_on(&engine, json!({"limit": 2}), "d1");
        assert_eq!(
            first["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|item| (
                    item["state"].clone(),
                    item["subject"].clone(),
                    item["note"].clone(),
                    item.get("consumedAt").is_some()
                ))
                .collect::<Vec<_>>(),
            [
                (json!("approved"), json!("ann"), json!("First."), true),
                (json!("published"), json!("pat"), Value::Null, true)
            ]
        );
        assert!(first["next"].is_string());
        let rest = sign_offs_on(&engine, json!({"limit": 2, "cursor": first["next"]}), "d1");
        let items = rest["items"].as_array().expect("items");
        assert_eq!(
            (
                items
                    .iter()
                    .map(|item| (
                        item["subject"].clone(),
                        item["note"].clone(),
                        item["consumedAt"].clone()
                    ))
                    .collect::<Vec<_>>(),
                rest["next"].clone()
            ),
            (
                vec![
                    (json!("bob"), Value::Null, items[0]["consumedAt"].clone()),
                    (json!("ann"), json!("Again."), Value::Null)
                ],
                Value::Null
            )
        );
        assert!(items[0]["consumedAt"].is_i64());
        assert_eq!(
            sign_offs_on(&engine, json!({"state": "published"}), "d1")["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|item| item["subject"].clone())
                .collect::<Vec<_>>(),
            [json!("pat")]
        );
        assert_eq!(read(&engine, "d1").seq, seq);
        let refused = refusal(invoke_on(
            &engine,
            &alice(),
            "d1",
            "listSignOffs",
            json!({"cursor": "nope"}),
        ));
        assert_eq!(
            refused
                .issues()
                .iter()
                .map(|issue| issue.path.as_str())
                .collect::<Vec<_>>(),
            ["/cursor"]
        );
    }
}

#[test]
fn its_config_holds_every_gated_and_from_state_to_the_workflow() {
    for target in targets("approvals_config") {
        let engine = open_with(&target, Clock::System, core());
        let refused = |config: Value| {
            refusal(engine.schemas().define(
                &alice(),
                doc_schema(&[
                    json!({"name": "Workflow", "config": flow()}),
                    json!({"name": "Approvals", "config": config}),
                ]),
                DefineOptions::default(),
            ))
        };
        let message = |config: Value| refused(config).message().to_owned();
        let lost = message(json!({"gates": {"lost": {"count": 1, "permission": "docs.approve"}}}));
        assert!(
            lost.contains("behavior Approvals config: gate \"lost\" is not a state of the type's Workflow (draft, review, approved, published)"),
            "{lost}"
        );
        let gone = message(
            json!({"gates": {"approved": {"count": 1, "permission": "docs.approve", "from": ["review", "gone"]}}}),
        );
        assert!(
            gone.contains(
                "gate \"approved\": from state \"gone\" is not a state of the type's Workflow"
            ),
            "{gone}"
        );
        let itself = message(
            json!({"gates": {"approved": {"count": 1, "permission": "docs.approve", "from": ["approved"]}}}),
        );
        assert!(
            itself.contains("gate \"approved\": from names \"approved\" itself, and no transition leads from a state to itself"),
            "{itself}"
        );
        // The core meta-schema holds the config to the declaration's
        // configSchema first.
        let at = |config: Value| -> Vec<String> {
            issues_of(&refused(config))
                .into_iter()
                .map(|(path, _)| path)
                .collect()
        };
        assert!(
            at(json!({"gates": {}})).contains(&"/types/Doc/behaviors/1/config/gates".to_owned())
        );
        assert!(
            at(json!({"gates": {"approved": {"count": 0, "permission": "docs.approve"}}}))
                .contains(&"/types/Doc/behaviors/1/config/gates/approved/count".to_owned())
        );
        assert!(at(json!({"gates": {"approved": {"count": 1}}}))
            .iter()
            .any(|path| path.starts_with("/types/Doc/behaviors/1/config/gates/approved")));
        let unflowed = refusal(engine.schemas().define(
            &alice(),
            doc_schema(&[json!({"name": "Approvals", "config": {"gates": gates()}})]),
            DefineOptions::default(),
        ));
        assert!(
            unflowed.message().contains(
                "type Doc: behavior Approvals requires behavior Workflow, which the type does not list"
            ),
            "{unflowed}"
        );
    }
}

#[test]
fn a_new_version_may_add_change_and_remove_gates_and_approvals_does_not_leave_a_schema_with_instances(
) {
    for target in targets("approvals_versions") {
        let engine = open_with(&target, Clock::Manual(1), core());
        let flowed = [json!({"name": "Workflow", "config": flow()})];
        publish(&engine, &flowed);
        create(&engine, &alice(), "Plan", "d1");

        assert_eq!(publish(&engine, &standard()), 2);
        assert_eq!(gates_of(&engine, "d1"), json!({}));
        invoke(&engine, &alice(), "transition", json!({"to": "review"})).expect("review");
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        invoke(&engine, &pat(), "signOff", json!({"state": "published"})).expect("signOff");

        // The approved gate needs one now, and the published gate goes.
        engine.clock().set(50).expect("set");
        assert_eq!(
            publish(
                &engine,
                &[
                    json!({"name": "Workflow", "config": flow()}),
                    json!({"name": "Approvals", "config": {"gates": {"approved": {"count": 1, "permission": "docs.approve"}}}}),
                ]
            ),
            3
        );
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann"]}})
        );
        assert_eq!(
            subjects_consumed(&sign_offs(&engine)),
            [(json!("ann"), Value::Null), (json!("pat"), json!(50))]
        );
        assert_eq!(
            invoke(&engine, &alice(), "transition", json!({"to": "approved"})).expect("approved"),
            json!({"from": "review", "to": "approved"})
        );

        let removed = refusal(engine.schemas().define(
            &alice(),
            doc_schema(&flowed),
            DefineOptions::default(),
        ));
        let changes = removed.changes();
        assert!(
            changes[0].message.contains(
                "the sign-offs its instances hold would stay behind with nothing to delete them"
            ),
            "{changes:?}"
        );
    }
    for target in targets("approvals_empty") {
        // A schema with no instances may drop it.
        let empty = open_with(&target, Clock::System, core());
        publish(&empty, &standard());
        assert_eq!(
            publish(&empty, &[json!({"name": "Workflow", "config": flow()})]),
            2
        );
    }
}

#[test]
fn deleting_the_instance_deletes_its_sign_offs() {
    for target in targets("approvals_delete") {
        let engine = published(&target, &standard(), Clock::System);
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        engine
            .instances()
            .delete(&alice(), "Doc", "d1", WriteOptions::default())
            .expect("delete");
        create(&engine, &alice(), "Plan again", "d1");
        assert_eq!(
            (gates_of(&engine, "d1"), sign_offs(&engine)),
            (json!({}), Vec::<Value>::new())
        );
        invoke(&engine, &ann(), "signOff", json!({"state": "approved"})).expect("signOff");
        assert_eq!(
            gates_of(&engine, "d1"),
            json!({"approved": {"signedBy": ["ann"]}})
        );
    }
}

/// The expenses document the core binary builds
/// (fixture-approvals-json).
fn expenses_document() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-approvals-json/src/expense.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the expenses document"))
        .expect("JSON")
}

/// The case of `test/core-behaviors.test.ts` that runs the expenses
/// document.
#[test]
fn it_runs_the_expenses_document_a_claim_waits_for_two_approvers_then_one_to_pay_it() {
    for target in targets("approvals_expenses_document") {
        let claimant = principal("cleo", &["expenses.approve"]);
        let approvers = [
            principal("ann", &["expenses.approve"]),
            principal("bob", &["expenses.approve"]),
        ];
        let payer = principal("pia", &["expenses.pay"]);
        let engine = open_with(&target, Clock::System, core());
        engine
            .schemas()
            .define(&alice(), expenses_document(), DefineOptions::default())
            .expect("define");
        engine
            .schemas()
            .publish(&alice(), "expenses", SchemaTarget::default())
            .expect("publish");
        assert_eq!(
            engine
                .schemas()
                .behaviors(&alice(), "expenses", Default::default())
                .expect("behaviors")
                .iter()
                .map(|behavior| behavior.name.as_str())
                .collect::<Vec<_>>(),
            ["Workflow", "Approvals"]
        );
        let created = engine
            .instances()
            .create(
                &claimant,
                "expenses",
                &json!({"title": "Train to the offsite", "amount": 120}),
                CreateOptions {
                    id: Some("trip"),
                    ..Default::default()
                },
            )
            .expect("create");
        assert_eq!(
            Value::Object(created.behaviors.clone()),
            json!({"Workflow": {"status": "draft", "enteredAt": created.created_at}, "Approvals": {"gates": {}}})
        );
        let call = |who: &Principal, operation: &str, params: Value| {
            engine.instances().invoke(
                who,
                "expenses",
                "trip",
                operation,
                &params,
                WriteOptions::default(),
            )
        };
        let approvals = || {
            engine
                .instances()
                .get(&alice(), "expenses", "trip", Default::default())
                .expect("get")
                .expect("trip")
                .behaviors["Approvals"]
                .clone()
        };
        call(&claimant, "transition", json!({"to": "submitted"})).expect("submitted");
        assert_eq!(
            refusal(call(&claimant, "signOff", json!({"state": "approved"}))).code(),
            EngineErrorCode::Vetoed
        );
        for approver in &approvers {
            call(approver, "signOff", json!({"state": "approved"})).expect("signOff");
        }
        // The claim changes: the sign-offs it had no longer count.
        engine
            .instances()
            .update(
                &claimant,
                "expenses",
                "trip",
                &json!({"amount": 140}),
                WriteOptions::default(),
            )
            .expect("update");
        assert_eq!(approvals(), json!({"gates": {}}));
        assert_eq!(
            refusal(call(&claimant, "transition", json!({"to": "approved"}))).code(),
            EngineErrorCode::Vetoed
        );
        for approver in &approvers {
            call(approver, "signOff", json!({"state": "approved"})).expect("signOff");
        }
        assert_eq!(
            call(&claimant, "transition", json!({"to": "approved"})).expect("approved"),
            json!({"from": "submitted", "to": "approved"})
        );
        assert_eq!(
            refusal(call(&claimant, "transition", json!({"to": "paid"}))).code(),
            EngineErrorCode::Vetoed
        );
        call(&payer, "signOff", json!({"state": "paid"})).expect("signOff");
        call(&claimant, "transition", json!({"to": "paid"})).expect("paid");
        let read = engine
            .instances()
            .get(&alice(), "expenses", "trip", Default::default())
            .expect("get")
            .expect("trip");
        assert_eq!(read.behaviors["Workflow"]["status"], "paid");
        assert_eq!(read.behaviors["Approvals"], json!({"gates": {}}));
        let listed = call(&alice(), "listSignOffs", json!({})).expect("listSignOffs");
        assert_eq!(
            listed["items"]
                .as_array()
                .expect("items")
                .iter()
                .map(|item| (item["subject"].clone(), item.get("consumedAt").is_some()))
                .collect::<Vec<_>>(),
            [
                (json!("ann"), true),
                (json!("bob"), true),
                (json!("ann"), true),
                (json!("bob"), true),
                (json!("pia"), true)
            ]
        );
    }
}
