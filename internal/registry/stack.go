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

// PlatformSpec realizes one deployable kind on one runtime: Cloud Run
// servers, Cloud SQL databases, local processes (section 6.1).
type PlatformSpec struct {
	// Name is the registry key (`gcp.cloudrun`): lowercase words joined by
	// dots or hyphens.
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Kind is the deployable kind the platform realizes.
	Kind ir.DeployableKind

	// Languages are the server languages a server platform runs, as
	// `outputs.api.language` spells them (APILanguageGo, ...). Required
	// for a server platform, refused for a database platform.
	Languages []string

	// Dialects are the SQL dialects a database platform runs
	// (SQLDialectPostgres, ...), in order of preference. Required for a
	// database platform, refused for a server platform.
	Dialects []string

	// Settings is the JSON Schema of a deployable's settings on this
	// platform (`minInstances`, `tier`). Nil accepts no settings.
	Settings json.RawMessage

	// NameOf returns the deployable's name in the environment, a string or
	// a value that references the environment's parameters.
	NameOf func(PlatformContext) any

	// AddressOf returns how an edge reaches the deployable: a value that
	// usually references an output of one of the deployable's nodes.
	AddressOf func(PlatformContext) any

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
	// call within one server carries no credential and has none.
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

	// From and To name the platforms at the two ends. From is a server
	// platform; To is a database platform for sql and a server platform for
	// http.
	From string
	To   string

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

	// Builder builds the images of the target's servers from the
	// Dockerfiles a stack's build writes (sections 8.2 and 11.2). Nil
	// builds none, and every server's image comes from --image or the
	// deploy manifest.
	Builder ImageBuilder

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

	// Backend is where the provisioner keeps the environment's state, as
	// the target's bootstrap created it.
	Backend StateBackend

	// Env holds the platform credentials the run needs, keyed by the
	// environment variable their provider reads (Credential.Env). The
	// provisioner hands them to its tool's process for this run only, and
	// never writes them to its config, its program, a log or a file.
	Env map[string]string
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
// name, an unknown kind, a server platform without languages or a database
// platform without dialects (or either with the other's list), an unknown
// or repeated language or dialect, a settings schema that does not compile,
// and a missing NameOf, AddressOf or Lower.
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
	switch spec.Kind {
	case ir.DeployableServer:
		if len(spec.Languages) == 0 {
			return fmt.Errorf("registry: server platform %q declares no languages", spec.Name)
		}
		if len(spec.Dialects) > 0 {
			return fmt.Errorf("registry: server platform %q declares SQL dialects; only a database platform does", spec.Name)
		}
		if err := checkList("server platform "+spec.Name, "language", spec.Languages, serverLanguages); err != nil {
			return err
		}
	case ir.DeployableDatabase:
		if len(spec.Dialects) == 0 {
			return fmt.Errorf("registry: database platform %q declares no SQL dialects", spec.Name)
		}
		if len(spec.Languages) > 0 {
			return fmt.Errorf("registry: database platform %q declares server languages; only a server platform does", spec.Name)
		}
		if err := checkList("database platform "+spec.Name, "SQL dialect", spec.Dialects, SQLDialectNames); err != nil {
			return err
		}
	default:
		return fmt.Errorf("registry: platform %q has deployable kind %q (want %s or %s)", spec.Name, spec.Kind, ir.DeployableDatabase, ir.DeployableServer)
	}
	if spec.NameOf == nil || spec.AddressOf == nil || spec.Lower == nil {
		return fmt.Errorf("registry: platform %q needs NameOf, AddressOf and Lower", spec.Name)
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
// name, an unknown edge kind, a missing platform name or Connect, and a
// second connector for the same edge kind between the same two platforms.
// Finalize checks that both platforms are registered and of the kinds the
// edge joins.
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
		return fmt.Errorf("registry: connector %q has edge kind %q (want %s or %s)", spec.Name, spec.Edge, ir.EdgeSQL, ir.EdgeHTTP)
	}
	if spec.From == "" || spec.To == "" {
		return fmt.Errorf("registry: connector %q needs a From and a To platform", spec.Name)
	}
	if spec.Connect == nil {
		return fmt.Errorf("registry: connector %q has no Connect function", spec.Name)
	}
	for _, other := range r.stack.connectors {
		if other.Edge == spec.Edge && other.From == spec.From && other.To == spec.To {
			return fmt.Errorf("registry: connectors %q and %q both connect %s to %s over %s", other.Name, spec.Name, spec.From, spec.To, spec.Edge)
		}
	}
	r.stack.connectors[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// RegisterTarget adds a target. It refuses a malformed or duplicate name,
// an unknown deployable kind or an empty platform name in Platforms, a
// values schema or resource type schema that does not compile, a resource
// type another target registered with a different schema, a policy rule
// without a name or Check, or with a repeated name, and a deploy seam it
// cannot use: State, Bootstrap, Migrations, Builder or CI without a
// provisioner, and Bootstrap, Migrations, Builder or CI without State.
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
			return fmt.Errorf("registry: target %q names a platform for deployable kind %q (want %s or %s)", spec.Name, kind, ir.DeployableDatabase, ir.DeployableServer)
		}
		if platform == "" {
			return fmt.Errorf("registry: target %q names an empty platform for %s", spec.Name, kind)
		}
		platforms[kind] = platform
	}
	spec.Platforms = platforms
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
// connector joins registered platforms of the kinds its edge joins, every
// platform a target names is registered and of the kind it places, and the
// DNS platform and provisioner a target names, when it names one, are
// registered.
func (r *Registry) checkStackReferences() error {
	for _, name := range keysOf(r.stack.connectors) {
		spec := r.stack.connectors[name]
		toKind := ir.DeployableServer
		if spec.Edge == ir.EdgeSQL {
			toKind = ir.DeployableDatabase
		}
		for _, end := range []struct {
			role, platform string
			kind           ir.DeployableKind
		}{{"From", spec.From, ir.DeployableServer}, {"To", spec.To, toKind}} {
			platform, ok := r.stack.platforms[end.platform]
			if !ok {
				return fmt.Errorf("registry: connector %q %s names platform %q, which is not registered (registered platforms: %v)", name, end.role, end.platform, keysOf(r.stack.platforms))
			}
			if platform.Kind != end.kind {
				return fmt.Errorf("registry: connector %q %s names platform %q, a %s platform; a %s edge's %s is a %s", name, end.role, end.platform, platform.Kind, spec.Edge, strings.ToLower(end.role), end.kind)
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
// platform from to platform to.
func (r *Registry) Connector(kind ir.EdgeKind, from, to string) (ConnectorSpec, bool) {
	for _, spec := range r.stack.connectors {
		if spec.Edge == kind && spec.From == from && spec.To == to {
			return spec, true
		}
	}
	return ConnectorSpec{}, false
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
