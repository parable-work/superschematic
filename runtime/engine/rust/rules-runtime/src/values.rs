//! Values between the handler's forms and a table's columns (D117).
//!
//! The handler holds a row's own fields as a document's field kinds say
//! (an instant as `toISOString`'s text, a UUID in its base62 wire form),
//! and its owned fields, sub-table rows and shared rows as the core holds
//! them (an instant as epoch milliseconds). A column holds what the table a
//! build writes holds: a UUID's hyphenated text, an instant as ISO text on
//! SQLite and a `TIMESTAMPTZ` on Postgres, a JSON value as its text on
//! SQLite and as `JSONB` on Postgres, a boolean as 1 or 0 on SQLite.
//!
//! A value bound for a column is in its column form: the JSON a statement
//! binds, which SQLite binds by its storage class and Postgres reads through
//! `jsonb_populate_record` into each column's type. A value read from a
//! column is what the database gave: SQLite's storage class as JSON, or
//! Postgres's `to_jsonb` of the column.

use serde_json::{Number, Value};
use superschematic_rules::Refusal;
use superschematic_rules_adapter::{Column, Convert};
use superschematic_rules_handler::document::{Field, Kind};
use superschematic_rules_handler::values::{base62, instant_of, iso_of, uuid_of};

/// The database a store's statements are written for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Dialect {
    Sqlite,
    Postgres,
}

impl Dialect {
    /// The dialect as a plan names it.
    pub fn as_str(self) -> &'static str {
        match self {
            Dialect::Sqlite => "sqlite",
            Dialect::Postgres => "postgres",
        }
    }
}

/// The IR types whose wire form is base62 and whose column holds the
/// hyphenated text (D79).
const UUID_TYPES: [&str; 2] = ["Identity.UUID", "Identity.UserID"];

/// The IR type of a column that holds JSON.
const JSON_TYPE: &str = "Generic.JSON";

pub(crate) fn internal(text: impl std::fmt::Display) -> Refusal {
    Refusal::internal(format!("runtime: {text}"))
}

/// A UUID's value as its lowercase hyphenated text.
pub fn hyphenate(n: u128) -> String {
    let hex = format!("{n:032x}");
    format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    )
}

/// A double as the core's JSON holds it, as JavaScript writes one: a whole
/// number below 1e21 with no fraction, so SQLite's `REAL` 2.0 reads back as
/// the 2 a write gave it, as Postgres's `to_jsonb` writes it.
pub(crate) fn real(x: f64) -> Value {
    if x == 0.0 {
        return Value::from(0);
    }
    if x.fract() == 0.0 && x.abs() < 1e21 {
        if let Ok(whole) = format!("{x}").parse::<Number>() {
            return Value::Number(whole);
        }
    }
    Number::from_f64(x).map_or(Value::Null, Value::Number)
}

/// A number as it reads back from a column: a whole double as its digits.
fn number(value: &Value) -> Value {
    match value {
        Value::Number(n) if n.as_i64().is_none() && n.as_u64().is_none() => {
            n.as_f64().map_or_else(|| value.clone(), real)
        }
        other => other.clone(),
    }
}

/// A value the dialect keeps as JSON in its column form: SQLite's text of
/// it, Postgres's value itself, which `jsonb_populate_record` reads into a
/// `JSONB` or an array column.
fn json_column(value: Value, dialect: Dialect) -> Value {
    match dialect {
        Dialect::Sqlite => Value::String(value.to_string()),
        Dialect::Postgres => value,
    }
}

/// A JSON column's value as the dialect gave it: SQLite's text parsed,
/// Postgres's as it is.
fn json_read(value: &Value, dialect: Dialect) -> Value {
    match (dialect, value) {
        (Dialect::Sqlite, Value::String(text)) => {
            serde_json::from_str(text).unwrap_or_else(|_| value.clone())
        }
        _ => value.clone(),
    }
}

fn uuid_text(value: &Value, what: &str) -> Result<Value, Refusal> {
    match value {
        Value::String(text) => uuid_of(text)
            .map(|n| Value::String(hyphenate(n)))
            .ok_or_else(|| internal(format!("{what}: {value} is no UUID"))),
        other => Err(internal(format!("{what}: {other} is no UUID"))),
    }
}

fn uuid_wire(value: &Value, what: &str) -> Result<Value, Refusal> {
    match value {
        Value::String(text) => uuid_of(text)
            .map(|n| Value::String(base62(n)))
            .ok_or_else(|| internal(format!("{what}: {value} is no UUID"))),
        other => Err(internal(format!("{what}: {other} is no UUID"))),
    }
}

fn boolean(value: &Value) -> Value {
    match value {
        Value::Number(n) => Value::Bool(n.as_i64().unwrap_or(0) != 0),
        Value::String(text) => Value::Bool(matches!(text.as_str(), "1" | "true" | "t")),
        other => other.clone(),
    }
}

/// A key the handler holds (a UUID's base62 form) as its column binds it.
pub(crate) fn key_column(kind: &Kind, key: &str, what: &str) -> Result<Value, Refusal> {
    match kind {
        Kind::Uuid => uuid_text(&Value::String(key.to_owned()), what),
        _ => Ok(Value::String(key.to_owned())),
    }
}

/// A key column's value as the handler holds the key.
pub(crate) fn key_of(kind: &Kind, value: &Value, what: &str) -> Result<String, Refusal> {
    let value = match kind {
        Kind::Uuid => uuid_wire(value, what)?,
        _ => value.clone(),
    };
    Ok(match value {
        Value::String(text) => text,
        other => other.to_string(),
    })
}

/// An own field's value, in the handler's stored form, as its column binds
/// it.
pub(crate) fn own_to_column(
    field: &Field,
    value: &Value,
    dialect: Dialect,
    what: &str,
) -> Result<Value, Refusal> {
    if value.is_null() {
        return Ok(Value::Null);
    }
    if field.list {
        let Value::Array(items) = value else {
            return Ok(json_column(value.clone(), dialect));
        };
        let items = items
            .iter()
            .map(|item| element_to_column(&field.kind, item, what))
            .collect::<Result<Vec<_>, _>>()?;
        return Ok(json_column(Value::Array(items), dialect));
    }
    match &field.kind {
        Kind::Json | Kind::Object(_) => Ok(json_column(value.clone(), dialect)),
        kind => element_to_column(kind, value, what),
    }
}

fn element_to_column(kind: &Kind, value: &Value, what: &str) -> Result<Value, Refusal> {
    match kind {
        Kind::Uuid if !value.is_null() => uuid_text(value, what),
        _ => Ok(value.clone()),
    }
}

/// An own field's column, as the dialect read it, in the handler's stored
/// form; `None` for NULL, which the row leaves absent.
pub(crate) fn own_from_column(
    field: &Field,
    value: &Value,
    dialect: Dialect,
    what: &str,
) -> Result<Option<Value>, Refusal> {
    if value.is_null() {
        return Ok(None);
    }
    if field.list {
        let items = match json_read(value, dialect) {
            Value::Array(items) => items,
            other => return Ok(Some(other)),
        };
        let items = items
            .iter()
            .map(|item| element_from_column(&field.kind, item, dialect, what))
            .collect::<Result<Vec<_>, _>>()?;
        return Ok(Some(Value::Array(items)));
    }
    match &field.kind {
        Kind::Json | Kind::Object(_) => Ok(Some(json_read(value, dialect))),
        kind => element_from_column(kind, value, dialect, what).map(Some),
    }
}

fn element_from_column(
    kind: &Kind,
    value: &Value,
    dialect: Dialect,
    what: &str,
) -> Result<Value, Refusal> {
    if value.is_null() {
        return Ok(Value::Null);
    }
    Ok(match kind {
        Kind::Uuid => uuid_wire(value, what)?,
        Kind::DateTime => instant_of(value)
            .and_then(iso_of)
            .map(Value::String)
            .ok_or_else(|| internal(format!("{what}: {value} is no instant")))?,
        Kind::Bool => boolean(value),
        Kind::Number | Kind::Integer => number(value),
        Kind::Json | Kind::Object(_) => json_read(value, dialect),
        _ => value.clone(),
    })
}

/// A value the core holds (an owned field, a sub-table's or a shared
/// table's field) as its layout column binds it.
pub(crate) fn core_to_column(
    column: &Column,
    value: &Value,
    dialect: Dialect,
    what: &str,
) -> Result<Value, Refusal> {
    if value.is_null() {
        return Ok(Value::Null);
    }
    if column.type_name == JSON_TYPE {
        return Ok(json_column(value.clone(), dialect));
    }
    if column.convert == Some(Convert::EpochMillis) {
        return instant_of(value)
            .and_then(iso_of)
            .map(Value::String)
            .ok_or_else(|| internal(format!("{what}: {value} is no epoch milliseconds")));
    }
    if UUID_TYPES.contains(&column.type_name.as_str()) && value.is_string() {
        return uuid_text(value, what);
    }
    Ok(match value {
        Value::Array(_) | Value::Object(_) => json_column(value.clone(), dialect),
        other => other.clone(),
    })
}

/// A layout column's value, as the dialect read it, as the core holds it;
/// `None` for NULL.
pub(crate) fn core_from_column(
    column: &Column,
    value: &Value,
    dialect: Dialect,
    what: &str,
) -> Result<Option<Value>, Refusal> {
    if value.is_null() {
        return Ok(None);
    }
    if column.convert == Some(Convert::EpochMillis) {
        return instant_of(value)
            .map(|ms| Some(Value::from(ms)))
            .ok_or_else(|| internal(format!("{what}: {value} is no timestamp")));
    }
    if UUID_TYPES.contains(&column.type_name.as_str()) {
        return uuid_wire(value, what).map(Some);
    }
    Ok(Some(match column.type_name.as_str() {
        JSON_TYPE => json_read(value, dialect),
        "boolean" => boolean(value),
        _ => number(value),
    }))
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn field(kind: Kind, list: bool) -> Field {
        Field {
            name: "f".into(),
            type_name: String::new(),
            kind,
            list,
            required: false,
            unique: false,
            relation: None,
            min_length: None,
            max_length: None,
            min: None,
            max: None,
            list_min: None,
            list_max: None,
            default: None,
            description: String::new(),
        }
    }

    #[test]
    fn an_own_field_round_trips_through_each_dialect() {
        let uuid = base62(0x0123_4567_89ab_cdef_0123_4567_89ab_cdef);
        for dialect in [Dialect::Sqlite, Dialect::Postgres] {
            for (field, value) in [
                (field(Kind::Uuid, false), json!(uuid)),
                (field(Kind::Uuid, true), json!([uuid])),
                (
                    field(Kind::DateTime, false),
                    json!("2026-11-20T10:00:00.250Z"),
                ),
                (field(Kind::Json, false), json!({"a": [1, "b"]})),
                (field(Kind::Json, false), json!("text")),
                (field(Kind::Text, true), json!(["a", "b"])),
                (field(Kind::Number, false), json!(2.5)),
            ] {
                let column = own_to_column(&field, &value, dialect, "f").unwrap();
                let back = own_from_column(&field, &column, dialect, "f").unwrap();
                assert_eq!(back, Some(value.clone()), "{dialect:?} {column}");
            }
        }
        let column = own_to_column(
            &field(Kind::Uuid, false),
            &json!(uuid),
            Dialect::Sqlite,
            "f",
        );
        assert_eq!(
            column.unwrap(),
            json!("01234567-89ab-cdef-0123-456789abcdef")
        );
    }

    #[test]
    fn a_column_reads_as_the_handler_holds_it() {
        let bool_field = field(Kind::Bool, false);
        assert_eq!(
            own_from_column(&bool_field, &json!(1), Dialect::Sqlite, "f").unwrap(),
            Some(json!(true))
        );
        let at = field(Kind::DateTime, false);
        assert_eq!(
            own_from_column(
                &at,
                &json!("2026-11-20T11:00:00+01:00"),
                Dialect::Postgres,
                "f"
            )
            .unwrap(),
            Some(json!("2026-11-20T10:00:00.000Z"))
        );
        let whole = field(Kind::Number, false);
        assert_eq!(
            own_from_column(&whole, &json!(2.0), Dialect::Sqlite, "f").unwrap(),
            Some(json!(2))
        );
    }

    #[test]
    fn a_core_instant_is_epoch_milliseconds_and_its_column_iso_text() {
        let column: Column = serde_json::from_value(json!({
            "field": "enteredAt", "column": "status_entered_at", "type": "Temporal.DateTime", "convert": "epochMillis"
        }))
        .unwrap();
        let bound = core_to_column(&column, &json!(1_000), Dialect::Sqlite, "f").unwrap();
        assert_eq!(bound, json!("1970-01-01T00:00:01.000Z"));
        let back = core_from_column(
            &column,
            &json!("1970-01-01T00:00:01+00:00"),
            Dialect::Postgres,
            "f",
        )
        .unwrap();
        assert_eq!(back, Some(json!(1_000)));
    }
}
