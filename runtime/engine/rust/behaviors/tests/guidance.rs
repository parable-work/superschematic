//! The core behaviors' guidance, held to the TypeScript engine's text for
//! the same configs (`src/behaviors/core/guidance/*.ts`, as the built
//! engine's `workflowGuidance`, `constantsGuidance`, `commentsGuidance`
//! and `variantsGuidance` return it): what milestone 6's describe document
//! shows beside each config, written as the TypeScript object is, a member
//! left out where it has none.

use serde_json::{json, Map, Value};

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget};
use superschematic_engine::json::stringify;
use superschematic_engine::Behavior;
use superschematic_engine_behaviors::{
    Comments, Constants, ConstantsConfig, Dependencies, DependenciesConfig, LinkPin, LinkSpec,
    Links, LinksConfig, Numbering, NumberingConfig, Revisions, RevisionsConfig, RevisionsReview,
    Variants, VariantsConfig, Workflow, WorkflowConfig, WorkflowOutcome, WorkflowTransition,
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

/// StateRules' TypeScript guidance (`stateRulesGuidance`) for the configs
/// below, on an Invoice whose fields are title, customer, amount, lines and
/// notes and whose Workflow starts in draft.
const STATERULES_EXPECTED: &str = r#"{
  "described": {
    "summary": "Entering draft needs title, which stays set while it is draft. Entering sent needs customer and amount, which stay set while it is sent; customer and amount do not change while it is sent. No field changes while it is paid. Only a caller with permission invoices.amend changes a frozen field. A field is held when it is neither absent nor null.",
    "operations": {
      "update": {
        "doNotUseWhen": "While it is draft, do not clear title; while it is sent, do not change or clear customer or amount; while it is paid, do not change any field (a caller with permission invoices.amend may change a frozen field, but not clear a required one).",
        "errors": [
          {
            "code": "required_fields",
            "description": "An update that clears a field the instance's state requires; details.details.fields lists them.",
            "commonCorrection": "Give the field a value instead of clearing it, or move the instance to a state that does not require it first."
          },
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission invoices.amend."
          }
        ]
      },
      "transition": {
        "doNotUseWhen": "Do not move into draft before title holds a value, or sent before customer and amount hold values.",
        "errors": [
          {
            "code": "missing_fields",
            "description": "A move into draft or sent while a field it requires is absent or null; details.details.fields lists them.",
            "commonCorrection": "Update the instance to give the fields the state requires, then move it."
          }
        ]
      },
      "create": {
        "useWhen": "Give title at create: draft, the state a new Invoice starts in, requires it, and a create without it is refused as invalid_instance, with rule required at the field."
      },
      "approve": {
        "doNotUseWhen": "Do not approve a proposal whose patch breaks the rules of the instance's state: propose accepts it, and approve is refused.",
        "errors": [
          {
            "code": "required_fields",
            "description": "An update that clears a field the instance's state requires; details.details.fields lists them.",
            "commonCorrection": "Give the field a value instead of clearing it, or move the instance to a state that does not require it first."
          },
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission invoices.amend."
          }
        ]
      }
    }
  },
  "plain": {
    "summary": "No field changes while it is paid. Nobody changes a frozen field. A field is held when it is neither absent nor null.",
    "operations": {
      "update": {
        "doNotUseWhen": "While it is paid, do not change any field.",
        "errors": [
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, or move the instance to a state that does not freeze it first."
          }
        ]
      }
    }
  },
  "mixed": {
    "summary": "Entering draft needs title and customer, which stay set while it is draft. Entering sent needs customer, which stays set while it is sent; amount and lines do not change while it is sent. Notes does not change while it is void. Only a caller with permission invoices.amend changes a frozen field. A field is held when it is neither absent nor null.",
    "operations": {
      "update": {
        "doNotUseWhen": "While it is draft, do not clear title or customer; while it is sent, do not clear customer or change amount or lines; while it is void, do not change notes (a caller with permission invoices.amend may change a frozen field, but not clear a required one).",
        "errors": [
          {
            "code": "required_fields",
            "description": "An update that clears a field the instance's state requires; details.details.fields lists them.",
            "commonCorrection": "Give the field a value instead of clearing it, or move the instance to a state that does not require it first."
          },
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission invoices.amend."
          }
        ]
      },
      "transition": {
        "doNotUseWhen": "Do not move into draft before title and customer hold values, or sent before customer holds a value.",
        "errors": [
          {
            "code": "missing_fields",
            "description": "A move into draft or sent while a field it requires is absent or null; details.details.fields lists them.",
            "commonCorrection": "Update the instance to give the fields the state requires, then move it."
          }
        ]
      },
      "create": {
        "useWhen": "Give title and customer at create: draft, the state a new Invoice starts in, requires them, and a create without them is refused as invalid_instance, with rule required at the field."
      }
    }
  },
  "frozenOnly": {
    "summary": "Amount does not change while it is sent. Only a caller with permission invoices.amend changes a frozen field. A field is held when it is neither absent nor null.",
    "operations": {
      "update": {
        "doNotUseWhen": "While it is sent, do not change amount (a caller with permission invoices.amend may change a frozen field).",
        "errors": [
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission invoices.amend."
          }
        ]
      },
      "approve": {
        "doNotUseWhen": "Do not approve a proposal whose patch breaks the rules of the instance's state: propose accepts it, and approve is refused.",
        "errors": [
          {
            "code": "frozen_fields",
            "description": "An update that changes a field the instance's state freezes; details.details.fields lists them.",
            "commonCorrection": "Leave the field as it is, move the instance to a state that does not freeze it first, or ask a caller with permission invoices.amend."
          }
        ]
      }
    }
  }
}"#;

#[test]
fn the_staterules_guidance_is_the_typescript_engines_text() {
    use superschematic_engine::behaviors::{DescribedTypeOperation, OperationScope};
    use superschematic_engine_behaviors::{StateRule, StateRules, StateRulesConfig};

    let expected: Value = serde_json::from_str(STATERULES_EXPECTED).expect("the expected guidance");
    let fields = names(&["title", "customer", "amount", "lines", "notes"]);
    let rule = |required: &[&str], frozen: &[&str], all: bool| StateRule {
        required: names(required),
        frozen: if all { fields.clone() } else { names(frozen) },
        frozen_all: all,
    };
    let invoice = |approve: bool| DescribeTarget {
        schema: "invoices".to_owned(),
        type_name: "Invoice".to_owned(),
        operations: if approve {
            vec![DescribedTypeOperation {
                name: "approve".to_owned(),
                behavior: Some("Revisions".to_owned()),
                writes: true,
                scope: OperationScope::Instance,
            }]
        } else {
            Vec::new()
        },
        ..target()
    };
    let config = |states: Vec<(&str, StateRule)>, permission: Option<&str>| StateRulesConfig {
        states: states
            .into_iter()
            .map(|(state, rule)| (state.to_owned(), rule))
            .collect(),
        permission: permission.map(str::to_owned),
        initial: Some("draft".to_owned()),
    };
    let guidance = StateRules.hooks().guidance.expect("guidance");
    for (key, config, approve) in [
        (
            "described",
            config(
                vec![
                    ("draft", rule(&["title"], &[], false)),
                    (
                        "sent",
                        rule(&["customer", "amount"], &["customer", "amount"], false),
                    ),
                    ("paid", rule(&[], &[], true)),
                ],
                Some("invoices.amend"),
            ),
            true,
        ),
        (
            "plain",
            config(vec![("paid", rule(&[], &[], true))], None),
            false,
        ),
        (
            "mixed",
            config(
                vec![
                    ("draft", rule(&["title", "customer"], &[], false)),
                    ("sent", rule(&["customer"], &["amount", "lines"], false)),
                    ("void", rule(&[], &["notes"], false)),
                ],
                Some("invoices.amend"),
            ),
            false,
        ),
        (
            "frozenOnly",
            config(
                vec![("sent", rule(&[], &["amount"], false))],
                Some("invoices.amend"),
            ),
            true,
        ),
    ] {
        assert_eq!(
            as_json(&guidance(&StateRules, &config, &invoice(approve))),
            expected[key],
            "{key}"
        );
    }
}

/// Approvals' TypeScript guidance (`approvalsGuidance`) for the configs
/// below, on a Doc.
const APPROVALS_EXPECTED: &str = r#"{
  "standard": {
    "summary": "A move into approved waits for 2 sign-offs by callers with permission docs.approve; a move into published from approved waits for 1 sign-off by callers with permission docs.publish. The principal who created a Doc cannot sign off on it. behaviors.Approvals.gates lists the sign-offs that count, by state. A change of the own fields consumes every sign-off, and entering a gated state consumes its own, so each is signed again after.",
    "operations": {
      "signOff": {
        "useWhen": "Use to sign off for approved or published, as the caller; it needs the gate's permission, else the call is forbidden.",
        "doNotUseWhen": "Do not use while the instance is in the state, or to sign twice.",
        "success": "Returns the sign-off, the subjects whose sign-offs for the state count (signedBy) and the count the gate needs.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved or published."
          },
          {
            "code": "in_state",
            "commonCorrection": "Wait until the instance has left the state; a sign-off counts toward the next move into it."
          },
          {
            "code": "creator_cannot_sign",
            "commonCorrection": "Ask another principal with the permission to sign off."
          },
          {
            "code": "already_signed",
            "commonCorrection": "Leave it: the caller has signed off for the state already."
          }
        ]
      },
      "withdrawSignOff": {
        "useWhen": "Use to take back the caller's own sign-off for a gated state before the move it waits for.",
        "success": "Returns the sign-off withdrawn; it no longer counts.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved or published."
          },
          {
            "code": "not_signed",
            "commonCorrection": "None: the caller has no sign-off for the state that counts."
          }
        ]
      },
      "listSignOffs": {
        "useWhen": "Use to read the instance's sign-offs, oldest first, the consumed ones with consumedAt; with state, only those for it.",
        "doNotUseWhen": "Do not page through them for the ones that count; read behaviors.Approvals.gates.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "transition": {
        "doNotUseWhen": "Read behaviors.Approvals.gates before a move: a move into approved waits for 2 sign-offs by callers with permission docs.approve; a move into published from approved waits for 1 sign-off by callers with permission docs.publish.",
        "errors": [
          {
            "code": "approvals_missing",
            "description": "A move into a gated state with fewer sign-offs than its count; details.details gives the state, the count it requires and the sign-offs it has.",
            "commonCorrection": "Ask callers with the gate's permission to sign off, then try again."
          }
        ]
      },
      "update": {
        "success": "A change of the own fields consumes every sign-off: each gate is signed again before its move."
      }
    }
  },
  "lenient": {
    "summary": "A move into approved waits for 1 sign-off by callers with permission docs.approve. behaviors.Approvals.gates lists the sign-offs that count, by state. Entering a gated state consumes its sign-offs, so a return to it is signed again; a change of the own fields keeps them.",
    "operations": {
      "signOff": {
        "useWhen": "Use to sign off for approved, as the caller; it needs the gate's permission, else the call is forbidden.",
        "doNotUseWhen": "Do not use while the instance is in the state, or to sign twice.",
        "success": "Returns the sign-off, the subjects whose sign-offs for the state count (signedBy) and the count the gate needs.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved."
          },
          {
            "code": "in_state",
            "commonCorrection": "Wait until the instance has left the state; a sign-off counts toward the next move into it."
          },
          {
            "code": "already_signed",
            "commonCorrection": "Leave it: the caller has signed off for the state already."
          }
        ]
      },
      "withdrawSignOff": {
        "useWhen": "Use to take back the caller's own sign-off for a gated state before the move it waits for.",
        "success": "Returns the sign-off withdrawn; it no longer counts.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved."
          },
          {
            "code": "not_signed",
            "commonCorrection": "None: the caller has no sign-off for the state that counts."
          }
        ]
      },
      "listSignOffs": {
        "useWhen": "Use to read the instance's sign-offs, oldest first, the consumed ones with consumedAt; with state, only those for it.",
        "doNotUseWhen": "Do not page through them for the ones that count; read behaviors.Approvals.gates.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "transition": {
        "doNotUseWhen": "Read behaviors.Approvals.gates before a move: a move into approved waits for 1 sign-off by callers with permission docs.approve.",
        "errors": [
          {
            "code": "approvals_missing",
            "description": "A move into a gated state with fewer sign-offs than its count; details.details gives the state, the count it requires and the sign-offs it has.",
            "commonCorrection": "Ask callers with the gate's permission to sign off, then try again."
          }
        ]
      }
    }
  },
  "mixed": {
    "summary": "A move into approved from review or draft waits for 3 sign-offs by callers with permission docs.approve; a move into published waits for 1 sign-off by callers with permission docs.publish. The principal who created a Doc cannot sign off on it for approved. behaviors.Approvals.gates lists the sign-offs that count, by state. A change of the own fields consumes every sign-off, and entering a gated state consumes its own, so each is signed again after.",
    "operations": {
      "signOff": {
        "useWhen": "Use to sign off for approved or published, as the caller; it needs the gate's permission, else the call is forbidden.",
        "doNotUseWhen": "Do not use while the instance is in the state, or to sign twice.",
        "success": "Returns the sign-off, the subjects whose sign-offs for the state count (signedBy) and the count the gate needs.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved or published."
          },
          {
            "code": "in_state",
            "commonCorrection": "Wait until the instance has left the state; a sign-off counts toward the next move into it."
          },
          {
            "code": "creator_cannot_sign",
            "commonCorrection": "Ask another principal with the permission to sign off."
          },
          {
            "code": "already_signed",
            "commonCorrection": "Leave it: the caller has signed off for the state already."
          }
        ]
      },
      "withdrawSignOff": {
        "useWhen": "Use to take back the caller's own sign-off for a gated state before the move it waits for.",
        "success": "Returns the sign-off withdrawn; it no longer counts.",
        "errors": [
          {
            "code": "no_gate",
            "commonCorrection": "Name a gated state: approved or published."
          },
          {
            "code": "not_signed",
            "commonCorrection": "None: the caller has no sign-off for the state that counts."
          }
        ]
      },
      "listSignOffs": {
        "useWhen": "Use to read the instance's sign-offs, oldest first, the consumed ones with consumedAt; with state, only those for it.",
        "doNotUseWhen": "Do not page through them for the ones that count; read behaviors.Approvals.gates.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "transition": {
        "doNotUseWhen": "Read behaviors.Approvals.gates before a move: a move into approved from review or draft waits for 3 sign-offs by callers with permission docs.approve; a move into published waits for 1 sign-off by callers with permission docs.publish.",
        "errors": [
          {
            "code": "approvals_missing",
            "description": "A move into a gated state with fewer sign-offs than its count; details.details gives the state, the count it requires and the sign-offs it has.",
            "commonCorrection": "Ask callers with the gate's permission to sign off, then try again."
          }
        ]
      },
      "update": {
        "success": "A change of the own fields consumes every sign-off: each gate is signed again before its move."
      }
    }
  }
}"#;

#[test]
fn the_approvals_guidance_is_the_typescript_engines_text() {
    use superschematic_engine_behaviors::{Approvals, ApprovalsConfig, ApprovalsGate};

    let expected: Value = serde_json::from_str(APPROVALS_EXPECTED).expect("the expected guidance");
    let gate = |count: i64, permission: &str, from: Option<&[&str]>, exclude_creator: bool| {
        ApprovalsGate {
            count,
            permission: permission.to_owned(),
            from: from.map(names),
            exclude_creator,
        }
    };
    let doc = DescribeTarget {
        schema: "Doc".to_owned(),
        type_name: "Doc".to_owned(),
        ..target()
    };
    let guidance = Approvals.hooks().guidance.expect("guidance");
    for (key, config) in [
        (
            "standard",
            ApprovalsConfig {
                gates: vec![
                    ("approved".to_owned(), gate(2, "docs.approve", None, true)),
                    (
                        "published".to_owned(),
                        gate(1, "docs.publish", Some(&["approved"]), true),
                    ),
                ],
                reset_on_change: true,
            },
        ),
        (
            "lenient",
            ApprovalsConfig {
                gates: vec![("approved".to_owned(), gate(1, "docs.approve", None, false))],
                reset_on_change: false,
            },
        ),
        (
            "mixed",
            ApprovalsConfig {
                gates: vec![
                    (
                        "approved".to_owned(),
                        gate(3, "docs.approve", Some(&["review", "draft"]), true),
                    ),
                    ("published".to_owned(), gate(1, "docs.publish", None, false)),
                ],
                reset_on_change: true,
            },
        ),
    ] {
        assert_eq!(
            as_json(&guidance(&Approvals, &config, &doc)),
            expected[key],
            "{key}"
        );
    }
}

/// Timers' TypeScript guidance (`timersGuidance`) for the configs below,
/// on a Ticket, each deadline's then.from filled in as `parseConfig` fills
/// it.
const TIMERS_EXPECTED: &str = r#"{
  "described": {
    "summary": "Deadline close is due 30 days after the create; when it fires, it moves the status to closed from open; it is cancelled when the status enters resolved. Deadline remind is due 1 day before the time visitAt holds. Deadline respond is due 4 hours after the status enters open; when it fires, it moves the status to escalated from open; it is cancelled when the status leaves open. behaviors.Timers.deadlines gives each started deadline's dueAt, fired, firedAt and outcome; one not fired is overdue once its dueAt has passed. The runner fires each due deadline within 1 minute, and a deadline stays fired whatever its move meets. A caller with permission tickets.timers may reset or snooze a deadline.",
    "operations": {
      "fireTimer": {
        "useWhen": "Use to fire a due deadline at once rather than wait for the runner's sweep.",
        "doNotUseWhen": "Do not use to move the instance early: on a deadline not due, fired already or not started, the call changes nothing.",
        "success": "Returns fired, whether this call fired the deadline, and with a then the outcome of its move: moved, skipped (the status was not one it moves from), vetoed or forbidden; the deadline is fired whichever.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: close, remind or respond."
          }
        ]
      },
      "resetTimer": {
        "useWhen": "Use to start a deadline again from what it starts from now, undoing a snooze or a fire. It needs permission tickets.timers, else the call is forbidden.",
        "doNotUseWhen": "Do not use to put a deadline off by a time of your choosing; snoozeTimer takes the dueAt.",
        "success": "Returns dueAt, afterMs after what the deadline starts from; it has not fired.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: close, remind or respond."
          },
          {
            "code": "not_armed",
            "commonCorrection": "Wait until the instance holds what the deadline starts from, the state it enters or a date-time in its field, and is out of the states that cancel it."
          }
        ]
      },
      "snoozeTimer": {
        "useWhen": "Use to move a started deadline's due time to dueAt, in epoch milliseconds; a fired one fires again then. It needs permission tickets.timers, else the call is forbidden.",
        "doNotUseWhen": "Do not use on a deadline that has not started or was cancelled; behaviors.Timers.deadlines lists the started ones.",
        "success": "Returns dueAt. A later write keeps it, until what the deadline starts from changes.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: close, remind or respond."
          },
          {
            "code": "not_armed",
            "commonCorrection": "None: the deadline has no due time to move. Read behaviors.Timers.deadlines."
          }
        ]
      },
      "create": {
        "success": "A new Ticket starts deadline close, due 30 days later. A visitAt the create gives starts deadline remind."
      },
      "transition": {
        "success": "Entering resolved cancels deadline close. Entering open starts deadline respond, due 4 hours later. Leaving open cancels deadline respond."
      },
      "update": {
        "success": "Changing visitAt moves deadline remind, which fires again when it comes due; clearing visitAt cancels it."
      }
    }
  },
  "closed": {
    "summary": "Deadline ping is due atthe time visitAt holds. behaviors.Timers.deadlines gives each started deadline's dueAt, fired, firedAt and outcome; one not fired is overdue once its dueAt has passed. The runner fires each due deadline within 5 seconds, and a deadline stays fired whatever its move meets. Nobody resets or snoozes a deadline: the config names no permission.",
    "operations": {
      "fireTimer": {
        "useWhen": "Use to fire a due deadline at once rather than wait for the runner's sweep.",
        "doNotUseWhen": "Do not use to move the instance early: on a deadline not due, fired already or not started, the call changes nothing.",
        "success": "Returns fired, whether this call fired the deadline, and with a then the outcome of its move: moved, skipped (the status was not one it moves from), vetoed or forbidden; the deadline is fired whichever.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: ping."
          }
        ]
      },
      "resetTimer": {
        "useWhen": "",
        "doNotUseWhen": "Do not use: the config names no permission, so the call is forbidden for every caller.",
        "success": "Returns dueAt, afterMs after what the deadline starts from; it has not fired.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: ping."
          },
          {
            "code": "not_armed",
            "commonCorrection": "Wait until the instance holds what the deadline starts from, the state it enters or a date-time in its field, and is out of the states that cancel it."
          }
        ]
      },
      "snoozeTimer": {
        "useWhen": "",
        "doNotUseWhen": "Do not use: the config names no permission, so the call is forbidden for every caller.",
        "success": "Returns dueAt. A later write keeps it, until what the deadline starts from changes.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: ping."
          },
          {
            "code": "not_armed",
            "commonCorrection": "None: the deadline has no due time to move. Read behaviors.Timers.deadlines."
          }
        ]
      },
      "create": {
        "success": "A visitAt the create gives starts deadline ping."
      },
      "update": {
        "success": "Changing visitAt moves deadline ping, which fires again when it comes due; clearing visitAt cancels it."
      }
    }
  },
  "mixed": {
    "summary": "Deadline idle is due 90 seconds after the status enters waiting; it is cancelled when the status enters resolved or closed. Deadline nudge is due 1500 ms after the time visitAt holds; when it fires, it moves the status to escalated from open or waiting. Deadline ping is due at the create. Deadline wake is due when the status enters open. behaviors.Timers.deadlines gives each started deadline's dueAt, fired, firedAt and outcome; one not fired is overdue once its dueAt has passed. The runner fires each due deadline within 7 seconds, and a deadline stays fired whatever its move meets. A caller with permission tickets.timers may reset or snooze a deadline.",
    "operations": {
      "fireTimer": {
        "useWhen": "Use to fire a due deadline at once rather than wait for the runner's sweep.",
        "doNotUseWhen": "Do not use to move the instance early: on a deadline not due, fired already or not started, the call changes nothing.",
        "success": "Returns fired, whether this call fired the deadline, and with a then the outcome of its move: moved, skipped (the status was not one it moves from), vetoed or forbidden; the deadline is fired whichever.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: idle, nudge, ping or wake."
          }
        ]
      },
      "resetTimer": {
        "useWhen": "Use to start a deadline again from what it starts from now, undoing a snooze or a fire. It needs permission tickets.timers, else the call is forbidden.",
        "doNotUseWhen": "Do not use to put a deadline off by a time of your choosing; snoozeTimer takes the dueAt.",
        "success": "Returns dueAt, afterMs after what the deadline starts from; it has not fired.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: idle, nudge, ping or wake."
          },
          {
            "code": "not_armed",
            "commonCorrection": "Wait until the instance holds what the deadline starts from, the state it enters or a date-time in its field, and is out of the states that cancel it."
          }
        ]
      },
      "snoozeTimer": {
        "useWhen": "Use to move a started deadline's due time to dueAt, in epoch milliseconds; a fired one fires again then. It needs permission tickets.timers, else the call is forbidden.",
        "doNotUseWhen": "Do not use on a deadline that has not started or was cancelled; behaviors.Timers.deadlines lists the started ones.",
        "success": "Returns dueAt. A later write keeps it, until what the deadline starts from changes.",
        "errors": [
          {
            "code": "no_deadline",
            "commonCorrection": "Name a deadline: idle, nudge, ping or wake."
          },
          {
            "code": "not_armed",
            "commonCorrection": "None: the deadline has no due time to move. Read behaviors.Timers.deadlines."
          }
        ]
      },
      "create": {
        "success": "A new Ticket starts deadline ping, due at once. A visitAt the create gives starts deadline nudge."
      },
      "transition": {
        "success": "Entering waiting starts deadline idle, due 90 seconds later. Entering resolved or closed cancels deadline idle. Entering open starts deadline wake, due at once."
      },
      "update": {
        "success": "Changing visitAt moves deadline nudge, which fires again when it comes due; clearing visitAt cancels it."
      }
    }
  }
}"#;

#[test]
fn the_timers_guidance_is_the_typescript_engines_text() {
    use superschematic_engine_behaviors::{
        Timers, TimersConfig, TimersDeadline, TimersStart, TimersThen, TimersUntil,
    };

    const HOUR: i64 = 3_600_000;
    const DAY: i64 = 24 * HOUR;
    let expected: Value = serde_json::from_str(TIMERS_EXPECTED).expect("the expected guidance");
    let deadline = |start: TimersStart,
                    after_ms: i64,
                    then: Option<(&str, &[&str])>,
                    until: Option<TimersUntil>| TimersDeadline {
        start,
        after_ms,
        then: then.map(|(transition, from)| TimersThen {
            transition: transition.to_owned(),
            from: names(from),
        }),
        until,
    };
    let enters = |state: &str| TimersStart::Enters(state.to_owned());
    let field = |name: &str| TimersStart::Field(name.to_owned());
    let config = |deadlines: Vec<(&str, TimersDeadline)>,
                  sweep_ms: i64,
                  permission: Option<&str>| TimersConfig {
        deadlines: deadlines
            .into_iter()
            .map(|(name, deadline)| (name.to_owned(), deadline))
            .collect(),
        sweep_ms,
        permission: permission.map(str::to_owned),
    };
    let ticket = DescribeTarget {
        schema: "Ticket".to_owned(),
        type_name: "Ticket".to_owned(),
        ..target()
    };
    let guidance = Timers.hooks().guidance.expect("guidance");
    for (key, config) in [
        (
            "described",
            config(
                vec![
                    (
                        "close",
                        deadline(
                            TimersStart::Create,
                            30 * DAY,
                            Some(("closed", &["open"])),
                            Some(TimersUntil::Enters(names(&["resolved"]))),
                        ),
                    ),
                    ("remind", deadline(field("visitAt"), -DAY, None, None)),
                    (
                        "respond",
                        deadline(
                            enters("open"),
                            4 * HOUR,
                            Some(("escalated", &["open"])),
                            Some(TimersUntil::Leaves("open".to_owned())),
                        ),
                    ),
                ],
                60_000,
                Some("tickets.timers"),
            ),
        ),
        (
            "closed",
            config(
                vec![("ping", deadline(field("visitAt"), 0, None, None))],
                5000,
                None,
            ),
        ),
        (
            "mixed",
            config(
                vec![
                    (
                        "idle",
                        deadline(
                            enters("waiting"),
                            90_000,
                            None,
                            Some(TimersUntil::Enters(names(&["resolved", "closed"]))),
                        ),
                    ),
                    (
                        "nudge",
                        deadline(
                            field("visitAt"),
                            1500,
                            Some(("escalated", &["open", "waiting"])),
                            None,
                        ),
                    ),
                    ("ping", deadline(TimersStart::Create, 0, None, None)),
                    ("wake", deadline(enters("open"), 0, None, None)),
                ],
                7000,
                Some("tickets.timers"),
            ),
        ),
    ] {
        assert_eq!(
            as_json(&guidance(&Timers, &config, &ticket)),
            expected[key],
            "{key}"
        );
    }
}

/// Revisions' TypeScript guidance (`revisionsGuidance`), without a review
/// step and with one.
const REVISIONS: &str = r#"{
  "revisionsPlain": {
    "summary": "Every change of the own fields of a Order records a numbered revision of them; behaviors.Revisions.revision holds the latest. There is no review step.",
    "operations": {
      "listRevisions": {
        "useWhen": "Use to read the instance's revisions, oldest first: each its number and the own fields as that change left them.",
        "doNotUseWhen": "Do not page through them for one revision; call getRevision with its number.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "getRevision": {
        "useWhen": "Use to read one revision by its number, the one a pinned link or a proposal names say: the own fields as that change left them.",
        "success": "Returns the revision; a number the instance has not reached is not_found."
      },
      "propose": {
        "doNotUseWhen": "Do not use: Order has no review step, so a proposal is refused (no_review).",
        "errors": [
          {
            "code": "no_review",
            "commonCorrection": "None on this type: change the instance with update instead."
          }
        ]
      },
      "approve": {
        "doNotUseWhen": "Do not use: Order has no review step, so an approval is refused (no_review).",
        "errors": [
          {
            "code": "no_review",
            "commonCorrection": "None on this type: change the instance with update instead."
          }
        ]
      },
      "reject": {
        "doNotUseWhen": "Do not use: Order has no review step, so a rejection is refused (no_review).",
        "errors": [
          {
            "code": "no_review",
            "commonCorrection": "None on this type: change the instance with update instead."
          }
        ]
      },
      "listProposals": {
        "doNotUseWhen": "Do not use: Order has no review step, so listing proposals is refused (no_review).",
        "errors": [
          {
            "code": "no_review",
            "commonCorrection": "None on this type: change the instance with update instead."
          }
        ]
      },
      "update": {
        "success": "A change of the own fields records the next revision."
      }
    }
  },
  "revisionsReview": {
    "summary": "Every change of the own fields of a Order records a numbered revision of them; behaviors.Revisions.revision holds the latest. A change may also be proposed, and a caller with permission orders.review approves or rejects it; behaviors.Revisions.pendingProposals counts the proposals waiting.",
    "operations": {
      "listRevisions": {
        "useWhen": "Use to read the instance's revisions, oldest first: each its number and the own fields as that change left them.",
        "doNotUseWhen": "Do not page through them for one revision; call getRevision with its number.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "getRevision": {
        "useWhen": "Use to read one revision by its number, the one a pinned link or a proposal names say: the own fields as that change left them.",
        "success": "Returns the revision; a number the instance has not reached is not_found."
      },
      "propose": {
        "useWhen": "Use to propose a change of the own fields, as a JSON merge patch, for a caller with permission orders.review to approve. evidence cites instances the proposer may read, each optionally at a revision it has had.",
        "doNotUseWhen": "Do not use to change the instance at once; call update.",
        "success": "Returns the pending proposal with its id and its evidence; the instance does not change until it is approved."
      },
      "approve": {
        "useWhen": "Use to apply a pending proposal; it needs permission orders.review, else the call is forbidden.",
        "success": "Returns the proposal, approved, with the revision its patch made.",
        "errors": [
          {
            "code": "not_pending",
            "commonCorrection": "Read the proposal with listProposals: someone has already settled it."
          }
        ]
      },
      "reject": {
        "useWhen": "Use to turn a pending proposal down, with an optional reason; it needs permission orders.review, else the call is forbidden.",
        "success": "Returns the proposal, rejected; the instance does not change.",
        "errors": [
          {
            "code": "not_pending",
            "commonCorrection": "Read the proposal with listProposals: someone has already settled it."
          }
        ]
      },
      "listProposals": {
        "useWhen": "Use to read the instance's proposals, oldest first, each with the evidence it cites; with state, only the pending, approved or rejected ones.",
        "success": "Returns items and next; pass next as cursor for the page after, until it is null."
      },
      "update": {
        "success": "A change of the own fields records the next revision."
      }
    }
  }
}"#;

#[test]
fn revisions_guidance_is_the_typescript_engines_text() {
    let expected: Value = serde_json::from_str(REVISIONS).expect("the expected guidance");
    let guidance = Revisions.hooks().guidance.expect("guidance");
    assert_eq!(
        as_json(&guidance(
            &Revisions,
            &RevisionsConfig::default(),
            &target()
        )),
        expected["revisionsPlain"]
    );
    let review = RevisionsConfig {
        review: Some(RevisionsReview {
            permission: "orders.review".to_owned(),
        }),
    };
    assert_eq!(
        as_json(&guidance(&Revisions, &review, &target())),
        expected["revisionsReview"]
    );
}

/// Links' TypeScript guidance and create parameters (`linksGuidance`,
/// `linksCreateParams`) for a Task: single links, one pinned to a
/// revision; many-valued ones; a required many-valued one; required links
/// to its own schema and links pinned to a release; and one plain link.
const LINKS: &str = r#"{
  "single": {
    "guidance": {
      "summary": "Each Task links to other instances by name: spec to Spec (pinned to a revision), owner to Person (required) and related to Task. Every create gives owner; a required link is moved, never unlinked, and the instance it points at cannot be deleted. A link pinned to a revision records the target's revision; behaviors.Links.targets gives the target's latest beside it and says stale once the target has a newer one.",
      "operations": {
        "link": {
          "useWhen": "Use to point spec, owner or related at an instance of its schema, moving it when it is set. spec records the target's latest revision, or the one given.",
          "doNotUseWhen": "Do not use at create; give the links in behaviors.Links instead.",
          "success": "Returns the link: its name, schema, id and the revision or release a pinned link records.",
          "errors": [
            {
              "code": "no_revision",
              "commonCorrection": "Wait until the target has a revision, or link a target that has one."
            }
          ]
        },
        "unlink": {
          "useWhen": "Use to clear spec or related.",
          "doNotUseWhen": "Do not use on owner: a required link is moved with link, never unlinked.",
          "success": "Returns the link as it was.",
          "errors": [
            {
              "code": "required_link",
              "commonCorrection": "Move the link with link instead."
            }
          ]
        },
        "listLinked": {
          "useWhen": "Use to find the Task instances whose link points at a target. With stale true, only the pinned ones the target has moved past.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Links gives the links from the create, by name (spec, owner and related): each the target's id, or { id, revision } for spec. owner is required.",
          "errors": [
            {
              "code": "no_revision",
              "commonCorrection": "Wait until the target has a revision, or link a target that has one."
            }
          ]
        }
      }
    },
    "createParams": {
      "description": "The links the instance holds from its create, by name, each held to link's rules.",
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "spec": {
          "description": "The Spec instance spec points at: its id, or an object with its id and the revision to record, its latest when absent.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            },
            "revision": {
              "description": "The target's revision to record, one it has had.",
              "type": "integer",
              "minimum": 1
            }
          }
        },
        "owner": {
          "description": "The Person instance owner points at: its id.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            }
          }
        },
        "related": {
          "description": "The Task instance related points at: its id.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            }
          }
        }
      },
      "required": [
        "owner"
      ]
    }
  },
  "many": {
    "guidance": {
      "summary": "Each Task links to other instances by name: owner to Person, reviewers to Person (a list of at most 3) and specs to Spec (pinned to a revision, a list of at most 2). reviewers and specs hold a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names. A link pinned to a revision records the target's revision; behaviors.Links.targets gives the target's latest beside it and says stale once the target has a newer one.",
      "operations": {
        "link": {
          "useWhen": "Use to point owner at an instance of its schema, moving it when it is set. Use to add a target to reviewers or specs, or to pin again one it holds. specs records the target's latest revision, or the one given.",
          "doNotUseWhen": "Do not use at create; give the links in behaviors.Links instead.",
          "success": "Returns the link: its name, schema, id and the revision or release a pinned link records.",
          "errors": [
            {
              "code": "no_revision",
              "commonCorrection": "Wait until the target has a revision, or link a target that has one."
            },
            {
              "code": "too_many",
              "commonCorrection": "Unlink a target first."
            }
          ]
        },
        "unlink": {
          "useWhen": "Use to clear owner. Use with the target's id to remove one target of reviewers or specs.",
          "success": "Returns the link as it was.",
          "errors": [
            {
              "code": "not_linked",
              "commonCorrection": "Read behaviors.Links.targets for the targets the link holds."
            }
          ]
        },
        "listLinked": {
          "useWhen": "Use to find the Task instances whose link points at a target. With stale true, only the pinned ones the target has moved past.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Links gives the links from the create, by name (owner, reviewers and specs): each the target's id, or { id, revision } for specs. Give reviewers a list of up to 3 and specs a list of up to 2, each target once.",
          "errors": [
            {
              "code": "no_revision",
              "commonCorrection": "Wait until the target has a revision, or link a target that has one."
            }
          ]
        }
      }
    },
    "createParams": {
      "description": "The links the instance holds from its create, by name, each held to link's rules.",
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "owner": {
          "description": "The Person instance owner points at: its id.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            }
          }
        },
        "reviewers": {
          "description": "The Person instances reviewers points at, in order, each once: its id.",
          "type": "array",
          "maxItems": 3,
          "items": {
            "description": "One Person instance reviewers points at: its id.",
            "type": [
              "string",
              "object"
            ],
            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "id": {
                "description": "The target's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        },
        "specs": {
          "description": "The Spec instances specs points at, in order, each once: its id, or an object with its id and the revision to record, its latest when absent.",
          "type": "array",
          "maxItems": 2,
          "items": {
            "description": "One Spec instance specs points at: its id, or an object with its id and the revision to record, its latest when absent.",
            "type": [
              "string",
              "object"
            ],
            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "id": {
                "description": "The target's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              },
              "revision": {
                "description": "The target's revision to record, one it has had.",
                "type": "integer",
                "minimum": 1
              }
            }
          }
        }
      }
    }
  },
  "review": {
    "guidance": {
      "summary": "Each Task links to other instances by name: approvers to Person (required, a list of at most 2). approvers holds a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names. Every create gives at least one target of approvers, which keeps one: the last target can be neither unlinked nor deleted.",
      "operations": {
        "link": {
          "useWhen": "Use to add a target to approvers, or to pin again one it holds.",
          "doNotUseWhen": "Do not use at create; give the links in behaviors.Links instead.",
          "success": "Returns the link: its name, schema, id and the revision or release a pinned link records.",
          "errors": [
            {
              "code": "too_many",
              "commonCorrection": "Unlink a target first."
            }
          ]
        },
        "unlink": {
          "useWhen": "Use with the target's id to remove one target of approvers.",
          "doNotUseWhen": "Do not remove the last target of approvers: a required link keeps one.",
          "success": "Returns the link as it was.",
          "errors": [
            {
              "code": "required_link",
              "commonCorrection": "Link another target before unlinking the last."
            },
            {
              "code": "not_linked",
              "commonCorrection": "Read behaviors.Links.targets for the targets the link holds."
            }
          ]
        },
        "listLinked": {
          "useWhen": "Use to find the Task instances whose link points at a target.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Links gives the links from the create, by name (approvers): each the target's id. Give approvers a list of up to 2, each target once. approvers is required.",
          "errors": []
        }
      }
    },
    "createParams": {
      "description": "The links the instance holds from its create, by name, each held to link's rules.",
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "approvers": {
          "description": "The Person instances approvers points at, in order, each once: its id.",
          "type": "array",
          "minItems": 1,
          "maxItems": 2,
          "items": {
            "description": "One Person instance approvers points at: its id.",
            "type": [
              "string",
              "object"
            ],
            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "id": {
                "description": "The target's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        }
      },
      "required": [
        "approvers"
      ]
    }
  },
  "own": {
    "guidance": {
      "summary": "Each Task links to other instances by name: parent to Task (required), kids to Task (required, a list of at most 5), recipe to Recipe (pinned to a release) and notes to Note (pinned to a release, a list of at most 4). Every create gives parent; a required link is moved, never unlinked, and the instance it points at cannot be deleted. kids and notes hold a list of targets in behaviors.Links.targets, in the order they were linked: link adds one and unlink removes the one it names. Every create gives at least one target of kids, which keeps one: the last target can be neither unlinked nor deleted. A link pinned to a release records the target's release; behaviors.Links.targets gives the target's latest beside it and says stale once the target has released again.",
      "operations": {
        "link": {
          "useWhen": "Use to point parent or recipe at an instance of its schema, moving it when it is set. Use to add a target to kids or notes, or to pin again one it holds. recipe and notes record the target's latest release, or the one given.",
          "doNotUseWhen": "Do not use at create; give the links in behaviors.Links instead.",
          "success": "Returns the link: its name, schema, id and the revision or release a pinned link records.",
          "errors": [
            {
              "code": "no_release",
              "commonCorrection": "Wait until the target has been released, or link a target that has."
            },
            {
              "code": "too_many",
              "commonCorrection": "Unlink a target first."
            }
          ]
        },
        "unlink": {
          "useWhen": "Use to clear recipe. Use with the target's id to remove one target of kids or notes.",
          "doNotUseWhen": "Do not use on parent: a required link is moved with link, never unlinked. Do not remove the last target of kids: a required link keeps one.",
          "success": "Returns the link as it was.",
          "errors": [
            {
              "code": "required_link",
              "commonCorrection": "Move the link with link instead. Link another target before unlinking the last."
            },
            {
              "code": "not_linked",
              "commonCorrection": "Read behaviors.Links.targets for the targets the link holds."
            }
          ]
        },
        "listLinked": {
          "useWhen": "Use to find the Task instances whose link points at a target. With stale true, only the pinned ones the target has moved past.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Links gives the links from the create, by name (parent, kids, recipe and notes): each the target's id, or { id, release } for recipe and notes. Give kids a list of up to 5 and notes a list of up to 4, each target once. parent and kids are required.",
          "errors": [
            {
              "code": "no_release",
              "commonCorrection": "Wait until the target has been released, or link a target that has."
            }
          ]
        },
        "delete": {
          "doNotUseWhen": "Do not delete an instance that the parent link of another Task points at. Do not delete the last target of the kids link of another Task.",
          "errors": [
            {
              "code": "required_target",
              "commonCorrection": "Move the required links that point at it first."
            }
          ]
        }
      }
    },
    "createParams": {
      "description": "The links the instance holds from its create, by name, each held to link's rules.",
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "parent": {
          "description": "The Task instance parent points at: its id.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            }
          }
        },
        "kids": {
          "description": "The Task instances kids points at, in order, each once: its id.",
          "type": "array",
          "minItems": 1,
          "maxItems": 5,
          "items": {
            "description": "One Task instance kids points at: its id.",
            "type": [
              "string",
              "object"
            ],
            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "id": {
                "description": "The target's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        },
        "recipe": {
          "description": "The Recipe instance recipe points at: its id, or an object with its id and the release to record, its latest when absent.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            },
            "release": {
              "description": "The target's release to record, one it has had.",
              "type": "integer",
              "minimum": 1
            }
          }
        },
        "notes": {
          "description": "The Note instances notes points at, in order, each once: its id, or an object with its id and the release to record, its latest when absent.",
          "type": "array",
          "maxItems": 4,
          "items": {
            "description": "One Note instance notes points at: its id, or an object with its id and the release to record, its latest when absent.",
            "type": [
              "string",
              "object"
            ],
            "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "id": {
                "description": "The target's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              },
              "release": {
                "description": "The target's release to record, one it has had.",
                "type": "integer",
                "minimum": 1
              }
            }
          }
        }
      },
      "required": [
        "parent",
        "kids"
      ]
    }
  },
  "plain": {
    "guidance": {
      "summary": "Each Task links to other instances by name: related to Task.",
      "operations": {
        "link": {
          "useWhen": "Use to point related at an instance of its schema, moving it when it is set.",
          "doNotUseWhen": "Do not use at create; give the links in behaviors.Links instead.",
          "success": "Returns the link: its name, schema, id and the revision or release a pinned link records.",
          "errors": []
        },
        "unlink": {
          "useWhen": "Use to clear related.",
          "success": "Returns the link as it was.",
          "errors": []
        },
        "listLinked": {
          "useWhen": "Use to find the Task instances whose link points at a target.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Links gives the links from the create, by name (related): each the target's id.",
          "errors": []
        }
      }
    },
    "createParams": {
      "description": "The links the instance holds from its create, by name, each held to link's rules.",
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "related": {
          "description": "The Task instance related points at: its id.",
          "type": [
            "string",
            "object"
          ],
          "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$",
          "additionalProperties": false,
          "required": [
            "id"
          ],
          "properties": {
            "id": {
              "description": "The target's id.",
              "type": "string",
              "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
            }
          }
        }
      }
    }
  }
}"#;

// without_empty_errors drops an operation's errors when it lists none: the
// TypeScript object writes an empty list, which Rust's guidance holds as
// none, as it holds an absent one.
fn without_empty_errors(mut guidance: Value) -> Value {
    if let Some(operations) = guidance["operations"].as_object_mut() {
        for said in operations.values_mut() {
            if let Some(said) = said.as_object_mut() {
                if said
                    .get("errors")
                    .and_then(Value::as_array)
                    .is_some_and(Vec::is_empty)
                {
                    said.remove("errors");
                }
            }
        }
    }
    guidance
}

fn link(schema: &str, required: bool, pin: Option<LinkPin>, max: Option<i64>) -> LinkSpec {
    LinkSpec {
        schema: schema.to_owned(),
        required,
        pin,
        many: max.is_some(),
        max,
    }
}

#[test]
fn links_guidance_and_create_parameters_are_the_typescript_engines() {
    let expected: Value = serde_json::from_str(LINKS).expect("the expected guidance");
    let configs = [
        (
            "single",
            vec![
                ("spec", link("Spec", false, Some(LinkPin::Revision), None)),
                ("owner", link("Person", true, None, None)),
                ("related", link("Task", false, None, None)),
            ],
        ),
        (
            "many",
            vec![
                ("owner", link("Person", false, None, None)),
                ("reviewers", link("Person", false, None, Some(3))),
                (
                    "specs",
                    link("Spec", false, Some(LinkPin::Revision), Some(2)),
                ),
            ],
        ),
        (
            "review",
            vec![("approvers", link("Person", true, None, Some(2)))],
        ),
        (
            "own",
            vec![
                ("parent", link("Task", true, None, None)),
                ("kids", link("Task", true, None, Some(5))),
                (
                    "recipe",
                    link("Recipe", false, Some(LinkPin::Release), None),
                ),
                (
                    "notes",
                    link("Note", false, Some(LinkPin::Release), Some(4)),
                ),
            ],
        ),
        ("plain", vec![("related", link("Task", false, None, None))]),
    ];
    let hooks = Links.hooks();
    let guidance = hooks.guidance.expect("guidance");
    let create_params = hooks.create_params_schema.expect("createParamsSchema");
    let mut task = target();
    task.schema = "Task".to_owned();
    task.type_name = "Task".to_owned();
    for (name, links) in configs {
        let config = LinksConfig {
            links: links
                .into_iter()
                .map(|(name, link)| (name.to_owned(), link))
                .collect(),
        };
        assert_eq!(
            as_json(&guidance(&Links, &config, &task)),
            without_empty_errors(expected[name]["guidance"].clone()),
            "{name}"
        );
        // The members in the TypeScript object's order too: a tool's
        // parameters are hashed as written.
        assert_eq!(
            stringify(&create_params(&Links, &config, &task)),
            stringify(&expected[name]["createParams"]),
            "{name}"
        );
    }
}

/// Dependencies' TypeScript guidance and create parameters
/// (`dependenciesGuidance`, `dependenciesCreateParams`) for a Task: blockers
/// of its own schema or another's, gated on a terminal state; of another
/// schema alone, gated on states a transition leaves; and gated on none.
const DEPENDENCIES: &str = r#"{
  "own": {
    "guidance": {
      "summary": "Each Task waits on blockers, each an instance of Task or Milestone (the instance's own schema when schema is absent). A transition into done waits until every blocker has finished: its status is a terminal state of its own Workflow whose outcome is success. blocked says whether one is open.",
      "operations": {
        "addBlocker": {
          "useWhen": "Use to make an instance of Task or Milestone (the instance's own schema when schema is absent) block this one. It holds up the transition into done until it finishes.",
          "doNotUseWhen": "Do not use at create; give the blockers in behaviors.Dependencies.blockers instead.",
          "success": "Returns the blocker, with its status and whether it is open; blocked is true while one is.",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            },
            {
              "code": "gated",
              "commonCorrection": "Add only finished blockers to an instance in a gated state no transition leaves, or none."
            }
          ]
        },
        "removeBlocker": {
          "useWhen": "Use to stop an instance blocking this one, as when a blocker failed or was cancelled and the work goes on without it.",
          "success": "Returns the blocker it removed; blocked is false once no open blocker is left."
        },
        "listBlockers": {
          "useWhen": "Use to read the instance's blockers, each with its status and whether it is open.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "listDependents": {
          "useWhen": "Use to read the instances this one blocks, in schemas the caller may read.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Dependencies.blockers gives the instance its blockers from the create, at most 500, each an instance of Task or Milestone (the instance's own schema when schema is absent).",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            },
            {
              "code": "gated",
              "commonCorrection": "Add only finished blockers to an instance in a gated state no transition leaves, or none."
            }
          ]
        },
        "transition": {
          "doNotUseWhen": "Do not move into done while blocked is true.",
          "errors": [
            {
              "code": "blocked",
              "description": "A move into done while a blocker is open; details.details.blockers lists the open ones.",
              "commonCorrection": "Wait for the open blockers to finish, or remove the ones the work goes on without, then try again."
            }
          ]
        }
      }
    },
    "createParams": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "blockers": {
          "description": "The instance's blockers from its create, each held to addBlocker's rules: an instance of Task or Milestone.",
          "type": "array",
          "maxItems": 500,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "schema": {
                "description": "The blocker's schema; Task when absent.",
                "type": "string",
                "enum": [
                  "Task",
                  "Milestone"
                ]
              },
              "id": {
                "description": "The blocker's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        }
      }
    }
  },
  "other": {
    "guidance": {
      "summary": "Each Task waits on blockers, each an instance of Milestone, named by schema. A transition into doing or review waits until every blocker has finished: its status is a terminal state of its own Workflow whose outcome is success or neutral. blocked says whether one is open.",
      "operations": {
        "addBlocker": {
          "useWhen": "Use to make an instance of Milestone, named by schema block this one. It holds up the transition into doing or review until it finishes.",
          "doNotUseWhen": "Do not use at create; give the blockers in behaviors.Dependencies.blockers instead.",
          "success": "Returns the blocker, with its status and whether it is open; blocked is true while one is.",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            }
          ]
        },
        "removeBlocker": {
          "useWhen": "Use to stop an instance blocking this one, as when a blocker failed or was cancelled and the work goes on without it.",
          "success": "Returns the blocker it removed; blocked is false once no open blocker is left."
        },
        "listBlockers": {
          "useWhen": "Use to read the instance's blockers, each with its status and whether it is open.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "listDependents": {
          "useWhen": "Use to read the instances this one blocks, in schemas the caller may read.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Dependencies.blockers gives the instance its blockers from the create, at most 500, each an instance of Milestone, named by schema.",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            }
          ]
        },
        "transition": {
          "doNotUseWhen": "Do not move into doing or review while blocked is true.",
          "errors": [
            {
              "code": "blocked",
              "description": "A move into doing or review while a blocker is open; details.details.blockers lists the open ones.",
              "commonCorrection": "Wait for the open blockers to finish, or remove the ones the work goes on without, then try again."
            }
          ]
        }
      }
    },
    "createParams": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "blockers": {
          "description": "The instance's blockers from its create, each held to addBlocker's rules: an instance of Milestone.",
          "type": "array",
          "maxItems": 500,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": [
              "id",
              "schema"
            ],
            "properties": {
              "schema": {
                "description": "The blocker's schema.",
                "type": "string",
                "enum": [
                  "Milestone"
                ]
              },
              "id": {
                "description": "The blocker's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        }
      }
    }
  },
  "open": {
    "guidance": {
      "summary": "Each Task waits on blockers, each an instance of Task (the instance's own schema when schema is absent). No state is gated, so blockers hold up no transition; blocked says whether one is open.",
      "operations": {
        "addBlocker": {
          "useWhen": "Use to make an instance of Task (the instance's own schema when schema is absent) block this one.",
          "doNotUseWhen": "Do not use at create; give the blockers in behaviors.Dependencies.blockers instead.",
          "success": "Returns the blocker, with its status and whether it is open; blocked is true while one is.",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            }
          ]
        },
        "removeBlocker": {
          "useWhen": "Use to stop an instance blocking this one, as when a blocker failed or was cancelled and the work goes on without it.",
          "success": "Returns the blocker it removed; blocked is false once no open blocker is left."
        },
        "listBlockers": {
          "useWhen": "Use to read the instance's blockers, each with its status and whether it is open.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "listDependents": {
          "useWhen": "Use to read the instances this one blocks, in schemas the caller may read.",
          "success": "Returns items and next; pass next as cursor for the page after, until it is null."
        },
        "create": {
          "useWhen": "behaviors.Dependencies.blockers gives the instance its blockers from the create, at most 500, each an instance of Task (the instance's own schema when schema is absent).",
          "errors": [
            {
              "code": "already_blocking",
              "commonCorrection": "Leave it: that instance already blocks this one."
            },
            {
              "code": "cycle",
              "commonCorrection": "Remove the edge that makes the other instance wait on this one first, or leave this edge out."
            }
          ]
        }
      }
    },
    "createParams": {
      "type": "object",
      "additionalProperties": false,
      "properties": {
        "blockers": {
          "description": "The instance's blockers from its create, each held to addBlocker's rules: an instance of Task.",
          "type": "array",
          "maxItems": 500,
          "items": {
            "type": "object",
            "additionalProperties": false,
            "required": [
              "id"
            ],
            "properties": {
              "schema": {
                "description": "The blocker's schema; Task when absent.",
                "type": "string",
                "enum": [
                  "Task"
                ]
              },
              "id": {
                "description": "The blocker's id.",
                "type": "string",
                "pattern": "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"
              }
            }
          }
        }
      }
    }
  }
}"#;

#[test]
fn dependencies_guidance_and_create_parameters_are_the_typescript_engines() {
    let expected: Value = serde_json::from_str(DEPENDENCIES).expect("the expected guidance");
    let configs = [
        (
            "own",
            DependenciesConfig {
                schemas: names(&["Task", "Milestone"]),
                gated_states: names(&["done"]),
                satisfied_by: vec![WorkflowOutcome::Success],
                terminal_gated_states: names(&["done"]),
            },
        ),
        (
            "other",
            DependenciesConfig {
                schemas: names(&["Milestone"]),
                gated_states: names(&["doing", "review"]),
                satisfied_by: vec![WorkflowOutcome::Success, WorkflowOutcome::Neutral],
                terminal_gated_states: Vec::new(),
            },
        ),
        (
            "open",
            DependenciesConfig {
                schemas: names(&["Task"]),
                gated_states: Vec::new(),
                satisfied_by: vec![WorkflowOutcome::Success],
                terminal_gated_states: Vec::new(),
            },
        ),
    ];
    let hooks = Dependencies.hooks();
    let guidance = hooks.guidance.expect("guidance");
    let create_params = hooks.create_params_schema.expect("createParamsSchema");
    let mut task = target();
    task.schema = "Task".to_owned();
    task.type_name = "Task".to_owned();
    for (name, config) in configs {
        assert_eq!(
            as_json(&guidance(&Dependencies, &config, &task)),
            without_empty_errors(expected[name]["guidance"].clone()),
            "{name}"
        );
        assert_eq!(
            stringify(&create_params(&Dependencies, &config, &task)),
            stringify(&expected[name]["createParams"]),
            "{name}"
        );
    }
}

/// Numbering's TypeScript guidance (`numberingGuidance`) for an Invoice: a
/// yearly count per customer, printed with the year, on a type whose link
/// operation moves the customer; one count, printed without it; and a count
/// per project with the year's two digits, on a type with no link
/// operation.
const NUMBERING: &str = r#"{
  "scoped": {
    "summary": "Each Invoice gets a number at its create, in a count per target of its customer link, starting again at 1 each UTC year, printed as INV-{yyyy}-{seq:4}: number 42, made in 2026, prints INV-2026-0042. A label is unique among one customer target's instances. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.",
    "operations": {
      "create": {
        "success": "The new Invoice holds behaviors.Numbering.seq, the next number of its count, and label, the number as INV-{yyyy}-{seq:4} prints it. It is counted in the count of the customer target the create gives."
      },
      "list": {
        "useWhen": "where: { \"Numbering.label\": \"INV-2026-0042\" } finds an instance by the number a person reads, and \"Numbering.seq\" by its number. Each customer target counts from 1, so a label can match one instance per target."
      },
      "update": {
        "doNotUseWhen": "Do not use to change a number, which behaviors.Numbering holds: nothing changes a number once set."
      },
      "assignNumber": {
        "useWhen": "Use on an instance created before Invoice composed Numbering, which holds no number yet; to backfill, invoke it on each in creation order.",
        "doNotUseWhen": "Do not use to renumber: an instance numbered already keeps its number, and the call changes nothing.",
        "success": "Returns seq and label, counted as its create would have counted them, by its customer target, in the year it was created; behaviors.Numbering holds them from then on."
      },
      "link": {
        "success": "Moving customer keeps the instance's number, which stays in the count of the target it was numbered under."
      }
    }
  },
  "plain": {
    "summary": "Each Invoice gets a number at its create, in one count for Invoice, printed as No. {seq}: number 42 prints No. 42. A label is unique. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.",
    "operations": {
      "create": {
        "success": "The new Invoice holds behaviors.Numbering.seq, the next number of its count, and label, the number as No. {seq} prints it."
      },
      "list": {
        "useWhen": "where: { \"Numbering.label\": \"No. 42\" } finds an instance by the number a person reads, and \"Numbering.seq\" by its number."
      },
      "update": {
        "doNotUseWhen": "Do not use to change a number, which behaviors.Numbering holds: nothing changes a number once set."
      },
      "assignNumber": {
        "useWhen": "Use on an instance created before Invoice composed Numbering, which holds no number yet; to backfill, invoke it on each in creation order.",
        "doNotUseWhen": "Do not use to renumber: an instance numbered already keeps its number, and the call changes nothing.",
        "success": "Returns seq and label, counted as its create would have counted them; behaviors.Numbering holds them from then on."
      }
    }
  },
  "short": {
    "summary": "Each Invoice gets a number at its create, in a count per target of its project link, printed as {yy}/{seq:1}: number 42, made in 2026, prints 26/42. A label is unique among one project target's instances. A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.",
    "operations": {
      "create": {
        "success": "The new Invoice holds behaviors.Numbering.seq, the next number of its count, and label, the number as {yy}/{seq:1} prints it. It is counted in the count of the project target the create gives."
      },
      "list": {
        "useWhen": "where: { \"Numbering.label\": \"26/42\" } finds an instance by the number a person reads, and \"Numbering.seq\" by its number. Each project target counts from 1, so a label can match one instance per target."
      },
      "update": {
        "doNotUseWhen": "Do not use to change a number, which behaviors.Numbering holds: nothing changes a number once set."
      },
      "assignNumber": {
        "useWhen": "Use on an instance created before Invoice composed Numbering, which holds no number yet; to backfill, invoke it on each in creation order.",
        "doNotUseWhen": "Do not use to renumber: an instance numbered already keeps its number, and the call changes nothing.",
        "success": "Returns seq and label, counted as its create would have counted them, by its project target; behaviors.Numbering holds them from then on."
      }
    }
  }
}"#;

#[test]
fn numbering_guidance_is_the_typescript_engines_text() {
    use superschematic_engine::behaviors::{DescribedTypeOperation, OperationScope};

    let expected: Value = serde_json::from_str(NUMBERING).expect("the expected guidance");
    let invoice = |operations: &[&str]| {
        let mut invoice = target();
        invoice.schema = "Invoice".to_owned();
        invoice.type_name = "Invoice".to_owned();
        invoice.operations = operations
            .iter()
            .map(|name| DescribedTypeOperation {
                name: (*name).to_owned(),
                behavior: Some("Links".to_owned()),
                writes: true,
                scope: OperationScope::Instance,
            })
            .collect();
        invoice
    };
    let guidance = Numbering.hooks().guidance.expect("guidance");
    for (name, config, operations) in [
        (
            "scoped",
            NumberingConfig {
                format: "INV-{yyyy}-{seq:4}".to_owned(),
                scope: Some("customer".to_owned()),
                yearly: true,
            },
            &["link", "unlink"][..],
        ),
        (
            "plain",
            NumberingConfig {
                format: "No. {seq}".to_owned(),
                scope: None,
                yearly: false,
            },
            &[][..],
        ),
        (
            "short",
            NumberingConfig {
                format: "{yy}/{seq:1}".to_owned(),
                scope: Some("project".to_owned()),
                yearly: false,
            },
            &["assignNumber"][..],
        ),
    ] {
        assert_eq!(
            as_json(&guidance(&Numbering, &config, &invoice(operations))),
            expected[name],
            "{name}"
        );
    }
}

/// The TypeScript guidance for the Rollups configs below
/// (`rollupsGuidance`).
const ROLLUPS: &str = r#"{
  "gated": {
    "summary": "behaviors.Rollups.values holds values computed from linked instances at each read: tasks, how many of the Task instances whose project points here there are; byStatus, how many of the Task instances whose project points here hold each value of Workflow.status; estimate, the sum of estimate over the Task instances whose project points here; smallest, the min of estimate over the Task instances whose project points here; longest, the max of hours over the Task instances whose project points here; last, the kind of the one of the Task instances whose project points here created last; finished, whether every one of the Task instances whose project points here is in a terminal state; started, whether one of the Task instances whose project points here is in a terminal state with outcome success or neutral; failed, whether one of the Task instances whose project points here is in a terminal state with outcome failure. A rollup over more than 500 linked instances reads {\"over\": true}. A move into done waits until finished holds; a move into done or dropped waits until started holds.",
    "operations": {
      "transition": {
        "doNotUseWhen": "Read behaviors.Rollups.values before a move: a move into done waits until finished holds; a move into done or dropped waits until started holds.",
        "errors": [
          {
            "code": "not_held",
            "description": "A move into a state a rollup gates while the rollup does not hold; details names the rollup and counts the linked instances it read and counted.",
            "commonCorrection": "Read behaviors.Rollups.values, and finish the linked work the rollup waits on, then try again."
          }
        ]
      }
    }
  },
  "plain": {
    "summary": "behaviors.Rollups.values holds values computed from linked instances at each read: tasks, how many of the Task instances whose project points here there are; boards, how many of the Board instances whose project points here there are. A rollup over more than 500 linked instances reads {\"over\": true}.",
    "operations": {}
  }
}"#;

#[test]
fn rollups_guidance_is_the_typescript_engines_text() {
    use superschematic_engine_behaviors::{RollupFunction, RollupSpec, Rollups, RollupsConfig};

    let expected: Value = serde_json::from_str(ROLLUPS).expect("the expected guidance");
    let rollup = |schema: &str,
                  function: RollupFunction,
                  field: Option<&str>,
                  gated: &[&str],
                  outcomes: Option<&[WorkflowOutcome]>| RollupSpec {
        schema: schema.to_owned(),
        link: "project".to_owned(),
        function,
        field: field.map(str::to_owned),
        gated_states: names(gated),
        outcomes: outcomes.map(<[WorkflowOutcome]>::to_vec),
    };
    let config = |rollups: Vec<(&str, RollupSpec)>| RollupsConfig {
        rollups: rollups
            .into_iter()
            .map(|(name, spec)| (name.to_owned(), spec))
            .collect(),
    };
    let gated = config(vec![
        (
            "tasks",
            rollup("Task", RollupFunction::Count, None, &[], None),
        ),
        (
            "byStatus",
            rollup(
                "Task",
                RollupFunction::CountBy,
                Some("Workflow.status"),
                &[],
                None,
            ),
        ),
        (
            "estimate",
            rollup("Task", RollupFunction::Sum, Some("estimate"), &[], None),
        ),
        (
            "smallest",
            rollup("Task", RollupFunction::Min, Some("estimate"), &[], None),
        ),
        (
            "longest",
            rollup("Task", RollupFunction::Max, Some("hours"), &[], None),
        ),
        (
            "last",
            rollup("Task", RollupFunction::Latest, Some("kind"), &[], None),
        ),
        (
            "finished",
            rollup("Task", RollupFunction::All, None, &["done"], None),
        ),
        (
            "started",
            rollup(
                "Task",
                RollupFunction::Any,
                None,
                &["done", "dropped"],
                Some(&[WorkflowOutcome::Success, WorkflowOutcome::Neutral]),
            ),
        ),
        (
            "failed",
            rollup(
                "Task",
                RollupFunction::Any,
                None,
                &[],
                Some(&[WorkflowOutcome::Failure]),
            ),
        ),
    ]);
    let plain = config(vec![
        (
            "tasks",
            rollup("Task", RollupFunction::Count, None, &[], None),
        ),
        (
            "boards",
            rollup("Board", RollupFunction::Count, None, &[], None),
        ),
    ]);
    let guidance = Rollups.hooks().guidance.expect("guidance");
    for (name, config) in [("gated", gated), ("plain", plain)] {
        assert_eq!(
            as_json(&guidance(&Rollups, &config, &target())),
            expected[name],
            "{name}"
        );
    }
}
