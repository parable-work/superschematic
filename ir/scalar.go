package ir

// LanguagePrimitive is the host-language primitive a semantic scalar bottoms
// out on. It is named LanguagePrimitive to be explicit that this is the
// host-language backing type.
type LanguagePrimitive string

const (
	// LanguageString backs string-valued scalars.
	LanguageString LanguagePrimitive = "string"

	// LanguageNumber backs numeric scalars.
	LanguageNumber LanguagePrimitive = "number"

	// LanguageBoolean backs boolean scalars.
	LanguageBoolean LanguagePrimitive = "boolean"

	// LanguageObject backs structured scalars.
	LanguageObject LanguagePrimitive = "object"
)

// String returns the string representation of a LanguagePrimitive.
func (p LanguagePrimitive) String() string {
	return string(p)
}

// ScalarDef is a format-agnostic representation of a semantic scalar type.
//
// ScalarDef captures ALL language type mappings in the TypeMappings map. The
// extraction layer selects the appropriate mapping for each target language
// during code generation.
type ScalarDef struct {
	// Name is the canonical scalar identity.
	//
	// Canonical names are namespaced using dot segments (for example,
	// "Identity.UUID"). Legacy flat names ("UUID") are still representable.
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description of the scalar.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the scalar declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// LanguagePrimitive is the host-language primitive this scalar bottoms out on.
	LanguagePrimitive LanguagePrimitive `json:"languagePrimitive" yaml:"languagePrimitive"`

	// Primitive is retained for legacy runtime schema payloads.
	Primitive string `json:"primitive,omitempty" yaml:"primitive,omitempty"`

	// TypeMappings maps target languages to their type representations.
	// Keys are language identifiers: "go", "typescript", "python", "rust", "sql", "json_schema".
	TypeMappings map[string]string `json:"typeMappings,omitempty" yaml:"typeMappings,omitempty"`

	// MaxLength is the maximum string length constraint (0 means no limit).
	MaxLength int `json:"maxLength,omitempty" yaml:"maxLength,omitempty"`

	// MinLength is the minimum string length constraint (0 means no minimum).
	MinLength int `json:"minLength,omitempty" yaml:"minLength,omitempty"`

	// Pattern is a regex pattern for value validation.
	Pattern string `json:"pattern,omitempty" yaml:"pattern,omitempty"`

	// Format is an optional format hint (e.g., "email", "uri", "date-time", "uuid").
	Format string `json:"format,omitempty" yaml:"format,omitempty"`

	// CaseInsensitive indicates that string comparisons should be
	// case-insensitive and values should be normalized (e.g., email addresses).
	CaseInsensitive bool `json:"caseInsensitive,omitempty" yaml:"caseInsensitive,omitempty"`

	// ReservedWords is a list of words that are reserved and cannot be used as values.
	ReservedWords []string `json:"reservedWords,omitempty" yaml:"reservedWords,omitempty"`

	// ReservedWordsCaseInsensitive controls case-insensitive reserved word matching.
	ReservedWordsCaseInsensitive bool `json:"reservedWordsCaseInsensitive,omitempty" yaml:"reservedWordsCaseInsensitive,omitempty"`

	// ReservedWordsMatchPartial controls partial-match reserved word matching.
	ReservedWordsMatchPartial bool `json:"reservedWordsMatchPartial,omitempty" yaml:"reservedWordsMatchPartial,omitempty"`

	// Minimum is the minimum numeric value constraint (nil means no minimum).
	Minimum *int64 `json:"minimum,omitempty" yaml:"minimum,omitempty"`

	// Maximum is the maximum numeric value constraint (nil means no maximum).
	Maximum *int64 `json:"maximum,omitempty" yaml:"maximum,omitempty"`

	// Example is an example value for documentation and testing.
	Example string `json:"example,omitempty" yaml:"example,omitempty"`

	// FileUpload holds upload constraints for file-type scalars (nil for non-file scalars).
	FileUpload *FileUploadConfig `json:"fileUpload,omitempty" yaml:"fileUpload,omitempty"`

	// ImageConstraints holds additional constraints for image-type scalars
	// (nil for non-image scalars).
	ImageConstraints *ImageConstraints `json:"imageConstraints,omitempty" yaml:"imageConstraints,omitempty"`

	// HasCustomNormalize indicates the scalar library has a custom normalize implementation.
	HasCustomNormalize bool `json:"hasCustomNormalize,omitempty" yaml:"hasCustomNormalize,omitempty"`

	// HasCustomValidate indicates the scalar library has a custom validate implementation.
	HasCustomValidate bool `json:"hasCustomValidate,omitempty" yaml:"hasCustomValidate,omitempty"`

	// HasCustomParse indicates the scalar library has a custom parse implementation.
	HasCustomParse bool `json:"hasCustomParse,omitempty" yaml:"hasCustomParse,omitempty"`
}

// JSONSchemaAnyType is the json_schema type mapping of a scalar whose value
// is any JSON value (Generic.JSON in the core catalog).
const JSONSchemaAnyType = "any"

// IsAnyJSON reports whether the scalar's value is any JSON value, which its
// json_schema type mapping declares as "any". The scalar catalog gives such a
// scalar the String primitive, but its value is not a string: an object, an
// array, a string, a number and a boolean are all values, and only JSON null
// stands for a missing one. Validators key the rule off this mapping, not the
// scalar's name or primitive.
func (s *ScalarDef) IsAnyJSON() bool {
	return s != nil && s.TypeMappings["json_schema"] == JSONSchemaAnyType
}

// JSONSchemaObjectType and JSONSchemaArrayType are the json_schema type
// mappings of a scalar whose value is a JSON object (Generic.StringMap in the
// core catalog) or a JSON array (Embedding.Vector).
const (
	JSONSchemaObjectType = "object"
	JSONSchemaArrayType  = "array"
)

// StructuredJSONType returns JSONSchemaObjectType or JSONSchemaArrayType when
// the scalar's value is a JSON object or a JSON array, and "" otherwise. The
// json_schema type mapping declares the shape. The scalar catalog gives such
// a scalar the String primitive, but every generated type holds the object or
// the array. A scalar that also declares a pattern or a length, which are
// rules on a string, contradicts itself (Geo.Location's row has a "lat,lon"
// pattern) and is not structured: it keeps the String primitive's checks
// until its metadata agrees. Validators key the rule off this, not the
// scalar's name or primitive.
func (s *ScalarDef) StructuredJSONType() string {
	if s == nil || s.Pattern != "" || s.MinLength > 0 || s.MaxLength > 0 {
		return ""
	}
	switch jsonType := s.TypeMappings["json_schema"]; jsonType {
	case JSONSchemaObjectType, JSONSchemaArrayType:
		return jsonType
	}
	return ""
}

// FileUploadConfig defines upload constraints for file-type scalars
// (File, Image, LogoImage, etc.).
type FileUploadConfig struct {
	// MaxSize is the maximum file size in bytes.
	MaxSize int `json:"maxSize,omitempty" yaml:"maxSize,omitempty"`

	// AllowedTypes lists permitted MIME types. An empty list means all types are allowed.
	AllowedTypes []string `json:"allowedTypes,omitempty" yaml:"allowedTypes,omitempty"`

	// Category is a file category hint for UI and processing
	// ("file", "image", "document", etc.).
	Category string `json:"category,omitempty" yaml:"category,omitempty"`
}

// ImageConstraints defines additional validation constraints for image-type scalars.
type ImageConstraints struct {
	// MaxWidth is the maximum image width in pixels (0 means no limit).
	MaxWidth int `json:"maxWidth,omitempty" yaml:"maxWidth,omitempty"`

	// MaxHeight is the maximum image height in pixels (0 means no limit).
	MaxHeight int `json:"maxHeight,omitempty" yaml:"maxHeight,omitempty"`

	// MinAspectRatio is the minimum width/height ratio (nil means no minimum).
	MinAspectRatio *float64 `json:"minAspectRatio,omitempty" yaml:"minAspectRatio,omitempty"`

	// MaxAspectRatio is the maximum width/height ratio (nil means no maximum).
	MaxAspectRatio *float64 `json:"maxAspectRatio,omitempty" yaml:"maxAspectRatio,omitempty"`

	// RequireTransparency requires the image to have an alpha channel (PNG only).
	RequireTransparency bool `json:"requireTransparency,omitempty" yaml:"requireTransparency,omitempty"`
}
