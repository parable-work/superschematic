package ir

import "encoding/json"

// OperationSet represents a named group of API operations with shared
// middleware. It replaces the v1 Queries/Mutations split: the set name (the
// authoring class name) carries intent, and each operation carries its HTTP
// method explicitly via [FieldDef.HTTPMethod].
//
// Operations are modeled as [FieldDef] entries since they share the same
// structural properties (name, type reference, arguments, decorators).
// API-specific fields on FieldDef (Auth, Permissions, HTTPMethod, Middleware)
// are used by operation fields.
type OperationSet struct {
	// Name is the operation set name (the authoring class name).
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the operation set declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Operations lists individual operations in declaration order.
	Operations []*FieldDef `json:"operations" yaml:"operations"`

	// Middleware holds set-level middleware defaults inherited by all operations.
	// Individual operations can override these via their own Middleware field.
	Middleware *MiddlewareConfig `json:"middleware,omitempty" yaml:"middleware,omitempty"`

	// Encrypted marks this entire operation set as requiring encrypted
	// payload transport.
	Encrypted bool `json:"encrypted,omitempty" yaml:"encrypted,omitempty"`

	// Extensions holds extension decorator data keyed by extension name; see
	// [Schema.Extensions].
	Extensions map[string]json.RawMessage `json:"extensions,omitempty" yaml:"extensions,omitempty"`
}

// MiddlewareConfig holds middleware configuration for rate limiting, body size
// limits, and request timeouts.
//
// When set on an [OperationSet], values are inherited by all operations within
// the set. When set on a [FieldDef], values override the OperationSet defaults
// for that operation. A nil pointer for any field means no override (inherit
// from parent or use server default).
type MiddlewareConfig struct {
	// RateLimit is the maximum number of requests per minute.
	RateLimit *int `json:"rateLimit,omitempty" yaml:"rateLimit,omitempty"`

	// BodyLimit is the maximum request body size in megabytes.
	BodyLimit *int `json:"bodyLimit,omitempty" yaml:"bodyLimit,omitempty"`

	// Timeout is the maximum request processing time in seconds.
	Timeout *int `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}
