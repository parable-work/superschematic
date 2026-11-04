//! What only a table's Retries takes (D104), on an engine type: a class's
//! `backoff`, and the config's `retryInto`, `then`, `giveUpAfterMs`,
//! `window`, `keyField` and `status`. The engine shares Retries'
//! declaration with the core, so its describe advertises them; it refuses
//! each at define, the first it names, naming the table form, rather than
//! ignoring a wait it would not keep. On SQLite and, with the variable set,
//! on Postgres.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, DefineOptions, Engine, Error};
use superschematic_engine_workqueue::Retries;
use support::{alice, issue, issues_of, refusal, targets};
use workqueue_support::{job_flow, jobs_document, T0};

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    let document = jobs_document(json!([
        {"name": "Workflow", "config": job_flow()},
        {"name": "Retries", "config": config}
    ]));
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

// with is the base config with members added.
fn with(members: Value) -> Value {
    let mut config = json!({
        "classes": {"timeout": {"attempts": 3}, "rejected": "terminal"},
        "totalAttempts": 4,
        "exhaustedState": "failed"
    });
    for (key, value) in members.as_object().cloned().unwrap_or_default() {
        config[key] = value;
    }
    config
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_retries_takes() {
    for target in targets("workqueue_retries_table_members") {
        // The work queue's Retries alone beside the core's behaviors, as the
        // work queue's tests register it.
        let engine = target.open_with(
            superschematic_engine_behaviors::core().with(Retries),
            |options| options.clock = Clock::Manual(T0),
        );
        let table_form = "implements Retries<T, { ... }> on a DB table, D104";
        let member = |name: &str, why: &str| {
            format!("{name} is a table's Retries' ({table_form}): an engine type's Retries {why}")
        };
        let backoff = |class: &str| {
            format!(
                "the class \"{class}\" sets backoff, which a table's Retries takes ({table_form}): an engine type's Retries keeps no time, and work that may run again may be claimed at once"
            )
        };
        let backed = json!({"attempts": 3, "backoff": {"firstMs": 1000, "maxMs": 2000}});
        let cases = [
            (
                with(json!({"classes": {"timeout": backed, "rejected": "terminal"}})),
                backoff("timeout"),
            ),
            (
                with(json!({"retryInto": ["running"]})),
                member("retryInto", "keeps no time, so no move waits for it"),
            ),
            (
                with(json!({"then": {"transition": "queued", "from": ["running"]}})),
                member("then", "arms no deadline"),
            ),
            (
                with(json!({"giveUpAfterMs": 60000})),
                member("giveUpAfterMs", "exhausts an instance by its caps alone"),
            ),
            (
                with(json!({"window": {"attempts": 2, "ms": 60000}})),
                member("window", "keeps no attempt's time"),
            ),
            (
                with(json!({"keyField": "priority"})),
                member("keyField", "counts every attempt of an instance together"),
            ),
            (
                with(json!({"status": "Workflow"})),
                member("status", "reads its one Workflow"),
            ),
            // The first it names, in order: each class's backoff, in the
            // order the document gives the classes, then the config's
            // members; and before the engine's own rules (exhaustedState
            // names no state here).
            (
                with(
                    json!({"classes": {"b": backed, "a": backed}, "status": "Workflow", "retryInto": ["running"]}),
                ),
                backoff("b"),
            ),
            (
                with(
                    json!({"keyField": "priority", "then": {"transition": "queued", "from": ["running"]}, "exhaustedState": "dead"}),
                ),
                member("then", "arms no deadline"),
            ),
        ];
        for (config, reason) in cases {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Job/behaviors/1/config",
                    &format!("type Job: behavior Retries config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Job/behaviors/1/config: type Job: behavior Retries config: {reason}")
            );
        }
        // A config without them defines as before.
        define(&engine, &with(json!({}))).expect("define");
    }
}
