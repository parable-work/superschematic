// Package search is what an implementation needs to answer a SearchQuery
// argument (D62): the words of the search text, each word's LIKE pattern,
// and the case folding an in-memory match compares by. The TypeScript and
// Rust HTTP runtimes give the same three steps (searchTerms, likePattern,
// foldText and searchMatches; search::terms, search::like_pattern,
// search::fold and search::matches), and
// runtime/http/testdata/search_parity.json, which this package's test
// writes, holds all three to the same answers.
//
// A search keeps the items whose searchable text contains each of its
// words, case-insensitively; it filters and does not rank. Over SQL each
// word is one condition, all of them ANDed: on Postgres
// `search_text ILIKE $n ESCAPE '\'`, on SQLite
// `search_text LIKE ?n ESCAPE '\'`, with LikePattern(word) as the value.
// In memory, Matches(text, Terms(q)).
package search

//go:generate sh -c "cd ../../rust && cargo run -q --example fold_mappings | (cd ../go/search && go run gen_fold.go)"

import (
	"strings"
	"unicode"
)

// MaxTerms is how many words of a search count: the first eight, so a
// pasted paragraph cannot make a query of hundreds of conditions.
const MaxTerms = 8

// Terms returns the words of a search text: its runs of characters that
// are not Unicode White_Space, the first MaxTerms of them, as written. A
// blank text has none, and a search with none keeps every item. U+FEFF and
// the zero-width characters are not White_Space, so they stay inside a
// word.
func Terms(text string) []string {
	words := strings.FieldsFunc(text, unicode.IsSpace)
	if len(words) > MaxTerms {
		words = words[:MaxTerms]
	}
	if len(words) == 0 {
		return []string{}
	}
	return words
}

// LikePattern is the LIKE pattern that matches a text containing term: the
// term with `\`, `%` and `_` escaped by `\`, between two `%`. The query
// says `ESCAPE '\'`, so a `%` or `_` a caller typed matches itself.
func LikePattern(term string) string {
	var b strings.Builder
	b.Grow(len(term) + 2)
	b.WriteByte('%')
	for _, r := range term {
		if r == '\\' || r == '%' || r == '_' {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	b.WriteByte('%')
	return b.String()
}

// Fold is the text an in-memory search compares: each character's simple
// lowercase mapping (Unicode's UnicodeData), one character for one, so
// "İ" is "i", a final "Σ" is "σ", and a character without a lowercase is
// itself. The mappings are the TypeScript and Rust runtimes': Go's
// unicode tables are of an older Unicode, and foldAdded holds what the
// later versions added.
func Fold(text string) string {
	return strings.Map(lower, text)
}

// lower is a character's simple lowercase mapping, foldAdded's when it
// has one.
func lower(r rune) rune {
	if folded, ok := foldAdded[r]; ok {
		return folded
	}
	return unicode.ToLower(r)
}

// Matches reports whether a searchable text contains every term, by Fold:
// what an implementation that holds its items in memory keeps. No terms
// match every text.
func Matches(text string, terms []string) bool {
	folded := Fold(text)
	for _, term := range terms {
		if !strings.Contains(folded, Fold(term)) {
			return false
		}
	}
	return true
}
