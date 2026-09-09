package rustgen

import (
	"strings"
	"text/template"
	"unicode"

	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// customTemplateFuncs returns the rustgen-specific template helpers merged on
// top of the shared codegen base funcs by GenerateFile.
func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"rustEnumVariant": toRustEnumVariant,
		"rustDoc":         rustDoc,
		"formatFeatures":  formatFeatures,
		"isStructDef":     isStructDef,
		"formatStructDef": formatStructDef,
	}
}

// rustDoc renders documentation text as `///` doc comment lines at the given
// indent (number of 4-space levels). Returns "" for empty docs so templates
// can guard with {{with}}.
func rustDoc(doc string, indent int) string {
	doc = strings.TrimSpace(doc)
	if doc == "" {
		return ""
	}
	pad := strings.Repeat("    ", indent)
	lines := strings.Split(doc, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString("\n")
		}
		line = strings.TrimRight(line, " \t")
		if line == "" {
			b.WriteString(pad + "///")
			continue
		}
		b.WriteString(pad + "/// " + line)
	}
	return b.String()
}

// formatFeatures renders a Cargo features list (e.g. `"serde", "v4"`).
func formatFeatures(features []string) string {
	if len(features) == 0 {
		return ""
	}
	quoted := make([]string, len(features))
	for i, f := range features {
		quoted[i] = `"` + f + `"`
	}
	return strings.Join(quoted, ", ")
}

// isStructDef reports whether a scalar's Rust type mapping is an inline
// struct definition rather than a type expression.
func isStructDef(s string) bool {
	return strings.HasPrefix(s, "struct ")
}

// formatStructDef expands a single-line `struct Name { a: T, b: U }` mapping
// into a multi-line definition with pub fields.
func formatStructDef(s string) string {
	braceIdx := strings.Index(s, "{")
	if braceIdx == -1 {
		return s
	}
	header := strings.TrimSpace(s[:braceIdx])
	body := strings.TrimSuffix(strings.TrimSpace(s[braceIdx+1:]), "}")
	fields := strings.Split(body, ",")
	var lines []string
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		lines = append(lines, "    pub "+f+",")
	}
	return header + " {\n" + strings.Join(lines, "\n") + "\n}"
}

// toRustEnumVariant converts an enum value name to a Rust variant name
// (PascalCase over `_`, `-`, `.`, and whitespace boundaries; digits at the
// start are prefixed with N). Rust keywords get a trailing underscore.
func toRustEnumVariant(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r)
	})

	if len(parts) == 0 {
		return "Unknown"
	}

	var b strings.Builder
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		wroteFirst := false
		for _, r := range part {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				continue
			}
			if !wroteFirst {
				if unicode.IsDigit(r) {
					b.WriteRune('N')
				}
				b.WriteRune(unicode.ToUpper(r))
				wroteFirst = true
				continue
			}
			b.WriteRune(unicode.ToLower(r))
		}
	}

	out := b.String()
	if out == "" {
		return "Unknown"
	}
	if out[0] >= '0' && out[0] <= '9' {
		return "N" + out
	}
	if rustutil.IsRustKeyword(out) {
		return out + "_"
	}
	return out
}
