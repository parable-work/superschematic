//! What only a table's Budget takes (D104), on an engine type: a meter's
//! reset other than daily (hourly, weekly, monthly, `{ everyMs }`) and its
//! `window`, the config's `observePermission` and `status`, and every call
//! of `observe`. The engine shares Budget's declaration with the core, so
//! its describe advertises them; it refuses each, naming the table form, at
//! define or as a bad parameter, rather than ignoring a rule of what a
//! meter counts. On SQLite and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, Principal, SchemaTarget,
    WriteOptions,
};
use support::{alice, issue, issues_of, refusal, targets};

const START: i64 = 1_000_000;

// A ledger has a name and counts its meters.
fn ledger_document(config: &Value) -> Value {
    json!({
        "kind": "General",
        "name": "Ledger",
        "types": {
            "Ledger": {
                "name": "Ledger",
                "role": "EmbeddedStruct",
                "behaviors": [{"name": "Budget", "config": config}],
                "fields": [
                    {"name": "name", "typeRef": {"name": "string"}, "required": true}
                ]
            }
        }
    })
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), ledger_document(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_budget_takes() {
    for target in targets("budget_table_members") {
        let engine = target.open_workqueue_with(|options| options.clock = Clock::Manual(START));
        let table_form = "implements Budget<T, { ... }> on a DB table, D104";
        let reset = |meter: &str, json: &str| {
            format!(
                "meter {meter}: reset {json} is a table's Budget's ({table_form}): an engine type's meter resets daily or never"
            )
        };
        let window = |meter: &str| {
            format!(
                "meter {meter}: window is a table's Budget's ({table_form}): an engine type's meter counts its current day, or everything"
            )
        };
        let observe_permission = format!(
            "observePermission is a table's Budget's ({table_form}): an engine type's meters count what its instances record, and refuse observe"
        );
        let status = format!(
            "status is a table's Budget's ({table_form}): an engine type's Budget reads its one Workflow"
        );
        let cases = [
            (
                json!({"meters": {"calls": {"limit": 10, "reset": "hourly"}}}),
                reset("calls", "\"hourly\""),
            ),
            (
                json!({"meters": {"calls": {"limit": 10, "reset": "weekly"}}}),
                reset("calls", "\"weekly\""),
            ),
            (
                json!({"meters": {"calls": {"limit": 10, "reset": "monthly"}}}),
                reset("calls", "\"monthly\""),
            ),
            (
                json!({"meters": {"calls": {"limit": 10, "reset": {"everyMs": 3_600_000}}}}),
                reset("calls", "{\"everyMs\":3600000}"),
            ),
            (
                json!({"meters": {"calls": {"limit": 10, "reset": "daily", "window": "rolling"}}}),
                window("calls"),
            ),
            (
                json!({"meters": {"calls": {"limit": 10}}, "observePermission": "ledgers.observe"}),
                observe_permission.clone(),
            ),
            (
                json!({"meters": {"calls": {"limit": 10}}, "status": "Workflow"}),
                status,
            ),
            // The first member it names: each meter's reset, then its
            // window, meter by meter in the config's order, then
            // observePermission, then status.
            (
                json!({"meters": {"calls": {"limit": 10, "window": "rolling", "reset": "hourly"}}}),
                reset("calls", "\"hourly\""),
            ),
            (
                json!({"meters": {
                    "calls": {"limit": 10, "reset": "daily", "window": "rolling"},
                    "tokens": {"limit": 10, "reset": "hourly"}
                }}),
                window("calls"),
            ),
            (
                json!({"meters": {
                    "calls": {"limit": 10, "reset": "daily"},
                    "tokens": {"limit": 10, "reset": "monthly"}
                }, "observePermission": "ledgers.observe"}),
                reset("tokens", "\"monthly\""),
            ),
            (
                json!({"meters": {"calls": {"limit": 10}}, "status": "Workflow", "observePermission": "ledgers.observe"}),
                observe_permission,
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Ledger/behaviors/0/config",
                    &format!("type Ledger: behavior Budget config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Ledger/behaviors/0/config: type Ledger: behavior Budget config: {reason}")
            );
        }

        // A daily meter is an engine type's, and observe is a bad
        // parameter, whatever it is given.
        define(
            &engine,
            &json!({"meters": {"calls": {"limit": 10, "reset": "daily"}}}),
        )
        .expect("define");
        engine
            .schemas()
            .publish(&alice(), "Ledger", SchemaTarget::default())
            .expect("publish");
        engine
            .instances()
            .create(
                &alice(),
                "Ledger",
                &json!({"name": "Main"}),
                CreateOptions {
                    id: Some("l1"),
                    ..Default::default()
                },
            )
            .expect("create");
        let observer = Principal::new("obs", ["ledgers.observe"]);
        for params in [
            json!({"meter": "calls", "used": 4}),
            json!({"meter": "calls", "used": 4, "limit": 20, "resetsAt": START + 60_000}),
        ] {
            let error = refusal(engine.instances().invoke(
                &observer,
                "Ledger",
                "l1",
                "observe",
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
                    "",
                    &format!("observe is a table's Budget's ({table_form}): an engine type's meters count what its instances record")
                )]
            );
        }
        // The meter counts what the instance records, as before.
        assert_eq!(
            engine
                .instances()
                .invoke(
                    &alice(),
                    "Ledger",
                    "l1",
                    "recordUsage",
                    &json!({"meter": "calls", "amount": 2}),
                    WriteOptions::default(),
                )
                .expect("recordUsage")["used"],
            json!(2)
        );
    }
}
