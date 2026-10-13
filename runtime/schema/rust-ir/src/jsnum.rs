//! Numbers written as JavaScript writes them.
//!
//! The TypeScript runtime and loader hold every number as a float64 and
//! write it with `String(value)`: the shortest decimal that reads back, in
//! exponent form below 1e-6 and from 1e21. Messages that name a bound and
//! the loader's float fields need the same text, which Rust's `Display`
//! does not give (it never writes an exponent).

/// The text JavaScript's `String(value)` gives a number. Zero of either
/// sign is `0`.
pub(crate) fn js_number(value: f64) -> String {
    if value.is_nan() {
        return "NaN".to_owned();
    }
    if value.is_infinite() {
        return if value > 0.0 { "Infinity" } else { "-Infinity" }.to_owned();
    }
    if value == 0.0 {
        return "0".to_owned();
    }
    // `{:e}` writes the shortest digits that read back, as d.ddde±x.
    let scientific = format!("{:e}", value.abs());
    let (mantissa, exponent) = scientific
        .split_once('e')
        .unwrap_or((scientific.as_str(), "0"));
    let digits: String = mantissa.chars().filter(|c| *c != '.').collect();
    let exponent: i64 = exponent.parse().unwrap_or(0);
    let k = digits.len() as i64;
    // The decimal point sits after n digits (ECMAScript Number::toString).
    let n = exponent + 1;
    let mut out = String::with_capacity(digits.len() + 8);
    if value < 0.0 {
        out.push('-');
    }
    if k <= n && n <= 21 {
        out.push_str(&digits);
        out.extend(std::iter::repeat_n('0', (n - k) as usize));
    } else if 0 < n && n <= 21 {
        let (whole, fraction) = digits.split_at(n as usize);
        out.push_str(whole);
        out.push('.');
        out.push_str(fraction);
    } else if -6 < n && n <= 0 {
        out.push_str("0.");
        out.extend(std::iter::repeat_n('0', (-n) as usize));
        out.push_str(&digits);
    } else {
        let (first, rest) = digits.split_at(1);
        out.push_str(first);
        if !rest.is_empty() {
            out.push('.');
            out.push_str(rest);
        }
        out.push('e');
        out.push(if n > 0 { '+' } else { '-' });
        out.push_str(&(n - 1).abs().to_string());
    }
    out
}

#[cfg(test)]
mod tests {
    use super::js_number;

    #[test]
    fn writes_numbers_as_javascript_does() {
        let cases: [(f64, &str); 18] = [
            (0.0, "0"),
            (-0.0, "0"),
            (1.0, "1"),
            (-1.5, "-1.5"),
            (0.1, "0.1"),
            (100.0, "100"),
            (123456789.0, "123456789"),
            (9007199254740991.0, "9007199254740991"),
            (1e21, "1e+21"),
            (1.5e21, "1.5e+21"),
            (123456789012345680000.0, "123456789012345680000"),
            (1e-6, "0.000001"),
            (1e-7, "1e-7"),
            (-1.5e-7, "-1.5e-7"),
            (0.000123, "0.000123"),
            (1.7976931348623157e308, "1.7976931348623157e+308"),
            (5e-324, "5e-324"),
            (12345678901234567890123.0, "1.2345678901234568e+22"),
        ];
        for (value, want) in cases {
            assert_eq!(js_number(value), want, "{value:?}");
        }
    }
}
