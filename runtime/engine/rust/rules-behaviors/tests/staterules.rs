//! StateRules on the pure core (D103), through the core's public API over a
//! source in memory: what a state requires (a create in the initial state,
//! a transition into the state, an update that clears it) and what it
//! freezes, the config's permission past a freeze; and who may change a
//! frozen field (D104): the subject a `by` field holds, on a string field,
//! on a UUID field in either form and on createdBy, read from the row
//! before the update, required held for them all the same, `frozen: "all"`
//! with by; a seat of Seats in by (D104, step 3), through an occupant row
//! and a fromField, read before the update, the refusal naming it, and the
//! seat a define holds to the type's Seats; a move Workflow's config
//! refuses let through to Workflow, the
//! Workflow a config reads (implied, named among two, refused), a Timers
//! fire whose move it vetoes recorded vetoed; and the config held to the
//! declaration file's schema, to the Workflow's states and to the type's
//! own fields.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{Behavior, Declaration, FieldSpec, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{core, staterules, StateRules};
use support::{
    caller, changed, define, refused, result, rule, shape, undescribed, veto_of, Host, START,
};

// dana, the creator, by her UUID in base62 and hyphenated.
const DANA: &str = "TMXp8QPR6yGvKuV2GZQ10";
const DANA_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
// The reviewer, by his UUID hyphenated and in base62.
const REVIEWER: &str = "6ba7b810-9dad-11d1-80b4-00c04fd430c8";
const REVIEWER_BASE62: &str = "3H8pGALtipnCnHud4zBiky";

// A plan is planned, worked on, proved and done; archived it never reaches.
fn flow() -> Value {
    json!({
        "states": ["planned", "working", "proving", "done", "archived"],
        "transitions": [
            {"from": "planned", "to": "working"},
            {"from": "working", "to": "proving"},
            {"from": "proving", "to": "working"},
            {"from": "proving", "to": "done"}
        ]
    })
}

// What each state requires and freezes, and who changes what it freezes.
fn rules() -> Value {
    json!({
        "states": {
            "planned": {"frozen": ["plan"], "by": ["createdBy"]},
            "working": {"frozen": ["plan"], "by": ["reviewer", "owner"]},
            "proving": {"required": ["proof"], "frozen": ["proof"], "by": ["reviewer"]},
            "done": {"frozen": "all", "by": ["owner"]},
            "archived": {"required": ["proof"]}
        },
        "permission": "plans.admin"
    })
}

// A plan as a table holds it: StateRules on the type before its Workflow
// field, as a table lists the uses it implements before its field uses.
fn plan(rules: Value) -> TypeSpec {
    TypeSpec::new("Plan")
        .with(UseSpec::on_type("StateRules", rules))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("plan", "string"))
        .with_field(FieldSpec::new("proof", "string"))
        .with_field(FieldSpec::new("owner", "string"))
        .with_field(FieldSpec::new("reviewer", "Identity.UUID"))
        .with_field(FieldSpec::new("urgent", "boolean"))
        .with_field(FieldSpec::new("createdBy", "Identity.UUID"))
}

// hosting creates p1, planned, reviewed by a UUID held hyphenated, owned by
// bob and created by dana, her UUID held in base62.
fn hosting(rules: Value) -> Host {
    let mut host = Host::new(core(), plan(rules));
    changed(host.create(
        &dana(),
        "p1",
        json!({"title": "Launch", "plan": "Draft", "owner": "bob", "reviewer": REVIEWER, "createdBy": DANA}),
    ));
    host
}

fn dana() -> superschematic_rules::Caller {
    caller(DANA, &[])
}

fn bob() -> superschematic_rules::Caller {
    caller("bob", &[])
}

fn carol() -> superschematic_rules::Caller {
    caller("carol", &[])
}

fn admin() -> superschematic_rules::Caller {
    caller("ada", &["plans.admin"])
}

fn transition(
    host: &mut Host,
    who: &superschematic_rules::Caller,
    to: &str,
) -> superschematic_rules::Outcome {
    host.invoke(who, "p1", "status", "transition", json!({ "to": to }))
}

fn status(host: &Host) -> Value {
    host.row("p1").owned["status"]["status"].clone()
}

// ---- the declaration and its schema ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        StateRules.declaration(),
        Declaration::new("StateRules")
            .veto("missing_fields")
            .veto("frozen_fields")
            .veto("required_fields")
    );
}

/// The checks StateRules writes by hand are the declaration's
/// configSchema, keyword for keyword, so a keyword the file gains fails
/// this test until the checks take it; the shape refusals the recorded
/// config scenario holds (`core-staterules.json`) come out in its words,
/// and so do the rest of the schema's keywords.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(staterules::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let fields =
        json!({"items": text, "maxItems": 64, "minItems": 1, "type": "array", "uniqueItems": true});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "permission": text,
                "states": {
                    "additionalProperties": {
                        "additionalProperties": false,
                        "minProperties": 1,
                        "properties": {
                            "by": {"items": text, "minItems": 1, "type": "array", "uniqueItems": true},
                            "frozen": {"oneOf": [fields, {"const": "all"}]},
                            "required": fields
                        },
                        "type": "object"
                    },
                    "minProperties": 1,
                    "propertyNames": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$"},
                    "type": "object"
                },
                "status": text
            },
            "required": ["states"],
            "type": "object"
        })
    );
    assert_eq!(file.get("createParamsSchema"), None);
    let shaped = |config: Value| shape(&core(), &plan(config));
    // The recorded scenario's.
    assert_eq!(
        shaped(json!({"states": {}})),
        ["/config/states: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        shaped(json!({"states": {"working": {}}})),
        ["/config/states/working: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        shaped(json!({"states": {"working": {"frozen": "some"}}})),
        [
            "/config/states/working/frozen: must be equal to constant",
            "/config/states/working/frozen: must match exactly one schema in oneOf",
            "/config/states/working/frozen: must be array"
        ]
    );
    assert_eq!(
        shaped(json!({"states": {"working": {"required": []}}})),
        ["/config/states/working/required: must NOT have fewer than 1 items"]
    );
    // The rest of the schema's keywords.
    assert_eq!(
        shaped(json!({"states": {"2nd": {"frozen": ["plan", "plan"], "by": "owner", "lost": 1}}, "status": "", "permission": 5, "x": 1})),
        [
            "/config: unknown key \"x\"",
            "/config/permission: must be string",
            "/config/states: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\"",
            "/config/states: property name must be valid",
            "/config/states/2nd: unknown key \"lost\"",
            "/config/states/2nd/by: must be array",
            "/config/states/2nd/frozen: must be equal to constant",
            "/config/states/2nd/frozen: must match exactly one schema in oneOf",
            "/config/states/2nd/frozen: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/status: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(json!({"states": {"working": {"frozen": ["plan"], "by": ["", "owner", "owner"]}}})),
        [
            "/config/states/working/by: must NOT have duplicate items (items ## 2 and 1 are identical)",
            "/config/states/working/by/0: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(json!({"states": {"working": {"frozen": ["plan"], "by": []}}})),
        ["/config/states/working/by: must NOT have fewer than 1 items"]
    );
    assert_eq!(
        shaped(json!({})),
        ["/config: must have required property 'states'"]
    );
    assert_eq!(
        shaped(json!({"states": []})),
        ["/config/states: must be object"]
    );
}

/// Each state is one of the Workflow's and each field one of the type's
/// own, in the engine's words; by names an own field that holds a subject,
/// in a state that freezes one, or a seat of the type's Seats.
#[test]
fn its_states_are_the_workflows_and_its_fields_the_types_own() {
    let ruled = |config: Value| rule(&core(), &plan(config), "StateRules");
    let fields = "title, plan, proof, owner, reviewer, urgent, createdBy";
    assert_eq!(
        ruled(json!({"states": {"shipped": {"required": ["plan"]}}})),
        "states: \"shipped\" is not a state of the type's Workflow (planned, working, proving, done, archived)"
    );
    assert_eq!(
        ruled(json!({"states": {"working": {"required": ["total"]}}})),
        format!("states.working.required: total is not a field of Plan (its fields: {fields})")
    );
    assert_eq!(
        ruled(json!({"states": {"working": {"frozen": ["status"]}}})),
        format!("states.working.frozen: status is not a field of Plan (its fields: {fields})")
    );
    assert_eq!(
        ruled(json!({"states": {"proving": {"required": ["proof"], "by": ["reviewer"]}}})),
        "states.proving.by lists who may change a field proving freezes, and proving freezes none"
    );
    assert_eq!(
        ruled(json!({"states": {"working": {"frozen": ["plan"], "by": ["seat:reviewer"]}}})),
        "states.working.by names \"seat:reviewer\", a seat of Seats, which Plan does not implement"
    );
    let seated = |config: Value| {
        let spec = TypeSpec {
            uses: [UseSpec::on_type(
                "Seats",
                json!({"seats": {"lead": {"max": 1}, "editors": {}}}),
            )]
            .into_iter()
            .chain(plan(config).uses)
            .collect(),
            ..plan(json!({}))
        };
        rule(&core(), &spec, "StateRules")
    };
    assert_eq!(
        seated(json!({"states": {"working": {"frozen": ["plan"], "by": ["owner", "seat:reviewer"]}}})),
        "states.working.by names \"seat:reviewer\", which is not a seat of Plan's Seats (its seats: editors, lead)"
    );
    assert_eq!(
        ruled(json!({"states": {"working": {"frozen": ["plan"], "by": ["missing"]}}})),
        format!("states.working.by names \"missing\", which is not a field of Plan (its fields: {fields})")
    );
    assert_eq!(
        ruled(json!({"states": {"working": {"frozen": "all", "by": ["urgent"]}}})),
        "states.working.by names \"urgent\", a boolean field of Plan: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)"
    );
    // The states in the config's order, each checked whole before the next.
    assert_eq!(
        ruled(json!({"states": {"working": {"frozen": ["plan"], "by": ["missing"]}, "lost": {"required": ["plan"]}}})),
        format!("states.working.by names \"missing\", which is not a field of Plan (its fields: {fields})")
    );
}

/// The Workflow the rules are of is the type's one, the one status names
/// among several, and refused when the type has none, or several and the
/// config names none.
#[test]
fn the_workflow_it_reads_is_implied_named_or_refused() {
    let alone = TypeSpec::new("Plan")
        .with(UseSpec::on_type(
            "StateRules",
            json!({"states": {"working": {"required": ["plan"]}}}),
        ))
        .with_field(FieldSpec::new("plan", "string"));
    assert_eq!(
        rule(&core(), &alone, "StateRules"),
        "it reads the status of Workflow, which the type does not list"
    );
    let two = |rules: Value| {
        plan(rules).with(UseSpec::on_field(
            "phase",
            "Workflow",
            json!({"states": ["early", "late"], "transitions": [{"from": "early", "to": "late"}]}),
        ))
    };
    assert_eq!(
        rule(&core(), &two(json!({"states": {"late": {"required": ["proof"]}}})), "StateRules"),
        "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    assert_eq!(
        rule(
            &core(),
            &two(json!({"states": {"late": {"required": ["proof"]}}, "status": "stage"})),
            "StateRules"
        ),
        "status \"stage\" is not a Workflow of the type (its Workflows: status, phase)"
    );
    assert_eq!(
        rule(
            &core(),
            &two(json!({"states": {"working": {"required": ["proof"]}}, "status": "phase"})),
            "StateRules"
        ),
        "states: \"working\" is not a state of the type's Workflow (early, late)"
    );
    // Named among two, the rules hold that Workflow's moves and states
    // alone.
    let mut host = Host::new(
        core(),
        two(
            json!({"states": {"late": {"required": ["proof"], "frozen": ["plan"]}}, "status": "phase"}),
        ),
    );
    changed(host.create(&bob(), "p1", json!({"title": "Launch", "plan": "Draft"})));
    changed(transition(&mut host, &bob(), "working"));
    changed(host.update(&bob(), "p1", json!({"plan": "Second"})));
    assert_eq!(
        veto_of(host.invoke(&bob(), "p1", "phase", "transition", json!({"to": "late"}))),
        (
            "missing_fields".to_owned(),
            json!({"state": "late", "fields": ["proof"]})
        )
    );
    changed(host.update(&bob(), "p1", json!({"proof": "Screenshots"})));
    changed(host.invoke(&bob(), "p1", "phase", "transition", json!({"to": "late"})));
    assert_eq!(
        veto_of(host.update(&bob(), "p1", json!({"plan": "Third"}))),
        (
            "frozen_fields".to_owned(),
            json!({"state": "late", "fields": ["plan"]})
        )
    );
}

// ---- what a state requires ----

/// A create whose initial state requires a field it does not give is
/// refused at the field; a transition into a state is held to what it
/// requires, whoever moves the row; an update that clears a required field
/// is refused whoever asks, the permission and a by subject alike.
#[test]
fn what_a_state_requires_holds_for_every_caller() {
    let mut host = Host::new(
        core(),
        plan(
            json!({"states": {"planned": {"required": ["plan", "owner"]}, "proving": {"required": ["proof"], "frozen": ["proof"], "by": ["reviewer"]}}, "permission": "plans.admin"}),
        ),
    );
    assert_eq!(
        refused(host.create(&bob(), "p0", json!({"title": "Launch", "owner": null}))),
        json!({"class": "InstanceValidationError", "code": "invalid_instance",
        "message": "Plan: plan: plan is required in planned, the state an instance of Plan is created in; owner: owner is required in planned, the state an instance of Plan is created in",
        "details": {"issues": [
            {"path": "plan", "rule": "required", "message": "plan is required in planned, the state an instance of Plan is created in"},
            {"path": "owner", "rule": "required", "message": "owner is required in planned, the state an instance of Plan is created in"}
        ]}})
    );
    changed(host.create(
        &bob(),
        "p1",
        json!({"title": "Launch", "plan": "", "owner": "bob", "reviewer": REVIEWER_BASE62}),
    ));
    changed(transition(&mut host, &bob(), "working"));
    let error = refused(transition(&mut host, &bob(), "proving"));
    assert_eq!(
        error["message"],
        "behavior StateRules vetoes transition of Plan p1: Plan p1 cannot move to proving without proof, which proving requires"
    );
    changed(host.update(&bob(), "p1", json!({"proof": "Screenshots"})));
    changed(transition(&mut host, &bob(), "proving"));
    for who in [admin(), caller(REVIEWER, &[])] {
        assert_eq!(
            refused(host.update(&who, "p1", json!({"proof": null})))["details"]["reason"],
            "Plan p1 is proving, which requires proof: it keeps its value while it is proving"
        );
    }
    // The reviewer, by its by, changes it.
    changed(host.update(&caller(REVIEWER, &[]), "p1", json!({"proof": "Better"})));
}

// ---- who may change a frozen field ----

/// The subject a by field holds changes what the state freezes: createdBy
/// names the creator, a UUID field its subject in either form, a string
/// field its text; another caller is frozen_fields, the refusal naming the
/// state's by, and the config's permission still passes.
#[test]
fn the_subject_a_by_field_holds_changes_a_frozen_field() {
    let mut host = hosting(rules());
    // planned freezes plan, by createdBy: dana, in base62.
    changed(host.update(&dana(), "p1", json!({"plan": "Second"})));
    changed(host.update(
        &caller(DANA_HYPHENATED, &[]),
        "p1",
        json!({"plan": "Third"}),
    ));
    let error = refused(host.update(&bob(), "p1", json!({"plan": "Bob's"})));
    assert_eq!(
        error,
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": "behavior StateRules vetoes update of Plan p1: Plan p1 is planned, which freezes plan: it does not change while it is planned; only the subject createdBy holds, or a caller with plans.admin, may change it",
            "details": {"behavior": "StateRules", "action": "update",
                "reason": "Plan p1 is planned, which freezes plan: it does not change while it is planned; only the subject createdBy holds, or a caller with plans.admin, may change it",
                "vetoCode": "frozen_fields",
                "vetoDetails": {"state": "planned", "fields": ["plan"], "by": ["createdBy"]}}})
    );
    changed(host.update(&admin(), "p1", json!({"plan": "Moderated"})));
    // A field the state does not freeze is open to any caller.
    changed(host.update(&carol(), "p1", json!({"title": "Renamed"})));
    // working freezes plan, by reviewer or owner: the reviewer's UUID in
    // base62 against the hyphenated value, and the owner's text.
    changed(transition(&mut host, &bob(), "working"));
    changed(host.update(
        &caller(REVIEWER_BASE62, &[]),
        "p1",
        json!({"plan": "Reviewed"}),
    ));
    changed(host.update(&bob(), "p1", json!({"plan": "Owned"})));
    assert_eq!(
        refused(host.update(&caller("Bob", &[]), "p1", json!({"plan": "Bob's"})))["details"]
            ["reason"],
        "Plan p1 is working, which freezes plan: it does not change while it is working; only the subject reviewer or owner holds, or a caller with plans.admin, may change it"
    );
    assert_eq!(
        refused(host.update(&dana(), "p1", json!({"plan": "Dana's"})))["details"]["vetoDetails"],
        json!({"state": "working", "fields": ["plan"], "by": ["reviewer", "owner"]})
    );
}

/// A seat of the type's Seats in a state's by (D104): a subject seated
/// there, through an occupant row or the seat's fromField, before the
/// update, changes a frozen field; a caller who takes the seat in the
/// command's patch of the fromField does not count, and another caller is
/// frozen_fields naming the seat as by lists it, fields first.
#[test]
fn a_subject_seated_in_a_by_seat_changes_a_frozen_field() {
    let seats = json!({"seats": {"lead": {"max": 1}, "editors": {"fromField": "owner"}}});
    let rules = json!({"states": {"working": {"frozen": ["plan"], "by": ["reviewer", "seat:lead", "seat:editors"]}}});
    let spec = TypeSpec {
        uses: [UseSpec::on_type("Seats", seats)]
            .into_iter()
            .chain(plan(rules).uses)
            .collect(),
        ..plan(json!({}))
    };
    let mut host = Host::new(core(), spec);
    changed(host.create(
        &dana(),
        "p1",
        json!({"title": "Launch", "plan": "Draft", "owner": "bob", "createdBy": DANA}),
    ));
    changed(transition(&mut host, &bob(), "working"));
    // bob is seated in editors through owner.
    changed(host.update(&bob(), "p1", json!({"plan": "Bob's"})));
    // carol is seated nowhere, until she takes lead.
    let error = refused(host.update(&carol(), "p1", json!({"plan": "Carol's"})));
    assert_eq!(
        error["details"]["reason"],
        "Plan p1 is working, which freezes plan: it does not change while it is working; only the subject reviewer holds or a subject seated in lead or editors may change it"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"state": "working", "fields": ["plan"], "by": ["reviewer", "seat:lead", "seat:editors"]})
    );
    changed(host.invoke(&carol(), "p1", "Seats", "takeSeat", json!({"seat": "lead"})));
    changed(host.update(&carol(), "p1", json!({"plan": "Carol's"})));
    // A patch that names the caller in the fromField does not seat it first.
    assert_eq!(
        veto_of(host.update(
            &caller("eve", &[]),
            "p1",
            json!({"owner": "eve", "plan": "Eve's"})
        ))
        .0,
        "frozen_fields"
    );
    // Once owner names eve, she is seated in editors.
    changed(host.update(&caller("eve", &[]), "p1", json!({"owner": "eve"})));
    changed(host.update(&caller("eve", &[]), "p1", json!({"plan": "Eve's"})));
    assert_eq!(
        veto_of(host.update(&bob(), "p1", json!({"plan": "Bob's again"}))).0,
        "frozen_fields"
    );
}

/// A patch that names the caller in a by field does not count: by is read
/// from the row before the update.
#[test]
fn a_patch_that_names_the_caller_in_by_does_not_count() {
    let mut host = hosting(rules());
    changed(transition(&mut host, &bob(), "working"));
    let (code, details) =
        veto_of(host.update(&carol(), "p1", json!({"owner": "carol", "plan": "Carol's"})));
    assert_eq!(code, "frozen_fields");
    assert_eq!(details["fields"], json!(["plan"]));
    // Named first, carol changes it after.
    changed(host.update(&carol(), "p1", json!({"owner": "carol"})));
    changed(host.update(&carol(), "p1", json!({"plan": "Carol's"})));
    // A by field with no value names no one.
    changed(host.update(&carol(), "p1", json!({"owner": null, "reviewer": null})));
    assert_eq!(
        veto_of(host.update(&carol(), "p1", json!({"plan": "Again"}))).0,
        "frozen_fields"
    );
}

/// frozen all freezes every own field, in the order the type lists them,
/// and by lets its subject change them; with no permission, the refusal
/// names the by alone.
#[test]
fn frozen_all_with_by_lets_its_subject_change_every_field() {
    let mut rules = rules();
    rules
        .as_object_mut()
        .expect("an object")
        .remove("permission");
    let mut host = hosting(rules);
    host.memory
        .rows
        .get_mut("p1")
        .expect("p1")
        .owned
        .get_mut("status")
        .expect("status")
        .insert("status".into(), json!("done"));
    assert_eq!(
        refused(host.update(&carol(), "p1", json!({"urgent": true, "title": "Done"})))["details"]
            ["vetoDetails"],
        json!({"state": "done", "fields": ["title", "urgent"], "by": ["owner"]})
    );
    assert_eq!(
        refused(host.update(&carol(), "p1", json!({"title": "Done"})))["details"]["reason"],
        "Plan p1 is done, which freezes title: it does not change while it is done; only the subject owner holds may change it"
    );
    changed(host.update(&bob(), "p1", json!({"title": "Done", "urgent": true})));
    // With neither by nor permission, nobody changes it.
    let mut host = hosting(json!({"states": {"planned": {"frozen": ["plan"]}}}));
    assert_eq!(
        refused(host.update(&admin(), "p1", json!({"plan": "x"})))["details"]["reason"],
        "Plan p1 is planned, which freezes plan: it does not change while it is planned"
    );
}

// ---- composition ----

/// A move Workflow's config refuses is Workflow's to refuse, though
/// StateRules is listed first and the state's rule is unmet: archived is
/// no state a transition leads to.
#[test]
fn a_move_workflow_refuses_is_workflows_to_refuse() {
    let mut host = hosting(rules());
    let error = refused(transition(&mut host, &bob(), "archived"));
    assert_eq!(error["details"]["behavior"], "Workflow");
    assert_eq!(error["details"]["vetoCode"], "transition_not_allowed");
    assert_eq!(status(&host), "planned");
}

/// A Timers fire whose move StateRules vetoes is recorded vetoed, and the
/// status stays: the fire calls Workflow's transition, which StateRules'
/// guard holds as it holds a caller's.
#[test]
fn a_timers_fire_whose_move_staterules_vetoes_is_recorded_vetoed() {
    let spec = plan(rules()).with(UseSpec::on_type(
        "Timers",
        json!({"deadlines": {"prove": {"start": {"enters": "working"}, "afterMs": 1000, "then": {"transition": "proving"}}}}),
    ));
    let mut host = Host::new(core(), spec);
    changed(host.create(
        &bob(),
        "p1",
        json!({"title": "Launch", "plan": "Draft", "owner": "bob"}),
    ));
    changed(transition(&mut host, &bob(), "working"));
    let fired = host.fall_due(START + 1000, &caller("runner", &[]));
    assert_eq!(fired.len(), 1);
    changed(fired.into_iter().next().expect("a fire").1);
    assert_eq!(status(&host), "working");
    let deadlines = host.read_fields("p1", "Timers", "deadlines");
    assert_eq!(deadlines["deadlines"]["prove"]["outcome"], "vetoed");
    // With the proof it needs, a move by a caller goes through.
    changed(host.update(&bob(), "p1", json!({"proof": "Screenshots"})));
    assert_eq!(
        result(transition(&mut host, &bob(), "proving")),
        json!({"from": "working", "to": "proving"})
    );
}

/// StateRules keeps no state: any config may change, and it joins and
/// leaves a type with rows.
#[test]
fn any_config_change_is_allowed() {
    let mut host = hosting(rules());
    let from = host.spec.clone();
    for next in [
        plan(json!({"states": {"working": {"required": ["proof"]}}})),
        TypeSpec::new("Plan").with(UseSpec::on_field("status", "Workflow", flow())),
    ] {
        let command = superschematic_rules::Command::new(
            bob(),
            START,
            superschematic_rules::Action::Publish {
                from: Some(from.clone()),
            },
        );
        assert!(host
            .core
            .drive(&next, &command, &mut host.memory)
            .into_result()
            .is_ok());
    }
    assert!(define(&core(), &plan(rules())).is_ok());
}
