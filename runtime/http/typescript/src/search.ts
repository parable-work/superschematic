/*
What an implementation needs to answer a SearchQuery argument (D62), the
twin of the Go runtime's search package and the Rust runtime's search
module: the words of the search text, each word's LIKE pattern, and the
case folding an in-memory match compares by. The vectors in
runtime/http/testdata/search_parity.json hold the three runtimes to the
same answers.

A search keeps the items whose searchable text contains each of its words,
case-insensitively; it filters and does not rank. Over SQL each word is one
condition, all of them ANDed: on Postgres `search_text ILIKE $n ESCAPE '\'`,
on SQLite `search_text LIKE ?n ESCAPE '\'`, with likePattern(word) as the
value. In memory, searchMatches(text, searchTerms(q)).
*/

/** How many words of a search count: the first eight. */
export const MAX_SEARCH_TERMS = 8;

/*
Unicode White_Space. `\s` is not it: it takes U+FEFF and leaves U+0085.
*/
const WHITE_SPACE = /\p{White_Space}+/u;

/**
 * The words of a search text: its runs of characters that are not Unicode
 * White_Space, the first MAX_SEARCH_TERMS of them, as written. A blank text
 * has none, and a search with none keeps every item.
 */
export function searchTerms(text: string): string[] {
  return text
    .split(WHITE_SPACE)
    .filter(word => word !== '')
    .slice(0, MAX_SEARCH_TERMS);
}

/**
 * The LIKE pattern that matches a text containing term: the term with `\`,
 * `%` and `_` escaped by `\`, between two `%`. The query says
 * `ESCAPE '\'`, so a `%` or `_` a caller typed matches itself.
 */
export function likePattern(term: string): string {
  return `%${term.replace(/[\\%_]/g, '\\$&')}%`;
}

/**
 * The text an in-memory search compares: each character's simple lowercase
 * mapping, one character for one, so "İ" is "i" and a final "Σ" is "σ".
 * String.prototype.toLowerCase applies the full mappings and the final
 * sigma rule, so each character is folded alone and keeps the first
 * character of its lowercase.
 */
export function foldText(text: string): string {
  let out = '';
  for (const char of text) {
    const lower = char.toLowerCase();
    out += lower === char ? char : String.fromCodePoint(lower.codePointAt(0)!);
  }
  return out;
}

/**
 * Whether a searchable text contains every term, by foldText: what an
 * implementation that holds its items in memory keeps. No terms match
 * every text.
 */
export function searchMatches(text: string, terms: readonly string[]): boolean {
  const folded = foldText(text);
  return terms.every(term => folded.includes(foldText(term)));
}
