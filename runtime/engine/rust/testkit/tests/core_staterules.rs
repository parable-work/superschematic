//! StateRules, the own fields a state of the type's Workflow requires or
//! freezes (D56): the cases of `test/core-staterules.test.ts` (a transition
//! into a state without a field it requires is vetoed, `missing_fields`,
//! and a create whose initial state requires one is invalid_instance at the
//! field; while the instance is in a state, an update that clears a
//! required field, `required_fields`, or changes a frozen one,
//! `frozen_fields`, is vetoed, a caller's and a behavior's `update()`
//! alike, the frozen rule lifted by the config's permission alone; absent
//! and null are not held, `""` and `[]` are; `"all"` freezes every own
//! field; any config change is allowed, an instance that predates a
//! requirement keeping on), on SQLite and, with the variable set, on
//! Postgres. The case that approves a Revisions proposal waits for
//! Revisions (wave 2a); the guidance, which milestone 6's describe document
//! shows, is held to the TypeScript text in `behaviors/tests/guidance.rs`.

mod support;

use serde_json::{json, Map, Value};

use superschematic_engine::{
    BehaviorSet, CreateOptions, DefineOptions, Engine, EngineErrorKind, Error, InstanceRecord,
    Principal, ReadEvents, SchemaTarget, WriteOptions,
};
use superschematic_engine_testkit::core_fixtures::Recorder;
use support::{alice, issues_of, refusal, repository, targets, veto, Target};

fn clerk() -> Principal {
    Principal::new("cleo", Vec::<String>::new())
}

fn amender() -> Principal {
    Principal::new("amos", ["invoices.amend"])
}

/// The core's behaviors, as the TypeScript engine registers them.
fn core() -> BehaviorSet {
    superschematic_engine_behaviors::core().with(superschematic_engine_behaviors::StateRules)
}

fn open(target: &Target) -> Engine {
    target.open_core_with(|options| options.behaviors = core())
}

/// An engine with the open meta-schema and test.Recorder beside the core.
fn open_recording(target: &Target) -> Engine {
    target.open(core().with(Recorder))
}

/// The fixture's invoices (fixture-staterules-json).
fn invoices_document() -> Value {
    let path = repository()
        .join("internal/loader/testdata/services/fixture-staterules-json/src/invoice.schema.json");
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the invoices document"))
        .expect("JSON")
}

/// The fixture's invoices, its StateRules config replaced (`Some`), or
/// removed (`Some(Value::Null)`), and behaviors added after it.
fn invoices(rules: Option<Value>, extra: &[Value]) -> Value {
    let mut document = invoices_document();
    let behaviors = document["types"]["Invoice"]["behaviors"]
        .as_array_mut()
        .expect("behaviors");
    let at = behaviors
        .iter()
        .position(|behavior| behavior["name"] == "StateRules")
        .expect("StateRules");
    match rules {
        Some(Value::Null) => {
            behaviors.remove(at);
        }
        Some(rules) => behaviors[at] = json!({"name": "StateRules", "config": rules}),
        None => {}
    }
    behaviors.extend(extra.iter().cloned());
    document
}

fn publish(engine: &Engine, document: &Value) {
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), "invoices", SchemaTarget::default())
        .expect("publish");
}

fn create(engine: &Engine, principal: &Principal, data: Value, id: &str) -> InstanceRecord {
    engine
        .instances()
        .create(
            principal,
            "invoices",
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create")
}

fn get(engine: &Engine, id: &str) -> Option<InstanceRecord> {
    engine
        .instances()
        .get(&alice(), "invoices", id, Default::default())
        .expect("get")
}

fn status_of(engine: &Engine, id: &str) -> Value {
    get(engine, id).expect("the instance").behaviors["Workflow"]["status"].clone()
}

fn data_of(engine: &Engine, id: &str) -> Value {
    Value::Object(get(engine, id).expect("the instance").data)
}

fn update(
    engine: &Engine,
    principal: &Principal,
    id: &str,
    patch: Value,
) -> Result<InstanceRecord, Error> {
    engine
        .instances()
        .update(principal, "invoices", id, &patch, WriteOptions::default())
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
        "invoices",
        id,
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn move_to(engine: &Engine, principal: &Principal, id: &str, to: &str) -> Result<Value, Error> {
    invoke(engine, principal, id, "transition", json!({ "to": to }))
}

/// The behavior, action, code and details of the veto a call meets.
fn veto_of<T: std::fmt::Debug>(result: Result<T, Error>) -> (String, String, String, Value) {
    let (behavior, action, _, code, details) = veto(result);
    (
        behavior,
        action,
        code.unwrap_or_default(),
        details.unwrap_or(Value::Null),
    )
}

fn vetoed(
    behavior: &str,
    action: &str,
    code: &str,
    details: Value,
) -> (String, String, String, Value) {
    (
        behavior.to_owned(),
        action.to_owned(),
        code.to_owned(),
        details,
    )
}

fn full() -> Value {
    json!({"title": "March", "customer": "Acme", "amount": 1200, "lines": ["Consulting"]})
}

// with merges members into an object.
fn with(base: Value, members: Value) -> Value {
    let mut out: Map<String, Value> = base.as_object().cloned().unwrap_or_default();
    for (key, value) in members.as_object().expect("an object") {
        out.insert(key.clone(), value.clone());
    }
    Value::Object(out)
}

#[test]
fn a_transition_into_a_state_that_requires_a_field_the_instance_does_not_hold_is_vetoed() {
    for target in targets("staterules_missing") {
        let engine = open(&target);
        publish(&engine, &invoices(None, &[]));
        create(&engine, &clerk(), json!({"title": "March"}), "i1");
        let refused = refusal(move_to(&engine, &clerk(), "i1", "sent"));
        assert_eq!(refused.code().as_str(), "vetoed");
        assert_eq!(refused.status(), 409);
        assert_eq!(
            refused.message(),
            "behavior StateRules vetoes transition of invoices i1: invoices i1 cannot move to sent without customer, amount and lines, which sent requires"
        );
        assert_eq!(
            veto_of(move_to(&engine, &clerk(), "i1", "sent")),
            vetoed(
                "StateRules",
                "transition",
                "missing_fields",
                json!({"state": "sent", "fields": ["customer", "amount", "lines"]})
            )
        );
        // Null is not held; "" and [] and 0 are.
        update(
            &engine,
            &clerk(),
            "i1",
            json!({"customer": "", "amount": null, "lines": []}),
        )
        .expect("update");
        assert_eq!(
            veto_of(move_to(&engine, &clerk(), "i1", "sent")),
            vetoed(
                "StateRules",
                "transition",
                "missing_fields",
                json!({"state": "sent", "fields": ["amount"]})
            )
        );
        update(&engine, &clerk(), "i1", json!({"amount": 0})).expect("update");
        assert_eq!(
            move_to(&engine, &clerk(), "i1", "sent").expect("sent"),
            json!({"from": "draft", "to": "sent"})
        );
        assert_eq!(status_of(&engine, "i1"), "sent");
        // A state with no requirement takes any instance, and so does a
        // state the config does not name.
        create(&engine, &clerk(), json!({"title": "April"}), "i2");
        assert_eq!(
            move_to(&engine, &clerk(), "i2", "void").expect("void"),
            json!({"from": "draft", "to": "void"})
        );
    }
}

#[test]
fn an_operations_transition_reads_the_fields_as_its_update_leaves_them() {
    for target in targets("staterules_operation") {
        let engine = open_recording(&target);
        publish(
            &engine,
            &invoices(None, &[json!({"name": "test.Recorder"})]),
        );
        create(&engine, &clerk(), json!({"title": "March"}), "i1");
        let record = |params: Value| invoke(&engine, &clerk(), "i1", "record", params);
        assert_eq!(
            veto_of(record(json!({"patch": {"customer": "Acme"}, "to": "sent"}))),
            vetoed(
                "StateRules",
                "transition",
                "missing_fields",
                json!({"state": "sent", "fields": ["amount", "lines"]})
            )
        );
        // The veto rolls the whole call back, the update() before it
        // included.
        assert_eq!(data_of(&engine, "i1"), json!({"title": "March"}));
        record(json!({"patch": {"customer": "Acme", "amount": 1200, "lines": ["Consulting"]}, "to": "sent"}))
            .expect("record");
        assert_eq!(status_of(&engine, "i1"), "sent");
    }
}

#[test]
fn while_an_instance_is_in_a_state_an_update_that_changes_a_frozen_field_or_clears_a_required_one_is_vetoed(
) {
    for target in targets("staterules_update") {
        let engine = open(&target);
        publish(&engine, &invoices(None, &[]));
        create(&engine, &clerk(), full(), "i1");
        // In draft nothing is frozen.
        update(&engine, &clerk(), "i1", json!({"amount": 1500})).expect("update");
        move_to(&engine, &clerk(), "i1", "sent").expect("sent");
        let frozen = refusal(update(&engine, &clerk(), "i1", json!({"amount": 1600})));
        assert_eq!(
            frozen.message(),
            "behavior StateRules vetoes update of invoices i1: invoices i1 is sent, which freezes amount: it does not change while it is sent; only a caller with invoices.amend may change it"
        );
        assert_eq!(
            veto_of(update(&engine, &clerk(), "i1", json!({"amount": 1600}))),
            vetoed(
                "StateRules",
                "update",
                "frozen_fields",
                json!({"state": "sent", "fields": ["amount"]})
            )
        );
        // A change inside a list is a change, and every frozen field it
        // changes is named.
        assert_eq!(
            veto_of(update(
                &engine,
                &clerk(),
                "i1",
                json!({"lines": ["Consulting", "Travel"], "customer": "Acme Ltd"})
            )),
            vetoed(
                "StateRules",
                "update",
                "frozen_fields",
                json!({"state": "sent", "fields": ["customer", "lines"]})
            )
        );
        // Clearing a required field is required_fields, checked first, and
        // no permission lifts it.
        let cleared = refusal(update(
            &engine,
            &amender(),
            "i1",
            json!({"customer": null, "amount": 1}),
        ));
        assert_eq!(
            cleared.message(),
            "behavior StateRules vetoes update of invoices i1: invoices i1 is sent, which requires customer: it keeps its value while it is sent"
        );
        assert_eq!(
            veto_of(update(
                &engine,
                &amender(),
                "i1",
                json!({"customer": null, "amount": 1})
            )),
            vetoed(
                "StateRules",
                "update",
                "required_fields",
                json!({"state": "sent", "fields": ["customer"]})
            )
        );
        // The same value again is no change, and the fields it does not
        // name stay open.
        let updated = update(
            &engine,
            &clerk(),
            "i1",
            json!({"amount": 1500, "notes": "Net 30"}),
        )
        .expect("update");
        assert_eq!(
            (updated.seq, Value::Object(updated.data)),
            (4, with(full(), json!({"amount": 1500, "notes": "Net 30"})))
        );
        // A vetoed update leaves nothing of it.
        let events = engine
            .events()
            .read(
                &alice(),
                &ReadEvents {
                    schema: Some("invoices"),
                    instance_id: Some("i1"),
                    ..ReadEvents::default()
                },
            )
            .expect("events")
            .events;
        assert_eq!(events.len(), 4);
        // Back in draft, the fields move again.
        move_to(&engine, &clerk(), "i1", "draft").expect("draft");
        let back = update(
            &engine,
            &clerk(),
            "i1",
            json!({"amount": 900, "customer": null}),
        )
        .expect("update");
        assert_eq!(back.data.get("customer"), None);
    }
}

#[test]
fn the_configs_permission_lets_a_caller_change_a_frozen_field_and_nothing_it_requires() {
    for target in targets("staterules_permission") {
        let engine = open(&target);
        publish(&engine, &invoices(None, &[]));
        create(&engine, &clerk(), full(), "i1");
        move_to(&engine, &clerk(), "i1", "sent").expect("sent");
        let updated = update(
            &engine,
            &amender(),
            "i1",
            json!({"amount": 1100, "lines": ["Consulting", "Travel"]}),
        )
        .expect("update");
        assert_eq!(
            Value::Object(updated.data),
            with(
                full(),
                json!({"amount": 1100, "lines": ["Consulting", "Travel"]})
            )
        );
        assert_eq!(
            veto_of(update(&engine, &amender(), "i1", json!({"lines": null}))),
            vetoed(
                "StateRules",
                "update",
                "required_fields",
                json!({"state": "sent", "fields": ["lines"]})
            )
        );
        // Without a permission in the config, nobody changes a frozen
        // field.
        publish(
            &engine,
            &invoices(
                Some(json!({"states": {"sent": {"frozen": ["amount"]}}})),
                &[],
            ),
        );
        let (_, _, reason, code, _) = veto(update(&engine, &amender(), "i1", json!({"amount": 1})));
        assert_eq!(code.as_deref(), Some("frozen_fields"));
        assert!(
            reason.ends_with("it does not change while it is sent"),
            "{reason}"
        );
    }
}

#[test]
fn frozen_all_freezes_every_own_field_in_the_order_the_type_lists_them() {
    for target in targets("staterules_all") {
        let engine = open(&target);
        publish(&engine, &invoices(None, &[]));
        create(&engine, &clerk(), full(), "i1");
        move_to(&engine, &clerk(), "i1", "sent").expect("sent");
        move_to(&engine, &clerk(), "i1", "paid").expect("paid");
        assert_eq!(
            veto_of(update(
                &engine,
                &clerk(),
                "i1",
                json!({"notes": "Paid late", "title": "March, paid"})
            )),
            vetoed(
                "StateRules",
                "update",
                "frozen_fields",
                json!({"state": "paid", "fields": ["title", "notes"]})
            )
        );
        // paid requires nothing, so a caller with the permission may clear
        // a field.
        let updated = update(
            &engine,
            &amender(),
            "i1",
            json!({"lines": null, "notes": "Paid late"}),
        )
        .expect("update");
        assert_eq!(
            Value::Object(updated.data),
            json!({"title": "March", "customer": "Acme", "amount": 1200, "notes": "Paid late"})
        );
    }
}

#[test]
fn a_create_whose_initial_state_requires_a_field_it_does_not_give_is_refused_at_the_field() {
    for target in targets("staterules_create") {
        let engine = open(&target);
        publish(
            &engine,
            &invoices(
                Some(
                    json!({"states": {"draft": {"required": ["customer"]}, "sent": {"required": ["customer", "amount", "lines"]}}}),
                ),
                &[],
            ),
        );
        let refused = refusal(engine.instances().create(
            &clerk(),
            "invoices",
            &json!({"title": "March", "customer": null}),
            CreateOptions {
                id: Some("i1"),
                ..Default::default()
            },
        ));
        assert_eq!(refused.code().as_str(), "invalid_instance");
        assert_eq!(refused.status(), 422);
        let EngineErrorKind::InstanceValidation { issues } = refused.kind() else {
            panic!("expected an InstanceValidationError, got {refused:?}");
        };
        assert_eq!(
            serde_json::to_value(issues).expect("JSON"),
            json!([{"path": "customer", "rule": "required", "message": "customer is required in draft, the state an instance of invoices is created in"}])
        );
        assert!(get(&engine, "i1").is_none());
        let created = create(
            &engine,
            &clerk(),
            json!({"title": "March", "customer": ""}),
            "i1",
        );
        assert_eq!(created.behaviors["Workflow"]["status"], "draft");
        // What another state requires binds only when the instance enters
        // it.
        assert_eq!(
            veto_of(move_to(&engine, &clerk(), "i1", "sent")).3,
            json!({"state": "sent", "fields": ["amount", "lines"]})
        );
        // The Workflow's initial state is the one a create starts in.
        let mut sent_first = invoices(None, &[]);
        sent_first["types"]["Invoice"]["behaviors"][0]["config"]["initial"] = json!("sent");
        publish(&engine, &sent_first);
        let refused = refusal(engine.instances().create(
            &clerk(),
            "invoices",
            &json!({"title": "April", "amount": 5}),
            CreateOptions::default(),
        ));
        let EngineErrorKind::InstanceValidation { issues } = refused.kind() else {
            panic!("expected an InstanceValidationError, got {refused:?}");
        };
        assert_eq!(
            issues
                .iter()
                .map(|issue| (issue.path.as_str(), issue.rule.as_str()))
                .collect::<Vec<_>>(),
            [("customer", "required"), ("lines", "required")]
        );
        let created = engine
            .instances()
            .create(
                &clerk(),
                "invoices",
                &with(full(), json!({"title": "April"})),
                CreateOptions::default(),
            )
            .expect("create");
        assert_eq!(created.behaviors["Workflow"]["status"], "sent");
    }
}

#[test]
fn a_behaviors_update_is_held_to_the_frozen_rule_as_a_callers_update_is() {
    for target in targets("staterules_behavior_update") {
        let engine = open_recording(&target);
        publish(
            &engine,
            &invoices(None, &[json!({"name": "test.Recorder"})]),
        );
        create(&engine, &clerk(), full(), "i1");
        move_to(&engine, &clerk(), "i1", "sent").expect("sent");
        assert_eq!(
            veto_of(invoke(
                &engine,
                &clerk(),
                "i1",
                "record",
                json!({"patch": {"amount": 1300}})
            )),
            vetoed(
                "StateRules",
                "update",
                "frozen_fields",
                json!({"state": "sent", "fields": ["amount"]})
            )
        );
        assert_eq!(data_of(&engine, "i1")["amount"], 1200);
        invoke(
            &engine,
            &clerk(),
            "i1",
            "record",
            json!({"patch": {"notes": "Recorded"}}),
        )
        .expect("record");
        invoke(
            &engine,
            &amender(),
            "i1",
            "record",
            json!({"patch": {"amount": 1300}}),
        )
        .expect("record");
        assert_eq!(
            data_of(&engine, "i1"),
            with(full(), json!({"amount": 1300, "notes": "Recorded"}))
        );
    }
}

#[test]
fn its_states_are_the_workflows_and_its_fields_the_types_own_and_it_requires_workflow() {
    for target in targets("staterules_config") {
        let engine = open(&target);
        let refused = |rules: Value| {
            issues_of(&refusal(engine.schemas().define(
                &alice(),
                invoices(Some(rules), &[]),
                DefineOptions::default(),
            )))
        };
        let issue = |path: &str, message: &str| (path.to_owned(), message.to_owned());
        assert_eq!(
            refused(json!({"states": {"shipped": {"required": ["customer"]}}})),
            [issue(
                "/types/Invoice/behaviors/1/config",
                "type Invoice: behavior StateRules config: states: \"shipped\" is not a state of the type's Workflow (draft, sent, paid, void)"
            )]
        );
        assert_eq!(
            refused(json!({"states": {"sent": {"required": ["total"]}}})),
            [issue(
                "/types/Invoice/behaviors/1/config",
                "type Invoice: behavior StateRules config: states.sent.required: total is not a field of Invoice (its fields: title, customer, amount, lines, notes)"
            )]
        );
        // status is Workflow's, not the type's own.
        let status = refused(json!({"states": {"sent": {"frozen": ["status"]}}}));
        assert!(
            status[0]
                .1
                .contains("states.sent.frozen: status is not a field of Invoice"),
            "{status:?}"
        );
        assert_eq!(
            refused(json!({"states": {}})),
            [
                issue("/types/Invoice/behaviors/1", "must match \"then\" schema"),
                issue(
                    "/types/Invoice/behaviors/1/config/states",
                    "must NOT have fewer than 1 properties"
                )
            ]
        );
        assert_eq!(
            refused(json!({"states": {"sent": {}}})),
            [
                issue("/types/Invoice/behaviors/1", "must match \"then\" schema"),
                issue(
                    "/types/Invoice/behaviors/1/config/states/sent",
                    "must NOT have fewer than 1 properties"
                )
            ]
        );
        // By path, then keyword (const, oneOf, type), as the engine orders
        // ajv's issues.
        assert_eq!(
            refused(json!({"states": {"sent": {"frozen": "some"}}})),
            [
                issue("/types/Invoice/behaviors/1", "must match \"then\" schema"),
                issue(
                    "/types/Invoice/behaviors/1/config/states/sent/frozen",
                    "must be equal to constant"
                ),
                issue(
                    "/types/Invoice/behaviors/1/config/states/sent/frozen",
                    "must match exactly one schema in oneOf"
                ),
                issue(
                    "/types/Invoice/behaviors/1/config/states/sent/frozen",
                    "must be array"
                )
            ]
        );
        assert_eq!(
            refused(json!({"states": {"sent": {"required": []}}})),
            [
                issue("/types/Invoice/behaviors/1", "must match \"then\" schema"),
                issue(
                    "/types/Invoice/behaviors/1/config/states/sent/required",
                    "must NOT have fewer than 1 items"
                )
            ]
        );
        let mut unflowed = invoices_document();
        unflowed["types"]["Invoice"]["behaviors"]
            .as_array_mut()
            .expect("behaviors")
            .remove(0);
        let message = refusal(engine.schemas().define(
            &alice(),
            &unflowed,
            DefineOptions::default(),
        ))
        .message()
        .to_owned();
        assert!(
            message.contains(
                "behavior StateRules requires behavior Workflow, which the type does not list"
            ),
            "{message}"
        );
    }
}

#[test]
fn any_config_may_change_and_an_instance_that_predates_a_requirement_keeps_working() {
    for target in targets("staterules_change") {
        let engine = open(&target);
        // With no instances, any change.
        publish(
            &engine,
            &invoices(Some(json!({"states": {"sent": {"frozen": "all"}}})), &[]),
        );
        publish(
            &engine,
            &invoices(
                Some(
                    json!({"states": {"paid": {"required": ["notes"]}}, "permission": "invoices.amend"}),
                ),
                &[],
            ),
        );
        // Added to a schema with instances.
        publish(&engine, &invoices(Some(Value::Null), &[]));
        create(
            &engine,
            &clerk(),
            json!({"title": "March", "amount": 1200}),
            "i1",
        );
        move_to(&engine, &clerk(), "i1", "sent").expect("sent");
        publish(&engine, &invoices(None, &[]));
        assert_eq!(status_of(&engine, "i1"), "sent");
        // It holds no customer and no lines, and an update that leaves them
        // missing passes.
        assert_eq!(
            update(&engine, &clerk(), "i1", json!({"notes": "Late"}))
                .expect("update")
                .data["notes"],
            "Late"
        );
        assert_eq!(
            veto_of(update(&engine, &clerk(), "i1", json!({"amount": 1}))).2,
            "frozen_fields"
        );
        update(&engine, &amender(), "i1", json!({"customer": "Acme"})).expect("update");
        assert_eq!(
            veto_of(update(&engine, &amender(), "i1", json!({"customer": null}))).2,
            "required_fields"
        );
        // Entering the state again needs what it requires.
        move_to(&engine, &clerk(), "i1", "draft").expect("draft");
        assert_eq!(
            veto_of(move_to(&engine, &clerk(), "i1", "sent")).3,
            json!({"state": "sent", "fields": ["lines"]})
        );
        // Changed and removed while instances exist.
        publish(
            &engine,
            &invoices(
                Some(json!({"states": {"draft": {"frozen": ["amount"]}}})),
                &[],
            ),
        );
        assert_eq!(
            veto_of(update(&engine, &clerk(), "i1", json!({"amount": 2}))).3,
            json!({"state": "draft", "fields": ["amount"]})
        );
        publish(&engine, &invoices(Some(Value::Null), &[]));
        assert_eq!(
            update(&engine, &clerk(), "i1", json!({"amount": 2}))
                .expect("update")
                .data["amount"],
            2
        );
        assert_eq!(
            engine
                .schemas()
                .live(&alice(), "invoices", Default::default())
                .expect("live")
                .expect("a live version")
                .version,
            Some(6)
        );
    }
}

/// The case of `test/core-behaviors.test.ts` that runs the invoices
/// document the core binary builds (fixture-staterules-json).
#[test]
fn it_runs_the_invoices_document_a_sent_invoice_needs_and_keeps_fields_and_a_paid_one_keeps_every_field(
) {
    for target in targets("staterules_invoices_document") {
        let engine = open(&target);
        let writer = Principal::new("wes", Vec::<String>::new());
        publish(&engine, &invoices_document());
        create(
            &engine,
            &writer,
            json!({"title": "March", "customer": "Acme"}),
            "march",
        );
        let missing = refusal(move_to(&engine, &writer, "march", "sent"));
        assert_eq!(
            (missing.code().as_str(), missing.message()),
            (
                "vetoed",
                "behavior StateRules vetoes transition of invoices march: invoices march cannot move to sent without amount and lines, which sent requires"
            )
        );
        update(
            &engine,
            &writer,
            "march",
            json!({"amount": 1200, "lines": ["Consulting"]}),
        )
        .expect("update");
        move_to(&engine, &writer, "march", "sent").expect("sent");
        let frozen = refusal(update(&engine, &writer, "march", json!({"amount": 1300})));
        assert!(
            frozen.message().contains("which freezes amount"),
            "{frozen}"
        );
        assert_eq!(
            update(&engine, &amender(), "march", json!({"amount": 1300}))
                .expect("update")
                .data["amount"],
            1300
        );
        move_to(&engine, &writer, "march", "paid").expect("paid");
        let paid = refusal(update(
            &engine,
            &writer,
            "march",
            json!({"notes": "Thanks"}),
        ));
        assert!(
            paid.message().contains("is paid, which freezes notes"),
            "{paid}"
        );
        let read = get(&engine, "march").expect("march");
        assert_eq!(
            (
                Value::Object(read.data),
                read.behaviors["Workflow"]["status"].clone(),
                read.seq
            ),
            (
                json!({"title": "March", "customer": "Acme", "amount": 1300, "lines": ["Consulting"]}),
                json!("paid"),
                5
            )
        );
    }
}
