//! How the behaviors that hold a row's own fields compare their values
//! (D16, D104): two values as the engine's `jsonEqual` compares them, with
//! absent and null one value, none, since a merge patch removes a member
//! with null, and two whole numbers by their digits; whether a field holds
//! a value; the order a field that moves one way is held to, in which no
//! value is below every value; and a date-time's epoch milliseconds as the
//! ISO 8601 text a refusal names it by.

use std::cmp::Ordering;

use serde_json::{Map, Value};

use crate::constants::OneWayKind;
use crate::timers::parse_date_time;

/// Whether two values that may be absent are one value, as the engine's
/// `js_equal` compares them: absent and null are one value, none; objects
/// by member, arrays in order, numbers by value, anything else as JSON.
/// Two whole numbers compare by their digits, so a `Generic.Int64` past
/// 2^53, which an adapter gives whole, is not one value with its neighbour
/// that rounds to the same double.
pub(crate) fn equal(a: Option<&Value>, b: Option<&Value>) -> bool {
    match (present(a), present(b)) {
        (None, None) => true,
        (Some(a), Some(b)) => json_equal(a, b),
        _ => false,
    }
}

/// Whether an own field holds a value: neither absent nor null. `""` and
/// `[]` are held.
pub(crate) fn held(own: &Map<String, Value>, field: &str) -> bool {
    own.get(field).is_some_and(|value| !value.is_null())
}

/// How two values of a one-way field compare: Less, Equal or Greater, no
/// value below every value; None when either is a value the kind cannot read.
pub(crate) fn one_way_order(
    kind: OneWayKind,
    a: Option<&Value>,
    b: Option<&Value>,
) -> Option<Ordering> {
    match (present(a), present(b)) {
        (None, None) => Some(Ordering::Equal),
        (None, Some(b)) => read(kind, b).map(|_| Ordering::Less),
        (Some(a), None) => read(kind, a).map(|_| Ordering::Greater),
        (Some(a), Some(b)) => read(kind, a)?.compare(&read(kind, b)?),
    }
}

// present is a value that is there: an absent one and a null are none.
fn present(value: Option<&Value>) -> Option<&Value> {
    value.filter(|value| !value.is_null())
}

fn json_equal(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => {
            match (whole(&x.to_string()), whole(&y.to_string())) {
                (Some(x), Some(y)) => x == y,
                _ => match (x.as_f64(), y.as_f64()) {
                    (Some(x), Some(y)) => x == y,
                    _ => x == y,
                },
            }
        }
        (Value::Array(x), Value::Array(y)) => {
            x.len() == y.len() && x.iter().zip(y).all(|(x, y)| json_equal(x, y))
        }
        (Value::Object(x), Value::Object(y)) => {
            x.len() == y.len()
                && x.iter()
                    .all(|(key, value)| y.get(key).is_some_and(|other| json_equal(value, other)))
        }
        _ => a == b,
    }
}

// A value of a one-way field as its kind orders it.
enum Ordered {
    Boolean(bool),
    Whole(i128),
    Number(f64),
}

impl Ordered {
    fn compare(&self, other: &Ordered) -> Option<Ordering> {
        match (self, other) {
            (Ordered::Boolean(a), Ordered::Boolean(b)) => Some(a.cmp(b)),
            (Ordered::Whole(a), Ordered::Whole(b)) => Some(a.cmp(b)),
            (a, b) => a.number()?.partial_cmp(&b.number()?),
        }
    }

    fn number(&self) -> Option<f64> {
        match self {
            Ordered::Boolean(_) => None,
            Ordered::Whole(n) => Some(*n as f64),
            Ordered::Number(n) => Some(*n),
        }
    }
}

// read reads a value as its kind orders it: a boolean (or 0 and 1, as an
// adapter may read one), a number (or a string that holds a decimal
// number, as an adapter may read a BIGINT), or a date-time's epoch
// milliseconds (a whole number, or ISO 8601 text as Timers reads it).
fn read(kind: OneWayKind, value: &Value) -> Option<Ordered> {
    match kind {
        OneWayKind::Boolean => match value {
            Value::Bool(flag) => Some(Ordered::Boolean(*flag)),
            Value::Number(number) => match whole(&number.to_string())? {
                0 => Some(Ordered::Boolean(false)),
                1 => Some(Ordered::Boolean(true)),
                _ => None,
            },
            _ => None,
        },
        OneWayKind::Number => match value {
            Value::Number(number) => numeric(&number.to_string()),
            Value::String(text) if is_decimal(text) => numeric(text),
            _ => None,
        },
        OneWayKind::DateTime => match value {
            Value::Number(number) => whole(&number.to_string()).map(Ordered::Whole),
            Value::String(text) => parse_date_time(text).map(|at| Ordered::Whole(i128::from(at))),
            _ => None,
        },
    }
}

/// The ISO 8601 text of a time in epoch milliseconds, as JavaScript's
/// `toISOString` writes it (`2026-02-01T00:00:00.000Z`, a year past 9999 or
/// before 0 as a signed six-digit one), the form a table's API gives a
/// date-time in; `None` past 8.64e15 ms either side of the epoch, where
/// `toISOString` throws.
pub(crate) fn date_time_text(ms: i64) -> Option<String> {
    if ms.unsigned_abs() > 8_640_000_000_000_000 {
        return None;
    }
    let (year, month, day) = civil_from_days(ms.div_euclid(86_400_000));
    let of_day = ms.rem_euclid(86_400_000);
    let year = match year {
        0..=9999 => format!("{year:04}"),
        _ if year < 0 => format!("-{:06}", -year),
        _ => format!("+{year:06}"),
    };
    Some(format!(
        "{year}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    ))
}

// civil_from_days is the year, month and day of the proleptic Gregorian
// calendar a count of days from the epoch falls on (Howard Hinnant's
// algorithm), the inverse of Timers' days_from_civil.
fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let days = days + 719_468;
    let era = days.div_euclid(146_097);
    let of_era = days - era * 146_097;
    let year_of_era = (of_era - of_era / 1460 + of_era / 36_524 - of_era / 146_096) / 365;
    let of_year = of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let shifted = (5 * of_year + 2) / 153;
    let day = of_year - (153 * shifted + 2) / 5 + 1;
    let month = if shifted < 10 {
        shifted + 3
    } else {
        shifted - 9
    };
    let year = year_of_era + era * 400;
    (if month <= 2 { year + 1 } else { year }, month, day)
}

// numeric reads a number's text: a whole number as one, any other as the
// double nearest it.
fn numeric(text: &str) -> Option<Ordered> {
    if let Some(n) = whole(text) {
        return Some(Ordered::Whole(n));
    }
    text.parse::<f64>().ok().map(Ordered::Number)
}

// whole reads text that is a whole number, digits with an optional minus.
fn whole(text: &str) -> Option<i128> {
    let digits = text.strip_prefix('-').unwrap_or(text);
    if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    text.parse().ok()
}

// is_decimal says whether text is `-?[0-9]+(\.[0-9]+)?`.
fn is_decimal(text: &str) -> bool {
    let digits = text.strip_prefix('-').unwrap_or(text);
    let (whole, fraction) = match digits.split_once('.') {
        Some((whole, fraction)) => (whole, Some(fraction)),
        None => (digits, None),
    };
    let all = |part: &str| !part.is_empty() && part.bytes().all(|b| b.is_ascii_digit());
    all(whole) && fraction.is_none_or(all)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn absent_and_null_are_one_value() {
        assert!(equal(None, Some(&Value::Null)));
        assert!(equal(Some(&json!(5)), Some(&json!(5.0))));
        assert!(equal(
            Some(&json!({"a": [1, 2]})),
            Some(&json!({"a": [1.0, 2]}))
        ));
        assert!(!equal(
            Some(&json!({"a": [1, 2]})),
            Some(&json!({"a": [2, 1]}))
        ));
        assert!(!equal(Some(&json!("")), None));
        assert!(!equal(
            Some(&json!(9_007_199_254_740_993_i64)),
            Some(&json!(9_007_199_254_740_992_i64))
        ));
        assert!(equal(
            Some(&json!(9_007_199_254_740_993_i64)),
            Some(&json!(9_007_199_254_740_993_u64))
        ));
        let own = json!({"a": "", "b": [], "c": 0, "d": null});
        let own = own.as_object().expect("an object");
        for field in ["a", "b", "c"] {
            assert!(held(own, field), "{field}");
        }
        assert!(!held(own, "d") && !held(own, "e"));
    }

    #[test]
    fn a_date_time_is_written_as_javascripts_to_iso_string_writes_it() {
        // Each recorded from Node.js's Date.prototype.toISOString.
        for (ms, text) in [
            (0, Some("1970-01-01T00:00:00.000Z")),
            (1_767_225_600_000, Some("2026-01-01T00:00:00.000Z")),
            (1_769_904_000_123, Some("2026-02-01T00:00:00.123Z")),
            (951_782_400_000, Some("2000-02-29T00:00:00.000Z")),
            (-1, Some("1969-12-31T23:59:59.999Z")),
            (-62_198_755_200_000, Some("-000001-01-01T00:00:00.000Z")),
            (253_402_300_800_000, Some("+010000-01-01T00:00:00.000Z")),
            (8_640_000_000_000_000, Some("+275760-09-13T00:00:00.000Z")),
            (8_640_000_000_000_001, None),
        ] {
            assert_eq!(date_time_text(ms).as_deref(), text, "{ms}");
            if let Some(text) = text {
                assert_eq!(crate::timers::parse_date_time(text), Some(ms), "{text}");
            }
        }
    }

    #[test]
    fn no_value_is_below_every_value() {
        let order = |kind, a: Value, b: Value| one_way_order(kind, Some(&a), Some(&b));
        assert_eq!(
            order(OneWayKind::Boolean, json!(false), json!(true)),
            Some(Ordering::Less)
        );
        assert_eq!(
            order(OneWayKind::Boolean, json!(1), json!(false)),
            Some(Ordering::Greater)
        );
        assert_eq!(
            order(OneWayKind::Boolean, json!(null), json!(false)),
            Some(Ordering::Less)
        );
        assert_eq!(
            order(OneWayKind::Boolean, json!(true), json!(null)),
            Some(Ordering::Greater)
        );
        assert_eq!(
            order(OneWayKind::Boolean, json!(null), json!(null)),
            Some(Ordering::Equal)
        );
        assert_eq!(order(OneWayKind::Boolean, json!(2), json!(true)), None);
        assert_eq!(
            order(OneWayKind::Number, json!(2), json!(1.5)),
            Some(Ordering::Greater)
        );
        assert_eq!(
            order(OneWayKind::Number, json!(2), json!(2.0)),
            Some(Ordering::Equal)
        );
        assert_eq!(
            order(
                OneWayKind::Number,
                json!("9007199254740993"),
                json!(9_007_199_254_740_992_i64)
            ),
            Some(Ordering::Greater)
        );
        assert_eq!(
            order(OneWayKind::Number, json!("-1.5"), json!(-1)),
            Some(Ordering::Less)
        );
        assert_eq!(order(OneWayKind::Number, json!("1e3"), json!(1)), None);
        assert_eq!(order(OneWayKind::Number, json!(true), json!(1)), None);
        assert_eq!(
            order(
                OneWayKind::DateTime,
                json!("2026-01-01T00:00:00Z"),
                json!(1_767_225_600_001_i64)
            ),
            Some(Ordering::Less)
        );
        assert_eq!(order(OneWayKind::DateTime, json!(1.5), json!(2)), None);
        assert_eq!(order(OneWayKind::DateTime, json!("soon"), json!(2)), None);
        assert_eq!(
            one_way_order(OneWayKind::Number, None, Some(&json!("x"))),
            None
        );
    }
}
