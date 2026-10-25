// Package stacktest is the stack model's acceptance extension
// (docs/stack-model.md, section 6.7, and D10): it registers a fake target
// with its platforms, connectors, DNS platform and provisioner through the
// public registry package alone, with no core edit, and its fixture is a
// stack over the acme-shop services. The core's resolver tests use it too.
//
// The fake platforms lower to resource types of a provider named `fake`,
// shaped like the gcp target's (section 7.2): a server is a service with
// its own account, a database an instance with a database per hosted
// schema, a bucket a private bucket, a sql edge a client grant, an http
// edge an invoker grant and a bucket edge an object user grant.
package stacktest

import (
	"encoding/json"
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The names the extension registers.
const (
	Name = "fake"

	Target      = "fake"
	DNSPlatform = "fake.dns"
	Provisioner = "fake"

	// Provider is the provider each fake platform runs on. A platform of
	// another provider reaches a fake server, and a fake deployable a
	// server of another provider, through the core's generic connector
	// (D63).
	Provider = "fake"

	// RunPlatform runs Go, TypeScript and Rust servers, the last the
	// servers of the Rust APIs whose apps AppPlatform runs (D78);
	// SQLPlatform runs Postgres databases.
	RunPlatform = "fake.run"
	SQLPlatform = "fake.sql"

	// EdgePlatform runs only TypeScript servers and LitePlatform only
	// SQLite databases, as Cloudflare's Workers and D1 do (section 6.8).
	// No fake connector reaches EdgePlatform; a deployable of another provider
	// reaches an exposed server on EdgePlatform through the generic
	// connector, and one on EdgePlatform calls an exposed server of
	// another provider the same way (D63).
	EdgePlatform = "fake.edge"
	LitePlatform = "fake.lite"

	// JobPlatform runs Go and TypeScript jobs, as gcp runs a job on Cloud
	// Run jobs with a scheduler for its schedule (D52).
	JobPlatform = "fake.job"

	// WorkerPlatform runs Go and TypeScript workers, as gcp runs a worker
	// on a Cloud Run worker pool (D53).
	WorkerPlatform = "fake.worker"

	// RunnerPlatform runs an engine's runner as a pool of one instance, as
	// gcp runs it on a Cloud Run worker pool (D58); RunnerSQLConnector
	// connects it to its engine database, its one edge.
	RunnerPlatform     = "fake.runner"
	RunnerSQLConnector = "fake.runner-sql"

	// DeriverPlatform runs TypeScript and Rust derivers as a pool of one
	// instance, as gcp runs one on a Cloud Run worker pool (D74);
	// DeriverSQLConnector, DeriverHTTPConnector and DeriverBucketConnector
	// connect it, whose edges are its API's, as a worker's connectors do.
	DeriverPlatform        = "fake.deriver"
	DeriverSQLConnector    = "fake.deriver-sql"
	DeriverHTTPConnector   = "fake.deriver-run"
	DeriverBucketConnector = "fake.deriver-storage"

	// AppPlatform runs Rust apps as a service, as gcp runs a server on
	// Cloud Run (D78); AppSQLConnector, AppHTTPConnector and
	// AppBucketConnector connect it, whose edges are its API's, as a job's
	// connectors do.
	AppPlatform        = "fake.app"
	AppSQLConnector    = "fake.app-sql"
	AppHTTPConnector   = "fake.app-run"
	AppBucketConnector = "fake.app-storage"

	SQLConnector  = "fake.run-sql"
	HTTPConnector = "fake.run-run"

	// JobSQLConnector and JobHTTPConnector connect a job, whose edges are
	// its API's, as the server's connectors do.
	JobSQLConnector  = "fake.job-sql"
	JobHTTPConnector = "fake.job-run"

	// SitePlatform serves a site's files from storage under their digest,
	// as gcp serves them from a bucket behind its load balancer, and
	// SiteConnector connects a site to a server whose API it calls (D55).
	SitePlatform  = "fake.site"
	SiteConnector = "fake.site-run"

	// BucketPlatform keeps a bucket, as gcp keeps one in Cloud Storage;
	// BucketConnector and JobBucketConnector grant a server's or a job's
	// account its objects (D54).
	BucketPlatform     = "fake.storage"
	BucketConnector    = "fake.run-storage"
	JobBucketConnector = "fake.job-storage"

	// BundlePlatform runs TypeScript servers whose artifact is a bundle,
	// as a Workers platform runs a Worker's files (D64): a script that reads
	// each file of the bundle from the program's artifacts, with its hash.
	// No connector reaches it.
	BundlePlatform = "fake.bundle"

	// CronPlatform runs TypeScript jobs whose artifact is a bundle, as
	// Cloudflare's Cron Triggers run a job's Worker (D64): the same script
	// as BundlePlatform's, for a job. CronD1Connector reaches from it.
	CronPlatform = "fake.cron"

	// WorkerSQLConnector, WorkerHTTPConnector and WorkerBucketConnector
	// connect a worker, whose edges are its API's, as a job's connectors
	// do.
	WorkerSQLConnector    = "fake.worker-sql"
	WorkerHTTPConnector   = "fake.worker-run"
	WorkerBucketConnector = "fake.worker-storage"

	// BundleD1Connector connects a server on BundlePlatform to a database on
	// LitePlatform as a Worker reaches D1 (D64): through a binding named
	// after the field, of the database's id. JobLiteConnector connects a job
	// to one with a sqlite: URL, as the local target derives one for a
	// SQLite file, and WorkerLiteConnector a worker, which claims its
	// queue's messages on the file (D53, amended). CronD1Connector connects
	// a job on CronPlatform as BundleD1Connector connects a server, as a
	// job's Worker reaches D1.
	BundleD1Connector   = "fake.bundle-lite"
	JobLiteConnector    = "fake.job-lite"
	WorkerLiteConnector = "fake.worker-lite"
	CronD1Connector     = "fake.cron-lite"

	// BundleHTTPConnector connects a server on BundlePlatform to an API it
	// serves itself, as a call within one Worker stays in process (D64,
	// docs/stack-model.md, section 18.2): its value is the deployable's name as
	// an https URL, with no binding and no credential. It refuses a call to
	// another server, which a Worker makes through a service binding the
	// fake platform does not model.
	BundleHTTPConnector = "fake.bundle-bundle"

	// FakeIssuer is the issuer of the fake target's service credentials,
	// which a callee's callers field names.
	FakeIssuer = "https://issuer.fake.test"

	// PolicyPublic refuses a public resource of a deployable that is not
	// exposed; PolicyHighAvailability refuses a database that is not
	// highly available in an environment whose values set production.
	PolicyPublic           = "public-only-if-exposed"
	PolicyHighAvailability = "production-ha"
)

// The resource types the fake platforms emit.
const (
	TypeAccount  = "fake:iam/account:Account"
	TypeGrant    = "fake:iam/grant:Grant"
	TypeService  = "fake:run/service:Service"
	TypeRoute    = "fake:run/route:Route"
	TypeSecret   = "fake:secrets/secret:Secret"
	TypeInstance = "fake:sql/instance:Instance"

	// TypeSecretVersion is a version of a secret that holds a value the
	// graph references, such as the private key of a generic connector's
	// key node (D63).
	TypeSecretVersion = "fake:secrets/version:Version"
	TypeDatabase      = "fake:sql/database:Database"
	TypeRecord        = "fake:dns/record:Record"
	TypeJob           = "fake:run/job:Job"
	TypeSchedule      = "fake:scheduler/job:Job"
	TypeSite          = "fake:storage/site:Site"
	TypeBucket        = "fake:storage/bucket:Bucket"
	TypePool          = "fake:run/pool:Pool"
	TypeScript        = "fake:edge/script:Script"
)

// Extension is the fake extension. Its Provisioner records the calls a
// test makes, and the target's deploy seams record theirs in the same
// log; Register creates each one that is nil. Tools are the command-line
// tools the fake provisioner declares, which a generated CI job installs;
// nil declares none. NoJobRunner registers the target with no job runner,
// as a target that runs no job on demand, and NoQueueRunner with no queue
// runner, as a target whose databases `stack queue` does not reach.
// PrivilegelessMigrations registers Migrations as a runner whose databases
// have no roles, as the cloudflare target's on D1
// (registry.PrivilegelessMigrations).
type Extension struct {
	Provisioner *FakeProvisioner
	State       *FakeState
	Secrets     *FakeSecrets
	Migrations  *FakeMigrations
	Bootstrap   *FakeBootstrap
	Builder     *FakeBuilder
	CI          *FakeCI
	Jobs        *FakeJobs
	Queues      *FakeQueues
	Sites       *FakeSites
	Bundles     *FakeBundles
	PreDestroy  *FakePreDestroy
	Tools       []registry.CLITool
	NoJobRunner bool

	NoQueueRunner bool

	PrivilegelessMigrations bool
}

// Name is the extension's name.
func (*Extension) Name() string { return Name }

// Register adds the fake target and everything it names.
func (e *Extension) Register(r *registry.Registry) error {
	if e.Provisioner == nil {
		e.Provisioner = &FakeProvisioner{}
	}
	if e.State == nil {
		e.State = &FakeState{}
	}
	if e.Secrets == nil {
		e.Secrets = &FakeSecrets{}
	}
	if e.Migrations == nil {
		e.Migrations = &FakeMigrations{}
	}
	if e.Bootstrap == nil {
		e.Bootstrap = &FakeBootstrap{}
	}
	if e.Builder == nil {
		e.Builder = &FakeBuilder{}
	}
	if e.CI == nil {
		e.CI = &FakeCI{}
	}
	if e.Jobs == nil {
		e.Jobs = &FakeJobs{}
	}
	if e.Queues == nil {
		e.Queues = &FakeQueues{}
	}
	if e.Sites == nil {
		e.Sites = &FakeSites{}
	}
	if e.Bundles == nil {
		e.Bundles = &FakeBundles{}
	}
	if e.PreDestroy == nil {
		e.PreDestroy = &FakePreDestroy{}
	}
	e.Migrations.log = e.Provisioner
	e.Bootstrap.log = e.Provisioner
	e.Builder.log = e.Provisioner
	e.Jobs.log = e.Provisioner
	e.Queues.log = e.Provisioner
	e.Sites.log = e.Provisioner
	e.Bundles.log = e.Provisioner
	e.PreDestroy.log = e.Provisioner
	var jobs registry.JobRunner = e.Jobs
	if e.NoJobRunner {
		jobs = nil
	}
	var queues registry.QueueRunner = e.Queues
	if e.NoQueueRunner {
		queues = nil
	}
	var migrations registry.MigrationRunner = e.Migrations
	if e.PrivilegelessMigrations {
		migrations = privilegelessMigrations{e.Migrations}
	}
	server := func(name string, languages ...string) registry.PlatformSpec {
		return registry.PlatformSpec{
			Name:      name,
			Extension: Name,
			Kind:      ir.DeployableServer,
			Languages: languages,
			Settings:  json.RawMessage(serverSettings),
			NameOf:    serverName,
			AddressOf: func(ctx registry.PlatformContext) any {
				return ir.Output{Resource: ctx.Deployable.Name + ".service", Name: "url"}
			},
			PublicAddressOf: func(ctx registry.PlatformContext) any {
				return publicAddress(ctx, ctx.Deployable.Name+".service")
			},
			Lower: lowerServer,
		}
	}
	database := func(name string, dialects ...string) registry.PlatformSpec {
		return registry.PlatformSpec{
			Name:      name,
			Extension: Name,
			Kind:      ir.DeployableDatabase,
			Dialects:  dialects,
			Settings:  json.RawMessage(databaseSettings),
			NameOf: func(ctx registry.PlatformContext) any {
				return kebab(ctx.Deployable.Name)
			},
			AddressOf: func(ctx registry.PlatformContext) any {
				return ir.Output{Resource: ctx.Deployable.Name + ".instance", Name: "connectionName"}
			},
			Lower: lowerDatabase,
		}
	}
	for _, spec := range []registry.PlatformSpec{
		server(RunPlatform, registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust),
		server(EdgePlatform, registry.APILanguageTypeScript),
		{
			Name:      BundlePlatform,
			Extension: Name,
			Kind:      ir.DeployableServer,
			Languages: []string{registry.APILanguageTypeScript},
			Artifact:  ir.ArtifactBundle,
			NameOf:    serverName,
			AddressOf: func(ctx registry.PlatformContext) any {
				return ir.Output{Resource: ctx.Deployable.Name + ".script", Name: "url"}
			},
			Lower: lowerBundle,
		},
		{
			Name:      CronPlatform,
			Extension: Name,
			Kind:      ir.DeployableJob,
			Languages: []string{registry.APILanguageTypeScript},
			Artifact:  ir.ArtifactBundle,
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerBundle,
		},
		database(SQLPlatform, registry.SQLDialectPostgres),
		database(LitePlatform, registry.SQLDialectSQLite),
		{
			Name:      JobPlatform,
			Extension: Name,
			Kind:      ir.DeployableJob,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript},
			Settings:  json.RawMessage(jobSettings),
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerJob,
		},
		{
			Name:      SitePlatform,
			Extension: Name,
			Kind:      ir.DeployableSite,
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			PublicAddressOf: func(ctx registry.PlatformContext) any {
				return publicAddress(ctx, ctx.Deployable.Name+".site")
			},
			Lower: lowerSite,
		},
		{
			Name:      BucketPlatform,
			Extension: Name,
			Kind:      ir.DeployableBucket,
			Settings:  json.RawMessage(bucketSettings),
			NameOf:    bucketName,
			AddressOf: func(ctx registry.PlatformContext) any {
				return ir.Output{Resource: ctx.Deployable.Name + ".bucket", Name: "name"}
			},
			Lower: lowerBucket,
		},
		{
			Name:      WorkerPlatform,
			Extension: Name,
			Kind:      ir.DeployableWorker,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript},
			Settings:  json.RawMessage(workerSettings),
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerWorker,
		},
		{
			Name:      RunnerPlatform,
			Extension: Name,
			Kind:      ir.DeployableRunner,
			// A Rust API's engine runs its runner in Rust (D58), as its app
			// does (D78).
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerRunner,
		},
		{
			Name:      DeriverPlatform,
			Extension: Name,
			Kind:      ir.DeployableDeriver,
			Languages: []string{registry.APILanguageTypeScript, registry.APILanguageRust},
			NameOf:    serverName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerDeriver,
		},
		{
			Name:      AppPlatform,
			Extension: Name,
			Kind:      ir.DeployableApp,
			Languages: []string{registry.APILanguageRust},
			Settings:  json.RawMessage(serverSettings),
			NameOf:    serverName,
			// An app turned off runs no service, so it has no address.
			AddressOf: func(ctx registry.PlatformContext) any {
				if app := ctx.Deployable.App; app == nil || !app.Enabled {
					return nil
				}
				return ir.Output{Resource: ctx.Deployable.Name + ".service", Name: "url"}
			},
			PublicAddressOf: func(ctx registry.PlatformContext) any {
				if app := ctx.Deployable.App; app == nil || !app.Enabled {
					return nil
				}
				return publicAddress(ctx, ctx.Deployable.Name+".service")
			},
			Lower: lowerApp,
		},
	} {
		spec.Provider = Provider
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	for _, spec := range []registry.ConnectorSpec{
		{Name: SQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: RunPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: HTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: RunPlatform, To: RunPlatform, Connect: connectHTTP},
		{Name: JobSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: JobPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: JobHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: JobPlatform, To: RunPlatform, Connect: connectHTTP},
		{Name: SiteConnector, Extension: Name, Edge: ir.EdgeSite, From: SitePlatform, To: RunPlatform, Connect: connectSite},
		{Name: BucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: RunPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: JobBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: JobPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: WorkerSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: WorkerPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: WorkerHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: WorkerPlatform, To: RunPlatform, Connect: connectHTTP},
		{Name: WorkerBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: WorkerPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: BundleD1Connector, Extension: Name, Edge: ir.EdgeSQL, From: BundlePlatform, To: LitePlatform, Connect: connectD1},
		{Name: JobLiteConnector, Extension: Name, Edge: ir.EdgeSQL, From: JobPlatform, To: LitePlatform, Connect: connectLite},
		{Name: WorkerLiteConnector, Extension: Name, Edge: ir.EdgeSQL, From: WorkerPlatform, To: LitePlatform, Connect: connectLite},
		{Name: CronD1Connector, Extension: Name, Edge: ir.EdgeSQL, From: CronPlatform, To: LitePlatform, Connect: connectD1},
		{Name: BundleHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: BundlePlatform, To: BundlePlatform, Connect: connectBundleHTTP},
		{Name: RunnerSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: RunnerPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: DeriverSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: DeriverPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: DeriverHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: DeriverPlatform, To: RunPlatform, Connect: connectHTTP},
		{Name: DeriverBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: DeriverPlatform, To: BucketPlatform, Connect: connectBucket},
		{Name: AppSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: AppPlatform, To: SQLPlatform, Connect: connectSQL},
		{Name: AppHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: AppPlatform, To: RunPlatform, Connect: connectHTTP},
		{Name: AppBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: AppPlatform, To: BucketPlatform, Connect: connectBucket},
	} {
		if err := r.RegisterConnector(spec); err != nil {
			return err
		}
	}
	if err := r.RegisterDNSPlatform(registry.DNSPlatformSpec{
		Name: DNSPlatform, Extension: Name, Values: json.RawMessage(dnsValues), Lower: lowerRecords,
	}); err != nil {
		return err
	}
	if err := r.RegisterProvisioner(registry.ProvisionerSpec{
		Name: Provisioner, Extension: Name, Provisioner: e.Provisioner, Tools: e.Tools,
	}); err != nil {
		return err
	}
	types := map[string]json.RawMessage{}
	for typ, schema := range resourceTypes {
		types[typ] = json.RawMessage(schema)
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      Target,
		Extension: Name,
		Platforms: map[ir.DeployableKind]string{
			ir.DeployableServer:   RunPlatform,
			ir.DeployableDatabase: SQLPlatform,
			ir.DeployableJob:      JobPlatform,
			ir.DeployableSite:     SitePlatform,
			ir.DeployableBucket:   BucketPlatform,
			ir.DeployableWorker:   WorkerPlatform,
			ir.DeployableRunner:   RunnerPlatform,
			ir.DeployableDeriver:  DeriverPlatform,
			ir.DeployableApp:      AppPlatform,
		},
		Values:        json.RawMessage(targetValues),
		DNS:           DNSPlatform,
		Provisioner:   Provisioner,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyPublic, Check: checkPublic},
			{Name: PolicyHighAvailability, Check: checkHighAvailability},
		},
		State:      e.State,
		Secrets:    e.Secrets,
		Bootstrap:  e.Bootstrap,
		Migrations: migrations,
		Builder:    e.Builder,
		CI:         e.CI,
		Jobs:       jobs,
		Queues:     queues,
		Sites:      e.Sites,
		Bundles:    e.Bundles,
		PreDestroy: e.PreDestroy,
	})
}

const targetValues = `{
  "type": "object",
  "required": ["project", "region"],
  "properties": {
    "project": {"type": "string", "minLength": 1},
    "projectNumber": {"type": "string", "pattern": "^[0-9]+$"},
    "region": {"type": "string", "minLength": 1},
    "production": {"type": "boolean"}
  },
  "additionalProperties": false
}`

const serverSettings = `{
  "type": "object",
  "properties": {
    "minInstances": {"type": "integer", "minimum": 0},
    "public": {"type": "boolean"}
  },
  "additionalProperties": false
}`

const jobSettings = `{
  "type": "object",
  "properties": {
    "cpu": {"type": "string", "minLength": 1}
  },
  "additionalProperties": false
}`

const workerSettings = `{
  "type": "object",
  "properties": {
    "memory": {"type": "string", "minLength": 1}
  },
  "additionalProperties": false
}`

const databaseSettings = `{
  "type": "object",
  "properties": {
    "tier": {"enum": ["small", "large"]},
    "highAvailability": {"type": "boolean"}
  },
  "additionalProperties": false
}`

const bucketSettings = `{
  "type": "object",
  "properties": {
    "versioning": {"type": "boolean"}
  },
  "additionalProperties": false
}`

const dnsValues = `{
  "type": "object",
  "properties": {"zone": {"type": "string", "minLength": 1}},
  "additionalProperties": false
}`

// resourceTypes are the schemas of the fake provider's resource types,
// as a target checks in the schemas of the provider types it uses.
var resourceTypes = map[string]string{
	TypeAccount: `{"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}, "additionalProperties": false}`,
	TypeGrant: `{"type": "object", "required": ["role", "member", "resource"],
	  "properties": {"role": {"type": "string"}, "member": {"type": "string"}, "resource": {"type": "string"}},
	  "additionalProperties": false}`,
	TypeService: `{"type": "object", "required": ["name", "image", "account", "language"],
	  "properties": {
	    "name": {"type": "string"}, "image": {"type": "string"}, "account": {"type": "string"},
	    "language": {"type": "string"}, "minInstances": {"type": "integer"}, "public": {"type": "boolean"},
	    "env": {"type": "array", "items": {"type": "object", "required": ["name"],
	      "properties": {"name": {"type": "string"}, "value": {}, "secret": {"type": "string"}},
	      "additionalProperties": false}}
	  },
	  "additionalProperties": false}`,
	TypeRoute:  `{"type": "object", "required": ["host", "service"], "properties": {"host": {"type": "string"}, "service": {"type": "string"}}, "additionalProperties": false}`,
	TypeSecret: `{"type": "object", "required": ["name"], "properties": {"name": {"type": "string"}}, "additionalProperties": false}`,
	TypeSecretVersion: `{"type": "object", "required": ["secret", "data"],
	  "properties": {"secret": {"type": "string"}, "data": {"type": "string"}},
	  "additionalProperties": false}`,
	TypeInstance: `{"type": "object", "required": ["name", "dialect"],
	  "properties": {"name": {"type": "string"}, "dialect": {"type": "string"}, "tier": {"type": "string"}, "highAvailability": {"type": "boolean"}},
	  "additionalProperties": false}`,
	TypeDatabase: `{"type": "object", "required": ["name", "instance"], "properties": {"name": {"type": "string"}, "instance": {"type": "string"}}, "additionalProperties": false}`,
	TypeRecord: `{"type": "object", "required": ["zone", "name", "type", "value"],
	  "properties": {"zone": {"type": "string"}, "name": {"type": "string"}, "type": {"type": "string"}, "value": {"type": "string"}},
	  "additionalProperties": false}`,
	TypeJob: `{"type": "object", "required": ["name", "image", "account", "language", "timeoutSeconds", "retries"],
	  "properties": {
	    "name": {"type": "string"}, "image": {"type": "string"}, "account": {"type": "string"},
	    "language": {"type": "string"}, "timeoutSeconds": {"type": "integer", "minimum": 1},
	    "retries": {"type": "integer", "minimum": 0}, "cpu": {"type": "string"},
	    "env": {"type": "array", "items": {"type": "object", "required": ["name"],
	      "properties": {"name": {"type": "string"}, "value": {}, "secret": {"type": "string"}},
	      "additionalProperties": false}}
	  },
	  "additionalProperties": false}`,
	TypeSite: `{"type": "object", "required": ["name", "prefix", "public"],
	  "properties": {
	    "name": {"type": "string"}, "prefix": {"type": "string"}, "fallback": {"type": "string"}, "public": {"type": "boolean"}
	  },
	  "additionalProperties": false}`,
	TypeSchedule: `{"type": "object", "required": ["name", "schedule", "timeZone", "job", "account"],
	  "properties": {
	    "name": {"type": "string"}, "schedule": {"type": "string"}, "timeZone": {"type": "string"},
	    "job": {"type": "string"}, "account": {"type": "string"}
	  },
	  "additionalProperties": false}`,
	TypeBucket: `{"type": "object", "required": ["name", "location", "public"],
	  "properties": {
	    "name": {"type": "string"}, "location": {"type": "string"}, "public": {"type": "boolean"},
	    "versioning": {"type": "boolean"}, "forceDestroy": {"type": "boolean"}
	  },
	  "additionalProperties": false}`,
	TypeScript: `{"type": "object", "required": ["name", "account", "mainModule", "modules"],
	  "properties": {
	    "name": {"type": "string"}, "account": {"type": "string"}, "mainModule": {"type": "string"},
	    "modules": {"type": "array", "items": {"type": "object", "required": ["name", "contentFile", "contentSha256"],
	      "properties": {"name": {"type": "string"}, "contentFile": {"type": "string"}, "contentSha256": {"type": "string"}},
	      "additionalProperties": false}},
	    "env": {"type": "array", "items": {"type": "object", "required": ["name"],
	      "properties": {"name": {"type": "string"}, "value": {}, "secret": {"type": "string"}},
	      "additionalProperties": false}}
	  },
	  "additionalProperties": false}`,
	TypePool: `{"type": "object", "required": ["name", "image", "account", "language", "instances", "concurrency"],
	  "properties": {
	    "name": {"type": "string"}, "image": {"type": "string"}, "account": {"type": "string"},
	    "language": {"type": "string"}, "instances": {"type": "integer", "minimum": 0},
	    "concurrency": {"type": "integer", "minimum": 1}, "memory": {"type": "string"},
	    "env": {"type": "array", "items": {"type": "object", "required": ["name"],
	      "properties": {"name": {"type": "string"}, "value": {}, "secret": {"type": "string"}},
	      "additionalProperties": false}}
	  },
	  "additionalProperties": false}`,
}

// serverName names a server after its deployable, suffixed with each
// parameter's value under a parameter: `shop-api`, `shop-api-<pr>`.
func serverName(ctx registry.PlatformContext) any {
	parts := []any{kebab(ctx.Deployable.Name)}
	for _, param := range ctx.Environment.Parameters {
		parts = append(parts, "-", ir.Parameter(param))
	}
	return Join(parts...)
}

// lowerServer lowers a server to an account, a service whose environment
// carries every binding (a derived one as the variables
// ir.DerivedVariables encodes it in), a secret and an accessor grant per
// secret binding, and for an exposed server a route and a CNAME record under the
// environment's domain. Under a parameter the secrets are the parent
// environment's.
func lowerServer(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	service := map[string]any{
		"name":     d.ResourceName,
		"image":    kebab(d.Name),
		"account":  member,
		"language": strings.ToLower(d.Language),
		"public":   d.Exposed || d.Settings["public"] == true,
	}
	if n, ok := d.Settings["minInstances"]; ok {
		service["minInstances"] = n
	}
	if len(env) > 0 {
		service["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".service", Type: TypeService, Properties: service})
	if d.Exposed && ctx.Environment.Domain != "" {
		host := Join(d.ResourceName, ".", ctx.Environment.Domain)
		out.Resources = append(out.Resources, &ir.Resource{
			ID:         d.Name + ".route",
			Type:       TypeRoute,
			Properties: map[string]any{"host": host, "service": ir.Output{Resource: d.Name + ".service", Name: "id"}},
			Phase:      ir.PhaseExposure,
		})
		out.Records = append(out.Records, &ir.DNSRecord{
			Name:  host,
			Type:  "CNAME",
			Value: ir.Output{Resource: d.Name + ".route", Name: "target"},
		})
	}
	return out, nil
}

// lowerBundle lowers a server on the bundle platform, or a job on the cron
// platform (D64), to an account and the secrets it reads, as a server's,
// and a script that runs its bundle: each file BundleFiles names, read
// from the program's artifacts under the bundle's digest with its
// SHA-256, which the deploy pins, and the deployable's environment.
func lowerBundle(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	var modules []any
	for _, file := range BundleFiles {
		modules = append(modules, map[string]any{
			"name":          file,
			"contentFile":   ir.BundlePath(d.Name, file),
			"contentSha256": ir.BundleFileToken(file),
		})
	}
	script := map[string]any{"name": d.ResourceName, "account": member, "mainModule": BundleFiles[0], "modules": modules}
	if len(env) > 0 {
		script["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".script", Type: TypeScript, Properties: script})
	return out, nil
}

// lowerAccount lowers what a server and a job share: the deployable's
// account, and a secret and an accessor grant per secret binding. It
// returns them with the account's member and the deployable's environment,
// which carries every binding, a derived one as the variables
// ir.DerivedVariables encodes it in. Under a parameter the secrets are the
// parent environment's.
func lowerAccount(ctx registry.PlatformContext) (registry.Lowered, ir.Output, []any, error) {
	d := ctx.Deployable
	account := d.Name + ".account"
	member := ir.Output{Resource: account, Name: "email"}
	inherited := len(ctx.Environment.Parameters) > 0
	out := registry.Lowered{Resources: []*ir.Resource{{
		ID:         account,
		Type:       TypeAccount,
		Properties: map[string]any{"name": d.ResourceName},
		Phase:      ir.PhaseInfrastructure,
	}}}
	var env []any
	for _, b := range d.Bindings {
		entry := map[string]any{"name": b.Field}
		switch b.Source {
		case ir.BindingDerived:
			vars, err := ir.DerivedVariables(b.Field, b.Value)
			if err != nil {
				return registry.Lowered{}, member, nil, fmt.Errorf("binding %s: %w", b.Field, err)
			}
			for _, v := range vars {
				if !v.Secret {
					env = append(env, map[string]any{"name": v.Name, "value": v.Value})
					continue
				}
				// A secret member, such as the generic connector's
				// signing key (D63): a secret of the deployable's own
				// whose version holds the value, which the variable
				// reads, as a real platform's secret store does.
				secret, version := d.Name+".secret."+v.Name, d.Name+".secret-version."+v.Name
				out.Resources = append(out.Resources,
					&ir.Resource{ID: secret, Type: TypeSecret, Properties: map[string]any{"name": Join(d.ResourceName, "-", v.Name)}, Phase: ir.PhaseInfrastructure},
					&ir.Resource{ID: version, Type: TypeSecretVersion, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
						"secret": ir.Output{Resource: secret, Name: "id"}, "data": v.Value,
					}},
					&ir.Resource{ID: d.Name + ".reads." + v.Name, Type: TypeGrant, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
						"role": "secret.accessor", "member": member, "resource": ir.Output{Resource: secret, Name: "id"},
					}},
				)
				env = append(env, map[string]any{"name": v.Name, "secret": ir.Output{Resource: version, Name: "id"}})
			}
			continue
		case ir.BindingLiteral:
			entry["value"] = b.Value
		case ir.BindingParameter:
			entry["value"] = ir.Parameter(b.Parameter)
		case ir.BindingSecret:
			secret := "secret." + b.Secret
			entry["secret"] = ir.Output{Resource: secret, Name: "id"}
			out.Resources = append(out.Resources,
				&ir.Resource{ID: secret, Type: TypeSecret, Properties: map[string]any{"name": b.Secret}, Phase: ir.PhaseInfrastructure, Inherited: inherited},
				&ir.Resource{ID: d.Name + ".reads." + b.Secret, Type: TypeGrant, Phase: ir.PhaseInfrastructure, Properties: map[string]any{
					"role": "secret.accessor", "member": member, "resource": ir.Output{Resource: secret, Name: "id"},
				}},
			)
		default:
			return registry.Lowered{}, member, nil, fmt.Errorf("binding %s has source %q", b.Field, b.Source)
		}
		env = append(env, entry)
	}
	return out, member, env, nil
}

// lowerJob lowers a job (D52) to an account and the secrets it reads, as a
// server's, the job, which runs its image with its environment, one try
// and the retries and timeout its run takes, and for a schedule the
// environment runs, a scheduler entry that runs the job as its account.
func lowerJob(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Job == nil {
		return registry.Lowered{}, fmt.Errorf("job %s has no run", d.Name)
	}
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	job := map[string]any{
		"name":           d.ResourceName,
		"image":          kebab(d.Name),
		"account":        member,
		"language":       strings.ToLower(d.Language),
		"timeoutSeconds": d.Job.TimeoutSeconds,
		"retries":        d.Job.Retries,
	}
	if cpu, ok := d.Settings["cpu"]; ok {
		job["cpu"] = cpu
	}
	if len(env) > 0 {
		job["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".job", Type: TypeJob, Properties: job})
	if d.Job.Schedule != "" {
		out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".schedule", Type: TypeSchedule, Properties: map[string]any{
			"name":     d.ResourceName,
			"schedule": d.Job.Schedule,
			"timeZone": d.Job.TimeZone,
			"job":      ir.Output{Resource: d.Name + ".job", Name: "id"},
			"account":  member,
		}})
	}
	return out, nil
}

// publicAddress is where a browser reaches an exposed server or site: its
// host under the environment's domain, or the url output of its node id
// when the environment sets no domain (D55).
func publicAddress(ctx registry.PlatformContext, id string) any {
	if ctx.Environment.Domain == "" {
		return ir.Output{Resource: id, Name: "url"}
	}
	return Join("https://", ctx.Deployable.ResourceName, ".", ctx.Environment.Domain)
}

// lowerSite lowers a site (D55) to its storage, public, serving its files
// under the prefix of their digest, which the deploy pins, with the
// single-page fallback, and under the environment's domain a route and a
// CNAME record, as an exposed server's.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	id := d.Name + ".site"
	props := map[string]any{"name": d.ResourceName, "prefix": "/" + ir.SiteDigestToken + "/", "public": true}
	if d.Site.Fallback != "" {
		props["fallback"] = d.Site.Fallback
	}
	out := registry.Lowered{Resources: []*ir.Resource{{ID: id, Type: TypeSite, Properties: props}}}
	if ctx.Environment.Domain != "" {
		host := Join(d.ResourceName, ".", ctx.Environment.Domain)
		out.Resources = append(out.Resources, &ir.Resource{
			ID:         d.Name + ".route",
			Type:       TypeRoute,
			Properties: map[string]any{"host": host, "service": ir.Output{Resource: id, Name: "id"}},
			Phase:      ir.PhaseExposure,
		})
		out.Records = append(out.Records, &ir.DNSRecord{
			Name:  host,
			Type:  "CNAME",
			Value: ir.Output{Resource: d.Name + ".route", Name: "target"},
		})
	}
	return out, nil
}

// connectSite derives the public address of the server whose API a site
// calls (D55). The browser carries its end user's token, so the edge
// grants nothing.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.SiteEndpoint{URL: ctx.To.PublicAddress}}, nil
}

// lowerWorker lowers a worker (D53) to an account and the secrets it
// reads, as a server's, and a pool that runs its image with its
// environment, as many instances as its run takes, none when the
// environment turns it off, each handling its concurrency of messages.
func lowerWorker(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Worker == nil {
		return registry.Lowered{}, fmt.Errorf("worker %s has no run", d.Name)
	}
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	pool := map[string]any{
		"name":        d.ResourceName,
		"image":       kebab(d.Name),
		"account":     member,
		"language":    strings.ToLower(d.Language),
		"instances":   d.Worker.Instances,
		"concurrency": d.Worker.Concurrency,
	}
	if memory, ok := d.Settings["memory"]; ok {
		pool["memory"] = memory
	}
	if len(env) > 0 {
		pool["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".pool", Type: TypePool, Properties: pool})
	return out, nil
}

// lowerRunner lowers an engine's runner to an account and a pool of its
// instances, one, which handles one thing at a time (D58).
func lowerRunner(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Runner == nil {
		return registry.Lowered{}, fmt.Errorf("runner %s has no engine database", d.Name)
	}
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	pool := map[string]any{
		"name":        d.ResourceName,
		"image":       kebab(d.Name),
		"account":     member,
		"language":    strings.ToLower(d.Language),
		"instances":   d.Runner.Instances,
		"concurrency": 1,
	}
	if len(env) > 0 {
		pool["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".pool", Type: TypePool, Properties: pool})
	return out, nil
}

// lowerDeriver lowers a deriver (D74) to an account and the secrets it
// reads, as a server's, and a pool that runs its image with its
// environment, one instance or none when the environment turns it off,
// running its concurrency of answers.
func lowerDeriver(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Deriver == nil {
		return registry.Lowered{}, fmt.Errorf("deriver %s has no run", d.Name)
	}
	out, member, env, err := lowerAccount(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	pool := map[string]any{
		"name":        d.ResourceName,
		"image":       kebab(d.Name),
		"account":     member,
		"language":    strings.ToLower(d.Language),
		"instances":   d.Deriver.Instances,
		"concurrency": d.Deriver.Concurrency,
	}
	if len(env) > 0 {
		pool["env"] = env
	}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".pool", Type: TypePool, Properties: pool})
	return out, nil
}

// lowerApp lowers an app (D78) as a server: an account, the secrets it
// reads, and a service that runs its image, with a route when it is
// exposed. An app the environment turns off lowers to its account and
// secrets alone, and runs nothing.
func lowerApp(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.App == nil {
		return registry.Lowered{}, fmt.Errorf("app %s has no run", d.Name)
	}
	if !d.App.Enabled {
		out, _, _, err := lowerAccount(ctx)
		return out, err
	}
	return lowerServer(ctx)
}

// lowerDatabase lowers a database to an instance and a database per
// hosted schema. Under a parameter the instance is the parent
// environment's, and each schema's database is suffixed with the
// parameter's value (`shop_db_<pr>`).
func lowerDatabase(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	instance := d.Name + ".instance"
	tier, _ := d.Settings["tier"].(string)
	if tier == "" {
		tier = "small"
	}
	ha, _ := d.Settings["highAvailability"].(bool)
	out := registry.Lowered{Resources: []*ir.Resource{{
		ID:   instance,
		Type: TypeInstance,
		Properties: map[string]any{
			"name": d.ResourceName, "dialect": d.Dialect, "tier": tier, "highAvailability": ha,
		},
		Inherited: len(ctx.Environment.Parameters) > 0,
	}}}
	for _, svc := range d.Services {
		parts := []any{snake(svc.Name)}
		for _, param := range ctx.Environment.Parameters {
			parts = append(parts, "_", ir.Parameter(param))
		}
		out.Resources = append(out.Resources, &ir.Resource{
			ID:         d.Name + ".database." + svc.Name,
			Type:       TypeDatabase,
			Properties: map[string]any{"name": Join(parts...), "instance": ir.Output{Resource: instance, Name: "name"}},
		})
	}
	return out, nil
}

// connectD1 derives the binding a server on the bundle platform, or a job
// on the cron platform, reaches its SQLite database through, as a Worker reaches D1 (D64): the binding
// named after the field, of the schema's database's id. The binding is the
// grant, so the edge has no resource.
func connectD1(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{D1: &ir.D1Connection{
		Binding:  ctx.Edge.Field,
		Database: ir.Output{Resource: ctx.To.Name + ".database." + ctx.Edge.Service.Name, Name: "id"},
	}}}, nil
}

// connectLite derives a sqlite: URL of the schema's database, as the local
// target derives one for a SQLite file (D64).
func connectLite(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{URL: "sqlite:/data/" + snake(ctx.Edge.Service.Name) + ".sqlite"}}, nil
}

// connectSQL grants the server's account the client role on the instance
// and derives a Cloud SQL connection: the instance's address, the
// schema's database, and the server's account as its database user.
func connectSQL(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   ctx.From.Name + ".sql." + ctx.Edge.Service.Name,
			Type: TypeGrant,
			Properties: map[string]any{
				"role":     "sql.client",
				"member":   ir.Output{Resource: ctx.From.Name + ".account", Name: "email"},
				"resource": ir.Output{Resource: ctx.To.Name + ".instance", Name: "id"},
			},
		}},
		Value: ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
			Instance: ctx.To.Address,
			Database: ir.Output{Resource: ctx.To.Name + ".database." + ctx.Edge.Service.Name, Name: "name"},
			User:     ir.Output{Resource: ctx.From.Name + ".account", Name: "email"},
		}},
	}, nil
}

// connectHTTP grants the caller's account the invoker role on the callee
// and derives the callee's address, with an ID token as the service
// credential. Its audience is the callee's name, which the callee's
// callers field can hold, as it could not hold an output of the callee's
// own service. The callee gets FakeIssuer, whose tokens name the caller by
// its account's email. A server that calls an API it serves itself
// reaches it over loopback and needs no grant; the server signs that call
// with its own loopback key (docs/stack-model.md, section 9.9).
// connectBundleHTTP derives the in-process endpoint of an API a server on
// BundlePlatform serves itself (BundleHTTPConnector).
func connectBundleHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	if ctx.From.Name != ctx.To.Name {
		return registry.Connected{}, fmt.Errorf("%s calls %s on another server, which the fake bundle platform does not bind", ctx.From.Name, ctx.Edge.Service.Name)
	}
	// The deployable.s name as its address, as Cloudflare.s is the
	// Worker's: the script's url output would make the script depend on
	// itself.
	return registry.Connected{Value: ir.ServiceEndpoint{URL: "https://" + ctx.To.Name}}, nil
}

func connectHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	if ctx.From.Name == ctx.To.Name {
		return registry.Connected{Value: ir.ServiceEndpoint{URL: "http://127.0.0.1:8080"}}, nil
	}
	serves := make([]string, len(ctx.From.Services))
	for i, ref := range ctx.From.Services {
		serves[i] = ref.Name
	}
	account := ir.Output{Resource: ctx.From.Name + ".account", Name: "email"}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   ctx.From.Name + ".invokes." + ctx.Edge.Service.Name,
			Type: TypeGrant,
			Properties: map[string]any{
				"role":     "run.invoker",
				"member":   account,
				"resource": ir.Output{Resource: ctx.To.Name + ".service", Name: "id"},
			},
		}},
		Value: ir.ServiceEndpoint{
			URL:        ctx.To.Address,
			Credential: &ir.ServiceCredential{Source: ir.CredentialGoogleIDToken, Audience: ctx.To.ResourceName},
		},
		Callee: ir.ServiceAuthIssuer{
			Issuer:       FakeIssuer,
			Audience:     ctx.To.ResourceName,
			Algorithms:   []string{ir.AlgorithmRS256},
			JWKSURL:      FakeIssuer + "/keys",
			SubjectClaim: "email",
			Callers:      []ir.ServiceAuthCaller{{Subject: account, Deployable: ctx.From.Name, Serves: serves}},
		},
	}, nil
}

// bucketName names a bucket after the project, the stack and the bucket,
// since a bucket's name is unique across every project, suffixed with
// each parameter's value under a parameter: a member of a parameterized
// environment gets a bucket of its own (D54).
func bucketName(ctx registry.PlatformContext) any {
	project, _ := ctx.Environment.Values["project"].(string)
	parts := []any{project, "-", kebab(ctx.Environment.Stack), "-", kebab(ctx.Deployable.Name)}
	for _, param := range ctx.Environment.Parameters {
		parts = append(parts, "-", ir.Parameter(param))
	}
	return Join(parts...)
}

// lowerBucket lowers a bucket to a private bucket in the environment's
// region, versioned when its settings say so. A member of a parameterized
// environment's bucket is its own, and its destroy empties it.
func lowerBucket(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	region, _ := ctx.Environment.Values["region"].(string)
	props := map[string]any{
		"name":     d.ResourceName,
		"location": region,
		"public":   false,
	}
	if versioning, ok := d.Settings["versioning"].(bool); ok {
		props["versioning"] = versioning
	}
	if len(ctx.Environment.Parameters) > 0 {
		props["forceDestroy"] = true
	}
	return registry.Lowered{Resources: []*ir.Resource{{ID: d.Name + ".bucket", Type: TypeBucket, Properties: props}}}, nil
}

// connectBucket grants the server's or the job's account the objects of
// the bucket and derives the bucket's name, which the account reaches with
// no credential of the edge's own (D54).
func connectBucket(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   ctx.From.Name + ".storage." + ctx.Edge.Service.Name,
			Type: TypeGrant,
			Properties: map[string]any{
				"role":     "storage.objectUser",
				"member":   ir.Output{Resource: ctx.From.Name + ".account", Name: "email"},
				"resource": ir.Output{Resource: ctx.To.Name + ".bucket", Name: "id"},
			},
		}},
		Value: ir.BucketConnection{Name: ctx.To.Address},
	}, nil
}

// lowerRecords writes each record into the zone the values name, or the
// environment's domain.
func lowerRecords(ctx registry.DNSContext) ([]*ir.Resource, error) {
	zone, _ := ctx.Values["zone"].(string)
	if zone == "" {
		zone = ctx.Environment.Domain
	}
	count := map[string]int{}
	var out []*ir.Resource
	for _, rec := range ctx.Records {
		count[rec.Deployable]++
		out = append(out, &ir.Resource{
			ID:   fmt.Sprintf("dns.%s.%d", rec.Deployable, count[rec.Deployable]),
			Type: TypeRecord,
			Properties: map[string]any{
				"zone": zone, "name": rec.Name, "type": rec.Type, "value": rec.Value,
			},
		})
	}
	return out, nil
}

// checkPublic refuses a public resource whose owner is a deployable that
// is not exposed.
func checkPublic(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, res := range env.Resources.Resources {
		if res.Properties["public"] != true {
			continue
		}
		for _, owner := range res.Owners {
			if d := env.Deployable(owner); d != nil && !d.Exposed {
				out = append(out, fmt.Sprintf("resource %s is public, but %s is not exposed", res.ID, owner))
			}
		}
	}
	return out
}

// checkHighAvailability refuses, in an environment whose values set
// production, an instance that is not highly available.
func checkHighAvailability(env *ir.ResolvedEnvironment) []string {
	if env.Values["production"] != true {
		return nil
	}
	var out []string
	for _, res := range env.Resources.Resources {
		if res.Type == TypeInstance && res.Properties["highAvailability"] != true {
			out = append(out, fmt.Sprintf("instance %s of %s is not highly available", res.ID, strings.Join(res.Owners, ", ")))
		}
	}
	return out
}

// Join joins strings and references into one value: a string when every
// part is a string, else an ir.Concat with adjacent strings merged.
func Join(parts ...any) any {
	var out ir.Concat
	var buf strings.Builder
	flush := func() {
		if buf.Len() > 0 {
			out = append(out, buf.String())
			buf.Reset()
		}
	}
	var add func(any)
	add = func(part any) {
		switch part := part.(type) {
		case string:
			buf.WriteString(part)
		case ir.Concat:
			for _, inner := range part {
				add(inner)
			}
		default:
			flush()
			out = append(out, part)
		}
	}
	for _, part := range parts {
		add(part)
	}
	flush()
	switch {
	case len(out) == 0:
		return ""
	case len(out) == 1:
		if s, ok := out[0].(string); ok {
			return s
		}
	}
	return out
}

// kebab lowercases a name and joins its words with hyphens: Orders is
// orders, shop-api stays shop-api.
func kebab(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && name[i-1] != '-' && name[i-1] != '_' {
				b.WriteByte('-')
			}
			b.WriteRune(r - 'A' + 'a')
		case r == '_':
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// snake is kebab with underscores.
func snake(name string) string { return strings.ReplaceAll(kebab(name), "-", "_") }
