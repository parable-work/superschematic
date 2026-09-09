package ir

// EnumDef is a format-agnostic representation of an enumeration type.
type EnumDef struct {
	// Name is the enum type name.
	Name string `json:"name" yaml:"name"`

	// Owner identifies the schema that owns this enum definition.
	// Empty means ownership was not set during extraction/merge.
	Owner string `json:"owner,omitempty" yaml:"owner,omitempty"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the enum declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Values lists all enum values in declaration order.
	Values []EnumValueDef `json:"values" yaml:"values"`
}

// EnumValueDef represents a single value within an enumeration.
type EnumValueDef struct {
	// Name is the enum value name as declared (e.g., "Pending", "Running").
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description of this value.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for this enum value.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// SerializedAs is the custom serialization value (e.g., "pending").
	// Empty string means the value serializes as its Name.
	SerializedAs string `json:"serializedAs,omitempty" yaml:"serializedAs,omitempty"`
}

// UnionDef is a format-agnostic representation of a union type.
type UnionDef struct {
	// Name is the union type name.
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the union declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Types lists the names of all member types.
	Types []string `json:"types" yaml:"types"`
}
