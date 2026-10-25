//! Lease's guidance, held to the TypeScript package's text for the same
//! configs and types (`typescript/src/guidance/lease.ts`): `guidance/lease.json`
//! is what the built package's `leaseGuidance` (`dist/guidance/lease.js`)
//! returned under node for each config below, on a Job type whose
//! operations are listed as the describe document lists them.

mod common;

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{DescribeTarget, DescribedTypeOperation, OperationScope};
use superschematic_engine::Behavior;
use superschematic_engine_behaviors::LinkPin;
use superschematic_engine_workqueue::{Lease, LeaseConfig, LeaseDirectOn, StatusMove};

use common::{as_json, operation, target};

const EXPECTED: &str = include_str!("guidance/lease.json");

fn schema_operation(name: &str, behavior: &str, writes: bool) -> DescribedTypeOperation {
    DescribedTypeOperation {
        scope: OperationScope::Schema,
        ..operation(name, Some(behavior), writes)
    }
}

// builtins are the operations every type has, as describe lists them.
fn builtins() -> Vec<DescribedTypeOperation> {
    vec![
        operation("create", None, true),
        operation("get", None, false),
        operation("list", None, false),
        operation("update", None, true),
        operation("delete", None, true),
    ]
}

fn lease_operations() -> Vec<DescribedTypeOperation> {
    let mut out: Vec<DescribedTypeOperation> = [
        "acquire",
        "heartbeat",
        "release",
        "expire",
        "direct",
        "acknowledge",
        "resetExpiries",
    ]
    .iter()
    .map(|name| operation(name, Some("Lease"), true))
    .collect();
    out.push(schema_operation("expireHolder", "Lease", true));
    out
}

fn plain_target() -> DescribeTarget {
    let mut operations = builtins();
    operations.push(operation("transition", Some("Workflow"), true));
    operations.extend(lease_operations());
    operations.push(operation("comment", Some("Comments"), true));
    operations.push(operation("listComments", Some("Comments"), false));
    // An operation beside the instance (D77) asks Lease's guard nothing,
    // so its guidance names no refusal of Lease's.
    operations.push(DescribedTypeOperation {
        beside: true,
        ..operation("post", Some("Comments"), true)
    });
    DescribeTarget {
        operations,
        ..target(&["Workflow", "Lease", "Comments"], json!({}))
    }
}

fn full_target() -> DescribeTarget {
    let mut operations = builtins();
    operations.push(operation("transition", Some("Workflow"), true));
    operations.push(operation("link", Some("Links"), true));
    operations.push(schema_operation("listLinked", "Links", false));
    operations.extend(lease_operations());
    operations.push(operation("comment", Some("Comments"), true));
    operations.push(operation("claim", Some("Queue"), true));
    operations.push(schema_operation("claimNext", "Queue", true));
    operations.push(schema_operation("countClaimable", "Queue", false));
    operations.push(operation("refresh", Some("Queue"), true));
    DescribeTarget {
        operations,
        ..target(
            &["Workflow", "Links", "Lease", "Comments", "Queue"],
            json!({}),
        )
    }
}

fn moves(transition: &str, from: &[&str]) -> Option<StatusMove> {
    Some(StatusMove {
        transition: transition.to_owned(),
        from: from.iter().map(|state| (*state).to_owned()).collect(),
    })
}

fn texts(names: &[&str]) -> Vec<String> {
    names.iter().map(|name| (*name).to_owned()).collect()
}

fn plain() -> LeaseConfig {
    LeaseConfig {
        ttl_ms: 60_000,
        heartbeat_ms: 20_000,
        sweep_ms: 5000,
        max_hold_ms: None,
        max_hold_field: None,
        on_expiry: None,
        max_expiries: None,
        escalate: None,
        exempt: Vec::new(),
        require_token: false,
        acquire_permission: None,
        override_permission: None,
        direct_permission: None,
        direct_on: Vec::new(),
    }
}

fn direct_on(
    link: &str,
    schema: &str,
    pin: Option<LinkPin>,
    name: &str,
    data: Value,
) -> LeaseDirectOn {
    LeaseDirectOn {
        link: link.to_owned(),
        schema: schema.to_owned(),
        pin,
        name: name.to_owned(),
        data: match data {
            Value::Object(data) => Some(data),
            _ => None::<Map<String, Value>>,
        },
    }
}

#[test]
fn leases_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the recorded guidance");
    let guidance = Lease.hooks().guidance.expect("guidance");
    let full = LeaseConfig {
        ttl_ms: 30_000,
        heartbeat_ms: 10_000,
        sweep_ms: 2000,
        max_hold_ms: Some(90_000),
        max_hold_field: Some("timeLimitMs".to_owned()),
        on_expiry: moves("queued", &["running"]),
        max_expiries: Some(3),
        escalate: moves("failed", &["running", "queued"]),
        exempt: texts(&["Comments.comment"]),
        require_token: true,
        acquire_permission: Some("jobs.work".to_owned()),
        override_permission: Some("jobs.override".to_owned()),
        direct_permission: Some("jobs.direct".to_owned()),
        direct_on: vec![
            direct_on(
                "plan",
                "Plan",
                Some(LinkPin::Revision),
                "rebase",
                json!({"why": "the plan moved"}),
            ),
            direct_on("notes", "Plan", None, "reread", Value::Null),
            direct_on(
                "recipe",
                "Recipe",
                Some(LinkPin::Release),
                "reload",
                Value::Null,
            ),
        ],
    };
    let overridden = LeaseConfig {
        ttl_ms: 120_000,
        heartbeat_ms: 1500,
        sweep_ms: 60_000,
        max_hold_ms: Some(7_200_000),
        on_expiry: moves("queued", &["running", "failed"]),
        max_expiries: Some(1),
        exempt: texts(&["Comments.comment", "Workflow.transition"]),
        override_permission: Some("jobs.override".to_owned()),
        direct_on: vec![direct_on("notes", "Plan", None, "reread", Value::Null)],
        ..plain()
    };
    let field = LeaseConfig {
        ttl_ms: 10_000,
        heartbeat_ms: 3000,
        max_hold_field: Some("timeLimitMs".to_owned()),
        direct_permission: Some("jobs.direct".to_owned()),
        ..plain()
    };
    for (key, config, target) in [
        ("plain", plain(), plain_target()),
        ("full", full, full_target()),
        ("overridden", overridden, plain_target()),
        ("field", field, plain_target()),
    ] {
        let said = guidance(&Lease, &config, &target);
        assert_eq!(as_json(&said), expected[key], "{key}");
        let text = serde_json::to_string(&as_json(&said)).expect("JSON");
        assert!(text.is_ascii(), "{key}: plain ASCII");
    }
}
