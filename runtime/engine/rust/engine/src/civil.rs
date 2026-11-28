//! The proleptic Gregorian calendar of days counted from 1970-01-01, and an
//! instant as JavaScript's `toISOString` writes it: the one copy the
//! engine's behaviors, its test kit and the work queue's behaviors share.
//! (The behaviors' core, D103, keeps its own in `superschematic_rules::civil`,
//! which the engine does not depend on.)
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

/// An instant in epoch milliseconds as `toISOString` writes it:
/// `YYYY-MM-DDTHH:MM:SS.mmmZ`, a year outside 0 to 9999 as a sign and six
/// digits. Past 8.64e15 ms either side of the epoch, where `toISOString`
/// throws, it writes the date the same way.
pub fn iso(ms: i64) -> String {
    let (year, month, day) = from_days(ms.div_euclid(DAY_MS));
    let of_day = ms.rem_euclid(DAY_MS);
    let year = if (0..=9999).contains(&year) {
        format!("{year:04}")
    } else {
        format!("{}{:06}", if year < 0 { '-' } else { '+' }, year.abs())
    };
    format!(
        "{year}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_day_is_its_gregorian_date_and_back() {
        for (days, date) in [
            (0, (1970, 1, 1)),
            (-1, (1969, 12, 31)),
            (11_016, (2000, 2, 29)),
            (47_541, (2100, 3, 1)),
            // Year 0, a leap year, and the year before it.
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
    }

    #[test]
    fn an_instant_reads_as_javascripts_to_iso_string_writes_it() {
        // Each recorded from Node.js's new Date(ms).toISOString().
        for (ms, text) in [
            (0, "1970-01-01T00:00:00.000Z"),
            (1_000_000, "1970-01-01T00:16:40.000Z"),
            (-1, "1969-12-31T23:59:59.999Z"),
            (1_791_536_400_000, "2026-10-09T09:00:00.000Z"),
            (951_782_400_000, "2000-02-29T00:00:00.000Z"),
            (-62_167_219_200_000, "0000-01-01T00:00:00.000Z"),
            (253_402_300_800_000, "+010000-01-01T00:00:00.000Z"),
            (-62_198_755_200_000, "-000001-01-01T00:00:00.000Z"),
        ] {
            assert_eq!(iso(ms), text, "{ms}");
        }
    }
}
