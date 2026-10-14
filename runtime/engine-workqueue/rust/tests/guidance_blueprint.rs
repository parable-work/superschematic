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

/// The TypeScript guidance for the configs below.
const EXPECTED: &str = r#"{
  "inline": {
    "summary": "The create of a Run stamps a child per step, design, build, audit, docs, review and release, a step whose when does not hold left out. Each child is a Step instance whose step holds its step's key and whose run links to its parent, copying topic; a step after others is blocked by their children, so claimable children are claimed in that order.",
    "operations": {
      "create": {
        "success": "The create stamps the children (design, build, audit, docs, review and release) in its own write; behaviors.Blueprint.children lists them.",
        "errors": [
          {
            "code": "no_dependencies",
            "commonCorrection": "None: the child schema's live version must compose Dependencies for steps that come after others."
          },
          {
            "code": "not_constant",
            "commonCorrection": "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."
          }
        ]
      }
    }
  },
  "bare": {
    "summary": "The create of a Run stamps a child per step, only, a step whose when does not hold left out. Each child is a Step instance whose step holds its step's key and whose run links to its parent; a step after others is blocked by their children, so claimable children are claimed in that order.",
    "operations": {
      "create": {
        "success": "The create stamps the children (only) in its own write; behaviors.Blueprint.children lists them.",
        "errors": [
          {
            "code": "no_dependencies",
            "commonCorrection": "None: the child schema's live version must compose Dependencies for steps that come after others."
          },
          {
            "code": "not_constant",
            "commonCorrection": "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."
          }
        ]
      }
    }
  },
  "links": {
    "summary": "The create of a Run stamps a child per step, a and b, a step whose when does not hold left out. Each child is a Step instance whose step holds its step's key and whose run links to its parent, copying the area link; a step after others is blocked by their children, so claimable children are claimed in that order.",
    "operations": {
      "create": {
        "success": "The create stamps the children (a and b) in its own write; behaviors.Blueprint.children lists them.",
        "errors": [
          {
            "code": "no_dependencies",
            "commonCorrection": "None: the child schema's live version must compose Dependencies for steps that come after others."
          },
          {
            "code": "not_constant",
            "commonCorrection": "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."
          }
        ]
      }
    }
  },
  "from": {
    "summary": "When the plan link of a Run is first set, at its create or by link, the steps are read from steps of the revision it pins, and a child is stamped per step. Each child is a Step instance whose step holds its step's key and whose run links to its parent, copying topic, owner, the plan link and the area link; a step after others is blocked by their children, so claimable children are claimed in that order. Once stamped, plan does not move.",
    "operations": {
      "create": {
        "success": "A create that gives the plan link stamps its children in the same write; behaviors.Blueprint.children lists them.",
        "errors": [
          {
            "code": "no_revision",
            "commonCorrection": "Link a definition that has a revision, or wait until it has one."
          },
          {
            "code": "unreadable",
            "commonCorrection": "None: the caller cannot read the definition's revision; a caller who may read it stamps."
          },
          {
            "code": "invalid_steps",
            "commonCorrection": "Fix the steps in the definition, as the message says, and link its new revision."
          },
          {
            "code": "no_dependencies",
            "commonCorrection": "None: the child schema's live version must compose Dependencies for steps that come after others."
          },
          {
            "code": "not_constant",
            "commonCorrection": "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."
          }
        ]
      },
      "link": {
        "doNotUseWhen": "Do not move plan once the children are stamped.",
        "success": "Setting plan for the first time stamps the children in the same write.",
        "errors": [
          {
            "code": "stamped",
            "commonCorrection": "None: the children were stamped from the revision it pins, so the link stays."
          },
          {
            "code": "no_revision",
            "commonCorrection": "Link a definition that has a revision, or wait until it has one."
          },
          {
            "code": "unreadable",
            "commonCorrection": "None: the caller cannot read the definition's revision; a caller who may read it stamps."
          },
          {
            "code": "invalid_steps",
            "commonCorrection": "Fix the steps in the definition, as the message says, and link its new revision."
          },
          {
            "code": "no_dependencies",
            "commonCorrection": "None: the child schema's live version must compose Dependencies for steps that come after others."
          },
          {
            "code": "not_constant",
            "commonCorrection": "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."
          }
        ]
      }
    }
  }
}"#;

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
