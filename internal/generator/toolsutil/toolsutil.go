package toolsutil

import (
	"fmt"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// JSONSchemaObject represents a JSON Schema object type.
type JSONSchemaObject struct {
	Type       string                        `json:"type"`
	Properties map[string]JSONSchemaProperty `json:"properties"`
	Required   []string                      `json:"required,omitempty"`
}

// JSONSchemaProperty represents a property in JSON Schema.
type JSONSchemaProperty struct {
	Type        string                        `json:"type"`
	Format      string                        `json:"format,omitempty"`
	Description string                        `json:"description,omitempty"`
	Pattern     string                        `json:"pattern,omitempty"`
	Enum        []string                      `json:"enum,omitempty"`
	MinLength   *int                          `json:"minLength,omitempty"`
	MaxLength   *int                          `json:"maxLength,omitempty"`
	Minimum     *int64                        `json:"minimum,omitempty"`
	Maximum     *int64                        `json:"maximum,omitempty"`
	Items       *JSONSchemaProperty           `json:"items,omitempty"`
	Properties  map[string]JSONSchemaProperty `json:"properties,omitempty"`
	Required    []string                      `json:"required,omitempty"`
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

// ToolScalarArg represents a scalar argument for tool invocation helpers.
type ToolScalarArg struct {
	Name     string
	TSName   string
	Type     string
	Required bool
}

// ParamKeyFn chooses the JSON object key for a parameter.
// name is the GraphQL field/argument name, tsName is a caller-provided language key.
type ParamKeyFn func(name, tsName string) string

// BuildInputTypeFieldsMap creates a map of input type names to their fields.
func BuildInputTypeFieldsMap(apiOutput *apigen.APIOutput) map[string][]apigen.Param {
	result := make(map[string][]apigen.Param)
	if apiOutput == nil {
		return result
	}

	for _, endpoint := range apiOutput.Endpoints {
		if endpoint.InputType != "" && len(endpoint.InputTypeFields) > 0 {
			result[endpoint.InputType] = endpoint.InputTypeFields
		}
	}

	return result
}

// BuildParametersSchema builds the JSON Schema for tool parameters.
func BuildParametersSchema(
	pathParams []ToolPathParam,
	hasInput bool,
	inputType string,
	inputTypeFields map[string][]apigen.Param,
	scalarArgs []ToolScalarArg,
	encrypted bool,
	scalars map[string]apigen.ScalarJSONSchemaInfo,
	paramKeyFn ParamKeyFn,
) JSONSchemaObject {
	schema := JSONSchemaObject{
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

	if hasInput && inputType != "" {
		if fields, ok := inputTypeFields[inputType]; ok {
			for _, field := range fields {
				prop := FieldToJSONSchemaProperty(field, scalars)
				key := paramKeyFn(field.Name, "")
				schema.Properties[key] = prop
				if field.Required {
					schema.Required = append(schema.Required, key)
				}
			}
		}
	}

	for _, arg := range scalarArgs {
		prop := ScalarToJSONSchemaProperty(arg.Type, scalars)
		key := paramKeyFn(arg.Name, arg.TSName)
		schema.Properties[key] = prop
		if arg.Required {
			schema.Required = append(schema.Required, key)
		}
	}

	if encrypted {
		schema.Properties["publicEncryptionKey"] = JSONSchemaProperty{
			Type:        "object",
			Description: "Public key details used to encrypt this request payload",
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

	sort.Strings(schema.Required)
	return schema
}

// ScalarToJSONSchemaProperty converts a scalar type to a JSON Schema property.
func ScalarToJSONSchemaProperty(typeName string, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaProperty {
	if info, ok := scalars[typeName]; ok {
		return JSONSchemaProperty{
			Type:        info.Type,
			Format:      info.Format,
			Description: info.Description,
			Pattern:     info.Pattern,
			MinLength:   info.MinLength,
			MaxLength:   info.MaxLength,
			Minimum:     info.Minimum,
			Maximum:     info.Maximum,
		}
	}

	return JSONSchemaProperty{
		Type:        "string",
		Description: fmt.Sprintf("A %s value", typeName),
	}
}

// FieldToJSONSchemaProperty converts an input field to a JSON Schema property.
func FieldToJSONSchemaProperty(field apigen.Param, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaProperty {
	scalarProp := ScalarToJSONSchemaProperty(field.Type, scalars)
	if field.IsArray {
		return JSONSchemaProperty{
			Type:        "array",
			Description: fmt.Sprintf("Array of %s values", field.Type),
			Items:       &scalarProp,
		}
	}
	return scalarProp
}

// BuildReturnSchema builds the JSON Schema for the return type.
func BuildReturnSchema(typeName string, isArray bool, scalars map[string]apigen.ScalarJSONSchemaInfo) JSONSchemaReturn {
	baseType := GetReturnTypeSchema(typeName, scalars)
	if isArray {
		return JSONSchemaReturn{
			Type:        "array",
			Description: fmt.Sprintf("Array of %s", typeName),
			Items: &JSONSchemaProperty{
				Type:        baseType.Type,
				Description: baseType.Description,
			},
		}
	}
	return JSONSchemaReturn{
		Type:        baseType.Type,
		Description: baseType.Description,
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

// EscapeJSON escapes a string for safe JSON embedding in templates.
func EscapeJSON(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return s
}
