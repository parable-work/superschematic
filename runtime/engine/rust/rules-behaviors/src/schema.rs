//! The issues a declaration's configSchema and paramsSchema find, in the
//! words the engine gave them (D58). The core runs no JSON Schema (D103),
//! so a behavior holds a config and an operation's parameters to its
//! declaration's schemas in its own code, and words what it finds as ajv
//! does: a keyword's message at a JSON pointer, ordered by pointer, then
//! keyword, then message, each by code point, as the engine sorts ajv's
//! errors. A config's issues take the schema loader's words for an unknown
//! key and a value outside an enum (`unknown key "terminal"`); an
//! operation's parameters take the behavior registry's (`must NOT have
//! additional properties: status`).

use serde_json::Value;
use superschematic_rules::SchemaIssue;

/// What a check found: each issue with the keyword that orders it.
#[derive(Debug, Default)]
pub(crate) struct Issues {
    found: Vec<(String, String, String)>,
}

impl Issues {
    /// Adds an issue: where, the ajv keyword that failed, and its message.
    pub fn push(&mut self, path: impl Into<String>, keyword: &str, message: impl Into<String>) {
        self.found
            .push((path.into(), keyword.to_owned(), message.into()));
    }

    pub fn is_empty(&self) -> bool {
        self.found.is_empty()
    }

    /// The issues in the engine's order: by pointer, keyword and message,
    /// each by code point (the byte order of their UTF-8).
    pub fn into_issues(mut self) -> Vec<SchemaIssue> {
        self.found.sort();
        self.found
            .into_iter()
            .map(|(path, _, message)| SchemaIssue::new(path, message))
            .collect()
    }
}

/// A member's JSON pointer under an object's: `~` and `/` escaped.
pub(crate) fn member(at: &str, key: &str) -> String {
    format!("{at}/{}", key.replace('~', "~0").replace('/', "~1"))
}

/// A string as a message quotes it, as `JSON.stringify` writes it.
pub(crate) fn quoted(text: &str) -> String {
    serde_json::to_string(text).unwrap_or_else(|_| format!("\"{text}\""))
}

/// A string's length as ajv counts it, in code points.
pub(crate) fn length(text: &str) -> usize {
    text.chars().count()
}

/// Two string items of a list that are equal, as ajv's `uniqueItems`
/// names them for a list of strings: `(j, i)`, the later and the earlier,
/// from the first pair it meets reading from the end.
pub(crate) fn duplicate(items: &[Value]) -> Option<(usize, usize)> {
    let mut seen: Vec<(&str, usize)> = Vec::new();
    for (i, item) in items.iter().enumerate().rev() {
        let Some(text) = item.as_str() else {
            continue;
        };
        if let Some((_, j)) = seen.iter().find(|(known, _)| *known == text) {
            return Some((*j, i));
        }
        seen.push((text, i));
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn issues_come_by_pointer_then_keyword_then_message() {
        let mut issues = Issues::default();
        issues.push("", "required", "must have required property 'to'");
        issues.push("/to", "type", "must be string");
        issues.push(
            "",
            "additionalProperties",
            "must NOT have additional properties: status",
        );
        assert_eq!(
            issues.into_issues(),
            vec![
                SchemaIssue::new("", "must NOT have additional properties: status"),
                SchemaIssue::new("", "must have required property 'to'"),
                SchemaIssue::new("/to", "must be string"),
            ]
        );
    }

    #[test]
    fn a_duplicate_is_named_as_ajv_names_it() {
        assert_eq!(duplicate(&[json!("a"), json!("a")]), Some((1, 0)));
        assert_eq!(
            duplicate(&[json!("a"), json!("b"), json!("a"), json!("b")]),
            Some((3, 1))
        );
        assert_eq!(duplicate(&[json!("a"), json!(1), json!(1)]), None);
    }

    #[test]
    fn a_member_pointer_escapes_its_key() {
        assert_eq!(member("/outcomes", "a/b~c"), "/outcomes/a~1b~0c");
        assert_eq!(quoted("in \"review\""), "\"in \\\"review\\\"\"");
        assert_eq!(length("é"), 1);
    }
}
