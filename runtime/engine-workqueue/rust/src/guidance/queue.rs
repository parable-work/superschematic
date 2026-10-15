//! Queue's guidance (`typescript/src/guidance/queue.ts`): which states an
//! instance is claimed from and into, how claimNext orders and filters the
//! work, and what keeps an instance out of it, each from the config and the
//! behaviors the type composes. It adds claim_required to Lease's acquire,
//! which a claim takes instead.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};
use superschematic_engine_behaviors::{link_pin, LinkPin};

use super::{list, refusal, said, sentences};
use crate::queue::QueueConfig;

const TOKEN: &str = "preconditions {\"Lease\": {\"token\": n}}";

// pinned says what the links excludeStale names pin, read from the type's
// Links config: a revision, a release, or either.
fn pinned(config: &QueueConfig, target: &DescribeTarget) -> (&'static str, &'static str) {
    let links = target
        .configs
        .get("Links")
        .and_then(|links| links.get("links"))
        .and_then(|links| links.as_object());
    let pins: Vec<Option<LinkPin>> = config
        .exclude_stale
        .iter()
        .map(|name| links.and_then(|links| links.get(name)).and_then(link_pin))
        .collect();
    if pins.contains(&Some(LinkPin::Release)) {
        if pins.contains(&Some(LinkPin::Revision)) {
            ("a revision or a release", "revision or release")
        } else {
            ("a release", "release")
        }
    } else {
        ("a revision", "revision")
    }
}

/// What Queue tells a caller about the type under its config.
pub(crate) fn guidance(config: &QueueConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let from = list(&config.claim.from, "or");
    let (one, newest) = pinned(config, target);
    let mut out: Vec<String> = Vec::new();
    if config.dependencies {
        out.push("blocked".to_owned());
    }
    if config.retries {
        out.push("exhausted (Retries)".to_owned());
    }
    if config.budget {
        out.push("over its budget, here or in an enclosing scope (Budget)".to_owned());
    }
    if !config.exclude_stale.is_empty() {
        out.push(format!(
            "pinned through {} to {one} its target has moved past",
            list(&config.exclude_stale, "or")
        ));
    }
    if let Some(max) = config.max_expiries {
        out.push(format!("at {max} lease expiries"));
    }
    if config.assignment {
        out.push("assigned to another principal".to_owned());
    }
    let order = format!(
        "{}oldest first",
        config
            .priority_field
            .as_ref()
            .map_or(String::new(), |field| format!(
                "highest {field} first, then "
            ))
    );
    let matching = if config.match_fields.is_empty() {
        String::new()
    } else {
        format!(", matching {}", list(&config.match_fields, "and"))
    };
    let summary = sentences(&[
        Some(format!(
            "A claim takes an instance in {from}: its lease, {}and a move to {}, in one write.",
            if config.budget {
                "its budget reservation, "
            } else {
                ""
            },
            config.claim.to
        )),
        Some(format!(
            "claimNext claims the first the caller can, {order}{matching}, trying at most {}.",
            config.max_candidates
        )),
        (!out.is_empty()).then(|| {
            format!(
                "An instance is not claimed while it is {}.",
                list(&out, "or")
            )
        }),
    ]);
    let mut claim_errors = vec![refusal(
        "not_claimable",
        None,
        &format!(
            "None now: only an instance in {from} is claimed. Call claimNext for work that is."
        ),
    )];
    if config.dependencies {
        claim_errors.push(refusal(
            "blocked",
            None,
            "Wait for its blockers to finish, or take other work with claimNext.",
        ));
    }
    if !config.exclude_stale.is_empty() {
        claim_errors.push(refusal(
            "stale_link",
            None,
            &format!(
                "Link {} to its target's newest {newest} first, or take other work.",
                list(&config.exclude_stale, "or")
            ),
        ));
    }
    let operations: Vec<(String, OperationGuidance)> = vec![
        (
            "claim".to_owned(),
            said(
                Some(format!(
                    "Use to claim this {} when you know its id: it must be in {from}.",
                    target.type_name
                )),
                Some("Do not use to find work; call claimNext.".to_owned()),
                Some(format!(
                    "Returns id, token, expiresAt and heartbeatMs; the status is {}. Present the token as {TOKEN} on each write and heartbeat at least every heartbeatMs.",
                    config.claim.to
                )),
                claim_errors,
            ),
        ),
        (
            "claimNext".to_owned(),
            said(
                Some(sentences(&[
                    Some(format!(
                        "Use as a worker to take the next {} to work on, {order}.",
                        target.type_name
                    )),
                    (!config.match_fields.is_empty()).then(|| {
                        format!(
                            "match filters on {}, each a value or a list of values.",
                            list(&config.match_fields, "and")
                        )
                    }),
                    config
                        .assignment
                        .then(|| "assignedOnly takes only work assigned to you.".to_owned()),
                ])),
                Some("Do not use to count the work waiting; call countClaimable.".to_owned()),
                Some(format!(
                    "Returns claimed: id, token, expiresAt and heartbeatMs, or null when nothing is claimable now. Present the token as {TOKEN} on each write."
                )),
                Vec::new(),
            ),
        ),
        (
            "countClaimable".to_owned(),
            said(
                Some("Use to count the work claimNext would try for you, a signal to scale workers on; it claims nothing.".to_owned()),
                None,
                Some("Returns count.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "refresh".to_owned(),
            said(
                Some("Use only to recompute Queue's copies of the instance's facts; Queue invokes it itself when a blocker, a budget scope or a pinned link's target changes.".to_owned()),
                None,
                Some("Returns {}.".to_owned()),
                Vec::new(),
            ),
        ),
        (
            "acquire".to_owned(),
            said(
                None,
                Some(format!(
                    "Do not use: on {} the lease is taken by claim or claimNext.",
                    target.type_name
                )),
                None,
                vec![refusal("claim_required", None, "Call claim, or claimNext.")],
            ),
        ),
    ];
    BehaviorGuidance {
        summary,
        operations,
    }
}
