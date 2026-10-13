//! Constants' guidance (`guidance/constants.ts`): which fields keep the
//! value their create gave them, and who may change them after it.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::list;
use crate::constants::ConstantsConfig;

pub(crate) fn guidance(config: &ConstantsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let one = config.fields.len() == 1;
    let who = config
        .permission
        .as_ref()
        .map_or("nothing changes".to_owned(), |permission| {
            format!("only a caller with permission {permission} changes")
        });
    BehaviorGuidance {
        summary: format!(
            "{} keep{} the value the create of each {} gives; after it, {who} {}.",
            list(&config.fields, "and"),
            if one { "s" } else { "" },
            target.type_name,
            if one { "it" } else { "them" }
        ),
        operations: vec![
            (
                "create".to_owned(),
                OperationGuidance {
                    use_when: Some(format!(
                        "Give {} at create: {} not change after it, and one left absent stays absent.",
                        list(&config.fields, "and"),
                        if one { "it does" } else { "they do" }
                    )),
                    ..OperationGuidance::default()
                },
            ),
            (
                "update".to_owned(),
                OperationGuidance {
                    do_not_use_when: Some(format!(
                        "Do not change {}{}: a change is refused as invalid_instance, with rule constant at the field.",
                        list(&config.fields, "or"),
                        config
                            .permission
                            .as_ref()
                            .map_or(String::new(), |permission| format!(
                                " without permission {permission}"
                            ))
                    )),
                    ..OperationGuidance::default()
                },
            ),
        ],
    }
}
