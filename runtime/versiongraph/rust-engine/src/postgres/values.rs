//! The forms a statement writes values in.

use serde_json::Value;

use crate::canonical;
use crate::Error;

/// The hyphenated form Postgres reads of a UUID given in its canonical form,
/// or hyphenated.
pub(super) fn uuid_text(id: &str) -> Result<String, Error> {
    Ok(canonical::uuid_hyphenated(id)?)
}

/// `uuid_text`, with `""` for `None`.
pub(super) fn optional_uuid_text(id: Option<&str>) -> Result<String, Error> {
    id.map_or(Ok(String::new()), uuid_text)
}

/// The canonical form of a UUID Postgres rendered as text.
pub(super) fn canonical_uuid(text: &str) -> Result<String, Error> {
    Ok(canonical::uuid(text)?)
}

/// `canonical_uuid`, with `None` for `""`.
pub(super) fn optional_canonical_uuid(text: &str) -> Result<Option<String>, Error> {
    if text.is_empty() {
        return Ok(None);
    }
    canonical_uuid(text).map(Some)
}

/// Turns a canonical value of `class` into the JSON `jsonb_populate_record`
/// reads into the column: a UUID, or a list of them, hyphenated, and a
/// duration, or a list of them, as interval text. A json column and a list
/// of lists are JSONB and keep the canonical JSON, which is the schema
/// runtime's; every other class is already what Postgres reads.
pub(super) fn input_value(class: &str, value: &Value) -> Result<Value, Error> {
    let normalized = canonical::postgres_value(class, value)?;
    let normalized: Value = serde_json::from_str(&normalized)
        .map_err(|e| Error::Invalid(format!("reread a canonical value: {e}")))?;
    if class.ends_with("[][]") {
        return Ok(normalized);
    }
    let (element, list) = match class.strip_suffix("[]") {
        Some(element) => (element, true),
        None => (class, false),
    };
    let convert: fn(&str) -> Result<String, Error> = match element {
        canonical::UUID => uuid_text,
        canonical::DURATION => interval_text,
        _ => return Ok(normalized),
    };
    match normalized {
        Value::Null => Ok(Value::Null),
        Value::String(s) if !list => Ok(Value::String(convert(&s)?)),
        Value::Array(items) if list => items
            .into_iter()
            .map(|item| match item {
                Value::String(s) => convert(&s).map(Value::String),
                other => Err(Error::Invalid(format!("{other} is not a {element}"))),
            })
            .collect::<Result<Vec<_>, _>>()
            .map(Value::Array),
        other => Err(Error::Invalid(format!("{other} is not a {class}"))),
    }
}

/// Writes a canonical duration as interval text Postgres reads exactly:
/// `[-]H:MM:SS` with the fraction of a second.
pub(super) fn interval_text(duration: &str) -> Result<String, Error> {
    let nanos = canonical::parse_duration(duration)?;
    let sign = if nanos < 0 { "-" } else { "" };
    let nanos = nanos.unsigned_abs();
    let (seconds, fraction) = (nanos / 1_000_000_000, nanos % 1_000_000_000);
    let mut text = format!(
        "{sign}{:02}:{:02}:{:02}",
        seconds / 3600,
        seconds / 60 % 60,
        seconds % 60
    );
    if fraction > 0 {
        text.push('.');
        text.push_str(format!("{fraction:09}").trim_end_matches('0'));
    }
    Ok(text)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_write_as_interval_text() {
        for (duration, text) in [
            ("0s", "00:00:00"),
            ("1h30m0s", "01:30:00"),
            ("-1m30.5s", "-00:01:30.5"),
            ("1500us", "00:00:00.0015"),
            ("30h0m0s", "30:00:00"),
        ] {
            assert_eq!(interval_text(duration).expect(duration), text);
        }
    }

    #[test]
    fn input_values_are_what_postgres_reads() {
        let cases = [
            (
                "uuid",
                serde_json::json!("2tLrGjz6ktIRCukXDsqykS"),
                serde_json::json!("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"),
            ),
            (
                "uuid[]",
                serde_json::json!(["0"]),
                serde_json::json!(["00000000-0000-0000-0000-000000000000"]),
            ),
            (
                "duration",
                serde_json::json!("1h30m0s"),
                serde_json::json!("01:30:00"),
            ),
            (
                "duration[]",
                serde_json::json!(["1.5s"]),
                serde_json::json!(["00:00:01.5"]),
            ),
            ("uuid", Value::Null, Value::Null),
            (
                "time",
                serde_json::json!("2:30 pm"),
                serde_json::json!("14:30:00"),
            ),
            (
                "uuid[][]",
                serde_json::json!([["0"]]),
                serde_json::json!([["0"]]),
            ),
        ];
        for (class, value, want) in cases {
            assert_eq!(input_value(class, &value).expect(class), want, "{class}");
        }
    }
}
