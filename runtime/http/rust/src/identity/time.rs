//! Timestamps as the identity runtime writes and reads them: RFC 3339 in
//! UTC on the wire, as Go's `time.Time` encodes one, and text to the
//! millisecond in a SQLite database, as the DDL's
//! `strftime('%Y-%m-%dT%H:%M:%fZ', 'now')` writes one.

use std::time::{Duration, SystemTime, UNIX_EPOCH};

const NANOS_PER_SECOND: i128 = 1_000_000_000;
const SECONDS_PER_DAY: i64 = 86_400;

/// Nanoseconds since the Unix epoch, negative before it.
fn unix_nanos(t: SystemTime) -> i128 {
    match t.duration_since(UNIX_EPOCH) {
        Ok(after) => i128::try_from(after.as_nanos()).unwrap_or(i128::MAX),
        Err(before) => -i128::try_from(before.duration().as_nanos()).unwrap_or(i128::MAX),
    }
}

fn from_unix_nanos(nanos: i128) -> Option<SystemTime> {
    let magnitude = u64::try_from(nanos.unsigned_abs() / 1_000_000_000).ok()?;
    let rest = u32::try_from(nanos.unsigned_abs() % 1_000_000_000).ok()?;
    let duration = Duration::new(magnitude, rest);
    if nanos >= 0 {
        UNIX_EPOCH.checked_add(duration)
    } else {
        UNIX_EPOCH.checked_sub(duration)
    }
}

/// The civil date of a day count since 1970-01-01, after Howard Hinnant's
/// `civil_from_days`.
fn civil_from_days(days: i64) -> (i64, u32, u32) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    (
        year,
        u32::try_from(month).unwrap_or(1),
        u32::try_from(day).unwrap_or(1),
    )
}

/// The day count since 1970-01-01 of a civil date, after Howard Hinnant's
/// `days_from_civil`.
fn days_from_civil(year: i64, month: u32, day: u32) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let yoe = year.rem_euclid(400);
    let month = i64::from(month);
    let mp = if month > 2 { month - 3 } else { month + 9 };
    let doy = (153 * mp + 2) / 5 + i64::from(day) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// The date and time of `t` in UTC, and its nanoseconds.
fn parts(t: SystemTime) -> ((i64, u32, u32), (i64, i64, i64), i128) {
    let nanos = unix_nanos(t);
    let seconds = i64::try_from(nanos.div_euclid(NANOS_PER_SECOND)).unwrap_or(0);
    let fraction = nanos.rem_euclid(NANOS_PER_SECOND);
    let days = seconds.div_euclid(SECONDS_PER_DAY);
    let of_day = seconds.rem_euclid(SECONDS_PER_DAY);
    (
        civil_from_days(days),
        (of_day / 3600, of_day % 3600 / 60, of_day % 60),
        fraction,
    )
}

/// `t` in RFC 3339 in UTC with as many fractional digits as it needs, as
/// Go's `time.Time` encodes a UTC time in JSON: `2026-10-22T12:00:00Z`,
/// `2026-10-22T12:00:00.5Z`.
pub fn format_rfc3339(t: SystemTime) -> String {
    let ((year, month, day), (hour, minute, second), fraction) = parts(t);
    let mut out = format!("{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}");
    if fraction > 0 {
        let digits = format!("{fraction:09}");
        out.push('.');
        out.push_str(digits.trim_end_matches('0'));
    }
    out.push('Z');
    out
}

/// `t` as a SQLite store writes it: UTC to the millisecond, truncated,
/// `YYYY-MM-DDTHH:MM:SS.sssZ`, so every value has one width and compares as
/// text in time order.
pub fn format_sqlite(t: SystemTime) -> String {
    let ((year, month, day), (hour, minute, second), fraction) = parts(t);
    let millis = fraction / 1_000_000;
    format!("{year:04}-{month:02}-{day:02}T{hour:02}:{minute:02}:{second:02}.{millis:03}Z")
}

/// Reads a timestamp in any form a database returns one as text: RFC 3339
/// with `T` or a space between the date and the time, a fraction of up to
/// nine digits, and `Z`, an offset, or none (UTC).
pub fn parse_timestamp(text: &str) -> Option<SystemTime> {
    let b = text.as_bytes();
    if b.len() < 19 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't' | b' ') {
        return None;
    }
    if b[13] != b':' || b[16] != b':' {
        return None;
    }
    let number = |range: std::ops::Range<usize>| -> Option<i64> {
        let digits = text.get(range)?;
        digits
            .bytes()
            .all(|c| c.is_ascii_digit())
            .then(|| digits.parse().ok())
            .flatten()
    };
    let year = number(0..4)?;
    let month = u32::try_from(number(5..7)?).ok()?;
    let day = u32::try_from(number(8..10)?).ok()?;
    let (hour, minute, second) = (number(11..13)?, number(14..16)?, number(17..19)?);
    if !(1..=12).contains(&month)
        || !(1..=31).contains(&day)
        || hour > 23
        || minute > 59
        || second > 60
    {
        return None;
    }
    let mut rest = &text[19..];
    let mut fraction: i128 = 0;
    if let Some(after) = rest.strip_prefix('.') {
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 || digits > 9 {
            return None;
        }
        let padded = format!("{:0<9}", &after[..digits]);
        fraction = padded.parse().ok()?;
        rest = &after[digits..];
    }
    let offset_seconds: i64 = match rest {
        "" | "Z" | "z" => 0,
        _ => {
            let sign = match rest.as_bytes()[0] {
                b'+' => 1,
                b'-' => -1,
                _ => return None,
            };
            let offset = &rest[1..];
            let (h, m) = match offset.len() {
                2 => (offset, "00"),
                4 => (&offset[..2], &offset[2..]),
                5 if offset.as_bytes()[2] == b':' => (&offset[..2], &offset[3..]),
                _ => return None,
            };
            let h: i64 = h.parse().ok()?;
            let m: i64 = m.parse().ok()?;
            sign * (h * 3600 + m * 60)
        }
    };
    let seconds =
        days_from_civil(year, month, day) * SECONDS_PER_DAY + hour * 3600 + minute * 60 + second
            - offset_seconds;
    from_unix_nanos(i128::from(seconds) * NANOS_PER_SECOND + fraction)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_time_round_trips_through_each_form() {
        let t = UNIX_EPOCH + Duration::new(1_791_460_800, 123_456_789);
        assert_eq!(format_rfc3339(t), "2026-10-08T12:00:00.123456789Z");
        assert_eq!(format_sqlite(t), "2026-10-08T12:00:00.123Z");
        assert_eq!(parse_timestamp("2026-10-08T12:00:00.123456789Z"), Some(t));
        assert_eq!(
            parse_timestamp("2026-10-08 14:00:00.123456789+02:00"),
            Some(t)
        );
        assert_eq!(
            format_rfc3339(UNIX_EPOCH + Duration::from_secs(1_791_460_800)),
            "2026-10-08T12:00:00Z"
        );
        assert_eq!(
            format_rfc3339(UNIX_EPOCH + Duration::from_millis(1_791_460_800_500)),
            "2026-10-08T12:00:00.5Z"
        );
        assert_eq!(format_rfc3339(UNIX_EPOCH), "1970-01-01T00:00:00Z");
        assert_eq!(
            parse_timestamp("2026-10-08T12:00:00"),
            Some(UNIX_EPOCH + Duration::from_secs(1_791_460_800))
        );
        assert_eq!(parse_timestamp("2026-10-08"), None);
        assert_eq!(parse_timestamp("2026-13-08T12:00:00Z"), None);
    }
}
