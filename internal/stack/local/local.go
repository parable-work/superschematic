// Package local is the core's `local` target (docs/stack-model.md,
// sections 6.3 and 8.3): it runs an environment of a stack on the machine
// at hand. A server is a process run from its generated entrypoint: a Go
// server's binary, built from its module, a TypeScript server's main.ts on
// Bun (D51), or a Rust server's binary, built from its crate with cargo
// (D57). Every database deployable of the environment shares one Postgres
// container, with a database per hosted DB schema, a sql edge derives a
// connection string to that container, and an http edge the callee's
// loopback URL with a service credential the caller signs with the edge's
// Ed25519 key (D37), and gives the callee the edge's public key for its
// callers field. Every bucket deployable shares one storage emulator,
// fake-gcs-server, in a container of its own, with a bucket per Bucket
// service, and a bucket edge derives the bucket's name and the emulator's
// endpoint (D54).
//
// The target registers like any other (Register), but the core registers
// it, so a binary with no extension linked runs `stack dev`. Its resource
// vocabulary is the core's own, the `local` provider's types
// (TypeContainer, TypeDatabase, TypeKeyPair, TypeProcess, TypeJob,
// TypeBucket), not a Pulumi
// package's: no published provider schema describes a local process, and
// nothing but the local provisioner applies them (D30, amended: the local
// target).
//
// The platforms and connectors are pure. Provisioner applies their graph:
// it runs Docker, the migration runner, `go build`, `bun install`, `cargo
// build` and the servers, and creates each bucket through the emulator's
// API.
package local

import (
	"encoding/json"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The names the target registers.
const (
	// Target is the target's name.
	Target = "local"

	// Provider is the provider each platform of the target runs on: the
	// machine at hand. Its http edges are its own connector's, so the
	// generic connector never serves two of its platforms (D63).
	Provider = "local"

	// ServerPlatform runs a server as a process; DatabasePlatform runs a
	// database as databases on the environment's Postgres container;
	// JobPlatform runs a job as a process on its schedule, or once on
	// demand (D52).
	ServerPlatform   = "local.process"
	DatabasePlatform = "local.postgres"
	JobPlatform      = "local.job"

	// SQLitePlatform keeps a database whose DB services list sqlite first
	// in a SQLite file per hosted DB schema, under the environment's state
	// directory (D64): the target places such a database on it
	// (TargetSpec.DatabasePlatforms), so a TypeScript API's Deps.db is the
	// SqlDatabase its first dialect types.
	SQLitePlatform = "local.sqlite"

	// SitePlatform serves a site's built files and its config from a file
	// server in the provisioner, on loopback (D55).
	SitePlatform = "local.site"

	// SiteConnector connects a site to a process whose API it calls: the
	// process's loopback URL.
	SiteConnector = "local.site-process"

	// BucketPlatform keeps a bucket on the environment's storage emulator,
	// fake-gcs-server, which speaks GCS's API (D54).
	BucketPlatform = "local.gcs"

	// WorkerPlatform runs a worker as a process with no port, ready once it
	// starts, beside the servers (D53).
	WorkerPlatform = "local.worker"

	// RunnerPlatform runs an engine's runner as a process with no port,
	// ready once it starts, as a worker is (D58): its own process, which
	// takes the runner's lock on the environment's Postgres, as it does on
	// gcp.
	RunnerPlatform = "local.runner"

	// DeriverPlatform runs a deriver as a process with no port, ready once
	// it starts, as a worker is (D74).
	DeriverPlatform = "local.deriver"

	// SQLConnector connects a process to a database on the container;
	// HTTPConnector connects a process to one it calls. JobSQLConnector
	// and JobHTTPConnector connect a job, whose edges are its API's, the
	// same way.
	SQLConnector     = "local.process-postgres"
	HTTPConnector    = "local.process-process"
	JobSQLConnector  = "local.job-postgres"
	JobHTTPConnector = "local.job-process"

	// SQLiteConnector, JobSQLiteConnector and WorkerSQLiteConnector
	// connect a process, a job and a worker to a SQLite file: its sqlite:
	// URL (D64). A worker claims its queue's messages on the file (D53,
	// amended), beside the server that enqueues them, each with a
	// connection of its own that waits for the other's write lock.
	SQLiteConnector       = "local.process-sqlite"
	JobSQLiteConnector    = "local.job-sqlite"
	WorkerSQLiteConnector = "local.worker-sqlite"

	// BucketConnector and JobBucketConnector connect a process and a job
	// to a bucket on the storage emulator (D54).
	BucketConnector    = "local.process-gcs"
	JobBucketConnector = "local.job-gcs"

	// WorkerSQLConnector, WorkerHTTPConnector and WorkerBucketConnector
	// connect a worker, whose edges are its API's, as a job's connectors
	// do.
	WorkerSQLConnector    = "local.worker-postgres"
	WorkerHTTPConnector   = "local.worker-process"
	WorkerBucketConnector = "local.worker-gcs"

	// RunnerSQLConnector connects an engine's runner to its engine
	// database, its one edge (D58).
	RunnerSQLConnector = "local.runner-postgres"

	// DeriverSQLConnector, DeriverSQLiteConnector, DeriverHTTPConnector and
	// DeriverBucketConnector connect a deriver, whose edges are its API's,
	// as a worker's connectors do (D74).
	DeriverSQLConnector    = "local.deriver-postgres"
	DeriverSQLiteConnector = "local.deriver-sqlite"
	DeriverHTTPConnector   = "local.deriver-process"
	DeriverBucketConnector = "local.deriver-gcs"

	// ProvisionerName is the provisioner the target names.
	ProvisionerName = "local"

	// PolicyNoDomain refuses an environment that sets a domain: a local
	// server is reached on loopback, and no DNS platform writes records
	// for it.
	PolicyNoDomain = "local-no-domain"

	// PolicyNoParameters refuses a parameterized environment: the local
	// target runs one copy of each environment.
	PolicyNoParameters = "local-no-parameters"

	// PolicyDistinctPorts refuses two processes, a process and a
	// container, or two containers, that listen on one port.
	PolicyDistinctPorts = "local-distinct-ports"
)

// The resource types the target's platforms emit: the `local` provider's.
const (
	// TypeContainer is a Docker container: the environment's Postgres, and
	// its storage emulator (D54). It may give the image a command and its
	// arguments, and says how the provisioner knows it is ready: a command
	// run inside it, or a path it answers over HTTP.
	TypeContainer = "local:docker/container:Container"

	// TypeDatabase is a database on a Postgres container.
	TypeDatabase = "local:postgres/database:Database"

	// TypeSQLiteDatabase is a SQLite file under the environment's state
	// directory, which the provisioner creates (D64). Its url output is the
	// file's sqlite: URL.
	TypeSQLiteDatabase = "local:sqlite/database:Database"

	// TypeKeyPair is an http edge's Ed25519 key pair. The provisioner
	// generates it into the environment's state directory, never the
	// output root, and the caller signs its service credential with the
	// private key. Its public key is an output the callee's callers field
	// references.
	TypeKeyPair = "local:serviceauth/keyPair:KeyPair"

	// TypeProcess is a process run from its entrypoint: built from its
	// module or its crate, or run by Bun. A server's listens on its port
	// and is ready once its readiness path answers; a worker's, an
	// engine's runner's and a deriver's have no port and are ready once
	// they start (ReadinessStarted, D53, D58, D74).
	TypeProcess = "local:process/process:Process"

	// TypeJob is a job built from its entrypoint module, which the
	// provisioner runs on its schedule while the environment runs (D52).
	TypeJob = "local:process/job:Job"

	// TypeSite is a site the provisioner builds once and serves from a
	// file server of its own, with the site's config and its single-page
	// fallback (D55).
	TypeSite = "local:site/site:Site"

	// TypeBucket is a bucket on the environment's storage emulator, which
	// the provisioner creates through the emulator's JSON API (D54).
	TypeBucket = "local:storage/bucket:Bucket"
)

// The languages of a process, as its node's language property names them:
// a server's API language in lower case.
const (
	// LanguageGo is a Go server, whose module `go build` builds.
	LanguageGo = "go"

	// LanguageTypeScript is a TypeScript server, whose main.ts Bun runs
	// after one `bun install` at the output root, the Bun workspace's root
	// (D51).
	LanguageTypeScript = "typescript"

	// LanguageRust is a Rust server, job or worker, whose crate `cargo
	// build --release` builds into the program directory's Cargo target
	// directory, RustTargetDir, and whose binary, the one its Cargo.toml
	// names, runs from there (D57, D65).
	LanguageRust = "rust"
)

// Register adds the local target, its four platforms and six connectors,
// the schema of each resource type they emit, and its provisioner. The
// provisioner is a new Provisioner with its defaults.
func Register(r *registry.Registry) error {
	for _, spec := range []registry.PlatformSpec{
		{
			Name:      ServerPlatform,
			Kind:      ir.DeployableServer,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(serverSettings),
			// A process serves plain HTTP, where a browser drops a Secure
			// cookie: the session cookie of an API over the user model
			// (D50) is session, without Secure.
			IdentityConfig: identityConfig,
			NameOf:         processName,
			AddressOf:      processAddress,
			// A browser on this machine reaches an exposed server where
			// another server does (D55).
			PublicAddressOf: processAddress,
			Lower:           lowerProcess,
		},
		{
			Name:            SitePlatform,
			Kind:            ir.DeployableSite,
			Settings:        json.RawMessage(serverSettings),
			NameOf:          processName,
			AddressOf:       siteAddress,
			PublicAddressOf: siteAddress,
			Lower:           lowerSite,
		},
		{
			Name: JobPlatform,
			Kind: ir.DeployableJob,
			// A job is written in its API's language, as a server is
			// (D52, D65).
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    processName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerJob,
		},
		{
			Name: WorkerPlatform,
			Kind: ir.DeployableWorker,
			// In its API's language, as a job is (D53, D65).
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    processName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerWorker,
		},
		{
			Name: RunnerPlatform,
			Kind: ir.DeployableRunner,
			// In its API's language, as a worker is (D58).
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    processName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerRunner,
		},
		{
			Name: DeriverPlatform,
			Kind: ir.DeployableDeriver,
			// In its API's language, TypeScript or Rust (D74).
			Languages: []string{registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    processName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerDeriver,
		},
		{
			Name:      DatabasePlatform,
			Kind:      ir.DeployableDatabase,
			Dialects:  []string{registry.SQLDialectPostgres},
			NameOf:    databaseDeployableName,
			AddressOf: databaseAddress,
			Lower:     lowerDatabase,
		},
		{
			Name:     SQLitePlatform,
			Kind:     ir.DeployableDatabase,
			Dialects: []string{registry.SQLDialectSQLite},
			NameOf:   databaseDeployableName,
			// A file has no address: each edge derives its file's URL.
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerSQLite,
		},
		{
			Name:      BucketPlatform,
			Kind:      ir.DeployableBucket,
			NameOf:    bucketDeployableName,
			AddressOf: bucketAddress,
			Lower:     lowerBucket,
		},
	} {
		spec.Provider = Provider
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	for _, spec := range []registry.ConnectorSpec{
		{Name: SQLConnector, Edge: ir.EdgeSQL, From: ServerPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: HTTPConnector, Edge: ir.EdgeHTTP, From: ServerPlatform, To: ServerPlatform, Connect: connectHTTP},
		{Name: JobSQLConnector, Edge: ir.EdgeSQL, From: JobPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: SQLiteConnector, Edge: ir.EdgeSQL, From: ServerPlatform, To: SQLitePlatform, Connect: connectSQLite},
		{Name: JobSQLiteConnector, Edge: ir.EdgeSQL, From: JobPlatform, To: SQLitePlatform, Connect: connectSQLite},
		{Name: JobHTTPConnector, Edge: ir.EdgeHTTP, From: JobPlatform, To: ServerPlatform, Connect: connectHTTP},
		{Name: SiteConnector, Edge: ir.EdgeSite, From: SitePlatform, To: ServerPlatform, Connect: connectSite},
		{Name: BucketConnector, Edge: ir.EdgeBucket, From: ServerPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: JobBucketConnector, Edge: ir.EdgeBucket, From: JobPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: WorkerSQLConnector, Edge: ir.EdgeSQL, From: WorkerPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: WorkerSQLiteConnector, Edge: ir.EdgeSQL, From: WorkerPlatform, To: SQLitePlatform, Connect: connectSQLite},
		{Name: WorkerHTTPConnector, Edge: ir.EdgeHTTP, From: WorkerPlatform, To: ServerPlatform, Connect: connectHTTP},
		{Name: WorkerBucketConnector, Edge: ir.EdgeBucket, From: WorkerPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: RunnerSQLConnector, Edge: ir.EdgeSQL, From: RunnerPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: DeriverSQLConnector, Edge: ir.EdgeSQL, From: DeriverPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: DeriverSQLiteConnector, Edge: ir.EdgeSQL, From: DeriverPlatform, To: SQLitePlatform, Connect: connectSQLite},
		{Name: DeriverHTTPConnector, Edge: ir.EdgeHTTP, From: DeriverPlatform, To: ServerPlatform, Connect: connectHTTP},
		{Name: DeriverBucketConnector, Edge: ir.EdgeBucket, From: DeriverPlatform, To: BucketPlatform, Connect: connectBucket},
	} {
		if err := r.RegisterConnector(spec); err != nil {
			return err
		}
	}
	if err := r.RegisterProvisioner(registry.ProvisionerSpec{Name: ProvisionerName, Provisioner: &Provisioner{}}); err != nil {
		return err
	}
	types := make(map[string]json.RawMessage, len(resourceTypes))
	for typ, schema := range resourceTypes {
		types[typ] = json.RawMessage(schema)
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name: Target,
		Platforms: map[ir.DeployableKind]string{
			ir.DeployableServer:   ServerPlatform,
			ir.DeployableDatabase: DatabasePlatform,
			ir.DeployableJob:      JobPlatform,
			ir.DeployableSite:     SitePlatform,
			ir.DeployableBucket:   BucketPlatform,
			ir.DeployableWorker:   WorkerPlatform,
			ir.DeployableRunner:   RunnerPlatform,
			ir.DeployableDeriver:  DeriverPlatform,
		},
		DatabasePlatforms: map[string]string{registry.SQLDialectSQLite: SQLitePlatform},
		Values:            json.RawMessage(targetValues),
		Provisioner:       ProvisionerName,
		ResourceTypes:     types,
		Policies: []registry.PolicyRule{
			{Name: PolicyNoDomain, Check: checkNoDomain},
			{Name: PolicyNoParameters, Check: checkNoParameters},
			{Name: PolicyDistinctPorts, Check: checkDistinctPorts},
		},
	})
}

// targetValues is the schema of a local environment's values: the
// Postgres image and the host port its container publishes, and the
// storage emulator's (D54).
const targetValues = `{
  "type": "object",
  "properties": {
    "postgresImage": {"type": "string", "minLength": 1},
    "postgresPort": {"type": "integer", "minimum": 1, "maximum": 65535},
    "storageImage": {"type": "string", "minLength": 1},
    "storagePort": {"type": "integer", "minimum": 1, "maximum": 65535}
  },
  "additionalProperties": false
}`

// identityConfig is the identity config a process runs each API over the
// user model with unless its environment sets the API's identity config
// field: a session cookie without Secure, since the process serves plain
// HTTP.
const identityConfig = `{"cookie":{"secure":false}}`

// serverSettings is the schema of a server's settings: the port it
// listens on.
const serverSettings = `{
  "type": "object",
  "properties": {
    "port": {"type": "integer", "minimum": 1, "maximum": 65535}
  },
  "additionalProperties": false
}`

// resourceTypes are the schemas of the local provider's resource types.
// Every object is closed, as a pinned provider schema's are.
var resourceTypes = map[string]string{
	TypeContainer: `{
	  "type": "object",
	  "required": ["name", "image", "ports"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "image": {"type": "string", "minLength": 1},
	    "ports": {"type": "array", "items": {
	      "type": "object",
	      "required": ["host", "hostPort", "containerPort"],
	      "properties": {
	        "host": {"type": "string"},
	        "hostPort": {"type": "integer", "minimum": 1, "maximum": 65535},
	        "containerPort": {"type": "integer", "minimum": 1, "maximum": 65535}
	      },
	      "additionalProperties": false
	    }},
	    "env": {"type": "array", "items": {
	      "type": "object",
	      "required": ["name", "value"],
	      "properties": {"name": {"type": "string"}, "value": {"type": "string"}},
	      "additionalProperties": false
	    }},
	    "labels": {"type": "object", "additionalProperties": {"type": "string"}},
	    "command": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}},
	    "args": {"type": "array", "items": {"type": "string"}},
	    "readiness": {"oneOf": [
	      {"type": "object", "required": ["exec"], "properties": {"exec": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}}}, "additionalProperties": false},
	      {"type": "object", "required": ["http"], "properties": {"http": {"type": "string", "pattern": "^/"}}, "additionalProperties": false}
	    ]}
	  },
	  "additionalProperties": false
	}`,
	TypeDatabase: `{
	  "type": "object",
	  "required": ["name", "service", "container", "url"],
	  "properties": {
	    "name": {"type": "string", "pattern": "^[a-z_][a-z0-9_]*$", "maxLength": 63},
	    "service": {"type": "string", "minLength": 1},
	    "container": {"type": "string", "minLength": 1},
	    "url": {"type": "string", "minLength": 1}
	  },
	  "additionalProperties": false
	}`,
	TypeSQLiteDatabase: `{
	  "type": "object",
	  "required": ["name", "service", "file"],
	  "properties": {
	    "name": {"type": "string", "pattern": "^[a-z_][a-z0-9_]*$"},
	    "service": {"type": "string", "minLength": 1},
	    "file": {"type": "string", "pattern": "^databases/[a-z_][a-z0-9_]*\\.sqlite$"}
	  },
	  "additionalProperties": false
	}`,
	TypeKeyPair: `{
	  "type": "object",
	  "required": ["caller", "callee", "algorithm"],
	  "properties": {
	    "caller": {"type": "string", "minLength": 1},
	    "callee": {"type": "string", "minLength": 1},
	    "algorithm": {"enum": ["Ed25519"]}
	  },
	  "additionalProperties": false
	}`,
	TypeProcess: `{
	  "type": "object",
	  "required": ["name", "module", "language", "readiness"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "module": {"type": "string", "minLength": 1},
	    "language": {"enum": ["go", "typescript", "rust"]},
	    "kind": {"enum": ["worker", "runner", "deriver"]},
	    "port": {"type": "integer", "minimum": 1, "maximum": 65535},
	    "readiness": {"type": "string", "pattern": "^(/|started$)"},
	    "env": {"type": "array", "items": {
	      "type": "object",
	      "required": ["name"],
	      "properties": {"name": {"type": "string", "minLength": 1}, "value": {}, "secret": {"type": "string", "minLength": 1}},
	      "additionalProperties": false
	    }}
	  },
	  "additionalProperties": false
	}`,
	TypeSite: `{
	  "type": "object",
	  "required": ["name", "dir", "build", "output", "port", "config"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "dir": {"type": "string", "minLength": 1},
	    "build": {"type": "string", "minLength": 1},
	    "output": {"type": "string", "minLength": 1},
	    "fallback": {"type": "string", "minLength": 1},
	    "port": {"type": "integer", "minimum": 1, "maximum": 65535},
	    "config": {
	      "type": "object",
	      "required": ["apis"],
	      "properties": {"apis": {"type": "object", "additionalProperties": {
	        "type": "object",
	        "required": ["url"],
	        "properties": {"url": {"type": "string", "minLength": 1}},
	        "additionalProperties": false
	      }}},
	      "additionalProperties": false
	    }
	  },
	  "additionalProperties": false
	}`,
	TypeBucket: `{
	  "type": "object",
	  "required": ["name", "service", "container", "endpoint"],
	  "properties": {
	    "name": {"type": "string", "pattern": "^[a-z0-9][a-z0-9_-]{1,61}[a-z0-9]$"},
	    "service": {"type": "string", "minLength": 1},
	    "container": {"type": "string", "minLength": 1},
	    "endpoint": {"type": "string", "pattern": "^https?://"}
	  },
	  "additionalProperties": false
	}`,
	TypeJob: `{
	  "type": "object",
	  "required": ["name", "module", "language", "api", "job", "timeZone", "timeoutSeconds", "retries"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "module": {"type": "string", "minLength": 1},
	    "language": {"enum": ["go", "typescript", "rust"]},
	    "api": {"type": "string", "minLength": 1},
	    "job": {"type": "string", "minLength": 1},
	    "schedule": {"type": "string", "minLength": 1},
	    "timeZone": {"type": "string", "minLength": 1},
	    "timeoutSeconds": {"type": "integer", "minimum": 1},
	    "retries": {"type": "integer", "minimum": 0},
	    "env": {"type": "array", "items": {
	      "type": "object",
	      "required": ["name"],
	      "properties": {"name": {"type": "string", "minLength": 1}, "value": {}, "secret": {"type": "string", "minLength": 1}},
	      "additionalProperties": false
	    }}
	  },
	  "additionalProperties": false
	}`,
}
