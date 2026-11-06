//! Pool on the pure core (D104, step 3), through the core's public API over
//! a host of several types in memory that keeps rows, the deadlines the
//! core arms with their payloads, the events it emits and the reads across
//! a table it answers: the declaration and the config held to the file's
//! schemas, each config rule; the parameters held to the paramsSchemas, and
//! pick's match and prefer held to the type's own fields; a new member
//! open; markOut with and without until, a past until, the later kept, a
//! repeat answered unchanged before any guard, and the permission refused
//! before the row is written; reportRoom's three paths, near and back to
//! open, a check without room with and without resetsAt and its recheck
//! armed at the later of outUntil and recheckAfterMs from now; the
//! recheck's three paths and its payload held to the file's payloadSchema;
//! pick's tiers, prefer absent, match pushed to the host and checked again,
//! a UUID field in either form, exclude, an order with no value last and
//! ties by key, none open with until, Capacity's no room and its until,
//! pool_too_large past 2,000 members; pick refused as an operation of a
//! row; and the config change.

mod support;
mod tables;

use serde_json::{json, Map, Value};
use superschematic_rules::{
    Action, Behavior, Caller, Declaration, FieldSpec, Outcome, ReadRequest, Row, TableRef,
    TypeSpec, UseSpec,
};
use superschematic_rules_behaviors::{core, pool, Pool, ONE_WAY_NUMBERS};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

// 2026-03-01T00:00:00Z.
const T0: i64 = 1_772_323_200_000;
const MINUTE: i64 = 60_000;
const HOUR: i64 = 60 * MINUTE;

// DANA is a UUID in base62, DANA_HYPHENATED the same one hyphenated.
const DANA: &str = "TMXp8QPR6yGvKuV2GZQ10";
const DANA_HYPHENATED: &str = "0f8fad5b-d9cb-469f-a165-70867728950e";

// An account a person's agents run on: its owner (a UUID), its vendor, its
// kind, its rank and when it was opened.
fn account(uses: Vec<UseSpec>) -> TypeSpec {
    let mut spec = TypeSpec::new("Account");
    for entry in uses {
        spec = spec.with(entry);
    }
    spec.with_field(FieldSpec::new("owner", "Identity.UUID"))
        .with_field(FieldSpec::new("name", "string"))
        .with_field(FieldSpec::new("vendor", "string"))
        .with_field(FieldSpec::new("kind", "string"))
        .with_field(FieldSpec::new("rank", "Generic.Int64"))
        .with_field(FieldSpec::date_time("openedAt", "Temporal.DateTime"))
}

fn pooled(config: Value) -> TypeSpec {
    account(vec![UseSpec::on_type("Pool", config)])
}

fn config() -> Value {
    json!({"order": "rank", "permission": "accounts.pool"})
}

fn alice() -> Caller {
    Caller::new("alice", ["accounts.pool", "accounts.observe"])
}

fn guest() -> Caller {
    Caller::new("guest", Vec::<String>::new())
}

fn runner() -> Caller {
    Caller::new("runner", Vec::<String>::new())
}

// hosting creates each account given, `(key, vendor, kind, rank)`.
fn hosting(spec: TypeSpec, accounts: &[(&str, &str, &str, Option<i64>)]) -> Host {
    let mut host = Host::new(core(), [spec]);
    for (key, vendor, kind, rank) in accounts {
        let mut own = json!({"name": key, "vendor": vendor, "kind": kind, "owner": DANA});
        if let Some(rank) = rank {
            own["rank"] = json!(rank);
        }
        changed(host.create("Account", &alice(), T0, key, own));
    }
    host
}

fn two() -> Host {
    hosting(
        pooled(config()),
        &[
            ("a1", "anthropic", "account", Some(1)),
            ("a2", "anthropic", "account", Some(2)),
        ],
    )
}

fn invoke(
    host: &mut Host,
    who: &Caller,
    now: i64,
    key: &str,
    name: &str,
    params: Value,
) -> Outcome {
    host.invoke("Account", who, now, key, "Pool", name, params)
}

fn mark_out(host: &mut Host, now: i64, key: &str, params: Value) -> Value {
    result(invoke(host, &alice(), now, key, pool::MARK_OUT, params))
}

fn report(host: &mut Host, now: i64, key: &str, params: Value) -> Value {
    result(invoke(host, &alice(), now, key, pool::REPORT_ROOM, params))
}

fn pick_as(host: &mut Host, who: &Caller, now: i64, params: Value) -> Outcome {
    host.run(
        "Account",
        who,
        now,
        Action::TableOperation {
            use_id: "Pool".into(),
            operation: pool::PICK.into(),
            params,
        },
    )
}

fn pick(host: &mut Host, now: i64, params: Value) -> Value {
    result(pick_as(host, &alice(), now, params))
}

fn member(key: &str, availability: &str, order: Value, preferred: bool) -> Value {
    json!({"member": {"key": key, "availability": availability, "order": order, "preferred": preferred}})
}

fn owned(host: &Host, key: &str) -> Value {
    Value::Object(
        host.row("Account", key)
            .owned
            .get("Pool")
            .cloned()
            .unwrap_or_default()
            .into_iter()
            .filter(|(_, value)| !value.is_null())
            .collect(),
    )
}

fn armed(host: &Host, key: &str) -> Option<(i64, Value)> {
    let at = host.armed("Account", key, "Pool", pool::RECHECK)?;
    let payload = host
        .tables
        .payloads
        .get(&(
            ("Account".to_owned(), key.to_owned()),
            "Pool".to_owned(),
            pool::RECHECK.to_owned(),
        ))
        .cloned()
        .unwrap_or(Value::Null);
    Some((at, payload))
}

// ---- the declaration and its schemas ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Pool.declaration(),
        Declaration::new("Pool")
            .field("availability")
            .field("outUntil")
            .field("outReason")
            .field("checkedAt")
            .arms_deadlines()
            .operation("markOut", true)
            .operation("reportRoom", true)
            .table_operation("pick", false)
            .event("recheck")
            .asking("Capacity")
            .veto("pool_too_large")
    );
    let file: Value = serde_json::from_str(pool::DECLARATION).expect("it parses");
    assert_eq!(file.get("tables"), None);
    assert_eq!(file.get("hearsChanges"), None);
    assert_eq!(file.get("value"), None);
    assert_eq!(pool::ORDER_TYPES, ONE_WAY_NUMBERS);
    assert_eq!(pool::MAX_MEMBERS, 4 * superschematic_rules::LINKED_PAGE);
}

/// The checks Pool writes by hand are the declaration's configSchema,
/// keyword for keyword; each comes out in the schema loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(pool::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    let at_least = json!({"minimum": 1000, "type": "integer"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "order": text,
                "outForMs": at_least,
                "permission": text,
                "recheckAfterMs": at_least
            },
            "required": ["order"],
            "type": "object"
        })
    );
    let shaped = |config: Value| shape(&core(), &pooled(config));
    assert_eq!(shaped(json!([])), ["/config: must be object"]);
    assert_eq!(
        shaped(json!({})),
        ["/config: must have required property 'order'"]
    );
    assert_eq!(
        shaped(
            json!({"order": "", "outForMs": 999, "recheckAfterMs": 1500.5, "permission": 5, "ttlMs": 1})
        ),
        [
            "/config: unknown key \"ttlMs\"",
            "/config/order: must NOT have fewer than 1 characters",
            "/config/outForMs: must be >= 1000",
            "/config/permission: must be string",
            "/config/recheckAfterMs: must be integer",
        ]
    );
    let taken = define(
        &core(),
        &pooled(json!({"order": "rank", "outForMs": 1000, "recheckAfterMs": 1000})),
    );
    assert!(taken.is_ok());
}

#[test]
fn order_names_a_number_field_of_the_type() {
    let core = core();
    assert_eq!(
        rule(&core, &pooled(json!({"order": "score"})), "Pool"),
        "order \"score\" is not a field of Account (its fields: owner, name, vendor, kind, rank, openedAt)"
    );
    assert_eq!(
        rule(&core, &pooled(json!({"order": "name"})), "Pool"),
        "order \"name\" is not a number field of Account: it ranks the members by a number (number, Generic.Int64 or Generic.Float64)"
    );
    assert_eq!(
        rule(
            &core,
            &TypeSpec::new("Bare").with(UseSpec::on_type("Pool", json!({"order": "rank"}))),
            "Pool"
        ),
        "order \"rank\" is not a field of Bare (its fields: none)"
    );
    for type_name in ["number", "Generic.Int64", "Generic.Float64"] {
        let spec = TypeSpec::new("Key")
            .with(UseSpec::on_type("Pool", json!({"order": "weight"})))
            .with_field(FieldSpec::new("weight", type_name));
        assert!(define(&core, &spec).is_ok(), "{type_name}");
    }
}

/// The checks of each operation's parameters are its paramsSchema, keyword
/// for keyword, in the registry's words; pick's match and prefer name own
/// fields that hold no instant.
#[test]
fn the_params_checks_are_the_params_schemas() {
    let file: Value = serde_json::from_str(pool::DECLARATION).expect("it parses");
    let schema_of = |name: &str| {
        let operation = file["operations"]
            .as_array()
            .expect("operations")
            .iter()
            .find(|operation| operation["name"] == name)
            .expect("the operation");
        undescribed(&operation["paramsSchema"])
    };
    let reason = json!({"maxLength": 256, "minLength": 1, "type": "string"});
    let instant = json!({"minimum": 0, "type": "integer"});
    let named = json!({"additionalProperties": {"type": ["string", "number", "boolean", "null"]}, "type": "object"});
    assert_eq!(
        schema_of("markOut"),
        json!({"additionalProperties": false, "properties": {"reason": reason, "until": instant}, "required": ["reason"], "type": "object"})
    );
    assert_eq!(
        schema_of("reportRoom"),
        json!({"additionalProperties": false, "properties": {"near": {"type": "boolean"}, "reason": reason, "resetsAt": instant, "room": {"type": "boolean"}}, "required": ["room"], "type": "object"})
    );
    assert_eq!(
        schema_of("pick"),
        json!({"additionalProperties": false, "properties": {
            "exclude": {"items": {"minLength": 1, "type": "string"}, "maxItems": 100, "type": "array"},
            "match": named, "prefer": named
        }, "type": "object"})
    );
    let mut host = two();
    let message = |host: &mut Host, name: &str, params: Value| {
        let outcome = if name == pool::PICK {
            pick_as(host, &alice(), T0, params)
        } else {
            invoke(host, &alice(), T0, "a1", name, params)
        };
        refused(outcome)["message"]
            .as_str()
            .unwrap_or_default()
            .to_owned()
    };
    assert_eq!(
        message(&mut host, "markOut", json!({"until": -1, "reason": "x".repeat(257), "x": 1})),
        "operation markOut of behavior Pool: must NOT have additional properties: x; /reason: must NOT have more than 256 characters; /until: must be >= 0"
    );
    assert_eq!(
        message(&mut host, "markOut", json!({"until": 1.5})),
        "operation markOut of behavior Pool: must have required property 'reason'; /until: must be integer"
    );
    assert_eq!(
        message(&mut host, "reportRoom", json!({"room": "yes", "near": 1, "resetsAt": "soon", "reason": ""})),
        "operation reportRoom of behavior Pool: /near: must be boolean; /reason: must NOT have fewer than 1 characters; /resetsAt: must be integer; /room: must be boolean"
    );
    assert_eq!(
        message(&mut host, "pick", json!({"match": {"vendor": {"x": 1}, "openedAt": 5, "owner": null}, "prefer": [], "exclude": [""]})),
        "operation pick of behavior Pool: /exclude/0: must NOT have fewer than 1 characters; /match/openedAt: openedAt holds an instant, which match compares no value of; /match/vendor: must be string,number,boolean,null; /prefer: must be object"
    );
    let many: Vec<String> = (0..=pool::MAX_EXCLUDE).map(|n| format!("k{n}")).collect();
    assert_eq!(
        message(&mut host, "pick", json!({"exclude": many, "prefer": {"plan": "pro"}})),
        "operation pick of behavior Pool: /exclude: must NOT have more than 100 items; /prefer/plan: Account has no own field plan (its own fields: owner, name, vendor, kind, rank, openedAt)"
    );
}

// ---- the members' state ----

#[test]
fn a_new_member_is_open() {
    let host = two();
    assert_eq!(owned(&host, "a1"), json!({"availability": "open"}));
    assert_eq!(armed(&host, "a1"), None);
}

#[test]
fn mark_out_keeps_the_later_until_and_answers_a_repeat_unchanged() {
    let mut host = two();
    // outForMs on, with no until, and its recheck armed then.
    assert_eq!(
        mark_out(&mut host, T0, "a1", json!({"reason": "rate limited"})),
        json!({"availability": "out", "outUntil": T0 + 30 * MINUTE, "outReason": "rate limited"})
    );
    assert_eq!(
        armed(&host, "a1"),
        Some((T0 + 30 * MINUTE, json!({"dueAt": T0 + 30 * MINUTE})))
    );
    // A repeat that moves neither its time nor its reason writes nothing.
    let version = host.row("Account", "a1").version;
    let repeat = changed(invoke(
        &mut host,
        &alice(),
        T0 + MINUTE,
        "a1",
        pool::MARK_OUT,
        json!({"until": T0 + 10 * MINUTE, "reason": "rate limited"}),
    ));
    assert_eq!(
        repeat.result,
        json!({"availability": "out", "outUntil": T0 + 30 * MINUTE, "outReason": "rate limited", "unchanged": true})
    );
    assert!(repeat.rows.is_empty() && repeat.deadlines.is_empty());
    assert_eq!(host.row("Account", "a1").version, version);
    // An earlier until with a new reason keeps the time and takes the reason.
    assert_eq!(
        mark_out(
            &mut host,
            T0 + MINUTE,
            "a1",
            json!({"until": T0 + 10 * MINUTE, "reason": "quota"})
        ),
        json!({"availability": "out", "outUntil": T0 + 30 * MINUTE, "outReason": "quota"})
    );
    // A later one moves it, and its recheck.
    assert_eq!(
        mark_out(
            &mut host,
            T0 + MINUTE,
            "a1",
            json!({"until": T0 + HOUR, "reason": "quota"})
        ),
        json!({"availability": "out", "outUntil": T0 + HOUR, "outReason": "quota"})
    );
    assert_eq!(armed(&host, "a1").map(|(at, _)| at), Some(T0 + HOUR));
    // A time already past is now.
    assert_eq!(
        mark_out(
            &mut host,
            T0 + 2 * MINUTE,
            "a2",
            json!({"until": T0, "reason": "gone"})
        ),
        json!({"availability": "out", "outUntil": T0 + 2 * MINUTE, "outReason": "gone"})
    );
    // checkedAt is left as it was.
    assert_eq!(owned(&host, "a2").get("checkedAt"), None);
}

#[test]
fn mark_out_and_report_room_need_the_permission_before_any_write() {
    let mut host = two();
    let version = host.row("Account", "a1").version;
    assert_eq!(
        refused(invoke(
            &mut host,
            &guest(),
            T0,
            "a1",
            pool::MARK_OUT,
            json!({"reason": "x"})
        )),
        json!({"class": "EngineError", "code": "forbidden", "message": "guest may not mark Account a1 out: it needs permission accounts.pool"})
    );
    assert_eq!(
        refused(invoke(&mut host, &Caller::new("", Vec::<String>::new()), T0, "a1", pool::REPORT_ROOM, json!({"room": true})))["message"],
        "a caller with no subject may not report a check of Account a1: it needs permission accounts.pool"
    );
    assert_eq!(host.row("Account", "a1").version, version);
    // Without a permission any caller the route admits may.
    let mut host = hosting(
        pooled(json!({"order": "rank"})),
        &[("a1", "openai", "key", None)],
    );
    assert_eq!(
        result(invoke(
            &mut host,
            &guest(),
            T0,
            "a1",
            pool::MARK_OUT,
            json!({"reason": "x"})
        ))["availability"],
        "out"
    );
}

#[test]
fn report_room_opens_an_out_member_only_at_or_after_its_time() {
    let mut host = two();
    mark_out(
        &mut host,
        T0,
        "a1",
        json!({"until": T0 + 10 * MINUTE, "reason": "quota"}),
    );
    // A check with room before outUntil records checkedAt alone.
    assert_eq!(
        report(
            &mut host,
            T0 + MINUTE,
            "a1",
            json!({"room": true, "near": true})
        ),
        json!({"availability": "out", "checkedAt": T0 + MINUTE, "outUntil": T0 + 10 * MINUTE, "outReason": "quota"})
    );
    assert_eq!(armed(&host, "a1").map(|(at, _)| at), Some(T0 + 10 * MINUTE));
    // At its time it opens, near as near says, and clears the recheck.
    assert_eq!(
        report(
            &mut host,
            T0 + 10 * MINUTE,
            "a1",
            json!({"room": true, "near": true})
        ),
        json!({"availability": "near", "checkedAt": T0 + 10 * MINUTE, "opened": true})
    );
    assert_eq!(
        owned(&host, "a1"),
        json!({"availability": "near", "checkedAt": T0 + 10 * MINUTE})
    );
    assert_eq!(armed(&host, "a1"), None);
    // And back to open.
    assert_eq!(
        report(
            &mut host,
            T0 + 11 * MINUTE,
            "a1",
            json!({"room": true, "resetsAt": 1, "reason": "x"})
        ),
        json!({"availability": "open", "checkedAt": T0 + 11 * MINUTE})
    );
}

#[test]
fn a_check_without_room_puts_a_member_out_and_arms_its_recheck() {
    let mut host = two();
    // With no time named: outForMs on, the reason no room.
    assert_eq!(
        report(&mut host, T0, "a1", json!({"room": false, "near": true})),
        json!({"availability": "out", "checkedAt": T0, "outUntil": T0 + 30 * MINUTE, "outReason": "no room"})
    );
    assert_eq!(
        armed(&host, "a1"),
        Some((T0 + 30 * MINUTE, json!({"dueAt": T0 + 30 * MINUTE})))
    );
    // An out member's time only lengthens, and it keeps its reason unless
    // the check names one.
    assert_eq!(
        report(
            &mut host,
            T0 + MINUTE,
            "a1",
            json!({"room": false, "resetsAt": T0 + 5 * MINUTE})
        ),
        json!({"availability": "out", "checkedAt": T0 + MINUTE, "outUntil": T0 + 30 * MINUTE, "outReason": "no room"})
    );
    assert_eq!(
        report(
            &mut host,
            T0 + MINUTE,
            "a1",
            json!({"room": false, "resetsAt": T0 + HOUR, "reason": "quota"})
        ),
        json!({"availability": "out", "checkedAt": T0 + MINUTE, "outUntil": T0 + HOUR, "outReason": "quota"})
    );
    // A reset already past puts it out until now and rechecks it no sooner
    // than recheckAfterMs on.
    mark_out(
        &mut host,
        T0,
        "a2",
        json!({"until": T0 + MINUTE, "reason": "busy"}),
    );
    assert_eq!(
        report(
            &mut host,
            T0 + 2 * MINUTE,
            "a2",
            json!({"room": false, "resetsAt": T0})
        ),
        json!({"availability": "out", "checkedAt": T0 + 2 * MINUTE, "outUntil": T0 + 2 * MINUTE, "outReason": "busy"})
    );
    assert_eq!(armed(&host, "a2").map(|(at, _)| at), Some(T0 + 4 * MINUTE));
}

// ---- the recheck ----

// holds_payload holds a recheck event's payload to the file's
// payloadSchema.
fn holds_payload(payload: &Value) {
    let file: Value = serde_json::from_str(pool::DECLARATION).expect("it parses");
    let schema = &file["events"][0]["payloadSchema"];
    let members = payload.as_object().expect("an object");
    for required in schema["required"].as_array().expect("required") {
        assert!(
            members.contains_key(required.as_str().unwrap()),
            "{required}"
        );
    }
    for (name, value) in members {
        let declared = &schema["properties"][name];
        match declared["type"].as_str() {
            Some("integer") => assert!(value.is_i64(), "{name}"),
            Some("string") => assert!(value.is_string(), "{name}"),
            _ => panic!("{name} is no property of the payloadSchema"),
        }
    }
}

#[test]
fn a_recheck_fires_at_out_until_and_every_recheck_after_ms_until_a_check() {
    let mut host = hosting(
        pooled(json!({"order": "rank", "recheckAfterMs": 120_000})),
        &[("a1", "anthropic", "account", Some(1))],
    );
    mark_out(
        &mut host,
        T0,
        "a1",
        json!({"until": T0 + 5 * MINUTE, "reason": "rate limited"}),
    );
    let fired = host.fall_due(T0 + 5 * MINUTE, &runner());
    assert_eq!(fired.len(), 1);
    let events = std::mem::take(&mut host.tables.events);
    assert_eq!(events.len(), 1);
    assert_eq!(events[0].kind, pool::RECHECK);
    assert_eq!(events[0].use_id, "Pool");
    assert_eq!(events[0].row.key.as_deref(), Some("a1"));
    assert_eq!(
        events[0].payload,
        json!({"outUntil": T0 + 5 * MINUTE, "outReason": "rate limited", "dueAt": T0 + 5 * MINUTE, "firedAt": T0 + 5 * MINUTE})
    );
    holds_payload(&events[0].payload);
    assert_eq!(
        armed(&host, "a1"),
        Some((T0 + 7 * MINUTE, json!({"dueAt": T0 + 7 * MINUTE})))
    );
    // A check with room after its time opens it.
    report(&mut host, T0 + 6 * MINUTE, "a1", json!({"room": true}));
    assert_eq!(owned(&host, "a1")["availability"], "open");
    // A check with room at its time clears the recheck it armed.
    mark_out(
        &mut host,
        T0 + 6 * MINUTE,
        "a1",
        json!({"until": T0 + 7 * MINUTE, "reason": "quota"}),
    );
    report(&mut host, T0 + 7 * MINUTE, "a1", json!({"room": true}));
    host.fall_due(T0 + 7 * MINUTE, &runner());
    assert!(host.tables.events.is_empty());
    // A check before its time leaves it out and armed, and the fire names
    // the check; a worker late to it fires at its clock.
    mark_out(
        &mut host,
        T0 + 8 * MINUTE,
        "a1",
        json!({"until": T0 + 9 * MINUTE, "reason": "quota"}),
    );
    report(&mut host, T0 + 8 * MINUTE + 1, "a1", json!({"room": true}));
    assert_eq!(armed(&host, "a1").map(|(at, _)| at), Some(T0 + 9 * MINUTE));
    host.fall_due(T0 + 10 * MINUTE, &runner());
    let events = std::mem::take(&mut host.tables.events);
    assert_eq!(
        events[0].payload,
        json!({"outUntil": T0 + 9 * MINUTE, "outReason": "quota", "checkedAt": T0 + 8 * MINUTE + 1, "dueAt": T0 + 9 * MINUTE, "firedAt": T0 + 10 * MINUTE})
    );
    holds_payload(&events[0].payload);
}

#[test]
fn a_recheck_on_a_member_opened_or_moved_since_emits_nothing() {
    let mut host = two();
    mark_out(
        &mut host,
        T0,
        "a1",
        json!({"until": T0 + MINUTE, "reason": "busy"}),
    );
    // A raw write opens it: the fire is spent and emits nothing.
    host.tables
        .rows
        .get_mut(&("Account".to_owned(), "a1".to_owned()))
        .expect("a1")
        .owned
        .insert(
            "Pool".into(),
            Map::from_iter([("availability".to_owned(), json!("open"))]),
        );
    host.fall_due(T0 + MINUTE, &runner());
    assert!(host.tables.events.is_empty());
    assert_eq!(armed(&host, "a1"), None);
    // A raw write moves its time past the fire: it arms again there.
    mark_out(
        &mut host,
        T0 + MINUTE,
        "a2",
        json!({"until": T0 + 2 * MINUTE, "reason": "busy"}),
    );
    host.tables
        .rows
        .get_mut(&("Account".to_owned(), "a2".to_owned()))
        .expect("a2")
        .owned
        .get_mut("Pool")
        .expect("Pool's fields")
        .insert("outUntil".into(), json!(T0 + 9 * MINUTE));
    host.fall_due(T0 + 2 * MINUTE, &runner());
    assert!(host.tables.events.is_empty());
    assert_eq!(
        armed(&host, "a2"),
        Some((T0 + 9 * MINUTE, json!({"dueAt": T0 + 9 * MINUTE})))
    );
    // A deleted member's recheck goes with its row.
    changed(host.delete("Account", &alice(), T0 + 3 * MINUTE, "a2"));
    assert_eq!(host.tables.armed.len(), 0);
}

// ---- pick ----

fn many() -> Host {
    hosting(
        pooled(config()),
        &[
            ("a1", "anthropic", "account", Some(1)),
            ("a2", "anthropic", "account", Some(2)),
            ("a3", "openai", "account", Some(3)),
            ("k1", "anthropic", "key", Some(9)),
            ("a4", "openai", "account", None),
        ],
    )
}

#[test]
fn pick_tries_prefers_open_then_its_near_then_the_rest_by_order_and_key() {
    let mut host = many();
    // With no prefer every member is preferred.
    assert_eq!(
        pick(&mut host, T0, json!({})),
        member("a1", "open", json!(1), true)
    );
    report(&mut host, T0, "a2", json!({"room": true, "near": true}));
    let anthropic = json!({"vendor": "anthropic", "kind": "account"});
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"prefer": anthropic, "exclude": ["a1"]})
        ),
        member("a2", "near", json!(2), true)
    );
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"prefer": anthropic, "exclude": ["a1", "a2"]})
        ),
        member("a3", "open", json!(3), false)
    );
    // A member with no order value comes after every one with one.
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"prefer": {"vendor": "openai"}, "exclude": ["a3"]})
        ),
        member("a4", "open", Value::Null, true)
    );
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"match": {"vendor": "openai"}, "prefer": {"vendor": "nobody"}, "exclude": ["a3"]})
        ),
        member("a4", "open", Value::Null, false)
    );
    // Ties by order go by key.
    let mut host = hosting(
        pooled(config()),
        &[
            ("b2", "x", "account", Some(5)),
            ("b1", "x", "account", Some(5)),
            ("b0", "x", "account", Some(6)),
        ],
    );
    assert_eq!(
        pick(&mut host, T0, json!({})),
        member("b1", "open", json!(5), true)
    );
    // pick needs no permission, and writes nothing.
    let picked = changed(pick_as(&mut host, &guest(), T0, json!({})));
    assert!(picked.rows.is_empty() && picked.deadlines.is_empty() && picked.events.is_empty());
}

#[test]
fn pick_pushes_match_to_the_host_and_checks_it_again() {
    let mut host = many();
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"kind": "key"}})),
        member("k1", "open", json!(9), true)
    );
    let last = host.tables.rows_read.last().cloned().expect("a read");
    assert_eq!(
        last,
        ReadRequest::rows(
            "Account",
            Map::from_iter([("kind".to_owned(), json!("key"))]),
            Vec::new(),
            None
        )
    );
    // A host that answers more than it was asked still picks right.
    host.tables.loose = true;
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"kind": "key"}})),
        member("k1", "open", json!(9), true)
    );
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"vendor": "nobody"}})),
        json!({"member": null})
    );
    // A null member is absent (D12): it asks the host for no equality, a
    // member with no value is not what it keeps, and a prefer of nulls
    // alone prefers every member.
    host.tables.loose = false;
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"match": {"rank": null, "vendor": "openai"}})
        ),
        member("a3", "open", json!(3), true)
    );
    let last = host.tables.rows_read.last().cloned().expect("a read");
    assert_eq!(
        last,
        ReadRequest::rows(
            "Account",
            Map::from_iter([("vendor".to_owned(), json!("openai"))]),
            Vec::new(),
            None
        )
    );
    assert_eq!(
        pick(
            &mut host,
            T0,
            json!({"prefer": {"vendor": null}, "exclude": ["a1"]})
        ),
        member("a2", "open", json!(2), true)
    );
    // A null member of a name no own field has is absent too.
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"nobody": null}})),
        member("a1", "open", json!(1), true)
    );
}

#[test]
fn a_uuid_field_and_a_key_compare_in_either_form() {
    let mut host = many();
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"owner": DANA_HYPHENATED}})),
        member("a1", "open", json!(1), true)
    );
    // The host is asked for the base62 wire form.
    let last = host.tables.rows_read.last().cloned().expect("a read");
    let ReadRequest::Rows { equal, .. } = last else {
        panic!("a read across the table");
    };
    assert_eq!(equal.get("owner"), Some(&json!(DANA)));
    assert_eq!(
        pick(&mut host, T0, json!({"prefer": {"owner": DANA_HYPHENATED}})),
        member("a1", "open", json!(1), true)
    );
    let mut host = hosting(
        pooled(config()),
        &[
            (DANA, "x", "account", Some(1)),
            ("b1", "x", "account", Some(2)),
        ],
    );
    assert_eq!(
        pick(&mut host, T0, json!({"exclude": [DANA_HYPHENATED]})),
        member("b1", "open", json!(2), true)
    );
}

#[test]
fn with_none_open_pick_answers_the_soonest_time_one_may_be_back() {
    let mut host = two();
    mark_out(
        &mut host,
        T0,
        "a1",
        json!({"until": T0 + 20 * MINUTE, "reason": "quota"}),
    );
    mark_out(
        &mut host,
        T0,
        "a2",
        json!({"until": T0 + 10 * MINUTE, "reason": "quota"}),
    );
    assert_eq!(
        pick(&mut host, T0, json!({})),
        json!({"member": null, "until": T0 + 10 * MINUTE})
    );
    // One past its outUntil awaiting a check is back recheckAfterMs on at
    // the soonest.
    assert_eq!(
        pick(&mut host, T0 + 15 * MINUTE, json!({})),
        json!({"member": null, "until": T0 + 17 * MINUTE})
    );
    // An excluded out member counts; an excluded open one does not.
    assert_eq!(
        pick(&mut host, T0, json!({"exclude": ["a2"]})),
        json!({"member": null, "until": T0 + 10 * MINUTE})
    );
    report(&mut host, T0 + 20 * MINUTE, "a1", json!({"room": true}));
    assert_eq!(
        pick(&mut host, T0 + 20 * MINUTE, json!({"exclude": ["a1"]})),
        json!({"member": null, "until": T0 + 22 * MINUTE})
    );
    // No member at all: no time.
    assert_eq!(
        pick(&mut host, T0, json!({"match": {"kind": "none"}})),
        json!({"member": null})
    );
}

#[test]
fn a_member_whose_capacity_has_no_room_counts_as_out_until_it_has() {
    let budgeted = account(vec![
        UseSpec::on_type("Pool", config()),
        UseSpec::on_type(
            "Budget",
            json!({"meters": {"tokens": {"limit": 100, "reset": "hourly"}}}),
        ),
    ]);
    let mut host = hosting(
        budgeted,
        &[
            ("a1", "anthropic", "account", Some(1)),
            ("a2", "anthropic", "account", Some(2)),
        ],
    );
    let usage = |host: &mut Host, key: &str, now: i64| {
        changed(host.invoke(
            "Account",
            &alice(),
            now,
            key,
            "Budget",
            "recordUsage",
            json!({"meter": "tokens", "amount": 100}),
        ));
    };
    usage(&mut host, "a1", T0 + 30 * MINUTE);
    assert_eq!(
        pick(&mut host, T0 + 30 * MINUTE, json!({})),
        member("a2", "open", json!(2), true)
    );
    // The read took Budget's sub-tables with each row.
    let last = host.tables.rows_read.last().cloned().expect("a read");
    let ReadRequest::Rows { tables, .. } = last else {
        panic!("a read across the table");
    };
    assert_eq!(
        tables,
        [
            TableRef::new("Budget", "meters"),
            TableRef::new("Budget", "holds"),
            TableRef::new("Budget", "buckets")
        ]
    );
    usage(&mut host, "a2", T0 + 30 * MINUTE);
    assert_eq!(
        pick(&mut host, T0 + 30 * MINUTE, json!({})),
        json!({"member": null, "until": T0 + HOUR})
    );
    assert_eq!(
        pick(&mut host, T0 + HOUR, json!({})),
        member("a1", "open", json!(1), true)
    );
    // Out both ways: the later of the two.
    usage(&mut host, "a1", T0 + HOUR);
    usage(&mut host, "a2", T0 + HOUR);
    mark_out(
        &mut host,
        T0 + HOUR,
        "a1",
        json!({"until": T0 + 3 * HOUR, "reason": "quota"}),
    );
    mark_out(
        &mut host,
        T0 + HOUR,
        "a2",
        json!({"until": T0 + HOUR + MINUTE, "reason": "quota"}),
    );
    assert_eq!(
        pick(&mut host, T0 + HOUR, json!({})),
        json!({"member": null, "until": T0 + 2 * HOUR})
    );
}

#[test]
fn a_pick_reads_at_most_two_thousand_members() {
    let mut host = Host::new(core(), [pooled(config())]);
    for at in 0..=pool::MAX_MEMBERS {
        host.tables.rows.insert(
            ("Account".to_owned(), format!("m{at:05}")),
            Row {
                own: Map::from_iter([("rank".to_owned(), json!(at))]),
                owned: Default::default(),
                version: 1,
            },
        );
    }
    let (code, details) = veto_of(pick_as(&mut host, &alice(), T0, json!({})));
    assert_eq!(code, "pool_too_large");
    assert_eq!(details, json!({"max": 2000}));
    let refusal = refused(pick_as(&mut host, &alice(), T0, json!({})));
    assert_eq!(
        refusal["message"],
        "behavior Pool vetoes pick of Account: Account's Pool reads every member match keeps at a pick, at most 2000, and more match: narrow match, or pick with code of the project's own"
    );
    // At 2,000 it picks, a row that predates the use reading as open.
    host.tables
        .rows
        .remove(&("Account".to_owned(), "m02000".to_owned()));
    assert_eq!(
        pick(&mut host, T0, json!({})),
        member("m00000", "open", json!(0), true)
    );
}

#[test]
fn pick_is_an_operation_of_the_table_and_mark_out_of_a_row() {
    let mut host = two();
    let refusal = refused(invoke(&mut host, &alice(), T0, "a1", pool::PICK, json!({})));
    assert_eq!(
        refusal["message"],
        "Account has no operation pick of Pool (its operations: markOut, reportRoom)"
    );
    let refusal = refused(host.run(
        "Account",
        &alice(),
        T0,
        Action::TableOperation {
            use_id: "Pool".into(),
            operation: pool::MARK_OUT.into(),
            params: json!({"reason": "x"}),
        },
    ));
    assert_eq!(
        refusal["message"],
        "Account has no operation markOut of Pool on the table as a whole (its operations on the table: pick)"
    );
}

#[test]
fn pool_joins_a_type_with_rows_and_does_not_leave_one() {
    use superschematic_rules::{Answer, Answers, Command, Outcome, Step};
    let core = core();
    let with = pooled(config());
    let without = account(Vec::new());
    let publish = |from: &TypeSpec, to: &TypeSpec, rows: bool| -> Option<String> {
        let command = Command::new(
            alice(),
            T0,
            Action::Publish {
                from: Some(from.clone()),
            },
        );
        let answers = Answers::new().with(ReadRequest::has_rows("Account"), Answer::HasRows(rows));
        match core.run(to, &command, &answers) {
            Step::Done(Outcome::Refused(refusal)) => Some(refusal.message),
            Step::Done(Outcome::Changed(_)) => None,
            other => panic!("{other:?}"),
        }
    };
    assert_eq!(publish(&without, &with, true), None);
    assert_eq!(
        publish(&with, &without, true),
        Some("type Account: behavior Pool cannot be removed from type Account, which has instances: the members' availability and their rechecks would stay behind".to_owned())
    );
    assert_eq!(publish(&with, &without, false), None);
    let narrower = pooled(json!({"order": "rank", "outForMs": 60_000, "recheckAfterMs": 1000}));
    assert_eq!(publish(&with, &narrower, true), None);
}
