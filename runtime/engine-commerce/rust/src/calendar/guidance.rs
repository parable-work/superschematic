//! Calendar's guidance: when the resource opens and in which zone, how a
//! hold must be shaped (grid, length, lead time, horizon, buffers,
//! capacity), that a claim holds time through its own Holds rather than by
//! calling the contract, and who may set hours and closures. The text is
//! plain ASCII and depends only on the config and the type.

use superschematic_engine::behaviors::{
    BehaviorGuidance, DescribeTarget, GuidanceError, OperationGuidance,
};

use super::time::{wall_text, DAYS};
use super::{CalendarConfig, Capacity};

/// A count and a noun: `1 hold`, `2 holds`.
pub(crate) fn plural(count: i64, noun: &str) -> String {
    if count == 1 {
        format!("{count} {noun}")
    } else {
        format!("{count} {noun}s")
    }
}

/// A span of milliseconds as a reader says it: `90 seconds`, `15
/// minutes`, `31 days`, `1500 ms`.
pub(crate) fn duration(ms: i64) -> String {
    for (unit, noun) in [
        (86_400_000, "day"),
        (3_600_000, "hour"),
        (60_000, "minute"),
        (1000, "second"),
    ] {
        if ms != 0 && ms % unit == 0 {
            return plural(ms / unit, noun);
        }
    }
    format!("{ms} ms")
}

fn refusal(code: &str, correction: &str) -> GuidanceError {
    GuidanceError {
        code: code.to_owned(),
        description: None,
        common_correction: correction.to_owned(),
    }
}

const CORRECTIONS: [(&str, &str); 13] = [
    ("not_requested", "Do not call it yourself: a claim's Holds calls it for the items it holds."),
    ("unavailable", "Pick another time or resource: findAvailability lists what is free; details.conflict is a hold in the way."),
    ("outside_hours", "Pick a time inside the opening hours, in the resource's timeZone."),
    ("closed", "Pick a time outside the closure in details.closure."),
    ("off_grid", "Start on the grid: a whole number of details.slotMs after an opening time."),
    ("too_short", "Ask at least details.minMs."),
    ("too_long", "Ask at most details.maxMs."),
    ("too_soon", "Start at or after details.earliestMs."),
    ("too_far", "Start at or before details.latestMs."),
    ("has_bookings", "Move the claims in details first, or force the closure with the permission's override."),
    ("window_too_long", "Ask for 31 days or less, and page through longer spans."),
    ("no_closure", "Name a closure the Calendar.closures field lists."),
    ("held", "Let go of, or move, the claims that hold its time first."),
];

fn errors(codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| {
            let correction = CORRECTIONS
                .iter()
                .find(|(known, _)| known == code)
                .map_or("", |(_, correction)| correction);
            refusal(code, correction)
        })
        .collect()
}

fn said(
    use_when: Option<String>,
    do_not_use_when: Option<String>,
    success: Option<String>,
    errors: Vec<GuidanceError>,
) -> OperationGuidance {
    OperationGuidance {
        use_when,
        do_not_use_when,
        success,
        errors,
    }
}

// hours says the config's week: each open day and its ranges.
fn hours(config: &CalendarConfig) -> String {
    let days: Vec<String> = DAYS
        .iter()
        .zip(&config.hours.days)
        .filter(|(_, ranges)| !ranges.is_empty())
        .map(|(day, ranges)| {
            format!(
                "{day} {}",
                ranges
                    .iter()
                    .map(|range| format!("{}-{}", wall_text(range.open), wall_text(range.close)))
                    .collect::<Vec<_>>()
                    .join(", ")
            )
        })
        .collect();
    if days.is_empty() {
        "it opens on no day".to_owned()
    } else {
        format!("it opens {}", days.join("; "))
    }
}

// shape says what a hold must be: its grid, length, lead and horizon.
fn shape(config: &CalendarConfig) -> String {
    let mut parts = vec![match config.slot_ms {
        Some(slot) => format!(
            "a hold starts a whole number of {} after an opening time",
            duration(slot)
        ),
        None => "a hold starts on a whole minute".to_owned(),
    }];
    parts.push(format!(
        "lasts {} to {}",
        duration(config.min_ms),
        duration(config.max_ms)
    ));
    if config.lead_ms > 0 {
        parts.push(format!(
            "starts at least {} from now",
            duration(config.lead_ms)
        ));
    }
    if let Some(horizon) = config.horizon_ms {
        parts.push(format!("and at most {} ahead", duration(horizon)));
    }
    parts.join(", ")
}

/// What Calendar tells a caller about the type under its config.
pub(crate) fn guidance(config: &CalendarConfig, target: &DescribeTarget) -> BehaviorGuidance {
    let capacity = match &config.capacity {
        Capacity::Fixed(1) => "one hold at a time".to_owned(),
        Capacity::Fixed(count) => format!("{count} holds at once"),
        Capacity::Field(field) => format!("as many holds at once as its {field} says"),
    };
    let buffers = match (config.buffer_before, config.buffer_after) {
        (0, 0) => None,
        (before, after) => Some(format!(
            " Each hold keeps {} free before it and {} after it, which count as the hold does.",
            duration(before),
            duration(after)
        )),
    };
    let summary = format!(
        "Each {} is a resource whose time a claim books through its Holds: {}, in {} unless an instance sets its own hours or zone, less its closures. It takes {capacity}; {}.{} Wall times a daylight-saving change skips have no slot, and repeated ones are open twice.",
        target.type_name,
        hours(config),
        config.time_zone,
        shape(config),
        buffers.unwrap_or_default()
    );
    let permission = config.permission.as_deref();
    let manage = |what: &str| {
        match permission {
        Some(permission) => said(
            Some(format!("Use to {what}.")),
            None,
            None,
            Vec::new(),
        )
        .with_note(format!("It needs permission {permission}, else the call is forbidden.")),
        None => said(
            None,
            Some("Do not use: the config names no permission, so the call is forbidden for every caller.".to_owned()),
            None,
            Vec::new(),
        ),
    }
    };
    let mut set_hours = manage("set this instance's own weekly hours or time zone; null goes back to the config's, and holds already made stay");
    set_hours.errors = Vec::new();
    let mut add = manage("close the resource for a holiday, a repair or a training day");
    add.errors = errors(&["has_bookings"]);
    let mut remove = manage("reopen the time a closure took");
    remove.errors = errors(&["no_closure"]);
    let contract = |verb: &str| {
        said(
            None,
            Some(format!(
                "Do not call it yourself: a claim's Holds {verb} its items' time through it, and anything else is refused."
            )),
            None,
            errors(&["not_requested"]),
        )
    };
    let mut hold = contract("holds");
    hold.errors = errors(&[
        "not_requested",
        "outside_hours",
        "closed",
        "off_grid",
        "too_short",
        "too_long",
        "too_soon",
        "too_far",
        "unavailable",
    ]);
    let operations = vec![
        ("setOpeningHours".to_owned(), set_hours),
        ("addClosure".to_owned(), add),
        ("removeClosure".to_owned(), remove),
        ("holdFor".to_owned(), hold),
        ("commitFor".to_owned(), contract("commits")),
        ("releaseFor".to_owned(), contract("releases")),
        (
            "checkHold".to_owned(),
            said(
                Some("Use to ask whether a claim's item would fit now, and why not; it changes nothing.".to_owned()),
                None,
                None,
                Vec::new(),
            ),
        ),
        (
            "listBookings".to_owned(),
            said(
                Some("Use to see an instance's held and committed time over up to 31 days.".to_owned()),
                None,
                None,
                errors(&["window_too_long"]),
            ),
        ),
        (
            "findAvailability".to_owned(),
            said(
                Some(format!(
                    "Use to find free slots of a duration across {}s, soonest first, by the rules a hold meets now; narrow them with ids or where. Joint availability over two resources is the caller's to intersect.",
                    target.type_name
                )),
                None,
                Some("Book a slot by creating or moving a claim whose Holds asks that interval of that instance; it can still be taken first.".to_owned()),
                errors(&["window_too_long", "too_short", "too_long"]),
            ),
        ),
        (
            "delete".to_owned(),
            said(None, None, None, errors(&["held"])),
        ),
    ];
    BehaviorGuidance {
        summary,
        operations,
    }
}

trait Note {
    fn with_note(self, note: String) -> Self;
}

impl Note for OperationGuidance {
    // with_note adds a sentence to useWhen.
    fn with_note(mut self, note: String) -> Self {
        self.use_when = Some(match self.use_when {
            Some(text) => format!("{text} {note}"),
            None => note,
        });
        self
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn spans_and_counts_read_as_a_reader_says_them() {
        assert_eq!(duration(900_000), "15 minutes");
        assert_eq!(duration(2_678_400_000), "31 days");
        assert_eq!(duration(1500), "1500 ms");
        assert_eq!(duration(0), "0 ms");
        assert_eq!(plural(1, "hold"), "1 hold");
        assert_eq!(plural(2, "hold"), "2 holds");
    }
}
