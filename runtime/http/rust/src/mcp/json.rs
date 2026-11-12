//! JSON text two runtimes must write the same: the canonical JSON a
//! confirmation binds a call's arguments as (D93), which the TypeScript
//! runtime's `canonicalJson` writes with `JSON.stringify`, and a number as
//! a path or a query string carries it, JavaScript's `String(value)`.
//!
//! A number that is an integer in serde_json's reading is written digit for
//! digit, which is JavaScript's text up to 2^53; past it JavaScript holds
//! the nearest double and this module the integer itself, so two calls that
//! differ in such an argument stay two calls. Any other number is the
//! double serde_json read it as, written as JavaScript writes one. Without
//! its `float_roundtrip` feature serde_json may read a literal of more than
//! 15 significant digits as the double beside the one JavaScript reads; the
//! text is then that double's, which is also the number the call's route
//! is sent.

use serde_json::{Map, Number, Value};
use std::cmp::Ordering;

/// `value` as JSON with every object's keys sorted, by UTF-16 code units as
/// JavaScript sorts them, and no space: one text for one value, whatever
/// order a client wrote it in. It is the TypeScript runtime's
/// `canonicalJson`, byte for byte, but for an integer past 2^53, which this
/// writes in full, and a number serde_json reads as another double than
/// JavaScript does.
pub fn canonical_json(value: &Value) -> String {
    let mut out = String::new();
    write_value(value, &mut out);
    out
}

/// [`canonical_json`] of an object, given as its map.
pub(super) fn canonical_object(members: &Map<String, Value>) -> String {
    let mut out = String::new();
    write_object(members, &mut out);
    out
}

fn write_value(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(true) => out.push_str("true"),
        Value::Bool(false) => out.push_str("false"),
        Value::Number(number) => out.push_str(&number_text(number)),
        Value::String(text) => write_string(text, out),
        Value::Array(items) => {
            out.push('[');
            for (index, item) in items.iter().enumerate() {
                if index > 0 {
                    out.push(',');
                }
                write_value(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => write_object(members, out),
    }
}

fn write_object(members: &Map<String, Value>, out: &mut String) {
    let mut sorted: Vec<(&String, &Value)> = members.iter().collect();
    sorted.sort_by(|(a, _), (b, _)| utf16_order(a, b));
    out.push('{');
    for (index, (key, value)) in sorted.into_iter().enumerate() {
        if index > 0 {
            out.push(',');
        }
        write_string(key, out);
        out.push(':');
        write_value(value, out);
    }
    out.push('}');
}

// utf16_order is the order of JavaScript's default sort: by UTF-16 code
// units, under which a character past the basic plane sorts before U+E000.
fn utf16_order(a: &str, b: &str) -> Ordering {
    a.encode_utf16().cmp(b.encode_utf16())
}

// write_string is `JSON.stringify` of a string: the quote and the backslash
// escaped, a control character by its short escape or four lowercase hex
// digits, and every other character as it is.
fn write_string(text: &str, out: &mut String) {
    out.push('"');
    for character in text.chars() {
        match character {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            control if control < '\u{20}' => {
                out.push_str(&format!("\\u{:04x}", u32::from(control)));
            }
            other => out.push(other),
        }
    }
    out.push('"');
}

/// A JSON number's text: an integer digit for digit, any other number as
/// JavaScript writes the double it reads as, and `null` for one past the
/// double range, which only serde_json's `arbitrary_precision` holds.
pub(super) fn number_text(number: &Number) -> String {
    if let Some(integer) = number.as_u64() {
        return integer.to_string();
    }
    if let Some(integer) = number.as_i64() {
        return integer.to_string();
    }
    number
        .as_f64()
        .filter(|value| value.is_finite())
        .map_or_else(|| "null".to_owned(), js_number)
}

// js_number is JavaScript's `String(value)` of a finite double (ECMA-262,
// Number::toString): the shortest digits that read back, written in full
// for an exponent from -6 to 21 and in exponent form outside it; `0` for
// either zero.
fn js_number(value: f64) -> String {
    if value == 0.0 {
        return "0".to_owned();
    }
    // Rust's exponent form holds the same shortest digits: `d.ddde-7`.
    let scientific = format!("{:e}", value.abs());
    let (mantissa, exponent) = scientific
        .split_once('e')
        .unwrap_or((scientific.as_str(), "0"));
    let digits: String = mantissa.chars().filter(char::is_ascii_digit).collect();
    let exponent: i64 = exponent.parse().unwrap_or(0);
    // The value is 0.digits * 10^point.
    let point = exponent + 1;
    let count = i64::try_from(digits.len()).unwrap_or(i64::MAX);
    let mut out = String::new();
    if value < 0.0 {
        out.push('-');
    }
    if (count..=21).contains(&point) {
        out.push_str(&digits);
        out.push_str(&zeros(point - count));
    } else if (1..=21).contains(&point) {
        let (whole, fraction) = digits.split_at(usize::try_from(point).unwrap_or(0));
        out.push_str(whole);
        out.push('.');
        out.push_str(fraction);
    } else if (-5..=0).contains(&point) {
        out.push_str("0.");
        out.push_str(&zeros(-point));
        out.push_str(&digits);
    } else {
        let (first, rest) = digits.split_at(1);
        out.push_str(first);
        if !rest.is_empty() {
            out.push('.');
            out.push_str(rest);
        }
        out.push('e');
        out.push(if exponent < 0 { '-' } else { '+' });
        out.push_str(&exponent.unsigned_abs().to_string());
    }
    out
}

fn zeros(count: i64) -> String {
    "0".repeat(usize::try_from(count).unwrap_or(0))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn canonical_json_writes_one_text_for_one_value() {
        assert_eq!(
            canonical_json(&json!({"b": [1, {"d": 2, "a": null}], "a": "x"})),
            r#"{"a":"x","b":[1,{"a":null,"d":2}]}"#
        );
        assert_eq!(
            canonical_json(&json!({"b": 1, "a": 2})),
            canonical_json(&json!({"a": 2, "b": 1}))
        );
        assert_eq!(canonical_json(&json!("\u{e9}\"")), "\"\u{e9}\\\"\"");
        assert_eq!(
            canonical_json(&json!("a\u{1}\n\u{1f}\u{7f}\u{2028}")),
            "\"a\\u0001\\n\\u001f\u{7f}\u{2028}\""
        );
    }

    #[test]
    fn keys_sort_by_utf16_code_units() {
        // U+1F600 is the surrogates D83D DE00, below U+E000 and U+FF5E.
        let value = json!({"\u{ff5e}": 1, "\u{1f600}": 2, "\u{e000}": 3, "a": 4});
        assert_eq!(
            canonical_json(&value),
            "{\"a\":4,\"\u{1f600}\":2,\"\u{e000}\":3,\"\u{ff5e}\":1}"
        );
    }

    #[test]
    fn a_number_is_written_as_javascript_writes_it() {
        for (value, text) in [
            (0.0, "0"),
            (-0.0, "0"),
            (1.0, "1"),
            (-1.5, "-1.5"),
            (0.1, "0.1"),
            (100.0, "100"),
            (123456.789, "123456.789"),
            (1e21, "1e+21"),
            (1.5e21, "1.5e+21"),
            (123456789012345680000.0, "123456789012345680000"),
            (1e-6, "0.000001"),
            (1.5e-6, "0.0000015"),
            (1e-7, "1e-7"),
            (1.25e-7, "1.25e-7"),
            (5e-324, "5e-324"),
            (1.7976931348623157e308, "1.7976931348623157e+308"),
            (0.30000000000000004, "0.30000000000000004"),
            (9007199254740992.0, "9007199254740992"),
        ] {
            assert_eq!(js_number(value), text, "{value:e}");
        }
    }

    #[test]
    fn an_integer_is_written_digit_for_digit() {
        assert_eq!(
            canonical_json(&json!([
                1,
                -7,
                9007199254740993_u64,
                u64::MAX,
                i64::MIN,
                2.50
            ])),
            "[1,-7,9007199254740993,18446744073709551615,-9223372036854775808,2.5]"
        );
    }
}
