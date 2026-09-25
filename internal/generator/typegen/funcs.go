package typegen

import (
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// isEnumType reports whether typeName refers to a local or imported enum.
func isEnumType(localEnums []EnumInfo, importedEnums []ImportedEnumInfo, typeName string) bool {
	name := strings.TrimSpace(typeName)
	if name == "" {
		return false
	}

	for _, enumInfo := range localEnums {
		if enumInfo.Name == name {
			return true
		}
	}

	for _, enumInfo := range importedEnums {
		if enumInfo.Name == name {
			return true
		}
	}

	return false
}

// hasEmittableValidations reports whether any rule produces inline Go checks.
func hasEmittableValidations(validations []codegen.ValidationRule) bool {
	for _, validation := range validations {
		if isZeroListMinimum(validation) {
			continue
		}
		switch validation.Validator {
		case "minLength", "maxLength", "listMin", "listMax", "pattern", "min", "max":
			return true
		}
	}
	return false
}

func isZeroListMinimum(validation codegen.ValidationRule) bool {
	if validation.Validator != "listMin" {
		return false
	}
	minimum, ok := validation.Value.(int)
	return ok && minimum == 0
}

// validationStringExpr returns the Go expression that yields the string form
// of a field value for length/pattern validation. A UUID or duration scalar
// is not string-backed (Temporal.Duration is an int64 time.Duration), so a
// string conversion would yield a single rune; it formats through String.
func validationStringExpr(field FieldInfo, valueVar string) string {
	if field.IsScalar && field.ScalarInfo != nil &&
		(field.ScalarInfo.Traits.IsUUIDLike || field.ScalarInfo.Traits.IsDurationLike) {
		return valueVar + ".String()"
	}
	return "string(" + valueVar + ")"
}

// isValidatableScalarField reports whether field's Go type is a scalar with
// its own Validate and ValidateRequired methods.
func isValidatableScalarField(field FieldInfo) bool {
	if !field.IsScalar || field.ScalarInfo == nil {
		return false
	}
	// JSON-like scalars map to generic Go values (for example
	// interface{}) and do not provide scalar Validate /
	// ValidateRequired methods.
	if field.ScalarInfo.Traits.IsJSONLike {
		return false
	}
	return true
}

// autoFilledField reports whether Validate leaves a required field alone
// because the store fills it: internal metadata and the audit fields.
func autoFilledField(field FieldInfo) bool {
	if !field.Required || field.HasDefault {
		return false
	}
	if field.InternalMetadata {
		return true
	}
	switch field.Name {
	case "id", "createdAt", "createdBy", "updatedAt", "updatedBy":
		return true
	}
	return false
}

// scalarValidates reports whether Validate hands field's values to the
// scalar type's own Validate or ValidateRequired. The scalar core checks the
// scalar's pattern, lengths and range there, so the rules copied from the
// scalar (FromScalar) would report a malformed value a second time.
func scalarValidates(field FieldInfo) bool {
	if !isValidatableScalarField(field) {
		return false
	}
	if field.IsMap || field.IsArrayOfArrays {
		return true
	}
	return !autoFilledField(field)
}

// withoutScalarRules drops the rules copied from the scalar type.
func withoutScalarRules(rules []codegen.ValidationRule) []codegen.ValidationRule {
	kept := make([]codegen.ValidationRule, 0, len(rules))
	for _, rule := range rules {
		if !rule.FromScalar {
			kept = append(kept, rule)
		}
	}
	return kept
}

// isGeneratedType reports whether typeName is an object or input type this
// module declares or aliases, which has Validate and MaskSecrets methods.
func isGeneratedType(types []TypeInfo, importedTypes []ImportedTypeInfo, typeName string) bool {
	for _, typeInfo := range types {
		if typeInfo.Name == typeName {
			return true
		}
	}
	for _, typeInfo := range importedTypes {
		if typeInfo.Name == typeName {
			return true
		}
	}
	return false
}

// nestedList is what the nested-list template blocks render a T[][] field
// from.
type nestedList struct {
	Field FieldInfo

	// ListGoType is the field's Go type without an InputField wrapper,
	// [][]T, and InnerGoType the type of one inner list, []T.
	ListGoType  string
	InnerGoType string

	// ElemValidate names the method each innermost scalar or enum element
	// validates with, ValidateRequired or Validate as for T[]. It is empty
	// for other element types.
	ElemValidate string

	// ElemNested is true when each innermost element is a generated type
	// that validates and masks its own fields.
	ElemNested bool

	// ListBounds are the listMin and listMax rules, checked against the
	// outer list. ElemRules are the length, pattern and range rules, checked
	// against every innermost element.
	ListBounds []codegen.ValidationRule
	ElemRules  []codegen.ValidationRule
}

// ChecksElements reports whether Validate visits each innermost element.
func (n nestedList) ChecksElements() bool {
	return n.ElemValidate != "" || n.ElemNested || len(n.ElemRules) > 0
}

// newNestedList describes the T[][] field to the template, resolving its
// element kind against the module's types and enums.
func newNestedList(module *ModuleOutput, field FieldInfo) nestedList {
	listType := field.GoType
	if field.UsesWrapper {
		listType = inputFieldValueType(listType)
	}
	list := nestedList{
		Field:       field,
		ListGoType:  listType,
		InnerGoType: strings.TrimPrefix(listType, "[]"),
	}
	switch {
	case isValidatableScalarField(field) || isEnumType(module.Enums, module.ImportedEnums, field.Type):
		list.ElemValidate = "Validate"
		if field.Required && !field.HasDefault {
			list.ElemValidate = "ValidateRequired"
		}
	case !field.IsScalar && isGeneratedType(module.Types, module.ImportedTypes, field.Type):
		list.ElemNested = true
	}
	for _, rule := range field.Validations {
		switch rule.Validator {
		case "listMin":
			if !isZeroListMinimum(rule) {
				list.ListBounds = append(list.ListBounds, rule)
			}
		case "listMax":
			list.ListBounds = append(list.ListBounds, rule)
		case "minLength", "maxLength", "pattern", "min", "max":
			list.ElemRules = append(list.ElemRules, rule)
		}
	}
	return list
}

// inputFieldValueType returns T for the Go type InputField[T], the type an
// optional input field's wrapper holds.
func inputFieldValueType(goType string) string {
	return strings.TrimSuffix(strings.TrimPrefix(goType, "InputField["), "]")
}

// templateFuncs returns the typegen-specific template functions.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"hasEmittableValidations":  hasEmittableValidations,
		"isZeroListMinimum":        isZeroListMinimum,
		"validationStringExpr":     validationStringExpr,
		"isEnumType":               isEnumType,
		"isValidatableScalarField": isValidatableScalarField,
		"isGeneratedType":          isGeneratedType,
		"nestedList":               newNestedList,
		"autoFilledField":          autoFilledField,
		"inputFieldValueType":      inputFieldValueType,
		"hasUnionFields": func(fields []FieldInfo) bool {
			for _, f := range fields {
				if f.IsUnion {
					return true
				}
			}
			return false
		},
		"unionWrapperType": func(typeName string, importedUnions []ImportedUnionInfo) string {
			for _, iu := range importedUnions {
				if iu.Name == typeName {
					return iu.ImportAlias + "." + typeName + "Wrapper"
				}
			}
			return typeName + "Wrapper"
		},
		"formatComment": func(name, doc string) string {
			return codegen.FormatComment("// ", name, doc)
		},
	}
}
