//! A JSON decoder and encoder that accept, refuse and write what Go's
//! `encoding/json` does for a value decoded with `UseNumber` into `any`,
//! which is what the Go runner hashes (`runtime/migrate/README.md`,
//! "Canonical JSON and hashes").
//!
//! The decoder keeps a number as its literal, keeps the last of two equal
//! keys, turns a byte that is not UTF-8 and a surrogate escape without its
//! pair into U+FFFD, and refuses a raw control character in a string. The
//! encoder writes compact JSON with object members sorted by the bytes of
//! their keys, and escapes `<`, `>`, `&`, U+2028 and U+2029.
//!
//! One difference: Go's decoder refuses nesting deeper than 10000, and this
//! one refuses nesting deeper than [`MAX_DEPTH`], so a hostile document
//! cannot exhaust the stack. No plan or model nests that deep.

use std::collections::BTreeMap;
use std::fmt;

/// The deepest nesting of arrays and objects the decoder reads.
pub(crate) const MAX_DEPTH: usize = 512;

/// A JSON value as the decoder reads it. A number is its literal, as
/// written. An object's members sort by the bytes of their keys, which is
/// the order canonical JSON writes them in.
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum Value {
    Null,
    Bool(bool),
    Number(String),
    String(String),
    Array(Vec<Value>),
    Object(BTreeMap<String, Value>),
}

/// A document that is not one JSON value: what is wrong, and the byte
/// offset it was found at.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct JsonError {
    message: String,
    offset: usize,
}

impl JsonError {
    pub(crate) fn new(message: impl Into<String>, offset: usize) -> Self {
        JsonError {
            message: message.into(),
            offset,
        }
    }

    /// The byte offset in the document the error was found at.
    pub fn offset(&self) -> usize {
        self.offset
    }
}

impl fmt::Display for JsonError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for JsonError {}

/// Reads one JSON value and refuses anything but whitespace after it.
pub(crate) fn parse(raw: &[u8]) -> Result<Value, JsonError> {
    let mut parser = Parser { raw, at: 0 };
    parser.whitespace();
    let value = parser.value(0)?;
    parser.whitespace();
    if parser.at < raw.len() {
        return Err(JsonError::new("trailing data after JSON value", parser.at));
    }
    Ok(value)
}

struct Parser<'a> {
    raw: &'a [u8],
    at: usize,
}

impl Parser<'_> {
    fn peek(&self) -> Option<u8> {
        self.raw.get(self.at).copied()
    }

    fn whitespace(&mut self) {
        while matches!(self.peek(), Some(b' ' | b'\t' | b'\r' | b'\n')) {
            self.at += 1;
        }
    }

    /// The error for the byte at the cursor, or for the end of the input.
    fn unexpected(&self, context: &str) -> JsonError {
        match self.peek() {
            None => JsonError::new("unexpected end of JSON input", self.at),
            Some(byte) => JsonError::new(
                format!(
                    "invalid character {:?} {context}",
                    char::from_u32(u32::from(byte)).unwrap_or(char::REPLACEMENT_CHARACTER)
                ),
                self.at,
            ),
        }
    }

    fn value(&mut self, depth: usize) -> Result<Value, JsonError> {
        match self.peek() {
            Some(b'{') => self.object(depth + 1),
            Some(b'[') => self.array(depth + 1),
            Some(b'"') => self.string().map(Value::String),
            Some(b't') => self.literal("true", Value::Bool(true)),
            Some(b'f') => self.literal("false", Value::Bool(false)),
            Some(b'n') => self.literal("null", Value::Null),
            Some(b'-' | b'0'..=b'9') => self.number(),
            _ => Err(self.unexpected("looking for beginning of value")),
        }
    }

    fn literal(&mut self, word: &str, value: Value) -> Result<Value, JsonError> {
        for expected in word.bytes() {
            if self.peek() != Some(expected) {
                return Err(self.unexpected(&format!("in literal {word}")));
            }
            self.at += 1;
        }
        Ok(value)
    }

    /// A number in JSON's grammar, kept as written.
    fn number(&mut self) -> Result<Value, JsonError> {
        let start = self.at;
        if self.peek() == Some(b'-') {
            self.at += 1;
        }
        match self.peek() {
            Some(b'0') => self.at += 1,
            Some(b'1'..=b'9') => self.digits(),
            _ => return Err(self.unexpected("in numeric literal")),
        }
        if self.peek() == Some(b'.') {
            self.at += 1;
            self.some_digits()?;
        }
        if matches!(self.peek(), Some(b'e' | b'E')) {
            self.at += 1;
            if matches!(self.peek(), Some(b'+' | b'-')) {
                self.at += 1;
            }
            self.some_digits()?;
        }
        // A digit after a leading zero ends the number, and what follows a
        // value decides whether it is an error.
        let literal = String::from_utf8_lossy(&self.raw[start..self.at]).into_owned();
        Ok(Value::Number(literal))
    }

    fn digits(&mut self) {
        while matches!(self.peek(), Some(b'0'..=b'9')) {
            self.at += 1;
        }
    }

    fn some_digits(&mut self) -> Result<(), JsonError> {
        if !matches!(self.peek(), Some(b'0'..=b'9')) {
            return Err(self.unexpected("in numeric literal"));
        }
        self.digits();
        Ok(())
    }

    fn array(&mut self, depth: usize) -> Result<Value, JsonError> {
        self.open(depth)?;
        let mut items = Vec::new();
        self.whitespace();
        if self.peek() == Some(b']') {
            self.at += 1;
            return Ok(Value::Array(items));
        }
        loop {
            self.whitespace();
            items.push(self.value(depth)?);
            self.whitespace();
            match self.peek() {
                Some(b',') => self.at += 1,
                Some(b']') => {
                    self.at += 1;
                    return Ok(Value::Array(items));
                }
                _ => return Err(self.unexpected("after array element")),
            }
        }
    }

    fn object(&mut self, depth: usize) -> Result<Value, JsonError> {
        self.open(depth)?;
        let mut members = BTreeMap::new();
        self.whitespace();
        if self.peek() == Some(b'}') {
            self.at += 1;
            return Ok(Value::Object(members));
        }
        loop {
            self.whitespace();
            if self.peek() != Some(b'"') {
                return Err(self.unexpected("looking for beginning of object key string"));
            }
            let key = self.string()?;
            self.whitespace();
            if self.peek() != Some(b':') {
                return Err(self.unexpected("after object key"));
            }
            self.at += 1;
            self.whitespace();
            let value = self.value(depth)?;
            // The last of two equal keys stays, as in Go's map.
            members.insert(key, value);
            self.whitespace();
            match self.peek() {
                Some(b',') => self.at += 1,
                Some(b'}') => {
                    self.at += 1;
                    return Ok(Value::Object(members));
                }
                _ => return Err(self.unexpected("after object key:value pair")),
            }
        }
    }

    /// Steps over an opening bracket or brace, refusing one nested too deep.
    fn open(&mut self, depth: usize) -> Result<(), JsonError> {
        if depth > MAX_DEPTH {
            return Err(JsonError::new("exceeded max depth", self.at));
        }
        self.at += 1;
        Ok(())
    }

    fn string(&mut self) -> Result<String, JsonError> {
        self.at += 1;
        let mut out = String::new();
        let mut run = self.at;
        loop {
            match self.peek() {
                None => return Err(self.unexpected("in string literal")),
                Some(b'"') => {
                    push_utf8(&mut out, &self.raw[run..self.at]);
                    self.at += 1;
                    return Ok(out);
                }
                Some(b'\\') => {
                    push_utf8(&mut out, &self.raw[run..self.at]);
                    self.at += 1;
                    self.escape(&mut out)?;
                    run = self.at;
                }
                Some(byte) if byte < 0x20 => return Err(self.unexpected("in string literal")),
                Some(_) => self.at += 1,
            }
        }
    }

    /// Reads the escape after a backslash into `out`.
    fn escape(&mut self, out: &mut String) -> Result<(), JsonError> {
        let unescaped = match self.peek() {
            Some(b'"') => '"',
            Some(b'\\') => '\\',
            Some(b'/') => '/',
            Some(b'b') => '\u{8}',
            Some(b'f') => '\u{c}',
            Some(b'n') => '\n',
            Some(b'r') => '\r',
            Some(b't') => '\t',
            Some(b'u') => {
                self.at += 1;
                let unit = self.hex4()?;
                out.push(self.code_point(unit));
                return Ok(());
            }
            _ => return Err(self.unexpected("in string escape code")),
        };
        self.at += 1;
        out.push(unescaped);
        Ok(())
    }

    /// The four hex digits of a `\u` escape.
    fn hex4(&mut self) -> Result<u32, JsonError> {
        let mut unit = 0;
        for _ in 0..4 {
            let digit = self
                .peek()
                .and_then(|byte| char::from(byte).to_digit(16))
                .ok_or_else(|| self.unexpected("in \\u hexadecimal character escape"))?;
            unit = unit * 16 + digit;
            self.at += 1;
        }
        Ok(unit)
    }

    /// The character a `\u` escape's UTF-16 unit stands for. A high
    /// surrogate takes the low surrogate escape after it; a surrogate
    /// without its pair is U+FFFD, and the escape after it is read on its
    /// own.
    fn code_point(&mut self, unit: u32) -> char {
        if let Some(character) = char::from_u32(unit) {
            return character;
        }
        if (0xD800..0xDC00).contains(&unit) {
            if let Some(low) = self.low_surrogate() {
                let code = 0x10000 + ((unit - 0xD800) << 10) + (low - 0xDC00);
                if let Some(character) = char::from_u32(code) {
                    self.at += 6;
                    return character;
                }
            }
        }
        char::REPLACEMENT_CHARACTER
    }

    /// The low surrogate a `\uXXXX` escape at the cursor holds, if one is
    /// there. It does not move the cursor.
    fn low_surrogate(&self) -> Option<u32> {
        let escape = self.raw.get(self.at..self.at + 6)?;
        if &escape[..2] != b"\\u" {
            return None;
        }
        let mut unit = 0;
        for &byte in &escape[2..] {
            unit = unit * 16 + char::from(byte).to_digit(16)?;
        }
        (0xDC00..0xE000).contains(&unit).then_some(unit)
    }
}

/// Appends the bytes of a string literal between two escapes. Each byte
/// that is not part of a UTF-8 sequence becomes U+FFFD, one per byte, as
/// Go's decoder writes it.
fn push_utf8(out: &mut String, bytes: &[u8]) {
    if let Ok(text) = std::str::from_utf8(bytes) {
        out.push_str(text);
        return;
    }
    let mut at = 0;
    while at < bytes.len() {
        let width = match bytes[at] {
            0x00..=0x7F => 1,
            0xC0..=0xDF => 2,
            0xE0..=0xEF => 3,
            _ => 4,
        };
        match bytes
            .get(at..at + width)
            .and_then(|sequence| std::str::from_utf8(sequence).ok())
        {
            Some(text) => {
                out.push_str(text);
                at += width;
            }
            None => {
                out.push(char::REPLACEMENT_CHARACTER);
                at += 1;
            }
        }
    }
}

/// Appends `value` in canonical form.
pub(crate) fn write(value: &Value, out: &mut String) {
    match value {
        Value::Null => out.push_str("null"),
        Value::Bool(true) => out.push_str("true"),
        Value::Bool(false) => out.push_str("false"),
        Value::Number(literal) => out.push_str(literal),
        Value::String(text) => write_string(text, out),
        Value::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write(item, out);
            }
            out.push(']');
        }
        Value::Object(members) => write_object(members.iter(), out),
    }
}

/// Appends an object of `members`, which come sorted by key, in canonical
/// form.
pub(crate) fn write_object<'a>(
    members: impl Iterator<Item = (&'a String, &'a Value)>,
    out: &mut String,
) {
    out.push('{');
    for (i, (key, value)) in members.enumerate() {
        if i > 0 {
            out.push(',');
        }
        write_string(key, out);
        out.push(':');
        write(value, out);
    }
    out.push('}');
}

/// Appends a string as Go's encoder writes it with HTML escaping on.
fn write_string(text: &str, out: &mut String) {
    out.push('"');
    for character in text.chars() {
        match character {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '\u{0}'..='\u{1f}' | '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", u32::from(character)));
            }
            other => out.push(other),
        }
    }
    out.push('"');
}

#[cfg(test)]
mod tests {
    use super::*;

    fn canonical(raw: &[u8]) -> String {
        let mut out = String::new();
        write(&parse(raw).expect("the document reads"), &mut out);
        out
    }

    #[test]
    fn a_surrogate_without_its_pair_is_the_replacement_character() {
        // A lone high surrogate, a lone low one, a high one before an escape
        // that is no low surrogate, and a pair.
        assert_eq!(canonical(br#""\ud83d""#), "\"\u{fffd}\"");
        assert_eq!(canonical(br#""\ude00""#), "\"\u{fffd}\"");
        assert_eq!(canonical(br#""\ud83d\u0041""#), "\"\u{fffd}A\"");
        assert_eq!(canonical(br#""\ud83dx""#), "\"\u{fffd}x\"");
        assert_eq!(canonical(br#""\ud83d\ude00""#), "\"\u{1f600}\"");
    }

    #[test]
    fn each_byte_that_is_not_utf8_is_the_replacement_character() {
        // A truncated three-byte sequence is two bytes, so two characters.
        assert_eq!(canonical(b"\"a\xe2\x82b\""), "\"a\u{fffd}\u{fffd}b\"");
        assert_eq!(canonical(b"\"\xff\xc3\xa9\""), "\"\u{fffd}\u{e9}\"");
    }

    #[test]
    fn nesting_past_the_limit_is_refused() {
        let deep = |depth: usize| [vec![b'['; depth], vec![b']'; depth]].concat();
        assert!(parse(&deep(MAX_DEPTH)).is_ok());
        let err = parse(&deep(MAX_DEPTH + 1)).expect_err("too deep");
        assert_eq!(err.to_string(), "exceeded max depth");
        assert_eq!(err.offset(), MAX_DEPTH);
    }

    #[test]
    fn a_syntax_error_names_the_byte() {
        let err = parse(br#"{"a":}"#).expect_err("a missing value");
        assert_eq!(
            err.to_string(),
            "invalid character '}' looking for beginning of value"
        );
        assert_eq!(err.offset(), 5);
        assert_eq!(
            parse(b"[1").expect_err("an open array").to_string(),
            "unexpected end of JSON input"
        );
    }
}
