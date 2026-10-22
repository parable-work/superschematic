//! Payments' guidance: what is owed and in which currency, what the
//! Workflow waits for and what its states do to payments, who records
//! what providers did, and how each operation is refused and corrected.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::codes::*;
use super::{AmountSource, CurrencySource, PaymentsConfig};

const CORRECTIONS: [(&str, &str); 13] = [
    (NOTHING_DUE, "None: nothing is outstanding, or the instance is in a state that voids its payments; read Payments.outstanding."),
    (AMOUNT_LOCKED, "Void the attempt in flight, or let it finish, then change what is due."),
    (CURRENCY_MISMATCH, "Report the amount in the instance's currency (Payments.currency); a payment in another is the provider's to refund."),
    (OVER_CAPTURE, "Capture no more than is authorized; details.capturable says how much."),
    (EXCEEDS_REFUNDABLE, "Refund no more than details.refundable."),
    (UNPAID, "Wait until Payments.state is paid."),
    (UNAUTHORIZED, "Wait until Payments.state is authorized or paid."),
    (PAYMENT_IN_PROGRESS, "Wait for the provider to settle the processing attempt."),
    (NO_ATTEMPT, "Name an attempt, provider reference or refund the instance has, at its provider."),
    (NOT_LINKED, "None: only the refund instance this one's link names, in a state that issues it, asks or settles a refund."),
    (KEY_REUSED, "Use a new idempotencyKey for a different request, or repeat the first one's parameters."),
    (ATTEMPT_CLOSED, "Read Payments.attempts: capture an authorized attempt, void an open or authorized one."),
    (NOT_CONFIGURED, "None: the config does not allow it here."),
];

fn errors(codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| GuidanceError {
            code: (*code).to_owned(),
            description: None,
            common_correction: CORRECTIONS
                .iter()
                .find(|(known, _)| known == code)
                .map_or("", |(_, correction)| correction)
                .to_owned(),
        })
        .collect()
}

fn said(
    use_when: &str,
    do_not_use_when: Option<&str>,
    success: &str,
    codes: &[&str],
) -> OperationGuidance {
    OperationGuidance {
        use_when: Some(use_when.to_owned()),
        do_not_use_when: do_not_use_when.map(str::to_owned),
        success: Some(success.to_owned()),
        errors: errors(codes),
    }
}

fn list(items: &[String]) -> String {
    items.join(", ")
}

/// What Payments tells a caller about the type under its config.
pub(crate) fn guidance(config: &PaymentsConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let currency = match &config.currency {
        Some(CurrencySource::Code(code)) => code.clone(),
        Some(CurrencySource::Field(field)) => format!("the instance's {field}"),
        None => "Holds.total's".to_owned(),
    };
    let amount = match &config.amount {
        AmountSource::Field(field) => format!("its {field}"),
        AmountSource::Holds => "its Holds.total".to_owned(),
    };
    let mut summary = vec![format!(
        "{} {} owes {amount}, in {currency}, in minor units. The engine calls no payment provider: an adapter reads listPaymentActions, calls the provider with each action's id as its idempotency key, and reports what happened through recordPaymentEvent (permission {}). Payments stores no card data and no raw provider event.",
        target.type_name,
        if config.is_refund() { "is a refund that" } else { "" },
        config.record_permission
    )
    .replace("  ", " ")];
    if config.amount == AmountSource::Holds {
        summary.push(
            "What is owed is not known while a held line is pending, or its priced lines are in two currencies: openPayment and a move that waits for payment refuse with total_unknown. A claim with nothing priced owes 0."
                .to_owned(),
        );
    }
    if !config.require.is_empty() {
        summary.push(format!(
            "A move waits for payment: {}.",
            config
                .require
                .iter()
                .map(|(state, requirement)| format!("{state} once {}", requirement.as_str()))
                .collect::<Vec<_>>()
                .join("; ")
        ));
    }
    if !config.void_on.is_empty() {
        summary.push(format!(
            "Entering {} voids the open payments{}.",
            list(&config.void_on),
            if config.refund_after_release {
                ", and money captured there is refunded"
            } else {
                ", and money captured there is kept for an operator"
            }
        ));
    }
    if config.manual_capture {
        summary.push(if config.capture_on.is_empty() {
            "Capture is manual: capturePayment captures what is authorized.".to_owned()
        } else {
            format!(
                "Capture is manual: entering {} captures what is authorized.",
                list(&config.capture_on)
            )
        });
    }
    if let Some(link) = &config.refund_of {
        summary.push(format!(
            "Entering {} asks the instance its {link} link points at for the refund; Payments.state turns refunded when the provider refunds it, and back to unpaid when it fails.",
            list(&config.issue_on)
        ));
    }
    summary.push(
        "No move leaves a state while an attempt is processing, and what is due does not change while an attempt is in flight."
            .to_owned(),
    );
    let mut operations = Vec::new();
    if !config.is_refund() {
        operations.push((
            "openPayment".to_owned(),
            said(
                "Use to start paying what is outstanding at a provider; give the same idempotencyKey to retry.",
                Some("Do not use to pay part of it, or a second time while an attempt is in flight: void that one first."),
                "Returns the attempt, its amount and currency; the adapter creates it at the provider, and voids an earlier attempt that failed.",
                &[NOTHING_DUE, TOTAL_UNKNOWN, CURRENCY_MISMATCH, KEY_REUSED],
            ),
        ));
        operations.push((
            "recordPaymentEvent".to_owned(),
            said(
                "Use from the provider's adapter, with each provider event and each action's outcome.",
                Some("Do not use from an end user's tools."),
                "Returns whether the event moved anything (false for a repeat or a late event), the attempt and Payments.state.",
                &[CURRENCY_MISMATCH, OVER_CAPTURE, NO_ATTEMPT, KEY_REUSED, EXCEEDS_REFUNDABLE],
            ),
        ));
        operations.push((
            "capturePayment".to_owned(),
            said(
                "Use to capture an authorized attempt, all of it or an amount.",
                None,
                "Returns the capture action the adapter runs.",
                &[ATTEMPT_CLOSED, OVER_CAPTURE, NO_ATTEMPT],
            ),
        ));
        operations.push((
            "voidPayment".to_owned(),
            said(
                "Use to cancel an open attempt or void an authorized one.",
                Some("Do not use on a processing attempt: wait for it."),
                "Returns the void action the adapter runs.",
                &[ATTEMPT_CLOSED, NO_ATTEMPT],
            ),
        ));
        operations.push((
            "refundPayment".to_owned(),
            said(
                &match &config.refund_permission {
                    Some(permission) => {
                        format!("Use to refund captured money directly (permission {permission}).")
                    }
                    None => "Refunds go through a refund instance here.".to_owned(),
                },
                None,
                "Returns the refund and the action the adapter runs.",
                &[EXCEEDS_REFUNDABLE, KEY_REUSED, NOT_CONFIGURED],
            ),
        ));
        operations.push((
            "listPaymentActions".to_owned(),
            said(
                "Use from the provider's adapter to read what it is to do, oldest first.",
                Some("Do not use from an end user's tools."),
                "Returns a page of waiting actions; an action stops waiting when an event for it is recorded.",
                &[],
            ),
        ));
        operations.push((
            "refundFor".to_owned(),
            said(
                "Do not call it yourself: a refund instance's Payments calls it as the instance enters an issueOn state.",
                None,
                "Returns the refund: one per refund instance while it is asked or paid, under the key that instance derives.",
                &[NOT_LINKED, KEY_REUSED, EXCEEDS_REFUNDABLE, CURRENCY_MISMATCH],
            ),
        ));
    } else {
        operations.push((
            "settleRefundFor".to_owned(),
            said(
                "Do not call it yourself: the refunded instance's Payments calls it when the provider's refund event is recorded there.",
                None,
                "Returns Payments.state.",
                &[NOT_LINKED, NO_ATTEMPT],
            ),
        ));
    }
    if !config.require.is_empty() || !config.void_on.is_empty() {
        operations.push((
            "transition".to_owned(),
            said(
                "Moves wait for payment as the summary says.",
                None,
                "The move is made.",
                &[UNPAID, UNAUTHORIZED, PAYMENT_IN_PROGRESS],
            ),
        ));
    }
    BehaviorGuidance {
        summary: summary.join(" "),
        operations: operations
            .into_iter()
            .filter(|(name, _)| {
                target
                    .operations
                    .iter()
                    .any(|operation| operation.name == *name)
            })
            .collect(),
    }
}
