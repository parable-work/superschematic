package stack

import (
	"fmt"
	"strings"
)

// Code names the check a resolution error comes from, so a test or a tool
// can tell the failures apart without matching message text.
type Code string

// The checks of docs/stack-model.md, section 5.2.
const (
	// CodeUnboundField is a required config field with no binding and no
	// default.
	CodeUnboundField Code = "unbound-field"

	// CodeUnknownEnvKey is an `env` key that is not a field of the server's
	// `@envVars`, or an `env` on a database.
	CodeUnknownEnvKey Code = "unknown-env-key"

	// CodeSecretLiteral is a literal or a parameter for a `Secret<T>`
	// field.
	CodeSecretLiteral Code = "secret-literal"

	// CodeKindMismatch is a handle whose kind does not match the service it
	// names, or a handle to a service of a kind its place does not take.
	CodeKindMismatch Code = "kind-mismatch"

	// CodeUnrealizable is a deployable its platform cannot realize: a
	// language or dialect the platform does not run, a platform of another
	// deployable kind, or a target with no platform for the kind.
	CodeUnrealizable Code = "unrealizable"

	// CodeNoConnector is an edge no connector realizes between the two
	// platforms.
	CodeNoConnector Code = "no-connector"

	// CodeExposeNotServer is an exposed deployable that is not a server.
	CodeExposeNotServer Code = "expose-not-server"

	// CodePolicy is a violation of a target's policy rule.
	CodePolicy Code = "policy"

	// CodeUnreachableEdge is a calls edge from a server to an API none of
	// whose operations admits the server (section 9.3).
	CodeUnreachableEdge Code = "unreachable-edge"
)

// The other failures resolution reports.
const (
	// CodeInvalidStack is a malformed declaration: a bad or repeated name,
	// a deployable that hosts or serves nothing, a service two deployables
	// claim, an environment that extends an unknown or its own descendant.
	CodeInvalidStack Code = "invalid-stack"

	// CodeUnknownService is a handle to a service Input.Services lacks.
	CodeUnknownService Code = "unknown-service"

	// CodeUnknownDeployable is an `of` or `expose` entry that names no
	// deployable of the stack.
	CodeUnknownDeployable Code = "unknown-deployable"

	// CodeUnknownTarget is an environment without a target, or with one
	// the registry lacks.
	CodeUnknownTarget Code = "unknown-target"

	// CodeUnknownPlatform is a platform or DNS platform the registry lacks.
	CodeUnknownPlatform Code = "unknown-platform"

	// CodeInvalidValues is a target's or DNS platform's values that fail
	// its schema.
	CodeInvalidValues Code = "invalid-values"

	// CodeInvalidSettings is a deployable's settings that fail its
	// platform's schema, or an `env` literal that is not a string, a number
	// or a boolean.
	CodeInvalidSettings Code = "invalid-settings"

	// CodeUnknownParameter is a reference to a parameter the environment
	// does not declare.
	CodeUnknownParameter Code = "unknown-parameter"

	// CodeFieldCollision is a config field two types declare for one
	// server, or one a derived field takes.
	CodeFieldCollision Code = "field-collision"

	// CodeAmbiguousDatabase is an API service with several DB dependencies
	// and no `authDb` to pick its database.
	CodeAmbiguousDatabase Code = "ambiguous-database"

	// CodeCallCycle is a cycle of calls, which no callee-first order
	// serves.
	CodeCallCycle Code = "call-cycle"

	// CodeLowering is an error a platform, connector or DNS platform
	// returned, or a result it should not have returned.
	CodeLowering Code = "lowering"

	// CodeGraph is a resource graph check (validation level 3): a dangling
	// or cyclic dependency, a node two producers make differently, an
	// unknown resource type, properties that fail their type's schema, an
	// inherited node the parent environment lacks, or a deploy order the
	// dependencies contradict.
	CodeGraph Code = "graph"
)

// Error is one resolution failure.
type Error struct {
	Code    Code
	Message string
}

func (e Error) Error() string { return e.Message }

// Errors are every failure one resolution found.
type Errors struct {
	// Stack and Environment name what was resolved.
	Stack, Environment string

	// List holds the failures in the order they were found.
	List []Error
}

func (e *Errors) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "stack %s environment %s does not resolve:", e.Stack, e.Environment)
	for _, err := range e.List {
		fmt.Fprintf(&b, "\n  %s: %s", err.Code, err.Message)
	}
	return b.String()
}

// Has reports whether one of the failures has code.
func (e *Errors) Has(code Code) bool {
	for _, err := range e.List {
		if err.Code == code {
			return true
		}
	}
	return false
}

// Of returns the failures with code.
func (e *Errors) Of(code Code) []Error {
	var out []Error
	for _, err := range e.List {
		if err.Code == code {
			out = append(out, err)
		}
	}
	return out
}
