//! Stock's guidance: what the counts mean, who receives, adjusts and
//! counts, what a hold takes and when it is refused, and that claims reach
//! the supply contract through their own Holds, never directly.

use superschematic_engine::behaviors::{BehaviorGuidance, DescribeTarget, OperationGuidance};

use super::{errors, said, sentences};
use crate::stock::{CurrencySource, LowAt, StockConfig};

const CORRECTIONS: [(&str, &str); 5] = [
    ("below_held", "Release or commit what claims hold first, or count with the permission's .override; details give onHand and held."),
    ("unavailable", "Hold fewer units, receive more stock, or wait for claims to let go; details.available says how many are free."),
    ("over_max", "Hold at most details.max units per item."),
    ("not_requested", "Do not call the supply contract directly: a claim's Holds calls it when the claim moves."),
    ("held", "Let go of the claims' holds first: move each claim into a releaseOn state, or commit them."),
];

pub(crate) fn guidance(config: &StockConfig, _target: &DescribeTarget) -> BehaviorGuidance {
    let permission = &config.permission;
    let price = config.price.as_ref().map(|price| {
        format!(
            "A hold keeps the unit price {} holds, in {}, at the time of the hold.",
            price.field,
            match &price.currency {
                CurrencySource::Code(code) => code.clone(),
                CurrencySource::Field(field) => format!("the currency {field} holds"),
            }
        )
    });
    let low = config.low_at.as_ref().map(|low_at| {
        format!(
            "Stock.low is true while available is at or below {}; list with where {{ \"Stock.low\": true }} to find what to reorder.",
            match low_at {
                LowAt::Count(count) => count.to_string(),
                LowAt::Field(field) => format!("the instance's {field}"),
            }
        )
    });
    let summary = sentences(&[
        Some("Stock.onHand counts units on hand, Stock.held what claims hold, and Stock.available what a new hold can take.".to_owned()),
        Some(if config.backorder {
            "A hold may take more than is available (backorder), leaving available below 0.".to_owned()
        } else {
            "A hold never takes more than is available.".to_owned()
        }),
        config
            .max_per_hold
            .map(|max| format!("One item of a claim holds at most {max} units.")),
        price,
        low,
        Some("Claims hold, commit, release and return units through their Holds as they move; the contract's operations refuse a caller that is not the claim.".to_owned()),
    ]);
    let manage = |use_when: &str, codes: &[&str]| {
        said(
            Some(format!("{use_when} Needs permission {permission}.")),
            None,
            Some("The result gives onHand, held, available and the movement recorded.".to_owned()),
            errors(&CORRECTIONS, codes),
        )
    };
    let contract = || {
        said(
            None,
            Some(
                "Do not call it yourself: the claim's Holds calls it when the claim moves."
                    .to_owned(),
            ),
            None,
            errors(&CORRECTIONS, &["not_requested", "unavailable", "over_max"]),
        )
    };
    let mut operations: Vec<(String, OperationGuidance)> = vec![
        (
            "receiveStock".to_owned(),
            manage("Use when units arrive, with the reason and a reference such as a delivery note.", &[]),
        ),
        (
            "adjustStock".to_owned(),
            manage("Use to correct what is on hand by a delta: damage, loss, a correction.", &["below_held"]),
        ),
        (
            "countStock".to_owned(),
            manage(&format!("Use after a stock take to set what is on hand; a count below what claims hold needs {permission}.override."), &["below_held"]),
        ),
    ];
    for name in ["holdFor", "commitFor", "releaseFor", "returnFor"] {
        operations.push((name.to_owned(), contract()));
    }
    operations.push((
        "checkHold".to_owned(),
        said(
            Some(
                "Use to ask whether an item of a claim would fit now; it holds nothing.".to_owned(),
            ),
            None,
            None,
            Vec::new(),
        ),
    ));
    operations.push((
        "listStockMovements".to_owned(),
        said(
            Some("Use to read why what is on hand changed, newest first.".to_owned()),
            None,
            None,
            Vec::new(),
        ),
    ));
    operations.push((
        "delete".to_owned(),
        said(None, None, None, errors(&CORRECTIONS, &["held"])),
    ));
    BehaviorGuidance {
        summary,
        operations,
    }
}
