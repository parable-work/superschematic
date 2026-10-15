//! Budget's guidance, held to the TypeScript package's text for the same
//! configs (`typescript/src/guidance/budget.ts`, as `dist/guidance/budget.js`
//! returns it): each meter's limit, reservation, scope and day, what a
//! reservation needs, what usage over a limit does, and the refusals its
//! guard adds to a scope link's moves, a limit field's updates and a claim.

mod common;

use serde_json::Value;

use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Budget, BudgetConfig, BudgetMeter, BudgetTransition};

use common::{as_json, operation, target};

/// The TypeScript guidance for the configs below, as `dist/guidance/budget.js`
/// returned it under node.
const EXPECTED: &str = include_str!("guidance/budget.json");

fn meter(name: &str, spec: BudgetMeter) -> (String, BudgetMeter) {
    (name.to_owned(), spec)
}

fn guidance(config: &BudgetConfig, operations: &[&str]) -> Value {
    let mut on = target(&["Budget"], serde_json::json!({}));
    on.operations = operations
        .iter()
        .map(|name| operation(name, None, true))
        .collect();
    let guidance = Budget.hooks().guidance.expect("guidance");
    as_json(&guidance(&Budget, config, &on))
}

#[test]
fn budget_guidance_is_the_typescript_packages_text() {
    let fixture = BudgetConfig {
        meters: vec![meter(
            "cpuSeconds",
            BudgetMeter {
                limit: Some(3600),
                reserve: Some(600),
                daily: true,
                ..BudgetMeter::default()
            },
        )],
        limit_permission: Some("jobs.budget".to_owned()),
        leased: true,
        ..BudgetConfig::default()
    };
    let scoped = BudgetConfig {
        meters: vec![meter(
            "cpu",
            BudgetMeter {
                reserve_field: Some("timeLimitMs".to_owned()),
                scope: Some("pool".to_owned()),
                ..BudgetMeter::default()
            },
        )],
        ..BudgetConfig::default()
    };
    let full = BudgetConfig {
        meters: vec![
            meter(
                "cpu",
                BudgetMeter {
                    limit: Some(80),
                    limit_field: Some("cap".to_owned()),
                    reserve: Some(60),
                    reserve_field: Some("estimate".to_owned()),
                    scope: Some("run".to_owned()),
                    daily: true,
                },
            ),
            meter("calls", BudgetMeter::default()),
            meter(
                "gpu",
                BudgetMeter {
                    limit: Some(10),
                    scope: Some("pool".to_owned()),
                    ..BudgetMeter::default()
                },
            ),
            meter(
                "disk",
                BudgetMeter {
                    limit_field: Some("quota".to_owned()),
                    scope: Some("run".to_owned()),
                    ..BudgetMeter::default()
                },
            ),
        ],
        limit_permission: Some("budget.limit".to_owned()),
        on_exceeded: Some("meterExceeded".to_owned()),
        escalate: Some(BudgetTransition {
            transition: "failed".to_owned(),
            from: vec!["running".to_owned(), "queued".to_owned()],
        }),
        leased: true,
    };
    let unnamed = BudgetConfig {
        meters: vec![meter(
            "cpu",
            BudgetMeter {
                limit_field: Some("cap".to_owned()),
                ..BudgetMeter::default()
            },
        )],
        escalate: Some(BudgetTransition {
            transition: "paused".to_owned(),
            from: vec!["open".to_owned()],
        }),
        ..BudgetConfig::default()
    };
    let expected: Value = serde_json::from_str(EXPECTED).expect("the expected guidance");
    for (name, config, operations) in [
        ("fixture", &fixture, &["claim", "reserve", "acquire"][..]),
        ("scoped", &scoped, &["link", "unlink", "update"][..]),
        ("full", &full, &["link", "unlink", "update", "claim"][..]),
        ("unnamed", &unnamed, &["update"][..]),
    ] {
        // Compared as text, so the operations' order counts.
        assert_eq!(
            serde_json::to_string_pretty(&guidance(config, operations)).expect("JSON"),
            serde_json::to_string_pretty(&expected[name]).expect("JSON"),
            "{name}"
        );
    }
}
