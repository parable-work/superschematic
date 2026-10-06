// Package stack resolves one environment of a stack: it places every
// deployable on a platform, wires every need to something that provides
// it, binds every config field of every server, lowers the result to a
// resource graph through the registered platforms, connectors and DNS
// platforms, and orders the deploy (docs/stack-model.md, sections 3, 5
// and 6). Resolve is a pure function of its inputs and the registry; Write
// puts its result at `<output-root>/stack/<stack>/<environment>/environment.json`.
package stack

import ir "github.com/parable-work/superschematic/ir"

// Input is what Resolve reads: a stack, the facts of the services it
// references, and the environment to resolve.
type Input struct {
	// Stack is the stack's IR.
	Stack *ir.Stack

	// Services are the facts of every service the stack reaches. A service
	// the stack does not reach is ignored.
	Services []Service

	// Environment names the environment of Stack to resolve.
	Environment string

	// FieldNames name the config field each edge fills: the naming file's
	// `[derived_fields]`, which envgen names the generated fields by. The
	// zero value is the core's rule.
	FieldNames ir.DerivedFieldNames
}

// Service is what resolution needs to know about one service. It is read
// from the service's IR and schema config; its fields are plain so the
// resolver does not depend on where `authDb`, `dependencies` and `calls`
// live.
type Service struct {
	// Name is the service's name.
	Name string

	// Kind is the service's schema kind.
	Kind ir.SchemaKind

	// AuthDB is the handle an API service's `authDb` holds, or nil.
	AuthDB *ir.ServiceRef

	// Dependencies are the handles in the service's `dependencies`. An API
	// service without AuthDB connects to its one DB-kind dependency.
	Dependencies []ir.ServiceRef

	// Calls are the handles in an API service's `calls`: the APIs its
	// server's code calls.
	Calls []ir.ServiceRef

	// Language is an API service's `outputs.api.language`
	// (registry.APILanguageGo, ...). Empty means Go.
	Language string

	// Dialects are a DB service's `outputs.sql.dialects`. Empty means
	// postgres.
	Dialects []string

	// Config is the service's `@envVars` type, or nil when it has none.
	Config *Config

	// Operations are an API service's operations, each with what admits a
	// caller to it (docs/stack-model.md, section 9.3): every calls edge to
	// the API must reach one its caller may invoke. OperationsOf reads them
	// from the service's IR.
	Operations []Operation
}

// Operation is what admits a caller to one operation of an API service.
type Operation struct {
	// Name is the operation's set and its name, dotted:
	// "ProductQueries.getProduct".
	Name string

	// UserClause reports that the operation requires an end user: @auth,
	// an Authenticated set, @requirePermission or @requireOwnership.
	UserClause bool

	// ServiceCallers is the operation's effective service clause,
	// @requireService or @allowService, its own or its set's; nil when it
	// has none.
	ServiceCallers *ir.ServiceCallers
}

// OperationsOf reads the operations of an API service's IR as
// Service.Operations holds them.
func OperationsOf(schema *ir.Schema) []Operation {
	var ops []Operation
	for _, set := range schema.OperationSets {
		if set == nil {
			continue
		}
		for _, op := range set.Operations {
			if op == nil {
				continue
			}
			ops = append(ops, Operation{
				Name:           set.Name + "." + op.Name,
				UserClause:     op.HasUserClause(),
				ServiceCallers: ir.EffectiveServiceCallers(set, op),
			})
		}
	}
	return ops
}

// Config is a service's `@envVars` type.
type Config struct {
	// Type is the `@envVars` type's name.
	Type string

	// Fields are the type's fields, inherited ones included.
	Fields []ConfigField
}

// ConfigField is one field of an `@envVars` type.
type ConfigField struct {
	// Name is the field's name, which is its environment variable.
	Name string

	// Required is true for a field that is not optional.
	Required bool

	// Secret is true for a `Secret<T>` field.
	Secret bool

	// Default is a `Default<T, V>` field's default, or nil.
	Default *string

	// InheritedFrom names the base type that declares the field, as
	// ir.FieldDef.InheritedFrom does; empty when Type declares it.
	InheritedFrom string
}

// declaringType returns the type that declares f in a config of type
// typeName: what identifies a secret (section 4.2).
func (f ConfigField) declaringType(typeName string) string {
	if f.InheritedFrom != "" {
		return f.InheritedFrom
	}
	return typeName
}
