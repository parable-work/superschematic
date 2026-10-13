//! Values bound to placeholders and read from columns, and rows as plain
//! data keyed by column name.
//!
//! A value is one of SQLite's five storage classes, as the TypeScript
//! driver's `SqlValue` is (`storage/driver.ts`): null, a 64-bit integer, a
//! finite or infinite double, text or bytes. There is no boolean, as
//! there is none in SQLite; the engine binds 0 and 1. On Postgres each
//! bound value is coerced to the type the server inferred for its
//! parameter (an integral real bound to `int8` is that integer), and each
//! column read comes back as one of these, a Postgres boolean as 0 or 1.

use std::fmt;
use std::sync::Arc;

use crate::error::{Error, Result};

/// A value bound to a `?` placeholder or read from a column.
#[derive(Clone, Debug, PartialEq)]
pub enum SqlValue {
    Null,
    Integer(i64),
    Real(f64),
    Text(String),
    Blob(Vec<u8>),
}

impl SqlValue {
    /// Whether the value is null.
    pub fn is_null(&self) -> bool {
        matches!(self, SqlValue::Null)
    }

    /// The integer, or `None` for any other value.
    pub fn as_i64(&self) -> Option<i64> {
        match self {
            SqlValue::Integer(value) => Some(*value),
            _ => None,
        }
    }

    /// The number as a double: a real, or an integer widened to one.
    pub fn as_f64(&self) -> Option<f64> {
        match self {
            SqlValue::Real(value) => Some(*value),
            // A double holds every integer the engine stores (below 2^53)
            // exactly.
            SqlValue::Integer(value) => Some(*value as f64),
            _ => None,
        }
    }

    /// The text, or `None` for any other value.
    pub fn as_str(&self) -> Option<&str> {
        match self {
            SqlValue::Text(value) => Some(value),
            _ => None,
        }
    }

    /// The bytes, or `None` for any other value.
    pub fn as_blob(&self) -> Option<&[u8]> {
        match self {
            SqlValue::Blob(value) => Some(value),
            _ => None,
        }
    }

    /// The storage class's name, as messages write it.
    pub fn kind(&self) -> &'static str {
        match self {
            SqlValue::Null => "null",
            SqlValue::Integer(_) => "an integer",
            SqlValue::Real(_) => "a real",
            SqlValue::Text(_) => "text",
            SqlValue::Blob(_) => "a blob",
        }
    }
}

impl fmt::Display for SqlValue {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            SqlValue::Null => f.write_str("NULL"),
            SqlValue::Integer(value) => write!(f, "{value}"),
            SqlValue::Real(value) => write!(f, "{value:?}"),
            SqlValue::Text(value) => write!(f, "{value:?}"),
            SqlValue::Blob(value) => write!(f, "<{} bytes>", value.len()),
        }
    }
}

macro_rules! from_integer {
    ($($t:ty),*) => {$(
        impl From<$t> for SqlValue {
            fn from(value: $t) -> Self {
                SqlValue::Integer(i64::from(value))
            }
        }
    )*};
}

from_integer!(i8, i16, i32, i64, u8, u16, u32);

impl From<f64> for SqlValue {
    fn from(value: f64) -> Self {
        SqlValue::Real(value)
    }
}

impl From<f32> for SqlValue {
    fn from(value: f32) -> Self {
        SqlValue::Real(f64::from(value))
    }
}

impl From<&str> for SqlValue {
    fn from(value: &str) -> Self {
        SqlValue::Text(value.to_owned())
    }
}

impl From<String> for SqlValue {
    fn from(value: String) -> Self {
        SqlValue::Text(value)
    }
}

impl From<&String> for SqlValue {
    fn from(value: &String) -> Self {
        SqlValue::Text(value.clone())
    }
}

impl From<Vec<u8>> for SqlValue {
    fn from(value: Vec<u8>) -> Self {
        SqlValue::Blob(value)
    }
}

impl From<&[u8]> for SqlValue {
    fn from(value: &[u8]) -> Self {
        SqlValue::Blob(value.to_vec())
    }
}

impl From<&SqlValue> for SqlValue {
    fn from(value: &SqlValue) -> Self {
        value.clone()
    }
}

impl<T: Into<SqlValue>> From<Option<T>> for SqlValue {
    fn from(value: Option<T>) -> Self {
        value.map_or(SqlValue::Null, Into::into)
    }
}

/// The parameters of one statement, each converted to a [`SqlValue`]:
/// `params![namespace, 3, None::<i64>]`.
#[macro_export]
macro_rules! params {
    () => {
        &[] as &[$crate::SqlValue]
    };
    ($($value:expr),+ $(,)?) => {
        &[$($crate::SqlValue::from($value)),+] as &[$crate::SqlValue]
    };
}

/// A row as plain data: its values by column, in the statement's column
/// order.
#[derive(Clone, Debug, PartialEq)]
pub struct Row {
    columns: Arc<[String]>,
    values: Vec<SqlValue>,
}

impl Row {
    /// A row of `values` under `columns`, one value per column.
    pub fn new(columns: Arc<[String]>, values: Vec<SqlValue>) -> Self {
        debug_assert_eq!(columns.len(), values.len());
        Row { columns, values }
    }

    /// The column names, in order.
    pub fn columns(&self) -> &[String] {
        &self.columns
    }

    /// The values, in column order.
    pub fn values(&self) -> &[SqlValue] {
        &self.values
    }

    /// The values, in column order.
    pub fn into_values(self) -> Vec<SqlValue> {
        self.values
    }

    /// Each column's name and value, in order.
    pub fn iter(&self) -> impl Iterator<Item = (&str, &SqlValue)> {
        self.columns
            .iter()
            .map(String::as_str)
            .zip(self.values.iter())
    }

    /// The value of `column`, or `None` when the row has no such column.
    /// Two columns of one name (a join) read as the first.
    pub fn get(&self, column: &str) -> Option<&SqlValue> {
        self.columns
            .iter()
            .position(|name| name == column)
            .map(|index| &self.values[index])
    }

    /// The value of `column`; a column the row lacks is an error.
    pub fn value(&self, column: &str) -> Result<&SqlValue> {
        self.get(column).ok_or_else(|| Error::Value {
            message: format!(
                "the row has no column {column}; its columns are {}",
                self.columns.join(", ")
            ),
        })
    }

    /// The integer in `column`.
    pub fn i64(&self, column: &str) -> Result<i64> {
        self.opt_i64(column)?
            .ok_or_else(|| wrong(column, "an integer", &SqlValue::Null))
    }

    /// The integer in `column`, or `None` for null.
    pub fn opt_i64(&self, column: &str) -> Result<Option<i64>> {
        match self.value(column)? {
            SqlValue::Null => Ok(None),
            SqlValue::Integer(value) => Ok(Some(*value)),
            other => Err(wrong(column, "an integer", other)),
        }
    }

    /// The number in `column` as a double.
    pub fn f64(&self, column: &str) -> Result<f64> {
        self.opt_f64(column)?
            .ok_or_else(|| wrong(column, "a number", &SqlValue::Null))
    }

    /// The number in `column` as a double, or `None` for null.
    pub fn opt_f64(&self, column: &str) -> Result<Option<f64>> {
        match self.value(column)? {
            SqlValue::Null => Ok(None),
            other => other
                .as_f64()
                .map(Some)
                .ok_or_else(|| wrong(column, "a number", other)),
        }
    }

    /// The text in `column`.
    pub fn text(&self, column: &str) -> Result<&str> {
        self.opt_text(column)?
            .ok_or_else(|| wrong(column, "text", &SqlValue::Null))
    }

    /// The text in `column`, or `None` for null.
    pub fn opt_text(&self, column: &str) -> Result<Option<&str>> {
        match self.value(column)? {
            SqlValue::Null => Ok(None),
            SqlValue::Text(value) => Ok(Some(value)),
            other => Err(wrong(column, "text", other)),
        }
    }

    /// The bytes in `column`, or `None` for null.
    pub fn opt_blob(&self, column: &str) -> Result<Option<&[u8]>> {
        match self.value(column)? {
            SqlValue::Null => Ok(None),
            SqlValue::Blob(value) => Ok(Some(value)),
            other => Err(wrong(column, "a blob", other)),
        }
    }
}

fn wrong(column: &str, wanted: &str, got: &SqlValue) -> Error {
    Error::Value {
        message: format!("column {column} holds {}, not {wanted}", got.kind()),
    }
}

/// The outcome of a statement that writes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RunResult {
    /// The rows the statement inserted, updated or deleted.
    pub changes: u64,
    /// SQLite's rowid of the last row an INSERT on the connection added;
    /// `None` on Postgres, where a statement returns the key it needs with
    /// RETURNING.
    pub last_insert_rowid: Option<i64>,
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn values_convert_from_rust_types() {
        assert_eq!(SqlValue::from(3_i32), SqlValue::Integer(3));
        assert_eq!(SqlValue::from(1.5), SqlValue::Real(1.5));
        assert_eq!(SqlValue::from("a"), SqlValue::Text("a".into()));
        assert_eq!(SqlValue::from(None::<i64>), SqlValue::Null);
        assert_eq!(SqlValue::from(Some("b")), SqlValue::Text("b".into()));
        assert_eq!(SqlValue::from(&[1_u8, 2][..]), SqlValue::Blob(vec![1, 2]));
        assert_eq!(
            params![1, "x", None::<i64>],
            &[
                SqlValue::Integer(1),
                SqlValue::Text("x".into()),
                SqlValue::Null
            ]
        );
        assert!(params![].is_empty());
    }

    #[test]
    fn a_row_reads_by_column_name() {
        let row = Row::new(
            Arc::from(vec!["id".to_owned(), "n".to_owned(), "gone".to_owned()]),
            vec![SqlValue::from("n1"), SqlValue::from(4), SqlValue::Null],
        );
        assert_eq!(row.text("id").unwrap(), "n1");
        assert_eq!(row.i64("n").unwrap(), 4);
        assert_eq!(row.f64("n").unwrap(), 4.0);
        assert_eq!(row.opt_text("gone").unwrap(), None);
        assert!(row.i64("gone").is_err());
        assert!(row.text("n").is_err());
        assert!(row.value("missing").is_err());
        assert_eq!(
            row.iter().map(|(name, _)| name).collect::<Vec<_>>(),
            ["id", "n", "gone"]
        );
    }
}
