//! Blueprint's guidance, held to the TypeScript package's text for the
//! same configs (`typescript/src/guidance/blueprint.ts`, as
//! `dist/guidance/blueprint.js` returns it): which children a stamp creates
//! and when, what each holds from its parent, and the refusals a stamp
//! meets, on the create for inline steps and the create or the link of the
//! from link for steps read from a definition.

mod common;

use serde_json::Value;

use superschematic_engine::Behavior;
use superschematic_engine_workqueue::{Blueprint, BlueprintConfig, BlueprintFrom, BlueprintStep};

use common::{as_json, target};

/// The TypeScript guidance for the configs below, as `dist/guidance/blueprint.js`
/// returned it under node.
const EXPECTED: &str = include_str!("guidance/blueprint.json");

fn steps(keys: &[&str]) -> Option<Vec<BlueprintStep>> {
    Some(
        keys.iter()
            .map(|key| BlueprintStep {
                key: (*key).to_owned(),
                after: Vec::new(),
                when: None,
                data: serde_json::Map::new(),
            })
            .collect(),
    )
}

fn names(names: &[&str]) -> Vec<String> {
    names.iter().map(|name| (*name).to_owned()).collect()
}

fn config(
    steps: Option<Vec<BlueprintStep>>,
    from: Option<BlueprintFrom>,
    copy_fields: &[&str],
    copy_links: &[&str],
) -> BlueprintConfig {
    BlueprintConfig {
        schema: "Step".to_owned(),
        parent_link: "run".to_owned(),
        key_field: "step".to_owned(),
        steps,
        from,
        copy_fields: names(copy_fields),
        copy_links: names(copy_links),
        fields: Vec::new(),
        list_fields: Vec::new(),
    }
}

#[test]
fn blueprint_guidance_is_the_typescript_packages_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the expected guidance");
    let mut on = target(&["Blueprint"], serde_json::json!({}));
    on.schema = "Run".to_owned();
    on.type_name = "Run".to_owned();
    let from = BlueprintFrom {
        link: "plan".to_owned(),
        field: "steps".to_owned(),
    };
    for (name, config) in [
        (
            "inline",
            config(
                steps(&["design", "build", "audit", "docs", "review", "release"]),
                None,
                &["topic"],
                &[],
            ),
        ),
        ("bare", config(steps(&["only"]), None, &[], &[])),
        ("links", config(steps(&["a", "b"]), None, &[], &["area"])),
        (
            "from",
            config(
                None,
                Some(from.clone()),
                &["topic", "owner"],
                &["plan", "area"],
            ),
        ),
    ] {
        let guidance = Blueprint.hooks().guidance.expect("guidance");
        // Compared as text, so the operations' order counts.
        assert_eq!(
            serde_json::to_string_pretty(&as_json(&guidance(&Blueprint, &config, &on)))
                .expect("JSON"),
            serde_json::to_string_pretty(&expected[name]).expect("JSON"),
            "{name}"
        );
    }
}
