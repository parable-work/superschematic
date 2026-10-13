//! JSON with its number literals kept, and the canonical form the Go IR
//! persists extension and document data in (`ir.CanonicalJSON`): compact,
//! object keys sorted by their UTF-8 bytes, arrays in order, number literals
//! as written, strings escaped as Go's encoding/json escapes them.
//!
//! The reader is the TypeScript loader's `JSON.parse` with source text
//! access: JSON's syntax, a repeated key keeping its first place and its
//! last value, and a `\u` escape that is half a surrogate pair read as
//! U+FFFD, as Go reads it. It also notes the first repeated key, by its
//! JSON pointer, which the loader refuses, as Go's reader does.
//!
//! It words a syntax error as Node 24's `JSON.parse` (V8's JSON parser)
//! does, at the same character of the same text: a position and a column
//! count UTF-16 code units, lines break at `\n`, `\r` and `\r\n`, and an
//! unexpected token quotes the text, or ten code units either side of the
//! token when the text is 21 units or more. tests/json_syntax_errors.rs
//! holds Node's message for each case. A Rust string holds no lone
//! surrogate, so where V8's message holds half a pair (the token is a
//! character past U+FFFF, or the quoted context cuts one) this one holds
//! U+FFFD. V8 nests without bound; this reader refuses more than
//! `MAX_DEPTH` levels, in its own words.

use std::collections::HashMap;
use std::fmt;

use serde_json::{Map, Number, Value};

use crate::jsnum::js_number;

/// How deep the reader nests objects and arrays. A schema file nests a few
/// levels; the bound keeps a hostile one from exhausting the stack.
const MAX_DEPTH: usize = 1000;

/// A JSON value, its numbers kept as the literals they were written as and
/// its objects' members in the order they were first written.
#[derive(Debug, Clone, PartialEq)]
pub(crate) enum Node {
    Null,
    Bool(bool),
    Number(String),
    String(String),
    Array(Vec<Node>),
    Object(Vec<(String, Node)>),
}

impl Node {
    pub(crate) fn as_object(&self) -> Option<&[(String, Node)]> {
        match self {
            Node::Object(members) => Some(members),
            _ => None,
        }
    }

    pub(crate) fn as_object_mut(&mut self) -> Option<&mut Vec<(String, Node)>> {
        match self {
            Node::Object(members) => Some(members),
            _ => None,
        }
    }

    pub(crate) fn as_array(&self) -> Option<&[Node]> {
        match self {
            Node::Array(items) => Some(items),
            _ => None,
        }
    }

    pub(crate) fn as_array_mut(&mut self) -> Option<&mut Vec<Node>> {
        match self {
            Node::Array(items) => Some(items),
            _ => None,
        }
    }

    /// The member key holds, for an object.
    pub(crate) fn get(&self, key: &str) -> Option<&Node> {
        self.as_object()?
            .iter()
            .find(|(name, _)| name == key)
            .map(|(_, value)| value)
    }

    pub(crate) fn get_mut(&mut self, key: &str) -> Option<&mut Node> {
        self.as_object_mut()?
            .iter_mut()
            .find(|(name, _)| name == key)
            .map(|(_, value)| value)
    }

    pub(crate) fn has(&self, key: &str) -> bool {
        self.get(key).is_some()
    }

    /// Removes the member key, for an object.
    pub(crate) fn remove(&mut self, key: &str) {
        if let Some(members) = self.as_object_mut() {
            members.retain(|(name, _)| name != key);
        }
    }

    /// Sets the member key, in its place when the object has it, else at
    /// the end.
    pub(crate) fn set(&mut self, key: &str, value: Node) {
        if let Some(members) = self.as_object_mut() {
            match members.iter_mut().find(|(name, _)| name == key) {
                Some(member) => member.1 = value,
                None => members.push((key.to_owned(), value)),
            }
        }
    }

    /// The value with plain JSON numbers, which serde_json reads from each
    /// literal: exactly with its `arbitrary_precision` feature, as a float64
    /// past the integer range without it.
    pub(crate) fn to_value(&self) -> Value {
        match self {
            Node::Null => Value::Null,
            Node::Bool(flag) => Value::Bool(*flag),
            Node::Number(literal) => literal.parse::<Number>().map_or(Value::Null, Value::Number),
            Node::String(text) => Value::String(text.clone()),
            Node::Array(items) => Value::Array(items.iter().map(Node::to_value).collect()),
            Node::Object(members) => Value::Object(
                members
                    .iter()
                    .map(|(key, value)| (key.clone(), value.to_value()))
                    .collect::<Map<String, Value>>(),
            ),
        }
    }
}

/// How many UTF-16 code units of context V8 quotes on each side of an
/// unexpected token (`kMaxContextCharacters`).
const CONTEXT: usize = 10;

/// The length from which V8 quotes the context around an unexpected token
/// rather than the whole text (`kMinOriginalSourceLengthForContext`).
const MIN_LENGTH_FOR_CONTEXT: usize = 2 * CONTEXT + 1;

/// Texts V8 names whole when it meets an unexpected token in one: what
/// `JSON.parse` gets from `undefined`, `NaN`, `Infinity` and `{}`.
const SPECIAL_TEXTS: [&str; 4] = ["[object Object]", "undefined", "Infinity", "NaN"];

/// JSON text the reader refuses, with the reason Node's `JSON.parse` gives.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct JsonSyntaxError {
    message: String,
}

impl JsonSyntaxError {
    fn new(message: String) -> Self {
        Self { message }
    }

    /// The message, as Node 24's `JSON.parse` words it.
    pub fn message(&self) -> &str {
        &self.message
    }
}

impl fmt::Display for JsonSyntaxError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

impl std::error::Error for JsonSyntaxError {}

/// A parsed document and the first repeated key in it, by text order.
pub(crate) struct Parsed {
    pub(crate) node: Node,
    /// The repeated key and the JSON pointer of the member that repeats it.
    pub(crate) repeated: Option<(String, String)>,
}

/// Reads JSON text into a [`Node`].
pub(crate) fn parse(text: &str) -> Result<Parsed, JsonSyntaxError> {
    let mut reader = Reader {
        text: text.as_bytes(),
        source: text,
        at: 0,
        path: Vec::new(),
        repeated: None,
    };
    reader.skip_space();
    let node = reader.value(0)?;
    reader.skip_space();
    if reader.at < reader.text.len() {
        return Err(JsonSyntaxError::new(format!(
            "Unexpected non-whitespace character after JSON at {}",
            reader.location()
        )));
    }
    Ok(Parsed {
        node,
        repeated: reader.repeated,
    })
}

struct Reader<'a> {
    text: &'a [u8],
    source: &'a str,
    at: usize,
    /// The JSON pointer segments of the value being read.
    path: Vec<String>,
    repeated: Option<(String, String)>,
}

impl Reader<'_> {
    fn skip_space(&mut self) {
        while let Some(b' ' | b'\t' | b'\n' | b'\r') = self.text.get(self.at) {
            self.at += 1;
        }
    }

    fn peek(&self) -> Option<u8> {
        self.text.get(self.at).copied()
    }

    /// The character at the reader, if any.
    fn current(&self) -> Option<char> {
        self.source[self.at..].chars().next()
    }

    /// `message` at the reader, as V8 words an error at a position:
    /// `<message> in JSON at position <p> (line <l> column <c>)`.
    fn expected(&self, message: &str) -> JsonSyntaxError {
        JsonSyntaxError::new(format!("{message} in JSON at {}", self.location()))
    }

    /// Where the reader is, as V8 gives it: `position <p> (line <l> column
    /// <c>)`, the position and column in UTF-16 code units, from 0 and 1,
    /// and the line from 1. JSON breaks lines at `\n` and `\r`, and counts
    /// `\r\n` as one break.
    fn location(&self) -> String {
        let before = &self.text[..self.at];
        let mut line = 1;
        let mut line_start = 0;
        let mut i = 0;
        while i < before.len() {
            if before[i] == b'\r' && before.get(i + 1) == Some(&b'\n') {
                i += 1;
            }
            if before[i] == b'\r' || before[i] == b'\n' {
                line += 1;
                line_start = i + 1;
            }
            i += 1;
        }
        let position = utf16_len(&self.source[..self.at]);
        let column = 1 + utf16_len(&self.source[line_start..self.at]);
        format!("position {position} (line {line} column {column})")
    }

    /// The end of the text where a value or the rest of an escape belongs.
    fn end_of_input() -> JsonSyntaxError {
        JsonSyntaxError::new("Unexpected end of JSON input".to_owned())
    }

    /// The character at the reader where V8 expected something else and has
    /// no message of its own for the place: the end of the text, a number
    /// (a digit or `-`), a string (`"`) or any other token.
    fn unexpected(&self) -> JsonSyntaxError {
        match self.current() {
            None => Self::end_of_input(),
            Some('0'..='9' | '-') => self.expected("Unexpected number"),
            Some('"') => self.expected("Unexpected string"),
            Some(_) => self.unexpected_token(),
        }
    }

    /// An unexpected token, quoted with the text around it:
    /// `Unexpected token '<t>', "<text>" is not valid JSON`, the whole text
    /// when it is shorter than 21 UTF-16 code units, else ten units either
    /// side of the token, `...` marking each side that is not the text's
    /// start or end. The token is one UTF-16 code unit. V8 names one of
    /// [`SPECIAL_TEXTS`] whole instead.
    fn unexpected_token(&self) -> JsonSyntaxError {
        if SPECIAL_TEXTS.contains(&self.source) {
            return JsonSyntaxError::new(format!("\"{}\" is not valid JSON", self.source));
        }
        let units: Vec<u16> = self.source.encode_utf16().collect();
        let at = utf16_len(&self.source[..self.at]);
        let quote = |start: usize, end: usize| String::from_utf16_lossy(&units[start..end]);
        let token = quote(at, at + 1);
        let len = units.len();
        let context = if len < MIN_LENGTH_FOR_CONTEXT {
            format!("\"{}\"", self.source)
        } else if at < CONTEXT {
            format!("\"{}\"...", quote(0, at + CONTEXT))
        } else if at < len - CONTEXT {
            format!("...\"{}\"...", quote(at - CONTEXT, at + CONTEXT))
        } else {
            format!("...\"{}\"", quote(at - CONTEXT, len))
        };
        JsonSyntaxError::new(format!(
            "Unexpected token '{token}', {context} is not valid JSON"
        ))
    }

    fn value(&mut self, depth: usize) -> Result<Node, JsonSyntaxError> {
        if depth > MAX_DEPTH {
            return Err(JsonSyntaxError::new(format!(
                "JSON nests more than {MAX_DEPTH} levels deep at {}",
                self.location()
            )));
        }
        match self.peek() {
            Some(b'{') => self.object(depth),
            Some(b'[') => self.array(depth),
            Some(b'"') => Ok(Node::String(self.string()?)),
            Some(b't') => self.literal("true", Node::Bool(true)),
            Some(b'f') => self.literal("false", Node::Bool(false)),
            Some(b'n') => self.literal("null", Node::Null),
            Some(b'-' | b'0'..=b'9') => self.number(),
            // Never a number or a string here.
            _ => Err(self.unexpected()),
        }
    }

    fn literal(&mut self, word: &str, node: Node) -> Result<Node, JsonSyntaxError> {
        if self.text[self.at..].starts_with(word.as_bytes()) {
            self.at += word.len();
            Ok(node)
        } else {
            // The first character that differs, or the end of the text.
            let mut probe = self.at;
            while probe < self.text.len() && self.text[probe] == word.as_bytes()[probe - self.at] {
                probe += 1;
            }
            self.at = probe;
            Err(self.unexpected())
        }
    }

    fn digits(&mut self) -> usize {
        let start = self.at;
        while let Some(b'0'..=b'9') = self.peek() {
            self.at += 1;
        }
        self.at - start
    }

    fn number(&mut self) -> Result<Node, JsonSyntaxError> {
        let start = self.at;
        if self.peek() == Some(b'-') {
            self.at += 1;
        }
        match self.peek() {
            Some(b'0') => {
                self.at += 1;
                if let Some(b'0'..=b'9') = self.peek() {
                    // A leading zero.
                    return Err(self.expected("Unexpected number"));
                }
            }
            Some(b'1'..=b'9') => {
                self.digits();
            }
            _ => return Err(self.expected("No number after minus sign")),
        }
        if self.peek() == Some(b'.') {
            self.at += 1;
            if self.digits() == 0 {
                return Err(self.expected("Unterminated fractional number"));
            }
        }
        if let Some(b'e' | b'E') = self.peek() {
            self.at += 1;
            if let Some(b'+' | b'-') = self.peek() {
                self.at += 1;
            }
            if self.digits() == 0 {
                return Err(self.expected("Exponent part is missing a number"));
            }
        }
        Ok(Node::Number(self.source[start..self.at].to_owned()))
    }

    fn hex4(&mut self) -> Result<u32, JsonSyntaxError> {
        let mut value = 0;
        for _ in 0..4 {
            let digit = self
                .peek()
                .and_then(|b| (b as char).to_digit(16))
                .ok_or_else(|| self.expected("Bad Unicode escape"))?;
            value = value * 16 + digit;
            self.at += 1;
        }
        Ok(value)
    }

    fn string(&mut self) -> Result<String, JsonSyntaxError> {
        // The opening quote.
        self.at += 1;
        let mut out = String::new();
        loop {
            let start = self.at;
            while let Some(b) = self.peek() {
                if b == b'"' || b == b'\\' || b < 0x20 {
                    break;
                }
                self.at += 1;
            }
            out.push_str(&self.source[start..self.at]);
            match self.peek() {
                Some(b'"') => {
                    self.at += 1;
                    return Ok(out);
                }
                Some(b'\\') => {
                    self.at += 1;
                    self.escape(&mut out)?;
                }
                Some(_) => return Err(self.expected("Bad control character in string literal")),
                None => return Err(self.expected("Unterminated string")),
            }
        }
    }

    /// The escape after a backslash. V8 takes the end of the text there as
    /// the end of the input, and a character past U+00FF as an unexpected
    /// token.
    fn escape(&mut self, out: &mut String) -> Result<(), JsonSyntaxError> {
        let c = match self.current() {
            None => return Err(Self::end_of_input()),
            Some('"') => '"',
            Some('\\') => '\\',
            Some('/') => '/',
            Some('b') => '\u{8}',
            Some('f') => '\u{c}',
            Some('n') => '\n',
            Some('r') => '\r',
            Some('t') => '\t',
            Some('u') => {
                self.at += 1;
                out.push(self.unicode_escape()?);
                return Ok(());
            }
            Some(c) if u32::from(c) > 0xFF => return Err(self.unexpected_token()),
            Some(_) => return Err(self.expected("Bad escaped character")),
        };
        self.at += 1;
        out.push(c);
        Ok(())
    }

    /// The character a `\u` escape names. A high surrogate followed by a
    /// low one is the pair's character; any other surrogate is U+FFFD.
    fn unicode_escape(&mut self) -> Result<char, JsonSyntaxError> {
        let unit = self.hex4()?;
        if (0xD800..0xDC00).contains(&unit) && self.text[self.at..].starts_with(b"\\u") {
            let mark = self.at;
            self.at += 2;
            let low = self.hex4()?;
            if (0xDC00..0xE000).contains(&low) {
                let code = 0x10000 + ((unit - 0xD800) << 10) + (low - 0xDC00);
                return Ok(char::from_u32(code).unwrap_or('\u{FFFD}'));
            }
            // Not a pair: the next escape is read on its own.
            self.at = mark;
        }
        Ok(char::from_u32(unit).unwrap_or('\u{FFFD}'))
    }

    fn object(&mut self, depth: usize) -> Result<Node, JsonSyntaxError> {
        self.at += 1;
        let mut members: Vec<(String, Node)> = Vec::new();
        let mut index: HashMap<String, usize> = HashMap::new();
        self.skip_space();
        if self.peek() == Some(b'}') {
            self.at += 1;
            return Ok(Node::Object(members));
        }
        let mut first = true;
        loop {
            self.skip_space();
            if self.peek() != Some(b'"') {
                return Err(self.expected(if first {
                    "Expected property name or '}'"
                } else {
                    "Expected double-quoted property name"
                }));
            }
            first = false;
            let key = self.string()?;
            self.skip_space();
            if self.peek() != Some(b':') {
                return Err(self.expected("Expected ':' after property name"));
            }
            self.at += 1;
            self.skip_space();
            let seen = index.get(&key).copied();
            if seen.is_some() && self.repeated.is_none() {
                self.repeated = Some((key.clone(), self.pointer_to(&key)));
            }
            self.path.push(escape_pointer(&key));
            let value = self.value(depth + 1)?;
            self.path.pop();
            match seen {
                Some(position) => members[position].1 = value,
                None => {
                    index.insert(key.clone(), members.len());
                    members.push((key, value));
                }
            }
            self.skip_space();
            match self.peek() {
                Some(b',') => self.at += 1,
                Some(b'}') => {
                    self.at += 1;
                    return Ok(Node::Object(members));
                }
                _ => return Err(self.expected("Expected ',' or '}' after property value")),
            }
        }
    }

    fn array(&mut self, depth: usize) -> Result<Node, JsonSyntaxError> {
        self.at += 1;
        let mut items = Vec::new();
        self.skip_space();
        if self.peek() == Some(b']') {
            self.at += 1;
            return Ok(Node::Array(items));
        }
        loop {
            self.skip_space();
            self.path.push(items.len().to_string());
            let item = self.value(depth + 1)?;
            self.path.pop();
            items.push(item);
            self.skip_space();
            match self.peek() {
                Some(b',') => self.at += 1,
                Some(b']') => {
                    self.at += 1;
                    return Ok(Node::Array(items));
                }
                _ => return Err(self.expected("Expected ',' or ']' after array element")),
            }
        }
    }

    fn pointer_to(&self, key: &str) -> String {
        let mut pointer = String::new();
        for segment in &self.path {
            pointer.push('/');
            pointer.push_str(segment);
        }
        pointer.push('/');
        pointer.push_str(&escape_pointer(key));
        pointer
    }
}

/// The length of text in UTF-16 code units, a JavaScript string's length.
fn utf16_len(text: &str) -> usize {
    text.chars().map(char::len_utf16).sum()
}

/// A key as a JSON pointer segment: `~` as `~0`, `/` as `~1`.
pub(crate) fn escape_pointer(key: &str) -> String {
    key.replace('~', "~0").replace('/', "~1")
}

/// node as ir.CanonicalJSON writes a value.
pub(crate) fn write_canonical(node: &Node) -> String {
    let mut out = String::new();
    write(node, &mut out);
    out
}

/// Rewrites JSON text in the form ir.CanonicalJSON writes it, which is how
/// the IR persists extension and document data. It refuses invalid JSON,
/// and any data after the value.
pub fn canonical_json(text: &str) -> Result<String, JsonSyntaxError> {
    Ok(write_canonical(&parse(text)?.node))
}

fn write(node: &Node, out: &mut String) {
    match node {
        Node::Null => out.push_str("null"),
        Node::Bool(flag) => out.push_str(if *flag { "true" } else { "false" }),
        Node::String(text) => quote(text, out),
        Node::Number(literal) => out.push_str(literal),
        Node::Array(items) => {
            out.push('[');
            for (i, item) in items.iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                write(item, out);
            }
            out.push(']');
        }
        Node::Object(members) => {
            let mut sorted: Vec<&(String, Node)> = members.iter().collect();
            // Rust orders strings by their UTF-8 bytes, which is code point
            // order and Go's order for map keys.
            sorted.sort_by(|a, b| a.0.cmp(&b.0));
            out.push('{');
            for (i, (key, value)) in sorted.into_iter().enumerate() {
                if i > 0 {
                    out.push(',');
                }
                quote(key, out);
                out.push(':');
                write(value, out);
            }
            out.push('}');
        }
    }
}

/// Writes a JSON string as Go's encoder does with HTML escaping on: `"` and
/// `\` escaped, `\b` `\f` `\n` `\r` `\t` short, every other control
/// character and `<`, `>`, `&` as `\u00XX`, U+2028 and U+2029 as `\u2028`
/// and `\u2029`, and everything else, DEL and non-ASCII included, as is.
fn quote(text: &str, out: &mut String) {
    out.push('"');
    for c in text.chars() {
        match c {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\u{8}' => out.push_str("\\b"),
            '\u{c}' => out.push_str("\\f"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                out.push_str(&format!("\\u{:04x}", c as u32));
            }
            c if (c as u32) < 0x20 => out.push_str(&format!("\\u{:04x}", c as u32)),
            c => out.push(c),
        }
    }
    out.push('"');
}

/// A float64 as Go's encoder writes it: the shortest decimal that reads
/// back, in exponent form below 1e-6 and from 1e21, which is also
/// JavaScript's, except that Go keeps the sign of negative zero.
pub(crate) fn format_float64(value: f64) -> String {
    if value == 0.0 && value.is_sign_negative() {
        "-0".to_owned()
    } else {
        js_number(value)
    }
}

/// The float64 a number literal reads as, which is infinite past the
/// float64 range.
pub(crate) fn literal_value(literal: &str) -> f64 {
    literal.parse::<f64>().unwrap_or(f64::NAN)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn keeps_literals_and_member_order() {
        let parsed = parse(r#"{"b": 1.0, "a": [12345678901234567890123, -0, 1E2]}"#).unwrap();
        assert!(parsed.repeated.is_none());
        let members = parsed.node.as_object().unwrap();
        assert_eq!(members[0].0, "b");
        assert_eq!(members[0].1, Node::Number("1.0".to_owned()));
        assert_eq!(
            write_canonical(&parsed.node),
            r#"{"a":[12345678901234567890123,-0,1E2],"b":1.0}"#
        );
    }

    #[test]
    fn a_repeated_key_keeps_its_place_and_last_value() {
        let parsed = parse(r#"{"a": 1, "b": 2, "a": {"c": 3}}"#).unwrap();
        assert_eq!(parsed.repeated, Some(("a".to_owned(), "/a".to_owned())));
        let members = parsed.node.as_object().unwrap();
        assert_eq!(members.len(), 2);
        assert_eq!(members[0].0, "a");
        assert_eq!(write_canonical(&parsed.node), r#"{"a":{"c":3},"b":2}"#);
        let nested = parse(r#"{"x/y": [{"k": 1}, {"k~": 1, "k~": 2}]}"#).unwrap();
        assert_eq!(
            nested.repeated,
            Some(("k~".to_owned(), "/x~1y/1/k~0".to_owned()))
        );
    }

    #[test]
    fn lone_surrogates_read_as_the_replacement_character() {
        let parsed =
            parse(r#"["\ud800", "\udc00x\ud800", "\ud83d\ude00", "\ud800\u0041"]"#).unwrap();
        let items = parsed.node.as_array().unwrap();
        assert_eq!(items[0], Node::String("\u{FFFD}".to_owned()));
        assert_eq!(items[1], Node::String("\u{FFFD}x\u{FFFD}".to_owned()));
        assert_eq!(items[2], Node::String("\u{1F600}".to_owned()));
        assert_eq!(items[3], Node::String("\u{FFFD}A".to_owned()));
    }

    #[test]
    fn refuses_what_json_parse_refuses() {
        for text in [
            "",
            " ",
            "{",
            "[1,]",
            "{\"a\" 1}",
            "01",
            "1.",
            ".5",
            "-",
            "1e",
            "+1",
            "tru",
            "nul",
            "\"a\nb\"",
            "\"\\x\"",
            "\"\\u12g4\"",
            "{} {}",
            "[1]}",
            "{}]",
            "'a'",
            "NaN",
        ] {
            assert!(parse(text).is_err(), "{text:?}");
        }
        assert!(parse(&"[".repeat(MAX_DEPTH + 2)).is_err());
    }

    #[test]
    fn escapes_strings_as_go_does() {
        assert_eq!(
            canonical_json("[\"<a&b>\", \"\\u2028\\u2029\", \"\\b\\f\\n\\r\\t\", \"\\u0001\\u007f\"]").unwrap(),
            "[\"\\u003ca\\u0026b\\u003e\",\"\\u2028\\u2029\",\"\\b\\f\\n\\r\\t\",\"\\u0001\u{7f}\"]"
        );
    }

    #[test]
    fn floats_write_as_go_writes_them() {
        assert_eq!(format_float64(-0.0), "-0");
        assert_eq!(format_float64(1.0), "1");
        assert_eq!(format_float64(1e21), "1e+21");
        assert_eq!(format_float64(1e-7), "1e-7");
        assert!(literal_value("1e400").is_infinite());
    }
}
