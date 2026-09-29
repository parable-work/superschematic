package goutil

import (
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// ToPackageName converts a schema name to a valid Go package name.
func ToPackageName(value string) string {
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	return strings.ToLower(value)
}

// ToPascalCase converts input to PascalCase using shared codegen conventions.
func ToPascalCase(value string) string {
	return codegen.ToPascalCase(strings.ReplaceAll(strings.ReplaceAll(value, "-", "_"), " ", "_"))
}

// SplitLines trims and splits a string by newlines.
func SplitLines(value string) []string {
	return strings.Split(strings.TrimSpace(value), "\n")
}

// GoPublicIdentifier converts an identifier into an exported Go name.
func GoPublicIdentifier(value string) string {
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == '-' || r == '_' || r == '.'
	})
	if len(parts) == 0 {
		return "Value"
	}

	var builder strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}

		builder.WriteString(strings.ToUpper(part[:1]))
		if len(part) > 1 {
			builder.WriteString(part[1:])
		}
	}

	output := builder.String()
	if output == "" {
		return "Value"
	}

	return output
}

// GoPrivateIdentifier converts an identifier into an unexported Go name.
func GoPrivateIdentifier(value string) string {
	publicName := GoPublicIdentifier(value)
	if publicName == "" {
		return "value"
	}

	return strings.ToLower(publicName[:1]) + publicName[1:]
}

// UniqueModules returns the module paths in lists, sorted, without
// duplicates, empty entries or any of exclude: the generated modules a
// go.mod requires or replaces.
func UniqueModules(exclude []string, lists ...[]string) []string {
	seen := map[string]bool{"": true}
	for _, modulePath := range exclude {
		seen[modulePath] = true
	}
	var modules []string
	for _, list := range lists {
		for _, modulePath := range list {
			if seen[modulePath] {
				continue
			}
			seen[modulePath] = true
			modules = append(modules, modulePath)
		}
	}
	sort.Strings(modules)
	return modules
}

// TemplateFuncs returns shared Go template functions merged with optional extras.
func TemplateFuncs(extras template.FuncMap) template.FuncMap {
	funcs := template.FuncMap{
		"toPascalCase":  ToPascalCase,
		"toPackageName": ToPackageName,
		"splitLines":    SplitLines,
	}

	for name, fn := range extras {
		funcs[name] = fn
	}

	return funcs
}
