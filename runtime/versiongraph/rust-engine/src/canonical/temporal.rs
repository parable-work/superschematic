//! The UUID, date-time, date, time and duration rules.

use std::sync::LazyLock;

use regex::Regex;
use serde_json::Value;

use super::describe;

static DATE_TIME_PATTERN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(
        r"^([0-9]{4})-([0-9]{2})-([0-9]{2})[T ]([0-9]{2}):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?(Z|[+-][0-9]{2}(?::[0-9]{2}(?::[0-9]{2})?)?)$",
    )
    .expect("date-time pattern")
});
static DATE_PATTERN: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^([0-9]{4})-([0-9]{2})-([0-9]{2})$").expect("date pattern"));
static TIME_PATTERN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^([0-9]{2}):([0-9]{2})(?::([0-9]{2})(?:\.([0-9]{1,9}))?)?$").expect("time pattern")
});
// `\s` is RE2's, as the Go rule reads it: tab, newline, form feed, carriage
// return and space.
static CLOCK_PATTERN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^([0-9]{1,2}):([0-9]{2})(?::([0-9]{2}))?[\t\n\x0C\r ]?([AaPp])[Mm]$")
        .expect("clock pattern")
});
static HYPHENATED_UUID: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$")
        .expect("uuid pattern")
});
static BASE62_UUID: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[0-9A-Za-z]{1,22}$").expect("base62 pattern"));
static INTERVAL_TIME: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^([+-]?)([0-9]+):([0-9]{2}):([0-9]{2})(?:\.([0-9]{1,9}))?$")
        .expect("interval time pattern")
});
static INTERVAL_COUNT: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^[+-]?[0-9]+$").expect("interval count pattern"));

const BASE62_ALPHABET: &[u8; 62] =
    b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

const SECOND: i64 = 1_000_000_000;
const DAY_SECONDS: i64 = 24 * 3600;

fn string_of(value: &Value) -> Result<&str, String> {
    match value {
        Value::String(s) => Ok(s),
        _ => Err(format!("{} is not a string", describe(value))),
    }
}

/// Digits a pattern has already matched.
fn digits(text: &str) -> i64 {
    text.parse().unwrap_or(0)
}

/// The 128 bits of a UUID given hyphenated, in either case, or in base62.
pub(super) fn uuid_bits(s: &str) -> Result<u128, String> {
    if HYPHENATED_UUID.is_match(s) {
        return u128::from_str_radix(&s.replace('-', ""), 16).map_err(|e| e.to_string());
    }
    if !BASE62_UUID.is_match(s) {
        return Err(format!("{s:?} is not a UUID"));
    }
    let mut n: u128 = 0;
    for c in s.bytes() {
        let digit = BASE62_ALPHABET
            .iter()
            .position(|&a| a == c)
            .expect("the pattern admits only base62 digits") as u128;
        n = n
            .checked_mul(62)
            .and_then(|n| n.checked_add(digit))
            .ok_or_else(|| format!("{s:?} is wider than a UUID"))?;
    }
    Ok(n)
}

/// Writes a UUID in the scalar core's canonical form, base62 of its 128 bits
/// (the nil UUID is `"0"`). It reads the hyphenated form Postgres renders,
/// in either case, and the base62 form.
pub(super) fn uuid_rule(value: &Value) -> Result<String, String> {
    let mut n = uuid_bits(string_of(value)?)?;
    if n == 0 {
        return Ok("\"0\"".to_owned());
    }
    let mut out = Vec::new();
    while n > 0 {
        out.push(BASE62_ALPHABET[(n % 62) as usize]);
        n /= 62;
    }
    out.reverse();
    Ok(format!(
        "\"{}\"",
        String::from_utf8(out).expect("base62 digits are ASCII")
    ))
}

fn is_leap(year: i64) -> bool {
    (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
}

fn days_in_month(year: i64, month: i64) -> i64 {
    match month {
        1 | 3 | 5 | 7 | 8 | 10 | 12 => 31,
        4 | 6 | 9 | 11 => 30,
        2 if is_leap(year) => 29,
        2 => 28,
        _ => 0,
    }
}

fn valid_date(year: i64, month: i64, day: i64) -> bool {
    (1..=12).contains(&month) && day >= 1 && day <= days_in_month(year, month)
}

/// Days since 1970-01-01 of a proleptic Gregorian date.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let y = if month <= 2 { year - 1 } else { year };
    let era = y.div_euclid(400);
    let yoe = y - era * 400;
    let mp = (month + 9) % 12;
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// The proleptic Gregorian date of a day count since 1970-01-01.
fn civil_from_days(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

/// Writes an instant as RFC 3339 in UTC with a Z, its fraction of a second
/// without trailing zeros and left out when zero: Go's `RFC3339Nano` of the
/// UTC time. It reads the form Postgres renders, whose offset follows the
/// session's time zone and may carry seconds (`+00:17:30`), and any RFC 3339
/// time with an offset. A year outside 0000-9999, BC, infinity, a time
/// without an offset and an offset of a day or more are refused.
pub(super) fn date_time_rule(value: &Value) -> Result<String, String> {
    let s = string_of(value)?;
    let Some(m) = DATE_TIME_PATTERN.captures(s) else {
        return Err(format!("{s:?} is not a date-time with an offset"));
    };
    let offset = offset_seconds(&m[8]).map_err(|e| format!("{s:?}: {e}"))?;
    let (year, month, day) = (digits(&m[1]), digits(&m[2]), digits(&m[3]));
    let (hour, minute, second) = (digits(&m[4]), digits(&m[5]), digits(&m[6]));
    let fraction = m.get(7).map_or("", |f| f.as_str());
    let nanos = digits(&format!("{fraction}000000000")[..9]);
    if !valid_date(year, month, day) || hour > 23 || minute > 59 || second > 59 {
        return Err(format!("{s:?} is not a valid date-time"));
    }
    let seconds =
        days_from_civil(year, month, day) * DAY_SECONDS + hour * 3600 + minute * 60 + second
            - offset;
    let (days, of_day) = (
        seconds.div_euclid(DAY_SECONDS),
        seconds.rem_euclid(DAY_SECONDS),
    );
    let (year, month, day) = civil_from_days(days);
    if !(0..=9999).contains(&year) {
        return Err(format!("{s:?} falls outside the years 0000-9999"));
    }
    let mut out = format!(
        "\"{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}",
        of_day / 3600,
        of_day / 60 % 60,
        of_day % 60
    );
    let fraction = format!("{nanos:09}");
    let fraction = fraction.trim_end_matches('0');
    if !fraction.is_empty() {
        out.push('.');
        out.push_str(fraction);
    }
    out.push_str("Z\"");
    Ok(out)
}

fn offset_seconds(text: &str) -> Result<i64, String> {
    if text == "Z" {
        return Ok(0);
    }
    let sign = if text.starts_with('-') { -1 } else { 1 };
    let parts: Vec<&str> = text[1..].split(':').collect();
    let mut seconds = digits(parts[0]) * 3600;
    if let Some(minutes) = parts.get(1) {
        seconds += digits(minutes) * 60;
    }
    if let Some(secs) = parts.get(2) {
        seconds += digits(secs);
    }
    if seconds >= DAY_SECONDS {
        return Err(format!("offset {text} is a day or more"));
    }
    Ok(sign * seconds)
}

/// Writes a calendar date as `YYYY-MM-DD`, the form Postgres renders. BC,
/// infinity and a date that does not exist are refused.
pub(super) fn date_rule(value: &Value) -> Result<String, String> {
    let s = string_of(value)?;
    let Some(m) = DATE_PATTERN.captures(s) else {
        return Err(format!("{s:?} is not a date"));
    };
    if !valid_date(digits(&m[1]), digits(&m[2]), digits(&m[3])) {
        return Err(format!("{s:?} is not a valid date"));
    }
    Ok(format!("\"{s}\""))
}

/// Writes a time of day as `HH:MM:SS` on a 24-hour clock, its fraction of a
/// second without trailing zeros and left out when zero, the form Postgres
/// renders. It also reads the other forms the scalar accepts: `HH:MM`, and a
/// 12-hour clock (`2:30 pm`, `12:05:09AM`), each as Postgres reads it into a
/// time. `24:00:00`, which Postgres stores and the scalar refuses, is
/// refused.
pub(super) fn time_rule(value: &Value) -> Result<String, String> {
    let s = string_of(value)?;
    let (mut hour, minute, second, fraction);
    if let Some(m) = TIME_PATTERN.captures(s) {
        hour = digits(&m[1]);
        minute = digits(&m[2]);
        second = m.get(3).map_or(0, |v| digits(v.as_str()));
        fraction = m.get(4).map_or("", |v| v.as_str());
    } else if let Some(m) = CLOCK_PATTERN
        .captures(s)
        .filter(|m| (1..=12).contains(&digits(&m[1])))
    {
        hour = digits(&m[1]) % 12;
        minute = digits(&m[2]);
        second = m.get(3).map_or(0, |v| digits(v.as_str()));
        fraction = "";
        if matches!(&m[4], "p" | "P") {
            hour += 12;
        }
    } else {
        return Err(format!("{s:?} is not a time of day"));
    }
    if hour > 23 || minute > 59 || second > 59 {
        return Err(format!("{s:?} is not a time of day"));
    }
    let mut out = format!("\"{hour:02}:{minute:02}:{second:02}");
    let fraction = fraction.trim_end_matches('0');
    if !fraction.is_empty() {
        out.push('.');
        out.push_str(fraction);
    }
    out.push('"');
    Ok(out)
}

/// Writes a duration in the scalar core's canonical form: under a second,
/// the largest of ms, us and ns that holds it whole (`500ms`, `1500us`);
/// from a second, hours and minutes when present, then seconds with their
/// fraction (`1h30m0s`, `1m30s`, `1.5s`); `0s` for zero. It reads the
/// interval text Postgres renders with `IntervalStyle` postgres, the default
/// (`01:30:00`, `1 day 02:00:00`, `-1 days +02:00:00`), where a day is 24
/// hours, and a duration string as Go's `time.ParseDuration` reads it
/// (`1h30m0s`, `1.5ms`). Months and years, which have no fixed length, are
/// refused.
pub(super) fn duration_rule(value: &Value) -> Result<String, String> {
    let s = string_of(value)?;
    let nanos = match interval_nanos(s) {
        Ok(nanos) => nanos,
        Err(interval_error) => parse_go_duration(s).map_err(|_| interval_error)?,
    };
    Ok(format!("\"{}\"", format_duration(nanos)))
}

/// Reads Postgres interval text in its postgres style.
fn interval_nanos(s: &str) -> Result<i64, String> {
    let fields: Vec<&str> = s.split_whitespace().collect();
    if fields.is_empty() {
        return Err(format!("{s:?} is not a duration"));
    }
    let too_long = || format!("{s:?} is too long a duration");
    let mut total: i128 = 0;
    let mut i = 0;
    while i < fields.len() {
        let field = fields[i];
        if let Some(m) = INTERVAL_TIME.captures(field) {
            let hours: i128 = m[2].parse().map_err(|_| too_long())?;
            let minutes = i128::from(digits(&m[3]));
            let seconds = i128::from(digits(&m[4]));
            let fraction = m.get(5).map_or("", |f| f.as_str());
            let nanos = i128::from(digits(&format!("{fraction}000000000")[..9]));
            let mut part = hours
                .checked_mul(3600)
                .and_then(|h| h.checked_add(minutes * 60 + seconds))
                .and_then(|s| s.checked_mul(i128::from(SECOND)))
                .and_then(|n| n.checked_add(nanos))
                .ok_or_else(too_long)?;
            if &m[1] == "-" {
                part = -part;
            }
            total = total.checked_add(part).ok_or_else(too_long)?;
            i += 1;
            continue;
        }
        if !INTERVAL_COUNT.is_match(field) || i + 1 == fields.len() {
            return Err(format!("{s:?} is not a Postgres interval"));
        }
        let count: i128 = field
            .trim_start_matches('+')
            .parse()
            .map_err(|_| too_long())?;
        i += 1;
        match fields[i] {
            "day" | "days" => {
                let part = count
                    .checked_mul(i128::from(DAY_SECONDS) * i128::from(SECOND))
                    .ok_or_else(too_long)?;
                total = total.checked_add(part).ok_or_else(too_long)?;
            }
            "mon" | "mons" | "year" | "years" => {
                return Err(format!(
                    "{s:?} has months or years, which have no fixed length"
                ))
            }
            _ => return Err(format!("{s:?} is not a Postgres interval")),
        }
        i += 1;
    }
    i64::try_from(total).map_err(|_| too_long())
}

/// The scalar core's canonical duration text.
pub(crate) fn format_duration(nanos: i64) -> String {
    if nanos == 0 {
        return "0s".to_owned();
    }
    let sign = if nanos < 0 { "-" } else { "" };
    let mut remaining = nanos.unsigned_abs();
    const MICROSECOND: u64 = 1_000;
    const MILLISECOND: u64 = 1_000_000;
    const SEC: u64 = 1_000_000_000;
    const MINUTE: u64 = 60 * SEC;
    const HOUR: u64 = 60 * MINUTE;
    if remaining < SEC {
        if remaining.is_multiple_of(MILLISECOND) {
            return format!("{sign}{}ms", remaining / MILLISECOND);
        }
        if remaining.is_multiple_of(MICROSECOND) {
            return format!("{sign}{}us", remaining / MICROSECOND);
        }
        return format!("{sign}{remaining}ns");
    }
    let hours = remaining / HOUR;
    remaining %= HOUR;
    let minutes = remaining / MINUTE;
    remaining %= MINUTE;
    let mut seconds = (remaining / SEC).to_string();
    let fraction = remaining % SEC;
    if fraction != 0 {
        seconds.push('.');
        seconds.push_str(format!("{fraction:09}").trim_end_matches('0'));
    }
    let mut out = sign.to_owned();
    if hours > 0 {
        out.push_str(&format!("{hours}h"));
    }
    if hours > 0 || minutes > 0 {
        out.push_str(&format!("{minutes}m"));
    }
    out.push_str(&seconds);
    out.push('s');
    out
}

/// Go's `time.ParseDuration`: `[-+]?([0-9]*(\.[0-9]*)?[a-z]+)+`, with the
/// units ns, us (or µs, μs), ms, s, m and h, and `0` alone for zero.
pub(super) fn parse_go_duration(orig: &str) -> Result<i64, String> {
    const LIMIT: u64 = 1 << 63;
    let invalid = || format!("time: invalid duration {orig:?}");
    let mut s = orig;
    let mut negative = false;
    if let Some(rest) = s.strip_prefix('-') {
        negative = true;
        s = rest;
    } else if let Some(rest) = s.strip_prefix('+') {
        s = rest;
    }
    if s == "0" {
        return Ok(0);
    }
    if s.is_empty() {
        return Err(invalid());
    }
    let mut d: u64 = 0;
    while !s.is_empty() {
        let first = s.as_bytes()[0];
        if !(first == b'.' || first.is_ascii_digit()) {
            return Err(invalid());
        }
        // The integer before the decimal point.
        let before = s.len();
        let mut v: u64 = 0;
        let mut i = 0;
        for &c in s.as_bytes() {
            if !c.is_ascii_digit() {
                break;
            }
            if v > LIMIT / 10 {
                return Err(invalid());
            }
            v = v * 10 + u64::from(c - b'0');
            if v > LIMIT {
                return Err(invalid());
            }
            i += 1;
        }
        s = &s[i..];
        let pre = before != s.len();
        // The fraction after it.
        let mut f: u64 = 0;
        let mut scale = 1.0_f64;
        let mut post = false;
        if let Some(rest) = s.strip_prefix('.') {
            s = rest;
            let before = s.len();
            let mut overflow = false;
            let mut i = 0;
            for &c in s.as_bytes() {
                if !c.is_ascii_digit() {
                    break;
                }
                i += 1;
                if overflow {
                    continue;
                }
                if f > (LIMIT - 1) / 10 {
                    overflow = true;
                    continue;
                }
                let y = f * 10 + u64::from(c - b'0');
                if y > LIMIT {
                    overflow = true;
                    continue;
                }
                f = y;
                scale *= 10.0;
            }
            s = &s[i..];
            post = before != s.len();
        }
        if !pre && !post {
            return Err(invalid());
        }
        // The unit.
        let unit_len = s
            .bytes()
            .position(|c| c == b'.' || c.is_ascii_digit())
            .unwrap_or(s.len());
        if unit_len == 0 {
            return Err(format!("time: missing unit in duration {orig:?}"));
        }
        let (unit_text, rest) = s.split_at(unit_len);
        s = rest;
        let unit: u64 = match unit_text {
            "ns" => 1,
            "us" | "\u{b5}s" | "\u{3bc}s" => 1_000,
            "ms" => 1_000_000,
            "s" => 1_000_000_000,
            "m" => 60_000_000_000,
            "h" => 3_600_000_000_000,
            _ => {
                return Err(format!(
                    "time: unknown unit {unit_text:?} in duration {orig:?}"
                ))
            }
        };
        if v > LIMIT / unit {
            return Err(invalid());
        }
        v *= unit;
        if f > 0 {
            // A float is nanosecond-accurate for a fraction of an hour, the
            // largest unit.
            v += (f as f64 * (unit as f64 / scale)) as u64;
            if v > LIMIT {
                return Err(invalid());
            }
        }
        d += v;
        if d > LIMIT {
            return Err(invalid());
        }
    }
    if negative {
        return Ok((d as i64).wrapping_neg());
    }
    if d > LIMIT - 1 {
        return Err(invalid());
    }
    Ok(d as i64)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_durations_parse_as_go_reads_them() {
        for (text, nanos) in [
            ("0", 0),
            ("1h30m0s", 5_400_000_000_000),
            ("1.5ms", 1_500_000),
            ("-1m30.5s", -90_500_000_000),
            ("+2us", 2_000),
            ("1\u{b5}s", 1_000),
            (".5s", 500_000_000),
            ("2562047h47m16.854775807s", i64::MAX),
            ("-2562047h47m16.854775808s", i64::MIN),
        ] {
            assert_eq!(parse_go_duration(text), Ok(nanos), "{text}");
        }
        for text in [
            "",
            "-",
            "1",
            "s",
            "1x",
            ".s",
            "1..5s",
            "2562047h47m16.854775808s",
            "1d",
        ] {
            assert!(parse_go_duration(text).is_err(), "{text:?}");
        }
    }

    #[test]
    fn durations_format_in_the_scalar_core_form() {
        for (nanos, text) in [
            (0, "0s"),
            (500_000_000, "500ms"),
            (1_500_000, "1500us"),
            (1_500, "1500ns"),
            (5_400_000_000_000, "1h30m0s"),
            (90_000_000_000, "1m30s"),
            (1_500_000_000, "1.5s"),
            (-90_500_000_000, "-1m30.5s"),
            (i64::MIN, "-2562047h47m16.854775808s"),
        ] {
            assert_eq!(format_duration(nanos), text, "{nanos}");
        }
    }

    #[test]
    fn civil_days_round_trip() {
        for (y, m, d) in [
            (1970, 1, 1),
            (0, 1, 1),
            (9999, 12, 31),
            (2024, 2, 29),
            (1890, 1, 1),
        ] {
            assert_eq!(civil_from_days(days_from_civil(y, m, d)), (y, m, d));
        }
        assert_eq!(days_from_civil(1970, 1, 1), 0);
        assert_eq!(days_from_civil(2000, 3, 1), 11_017);
    }
}
