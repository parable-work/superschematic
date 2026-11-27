//! What only a table's Rollups takes (D104's Barrier), on an engine type: a
//! gate's `exemptions`. The engine shares Rollups' declaration with the
//! core, so its describe advertises them; it refuses them at define,
//! naming the table form, rather than letting a move through a gate it
//! does not exempt. On SQLite and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, DefineOptions, Engine, Error};
use support::{alice, issue, issues_of, refusal, schema_document, targets};

const START: i64 = 1_000_000;

// A project whose finished rollup gates done over its tasks.
fn project_schema(rollups: &Value) -> Value {
    let mut document = schema_document(
        "Project",
        json!([{"name": "title", "typeRef": {"name": "string"}, "required": true}]),
        json!({}),
    );
    document["types"]["Project"]["behaviors"] = json!([
        {"name": "Workflow", "config": {
            "states": ["active", "done"],
            "transitions": [{"from": "active", "to": "done"}]
        }},
        {"name": "Rollups", "config": {"rollups": rollups}}
    ]);
    document
}

fn define(engine: &Engine, rollups: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), project_schema(rollups), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_a_gates_exemptions() {
    for target in targets("rollups_table_members") {
        let engine = target.open_core_with(|options| options.clock = Clock::Manual(START));
        let reason = "rollup finished names exemptions, which a table's Rollups takes: an engine type's gate holds every move into a state it gates";
        for exemptions in [
            json!({"permission": "projects.close"}),
            json!({"deadlines": ["closeBy"]}),
        ] {
            let rollups = json!({"finished": {
                "schema": "Task", "link": "project", "function": "all",
                "gatedStates": ["done"], "exemptions": exemptions
            }});
            let error = refusal(define(&engine, &rollups));
            assert_eq!(error.name(), "SchemaDocumentError", "{exemptions}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Project/behaviors/1/config",
                    &format!("type Project: behavior Rollups config: {reason}")
                )],
                "{exemptions}"
            );
        }
    }
}
