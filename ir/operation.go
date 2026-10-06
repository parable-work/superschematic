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

	// ServiceCallers is the set-level service clause (@requireService or
	// @allowService on the class), inherited by every operation that declares
	// none and is not @publicRoute (see [EffectiveServiceCallers]).
	ServiceCallers *ServiceCallers `json:"serviceCallers,omitempty" yaml:"serviceCallers,omitempty"`

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

// ServiceCallersMode is how an operation admits a calling service (D37).
type ServiceCallersMode string

const (
	// ServiceCallersRequire (@requireService) admits only a listed service.
	// With a user clause, the service must forward an end user who meets
	// it; without one, no end user is looked at.
	ServiceCallersRequire ServiceCallersMode = "require"

	// ServiceCallersAllow (@allowService) admits an end user who meets the
	// operation's user clause, or a listed service with no end user, which
	// then stands in for the user. It needs a user clause.
	ServiceCallersAllow ServiceCallersMode = "allow"
)

// ServiceCallers is the service clause of an operation or an operation set
// (@requireService or @allowService): which deployables may call it, beside
// the end-user clause (Auth, Permissions, RequireOwnership). Section 9.3 of
// docs/stack-model.md has the rules.
//
// When set on an [OperationSet], every operation of the set takes it unless
// the operation declares its own, which replaces it, or is @publicRoute.
// [EffectiveServiceCallers] applies that rule.
type ServiceCallers struct {
	// Mode is ServiceCallersRequire or ServiceCallersAllow.
	Mode ServiceCallersMode `json:"mode" yaml:"mode"`

	// From lists the API services whose servers may call, by service name
	// (the handles in @requireService({ from })). Empty means every server
	// with a calls edge to the API in the stack: from narrows the edges and
	// never widens them.
	From []string `json:"from,omitempty" yaml:"from,omitempty"`
}

// EffectiveServiceCallers returns the service clause that applies to op in
// set: op's own, else the set's unless op is @publicRoute, which opens its
// route even in a set with a service clause. Nil means no service clause.
func EffectiveServiceCallers(set *OperationSet, op *FieldDef) *ServiceCallers {
	if op == nil {
		return nil
	}
	if op.ServiceCallers != nil {
		return op.ServiceCallers
	}
	if set == nil || op.Public {
		return nil
	}
	return set.ServiceCallers
}

// HasUserClause reports whether the operation requires an end user: @auth
// (an Authenticated set reaches its operations as Auth), @requirePermission
// or @requireOwnership.
func (f *FieldDef) HasUserClause() bool {
	return f.Auth || len(f.Permissions) > 0 || f.RequireOwnership
}
