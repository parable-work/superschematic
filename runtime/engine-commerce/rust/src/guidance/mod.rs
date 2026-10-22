//! What each commerce behavior tells a caller about a type under its
//! config: the guidance the describe document and every tool of a type
//! that composes it carry, one module per behavior, and the helpers they
//! write their sentences with, as the core's and the work queue's write
//! theirs. The text is plain ASCII and depends only on the config and the
//! type.

pub(crate) mod holds;
pub(crate) mod stock;

use superschematic_engine::behaviors::{GuidanceError, OperationGuidance};

/// Joins names as a sentence does: `a`; `a and b`; `a, b and c`.
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

/// Joins the sentences that are there with a space.
pub(crate) fn sentences(parts: &[Option<String>]) -> String {
    parts
        .iter()
        .flatten()
        .filter(|part| !part.is_empty())
        .cloned()
        .collect::<Vec<_>>()
        .join(" ")
}

/// One operation's guidance.
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

/// The refusals an operation's guidance lists, each with its correction
/// from `corrections`.
pub(crate) fn errors(corrections: &[(&str, &str)], codes: &[&str]) -> Vec<GuidanceError> {
    codes
        .iter()
        .map(|code| GuidanceError {
            code: (*code).to_owned(),
            description: None,
            common_correction: corrections
                .iter()
                .find(|(known, _)| known == code)
                .map_or("", |(_, correction)| correction)
                .to_owned(),
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn names_and_sentences_join_as_a_reader_says_them() {
        assert_eq!(list::<&str>(&[], "and"), "");
        assert_eq!(list(&["a"], "and"), "a");
        assert_eq!(list(&["a", "b", "c"], "or"), "a, b or c");
        assert_eq!(
            sentences(&[
                Some("One.".into()),
                None,
                Some(String::new()),
                Some("Two.".into())
            ]),
            "One. Two."
        );
        let found = errors(&[("a", "Do a.")], &["a", "b"]);
        assert_eq!(found[0].common_correction, "Do a.");
        assert_eq!(found[1].common_correction, "");
    }
}
