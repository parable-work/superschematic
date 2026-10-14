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
}
