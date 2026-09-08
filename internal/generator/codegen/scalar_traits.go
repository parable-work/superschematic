package codegen

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// BuildScalarTokens creates canonical, symbol, and module stems for a scalar
// identity. Canonical names are namespaced with dot segments (for example,
// "Identity.UUID"); legacy flat names remain representable.
func BuildScalarTokens(canonicalName string) ScalarTokens {
	canonical := strings.TrimSpace(canonicalName)
	segments := splitScalarIdentitySegments(canonical)
	if len(segments) == 0 && canonical != "" {
		segments = []string{canonical}
	}

	moduleSegments := make([]string, 0, len(segments))
	for _, segment := range segments {
		moduleSegments = append(moduleSegments, ToSnakeCase(segment))
	}

	return ScalarTokens{
		Canonical: canonical,
		Symbol:    strings.Join(segments, ""),
		Module:    strings.Join(moduleSegments, "_"),
		Segments:  append([]string{}, segments...),
	}
}

// BuildScalarTraits derives semantic traits from scalar metadata.
func BuildScalarTraits(scalarDef *ir.ScalarDef, tokens ScalarTokens, targetType string) ScalarTraits {
	traits := PrimitiveScalarTraits(scalarDef.LanguagePrimitive)
	inferNameTraits(&traits, tokens)
	inferTypeMappingTraits(&traits, scalarDef, targetType)
	if scalarDef.FileUpload != nil {
		traits.IsPathLike = true
	}
	return traits
}

// PrimitiveScalarTraits returns base traits for host-language primitives.
func PrimitiveScalarTraits(primitive ir.LanguagePrimitive) ScalarTraits {
	switch primitive {
	case ir.LanguageString:
		return ScalarTraits{IsStringLike: true}
	case ir.LanguageNumber:
		return ScalarTraits{IsFloatLike: true}
	case ir.LanguageBoolean:
		return ScalarTraits{IsBooleanLike: true}
	case ir.LanguageObject:
		return ScalarTraits{IsObjectLike: true}
	default:
		return ScalarTraits{}
	}
}

// LanguagePrimitiveTraits returns traits for bare primitive type-reference
// names ("string", "number", "boolean").
func LanguagePrimitiveTraits(typeName string) ScalarTraits {
	switch strings.TrimSpace(typeName) {
	case PrimitiveString:
		return ScalarTraits{IsStringLike: true, IsPathLike: true}
	case PrimitiveNumber:
		return ScalarTraits{IsFloatLike: true}
	case PrimitiveBoolean:
		return ScalarTraits{IsBooleanLike: true}
	default:
		return ScalarTraits{}
	}
}

// GuessScalarTraits provides a best-effort trait derivation for type
// identifiers. This is used in generator paths that only have a type name
// available.
func GuessScalarTraits(typeName string) ScalarTraits {
	known := LanguagePrimitiveTraits(typeName)
	if known != (ScalarTraits{}) {
		return known
	}
	tokens := BuildScalarTokens(typeName)
	traits := ScalarTraits{}
	inferNameTraits(&traits, tokens)
	normalized := strings.ToLower(typeName)
	if strings.Contains(normalized, "userid") {
		traits.IsUUIDLike = true
		traits.IsIDLike = true
		traits.IsStringLike = true
		traits.IsPathLike = true
	}
	if strings.Contains(normalized, "datetime") {
		traits.IsDateTimeLike = true
	}
	return traits
}

func splitScalarIdentitySegments(name string) []string {
	if name == "" {
		return nil
	}
	if strings.Contains(name, ".") {
		return strings.Split(name, ".")
	}
	return []string{name}
}

// containsTypeSegment reports whether term appears as a discrete segment in a
// type string, split on common delimiters (. / * [ ]). This avoids false
// positives from substring matches like "non_uuid_helper" matching "uuid".
func containsTypeSegment(typeName, term string) bool {
	for _, seg := range strings.FieldsFunc(typeName, isTypeDelimiter) {
		if seg == term {
			return true
		}
	}
	return false
}

func isTypeDelimiter(r rune) bool {
	return r == '.' || r == '/' || r == '*' || r == '[' || r == ']'
}

func inferNameTraits(traits *ScalarTraits, tokens ScalarTokens) {
	if len(tokens.Segments) == 0 {
		return
	}
	leaf := strings.ToLower(tokens.Segments[len(tokens.Segments)-1])

	switch leaf {
	case "uuid", "userid":
		traits.IsUUIDLike = true
		traits.IsIDLike = true
		traits.IsStringLike = true
		traits.IsPathLike = true
	case "id":
		traits.IsIDLike = true
		traits.IsStringLike = true
		traits.IsPathLike = true
	case "datetime":
		traits.IsDateTimeLike = true
	case "date":
		traits.IsDateLike = true
	case "time":
		traits.IsTimeLike = true
	case "int", "int32", "int64", "integer":
		traits.IsIntegerLike = true
	case "duration", "milliseconds":
		traits.IsDurationLike = true
	case "json":
		traits.IsJSONLike = true
		traits.IsObjectLike = true
	case "location":
		traits.IsLocationLike = true
		traits.IsObjectLike = true
	case "email":
		traits.IsEmailLike = true
		traits.IsStringLike = true
	case "url", "uri":
		traits.IsURLLike = true
		traits.IsPathLike = true
		traits.IsStringLike = true
	case "slug", "name":
		traits.IsStringLike = true
		traits.IsPathLike = true
	}
}

func inferTypeMappingTraits(traits *ScalarTraits, scalarDef *ir.ScalarDef, targetType string) {
	for _, mapped := range scalarDef.TypeMappings {
		applyTypeStringTraits(traits, mapped)
	}
	applyTypeStringTraits(traits, targetType)

	if scalarDef.Format != "" {
		format := strings.ToLower(strings.TrimSpace(scalarDef.Format))
		switch format {
		case "uuid":
			traits.IsUUIDLike = true
			traits.IsIDLike = true
			traits.IsStringLike = true
			traits.IsPathLike = true
		case "date-time":
			traits.IsDateTimeLike = true
		case "date":
			traits.IsDateLike = true
		case "time":
			traits.IsTimeLike = true
		case "email":
			traits.IsEmailLike = true
			traits.IsStringLike = true
		case "uri", "url":
			traits.IsURLLike = true
			traits.IsStringLike = true
			traits.IsPathLike = true
		}
	}
}

func applyTypeStringTraits(traits *ScalarTraits, typeName string) {
	normalized := strings.ToLower(strings.TrimSpace(typeName))
	if normalized == "" {
		return
	}

	switch normalized {
	case "string", "str":
		traits.IsStringLike = true
	case "int", "int32", "int64", "integer":
		traits.IsIntegerLike = true
	case "float", "float32", "float64", "number":
		traits.IsFloatLike = true
	case "bool", "boolean":
		traits.IsBooleanLike = true
	case "time.time", "datetime", "datetime.datetime", "jsdate":
		traits.IsDateTimeLike = true
	case "time.duration":
		traits.IsDurationLike = true
	case "dict", "interface{}", "object", "json", "jsonb", "record<string, any>":
		traits.IsObjectLike = true
		traits.IsJSONLike = true
	case "uuid", "uuid.uuid":
		traits.IsUUIDLike = true
		traits.IsIDLike = true
		traits.IsStringLike = true
		traits.IsPathLike = true
	}

	if containsTypeSegment(normalized, "uuid") {
		traits.IsUUIDLike = true
		traits.IsIDLike = true
		traits.IsStringLike = true
		traits.IsPathLike = true
	}
	if strings.Contains(normalized, "time.time") {
		traits.IsDateTimeLike = true
	}
	if containsTypeSegment(normalized, "duration") {
		traits.IsDurationLike = true
	}
	if strings.Contains(normalized, "record<") || strings.HasPrefix(normalized, "dict[") {
		traits.IsObjectLike = true
		traits.IsJSONLike = true
	}
}
