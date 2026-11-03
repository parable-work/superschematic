//! The recording's symbols (`runtime/engine/testdata/scenarios/README.md`,
//! "Symbols"): what differs from run to run is written `{{id:N}}`,
//! `{{time:N}}`, `{{dir:N}}` or `{{opaque:N}}`, in a place the value took
//! or inside a string that held it. The replayer binds a symbol at its
//! first appearance in a compared place to the value the host gave there,
//! requires the bound value wherever the symbol appears again, and writes
//! it into the arguments of later steps. Two symbols of one kind never
//! stand for one value.
//!
//! JSON compares as the README says: objects by member, regardless of
//! order; arrays in order; numbers by value.

use std::collections::BTreeMap;

use serde_json::{Map, Value};

use superschematic_engine_testkit::{iso_of, ms_of_iso};

/// What a symbol is bound to.
#[derive(Clone, Debug, PartialEq)]
enum Bound {
    Text(String),
    /// An instant, in epoch milliseconds.
    Time(i64),
}

/// One symbol's parts: `{{time:3:us}}` is kind `time`, number 3, form
/// `us`.
#[derive(Clone, Debug, PartialEq, Eq)]
struct Symbol {
    kind: String,
    name: String,
    micros: bool,
}

/// The symbols one scenario has bound so far.
#[derive(Clone, Debug, Default)]
pub(crate) struct Symbols {
    bound: BTreeMap<String, Bound>,
}

impl Symbols {
    /// Whether the host's value matches the recorded one, binding each
    /// symbol of the recorded value not bound yet.
    pub fn matches(&mut self, expected: &Value, actual: &Value) -> bool {
        match (expected, actual) {
            (Value::String(text), _) if symbol(text).is_some() => self.match_symbol(text, actual),
            (Value::String(text), Value::String(found)) if text.contains("{{") => {
                self.match_text(text, found)
            }
            (Value::Number(a), Value::Number(b)) => same_number(a, b),
            (Value::Array(a), Value::Array(b)) => {
                a.len() == b.len() && a.iter().zip(b).all(|(a, b)| self.matches(a, b))
            }
            (Value::Object(a), Value::Object(b)) => {
                a.len() == b.len()
                    && a.iter()
                        .all(|(key, a)| b.get(key).is_some_and(|b| self.matches(a, b)))
            }
            _ => expected == actual,
        }
    }

    /// A recorded value with every bound symbol written as its value: a
    /// symbol in place of a value as the value, one inside a string as
    /// its text. A symbol not bound yet stays as it is.
    pub fn substitute(&self, value: &Value) -> Value {
        match value {
            Value::String(text) => {
                if let Some(parsed) = symbol(text) {
                    return match self.bound.get(&key_of(&parsed)) {
                        Some(Bound::Text(bound)) => Value::from(bound.as_str()),
                        Some(Bound::Time(ms)) => Value::from(scaled(*ms, parsed.micros)),
                        None => value.clone(),
                    };
                }
                if !text.contains("{{") {
                    return value.clone();
                }
                let mut out = String::new();
                for piece in pieces(text) {
                    match piece {
                        Piece::Literal(literal) => out.push_str(literal),
                        Piece::Symbol(raw, parsed) => match self.bound.get(&key_of(&parsed)) {
                            Some(bound) => out.push_str(&text_of(bound, &parsed)),
                            None => out.push_str(raw),
                        },
                    }
                }
                Value::from(out)
            }
            Value::Array(items) => Value::Array(items.iter().map(|v| self.substitute(v)).collect()),
            Value::Object(members) => Value::Object(
                members
                    .iter()
                    .map(|(key, v)| (key.clone(), self.substitute(v)))
                    .collect::<Map<String, Value>>(),
            ),
            _ => value.clone(),
        }
    }

    // match_symbol matches a value against a symbol in its place.
    fn match_symbol(&mut self, text: &str, actual: &Value) -> bool {
        let Some(parsed) = symbol(text) else {
            return false;
        };
        let key = key_of(&parsed);
        if parsed.kind == "time" {
            let found = match actual {
                Value::Number(number) => {
                    number
                        .as_i64()
                        .map(|n| if parsed.micros { n.div_euclid(1000) } else { n })
                }
                Value::String(iso) => ms_of_iso(iso),
                _ => None,
            };
            let Some(found) = found else {
                return false;
            };
            return match self.bound.get(&key) {
                Some(Bound::Time(ms)) => *ms == found,
                Some(Bound::Text(_)) => false,
                None => self.bind(&parsed, Bound::Time(found)),
            };
        }
        let Value::String(found) = actual else {
            return false;
        };
        match self.bound.get(&key) {
            Some(Bound::Text(bound)) => bound == found,
            Some(Bound::Time(_)) => false,
            None => self.bind(&parsed, Bound::Text(found.clone())),
        }
    }

    // match_text matches a string against a recorded one with symbols
    // inside it: each literal piece in order, and each symbol the text
    // between it and the next literal.
    fn match_text(&mut self, text: &str, found: &str) -> bool {
        let pieces = pieces(text);
        let mut rest = found;
        for (at, piece) in pieces.iter().enumerate() {
            match piece {
                Piece::Literal(literal) => {
                    let Some(after) = rest.strip_prefix(literal) else {
                        return false;
                    };
                    rest = after;
                }
                Piece::Symbol(_, parsed) => {
                    if let Some(bound) = self.bound.get(&key_of(parsed)) {
                        let written = text_of(bound, parsed);
                        let Some(after) = rest.strip_prefix(written.as_str()) else {
                            return false;
                        };
                        rest = after;
                        continue;
                    }
                    let end = match pieces.get(at + 1) {
                        Some(Piece::Literal(next)) => match rest.find(next) {
                            Some(end) => end,
                            None => return false,
                        },
                        _ => rest.len(),
                    };
                    let (value, after) = rest.split_at(end);
                    let bound = match (parsed.kind.as_str(), ms_of_iso(value)) {
                        ("time", Some(ms)) => Bound::Time(ms),
                        _ => Bound::Text(value.to_owned()),
                    };
                    if !self.bind(parsed, bound) {
                        return false;
                    }
                    rest = after;
                }
            }
        }
        rest.is_empty()
    }

    // bind binds a symbol, refusing a value another symbol of its kind
    // holds.
    fn bind(&mut self, parsed: &Symbol, bound: Bound) -> bool {
        let taken = self.bound.iter().any(|(other, value)| {
            symbol(other).is_some_and(|other| other.kind == parsed.kind) && *value == bound
        });
        if taken {
            return false;
        }
        self.bound.insert(key_of(parsed), bound);
        true
    }
}

enum Piece<'t> {
    Literal(&'t str),
    Symbol(&'t str, Symbol),
}

// pieces splits a string into its literal text and its symbols.
fn pieces(text: &str) -> Vec<Piece<'_>> {
    let mut out = Vec::new();
    let mut rest = text;
    while let Some(start) = rest.find("{{") {
        let Some(len) = rest[start..].find("}}") else {
            break;
        };
        let raw = &rest[start..start + len + 2];
        match symbol(raw) {
            Some(parsed) => {
                if start > 0 {
                    out.push(Piece::Literal(&rest[..start]));
                }
                out.push(Piece::Symbol(raw, parsed));
            }
            None => out.push(Piece::Literal(&rest[..start + len + 2])),
        }
        rest = &rest[start + len + 2..];
    }
    if !rest.is_empty() {
        out.push(Piece::Literal(rest));
    }
    out
}

// symbol parses a whole string as one symbol: `{{kind:N}}` or
// `{{kind:N:form}}`.
fn symbol(text: &str) -> Option<Symbol> {
    let inner = text.strip_prefix("{{")?.strip_suffix("}}")?;
    let mut parts = inner.split(':');
    let kind = parts.next()?;
    let name = parts.next()?;
    let form = parts.next();
    if parts.next().is_some()
        || !matches!(kind, "id" | "time" | "dir" | "opaque")
        || name.is_empty()
        || !name.bytes().all(|b| b.is_ascii_digit())
    {
        return None;
    }
    let micros = match form {
        None => false,
        Some("us") if kind == "time" => true,
        // `{{id:N:62}}` is the id's base62 form: a value of its own.
        Some("62") if kind == "id" => false,
        Some(_) => return None,
    };
    let name = match form {
        Some("62") => format!("{name}:62"),
        _ => name.to_owned(),
    };
    Some(Symbol {
        kind: kind.to_owned(),
        name,
        micros,
    })
}

// key_of is the name a symbol is bound under: both forms of one instant
// share it.
fn key_of(parsed: &Symbol) -> String {
    format!("{{{{{}:{}}}}}", parsed.kind, parsed.name)
}

fn scaled(ms: i64, micros: bool) -> i64 {
    if micros {
        ms.saturating_mul(1000)
    } else {
        ms
    }
}

// text_of writes a bound value inside a string: an instant as its RFC 3339
// date-time, or its microseconds.
fn text_of(bound: &Bound, parsed: &Symbol) -> String {
    match bound {
        Bound::Text(text) => text.clone(),
        Bound::Time(ms) if parsed.micros => scaled(*ms, true).to_string(),
        Bound::Time(ms) => iso_of(*ms),
    }
}

// same_number compares two numbers by value: integers exactly, others as
// doubles.
fn same_number(a: &serde_json::Number, b: &serde_json::Number) -> bool {
    if let (Some(a), Some(b)) = (a.as_i64(), b.as_i64()) {
        return a == b;
    }
    if let (Some(a), Some(b)) = (a.as_u64(), b.as_u64()) {
        return a == b;
    }
    match (a.as_f64(), b.as_f64()) {
        (Some(a), Some(b)) => a == b,
        _ => a == b,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_symbol_binds_at_its_first_appearance_and_holds_after() {
        let mut symbols = Symbols::default();
        assert!(symbols.matches(&json!({"at": "{{time:1}}"}), &json!({"at": 7})));
        assert!(symbols.matches(&json!("{{time:1}}"), &json!(7)));
        assert!(!symbols.matches(&json!("{{time:1}}"), &json!(8)));
        // Another symbol of the kind never stands for the same value.
        assert!(!symbols.matches(&json!("{{time:2}}"), &json!(7)));
        assert!(symbols.matches(&json!("{{time:2}}"), &json!(9)));
        assert_eq!(symbols.substitute(&json!(["{{time:2}}"])), json!([9]));
    }

    #[test]
    fn a_symbol_inside_a_string_matches_the_text_it_stands_for() {
        let mut symbols = Symbols::default();
        assert!(symbols.matches(
            &json!("behavior Links vetoes create of Task {{id:1}}: no"),
            &json!("behavior Links vetoes create of Task abc-1: no")
        ));
        assert_eq!(symbols.substitute(&json!("{{id:1}}")), json!("abc-1"));
        assert_eq!(
            symbols.substitute(&json!("Task {{id:1}} and {{id:2}}")),
            json!("Task abc-1 and {{id:2}}")
        );
        assert!(!symbols.matches(&json!("Task {{id:1}}"), &json!("Task abc-2")));
    }

    #[test]
    fn json_compares_objects_by_member_and_numbers_by_value() {
        let mut symbols = Symbols::default();
        let a: Value = serde_json::from_str(r#"{"a": 1.0, "b": [1, 2]}"#).unwrap();
        let b: Value = serde_json::from_str(r#"{"b": [1, 2], "a": 1}"#).unwrap();
        assert!(symbols.matches(&a, &b));
        assert!(!symbols.matches(&json!([1, 2]), &json!([2, 1])));
        assert!(!symbols.matches(&json!({"a": 1}), &json!({"a": 1, "b": 2})));
    }
}
