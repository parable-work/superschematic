package tsgen

import (
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// customTemplateFuncs returns template functions specific to the TypeScript
// type generator, merged on top of codegen.BaseTemplateFuncs by generateFile.
func customTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"uniqueScalarLibTypeNames": uniqueScalarLibTypeNames,
		"scalarLibTypeName":        scalarLibTypeName,
		"elemType":                 elemType,
		"hasStringValidator": func(s ScalarInfo) bool {
			return s.TSType == "string" && s.Primitive == ir.LanguageString
		},
		"containsString": func(values []string, target string) bool {
			for _, value := range values {
				if value == target {
					return true
				}
			}
			return false
		},
		"jsonString": func(v interface{}) string {
			switch val := v.(type) {
			case string:
				return jsonQuote(val)
			default:
				return fmt.Sprintf("%v", val)
			}
		},
		"hasFieldLevelValidations": func(field FieldInfo) bool {
			// Required checks are emitted separately; scalar constraints are
			// covered by the scalar validators. Field-level blocks are only
			// needed for explicit @validate rules (any rule on non-scalars
			// that can fail for the field's type, list-size rules on
			// scalars).
			for _, v := range field.Validations {
				if v.Validator == "required" || !ruleApplies(field, v.Validator) {
					continue
				}
				if !field.IsScalar {
					return true
				}
				if v.Validator == "listMin" || v.Validator == "listMax" {
					return true
				}
			}
			return false
		},
		"listValidations":    listValidations,
		"elementValidations": elementValidations,
		"primitiveCheck":     primitiveCheck,
		"ruleApplies":        ruleApplies,
		// A map value of an any-JSON scalar (Generic.JSON) may be null: the
		// rule that null is a missing Generic.JSON value covers a field and a
		// list element, and no validator in the other languages checks map
		// values for null.
		"mapScalarValueRequired": func(field FieldInfo) bool {
			if !field.IsMap || field.IsArray || isAnyJSONField(field) {
				return false
			}
			return !strings.Contains(field.TSType, "| null")
		},
		"mapArrayElementRequired": func(field FieldInfo) bool {
			if !field.IsMap || !field.IsArray || isAnyJSONField(field) {
				return false
			}
			return !strings.Contains(field.TSType, "| null")
		},
	}
}

// isAnyJSONField reports whether field's scalar holds any JSON value.
func isAnyJSONField(field FieldInfo) bool {
	return field.IsScalar && field.ScalarInfo != nil && field.ScalarInfo.IsAnyJSON
}

// listValidations returns a field's list-size rules (listMin, listMax). On a
// T[][] field they bound the outer list only.
func listValidations(field FieldInfo) []codegen.ValidationRule {
	var rules []codegen.ValidationRule
	for _, v := range field.Validations {
		if v.Validator == "listMin" || v.Validator == "listMax" {
			rules = append(rules, v)
		}
	}
	return rules
}

// elementValidations returns the explicit @validate rules a field's
// validator checks on each value, which for a T[][] field is each innermost
// element: every rule except required and the list-size rules. A scalar
// field returns none, because its scalar validator covers these constraints.
func elementValidations(field FieldInfo) []codegen.ValidationRule {
	if field.IsScalar {
		return nil
	}
	var rules []codegen.ValidationRule
	for _, v := range field.Validations {
		switch v.Validator {
		case "required", "listMin", "listMax":
			continue
		}
		if ruleApplies(field, v.Validator) {
			rules = append(rules, v)
		}
	}
	return rules
}

// Helpers of the generated validators/primitives.ts module.
const (
	expectStringHelper  = "expectString"
	expectNumberHelper  = "expectNumber"
	expectBooleanHelper = "expectBoolean"
	finiteNumberHelper  = "isFiniteNumber"
)

// primitiveCheck returns the validators/primitives.ts helper that reports
// "type" for a value of the wrong JSON type in a field typed with a builtin
// primitive (string, number, boolean), or "" for any other field.
func primitiveCheck(field FieldInfo) string {
	if field.IsScalar {
		return ""
	}
	switch field.Type {
	case codegen.PrimitiveString:
		return expectStringHelper
	case codegen.PrimitiveNumber:
		return expectNumberHelper
	case codegen.PrimitiveBoolean:
		return expectBooleanHelper
	}
	return ""
}

// ruleApplies reports whether a field's @validate rule can fail for a value
// the validator checks it on. A builtin primitive field's value of the
// wrong JSON type is "type" and no rule checks it, so a string rule
// (minLength, maxLength, pattern) applies only to a string field and a range
// rule (min, max) only to a number field, as in the schema runtimes. Every
// rule applies to any other field.
func ruleApplies(field FieldInfo, validator string) bool {
	stringRule := validator == "minLength" || validator == "maxLength" || validator == "pattern"
	rangeRule := validator == "min" || validator == "max"
	switch primitiveCheck(field) {
	case expectStringHelper:
		return !rangeRule
	case expectNumberHelper:
		return !stringRule
	case expectBooleanHelper:
		return !stringRule && !rangeRule
	}
	return true
}

// typePrimitiveHelpers returns the validators/primitives.ts helpers a
// type's validator calls, sorted: a type check per builtin primitive field
// and isFiniteNumber for a range rule.
func typePrimitiveHelpers(t *TypeInfo) []string {
	used := map[string]bool{}
	for _, f := range t.Fields {
		if check := primitiveCheck(f); check != "" {
			used[check] = true
		}
		if f.IsScalar {
			continue
		}
		for _, v := range f.Validations {
			if (v.Validator == "min" || v.Validator == "max") && ruleApplies(f, v.Validator) {
				used[finiteNumberHelper] = true
			}
		}
	}
	helpers := make([]string, 0, len(used))
	for helper := range used {
		helpers = append(helpers, helper)
	}
	sort.Strings(helpers)
	return helpers
}

// scalarLibTypeName returns the structured superscalar type name a scalar's
// TypeScript type resolves to, or "" when the scalar maps to a builtin shape
// (string/number/boolean/JSDate/object literals/generics). Structured types
// are declared in superscalar's scalar-validators module and must be imported
// from there.
func scalarLibTypeName(s ScalarInfo) string {
	// JSONValue (Generic.JSON) is declared by superscalar whatever the
	// scalar's primitive.
	if s.Primitive != ir.LanguageObject && s.TSType != "JSONValue" {
		return ""
	}
	if s.TSType == "" || !isImportableType(s.TSType) {
		return ""
	}
	return s.TSType
}

// uniqueScalarLibTypeNames returns unique structured superscalar type names
// used by the module's scalars, for re-export from superscalar.
func uniqueScalarLibTypeNames(scalars []ScalarInfo) []string {
	seen := make(map[string]bool)
	var names []string
	for _, s := range scalars {
		name := scalarLibTypeName(s)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	return names
}

// elemType strips a trailing "[]" from a TypeScript type to get the element type.
func elemType(tsType string) string {
	return strings.TrimSuffix(tsType, "[]")
}

// typeScalarsUsed returns unique scalars used by the given type's fields.
func typeScalarsUsed(t *TypeInfo) []ScalarInfo {
	seen := make(map[string]bool)
	var scalars []ScalarInfo
	for _, f := range t.Fields {
		if f.IsScalar && f.ScalarInfo != nil && !seen[f.ScalarInfo.Name] {
			seen[f.ScalarInfo.Name] = true
			scalars = append(scalars, *f.ScalarInfo)
		}
	}
	return scalars
}

// typeEnumNames returns unique enum type names used by the given type's fields.
func typeEnumNames(t *TypeInfo, moduleEnums []codegen.EnumInfo) []string {
	enumSet := make(map[string]struct{}, len(moduleEnums))
	for _, enum := range moduleEnums {
		enumSet[enum.Name] = struct{}{}
	}

	seen := make(map[string]bool)
	var names []string
	for _, f := range t.Fields {
		if _, ok := enumSet[f.Type]; !ok {
			continue
		}
		if seen[f.Type] {
			continue
		}
		seen[f.Type] = true
		names = append(names, f.Type)
	}
	return names
}

// typeImportedEnumsUsed returns the imported enum definitions referenced by
// the given type's fields. Imported enums are not emitted into the local
// enums module, so per-type validators import their validators from the
// owning package's validators/enums subpath.
func typeImportedEnumsUsed(t *TypeInfo, importedTypes []ImportedTypeInfo) []ImportedTypeInfo {
	fieldTypes := make(map[string]struct{}, len(t.Fields))
	for _, field := range t.Fields {
		fieldTypes[field.Type] = struct{}{}
	}

	var enums []ImportedTypeInfo
	for _, imported := range importedTypes {
		if !imported.IsEnum {
			continue
		}
		if _, used := fieldTypes[imported.Name]; !used {
			continue
		}
		enums = append(enums, imported)
	}
	return enums
}

// typeEnumDefaultsUsed returns unique enum type names referenced in @default
// literals on the type's fields. The validator template imports these as
// types so it can emit casts like `"OFF" as Phase` when seeding defaults.
func typeEnumDefaultsUsed(t *TypeInfo, moduleEnums []codegen.EnumInfo) []string {
	enumSet := make(map[string]struct{}, len(moduleEnums))
	for _, enum := range moduleEnums {
		enumSet[enum.Name] = struct{}{}
	}

	seen := make(map[string]bool)
	var names []string
	for _, f := range t.Fields {
		if !f.HasDefault {
			continue
		}
		if _, ok := enumSet[f.Type]; !ok {
			continue
		}
		if seen[f.Type] {
			continue
		}
		seen[f.Type] = true
		names = append(names, f.Type)
	}
	return names
}

// typeMaskTypesUsed returns unique nested generated type names referenced by
// the given type's fields, for recursive mask helper imports/calls.
func typeMaskTypesUsed(t *TypeInfo, generatedTypeNames map[string]bool) []string {
	seen := make(map[string]bool)
	var names []string
	for _, f := range t.Fields {
		if f.IsScalar || f.Type == "" || f.Type == t.Name || !generatedTypeNames[f.Type] || seen[f.Type] {
			continue
		}
		seen[f.Type] = true
		names = append(names, f.Type)
	}
	return names
}

// typeMaskTypeImports returns unique types that must be type-imported by a
// per-type mask helper: recursively-maskable generated types and custom
// scalar TS types used in secret fallback casts.
func typeMaskTypeImports(t *TypeInfo, generatedTypeNames map[string]bool) []string {
	seen := make(map[string]bool)
	var names []string
	for _, f := range t.Fields {
		if f.Type != "" && f.Type != t.Name && generatedTypeNames[f.Type] && !seen[f.Type] {
			seen[f.Type] = true
			names = append(names, f.Type)
		}

		needsSecretFallbackCast := f.Secret &&
			f.Required &&
			!f.IsArray &&
			f.TSType != "" &&
			f.TSType != "string" &&
			f.TSType != "number" &&
			f.TSType != "boolean" &&
			f.TSType != "JSDate"
		if needsSecretFallbackCast && isImportableType(f.TSType) && !seen[f.TSType] {
			seen[f.TSType] = true
			names = append(names, f.TSType)
		}
	}
	return names
}

// typeParserNestedTypes returns unique nested generated type names referenced
// by fields that need recursive JSON parsing.
func typeParserNestedTypes(t *TypeInfo, generatedTypeNames map[string]bool) []string {
	seen := make(map[string]bool)
	var names []string
	for _, f := range t.Fields {
		if f.IsScalar || f.Type == "" || f.Type == t.Name || !generatedTypeNames[f.Type] || seen[f.Type] {
			continue
		}
		seen[f.Type] = true
		names = append(names, f.Type)
	}
	return names
}

// typeValidatesNestedObjects reports whether validate<Type> of a type that is
// not @strictJSON validates a nested object field: one whose type is a local
// generated type (parserNestedTypes) or the type itself. A @strictJSON type
// validates its nested fields through validate<Nested>Required instead.
func typeValidatesNestedObjects(t *TypeInfo, parserNestedTypes []string) bool {
	if t.StrictJSON {
		return false
	}
	if len(parserNestedTypes) > 0 {
		return true
	}
	for _, f := range t.Fields {
		if !f.IsScalar && f.Type == t.Name {
			return true
		}
	}
	return false
}

// typeNeedsJSONParse returns true if the type has any fields that need
// runtime transformation when parsing from JSON: DateTime-like scalars that
// must be converted from string to Date, or nested object types that need
// recursive parsing.
func typeNeedsJSONParse(t *TypeInfo, generatedTypeNames map[string]bool) bool {
	for _, f := range t.Fields {
		if f.IsScalar && f.ScalarInfo != nil &&
			f.ScalarInfo.TSType == "JSDate" && f.ScalarInfo.HasParseFromJSON {
			return true
		}
		if !f.IsScalar && f.Type != "" && f.Type != t.Name && generatedTypeNames[f.Type] {
			return true
		}
	}
	return false
}

// typeParserScalarsUsed returns scalars that need parse-from-library calls in
// parseFromJSON: DateTime-like scalars with a custom parse in superscalar.
func typeParserScalarsUsed(t *TypeInfo) []ScalarInfo {
	seen := make(map[string]bool)
	var scalars []ScalarInfo
	for _, f := range t.Fields {
		if f.IsScalar && f.ScalarInfo != nil &&
			f.ScalarInfo.TSType == "JSDate" && f.ScalarInfo.HasParseFromJSON &&
			!seen[f.ScalarInfo.Name] {
			seen[f.ScalarInfo.Name] = true
			scalars = append(scalars, *f.ScalarInfo)
		}
	}
	return scalars
}
