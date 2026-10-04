//! A schema `pattern` compiled once, matched as Go's RE2 matches it.
//!
//! Rust's `regex` crate reads `\d`, `\w`, `\s` and `\b` as Unicode classes,
//! where Go's RE2 reads them as ASCII and JavaScript (with the `u` flag)
//! reads `\d`, `\w` and `\b` as ASCII. A pattern is translated before it
//! compiles so `\d` matches only `0-9`, `\w` only `[0-9A-Za-z_]`, `\s` only
//! RE2's `[\t\n\f\r ]` and `\b` only an ASCII word boundary. Everything else
//! (`.` and literals matching code points, `\pL`, flags, anchors) is left as
//! written. Inside a bracket class a `[` that does not open a POSIX class
//! (`[:alpha:]`) is escaped, as are the doubled `&&`, `--` and `~~` that
//! Rust reads as class set operations, so a class means what it means to
//! RE2.

use std::iter::Peekable;
use std::str::Chars;
use std::sync::OnceLock;

use regex::Regex;

const DIGIT: &str = "0-9";
const WORD: &str = "0-9A-Za-z_";
const SPACE: &str = r"\t\n\f\r ";

/// A pattern rule, compiled the first time it matches. Generated validators
/// hold one per rule in a `static`.
pub struct Pattern {
    source: &'static str,
    compiled: OnceLock<Option<Regex>>,
}

impl Pattern {
    pub const fn new(source: &'static str) -> Self {
        Self {
            source,
            compiled: OnceLock::new(),
        }
    }

    /// The pattern as the schema wrote it.
    pub fn source(&self) -> &'static str {
        self.source
    }

    /// False when the translated pattern does not compile.
    pub fn is_valid(&self) -> bool {
        self.regex().is_some()
    }

    /// Whether text contains a match, anywhere unless the pattern anchors
    /// itself, as Go's `MatchString` and JavaScript's `test` search. A
    /// pattern that does not compile matches nothing, so its rule refuses
    /// every value instead of passing them all.
    pub fn is_match(&self, text: &str) -> bool {
        self.regex().is_some_and(|re| re.is_match(text))
    }

    fn regex(&self) -> Option<&Regex> {
        self.compiled
            .get_or_init(|| Regex::new(&translate_pattern(self.source)).ok())
            .as_ref()
    }
}

impl std::fmt::Debug for Pattern {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_tuple("Pattern").field(&self.source).finish()
    }
}

/// The pattern rewritten so the `regex` crate reads it as RE2 does: see the
/// module documentation.
pub fn translate_pattern(source: &str) -> String {
    let mut out = String::with_capacity(source.len() + 16);
    let mut chars = source.chars().peekable();
    // Inside a bracket class; at its first member, where `]` is a literal.
    let mut in_class = false;
    let mut class_start = false;
    while let Some(c) = chars.next() {
        if c == '\\' {
            let Some(escaped) = chars.next() else {
                out.push('\\');
                break;
            };
            push_escape(&mut out, escaped, in_class);
            class_start = false;
            continue;
        }
        if !in_class {
            out.push(c);
            if c == '[' {
                in_class = true;
                class_start = true;
                if chars.peek() == Some(&'^') {
                    out.push('^');
                    chars.next();
                }
            }
            continue;
        }
        match c {
            ']' if !class_start => {
                in_class = false;
                out.push(']');
            }
            '[' if chars.peek() == Some(&':') => push_posix_class(&mut out, &mut chars),
            '[' => out.push_str(r"\["),
            '&' | '-' | '~' if chars.peek() == Some(&c) => {
                chars.next();
                out.push(c);
                out.push('\\');
                out.push(c);
            }
            _ => out.push(c),
        }
        class_start = false;
    }
    out
}

fn push_escape(out: &mut String, escaped: char, in_class: bool) {
    let replacement = match (escaped, in_class) {
        ('d', false) => Some(format!("[{DIGIT}]")),
        ('w', false) => Some(format!("[{WORD}]")),
        ('s', false) => Some(format!("[{SPACE}]")),
        ('d', true) => Some(DIGIT.to_owned()),
        ('w', true) => Some(WORD.to_owned()),
        ('s', true) => Some(SPACE.to_owned()),
        // A negated class nests inside a bracket class, which Rust allows.
        ('D', _) => Some(format!("[^{DIGIT}]")),
        ('W', _) => Some(format!("[^{WORD}]")),
        ('S', _) => Some(format!("[^{SPACE}]")),
        ('b', false) => Some(r"(?-u:\b)".to_owned()),
        ('B', false) => Some(r"(?-u:\B)".to_owned()),
        _ => None,
    };
    match replacement {
        Some(text) => out.push_str(&text),
        None => {
            out.push('\\');
            out.push(escaped);
        }
    }
}

/// Copies a POSIX class such as `[:alpha:]` through its closing `:]`.
fn push_posix_class(out: &mut String, chars: &mut Peekable<Chars<'_>>) {
    out.push('[');
    let mut previous = '[';
    for c in chars.by_ref() {
        out.push(c);
        if previous == ':' && c == ']' {
            return;
        }
        previous = c;
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn perl_classes_become_ascii() {
        assert_eq!(translate_pattern(r"^\d+$"), "^[0-9]+$");
        assert_eq!(translate_pattern(r"\w\W"), "[0-9A-Za-z_][^0-9A-Za-z_]");
        assert_eq!(translate_pattern(r"\s\S"), r"[\t\n\f\r ][^\t\n\f\r ]");
        assert_eq!(translate_pattern(r"\bx\B"), r"(?-u:\b)x(?-u:\B)");
        assert_eq!(translate_pattern(r"[\d\w-]"), "[0-90-9A-Za-z_-]");
        assert_eq!(translate_pattern(r"[a\D]"), "[a[^0-9]]");
    }

    #[test]
    fn other_escapes_and_literals_are_kept() {
        assert_eq!(translate_pattern(r"a\.b\\d\pL"), r"a\.b\\d\pL");
        assert_eq!(translate_pattern(r"[\]\-]"), r"[\]\-]");
        assert_eq!(translate_pattern("(?i)^ab$"), "(?i)^ab$");
    }

    #[test]
    fn class_syntax_rust_would_misread_is_escaped() {
        assert_eq!(translate_pattern("[[]"), r"[\[]");
        assert_eq!(translate_pattern("[a&&b]"), r"[a&\&b]");
        assert_eq!(translate_pattern("[a~~b]"), r"[a~\~b]");
        assert_eq!(translate_pattern("[[:alpha:]_]"), "[[:alpha:]_]");
        assert_eq!(translate_pattern("[]a]"), "[]a]");
        assert_eq!(translate_pattern("[^]a]"), "[^]a]");
    }

    #[test]
    fn matches_as_re2_matches() {
        let word = Pattern::new(r"^\w\W\w$");
        assert!(word.is_match("a-b"));
        // é is not \w to RE2, so it is \W here: the value matches.
        assert!(word.is_match("aéb"));
        assert!(!Pattern::new(r"^\w+$").is_match("café"));
        assert!(!Pattern::new(r"^\d$").is_match("٣"));
        assert!(Pattern::new(r"^\d$").is_match("7"));
        assert!(!Pattern::new(r"^\s$").is_match("\u{a0}"));
        assert!(Pattern::new(r"^\s$").is_match("\t"));
        // An ASCII word boundary: é is not a word character to RE2.
        assert!(Pattern::new(r"\bb").is_match("éb"));
        assert!(!Pattern::new(r"\bé").is_match("é"));
    }

    #[test]
    fn dot_and_lengths_match_code_points() {
        assert!(Pattern::new("^.$").is_match("😀"));
        assert!(Pattern::new("^.{2}$").is_match("é😀"));
        assert!(!Pattern::new("^.$").is_match("\n"));
    }

    #[test]
    fn brackets_and_posix_classes_compile() {
        assert!(Pattern::new("^[[]$").is_match("["));
        assert!(Pattern::new("^[]a]+$").is_match("]a"));
        assert!(Pattern::new("^[[:alpha:]_]+$").is_match("ab_"));
        assert!(!Pattern::new("^[[:alpha:]]$").is_match("é"));
        assert!(Pattern::new("^[a&&b]+$").is_match("a&b"));
    }

    #[test]
    fn a_pattern_that_does_not_compile_matches_nothing() {
        let broken = Pattern::new("(unclosed");
        assert!(!broken.is_valid());
        assert!(!broken.is_match("(unclosed"));
        assert!(!broken.is_match(""));
        assert_eq!(broken.source(), "(unclosed");
    }

    static SHARED: Pattern = Pattern::new("^[a-z]+$");

    #[test]
    fn a_static_pattern_compiles_once() {
        assert!(SHARED.is_match("abc"));
        assert!(!SHARED.is_match("ABC"));
        assert!(SHARED.is_valid());
    }
}
