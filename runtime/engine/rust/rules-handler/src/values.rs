//! How the handler reads and compares the values a row holds: instants,
//! UUIDs and the JSON a filter, a sort, a unique field and a relation
//! compare, as a table's column would.

use std::cmp::Ordering;

use serde_json::Value;

/// An instant's epoch milliseconds from a date-time's text, as JavaScript's
/// `Date.parse` reads it (Timers' reading, D103), or from a whole number of
/// milliseconds; `None` for anything else.
pub fn instant_of(value: &Value) -> Option<i64> {
    match value {
        Value::String(text) => superschematic_rules_behaviors::parse_date_time(text),
        Value::Number(number) => number.as_i64(),
        _ => None,
    }
}

/// The ISO 8601 text of a time in epoch milliseconds, as JavaScript's
/// `toISOString` writes it (`2026-02-01T00:00:00.000Z`, a year past 9999 or
/// before 0 as a signed six-digit one): the form a row holds a date-time
/// in and a table's API gives one in; `None` past 8.64e15 ms either side of
/// the epoch, where `toISOString` throws.
pub fn iso_of(ms: i64) -> Option<String> {
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
// algorithm).
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
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// A UUID's value from its hyphenated form (36 characters, `8-4-4-4-12`
/// hexadecimal digits of either case) or its base62 wire form (1 to 22
/// characters, case-sensitive, at most 2^128 - 1), as the adapters read
/// one; `None` for any other text.
pub fn uuid_of(text: &str) -> Option<u128> {
    if text.len() == 36 {
        let bytes = text.as_bytes();
        let hyphenated = bytes.iter().enumerate().all(|(i, byte)| match i {
            8 | 13 | 18 | 23 => *byte == b'-',
            _ => byte.is_ascii_hexdigit(),
        });
        if !hyphenated {
            return None;
        }
        return u128::from_str_radix(&text.replace('-', ""), 16).ok();
    }
    if text.is_empty() || text.len() > 22 {
        return None;
    }
    text.bytes().try_fold(0u128, |n, byte| {
        let digit = BASE62.iter().position(|c| *c == byte)?;
        n.checked_mul(62)?.checked_add(digit as u128)
    })
}

/// A UUID's value as its base62 text, the wire form (D79): no padding, `0`
/// for zero.
pub fn base62(mut n: u128) -> String {
    let mut digits = Vec::new();
    loop {
        digits.push(BASE62[(n % 62) as usize]);
        n /= 62;
        if n == 0 {
            break;
        }
    }
    digits.reverse();
    String::from_utf8(digits).unwrap_or_default()
}

/// A version 4 UUID's value from 16 bytes a host drew at random: its
/// version and variant bits set.
pub fn uuid_v4(bytes: [u8; 16]) -> u128 {
    let n = u128::from_be_bytes(bytes);
    (n & !(0xF << 76) | (0x4 << 76)) & !(0x3 << 62) | (0x2 << 62)
}

/// Whether two values are one value as a column compares them: numbers by
/// value, anything else by its JSON.
pub fn same(a: &Value, b: &Value) -> bool {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => {
            x == y
                || match (x.as_i64(), y.as_i64()) {
                    (Some(x), Some(y)) => x == y,
                    _ => x.as_f64().is_some() && x.as_f64() == y.as_f64(),
                }
        }
        _ => a == b,
    }
}

/// How two values of one field order, as a column of them sorts: numbers
/// by value, strings by code point, booleans false first, and anything
/// else (which no sort takes) by its JSON text.
pub fn order(a: &Value, b: &Value) -> Ordering {
    match (a, b) {
        (Value::Number(x), Value::Number(y)) => match (x.as_i64(), y.as_i64()) {
            (Some(x), Some(y)) => x.cmp(&y),
            _ => x
                .as_f64()
                .unwrap_or(f64::NAN)
                .total_cmp(&y.as_f64().unwrap_or(f64::NAN)),
        },
        (Value::String(x), Value::String(y)) => x.cmp(y),
        (Value::Bool(x), Value::Bool(y)) => x.cmp(y),
        _ => a.to_string().cmp(&b.to_string()),
    }
}

/// Whether a text matches an `ILIKE` pattern as Postgres reads one: `%` any
/// run of characters, `_` any one, `\` escaping the next, every other
/// character itself in any case.
pub fn ilike(text: &str, pattern: &str) -> bool {
    let text: Vec<char> = text.chars().flat_map(char::to_lowercase).collect();
    let mut parts: Vec<Piece> = Vec::new();
    let mut chars = pattern.chars();
    while let Some(c) = chars.next() {
        match c {
            '%' => parts.push(Piece::Any),
            '_' => parts.push(Piece::One),
            '\\' => {
                if let Some(next) = chars.next() {
                    parts.extend(next.to_lowercase().map(Piece::Char));
                }
            }
            c => parts.extend(c.to_lowercase().map(Piece::Char)),
        }
    }
    matches(&text, &parts)
}

#[derive(Clone, Copy, PartialEq)]
enum Piece {
    Any,
    One,
    Char(char),
}

// matches is whether text matches the pieces, by the classic two-pointer
// walk that backtracks to the last `%`.
fn matches(text: &[char], pieces: &[Piece]) -> bool {
    let (mut t, mut p) = (0, 0);
    let mut star: Option<(usize, usize)> = None;
    while t < text.len() {
        match pieces.get(p) {
            Some(Piece::Any) => {
                star = Some((p, t));
                p += 1;
            }
            Some(Piece::One) => {
                t += 1;
                p += 1;
            }
            Some(Piece::Char(c)) if *c == text[t] => {
                t += 1;
                p += 1;
            }
            _ => match star {
                Some((at, from)) => {
                    p = at + 1;
                    t = from + 1;
                    star = Some((at, from + 1));
                }
                None => return false,
            },
        }
    }
    pieces[p..].iter().all(|piece| *piece == Piece::Any)
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn an_instant_reads_and_writes_as_javascript_does() {
        let ms = instant_of(&json!("2026-02-01T01:30:00+01:00")).expect("an instant");
        assert_eq!(iso_of(ms).as_deref(), Some("2026-02-01T00:30:00.000Z"));
        assert_eq!(instant_of(&json!(1000)), Some(1000));
        assert_eq!(instant_of(&json!("tuesday")), None);
        assert_eq!(iso_of(0).as_deref(), Some("1970-01-01T00:00:00.000Z"));
    }

    #[test]
    fn a_uuid_reads_in_either_form_and_writes_as_base62() {
        let hyphenated = "0190f0e2-7b1c-7d4e-8f00-000000000001";
        let n = uuid_of(hyphenated).expect("a UUID");
        let wire = base62(n);
        assert_eq!(uuid_of(&wire), Some(n));
        assert_eq!(uuid_of(&hyphenated.to_uppercase()), Some(n));
        assert_eq!(uuid_of("not a uuid!"), None);
        let v4 = uuid_v4([0xFF; 16]);
        assert_eq!((v4 >> 76) & 0xF, 4);
        assert_eq!((v4 >> 62) & 0x3, 2);
    }

    #[test]
    fn ilike_matches_as_postgres_does() {
        assert!(ilike("Standing Desk", "%desk%"));
        assert!(ilike("desk", "d_sk"));
        assert!(!ilike("desk", "d_k"));
        assert!(ilike("100%", "100\\%"));
        assert!(!ilike("1000", "100\\%"));
        assert!(ilike("", "%"));
        assert!(ilike("ÉCOLE", "%école%"));
    }

    #[test]
    fn values_order_as_a_column_sorts_them() {
        assert_eq!(order(&json!(2), &json!(10)), Ordering::Less);
        assert_eq!(order(&json!(2.5), &json!(2)), Ordering::Greater);
        assert_eq!(order(&json!("B"), &json!("a")), Ordering::Less);
        assert!(same(&json!(5), &json!(5.0)));
    }
}
