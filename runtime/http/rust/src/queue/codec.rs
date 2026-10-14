//! How a message's values cross to Postgres and back, as node-postgres moves
//! them for the TypeScript queue client: each parameter goes as text, which
//! Postgres reads by the type it gives the parameter, and each column of a
//! claimed row comes back as the JSON value a message's type reads.
//!
//! A value goes as its JSON text for a JSON column, as a Postgres array
//! literal for a list, and otherwise as itself: a string as it is, a number
//! or a boolean as JSON writes it. A string for a `UUID` column is read as
//! superscalar's `Identity.UUID` reads one, base62 or hyphenated, since a
//! Rust type's `Identity.UUID` serializes as base62, and goes hyphenated.
//!
//! A column comes back by its type: a boolean, an integer or a float as a
//! JSON number or boolean; a `NUMERIC` as the number its digits write; text
//! (`TEXT`, `VARCHAR`, `CITEXT`, an enum) as a string; a `UUID` hyphenated;
//! a `TIMESTAMPTZ` in RFC 3339 in UTC, as superscalar's `DateTime` writes
//! it (`2026-10-14T12:00:00.5Z`); a `DATE` as `YYYY-MM-DD` and a `TIME` as
//! `HH:MM:SS` with its fraction, as Postgres writes them; an `INET` as
//! Postgres writes it; `JSON` and `JSONB` as the value they hold; and an
//! array as a list of its elements, `NULL` as null. Any other type is
//! refused, naming it.

use std::error::Error;
use std::fmt::Write as _;
use std::net::{Ipv4Addr, Ipv6Addr};

use bytes::BytesMut;
use serde_json::Value;
use tokio_postgres::types::{to_sql_checked, Format, FromSql, IsNull, Kind, ToSql, Type};

use crate::uuid62::parse_uuid;

type BoxError = Box<dyn Error + Sync + Send>;

/// The microseconds from 1970-01-01 to 2000-01-01, Postgres's epoch.
const POSTGRES_EPOCH_MICROS: i64 = 946_684_800_000_000;

/// The days from 1970-01-01 to 2000-01-01.
const POSTGRES_EPOCH_DAYS: i64 = 10_957;

/// A parameter sent as text, which Postgres reads by the type it gives the
/// parameter; `None` is NULL.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Text(pub(crate) Option<String>);

impl Text {
    pub(crate) fn of(value: impl ToString) -> Self {
        Self(Some(value.to_string()))
    }
}

impl ToSql for Text {
    fn to_sql(&self, _ty: &Type, out: &mut BytesMut) -> Result<IsNull, BoxError> {
        match &self.0 {
            None => Ok(IsNull::Yes),
            Some(text) => {
                out.extend_from_slice(text.as_bytes());
                Ok(IsNull::No)
            }
        }
    }

    fn accepts(_ty: &Type) -> bool {
        true
    }

    fn encode_format(&self, _ty: &Type) -> Format {
        Format::Text
    }

    to_sql_checked!();
}

/// Whether a column of `ty` holds JSON.
pub(crate) fn is_json(ty: &Type) -> bool {
    match ty.kind() {
        Kind::Domain(base) => is_json(base),
        _ => *ty == Type::JSON || *ty == Type::JSONB,
    }
}

/// The text of `value` for a parameter of `ty`, or `None` for null: its
/// JSON text when `json` says the column holds JSON, or `ty` is a JSON
/// type.
pub(crate) fn param_text(value: &Value, ty: &Type, json: bool) -> Result<Option<String>, String> {
    if value.is_null() {
        return Ok(None);
    }
    if json || is_json(ty) {
        return serde_json::to_string(value)
            .map(Some)
            .map_err(|err| err.to_string());
    }
    match ty.kind() {
        Kind::Array(element) => array_literal(value, element).map(Some),
        Kind::Domain(base) => param_text(value, base, false),
        _ => Ok(Some(scalar_text(value, ty))),
    }
}

/// A value of a column that is no list and holds no JSON, as text.
fn scalar_text(value: &Value, ty: &Type) -> String {
    match value {
        Value::String(text) if *ty == Type::UUID => {
            parse_uuid(text).map_or_else(|| text.clone(), |uuid| uuid.hyphenated().to_string())
        }
        Value::String(text) => text.clone(),
        // A number, a boolean, or a list or object, which Postgres reads
        // or refuses as its JSON text.
        other => other.to_string(),
    }
}

/// A list as a one-dimensional Postgres array literal of `element`s: each
/// element quoted, a quote or backslash in it escaped, and null `NULL`.
fn array_literal(value: &Value, element: &Type) -> Result<String, String> {
    let Value::Array(items) = value else {
        return Err(format!("{value} is no list, which the column holds"));
    };
    let mut out = String::from("{");
    for (i, item) in items.iter().enumerate() {
        if i > 0 {
            out.push(',');
        }
        let Some(text) = param_text(item, element, false)? else {
            out.push_str("NULL");
            continue;
        };
        out.push('"');
        for c in text.chars() {
            if c == '"' || c == '\\' {
                out.push('\\');
            }
            out.push(c);
        }
        out.push('"');
    }
    out.push('}');
    Ok(out)
}

/// A column of a claimed row as the JSON value it holds; see the module's
/// documentation. A NULL is read as `Option<Column>`'s `None`.
#[derive(Clone, Debug, PartialEq)]
pub(crate) struct Column(pub(crate) Value);

impl<'a> FromSql<'a> for Column {
    fn from_sql(ty: &Type, raw: &'a [u8]) -> Result<Self, BoxError> {
        decode(ty, raw).map(Column)
    }

    fn accepts(_ty: &Type) -> bool {
        true
    }
}

fn decode(ty: &Type, raw: &[u8]) -> Result<Value, BoxError> {
    match ty.kind() {
        Kind::Array(_) => {
            let elements = Vec::<Option<Column>>::from_sql(ty, raw)?;
            return Ok(Value::Array(
                elements
                    .into_iter()
                    .map(|element| element.map_or(Value::Null, |Column(value)| value))
                    .collect(),
            ));
        }
        Kind::Domain(base) => return decode(base, raw),
        Kind::Enum(_) => return Ok(Value::from(std::str::from_utf8(raw)?)),
        _ => {}
    }
    if ty.name() == "citext" {
        return Ok(Value::from(std::str::from_utf8(raw)?));
    }
    if let Some(value) = decode_number(ty, raw)? {
        return Ok(value);
    }
    Ok(match *ty {
        Type::TEXT | Type::VARCHAR | Type::BPCHAR | Type::NAME | Type::UNKNOWN => {
            Value::from(String::from_sql(ty, raw)?)
        }
        Type::UUID => Value::from(uuid::Uuid::from_sql(ty, raw)?.hyphenated().to_string()),
        Type::TIMESTAMPTZ => Value::from(timestamp(i64::from_sql(&Type::INT8, raw)?, true)),
        Type::TIMESTAMP => Value::from(timestamp(i64::from_sql(&Type::INT8, raw)?, false)),
        Type::DATE => Value::from(date(i32::from_sql(&Type::INT4, raw)?)),
        Type::TIME => Value::from(time(i64::from_sql(&Type::INT8, raw)?)),
        Type::JSON => serde_json::from_slice(raw)?,
        Type::JSONB => match raw.split_first() {
            Some((1, text)) => serde_json::from_slice(text)?,
            _ => return Err("a JSONB value of a version other than 1".into()),
        },
        Type::INET | Type::CIDR => Value::from(inet(raw, *ty == Type::CIDR)?),
        _ => {
            return Err(format!(
                "a column of type {}, which the Rust queue client does not read",
                ty.name()
            )
            .into())
        }
    })
}

/// A boolean or a number column as its JSON value, or `None` for a column
/// of another type.
fn decode_number(ty: &Type, raw: &[u8]) -> Result<Option<Value>, BoxError> {
    Ok(Some(match *ty {
        Type::BOOL => Value::from(bool::from_sql(ty, raw)?),
        Type::INT2 => Value::from(i16::from_sql(ty, raw)?),
        Type::INT4 => Value::from(i32::from_sql(ty, raw)?),
        Type::INT8 => Value::from(i64::from_sql(ty, raw)?),
        Type::FLOAT4 => float(f64::from(f32::from_sql(ty, raw)?))?,
        Type::FLOAT8 => float(f64::from_sql(ty, raw)?)?,
        Type::NUMERIC => serde_json::from_str(&numeric(raw)?)
            .map_err(|_| "a NUMERIC that is not a number, which JSON cannot hold")?,
        _ => return Ok(None),
    }))
}

/// A float as a JSON number; JSON has no NaN or infinity.
fn float(value: f64) -> Result<Value, BoxError> {
    serde_json::Number::from_f64(value)
        .map(Value::Number)
        .ok_or_else(|| format!("{value}, which JSON cannot hold").into())
}

/// A `NUMERIC` in its binary form, written as Postgres writes it: its
/// digits, with its scale's places after the point.
pub(crate) fn numeric(raw: &[u8]) -> Result<String, BoxError> {
    let word = |i: usize| -> Result<u16, BoxError> {
        raw.get(i..i + 2)
            .map(|b| u16::from_be_bytes([b[0], b[1]]))
            .ok_or_else(|| "a NUMERIC cut short".into())
    };
    let ndigits = usize::from(word(0)?);
    let weight = i32::from(i16::from_be_bytes(word(2)?.to_be_bytes()));
    let sign = word(4)?;
    let scale = usize::from(word(6)?);
    let negative = match sign {
        0x0000 => false,
        0x4000 => true,
        0xC000 => return Ok("NaN".to_string()),
        0xD000 => return Ok("Infinity".to_string()),
        0xF000 => return Ok("-Infinity".to_string()),
        _ => return Err(format!("a NUMERIC of sign {sign:#x}").into()),
    };
    let mut digits = Vec::with_capacity(ndigits);
    for i in 0..ndigits {
        digits.push(word(8 + 2 * i)?);
    }
    // The base-10000 digit at position i, where 0 is the one the weight
    // counts from; a position outside the digits is zero.
    let digit = |i: i32| {
        usize::try_from(i)
            .ok()
            .and_then(|i| digits.get(i).copied())
            .unwrap_or(0)
    };
    let mut out = String::new();
    if negative {
        out.push('-');
    }
    if weight < 0 {
        out.push('0');
    } else {
        for i in 0..=weight {
            let d = digit(i);
            let _ = if i == 0 {
                write!(out, "{d}")
            } else {
                write!(out, "{d:04}")
            };
        }
    }
    if scale > 0 {
        let mut fraction = String::new();
        let mut i = weight + 1;
        while fraction.len() < scale {
            let _ = write!(fraction, "{:04}", digit(i));
            i += 1;
        }
        fraction.truncate(scale);
        out.push('.');
        out.push_str(&fraction);
    }
    Ok(out)
}

/// A timestamp, microseconds since 2000-01-01, in RFC 3339 as Go's
/// `RFC3339Nano` writes it and superscalar's `DateTime` serializes it:
/// the fraction without its trailing zeros, none when it is zero, and `Z`
/// in UTC when `zone`; a timestamp without a zone has none.
pub(crate) fn timestamp(micros: i64, zone: bool) -> String {
    match micros {
        i64::MAX => return "infinity".to_string(),
        i64::MIN => return "-infinity".to_string(),
        _ => {}
    }
    let unix = micros.saturating_add(POSTGRES_EPOCH_MICROS);
    let seconds = unix.div_euclid(1_000_000);
    let (year, month, day) = crate::civil::from_days(seconds.div_euclid(86_400));
    let of_day = seconds.rem_euclid(86_400);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}{}{}",
        of_day / 3600,
        of_day % 3600 / 60,
        of_day % 60,
        fraction(unix.rem_euclid(1_000_000)),
        if zone { "Z" } else { "" }
    )
}

/// A date, days since 2000-01-01, as `YYYY-MM-DD`.
pub(crate) fn date(days: i32) -> String {
    match days {
        i32::MAX => return "infinity".to_string(),
        i32::MIN => return "-infinity".to_string(),
        _ => {}
    }
    let (year, month, day) = crate::civil::from_days(i64::from(days) + POSTGRES_EPOCH_DAYS);
    format!("{year:04}-{month:02}-{day:02}")
}

/// A time of day, microseconds since midnight, as Postgres writes it:
/// `HH:MM:SS`, and the fraction without its trailing zeros.
pub(crate) fn time(micros: i64) -> String {
    let seconds = micros.div_euclid(1_000_000);
    format!(
        "{:02}:{:02}:{:02}{}",
        seconds / 3600,
        seconds % 3600 / 60,
        seconds % 60,
        fraction(micros.rem_euclid(1_000_000))
    )
}

/// Microseconds as a second's fraction: `.5` for 500000, nothing for 0.
fn fraction(micros: i64) -> String {
    if micros == 0 {
        return String::new();
    }
    let digits = format!(".{micros:06}");
    digits.trim_end_matches('0').to_string()
}

/// An `INET` or `CIDR` in its binary form, written as Postgres writes it:
/// the address, then the prefix length for a network, or a host address
/// with less than every bit.
fn inet(raw: &[u8], cidr: bool) -> Result<String, BoxError> {
    let [family, bits, _, len, address @ ..] = raw else {
        return Err("an INET cut short".into());
    };
    if address.len() != usize::from(*len) {
        return Err("an INET of the wrong length".into());
    }
    let (text, all) = match (family, address) {
        (2, &[a, b, c, d]) => (Ipv4Addr::new(a, b, c, d).to_string(), 32),
        (3, octets) if octets.len() == 16 => {
            let mut bytes = [0; 16];
            bytes.copy_from_slice(octets);
            (Ipv6Addr::from(bytes).to_string(), 128)
        }
        _ => return Err(format!("an INET of family {family}").into()),
    };
    Ok(if cidr || *bits != all {
        format!("{text}/{bits}")
    } else {
        text
    })
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn numeric_bytes(weight: i16, sign: u16, scale: u16, digits: &[u16]) -> Vec<u8> {
        let mut raw = Vec::new();
        raw.extend_from_slice(&u16::try_from(digits.len()).unwrap().to_be_bytes());
        raw.extend_from_slice(&weight.to_be_bytes());
        raw.extend_from_slice(&sign.to_be_bytes());
        raw.extend_from_slice(&scale.to_be_bytes());
        for d in digits {
            raw.extend_from_slice(&d.to_be_bytes());
        }
        raw
    }

    #[test]
    fn a_numeric_is_its_digits_to_its_scale() {
        let cases: [(i16, u16, u16, &[u16], &str); 8] = [
            (0, 0, 0, &[], "0"),
            (0, 0, 2, &[12, 3400], "12.34"),
            (1, 0x4000, 0, &[1, 2345], "-12345"),
            (-1, 0, 7, &[1, 2340], "0.0001234"),
            (-2, 0, 5, &[1000], "0.00001"),
            (2, 0, 1, &[1], "100000000.0"),
            (0, 0xC000, 0, &[], "NaN"),
            (0, 0xD000, 0, &[], "Infinity"),
        ];
        for (weight, sign, scale, digits, want) in cases {
            let raw = numeric_bytes(weight, sign, scale, digits);
            assert_eq!(numeric(&raw).unwrap(), want, "{digits:?} at {weight}");
        }
        assert!(numeric(&[0, 1]).is_err());
    }

    #[test]
    fn a_timestamp_is_rfc_3339_without_trailing_zeros() {
        // 2026-10-14T12:00:00Z, microseconds since 2000-01-01.
        let noon = (20_740 - 10_957) * 86_400 * 1_000_000 + 12 * 3600 * 1_000_000;
        assert_eq!(timestamp(noon, true), "2026-10-14T12:00:00Z");
        assert_eq!(timestamp(noon + 500_000, true), "2026-10-14T12:00:00.5Z");
        assert_eq!(
            timestamp(noon + 123_456, false),
            "2026-10-14T12:00:00.123456"
        );
        assert_eq!(timestamp(-1, true), "1999-12-31T23:59:59.999999Z");
        assert_eq!(timestamp(i64::MAX, true), "infinity");
        assert_eq!(date(0), "2000-01-01");
        assert_eq!(date(-1), "1999-12-31");
        assert_eq!(date(i32::MIN), "-infinity");
        assert_eq!(time(0), "00:00:00");
        assert_eq!(
            time(((13 * 60 + 5) * 60 + 9) * 1_000_000 + 10),
            "13:05:09.00001"
        );
    }

    #[test]
    fn an_inet_is_postgress_text() {
        assert_eq!(
            inet(&[2, 32, 0, 4, 10, 0, 0, 1], false).unwrap(),
            "10.0.0.1"
        );
        assert_eq!(
            inet(&[2, 8, 0, 4, 10, 0, 0, 0], false).unwrap(),
            "10.0.0.0/8"
        );
        assert_eq!(
            inet(&[2, 32, 1, 4, 10, 0, 0, 1], true).unwrap(),
            "10.0.0.1/32"
        );
        let mut v6 = vec![3, 128, 0, 16];
        v6.extend_from_slice(&Ipv6Addr::LOCALHOST.octets());
        assert_eq!(inet(&v6, false).unwrap(), "::1");
        assert!(inet(&[2, 32, 0, 4, 10], false).is_err());
    }

    #[test]
    fn a_parameter_is_its_text_for_its_column() {
        let text = |value: Value, ty: &Type, json: bool| param_text(&value, ty, json).unwrap();
        assert_eq!(text(json!(null), &Type::TEXT, false), None);
        assert_eq!(
            text(json!("a b"), &Type::TEXT, false).as_deref(),
            Some("a b")
        );
        assert_eq!(text(json!(42), &Type::INT8, false).as_deref(), Some("42"));
        assert_eq!(
            text(json!(0.5), &Type::FLOAT8, false).as_deref(),
            Some("0.5")
        );
        assert_eq!(
            text(json!(true), &Type::BOOL, false).as_deref(),
            Some("true")
        );
        assert_eq!(
            text(json!({"a": [1, "x"]}), &Type::JSONB, false).as_deref(),
            Some(r#"{"a":[1,"x"]}"#)
        );
        assert_eq!(
            text(json!("held"), &Type::TEXT, true).as_deref(),
            Some(r#""held""#),
            "a JSON column's string is its JSON text"
        );
        let uuid = uuid::Uuid::parse_str("123e4567-e89b-12d3-a456-426614174000").unwrap();
        let base62 = crate::uuid62::uuid_to_base62(uuid);
        assert_eq!(
            text(json!(base62), &Type::UUID, false).as_deref(),
            Some("123e4567-e89b-12d3-a456-426614174000")
        );
        assert_eq!(
            text(json!("not a uuid"), &Type::UUID, false).as_deref(),
            Some("not a uuid"),
            "Postgres refuses it"
        );
        assert_eq!(
            text(
                json!(["a", "b \"c\"", "back\\slash", "x,y", "{}", "NULL", "", null]),
                &Type::TEXT_ARRAY,
                false
            )
            .as_deref(),
            Some(r#"{"a","b \"c\"","back\\slash","x,y","{}","NULL","",NULL}"#)
        );
        assert_eq!(
            text(json!([base62]), &Type::UUID_ARRAY, false).as_deref(),
            Some(r#"{"123e4567-e89b-12d3-a456-426614174000"}"#)
        );
        assert_eq!(
            text(json!([]), &Type::INT8_ARRAY, false).as_deref(),
            Some("{}")
        );
        assert!(param_text(&json!("x"), &Type::TEXT_ARRAY, false).is_err());
    }

    #[test]
    fn a_column_is_the_json_its_type_holds() {
        let column = |ty: &Type, raw: &[u8]| decode(ty, raw).unwrap();
        assert_eq!(column(&Type::BOOL, &[1]), json!(true));
        assert_eq!(column(&Type::INT8, &7_i64.to_be_bytes()), json!(7));
        assert_eq!(column(&Type::FLOAT8, &0.25_f64.to_be_bytes()), json!(0.25));
        assert!(decode(&Type::FLOAT8, &f64::NAN.to_be_bytes()).is_err());
        assert_eq!(column(&Type::TEXT, b"hi"), json!("hi"));
        let citext = Type::new("citext".into(), 99_999, Kind::Simple, "public".into());
        assert_eq!(
            column(&citext, b"Ada@Example.com"),
            json!("Ada@Example.com")
        );
        let mood = Type::new(
            "mood".into(),
            99_998,
            Kind::Enum(vec!["sad".into(), "ok".into()]),
            "public".into(),
        );
        assert_eq!(column(&mood, b"ok"), json!("ok"));
        assert_eq!(column(&Type::JSON, br#"{"a":1}"#), json!({"a": 1}));
        assert_eq!(column(&Type::JSONB, b"\x01[1,2]"), json!([1, 2]));
        assert!(decode(&Type::JSONB, b"\x02[1,2]").is_err());
        let err = decode(&Type::POINT, &[0; 16]).unwrap_err().to_string();
        assert_eq!(
            err,
            "a column of type point, which the Rust queue client does not read"
        );
    }
}
