package rustgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// rustDefaultFnName builds the snake_case free-function name used by serde
// via #[serde(default = "...")]. The shape is `default_<type>_<field>` and
// matches the generated `fn default_settings_port() -> i64` pattern used by
// the types.rs template.
func rustDefaultFnName(typeName string, field FieldInfo) string {
	fieldName := strings.TrimPrefix(field.RustName, "r#")
	if fieldName == "" {
		fieldName = codegen.ToSnakeCase(field.Name)
	}
	return "default_" + codegen.ToSnakeCase(typeName) + "_" + fieldName
}

// formatRustDefaultLiteral renders the raw @default value as a Rust
// expression suitable for use inside the body of a default function.
//
// Optional (non-required) fields are wrapped in Some(...) so the function's
// return type matches the field's Option<T> serde type.
//
// Returns ok=false when the raw value cannot be parsed for the declared kind
// or when no matching enum variant exists for an enum default.
func formatRustDefaultLiteral(raw string, kind codegen.DefaultLiteralKind, field FieldInfo, enums []codegen.EnumInfo) (string, bool) {
	literal, ok := rustInnerLiteral(raw, kind, field, enums)
	if !ok {
		return "", false
	}
	if !field.Required {
		return fmt.Sprintf("Some(%s)", literal), true
	}
	return literal, true
}

func rustInnerLiteral(raw string, kind codegen.DefaultLiteralKind, field FieldInfo, enums []codegen.EnumInfo) (string, bool) {
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
		return integerLiteralFor(field.RustType, trimmed)
	case codegen.DefaultLiteralFloat:
		if _, err := strconv.ParseFloat(trimmed, 64); err != nil {
			return "", false
		}
		return trimmed + "_f64", true
	case codegen.DefaultLiteralString:
		if innerRustType(field.RustType) != "String" {
			// Scalar aliases over non-String Rust types cannot carry a raw
			// string literal.
			return "", false
		}
		return strconv.Quote(raw) + ".to_string()", true
	case codegen.DefaultLiteralEnum:
		variant, ok := rustEnumVariantForValue(field.Type, raw, enums)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("%s::%s", field.Type, variant), true
	case codegen.DefaultLiteralEmptyArray:
		if strings.HasPrefix(innerRustType(field.RustType), "Vec<") {
			return "Vec::new()", true
		}
		return "", false
	}
	return "", false
}

// integerLiteralFor suffixes an integer default with the field's concrete
// Rust numeric type so the default function type-checks (the schema's
// number primitive maps to f64 unless the scalar is integer-like).
func integerLiteralFor(rustType, trimmed string) (string, bool) {
	switch innerRustType(rustType) {
	case "i64":
		return trimmed + "_i64", true
	case "f64":
		return trimmed + "_f64", true
	}
	return "", false
}

// innerRustType strips an Option<...> wrapper from a field's Rust type.
func innerRustType(rustType string) string {
	rustType = strings.TrimSpace(rustType)
	if strings.HasPrefix(rustType, "Option<") && strings.HasSuffix(rustType, ">") {
		return strings.TrimSuffix(strings.TrimPrefix(rustType, "Option<"), ">")
	}
	return rustType
}

// rustEnumVariantForValue maps a raw @default value (e.g. "OFF") to the
// generated Rust enum variant (e.g. "Off") for the given enum type. Returns
// false when the value does not match any of the enum's declared values.
func rustEnumVariantForValue(enumName, rawValue string, enums []codegen.EnumInfo) (string, bool) {
	for _, e := range enums {
		if e.Name != enumName {
			continue
		}
		for _, v := range e.Values {
			if v.Name == rawValue || v.Value == rawValue {
				return toRustEnumVariant(v.Name), true
			}
		}
	}
	return "", false
}
