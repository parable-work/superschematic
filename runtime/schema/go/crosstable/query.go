package crosstable

import (
	"strings"
	"unicode"
)

/*
The query syntax of search across tables (#740, D155): what SearchTables
reads a query's text as, the TypeScript runtime's search-query.ts and the
Rust runtime's orm::search_tables::query term for term, and what it asks
Postgres and SQLite's FTS5 for.

A query is read once into a plan:

- A term is a quoted phrase ("walnut desk") or a run of characters that is
  neither white space nor a quote (walnut). A bare term that ends in *
  is a prefix (wal*). A term after - is excluded (-oak, -"oak chair"); a
  + before one says it is required, as every term is.
- OR, upper case, between two terms puts them in one group: a row holds
  one of a group's terms. A row matches when it holds every group and no
  excluded term. OR binds the terms beside it before the groups are
  joined: walnut oak OR pine is walnut, and oak or pine.
- A term with no letter or digit, an OR with no term before it, and an OR
  beside an excluded term are dropped. The first MaxQueryTerms terms
  count, each its first 200 characters.

A term's words are the database's: on Postgres a term is phraseto_tsquery
of its text and a prefix to_tsquery of its text quoted with :*, under the
index's text search configuration; on SQLite each is a quoted FTS5
phrase, a prefix with * after it. Every text is a bound value.
*/

// SearchTerm is one term of a query: its text, a phrase of the words the
// database finds in it, and whether its last word is a prefix.
type SearchTerm struct {
	Text   string `json:"text"`
	Prefix bool   `json:"prefix"`
}

// SearchPlan is a query read: groups a row must each hold one term of,
// and terms it must hold none of.
type SearchPlan struct {
	All  [][]SearchTerm `json:"all"`
	None []SearchTerm   `json:"none"`
}

// MaxQueryTerms is the most terms of a query that count.
const MaxQueryTerms = 16

const maxTerm = 200

type queryToken struct {
	or      bool
	text    string
	prefix  bool
	exclude bool
}

func queryTokens(q string) []queryToken {
	chars := []rune(dropMarks(q))
	var out []queryToken
	i := 0
	for i < len(chars) {
		if unicode.IsSpace(chars[i]) {
			i++
			continue
		}
		sign := rune(0)
		if (chars[i] == '-' || chars[i] == '+') && i+1 < len(chars) && !unicode.IsSpace(chars[i+1]) {
			sign = chars[i]
			i++
		}
		var text strings.Builder
		if chars[i] == '"' {
			i++
			for i < len(chars) && chars[i] != '"' {
				text.WriteRune(chars[i])
				i++
			}
			i++
			out = append(out, queryToken{text: text.String(), exclude: sign == '-'})
			continue
		}
		for i < len(chars) && !unicode.IsSpace(chars[i]) && chars[i] != '"' {
			text.WriteRune(chars[i])
			i++
		}
		word := text.String()
		if sign == 0 && word == "OR" {
			out = append(out, queryToken{or: true, text: word})
			continue
		}
		bare := strings.TrimRight(word, "*")
		out = append(out, queryToken{text: bare, prefix: bare != word, exclude: sign == '-'})
	}
	return out
}

func hasLetterOrDigit(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return true
		}
	}
	return false
}

// ReadSearchQuery reads a query's text into its plan (#740).
func ReadSearchQuery(q string) SearchPlan {
	plan := SearchPlan{All: [][]SearchTerm{}, None: []SearchTerm{}}
	count := 0
	afterTerm, join := false, false
	for _, token := range queryTokens(q) {
		if token.or {
			if afterTerm {
				join = true
			}
			continue
		}
		if count == MaxQueryTerms {
			break
		}
		if !hasLetterOrDigit(token.text) {
			continue
		}
		count++
		text := token.text
		if runes := []rune(text); len(runes) > maxTerm {
			text = string(runes[:maxTerm])
		}
		term := SearchTerm{Text: text, Prefix: token.prefix}
		if token.exclude {
			plan.None = append(plan.None, term)
			afterTerm = false
		} else {
			if join && len(plan.All) > 0 {
				plan.All[len(plan.All)-1] = append(plan.All[len(plan.All)-1], term)
			} else {
				plan.All = append(plan.All, []SearchTerm{term})
			}
			afterTerm = true
		}
		join = false
	}
	return plan
}

// prefixOperand is a term as to_tsquery takes a prefix: its text quoted,
// a quote doubled and a backslash escaped, then :*.
func prefixOperand(text string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(text, `\`, `\\`), "'", "''") + "':*"
}

// PostgresQuery is the plan as a tsquery expression of Postgres's under
// config, a quoted regconfig: each term phraseto_tsquery or to_tsquery of
// a value param binds, a group || of its terms, the groups && and each
// excluded term && !!. The plan's None alone when it has no group, which
// a search by vector alone excludes by.
func PostgresQuery(plan SearchPlan, config string, param func(any) string) string {
	term := func(t SearchTerm) string {
		if t.Prefix {
			return "to_tsquery(" + config + ", " + param(prefixOperand(t.Text)) + "::text)"
		}
		return "phraseto_tsquery(" + config + ", " + param(t.Text) + "::text)"
	}
	group := func(g []SearchTerm) string {
		if len(g) == 1 {
			return term(g[0])
		}
		parts := make([]string, len(g))
		for i, t := range g {
			parts[i] = term(t)
		}
		return "(" + strings.Join(parts, " || ") + ")"
	}
	var parts []string
	if len(plan.All) == 0 {
		for _, t := range plan.None {
			parts = append(parts, term(t))
		}
		return strings.Join(parts, " || ")
	}
	for _, g := range plan.All {
		parts = append(parts, group(g))
	}
	for _, t := range plan.None {
		parts = append(parts, "!!"+term(t))
	}
	return strings.Join(parts, " && ")
}

// ftsPhrase is a term as an FTS5 phrase: its text quoted, a quote doubled,
// and * after a prefix.
func ftsPhrase(t SearchTerm) string {
	out := `"` + strings.ReplaceAll(t.Text, `"`, `""`) + `"`
	if t.Prefix {
		out += "*"
	}
	return out
}

// FTSQuery is the plan as an FTS5 expression: each term a quoted phrase,
// a group OR of its terms, the groups AND and each excluded term NOT; ""
// for a plan with no group.
func FTSQuery(plan SearchPlan) string {
	if len(plan.All) == 0 {
		return ""
	}
	groups := make([]string, len(plan.All))
	for i, g := range plan.All {
		if len(g) == 1 {
			groups[i] = ftsPhrase(g[0])
			continue
		}
		parts := make([]string, len(g))
		for j, t := range g {
			parts[j] = ftsPhrase(t)
		}
		groups[i] = "(" + strings.Join(parts, " OR ") + ")"
	}
	expression := strings.Join(groups, " AND ")
	if len(plan.None) > 0 {
		expression = "(" + expression + ")"
		for _, t := range plan.None {
			expression += " NOT " + ftsPhrase(t)
		}
	}
	return expression
}
