//! Presence's guidance, held to the TypeScript package's text for the same
//! configs (`typescript/src/guidance/presence.ts`): `guidance/presence.json`
//! is what the built package's `presenceGuidance`
//! (`dist/guidance/presence.js`) returned under node for each config
//! below, on a Worker type.

mod common;

use serde_json::{json, Value};

use superschematic_engine::behaviors::DescribeTarget;
use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Presence, PresenceConfig, StatusMove};

use common::{as_json, target};

const EXPECTED: &str = include_str!("guidance/presence.json");

fn moves(transition: &str, from: &[&str]) -> Option<StatusMove> {
    Some(StatusMove {
        transition: transition.to_owned(),
        from: from.iter().map(|state| (*state).to_owned()).collect(),
    })
}

#[test]
fn presences_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the recorded guidance");
    let guidance = Presence.hooks().guidance.expect("guidance");
    let worker = DescribeTarget {
        schema: "Worker".to_owned(),
        type_name: "Worker".to_owned(),
        ..target(&["Workflow", "Presence"], json!({}))
    };
    let names =
        |names: &[&str]| -> Vec<String> { names.iter().map(|name| (*name).to_owned()).collect() };
    for (key, config) in [
        (
            "plain",
            PresenceConfig {
                ttl_ms: 30_000,
                principal_field: "subject".to_owned(),
                on_missed: None,
                on_beat: None,
                release_leases: Vec::new(),
                sweep_ms: 5000,
            },
        ),
        (
            "full",
            PresenceConfig {
                ttl_ms: 90_000,
                principal_field: "subject".to_owned(),
                on_missed: moves("missing", &["idle", "busy"]),
                on_beat: moves("idle", &["missing"]),
                release_leases: names(&["Job", "Task", "Build"]),
                sweep_ms: 1500,
            },
        ),
        (
            "one",
            PresenceConfig {
                ttl_ms: 3_600_000,
                principal_field: "who".to_owned(),
                on_missed: None,
                on_beat: moves("idle", &["missing"]),
                release_leases: names(&["Job"]),
                sweep_ms: 60_000,
            },
        ),
    ] {
        assert_eq!(
            as_json(&guidance(&Presence, &config, &worker)),
            expected[key],
            "{key}"
        );
    }
}
