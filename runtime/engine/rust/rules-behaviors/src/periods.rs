//! The periods a Budget meter starts again at (D104), in UTC: the hour, the
//! day, the week from each Monday 00:00 (ISO 8601's), the calendar month
//! from each first 00:00, or each multiple of a length from the epoch; and a
//! rolling window's buckets, a sixtieth of its period each. Every time is
//! epoch milliseconds; the calendar is the proleptic Gregorian one, as
//! JavaScript's `Date` reads it.

/// A minute, in milliseconds: `{ everyMs }` is a whole number of them.
pub(crate) const MINUTE_MS: i64 = 60_000;
pub(crate) const HOUR_MS: i64 = 3_600_000;
pub(crate) const DAY_MS: i64 = 86_400_000;
pub(crate) const WEEK_MS: i64 = 7 * DAY_MS;

/// A rolling month's length: thirty days, since a calendar month has no one
/// length.
pub(crate) const MONTH_WINDOW_MS: i64 = 30 * DAY_MS;

// The epoch fell on a Thursday: the first Monday after it is four days on.
const FIRST_MONDAY_MS: i64 = 4 * DAY_MS;

/// The start of the length-long period an instant falls in, the periods
/// starting at each multiple of `length` from `origin`.
pub(crate) fn start_of(at: i64, length: i64, origin: i64) -> i64 {
    (at - origin).div_euclid(length) * length + origin
}

/// The start of the hour an instant falls in.
pub(crate) fn hour_start(at: i64) -> i64 {
    start_of(at, HOUR_MS, 0)
}

/// The start of the UTC day an instant falls in.
pub(crate) fn day_start(at: i64) -> i64 {
    start_of(at, DAY_MS, 0)
}

/// The start of the week an instant falls in: its Monday, 00:00 UTC.
pub(crate) fn week_start(at: i64) -> i64 {
    start_of(at, WEEK_MS, FIRST_MONDAY_MS)
}

/// The start of the calendar month an instant falls in: its first, 00:00
/// UTC.
pub(crate) fn month_start(at: i64) -> i64 {
    let (year, month, _) = civil_from_days(at.div_euclid(DAY_MS));
    days_from_civil(year, month, 1) * DAY_MS
}

/// The start of the calendar month after the one an instant falls in.
pub(crate) fn next_month_start(at: i64) -> i64 {
    let (year, month, _) = civil_from_days(at.div_euclid(DAY_MS));
    let (year, month) = if month == 12 {
        (year + 1, 1)
    } else {
        (year, month + 1)
    };
    days_from_civil(year, month, 1) * DAY_MS
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
    let year = year_of_era + era * 400;
    (if month <= 2 { year + 1 } else { year }, month, day)
}

// days_from_civil is the count of days from the epoch to a date of the
// proleptic Gregorian calendar, the inverse of civil_from_days.
fn days_from_civil(year: i64, month: i64, day: i64) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let of_era = year - era * 400;
    let of_year = (153 * (if month > 2 { month - 3 } else { month + 9 }) + 2) / 5 + day - 1;
    let of_era_days = of_era * 365 + of_era / 4 - of_era / 100 + of_year;
    era * 146_097 + of_era_days - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;

    // At 2026-10-09T09:00:00.000Z, a Friday.
    const FRIDAY: i64 = 1_791_536_400_000;

    #[test]
    fn a_period_starts_at_its_hour_day_monday_and_first_in_utc() {
        assert_eq!(hour_start(FRIDAY + 59 * MINUTE_MS), FRIDAY);
        assert_eq!(hour_start(FRIDAY + HOUR_MS), FRIDAY + HOUR_MS);
        assert_eq!(day_start(FRIDAY), FRIDAY - 9 * HOUR_MS);
        // Monday 2026-10-05 and Monday 2026-10-12.
        assert_eq!(week_start(FRIDAY), 1_791_158_400_000);
        assert_eq!(week_start(1_791_763_200_000 - 1), 1_791_158_400_000);
        assert_eq!(week_start(1_791_763_200_000), 1_791_763_200_000);
        // 2026-10-01 and 2026-11-01.
        assert_eq!(month_start(FRIDAY), 1_790_812_800_000);
        assert_eq!(next_month_start(FRIDAY), 1_793_491_200_000);
        // The epoch's own week started the Monday before it.
        assert_eq!(week_start(0), -3 * DAY_MS);
        assert_eq!(week_start(-1), -3 * DAY_MS);
        // December rolls into January.
        assert_eq!(next_month_start(1_798_761_600_000 - 1), 1_798_761_600_000);
    }

    #[test]
    fn a_month_has_its_calendars_length() {
        // 2028-02 has 29 days, 2026-02 has 28.
        let february_2028 = days_from_civil(2028, 2, 1) * DAY_MS;
        assert_eq!(next_month_start(february_2028) - february_2028, 29 * DAY_MS);
        let february_2026 = days_from_civil(2026, 2, 1) * DAY_MS;
        assert_eq!(next_month_start(february_2026) - february_2026, 28 * DAY_MS);
        for days in [-800_000, -1, 0, 1, 20_000, 2_000_000] {
            let (year, month, day) = civil_from_days(days);
            assert_eq!(days_from_civil(year, month, day), days, "{days}");
        }
    }
}
