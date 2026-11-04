//! Approvals on the pure core (D103), through the core's public API over a
//! source in memory that keeps rows and their sign-offs' sub-table rows:
//! the engine's gate (a move into a gated state waits for its count of
//! sign-offs, from the states its from lists), signOff's checks in their
//! order, withdrawSignOff, listSignOffs a page at a time with its cursor,
//! the gates a read shows; and the upgrades (D104): resetOn changed and
//! unchanged, `[]`, a gate without it following resetOnChange either way,
//! entering the state consuming, a move another use makes consuming, a
//! removed gate's sign-offs consumed at the next write, a pinned gate's
//! version matching, stale and missing, a version on a gate that is not
//! pinned, a sign-off moving the version, the creator by createdBy as text
//! and as a UUID; composed with Workflow's repeat, settled before any
//! guard, and with a Timers fire whose move the gate vetoes; and the config
//! and the operations' parameters held to the declaration file's schemas.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Behavior, Command, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, GuardRequest,
    Hook, TableDecl, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{approvals, core, Approvals};
use support::{
    caller, changed, define, refused, result, rule, shape, undescribed, veto_of, Host, START,
};

// A document is drafted, reviewed, approved and published.
fn flow() -> Value {
    json!({
        "states": ["draft", "review", "approved", "published"],
        "transitions": [
            {"from": "draft", "to": "review"},
            {"from": "review", "to": "draft"},
            {"from": "review", "to": "approved"},
            {"from": "approved", "to": "review"},
            {"from": "approved", "to": "published"}
        ]
    })
}

// A document as a table holds it: Approvals on the type before its
// Workflow field, its title, proof and body, and the audit field createdBy.
fn doc(gates: Value) -> TypeSpec {
    TypeSpec::new("Doc")
        .with(UseSpec::on_type("Approvals", gates))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("proof", "string"))
        .with_field(FieldSpec::new("body", "string"))
        .with_field(FieldSpec::new("createdBy", "Identity.UUID"))
}

// DANA is a UUID in base62, DANA_HYPHENATED the same one hyphenated.
const DANA: &str = "TMXp8QPR6yGvKuV2GZQ10";
const DANA_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";

// hosting creates d1, a draft dana created, moved to review.
fn hosting(gates: Value) -> Host {
    hosted(Host::new(core(), doc(gates)))
}

fn hosted(mut host: Host) -> Host {
    changed(host.create(
        &caller(DANA, &[]),
        "d1",
        json!({"title": "Plan", "proof": "v1", "createdBy": DANA}),
    ));
    changed(transition(&mut host, "review"));
    host
}

fn ann() -> superschematic_rules::Caller {
    caller("ann", &["docs.approve"])
}

fn ben() -> superschematic_rules::Caller {
    caller("ben", &["docs.approve"])
}

fn alice() -> superschematic_rules::Caller {
    caller("alice", &[])
}

fn transition(host: &mut Host, to: &str) -> superschematic_rules::Outcome {
    host.invoke(&alice(), "d1", "status", "transition", json!({ "to": to }))
}

fn sign(
    host: &mut Host,
    who: &superschematic_rules::Caller,
    params: Value,
) -> superschematic_rules::Outcome {
    host.invoke(who, "d1", "Approvals", "signOff", params)
}

// gates is the gates a read of d1 shows.
fn gates(host: &Host) -> Value {
    host.read_fields("d1", "Approvals", "signOffs")["gates"].clone()
}

// sign_offs lists d1's sign-off rows: their keys and values.
fn sign_offs(host: &Host) -> Value {
    serde_json::to_value(host.sub_rows("d1", "Approvals", "signOffs")).expect("rows")
}

fn gate(count: i64) -> Value {
    json!({"count": count, "permission": "docs.approve"})
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Approvals.declaration(),
        Declaration::new("Approvals")
            .computed("gates")
            .sub_table(TableDecl::new(
                "signOffs",
                "number",
                ["number", "state", "subject", "note", "at", "consumedAt"]
            ))
            .operation("signOff", true)
            .operation("withdrawSignOff", true)
            .operation("listSignOffs", false)
            .veto("approvals_missing")
            .veto("already_signed")
            .veto("creator_cannot_sign")
            .veto("in_state")
            .veto("no_gate")
            .veto("not_signed")
            .veto("stale")
    );
}

/// The checks Approvals writes by hand are the declaration's configSchema,
/// keyword for keyword, so a keyword the file gains fails this test until
/// the checks take it; the shape refusals the recorded config scenario
/// holds (`core-approvals.json`) come out in its words, and so do the rest
/// of the schema's keywords.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(approvals::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "gates": {
                    "additionalProperties": {
                        "additionalProperties": false,
                        "properties": {
                            "count": {"maximum": 100, "minimum": 1, "type": "integer"},
                            "excludeCreator": {"type": "boolean"},
                            "from": {"items": text, "minItems": 1, "type": "array", "uniqueItems": true},
                            "permission": text,
                            "pinned": {"type": "boolean"},
                            "resetOn": {"items": text, "maxItems": 64, "type": "array", "uniqueItems": true}
                        },
                        "required": ["count", "permission"],
                        "type": "object"
                    },
                    "minProperties": 1,
                    "propertyNames": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$"},
                    "type": "object"
                },
                "resetOnChange": {"type": "boolean"},
                "status": text
            },
            "required": ["gates"],
            "type": "object"
        })
    );
    assert_eq!(file.get("createParamsSchema"), None);
    let shaped = |config: Value| shape(&core(), &doc(config));
    // The recorded scenario's.
    assert_eq!(
        shaped(json!({"gates": {}})),
        ["/config/gates: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        shaped(json!({"gates": {"approved": {"count": 0, "permission": "docs.approve"}}})),
        ["/config/gates/approved/count: must be >= 1"]
    );
    assert_eq!(
        shaped(json!({"gates": {"approved": {"count": 1}}})),
        ["/config/gates/approved: must have required property 'permission'"]
    );
    // The rest of the schema's keywords.
    assert_eq!(
        shaped(json!({"gates": {"approved": {"count": 1.5, "permission": "", "from": [], "excludeCreator": "no", "pinned": "yes", "resetOn": "proof", "x": 1}, "2nd": {"count": 101, "permission": "p", "resetOn": ["proof", "proof", ""]}}, "resetOnChange": 1, "status": 5})),
        [
            "/config/gates: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\"",
            "/config/gates: property name must be valid",
            "/config/gates/2nd/count: must be <= 100",
            "/config/gates/2nd/resetOn: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/gates/2nd/resetOn/2: must NOT have fewer than 1 characters",
            "/config/gates/approved: unknown key \"x\"",
            "/config/gates/approved/count: must be integer",
            "/config/gates/approved/excludeCreator: must be boolean",
            "/config/gates/approved/from: must NOT have fewer than 1 items",
            "/config/gates/approved/permission: must NOT have fewer than 1 characters",
            "/config/gates/approved/pinned: must be boolean",
            "/config/gates/approved/resetOn: must be array",
            "/config/resetOnChange: must be boolean",
            "/config/status: must be string"
        ]
    );
    let wide: Vec<String> = (0..65).map(|i| format!("f{i}")).collect();
    assert_eq!(
        shaped(json!({"gates": {"approved": {"count": 1, "permission": "p", "resetOn": wide}}})),
        ["/config/gates/approved/resetOn: must NOT have more than 64 items"]
    );
    // resetOn may name no field.
    assert!(define(
        &core(),
        &doc(json!({"gates": {"approved": {"count": 1, "permission": "p", "resetOn": []}}}))
    )
    .is_ok());
}

/// Each operation's parameters are held to its closed paramsSchema, in the
/// behavior registry's words, before any guard sees them; a pinned gate's
/// signOff needs version.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(approvals::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let operations = file["operations"].as_array().expect("operations");
    let schemas: Vec<(String, Value)> = operations
        .iter()
        .map(|operation| {
            (
                operation["name"].as_str().expect("a name").to_owned(),
                undescribed(&operation["paramsSchema"]),
            )
        })
        .collect();
    assert_eq!(
        schemas,
        vec![
            (
                "signOff".to_owned(),
                json!({"additionalProperties": false, "properties": {
                    "note": {"maxLength": 10000, "type": "string"},
                    "state": text,
                    "version": {"minimum": 1, "type": "integer"}
                }, "required": ["state"], "type": "object"})
            ),
            (
                "withdrawSignOff".to_owned(),
                json!({"additionalProperties": false, "properties": {"state": text}, "required": ["state"], "type": "object"})
            ),
            (
                "listSignOffs".to_owned(),
                json!({"additionalProperties": false, "properties": {
                    "cursor": {"type": "string"},
                    "limit": {"maximum": 500, "minimum": 1, "type": "integer"},
                    "state": text
                }, "type": "object"})
            ),
        ]
    );
    let mut host = hosting(
        json!({"gates": {"approved": gate(1), "published": {"count": 1, "permission": "docs.approve", "pinned": true}}}),
    );
    let issues = |outcome| refused(outcome)["details"]["issues"].clone();
    assert_eq!(
        refused(sign(
            &mut host,
            &ann(),
            json!({"state": "", "note": 5, "version": 0, "x": 1})
        )),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
        "message": "operation signOff of behavior Approvals: must NOT have additional properties: x; /note: must be string; /state: must NOT have fewer than 1 characters; /version: must be >= 1",
        "details": {"issues": [
            {"path": "", "message": "must NOT have additional properties: x"},
            {"path": "/note", "message": "must be string"},
            {"path": "/state", "message": "must NOT have fewer than 1 characters"},
            {"path": "/version", "message": "must be >= 1"}
        ]}})
    );
    assert_eq!(
        issues(sign(
            &mut host,
            &ann(),
            json!({"note": "x".repeat(10_001), "version": 1.5})
        )),
        json!([
            {"path": "", "message": "must have required property 'state'"},
            {"path": "/note", "message": "must NOT have more than 10000 characters"},
            {"path": "/version", "message": "must be integer"}
        ])
    );
    assert_eq!(
        issues(host.invoke(
            &ann(),
            "d1",
            "Approvals",
            "withdrawSignOff",
            json!({"note": "x"})
        )),
        json!([
            {"path": "", "message": "must NOT have additional properties: note"},
            {"path": "", "message": "must have required property 'state'"}
        ])
    );
    assert_eq!(
        issues(host.invoke(
            &ann(),
            "d1",
            "Approvals",
            "listSignOffs",
            json!({"limit": 501, "cursor": 5, "state": 1})
        )),
        json!([
            {"path": "/cursor", "message": "must be string"},
            {"path": "/limit", "message": "must be <= 500"},
            {"path": "/state", "message": "must be string"}
        ])
    );
    assert_eq!(
        issues(host.invoke(
            &ann(),
            "d1",
            "Approvals",
            "listSignOffs",
            json!({"limit": 0})
        )),
        json!([{"path": "/limit", "message": "must be >= 1"}])
    );
    // A pinned gate's signOff names the version its signer read.
    assert_eq!(
        refused(sign(&mut host, &ann(), json!({"state": "published"}))),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
            "message": "operation signOff of behavior Approvals: /version: is required: the gate on published is pinned, so a sign-off names the row's version its signer read",
            "details": {"issues": [{"path": "/version", "message": "is required: the gate on published is pinned, so a sign-off names the row's version its signer read"}]}})
    );
    assert_eq!(
        result(sign(
            &mut host,
            &ann(),
            json!({"state": "approved", "note": "Fine."})
        )),
        json!({"state": "approved", "subject": "ann", "note": "Fine.", "at": START, "signedBy": ["ann"], "count": 1})
    );
}

/// Each gated and from state is one of the Workflow's, each resetOn field
/// one of the type's own, and a gate that excludes the creator needs a
/// createdBy to read the creator from.
#[test]
fn its_gates_are_the_workflows_states_and_its_fields_the_types_own() {
    let ruled = |config: Value| rule(&core(), &doc(config), "Approvals");
    let all = "draft, review, approved, published";
    assert_eq!(
        ruled(json!({"gates": {"lost": gate(1)}})),
        format!("gate \"lost\" is not a state of the type's Workflow ({all})")
    );
    assert_eq!(
        ruled(
            json!({"gates": {"approved": {"count": 1, "permission": "p", "from": ["review", "gone"]}}})
        ),
        format!(
            "gate \"approved\": from state \"gone\" is not a state of the type's Workflow ({all})"
        )
    );
    assert_eq!(
        ruled(json!({"gates": {"approved": {"count": 1, "permission": "p", "from": ["approved"]}}})),
        "gate \"approved\": from names \"approved\" itself, and no transition leads from a state to itself"
    );
    assert_eq!(
        ruled(json!({"gates": {"approved": {"count": 1, "permission": "p", "resetOn": ["proof", "status"]}}})),
        "gate \"approved\": resetOn names \"status\", which is not a field of Doc (its fields: title, proof, body, createdBy)"
    );
    let bare = |gate: Value| {
        TypeSpec::new("Bare")
            .with(UseSpec::on_type(
                "Approvals",
                json!({"gates": {"approved": gate}}),
            ))
            .with(UseSpec::on_field("status", "Workflow", flow()))
            .with_field(FieldSpec::new("title", "string"))
    };
    assert_eq!(
        rule(&core(), &bare(gate(1)), "Approvals"),
        "gate \"approved\" excludes the creator (excludeCreator is true when absent), and Bare has no field createdBy to read the creator from: give Bare a createdBy field, or set excludeCreator to false"
    );
    assert!(define(
        &core(),
        &bare(json!({"count": 1, "permission": "p", "excludeCreator": false}))
    )
    .is_ok());
    // The Workflow it reads.
    let alone = TypeSpec::new("Doc")
        .with(UseSpec::on_type(
            "Approvals",
            json!({"gates": {"approved": gate(1)}}),
        ))
        .with_field(FieldSpec::new("createdBy", "string"));
    assert_eq!(
        rule(&core(), &alone, "Approvals"),
        "it reads the status of Workflow, which the type does not list"
    );
    let two = |config: Value| {
        doc(config).with(UseSpec::on_field(
            "phase",
            "Workflow",
            json!({"states": ["early", "late"], "transitions": [{"from": "early", "to": "late"}]}),
        ))
    };
    assert_eq!(
        rule(&core(), &two(json!({"gates": {"approved": gate(1)}})), "Approvals"),
        "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    assert_eq!(
        rule(
            &core(),
            &two(json!({"gates": {"approved": gate(1)}, "status": "phase"})),
            "Approvals"
        ),
        "gate \"approved\" is not a state of the type's Workflow (early, late)"
    );
    // Named among two, the gate holds that Workflow's moves alone.
    let mut host = hosted(Host::new(
        core(),
        two(json!({"gates": {"late": gate(1)}, "status": "phase"})),
    ));
    changed(transition(&mut host, "approved"));
    assert_eq!(
        veto_of(host.invoke(&alice(), "d1", "phase", "transition", json!({"to": "late"}))).0,
        "approvals_missing"
    );
}

// ---- the gate and signOff ----

/// A move into a gated state waits for its count of sign-offs, which the
/// read's gates list in the order they were given; signOff's checks come in
/// the engine's order; a withdrawal deletes its row.
#[test]
fn a_move_into_a_gated_state_waits_for_its_count_of_sign_offs() {
    // review is gated from approved alone, so the move from draft that
    // hosting made went through ungated.
    let mut host = hosting(json!({"gates": {
        "review": {"count": 1, "permission": "docs.approve", "from": ["approved"]},
        "approved": gate(2),
        "published": {"count": 1, "permission": "docs.publish", "from": ["approved"]}
    }}));
    assert_eq!(host.row("d1").owned["status"]["status"], "review");
    assert_eq!(gates(&host), json!({}));
    assert_eq!(
        refused(transition(&mut host, "approved")),
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": "behavior Approvals vetoes transition of Doc d1: Doc d1 cannot move to approved with 0 of the 2 sign-offs it needs",
            "details": {"behavior": "Approvals", "action": "transition",
                "reason": "Doc d1 cannot move to approved with 0 of the 2 sign-offs it needs",
                "vetoCode": "approvals_missing",
                "vetoDetails": {"state": "approved", "required": 2, "count": 0}}})
    );
    let no_gate = refused(sign(&mut host, &ann(), json!({"state": "draft"})));
    assert_eq!(
        no_gate["details"]["reason"],
        "draft is not a state Doc's Approvals gates (it gates review, approved, published)"
    );
    assert_eq!(no_gate["details"].get("vetoDetails"), None);
    assert_eq!(
        refused(sign(&mut host, &alice(), json!({"state": "approved"}))),
        json!({"class": "EngineError", "code": "forbidden", "message": "alice may not sign off on Doc d1 for approved: it needs permission docs.approve"})
    );
    changed(sign(&mut host, &ann(), json!({"state": "approved"})));
    assert_eq!(
        refused(sign(&mut host, &ann(), json!({"state": "approved"})))["details"]["reason"],
        "ann has signed off on Doc d1 for approved already"
    );
    assert_eq!(
        refused(host.invoke(
            &ben(),
            "d1",
            "Approvals",
            "withdrawSignOff",
            json!({"state": "approved"})
        ))["details"]["reason"],
        "ben has no sign-off on Doc d1 for approved to withdraw"
    );
    assert_eq!(gates(&host), json!({"approved": {"signedBy": ["ann"]}}));
    assert_eq!(
        result(sign(&mut host, &ben(), json!({"state": "approved"}))),
        json!({"state": "approved", "subject": "ben", "at": START, "signedBy": ["ann", "ben"], "count": 2})
    );
    changed(transition(&mut host, "approved"));
    assert_eq!(
        refused(sign(&mut host, &ann(), json!({"state": "approved"})))["details"]["reason"],
        "Doc d1 is approved already; a sign-off for it waits until it has left"
    );
    // Entering the gate's state consumed its sign-offs.
    assert_eq!(gates(&host), json!({}));
    assert_eq!(
        sign_offs(&host),
        json!([
            {"key": "0000000001", "values": {"state": "approved", "subject": "ann", "at": START, "consumedAt": START}},
            {"key": "0000000002", "values": {"state": "approved", "subject": "ben", "at": START, "consumedAt": START}}
        ])
    );
    // From approved, the moves its gates name from it are gated.
    assert_eq!(
        veto_of(transition(&mut host, "review")),
        (
            "approvals_missing".to_owned(),
            json!({"state": "review", "required": 1, "count": 0})
        )
    );
    assert_eq!(
        veto_of(transition(&mut host, "published")).0,
        "approvals_missing"
    );
    let signed = host.invoke(
        &caller("pat", &["docs.publish"]),
        "d1",
        "Approvals",
        "signOff",
        json!({"state": "published", "note": "Go."}),
    );
    changed(signed);
    assert_eq!(
        result(host.invoke(
            &caller("pat", &[]),
            "d1",
            "Approvals",
            "withdrawSignOff",
            json!({"state": "published"})
        )),
        json!({"state": "published", "subject": "pat", "note": "Go.", "at": START})
    );
    assert_eq!(sign_offs(&host).as_array().map(Vec::len), Some(2));
}

/// The creator a gate excludes is the subject createdBy holds: as text, or
/// as a UUID in either form; excludeCreator false lets it sign.
#[test]
fn a_gate_excludes_the_creator_createdby_names() {
    let mut host = hosting(
        json!({"gates": {"approved": gate(1), "published": {"count": 1, "permission": "docs.approve", "excludeCreator": false}}}),
    );
    for creator in [DANA, DANA_HYPHENATED] {
        assert_eq!(
            refused(sign(
                &mut host,
                &caller(creator, &["docs.approve"]),
                json!({"state": "approved"})
            )),
            json!({"class": "BehaviorVetoError", "code": "vetoed",
                "message": format!("behavior Approvals vetoes signOff of Doc d1: {creator} created Doc d1, and its gate on approved takes no sign-off from its creator"),
                "details": {"behavior": "Approvals", "action": "signOff",
                    "reason": format!("{creator} created Doc d1, and its gate on approved takes no sign-off from its creator"),
                    "vetoCode": "creator_cannot_sign"}})
        );
    }
    changed(sign(
        &mut host,
        &caller(DANA, &["docs.approve"]),
        json!({"state": "published"}),
    ));
    // A string createdBy compares its text.
    let spec = TypeSpec::new("Doc")
        .with(UseSpec::on_type(
            "Approvals",
            json!({"gates": {"approved": gate(1)}}),
        ))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("createdBy", "string"));
    let mut host = Host::new(core(), spec);
    changed(host.create(&alice(), "d1", json!({"createdBy": "ann"})));
    assert_eq!(
        veto_of(sign(&mut host, &ann(), json!({"state": "approved"}))).0,
        "creator_cannot_sign"
    );
    changed(sign(
        &mut host,
        &caller("Ann", &["docs.approve"]),
        json!({"state": "approved"}),
    ));
    // A row whose createdBy holds nothing excludes no creator.
    changed(host.create(&alice(), "d2", json!({})));
    changed(host.invoke(
        &ann(),
        "d2",
        "Approvals",
        "signOff",
        json!({"state": "approved"}),
    ));
}

// ---- what consumes a sign-off ----

/// An update consumes the sign-offs of a gate whose resetOn names a field
/// it changed, and keeps the others'; `[]` keeps them through every update;
/// a gate without resetOn follows resetOnChange, true when absent.
#[test]
fn reset_on_names_the_fields_whose_change_consumes_a_gates_sign_offs() {
    let mut host = hosting(json!({"gates": {
        "draft": {"count": 1, "permission": "docs.approve", "resetOn": []},
        "approved": {"count": 1, "permission": "docs.approve", "resetOn": ["proof"]},
        "published": gate(1)
    }}));
    for state in ["draft", "approved", "published"] {
        changed(sign(&mut host, &ann(), json!({ "state": state })));
    }
    changed(host.update_at(
        &alice(),
        START + 1,
        "d1",
        json!({"body": "Edited", "proof": "v1"}),
    ));
    assert_eq!(
        gates(&host),
        json!({"draft": {"signedBy": ["ann"]}, "approved": {"signedBy": ["ann"]}})
    );
    changed(host.update_at(&alice(), START + 2, "d1", json!({"proof": "v2"})));
    assert_eq!(gates(&host), json!({"draft": {"signedBy": ["ann"]}}));
    let consumed: Vec<Value> = sign_offs(&host)
        .as_array()
        .expect("rows")
        .iter()
        .map(|row| row["values"]["consumedAt"].clone())
        .collect();
    assert_eq!(consumed, [Value::Null, json!(START + 2), json!(START + 1)]);
    // Entering a gate's state consumes its sign-offs, whatever resetOn says.
    changed(transition(&mut host, "draft"));
    assert_eq!(gates(&host), json!({}));
    // resetOnChange false: a gate without resetOn keeps its sign-offs.
    let mut host = hosting(json!({"gates": {"approved": gate(1)}, "resetOnChange": false}));
    changed(sign(&mut host, &ann(), json!({"state": "approved"})));
    changed(host.update(&alice(), "d1", json!({"body": "Edited"})));
    assert_eq!(gates(&host), json!({"approved": {"signedBy": ["ann"]}}));
    // A gate with resetOn follows it whatever resetOnChange says.
    let mut host = hosting(
        json!({"gates": {"approved": {"count": 1, "permission": "docs.approve", "resetOn": ["proof"]}}, "resetOnChange": false}),
    );
    changed(sign(&mut host, &ann(), json!({"state": "approved"})));
    changed(host.update(&alice(), "d1", json!({"proof": "v2"})));
    assert_eq!(gates(&host), json!({}));
}

// Mover moves its row's status through Workflow's transition, as another
// use's call() does (a Reactions rule, a Lease's expiry).
struct Mover;

impl Behavior for Mover {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Mover").operation("advance", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, params: &Value) -> Hook<Value> {
        cx.call("status", "transition", params)
    }
}

/// A move another use makes through call() is gated as a caller's is, and
/// entering the state consumes the gate's sign-offs, from the row as the
/// change leaves it.
#[test]
fn a_move_another_use_makes_is_gated_and_consumes() {
    let spec =
        doc(json!({"gates": {"approved": gate(1)}})).with(UseSpec::on_type("test.Mover", None));
    let mut host = hosted(Host::new(core().with(Mover), spec));
    let advance = |host: &mut Host| {
        host.invoke(
            &alice(),
            "d1",
            "test.Mover",
            "advance",
            json!({"to": "approved"}),
        )
    };
    assert_eq!(veto_of(advance(&mut host)).0, "approvals_missing");
    changed(sign(&mut host, &ann(), json!({"state": "approved"})));
    assert_eq!(
        result(advance(&mut host)),
        json!({"from": "review", "to": "approved"})
    );
    assert_eq!(gates(&host), json!({}));
}

/// A sign-off for a gate a later build removed is consumed at the row's
/// next write through the core, and a read shows none of it before.
#[test]
fn a_removed_gates_sign_offs_are_consumed_at_the_next_write() {
    let mut host = hosting(
        json!({"gates": {"approved": gate(1), "published": gate(1)}, "resetOnChange": false}),
    );
    changed(sign(&mut host, &ann(), json!({"state": "published"})));
    changed(sign(&mut host, &ann(), json!({"state": "approved"})));
    host.spec = doc(json!({"gates": {"approved": gate(1)}, "resetOnChange": false}));
    assert_eq!(gates(&host), json!({"approved": {"signedBy": ["ann"]}}));
    changed(host.update_at(&alice(), START + 5, "d1", json!({"body": "Edited"})));
    assert_eq!(
        sign_offs(&host),
        json!([
            {"key": "0000000001", "values": {"state": "published", "subject": "ann", "at": START, "consumedAt": START + 5}},
            {"key": "0000000002", "values": {"state": "approved", "subject": "ann", "at": START}}
        ])
    );
}

/// Approvals joins a type with rows, which start with none, and cannot
/// leave one.
#[test]
fn approvals_joins_a_type_with_rows_and_does_not_leave_one() {
    let mut host = hosting(json!({"gates": {"approved": gate(1)}}));
    let with = host.spec.clone();
    let without = TypeSpec::new("Doc")
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("createdBy", "Identity.UUID"));
    let publish = |from: &TypeSpec| {
        Command::new(
            alice(),
            START,
            Action::Publish {
                from: Some(from.clone()),
            },
        )
    };
    let refusal = host
        .core
        .drive(&without, &publish(&with), &mut host.memory)
        .into_result()
        .expect_err("refused");
    assert_eq!(
        refusal.details.expect("details")["changes"][0]["message"],
        "behavior Approvals cannot be removed from type Doc, which has instances: the sign-offs its instances hold would stay behind with nothing to delete them"
    );
    assert!(host
        .core
        .drive(&with, &publish(&without), &mut host.memory)
        .into_result()
        .is_ok());
}

// ---- pinned and version ----

/// A pinned gate's sign-off names the row's version its signer read: one
/// that is not the row's is stale, checked before the gate's permission; a
/// sign-off is a write, which moves the version, so each later signer signs
/// against the version the one before left; a version on a gate that is not
/// pinned is held to it too.
#[test]
fn a_pinned_sign_off_names_the_version_its_signer_read() {
    let mut host = hosting(
        json!({"gates": {"approved": {"count": 2, "permission": "docs.approve", "pinned": true}, "published": gate(1)}}),
    );
    // created at 1, moved to review at 2.
    assert_eq!(host.row("d1").version, 2);
    assert_eq!(
        refused(sign(
            &mut host,
            &alice(),
            json!({"state": "approved", "version": 1})
        )),
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": "behavior Approvals vetoes signOff of Doc d1: Doc d1 is at version 2, not 1",
            "details": {"behavior": "Approvals", "action": "signOff",
                "reason": "Doc d1 is at version 2, not 1",
                "vetoCode": "stale",
                "vetoDetails": {"version": 1, "current": 2}}})
    );
    changed(sign(
        &mut host,
        &ann(),
        json!({"state": "approved", "version": 2}),
    ));
    assert_eq!(host.row("d1").version, 3);
    assert_eq!(
        veto_of(sign(
            &mut host,
            &ben(),
            json!({"state": "approved", "version": 2})
        )),
        ("stale".to_owned(), json!({"version": 2, "current": 3}))
    );
    changed(sign(
        &mut host,
        &ben(),
        json!({"state": "approved", "version": 3.0}),
    ));
    // Not pinned: no version is needed, and one given is held.
    changed(sign(&mut host, &ann(), json!({"state": "published"})));
    assert_eq!(
        veto_of(sign(
            &mut host,
            &ben(),
            json!({"state": "published", "version": 4})
        )),
        ("stale".to_owned(), json!({"version": 4, "current": 5}))
    );
    // A state no gate names goes on to no_gate whatever version it names.
    assert_eq!(
        veto_of(sign(
            &mut host,
            &ann(),
            json!({"state": "draft", "version": 1})
        ))
        .0,
        "no_gate"
    );
}

// ---- reads ----

/// listSignOffs pages oldest first, the consumed ones with them, filters by
/// state, refuses a cursor it did not write, and writes nothing.
#[test]
fn list_sign_offs_pages_by_the_sign_offs_number() {
    let mut host = hosting(json!({"gates": {"approved": gate(2), "published": gate(1)}}));
    changed(sign(
        &mut host,
        &ann(),
        json!({"state": "approved", "note": "First."}),
    ));
    changed(sign(&mut host, &ann(), json!({"state": "published"})));
    changed(sign(&mut host, &ben(), json!({"state": "approved"})));
    let version = host.row("d1").version;
    let list = |host: &mut Host, params: Value| {
        host.invoke(&alice(), "d1", "Approvals", "listSignOffs", params)
    };
    let first = changed(list(&mut host, json!({"limit": 2})));
    assert!(first.rows.is_empty() && first.sub_rows.is_empty());
    assert_eq!(
        first.result,
        json!({"items": [
            {"state": "approved", "subject": "ann", "note": "First.", "at": START},
            {"state": "published", "subject": "ann", "at": START}
        ], "next": "YWZ0ZXI6Mg"})
    );
    assert_eq!(
        result(list(&mut host, json!({"limit": 2, "cursor": "YWZ0ZXI6Mg"}))),
        json!({"items": [{"state": "approved", "subject": "ben", "at": START}], "next": null})
    );
    assert_eq!(
        result(list(&mut host, json!({"state": "approved"})))["items"]
            .as_array()
            .map(Vec::len),
        Some(2)
    );
    assert_eq!(
        refused(list(&mut host, json!({"cursor": "nope"}))),
        json!({"class": "OperationParamsError", "code": "invalid_argument",
            "message": "operation listSignOffs of behavior Approvals: /cursor: is not a cursor this operation returned",
            "details": {"issues": [{"path": "/cursor", "message": "is not a cursor this operation returned"}]}})
    );
    assert_eq!(host.row("d1").version, version);
    // The gates a read shows, by state where its first sign-off is.
    assert_eq!(
        gates(&host),
        json!({"approved": {"signedBy": ["ann", "ben"]}, "published": {"signedBy": ["ann"]}})
    );
}

// ---- composition ----

// Gate vetoes every move of its row's status: a guard a settled move never
// meets.
struct Gate;

impl Behavior for Gate {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("test.Gate").veto("gated")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(request
            .operation_of("status", "transition")
            .map(|_| Veto::new("it is gated").with_code("gated")))
    }
}

/// Workflow's repeat answers a move to the state a row is in before any
/// guard is asked, so neither StateRules nor Approvals, nor a gate that
/// vetoes every move, refuses a move that is no move, and nothing is
/// written.
#[test]
fn a_workflow_repeat_is_settled_before_any_guard() {
    let mut repeat = flow();
    repeat["repeat"] = json!("unchanged");
    let spec = TypeSpec::new("Doc")
        .with(UseSpec::on_type(
            "StateRules",
            json!({"states": {"review": {"required": ["body"]}}}),
        ))
        .with(UseSpec::on_type(
            "Approvals",
            json!({"gates": {"review": gate(1)}}),
        ))
        .with(UseSpec::on_type("test.Gate", None))
        .with(UseSpec::on_field("status", "Workflow", repeat))
        .with_field(FieldSpec::new("body", "string"))
        .with_field(FieldSpec::new("createdBy", "string"));
    let mut host = Host::new(core().with(Gate), spec);
    changed(host.create(&alice(), "d1", json!({})));
    let settled = changed(host.invoke(
        &alice(),
        "d1",
        "status",
        "transition",
        json!({"to": "draft"}),
    ));
    assert_eq!(
        settled.result,
        json!({"from": "draft", "to": "draft", "unchanged": true})
    );
    assert!(settled.rows.is_empty() && settled.sub_rows.is_empty());
    assert_eq!(host.row("d1").version, 1);
    // A move that is one meets the guards, StateRules' first.
    assert_eq!(veto_of(transition(&mut host, "review")).0, "missing_fields");
}

/// A Timers fire whose move the gate vetoes is recorded vetoed, and the
/// status stays.
#[test]
fn a_timers_fire_whose_move_approvals_vetoes_is_recorded_vetoed() {
    let spec = doc(json!({"gates": {"approved": gate(1)}})).with(UseSpec::on_type(
        "Timers",
        json!({"deadlines": {"approve": {"start": {"enters": "review"}, "afterMs": 1000, "then": {"transition": "approved"}}}}),
    ));
    let mut host = hosted(Host::new(core(), spec));
    let fired = host.fall_due(START + 1000, &caller("runner", &[]));
    assert_eq!(fired.len(), 1);
    changed(fired.into_iter().next().expect("a fire").1);
    assert_eq!(host.row("d1").owned["status"]["status"], "review");
    assert_eq!(
        host.read_fields("d1", "Timers", "deadlines")["deadlines"]["approve"]["outcome"],
        "vetoed"
    );
}
