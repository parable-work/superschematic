// Package stack resolves one environment of a stack: it places every
// deployable on a platform, wires every need to something that provides
// it, binds every config field of every server, lowers the result to a
// resource graph through the registered platforms, connectors and DNS
// platforms, and orders the deploy (docs/stack-model.md, sections 3, 5
// and 6). Resolve is a pure function of its inputs and the registry; Write
// puts its result at `<output-root>/stack/<stack>/<environment>/environment.json`.
package stack

import (
	"path/filepath"

	ir "github.com/parable-work/superschematic/ir"
)

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

	// SchemasRoot is the absolute schemas root of the checkout the
	// environment is resolved in, symlinks resolved, which platforms see
	// as registry.StackEnvironment.SchemasRoot: the local target names its
	// containers and derives its default ports per checkout by it. Every
	// build of the CLI sets it (CanonicalSchemasRoot); empty resolves a
	// local environment for no checkout, as before checkouts were told
	// apart.
	SchemasRoot string
}

// CanonicalSchemasRoot returns the absolute path of a schemas root with
// its symlinks resolved, as Input.SchemasRoot holds it, so a checkout
// reached through a link resolves as it does through its own path. A root
// whose links do not resolve is its absolute path.
func CanonicalSchemasRoot(root string) (string, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		return real, nil
	}
	return abs, nil
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
	// server's code calls. A Site service's are the APIs its code calls
	// from the browser (D55).
	Calls []ir.ServiceRef

	// Buckets are the handles in an API service's `buckets`: the Bucket
	// services its server's and jobs' code use (D54).
	Buckets []ir.ServiceRef

	// MemoryStores are the MemoryStore services an API service uses: the
	// handles in its `memoryStores`, with its `rateLimitStore` (D75), as
	// ir.Schema.MemoryStores holds them.
	MemoryStores []ir.ServiceRef

	// Language is an API service's `outputs.api.language`
	// (registry.APILanguageGo, ...). Empty means Go.
	Language string

	// Dialects are a DB service's `outputs.sql.dialects`. Empty means
	// postgres.
	Dialects []string

	// FieldKeys is a DB service's `outputs.sql.encryption.key` (D85): the
	// secret, by its ir.SecretID, that holds the keys of its Encrypted<T>
	// fields, which every deployable that runs code on the database reads
	// through its API's `@envVars`. Empty for a database without one.
	FieldKeys string

	// Config is the service's `@envVars` type, or nil when it has none.
	Config *Config

	// Identity reports that an API service's server authenticates with the
	// identity runtime (D50): its authDb, or a public API's one DB-kind
	// dependency, declares the user model. Each server that serves it
	// binds its identity config field (ir.IdentityConfigField).
	Identity bool

	// Operations are an API service's operations, each with what admits a
	// caller to it (docs/stack-model.md, section 9.3): every calls edge to
	// the API must reach one its caller may invoke. OperationsOf reads them
	// from the service's IR.
	Operations []Operation

	// Jobs are an API service's jobs, its `@job` classes (D52), as
	// ir.Schema.Jobs holds them. Each is a deployable of the stack.
	Jobs []ir.Job

	// Site is a Site service's build and where its code is, with every
	// default filled in (D55); nil for every other kind. Its Calls are the
	// APIs its code calls from the browser.
	Site *ir.ResolvedSite

	// Workers are an API service's workers, its `@worker` classes (D53), as
	// ir.Schema.Workers holds them. Each is a deployable of the stack.
	Workers []ir.Worker

	// Derivers are an API service's derivers, its `@deriver` classes
	// (D74), as ir.Schema.Derivers holds them. Each is a deployable of the
	// stack.
	Derivers []ir.Deriver

	// Apps are an API service's apps, its `@app` classes (D78), as
	// ir.Schema.Apps holds them. Each is a deployable of the stack.
	Apps []ir.App

	// Queues are the names of a DB service's queues, its `@queue` classes
	// (D53), sorted. A worker's queue is one of its API's database.
	Queues []string

	// QueueKeys are the quoted key columns of a DB service's queues that
	// keep one ready message per key (D80), by queue: an operator's retry
	// of such a queue's dead messages leaves a key one ready message.
	QueueKeys map[string]string

	// QueueTables are the tables of a DB service's queues whose @table
	// names them (D85), by queue, so the operator's statements name that
	// table; every other queue's table is its class's name with _queue.
	QueueTables map[string]string

	// Engine reports that a DB service is an engine database (D58): a type
	// of it composes a behavior. The servers of the APIs that connect to it
	// open the engine on its database, and the stack runs the engine's
	// runner over it, a deployable of its own.
	Engine bool

	// Derived are the names of a DB service's engine types that compose
	// Derived (D73), sorted. A deriver's schemas are among those of its
	// API's database (D74).
	Derived []string
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
