package sqlmigrate

import (
	"regexp"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/sqlutil"
)

// A projection view keeps working when a table or column it reads is
// renamed: Postgres stores the view by reference and prints it with the new
// names. renameInView writes a view's CREATE VIEW statement as the new
// version's compiler would after the renames, so a view whose only change
// follows from a rename is left alone instead of dropped and created again.

var (
	viewFromPattern = regexp.MustCompile(`(?m)^FROM (\S+) AS (\S+)$`)
	viewJoinPattern = regexp.MustCompile(`(?m)^  (?:INNER|LEFT) JOIN (\S+) AS (\S+) ON `)
	// viewBareItem is a select list item that keeps its source column's
	// name, so the view selects it without an alias.
	viewBareItem = regexp.MustCompile(`^  ([^\s.]+)\.([^\s.,]+)(,?)$`)
)

// renameInView applies the renames to a CREATE VIEW statement sqlgen wrote.
func renameInView(statement string, r *renames) string {
	aliases := map[string]string{} // alias -> previous table
	for _, pattern := range []*regexp.Regexp{viewFromPattern, viewJoinPattern} {
		for _, m := range pattern.FindAllStringSubmatch(statement, -1) {
			aliases[unquoteIdent(m[2])] = unquoteIdent(m[1])
		}
	}

	// A column selected bare keeps its name in the view when its source
	// column is renamed, so the item gains an alias.
	lines := strings.Split(statement, "\n")
	inSelect := false
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "SELECT"):
			inSelect = true
			continue
		case strings.HasPrefix(line, "FROM "):
			inSelect = false
		}
		if !inSelect {
			continue
		}
		m := viewBareItem.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		table, ok := aliases[unquoteIdent(m[1])]
		column := unquoteIdent(m[2])
		if ok && r.column(table, column) != column {
			lines[i] = "  " + m[1] + "." + m[2] + " AS " + sqlutil.QuoteIdentifier(column) + m[3]
		}
	}

	tokens := tokenizeSQL(strings.Join(lines, "\n"))
	prev := -1 // the last token that is not space or a comment
	for i, tok := range tokens {
		if tok.kind != tokIdent {
			if tok.kind == tokPunct || tok.kind == tokString {
				prev = i
			}
			continue
		}
		if prev >= 0 && tokens[prev].kind == tokIdent && (tokens[prev].text == "FROM" || tokens[prev].text == "JOIN") {
			tokens[i].text = sqlutil.QuoteIdentifier(r.table(tok.value))
		}
		if table, ok := aliases[tok.value]; ok && i+2 < len(tokens) &&
			tokens[i+1].kind == tokPunct && tokens[i+1].text == "." && tokens[i+2].kind == tokIdent {
			tokens[i+2].text = sqlutil.QuoteIdentifier(r.column(table, tokens[i+2].value))
		}
		prev = i
	}
	var b strings.Builder
	for _, tok := range tokens {
		b.WriteString(tok.text)
	}
	return b.String()
}

type tokenKind int

const (
	tokSpace tokenKind = iota
	tokComment
	tokString
	tokIdent
	tokPunct
)

type token struct {
	kind tokenKind
	text string
	// value is an identifier unquoted.
	value string
}

// tokenizeSQL splits SQL into identifiers, string literals, comments,
// space and single punctuation characters. Joining the texts gives the
// input back.
func tokenizeSQL(sql string) []token {
	var out []token
	isIdent := func(ch byte) bool {
		return ch == '_' || ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch >= 0x80
	}
	for i := 0; i < len(sql); {
		ch := sql[i]
		start := i
		switch {
		case ch == ' ' || ch == '\n' || ch == '\t' || ch == '\r':
			for i < len(sql) && strings.IndexByte(" \n\t\r", sql[i]) >= 0 {
				i++
			}
			out = append(out, token{kind: tokSpace, text: sql[start:i]})
		case ch == '-' && i+1 < len(sql) && sql[i+1] == '-':
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			out = append(out, token{kind: tokComment, text: sql[start:i]})
		case ch == '\'':
			i++
			for i < len(sql) {
				if sql[i] == '\'' {
					if i+1 < len(sql) && sql[i+1] == '\'' {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
			out = append(out, token{kind: tokString, text: sql[start:i]})
		case ch == '"':
			i++
			for i < len(sql) && sql[i] != '"' {
				i++
			}
			i++
			if i > len(sql) {
				i = len(sql)
			}
			out = append(out, token{kind: tokIdent, text: sql[start:i], value: unquoteIdent(sql[start:i])})
		case isIdent(ch) && (ch < '0' || ch > '9'):
			for i < len(sql) && isIdent(sql[i]) {
				i++
			}
			out = append(out, token{kind: tokIdent, text: sql[start:i], value: sql[start:i]})
		default:
			i++
			out = append(out, token{kind: tokPunct, text: sql[start:i]})
		}
	}
	return out
}

// unquoteIdent strips the double quotes of a quoted identifier.
func unquoteIdent(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}
