package ir

import "sort"

// The Stack IR: what a stack declares (docs/stack-model.md, sections 3
// and 4). A stack names its entry points, the deployables that differ from
// the defaults, and its environments. The resolver
// (internal/stack) turns one environment of it into a ResolvedEnvironment.
//
// The Stack kind's decorators, `@stack`, `@server`, `@database` and
// `@environment` from `@superschematic/stack`, each write a declaration on
// its class's TypeDef (StackDecl, ServerDecl, DatabaseDecl and
// EnvironmentDecl), and StackOf assembles a schema's declarations into its
// Stack. A service handle is a ServiceRef, a declared deployable's class is
// its name, and the keys of one `settings` element other than `of`, `job`,
// `platform`, `env`, `schedule`, `timeZone` and `enabled` are the platform
// settings.

// DeployableKind is the kind of a deployable: what runs (section 3.1).
type DeployableKind string

const (
	// DeployableDatabase hosts one or more DB schemas and provides a SQL
	// connection per hosted schema.
	DeployableDatabase DeployableKind = "database"

	// DeployableServer serves one or more API schemas, needs a connection
	// to each served API's database and the address of each service it
	// calls.
	DeployableServer DeployableKind = "server"

	// DeployableJob runs one `@job` of an API schema to completion, on a
	// schedule or on demand. Its needs are its API's: the connection to
	// the API's database and the address of each service the API calls
	// (D52).
	DeployableJob DeployableKind = "job"

	// DeployableBucket is one Bucket service: private object storage, which
	// needs nothing. The server and jobs of each API that lists it reach it
	// over a bucket edge (D54).
	DeployableBucket DeployableKind = "bucket"
)

// Valid reports whether k is a deployable kind v1 knows.
func (k DeployableKind) Valid() bool {
	return k == DeployableDatabase || k == DeployableServer || k == DeployableJob || k == DeployableBucket
}

// HasImage reports whether a deployable of kind k runs code the stack's
// build writes an entrypoint for, and so has an image a deploy builds,
// pins and records: a server or a job (D52).
func (k DeployableKind) HasImage() bool {
	return k == DeployableServer || k == DeployableJob
}

// DeployableKinds returns the deployable kinds, in a fixed order.
func DeployableKinds() []DeployableKind {
	return []DeployableKind{DeployableDatabase, DeployableServer, DeployableJob, DeployableBucket}
}

// EdgeKind is the kind of an edge: a need met by something that provides
// it (section 3.3).
type EdgeKind string

const (
	// EdgeSQL runs from a server to the database that hosts a served API's
	// database schema, and from a job to its API's.
	EdgeSQL EdgeKind = "sql"

	// EdgeHTTP runs from a server to the server that serves an API it
	// calls, and from a job to the server of each API its API calls.
	EdgeHTTP EdgeKind = "http"

	// EdgeBucket runs from a server, and from a job, to each bucket an API
	// it serves lists in its config's buckets (D54).
	EdgeBucket EdgeKind = "bucket"
)

// Valid reports whether k is an edge kind v1 knows.
func (k EdgeKind) Valid() bool {
	return k == EdgeSQL || k == EdgeHTTP || k == EdgeBucket
}

// A service handle is a ServiceRef (ir/schema.go), written `{name, kind}`
// in the data forms. Resolution checks its kind against the service it
// names.

// DeployableRef names a deployable: by a service handle, which means the
// deployable that hosts or serves that service, or by the name of a
// declared deployable's class. Exactly one is set. With Job beside an API
// service's handle, it names that job of the API (D52).
type DeployableRef struct {
	// Service is a handle to a service the deployable hosts or serves.
	Service *ServiceRef `json:"service,omitempty" yaml:"service,omitempty"`

	// Deployable is the name of a declared deployable.
	Deployable string `json:"deployable,omitempty" yaml:"deployable,omitempty"`

	// Job names a job of the API service Service names: the name of its
	// `@job` class (`{of: ShopOrders, job: "ExpireCarts"}`).
	Job string `json:"job,omitempty" yaml:"job,omitempty"`
}

// String renders the reference the way resolution errors quote it.
func (r DeployableRef) String() string {
	if r.Service != nil {
		if r.Job != "" {
			return r.Service.Name + " job " + r.Job
		}
		return r.Service.Name
	}
	return r.Deployable
}

// Stack is a stack's declarations (section 4.1).
type Stack struct {
	// Name is the stack's name: its service's name. It is the `<stack>`
	// segment of `stack/<stack>/<environment>/environment.json`.
	Name string `json:"name" yaml:"name"`

	// Deploy names the entry points. Every service they reach through
	// `authDb`, DB dependencies and `calls` joins the stack.
	Deploy []ServiceRef `json:"deploy,omitempty" yaml:"deploy,omitempty"`

	// Expose names what is reachable from outside the environment. Each
	// must be a server.
	Expose []DeployableRef `json:"expose,omitempty" yaml:"expose,omitempty"`

	// Deployables are the declared deployables: those that differ from the
	// defaults of section 3.2. StackOf gives them in name order.
	Deployables []*DeployableDecl `json:"deployables,omitempty" yaml:"deployables,omitempty"`

	// Environments are the stack's environments. StackOf gives them in the
	// order they are declared (EnvironmentDecl.Order), the order the
	// generated CI deploys them in (D47).
	Environments []*Environment `json:"environments,omitempty" yaml:"environments,omitempty"`
}

// Environment returns the environment named name, or nil.
func (s *Stack) Environment(name string) *Environment {
	for _, env := range s.Environments {
		if env != nil && env.Name == name {
			return env
		}
	}
	return nil
}

// DeployableDecl is a declared deployable, such as an `@server` class. It
// is declared only to change a default: to run several APIs in one
// process, or to host several DB schemas on one database. It only groups:
// a server's edges are the union of its APIs' edges, from their `authDb`
// and `calls` (section 3.3).
type DeployableDecl struct {
	// Name is the declaring class's name.
	Name string `json:"name" yaml:"name"`

	// Kind is the deployable's kind.
	Kind DeployableKind `json:"kind" yaml:"kind"`

	// Serves lists the API services a server serves.
	Serves []ServiceRef `json:"serves,omitempty" yaml:"serves,omitempty"`

	// Hosts lists the DB services a database hosts.
	Hosts []ServiceRef `json:"hosts,omitempty" yaml:"hosts,omitempty"`
}

// Environment places the stack's deployables on a target and sets the
// values only a person can decide (sections 4.1 and 5.4).
type Environment struct {
	// Name is the environment's name: the declaring class's name.
	Name string `json:"name" yaml:"name"`

	// Extends names the environment this one inherits its values from.
	Extends string `json:"extends,omitempty" yaml:"extends,omitempty"`

	// Target names the target that places every deployable no setting
	// places elsewhere. An environment that extends another may leave it
	// empty to inherit it.
	Target string `json:"target,omitempty" yaml:"target,omitempty"`

	// Values are the target's environment values (`gcp: {project,
	// region}`), checked against the schema the target registers. An
	// extending environment's values are merged key by key over its
	// parent's.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`

	// Domain is where exposed servers are reached.
	Domain string `json:"domain,omitempty" yaml:"domain,omitempty"`

	// DNS places the domain's records on a DNS platform. Nil takes the
	// target's default.
	DNS *DNSPlacement `json:"dns,omitempty" yaml:"dns,omitempty"`

	// Settings set values per deployable.
	Settings []*DeployableSettings `json:"settings,omitempty" yaml:"settings,omitempty"`

	// Parameters make the environment a family, one member per value
	// (section 5.4). A value is never written into a generated file.
	Parameters []string `json:"parameters,omitempty" yaml:"parameters,omitempty"`
}

// DNSPlacement names the DNS platform that holds an environment's records
// and the values it needs (`dns: { cloudflare: { zone, zoneId } }`).
type DNSPlacement struct {
	// Platform is the registered DNS platform's name.
	Platform string `json:"platform" yaml:"platform"`

	// Values are checked against the schema the DNS platform registers.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`
}

// DeployableSettings is one `settings` element of an environment.
type DeployableSettings struct {
	// Of names the deployable the element sets.
	Of DeployableRef `json:"of" yaml:"of"`

	// Platform places the deployable on a platform other than the one its
	// environment's target names for its kind.
	Platform string `json:"platform,omitempty" yaml:"platform,omitempty"`

	// Values are the platform settings (`minInstances`, `tier`), checked
	// against the schema of the platform the deployable lands on.
	Values map[string]any `json:"values,omitempty" yaml:"values,omitempty"`

	// Env binds fields of a server's `@envVars` type, keyed by field name.
	// A job's fields are its API's: it takes the env its API's server is
	// given, with its own over that.
	Env map[string]EnvValue `json:"env,omitempty" yaml:"env,omitempty"`

	// Schedule replaces a job's schedule, a five-field cron, and TimeZone
	// the IANA time zone it is read in. Only an element whose `of` names a
	// job sets them (D52).
	Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty"`
	TimeZone string `json:"timeZone,omitempty" yaml:"timeZone,omitempty"`

	// Enabled turns a job's schedule on or off. Unset, a schedule runs,
	// except in a parameterized environment, whose members run none unless
	// their settings turn it on. A job whose schedule is off runs only on
	// demand.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// EnvValue is the value an environment gives one config field: a literal,
// or one of the environment's parameters, which the deploy run supplies.
// Exactly one is set.
type EnvValue struct {
	// Value is a literal: a string, a number or a boolean.
	Value any `json:"value,omitempty" yaml:"value,omitempty" jsonschema:"oneof_type=string;number;boolean"`

	// Parameter names one of the environment's parameters.
	Parameter string `json:"parameter,omitempty" yaml:"parameter,omitempty"`
}

// StackDecl is the declaration of a stack's `@stack` class: Stack's Deploy
// and Expose.
type StackDecl struct {
	// Deploy names the entry points.
	Deploy []ServiceRef `json:"deploy,omitempty" yaml:"deploy,omitempty"`

	// Expose names what is reachable from outside the environment.
	Expose []DeployableRef `json:"expose,omitempty" yaml:"expose,omitempty"`
}

// ServerDecl is the declaration of an `@server` class: a declared server
// that serves several APIs in one process.
type ServerDecl struct {
	// Serves lists the API services the server serves.
	Serves []ServiceRef `json:"serves" yaml:"serves"`
}

// DatabaseDecl is the declaration of an `@database` class: a declared
// database that hosts several DB schemas.
type DatabaseDecl struct {
	// Hosts lists the DB services the database hosts.
	Hosts []ServiceRef `json:"hosts" yaml:"hosts"`
}

// EnvironmentDecl is the declaration of an `@environment` class: an
// Environment without the name and the parent, which are the class's name
// and the class it extends, and with its place among the stack's
// environments. Its other fields are Environment's.
type EnvironmentDecl struct {
	Target     string                `json:"target,omitempty" yaml:"target,omitempty"`
	Values     map[string]any        `json:"values,omitempty" yaml:"values,omitempty"`
	Domain     string                `json:"domain,omitempty" yaml:"domain,omitempty"`
	DNS        *DNSPlacement         `json:"dns,omitempty" yaml:"dns,omitempty"`
	Settings   []*DeployableSettings `json:"settings,omitempty" yaml:"settings,omitempty"`
	Parameters []string              `json:"parameters,omitempty" yaml:"parameters,omitempty"`

	// Order is the environment's place in declaration order, from 1. The
	// TypeScript reader numbers the `@environment` classes: the schema
	// files in path order, and the classes of a file in source order. A
	// data form writes it; zero is none, and such an environment comes
	// after those with one (D47).
	Order int `json:"order,omitempty" yaml:"order,omitempty"`
}

// StackOf assembles the stack a Stack schema declares from its types'
// declarations. The stack takes the schema's name, the `@stack` class gives
// its entry points and what it exposes, each `@server` and `@database`
// class is a declared deployable, and each `@environment` class is an
// environment that extends the class it extends, in declaration order
// (EnvironmentNames). It returns nil when no type declares `@stack`, and
// reads the first by name when several do. It checks nothing: the Stack
// kind's verification does, and resolution checks the rest.
func StackOf(schema *Schema) *Stack {
	if schema == nil {
		return nil
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var stack *Stack
	var deployables []*DeployableDecl
	for _, name := range names {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		if td.Stack != nil && stack == nil {
			stack = &Stack{
				Name:   schema.Name,
				Deploy: td.Stack.Deploy,
				Expose: td.Stack.Expose,
			}
		}
		if td.Server != nil {
			deployables = append(deployables, &DeployableDecl{Name: td.Name, Kind: DeployableServer, Serves: td.Server.Serves})
		}
		if td.Database != nil {
			deployables = append(deployables, &DeployableDecl{Name: td.Name, Kind: DeployableDatabase, Hosts: td.Database.Hosts})
		}
	}
	if stack == nil {
		return nil
	}
	stack.Deployables = deployables
	for _, name := range EnvironmentNames(schema.Types) {
		td := schema.Types[name]
		e := td.Environment
		stack.Environments = append(stack.Environments, &Environment{
			Name:       td.Name,
			Extends:    td.Extends,
			Target:     e.Target,
			Values:     e.Values,
			Domain:     e.Domain,
			DNS:        e.DNS,
			Settings:   e.Settings,
			Parameters: e.Parameters,
		})
	}
	return stack
}

// EnvironmentNames returns the names of the types that declare an
// `@environment`, in declaration order: by EnvironmentDecl.Order, then
// those without one by name. Equal orders, which the Stack kind's
// verification refuses, keep name order, so the result is the same on
// every call.
func EnvironmentNames(types map[string]*TypeDef) []string {
	var names []string
	for name, td := range types {
		if td != nil && td.Environment != nil {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	order := func(name string) int { return max(types[name].Environment.Order, 0) }
	sort.SliceStable(names, func(i, j int) bool {
		a, b := order(names[i]), order(names[j])
		if (a == 0) != (b == 0) {
			return b == 0
		}
		return a < b
	})
	return names
}

// StackReferences returns every service handle the declarations of a Stack
// schema's classes hold: `@stack`'s deploy and its exposed handles,
// `@server`'s serves, `@database`'s hosts and each settings element's `of`,
// in declaration order and with repeats. They are the schema's references
// (Schema.References, D41). The TypeScript form records them from the
// decorators' arguments; the data forms write the declarations as these
// typed fields, so the loader adds them from here in every form.
func StackReferences(schema *Schema) []ServiceRef {
	if schema == nil {
		return nil
	}
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	var refs []ServiceRef
	deployable := func(ref DeployableRef) {
		if ref.Service != nil {
			refs = append(refs, *ref.Service)
		}
	}
	for _, name := range names {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		if td.Stack != nil {
			refs = append(refs, td.Stack.Deploy...)
			for _, exposed := range td.Stack.Expose {
				deployable(exposed)
			}
		}
		if td.Server != nil {
			refs = append(refs, td.Server.Serves...)
		}
		if td.Database != nil {
			refs = append(refs, td.Database.Hosts...)
		}
		if td.Environment != nil {
			for _, settings := range td.Environment.Settings {
				if settings != nil {
					deployable(settings.Of)
				}
			}
		}
	}
	return refs
}
