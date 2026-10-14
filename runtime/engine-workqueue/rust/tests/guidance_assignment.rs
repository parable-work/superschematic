//! Assignment's guidance, held to the TypeScript package's text for the
//! same configs and types (`typescript/src/guidance/assignment.ts`):
//! `guidance/assignment.json` is what the built package's
//! `assignmentGuidance` (`dist/guidance/assignment.js`) returned under
//! node for each config below, on a Job type with the operations given.

mod common;

use serde_json::{json, Value};

use superschematic_engine::behaviors::DescribeTarget;
use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Assignment, AssignmentConfig};

use common::{as_json, operation, target};

const EXPECTED: &str = include_str!("guidance/assignment.json");

#[test]
fn assignments_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the recorded guidance");
    let guidance = Assignment.hooks().guidance.expect("guidance");
    let plain = DescribeTarget {
        operations: vec![
            operation("create", None, true),
            operation("assign", Some("Assignment"), true),
            operation("unassign", Some("Assignment"), true),
        ],
        ..target(&["Assignment"], json!({}))
    };
    let queued = DescribeTarget {
        operations: vec![
            operation("create", None, true),
            operation("acquire", Some("Lease"), true),
            operation("claim", Some("Queue"), true),
            operation("assign", Some("Assignment"), true),
            operation("unassign", Some("Assignment"), true),
        ],
        ..target(&["Lease", "Assignment", "Queue"], json!({}))
    };
    let leased = DescribeTarget {
        operations: vec![
            operation("acquire", Some("Lease"), true),
            operation("assign", Some("Assignment"), true),
        ],
        ..target(&["Lease", "Assignment"], json!({}))
    };
    let permitted = AssignmentConfig {
        permission: Some("jobs.assign".to_owned()),
    };
    for (key, config, target) in [
        ("plain", AssignmentConfig::default(), plain),
        ("permitted", permitted, queued),
        ("leased", AssignmentConfig::default(), leased),
    ] {
        assert_eq!(
            as_json(&guidance(&Assignment, &config, &target)),
            expected[key],
            "{key}"
        );
    }
}
