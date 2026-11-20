//! Numbering on the pure core (D56, D103 step 4), through the core's public
//! API over a source in memory that keeps rows and the uses' shared tables:
//! the declaration file's layout members and schemas; the config held to its
//! tokens, its reset and a to-one relation of the table; a create's number
//! printed by the format in the year of its time; a count per table, per
//! relation target and per year, kept in the shared table and outliving the
//! rows it numbered; a refused create that takes no number; two creates that
//! read one count, held to its version so the second runs again; assignNumber
//! in the year of the row's createdAt and on a numbered row, writing nothing;
//! the number no write of the row sets; the config held while the table has
//! rows and a count made under another config started again; and two
//! Numbering fields on one table, each counting apart.

mod support;

use serde_json::{json, Value};
use superschematic_rules::{
    Action, Caller, Command, FieldSpec, Outcome, SharedRowChange, TypeSpec, UseSpec,
};
use superschematic_rules_behaviors::{core, numbering};
use support::{caller, changed, define, object, refused, result, shape, undescribed, Host};

// 2026-03-01T00:00:00Z and 2027-01-01T00:00:00Z, in epoch milliseconds.
const MARCH_2026: i64 = 1_772_323_200_000;
const JANUARY_2027: i64 = 1_798_761_600_000;

// An invoice as a table holds it: its number on a field, a customer by a
// to-one relation, a title, and its createdAt audit field.
fn invoice(config: Value) -> TypeSpec {
    TypeSpec::new("Invoice")
        .with(UseSpec::on_field("number", "Numbering", config))
        .with_field(FieldSpec::new("title", "string"))
        .with_field(FieldSpec::relation(
            "customerId",
            "Identity.UUID",
            "customer",
            "Customer",
        ))
        .with_field(FieldSpec::date_time("createdAt", "Temporal.DateTime"))
}

fn alice() -> Caller {
    caller("alice", &[])
}

fn create_at(host: &mut Host, now: i64, key: &str, own: Value) -> Outcome {
    host.run(
        &alice(),
        now,
        Action::Create {
            id: Some(key.to_owned()),
            own: object(own),
            params: Default::default(),
        },
    )
}

// number is the row's number and label, as its owned fields hold them.
fn number(host: &Host, key: &str) -> Value {
    let row = host.row(key);
    json!({
        "seq": row.owned("number", "seq").cloned().unwrap_or(Value::Null),
        "label": row.owned("number", "label").cloned().unwrap_or(Value::Null),
    })
}

/// The declaration file names the label its value field, optional, the seq
/// a Generic.Int64, and the shared table counts, kept for the table as a
/// whole (D103's step 4); the core reads the table shared.
#[test]
fn the_declaration_names_its_value_and_its_shared_counts() {
    let file: Value = serde_json::from_str(numbering::DECLARATION).expect("it parses");
    assert_eq!(file["value"], "label");
    assert_eq!(
        file["fields"],
        json!([
            {"name": "seq", "description": file["fields"][0]["description"], "type": "Generic.Int64"},
            {"name": "label", "description": file["fields"][1]["description"], "type": "string", "optional": true}
        ])
    );
    let counts = &file["tables"][0];
    assert_eq!(counts["name"], "counts");
    assert_eq!(counts["key"], "scope");
    assert_eq!(counts["scope"], "schema");
    let fields: Vec<(&str, &str)> = counts["fields"]
        .as_array()
        .expect("fields")
        .iter()
        .map(|field| {
            (
                field["name"].as_str().unwrap(),
                field["type"].as_str().unwrap(),
            )
        })
        .collect();
    assert_eq!(
        fields,
        [
            ("scope", "string"),
            ("last", "Generic.Int64"),
            ("counting", "string")
        ]
    );
    let declared = core().declaration("Numbering").expect("registered").clone();
    assert_eq!(declared.value.as_deref(), Some("label"));
    assert!(declared.find_table("counts").expect("counts").shared);
    assert_eq!(
        declared.find_operation("assignNumber").map(|op| op.writes),
        Some(true)
    );
}

/// The checks Numbering writes by hand are the declaration's configSchema
/// and assignNumber's paramsSchema, keyword for keyword.
#[test]
fn the_config_and_parameter_checks_are_the_declarations_schemas() {
    let file: Value = serde_json::from_str(numbering::DECLARATION).expect("it parses");
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({
            "type": "object",
            "additionalProperties": false,
            "required": ["format"],
            "properties": {
                "format": {"type": "string", "minLength": 1, "maxLength": 128},
                "scope": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["link"],
                    "properties": {"link": {"type": "string", "pattern": "^[a-z][A-Za-z0-9]{0,63}$"}}
                },
                "reset": {"enum": ["yearly"]}
            }
        })
    );
    assert_eq!(
        undescribed(&file["operations"][0]["paramsSchema"]),
        json!({"type": "object", "additionalProperties": false, "properties": {}})
    );
    let core = core();
    assert_eq!(
        shape(&core, &invoice(json!({"start": 1}))),
        [
            "/config: unknown key \"start\"",
            "/config: must have required property 'format'"
        ]
    );
    assert_eq!(
        shape(
            &core,
            &invoice(json!({"format": "", "reset": "daily", "scope": {"to": "x"}}))
        ),
        [
            "/config/format: must NOT have fewer than 1 characters",
            "/config/reset: must be one of \"yearly\"",
            "/config/scope: unknown key \"to\"",
            "/config/scope: must have required property 'link'"
        ]
    );
    assert_eq!(
        shape(
            &core,
            &invoice(json!({"format": "{seq}", "scope": {"link": "Customer"}}))
        ),
        ["/config/scope/link: must match pattern \"^[a-z][A-Za-z0-9]{0,63}$\""]
    );
    let mut host = Host::new(core, invoice(json!({"format": "{seq}"})));
    changed(host.create(&alice(), "i1", json!({"title": "a"})));
    assert_eq!(
        refused(host.invoke(&alice(), "i1", "number", "assignNumber", json!({"x": 1})))["details"],
        json!({"use": "number", "issues": [
            {"path": "", "message": "must NOT have additional properties: x"}
        ]})
    );
}

/// A define refuses a brace outside its tokens, a {seq} not there once, a
/// yearly reset without the year, and a scope that names no to-one relation
/// of the table, each in the engine's words but the last, which names the
/// table's relations where the engine named its Links.
#[test]
fn a_define_refuses_a_format_a_reset_and_a_scope_it_cannot_count_by() {
    let core = core();
    for (format, reason) in [
        ("INV-{n}", "format \"INV-{n}\" has {n}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}"),
        ("{seq:04}", "format \"{seq:04}\" has {seq:04}, which is not one of its tokens: {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}"),
        ("INV-{seq", "format \"INV-{seq\" has a \"{\" that is not part of a token; its tokens are {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}"),
        ("INV}{seq}", "format \"INV}{seq}\" has a \"}\" that is not part of a token; its tokens are {seq}, {seq:N} (N from 1 to 10), {yyyy} and {yy}"),
        ("INV-{yyyy}", "format \"INV-{yyyy}\" has no {seq}: it prints the number with {seq}, or {seq:N} padded to N digits, once"),
        ("{seq}-{seq:3}", "format \"{seq}-{seq:3}\" has the number 2 times: it prints it with {seq} or {seq:N} once, so two numbers never print one label"),
    ] {
        let refusal = define(&core, &invoice(json!({"format": format}))).expect_err("refused");
        assert_eq!(refusal.message, format!("type Invoice: field number: behavior Numbering config: {reason}"), "{format}");
    }
    let message = |spec: &TypeSpec| define(&core, spec).expect_err("refused").message;
    assert_eq!(
        message(&invoice(json!({"format": "INV-{seq}", "reset": "yearly"}))),
        "type Invoice: field number: behavior Numbering config: reset yearly counts each year from 1, so format \"INV-{seq}\" prints the year, {yyyy} or {yy}, or two years would print one label"
    );
    assert_eq!(
        message(&invoice(json!({"format": "{seq}", "scope": {"link": "referrer"}}))),
        "type Invoice: field number: behavior Numbering config: scope.link referrer is not a to-one relation of Invoice (its to-one relations: customer), so a row has no one target to count by"
    );
    let bare = TypeSpec::new("Ticket").with(UseSpec::on_field(
        "number",
        "Numbering",
        json!({"format": "T-{seq}", "scope": {"link": "queue"}}),
    ));
    assert_eq!(
        message(&bare),
        "type Ticket: field number: behavior Numbering config: scope.link queue is not a to-one relation of Ticket (it has none), so a row has no one target to count by"
    );
    assert!(define(
        &core,
        &invoice(json!({"format": "INV-{yyyy}-{seq:4}", "scope": {"link": "customer"}, "reset": "yearly"}))
    )
    .is_ok());
}

/// A create numbers its row from its count, the format padding the number
/// and printing a longer one in full, in the UTC year of its time; the
/// count of the whole table is the shared row `*`, inserted at the first
/// create and updated held to its version at each after it.
#[test]
fn a_create_takes_the_next_number_of_its_count() {
    let mut host = Host::new(core(), invoice(json!({"format": "INV-{yyyy}-{seq:2}"})));
    let first = changed(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a"}),
    ));
    assert_eq!(
        number(&host, "i1"),
        json!({"seq": 1, "label": "INV-2026-01"})
    );
    assert_eq!(
        serde_json::to_value(&first.shared_rows).unwrap(),
        json!([{"type": "Invoice", "useId": "number", "table": "counts", "key": "*",
                "change": {"kind": "insert", "values": {"last": 1, "counting": "{\"format\":\"INV-{yyyy}-{seq:2}\",\"reset\":null,\"scope\":null}"}}}])
    );
    let second = changed(create_at(
        &mut host,
        JANUARY_2027,
        "i2",
        json!({"title": "b"}),
    ));
    assert_eq!(
        number(&host, "i2"),
        json!({"seq": 2, "label": "INV-2027-02"})
    );
    assert!(matches!(
        &second.shared_rows[0].change,
        SharedRowChange::Update { expected_version: 1, values } if values["last"] == 2
    ));
    for key in 3..=100 {
        changed(create_at(
            &mut host,
            MARCH_2026,
            &format!("i{key}"),
            json!({"title": "c"}),
        ));
    }
    assert_eq!(
        number(&host, "i100"),
        json!({"seq": 100, "label": "INV-2026-100"})
    );
}

/// A count runs per target of the scope's relation and per UTC year, each a
/// shared row of its own (`c1/2026`), so a label is unique within its scope
/// alone; moving the relation keeps the number; and a delete leaves the
/// count where it was, a gap.
#[test]
fn a_count_runs_per_target_and_year_and_a_delete_leaves_a_gap() {
    let mut host = Host::new(
        core(),
        invoice(
            json!({"format": "C-{yy}-{seq}", "scope": {"link": "customer"}, "reset": "yearly"}),
        ),
    );
    let at = |host: &mut Host, now, key: &str, customer: &str| {
        changed(create_at(
            host,
            now,
            key,
            json!({"title": key, "customerId": customer}),
        ));
        number(host, key)["label"].clone()
    };
    assert_eq!(at(&mut host, MARCH_2026, "a1", "c1"), "C-26-1");
    assert_eq!(at(&mut host, MARCH_2026, "a2", "c1"), "C-26-2");
    assert_eq!(at(&mut host, MARCH_2026, "b1", "c2"), "C-26-1");
    assert_eq!(at(&mut host, JANUARY_2027, "a3", "c1"), "C-27-1");
    let scopes: Vec<&String> = host.memory.shared.keys().map(|(_, _, key)| key).collect();
    assert_eq!(scopes, ["c1/2026", "c1/2027", "c2/2026"]);
    changed(host.update(&alice(), "a2", json!({"customerId": "c2"})));
    assert_eq!(number(&host, "a2"), json!({"seq": 2, "label": "C-26-2"}));
    changed(host.delete(&alice(), "a2"));
    assert_eq!(at(&mut host, MARCH_2026, "a4", "c1"), "C-26-3");
}

/// A refused create takes no number: a create without its scope's target is
/// refused by Numbering's validation and writes no count, and the next create
/// takes the number it would have.
#[test]
fn a_refused_create_takes_no_number() {
    let mut host = Host::new(
        core(),
        invoice(json!({"format": "INV-{seq}", "scope": {"link": "customer"}})),
    );
    let refusal = refused(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a"}),
    ));
    assert_eq!(refusal["code"], "invalid_instance");
    assert_eq!(
        refusal["details"]["issues"],
        json!([{"path": "customerId", "rule": "required", "message": "customerId is required: Numbering counts Invoice's numbers per customer"}])
    );
    assert!(host.memory.shared.is_empty());
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i2",
        json!({"title": "b", "customerId": "c1"}),
    ));
    assert_eq!(number(&host, "i2"), json!({"seq": 1, "label": "INV-1"}));
}

/// Two creates that read one count at once write it held to the version
/// they read: the first moves it on, so the second's fence no longer holds
/// and its host runs it again from fresh state, when it takes the next
/// number. No two rows take one number, on any dialect, with no lock.
#[test]
fn two_creates_that_read_one_count_are_held_to_its_version() {
    let core = core();
    let spec = invoice(json!({"format": "{seq}"}));
    let mut host = Host::new(core, spec);
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a"}),
    ));
    let command = |key: &str| {
        Command::new(
            alice(),
            MARCH_2026,
            Action::Create {
                id: Some(key.to_owned()),
                own: object(json!({"title": key})),
                params: Default::default(),
            },
        )
        .with_request(key)
    };
    let read_both = |host: &mut Host| {
        let a = host
            .core
            .drive(&host.spec, &command("i2"), &mut host.memory);
        let b = host
            .core
            .drive(&host.spec, &command("i3"), &mut host.memory);
        (changed(a), changed(b))
    };
    let (a, b) = read_both(&mut host);
    for changes in [&a, &b] {
        assert_eq!(changes.owned("number", "seq"), Some(&json!(2)));
        assert!(matches!(
            &changes.shared_rows[0].change,
            SharedRowChange::Update {
                expected_version: 1,
                ..
            }
        ));
    }
    // The first applies; the second's fence would not hold, so its host
    // runs it again, and it takes 3.
    changed(host.run(
        &alice(),
        MARCH_2026,
        Action::Create {
            id: Some("i2".into()),
            own: object(json!({"title": "i2"})),
            params: Default::default(),
        },
    ));
    assert_eq!(host.memory.shared.values().next().unwrap().version, 2);
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i3",
        json!({"title": "i3"}),
    ));
    assert_eq!(number(&host, "i3"), json!({"seq": 3, "label": "3"}));
}

/// assignNumber numbers a row made before the table composed Numbering, in
/// the UTC year of its createdAt and that year's count, as its create would
/// have; on a numbered row it writes nothing and returns its number.
#[test]
fn assign_number_numbers_a_row_made_before_and_changes_nothing_after() {
    let config = json!({"format": "INV-{yyyy}-{seq:3}", "reset": "yearly"});
    let mut host = Host::new(core(), invoice(config.clone()));
    // A row made in 2025, before the table numbered: it has no number.
    host.memory.rows.insert(
        "o1".into(),
        superschematic_rules::Row {
            own: object(json!({"title": "old", "createdAt": 1_740_000_000_000_i64})),
            ..Default::default()
        },
    );
    host.memory.rows.get_mut("o1").unwrap().version = 1;
    changed(create_at(
        &mut host,
        MARCH_2026,
        "o4",
        json!({"title": "new", "createdAt": MARCH_2026}),
    ));
    assert_eq!(
        number(&host, "o4"),
        json!({"seq": 1, "label": "INV-2026-001"})
    );
    assert_eq!(
        result(host.invoke(&alice(), "o1", "number", "assignNumber", json!({}))),
        json!({"seq": 1, "label": "INV-2025-001"})
    );
    assert_eq!(
        number(&host, "o1"),
        json!({"seq": 1, "label": "INV-2025-001"})
    );
    let again = changed(host.invoke(&alice(), "o4", "number", "assignNumber", json!({})));
    assert_eq!(again.result, json!({"seq": 1, "label": "INV-2026-001"}));
    assert!(again.rows.is_empty() && again.shared_rows.is_empty());
    // An ISO createdAt reads as its instant.
    host.memory.rows.insert(
        "o2".into(),
        superschematic_rules::Row {
            own: object(json!({"title": "old", "createdAt": "2025-06-01T00:00:00.000Z"})),
            version: 1,
            ..Default::default()
        },
    );
    assert_eq!(
        result(host.invoke(&alice(), "o2", "number", "assignNumber", json!({})))["label"],
        "INV-2025-002"
    );
}

/// Nothing changes a number once set: an update that names the field is
/// refused by the core, as every field anchor's own write is.
#[test]
fn no_write_of_the_row_sets_its_number() {
    let mut host = Host::new(core(), invoice(json!({"format": "INV-{seq}"})));
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a"}),
    ));
    let refusal = refused(host.update(&alice(), "i1", json!({"number": "INV-9"})));
    assert_eq!(refusal["code"], "invalid_instance");
    changed(host.update(&alice(), "i1", json!({"title": "b"})));
    assert_eq!(number(&host, "i1"), json!({"seq": 1, "label": "INV-1"}));
}

/// While the table has rows, the format, the scope and the reset stay, and
/// Numbering cannot be removed; with none, any change is allowed, and a
/// count made under another config starts again at 1.
#[test]
fn the_config_stays_while_there_are_rows_and_a_new_one_counts_again() {
    let core = core();
    let before = invoice(json!({"format": "INV-{yyyy}-{seq}", "scope": {"link": "customer"}}));
    let after = invoice(json!({"format": "N-{seq:2}", "scope": {"link": "customer"}}));
    let mut host = Host::new(core, before.clone());
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a", "customerId": "c1"}),
    ));
    let publish = |host: &mut Host, spec: &TypeSpec| {
        let command = Command::new(
            alice(),
            MARCH_2026,
            Action::Publish {
                from: Some(before.clone()),
            },
        );
        host.core
            .drive(spec, &command, &mut host.memory)
            .into_result()
    };
    let refusal = publish(&mut host, &after).expect_err("refused");
    assert_eq!(refusal.code.as_str(), "incompatible_change");
    assert!(refusal.message.ends_with(
        "the instances were numbered under the format, the scope and the reset, which stay while there are instances; the new config changes the format from \"INV-{yyyy}-{seq}\" to \"N-{seq:2}\""
    ));
    let removed = TypeSpec::new("Invoice").with_field(FieldSpec::new("title", "string"));
    let refusal = publish(&mut host, &removed).expect_err("refused");
    assert!(refusal.message.contains(
        "the instances would lose their numbers, and Numbering added again would count from 1, printing labels they hold"
    ));
    changed(host.delete(&alice(), "i1"));
    publish(&mut host, &after).expect("allowed with no rows");
    host.spec = after;
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i2",
        json!({"title": "b", "customerId": "c1"}),
    ));
    assert_eq!(number(&host, "i2"), json!({"seq": 1, "label": "N-01"}));
}

/// Two Numbering fields on one table keep their counts apart, each its own
/// use's shared rows.
#[test]
fn two_numbering_fields_count_apart() {
    let spec = invoice(json!({"format": "INV-{seq}"})).with(UseSpec::on_field(
        "reference",
        "Numbering",
        json!({"format": "R{seq:3}"}),
    ));
    let mut host = Host::new(core(), spec);
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i1",
        json!({"title": "a"}),
    ));
    changed(create_at(
        &mut host,
        MARCH_2026,
        "i2",
        json!({"title": "b"}),
    ));
    let row = host.row("i2");
    assert_eq!(row.owned("number", "label"), Some(&json!("INV-2")));
    assert_eq!(row.owned("reference", "label"), Some(&json!("R002")));
    let uses: Vec<&String> = host
        .memory
        .shared
        .keys()
        .map(|(use_id, ..)| use_id)
        .collect();
    assert_eq!(uses, ["number", "reference"]);
}
