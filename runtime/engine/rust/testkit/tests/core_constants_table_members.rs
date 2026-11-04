//! What only a table's Constants takes (D104), on an engine type: `oneWay`,
//! the fields that move one way. The engine shares Constants' declaration
//! with the core, so its describe advertises it; it refuses it at define,
//! naming the table form, rather than ignoring a field an update could then
//! move back. On SQLite and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{DefineOptions, Engine, Error};
use support::{alice, issue, issues_of, refusal, schema_document, targets};

// A run: a key, a kind, whether it is urgent and how often it ran.
fn run_schema(config: &Value) -> Value {
    let mut document = schema_document(
        "runs",
        json!([
            {"name": "key", "typeRef": {"name": "string"}, "required": true},
            {"name": "kind", "typeRef": {"name": "string"}},
            {"name": "urgent", "typeRef": {"name": "boolean"}},
            {"name": "attempts", "typeRef": {"name": "number"}}
        ]),
        json!({}),
    );
    document["types"]["runs"]["behaviors"] = json!([{"name": "Constants", "config": config}]);
    document
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), run_schema(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_constants_takes() {
    for target in targets("constants_table_members") {
        let engine = target.open_core();
        let table_form = "implements Constants<T, { ... }> on a DB table, D104";
        let reason = format!(
            "oneWay is a table's Constants' ({table_form}): an engine type's Constants keeps a field as its create gave it, and moves none one way"
        );
        for config in [
            json!({"fields": ["key"], "oneWay": {"urgent": "up"}}),
            json!({"fields": ["key"], "oneWay": {"attempts": "down", "urgent": "up"}, "permission": "runs.admin"}),
        ] {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/runs/behaviors/0/config",
                    &format!("type runs: behavior Constants config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/runs/behaviors/0/config: type runs: behavior Constants config: {reason}")
            );
        }
        // Without oneWay, the config defines as before.
        define(&engine, &json!({"fields": ["key", "kind"]})).expect("define");
    }
}
