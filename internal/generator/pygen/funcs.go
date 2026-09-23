package pygen

import (
	"fmt"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// customTemplateFuncs returns the pygen-specific template helpers. The enum
// lookup spans local and imported enums so default-literal classification
// and masking treat dependency-owned enums correctly.
func customTemplateFuncs(output *ModuleOutput) template.FuncMap {
	enumLookup := buildEnumLookup(output)
	return template.FuncMap{
		"snakeCase":          codegen.ToSnakeCase,
		"pythonString":       pythonString,
		"pythonRawString":    pythonRawString,
		"pythonComment":      pythonComment,
		"pythonScalarType":   pythonScalarType,
		"pythonScalarSymbol": pythonScalarSymbol,
		"pythonScalarModule": pythonScalarModule,
		"scalarDoc": func(s codegen.ScalarInfo) string {
			return codegen.DocText(s.Description, s.Comment)
		},
		"isEnumType": func(typeName string) bool {
			return enumLookup(typeName)
		},
		"hasNonRequiredValidations": hasNonRequiredValidations,
		"allowsExplicitNullRoot":    allowsExplicitNullRoot,
		"pythonFieldDefault": func(field codegen.FieldInfo) string {
			expr, ok := pythonFieldDefaultExpr(field, enumLookup)
			if !ok {
				return ""
			}
			return expr
		},
		"pythonMaskFieldExpr": func(field codegen.FieldInfo) string {
			return pythonMaskFieldExpr(field, enumLookup)
		},
	}
}

// buildEnumLookup reports whether a type name refers to a local or imported enum.
func buildEnumLookup(output *ModuleOutput) codegen.EnumLookup {
	known := make(map[string]struct{}, len(output.Enums)+len(output.ImportedEnumNames))
	for _, e := range output.Enums {
		known[e.Name] = struct{}{}
	}
	for _, name := range output.ImportedEnumNames {
		known[name] = struct{}{}
	}
	return func(typeName string) bool {
		_, ok := known[typeName]
		return ok
	}
}

// hasNonRequiredValidations reports whether the field carries validation
// rules beyond the implicit "required" rule.
func hasNonRequiredValidations(field codegen.FieldInfo) bool {
	for _, rule := range field.Validations {
		if rule.Validator != "required" {
			return true
		}
	}
	return false
}

// genericJSONScalar is the canonical name of the scalar library's any-JSON
// scalar. Its value may be any JSON token, null included.
const genericJSONScalar = "Generic.JSON"

// allowsExplicitNullRoot reports whether the field is a direct Generic.JSON
// value: there None is the JSON null token, a present value, not a missing
// one. A list or map of Generic.JSON still uses None for a missing container.
func allowsExplicitNullRoot(field codegen.FieldInfo) bool {
	return field.IsScalar && field.Type == genericJSONScalar && !field.IsArray && !field.IsMap
}

// pythonString escapes a string for use inside a double-quoted Python string
// literal and normalizes arrow glyphs to ASCII.
func pythonString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	return asciiArrows(s)
}

// pythonRawString escapes only double quotes for use inside a raw r"..."
// Python string literal (regex patterns keep their backslashes).
func pythonRawString(s string) string {
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return asciiArrows(s)
}

// pythonComment formats multi-line doc text for emission as Python comments,
// prefixing continuation lines with "# " and normalizing typography to ASCII.
func pythonComment(s string) string {
	s = asciiArrows(s)
	s = strings.ReplaceAll(s, "\u2026", "...")
	s = strings.ReplaceAll(s, "\u2013", "-")
	s = strings.ReplaceAll(s, "\u2014", "--")
	s = strings.ReplaceAll(s, "\u201c", "\"")
	s = strings.ReplaceAll(s, "\u201d", "\"")
	s = strings.ReplaceAll(s, "\u2018", "'")
	s = strings.ReplaceAll(s, "\u2019", "'")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if i > 0 && strings.TrimSpace(line) != "" {
			lines[i] = "# " + strings.TrimSpace(line)
		}
	}
	return strings.Join(lines, "\n")
}

func asciiArrows(s string) string {
	s = strings.ReplaceAll(s, "\u2192", "->")
	s = strings.ReplaceAll(s, "\u2190", "<-")
	s = strings.ReplaceAll(s, "\u2194", "<->")
	return s
}

// pythonMaskFieldExpr returns the masked-value expression for a field in
// the generated mask_secrets() model_copy update, or "" when the field is
// unchanged by masking (non-secret scalars, enums, and their arrays).
// model_copy skips re-validation, so masked values never trip the strict
// constructor (e.g. enum fields stored as raw values under use_enum_values).
func pythonMaskFieldExpr(field codegen.FieldInfo, enumLookup codegen.EnumLookup) string {
	fieldRef := "self." + field.TargetName
	isScalarLike := field.IsScalar || isPythonScalarTargetType(field.TargetType)
	// Relation fields render as Optional regardless of Required: the full
	// object may not be loaded. Masking must null-check them.
	renderedRequired := field.Required && !field.IsRelation

	if field.Secret {
		if !renderedRequired {
			return "None"
		}
		if field.IsArray {
			return "[]"
		}
		if isScalarLike {
			return pythonScalarZeroValue(field.TargetType)
		}
		if enumLookup(field.Type) {
			return fmt.Sprintf("next(iter(%s)).value", field.TargetType)
		}
		return fieldRef + ".__class__.model_construct()"
	}

	if field.IsArray {
		if field.IsScalar || isPythonScalarArrayTargetType(field.TargetType) || enumLookup(field.Type) {
			return ""
		}

		maskedItems := fmt.Sprintf("[item.mask_secrets() for item in %s]", fieldRef)
		if renderedRequired {
			return maskedItems
		}
		return fmt.Sprintf("%s if %s is not None else None", maskedItems, fieldRef)
	}

	if isScalarLike || enumLookup(field.Type) {
		return ""
	}

	maskedNested := fieldRef + ".mask_secrets()"
	if renderedRequired {
		return maskedNested
	}
	return fmt.Sprintf("%s if %s is not None else None", maskedNested, fieldRef)
}

func isPythonScalarTargetType(targetType string) bool {
	switch targetType {
	case "bool", "int", "float", "str", "dict", "Any":
		return true
	}
	return strings.HasPrefix(targetType, "Dict[")
}

func isPythonScalarArrayTargetType(targetType string) bool {
	if !strings.HasPrefix(targetType, "List[") || !strings.HasSuffix(targetType, "]") {
		return false
	}
	elemType := strings.TrimSuffix(strings.TrimPrefix(targetType, "List["), "]")
	return isPythonScalarTargetType(elemType)
}

func pythonScalarZeroValue(targetType string) string {
	if isPythonScalarArrayTargetType(targetType) {
		return "[]"
	}
	switch targetType {
	case "bool":
		return "False"
	case "int":
		return "0"
	case "float":
		return "0.0"
	case "str":
		return "\"\""
	case "dict", "Any":
		return "{}"
	}
	if strings.HasPrefix(targetType, "Dict[") {
		return "{}"
	}
	return targetType + "()"
}

// pythonScalarType maps a scalar's TargetType to the base type used inside
// the generated Annotated alias.
func pythonScalarType(targetType string) string {
	targetType = strings.TrimSpace(targetType)
	if targetType == "" {
		return "Any"
	}

	switch targetType {
	case "str", "int", "float", "bool", "Any":
		return targetType
	case "dict":
		return "dict"
	case "datetime", "datetime.datetime":
		return "datetime"
	case "uuid.UUID":
		// Keep UUID-compatible values as strings in generated scalar aliases.
		return "str"
	}

	if strings.HasPrefix(targetType, "Dict[") || strings.HasPrefix(targetType, "List[") || strings.HasPrefix(targetType, "Optional[") {
		return targetType
	}

	// Custom model type mappings are not importable here without introducing
	// circular dependencies with generated types.py.
	return "Any"
}
