//! Holds' guidance: what each hold asks of which supply, which moves of
//! the claim take, commit, release and return, what a caller cannot
//! change while the claim holds, and each `takeOn` state that no Timers
//! deadline leaves, whose holds stay until the claim moves.

use serde_json::Value;
use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{errors, list, said, sentences};
use crate::holds::{Amount, HoldAsk, HoldSource, HoldSpec, HoldsConfig, IntervalEnd};

const CORRECTIONS: [(&str, &str); 7] = [
    ("unavailable", "Ask less, or wait for supply to free; details.items lists each item that does not fit and what is free."),
    ("holds_missing", "Take the holds first: retakeHolds, or move the claim into a takeOn state again."),
    ("held_fields", "Let go of the holds first (move the claim into a releaseOn state), or leave details.fields as they are."),
    ("committed", "Move the claim into a returnOn state first, or keep it."),
    ("too_many_items", "Ask fewer items in one claim."),
    ("not_taking", "Move the claim into a takeOn state first; nothing is left to take when every item is held."),
    ("no_supply", "Name an instance that exists, of a schema that composes a supply."),
];

// hold says one hold: where its items come from and what each asks.
fn hold(name: &str, spec: &HoldSpec) -> String {
    let from = match &spec.source {
        HoldSource::Link { link, schema } => format!("the {schema} link {link} names"),
        HoldSource::Each {
            field,
            target,
            schema,
        } => format!("each element of {field}, by its {target} ({schema})"),
    };
    let asks = match &spec.ask {
        HoldAsk::Quantity(Amount::Count(count)) => format!("{count} units"),
        HoldAsk::Quantity(Amount::Field(field)) => format!("the units in {field}"),
        HoldAsk::Interval { start, end } => match end {
            IntervalEnd::Field(end) => format!("the time from {start} to {end}"),
            IntervalEnd::DurationMs(ms) => format!("{ms} ms from {start}"),
        },
    };
    format!("{name} holds {asks} of {from}")
}

// left says whether a Timers deadline of the type moves the claim out of
// a state: one that starts as the status enters it, or at the create for
// the initial state, and fires a transition.
fn left(target: &DescribeTarget, state: &str, initial: Option<&str>) -> bool {
    let Some(deadlines) = target
        .configs
        .get("Timers")
        .and_then(|config| config.get("deadlines"))
        .and_then(Value::as_object)
    else {
        return false;
    };
    deadlines.values().any(|deadline| {
        let start = deadline.get("start");
        let starts = start
            .and_then(|start| start.get("enters"))
            .and_then(Value::as_str)
            == Some(state)
            || (initial == Some(state)
                && start.and_then(|start| start.get("create")) == Some(&Value::Bool(true)));
        starts && deadline.get("then").is_some()
    })
}

pub(crate) fn guidance(config: &HoldsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let holds: Vec<String> = config
        .holds
        .iter()
        .map(|(name, spec)| hold(name, spec))
        .collect();
    let takes = match &config.take_on {
        Some(states) => format!(
            "Entering {} takes every hold, all or none.",
            list(states, "or")
        ),
        None => "The create takes every hold, all or none.".to_owned(),
    };
    let moves = [
        (!config.commit_on.is_empty())
            .then(|| format!("entering {} commits", list(&config.commit_on, "or"))),
        (!config.release_on.is_empty())
            .then(|| format!("entering {} releases", list(&config.release_on, "or"))),
        (!config.return_on.is_empty()).then(|| {
            format!(
                "entering {} returns committed units",
                list(&config.return_on, "or")
            )
        }),
    ]
    .into_iter()
    .flatten()
    .collect::<Vec<_>>();
    let unexpiring: Vec<&String> = config
        .take_on
        .iter()
        .flatten()
        .filter(|state| !left(target, state, config.initial.as_deref()))
        .collect();
    let reads = config.reads();
    let summary = sentences(&[
        Some(format!("Holds: {}.", holds.join("; "))),
        Some(takes),
        (!moves.is_empty()).then(|| format!("Then {}.", list(&moves, "and"))),
        (!reads.is_empty()).then(|| {
            format!(
                "While the claim holds, {} do not change.",
                list(&reads, "and")
            )
        }),
        (!unexpiring.is_empty()).then(|| {
            format!(
                "No Timers deadline leaves {}: what the claim holds there stays held until it moves.",
                list(&unexpiring, "or")
            )
        }),
    ]);
    let mut operations: Vec<(String, OperationGuidance)> = Vec::new();
    if target
        .operations
        .iter()
        .any(|operation| operation.name == "transition")
    {
        operations.push((
            "transition".to_owned(),
            said(
                None,
                None,
                None,
                errors(
                    &CORRECTIONS,
                    &[
                        "unavailable",
                        "holds_missing",
                        "too_many_items",
                        "no_supply",
                    ],
                ),
            ),
        ));
    }
    operations.extend([
        (
            "update".to_owned(),
            said(None, None, None, errors(&CORRECTIONS, &["held_fields", "unavailable"])),
        ),
        (
            "delete".to_owned(),
            said(None, None, None, errors(&CORRECTIONS, &["committed"])),
        ),
        (
            "retakeHolds".to_owned(),
            said(
                Some("Use when a claim in a takeOn state has items its supplies have not held yet.".to_owned()),
                None,
                Some("The result gives the items, the least advanced state and the total.".to_owned()),
                errors(&CORRECTIONS, &["not_taking", "unavailable", "too_many_items", "no_supply"]),
            ),
        ),
        (
            "listHolds".to_owned(),
            said(
                Some("Use to read the claim's items, with each supply's answer for an item not held yet.".to_owned()),
                None,
                None,
                Vec::new(),
            ),
        ),
    ]);
    BehaviorGuidance {
        summary,
        operations,
    }
}
