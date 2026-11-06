//! Schedules on the pure core (D104, step 3), through the core's public API
//! over a source in memory that keeps rows, their sub-table rows and the
//! deadlines the core arms: the declaration and the config held to the
//! file's schemas, each config rule in order; the parameters held to the
//! paramsSchemas, each settle refusal and its order (the parameters before
//! the permission before the row, an ended row before the limit); a set of
//! each kind, its view and its deadline, a repeat unchanged under a
//! normalized line and zone, a replace and a set over a finished name;
//! pause, resume and cancel and their repeats, a resume past its time; the
//! fire of each kind, an overdue every once at its grid, an after
//! finishing, a line a raw write left with no run finishing; a stale fire
//! of a paused, a finished and a missing schedule; a due a raw write moved
//! later armed again; a retry before and after the next run, its fire as
//! the run's next attempt, a retry the next run due too drops, another
//! outcome cancelling it, an after revived for its retry and the revival
//! refused at max; a report of an older run and of a run not had; a name's
//! runs counted on across a replace and a set again, and a late report of a
//! replaced schedule's run or of an earlier attempt changing nothing; endOn
//! through a transition, through a heard write, on a fire, and status
//! naming one of two Workflows; pruning; and every run payload held to the
//! file's payloadSchema.

mod support;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Behavior, Caller, Declaration, OutboxEvent, Outcome, RowChange, RowImage, TableDecl,
    TypeSpec, UseSpec, WriteKind,
};
use superschematic_rules_behaviors::{core, schedules, Schedules, SchedulesKind};
use support::{caller, changed, define, refused, result, rule, shape, undescribed, veto_of, Host};

// 2026-03-01T00:00:00Z.
const T0: i64 = 1_772_323_200_000;
const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;

fn flow() -> Value {
    json!({
        "states": ["open", "working", "done", "dropped"],
        "transitions": [
            {"from": "open", "to": "working"}, {"from": "working", "to": "done"},
            {"from": "working", "to": "open"}, {"from": "open", "to": "dropped"},
            {"from": "working", "to": "dropped"}
        ]
    })
}

// The agent's wake-ups, as the scenarios' config has them.
fn wakes() -> Value {
    json!({
        "max": 3, "minMs": 120_000, "maxMs": 2_592_000_000_i64,
        "kinds": ["after", "every", "cron"], "timeZone": "America/New_York",
        "outcomes": ["told", "resumed", "held", "missed"],
        "retry": {"outcome": "missed", "afterMs": 300_000},
        "endOn": {"enters": ["done", "dropped"]},
        "permission": "problems.schedule"
    })
}

// A problem with its Workflow on the type and Schedules beside it.
fn problem(config: Value) -> TypeSpec {
    TypeSpec::new("Problem")
        .with(UseSpec::on_type("Workflow", flow()))
        .with(UseSpec::on_type("Schedules", config))
}

fn hosting(config: Value) -> Host {
    let mut host = Host::new(core(), problem(config));
    changed(host.run(
        &agent(),
        T0,
        Action::Create {
            id: Some("p1".to_owned()),
            own: Map::new(),
            params: Map::new(),
        },
    ));
    host
}

fn agent() -> Caller {
    caller("agent", &["problems.schedule"])
}

fn guest() -> Caller {
    caller("guest", &[])
}

fn runner() -> Caller {
    caller("runner", &[])
}

fn call(host: &mut Host, now: i64, operation: &str, params: Value) -> Outcome {
    host.invoke_at(&agent(), now, "p1", "Schedules", operation, params)
}

fn set(host: &mut Host, now: i64, params: Value) -> Value {
    result(call(host, now, schedules::SET_SCHEDULE, params))
}

// fire runs every deadline due at a time and answers the events they wrote.
fn fire(host: &mut Host, now: i64) -> Vec<OutboxEvent> {
    host.fall_due(now, &runner())
        .into_iter()
        .flat_map(|(_, outcome)| changed(outcome).events)
        .collect()
}

// payloads is the payloads of a fire's events, each held to the file's
// payloadSchema.
fn payloads(host: &mut Host, now: i64) -> Vec<Value> {
    fire(host, now)
        .into_iter()
        .map(|event| {
            assert_eq!(event.kind, schedules::RUN);
            assert_eq!(event.use_id, "Schedules");
            holds_payload(&event.payload);
            event.payload
        })
        .collect()
}

// armed is when the deadline of a schedule falls due; None when unarmed.
fn armed(host: &Host, name: &str) -> Option<i64> {
    host.memory
        .armed
        .get(&("p1".to_owned(), "Schedules".to_owned(), name.to_owned()))
        .copied()
}

fn list(host: &mut Host) -> Value {
    result(call(host, T0, schedules::LIST_SCHEDULES, json!({})))["items"].clone()
}

fn one(host: &mut Host, name: &str) -> Value {
    result(call(
        host,
        T0,
        schedules::LIST_SCHEDULES,
        json!({ "name": name }),
    ))["items"][0]
        .clone()
}

// params_refusal is an invalid parameters refusal's issues, each as
// `<path>: <message>`.
fn params_refusal(outcome: Outcome) -> Vec<String> {
    let error = refused(outcome);
    assert_eq!(error["class"], "OperationParamsError", "{error}");
    error["details"]["issues"]
        .as_array()
        .expect("issues")
        .iter()
        .map(|issue| {
            let path = issue["path"].as_str().unwrap_or_default();
            let message = issue["message"].as_str().unwrap_or_default();
            if path.is_empty() {
                message.to_owned()
            } else {
                format!("{path}: {message}")
            }
        })
        .collect()
}

fn version(host: &Host) -> i64 {
    host.row("p1").version
}

// A raw SQL write of the sub-table's row of a schedule, past the core.
fn raw(host: &mut Host, name: &str, values: Value) {
    let row = host
        .memory
        .sub_rows
        .get_mut(&(
            "p1".to_owned(),
            "Schedules".to_owned(),
            "schedules".to_owned(),
            name.to_owned(),
        ))
        .expect("the schedule's row");
    for (field, value) in values.as_object().expect("an object") {
        if value.is_null() {
            row.remove(field);
        } else {
            row.insert(field.clone(), value.clone());
        }
    }
}

// heard runs the change command of a write of p1 at a time, as the worker
// runs the table's change queue, on the row as it is.
fn heard(host: &mut Host, now: i64) -> Outcome {
    let image = RowImage {
        own: Map::new(),
        owned: host.row("p1").owned.clone(),
    };
    host.run(
        &runner(),
        now,
        Action::Change {
            key: "p1".to_owned(),
            change: RowChange {
                write: WriteKind::Update,
                at: now,
                before: Some(image.clone()),
                after: Some(image),
            },
        },
    )
}

// holds_payload holds a run event's payload to the declaration file's
// payloadSchema: an object of the members it declares alone, each required
// one present, each of its type, enum and minimum.
fn holds_payload(payload: &Value) {
    let file: Value = serde_json::from_str(schedules::DECLARATION).expect("it parses");
    let schema = &file["events"][0]["payloadSchema"];
    let members = payload.as_object().expect("an object payload");
    for required in schema["required"].as_array().expect("required") {
        let required = required.as_str().unwrap_or_default();
        assert!(members.contains_key(required), "{payload} lacks {required}");
    }
    for (name, value) in members {
        let property = &schema["properties"][name];
        assert!(
            property.is_object(),
            "{payload} holds {name}, which the payloadSchema does not declare"
        );
        match property["type"].as_str() {
            Some("integer") => assert!(value.is_i64(), "{name} of {payload} is no integer"),
            Some("string") => assert!(value.is_string(), "{name} of {payload} is no string"),
            _ => {}
        }
        if let Some(allowed) = property["enum"].as_array() {
            assert!(
                allowed.contains(value),
                "{name} of {payload} is none of {allowed:?}"
            );
        }
        if let Some(minimum) = property["minimum"].as_i64() {
            assert!(
                value.as_i64() >= Some(minimum),
                "{name} of {payload} is under {minimum}"
            );
        }
    }
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    let declared = Schedules.declaration();
    assert_eq!(
        declared,
        Declaration::new("Schedules")
            .sub_table(TableDecl::new(
                "schedules",
                "name",
                [
                    "name",
                    "kind",
                    "spec",
                    "timeZone",
                    "state",
                    "setAt",
                    "nextAt",
                    "retryAt",
                    "runs",
                    "attempt",
                    "lastAt",
                    "lastOutcome",
                    "finishedAt",
                    "endedBy"
                ]
            ))
            .arms_deadlines()
            .hears_changes()
            .operation("setSchedule", true)
            .operation("pauseSchedule", true)
            .operation("resumeSchedule", true)
            .operation("cancelSchedule", true)
            .operation("reportRun", true)
            .operation("listSchedules", false)
            .event("run")
            .veto("no_schedule")
            .veto("schedule_limit")
            .veto("schedule_finished")
            .veto("schedules_ended")
            .veto("no_run")
    );
    let file: Value = serde_json::from_str(schedules::DECLARATION).expect("it parses");
    assert_eq!(file["name"], schedules::NAME);
    assert_eq!(file.get("fields"), None);
    assert_eq!(file.get("requires"), None);
    assert_eq!(file["events"][0]["kind"], schedules::RUN);
    assert_eq!(schedules::DEFAULT_MIN_MS, 60_000);
    assert_eq!(schedules::DEFAULT_MAX_MS, 30 * 24 * HOUR);
    assert_eq!(SchedulesKind::named("cron"), Some(SchedulesKind::Cron));
    assert_eq!(SchedulesKind::Every.name(), "every");
}

/// The checks Schedules writes by hand are the declaration's configSchema
/// and its operations' paramsSchemas, keyword for keyword, and its event's
/// payloadSchema is what every fire's payload is held to.
#[test]
fn the_config_and_parameter_checks_are_the_declarations_schemas() {
    let file: Value = serde_json::from_str(schedules::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let bound = json!({"minimum": 1000, "type": "integer"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "endOn": {
                    "additionalProperties": false,
                    "properties": {"enters": {"items": text, "minItems": 1, "type": "array", "uniqueItems": true}},
                    "required": ["enters"],
                    "type": "object"
                },
                "kinds": {"items": {"enum": ["after", "every", "cron"]}, "minItems": 1, "type": "array", "uniqueItems": true},
                "max": {"maximum": 100, "minimum": 1, "type": "integer"},
                "maxMs": bound,
                "minMs": bound,
                "outcomes": {
                    "items": {"maxLength": 64, "pattern": "^[a-z][a-z0-9_]*$", "type": "string"},
                    "maxItems": 32, "minItems": 1, "type": "array", "uniqueItems": true
                },
                "permission": text,
                "retry": {
                    "additionalProperties": false,
                    "properties": {"afterMs": bound, "outcome": text},
                    "required": ["outcome", "afterMs"],
                    "type": "object"
                },
                "status": text,
                "timeZone": text
            },
            "required": ["max", "kinds", "outcomes"],
            "type": "object"
        })
    );
    let operation = |name: &str| {
        file["operations"]
            .as_array()
            .expect("operations")
            .iter()
            .find(|operation| operation["name"] == name)
            .map(|operation| undescribed(&operation["paramsSchema"]))
            .expect("the operation")
    };
    let named = json!({"additionalProperties": false, "properties": {"name": text}, "required": ["name"], "type": "object"});
    for name in ["pauseSchedule", "resumeSchedule", "cancelSchedule"] {
        assert_eq!(operation(name), named, "{name}");
    }
    assert_eq!(
        operation("setSchedule"),
        json!({
            "additionalProperties": false,
            "properties": {
                "after": {"minimum": 1, "type": "integer"},
                "cron": text,
                "every": {"minimum": 1, "type": "integer"},
                "name": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$", "type": "string"},
                "timeZone": text
            },
            "required": ["name"],
            "type": "object"
        })
    );
    assert_eq!(
        operation("reportRun"),
        json!({
            "additionalProperties": false,
            "properties": {
                "attempt": {"minimum": 1, "type": "integer"},
                "name": text,
                "outcome": text,
                "run": {"minimum": 1, "type": "integer"}
            },
            "required": ["name", "run", "outcome"],
            "type": "object"
        })
    );
    assert_eq!(
        operation("listSchedules"),
        json!({"additionalProperties": false, "properties": {"name": text}, "type": "object"})
    );
    // The config's shape, in the schema loader's words.
    let shaped = |config: Value| shape(&core(), &problem(config));
    assert_eq!(
        shaped(json!({"x": 1})),
        [
            "/config: unknown key \"x\"",
            "/config: must have required property 'kinds'",
            "/config: must have required property 'max'",
            "/config: must have required property 'outcomes'"
        ]
    );
    assert_eq!(
        shaped(json!({
            "max": 101, "minMs": 999, "maxMs": 1.5, "kinds": ["weekly", "after", "after"],
            "outcomes": ["Told", 3, "a".repeat(65)], "timeZone": "", "retry": {"outcome": "", "x": 1},
            "endOn": {"enters": []}, "status": 5, "permission": ""
        })),
        [
            "/config/endOn/enters: must NOT have fewer than 1 items",
            "/config/kinds: must NOT have duplicate items (items ## 2 and 1 are identical)",
            "/config/kinds/0: must be one of \"after\", \"every\", \"cron\"",
            "/config/max: must be <= 100",
            "/config/maxMs: must be >= 1000",
            "/config/maxMs: must be integer",
            "/config/minMs: must be >= 1000",
            "/config/outcomes/0: must match pattern \"^[a-z][a-z0-9_]*$\"",
            "/config/outcomes/1: must be string",
            "/config/outcomes/2: must NOT have more than 64 characters",
            "/config/permission: must NOT have fewer than 1 characters",
            "/config/retry: unknown key \"x\"",
            "/config/retry: must have required property 'afterMs'",
            "/config/retry/outcome: must NOT have fewer than 1 characters",
            "/config/status: must be string",
            "/config/timeZone: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(json!({"max": 1, "kinds": [], "outcomes": ["told", "told"]})),
        [
            "/config/kinds: must NOT have fewer than 1 items",
            "/config/outcomes: must NOT have duplicate items (items ## 1 and 0 are identical)"
        ]
    );
    let many: Vec<String> = (0..33).map(|n| format!("o{n}")).collect();
    assert_eq!(
        shaped(json!({"max": 1, "kinds": ["every"], "outcomes": many})),
        ["/config/outcomes: must NOT have more than 32 items"]
    );
    // The parameters' shapes, in the behavior registry's words.
    let mut host = hosting(wakes());
    assert_eq!(
        params_refusal(call(&mut host, T0, "setSchedule", json!({"foo": 1}))),
        [
            "must NOT have additional properties: foo",
            "must have required property 'name'"
        ]
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "a".repeat(65), "after": 0, "every": 1.5, "cron": "", "timeZone": 3})
        )),
        [
            "/after: must be >= 1",
            "/cron: must NOT have fewer than 1 characters",
            "/every: must be integer",
            "/name: must NOT have more than 64 characters",
            "/timeZone: must be string"
        ]
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "2nd", "after": 300_000})
        )),
        ["/name: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\""]
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "", "run": 0})
        )),
        [
            "must have required property 'outcome'",
            "/name: must NOT have fewer than 1 characters",
            "/run: must be >= 1"
        ]
    );
    assert_eq!(
        params_refusal(call(&mut host, T0, "pauseSchedule", json!({}))),
        ["must have required property 'name'"]
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "listSchedules",
            json!({"name": 1, "x": 1})
        )),
        [
            "must NOT have additional properties: x",
            "/name: must be string"
        ]
    );
}

/// Each rule of the config, in order, the first refused.
#[test]
fn the_config_rules_come_in_order() {
    let ruled = |config: Value| rule(&core(), &problem(config), "Schedules");
    let base = |members: Value| {
        let mut config = json!({"max": 1, "kinds": ["every"], "outcomes": ["told"]});
        for (key, value) in members.as_object().expect("an object") {
            config[key] = value.clone();
        }
        config
    };
    assert!(define(&core(), &problem(base(json!({})))).is_ok());
    assert!(define(&core(), &problem(wakes())).is_ok());
    assert_eq!(
        ruled(base(json!({"minMs": 600_000, "maxMs": 300_000}))),
        "minMs (600000) is more than maxMs (300000): no delay or interval lies between them"
    );
    // The defaults are the bounds a minMs or a maxMs alone is held to.
    assert_eq!(
        ruled(base(json!({"minMs": 3_000_000_000_i64}))),
        "minMs (3000000000) is more than maxMs (2592000000): no delay or interval lies between them"
    );
    assert_eq!(
        ruled(base(json!({"kinds": ["cron"], "timeZone": "Mars/Olympus"}))),
        "timeZone \"Mars/Olympus\" is not a time zone of the IANA database the core bundles"
    );
    // An unknown zone is named before a zone with no cron kind.
    assert_eq!(
        ruled(base(json!({"timeZone": "Mars/Olympus"}))),
        "timeZone \"Mars/Olympus\" is not a time zone of the IANA database the core bundles"
    );
    assert_eq!(
        ruled(base(json!({"timeZone": "UTC"}))),
        "timeZone names the zone a cron line is read in, and kinds leaves out cron"
    );
    assert_eq!(
        ruled(base(
            json!({"outcomes": ["told", "missed"], "retry": {"outcome": "lost", "afterMs": 300_000}})
        )),
        "retry.outcome \"lost\" is not one of outcomes (told, missed)"
    );
    assert_eq!(
        ruled(base(json!({"retry": {"outcome": "told", "afterMs": 1000}}))),
        "retry.afterMs (1000) is outside minMs..maxMs (60000..2592000000): a retry is held to the bounds a schedule is"
    );
    assert_eq!(
        ruled(base(json!({"status": "Workflow"}))),
        "status names the Workflow endOn reads, and the config gives no endOn"
    );
    assert_eq!(
        ruled(base(json!({"endOn": {"enters": ["closed"]}}))),
        "endOn.enters names \"closed\", which is not a state of the type's Workflow (open, working, done, dropped)"
    );
    assert_eq!(
        ruled(base(
            json!({"endOn": {"enters": ["done"]}, "status": "phase"})
        )),
        "status \"phase\" is not a Workflow of the type (its Workflows: Workflow)"
    );
    let lone = TypeSpec::new("Problem").with(UseSpec::on_type(
        "Schedules",
        base(json!({"endOn": {"enters": ["done"]}})),
    ));
    assert_eq!(
        rule(&core(), &lone, "Schedules"),
        "it reads the status of Workflow, which the type does not list"
    );
}

// ---- settle: parameters, permission, row, unchanged, vetoes ----

#[test]
fn a_set_names_one_kind_the_config_takes_within_its_bounds() {
    let mut host = hosting(wakes());
    let bad =
        |host: &mut Host, params: Value| params_refusal(call(host, T0, "setSchedule", params));
    assert_eq!(
        bad(&mut host, json!({"name": "x"})),
        ["names none of after, every and cron: a schedule is one of them"]
    );
    assert_eq!(
        bad(
            &mut host,
            json!({"name": "x", "after": 300_000, "every": 600_000, "cron": "@daily"})
        ),
        ["names after, every and cron: a schedule is one of after, every and cron"]
    );
    assert_eq!(
        bad(
            &mut host,
            json!({"name": "x", "after": 300_000, "timeZone": "UTC"})
        ),
        ["/timeZone: names the zone a cron line is read in, and an after schedule reads none"]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "every": 60_000})),
        ["/every: 60000 is outside minMs..maxMs (120000..2592000000): an interval lies between them"]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "after": 2_592_000_001_i64})),
        ["/after: 2592000001 is outside minMs..maxMs (120000..2592000000): a delay lies between them"]
    );
    // A bad line and an unknown zone are reported together.
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "0 0 L * *", "timeZone": "Mars/Olympus"})),
        [
            "/cron: the day-of-month field \"L\" holds the name L: only the month and day-of-week fields take names",
            "/timeZone: \"Mars/Olympus\" is not a time zone of the IANA database the core bundles"
        ]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "*/1 * * * *"})),
        ["/cron: its runs come 60000 ms apart at their closest, under minMs (120000): a schedule's runs are at least minMs apart"]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "0 0 30 2 *"})),
        ["/cron: it has no run within ten years"]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "0 12 29 2 *"})),
        ["/cron: its next run, 2028-02-29T17:00:00.000Z, is more than maxMs (2592000000 ms) away: a schedule's next run is within maxMs when it is set"]
    );
    // A kind the config leaves out.
    let mut host = hosting(json!({"max": 1, "kinds": ["every"], "outcomes": ["told"]}));
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "@daily"})),
        ["/cron: Problem's Schedules takes no cron schedule: its kinds are every"]
    );
    // Nothing was written.
    assert_eq!(version(&host), 1);
    // A cron line's closest runs read its day fields: with minMs two days,
    // a weekly line is set and a daily one, or one whose Monday times come
    // 22 hours apart, is refused.
    let mut host = hosting(json!({
        "max": 2, "minMs": 2 * 24 * HOUR, "maxMs": 30 * 24 * HOUR, "kinds": ["cron"], "outcomes": ["told"]
    }));
    assert_eq!(
        set(&mut host, T0, json!({"name": "weekly", "cron": "@weekly"}))["state"],
        "active"
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "@daily"})),
        ["/cron: its runs come 86400000 ms apart at their closest, under minMs (172800000): a schedule's runs are at least minMs apart"]
    );
    assert_eq!(
        bad(&mut host, json!({"name": "x", "cron": "0 1,23 * * MON"})),
        ["/cron: its runs come 79200000 ms apart at their closest, under minMs (172800000): a schedule's runs are at least minMs apart"]
    );
}

#[test]
fn the_parameters_come_before_the_permission_and_the_permission_before_the_row() {
    let mut host = hosting(wakes());
    for name in ["a", "b", "c"] {
        set(&mut host, T0, json!({"name": name, "after": 300_000}));
    }
    // A guest's bad parameters are bad parameters.
    assert_eq!(
        params_refusal(host.invoke_at(
            &guest(),
            T0,
            "p1",
            "Schedules",
            "setSchedule",
            json!({"name": "x", "after": 1000})
        )),
        ["/after: 1000 is outside minMs..maxMs (120000..2592000000): a delay lies between them"]
    );
    // Its good ones are forbidden, before the row's limit, and before an
    // unknown name.
    let forbidden = |host: &mut Host, operation: &str, params: Value| {
        refused(host.invoke_at(&guest(), T0, "p1", "Schedules", operation, params))
    };
    for (operation, params, verb) in [
        (
            "setSchedule",
            json!({"name": "x", "after": 300_000}),
            "set a schedule of",
        ),
        (
            "pauseSchedule",
            json!({"name": "ghost"}),
            "pause a schedule of",
        ),
        (
            "resumeSchedule",
            json!({"name": "ghost"}),
            "resume a schedule of",
        ),
        (
            "cancelSchedule",
            json!({"name": "ghost"}),
            "cancel a schedule of",
        ),
        (
            "reportRun",
            json!({"name": "ghost", "run": 1, "outcome": "told"}),
            "report a run of a schedule of",
        ),
        ("listSchedules", json!({}), "list the schedules of"),
    ] {
        assert_eq!(
            forbidden(&mut host, operation, params),
            json!({"class": "EngineError", "code": "forbidden", "message": format!("guest may not {verb} Problem p1: it needs permission problems.schedule")}),
            "{operation}"
        );
    }
    // A caller with no subject is named so.
    assert_eq!(
        refused(host.invoke_at(&caller("", &[]), T0, "p1", "Schedules", "listSchedules", json!({})))["message"],
        "a caller with no subject may not list the schedules of Problem p1: it needs permission problems.schedule"
    );
    // A report's outcome comes before the permission too.
    assert_eq!(
        params_refusal(host.invoke_at(&guest(), T0, "p1", "Schedules", "reportRun", json!({"name": "a", "run": 1, "outcome": "lost"}))),
        ["/outcome: \"lost\" is not an outcome of Problem's Schedules (told, resumed, held, missed)"]
    );
    // The row's limit, then a bad parameter before it again.
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "x", "after": 300_000})
        )),
        (
            "schedule_limit".to_owned(),
            json!({"max": 3, "live": ["a", "b", "c"]})
        )
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "x", "after": 1000})
        )),
        ["/after: 1000 is outside minMs..maxMs (120000..2592000000): a delay lies between them"]
    );
    assert_eq!(
        refused(call(&mut host, T0, "pauseSchedule", json!({"name": "ghost"})))["message"],
        "behavior Schedules vetoes pauseSchedule of Problem p1: it has no schedule named \"ghost\" (its schedules: a, b, c)"
    );
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "ghost", "run": 1, "outcome": "told"})
        )),
        (
            "no_schedule".to_owned(),
            json!({"name": "ghost", "schedules": ["a", "b", "c"]})
        )
    );
    // A config with no permission lets any caller through.
    let mut open = hosting(json!({"max": 1, "kinds": ["every"], "outcomes": ["told"]}));
    assert_eq!(
        result(open.invoke_at(
            &guest(),
            T0,
            "p1",
            "Schedules",
            "listSchedules",
            json!({"name": "x"})
        )),
        json!({"items": []})
    );
    assert_eq!(
        refused(open.invoke_at(&guest(), T0, "p1", "Schedules", "cancelSchedule", json!({"name": "x"})))["message"],
        "behavior Schedules vetoes cancelSchedule of Problem p1: it has no schedule named \"x\" (its schedules: none)"
    );
    // Every refusal left the row's version where it was.
    assert_eq!(version(&host), 4);
}

// ---- set ----

#[test]
fn a_set_of_each_kind_arms_its_first_run_and_answers_its_view() {
    let mut host = hosting(wakes());
    assert_eq!(
        set(&mut host, T0, json!({"name": "nudge", "after": 300_000})),
        json!({"name": "nudge", "kind": "after", "spec": "300000", "state": "active", "setAt": T0, "nextAt": T0 + 300_000, "runs": 0})
    );
    assert_eq!(armed(&host, "nudge"), Some(T0 + 300_000));
    assert_eq!(
        set(&mut host, T0, json!({"name": "hourly", "every": HOUR})),
        json!({"name": "hourly", "kind": "every", "spec": "3600000", "state": "active", "setAt": T0, "nextAt": T0 + HOUR, "runs": 0})
    );
    // A line read in the config's zone: Monday 2026-03-02 at 09:00 EST.
    assert_eq!(
        set(
            &mut host,
            T0,
            json!({"name": "morning", "cron": "0 9 * * MON-FRI"})
        ),
        json!({"name": "morning", "kind": "cron", "spec": "0 9 * * MON-FRI", "timeZone": "America/New_York", "state": "active", "setAt": T0, "nextAt": 1_772_460_000_000_i64, "runs": 0})
    );
    assert_eq!(armed(&host, "morning"), Some(1_772_460_000_000));
    // And in its own: 09:00 GMT.
    let mut host = hosting(wakes());
    assert_eq!(
        set(
            &mut host,
            T0,
            json!({"name": "morning", "cron": "0 9 * * MON-FRI", "timeZone": "europe/london"})
        )["timeZone"],
        "Europe/London"
    );
    assert_eq!(armed(&host, "morning"), Some(1_772_442_000_000));
    // A config with no zone reads a line in UTC.
    let mut host = hosting(json!({"max": 2, "kinds": ["cron"], "outcomes": ["told"]}));
    assert_eq!(
        set(&mut host, T0, json!({"name": "daily", "cron": "@DAILY"})),
        json!({"name": "daily", "kind": "cron", "spec": "@daily", "timeZone": "UTC", "state": "active", "setAt": T0, "nextAt": T0 + 24 * HOUR, "runs": 0})
    );
}

#[test]
fn a_repeat_answers_unchanged_and_another_spec_replaces_the_live_schedule() {
    let mut host = hosting(wakes());
    let morning = set(
        &mut host,
        T0,
        json!({"name": "morning", "cron": "0 9 * * MON-FRI"}),
    );
    let at = version(&host);
    // The same line normalized, in the same zone however it is spelled.
    let mut repeat = morning.clone();
    repeat["unchanged"] = json!(true);
    assert_eq!(
        set(
            &mut host,
            T0 + 1000,
            json!({"name": "morning", "cron": "  0  9 * *   MON-FRI", "timeZone": "america/new_york"})
        ),
        repeat
    );
    assert_eq!(version(&host), at);
    // A paused one stays paused.
    result(call(
        &mut host,
        T0,
        "pauseSchedule",
        json!({"name": "morning"}),
    ));
    assert_eq!(
        set(
            &mut host,
            T0,
            json!({"name": "morning", "cron": "0 9 * * MON-FRI"})
        )["state"],
        "paused"
    );
    // Another zone, spec or kind replaces it, its runs counting on.
    let replaced = set(
        &mut host,
        T0 + MINUTE,
        json!({"name": "morning", "cron": "0 9 * * MON-FRI", "timeZone": "UTC"}),
    );
    assert_eq!(replaced["replaced"], true);
    assert_eq!(replaced["state"], "active");
    assert_eq!(replaced["setAt"], T0 + MINUTE);
    assert_eq!(armed(&host, "morning"), Some(T0 + 24 * HOUR + 9 * HOUR));
    let replaced = set(
        &mut host,
        T0 + 2 * MINUTE,
        json!({"name": "morning", "every": HOUR}),
    );
    assert_eq!(
        replaced,
        json!({"name": "morning", "kind": "every", "spec": "3600000", "state": "active", "setAt": T0 + 2 * MINUTE, "nextAt": T0 + 2 * MINUTE + HOUR, "runs": 0, "replaced": true})
    );
    assert_eq!(armed(&host, "morning"), Some(T0 + 2 * MINUTE + HOUR));
    // A finished name set again is a new schedule, not a replace.
    result(call(
        &mut host,
        T0,
        "cancelSchedule",
        json!({"name": "morning"}),
    ));
    let again = set(
        &mut host,
        T0 + 3 * MINUTE,
        json!({"name": "morning", "after": 300_000}),
    );
    assert_eq!(again.get("replaced"), None);
    assert_eq!(again["runs"], 0);
    assert_eq!(again.get("endedBy"), None);
}

// ---- pause, resume, cancel ----

#[test]
fn pause_keeps_next_at_resume_arms_it_and_cancel_finishes_it() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    let paused = result(call(
        &mut host,
        T0 + 10 * MINUTE,
        "pauseSchedule",
        json!({"name": "hourly"}),
    ));
    assert_eq!(paused["state"], "paused");
    assert_eq!(paused["nextAt"], T0 + HOUR);
    assert_eq!(armed(&host, "hourly"), None);
    let at = version(&host);
    assert_eq!(
        result(call(
            &mut host,
            T0,
            "pauseSchedule",
            json!({"name": "hourly"})
        ))["unchanged"],
        true
    );
    assert_eq!(version(&host), at);
    // Nothing fires while it is paused.
    assert!(fire(&mut host, T0 + 2 * HOUR).is_empty());
    // Resumed past its nextAt, it falls due at once and fires once, the
    // next from the time it fired.
    let resumed = result(call(
        &mut host,
        T0 + 3 * HOUR,
        "resumeSchedule",
        json!({"name": "hourly"}),
    ));
    assert_eq!(resumed["state"], "active");
    assert_eq!(armed(&host, "hourly"), Some(T0 + HOUR));
    assert_eq!(
        result(call(
            &mut host,
            T0,
            "resumeSchedule",
            json!({"name": "hourly"})
        ))["unchanged"],
        true
    );
    assert_eq!(
        payloads(&mut host, T0 + 3 * HOUR),
        [
            json!({"name": "hourly", "run": 1, "attempt": 1, "kind": "every", "spec": "3600000", "setAt": T0, "dueAt": T0 + HOUR, "firedAt": T0 + 3 * HOUR, "nextAt": T0 + 4 * HOUR})
        ]
    );
    assert!(fire(&mut host, T0 + 3 * HOUR).is_empty());
    let cancelled = result(call(
        &mut host,
        T0 + 3 * HOUR + MINUTE,
        "cancelSchedule",
        json!({"name": "hourly"}),
    ));
    assert_eq!(
        cancelled,
        json!({"name": "hourly", "kind": "every", "spec": "3600000", "state": "finished", "setAt": T0, "runs": 1, "attempt": 1, "lastAt": T0 + 3 * HOUR, "finishedAt": T0 + 3 * HOUR + MINUTE, "endedBy": "cancel"})
    );
    assert_eq!(armed(&host, "hourly"), None);
    let at = version(&host);
    assert_eq!(
        result(call(
            &mut host,
            T0,
            "cancelSchedule",
            json!({"name": "hourly"})
        ))["unchanged"],
        true
    );
    assert_eq!(version(&host), at);
    for operation in ["pauseSchedule", "resumeSchedule"] {
        assert_eq!(
            veto_of(call(&mut host, T0, operation, json!({"name": "hourly"}))),
            ("schedule_finished".to_owned(), json!({"name": "hourly"})),
            "{operation}"
        );
    }
}

// ---- the fire ----

#[test]
fn a_due_schedule_fires_once_and_arms_the_next_from_its_fire() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "nudge", "after": 300_000}));
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    // An after runs once and finishes.
    assert_eq!(
        payloads(&mut host, T0 + 300_000),
        [
            json!({"name": "nudge", "run": 1, "attempt": 1, "kind": "after", "spec": "300000", "setAt": T0, "dueAt": T0 + 300_000, "firedAt": T0 + 300_000})
        ]
    );
    assert_eq!(
        one(&mut host, "nudge"),
        json!({"name": "nudge", "kind": "after", "spec": "300000", "state": "finished", "setAt": T0, "runs": 1, "attempt": 1, "lastAt": T0 + 300_000, "finishedAt": T0 + 300_000, "endedBy": "run"})
    );
    assert_eq!(armed(&host, "nudge"), None);
    // An every overdue by many runs fires once, at the next point of its
    // grid after the fire.
    let late = T0 + 5 * HOUR + 30 * MINUTE;
    assert_eq!(
        payloads(&mut host, late),
        [
            json!({"name": "hourly", "run": 1, "attempt": 1, "kind": "every", "spec": "3600000", "setAt": T0, "dueAt": T0 + HOUR, "firedAt": late, "nextAt": T0 + 6 * HOUR})
        ]
    );
    assert!(fire(&mut host, late).is_empty());
    assert_eq!(armed(&host, "hourly"), Some(T0 + 6 * HOUR));
    // A cron line, its zone in its payload.
    result(call(
        &mut host,
        late,
        "cancelSchedule",
        json!({"name": "hourly"}),
    ));
    set(
        &mut host,
        T0,
        json!({"name": "morning", "cron": "0 9 * * MON-FRI"}),
    );
    let events = payloads(&mut host, 1_772_460_000_000);
    assert_eq!(
        events,
        [
            json!({"name": "morning", "run": 1, "attempt": 1, "kind": "cron", "spec": "0 9 * * MON-FRI", "timeZone": "America/New_York", "setAt": T0, "dueAt": 1_772_460_000_000_i64, "firedAt": 1_772_460_000_000_i64, "nextAt": 1_772_460_000_000_i64 + 24 * HOUR})
        ]
    );
}

#[test]
fn a_stale_arming_fires_nothing_and_a_due_moved_later_is_armed_again() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    let deadline = |host: &mut Host, now: i64, name: &str| {
        changed(host.run(
            &runner(),
            now,
            Action::Deadline {
                key: "p1".to_owned(),
                use_id: "Schedules".to_owned(),
                name: name.to_owned(),
                at: T0 + HOUR,
                payload: json!({}),
            },
        ))
    };
    // Paused, finished and missing schedules: nothing.
    result(call(
        &mut host,
        T0,
        "pauseSchedule",
        json!({"name": "hourly"}),
    ));
    let quiet = deadline(&mut host, T0 + HOUR, "hourly");
    assert!(quiet.events.is_empty() && quiet.sub_rows.is_empty() && quiet.deadlines.is_empty());
    result(call(
        &mut host,
        T0,
        "cancelSchedule",
        json!({"name": "hourly"}),
    ));
    assert!(deadline(&mut host, T0 + HOUR, "hourly").events.is_empty());
    assert!(deadline(&mut host, T0 + HOUR, "ghost").sub_rows.is_empty());
    // A raw write moved the next run later: the old arming arms it there.
    set(&mut host, T0, json!({"name": "daily", "every": HOUR}));
    raw(&mut host, "daily", json!({"nextAt": T0 + 3 * HOUR}));
    let moved = deadline(&mut host, T0 + HOUR, "daily");
    assert!(moved.events.is_empty());
    assert_eq!(armed(&host, "daily"), Some(T0 + 3 * HOUR));
    result(call(
        &mut host,
        T0,
        "cancelSchedule",
        json!({"name": "daily"}),
    ));
    // A line a raw write left with no run finishes after its run.
    set(&mut host, T0, json!({"name": "late", "cron": "0 9 * * *"}));
    raw(&mut host, "late", json!({"spec": "0 0 30 2 *"}));
    let events = payloads(&mut host, T0 + 14 * HOUR);
    assert_eq!(events.len(), 1);
    assert_eq!(events[0].get("nextAt"), None);
    assert_eq!(one(&mut host, "late")["endedBy"], "run");
}

// ---- reportRun ----

#[test]
fn a_report_records_the_latest_runs_outcome_and_the_retry_tries_it_again() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "hourly", "run": 1, "outcome": "told"})
        )),
        (
            "no_run".to_owned(),
            json!({"name": "hourly", "run": 1, "runs": 0})
        )
    );
    payloads(&mut host, T0 + HOUR);
    let report = |host: &mut Host, now: i64, run: i64, outcome: &str| {
        result(call(
            host,
            now,
            "reportRun",
            json!({"name": "hourly", "run": run, "outcome": outcome}),
        ))
    };
    assert_eq!(
        report(&mut host, T0 + HOUR + 1000, 1, "told"),
        json!({"name": "hourly", "run": 1, "outcome": "told"})
    );
    let at = version(&host);
    assert_eq!(
        report(&mut host, T0 + HOUR + 1000, 1, "told"),
        json!({"name": "hourly", "run": 1, "outcome": "told", "unchanged": true})
    );
    assert_eq!(version(&host), at);
    // missed tries the run again five minutes on, its next attempt.
    assert_eq!(
        report(&mut host, T0 + HOUR + 2000, 1, "missed"),
        json!({"name": "hourly", "run": 1, "outcome": "missed", "retryAt": T0 + HOUR + 2000 + 300_000})
    );
    assert_eq!(armed(&host, "hourly"), Some(T0 + HOUR + 302_000));
    assert_eq!(
        report(&mut host, T0 + HOUR + 3000, 1, "missed"),
        json!({"name": "hourly", "run": 1, "outcome": "missed", "retryAt": T0 + HOUR + 302_000, "unchanged": true})
    );
    assert_eq!(
        payloads(&mut host, T0 + HOUR + 302_000),
        [
            json!({"name": "hourly", "run": 1, "attempt": 2, "kind": "every", "spec": "3600000", "setAt": T0, "dueAt": T0 + HOUR + 302_000, "firedAt": T0 + HOUR + 302_000, "nextAt": T0 + 2 * HOUR})
        ]
    );
    assert_eq!(armed(&host, "hourly"), Some(T0 + 2 * HOUR));
    // Another outcome cancels a try armed.
    report(&mut host, T0 + HOUR + 400_000, 1, "missed");
    assert_eq!(armed(&host, "hourly"), Some(T0 + HOUR + 700_000));
    assert_eq!(
        report(&mut host, T0 + HOUR + 500_000, 1, "held"),
        json!({"name": "hourly", "run": 1, "outcome": "held"})
    );
    assert_eq!(armed(&host, "hourly"), Some(T0 + 2 * HOUR));
    assert_eq!(one(&mut host, "hourly")["lastOutcome"], "held");
    assert_eq!(one(&mut host, "hourly")["attempt"], 2);
    // A retry the next regular run would overtake is not armed.
    payloads(&mut host, T0 + 2 * HOUR);
    assert_eq!(
        report(&mut host, T0 + 3 * HOUR - 100_000, 2, "missed"),
        json!({"name": "hourly", "run": 2, "outcome": "missed"})
    );
    assert_eq!(armed(&host, "hourly"), Some(T0 + 3 * HOUR));
    // A report of an older run answers unchanged; of a run not had, no_run.
    assert_eq!(
        report(&mut host, T0 + 3 * HOUR - 100_000, 1, "told"),
        json!({"name": "hourly", "run": 1, "outcome": "told", "unchanged": true})
    );
    assert_eq!(
        refused(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "hourly", "run": 3, "outcome": "told"})
        ))["details"]["reason"],
        "its schedule \"hourly\" has had 2 runs, and no run 3 to report"
    );
    // A paused schedule keeps a retry for its resume.
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    payloads(&mut host, T0 + HOUR);
    result(call(
        &mut host,
        T0 + HOUR,
        "pauseSchedule",
        json!({"name": "hourly"}),
    ));
    assert_eq!(
        report(&mut host, T0 + HOUR, 1, "missed")["retryAt"],
        T0 + HOUR + 300_000
    );
    assert_eq!(armed(&host, "hourly"), None);
    result(call(
        &mut host,
        T0 + HOUR + MINUTE,
        "resumeSchedule",
        json!({"name": "hourly"}),
    ));
    assert_eq!(armed(&host, "hourly"), Some(T0 + HOUR + 300_000));
}

#[test]
fn a_names_runs_count_on_and_a_late_report_of_another_schedule_or_attempt_changes_nothing() {
    let mut host = hosting(wakes());
    let report =
        |host: &mut Host, now: i64, params: Value| result(call(host, now, "reportRun", params));
    set(&mut host, T0, json!({"name": "nudge", "every": HOUR}));
    payloads(&mut host, T0 + HOUR);
    // A replace counts the name's runs on: its first fire is run 2, and the
    // replaced schedule's late report of run 1 changes nothing, before the
    // new one fires and after.
    let replaced = set(
        &mut host,
        T0 + HOUR,
        json!({"name": "nudge", "after": 600_000}),
    );
    assert_eq!(replaced["runs"], 1);
    assert_eq!(replaced.get("lastAt"), None);
    let at = version(&host);
    assert_eq!(
        report(
            &mut host,
            T0 + HOUR + 1000,
            json!({"name": "nudge", "run": 1, "outcome": "missed"})
        ),
        json!({"name": "nudge", "run": 1, "outcome": "missed", "unchanged": true})
    );
    assert_eq!(version(&host), at);
    assert_eq!(armed(&host, "nudge"), Some(T0 + HOUR + 600_000));
    assert_eq!(
        payloads(&mut host, T0 + HOUR + 600_000),
        [
            json!({"name": "nudge", "run": 2, "attempt": 1, "kind": "after", "spec": "600000", "setAt": T0 + HOUR, "dueAt": T0 + HOUR + 600_000, "firedAt": T0 + HOUR + 600_000})
        ]
    );
    assert_eq!(
        report(
            &mut host,
            T0 + HOUR + 601_000,
            json!({"name": "nudge", "run": 1, "outcome": "missed"})
        )["unchanged"],
        true
    );
    // A finished name set again counts on too, with a later setAt.
    set(
        &mut host,
        T0 + 2 * HOUR,
        json!({"name": "nudge", "after": 600_000}),
    );
    assert_eq!(
        payloads(&mut host, T0 + 2 * HOUR + 600_000),
        [
            json!({"name": "nudge", "run": 3, "attempt": 1, "kind": "after", "spec": "600000", "setAt": T0 + 2 * HOUR, "dueAt": T0 + 2 * HOUR + 600_000, "firedAt": T0 + 2 * HOUR + 600_000})
        ]
    );
    // missed revives it for attempt 2; a late report of attempt 1 that names
    // it, after attempt 2 fired, arms no third.
    assert_eq!(
        report(
            &mut host,
            T0 + 2 * HOUR + 601_000,
            json!({"name": "nudge", "run": 3, "attempt": 1, "outcome": "missed"})
        )["retryAt"],
        T0 + 2 * HOUR + 901_000
    );
    assert_eq!(
        payloads(&mut host, T0 + 2 * HOUR + 901_000)[0]["attempt"],
        2
    );
    let at = version(&host);
    assert_eq!(
        report(
            &mut host,
            T0 + 2 * HOUR + 902_000,
            json!({"name": "nudge", "run": 3, "attempt": 1, "outcome": "missed"})
        ),
        json!({"name": "nudge", "run": 3, "outcome": "missed", "unchanged": true})
    );
    assert_eq!(version(&host), at);
    assert_eq!(armed(&host, "nudge"), None);
    // The latest attempt's report lands.
    assert_eq!(
        report(
            &mut host,
            T0 + 2 * HOUR + 903_000,
            json!({"name": "nudge", "run": 3, "attempt": 2, "outcome": "told"})
        ),
        json!({"name": "nudge", "run": 3, "outcome": "told"})
    );
    assert_eq!(one(&mut host, "nudge")["lastOutcome"], "told");
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "nudge", "run": 3, "attempt": 0, "outcome": "told"})
        )),
        ["/attempt: must be >= 1"]
    );
}

#[test]
fn a_retry_the_next_run_is_due_beside_gives_way_to_it() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    payloads(&mut host, T0 + HOUR);
    result(call(
        &mut host,
        T0 + 2 * HOUR - 400_000,
        "reportRun",
        json!({"name": "hourly", "run": 1, "outcome": "missed"}),
    ));
    assert_eq!(armed(&host, "hourly"), Some(T0 + 2 * HOUR - 100_000));
    // The worker comes late, after the next run's time too: the regular
    // run fires, not the try.
    assert_eq!(
        payloads(&mut host, T0 + 2 * HOUR + MINUTE),
        [
            json!({"name": "hourly", "run": 2, "attempt": 1, "kind": "every", "spec": "3600000", "setAt": T0, "dueAt": T0 + 2 * HOUR, "firedAt": T0 + 2 * HOUR + MINUTE, "nextAt": T0 + 3 * HOUR})
        ]
    );
    assert_eq!(one(&mut host, "hourly").get("retryAt"), None);
}

#[test]
fn an_afters_missed_run_revives_it_for_the_try_unless_the_row_is_at_max() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "nudge", "after": 300_000}));
    payloads(&mut host, T0 + 300_000);
    assert_eq!(
        result(call(
            &mut host,
            T0 + 310_000,
            "reportRun",
            json!({"name": "nudge", "run": 1, "outcome": "missed"})
        )),
        json!({"name": "nudge", "run": 1, "outcome": "missed", "retryAt": T0 + 610_000})
    );
    assert_eq!(one(&mut host, "nudge")["state"], "active");
    assert_eq!(
        payloads(&mut host, T0 + 610_000),
        [
            json!({"name": "nudge", "run": 1, "attempt": 2, "kind": "after", "spec": "300000", "setAt": T0, "dueAt": T0 + 610_000, "firedAt": T0 + 610_000})
        ]
    );
    assert_eq!(
        one(&mut host, "nudge"),
        json!({"name": "nudge", "kind": "after", "spec": "300000", "state": "finished", "setAt": T0, "runs": 1, "attempt": 2, "lastAt": T0 + 610_000, "finishedAt": T0 + 610_000, "endedBy": "run"})
    );
    // Another outcome of a revived one finishes it again.
    result(call(
        &mut host,
        T0 + 620_000,
        "reportRun",
        json!({"name": "nudge", "run": 1, "outcome": "missed"}),
    ));
    assert_eq!(one(&mut host, "nudge")["state"], "active");
    result(call(
        &mut host,
        T0 + 630_000,
        "reportRun",
        json!({"name": "nudge", "run": 1, "outcome": "told"}),
    ));
    assert_eq!(one(&mut host, "nudge")["endedBy"], "run");
    assert_eq!(armed(&host, "nudge"), None);
    // At max, nothing revives.
    for name in ["a", "b", "c"] {
        set(
            &mut host,
            T0 + 700_000,
            json!({"name": name, "every": HOUR}),
        );
    }
    assert_eq!(
        result(call(
            &mut host,
            T0 + 700_000,
            "reportRun",
            json!({"name": "nudge", "run": 1, "outcome": "missed"})
        )),
        json!({"name": "nudge", "run": 1, "outcome": "missed"})
    );
    assert_eq!(one(&mut host, "nudge")["state"], "finished");
    assert_eq!(armed(&host, "nudge"), None);
    // A cancelled one revives for nothing.
    result(call(&mut host, T0, "cancelSchedule", json!({"name": "a"})));
    payloads(&mut host, T0 + HOUR + 700_000);
    result(call(
        &mut host,
        T0 + HOUR + 700_000,
        "cancelSchedule",
        json!({"name": "b"}),
    ));
    result(call(
        &mut host,
        T0 + HOUR + 710_000,
        "reportRun",
        json!({"name": "b", "run": 1, "outcome": "missed"}),
    ));
    assert_eq!(one(&mut host, "b")["state"], "finished");
}

// ---- endOn ----

#[test]
fn end_on_finishes_the_live_schedules_through_a_transition_a_heard_write_and_a_fire() {
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    set(&mut host, T0, json!({"name": "nudge", "after": 600_000}));
    result(call(
        &mut host,
        T0,
        "pauseSchedule",
        json!({"name": "nudge"}),
    ));
    let transition = |host: &mut Host, now: i64, to: &str| {
        result(host.invoke_at(
            &agent(),
            now,
            "p1",
            "Workflow",
            "transition",
            json!({"to": to}),
        ))
    };
    transition(&mut host, T0 + MINUTE, "working");
    assert_eq!(one(&mut host, "hourly")["state"], "active");
    transition(&mut host, T0 + 2 * MINUTE, "done");
    let ended = |name: &str, kind: &str, spec: &str| json!({"name": name, "kind": kind, "spec": spec, "state": "finished", "setAt": T0, "runs": 0, "finishedAt": T0 + 2 * MINUTE, "endedBy": "endOn"});
    assert_eq!(
        list(&mut host),
        json!([
            ended("hourly", "every", "3600000"),
            ended("nudge", "after", "600000")
        ])
    );
    assert_eq!(armed(&host, "hourly"), None);
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "again", "after": 300_000})
        )),
        ("schedules_ended".to_owned(), json!({"status": "done"}))
    );
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "resumeSchedule",
            json!({"name": "nudge"})
        )),
        ("schedules_ended".to_owned(), json!({"status": "done"}))
    );
    // A heard write changes nothing more.
    assert!(changed(heard(&mut host, T0 + 3 * MINUTE))
        .sub_rows
        .is_empty());

    // A raw write of the status, heard through the change queue.
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    host.memory
        .rows
        .get_mut("p1")
        .expect("the row")
        .owned
        .get_mut("Workflow")
        .expect("its Workflow")
        .insert("status".into(), json!("dropped"));
    changed(heard(&mut host, T0 + 5 * MINUTE));
    assert_eq!(one(&mut host, "hourly")["endedBy"], "endOn");
    assert_eq!(one(&mut host, "hourly")["finishedAt"], T0 + 5 * MINUTE);
    assert_eq!(armed(&host, "hourly"), None);

    // A raw write heard by no queue yet: the fire finishes them, and emits
    // nothing.
    let mut host = hosting(wakes());
    set(&mut host, T0, json!({"name": "hourly", "every": HOUR}));
    host.memory
        .rows
        .get_mut("p1")
        .expect("the row")
        .owned
        .get_mut("Workflow")
        .expect("its Workflow")
        .insert("status".into(), json!("done"));
    assert!(fire(&mut host, T0 + HOUR).is_empty());
    assert_eq!(one(&mut host, "hourly")["endedBy"], "endOn");
}

// two_flows is a problem with two Workflow fields, status and phase, and
// Schedules beside them.
fn two_flows(config: Value) -> TypeSpec {
    TypeSpec::new("Problem")
        .with(UseSpec::on_field("status", "Workflow", flow()))
        .with(UseSpec::on_field(
            "phase",
            "Workflow",
            json!({"states": ["draft", "live", "retired"], "transitions": [{"from": "draft", "to": "live"}, {"from": "live", "to": "retired"}]}),
        ))
        .with(UseSpec::on_type("Schedules", config))
}

#[test]
fn status_names_which_of_two_workflows_end_on_reads() {
    assert_eq!(
        rule(
            &core(),
            &two_flows(json!({"max": 2, "kinds": ["every"], "outcomes": ["told"], "endOn": {"enters": ["done"]}})),
            "Schedules"
        ),
        "it reads the status of Workflow, and the type has several (status, phase): status names the field whose Workflow it reads"
    );
    let spec = two_flows(
        json!({"max": 2, "kinds": ["every"], "outcomes": ["told"], "endOn": {"enters": ["retired"]}, "status": "phase"}),
    );
    let mut host = Host::new(core(), spec);
    changed(host.create(&agent(), "p1", json!({})));
    let now = support::START;
    set(&mut host, now, json!({"name": "hourly", "every": HOUR}));
    let move_to = |host: &mut Host, field: &str, to: &str| {
        result(host.invoke_at(&agent(), now, "p1", field, "transition", json!({"to": to})))
    };
    // The status Workflow's done ends nothing; the phase's retired does.
    move_to(&mut host, "status", "working");
    move_to(&mut host, "status", "done");
    assert_eq!(one(&mut host, "hourly")["state"], "active");
    move_to(&mut host, "phase", "live");
    move_to(&mut host, "phase", "retired");
    assert_eq!(one(&mut host, "hourly")["endedBy"], "endOn");
}

// ---- max and pruning ----

#[test]
fn max_counts_the_live_schedules_and_the_finished_beyond_it_are_pruned() {
    let mut host = hosting(json!({"max": 2, "kinds": ["after", "every"], "outcomes": ["told"]}));
    set(&mut host, T0, json!({"name": "a", "every": 600_000}));
    set(&mut host, T0, json!({"name": "b", "every": 600_000}));
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "c", "every": 600_000})
        )),
        (
            "schedule_limit".to_owned(),
            json!({"max": 2, "live": ["a", "b"]})
        )
    );
    // A paused one counts.
    result(call(&mut host, T0, "pauseSchedule", json!({"name": "b"})));
    assert_eq!(
        refused(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "c", "every": 600_000})
        ))["details"]["reason"],
        "it holds 2 live schedules (a, b), and its max is 2: cancel one to set another"
    );
    // A cancelled one frees its place.
    result(call(
        &mut host,
        T0 + 1,
        "cancelSchedule",
        json!({"name": "a"}),
    ));
    set(&mut host, T0 + 2, json!({"name": "c", "every": 600_000}));
    result(call(
        &mut host,
        T0 + 3,
        "cancelSchedule",
        json!({"name": "b"}),
    ));
    // Three finished, max two: the one finished longest ago goes.
    result(call(
        &mut host,
        T0 + 4,
        "cancelSchedule",
        json!({"name": "c"}),
    ));
    let names: Vec<Value> = list(&mut host)
        .as_array()
        .expect("items")
        .iter()
        .map(|item| item["name"].clone())
        .collect();
    assert_eq!(names, [json!("b"), json!("c")]);
    // A report of a pruned schedule's run finds no schedule.
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "reportRun",
            json!({"name": "a", "run": 1, "outcome": "told"})
        ))
        .0,
        "no_schedule"
    );
    // Ties by name: under a max lowered to one, two afters that finish at
    // one fire time leave the second by name.
    let mut host = hosting(json!({"max": 2, "kinds": ["after"], "outcomes": ["told"]}));
    set(&mut host, T0, json!({"name": "e", "after": 600_000}));
    set(&mut host, T0, json!({"name": "d", "after": 600_000}));
    host.spec = problem(json!({"max": 1, "kinds": ["after"], "outcomes": ["told"]}));
    assert_eq!(payloads(&mut host, T0 + 600_000).len(), 2);
    let names: Vec<Value> = list(&mut host)
        .as_array()
        .expect("items")
        .iter()
        .map(|item| item["name"].clone())
        .collect();
    assert_eq!(names, [json!("e")]);
}

#[test]
fn a_new_config_holds_new_sets_alone() {
    let mut host = hosting(json!({"max": 2, "kinds": ["after", "every"], "outcomes": ["told"]}));
    set(&mut host, T0, json!({"name": "d", "after": 600_000}));
    set(&mut host, T0, json!({"name": "e", "every": 600_000}));
    host.spec =
        problem(json!({"max": 1, "kinds": ["every"], "minMs": 300_000, "outcomes": ["told"]}));
    assert_eq!(
        veto_of(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "f", "every": 600_000})
        )),
        (
            "schedule_limit".to_owned(),
            json!({"max": 1, "live": ["d", "e"]})
        )
    );
    assert_eq!(
        params_refusal(call(
            &mut host,
            T0,
            "setSchedule",
            json!({"name": "d", "after": 600_000})
        )),
        ["/after: Problem's Schedules takes no after schedule: its kinds are every"]
    );
    // The schedule of a dropped kind runs.
    assert_eq!(payloads(&mut host, T0 + 600_000).len(), 2);
    assert_eq!(one(&mut host, "d")["endedBy"], "run");
}
