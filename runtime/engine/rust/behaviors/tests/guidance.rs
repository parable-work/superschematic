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
