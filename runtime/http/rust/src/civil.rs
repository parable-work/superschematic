//! The proleptic Gregorian date of a day since the Unix epoch, for the times
//! the log writes and the dates and timestamps a queue's claim reads (D65).

/// The year, month (1 to 12) and day (1 to 31) of `days` since 1970-01-01:
/// Howard Hinnant's `civil_from_days`.
pub(crate) fn from_days(days: i64) -> (i64, i64, i64) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

/// The days since 1970-01-01 of a proleptic Gregorian date: Howard
/// Hinnant's `days_from_civil`.
#[cfg(any(feature = "gcs", feature = "sqlite"))]
pub(crate) fn to_days(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let yoe = year.rem_euclid(400);
    let mp = if month > 2 { month - 3 } else { month + 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

/// `t` as a V4 signature's `X-Goog-Date` writes it, in UTC to the second:
/// `20310301T123456Z`. A time before the epoch is the epoch.
#[cfg(feature = "gcs")]
pub(crate) fn basic_timestamp(t: std::time::SystemTime) -> String {
    let seconds = t.duration_since(std::time::UNIX_EPOCH).map_or(0, |since| {
        i64::try_from(since.as_secs()).unwrap_or(i64::MAX)
    });
    let (year, month, day) = from_days(seconds.div_euclid(86_400));
    let of_day = seconds.rem_euclid(86_400);
    format!(
        "{year:04}{month:02}{day:02}T{:02}{:02}{:02}Z",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60
    )
}

/// Reads an RFC 3339 timestamp, as Cloud Storage writes an object's
/// `updated`: `2026-10-19T12:34:56.789Z`, with a fraction of up to nine
/// digits, and `Z` or an offset. `None` for anything else.
#[cfg(feature = "gcs")]
pub(crate) fn parse_rfc3339(text: &str) -> Option<std::time::SystemTime> {
    use std::time::{Duration, UNIX_EPOCH};
    let b = text.as_bytes();
    if b.len() < 20 || b[4] != b'-' || b[7] != b'-' || !matches!(b[10], b'T' | b't') {
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
    let (year, month, day) = (number(0..4)?, number(5..7)?, number(8..10)?);
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
    let mut nanos: u32 = 0;
    if let Some(after) = rest.strip_prefix('.') {
        let digits = after.bytes().take_while(u8::is_ascii_digit).count();
        if digits == 0 || digits > 9 {
            return None;
        }
        nanos = format!("{:0<9}", &after[..digits]).parse().ok()?;
        rest = &after[digits..];
    }
    let offset = match rest.as_bytes() {
        [b'Z' | b'z'] => 0,
        [sign @ (b'+' | b'-'), h1, h2, b':', m1, m2] => {
            let digits = [*h1, *h2, *m1, *m2];
            if !digits.iter().all(u8::is_ascii_digit) {
                return None;
            }
            let value = |pair: &[u8]| i64::from(pair[0] - b'0') * 10 + i64::from(pair[1] - b'0');
            let minutes = value(&digits[..2]) * 60 + value(&digits[2..]);
            if *sign == b'+' {
                minutes * 60
            } else {
                -minutes * 60
            }
        }
        _ => return None,
    };
    let seconds = to_days(year, month, day) * 86_400 + hour * 3600 + minute * 60 + second - offset;
    let seconds = u64::try_from(seconds).ok()?;
    UNIX_EPOCH.checked_add(Duration::new(seconds, nanos))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_day_is_its_gregorian_date() {
        assert_eq!(from_days(0), (1970, 1, 1));
        assert_eq!(from_days(10_957), (2000, 1, 1));
        assert_eq!(from_days(11_016), (2000, 2, 29));
        assert_eq!(from_days(-1), (1969, 12, 31));
        assert_eq!(from_days(20_740), (2026, 10, 14));
    }

    #[cfg(feature = "gcs")]
    #[test]
    fn a_gcs_timestamp_reads_and_writes() {
        use std::time::{Duration, UNIX_EPOCH};
        for days in [-1, 0, 10_957, 11_016, 20_740, 22_339] {
            let (year, month, day) = from_days(days);
            assert_eq!(to_days(year, month, day), days);
        }
        let at = UNIX_EPOCH + Duration::from_secs(1_930_134_896);
        assert_eq!(basic_timestamp(at), "20310301T123456Z");
        assert_eq!(parse_rfc3339("2031-03-01T12:34:56Z"), Some(at));
        assert_eq!(
            parse_rfc3339("2031-03-01T12:34:56.25Z"),
            Some(at + Duration::from_millis(250))
        );
        assert_eq!(parse_rfc3339("2031-03-01T14:34:56+02:00"), Some(at));
        assert_eq!(parse_rfc3339("2031-03-01T11:04:56-01:30"), Some(at));
        for bad in [
            "",
            "2031-03-01",
            "2031-03-01T12:34:56",
            "2031-13-01T12:34:56Z",
            "2031-03-01T12:34:56.Z",
            "2031-03-01T12:34:56+0200",
        ] {
            assert_eq!(parse_rfc3339(bad), None, "{bad}");
        }
    }
}
