//! What only a table's Presence takes (D104), on an engine type:
//! `ttlByState`, how long an instance stays present in each state of its
//! Workflow, and the config's `status`. The engine shares Presence's
//! declaration with the core, so its describe advertises them; it refuses
//! each at define, naming the table form, rather than ignoring a rule of
//! when an instance is missed. On SQLite and, with the variable set, on
//! Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, DefineOptions, Engine, Error};
use support::{alice, issue, issues_of, refusal, targets};

const START: i64 = 1_000_000;

// A worker is idle or busy while it beats, and missing once it stops.
fn workers_document(config: &Value) -> Value {
    json!({
        "kind": "General",
        "name": "Worker",
        "types": {
            "Worker": {
                "name": "Worker",
                "role": "EmbeddedStruct",
                "behaviors": [
                    {"name": "Workflow", "config": {
                        "states": ["idle", "busy", "missing"],
                        "transitions": [
                            {"from": "idle", "to": "busy"},
                            {"from": "busy", "to": "idle"},
                            {"from": "idle", "to": "missing"},
                            {"from": "busy", "to": "missing"},
                            {"from": "missing", "to": "idle"}
                        ]
                    }},
                    {"name": "Presence", "config": config}
                ],
                "fields": [
                    {"name": "subject", "typeRef": {"name": "string"}, "required": true}
                ]
            }
        }
    })
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), workers_document(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_presence_takes() {
    for target in targets("presence_table_members") {
        let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(START));
        let table_form = "implements Presence<T, { ... }> on a DB table, D104";
        let ttl_by_state = format!(
            "ttlByState is a table's Presence's ({table_form}): an engine type's instance stays present ttlMs after its create and each beat, whatever its state"
        );
        let status = format!(
            "status is a table's Presence's ({table_form}): an engine type's Presence reads its one Workflow"
        );
        let base = json!({"ttlMs": 30000, "principalField": "subject"});
        let with = |more: Value| {
            let mut config = base.clone();
            config
                .as_object_mut()
                .expect("an object")
                .extend(more.as_object().expect("an object").clone());
            config
        };
        let cases = [
            (
                with(json!({"ttlByState": {"busy": 600000}})),
                ttl_by_state.clone(),
            ),
            (with(json!({"status": "Workflow"})), status),
            // The first member it names: ttlByState, then status.
            (
                with(json!({"status": "Workflow", "ttlByState": {"busy": 600000}})),
                ttl_by_state,
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Worker/behaviors/1/config",
                    &format!("type Worker: behavior Presence config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Worker/behaviors/1/config: type Worker: behavior Presence config: {reason}")
            );
        }
        // Without them, the config defines as before.
        define(&engine, &base).expect("define");
    }
}
