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
//! RE2. RE2 syntax the crate lacks or reads otherwise is spelled out too:
//! `\Q...\E` and octal `\101` become literals, `\p{^L}` is `\P{L}`, `\<`
//! and `\>` are the literals `<` and `>` (word boundaries to the crate), and
//! a `{` that opens no repetition is a literal.

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
            match escaped {
                'Q' if !in_class => push_quoted(&mut out, &mut chars),
                '0'..='7'
                    if escaped == '0' || chars.peek().is_some_and(|d| ('0'..='7').contains(d)) =>
                {
                    push_octal(&mut out, escaped, &mut chars);
                }
                'p' | 'P' if chars.peek() == Some(&'{') => {
                    push_property(&mut out, escaped, &mut chars)
                }
                'x' if chars.peek() == Some(&'{') => {
                    out.push_str(r"\x");
                    for c in chars.by_ref() {
                        out.push(c);
                        if c == '}' {
                            break;
                        }
                    }
                }
                // Word boundaries to the `regex` crate, literals to RE2.
                '<' | '>' => out.push(escaped),
                _ => push_escape(&mut out, escaped, in_class),
            }
            class_start = false;
            continue;
        }
        if !in_class {
            if c == '{' && !opens_repetition(chars.clone()) {
                out.push_str(r"\{");
                continue;
            }
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

/// Writes the text of `\Q...\E`, its `\Q` read, as escaped literals.
fn push_quoted(out: &mut String, chars: &mut Peekable<Chars<'_>>) {
    while let Some(c) = chars.next() {
        if c == '\\' && chars.peek() == Some(&'E') {
            chars.next();
            return;
        }
        out.push_str(&regex::escape(c.encode_utf8(&mut [0; 4])));
    }
}

/// Writes an octal escape (`\0`, `\12`, `\101`: up to three digits, the
/// first read), which the `regex` crate reads as a backreference, as `\x{..}`.
fn push_octal(out: &mut String, first: char, chars: &mut Peekable<Chars<'_>>) {
    let mut value = first.to_digit(8).unwrap_or(0);
    for _ in 0..2 {
        match chars.peek().and_then(|d| d.to_digit(8)) {
            Some(digit) => {
                value = value * 8 + digit;
                chars.next();
            }
            None => break,
        }
    }
    out.push_str(&format!("\\x{{{value:X}}}"));
}

/// Copies `\p{Name}`, its `\p` or `\P` read, writing RE2's negated
/// `\p{^Name}` as `\P{Name}`.
fn push_property(out: &mut String, letter: char, chars: &mut Peekable<Chars<'_>>) {
    chars.next();
    let negated = chars.peek() == Some(&'^');
    if negated {
        chars.next();
    }
    let flipped = match (letter, negated) {
        ('p', true) => 'P',
        ('P', true) => 'p',
        _ => letter,
    };
    out.push('\\');
    out.push(flipped);
    out.push('{');
    for c in chars.by_ref() {
        out.push(c);
        if c == '}' {
            return;
        }
    }
}

/// Whether the text after a `{` is a repetition RE2 reads (`{n}`, `{n,}`,
/// `{n,m}`); RE2 reads any other `{` as a literal.
fn opens_repetition(mut rest: Peekable<Chars<'_>>) -> bool {
    let digits = |rest: &mut Peekable<Chars<'_>>| {
        let mut n = 0;
        while rest.next_if(char::is_ascii_digit).is_some() {
            n += 1;
        }
        n
    };
    if digits(&mut rest) == 0 {
        return false;
    }
    if rest.next_if_eq(&',').is_some() {
        digits(&mut rest);
    }
    rest.next() == Some('}')
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

    /// runtime/schema/testdata/pattern_dialect.json: patterns with texts and
    /// whether Go's regexp (RE2) finds a match, which
    /// internal/generator/patterndialect writes and every runtime asserts.
    #[test]
    fn the_dialect_corpus_matches_as_re2_matches() {
        let corpus: serde_json::Value =
            serde_json::from_str(include_str!("../../testdata/pattern_dialect.json")).unwrap();
        let mut failures = Vec::new();
        for case in corpus["cases"].as_array().unwrap() {
            let pattern = case["pattern"].as_str().unwrap();
            let text = case["text"].as_str().unwrap();
            let want = case["match"].as_bool().unwrap();
            let got = Regex::new(&translate_pattern(pattern)).map(|re| re.is_match(text));
            if got.as_ref().ok() != Some(&want) {
                failures.push(format!(
                    "{pattern:?} against {text:?}: got {got:?}, RE2 says {want}"
                ));
            }
        }
        assert!(failures.is_empty(), "{}", failures.join("\n"));
    }
}
