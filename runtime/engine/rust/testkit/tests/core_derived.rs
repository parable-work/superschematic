//! Derived, the core's own fields a model outside the engine fills (D73):
//! a deriver pulls what is stale and settles values by hash; a value is
//! an own field that the type validates and every guard judges, attributed
//! to Derived in its event (D16, amended); a write by anyone else
//! overrides the field until rederive; failures, invalid values and other
//! behaviors' vetoes count attempts and append no event; states, the
//! config's permission, its refusals at define and new versions. On SQLite
//! and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, InstanceRecord, Principal,
    ReadEvents, SchemaTarget, WriteOptions,
};
use support::{alice, code, refusal, schema_document, targets, veto, Target};

fn deriver() -> Principal {
    Principal::new("deriver", ["tickets.derive"])
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

fn derived(extra: Value) -> Value {
    let mut config = json!({
        "fields": {
            "category": {"from": ["subject", "body"], "instruction": "Classify the request.", "model": "standard"},
            "summary": {"from": ["body"], "instruction": "Summarize it in one sentence.", "model": "simple"}
        },
        "permission": "tickets.derive"
    });
    if let (Some(config), Value::Object(extra)) = (config.as_object_mut(), extra) {
        config.extend(extra);
    }
    config
}

fn ticket_document(behaviors: &[Value]) -> Value {
    let mut document = schema_document(
        "Ticket",
        json!([
            {"name": "subject", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}},
            {"name": "category", "typeRef": {"name": "Category"}},
            {"name": "summary", "typeRef": {"name": "string"}},
            {"name": "note", "typeRef": {"name": "string"}}
        ]),
        json!({"enums": {"Category": {"name": "Category", "values": [{"name": "billing"}, {"name": "bug"}, {"name": "other"}]}}}),
    );
    document["types"]["Ticket"]["behaviors"] = Value::Array(behaviors.to_vec());
    document
}

fn publish(engine: &Engine, behaviors: &[Value]) -> Result<i64, Error> {
    engine.schemas().define(
        &alice(),
        ticket_document(behaviors),
        DefineOptions::default(),
    )?;
    Ok(engine
        .schemas()
        .publish(&alice(), "Ticket", SchemaTarget::default())?
        .version)
}

fn create(engine: &Engine, id: &str, data: Value) -> InstanceRecord {
    engine
        .instances()
        .create(
            &alice(),
            "Ticket",
            &data,
            CreateOptions {
                id: Some(id),
                ..Default::default()
            },
        )
        .expect("create")
}

fn opened(target: &Target, behaviors: &[Value]) -> Engine {
    let engine = target.open_core();
    publish(&engine, behaviors).expect("publish");
    create(
        &engine,
        "t1",
        json!({"subject": "Card declined", "body": "My card is refused at checkout."}),
    );
    engine
}

fn schema_op(
    engine: &Engine,
    who: &Principal,
    operation: &str,
    params: Value,
) -> Result<Value, Error> {
    engine
        .instances()
        .invoke_schema(who, "Ticket", operation, &params, None)
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

fn stale(engine: &Engine) -> Vec<Value> {
    schema_op(engine, &deriver(), "staleDerivations", json!({})).expect("staleDerivations")["items"]
        .as_array()
        .expect("items")
        .clone()
}

fn hash_of(items: &[Value], field: &str) -> String {
    items
        .iter()
        .find(|item| item["field"] == field)
        .and_then(|item| item["sourceHash"].as_str())
        .expect("a stale item of the field")
        .to_owned()
}

fn events(engine: &Engine) -> Vec<Value> {
    engine
        .events()
        .read(
            &alice(),
            &ReadEvents {
                schema: Some("Ticket"),
                instance_id: Some("t1"),
                limit: Some(500),
                ..ReadEvents::default()
            },
        )
        .expect("events")
        .events
        .into_iter()
        .map(|event| event.change)
        .collect()
}

fn just_derived() -> Vec<Value> {
    vec![json!({"name": "Derived", "config": derived(json!({}))})]
}

#[test]
fn a_deriver_pulls_the_stale_fields_and_settles_values_that_land_as_own_fields_attributed_to_derived(
) {
    for target in targets("a_deriver_pulls_the_stale_fields") {
        let engine = opened(&target, &just_derived());
        let items = stale(&engine);
        assert_eq!(items.len(), 2);
        let category = items
            .iter()
            .find(|item| item["field"] == "category")
            .unwrap();
        assert_eq!(category["id"], "t1");
        assert_eq!(category["attempt"], 1);
        assert_eq!(category["instruction"], "Classify the request.");
        assert_eq!(category["model"], "standard");
        assert_eq!(
            category["inputs"],
            json!({"subject": "Card declined", "body": "My card is refused at checkout."})
        );
        // The field's own JSON Schema: an optional field may also be cleared.
        assert_eq!(
            category["output"]["enum"],
            json!(["billing", "bug", "other", null])
        );
        assert_eq!(
            read(&engine).behaviors["Derived"]["stale"],
            json!(["category", "summary"])
        );

        let settled = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [
                {"id": "t1", "field": "category", "sourceHash": hash_of(&items, "category"), "value": "billing", "confidence": 0.9},
                {"id": "t1", "field": "summary", "sourceHash": hash_of(&items, "summary"), "value": "A card is refused.",
                 "usage": {"inputTokens": 40, "outputTokens": 8}}
            ]}),
        )
        .expect("settle");
        assert_eq!(settled, json!({"settled": 2, "failed": 0, "skipped": []}));
        let instance = read(&engine);
        assert_eq!(instance.data["category"], "billing");
        assert_eq!(instance.data["summary"], "A card is refused.");
        assert_eq!(instance.seq, 2, "one event writes both fields");
        let fields = &instance.behaviors["Derived"]["fields"];
        assert_eq!(fields["category"]["confidence"], json!(0.9));
        assert_eq!(fields["category"]["model"], "standard");
        assert_eq!(fields["category"]["settledBy"], "deriver");
        assert_eq!(
            fields["category"]["sourceHash"],
            hash_of(&items, "category")
        );
        assert_eq!(fields["summary"].get("confidence"), None);
        assert_eq!(instance.behaviors["Derived"]["stale"], json!([]));
        assert!(stale(&engine).is_empty());
        let change = events(&engine).pop().unwrap();
        assert_eq!(change["behavior"], "Derived");
        assert_eq!(change["operation"], "applyDerived");
        assert_eq!(
            change["attribution"],
            json!({"category": {"behavior": "Derived", "confidence": 0.9}, "summary": {"behavior": "Derived"}})
        );
        assert_eq!(
            change["params"]["fields"]["summary"]["usage"]["inputTokens"],
            40
        );
    }
}

#[test]
fn an_input_that_moves_makes_the_field_stale_again_keeps_its_value_and_skips_a_settle_of_the_old_hash(
) {
    for target in targets("an_input_that_moves_makes_the_field_stale") {
        let engine = opened(&target, &just_derived());
        let first = stale(&engine);
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"note": "unrelated"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(
            hash_of(&stale(&engine), "summary"),
            hash_of(&first, "summary")
        );
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"body": "Refused twice."}),
                WriteOptions::default(),
            )
            .unwrap();
        let second = stale(&engine);
        assert_ne!(hash_of(&second, "summary"), hash_of(&first, "summary"));
        let settled = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [
                {"id": "t1", "field": "summary", "sourceHash": hash_of(&first, "summary"), "value": "Old."},
                {"id": "t1", "field": "category", "sourceHash": hash_of(&second, "category"), "value": "bug"},
                {"id": "gone", "field": "summary", "sourceHash": "x", "value": "?"},
                {"id": "t1", "field": "note", "sourceHash": "x", "value": "?"}
            ]}),
        )
        .unwrap();
        assert_eq!(settled["settled"], 1);
        assert_eq!(
            settled["skipped"],
            json!([
                {"id": "t1", "field": "summary", "reason": "moved"},
                {"id": "gone", "field": "summary", "reason": "not_found"},
                {"id": "t1", "field": "note", "reason": "not_derived"}
            ])
        );
        assert_eq!(read(&engine).data.get("summary"), None);
        // The settled category is stale once its inputs move, and keeps its
        // value until a new one settles.
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"subject": "Card refused"}),
                WriteOptions::default(),
            )
            .unwrap();
        let instance = read(&engine);
        assert_eq!(instance.data["category"], "bug");
        assert_eq!(
            instance.behaviors["Derived"]["stale"],
            json!(["category", "summary"])
        );
    }
}

#[test]
fn a_callers_write_overrides_the_field_until_rederive_and_a_create_that_gives_it_overrides_it_from_the_start(
) {
    for target in targets("a_callers_write_overrides_the_field") {
        let engine = opened(&target, &just_derived());
        let items = stale(&engine);
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"category": "other"}),
                WriteOptions::default(),
            )
            .unwrap();
        let instance = read(&engine);
        assert_eq!(
            instance.behaviors["Derived"]["fields"]["category"]["overridden"]["by"],
            "alice"
        );
        assert_eq!(instance.behaviors["Derived"]["stale"], json!(["summary"]));
        assert_eq!(stale(&engine).len(), 1);
        let settled = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [{"id": "t1", "field": "category", "sourceHash": hash_of(&items, "category"), "value": "bug"}]}),
        )
        .unwrap();
        assert_eq!(settled["skipped"][0]["reason"], "overridden");
        let (behavior, action, _, veto_code, _) = veto(invoke(
            &engine,
            &deriver(),
            "applyDerived",
            json!({"fields": {"category": {"sourceHash": hash_of(&items, "category"), "value": "bug"}}}),
        ));
        assert_eq!(
            (behavior.as_str(), action.as_str()),
            ("Derived", "applyDerived")
        );
        assert_eq!(veto_code.as_deref(), Some("overridden"));
        assert_eq!(read(&engine).data["category"], "other");

        assert_eq!(
            invoke(
                &engine,
                &alice(),
                "rederive",
                json!({"fields": ["category"]})
            )
            .unwrap(),
            json!({"stale": ["category", "summary"]})
        );
        assert_eq!(
            read(&engine).behaviors["Derived"]["fields"].get("category"),
            None
        );
        assert_eq!(stale(&engine).len(), 2);
        assert_eq!(
            veto(invoke(
                &engine,
                &alice(),
                "rederive",
                json!({"fields": ["note"]})
            ))
            .3
            .as_deref(),
            Some("not_derived")
        );

        create(
            &engine,
            "t2",
            json!({"subject": "Login", "summary": "Typed by a person."}),
        );
        let given = engine
            .instances()
            .get(&alice(), "Ticket", "t2", Default::default())
            .unwrap()
            .unwrap();
        assert_eq!(
            given.behaviors["Derived"]["fields"]["summary"]["overridden"]["by"],
            "alice"
        );
        // t2 has no body, the summary's one input: it wants no value either.
        assert_eq!(given.behaviors["Derived"]["stale"], json!(["category"]));
    }
}

#[test]
fn a_failure_an_invalid_value_and_another_behaviors_veto_count_attempts_append_no_event_and_run_out(
) {
    for target in targets("a_failure_an_invalid_value_and_another_behaviors_veto") {
        let behaviors = vec![
            json!({"name": "Workflow", "config": flow()}),
            json!({"name": "Derived", "config": derived(json!({"maxAttempts": 2}))}),
            json!({"name": "StateRules", "config": {"states": {"resolved": {"frozen": ["summary"]}}}}),
        ];
        let engine = opened(&target, &behaviors);
        let items = stale(&engine);
        let category = hash_of(&items, "category");
        let invalid = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [{"id": "t1", "field": "category", "sourceHash": category, "value": "payments"}]}),
        )
        .unwrap();
        assert_eq!(invalid["skipped"][0]["reason"], "invalid");
        assert_eq!(invalid["skipped"][0]["issues"][0]["path"], "category");
        assert_eq!(read(&engine).seq, 1, "no event for a refused value");
        let failed = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [{"id": "t1", "field": "category", "sourceHash": category, "failure": {"class": "timeout", "message": "the model took too long"}}]}),
        )
        .unwrap();
        assert_eq!(failed, json!({"settled": 0, "failed": 1, "skipped": []}));
        assert_eq!(read(&engine).seq, 1);
        // Out of attempts: the field leaves the queue and failedDerivations
        // lists it with its last failure.
        assert_eq!(
            stale(&engine)
                .iter()
                .map(|item| item["field"].clone())
                .collect::<Vec<_>>(),
            [json!("summary")]
        );
        let listed = schema_op(&engine, &deriver(), "failedDerivations", json!({})).unwrap();
        assert_eq!(
            listed["items"],
            json!([{"id": "t1", "field": "category", "attempts": 2,
                    "failure": {"class": "timeout", "message": "the model took too long"}}])
        );
        // A move of its inputs gives it its attempts back.
        engine
            .instances()
            .update(
                &alice(),
                "Ticket",
                "t1",
                &json!({"subject": "Card refused"}),
                WriteOptions::default(),
            )
            .unwrap();
        assert_eq!(stale(&engine).len(), 2);

        // StateRules freezes the summary in resolved: its veto skips the
        // value and counts an attempt.
        for to in ["assigned", "resolved"] {
            invoke(&engine, &alice(), "transition", json!({"to": to})).unwrap();
        }
        let summary = hash_of(&stale(&engine), "summary");
        let vetoed = schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [{"id": "t1", "field": "summary", "sourceHash": summary, "value": "Refused."}]}),
        )
        .unwrap();
        assert_eq!(
            vetoed["skipped"],
            json!([{"id": "t1", "field": "summary", "reason": "vetoed", "behavior": "StateRules", "code": "frozen_fields"}])
        );
        assert_eq!(
            stale(&engine)
                .iter()
                .find(|item| item["field"] == "summary")
                .unwrap()["attempt"],
            2
        );
    }
}

#[test]
fn a_field_with_states_is_listed_and_applied_only_in_them() {
    for target in targets("a_field_with_states_is_listed_and_applied_only_in_them") {
        let mut config = derived(json!({}));
        config["fields"]["category"]["states"] = json!(["new", "triage"]);
        let behaviors = vec![
            json!({"name": "Workflow", "config": flow()}),
            json!({"name": "Derived", "config": config}),
        ];
        let engine = opened(&target, &behaviors);
        let category = hash_of(&stale(&engine), "category");
        invoke(&engine, &alice(), "transition", json!({"to": "assigned"})).unwrap();
        assert_eq!(
            stale(&engine)
                .iter()
                .map(|item| item["field"].clone())
                .collect::<Vec<_>>(),
            [json!("summary")]
        );
        let (_, _, _, veto_code, _) = veto(invoke(
            &engine,
            &deriver(),
            "applyDerived",
            json!({"fields": {"category": {"sourceHash": category, "value": "bug"}}}),
        ));
        assert_eq!(veto_code.as_deref(), Some("out_of_state"));
        // The state does not change what is stale: the field still has no
        // value, and is listed again once the status is one of its states.
        assert_eq!(
            read(&engine).behaviors["Derived"]["stale"],
            json!(["category", "summary"])
        );
    }
}

#[test]
fn a_deriver_needs_the_configs_permission_and_a_batch_is_held_to_its_shape() {
    for target in targets("a_deriver_needs_the_configs_permission") {
        let engine = opened(&target, &just_derived());
        for operation in ["staleDerivations", "failedDerivations"] {
            assert_eq!(
                code(schema_op(&engine, &alice(), operation, json!({}))),
                EngineErrorCode::Forbidden
            );
        }
        assert_eq!(
            code(schema_op(
                &engine,
                &alice(),
                "settleDerivations",
                json!({"items": []})
            )),
            EngineErrorCode::Forbidden
        );
        assert_eq!(
            code(invoke(
                &engine,
                &alice(),
                "applyDerived",
                json!({"fields": {"summary": {"sourceHash": "x", "value": "y"}}})
            )),
            EngineErrorCode::Forbidden
        );
        let twice = refusal(schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [
                {"id": "t1", "field": "summary", "sourceHash": "x", "value": "a"},
                {"id": "t1", "field": "summary", "sourceHash": "x", "value": "b"},
                {"id": "t1", "field": "category", "sourceHash": "x"}
            ]}),
        ));
        assert_eq!(twice.code(), EngineErrorCode::InvalidArgument);
        let paths: Vec<&str> = twice
            .issues()
            .iter()
            .map(|issue| issue.path.as_str())
            .collect();
        assert_eq!(paths, ["/items/1", "/items/2"]);
    }
}

#[test]
fn define_refuses_a_field_or_input_the_type_lacks_a_derived_input_states_without_workflow_and_a_constant(
) {
    for target in targets("define_refuses_a_field_or_input_the_type_lacks") {
        let engine = target.open_core();
        let refused = |behaviors: Vec<Value>| match publish(&engine, &behaviors) {
            Err(error) => error.to_string(),
            Ok(_) => panic!("expected a refusal"),
        };
        let with = |fields: Value| {
            vec![json!({"name": "Derived", "config": {"fields": fields, "permission": "p"}})]
        };
        assert!(refused(with(
            json!({"title": {"from": ["body"], "instruction": "i", "model": "m"}})
        ))
        .contains("has no field title"));
        assert!(refused(with(
            json!({"summary": {"from": ["missing"], "instruction": "i", "model": "m"}})
        ))
        .contains("which Ticket does not have"));
        assert!(refused(with(
            json!({"summary": {"from": ["summary"], "instruction": "i", "model": "m"}})
        ))
        .contains("derived from itself"));
        assert!(refused(with(json!({
            "summary": {"from": ["category"], "instruction": "i", "model": "m"},
            "category": {"from": ["body"], "instruction": "i", "model": "m"}
        })))
        .contains("which is derived too"));
        assert!(refused(with(json!({"summary": {"from": ["body"], "instruction": "i", "model": "m", "states": ["new"]}}))).contains("has no Workflow"));
        let mut constant =
            with(json!({"summary": {"from": ["body"], "instruction": "i", "model": "m"}}));
        constant.push(json!({"name": "Constants", "config": {"fields": ["summary"]}}));
        assert!(refused(constant).contains("Constants keeps"));
        // A secret never goes to a model (D74).
        let mut secret = ticket_document(&with(
            json!({"summary": {"from": ["body", "apiKey"], "instruction": "i", "model": "m"}}),
        ));
        secret["types"]["Ticket"]["fields"]
            .as_array_mut()
            .unwrap()
            .push(json!({"name": "apiKey", "typeRef": {"name": "string"}, "secret": true}));
        let refusal = engine
            .schemas()
            .define(&alice(), secret, DefineOptions::default())
            .expect_err("a secret input");
        assert!(
            refusal.to_string().contains("apiKey, which is secret"),
            "{refusal}"
        );
    }
}

#[test]
fn a_new_instruction_makes_the_field_stale_and_adding_derived_counts_a_held_value_as_overridden() {
    for target in targets("a_new_instruction_makes_the_field_stale") {
        let engine = target.open_core();
        publish(&engine, &[]).unwrap();
        create(
            &engine,
            "t1",
            json!({"subject": "Card", "body": "Refused.", "summary": "By hand."}),
        );
        publish(&engine, &just_derived()).unwrap();
        let instance = read(&engine);
        assert_eq!(
            instance.behaviors["Derived"]["fields"]["summary"]["overridden"]["by"],
            "existing"
        );
        assert_eq!(instance.behaviors["Derived"]["stale"], json!(["category"]));
        let items = stale(&engine);
        schema_op(
            &engine,
            &deriver(),
            "settleDerivations",
            json!({"items": [{"id": "t1", "field": "category", "sourceHash": hash_of(&items, "category"), "value": "billing"}]}),
        )
        .unwrap();
        let mut config = derived(json!({}));
        config["fields"]["category"]["instruction"] = json!("Classify the request by its product.");
        publish(&engine, &[json!({"name": "Derived", "config": config})]).unwrap();
        let instance = read(&engine);
        assert_eq!(instance.data["category"], "billing");
        assert_eq!(instance.behaviors["Derived"]["stale"], json!(["category"]));
        assert_ne!(
            hash_of(&stale(&engine), "category"),
            hash_of(&items, "category")
        );
        // Removing Derived drops what it keeps; the values stay as own fields.
        publish(&engine, &[]).unwrap();
        let instance = read(&engine);
        assert_eq!(instance.data["category"], "billing");
        assert_eq!(instance.behaviors.get("Derived"), None);
    }
}
