//! Retries on the pure core (D103, D104), through the core's public API over
//! a source in memory that keeps rows, their attempts' sub-table rows and
//! the deadlines the core arms: the engine's rules (each class's cap and
//! the total, a terminal class, limitsField narrowing and widening the caps
//! and held to limitsPermission and from the lease's holder, keepBest and
//! neverRegress, stuckAfter), and the waits (D104): a backoff's wait growing
//! by its factor up to maxMs and starting again after a success, retryAt
//! cleared by a success and by an exhausting failure, backing_off on a move
//! into retryInto and on Lease's acquire, then's deadline (moving, from
//! another state and armed again at the status's next entry, vetoed and
//! falling due again, held by another principal's lease, waiting for the
//! window), giveUpAfterMs at its boundary, the window and its
//! until, keyField read lazily after a raw write and reset at once by an
//! update, the detail kept in the sub-table, the Workflow status names among
//! two; composed with Lease (an exhausted row's acquire, a non-holder's
//! attempt); and the config and recordAttempt's parameters held to the
//! declaration file's schemas, resultField refused last.

mod support;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Behavior, Caller, ConfigError, ConfigTarget, Declaration, FieldSpec, GuardRequest,
    Hook, Outcome, TableDecl, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{retries, Retries, LIMITS_TYPES};
use support::{
    caller, changed, define, object, refused, result, rule, shape, undescribed, veto_of, Host,
    START,
};

// A job is queued, run, waits, and is done or failed; a failed one may be
// queued again.
fn flow() -> Value {
    json!({
        "states": ["queued", "running", "waiting", "done", "failed"],
        "transitions": [
            {"from": "queued", "to": "running"},
            {"from": "running", "to": "waiting"},
            {"from": "waiting", "to": "queued"},
            {"from": "running", "to": "queued"},
            {"from": "running", "to": "done"},
            {"from": "queued", "to": "failed"},
            {"from": "running", "to": "failed"},
            {"from": "waiting", "to": "failed"},
            {"from": "failed", "to": "queued"}
        ]
    })
}

// A job as a table holds it: Retries on the type before its Workflow
// field, with its title, its revision, its caps and its report.
fn job(config: Value) -> TypeSpec {
    TypeSpec::new("Job")
        .with(UseSpec::on_type("Retries", config))
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::new("revision", "Generic.Int64"))
        .with_field(FieldSpec::new("caps", "Generic.JSON"))
        .with_field(FieldSpec::new("report", "Generic.JSON"))
}

// leased is a job with Lease on the type too, before Retries.
fn leased(config: Value) -> TypeSpec {
    let spec = job(config);
    TypeSpec {
        uses: std::iter::once(UseSpec::on_type("Lease", json!({})))
            .chain(spec.uses.clone())
            .collect(),
        ..spec
    }
}

fn core() -> superschematic_rules::Core {
    superschematic_rules_behaviors::core().with(Hold)
}

fn alice() -> Caller {
    caller("alice", &[])
}

fn wren() -> Caller {
    caller("wren", &[])
}

fn runner() -> Caller {
    caller("runner", &[])
}

fn hosting(spec: TypeSpec, own: Value) -> Host {
    let mut host = Host::new(core(), spec);
    changed(host.create(&alice(), "j1", own));
    host
}

fn attempt(host: &mut Host, now: i64, params: Value) -> Outcome {
    host.invoke_at(&alice(), now, "j1", "Retries", "recordAttempt", params)
}

fn fail(host: &mut Host, now: i64, class: &str) -> Value {
    result(attempt(host, now, json!({ "failure": class })))
}

fn transition(host: &mut Host, now: i64, to: &str) -> Outcome {
    host.invoke_at(
        &alice(),
        now,
        "j1",
        "status",
        "transition",
        json!({ "to": to }),
    )
}

fn owned(host: &Host, use_id: &str) -> Value {
    let mut fields = host
        .row("j1")
        .owned
        .get(use_id)
        .cloned()
        .unwrap_or_default();
    fields.retain(|_, value| !value.is_null());
    Value::Object(fields)
}

fn status(host: &Host) -> Value {
    owned(host, "status")["status"].clone()
}

fn attempts(host: &Host) -> Vec<Value> {
    host.sub_rows("j1", "Retries", "attempts")
        .into_iter()
        .map(|row| Value::Object(row.values))
        .collect()
}

fn armed(host: &Host) -> Option<i64> {
    host.memory
        .armed
        .get(&("j1".to_owned(), "Retries".to_owned(), "retry".to_owned()))
        .copied()
}

// Hold: vetoes Workflow's transition of a row titled held, whoever calls.
struct Hold;

impl Behavior for Hold {
    type Config = ();

    fn declaration(&self) -> Declaration {
        Declaration::new("Hold")
    }

    fn parse_config(&self, _raw: &Value, _target: &ConfigTarget<'_>) -> Result<(), ConfigError> {
        Ok(())
    }

    fn guard(&self, view: &View<'_, ()>, request: &GuardRequest<'_>) -> Hook<Option<Veto>> {
        let held = view.own_field("title") == Some(json!("held"));
        Ok(
            (held && request.operation_of("status", "transition").is_some())
                .then(|| Veto::new("it is held")),
        )
    }
}

fn base(extra: Value) -> Value {
    let mut config = json!({
        "classes": {"timeout": {"attempts": 3}, "invalid": {"attempts": 1, "hint": "fix the input"}, "rejected": "terminal"},
        "totalAttempts": 4,
        "exhaustedState": "failed"
    });
    for (key, value) in object(extra) {
        config[key] = value;
    }
    config
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Retries.declaration(),
        Declaration::new("Retries")
            .field("total")
            .field("classAttempts")
            .field("bestScore")
            .field("exhausted")
            .field("stuck")
            .field("retryAt")
            .field("failingSince")
            .field("keyedBy")
            .sub_table(TableDecl::new(
                "attempts",
                "number",
                [
                    "number",
                    "at",
                    "failure",
                    "score",
                    "kept",
                    "signature",
                    "predicates",
                    "detail",
                    "subject",
                    "keyedBy"
                ]
            ))
            .arms_deadlines()
            .operation("recordAttempt", true)
            .veto("exhausted")
            .veto("limits_fixed")
            .veto("not_configured")
            .veto("backing_off")
            .veto("throttled")
    );
    assert_eq!(LIMITS_TYPES, ["Generic.JSON"]);
}

/// The checks Retries writes by hand are the declaration's configSchema,
/// keyword for keyword, and the shape refusals come out in the schema
/// loader's words, a class's oneOf as ajv reports it.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(retries::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let states = json!({"items": text, "minItems": 1, "type": "array", "uniqueItems": true});
    let name = "^[A-Za-z][A-Za-z0-9_.-]{0,63}$";
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "classes": {
                    "additionalProperties": {
                        "oneOf": [
                            {
                                "additionalProperties": false,
                                "properties": {
                                    "attempts": {"minimum": 1, "type": "integer"},
                                    "backoff": {
                                        "additionalProperties": false,
                                        "properties": {
                                            "factor": {"minimum": 1, "type": "number"},
                                            "firstMs": {"minimum": 1, "type": "integer"},
                                            "maxMs": {"minimum": 1, "type": "integer"}
                                        },
                                        "required": ["firstMs", "maxMs"],
                                        "type": "object"
                                    },
                                    "hint": {"maxLength": 2000, "minLength": 1, "type": "string"}
                                },
                                "required": ["attempts"],
                                "type": "object"
                            },
                            {"const": "terminal"}
                        ]
                    },
                    "minProperties": 1,
                    "propertyNames": {"not": {"const": "totalAttempts"}, "pattern": name},
                    "type": "object"
                },
                "exhaustedState": text,
                "from": states,
                "giveUpAfterMs": {"minimum": 1000, "type": "integer"},
                "keepBest": {
                    "additionalProperties": false,
                    "properties": {
                        "minDelta": {"minimum": 0, "type": "number"},
                        "neverRegress": {"items": {"pattern": name, "type": "string"}, "type": "array", "uniqueItems": true}
                    },
                    "type": "object"
                },
                "keyField": text,
                "limitsField": text,
                "limitsPermission": text,
                "permission": text,
                "resultField": text,
                "retryInto": states,
                "status": text,
                "stuckAfter": {"minimum": 1, "type": "integer"},
                "then": {
                    "additionalProperties": false,
                    "properties": {"from": states, "transition": text},
                    "required": ["transition", "from"],
                    "type": "object"
                },
                "totalAttempts": {"minimum": 1, "type": "integer"},
                "window": {
                    "additionalProperties": false,
                    "properties": {
                        "attempts": {"minimum": 1, "type": "integer"},
                        "ms": {"minimum": 1000, "type": "integer"}
                    },
                    "required": ["attempts", "ms"],
                    "type": "object"
                }
            },
            "required": ["classes", "totalAttempts", "exhaustedState"],
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &job(config));
    // The recorded scenario's.
    assert_eq!(
        shaped(json!({"classes": {"timeout": {"attempts": 1}}, "exhaustedState": "failed"})),
        ["/config: must have required property 'totalAttempts'"]
    );
    assert_eq!(
        shaped(json!({
            "classes": {
                "totalAttempts": {"attempts": 1},
                "1st": "terminal",
                "a": {"attempts": 0, "hint": "", "x": 1, "backoff": {"firstMs": 0, "factor": 0.5, "y": 2}},
                "b": "fatal",
                "c": {"hint": 5}
            },
            "totalAttempts": 0, "exhaustedState": "", "x": 1,
            "from": [], "retryInto": ["a", "a"], "giveUpAfterMs": 999, "stuckAfter": 0, "status": 1,
            "then": {"transition": "q"}, "window": {"attempts": 1.5},
            "keepBest": {"minDelta": -1, "neverRegress": ["ok", "no way", "ok"], "z": 0},
            "keyField": "", "limitsField": "", "limitsPermission": "", "permission": "", "resultField": ""
        })),
        [
            "/config: unknown key \"x\"",
            "/config/classes: must NOT be valid",
            "/config/classes: must match pattern \"^[A-Za-z][A-Za-z0-9_.-]{0,63}$\"",
            "/config/classes: property name must be valid",
            "/config/classes: property name must be valid",
            "/config/classes/a: unknown key \"x\"",
            "/config/classes/a: must be equal to constant",
            "/config/classes/a: must match exactly one schema in oneOf",
            "/config/classes/a/attempts: must be >= 1",
            "/config/classes/a/backoff: unknown key \"y\"",
            "/config/classes/a/backoff: must have required property 'maxMs'",
            "/config/classes/a/backoff/factor: must be >= 1",
            "/config/classes/a/backoff/firstMs: must be >= 1",
            "/config/classes/a/hint: must NOT have fewer than 1 characters",
            "/config/classes/b: must be equal to constant",
            "/config/classes/b: must match exactly one schema in oneOf",
            "/config/classes/b: must be object",
            "/config/classes/c: must be equal to constant",
            "/config/classes/c: must match exactly one schema in oneOf",
            "/config/classes/c: must have required property 'attempts'",
            "/config/classes/c/hint: must be string",
            "/config/exhaustedState: must NOT have fewer than 1 characters",
            "/config/from: must NOT have fewer than 1 items",
            "/config/giveUpAfterMs: must be >= 1000",
            "/config/keepBest: unknown key \"z\"",
            "/config/keepBest/minDelta: must be >= 0",
            "/config/keepBest/neverRegress: must NOT have duplicate items (items ## 2 and 0 are identical)",
            "/config/keepBest/neverRegress/1: must match pattern \"^[A-Za-z][A-Za-z0-9_.-]{0,63}$\"",
            "/config/keyField: must NOT have fewer than 1 characters",
            "/config/limitsField: must NOT have fewer than 1 characters",
            "/config/limitsPermission: must NOT have fewer than 1 characters",
            "/config/permission: must NOT have fewer than 1 characters",
            "/config/resultField: must NOT have fewer than 1 characters",
            "/config/retryInto: must NOT have duplicate items (items ## 1 and 0 are identical)",
            "/config/status: must be string",
            "/config/stuckAfter: must be >= 1",
            "/config/then: must have required property 'from'",
            "/config/totalAttempts: must be >= 1",
            "/config/window: must have required property 'ms'",
            "/config/window/attempts: must be integer"
        ]
    );
    assert_eq!(
        shaped(json!({"classes": {}, "totalAttempts": 1, "exhaustedState": "failed"})),
        ["/config/classes: must NOT have fewer than 1 properties"]
    );
}

/// recordAttempt's parameters are held to its closed paramsSchema in the
/// behavior registry's words.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(retries::DECLARATION).expect("it parses");
    let operation = &file["operations"][0];
    assert_eq!(operation["name"], "recordAttempt");
    assert_eq!(
        undescribed(&operation["paramsSchema"]),
        json!({"additionalProperties": false, "properties": {
            "detail": {"type": "object"},
            "failure": {"minLength": 1, "type": "string"},
            "predicates": {"additionalProperties": {"type": "boolean"}, "type": "object"},
            "result": {},
            "score": {"type": "number"},
            "signature": {"maxLength": 512, "minLength": 1, "type": "string"}
        }, "type": "object"})
    );
    let mut host = hosting(job(base(json!({}))), json!({"title": "Build"}));
    let issues = |host: &mut Host, params: Value| -> Vec<String> {
        refused(attempt(host, START, params))["details"]["issues"]
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
        issues(
            &mut host,
            json!({"failure": "", "score": "1", "signature": "", "detail": [], "predicates": {"a": 1}, "x": 1})
        ),
        [
            ": must NOT have additional properties: x",
            "/detail: must be object",
            "/failure: must NOT have fewer than 1 characters",
            "/predicates/a: must be boolean",
            "/score: must be number",
            "/signature: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        issues(
            &mut host,
            json!({"signature": "s".repeat(513), "predicates": []})
        ),
        [
            "/predicates: must be object",
            "/signature: must NOT have more than 512 characters"
        ]
    );
    // The handler's own refusals, in the engine's words.
    assert_eq!(
        refused(attempt(&mut host, START, json!({"result": {"ok": true}})))["message"],
        "operation recordAttempt of behavior Retries: /result: the config names no resultField to keep a result in"
    );
    assert_eq!(
        refused(attempt(&mut host, START, json!({"failure": "oom"})))["message"],
        "operation recordAttempt of behavior Retries: /failure: oom is not a failure class of Job (its classes: invalid, rejected, timeout)"
    );
}

/// The config's rules, refused in the engine's order and words, then the
/// waits', then resultField, last.
#[test]
fn the_config_is_held_to_the_type() {
    let core = core();
    let refused = |config: Value| rule(&core, &job(config), "Retries");
    let states = "queued, running, waiting, done, failed";
    assert_eq!(
        refused(base(json!({"limitsField": "missing"}))),
        "limitsField \"missing\" is not a field of Job (its fields: title, revision, caps, report)"
    );
    assert_eq!(
        refused(base(json!({"limitsField": "title"}))),
        "limitsField \"title\" is not an object field of Job"
    );
    assert_eq!(
        refused(base(json!({"resultField": "result"}))),
        "resultField \"result\" is not a field of Job (its fields: title, revision, caps, report)"
    );
    assert_eq!(
        refused(base(json!({"exhaustedState": "dead"}))),
        format!("exhaustedState \"dead\" is not a state of the type's Workflow ({states})")
    );
    assert_eq!(
        refused(base(json!({"from": ["paused"]}))),
        format!("from names \"paused\", which is not a state of the type's Workflow ({states})")
    );
    assert_eq!(
        refused(base(json!({"from": ["failed"]}))),
        "from names exhaustedState \"failed\", which exhaustion cannot move the status from"
    );
    assert_eq!(
        refused(base(json!({"from": ["done"]}))),
        "from: no transition of the type's Workflow leads from \"done\" to \"failed\""
    );
    assert_eq!(
        refused(base(json!({"keyField": "missing"}))),
        "keyField \"missing\" is not a field of Job (its fields: title, revision, caps, report)"
    );
    assert_eq!(
        refused(base(json!({"keyField": "caps"}))),
        "keyField \"caps\" is a Generic.JSON field of Job: the value that keys the counts is one scalar"
    );
    let backed = json!({"timeout": {"attempts": 3, "backoff": {"firstMs": 60000, "maxMs": 1000}}});
    assert_eq!(
        refused(base(json!({"classes": backed, "retryInto": ["running"]}))),
        "classes.timeout.backoff: maxMs (1000) is shorter than firstMs (60000)"
    );
    let backed = json!({"timeout": {"attempts": 3, "backoff": {"firstMs": 1000, "maxMs": 60000}}});
    assert_eq!(
        refused(base(json!({"classes": backed, "retryInto": ["paused"]}))),
        format!(
            "retryInto names \"paused\", which is not a state of the type's Workflow ({states})"
        )
    );
    assert_eq!(
        refused(base(json!({"classes": backed, "retryInto": ["failed"]}))),
        "retryInto names exhaustedState \"failed\", which an exhausted row moves to and stays in"
    );
    assert_eq!(
        refused(base(
            json!({"classes": backed, "then": {"transition": "queued", "from": ["done"]}})
        )),
        "then: no transition of the type's Workflow leads from \"done\" to \"queued\""
    );
    assert_eq!(
        refused(base(
            json!({"then": {"transition": "queued", "from": ["waiting"]}})
        )),
        "then moves the row when its backoff ends, and no class sets a backoff"
    );
    assert_eq!(
        refused(base(json!({"retryInto": ["running"]}))),
        "retryInto names the states an attempt starts in, which wait for a backoff or the window, and the config sets neither"
    );
    assert_eq!(
        refused(base(json!({"classes": backed}))),
        "classes.timeout.backoff sets when the row runs again (retryAt), which a move into retryInto, then and Lease's acquire wait for: the config names no retryInto and no then, and the type has no Lease"
    );
    assert_eq!(
        refused(base(json!({"window": {"attempts": 2, "ms": 60000}}))),
        "window caps the attempts that start, a move into retryInto or Lease's acquire: the config names no retryInto, and the type has no Lease"
    );
    assert_eq!(
        refused(base(json!({"resultField": "report"}))),
        "resultField writes a kept result to a field of the row through an update an operation makes, which waits for the core to run one (D103, step 4: Revisions)"
    );
    // On a type with Lease, a backoff and a window wait for its acquire.
    assert!(define(
        &core,
        &leased(base(
            json!({"classes": backed, "window": {"attempts": 2, "ms": 60000}})
        ))
    )
    .is_ok());
    // A type with no Workflow is refused in flow.rs's words.
    let bare = TypeSpec::new("Job").with(UseSpec::on_type("Retries", base(json!({}))));
    assert_eq!(
        rule(&core, &bare, "Retries"),
        "it reads the status of Workflow, which the type does not list"
    );
}

/// exhaustedState and the rest name the states of the Workflow status names
/// among several; the default from is every state but the terminal ones
/// and exhaustedState.
#[test]
fn the_states_are_the_workflow_status_names() {
    let core = core();
    let two = |config: Value| {
        TypeSpec::new("Job")
            .with(UseSpec::on_type("Retries", config))
            .with(UseSpec::on_field("status", "Workflow", flow()))
            .with(UseSpec::on_field("phase", "Workflow", json!({"states": ["open", "dead"], "transitions": [{"from": "open", "to": "dead"}]})))
    };
    assert_eq!(
        rule(&core, &two(base(json!({}))), "Retries"),
        "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    assert_eq!(
        rule(&core, &two(base(json!({"status": "phase"}))), "Retries"),
        "exhaustedState \"failed\" is not a state of the type's Workflow (open, dead)"
    );
    let mut host = Host::new(
        core,
        two(base(json!({"status": "phase", "exhaustedState": "dead"}))),
    );
    changed(host.create(&alice(), "j1", json!({})));
    fail(&mut host, START, "rejected");
    assert_eq!(owned(&host, "phase")["status"], "dead");
    assert_eq!(status(&host), "queued");
}

// ---- the engine's rules ----

/// A failure counts against its class's cap and the total; a terminal class
/// exhausts at once; a class at its cap exhausts when no other has room;
/// a failure with no room left is not counted; and exhaustion moves the
/// status, after which every move but into exhaustedState is refused, and
/// so is another attempt.
#[test]
fn a_failure_counts_against_its_caps() {
    let mut host = hosting(job(base(json!({}))), json!({"title": "Build"}));
    assert_eq!(
        owned(&host, "Retries"),
        json!({"total": 0, "classAttempts": {"invalid": 0, "rejected": 0, "timeout": 0}, "exhausted": false, "stuck": false})
    );
    changed(transition(&mut host, START, "running"));
    assert_eq!(
        fail(&mut host, START, "invalid"),
        json!({"failure": "invalid", "score": null, "kept": false, "total": 1, "classAttempts": {"invalid": 1, "rejected": 0, "timeout": 0}, "exhausted": false, "stuck": false, "hint": "fix the input"})
    );
    // invalid is at its cap: not counted, and exhausted.
    let full = fail(&mut host, START, "invalid");
    assert_eq!(full["total"], 1);
    assert_eq!(full["exhausted"], true);
    assert_eq!(status(&host), "failed");
    assert_eq!(
        veto_of(transition(&mut host, START, "queued")).0,
        "exhausted"
    );
    assert_eq!(
        refused(transition(&mut host, START, "queued"))["details"]["reason"],
        "its retries are exhausted, so its status moves only to failed"
    );
    assert_eq!(
        refused(attempt(&mut host, START, json!({})))["details"]["reason"],
        "its retries are exhausted"
    );
    // A terminal class exhausts at once, and the total's cap.
    let mut host = hosting(job(base(json!({}))), json!({"title": "Build"}));
    assert_eq!(fail(&mut host, START, "rejected")["exhausted"], true);
    let mut host = hosting(job(base(json!({}))), json!({"title": "Build"}));
    for (n, exhausted) in [(1, false), (2, false), (3, false)] {
        let outcome = fail(&mut host, START, "timeout");
        assert_eq!(outcome["total"], n);
        assert_eq!(outcome["exhausted"], exhausted, "{n}");
    }
    // timeout at its cap with invalid's room left goes on; then invalid
    // reaches the total.
    let last = fail(&mut host, START, "invalid");
    assert_eq!(
        (last["total"].clone(), last["exhausted"].clone()),
        (json!(4), json!(true))
    );
}

/// limitsField narrows and widens the caps; a change of it needs
/// limitsPermission, which the holder of the row's active lease may not use.
#[test]
fn limits_field_holds_the_rows_own_caps() {
    let config = base(json!({"limitsField": "caps", "limitsPermission": "jobs.limits"}));
    let mut host = hosting(
        leased(config),
        json!({"title": "Build", "caps": {"timeout": 1, "totalAttempts": 9, "rejected": 5, "x": "y"}}),
    );
    assert_eq!(fail(&mut host, START, "timeout")["exhausted"], false);
    // timeout's cap is 1 now, invalid has room: the next is not counted.
    let capped = fail(&mut host, START, "timeout");
    assert_eq!(capped["total"], 1);
    assert_eq!(capped["exhausted"], true);
    let mut host = hosting(
        leased(config_without_permission()),
        json!({"title": "Build", "caps": {}}),
    );
    assert_eq!(
        veto_of(host.update(&alice(), "j1", json!({"caps": {"timeout": 9}}))).0,
        "not_configured"
    );
    let mut host = hosting(
        leased(base(
            json!({"limitsField": "caps", "limitsPermission": "jobs.limits"}),
        )),
        json!({"title": "Build", "caps": {}}),
    );
    assert_eq!(
        refused(host.update(&alice(), "j1", json!({"caps": {"timeout": 9}}))),
        json!({"class": "EngineError", "code": "forbidden", "message": "alice may not change caps, the caps of Job j1: it needs permission jobs.limits"})
    );
    let limiter = caller("lim", &["jobs.limits"]);
    result(host.invoke(&limiter, "j1", "Lease", "acquire", json!({})));
    assert_eq!(
        refused(host.update(&limiter, "j1", json!({"caps": {"timeout": 9}})))["details"]["reason"],
        "caps holds the caps its holder's attempts count against, so the holder of its lease does not change it"
    );
    // An update by the holder that leaves the field as it was passes.
    changed(host.update(&limiter, "j1", json!({"caps": {}, "title": "x"})));
    // With the lease released, a caller with the permission changes it.
    let presented = Map::from_iter([("Lease".to_owned(), json!({"token": 1}))]);
    let release = Action::Operation {
        key: "j1".into(),
        use_id: "Lease".into(),
        operation: "release".into(),
        params: json!({}),
    };
    result(host.run_presenting(&limiter, START, release, presented));
    changed(host.update(
        &caller("ops", &["jobs.limits"]),
        "j1",
        json!({"caps": {"timeout": 9}}),
    ));
}

fn config_without_permission() -> Value {
    base(json!({"limitsField": "caps"}))
}

/// keepBest keeps a failure's result that beats the best kept score by
/// minDelta and loses no neverRegress predicate the last kept one held;
/// stuckAfter exhausts a streak of failures not kept with one signature.
#[test]
fn keep_best_and_stuck_after_read_the_attempts() {
    let config = base(json!({
        "classes": {"timeout": {"attempts": 9}},
        "totalAttempts": 9,
        "keepBest": {"minDelta": 0.5, "neverRegress": ["compiles"]},
        "stuckAfter": 3
    }));
    let mut host = hosting(job(config), json!({"title": "Build"}));
    let tried = |host: &mut Host, params: Value| result(attempt(host, START, params));
    assert_eq!(
        tried(
            &mut host,
            json!({"failure": "timeout", "score": 1, "predicates": {"compiles": true}})
        )["kept"],
        true
    );
    assert_eq!(owned(&host, "Retries")["bestScore"], 1);
    assert_eq!(
        tried(&mut host, json!({"failure": "timeout", "score": 1.25}))["kept"],
        false
    );
    // A better score that loses compiles is not kept.
    assert_eq!(
        tried(
            &mut host,
            json!({"failure": "timeout", "score": 3, "predicates": {"compiles": false}})
        )["kept"],
        false
    );
    assert_eq!(
        tried(
            &mut host,
            json!({"failure": "timeout", "score": 2.5, "predicates": {"compiles": true}})
        )["kept"],
        true
    );
    assert_eq!(owned(&host, "Retries")["bestScore"], 2.5);
    // Three failures in a row not kept with one signature are stuck.
    for (n, stuck) in [(1, false), (2, false), (3, true)] {
        let outcome = tried(&mut host, json!({"failure": "timeout", "signature": "E1"}));
        assert_eq!(outcome["stuck"], stuck, "{n}");
    }
    assert_eq!(owned(&host, "Retries")["exhausted"], true);
    assert_eq!(
        refused(attempt(&mut host, START, json!({})))["details"]["reason"],
        "its retries are exhausted, stuck on one failure"
    );
    // A kept attempt between breaks the streak.
    let mut host = hosting(
        job(base(
            json!({"classes": {"timeout": {"attempts": 9}}, "totalAttempts": 9, "stuckAfter": 2}),
        )),
        json!({"title": "Build"}),
    );
    assert_eq!(
        tried(&mut host, json!({"failure": "timeout", "signature": "E1"}))["stuck"],
        false
    );
    assert_eq!(tried(&mut host, json!({}))["kept"], true);
    assert_eq!(
        tried(&mut host, json!({"failure": "timeout", "signature": "E1"}))["stuck"],
        false
    );
    assert_eq!(
        tried(&mut host, json!({"failure": "timeout", "signature": "E1"}))["stuck"],
        true
    );
}

// ---- the waits ----

/// A failure in a class with a backoff sets retryAt, firstMs × factor^(n-1)
/// after it, at most maxMs, n the class's failures since the last success;
/// a move into retryInto and Lease's acquire wait for it; a success, and an
/// exhausting failure, clear it; and the attempt itself never waits.
#[test]
fn a_backoff_holds_the_next_start() {
    let config = base(json!({
        "classes": {"timeout": {"attempts": 9, "backoff": {"firstMs": 1000, "maxMs": 5000, "factor": 2.5}}, "rejected": "terminal"},
        "totalAttempts": 9,
        "retryInto": ["running"]
    }));
    let mut host = hosting(leased(config), json!({"title": "Build"}));
    changed(transition(&mut host, START, "running"));
    assert_eq!(fail(&mut host, START, "timeout")["retryAt"], START + 1000);
    assert_eq!(fail(&mut host, START, "timeout")["retryAt"], START + 2500);
    assert_eq!(fail(&mut host, START, "timeout")["retryAt"], START + 5000);
    assert_eq!(owned(&host, "Retries")["retryAt"], START + 5000);
    changed(transition(&mut host, START, "queued"));
    let error = refused(transition(&mut host, START + 4999, "running"));
    assert_eq!(error["details"]["vetoCode"], "backing_off");
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"retryAt": 1_005_000})
    );
    assert_eq!(
        error["details"]["reason"],
        "it waits until 1970-01-01T00:16:45.000Z before its next attempt: its last one failed in a class with a backoff"
    );
    assert_eq!(
        veto_of(host.invoke_at(&wren(), START + 4999, "j1", "Lease", "acquire", json!({}))).0,
        "backing_off"
    );
    // A move into a state retryInto does not list does not wait.
    changed(transition(&mut host, START + 4999, "failed"));
    changed(transition(&mut host, START + 4999, "queued"));
    // The attempt is recorded whatever the wait; a success clears it and
    // starts the count again.
    assert_eq!(
        result(attempt(&mut host, START + 4999, json!({}))).get("retryAt"),
        None
    );
    assert_eq!(owned(&host, "Retries").get("retryAt"), None);
    changed(transition(&mut host, START + 4999, "running"));
    assert_eq!(
        fail(&mut host, START + 6000, "timeout")["retryAt"],
        START + 7000
    );
    // An exhausting failure clears it.
    let last = fail(&mut host, START + 6000, "rejected");
    assert_eq!(last["exhausted"], true);
    assert_eq!(last.get("retryAt"), None);
    assert_eq!(owned(&host, "Retries").get("retryAt"), None);
}

/// then's deadline, armed at retryAt, moves the row from its from states
/// when the backoff ends; a row in another state stays; a guard's veto
/// leaves the status; and a success clears the deadline.
#[test]
fn then_moves_the_row_when_its_backoff_ends() {
    let config = base(json!({
        "classes": {"timeout": {"attempts": 9, "backoff": {"firstMs": 60000, "maxMs": 60000}}},
        "totalAttempts": 9,
        "then": {"transition": "queued", "from": ["waiting"]}
    }));
    let mut host = hosting(job(config.clone()), json!({"title": "Build"}));
    changed(transition(&mut host, START, "running"));
    fail(&mut host, START, "timeout");
    assert_eq!(armed(&host), Some(1_060_000));
    changed(transition(&mut host, START, "waiting"));
    for (_, outcome) in host.fall_due(1_060_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "queued");
    assert_eq!(owned(&host, "status")["enteredAt"], 1_060_000);
    // In another state, it stays, and the deadline is spent; the status's
    // next entry into one of then's from arms it again at once, and an
    // entry before retryAt arms nothing, the deadline still armed then.
    let mut host = hosting(job(config.clone()), json!({"title": "Build"}));
    changed(transition(&mut host, START, "running"));
    fail(&mut host, START, "timeout");
    for (_, outcome) in host.fall_due(1_060_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "running");
    assert_eq!(armed(&host), None);
    changed(transition(&mut host, 1_090_000, "waiting"));
    assert_eq!(armed(&host), Some(1_090_000));
    for (_, outcome) in host.fall_due(1_090_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "queued");
    assert_eq!(armed(&host), None);
    // A write that leaves the status in from arms nothing again.
    let mut host = hosting(job(config.clone()), json!({"title": "Build"}));
    changed(transition(&mut host, START, "running"));
    fail(&mut host, START, "timeout");
    changed(transition(&mut host, START, "waiting"));
    assert_eq!(armed(&host), Some(1_060_000));
    changed(host.update_at(&alice(), 1_070_000, "j1", json!({"title": "Renamed"})));
    assert_eq!(armed(&host), Some(1_060_000));
    // A guard's veto refuses the fire, as Timers' is, and leaves the
    // deadline armed to fall due again (the worker's next minute), and the
    // status as it was.
    let mut host = Host::new(
        core(),
        job(config.clone()).with(UseSpec::on_type("Hold", None)),
    );
    changed(host.create(&alice(), "j1", json!({"title": "held"})));
    host.memory
        .rows
        .get_mut("j1")
        .expect("j1")
        .owned
        .get_mut("status")
        .expect("status")
        .insert("status".into(), json!("waiting"));
    fail(&mut host, START, "timeout");
    for (_, outcome) in host.fall_due(1_060_000, &runner()) {
        assert_eq!(refused(outcome)["details"]["reason"], "it is held");
    }
    assert_eq!(status(&host), "waiting");
    assert_eq!(armed(&host), Some(1_060_000));
    changed(host.update(&alice(), "j1", json!({"title": "free"})));
    for (_, outcome) in host.fall_due(1_120_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "queued");
    // A success clears it.
    let mut host = hosting(job(config), json!({"title": "Build"}));
    changed(transition(&mut host, START, "running"));
    fail(&mut host, START, "timeout");
    result(attempt(&mut host, START + 1, json!({})));
    assert_eq!(armed(&host), None);
}

/// then's move is the worker's request, which the core tells each guard
/// (D103): another principal's active lease holds it as it holds Timers'
/// fire, refusing the fire, which falls due again; once the lease ends the
/// move goes through. The holder's own writes go through meanwhile.
#[test]
fn then_meets_another_principals_active_lease() {
    let config = base(json!({
        "classes": {"timeout": {"attempts": 9, "backoff": {"firstMs": 10000, "maxMs": 10000}}},
        "totalAttempts": 9,
        "then": {"transition": "queued", "from": ["waiting"]}
    }));
    let mut host = hosting(leased(config), json!({"title": "Build"}));
    let acquired = result(host.invoke(&wren(), "j1", "Lease", "acquire", json!({})));
    assert_eq!(acquired["token"], 1);
    let move_to = |to: &str| Action::Operation {
        key: "j1".into(),
        use_id: "status".into(),
        operation: "transition".into(),
        params: json!({ "to": to }),
    };
    changed(host.run(&wren(), START, move_to("running")));
    result(host.invoke(
        &wren(),
        "j1",
        "Retries",
        "recordAttempt",
        json!({"failure": "timeout"}),
    ));
    changed(host.run(&wren(), START, move_to("waiting")));
    assert_eq!(armed(&host), Some(START + 10_000));
    for (_, outcome) in host.fall_due(START + 10_000, &runner()) {
        let (code, details) = veto_of(outcome);
        assert_eq!(code, "held_by_another");
        assert_eq!(details["expiresAt"], START + 60_000);
    }
    assert_eq!(status(&host), "waiting");
    assert_eq!(armed(&host), Some(START + 10_000));
    // wren releases its lease, presenting its token: the next fire moves it.
    let presented = Map::from_iter([("Lease".to_owned(), json!({"token": 1}))]);
    let release = Action::Operation {
        key: "j1".into(),
        use_id: "Lease".into(),
        operation: "release".into(),
        params: json!({}),
    };
    result(host.run_presenting(&wren(), START + 20_000, release, presented));
    for (_, outcome) in host.fall_due(START + 80_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "queued");
    assert_eq!(owned(&host, "status")["enteredAt"], START + 80_000);
}

/// A then that moves the row into retryInto waits for the window too: a
/// fire while the window holds its attempts arms the deadline again at the
/// window's until, and the fire then moves the row.
#[test]
fn then_into_retry_into_waits_for_the_window() {
    let flowing = |config: Value| {
        TypeSpec::new("Job")
            .with(UseSpec::on_type("Retries", config))
            .with(UseSpec::on_field(
                "status",
                "Workflow",
                json!({
                    "states": ["queued", "running", "waiting", "failed"],
                    "transitions": [
                        {"from": "queued", "to": "running"},
                        {"from": "running", "to": "waiting"},
                        {"from": "waiting", "to": "running"},
                        {"from": "running", "to": "failed"},
                        {"from": "waiting", "to": "failed"}
                    ]
                }),
            ))
    };
    let config = json!({
        "classes": {"timeout": {"attempts": 9, "backoff": {"firstMs": 10000, "maxMs": 10000}}},
        "totalAttempts": 9,
        "exhaustedState": "failed",
        "retryInto": ["running"],
        "then": {"transition": "running", "from": ["waiting"]},
        "window": {"attempts": 2, "ms": 600000}
    });
    let mut host = Host::new(core(), flowing(config));
    changed(host.create(&alice(), "j1", json!({})));
    changed(transition(&mut host, START, "running"));
    fail(&mut host, START, "timeout");
    changed(transition(&mut host, START, "waiting"));
    // One attempt in the window: the fire moves it.
    for (_, outcome) in host.fall_due(START + 10_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "running");
    fail(&mut host, START + 20_000, "timeout");
    changed(transition(&mut host, START + 20_000, "waiting"));
    // Two: the fire arms the deadline again at the oldest's departure.
    for (_, outcome) in host.fall_due(START + 30_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "waiting");
    assert_eq!(armed(&host), Some(START + 600_000));
    for (_, outcome) in host.fall_due(START + 600_000, &runner()) {
        changed(outcome);
    }
    assert_eq!(status(&host), "running");
    assert_eq!(owned(&host, "status")["enteredAt"], START + 600_000);
}

/// giveUpAfterMs exhausts at the first failure that long after the first
/// failure since the last success, after it counts; a success starts the
/// clock again.
#[test]
fn give_up_after_ms_counts_from_the_first_failure() {
    let mut host = hosting(
        job(base(
            json!({"classes": {"timeout": {"attempts": 9}}, "totalAttempts": 9, "giveUpAfterMs": 10000}),
        )),
        json!({"title": "Build"}),
    );
    fail(&mut host, START, "timeout");
    assert_eq!(owned(&host, "Retries")["failingSince"], START);
    assert_eq!(fail(&mut host, START + 9999, "timeout")["exhausted"], false);
    result(attempt(&mut host, START + 9999, json!({})));
    assert_eq!(owned(&host, "Retries").get("failingSince"), None);
    fail(&mut host, START + 20_000, "timeout");
    assert_eq!(
        fail(&mut host, START + 29_999, "timeout")["exhausted"],
        false
    );
    let last = fail(&mut host, START + 30_000, "timeout");
    assert_eq!(
        (last["total"].clone(), last["exhausted"].clone()),
        (json!(5), json!(true))
    );
    assert_eq!(owned(&host, "Retries")["failingSince"], START + 20_000);
}

/// The window holds the next start while it holds its attempts, until the
/// oldest of the last ones leaves it, whatever their outcome; with more
/// attempts in it than the cap, until is when enough of them have left.
#[test]
fn the_window_throttles_the_next_start() {
    let config = base(
        json!({"classes": {"timeout": {"attempts": 9}}, "totalAttempts": 9, "retryInto": ["running"], "window": {"attempts": 2, "ms": 10000}}),
    );
    let mut host = hosting(job(config), json!({"title": "Build"}));
    for at in [START, START + 1000, START + 2000] {
        // Each is recorded whatever the window holds.
        result(attempt(&mut host, at, json!({})));
    }
    let error = refused(transition(&mut host, START + 3000, "running"));
    assert_eq!(error["details"]["vetoCode"], "throttled");
    assert_eq!(
        error["details"]["vetoDetails"],
        json!({"until": START + 11_000})
    );
    assert_eq!(
        error["details"]["reason"],
        "it has had 3 attempts in the last 10000 ms, and its window allows 2: its next attempt waits until 1970-01-01T00:16:51.000Z"
    );
    assert_eq!(
        refused(transition(&mut host, START + 10_999, "running"))["details"]["vetoDetails"],
        json!({"until": START + 11_000})
    );
    changed(transition(&mut host, START + 11_000, "running"));
}

/// keyField keys the counts: a row whose field holds another value than its
/// counts were made under reads as fresh, as after a raw write of it, and
/// its next attempt writes them anew; an update through the core resets
/// them at once; the attempts keep the key they counted under and their
/// detail.
#[test]
fn key_field_starts_the_counts_again() {
    let config = base(
        json!({"classes": {"build": {"attempts": 1}}, "totalAttempts": 5, "keyField": "revision"}),
    );
    let mut host = hosting(job(config), json!({"title": "Build", "revision": 1}));
    assert_eq!(owned(&host, "Retries")["keyedBy"], "1");
    let first = result(attempt(
        &mut host,
        START,
        json!({"failure": "build", "detail": {"log": "line 3"}}),
    ));
    assert_eq!(first["exhausted"], true);
    // A raw write of the field: the next decision reads the row as fresh.
    host.memory
        .rows
        .get_mut("j1")
        .expect("j1")
        .own
        .insert("revision".into(), json!(2));
    let again = result(attempt(&mut host, START + 1, json!({"failure": "build"})));
    assert_eq!(
        (again["total"].clone(), again["exhausted"].clone()),
        (json!(1), json!(true))
    );
    assert_eq!(owned(&host, "Retries")["keyedBy"], "2");
    // An update through the core resets them at once.
    let record = changed(host.update(&alice(), "j1", json!({"revision": 3})));
    assert_eq!(record.owned("Retries", "keyedBy"), Some(&json!("3")));
    assert_eq!(
        owned(&host, "Retries"),
        json!({"total": 0, "classAttempts": {"build": 0}, "exhausted": false, "stuck": false, "keyedBy": "3"})
    );
    changed(host.update(&alice(), "j1", json!({"title": "Renamed"})));
    assert_eq!(owned(&host, "Retries")["keyedBy"], "3");
    let rows = attempts(&host);
    assert_eq!(rows[0]["detail"], json!({"log": "line 3"}));
    assert_eq!(rows[0]["keyedBy"], "1");
    assert_eq!(rows[1]["keyedBy"], "2");
    assert_eq!(rows[0]["subject"], "alice");
    assert_eq!(
        host.sub_rows("j1", "Retries", "attempts")[1].key,
        "0000000002"
    );
}

// ---- composition with Lease ----

/// An exhausted row is not taken again (Lease's acquire), and Lease's guard
/// holds another principal's attempt under an active lease.
#[test]
fn retries_and_lease_hold_each_other() {
    let mut host = hosting(leased(base(json!({}))), json!({"title": "Build"}));
    result(host.invoke(&wren(), "j1", "Lease", "acquire", json!({})));
    assert_eq!(
        veto_of(attempt(&mut host, START, json!({"failure": "rejected"}))),
        (
            "held_by_another".to_owned(),
            json!({"expiresAt": 1_060_000})
        )
    );
    result(host.invoke(
        &wren(),
        "j1",
        "Retries",
        "recordAttempt",
        json!({"failure": "rejected"}),
    ));
    let presented = Map::from_iter([("Lease".to_owned(), json!({"token": 1}))]);
    let release = Action::Operation {
        key: "j1".into(),
        use_id: "Lease".into(),
        operation: "release".into(),
        params: json!({}),
    };
    result(host.run_presenting(&wren(), START, release, presented));
    let error = refused(host.invoke(&alice(), "j1", "Lease", "acquire", json!({})));
    assert_eq!(
        error["message"],
        "behavior Retries vetoes acquire of Job j1: its retries are exhausted, so it is not taken again"
    );
}
