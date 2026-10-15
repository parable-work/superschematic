//! Instants as the work-queue behaviors' refusals write them: JavaScript's
//! `Date.prototype.toISOString`, which the TypeScript package's messages
//! read (`new Date(ms).toISOString()`), so a refusal reads the same on
//! either engine (D58).

/// An instant in epoch milliseconds as `toISOString` writes it:
/// `YYYY-MM-DDTHH:MM:SS.mmmZ`, a year outside 0 to 9999 as a sign and six
/// digits.
pub(crate) fn iso(ms: i64) -> String {
    let days = ms.div_euclid(86_400_000);
    let of_day = ms.rem_euclid(86_400_000);
    // Howard Hinnant's civil_from_days.
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = yoe + era * 400 + i64::from(month <= 2);
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
    fn an_instant_reads_as_javascripts_to_iso_string_writes_it() {
        // Each recorded from Node.js's new Date(ms).toISOString().
        for (ms, text) in [
            (0, "1970-01-01T00:00:00.000Z"),
            (1_000_000, "1970-01-01T00:16:40.000Z"),
            (1_060_000, "1970-01-01T00:17:40.000Z"),
            (-1, "1969-12-31T23:59:59.999Z"),
            (1_791_536_400_000, "2026-10-09T09:00:00.000Z"),
            (951_782_400_000, "2000-02-29T00:00:00.000Z"),
            (253_402_300_800_000, "+010000-01-01T00:00:00.000Z"),
            (-62_198_755_200_000, "-000001-01-01T00:00:00.000Z"),
        ] {
            assert_eq!(iso(ms), text, "{ms}");
        }
    }
}
