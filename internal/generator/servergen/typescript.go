package servergen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The entrypoint of a TypeScript server (D51; docs/stack-model.md,
// sections 8.1 and 8.6): a package at `<output-root>/server/<stack>/<server>/`
// holding main.ts, package.json and tsconfig.json, a member of the output
// root's Bun workspace, and a Dockerfile. Bun runs main.ts as it is, as it
// runs every generated package, so nothing is built. main.ts does what the
// Go entrypoint's main does: it loads each API's config, opens one pg Pool
// per database, or for a database whose DB service lists sqlite first the
// SQLite file its sqlite: URL names (D64), builds one SDK client per API
// called, builds each API's
// implementation from its Deps and mounts every API's router on one Hono
// app beside /healthz and /readyz. It exports its traces through the HTTP
// runtime's ./telemetry entry when the environment names an OTLP endpoint
// (D67). An API whose authDb has a User table
// authenticates with the identity runtime (D50), as on a Go server: main.ts
// builds one identity store over the pool of that database, from the
// descriptor its TypeScript types export, and the API's identity service
// from its identity config field. A server whose APIs list a
// bucket opens each on GCS through the HTTP runtime's ./gcs entry, and
// depends on GCS's Node client (D54).

// BunVersion is the oven/bun image a TypeScript server's Dockerfile runs it
// on: tools.env's BUN_VERSION, the Bun the repository's checks run on.
const BunVersion = "1.4.2"

// The files of a TypeScript entrypoint package.
const (
	TypeScriptMainFile    = "main.ts"
	TypeScriptPackageFile = "package.json"
	TypeScriptConfigFile  = "tsconfig.json"
)

// The files the package of a TypeScript server some environment places on
// Cloudflare Workers holds beside main.ts (docs/stack-model.md, section
// 18.5, D64): the Workers entrypoint, the script that bundles it, which a
// deploy runs as `bun bundle.ts <dir>`, and the declaration of the
// cloudflare:workers module fetch.ts imports, which the bundle leaves to
// workerd.
const (
	TypeScriptFetchFile        = "fetch.ts"
	TypeScriptBundleFile       = "bundle.ts"
	TypeScriptWorkersTypesFile = "workers.d.ts"
)

// TypeScriptScheduledFile is the Workers entrypoint of a TypeScript job some
// environment places on Cloudflare's Cron Triggers, which its package holds
// beside main.ts in place of fetch.ts, and which its bundle.ts bundles
// (docs/stack-model.md, section 18.9, D64).
const TypeScriptScheduledFile = "scheduled.ts"

// TypeScriptDrainFile is the Workers entrypoint of a TypeScript worker some
// environment places on Cloudflare Workers, under a Cron Trigger every
// minute, which its package holds beside main.ts, and which its bundle.ts
// bundles (D92).
const TypeScriptDrainFile = "drain.ts"

// The versions of the third-party packages main.ts imports beside Hono,
// which the API packages pin: pg, within the HTTP runtime's and the API
// packages' peer range, and the Cloud SQL Node connector the runtime's
// ./postgres entry loads, which only a server some environment places on
// Cloud SQL depends on, as only such a Go server links the Go connector.
const (
	pgVersion                = "8.23.0"
	cloudSQLConnectorPackage = "@google-cloud/cloud-sql-connector"
	cloudSQLConnectorVersion = "1.12.0"
)

// TelemetryPackages are the OpenTelemetry packages the HTTP runtime's
// ./telemetry entry imports, which every Bun server, job and worker
// depends on (D67): the runtime's optional peer dependencies, at the
// versions its tests run with.
var TelemetryPackages = map[string]string{
	"@opentelemetry/api":                       "1.9.1",
	"@opentelemetry/context-async-hooks":       "2.12.0",
	"@opentelemetry/core":                      "2.12.0",
	"@opentelemetry/exporter-trace-otlp-proto": "0.223.0",
	"@opentelemetry/resources":                 "2.12.0",
	"@opentelemetry/sdk-trace":                 "2.12.0",
}

// WorkersTelemetryPackages are the OpenTelemetry packages the HTTP
// runtime's ./workers-telemetry entry imports beside TelemetryPackages,
// which a server or a job on Workers depends on too (D64, amended): it
// sends with OpenTelemetry's own protobuf serializer.
var WorkersTelemetryPackages = map[string]string{
	"@opentelemetry/otlp-transformer": "0.223.0",
}

// The GCS Node client the HTTP runtime's ./gcs entry imports, which only a
// server whose APIs list a bucket depends on (D54): the release the
// runtime is checked against.
const (
	storagePackage = "@google-cloud/storage"
	storageNodeVer = "8.3.0"

	// redisClientPackage is the Redis client the HTTP runtime's ./redis
	// entry opens a memory store with, which a package depends on only when
	// an API it serves lists a store (D75), at redisClientVersion, the
	// runtime's pin.
	redisClientPackage = "@redis/client"
	redisClientVersion = "5.12.1"
)

// TypeScriptImplementation is where an API's TypeScript implementation
// lives (docs/stack-model.md, section 8.6).
type TypeScriptImplementation struct {
	// Dir is the package's directory, at the naming file's
	// [implementation_paths] typescript template.
	Dir string

	// Package is its npm name: its package.json's, or the scaffold's when
	// the scaffold writes it (ImplementationPackage).
	Package string
}

// TypeScriptAPIInput is one API a TypeScript server serves.
type TypeScriptAPIInput struct {
	// Output is the API's TypeScript package, with its Deps.
	Output *tsrestgen.APIOutput

	// Routes is the API's server output, whose routes the build checks
	// against the other APIs' (checkRoutes).
	Routes *apigen.APIOutput

	// Config is the API's EnvConfig: its derived fields and its callers
	// field. Nil when the API has none.
	Config *envgen.ConfigOutput

	// Implementation is where its implementation lives.
	Implementation TypeScriptImplementation

	// CORS is true when a site of the stack calls the API: its server then
	// answers CORS for the origins its CORS field lists (D55).
	CORS bool
}

// TypeScriptInput is what a TypeScript server's entrypoint is planned
// from.
type TypeScriptInput struct {
	// Stack is the stack's name; Server the server's name in the stack.
	Stack  string
	Server string

	// Dir is the package's directory, ServerDir.
	Dir string

	// OutputRoot is the root of the Bun workspace the package joins.
	OutputRoot string

	// DefaultOutputRoot is the output root whose Bun workspace the
	// project's own packages are members of, the implementations among
	// them (tsgen.DefaultOutputRoot). Under any other output root the
	// server builds against mirrors of the implementations its APIs serve,
	// which the build copies under the root's impl/ (D65; tsmirror.go).
	// Empty takes OutputRoot as the default.
	DefaultOutputRoot string

	// APIs are the APIs the server serves, sorted by service.
	APIs []TypeScriptAPIInput

	// Naming names the package and the runtime packages main.ts imports.
	Naming naming.Naming

	// RepositoryRoot is the Dockerfile's build context: the repository
	// root, or the naming file's [paths] build_context. Every directory the
	// image reads must lie under it, or no Dockerfile is written.
	RepositoryRoot string

	// ImplementationRoot is the root the naming file's
	// [implementation_paths] typescript template is relative to: the
	// repository root, the parent of the schemas root.
	ImplementationRoot string

	// Image is what the stack adds to the entrypoint's image, nil when it
	// adds nothing (D144).
	Image *ImageInput

	// PackageDirs are the directories of the workspace packages the
	// server depends on, by npm name: each API package, implementation
	// and SDK, which its image holds whole.
	PackageDirs map[string]string

	// Paths are the naming file's [paths]: the image builds superscalar's
	// Node addon and the HTTP runtime's package from the checkouts they
	// name, which the workspace's root overrides the packages with.
	Paths naming.LocalPaths

	// CloudSQL are the DB services some environment of the stack connects
	// the server to with a Cloud SQL connector configuration. The server
	// depends on the Cloud SQL Node connector when one of them is a
	// database it connects to.
	CloudSQL []string

	// Job, when set, plans the entrypoint of a job of the one API in APIs
	// rather than a server's (D52): Server is then the job's deployable.
	Job *JobInput

	// Worker, when set, plans the entrypoint of a worker of the one API in
	// APIs (D53): Server is then the worker's deployable.
	Worker *WorkerInput

	// Workers is true when some environment of the stack places the server
	// on a platform whose artifact is a bundle, Cloudflare Workers (D64):
	// its package gets fetch.ts and bundle.ts beside main.ts, which stack
	// dev still runs on Bun; a job's gets scheduled.ts in place of
	// fetch.ts, for Cron Triggers, and a worker's drain.ts (D92).
	Workers bool

	// Deriver, when set, plans the entrypoint of a deriver of the one API
	// in APIs (D146): Server is then the deriver's deployable.
	Deriver *DeriverInput
}

// TypeScriptServer is a planned TypeScript entrypoint, what the templates
// read: a server's, a job's when Job is set, or a worker's when Worker is.
type TypeScriptServer struct {
	Stack   string
	Name    string
	Package string
	Naming  naming.Naming

	// Kind is "server", "job", "worker" or "deriver".
	Kind string

	// Job is what a job's entrypoint runs, nil for a server and a worker
	// (D52).
	Job *Job

	// Deriver is what a deriver's entrypoint runs, nil for every other
	// (D146).
	Deriver *TypeScriptDeriver

	// Worker is what a worker's entrypoint runs, nil for a server and a
	// job (D53).
	Worker *TypeScriptWorker

	// Workers is true for a server some environment places on Cloudflare
	// Workers, whose package holds fetch.ts and bundle.ts beside main.ts,
	// for a job some environment places on Cron Triggers, whose package
	// holds scheduled.ts and bundle.ts (D64), and for a worker some
	// environment places on Workers, whose package holds drain.ts and
	// bundle.ts (D92).
	Workers bool

	// SQLiteFieldKeys is whether an API's repositories on a sqlite-first
	// database open Encrypted<T> fields, whose keyring main.ts reads with
	// parseFieldKeys of the runtime's ./sqlite (D85, D182).
	SQLiteFieldKeys bool

	// RustVersion and BunVersion are the toolchain pins.
	RustVersion string
	BunVersion  string

	APIs      []*TypeScriptAPI
	Databases []*TypeScriptDatabase
	Clients   []*TypeScriptClient

	// Loopback is the server's loopback key, nil without one: a server
	// has one when it calls an API it serves itself whose operations have a
	// service clause (docs/stack-model.md, section 9.9). The shared wiring's
	// ts-loopback builds it, and each such client and service authenticator
	// takes it.
	Loopback *Loopback

	// Buckets are the buckets main.ts opens once, for every API that lists
	// it. With one, the package depends on GCS's Node client (D54).
	Buckets []*TypeScriptBucket

	// MemoryStores are the memory stores the entrypoint opens once, for
	// every API that lists it (D75). With one, the package depends on the
	// Redis client, which a Workers entry never imports: on Workers each
	// store is disabled.
	MemoryStores []*TypeScriptMemoryStore

	// CloudSQL are the databases, by service and sorted, that some
	// environment places on Cloud SQL. With one, the package depends on
	// the Cloud SQL Node connector; with none main.ts refuses a Cloud SQL
	// configuration.
	CloudSQL []string

	// Dependencies and DevDependencies are package.json's, sorted by name.
	Dependencies    []NpmDependency
	DevDependencies []NpmDependency

	// Rules is true when the entrypoint opens the repositories of a
	// database one of whose tables' fields are behaviors (D103), whose
	// orm.ts runs them through the behaviors' core's package
	// (Naming.RulesNpmPackage): the package depends on it, which the
	// workspace's root overrides with the checkout the naming file's
	// [paths] rules_typescript names, and a checkout's image builds its
	// WebAssembly module and emits its dist/.
	Rules bool

	// VersionGraphPool is the name the entrypoint imports the version-graph
	// package's pgPool as (Naming.VersionGraphNpmPackage's ./postgres
	// entry), which each graph's facade is built over, when an API the
	// server serves has a @graphOperations set (D138); empty otherwise. The
	// package then depends on the version-graph package.
	VersionGraphPool string

	// Docker is the Dockerfile's plan, nil when none is written, and
	// NoDocker says why.
	Docker   *TypeScriptDocker
	NoDocker string

	// Mirrors are the mirrors of the served APIs' implementations under
	// the output root, which the server builds against outside the default
	// output root (D65), one per API; none in the default one.
	Mirrors []*TypeScriptMirror
}

// TypeScriptWorker is the worker a TypeScript worker's entrypoint runs
// (D53): its @worker class, the method of the API's Workers that handles a
// message, the queue it claims from, a class of Database, the API's
// database, its concurrency unless the environment sets one, and its grace.
// Definition is the name the database's TypeScript types export the
// queue's definition as, from Module, their ./queues entry, of Package.
type TypeScriptWorker struct {
	Name         string
	Method       string
	Queue        string
	Database     *TypeScriptDatabase
	Concurrency  int
	GraceSeconds int

	Definition string
	Module     string
	Package    string

	// Behavior is set for a @behaviorWorker (D103), whose entrypoint
	// (behavior_worker.ts.tmpl) runs each of the table's behavior queues
	// that QueuesFunc, the API package's export, builds over the
	// database's repositories, and builds no Deps or Workers, unless its
	// table's behaviors emit events (Behavior.Emits, D104), whose handlers
	// are the implementation's Workers methods: it then builds Deps and
	// calls createWorkers, as a worker's entrypoint does. Method is empty
	// then.
	Behavior   *apigen.BehaviorWorkerInfo
	QueuesFunc string

	// Handler is the API package's function a @worker of a runtime
	// piece's queue hands each message to (typescript_wiring.go): the
	// agent dispatcher's requests queue or the run requests queue
	// (dispatchAgentMessage, D183), the notifier's dispatch queue
	// (notifyMessage, D153), a @searchable declaration's queue
	// (indexSearchMessage, D155), which takes the runtime options too
	// (HandlerOptions). The entrypoint calls no createWorkers then. Empty
	// for a worker the implementation handles. Transcripts is set for the
	// dispatcher's requests queue, whose prune also deletes the
	// transcripts past their retention (agents.prune). Upkeep is set for
	// the notifier's dispatch queue, whose prune also asks every channel
	// adapter's health on its schedule and deletes the deliveries past
	// their retention (notifierUpkeepOf, #702).
	Handler        string
	HandlerOptions bool
	Transcripts    bool
	Upkeep         bool
}

// Messages names what a behavior worker's messages carry, for its main's
// comment (D103): its table's deadline, change, or deadline or change.
func (w *TypeScriptWorker) Messages() string {
	switch {
	case w == nil || w.Behavior == nil:
		return ""
	case w.Behavior.DeadlineQueue != "" && w.Behavior.ChangeQueue != "":
		return "deadline or change"
	case w.Behavior.ChangeQueue != "":
		return "change"
	}
	return "deadline"
}

// TypeScriptDeriver is the deriver a TypeScript deriver's entrypoint runs
// (D146): its @deriver class, the member of the API's Derivers that holds
// its methods, and the repositories of the API's database it pulls from
// and settles to, as the principal Subject and Permissions name.
type TypeScriptDeriver struct {
	tsrestgen.DeriverInfo

	// Database is the database whose repositories the deriver pulls and
	// settles through.
	Database *TypeScriptDatabase
}

// Sources are the tables a deriver of tables fills, each with the member
// of the database's repositories that holds its repository.
func (d *TypeScriptDeriver) Sources() []TypeScriptDerivedTable {
	out := make([]TypeScriptDerivedTable, 0, len(d.Schemas))
	for i, schema := range d.Schemas {
		if i < len(d.Repositories) {
			out = append(out, TypeScriptDerivedTable{Schema: schema, Repository: d.Repositories[i]})
		}
	}
	return out
}

// DerivedPermissions are the permissions a deriver of tables holds as a
// behavior caller: each Derived config's permission, which the core asks
// of a pull and a settle.
func (d *TypeScriptDeriver) DerivedPermissions() []string {
	var out []string
	for _, permission := range d.Permissions {
		if !slices.Contains(d.Schemas, permission) {
			out = append(out, permission)
		}
	}
	return out
}

// TypeScriptDerivedTable is a table a deriver of tables fills (D146).
type TypeScriptDerivedTable struct {
	Schema     string
	Repository string
}

// Background reports whether the entrypoint is a job's, a worker's or a
// deriver's, which serves no request: it mounts no routes, listens on no
// port, and verifies no end user or caller.
func (s *TypeScriptServer) Background() bool {
	return s.Job != nil || s.Worker != nil || s.Deriver != nil
}

// NpmDependency is a package.json dependency.
type NpmDependency struct {
	Name string
	Spec string
}

// TypeScriptAPI is a served API in main.ts.
type TypeScriptAPI struct {
	Service string

	// Module and Impl are the namespaces main.ts imports the API package
	// and its implementation as; Package and ImplPackage their npm names,
	// and ImplDir the implementation's directory from the repository root.
	Module      string
	Impl        string
	Package     string
	ImplPackage string
	ImplDir     string

	// Config, Deps, Implementations and Authenticate are the variables of
	// its config, its Deps, its implementations and its authenticator.
	// Config is empty when the API has no EnvConfig, and Authenticate when
	// no route needs an end user or the identity service establishes it.
	// ConfigLoader is the function of the API package that loads Config:
	// loadEnvConfig, or for a job or a worker of an API with a callers
	// field loadBackgroundEnvConfig, since a stack sets no callers field
	// for either (D65).
	Config          string
	ConfigLoader    string
	Deps            string
	Implementations string
	Authenticate    string

	// ManualRoutes is the variable of the handlers of its
	// @manualRouteRegistration operations, which the implementation's
	// createManualRoutes builds from Deps and the router mounts behind
	// each one's auth gate (D51); empty when it has none, and for a job or
	// a worker, which mounts no routes.
	ManualRoutes string

	// Identity is the variable of its identity service when it
	// authenticates with the identity runtime over the user model (D50),
	// empty otherwise, and IdentityField the identity config field the
	// service's config is read from (ir.IdentityConfigField).
	Identity      string
	IdentityField string

	// Callers is its callers field, which serviceAuthenticator reads, when
	// an operation has a service clause (D37), and Loopback the variable of
	// the server's loopback key, which that authenticator accepts beside
	// the field, when the server has one (section 9.9).
	Callers  string
	Loopback string

	// CORS is the variable of the origins its CORS field lists, which
	// loadCors reads, and CORSField that field, when a site of the stack
	// calls it (D55); both empty otherwise.
	CORS      string
	CORSField string

	// Database is the pool Deps.db holds, nil without one.
	Database *TypeScriptDatabase

	// Agents is true when Deps holds the agent dispatcher (D158, D183),
	// which the implementation's createAgentOptions gives its options
	// (AgentOptions, their variable), and a server's MCP endpoint holds
	// its calls to a run's autonomy (agents.mcpGate).
	Agents       bool
	AgentOptions string

	// The runtime pieces the API package's runtime.ts wires
	// (typescript_wiring.go): Runtime is true when it has any, and the
	// entrypoint builds Deps but them as DepsBase, asks the
	// implementation for the runtime options (RuntimeOptions, their
	// variable) and builds Deps with withRuntime. A server mounts their
	// routes beside the router (RuntimeRoutes, the handler's variable) and
	// serves their MCP tools on the API's endpoint (RuntimeMCP).
	// Providers is true when Deps holds the runtime providers (D159),
	// which providersOf reads from the config. Grants and AccessLog wrap
	// the router's authenticator in grantAuthenticator and give it the
	// access log (D156).
	Runtime        bool
	DepsBase       string
	RuntimeOptions string
	RuntimeRoutes  string
	RuntimeMCP     bool
	Providers      bool
	Grants         bool
	AccessLog      bool

	// MCP is true when the server serves the API's MCP endpoint (D81).
	MCP bool

	// ORM is whether Deps.orm holds the repositories of Database (D79),
	// which the entrypoint builds over its pool with Database.ORM.
	ORM bool

	// ORMFieldKeys is the expression of the API's config that holds the
	// keys of Database's Encrypted<T> fields, which the repositories open
	// with (D85); empty for a database without one.
	ORMFieldKeys string

	// Behaviors is true when the API serves a table's behavior operations
	// (D103) or has an operation marked @tableOperation (D106), which its
	// router runs through Deps.orm: main.ts hands the router the
	// repositories as RouterOptions.orm. False for a job's, a worker's and
	// a deriver's API, which mount no routes.
	Behaviors bool

	// Graphs are the version graphs whose operations the API serves
	// (D138): main.ts hands the router each one's facade, built over
	// Database's pool, as RouterOptions.graphs. None for a job's, a
	// worker's and a deriver's API.
	Graphs []TypeScriptGraphUse

	// AttachmentsBucket is the member of Deps that holds the bucket the
	// API's file operations sign a URL of (#723): main.ts hands it to the
	// router as RouterOptions.attachmentsBucket. Empty for an API that
	// serves none, and for a job's, a worker's and a deriver's.
	AttachmentsBucket string

	// Calls are Deps' clients.
	Calls []TypeScriptCall

	// Buckets are Deps' buckets (D54).
	Buckets []TypeScriptBucketUse

	// MemoryStores are Deps' memory stores, and RateLimitStore the one its
	// @rateLimit routes count in, nil for none (D75).
	MemoryStores   []TypeScriptMemoryStoreUse
	RateLimitStore *TypeScriptMemoryStore

	// MinClientVersions is the expression of its config that holds the
	// oldest version of each client it serves, which its router options
	// carry (D143); empty for an API whose config does not say
	// clientVersions.
	MinClientVersions string

	// IdempotencyStore is the expression that builds the store of its
	// @idempotent operations' keys over its database, on the table the API
	// package names, which its router options carry (D140); empty for an
	// API with none.
	IdempotencyStore string

	// Secrets are the Secret<T> fields of its config, which a Worker binds
	// as Secrets Store secrets and fetch.ts awaits per isolate (D64), and
	// OptionalSecrets those of them that are optional, whose binding the
	// Worker may lack (#702).
	Secrets         []string
	OptionalSecrets []string

	// MCPConfirmationKey is the expression of its config that holds the key
	// its MCP endpoint signs confirmations with, which its router is built
	// with (D93); empty for an API whose endpoint serves no tool that asks.
	MCPConfirmationKey string
}

// TypeScriptCall is a client in an API's Deps.
type TypeScriptCall struct {
	Field  string
	Client *TypeScriptClient
}

// TypeScriptBucketUse is a bucket in an API's Deps.
type TypeScriptBucketUse struct {
	Field  string
	Bucket *TypeScriptBucket

	// ContentAddressed is true for a bucket Deps holds as a content
	// bucket, which puts at most MaxBytes (D142).
	ContentAddressed bool
	MaxBytes         int64
}

// TypeScriptBucket is a bucket main.ts opens once, for every API that
// lists it (D54).
type TypeScriptBucket struct {
	Service string
	Var     string

	// From is the expression that reads the bucket's connection from the
	// first listing API's config, and Field the config field it reads.
	From  string
	Field string
}

// TypeScriptMemoryStoreUse is a memory store in an API's Deps.
type TypeScriptMemoryStoreUse struct {
	Field string
	Store *TypeScriptMemoryStore
}

// TypeScriptMemoryStore is a memory store the entrypoint opens once, for
// every API that lists it (D75): from the first listing API's config
// field, on Redis, or disabled when the field is unset or the connection
// is refused.
type TypeScriptMemoryStore struct {
	Service string
	Var     string

	// From is the expression that reads the store's connection from the
	// first listing API's config, and Field the config field it reads.
	From  string
	Field string
}

// TypeScriptDatabase is a database the server opens once, for every API on
// it: a pg Pool, or for a database whose DB service lists sqlite first the
// SQLite file its sqlite: URL names (D64).
type TypeScriptDatabase struct {
	Service string
	Var     string

	// SQLite is true for a database whose DB service lists sqlite first:
	// the entrypoint opens it with openSqlite, and Deps.db is a
	// SqlDatabase.
	SQLite bool

	// Field is the derived config field the connection is read from, and
	// From the expression that reads it from the first API's config.
	Field string
	From  string

	// Store is the variable of the identity store over the pool when an
	// API on the database authenticates with the identity runtime (D50),
	// empty otherwise. Descriptor is the name main.ts imports the
	// database's identity descriptor as, from DescriptorModule, an entry of
	// its TypeScript types package, DescriptorPackage.
	Store             string
	Descriptor        string
	DescriptorModule  string
	DescriptorPackage string

	// ORM is the name the entrypoint imports createOrm of the database's
	// repositories as (D79), from ORMModule, the ./orm entry of its
	// TypeScript types package, ORMPackage, or for a sqlite-first database
	// its ./orm/sqlite entry, whose createOrm opens them over the SQLite
	// file under Bun and the D1 binding on Workers (D182), when an API on
	// the database holds them in Deps.orm; empty otherwise.
	ORM        string
	ORMModule  string
	ORMPackage string

	// Graphs are the facades of the database's version graphs the
	// entrypoint imports (D138), from the ./versiongraph entry of its
	// TypeScript types package, each once.
	Graphs []*TypeScriptGraph
}

// TypeScriptClient is an SDK client of an API called, built once for
// every API that calls it, by the function Constructor names.
type TypeScriptClient struct {
	Service     string
	Var         string
	Constructor string
	Module      string
	Package     string
	Class       string

	// From is the expression that reads the callee's endpoint from the
	// first calling API's config, and Field the config field it reads.
	From  string
	Field string

	// InProcess is set on a server on Workers for a callee the server
	// serves itself: fetch.ts hands the client's requests to its own app,
	// since the call stays in process and its endpoint names no binding
	// (D64, section 18.2).
	InProcess bool

	// Loopback is true when the callee is an API the server serves itself
	// whose operations have a service clause: the client then sends the
	// server's loopback credential when its endpoint carries none.
	Loopback bool
}

// TypeScriptDocker is a TypeScript server's Dockerfile plan. Paths are
// relative to the build context, slash-separated.
type TypeScriptDocker struct {
	// Dockerfile is the Dockerfile's path, Context the package's directory
	// and Workspace the output root, the Bun workspace's root.
	Dockerfile string
	Context    string
	Workspace  string

	// ScalarTypeScript is superscalar's TypeScript binding, whose native/
	// directory the addon goes in, and ScalarWorkspace the Cargo
	// workspace that holds the binding and its napi crate.
	ScalarTypeScript string
	ScalarWorkspace  string

	// HTTPRuntime is the HTTP runtime's TypeScript package, and
	// ScalarLink the binding's path from its node_modules, where the
	// runtime's own build resolves superscalar.
	HTTPRuntime string
	ScalarLink  string

	// Rules is where the rules stage builds the behaviors' core's
	// WebAssembly module from, nil when the entrypoint opens the
	// repositories of no database whose fields are behaviors (D103).
	Rules *tsRulesDocker

	// VersionGraph is where the versiongraph stage builds the
	// version-graph core's WebAssembly module from, nil when the server
	// serves no version graph's operations (D138).
	VersionGraph *tsVersionGraphDocker

	// Runtime are the directories the image copies from the build stage:
	// the workspace's root, then every package the server reaches outside
	// it, each with the node_modules the install linked into it.
	Runtime []string

	// Image is what the stack adds to the image, nil when it adds
	// nothing (D144).
	Image *Image

	// Include are the paths the build context holds, and Exclude the
	// paths under them it leaves out: what a checkout builds, which the
	// image builds again.
	Include []string
	Exclude []string
}

// tsRulesDocker is the behaviors' core's npm package (D103), TypeScript,
// whose dist/ the build stage emits with the WebAssembly module the rules
// stage builds in it, from the engine's Cargo workspace, Workspace, which
// holds the core's crates, and the crates it reaches by path, Crates.
type tsRulesDocker struct {
	TypeScript string
	Workspace  string
	Crates     []string
}

// RulesModule is the WebAssembly module of the behaviors' core, which the
// package's index.js loads from its dist/.
const RulesModule = "superschematic_rules_wasm.wasm"

// Module is RulesModule, for the template.
func (d *tsRulesDocker) Module() string { return RulesModule }

// tsVersionGraphDocker is the version-graph runtime's npm package (D138),
// TypeScript, whose dist/ the build stage emits with the WebAssembly
// module the versiongraph stage builds in it, from the version-graph
// core's crate beside the package, Crate, which reaches no crate by path.
type tsVersionGraphDocker struct {
	TypeScript string
	Crate      string
}

// VersionGraphModule is the WebAssembly module of the version-graph core,
// which the package's index.js loads from its dist/.
const VersionGraphModule = "superschematic_versiongraph.wasm"

// Module is VersionGraphModule, for the template.
func (d *tsVersionGraphDocker) Module() string { return VersionGraphModule }

// tsReserved are the names main.ts declares or imports beside the served
// APIs' own: an API, database or client never takes one.
var tsReserved = []string{
	"Hono", "createLogger", "serviceAuthenticator", "serviceCredentialFor", "errorHandler", "notFoundHandler",
	"connectPostgres", "ping", "Pool", "Database", "Service", "DEFAULT_PORT", "SHUTDOWN_TIMEOUT_MS",
	"READINESS_TIMEOUT_MS", "BunServer", "BunRuntime", "Dependency", "logger", "exitCodes", "main",
	"listenPort", "configure", "construct", "connect", "drain", "app", "draining", "dependencies", "bun",
	"server", "stopping", "shutdown", "port", "process", "openBucket", "contentBucket",
	"parseIdentityConfigJSON", "postgresIdentityStore", "IdentityConfig", "identityConfig",
	"loadCors", "corsHandler", "corsMethods", "matchOperations", "handle",
	"run", "jobs", "workers", "queue", "stop", "signal", "started", "concurrency", "pools",
	"postgresQueue", "runWorker", "workerConcurrency", "loopbackServiceAuth", "Loopback",
	"openSqlite", "sqliteFile", "SqliteDatabase", "pingSqlDatabase", "openDatabase", "DatabaseConnection",
	"d1Database", "SqlDatabase", "unavailable",
	// fetch.ts's, the Workers entrypoint's (D64).
	"env", "build", "perIsolate", "requestIP", "stackEnvOf", "stackEnvWithSecrets", "binding", "serviceFetch",
	"openR2Bucket", "bindingName", "values", "bindings", "Handle",
	// scheduled.ts's, a job's on Cron Triggers (D64).
	"ScheduledController", "TIMEOUT_MS", "TRIES", "tryOnce", "untilAborted", "controller", "attempt", "last",
	"timer", "deadline", "failure",
	// A database on Hyperdrive's, and drain.ts's, a worker's on Workers (D92).
	"hyperdrivePool", "withHyperdrive", "hyperdriveDatabase", "pingPool", "respond", "GRACE_MS",
	"drainSeconds", "workerDrainSeconds",
	"Principal", "Logger", "cloudSqlSocket", "loadDatabase", "principal", "alive", "stopped",
	// A deriver's of tables (D146).
	"derivers", "deriver", "runDeriver", "inProcess", "DerivationAnswer", "DerivationItem", "answer", "item",
	"deriving", "DerivedTable", "table",
}

// PlanTypeScript plans the entrypoint of one TypeScript server. It refuses
// a server whose APIs register one method and path between them, since one
// app answers each once, and an API whose config holds no field for a
// database or a callee its Deps needs. With in.Job it plans a job's
// entrypoint instead (D52), and with in.Worker a worker's (D53): one API,
// whose Deps it builds as a server does, and no routes.
func PlanTypeScript(in TypeScriptInput) (*TypeScriptServer, error) {
	n := in.Naming.OrDefault()
	s := &TypeScriptServer{
		Stack:       in.Stack,
		Name:        in.Server,
		Package:     n.NpmServerPackage(in.Stack, in.Server),
		Naming:      n,
		Kind:        "server",
		RustVersion: RustVersion,
		BunVersion:  BunVersion,
	}
	switch {
	case in.Job != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: job %s runs a job of %d APIs; a job belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		i := slices.IndexFunc(o.Jobs, func(j tsrestgen.JobInfo) bool { return j.Name == in.Job.Name })
		if i < 0 {
			return nil, fmt.Errorf("stack %s: job %s runs %s of %s, which declares no such job", in.Stack, in.Server, in.Job.Name, o.SchemaName)
		}
		s.Kind = "job"
		s.Job = &Job{Name: o.Jobs[i].Name, Method: o.Jobs[i].Method, TimeoutSeconds: in.Job.TimeoutSeconds, Tries: in.Job.Retries + 1}
		if s.Job.TimeoutSeconds <= 0 {
			s.Job.TimeoutSeconds = ir.DefaultJobTimeoutSeconds
		}
	case in.Worker != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: worker %s runs a worker of %d APIs; a worker belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		i := slices.IndexFunc(o.Workers, func(w tsrestgen.WorkerInfo) bool { return w.Name == in.Worker.Name })
		if i < 0 {
			return nil, fmt.Errorf("stack %s: worker %s runs %s of %s, which declares no such worker", in.Stack, in.Server, in.Worker.Name, o.SchemaName)
		}
		w := o.Workers[i]
		if o.Deps.Database == "" {
			return nil, fmt.Errorf("stack %s: worker %s of %s claims queue %s, and %s connects to no database", in.Stack, in.Server, o.SchemaName, w.Queue, o.SchemaName)
		}
		if o.Deps.DatabaseTypesPackage == "" {
			return nil, fmt.Errorf("stack %s: worker %s of %s claims queue %s of %s, whose TypeScript types export no queue definitions", in.Stack, in.Server, o.SchemaName, w.Queue, o.Deps.Database)
		}
		grace := in.Worker.GraceSeconds
		if grace <= 0 {
			grace = ir.DefaultWorkerGraceSeconds
		}
		s.Kind = "worker"
		s.Worker = &TypeScriptWorker{
			Name:         w.Name,
			Method:       w.Method,
			Queue:        w.Queue,
			Concurrency:  w.Concurrency,
			GraceSeconds: grace,
			Definition:   tsgen.QueueConst(w.Queue),
			Module:       o.Deps.DatabaseTypesPackage + tsgen.QueuesEntry,
			Package:      o.Deps.DatabaseTypesPackage,
		}
		// A runtime piece's queue: its handler takes each message.
		s.Worker.planRuntimeHandler(o, w)
		if w.Behavior != nil {
			// A behavior worker runs its table's deadlines and changes
			// through the repositories, claimed over the pool, or over the
			// SQLite file or the D1 binding (D103, #702).
			if !o.DepsORM() {
				return nil, fmt.Errorf("stack %s: worker %s runs %s of %s, a behavior worker (D103), which runs %s through the repositories %s's TypeScript types export, and %s has none; enable outputs.types.typescript in %s's config", in.Stack, in.Server, w.Name, o.SchemaName, w.Behavior.Runs(), o.Deps.Database, o.SchemaName, o.Deps.Database)
			}
			s.Worker.Behavior = w.Behavior
			s.Worker.QueuesFunc = w.QueuesFunc()
		}
	case in.Deriver != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: deriver %s runs a deriver of %d APIs; a deriver belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		d := o.Deriver(in.Deriver.Name)
		if d == nil {
			return nil, fmt.Errorf("stack %s: deriver %s runs %s of %s, which declares no such deriver (D146)", in.Stack, in.Server, in.Deriver.Name, o.SchemaName)
		}
		s.Kind = "deriver"
		s.Deriver = &TypeScriptDeriver{DeriverInfo: *d}
	case len(in.APIs) == 0:
		return nil, fmt.Errorf("stack %s: server %s serves no API", in.Stack, in.Server)
	default:
		// A TypeScript router mounts every operation, a manually routed one
		// included, whose handler the implementation hands it.
		routes := Input{Stack: in.Stack, Server: in.Server}
		for _, a := range in.APIs {
			routes.APIs = append(routes.APIs, APIInput{Output: a.Routes})
		}
		if err := checkRoutes(routes); err != nil {
			return nil, err
		}
	}
	background := s.Background()
	if in.Workers {
		// A server's Workers entrypoint is fetch.ts, a job's scheduled.ts
		// and a worker's drain.ts, which claims for a window at each firing
		// of a Cron Trigger (D92); a deriver fills its fields in a loop no
		// Worker runs (D146).
		if s.Deriver != nil {
			return nil, fmt.Errorf("stack %s: %s %s runs on a platform whose artifact is a bundle, and a Workers entrypoint is built for a server, a job or a worker alone (D64, D92)", in.Stack, s.Kind, in.Server)
		}
		// A behavior worker's drain.ts runs its table's behavior queues at
		// each firing, as its main.ts runs them (D151).
		s.Workers = true
	}
	taken := names{}
	for _, name := range tsReserved {
		taken[name] = true
	}
	databases := map[string]*TypeScriptDatabase{}
	clients := map[string]*TypeScriptClient{}
	buckets := map[string]*TypeScriptBucket{}
	memoryStores := map[string]*TypeScriptMemoryStore{}
	for _, a := range in.APIs {
		o := a.Output
		stem := varStem(o.SchemaName)
		api := &TypeScriptAPI{
			Service:         o.SchemaName,
			Module:          taken.take(stem + "Api"),
			Impl:            taken.take(stem + "Impl"),
			Package:         o.PackageName,
			ImplPackage:     a.Implementation.Package,
			Deps:            taken.take(stem + "Deps"),
			Implementations: taken.take(stem + "Implementations"),
			ConfigLoader:    "loadEnvConfig",
		}
		if in.ImplementationRoot != "" {
			if rel, err := filepath.Rel(in.ImplementationRoot, a.Implementation.Dir); err == nil {
				api.ImplDir = filepath.ToSlash(rel)
			}
		}
		derived := map[string]string{}
		if a.Config != nil {
			for _, f := range a.Config.Derived {
				derived[string(f.Kind)+" "+f.Service] = f.Key
			}
			switch {
			case background && a.Config.CallersField != "":
				api.ConfigLoader = "loadBackgroundEnvConfig"
			case !background:
				api.Callers = a.Config.CallersField
			}
		}
		if o.HasEnvConfig {
			api.Config = taken.take(stem + "Config")
		}
		if a.Config != nil {
			for _, f := range a.Config.Fields {
				if f.Secret {
					api.Secrets = append(api.Secrets, f.Key)
					if !f.Required {
						api.OptionalSecrets = append(api.OptionalSecrets, f.Key)
					}
				}
			}
		}
		// A job or a worker serves no request: it answers no CORS,
		// establishes no end user, and verifies no caller (D52, D50).
		if a.CORS && !background {
			api.CORS = taken.take(stem + "Origins")
			api.CORSField = ir.CORSField(o.SchemaName)
		}
		if o.ChecksEndUsers() && !background {
			api.Authenticate = taken.take(stem + "Authenticate")
		}
		if o.HasManualRoutes() && !background {
			api.ManualRoutes = taken.take(stem + "ManualRoutes")
		}
		if o.Identity != nil && !background {
			api.Identity = taken.take(stem + "Identity")
			api.IdentityField = ir.IdentityConfigField(o.SchemaName)
		}
		if a.Config != nil && a.Config.ClientVersions && api.Config != "" && !background {
			// A job or a worker serves no request, so it turns no client
			// away (D143).
			api.MinClientVersions = api.Config + "." + ir.MinClientVersionsSetting
		}
		api.MCP = o.MCP != nil && !background
		// The runtime pieces its database declares (D159, D153, D157,
		// D152, D155, D156, D158).
		api.planRuntime(o, stem, taken, background)
		if o.MCP != nil && len(o.MCP.Confirming) > 0 && !background {
			// The MCP endpoint signs the confirmations of its tools that
			// ask with the secret the API's config holds (D93).
			if o.Deps.MCPConfirmationKey == "" || api.Config == "" {
				return nil, fmt.Errorf("stack %s: server %s serves %s, whose MCP endpoint runs %s only once the person confirms the call, and whose config holds no confirmation key; set outputs.api.mcpConfirmationKey (D93)", in.Stack, in.Server, o.SchemaName, strings.Join(o.MCP.Confirming, ", "))
			}
			api.MCPConfirmationKey = api.Config + "." + o.Deps.MCPConfirmationKey
		}
		if o.HasServiceCallers && !background && (api.Callers == "" || api.Config == "") {
			return nil, fmt.Errorf("stack %s: server %s serves %s, whose operations have a service clause, and whose config has no callers field", in.Stack, in.Server, o.SchemaName)
		}
		if db := o.Deps.Database; db != "" {
			d := databases[db]
			if d == nil {
				key, ok := derived[string(ir.EdgeSQL)+" "+db]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for its database %s", in.Stack, s.Kind, in.Server, o.SchemaName, db)
				}
				suffix := "Pool"
				if o.DepsSQLite() {
					suffix = "Database"
				}
				d = &TypeScriptDatabase{
					Service: db,
					Var:     taken.take(varStem(db) + suffix),
					SQLite:  o.DepsSQLite(),
					Field:   key,
					From:    api.Config + "." + key,
				}
				databases[db] = d
				s.Databases = append(s.Databases, d)
				if slices.Contains(in.CloudSQL, db) {
					s.CloudSQL = append(s.CloudSQL, db)
				}
			}
			api.Database = d
			if o.IdempotencyTable != nil && !background {
				// A job or a worker serves no request, so it keeps no key.
				store := "postgresIdempotencyStore"
				if d.SQLite {
					store = "sqliteIdempotencyStore"
				}
				api.IdempotencyStore = fmt.Sprintf("%s(%s, %s.idempotencyTable)", store, d.Var, api.Module)
			}
			if o.DepsORM() {
				api.ORM = true
				// orm.ts of a database whose fields are behaviors imports
				// the behaviors' core (D103).
				s.Rules = s.Rules || o.TableBehaviors
				if key := o.Deps.ORMFieldKeys; key != "" && api.Config != "" {
					api.ORMFieldKeys = api.Config + "." + key
					if d.SQLite {
						// The repositories on SQLite take a keyring read
						// already, which parseFieldKeys of the runtime's
						// ./sqlite reads under Bun; a Worker has no
						// node:crypto to read one with (D182).
						if s.Workers {
							return nil, fmt.Errorf("stack %s: server %s serves %s, whose database %s lists sqlite first and holds Encrypted<T> fields, and an environment places the server on Cloudflare Workers, where the database is a D1 binding: a Worker has no node:crypto to read the field keys with, so the repositories on D1 open no Encrypted<T> field yet (D85, D182)", in.Stack, in.Server, o.SchemaName, o.Deps.Database)
						}
						api.ORMFieldKeys = "parseFieldKeys(" + api.ORMFieldKeys + ")"
						s.SQLiteFieldKeys = true
					}
				}
				if d.ORM == "" {
					stem := varStem(d.Service)
					d.ORM = taken.take("create" + strings.ToUpper(stem[:1]) + stem[1:] + "Orm")
					d.ORMModule = o.Deps.DatabaseTypesPackage + tsgen.ORMEntry
					if d.SQLite {
						// The repositories on SQLite and D1 (D182).
						d.ORMModule = o.Deps.DatabaseTypesPackage + tsgen.ORMSQLiteEntry
					}
					d.ORMPackage = o.Deps.DatabaseTypesPackage
				}
			}
		}
		if o.HasGraphs() && !background && api.Database != nil {
			// The router runs a version graph's operations (D138) through
			// the graph's facade, which the entrypoint builds over the
			// database's pool.
			if s.VersionGraphPool == "" {
				s.VersionGraphPool = taken.take("versionGraphPool")
			}
			for _, set := range o.GraphSets {
				graph := api.Database.graph(set.Facade, o.GraphFacadeModule(), taken)
				api.Graphs = append(api.Graphs, TypeScriptGraphUse{Member: set.Member, Graph: graph, Pool: s.VersionGraphPool})
			}
		}
		if o.RouterNeedsORM() && !background {
			// The router runs the behavior operations (D103) and the
			// table operations (D106) through the repositories.
			if !api.ORM {
				what, decisions := o.ORMOperations()
				return nil, fmt.Errorf("stack %s: %s %s serves %s, whose %s (%s) run through Deps.orm, the repositories of %s, and the %s opens none", in.Stack, s.Kind, in.Server, o.SchemaName, strings.TrimPrefix(what, "the "), decisions, o.Deps.Database, s.Kind)
			}
			api.Behaviors = true
			// A table's files sign their URLs of the bucket the set names
			// (#723).
			api.AttachmentsBucket = o.RowFeatureBucketField()
		}
		if s.Worker != nil {
			if api.Database == nil {
				return nil, fmt.Errorf("stack %s: worker %s of %s opens no pool on %s, whose queue it claims", in.Stack, in.Server, o.SchemaName, o.Deps.Database)
			}
			// A sqlite-first database's worker claims on its SQLite file
			// with sqliteQueue (D53, amended).
			s.Worker.Database = api.Database
		}
		if s.Deriver != nil {
			// A deriver of tables pulls and settles through the
			// repositories, on Postgres alone (D146), as a behavior
			// worker runs its queues.
			if !api.ORM || api.Database.SQLite {
				return nil, fmt.Errorf("stack %s: deriver %s runs %s of %s, which fills the Derived fields of %s through the repositories %s's TypeScript types export for Postgres (D146), and %s has none: list postgres first in %s's outputs.sql.dialects and enable its TypeScript types", in.Stack, in.Server, s.Deriver.Name, o.SchemaName, strings.Join(s.Deriver.Schemas, ", "), o.Deps.Database, o.SchemaName, o.Deps.Database)
			}
			s.Deriver.Database = api.Database
		}
		if o.Identity != nil && !background {
			d := api.Database
			if d == nil || d.Service != o.Identity.AuthDB {
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime over %s, its authDb, and opens no pool on it to build its identity store over", in.Stack, in.Server, o.SchemaName, o.Identity.AuthDB)
			}
			if d.SQLite && s.Workers {
				// A Worker reaches a sqlite-first database through a D1
				// binding, which has no interactive transaction, and an
				// identity store's operation reads before it writes in
				// one (D92, D116).
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime over %s, its authDb, whose DB service lists sqlite first, and an environment places the server on Cloudflare Workers, where the database is a D1 binding: an identity store runs on a SQLite file or on Postgres, and none over D1 is built (D50, D92)", in.Stack, in.Server, o.SchemaName, o.Identity.AuthDB)
			}
			if d.Store == "" {
				stem := varStem(d.Service)
				d.Store = taken.take(stem + "IdentityStore")
				d.Descriptor = taken.take(stem + "IdentityDescriptor")
				d.DescriptorModule = o.Identity.DescriptorModule
				d.DescriptorPackage = n.NpmTypesPackage(d.Service)
			}
		}
		for _, call := range o.Deps.Calls {
			c := clients[call.Service]
			if c == nil {
				key, ok := derived[string(ir.EdgeHTTP)+" "+call.Service]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for %s, which it calls", in.Stack, s.Kind, in.Server, o.SchemaName, call.Service)
				}
				callee := varStem(call.Service)
				served := slices.ContainsFunc(in.APIs, func(a TypeScriptAPIInput) bool { return a.Output.SchemaName == call.Service })
				c = &TypeScriptClient{
					// A job has no app of its own, so it calls through a
					// service binding even an API it serves.
					InProcess:   s.Workers && served && !background,
					Service:     call.Service,
					Var:         taken.take(callee + "Client"),
					Constructor: taken.take("new" + strings.ToUpper(callee[:1]) + callee[1:] + "Client"),
					Module:      taken.take(callee + "Sdk"),
					Package:     call.Package,
					Class:       call.Client,
					From:        api.Config + "." + key,
					Field:       key,
				}
				clients[call.Service] = c
				s.Clients = append(s.Clients, c)
			}
			api.Calls = append(api.Calls, TypeScriptCall{Field: call.Field, Client: c})
		}
		for _, b := range o.Deps.Buckets {
			h := buckets[b.Service]
			if h == nil {
				key, ok := derived[string(ir.EdgeBucket)+" "+b.Service]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for bucket %s, which it lists", in.Stack, s.Kind, in.Server, o.SchemaName, b.Service)
				}
				h = &TypeScriptBucket{
					Service: b.Service,
					Var:     taken.take(varStem(b.Service) + "Bucket"),
					From:    api.Config + "." + key,
					Field:   key,
				}
				buckets[b.Service] = h
				s.Buckets = append(s.Buckets, h)
			}
			api.Buckets = append(api.Buckets, TypeScriptBucketUse{Field: b.Field, Bucket: h, ContentAddressed: b.ContentAddressed, MaxBytes: b.MaxBytes})
		}
		for _, m := range o.Deps.MemoryStores {
			h := memoryStores[m.Service]
			if h == nil {
				key, ok := derived[string(ir.EdgeMemoryStore)+" "+m.Service]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for memory store %s, which it lists", in.Stack, s.Kind, in.Server, o.SchemaName, m.Service)
				}
				h = &TypeScriptMemoryStore{
					Service: m.Service,
					Var:     taken.take(varStem(m.Service) + "Store"),
					From:    api.Config + "." + key,
					Field:   key,
				}
				memoryStores[m.Service] = h
				s.MemoryStores = append(s.MemoryStores, h)
			}
			api.MemoryStores = append(api.MemoryStores, TypeScriptMemoryStoreUse{Field: m.Field, Store: h})
			if m.Service == o.Deps.RateLimitStore && !background {
				api.RateLimitStore = h
			}
		}
		s.APIs = append(s.APIs, api)
	}
	slices.Sort(s.CloudSQL)
	s.planLoopback(taken)
	s.planPackage()
	if in.DefaultOutputRoot != "" && !tsgen.SamePath(in.OutputRoot, in.DefaultOutputRoot) {
		// The image holds each mirror whole in place of its package.
		dirs := maps.Clone(in.PackageDirs)
		for _, a := range in.APIs {
			m, err := planTypeScriptMirror(in.OutputRoot, in.DefaultOutputRoot, a)
			if err != nil {
				return nil, err
			}
			s.Mirrors = append(s.Mirrors, m)
			if dirs != nil {
				dirs[a.Implementation.Package] = m.Dir
			}
		}
		in.PackageDirs = dirs
	}
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planTypeScriptDocker(in, s, dir)
	if err != nil {
		return nil, err
	}
	if s.Docker != nil {
		if s.Docker.Image, s.Docker.Include, err = planImage(in.Image, s.Kind, s.Name, in.RepositoryRoot, s.Docker.Include); err != nil {
			return nil, fmt.Errorf("stack %s: %w", in.Stack, err)
		}
	}
	return s, nil
}

// planLoopback gives a server that calls an API it serves itself, whose
// operations have a service clause, its loopback key, marks each such
// client to send it, and has each API with a callers field accept it
// (docs/stack-model.md, section 9.9). A job or a worker has none: an API
// it calls is served by another deployable, whose connector gives the call
// its credential.
func (s *TypeScriptServer) planLoopback(taken names) {
	if s.Background() {
		return
	}
	serves := make([]string, len(s.APIs))
	guarded := map[string]bool{}
	for i, a := range s.APIs {
		serves[i] = a.Service
		guarded[a.Service] = a.Callers != ""
	}
	for _, c := range s.Clients {
		c.Loopback = guarded[c.Service]
	}
	if !slices.ContainsFunc(s.Clients, func(c *TypeScriptClient) bool { return c.Loopback }) {
		return
	}
	s.Loopback = &Loopback{Var: taken.take("loopback"), Serves: serves}
	for _, a := range s.APIs {
		if a.Callers != "" {
			a.Loopback = s.Loopback.Var
		}
	}
}

// RouterOptions are the members of the options main.ts builds the API's
// router with: its authenticator or its identity service, the service
// authenticator over its callers field, and the handlers of its manually
// registered operations, which the router mounts behind their auth gates.
func (a *TypeScriptAPI) RouterOptions() []string {
	var out []string
	if a.Authenticate != "" {
		out = append(out, "authenticate: "+a.Authenticate)
	}
	if a.Identity != "" {
		out = append(out, "identity: "+a.Identity)
	}
	switch {
	case a.Callers != "" && a.Loopback != "":
		out = append(out, "authenticateService: serviceAuthenticator("+a.Loopback+".accept("+a.Config+"."+a.Callers+"))")
	case a.Callers != "":
		out = append(out, "authenticateService: serviceAuthenticator("+a.Config+"."+a.Callers+")")
	}
	if a.ManualRoutes != "" {
		out = append(out, "manualRoutes: "+a.ManualRoutes)
	}
	if a.RateLimitStore != nil {
		// Its @rateLimit routes count in the store, and in process while
		// the store is unavailable (D75).
		out = append(out, fmt.Sprintf("rateLimit: { store: memoryStoreRateLimitStore(%s, %s) }", a.RateLimitStore.Var, tsQuote(a.Service)))
	}
	var mcp []string
	if a.MCPConfirmationKey != "" {
		// Its MCP endpoint signs confirmations with the secret (D93).
		mcp = append(mcp, "confirmationKey: "+a.MCPConfirmationKey)
	}
	if a.Agents && a.MCP {
		// A call an agent run makes with its token is held to the run's
		// autonomy, by the tool's @autonomy (D158, D183).
		mcp = append(mcp, "callGate: "+a.Deps+".agents.mcpGate")
	}
	// The runtime pieces' tools beside the routes' (D152, D155).
	mcp = append(mcp, a.runtimeMCPOptions()...)
	if len(mcp) > 0 {
		out = append(out, "mcp: { "+strings.Join(mcp, ", ")+" }")
	}
	if a.MinClientVersions != "" {
		// Its routes turn away a client under its minimum (D143).
		out = append(out, "minClientVersions: "+a.MinClientVersions)
	}
	if a.IdempotencyStore != "" {
		// Its @idempotent routes keep their keys in its database (D140).
		out = append(out, "idempotencyStore: "+a.IdempotencyStore)
	}
	// The access log its database keeps (D156).
	out = append(out, a.runtimeRouterOptions()...)
	if a.Behaviors {
		// Its behavior operations (D103) and its table operations (D106)
		// run through the repositories Deps holds.
		out = append(out, "orm: "+a.Deps+".orm")
	}
	if len(a.Graphs) > 0 {
		// Its graph operations (D138) run through each graph's facade,
		// built over the database's pool.
		members := make([]string, 0, len(a.Graphs))
		for _, use := range a.Graphs {
			members = append(members, fmt.Sprintf("%s: new %s(%s(%s))", use.Member, use.Graph.Class, use.Pool, a.Database.Var))
		}
		out = append(out, "graphs: { "+strings.Join(members, ", ")+" }")
	}
	if a.AttachmentsBucket != "" {
		// Its file operations sign their URLs of the bucket (#723).
		out = append(out, "attachmentsBucket: "+a.Deps+"."+a.AttachmentsBucket)
	}
	return out
}

// IdentityStoreImports are the identity stores main.ts imports from the
// runtime's ./identity entry: postgresIdentityStore over a pool, and
// sqlDatabaseIdentityStore over a sqlite-first database's file (D116).
func (s *TypeScriptServer) IdentityStoreImports() []string {
	var postgres, sqlite bool
	for _, d := range s.Databases {
		if d.Store == "" {
			continue
		}
		if d.SQLite {
			sqlite = true
		} else {
			postgres = true
		}
	}
	var out []string
	if postgres {
		out = append(out, "postgresIdentityStore")
	}
	if sqlite {
		out = append(out, "sqlDatabaseIdentityStore")
	}
	return out
}

// Identity reports whether an API of the server authenticates with the
// identity runtime, so main.ts imports its ./identity entry.
func (s *TypeScriptServer) Identity() bool {
	return slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.Identity != "" })
}

// AnyCORS reports whether a site of the stack calls an API the server
// serves, which then answers CORS (D55).
func (s *TypeScriptServer) AnyCORS() bool {
	return slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.CORS != "" })
}

// RuntimeImports are the names main.ts imports from the HTTP runtime's
// main entry.
func (s *TypeScriptServer) RuntimeImports() []string {
	out := []string{"createLogger"}
	if slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.Callers != "" }) {
		out = append(out, "serviceAuthenticator")
	}
	if len(s.Clients) > 0 {
		out = append(out, "serviceCredentialFor")
	}
	if s.Loopback != nil {
		out = append(out, "loopbackServiceAuth", "type Loopback")
	}
	if s.AnyCORS() {
		out = append(out, "corsHandler", "corsMethods", "loadCors", "matchOperations")
	}
	if len(s.PostgresDatabases()) > 0 {
		out = append(out, "type Database")
	}
	if len(s.SQLiteDatabases()) > 0 {
		out = append(out, "type DatabaseConnection")
	}
	if len(s.Clients) > 0 {
		out = append(out, "type Service")
	}
	if len(s.MemoryStores) > 0 {
		out = append(out, "disabledMemoryStore", "type MemoryStore", "type MemoryStoreConnection")
	}
	if slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.RateLimitStore != nil }) {
		out = append(out, "memoryStoreRateLimitStore")
	}
	if slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool {
		return slices.ContainsFunc(a.Buckets, func(b TypeScriptBucketUse) bool { return b.ContentAddressed })
	}) {
		// Deps holds a content bucket over the bucket main.ts opens (D142).
		out = append(out, "contentBucket")
	}
	for _, store := range []string{"postgresIdempotencyStore", "sqliteIdempotencyStore"} {
		// The store of an API's idempotency keys, over its database (D140).
		if slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return strings.HasPrefix(a.IdempotencyStore, store+"(") }) {
			out = append(out, store)
		}
	}
	if request, _ := s.routesAuthenticate(); request {
		// The runtime's routes authenticate as the router does (D153).
		out = append(out, "requestAuthenticator")
	}
	return out
}

// RoutesIdentity reports whether an API's runtime routes authenticate
// through its identity service, which identityAuthenticator adapts.
func (s *TypeScriptServer) RoutesIdentity() bool {
	_, identity := s.routesAuthenticate()
	return identity
}

// PostgresDatabases are the databases the entrypoint opens a pg Pool on.
func (s *TypeScriptServer) PostgresDatabases() []*TypeScriptDatabase {
	var out []*TypeScriptDatabase
	for _, d := range s.Databases {
		if !d.SQLite {
			out = append(out, d)
		}
	}
	return out
}

// SQLiteDatabases are the databases whose DB services list sqlite first,
// which the entrypoint opens with openSqlite (D64). With one, each
// database /readyz pings is a ping and a close rather than a pool.
func (s *TypeScriptServer) SQLiteDatabases() []*TypeScriptDatabase {
	var out []*TypeScriptDatabase
	for _, d := range s.Databases {
		if d.SQLite {
			out = append(out, d)
		}
	}
	return out
}

// BundleEntry is the module a Workers server's bundle.ts bundles: its
// fetch.ts, a job's scheduled.ts (D64), or a worker's drain.ts (D92).
func (s *TypeScriptServer) BundleEntry() string {
	switch {
	case s.Job != nil:
		return TypeScriptScheduledFile
	case s.Worker != nil:
		return TypeScriptDrainFile
	}
	return TypeScriptFetchFile
}

// NodeCompat reports whether the Workers bundle runs with Node's modules:
// it opens a Postgres database through Hyperdrive with node-postgres, which
// imports them (D92), so bundle.ts keeps them external. The repositories
// of a D1 database import none but node:async_hooks, which nodejs_als
// gives (D182).
func (s *TypeScriptServer) NodeCompat() bool {
	return s.Workers && len(s.PostgresDatabases()) > 0
}

// DrainSeconds is how long each firing of a worker's drain.ts claims unless
// its Worker's WORKER_DRAIN_SECONDS binding says otherwise (D92).
func (s *TypeScriptServer) DrainSeconds() int { return ir.DefaultWorkerDrainSeconds }

// WorkersRuntimeImports are the names a Workers entrypoint imports from the
// HTTP runtime's main entry: main.ts's, with a Postgres database's
// connection read as any DatabaseConnection, which a Hyperdrive binding is
// (D92), in place of the Database a pool opens over, and the string
// bindings' type for an identity config.
func (s *TypeScriptServer) WorkersRuntimeImports() []string {
	var out []string
	for _, name := range s.RuntimeImports() {
		if name == "type Database" {
			name = "type DatabaseConnection"
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if s.Identity() {
		// identityConfig reads the identity config field from the string
		// bindings (D92).
		out = append(out, "type StackEnv")
	}
	return out
}

// WorkflowClass is the class a job's scheduled.ts exports, which the
// cloudflare target's job runner starts an instance of, and BundleBinding
// the binding it reads its bundle's digest from (D64, amended).
func (s *TypeScriptServer) WorkflowClass() string { return ir.JobRunClass }
func (s *TypeScriptServer) BundleBinding() string { return ir.BundleDigestVariable }

// APIsWithConfig reports whether an API of the server has an EnvConfig,
// which fetch.ts reads from the Worker's bindings (D64).
func (s *TypeScriptServer) APIsWithConfig() bool {
	return slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.Config != "" })
}

// ReadsBindings reports whether fetch.ts reads the Worker's string and
// secret bindings when it builds the server: for an API's config, the
// origins its CORS field lists, or its identity config (D92).
func (s *TypeScriptServer) ReadsBindings() bool {
	return s.APIsWithConfig() || s.AnyCORS() || s.Identity()
}

// OptionalSecretBindings are the optional Secret<T> fields of every API's
// config, sorted: of SecretBindings, those a Worker may have no binding of,
// a provider's secret setting where the environment's provider of its slot
// reads none (D159, #702).
func (s *TypeScriptServer) OptionalSecretBindings() []string {
	var out []string
	for _, a := range s.APIs {
		out = append(out, a.OptionalSecrets...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// SecretBindings are the Secret<T> fields of every API's config, sorted:
// the Secrets Store secret bindings fetch.ts awaits once per isolate
// (D64).
func (s *TypeScriptServer) SecretBindings() []string {
	var out []string
	for _, a := range s.APIs {
		out = append(out, a.Secrets...)
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// BindsServices reports whether a client of a server on Workers reaches
// its callee through a service binding: the callee is another server's.
func (s *TypeScriptServer) BindsServices() bool {
	return slices.ContainsFunc(s.Clients, func(c *TypeScriptClient) bool { return !c.InProcess })
}

// CallsInProcess reports whether a client of a server on Workers calls an
// API the server serves itself, which fetch.ts hands to its own app (D64,
// section 18.2).
func (s *TypeScriptServer) CallsInProcess() bool {
	return slices.ContainsFunc(s.Clients, func(c *TypeScriptClient) bool { return c.InProcess })
}

// WorkersImports are the names fetch.ts imports from the HTTP runtime's
// ./workers entry (D64): the per-isolate builder and the client's address
// always; the readers of the string bindings, and of the secret ones when
// a config has a Secret<T> field, when it reads a config; and the binding
// lookup, with the transport through a service binding, when the server
// calls an API another server serves, lists a bucket or reaches a D1
// database.
func (s *TypeScriptServer) WorkersImports() []string {
	var out []string
	if s.BindsServices() || len(s.Buckets) > 0 || len(s.Databases) > 0 {
		out = append(out, "binding")
	}
	if s.Job == nil && s.Worker == nil {
		// A job's scheduled.ts and a worker's drain.ts build per firing and
		// serve no request.
		out = append(out, "perIsolate", "requestIP")
	}
	if s.BindsServices() {
		out = append(out, "serviceFetch")
	}
	if s.ReadsBindings() || s.Worker != nil {
		// drain.ts reads its concurrency and its window from the bindings.
		out = append(out, "stackEnvOf")
	}
	if len(s.SecretBindings()) > 0 {
		out = append(out, "stackEnvWithSecrets")
	}
	if s.WakesQueues() {
		out = append(out, "installQueueWakes", "queueWakesSettled")
	}
	return out
}

// WakesQueues reports whether a Workers entrypoint installs the queue
// wakes its Worker's QUEUE_WAKES binding names and hands each invocation's
// waitUntil the wakes it sent (D151): one that reaches a database, a
// Postgres one through Hyperdrive or a D1 one through its binding, on
// either of which a worker on Workers claims (#702), so its enqueues, its
// deadlines and its events may leave a worker a message due before the
// worker's next firing.
func (s *TypeScriptServer) WakesQueues() bool {
	return s.Workers && len(s.Databases) > 0
}

// QueueWakeClass is the class a worker's drain.ts exports, the worker's
// Workflow, which a wake creates an instance of (D151).
func (s *TypeScriptServer) QueueWakeClass() string { return ir.QueueWakeClass }

// ImportsPostgres reports whether main.ts imports the HTTP runtime's
// ./postgres entry, for its pools. The repositories of a sqlite-first
// database import no pg (./orm/sqlite, D182).
func (s *TypeScriptServer) ImportsPostgres() bool {
	return len(s.PostgresDatabases()) > 0
}

// planPackage plans package.json's dependencies: each served API package,
// its implementation, each callee's SDK and the TypeScript types of each
// database an identity store reads, or a worker's queue is of, from the
// workspace; the HTTP
// runtime, which the workspace's root overrides with a checkout or the
// registry serves; and the third-party packages main.ts imports, the
// OpenTelemetry packages of the runtime's ./telemetry entry among them.
func (s *TypeScriptServer) planPackage() {
	deps := map[string]string{
		s.Naming.HTTPRuntimeNpmPackage: "*",
		"hono":                         tsrestgen.HonoVersion,
	}
	maps.Copy(deps, TelemetryPackages)
	if s.Workers {
		// fetch.ts and scheduled.ts export through ./workers-telemetry.
		maps.Copy(deps, WorkersTelemetryPackages)
	}
	for _, a := range s.APIs {
		deps[a.Package] = "workspace:*"
		deps[a.ImplPackage] = "workspace:*"
	}
	for _, c := range s.Clients {
		deps[c.Package] = "workspace:*"
	}
	for _, d := range s.Databases {
		if d.Store != "" {
			// main.ts imports the database's identity descriptor.
			deps[d.DescriptorPackage] = "workspace:*"
		}
	}
	if s.Worker != nil {
		// main.ts imports the queue's definition from the database's types.
		deps[s.Worker.Package] = "workspace:*"
	}
	for _, d := range s.Databases {
		if d.ORM != "" {
			// The entrypoint opens the database's repositories (D79).
			deps[d.ORMPackage] = "workspace:*"
		}
	}
	if s.Rules {
		// Which run a behavior through the behaviors' core (D103).
		deps[s.Naming.RulesNpmPackage] = "*"
	}
	if s.VersionGraphPool != "" {
		// The entrypoint builds a graph's facade over the version-graph
		// package's Postgres client (D138).
		deps[s.Naming.VersionGraphNpmPackage] = "*"
	}
	dev := map[string]string{
		"@types/node": tsrestgen.NodeTypesVersion,
		"typescript":  tsrestgen.TypeScriptVersion,
	}
	if s.ImportsPostgres() {
		deps["pg"] = pgVersion
		dev["@types/pg"] = tsrestgen.PGTypesVersion
	}
	if len(s.CloudSQL) > 0 {
		deps[cloudSQLConnectorPackage] = cloudSQLConnectorVersion
	}
	if len(s.Buckets) > 0 {
		deps[storagePackage] = storageNodeVer
	}
	if len(s.MemoryStores) > 0 {
		deps[redisClientPackage] = redisClientVersion
	}
	sorted := func(m map[string]string) []NpmDependency {
		out := make([]NpmDependency, 0, len(m))
		for _, name := range slices.Sorted(maps.Keys(m)) {
			out = append(out, NpmDependency{Name: name, Spec: m[name]})
		}
		return out
	}
	s.Dependencies, s.DevDependencies = sorted(deps), sorted(dev)
}

// planTypeScriptDocker plans the Dockerfile, or says why there is none.
// The image builds superscalar's Node addon from the checkout the naming
// file's [paths] scalar_typescript names, which no published package ships
// yet, and the HTTP runtime's package from the one [paths]
// http_runtime_typescript names, and installs the workspace, whose root
// overrides both packages with their checkouts. Every directory it reads
// must lie under the build context.
func planTypeScriptDocker(in TypeScriptInput, s *TypeScriptServer, dir string) (*TypeScriptDocker, string, error) {
	if in.RepositoryRoot == "" {
		return nil, "no repository root to take as the build context", nil
	}
	if in.Paths.ScalarTypeScript == "" {
		return nil, "the naming file's [paths] scalar_typescript is unset, and the image builds superscalar's Node addon from a checkout, since no published package ships it yet; set it to a superscalar checkout's bindings/typescript", nil
	}
	if in.Paths.HTTPRuntimeTypeScript == "" {
		return nil, "the naming file's [paths] http_runtime_typescript is unset, and the image builds the HTTP runtime's package from a checkout, since none is published yet; set it to a superschematic checkout's runtime/http/typescript", nil
	}
	if s.servesGraphs() && in.Paths.VersionGraphTypeScript == "" {
		// A graph's facade loads the version-graph core's WebAssembly module,
		// which no published package ships: the image builds it from the
		// checkout the package lies in.
		return nil, fmt.Sprintf("the %s serves a version graph's operations (D138) through the version-graph package, %s, whose WebAssembly module no published package ships, and the naming file's [paths] versiongraph_typescript, the package the image builds it beside, is unset; set it to a superschematic checkout's runtime/versiongraph/typescript", s.Kind, s.Naming.VersionGraphNpmPackage), nil
	}
	root, err := filepath.Abs(in.RepositoryRoot)
	if err != nil {
		return nil, "", err
	}
	rel := func(p string) (string, bool) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", false
		}
		r, err := filepath.Rel(root, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(r), true
	}
	outside := func(what, p string) string {
		return fmt.Sprintf("%s %s lies outside the repository root %s, the build context", what, p, root)
	}
	d := &TypeScriptDocker{}
	var ok bool
	if d.Workspace, ok = rel(in.OutputRoot); !ok {
		return nil, outside("the output root", in.OutputRoot), nil
	}
	if d.Context, ok = rel(dir); !ok {
		return nil, outside("the server's package", dir), nil
	}
	d.Dockerfile = path.Join(d.Context, DockerFile)
	if d.ScalarTypeScript, ok = rel(in.Paths.ScalarTypeScript); !ok {
		return nil, outside("superscalar's TypeScript binding", in.Paths.ScalarTypeScript), nil
	}
	d.ScalarWorkspace = path.Dir(path.Dir(d.ScalarTypeScript))
	if d.HTTPRuntime, ok = rel(in.Paths.HTTPRuntimeTypeScript); !ok {
		return nil, outside("the HTTP runtime's TypeScript package", in.Paths.HTTPRuntimeTypeScript), nil
	}
	link, err := filepath.Rel(filepath.Join(in.Paths.HTTPRuntimeTypeScript, "node_modules"), in.Paths.ScalarTypeScript)
	if err != nil {
		return nil, "", err
	}
	d.ScalarLink = filepath.ToSlash(link)

	// An install reads the workspace's root, its lockfile when the output
	// root's install wrote one, and the manifest of every member, which
	// patterns name, so the context does not depend on which services were
	// built before the stack. The server runs the packages it imports,
	// whole: every types package, whichever the generated packages it
	// imports reach, and each API package, SDK and implementation it
	// depends on. Outside the default output root the implementations are
	// the root's mirrors, its members under impl/ (D65), and no
	// implementation's own directory goes in.
	var implementations string
	if len(s.Mirrors) > 0 {
		implementations = d.Workspace + "/" + MirrorRoot + "/*"
	} else {
		if in.ImplementationRoot == "" {
			return nil, "no repository root to find the implementations under", nil
		}
		if implementations, ok = rel(filepath.Join(in.ImplementationRoot, filepath.FromSlash(in.Naming.OrDefault().TypeScriptImplementationGlob()))); !ok {
			return nil, outside("the TypeScript implementations at", in.Naming.OrDefault().TypeScriptImplementationGlob()), nil
		}
	}
	d.Include = []string{
		d.Workspace + "/package.json",
		d.Workspace + "/bun.lock*",
		d.Workspace + "/types/typescript",
		d.Workspace + "/sdk/typescript/*/package.json",
		d.Workspace + "/api/*/package.json",
		d.Workspace + "/server/*/*/package.json",
		implementations + "/package.json",
		d.Context,
	}
	var runtime []string
	for _, name := range slices.Sorted(maps.Keys(in.PackageDirs)) {
		r, ok := rel(in.PackageDirs[name])
		if !ok {
			return nil, outside("package "+name+", at", in.PackageDirs[name]), nil
		}
		d.Include = append(d.Include, r)
		if r != d.Workspace && !strings.HasPrefix(r, d.Workspace+"/") {
			runtime = append(runtime, r)
		}
	}
	d.Include = append(d.Include, d.ScalarTypeScript, d.HTTPRuntime,
		d.ScalarWorkspace+"/Cargo.toml", d.ScalarWorkspace+"/Cargo.lock", d.ScalarWorkspace+"/crates")
	if versionGraph := in.Paths.VersionGraphTypeScript; versionGraph != "" {
		// The workspace's root overrides the version-graph runtime too, so
		// an install reads its manifest, though the server may not import
		// it.
		vg, ok := rel(versionGraph)
		if !ok {
			return nil, outside("the version-graph runtime's TypeScript package", versionGraph), nil
		}
		if !s.servesGraphs() {
			d.Include = append(d.Include, vg+"/package.json")
		} else {
			// The versiongraph stage builds the core's WebAssembly module
			// from its crate beside the package, as the package's build does,
			// and the build stage emits the package's dist/ with it, which
			// the workspace's root overrides the package with (D138).
			crate, ok := rel(filepath.Join(filepath.Dir(versionGraph), "rust"))
			if !ok {
				return nil, outside("the version-graph core's crate beside", versionGraph), nil
			}
			d.VersionGraph = &tsVersionGraphDocker{TypeScript: vg, Crate: crate}
			d.Include = append(d.Include, vg, crate)
		}
	}
	if rules := in.Paths.RulesTypeScript; rules != "" && !s.Rules {
		// And the behaviors' core (D103), whose package the server does
		// not reach: it opens the repositories of no database whose
		// fields are behaviors.
		r, ok := rel(rules)
		if !ok {
			return nil, outside("the behaviors' core's TypeScript package", rules), nil
		}
		d.Include = append(d.Include, r+"/package.json")
	}
	if s.Rules {
		// The rules stage builds the core's WebAssembly module from the
		// engine's Cargo workspace, and the build stage emits the
		// package's dist/ with it, which the workspace's root overrides
		// the package with (D103).
		rules, why := planTSRulesDocker(in.Paths.RulesTypeScript, s.Kind, root, rel)
		if rules == nil {
			return nil, why, nil
		}
		d.Rules = rules
		d.Include = append(d.Include, rules.TypeScript, rules.Workspace)
		d.Include = append(d.Include, rules.Crates...)
	}
	slices.Sort(d.Include)
	d.Include = slices.Compact(d.Include)
	slices.Sort(runtime)
	d.Runtime = append([]string{d.Workspace}, slices.Compact(runtime)...)
	// What a checkout builds, which the image builds again.
	d.Exclude = []string{d.ScalarTypeScript + "/dist", d.ScalarTypeScript + "/native", d.HTTPRuntime + "/dist"}
	if d.Rules != nil {
		d.Exclude = append(d.Exclude, d.Rules.TypeScript+"/dist")
	}
	if d.VersionGraph != nil {
		d.Exclude = append(d.Exclude, d.VersionGraph.TypeScript+"/dist", d.VersionGraph.TypeScript+"/prebuilt")
	}
	return d, "", nil
}

// planTSRulesDocker plans the rules stage of a checkout's Dockerfile from
// the behaviors' core's npm package at rulesTS (D103), or says why it cannot:
// the package, the engine's Cargo workspace of the checkout it lies in,
// runtime/engine/rust, which builds the core's WebAssembly module
// (superschematic-rules-wasm), and the crates that workspace reaches by
// path must be under the build context, root.
func planTSRulesDocker(rulesTS, kind, root string, rel func(string) (string, bool)) (*tsRulesDocker, string) {
	if rulesTS == "" {
		return nil, fmt.Sprintf("the %s opens the repositories of a database whose fields are behaviors (D103), which run them through the behaviors' core, and the naming file's [paths] rules_typescript, the core's npm package, whose WebAssembly module the image builds from the checkout it lies in, is unset; set it to a superschematic checkout's runtime/rules/typescript", kind)
	}
	pkg, ok := rel(rulesTS)
	if !ok {
		return nil, fmt.Sprintf("the behaviors' core's TypeScript package %s lies outside the repository root %s, the build context", rulesTS, root)
	}
	workspace, crates, why := rulesWorkspace(rulesTS, "the behaviors' core's package", root, rel)
	if why != "" {
		return nil, why
	}
	return &tsRulesDocker{TypeScript: pkg, Workspace: workspace, Crates: crates}, ""
}

// manifestName reads the name of the manifest at dir.
func manifestName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, TypeScriptPackageFile))
	if err != nil {
		return "", err
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, TypeScriptPackageFile), err)
	}
	return manifest.Name, nil
}

// ImplementationPackage is the npm name of the TypeScript implementation
// of service at dir: its package.json's name, or the scaffold's when dir
// holds no package.json yet. The server's package.json depends on the
// implementation by that name, so a package.json without one fails.
func ImplementationPackage(n naming.Naming, service, dir string) (string, error) {
	name, err := manifestName(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return n.OrDefault().NpmImplementationPackage(service), nil
	}
	if err != nil {
		return "", fmt.Errorf("the implementation of %s: %w", service, err)
	}
	if name == "" {
		return "", fmt.Errorf("the implementation of %s, at %s, has a package.json with no name, and the server's package.json depends on it by name; name it, as the scaffold names it %s", service, dir, n.OrDefault().NpmImplementationPackage(service))
	}
	return name, nil
}

// WriteTypeScript writes the entrypoint package into dir: main.ts,
// package.json and tsconfig.json, and the Dockerfile with its ignore file
// when s.Docker is planned. A job's main.ts runs the job
// (job.ts.tmpl), a worker's its claim loop (worker.ts.tmpl), and a
// behavior worker's its table's behavior queues (behavior_worker.ts.tmpl,
// D103); every
// main.ts template takes the wiring they share from wiring.ts.tmpl. A
// server some environment places on Workers also gets fetch.ts, its
// Workers entrypoint, bundle.ts, which bundles it, and workers.d.ts,
// which declares the cloudflare:workers module fetch.ts imports; a job
// some environment places on Cron Triggers gets scheduled.ts in place of
// fetch.ts (D64), and a worker some environment places on Workers drain.ts
// (D92).
func WriteTypeScript(s *TypeScriptServer, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	main := "main.ts.tmpl"
	switch {
	case s.Job != nil:
		main = "job.ts.tmpl"
	case s.Worker != nil && s.Worker.Behavior != nil:
		main = "behavior_worker.ts.tmpl"
	case s.Worker != nil:
		main = "worker.ts.tmpl"
	case s.Deriver != nil:
		main = "table_deriver.ts.tmpl"
	}
	files := []struct{ template, name string }{
		{main, TypeScriptMainFile},
		{"package.json.tmpl", TypeScriptPackageFile},
		{"tsconfig.json.tmpl", TypeScriptConfigFile},
	}
	if s.Docker != nil {
		files = append(files, struct{ template, name string }{"Dockerfile.ts.tmpl", DockerFile}, struct{ template, name string }{"dockerignore.ts.tmpl", DockerIgnoreFile})
	}
	if s.Workers && s.Job != nil {
		// A job's Workers entrypoint runs on its Cron Trigger (D64).
		files = append(files,
			struct{ template, name string }{"scheduled.ts.tmpl", TypeScriptScheduledFile},
			struct{ template, name string }{"bundle.ts.tmpl", TypeScriptBundleFile},
			struct{ template, name string }{"workers.d.ts.tmpl", TypeScriptWorkersTypesFile},
		)
	} else if s.Workers && s.Worker != nil {
		// A worker's claims for a window at each firing of its Cron
		// Trigger (D92), a behavior worker's on each of its table's
		// behavior queues (D151).
		drain := "drain.ts.tmpl"
		if s.Worker.Behavior != nil {
			drain = "behavior_drain.ts.tmpl"
		}
		files = append(files,
			struct{ template, name string }{drain, TypeScriptDrainFile},
			struct{ template, name string }{"bundle.ts.tmpl", TypeScriptBundleFile},
			struct{ template, name string }{"workers.d.ts.tmpl", TypeScriptWorkersTypesFile},
		)
	} else if s.Workers {
		files = append(files,
			struct{ template, name string }{"fetch.ts.tmpl", TypeScriptFetchFile},
			struct{ template, name string }{"bundle.ts.tmpl", TypeScriptBundleFile},
			struct{ template, name string }{"workers.d.ts.tmpl", TypeScriptWorkersTypesFile},
		)
	}
	for _, f := range files {
		if err := render(f.template, filepath.Join(dir, f.name), s); err != nil {
			return fmt.Errorf("servergen: %s %s: %w", s.Kind, s.Name, err)
		}
	}
	return nil
}
