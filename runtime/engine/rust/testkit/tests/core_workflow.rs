//! Workflow, the core's state machine: the cases of
//! `test/core-workflow.test.ts` (its config, the transition operation and
//! its guard, which no caller and no other behavior gets past, its
//! permission gates, its events, when the status last changed, its rule for
//! a new version, and the outcomes of its terminal states), on SQLite and,
//! with the variable set, on Postgres. The permission matcher a deployment
//! passes, which the corpus cannot record, runs here too.

mod support;

use std::sync::Arc;

use serde_json::{json, Value};

use superschematic_engine::storage::Lock;
use superschematic_engine::{
    Clock, DefineOptions, Engine, EngineError, EngineErrorCode, EngineErrorKind, Error,
    InstanceRecord, ListOptions, PermissionMatcher, Principal, ReadEvents, SchemaTarget,
    ValidateOptions, WriteOptions,
};
use superschematic_engine_behaviors::{is_terminal_state, state_outcome, WorkflowOutcome};
use superschematic_engine_testkit::behaviors::Dispatch;
use support::{
    alice, defect, fields_of, issue, issues_of, refusal, schema_document, targets, veto, Target,
};

const START: i64 = 1_000_000;

// An order moves from draft to open, then ships (which needs orders.ship)
// or is cancelled; shipped and cancelled are terminal.
fn order_flow() -> Value {
    json!({
        "states": ["draft", "open", "shipped", "cancelled"],
        "transitions": [
            {"from": "draft", "to": "open"},
            {"from": "draft", "to": "cancelled"},
            {"from": "open", "to": "shipped", "permission": "orders.ship"},
            {"from": "open", "to": "cancelled"}
        ]
    })
}

// order_flow with members changed.
fn flow_with(members: Value) -> Value {
    let mut flow = order_flow();
    for (key, value) in members.as_object().expect("an object") {
        flow[key] = value.clone();
    }
    flow
}

fn order_schema(config: &Value, extra: &[Value]) -> Value {
    let mut document = schema_document(
        "Order",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    let mut behaviors = vec![json!({"name": "Workflow", "config": config})];
    behaviors.extend(extra.iter().cloned());
    document["types"]["Order"]["behaviors"] = Value::Array(behaviors);
    document
}

fn shipper() -> Principal {
    Principal::new("sam", ["orders.ship"])
}

fn open(target: &Target) -> Engine {
    target.open_core_with(|options| options.clock = Clock::Manual(START))
}

fn define(engine: &Engine, document: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

fn publish(engine: &Engine, document: &Value) -> i64 {
    define(engine, document).expect("define");
    engine
        .schemas()
        .publish(&alice(), "Order", SchemaTarget::default())
        .expect("publish")
        .version
}

fn create(engine: &Engine, data: Value, id: Option<&str>) -> Result<InstanceRecord, Error> {
    engine.instances().create(
        &alice(),
        "Order",
        &data,
        superschematic_engine::CreateOptions {
            id,
            ..Default::default()
        },
    )
}

// published opens an engine, publishes Order with a config and creates o1.
fn published(target: &Target, config: &Value) -> Engine {
    let engine = open(target);
    publish(&engine, &order_schema(config, &[]));
    create(&engine, json!({"title": "Desk"}), Some("o1")).expect("create");
    engine
}

fn invoke(
    engine: &Engine,
    principal: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        principal,
        "Order",
        "o1",
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn transition(engine: &Engine, principal: &Principal, to: &str) -> Result<Value, Error> {
    invoke(engine, principal, "transition", json!({ "to": to }))
}

fn get(engine: &Engine, id: &str) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Order", id, Default::default())
        .expect("get")
        .expect("the instance")
}

fn update(engine: &Engine, patch: Value) -> Result<InstanceRecord, Error> {
    engine
        .instances()
        .update(&alice(), "Order", "o1", &patch, WriteOptions::default())
}

fn changes(engine: &Engine) -> Vec<Value> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Order"),
                instance_id: Some("o1"),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .into_iter()
        .map(|event| event.change)
        .collect()
}

fn instance_issues(error: &EngineError) -> Vec<(String, String, String)> {
    match error.kind() {
        EngineErrorKind::InstanceValidation { issues } => issues
            .iter()
            .map(|issue| {
                (
                    issue.path.clone(),
                    issue.rule.clone(),
                    issue.message.clone(),
                )
            })
            .collect(),
        other => panic!("expected an InstanceValidationError, got {other:?}"),
    }
}

fn composed_config(engine: &Engine) -> Value {
    let composed = engine
        .schemas()
        .behaviors(&alice(), "Order", ValidateOptions::default())
        .expect("behaviors");
    assert_eq!(composed[0].name, "Workflow");
    composed[0].config.clone()
}

#[test]
fn a_new_instance_starts_in_the_initial_state_entered_at_its_create() {
    for target in targets("workflow_initial") {
        let engine = published(&target, &order_flow());
        assert_eq!(
            fields_of(&get(&engine, "o1")),
            json!({"data": {"title": "Desk"}, "behaviors": {"Workflow": {"status": "draft", "enteredAt": START}}})
        );
    }
    // The first state when the config names none; initial when it does.
    for target in targets("workflow_initial_named") {
        let engine = published(&target, &flow_with(json!({"initial": "open"})));
        assert_eq!(
            get(&engine, "o1").behaviors["Workflow"]["status"],
            json!("open")
        );
        assert_eq!(
            changes(&engine),
            [
                json!({"data": {"title": "Desk"}, "behaviors": {"Workflow": {"status": "open", "enteredAt": START}}})
            ]
        );
    }
}

#[test]
fn transition_moves_the_status_and_its_event_carries_the_status_and_when_it_was_entered() {
    for target in targets("workflow_transition") {
        let engine = published(&target, &order_flow());
        let now = engine.clock().advance(5_000).expect("advance");
        assert_eq!(
            transition(&engine, &alice(), "open").expect("transition"),
            json!({"from": "draft", "to": "open"})
        );
        let order = get(&engine, "o1");
        assert_eq!(
            (order.behaviors["Workflow"].clone(), order.seq),
            (json!({"status": "open", "enteredAt": now}), 2)
        );
        assert_eq!(
            changes(&engine).last().cloned(),
            Some(json!({
                "behavior": "Workflow",
                "operation": "transition",
                "params": {"to": "open"},
                "patch": {"behaviors": {"Workflow": {"status": "open", "enteredAt": now}}}
            }))
        );
        // A write that leaves the status alone leaves enteredAt where it is.
        engine.clock().advance(5_000).expect("advance");
        assert_eq!(
            update(&engine, json!({"title": "Oak desk"}))
                .expect("update")
                .behaviors["Workflow"],
            json!({"status": "open", "enteredAt": now})
        );
    }
}

#[test]
fn an_instance_from_before_workflow_kept_entered_at_has_none_until_its_next_transition() {
    for target in targets("workflow_entered_at") {
        let engine = published(&target, &order_flow());
        // As a row migration 3 found: the column is there, with no time in it.
        engine
            .db()
            .write(Lock::Catalog, |tx| {
                tx.run(
                    "UPDATE engine_instances SET bhv_workflow__entered_at = NULL WHERE schema = 'Order' AND id = 'o1'",
                    &[],
                )
            })
            .expect("clear entered_at");
        assert_eq!(
            get(&engine, "o1").behaviors["Workflow"],
            json!({"status": "draft"})
        );
        let now = engine.clock().advance(1_000).expect("advance");
        transition(&engine, &alice(), "open").expect("transition");
        assert_eq!(
            get(&engine, "o1").behaviors["Workflow"],
            json!({"status": "open", "enteredAt": now})
        );
    }
}

#[test]
fn its_guard_refuses_an_unknown_state_the_same_state_an_unlisted_move_and_a_move_out_of_a_terminal_state(
) {
    for target in targets("workflow_guard") {
        let engine = published(&target, &order_flow());
        let unknown = refusal(transition(&engine, &alice(), "lost"));
        assert_eq!(unknown.name(), "OperationParamsError");
        assert_eq!(
            issues_of(&unknown),
            [issue(
                "/to",
                "\"lost\" is not a state of Order (draft, open, shipped, cancelled)"
            )]
        );
        assert_eq!(
            veto(transition(&engine, &alice(), "draft")),
            (
                "Workflow".to_owned(),
                "transition".to_owned(),
                "Order o1 is already draft".to_owned(),
                Some("already_in_state".to_owned()),
                Some(json!({"from": "draft", "to": "draft"}))
            )
        );
        let skip = veto(transition(&engine, &alice(), "shipped"));
        assert_eq!(
            (skip.2.as_str(), skip.3.as_deref(), skip.4),
            (
                "no transition leads from draft to shipped; from draft it can move to open, cancelled",
                Some("transition_not_allowed"),
                Some(json!({"from": "draft", "to": "shipped", "allowed": ["open", "cancelled"]}))
            )
        );
        transition(&engine, &alice(), "cancelled").expect("cancel");
        let terminal = veto(transition(&engine, &alice(), "open"));
        assert_eq!(
            (terminal.2.as_str(), terminal.3.as_deref()),
            (
                "cancelled is a terminal state: no transition leaves it",
                Some("terminal_state")
            )
        );
        assert_eq!(get(&engine, "o1").seq, 2);
    }
}

// A deployment's matcher: whoever holds root holds every permission.
struct Rooted;

impl PermissionMatcher for Rooted {
    fn matches(&self, held: &[String], _required: &[String]) -> bool {
        held.iter().any(|permission| permission == "root")
    }
}

#[test]
fn a_transition_that_names_a_permission_needs_a_caller_who_holds_it() {
    for target in targets("workflow_permission") {
        let engine = published(&target, &order_flow());
        transition(&engine, &alice(), "open").expect("open");
        let refused = refusal(transition(&engine, &alice(), "shipped"));
        assert_eq!(
            (refused.code(), refused.message()),
            (
                EngineErrorCode::Forbidden,
                "alice may not move Order o1 from open to shipped: the transition needs permission orders.ship"
            )
        );
        assert_eq!(
            transition(&engine, &Principal::new("olga", ["orders"]), "shipped").expect("ship"),
            json!({"from": "open", "to": "shipped"})
        );
    }
    // A deployment's matcher decides instead of the HTTP runtime's rule.
    for target in targets("workflow_permission_matcher") {
        let engine = target.open_core_with(|options| {
            options.permission_matcher = Some(Arc::new(Rooted));
        });
        publish(&engine, &order_schema(&order_flow(), &[]));
        create(&engine, json!({"title": "Desk"}), Some("o1")).expect("create");
        transition(&engine, &alice(), "open").expect("open");
        assert_eq!(
            refusal(transition(&engine, &shipper(), "shipped")).code(),
            EngineErrorCode::Forbidden
        );
        transition(&engine, &Principal::new("rita", ["root"]), "shipped").expect("ship");
    }
}

#[test]
fn transition_takes_to_and_nothing_else() {
    for target in targets("workflow_params") {
        let engine = published(&target, &order_flow());
        let alias = refusal(invoke(
            &engine,
            &alice(),
            "transition",
            json!({"status": "open"}),
        ));
        assert_eq!(alias.name(), "OperationParamsError");
        assert_eq!(
            issues_of(&alias),
            [
                issue("", "must NOT have additional properties: status"),
                issue("", "must have required property 'to'")
            ]
        );
        assert_eq!(
            refusal(invoke(
                &engine,
                &alice(),
                "transition",
                json!({"to": "open", "status": "shipped"}),
            ))
            .name(),
            "OperationParamsError"
        );
        assert_eq!(
            get(&engine, "o1").behaviors["Workflow"]["status"],
            json!("draft")
        );
    }
}

#[test]
fn the_status_is_not_a_field_a_create_or_an_update_sets() {
    for target in targets("workflow_not_data") {
        let engine = published(&target, &order_flow());
        let created = refusal(create(
            &engine,
            json!({"title": "Lamp", "status": "shipped"}),
            None,
        ));
        assert_eq!(
            instance_issues(&created),
            [(
                "status".to_owned(),
                "unknown".to_owned(),
                "Order has no field status".to_owned()
            )]
        );
        let updated = refusal(update(&engine, json!({"status": "shipped"})));
        assert_eq!(
            instance_issues(&updated)
                .into_iter()
                .map(|(path, rule, _)| (path, rule))
                .collect::<Vec<_>>(),
            [("status".to_owned(), "unknown".to_owned())]
        );
        // A patch that removes an own status the order does not have
        // changes nothing.
        assert_eq!(
            update(&engine, json!({"status": null}))
                .expect("update")
                .seq,
            1
        );
        assert_eq!(
            get(&engine, "o1").behaviors["Workflow"]["status"],
            json!("draft")
        );
    }
}

#[test]
fn an_own_field_named_status_sits_beside_workflows() {
    for target in targets("workflow_own_status") {
        let engine = open(&target);
        let mut document = order_schema(&order_flow(), &[]);
        document["types"]["Order"]["fields"]
            .as_array_mut()
            .expect("fields")
            .push(json!({"name": "status", "typeRef": {"name": "string"}}));
        publish(&engine, &document);
        create(
            &engine,
            json!({"title": "Desk", "status": "paid"}),
            Some("o1"),
        )
        .expect("o1");
        create(
            &engine,
            json!({"title": "Lamp", "status": "unpaid"}),
            Some("o2"),
        )
        .expect("o2");
        engine
            .instances()
            .invoke(
                &alice(),
                "Order",
                "o2",
                "transition",
                &json!({"to": "open"}),
                WriteOptions::default(),
            )
            .expect("transition");
        // An update writes the own status and leaves Workflow's where it is.
        let desk = update(&engine, json!({"status": "refunded"})).expect("update");
        assert_eq!(
            (
                Value::Object(desk.data),
                desk.behaviors["Workflow"]["status"].clone()
            ),
            (
                json!({"title": "Desk", "status": "refunded"}),
                json!("draft")
            )
        );
        let lamp = get(&engine, "o2");
        assert_eq!(
            (
                Value::Object(lamp.data),
                lamp.behaviors["Workflow"]["status"].clone()
            ),
            (json!({"title": "Lamp", "status": "unpaid"}), json!("open"))
        );
        // A bare key filters on the own field, the qualified name on
        // Workflow's.
        let ids = |filter: Value| -> Vec<String> {
            engine
                .instances()
                .list(
                    &alice(),
                    "Order",
                    ListOptions {
                        filter: Some(&filter),
                        ..ListOptions::default()
                    },
                )
                .expect("list")
                .items
                .into_iter()
                .map(|item| item.id)
                .collect()
        };
        assert_eq!(ids(json!({"status": "unpaid"})), ["o2"]);
        assert_eq!(ids(json!({"Workflow.status": "draft"})), ["o1"]);
        assert_eq!(
            ids(json!({"status": "refunded", "Workflow.status": "draft"})),
            ["o1"]
        );
        assert!(ids(json!({"status": "draft"})).is_empty());
    }
}

#[test]
fn another_behavior_moves_the_status_only_through_transition() {
    for target in targets("workflow_dispatch") {
        let engine = target.open_with(
            superschematic_engine_behaviors::core().with(Dispatch),
            |options| options.clock = Clock::Manual(START),
        );
        publish(
            &engine,
            &order_schema(&order_flow(), &[json!({"name": "test.Dispatch"})]),
        );
        create(&engine, json!({"title": "Desk"}), Some("o1")).expect("create");
        let dispatch = |principal: &Principal, to: &str| {
            invoke(&engine, principal, "dispatch", json!({ "to": to }))
        };

        let skip = veto(dispatch(&alice(), "shipped"));
        assert_eq!(
            (skip.0.as_str(), skip.2.as_str()),
            (
                "Workflow",
                "no transition leads from draft to shipped; from draft it can move to open, cancelled"
            )
        );
        let opened = engine.clock().advance(1_000).expect("advance");
        assert_eq!(
            dispatch(&alice(), "open").expect("open"),
            json!({"from": "draft", "to": "open"})
        );
        assert_eq!(
            refusal(dispatch(&alice(), "shipped")).code(),
            EngineErrorCode::Forbidden
        );
        let shipped = engine.clock().advance(1_000).expect("advance");
        assert_eq!(
            dispatch(&shipper(), "shipped").expect("ship"),
            json!({"from": "open", "to": "shipped"})
        );
        // It has no handle on Workflow's column.
        let (behavior, message) = defect(invoke(&engine, &alice(), "forceStatus", json!({})));
        assert_eq!(behavior, "test.Dispatch");
        assert!(message.contains("SQL refused"), "{message}");
        assert_eq!(
            changes(&engine)[1..],
            [
                json!({"behavior": "test.Dispatch", "operation": "dispatch", "params": {"to": "open"}, "patch": {"behaviors": {"Workflow": {"status": "open", "enteredAt": opened}}}}),
                json!({"behavior": "test.Dispatch", "operation": "dispatch", "params": {"to": "shipped"}, "patch": {"behaviors": {"Workflow": {"status": "shipped", "enteredAt": shipped}}}}),
            ]
        );
    }
}

#[test]
fn define_refuses_a_config_its_schema_lets_through_that_is_not_a_state_machine() {
    for target in targets("workflow_config") {
        let engine = open(&target);
        let more = order_flow()["transitions"]
            .as_array()
            .expect("transitions")
            .clone();
        let with_transition = |transition: Value| {
            let mut transitions = more.clone();
            transitions.push(transition);
            flow_with(json!({ "transitions": transitions }))
        };
        let refusals = [
            (
                flow_with(json!({"initial": "lost"})),
                "the initial state \"lost\" is not one of its states (draft, open, shipped, cancelled)",
            ),
            (
                with_transition(json!({"from": "open", "to": "held"})),
                "the transition from \"open\" to \"held\" names \"held\", which is not one of its states (draft, open, shipped, cancelled)",
            ),
            (
                with_transition(json!({"from": "open", "to": "open"})),
                "a transition from \"open\" to itself changes nothing",
            ),
            (
                with_transition(json!({"from": "draft", "to": "open"})),
                "the transition from \"draft\" to \"open\" is listed twice",
            ),
            (
                flow_with(json!({"outcomes": {"lost": "failure"}})),
                "outcomes names \"lost\", which is not one of its states (draft, open, shipped, cancelled)",
            ),
            (
                flow_with(json!({"outcomes": {"shipped": "success", "open": "failure"}})),
                "outcomes names \"open\", which is not a terminal state: a transition leaves it, and only a terminal state has an outcome",
            ),
        ];
        for (config, message) in refusals {
            let error = refusal(define(&engine, &order_schema(&config, &[])));
            assert_eq!(error.name(), "SchemaDocumentError");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Order/behaviors/0/config",
                    &format!("type Order: behavior Workflow config: {message}")
                )]
            );
        }
        // What the declaration's config schema refuses, the loader refuses
        // first.
        let mut unconfigured = order_schema(&order_flow(), &[]);
        unconfigured["types"]["Order"]["behaviors"][0]
            .as_object_mut()
            .expect("the behavior")
            .shift_remove("config");
        for document in [
            order_schema(&json!({"states": [], "transitions": []}), &[]),
            order_schema(&json!({"states": ["in review"], "transitions": []}), &[]),
            order_schema(&flow_with(json!({"terminal": ["shipped"]})), &[]),
            order_schema(
                &flow_with(json!({"outcomes": {"cancelled": "aborted"}})),
                &[],
            ),
            order_schema(&flow_with(json!({"outcomes": {}})), &[]),
            unconfigured,
        ] {
            let error = refusal(define(&engine, &document));
            assert_eq!(error.name(), "SchemaDocumentError");
            assert!(
                error.issues()[0]
                    .path
                    .starts_with("/types/Order/behaviors/0"),
                "{:?}",
                error.issues()
            );
        }
        assert!(engine
            .schemas()
            .list(&alice(), SchemaTarget::default())
            .expect("list")
            .is_empty());
    }
}

#[test]
fn a_new_version_keeps_every_state_and_may_change_the_rest() {
    for target in targets("workflow_versions") {
        let engine = published(&target, &order_flow());
        let version = |config: Value| -> Result<i64, Error> {
            define(&engine, &order_schema(&config, &[]))?;
            Ok(engine
                .schemas()
                .publish(&alice(), "Order", SchemaTarget::default())?
                .version)
        };
        assert_eq!(
            version(json!({
                "states": ["draft", "open", "shipped", "cancelled", "held"],
                "initial": "open",
                "transitions": [
                    {"from": "draft", "to": "open"},
                    {"from": "draft", "to": "cancelled"},
                    {"from": "open", "to": "shipped"},
                    {"from": "open", "to": "held"},
                    {"from": "held", "to": "open"}
                ]
            }))
            .expect("version 2"),
            2
        );
        assert_eq!(
            transition(&engine, &alice(), "open").expect("open"),
            json!({"from": "draft", "to": "open"})
        );
        assert_eq!(
            transition(&engine, &alice(), "shipped").expect("ship"),
            json!({"from": "open", "to": "shipped"})
        );
        assert_eq!(
            create(&engine, json!({"title": "Lamp"}), None)
                .expect("create")
                .behaviors["Workflow"]["status"],
            json!("open")
        );

        let dropped = refusal(version(json!({
            "states": ["draft", "open", "shipped"],
            "transitions": [{"from": "draft", "to": "open"}, {"from": "open", "to": "shipped"}]
        })));
        assert_eq!(dropped.name(), "IncompatibleChangeError");
        assert_eq!(
            dropped
                .changes()
                .iter()
                .map(|change| change.path.as_str())
                .collect::<Vec<_>>(),
            ["Order.behaviors.Workflow"]
        );
        assert!(
            dropped.changes()[0].message.contains(
                "an instance may be in \"cancelled\", \"held\", which the new config drops"
            ),
            "{}",
            dropped.changes()[0].message
        );

        let mut plain = order_schema(&order_flow(), &[]);
        plain["types"]["Order"]
            .as_object_mut()
            .expect("the type")
            .shift_remove("behaviors");
        let removed = refusal(define(&engine, &plain));
        assert!(
            removed.changes()[0]
                .message
                .contains("the instances would lose their status"),
            "{}",
            removed.changes()[0].message
        );
    }
}

#[test]
fn with_no_instance_a_new_version_may_drop_a_state() {
    for target in targets("workflow_drop") {
        let engine = published(&target, &order_flow());
        let version = |config: &Value| -> Result<i64, Error> {
            define(&engine, &order_schema(config, &[]))?;
            Ok(engine
                .schemas()
                .publish(&alice(), "Order", SchemaTarget::default())?
                .version)
        };
        let fewer = json!({
            "states": ["draft", "open", "shipped"],
            "transitions": [{"from": "draft", "to": "open"}, {"from": "open", "to": "shipped"}]
        });
        assert!(refusal(version(&fewer)).changes()[0]
            .message
            .contains("an instance may be in \"cancelled\", which the new config drops"));
        engine
            .instances()
            .delete(&alice(), "Order", "o1", WriteOptions::default())
            .expect("delete");
        assert_eq!(version(&fewer).expect("version 2"), 2);
        assert_eq!(
            version(&json!({"states": ["draft", "open"], "transitions": [{"from": "draft", "to": "open"}]}))
                .expect("version 3"),
            3
        );
    }
}

#[test]
fn workflow_joins_a_schema_with_no_instances_and_not_one_that_has_them() {
    let mut plain = order_schema(&order_flow(), &[]);
    plain["types"]["Order"]
        .as_object_mut()
        .expect("the type")
        .shift_remove("behaviors");
    for target in targets("workflow_join") {
        let engine = open(&target);
        assert_eq!(publish(&engine, &plain), 1);
        assert_eq!(publish(&engine, &order_schema(&order_flow(), &[])), 2);
    }
    for target in targets("workflow_join_populated") {
        let engine = open(&target);
        publish(&engine, &plain);
        create(&engine, json!({"title": "Desk"}), None).expect("create");
        let added = refusal(define(&engine, &order_schema(&order_flow(), &[])));
        assert!(
            added.changes()[0]
                .message
                .contains("the instances that exist have no status to start from"),
            "{}",
            added.changes()[0].message
        );
    }
}

#[test]
fn is_terminal_state_is_a_state_no_transition_leaves() {
    let flow = order_flow();
    assert!(is_terminal_state(&flow, "shipped"));
    assert!(is_terminal_state(&flow, "cancelled"));
    assert!(!is_terminal_state(&flow, "open"));
    assert!(!is_terminal_state(&flow, "lost"));
    for target in targets("workflow_terminal") {
        let engine = published(&target, &order_flow());
        assert!(!is_terminal_state(&composed_config(&engine), "draft"));
    }
}

#[test]
fn state_outcome_is_the_outcome_outcomes_names_or_success() {
    let flow = flow_with(json!({"outcomes": {"cancelled": "failure"}}));
    assert_eq!(
        state_outcome(&flow, "cancelled"),
        Some(WorkflowOutcome::Failure)
    );
    assert_eq!(
        state_outcome(&flow, "shipped"),
        Some(WorkflowOutcome::Success)
    );
    assert_eq!(state_outcome(&flow, "open"), None);
    assert_eq!(state_outcome(&flow, "lost"), None);
    // A config without outcomes means what it did before them.
    assert_eq!(
        state_outcome(&order_flow(), "cancelled"),
        Some(WorkflowOutcome::Success)
    );
    for target in targets("workflow_outcome") {
        let engine = published(&target, &flow);
        // The config as the schema holds it, which state_outcome reads.
        let config = composed_config(&engine);
        assert_eq!(config, flow);
        assert_eq!(
            state_outcome(&config, "cancelled"),
            Some(WorkflowOutcome::Failure)
        );
        // Workflow reads no outcome itself: a move into a failure state is a
        // move like any other.
        assert_eq!(
            transition(&engine, &alice(), "cancelled").expect("cancel"),
            json!({"from": "draft", "to": "cancelled"})
        );
    }
}

#[test]
fn a_new_version_may_change_outcomes_and_readers_see_the_new_one() {
    for target in targets("workflow_outcomes_change") {
        let engine = published(
            &target,
            &flow_with(json!({"outcomes": {"cancelled": "failure"}})),
        );
        transition(&engine, &alice(), "cancelled").expect("cancel");
        for outcomes in [
            Some(json!({"cancelled": "neutral"})),
            Some(json!({"cancelled": "neutral", "shipped": "success"})),
            None,
        ] {
            let config = match outcomes {
                Some(outcomes) => flow_with(json!({ "outcomes": outcomes })),
                None => order_flow(),
            };
            publish(&engine, &order_schema(&config, &[]));
        }
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "Order", SchemaTarget::default())
                .expect("live")
                .expect("a live version")
                .version,
            Some(4)
        );
        assert_eq!(
            state_outcome(&composed_config(&engine), "cancelled"),
            Some(WorkflowOutcome::Success)
        );
        // A transition out of a state with an outcome makes it not
        // terminal, which a new version must say by dropping the outcome.
        let mut transitions = order_flow()["transitions"]
            .as_array()
            .expect("transitions")
            .clone();
        transitions.push(json!({"from": "cancelled", "to": "draft"}));
        let reopened =
            flow_with(json!({"transitions": transitions, "outcomes": {"cancelled": "failure"}}));
        let error = refusal(define(&engine, &order_schema(&reopened, &[])));
        assert!(
            error
                .message()
                .contains("outcomes names \"cancelled\", which is not a terminal state"),
            "{}",
            error.message()
        );
    }
}
