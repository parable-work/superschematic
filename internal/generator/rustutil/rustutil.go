// Package rustutil provides Rust naming and type helpers shared by the
// Rust-targeting generators (rustgen, and later rustrestgen / rustsdkgen).
package rustutil

import (
	"sort"
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

// EscapeKeyword makes a snake_case identifier usable where Rust would read it
// as a keyword: a raw identifier (r#type) for most keywords, and a trailing
// underscore for crate, self, Self and super, which cannot be raw
// identifiers. Any other identifier is returned as it is.
func EscapeKeyword(identifier string) string {
	if !IsRustKeyword(identifier) {
		return identifier
	}
	switch identifier {
	case "crate", "self", "Self", "super":
		return identifier + "_"
	}
	return "r#" + identifier
}

// Identifier is name as a snake_case Rust identifier: letters and digits
// lowercased, anything else an underscore, runs of underscores collapsed, a
// leading digit prefixed with an underscore and a keyword escaped
// (EscapeKeyword). A name with nothing left is fallback. The Rust SDK names
// its methods and fields with it, and the Rust server its argument fields,
// so both spell an argument alike.
func Identifier(name, fallback string) string {
	base := codegen.ToSnakeCase(strings.TrimSpace(name))
	if base == "" {
		return fallback
	}

	var b strings.Builder
	for _, r := range base {
		switch {
		case r == '_':
			b.WriteRune(r)
		case unicode.IsLetter(r), unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune('_')
		}
	}

	identifier := CollapseUnderscores(strings.Trim(b.String(), "_"))
	if identifier == "" {
		identifier = fallback
	}
	if identifier[0] >= '0' && identifier[0] <= '9' {
		identifier = "_" + identifier
	}
	return EscapeKeyword(identifier)
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

// RawString writes s as a Rust raw string literal with enough #s that no
// "#... sequence in s ends it.
func RawString(s string) string {
	hashes := "#"
	for strings.Contains(s, `"`+hashes) {
		hashes += "#"
	}
	return "r" + hashes + `"` + s + `"` + hashes
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

// UseLine renders a use declaration of items under path, as rustfmt lays
// one out: `use path::item;` for one, `use path::{a, b};` for more, the
// items sorted with self first, then lower case, then upper case, and,
// when the line passes 98 columns, the items on lines of their own
// indented four spaces, as many to a line as fit.
func UseLine(path string, items []string) string {
	items = append([]string(nil), items...)
	rank := func(item string) int {
		switch {
		case item == "self":
			return 0
		case item != "" && strings.ToLower(item[:1]) == item[:1]:
			return 1
		}
		return 2
	}
	sort.SliceStable(items, func(i, j int) bool {
		if ri, rj := rank(items[i]), rank(items[j]); ri != rj {
			return ri < rj
		}
		return items[i] < items[j]
	})
	if len(items) == 1 && items[0] != "self" {
		return "use " + path + "::" + items[0] + ";"
	}
	line := "use " + path + "::{" + strings.Join(items, ", ") + "};"
	// rustfmt keeps a braced list on the use's line up to 98 columns: a
	// line of 99 or 100 it breaks too.
	if len(line) <= 98 {
		return line
	}
	var b strings.Builder
	b.WriteString("use " + path + "::{\n")
	row := ""
	for _, item := range items {
		next := item + ","
		if row != "" && 4+len(row)+1+len(next) > 100 {
			b.WriteString("    " + row + "\n")
			row = ""
		}
		if row != "" {
			row += " "
		}
		row += next
	}
	b.WriteString("    " + row + "\n};")
	return b.String()
}
