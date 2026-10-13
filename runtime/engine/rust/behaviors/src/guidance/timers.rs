//! Timers' guidance (`guidance/timers.ts`): when each deadline is due,
//! what its fire moves and what cancels it, how a reader tells one is
//! overdue, and who may reset or snooze one, so a caller knows what a
//! create, a transition or an update starts; create, transition and update
//! hear the same.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{capital, duration, list, refusal, said, sentences};
use crate::timers::{TimersConfig, TimersDeadline, TimersStart, TimersUntil};

// due says when a deadline is due, from what it starts from.
fn due(deadline: &TimersDeadline) -> String {
    let after_ms = deadline.after_ms;
    let offset = if after_ms == 0 {
        String::new()
    } else {
        format!(
            "{} {} ",
            duration(after_ms.abs()),
            if after_ms < 0 { "before" } else { "after" }
        )
    };
    match &deadline.start {
        TimersStart::Create if after_ms == 0 => "at the create".to_owned(),
        TimersStart::Create => format!("{offset}the create"),
        TimersStart::Enters(state) if after_ms == 0 => format!("when the status enters {state}"),
        TimersStart::Enters(state) => format!("{offset}the status enters {state}"),
        // The TypeScript text runs "at" into "the" ("due atthe time
        // visitAt holds"); the port keeps its words until it changes.
        TimersStart::Field(field) => format!(
            "{}the time {field} holds",
            if after_ms == 0 { "at" } else { &offset }
        ),
    }
}

// cancels says what a deadline's until is, as a clause.
fn cancels(until: &TimersUntil) -> String {
    match until {
        TimersUntil::Leaves(state) => format!("it is cancelled when the status leaves {state}"),
        TimersUntil::Enters(states) => {
            format!(
                "it is cancelled when the status enters {}",
                list(states, "or")
            )
        }
    }
}

// deadline_text says one deadline as a sentence: when it is due, its move
// and what cancels it.
fn deadline_text(name: &str, deadline: &TimersDeadline) -> String {
    let mut parts = vec![format!("deadline {name} is due {}", due(deadline))];
    if let Some(then) = &deadline.then {
        parts.push(format!(
            "when it fires, it moves the status to {} from {}",
            then.transition,
            list(&then.from, "or")
        ));
    }
    if let Some(until) = &deadline.until {
        parts.push(cancels(until));
    }
    format!("{}.", capital(&parts.join("; ")))
}

// later says how long after its start a deadline is due, for a caller of
// the write that starts it.
fn later(deadline: &TimersDeadline) -> String {
    if deadline.after_ms == 0 {
        "due at once".to_owned()
    } else {
        format!("due {} later", duration(deadline.after_ms))
    }
}

pub(crate) fn guidance(config: &TimersConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let deadlines = &config.deadlines;
    let names: Vec<&str> = deadlines.iter().map(|(name, _)| name.as_str()).collect();
    let permission = config.permission.as_deref();
    let no_deadline = || {
        refusal(
            "no_deadline",
            None,
            &format!("Name a deadline: {}.", list(&names, "or")),
        )
    };
    let forbidden = permission
        .map(|permission| format!("It needs permission {permission}, else the call is forbidden."));
    let closed =
        "Do not use: the config names no permission, so the call is forbidden for every caller.";
    let open_or_closed = |open: String| match permission {
        None => String::new(),
        Some(_) => open,
    };

    // What a create, a transition and an update start or cancel.
    let created: Vec<String> = deadlines
        .iter()
        .filter(|(_, deadline)| deadline.start == TimersStart::Create)
        .map(|(name, deadline)| {
            format!(
                "A new {} starts deadline {name}, {}.",
                target.type_name,
                later(deadline)
            )
        })
        .collect();
    let fields: Vec<(&str, &str)> = deadlines
        .iter()
        .filter_map(|(name, deadline)| match &deadline.start {
            TimersStart::Field(field) => Some((name.as_str(), field.as_str())),
            _ => None,
        })
        .collect();
    let mut moves: Vec<String> = Vec::new();
    for (name, deadline) in deadlines {
        if let TimersStart::Enters(state) = &deadline.start {
            moves.push(format!(
                "Entering {state} starts deadline {name}, {}.",
                later(deadline)
            ));
        }
        match &deadline.until {
            Some(TimersUntil::Leaves(state)) => {
                moves.push(format!("Leaving {state} cancels deadline {name}."));
            }
            Some(TimersUntil::Enters(states)) => {
                moves.push(format!(
                    "Entering {} cancels deadline {name}.",
                    list(states, "or")
                ));
            }
            None => {}
        }
    }

    let mut operations = vec![
        (
            "fireTimer".to_owned(),
            said(
                Some("Use to fire a due deadline at once rather than wait for the runner's sweep.".to_owned()),
                Some("Do not use to move the instance early: on a deadline not due, fired already or not started, the call changes nothing.".to_owned()),
                Some("Returns fired, whether this call fired the deadline, and with a then the outcome of its move: moved, skipped (the status was not one it moves from), vetoed or forbidden; the deadline is fired whichever.".to_owned()),
                vec![no_deadline()],
            ),
        ),
        (
            "resetTimer".to_owned(),
            said(
                Some(open_or_closed(format!(
                    "Use to start a deadline again from what it starts from now, undoing a snooze or a fire. {}",
                    forbidden.clone().unwrap_or_default()
                ))),
                Some(match permission {
                    None => closed.to_owned(),
                    Some(_) => "Do not use to put a deadline off by a time of your choosing; snoozeTimer takes the dueAt.".to_owned(),
                }),
                Some("Returns dueAt, afterMs after what the deadline starts from; it has not fired.".to_owned()),
                vec![
                    no_deadline(),
                    refusal(
                        "not_armed",
                        None,
                        "Wait until the instance holds what the deadline starts from, the state it enters or a date-time in its field, and is out of the states that cancel it.",
                    ),
                ],
            ),
        ),
        (
            "snoozeTimer".to_owned(),
            said(
                Some(open_or_closed(format!(
                    "Use to move a started deadline's due time to dueAt, in epoch milliseconds; a fired one fires again then. {}",
                    forbidden.unwrap_or_default()
                ))),
                Some(match permission {
                    None => closed.to_owned(),
                    Some(_) => "Do not use on a deadline that has not started or was cancelled; behaviors.Timers.deadlines lists the started ones.".to_owned(),
                }),
                Some("Returns dueAt. A later write keeps it, until what the deadline starts from changes.".to_owned()),
                vec![
                    no_deadline(),
                    refusal(
                        "not_armed",
                        None,
                        "None: the deadline has no due time to move. Read behaviors.Timers.deadlines.",
                    ),
                ],
            ),
        ),
    ];
    if !created.is_empty() || !fields.is_empty() {
        let mut parts: Vec<Option<String>> = created.into_iter().map(Some).collect();
        parts.extend(fields.iter().map(|(name, field)| {
            Some(format!(
                "A {field} the create gives starts deadline {name}."
            ))
        }));
        operations.push((
            "create".to_owned(),
            OperationGuidance {
                success: Some(sentences(&parts)),
                ..OperationGuidance::default()
            },
        ));
    }
    if !moves.is_empty() {
        let parts: Vec<Option<String>> = moves.into_iter().map(Some).collect();
        operations.push((
            "transition".to_owned(),
            OperationGuidance {
                success: Some(sentences(&parts)),
                ..OperationGuidance::default()
            },
        ));
    }
    if !fields.is_empty() {
        let parts: Vec<Option<String>> = fields
            .iter()
            .map(|(name, field)| {
                Some(format!(
                    "Changing {field} moves deadline {name}, which fires again when it comes due; clearing {field} cancels it."
                ))
            })
            .collect();
        operations.push((
            "update".to_owned(),
            OperationGuidance {
                success: Some(sentences(&parts)),
                ..OperationGuidance::default()
            },
        ));
    }
    let mut summary: Vec<Option<String>> = deadlines
        .iter()
        .map(|(name, deadline)| Some(deadline_text(name, deadline)))
        .collect();
    summary.push(Some("behaviors.Timers.deadlines gives each started deadline's dueAt, fired, firedAt and outcome; one not fired is overdue once its dueAt has passed.".to_owned()));
    summary.push(Some(format!(
        "The runner fires each due deadline within {}, and a deadline stays fired whatever its move meets.",
        duration(config.sweep_ms)
    )));
    summary.push(Some(match permission {
        None => "Nobody resets or snoozes a deadline: the config names no permission.".to_owned(),
        Some(permission) => {
            format!("A caller with permission {permission} may reset or snooze a deadline.")
        }
    }));
    BehaviorGuidance {
        summary: sentences(&summary),
        operations,
    }
}
