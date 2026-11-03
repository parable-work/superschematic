//! Revisions, the core's immutable revisions with an optional review step:
//! the cases of `test/core-revisions.test.ts` (a revision per change,
//! listRevisions and getRevision, propose with its evidence, approve and
//! reject with the review permission, pendingProposals, listProposals
//! through the read path, events, delete, and its rule for a new version),
//! on SQLite and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    BehaviorSet, Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, EngineErrorKind,
    EngineOptions, Error, Policy, Principal, ReadEvents, SchemaTarget, WriteOptions,
};
use superschematic_engine_behaviors::{Comments, Revisions, Workflow};
use superschematic_engine_testkit::behaviors::RevisionHold;
use support::{
    alice, issue, issues_of, refusal, schema_document, targets, veto, RecordingPolicy, Target,
};

fn review() -> Value {
    json!({"review": {"permission": "documents.review"}})
}

fn reviewer() -> Principal {
    Principal::new("rae", ["documents.review"])
}

fn writer() -> Principal {
    Principal::new("wes", Vec::<String>::new())
}

/// The core behaviors these tests compose.
fn behaviors() -> BehaviorSet {
    BehaviorSet::new()
        .with(Workflow)
        .with(Comments)
        .with(Revisions)
}

fn document_schema(behaviors: Value) -> Value {
    let mut document = schema_document(
        "Document",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true, "validateMaxLength": 40},
            {"name": "body", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    if behaviors.as_array().is_some_and(|list| !list.is_empty()) {
        document["types"]["Document"]["behaviors"] = behaviors;
    }
    document
}

fn reviewed() -> Value {
    json!([{"name": "Revisions", "config": review()}])
}

fn open_with(target: &Target, configure: impl FnOnce(&mut EngineOptions)) -> Engine {
    target.open_core_with(|options| {
        options.behaviors = behaviors();
        configure(options);
    })
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine) -> i64 {
    engine
        .schemas()
        .publish(&alice(), "Document", SchemaTarget::default())
        .expect("publish")
        .version
}

fn create(engine: &Engine, title: &str, id: &str) {
    engine
        .instances()
        .create(
            &alice(),
            "Document",
            &json!({ "title": title }),
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create");
}

// published opens an engine, publishes Document with the behaviors given
// and creates d1.
fn published(
    target: &Target,
    behaviors: Value,
    configure: impl FnOnce(&mut EngineOptions),
) -> Engine {
    let engine = open_with(target, configure);
    define(&engine, &document_schema(behaviors)).expect("define");
    publish(&engine);
    create(&engine, "Plan", "d1");
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
        "Document",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn update(engine: &Engine, principal: &Principal, id: &str, patch: Value) {
    engine
        .instances()
        .update(principal, "Document", id, &patch, WriteOptions::default())
        .expect("update");
}

fn read(engine: &Engine, id: &str) -> superschematic_engine::InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Document", id, Default::default())
        .expect("get")
        .expect("the instance")
}

fn revisions_of(engine: &Engine, id: &str) -> Vec<Value> {
    invoke(engine, &alice(), id, "listRevisions", json!({})).expect("listRevisions")["items"]
        .as_array()
        .expect("items")
        .clone()
}

fn propose(engine: &Engine, patch: Value) -> Result<Value, Error> {
    invoke(
        engine,
        &writer(),
        "d1",
        "propose",
        json!({ "patch": patch }),
    )
}

fn events(engine: &Engine, id: &str) -> Vec<superschematic_engine::EngineEvent> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Document"),
                instance_id: Some(id),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
}

fn last_change(engine: &Engine, id: &str) -> Value {
    events(engine, id).last().expect("an event").change.clone()
}

// The paths and rules of a refused instance's issues.
fn rules(error: &superschematic_engine::EngineError) -> Vec<(String, String)> {
    match error.kind() {
        EngineErrorKind::InstanceValidation { issues } => issues
            .iter()
            .map(|issue| (issue.path.clone(), issue.rule.clone()))
            .collect(),
        other => panic!("expected an InstanceValidationError, got {other:?}"),
    }
}

fn pair(path: &str, rule: &str) -> (String, String) {
    (path.to_owned(), rule.to_owned())
}

#[test]
fn a_create_records_revision_1_and_each_change_the_next_and_a_change_of_nothing_none() {
    for target in targets("revisions_each_change") {
        let engine = published(&target, json!([{"name": "Revisions"}]), |options| {
            options.clock = Clock::Manual(10);
        });
        assert_eq!(
            read(&engine, "d1").behaviors["Revisions"]["revision"],
            json!(1)
        );
        engine.clock().set(20).expect("clock");
        update(&engine, &writer(), "d1", json!({"body": "Draft one."}));
        update(&engine, &writer(), "d1", json!({"body": "Draft one."}));
        engine.clock().set(30).expect("clock");
        update(
            &engine,
            &alice(),
            "d1",
            json!({"title": "Plan B", "body": null}),
        );
        assert_eq!(
            revisions_of(&engine, "d1"),
            [
                json!({"revision": 1, "data": {"title": "Plan"}, "createdBy": "alice", "createdAt": 10}),
                json!({"revision": 2, "data": {"title": "Plan", "body": "Draft one."}, "createdBy": "wes", "createdAt": 20}),
                json!({"revision": 3, "data": {"title": "Plan B"}, "createdBy": "alice", "createdAt": 30}),
            ]
        );
        let record = read(&engine, "d1");
        assert_eq!(
            json!([record.data, record.behaviors]),
            json!([{"title": "Plan B"}, {"Revisions": {"revision": 3}}])
        );
        // The update's event carries the new revision number beside the
        // patch.
        assert_eq!(
            last_change(&engine, "d1"),
            json!({"data": {"title": "Plan B", "body": null}, "behaviors": {"Revisions": {"revision": 3}}})
        );
    }
}

#[test]
fn list_revisions_pages_oldest_first_through_no_write() {
    for target in targets("revisions_pages") {
        let engine = published(&target, json!([{"name": "Revisions"}]), |_| {});
        for body in ["a", "b", "c", "d"] {
            update(&engine, &alice(), "d1", json!({ "body": body }));
        }
        let seq = read(&engine, "d1").seq;
        let first = invoke(
            &engine,
            &alice(),
            "d1",
            "listRevisions",
            json!({"limit": 3}),
        )
        .expect("list");
        let numbers: Vec<Value> = first["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| item["revision"].clone())
            .collect();
        assert_eq!(numbers, [json!(1), json!(2), json!(3)]);
        let rest = invoke(
            &engine,
            &alice(),
            "d1",
            "listRevisions",
            json!({"limit": 3, "cursor": first["next"]}),
        )
        .expect("list");
        let bodies: Vec<Value> = rest["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| item["data"]["body"].clone())
            .collect();
        assert_eq!(json!([bodies, rest["next"]]), json!([["c", "d"], null]));
        assert_eq!(read(&engine, "d1").seq, seq);
    }
}

#[test]
fn without_review_in_its_config_the_review_operations_refuse() {
    for target in targets("revisions_no_review") {
        let engine = published(&target, json!([{"name": "Revisions"}]), |_| {});
        for (operation, params) in [
            ("propose", json!({"patch": {"title": "X"}})),
            ("approve", json!({"proposal": 1})),
            ("reject", json!({"proposal": 1})),
            ("listProposals", json!({})),
        ] {
            let (_, _, reason, code, _) =
                veto(invoke(&engine, &reviewer(), "d1", operation, params));
            assert_eq!(
                (reason.as_str(), code.as_deref()),
                (
                    "Document has no review step: its Revisions config sets no review",
                    Some("no_review")
                ),
                "{operation}"
            );
        }
    }
}

#[test]
fn propose_stores_a_pending_proposal_and_changes_no_own_field() {
    for target in targets("revisions_propose") {
        let engine = published(&target, reviewed(), |_| {});
        let seq = read(&engine, "d1").seq;
        assert_eq!(
            read(&engine, "d1").behaviors["Revisions"]["pendingProposals"],
            json!(0)
        );
        let proposed = invoke(
            &engine,
            &writer(),
            "d1",
            "propose",
            json!({"patch": {"body": "Proposed."}, "note": "Fills it in."}),
        )
        .expect("propose");
        let listed =
            invoke(&engine, &alice(), "d1", "listProposals", json!({})).expect("listProposals");
        assert_eq!(
            proposed,
            json!({
                "id": 1,
                "patch": {"body": "Proposed."},
                "note": "Fills it in.",
                "base": 1,
                "state": "pending",
                "createdBy": "wes",
                "createdAt": listed["items"][0]["createdAt"]
            })
        );
        // The members come in the TypeScript engine's order.
        let keys: Vec<&String> = proposed.as_object().unwrap().keys().collect();
        assert_eq!(
            keys,
            [
                "id",
                "patch",
                "note",
                "base",
                "state",
                "createdBy",
                "createdAt"
            ]
        );
        let instance = read(&engine, "d1");
        assert_eq!(
            json!([instance.data, instance.behaviors]),
            json!([{"title": "Plan"}, {"Revisions": {"revision": 1, "pendingProposals": 1}}])
        );
        assert_eq!(instance.seq, seq + 1);
        assert_eq!(revisions_of(&engine, "d1").len(), 1);
        assert_eq!(
            last_change(&engine, "d1"),
            json!({
                "behavior": "Revisions",
                "operation": "propose",
                "params": {"patch": {"body": "Proposed."}, "note": "Fills it in."},
                "patch": {"behaviors": {"Revisions": {"pendingProposals": 1}}}
            })
        );
    }
}

#[test]
fn propose_refuses_a_patch_that_leaves_the_instance_invalid_names_an_unknown_field_or_changes_nothing(
) {
    for target in targets("revisions_propose_refused") {
        let engine = published(&target, reviewed(), |_| {});
        let invalid = refusal(propose(&engine, json!({"title": "x".repeat(41)})));
        assert_eq!(rules(&invalid), [pair("title", "maxLength")]);
        let required = refusal(propose(&engine, json!({"title": null})));
        assert_eq!(rules(&required), [pair("title", "required")]);
        // A behavior's field is no own field: its name in a patch is
        // unknown, as any undeclared key is.
        let owned = refusal(propose(&engine, json!({"revision": 9})));
        assert_eq!(
            owned.details().expect("details")["issues"],
            json!([{"path": "revision", "rule": "unknown", "message": "Document has no field revision"}])
        );
        let nothing = refusal(propose(&engine, json!({"title": "Plan"})));
        assert_eq!(
            issues_of(&nothing),
            [issue(
                "/patch",
                "changes nothing: Document d1 already has these fields"
            )]
        );
        assert!(matches!(
            refusal(invoke(
                &engine,
                &writer(),
                "d1",
                "propose",
                json!({"patch": {}})
            ))
            .kind(),
            EngineErrorKind::OperationParams { .. }
        ));
        assert!(matches!(
            refusal(invoke(
                &engine,
                &writer(),
                "d1",
                "propose",
                json!({"fields": {"title": "X"}})
            ))
            .kind(),
            EngineErrorKind::OperationParams { .. }
        ));
        assert_eq!(
            invoke(&engine, &alice(), "d1", "listProposals", json!({})).expect("list")["items"],
            json!([])
        );
    }
}

#[test]
fn approve_needs_the_review_permission_applies_the_patch_and_records_the_revision_it_makes() {
    for target in targets("revisions_approve") {
        let engine = published(&target, reviewed(), |_| {});
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        let refused = refusal(invoke(
            &engine,
            &writer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        ));
        assert_eq!(refused.code(), EngineErrorCode::Forbidden);
        assert_eq!(
            refused.message(),
            "wes may not approve a proposal on Document d1: it needs permission documents.review"
        );
        assert_eq!(
            refusal(invoke(
                &engine,
                &writer(),
                "d1",
                "reject",
                json!({"proposal": 1})
            ))
            .code(),
            EngineErrorCode::Forbidden
        );

        let approved = invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        assert_eq!(
            json!([
                approved["state"],
                approved["reviewedBy"],
                approved["revision"],
                approved["base"]
            ]),
            json!(["approved", "rae", 2, 1])
        );
        let record = read(&engine, "d1");
        assert_eq!(
            json!([record.data, record.behaviors]),
            json!([{"title": "Plan", "body": "Proposed."}, {"Revisions": {"revision": 2, "pendingProposals": 0}}])
        );
        let revisions = revisions_of(&engine, "d1");
        assert_eq!(revisions.len(), 2);
        assert_eq!(
            revisions[1],
            json!({
                "revision": 2,
                "data": {"title": "Plan", "body": "Proposed."},
                "createdBy": "rae",
                "createdAt": revisions[1]["createdAt"],
                "proposal": 1
            })
        );
        // The approval's event carries the own field it changed, the new
        // revision and the pending count.
        assert_eq!(
            last_change(&engine, "d1"),
            json!({
                "behavior": "Revisions",
                "operation": "approve",
                "params": {"proposal": 1},
                "patch": {"data": {"body": "Proposed."}, "behaviors": {"Revisions": {"revision": 2, "pendingProposals": 0}}}
            })
        );
        let (_, _, reason, code, details) = veto(invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        ));
        assert_eq!(
            json!([reason, code, details]),
            json!(["proposal 1 is approved, not pending", "not_pending", {"proposal": 1, "state": "approved"}])
        );
        let unknown = refusal(invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 7}),
        ));
        assert_eq!(
            issues_of(&unknown),
            [issue("/proposal", "Document d1 has no proposal 7")]
        );
    }
}

#[test]
fn an_approvals_update_meets_every_guard_and_a_veto_leaves_the_proposal_pending() {
    for target in targets("revisions_guarded") {
        let engine = target.open_with(behaviors().with(RevisionHold), |_| {});
        define(
            &engine,
            &document_schema(
                json!([{"name": "Revisions", "config": review()}, {"name": "test.Hold"}]),
            ),
        )
        .expect("define");
        publish(&engine);
        create(&engine, "Plan", "d1");
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        invoke(&engine, &alice(), "d1", "hold", json!({})).expect("hold");
        let (behavior, action, reason, _, _) = veto(invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str(), reason.as_str()),
            ("test.Hold", "update", "it is held")
        );
        assert_eq!(
            invoke(
                &engine,
                &alice(),
                "d1",
                "listProposals",
                json!({"state": "pending"})
            )
            .expect("list")["items"]
                .as_array()
                .unwrap()
                .len(),
            1
        );
        assert_eq!(read(&engine, "d1").data.get("body"), None);
    }
}

#[test]
fn a_patch_applies_to_the_instance_as_it_is_at_approval_and_base_shows_what_it_was_made_against() {
    for target in targets("revisions_base") {
        let engine = published(&target, reviewed(), |_| {});
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        update(&engine, &alice(), "d1", json!({"title": "Plan B"}));
        let approved = invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        assert_eq!(
            json!([approved["base"], approved["revision"]]),
            json!([1, 3])
        );
        let record = read(&engine, "d1");
        assert_eq!(
            json!([record.data, record.behaviors]),
            json!([{"title": "Plan B", "body": "Proposed."}, {"Revisions": {"revision": 3, "pendingProposals": 0}}])
        );
        // A proposal whose patch the instance already has approves without a
        // new revision.
        propose(&engine, json!({"title": "Plan C"})).expect("propose");
        update(&engine, &alice(), "d1", json!({"title": "Plan C"}));
        let noop = invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 2}),
        )
        .expect("approve");
        assert_eq!(
            json!([noop["state"], noop["revision"]]),
            json!(["approved", 4])
        );
        assert_eq!(revisions_of(&engine, "d1").len(), 4);
    }
}

#[test]
fn reject_settles_a_proposal_with_a_reason_and_changes_nothing_else() {
    for target in targets("revisions_reject") {
        let engine = published(&target, reviewed(), |_| {});
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        let rejected = invoke(
            &engine,
            &reviewer(),
            "d1",
            "reject",
            json!({"proposal": 1, "reason": "Not yet."}),
        )
        .expect("reject");
        assert_eq!(
            json!([
                rejected["state"],
                rejected["reviewedBy"],
                rejected["reason"]
            ]),
            json!(["rejected", "rae", "Not yet."])
        );
        assert_eq!(rejected.get("revision"), None);
        let record = read(&engine, "d1");
        assert_eq!(
            json!([record.data, record.behaviors]),
            json!([{"title": "Plan"}, {"Revisions": {"revision": 1, "pendingProposals": 0}}])
        );
        let (_, _, reason, code, _) = veto(invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        ));
        assert_eq!(
            (reason.as_str(), code.as_deref()),
            ("proposal 1 is rejected, not pending", Some("not_pending"))
        );
    }
}

fn recording() -> RecordingPolicy {
    RecordingPolicy::new(|_| true)
}

#[test]
fn list_proposals_filters_by_state_and_pages_is_read_only_and_the_policy_is_asked_for_read() {
    for target in targets("revisions_list_proposals") {
        let policy = recording();
        let asked = policy.clone();
        let engine = published(&target, reviewed(), |options| {
            options.policy = Some(Policy::custom(policy));
        });
        for body in ["a", "b", "c", "d"] {
            propose(&engine, json!({ "body": body })).expect("propose");
        }
        invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 2}),
        )
        .expect("approve");
        invoke(&engine, &reviewer(), "d1", "reject", json!({"proposal": 3})).expect("reject");
        let seq = read(&engine, "d1").seq;
        asked.take();
        let ids = |page: &Value| -> Vec<Value> {
            page["items"]
                .as_array()
                .unwrap()
                .iter()
                .map(|item| item["id"].clone())
                .collect()
        };
        let pending = invoke(
            &engine,
            &reviewer(),
            "d1",
            "listProposals",
            json!({"state": "pending", "limit": 1}),
        )
        .expect("list");
        assert_eq!(ids(&pending), [json!(1)]);
        let next = invoke(
            &engine,
            &reviewer(),
            "d1",
            "listProposals",
            json!({"state": "pending", "cursor": pending["next"]}),
        )
        .expect("list");
        assert_eq!(json!([ids(&next), next["next"]]), json!([[4], null]));
        let all = invoke(&engine, &reviewer(), "d1", "listProposals", json!({})).expect("list");
        let states: Vec<Value> = all["items"]
            .as_array()
            .unwrap()
            .iter()
            .map(|item| json!([item["id"], item["state"]]))
            .collect();
        assert_eq!(
            states,
            [
                json!([1, "pending"]),
                json!([2, "approved"]),
                json!([3, "rejected"]),
                json!([4, "pending"])
            ]
        );
        let mut questions: Vec<String> = asked
            .take()
            .iter()
            .map(|question| format!("{} {}", question["action"], question["operation"]))
            .collect();
        questions.dedup();
        assert_eq!(questions, ["\"read\" \"listProposals\""]);
        assert_eq!(read(&engine, "d1").seq, seq);
        assert!(matches!(
            refusal(invoke(
                &engine,
                &reviewer(),
                "d1",
                "listProposals",
                json!({"state": "draft"})
            ))
            .kind(),
            EngineErrorKind::OperationParams { .. }
        ));
    }
}

#[test]
fn an_operation_that_changes_no_own_field_records_no_revision_and_one_that_does_records_it() {
    for target in targets("revisions_operations") {
        let engine = published(
            &target,
            json!([{"name": "Revisions", "config": review()}, {"name": "Comments"}]),
            |_| {},
        );
        invoke(&engine, &alice(), "d1", "post", json!({"body": "Nice."})).expect("post");
        assert_eq!(revisions_of(&engine, "d1").len(), 1);
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        let made: Vec<Value> = revisions_of(&engine, "d1")
            .iter()
            .map(|revision| json!([revision["revision"], revision.get("proposal")]))
            .collect();
        assert_eq!(made, [json!([1, null]), json!([2, 1])]);
    }
}

#[test]
fn get_revision_reads_one_revision_through_no_write_and_one_not_reached_is_not_found() {
    for target in targets("revisions_get") {
        let policy = recording();
        let asked = policy.clone();
        let engine = published(&target, json!([{"name": "Revisions"}]), |options| {
            options.clock = Clock::Manual(10);
            options.policy = Some(Policy::custom(policy));
        });
        engine.clock().set(20).expect("clock");
        update(&engine, &writer(), "d1", json!({"body": "Draft one."}));
        let seq = read(&engine, "d1").seq;
        asked.take();
        let get = |revision: Value| {
            invoke(
                &engine,
                &writer(),
                "d1",
                "getRevision",
                json!({ "revision": revision }),
            )
        };
        assert_eq!(
            get(json!(1)).expect("revision 1"),
            json!({"revision": 1, "data": {"title": "Plan"}, "createdBy": "alice", "createdAt": 10})
        );
        assert_eq!(
            get(json!(2)).expect("revision 2"),
            json!({"revision": 2, "data": {"title": "Plan", "body": "Draft one."}, "createdBy": "wes", "createdAt": 20})
        );
        let mut questions: Vec<String> = asked
            .take()
            .iter()
            .map(|question| format!("{} {}", question["action"], question["operation"]))
            .collect();
        questions.dedup();
        assert_eq!(questions, ["\"read\" \"getRevision\""]);
        assert_eq!(read(&engine, "d1").seq, seq);
        let missing = refusal(get(json!(3)));
        assert_eq!(
            (missing.code(), missing.message()),
            (
                EngineErrorCode::NotFound,
                "Document d1 has revisions 1 to 2, not revision 3"
            )
        );
        assert_eq!(
            refusal(get(json!(0))).code(),
            EngineErrorCode::InvalidArgument
        );
        // A revision number JSON writes with a point reads as the integer.
        assert_eq!(
            get(serde_json::from_str("2.0").unwrap()).expect("revision 2.0")["revision"],
            json!(2)
        );
    }
    // A proposal's revision is the approval's.
    for target in targets("revisions_get_proposal") {
        let engine = published(&target, reviewed(), |_| {});
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        assert_eq!(
            invoke(
                &engine,
                &writer(),
                "d1",
                "getRevision",
                json!({"revision": 2})
            )
            .expect("getRevision")["proposal"],
            json!(1)
        );
    }
}

#[test]
fn pending_proposals_counts_the_pending_proposals_and_is_absent_without_review() {
    for target in targets("revisions_pending") {
        let engine = published(&target, reviewed(), |_| {});
        let pending = || read(&engine, "d1").behaviors["Revisions"]["pendingProposals"].clone();
        for body in ["a", "b", "c"] {
            propose(&engine, json!({ "body": body })).expect("propose");
        }
        assert_eq!(pending(), json!(3));
        invoke(&engine, &reviewer(), "d1", "reject", json!({"proposal": 1})).expect("reject");
        invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 2}),
        )
        .expect("approve");
        assert_eq!(pending(), json!(1));
        // The reject's event carries the count it moved.
        let all = events(&engine, "d1");
        assert_eq!(
            all[all.len() - 2].change["patch"],
            json!({"behaviors": {"Revisions": {"pendingProposals": 2}}})
        );
    }
    for target in targets("revisions_pending_plain") {
        let engine = published(&target, json!([{"name": "Revisions"}]), |_| {});
        let record = read(&engine, "d1");
        assert_eq!(
            json!([record.data, record.behaviors]),
            json!([{"title": "Plan"}, {"Revisions": {"revision": 1}}])
        );
    }
}

#[test]
fn propose_takes_evidence_the_proposer_may_read_each_at_a_revision_it_has_had() {
    for target in targets("revisions_evidence") {
        let engine = published(&target, reviewed(), |options| {
            options.policy = Some(Policy::custom(RecordingPolicy::new(|request| {
                request.principal().subject != "wes" || request.schema() != Some("Secret")
            })));
        });
        for name in ["Note", "Secret"] {
            engine
                .schemas()
                .define(
                    &alice(),
                    schema_document(
                        name,
                        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
                        json!({}),
                    ),
                    DefineOptions::default(),
                )
                .expect("define");
            engine
                .schemas()
                .publish(&alice(), name, SchemaTarget::default())
                .expect("publish");
        }
        let make = |schema: &str, id: &str| {
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
        };
        make("Note", "n1");
        make("Secret", "x1");
        create(&engine, "Brief", "d2");
        update(&engine, &alice(), "d2", json!({"body": "v2"}));
        let evidence = json!([
            {"schema": "Note", "id": "n1"},
            {"schema": "Document", "id": "d2", "revision": 2}
        ]);
        let proposed = invoke(
            &engine,
            &writer(),
            "d1",
            "propose",
            json!({"patch": {"body": "Cited."}, "evidence": evidence}),
        )
        .expect("propose");
        assert_eq!(proposed["evidence"], evidence);
        let listed = invoke(&engine, &reviewer(), "d1", "listProposals", json!({})).expect("list");
        assert_eq!(listed["items"][0]["evidence"], evidence);
        // Evidence is data, not a reference: the cited note goes, the
        // proposal keeps it.
        assert!(engine
            .instances()
            .delete(&alice(), "Note", "n1", WriteOptions::default())
            .expect("delete"));
        let approved = invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        assert_eq!(approved["evidence"], evidence);
        // A proposal without evidence has none.
        assert_eq!(
            propose(&engine, json!({"body": "Bare."}))
                .expect("propose")
                .get("evidence"),
            None
        );

        let issues = |cited: Value| {
            issues_of(&refusal(invoke(
                &engine,
                &writer(),
                "d1",
                "propose",
                json!({"patch": {"body": "Again."}, "evidence": cited}),
            )))
        };
        make("Note", "n2");
        assert_eq!(
            issues(json!([{"schema": "Note", "id": "n9"}])),
            [issue("/evidence/0/id", "Note n9 does not exist")]
        );
        assert_eq!(
            issues(json!([{"schema": "Nope", "id": "n2"}])),
            [issue(
                "/evidence/0/schema",
                "Nope is not a schema of namespace default"
            )]
        );
        assert_eq!(
            issues(json!([{"schema": "Note", "id": "n2", "revision": 1}])),
            [issue(
                "/evidence/0/revision",
                "Note does not compose Revisions, so Note n2 has no revision to cite"
            )]
        );
        assert_eq!(
            issues(
                json!([{"schema": "Note", "id": "n2"}, {"schema": "Document", "id": "d2", "revision": 3}])
            ),
            [issue(
                "/evidence/1/revision",
                "Document d2 has revisions 1 to 2, not 3"
            )]
        );
        // Its paramsSchema: at most 64, no entry twice, nothing but schema,
        // id and revision.
        assert_eq!(
            issues(json!([{"schema": "Note", "id": "n2"}, {"schema": "Note", "id": "n2"}]))[0].0,
            "/evidence"
        );
        assert_eq!(
            issues(json!([{"schema": "Note", "id": "n2", "note": "x"}]))[0].0,
            "/evidence/0"
        );
        let many: Vec<Value> = (0..65)
            .map(|index| json!({"schema": "Note", "id": format!("n{index}")}))
            .collect();
        assert_eq!(issues(Value::Array(many))[0].0, "/evidence");
        // A schema the proposer may not read is forbidden, as any read of
        // it is.
        let forbidden = refusal(invoke(
            &engine,
            &writer(),
            "d1",
            "propose",
            json!({"patch": {"body": "Again."}, "evidence": [{"schema": "Secret", "id": "x1"}]}),
        ));
        assert_eq!(forbidden.code(), EngineErrorCode::Forbidden);
        // A refused proposal stores nothing.
        assert_eq!(
            read(&engine, "d1").behaviors["Revisions"]["pendingProposals"],
            json!(1)
        );
    }
}

#[test]
fn deleting_the_instance_deletes_its_revisions_and_proposals() {
    for target in targets("revisions_delete") {
        let engine = published(&target, reviewed(), |_| {});
        propose(&engine, json!({"body": "Proposed."})).expect("propose");
        assert!(engine
            .instances()
            .delete(&alice(), "Document", "d1", WriteOptions::default())
            .expect("delete"));
        create(&engine, "Plan again", "d1");
        let data: Vec<Value> = revisions_of(&engine, "d1")
            .iter()
            .map(|revision| revision["data"].clone())
            .collect();
        assert_eq!(data, [json!({"title": "Plan again"})]);
        assert_eq!(
            invoke(&engine, &alice(), "d1", "listProposals", json!({})).expect("list")["items"],
            json!([])
        );
    }
}

#[test]
fn review_may_come_and_go_and_revisions_joins_a_schema_with_instances_and_does_not_leave_one() {
    for target in targets("revisions_versions") {
        let engine = open_with(&target, |_| {});
        define(&engine, &document_schema(json!([]))).expect("define");
        publish(&engine);
        create(&engine, "Plan", "d1");
        define(&engine, &document_schema(json!([{"name": "Revisions"}]))).expect("define");
        assert_eq!(publish(&engine), 2);
        // It has no revision yet: Revisions' entry is empty.
        let joined = read(&engine, "d1");
        assert_eq!(
            json!([joined.data, joined.behaviors]),
            json!([{"title": "Plan"}, {"Revisions": {}}])
        );
        assert!(revisions_of(&engine, "d1").is_empty());
        update(&engine, &alice(), "d1", json!({"body": "Now tracked."}));
        let made: Vec<Value> = revisions_of(&engine, "d1")
            .iter()
            .map(|revision| json!([revision["revision"], revision["data"]]))
            .collect();
        assert_eq!(
            made,
            [json!([1, {"title": "Plan", "body": "Now tracked."}])]
        );

        define(&engine, &document_schema(reviewed())).expect("define");
        assert_eq!(publish(&engine), 3);
        define(
            &engine,
            &document_schema(
                json!([{"name": "Revisions", "config": {"review": {"permission": "documents.edit"}}}]),
            ),
        )
        .expect("define");
        assert_eq!(publish(&engine), 4);
        define(&engine, &document_schema(json!([{"name": "Revisions"}]))).expect("define");
        assert_eq!(publish(&engine), 5);

        let removed = refusal(define(&engine, &document_schema(json!([]))));
        assert!(
            removed.changes()[0].message.contains(
                "the revisions and proposals its instances have would stay behind with nothing to delete them"
            ),
            "{removed}"
        );
    }
}

#[test]
fn a_long_field_goes_to_the_value_store_once_and_every_read_puts_it_back() {
    for target in targets("revisions_values") {
        let engine = published(&target, reviewed(), |options| {
            options.values = superschematic_engine::ValueOptions {
                threshold_bytes: Some(1024),
                ..Default::default()
            };
        });
        let long = "x".repeat(2000);
        propose(&engine, json!({ "body": long })).expect("propose");
        invoke(
            &engine,
            &reviewer(),
            "d1",
            "approve",
            json!({"proposal": 1}),
        )
        .expect("approve");
        let revisions = revisions_of(&engine, "d1");
        assert_eq!(revisions[1]["data"]["body"], json!(long));
        let proposals = invoke(&engine, &alice(), "d1", "listProposals", json!({})).expect("list");
        assert_eq!(proposals["items"][0]["patch"]["body"], json!(long));
        assert_eq!(
            invoke(
                &engine,
                &alice(),
                "d1",
                "getRevision",
                json!({"revision": 2})
            )
            .expect("getRevision")["data"]["body"],
            json!(long)
        );
        // The rows keep a ref, not the text.
        let table_text = support::texts(
            &engine,
            "SELECT data FROM bhv_revisions__revisions WHERE revision = 2",
            &[],
        );
        assert!(!table_text[0].contains(&long), "{}", table_text[0]);
        // The delete releases what the rows held.
        assert!(engine
            .instances()
            .delete(&alice(), "Document", "d1", WriteOptions::default())
            .expect("delete"));
        assert_eq!(
            support::count(&engine, "SELECT COUNT(*) FROM bhv_revisions__revisions"),
            0
        );
    }
}
