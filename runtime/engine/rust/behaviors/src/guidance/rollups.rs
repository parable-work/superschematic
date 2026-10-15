//! Rollups' guidance (`guidance/rollups.ts`): what each rollup computes and
//! over which link, and which Workflow moves wait for an all or any rollup
//! to hold.

use superschematic_engine::behaviors::BehaviorGuidance;

use super::{capital, list, refusal, said, sentences};
use crate::rollups::{RollupFunction, RollupSpec, RollupsConfig};

// computes says what one rollup's value is.
fn computes(spec: &RollupSpec) -> String {
    let over = format!(
        "the {} instances whose {} points here",
        spec.schema, spec.link
    );
    let terminal = match &spec.outcomes {
        None => "a terminal state".to_owned(),
        Some(outcomes) => {
            let names: Vec<&str> = outcomes.iter().map(|outcome| outcome.as_str()).collect();
            format!("a terminal state with outcome {}", list(&names, "or"))
        }
    };
    let field = spec.field.as_deref().unwrap_or("undefined");
    match spec.function {
        RollupFunction::Count => format!("how many of {over} there are"),
        RollupFunction::CountBy => format!("how many of {over} hold each value of {field}"),
        RollupFunction::Sum | RollupFunction::Min | RollupFunction::Max => {
            format!("the {} of {field} over {over}", spec.function.as_str())
        }
        RollupFunction::Latest => format!("the {field} of the one of {over} created last"),
        RollupFunction::All => format!("whether every one of {over} is in {terminal}"),
        RollupFunction::Any => format!("whether one of {over} is in {terminal}"),
    }
}

pub(crate) fn guidance(config: &RollupsConfig) -> BehaviorGuidance {
    let computed: Vec<String> = config
        .rollups
        .iter()
        .map(|(name, spec)| format!("{name}, {}", computes(spec)))
        .collect();
    let gating: Vec<String> = config
        .rollups
        .iter()
        .filter(|(_, spec)| !spec.gated_states.is_empty())
        .map(|(name, spec)| {
            format!(
                "a move into {} waits until {name} holds",
                list(&spec.gated_states, "or")
            )
        })
        .collect();
    let summary = sentences(&[
        Some(format!(
            "behaviors.Rollups.values holds values computed from linked instances at each read: {}.",
            computed.join("; ")
        )),
        Some("A rollup over more than 500 linked instances reads {\"over\": true}.".to_owned()),
        (!gating.is_empty()).then(|| format!("{}.", capital(&gating.join("; ")))),
    ]);
    let operations = if gating.is_empty() {
        Vec::new()
    } else {
        vec![(
            "transition".to_owned(),
            said(
                None,
                Some(format!(
                    "Read behaviors.Rollups.values before a move: {}.",
                    gating.join("; ")
                )),
                None,
                vec![refusal(
                    "not_held",
                    Some("A move into a state a rollup gates while the rollup does not hold; details names the rollup and counts the linked instances it read and counted."),
                    "Read behaviors.Rollups.values, and finish the linked work the rollup waits on, then try again.",
                )],
            ),
        )]
    };
    BehaviorGuidance {
        summary,
        operations,
    }
}
