//! What only a table's Approvals takes (D104), on an engine type: a gate's
//! `resetOn` and `pinned`, the config's `status`, and `signOff`'s `version`.
//! The engine shares Approvals' declaration with the core, so its describe
//! advertises them; it refuses each, naming the table form, at define or as
//! a bad parameter, rather than ignoring a rule of what a sign-off holds
//! to. On SQLite and, with the variable set, on Postgres.

mod support;

use serde_json::{json, Value};

use superschematic_engine::{
    Clock, CreateOptions, DefineOptions, Engine, EngineErrorCode, Error, Principal, SchemaTarget,
    WriteOptions,
};
use support::{alice, issue, issues_of, refusal, schema_document, targets};

const START: i64 = 1_000_000;

// A document is drafted, reviewed, approved and published.
fn doc_schema(config: &Value) -> Value {
    let mut document = schema_document(
        "Doc",
        json!([
            {"name": "title", "typeRef": {"name": "string"}, "required": true},
            {"name": "body", "typeRef": {"name": "string"}}
        ]),
        json!({}),
    );
    document["types"]["Doc"]["behaviors"] = json!([
        {"name": "Workflow", "config": {
            "states": ["draft", "review", "approved", "published"],
            "transitions": [
                {"from": "draft", "to": "review"},
                {"from": "review", "to": "approved"},
                {"from": "approved", "to": "published"}
            ]
        }},
        {"name": "Approvals", "config": config}
    ]);
    document
}

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    engine
        .schemas()
        .define(&alice(), doc_schema(config), DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_approvals_takes() {
    for target in targets("approvals_table_members") {
        let engine = target.open_core_with(|options| options.clock = Clock::Manual(START));
        let table_form = "implements Approvals<T, { ... }> on a DB table, D104";
        let reset_on = |state: &str| {
            format!(
                "the gate on \"{state}\" names resetOn, which a table's Approvals takes ({table_form}): an engine type's gates follow resetOnChange"
            )
        };
        let pinned = |state: &str| {
            format!(
                "the gate on \"{state}\" sets pinned, which a table's Approvals takes ({table_form}): an engine type's sign-off names no version"
            )
        };
        let status = format!(
            "status is a table's Approvals' ({table_form}): an engine type's Approvals reads its one Workflow"
        );
        let gate = json!({"count": 1, "permission": "docs.approve"});
        let with = |member: &str, value: Value| {
            let mut gate = gate.clone();
            gate[member] = value;
            gate
        };
        let cases = [
            (
                json!({"gates": {"approved": with("resetOn", json!([]))}}),
                reset_on("approved"),
            ),
            // pinned is refused whatever its value.
            (
                json!({"gates": {"approved": with("pinned", json!(false))}}),
                pinned("approved"),
            ),
            (
                json!({"gates": {"approved": with("pinned", json!(true))}}),
                pinned("approved"),
            ),
            // The first member it names, in order: each gate's resetOn,
            // then its pinned, gate by gate in the config's order, then
            // status.
            (
                json!({"gates": {"approved": {"count": 1, "permission": "docs.approve", "resetOn": ["body"], "pinned": true}}}),
                reset_on("approved"),
            ),
            (
                json!({"gates": {
                    "approved": with("pinned", json!(true)),
                    "published": with("resetOn", json!(["body"]))
                }, "status": "Workflow"}),
                pinned("approved"),
            ),
            (
                json!({"gates": {"approved": gate.clone()}, "status": "Workflow"}),
                status,
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Doc/behaviors/1/config",
                    &format!("type Doc: behavior Approvals config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Doc/behaviors/1/config: type Doc: behavior Approvals config: {reason}")
            );
        }

        // signOff's version is a bad parameter, whatever version it names.
        define(&engine, &json!({"gates": {"approved": gate.clone()}})).expect("define");
        engine
            .schemas()
            .publish(&alice(), "Doc", SchemaTarget::default())
            .expect("publish");
        engine
            .instances()
            .create(
                &alice(),
                "Doc",
                &json!({"title": "Plan"}),
                CreateOptions {
                    id: Some("d1"),
                    ..Default::default()
                },
            )
            .expect("create");
        let ann = Principal::new("ann", ["docs.approve"]);
        for params in [
            json!({"state": "approved", "version": 1}),
            json!({"state": "approved", "version": 7, "note": "Read it."}),
        ] {
            let error = refusal(engine.instances().invoke(
                &ann,
                "Doc",
                "d1",
                "signOff",
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
                    "/version",
                    &format!("version is a table's Approvals' ({table_form}): an engine type's signOff takes state and note alone")
                )]
            );
        }
        // Without version, signOff signs as before.
        assert_eq!(
            engine
                .instances()
                .invoke(
                    &ann,
                    "Doc",
                    "d1",
                    "signOff",
                    &json!({"state": "approved"}),
                    WriteOptions::default(),
                )
                .expect("signOff"),
            json!({"state": "approved", "subject": "ann", "at": START, "signedBy": ["ann"], "count": 1})
        );
    }
}
