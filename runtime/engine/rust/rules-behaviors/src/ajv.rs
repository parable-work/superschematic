//! A value held to a schema of a behavior's declaration, in ajv's words
//! (D58, D103): what the engine's registry told a client of an operation's
//! parameters, and the schema loader of a config, for the keywords the
//! commerce behaviors' declarations use (Stock, Holds, Calendar and
//! Payments, D70). The core runs no JSON Schema, so a behavior holds its
//! parameters to its declaration in its own code; the commerce behaviors
//! read the schema from the declaration they embed and walk it here, so a
//! paramsSchema and the check of it are one text.
//!
//! The keywords: `type` (a name or a list), `properties`, `required`,
//! `additionalProperties` (false or a schema), `minProperties`,
//! `maxProperties`, `propertyNames`, `minimum`, `maximum`, `minLength` and
//! `maxLength` (in code points), `pattern` ([`matches`], the subset of
//! ECMAScript patterns the declarations write), `format` (`uri`, checked
//! for a scheme), `enum`, `const`, `oneOf`, `allOf`, `if` with `then`,
//! `items`, `prefixItems`, `minItems`, `maxItems` and `uniqueItems`. Every
//! failure is reported, as `allErrors` has ajv do: a `oneOf` that no branch
//! matches lists each branch's failures before its own, and a `then` its
//! failures beside the `if`'s own. The issues come sorted as the engine
//! sorts them: by pointer, keyword and message, each by code point.
//!
//! A keyword outside the list is passed over, as ajv's `strict: false`
//! passes over one it does not know; a test of each behavior holds its
//! declaration to the list.

use serde_json::Value;
use superschematic_rules::SchemaIssue;

use crate::schema::{self, Issues};

/// Whose words a failure is written in.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Words {
    /// The behavior registry's, for an operation's parameters:
    /// `must NOT have additional properties: x`, `must be equal to one of
    /// the allowed values: "a", "b"`.
    Params,
    /// The schema loader's, for a config: `unknown key "x"`, `must be one
    /// of "a", "b"`.
    Config,
}

/// A declaration file's text read once as JSON, which a behavior keeps in a
/// `OnceLock` of its own; null for text that is no JSON.
pub(crate) fn file(text: &str) -> Value {
    serde_json::from_str(text).unwrap_or(Value::Null)
}

/// The paramsSchema a declaration file gives an operation; `None` for an
/// operation it does not declare or one with no schema.
pub(crate) fn params_schema<'f>(file: &'f Value, operation: &str) -> Option<&'f Value> {
    file.get("operations")?
        .as_array()?
        .iter()
        .find(|declared| declared.get("name").and_then(Value::as_str) == Some(operation))?
        .get("paramsSchema")
}

/// The issues an operation's parameters break of its paramsSchema in a
/// declaration file, in the registry's words; none for an operation with
/// no schema.
pub(crate) fn params_issues(file: &Value, operation: &str, params: &Value) -> Vec<SchemaIssue> {
    match params_schema(file, operation) {
        Some(schema) => issues(schema, params, Words::Params),
        None => Vec::new(),
    }
}

/// The issues a config breaks of a declaration file's configSchema, in
/// the schema loader's words.
pub(crate) fn config_issues(file: &Value, config: &Value) -> Vec<SchemaIssue> {
    match file.get("configSchema") {
        Some(schema) => issues(schema, config, Words::Config),
        None => Vec::new(),
    }
}

/// The issues a value breaks of a schema, in the engine's order.
pub(crate) fn issues(schema: &Value, value: &Value, words: Words) -> Vec<SchemaIssue> {
    let mut found = Issues::default();
    check(schema, value, "", words, &mut found);
    found.into_issues()
}

/// Whether a value meets a schema.
pub(crate) fn valid(schema: &Value, value: &Value) -> bool {
    let mut found = Issues::default();
    check(schema, value, "", Words::Params, &mut found);
    found.is_empty()
}

// check adds what a value at `at` breaks of a schema.
fn check(schema: &Value, value: &Value, at: &str, words: Words, found: &mut Issues) {
    let Some(keywords) = schema.as_object() else {
        if schema == &Value::Bool(false) {
            found.push(at, "false schema", "boolean schema is false");
        }
        return;
    };
    if let Some(types) = keywords.get("type") {
        if !type_matches(types, value) {
            let names: Vec<&str> = match types {
                Value::Array(names) => names.iter().filter_map(Value::as_str).collect(),
                other => other.as_str().into_iter().collect(),
            };
            found.push(at, "type", format!("must be {}", names.join(",")));
        }
    }
    if let Some(constant) = keywords.get("const") {
        if !crate::values::equal(Some(constant), Some(value)) {
            found.push(at, "const", "must be equal to constant");
        }
    }
    if let Some(Value::Array(allowed)) = keywords.get("enum") {
        if !allowed
            .iter()
            .any(|one| crate::values::equal(Some(one), Some(value)))
        {
            let listed = allowed
                .iter()
                .map(|one| serde_json::to_string(one).unwrap_or_default())
                .collect::<Vec<_>>()
                .join(", ");
            match words {
                Words::Params => found.push(
                    at,
                    "enum",
                    format!("must be equal to one of the allowed values: {listed}"),
                ),
                Words::Config => found.push(at, "enum", format!("must be one of {listed}")),
            }
        }
    }
    match value {
        Value::Number(number) => number_keywords(keywords, number, at, found),
        Value::String(text) => string_keywords(keywords, text, at, found),
        Value::Array(items) => array_keywords(keywords, items, at, words, found),
        Value::Object(members) => object_keywords(keywords, members, at, words, found),
        _ => {}
    }
    if let Some(Value::Array(branches)) = keywords.get("allOf") {
        for branch in branches {
            check(branch, value, at, words, found);
        }
    }
    if let Some(Value::Array(branches)) = keywords.get("oneOf") {
        let mut failures = Vec::new();
        let mut passing = 0;
        for branch in branches {
            let mut branch_found = Issues::default();
            check(branch, value, at, words, &mut branch_found);
            if branch_found.is_empty() {
                passing += 1;
            } else {
                failures.push(branch_found);
            }
        }
        if passing == 0 {
            for failed in failures {
                found.extend(failed);
            }
            found.push(at, "oneOf", "must match exactly one schema in oneOf");
        } else if passing > 1 {
            found.push(at, "oneOf", "must match exactly one schema in oneOf");
        }
    }
    if let (Some(condition), Some(then)) = (keywords.get("if"), keywords.get("then")) {
        if valid(condition, value) {
            let mut then_found = Issues::default();
            check(then, value, at, words, &mut then_found);
            if !then_found.is_empty() {
                found.extend(then_found);
                found.push(at, "if", "must match \"then\" schema");
            }
        }
    }
}

// type_matches says whether a value is of a type a schema names: integer
// is a whole number, as ajv reads 2.0.
fn type_matches(types: &Value, value: &Value) -> bool {
    let one = |name: &str| match name {
        "string" => value.is_string(),
        "integer" => value.as_number().is_some_and(|number| {
            number.is_i64()
                || number.is_u64()
                || number
                    .as_f64()
                    .is_some_and(|n| n.is_finite() && n.fract() == 0.0)
        }),
        "number" => value.is_number(),
        "boolean" => value.is_boolean(),
        "object" => value.is_object(),
        "array" => value.is_array(),
        "null" => value.is_null(),
        _ => true,
    };
    match types {
        Value::String(name) => one(name),
        Value::Array(names) => names.iter().filter_map(Value::as_str).any(one),
        _ => true,
    }
}

// limit is a bound as a message writes it: its own text, as JavaScript
// prints an integer.
fn limit(bound: &Value) -> String {
    bound.to_string()
}

fn number_keywords(
    keywords: &serde_json::Map<String, Value>,
    number: &serde_json::Number,
    at: &str,
    found: &mut Issues,
) {
    let n = number.as_f64().unwrap_or_default();
    if let Some(minimum) = keywords.get("minimum").filter(|bound| bound.is_number()) {
        if n < minimum.as_f64().unwrap_or_default() {
            found.push(at, "minimum", format!("must be >= {}", limit(minimum)));
        }
    }
    if let Some(maximum) = keywords.get("maximum").filter(|bound| bound.is_number()) {
        if n > maximum.as_f64().unwrap_or_default() {
            found.push(at, "maximum", format!("must be <= {}", limit(maximum)));
        }
    }
}

fn string_keywords(
    keywords: &serde_json::Map<String, Value>,
    text: &str,
    at: &str,
    found: &mut Issues,
) {
    let length = schema::length(text);
    if let Some(min) = keywords.get("minLength").and_then(Value::as_u64) {
        if (length as u64) < min {
            found.push(
                at,
                "minLength",
                format!("must NOT have fewer than {min} characters"),
            );
        }
    }
    if let Some(max) = keywords.get("maxLength").and_then(Value::as_u64) {
        if (length as u64) > max {
            found.push(
                at,
                "maxLength",
                format!("must NOT have more than {max} characters"),
            );
        }
    }
    if let Some(pattern) = keywords.get("pattern").and_then(Value::as_str) {
        if !matches(pattern, text) {
            found.push(at, "pattern", format!("must match pattern \"{pattern}\""));
        }
    }
    if keywords.get("format").and_then(Value::as_str) == Some("uri") && !is_uri(text) {
        found.push(at, "format", "must match format \"uri\"");
    }
}

// is_uri says whether a text begins with a scheme, a letter then letters,
// digits, +, - or ., and a colon, and holds no space.
fn is_uri(text: &str) -> bool {
    let Some((scheme, rest)) = text.split_once(':') else {
        return false;
    };
    let mut chars = scheme.chars();
    chars.next().is_some_and(|c| c.is_ascii_alphabetic())
        && chars.all(|c| c.is_ascii_alphanumeric() || matches!(c, '+' | '-' | '.'))
        && !rest.is_empty()
        && !text.chars().any(char::is_whitespace)
}

fn array_keywords(
    keywords: &serde_json::Map<String, Value>,
    items: &[Value],
    at: &str,
    words: Words,
    found: &mut Issues,
) {
    if let Some(min) = keywords.get("minItems").and_then(Value::as_u64) {
        if (items.len() as u64) < min {
            found.push(
                at,
                "minItems",
                format!("must NOT have fewer than {min} items"),
            );
        }
    }
    if let Some(max) = keywords.get("maxItems").and_then(Value::as_u64) {
        if (items.len() as u64) > max {
            found.push(
                at,
                "maxItems",
                format!("must NOT have more than {max} items"),
            );
        }
    }
    if keywords.get("uniqueItems") == Some(&Value::Bool(true)) {
        if let Some((j, i)) = schema::duplicate_value(items) {
            found.push(
                at,
                "uniqueItems",
                format!("must NOT have duplicate items (items ## {j} and {i} are identical)"),
            );
        }
    }
    let prefix = keywords
        .get("prefixItems")
        .and_then(Value::as_array)
        .map_or(&[][..], Vec::as_slice);
    for (index, item) in items.iter().enumerate() {
        let schema = match prefix.get(index) {
            Some(schema) => Some(schema),
            None => keywords.get("items"),
        };
        if let Some(schema) = schema {
            check(schema, item, &format!("{at}/{index}"), words, found);
        }
    }
}

fn object_keywords(
    keywords: &serde_json::Map<String, Value>,
    members: &serde_json::Map<String, Value>,
    at: &str,
    words: Words,
    found: &mut Issues,
) {
    if let Some(Value::Array(required)) = keywords.get("required") {
        for name in required.iter().filter_map(Value::as_str) {
            if !members.contains_key(name) {
                found.push(
                    at,
                    "required",
                    format!("must have required property '{name}'"),
                );
            }
        }
    }
    if let Some(min) = keywords.get("minProperties").and_then(Value::as_u64) {
        if (members.len() as u64) < min {
            found.push(
                at,
                "minProperties",
                format!("must NOT have fewer than {min} properties"),
            );
        }
    }
    if let Some(max) = keywords.get("maxProperties").and_then(Value::as_u64) {
        if (members.len() as u64) > max {
            found.push(
                at,
                "maxProperties",
                format!("must NOT have more than {max} properties"),
            );
        }
    }
    if let Some(names) = keywords.get("propertyNames") {
        for name in members.keys() {
            let mut name_found = Issues::default();
            check(
                names,
                &Value::from(name.as_str()),
                at,
                words,
                &mut name_found,
            );
            if !name_found.is_empty() {
                found.extend(name_found);
                found.push(at, "propertyNames", "property name must be valid");
            }
        }
    }
    let properties = keywords.get("properties").and_then(Value::as_object);
    for (name, member) in members {
        let path = schema::member(at, name);
        match properties.and_then(|properties| properties.get(name)) {
            Some(schema) => check(schema, member, &path, words, found),
            None => match keywords.get("additionalProperties") {
                Some(Value::Bool(false)) => match words {
                    Words::Params => found.push(
                        at,
                        "additionalProperties",
                        format!("must NOT have additional properties: {name}"),
                    ),
                    Words::Config => found.push(
                        at,
                        "additionalProperties",
                        format!("unknown key {}", schema::quoted(name)),
                    ),
                },
                Some(schema @ Value::Object(_)) => check(schema, member, &path, words, found),
                _ => {}
            },
        }
    }
}

/// Whether a text matches a pattern of the subset of ECMAScript patterns
/// the declarations write: `^` and `$`, literal characters and escapes
/// (`\d`, `\w`, `\.`), classes with ranges (`[A-Za-z0-9_-]`), groups with
/// alternatives (`(a|b)`), and the quantifiers `?`, `*`, `+`, `{n}` and
/// `{m,n}`. Without `^` or `$` a pattern matches anywhere, as `RegExp.test`
/// does. A pattern outside the subset matches nothing, which a test of each
/// declaration holds against.
pub(crate) fn matches(pattern: &str, text: &str) -> bool {
    let Some(node) = Parser::new(pattern).parse() else {
        return false;
    };
    let chars: Vec<char> = text.chars().collect();
    (0..=chars.len()).any(|start| {
        let mut ends = Vec::new();
        node.ends(&chars, start, &mut ends);
        !ends.is_empty()
    })
}

/// A pattern's parsed form.
#[derive(Clone, Debug)]
enum Node {
    /// Alternatives, each a sequence.
    Alt(Vec<Vec<Node>>),
    /// One character of a set.
    Class(Vec<(char, char)>, bool),
    Start,
    End,
    Repeat(Box<Node>, usize, Option<usize>),
}

impl Node {
    // ends adds every position a match from `at` may end at.
    fn ends(&self, chars: &[char], at: usize, out: &mut Vec<usize>) {
        match self {
            Node::Alt(branches) => {
                for branch in branches {
                    sequence_ends(branch, chars, at, out);
                }
            }
            Node::Class(ranges, negated) => {
                if let Some(c) = chars.get(at) {
                    let inside = ranges.iter().any(|(lo, hi)| lo <= c && c <= hi);
                    if inside != *negated {
                        out.push(at + 1);
                    }
                }
            }
            Node::Start => {
                if at == 0 {
                    out.push(at);
                }
            }
            Node::End => {
                if at == chars.len() {
                    out.push(at);
                }
            }
            Node::Repeat(node, min, max) => {
                // Breadth first by count: the positions after `count`
                // repeats, each kept once past the minimum, so a repeat
                // that consumes nothing ends.
                let mut current = vec![at];
                let mut seen: Vec<usize> = Vec::new();
                let mut count = 0;
                loop {
                    if count >= *min {
                        for end in &current {
                            if !seen.contains(end) {
                                seen.push(*end);
                                out.push(*end);
                            }
                        }
                    }
                    if max.is_some_and(|max| count >= max) || count > chars.len() + *min {
                        break;
                    }
                    let mut next = Vec::new();
                    for start in &current {
                        node.ends(chars, *start, &mut next);
                    }
                    next.sort_unstable();
                    next.dedup();
                    count += 1;
                    if count > *min {
                        next.retain(|end| !seen.contains(end));
                    }
                    if next.is_empty() {
                        break;
                    }
                    current = next;
                }
            }
        }
    }
}

fn sequence_ends(sequence: &[Node], chars: &[char], at: usize, out: &mut Vec<usize>) {
    let mut current = vec![at];
    for node in sequence {
        let mut next = Vec::new();
        for start in &current {
            node.ends(chars, *start, &mut next);
        }
        next.sort_unstable();
        next.dedup();
        if next.is_empty() {
            return;
        }
        current = next;
    }
    out.extend(current);
}

struct Parser {
    chars: Vec<char>,
    at: usize,
}

impl Parser {
    fn new(pattern: &str) -> Self {
        Parser {
            chars: pattern.chars().collect(),
            at: 0,
        }
    }

    fn parse(mut self) -> Option<Node> {
        let node = self.alternatives()?;
        (self.at == self.chars.len()).then_some(node)
    }

    fn alternatives(&mut self) -> Option<Node> {
        let mut branches = vec![self.sequence()?];
        while self.chars.get(self.at) == Some(&'|') {
            self.at += 1;
            branches.push(self.sequence()?);
        }
        Some(Node::Alt(branches))
    }

    fn sequence(&mut self) -> Option<Vec<Node>> {
        let mut out = Vec::new();
        while let Some(&c) = self.chars.get(self.at) {
            if c == '|' || c == ')' {
                break;
            }
            let atom = self.atom()?;
            out.push(self.quantified(atom)?);
        }
        Some(out)
    }

    fn atom(&mut self) -> Option<Node> {
        let c = *self.chars.get(self.at)?;
        self.at += 1;
        Some(match c {
            '^' => Node::Start,
            '$' => Node::End,
            '(' => {
                let inner = self.alternatives()?;
                if self.chars.get(self.at) != Some(&')') {
                    return None;
                }
                self.at += 1;
                inner
            }
            '[' => self.class()?,
            '\\' => self.escape()?,
            '.' => Node::Class(vec![('\n', '\n'), ('\r', '\r')], true),
            '?' | '*' | '+' | '{' | ')' => return None,
            c => Node::Class(vec![(c, c)], false),
        })
    }

    fn escape(&mut self) -> Option<Node> {
        let c = *self.chars.get(self.at)?;
        self.at += 1;
        Some(match c {
            'd' => Node::Class(vec![('0', '9')], false),
            'w' => Node::Class(vec![('a', 'z'), ('A', 'Z'), ('0', '9'), ('_', '_')], false),
            c if c.is_ascii_alphanumeric() => return None,
            c => Node::Class(vec![(c, c)], false),
        })
    }

    fn class(&mut self) -> Option<Node> {
        let negated = self.chars.get(self.at) == Some(&'^');
        if negated {
            self.at += 1;
        }
        let mut ranges = Vec::new();
        loop {
            let c = *self.chars.get(self.at)?;
            self.at += 1;
            if c == ']' {
                break;
            }
            let lo = if c == '\\' {
                let escaped = *self.chars.get(self.at)?;
                self.at += 1;
                match escaped {
                    'd' => {
                        ranges.push(('0', '9'));
                        continue;
                    }
                    'w' => {
                        ranges.extend([('a', 'z'), ('A', 'Z'), ('0', '9'), ('_', '_')]);
                        continue;
                    }
                    other => other,
                }
            } else {
                c
            };
            if self.chars.get(self.at) == Some(&'-')
                && self.chars.get(self.at + 1).is_some_and(|next| *next != ']')
            {
                let hi = *self.chars.get(self.at + 1)?;
                self.at += 2;
                ranges.push((lo, hi));
            } else {
                ranges.push((lo, lo));
            }
        }
        Some(Node::Class(ranges, negated))
    }

    fn quantified(&mut self, atom: Node) -> Option<Node> {
        let Some(&c) = self.chars.get(self.at) else {
            return Some(atom);
        };
        let (min, max) = match c {
            '?' => (0, Some(1)),
            '*' => (0, None),
            '+' => (1, None),
            '{' => {
                let close = self.chars[self.at..].iter().position(|c| *c == '}')? + self.at;
                let inner: String = self.chars[self.at + 1..close].iter().collect();
                let bounds = match inner.split_once(',') {
                    Some((lo, "")) => (lo.parse().ok()?, None),
                    Some((lo, hi)) => (lo.parse().ok()?, Some(hi.parse().ok()?)),
                    None => {
                        let n = inner.parse().ok()?;
                        (n, Some(n))
                    }
                };
                self.at = close;
                bounds
            }
            _ => return Some(atom),
        };
        self.at += 1;
        Some(Node::Repeat(Box::new(atom), min, max))
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_pattern_of_the_declarations_matches_as_a_regexp_does() {
        assert!(matches("^[A-Z]{3}$", "GBP"));
        assert!(!matches("^[A-Z]{3}$", "GB"));
        assert!(!matches("^[A-Z]{3}$", "GBPX"));
        assert!(matches("^[a-z][A-Za-z0-9]{0,63}$", "lines"));
        assert!(!matches("^[a-z][A-Za-z0-9]{0,63}$", "Lines"));
        assert!(matches("^[A-Za-z][A-Za-z0-9_+/-]*$", "Europe/London"));
        assert!(matches("^[A-Za-z][A-Za-z0-9_+/-]*$", "Etc/GMT+5"));
        let hhmm = "^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$";
        for good in ["00:00", "09:30", "23:59", "24:00"] {
            assert!(matches(hhmm, good), "{good}");
        }
        for bad in ["24:01", "9:30", "25:00", "12:60", ""] {
            assert!(!matches(hhmm, bad), "{bad}");
        }
        assert!(matches("^[A-Za-z0-9._:-]{1,255}$", "Refund:rf-1.0"));
        assert!(!matches("^[A-Za-z0-9._:-]{1,255}$", "a b"));
        assert!(matches("^[0-9]{4}$", "4242"));
        assert!(matches("^\\d{4}$", "4242"));
        assert!(matches("b", "abc"));
        assert!(!matches("^[A-Z", "A"));
    }

    #[test]
    fn issues_come_in_ajvs_words_and_order() {
        let schema = json!({
            "type": "object", "additionalProperties": false, "required": ["quantity", "reason"],
            "properties": {
                "quantity": {"type": "integer", "minimum": 1},
                "reason": {"type": "string", "minLength": 1, "maxLength": 3},
                "kind": {"type": "string", "enum": ["a", "b"]},
                "when": {"oneOf": [{"type": "integer"}, {"type": "null"}]}
            }
        });
        let listed = |value: Value, words| -> Vec<(String, String)> {
            issues(&schema, &value, words)
                .into_iter()
                .map(|issue| (issue.path, issue.message))
                .collect()
        };
        assert_eq!(
            listed(
                json!({"quantity": 0, "reason": "long", "x": 1, "kind": "c", "when": "now"}),
                Words::Params
            ),
            [
                ("".into(), "must NOT have additional properties: x".into()),
                (
                    "/kind".into(),
                    "must be equal to one of the allowed values: \"a\", \"b\"".into()
                ),
                ("/quantity".into(), "must be >= 1".into()),
                (
                    "/reason".into(),
                    "must NOT have more than 3 characters".into()
                ),
                (
                    "/when".into(),
                    "must match exactly one schema in oneOf".into()
                ),
                ("/when".into(), "must be integer".into()),
                ("/when".into(), "must be null".into()),
            ]
        );
        assert_eq!(
            listed(json!({"x": 1}), Words::Config),
            [
                ("".into(), "unknown key \"x\"".into()),
                ("".into(), "must have required property 'quantity'".into()),
                ("".into(), "must have required property 'reason'".into()),
            ]
        );
        assert!(valid(&schema, &json!({"quantity": 2.0, "reason": "ok"})));
    }
}
