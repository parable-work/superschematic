package pygen

import (
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// pythonFieldDefaultExpr renders the raw @default value as a Python literal
// expression suitable for use as `Field(default=<expr>, ...)`. The second
// return value reports whether the field has a usable default.
//
// Booleans are translated to Python's True/False capitalization. Strings are
// double-quoted with escaping. Enums are emitted as the raw string value (the
// generated pydantic enum classes accept the string form via
// use_enum_values). An empty array becomes the [] literal, which is safe as a
// pydantic Field default: pydantic deep-copies mutable defaults per instance.
func pythonFieldDefaultExpr(field codegen.FieldInfo, enumLookup codegen.EnumLookup) (string, bool) {
	kind := codegen.ClassifyDefault(field, enumLookup)
	if kind == codegen.DefaultLiteralUnknown {
		return "", false
	}

	raw := *field.Default
	trimmed := strings.TrimSpace(raw)
	switch kind {
	case codegen.DefaultLiteralBool:
		switch strings.ToLower(trimmed) {
		case "true":
			return "True", true
		case "false":
			return "False", true
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
	case codegen.DefaultLiteralString, codegen.DefaultLiteralEnum:
		return pythonQuote(raw), true
	case codegen.DefaultLiteralEmptyArray:
		return "[]", true
	}
	return "", false
}

// pythonQuote produces a double-quoted Python string literal with control
// characters, quotes, and backslashes escaped.
func pythonQuote(s string) string {
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
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
