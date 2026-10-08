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
	// server serves, sorted by name. A job's is its API, which it serves
	// in a callee's callers field (D52).
	Services []ServiceRef `json:"services"`

	// Calls are the API services a server calls: the union of the `calls`
	// of the APIs it serves, sorted by name. A job's are its API's.
	Calls []ServiceRef `json:"calls,omitempty"`

	// Language is a server's or a job's language; Dialect is the SQL
	// dialect a database runs, the first of its platform's dialects every
	// hosted schema supports.
	Language string `json:"language,omitempty"`
	Dialect  string `json:"dialect,omitempty"`

	// Job is what a job runs and when; nil for every other kind.
	Job *ResolvedJob `json:"job,omitempty"`

	// Exposed is true for a server reachable from outside the environment.
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

	// Bindings bind every config field of a server or a job, sorted by
	// field. An optional field with no value and no default has none.
	Bindings []*Binding `json:"bindings,omitempty"`
}

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
	*d = ResolvedDeployable(p)
	return nil
}

// Edge is a need of one deployable met by another (section 3.3).
type Edge struct {
	// ID is `<kind>:<from>-><service>`, unique in the environment.
	ID string `json:"id"`

	// Kind is sql or http.
	Kind EdgeKind `json:"kind"`

	// From is the server or job with the need; To is the deployable that
	// meets it.
	From string `json:"from"`
	To   string `json:"to"`

	// Service is the DB service the sql edge connects to, or the API
	// service the http edge calls.
	Service ServiceRef `json:"service"`

	// Connector is the registered connector that realizes the edge.
	Connector string `json:"connector"`

	// Field is the config field of From the edge's derived binding fills.
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

	// Edges are the edges a callers field's value comes from: the http
	// edges to CallersOf from other servers, sorted. None means no server
	// calls the API, and the field's value has no issuers.
	Edges []string `json:"edges,omitempty"`

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
// phase runs there and holds no resources.
type DeployStep struct {
	// Step is the step's kind.
	Step DeployStepKind `json:"step"`

	// Migration is a migrate step's phase.
	Migration MigrationPhase `json:"migration,omitempty"`

	// Wave numbers a rollout step's wave from 1.
	Wave int `json:"wave,omitempty"`

	// Deployables are the databases a migrate step migrates, or the
	// servers and jobs a rollout wave rolls out, sorted.
	Deployables []string `json:"deployables,omitempty"`

	// Resources are the IDs of the resources the step applies, sorted.
	Resources []string `json:"resources,omitempty"`
}
