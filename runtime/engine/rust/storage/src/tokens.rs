//! The SQL tokenizer the behavior checker reads (a port of `tokenize` in
//! `runtime/engine/typescript/src/behaviors/sql.ts`), and what the storage
//! reads with it: the `?` placeholders it numbers for Postgres and whether
//! a statement writes rows, which the write counter needs on Postgres.
//!
//! It skips comments and reads string, blob and number literals and
//! parameters as single tokens, so a name in a comment or a `?` in a
//! string is neither checked nor numbered. A malformed statement (an
//! unterminated literal, a number glued to a word) is the database's to
//! refuse; the tokenizer only keeps it from hiding a name.
//!
//! On Postgres it also reads what SQLite has no use for: dollar-quoted
//! strings (`$$ ... $$`, `$tag$ ... $tag$`), escape strings (`E'it\'s'`),
//! block comments that nest, `$1` parameters, and `[` as a subscript
//! rather than a quoted name.

use std::borrow::Cow;

use crate::dialect::Dialect;
use crate::error::{Error, Result};

/// What a token is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Kind {
    /// A bare name or keyword.
    Word,
    /// A quoted name: `"x"`, `` `x` ``, or on SQLite `[x]`.
    Quoted,
    /// A string literal, its quotes removed and its doubled quotes made one.
    Str,
    Semicolon,
    Open,
    Close,
    /// A parameter.
    Param(Param),
    /// Anything else: an operator, punctuation, a number or blob literal.
    Other,
}

/// A parameter's form.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Param {
    /// `?`, numbered one past the largest number before it.
    Bare,
    /// `?NNN`.
    Numbered(u32),
    /// Postgres's `$NNN`.
    Dollar(u32),
    /// SQLite's `:name`, `@name` and `$name`.
    Named,
}

/// One token, with where it starts and ends in the SQL text (bytes).
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) struct Token {
    pub kind: Kind,
    /// A word's or quoted name's text, a string's content, or the
    /// punctuation itself; `0` for a number and `?` for a parameter.
    pub text: String,
    pub start: usize,
    pub end: usize,
}

impl Token {
    /// The text upper-cased when the token is a word, and empty otherwise,
    /// as the checker compares keywords.
    pub fn keyword(&self) -> String {
        if self.kind == Kind::Word {
            self.text.to_ascii_uppercase()
        } else {
            String::new()
        }
    }

    /// Whether the token is a name, bare or quoted, or a string literal,
    /// which SQLite reads as a name where a name is expected.
    pub fn names(&self) -> bool {
        matches!(self.kind, Kind::Word | Kind::Quoted | Kind::Str)
    }
}

/// Splits `sql` into the tokens the checker reads.
pub(crate) fn tokenize(sql: &str, dialect: Dialect) -> Vec<Token> {
    let postgres = dialect == Dialect::Postgres;
    let mut tokens = Vec::new();
    let mut i = 0;
    while i < sql.len() {
        if let Some(after) = skipped(sql, i, postgres) {
            i = after;
            continue;
        }
        let (kind, text, end) = literal(sql, i, postgres)
            .or_else(|| parameter(sql, i, postgres))
            .unwrap_or_else(|| plain(sql, i));
        tokens.push(Token {
            kind,
            text,
            start: i,
            end,
        });
        i = end;
    }
    tokens
}

/// A token's kind, text and end.
type Scanned = (Kind, String, usize);

// byte_at reads the byte at i, or 0 past the end.
fn byte_at(sql: &str, i: usize) -> u8 {
    sql.as_bytes().get(i).copied().unwrap_or(0)
}

// skipped returns the index after the whitespace or comment at i, or None
// when a token starts there.
fn skipped(sql: &str, i: usize, postgres: bool) -> Option<usize> {
    let (c, next) = (byte_at(sql, i), byte_at(sql, i + 1));
    if c.is_ascii_whitespace() {
        Some(i + 1)
    } else if c == b'-' && next == b'-' {
        Some(
            sql[i..]
                .find('\n')
                .map_or(sql.len(), |offset| i + offset + 1),
        )
    } else if c == b'/' && next == b'*' {
        Some(block_comment_end(sql.as_bytes(), i, postgres))
    } else {
        None
    }
}

// literal scans a string, a quoted name or a blob at i.
fn literal(sql: &str, i: usize, postgres: bool) -> Option<Scanned> {
    let bytes = sql.as_bytes();
    let (c, next) = (byte_at(sql, i), byte_at(sql, i + 1));
    Some(if c == b'\'' {
        let end = quoted_end(bytes, i, b'\'');
        (Kind::Str, unquote(sql, i, end, b'\''), end)
    } else if postgres && (c == b'e' || c == b'E') && next == b'\'' {
        let end = escape_string_end(bytes, i + 1);
        let close = if end > i + 2 && bytes[end - 1] == b'\'' {
            end - 1
        } else {
            end
        };
        (Kind::Str, sql[(i + 2).min(close)..close].to_owned(), end)
    } else if (c == b'x' || c == b'X') && next == b'\'' {
        (
            Kind::Other,
            "x'".to_owned(),
            quoted_end(bytes, i + 1, b'\''),
        )
    } else if c == b'"' || c == b'`' {
        let end = quoted_end(bytes, i, c);
        (Kind::Quoted, unquote(sql, i, end, c), end)
    } else if c == b'[' && !postgres {
        let end = sql[i + 1..]
            .find(']')
            .map_or(sql.len(), |offset| i + 1 + offset);
        (
            Kind::Quoted,
            sql[i + 1..end].to_owned(),
            (end + 1).min(sql.len()),
        )
    } else if postgres && c == b'$' && !next.is_ascii_digit() {
        let (content, end) = dollar_quoted(sql, i)?;
        (Kind::Str, content.to_owned(), end)
    } else {
        return None;
    })
}

// parameter scans a parameter at i: `?`, `?NNN`, and SQLite's `:name`,
// `@name` and `$name` or Postgres's `$NNN`.
fn parameter(sql: &str, i: usize, postgres: bool) -> Option<Scanned> {
    let c = byte_at(sql, i);
    let named_end = || {
        let mut j = i + 1;
        while j < sql.len() && (identifier_part(byte_at(sql, j)) || byte_at(sql, j) == b':') {
            j += 1;
        }
        j
    };
    let (param, end) = if c == b'?' {
        // On Postgres a placeholder is often cast (`?::text`), so only
        // digits continue one there.
        let end = if postgres {
            let mut end = i + 1;
            while byte_at(sql, end).is_ascii_digit() {
                end += 1;
            }
            end
        } else {
            named_end()
        };
        let rest = &sql[i + 1..end];
        let param = if rest.is_empty() {
            Param::Bare
        } else if rest.bytes().all(|b| b.is_ascii_digit()) {
            rest.parse().map_or(Param::Named, Param::Numbered)
        } else {
            Param::Named
        };
        (param, end)
    } else if postgres && c == b'$' && byte_at(sql, i + 1).is_ascii_digit() {
        let mut end = i + 1;
        while byte_at(sql, end).is_ascii_digit() {
            end += 1;
        }
        (
            sql[i + 1..end].parse().map_or(Param::Named, Param::Dollar),
            end,
        )
    } else if !postgres && (c == b':' || c == b'@' || c == b'$') {
        (Param::Named, named_end())
    } else {
        return None;
    };
    Some((Kind::Param(param), "?".to_owned(), end))
}

// plain scans punctuation, a word, a number or any other character at i.
fn plain(sql: &str, i: usize) -> Scanned {
    let (c, next) = (byte_at(sql, i), byte_at(sql, i + 1));
    match c {
        b';' => (Kind::Semicolon, ";".to_owned(), i + 1),
        b'(' => (Kind::Open, "(".to_owned(), i + 1),
        b')' => (Kind::Close, ")".to_owned(), i + 1),
        _ if identifier_start(c) => {
            let mut j = i + 1;
            while j < sql.len() && identifier_part(byte_at(sql, j)) {
                j += 1;
            }
            (Kind::Word, sql[i..j].to_owned(), j)
        }
        _ if c.is_ascii_digit() || (c == b'.' && next.is_ascii_digit()) => {
            let mut j = i + 1;
            while j < sql.len() && number_part(byte_at(sql, j), byte_at(sql, j - 1)) {
                j += 1;
            }
            (Kind::Other, "0".to_owned(), j)
        }
        _ => {
            // One character, which may be several bytes.
            let width = sql[i..].chars().next().map_or(1, char::len_utf8);
            (Kind::Other, sql[i..i + width].to_owned(), i + width)
        }
    }
}

// number_part says whether c continues a number literal after previous:
// digits, letters, `_` and `.`, and a sign after an exponent's e.
fn number_part(c: u8, previous: u8) -> bool {
    c.is_ascii_alphanumeric()
        || c == b'_'
        || c == b'.'
        || ((c == b'+' || c == b'-') && matches!(previous, b'e' | b'E'))
}

// block_comment_end returns the index after the comment that opens at
// start. Postgres nests block comments; SQLite ends one at the first `*/`.
fn block_comment_end(bytes: &[u8], start: usize, nested: bool) -> usize {
    let n = bytes.len();
    let mut depth = 0_usize;
    let mut i = start;
    while i + 1 < n {
        if bytes[i] == b'/' && bytes[i + 1] == b'*' {
            if depth > 0 && !nested {
                i += 1;
                continue;
            }
            depth += 1;
            i += 2;
        } else if bytes[i] == b'*' && bytes[i + 1] == b'/' {
            depth -= 1;
            i += 2;
            if depth == 0 {
                return i;
            }
        } else {
            i += 1;
        }
    }
    n
}

// quoted_end returns the index after the literal that opens at start with
// quote; a doubled quote is part of the literal.
fn quoted_end(bytes: &[u8], start: usize, quote: u8) -> usize {
    let mut i = start + 1;
    while i < bytes.len() {
        if bytes[i] == quote {
            if bytes.get(i + 1) == Some(&quote) {
                i += 2;
                continue;
            }
            return i + 1;
        }
        i += 1;
    }
    bytes.len()
}

// escape_string_end returns the index after Postgres's escape string whose
// opening quote is at start: a backslash escapes the character after it,
// and a doubled quote is part of the literal.
fn escape_string_end(bytes: &[u8], start: usize) -> usize {
    let mut i = start + 1;
    while i < bytes.len() {
        match bytes[i] {
            b'\\' => i += 2,
            b'\'' if bytes.get(i + 1) == Some(&b'\'') => i += 2,
            b'\'' => return i + 1,
            _ => i += 1,
        }
    }
    bytes.len()
}

// unquote returns the content of the literal from start to end, its
// doubled quotes made one.
fn unquote(sql: &str, start: usize, end: usize, quote: u8) -> String {
    let close = if end > start + 1 && sql.as_bytes()[end - 1] == quote {
        end - 1
    } else {
        end
    };
    let quote = quote as char;
    let doubled: String = [quote, quote].iter().collect();
    sql[start + 1..close.max(start + 1)].replace(&doubled, &quote.to_string())
}

// dollar_quoted reads Postgres's dollar-quoted string opening at start
// (`$$` or `$tag$`): its content and the index after its closing tag, or
// None when no tag opens there.
fn dollar_quoted(sql: &str, start: usize) -> Option<(&str, usize)> {
    let bytes = sql.as_bytes();
    let mut j = start + 1;
    if j < bytes.len() && identifier_start(bytes[j]) {
        j += 1;
        while j < bytes.len() && identifier_part(bytes[j]) && bytes[j] != b'$' {
            j += 1;
        }
    }
    if bytes.get(j) != Some(&b'$') {
        return None;
    }
    let tag = &sql[start..=j];
    let body = j + 1;
    match sql[body..].find(tag) {
        Some(offset) => Some((&sql[body..body + offset], body + offset + tag.len())),
        None => Some((&sql[body..], sql.len())),
    }
}

fn identifier_start(c: u8) -> bool {
    c.is_ascii_alphabetic() || c == b'_' || c >= 0x80
}

fn identifier_part(c: u8) -> bool {
    c.is_ascii_alphanumeric() || c == b'_' || c == b'$' || c >= 0x80
}

/// The statements a `WITH` clause may prefix, and the ones a read runs.
pub(crate) const READ_VERBS: &[&str] = &["SELECT", "VALUES"];
pub(crate) const WRITE_VERBS: &[&str] = &["INSERT", "UPDATE", "DELETE", "REPLACE"];

/// The statement a `WITH` clause prefixes: the first of SELECT, VALUES,
/// INSERT, UPDATE, DELETE or REPLACE outside parentheses. Data-changing
/// statements cannot nest in SQLite (and the checker refuses them nested
/// on Postgres), so it decides whether the whole statement writes.
pub(crate) fn main_verb(statement: &[Token]) -> Option<String> {
    let mut depth = 0_i64;
    for token in statement.iter().skip(1) {
        match token.kind {
            Kind::Open => depth += 1,
            Kind::Close => depth -= 1,
            Kind::Word if depth == 0 => {
                let verb = token.keyword();
                if READ_VERBS.contains(&verb.as_str())
                    || WRITE_VERBS.contains(&verb.as_str())
                    || verb == "MERGE"
                {
                    return Some(verb);
                }
            }
            _ => {}
        }
    }
    None
}

/// Whether a statement changes rows: an INSERT, UPDATE, DELETE, REPLACE or
/// MERGE, or a WITH clause before one. The write counter adds the rows a
/// Postgres statement reports only for these, since Postgres reports a
/// SELECT's rows the same way (SQLite counts its changes itself).
#[cfg(feature = "postgres")]
pub(crate) fn statement_writes(sql: &str, dialect: Dialect) -> bool {
    let tokens = tokenize(sql, dialect);
    let Some(first) = tokens.first() else {
        return false;
    };
    let mut verb = first.keyword();
    if verb == "WITH" {
        verb = main_verb(&tokens).unwrap_or_default();
    }
    WRITE_VERBS.contains(&verb.as_str()) || verb == "MERGE"
}

/// Whether a statement changes tables: a CREATE, ALTER or DROP.
#[cfg(feature = "postgres")]
pub(crate) fn statement_changes_tables(sql: &str, dialect: Dialect) -> bool {
    let tokens = tokenize(sql, dialect);
    tokens
        .first()
        .is_some_and(|first| matches!(first.keyword().as_str(), "CREATE" | "ALTER" | "DROP"))
}

/// The statement with each `?` placeholder numbered for Postgres: a bare
/// `?` is `$k`, one past the largest number before it, as SQLite numbers
/// it, and `?NNN` is `$NNN`. A `?` in a string, a quoted name or a comment
/// is not a placeholder. Text that numbers its own placeholders (`$1`)
/// runs as written; text that mixes the two, or names a parameter
/// (`:name`), is refused.
///
/// A behavior that needs Postgres's `jsonb` operators `?`, `?|` and `?&`
/// calls `jsonb_exists`, `jsonb_exists_any` and `jsonb_exists_all`.
pub fn postgres_placeholders(sql: &str) -> Result<Cow<'_, str>> {
    if !sql.contains('?') {
        return Ok(Cow::Borrowed(sql));
    }
    let tokens = tokenize(sql, Dialect::Postgres);
    let mut out = String::with_capacity(sql.len() + 8);
    let mut copied = 0;
    let mut largest = 0_u32;
    let mut questions = false;
    let mut dollars = false;
    for token in &tokens {
        let Kind::Param(param) = token.kind else {
            continue;
        };
        let number = match param {
            Param::Bare => largest + 1,
            Param::Numbered(number) => number,
            Param::Dollar(_) => {
                dollars = true;
                continue;
            }
            Param::Named => {
                return Err(Error::usage(format!(
                    "a statement for Postgres binds ? or $1, not a named parameter ({}): {}",
                    &sql[token.start..token.end],
                    sql.trim()
                )));
            }
        };
        questions = true;
        largest = largest.max(number);
        out.push_str(&sql[copied..token.start]);
        out.push('$');
        out.push_str(&number.to_string());
        copied = token.end;
    }
    if questions && dollars {
        return Err(Error::usage(format!(
            "a statement binds ? or $1 placeholders, not both: {}",
            sql.trim()
        )));
    }
    if !questions {
        return Ok(Cow::Borrowed(sql));
    }
    out.push_str(&sql[copied..]);
    Ok(Cow::Owned(out))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn kinds(sql: &str, dialect: Dialect) -> Vec<(Kind, String)> {
        tokenize(sql, dialect)
            .into_iter()
            .map(|token| (token.kind, token.text))
            .collect()
    }

    #[test]
    fn comments_strings_and_names_are_single_tokens() {
        let tokens = kinds(
            "SELECT \"a\"\"b\", 'it''s', [x y], `q` -- engine_x\n/* engine_y */ FROM t;",
            Dialect::Sqlite,
        );
        assert_eq!(
            tokens,
            vec![
                (Kind::Word, "SELECT".into()),
                (Kind::Quoted, "a\"b".into()),
                (Kind::Other, ",".into()),
                (Kind::Str, "it's".into()),
                (Kind::Other, ",".into()),
                (Kind::Quoted, "x y".into()),
                (Kind::Other, ",".into()),
                (Kind::Quoted, "q".into()),
                (Kind::Word, "FROM".into()),
                (Kind::Word, "t".into()),
                (Kind::Semicolon, ";".into()),
            ]
        );
    }

    #[test]
    fn numbers_blobs_and_parameters_hide_nothing() {
        let tokens = kinds("x'00ff' 1.5e+3 .5 ?1 ? :name @n $v", Dialect::Sqlite);
        assert_eq!(
            tokens.iter().map(|(kind, _)| *kind).collect::<Vec<_>>(),
            vec![
                Kind::Other,
                Kind::Other,
                Kind::Other,
                Kind::Param(Param::Numbered(1)),
                Kind::Param(Param::Bare),
                Kind::Param(Param::Named),
                Kind::Param(Param::Named),
                Kind::Param(Param::Named),
            ]
        );
    }

    #[test]
    fn postgres_reads_dollar_quotes_escape_strings_and_nested_comments() {
        let tokens = kinds(
            "SELECT $$engine_x ?$$, $tag$ a $ b $tag$, E'it\\'s ?', arr[1], $2 /* a /* b */ c */ ::text",
            Dialect::Postgres,
        );
        assert_eq!(
            tokens,
            vec![
                (Kind::Word, "SELECT".into()),
                (Kind::Str, "engine_x ?".into()),
                (Kind::Other, ",".into()),
                (Kind::Str, " a $ b ".into()),
                (Kind::Other, ",".into()),
                (Kind::Str, "it\\'s ?".into()),
                (Kind::Other, ",".into()),
                (Kind::Word, "arr".into()),
                (Kind::Other, "[".into()),
                (Kind::Other, "0".into()),
                (Kind::Other, "]".into()),
                (Kind::Other, ",".into()),
                (Kind::Param(Param::Dollar(2)), "?".into()),
                (Kind::Other, ":".into()),
                (Kind::Other, ":".into()),
                (Kind::Word, "text".into()),
            ]
        );
        // SQLite ends a block comment at the first */.
        assert_eq!(
            kinds("/* a /* b */ c */", Dialect::Sqlite),
            vec![
                (Kind::Word, "c".into()),
                (Kind::Other, "*".into()),
                (Kind::Other, "/".into())
            ]
        );
    }

    #[test]
    fn placeholders_are_numbered_outside_strings_and_comments() {
        let sql =
            "SELECT '?', ?, \"?\" -- ?\n/* ? */ FROM t WHERE a = ? AND b = $$?$$ AND c = E'\\'?'";
        assert_eq!(
            postgres_placeholders(sql).unwrap(),
            "SELECT '?', $1, \"?\" -- ?\n/* ? */ FROM t WHERE a = $2 AND b = $$?$$ AND c = E'\\'?'"
        );
        // A bare ? takes one past the largest number before it, as SQLite
        // numbers it.
        assert_eq!(
            postgres_placeholders("VALUES (?2, ?, ?1, ?)").unwrap(),
            "VALUES ($2, $3, $1, $4)"
        );
        // Text with no placeholder, or numbered for Postgres, runs as written.
        assert!(matches!(
            postgres_placeholders("SELECT $1, '?'").unwrap(),
            Cow::Borrowed(_)
        ));
        assert!(matches!(
            postgres_placeholders("SELECT 1").unwrap(),
            Cow::Borrowed(_)
        ));
        assert!(postgres_placeholders("SELECT ?, $1").is_err());
        assert_eq!(
            postgres_placeholders("SELECT ?::text, ?3::bigint").unwrap(),
            "SELECT $1::text, $3::bigint"
        );
    }

    #[cfg(feature = "postgres")]
    #[test]
    fn a_statement_writes_when_its_main_verb_does() {
        for sql in [
            "INSERT INTO t VALUES (1)",
            "update t set a = 1",
            "DELETE FROM t RETURNING id",
            "WITH x AS (SELECT 1) INSERT INTO t SELECT * FROM x",
            "MERGE INTO t USING s ON true WHEN MATCHED THEN DELETE",
        ] {
            assert!(statement_writes(sql, Dialect::Postgres), "{sql}");
        }
        for sql in [
            "SELECT * FROM t FOR UPDATE",
            "WITH x AS (SELECT 1) SELECT * FROM x",
            "VALUES (1)",
            "",
            "-- INSERT",
        ] {
            assert!(!statement_writes(sql, Dialect::Postgres), "{sql}");
        }
        assert!(statement_changes_tables(
            "/* x */ alter table t add column c text",
            Dialect::Postgres
        ));
        assert!(!statement_changes_tables(
            "SELECT 'CREATE'",
            Dialect::Postgres
        ));
    }
}
