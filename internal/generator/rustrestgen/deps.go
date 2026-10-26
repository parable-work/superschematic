package rustrestgen

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The implementation side of a Rust API (D57; docs/stack-model.md, section
// 8.5): src/deps.rs declares Deps, what the implementation is built from,
// Handlers, what its create returns, and Implementations::new, which joins
// the handlers with what the server's entrypoint builds, the twin of the
// Go API's deps.go and the TypeScript API's deps.ts; the scaffold writes
// the implementation's crate once, at the naming file's
// [implementation_paths] rust template.

// TracingVersion is the tracing crate Deps.span is of: the HTTP runtime's
// version, without its default features, as the runtime takes it.
const TracingVersion = "0.1.41"

// DepsInfo is what Deps holds beside the config and the span: a pool of
// the API's database, a Rust SDK client per API it calls, and the HTTP
// runtime's Bucket per bucket it lists (D54).
type DepsInfo struct {
	// Database is the DB service whose pool Deps.db is: the API's authDb,
	// or its one DB-kind dependency. Empty when the API has neither.
	Database string

	// Calls are the API's calls entries, in order.
	Calls []DepsCall

	// Buckets are the API's buckets entries, in order (D54).
	Buckets []DepsBucket

	// MemoryStores are the API's memoryStores entries, its rateLimitStore
	// among them, in order (D75), and RateLimitStore the service of its
	// rateLimitStore, "" without one: the store its server's entrypoint
	// counts its @rateLimit routes in.
	MemoryStores   []DepsMemoryStore
	RateLimitStore string

	// DatabaseTypesCrate is the Rust types crate of Database when the API
	// declares a worker (D65): it declares the queues' messages, which the
	// Workers trait takes, and holds their descriptors in src/queues.rs,
	// which a worker claims with and an implementation enqueues with. The
	// API crate re-exports it as database_types (DatabaseTypesModule), and
	// DatabaseTypesPath is its directory relative to the API crate's,
	// which Cargo.toml depends on. Both are empty otherwise.
	DatabaseTypesCrate string
	DatabaseTypesPath  string

	// Engine is the engine of the API's engine database (D58), which Deps
	// holds when the database's types compose behaviors; nil otherwise.
	Engine *DepsEngine
}

// DepsEngine is the engine Deps holds: the engine database's, opened in
// the background by the entrypoint, a superschematic_engine::background::
// Background from the engine's crate, Crate, at Path relative to the API
// crate's directory ([paths] engine_rust).
type DepsEngine struct {
	Database  string
	Namespace string
	Crate     string
	Path      string

	// Policy is the access policy's JSON (outputs.engine.policy of the
	// database's config, "permission" unless set), and Project the
	// project's engine crate whose behaviors the engine runs, nil for the
	// core's: what a host that opens the engine itself reads, as the
	// Topcoat extension's crate does.
	Policy  string
	Project *DepsProject
}

// DepsProject is the project's engine crate of the engine database (D58;
// projectengine): its package name, its name in Rust source, and its
// directory relative to the API crate's.
type DepsProject struct {
	Crate string
	Ident string
	Path  string
}

// CrateIdent is Crate as Rust source spells it.
func (e DepsEngine) CrateIdent() string {
	return strings.ReplaceAll(e.Crate, "-", "_")
}

// DatabaseTypesCrateIdent is DatabaseTypesCrate as Rust source spells it.
func (d DepsInfo) DatabaseTypesCrateIdent() string {
	return strings.ReplaceAll(d.DatabaseTypesCrate, "-", "_")
}

// DatabaseTypesModule is the name the API crate re-exports the Rust types
// of its database as, for an API with workers (D65): the implementation
// names a worker's message, and enqueues with a queue's descriptor,
// through it, and a worker's entrypoint claims with the descriptor.
const DatabaseTypesModule = "database_types"

// DepsCall is a Rust SDK client in Deps.
type DepsCall struct {
	// Service is the called API service.
	Service string

	// Field is the Deps field that holds the client.
	Field string

	// Crate is the service's Rust SDK crate, and Path its directory
	// relative to the API crate's, which Cargo.toml depends on.
	Crate string
	Path  string

	// Client is the SDK's struct, which its new builds from a
	// ClientConfig.
	Client string
}

// DepsBucket is a bucket in Deps: an Arc of the HTTP runtime's
// provider-neutral bucket::Bucket, which the entrypoint opens on the
// bucket's provider (D54), GCS's through the runtime's gcs feature.
type DepsBucket struct {
	// Service is the Bucket service.
	Service string

	// Field is the Deps field that holds the bucket.
	Field string
}

// DepsMemoryStore is a memory store in Deps (D75): an Arc of the HTTP
// runtime's memorystore::MemoryStore, which the entrypoint opens once, on
// Redis through the runtime's memorystore-redis feature, or disabled while
// the store's field is unset.
type DepsMemoryStore struct {
	// Service is the MemoryStore service.
	Service string

	// Field is the Deps field that holds the store.
	Field string

	// Definitions is the crate of the store's Rust definitions, which the
	// implementation's crate depends on to name a channel or a key, and
	// DefinitionsPath its directory relative to the API crate's; both are
	// empty when the store's outputs write none.
	Definitions     string
	DefinitionsPath string
}

// CrateIdent is the SDK crate as Rust source spells it.
func (c DepsCall) CrateIdent() string {
	return strings.ReplaceAll(c.Crate, "-", "_")
}

// depsFields are the fields every Deps may hold beside its clients.
var depsFields = []string{"config", "db", "engine", "span"}

// JobInfo is a job of the API (D52, D65): a method of the generated Jobs
// trait, which a job's entrypoint calls once per run.
type JobInfo struct {
	// Name is the job's @job class, and Method the Jobs method that runs
	// it, the class in snake case.
	Name   string
	Method string

	// Schedule is the decorator's five-field cron, empty for a job that
	// runs only on demand, and TimeZone the zone it is read in, empty for
	// UTC.
	Schedule string
	TimeZone string

	// TimeoutSeconds bounds one run: the decorator's timeout, or
	// ir.DefaultJobTimeoutSeconds. Retries is how many times a failed run
	// is run again.
	TimeoutSeconds int
	Retries        int
}

// WorkerInfo is a worker of the API (D53, D65): a method of the generated
// Workers trait, which handles one message of its queue.
type WorkerInfo struct {
	// Name is the worker's @worker class, and Method the Workers method
	// that handles a message, the class in snake case.
	Name   string
	Method string

	// Queue is the @queue class of the API's database whose messages the
	// worker handles: the message's type in the database's Rust types
	// (DepsInfo.DatabaseTypesCrate), whose src/queues.rs holds the
	// queue's descriptor in the constant QueueConst (ORDER_PLACED_QUEUE).
	Queue      string
	QueueConst string

	// Concurrency is the decorator's concurrency, how many messages an
	// instance handles at a time unless an environment changes it, and
	// GraceSeconds the decorator's grace, how long a stopped worker lets
	// its running handlers finish.
	Concurrency  int
	GraceSeconds int
}

// jobsOf reads the API's jobs, sorted by name.
func jobsOf(schema *ir.Schema) ([]JobInfo, error) {
	var jobs []JobInfo
	for _, job := range schema.Jobs {
		if job == nil {
			continue
		}
		timeout := ir.DefaultJobTimeoutSeconds
		if job.Timeout != "" {
			seconds, err := registry.TimeoutSeconds(job.Timeout)
			if err != nil {
				return nil, fmt.Errorf("rustrestgen: job %s: timeout: %w", job.Name, err)
			}
			timeout = seconds
		}
		jobs = append(jobs, JobInfo{
			Name:           job.Name,
			Method:         rustutil.Identifier(job.Name, "run"),
			Schedule:       job.Schedule,
			TimeZone:       job.TimeZone,
			TimeoutSeconds: timeout,
			Retries:        job.Retries,
		})
	}
	slices.SortFunc(jobs, func(a, b JobInfo) int { return strings.Compare(a.Name, b.Name) })
	return jobs, nil
}

// workersOf reads the API's workers, sorted by name.
func workersOf(schema *ir.Schema) ([]WorkerInfo, error) {
	var workers []WorkerInfo
	for _, w := range schema.Workers {
		if w == nil {
			continue
		}
		if err := registry.CheckWorker(w); err != nil {
			return nil, fmt.Errorf("rustrestgen: worker %s: %w", w.Name, err)
		}
		workers = append(workers, WorkerInfo{
			Name:         w.Name,
			Method:       rustutil.Identifier(w.Name, "handle"),
			Queue:        w.Queue,
			QueueConst:   rustgen.QueueConst(w.Queue),
			Concurrency:  registry.WorkerConcurrency(w),
			GraceSeconds: registry.WorkerGraceSeconds(w),
		})
	}
	slices.SortFunc(workers, func(a, b WorkerInfo) int { return strings.Compare(a.Name, b.Name) })
	return workers, nil
}

// HasJobs reports whether the API declares a job, for which deps.rs
// declares Jobs and the scaffold create_jobs.
func (o *APIOutput) HasJobs() bool { return o.Deps != nil && len(o.Jobs) > 0 }

// HasWorkers reports whether the API declares a worker, for which deps.rs
// declares Workers and the scaffold create_workers.
func (o *APIOutput) HasWorkers() bool { return o.Deps != nil && len(o.Workers) > 0 }

// HasBackgroundWork reports whether deps.rs declares Jobs or Workers, and
// BoxError, the error their methods return.
func (o *APIOutput) HasBackgroundWork() bool { return o.HasJobs() || o.HasWorkers() }

// HasTraits reports whether deps.rs declares a trait an implementation
// implements beside a namespace's, whose methods fail with BoxError: Jobs,
// Workers or a deriver's (D74).
func (o *APIOutput) HasTraits() bool { return o.HasBackgroundWork() || o.HasDerivers() }

// The functions an implementation of an API with jobs or workers declares
// in its src/lib.rs, which a job's and a worker's entrypoint call with the
// API's Deps (D65).
const (
	JobsFunc    = "create_jobs"
	WorkersFunc = "create_workers"
)

// JobSignature is the signature of job's method in the Jobs trait.
func (o *APIOutput) JobSignature(job JobInfo) string {
	return rustSignature("    ", "async fn "+job.Method, []string{"&self", "shutdown: Shutdown"}, "Result<(), BoxError>", ";")
}

// WorkerSignature is the signature of worker's method in the Workers
// trait, its message typed by the database's Rust types.
func (o *APIOutput) WorkerSignature(worker WorkerInfo) string {
	return rustSignature("    ", "async fn "+worker.Method, []string{"&self", "message: " + o.Deps.DatabaseTypesCrateIdent() + "::" + worker.Queue}, "Result<(), BoxError>", ";")
}

// ManualRoutesFunc is the function an implementation of an API with a
// @manualRouteRegistration operation declares in its src/lib.rs, which a
// server's entrypoint calls with the API's Deps and whose handlers it
// mounts with build_router_with_manual_routes (D51).
const ManualRoutesFunc = "create_manual_routes"

// ManualSignature is the signature of the method of the manual operation
// endpoint in the ManualRoutes trait: the operation's namespace and name,
// as the router names its handler.
func (o *APIOutput) ManualSignature(endpoint EndpointInfo) string {
	return rustSignature("    ", "async fn "+endpoint.SnakeName(), []string{"&self", "ctx: RequestContext", "request: Request"}, "Result<Response, ApiError>", ";")
}

// DepsExports are the items lib.rs re-exports from deps.rs: Deps and
// Handlers, and Jobs, Workers and BoxError for an API with either.
func (o *APIOutput) DepsExports() string {
	names := []string{"Deps", "Handlers"}
	if o.HasTraits() {
		names = append(names, "BoxError")
	}
	if o.HasJobs() {
		names = append(names, "Jobs")
	}
	if o.HasWorkers() {
		names = append(names, "Workers")
	}
	names = append(names, o.DeriverExports()...)
	return strings.TrimPrefix(rustutil.UseLine("deps", names), "use ")
}

// DatabaseTypesDependency is the Cargo.toml dependency on the database's
// Rust types for an API with workers, nil without one or when the crate
// depends on them already for a dependency's validators.
func (o *APIOutput) DatabaseTypesDependency() *DependencyCrate {
	if (!o.HasWorkers() && !o.HasDerivers()) || o.Deps.DatabaseTypesCrate == "" {
		return nil
	}
	for _, crate := range o.DependencyCrates {
		if crate.Name == o.Deps.DatabaseTypesCrate {
			return nil
		}
	}
	return &DependencyCrate{Name: o.Deps.DatabaseTypesCrate, Path: o.Deps.DatabaseTypesPath}
}

// SetDeps sets what Deps holds, so the crate writes src/deps.rs, refusing
// a client or a bucket whose field would take the name of another of its
// fields.
func (o *APIOutput) SetDeps(deps DepsInfo) error {
	seen := map[string]string{}
	for _, name := range depsFields {
		seen[name] = "Deps." + name
	}
	for _, call := range deps.Calls {
		if prev, clash := seen[call.Field]; clash {
			return fmt.Errorf("rustrestgen: %s calls %s, whose client would be Deps.%s, the field of %s", o.SchemaName, call.Service, call.Field, prev)
		}
		seen[call.Field] = call.Service
	}
	for _, b := range deps.Buckets {
		if prev, clash := seen[b.Field]; clash {
			return fmt.Errorf("rustrestgen: %s lists bucket %s, which would be Deps.%s, the field of %s", o.SchemaName, b.Service, b.Field, prev)
		}
		seen[b.Field] = b.Service
	}
	for _, m := range deps.MemoryStores {
		if prev, clash := seen[m.Field]; clash {
			return fmt.Errorf("rustrestgen: %s lists memory store %s, which would be Deps.%s, the field of %s", o.SchemaName, m.Service, m.Field, prev)
		}
		seen[m.Field] = m.Service
	}
	if len(o.Workers) > 0 && deps.DatabaseTypesCrate == "" {
		return fmt.Errorf("rustrestgen: %s declares workers, whose messages are its database's Rust types, and its Deps names none", o.SchemaName)
	}
	if len(o.Derivers) > 0 && (deps.DatabaseTypesCrate == "" || deps.Engine == nil) {
		return fmt.Errorf("rustrestgen: %s declares derivers, whose methods its database's Rust types type and whose engine its Deps holds, and its Deps names neither", o.SchemaName)
	}
	o.Deps = &deps
	return nil
}

// DepsField is the Deps field of the client of the API service named
// service, of the bucket of the Bucket service named service, or of the
// store of the MemoryStore service named service: its name
// in snake_case (shop-api is shop_api).
func DepsField(service string) string {
	return rustutil.ToSnakeCase(service)
}

// ChecksEndUsers reports whether an operation needs a caller that the
// implementation establishes: Handlers then holds the authenticator. An
// API whose authDb has a User table has the identity runtime establish
// every end user (D50), whose authenticator the entrypoint builds.
func (o *APIOutput) ChecksEndUsers() bool {
	return o.HasAuth && o.Identity == nil
}

// HandlersFields are the fields of Handlers: one per namespace, the
// authenticator when the implementation establishes the end user, and the
// webhook verifiers.
func (o *APIOutput) HandlersFields() []string {
	var fields []string
	for _, ns := range o.Namespaces {
		fields = append(fields, rustutil.ToSnakeCase(ns))
	}
	if o.ChecksEndUsers() {
		fields = append(fields, "authenticator")
	}
	if len(o.WebhookProviders) > 0 {
		fields = append(fields, "webhook_verifiers")
	}
	return fields
}

// DepsUses are the use declarations of src/deps.rs beside std's: the
// config, what it names from the interfaces module (each namespace's trait,
// Implementations and the webhook verifier's trait), async_trait for Jobs
// and Workers, and the runtime's Bucket, pool, authenticators and
// Shutdown.
func (o *APIOutput) DepsUses() []string {
	var uses []string
	if o.HasEnvConfig {
		uses = append(uses, "use crate::config::EnvConfig;")
	}
	interfaces := []string{"Implementations"}
	for _, ns := range o.Namespaces {
		interfaces = append(interfaces, rustutil.ToPascalCase(ns)+"Implementation")
	}
	if len(o.WebhookProviders) > 0 {
		interfaces = append(interfaces, "WebhookVerifier")
	}
	if o.HasDerivers() {
		// The database's types, which type the derivers' methods (D74).
		uses = append(uses, "use crate::"+DatabaseTypesModule+";")
	}
	uses = append(uses, rustutil.UseLine("crate::interfaces", interfaces))
	if o.HasTraits() {
		// The Jobs, Workers and derivers' traits, after crate's paths, as
		// rustfmt orders them.
		uses = append(uses, "use async_trait::async_trait;")
	}
	if o.HasDerivers() {
		uses = append(uses, "use serde::{Deserialize, Serialize};")
	}
	var runtime []string
	if o.Deps != nil && len(o.Deps.Buckets) > 0 {
		runtime = append(runtime, "bucket::Bucket")
	}
	if o.Deps != nil && len(o.Deps.MemoryStores) > 0 {
		runtime = append(runtime, "memorystore::MemoryStore")
	}
	if o.Deps != nil && o.Deps.Database != "" {
		runtime = append(runtime, "postgres::Pool")
	}
	if o.ChecksEndUsers() {
		runtime = append(runtime, "Authenticator")
	}
	if o.Identity != nil {
		runtime = append(runtime, "IdentityAuthenticator")
	}
	if o.HasServiceCallers {
		runtime = append(runtime, "ServiceAuthenticator")
	}
	if o.HasJobs() {
		runtime = append(runtime, "Shutdown")
	}
	engine := ""
	if o.HasDerivers() {
		engine = rustutil.UseLine(o.Deps.Engine.CrateIdent()+"::derive", []string{"DerivationAnswer", "DerivationItem"})
	}
	if engine != "" && o.Deps.Engine.CrateIdent() < o.RuntimeCrateIdent {
		uses = append(uses, engine)
		engine = ""
	}
	if len(runtime) > 0 {
		uses = append(uses, rustutil.UseLine(o.RuntimeCrateIdent, runtime))
	}
	if engine != "" {
		uses = append(uses, engine)
	}
	return uses
}

// NewSignature is the signature of Implementations::new, indented as a
// method of an impl: the handlers, the identity runtime's authenticator for
// an API over the user model (D50) and the service authenticator for an
// API with a service clause (D37), on one line when it fits.
func (o *APIOutput) NewSignature() string {
	params := []string{"handlers: Handlers"}
	if o.Identity != nil {
		params = append(params, "authenticator: Arc<IdentityAuthenticator>")
	}
	if o.HasServiceCallers {
		params = append(params, "service_authenticator: Arc<dyn ServiceAuthenticator>")
	}
	return rustFn("    ", "pub fn new", params, "Self")
}

// NotImplemented is the body of endpoint's method in the implementation's
// impl: a 501 naming the method, laid out as rustfmt lays the call out.
func (s ImplementationScaffold) NotImplemented(endpoint EndpointInfo) string {
	message := rustString(rustutil.ToSnakeCase(endpoint.Namespace) + "." + endpoint.FunctionName + " is not implemented")
	if call := "ApiError::not_implemented(" + message + ")"; len(call) <= 60 {
		return "        Err(" + call + ")"
	}
	return "        Err(ApiError::not_implemented(\n            " + message + ",\n        ))"
}

// rustFn is a function's signature through its opening brace, indented by
// indent: on one line when it fits in 100 columns, as rustfmt lays it out,
// and otherwise a parameter to a line.
func rustFn(indent, name string, params []string, result string) string {
	return rustSignature(indent, name, params, result, " {")
}

// rustSignature is a function's signature followed by end, its opening
// brace or, in a trait, a semicolon, laid out as rustFn lays one out.
func rustSignature(indent, name string, params []string, result, end string) string {
	line := indent + name + "(" + strings.Join(params, ", ") + ") -> " + result + end
	if len(line) <= 100 {
		return line
	}
	var b strings.Builder
	b.WriteString(indent + name + "(\n")
	for _, param := range params {
		b.WriteString(indent + "    " + param + ",\n")
	}
	b.WriteString(indent + ") -> " + result + end)
	return b.String()
}

// RuntimeFeatures are the HTTP runtime's features the crate turns on:
// identity for the user model (D50); postgres, Deps.db's pool, for an API
// with a database, and identity-postgres beside both, so the identity store
// runs over the same pool; and http-client for an API that calls another,
// whose credential may be a Google ID token (D57).
func (o *APIOutput) RuntimeFeatures() []string {
	var features []string
	if o.Identity != nil {
		features = append(features, "identity")
	}
	if o.Deps == nil {
		return features
	}
	if o.Deps.Database != "" {
		features = append(features, "postgres")
		if o.Identity != nil {
			features = append(features, "identity-postgres")
		}
	}
	if len(o.Deps.Calls) > 0 {
		features = append(features, "http-client")
	}
	return features
}

// ImplementationScaffold is the template view of the implementation
// crate's scaffold.
type ImplementationScaffold struct {
	*APIOutput
	// Crate is the implementation crate's name
	// (Naming.RustImplementationCrate).
	Crate string
	// APIPath and RuntimePath are the API crate's and the HTTP runtime's
	// directories relative to the implementation's, which its Cargo.toml
	// depends on.
	APIPath     string
	RuntimePath string
	// Definitions are the crates of the Rust definitions of the memory
	// stores the API lists (D75), by their paths relative to the
	// implementation's directory, which its Cargo.toml depends on.
	Definitions []ScaffoldDependency
}

// ScaffoldDependency is a crate the implementation's Cargo.toml depends on
// by path.
type ScaffoldDependency struct {
	Crate string
	Path  string
}

// CrateIdent is the API crate as Rust source spells it.
func (s ImplementationScaffold) CrateIdent() string {
	return strings.ReplaceAll(s.CrateName, "-", "_")
}

// UsesTypes reports whether a method signature of the implementation names
// the types crate.
func (s ImplementationScaffold) UsesTypes() bool {
	for _, endpoint := range s.Endpoints {
		if endpoint.UsesTypes() {
			return true
		}
	}
	return false
}

// APIUse is the use declaration of what the implementation names from the
// API crate: Deps, Handlers, each namespace's trait and each Args struct
// its methods take.
func (s ImplementationScaffold) APIUse() string {
	names := []string{"Deps", "Handlers"}
	for _, ns := range s.Namespaces {
		names = append(names, rustutil.ToPascalCase(ns)+"Implementation")
	}
	for _, endpoint := range s.Endpoints {
		if endpoint.HasArgs() {
			names = append(names, endpoint.ArgsName)
		}
	}
	if s.HasTraits() {
		names = append(names, "BoxError")
	}
	if s.HasJobs() {
		names = append(names, "Jobs")
	}
	if s.HasWorkers() {
		names = append(names, "Workers", DatabaseTypesModule)
	}
	if s.HasDerivers() {
		if !s.HasWorkers() {
			names = append(names, DatabaseTypesModule)
		}
		names = append(names, "DerivedAnswer", "DerivedItem", "Derivers")
		for _, d := range s.Derivers {
			names = append(names, d.Trait)
		}
		for _, m := range s.DerivedInputs() {
			names = append(names, m.Inputs)
		}
	}
	if len(s.ManualEndpoints) > 0 {
		names = append(names, "ManualRoutes")
	}
	return rustutil.UseLine(s.CrateIdent(), names)
}

// JobsStruct is the struct the scaffold implements Jobs with, named after
// the API: ShopStockJobs.
func (s ImplementationScaffold) JobsStruct() string {
	return rustutil.ToPascalCase(s.SchemaName) + "Jobs"
}

// WorkersStruct is the struct the scaffold implements Workers with:
// ShopStockWorkers.
func (s ImplementationScaffold) WorkersStruct() string {
	return rustutil.ToPascalCase(s.SchemaName) + "Workers"
}

// JobMethodSignature is the signature of job's method in the scaffold's
// impl of Jobs.
func (s ImplementationScaffold) JobMethodSignature(job JobInfo) string {
	return rustFn("    ", "async fn "+job.Method, []string{"&self", "_shutdown: Shutdown"}, "Result<(), BoxError>")
}

// WorkerMethodSignature is the signature of worker's method in the
// scaffold's impl of Workers, its message named through the API crate's
// re-export of its database's types.
func (s ImplementationScaffold) WorkerMethodSignature(worker WorkerInfo) string {
	return rustFn("    ", "async fn "+worker.Method, []string{"&self", "_message: " + DatabaseTypesModule + "::" + worker.Queue}, "Result<(), BoxError>")
}

// ManualRoutesStruct is the struct the scaffold implements ManualRoutes
// with, named after the API: ShopStockManualRoutes.
func (s ImplementationScaffold) ManualRoutesStruct() string {
	return rustutil.ToPascalCase(s.SchemaName) + "ManualRoutes"
}

// ManualMethodSignature is the signature of the manual operation
// endpoint's method in the scaffold's impl of ManualRoutes.
func (s ImplementationScaffold) ManualMethodSignature(endpoint EndpointInfo) string {
	return rustFn("    ", "async fn "+endpoint.SnakeName(), []string{"&self", "_ctx: RequestContext", "_request: Request"}, "Result<Response, ApiError>")
}

// Async reports whether the scaffold implements a trait, whose impls are
// #[async_trait] and hold their deps in an Arc: a namespace's, the
// authenticator's, Jobs, Workers or ManualRoutes.
func (s ImplementationScaffold) Async() bool {
	return len(s.Namespaces) > 0 || s.ChecksEndUsers() || s.HasTraits() || len(s.ManualEndpoints) > 0
}

// RuntimeUse is the use declaration of what the implementation names from
// the HTTP runtime: the error and the request context its methods take,
// and the authenticator's trait and principal when it establishes the end
// user. Empty when it names none.
func (s ImplementationScaffold) RuntimeUse() string {
	var names []string
	manual := len(s.ManualEndpoints) > 0
	if len(s.Namespaces) > 0 || s.ChecksEndUsers() || manual {
		names = append(names, "ApiError")
	}
	if len(s.Namespaces) > 0 || manual {
		names = append(names, "RequestContext")
	}
	if s.ChecksEndUsers() {
		names = append(names, "Authenticator", "Principal")
	}
	if s.HasJobs() {
		names = append(names, "Shutdown")
	}
	if len(names) == 0 {
		return ""
	}
	return rustutil.UseLine(s.RuntimeCrateIdent, names)
}

// MethodSignature is the signature of endpoint's method in the
// implementation's impl of its namespace's trait.
func (s ImplementationScaffold) MethodSignature(endpoint EndpointInfo) string {
	params := []string{"&self", "_ctx: RequestContext"}
	if endpoint.HasArgs() {
		params = append(params, "_args: "+endpoint.ArgsName)
	}
	return rustFn("    ", "async fn "+endpoint.FunctionName, params, "Result<"+endpoint.OutputRustType+", ApiError>")
}

// ImplementationManifest is the manifest the scaffold writes; a directory
// that holds one is an implementation, which the scaffold never writes into.
const ImplementationManifest = "Cargo.toml"

// ImplementationLib is the file the scaffold writes the implementation in.
var ImplementationLib = filepath.Join("src", "lib.rs")

// WriteImplementationScaffold writes the scaffold of the API's Rust
// implementation into dir, the crate the naming file's
// [implementation_paths] rust template places it in, when the crate is
// missing: when dir holds no Cargo.toml (D57). It never writes into a
// crate that exists, so it never overwrites the engineer's code, and
// reports whether it wrote. Its Cargo.toml depends on the API crate at
// apiDir and on the HTTP runtime at paths.HTTPRuntimeRust, by paths
// relative to dir, for the crate's editor and its own `cargo test`; a
// stack's server builds against the crate's mirror under its output root,
// whose Cargo.toml names that root's API crate (D65). Its create(deps)
// returns the Handlers, each method answering 501 Not Implemented. The
// output must have its Deps (SetDeps).
func WriteImplementationScaffold(output *APIOutput, dir, apiDir string, paths naming.LocalPaths, names naming.Naming) (bool, error) {
	if output.Deps == nil {
		return false, fmt.Errorf("rustrestgen: the implementation scaffold of %s needs its Deps", output.SchemaName)
	}
	exists, err := ImplementationExists(dir)
	if err != nil || exists {
		return false, err
	}
	if paths.HTTPRuntimeRust == "" {
		return false, fmt.Errorf("rustrestgen: [paths] http_runtime_rust is required to locate the http-runtime crate from the implementation of %s", output.SchemaName)
	}
	view := ImplementationScaffold{APIOutput: output, Crate: names.OrDefault().RustImplementationCrate(output.SchemaName)}
	if view.APIPath, err = naming.RelPath(dir, apiDir); err != nil {
		return false, fmt.Errorf("rustrestgen: the API crate's path from the implementation of %s: %w", output.SchemaName, err)
	}
	if view.RuntimePath, err = naming.RelPath(dir, paths.HTTPRuntimeRust); err != nil {
		return false, fmt.Errorf("rustrestgen: the runtime crate's path from the implementation of %s: %w", output.SchemaName, err)
	}
	for _, m := range output.Deps.MemoryStores {
		if m.Definitions == "" {
			continue
		}
		path, err := naming.RelPath(dir, filepath.Join(apiDir, m.DefinitionsPath))
		if err != nil {
			return false, fmt.Errorf("rustrestgen: the definitions of %s from the implementation of %s: %w", m.Service, output.SchemaName, err)
		}
		view.Definitions = append(view.Definitions, ScaffoldDependency{Crate: m.Definitions, Path: path})
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return false, fmt.Errorf("create implementation crate %s: %w", dir, err)
	}
	for _, file := range []struct{ template, name string }{
		{"implementation-cargo.tmpl", ImplementationManifest},
		{"implementation-lib.tmpl", ImplementationLib},
	} {
		if _, err := codegen.GenerateFileIfNotExists(generateFile, file.template, filepath.Join(dir, file.name), view); err != nil {
			return false, fmt.Errorf("write implementation scaffold: %w", err)
		}
	}
	return true, nil
}

// fnPattern matches a public function at the top of a file, by its name in
// the match's first group: `pub async fn create_jobs`, or `pub fn`.
var fnPattern = regexp.MustCompile(`(?m)^[ \t]*pub[ \t]+(?:async[ \t]+)?fn[ \t]+([A-Za-z_][A-Za-z0-9_]*)`)

// usePattern matches a re-export, its tree in the match's first group:
// `pub use jobs::create_jobs;`, `pub use jobs::{build as create_jobs};`.
var usePattern = regexp.MustCompile(`(?m)^[ \t]*pub[ \t]+use[ \t]+([^;]*);`)

// DeclaresFunc reports whether the Rust implementation crate at dir
// declares, at its root, a public function named name, which a job's or a
// worker's entrypoint calls (D65): its src/lib.rs declares one, or
// re-exports one by that name. A glob re-export (`pub use jobs::*;`) may
// export it, so it counts as declaring it, as tsrestgen.ExportsName counts
// `export * from`.
func DeclaresFunc(dir, name string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(dir, ImplementationLib))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation crate %s: %w", dir, err)
	}
	text := string(data)
	for _, m := range fnPattern.FindAllStringSubmatch(text, -1) {
		if m[1] == name {
			return true, nil
		}
	}
	for _, m := range usePattern.FindAllStringSubmatch(text, -1) {
		for _, item := range strings.FieldsFunc(m[1], func(r rune) bool { return r == ',' || r == '{' || r == '}' }) {
			fields := strings.Fields(item)
			if len(fields) == 0 {
				continue
			}
			// `path::create_jobs`, `path::build as create_jobs`, or a glob.
			last := fields[len(fields)-1]
			if strings.HasSuffix(last, "*") || last == name || strings.HasSuffix(last, "::"+name) {
				return true, nil
			}
		}
	}
	return false, nil
}

// ImplementationExists reports whether dir holds a Rust crate: a
// Cargo.toml.
func ImplementationExists(dir string) (bool, error) {
	_, err := os.Stat(filepath.Join(dir, ImplementationManifest))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read implementation crate %s: %w", dir, err)
	}
	return true, nil
}
