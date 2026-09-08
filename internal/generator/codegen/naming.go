// Package codegen provides the shared types and utilities the psgen
// generators build on: casing helpers, template rendering, and the
// language-agnostic extraction layer over the Schema IR.
package codegen

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var caseBoundaryRegex = regexp.MustCompile("([a-z0-9])([A-Z])")

// irregularPlurals maps singular forms to their irregular plurals.
var irregularPlurals = map[string]string{
	"person":   "people",
	"child":    "children",
	"man":      "men",
	"woman":    "women",
	"mouse":    "mice",
	"goose":    "geese",
	"tooth":    "teeth",
	"foot":     "feet",
	"datum":    "data",
	"analysis": "analyses",
	"crisis":   "crises",
	"thesis":   "theses",
}

// ToSnakeCase converts PascalCase, camelCase, or kebab-case strings to snake_case.
// Examples:
//   - "UserName" -> "user_name"
//   - "getHTTPResponse" -> "get_http_response"
//   - "connector-requests" -> "connector_requests"
func ToSnakeCase(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	snake := caseBoundaryRegex.ReplaceAllString(s, "${1}_${2}")
	return strings.ToLower(snake)
}

// ToKebabCase converts PascalCase or camelCase strings to kebab-case.
// Examples:
//   - "UserName" -> "user-name"
//   - "getHTTPResponse" -> "get-http-response"
func ToKebabCase(s string) string {
	kebab := caseBoundaryRegex.ReplaceAllString(s, "${1}-${2}")
	return strings.ToLower(kebab)
}

// ToPlural converts a singular kebab-case word to its plural form.
// It applies simple English pluralization rules. Only the last hyphenated
// segment is pluralized (e.g., "sync-job" -> "sync-jobs").
func ToPlural(s string) string {
	if s == "" {
		return s
	}

	parts := strings.Split(s, "-")
	last := parts[len(parts)-1]

	if plural, ok := irregularPlurals[last]; ok {
		parts[len(parts)-1] = plural
		return strings.Join(parts, "-")
	}

	switch {
	case strings.HasSuffix(last, "ss") || strings.HasSuffix(last, "sh") ||
		strings.HasSuffix(last, "ch") || strings.HasSuffix(last, "x"):
		last += "es"
	case strings.HasSuffix(last, "z"):
		last += "zes"
	case strings.HasSuffix(last, "us"):
		last += "es"
	case strings.HasSuffix(last, "is"):
		last += "es"
	case strings.HasSuffix(last, "s"):
		return s
	case strings.HasSuffix(last, "y") && len(last) > 1 && !isVowel(last[len(last)-2]):
		last = last[:len(last)-1] + "ies"
	default:
		last += "s"
	}

	parts[len(parts)-1] = last
	return strings.Join(parts, "-")
}

// ToCamelCase converts PascalCase, kebab-case, or snake_case to camelCase.
func ToCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}
	pascal := ToPascalCase(s)
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

// ToPascalCase converts camelCase, kebab-case, or snake_case to PascalCase.
func ToPascalCase(s string) string {
	if len(s) == 0 {
		return s
	}
	if strings.ContainsAny(s, "-_") {
		var parts []string
		for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == '-' || r == '_' }) {
			if len(part) > 0 {
				part = strings.ToLower(part)
				parts = append(parts, strings.ToUpper(part[:1])+part[1:])
			}
		}
		return strings.Join(parts, "")
	}
	if s == strings.ToUpper(s) {
		s = strings.ToLower(s)
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// TitleCase converts strings to title case using English language rules.
func TitleCase(s string) string {
	return cases.Title(language.English).String(s)
}

// SanitizeImportAlias normalizes a dependency name into a valid import alias.
// Non-alphanumeric characters are removed, letters are lowercased, and aliases
// starting with a digit are prefixed with "dep". If isKeyword is provided and
// returns true for the alias, "_dep" is appended.
func SanitizeImportAlias(value string, isKeyword func(string) bool) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		}
	}

	alias := b.String()
	if alias == "" {
		return "dep"
	}
	if alias[0] >= '0' && alias[0] <= '9' {
		alias = "dep" + alias
	}
	if isKeyword != nil && isKeyword(alias) {
		return alias + "_dep"
	}
	return alias
}

// UniqueImportAlias reserves and returns a unique alias using numeric suffixes.
func UniqueImportAlias(base string, used map[string]struct{}) string {
	alias := base
	if alias == "" {
		alias = "dep"
	}

	if _, exists := used[alias]; !exists {
		used[alias] = struct{}{}
		return alias
	}

	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", alias, i)
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
}

func isVowel(b byte) bool {
	return b == 'a' || b == 'e' || b == 'i' || b == 'o' || b == 'u'
}
