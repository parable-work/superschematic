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
// of a field value for length/pattern validation.
func validationStringExpr(field FieldInfo, valueVar string) string {
	if field.IsScalar && field.ScalarInfo != nil && field.ScalarInfo.Traits.IsUUIDLike {
		return valueVar + ".String()"
	}
	return "string(" + valueVar + ")"
}

// templateFuncs returns the typegen-specific template functions.
func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"hasEmittableValidations": hasEmittableValidations,
		"isZeroListMinimum":       isZeroListMinimum,
		"validationStringExpr":    validationStringExpr,
		"isEnumType":              isEnumType,
		"isValidatableScalarField": func(field FieldInfo) bool {
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
		},
		"isGeneratedType": func(types []TypeInfo, importedTypes []ImportedTypeInfo, typeName string) bool {
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
		},
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
