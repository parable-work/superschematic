//! The proleptic Gregorian calendar of days counted from 1970-01-01, and an
//! instant as JavaScript's `toISOString` writes it: the one copy the
//! behaviors, the handler and the adapters share. The core reads no clock;
//! these are pure functions of the numbers they take.
//!
//! Both directions are Howard Hinnant's algorithms over eras of 400 years
//! that start on 1 March, so a leap day is an era's last. They hold for
//! any day before or after the epoch: a year is astronomical (year 0 is
//! 1 BC, -1 is 2 BC), as JavaScript's `Date` counts years.

/// The milliseconds of a day.
const DAY_MS: i64 = 86_400_000;

/// The year, month (1 to 12) and day (1 to 31) a count of days from
/// 1970-01-01 falls on: Howard Hinnant's `civil_from_days`.
pub fn from_days(days: i64) -> (i64, i64, i64) {
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

/// The count of days from 1970-01-01 to a date, its month 1 to 12: Howard
/// Hinnant's `days_from_civil`, the inverse of [`from_days`]. A day past its
/// month's last counts on into the next month.
pub fn to_days(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let yoe = year.rem_euclid(400);
    let mp = if month > 2 { month - 3 } else { month + 9 };
    let doy = (153 * mp + 2) / 5 + day - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
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
    let (year, month, day) = from_days(ms.div_euclid(DAY_MS));
    let of_day = ms.rem_euclid(DAY_MS);
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_day_is_its_gregorian_date_and_back() {
        for (days, date) in [
            (0, (1970, 1, 1)),
            (-1, (1969, 12, 31)),
            (10_957, (2000, 1, 1)),
            (11_016, (2000, 2, 29)),
            (11_017, (2000, 3, 1)),
            (20_740, (2026, 10, 14)),
            (47_541, (2100, 3, 1)),
            // Year 0, a leap year, and the year before it.
            (-719_468, (0, 3, 1)),
            (-719_469, (0, 2, 29)),
            (-719_528, (0, 1, 1)),
            (-719_529, (-1, 12, 31)),
        ] {
            assert_eq!(from_days(days), date, "{days}");
            assert_eq!(to_days(date.0, date.1, date.2), days, "{date:?}");
        }
        for days in [
            -100_000_000,
            -800_000,
            -1,
            0,
            1,
            20_000,
            2_000_000,
            100_000_000,
        ] {
            let (year, month, day) = from_days(days);
            assert_eq!(to_days(year, month, day), days, "{days}");
        }
        // A day past a month's last is the next month's.
        assert_eq!(to_days(2026, 2, 29), to_days(2026, 3, 1));
    }

    #[test]
    fn an_instant_is_written_as_javascripts_to_iso_string_writes_it() {
        // Each recorded from Node.js's Date.prototype.toISOString.
        for (ms, text) in [
            (0, Some("1970-01-01T00:00:00.000Z")),
            (1_000_000, Some("1970-01-01T00:16:40.000Z")),
            (1_767_225_600_000, Some("2026-01-01T00:00:00.000Z")),
            (1_769_904_000_123, Some("2026-02-01T00:00:00.123Z")),
            (951_782_400_000, Some("2000-02-29T00:00:00.000Z")),
            (-1, Some("1969-12-31T23:59:59.999Z")),
            (-62_167_219_200_000, Some("0000-01-01T00:00:00.000Z")),
            (-62_198_755_200_000, Some("-000001-01-01T00:00:00.000Z")),
            (253_402_300_800_000, Some("+010000-01-01T00:00:00.000Z")),
            (8_640_000_000_000_000, Some("+275760-09-13T00:00:00.000Z")),
            (-8_640_000_000_000_000, Some("-271821-04-20T00:00:00.000Z")),
            (8_640_000_000_000_001, None),
            (-8_640_000_000_000_001, None),
        ] {
            assert_eq!(iso_of(ms).as_deref(), text, "{ms}");
        }
    }
}
