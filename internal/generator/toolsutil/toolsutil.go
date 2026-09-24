package toolsutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// JSONSchemaObject is the JSON Schema of one tool's arguments. It encodes
// with the vendor keys first, then the struct fields in declaration order:
// that encoding is what the input schema digest hashes.
type JSONSchemaObject struct {
	// Vendor holds the ToolKeys.Parameters entries, written first.
	Vendor               []apigen.ToolKeyValue         `json:"-"`
	AdditionalProperties bool                          `json:"additionalProperties"`
	Type                 string                        `json:"type"`
	Properties           map[string]JSONSchemaProperty `json:"properties"`
	Required             []string                      `json:"required,omitempty"`
}

// MarshalJSON writes the vendor keys ahead of the schema's own keys.
func (object JSONSchemaObject) MarshalJSON() ([]byte, error) {
	type plain JSONSchemaObject
	encoded, err := json.Marshal(plain(object))
	if err != nil {
		return nil, err
	}
	for i := len(object.Vendor) - 1; i >= 0; i-- {
		if encoded, err = prependKey(encoded, object.Vendor[i].Key, object.Vendor[i].Value); err != nil {
			return nil, err
		}
	}
	return encoded, nil
}

// JSONSchemaProperty represents a property in JSON Schema.
type JSONSchemaProperty struct {
	// ScalarType is generator-only semantic provenance and is never serialized.
	ScalarType string `json:"-"`
	Nullable   bool   `json:"-"`
	// CanonicalScalar is the scalar's schema name; it is written under
	// ScalarKey, first, when both are set.
	CanonicalScalar      string                          `json:"-"`
	ScalarKey            string                          `json:"-"`
	AdditionalProperties *JSONSchemaAdditionalProperties `json:"additionalProperties,omitempty"`
	Type                 string                          `json:"type"`
	Format               string                          `json:"format,omitempty"`
	Description          string                          `json:"description,omitempty"`
	Pattern              string                          `json:"pattern,omitempty"`
	Enum                 []string                        `json:"enum,omitempty"`
	MinLength            *int                            `json:"minLength,omitempty"`
	MaxLength            *int                            `json:"maxLength,omitempty"`
	Minimum              *int64                          `json:"minimum,omitempty"`
	Maximum              *int64                          `json:"maximum,omitempty"`
	MinItems             *int                            `json:"minItems,omitempty"`
	MaxItems             *int                            `json:"maxItems,omitempty"`
	Items                *JSONSchemaProperty             `json:"items,omitempty"`
	Properties           map[string]JSONSchemaProperty   `json:"properties,omitempty"`
	Required             []string                        `json:"required,omitempty"`
	OneOf                []JSONSchemaProperty            `json:"oneOf,omitempty"`
}

// JSONSchemaAdditionalProperties is either false for closed structural objects
// or a nested schema for typed string-keyed maps.
type JSONSchemaAdditionalProperties struct {
	Bool   *bool
	Schema *JSONSchemaProperty
}

// MarshalJSON writes the nested schema, else the boolean; an empty value is
// open.
func (additional JSONSchemaAdditionalProperties) MarshalJSON() ([]byte, error) {
	if additional.Schema != nil {
		return json.Marshal(additional.Schema)
	}
	if additional.Bool != nil {
		return json.Marshal(*additional.Bool)
	}
	return json.Marshal(true)
}

// MarshalJSON preserves body-field nullability independently from whether the
// containing object requires the field. Array items retain their own contract.
func (property JSONSchemaProperty) MarshalJSON() ([]byte, error) {
	encoded, err := property.marshalShape()
	if err != nil {
		return nil, err
	}
	if property.ScalarKey != "" && property.CanonicalScalar != "" {
		return prependKey(encoded, property.ScalarKey, property.CanonicalScalar)
	}
	return encoded, nil
}

func (property JSONSchemaProperty) marshalShape() ([]byte, error) {
	type plain JSONSchemaProperty
	if !property.Nullable || property.Type == "any" || property.Type == "null" {
		return json.Marshal(plain(property))
	}
	oneOf := property.OneOf
	if len(oneOf) > 0 {
		oneOf = append(append([]JSONSchemaProperty(nil), oneOf...), JSONSchemaProperty{Type: "null"})
	}
	var enum []any
	if len(property.Enum) > 0 {
		for _, value := range property.Enum {
			enum = append(enum, value)
		}
		enum = append(enum, nil)
	}
	return json.Marshal(struct {
		plain
		Type  []string             `json:"type"`
		Enum  []any                `json:"enum,omitempty"`
		OneOf []JSONSchemaProperty `json:"oneOf,omitempty"`
	}{plain(property), []string{property.Type, "null"}, enum, oneOf})
}

// prependKey writes "key": value as the first member of the encoded object.
func prependKey(object []byte, key string, value any) ([]byte, error) {
	encodedKey, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	encodedValue, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteByte('{')
	out.Write(encodedKey)
	out.WriteByte(':')
	out.Write(encodedValue)
	if !bytes.Equal(object, []byte("{}")) {
		out.WriteByte(',')
	}
	out.Write(object[1:])
	return out.Bytes(), nil
}

// withScalarKey sets key on the property and every property nested in it.
func (property JSONSchemaProperty) withScalarKey(key string) JSONSchemaProperty {
	property.ScalarKey = key
	if property.Items != nil {
		items := property.Items.withScalarKey(key)
		property.Items = &items
	}
	if property.AdditionalProperties != nil && property.AdditionalProperties.Schema != nil {
		values := property.AdditionalProperties.Schema.withScalarKey(key)
		property.AdditionalProperties = &JSONSchemaAdditionalProperties{Schema: &values}
	}
	if len(property.Properties) > 0 {
		nested := make(map[string]JSONSchemaProperty, len(property.Properties))
		for name, child := range property.Properties {
			nested[name] = child.withScalarKey(key)
		}
		property.Properties = nested
	}
	if len(property.OneOf) > 0 {
		members := make([]JSONSchemaProperty, len(property.OneOf))
		for i, member := range property.OneOf {
			members[i] = member.withScalarKey(key)
		}
		property.OneOf = members
	}
	return property
}

// JSONSchemaReturn represents the return type schema.
type JSONSchemaReturn struct {
	Type        string              `json:"type"`
	Description string              `json:"description,omitempty"`
	Items       *JSONSchemaProperty `json:"items,omitempty"`
}

// ToolPathParam represents a path parameter for tool invocation helpers.
type ToolPathParam struct {
	Name   string
	TSName string
	Type   string
}

// ToolScalarArg is one body argument of an operation without an input
// type, for tool invocation helpers. IsArray and IsArrayOfArrays carry its
// list shape (apigen.Param): the argument schema is T, T[] or T[][].
type ToolScalarArg struct {
	Name            string
	TSName          string
	Type            string
	Required        bool
	IsArray         bool
	IsArrayOfArrays bool
}

// ToolQueryArg is one query parameter as a tool argument: its type,
// collection shape, requiredness and Validate<> bounds. Tool arguments are
// one flat object, so query values appear in the same JSON Schema as path
// and body values.
type ToolQueryArg struct {
	Name              string
	TSName            string
	Type              string
	Required          bool
	IsArray           bool
	IsMap             bool
	ValidateMin       *float64
	ValidateMax       *float64
	ValidateMinLength *int
	ValidateMaxLength *int
	ValidateListMin   *int
	ValidateListMax   *int
	ValidatePattern   string
}

// ParamKeyFn chooses the JSON object key for a parameter.
// name is the schema field/argument name, tsName is a caller-provided language key.
type ParamKeyFn func(name, tsName string) string

// BuildInputTypeFieldsMap maps every object type a tool argument can reach
// to its fields: the API's types and its dependencies' (TypeFields), with
// each endpoint's input type over them.
func BuildInputTypeFieldsMap(apiOutput *apigen.APIOutput) map[string][]apigen.Param {
	result := make(map[string][]apigen.Param)
	if apiOutput == nil {
		return result
	}
	for name, fields := range apiOutput.TypeFields {
		result[name] = fields
	}
	for _, endpoint := range apiOutput.Endpoints {
		if endpoint.InputType != "" && len(endpoint.InputTypeFields) > 0 {
			result[endpoint.InputType] = endpoint.InputTypeFields
		}
	}
	return result
}

// BuildParametersSchema builds the JSON Schema for tool parameters: path
// parameters, query parameters, the input type's fields (nested objects
// expanded, unions as oneOf) or the scalar arguments, and the encryption
// key of an encrypted endpoint. keys supplies the vendor keys.
func BuildParametersSchema(
	pathParams []ToolPathParam,
	queryArgs []ToolQueryArg,
	hasInput bool,
	inputType string,
	inputTypeFields map[string][]apigen.Param,
	inputTypeUnions map[string]apigen.ToolUnionInfo,
	scalarArgs []ToolScalarArg,
	encrypted bool,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	paramKeyFn ParamKeyFn,
	keys apigen.ToolKeys,
) JSONSchemaObject {
	schema := JSONSchemaObject{
		Vendor:     append([]apigen.ToolKeyValue(nil), keys.Parameters...),
		Type:       "object",
		Properties: make(map[string]JSONSchemaProperty),
		Required:   []string{},
	}

	for _, param := range pathParams {
		prop := ScalarToJSONSchemaProperty(param.Type, scalars)
		prop.Description = fmt.Sprintf("%s parameter", param.Name)
		key := paramKeyFn(param.Name, param.TSName)
		schema.Properties[key] = prop
		schema.Required = append(schema.Required, key)
	}

	for _, arg := range queryArgs {
		prop := FieldToJSONSchemaProperty(apigen.Param{
			// Optional query values are omitted by the SDK; JSON null is a body contract.
			Name: arg.Name, Type: arg.Type, Required: true, IsArray: arg.IsArray, IsMap: arg.IsMap,
			ValidateMin: arg.ValidateMin, ValidateMax: arg.ValidateMax,
			ValidateMinLength: arg.ValidateMinLength, ValidateMaxLength: arg.ValidateMaxLength,
			ValidateListMin: arg.ValidateListMin, ValidateListMax: arg.ValidateListMax,
			ValidatePattern: arg.ValidatePattern,
		}, scalars, inputTypeFields, inputTypeUnions, nil)
		key := paramKeyFn(arg.Name, arg.TSName)
		schema.Properties[key] = prop
		if arg.Required {
			schema.Required = append(schema.Required, key)
		}
	}

	if hasInput && inputType != "" {
		if fields, ok := inputTypeFields[inputType]; ok {
			for _, field := range fields {
				prop := FieldToJSONSchemaProperty(field, scalars, inputTypeFields, inputTypeUnions, nil)
				key := paramKeyFn(field.Name, "")
				schema.Properties[key] = prop
				if field.Required {
					schema.Required = append(schema.Required, key)
				}
			}
		}
	}

	for _, arg := range scalarArgs {
		prop := FieldToJSONSchemaProperty(apigen.Param{
			Name: arg.Name, Type: arg.Type, Required: arg.Required,
			IsArray: arg.IsArray, IsArrayOfArrays: arg.IsArrayOfArrays,
		}, scalars, inputTypeFields, inputTypeUnions, nil)
		key := paramKeyFn(arg.Name, arg.TSName)
		schema.Properties[key] = prop
		if arg.Required {
			schema.Required = append(schema.Required, key)
		}
	}

	if encrypted {
		schema.Properties["publicEncryptionKey"] = JSONSchemaProperty{
			Type:                 "object",
			AdditionalProperties: closedObject(),
			Description:          "Public key details used to encrypt this request payload",
			Properties: map[string]JSONSchemaProperty{
				"publicKey": {
					Type:        "string",
					Description: "PEM-encoded RSA public key",
				},
				"algorithm": {
					Type:        "string",
					Description: "Encryption algorithm identifier from the server",
				},
				"keyId": {
					Type:        "string",
					Description: "Server key identifier for the public key",
				},
			},
			Required: []string{"publicKey", "algorithm", "keyId"},
		}
	}

	if keys.Scalar != "" {
		for name, prop := range schema.Properties {
			schema.Properties[name] = prop.withScalarKey(keys.Scalar)
		}
	}
	sort.Strings(schema.Required)
	return schema
}

// ScalarToJSONSchemaProperty converts a scalar type to a JSON Schema property.
func ScalarToJSONSchemaProperty(typeName string, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaProperty {
	if info, ok := scalars[typeName]; ok {
		return JSONSchemaProperty{
			ScalarType:      typeName,
			CanonicalScalar: info.CanonicalName,
			Type:            info.Type,
			Format:          info.Format,
			Description:     info.Description,
			Pattern:         info.Pattern,
			MinLength:       info.MinLength,
			MaxLength:       info.MaxLength,
			Minimum:         info.Minimum,
			Maximum:         info.Maximum,
		}
	}

	return JSONSchemaProperty{
		ScalarType:  typeName,
		Type:        "string",
		Description: fmt.Sprintf("A %s value", typeName),
	}
}

// FieldToJSONSchemaProperty converts a field to a JSON Schema property. An
// object type expands to its fields (closed with additionalProperties
// false), a union to oneOf with each member's discriminator pinned, an
// array to items (an array of arrays to items of items), a map to typed
// additionalProperties. The field's value constraints go on the innermost
// items and its list bounds on the outer array. A type already on the
// expansion stack stays a plain reference, so recursive types end. A field
// that is not required is nullable; the inner lists of an array of arrays
// never are.
func FieldToJSONSchemaProperty(
	field apigen.Param,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	typeFields map[string][]apigen.Param,
	typeUnions map[string]apigen.ToolUnionInfo,
	stack map[string]bool,
) JSONSchemaProperty {
	value := ScalarToJSONSchemaProperty(field.Type, scalars)
	if _, scalar := scalars[field.Type]; !scalar {
		if stack == nil {
			stack = make(map[string]bool)
		}
		if !stack[field.Type] {
			nextStack := make(map[string]bool, len(stack)+1)
			for name, active := range stack {
				nextStack[name] = active
			}
			nextStack[field.Type] = true
			if union, ok := typeUnions[field.Type]; ok && len(union.Members) > 0 {
				oneOf := make([]JSONSchemaProperty, 0, len(union.Members))
				for _, member := range union.Members {
					memberValue := FieldToJSONSchemaProperty(
						apigen.Param{Type: member.Name, Required: true}, scalars, typeFields, typeUnions, nextStack,
					)
					if union.Discriminator != "" && member.DiscriminatorValue != "" && memberValue.Properties != nil {
						discriminator := memberValue.Properties[union.Discriminator]
						discriminator.Enum = []string{member.DiscriminatorValue}
						memberValue.Properties[union.Discriminator] = discriminator
					}
					oneOf = append(oneOf, memberValue)
				}
				description := union.Description
				if description == "" {
					description = fmt.Sprintf("%s object union", field.Type)
				}
				value = JSONSchemaProperty{Type: "object", Description: description, OneOf: oneOf}
			} else if fields, ok := typeFields[field.Type]; ok {
				properties := make(map[string]JSONSchemaProperty, len(fields))
				required := make([]string, 0, len(fields))
				for _, nested := range fields {
					properties[nested.Name] = FieldToJSONSchemaProperty(nested, scalars, typeFields, typeUnions, nextStack)
					if nested.Required {
						required = append(required, nested.Name)
					}
				}
				sort.Strings(required)
				value = JSONSchemaProperty{
					Type: "object", Description: fmt.Sprintf("%s object", field.Type),
					AdditionalProperties: closedObject(),
					Properties:           properties, Required: required,
				}
			}
		}
	}
	if depth := field.ArrayDepth(); depth > 0 {
		itemField := field
		itemField.ValidateListMin = nil
		itemField.ValidateListMax = nil
		itemField.IsArray = false
		itemField.IsArrayOfArrays = false
		applyFieldValidation(&value, itemField)
		value = wrapArrayProperty(value, depth, field.Type)
		value.MinItems = field.ValidateListMin
		value.MaxItems = field.ValidateListMax
	} else {
		applyFieldValidation(&value, field)
	}
	if field.IsMap {
		value = JSONSchemaProperty{
			Type:                 "object",
			Description:          fmt.Sprintf("Map of %s values", field.Type),
			AdditionalProperties: mapValues(value),
		}
	}
	value.Nullable = !field.Required
	return value
}

// wrapArrayProperty wraps an item property in depth array levels. The
// level around the items is an "Array of <type> values" and the one around
// that an "Array of arrays of <type> values".
func wrapArrayProperty(item JSONSchemaProperty, depth int, typeName string) JSONSchemaProperty {
	level := 0
	return codegen.WrapArray(item, depth, func(items JSONSchemaProperty) JSONSchemaProperty {
		level++
		return JSONSchemaProperty{
			Type:        "array",
			Description: arrayDescription(level, typeName, " values"),
			Items:       &items,
		}
	})
}

// arrayDescription names the list level of an array schema: "Array of T"
// around the items, "Array of arrays of T" one level out.
func arrayDescription(level int, typeName, suffix string) string {
	if level > 1 {
		return fmt.Sprintf("Array of arrays of %s%s", typeName, suffix)
	}
	return fmt.Sprintf("Array of %s%s", typeName, suffix)
}

func closedObject() *JSONSchemaAdditionalProperties {
	value := false
	return &JSONSchemaAdditionalProperties{Bool: &value}
}

func mapValues(value JSONSchemaProperty) *JSONSchemaAdditionalProperties {
	return &JSONSchemaAdditionalProperties{Schema: &value}
}

func applyFieldValidation(property *JSONSchemaProperty, field apigen.Param) {
	if field.ValidateMinLength != nil {
		property.MinLength = field.ValidateMinLength
	}
	if field.ValidateMaxLength != nil {
		property.MaxLength = field.ValidateMaxLength
	}
	if field.ValidateListMin != nil {
		property.MinItems = field.ValidateListMin
	}
	if field.ValidateListMax != nil {
		property.MaxItems = field.ValidateListMax
	}
	if field.ValidatePattern != "" {
		property.Pattern = field.ValidatePattern
	}
	if field.ValidateMin != nil {
		value := int64(*field.ValidateMin)
		property.Minimum = &value
	}
	if field.ValidateMax != nil {
		value := int64(*field.ValidateMax)
		property.Maximum = &value
	}
}

// BuildReturnSchema builds the JSON Schema for a T or T[] return type. An
// endpoint's T[][] response needs BuildReturnSchemaAtDepth.
func BuildReturnSchema(typeName string, isArray bool, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaReturn {
	depth := 0
	if isArray {
		depth = 1
	}
	return BuildReturnSchemaAtDepth(typeName, depth, scalars)
}

// BuildReturnSchemaAtDepth builds the JSON Schema for a return type wrapped
// in arrayDepth list levels: 0 for T, 1 for T[], 2 for T[][]
// (apigen.EndpointInfo.OutputArrayDepth).
func BuildReturnSchemaAtDepth(typeName string, arrayDepth int, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaReturn {
	baseType := GetReturnTypeSchema(typeName, scalars)
	if arrayDepth < 1 {
		return JSONSchemaReturn{
			Type:        baseType.Type,
			Description: baseType.Description,
		}
	}
	level := 0
	items := codegen.WrapArray(JSONSchemaProperty{
		Type:        baseType.Type,
		Description: baseType.Description,
	}, arrayDepth-1, func(inner JSONSchemaProperty) JSONSchemaProperty {
		level++
		return JSONSchemaProperty{
			Type:        "array",
			Description: arrayDescription(level, typeName, ""),
			Items:       &inner,
		}
	})
	return JSONSchemaReturn{
		Type:        "array",
		Description: arrayDescription(arrayDepth, typeName, ""),
		Items:       &items,
	}
}

// GetReturnTypeSchema gets the JSON Schema for an endpoint return type.
func GetReturnTypeSchema(typeName string, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaProperty {
	if info, ok := scalars[typeName]; ok {
		return JSONSchemaProperty{
			Type:        info.Type,
			Description: info.Description,
		}
	}
	return JSONSchemaProperty{
		Type:        "object",
		Description: fmt.Sprintf("%s object", typeName),
	}
}

// JSONSchemaPropertyLiteral renders a complete nested property as JSON with
// sorted keys. The internal `any` type becomes the JSON Schema union every
// value satisfies.
func JSONSchemaPropertyLiteral(property JSONSchemaProperty) string {
	encoded, err := json.Marshal(property)
	if err != nil {
		return `{"type":["object","array","string","number","boolean","null"]}`
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		return string(encoded)
	}
	var normalize func(any) any
	normalize = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			if typed["type"] == "any" {
				typed["type"] = []string{"object", "array", "string", "number", "boolean", "null"}
			}
			for key, child := range typed {
				typed[key] = normalize(child)
			}
		case []any:
			for index, child := range typed {
				typed[index] = normalize(child)
			}
		}
		return current
	}
	normalized, err := json.Marshal(normalize(value))
	if err != nil {
		return string(encoded)
	}
	return string(normalized)
}

// JSONLiteral renders a value (guidance, _meta, a vendor key's value) for a
// generated JSON document or TypeScript object literal. A value that does
// not encode renders as an empty object; the loader accepts only literal
// values, so that does not happen for schema input.
func JSONLiteral(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// ValidateReplayContract checks an operation's @docs replay pointers against
// the tool arguments they address: each pointer resolves, every segment is a
// required argument and every segment but the last an object with
// properties, an idempotency key is a string and an expected revision a
// number or an integer.
func ValidateReplayContract(
	schema JSONSchemaObject,
	mode string,
	idempotencyKeyPointers []string,
	expectedRevisionPointers []string,
) error {
	if mode == "" {
		if len(idempotencyKeyPointers) > 0 || len(expectedRevisionPointers) > 0 {
			return fmt.Errorf("replay pointers require a replay mode")
		}
		return nil
	}
	if mode == "read_only" {
		if len(idempotencyKeyPointers) > 0 || len(expectedRevisionPointers) > 0 {
			return fmt.Errorf("read_only operations cannot declare replay pointers")
		}
		return nil
	}
	for _, pointer := range idempotencyKeyPointers {
		property, err := resolveRequiredProperty(schema, pointer)
		if err != nil {
			return fmt.Errorf("idempotency pointer %q: %w", pointer, err)
		}
		if property.Type != "string" {
			return fmt.Errorf("idempotency pointer %q resolves to a %s argument, want a string", pointer, property.Type)
		}
	}
	for _, pointer := range expectedRevisionPointers {
		property, err := resolveRequiredProperty(schema, pointer)
		if err != nil {
			return fmt.Errorf("expected revision pointer %q: %w", pointer, err)
		}
		if property.Type != "integer" && property.Type != "number" {
			return fmt.Errorf("expected revision pointer %q resolves to a %s argument, want a number", pointer, property.Type)
		}
	}
	return nil
}

func resolveRequiredProperty(schema JSONSchemaObject, pointer string) (JSONSchemaProperty, error) {
	segments, err := decodeJSONPointer(pointer)
	if err != nil {
		return JSONSchemaProperty{}, err
	}
	properties := schema.Properties
	required := schema.Required
	var property JSONSchemaProperty
	var ok bool
	for index, segment := range segments {
		property, ok = properties[segment]
		if !ok {
			return JSONSchemaProperty{}, fmt.Errorf("does not resolve at segment %q", segment)
		}
		if !containsRequired(required, segment) {
			return JSONSchemaProperty{}, fmt.Errorf("segment %q is optional", segment)
		}
		if index == len(segments)-1 {
			return property, nil
		}
		if property.Type != "object" || len(property.Properties) == 0 {
			return JSONSchemaProperty{}, fmt.Errorf("segment %q is not a concrete object", segment)
		}
		properties = property.Properties
		required = property.Required
	}
	return JSONSchemaProperty{}, fmt.Errorf("pointer is empty")
}

func decodeJSONPointer(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, fmt.Errorf("must be a non-empty RFC 6901 pointer")
	}
	raw := strings.Split(pointer[1:], "/")
	segments := make([]string, len(raw))
	for index, segment := range raw {
		for position := 0; position < len(segment); position++ {
			if segment[position] == '~' && (position+1 >= len(segment) || (segment[position+1] != '0' && segment[position+1] != '1')) {
				return nil, fmt.Errorf("contains an invalid RFC 6901 escape")
			}
		}
		segment = strings.ReplaceAll(segment, "~1", "/")
		segment = strings.ReplaceAll(segment, "~0", "~")
		segments[index] = segment
	}
	return segments, nil
}

func containsRequired(required []string, name string) bool {
	for _, candidate := range required {
		if candidate == name {
			return true
		}
	}
	return false
}

// EscapeJSON escapes a string for safe JSON embedding in templates.
func EscapeJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}
