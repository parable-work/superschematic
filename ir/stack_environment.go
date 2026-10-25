package ir

import (
	"encoding/json"
	"fmt"
)

// ResolvedEnvironmentVersion is the version of the environment.json format
// ResolvedEnvironment encodes to.
const ResolvedEnvironmentVersion = 1

// ResolvedEnvironment is one environment of a stack with every need wired
// to something that provides it (docs/stack-model.md, section 5.1). It is
// what `<output-root>/stack/<stack>/<environment>/environment.json`
// holds, and the only input renderers and provisioners read: none of them
// recomputes an address or a name.
type ResolvedEnvironment struct {
	// Version is ResolvedEnvironmentVersion.
	Version int `json:"version"`

	// Stack and Environment name what was resolved.
	Stack       string `json:"stack"`
	Environment string `json:"environment"`

	// Extends names the parent environment, whose infrastructure the
	// members of a parameterized environment share.
	Extends string `json:"extends,omitempty"`

	// Target is the environment's target; Provisioner is the provisioner
	// the target names, if any.
	Target      string `json:"target"`
	Provisioner string `json:"provisioner,omitempty"`

	// Values are the target's environment values, with the parent's
	// merged in.
	Values map[string]any `json:"values,omitempty"`

	// Parameters are the environment's parameters, the parent's first.
	Parameters []string `json:"parameters,omitempty"`

	// Domain is where exposed servers are reached; DNS holds its records.
	Domain string       `json:"domain,omitempty"`
	DNS    *ResolvedDNS `json:"dns,omitempty"`

	// Deployables are sorted by name.
	Deployables []*ResolvedDeployable `json:"deployables"`

	// Disabled are the deployables the environment's settings turn off
	// and so places nowhere, sorted by name: a memory store with `enabled:
	// false`, which no platform realizes and no edge reaches, so the field
	// of every workload that lists it is unset and each opens it disabled
	// (D75).
	Disabled []*DisabledDeployable `json:"disabled,omitempty"`

	// Edges are sorted by ID.
	Edges []*Edge `json:"edges,omitempty"`

	// Secrets are the secrets the servers read, sorted by ID. Their values
	// are entered with `stack secrets set` and never written to a file.
	Secrets []*StackSecret `json:"secrets,omitempty"`

	// Resources is the graph the platforms, connectors and DNS platform
	// lowered the environment to.
	Resources *ResourceGraph `json:"resources"`

	// DeployOrder is the order the steps run in (section 5.3).
	DeployOrder []*DeployStep `json:"deployOrder"`
}

// Deployable returns the deployable named name, or nil.
func (e *ResolvedEnvironment) Deployable(name string) *ResolvedDeployable {
	for _, d := range e.Deployables {
		if d.Name == name {
			return d
		}
	}
	return nil
}

// ResolvedDeployable is a deployable placed on a platform.
type ResolvedDeployable struct {
	// Name is the declared deployable's name, or for a default deployable
	// the name of the service it hosts or serves.
	Name string `json:"name"`

	// Kind is the deployable's kind.
	Kind DeployableKind `json:"kind"`

	// Declared is true for a declared deployable and false for a default.
	Declared bool `json:"declared,omitempty"`

	// Platform is the registered platform the deployable is placed on.
	Platform string `json:"platform"`

	// Services are the DB services a database hosts or the API services a
	// server serves, sorted by name. A job's or a worker's is its API,
	// which it serves in a callee's callers field (D52, D53).
	Services []ServiceRef `json:"services"`

	// Calls are the API services a server calls: the union of the `calls`
	// of the APIs it serves, sorted by name. A job's or a worker's are its
	// API's.
	Calls []ServiceRef `json:"calls,omitempty"`

	// Buckets are the Bucket services a server reaches: the union of the
	// `buckets` of the APIs it serves, sorted by name. A job's are its
	// API's (D54).
	Buckets []ServiceRef `json:"buckets,omitempty"`

	// MemoryStores are the MemoryStore services a server, a job or a
	// worker reaches: the union of the `memoryStores` of the APIs it
	// serves, a rateLimitStore among them, sorted by name, less those the
	// environment turns off (D75).
	MemoryStores []ServiceRef `json:"memoryStores,omitempty"`

	// Language is a server's, a job's or a worker's language; Dialect is the SQL
	// dialect a database runs, the first of its platform's dialects every
	// hosted schema supports.
	Language string `json:"language,omitempty"`
	Dialect  string `json:"dialect,omitempty"`

	// Artifact is what a deploy builds, pins and records for a server, a
	// job or a worker, as its platform says: an image, or a bundle of files
	// (D64). Empty for every other kind.
	Artifact Artifact `json:"artifact,omitempty"`

	// Job is what a job runs and when; nil for every other kind.
	Job *ResolvedJob `json:"job,omitempty"`

	// Site is what a site builds and serves; nil for every other kind
	// (D55).
	Site *ResolvedSite `json:"site,omitempty"`

	// Worker is what a worker handles and how many of it run; nil for
	// every other kind (D53).
	Worker *ResolvedWorker `json:"worker,omitempty"`

	// Runner is the engine database an engine's runner runs over, and the
	// API whose language it is written in; nil for every other kind (D58).
	Runner *ResolvedRunner `json:"runner,omitempty"`

	// Deriver is what a deriver fills and how many of it run; nil for
	// every other kind (D74).
	Deriver *ResolvedDeriver `json:"deriver,omitempty"`

	// Exposed is true for a server reachable from outside the environment,
	// and for every site.
	Exposed bool `json:"exposed,omitempty"`

	// Settings are the platform settings, the parent environment's merged
	// under the environment's own.
	Settings map[string]any `json:"settings,omitempty"`

	// ResourceName is the deployable's name in the environment, as its
	// platform names it. Under a parameter it references the parameter.
	ResourceName any `json:"resourceName"`

	// Address is how an edge reaches the deployable, as its platform
	// addresses it.
	Address any `json:"address,omitempty"`

	// PublicAddress is where a browser reaches an exposed deployable from
	// outside the environment, as its platform's PublicAddressOf gives it:
	// the base URL a site edge to a server derives, and a site's origin,
	// which the CORS field of each API it calls lists (D55). Empty for a
	// deployable that is not exposed, or whose platform gives none.
	PublicAddress any `json:"publicAddress,omitempty"`

	// Bindings bind every config field of a server or a job, sorted by
	// field. An optional field with no value and no default has none.
	Bindings []*Binding `json:"bindings,omitempty"`
}

// HasImage reports whether a deploy builds, pins and records an image for
// d: a server, a job or a worker whose artifact is not a bundle. One
// resolved before D64 records no artifact, and has an image.
func (d *ResolvedDeployable) HasImage() bool {
	return d.Kind.RunsCode() && d.Artifact != ArtifactBundle
}

// HasBundle reports whether a deploy builds, pins and records a bundle for
// d: a server, a job or a worker whose platform builds one (D64).
func (d *ResolvedDeployable) HasBundle() bool {
	return d.Kind.RunsCode() && d.Artifact == ArtifactBundle
}

// Artifact is what a deploy builds from a server's, a job's or a worker's
// code, pins in its nodes and records in the deploy manifest (D64).
type Artifact string

const (
	// ArtifactImage is a container image, which the target's ImageBuilder
	// builds from the Dockerfile the stack's build writes, and which the
	// deploy pins by digest (D46). The default.
	ArtifactImage Artifact = "image"

	// ArtifactBundle is a directory of files that `bun bundle.ts <dir>`
	// writes in the deployable's package, which the target's
	// BundlePublisher keeps under their digest, and which the deploy pins
	// with BundleDigestToken and BundleFileToken and places beside the
	// provisioner's program (D64).
	ArtifactBundle Artifact = "bundle"
)

// Valid reports whether a is an artifact v1 knows.
func (a Artifact) Valid() bool { return a == ArtifactImage || a == ArtifactBundle }

// ResolvedJob is a job's run in one environment (D52): the method of its
// API's Jobs interface it calls, and the schedule, time zone, timeout and
// retries that apply, each the decorator's unless the environment's
// settings change it.
type ResolvedJob struct {
	// API is the API service that declares the job; Name is its `@job`
	// class's name.
	API  string `json:"api"`
	Name string `json:"name"`

	// Schedule is the five-field cron the job runs on in this environment,
	// in TimeZone. Empty runs it only on demand: it declares none, or the
	// environment turns it off, or the environment is parameterized and
	// turns none on.
	Schedule string `json:"schedule,omitempty"`
	TimeZone string `json:"timeZone"`

	// TimeoutSeconds bounds one run; Retries is how many times a failed
	// run is run again.
	TimeoutSeconds int `json:"timeoutSeconds"`
	Retries        int `json:"retries,omitempty"`

	// OnDeploy runs the job once on every deploy, in the deploy order's
	// jobs step, after the last rollout wave and before the contract phase
	// (D52, amended). A deploy waits for the run, its retries included,
	// and fails when its last try fails.
	OnDeploy bool `json:"onDeploy,omitempty"`
}

// ResolvedWorker is a worker's run in one environment (D53): the method of
// its API's Workers interface that handles a message, the queue it claims
// from, and how many instances run, each handling how many messages at a
// time.
type ResolvedWorker struct {
	// API is the API service that declares the worker; Name is its
	// `@worker` class's name.
	API  string `json:"api"`
	Name string `json:"name"`

	// Queue is the `@queue` class whose messages the worker handles, and
	// Database the DB service that declares it: the database its API
	// connects to.
	Queue    string `json:"queue"`
	Database string `json:"database"`

	// Instances is how many instances the environment runs: zero when its
	// settings turn the worker off. Concurrency is how many messages each
	// handles at a time.
	Instances   int `json:"instances"`
	Concurrency int `json:"concurrency"`

	// MaxInstances is the most instances its settings let a target run as
	// the queue's depth grows, Instances the fewest; zero when they set
	// none, a fixed count (D53, amended). No target scales a worker yet.
	MaxInstances int `json:"maxInstances,omitempty"`

	// GraceSeconds is how long a stopping instance lets its running
	// handlers finish before it gives their messages back.
	GraceSeconds int `json:"graceSeconds"`
}

// ResolvedRunner is what an engine's runner runs over (D58): the engine
// database, a DB service whose types compose behaviors, and the API, the
// first by name that connects to it, whose language the runner is written
// in, with the engine archive that API's servers link.
type ResolvedRunner struct {
	// Database is the DB service; API is the API service.
	Database string `json:"database"`
	API      string `json:"api"`

	// Instances is how many instances the environment runs: one, which
	// runs while it holds the runner's lock, or zero when its settings
	// turn the runner off.
	Instances int `json:"instances"`
}

// ResolvedDeriver is a deriver's run in one environment (D74): the member
// of its API's Derivers that answers its fields, the engine types whose
// Derived fields it fills, and whether it runs.
type ResolvedDeriver struct {
	// API is the API service that declares the deriver; Name is its
	// `@deriver` class's name.
	API  string `json:"api"`
	Name string `json:"name"`

	// Database is the engine database its API connects to, and Schemas
	// the engine types of it whose Derived fields it fills, as the
	// decorator names them; empty is every type that composes Derived,
	// which the API's build lists.
	Database string   `json:"database"`
	Schemas  []string `json:"schemas,omitempty"`

	// Instances is how many instances the environment runs: one, or zero
	// when its settings turn the deriver off. Concurrency is how many
	// answers it runs at a time, and BatchSize how many stale fields a
	// pull asks for.
	Instances   int `json:"instances"`
	Concurrency int `json:"concurrency"`
	BatchSize   int `json:"batchSize"`
}

// DisabledDeployable is a deployable an environment's settings turn off,
// which no platform places (D75): a memory store with `enabled: false`.
type DisabledDeployable struct {
	// Name is the deployable's name: the MemoryStore service's.
	Name string `json:"name"`

	// Kind is the deployable's kind, memoryStore.
	Kind DeployableKind `json:"kind"`

	// Services are the services it would host: the MemoryStore service.
	Services []ServiceRef `json:"services"`

	// From names the environment of the chain whose settings turned it
	// off.
	From string `json:"from"`
}

// UnmarshalJSON decodes a deployable and turns the references in its name
// and address back into Output, Parameter and Concat values.
func (d *ResolvedDeployable) UnmarshalJSON(data []byte) error {
	type plain ResolvedDeployable
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var err error
	if p.ResourceName, err = DecodeValue(p.ResourceName); err != nil {
		return fmt.Errorf("deployable %s: resourceName: %w", p.Name, err)
	}
	if p.Address, err = DecodeValue(p.Address); err != nil {
		return fmt.Errorf("deployable %s: address: %w", p.Name, err)
	}
	if p.PublicAddress, err = DecodeValue(p.PublicAddress); err != nil {
		return fmt.Errorf("deployable %s: publicAddress: %w", p.Name, err)
	}
	*d = ResolvedDeployable(p)
	return nil
}

// Edge is a need of one deployable met by another (section 3.3).
type Edge struct {
	// ID is `<kind>:<from>-><service>`, unique in the environment.
	ID string `json:"id"`

	// Kind is sql, http, site, bucket or memoryStore.
	Kind EdgeKind `json:"kind"`

	// From is the server, job or site with the need; To is the deployable
	// that meets it.
	From string `json:"from"`
	To   string `json:"to"`

	// Service is the DB service the sql edge connects to, the API service
	// the http or site edge calls, the Bucket service the bucket edge
	// reaches, or the MemoryStore service the memoryStore edge reaches.
	Service ServiceRef `json:"service"`

	// Connector is the registered connector that realizes the edge.
	Connector string `json:"connector"`

	// Field is the config field of From the edge's derived binding fills.
	// A site edge's is the API service's name, the key of the API in the
	// site's config (SiteConfigPath, D55).
	Field string `json:"field"`
}

// EdgeID returns the ID of the edge of kind from a deployable to a
// service.
func EdgeID(kind EdgeKind, from, service string) string {
	return string(kind) + ":" + from + "->" + service
}

// BindingSource is where a config field's value comes from (section 5.1).
type BindingSource string

const (
	// BindingLiteral is a value the environment sets, or the field's
	// default.
	BindingLiteral BindingSource = "literal"

	// BindingSecret is a secret the platform stores and a person enters.
	BindingSecret BindingSource = "secret"

	// BindingDerived is the value an edge's connector derives, or for an
	// API's callers field the value the connectors of the http edges to
	// the API derive together.
	BindingDerived BindingSource = "derived"

	// BindingParameter is one of the environment's parameters, which the
	// deploy run supplies.
	BindingParameter BindingSource = "parameter"
)

// Binding binds one config field of a server.
type Binding struct {
	// Field is the config field's name.
	Field string `json:"field"`

	// Source is where the value comes from.
	Source BindingSource `json:"source"`

	// Value is a literal's value or a derived binding's value, which may
	// hold references.
	Value any `json:"value,omitempty"`

	// Default marks a literal taken from the field's default.
	Default bool `json:"default,omitempty"`

	// Secret is a secret binding's StackSecret ID.
	Secret string `json:"secret,omitempty"`

	// Edge is a derived binding's edge ID.
	Edge string `json:"edge,omitempty"`

	// CallersOf is the API service a callers field belongs to: a derived
	// binding, an ir.ServiceAuth, that the server of an API with a service
	// clause verifies its callers against (CallersField).
	CallersOf string `json:"callersOf,omitempty"`

	// CORSOf is the API service a CORS field belongs to: a derived
	// binding, an ir.CORSPolicy, that lists the origins of the sites that
	// call the API, which its server answers CORS for (CORSField, D55).
	CORSOf string `json:"corsOf,omitempty"`

	// Edges are the edges a callers field's value comes from: the http
	// edges to CallersOf from other servers, sorted. None means no server
	// calls the API, and the field's value has no issuers. A CORS field's
	// are the site edges to CORSOf, sorted.
	Edges []string `json:"edges,omitempty"`

	// IdentityOf is the API service an identity config field belongs to
	// (IdentityConfigField, D50): a literal, the environment's env setting
	// or else the platform's identity config, or a parameter.
	IdentityOf string `json:"identityOf,omitempty"`

	// Parameter is a parameter binding's parameter.
	Parameter string `json:"parameter,omitempty"`
}

// UnmarshalJSON decodes a binding and turns the references in its value
// back into Output, Parameter and Concat values.
func (b *Binding) UnmarshalJSON(data []byte) error {
	type plain Binding
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var err error
	if p.Value, err = DecodeValue(p.Value); err != nil {
		return fmt.Errorf("binding %s: value: %w", p.Field, err)
	}
	*b = Binding(p)
	return nil
}

// StackSecret is one secret of an environment. It is identified by the
// type that declares the `Secret<T>` field and the field's name, not by the
// server that reads it, so servers that include the same declared field
// share it (section 4.2).
type StackSecret struct {
	// ID is `<Type>.<Field>`.
	ID string `json:"id"`

	// Type declares the field; Field is its name.
	Type  string `json:"type"`
	Field string `json:"field"`

	// Readers are the servers and jobs whose config includes the field,
	// sorted.
	Readers []string `json:"readers"`
}

// SecretID returns the ID of the secret field declares on typeName.
func SecretID(typeName, field string) string {
	return typeName + "." + field
}

// ManualDNS is the DNS platform of an environment whose domain no
// registered DNS platform holds: `stack plan` prints the records to
// create, and nothing writes them (section 6.9).
const ManualDNS = "manual"

// ResolvedDNS is where an environment's domain records go.
type ResolvedDNS struct {
	// Platform is the DNS platform, or ManualDNS.
	Platform string `json:"platform"`

	// Values are the DNS platform's values (a zone).
	Values map[string]any `json:"values,omitempty"`

	// Credentials are the secrets the DNS platform's provider reads when
	// the provisioner writes the records, in the order the platform names
	// them.
	Credentials []*DNSCredential `json:"credentials,omitempty"`

	// Records are the records exposure needs, in the neutral shape the
	// DNS platform lowers, sorted by deployable and then as produced.
	Records []*DNSRecord `json:"records,omitempty"`
}

// DNSCredential is a secret a DNS platform's provider reads when the
// provisioner runs, such as an API token scoped to the zone (section 6.9).
// The target's bootstrap asks the engineer for it and stores it in the
// target's secret store under Secret, readable by the accounts that plan
// and deploy. A run reads it there and hands its value to the provisioner
// in the environment variable Env, so the value never reaches the resource
// graph or a file.
type DNSCredential struct {
	// Secret is the secret's name in the target's secret store.
	Secret string `json:"secret"`

	// Env is the environment variable the provider reads it from.
	Env string `json:"env"`

	// Description says what the engineer enters, for bootstrap's prompt.
	Description string `json:"description"`
}

// DNSRecord is one record in a neutral shape (section 6.9).
type DNSRecord struct {
	// Name is the record's name, which may reference a parameter.
	Name any `json:"name"`

	// Type is the record type: A, AAAA, CNAME, TXT.
	Type string `json:"type"`

	// Value is the record's value, which may reference an output.
	Value any `json:"value"`

	// Deployable is the exposed server the record is for. Resolution
	// sets it.
	Deployable string `json:"deployable,omitempty"`
}

// UnmarshalJSON decodes a record and turns the references in its name and
// value back into Output, Parameter and Concat values.
func (r *DNSRecord) UnmarshalJSON(data []byte) error {
	type plain DNSRecord
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var err error
	if p.Name, err = DecodeValue(p.Name); err != nil {
		return fmt.Errorf("dns record: name: %w", err)
	}
	if p.Value, err = DecodeValue(p.Value); err != nil {
		return fmt.Errorf("dns record: value: %w", err)
	}
	*r = DNSRecord(p)
	return nil
}

// DeployStepKind is one step of the deploy order (section 5.3).
type DeployStepKind string

const (
	// StepInfrastructure applies the infrastructure resources.
	StepInfrastructure DeployStepKind = "infrastructure"

	// StepMigrate runs one migration phase on the databases.
	StepMigrate DeployStepKind = "migrate"

	// StepRollout applies one wave of servers and jobs: the callees of
	// each roll out in an earlier wave.
	StepRollout DeployStepKind = "rollout"

	// StepJobs runs, once each, the jobs that run on every deploy, after
	// the last rollout wave, so every server of the new version runs, and
	// before the contract phase, so a job may still read what the contract
	// drops (D52, amended). It holds no resources: the deploy runs each
	// job through its target's job runner, and stack dev runs it itself.
	StepJobs DeployStepKind = "jobs"

	// StepExposure applies the exposure resources.
	StepExposure DeployStepKind = "exposure"
)

// MigrationPhase is the half of a migration a migrate step runs.
type MigrationPhase string

const (
	// MigrationExpand runs before the rollout; the running servers
	// survive it.
	MigrationExpand MigrationPhase = "expand"

	// MigrationContract runs after the rollout, once no server of the
	// previous version runs.
	MigrationContract MigrationPhase = "contract"
)

// DeployStep is one step of the deploy order. The provisioner applies a
// step's resources; a migrate step names the databases whose migration
// phase runs there and holds no resources, and a jobs step the jobs that
// run there.
type DeployStep struct {
	// Step is the step's kind.
	Step DeployStepKind `json:"step"`

	// Migration is a migrate step's phase.
	Migration MigrationPhase `json:"migration,omitempty"`

	// Wave numbers a rollout step's wave from 1.
	Wave int `json:"wave,omitempty"`

	// Deployables are the databases a migrate step migrates, the servers
	// and jobs a rollout wave rolls out, or the jobs a jobs step runs,
	// sorted.
	Deployables []string `json:"deployables,omitempty"`

	// Resources are the IDs of the resources the step applies, sorted.
	Resources []string `json:"resources,omitempty"`
}
