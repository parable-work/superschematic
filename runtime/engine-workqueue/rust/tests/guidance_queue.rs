//! Queue's guidance, held to the TypeScript package's text for the same
//! configs (`typescript/src/guidance/queue.ts`, as `dist/guidance/queue.js`
//! returns it): the claim's states, claimNext's order and filters, what
//! keeps an instance out, and the claim_required refusal it adds to Lease's
//! acquire.

mod common;

use serde_json::{json, Value};

use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Queue, QueueClaim, QueueConfig};

use common::{as_json, target};

/// The TypeScript guidance for the configs below, as `dist/guidance/queue.js`
/// returned it under node.
const EXPECTED: &str = include_str!("guidance/queue.json");

fn names(names: &[&str]) -> Vec<String> {
    names.iter().map(|name| (*name).to_owned()).collect()
}

fn base() -> QueueConfig {
    QueueConfig {
        claim: QueueClaim {
            from: names(&["queued"]),
            to: "running".to_owned(),
        },
        priority_field: None,
        match_fields: Vec::new(),
        max_candidates: 100,
        exclude_stale: Vec::new(),
        dependencies: false,
        budget: false,
        retries: false,
        assignment: false,
        max_expiries: None,
    }
}

#[test]
fn queue_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the expected guidance");
    let cases = [
        (
            "fixture",
            QueueConfig {
                priority_field: Some("priority".to_owned()),
                match_fields: names(&["topic"]),
                budget: true,
                retries: true,
                assignment: true,
                max_expiries: Some(3),
                ..base()
            },
            json!({}),
        ),
        (
            "plain",
            QueueConfig {
                dependencies: true,
                ..base()
            },
            json!({}),
        ),
        (
            "bare",
            QueueConfig {
                claim: QueueClaim {
                    from: names(&["queued", "paused"]),
                    to: "running".to_owned(),
                },
                max_candidates: 5,
                ..base()
            },
            json!({}),
        ),
        (
            "revision",
            QueueConfig {
                exclude_stale: names(&["spec"]),
                match_fields: names(&["topic", "urgent"]),
                ..base()
            },
            json!({"Links": {"links": {"spec": {"schema": "Spec", "pinned": true}}}}),
        ),
        (
            "release",
            QueueConfig {
                exclude_stale: names(&["recipe"]),
                ..base()
            },
            json!({"Links": {"links": {"recipe": {"schema": "Recipe", "pinned": "release"}}}}),
        ),
        (
            "both",
            QueueConfig {
                exclude_stale: names(&["spec", "recipe"]),
                dependencies: true,
                budget: true,
                ..base()
            },
            json!({"Links": {"links": {
                "spec": {"schema": "Spec", "pinned": "revision"},
                "recipe": {"schema": "Recipe", "pinned": "release"}
            }}}),
        ),
    ];
    for (name, config, configs) in cases {
        let on = target(&["Queue"], configs);
        let guidance = Queue.hooks().guidance.expect("guidance");
        // Compared as text, so the operations' order counts.
        assert_eq!(
            serde_json::to_string_pretty(&as_json(&guidance(&Queue, &config, &on))).expect("JSON"),
            serde_json::to_string_pretty(&expected[name]).expect("JSON"),
            "{name}"
        );
    }
}
