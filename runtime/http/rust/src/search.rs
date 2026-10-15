//! What an implementation needs to answer a `SearchQuery` argument (D62),
//! the twin of the Go runtime's `search` package and the TypeScript
//! runtime's `search.ts`: the words of the search text, each word's `LIKE`
//! pattern, and the case folding an in-memory match compares by. The
//! vectors in `runtime/http/testdata/search_parity.json` hold the three
//! runtimes to the same answers.
//!
//! A search keeps the items whose searchable text contains each of its
//! words, case-insensitively; it filters and does not rank. Over SQL each
//! word is one condition, all of them ANDed: on Postgres
//! `search_text ILIKE $n ESCAPE '\'`, on SQLite
//! `search_text LIKE ?n ESCAPE '\'`, with `like_pattern(word)` as the
//! value. In memory, `matches(text, &terms(q))`.

/// How many words of a search count: the first eight.
pub const MAX_TERMS: usize = 8;

/// The words of a search text: its runs of characters that are not
/// Unicode `White_Space` (`char::is_whitespace`), the first [`MAX_TERMS`]
/// of them, as written. A blank text has none, and a search with none
/// keeps every item.
pub fn terms(text: &str) -> Vec<String> {
    text.split(char::is_whitespace)
        .filter(|word| !word.is_empty())
        .take(MAX_TERMS)
        .map(str::to_owned)
        .collect()
}

/// The `LIKE` pattern that matches a text containing `term`: the term with
/// `\`, `%` and `_` escaped by `\`, between two `%`. The query says
/// `ESCAPE '\'`, so a `%` or `_` a caller typed matches itself.
pub fn like_pattern(term: &str) -> String {
    let mut out = String::with_capacity(term.len() + 2);
    out.push('%');
    for c in term.chars() {
        if matches!(c, '\\' | '%' | '_') {
            out.push('\\');
        }
        out.push(c);
    }
    out.push('%');
    out
}

/// The text an in-memory search compares: each character's simple
/// lowercase mapping, one character for one, so `İ` is `i` and a final
/// `Σ` is `σ`. `char::to_lowercase` gives the full mapping, so each
/// character keeps the first character of its lowercase, and `str`'s own
/// `to_lowercase`, which applies the final sigma rule, is not used.
pub fn fold(text: &str) -> String {
    text.chars()
        .map(|c| c.to_lowercase().next().unwrap_or(c))
        .collect()
}

/// Whether a searchable text contains every term, by [`fold`]: what an
/// implementation that holds its items in memory keeps. No terms match
/// every text.
pub fn matches<S: AsRef<str>>(text: &str, terms: &[S]) -> bool {
    let folded = fold(text);
    terms
        .iter()
        .all(|term| folded.contains(fold(term.as_ref()).as_str()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rules() {
        assert_eq!(terms("  red\u{a0}anvil  "), ["red", "anvil"]);
        assert_eq!(terms("red\u{feff}anvil"), ["red\u{feff}anvil"]);
        assert!(terms(" \t\u{3000} ").is_empty());
        assert_eq!(terms("a b c d e f g h i").len(), MAX_TERMS);
        assert_eq!(like_pattern(r"50%_off\"), r"%50\%\_off\\%");
        assert_eq!(fold("İΣ"), "iσ");
        assert!(matches("Anvil, red", &terms("RED anvil")));
        assert!(!matches("500 off", &terms("50%")));
        assert!(matches::<&str>("anything", &[]));
    }
}
