// Package rustutil provides Rust naming and type helpers shared by the
// Rust-targeting generators (rustgen, and later rustrestgen / rustsdkgen).
package rustutil

import (
	"strings"
	"unicode"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// IsRustKeyword reports whether value is a Rust keyword (strict or reserved).
func IsRustKeyword(value string) bool {
	switch value {
	case "as", "break", "const", "continue", "crate", "else", "enum", "extern", "false",
		"fn", "for", "if", "impl", "in", "let", "loop", "match", "mod", "move", "mut", "pub",
		"ref", "return", "self", "Self", "static", "struct", "super", "trait", "true", "type",
		"unsafe", "use", "where", "while", "async", "await", "dyn", "abstract", "become", "box",
		"do", "final", "macro", "override", "priv", "try", "typeof", "unsized", "virtual", "yield":
		return true
	default:
		return false
	}
}

// CollapseUnderscores normalizes consecutive underscores into a single underscore.
func CollapseUnderscores(value string) string {
	for strings.Contains(value, "__") {
		value = strings.ReplaceAll(value, "__", "_")
	}
	return value
}

// WrapOptionalType wraps typeName in Option<> unless it is already wrapped.
func WrapOptionalType(typeName string) string {
	if strings.HasPrefix(typeName, "Option<") {
		return typeName
	}
	return "Option<" + typeName + ">"
}

// CrateNameToModulePath converts a crate name to a safe Rust module identifier.
func CrateNameToModulePath(crateName string) string {
	crateName = strings.TrimSpace(crateName)
	if crateName == "" {
		return "dep"
	}

	var b strings.Builder
	for _, r := range crateName {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune('_')
		}
	}

	moduleName := strings.Trim(b.String(), "_")
	moduleName = CollapseUnderscores(moduleName)
	moduleName = strings.Trim(moduleName, "_")
	if moduleName == "" {
		moduleName = "dep"
	}
	if moduleName[0] >= '0' && moduleName[0] <= '9' {
		moduleName = "dep_" + moduleName
	}
	if IsRustKeyword(moduleName) {
		moduleName = moduleName + "_"
	}
	return moduleName
}

// ToPascalCase converts string input to PascalCase using codegen conventions.
func ToPascalCase(value string) string {
	return codegen.ToPascalCase(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
}

// ToSnakeCase converts string input to snake_case using codegen conventions.
func ToSnakeCase(value string) string {
	return codegen.ToSnakeCase(value)
}

// ToPackageName converts a package name to lowercase snake_case.
func ToPackageName(value string) string {
	value = strings.ReplaceAll(value, "-", "_")
	return strings.ToLower(value)
}

// SplitLines trims and splits a string by newlines.
func SplitLines(value string) []string {
	return strings.Split(strings.TrimSpace(value), "\n")
}

// PrimitiveToRustType maps IR language primitive names to Rust primitive
// types. Integer-like refinement happens upstream via scalar traits; the
// bare "number" primitive maps to f64.
func PrimitiveToRustType(value string) (string, bool) {
	switch value {
	case codegen.PrimitiveString:
		return "String", true
	case codegen.PrimitiveNumber:
		return "f64", true
	case codegen.PrimitiveBoolean:
		return "bool", true
	default:
		return "", false
	}
}
