//! SQL values across JSON, as the facade writes them (`src/native/bridge.ts`
//! and `src/native/engine.ts`'s storage): a string, a number or null as
//! itself, an integer the engine binds as one, a number with a fraction (or
//! one written `{ "$real": n }`) a real, a bigint `{ "$bigint": "<digits>" }`
//! and a blob `{ "$blob": "<base64>" }`. A row reads back the same way.

use serde_json::{Map, Value};

use superschematic_engine::storage::{Row, RunResult, SqlValue};
use superschematic_engine::{json, Error as Failed, Result as EngineResult};

// SQL values cross as JSON, a bigint and a blob tagged ($bigint, $blob),
// a number with a fraction or past i64 a real ($real or plain).
pub(crate) fn sql_value(value: &Value) -> EngineResult<SqlValue> {
    Ok(match value {
        Value::Null => SqlValue::Null,
        Value::String(text) => SqlValue::from(text.as_str()),
        Value::Number(number) => {
            let text = json::stringify(value);
            match number.as_i64() {
                Some(integer) if !text.contains(['.', 'e', 'E']) => SqlValue::Integer(integer),
                _ => SqlValue::Real(json::number_value(number)),
            }
        }
        Value::Object(tagged) => {
            if let Some(real) = tagged.get("$real").and_then(Value::as_number) {
                SqlValue::Real(json::number_value(real))
            } else if let Some(big) = tagged.get("$bigint").and_then(Value::as_str) {
                SqlValue::Integer(big.parse::<i64>().map_err(|_| {
                    Failed::Storage(superschematic_engine::storage::Error::Value {
                        message: format!("the bigint {big} does not fit a 64-bit integer"),
                    })
                })?)
            } else if let Some(blob) = tagged.get("$blob").and_then(Value::as_str) {
                SqlValue::Blob(base64_decode(blob))
            } else {
                return Err(Failed::Usage(
                    "a SQL value is a string, a number, a bigint, a Uint8Array or null".to_owned(),
                ));
            }
        }
        _ => {
            return Err(Failed::Usage(
                "a SQL value is a string, a number, a bigint, a Uint8Array or null".to_owned(),
            ))
        }
    })
}

pub(crate) fn sql_params(value: Option<&Value>) -> EngineResult<Vec<SqlValue>> {
    value
        .and_then(Value::as_array)
        .map(|list| list.iter().map(sql_value).collect())
        .unwrap_or_else(|| Ok(Vec::new()))
}

pub(crate) fn sql_json(value: &SqlValue) -> Value {
    match value {
        SqlValue::Null => Value::Null,
        SqlValue::Integer(integer) => Value::from(*integer),
        SqlValue::Real(real) => number(*real),
        SqlValue::Text(text) => Value::from(text.as_str()),
        SqlValue::Blob(bytes) => serde_json::json!({ "$blob": base64_encode(bytes) }),
    }
}

pub(crate) fn row_json(row: &Row) -> Value {
    let mut out = Map::new();
    for (column, value) in row.iter() {
        if !out.contains_key(column) {
            out.insert(column.to_owned(), sql_json(value));
        }
    }
    Value::Object(out)
}

pub(crate) fn run_json(result: &RunResult) -> Value {
    serde_json::json!({
        "changes": result.changes,
        "lastInsertRowid": result.last_insert_rowid,
    })
}

const BASE64: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

fn base64_encode(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let n = (u32::from(chunk[0]) << 16)
            | (u32::from(*chunk.get(1).unwrap_or(&0)) << 8)
            | u32::from(*chunk.get(2).unwrap_or(&0));
        for (index, shift) in [18, 12, 6, 0].into_iter().enumerate() {
            if index <= chunk.len() {
                out.push(char::from(BASE64[((n >> shift) & 63) as usize]));
            } else {
                out.push('=');
            }
        }
    }
    out
}

fn base64_decode(text: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity(text.len() / 4 * 3);
    let mut buffer = 0u32;
    let mut bits = 0;
    for byte in text.bytes() {
        let Some(value) = BASE64.iter().position(|each| *each == byte) else {
            continue;
        };
        buffer = (buffer << 6) | value as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push(((buffer >> bits) & 0xff) as u8);
        }
    }
    out
}

// number is a finite double as JavaScript writes it.
fn number(value: f64) -> Value {
    serde_json::from_str(&json::js_number(value)).unwrap_or(Value::Null)
}
