//! What only a table's StateRules takes (D104), on an engine type: a
//! state's `by`, who may change a field it freezes, and `status`, the
//! Workflow field the rules are of. The engine shares StateRules'
//! declaration with the core, so its describe advertises them; it refuses
//! each at define, naming the table form, rather than ignoring a rule of who
//! may change a frozen field. On SQLite and, with the variable set, on
//! Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{DefineOptions, Engine, Error};
use support::{alice, issue, issues_of, refusal, schema_document, targets};

// An invoice is drafted, sent and paid.
fn invoice_schema(config: &Value) -> Value {
    let mut document = schema_document(
        "invoices",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "owner", "typeRef": {"name": "string"}},
            {"name": "amount", "typeRef": {"name": "number"}}
        ]),
        json!({}),
    );
    document["types"]["invoices"]["behaviors"] = json!([
        {"name": "Workflow", "config": {
            "states": ["draft", "sent", "paid"],
            "transitions": [{"from": "draft", "to": "sent"}, {"from": "sent", "to": "paid"}]
        }},
        {"name": "StateRules", "config": config}
    ]);
    document
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), invoice_schema(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_staterules_takes() {
    for target in targets("staterules_table_members") {
        let engine = target.open_core();
        let table_form = "implements StateRules<T, { ... }> on a DB table, D104";
        let by = |state: &str| {
            format!(
                "the rules of \"{state}\" name by, which a table's StateRules takes ({table_form}): on an engine type a frozen field changes for a caller with the config's permission alone"
            )
        };
        let status = format!(
            "status is a table's StateRules' ({table_form}): an engine type's StateRules reads its one Workflow"
        );
        let cases = [
            (
                json!({"states": {"sent": {"frozen": ["amount"], "by": ["owner"]}}}),
                by("sent"),
            ),
            // The first member it names, in order: the first state, in the
            // config's order, whose rules name by, before status.
            (
                json!({"states": {
                    "draft": {"required": ["title"]},
                    "sent": {"frozen": ["amount"], "by": ["owner"]},
                    "paid": {"frozen": "all", "by": ["owner"]}
                }, "status": "Workflow"}),
                by("sent"),
            ),
            (
                json!({"states": {"sent": {"frozen": ["amount"]}}, "status": "Workflow"}),
                status,
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/invoices/behaviors/1/config",
                    &format!("type invoices: behavior StateRules config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/invoices/behaviors/1/config: type invoices: behavior StateRules config: {reason}")
            );
        }
        // Without them, the config defines as before.
        define(
            &engine,
            &json!({"states": {"sent": {"required": ["owner"], "frozen": ["amount"]}}, "permission": "invoices.amend"}),
        )
        .expect("define");
    }
}
