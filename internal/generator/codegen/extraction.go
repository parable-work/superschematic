package codegen

import (
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
)

// FieldTypeMapper maps an IR type reference to a target language type string.
// Parameters:
//   - typeName: the base type name (scalar, enum, object, primitive, etc.)
//   - isArray: whether this is a list/array type
//   - isMap: whether this is a string-keyed map type
//   - isRequired: whether the field is non-null (affects pointer types in Go)
//   - scalarMap: lookup table for resolved scalar types
type FieldTypeMapper func(typeName string, isArray bool, isMap bool, isRequired bool, scalarMap ScalarMap) string

// FieldNameMapper converts a schema field name to the target naming convention.
type FieldNameMapper func(name string) string

// PrimitiveTypeMapper maps a host-language primitive (and the scalar name,
// for context) to a target language type. Used as the fallback when a scalar
// has no TypeMappings entry for the target language.
type PrimitiveTypeMapper func(primitive ir.LanguagePrimitive, scalarName string) string

// ScalarPostProcessor allows language-specific post-processing of scalar info.
type ScalarPostProcessor func(scalar *ScalarInfo, scalarDef *ir.ScalarDef)

// ExtractionConfig configures the shared IR extraction layer.
type ExtractionConfig struct {
	// Language is the TypeMappings key for the target language:
	// "go", "typescript", "python", "rust", "sql", "json_schema".
	Language string

	// PrimitiveMapper maps host-language primitives to target language types
	// when no TypeMappings entry exists for the target language.
	PrimitiveMapper PrimitiveTypeMapper

	// FieldTypeMapper maps IR type references to target language types.
	FieldTypeMapper FieldTypeMapper

	// FieldNameMapper converts field names to the target naming convention.
	FieldNameMapper FieldNameMapper

	// ScalarPostProcessor allows language-specific scalar post-processing.
	ScalarPostProcessor ScalarPostProcessor
}

// ExtractScalars extracts scalar information from the Schema IR. It reads
// type mappings from ScalarDef.TypeMappings and falls back to
// PrimitiveMapper when no mapping exists for the target language.
//
// Custom superscalar implementation flags (normalize/validate/parse) come from
// the IR itself; there is no filesystem detection in v2.
func ExtractScalars(schema *ir.Schema, config ExtractionConfig) []ScalarInfo {
	var scalars []ScalarInfo

	for _, scalarDef := range schema.Scalars {
		tokens := BuildScalarTokens(scalarDef.Name)
		scalar := ScalarInfo{
			Name:                         scalarDef.Name,
			Description:                  scalarDef.Description,
			Comment:                      scalarDef.Comment,
			Primitive:                    scalarDef.LanguagePrimitive,
			MaxLength:                    scalarDef.MaxLength,
			MinLength:                    scalarDef.MinLength,
			Pattern:                      scalarDef.Pattern,
			Format:                       scalarDef.Format,
			CaseInsensitive:              scalarDef.CaseInsensitive,
			ReservedWords:                scalarDef.ReservedWords,
			ReservedWordsCaseInsensitive: scalarDef.ReservedWordsCaseInsensitive,
			ReservedWordsMatchPartial:    scalarDef.ReservedWordsMatchPartial,
			Minimum:                      scalarDef.Minimum,
			Maximum:                      scalarDef.Maximum,
			Example:                      scalarDef.Example,
			HasCustomNormalize:           scalarDef.HasCustomNormalize,
			HasCustomValidate:            scalarDef.HasCustomValidate,
			HasCustomParse:               scalarDef.HasCustomParse,
			Tokens:                       tokens,
		}

		if mapping, ok := scalarDef.TypeMappings[config.Language]; ok {
			scalar.TargetType = mapping
		}

		if scalar.TargetType == "" && config.PrimitiveMapper != nil {
			scalar.TargetType = config.PrimitiveMapper(scalarDef.LanguagePrimitive, scalarDef.Name)
		}

		scalar.Traits = BuildScalarTraits(scalarDef, tokens, scalar.TargetType)
		if config.ScalarPostProcessor != nil {
			config.ScalarPostProcessor(&scalar, scalarDef)
		}

		scalars = append(scalars, scalar)
	}

	sort.Slice(scalars, func(i, j int) bool {
		return scalars[i].Name < scalars[j].Name
	})

	return scalars
}

// ExtractTypes extracts object type information from the Schema IR for the
// given roles. All roles share the IR's Types map; generators key off Role
// to select what they consume.
func ExtractTypes(schema *ir.Schema, scalars []ScalarInfo, config ExtractionConfig, roles ...ir.Role) []TypeInfo {
	roleSet := make(map[ir.Role]bool, len(roles))
	for _, role := range roles {
		roleSet[role] = true
	}

	var types []TypeInfo
	scalarMap := BuildScalarMap(scalars)

	for _, typeDef := range schema.Types {
		if !roleSet[typeDef.Role] {
			continue
		}

		typeInfo := TypeInfo{
			Name:              typeDef.Name,
			Owner:             typeDef.Owner,
			Role:              typeDef.Role,
			Description:       typeDef.Description,
			Comment:           typeDef.Comment,
			Extends:           typeDef.Extends,
			Fields:            make([]FieldInfo, 0, len(typeDef.Fields)),
			JsonField:         typeDef.JsonField,
			DenyUnknownFields: typeDef.DenyUnknownFields,
			EnvVars:           typeDef.EnvVars,
			Versioned:         typeDef.Versioned,
			VersionedConfig:   typeDef.VersionedConfig,
		}

		for _, field := range typeDef.Fields {
			typeInfo.Fields = append(typeInfo.Fields, ExtractFieldInfo(field, scalarMap, schema, config))
		}

		types = append(types, typeInfo)
	}

	sort.Slice(types, func(i, j int) bool {
		return types[i].Name < types[j].Name
	})

	return types
}

// ExtractEnums extracts enum type information from the Schema IR.
func ExtractEnums(schema *ir.Schema) []EnumInfo {
	var enums []EnumInfo

	for _, enumDef := range schema.Enums {
		enumInfo := EnumInfo{
			Name:        enumDef.Name,
			Owner:       enumDef.Owner,
			Description: enumDef.Description,
			Comment:     enumDef.Comment,
			Values:      make([]EnumValueInfo, 0, len(enumDef.Values)),
		}

		for _, val := range enumDef.Values {
			valueInfo := EnumValueInfo{
				Name:        val.Name,
				Value:       val.Name,
				Description: val.Description,
				Comment:     val.Comment,
			}
			if val.SerializedAs != "" {
				valueInfo.Value = val.SerializedAs
			}
			enumInfo.Values = append(enumInfo.Values, valueInfo)
		}

		enums = append(enums, enumInfo)
	}

	sort.Slice(enums, func(i, j int) bool {
		return enums[i].Name < enums[j].Name
	})

	return enums
}

// ExtractUnions extracts union type information from the Schema IR.
func ExtractUnions(schema *ir.Schema) []UnionInfo {
	var unions []UnionInfo

	for _, unionDef := range schema.Unions {
		discriminator := detectUnionDiscriminator(unionDef.Types, schema)
		members := buildUnionMembers(unionDef.Types, discriminator, schema)
		unions = append(unions, UnionInfo{
			Name:          unionDef.Name,
			Description:   unionDef.Description,
			Comment:       unionDef.Comment,
			Types:         append([]string{}, unionDef.Types...),
			Discriminator: discriminator,
			Members:       members,
		})
	}

	sort.Slice(unions, func(i, j int) bool {
		return unions[i].Name < unions[j].Name
	})

	return unions
}

// BaseTypeNames returns the set of type names other types extend. Base
// classes are flattened into their subclasses at load time; generators that
// materialize storage (tables, repositories) skip these.
func BaseTypeNames(schema *ir.Schema) map[string]bool {
	bases := make(map[string]bool)
	for _, typeDef := range schema.Types {
		if typeDef.Extends != "" {
			bases[typeDef.Extends] = true
		}
	}
	return bases
}

// ExtractFieldInfo extracts field information from an IR FieldDef.
func ExtractFieldInfo(field *ir.FieldDef, scalarMap ScalarMap, schema *ir.Schema, config ExtractionConfig) FieldInfo {
	required := field.Required && !field.AutoGenerated

	targetName := field.Name
	if config.FieldNameMapper != nil {
		targetName = config.FieldNameMapper(field.Name)
	}

	var targetType string
	if config.FieldTypeMapper != nil {
		targetType = config.FieldTypeMapper(
			field.TypeRef.Name,
			field.TypeRef.IsArray,
			field.TypeRef.IsMap,
			required,
			scalarMap,
		)
	}

	fieldInfo := FieldInfo{
		Name:             field.Name,
		TargetName:       targetName,
		Type:             field.TypeRef.Name,
		TargetType:       targetType,
		Required:         required,
		InternalMetadata: field.InternalMetadata,
		Secret:           field.Secret,
		IsArray:          field.TypeRef.IsArray,
		IsMap:            field.TypeRef.IsMap,
		IsPrimitive:      IsLanguagePrimitive(field.TypeRef.Name),
		InheritedFrom:    field.InheritedFrom,
		Description:      field.Description,
		Comment:          field.Comment,
		Validations:      []ValidationRule{},
		Default:          field.Default,
	}

	if scalarInfo, ok := scalarMap[field.TypeRef.Name]; ok {
		fieldInfo.IsScalar = true
		fieldInfo.ScalarInfo = scalarInfo
	}

	if schema != nil {
		if _, ok := schema.Unions[field.TypeRef.Name]; ok {
			fieldInfo.IsUnion = true
		}
	}

	if field.Relation != nil || field.HasMany || field.ManyToMany {
		fieldInfo.IsRelation = true
	}

	fieldInfo.Validations = buildValidations(fieldInfo.ScalarInfo, field, fieldInfo.Required)

	return fieldInfo
}

// AddVersionFields appends superschematic's readable _version metadata field to
// versioned DB table type models. Generators opt in explicitly so Phase 2 can
// scope the new type surface to the languages it supports.
func AddVersionFields(types []TypeInfo, config ExtractionConfig) []TypeInfo {
	for i := range types {
		if types[i].Role != ir.RoleDBTable || !types[i].Versioned {
			continue
		}
		if hasFieldNamed(types[i].Fields, "_version") {
			continue
		}
		types[i].Fields = append(types[i].Fields, versionFieldInfo(config))
	}
	return types
}

func hasFieldNamed(fields []FieldInfo, name string) bool {
	for _, field := range fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

func versionFieldInfo(config ExtractionConfig) FieldInfo {
	targetName := "_version"
	if config.FieldNameMapper != nil {
		targetName = config.FieldNameMapper(targetName)
	}

	targetType := "number"
	switch config.Language {
	case "go":
		targetType = "int64"
	case "typescript":
		targetType = "number"
	}

	return FieldInfo{
		Name:             "_version",
		TargetName:       targetName,
		Type:             PrimitiveNumber,
		TargetType:       targetType,
		Required:         true,
		InternalMetadata: true,
		IsPrimitive:      true,
		Validations:      []ValidationRule{},
	}
}

// buildUnionMembers constructs the per-member metadata for a union, including
// each member's discriminator literal value when one is present. The returned
// slice is index-aligned with memberTypeNames.
func buildUnionMembers(memberTypeNames []string, discriminator string, schema *ir.Schema) []UnionMemberInfo {
	members := make([]UnionMemberInfo, 0, len(memberTypeNames))
	for _, typeName := range memberTypeNames {
		member := UnionMemberInfo{Name: typeName}
		if discriminator != "" && schema != nil {
			if typeDef, ok := schema.Types[typeName]; ok && typeDef != nil {
				for _, field := range typeDef.Fields {
					if field == nil || !field.InternalMetadata {
						continue
					}
					if field.Name != discriminator {
						continue
					}
					if field.Default != nil {
						member.DiscriminatorValue = *field.Default
					}
					break
				}
			}
		}
		members = append(members, member)
	}
	return members
}

// detectUnionDiscriminator returns the name of a field present on every union
// member that is marked @internalMetadata. When members do not share such a
// field, it returns the empty string.
func detectUnionDiscriminator(memberTypeNames []string, schema *ir.Schema) string {
	if len(memberTypeNames) == 0 || schema == nil {
		return ""
	}

	candidate := ""
	for i, typeName := range memberTypeNames {
		typeDef, ok := schema.Types[typeName]
		if !ok || typeDef == nil {
			return ""
		}

		memberCandidate := ""
		for _, field := range typeDef.Fields {
			if field == nil || !field.InternalMetadata {
				continue
			}
			memberCandidate = field.Name
			break
		}

		if memberCandidate == "" {
			return ""
		}
		if i == 0 {
			candidate = memberCandidate
			continue
		}
		if memberCandidate != candidate {
			return ""
		}
	}

	return candidate
}

// buildValidations creates validation rules from required/scalar/field
// constraints. Rule ordering is stable and deterministic: required,
// scalar-derived rules, then field-derived rules.
func buildValidations(scalar *ScalarInfo, field *ir.FieldDef, required bool) []ValidationRule {
	var rules []ValidationRule

	if required {
		rules = append(rules, ValidationRule{
			Validator: "required",
			Message:   "required field",
		})
	}

	if scalar != nil {
		if scalar.MaxLength > 0 {
			rules = append(rules, ValidationRule{
				Validator: "maxLength",
				Message:   fmt.Sprintf("must be at most %d characters", scalar.MaxLength),
				Value:     scalar.MaxLength,
			})
		}

		if scalar.MinLength > 0 {
			rules = append(rules, ValidationRule{
				Validator: "minLength",
				Message:   fmt.Sprintf("must be at least %d characters", scalar.MinLength),
				Value:     scalar.MinLength,
			})
		}

		if scalar.Pattern != "" {
			rules = append(rules, ValidationRule{
				Validator: "pattern",
				Message:   "invalid format",
				Value:     scalar.Pattern,
			})
		}

		if scalar.Minimum != nil {
			rules = append(rules, ValidationRule{
				Validator: "min",
				Message:   fmt.Sprintf("must be at least %d", *scalar.Minimum),
				Value:     *scalar.Minimum,
			})
		}

		if scalar.Maximum != nil {
			rules = append(rules, ValidationRule{
				Validator: "max",
				Message:   fmt.Sprintf("must be at most %d", *scalar.Maximum),
				Value:     *scalar.Maximum,
			})
		}
	}

	if field != nil {
		if field.ValidateMaxLength != nil {
			rules = append(rules, ValidationRule{
				Validator: "maxLength",
				Message:   fmt.Sprintf("must be at most %d characters", *field.ValidateMaxLength),
				Value:     *field.ValidateMaxLength,
			})
		}

		if field.ValidateMinLength != nil {
			rules = append(rules, ValidationRule{
				Validator: "minLength",
				Message:   fmt.Sprintf("must be at least %d characters", *field.ValidateMinLength),
				Value:     *field.ValidateMinLength,
			})
		}

		if field.ValidateListMax != nil {
			rules = append(rules, ValidationRule{
				Validator: "listMax",
				Message:   fmt.Sprintf("must contain at most %d items", *field.ValidateListMax),
				Value:     *field.ValidateListMax,
			})
		}

		if field.ValidateListMin != nil {
			rules = append(rules, ValidationRule{
				Validator: "listMin",
				Message:   fmt.Sprintf("must contain at least %d items", *field.ValidateListMin),
				Value:     *field.ValidateListMin,
			})
		}

		if field.ValidatePattern != "" {
			rules = append(rules, ValidationRule{
				Validator: "pattern",
				Message:   "invalid format",
				Value:     field.ValidatePattern,
			})
		}

		if field.ValidateMin != nil {
			rules = append(rules, ValidationRule{
				Validator: "min",
				Message:   fmt.Sprintf("must be at least %g", *field.ValidateMin),
				Value:     *field.ValidateMin,
			})
		}

		if field.ValidateMax != nil {
			rules = append(rules, ValidationRule{
				Validator: "max",
				Message:   fmt.Sprintf("must be at most %g", *field.ValidateMax),
				Value:     *field.ValidateMax,
			})
		}
	}

	return rules
}

// BuildScalarSQLMapping builds a map of scalar names to SQL types from IR
// scalars. Bare language primitives are included alongside declared scalars.
func BuildScalarSQLMapping(schema *ir.Schema) map[string]string {
	mapping := map[string]string{
		PrimitiveString:  "TEXT",
		PrimitiveNumber:  "DOUBLE PRECISION",
		PrimitiveBoolean: "BOOLEAN",
	}

	for _, scalarDef := range schema.Scalars {
		if sqlType, ok := scalarDef.TypeMappings["sql"]; ok {
			mapping[scalarDef.Name] = sqlType
			continue
		}
		if inferred := primitiveToSQLType(scalarDef.LanguagePrimitive); inferred != "" {
			mapping[scalarDef.Name] = inferred
		}
	}

	return mapping
}

func primitiveToSQLType(primitive ir.LanguagePrimitive) string {
	switch primitive {
	case ir.LanguageString:
		return "TEXT"
	case ir.LanguageNumber:
		return "DOUBLE PRECISION"
	case ir.LanguageBoolean:
		return "BOOLEAN"
	case ir.LanguageObject:
		return "JSONB"
	default:
		return ""
	}
}

// BuildScalarJSONSchemaMapping builds a map of scalar names to JSON Schema types.
func BuildScalarJSONSchemaMapping(schema *ir.Schema) map[string]string {
	mapping := map[string]string{
		PrimitiveString:  "string",
		PrimitiveNumber:  "number",
		PrimitiveBoolean: "boolean",
	}

	for _, scalarDef := range schema.Scalars {
		if jsonSchemaType, ok := scalarDef.TypeMappings["json_schema"]; ok && jsonSchemaType != "" {
			mapping[scalarDef.Name] = jsonSchemaType
			continue
		}
		if inferred := primitiveToJSONSchemaType(scalarDef.LanguagePrimitive); inferred != "" {
			mapping[scalarDef.Name] = inferred
		}
	}

	return mapping
}

func primitiveToJSONSchemaType(primitive ir.LanguagePrimitive) string {
	switch primitive {
	case ir.LanguageString:
		return "string"
	case ir.LanguageNumber:
		return "number"
	case ir.LanguageBoolean:
		return "boolean"
	case ir.LanguageObject:
		return "object"
	default:
		return ""
	}
}
