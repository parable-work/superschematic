//! Values between the core's JSON and a table's columns (D103), by the
//! layout's column: a timestamp the core holds as epoch milliseconds is
//! the text `Date.prototype.toISOString` writes in a column, a UUID is
//! base62 in the core and lowercase hyphenated text in a column (D79), and
//! the rest is its JSON value, an object or an array as its JSON text.
//!
//! A value bound for a statement is a [`Bind`], which each store binds in
//! its own way: Postgres every one as text, as node-pg does, and SQLite
//! each as its own storage class. A value a store read is a [`Cell`].

use serde_json::{Number, Value};
use superschematic_rules::Refusal;

use crate::layout::{Column, Convert, Table};

/// The IR types whose wire form is base62 and whose column holds the
/// hyphenated text (D79).
pub(crate) const UUID_TYPES: [&str; 2] = ["Identity.UUID", "Identity.UserID"];

/// The IR type of a column that holds JSON, whose SQLite text the adapter
/// reads as the JSON it holds.
const JSON_TYPE: &str = "Generic.JSON";

/// A value a statement binds.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Bind {
    Null,
    Text(String),
    Int(i64),
    /// A number as the core wrote it, its text kept.
    Number(Number),
    Bool(bool),
}

impl Bind {
    /// A JSON value as it binds: a string as text, a number, a boolean,
    /// null, and an object or an array as its JSON text.
    pub(crate) fn of_json(value: &Value) -> Bind {
        match value {
            Value::Null => Bind::Null,
            Value::Bool(flag) => Bind::Bool(*flag),
            Value::Number(number) => Bind::Number(number.clone()),
            Value::String(text) => Bind::Text(text.clone()),
            Value::Array(_) | Value::Object(_) => Bind::Text(value.to_string()),
        }
    }

    /// The value as text, as Postgres reads a parameter sent as text; `None`
    /// for null.
    #[cfg(any(feature = "postgres", test))]
    pub(crate) fn text(&self) -> Option<String> {
        match self {
            Bind::Null => None,
            Bind::Text(text) => Some(text.clone()),
            Bind::Int(n) => Some(n.to_string()),
            Bind::Number(number) => Some(number.to_string()),
            Bind::Bool(flag) => Some(flag.to_string()),
        }
    }
}

/// A value a store read from a column. Each store makes the cells its
/// database gives, so a build with one store makes some of them alone.
#[derive(Clone, Debug, PartialEq)]
#[cfg_attr(not(all(feature = "postgres", feature = "sqlite")), allow(dead_code))]
pub(crate) enum Cell {
    Null,
    Int(i64),
    Real(f64),
    Bool(bool),
    Text(String),
    Blob(Vec<u8>),
    /// The column's JSON, as Postgres's `to_jsonb` gives it.
    Json(Value),
}

/// The refusal of a host's own failure: `internal`, 500, the class
/// `Error`, its message `rules: ` and the text.
pub(crate) fn internal(text: impl std::fmt::Display) -> Refusal {
    Refusal::internal(format!("rules: {text}"))
}

/// A row key as its column binds it: a UUID's hyphenated text, any other
/// key its text.
pub(crate) fn key_value(table: &Table, key: &str) -> Result<Bind, Refusal> {
    if UUID_TYPES.contains(&table.key.type_name.as_str()) {
        let n = uuid_value(key).ok_or_else(|| {
            internal(format!(
                "{}'s key {} is no UUID, hyphenated or base62",
                table.type_name,
                Value::from(key)
            ))
        })?;
        return Ok(Bind::Text(hyphenate(n)));
    }
    Ok(Bind::Text(key.to_owned()))
}

/// A value of the core's as its column binds it: null for none; a
/// timestamp's epoch milliseconds as its ISO text; a UUID's hyphenated
/// text; an object or array as its JSON text; anything else as it is.
pub(crate) fn to_column(value: &Value, column: &Column, table: &Table) -> Result<Bind, Refusal> {
    if value.is_null() {
        return Ok(Bind::Null);
    }
    if column.convert == Some(Convert::EpochMillis) {
        return millis_of(value)
            .and_then(iso_of_millis)
            .map(Bind::Text)
            .ok_or_else(|| {
                internal(format!(
                    "{}.{}: {value} is no epoch milliseconds",
                    table.type_name, column.field
                ))
            });
    }
    if let (true, Value::String(text)) = (is_uuid(column), value) {
        let n = uuid_value(text).ok_or_else(|| {
            internal(format!(
                "{}.{}: {value} is no UUID, hyphenated or base62",
                table.type_name, column.field
            ))
        })?;
        return Ok(Bind::Text(hyphenate(n)));
    }
    Ok(Bind::of_json(value))
}

/// A column's value as the core holds it: `None` for NULL, which the row
/// leaves absent; a timestamp as epoch milliseconds, from ISO text (any
/// offset) or a number; a UUID as its base62 wire form; a boolean column's
/// 1 or 0 as a boolean; a JSON column's SQLite text as its JSON.
pub(crate) fn from_cell(
    cell: Cell,
    column: &Column,
    table: &Table,
) -> Result<Option<Value>, Refusal> {
    if matches!(cell, Cell::Null | Cell::Json(Value::Null)) {
        return Ok(None);
    }
    if column.convert == Some(Convert::EpochMillis) {
        return timestamp_of(&cell).map(Some).ok_or_else(|| {
            internal(format!(
                "{}.{}: {} is no timestamp",
                table.type_name,
                column.field,
                Value::from(shown(&cell))
            ))
        });
    }
    if is_uuid(column) {
        return uuid_of(&cell)
            .map(|n| Some(Value::from(base62(n))))
            .ok_or_else(|| {
                internal(format!(
                    "{}.{}: {} is no UUID, hyphenated or base62",
                    table.type_name,
                    column.field,
                    Value::from(shown(&cell))
                ))
            });
    }
    Ok(Some(match cell {
        Cell::Int(n) if column.type_name == "boolean" => Value::Bool(n != 0),
        Cell::Text(text) if column.type_name == JSON_TYPE => {
            serde_json::from_str(&text).unwrap_or(Value::String(text))
        }
        other => plain(other),
    }))
}

fn is_uuid(column: &Column) -> bool {
    UUID_TYPES.contains(&column.type_name.as_str())
}

/// A cell as the JSON value it holds, with no conversion.
fn plain(cell: Cell) -> Value {
    match cell {
        Cell::Null => Value::Null,
        Cell::Int(n) => Value::from(n),
        Cell::Real(x) => Number::from_f64(x).map_or(Value::Null, Value::Number),
        Cell::Bool(flag) => Value::Bool(flag),
        Cell::Text(text) => Value::String(text),
        Cell::Blob(bytes) => Value::String(String::from_utf8_lossy(&bytes).into_owned()),
        Cell::Json(value) => value,
    }
}

/// A cell as a refusal shows it.
fn shown(cell: &Cell) -> String {
    match cell {
        Cell::Text(text) => text.clone(),
        Cell::Json(Value::String(text)) => text.clone(),
        other => plain(other.clone()).to_string(),
    }
}

/// A timestamp cell as epoch milliseconds: text parsed, a number as it is.
fn timestamp_of(cell: &Cell) -> Option<Value> {
    match cell {
        Cell::Text(text) | Cell::Json(Value::String(text)) => {
            parse_timestamp(text).map(Value::from)
        }
        Cell::Blob(bytes) => parse_timestamp(std::str::from_utf8(bytes).ok()?).map(Value::from),
        Cell::Int(n) => Some(Value::from(*n)),
        Cell::Real(x) => Number::from_f64(*x).map(Value::Number),
        Cell::Json(Value::Number(number)) => Some(Value::Number(number.clone())),
        _ => None,
    }
}

/// A UUID cell's value: hyphenated or base62 text, or 16 bytes.
fn uuid_of(cell: &Cell) -> Option<u128> {
    match cell {
        Cell::Text(text) | Cell::Json(Value::String(text)) => uuid_value(text),
        Cell::Blob(bytes) => match <[u8; 16]>::try_from(bytes.as_slice()) {
            Ok(raw) => Some(u128::from_be_bytes(raw)),
            Err(_) => uuid_value(std::str::from_utf8(bytes).ok()?),
        },
        _ => None,
    }
}

/// A value the core holds as epoch milliseconds: a number, its fraction
/// dropped as a `Date` drops it, or the text of one.
fn millis_of(value: &Value) -> Option<i64> {
    let millis = match value {
        Value::Number(number) => number
            .as_i64()
            .map(|n| n as f64)
            .or_else(|| number.as_f64())?,
        Value::String(text) => text.trim().parse::<f64>().ok()?,
        _ => return None,
    };
    millis.is_finite().then_some(millis.trunc() as i64)
}

const BASE62: &[u8; 62] = b"0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz";

/// A UUID's value from its hyphenated form (36 characters) or its base62
/// form (1 to 22, case-sensitive), as superscalar's `ParseUUID` reads it;
/// `None` for any other text.
pub(crate) fn uuid_value(text: &str) -> Option<u128> {
    if text.len() == 36 {
        let bytes = text.as_bytes();
        let hyphenated = bytes.iter().enumerate().all(|(i, byte)| match i {
            8 | 13 | 18 | 23 => *byte == b'-',
            _ => byte.is_ascii_hexdigit(),
        });
        if !hyphenated {
            return None;
        }
        return u128::from_str_radix(&text.replace('-', ""), 16).ok();
    }
    if text.is_empty() || text.len() > 22 {
        return None;
    }
    text.bytes().try_fold(0u128, |n, byte| {
        let digit = BASE62.iter().position(|c| *c == byte)?;
        n.checked_mul(62)?.checked_add(digit as u128)
    })
}

/// A UUID's value as its lowercase hyphenated text.
pub(crate) fn hyphenate(n: u128) -> String {
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

/// A UUID's value as its base62 text, the scalar's wire form: no padding,
/// `0` for zero.
pub(crate) fn base62(mut n: u128) -> String {
    if n == 0 {
        return "0".to_owned();
    }
    let mut out = Vec::new();
    while n > 0 {
        out.push(BASE62[(n % 62) as usize]);
        n /= 62;
    }
    out.reverse();
    String::from_utf8(out).unwrap_or_default()
}

const DAY: i64 = 86_400_000;

/// The text `Date.prototype.toISOString` writes for an instant in epoch
/// milliseconds: UTC, three fraction digits, a year past 9999 or before 0
/// in six digits with its sign; `None` past the range a `Date` holds.
pub(crate) fn iso_of_millis(millis: i64) -> Option<String> {
    if millis.unsigned_abs() > 8_640_000_000_000_000 {
        return None;
    }
    let (year, month, day) = civil_of_days(millis.div_euclid(DAY));
    let of_day = millis.rem_euclid(DAY);
    let year = if (0..=9999).contains(&year) {
        format!("{year:04}")
    } else if year < 0 {
        format!("-{:06}", -year)
    } else {
        format!("+{year:06}")
    };
    Some(format!(
        "{year}-{month:02}-{day:02}T{:02}:{:02}:{:02}.{:03}Z",
        of_day / 3_600_000,
        of_day / 60_000 % 60,
        of_day / 1000 % 60,
        of_day % 1000
    ))
}

/// Epoch milliseconds from RFC 3339 text, with `T` or a space between the
/// date and the time, any number of fraction digits (past the third
/// dropped) and `Z`, an offset, or no zone, which is UTC: what
/// `toISOString` writes and what Postgres's `to_jsonb` of a timestamp
/// writes in any session time zone.
pub(crate) fn parse_timestamp(text: &str) -> Option<i64> {
    let mut at = Cursor {
        text: text.as_bytes(),
        at: 0,
    };
    let sign = match at.peek() {
        Some(b'+') => {
            at.at += 1;
            1
        }
        Some(b'-') => {
            at.at += 1;
            -1
        }
        _ => 0,
    };
    let year = at.digits(if sign == 0 { 4 } else { 6 })? as i64 * if sign < 0 { -1 } else { 1 };
    at.expect(b'-')?;
    let month = at.digits(2)?;
    at.expect(b'-')?;
    let day = at.digits(2)?;
    if !(1..=12).contains(&month) || day < 1 || day > days_in_month(year, month) {
        return None;
    }
    match at.next()? {
        b'T' | b't' | b' ' => {}
        _ => return None,
    }
    let hour = at.digits(2)? as i64;
    at.expect(b':')?;
    let minute = at.digits(2)? as i64;
    let mut second = 0;
    let mut fraction = 0;
    if at.peek() == Some(b':') {
        at.at += 1;
        second = at.digits(2)? as i64;
        if at.peek() == Some(b'.') || at.peek() == Some(b',') {
            at.at += 1;
            fraction = at.fraction()?;
        }
    }
    if hour > 23 || minute > 59 || second > 60 {
        return None;
    }
    let offset = at.offset()?;
    if at.at != at.text.len() {
        return None;
    }
    Some(
        days_of_civil(year, month, day) * DAY
            + hour * 3_600_000
            + minute * 60_000
            + second * 1000
            + fraction
            - offset,
    )
}

struct Cursor<'a> {
    text: &'a [u8],
    at: usize,
}

impl Cursor<'_> {
    fn peek(&self) -> Option<u8> {
        self.text.get(self.at).copied()
    }

    fn next(&mut self) -> Option<u8> {
        let byte = self.peek()?;
        self.at += 1;
        Some(byte)
    }

    fn expect(&mut self, byte: u8) -> Option<()> {
        (self.next()? == byte).then_some(())
    }

    fn digits(&mut self, count: usize) -> Option<u32> {
        let mut n = 0;
        for _ in 0..count {
            let byte = self.next()?;
            if !byte.is_ascii_digit() {
                return None;
            }
            n = n * 10 + u32::from(byte - b'0');
        }
        Some(n)
    }

    /// A fraction's milliseconds: its first three digits, the rest dropped.
    fn fraction(&mut self) -> Option<i64> {
        let start = self.at;
        let mut millis = 0;
        while let Some(byte) = self.peek().filter(u8::is_ascii_digit) {
            if self.at - start < 3 {
                millis = millis * 10 + i64::from(byte - b'0');
            }
            self.at += 1;
        }
        let read = self.at - start;
        if read == 0 {
            return None;
        }
        for _ in read..3 {
            millis *= 10;
        }
        Some(millis)
    }

    /// The zone's offset east of UTC in milliseconds: 0 for `Z` or none,
    /// else `±HH`, `±HHMM`, `±HH:MM` or `±HH:MM:SS`.
    fn offset(&mut self) -> Option<i64> {
        let sign = match self.peek() {
            None => return Some(0),
            Some(b'Z' | b'z') => {
                self.at += 1;
                return Some(0);
            }
            Some(b'+') => 1,
            Some(b'-') => -1,
            Some(_) => return None,
        };
        self.at += 1;
        let hours = i64::from(self.digits(2)?);
        let mut minutes = 0;
        let mut seconds = 0;
        if self.peek() == Some(b':') {
            self.at += 1;
        }
        if self.peek().is_some() {
            minutes = i64::from(self.digits(2)?);
            if self.peek() == Some(b':') {
                self.at += 1;
                seconds = i64::from(self.digits(2)?);
            }
        }
        Some(sign * (hours * 3_600_000 + minutes * 60_000 + seconds * 1000))
    }
}

fn is_leap(year: i64) -> bool {
    (year % 4 == 0 && year % 100 != 0) || year % 400 == 0
}

fn days_in_month(year: i64, month: u32) -> u32 {
    match month {
        2 if is_leap(year) => 29,
        2 => 28,
        4 | 6 | 9 | 11 => 30,
        _ => 31,
    }
}

/// The civil date of a day counted from 1970-01-01 (Howard Hinnant's
/// algorithm, proleptic Gregorian).
fn civil_of_days(days: i64) -> (i64, u32, u32) {
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = (doy - (153 * mp + 2) / 5 + 1) as u32;
    let month = if mp < 10 { mp + 3 } else { mp - 9 } as u32;
    let year = yoe + era * 400 + i64::from(month <= 2);
    (year, month, day)
}

/// The day counted from 1970-01-01 of a civil date.
fn days_of_civil(year: i64, month: u32, day: u32) -> i64 {
    let year = if month <= 2 { year - 1 } else { year };
    let era = year.div_euclid(400);
    let yoe = year.rem_euclid(400);
    let month = i64::from(month);
    let doy = (153 * if month > 2 { month - 3 } else { month + 9 } + 2) / 5 + i64::from(day) - 1;
    let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy;
    era * 146_097 + doe - 719_468
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn order() -> Table {
        Table {
            type_name: "Order".into(),
            table: "order".into(),
            key: Column::new("id", "id", "Identity.UUID"),
            version: "_version".into(),
            live: None,
            own: Vec::new(),
            uses: Vec::new(),
            spec: superschematic_rules::TypeSpec::new("Order"),
        }
    }

    #[test]
    fn a_uuid_reads_in_either_form_and_writes_in_each() {
        let key = "6fe1XoZ0UJpMNNdUOSMC2X";
        let column = "db360f43-5530-c407-56da-9cd8e0c971dd";
        assert_eq!(uuid_value(key).map(hyphenate).as_deref(), Some(column));
        assert_eq!(uuid_value(column).map(base62).as_deref(), Some(key));
        assert_eq!(
            uuid_value("DB360F43-5530-C407-56DA-9CD8E0C971DD")
                .map(base62)
                .as_deref(),
            Some(key)
        );
        assert_eq!(base62(0), "0");
        assert_eq!(uuid_value("0"), Some(0));
        assert_eq!(hyphenate(0), "00000000-0000-0000-0000-000000000000");
        assert_eq!(uuid_value(&base62(u128::MAX)), Some(u128::MAX));
        assert_eq!(base62(u128::MAX).len(), 22);
        assert_eq!(hyphenate(u128::MAX), "ffffffff-ffff-ffff-ffff-ffffffffffff");
        // 22 base62 digits can name more than 128 bits, and other text is
        // no UUID.
        assert_eq!(uuid_value("zzzzzzzzzzzzzzzzzzzzzz"), None);
        assert_eq!(uuid_value(""), None);
        assert_eq!(uuid_value("not-a-uuid!"), None);
        assert_eq!(uuid_value("db360f43_5530-c407-56da-9cd8e0c971dd"), None);
        assert_eq!(uuid_value("0123456789012345678901234"), None);
    }

    #[test]
    fn a_timestamp_writes_as_to_iso_string_and_reads_back_from_rfc_3339() {
        assert_eq!(
            iso_of_millis(0).as_deref(),
            Some("1970-01-01T00:00:00.000Z")
        );
        assert_eq!(
            iso_of_millis(5000).as_deref(),
            Some("1970-01-01T00:00:05.000Z")
        );
        assert_eq!(
            iso_of_millis(1_000_000).as_deref(),
            Some("1970-01-01T00:16:40.000Z")
        );
        assert_eq!(
            iso_of_millis(-1).as_deref(),
            Some("1969-12-31T23:59:59.999Z")
        );
        assert_eq!(
            iso_of_millis(951_782_400_123).as_deref(),
            Some("2000-02-29T00:00:00.123Z")
        );
        assert_eq!(
            iso_of_millis(253_402_300_800_000).as_deref(),
            Some("+010000-01-01T00:00:00.000Z")
        );
        assert_eq!(
            iso_of_millis(-62_198_755_200_000).as_deref(),
            Some("-000001-01-01T00:00:00.000Z")
        );
        assert_eq!(iso_of_millis(8_640_000_000_000_001), None);
        for millis in [
            0,
            1,
            -1,
            1_234,
            951_782_400_123,
            1_793_000_000_000,
            -62_198_755_200_000,
            253_402_300_800_000,
        ] {
            let text = iso_of_millis(millis).expect("in range");
            assert_eq!(parse_timestamp(&text), Some(millis), "{text}");
        }
        // Postgres's to_jsonb writes the session's offset and microseconds;
        // SQLite's text may use a space and no zone.
        assert_eq!(
            parse_timestamp("1970-01-01T02:00:01.234567+02:00"),
            Some(1234)
        );
        assert_eq!(
            parse_timestamp("1970-01-01T00:00:01.2-00:30"),
            Some(1_801_200)
        );
        assert_eq!(parse_timestamp("1970-01-01 00:00:01.5"), Some(1500));
        assert_eq!(
            parse_timestamp("1970-01-01T00:00:01+0100"),
            Some(1000 - 3_600_000)
        );
        assert_eq!(
            parse_timestamp("1970-01-01T00:00:01+01"),
            Some(1000 - 3_600_000)
        );
        assert_eq!(parse_timestamp("1970-01-01T00:01Z"), Some(60_000));
        assert_eq!(parse_timestamp("1969-12-31T23:59:59.9985Z"), Some(-2));
        for bad in [
            "",
            "yesterday",
            "1970-13-01T00:00:00Z",
            "1970-02-30T00:00:00Z",
            "1970-01-01T24:00:00Z",
            "1970-01-01T00:00:00Zjunk",
            "1970-01-01",
        ] {
            assert_eq!(parse_timestamp(bad), None, "{bad}");
        }
    }

    #[test]
    fn a_value_binds_by_its_column() {
        let table = order();
        let at = Column::new("enteredAt", "entered_at", "Temporal.DateTime")
            .converted(Convert::EpochMillis);
        let id = Column::new("ownerId", "owner_id", "Identity.UUID");
        let text = Column::new("title", "title", "string");
        assert_eq!(to_column(&Value::Null, &at, &table), Ok(Bind::Null));
        assert_eq!(
            to_column(&json!(5000), &at, &table),
            Ok(Bind::Text("1970-01-01T00:00:05.000Z".into()))
        );
        assert_eq!(
            to_column(&json!(5000.9), &at, &table),
            Ok(Bind::Text("1970-01-01T00:00:05.000Z".into()))
        );
        assert_eq!(
            to_column(&json!("5000"), &at, &table),
            Ok(Bind::Text("1970-01-01T00:00:05.000Z".into()))
        );
        assert_eq!(
            to_column(&json!("soon"), &at, &table).map_err(|refusal| refusal.message),
            Err("rules: Order.enteredAt: \"soon\" is no epoch milliseconds".to_owned())
        );
        assert_eq!(
            to_column(&json!("6fe1XoZ0UJpMNNdUOSMC2X"), &id, &table),
            Ok(Bind::Text("db360f43-5530-c407-56da-9cd8e0c971dd".into()))
        );
        assert!(to_column(&json!("no-uuid!"), &id, &table).is_err());
        assert_eq!(
            to_column(&json!({"a": [1]}), &text, &table),
            Ok(Bind::Text(r#"{"a":[1]}"#.into()))
        );
        assert_eq!(
            to_column(&json!([1, 2]), &text, &table),
            Ok(Bind::Text("[1,2]".into()))
        );
        assert_eq!(to_column(&json!(true), &text, &table), Ok(Bind::Bool(true)));
        assert_eq!(
            to_column(&json!("Desk"), &text, &table),
            Ok(Bind::Text("Desk".into()))
        );
        let big: Value = serde_json::from_str("12345678901234567890.5").expect("a number");
        assert_eq!(
            to_column(&big, &text, &table).map(|bind| bind.text()),
            Ok(Some("12345678901234567890.5".into()))
        );
        assert_eq!(
            key_value(&table, "6fe1XoZ0UJpMNNdUOSMC2X"),
            Ok(Bind::Text("db360f43-5530-c407-56da-9cd8e0c971dd".into()))
        );
        assert!(key_value(&table, "o-1").is_err());
    }

    #[test]
    fn a_cell_reads_by_its_column() {
        let table = order();
        let at = Column::new("enteredAt", "entered_at", "Temporal.DateTime")
            .converted(Convert::EpochMillis);
        let id = Column::new("ownerId", "owner_id", "Identity.UUID");
        let flag = Column::new("done", "done", "boolean");
        let data = Column::new("data", "data", "Generic.JSON");
        let text = Column::new("title", "title", "string");
        let read = |cell: Cell, column: &Column| from_cell(cell, column, &table);
        assert_eq!(read(Cell::Null, &text), Ok(None));
        assert_eq!(read(Cell::Json(Value::Null), &text), Ok(None));
        assert_eq!(
            read(Cell::Text("1970-01-01T00:00:01.234Z".into()), &at),
            Ok(Some(json!(1234)))
        );
        assert_eq!(
            read(Cell::Json(json!("1970-01-01T01:00:01.234+01:00")), &at),
            Ok(Some(json!(1234)))
        );
        assert_eq!(read(Cell::Int(77), &at), Ok(Some(json!(77))));
        assert_eq!(
            read(Cell::Text("soon".into()), &at).map_err(|refusal| refusal.message),
            Err("rules: Order.enteredAt: \"soon\" is no timestamp".to_owned())
        );
        assert_eq!(
            read(
                Cell::Text("db360f43-5530-c407-56da-9cd8e0c971dd".into()),
                &id
            ),
            Ok(Some(json!("6fe1XoZ0UJpMNNdUOSMC2X")))
        );
        assert_eq!(
            read(
                Cell::Json(json!("db360f43-5530-c407-56da-9cd8e0c971dd")),
                &id
            ),
            Ok(Some(json!("6fe1XoZ0UJpMNNdUOSMC2X")))
        );
        let raw = 0xdb36_0f43_5530_c407_56da_9cd8_e0c9_71dd_u128
            .to_be_bytes()
            .to_vec();
        assert_eq!(
            read(Cell::Blob(raw), &id),
            Ok(Some(json!("6fe1XoZ0UJpMNNdUOSMC2X")))
        );
        assert!(read(Cell::Int(1), &id).is_err());
        assert_eq!(read(Cell::Int(0), &flag), Ok(Some(json!(false))));
        assert_eq!(read(Cell::Int(2), &flag), Ok(Some(json!(true))));
        assert_eq!(
            read(Cell::Text(r#"{"a":1}"#.into()), &data),
            Ok(Some(json!({"a": 1})))
        );
        assert_eq!(
            read(Cell::Text("not json".into()), &data),
            Ok(Some(json!("not json")))
        );
        assert_eq!(read(Cell::Int(3), &text), Ok(Some(json!(3))));
        assert_eq!(read(Cell::Real(1.5), &text), Ok(Some(json!(1.5))));
        assert_eq!(read(Cell::Bool(true), &text), Ok(Some(json!(true))));
        assert_eq!(
            read(Cell::Blob(b"Desk".to_vec()), &text),
            Ok(Some(json!("Desk")))
        );
        assert_eq!(
            read(Cell::Json(json!({"a": [1]})), &text),
            Ok(Some(json!({"a": [1]})))
        );
    }
}
