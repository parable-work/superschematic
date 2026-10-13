//! Values written and read as JavaScript writes and reads them: the text
//! `JSON.stringify` gives a value, which the TypeScript runtime hands the
//! scalar core for a structured scalar, and the number `Number(text)` reads.

use serde_json::{Map, Number, Value};

use crate::jsnum::js_number;

/// The text `JSON.stringify` writes for value: object keys that are array
/// indexes first, ascending, then the rest in order; numbers as JavaScript
/// writes them; strings with JavaScript's escapes.
pub(crate) fn stringify(value: &Value) -> String {
    let mut out = String::new();
    write(value, &mut out);
    out
}

fn write(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Value::Number(number) => match number.as_f64() {
            Some(n) if n.is_finite() => out.push_str(&js_number(n)),
            _ => out.push_str("null"),
        },
        Value::String(text) => quote(text, out),
        Value::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write(item, out);
            }
            out.push(']');
        }
        Value::Object(map) => {
            out.push('{');
            for (i, key) in js_key_order(map).into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                quote(key, out);
                out.push(':');
                if let Some(member) = map.get(key) {
                    write(member, out);
                }
            }
            out.push('}');
        }
    }
}

/// An object's keys in the order JavaScript iterates them.
fn js_key_order(map: &Map<String, Value>) -> Vec<&str> {
    let mut indexes: Vec<(u32, &str)> = Vec::new();
    let mut rest: Vec<&str> = Vec::new();
    for key in map.keys() {
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

fn quote(text: &str, out: &mut String) {
    out.push('"');
    for c in text.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// Whether c is white space to JavaScript's `String.prototype.trim`.
fn js_space(c: char) -> bool {
    c.is_whitespace() || c == '\u{feff}'
}

/// text without the white space JavaScript's `trim` removes.
pub(crate) fn js_trim(text: &str) -> &str {
    text.trim_matches(js_space)
}

/// The number JavaScript's `Number(text)` reads: NaN for text that is no
/// number, 0 for blank text.
pub(crate) fn js_string_to_number(text: &str) -> f64 {
    let trimmed = js_trim(text);
    if trimmed.is_empty() {
        return 0.0;
    }
    if let Some(value) = radix_integer(trimmed) {
        return value;
    }
    let unsigned = trimmed
        .strip_prefix('+')
        .or_else(|| trimmed.strip_prefix('-'))
        .unwrap_or(trimmed);
    if unsigned == "Infinity" {
        return if trimmed.starts_with('-') {
            f64::NEG_INFINITY
        } else {
            f64::INFINITY
        };
    }
    if !is_decimal_literal(unsigned) {
        return f64::NAN;
    }
    trimmed.parse::<f64>().unwrap_or(f64::NAN)
}

/// A `0x`, `0o` or `0b` literal, which JavaScript reads without a sign.
fn radix_integer(text: &str) -> Option<f64> {
    let bytes = text.as_bytes();
    if bytes.len() < 3 || bytes[0] != b'0' {
        return None;
    }
    let radix = match bytes[1] {
        b'x' | b'X' => 16,
        b'o' | b'O' => 8,
        b'b' | b'B' => 2,
        _ => return None,
    };
    let digits = &text[2..];
    if !digits.chars().all(|c| c.is_digit(radix)) {
        return Some(f64::NAN);
    }
    let mut value = 0.0_f64;
    for c in digits.chars() {
        value = value * f64::from(radix) + f64::from(c.to_digit(radix).unwrap_or(0));
    }
    Some(value)
}

/// Digits with an optional fraction and exponent: `12`, `1.5`, `.5`, `5.`,
/// `1e3`.
fn is_decimal_literal(text: &str) -> bool {
    let (mantissa, exponent) = match text.find(['e', 'E']) {
        Some(at) => (&text[..at], Some(&text[at + 1..])),
        None => (text, None),
    };
    let (whole, fraction) = match mantissa.split_once('.') {
        Some((whole, fraction)) => (whole, Some(fraction)),
        None => (mantissa, None),
    };
    let digits = |part: &str| part.bytes().all(|b| b.is_ascii_digit());
    if !digits(whole) || !fraction.is_none_or(digits) {
        return false;
    }
    if whole.is_empty() && fraction.is_none_or(str::is_empty) {
        return false;
    }
    match exponent {
        None => true,
        Some(exponent) => {
            let unsigned = exponent
                .strip_prefix('+')
                .or_else(|| exponent.strip_prefix('-'))
                .unwrap_or(exponent);
            !unsigned.is_empty() && digits(unsigned)
        }
    }
}

/// The largest integer a float64 holds exactly, `Number.MAX_SAFE_INTEGER`.
pub(crate) const MAX_SAFE_INTEGER: f64 = 9007199254740991.0;

/// A JSON number holding value: an integer within the safe range as one,
/// anything else as a float. JavaScript holds both alike.
pub(crate) fn number_value(value: f64) -> Value {
    if value.fract() == 0.0 && value.abs() <= MAX_SAFE_INTEGER {
        return Value::from(value as i64);
    }
    Number::from_f64(value).map_or(Value::Null, Value::Number)
}

#[cfg(test)]
mod tests {
    use serde_json::json;

    use super::*;

    #[test]
    fn stringify_writes_as_javascript_does() {
        let value: Value = serde_json::from_str(
            r#"{"a": {}, "b": 1.0, "10": [true, null, 1e21], "2": "q\"\u0001<\u2028"}"#,
        )
        .unwrap();
        assert_eq!(
            stringify(&value),
            "{\"2\":\"q\\\"\\u0001<\u{2028}\",\"10\":[true,null,1e+21],\"a\":{},\"b\":1}"
        );
        assert_eq!(
            stringify(&json!({"lat": 1.5, "lon": -0.0})),
            r#"{"lat":1.5,"lon":0}"#
        );
    }

    #[test]
    fn number_reads_as_javascript_reads_it() {
        assert_eq!(js_string_to_number(" 12 "), 12.0);
        assert_eq!(js_string_to_number(""), 0.0);
        assert_eq!(js_string_to_number("1.5e3"), 1500.0);
        assert_eq!(js_string_to_number(".5"), 0.5);
        assert_eq!(js_string_to_number("5."), 5.0);
        assert_eq!(js_string_to_number("0x1F"), 31.0);
        assert_eq!(js_string_to_number("-Infinity"), f64::NEG_INFINITY);
        for refused in ["abc", "1e", "--1", "0x", "inf", "nan", "1_000", "+0x1", "."] {
            assert!(js_string_to_number(refused).is_nan(), "{refused}");
        }
    }

    #[test]
    fn number_value_keeps_safe_integers_integral() {
        assert_eq!(number_value(5.0), json!(5));
        assert_eq!(number_value(-0.0), json!(0));
        assert_eq!(number_value(1.5), json!(1.5));
        assert!(number_value(1e300).is_f64());
    }
}
