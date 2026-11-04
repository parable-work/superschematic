//! Presence on the pure core (D103), through the core's public API over a
//! source in memory that keeps rows of several types, their sub-table rows
//! and the deadlines the core arms: the engine's rules (the first deadline
//! at create, a beat by the principal the row stands for and no other, on
//! a string field and on a UUID field in either form, a miss applied once
//! and only past its deadline, onMissed only from its from states and onBeat
//! back, a vetoed move leaving the row missed, the guard on principalField)
//! with the miss a deadline the core arms; and the upgrade (D104): ttlByState
//! on a beat in each state, on a transition into a longer and a shorter
//! state, on a transition another use's code makes, on a create in a listed
//! state, a missed row arming nothing, the fire reconciling a raw write of
//! the status, the Workflow `status` names among several; the define
//! refusals in their order, the config change, and the config and the
//! operations' parameters held to the declaration file's schemas.

mod support;
mod tables;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Behavior, Caller, Code, ConfigError, ConfigTarget, Core, Cx, Declaration, FieldSpec,
    GuardRequest, Hook, Outcome, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{core, presence, Presence};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

const T0: i64 = 1_000_000;
const TTL: i64 = 180_000;
const TURN: i64 = 900_000;

// A session is idle or in a turn while it beats, missing once it stops,
// and closed for good.
fn flow() -> Value {
    json!({
        "states": ["idle", "turn", "missing", "closed"],
        "transitions": [
            {"from": "idle", "to": "turn"},
            {"from": "turn", "to": "idle"},
            {"from": "idle", "to": "missing"},
            {"from": "turn", "to": "missing"},
            {"from": "missing", "to": "idle"},
            {"from": "idle", "to": "closed"},
            {"from": "turn", "to": "closed"},
            {"from": "missing", "to": "closed"}
        ]
    })
}

fn moves() -> Value {
    json!({
        "ttlMs": TTL,
        "principalField": "device",
        "onMissed": {"transition": "missing", "from": ["idle", "turn"]},
        "onBeat": {"transition": "idle", "from": ["missing"]}
    })
}

fn with(config: Value, more: Value) -> Value {
    let mut config = config;
    config
        .as_object_mut()
        .expect("an object")
        .extend(more.as_object().expect("an object").clone());
    config
}

// session is a table that implements Presence before its Workflow field,
// with the device it stands for, as a string or a UUID, and an agent.
fn session(config: Value, device: &str) -> TypeSpec {
    TypeSpec::new("Session")
        .with(UseSpec::on_type("Presence", config))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("device", device))
        .with_field(FieldSpec::new("agent", "string"))
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn named(subject: &str) -> Caller {
    Caller::new(subject, Vec::<String>::new())
}

fn runner() -> Caller {
    Caller::new("runner", Vec::<String>::new())
}

// hosting creates s1, for dev1, at T0.
fn hosting(config: Value) -> Host {
    hosting_on(core(), session(config, "string"), "dev1")
}

fn hosting_on(core: Core, spec: TypeSpec, device: &str) -> Host {
    let mut host = Host::new(core, [spec]);
    changed(host.create(
        "Session",
        &alice(),
        T0,
        "s1",
        json!({"device": device, "agent": "a"}),
    ));
    host
}

fn beat(host: &mut Host, who: &Caller, now: i64) -> Outcome {
    host.invoke("Session", who, now, "s1", "Presence", "beat", json!({}))
}

fn transition(host: &mut Host, now: i64, to: &str) -> Outcome {
    host.invoke(
        "Session",
        &alice(),
        now,
        "s1",
        "status",
        "transition",
        json!({ "to": to }),
    )
}

fn presence_of(host: &Host) -> Value {
    serde_json::to_value(host.row("Session", "s1").owned.get("Presence")).expect("its fields")
}

fn status_of(host: &Host) -> Value {
    host.owned("Session", "s1", "status", "status")
}

fn armed(host: &Host) -> Option<i64> {
    host.armed("Session", "s1", "Presence", presence::MISS)
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    let file: Value = serde_json::from_str(presence::DECLARATION).expect("it parses");
    assert_eq!(file["name"], "Presence");
    assert_eq!(
        Presence.declaration(),
        Declaration::new("Presence")
            .field("deadline")
            .field("lastBeatAt")
            .field("missed")
            .field("released")
            .arms_deadlines()
            .operation("beat", true)
            .operation("miss", true)
            .veto("no_principal")
            .veto("not_principal")
            .veto("principal_fixed")
    );
    let types: Vec<&str> = file["fields"]
        .as_array()
        .expect("fields")
        .iter()
        .map(|field| field["type"].as_str().expect("a type"))
        .collect();
    assert_eq!(
        types,
        [
            "Temporal.DateTime",
            "Temporal.DateTime",
            "boolean",
            "Generic.JSON"
        ]
    );
}

/// The checks Presence writes by hand are the declaration's configSchema,
/// keyword for keyword, so a keyword the file gains fails this test until
/// the checks take it; each keyword's refusal comes out in the schema
/// loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(presence::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let movement = json!({
        "additionalProperties": false,
        "properties": {
            "from": {"items": text, "minItems": 1, "type": "array", "uniqueItems": true},
            "transition": text
        },
        "required": ["transition", "from"],
        "type": "object"
    });
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "onBeat": movement,
                "onMissed": movement,
                "principalField": text,
                "releaseLeases": {
                    "items": {"pattern": "^[A-Za-z][A-Za-z0-9_-]{0,127}$", "type": "string"},
                    "type": "array",
                    "uniqueItems": true
                },
                "status": text,
                "sweepMs": {"minimum": 1000, "type": "integer"},
                "ttlByState": {
                    "additionalProperties": {"minimum": 1000, "type": "integer"},
                    "minProperties": 1,
                    "type": "object"
                },
                "ttlMs": {"minimum": 1000, "type": "integer"}
            },
            "required": ["ttlMs", "principalField"],
            "type": "object"
        })
    );
    assert_eq!(file.get("createParamsSchema"), None);
    let shaped = |config: Value| shape(&core(), &session(config, "string"));
    assert_eq!(
        shaped(json!({})),
        [
            "/config: must have required property 'principalField'",
            "/config: must have required property 'ttlMs'"
        ]
    );
    assert_eq!(shaped(json!(5)), ["/config: must be object"]);
    assert_eq!(
        shaped(json!({"ttlMs": 999.5, "principalField": "", "status": 1, "sweepMs": 10, "x": 1})),
        [
            "/config: unknown key \"x\"",
            "/config/principalField: must NOT have fewer than 1 characters",
            "/config/status: must be string",
            "/config/sweepMs: must be >= 1000",
            "/config/ttlMs: must be >= 1000",
            "/config/ttlMs: must be integer"
        ]
    );
    assert_eq!(
        shaped(with(
            moves(),
            json!({
                "onMissed": {"transition": "", "from": [], "x": 1},
                "onBeat": {"from": ["idle", "idle", 3]}
            })
        )),
        [
            "/config/onBeat: must have required property 'transition'",
            "/config/onBeat/from: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/onBeat/from/2: must be string",
            "/config/onMissed: unknown key \"x\"",
            "/config/onMissed/from: must NOT have fewer than 1 items",
            "/config/onMissed/transition: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(with(
            moves(),
            json!({"onMissed": [], "onBeat": {"transition": "idle", "from": "missing"}})
        )),
        [
            "/config/onBeat/from: must be array",
            "/config/onMissed: must be object"
        ]
    );
    assert_eq!(
        shaped(with(
            moves(),
            json!({"releaseLeases": ["Job", "2nd", 5, "Job"]})
        )),
        [
            "/config/releaseLeases: must NOT have duplicate items (items ## 3 and 0 are identical)",
            "/config/releaseLeases/1: must match pattern \"^[A-Za-z][A-Za-z0-9_-]{0,127}$\"",
            "/config/releaseLeases/2: must be string"
        ]
    );
    assert_eq!(
        shaped(with(moves(), json!({"releaseLeases": "Job"}))),
        ["/config/releaseLeases: must be array"]
    );
    assert_eq!(
        shaped(with(
            moves(),
            json!({"ttlByState": {"turn": 500, "idle": "long"}})
        )),
        [
            "/config/ttlByState/idle: must be integer",
            "/config/ttlByState/turn: must be >= 1000"
        ]
    );
    assert_eq!(
        shaped(with(moves(), json!({"ttlByState": {}}))),
        ["/config/ttlByState: must NOT have fewer than 1 properties"]
    );
    assert_eq!(
        shaped(with(moves(), json!({"ttlByState": [1]}))),
        ["/config/ttlByState: must be object"]
    );
}

/// beat's and miss's closed paramsSchemas, in the behavior registry's
/// words.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(presence::DECLARATION).expect("it parses");
    for operation in file["operations"].as_array().expect("operations") {
        assert_eq!(
            undescribed(&operation["paramsSchema"]),
            json!({"additionalProperties": false, "type": "object"}),
            "{}",
            operation["name"]
        );
    }
    let mut host = hosting(moves());
    for operation in ["beat", "miss"] {
        let error = refused(host.invoke(
            "Session",
            &named("dev1"),
            T0,
            "s1",
            "Presence",
            operation,
            json!({"subject": "dev1"}),
        ));
        assert_eq!(error["class"], "OperationParamsError");
        assert_eq!(
            error["message"],
            format!("operation {operation} of behavior Presence: must NOT have additional properties: subject")
        );
        let error = refused(host.invoke(
            "Session",
            &named("dev1"),
            T0,
            "s1",
            "Presence",
            operation,
            json!([]),
        ));
        assert_eq!(
            error["message"],
            format!("operation {operation} of behavior Presence: must be object")
        );
    }
}

// ---- define ----

#[test]
fn define_holds_the_config_to_the_type_in_its_order() {
    let reason = |spec: TypeSpec| rule(&core(), &spec, "Presence");
    // sweepMs is the engine's runner's, refused before the rest.
    assert_eq!(
        reason(session(with(moves(), json!({"sweepMs": 5000, "principalField": "nobody"})), "string")),
        "sweepMs is the engine's runner's, which missed the instances past their deadline every sweepMs: on a table the API's @behaviorWorker runs each row's miss as its deadline falls due, so a table's Presence takes no sweepMs (D104)"
    );
    assert_eq!(
        reason(session(
            with(moves(), json!({"principalField": "nobody"})),
            "string"
        )),
        "principalField \"nobody\" is not a field of Session (its fields: device, agent)"
    );
    assert_eq!(
        reason(session(moves(), "Generic.Int64")),
        "principalField \"device\" is not a string field of Session"
    );
    // A UUID field holds a subject.
    define(&core(), &session(moves(), "Identity.UUID")).expect("a UUID field");
    define(&core(), &session(moves(), "Identity.UserID")).expect("a UserID field");
    // With no Workflow, the first of the members that read one.
    let bare = |config: Value| {
        TypeSpec::new("Session")
            .with(UseSpec::on_type("Presence", config))
            .with_field(FieldSpec::new("device", "string"))
    };
    assert_eq!(
        reason(bare(moves())),
        "onMissed moves the status through Workflow, which the type does not list"
    );
    assert_eq!(
        reason(bare(
            json!({"ttlMs": TTL, "principalField": "device", "onBeat": {"transition": "idle", "from": ["missing"]}, "ttlByState": {"turn": TURN}})
        )),
        "onBeat moves the status through Workflow, which the type does not list"
    );
    assert_eq!(
        reason(bare(
            json!({"ttlMs": TTL, "principalField": "device", "ttlByState": {"turn": TURN}})
        )),
        "ttlByState times the states of Workflow, which the type does not list"
    );
    define(
        &core(),
        &bare(json!({"ttlMs": TTL, "principalField": "device"})),
    )
    .expect("no Workflow read");
    // The moves, in the engine's words.
    let on_missed = |rule: Value| {
        reason(session(
            with(moves(), json!({ "onMissed": rule })),
            "string",
        ))
    };
    assert_eq!(
        on_missed(json!({"transition": "lost", "from": ["idle"]})),
        "onMissed.transition \"lost\" is not a state of the type's Workflow (idle, turn, missing, closed)"
    );
    assert_eq!(
        on_missed(json!({"transition": "missing", "from": ["asleep"]})),
        "onMissed.from names \"asleep\", which is not a state of the type's Workflow (idle, turn, missing, closed)"
    );
    assert_eq!(
        on_missed(json!({"transition": "missing", "from": ["missing"]})),
        "onMissed moves the status from \"missing\" to itself"
    );
    assert_eq!(
        on_missed(json!({"transition": "missing", "from": ["closed"]})),
        "onMissed: no transition of the type's Workflow leads from \"closed\" to \"missing\""
    );
    assert_eq!(
        reason(session(
            with(
                moves(),
                json!({"onBeat": {"transition": "turn", "from": ["missing"]}})
            ),
            "string"
        )),
        "onBeat: no transition of the type's Workflow leads from \"missing\" to \"turn\""
    );
    // ttlByState's states, after the moves.
    assert_eq!(
        reason(session(
            with(
                moves(),
                json!({"ttlByState": {"turn": TURN, "lunch": 600_000}})
            ),
            "string"
        )),
        "ttlByState: \"lunch\" is not a state of the type's Workflow (idle, turn, missing, closed)"
    );
    // status names a Workflow of the type.
    assert_eq!(
        reason(session(with(moves(), json!({"status": "phase"})), "string")),
        "status \"phase\" is not a Workflow of the type (its Workflows: status)"
    );
    define(
        &core(),
        &session(
            with(
                moves(),
                json!({"ttlByState": {"turn": TURN}, "status": "status"}),
            ),
            "string",
        ),
    )
    .expect("a full config");
}

/// With two Workflows, status names the one Presence reads and moves; with
/// none named, the config is refused.
#[test]
fn status_names_the_workflow_among_several() {
    let two = |config: Value| {
        TypeSpec::new("Session")
            .with(UseSpec::on_type("Presence", config))
            .with(UseSpec::on_field("status", "Workflow", flow()))
            .with(UseSpec::on_field(
                "billing",
                "Workflow",
                json!({"states": ["trial", "paid"], "transitions": [{"from": "trial", "to": "paid"}]}),
            ))
            .with_field(FieldSpec::new("device", "string"))
            .with_field(FieldSpec::new("agent", "string"))
    };
    assert_eq!(
        rule(&core(), &two(moves()), "Presence"),
        "it reads the status of Workflow, and the type has several (status, billing): status names the field whose Workflow it reads"
    );
    assert_eq!(
        rule(
            &core(),
            &two(with(moves(), json!({"status": "billing"}))),
            "Presence"
        ),
        "onMissed.transition \"missing\" is not a state of the type's Workflow (trial, paid)"
    );
    let config = with(
        moves(),
        json!({"status": "status", "ttlByState": {"turn": TURN}}),
    );
    let mut host = hosting_on(core(), two(config), "dev1");
    changed(transition(&mut host, T0 + 1000, "turn"));
    assert_eq!(armed(&host), Some(T0 + 1000 + TURN));
    let missed = host.fall_due(T0 + 1000 + TURN, &runner());
    assert_eq!(missed.len(), 1);
    changed(missed.into_iter().next().expect("a miss").2);
    assert_eq!(status_of(&host), "missing");
    assert_eq!(host.owned("Session", "s1", "billing", "status"), "trial");
}

#[test]
fn principal_field_changes_only_on_a_type_with_no_rows() {
    let spec = session(moves(), "string");
    // Through the core's publish check: with a row, refused; without, allowed.
    let publish = |rows: bool| {
        let mut host = Host::new(core(), [spec.clone()]);
        if rows {
            changed(host.create("Session", &alice(), T0, "s1", json!({"device": "dev1"})));
        }
        let mut next = spec.clone();
        next.uses[0].config = Some(with(moves(), json!({"principalField": "agent"})));
        host.tables.specs.insert("Session".into(), next);
        host.run(
            "Session",
            &alice(),
            T0,
            Action::Publish {
                from: Some(spec.clone()),
            },
        )
    };
    let error = refused(publish(true));
    assert_eq!(error["code"], "incompatible_change");
    assert!(
        error["message"].as_str().unwrap().contains(
            "principalField is where every instance holds its principal: it stays device"
        ),
        "{error}"
    );
    changed(publish(false));
}

// ---- the engine's rules ----

#[test]
fn a_create_arms_the_miss_ttl_ms_on_and_only_the_principal_beats_it() {
    let mut host = hosting(moves());
    assert_eq!(
        presence_of(&host),
        json!({"missed": false, "deadline": T0 + TTL})
    );
    assert_eq!(armed(&host), Some(T0 + TTL));
    // Another principal, whatever it holds.
    let (code, details) = veto_of(beat(&mut host, &named("dev2"), T0 + 1000));
    assert_eq!((code.as_str(), details), ("not_principal", Value::Null));
    let error = refused(beat(&mut host, &named("dev2"), T0 + 1000));
    assert_eq!(
        error["message"],
        "behavior Presence vetoes beat of Session s1: it stands for another principal, who alone may beat it"
    );
    // The principal, by its text exactly.
    veto_of(beat(&mut host, &named("DEV1"), T0 + 1000));
    assert_eq!(
        result(beat(&mut host, &named("dev1"), T0 + 60_000)),
        json!({ "deadline": T0 + 60_000 + TTL })
    );
    assert_eq!(
        presence_of(&host),
        json!({"missed": false, "deadline": T0 + 60_000 + TTL, "lastBeatAt": T0 + 60_000})
    );
    assert_eq!(armed(&host), Some(T0 + 60_000 + TTL));
    // Without ttlByState a transition arms nothing.
    changed(transition(&mut host, T0 + 70_000, "turn"));
    assert_eq!(armed(&host), Some(T0 + 60_000 + TTL));
    assert_eq!(
        host.owned("Session", "s1", "Presence", "deadline"),
        T0 + 60_000 + TTL
    );
}

#[test]
fn a_row_with_no_principal_is_beaten_by_no_one() {
    let mut host = Host::new(core(), [session(moves(), "string")]);
    changed(host.create("Session", &alice(), T0, "s1", json!({"agent": "a"})));
    let error = refused(beat(&mut host, &named("dev1"), T0));
    assert_eq!(error["details"]["vetoCode"], "no_principal");
    assert_eq!(
        error["details"]["reason"],
        "its device holds no principal, so no one may beat it"
    );
    // A principal set once is fixed; one set where none was is not a change.
    changed(host.update("Session", &alice(), T0, "s1", json!({"device": "dev1"})));
    changed(host.update(
        "Session",
        &alice(),
        T0,
        "s1",
        json!({"device": "dev1", "agent": "b"}),
    ));
    let error = refused(host.update("Session", &alice(), T0, "s1", json!({"device": "dev2"})));
    assert_eq!(
        error["message"],
        "behavior Presence vetoes update of Session s1: device holds the principal the instance stands for, so it cannot change once set"
    );
    assert_eq!(error["details"]["vetoCode"], "principal_fixed");
    assert_eq!(error["details"].get("vetoDetails"), None);
    refused(host.update("Session", &alice(), T0, "s1", json!({"device": null})));
}

#[test]
fn a_uuid_field_names_its_principal_in_either_form() {
    const BASE62: &str = "TMXp8QPR6yGvKuV2GZQ10";
    const HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
    for held in [BASE62, HYPHENATED] {
        let mut host = hosting_on(core(), session(moves(), "Identity.UUID"), held);
        changed(beat(&mut host, &named(BASE62), T0 + 1000));
        changed(beat(&mut host, &named(HYPHENATED), T0 + 2000));
        changed(beat(
            &mut host,
            &named(&HYPHENATED.to_uppercase()),
            T0 + 3000,
        ));
        veto_of(beat(&mut host, &named("3n9R8RlRyqVJBUQUv6toL1"), T0 + 4000));
    }
}

#[test]
fn a_miss_falls_due_at_the_deadline_once_and_moves_the_status_from_its_states() {
    let mut host = hosting(moves());
    changed(beat(&mut host, &named("dev1"), T0 + 10_000));
    // A caller's miss before the deadline changes nothing, and writes
    // nothing.
    let early = host.invoke(
        "Session",
        &alice(),
        T0 + 20_000,
        "s1",
        "Presence",
        "miss",
        json!({}),
    );
    let early = changed(early);
    assert_eq!(early.result, json!({"missed": false}));
    assert!(early.rows.is_empty());
    assert!(host.fall_due(T0 + 10_000 + TTL - 1, &runner()).is_empty());
    let due = host.fall_due(T0 + 10_000 + TTL, &runner());
    assert_eq!(due.len(), 1);
    changed(due.into_iter().next().expect("a miss").2);
    assert_eq!(status_of(&host), "missing");
    assert_eq!(
        presence_of(&host),
        json!({"missed": true, "deadline": T0 + 10_000 + TTL, "lastBeatAt": T0 + 10_000, "released": {}})
    );
    assert_eq!(armed(&host), None);
    // Missed already: once.
    let again = host.invoke(
        "Session",
        &alice(),
        T0 + 500_000,
        "s1",
        "Presence",
        "miss",
        json!({}),
    );
    assert_eq!(changed(again).result, json!({"missed": false}));
    // The next beat clears missed and onBeat moves it back.
    assert_eq!(
        result(beat(&mut host, &named("dev1"), T0 + 600_000)),
        json!({"deadline": T0 + 600_000 + TTL})
    );
    assert_eq!(status_of(&host), "idle");
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), false);
    assert_eq!(armed(&host), Some(T0 + 600_000 + TTL));
    // From a state onMissed does not move from, the miss leaves the status.
    changed(transition(&mut host, T0 + 600_000, "closed"));
    changed(host.fall_due(T0 + 600_000 + TTL, &runner()).remove(0).2);
    assert_eq!(status_of(&host), "closed");
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), true);
    // A beat in a state onBeat does not move from leaves it too.
    changed(beat(&mut host, &named("dev1"), T0 + 900_000));
    assert_eq!(status_of(&host), "closed");
}

// Gate is a test behavior that vetoes Workflow's transition into missing,
// whoever makes it: a guard a miss's move meets.
struct Gate;

impl Behavior for Gate {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Gate").veto("held")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, _view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        Ok(request
            .operation_of("status", "transition")
            .filter(|params| params["to"] == "missing")
            .map(|_| Veto::new("it is held").with_code("held")))
    }
}

#[test]
fn a_vetoed_move_leaves_the_status_and_the_row_missed_all_the_same() {
    let spec = session(moves(), "string").with(UseSpec::on_type("Gate", None));
    let mut host = hosting_on(core().with(Gate), spec, "dev1");
    changed(host.fall_due(T0 + TTL, &runner()).remove(0).2);
    assert_eq!(status_of(&host), "idle");
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), true);
}

// ---- ttlByState (D104) ----

fn by_state() -> Value {
    with(moves(), json!({"ttlByState": {"turn": TURN}}))
}

#[test]
fn ttl_by_state_arms_the_deadline_the_rows_state_gives_on_a_beat_and_a_transition() {
    let mut host = hosting(by_state());
    // The create, in idle, which ttlByState does not list: ttlMs from the
    // entry.
    assert_eq!(
        presence_of(&host),
        json!({"missed": false, "deadline": T0 + TTL})
    );
    assert_eq!(armed(&host), Some(T0 + TTL));
    // A beat in idle.
    assert_eq!(
        result(beat(&mut host, &named("dev1"), T0 + 60_000)),
        json!({"deadline": T0 + 60_000 + TTL})
    );
    // Into the longer state: from the entry.
    changed(transition(&mut host, T0 + 120_000, "turn"));
    assert_eq!(armed(&host), Some(T0 + 120_000 + TURN));
    assert_eq!(
        host.owned("Session", "s1", "Presence", "deadline"),
        T0 + 120_000 + TURN
    );
    // A beat in turn: from the beat.
    assert_eq!(
        result(beat(&mut host, &named("dev1"), T0 + 600_000)),
        json!({"deadline": T0 + 600_000 + TURN})
    );
    // Into the shorter state: from the entry, the later sighting.
    changed(transition(&mut host, T0 + 700_000, "idle"));
    assert_eq!(armed(&host), Some(T0 + 700_000 + TTL));
    assert!(host.fall_due(T0 + 700_000 + TTL - 1, &runner()).is_empty());
    changed(host.fall_due(T0 + 700_000 + TTL, &runner()).remove(0).2);
    assert_eq!(status_of(&host), "missing");
    assert_eq!(
        presence_of(&host),
        json!({"missed": true, "deadline": T0 + 700_000 + TTL, "lastBeatAt": T0 + 600_000, "released": {}})
    );
    // A missed row arms nothing, whatever moves it, until its next beat.
    assert_eq!(armed(&host), None);
    changed(transition(&mut host, T0 + 890_000, "idle"));
    assert_eq!(armed(&host), None);
    changed(transition(&mut host, T0 + 891_000, "turn"));
    assert_eq!(armed(&host), None);
    assert_eq!(
        result(beat(&mut host, &named("dev1"), T0 + 900_000)),
        json!({"deadline": T0 + 900_000 + TURN})
    );
    assert_eq!(armed(&host), Some(T0 + 900_000 + TURN));
}

#[test]
fn ttl_by_state_on_a_create_in_a_listed_state_counts_from_the_create() {
    let initial = json!({
        "states": ["idle", "turn", "missing", "closed"],
        "initial": "turn",
        "transitions": flow()["transitions"].clone()
    });
    let spec = TypeSpec::new("Session")
        .with(UseSpec::on_type("Presence", by_state()))
        .with(UseSpec::on_field("status", "Workflow", initial))
        .with_field(FieldSpec::new("device", "string"))
        .with_field(FieldSpec::new("agent", "string"));
    let host = hosting_on(core(), spec, "dev1");
    assert_eq!(status_of(&host), "turn");
    assert_eq!(armed(&host), Some(T0 + TURN));
    assert_eq!(
        host.owned("Session", "s1", "Presence", "deadline"),
        T0 + TURN
    );
}

// Mover is a test behavior whose operation moves the row's Workflow through
// a call: a transition another use's code makes.
struct Mover;

impl Behavior for Mover {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Mover").operation("move", true)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, _name: &str, params: &Value) -> Hook<Value> {
        cx.call("status", "transition", &json!({ "to": params["to"] }))?;
        Ok(json!({}))
    }
}

#[test]
fn a_transition_another_use_makes_arms_the_deadline_again() {
    let spec = session(by_state(), "string").with(UseSpec::on_type("Mover", None));
    let mut host = hosting_on(core().with(Mover), spec, "dev1");
    changed(host.invoke(
        "Session",
        &alice(),
        T0 + 5000,
        "s1",
        "Mover",
        "move",
        json!({"to": "turn"}),
    ));
    assert_eq!(status_of(&host), "turn");
    assert_eq!(armed(&host), Some(T0 + 5000 + TURN));
}

#[test]
fn the_fire_arms_again_where_a_raw_write_moved_the_row_into_a_longer_state() {
    let mut host = hosting(by_state());
    assert_eq!(armed(&host), Some(T0 + TTL));
    // A raw SQL write of the status, which Presence does not hear.
    let row = host
        .tables
        .rows
        .get_mut(&("Session".to_owned(), "s1".to_owned()))
        .expect("the row");
    let status = row.owned.get_mut("status").expect("its Workflow");
    status.insert("status".into(), json!("turn"));
    status.insert("enteredAt".into(), json!(T0 + 30_000));
    let fired = host.fall_due(T0 + TTL, &runner());
    let changes = changed(fired.into_iter().next().expect("a fire").2);
    assert!(changes.result.is_null());
    assert_eq!(armed(&host), Some(T0 + 30_000 + TURN));
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), false);
    assert_eq!(status_of(&host), "turn");
    // At the new deadline it misses.
    changed(host.fall_due(T0 + 30_000 + TURN, &runner()).remove(0).2);
    assert_eq!(status_of(&host), "missing");
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), true);
}

#[test]
fn a_deadline_presence_does_not_arm_is_a_defect() {
    let mut host = hosting(moves());
    let outcome = host.run(
        "Session",
        &runner(),
        T0,
        Action::Deadline {
            key: "s1".into(),
            use_id: "Presence".into(),
            name: "expire".into(),
            at: T0,
            payload: Value::Null,
        },
    );
    let refusal = outcome.into_result().expect_err("a defect");
    assert_eq!(refusal.code, Code::BehaviorError);
    assert!(
        refusal
            .message
            .contains("deadline expire fell due, and Presence arms only miss"),
        "{}",
        refusal.message
    );
}

// ---- releaseLeases, through Lease's own expire ----

// DEVICE is a UUID in base62, as a table's adapter gives a UUID column's
// value, and DEVICE_HYPHENATED the same one hyphenated, as a deployment's
// authenticator may give its subject.
const DEVICE: &str = "TMXp8QPR6yGvKuV2GZQ10";
const DEVICE_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";
const SHORT: i64 = 30_000;

fn job() -> TypeSpec {
    TypeSpec::new("Job")
        .with(UseSpec::on_type(
            "Lease",
            json!({"ttlMs": 600_000, "overridePermission": "jobs.override"}),
        ))
        .with_field(FieldSpec::new("title", "string"))
}

fn device_session(releases: Value) -> TypeSpec {
    session(
        with(moves(), json!({"ttlMs": SHORT, "releaseLeases": releases})),
        "Identity.UUID",
    )
}

// leasing hosts Session s1 for DEVICE and jobs j1 to j4: j1 leased by the
// device under its hyphenated subject at T0, j2 under its base62 one, j3
// by another principal, and j4 by the device and renewed after its beat.
fn leasing(releases: Value) -> Host {
    let mut host = Host::new(core(), [device_session(releases), job()]);
    changed(host.create(
        "Session",
        &alice(),
        T0,
        "s1",
        json!({"device": DEVICE, "agent": "a"}),
    ));
    for key in ["j1", "j2", "j3", "j4"] {
        changed(host.create("Job", &alice(), T0, key, json!({"title": key})));
    }
    let acquire = |host: &mut Host, who: &str, key: &str| {
        result(host.invoke("Job", &named(who), T0, key, "Lease", "acquire", json!({})))
    };
    acquire(&mut host, DEVICE_HYPHENATED, "j1");
    acquire(&mut host, DEVICE, "j2");
    acquire(&mut host, "otto", "j3");
    acquire(&mut host, DEVICE, "j4");
    changed(beat(&mut host, &named(DEVICE), T0 + 1000));
    changed(host.invoke_fenced(
        "Job",
        &named(DEVICE),
        T0 + 2000,
        "j4",
        "Lease",
        "heartbeat",
        json!({}),
        1,
    ));
    host
}

fn holder(host: &Host, key: &str) -> Value {
    host.owned("Job", key, "Lease", "holder")
}

#[test]
fn a_miss_expires_the_leases_the_principal_holds_in_either_form_but_one_renewed_since() {
    let mut host = leasing(json!(["Job"]));
    let worker = caller_with("worker", &["jobs.override"]);
    let due = host.fall_due(T0 + 1000 + SHORT, &worker);
    assert_eq!(due.len(), 1);
    let changes = changed(due.into_iter().next().expect("a miss").2);
    // The Job rows the miss expired are the deadline command's others.
    assert!(changes.row("Job", "j1").is_some() && changes.row("Job", "j2").is_some());
    assert_eq!(
        host.owned("Session", "s1", "Presence", "released"),
        json!({"Job": ["j1", "j2"]})
    );
    assert_eq!(status_of(&host), "missing");
    assert_eq!(holder(&host, "j1"), Value::Null);
    assert_eq!(holder(&host, "j2"), Value::Null);
    assert_eq!(holder(&host, "j3"), "otto");
    // Renewed after the device's last beat: alive, and spared.
    assert_eq!(holder(&host, "j4"), DEVICE);
    assert_eq!(
        host.owned("Job", "j1", "Lease", "ended"),
        json!({"reason": "holder", "at": T0 + 1000 + SHORT})
    );
}

/// A lease held under the principal's uppercase hyphenated subject, as an
/// authenticator may give it, is found and expired too.
#[test]
fn a_miss_finds_a_lease_held_under_an_uppercase_subject() {
    let upper = DEVICE_HYPHENATED.to_uppercase();
    let mut host = Host::new(core(), [device_session(json!(["Job"])), job()]);
    changed(host.create(
        "Session",
        &alice(),
        T0,
        "s1",
        json!({"device": DEVICE, "agent": "a"}),
    ));
    changed(host.create("Job", &alice(), T0, "j1", json!({"title": "j1"})));
    result(host.invoke(
        "Job",
        &named(&upper),
        T0,
        "j1",
        "Lease",
        "acquire",
        json!({}),
    ));
    changed(beat(&mut host, &named(&upper), T0 + 1000));
    let worker = caller_with("worker", &["jobs.override"]);
    changed(host.fall_due(T0 + 1000 + SHORT, &worker).remove(0).2);
    assert_eq!(
        host.owned("Session", "s1", "Presence", "released"),
        json!({"Job": ["j1"]})
    );
    assert_eq!(holder(&host, "j1"), Value::Null);
}

#[test]
fn a_miss_needs_the_workers_override_and_a_failed_one_leaves_the_row_as_it_was() {
    let mut host = leasing(json!(["Job"]));
    let due = host.fall_due(T0 + 1000 + SHORT, &runner());
    let error = refused(due.into_iter().next().expect("a miss").2);
    assert_eq!(error["code"], "forbidden");
    assert_eq!(
        error["message"],
        "runner may not expire the lease of another holder of Job: it needs permission jobs.override"
    );
    assert_eq!(host.owned("Session", "s1", "Presence", "missed"), false);
    assert_eq!(status_of(&host), "idle");
    assert_eq!(holder(&host, "j1"), DEVICE_HYPHENATED);
    // The deadline stays armed for the worker to try again.
    assert_eq!(armed(&host), Some(T0 + 1000 + SHORT));
}

#[test]
fn a_miss_leaves_the_leases_while_another_row_of_the_principal_is_present() {
    let mut host = leasing(json!(["Job"]));
    changed(host.create(
        "Session",
        &alice(),
        T0 + 5000,
        "s2",
        json!({"device": DEVICE, "agent": "b"}),
    ));
    let worker = caller_with("worker", &["jobs.override"]);
    changed(host.fall_due(T0 + 1000 + SHORT, &worker).remove(0).2);
    assert_eq!(
        host.owned("Session", "s1", "Presence", "released"),
        json!({})
    );
    assert_eq!(holder(&host, "j1"), DEVICE_HYPHENATED);
    // When that one is missed too, the leases go, the one renewed before
    // s2's create, the principal's last sight, among them.
    changed(host.fall_due(T0 + 5000 + SHORT, &worker).remove(0).2);
    assert_eq!(
        host.owned("Session", "s2", "Presence", "released"),
        json!({"Job": ["j1", "j2", "j4"]})
    );
    assert_eq!(holder(&host, "j4"), Value::Null);
    assert_eq!(holder(&host, "j3"), "otto");
}

#[test]
fn a_release_leases_name_the_core_cannot_follow_is_a_defect() {
    let mut host = leasing(json!(["Ghost"]));
    let worker = caller_with("worker", &["jobs.override"]);
    let refusal = host
        .fall_due(T0 + 1000 + SHORT, &worker)
        .remove(0)
        .2
        .into_result()
        .expect_err("a defect");
    assert_eq!(refusal.code, Code::BehaviorError);
    assert!(
        refusal
            .message
            .contains("releaseLeases names Ghost, which is no table of the core's layout"),
        "{}",
        refusal.message
    );
    let mut host = Host::new(
        core(),
        [
            device_session(json!(["Note"])),
            TypeSpec::new("Note").with_field(FieldSpec::new("title", "string")),
        ],
    );
    changed(host.create("Session", &alice(), T0, "s1", json!({"device": DEVICE})));
    let refusal = host
        .fall_due(T0 + SHORT, &worker)
        .remove(0)
        .2
        .into_result()
        .expect_err("a defect");
    assert!(
        refusal
            .message
            .contains("releaseLeases names Note, which implements no Lease"),
        "{}",
        refusal.message
    );
}

fn caller_with(subject: &str, permissions: &[&str]) -> Caller {
    Caller::new(subject, permissions.iter().copied())
}

// A row that implements Lease and Presence: the miss's deadline calls the
// use's own miss, which Lease's guard holds as the worker's write while
// another principal holds the row's lease, unless Lease exempts it.
#[test]
fn on_a_leased_row_the_miss_is_the_workers_write_unless_lease_exempts_it() {
    let leased = |exempt: Value| {
        TypeSpec::new("Worker")
            .with(UseSpec::on_type(
                "Lease",
                json!({"ttlMs": 600_000, "exempt": exempt}),
            ))
            .with(UseSpec::on_type(
                "Presence",
                json!({"ttlMs": SHORT, "principalField": "device"}),
            ))
            .with_field(FieldSpec::new("device", "string"))
    };
    for (exempt, missed) in [(json!([]), false), (json!(["Presence.miss"]), true)] {
        let mut host = Host::new(core(), [leased(exempt)]);
        changed(host.create("Worker", &alice(), T0, "w1", json!({"device": "dev1"})));
        changed(host.invoke(
            "Worker",
            &named("otto"),
            T0,
            "w1",
            "Lease",
            "acquire",
            json!({}),
        ));
        let fired = host.fall_due(T0 + SHORT, &runner()).remove(0).2;
        if missed {
            changed(fired);
        } else {
            let error = refused(fired);
            assert_eq!(error["details"]["vetoCode"], "held_by_another");
        }
        assert_eq!(host.owned("Worker", "w1", "Presence", "missed"), missed);
    }
}
