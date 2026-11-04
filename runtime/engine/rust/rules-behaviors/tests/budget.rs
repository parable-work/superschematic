//! Budget on the pure core (D103), through the core's public API over a
//! source in memory that keeps rows of several types, their sub-table rows
//! and their specs: the engine's rules (a reservation held to the limit, a
//! meter with no row reading zeros, usage never refused, setLimit and
//! limitField held to limitPermission and to what is committed, reserveField
//! and reserve, settle, a delete settling up the chain), its scopes as to-one
//! relations (a chain of two, exceeds_reservation, scope_reserved on an
//! update of the relation, scope_moved), escalate tolerating a veto and a
//! forbidden move; and the upgrades (D104): each reset's period at and
//! around its boundary (a Sunday 23:59 and a Monday 00:00, the last of
//! February and the first of March, a 31-day month), everyMs, a rolling
//! window across its boundary with its buckets deleted as they leave and
//! checkReserve's until, observe and its lapse, setLimit refused for a meter
//! whose limit an own field holds; the define refusals in their order, the
//! config change, and the config and the operations' parameters held to the
//! declaration file's schemas.

mod support;
mod tables;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Behavior, Caller, ConfigError, ConfigTarget, Declaration, FieldSpec, GuardRequest,
    Hook, Outcome, TableDecl, TypeSpec, UseSpec, Veto, View,
};
use superschematic_rules_behaviors::{budget, core, Budget, BudgetReset, INTEGER_SEGMENTS};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

// At 2026-10-09T09:00:00.000Z, a Friday.
const T0: i64 = 1_791_536_400_000;
const MINUTE: i64 = 60_000;
const HOUR: i64 = 3_600_000;
const DAY: i64 = 86_400_000;

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn caller(subject: &str, permissions: &[&str]) -> Caller {
    Caller::new(subject, permissions.iter().copied())
}

fn with(config: Value, more: Value) -> Value {
    let mut config = config;
    config
        .as_object_mut()
        .expect("an object")
        .extend(more.as_object().expect("an object").clone());
    config
}

// A ledger keeps its meters, with a name, a cap and an estimate.
fn ledger(config: Value) -> TypeSpec {
    TypeSpec::new("Ledger")
        .with(UseSpec::on_type("Budget", config))
        .with_field(FieldSpec::new("name", "string"))
        .with_field(FieldSpec::new("cap", "Generic.Int64"))
        .with_field(FieldSpec::new("estimate", "Generic.Int64"))
}

fn ledger_host(config: Value) -> Host {
    let mut host = Host::new(core(), [ledger(config)]);
    changed(host.create("Ledger", &alice(), T0, "l1", json!({"name": "Main"})));
    host
}

#[allow(clippy::too_many_arguments)]
fn op(
    host: &mut Host,
    type_name: &str,
    key: &str,
    who: &Caller,
    now: i64,
    name: &str,
    params: Value,
) -> Outcome {
    host.invoke(type_name, who, now, key, "Budget", name, params)
}

fn on_ledger(host: &mut Host, now: i64, name: &str, params: Value) -> Outcome {
    op(host, "Ledger", "l1", &alice(), now, name, params)
}

fn meters_at(host: &Host, type_name: &str, key: &str, now: i64) -> Value {
    host.read_fields_at(type_name, key, "Budget", now)["meters"].clone()
}

fn meter(used: i64, reserved: i64, limit: Option<i64>) -> Value {
    json!({
        "used": used,
        "reserved": reserved,
        "limit": limit,
        "remaining": limit.map(|limit| limit - used - reserved),
    })
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    let fields = |names: &[&str]| {
        names
            .iter()
            .map(|name| (*name).to_owned())
            .collect::<Vec<_>>()
    };
    assert_eq!(
        Budget.declaration(),
        Declaration::new("Budget")
            .computed("meters")
            .sub_table(TableDecl::new(
                "meters",
                "meter",
                fields(&[
                    "meter",
                    "used",
                    "reserved",
                    "limitValue",
                    "periodStart",
                    "reservation",
                    "token",
                    "directed",
                    "scopeType",
                    "scopeKey",
                    "observed",
                    "observedLimit",
                    "observedAt",
                    "observedUntil"
                ])
            ))
            .sub_table(TableDecl::new(
                "holds",
                "hold",
                fields(&["hold", "meter", "innerType", "innerKey", "amount"])
            ))
            .sub_table(TableDecl::new(
                "buckets",
                "bucket",
                fields(&["bucket", "meter", "startsAt", "used"])
            ))
            .operation("reserve", true)
            .operation("checkReserve", false)
            .operation("recordUsage", true)
            .operation("settle", true)
            .operation("setLimit", true)
            .operation("reserveFor", true)
            .operation("settleFor", true)
            .operation("recordUsageFor", true)
            .operation("observe", true)
            .veto("over_limit")
            .veto("not_leased")
            .veto("scope_moved")
            .veto("scope_reserved")
            .veto("below_committed")
            .veto("exceeds_reservation")
            .veto("not_configured")
    );
    assert_eq!(INTEGER_SEGMENTS, ["int", "int32", "int64", "integer"]);
}

/// The checks Budget writes by hand are the declaration's configSchema,
/// keyword for keyword, so a keyword the file gains fails this test until
/// the checks take it; each keyword's refusal comes out in the schema
/// loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(budget::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let name = json!({"pattern": "^[a-z][A-Za-z0-9]{0,63}$", "type": "string"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "escalate": {
                    "additionalProperties": false,
                    "properties": {
                        "from": {"items": text, "minItems": 1, "type": "array", "uniqueItems": true},
                        "transition": text
                    },
                    "required": ["transition", "from"],
                    "type": "object"
                },
                "limitPermission": text,
                "meters": {
                    "additionalProperties": {
                        "additionalProperties": false,
                        "properties": {
                            "limit": {"minimum": 1, "type": "integer"},
                            "limitField": text,
                            "reserve": {"minimum": 1, "type": "integer"},
                            "reserveField": text,
                            "reset": {"oneOf": [
                                {"enum": ["hourly", "daily", "weekly", "monthly"]},
                                {
                                    "additionalProperties": false,
                                    "properties": {
                                        "everyMs": {"minimum": 60000, "multipleOf": 60000, "type": "integer"}
                                    },
                                    "required": ["everyMs"],
                                    "type": "object"
                                }
                            ]},
                            "scope": name,
                            "window": {"enum": ["rolling"]}
                        },
                        "type": "object"
                    },
                    "minProperties": 1,
                    "propertyNames": {"pattern": "^[a-z][A-Za-z0-9]{0,63}$"},
                    "type": "object"
                },
                "observePermission": text,
                "onExceeded": {
                    "additionalProperties": false,
                    "properties": {
                        "direct": {"pattern": "^[A-Za-z][A-Za-z0-9_.:-]{0,63}$", "type": "string"}
                    },
                    "required": ["direct"],
                    "type": "object"
                },
                "status": text
            },
            "required": ["meters"],
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &ledger(config));
    assert_eq!(
        shaped(json!({})),
        ["/config: must have required property 'meters'"]
    );
    assert_eq!(
        shaped(
            json!({"meters": {}, "limitPermission": "", "observePermission": 1, "status": "", "x": 1})
        ),
        [
            "/config: unknown key \"x\"",
            "/config/limitPermission: must NOT have fewer than 1 characters",
            "/config/meters: must NOT have fewer than 1 properties",
            "/config/observePermission: must be string",
            "/config/status: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(
            json!({"meters": {"Calls": {"limit": 0, "reserve": 1.5, "limitField": "", "reserveField": 2, "scope": "Up", "x": 1}}})
        ),
        [
            "/config/meters: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\"",
            "/config/meters: property name must be valid",
            "/config/meters/Calls: unknown key \"x\"",
            "/config/meters/Calls/limit: must be >= 1",
            "/config/meters/Calls/limitField: must NOT have fewer than 1 characters",
            "/config/meters/Calls/reserve: must be integer",
            "/config/meters/Calls/reserveField: must be string",
            "/config/meters/Calls/scope: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\""
        ]
    );
    let reset = |value: Value| shaped(json!({"meters": {"calls": {"limit": 10, "reset": value}}}));
    assert_eq!(
        reset(json!("yearly")),
        [
            "/config/meters/calls/reset: must be one of \"hourly\", \"daily\", \"weekly\", \"monthly\"",
            "/config/meters/calls/reset: must match exactly one schema in oneOf",
            "/config/meters/calls/reset: must be object"
        ]
    );
    assert_eq!(
        reset(json!({"everyMs": 90_000})),
        [
            "/config/meters/calls/reset: must be one of \"hourly\", \"daily\", \"weekly\", \"monthly\"",
            "/config/meters/calls/reset: must match exactly one schema in oneOf",
            "/config/meters/calls/reset/everyMs: must be multiple of 60000"
        ]
    );
    assert_eq!(
        reset(json!({"everyMs": 0, "x": 1})),
        [
            "/config/meters/calls/reset: unknown key \"x\"",
            "/config/meters/calls/reset: must be one of \"hourly\", \"daily\", \"weekly\", \"monthly\"",
            "/config/meters/calls/reset: must match exactly one schema in oneOf",
            "/config/meters/calls/reset/everyMs: must be >= 60000"
        ]
    );
    assert_eq!(
        reset(json!({})),
        [
            "/config/meters/calls/reset: must be one of \"hourly\", \"daily\", \"weekly\", \"monthly\"",
            "/config/meters/calls/reset: must match exactly one schema in oneOf",
            "/config/meters/calls/reset: must have required property 'everyMs'"
        ]
    );
    assert_eq!(
        shaped(json!({"meters": {"calls": {"limit": 10, "reset": "hourly", "window": "fixed"}}})),
        ["/config/meters/calls/window: must be one of \"rolling\""]
    );
    assert_eq!(
        shaped(
            json!({"meters": {"calls": {}}, "onExceeded": {"direct": "2x", "y": 1}, "escalate": {"transition": "out"}})
        ),
        [
            "/config/escalate: must have required property 'from'",
            "/config/onExceeded: unknown key \"y\"",
            "/config/onExceeded/direct: must match pattern \"^[A-Za-z][A-Za-z0-9_.:-]{0,63}$\""
        ]
    );
    assert_eq!(
        shaped(
            json!({"meters": [], "onExceeded": {}, "escalate": {"transition": "out", "from": []}})
        ),
        [
            "/config/escalate/from: must NOT have fewer than 1 items",
            "/config/meters: must be object",
            "/config/onExceeded: must have required property 'direct'"
        ]
    );
}

/// Each operation's closed paramsSchema, in the behavior registry's words.
#[test]
fn the_operations_parameters_are_held_to_their_params_schemas() {
    let file: Value = serde_json::from_str(budget::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let amount = json!({"minimum": 1, "type": "integer"});
    let count = json!({"minimum": 0, "type": "integer"});
    let closed = |properties: Value, required: &[&str]| {
        let mut schema =
            json!({"additionalProperties": false, "properties": properties, "type": "object"});
        if !required.is_empty() {
            schema["required"] = json!(required);
        }
        schema
    };
    let scoped = json!({"amount": amount, "id": text, "meter": text, "schema": text});
    let expected = [
        (
            "reserve",
            closed(json!({"amount": amount, "meter": text}), &[]),
        ),
        (
            "checkReserve",
            closed(json!({"amount": amount, "meter": text}), &[]),
        ),
        (
            "recordUsage",
            closed(
                json!({"amount": amount, "meter": text}),
                &["meter", "amount"],
            ),
        ),
        ("settle", closed(json!({"meter": text}), &[])),
        (
            "setLimit",
            closed(json!({"limit": count, "meter": text}), &["meter", "limit"]),
        ),
        (
            "reserveFor",
            closed(scoped.clone(), &["meter", "schema", "id", "amount"]),
        ),
        (
            "settleFor",
            closed(scoped.clone(), &["meter", "schema", "id", "amount"]),
        ),
        (
            "recordUsageFor",
            closed(
                with(scoped, json!({"released": count})),
                &["meter", "schema", "id", "amount", "released"],
            ),
        ),
        (
            "observe",
            closed(
                json!({"limit": count, "meter": text, "resetsAt": count, "used": count}),
                &["meter", "used"],
            ),
        ),
    ];
    let operations = file["operations"].as_array().expect("operations");
    assert_eq!(operations.len(), expected.len());
    for (operation, (name, schema)) in operations.iter().zip(expected) {
        assert_eq!(operation["name"], name);
        assert_eq!(undescribed(&operation["paramsSchema"]), schema, "{name}");
    }
    let mut host =
        ledger_host(json!({"meters": {"calls": {"limit": 10}}, "observePermission": "x"}));
    let message = |host: &mut Host, name: &str, params: Value| {
        refused(on_ledger(host, T0, name, params))["message"]
            .as_str()
            .unwrap()
            .to_owned()
    };
    assert_eq!(
        message(&mut host, "recordUsage", json!({"amount": 0, "meter": "", "x": 1})),
        "operation recordUsage of behavior Budget: must NOT have additional properties: x; /amount: must be >= 1; /meter: must NOT have fewer than 1 characters"
    );
    assert_eq!(
        message(&mut host, "recordUsage", json!({})),
        "operation recordUsage of behavior Budget: must have required property 'amount'; must have required property 'meter'"
    );
    assert_eq!(
        message(
            &mut host,
            "setLimit",
            json!({"meter": "calls", "limit": -1})
        ),
        "operation setLimit of behavior Budget: /limit: must be >= 0"
    );
    assert_eq!(
        message(&mut host, "observe", json!({"meter": "calls", "used": 1.5, "resetsAt": -1, "limit": "x"})),
        "operation observe of behavior Budget: /limit: must be integer; /resetsAt: must be >= 0; /used: must be integer"
    );
    assert_eq!(
        message(
            &mut host,
            "recordUsageFor",
            json!({"meter": "calls", "schema": "Ledger", "id": "l1", "amount": 1})
        ),
        "operation recordUsageFor of behavior Budget: must have required property 'released'"
    );
    assert_eq!(
        message(&mut host, "settle", json!(5)),
        "operation settle of behavior Budget: must be object"
    );
}

// ---- define ----

#[test]
fn define_holds_the_config_to_the_type_in_its_order() {
    let reason = |config: Value| rule(&core(), &ledger(config), "Budget");
    assert_eq!(
        reason(json!({"meters": {"calls": {"limitField": "missing"}}})),
        "meter calls: limitField \"missing\" is not a field of Ledger (its fields: name, cap, estimate)"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"limitField": "name"}}})),
        "meter calls: limitField \"name\" is not an integer field of Ledger"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"limitField": "cap", "reserveField": "name"}}})),
        "meter calls: reserveField \"name\" is not an integer field of Ledger"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"scope": "workspace"}}})),
        "meter calls: scope workspace is not a to-one relation of Ledger (its relations: none)"
    );
    let related = TypeSpec::new("Ledger")
        .with(UseSpec::on_type(
            "Budget",
            json!({"meters": {"calls": {"scope": "team"}}}),
        ))
        .with_field(FieldSpec::relation(
            "workspaceId",
            "Identity.UUID",
            "workspace",
            "Workspace",
        ));
    assert_eq!(
        rule(&core(), &related, "Budget"),
        "meter calls: scope team is not a to-one relation of Ledger (its relations: workspace)"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"limit": 10, "window": "rolling"}}})),
        "meter calls: window rolling counts the last period, and the meter names no reset to give it one"
    );
    // The meters in name order, however the config orders them: the first
    // one's problem.
    assert_eq!(
        reason(json!({"meters": {"tokens": {"limit": 1, "window": "rolling"}, "calls": {"limitField": "missing"}}})),
        "meter calls: limitField \"missing\" is not a field of Ledger (its fields: name, cap, estimate)"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"limit": 10}}, "onExceeded": {"direct": "slow"}})),
        "onExceeded sends a directive through Lease's direct, which the type does not list"
    );
    assert_eq!(
        reason(
            json!({"meters": {"calls": {"limit": 10}}, "escalate": {"transition": "out", "from": ["open"]}})
        ),
        "escalate moves the status through Workflow, which the type does not list"
    );
    assert_eq!(
        reason(json!({"meters": {"calls": {"limit": 10}}, "status": "status"})),
        "status \"status\" is not a Workflow of the type (its Workflows: none)"
    );
    let flowing = |config: Value| {
        ledger(config).with(UseSpec::on_field(
            "status",
            "Workflow",
            json!({"states": ["open", "out", "closed"], "transitions": [{"from": "open", "to": "out"}, {"from": "out", "to": "open"}]}),
        ))
    };
    assert_eq!(
        rule(
            &core(),
            &flowing(
                json!({"meters": {"calls": {"limit": 10}}, "escalate": {"transition": "out", "from": ["closed"]}})
            ),
            "Budget"
        ),
        "escalate: no transition of the type's Workflow leads from \"closed\" to \"out\""
    );
    assert_eq!(
        rule(
            &core(),
            &flowing(
                json!({"meters": {"calls": {"limit": 10}}, "escalate": {"transition": "gone", "from": ["open"]}})
            ),
            "Budget"
        ),
        "escalate.transition \"gone\" is not a state of the type's Workflow (open, out, closed)"
    );
    define(
        &core(),
        &flowing(json!({"meters": {"calls": {"limit": 10, "reset": {"everyMs": 600_000}, "window": "rolling", "limitField": "cap", "reserveField": "estimate"}}, "escalate": {"transition": "out", "from": ["open"]}, "status": "status", "observePermission": "x"})),
    )
    .expect("a full config");
}

#[test]
fn a_meter_cannot_be_removed_while_the_type_has_rows() {
    let config = json!({"meters": {"calls": {"limit": 10}, "tokens": {"limit": 5}}});
    let publish = |rows: bool, next: Value| {
        let spec = ledger(config.clone());
        let mut host = Host::new(core(), [spec.clone()]);
        if rows {
            changed(host.create("Ledger", &alice(), T0, "l1", json!({"name": "Main"})));
        }
        host.tables.specs.insert("Ledger".into(), ledger(next));
        host.run("Ledger", &alice(), T0, Action::Publish { from: Some(spec) })
    };
    let gone = json!({"meters": {"calls": {"limit": 20, "reset": "hourly"}}});
    let error = refused(publish(true, gone.clone()));
    assert!(
        error["message"].as_str().unwrap().contains(
            "meter tokens is gone, and its instances and the scopes they draw on may hold it"
        ),
        "{error}"
    );
    changed(publish(false, gone));
    changed(publish(
        true,
        json!({"meters": {"calls": {"limit": 20, "reset": "hourly", "window": "rolling"}, "tokens": {"limit": 1}}}),
    ));
}

// ---- periods (D104) ----

#[test]
fn each_reset_starts_its_period_at_its_utc_boundary() {
    // Sunday 2026-10-11T23:59Z and Monday 2026-10-12T00:00Z.
    let monday = 1_791_763_200_000;
    let weekly = BudgetReset::Weekly;
    assert_eq!(weekly.period_start(monday - MINUTE), monday - 7 * DAY);
    assert_eq!(weekly.next_start(monday - MINUTE), monday);
    assert_eq!(weekly.period_start(monday), monday);
    assert_eq!(weekly.next_start(monday), monday + 7 * DAY);
    // The last of February 2027 and the first of March, and 31-day
    // October.
    let march = 1_803_859_200_000;
    let monthly = BudgetReset::Monthly;
    assert_eq!(monthly.period_start(march - 1), march - 28 * DAY);
    assert_eq!(monthly.next_start(march - 1), march);
    assert_eq!(monthly.period_start(march), march);
    let october = 1_790_812_800_000;
    assert_eq!(monthly.period_start(T0), october);
    assert_eq!(monthly.next_start(T0), october + 31 * DAY);
    let daily = BudgetReset::Daily;
    assert_eq!(daily.period_start(T0), T0 - 9 * HOUR);
    assert_eq!(daily.next_start(T0), T0 + 15 * HOUR);
    let hourly = BudgetReset::Hourly;
    assert_eq!(hourly.period_start(T0 + HOUR - 1), T0);
    assert_eq!(hourly.next_start(T0 + HOUR - 1), T0 + HOUR);
    let every = BudgetReset::Every(7 * MINUTE);
    assert_eq!(every.period_start(T0), T0 - T0 % (7 * MINUTE));
    assert_eq!(every.next_start(T0), T0 - T0 % (7 * MINUTE) + 7 * MINUTE);
    // A rolling window's buckets: a sixtieth of the period, a month being
    // thirty days.
    assert_eq!(hourly.bucket_ms(), MINUTE);
    assert_eq!(daily.bucket_ms(), 24 * MINUTE);
    assert_eq!(weekly.bucket_ms(), 168 * MINUTE);
    assert_eq!(monthly.bucket_ms(), 12 * HOUR);
    assert_eq!(monthly.window_ms(), 30 * DAY);
    assert_eq!(every.bucket_ms(), 7_000);
}

#[test]
fn a_fixed_reset_reads_zero_once_its_period_passes_and_its_next_write_starts_the_new_one() {
    let mut host = ledger_host(json!({"meters": {
        "hour": {"limit": 10, "reset": "hourly"},
        "week": {"limit": 10, "reset": "weekly"},
        "tenMinutes": {"limit": 10, "reset": {"everyMs": 600_000}},
        "ever": {"limit": 10}
    }}));
    for name in ["hour", "week", "tenMinutes", "ever"] {
        assert_eq!(
            result(on_ledger(
                &mut host,
                T0,
                "recordUsage",
                json!({"meter": name, "amount": 4})
            ))["used"],
            4
        );
    }
    let read = |host: &Host, now: i64| meters_at(host, "Ledger", "l1", now);
    assert_eq!(
        read(&host, T0 + 599_999),
        json!({"hour": meter(4, 0, Some(10)), "week": meter(4, 0, Some(10)), "tenMinutes": meter(4, 0, Some(10)), "ever": meter(4, 0, Some(10))})
    );
    assert_eq!(
        read(&host, T0 + 600_000)["tenMinutes"],
        meter(0, 0, Some(10))
    );
    assert_eq!(read(&host, T0 + HOUR)["hour"], meter(0, 0, Some(10)));
    let monday = 1_791_763_200_000;
    assert_eq!(read(&host, monday - 1)["week"], meter(4, 0, Some(10)));
    assert_eq!(read(&host, monday)["week"], meter(0, 0, Some(10)));
    assert_eq!(
        read(&host, monday + 300 * DAY)["ever"],
        meter(4, 0, Some(10))
    );
    // A read wrote nothing; the next write starts the new period.
    let rows = host.sub_rows("Ledger", "l1", "Budget", "meters");
    assert_eq!(rows[1]["key"], "hour");
    assert_eq!(rows[1]["values"]["periodStart"], T0);
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + HOUR + 1,
            "recordUsage",
            json!({"meter": "hour", "amount": 1})
        ))["used"],
        1
    );
    let rows = host.sub_rows("Ledger", "l1", "Budget", "meters");
    assert_eq!(
        rows[1],
        json!({"key": "hour", "values": {"used": 1, "reserved": 0, "reservation": 0, "periodStart": T0 + HOUR}})
    );
    // A reservation is kept across the period.
    changed(on_ledger(
        &mut host,
        T0 + HOUR + 2,
        "reserve",
        json!({"meter": "hour", "amount": 9}),
    ));
    assert_eq!(read(&host, T0 + 2 * HOUR)["hour"], meter(0, 9, Some(10)));
    let (code, details) = veto_of(on_ledger(
        &mut host,
        T0 + 2 * HOUR,
        "reserve",
        json!({"meter": "hour", "amount": 2}),
    ));
    assert_eq!(code, "over_limit");
    assert_eq!(
        details,
        json!({"meter": "hour", "amount": 2, "remaining": 1, "limit": 10, "scope": {"schema": "Ledger", "id": "l1"}})
    );
}

#[test]
fn a_rolling_meter_counts_the_last_periods_buckets_and_deletes_the_ones_that_left() {
    let mut host = ledger_host(
        json!({"meters": {"calls": {"limit": 10, "reset": "hourly", "window": "rolling"}}}),
    );
    changed(on_ledger(
        &mut host,
        T0 + 30 * MINUTE,
        "recordUsage",
        json!({"meter": "calls", "amount": 6}),
    ));
    changed(on_ledger(
        &mut host,
        T0 + 50 * MINUTE,
        "recordUsage",
        json!({"meter": "calls", "amount": 3}),
    ));
    assert_eq!(
        host.sub_rows("Ledger", "l1", "Budget", "buckets"),
        json!([
            {"key": format!("calls:{:015}", T0 + 30 * MINUTE), "values": {"meter": "calls", "startsAt": T0 + 30 * MINUTE, "used": 6}},
            {"key": format!("calls:{:015}", T0 + 50 * MINUTE), "values": {"meter": "calls", "startsAt": T0 + 50 * MINUTE, "used": 3}}
        ])
    );
    // Across the hour a fixed reset would start again at, it still counts.
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + HOUR)["calls"],
        meter(9, 0, Some(10))
    );
    let (code, _) = veto_of(on_ledger(
        &mut host,
        T0 + HOUR,
        "reserve",
        json!({"meter": "calls", "amount": 2}),
    ));
    assert_eq!(code, "over_limit");
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + HOUR,
            "checkReserve",
            json!({"meter": "calls", "amount": 5})
        )),
        json!({"fits": false, "until": T0 + 91 * MINUTE, "scopes": []})
    );
    // More than the limit never fits, whenever.
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + HOUR,
            "checkReserve",
            json!({"meter": "calls", "amount": 11})
        )),
        json!({"fits": false, "until": null, "scopes": []})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + 91 * MINUTE - 1)["calls"],
        meter(9, 0, Some(10))
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + 91 * MINUTE)["calls"],
        meter(3, 0, Some(10))
    );
    // A bucket leaves a period after its end, and the next write deletes
    // it: at the period's length from its start it still counts.
    changed(on_ledger(
        &mut host,
        T0 + 90 * MINUTE,
        "recordUsage",
        json!({"meter": "calls", "amount": 1}),
    ));
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + 90 * MINUTE)["calls"],
        meter(10, 0, Some(10))
    );
    changed(on_ledger(
        &mut host,
        T0 + 91 * MINUTE,
        "recordUsage",
        json!({"meter": "calls", "amount": 1}),
    ));
    assert_eq!(
        host.sub_rows("Ledger", "l1", "Budget", "buckets"),
        json!([
            {"key": format!("calls:{:015}", T0 + 50 * MINUTE), "values": {"meter": "calls", "startsAt": T0 + 50 * MINUTE, "used": 3}},
            {"key": format!("calls:{:015}", T0 + 90 * MINUTE), "values": {"meter": "calls", "startsAt": T0 + 90 * MINUTE, "used": 1}},
            {"key": format!("calls:{:015}", T0 + 91 * MINUTE), "values": {"meter": "calls", "startsAt": T0 + 91 * MINUTE, "used": 1}}
        ])
    );
    // A second write in one bucket adds to it; the meter's row keeps no
    // used and no period.
    changed(on_ledger(
        &mut host,
        T0 + 91 * MINUTE + 59_999,
        "recordUsage",
        json!({"meter": "calls", "amount": 1}),
    ));
    assert_eq!(
        host.sub_rows("Ledger", "l1", "Budget", "buckets")[2]["values"]["used"],
        2
    );
    assert_eq!(
        host.sub_rows("Ledger", "l1", "Budget", "meters"),
        json!([{"key": "calls", "values": {"used": 0, "reserved": 0, "reservation": 0}}])
    );
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + 92 * MINUTE,
            "reserve",
            json!({"meter": "calls", "amount": 4})
        )),
        json!({"reserved": {"calls": 4}})
    );
}

/// A rolling window counts every bucket that overlaps the last period, so
/// the usage of no stretch of a period's length passes the limit: the cap
/// used half a minute into an hour holds the next one at its start, and
/// frees it once its bucket is a period behind.
#[test]
fn a_rolling_window_lets_no_period_pass_its_limit() {
    let mut host = ledger_host(
        json!({"meters": {"calls": {"limit": 40, "reset": "hourly", "window": "rolling"}}}),
    );
    changed(on_ledger(
        &mut host,
        T0 + 30_000,
        "recordUsage",
        json!({"meter": "calls", "amount": 40}),
    ));
    for at in [T0 + HOUR, T0 + HOUR + 30_000] {
        assert_eq!(
            meters_at(&host, "Ledger", "l1", at)["calls"],
            meter(40, 0, Some(40))
        );
        let (code, _) = veto_of(on_ledger(
            &mut host,
            at,
            "reserve",
            json!({"meter": "calls", "amount": 1}),
        ));
        assert_eq!(code, "over_limit");
    }
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + HOUR,
            "checkReserve",
            json!({"meter": "calls", "amount": 40})
        )),
        json!({"fits": false, "until": T0 + HOUR + MINUTE, "scopes": []})
    );
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + HOUR + MINUTE,
            "reserve",
            json!({"meter": "calls", "amount": 40})
        )),
        json!({"reserved": {"calls": 40}})
    );
}

#[test]
fn check_reserve_waits_for_the_next_period_of_a_fixed_reset() {
    let mut host = ledger_host(
        json!({"meters": {"calls": {"limit": 10, "reset": "daily"}, "ever": {"limit": 10}}}),
    );
    changed(on_ledger(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 8}),
    ));
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0,
            "checkReserve",
            json!({"meter": "calls", "amount": 5})
        )),
        json!({"fits": false, "until": T0 + 15 * HOUR, "scopes": []})
    );
    changed(on_ledger(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "ever", "amount": 8}),
    ));
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0,
            "checkReserve",
            json!({"meter": "ever", "amount": 5})
        )),
        json!({"fits": false, "until": null, "scopes": []})
    );
    // Every meter's claim amount at once: none configured, nothing planned.
    assert_eq!(
        result(on_ledger(&mut host, T0, "checkReserve", json!({}))),
        json!({"fits": true, "until": null, "scopes": []})
    );
}

// ---- observe (D104) ----

fn keys() -> Value {
    json!({"meters": {"tokens": {"limit": 1000, "reset": "daily"}, "calls": {"limit": 50}}, "observePermission": "keys.observe"})
}

#[test]
fn observe_replaces_what_the_meter_counts_until_resets_at_and_needs_its_permission() {
    let mut host = ledger_host(keys());
    let observer = caller("obs", &["keys.observe"]);
    changed(on_ledger(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "tokens", "amount": 100}),
    ));
    let error = refused(on_ledger(
        &mut host,
        T0,
        "observe",
        json!({"meter": "tokens", "used": 900}),
    ));
    assert_eq!(error["code"], "forbidden");
    assert_eq!(
        error["message"],
        "alice may not observe the usage of Ledger l1: it needs permission keys.observe"
    );
    let until = T0 + 30 * MINUTE;
    assert_eq!(
        result(op(
            &mut host,
            "Ledger",
            "l1",
            &observer,
            T0 + MINUTE,
            "observe",
            json!({"meter": "tokens", "used": 900, "limit": 950, "resetsAt": until})
        )),
        json!({"meter": "tokens", "used": 900, "limit": 950, "resetsAt": until})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + MINUTE)["tokens"],
        meter(900, 0, Some(950))
    );
    let refusal = refused(on_ledger(
        &mut host,
        T0 + MINUTE,
        "reserve",
        json!({"meter": "tokens", "amount": 60}),
    ));
    assert_eq!(
        refusal["message"],
        "behavior Budget vetoes reserve of Ledger l1: meter tokens has 50 of its limit 950 left, not 60"
    );
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + 2 * MINUTE,
            "recordUsage",
            json!({"meter": "tokens", "amount": 20})
        )),
        json!({"meter": "tokens", "used": 920, "released": 0, "overruns": [], "directed": false})
    );
    // checkReserve waits for the observation's lapse.
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0 + 2 * MINUTE,
            "checkReserve",
            json!({"meter": "tokens", "amount": 60})
        )),
        json!({"fits": false, "until": until, "scopes": []})
    );
    // At resetsAt it lapses: the meter's own count, with what was recorded
    // since, and its own limit.
    assert_eq!(
        meters_at(&host, "Ledger", "l1", until - 1)["tokens"],
        meter(920, 0, Some(950))
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", until)["tokens"],
        meter(120, 0, Some(1000))
    );
    // Without resetsAt it holds until the meter's next period start.
    let midnight = T0 + 15 * HOUR;
    assert_eq!(
        result(op(
            &mut host,
            "Ledger",
            "l1",
            &observer,
            until,
            "observe",
            json!({"meter": "tokens", "used": 500})
        )),
        json!({"meter": "tokens", "used": 500, "limit": 1000, "resetsAt": midnight})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", midnight - 1)["tokens"],
        meter(500, 0, Some(1000))
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", midnight)["tokens"],
        meter(0, 0, Some(1000))
    );
    // A meter with no reset holds it until the next observe.
    assert_eq!(
        result(op(
            &mut host,
            "Ledger",
            "l1",
            &observer,
            T0,
            "observe",
            json!({"meter": "calls", "used": 7})
        )),
        json!({"meter": "calls", "used": 7, "limit": 50, "resetsAt": null})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0 + 900 * DAY)["calls"],
        meter(7, 0, Some(50))
    );
    let rows = host.sub_rows("Ledger", "l1", "Budget", "meters");
    assert_eq!(
        rows[0],
        json!({"key": "calls", "values": {"used": 0, "reserved": 0, "reservation": 0, "observed": 7, "observedAt": T0}})
    );
    // A meter the config does not give, and what the schema requires.
    let error = refused(op(
        &mut host,
        "Ledger",
        "l1",
        &observer,
        T0,
        "observe",
        json!({"meter": "cups", "used": 1}),
    ));
    assert_eq!(error["class"], "OperationParamsError");
    assert_eq!(
        error["message"],
        "operation observe of behavior Budget: /meter: Ledger has no meter cups (its meters: calls, tokens)"
    );
    assert_eq!(
        error["details"],
        json!({"issues": [{"path": "/meter", "message": "Ledger has no meter cups (its meters: calls, tokens)"}]})
    );
}

#[test]
fn observe_on_a_config_with_no_observe_permission_is_not_configured() {
    let mut host = ledger_host(json!({"meters": {"calls": {"limit": 10}}}));
    let error = refused(on_ledger(
        &mut host,
        T0,
        "observe",
        json!({"meter": "calls", "used": 1}),
    ));
    assert_eq!(
        error["message"],
        "behavior Budget vetoes observe of Ledger l1: its config names no observePermission, which observe needs"
    );
    assert_eq!(error["details"]["vetoCode"], "not_configured");
    assert_eq!(error["details"].get("vetoDetails"), None);
}

// ---- limits ----

#[test]
fn set_limit_needs_limit_permission_and_a_limit_field_is_set_by_an_update() {
    let limiter = caller("lim", &["budget.limit"]);
    let mut host = ledger_host(json!({"meters": {"calls": {"limit": 10}}}));
    let error = refused(op(
        &mut host,
        "Ledger",
        "l1",
        &limiter,
        T0,
        "setLimit",
        json!({"meter": "calls", "limit": 5}),
    ));
    assert_eq!(
        error["details"]["reason"],
        "its config names no limitPermission, which changing a limit needs"
    );
    let mut host = ledger_host(
        json!({"meters": {"calls": {"limit": 10}, "cpu": {"limit": 100, "limitField": "cap"}}, "limitPermission": "budget.limit"}),
    );
    let error = refused(on_ledger(
        &mut host,
        T0,
        "setLimit",
        json!({"meter": "calls", "limit": 5}),
    ));
    assert_eq!(error["code"], "forbidden");
    assert_eq!(
        error["message"],
        "alice may not change a limit of Ledger l1: it needs permission budget.limit"
    );
    changed(on_ledger(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 6}),
    ));
    let (code, details) = veto_of(op(
        &mut host,
        "Ledger",
        "l1",
        &limiter,
        T0,
        "setLimit",
        json!({"meter": "calls", "limit": 5}),
    ));
    assert_eq!(
        (code.as_str(), details),
        ("below_committed", json!({"meter": "calls", "committed": 6}))
    );
    assert_eq!(
        result(op(
            &mut host,
            "Ledger",
            "l1",
            &limiter,
            T0,
            "setLimit",
            json!({"meter": "calls", "limit": 6})
        )),
        json!({"meter": "calls", "limit": 6, "previous": 10})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["calls"],
        meter(6, 0, Some(6))
    );
    // A limitField meter's limit is the field's.
    let error = refused(op(
        &mut host,
        "Ledger",
        "l1",
        &limiter,
        T0,
        "setLimit",
        json!({"meter": "cpu", "limit": 120}),
    ));
    assert_eq!(
        error["message"],
        "operation setLimit of behavior Budget: /meter: meter cpu keeps its limit in cap, an own field of Ledger: setLimit writes no own field on a table, so update cap, which needs limitPermission (D104)"
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["cpu"],
        meter(0, 0, Some(100))
    );
    changed(host.update("Ledger", &limiter, T0, "l1", json!({"cap": 120})));
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["cpu"],
        meter(0, 0, Some(120))
    );
    let error = refused(host.update("Ledger", &alice(), T0, "l1", json!({"cap": 130})));
    assert_eq!(
        error["message"],
        "alice may not change cap, the limit of meter cpu, of Ledger l1: it needs permission budget.limit"
    );
    changed(on_ledger(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "cpu", "amount": 90}),
    ));
    let (code, details) = veto_of(host.update("Ledger", &limiter, T0, "l1", json!({"cap": 80})));
    assert_eq!(
        (code.as_str(), details),
        ("below_committed", json!({"meter": "cpu", "committed": 90}))
    );
    // Cleared, the config's limit counts.
    changed(host.update("Ledger", &limiter, T0, "l1", json!({"cap": null})));
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["cpu"],
        meter(90, 0, Some(100))
    );
    // With no limitPermission a limitField does not change.
    let mut host = ledger_host(json!({"meters": {"cpu": {"limitField": "cap"}}}));
    let (code, _) = veto_of(host.update("Ledger", &limiter, T0, "l1", json!({"cap": 5})));
    assert_eq!(code, "not_configured");
    changed(host.update("Ledger", &limiter, T0, "l1", json!({"name": "Other"})));
}

#[test]
fn a_claim_reserves_the_reserve_field_else_the_configs_reserve() {
    let mut host = ledger_host(
        json!({"meters": {"calls": {"limit": 10, "reserve": 2, "reserveField": "estimate"}, "tokens": {"limit": 5}}}),
    );
    assert_eq!(
        result(on_ledger(&mut host, T0, "reserve", json!({}))),
        json!({"reserved": {"calls": 2}})
    );
    changed(host.update("Ledger", &alice(), T0, "l1", json!({"estimate": 3})));
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0,
            "reserve",
            json!({"meter": "calls"})
        )),
        json!({"reserved": {"calls": 3}})
    );
    let error = refused(on_ledger(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "tokens"}),
    ));
    assert_eq!(
        error["message"],
        "operation reserve of behavior Budget: /amount: meter tokens has no configured reservation, so reserve takes an amount"
    );
    let error = refused(on_ledger(&mut host, T0, "reserve", json!({"amount": 1})));
    assert_eq!(
        error["message"],
        "operation reserve of behavior Budget: /amount: an amount is reserved of one meter, which meter names"
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["calls"],
        meter(0, 5, Some(10))
    );
    // Usage draws the reservation down; settle releases the rest.
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0,
            "recordUsage",
            json!({"meter": "calls", "amount": 4})
        )),
        json!({"meter": "calls", "used": 4, "released": 4, "overruns": [], "directed": false})
    );
    assert_eq!(
        result(on_ledger(&mut host, T0, "settle", json!({}))),
        json!({"released": {"calls": 1, "tokens": 0}})
    );
    assert_eq!(
        meters_at(&host, "Ledger", "l1", T0)["calls"],
        meter(4, 0, Some(10))
    );
    // Usage is never refused.
    assert_eq!(
        result(on_ledger(
            &mut host,
            T0,
            "recordUsage",
            json!({"meter": "tokens", "amount": 9})
        )),
        json!({"meter": "tokens", "used": 9, "released": 0, "overruns": [{"schema": "Ledger", "id": "l1", "used": 9, "limit": 5, "escalated": false}], "directed": false})
    );
}

// ---- scopes ----

fn org() -> TypeSpec {
    TypeSpec::new("Org")
        .with(UseSpec::on_type(
            "Budget",
            json!({"meters": {"calls": {"limit": 100}}}),
        ))
        .with_field(FieldSpec::new("name", "string"))
}

fn workspace() -> TypeSpec {
    TypeSpec::new("Workspace")
        .with(UseSpec::on_type(
            "Budget",
            json!({"meters": {"calls": {"limit": 20, "scope": "org"}}}),
        ))
        .with_field(FieldSpec::new("name", "string"))
        .with_field(FieldSpec::relation("orgId", "Identity.UUID", "org", "Org"))
}

fn account_flow() -> Value {
    json!({"states": ["open", "out"], "transitions": [{"from": "open", "to": "out", "permission": "accounts.move"}, {"from": "out", "to": "open"}]})
}

fn account(config: Value) -> TypeSpec {
    TypeSpec::new("Account")
        .with(UseSpec::on_type("Budget", config))
        .with(UseSpec::on_field("status", "Workflow", account_flow()))
        .with_field(FieldSpec::new("name", "string"))
        .with_field(FieldSpec::relation(
            "workspaceId",
            "Identity.UUID",
            "workspace",
            "Workspace",
        ))
}

fn scoped() -> Value {
    json!({"meters": {"calls": {"limit": 10, "scope": "workspace"}}})
}

// chain hosts an org o1 over workspaces w1 and w2, and account a1 in w1.
fn chain(config: Value) -> Host {
    let mut host = Host::new(core(), [org(), workspace(), account(config)]);
    changed(host.create("Org", &alice(), T0, "o1", json!({"name": "Org"})));
    for key in ["w1", "w2"] {
        changed(host.create(
            "Workspace",
            &alice(),
            T0,
            key,
            json!({"name": key, "orgId": "o1"}),
        ));
    }
    changed(host.create(
        "Account",
        &alice(),
        T0,
        "a1",
        json!({"name": "A", "workspaceId": "w1"}),
    ));
    host
}

fn on_account(host: &mut Host, now: i64, name: &str, params: Value) -> Outcome {
    op(host, "Account", "a1", &alice(), now, name, params)
}

#[test]
fn a_reservation_and_usage_reach_every_scope_up_the_chain() {
    let mut host = chain(scoped());
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "reserve",
            json!({"meter": "calls", "amount": 4})
        )),
        json!({"reserved": {"calls": 4}})
    );
    assert_eq!(
        meters_at(&host, "Account", "a1", T0)["calls"],
        meter(0, 4, Some(10))
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 4, Some(20))
    );
    assert_eq!(
        meters_at(&host, "Org", "o1", T0)["calls"],
        meter(0, 4, Some(100))
    );
    assert_eq!(
        host.sub_rows("Workspace", "w1", "Budget", "holds"),
        json!([{"key": "calls:Account:a1", "values": {"meter": "calls", "innerType": "Account", "innerKey": "a1", "amount": 4}}])
    );
    assert_eq!(
        host.sub_rows("Org", "o1", "Budget", "holds"),
        json!([{"key": "calls:Workspace:w1", "values": {"meter": "calls", "innerType": "Workspace", "innerKey": "w1", "amount": 4}}])
    );
    let account_meter = &host.sub_rows("Account", "a1", "Budget", "meters")[0]["values"];
    assert_eq!(account_meter["scopeType"], "Workspace");
    assert_eq!(account_meter["scopeKey"], "w1");
    // Usage draws the reservation down at every scope.
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "recordUsage",
            json!({"meter": "calls", "amount": 3})
        )),
        json!({"meter": "calls", "used": 3, "released": 3, "overruns": [], "directed": false})
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(3, 1, Some(20))
    );
    assert_eq!(
        meters_at(&host, "Org", "o1", T0)["calls"],
        meter(3, 1, Some(100))
    );
    // checkReserve lists the scopes it read, innermost first.
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "checkReserve",
            json!({"meter": "calls", "amount": 6})
        )),
        json!({"fits": true, "until": null, "scopes": [
            {"schema": "Workspace", "id": "w1", "hears": [
                {"path": "/behaviors/Budget/meters/calls/remaining", "crosses": 6},
                {"path": "/behaviors/Links/targets/org"}
            ]},
            {"schema": "Org", "id": "o1", "hears": [
                {"path": "/behaviors/Budget/meters/calls/remaining", "crosses": 6}
            ]}
        ]})
    );
    // A scope over its limit is reported; usage is never refused.
    let overrun = result(on_account(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 18}),
    ));
    assert_eq!(
        overrun["overruns"],
        json!([
            {"schema": "Account", "id": "a1", "used": 21, "limit": 10, "escalated": false},
            {"schema": "Workspace", "id": "w1", "used": 21, "limit": 20, "escalated": false}
        ])
    );
    // Settle releases what is left at every scope.
    changed(on_account(&mut host, T0, "settle", json!({})));
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(21, 0, Some(20))
    );
    assert_eq!(
        host.sub_rows("Workspace", "w1", "Budget", "holds"),
        json!([])
    );
}

#[test]
fn a_scope_that_cannot_fit_refuses_the_reservation_and_leaves_nothing() {
    let mut host = chain(json!({"meters": {"calls": {"limit": 50, "scope": "workspace"}}}));
    let error = refused(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 25}),
    ));
    assert_eq!(
        error["message"],
        "behavior Budget vetoes reserveFor of Workspace w1: meter calls has 20 of its limit 20 left, not 25"
    );
    assert_eq!(
        error["details"]["vetoDetails"]["scope"],
        json!({"schema": "Workspace", "id": "w1"})
    );
    assert_eq!(
        host.sub_rows("Account", "a1", "Budget", "meters"),
        json!([])
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 0, Some(20))
    );
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "checkReserve",
            json!({"meter": "calls", "amount": 25})
        ))["fits"],
        false
    );
}

#[test]
fn the_scope_operations_take_only_what_a_row_inside_them_reserved() {
    let mut host = chain(scoped());
    changed(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 4}),
    ));
    // w2 is not a1's scope.
    let error = refused(op(
        &mut host,
        "Workspace",
        "w2",
        &alice(),
        T0,
        "reserveFor",
        json!({"meter": "calls", "schema": "Account", "id": "a1", "amount": 1}),
    ));
    assert_eq!(
        error["message"],
        "operation reserveFor of behavior Budget: /id: Account a1 does not draw meter calls from Workspace w2"
    );
    // w1 holds all a1 reserved.
    let (code, _) = veto_of(op(
        &mut host,
        "Workspace",
        "w1",
        &alice(),
        T0,
        "reserveFor",
        json!({"meter": "calls", "schema": "Account", "id": "a1", "amount": 1}),
    ));
    assert_eq!(code, "exceeds_reservation");
    let error = refused(op(
        &mut host,
        "Workspace",
        "w1",
        &alice(),
        T0,
        "reserveFor",
        json!({"meter": "calls", "schema": "Account", "id": "a1", "amount": 1}),
    ));
    assert_eq!(
        error["details"]["reason"],
        "Account a1 has 4 of meter calls reserved and 4 of it is held here, so 1 more is not its to hold"
    );
    // settleFor frees nothing a1 still holds.
    let settled = changed(op(
        &mut host,
        "Workspace",
        "w1",
        &alice(),
        T0,
        "settleFor",
        json!({"meter": "calls", "schema": "Account", "id": "a1", "amount": 4}),
    ));
    assert_eq!(settled.result, json!({"released": 0}));
    assert!(settled.rows.is_empty());
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 4, Some(20))
    );
}

#[test]
fn a_scope_relation_does_not_move_while_a_reservation_is_held_through_it() {
    let mut host = chain(scoped());
    changed(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 4}),
    ));
    let (code, details) =
        veto_of(host.update("Account", &alice(), T0, "a1", json!({"workspaceId": "w2"})));
    assert_eq!(code, "scope_reserved");
    assert_eq!(details, json!({"meter": "calls", "reserved": 4}));
    let error = refused(host.update("Account", &alice(), T0, "a1", json!({"workspaceId": "w2"})));
    assert_eq!(
        error["details"]["reason"],
        "meter calls has 4 reserved through workspace; settle it before workspaceId changes"
    );
    // The same key, or another field, is no move.
    changed(host.update(
        "Account",
        &alice(),
        T0,
        "a1",
        json!({"workspaceId": "w1", "name": "B"}),
    ));
    changed(on_account(&mut host, T0, "settle", json!({})));
    changed(host.update("Account", &alice(), T0, "a1", json!({"workspaceId": "w2"})));
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "reserve",
            json!({"meter": "calls", "amount": 2})
        )),
        json!({"reserved": {"calls": 2}})
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w2", T0)["calls"],
        meter(0, 2, Some(20))
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 0, Some(20))
    );
}

#[test]
fn a_reservation_through_a_scope_the_relation_no_longer_points_at_waits_for_its_settlement() {
    let mut host = chain(scoped());
    changed(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 4}),
    ));
    // A raw SQL write moves the relation past the guard.
    let row = host
        .tables
        .rows
        .get_mut(&("Account".to_owned(), "a1".to_owned()))
        .expect("the row");
    row.own.insert("workspaceId".into(), json!("w2"));
    let error = refused(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 1}),
    ));
    assert_eq!(error["details"]["vetoCode"], "scope_moved");
    assert_eq!(
        error["details"]["reason"],
        "meter calls has 4 reserved through Workspace w1; settle it before reserving through Workspace w2"
    );
    assert_eq!(
        result(on_account(
            &mut host,
            T0,
            "checkReserve",
            json!({"meter": "calls", "amount": 1})
        ))["fits"],
        false
    );
    // Usage is released where the reservation is held and counted where the
    // relation points.
    changed(on_account(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 4}),
    ));
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 0, Some(20))
    );
    assert_eq!(
        meters_at(&host, "Workspace", "w2", T0)["calls"],
        meter(4, 0, Some(20))
    );
}

#[test]
fn deleting_a_row_settles_everything_it_reserved_up_the_chain() {
    let mut host = chain(scoped());
    changed(on_account(
        &mut host,
        T0,
        "reserve",
        json!({"meter": "calls", "amount": 4}),
    ));
    changed(host.delete("Account", &alice(), T0, "a1"));
    assert_eq!(
        meters_at(&host, "Workspace", "w1", T0)["calls"],
        meter(0, 0, Some(20))
    );
    assert_eq!(
        meters_at(&host, "Org", "o1", T0)["calls"],
        meter(0, 0, Some(100))
    );
    assert_eq!(
        host.sub_rows("Workspace", "w1", "Budget", "holds"),
        json!([])
    );
    assert_eq!(host.sub_rows("Org", "o1", "Budget", "holds"), json!([]));
}

// Gate is a test behavior that vetoes Workflow's transition into out,
// whoever makes it.
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
            .filter(|params| params["to"] == "out")
            .map(|_| Veto::new("it is held").with_code("held")))
    }
}

#[test]
fn escalate_moves_the_status_and_a_veto_or_a_permission_the_caller_lacks_leaves_it() {
    let config = json!({"meters": {"calls": {"limit": 2}}, "escalate": {"transition": "out", "from": ["open"]}});
    let mover = caller("mover", &["accounts.move"]);
    // The caller lacks the transition's permission: no move, counted all
    // the same.
    let mut host = Host::new(core(), [org(), workspace(), account(config.clone())]);
    changed(host.create("Account", &alice(), T0, "a1", json!({"name": "A"})));
    let counted = result(on_account(
        &mut host,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 3}),
    ));
    assert_eq!(
        counted["overruns"],
        json!([{"schema": "Account", "id": "a1", "used": 3, "limit": 2, "escalated": false}])
    );
    assert_eq!(host.owned("Account", "a1", "status", "status"), "open");
    // With it, the move.
    let moved = result(op(
        &mut host,
        "Account",
        "a1",
        &mover,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 1}),
    ));
    assert_eq!(moved["overruns"][0]["escalated"], true);
    assert_eq!(host.owned("Account", "a1", "status", "status"), "out");
    // A guard's veto leaves it.
    let spec = account(config).with(UseSpec::on_type("Gate", None));
    let mut host = Host::new(core().with(Gate), [org(), workspace(), spec]);
    changed(host.create("Account", &alice(), T0, "a1", json!({"name": "A"})));
    let counted = result(op(
        &mut host,
        "Account",
        "a1",
        &mover,
        T0,
        "recordUsage",
        json!({"meter": "calls", "amount": 3}),
    ));
    assert_eq!(counted["overruns"][0]["escalated"], false);
    assert_eq!(counted["used"], 3);
    assert_eq!(host.owned("Account", "a1", "status", "status"), "open");
}

// ---- Lease ----

// A run is leased by the worker that runs it, which reserves under its
// lease.
fn run_spec(budget: Value) -> TypeSpec {
    TypeSpec::new("Run")
        .with(UseSpec::on_type("Lease", json!({"ttlMs": 60_000})))
        .with(UseSpec::on_type("Budget", budget))
        .with_field(FieldSpec::new("title", "string"))
}

fn leased_run(budget: Value) -> Host {
    let mut host = Host::new(core(), [run_spec(budget)]);
    changed(host.create("Run", &alice(), T0, "r1", json!({"title": "Build"})));
    host
}

fn on_run(host: &mut Host, who: &str, now: i64, name: &str, params: Value) -> Outcome {
    op(host, "Run", "r1", &caller(who, &[]), now, name, params)
}

fn acquire(host: &mut Host, who: &str, now: i64) -> i64 {
    let answer = result(host.invoke(
        "Run",
        &caller(who, &[]),
        now,
        "r1",
        "Lease",
        "acquire",
        json!({}),
    ));
    answer["token"].as_i64().expect("a token")
}

#[test]
fn on_a_table_that_implements_lease_a_reservation_lives_within_the_active_lease() {
    let mut host = leased_run(json!({"meters": {"cpu": {"limit": 10}}}));
    let (code, _) = veto_of(on_run(
        &mut host,
        "wren",
        T0,
        "reserve",
        json!({"meter": "cpu", "amount": 4}),
    ));
    assert_eq!(code, "not_leased");
    let token = acquire(&mut host, "wren", T0);
    assert_eq!(
        result(on_run(
            &mut host,
            "wren",
            T0,
            "reserve",
            json!({"meter": "cpu", "amount": 4})
        )),
        json!({"reserved": {"cpu": 4}})
    );
    assert_eq!(
        host.sub_rows("Run", "r1", "Budget", "meters")[0]["values"]["token"],
        token
    );
    // Lease's guard holds another principal's reserve to the holder.
    let (code, _) = veto_of(on_run(
        &mut host,
        "otto",
        T0,
        "reserve",
        json!({"meter": "cpu", "amount": 1}),
    ));
    assert_eq!(code, "held_by_another");
    // A release, a Lease operation, settles what the lease reserved.
    changed(host.invoke_fenced(
        "Run",
        &caller("wren", &[]),
        T0 + 1000,
        "r1",
        "Lease",
        "release",
        json!({}),
        token,
    ));
    assert_eq!(
        meters_at(&host, "Run", "r1", T0 + 1000)["cpu"],
        meter(0, 0, Some(10))
    );
    // So does the lease's expiry, its deadline.
    let token = acquire(&mut host, "wren", T0 + 2000);
    changed(on_run(
        &mut host,
        "wren",
        T0 + 2000,
        "reserve",
        json!({"meter": "cpu", "amount": 3}),
    ));
    // checkReserve counts a lapsed lease's reservation as settled.
    assert_eq!(
        result(on_run(
            &mut host,
            "otto",
            T0 + 70_000,
            "checkReserve",
            json!({"meter": "cpu", "amount": 9})
        )),
        json!({"fits": true, "until": null, "scopes": []})
    );
    let expired = host.fall_due(T0 + 2000 + 60_000, &caller("runner", &[]));
    assert_eq!(expired.len(), 1);
    changed(expired.into_iter().next().expect("an expiry").2);
    assert_eq!(host.owned("Run", "r1", "Lease", "token"), token + 1);
    assert_eq!(
        meters_at(&host, "Run", "r1", T0 + 70_000)["cpu"],
        meter(0, 0, Some(10))
    );
    // Usage is counted all the same, without a lease.
    assert_eq!(
        result(on_run(
            &mut host,
            "otto",
            T0 + 70_000,
            "recordUsage",
            json!({"meter": "cpu", "amount": 2})
        ))["used"],
        2
    );
}

#[test]
fn on_exceeded_directs_the_holder_of_the_active_lease_once_per_lease() {
    let mut host =
        leased_run(json!({"meters": {"cpu": {"limit": 3}}, "onExceeded": {"direct": "slowDown"}}));
    acquire(&mut host, "wren", T0);
    let first = result(on_run(
        &mut host,
        "wren",
        T0,
        "recordUsage",
        json!({"meter": "cpu", "amount": 4}),
    ));
    assert_eq!(first["directed"], true);
    let again = result(on_run(
        &mut host,
        "wren",
        T0,
        "recordUsage",
        json!({"meter": "cpu", "amount": 1}),
    ));
    assert_eq!(again["directed"], false);
    let directives = host.sub_rows("Run", "r1", "Lease", "directives");
    assert_eq!(directives.as_array().map(Vec::len), Some(1));
    assert_eq!(directives[0]["values"]["name"], "slowDown");
    assert_eq!(
        directives[0]["values"]["data"],
        json!({"meter": "cpu", "used": 4, "limit": 3, "scope": {"schema": "Run", "id": "r1"}})
    );
    assert_eq!(
        host.sub_rows("Run", "r1", "Budget", "meters")[0]["values"]["directed"],
        1
    );
}
