//! A request's path and query parameters, as the HTTP runtime reads them
//! (`runtime/http/typescript/src/params.ts`, the Hono adapter's routing):
//! a path segment percent-decoded once (an escape it cannot decode left as
//! it is), the query as `URLSearchParams` reads it, and each parameter
//! decoded to its kind or refused with 400 and a field-level detail.

use serde_json::{json, Value};

use super::problems::Problem;

/// Where a parameter comes from.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Location {
    Path,
    Query,
}

impl Location {
    fn as_str(self) -> &'static str {
        match self {
            Location::Path => "path",
            Location::Query => "query",
        }
    }
}

/// The kinds the engine's routes declare.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Kind {
    String,
    Integer,
    Boolean,
}

/// A decoded parameter.
#[derive(Clone, Debug, PartialEq)]
pub(crate) enum Param {
    Text(String),
    Integer(i64),
    Flag(bool),
    List(Vec<String>),
}

fn refuse(location: Location, name: &str, reason: &str) -> Problem {
    Problem::bad_request(format!(
        "Invalid {} parameter {name}: {reason}",
        location.as_str()
    ))
    .with_details(json!({"location": location.as_str(), "parameter": name, "reason": reason}))
}

/// Decodes one parameter from every occurrence on the wire: a list takes
/// repeated keys and comma-separated values; a single value takes the
/// first occurrence, and an empty one is absent unless it is a string.
pub(crate) fn decode(
    location: Location,
    name: &str,
    kind: Kind,
    required: bool,
    list: bool,
    raw: &[String],
) -> Result<Option<Param>, Problem> {
    if list {
        let items: Vec<String> = raw
            .iter()
            .flat_map(|value| value.split(','))
            .map(str::trim)
            .filter(|value| !value.is_empty())
            .map(str::to_owned)
            .collect();
        if items.is_empty() {
            if required {
                return Err(refuse(location, name, "required"));
            }
            return Ok(None);
        }
        return Ok(Some(Param::List(items)));
    }
    let Some(first) = raw.first() else {
        if required {
            return Err(refuse(location, name, "required"));
        }
        return Ok(None);
    };
    if first.is_empty() && kind != Kind::String {
        if required {
            return Err(refuse(location, name, "required"));
        }
        return Ok(None);
    }
    match kind {
        Kind::String => Ok(Some(Param::Text(first.clone()))),
        Kind::Integer => {
            let text = first.trim();
            let digits = text.strip_prefix(['+', '-']).unwrap_or(text);
            if digits.is_empty() || !digits.bytes().all(|b| b.is_ascii_digit()) {
                return Err(refuse(location, name, "expected an integer"));
            }
            match text.parse::<i64>() {
                Ok(value) if value.abs() <= 9_007_199_254_740_991 => {
                    Ok(Some(Param::Integer(value)))
                }
                _ => Err(refuse(location, name, "integer out of range")),
            }
        }
        Kind::Boolean => match first.trim().to_lowercase().as_str() {
            "true" | "1" => Ok(Some(Param::Flag(true))),
            "false" | "0" => Ok(Some(Param::Flag(false))),
            _ => Err(refuse(location, name, "expected true or false")),
        },
    }
}

/// A query string as `URLSearchParams` reads it: pairs split on `&`, each
/// on its first `=`, `+` a space, and each side percent-decoded, an escape
/// it cannot decode left as it is.
pub(crate) fn parse_query(query: &str) -> Vec<(String, String)> {
    query
        .split('&')
        .filter(|pair| !pair.is_empty())
        .map(|pair| {
            let (name, value) = pair.split_once('=').unwrap_or((pair, ""));
            (
                decode_lossy(&name.replace('+', " ")),
                decode_lossy(&value.replace('+', " ")),
            )
        })
        .collect()
}

/// Every value of a query key, in order.
pub(crate) fn all(query: &[(String, String)], name: &str) -> Vec<String> {
    query
        .iter()
        .filter(|(key, _)| key == name)
        .map(|(_, value)| value.clone())
        .collect()
}

// decode_lossy percent-decodes as URLSearchParams does: each run of escapes
// as UTF-8, a byte sequence that is not UTF-8 replaced, an escape that is
// not one kept as text.
fn decode_lossy(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] == b'%' && index + 2 < bytes.len() {
            if let (Some(high), Some(low)) = (hex(bytes[index + 1]), hex(bytes[index + 2])) {
                out.push(high * 16 + low);
                index += 3;
                continue;
            }
        }
        out.push(bytes[index]);
        index += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

fn hex(byte: u8) -> Option<u8> {
    match byte {
        b'0'..=b'9' => Some(byte - b'0'),
        b'a'..=b'f' => Some(byte - b'a' + 10),
        b'A'..=b'F' => Some(byte - b'A' + 10),
        _ => None,
    }
}

/// A path segment as the router hands it to a route: percent-decoded once
/// as `decodeURIComponent` does, or, when that fails (bad hex, or bytes
/// that are not UTF-8), left as it is.
pub(crate) fn decode_segment(segment: &str) -> String {
    strict_decode(segment).unwrap_or_else(|| segment.to_owned())
}

/// `decodeURIComponent`: every escape two hex digits, the bytes UTF-8;
/// `None` when it throws.
pub(crate) fn strict_decode(text: &str) -> Option<String> {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut index = 0;
    while index < bytes.len() {
        if bytes[index] == b'%' {
            let high = bytes.get(index + 1).copied().and_then(hex)?;
            let low = bytes.get(index + 2).copied().and_then(hex)?;
            out.push(high * 16 + low);
            index += 3;
            continue;
        }
        out.push(bytes[index]);
        index += 1;
    }
    String::from_utf8(out).ok()
}

/// A JSON value of a query parameter that carries a JSON object: a list's
/// `where`, a lookup's `key`. Anything else is 400.
pub(crate) fn json_object(name: &str, text: &str) -> Result<Value, Problem> {
    match serde_json::from_str::<Value>(text) {
        Ok(value @ Value::Object(_)) => Ok(value),
        _ => Err(Problem::bad_request(format!(
            "{name} is a JSON object of field values, by field"
        ))),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_query_and_a_segment_decode_as_the_runtime_decodes_them() {
        assert_eq!(
            parse_query("a=1&b=x+y&c=%41%ZZ&d"),
            vec![
                ("a".to_owned(), "1".to_owned()),
                ("b".to_owned(), "x y".to_owned()),
                ("c".to_owned(), "A%ZZ".to_owned()),
                ("d".to_owned(), String::new()),
            ]
        );
        assert_eq!(decode_segment("o%3A1"), "o:1");
        assert_eq!(decode_segment("a%2541"), "a%41");
        assert_eq!(decode_segment("%ZZ"), "%ZZ");
        assert_eq!(strict_decode("%ZZ"), None);
        assert_eq!(strict_decode("%E2%82"), None);
        assert_eq!(
            decode(
                Location::Query,
                "limit",
                Kind::Integer,
                false,
                false,
                &["two".into()]
            ),
            Err(refuse(Location::Query, "limit", "expected an integer"))
        );
        assert_eq!(
            decode(
                Location::Query,
                "kind",
                Kind::String,
                false,
                true,
                &["create,update".into(), "delete".into()]
            ),
            Ok(Some(Param::List(vec![
                "create".into(),
                "update".into(),
                "delete".into()
            ])))
        );
    }
}
