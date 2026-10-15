//! Full-text search, the engine's own (D58): an index of a few text
//! columns keyed by an integer row, created in a behavior's migration
//! ([`crate::behavior::BehaviorSql::create_fulltext`]) and matched with an
//! FTS5 expression ([`crate::behavior::BehaviorSql::fulltext`]), the
//! statements written for each database here rather than in the behavior
//! that calls it. Search is its caller today.
//!
//! On SQLite the index is an FTS5 table, exactly as the TypeScript engine
//! creates it (`fts5(<columns>, tokenize = 'unicode61 remove_diacritics
//! 2')`): a file the TypeScript engine wrote still works, and a match,
//! its order (`bm25` with a weight per column, then the row) and its
//! snippets (`snippet`) are SQLite's own, the same as the TypeScript
//! engine's.
//!
//! On Postgres the index is a table of the same columns and a `rowid`
//! primary key, beside a generated `tsvector` per column (`<column>_tsv`)
//! and one of all of them (`all_tsv`) with a GIN index, each through a
//! text search configuration of the index's own (`<index>_config`): the
//! default parser, then `unaccent` and `simple` for the words, so case
//! and diacritics fold as `remove_diacritics 2` folds them and no word is
//! stemmed or dropped, as FTS5's `unicode61` does neither. A hyphenated
//! word is indexed as its parts, a phrase of them, as FTS5 splits it; the
//! default parser keeps an email address, a URL, a host, a file path and a
//! number with a point whole, where FTS5 splits them into words, so on
//! Postgres a search finds those by the whole token alone. `unaccent` is
//! the database's extension where it has it, and otherwise the engine
//! creates it (trusted since Postgres 13, so a role with CREATE on the
//! database may) in a schema of its own, [`EXTENSION_SCHEMA`], shared by
//! every engine in the database: a database holds an extension once, and
//! an engine's own schema may be dropped with everything in it.
//!
//! A match takes an FTS5 expression on both databases. On Postgres it is
//! translated before it runs, since an error there would end the
//! transaction ([`tsquery`]): phrases (`"walnut desk"`, `walnut + desk`),
//! prefixes (`wal*`), AND, OR, NOT, implicit AND and parentheses; NEAR
//! groups and `^` are SQLite's alone and refused, as is a query FTS5 could
//! not parse, each with a [`QueryRefusal`]. Postgres ranks with
//! `ts_rank_cd` over the columns' vectors, each labelled with the class of
//! its weight (Postgres has four, A to D: [`weight_classes`]), then the
//! row; and writes a snippet with `ts_headline`, its matches between the
//! same marks as `snippet`'s, an ellipsis where it leaves text out before
//! or after. So the matches are the same, but for the tokens above, and
//! the order and the snippets differ: Search's documentation says so.

use std::fmt;

use crate::dialect::Dialect;
use crate::error::Error;
use crate::ident::{identifier, quote_identifier, sql_string};
use crate::value::{Row, SqlValue};

/// The tokenizer of an FTS5 index, the TypeScript engine's: words of
/// letters and digits, case folded and diacritics removed.
pub const SQLITE_TOKENIZER: &str = "unicode61 remove_diacritics 2";

/// The schema the engine creates `unaccent` in when the database does not
/// have it: one schema of the database's, shared by every engine in it.
pub const EXTENSION_SCHEMA: &str = "superschematic_extensions";

/// The token types of Postgres's default parser an index keeps, through
/// `unaccent` and `simple`: words, the parts of a hyphenated word (not the
/// word whole, so it is a phrase of its parts, as FTS5 has it), and the
/// tokens the parser keeps whole (an email address, a URL, a host, a path,
/// a number). XML tags and entities, a URL's protocol and blanks are left
/// out.
pub const POSTGRES_TOKEN_TYPES: &[&str] = &[
    "asciiword",
    "word",
    "numword",
    "hword_asciipart",
    "hword_part",
    "hword_numpart",
    "email",
    "url",
    "host",
    "url_path",
    "file",
    "sfloat",
    "float",
    "int",
    "uint",
    "version",
];

/// How deep parentheses nest in an expression Postgres takes, about where
/// FTS5's parser runs out of stack.
pub const MAX_NESTING: usize = 100;

/// The most columns an index has.
pub const MAX_COLUMNS: usize = 64;

/// Why a match's expression is not one the index takes: FTS5's own error
/// on SQLite, the translation's on Postgres. A caller's expression it
/// refuses is the caller's mistake (Search answers invalid_argument).
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct QueryRefusal {
    pub message: String,
}

impl QueryRefusal {
    fn new(message: impl Into<String>) -> Self {
        QueryRefusal {
            message: message.into(),
        }
    }

    fn near(text: &str) -> Self {
        QueryRefusal::new(format!("fts5: syntax error near \"{text}\""))
    }
}

impl fmt::Display for QueryRefusal {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}

/// The snippets a match writes: one per column of the first `columns`,
/// a few words around its matches, each match between `open` and `close`,
/// `ellipsis` where text is left out before or after.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Snippets<'a> {
    pub columns: usize,
    pub open: &'a str,
    pub close: &'a str,
    pub ellipsis: &'a str,
    /// How many words (FTS5's tokens) a snippet holds at most.
    pub words: u32,
}

/// One full-text match over an index, by the behavior's own names: the
/// rows of `keys` whose `key` is an index row the expression matches and
/// whose `filters` hold, best first, `limit` from `offset`.
#[derive(Clone, Debug)]
pub struct FullTextQuery<'a> {
    /// The index, by the behavior's own name for it.
    pub index: &'a str,
    /// The index's columns, in the order it was created with.
    pub columns: &'a [&'a str],
    /// A weight per column, in that order: how much a match in it counts;
    /// 0 leaves the column out of the ranking.
    pub weights: &'a [f64],
    pub snippets: Snippets<'a>,
    /// The table whose integer column `key` holds each index row's
    /// `rowid`, by the behavior's own name.
    pub keys: &'a str,
    pub key: &'a str,
    /// The columns of `keys` a hit returns besides `key`.
    pub select: &'a [&'a str],
    /// The values columns of `keys` must hold.
    pub filters: &'a [(&'a str, SqlValue)],
    /// A key to leave out.
    pub exclude: Option<i64>,
    /// An FTS5 expression.
    pub expression: &'a str,
    /// Whether the expression is a caller's, whose refusal is a
    /// [`QueryRefusal`]; a behavior's own is a defect when refused.
    pub caller: bool,
    pub limit: i64,
    pub offset: i64,
}

/// One row a match returns: `select` and `key` of `keys`, and a snippet
/// per column of `snippets` (`None` for a column that holds no text).
#[derive(Clone, Debug, PartialEq)]
pub struct FullTextHit {
    pub row: Row,
    pub snippets: Vec<Option<String>>,
}

/// What a match returns: its hits, best first, or why the index does not
/// take the caller's expression.
pub type FullTextMatches = std::result::Result<Vec<FullTextHit>, QueryRefusal>;

/// The four weight classes Postgres ranks with, for an index's columns:
/// the config's distinct weights above 0, highest first, are A, B, C and D,
/// and past four every lower one shares D, weighed as the highest of them;
/// each class weighs its weight over the highest (A is 1), since
/// `ts_rank_cd` takes weights from 0 to 1. A column weighed 0 has no class
/// and is left out of the ranking.
#[derive(Clone, Debug, PartialEq)]
pub struct WeightClasses {
    /// Each column's class, `A` to `D`.
    pub labels: Vec<Option<char>>,
    /// The classes' weights in `ts_rank_cd`'s order: D, C, B, A.
    pub weights: [f64; 4],
}

/// The weight class of each column and each class's weight
/// ([`WeightClasses`]).
pub fn weight_classes(weights: &[f64]) -> WeightClasses {
    let mut distinct: Vec<f64> = weights
        .iter()
        .copied()
        .filter(|weight| *weight > 0.0 && weight.is_finite())
        .collect();
    distinct.sort_by(|a, b| b.total_cmp(a));
    distinct.dedup();
    let highest = distinct.first().copied().unwrap_or(1.0);
    let mut classes = [0.0; 4];
    for (place, weight) in distinct.iter().take(4).enumerate() {
        classes[3 - place] = weight / highest;
    }
    let labels = weights
        .iter()
        .map(|weight| {
            let place = distinct.iter().position(|each| each == weight)?;
            Some(['A', 'B', 'C', 'D'][place.min(3)])
        })
        .collect();
    WeightClasses {
        labels,
        weights: classes,
    }
}

/// The Postgres `tsquery` text an FTS5 expression asks for, for
/// `to_tsquery` over an index's configuration: phrases and prefixes, AND
/// (also implicit), OR, NOT and parentheses, with FTS5's precedence (NOT,
/// then AND, then OR). What FTS5 would refuse is refused with FTS5's words
/// where it has them (`fts5: syntax error near "x"`, `unterminated
/// string`), and so is what Postgres has no match for: a NEAR group and
/// `^`. Each phrase's text goes to `to_tsquery` quoted, so the index's
/// parser splits it into the words it indexed, and a phrase that holds no
/// word matches nothing, as in FTS5.
pub fn tsquery(expression: &str) -> std::result::Result<String, QueryRefusal> {
    let tokens = lex(expression);
    let mut parser = Parser {
        tokens,
        at: 0,
        depth: 0,
    };
    let node = parser.or_expr()?;
    match parser.peek() {
        Tok::Eof => Ok(node.render()),
        other => Err(parser.refusal(other)),
    }
}

// A token of an FTS5 expression, as fts5ExprGetToken reads it.
#[derive(Clone, Debug, PartialEq)]
enum Tok {
    Lp,
    Rp,
    Lcp,
    Rcp,
    Colon,
    Comma,
    Plus,
    Star,
    Minus,
    Caret,
    And,
    Or,
    Not,
    /// A bareword or a quoted string: its text (a quoted one's with each
    /// `""` one `"`) and as written.
    Str {
        text: String,
        raw: String,
    },
    /// What the lexer refused, where the parser reaches it.
    Error(String),
    Eof,
}

impl Tok {
    fn raw(&self) -> &str {
        match self {
            Tok::Lp => "(",
            Tok::Rp => ")",
            Tok::Lcp => "{",
            Tok::Rcp => "}",
            Tok::Colon => ":",
            Tok::Comma => ",",
            Tok::Plus => "+",
            Tok::Star => "*",
            Tok::Minus => "-",
            Tok::Caret => "^",
            Tok::And => "AND",
            Tok::Or => "OR",
            Tok::Not => "NOT",
            Tok::Str { raw, .. } => raw,
            Tok::Error(_) | Tok::Eof => "",
        }
    }
}

// is_bareword is FTS5's bareword character: an ASCII letter or digit, `_`,
// 0x1A, or any character past ASCII.
fn is_bareword(c: char) -> bool {
    c.is_ascii_alphanumeric() || c == '_' || c == '\u{1a}' || !c.is_ascii()
}

// lex reads an expression's tokens; the first character FTS5 refuses, or
// an unterminated string, ends them with an Error.
fn lex(expression: &str) -> Vec<Tok> {
    let mut tokens = Vec::new();
    let mut chars = expression.char_indices().peekable();
    while let Some((at, c)) = chars.next() {
        let token = match c {
            ' ' | '\t' | '\n' | '\r' => continue,
            '(' => Tok::Lp,
            ')' => Tok::Rp,
            '{' => Tok::Lcp,
            '}' => Tok::Rcp,
            ':' => Tok::Colon,
            ',' => Tok::Comma,
            '+' => Tok::Plus,
            '*' => Tok::Star,
            '-' => Tok::Minus,
            '^' => Tok::Caret,
            '"' => {
                let mut text = String::new();
                let mut end = None;
                while let Some((position, next)) = chars.next() {
                    if next == '"' {
                        if chars.peek().is_some_and(|(_, after)| *after == '"') {
                            chars.next();
                            text.push('"');
                            continue;
                        }
                        end = Some(position + 1);
                        break;
                    }
                    text.push(next);
                }
                match end {
                    Some(end) => Tok::Str {
                        text,
                        raw: expression[at..end].to_owned(),
                    },
                    None => {
                        tokens.push(Tok::Error("unterminated string".to_owned()));
                        break;
                    }
                }
            }
            c if is_bareword(c) => {
                let mut end = at + c.len_utf8();
                while let Some((position, next)) = chars.peek().copied() {
                    if !is_bareword(next) {
                        break;
                    }
                    end = position + next.len_utf8();
                    chars.next();
                }
                let word = &expression[at..end];
                match word {
                    "AND" => Tok::And,
                    "OR" => Tok::Or,
                    "NOT" => Tok::Not,
                    _ => Tok::Str {
                        text: word.to_owned(),
                        raw: word.to_owned(),
                    },
                }
            }
            other => {
                tokens.push(Tok::Error(QueryRefusal::near(&other.to_string()).message));
                break;
            }
        };
        tokens.push(token);
    }
    tokens.push(Tok::Eof);
    tokens
}

// A parsed expression.
#[derive(Debug)]
enum Node {
    Or(Vec<Node>),
    And(Vec<Node>),
    Not(Box<Node>, Box<Node>),
    /// The parts of a phrase, each a string's text and whether a `*`
    /// follows it.
    Phrase(Vec<(String, bool)>),
}

impl Node {
    fn render(&self) -> String {
        let group = |nodes: &[Node], operator: &str| {
            nodes
                .iter()
                .map(|node| format!("({})", node.render()))
                .collect::<Vec<_>>()
                .join(operator)
        };
        match self {
            Node::Or(nodes) => group(nodes, " | "),
            Node::And(nodes) => group(nodes, " & "),
            Node::Not(left, right) => format!("({}) & !({})", left.render(), right.render()),
            Node::Phrase(parts) => {
                // A string of no letter or digit holds no word: FTS5 adds
                // no token to the phrase for it, and to_tsquery would count
                // it as a gap. A phrase of none is an operand that holds no
                // word (an empty one is a syntax error), which matches
                // nothing alone and drops out beside others, as in FTS5.
                let words: Vec<String> = parts
                    .iter()
                    .filter(|(text, _)| text.chars().any(char::is_alphanumeric))
                    .map(|(text, star)| phrase_part(text, *star))
                    .collect();
                if words.is_empty() {
                    "' '".to_owned()
                } else {
                    words.join(" <-> ")
                }
            }
        }
    }
}

// phrase_part is one string of a phrase as to_tsquery takes it: quoted,
// and with a `*` its last word a prefix, as FTS5 makes the last token of
// the string a prefix.
fn phrase_part(text: &str, star: bool) -> String {
    let text = text.replace('\0', " ");
    if !star {
        return quote_operand(&text);
    }
    let trimmed = text.trim_end();
    let (head, last) = match trimmed.rfind(char::is_whitespace) {
        Some(at) => {
            let width = trimmed[at..].chars().next().map_or(1, char::len_utf8);
            (&trimmed[..at], &trimmed[at + width..])
        }
        None => ("", trimmed),
    };
    if last.is_empty() {
        return quote_operand(&text);
    }
    if head.trim().is_empty() {
        format!("{}:*", quote_operand(last))
    } else {
        format!("{} <-> {}:*", quote_operand(head), quote_operand(last))
    }
}

// quote_operand quotes text as a to_tsquery operand: a quote doubled and
// a backslash escaped.
fn quote_operand(text: &str) -> String {
    format!("'{}'", text.replace('\\', "\\\\").replace('\'', "''"))
}

struct Parser {
    tokens: Vec<Tok>,
    at: usize,
    depth: usize,
}

type Parsed = std::result::Result<Node, QueryRefusal>;

impl Parser {
    fn peek(&self) -> Tok {
        self.tokens.get(self.at).cloned().unwrap_or(Tok::Eof)
    }

    fn peek_at(&self, offset: usize) -> Tok {
        self.tokens
            .get(self.at + offset)
            .cloned()
            .unwrap_or(Tok::Eof)
    }

    fn advance(&mut self) {
        self.at += 1;
    }

    // refusal is the error at a token: the lexer's own, or a syntax error
    // near it.
    fn refusal(&self, token: Tok) -> QueryRefusal {
        match token {
            Tok::Error(message) => QueryRefusal::new(message),
            other => QueryRefusal::near(other.raw()),
        }
    }

    fn or_expr(&mut self) -> Parsed {
        let mut nodes = vec![self.and_expr()?];
        while self.peek() == Tok::Or {
            self.advance();
            nodes.push(self.and_expr()?);
        }
        Ok(one_or(nodes, Node::Or))
    }

    fn and_expr(&mut self) -> Parsed {
        let mut nodes = vec![self.not_expr()?];
        while self.peek() == Tok::And {
            self.advance();
            nodes.push(self.not_expr()?);
        }
        Ok(one_or(nodes, Node::And))
    }

    fn not_expr(&mut self) -> Parsed {
        let mut node = self.unit()?;
        while self.peek() == Tok::Not {
            self.advance();
            let right = self.unit()?;
            node = Node::Not(Box::new(node), Box::new(right));
        }
        Ok(node)
    }

    // unit is a parenthesized expression, or nearsets side by side, which
    // FTS5 joins with an implicit AND.
    fn unit(&mut self) -> Parsed {
        if self.peek() == Tok::Lp {
            if self.depth >= MAX_NESTING {
                return Err(QueryRefusal::new("fts5: parser stack overflow"));
            }
            self.depth += 1;
            self.advance();
            let node = self.or_expr()?;
            match self.peek() {
                Tok::Rp => self.advance(),
                other => return Err(self.refusal(other)),
            }
            self.depth -= 1;
            return Ok(node);
        }
        let mut nodes = vec![self.nearset()?];
        while matches!(self.peek(), Tok::Str { .. } | Tok::Caret) {
            nodes.push(self.nearset()?);
        }
        Ok(one_or(nodes, Node::And))
    }

    fn nearset(&mut self) -> Parsed {
        match self.peek() {
            Tok::Caret => Err(QueryRefusal::new(
                "^, a match at the start of a field, is SQLite's alone: on Postgres an expression takes phrases, prefixes (word*), AND, OR, NOT and parentheses",
            )),
            Tok::Str { raw, .. } if self.peek_at(1) == Tok::Lp => {
                if raw == "NEAR" {
                    Err(QueryRefusal::new(
                        "NEAR groups are SQLite's alone: on Postgres an expression takes phrases, prefixes (word*), AND, OR, NOT and parentheses",
                    ))
                } else {
                    Err(QueryRefusal::near(&raw))
                }
            }
            Tok::Str { .. } => self.phrase(),
            other => Err(self.refusal(other)),
        }
    }

    fn phrase(&mut self) -> Parsed {
        let mut parts = Vec::new();
        loop {
            let text = match self.peek() {
                Tok::Str { text, .. } => text,
                other => return Err(self.refusal(other)),
            };
            self.advance();
            let star = self.peek() == Tok::Star;
            if star {
                self.advance();
            }
            parts.push((text, star));
            if self.peek() != Tok::Plus {
                break;
            }
            self.advance();
        }
        Ok(Node::Phrase(parts))
    }
}

fn one_or(mut nodes: Vec<Node>, many: fn(Vec<Node>) -> Node) -> Node {
    if nodes.len() == 1 {
        nodes.remove(0)
    } else {
        many(nodes)
    }
}

/// The SQL names of an index's text search configuration (Postgres), by
/// the behavior's prefix and its own name for the index.
pub(crate) fn config_name(prefix: &str, index: &str) -> String {
    identifier(Dialect::Postgres, &format!("{prefix}{index}_config")).into_owned()
}

// vector_column is the generated tsvector of a column (Postgres).
fn vector_column(column: &str) -> String {
    identifier(Dialect::Postgres, &format!("{column}_tsv")).into_owned()
}

/// The name of the generated tsvector of every column, which the GIN
/// index covers (Postgres).
pub const ALL_VECTOR: &str = "all_tsv";

/// What is wrong with an index's columns.
pub(crate) fn column_problems(columns: &[&str]) -> Vec<String> {
    let mut problems = Vec::new();
    if columns.is_empty() || columns.len() > MAX_COLUMNS {
        problems.push(format!(
            "a full-text index has 1 to {MAX_COLUMNS} columns, not {}",
            columns.len()
        ));
    }
    for (position, column) in columns.iter().enumerate() {
        if !crate::ddl::is_column_name(column) {
            problems.push(format!(
                "full-text column {column:?} must match ^[a-z][a-z0-9_]{{0,62}}$"
            ));
        } else if *column == "rowid" || column.ends_with("_tsv") {
            problems.push(format!(
                "full-text column {column} takes a name the index keeps (rowid, *_tsv)"
            ));
        }
        if columns[..position].contains(column) {
            problems.push(format!("full-text column {column} is named twice"));
        }
    }
    problems
}

/// The statement that creates an FTS5 index (SQLite), as the TypeScript
/// engine writes it.
pub(crate) fn sqlite_create(table: &str, columns: &[&str]) -> String {
    format!(
        "CREATE VIRTUAL TABLE {table} USING fts5({}, tokenize = {})",
        columns.join(", "),
        sql_string(SQLITE_TOKENIZER)
    )
}

/// The statements that create an index on Postgres once `unaccent` lives
/// in `extension`: its configuration, its table with the generated
/// vectors, and the GIN index over all of them.
pub(crate) fn postgres_create(
    table: &str,
    config: &str,
    extension: &str,
    columns: &[&str],
) -> Vec<String> {
    let regconfig = format!("CAST({} AS regconfig)", sql_string(config));
    let mut lines = vec!["rowid BIGINT PRIMARY KEY".to_owned()];
    lines.extend(
        columns
            .iter()
            .map(|column| format!("{} TEXT", quote_identifier(column))),
    );
    lines.extend(columns.iter().map(|column| {
        format!(
            "{} tsvector GENERATED ALWAYS AS (to_tsvector({regconfig}, COALESCE({}, ''))) STORED",
            quote_identifier(&vector_column(column)),
            quote_identifier(column)
        )
    }));
    let all = columns
        .iter()
        .map(|column| format!("COALESCE({}, '')", quote_identifier(column)))
        .collect::<Vec<_>>()
        .join(" || ' ' || ");
    lines.push(format!(
        "{ALL_VECTOR} tsvector GENERATED ALWAYS AS (to_tsvector({regconfig}, {all})) STORED"
    ));
    vec![
        format!(
            "CREATE TEXT SEARCH CONFIGURATION {} (PARSER = pg_catalog.\"default\")",
            quote_identifier(config)
        ),
        format!(
            "ALTER TEXT SEARCH CONFIGURATION {} ADD MAPPING FOR {} WITH {}.unaccent, pg_catalog.simple",
            quote_identifier(config),
            POSTGRES_TOKEN_TYPES.join(", "),
            quote_identifier(extension)
        ),
        format!(
            "CREATE TABLE {} (\n  {}\n)",
            quote_identifier(table),
            lines.join(",\n  ")
        ),
        format!(
            "CREATE INDEX {} ON {} USING gin ({ALL_VECTOR})",
            quote_identifier(&identifier(Dialect::Postgres, &format!("{table}_words"))),
            quote_identifier(table)
        ),
    ]
}

/// The statement that finds the schema `unaccent` lives in (Postgres).
pub(crate) const UNACCENT_SCHEMA: &str = "SELECT n.nspname::text AS schema
     FROM pg_catalog.pg_extension AS e
     JOIN pg_catalog.pg_namespace AS n ON n.oid = e.extnamespace
     WHERE e.extname = 'unaccent'";

/// The statements that create `unaccent` in [`EXTENSION_SCHEMA`]
/// (Postgres), each run in a savepoint whose duplicate error another
/// engine's create, at the same moment, explains.
pub(crate) fn unaccent_create() -> [String; 2] {
    [
        format!("CREATE SCHEMA IF NOT EXISTS {EXTENSION_SCHEMA}"),
        format!("CREATE EXTENSION IF NOT EXISTS unaccent WITH SCHEMA {EXTENSION_SCHEMA}"),
    ]
}

/// The SQL names a match runs over, resolved by the behavior's prefix.
pub(crate) struct Names<'a> {
    pub index: &'a str,
    pub keys: &'a str,
    /// The index's configuration (Postgres).
    pub config: &'a str,
}

/// The statement of a match and its parameters, with the expression as
/// the database takes it (on Postgres, translated by [`tsquery`]).
pub(crate) fn match_statement(
    dialect: Dialect,
    names: &Names<'_>,
    query: &FullTextQuery<'_>,
    expression: &str,
) -> (String, Vec<SqlValue>) {
    match dialect {
        Dialect::Sqlite => sqlite_match(names, query, expression),
        Dialect::Postgres => postgres_match(names, query, expression),
    }
}

// sqlite_match is the TypeScript engine's statement: the match drives the
// query (CROSS JOIN keeps it the outer loop, so a search's cost follows its
// matches rather than the rows of keys), bm25 with a weight per column,
// then the key.
fn sqlite_match(
    names: &Names<'_>,
    query: &FullTextQuery<'_>,
    expression: &str,
) -> (String, Vec<SqlValue>) {
    let index = names.index;
    let key = quote_identifier(query.key);
    let mut params = Vec::new();
    let mut columns: Vec<String> = query
        .select
        .iter()
        .map(|column| format!("k.{0} AS {0}", quote_identifier(column)))
        .collect();
    columns.push(format!("k.{key} AS {key}"));
    for position in 0..query.snippets.columns {
        columns.push(format!(
            "snippet({index}, {position}, ?, ?, ?, {}) AS s{position}",
            query.snippets.words
        ));
        params.extend([
            SqlValue::from(query.snippets.open),
            SqlValue::from(query.snippets.close),
            SqlValue::from(query.snippets.ellipsis),
        ]);
    }
    params.push(SqlValue::from(expression));
    let mut conditions = vec![format!("{index} MATCH ?")];
    for (column, value) in query.filters {
        conditions.push(format!("k.{} = ?", quote_identifier(column)));
        params.push(value.clone());
    }
    conditions.push(format!("k.{key} IS NOT ?"));
    params.push(query.exclude.map_or(SqlValue::Null, SqlValue::Integer));
    params.extend(query.weights.iter().map(|weight| SqlValue::Real(*weight)));
    params.extend([
        SqlValue::Integer(query.limit),
        SqlValue::Integer(query.offset),
    ]);
    let sql = format!(
        "SELECT {}
         FROM {index} CROSS JOIN {} AS k ON k.{key} = {index}.rowid
         WHERE {}
         ORDER BY bm25({index}, {}), k.{key}
         LIMIT ? OFFSET ?",
        columns.join(", "),
        names.keys,
        conditions.join(" AND "),
        vec!["?"; query.weights.len()].join(", ")
    );
    (sql, params)
}

// postgres_match ranks the page in a subquery (ts_rank_cd over the
// columns' vectors, labelled by weight class, then the key) and writes the
// headlines of that page alone, since ts_headline reads the text again.
fn postgres_match(
    names: &Names<'_>,
    query: &FullTextQuery<'_>,
    tsquery: &str,
) -> (String, Vec<SqlValue>) {
    let config = format!("CAST({} AS regconfig)", sql_string(names.config));
    let key = quote_identifier(query.key);
    let classes = weight_classes(query.weights);
    let weighted: Vec<String> = query
        .columns
        .iter()
        .zip(&classes.labels)
        .filter_map(|(column, label)| {
            label.map(|label| {
                format!(
                    "setweight(t.{}, '{label}')",
                    quote_identifier(&vector_column(column))
                )
            })
        })
        .collect();
    let rank = if weighted.is_empty() {
        format!("ts_rank_cd(t.{ALL_VECTOR}, q.q)")
    } else {
        let [d, c, b, a] = classes.weights;
        format!(
            "ts_rank_cd(CAST('{{{d}, {c}, {b}, {a}}}' AS real[]), {}, q.q)",
            weighted.join(" || ")
        )
    };
    let shown = query.snippets.columns.min(query.columns.len());
    let mut params = Vec::new();
    let mut outer: Vec<String> = query
        .select
        .iter()
        .map(|column| format!("m.{}", quote_identifier(column)))
        .collect();
    outer.push(format!("m.{key}"));
    let options = headline_options(&query.snippets);
    for position in 0..shown {
        outer.push(format!(
            "ts_headline({config}, m.c{position}, m.q, ?) AS h{position}"
        ));
        params.push(SqlValue::from(options.as_str()));
    }
    for position in 0..shown {
        outer.push(format!("m.c{position} AS t{position}"));
    }
    let mut inner: Vec<String> = query
        .select
        .iter()
        .map(|column| format!("k.{0} AS {0}", quote_identifier(column)))
        .collect();
    inner.push(format!("k.{key} AS {key}"));
    for (position, column) in query.columns.iter().take(shown).enumerate() {
        inner.push(format!("t.{} AS c{position}", quote_identifier(column)));
    }
    inner.push("q.q AS q".to_owned());
    inner.push(format!("{rank} AS rank"));
    params.push(SqlValue::from(tsquery));
    let mut conditions = vec![format!("t.{ALL_VECTOR} @@ q.q")];
    for (column, value) in query.filters {
        conditions.push(format!("k.{} = ?", quote_identifier(column)));
        params.push(value.clone());
    }
    conditions.push(format!("k.{key} IS DISTINCT FROM ?"));
    params.push(query.exclude.map_or(SqlValue::Null, SqlValue::Integer));
    params.extend([
        SqlValue::Integer(query.limit),
        SqlValue::Integer(query.offset),
    ]);
    let sql = format!(
        "SELECT {}
         FROM (
           SELECT {}
           FROM {} AS t JOIN {} AS k ON k.{key} = t.rowid
             CROSS JOIN to_tsquery({config}, ?) AS q(q)
           WHERE {}
           ORDER BY rank DESC, k.{key}
           LIMIT ? OFFSET ?
         ) AS m
         ORDER BY m.rank DESC, m.{key}",
        outer.join(", "),
        inner.join(", "),
        names.index,
        names.keys,
        conditions.join(" AND ")
    );
    (sql, params)
}

// headline_options are ts_headline's options for a snippet: the marks, at
// most `words` words and at least half as many, no short word trimmed,
// one fragment.
fn headline_options(snippets: &Snippets<'_>) -> String {
    let quoted = |value: &str| format!("\"{}\"", value.replace('"', "\"\""));
    let words = snippets.words.max(2);
    format!(
        "StartSel={}, StopSel={}, MaxWords={words}, MinWords={}, ShortWord=0, HighlightAll=false, MaxFragments=0",
        quoted(snippets.open),
        quoted(snippets.close),
        (words / 2).max(1)
    )
}

/// The hits of a match's rows: on SQLite each row's snippets as FTS5 wrote
/// them; on Postgres each headline with the ellipsis where it leaves out
/// text of its column before or after, as FTS5's snippet has it.
pub(crate) fn hits(
    dialect: Dialect,
    query: &FullTextQuery<'_>,
    rows: Vec<Row>,
) -> Vec<FullTextHit> {
    let kept = query.select.len() + 1;
    let shown = match dialect {
        Dialect::Sqlite => query.snippets.columns,
        Dialect::Postgres => query.snippets.columns.min(query.columns.len()),
    };
    rows.into_iter()
        .map(|row| {
            let columns: Vec<String> = row.columns()[..kept].to_vec();
            let values = row.values();
            let text = |value: &SqlValue| value.as_str().map(str::to_owned);
            let snippets = (0..shown)
                .map(|position| match dialect {
                    Dialect::Sqlite => text(&values[kept + position]),
                    Dialect::Postgres => {
                        let headline = values[kept + position].as_str();
                        let whole = values[kept + shown + position].as_str();
                        headline.map(|headline| {
                            with_ellipsis(headline, whole.unwrap_or_default(), &query.snippets)
                        })
                    }
                })
                .collect();
            FullTextHit {
                row: Row::new(columns.into(), values[..kept].to_vec()),
                snippets,
            }
        })
        .collect()
}

// with_ellipsis puts the ellipsis before a headline that starts after its
// column's text does, and after one that ends before it.
fn with_ellipsis(headline: &str, whole: &str, snippets: &Snippets<'_>) -> String {
    let plain = headline
        .replace(snippets.open, "")
        .replace(snippets.close, "");
    let Some(at) = whole.find(&plain) else {
        return headline.to_owned();
    };
    let mut out = String::with_capacity(headline.len() + 2 * snippets.ellipsis.len());
    if at > 0 {
        out.push_str(snippets.ellipsis);
    }
    out.push_str(headline);
    if at + plain.len() < whole.len() {
        out.push_str(snippets.ellipsis);
    }
    out
}

/// Whether a SQLite error of a match is FTS5's refusal of its expression:
/// its syntax, its depth, a special query, or a column exclusion
/// (`-word`), which names a column.
pub(crate) fn is_sqlite_query_error(message: &str) -> bool {
    ["fts5:", "fts5 "]
        .iter()
        .chain(&[
            "unterminated string",
            "expected integer",
            "unknown special query",
            "no such column",
        ])
        .any(|prefix| message.starts_with(prefix))
}

/// A refusal of a behavior's own expression: a defect of the behavior's.
pub(crate) fn own_expression_refused(behavior: &str, refusal: &QueryRefusal) -> Error {
    Error::behavior(
        behavior,
        format!("its full-text expression is not one the index takes: {refusal}"),
    )
}

/// Checks a match's names and shapes before any statement runs.
pub(crate) fn query_problems(query: &FullTextQuery<'_>) -> Vec<String> {
    let mut problems = column_problems(query.columns);
    if query.weights.len() != query.columns.len() {
        problems.push(format!(
            "a full-text match gives a weight per column: {} weights for {} columns",
            query.weights.len(),
            query.columns.len()
        ));
    }
    if query
        .weights
        .iter()
        .any(|weight| !weight.is_finite() || *weight < 0.0)
    {
        problems.push("a full-text weight is a finite number, 0 or more".to_owned());
    }
    if query.snippets.columns > query.columns.len() {
        problems.push(format!(
            "a full-text match writes snippets of at most its {} columns, not {}",
            query.columns.len(),
            query.snippets.columns
        ));
    }
    for name in query
        .select
        .iter()
        .chain(std::iter::once(&query.key))
        .chain(query.filters.iter().map(|(column, _)| column))
    {
        if !crate::ddl::is_column_name(name) {
            problems.push(format!(
                "column {name:?} of a full-text match must match ^[a-z][a-z0-9_]{{0,62}}$"
            ));
        }
    }
    if query.limit < 0 || query.offset < 0 {
        problems.push("a full-text match's limit and offset are 0 or more".to_owned());
    }
    problems
}

#[cfg(test)]
mod tests {
    use super::*;

    fn translated(expression: &str) -> String {
        tsquery(expression).unwrap_or_else(|refusal| panic!("{expression}: {refusal}"))
    }

    fn refused(expression: &str) -> String {
        tsquery(expression).expect_err(expression).message
    }

    #[test]
    fn an_fts5_expression_becomes_a_tsquery_with_fts5s_precedence() {
        assert_eq!(translated("walnut"), "'walnut'");
        assert_eq!(translated("\"walnut\" \"desk\""), "('walnut') & ('desk')");
        assert_eq!(translated("wood OR chair"), "('wood') | ('chair')");
        assert_eq!(translated("\"walnut desk\""), "'walnut desk'");
        assert_eq!(translated("walnut + desk"), "'walnut' <-> 'desk'");
        assert_eq!(translated("wal*"), "'wal':*");
        assert_eq!(translated("\"walnut de\"*"), "'walnut' <-> 'de':*");
        assert_eq!(translated("desk NOT oak"), "('desk') & !('oak')");
        assert_eq!(
            translated("a OR b c NOT d AND e"),
            "('a') | (((('b') & ('c')) & !('d')) & ('e'))"
        );
        assert_eq!(translated("(a OR b) AND c"), "(('a') | ('b')) & ('c')");
        assert_eq!(translated("\"it's\" \"a\\b\""), "('it''s') & ('a\\\\b')");
        assert_eq!(translated("\"say \"\"hi\"\"\""), "'say \"hi\"'");
        assert_eq!(translated("café"), "'café'");
        assert_eq!(translated("\"\""), "' '");
        assert_eq!(translated("\"-\" OR desk"), "(' ') | ('desk')");
        assert_eq!(translated("walnut + \"-\" + desk"), "'walnut' <-> 'desk'");
        assert_eq!(
            translated("and or not near"),
            "('and') & ('or') & ('not') & ('near')"
        );
    }

    #[test]
    fn what_fts5_refuses_is_refused_in_its_words_and_near_and_caret_are_sqlites() {
        assert_eq!(refused("walnut AND"), "fts5: syntax error near \"\"");
        assert_eq!(refused("walnut)"), "fts5: syntax error near \")\"");
        assert_eq!(refused("\"walnut"), "unterminated string");
        assert_eq!(refused("-wood"), "fts5: syntax error near \"-\"");
        assert_eq!(refused("*"), "fts5: syntax error near \"*\"");
        assert_eq!(refused("C++"), "fts5: syntax error near \"+\"");
        assert_eq!(refused("a ? b"), "fts5: syntax error near \"?\"");
        assert_eq!(refused("NOT walnut"), "fts5: syntax error near \"NOT\"");
        assert_eq!(
            refused("walnut (desk)"),
            "fts5: syntax error near \"walnut\""
        );
        assert_eq!(refused("foo(bar)"), "fts5: syntax error near \"foo\"");
        assert!(refused("NEAR(walnut desk, 3)").starts_with("NEAR groups are SQLite's alone"));
        assert!(refused("^walnut").starts_with("^, a match at the start of a field"));
        let deep = format!("{}walnut{}", "(".repeat(200), ")".repeat(200));
        assert_eq!(refused(&deep), "fts5: parser stack overflow");
        let fine = format!("{}walnut{}", "(".repeat(50), ")".repeat(50));
        assert_eq!(translated(&fine), "'walnut'");
    }

    #[test]
    fn weights_fall_into_four_classes_highest_first() {
        let classes = weight_classes(&[3.0, 1.0, 0.0, 0.0]);
        assert_eq!(classes.labels, [Some('A'), Some('B'), None, None]);
        assert_eq!(classes.weights, [0.0, 0.0, 1.0 / 3.0, 1.0]);
        let one = weight_classes(&[1.0, 1.0]);
        assert_eq!(one.labels, [Some('A'), Some('A')]);
        assert_eq!(one.weights, [0.0, 0.0, 0.0, 1.0]);
        let many = weight_classes(&[1.0, 2.0, 3.0, 4.0, 5.0, 6.0]);
        assert_eq!(
            many.labels,
            [
                Some('D'),
                Some('D'),
                Some('D'),
                Some('C'),
                Some('B'),
                Some('A')
            ]
        );
        assert_eq!(many.weights, [3.0 / 6.0, 4.0 / 6.0, 5.0 / 6.0, 1.0]);
    }

    #[test]
    fn a_headline_takes_an_ellipsis_where_it_leaves_text_out() {
        let snippets = Snippets {
            columns: 1,
            open: "<",
            close: ">",
            ellipsis: "...",
            words: 12,
        };
        assert_eq!(with_ellipsis("A <fox>", "A fox", &snippets), "A <fox>");
        assert_eq!(
            with_ellipsis("<fox> ran", "A fox ran by.", &snippets),
            "...<fox> ran..."
        );
        assert_eq!(
            with_ellipsis("<fox> ran by.", "A fox ran by.", &snippets),
            "...<fox> ran by."
        );
        assert_eq!(with_ellipsis("other", "A fox", &snippets), "other");
    }

    #[test]
    fn sqlite_says_which_errors_refuse_the_expression() {
        for message in [
            "fts5: syntax error near \"\"",
            "unterminated string",
            "unknown special query: ",
            "no such column: wood",
            "expected integer, got \"x\"",
        ] {
            assert!(is_sqlite_query_error(message), "{message}");
        }
        assert!(!is_sqlite_query_error("database is locked"));
    }
}
