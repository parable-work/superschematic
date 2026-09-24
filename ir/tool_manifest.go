package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// The types in this file are the Go wire contract of two tool documents the
// TypeScript and Go SDK generators write next to every SDK:
//
//   - <sdk>/tools/schema.json      -> ToolManifest
//   - <sdk>/tools/mcp-binding.json -> ToolBindingManifest
//
// The generators render both through text/templates. A consumer, such as an
// MCP server that builds its tool registry from them, decodes them into
// these types instead of keeping its own copy of the field list. A test in
// the SDK generator decodes every rendered document with unknown fields
// refused and checks that re-encoding gives the same JSON, so a key the
// templates emit that is missing here fails there. Add a field here first,
// then to the templates.
//
// The types spell the core's vendor keys (DefaultToolScalarKey in the api
// generator). A distribution that renames them with a tool hook decodes its
// documents with types of its own.
//
// Field declaration order matters for ToolParameterSchema and
// ToolSchemaProperty: encoding/json writes struct fields in that order, so
// re-encoding a decoded argument schema gives the key order the generator
// used when it computed the tool's inputSchemaDigest.

// ToolManifest is tools/schema.json: the document header plus one
// ToolManifestTool per operation.
type ToolManifest struct {
	Schema      string             `json:"$schema,omitempty"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Tools       []ToolManifestTool `json:"tools"`
}

// ToolManifestTool is one operation of a ToolManifest. mcp is absent for an
// operation without @mcp; replay is JSON null when the operation declares
// no replay mode.
type ToolManifestTool struct {
	Name                string                `json:"name"`
	OperationID         string                `json:"operationId"`
	Title               string                `json:"title"`
	MCP                 *OperationMCP         `json:"mcp,omitempty"`
	Capability          string                `json:"capability"`
	Lifecycle           string                `json:"lifecycle"`
	Visibility          string                `json:"visibility"`
	Audience            string                `json:"audience"`
	Guidance            ToolOperationGuidance `json:"guidance"`
	Replay              *ToolReplayContract   `json:"replay"`
	Description         string                `json:"description"`
	Namespace           string                `json:"namespace"`
	MethodName          string                `json:"methodName"`
	HTTPMethod          string                `json:"httpMethod"`
	HTTPPath            string                `json:"httpPath"`
	RequiresAuth        bool                  `json:"requiresAuth"`
	RequiredPermissions []string              `json:"requiredPermissions"`
	IsScoped            bool                  `json:"isScoped"`
	BindingStatus       string                `json:"bindingStatus"`
	InputSchemaDigest   string                `json:"inputSchemaDigest"`
	Parameters          ToolParameterSchema   `json:"parameters"`
	Returns             ToolReturnSchema      `json:"returns"`
}

// ToolReplayContract is an operation's replay contract, from its @docs
// replay keys. A consumer that retries a call reads it here and never
// infers replay safety from names.
type ToolReplayContract struct {
	Mode                     string   `json:"mode"`
	IdempotencyKeyPointers   []string `json:"idempotencyKeyPointers"`
	ExpectedRevisionPointers []string `json:"expectedRevisionPointers"`
}

// ToolOperationGuidance is an operation's @docs guidance, as structured
// facts a model or a client can read without parsing prose.
type ToolOperationGuidance struct {
	UseWhen      string                       `json:"useWhen"`
	DoNotUseWhen string                       `json:"doNotUseWhen"`
	Success      string                       `json:"success"`
	Errors       []ToolOperationGuidanceError `json:"errors"`
}

// ToolOperationGuidanceError is one expected error of an operation and the
// usual correction.
type ToolOperationGuidanceError struct {
	Code             string `json:"code"`
	Description      string `json:"description"`
	CommonCorrection string `json:"commonCorrection"`
}

// ToolSchemaAdditionalProperties carries a JSON Schema additionalProperties
// keyword in both forms the generators write: a boolean for a closed or open
// object and a nested schema for a typed map.
type ToolSchemaAdditionalProperties struct {
	Bool   *bool
	Schema *ToolSchemaProperty
}

// UnmarshalJSON accepts either the boolean or the nested-schema form.
func (value *ToolSchemaAdditionalProperties) UnmarshalJSON(data []byte) error {
	var boolean bool
	if err := json.Unmarshal(data, &boolean); err == nil {
		value.Bool = &boolean
		value.Schema = nil
		return nil
	}

	var schema ToolSchemaProperty
	if err := json.Unmarshal(data, &schema); err != nil {
		return fmt.Errorf("additionalProperties must be a boolean or JSON Schema object: %w", err)
	}
	value.Bool = nil
	value.Schema = &schema
	return nil
}

// MarshalJSON re-encodes whichever form was decoded; an empty value is open.
func (value ToolSchemaAdditionalProperties) MarshalJSON() ([]byte, error) {
	if value.Schema != nil {
		return json.Marshal(value.Schema)
	}
	if value.Bool != nil {
		return json.Marshal(*value.Bool)
	}
	return json.Marshal(true)
}

// IsFalse reports the closed-object form.
func (value *ToolSchemaAdditionalProperties) IsFalse() bool {
	return value != nil && value.Bool != nil && !*value.Bool
}

// IsTrue reports the explicit open-object form.
func (value *ToolSchemaAdditionalProperties) IsTrue() bool {
	return value != nil && value.Bool != nil && *value.Bool
}

// ToolParameterSchema is the JSON Schema of one tool's arguments.
// Declaration order matters (see the file comment).
type ToolParameterSchema struct {
	AdditionalProperties *ToolSchemaAdditionalProperties `json:"additionalProperties,omitempty"`
	Type                 string                          `json:"type"`
	Properties           map[string]ToolSchemaProperty   `json:"properties"`
	Required             []string                        `json:"required,omitempty"`
}

// ToolSchemaType carries a JSON Schema type keyword in either legal spelling:
// one string or an array of strings. A single type stays a string on
// re-encoding; a union stays an array.
type ToolSchemaType []string

// UnmarshalJSON accepts either JSON Schema representation of type.
func (value *ToolSchemaType) UnmarshalJSON(data []byte) error {
	var single string
	if err := json.Unmarshal(data, &single); err == nil {
		*value = ToolSchemaType{single}
		return nil
	}

	var union []string
	if err := json.Unmarshal(data, &union); err != nil {
		return fmt.Errorf("JSON Schema type must be a string or array of strings: %w", err)
	}
	if len(union) == 0 {
		return errors.New("JSON Schema type array must not be empty")
	}
	*value = append((*value)[:0], union...)
	return nil
}

// MarshalJSON keeps single types as strings and unions as arrays.
func (value ToolSchemaType) MarshalJSON() ([]byte, error) {
	if len(value) <= 1 {
		if len(value) == 0 {
			return json.Marshal("")
		}
		return json.Marshal(value[0])
	}
	return json.Marshal([]string(value))
}

// Is reports whether this is exactly one type, expected.
func (value ToolSchemaType) Is(expected string) bool {
	return len(value) == 1 && value[0] == expected
}

// Missing reports whether the type keyword is absent or names a blank type,
// which a generated document never does.
func (value ToolSchemaType) Missing() bool {
	if len(value) == 0 {
		return true
	}
	for _, name := range value {
		if strings.TrimSpace(name) == "" {
			return true
		}
	}
	return false
}

// ToolSchemaProperty is one property of a ToolParameterSchema.
// x-superschematic-scalar names the scalar whose parser applies to the
// value. Declaration order matters (see the file comment).
type ToolSchemaProperty struct {
	CanonicalScalar      string                          `json:"x-superschematic-scalar,omitempty"`
	AdditionalProperties *ToolSchemaAdditionalProperties `json:"additionalProperties,omitempty"`
	Type                 ToolSchemaType                  `json:"type"`
	Format               string                          `json:"format,omitempty"`
	Description          string                          `json:"description,omitempty"`
	Pattern              string                          `json:"pattern,omitempty"`
	Enum                 []any                           `json:"enum,omitempty"`
	MinLength            *int64                          `json:"minLength,omitempty"`
	MaxLength            *int64                          `json:"maxLength,omitempty"`
	Minimum              *float64                        `json:"minimum,omitempty"`
	Maximum              *float64                        `json:"maximum,omitempty"`
	MinItems             *int64                          `json:"minItems,omitempty"`
	MaxItems             *int64                          `json:"maxItems,omitempty"`
	Items                *ToolSchemaProperty             `json:"items,omitempty"`
	Properties           map[string]ToolSchemaProperty   `json:"properties,omitempty"`
	Required             []string                        `json:"required,omitempty"`
	OneOf                []ToolSchemaProperty            `json:"oneOf,omitempty"`
}

// ToolReturnSchema is the return shape of one tool.
type ToolReturnSchema struct {
	Type        ToolSchemaType   `json:"type"`
	Description string           `json:"description,omitempty"`
	Items       *ToolReturnItems `json:"items,omitempty"`
}

// ToolReturnItems is the item schema of an array-valued ToolReturnSchema.
type ToolReturnItems struct {
	Type        ToolSchemaType `json:"type"`
	Description string         `json:"description,omitempty"`
}

// ToolBindingManifest is tools/mcp-binding.json: the document header plus
// one ToolBinding per operation of the sibling ToolManifest.
type ToolBindingManifest struct {
	Schema        string        `json:"$schema,omitempty"`
	SchemaVersion string        `json:"schemaVersion,omitempty"`
	GeneratedAt   string        `json:"generatedAt,omitempty"`
	APIID         string        `json:"apiId"`
	SDKClassName  string        `json:"sdkClassName,omitempty"`
	Tools         []ToolBinding `json:"tools"`
}

// ToolBinding is how to call one tool through the generated SDK: which
// namespace method, how each tool argument reaches it, and the positional
// argument order the method expects. A scoped namespace takes ScopeParam in
// its factory, not in the method.
type ToolBinding struct {
	ToolName   string                      `json:"toolName"`
	APIID      string                      `json:"apiId"`
	Namespace  string                      `json:"namespace"`
	MethodName string                      `json:"methodName"`
	IsScoped   bool                        `json:"isScoped"`
	ScopeParam string                      `json:"scopeParam,omitempty"`
	Arguments  []ToolArgumentBinding       `json:"arguments"`
	MethodArgs []ToolMethodArgumentBinding `json:"methodArgs"`
}

// ToolArgumentBinding maps one tool argument to its target (path.<name>,
// query.<name>, input.<name> or options.<name>).
type ToolArgumentBinding struct {
	ToolParameter string `json:"toolParameter"`
	Required      bool   `json:"required"`
	Kind          string `json:"kind"`
	Target        string `json:"target"`
}

// ToolMethodArgumentBinding is one positional SDK method argument and the
// tool arguments that fill it.
type ToolMethodArgumentBinding struct {
	Position int      `json:"position"`
	Kind     string   `json:"kind"`
	Target   string   `json:"target"`
	Sources  []string `json:"sources,omitempty"`
}
