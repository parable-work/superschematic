//! The query syntax of search across tables (#740, D155): what
//! [`search_tables`](super::search_tables) reads a query's text as, the
//! TypeScript runtime's `search-query.ts` and the Go runtime's
//! `crosstable/query.go` term for term, and what it asks Postgres and
//! SQLite's FTS5 for.
//!
//! A query is read once into a plan:
//!
//! - A term is a quoted phrase (`"walnut desk"`) or a run of characters
//!   that is neither white space nor a quote (`walnut`). A bare term that
//!   ends in `*` is a prefix (`wal*`). A term after `-` is excluded
//!   (`-oak`, `-"oak chair"`); a `+` before one says it is required, as
//!   every term is.
//! - `OR`, upper case, between two terms puts them in one group: a row
//!   holds one of a group's terms. A row matches when it holds every group
//!   and no excluded term. `OR` binds the terms beside it before the groups
//!   are joined: `walnut oak OR pine` is walnut, and oak or pine.
//! - A term with no letter or digit, an `OR` with no term before it, and
//!   an `OR` beside an excluded term are dropped. The first
//!   [`MAX_QUERY_TERMS`] terms count, each its first 200 characters.
//!
//! A term's words are the database's: on Postgres a term is
//! `phraseto_tsquery` of its text and a prefix `to_tsquery` of its text
//! quoted with `:*`, under the index's text search configuration; on
//! SQLite each is a quoted FTS5 phrase, a prefix with `*` after it. Every
//! text is a bound value.

use serde::{Deserialize, Serialize};

/// One term of a query: its text, a phrase of the words the database
/// finds in it, and whether its last word is a prefix.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct SearchTerm {
    /// The term's text.
    pub text: String,
    /// Whether its last word is a prefix.
    pub prefix: bool,
}

/// A query read: groups a row must each hold one term of, and terms it
/// must hold none of.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SearchPlan {
    /// The groups, each of the terms a row holds one of.
    pub all: Vec<Vec<SearchTerm>>,
    /// The terms a row holds none of.
    pub none: Vec<SearchTerm>,
}

/// The most terms of a query that count.
pub const MAX_QUERY_TERMS: usize = 16;
const MAX_TERM: usize = 200;

struct Token {
    or: bool,
    text: String,
    prefix: bool,
    exclude: bool,
}

fn is_mark(c: char) -> bool {
    matches!(c, '\u{0}' | '\u{2}' | '\u{3}')
}

fn tokens(q: &str) -> Vec<Token> {
    let chars: Vec<char> = q.chars().filter(|c| !is_mark(*c)).collect();
    let mut out = Vec::new();
    let mut i = 0;
    while i < chars.len() {
        if chars[i].is_whitespace() {
            i += 1;
            continue;
        }
        let mut sign = None;
        if (chars[i] == '-' || chars[i] == '+')
            && i + 1 < chars.len()
            && !chars[i + 1].is_whitespace()
        {
            sign = Some(chars[i]);
            i += 1;
        }
        let exclude = sign == Some('-');
        let mut text = String::new();
        if chars[i] == '"' {
            i += 1;
            while i < chars.len() && chars[i] != '"' {
                text.push(chars[i]);
                i += 1;
            }
            i += 1;
            out.push(Token {
                or: false,
                text,
                prefix: false,
                exclude,
            });
            continue;
        }
        while i < chars.len() && !chars[i].is_whitespace() && chars[i] != '"' {
            text.push(chars[i]);
            i += 1;
        }
        if sign.is_none() && text == "OR" {
            out.push(Token {
                or: true,
                text,
                prefix: false,
                exclude: false,
            });
            continue;
        }
        let bare = text.trim_end_matches('*').to_owned();
        let prefix = bare.len() != text.len();
        out.push(Token {
            or: false,
            text: bare,
            prefix,
            exclude,
        });
    }
    out
}

/// Reads a query's text into its plan (#740).
pub fn search_plan(q: &str) -> SearchPlan {
    let mut plan = SearchPlan::default();
    let mut count = 0;
    let (mut after_term, mut join) = (false, false);
    for token in tokens(q) {
        if token.or {
            if after_term {
                join = true;
            }
            continue;
        }
        if count == MAX_QUERY_TERMS {
            break;
        }
        if !token
            .text
            .chars()
            .any(|c| c.is_alphabetic() || c.is_numeric())
        {
            continue;
        }
        count += 1;
        let term = SearchTerm {
            text: token.text.chars().take(MAX_TERM).collect(),
            prefix: token.prefix,
        };
        if token.exclude {
            plan.none.push(term);
            after_term = false;
        } else {
            match plan.all.last_mut() {
                Some(group) if join => group.push(term),
                _ => plan.all.push(vec![term]),
            }
            after_term = true;
        }
        join = false;
    }
    plan
}

/// A term as `to_tsquery` takes a prefix: its text quoted, a quote
/// doubled and a backslash escaped, then `:*`.
fn prefix_operand(text: &str) -> String {
    format!("'{}':*", text.replace('\\', "\\\\").replace('\'', "''"))
}

/// The plan as a tsquery expression of Postgres's under `config`, a
/// quoted regconfig: each term `phraseto_tsquery` or `to_tsquery` of a
/// value `param` binds, a group `||` of its terms, the groups `&&` and
/// each excluded term `&& !!`. The plan's `none` alone when it has no
/// group, which a search by vector alone excludes by.
pub fn postgres_query(
    plan: &SearchPlan,
    config: &str,
    param: &mut dyn FnMut(String) -> String,
) -> String {
    let mut term = |t: &SearchTerm| {
        if t.prefix {
            format!(
                "to_tsquery({config}, {}::text)",
                param(prefix_operand(&t.text))
            )
        } else {
            format!(
                "phraseto_tsquery({config}, {}::text)",
                param(t.text.clone())
            )
        }
    };
    let mut parts = Vec::new();
    if plan.all.is_empty() {
        for t in &plan.none {
            parts.push(term(t));
        }
        return parts.join(" || ");
    }
    for group in &plan.all {
        let terms: Vec<String> = group.iter().map(&mut term).collect();
        parts.push(if terms.len() == 1 {
            terms.into_iter().next().unwrap_or_default()
        } else {
            format!("({})", terms.join(" || "))
        });
    }
    for t in &plan.none {
        parts.push(format!("!!{}", term(t)));
    }
    parts.join(" && ")
}

/// A term as an FTS5 phrase: its text quoted, a quote doubled, and `*`
/// after a prefix.
fn fts_phrase(t: &SearchTerm) -> String {
    format!(
        "\"{}\"{}",
        t.text.replace('"', "\"\""),
        if t.prefix { "*" } else { "" }
    )
}

/// The plan as an FTS5 expression: each term a quoted phrase, a group
/// `OR` of its terms, the groups `AND` and each excluded term `NOT`; none
/// for a plan with no group.
pub fn fts_query(plan: &SearchPlan) -> Option<String> {
    if plan.all.is_empty() {
        return None;
    }
    let groups: Vec<String> = plan
        .all
        .iter()
        .map(|group| {
            if group.len() == 1 {
                fts_phrase(&group[0])
            } else {
                format!(
                    "({})",
                    group
                        .iter()
                        .map(fts_phrase)
                        .collect::<Vec<_>>()
                        .join(" OR ")
                )
            }
        })
        .collect();
    let mut expression = groups.join(" AND ");
    if !plan.none.is_empty() {
        expression = format!("({expression})");
        for t in &plan.none {
            expression.push_str(" NOT ");
            expression.push_str(&fts_phrase(t));
        }
    }
    Some(expression)
}

#[cfg(test)]
mod tests {
    use super::*;

    /// The parity file's plans (#740), which the TypeScript runtime writes
    /// and the Go runtime runs too: the plan each query reads as, the
    /// Postgres query it renders under the simple configuration, and its
    /// FTS5 match.
    #[test]
    fn the_parity_vectors_hold() {
        let path = concat!(
            env!("CARGO_MANIFEST_DIR"),
            "/../testdata/search_tables_parity.json"
        );
        let file: serde_json::Value =
            serde_json::from_str(&std::fs::read_to_string(path).expect("the parity file"))
                .expect("the parity file is JSON");
        let plans = file["plans"].as_array().expect("plans");
        assert!(!plans.is_empty());
        for case in plans {
            let name = case["name"].as_str().unwrap_or_default();
            let q = case["q"].as_str().unwrap_or_default();
            let plan = search_plan(q);
            let want: SearchPlan = serde_json::from_value(case["plan"].clone()).expect("a plan");
            assert_eq!(plan, want, "{name}: the plan of {q:?}");
            let mut args: Vec<serde_json::Value> = Vec::new();
            let query = postgres_query(&plan, "'simple'::regconfig", &mut |value| {
                args.push(serde_json::Value::String(value));
                format!("${}", args.len())
            });
            assert_eq!(
                query,
                case["postgres"]["query"].as_str().unwrap_or_default(),
                "{name}: the Postgres query"
            );
            assert_eq!(
                serde_json::Value::Array(args),
                case["postgres"]["args"],
                "{name}: its values"
            );
            assert_eq!(
                fts_query(&plan),
                case["fts5"].as_str().map(str::to_owned),
                "{name}: the FTS5 match"
            );
        }
    }
}
