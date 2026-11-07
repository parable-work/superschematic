//! Lease on the pure core (D103, D104), through the core's public API over
//! a source in memory that keeps rows, their directives' sub-table rows and
//! the deadlines the core arms: acquire, heartbeat (its renewal under a
//! hold, by the length the last renewal gave), release, abandon, expire and
//! its reasons, the expiry's deadline armed, moved, cleared and fired (on a
//! lapsed lease, a free row, a lease whose deadline moved, and vetoed by a
//! guard, which refuses the deadline), directives (deduped, acknowledged,
//! deleted at an end), the fencing token (stale on a free row, passed for a
//! read-only operation, required under requireToken and for a heartbeat,
//! absent in a behavior's call), the guard's rule for a use's call of its
//! own operation, the active field a read computes at its time, a row with
//! no token reading as 0, the Workflow its moves name among two, a Timers
//! fire the holder's lease vetoes; expireHolder, an operation of the table
//! as a whole run as a command without a row (D104), over a host of the
//! table's rows; and the config, the operations' parameters and the
//! precondition held to the declaration file's schemas.

mod support;
mod tables;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Behavior, Caller, ConfigError, ConfigTarget, Cx, Declaration, FieldSpec, GuardRequest,
    Hook, Outcome, Row, TableDecl, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{lease, Lease, HOLD_TYPES};
use support::{
    caller, changed, define, object, refused, result, rule, shape, undescribed, veto_of, Host,
    START,
};

// A job is queued, run, done or failed.
fn flow() -> Value {
    json!({
        "states": ["queued", "running", "done", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"},
            {"from": "running", "to": "failed"},
            {"from": "queued", "to": "failed"}
        ]
    })
}

// A job as a table holds it: Lease on the type before its Workflow field,
// its title and the time limit of its hold.
fn job(config: Value) -> TypeSpec {
    TypeSpec::new("Job")
        .with(UseSpec::on_type("Lease", config))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("timeLimitMs", "Generic.Int64"))
}

// The core with the test behaviors beside the ported ones.
fn core() -> superschematic_rules::Core {
    superschematic_rules_behaviors::core().with(Tick).with(Gate)
}

fn alice() -> Caller {
    caller("alice", &[])
}

fn wren() -> Caller {
    caller("wren", &[])
}

fn otto() -> Caller {
    caller("otto", &[])
}

fn boss() -> Caller {
    caller("boss", &["jobs.override", "jobs.direct"])
}

fn runner() -> Caller {
    caller("runner", &[])
}

// hosting creates j1, a job titled Build, on a host of the type.
fn hosting(spec: TypeSpec) -> Host {
    let mut host = Host::new(core(), spec);
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    host
}

fn token(value: i64) -> Map<String, Value> {
    object(json!({"Lease": {"token": value}}))
}

fn invoke(
    host: &mut Host,
    who: &Caller,
    now: i64,
    operation: &str,
    params: Value,
    presented: Option<i64>,
) -> Outcome {
    let action = Action::Operation {
        key: "j1".into(),
        use_id: "Lease".into(),
        operation: operation.into(),
        params,
    };
    host.run_presenting(who, now, action, presented.map(token).unwrap_or_default())
}

fn transition(
    host: &mut Host,
    who: &Caller,
    now: i64,
    to: &str,
    presented: Option<i64>,
) -> Outcome {
    let action = Action::Operation {
        key: "j1".into(),
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({ "to": to }),
    };
    host.run_presenting(who, now, action, presented.map(token).unwrap_or_default())
}

fn update(
    host: &mut Host,
    who: &Caller,
    now: i64,
    patch: Value,
    presented: Option<i64>,
) -> Outcome {
    let action = Action::Update {
        key: "j1".into(),
        patch: object(patch),
    };
    host.run_presenting(who, now, action, presented.map(token).unwrap_or_default())
}

// owned is j1's owned fields of a use.
fn owned(host: &Host, use_id: &str) -> Value {
    Value::Object(
        host.row("j1")
            .owned
            .get(use_id)
            .cloned()
            .unwrap_or_default(),
    )
}

// leased is j1's Lease fields, a field with no value left out.
fn leased(host: &Host) -> Value {
    let mut fields = host
        .row("j1")
        .owned
        .get("Lease")
        .cloned()
        .unwrap_or_default();
    fields.retain(|_, value| !value.is_null());
    Value::Object(fields)
}

fn status(host: &Host) -> Value {
    owned(host, "status")["status"].clone()
}

fn armed(host: &Host) -> Option<i64> {
    host.memory
        .armed
        .get(&("j1".to_owned(), "Lease".to_owned(), "expire".to_owned()))
        .copied()
}

fn directives(host: &Host) -> Value {
    serde_json::to_value(host.sub_rows("j1", "Lease", "directives")).expect("rows")
}

fn active(host: &Host, now: i64) -> Value {
    host.read_fields_at("j1", "Lease", "directives", now)["active"].clone()
}

// ---- test behaviors ----

// Tick: a count its deadline moves through its own operation, and a nudge
// that moves the row's Workflow through a call, which presents nothing.
struct Tick;

impl Behavior for Tick {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Tick")
            .field("ticks")
            .arms_deadlines()
            .operation("tick", true)
            .operation("schedule", true)
            .operation("nudge", true)
            .operation("peek", false)
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn operation(&self, cx: &Cx<'_, ()>, name: &str, params: &Value) -> Hook<Value> {
        match name {
            "tick" => {
                let ticks = cx.get("ticks").and_then(|v| v.as_i64()).unwrap_or(0);
                cx.set("ticks", ticks + 1)?;
                Ok(json!({ "ticks": ticks + 1 }))
            }
            "schedule" => {
                cx.arm("tick", cx.now() + 1000, Value::Null)?;
                Ok(Value::Null)
            }
            "nudge" => cx.call("status", "transition", &json!({ "to": params["to"] })),
            _ => Ok(json!({})),
        }
    }

    fn deadline(&self, cx: &Cx<'_, ()>, _name: &str, _payload: &Value) -> Hook<()> {
        cx.call(cx.use_id(), "tick", &json!({}))?;
        Ok(())
    }
}

// Gate: vetoes Lease's expire of a row titled stuck, whoever calls.
struct Gate;

impl Behavior for Gate {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Gate")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let stuck = view.own_field("title") == Some(json!("stuck"));
        Ok((stuck && request.operation_of("Lease", "expire").is_some())
            .then(|| Veto::new("it is stuck")))
    }
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Lease.declaration(),
        Declaration::new("Lease")
            .field("holder")
            .field("token")
            .field("acquiredAt")
            .field("renewedAt")
            .field("expiresAt")
            .computed("active")
            .field("expiries")
            .field("ended")
            .sub_table(TableDecl::new(
                "directives",
                "number",
                [
                    "number",
                    "token",
                    "name",
                    "data",
                    "dedupeKey",
                    "sentBy",
                    "sentAt",
                    "acknowledgedAt"
                ]
            ))
            .arms_deadlines()
            .operation("acquire", true)
            .operation("heartbeat", true)
            .operation("release", true)
            .operation("expire", true)
            .operation("direct", true)
            .operation("acknowledge", true)
            .table_operation("expireHolder", true)
            .operation("resetExpiries", true)
            .veto("held_by_another")
            .veto("held_by_caller")
            .veto("not_leased")
            .veto("not_holder")
            .veto("lapsed")
            .veto("token_stale")
            .veto("token_required")
            .veto("max_expiries")
            .veto("hold_limit_fixed")
            .veto("not_configured")
            .preconditions()
    );
    assert_eq!(HOLD_TYPES, ["Generic.Int64"]);
}

/// The checks Lease writes by hand are the declaration's configSchema,
/// keyword for keyword, and the shape refusals come out in the schema
/// loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(lease::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let status_move = json!({
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
                "acquirePermission": text,
                "directOn": {
                    "items": {
                        "additionalProperties": false,
                        "properties": {
                            "data": {"type": "object"},
                            "name": {"pattern": "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$", "type": "string"},
                            "revised": {
                                "additionalProperties": false,
                                "properties": {"link": {"pattern": "^[a-z][A-Za-z0-9]{0,63}$", "type": "string"}},
                                "required": ["link"],
                                "type": "object"
                            }
                        },
                        "required": ["revised", "name"],
                        "type": "object"
                    },
                    "maxItems": 16,
                    "minItems": 1,
                    "type": "array"
                },
                "directPermission": text,
                "escalate": status_move,
                "exempt": {
                    "items": {"pattern": "^([A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[a-z][A-Za-z0-9]*$", "type": "string"},
                    "type": "array",
                    "uniqueItems": true
                },
                "heartbeatMs": {"minimum": 1, "type": "integer"},
                "maxExpiries": {"minimum": 1, "type": "integer"},
                "maxHoldField": text,
                "maxHoldMs": {"minimum": 1000, "type": "integer"},
                "onExpiry": status_move,
                "overridePermission": text,
                "requireToken": {"type": "boolean"},
                "status": text,
                "sweepMs": {"minimum": 1000, "type": "integer"},
                "ttlMs": {"minimum": 1000, "type": "integer"}
            },
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &job(config));
    // The recorded scenario's.
    assert_eq!(
        shaped(json!({"ttlMs": 999})),
        ["/config/ttlMs: must be >= 1000"]
    );
    // The rest of the schema's keywords.
    assert_eq!(
        shaped(json!({
            "acquirePermission": "", "directPermission": 5, "heartbeatMs": 0, "maxExpiries": 1.5,
            "maxHoldMs": 10, "requireToken": "yes", "status": "", "sweepMs": 999, "x": 1,
            "onExpiry": {"transition": "", "from": [], "y": 2},
            "escalate": {"from": ["a", "a", ""]},
            "exempt": ["Comments.addComment", "comments.add", "Comments.addComment", 3],
            "directOn": [{"revised": {"link": "Spec", "z": 1}, "name": "1st", "data": [], "q": 0}, {}, 5]
        })),
        [
            "/config: unknown key \"x\"",
            "/config/acquirePermission: must NOT have fewer than 1 characters",
            "/config/directOn/0: unknown key \"q\"",
            "/config/directOn/0/data: must be object",
            "/config/directOn/0/name: must match pattern \"^[A-Za-z][A-Za-z0-9_.:-]{0,63}$\"",
            "/config/directOn/0/revised: unknown key \"z\"",
            "/config/directOn/0/revised/link: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\"",
            "/config/directOn/1: must have required property 'name'",
            "/config/directOn/1: must have required property 'revised'",
            "/config/directOn/2: must be object",
            "/config/directPermission: must be string",
            "/config/escalate: must have required property 'transition'",
            "/config/escalate/from: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/escalate/from/2: must NOT have fewer than 1 characters",
            "/config/exempt: must NOT have duplicate items (items ## 2 and 0 are identical)",
            "/config/exempt/1: must match pattern \"^([A-Za-z][A-Za-z0-9_-]*\\.)?[A-Z][A-Za-z0-9]*\\.[a-z][A-Za-z0-9]*$\"",
            "/config/exempt/3: must be string",
            "/config/heartbeatMs: must be >= 1",
            "/config/maxExpiries: must be integer",
            "/config/maxHoldMs: must be >= 1000",
            "/config/onExpiry: unknown key \"y\"",
            "/config/onExpiry/from: must NOT have fewer than 1 items",
            "/config/onExpiry/transition: must NOT have fewer than 1 characters",
            "/config/requireToken: must be boolean",
            "/config/status: must NOT have fewer than 1 characters",
            "/config/sweepMs: must be >= 1000"
        ]
    );
    assert_eq!(
        shaped(json!({"directOn": []})),
        ["/config/directOn: must NOT have fewer than 1 items"]
    );
    assert_eq!(
        shaped(json!({"directOn": "x", "exempt": "y", "onExpiry": 1})),
        [
            "/config/directOn: must be array",
            "/config/exempt: must be array",
            "/config/onExpiry: must be object"
        ]
    );
}

/// Each operation's parameters are held to its closed paramsSchema in the
/// behavior registry's words.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(lease::DECLARATION).expect("it parses");
    let schema_of = |name: &str| {
        file["operations"]
            .as_array()
            .expect("operations")
            .iter()
            .find(|operation| operation["name"] == name)
            .map(|operation| undescribed(&operation["paramsSchema"]))
            .expect("the operation")
    };
    let ids = |required: bool| {
        let mut ids = json!({"items": {"minimum": 1, "type": "integer"}, "maxItems": 500, "type": "array", "uniqueItems": true});
        if required {
            ids["minItems"] = json!(1);
        }
        ids
    };
    assert_eq!(
        schema_of("acquire"),
        json!({"additionalProperties": false, "properties": {"ttlMs": {"minimum": 1000, "type": "integer"}}, "type": "object"})
    );
    assert_eq!(
        schema_of("heartbeat"),
        json!({"additionalProperties": false, "properties": {"acknowledge": ids(false)}, "type": "object"})
    );
    assert_eq!(
        schema_of("release"),
        json!({"additionalProperties": false, "properties": {"abandon": {"type": "boolean"}}, "type": "object"})
    );
    assert_eq!(
        schema_of("expire"),
        json!({"additionalProperties": false, "properties": {"holder": {"minLength": 1, "type": "string"}, "notRenewedAfter": {"minimum": 0, "type": "integer"}}, "type": "object"})
    );
    assert_eq!(
        schema_of("direct"),
        json!({"additionalProperties": false, "properties": {
            "data": {"type": "object"},
            "dedupeKey": {"maxLength": 128, "minLength": 1, "type": "string"},
            "name": {"pattern": "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$", "type": "string"}
        }, "required": ["name"], "type": "object"})
    );
    assert_eq!(
        schema_of("acknowledge"),
        json!({"additionalProperties": false, "properties": {"ids": ids(true)}, "required": ["ids"], "type": "object"})
    );
    assert_eq!(
        schema_of("resetExpiries"),
        json!({"additionalProperties": false, "type": "object"})
    );
    let mut host = hosting(job(json!({})));
    let issues = |host: &mut Host, operation: &str, params: Value| -> Vec<String> {
        let error = refused(invoke(host, &wren(), START, operation, params, None));
        error["details"]["issues"]
            .as_array()
            .expect("issues")
            .iter()
            .map(|issue| {
                format!(
                    "{}: {}",
                    issue["path"].as_str().unwrap(),
                    issue["message"].as_str().unwrap()
                )
            })
            .collect()
    };
    assert_eq!(
        issues(&mut host, "acquire", json!({"ttlMs": 999, "x": 1})),
        [
            ": must NOT have additional properties: x",
            "/ttlMs: must be >= 1000"
        ]
    );
    assert_eq!(
        issues(
            &mut host,
            "heartbeat",
            json!({"acknowledge": [0, 2, 2, "3"]})
        ),
        [
            "/acknowledge: must NOT have duplicate items (items ## 2 and 1 are identical)",
            "/acknowledge/0: must be >= 1",
            "/acknowledge/3: must be integer"
        ]
    );
    assert_eq!(
        issues(
            &mut host,
            "heartbeat",
            json!({"acknowledge": vec![1; 501].iter().enumerate().map(|(i, _)| i + 1).collect::<Vec<_>>()})
        ),
        ["/acknowledge: must NOT have more than 500 items"]
    );
    assert_eq!(
        issues(&mut host, "release", json!({"abandon": 1})),
        ["/abandon: must be boolean"]
    );
    assert_eq!(
        issues(
            &mut host,
            "expire",
            json!({"holder": "", "notRenewedAfter": -1})
        ),
        [
            "/holder: must NOT have fewer than 1 characters",
            "/notRenewedAfter: must be >= 0"
        ]
    );
    assert_eq!(
        issues(
            &mut host,
            "direct",
            json!({"data": [], "dedupeKey": "k".repeat(129)})
        ),
        [
            ": must have required property 'name'",
            "/data: must be object",
            "/dedupeKey: must NOT have more than 128 characters"
        ]
    );
    assert_eq!(
        issues(&mut host, "direct", json!({"name": "1st", "dedupeKey": ""})),
        [
            "/dedupeKey: must NOT have fewer than 1 characters",
            "/name: must match pattern \"^[A-Za-z][A-Za-z0-9_.:-]{0,63}$\""
        ]
    );
    assert_eq!(
        issues(&mut host, "acknowledge", json!({})),
        [": must have required property 'ids'"]
    );
    assert_eq!(
        issues(&mut host, "acknowledge", json!({"ids": []})),
        ["/ids: must NOT have fewer than 1 items"]
    );
    assert_eq!(
        issues(&mut host, "resetExpiries", json!({"all": true})),
        [": must NOT have additional properties: all"]
    );
    assert_eq!(
        refused(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({"x": 1}),
            None
        ))["message"],
        "operation acquire of behavior Lease: must NOT have additional properties: x"
    );
}

/// The precondition a command presents for Lease is held to its
/// preconditionSchema before the row is read, each issue under its entry.
#[test]
fn the_precondition_is_held_to_its_schema() {
    let file: Value = serde_json::from_str(lease::DECLARATION).expect("it parses");
    assert_eq!(
        undescribed(&file["preconditionSchema"]),
        json!({"additionalProperties": false, "properties": {"token": {"minimum": 0, "type": "integer"}}, "required": ["token"], "type": "object"})
    );
    let mut host = hosting(job(json!({})));
    let presenting = |host: &mut Host, preconditions: Value| {
        refused(host.run_presenting(
            &wren(),
            START,
            Action::Update {
                key: "j9".into(),
                patch: object(json!({"title": "x"})),
            },
            object(preconditions),
        ))
    };
    assert_eq!(
        presenting(&mut host, json!({"Lease": {"token": -1, "at": 2}})),
        json!({"class": "PreconditionsError", "code": "invalid_argument",
        "message": "preconditions of Job: /Lease: must NOT have additional properties: at; /Lease/token: must be >= 0",
        "details": {"issues": [
            {"path": "/Lease", "message": "must NOT have additional properties: at"},
            {"path": "/Lease/token", "message": "must be >= 0"}
        ]}})
    );
    assert_eq!(
        presenting(&mut host, json!({"Lease": {}}))["message"],
        "preconditions of Job: /Lease: must have required property 'token'"
    );
    assert_eq!(
        presenting(&mut host, json!({"Lease": 3}))["message"],
        "preconditions of Job: /Lease: must be object"
    );
    assert_eq!(
        presenting(&mut host, json!({"Lease": {"token": 1.5}}))["message"],
        "preconditions of Job: /Lease/token: must be integer"
    );
    assert_eq!(
        presenting(&mut host, json!({"status": {"token": 1}}))["message"],
        "preconditions of Job: /status: behavior Workflow takes no precondition"
    );
}

/// The config's rules, refused in the engine's order and words, then what a
/// table's Lease refuses.
#[test]
fn the_config_is_held_to_the_type() {
    let core = core();
    let refused = |config: Value| rule(&core, &job(config), "Lease");
    assert_eq!(
        refused(json!({"heartbeatMs": 60000})),
        "heartbeatMs (60000) is more than half of ttlMs (60000): one late heartbeat would lose the lease"
    );
    assert_eq!(
        refused(json!({"ttlMs": 10000, "heartbeatMs": 5001})),
        "heartbeatMs (5001) is more than half of ttlMs (10000): one late heartbeat would lose the lease"
    );
    assert_eq!(
        refused(json!({"maxHoldField": "deadline"})),
        "maxHoldField \"deadline\" is not a field of Job (its fields: title, timeLimitMs)"
    );
    assert_eq!(
        refused(json!({"maxHoldField": "title"})),
        "maxHoldField \"title\" is not an integer field of Job"
    );
    assert_eq!(
        refused(json!({"onExpiry": {"transition": "waiting", "from": ["running"]}})),
        "onExpiry.transition \"waiting\" is not a state of the type's Workflow (queued, running, done, failed)"
    );
    assert_eq!(
        refused(json!({"onExpiry": {"transition": "queued", "from": ["done"]}})),
        "onExpiry: no transition of the type's Workflow leads from \"done\" to \"queued\""
    );
    assert_eq!(
        refused(json!({"onExpiry": {"transition": "queued", "from": ["queued"]}})),
        "onExpiry moves the status from \"queued\" to itself"
    );
    assert_eq!(
        refused(json!({"escalate": {"transition": "done", "from": ["queued"]}, "maxExpiries": 2})),
        "escalate: no transition of the type's Workflow leads from \"queued\" to \"done\""
    );
    assert_eq!(
        refused(json!({"escalate": {"transition": "failed", "from": ["running"]}})),
        "escalate applies at the expiry that reaches maxExpiries, which the config does not give"
    );
    assert_eq!(
        refused(json!({"exempt": ["Lease.release"]})),
        "exempt names Lease.release: Lease's own operations check their callers themselves"
    );
    assert_eq!(
        refused(json!({"exempt": ["Revisions.approve"]})),
        "exempt names Revisions.approve, but the type does not list Revisions (it lists Lease, Workflow)"
    );
    assert_eq!(
        refused(json!({"status": "phase"})),
        "status \"phase\" is not a Workflow of the type (its Workflows: status)"
    );
    // A table's refusals come after every check the engine made.
    assert_eq!(
        refused(
            json!({"directOn": [{"revised": {"link": "spec"}, "name": "rebase"}], "sweepMs": 1000, "exempt": ["Lease.acquire"]})
        ),
        "exempt names Lease.acquire: Lease's own operations check their callers themselves"
    );
    assert_eq!(
        refused(json!({"directOn": [{"revised": {"link": "spec"}, "name": "rebase"}], "sweepMs": 1000})),
        "directOn sends a directive when the target of a link moves on, a revision of Revisions or a release of Branches, which waits for Revisions and Branches on the core (D103, step 4)"
    );
    assert_eq!(
        refused(json!({"sweepMs": 1000})),
        "sweepMs is how often the engine's runner swept the lapsed leases: a table's lease expires at its own deadline, which the API's behavior worker runs when it falls due (D103), so a table's Lease takes no sweepMs"
    );
    // A type with no Workflow has none to move.
    let bare = TypeSpec::new("Job").with(UseSpec::on_type(
        "Lease",
        json!({"onExpiry": {"transition": "queued", "from": ["running"]}}),
    ));
    assert_eq!(
        rule(&core, &bare, "Lease"),
        "onExpiry moves the status through Workflow, which the type does not list"
    );
    assert!(define(
        &core,
        &TypeSpec::new("Job").with(UseSpec::on_type("Lease", None))
    )
    .is_ok());
    assert!(define(
        &core,
        &job(json!({"ttlMs": 9000, "heartbeatMs": 4500, "maxHoldField": "timeLimitMs"}))
    )
    .is_ok());
}

/// A config's moves name the Workflow its status names among several, and
/// a type with several and no status is refused in flow.rs's words.
#[test]
fn the_moves_name_the_workflow_status_names() {
    let core = core();
    let two = |config: Value| {
        TypeSpec::new("Job")
            .with(UseSpec::on_type("Lease", config))
            .with(UseSpec::on_field("status", "Workflow", flow()))
            .with(UseSpec::on_field("phase", "Workflow", json!({"states": ["open", "shut"], "transitions": [{"from": "open", "to": "shut"}]})))
            .with_field(FieldSpec::new("title", "string"))
    };
    assert_eq!(
        rule(&core, &two(json!({})), "Lease"),
        "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    assert_eq!(
        rule(
            &core,
            &two(
                json!({"status": "phase", "onExpiry": {"transition": "queued", "from": ["running"]}})
            ),
            "Lease"
        ),
        "onExpiry.transition \"queued\" is not a state of the type's Workflow (open, shut)"
    );
    let mut host = Host::new(
        core,
        two(json!({"status": "phase", "onExpiry": {"transition": "shut", "from": ["open"]}})),
    );
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "release",
        json!({}),
        Some(1),
    ));
    assert_eq!(owned(&host, "phase")["status"], "shut");
    assert_eq!(status(&host), "queued");
}

// ---- acquire, heartbeat, release, expire ----

/// An acquire takes a free row's lease with the next token and arms its
/// expiry; another's acquire is held_by_another and the holder's
/// held_by_caller; a heartbeat with the token renews it and moves its
/// deadline; a read shows the lease active until its expiry.
#[test]
fn an_acquire_takes_the_lease_and_a_heartbeat_renews_it() {
    let mut host = hosting(job(json!({})));
    assert_eq!(leased(&host), json!({"token": 0, "expiries": 0}));
    assert_eq!(active(&host, START), json!(false));
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({}),
            None
        )),
        json!({"token": 1, "expiresAt": 1_060_000, "heartbeatMs": 20000})
    );
    assert_eq!(
        leased(&host),
        json!({"holder": "wren", "token": 1, "acquiredAt": START, "renewedAt": START, "expiresAt": 1_060_000, "expiries": 0})
    );
    assert_eq!(armed(&host), Some(1_060_000));
    assert_eq!(active(&host, 1_059_999), json!(true));
    assert_eq!(active(&host, 1_060_000), json!(false));
    let error = refused(invoke(
        &mut host,
        &otto(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        error["message"],
        "behavior Lease vetoes acquire of Job j1: another principal holds its lease, until 1970-01-01T00:17:40.000Z"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"expiresAt": 1_060_000})
    );
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({}),
            None
        )),
        ("held_by_caller".to_owned(), json!({"expiresAt": 1_060_000}))
    );
    // A shorter ttl scales the heartbeat; a longer one is refused.
    assert_eq!(
        refused(invoke(&mut host, &wren(), START, "acquire", json!({"ttlMs": 70000}), None))["message"],
        "operation acquire of behavior Lease: /ttlMs: 70000 is longer than the config's ttlMs, 60000"
    );
    // The heartbeat presents the token.
    let error = refused(invoke(
        &mut host,
        &wren(),
        START + 1000,
        "heartbeat",
        json!({}),
        None,
    ));
    assert_eq!(
        error["message"],
        "behavior Lease vetoes heartbeat of Job j1: the holder presents its token as Lease's precondition"
    );
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &otto(),
            START + 1000,
            "heartbeat",
            json!({}),
            Some(1)
        ))
        .0,
        "not_holder"
    );
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &wren(),
            START + 1000,
            "heartbeat",
            json!({}),
            Some(7)
        )),
        ("token_stale".to_owned(), json!({"token": 7, "current": 1}))
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 30_000,
            "heartbeat",
            json!({}),
            Some(1)
        )),
        json!({"expiresAt": 1_090_000, "directives": []})
    );
    assert_eq!(leased(&host)["renewedAt"], 1_030_000);
    assert_eq!(armed(&host), Some(1_090_000));
    // A lapsed lease's heartbeat is refused.
    let error = refused(invoke(
        &mut host,
        &wren(),
        1_090_000,
        "heartbeat",
        json!({}),
        Some(1),
    ));
    assert_eq!(
        error["details"]["reason"],
        "its lease expired at 1970-01-01T00:18:10.000Z; expire applies the expiry, and acquire takes a new lease"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"expiredAt": 1_090_000})
    );
    // An acquire over the lapsed lease applies its expiry first.
    assert_eq!(
        result(invoke(
            &mut host,
            &otto(),
            1_100_000,
            "acquire",
            json!({"ttlMs": 30000}),
            None
        )),
        json!({"token": 3, "expiresAt": 1_130_000, "heartbeatMs": 10000})
    );
    assert_eq!(leased(&host)["expiries"], 1);
    assert_eq!(leased(&host).get("ended"), None);
}

/// A heartbeat renews by the length the last renewal gave the lease, so a
/// lease the longest hold capped stays capped there; the row's own
/// maxHoldField overrides maxHoldMs, and cannot change while the lease is
/// held.
#[test]
fn the_longest_hold_caps_the_lease() {
    let mut host = hosting(job(
        json!({"maxHoldMs": 50000, "maxHoldField": "timeLimitMs"}),
    ));
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({}),
            None
        ))["expiresAt"],
        1_050_000
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 10_000,
            "heartbeat",
            json!({}),
            Some(1)
        ))["expiresAt"],
        1_050_000
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 40_000,
            "heartbeat",
            json!({}),
            Some(1)
        ))["expiresAt"],
        1_050_000
    );
    // The field holds the limit while the lease is held.
    assert_eq!(
        veto_of(update(
            &mut host,
            &wren(),
            START + 40_000,
            json!({"timeLimitMs": 90000}),
            None
        ))
        .0,
        "hold_limit_fixed"
    );
    // At the hold's end the expiry's reason is maxHold.
    assert_eq!(
        result(invoke(
            &mut host,
            &otto(),
            1_050_000,
            "expire",
            json!({}),
            None
        )),
        json!({"expired": true, "reason": "maxHold"})
    );
    // The row's own limit, when it holds a positive whole number.
    changed(update(
        &mut host,
        &wren(),
        1_060_000,
        json!({"timeLimitMs": 20000}),
        None,
    ));
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            1_060_000,
            "acquire",
            json!({}),
            None
        ))["expiresAt"],
        1_080_000
    );
}

/// An update of maxHoldField while the lease is held, by a caller with
/// overridePermission: a shorter hold caps the expiry and moves its
/// deadline; a longer one after the old hold cut a heartbeat short gives
/// the lease the renewal the config's ttl gives under the new hold, so the
/// next heartbeat on its schedule renews by the ttl, not by what the cut
/// left; a longer one when nothing was cut leaves the expiry.
#[test]
fn a_raised_hold_gives_back_what_its_cut_took() {
    let config = json!({"ttlMs": 60000, "maxHoldField": "timeLimitMs", "overridePermission": "jobs.override"});
    let mut host = Host::new(core(), job(config));
    changed(host.create(
        &alice(),
        "j1",
        json!({"title": "Build", "timeLimitMs": 61000}),
    ));
    let acquired = result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(acquired["expiresAt"], 1_060_000);
    // The hold cuts the heartbeat at 59 s to 61 s from the acquire.
    let beat = result(invoke(
        &mut host,
        &wren(),
        START + 59_000,
        "heartbeat",
        json!({}),
        Some(1),
    ));
    assert_eq!(beat["expiresAt"], 1_061_000);
    // An operator gives the job an hour: the lease's renewal is its ttl
    // from the last heartbeat, and the deadline moves there.
    changed(update(
        &mut host,
        &boss(),
        START + 60_000,
        json!({"timeLimitMs": 3_600_000}),
        None,
    ));
    assert_eq!(leased(&host)["expiresAt"], 1_119_000);
    assert_eq!(armed(&host), Some(1_119_000));
    // The next heartbeat renews by the ttl, 60 s, not the 2 s the cut left.
    let beat = result(invoke(
        &mut host,
        &wren(),
        START + 79_000,
        "heartbeat",
        json!({}),
        Some(1),
    ));
    assert_eq!(beat["expiresAt"], 1_139_000);
    // A raise when no heartbeat was cut leaves the expiry.
    changed(update(
        &mut host,
        &boss(),
        START + 80_000,
        json!({"timeLimitMs": 7_200_000}),
        None,
    ));
    assert_eq!(leased(&host)["expiresAt"], 1_139_000);
    // A shorter hold caps it at once, and its deadline moves there.
    changed(update(
        &mut host,
        &boss(),
        START + 81_000,
        json!({"timeLimitMs": 90_000}),
        None,
    ));
    assert_eq!(leased(&host)["expiresAt"], 1_090_000);
    assert_eq!(armed(&host), Some(1_090_000));
}

/// A release presents the token, ends the lease without counting an expiry,
/// deletes its directives, clears its deadline and moves the status with
/// onExpiry; an abandon counts one and escalates at maxExpiries; another's
/// release is not_holder, or forbidden with overridePermission, which lets
/// its holder release it.
#[test]
fn a_release_ends_the_lease_and_an_abandon_counts_an_expiry() {
    let config = json!({
        "onExpiry": {"transition": "queued", "from": ["running"]},
        "maxExpiries": 2,
        "escalate": {"transition": "failed", "from": ["running"]}
    });
    let mut host = hosting(job(config));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    changed(transition(&mut host, &wren(), START, "running", None));
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &otto(),
            START,
            "release",
            json!({}),
            None
        ))
        .0,
        "not_holder"
    );
    assert_eq!(
        refused(invoke(
            &mut host,
            &wren(),
            START,
            "release",
            json!({}),
            None
        ))["details"]["reason"],
        "the holder releases its lease presenting its token as Lease's precondition"
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 5000,
            "release",
            json!({}),
            Some(1)
        )),
        json!({})
    );
    assert_eq!(
        leased(&host),
        json!({"token": 2, "expiries": 0, "ended": {"reason": "release", "at": 1_005_000}})
    );
    assert_eq!(status(&host), "queued");
    assert_eq!(armed(&host), None);
    // An abandon counts; the second reaches maxExpiries and escalates.
    for (at, expected) in [(1_010_000, "queued"), (1_020_000, "failed")] {
        let taken = result(invoke(&mut host, &wren(), at, "acquire", json!({}), None));
        changed(transition(&mut host, &wren(), at, "running", None));
        let current = taken["token"].as_i64();
        result(invoke(
            &mut host,
            &wren(),
            at,
            "release",
            json!({"abandon": true}),
            current,
        ));
        assert_eq!(status(&host), expected);
        assert_eq!(leased(&host)["ended"]["reason"], "abandon");
    }
    assert_eq!(leased(&host)["expiries"], 2);
    let error = refused(invoke(
        &mut host,
        &wren(),
        1_030_000,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        error["details"]["reason"],
        "its lease has expired 2 times, the most its config allows; resetExpiries lets it be leased again"
    );
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"expiries": 2, "maxExpiries": 2})
    );
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &boss(),
            1_030_000,
            "resetExpiries",
            json!({}),
            None
        ))
        .0,
        "not_configured"
    );
    // With overridePermission another's release is forbidden, and the
    // override releases it.
    let mut host = hosting(job(json!({"overridePermission": "jobs.override"})));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        refused(invoke(
            &mut host,
            &otto(),
            START,
            "release",
            json!({}),
            None
        )),
        json!({"class": "EngineError", "code": "forbidden", "message": "otto may not release another principal's lease of Job j1: it needs permission jobs.override"})
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START,
            "release",
            json!({}),
            None
        )),
        json!({})
    );
    // resetExpiries with the permission.
    assert_eq!(
        refused(invoke(
            &mut host,
            &otto(),
            START,
            "resetExpiries",
            json!({}),
            None
        ))["message"],
        "otto may not reset the expiries of Job j1: it needs permission jobs.override"
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START,
            "resetExpiries",
            json!({}),
            None
        )),
        json!({"expiries": 0})
    );
}

/// expire applies a lapsed lease's expiry for anyone and spares an active
/// one; with holder, a caller with overridePermission expires that
/// holder's lease at once unless notRenewedAfter spares it; without the
/// permission it is forbidden.
#[test]
fn expire_applies_a_lapsed_lease_and_a_holders_with_the_override() {
    let mut host = hosting(job(
        json!({"overridePermission": "jobs.override", "onExpiry": {"transition": "queued", "from": ["running"]}}),
    ));
    let version = host.row("j1").version;
    assert_eq!(
        result(invoke(&mut host, &otto(), START, "expire", json!({}), None)),
        json!({"expired": false})
    );
    assert_eq!(host.row("j1").version, version);
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    changed(transition(&mut host, &wren(), START, "running", None));
    assert_eq!(
        result(invoke(&mut host, &otto(), START, "expire", json!({}), None)),
        json!({"expired": false})
    );
    assert_eq!(
        refused(invoke(&mut host, &otto(), START, "expire", json!({"notRenewedAfter": 5}), None))["message"],
        "operation expire of behavior Lease: /notRenewedAfter: notRenewedAfter spares a holder's active leases, so it needs holder"
    );
    assert_eq!(
        refused(invoke(
            &mut host,
            &otto(),
            START,
            "expire",
            json!({"holder": "wren"}),
            None
        ))["message"],
        "otto may not expire the lease of another holder of Job: it needs permission jobs.override"
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START,
            "expire",
            json!({"holder": "wren", "notRenewedAfter": START - 1}),
            None
        )),
        json!({"expired": false})
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START,
            "expire",
            json!({"holder": "otto"}),
            None
        )),
        json!({"expired": false})
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START + 1000,
            "expire",
            json!({"holder": "wren", "notRenewedAfter": START}),
            None
        )),
        json!({"expired": true, "reason": "holder"})
    );
    assert_eq!(status(&host), "queued");
    assert_eq!(
        leased(&host)["ended"],
        json!({"reason": "holder", "at": 1_001_000})
    );
    // A lapsed lease, by anyone.
    result(invoke(
        &mut host,
        &wren(),
        1_002_000,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        result(invoke(
            &mut host,
            &otto(),
            1_062_000,
            "expire",
            json!({}),
            None
        )),
        json!({"expired": true, "reason": "ttl"})
    );
    // Without overridePermission, holder is refused outright.
    let mut host = hosting(job(json!({})));
    assert_eq!(
        refused(invoke(&mut host, &boss(), START, "expire", json!({"holder": "wren"}), None))["message"],
        "boss may not expire the lease of another holder of Job: its Lease config names no overridePermission"
    );
}

// ---- the deadline ----

/// The expiry is a deadline: a heartbeat moves it, and when it falls due
/// the runner expires the lease, counts it and moves the status; a lease
/// whose deadline moved past now since the arming arms it again; a guard's
/// veto of the fire refuses the deadline, which stays armed.
#[test]
fn the_expiry_falls_due_as_a_deadline() {
    let mut host = hosting(job(
        json!({"ttlMs": 10000, "onExpiry": {"transition": "queued", "from": ["running"]}}),
    ));
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({}),
            None
        )),
        json!({"token": 1, "expiresAt": 1_010_000, "heartbeatMs": 3333})
    );
    changed(transition(&mut host, &wren(), START, "running", None));
    result(invoke(
        &mut host,
        &wren(),
        1_005_000,
        "heartbeat",
        json!({}),
        Some(1),
    ));
    assert_eq!(armed(&host), Some(1_015_000));
    assert!(host.fall_due(1_010_000, &runner()).is_empty());
    let fired = host.fall_due(1_015_000, &runner());
    assert_eq!(fired.len(), 1);
    changed(fired.into_iter().next().expect("one").1);
    assert_eq!(
        leased(&host),
        json!({"token": 2, "expiries": 1, "ended": {"reason": "ttl", "at": 1_015_000}})
    );
    assert_eq!(status(&host), "queued");
    assert_eq!(armed(&host), None);
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &wren(),
            1_016_000,
            "heartbeat",
            json!({}),
            Some(1)
        )),
        ("token_stale".to_owned(), json!({"token": 1, "current": 2}))
    );
    // A lease whose deadline a raw write moved past now arms again.
    result(invoke(
        &mut host,
        &wren(),
        1_020_000,
        "acquire",
        json!({}),
        None,
    ));
    host.memory
        .rows
        .get_mut("j1")
        .expect("j1")
        .owned
        .get_mut("Lease")
        .expect("Lease")
        .insert("expiresAt".into(), json!(1_050_000));
    let fired = host.fall_due(1_030_000, &runner());
    changed(fired.into_iter().next().expect("one").1);
    assert_eq!(armed(&host), Some(1_050_000));
    assert_eq!(leased(&host)["holder"], "wren");
    // A guard's veto of the fire refuses the deadline.
    changed(update(
        &mut host,
        &wren(),
        1_040_000,
        json!({"title": "stuck"}),
        None,
    ));
    let gated = TypeSpec {
        ..host.spec.clone()
    }
    .with(UseSpec::on_type("Gate", None));
    host.spec = gated;
    let fired = host.fall_due(1_050_000, &runner());
    assert_eq!(
        refused(fired.into_iter().next().expect("one").1)["message"],
        "behavior Gate vetoes expire of Job j1: it is stuck"
    );
    assert_eq!(armed(&host), Some(1_050_000));
    assert_eq!(leased(&host)["holder"], "wren");
    // A free row's fire changes nothing of the lease.
    let mut host = hosting(job(json!({})));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "release",
        json!({}),
        Some(1),
    ));
    host.memory
        .armed
        .insert(("j1".into(), "Lease".into(), "expire".into()), 1_001_000);
    let before = leased(&host);
    changed(
        host.fall_due(1_001_000, &runner())
            .into_iter()
            .next()
            .expect("one")
            .1,
    );
    assert_eq!(leased(&host), before);
}

// ---- directives ----

/// A caller with directPermission sends the holder directives, deduped by
/// their key; a heartbeat returns the ones not acknowledged, acknowledge
/// marks them, and every end of the lease deletes them.
#[test]
fn directives_reach_the_holder_once_and_go_with_the_lease() {
    let mut host = hosting(job(json!({"directPermission": "jobs.direct"})));
    assert_eq!(
        veto_of(invoke(
            &mut host,
            &boss(),
            START,
            "direct",
            json!({"name": "stop"}),
            None
        ))
        .0,
        "not_leased"
    );
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        refused(invoke(
            &mut host,
            &alice(),
            START,
            "direct",
            json!({"name": "stop"}),
            None
        ))["message"],
        "alice may not send a directive to the holder of Job j1: it needs permission jobs.direct"
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START + 1,
            "direct",
            json!({"name": "stop", "data": {"why": "late"}}),
            None
        )),
        json!({"id": 1, "created": true})
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START + 2,
            "direct",
            json!({"name": "rebase", "dedupeKey": "v2"}),
            None
        )),
        json!({"id": 2, "created": true})
    );
    let version = host.row("j1").version;
    assert_eq!(
        result(invoke(
            &mut host,
            &boss(),
            START + 3,
            "direct",
            json!({"name": "rebase", "dedupeKey": "v2"}),
            None
        )),
        json!({"id": 2, "created": false})
    );
    assert_eq!(host.row("j1").version, version);
    assert_eq!(
        directives(&host),
        json!([
            {"key": "0000000001", "values": {"token": 1, "name": "stop", "data": {"why": "late"}, "sentBy": "boss", "sentAt": 1_000_001}},
            {"key": "0000000002", "values": {"token": 1, "name": "rebase", "dedupeKey": "v2", "sentBy": "boss", "sentAt": 1_000_002}}
        ])
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 10,
            "heartbeat",
            json!({}),
            Some(1)
        ))["directives"],
        json!([
            {"id": 1, "name": "stop", "data": {"why": "late"}, "createdAt": 1_000_001, "createdBy": "boss"},
            {"id": 2, "name": "rebase", "dedupeKey": "v2", "createdAt": 1_000_002, "createdBy": "boss"}
        ])
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 11,
            "acknowledge",
            json!({"ids": [1]}),
            Some(1)
        )),
        json!({})
    );
    let version = host.row("j1").version;
    result(invoke(
        &mut host,
        &wren(),
        START + 12,
        "acknowledge",
        json!({"ids": [1]}),
        Some(1),
    ));
    assert_eq!(host.row("j1").version, version);
    assert_eq!(
        refused(invoke(
            &mut host,
            &wren(),
            START + 12,
            "acknowledge",
            json!({"ids": [1, 9, 8]}),
            Some(1)
        ))["message"],
        "operation acknowledge of behavior Lease: /ids: no directive 9, 8 was sent under token 1"
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START + 13,
            "heartbeat",
            json!({"acknowledge": [2]}),
            Some(1)
        ))["directives"],
        json!([])
    );
    result(invoke(
        &mut host,
        &wren(),
        START + 14,
        "release",
        json!({}),
        Some(1),
    ));
    assert_eq!(directives(&host), json!([]));
    // A config that names no permission to send them.
    let mut host = hosting(job(json!({})));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    assert_eq!(
        refused(invoke(&mut host, &boss(), START, "direct", json!({"name": "stop"}), None))["details"]["reason"],
        "its config names no permission that sends directives (directPermission or overridePermission)"
    );
}

// ---- the token ----

/// Under requireToken a write without the token is token_required; a stale
/// token is token_stale whoever presents it, on a free row too, and only
/// for a write; and a request a behavior's code makes presents nothing and
/// passes.
#[test]
fn the_token_fences_every_write() {
    let mut host = Host::new(
        core(),
        job(json!({"requireToken": true})).with(UseSpec::on_type("Tick", None)),
    );
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    // On a free row a stale token is stale.
    assert_eq!(
        veto_of(update(
            &mut host,
            &otto(),
            START,
            json!({"title": "x"}),
            Some(5)
        )),
        ("token_stale".to_owned(), json!({"token": 5, "current": 0}))
    );
    changed(update(
        &mut host,
        &otto(),
        START,
        json!({"title": "x"}),
        Some(0),
    ));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        Some(0),
    ));
    let error = refused(transition(&mut host, &wren(), START, "running", None));
    assert_eq!(
        error["details"]["reason"],
        "its config requires every write under the lease to present the lease's token as Lease's precondition"
    );
    assert_eq!(error["details"]["vetoCode"], "token_required");
    changed(transition(&mut host, &wren(), START, "running", Some(1)));
    // A read-only operation passes whatever token it presents.
    let peek = Action::Operation {
        key: "j1".into(),
        use_id: "Tick".into(),
        operation: "peek".into(),
        params: json!({}),
    };
    result(host.run_presenting(&otto(), START, peek, token(9)));
    // A behavior's call presents nothing and passes: the command's own
    // operation is the holder's, with the token.
    let nudge = Action::Operation {
        key: "j1".into(),
        use_id: "Tick".into(),
        operation: "nudge".into(),
        params: json!({"to": "done"}),
    };
    result(host.run_presenting(&wren(), START, nudge, token(1)));
    assert_eq!(status(&host), "done");
    // A delete presents it too.
    let delete = Action::Delete { key: "j1".into() };
    assert_eq!(
        veto_of(host.run_presenting(&wren(), START, delete.clone(), token(0))).0,
        "token_stale"
    );
    changed(host.run_presenting(&wren(), START, delete, token(1)));
}

/// A use's call of its own operation on its row is the command's caller's:
/// a deadline that calls its own operation meets another's active lease as
/// a caller would, while its call of another use's operation passes; a
/// config that exempts the operation lets it through.
#[test]
fn a_uses_call_of_its_own_operation_is_the_callers() {
    let spec = |config: Value| job(config).with(UseSpec::on_type("Tick", None));
    let mut host = Host::new(core(), spec(json!({})));
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    let schedule = Action::Operation {
        key: "j1".into(),
        use_id: "Tick".into(),
        operation: "schedule".into(),
        params: json!({}),
    };
    changed(host.run(&alice(), START, schedule.clone()));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    let fired = host.fall_due(START + 1000, &runner());
    let ticked: Vec<(String, (String, Value))> = fired
        .into_iter()
        .filter(|(name, _)| name == "tick")
        .map(|(name, outcome)| (name, veto_of(outcome)))
        .collect();
    assert_eq!(
        ticked,
        [(
            "tick".to_owned(),
            (
                "held_by_another".to_owned(),
                json!({"expiresAt": 1_060_000})
            )
        )]
    );
    // Exempt, the fire goes through.
    let mut host = Host::new(core(), spec(json!({"exempt": ["Tick.tick"]})));
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    changed(host.run(&alice(), START, schedule));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    for (name, outcome) in host.fall_due(START + 1000, &runner()) {
        if name == "tick" {
            changed(outcome);
        }
    }
    assert_eq!(owned(&host, "Tick")["ticks"], 1);
    // Tick's nudge, the command of the holder, calls Workflow's transition,
    // which passes as a behavior's call; another caller's nudge is held.
    let nudge = |to: &str| Action::Operation {
        key: "j1".into(),
        use_id: "Tick".into(),
        operation: "nudge".into(),
        params: json!({ "to": to }),
    };
    assert_eq!(
        veto_of(host.run(&otto(), START + 2000, nudge("running"))).0,
        "held_by_another"
    );
    result(host.run(&wren(), START + 2000, nudge("running")));
    assert_eq!(status(&host), "running");
}

/// A row that predates its use, whose Lease columns are NULL, reads as a
/// free row at token 0, and its first acquire takes token 1.
#[test]
fn a_row_with_no_token_reads_as_token_zero() {
    let mut host = Host::new(core(), job(json!({})));
    host.memory.rows.insert(
        "j1".into(),
        Row {
            own: object(json!({"title": "Old"})),
            owned: Default::default(),
            version: 4,
        },
    );
    assert_eq!(active(&host, START), json!(false));
    assert_eq!(
        result(invoke(
            &mut host,
            &wren(),
            START,
            "acquire",
            json!({}),
            None
        ))["token"],
        1
    );
    assert_eq!(
        veto_of(update(
            &mut host,
            &otto(),
            START,
            json!({"title": "x"}),
            Some(0)
        ))
        .0,
        "token_stale"
    );
}

// ---- composition ----

/// A Timers fire, the deadline's call of Timers' own fireTimer, is held by
/// the holder's active lease as the engine's runner's invoke was; the
/// worker's message falls due again.
#[test]
fn a_timers_fire_meets_the_holders_lease() {
    let spec = job(json!({})).with(UseSpec::on_type(
        "Timers",
        json!({"deadlines": {"respond": {"start": {"create": true}, "afterMs": 1000}}}),
    ));
    let mut host = Host::new(core(), spec);
    changed(host.create(&alice(), "j1", json!({"title": "Build"})));
    result(invoke(
        &mut host,
        &wren(),
        START,
        "acquire",
        json!({}),
        None,
    ));
    let fired: Vec<(String, String)> = host
        .fall_due(START + 1000, &runner())
        .into_iter()
        .map(|(name, outcome)| (name, veto_of(outcome).0))
        .collect();
    assert_eq!(
        fired,
        [("respond".to_owned(), "held_by_another".to_owned())]
    );
}

// ---- expireHolder, on the table as a whole (D104) ----

// jobs is a host of three jobs, j1 and j2 leased by wren and j3 by otto,
// each moved to running by its holder, under a config with an override.
fn jobs(config: Value) -> tables::Host {
    let mut host = tables::Host::new(core(), [job(config)]);
    for (key, holder) in [("j1", wren()), ("j2", wren()), ("j3", otto())] {
        changed(host.create("Job", &alice(), START, key, json!({"title": key})));
        let acquired =
            result(host.invoke("Job", &holder, START, key, "Lease", "acquire", json!({})));
        changed(host.invoke_fenced(
            "Job",
            &holder,
            START,
            key,
            "status",
            "transition",
            json!({"to": "running"}),
            acquired["token"].as_i64().expect("a token"),
        ));
    }
    host
}

fn expire_holder(host: &mut tables::Host, who: &Caller, now: i64, params: Value) -> Outcome {
    host.run(
        "Job",
        who,
        now,
        Action::TableOperation {
            use_id: "Lease".into(),
            operation: lease::EXPIRE_HOLDER.into(),
            params,
        },
    )
}

#[test]
fn expire_holder_expires_a_holders_leases_and_spares_a_renewed_one() {
    let config = json!({"ttlMs": 60_000, "overridePermission": "jobs.override",
        "onExpiry": {"from": ["running"], "transition": "queued"}});
    let mut host = jobs(config.clone());
    // wren renews j2 later; a gone holder's lease not renewed after the
    // time expires, the renewed one is spared and not written.
    let token = host
        .owned("Job", "j2", "Lease", "token")
        .as_i64()
        .expect("a token");
    changed(host.invoke_fenced(
        "Job",
        &wren(),
        START + 20_000,
        "j2",
        "Lease",
        "heartbeat",
        json!({}),
        token,
    ));
    let j2_version = host.row("Job", "j2").version;
    let changes = changed(expire_holder(
        &mut host,
        &boss(),
        START + 30_000,
        json!({"holder": "wren", "notRenewedAfter": START + 10_000}),
    ));
    assert_eq!(
        changes.result,
        json!({"expired": 1, "reasons": {"holder": 1}, "ids": ["j1"]})
    );
    let written: Vec<Option<&str>> = changes.rows.iter().map(|write| write.key()).collect();
    assert_eq!(written, [Some("j1")]);
    assert_eq!(host.owned("Job", "j1", "Lease", "holder"), Value::Null);
    assert_eq!(host.owned("Job", "j1", "status", "status"), json!("queued"));
    assert_eq!(host.row("Job", "j2").version, j2_version);
    assert_eq!(host.owned("Job", "j3", "Lease", "holder"), json!("otto"));
    // Without notRenewedAfter every lease the holder holds expires, a lapsed
    // one by its lapse, an active one by its holder's name.
    let changes = changed(expire_holder(
        &mut host,
        &boss(),
        START + 81_000,
        json!({"holder": "wren"}),
    ));
    assert_eq!(
        changes.result,
        json!({"expired": 1, "reasons": {"ttl": 1}, "ids": ["j2"]})
    );
    // A holder that holds none expires none and writes nothing.
    let changes = changed(expire_holder(
        &mut host,
        &boss(),
        START + 82_000,
        json!({"holder": "wren"}),
    ));
    assert_eq!(
        changes.result,
        json!({"expired": 0, "reasons": {}, "ids": []})
    );
    assert!(changes.rows.is_empty());
    // A row a guard vetoes is passed over, the rest expiring.
    let mut host = jobs(config);
    let spec = host.spec("Job").with(UseSpec::on_type("Gate", None));
    host.tables.specs.insert("Job".into(), spec);
    host.tables
        .rows
        .get_mut(&("Job".to_owned(), "j1".to_owned()))
        .expect("j1")
        .own
        .insert("title".into(), json!("stuck"));
    let changes = changed(expire_holder(
        &mut host,
        &boss(),
        START + 1_000,
        json!({"holder": "wren"}),
    ));
    assert_eq!(
        changes.result,
        json!({"expired": 1, "reasons": {"holder": 1}, "ids": ["j2"]})
    );
}

#[test]
fn expire_holder_expires_more_leases_than_the_host_loop_has_rounds() {
    // A gone worker held 300 jobs, more than core::MAX_ROUNDS: a row whose
    // call waits on a read goes on to the next, so the rounds go by how
    // deep each row's reads go, not by how many rows the holder held.
    let config = json!({"ttlMs": 60_000, "overridePermission": "jobs.override",
        "onExpiry": {"from": ["running"], "transition": "queued"}});
    let mut host = tables::Host::new(core(), [job(config)]);
    let keys: Vec<String> = (0..300).map(|n| format!("j{n:03}")).collect();
    for key in &keys {
        changed(host.create("Job", &alice(), START, key, json!({"title": key})));
        let acquired =
            result(host.invoke("Job", &wren(), START, key, "Lease", "acquire", json!({})));
        changed(host.invoke_fenced(
            "Job",
            &wren(),
            START,
            key,
            "status",
            "transition",
            json!({"to": "running"}),
            acquired["token"].as_i64().expect("a token"),
        ));
    }
    assert!(keys.len() > superschematic_rules::MAX_ROUNDS);
    let changes = changed(expire_holder(
        &mut host,
        &boss(),
        START + 1_000,
        json!({"holder": "wren"}),
    ));
    assert_eq!(
        changes.result,
        json!({"expired": 300, "reasons": {"holder": 300}, "ids": keys})
    );
    assert_eq!(changes.rows.len(), 300);
    for key in ["j000", "j150", "j299"] {
        assert_eq!(host.owned("Job", key, "Lease", "holder"), Value::Null);
        assert_eq!(host.owned("Job", key, "status", "status"), json!("queued"));
    }
}

#[test]
fn expire_holder_needs_the_override_permission() {
    let mut host = jobs(json!({"ttlMs": 60_000, "overridePermission": "jobs.override"}));
    let versions: Vec<i64> = ["j1", "j2", "j3"]
        .iter()
        .map(|key| host.row("Job", key).version)
        .collect();
    assert_eq!(
        refused(expire_holder(
            &mut host,
            &otto(),
            START,
            json!({"holder": "wren"})
        )),
        json!({"class": "EngineError", "code": "forbidden",
            "message": "otto may not expire the leases of a holder on Job: it needs permission jobs.override"})
    );
    let after: Vec<i64> = ["j1", "j2", "j3"]
        .iter()
        .map(|key| host.row("Job", key).version)
        .collect();
    assert_eq!(after, versions);
    let mut host = jobs(json!({"ttlMs": 60_000}));
    assert_eq!(
        refused(expire_holder(&mut host, &boss(), START, json!({"holder": "wren"})))["message"],
        "boss may not expire the leases of a holder on Job: its Lease config names no overridePermission"
    );
    // Its parameters are checked first.
    assert_eq!(
        refused(expire_holder(&mut host, &otto(), START, json!({"holder": "", "x": 1})))["message"],
        "operation expireHolder of behavior Lease: must NOT have additional properties: x; /holder: must NOT have fewer than 1 characters"
    );
    assert_eq!(
        refused(expire_holder(&mut host, &otto(), START, json!({})))["message"],
        "operation expireHolder of behavior Lease: must have required property 'holder'"
    );
}
