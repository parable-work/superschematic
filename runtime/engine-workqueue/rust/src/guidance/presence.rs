//! Presence's guidance (`typescript/src/guidance/presence.ts`): who beats
//! an instance and how often, what a miss does to the status and to the
//! principal's leases, and that the field naming the principal does not
//! change once set.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget};

use super::{duration, list, refusal, said, sentences};
use crate::presence::{PresenceConfig, PresenceTransition};

fn moves(rule: &PresenceTransition) -> String {
    format!(
        "moves status from {} to {}",
        list(&rule.from, "or"),
        rule.transition
    )
}

/// What Presence tells a caller about the type under its config.
pub(crate) fn guidance(config: &PresenceConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let field = &config.principal_field;
    BehaviorGuidance {
        summary: sentences(&[
            Some(format!(
                "Each {} stands for the principal {field} names, which beats it at least every {}; the runner misses one past its deadline within {}.",
                target.type_name,
                duration(config.ttl_ms),
                duration(config.sweep_ms)
            )),
            config
                .on_missed
                .as_ref()
                .map(|rule| format!("A miss {}.", moves(rule))),
            (!config.release_leases.is_empty()).then(|| {
                format!(
                    "A miss expires the principal's leases on {}, but those renewed since its last beat.",
                    list(&config.release_leases, "and")
                )
            }),
            config
                .on_beat
                .as_ref()
                .map(|rule| format!("A beat {}.", moves(rule))),
        ]),
        operations: vec![
            (
                "beat".to_owned(),
                said(
                    Some(format!(
                        "Use as the principal {field} names, at least every {}, to show it is alive.",
                        duration(config.ttl_ms)
                    )),
                    None,
                    Some("Returns the new deadline; a missed instance is present again.".to_owned()),
                    vec![
                        refusal(
                            "not_principal",
                            None,
                            &format!("Beat the instance whose {field} names you."),
                        ),
                        refusal(
                            "no_principal",
                            None,
                            &format!("Set {field} to the principal's subject first."),
                        ),
                    ],
                ),
            ),
            (
                "miss".to_owned(),
                said(
                    Some(format!(
                        "Use to apply the miss of an instance past its deadline; the runner's sweep does so within {}.",
                        duration(config.sweep_ms)
                    )),
                    None,
                    Some("Returns missed, false for an instance not past its deadline, and the leases it released, by schema.".to_owned()),
                    Vec::new(),
                ),
            ),
            (
                "update".to_owned(),
                said(
                    None,
                    Some(format!("Do not change {field} once it holds a principal.")),
                    None,
                    vec![refusal(
                        "principal_fixed",
                        None,
                        "Create a new instance for another principal.",
                    )],
                ),
            ),
        ],
    }
}
