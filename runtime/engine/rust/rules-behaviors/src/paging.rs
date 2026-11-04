//! Paging for a behavior's list operations (D16, D58), ported from the
//! engine's (`../engine/src/behaviors/paging.rs`) word for word: the
//! engine's page size (a paramsSchema bounds `limit` to 1-500, and 50 is
//! the default) and an opaque cursor over a number the behavior gives each
//! record (Approvals' sign-off number). A page is the records after the
//! cursor's number, `limit + 1` of them read so a cursor is written only
//! when more follow.
//!
//! The cursor is `after:<number>` in unpadded base64url, as Node.js writes
//! it, and is read as Node.js's `Buffer.from(cursor, 'base64url')` reads
//! it: a character outside the alphabet is passed over and the first `=`
//! ends it, so a cursor the engine wrote is taken here.

use serde_json::{Map, Value};
use superschematic_rules::SchemaIssue;

/// The records a page holds when the parameters give no `limit`.
pub(crate) const DEFAULT_PAGE_SIZE: i64 = 50;

/// What a list operation's parameters ask for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) struct PageRequest {
    /// How many records the page holds: `limit`, 50 when absent.
    pub limit: i64,
    /// The number the page starts after: 0 for the first page.
    pub after: i64,
}

/// One page of a list operation's result.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Page<T> {
    pub items: Vec<T>,
    /// The cursor of the next page; `None` after the last.
    pub next: Option<String>,
}

impl Page<Value> {
    /// The page as a list operation returns it: `{ items, next }`, `next`
    /// null after the last page.
    pub fn into_json(self) -> Value {
        let mut out = Map::new();
        out.insert("items".into(), Value::Array(self.items));
        out.insert("next".into(), self.next.map_or(Value::Null, Value::String));
        Value::Object(out)
    }
}

/// Reads a list operation's `limit` and `cursor` (`pageRequest`). A cursor
/// this operation did not write is the issue `/cursor`, which the caller
/// refuses as the operation's parameters.
pub(crate) fn page_request(params: &Value) -> Result<PageRequest, SchemaIssue> {
    // The paramsSchema holds limit to an integer, which JSON may write as
    // 2 or 2.0.
    let limit = match params.get("limit") {
        Some(Value::Number(number)) => number.as_f64().unwrap_or_default() as i64,
        _ => DEFAULT_PAGE_SIZE,
    };
    let Some(cursor) = params.get("cursor").and_then(Value::as_str) else {
        return Ok(PageRequest { limit, after: 0 });
    };
    let decoded = String::from_utf8_lossy(&base64url_decode(cursor)).into_owned();
    match decoded.strip_prefix("after:") {
        Some(digits)
            if (1..=15).contains(&digits.len()) && digits.bytes().all(|b| b.is_ascii_digit()) =>
        {
            Ok(PageRequest {
                limit,
                after: digits.parse().unwrap_or(0),
            })
        }
        _ => Err(SchemaIssue::new(
            "/cursor",
            "is not a cursor this operation returned",
        )),
    }
}

/// The first `limit` of rows read with a limit of `limit + 1`, and the
/// cursor after the last of them when more follow (`page`).
pub(crate) fn page<T>(mut rows: Vec<T>, limit: i64, number_of: impl Fn(&T) -> i64) -> Page<T> {
    let limit = usize::try_from(limit).unwrap_or(0);
    let more = rows.len() > limit;
    rows.truncate(limit);
    let next = if more {
        rows.last()
            .map(|last| base64url_encode(format!("after:{}", number_of(last)).as_bytes()))
    } else {
        None
    };
    Page { items: rows, next }
}

const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";

/// Bytes in unpadded base64url, as Node.js's `toString('base64url')`.
pub(crate) fn base64url_encode(bytes: &[u8]) -> String {
    let mut out = String::with_capacity(bytes.len().div_ceil(3) * 4);
    for chunk in bytes.chunks(3) {
        let group = chunk.iter().enumerate().fold(0u32, |group, (at, byte)| {
            group | (u32::from(*byte) << (16 - 8 * at))
        });
        for at in 0..=chunk.len() {
            out.push(char::from(
                ALPHABET[((group >> (18 - 6 * at)) & 63) as usize],
            ));
        }
    }
    out
}

// sextet is a base64 character's value; both alphabets are read, as
// Node.js reads either under either name.
fn sextet(c: u8) -> Option<u32> {
    match c {
        b'A'..=b'Z' => Some(u32::from(c - b'A')),
        b'a'..=b'z' => Some(u32::from(c - b'a') + 26),
        b'0'..=b'9' => Some(u32::from(c - b'0') + 52),
        b'+' | b'-' => Some(62),
        b'/' | b'_' => Some(63),
        _ => None,
    }
}

/// Base64 or base64url as Node.js's `Buffer.from(text, 'base64url')`
/// reads it: characters outside the alphabets are passed over, the first
/// `=` ends the input, a lone trailing character is dropped, and no more
/// bytes come out than the text's length allows.
pub(crate) fn base64url_decode(text: &str) -> Vec<u8> {
    let bytes = text.as_bytes();
    let mut size = bytes.len();
    if size < 2 {
        return Vec::new();
    }
    if bytes[size - 1] == b'=' {
        size -= 1;
        if bytes[size - 1] == b'=' {
            size -= 1;
        }
    }
    let capacity = size / 4 * 3
        + match size % 4 {
            0 => 0,
            1 if size < 4 => 0,
            remainder => 1 + usize::from(remainder == 3),
        };
    let values: Vec<u32> = bytes
        .iter()
        .take_while(|c| **c != b'=')
        .filter_map(|c| sextet(*c))
        .collect();
    let mut out = Vec::with_capacity(capacity);
    for group in values.chunks(4) {
        let joined = group.iter().enumerate().fold(0u32, |joined, (at, value)| {
            joined | (value << (18 - 6 * at))
        });
        for at in 0..group.len().saturating_sub(1) {
            out.push(((joined >> (16 - 8 * at)) & 0xff) as u8);
        }
    }
    out.truncate(capacity);
    out
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn a_cursor_reads_as_nodejs_writes_and_reads_it() {
        // Buffer.from('after:2', 'utf8').toString('base64url')
        assert_eq!(base64url_encode(b"after:2"), "YWZ0ZXI6Mg");
        assert_eq!(base64url_encode(b"after:123"), "YWZ0ZXI6MTIz");
        assert_eq!(base64url_decode("YWZ0ZXI6Mg"), b"after:2");
        assert_eq!(base64url_decode("YWZ0ZXI6Mg=="), b"after:2");
        assert_eq!(base64url_decode("YWZ0 ZXI6\nMg"), b"after:2");
        assert_eq!(base64url_decode("YWZ0ZXI6Mg=garbage"), b"after:2");
        assert_eq!(base64url_decode("Y"), b"");
        for bytes in [&b""[..], b"a", b"ab", b"abc", b"\xff\xfe\x00"] {
            assert_eq!(base64url_decode(&base64url_encode(bytes)), bytes);
        }
    }

    #[test]
    fn a_page_takes_limit_records_and_a_cursor_after_the_last() {
        let first = page(vec![1, 2, 3], 2, |n| *n);
        assert_eq!(first.items, [1, 2]);
        let request =
            page_request(&json!({ "limit": 2, "cursor": first.next.clone().unwrap() })).unwrap();
        assert_eq!(request, PageRequest { limit: 2, after: 2 });
        assert_eq!(page(vec![1, 2], 2, |n| *n).next, None);
        assert_eq!(
            page_request(&json!({})).unwrap(),
            PageRequest {
                limit: 50,
                after: 0
            }
        );
        assert_eq!(
            page_request(&json!({ "cursor": "bogus" })).unwrap_err(),
            SchemaIssue::new("/cursor", "is not a cursor this operation returned")
        );
        let long = base64url_encode(b"after:1234567890123456");
        assert!(page_request(&json!({ "cursor": long })).is_err());
    }
}
