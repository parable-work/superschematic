//! Canonical rows (D19): the form the version-graph core compares and
//! hashes.
//!
//! A canonical row is a JSON object keyed by column name whose values are the
//! schema runtime's JSON for each field's type. A graph descriptor (version
//! 3) gives every column a value class, and each class has one rule that
//! turns what Postgres returns, in `to_jsonb` of a live row or in a history
//! image, into its canonical JSON. `runtime/versiongraph/README.md`
//! ("Canonical rows") is the contract, `runtime/versiongraph/testdata/canonical`
//! holds its vectors, and this module is the Rust port of the Go module's
//! package `canonical`.
//!
//! Every function returns canonical JSON as text: object members sorted by
//! name whatever `serde_json`'s map order is, so the output does not depend
//! on the features another crate turns on.

mod number;
mod temporal;

use std::fmt;

use serde_json::Value;

/// The element classes. A column's class is one of them, a list of one
/// (`"uuid[]"`) or a list of lists (`"uuid[][]"`).
pub const STRING: &str = "string";
pub const INTEGER: &str = "integer";
pub const NUMBER: &str = "number";
pub const BOOLEAN: &str = "boolean";
pub const UUID: &str = "uuid";
pub const DATE_TIME: &str = "dateTime";
pub const DATE: &str = "date";
pub const TIME: &str = "time";
pub const DURATION: &str = "duration";
pub const ENUM: &str = "enum";
pub const JSON: &str = "json";

/// The element classes, each with its rule.
pub const CLASSES: [&str; 11] = [
    STRING, INTEGER, NUMBER, BOOLEAN, UUID, DATE_TIME, DATE, TIME, DURATION, ENUM, JSON,
];

/// A value its class's rule refuses, a row that does not fit its columns, or
/// a class that is not one.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error {
    /// The row's column, empty for a single value.
    pub column: String,
    /// The value class the rule belongs to, empty when there is none.
    pub class: String,
    /// What is wrong with the value.
    pub message: String,
    /// Whether the class itself is unknown.
    pub unknown_class: bool,
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        if self.unknown_class {
            return write!(f, "canonical: unknown value class {:?}", self.class);
        }
        match (self.column.is_empty(), self.class.is_empty()) {
            (false, false) => write!(
                f,
                "canonical: column {} ({}): {}",
                self.column, self.class, self.message
            ),
            (false, true) => write!(f, "canonical: column {}: {}", self.column, self.message),
            _ => write!(f, "canonical: {}: {}", self.class, self.message),
        }
    }
}

impl std::error::Error for Error {}

impl Error {
    fn value(class: &str, message: impl Into<String>) -> Self {
        Error {
            column: String::new(),
            class: class.to_owned(),
            message: message.into(),
            unknown_class: false,
        }
    }

    fn unknown(class: &str) -> Self {
        Error {
            column: String::new(),
            class: class.to_owned(),
            message: "unknown value class".to_owned(),
            unknown_class: true,
        }
    }
}

/// A rule turns one decoded element into its canonical JSON text.
type Rule = fn(&Value) -> Result<String, String>;

fn rule_of(element: &str) -> Option<Rule> {
    Some(match element {
        STRING | ENUM => string_rule,
        INTEGER => number::integer_rule,
        NUMBER => number::number_rule,
        BOOLEAN => boolean_rule,
        UUID => temporal::uuid_rule,
        DATE_TIME => temporal::date_time_rule,
        DATE => temporal::date_rule,
        TIME => temporal::time_rule,
        DURATION => temporal::duration_rule,
        JSON => json_rule,
        _ => return None,
    })
}

/// Splits a class into its element rule and list depth.
fn parse_class(class: &str) -> Result<(Rule, usize), Error> {
    let (element, depth) = if let Some(element) = class.strip_suffix("[][]") {
        (element, 2)
    } else if let Some(element) = class.strip_suffix("[]") {
        (element, 1)
    } else {
        (class, 0)
    };
    rule_of(element)
        .map(|rule| (rule, depth))
        .ok_or_else(|| Error::unknown(class))
}

/// Whether `class` is an element class, a list of one or a list of lists of
/// one.
pub fn is_class(class: &str) -> bool {
    parse_class(class).is_ok()
}

/// The canonical JSON of one value of `class`, given as JSON text as
/// Postgres renders it inside `to_jsonb`. JSON `null` is `null` in every
/// class.
pub fn postgres(class: &str, value: &str) -> Result<String, Error> {
    let (rule, depth) = parse_class(class)?;
    let decoded = decode(value).map_err(|message| Error::value(class, message))?;
    apply(rule, depth, &decoded).map_err(|message| Error::value(class, message))
}

/// [`postgres`] over a decoded value.
pub fn postgres_value(class: &str, value: &Value) -> Result<String, Error> {
    let (rule, depth) = parse_class(class)?;
    apply(rule, depth, value).map_err(|message| Error::value(class, message))
}

/// The canonical row of a row as `to_jsonb` renders it, given as JSON text,
/// whose columns have the classes `columns` gives: a JSON object with its
/// members sorted by column name. A column the row has and `columns` lacks
/// is refused; one `columns` has and the row lacks stays absent.
pub fn postgres_row<C: Columns + ?Sized>(columns: &C, row: &str) -> Result<String, Error> {
    let decoded = decode(row).map_err(|message| Error {
        column: String::new(),
        class: String::new(),
        message: format!("row: {message}"),
        unknown_class: false,
    })?;
    row_value(columns, &decoded)
}

/// [`postgres_row`] over a decoded row. It is also the canonical row of a
/// row whose values are the schema runtime's JSON for each column's field
/// type, as a typed value serializes: the rules read the schema runtime's
/// forms as they read Postgres's (a base62 UUID, a date-time with any
/// offset, an `HH:MM` time, a duration string).
pub fn row_value<C: Columns + ?Sized>(columns: &C, row: &Value) -> Result<String, Error> {
    let Value::Object(members) = row else {
        return Err(Error {
            column: String::new(),
            class: String::new(),
            message: "a row is a JSON object".to_owned(),
            unknown_class: false,
        });
    };
    let mut names: Vec<&String> = members.keys().collect();
    names.sort();
    let mut out = String::from("{");
    for (i, name) in names.into_iter().enumerate() {
        let Some(class) = columns.class(name) else {
            return Err(Error {
                column: name.clone(),
                class: String::new(),
                message: "the row has a column its descriptor does not declare".to_owned(),
                unknown_class: false,
            });
        };
        let (rule, depth) = parse_class(class).map_err(|mut error| {
            error.column = name.clone();
            error
        })?;
        let value = apply(rule, depth, &members[name]).map_err(|message| Error {
            column: name.clone(),
            class: class.to_owned(),
            message,
            unknown_class: false,
        })?;
        if i > 0 {
            out.push(',');
        }
        write_string(&mut out, name);
        out.push(':');
        out.push_str(&value);
    }
    out.push('}');
    Ok(out)
}

/// A column-to-class lookup: a descriptor kind's `columns`.
pub trait Columns {
    /// The value class of `column`, `None` when the kind has no such column.
    fn class(&self, column: &str) -> Option<&str>;
}

impl Columns for std::collections::BTreeMap<String, String> {
    fn class(&self, column: &str) -> Option<&str> {
        self.get(column).map(String::as_str)
    }
}

impl Columns for std::collections::HashMap<String, String> {
    fn class(&self, column: &str) -> Option<&str> {
        self.get(column).map(String::as_str)
    }
}

/// Runs an element rule over a value, a list or a list of lists. A null
/// value is null; a null element is refused, since a list element is never
/// null (D12).
fn apply(rule: Rule, depth: usize, value: &Value) -> Result<String, String> {
    if value.is_null() {
        return Ok("null".to_owned());
    }
    if depth == 0 {
        return rule(value);
    }
    let Value::Array(list) = value else {
        return Err(format!("{} is not a list", describe(value)));
    };
    let mut out = String::from("[");
    for (i, element) in list.iter().enumerate() {
        if element.is_null() {
            return Err(format!(
                "element {i} is null, and a list element is never null"
            ));
        }
        let text = apply(rule, depth - 1, element).map_err(|e| format!("element {i}: {e}"))?;
        if i > 0 {
            out.push(',');
        }
        out.push_str(&text);
    }
    out.push(']');
    Ok(out)
}

/// Reads one JSON value, keeping each number's digits.
fn decode(text: &str) -> Result<Value, String> {
    serde_json::from_str(text).map_err(|e| e.to_string())
}

/// Names a decoded value for an error message.
fn describe(value: &Value) -> String {
    match value {
        Value::String(s) => format!("{s:?}"),
        Value::Number(n) => format!("the number {n}"),
        Value::Bool(b) => b.to_string(),
        Value::Array(_) => "a list".to_owned(),
        Value::Object(_) => "an object".to_owned(),
        Value::Null => "null".to_owned(),
    }
}

fn string_rule(value: &Value) -> Result<String, String> {
    let Value::String(s) = value else {
        return Err(format!("{} is not a string", describe(value)));
    };
    let mut out = String::new();
    write_string(&mut out, s);
    Ok(out)
}

fn boolean_rule(value: &Value) -> Result<String, String> {
    match value {
        Value::Bool(true) => Ok("true".to_owned()),
        Value::Bool(false) => Ok("false".to_owned()),
        _ => Err(format!("{} is not a boolean", describe(value))),
    }
}

/// Writes any JSON value canonically: object members sorted by key, no
/// whitespace, strings escaped as [`write_string`] does, and every number in
/// the number class's form.
fn json_rule(value: &Value) -> Result<String, String> {
    let mut out = String::new();
    write_json(&mut out, value)?;
    Ok(out)
}

fn write_json(out: &mut String, value: &Value) -> Result<(), String> {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(true) => out.push_str("true"),
        Value::Bool(false) => out.push_str("false"),
        Value::String(s) => write_string(out, s),
        Value::Number(_) => out.push_str(&number::number_rule(value)?),
        Value::Array(list) => {
            out.push('[');
            for (i, element) in list.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_json(out, element)?;
            }
            out.push(']');
        }
        Value::Object(members) => {
            let mut keys: Vec<&String> = members.keys().collect();
            keys.sort();
            out.push('{');
            for (i, key) in keys.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write_string(out, key);
                out.push(':');
                write_json(out, &members[key])?;
            }
            out.push('}');
        }
    }
    Ok(())
}

/// Writes a JSON string as the core's canonical JSON does: `"` and `\`
/// escaped, `\b`, `\f`, `\n`, `\r` and `\t` by name, every other control
/// character as `\u00xx` in lowercase hex, and everything else as it is.
pub(crate) fn write_string(out: &mut String, s: &str) {
    const HEX: &[u8; 16] = b"0123456789abcdef";
    out.push('"');
    for c in s.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{08}' => out.push_str("\\b"),
            '\u{0c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            c if (c as u32) < 0x20 => {
                let byte = c as u8;
                out.push_str("\\u00");
                out.push(HEX[usize::from(byte >> 4)] as char);
                out.push(HEX[usize::from(byte & 0xf)] as char);
            }
            c => out.push(c),
        }
    }
    out.push('"');
}

/// The canonical form (base62) of a UUID given hyphenated or in base62.
pub fn uuid(value: &str) -> Result<String, Error> {
    let text = temporal::uuid_rule(&Value::String(value.to_owned()))
        .map_err(|message| Error::value(UUID, message))?;
    Ok(text[1..text.len() - 1].to_owned())
}

/// The hyphenated form Postgres reads of a UUID given in its canonical form
/// or hyphenated.
pub fn uuid_hyphenated(value: &str) -> Result<String, Error> {
    let bits = temporal::uuid_bits(value).map_err(|message| Error::value(UUID, message))?;
    let hex = format!("{bits:032x}");
    Ok(format!(
        "{}-{}-{}-{}-{}",
        &hex[0..8],
        &hex[8..12],
        &hex[12..16],
        &hex[16..20],
        &hex[20..32]
    ))
}

/// The nanoseconds of a duration string as Go's `time.ParseDuration` reads
/// it (`1h30m0s`, `1.5ms`, `-1m30.5s`).
pub fn parse_duration(value: &str) -> Result<i64, Error> {
    temporal::parse_go_duration(value).map_err(|message| Error::value(DURATION, message))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unknown_classes_are_refused() {
        for class in ["decimal", "uuid[][][]", "", "[]"] {
            let error = postgres(class, "\"x\"").expect_err(class);
            assert!(error.unknown_class, "{class}: {error}");
        }
        let columns: std::collections::BTreeMap<String, String> =
            [("id".to_owned(), "decimal".to_owned())].into();
        let error = postgres_row(&columns, r#"{"id":1}"#).expect_err("unknown class");
        assert!(error.unknown_class, "{error}");
    }

    #[test]
    fn input_that_is_not_one_json_value_is_refused() {
        for input in ["", "1 2", r#"{"a":"#] {
            assert!(postgres(INTEGER, input).is_err(), "{input:?}");
        }
        let columns = std::collections::BTreeMap::<String, String>::new();
        assert!(postgres_row(&columns, "[1]").is_err());
    }

    /// -0, which no Postgres rendering carries (to_jsonb writes 0) but a
    /// stored JSON value can, is 0 in both numeric classes.
    #[test]
    fn negative_zero_is_zero() {
        for class in [INTEGER, NUMBER] {
            assert_eq!(postgres(class, "-0").expect(class), "0");
        }
    }

    #[test]
    fn uuid_forms_convert_both_ways() {
        let canonical = uuid("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90").expect("hyphenated");
        assert_eq!(canonical, "2tLrGjz6ktIRCukXDsqykS");
        assert_eq!(
            uuid_hyphenated(&canonical).expect("canonical"),
            "5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90"
        );
        assert_eq!(uuid("0").expect("nil"), "0");
        assert_eq!(
            uuid_hyphenated("0").expect("nil"),
            "00000000-0000-0000-0000-000000000000"
        );
        assert!(
            uuid("zzzzzzzzzzzzzzzzzzzzzz").is_err(),
            "wider than 128 bits"
        );
    }
}
