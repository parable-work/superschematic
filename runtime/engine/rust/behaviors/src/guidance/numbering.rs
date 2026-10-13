//! Numbering's guidance (`guidance/numbering.ts`): how a number prints,
//! with the format's own example, what it counts by, that a create takes
//! one and nothing changes it, how a list finds an instance by it, and when
//! assignNumber is the call to make.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{said, sentences};
use crate::numbering::{dated, example, NumberingConfig, EXAMPLE_SEQ, EXAMPLE_YEAR};

pub(crate) fn guidance(config: &NumberingConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let link = config.scope.as_deref();
    let printed = example(&config.format);
    let format = &config.format;
    let type_name = &target.type_name;
    let mut count = vec![match link {
        None => format!("one count for {}", target.schema),
        Some(link) => format!("a count per target of its {link} link"),
    }];
    if config.yearly {
        count.push("starting again at 1 each UTC year".to_owned());
    }
    let mut operations = vec![
        (
            "create".to_owned(),
            OperationGuidance {
                success: Some(sentences(&[
                    Some(format!("The new {type_name} holds behaviors.Numbering.seq, the next number of its count, and label, the number as {format} prints it.")),
                    link.map(|link| format!("It is counted in the count of the {link} target the create gives.")),
                ])),
                ..OperationGuidance::default()
            },
        ),
        (
            "list".to_owned(),
            OperationGuidance {
                use_when: Some(sentences(&[
                    Some(format!("where: {{ \"Numbering.label\": \"{printed}\" }} finds an instance by the number a person reads, and \"Numbering.seq\" by its number.")),
                    link.map(|link| format!("Each {link} target counts from 1, so a label can match one instance per target.")),
                ])),
                ..OperationGuidance::default()
            },
        ),
        (
            "update".to_owned(),
            OperationGuidance {
                do_not_use_when: Some("Do not use to change a number, which behaviors.Numbering holds: nothing changes a number once set.".to_owned()),
                ..OperationGuidance::default()
            },
        ),
        (
            "assignNumber".to_owned(),
            said(
                Some(format!("Use on an instance created before {} composed Numbering, which holds no number yet; to backfill, invoke it on each in creation order.", target.schema)),
                Some("Do not use to renumber: an instance numbered already keeps its number, and the call changes nothing.".to_owned()),
                Some(format!(
                    "Returns seq and label, counted as its create would have counted them{}{}; behaviors.Numbering holds them from then on.",
                    link.map_or(String::new(), |link| format!(", by its {link} target")),
                    if config.yearly { ", in the year it was created" } else { "" }
                )),
                Vec::new(),
            ),
        ),
    ];
    if let Some(link) = link {
        if target
            .operations
            .iter()
            .any(|operation| operation.name == "link")
        {
            operations.push((
                "link".to_owned(),
                OperationGuidance {
                    success: Some(format!("Moving {link} keeps the instance's number, which stays in the count of the target it was numbered under.")),
                    ..OperationGuidance::default()
                },
            ));
        }
    }
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Each {type_name} gets a number at its create, in {}, printed as {format}: number {EXAMPLE_SEQ}{} prints {printed}.",
                count.join(", "),
                if dated(format) {
                    format!(", made in {EXAMPLE_YEAR},")
                } else {
                    String::new()
                }
            )),
            Some(match link {
                None => "A label is unique.".to_owned(),
                Some(link) => format!("A label is unique among one {link} target's instances."),
            }),
            Some("A refused create takes no number, a delete leaves a gap, and nothing changes a number once set.".to_owned()),
        ]),
        operations,
    }
}
