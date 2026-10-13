//! What each core behavior tells a caller about a type under its config
//! (`src/behaviors/core/guidance/*.ts`): the guidance milestone 6's
//! describe document and tools show beside the config, one module per
//! behavior, and the helpers they write their sentences with
//! (`guidance/text.ts`), so every behavior lists names the same way. The
//! text is plain ASCII and depends only on the config and the type.

pub(crate) mod comments;
pub(crate) mod constants;
pub(crate) mod links;
pub(crate) mod revisions;
pub(crate) mod variants;
pub(crate) mod workflow;

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
    fn names_join_as_a_sentence_lists_them() {
        assert_eq!(list::<&str>(&[], "and"), "");
        assert_eq!(list(&["a"], "and"), "a");
        assert_eq!(list(&["a", "b"], "or"), "a or b");
        assert_eq!(list(&["a", "b", "c"], "and"), "a, b and c");
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
