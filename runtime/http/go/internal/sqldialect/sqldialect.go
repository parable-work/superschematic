// Package sqldialect holds the SQL text the runtime's stores write alike
// for Postgres and SQLite.
package sqldialect

import (
	"strconv"
	"strings"
)

// QuoteIdent quotes an identifier as Postgres and SQLite both read it: a
// double quote, the name with each double quote doubled, and a double
// quote.
func QuoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// NumberPlaceholders writes each ? placeholder of query as Postgres
// numbers them, $1, $2 and on, and leaves a ? inside a quoted identifier
// or a string literal alone. A doubled quote inside one closes it and
// opens it again, so it needs no case of its own.
func NumberPlaceholders(query string) string {
	var b strings.Builder
	n := 0
	var quote byte
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '?':
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
