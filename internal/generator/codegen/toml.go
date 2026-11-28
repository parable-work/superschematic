package codegen

import (
	"fmt"
	"strings"
)

// QuoteTOML writes s as a TOML basic string: a backslash, a double quote,
// a newline, a carriage return, a tab, a backspace and a form feed are
// escaped as \\, \", \n, \r, \t, \b and \f, any other control character
// and DEL as \uXXXX, which a basic string cannot hold as they are.
func QuoteTOML(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
