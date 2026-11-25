package typegen

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// formatGoDefaultLiteral renders the raw @default value as a Go literal for
// the supplied field. Returns ok=false when the value cannot be parsed for
// the declared kind, when the field uses the InputField tri-state wrapper
// (defaults are not emitted for input types), or when the field's GoType is
// a pointer (the current pipeline only emits literals for primitive built-ins
// and enums where the Go type is the value type itself).
func formatGoDefaultLiteral(raw string, kind codegen.DefaultLiteralKind, field FieldInfo) (string, bool) {
	if field.UsesWrapper {
		return "", false
	}
	if strings.HasPrefix(field.GoType, "*") {
		return "", false
	}

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
		if isDurationTextField(field) {
			return durationDefaultLiteral(raw, field)
		}
		return strconv.Quote(raw), true
	case codegen.DefaultLiteralEnum:
		return fmt.Sprintf("%s(%q)", field.GoType, raw), true
	case codegen.DefaultLiteralEmptyArray:
		if strings.HasPrefix(field.GoType, "[]") {
			return field.GoType + "{}", true
		}
		return "", false
	}
	return "", false
}

// durationDefaultLiteral renders the text default of a duration scalar,
// such as Default<Temporal.Duration, '30s'>. The scalar's Go value is a
// time.Duration, so a quoted text does not compile: the text is parsed as
// the scalar's ParseDuration parses a text its rules accept, and written
// as a typed count of nanoseconds, TemporalDuration(30000000000). A text
// Go's parser refuses gets no literal.
func durationDefaultLiteral(raw string, field FieldInfo) (string, bool) {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return fmt.Sprintf("%s(%d)", field.GoType, int64(parsed)), true
}
