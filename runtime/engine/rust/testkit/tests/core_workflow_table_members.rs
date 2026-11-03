//! What only a table's Workflow takes (D104), on an engine type: a
//! transition's `by`, the config's `overridePermission` and `repeat`, and
//! `transition`'s `from`. The engine shares Workflow's declaration with the
//! core, so its describe advertises them; it refuses each, naming the table
//! form, rather than ignoring a rule of who may move an instance. On SQLite
//! and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, DefineOptions, Engine, EngineErrorCode, Error, SchemaTarget, WriteOptions,
};
use support::{alice, issue, issues_of, refusal, schema_document, targets};

const START: i64 = 1_000_000;

// An order moves from draft to open, then ships or is cancelled.
fn order_flow() -> Value {
    json!({
        "states": ["draft", "open", "shipped", "cancelled"],
        "transitions": [
            {"from": "draft", "to": "open"},
            {"from": "draft", "to": "cancelled"},
            {"from": "open", "to": "shipped", "permission": "orders.ship"},
            {"from": "open", "to": "cancelled"}
        ]
    })
}

fn order_schema(config: &Value) -> Value {
    let mut document = schema_document(
        "Order",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "owner", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"]["Order"]["behaviors"] = json!([{"name": "Workflow", "config": config}]);
    document
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), order_schema(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_workflow_takes() {
    for target in targets("workflow_table_members") {
        let engine = target.open_core_with(|options| options.clock = Clock::Manual(START));
        let mut by = order_flow();
        by["transitions"][2]["by"] = json!(["owner"]);
        by["transitions"][3]["by"] = json!(["createdBy"]);
        let mut both = by.clone();
        both["overridePermission"] = json!("orders.moderate");
        both["repeat"] = json!("unchanged");
        let mut override_permission = order_flow();
        override_permission["overridePermission"] = json!("orders.moderate");
        let mut repeat = order_flow();
        repeat["repeat"] = json!("unchanged");
        let table_form = "status: Workflow<{ ... }> on a DB table, D104";
        let by_reason = format!(
            "the transition from \"open\" to \"shipped\" names by, which a table's Workflow takes ({table_form}): an engine type's transition names who may make it by its permission alone"
        );
        let cases = [
            (by, by_reason.clone()),
            // The first member it names, in order: a transition's by first.
            (both, by_reason),
            (
                override_permission,
                format!("overridePermission is a table's Workflow's ({table_form}): an engine type's transitions name no by for it to move a caller past"),
            ),
            (
                repeat,
                format!("repeat is a table's Workflow's ({table_form}): on an engine type a transition to the state an instance is in stays the veto already_in_state"),
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Order/behaviors/0/config",
                    &format!("type Order: behavior Workflow config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Order/behaviors/0/config: type Order: behavior Workflow config: {reason}")
            );
        }

        // transition's from is a bad parameter, whatever state it names.
        define(&engine, &order_flow()).expect("define");
        engine
            .schemas()
            .publish(&alice(), "Order", SchemaTarget::default())
            .expect("publish");
        engine
            .instances()
            .create(
                &alice(),
                "Order",
                &json!({"title": "Desk"}),
                superschematic_engine::CreateOptions {
                    id: Some("o1"),
                    ..Default::default()
                },
            )
            .expect("create");
        for params in [
            json!({"to": "open", "from": "draft"}),
            json!({"to": "open", "from": "open"}),
        ] {
            let error = refusal(engine.instances().invoke(
                &alice(),
                "Order",
                "o1",
                "transition",
                &params,
                WriteOptions::default(),
            ));
            assert_eq!(
                (error.name(), error.code()),
                ("OperationParamsError", EngineErrorCode::InvalidArgument),
                "{params}"
            );
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/from",
                    &format!("from is a table's Workflow's ({table_form}): an engine type's transition takes to alone")
                )]
            );
        }
        // Without from, transition moves the instance as before.
        assert_eq!(
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Order",
                    "o1",
                    "transition",
                    &json!({"to": "open"}),
                    WriteOptions::default(),
                )
                .expect("transition"),
            json!({"from": "draft", "to": "open"})
        );
    }
}
