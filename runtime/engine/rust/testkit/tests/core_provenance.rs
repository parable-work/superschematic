//! Provenance, the core's record of who set each own field last (D73):
//! a caller's write and another behavior's, by the attribution its
//! update() gave (D16, amended); review of a source's writes, with
//! `below`, accept and reject, and edited; the inReview filter; a gate on
//! a Workflow move; and the support ticket recipe of Derived, Provenance,
//! StateRules and Reactions on the runner. On SQLite and, with the
//! variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, InstanceRecord,
    ListOptions, Principal, RunnerOptions, SchemaTarget, WriteOptions,
};
use support::{alice, code, schema_document, targets, veto, Target};

fn deriver() -> Principal {
    Principal::new("deriver", ["tickets.derive"])
}

fn reviewer() -> Principal {
    Principal::new("rae", ["tickets.review"])
}

fn flow() -> Value {
    json!({
        "states": ["new", "triage", "assigned", "resolved"],
        "transitions": [
            {"from": "new", "to": "triage"},
            {"from": "new", "to": "assigned"},
            {"from": "triage", "to": "assigned"},
            {"from": "assigned", "to": "resolved"}
        ]
    })
}

fn derived() -> Value {
    json!({"name": "Derived", "config": {
        "fields": {
            "category": {"from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard", "states": ["new", "triage"]},
            "priority": {"from": ["subject", "body"], "instruction": "How urgent is this?", "model": "standard", "states": ["new", "triage"]}
        },
        "permission": "tickets.derive"
    }})
}

fn provenance() -> Value {
    json!({"name": "Provenance", "config": {
        "review": {"sources": ["Derived"], "below": 0.8, "permission": "tickets.review"},
        "gates": {"assigned": {"reviewed": ["category", "priority"]}}
    }})
}

fn ticket_document(behaviors: &[Value]) -> Value {
    let mut document = schema_document(
        "Ticket",
        json!([
            {"name": "subject", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "category", "typeRef": {"name": "string"}},
            {"name": "priority", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"]["Ticket"]["behaviors"] = Value::Array(behaviors.to_vec());
    document
}

fn opened(target: &Target, behaviors: &[Value]) -> Engine {
    let engine = target.open_core_with(|options| {
        options.clock = Clock::Manual(1_000_000);
        options.runner = Some(RunnerOptions::new(Principal::new(
            "runner",
            Vec::<String>::new(),
        )));
    });
    engine
        .schemas()
        .define(
            &alice(),
            ticket_document(behaviors),
            DefineOptions::default(),
        )
        .expect("define");
    engine
        .schemas()
        .publish(&alice(), "Ticket", SchemaTarget::default())
        .expect("publish");
    engine
        .instances()
        .create(
            &alice(),
            "Ticket",
            &json!({"subject": "Card declined", "body": "Refused at checkout."}),
            CreateOptions {
                id: Some("t1"),
                ..Default::default()
            },
        )
        .expect("create");
    engine
}

fn invoke(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine.instances().invoke(
        who,
        "Ticket",
        "t1",
        operation,
        &params,
        WriteOptions::default(),
    )
}

fn read(engine: &Engine) -> InstanceRecord {
    engine
        .instances()
        .get(&alice(), "Ticket", "t1", Default::default())
        .expect("get")
        .expect("the instance")
}

fn records(engine: &Engine) -> Value {
    read(engine).behaviors["Provenance"]["fields"].clone()
}

// settle settles the stale fields the values give, with their confidence.
fn settle(engine: &Engine, values: &[(&str, &str, f64)]) -> Value {
    let stale = engine
        .instances()
        .invoke_schema(&deriver(), "Ticket", "staleDerivations", &json!({}), None)
        .expect("staleDerivations");
    let items: Vec<Value> = values
        .iter()
        .map(|(field, value, confidence)| {
            let hash = stale["items"]
                .as_array()
                .unwrap()
                .iter()
                .find(|item| item["field"] == *field)
                .expect("a stale item")["sourceHash"]
                .clone();
            json!({"id": "t1", "field": field, "sourceHash": hash, "value": value, "confidence": confidence})
        })
        .collect();
    engine
        .instances()
        .invoke_schema(
            &deriver(),
            "Ticket",
            "settleDerivations",
            &json!({"items": items}),
            None,
        )
        .expect("settleDerivations")
}

#[test]
fn a_callers_write_and_another_behaviors_are_recorded_by_source_principal_and_confidence() {
    for target in targets("a_callers_write_and_another_behaviors_are_recorded") {
        let engine = opened(
            &target,
            &[
                derived(),
                json!({"name": "Provenance", "config": {}}),
                json!({"name": "Workflow", "config": flow()}),
            ],
        );
        let fields = records(&engine);
        assert_eq!(fields["subject"]["source"], "caller");
        assert_eq!(fields["subject"]["by"], "alice");
        assert_eq!(fields["subject"]["at"], 1_000_000);
        assert_eq!(
            fields.get("category"),
            None,
            "a field no write gave has no record"
        );
        settle(&engine, &[("category", "billing", 0.6)]);
        let fields = records(&engine);
        assert_eq!(
            fields["category"],
            json!({"source": "Derived", "by": "deriver", "at": 1_000_000, "confidence": 0.6})
        );
        assert_eq!(read(&engine).behaviors["Provenance"]["inReview"], false);
    }
}

#[test]
fn a_sources_write_is_proposed_below_the_threshold_accepted_at_it_and_a_list_filters_on_in_review()
{
    for target in targets("a_sources_write_is_proposed_below_the_threshold") {
        let engine = opened(
            &target,
            &[
                json!({"name": "Workflow", "config": flow()}),
                derived(),
                provenance(),
            ],
        );
        settle(
            &engine,
            &[("category", "billing", 0.95), ("priority", "high", 0.6)],
        );
        let fields = records(&engine);
        assert_eq!(
            fields["category"]["review"],
            json!({"state": "accepted", "at": 1_000_000})
        );
        assert_eq!(fields["priority"]["review"], json!({"state": "proposed"}));
        assert_eq!(read(&engine).behaviors["Provenance"]["inReview"], true);
        let filter = json!({"Provenance.inReview": true});
        let page = engine
            .instances()
            .list(
                &alice(),
                "Ticket",
                ListOptions {
                    filter: Some(&filter),
                    ..Default::default()
                },
            )
            .unwrap();
        assert_eq!(page.items.len(), 1);

        // The gate holds the move into assigned while the priority is
        // proposed.
        let (behavior, _, _, veto_code, details) = veto(invoke(
            &engine,
            &alice(),
            "transition",
            json!({"to": "assigned"}),
        ));
        assert_eq!(behavior, "Provenance");
        assert_eq!(veto_code.as_deref(), Some("unreviewed_fields"));
        assert_eq!(
            details,
            Some(json!({"state": "assigned", "fields": ["priority"]}))
        );
        // A move the gate does not name is not held.
        invoke(&engine, &alice(), "transition", json!({"to": "triage"})).unwrap();

        assert_eq!(
            code(invoke(
                &engine,
                &alice(),
                "accept",
                json!({"fields": ["priority"]})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            veto(invoke(
                &engine,
                &reviewer(),
                "accept",
                json!({"fields": ["category"]})
            ))
            .3
            .as_deref(),
            Some("not_in_review")
        );
        assert_eq!(
            veto(invoke(
                &engine,
                &reviewer(),
                "accept",
                json!({"fields": ["nope"]})
            ))
            .3
            .as_deref(),
            Some("not_recorded")
        );
        assert_eq!(
            invoke(
                &engine,
                &reviewer(),
                "accept",
                json!({"fields": ["priority"]})
            )
            .unwrap(),
            json!({"accepted": ["priority"], "inReview": false})
        );
        assert_eq!(records(&engine)["priority"]["review"]["by"], "rae");
        invoke(&engine, &alice(), "transition", json!({"to": "assigned"})).unwrap();
    }
}

#[test]
fn reject_clears_or_replaces_a_proposal_as_the_reviewers_write_and_derived_does_not_propose_it_again(
) {
    for target in targets("reject_clears_or_replaces_a_proposal") {
        let engine = opened(
            &target,
            &[
                json!({"name": "Workflow", "config": flow()}),
                derived(),
                provenance(),
            ],
        );
        settle(
            &engine,
            &[("category", "billing", 0.5), ("priority", "high", 0.5)],
        );
        assert_eq!(
            veto(invoke(
                &engine,
                &reviewer(),
                "reject",
                json!({"fields": ["category"], "patch": {"priority": "low"}})
            ))
            .3
            .as_deref(),
            Some("not_recorded")
        );
        assert_eq!(
            invoke(
                &engine,
                &reviewer(),
                "reject",
                json!({"fields": ["category", "priority"], "patch": {"priority": "low"}})
            )
            .unwrap(),
            json!({"rejected": ["category", "priority"], "inReview": false})
        );
        let instance = read(&engine);
        assert_eq!(instance.data.get("category"), None);
        assert_eq!(instance.data["priority"], "low");
        let fields = &instance.behaviors["Provenance"]["fields"];
        assert_eq!(fields["priority"]["source"], "Provenance");
        assert_eq!(fields["priority"]["by"], "rae");
        assert_eq!(
            fields["priority"]["review"],
            json!({"state": "rejected", "by": "rae", "at": 1_000_000})
        );
        // Derived records the reviewer's write as an override.
        assert_eq!(
            instance.behaviors["Derived"]["fields"]["priority"]["overridden"]["by"],
            "Provenance"
        );
        assert_eq!(instance.behaviors["Derived"]["stale"], json!([]));
    }
}

#[test]
fn a_callers_write_over_a_proposal_makes_it_edited() {
    for target in targets("a_callers_write_over_a_proposal_makes_it_edited") {
        let engine = opened(
            &target,
            &[
                json!({"name": "Workflow", "config": flow()}),
                derived(),
                provenance(),
            ],
        );
        settle(&engine, &[("priority", "high", 0.5)]);
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"priority": "urgent"}),
                WriteOptions::default(),
            )
            .unwrap();
        let instance = read(&engine);
        let priority = &instance.behaviors["Provenance"]["fields"]["priority"];
        assert_eq!(priority["source"], "caller");
        assert_eq!(
            priority["review"],
            json!({"state": "edited", "by": "alice", "at": 1_000_000})
        );
        assert_eq!(instance.behaviors["Provenance"]["inReview"], false);
        assert_eq!(
            instance.behaviors["Derived"]["fields"]["priority"]["overridden"]["by"],
            "alice"
        );
    }
}

#[test]
fn define_refuses_a_field_the_type_lacks_a_source_it_does_not_compose_and_a_gate_without_workflow()
{
    for target in targets("define_refuses_a_field_the_type_lacks") {
        let engine = target.open_core();
        let refused = |behaviors: Vec<Value>| match engine.schemas().define(
            &alice(),
            ticket_document(&behaviors),
            DefineOptions::default(),
        ) {
            Err(error) => error.to_string(),
            Ok(_) => panic!("expected a refusal"),
        };
        assert!(refused(vec![
            json!({"name": "Provenance", "config": {"fields": ["title"]}})
        ])
        .contains("has no field title"));
        assert!(refused(vec![json!({"name": "Provenance", "config": {"review": {"sources": ["Derived"], "permission": "p"}}})])
            .contains("is not another behavior"));
        assert!(refused(vec![
            json!({"name": "Provenance", "config": {"gates": {"assigned": {"reviewed": "all"}}}})
        ])
        .contains("has no Workflow"));
        assert!(refused(vec![
            json!({"name": "Workflow", "config": flow()}),
            json!({"name": "Provenance", "config": {"fields": ["subject"], "gates": {"assigned": {"reviewed": ["body"]}}}})
        ])
        .contains("is not a field Provenance records"));
    }
}

// The support ticket recipe (D73): a model triages, a person confirms.
// Reactions moves the ticket on the gate's own signals: settledAt, and
// Provenance.inReview.
fn recipe() -> Vec<Value> {
    vec![
        json!({"name": "Workflow", "config": flow()}),
        derived(),
        provenance(),
        json!({"name": "StateRules", "config": {"states": {"assigned": {"required": ["category", "priority"]}}}}),
        json!({"name": "Reactions", "config": {"rules": [
            {"when": {"changes": {"field": "Derived.fields.category.settledAt"}}, "then": {"transition": "assigned"}},
            {"when": {"changes": {"field": "Derived.fields.priority.settledAt"}}, "then": {"transition": "assigned"}},
            {"when": {"changes": {"field": "Provenance.inReview", "to": true}}, "then": {"transition": "triage"}},
            {"when": {"changes": {"field": "Provenance.inReview", "to": false}}, "then": {"transition": "assigned"}}
        ]}}),
    ]
}

fn status(engine: &Engine) -> Value {
    read(engine).behaviors["Workflow"]["status"].clone()
}

#[test]
fn the_ticket_recipe_triages_an_unsure_answer_and_assigns_once_a_reviewer_accepts_it() {
    for target in targets("the_ticket_recipe_triages_an_unsure_answer") {
        let engine = opened(&target, &recipe());
        settle(
            &engine,
            &[("category", "billing", 0.95), ("priority", "high", 0.6)],
        );
        engine.runner().run_due().expect("run due");
        assert_eq!(status(&engine), "triage");
        invoke(
            &engine,
            &reviewer(),
            "accept",
            json!({"fields": ["priority"]}),
        )
        .unwrap();
        engine.runner().run_due().expect("run due");
        assert_eq!(status(&engine), "assigned");
    }
}

#[test]
fn the_ticket_recipe_assigns_a_sure_answer_with_no_person_even_when_its_fields_settle_apart() {
    for target in targets("the_ticket_recipe_assigns_a_sure_answer") {
        let engine = opened(&target, &recipe());
        settle(&engine, &[("category", "billing", 0.95)]);
        engine.runner().run_due().expect("run due");
        // StateRules holds assigned until the priority is there too.
        assert_eq!(status(&engine), "new");
        settle(&engine, &[("priority", "high", 0.9)]);
        engine.runner().run_due().expect("run due");
        assert_eq!(status(&engine), "assigned");
    }
}
