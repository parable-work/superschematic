package sqldialect

import "testing"

// TestNumberPlaceholders: placeholders are numbered in order, and a ?
// inside a quoted name, one with a doubled quote, or a literal is left
// alone.
func TestNumberPlaceholders(t *testing.T) {
	for query, want := range map[string]string{
		`SELECT "a?b", 'c?d' FROM "t""?" WHERE x = ? AND y = ?`: `SELECT "a?b", 'c?d' FROM "t""?" WHERE x = $1 AND y = $2`,
		`UPDATE "t" SET "v" = 'it''s ?' WHERE "k" = ?`:          `UPDATE "t" SET "v" = 'it''s ?' WHERE "k" = $1`,
		`SELECT 1`: `SELECT 1`,
		`?, ?, ?`:  `$1, $2, $3`,
	} {
		if got := NumberPlaceholders(query); got != want {
			t.Errorf("NumberPlaceholders(%s) = %s, want %s", query, got, want)
		}
	}
}

// TestQuoteIdent: a name is quoted with each double quote doubled, and a
// quoted name keeps a ? from NumberPlaceholders.
func TestQuoteIdent(t *testing.T) {
	for name, want := range map[string]string{"user": `"user"`, `my "user"`: `"my ""user"""`, "a?b c": `"a?b c"`} {
		if got := QuoteIdent(name); got != want {
			t.Errorf("QuoteIdent(%q) = %s, want %s", name, got, want)
		}
	}
	query := "SELECT " + QuoteIdent(`col"?`) + " FROM " + QuoteIdent("t?") + " WHERE x = ?"
	if got, want := NumberPlaceholders(query), `SELECT "col""?" FROM "t?" WHERE x = $1`; got != want {
		t.Errorf("NumberPlaceholders = %s, want %s", got, want)
	}
}
