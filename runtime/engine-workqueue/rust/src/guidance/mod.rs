//! What each work-queue behavior tells a caller about a type under its
//! config (`typescript/src/guidance/*.ts`): the guidance the describe
//! document and every tool of a type that composes it carry, one module
//! per behavior, and the helpers they write their sentences with
//! (`guidance/text.ts`), so every behavior lists names and says durations
//! the same way. The text is plain ASCII and depends only on the config and
//! the type.

#![allow(dead_code)]

// Milestone 12a: Lease, Assignment, Presence and Retries.

// Milestone 12b: Queue, Budget and Blueprint.
pub(crate) mod budget;

use superschematic_engine::behaviors::{GuidanceError, OperationGuidance};

/// Joins names as a sentence does: `a`; `a and b`; `a, b and c` (`list`).
pub(crate) fn list<S: AsRef<str>>(names: &[S], conjunction: &str) -> String {
    match names {
        [] => String::new(),
        [one] => one.as_ref().to_owned(),
        [rest @ .., last] => format!(
            "{} {conjunction} {}",
            rest.iter()
                .map(AsRef::as_ref)
                .collect::<Vec<_>>()
                .join(", "),
            last.as_ref()
        ),
    }
}

/// A count and a noun: `1 rollup`, `2 rollups` (`plural`).
pub(crate) fn plural(count: i64, noun: &str) -> String {
    plural_as(count, noun, &format!("{noun}s"))
}

/// A count and a noun whose plural is not the noun and an s: `1 class`,
/// `2 classes` (`plural` with `many`).
pub(crate) fn plural_as(count: i64, noun: &str, many: &str) -> String {
    if count == 1 {
        format!("{count} {noun}")
    } else {
        format!("{count} {many}")
    }
}

/// A span of milliseconds as a reader says it: `90 seconds`, `2 minutes`,
/// `30 days`, `1500 ms` (`duration`).
pub(crate) fn duration(ms: i64) -> String {
    for (unit, noun) in [
        (86_400_000, "day"),
        (3_600_000, "hour"),
        (60_000, "minute"),
        (1000, "second"),
    ] {
        if ms % unit == 0 {
            return plural(ms / unit, noun);
        }
    }
    format!("{ms} ms")
}

/// Starts a sentence with a capital letter (`capital`).
pub(crate) fn capital(text: &str) -> String {
    let mut chars = text.chars();
    match chars.next() {
        Some(first) => first.to_uppercase().chain(chars).collect(),
        None => String::new(),
    }
}

/// Joins the sentences that are there with a space (`sentences`).
pub(crate) fn sentences(parts: &[Option<String>]) -> String {
    parts
        .iter()
        .flatten()
        .filter(|part| !part.is_empty())
        .cloned()
        .collect::<Vec<_>>()
        .join(" ")
}

/// One operation's guidance, as the TypeScript object literal writes it:
/// each member it gives.
pub(crate) fn said(
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

/// A refusal an operation's guidance lists.
pub(crate) fn refusal(
    code: &str,
    description: Option<&str>,
    common_correction: &str,
) -> GuidanceError {
    GuidanceError {
        code: code.to_owned(),
        description: description.map(str::to_owned),
        common_correction: common_correction.to_owned(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_counts_and_spans_read_as_the_typescript_helpers_write_them() {
        assert_eq!(list::<&str>(&[], "and"), "");
        assert_eq!(list(&["a"], "and"), "a");
        assert_eq!(list(&["a", "b"], "or"), "a or b");
        assert_eq!(list(&["a", "b", "c"], "and"), "a, b and c");
        assert_eq!(plural(1, "lease"), "1 lease");
        assert_eq!(plural(0, "lease"), "0 leases");
        assert_eq!(plural_as(2, "class", "classes"), "2 classes");
        assert_eq!(duration(90_000), "90 seconds");
        assert_eq!(duration(120_000), "2 minutes");
        assert_eq!(duration(2_592_000_000), "30 days");
        assert_eq!(duration(3_600_000), "1 hour");
        assert_eq!(duration(1500), "1500 ms");
        assert_eq!(capital("the move"), "The move");
        assert_eq!(capital(""), "");
        assert_eq!(
            sentences(&[
                Some("One.".into()),
                None,
                Some(String::new()),
                Some("Two.".into())
            ]),
            "One. Two."
        );
    }
}
