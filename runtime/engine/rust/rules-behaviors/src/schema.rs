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

    /// Adds every issue another check found.
    pub fn extend(&mut self, other: Issues) {
        self.found.extend(other.found);
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

/// What a declaration's schemas hold a name to (a Workflow state's, a
/// Timers deadline's): a letter, then letters, digits, underscores and
/// hyphens, ASCII alone, and no more than [`NAME_MAX_LENGTH`] characters.
pub(crate) const NAME_PATTERN: &str = "^[A-Za-z][A-Za-z0-9_-]*$";
pub(crate) const NAME_MAX_LENGTH: usize = 64;

/// What a name breaks of [`NAME_PATTERN`] and [`NAME_MAX_LENGTH`], each a
/// keyword of its own.
pub(crate) fn name_issues(name: &str) -> Vec<(&'static str, String)> {
    let mut broken = Vec::new();
    if length(name) > NAME_MAX_LENGTH {
        broken.push((
            "maxLength",
            format!("must NOT have more than {NAME_MAX_LENGTH} characters"),
        ));
    }
    if !is_name(name) {
        broken.push(("pattern", format!("must match pattern \"{NAME_PATTERN}\"")));
    }
    broken
}

/// Whether a name matches [`NAME_PATTERN`].
pub(crate) fn is_name(name: &str) -> bool {
    let mut chars = name.chars();
    chars
        .next()
        .is_some_and(|first| first.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || c == '_' || c == '-')
}

/// What a value breaks of `{ "type": "string", "minLength": 1 }`.
pub(crate) fn text_issues(at: &str, value: &Value, issues: &mut Issues) {
    match value {
        Value::String(text) if length(text) < 1 => {
            issues.push(at, "minLength", "must NOT have fewer than 1 characters");
        }
        Value::String(_) => {}
        _ => issues.push(at, "type", "must be string"),
    }
}

/// What a value breaks of `{ "type": "integer" }` with an inclusive
/// minimum and maximum, as ajv checks them: a number that is not whole
/// fails its type, and the bounds hold any number.
pub(crate) fn integer_issues(
    at: &str,
    value: &Value,
    minimum: Option<i64>,
    maximum: Option<i64>,
    issues: &mut Issues,
) {
    let Some(number) = value.as_number() else {
        issues.push(at, "type", "must be integer");
        return;
    };
    let whole = number.is_i64()
        || number.is_u64()
        || number
            .as_f64()
            .is_some_and(|n| n.is_finite() && n.fract() == 0.0);
    if !whole {
        issues.push(at, "type", "must be integer");
    }
    let n = number.as_f64().unwrap_or_default();
    if let Some(minimum) = minimum.filter(|minimum| n < *minimum as f64) {
        issues.push(at, "minimum", format!("must be >= {minimum}"));
    }
    if let Some(maximum) = maximum.filter(|maximum| n > *maximum as f64) {
        issues.push(at, "maximum", format!("must be <= {maximum}"));
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

/// Two items of any list that are equal, as ajv's `uniqueItems` compares
/// them (deeply, a number by its value), named as [`duplicate`] names
/// them: `(j, i)`, from the first pair it meets reading from the end.
pub(crate) fn duplicate_value(items: &[Value]) -> Option<(usize, usize)> {
    for i in (0..items.len()).rev() {
        if let Some(j) =
            (i + 1..items.len()).find(|j| crate::values::equal(Some(&items[i]), Some(&items[*j])))
        {
            return Some((j, i));
        }
    }
    None
}

/// What a value breaks of `{ "type": "boolean" }`.
pub(crate) fn boolean_issues(at: &str, value: &Value, issues: &mut Issues) {
    if !value.is_boolean() {
        issues.push(at, "type", "must be boolean");
    }
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
    fn a_duplicate_of_any_value_is_named_as_ajv_names_it() {
        assert_eq!(
            duplicate_value(&[json!(1), json!(2), json!(1.0)]),
            Some((2, 0))
        );
        assert_eq!(
            duplicate_value(&[json!(1), json!(2), json!(1), json!(2)]),
            Some((3, 1))
        );
        assert_eq!(duplicate_value(&[json!(1), json!("1")]), None);
    }

    #[test]
    fn a_name_matches_the_pattern() {
        for name in ["draft", "in-review", "step_2", "A"] {
            assert!(is_name(name), "{name}");
        }
        for name in ["", "in review", "2nd", "_x", "é"] {
            assert!(!is_name(name), "{name}");
        }
        assert_eq!(name_issues(&"a".repeat(65)).len(), 1);
    }

    #[test]
    fn an_integer_is_held_to_its_type_and_bounds() {
        // found lists the issues as `<pointer>: <message>`, in order.
        let found = |value: Value| -> Vec<String> {
            let mut issues = Issues::default();
            integer_issues("/n", &value, Some(0), Some(10), &mut issues);
            issues
                .into_issues()
                .into_iter()
                .map(|issue| format!("{}: {}", issue.path, issue.message))
                .collect()
        };
        assert!(found(json!(5)).is_empty());
        assert_eq!(found(json!(1.5)), ["/n: must be integer"]);
        assert_eq!(found(json!(-1)), ["/n: must be >= 0"]);
        assert_eq!(
            found(json!(11.5)),
            ["/n: must be <= 10", "/n: must be integer"]
        );
        assert_eq!(found(json!("5")), ["/n: must be integer"]);
    }

    #[test]
    fn a_member_pointer_escapes_its_key() {
        assert_eq!(member("/outcomes", "a/b~c"), "/outcomes/a~1b~0c");
        assert_eq!(quoted("in \"review\""), "\"in \\\"review\\\"\"");
        assert_eq!(length("é"), 1);
    }
}
