package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	validator "github.com/santhosh-tekuri/jsonschema/v6"

	ir "github.com/parable-work/superschematic/ir"
)

// The stack model's registrations (docs/stack-model.md, section 6): a
// deployable is placed on a platform, an edge between two placed
// deployables is realized by a connector, a target names a platform for
// each deployable kind, a DNS platform holds an environment's domain
// records, a provisioner turns the resource graph into running resources,
// and a CI renderer writes a stack's workflow (stack_ci.go). The resolver
// (internal/stack) reads them. The core registers one target, `local`,
// with its platforms, connectors and provisioner (internal/stack/local),
// and one CI renderer, `github`; every other target is an extension's.

// StackEnvironment is the environment being resolved, as platforms,
// connectors and DNS platforms see it. They must not modify it.
type StackEnvironment struct {
	// Stack and Name name the stack and the environment.
	Stack string
	Name  string

	// Extends names the parent environment.
	Extends string

	// Target is the environment's target.
	Target string

	// Values are the target's environment values, the parent's merged in.
	Values map[string]any

	// Domain is where exposed servers are reached; empty for none.
	Domain string

	// Parameters are the environment's parameters, the parent's first. A
	// platform names a deployable under them (section 5.4).
	Parameters []string

	// SchemasRoot is the absolute schemas root of the checkout the
	// environment is resolved in, symlinks resolved, or empty when the
	// resolution names none. A target that runs the environment on the
	// machine at hand tells checkouts apart by it: the local target names
	// its containers and derives its default ports per checkout (section
	// 8.3). A cloud target's platforms ignore it.
	SchemasRoot string
}

// PlatformContext is what a platform's NameOf, AddressOf and Lower
// receive: the environment, and a copy of the deployable resolved so far.
// NameOf sees no ResourceName, Address or Bindings; AddressOf sees the
// ResourceName; Lower sees all three.
type PlatformContext struct {
	Environment StackEnvironment
	Deployable  ir.ResolvedDeployable
}

// Lowered is what a platform lowers one deployable to.
type Lowered struct {
	// Resources are the deployable's nodes.
	Resources []*ir.Resource

	// Records are the DNS records an exposed server needs under the
	// environment's domain, in the neutral shape the DNS platform lowers
	// (section 6.9): the server's host and the records its certificate
	// needs for validation.
	Records []*ir.DNSRecord
}

// UnrealizableError is a platform's refusal of a deployable it cannot
// realize, such as a name its provider refuses (D64). A Lower that returns
// one, made with Unrealizable, has resolution report the deployable as
// unrealizable, as it reports a language its platform does not run, rather
// than as a failed lowering.
type UnrealizableError struct {
	// Reason says what the platform cannot realize, and what to change.
	Reason string
}

func (e *UnrealizableError) Error() string { return e.Reason }

// Unrealizable returns an UnrealizableError whose reason is the format
// applied to args.
func Unrealizable(format string, args ...any) error {
	return &UnrealizableError{Reason: fmt.Sprintf(format, args...)}
}

// PlatformSpec realizes one deployable kind on one runtime: Cloud Run
// servers, Cloud SQL databases, local processes (section 6.1).
type PlatformSpec struct {
	// Name is the registry key (`gcp.cloudrun`): lowercase words joined by
	// dots or hyphens.
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Provider names the provider the platform runs its deployables on, a
	// lowercase word or words joined by dots or hyphens: `local`, `gcp`,
	// `cloudflare`, `fake`. Two platforms of one provider meet only
	// through a connector that names them both; between two of different
	// providers that no connector names, the core's generic connector
	// serves an http or a site edge (section 6.2, D63). Required.
	Provider string

	// Kind is the deployable kind the platform realizes.
	Kind ir.DeployableKind

	// Languages are the server languages a server, job or worker platform
	// runs, as `outputs.api.language` spells them (APILanguageGo, ...): a
	// job or a worker is written in its API's language. Required for a
	// server, job or worker platform, refused for a database platform.
	Languages []string

	// Dialects are the SQL dialects a database platform runs
	// (SQLDialectPostgres, ...), in order of preference. Required for a
	// database platform, refused for a server platform.
	Dialects []string

	// Artifact is what a deploy builds for a server, a job or a worker on
	// the platform, and resolution records (ir.ResolvedDeployable.Artifact):
	// ir.ArtifactImage, the default, which the target's Builder builds from
	// the Dockerfile the stack's build writes; or ir.ArtifactBundle, the
	// files `bun bundle.ts <dir>` writes in the deployable's package, which
	// the target's Bundles keeps, and whose digest and files' hashes the
	// platform writes into its nodes as ir.BundleDigestToken and
	// ir.BundleFileToken (D64). Refused for any other platform.
	Artifact ir.Artifact

	// Settings is the JSON Schema of a deployable's settings on this
	// platform (`minInstances`, `tier`). Nil accepts no settings.
	Settings json.RawMessage

	// IdentityConfig is the identity config, a JSON object, that a server
	// on this platform runs each API over the user model with (D50;
	// ir.IdentityConfigField) unless its environment sets the field: the
	// local platform's turns the session cookie's Secure attribute off,
	// since it serves plain HTTP. Empty leaves the field unbound, and the
	// server runs with the identity runtime's defaults. Only a server
	// platform declares one: a job builds no identity service.
	IdentityConfig string

	// NoIdentity, when set, says why a server on this platform cannot
	// serve an API over the user model, which authenticates with the
	// identity runtime (D50): resolution refuses such a server as
	// unrealizable, giving the reason. The cloudflare.workers platform sets
	// it, since the TypeScript identity runtime hashes passwords with
	// node:crypto's argon2, which Workers lacks (D64). Only a server
	// platform sets it, and not beside IdentityConfig.
	NoIdentity string

	// NameOf returns the deployable's name in the environment, a string or
	// a value that references the environment's parameters.
	NameOf func(PlatformContext) any

	// AddressOf returns how an edge reaches the deployable: a value that
	// usually references an output of one of the deployable's nodes.
	AddressOf func(PlatformContext) any

	// PublicAddressOf returns where a browser reaches the deployable from
	// outside the environment when it is exposed: an https URL of its
	// host under the environment's domain, or of a host the platform gives
	// it, with no path. A site edge derives the API's from its server's,
	// and a site's own is the origin the CORS field of each API it calls
	// lists (D55). The generic connector, which joins deployables of two
	// providers, reaches the callee there too (D63). It sees what
	// AddressOf sees, and the Address. Nil, or a nil result, gives the
	// deployable none: a site edge or a generic one to it fails to
	// resolve. A site platform must have it.
	PublicAddressOf func(PlatformContext) any

	// Lower returns the deployable's resources. It is pure: the same
	// context gives the same result.
	Lower func(PlatformContext) (Lowered, error)

	compiledSettings *validator.Schema
}

// ValidateSettings checks a deployable's settings against the platform's
// schema.
func (s PlatformSpec) ValidateSettings(settings map[string]any) error {
	if s.compiledSettings == nil {
		if len(settings) > 0 {
			return fmt.Errorf("platform %s takes no settings", s.Name)
		}
		return nil
	}
	return validateValue(s.compiledSettings, settings)
}

// ConnectorContext is what a connector's Connect receives. Edge is the
// edge with its Connector and Field set; From and To are copies of the two
// deployables, with their names and addresses. From's derived bindings
// have no values yet: every connector runs before any value is set.
type ConnectorContext struct {
	Environment StackEnvironment
	Edge        ir.Edge
	From        ir.ResolvedDeployable
	To          ir.ResolvedDeployable
}

// Connected is what a connector returns for one edge.
type Connected struct {
	// Resources are the edge's nodes: an IAM grant, a network rule.
	Resources []*ir.Resource

	// Value is the derived binding's value: what fills the edge's config
	// field on the From server.
	Value any

	// Callee is what an http edge between two servers gives the callee:
	// how it verifies the caller's service credential, an
	// ir.ServiceAuthIssuer whose one caller is From, with the APIs From
	// serves (D37). Resolution merges the entries of the edges to an API
	// into the API's callers field when an operation of the API has a
	// service clause, and refuses an edge to such an API without one. A
	// call within one server has none: the server signs it with a
	// loopback key of its own, which it accepts itself
	// (docs/stack-model.md, section 9.9).
	Callee any
}

// ConnectorSpec realizes one edge kind between two platforms (section 6.2).
type ConnectorSpec struct {
	// Name is the registry key.
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Edge is the edge kind the connector realizes.
	Edge ir.EdgeKind

	// From and To name the platforms at the two ends. From is a server, a
	// job or a worker platform; To is a database platform for sql, a
	// server platform for http and a bucket platform for bucket (D54). A
	// job's edges are its API's (D52), and so are a worker's (D53), so a
	// target that places jobs or workers registers a connector from its
	// job or worker platform for each edge its servers take. A site edge
	// runs from a site platform to a server platform (D55). A generic
	// connector names neither.
	From string
	To   string

	// Generic makes the connector the one that serves its edge kind
	// between two platforms of different providers that no connector names
	// (section 6.2, D63): Registry.Connector falls back to it, and a
	// connector that names both platforms always wins. It serves an http
	// or a site edge, names no From or To, and is one per edge kind. The
	// core registers it (internal/stack/generic); a database or a bucket on
	// another provider needs a connector that knows both.
	Generic bool

	// ResourceTypes holds the JSON Schema of the properties of each
	// resource type Connect emits that no target registers, as a
	// DNSPlatformSpec's ResourceTypes does: the generic connector, which no
	// target owns, brings the schema of its key node's type.
	ResourceTypes map[string]json.RawMessage

	// Connect returns the edge's resources and the derived binding's
	// value. It is pure.
	Connect func(ConnectorContext) (Connected, error)
}

// PolicyRule is a target's rule over a resolved environment and its
// resource graph, such as "nothing is public unless exposed".
type PolicyRule struct {
	// Name identifies the rule in a resolution error.
	Name string

	// Check returns one message per violation.
	Check func(env *ir.ResolvedEnvironment) []string
}

// TargetSpec is a named bundle of a platform per deployable kind, the
// schema of its environment values, its default DNS platform and its
// policy rules (section 6.3).
type TargetSpec struct {
	// Name is the registry key (`gcp`, `local`).
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Platforms names the platform that places each deployable kind. A
	// kind may be missing; resolution then refuses a deployable of it on
	// this target.
	Platforms map[ir.DeployableKind]string

	// DatabasePlatforms names, by SQL dialect, the platform a database
	// deployable goes on when no settings platform places it and every DB
	// service it hosts lists that dialect first: the local target keeps a
	// sqlite-first database in a SQLite file (D64). A database whose DB
	// services list another dialect first, or disagree, goes on
	// Platforms' database platform.
	DatabasePlatforms map[string]string

	// Values is the JSON Schema of an environment's values for this target
	// (`project` and `region` for gcp). Nil accepts no values.
	Values json.RawMessage

	// DNS names the DNS platform an environment with a domain gets when it
	// names none. Empty gets ir.ManualDNS.
	DNS string

	// Provisioner names the provisioner that applies the target's
	// environments, whose state backend the target's bootstrap creates.
	// Empty leaves the environment without one.
	Provisioner string

	// ResourceTypes holds the JSON Schema of the properties of each
	// resource type the target's platforms, connectors and DNS platform
	// emit, checked in from the provider schemas the target pins (section
	// 6.4). Resolution checks every node against the schema of its type.
	ResourceTypes map[string]json.RawMessage

	// Policies are the target's rules over the resource graph.
	Policies []PolicyRule

	// State keeps the target's deploy state: the provisioner's state
	// backend and each run's deploy manifest (section 11.2). Plan, deploy,
	// destroy and outputs need it; a target without it resolves but does
	// not deploy.
	State StateStore

	// Secrets stores the values of its environments' secrets and of their
	// platform credentials (section 4.2). Nil stores none.
	Secrets SecretStore

	// Bootstrap prepares a cloud project for the target's environments
	// (section 7.3). Nil needs no bootstrap.
	Bootstrap Bootstrapper

	// Migrations runs migration plans on the target's databases between
	// the deploy's steps (section 5.3). Nil runs none, and a deploy that
	// has a migration to run on the target is refused.
	Migrations MigrationRunner

	// Builder builds the images of the target's servers and jobs from the
	// Dockerfiles a stack's build writes (sections 8.2 and 11.2). Nil
	// builds none, and every image comes from --image or the deploy
	// manifest.
	Builder ImageBuilder

	// Jobs runs a deployed job once on demand, outside its schedule
	// (`stack run`, section 8.7, D52). Nil runs none, and `stack run`
	// refuses the target's environments.
	Jobs JobRunner

	// Sites publishes the files a site's build writes, and its config
	// for each run, where the site's platform serves them (section 8.10,
	// D55). Nil publishes none, and a deploy of an environment with a site
	// on the target is refused. One that is also an ArtifactFetcher has the
	// deploy place each site's files beside the provisioner's program
	// (ir.ArtifactsDir, D64).
	Sites SitePublisher

	// Bundles keeps the files of each bundle a deploy builds, for a server,
	// a job or a worker whose platform's artifact is a bundle, under their
	// digest (D64). Nil keeps none, and a deploy of an environment with
	// such a deployable on the target is refused.
	Bundles BundlePublisher

	// PreDestroy readies a run's resources for the provisioner's Destroy,
	// which `stack destroy` calls it before: the cloudflare target empties a
	// member's R2 buckets, which its provider deletes only empty (D64). Nil
	// readies nothing.
	PreDestroy DestroyPreparer

	// CI says how a generated CI job signs in to the target's
	// environments (section 11.3, D47). Nil gives CI no identity, and the
	// environments no cloud jobs.
	CI CIIdentities

	compiledValues *validator.Schema
}

// ValidateValues checks an environment's values against the target's
// schema.
func (s TargetSpec) ValidateValues(values map[string]any) error {
	if s.compiledValues == nil {
		if len(values) > 0 {
			return fmt.Errorf("target %s takes no values", s.Name)
		}
		return nil
	}
	return validateValue(s.compiledValues, values)
}

// DNSContext is what a DNS platform's Lower receives.
type DNSContext struct {
	Environment StackEnvironment

	// Values are the environment's values for the DNS platform (a zone).
	Values map[string]any

	// Records are the records to write, with their Deployable set.
	Records []ir.DNSRecord
}

// DNSPlatformSpec places an environment's domain records with a DNS
// provider (section 6.9). DNS is a platform kind of its own, apart from
// targets, because a domain's DNS often lives with another provider than
// its compute. Its own spec, rather than a PlatformSpec, because it lowers
// records rather than a deployable.
type DNSPlatformSpec struct {
	// Name is the registry key. ir.ManualDNS is reserved.
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Values is the JSON Schema of an environment's values for the DNS
	// platform (a zone). Nil accepts no values.
	Values json.RawMessage

	// Lower returns the records' resources. It is pure.
	Lower func(DNSContext) ([]*ir.Resource, error)

	// ResourceTypes holds the JSON Schema of the properties of each
	// resource type Lower emits that no target registers, as a
	// TargetSpec's ResourceTypes does: a DNS platform of another provider
	// than the target's (Cloudflare DNS beside gcp) brings its own.
	ResourceTypes map[string]json.RawMessage

	// Credentials returns the secrets the platform's provider reads to
	// write an environment's records: an API token the engineer enters at
	// bootstrap. It sees the environment and the DNS values, and no
	// records. It is pure. Nil needs none.
	Credentials func(DNSContext) []ir.DNSCredential

	compiledValues *validator.Schema
}

// ValidateValues checks an environment's DNS values against the DNS
// platform's schema.
func (s DNSPlatformSpec) ValidateValues(values map[string]any) error {
	if s.compiledValues == nil {
		if len(values) > 0 {
			return fmt.Errorf("DNS platform %s takes no values", s.Name)
		}
		return nil
	}
	return validateValue(s.compiledValues, values)
}

// ProvisionRequest is one run of a provisioner over an environment.
type ProvisionRequest struct {
	// Environment is the resolved environment.
	Environment *ir.ResolvedEnvironment

	// Parameters are the values of the environment's parameters for this
	// run. A value is never written into a generated file.
	Parameters map[string]string

	// Dir is where Render wrote the program.
	Dir string

	// OutputRoot is the output root the stack and its services were built
	// to. A provisioner that runs or packages what the build wrote reads it
	// there: the local provisioner builds each server's entrypoint module
	// at `<OutputRoot>/server/<stack>/<server>`. Empty when the run needs
	// no build output.
	OutputRoot string

	// RepositoryRoot is the repository root, the parent of the schemas
	// root, under which each site's package lies at the directory its
	// ir.ResolvedSite names: the local provisioner builds a site there
	// (D55). Empty when the run needs no site's code.
	RepositoryRoot string

	// Backend is where the provisioner keeps the environment's state, as
	// the target's bootstrap created it.
	Backend StateBackend

	// Env holds the platform credentials the run needs, keyed by the
	// environment variable their provider reads (Credential.Env). The
	// provisioner hands them to its tool's process for this run only, and
	// never writes them to its config, its program, a log or a file.
	Env map[string]string

	// BuildEnv holds variables a provisioner that builds code on this
	// machine sets for its builds, over its own environment: CGO_LDFLAGS
	// pointing cgo's linker at the release's static archives in a project
	// whose naming file names no superscalar checkout (D47, amended). The
	// processes it runs from what it built never see them.
	BuildEnv map[string]string
}

// StateBackend is where a provisioner keeps an environment's state, and
// how it encrypts the secrets in it (section 6.5).
type StateBackend struct {
	// URL locates the state: a gs:// bucket for the gcp target, a file://
	// directory in tests.
	URL string

	// SecretsProvider encrypts the secrets in the state: a gcpkms:// key
	// for the gcp target, passphrase in tests.
	SecretsProvider string

	// Env holds the environment variables the state store gives a run to
	// reach the state and decrypt its secrets, keyed by the variable: the
	// cloudflare target's S3 credentials for its R2 bucket and the
	// passphrase of its secrets provider (D64). The provisioner hands them
	// to its tool's process for the run only, after the run's credentials
	// (ProvisionRequest.Env), and never writes them to its config, its
	// program, a log or a file. Nil gives none.
	Env map[string]string
}

// PlannedChange is one resource a plan would change.
type PlannedChange struct {
	// Resource is the node's ID.
	Resource string

	// Action is the provisioner's word for the change: create, update,
	// replace, delete.
	Action string
}

// Provisioner takes a resource graph to running resources and back
// (section 6.5). Plan, Apply and Destroy run with credentials, against a
// state backend the target's bootstrap created.
type Provisioner interface {
	// Render writes the tool's program for the environment's resource
	// graph into dir, where a person can read it. The program also exports
	// every output the environment references, so Outputs can read them.
	Render(env *ir.ResolvedEnvironment, dir string) error

	// Plan returns the changes applying the environment would make.
	Plan(ctx context.Context, req ProvisionRequest) ([]PlannedChange, error)

	// Apply applies the resources of one step of the environment's deploy
	// order. The deploy runs image builds and migrations between steps.
	Apply(ctx context.Context, req ProvisionRequest, step ir.DeployStep) error

	// Destroy removes every resource of the environment.
	Destroy(ctx context.Context, req ProvisionRequest) error

	// Outputs reads the applied graph's outputs, by node ID and output
	// name. They feed the bindings and the deploy manifest.
	Outputs(ctx context.Context, req ProvisionRequest) (map[string]map[string]any, error)
}

// SecretOutputReader is a Provisioner that also reads the value of a secret
// output of an applied node, which Outputs leaves out: a bootstrap hands
// the person, or the CI's secret store, a credential its own graph made,
// such as the cloudflare target's account tokens (D64). The deploy never
// calls it, since no binding and no manifest holds a secret.
type SecretOutputReader interface {
	// SecretOutput returns the string value of one top-level output of a
	// node of req's environment, applied, or an error when the node or the
	// output is missing or the value is not a string.
	SecretOutput(ctx context.Context, req ProvisionRequest, out ir.Output) (string, error)
}

// ProvisionerSpec registers a provisioner.
type ProvisionerSpec struct {
	// Name is the registry key (`pulumi`).
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Provisioner is the implementation.
	Provisioner Provisioner

	// Tools are the command-line tools the provisioner runs, each at the
	// version it needs, which a generated CI job installs (D47). Nil runs
	// none.
	Tools []CLITool
}

// resourceType is one resource type's properties schema and what
// registered it: `target "gcp"`, `DNS platform "cloudflare"`.
type resourceType struct {
	owner    string
	schema   []byte
	compiled *validator.Schema
}

// stackSpecs holds the stack model's registrations.
type stackSpecs struct {
	platforms     map[string]PlatformSpec
	connectors    map[string]ConnectorSpec
	targets       map[string]TargetSpec
	dnsPlatforms  map[string]DNSPlatformSpec
	provisioners  map[string]ProvisionerSpec
	ciRenderers   map[string]CIRendererSpec
	resourceTypes map[string]resourceType
}

func newStackSpecs() stackSpecs {
	return stackSpecs{
		platforms:     map[string]PlatformSpec{},
		connectors:    map[string]ConnectorSpec{},
		targets:       map[string]TargetSpec{},
		dnsPlatforms:  map[string]DNSPlatformSpec{},
		provisioners:  map[string]ProvisionerSpec{},
		ciRenderers:   map[string]CIRendererSpec{},
		resourceTypes: map[string]resourceType{},
	}
}

// stackKeyPattern is the shape of a platform, connector, target, DNS
// platform, provisioner or CI renderer name: lowercase words joined by dots
// or hyphens.
var stackKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9]*([.-][a-z0-9]+)*$`)

func checkStackKey(what, name string) error {
	if name == "" {
		return fmt.Errorf("registry: %s has no name", what)
	}
	if !stackKeyPattern.MatchString(name) {
		return fmt.Errorf("registry: %s name %q must be lowercase words joined by dots or hyphens", what, name)
	}
	return nil
}

// serverLanguages are the server languages a platform may declare.
var serverLanguages = []string{APILanguageGo, APILanguageRust, APILanguageTypeScript}

// RegisterPlatform adds a platform. It refuses a malformed or duplicate
// name, a missing or malformed provider, an unknown kind, a server, job or
// worker platform without languages or a database platform without
// dialects (or either with the other's list), an unknown or repeated
// language or dialect, an unknown artifact or one on a platform that runs
// no code, a settings schema that does not compile, an
// identity config that is not a server platform's JSON object, and a
// missing NameOf, AddressOf or Lower.
func (r *Registry) RegisterPlatform(spec PlatformSpec) error {
	if err := r.registrable("platform " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("platform", spec.Name); err != nil {
		return err
	}
	if _, dup := r.stack.platforms[spec.Name]; dup {
		return fmt.Errorf("registry: platform %q is already registered", spec.Name)
	}
	if spec.Provider == "" {
		return fmt.Errorf("registry: platform %q names no provider: say which provider it runs on (local, gcp, cloudflare), which tells the generic connector two platforms apart (D63)", spec.Name)
	}
	if err := checkStackKey("platform "+spec.Name+" provider", spec.Provider); err != nil {
		return err
	}
	switch spec.Kind {
	case ir.DeployableServer, ir.DeployableJob, ir.DeployableWorker, ir.DeployableRunner:
		if len(spec.Languages) == 0 {
			return fmt.Errorf("registry: %s platform %q declares no languages", spec.Kind, spec.Name)
		}
		if len(spec.Dialects) > 0 {
			return fmt.Errorf("registry: %s platform %q declares SQL dialects; only a database platform does", spec.Kind, spec.Name)
		}
		if err := checkList(string(spec.Kind)+" platform "+spec.Name, "language", spec.Languages, serverLanguages); err != nil {
			return err
		}
	case ir.DeployableDatabase:
		if len(spec.Dialects) == 0 {
			return fmt.Errorf("registry: database platform %q declares no SQL dialects", spec.Name)
		}
		if len(spec.Languages) > 0 {
			return fmt.Errorf("registry: database platform %q declares server languages; only a server, job, worker or runner platform does", spec.Name)
		}
		if err := checkList("database platform "+spec.Name, "SQL dialect", spec.Dialects, SQLDialectNames); err != nil {
			return err
		}
	case ir.DeployableSite:
		if len(spec.Languages) > 0 || len(spec.Dialects) > 0 {
			return fmt.Errorf("registry: site platform %q declares server languages or SQL dialects; a site serves files its own build writes (D55)", spec.Name)
		}
		if spec.PublicAddressOf == nil {
			return fmt.Errorf("registry: site platform %q has no PublicAddressOf: a site is always exposed, and the CORS field of each API it calls lists its origin (D55)", spec.Name)
		}
	case ir.DeployableBucket:
		if len(spec.Languages) > 0 || len(spec.Dialects) > 0 {
			return fmt.Errorf("registry: bucket platform %q declares server languages or SQL dialects; a bucket runs no code and hosts no schema (D54)", spec.Name)
		}
	default:
		return fmt.Errorf("registry: platform %q has deployable kind %q (want %s)", spec.Name, spec.Kind, deployableKindList())
	}
	if spec.NameOf == nil || spec.AddressOf == nil || spec.Lower == nil {
		return fmt.Errorf("registry: platform %q needs NameOf, AddressOf and Lower", spec.Name)
	}
	if spec.Artifact != "" {
		if !spec.Kind.RunsCode() {
			return fmt.Errorf("registry: %s platform %q declares an artifact; only a server, job, worker or runner platform builds one", spec.Kind, spec.Name)
		}
		if !spec.Artifact.Valid() {
			return fmt.Errorf("registry: platform %q declares artifact %q (want %s or %s)", spec.Name, spec.Artifact, ir.ArtifactImage, ir.ArtifactBundle)
		}
	}
	if spec.IdentityConfig != "" {
		var object map[string]any
		if spec.Kind != ir.DeployableServer {
			return fmt.Errorf("registry: %s platform %q declares an identity config; only a server platform does", spec.Kind, spec.Name)
		}
		if err := json.Unmarshal([]byte(spec.IdentityConfig), &object); err != nil || object == nil {
			return fmt.Errorf("registry: platform %q IdentityConfig is not a JSON object: %v", spec.Name, err)
		}
	}
	if spec.NoIdentity != "" {
		if spec.Kind != ir.DeployableServer {
			return fmt.Errorf("registry: %s platform %q says why it runs no identity runtime; only a server platform serves an API over the user model", spec.Kind, spec.Name)
		}
		if spec.IdentityConfig != "" {
			return fmt.Errorf("registry: platform %q declares an identity config and says it runs no identity runtime", spec.Name)
		}
	}
	if len(spec.Settings) > 0 {
		compiled, err := compileSchema(spec.Settings, "superschematic://platforms/"+spec.Name+"/settings.json")
		if err != nil {
			return fmt.Errorf("registry: platform %q Settings: %w", spec.Name, err)
		}
		spec.compiledSettings = compiled
	}
	spec.Languages = append([]string(nil), spec.Languages...)
	spec.Dialects = append([]string(nil), spec.Dialects...)
	r.stack.platforms[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

func checkList(owner, what string, list, known []string) error {
	seen := map[string]bool{}
	for _, item := range list {
		if !containsString(known, item) {
			return fmt.Errorf("registry: %s declares unknown %s %q (want one of %s)", owner, what, item, strings.Join(known, ", "))
		}
		if seen[item] {
			return fmt.Errorf("registry: %s declares %s %q twice", owner, what, item)
		}
		seen[item] = true
	}
	return nil
}

// RegisterConnector adds a connector. It refuses a malformed or duplicate
// name, an unknown edge kind, a missing platform name or Connect, a second
// connector for the same edge kind between the same two platforms, and a
// resource type schema that does not compile or that a target, a DNS
// platform or another connector registered differently. A generic
// connector names no platform, serves an http or a site edge, and is the
// only one of its edge kind. Finalize checks that a connector's platforms
// are registered and of the kinds the edge joins.
func (r *Registry) RegisterConnector(spec ConnectorSpec) error {
	if err := r.registrable("connector " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("connector", spec.Name); err != nil {
		return err
	}
	if _, dup := r.stack.connectors[spec.Name]; dup {
		return fmt.Errorf("registry: connector %q is already registered", spec.Name)
	}
	if !spec.Edge.Valid() {
		return fmt.Errorf("registry: connector %q has edge kind %q (want %s, %s, %s or %s)", spec.Name, spec.Edge, ir.EdgeSQL, ir.EdgeHTTP, ir.EdgeSite, ir.EdgeBucket)
	}
	switch {
	case spec.Generic && (spec.From != "" || spec.To != ""):
		return fmt.Errorf("registry: generic connector %q names a platform; it serves any two platforms of different providers that no connector names", spec.Name)
	case spec.Generic && spec.Edge != ir.EdgeHTTP && spec.Edge != ir.EdgeSite:
		return fmt.Errorf("registry: generic connector %q serves a %s edge; a generic connector serves an http or a site edge, and a %s edge across providers needs a connector that knows both", spec.Name, spec.Edge, spec.Edge)
	case !spec.Generic && (spec.From == "" || spec.To == ""):
		return fmt.Errorf("registry: connector %q needs a From and a To platform", spec.Name)
	}
	if spec.Connect == nil {
		return fmt.Errorf("registry: connector %q has no Connect function", spec.Name)
	}
	for _, other := range r.stack.connectors {
		if spec.Generic && other.Generic && other.Edge == spec.Edge {
			return fmt.Errorf("registry: generic connectors %q and %q both serve %s edges", other.Name, spec.Name, spec.Edge)
		}
		if !spec.Generic && !other.Generic && other.Edge == spec.Edge && other.From == spec.From && other.To == spec.To {
			return fmt.Errorf("registry: connectors %q and %q both connect %s to %s over %s", other.Name, spec.Name, spec.From, spec.To, spec.Edge)
		}
	}
	types, err := r.compileResourceTypes(fmt.Sprintf("connector %q", spec.Name), spec.ResourceTypes)
	if err != nil {
		return err
	}
	for typ, rt := range types {
		r.stack.resourceTypes[typ] = rt
	}
	spec.ResourceTypes = nil
	r.stack.connectors[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterTarget adds a target. It refuses a malformed or duplicate name,
// an unknown deployable kind or an empty platform name in Platforms, a
// values schema or resource type schema that does not compile, a resource
// type another target registered with a different schema, a policy rule
// without a name or Check, or with a repeated name, and a deploy seam it
// cannot use: State, Bootstrap, Migrations, Builder, CI, Jobs, Sites,
// Bundles or PreDestroy without a provisioner, and Bootstrap, Migrations,
// Builder, CI, Jobs, Sites, Bundles or PreDestroy without State.
// Finalize checks that the platforms, the DNS platform and the provisioner
// it names are registered.
func (r *Registry) RegisterTarget(spec TargetSpec) error {
	if err := r.registrable("target " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("target", spec.Name); err != nil {
		return err
	}
	if _, dup := r.stack.targets[spec.Name]; dup {
		return fmt.Errorf("registry: target %q is already registered", spec.Name)
	}
	platforms := make(map[ir.DeployableKind]string, len(spec.Platforms))
	for _, kind := range slices.Sorted(maps.Keys(spec.Platforms)) {
		platform := spec.Platforms[kind]
		if !kind.Valid() {
			return fmt.Errorf("registry: target %q names a platform for deployable kind %q (want %s)", spec.Name, kind, deployableKindList())
		}
		if platform == "" {
			return fmt.Errorf("registry: target %q names an empty platform for %s", spec.Name, kind)
		}
		platforms[kind] = platform
	}
	spec.Platforms = platforms
	databases := make(map[string]string, len(spec.DatabasePlatforms))
	for _, dialect := range slices.Sorted(maps.Keys(spec.DatabasePlatforms)) {
		platform := spec.DatabasePlatforms[dialect]
		if !slices.Contains(SQLDialectNames, dialect) {
			return fmt.Errorf("registry: target %q names a database platform for dialect %q (want %s)", spec.Name, dialect, strings.Join(SQLDialectNames, " or "))
		}
		if platform == "" {
			return fmt.Errorf("registry: target %q names an empty database platform for %s", spec.Name, dialect)
		}
		databases[dialect] = platform
	}
	spec.DatabasePlatforms = databases
	if len(spec.Values) > 0 {
		compiled, err := compileSchema(spec.Values, "superschematic://targets/"+spec.Name+"/values.json")
		if err != nil {
			return fmt.Errorf("registry: target %q Values: %w", spec.Name, err)
		}
		spec.compiledValues = compiled
	}
	seenRules := map[string]bool{}
	for _, rule := range spec.Policies {
		if rule.Name == "" || rule.Check == nil {
			return fmt.Errorf("registry: target %q has a policy rule without a name or a Check", spec.Name)
		}
		if seenRules[rule.Name] {
			return fmt.Errorf("registry: target %q has two policy rules named %q", spec.Name, rule.Name)
		}
		seenRules[rule.Name] = true
	}
	spec.Policies = append([]PolicyRule(nil), spec.Policies...)
	if err := checkDeploySeams(spec); err != nil {
		return err
	}
	types, err := r.compileResourceTypes(fmt.Sprintf("target %q", spec.Name), spec.ResourceTypes)
	if err != nil {
		return err
	}
	for typ, rt := range types {
		r.stack.resourceTypes[typ] = rt
	}
	spec.ResourceTypes = nil
	r.stack.targets[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// compileResourceTypes compiles the resource type schemas owner (`target
// "gcp"`) registers. It leaves out a type already registered with the same
// schema, and refuses an empty type name, a schema that does not compile
// and a type registered with a different schema.
func (r *Registry) compileResourceTypes(owner string, schemas map[string]json.RawMessage) (map[string]resourceType, error) {
	types := map[string]resourceType{}
	for _, typ := range keysOf(schemas) {
		if typ == "" {
			return nil, fmt.Errorf("registry: %s registers a resource type with no name", owner)
		}
		canonical, err := canonicalJSON(schemas[typ])
		if err != nil {
			return nil, fmt.Errorf("registry: %s resource type %s: %w", owner, typ, err)
		}
		if prev, ok := r.stack.resourceTypes[typ]; ok {
			if !bytes.Equal(prev.schema, canonical) {
				return nil, fmt.Errorf("registry: %s and %s register different schemas for resource type %s", prev.owner, owner, typ)
			}
			continue
		}
		compiled, err := compileSchema(canonical, "superschematic://resource-types/"+typ+".json")
		if err != nil {
			return nil, fmt.Errorf("registry: %s resource type %s: %w", owner, typ, err)
		}
		types[typ] = resourceType{owner: owner, schema: canonical, compiled: compiled}
	}
	return types, nil
}

// RegisterDNSPlatform adds a DNS platform. It refuses a malformed or
// duplicate name, the reserved name ir.ManualDNS, a values or resource
// type schema that does not compile, a resource type a target or another
// DNS platform registered with a different schema, and a missing Lower.
func (r *Registry) RegisterDNSPlatform(spec DNSPlatformSpec) error {
	if err := r.registrable("DNS platform " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("DNS platform", spec.Name); err != nil {
		return err
	}
	if spec.Name == ir.ManualDNS {
		return fmt.Errorf("registry: DNS platform name %q is reserved for a domain no DNS platform holds", ir.ManualDNS)
	}
	if _, dup := r.stack.dnsPlatforms[spec.Name]; dup {
		return fmt.Errorf("registry: DNS platform %q is already registered", spec.Name)
	}
	if spec.Lower == nil {
		return fmt.Errorf("registry: DNS platform %q has no Lower function", spec.Name)
	}
	if len(spec.Values) > 0 {
		compiled, err := compileSchema(spec.Values, "superschematic://dns-platforms/"+spec.Name+"/values.json")
		if err != nil {
			return fmt.Errorf("registry: DNS platform %q Values: %w", spec.Name, err)
		}
		spec.compiledValues = compiled
	}
	types, err := r.compileResourceTypes(fmt.Sprintf("DNS platform %q", spec.Name), spec.ResourceTypes)
	if err != nil {
		return err
	}
	for typ, rt := range types {
		r.stack.resourceTypes[typ] = rt
	}
	spec.ResourceTypes = nil
	r.stack.dnsPlatforms[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterProvisioner adds a provisioner. It refuses a malformed or
// duplicate name, a nil Provisioner, and a tool without a name or a
// version, or named twice.
func (r *Registry) RegisterProvisioner(spec ProvisionerSpec) error {
	if err := r.registrable("provisioner " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("provisioner", spec.Name); err != nil {
		return err
	}
	if _, dup := r.stack.provisioners[spec.Name]; dup {
		return fmt.Errorf("registry: provisioner %q is already registered", spec.Name)
	}
	if spec.Provisioner == nil {
		return fmt.Errorf("registry: provisioner %q has no Provisioner", spec.Name)
	}
	seenTools := map[string]bool{}
	for _, tool := range spec.Tools {
		if tool.Name == "" || tool.Version == "" {
			return fmt.Errorf("registry: provisioner %q has a tool without a name or a version", spec.Name)
		}
		if seenTools[tool.Name] {
			return fmt.Errorf("registry: provisioner %q names tool %q twice", spec.Name, tool.Name)
		}
		seenTools[tool.Name] = true
	}
	spec.Tools = append([]CLITool(nil), spec.Tools...)
	r.stack.provisioners[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// checkStackReferences is Finalize's part for the stack specs: every
// connector but a generic one, which names no platform, joins registered
// platforms of the kinds its edge joins, every platform a target names is
// registered and of the kind it places, each database platform it names
// for a dialect runs that dialect, and the DNS platform and
// provisioner a target names, when it names one, are registered.
func (r *Registry) checkStackReferences() error {
	for _, name := range keysOf(r.stack.connectors) {
		spec := r.stack.connectors[name]
		if spec.Generic {
			continue
		}
		toKind := ir.DeployableServer
		if spec.Edge == ir.EdgeSQL {
			toKind = ir.DeployableDatabase
		}
		fromKinds := []ir.DeployableKind{ir.DeployableServer, ir.DeployableJob, ir.DeployableWorker}
		if spec.Edge == ir.EdgeSQL {
			// An engine's runner reaches its engine database (D58).
			fromKinds = append(fromKinds, ir.DeployableRunner)
		}
		if spec.Edge == ir.EdgeSite {
			// A site edge runs from a site to a server (D55).
			fromKinds = []ir.DeployableKind{ir.DeployableSite}
		}
		if spec.Edge == ir.EdgeBucket {
			toKind = ir.DeployableBucket
		}
		for _, end := range []struct {
			role, platform string
			kinds          []ir.DeployableKind
		}{
			{"From", spec.From, fromKinds},
			{"To", spec.To, []ir.DeployableKind{toKind}},
		} {
			platform, ok := r.stack.platforms[end.platform]
			if !ok {
				return fmt.Errorf("registry: connector %q %s names platform %q, which is not registered (registered platforms: %v)", name, end.role, end.platform, keysOf(r.stack.platforms))
			}
			if !slices.Contains(end.kinds, platform.Kind) {
				want := make([]string, len(end.kinds))
				for i, kind := range end.kinds {
					want[i] = string(kind)
				}
				return fmt.Errorf("registry: connector %q %s names platform %q, a %s platform; a %s edge's %s is a %s", name, end.role, end.platform, platform.Kind, spec.Edge, strings.ToLower(end.role), strings.Join(want, " or a "))
			}
		}
	}
	for _, name := range keysOf(r.stack.targets) {
		spec := r.stack.targets[name]
		for _, kind := range ir.DeployableKinds() {
			platformName, ok := spec.Platforms[kind]
			if !ok {
				continue
			}
			platform, ok := r.stack.platforms[platformName]
			if !ok {
				return fmt.Errorf("registry: target %q names platform %q for %s, which is not registered (registered platforms: %v)", name, platformName, kind, keysOf(r.stack.platforms))
			}
			if platform.Kind != kind {
				return fmt.Errorf("registry: target %q names platform %q for %s, but it is a %s platform", name, platformName, kind, platform.Kind)
			}
		}
		for _, dialect := range keysOf(spec.DatabasePlatforms) {
			platformName := spec.DatabasePlatforms[dialect]
			platform, ok := r.stack.platforms[platformName]
			if !ok {
				return fmt.Errorf("registry: target %q names database platform %q for %s, which is not registered (registered platforms: %v)", name, platformName, dialect, keysOf(r.stack.platforms))
			}
			if platform.Kind != ir.DeployableDatabase || !slices.Contains(platform.Dialects, dialect) {
				return fmt.Errorf("registry: target %q names platform %q for %s databases, but it is a %s platform of dialects %v", name, platformName, dialect, platform.Kind, platform.Dialects)
			}
		}
		if spec.DNS != "" {
			if _, ok := r.stack.dnsPlatforms[spec.DNS]; !ok {
				return fmt.Errorf("registry: target %q names DNS platform %q, which is not registered (registered DNS platforms: %v)", name, spec.DNS, keysOf(r.stack.dnsPlatforms))
			}
		}
		if spec.Provisioner != "" {
			if _, ok := r.stack.provisioners[spec.Provisioner]; !ok {
				return fmt.Errorf("registry: target %q names provisioner %q, which is not registered (registered provisioners: %v)", name, spec.Provisioner, keysOf(r.stack.provisioners))
			}
		}
	}
	return nil
}

// Platform returns the platform registered under name.
func (r *Registry) Platform(name string) (PlatformSpec, bool) {
	spec, ok := r.stack.platforms[name]
	return spec, ok
}

// Platforms returns the registered platform names, sorted.
func (r *Registry) Platforms() []string { return keysOf(r.stack.platforms) }

// Connector returns the connector that realizes an edge of kind from
// platform from to platform to: the one that names both platforms, or else
// the generic connector of the edge kind when both platforms are registered
// and their providers differ (section 6.2, D63). Two platforms of one
// provider that no connector names have none.
func (r *Registry) Connector(kind ir.EdgeKind, from, to string) (ConnectorSpec, bool) {
	var generic *ConnectorSpec
	for _, name := range keysOf(r.stack.connectors) {
		spec := r.stack.connectors[name]
		switch {
		case spec.Edge != kind:
		case spec.Generic:
			generic = &spec
		case spec.From == from && spec.To == to:
			return spec, true
		}
	}
	if generic == nil {
		return ConnectorSpec{}, false
	}
	fromSpec, fromOK := r.stack.platforms[from]
	toSpec, toOK := r.stack.platforms[to]
	if !fromOK || !toOK || fromSpec.Provider == toSpec.Provider {
		return ConnectorSpec{}, false
	}
	return *generic, true
}

// Connectors returns the registered connector names, sorted.
func (r *Registry) Connectors() []string { return keysOf(r.stack.connectors) }

// Target returns the target registered under name.
func (r *Registry) Target(name string) (TargetSpec, bool) {
	spec, ok := r.stack.targets[name]
	return spec, ok
}

// Targets returns the registered target names, sorted.
func (r *Registry) Targets() []string { return keysOf(r.stack.targets) }

// DNSPlatform returns the DNS platform registered under name.
func (r *Registry) DNSPlatform(name string) (DNSPlatformSpec, bool) {
	spec, ok := r.stack.dnsPlatforms[name]
	return spec, ok
}

// DNSPlatforms returns the registered DNS platform names, sorted.
func (r *Registry) DNSPlatforms() []string { return keysOf(r.stack.dnsPlatforms) }

// Provisioner returns the provisioner registered under name.
func (r *Registry) Provisioner(name string) (ProvisionerSpec, bool) {
	spec, ok := r.stack.provisioners[name]
	return spec, ok
}

// Provisioners returns the registered provisioner names, sorted.
func (r *Registry) Provisioners() []string { return keysOf(r.stack.provisioners) }

// ValidateResource checks a resource's properties against the schema a
// target or a DNS platform registered for its type. ok is false when none
// registered one. References in the properties validate as strings, the form a
// provisioner renders them in.
func (r *Registry) ValidateResource(res *ir.Resource) (ok bool, err error) {
	rt, ok := r.stack.resourceTypes[res.Type]
	if !ok {
		return false, nil
	}
	props := map[string]any{}
	for key, value := range res.Properties {
		props[key] = referencesAsStrings(value)
	}
	return true, validateValue(rt.compiled, props)
}

// referencesAsStrings replaces each Output, Parameter and Concat in v with
// a placeholder string, the form a provisioner renders it in.
func referencesAsStrings(v any) any {
	switch v := v.(type) {
	case ir.Output:
		return "${" + v.Resource + "." + v.Name + "}"
	case ir.Parameter:
		return "${" + string(v) + "}"
	case ir.Concat:
		var b strings.Builder
		for _, part := range v {
			if s, ok := referencesAsStrings(part).(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, inner := range v {
			out[key] = referencesAsStrings(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = referencesAsStrings(inner)
		}
		return out
	}
	return v
}

// validateValue round-trips v through JSON, so Go numbers reach the
// validator in the form it reads, and validates it against schema. A nil
// map is an empty object. The error is one line: the validator's findings
// without its header.
func validateValue(schema *validator.Schema, v map[string]any) error {
	if v == nil {
		v = map[string]any{}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	doc, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	err = schema.Validate(doc)
	var verr *validator.ValidationError
	if !errors.As(err, &verr) {
		return err
	}
	var findings []string
	for _, line := range strings.Split(verr.Error(), "\n")[1:] {
		if line = strings.TrimPrefix(strings.TrimSpace(line), "- "); line != "" {
			findings = append(findings, line)
		}
	}
	if len(findings) == 0 {
		return err
	}
	return errors.New(strings.Join(findings, "; "))
}

func canonicalJSON(data []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

func keysOf[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// deployableKindList names the deployable kinds for an error message:
// "database, server or job".
func deployableKindList() string {
	kinds := ir.DeployableKinds()
	names := make([]string, len(kinds))
	for i, kind := range kinds {
		names[i] = string(kind)
	}
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}
