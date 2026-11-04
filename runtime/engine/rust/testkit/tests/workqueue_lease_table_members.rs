//! What only a table's Lease takes (D104), on an engine type: the config's
//! `status`, the Workflow a table's lease moves. The engine shares Lease's
//! declaration with the core, so its describe advertises it; it refuses it
//! at define, naming the table form, rather than ignoring it. On SQLite
//! and, with the variable set, on Postgres.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Clock, DefineOptions, Engine, Error};
use superschematic_engine_workqueue::Lease;
use support::{alice, issue, issues_of, refusal, targets};
use workqueue_support::{job_flow, jobs_document, T0};

fn define(engine: &Engine, config: &Value) -> Result<(), Error> {
    let document = jobs_document(json!([
        {"name": "Workflow", "config": job_flow()},
        {"name": "Lease", "config": config}
    ]));
    engine
        .schemas()
        .define(&alice(), document, DefineOptions::default())
        .map(|_| ())
}

#[test]
fn an_engine_type_refuses_what_only_a_tables_lease_takes() {
    for target in targets("workqueue_lease_table_members") {
        // The work queue's Lease alone beside the core's behaviors, as the
        // work queue's tests register it.
        let engine = target.open_with(
            superschematic_engine_behaviors::core().with(Lease),
            |options| options.clock = Clock::Manual(T0),
        );
        let reason = "status is a table's Lease's (implements Lease<T, { ... }> on a DB table, D104): an engine type's Lease moves its one Workflow";
        // status is refused whatever it names, and before the engine's own
        // rules: heartbeatMs is more than half of ttlMs here.
        for config in [
            json!({"status": "Workflow"}),
            json!({"status": "phase", "ttlMs": 10000, "heartbeatMs": 9000}),
        ] {
            let error = refusal(define(&engine, &config));
            assert_eq!(error.name(), "SchemaDocumentError", "{config}");
            assert_eq!(
                issues_of(&error),
                [issue(
                    "/types/Job/behaviors/1/config",
                    &format!("type Job: behavior Lease config: {reason}")
                )],
                "{config}"
            );
            assert_eq!(
                error.message(),
                format!("schema: /types/Job/behaviors/1/config: type Job: behavior Lease config: {reason}")
            );
        }
        // A config without it defines as before.
        define(
            &engine,
            &json!({"onExpiry": {"transition": "queued", "from": ["running"]}}),
        )
        .expect("define");
    }
}
