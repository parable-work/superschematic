package ir

import "encoding/json"

// SchemaKind classifies the purpose of a schema definition.
// It determines which outputs and toolchain imports are valid for the schema.
type SchemaKind string

const (
	// SchemaKindDB represents a database schema that generates SQL DDL, ORM, and type code.
	SchemaKindDB SchemaKind = "DB"

	// SchemaKindAPI represents an API schema that generates REST server, SDK, and type code.
	SchemaKindAPI SchemaKind = "API"

	// SchemaKindGeneral represents a general-purpose schema that generates types only.
	SchemaKindGeneral SchemaKind = "General"
)

// String returns the string representation of a SchemaKind.
func (k SchemaKind) String() string {
	return string(k)
}

// Schema is the top-level format-agnostic intermediate representation of a
// schema documents. It contains all definitions extracted from a
// service's schema files, regardless of whether the source format was
// TypeScript, JSON, or YAML.
//
// Named definitions use maps keyed by definition name for O(1) lookups during
// type resolution and cross-referencing. Object and input types share the
// Types map; [TypeDef.Role] carries the classification that v1 split across
// the Types and Inputs maps.
type Schema struct {
	// Name is the schema identifier (e.g., "web-api", "web-db").
	Name string `json:"name" yaml:"name"`

	// Kind classifies the schema purpose and determines valid outputs.
	Kind SchemaKind `json:"kind" yaml:"kind"`

	// Description is an optional human-readable description of the schema.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the schema declaration.
	// Comments are IR metadata and round-trip through all formats.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Imports lists cross-schema dependencies for type resolution.
	// Imports are always named; wildcard imports do not exist in v2.
	Imports []Import `json:"imports,omitempty" yaml:"imports,omitempty"`

	// RootType is retained for services that still consume the legacy runtime
	// runtime schema shape during the IR flip.
	RootType string `json:"rootType,omitempty" yaml:"rootType,omitempty"`

	// Scalars maps scalar names to their definitions.
	Scalars map[string]*ScalarDef `json:"scalars,omitempty" yaml:"scalars,omitempty"`

	// Types maps type names to their definitions. All roles share this map;
	// generators key off [TypeDef.Role].
	Types map[string]*TypeDef `json:"types,omitempty" yaml:"types,omitempty"`

	// Inputs is retained for services that still consume the legacy runtime
	// runtime schema shape during the IR flip.
	Inputs map[string]*TypeDef `json:"inputs,omitempty" yaml:"inputs,omitempty"`

	// Enums maps enum type names to their definitions.
	Enums map[string]*EnumDef `json:"enums,omitempty" yaml:"enums,omitempty"`

	// Unions maps union type names to their definitions.
	Unions map[string]*UnionDef `json:"unions,omitempty" yaml:"unions,omitempty"`

	// CompositeDefaults maps target type names to validated canonical JSON
	// defaults. Each target type may declare at most one platform default.
	CompositeDefaults map[string]*CompositeDefaultDef `json:"compositeDefaults,omitempty" yaml:"compositeDefaults,omitempty"`

	// OperationSets lists named operation sets in declaration order.
	// This replaces the v1 Queries/Mutations split: the set name carries
	// intent, and each operation carries its HTTP method explicitly.
	OperationSets []*OperationSet `json:"operationSets,omitempty" yaml:"operationSets,omitempty"`

	// Documents holds the loaded sidecar documents keyed by document name
	// (the registered DocumentSpec.Name), each in canonical JSON (see
	// [CanonicalJSON]). The registering extension owns the codec; typed
	// access goes through [GetDocument] and [SetDocument].
	Documents map[string]json.RawMessage `json:"documents,omitempty" yaml:"documents,omitempty"`

	// Extensions holds schema-level extension data keyed by extension name.
	// Each value is one JSON object whose keys are that extension's
	// decorator names. Core decorators write the typed fields above; only
	// extensions write here. Typed access goes through [GetExtension] and
	// [SetExtension].
	Extensions map[string]json.RawMessage `json:"extensions,omitempty" yaml:"extensions,omitempty"`

	// AuthoringImports is the union of the sidecar documents' transitive
	// module graphs, as absolute file paths reported by the bun harness.
	// The build layer persists the files under the schemas root but outside
	// the service directory for cache invalidation. Not part of the decoded
	// document contract.
	AuthoringImports []string `json:"-" yaml:"-"`
}

// NewSchema creates a Schema with initialized maps to prevent nil-map panics.
func NewSchema(name string, kind SchemaKind) *Schema {
	return &Schema{
		Name:              name,
		Kind:              kind,
		Scalars:           make(map[string]*ScalarDef),
		Types:             make(map[string]*TypeDef),
		Inputs:            make(map[string]*TypeDef),
		Enums:             make(map[string]*EnumDef),
		Unions:            make(map[string]*UnionDef),
		CompositeDefaults: make(map[string]*CompositeDefaultDef),
	}
}

// CompositeDefaultDef is a complete platform-owned default for one schema
// object type. CanonicalJSON is validated during loading and has stable key
// ordering for generated language accessors and SQL literals.
type CompositeDefaultDef struct {
	Type          string `json:"type" yaml:"type"`
	CanonicalJSON string `json:"canonicalJson" yaml:"canonicalJson"`
	Owner         string `json:"owner,omitempty" yaml:"owner,omitempty"`
}

// Import represents a cross-schema dependency reference.
//
// Imports are always named: Types is a non-empty list of symbols and never
// contains the "*" wildcard. Readers reject wildcard imports at parse time;
// [Schema.Validate] enforces the invariant on the assembled IR.
type Import struct {
	// From is retained for legacy runtime schema payloads.
	From string `json:"from,omitempty" yaml:"from,omitempty"`

	// Package is the resolved package name (e.g., "@schemas/web-db").
	Package string `json:"package" yaml:"package"`

	// Types lists the imported symbol names. Never empty, never "*".
	Types []string `json:"types" yaml:"types"`
}
