//! The work-queue crate as a deployment uses it, on the Rust engine (D58):
//! the parts of the work queue's `test/package.test.ts` and
//! `test/guidance.test.ts` that the behaviors of milestone 12a run (the
//! fixture-workqueue-json document composes Queue and Budget, and its
//! batch Blueprint, which milestone 12b ports): its behaviors register in
//! `workQueueBehaviors`' order beside the core's and carry the declarations
//! the core binary writes; Lease's guidance gives its lengths and fences
//! every write but the exempt ones; and every operation of a type that
//! composes them says when to use it, in plain ASCII, which the tools
//! document and MCP's `_meta` carry. On SQLite and, with the variable set,
//! on Postgres.

mod support;
mod workqueue_support;

use serde_json::{json, Value};

use superschematic_engine::{Behavior, Engine, ToolTarget};
use superschematic_engine_workqueue::{lease, Assignment, Lease, Presence, Retries, NAMES};
use support::{alice, repository, targets};
use workqueue_support::{job_flow, jobs_document, publish};

fn declaration_file(name: &str) -> Value {
    let path = repository().join(format!(
        "runtime/engine-workqueue/typescript/src/declarations/{name}.behavior.json"
    ));
    serde_json::from_str(&std::fs::read_to_string(&path).expect("the declaration")).expect("JSON")
}

fn declared(behavior: &impl Behavior) -> Value {
    serde_json::from_str(behavior.declaration()).expect("JSON")
}

#[test]
fn its_behaviors_carry_the_core_declarations_and_register_beside_the_core_behaviors() {
    // The TypeScript package's workQueueBehaviors, in its order.
    assert_eq!(
        NAMES,
        [
            "Lease",
            "Assignment",
            "Queue",
            "Presence",
            "Blueprint",
            "Budget",
            "Retries"
        ]
    );
    assert_eq!(declared(&Lease), declaration_file("Lease"));
    assert_eq!(declared(&Assignment), declaration_file("Assignment"));
    assert_eq!(declared(&Presence), declaration_file("Presence"));
    assert_eq!(declared(&Retries), declaration_file("Retries"));
    assert_eq!(lease::DEFAULT_TTL_MS, 60000);
    let ported: Vec<&str> = NAMES
        .iter()
        .copied()
        .filter(|name| superschematic_engine_workqueue::behavior(name).is_some())
        .collect();
    assert_eq!(
        superschematic_engine_workqueue::workqueue().len(),
        ported.len()
    );
    for target in targets("workqueue_package_names") {
        let engine = target.open_workqueue();
        let mut expected: Vec<&str> = superschematic_engine_behaviors::NAMES
            .iter()
            .copied()
            .chain(ported.iter().copied())
            .collect();
        expected.sort_unstable();
        assert_eq!(engine.behaviors().names(), expected);
    }
}

fn describe(engine: &Engine, schema: &str) -> Value {
    engine
        .tools()
        .describe(&alice(), schema, ToolTarget::default())
        .expect("describe")
}

fn guidance_of(engine: &Engine, schema: &str, operation: &str) -> Value {
    describe(engine, schema)["operations"]
        .as_array()
        .unwrap()
        .iter()
        .find(|candidate| candidate["name"] == operation)
        .unwrap_or_else(|| panic!("{schema} has {operation}"))["guidance"]
        .clone()
}

fn summary_of(engine: &Engine, schema: &str, behavior: &str) -> String {
    describe(engine, schema)["behaviors"]
        .as_array()
        .unwrap()
        .iter()
        .find(|candidate| candidate["name"] == behavior)
        .and_then(|candidate| candidate["summary"].as_str())
        .unwrap_or_else(|| panic!("{schema} {behavior} has a summary"))
        .to_owned()
}

// codes are a guidance's errors as `<behavior>.<code>`.
fn codes(guidance: &Value) -> Vec<String> {
    guidance["errors"]
        .as_array()
        .into_iter()
        .flatten()
        .map(|error| {
            let description = error["description"].as_str().unwrap_or_default();
            format!(
                "{}.{}",
                description.split(':').next().unwrap_or_default(),
                error["code"].as_str().unwrap_or_default()
            )
        })
        .collect()
}

#[test]
fn lease_gives_its_lengths_and_fences_every_write_but_the_exempt_ones() {
    for target in targets("workqueue_guidance_lease") {
        let engine = target.open_workqueue();
        // Exempt operations pass, and requireToken asks every write for the
        // token.
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Comments"},
                {"name": "Lease", "config": {"ttlMs": 120000, "heartbeatMs": 20000, "exempt": ["Comments.comment"], "requireToken": true}}
            ])),
        );
        let summary = summary_of(&engine, "Job", "Lease");
        assert!(
            summary.contains(
                "for 2 minutes at a time, renewed by heartbeat at least every 20 seconds."
            ),
            "{summary}"
        );
        assert!(
            summary.contains("only its holder writes, but for Comments.comment;"),
            "{summary}"
        );
        assert_eq!(
            codes(&guidance_of(&engine, "Job", "comment")),
            Vec::<String>::new()
        );
        let transition: Vec<String> = codes(&guidance_of(&engine, "Job", "transition"))
            .into_iter()
            .filter(|code| code.starts_with("Lease."))
            .collect();
        assert_eq!(
            transition,
            [
                "Lease.held_by_another",
                "Lease.token_stale",
                "Lease.lapsed",
                "Lease.token_required"
            ]
        );
        let reset = guidance_of(&engine, "Job", "resetExpiries");
        assert!(
            reset["doNotUseWhen"]
                .as_str()
                .unwrap_or_default()
                .contains("names no overridePermission"),
            "{reset}"
        );
        // A create holds no lease, and a read is let through.
        assert!(!codes(&guidance_of(&engine, "Job", "create"))
            .contains(&"Lease.held_by_another".to_owned()));
        assert!(!codes(&guidance_of(&engine, "Job", "listComments"))
            .contains(&"Lease.held_by_another".to_owned()));
    }
}

#[test]
fn every_operation_of_the_work_queue_behaviors_says_when_to_use_it_in_plain_ascii_and_the_tools_document_and_meta_carry_it(
) {
    for target in targets("workqueue_guidance_ascii") {
        let engine = target.open_workqueue();
        publish(
            &engine,
            &jobs_document(json!([
                {"name": "Workflow", "config": job_flow()},
                {"name": "Lease", "config": {"ttlMs": 30000, "maxHoldField": "timeLimitMs", "onExpiry": {"transition": "queued", "from": ["running"]}, "maxExpiries": 3, "escalate": {"transition": "failed", "from": ["running"]}, "overridePermission": "jobs.override"}},
                {"name": "Assignment", "config": {"permission": "jobs.assign"}},
                {"name": "Retries", "config": {"classes": {"timeout": {"attempts": 3}, "rejected": "terminal"}, "totalAttempts": 4, "exhaustedState": "failed"}}
            ])),
        );
        let flow = json!({
            "states": ["idle", "busy", "missing", "stopped"],
            "transitions": [
                {"from": "idle", "to": "busy"}, {"from": "busy", "to": "idle"},
                {"from": "idle", "to": "missing"}, {"from": "busy", "to": "missing"},
                {"from": "missing", "to": "idle"}, {"from": "missing", "to": "stopped"}
            ]
        });
        publish(
            &engine,
            &json!({
                "kind": "General",
                "name": "Worker",
                "types": {"Worker": {
                    "name": "Worker",
                    "role": "EmbeddedStruct",
                    "behaviors": [
                        {"name": "Workflow", "config": flow},
                        {"name": "Presence", "config": {"ttlMs": 30000, "principalField": "subject", "onMissed": {"transition": "missing", "from": ["idle", "busy"]}, "releaseLeases": ["Job"]}}
                    ],
                    "fields": [{"name": "subject", "typeRef": {"name": "string"}, "required": true}]
                }}
            }),
        );
        let manifest = engine
            .tools()
            .manifest(&alice(), ToolTarget::default())
            .expect("manifest");
        let tools = manifest["tools"].as_array().unwrap();
        for schema in ["Job", "Worker"] {
            let document = describe(&engine, schema);
            for operation in document["operations"].as_array().unwrap() {
                let name = format!("{schema}.{}", operation["name"].as_str().unwrap());
                let guidance = &operation["guidance"];
                let text = |key: &str| guidance[key].as_str().unwrap_or_default().to_owned();
                assert!(
                    !text("useWhen").is_empty() || !text("doNotUseWhen").is_empty(),
                    "{name} says when to use it"
                );
                let mut texts = vec![text("useWhen"), text("doNotUseWhen"), text("success")];
                for error in guidance["errors"].as_array().into_iter().flatten() {
                    for key in ["description", "commonCorrection"] {
                        texts.push(error[key].as_str().unwrap_or_default().to_owned());
                    }
                }
                for text in texts {
                    assert!(
                        text.bytes().all(|byte| (0x20..=0x7e).contains(&byte)),
                        "{name}: plain ASCII, {text:?}"
                    );
                }
                let entry = tools
                    .iter()
                    .find(|tool| tool["name"] == operation["tool"])
                    .unwrap_or_else(|| panic!("{name}'s tool"));
                assert_ne!(entry["mcp"]["hidden"], true, "{name}");
                assert_eq!(
                    (
                        &entry["guidance"],
                        &entry["mcp"]["_meta"]["superschematic/operation-guidance"]
                    ),
                    (guidance, guidance),
                    "{name}"
                );
            }
        }
    }
}
