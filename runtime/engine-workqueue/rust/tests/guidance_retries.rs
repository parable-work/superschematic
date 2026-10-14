//! Retries' guidance, held to the TypeScript package's text for the same
//! configs and types (`typescript/src/guidance/retries.ts`):
//! `guidance/retries.json` is what the built package's `retriesGuidance`
//! (`dist/guidance/retries.js`) returned under node for each config below,
//! an operation's empty list of errors left out as the describe document
//! leaves it.

mod common;

use serde_json::{json, Value};

use superschematic_engine::behaviors::DescribeTarget;
use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Retries, RetriesConfig, RetriesKeepBest, RetryClass};

use common::{as_json, operation, target};

const EXPECTED: &str = include_str!("guidance/retries.json");

// without_empty_errors drops an operation's empty list of errors.
fn without_empty_errors(mut guidance: Value) -> Value {
    if let Some(operations) = guidance["operations"].as_object_mut() {
        for said in operations.values_mut() {
            if said.get("errors") == Some(&json!([])) {
                said.as_object_mut().unwrap().remove("errors");
            }
        }
    }
    guidance
}

fn names(names: &[&str]) -> Vec<String> {
    names.iter().map(|name| (*name).to_owned()).collect()
}

fn capped(name: &str, attempts: i64, hint: Option<&str>) -> (String, RetryClass) {
    (
        name.to_owned(),
        RetryClass::Capped {
            attempts,
            hint: hint.map(str::to_owned),
        },
    )
}

#[test]
fn retries_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the recorded guidance");
    let guidance = Retries.hooks().guidance.expect("guidance");
    let operations = vec![
        operation("create", None, true),
        operation("update", None, true),
        operation("transition", Some("Workflow"), true),
        operation("recordAttempt", Some("Retries"), true),
    ];
    let plain = DescribeTarget {
        operations: operations.clone(),
        ..target(&["Workflow", "Retries"], json!({}))
    };
    let mut all = operations;
    all.push(operation("acquire", Some("Lease"), true));
    all.push(operation("claim", Some("Queue"), true));
    let queued = DescribeTarget {
        operations: all,
        ..target(&["Workflow", "Lease", "Queue", "Retries"], json!({}))
    };
    let base = RetriesConfig {
        classes: vec![
            capped("timeout", 3, None),
            capped("invalid", 1, Some("Return JSON.")),
            ("rejected".to_owned(), RetryClass::Terminal),
        ],
        total_attempts: 4,
        limits_field: None,
        keep_best: None,
        stuck_after: None,
        result_field: None,
        exhausted_state: "failed".to_owned(),
        from: names(&["queued", "running"]),
        permission: None,
        limits_permission: None,
        leased: false,
    };
    let full = RetriesConfig {
        classes: vec![
            capped("invalidOutput", 2, None),
            ("rejected".to_owned(), RetryClass::Terminal),
            capped("timeout", 3, None),
        ],
        total_attempts: 1,
        limits_field: Some("caps".to_owned()),
        keep_best: Some(RetriesKeepBest {
            min_delta: 0.25,
            never_regress: names(&["compiles"]),
        }),
        stuck_after: Some(2),
        result_field: Some("report".to_owned()),
        from: names(&["running"]),
        permission: Some("jobs.work".to_owned()),
        leased: true,
        ..base.clone()
    };
    let permitted = RetriesConfig {
        classes: vec![capped("flaky", 1, None)],
        total_attempts: 3,
        limits_field: Some("caps".to_owned()),
        limits_permission: Some("jobs.caps".to_owned()),
        result_field: Some("output".to_owned()),
        from: names(&["queued"]),
        ..base.clone()
    };
    let leased_permitted = RetriesConfig {
        keep_best: Some(RetriesKeepBest {
            min_delta: 0.0,
            never_regress: Vec::new(),
        }),
        from: Vec::new(),
        leased: true,
        ..permitted.clone()
    };
    for (key, config, target) in [
        ("plain", base, plain.clone()),
        ("full", full, queued.clone()),
        ("permitted", permitted, plain),
        ("leasedPermitted", leased_permitted, queued),
    ] {
        assert_eq!(
            as_json(&guidance(&Retries, &config, &target)),
            without_empty_errors(expected[key].clone()),
            "{key}"
        );
    }
}
