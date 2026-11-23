//! Derived on the pure core (D73, D103 step 4), through the core's public
//! API over a host of several types in memory: the declaration's computed
//! fields and sub-table; the config held to the file's schemas and to the
//! type; a deriver's pull of the stale fields and its settle by hash, the
//! values written as an attributed update; a caller's override until
//! rederive, and a value a row held before the use counted as existing;
//! failures, a value the field's schema refuses and another use's veto
//! counted as attempts, into failedDerivations; the attempt entry refused
//! to a caller; states; the permission; a pull a page at a time; and the
//! config change.

mod support;
mod tables;

use serde_json::{json, Value};
use superschematic_rules::{Action, Caller, FieldSpec, Outcome, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{core, derived};
use support::{changed, define, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

const T0: i64 = 1_772_323_200_000;
const USE: &str = "Derived";

fn category() -> Value {
    json!({"description": "A Category value", "enum": ["billing", "bug", "other", null], "type": ["string", "null"]})
}

fn text() -> Value {
    json!({"description": "A string value", "type": ["string", "null"]})
}

// A ticket whose category and summary a model derives.
fn ticket(uses: Vec<UseSpec>) -> TypeSpec {
    let mut spec = TypeSpec::new("Ticket")
        .with_field(FieldSpec::new("subject", "string"))
        .with_field(FieldSpec::new("body", "string"))
        .with_field(FieldSpec::new("category", "Category").with_schema(category()))
        .with_field(FieldSpec::new("summary", "string").with_schema(text()));
    for entry in uses {
        spec = spec.with(entry);
    }
    spec
}

fn config() -> Value {
    json!({
        "fields": {
            "category": {"from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard"},
            "summary": {"from": ["body"], "instruction": "Summarize it in one sentence.", "model": "simple"}
        },
        "permission": "tickets.derive",
        "maxAttempts": 2
    })
}

fn derived_on(config: Value) -> TypeSpec {
    ticket(vec![UseSpec::on_type(USE, config)])
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn deriver() -> Caller {
    Caller::new("deriver", ["tickets.derive"])
}

fn table_op(host: &mut Host, who: &Caller, name: &str, params: Value) -> Outcome {
    host.run(
        "Ticket",
        who,
        T0 + 10,
        Action::TableOperation {
            use_id: USE.into(),
            operation: name.into(),
            params,
        },
    )
}

fn stale(host: &mut Host, params: Value) -> Value {
    result(table_op(host, &deriver(), "staleDerivations", params))
}

fn settle(host: &mut Host, items: Value) -> Value {
    result(table_op(
        host,
        &deriver(),
        "settleDerivations",
        json!({ "items": items }),
    ))
}

fn read(host: &Host, key: &str) -> Value {
    Value::Object(host.read_fields_at("Ticket", key, USE, T0))
}

fn opened(spec: TypeSpec) -> Host {
    let mut host = Host::new(core(), [spec]);
    changed(host.create(
        "Ticket",
        &alice(),
        T0,
        "t1",
        json!({"subject": "Card declined", "body": "Refused at checkout."}),
    ));
    host
}

fn hash_of(pulled: &Value, field: &str) -> Value {
    pulled["items"]
        .as_array()
        .expect("items")
        .iter()
        .find(|item| item["field"] == field)
        .map(|item| item["sourceHash"].clone())
        .expect("the field")
}

#[test]
fn the_declaration_computes_its_fields_and_keeps_a_sub_table_of_derivations() {
    let registered = core();
    let declaration = registered.declaration(derived::NAME).expect("registered");
    assert!(declaration.is_computed(derived::FIELDS));
    assert!(declaration.is_computed(derived::STALE));
    let table = declaration
        .find_table(derived::DERIVATIONS)
        .expect("the table");
    assert_eq!(table.key.as_deref(), Some("field"));
    assert!(!table.shared);
    assert_eq!(
        declaration.operation_names(true),
        ["staleDerivations", "settleDerivations", "failedDerivations"]
    );
    assert_eq!(
        declaration.operation_names(false),
        ["applyDerived", "rederive"]
    );
    assert!(
        declaration
            .find_operation("applyDerived")
            .expect("it")
            .writes
    );
    assert!(
        !declaration
            .find_table_operation("staleDerivations")
            .expect("it")
            .writes
    );
}

/// The checks Derived writes by hand are the declaration's configSchema
/// and paramsSchemas, keyword for keyword.
#[test]
fn the_config_and_parameter_checks_are_the_declarations_schemas() {
    let file: Value = serde_json::from_str(derived::DECLARATION).expect("it parses");
    let names = json!({"items": {"minLength": 1, "type": "string"}, "minItems": 1, "type": "array", "uniqueItems": true});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({"additionalProperties": false, "properties": {
            "fields": {"additionalProperties": {"additionalProperties": false, "properties": {
                "from": {"items": {"minLength": 1, "type": "string"}, "maxItems": 16, "minItems": 1, "type": "array", "uniqueItems": true},
                "instruction": {"maxLength": 4000, "minLength": 1, "type": "string"},
                "model": {"maxLength": 200, "minLength": 1, "type": "string"},
                "states": names},
                "required": ["from", "instruction", "model"], "type": "object"},
                "maxProperties": 16, "minProperties": 1, "propertyNames": {"maxLength": 64, "minLength": 1}, "type": "object"},
            "maxAttempts": {"maximum": 10, "minimum": 1, "type": "integer"},
            "permission": {"minLength": 1, "type": "string"}},
            "required": ["fields", "permission"], "type": "object"})
    );
    let params: Vec<Value> = file["operations"]
        .as_array()
        .expect("operations")
        .iter()
        .map(|operation| undescribed(&operation["paramsSchema"]))
        .collect();
    let page = json!({"cursor": {"type": "string"}, "limit": {"maximum": 100, "minimum": 1, "type": "integer"}});
    assert_eq!(params[0]["properties"]["limit"], page["limit"]);
    assert_eq!(
        params[0]["properties"]["field"],
        json!({"minLength": 1, "type": "string"})
    );
    assert_eq!(params[2]["properties"], page);
    assert_eq!(
        params[1]["properties"]["items"]["items"]["required"],
        json!(["id", "field", "sourceHash"])
    );
    assert_eq!(params[1]["properties"]["items"]["maxItems"], json!(100));
    assert_eq!(
        params[3]["properties"]["fields"]["additionalProperties"]["required"],
        json!(["sourceHash", "value"])
    );
    assert_eq!(params[4]["properties"]["fields"], names);

    let core = core();
    assert_eq!(
        shape(
            &core,
            &derived_on(json!({"fields": {}, "keep": 1, "maxAttempts": 11}))
        ),
        [
            "/config: unknown key \"keep\"",
            "/config: must have required property 'permission'",
            "/config/fields: must NOT have fewer than 1 properties",
            "/config/maxAttempts: must be <= 10"
        ]
    );
    assert_eq!(
        shape(
            &core,
            &derived_on(json!({"permission": "p", "fields": {"category": {"from": [], "model": "", "states": ["a", "a"], "x": 1}}}))
        ),
        [
            "/config/fields/category: unknown key \"x\"",
            "/config/fields/category: must have required property 'instruction'",
            "/config/fields/category/from: must NOT have fewer than 1 items",
            "/config/fields/category/model: must NOT have fewer than 1 characters",
            "/config/fields/category/states: must NOT have duplicate items (items ## 1 and 0 are identical)"
        ]
    );
    let mut host = opened(derived_on(config()));
    let invalid = refused(table_op(
        &mut host,
        &deriver(),
        "staleDerivations",
        json!({"limit": 0, "x": 1}),
    ));
    assert_eq!(
        invalid["details"]["issues"],
        json!([
            {"path": "", "message": "must NOT have additional properties: x"},
            {"path": "/limit", "message": "must be >= 1"}
        ])
    );
    let settle = refused(table_op(
        &mut host,
        &deriver(),
        "settleDerivations",
        json!({"items": [{"id": "t1", "field": "category", "sourceHash": "h", "value": 1, "failure": {"message": "x"}},
                         {"id": "t1", "field": "category", "sourceHash": "h", "value": 1}]}),
    ));
    assert_eq!(
        settle["details"]["issues"],
        json!([
            {"path": "/items/0", "message": "holds a value or a failure, and not both"},
            {"path": "/items/1", "message": "settles category of t1 a second time in one call"}
        ])
    );
}

/// The config is held to the type: own fields, no field derived from
/// itself or another derived field, none Constants keeps, states the
/// Workflow's.
#[test]
fn the_config_is_held_to_the_types_own_fields_constants_and_workflow() {
    let core = core();
    let entry = |name: &str, from: Value| {
        derived_on(
            json!({"permission": "p", "fields": {name: {"from": from, "instruction": "i", "model": "m"}}}),
        )
    };
    assert_eq!(
        rule(&core, &entry("title", json!(["body"])), USE),
        "fields: Ticket has no field title"
    );
    assert_eq!(
        rule(&core, &entry("summary", json!(["summary"])), USE),
        "fields: summary is derived from itself"
    );
    assert_eq!(
        rule(&core, &entry("summary", json!(["notes"])), USE),
        "fields: summary is derived from notes, which Ticket does not have"
    );
    let both = derived_on(json!({"permission": "p", "fields": {
        "summary": {"from": ["body"], "instruction": "i", "model": "m"},
        "category": {"from": ["summary"], "instruction": "i", "model": "m"}}}));
    assert_eq!(
        rule(&core, &both, USE),
        "fields: category is derived from summary, which is derived too"
    );
    let kept = ticket(vec![
        UseSpec::on_type("Constants", json!({"fields": ["summary"]})),
        UseSpec::on_type(
            USE,
            json!({"permission": "p", "fields": {"summary": {"from": ["body"], "instruction": "i", "model": "m"}}}),
        ),
    ]);
    assert_eq!(
        rule(&core, &kept, USE),
        "fields: summary is a field Constants keeps, which no model may write after the create"
    );
    let stated = |states: Value| json!({"permission": "p", "fields": {"summary": {"from": ["body"], "instruction": "i", "model": "m", "states": states}}});
    assert_eq!(
        rule(&core, &derived_on(stated(json!(["new"]))), USE),
        "fields: summary names states, and the type has no Workflow"
    );
    let flow = UseSpec::on_type(
        "Workflow",
        json!({"states": ["new", "triage"], "transitions": [{"from": "new", "to": "triage"}]}),
    );
    assert_eq!(
        rule(
            &core,
            &ticket(vec![
                flow.clone(),
                UseSpec::on_type(USE, stated(json!(["done"])))
            ]),
            USE
        ),
        "fields: summary: state \"done\" is not a state of the type's Workflow (new, triage)"
    );
    assert!(define(
        &core,
        &ticket(vec![flow, UseSpec::on_type(USE, stated(json!(["new"])))])
    )
    .is_ok());
}

/// A deriver pulls the stale fields, each with its inputs, instruction,
/// model, output and hash, and settles values that land as own fields:
/// the read shows each value's settle, and nothing is stale.
#[test]
fn a_deriver_pulls_the_stale_fields_and_settles_values_by_hash() {
    let mut host = opened(derived_on(config()));
    assert_eq!(
        read(&host, "t1"),
        json!({"fields": {}, "stale": ["category", "summary"]})
    );
    let pulled = stale(&mut host, json!({}));
    assert_eq!(
        pulled,
        json!({"items": [
            {"id": "t1", "field": "category", "sourceHash": "72c4932fe182030ceb732c6ca4718bfaa443317c233d1d4a01602f949a066861",
             "attempt": 1, "instruction": "Classify the request.", "model": "standard",
             "inputs": {"subject": "Card declined", "body": "Refused at checkout."}, "output": category()},
            {"id": "t1", "field": "summary", "sourceHash": "665ead3d12a41a48205537b35e0681088e50f2200dc9ea6c0e4b34990822d1af",
             "attempt": 1, "instruction": "Summarize it in one sentence.", "model": "simple",
             "inputs": {"body": "Refused at checkout."}, "output": text()}
        ], "next": null})
    );
    let settled = settle(
        &mut host,
        json!([
            {"id": "t1", "field": "category", "sourceHash": hash_of(&pulled, "category"), "value": "billing", "confidence": 0.9},
            {"id": "t1", "field": "summary", "sourceHash": hash_of(&pulled, "summary"), "value": "A card is refused."}
        ]),
    );
    assert_eq!(settled, json!({"settled": 2, "failed": 0, "skipped": []}));
    let row = host.row("Ticket", "t1");
    assert_eq!(row.own["category"], json!("billing"));
    assert_eq!(row.own["summary"], json!("A card is refused."));
    assert_eq!(
        read(&host, "t1")["fields"]["category"],
        json!({"sourceHash": hash_of(&pulled, "category"), "model": "standard", "settledAt": T0 + 10, "settledBy": "deriver", "confidence": 0.9})
    );
    assert_eq!(read(&host, "t1")["stale"], json!([]));
    assert_eq!(
        stale(&mut host, json!({})),
        json!({"items": [], "next": null})
    );
}

/// A caller's write of a derived field overrides it: Derived stops
/// deriving it, a settle of it is skipped and applyDerived refused, until
/// rederive asks for it again; a moved input moves the other's hash.
#[test]
fn a_callers_write_overrides_a_field_until_rederive() {
    let mut host = opened(derived_on(config()));
    let pulled = stale(&mut host, json!({}));
    changed(host.update(
        "Ticket",
        &alice(),
        T0 + 5,
        "t1",
        json!({"category": "other", "body": "Refused twice."}),
    ));
    assert_eq!(
        read(&host, "t1"),
        json!({"fields": {"category": {"overridden": {"by": "alice", "at": T0 + 5}}}, "stale": ["summary"]})
    );
    let skipped = settle(
        &mut host,
        json!([
            {"id": "t1", "field": "category", "sourceHash": hash_of(&pulled, "category"), "value": "bug"},
            {"id": "t1", "field": "summary", "sourceHash": hash_of(&pulled, "summary"), "value": "x"},
            {"id": "t9", "field": "summary", "sourceHash": "h", "value": "x"},
            {"id": "t1", "field": "body", "sourceHash": "h", "value": "x"}
        ]),
    );
    assert_eq!(
        skipped["skipped"],
        json!([
            {"id": "t1", "field": "category", "reason": "overridden"},
            {"id": "t1", "field": "summary", "reason": "moved"},
            {"id": "t9", "field": "summary", "reason": "not_found"},
            {"id": "t1", "field": "body", "reason": "not_derived"}
        ])
    );
    let (code, _) = veto_of(host.invoke(
        "Ticket",
        &deriver(),
        T0 + 10,
        "t1",
        USE,
        "applyDerived",
        json!({"fields": {"category": {"sourceHash": hash_of(&pulled, "category"), "value": "bug"}}}),
    ));
    assert_eq!(code, "overridden");
    let rederived = result(host.invoke(
        "Ticket",
        &alice(),
        T0 + 20,
        "t1",
        USE,
        "rederive",
        json!({"fields": ["category"]}),
    ));
    assert_eq!(rederived, json!({"stale": ["category", "summary"]}));
    let again = stale(&mut host, json!({"field": "category"}));
    assert_eq!(
        again["items"][0]["inputs"],
        json!({"subject": "Card declined", "body": "Refused twice."})
    );
    let (code, _) = veto_of(host.invoke(
        "Ticket",
        &alice(),
        T0,
        "t1",
        USE,
        "rederive",
        json!({"fields": ["body"]}),
    ));
    assert_eq!(code, "not_derived");
}

/// A failure, a value the field's schema refuses and another use's veto
/// count attempts and write no own field; after maxAttempts the field
/// leaves the pull for failedDerivations, until its inputs move.
#[test]
fn failures_refused_values_and_vetoes_count_attempts_into_failed_derivations() {
    let frozen = ticket(vec![
        UseSpec::on_type(
            "Workflow",
            json!({"states": ["new", "closed"], "transitions": [{"from": "new", "to": "closed"}]}),
        ),
        UseSpec::on_type(
            "StateRules",
            json!({"states": {"new": {"frozen": ["summary"]}}}),
        ),
        UseSpec::on_type(USE, config()),
    ]);
    let mut host = opened(frozen);
    let pulled = stale(&mut host, json!({}));
    let (category, summary) = (hash_of(&pulled, "category"), hash_of(&pulled, "summary"));
    let refused_value = settle(
        &mut host,
        json!([{"id": "t1", "field": "category", "sourceHash": category, "value": "payments"}]),
    );
    assert_eq!(
        refused_value,
        json!({"settled": 0, "failed": 0, "skipped": [{"id": "t1", "field": "category", "reason": "invalid",
            "issues": [{"path": "category", "rule": "enum", "message": "Category must be one of the allowed values."}]}]})
    );
    let vetoed = settle(
        &mut host,
        json!([{"id": "t1", "field": "summary", "sourceHash": summary, "value": "x"}]),
    );
    assert_eq!(vetoed["skipped"][0]["reason"], json!("vetoed"));
    assert_eq!(vetoed["skipped"][0]["behavior"], json!("StateRules"));
    let failed = settle(
        &mut host,
        json!([{"id": "t1", "field": "category", "sourceHash": category, "failure": {"class": "timeout", "message": "too slow"}}]),
    );
    assert_eq!(failed, json!({"settled": 0, "failed": 1, "skipped": []}));
    assert!(host.row("Ticket", "t1").own.get("category").is_none());
    // category is out of its two attempts; summary has one left.
    let left = stale(&mut host, json!({}));
    assert_eq!(left["items"].as_array().map(Vec::len), Some(1));
    assert_eq!(left["items"][0]["field"], json!("summary"));
    assert_eq!(left["items"][0]["attempt"], json!(2));
    assert_eq!(
        result(table_op(
            &mut host,
            &deriver(),
            "failedDerivations",
            json!({})
        )),
        json!({"items": [{"id": "t1", "field": "category", "attempts": 2, "failure": {"class": "timeout", "message": "too slow"}}], "next": null})
    );
    // A moved input gives the field its attempts back.
    changed(host.update(
        "Ticket",
        &alice(),
        T0 + 30,
        "t1",
        json!({"subject": "Card refused"}),
    ));
    let back = stale(&mut host, json!({"field": "category"}));
    assert_eq!(back["items"][0]["attempt"], json!(1));
}

/// Only the use's own settle counts an attempt through applyDerived: a
/// caller's entry that does is refused as its paramsSchema refuses it.
#[test]
fn a_callers_attempt_entry_is_refused() {
    let mut host = opened(derived_on(config()));
    let pulled = stale(&mut host, json!({}));
    let refusal = refused(host.invoke(
        "Ticket",
        &deriver(),
        T0 + 10,
        "t1",
        USE,
        "applyDerived",
        json!({"fields": {"category": {"sourceHash": hash_of(&pulled, "category"), "attempt": {}}}}),
    ));
    assert_eq!(refusal["code"], json!("invalid_argument"));
    assert_eq!(
        refusal["details"]["issues"],
        json!([{"path": "/fields/category", "message": "must NOT have additional properties: attempt"}])
    );
}

/// A field with states is pulled and applied only while the status is
/// one of them.
#[test]
fn a_field_with_states_is_derived_only_in_them() {
    let spec = ticket(vec![
        UseSpec::on_type(
            "Workflow",
            json!({"states": ["new", "closed"], "transitions": [{"from": "new", "to": "closed"}]}),
        ),
        UseSpec::on_type(
            USE,
            json!({"permission": "tickets.derive", "fields": {"summary": {"from": ["body"], "instruction": "i", "model": "m", "states": ["closed"]}}}),
        ),
    ]);
    let mut host = opened(spec);
    assert_eq!(stale(&mut host, json!({}))["items"], json!([]));
    changed(host.invoke(
        "Ticket",
        &alice(),
        T0 + 1,
        "t1",
        "Workflow",
        "transition",
        json!({"to": "closed"}),
    ));
    let pulled = stale(&mut host, json!({}));
    assert_eq!(pulled["items"][0]["field"], json!("summary"));
}

/// Each operation of the table needs the config's permission, and so does
/// applyDerived.
#[test]
fn a_deriver_needs_the_configs_permission() {
    let mut host = opened(derived_on(config()));
    for (operation, params, what) in [
        ("staleDerivations", json!({}), "pull"),
        ("failedDerivations", json!({}), "list"),
        ("settleDerivations", json!({"items": []}), "settle"),
    ] {
        let refusal = refused(table_op(&mut host, &alice(), operation, params));
        assert_eq!(refusal["code"], json!("forbidden"));
        assert_eq!(
            refusal["message"],
            json!(format!(
                "alice may not {what} the derived fields of Ticket: it needs tickets.derive"
            ))
        );
    }
    let refusal = refused(host.invoke(
        "Ticket",
        &alice(),
        T0,
        "t1",
        USE,
        "applyDerived",
        json!({"fields": {"summary": {"sourceHash": "h", "value": "x"}}}),
    ));
    assert_eq!(refusal["code"], json!("forbidden"));
}

/// A pull lists at most limit fields, and its cursor resumes after the row
/// and the field it names, within a row too.
#[test]
fn a_pull_pages_by_row_and_field() {
    let mut host = opened(derived_on(config()));
    changed(host.create(
        "Ticket",
        &alice(),
        T0,
        "t2",
        json!({"subject": "Login", "body": "Locked out."}),
    ));
    let first = stale(&mut host, json!({"limit": 3}));
    let listed: Vec<(Value, Value)> = first["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|item| (item["id"].clone(), item["field"].clone()))
        .collect();
    assert_eq!(
        listed,
        [
            (json!("t1"), json!("category")),
            (json!("t1"), json!("summary")),
            (json!("t2"), json!("category"))
        ]
    );
    let next = first["next"].as_str().expect("a cursor").to_owned();
    let second = stale(&mut host, json!({"limit": 3, "cursor": next}));
    assert_eq!(second["items"].as_array().map(Vec::len), Some(1));
    assert_eq!(
        (
            second["items"][0]["id"].clone(),
            second["items"][0]["field"].clone()
        ),
        (json!("t2"), json!("summary"))
    );
    assert_eq!(second["next"], Value::Null);
    let bogus = refused(table_op(
        &mut host,
        &deriver(),
        "staleDerivations",
        json!({"cursor": "bogus"}),
    ));
    assert_eq!(
        bogus["details"]["issues"],
        json!([{"path": "/cursor", "message": "is not a cursor this operation returned"}])
    );
}

/// A value a row holds when the use first reaches it, on a row written
/// before the table composed Derived, counts as overridden by existing;
/// rederive starts the field's derivation on such a row.
#[test]
fn a_value_a_row_held_before_the_use_counts_as_existing() {
    let mut host = Host::new(core(), [ticket(Vec::new())]);
    changed(host.create(
        "Ticket",
        &alice(),
        T0,
        "t1",
        json!({"subject": "Card declined", "body": "Refused.", "category": "bug"}),
    ));
    host.tables
        .specs
        .insert("Ticket".into(), derived_on(config()));
    assert_eq!(stale(&mut host, json!({}))["items"], json!([]));
    changed(host.update(
        "Ticket",
        &alice(),
        T0 + 5,
        "t1",
        json!({"subject": "Card refused"}),
    ));
    assert_eq!(
        read(&host, "t1"),
        json!({"fields": {"category": {"overridden": {"by": "existing", "at": T0 + 5}}}, "stale": ["summary"]})
    );
    let rederived =
        result(host.invoke("Ticket", &alice(), T0 + 6, "t1", USE, "rederive", json!({})));
    assert_eq!(rederived, json!({"stale": ["category", "summary"]}));
}

/// Any config may change and Derived may be added to a table with rows;
/// removed from one, its derivations would stay behind.
#[test]
fn the_config_may_change_and_derived_stays_on_a_table_with_rows() {
    let host = opened(derived_on(config()));
    let mut changed_config = config();
    changed_config["fields"]["summary"]["instruction"] = json!("Say it shorter.");
    let publish = |from: TypeSpec, to: TypeSpec| {
        let command =
            superschematic_rules::Command::new(alice(), T0, Action::Publish { from: Some(from) });
        let mut tables = tables::Tables::default();
        tables.rows = host.tables.rows.clone();
        host.core.drive(&to, &command, &mut tables).into_result()
    };
    assert!(publish(derived_on(config()), derived_on(changed_config)).is_ok());
    assert!(publish(ticket(Vec::new()), derived_on(config())).is_ok());
    let removed = publish(derived_on(config()), ticket(Vec::new())).expect_err("refused");
    assert!(
        removed.message.contains(
            "the derivations its instances have would stay behind with nothing to delete them"
        ),
        "{}",
        removed.message
    );
}

/// A settle on a row another principal leases: Lease lets the settle's
/// call of applyDerived through, a request of Derived's own code, and
/// holds the update of the own fields it makes, so the value is skipped as
/// Lease's veto and counts an attempt, and a failure, which writes no own
/// field, counts one too.
#[test]
fn a_leased_row_vetoes_a_settled_value_and_counts_its_attempt() {
    let mut host = opened(ticket(vec![
        UseSpec::on_type("Lease", json!({})),
        UseSpec::on_type(USE, config()),
    ]));
    let wren = Caller::new("wren", Vec::<String>::new());
    changed(host.invoke("Ticket", &wren, T0 + 1, "t1", "Lease", "acquire", json!({})));
    let pulled = stale(&mut host, json!({}));
    let settled = settle(
        &mut host,
        json!([
            {"id": "t1", "field": "category", "sourceHash": hash_of(&pulled, "category"), "value": "bug"},
            {"id": "t1", "field": "summary", "sourceHash": hash_of(&pulled, "summary"), "failure": {"message": "too slow"}}
        ]),
    );
    assert_eq!(
        (settled["settled"].clone(), settled["failed"].clone()),
        (json!(0), json!(1))
    );
    assert_eq!(
        (
            settled["skipped"][0]["field"].clone(),
            settled["skipped"][0]["reason"].clone(),
            settled["skipped"][0]["behavior"].clone(),
            settled["skipped"][0]["code"].clone()
        ),
        (
            json!("category"),
            json!("vetoed"),
            json!("Lease"),
            json!("held_by_another")
        )
    );
    assert!(host.row("Ticket", "t1").own.get("category").is_none());
    let again = stale(&mut host, json!({}));
    let attempts: Vec<Value> = again["items"]
        .as_array()
        .expect("items")
        .iter()
        .map(|item| item["attempt"].clone())
        .collect();
    assert_eq!(attempts, [json!(2), json!(2)]);
}
