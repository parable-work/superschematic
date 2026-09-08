package tsgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// formatTSDefaultLiteral renders the raw @default value as a TypeScript
// expression. Enums are emitted as the string value cast through the enum
// type (e.g. `"OFF" as Phase`) so the literal matches the declared field
// type without requiring an import of the enum members.
//
// Returns ok=false when the raw value cannot be parsed for the declared kind.
func formatTSDefaultLiteral(raw string, kind codegen.DefaultLiteralKind, field FieldInfo) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	switch kind {
	case codegen.DefaultLiteralBool:
		switch strings.ToLower(trimmed) {
		case "true":
			return "true", true
		case "false":
			return "false", true
		}
		return "", false
	case codegen.DefaultLiteralInt:
		if _, err := strconv.ParseInt(trimmed, 10, 64); err != nil {
			return "", false
		}
		return trimmed, true
	case codegen.DefaultLiteralFloat:
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", false
		}
		return trimmed, true
	case codegen.DefaultLiteralString:
		return jsonQuote(raw), true
	case codegen.DefaultLiteralEnum:
		return fmt.Sprintf("%s as %s", jsonQuote(raw), field.Type), true
	case codegen.DefaultLiteralEmptyArray:
		return "[]", true
	}
	return "", false
}

// jsonQuote produces a double-quoted TypeScript string literal that is also
// valid JSON. It escapes control characters, quotes, and backslashes.
func jsonQuote(s string) string {
	var b strings.Builder
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
		default:
			if r < 0x20 {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
