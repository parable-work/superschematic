//! Budget's guidance (`typescript/src/guidance/budget.ts`): each meter's
//! limit, reservation, scope and day, what a reservation needs and when it
//! is settled, what usage over a limit does, and the refusals its guard
//! adds to Links' link and unlink of a scope link, to updates of a limit
//! field, and to a Queue claim, which reserves.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::{list, refusal, said, sentences};
use crate::budget::{BudgetConfig, BudgetMeter};

const CORRECTIONS: [(&str, &str); 7] = [
    ("over_limit", "Wait for reservations to settle or the day to pass, raise the limit with setLimit, or take other work; details.scope names where it did not fit."),
    ("not_leased", "Acquire the lease, or claim the instance, then reserve."),
    ("scope_moved", "Settle the reservation held through the old scope first."),
    ("scope_reserved", "Settle the reservation held through the link first."),
    ("below_committed", "Name a limit at or above what is used and reserved; details.committed says how much."),
    ("exceeds_reservation", "Settle or record no more than the instance reserved through this scope."),
    ("not_configured", "None: the config names no limitPermission, so limits do not change."),
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

// either says a field and a number a value falls back to: the field, else
// the number.
fn either(field: Option<&String>, number: Option<i64>) -> String {
    [
        field.map(|field| format!("the instance's {field}")),
        number.map(|number| number.to_string()),
    ]
    .into_iter()
    .flatten()
    .collect::<Vec<_>>()
    .join(", else ")
}

// meter says one meter: its limit, its reservation, its scope and its day.
fn meter(name: &str, spec: &BudgetMeter) -> String {
    let mut parts = Vec::new();
    if spec.limit.is_some() || spec.limit_field.is_some() {
        parts.push(format!(
            "limit {}",
            either(spec.limit_field.as_ref(), spec.limit)
        ));
    } else {
        parts.push("no limit".to_owned());
    }
    if spec.reserve.is_some() || spec.reserve_field.is_some() {
        parts.push(format!(
            "reserving {} at a claim",
            either(spec.reserve_field.as_ref(), spec.reserve)
        ));
    }
    if let Some(scope) = &spec.scope {
        parts.push(format!("drawn from the scope its {scope} link points at"));
    }
    if spec.daily {
        parts.push("counted per UTC day".to_owned());
    }
    format!("{name} ({})", parts.join("; "))
}

/// What Budget tells a caller about the type under its config.
pub(crate) fn guidance(config: &BudgetConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let names = config.names();
    let mut scopes: Vec<&str> = Vec::new();
    for (_, spec) in &config.meters {
        if let Some(scope) = &spec.scope {
            if !scopes.contains(&scope.as_str()) {
                scopes.push(scope);
            }
        }
    }
    let limit_fields: Vec<&str> = config
        .meters
        .iter()
        .filter_map(|(_, spec)| spec.limit_field.as_deref())
        .collect();
    let has = |name: &str| {
        target
            .operations
            .iter()
            .any(|operation| operation.name == name)
    };
    let permission = config.limit_permission.as_deref();
    let scope_side = || {
        Some("Do not call it yourself: an instance inside this scope calls it on its scope, as its reserve, settle and recordUsage run.".to_owned())
    };
    let mut operations: Vec<(String, OperationGuidance)> = Vec::new();
    if !scopes.is_empty() {
        for name in ["link", "unlink"] {
            if has(name) {
                operations.push((
                    name.to_owned(),
                    said(
                        None,
                        Some(format!(
                            "Do not move {} while a reservation is held through it.",
                            list(&scopes, "or")
                        )),
                        None,
                        errors(&["scope_reserved"]),
                    ),
                ));
            }
        }
    }
    let committed: &[&str] = if permission.is_none() {
        &["below_committed", "not_configured"]
    } else {
        &["below_committed"]
    };
    if !limit_fields.is_empty() {
        operations.push((
            "update".to_owned(),
            said(
                None,
                Some(format!(
                    "Do not change {}{} or below what is used and reserved.",
                    list(&limit_fields, "or"),
                    permission.map_or(String::new(), |permission| format!(
                        " without permission {permission}"
                    ))
                )),
                None,
                errors(committed),
            ),
        ));
    }
    if has("claim") {
        operations.push((
            "claim".to_owned(),
            said(None, None, None, errors(&["over_limit", "scope_moved"])),
        ));
    }
    let over = [
        config
            .on_exceeded
            .as_ref()
            .map(|direct| format!("sends directive {direct} to the lease holder")),
        config.escalate.as_ref().map(|rule| {
            format!(
                "moves status from {} to {}",
                list(&rule.from, "or"),
                rule.transition
            )
        }),
    ]
    .into_iter()
    .flatten()
    .collect::<Vec<_>>();
    let summary = sentences(&[
        Some(format!(
            "Meters: {}.",
            config
                .meters
                .iter()
                .map(|(name, spec)| meter(name, spec))
                .collect::<Vec<_>>()
                .join("; ")
        )),
        Some(format!(
            "A reservation fits while used plus reserved plus the amount is within the limit, here and in each enclosing scope{}.",
            if config.leased {
                "; it is made under the active lease and settled when the lease ends"
            } else {
                "; it lasts until settle"
            }
        )),
        (!over.is_empty()).then(|| format!("Usage over a limit {}.", list(&over, "and"))),
    ]);
    let reserve_errors: &[&str] = if config.leased {
        &["over_limit", "scope_moved", "not_leased"]
    } else {
        &["over_limit", "scope_moved"]
    };
    operations.extend([
        (
            "reserve".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!(
                        "Use to reserve budget before work: with no meter, every meter's configured amount ({}).",
                        list(&names, "and")
                    )),
                    config
                        .leased
                        .then(|| "It needs the active lease; a claim reserves for you.".to_owned()),
                ])),
                Some("Do not use to ask whether it would fit; call checkReserve.".to_owned()),
                Some("Returns what it reserved, by meter.".to_owned()),
                errors(reserve_errors),
            ),
        ),
        (
            "checkReserve".to_owned(),
            said(
                Some("Use to ask whether reserve would fit now, here and in every scope, without reserving.".to_owned()),
                None,
                Some("Returns fits, until (the next UTC day when that alone makes it fit, else null) and the scopes it read.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "recordUsage".to_owned(),
            said(
                Some("Use to record what the work used, by meter; it is never refused for its amount, and releases the reservation it covers.".to_owned()),
                None,
                Some("Returns used, what it released, and the overruns: each instance up the chain now over its limit.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "settle".to_owned(),
            said(
                Some(format!(
                    "Use to release what the instance's own reservations hold{}.",
                    if config.leased {
                        "; the end of a lease settles them too"
                    } else {
                        ""
                    }
                )),
                None,
                Some("Returns what it released, by meter.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "setLimit".to_owned(),
            said(
                permission.map(|permission| format!(
                    "Use to raise or lower a meter's limit; it needs permission {permission}, and never goes below what is used and reserved."
                )),
                permission.is_none().then(|| {
                    "Do not use: the config names no limitPermission, so limits do not change."
                        .to_owned()
                }),
                Some("Returns the meter, its limit and the one before.".to_owned()),
                errors(committed),
            ),
        ),
        (
            "reserveFor".to_owned(),
            said(None, scope_side(), None, errors(&["over_limit"])),
        ),
        (
            "settleFor".to_owned(),
            said(None, scope_side(), None, errors(&["exceeds_reservation"])),
        ),
        (
            "recordUsageFor".to_owned(),
            said(None, scope_side(), None, errors(&["exceeds_reservation"])),
        ),
    ]);
    BehaviorGuidance {
        summary,
        operations,
    }
}
