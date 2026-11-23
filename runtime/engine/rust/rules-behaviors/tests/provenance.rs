//! Provenance on the pure core (D73, D103 step 4), through the core's
//! public API over a host of several types in memory: the declaration's
//! owned inReview, computed fields and sub-table; the config held to the
//! file's schemas and to the type; a caller's write and Derived's recorded,
//! with the confidence Derived's update attributed; review below the
//! threshold, accept, reject and an edit over a proposal; inReview kept as
//! a column a list filters on; the gate on a move of the type's Workflow;
//! a write acting for a subject (D77); and the config change.

mod support;
mod tables;

use serde_json::{json, Value};
use superschematic_rules::{Action, Caller, FieldSpec, Outcome, TypeSpec, UseSpec};
use superschematic_rules_behaviors::{core, provenance};
use support::{changed, refused, result, rule, shape, undescribed, veto_of};
use tables::Host;

const T0: i64 = 1_772_323_200_000;
const USE: &str = "Provenance";

fn text() -> Value {
    json!({"description": "A string value", "type": ["string", "null"]})
}

fn workflow() -> UseSpec {
    UseSpec::on_type(
        "Workflow",
        json!({"states": ["new", "triage", "assigned"], "transitions": [
            {"from": "new", "to": "triage"}, {"from": "new", "to": "assigned"}, {"from": "triage", "to": "assigned"}]}),
    )
}

fn derived() -> UseSpec {
    UseSpec::on_type(
        "Derived",
        json!({"fields": {
            "category": {"from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard"},
            "priority": {"from": ["subject", "body"], "instruction": "How urgent is this?", "model": "standard"}},
            "permission": "tickets.derive"}),
    )
}

fn reviewed() -> Value {
    json!({"review": {"sources": ["Derived"], "below": 0.8, "permission": "tickets.review"},
           "gates": {"assigned": {"reviewed": ["category", "priority"]}}})
}

fn ticket(uses: Vec<UseSpec>) -> TypeSpec {
    let mut spec = TypeSpec::new("Ticket")
        .with_field(FieldSpec::new("subject", "string"))
        .with_field(FieldSpec::new("body", "string"))
        .with_field(FieldSpec::new("category", "string").with_schema(text()))
        .with_field(FieldSpec::new("priority", "string").with_schema(text()));
    for entry in uses {
        spec = spec.with(entry);
    }
    spec
}

fn triaged() -> TypeSpec {
    ticket(vec![
        workflow(),
        derived(),
        UseSpec::on_type(USE, reviewed()),
    ])
}

fn alice() -> Caller {
    Caller::new("alice", Vec::<String>::new())
}

fn deriver() -> Caller {
    Caller::new("deriver", ["tickets.derive"])
}

fn rae() -> Caller {
    Caller::new("rae", ["tickets.review"])
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

fn table_op(host: &mut Host, who: &Caller, name: &str, params: Value) -> Outcome {
    host.run(
        "Ticket",
        who,
        T0 + 10,
        Action::TableOperation {
            use_id: "Derived".into(),
            operation: name.into(),
            params,
        },
    )
}

// derive settles the two derived fields at their confidences.
fn derive(host: &mut Host, category: f64, priority: f64) {
    let pulled = result(table_op(host, &deriver(), "staleDerivations", json!({})));
    let hash = |field: &str| {
        pulled["items"]
            .as_array()
            .expect("items")
            .iter()
            .find(|item| item["field"] == field)
            .map(|item| item["sourceHash"].clone())
            .expect("pulled")
    };
    let settled = result(table_op(
        host,
        &deriver(),
        "settleDerivations",
        json!({"items": [
            {"id": "t1", "field": "category", "sourceHash": hash("category"), "value": "billing", "confidence": category},
            {"id": "t1", "field": "priority", "sourceHash": hash("priority"), "value": "high", "confidence": priority}]}),
    ));
    assert_eq!(settled["settled"], json!(2), "{settled}");
}

fn fields(host: &Host) -> Value {
    host.read_fields_at("Ticket", "t1", USE, T0)["fields"].clone()
}

#[test]
fn the_declaration_owns_in_review_computes_fields_and_keeps_records() {
    let registered = core();
    let declaration = registered
        .declaration(provenance::NAME)
        .expect("registered");
    assert!(declaration.has_field(provenance::IN_REVIEW));
    assert!(!declaration.is_computed(provenance::IN_REVIEW));
    assert!(declaration.is_computed(provenance::FIELDS));
    let table = declaration
        .find_table(provenance::RECORDS)
        .expect("records");
    assert_eq!(table.key.as_deref(), Some("field"));
    assert_eq!(declaration.operation_names(false), ["accept", "reject"]);
    assert_eq!(
        declaration.vetoes,
        ["not_recorded", "not_in_review", "unreviewed_fields"]
    );
}

/// The checks Provenance writes by hand are the declaration's configSchema
/// and paramsSchemas, keyword for keyword.
#[test]
fn the_config_and_parameter_checks_are_the_declarations_schemas() {
    let file: Value = serde_json::from_str(provenance::DECLARATION).expect("it parses");
    let names = json!({"items": {"minLength": 1, "type": "string"}, "minItems": 1, "type": "array", "uniqueItems": true});
    assert_eq!(
        undescribed(&file["configSchema"]),
        json!({"additionalProperties": false, "properties": {
            "fields": names,
            "gates": {"additionalProperties": {"additionalProperties": false, "properties": {
                "reviewed": {"oneOf": [names, {"const": "all"}]}}, "required": ["reviewed"], "type": "object"},
                "minProperties": 1, "propertyNames": {"maxLength": 64, "pattern": "^[A-Za-z][A-Za-z0-9_-]*$"}, "type": "object"},
            "review": {"additionalProperties": false, "properties": {
                "below": {"maximum": 1, "minimum": 0, "type": "number"},
                "permission": {"minLength": 1, "type": "string"},
                "sources": names}, "required": ["sources", "permission"], "type": "object"}},
            "type": "object"})
    );
    assert_eq!(
        undescribed(&file["operations"][0]["paramsSchema"]),
        json!({"additionalProperties": false, "properties": {"fields": names}, "required": ["fields"], "type": "object"})
    );
    assert_eq!(
        undescribed(&file["operations"][1]["paramsSchema"]),
        json!({"additionalProperties": false, "properties": {"fields": names, "patch": {"type": "object"}}, "required": ["fields"], "type": "object"})
    );
    let core = core();
    assert_eq!(
        shape(
            &core,
            &ticket(vec![
                workflow(),
                UseSpec::on_type(
                    USE,
                    json!({"fields": [], "review": {"below": 2}, "gates": {"1x": {"reviewed": "some"}}})
                )
            ])
        ),
        [
            "/config/fields: must NOT have fewer than 1 items",
            "/config/gates: must match pattern \"^[A-Za-z][A-Za-z0-9_-]*$\"",
            "/config/gates: property name must be valid",
            "/config/gates/1x/reviewed: must be equal to constant",
            "/config/gates/1x/reviewed: must match exactly one schema in oneOf",
            "/config/gates/1x/reviewed: must be array",
            "/config/review: must have required property 'permission'",
            "/config/review: must have required property 'sources'",
            "/config/review/below: must be <= 1"
        ]
    );
    let mut host = opened(triaged());
    let refusal = refused(host.invoke(
        "Ticket",
        &rae(),
        T0,
        "t1",
        USE,
        "accept",
        json!({"fields": [], "x": 1}),
    ));
    assert_eq!(
        refusal["details"]["issues"],
        json!([
            {"path": "", "message": "must NOT have additional properties: x"},
            {"path": "/fields", "message": "must NOT have fewer than 1 items"}
        ])
    );
}

/// The config is held to the type: recorded fields its own, review
/// sources other behaviors it composes, gates its Workflow's states and
/// recorded fields.
#[test]
fn the_config_is_held_to_the_types_fields_behaviors_and_workflow() {
    let core = core();
    let with = |uses: Vec<UseSpec>| ticket(uses);
    assert_eq!(
        rule(
            &core,
            &with(vec![UseSpec::on_type(USE, json!({"fields": ["title"]}))]),
            USE
        ),
        "fields: Ticket has no field title"
    );
    assert_eq!(
        rule(
            &core,
            &with(vec![UseSpec::on_type(
                USE,
                json!({"review": {"sources": ["Derived"], "permission": "p"}})
            )]),
            USE
        ),
        "review: source Derived is not another behavior Ticket composes"
    );
    assert_eq!(
        rule(
            &core,
            &with(vec![UseSpec::on_type(
                USE,
                json!({"gates": {"assigned": {"reviewed": "all"}}})
            )]),
            USE
        ),
        "gates: the type has no Workflow whose moves they gate"
    );
    assert_eq!(
        rule(
            &core,
            &with(vec![
                workflow(),
                UseSpec::on_type(USE, json!({"gates": {"done": {"reviewed": "all"}}}))
            ]),
            USE
        ),
        "gate \"done\" is not a state of the type's Workflow (new, triage, assigned)"
    );
    assert_eq!(
        rule(
            &core,
            &with(vec![
                workflow(),
                UseSpec::on_type(
                    USE,
                    json!({"fields": ["subject"], "gates": {"assigned": {"reviewed": ["body"]}}})
                )
            ]),
            USE
        ),
        "gate \"assigned\": body is not a field Provenance records"
    );
}

/// A caller's create and update record caller, Derived's settle records
/// Derived with the confidence it attributed, and a write below the
/// threshold is proposed, which inReview holds as a column.
#[test]
fn records_each_write_and_proposes_one_below_the_threshold() {
    let mut host = opened(triaged());
    assert_eq!(
        fields(&host),
        json!({"subject": {"source": "caller", "by": "alice", "at": T0}, "body": {"source": "caller", "by": "alice", "at": T0}})
    );
    assert_eq!(host.owned("Ticket", "t1", USE, "inReview"), json!(false));
    derive(&mut host, 0.95, 0.6);
    let recorded = fields(&host);
    assert_eq!(
        recorded["category"],
        json!({"source": "Derived", "by": "deriver", "at": T0 + 10, "confidence": 0.95, "review": {"state": "accepted", "at": T0 + 10}})
    );
    assert_eq!(
        recorded["priority"],
        json!({"source": "Derived", "by": "deriver", "at": T0 + 10, "confidence": 0.6, "review": {"state": "proposed"}})
    );
    assert_eq!(host.owned("Ticket", "t1", USE, "inReview"), json!(true));
    // A caller's write over the proposal edits it.
    changed(host.update(
        "Ticket",
        &alice(),
        T0 + 20,
        "t1",
        json!({"priority": "low"}),
    ));
    assert_eq!(
        fields(&host)["priority"],
        json!({"source": "caller", "by": "alice", "at": T0 + 20, "review": {"state": "edited", "by": "alice", "at": T0 + 20}})
    );
    assert_eq!(host.owned("Ticket", "t1", USE, "inReview"), json!(false));
}

/// A gate holds a move into its state while a field it lists is proposed,
/// until a reviewer accepts it; a move Workflow refuses is Workflow's.
#[test]
fn a_gate_holds_a_move_until_a_reviewer_accepts() {
    let mut host = opened(triaged());
    derive(&mut host, 0.95, 0.6);
    let (code, details) = veto_of(host.invoke(
        "Ticket",
        &alice(),
        T0 + 20,
        "t1",
        "Workflow",
        "transition",
        json!({"to": "assigned"}),
    ));
    assert_eq!(code, "unreviewed_fields");
    assert_eq!(
        details,
        json!({"state": "assigned", "fields": ["priority"]})
    );
    let forbidden = refused(host.invoke(
        "Ticket",
        &alice(),
        T0 + 21,
        "t1",
        USE,
        "accept",
        json!({"fields": ["priority"]}),
    ));
    assert_eq!(forbidden["code"], json!("forbidden"));
    assert_eq!(
        forbidden["message"],
        json!("alice may not review Ticket t1: it needs permission tickets.review")
    );
    let (code, _) = veto_of(host.invoke(
        "Ticket",
        &rae(),
        T0 + 21,
        "t1",
        USE,
        "accept",
        json!({"fields": ["category"]}),
    ));
    assert_eq!(code, "not_in_review");
    let (code, _) = veto_of(host.invoke(
        "Ticket",
        &rae(),
        T0 + 21,
        "t1",
        USE,
        "accept",
        json!({"fields": ["nothing"]}),
    ));
    assert_eq!(code, "not_recorded");
    assert_eq!(
        result(host.invoke(
            "Ticket",
            &rae(),
            T0 + 22,
            "t1",
            USE,
            "accept",
            json!({"fields": ["priority"]})
        )),
        json!({"accepted": ["priority"], "inReview": false})
    );
    assert_eq!(
        result(host.invoke(
            "Ticket",
            &alice(),
            T0 + 23,
            "t1",
            "Workflow",
            "transition",
            json!({"to": "assigned"})
        )),
        json!({"from": "new", "to": "assigned"})
    );
}

/// reject writes the field as the reviewer, which Derived records as an
/// override and does not propose again.
#[test]
fn reject_replaces_a_proposal_as_the_reviewers_write() {
    let mut host = opened(triaged());
    derive(&mut host, 0.95, 0.5);
    let (code, _) = veto_of(host.invoke(
        "Ticket",
        &rae(),
        T0 + 20,
        "t1",
        USE,
        "reject",
        json!({"fields": ["priority"], "patch": {"category": "x"}}),
    ));
    assert_eq!(code, "not_recorded");
    assert_eq!(
        result(host.invoke(
            "Ticket",
            &rae(),
            T0 + 20,
            "t1",
            USE,
            "reject",
            json!({"fields": ["priority"], "patch": {"priority": "low"}})
        )),
        json!({"rejected": ["priority"], "inReview": false})
    );
    assert_eq!(host.row("Ticket", "t1").own["priority"], json!("low"));
    assert_eq!(
        fields(&host)["priority"],
        json!({"source": "Provenance", "by": "rae", "at": T0 + 20, "review": {"state": "rejected", "by": "rae", "at": T0 + 20}})
    );
    let derived = host.read_fields_at("Ticket", "t1", "Derived", T0);
    assert_eq!(
        derived["fields"]["priority"]["overridden"],
        json!({"by": "Provenance", "at": T0 + 20})
    );
}

/// A write acting for a subject (D77) records for beside by.
#[test]
fn a_write_acting_for_a_subject_records_for() {
    let mut host = opened(ticket(vec![UseSpec::on_type(USE, json!({}))]));
    let agent = Caller::new("agent:triage", Vec::<String>::new()).acting_for("bob");
    changed(host.update(
        "Ticket",
        &agent,
        T0 + 5,
        "t1",
        json!({"body": "Refused twice."}),
    ));
    assert_eq!(
        fields(&host)["body"],
        json!({"source": "caller", "by": "agent:triage", "for": "bob", "at": T0 + 5})
    );
}

/// Any config may change and Provenance may be added to a table with rows;
/// removed from one, its records would stay behind.
#[test]
fn the_config_may_change_and_provenance_stays_on_a_table_with_rows() {
    let host = opened(triaged());
    let publish = |from: TypeSpec, to: TypeSpec| {
        let command =
            superschematic_rules::Command::new(alice(), T0, Action::Publish { from: Some(from) });
        let mut tables = tables::Tables::default();
        tables.rows = host.tables.rows.clone();
        host.core.drive(&to, &command, &mut tables).into_result()
    };
    let plain = ticket(vec![workflow(), derived()]);
    assert!(publish(plain.clone(), triaged()).is_ok());
    assert!(publish(
        triaged(),
        ticket(vec![
            workflow(),
            derived(),
            UseSpec::on_type(USE, json!({}))
        ])
    )
    .is_ok());
    let removed = publish(triaged(), plain).expect_err("refused");
    assert!(
        removed.message.contains(
            "the records its instances have would stay behind with nothing to delete them"
        ),
        "{}",
        removed.message
    );
}
