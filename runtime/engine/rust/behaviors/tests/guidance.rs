//! The core behaviors' guidance, held to the TypeScript engine's text for
//! the same configs (`src/behaviors/core/guidance/*.ts`, as the built
//! engine's `workflowGuidance`, `constantsGuidance`, `commentsGuidance`
//! and `variantsGuidance` return it): what milestone 6's describe document
//! shows beside each config, written as the TypeScript object is, a member
//! left out where it has none.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget};
use superschematic_engine::Behavior;
use superschematic_engine_behaviors::{
    Comments, Constants, ConstantsConfig, Variants, VariantsConfig, Workflow, WorkflowConfig,
    WorkflowOutcome, WorkflowTransition,
};

/// The TypeScript guidance for the configs below.
const EXPECTED: &str = r#"{
  "workflow": {
    "summary": "Its status is one of draft, open, shipped and cancelled; a new Order starts in draft, and status moves only by transition. Terminal, with their outcomes: shipped (success) and cancelled (failure).",
    "operations": {
      "transition": {
        "useWhen": "Use to move status from draft to open, cancelled or shipped; from open to shipped or cancelled. The moves open to shipped and open to cancelled need permission orders.ship; the move draft to shipped needs permission orders.rush; without it the call is forbidden.",
        "doNotUseWhen": "Do not use from shipped or cancelled: no transition leaves a terminal state. Do not use to stay where the instance is.",
        "success": "Returns from and to; status holds to from then on, and enteredAt the time of the move.",
        "errors": [
          {
            "code": "transition_not_allowed",
            "description": "No transition leads from the instance's status to the state named; details.allowed lists the states it can move to.",
            "commonCorrection": "Read status, then name a state details.allowed lists, or move through one of them first."
          },
          {
            "code": "already_in_state",
            "commonCorrection": "Read status: the instance is already there, so nothing needs to move."
          },
          {
            "code": "terminal_state",
            "commonCorrection": "None: the work has ended. Create a new instance to start again."
          },
          {
            "code": "no_status",
            "commonCorrection": "None: an instance without a status cannot move."
          }
        ]
      },
      "create": {
        "success": "A new instance's status is draft."
      },
      "list": {
        "useWhen": "where: { \"Workflow.status\": <state> } lists the instances in one of draft, open, shipped or cancelled, and a list of states the instances in any of them."
      },
      "update": {
        "doNotUseWhen": "Do not use to move Workflow's status, which behaviors.Workflow holds; call transition."
      }
    }
  },
  "cycle": {
    "summary": "Its status is one of on and off; a new Order starts in off, and status moves only by transition. No state is terminal: a transition leaves every state.",
    "operations": {
      "transition": {
        "useWhen": "Use to move status from on to off; from off to on.",
        "doNotUseWhen": "Do not use to stay where the instance is.",
        "success": "Returns from and to; status holds to from then on, and enteredAt the time of the move.",
        "errors": [
          {
            "code": "transition_not_allowed",
            "description": "No transition leads from the instance's status to the state named; details.allowed lists the states it can move to.",
            "commonCorrection": "Read status, then name a state details.allowed lists, or move through one of them first."
          },
          {
            "code": "already_in_state",
            "commonCorrection": "Read status: the instance is already there, so nothing needs to move."
          },
          {
            "code": "no_status",
            "commonCorrection": "None: an instance without a status cannot move."
          }
        ]
      },
      "create": {
        "success": "A new instance's status is off."
      },
      "list": {
        "useWhen": "where: { \"Workflow.status\": <state> } lists the instances in one of on or off, and a list of states the instances in any of them."
      },
      "update": {
        "doNotUseWhen": "Do not use to move Workflow's status, which behaviors.Workflow holds; call transition."
      }
    }
  },
  "constantsOne": {
    "summary": "kind keeps the value the create of each Order gives; after it, only a caller with permission steps.rename changes it.",
    "operations": {
      "create": {
        "useWhen": "Give kind at create: it does not change after it, and one left absent stays absent."
      },
      "update": {
        "doNotUseWhen": "Do not change kind without permission steps.rename: a change is refused as invalid_instance, with rule constant at the field."
      }
    }
  },
  "constantsMany": {
    "summary": "key, kind and owner keep the value the create of each Order gives; after it, nothing changes them.",
    "operations": {
      "create": {
        "useWhen": "Give key, kind and owner at create: they do not change after it, and one left absent stays absent."
      },
      "update": {
        "doNotUseWhen": "Do not change key, kind or owner: a change is refused as invalid_instance, with rule constant at the field."
      }
    }
  },
  "comments": {
    "summary": "Each Order holds a thread of comments, each one a reply to another of its comments or not; commentCount counts them.",
    "operations": {
      "comment": {
        "useWhen": "Use to add a comment to the instance, or with replyTo a reply to one of the instance's comments.",
        "doNotUseWhen": "Do not use to change the instance's fields; call update.",
        "success": "Returns the comment with its id, which a reply names as replyTo; commentCount grows by one."
      },
      "listComments": {
        "useWhen": "Use to read the instance's comments, oldest first, a page at a time.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      }
    }
  },
  "variants": {
    "summary": "On each Order, result takes the shape kind picks: result holds ReviewResult when kind is review or VerifyResult when kind is verify, and nothing while kind holds another value or none.",
    "operations": {
      "create": {
        "useWhen": "Give result the shape kind picks: result holds ReviewResult when kind is review or VerifyResult when kind is verify, and nothing while kind holds another value or none."
      },
      "update": {
        "useWhen": "A patch of result keeps the shape kind picks, as merged; the describe document shows each shape under allOf."
      }
    }
  }
}"#;

fn target() -> DescribeTarget {
    DescribeTarget {
        schema: "Order".to_owned(),
        type_name: "Order".to_owned(),
        behaviors: Vec::new(),
        configs: Map::new(),
        operations: Vec::new(),
    }
}

// as_json writes guidance as the TypeScript object holds it.
fn as_json(guidance: &BehaviorGuidance) -> Value {
    let mut operations = Map::new();
    for (name, said) in &guidance.operations {
        let mut out = Map::new();
        for (key, text) in [
            ("useWhen", &said.use_when),
            ("doNotUseWhen", &said.do_not_use_when),
            ("success", &said.success),
        ] {
            if let Some(text) = text {
                out.insert(key.into(), Value::from(text.as_str()));
            }
        }
        if !said.errors.is_empty() {
            let errors = said
                .errors
                .iter()
                .map(|error| {
                    let mut entry = Map::new();
                    entry.insert("code".into(), Value::from(error.code.as_str()));
                    if let Some(description) = &error.description {
                        entry.insert("description".into(), Value::from(description.as_str()));
                    }
                    entry.insert(
                        "commonCorrection".into(),
                        Value::from(error.common_correction.as_str()),
                    );
                    Value::Object(entry)
                })
                .collect();
            out.insert("errors".into(), Value::Array(errors));
        }
        operations.insert(name.clone(), Value::Object(out));
    }
    json!({ "summary": guidance.summary, "operations": operations })
}

fn transition(from: &str, to: &str, permission: Option<&str>) -> WorkflowTransition {
    WorkflowTransition {
        from: from.to_owned(),
        to: to.to_owned(),
        permission: permission.map(str::to_owned),
    }
}

fn names(names: &[&str]) -> Vec<String> {
    names.iter().map(|name| (*name).to_owned()).collect()
}

fn workflow(config: &WorkflowConfig) -> Value {
    let guidance = Workflow.hooks().guidance.expect("guidance");
    as_json(&guidance(&Workflow, config, &target()))
}

#[test]
fn the_guidance_is_the_typescript_engines_text() {
    let expected: Value = serde_json::from_str(EXPECTED).expect("the expected guidance");
    let orders = WorkflowConfig {
        states: names(&["draft", "open", "shipped", "cancelled"]),
        initial: "draft".to_owned(),
        transitions: vec![
            transition("draft", "open", None),
            transition("draft", "cancelled", None),
            transition("open", "shipped", Some("orders.ship")),
            transition("open", "cancelled", Some("orders.ship")),
            transition("draft", "shipped", Some("orders.rush")),
        ],
        outcomes: vec![
            ("shipped".to_owned(), WorkflowOutcome::Success),
            ("cancelled".to_owned(), WorkflowOutcome::Failure),
        ],
    };
    assert_eq!(workflow(&orders), expected["workflow"]);
    let cycle = WorkflowConfig {
        states: names(&["on", "off"]),
        initial: "off".to_owned(),
        transitions: vec![transition("on", "off", None), transition("off", "on", None)],
        outcomes: Vec::new(),
    };
    assert_eq!(workflow(&cycle), expected["cycle"]);

    let constants = Constants.hooks().guidance.expect("guidance");
    for (config, key) in [
        (
            ConstantsConfig {
                fields: names(&["kind"]),
                permission: Some("steps.rename".to_owned()),
            },
            "constantsOne",
        ),
        (
            ConstantsConfig {
                fields: names(&["key", "kind", "owner"]),
                permission: None,
            },
            "constantsMany",
        ),
    ] {
        assert_eq!(
            as_json(&constants(&Constants, &config, &target())),
            expected[key]
        );
    }

    let comments = Comments.hooks().guidance.expect("guidance");
    assert_eq!(
        as_json(&comments(&Comments, &(), &target())),
        expected["comments"]
    );

    let variants = Variants.hooks().guidance.expect("guidance");
    let config = VariantsConfig {
        field: "result".to_owned(),
        by: "kind".to_owned(),
        types: vec![
            ("review".to_owned(), "ReviewResult".to_owned()),
            ("verify".to_owned(), "VerifyResult".to_owned()),
        ],
        required: false,
    };
    assert_eq!(
        as_json(&variants(&Variants, &config, &target())),
        expected["variants"]
    );
}
