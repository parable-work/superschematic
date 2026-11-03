//! Lease's guidance (`typescript/src/guidance/lease.ts`): how long a lease
//! lasts, how often its holder renews it, how long it may be held, what an
//! expiry does to the status, the token every write under it presents,
//! the directives the runner sends when a link's target moves on
//! (directOn), and the filter a list takes on the holder. Lease's guard
//! refuses the writes of other behaviors and the instance's updates and
//! deletes while another principal holds the lease, so it adds its vetoes
//! to each of those operations' guidance too, but for the ones its config
//! exempts and Queue's refresh, which it lets through.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance, OperationScope,
};
use superschematic_engine_behaviors::LinkPin;

use super::{duration, list, refusal, said, sentences};
use crate::lease::{LeaseConfig, LeaseTransition};

const TOKEN: &str = r#"preconditions {"Lease": {"token": n}}"#;

// correction is what a caller does about a refusal of Lease's.
fn correction(code: &str) -> String {
    match code {
        "held_by_another" => "Wait until expiresAt in details, or work on another instance.".to_owned(),
        "held_by_caller" => "Keep the lease you hold: renew it with heartbeat.".to_owned(),
        "not_leased" => "Acquire the lease (or claim the instance) first.".to_owned(),
        "not_holder" => "Only the holder may do this; acquire the lease once it is free.".to_owned(),
        "lapsed" => "Stop working on the instance: the lease has lapsed. Acquire it again once it has expired.".to_owned(),
        "token_stale" => "Stop: a newer lease replaced yours. Read behaviors.Lease, and acquire again only if it is free.".to_owned(),
        "token_required" => format!("Present the lease's token as {TOKEN}."),
        "max_expiries" => "A caller with the override permission resets the count with resetExpiries.".to_owned(),
        "hold_limit_fixed" => "Change the field once the lease has ended, or with the override permission.".to_owned(),
        _ => "None: the type's Lease config names no permission that allows it.".to_owned(),
    }
}

fn errors(codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| refusal(code, None, &correction(code)))
        .collect()
}

// moves says a status move a Lease config makes.
fn moves(rule: &LeaseTransition) -> String {
    format!(
        "moves status from {} to {}",
        list(&rule.from, "or"),
        rule.transition
    )
}

/// What Lease tells a caller about the type under its config.
pub(crate) fn guidance(config: &LeaseConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let over = config.override_permission.as_deref();
    let hold = match (&config.max_hold_field, config.max_hold_ms) {
        (Some(field), ms) => Some(format!(
            "at most the instance's {field} milliseconds{}",
            ms.map(|ms| format!(", else {}", duration(ms)))
                .unwrap_or_default()
        )),
        (None, Some(ms)) => Some(format!("at most {}", duration(ms))),
        (None, None) => None,
    };
    let mut fenced_codes = vec!["held_by_another", "token_stale", "lapsed"];
    if config.require_token {
        fenced_codes.push("token_required");
    }
    let fenced_dont = if config.require_token {
        format!("Do not write under another principal's active lease; the holder presents its token as {TOKEN}.")
    } else {
        format!("Do not write under another principal's active lease; the holder may present its token as {TOKEN}, which a newer lease makes stale.")
    };
    let fenced = said(None, Some(fenced_dont.clone()), None, errors(&fenced_codes));
    // The writes Lease's guard holds while a lease is active: updates,
    // deletes and other behaviors' writing instance operations, but the
    // exempt ones, Queue's refresh and those beside the instance (D77),
    // which ask no guard but their own behavior's.
    let mut operations: Vec<(String, OperationGuidance)> = Vec::new();
    for operation in &target.operations {
        if operation.behavior.as_deref() == Some("Lease")
            || !operation.writes
            || operation.beside
            || operation.scope == OperationScope::Schema
            || operation.name == "create"
        {
            continue;
        }
        if let Some(behavior) = &operation.behavior {
            let named = format!("{behavior}.{}", operation.name);
            if config.exempt.contains(&named)
                || (behavior == "Queue" && operation.name == "refresh")
            {
                continue;
            }
        }
        let guidance = match &config.max_hold_field {
            Some(field) if operation.name == "update" => {
                let mut codes = fenced_codes.clone();
                codes.push("hold_limit_fixed");
                said(
                    None,
                    Some(sentences(&[
                        Some(fenced_dont.clone()),
                        Some(format!("{field} does not change while a lease is held.")),
                    ])),
                    None,
                    errors(&codes),
                )
            }
            _ => fenced.clone(),
        };
        operations.push((operation.name.clone(), guidance));
    }
    let moved: Vec<String> = config
        .direct_on
        .iter()
        .map(|rule| {
            format!(
                "directive {} when the {} link's target gains {}",
                rule.name,
                rule.link,
                match rule.pin {
                    Some(LinkPin::Revision) => "a revision past the pinned one or a release",
                    Some(LinkPin::Release) => "a revision or a release past the pinned one",
                    None => "a revision or a release",
                }
            )
        })
        .collect();
    let summary = sentences(&[
        Some(format!(
            "A lease gives one principal the {} for {} at a time, renewed by heartbeat at least every {}{}.",
            target.type_name,
            duration(config.ttl_ms),
            duration(config.heartbeat_ms),
            hold.map(|hold| format!(", and held {hold}")).unwrap_or_default()
        )),
        Some(format!(
            "The runner expires a lapsed lease within {}{}.",
            duration(config.sweep_ms),
            config
                .on_expiry
                .as_ref()
                .map(|rule| format!("; an expiry {}", moves(rule)))
                .unwrap_or_default()
        )),
        config.max_expiries.map(|max| {
            format!(
                "After {max} expiries it is not leased again{}.",
                config
                    .escalate
                    .as_ref()
                    .map(|rule| format!(", and that expiry {} instead", moves(rule)))
                    .unwrap_or_default()
            )
        }),
        Some(format!(
            "While a lease is active only its holder writes{}; each acquire gives a new token, which the holder presents as {TOKEN}{}.",
            if config.exempt.is_empty() {
                String::new()
            } else {
                format!(", but for {}", list(&config.exempt, "and"))
            },
            if config.require_token { " on every write" } else { "" }
        )),
        (!moved.is_empty()).then(|| {
            format!(
                "The runner sends the holder of an active lease {}; an instance with no lease held hears nothing, and its next holder nothing either.",
                list(&moved, "and")
            )
        }),
    ]);
    let holder_codes = [
        "not_leased",
        "not_holder",
        "token_required",
        "token_stale",
        "lapsed",
    ];
    let unsent = config.direct_permission.is_none() && over.is_none();
    let names: Vec<&str> = config
        .direct_on
        .iter()
        .map(|rule| rule.name.as_str())
        .collect();
    let mut acquire_codes = vec!["held_by_another", "held_by_caller", "token_stale"];
    if config.max_expiries.is_some() {
        acquire_codes.push("max_expiries");
    }
    let mut direct_codes = vec!["not_leased", "lapsed"];
    if unsent {
        direct_codes.push("not_configured");
    }
    operations.extend([
        (
            "list".to_owned(),
            said(
                Some(format!(
                    "where: {{ \"Lease.holder\": <subject> }} lists the {} instances whose lease a principal holds, a lapsed one until the runner expires it, and \"Lease.holder\": null the free ones.",
                    target.type_name
                )),
                None,
                None,
                Vec::new(),
            ),
        ),
        (
            "acquire".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!(
                        "Use to take the lease, for {} or a shorter ttlMs, before working on the instance alone.",
                        duration(config.ttl_ms)
                    )),
                    config.acquire_permission.as_ref().map(|permission| {
                        format!("It needs permission {permission}, else the call is forbidden.")
                    }),
                ])),
                None,
                Some(format!(
                    "Returns token, expiresAt and heartbeatMs: heartbeat at least every heartbeatMs and present the token as {TOKEN} on each write."
                )),
                errors(&acquire_codes),
            ),
        ),
        (
            "heartbeat".to_owned(),
            said(
                Some(format!(
                    "Use as the holder at least every {}, presenting the token, to keep the lease; acknowledge takes the ids of directives handled.",
                    duration(config.heartbeat_ms)
                )),
                None,
                Some(sentences(&[
                    Some("Returns the new expiresAt and the directives not yet acknowledged, oldest first.".to_owned()),
                    (!moved.is_empty()).then(|| {
                        format!(
                            "A directive the runner sends when a link's target moves on ({}) carries data.revised: the link, the target, and its revision, or its release and the release's commit.",
                            list(&names, "or")
                        )
                    }),
                ])),
                errors(&holder_codes),
            ),
        ),
        (
            "release".to_owned(),
            said(
                Some(sentences(&[
                    Some("Use as the holder, presenting the token, when done with the instance or handing it back.".to_owned()),
                    Some(format!(
                        "abandon true gives it up as failed, which counts as an expiry{}.",
                        config
                            .on_expiry
                            .as_ref()
                            .map(|rule| format!(" and {}", moves(rule)))
                            .unwrap_or_default()
                    )),
                ])),
                None,
                Some("Returns {}; the lease is free and its token stale.".to_owned()),
                errors(&holder_codes),
            ),
        ),
        (
            "expire".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!(
                        "Use to apply the expiry of a lapsed lease; the runner's sweep does so within {}.",
                        duration(config.sweep_ms)
                    )),
                    over.map(|permission| {
                        format!("With holder, it expires that principal's lease at once, and needs permission {permission}.")
                    }),
                ])),
                None,
                Some("Returns expired and its reason: ttl, maxHold or holder.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "direct".to_owned(),
            said(
                (!unsent).then(|| {
                    format!(
                        "Use to send a message to the holder of the active lease, which its next heartbeat returns; it needs permission {}.",
                        config
                            .direct_permission
                            .as_deref()
                            .or(over)
                            .unwrap_or_default()
                    )
                }),
                unsent.then(|| {
                    "Do not use: the config names no permission that sends directives.".to_owned()
                }),
                Some("Returns the directive id, and created false when one with its dedupeKey stands.".to_owned()),
                errors(&direct_codes),
            ),
        ),
        (
            "acknowledge".to_owned(),
            said(
                Some("Use as the holder, presenting the token, to stop directives' delivery; heartbeat's acknowledge does the same in the heartbeat's write.".to_owned()),
                None,
                Some("Returns {}.".to_owned()),
                errors(&holder_codes),
            ),
        ),
        (
            "resetExpiries".to_owned(),
            match over {
                None => said(
                    None,
                    Some("Do not use: the config names no overridePermission, which it needs.".to_owned()),
                    None,
                    errors(&["not_configured"]),
                ),
                Some(permission) => said(
                    Some(format!(
                        "Use to let an instance that reached its expiries be leased again; it needs permission {permission}."
                    )),
                    None,
                    Some("Returns the expiries it cleared.".to_owned()),
                    Vec::new(),
                ),
            },
        ),
        (
            "expireHolder".to_owned(),
            match over {
                None => said(
                    None,
                    Some("Do not use: the config names no overridePermission, which it needs.".to_owned()),
                    None,
                    Vec::new(),
                ),
                Some(permission) => said(
                    Some(format!(
                        "Use when a worker is gone: it expires every lease its principal holds on the schema; it needs permission {permission}. notRenewedAfter spares a lease renewed since then."
                    )),
                    None,
                    Some("Returns how many it expired, by reason, and their ids.".to_owned()),
                    Vec::new(),
                ),
            },
        ),
    ]);
    BehaviorGuidance {
        summary,
        operations,
    }
}
