//! A duration's text, as `Temporal.Duration` holds it, read and written in
//! the scalar core's canonical form, which the Postgres codec and the
//! SQLite client share.

/// The nanoseconds in a unit of a duration's text, the scalar core's
/// units, the longer first where one starts another.
const DURATION_UNITS: [(&str, i128); 8] = [
    ("ns", 1),
    ("us", 1_000),
    ("\u{00b5}s", 1_000),
    ("\u{03bc}s", 1_000),
    ("ms", 1_000_000),
    ("s", 1_000_000_000),
    ("m", 60_000_000_000),
    ("h", 3_600_000_000_000),
];

/// A duration's text, as `Temporal.Duration` holds it (`1h30m0s`, `-1.5s`,
/// `90m`, `250us`), as its nanoseconds: a sign for the whole, then one or
/// more numbers, each with its unit, or a bare `0`.
pub(crate) fn parse_duration(text: &str) -> Result<i128, String> {
    let refused = || format!("{text:?} is no duration");
    let value = text.trim();
    let (sign, mut rest) = match value.as_bytes().first() {
        Some(b'-') => (-1, &value[1..]),
        Some(b'+') => (1, &value[1..]),
        _ => (1, value),
    };
    if rest == "0" {
        return Ok(0);
    }
    if rest.is_empty() {
        return Err(refused());
    }
    let mut nanos: i128 = 0;
    while !rest.is_empty() {
        let digits = rest
            .find(|c: char| !c.is_ascii_digit() && c != '.')
            .unwrap_or(rest.len());
        let number = &rest[..digits];
        let (whole, fraction) = number.split_once('.').unwrap_or((number, ""));
        if (whole.is_empty() && fraction.is_empty()) || fraction.contains('.') {
            return Err(refused());
        }
        rest = &rest[digits..];
        let (unit, scale) = DURATION_UNITS
            .iter()
            .find(|(unit, _)| rest.starts_with(unit))
            .ok_or_else(refused)?;
        rest = &rest[unit.len()..];
        let parse = |digits: &str| -> Result<i128, String> {
            if digits.is_empty() {
                Ok(0)
            } else {
                digits.parse::<i128>().map_err(|_| refused())
            }
        };
        let mut part = parse(whole)?.checked_mul(*scale).ok_or_else(refused)?;
        if !fraction.is_empty() {
            let places = u32::try_from(fraction.len()).map_err(|_| refused())?;
            let denominator = 10_i128.checked_pow(places).ok_or_else(refused)?;
            part += parse(fraction)?.checked_mul(*scale).ok_or_else(refused)? / denominator;
        }
        nanos = nanos.checked_add(part).ok_or_else(refused)?;
    }
    Ok(sign * nanos)
}

/// Nanoseconds as a duration's text in the scalar core's canonical form:
/// under a second, the largest of `ms`, `us` and `ns` that holds it whole
/// (`500ms`, `250us`); from a second, hours and minutes when there are any,
/// then seconds with their fraction (`1h30m0s`, `1m30s`, `1.5s`); `0s` for
/// zero.
pub(crate) fn format_duration(nanos: i128) -> String {
    const SECOND: u128 = 1_000_000_000;
    if nanos == 0 {
        return "0s".to_string();
    }
    let sign = if nanos < 0 { "-" } else { "" };
    let mut rest = nanos.unsigned_abs();
    if rest < SECOND {
        return if rest.is_multiple_of(1_000_000) {
            format!("{sign}{}ms", rest / 1_000_000)
        } else if rest.is_multiple_of(1_000) {
            format!("{sign}{}us", rest / 1_000)
        } else {
            format!("{sign}{rest}ns")
        };
    }
    let hours = rest / (3600 * SECOND);
    rest %= 3600 * SECOND;
    let minutes = rest / (60 * SECOND);
    rest %= 60 * SECOND;
    let mut seconds = (rest / SECOND).to_string();
    let fraction = rest % SECOND;
    if fraction != 0 {
        let digits = format!("{fraction:09}");
        seconds.push('.');
        seconds.push_str(digits.trim_end_matches('0'));
    }
    if hours > 0 {
        format!("{sign}{hours}h{minutes}m{seconds}s")
    } else if minutes > 0 {
        format!("{sign}{minutes}m{seconds}s")
    } else {
        format!("{sign}{seconds}s")
    }
}
