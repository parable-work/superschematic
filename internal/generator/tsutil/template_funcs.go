package tsutil

import (
	"strings"
	"text/template"
	"unicode"
)

// titleCase converts the first letter of a string to uppercase.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// BaseTemplateFuncs returns the base set of template functions for TypeScript generation
// Generators can add their own custom functions by merging with this map
func BaseTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		// String manipulation
		"lower":     strings.ToLower,
		"upper":     strings.ToUpper,
		"title":     titleCase,
		"trimSpace": strings.TrimSpace,
		"join":      func(strs []string, sep string) string { return strings.Join(strs, sep) },
		"hasPrefix": strings.HasPrefix,
		"hasSuffix": strings.HasSuffix,
		"replace":   strings.ReplaceAll,

		// Case conversion
		"toCamelCase": ToCamelCase,
		"toClassName": ToClassName,
		"toSnakeCase": ToSnakeCase,

		// TypeScript-specific
		"escapeString": EscapeString,
	}
}
