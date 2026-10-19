//! JSON as the TypeScript engine writes and reads it, which every client of
//! the engine and every hash it keeps depend on (D58):
//!
//! - [`stringify()`] is `JSON.stringify`: an object's members in the order a
//!   JavaScript object iterates them (keys that are array indexes first,
//!   ascending, then the rest as inserted), numbers as JavaScript writes
//!   them ([`js_number`]) and strings with its escapes. The engine writes
//!   what a client reads with it.
//! - [`canonical_json`] is RFC 8785, the JSON Canonicalization Scheme, the
//!   port of `src/values/canonical.ts`: compact, members sorted by their
//!   keys' UTF-16 code units, strings and numbers as `JSON.stringify` writes
//!   them. Its SHA-256 ([`sha256_hex`]) is a stored value's hash, so a
//!   reader in any language computes the same hash from the same value.
//! - [`go_json`] is Go's `encoding/json`, the text a tool's
//!   `inputSchemaDigest` hashes (`src/tools/schema.ts`).
//!
//! serde_json keeps each number's text (`arbitrary_precision`), and each
//! writer here reads it as JavaScript's `JSON.parse` does, as a double:
//! `1.0` is written `1`, and a literal past the double range is Infinity,
//! which [`stringify()`] writes as `null` and [`canonical_json`] refuses.

use std::cmp::Ordering;
use std::fmt;

use serde_json::{Map, Number, Value};
use sha2::{Digest, Sha256};

/// The text JavaScript's `String(value)` gives a number: the shortest
/// decimal that reads back, in exponent form below 1e-6 and from 1e21;
/// `0` for either zero, and `NaN`, `Infinity` or `-Infinity`.
pub fn js_number(value: f64) -> String {
    if value == 0.0 {
        return "0".to_owned();
    }
    ryu_js::Buffer::new().format(value).to_owned()
}

/// A JSON number as JavaScript reads it: the double nearest its text,
/// infinite past the double range.
pub fn number_value(number: &Number) -> f64 {
    if let Some(value) = number.as_i64() {
        // Exact below 2^53, and rounded as JavaScript rounds the literal
        // above it.
        return value as f64;
    }
    number.to_string().parse::<f64>().unwrap_or(f64::NAN)
}

/// `JSON.stringify(value)`, compact.
pub fn stringify(value: &Value) -> String {
    let mut out = String::new();
    write_js(value, &mut out);
    out
}

fn write_js(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Value::Number(number) => {
            let value = number_value(number);
            if value.is_finite() {
                out.push_str(&js_number(value));
            } else {
                out.push_str("null");
            }
        }
        Value::String(text) => quote_into(text, out),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_js(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => {
            out.push('{');
            for (index, key) in js_keys(members).into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                quote_into(key, out);
                out.push(':');
                write_js(&members[key], out);
            }
            out.push('}');
        }
    }
}

/// An object's keys in the order a JavaScript object iterates them: the
/// keys that are array indexes (`0` to 2^32 - 2, with no leading zero)
/// ascending, then the rest in the order they were inserted.
pub fn js_keys(members: &Map<String, Value>) -> Vec<&str> {
    let mut indexes: Vec<(u32, &str)> = Vec::new();
    let mut rest: Vec<&str> = Vec::new();
    for key in members.keys() {
        match array_index(key) {
            Some(index) => indexes.push((index, key)),
            None => rest.push(key),
        }
    }
    indexes.sort_unstable();
    indexes
        .into_iter()
        .map(|(_, key)| key)
        .chain(rest)
        .collect()
}

fn array_index(key: &str) -> Option<u32> {
    if key.is_empty() || !key.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    if key.len() > 1 && key.starts_with('0') {
        return None;
    }
    key.parse::<u32>().ok().filter(|index| *index != u32::MAX)
}

/// `JSON.stringify(text)`: a string literal with JavaScript's escapes.
pub fn quote(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    quote_into(text, &mut out);
    out
}

fn quote_into(text: &str, out: &mut String) {
    out.push('"');
    for c in text.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c => out.push(c),
        }
    }
    out.push('"');
}

/// A value [`canonical_json`] cannot write: a number JavaScript reads as
/// Infinity. Its message is the TypeScript engine's TypeError.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct NotJson(pub String);

impl fmt::Display for NotJson {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.0)
    }
}

impl std::error::Error for NotJson {}

/// The canonical JSON of a value, RFC 8785: compact, every object's members
/// sorted by their keys' UTF-16 code units, strings and numbers as
/// `JSON.stringify` writes them.
pub fn canonical_json(value: &Value) -> Result<String, NotJson> {
    let mut out = String::new();
    write_canonical(value, &mut out)?;
    Ok(out)
}

fn write_canonical(value: &Value, out: &mut String) -> Result<(), NotJson> {
    match value {
        Value::Number(number) => {
            let value = number_value(number);
            if !value.is_finite() {
                return Err(NotJson(format!(
                    "{} is not a JSON number",
                    js_number(value)
                )));
            }
            out.push_str(&js_number(value));
        }
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_canonical(item, out)?;
            }
            out.push(']');
        }
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort_by(|a, b| compare_utf16(a, b));
            out.push('{');
            for (index, key) in keys.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                quote_into(key, out);
                out.push(':');
                write_canonical(&members[key.as_str()], out)?;
            }
            out.push('}');
        }
        other => write_js(other, out),
    }
    Ok(())
}

/// The order JavaScript's `<` and `Array.prototype.sort` give two strings:
/// by UTF-16 code unit, which differs from code point (byte) order only
/// past the Basic Multilingual Plane.
pub fn compare_utf16(a: &str, b: &str) -> Ordering {
    a.encode_utf16().cmp(b.encode_utf16())
}

/// The SHA-256 of a text's UTF-8 bytes, in lowercase hex.
pub fn sha256_hex(text: &str) -> String {
    hex(&Sha256::digest(text.as_bytes()))
}

fn hex(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        out.push(char::from(DIGITS[usize::from(byte >> 4)]));
        out.push(char::from(DIGITS[usize::from(byte & 0x0f)]));
    }
    out
}

/// A value as Go's `encoding/json` writes it: every object's keys sorted
/// (by UTF-16 code unit, as the TypeScript engine sorts them, which is
/// Go's byte order for the ASCII keys it writes), numbers as JavaScript
/// writes them, and `<`, `>`, `&`, U+2028 and U+2029 escaped.
pub fn go_json(value: &Value) -> String {
    let mut out = String::new();
    write_go(value, &mut out);
    out
}

fn write_go(value: &Value, out: &mut String) {
    match value {
        Value::String(text) => out.push_str(&go_string(text)),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_go(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort_by(|a, b| compare_utf16(a, b));
            out.push('{');
            for (index, key) in keys.into_iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                out.push_str(&go_string(key));
                out.push(':');
                write_go(&members[key.as_str()], out);
            }
            out.push('}');
        }
        other => write_js(other, out),
    }
}

/// A string literal as Go's `encoding/json` writes it: `JSON.stringify`'s,
/// with `<`, `>`, `&`, U+2028 and U+2029 escaped as `\u` sequences.
pub fn go_string(text: &str) -> String {
    let mut out = String::with_capacity(text.len() + 2);
    for c in quote(text).chars() {
        match c {
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c => out.push(c),
        }
    }
    out
}

/// `JSON.parse(JSON.stringify(value))`: the value as JavaScript reads it
/// back, each number its double's shortest text (`1.0` is `1`), each
/// object's members in the order a JavaScript object holds them, and a
/// number past the double range `null`.
pub fn js_copy(value: &Value) -> Value {
    serde_json::from_str(&stringify(value)).unwrap_or(Value::Null)
}

/// Whether two JSON values are equal as the TypeScript engine's
/// `jsonEqual` compares them: numbers by value, objects by their members
/// whatever their order, arrays item by item.
pub fn js_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => number_value(x) == number_value(y),
        (Value::Array(x), Value::Array(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(x, y)| js_equal(x, y))
        }
        (Value::Object(x), Value::Object(y)) => {
            x.len() == y.len()
                && x.iter()
                    .all(|(key, value)| y.get(key).is_some_and(|other| js_equal(value, other)))
        }
        _ => a == b,
    }
}

/// A JSON pointer from its tokens: each `~` written `~0` and each `/`
/// written `~1` (RFC 6901).
pub fn pointer<S: AsRef<str>>(tokens: &[S]) -> String {
    let mut out = String::new();
    for token in tokens {
        out.push('/');
        out.push_str(&token.as_ref().replace('~', "~0").replace('/', "~1"));
    }
    out
}

/// The member of the object a binding writes in place of a value JSON
/// cannot carry (a JavaScript NaN, a Date, a Map, an undefined list item),
/// its value the TypeScript engine's words for it (`NaN is not a JSON
/// number`, `a Date is not a JSON value`): the engine refuses it where the
/// TypeScript engine refuses that value, in its words (D58), rather than
/// meeting what JSON.stringify would have made of it.
pub const NOT_JSON: &str = "$superschematic:notJSON";

/// The member beside [`NOT_JSON`] that holds the value as JavaScript's
/// `String()` writes it (`NaN`, `undefined`), which a refusal that names
/// the value it was given names it by ([`crate::calls::js_string`]).
pub const NOT_JSON_STRING: &str = "$superschematic:string";

// marker is the members of a value a binding marked as no JSON: NOT_JSON,
// and NOT_JSON_STRING beside it, and nothing else.
fn marker(value: &Value) -> Option<&serde_json::Map<String, Value>> {
    let Value::Object(members) = value else {
        return None;
    };
    let marked = members.contains_key(NOT_JSON)
        && members
            .keys()
            .all(|key| key == NOT_JSON || key == NOT_JSON_STRING);
    marked.then_some(members)
}

/// The problem of a value a binding marked as no JSON ([`NOT_JSON`]).
pub fn not_json(value: &Value) -> Option<&str> {
    marker(value)?.get(NOT_JSON).and_then(Value::as_str)
}

/// A value a binding marked as no JSON as JavaScript's `String()` writes
/// it ([`NOT_JSON_STRING`]), when the binding says.
pub fn not_json_string(value: &Value) -> Option<&str> {
    marker(value)?.get(NOT_JSON_STRING).and_then(Value::as_str)
}

/// The first value a binding marked as no JSON in `value`, in the order
/// JavaScript walks it, with its path as the TypeScript engine's jsonCopy
/// writes one (`lines[0].sku`, `''` for the value itself) and its problem.
pub fn first_not_json(value: &Value) -> Option<(String, String)> {
    fn walk(value: &Value, path: &str) -> Option<(String, String)> {
        if let Some(problem) = not_json(value) {
            return Some((path.to_owned(), problem.to_owned()));
        }
        match value {
            Value::Array(items) => items
                .iter()
                .enumerate()
                .find_map(|(index, item)| walk(item, &format!("{path}[{index}]"))),
            Value::Object(members) => js_keys(members).into_iter().find_map(|key| {
                walk(
                    &members[key],
                    &if path.is_empty() {
                        key.to_owned()
                    } else {
                        format!("{path}.{key}")
                    },
                )
            }),
            _ => None,
        }
    }
    walk(value, "")
}

/// A jsonCopy path (`a.b[2]`) as the JSON pointer the TypeScript engine
/// turns it into (`/a/b/2`), `''` for the value itself.
pub fn path_pointer(path: &str) -> String {
    if path.is_empty() {
        return String::new();
    }
    let mut dotted = String::with_capacity(path.len());
    let mut rest = path;
    while let Some(open) = rest.find('[') {
        let Some(close) = rest[open..].find(']').map(|at| open + at) else {
            break;
        };
        let inside = &rest[open + 1..close];
        dotted.push_str(&rest[..open]);
        if !inside.is_empty() && inside.bytes().all(|b| b.is_ascii_digit()) {
            dotted.push('.');
            dotted.push_str(inside);
        } else {
            dotted.push_str(&rest[open..=close]);
        }
        rest = &rest[close + 1..];
    }
    dotted.push_str(rest);
    let dotted = dotted.strip_prefix('.').unwrap_or(&dotted);
    format!("/{}", dotted.split('.').collect::<Vec<_>>().join("/"))
}

/// A pointer's tokens, unescaped.
pub fn pointer_tokens(pointer: &str) -> Vec<String> {
    pointer
        .split('/')
        .skip(1)
        .map(|token| token.replace("~1", "/").replace("~0", "~"))
        .collect()
}

/// A string cut as the TypeScript engine cuts a value it shows in a
/// message: past 80 UTF-16 code units, its first 77 and `...`.
pub fn shown(text: &str) -> String {
    if text.encode_utf16().count() <= 80 {
        return text.to_owned();
    }
    let mut out = String::new();
    let mut units = 0;
    for c in text.chars() {
        units += c.len_utf16();
        if units > 77 {
            break;
        }
        out.push(c);
    }
    out.push_str("...");
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_value_marked_as_no_json_is_found_where_javascript_walks_to_it() {
        let nan = json!({ NOT_JSON: "NaN is not a JSON number" });
        let value = json!({"title": "Desk", "lines": [{"sku": "a"}, {"sku": nan}], "2": 1});
        assert_eq!(
            first_not_json(&value),
            Some((
                "lines[1].sku".to_owned(),
                "NaN is not a JSON number".to_owned()
            ))
        );
        assert_eq!(path_pointer("lines[1].sku"), "/lines/1/sku");
        assert_eq!(path_pointer("[0]"), "/0");
        assert_eq!(path_pointer(""), "");
        assert_eq!(first_not_json(&json!({"a": [1, 2]})), None);
        // An object that holds more than the marker is an object.
        assert_eq!(not_json(&json!({ NOT_JSON: "x", "other": 1 })), None);
        // The value as String() writes it rides beside the problem.
        let marked = json!({ NOT_JSON: "NaN is not a JSON number", NOT_JSON_STRING: "NaN" });
        assert_eq!(not_json(&marked), Some("NaN is not a JSON number"));
        assert_eq!(not_json_string(&marked), Some("NaN"));
        assert_eq!(not_json_string(&nan), None);
        assert_eq!(not_json(&json!({ NOT_JSON_STRING: "NaN" })), None);
    }

    fn parse(text: &str) -> Value {
        serde_json::from_str(text).unwrap()
    }

    #[test]
    fn numbers_are_written_as_javascript_writes_them() {
        let cases: [(f64, &str); 12] = [
            (0.0, "0"),
            (-0.0, "0"),
            (1.0, "1"),
            (-1.5, "-1.5"),
            (0.1, "0.1"),
            (1e21, "1e+21"),
            (123456789012345680000.0, "123456789012345680000"),
            (1e-6, "0.000001"),
            (1e-7, "1e-7"),
            (5e-324, "5e-324"),
            (9007199254740993.0, "9007199254740992"),
            (f64::INFINITY, "Infinity"),
        ];
        for (value, want) in cases {
            assert_eq!(js_number(value), want, "{value:?}");
        }
        // The literal's text is read as a double.
        assert_eq!(
            stringify(&parse("[1.0, 1e2, 1E-7, 12345678901234567890]")),
            "[1,100,1e-7,12345678901234567000]"
        );
        assert_eq!(stringify(&parse("1e400")), "null");
    }

    #[test]
    fn stringify_orders_members_as_a_javascript_object_does() {
        let value = parse(r#"{"b": 1, "2": true, "a": null, "1": "x", "01": [], "4294967295": 0}"#);
        assert_eq!(
            stringify(&value),
            r#"{"1":"x","2":true,"b":1,"a":null,"01":[],"4294967295":0}"#
        );
        assert_eq!(
            stringify(&json!("tab\t quote\" slash\\ \u{1} \u{2028} é")),
            "\"tab\\t quote\\\" slash\\\\ \\u0001 \u{2028} é\""
        );
    }

    #[test]
    fn canonical_json_is_rfc_8785() {
        // Members sorted by UTF-16 code units, as RFC 8785's example has
        // them: U+1F600 (a surrogate pair) before U+FB33.
        let value = parse("{\"\u{20ac}\": 1, \"\\r\": 2, \"\u{00f6}\": 3, \"1\": 4, \"\\ud83d\\ude00\": 5, \"\u{fb33}\": 6}");
        assert_eq!(
            canonical_json(&value).unwrap(),
            "{\"\\r\":2,\"1\":4,\"\u{00f6}\":3,\"\u{20ac}\":1,\"\u{1f600}\":5,\"\u{fb33}\":6}"
        );
        assert_eq!(
            canonical_json(&parse(
                r#"{"b": [1.50, -0, 1e30], "a": {"z": false, "y": null}}"#
            ))
            .unwrap(),
            r#"{"a":{"y":null,"z":false},"b":[1.5,0,1e+30]}"#
        );
        assert_eq!(
            canonical_json(&parse("[1e400]")).unwrap_err().to_string(),
            "Infinity is not a JSON number"
        );
        assert_eq!(
            sha256_hex(""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
        assert_eq!(
            sha256_hex("é"),
            "4a99557e4033c3539de2eb65472017cad5f9557f7a0625a09f1c3f6e2ba69c4c"
        );
    }

    #[test]
    fn go_json_sorts_keys_and_escapes_html() {
        assert_eq!(
            go_json(&json!({"b": "<a&b>", "a": [1, "\u{2028}"]})),
            r#"{"a":[1,"\u2028"],"b":"\u003ca\u0026b\u003e"}"#
        );
    }

    #[test]
    fn json_equal_compares_numbers_by_value_and_members_in_any_order() {
        assert!(js_equal(
            &parse(r#"{"a": 1.0, "b": [2]}"#),
            &parse(r#"{"b": [2e0], "a": 1}"#)
        ));
        assert!(!js_equal(
            &parse(r#"{"a": 1}"#),
            &parse(r#"{"a": 1, "b": 2}"#)
        ));
        assert!(!js_equal(&parse("[1, 2]"), &parse("[2, 1]")));
    }

    #[test]
    fn pointers_escape_their_tokens() {
        assert_eq!(pointer(&["types", "a/b~c"]), "/types/a~1b~0c");
        assert_eq!(pointer_tokens("/types/a~1b~0c"), ["types", "a/b~c"]);
        assert_eq!(shown("short"), "short");
        let long = "x".repeat(90);
        assert_eq!(shown(&long), format!("{}...", "x".repeat(77)));
    }
}
