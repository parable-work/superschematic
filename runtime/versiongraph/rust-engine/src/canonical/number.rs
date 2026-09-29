//! The integer and number rules.

use std::sync::LazyLock;

use regex::Regex;
use serde_json::Value;

use super::describe;

static INTEGER_PATTERN: LazyLock<Regex> =
    LazyLock::new(|| Regex::new(r"^-?(0|[1-9][0-9]*)$").expect("integer pattern"));
static NUMBER_PATTERN: LazyLock<Regex> = LazyLock::new(|| {
    Regex::new(r"^(-?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$")
        .expect("number pattern")
});

/// Keeps an integer's digits exactly, however wide: an optional minus and no
/// leading zeros. A fraction or an exponent is refused. -0 needs no rule:
/// `serde_json` reads it as the integer 0, which writes `0`.
pub(super) fn integer_rule(value: &Value) -> Result<String, String> {
    let Value::Number(n) = value else {
        return Err(format!("{} is not an integer", describe(value)));
    };
    let text = n.to_string();
    if !INTEGER_PATTERN.is_match(&text) {
        return Err(format!("{} is not an integer", describe(value)));
    }
    Ok(text)
}

/// Writes a number's exact decimal value in the layout ECMAScript's
/// `Number::toString` uses: plain digits while the decimal point falls
/// within 21 digits of the first and no more than 6 zeros follow it, else
/// one digit, a fraction and an exponent with its sign (`1e+21`, `1.5e-7`).
/// Trailing zeros go (`1.50` is `1.5`), and -0 is 0; the digits are never
/// rounded.
pub(super) fn number_rule(value: &Value) -> Result<String, String> {
    let Value::Number(n) = value else {
        return Err(format!("{} is not a number", describe(value)));
    };
    let text = n.to_string();
    let Some(m) = NUMBER_PATTERN.captures(&text) else {
        return Err(format!("{text:?} is not a JSON number"));
    };
    let negative = &m[1] == "-";
    let whole = &m[2];
    let fraction = m.get(3).map_or("", |f| f.as_str());
    let exponent: i64 = match m.get(4) {
        None => 0,
        Some(e) => e
            .as_str()
            .parse::<i32>()
            .map_err(|_| format!("the exponent of {text} is out of range"))?
            .into(),
    };
    // The value is 0.digits * 10^point.
    let digits = format!("{whole}{fraction}");
    let mut point = whole.len() as i64 + exponent;
    let trimmed = digits.trim_start_matches('0');
    point -= (digits.len() - trimmed.len()) as i64;
    let digits = trimmed.trim_end_matches('0');
    if digits.is_empty() {
        return Ok("0".to_owned());
    }
    let sign = if negative { "-" } else { "" };
    let k = digits.len() as i64;
    if k <= point && point <= 21 {
        return Ok(format!(
            "{sign}{digits}{}",
            "0".repeat((point - k) as usize)
        ));
    }
    if 0 < point && point <= 21 {
        let (before, after) = digits.split_at(point as usize);
        return Ok(format!("{sign}{before}.{after}"));
    }
    if -6 < point && point <= 0 {
        return Ok(format!("{sign}0.{}{digits}", "0".repeat((-point) as usize)));
    }
    let e = point - 1;
    let exponent_sign = if e < 0 { "-" } else { "+" };
    let mut mantissa = digits[..1].to_owned();
    if k > 1 {
        mantissa.push('.');
        mantissa.push_str(&digits[1..]);
    }
    Ok(format!("{sign}{mantissa}e{exponent_sign}{}", e.abs()))
}
