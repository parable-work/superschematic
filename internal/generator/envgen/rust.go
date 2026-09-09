package envgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// WriteRustConfig writes the generated Rust configuration loader to src/config.rs.
func WriteRustConfig(output *ConfigOutput, outputDir string) error {
	srcDir := filepath.Join(outputDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", srcDir, err)
	}

	target := filepath.Join(srcDir, "config.rs")
	if err := generateRustFile("config.rust.tmpl", target, output); err != nil {
		return fmt.Errorf("failed to generate Rust config loader: %w", err)
	}

	if err := WriteValuesSchema(output, outputDir); err != nil {
		return fmt.Errorf("failed to generate values schema: %w", err)
	}

	return nil
}

func generateRustFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, rustTemplateFuncs()),
	)
}

func rustTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"toRustFieldName":   toRustFieldName,
		"rustType":          rustType,
		"rustLoaderExpr":    rustLoaderExpr,
		"formatRustStrings": formatRustStrings,
		"usesRustInt":       usesRustInt,
		"usesRustBool":      usesRustBool,
		"usesRustFloat":     usesRustFloat,
		"usesRustEnum":      usesRustEnum,
	}
}

func toRustFieldName(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func rustType(field ConfigField) string {
	if field.IsEnum {
		return "String"
	}

	switch rustPrimitiveType(field) {
	case "Int":
		return "i64"
	case "Float":
		return "f64"
	case "Boolean":
		return "bool"
	default:
		return "String"
	}
}

func rustPrimitiveType(field ConfigField) string {
	if field.IsScalar {
		switch field.ScalarKind {
		case "Float":
			return "Float"
		case "Int":
			return "Int"
		case "Boolean":
			return "Boolean"
		}
	}
	if field.IsScalar && field.ScalarPrimitive != "" {
		return languagePrimitiveLoaderName(field.ScalarPrimitive)
	}
	return languagePrimitiveLoaderName(field.IRType)
}

func rustLoaderExpr(field ConfigField) string {
	keyLiteral := fmt.Sprintf("%q", field.Key)

	if field.IsEnum {
		allowed := formatRustStrings(field.EnumValues)
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_enum(%s, &%s)?", keyLiteral, allowed)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("enum_or_default(%s, &%s, %s)?", keyLiteral, allowed, defaultLiteral)
	}

	switch rustPrimitiveType(field) {
	case "Int":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_i64(%s)?", keyLiteral)
		}
		defaultInt := int64(0)
		if field.HasDefault {
			if parsed, err := strconv.ParseInt(field.DefaultValue, 10, 64); err == nil {
				defaultInt = parsed
			}
		}
		return fmt.Sprintf("i64_or_default(%s, %d)?", keyLiteral, defaultInt)
	case "Boolean":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_bool(%s)?", keyLiteral)
		}
		defaultBool := false
		if field.HasDefault {
			if parsed, err := strconv.ParseBool(field.DefaultValue); err == nil {
				defaultBool = parsed
			}
		}
		return fmt.Sprintf("bool_or_default(%s, %t)?", keyLiteral, defaultBool)
	case "Float":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_f64(%s)?", keyLiteral)
		}
		defaultFloat := float64(0)
		if field.HasDefault {
			if parsed, err := strconv.ParseFloat(field.DefaultValue, 64); err == nil {
				defaultFloat = parsed
			}
		}
		return fmt.Sprintf("f64_or_default(%s, %s)?", keyLiteral, strconv.FormatFloat(defaultFloat, 'g', -1, 64))
	default:
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_string(%s)?", keyLiteral)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("string_or_default(%s, %s)", keyLiteral, defaultLiteral)
	}
}

func formatRustStrings(values []string) string {
	if len(values) == 0 {
		return "[]"
	}

	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}

	return fmt.Sprintf("[%s]", strings.Join(quoted, ", "))
}

func usesRustInt(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && rustPrimitiveType(field) == "Int" {
			return true
		}
	}
	return false
}

func usesRustBool(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && rustPrimitiveType(field) == "Boolean" {
			return true
		}
	}
	return false
}

func usesRustFloat(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && rustPrimitiveType(field) == "Float" {
			return true
		}
	}
	return false
}

func usesRustEnum(fields []ConfigField) bool {
	for _, field := range fields {
		if field.IsEnum {
			return true
		}
	}
	return false
}
