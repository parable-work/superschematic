//! Variants' guidance (`guidance/variants.ts`): which type a field holds
//! for each value of another, so a create or an update gives the field the
//! shape that value picks.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::list;
use crate::variants::VariantsConfig;

pub(crate) fn guidance(config: &VariantsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let shapes: Vec<String> = config
        .types
        .iter()
        .map(|(value, type_name)| format!("{type_name} when {} is {value}", config.by))
        .collect();
    let rule = format!(
        "{} holds {}, and nothing while {} holds another value or none",
        config.field,
        list(&shapes, "or"),
        config.by
    );
    BehaviorGuidance {
        summary: format!(
            "On each {}, {} takes the shape {} picks: {rule}.",
            target.type_name, config.field, config.by
        ),
        operations: vec![
            (
                "create".to_owned(),
                OperationGuidance {
                    use_when: Some(format!(
                        "Give {} the shape {} picks: {rule}.",
                        config.field, config.by
                    )),
                    ..OperationGuidance::default()
                },
            ),
            (
                "update".to_owned(),
                OperationGuidance {
                    use_when: Some(format!(
                        "A patch of {} keeps the shape {} picks, as merged; the describe document shows each shape under allOf.",
                        config.field, config.by
                    )),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
