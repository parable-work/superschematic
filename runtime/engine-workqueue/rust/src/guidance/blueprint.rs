//! Blueprint's guidance (`typescript/src/guidance/blueprint.ts`): which
//! children a stamp creates and when, what each child holds from its
//! parent, and the refusals a stamp meets, which it adds to the write that
//! stamps: the create for inline steps, the create or the link of the from
//! link for steps read from a definition. It adds no operation of its own.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, GuidanceError};

use super::{list, refusal, said, sentences};
use crate::blueprint::BlueprintConfig;

const CORRECTIONS: [(&str, &str); 6] = [
    ("no_dependencies", "None: the child schema's live version must compose Dependencies for steps that come after others."),
    ("not_constant", "None: the child schema's live version must keep keyField and the copied fields in Constants; details.fields names the ones it does not."),
    ("no_revision", "Link a definition that has a revision, or wait until it has one."),
    ("unreadable", "None: the caller cannot read the definition's revision; a caller who may read it stamps."),
    ("invalid_steps", "Fix the steps in the definition, as the message says, and link its new revision."),
    ("stamped", "None: the children were stamped from the revision it pins, so the link stays."),
];

fn errors(codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| {
            let correction = CORRECTIONS
                .iter()
                .find(|(known, _)| known == code)
                .map_or("", |(_, correction)| correction);
            refusal(code, None, correction)
        })
        .collect()
}

/// What Blueprint tells a caller about the type under its config.
pub(crate) fn guidance(config: &BlueprintConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let copies: Vec<String> = config
        .copy_fields
        .iter()
        .cloned()
        .chain(
            config
                .copy_links
                .iter()
                .map(|link| format!("the {link} link")),
        )
        .collect();
    let holds = sentences(&[
        Some(format!(
            "Each child is a {} instance whose {} holds its step's key and whose {} links to its parent",
            config.schema, config.key_field, config.parent_link
        )),
        (!copies.is_empty()).then(|| format!(", copying {}", list(&copies, "and"))),
    ])
    .replacen(" ,", ",", 1);
    let after = "a step after others is blocked by their children, so claimable children are claimed in that order";
    let Some(from) = &config.from else {
        let keys: Vec<&str> = config
            .steps
            .iter()
            .flatten()
            .map(|step| step.key.as_str())
            .collect();
        return BehaviorGuidance {
            summary: format!(
                "The create of a {} stamps a child per step, {}, a step whose when does not hold left out. {holds}; {after}.",
                target.type_name,
                list(&keys, "and")
            ),
            operations: vec![(
                "create".to_owned(),
                said(
                    None,
                    None,
                    Some(format!(
                        "The create stamps the children ({}) in its own write; behaviors.Blueprint.children lists them.",
                        list(&keys, "and")
                    )),
                    errors(&["no_dependencies", "not_constant"]),
                ),
            )],
        };
    };
    BehaviorGuidance {
        summary: format!(
            "When the {link} link of a {} is first set, at its create or by link, the steps are read from {} of the revision it pins, and a child is stamped per step. {holds}; {after}. Once stamped, {link} does not move.",
            target.type_name,
            from.field,
            link = from.link
        ),
        operations: vec![
            (
                "create".to_owned(),
                said(
                    None,
                    None,
                    Some(format!(
                        "A create that gives the {} link stamps its children in the same write; behaviors.Blueprint.children lists them.",
                        from.link
                    )),
                    errors(&["no_revision", "unreadable", "invalid_steps", "no_dependencies", "not_constant"]),
                ),
            ),
            (
                "link".to_owned(),
                said(
                    None,
                    Some(format!(
                        "Do not move {} once the children are stamped.",
                        from.link
                    )),
                    Some(format!(
                        "Setting {} for the first time stamps the children in the same write.",
                        from.link
                    )),
                    errors(&[
                        "stamped",
                        "no_revision",
                        "unreadable",
                        "invalid_steps",
                        "no_dependencies",
                        "not_constant",
                    ]),
                ),
            ),
        ],
    }
}
