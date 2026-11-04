//! Constants on the pure core (D103), through the core's public API over a
//! source in memory: the engine's rule, an update that changes a listed
//! field refused at the field unless the caller holds the config's
//! permission, absent and null one value, a create never refused; and the
//! fields that move one way (D104): up and down for a boolean, a number and
//! a date-time, no value below every value, the permission past it, a
//! create, a value the core cannot read passing, the first field in the
//! type's order refused, a constant beside a one-way field; and the config
//! held to the declaration file's schema and to the type's own fields.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{Behavior, Declaration, FieldSpec, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{
    constants, core, Constants, ConstantsConfig, ConstantsOneWay, OneWayDirection, OneWayKind,
    ONE_WAY_BOOLEANS, ONE_WAY_NUMBERS,
};
use support::{caller, changed, define, refused, rule, shape, undescribed, veto_of, Host, START};

// A run as a table holds it: a key, a kind, whether it is urgent, how often
// it ran, its budget, when it opens and closes, its labels, and Constants.
fn run(config: Value) -> TypeSpec {
    TypeSpec::new("Run")
        .with(UseSpec::on_type("Constants", config))
        .with_field(FieldSpec::new("key", "string"))
        .with_field(FieldSpec::new("kind", "string"))
        .with_field(FieldSpec::new("urgent", "boolean"))
        .with_field(FieldSpec::new("attempts", "number"))
        .with_field(FieldSpec::new("budget", "Generic.Int64"))
        .with_field(FieldSpec::date_time("opensAt", "Temporal.DateTime"))
        .with_field(FieldSpec::date_time("closesAt", "Temporal.DateTime"))
        .with_field(FieldSpec::new("labels", "string"))
}

fn hosting(config: Value) -> Host {
    let mut host = Host::new(core(), run(config));
    changed(host.create(&alice(), "r1", json!({"key": "r1"})));
    host
}

fn alice() -> superschematic_rules::Caller {
    caller("alice", &[])
}

fn admin() -> superschematic_rules::Caller {
    caller("ada", &["runs.admin"])
}

// one_way is the veto an update that moves a field back meets, as its
// details hold it.
fn one_way(field: &str, direction: &str, current: Value, attempted: Value) -> (String, Value) {
    (
        "one_way".to_owned(),
        json!({"field": field, "direction": direction, "current": current, "attempted": attempted}),
    )
}

// ---- the declaration and its schema ----

#[test]
fn the_declaration_is_the_files() {
    assert_eq!(
        Constants.declaration(),
        Declaration::new("Constants").veto("one_way")
    );
}

/// The checks Constants writes by hand are the declaration's configSchema,
/// keyword for keyword: the schema, its descriptions left out, is exactly
/// the one they implement, so a keyword the file gains fails this test
/// until the checks take it. Each keyword's refusal comes out in the schema
/// loader's words.
#[test]
fn the_config_checks_are_the_config_schema() {
    let file: Value = serde_json::from_str(constants::DECLARATION).expect("it parses");
    let text = json!({"minLength": 1, "type": "string"});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "additionalProperties": false,
            "properties": {
                "fields": {"items": text, "maxItems": 64, "minItems": 1, "type": "array", "uniqueItems": true},
                "oneWay": {
                    "additionalProperties": {"enum": ["up", "down"]},
                    "maxProperties": 64,
                    "minProperties": 1,
                    "type": "object"
                },
                "permission": text
            },
            "required": ["fields"],
            "type": "object"
        })
    );
    assert_eq!(file.get("createParamsSchema"), None);
    assert_eq!(file.get("operations"), None);
    let shaped = |config: Value| shape(&core(), &run(config));
    assert_eq!(shaped(json!([])), ["/config: must be object"]);
    assert_eq!(
        shaped(json!({"fieldz": ["key"]})),
        [
            "/config: unknown key \"fieldz\"",
            "/config: must have required property 'fields'"
        ]
    );
    assert_eq!(
        shaped(json!({"fields": []})),
        ["/config/fields: must NOT have fewer than 1 items"]
    );
    let many: Vec<String> = (0..65).map(|i| format!("f{i}")).collect();
    assert_eq!(
        shaped(json!({"fields": many})),
        ["/config/fields: must NOT have more than 64 items"]
    );
    assert_eq!(
        shaped(json!({"fields": ["key", "", 5, "key"], "permission": ""})),
        [
            "/config/fields: must NOT have duplicate items (items ## 3 and 0 are identical)",
            "/config/fields/1: must NOT have fewer than 1 characters",
            "/config/fields/2: must be string",
            "/config/permission: must NOT have fewer than 1 characters"
        ]
    );
    assert_eq!(
        shaped(json!({"fields": "key"})),
        ["/config/fields: must be array"]
    );
    assert_eq!(
        shaped(json!({"fields": ["key"], "oneWay": []})),
        ["/config/oneWay: must be object"]
    );
    assert_eq!(
        shaped(json!({"fields": ["key"], "oneWay": {}})),
        ["/config/oneWay: must NOT have fewer than 1 properties"]
    );
    let wide: serde_json::Map<String, Value> =
        (0..65).map(|i| (format!("f{i}"), json!("up"))).collect();
    assert_eq!(
        shaped(json!({"fields": ["key"], "oneWay": wide})),
        ["/config/oneWay: must NOT have more than 64 properties"]
    );
    assert_eq!(
        shaped(json!({"fields": ["key"], "oneWay": {"urgent": "sideways", "a/b": 1}})),
        [
            "/config/oneWay/a~1b: must be one of \"up\", \"down\"",
            "/config/oneWay/urgent: must be one of \"up\", \"down\""
        ]
    );
    // A use that gives no config needs one.
    let refusal = define(
        &core(),
        &TypeSpec::new("Run").with(UseSpec::on_type("Constants", None)),
    )
    .expect_err("refused");
    assert_eq!(
        refusal.message,
        "type Run: behavior Constants: must have required property 'config'"
    );
}

#[test]
fn its_fields_are_the_types_own_and_one_way_holds_a_boolean_a_number_or_a_date_time() {
    let ruled = |config: Value| rule(&core(), &run(config), "Constants");
    let fields = "key, kind, urgent, attempts, budget, opensAt, closesAt, labels";
    assert_eq!(
        ruled(json!({"fields": ["key", "status"]})),
        format!("fields: status is not a field of Run (its fields: {fields})")
    );
    // fields are checked before oneWay, and oneWay in the config's order.
    assert_eq!(
        ruled(json!({"fields": ["status"], "oneWay": {"lost": "up"}})),
        format!("fields: status is not a field of Run (its fields: {fields})")
    );
    assert_eq!(
        ruled(json!({"fields": ["key"], "oneWay": {"lost": "up", "kind": "up"}})),
        format!("oneWay: lost is not a field of Run (its fields: {fields})")
    );
    assert_eq!(
        ruled(json!({"fields": ["key", "urgent"], "oneWay": {"urgent": "up"}})),
        "oneWay: urgent is in fields, which keep the value their create gives them: a field moves one way or stays, not both"
    );
    assert_eq!(
        ruled(json!({"fields": ["key"], "oneWay": {"kind": "down"}})),
        "oneWay: kind is a string field of Run: a field that moves one way holds a boolean, a number or a date-time"
    );
    // A type that lists none of its fields names none.
    let bare = TypeSpec::new("Run").with(UseSpec::on_type("Constants", json!({"fields": ["key"]})));
    assert_eq!(
        rule(&core(), &bare, "Constants"),
        "fields: key is not a field of Run (its fields: none)"
    );
    // Each kind a field's type gives, in the type's order however the
    // config orders them.
    let spec = run(json!({
        "fields": ["key"],
        "oneWay": {"closesAt": "down", "budget": "down", "urgent": "up", "attempts": "up"},
        "permission": "runs.admin"
    }));
    let config = ConstantsConfig::parse(
        &spec.uses[0].config_or_empty(),
        &superschematic_rules::ConfigTarget::new(&spec, 0),
    )
    .expect("a config");
    let moving = |field: &str, direction, kind| ConstantsOneWay {
        field: field.to_owned(),
        direction,
        kind,
    };
    assert_eq!(
        config.one_way,
        vec![
            moving("urgent", OneWayDirection::Up, OneWayKind::Boolean),
            moving("attempts", OneWayDirection::Up, OneWayKind::Number),
            moving("budget", OneWayDirection::Down, OneWayKind::Number),
            moving("closesAt", OneWayDirection::Down, OneWayKind::DateTime),
        ]
    );
    assert_eq!(config.permission.as_deref(), Some("runs.admin"));
    assert_eq!(ONE_WAY_BOOLEANS, ["boolean"]);
    assert_eq!(
        ONE_WAY_NUMBERS,
        ["number", "Generic.Int64", "Generic.Float64"]
    );
    assert_eq!(OneWayDirection::parse("down"), Some(OneWayDirection::Down));
    assert_eq!(OneWayDirection::Up.as_str(), "up");
    assert_eq!(OneWayDirection::parse("sideways"), None);
}

// ---- the constant rule ----

/// An update that changes a listed field is refused at the field, a change
/// inside a list or an object included, whoever makes it, unless the caller
/// holds the permission; absent and null are one value, and a create is
/// never refused.
#[test]
fn an_update_that_changes_a_constant_is_refused_at_the_field() {
    let mut host = hosting(json!({"fields": ["key", "labels"], "permission": "runs.admin"}));
    let refusal = refused(host.update(&alice(), "r1", json!({"key": "r2", "kind": "build"})));
    assert_eq!(
        refusal,
        json!({"class": "InstanceValidationError", "code": "invalid_instance",
            "message": "Run: key: key is a constant of Run: its create sets it and nothing changes it after; only a caller with runs.admin may change it",
            "details": {"issues": [{"path": "key", "rule": "constant", "message": "key is a constant of Run: its create sets it and nothing changes it after; only a caller with runs.admin may change it"}]}})
    );
    // labels was absent: setting it is a change, null is no change.
    assert_eq!(
        refused(host.update(&alice(), "r1", json!({"labels": ["a"]})))["details"]["issues"][0]
            ["path"],
        "labels"
    );
    changed(host.update(
        &alice(),
        "r1",
        json!({"labels": null, "key": "r1", "kind": "build"}),
    ));
    changed(host.update(&admin(), "r1", json!({"key": "r2", "labels": ["a"]})));
    assert_eq!(host.row("r1").own["key"], "r2");
    // A create gives any value.
    changed(host.create(&alice(), "r2", json!({"key": "r9", "labels": ["b"]})));
    // With no permission, nobody may.
    let mut host = host_without_permission();
    assert_eq!(
        refused(host.update(&admin(), "r1", json!({"key": "r2"})))["details"]["issues"][0]
            ["message"],
        "key is a constant of Run: its create sets it and nothing changes it after"
    );
}

fn host_without_permission() -> Host {
    hosting(json!({"fields": ["key"]}))
}

// ---- oneWay ----

/// A boolean that moves up only turns true: none to either, false to true;
/// true to false and true to none are vetoed. One that moves down only
/// turns false: true to false, either to none; false to true and none to
/// either are vetoed. An equal value is no move.
#[test]
fn a_boolean_moves_one_way() {
    let mut host = hosting(json!({"fields": ["key"], "oneWay": {"urgent": "up"}}));
    changed(host.update(&alice(), "r1", json!({"urgent": false})));
    changed(host.update(&alice(), "r1", json!({"urgent": false})));
    changed(host.update(&alice(), "r1", json!({"urgent": true})));
    let error = refused(host.update(&alice(), "r1", json!({"urgent": false})));
    assert_eq!(
        error,
        json!({"class": "BehaviorVetoError", "code": "vetoed",
            "message": "behavior Constants vetoes update of Run r1: Run r1's urgent only turns true: an update may not move it from true to false",
            "details": {"behavior": "Constants", "action": "update",
                "reason": "Run r1's urgent only turns true: an update may not move it from true to false",
                "vetoCode": "one_way",
                "vetoDetails": {"field": "urgent", "direction": "up", "current": true, "attempted": false}}})
    );
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"urgent": null}))),
        one_way("urgent", "up", json!(true), Value::Null)
    );
    let mut host = hosting(json!({"fields": ["key"], "oneWay": {"urgent": "down"}}));
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"urgent": true}))),
        one_way("urgent", "down", Value::Null, json!(true))
    );
    changed(host.create(&alice(), "r2", json!({"key": "r2", "urgent": true})));
    changed(host.update(&alice(), "r2", json!({"urgent": false})));
    assert_eq!(
        refused(host.update(&alice(), "r2", json!({"urgent": true})))["details"]["reason"],
        "Run r2's urgent only turns false: an update may not move it from false to true"
    );
    changed(host.update(&alice(), "r2", json!({"urgent": null})));
    // An adapter may read a boolean column as 0 or 1.
    host.memory
        .rows
        .get_mut("r2")
        .expect("r2")
        .own
        .insert("urgent".into(), json!(0));
    assert_eq!(
        veto_of(host.update(&alice(), "r2", json!({"urgent": true}))),
        one_way("urgent", "down", json!(0), json!(true))
    );
}

/// A number that moves up only grows, one that moves down only shrinks;
/// whole numbers compare exactly, and a string that holds a decimal number,
/// as an adapter may read a BIGINT, is a number.
#[test]
fn a_number_moves_one_way() {
    let mut host =
        hosting(json!({"fields": ["key"], "oneWay": {"attempts": "up", "budget": "down"}}));
    // A field that moves down gets its value from the create: none to a
    // value is a move up.
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"budget": 10}))),
        one_way("budget", "down", Value::Null, json!(10))
    );
    host.memory.rows.clear();
    changed(host.create(&alice(), "r1", json!({"key": "r1", "budget": 10})));
    changed(host.update(&alice(), "r1", json!({"attempts": 1})));
    changed(host.update(&alice(), "r1", json!({"attempts": 2.5})));
    changed(host.update(&alice(), "r1", json!({"attempts": 2.5, "budget": 9})));
    assert_eq!(
        refused(host.update(&alice(), "r1", json!({"attempts": 2})))["details"]["reason"],
        "Run r1's attempts only grows: an update may not move it from 2.5 to 2"
    );
    assert_eq!(
        refused(host.update(&alice(), "r1", json!({"budget": 9.5})))["details"]["reason"],
        "Run r1's budget only shrinks: an update may not move it from 9 to 9.5"
    );
    // Clearing a number that moves down is a move down.
    changed(host.update(&alice(), "r1", json!({"budget": null})));
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"budget": 1}))),
        one_way("budget", "down", Value::Null, json!(1))
    );
    // A BIGINT read as text.
    host.memory
        .rows
        .get_mut("r1")
        .expect("r1")
        .own
        .insert("attempts".into(), json!("9007199254740993"));
    assert_eq!(
        veto_of(host.update(
            &alice(),
            "r1",
            json!({"attempts": 9_007_199_254_740_992_i64})
        )),
        one_way(
            "attempts",
            "up",
            json!("9007199254740993"),
            json!(9_007_199_254_740_992_i64)
        )
    );
}

/// A date-time moves one way as epoch milliseconds, a table's column, or
/// as ISO 8601 text, an engine schema's, compared by the instant; a
/// refusal names one held as epoch milliseconds as the ISO 8601 text
/// `toISOString` writes, the table's API's form, and text as it is.
#[test]
fn a_date_time_moves_one_way() {
    let mut host =
        hosting(json!({"fields": ["key"], "oneWay": {"opensAt": "up", "closesAt": "down"}}));
    changed(host.update(&alice(), "r1", json!({"opensAt": 1_767_225_600_000_i64})));
    changed(host.update(&alice(), "r1", json!({"opensAt": "2026-02-01T00:00:00Z"})));
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"opensAt": 1_767_225_600_000_i64}))),
        one_way(
            "opensAt",
            "up",
            json!("2026-02-01T00:00:00Z"),
            json!("2026-01-01T00:00:00.000Z")
        )
    );
    changed(host.update(&alice(), "r1", json!({"opensAt": 1_769_990_400_000_i64})));
    assert_eq!(
        refused(host.update(&alice(), "r1", json!({"opensAt": 1_769_904_000_000_i64})))["details"],
        json!({"behavior": "Constants", "action": "update",
            "reason": "Run r1's opensAt only grows: an update may not move it from \"2026-02-02T00:00:00.000Z\" to \"2026-02-01T00:00:00.000Z\"",
            "vetoCode": "one_way",
            "vetoDetails": {"field": "opensAt", "direction": "up",
                "current": "2026-02-02T00:00:00.000Z", "attempted": "2026-02-01T00:00:00.000Z"}})
    );
    changed(host.create(
        &alice(),
        "r2",
        json!({"key": "r2", "closesAt": "2026-12-31T00:00:00Z"}),
    ));
    changed(host.update(
        &alice(),
        "r2",
        json!({"closesAt": "2026-12-30T23:00:00-01:00"}),
    ));
    assert_eq!(
        refused(host.update(&alice(), "r2", json!({"closesAt": "2027-01-01T00:00:00Z"})))
            ["details"]["reason"],
        "Run r2's closesAt only shrinks: an update may not move it from \"2026-12-30T23:00:00-01:00\" to \"2027-01-01T00:00:00Z\""
    );
    changed(host.update(&alice(), "r2", json!({"closesAt": null})));
}

/// A value the core cannot read as the field's type is no move it can
/// order, and passes: the store's own-field validation refuses it.
#[test]
fn a_value_of_another_shape_passes() {
    let mut host = hosting(
        json!({"fields": ["key"], "oneWay": {"urgent": "up", "attempts": "up", "opensAt": "up"}}),
    );
    changed(host.update(
        &alice(),
        "r1",
        json!({"urgent": true, "attempts": 5, "opensAt": 5}),
    ));
    changed(host.update(
        &alice(),
        "r1",
        json!({"urgent": "no", "attempts": "few", "opensAt": "soon"}),
    ));
    changed(host.update(
        &alice(),
        "r1",
        json!({"urgent": false, "attempts": 1, "opensAt": 1}),
    ));
}

/// The caller with the config's permission moves a field either way, and
/// the refusal says so; a create gives any value.
#[test]
fn the_permission_moves_a_field_back_and_a_create_gives_any_value() {
    let mut host =
        hosting(json!({"fields": ["key"], "oneWay": {"urgent": "up"}, "permission": "runs.admin"}));
    changed(host.update(&alice(), "r1", json!({"urgent": true})));
    assert_eq!(
        refused(host.update(&alice(), "r1", json!({"urgent": false})))["details"]["reason"],
        "Run r1's urgent only turns true: an update may not move it from true to false; only a caller with runs.admin may move it back"
    );
    changed(host.update(&admin(), "r1", json!({"urgent": false})));
    changed(host.update(&admin(), "r1", json!({"urgent": null})));
    changed(host.create(&alice(), "r2", json!({"key": "r2", "urgent": false})));
    changed(host.create(&alice(), "r3", json!({"key": "r3", "urgent": true})));
}

/// The first field that moved back, in the order the type lists its
/// fields, is the one refused; and a constant changed beside one is
/// refused first, as the validation it is.
#[test]
fn the_types_first_field_is_refused_and_a_constant_is_refused_first() {
    let mut host =
        hosting(json!({"fields": ["key"], "oneWay": {"closesAt": "down", "attempts": "up"}}));
    host.memory.rows.clear();
    changed(host.create(
        &alice(),
        "r1",
        json!({"key": "r1", "attempts": 3, "closesAt": 10}),
    ));
    assert_eq!(
        veto_of(host.update(&alice(), "r1", json!({"closesAt": 20, "attempts": 1}))),
        one_way("attempts", "up", json!(3), json!(1))
    );
    let error = refused(host.update(&alice(), "r1", json!({"key": "r2", "attempts": 1})));
    assert_eq!(error["code"], "invalid_instance");
    assert_eq!(error["details"]["issues"][0]["rule"], "constant");
}

/// Constants keeps no state: any config may change, and it joins and
/// leaves a type with rows.
#[test]
fn any_config_change_is_allowed() {
    let mut host = hosting(json!({"fields": ["key"]}));
    let from = host.spec.clone();
    for next in [
        run(json!({"fields": ["kind"], "oneWay": {"urgent": "up"}})),
        TypeSpec::new("Run"),
    ] {
        let command = superschematic_rules::Command::new(
            alice(),
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
}
