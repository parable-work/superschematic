// Package gcp is the gcp target of the stack model (docs/stack-model.md,
// section 7): Cloud Run servers, Cloud Run jobs and their Cloud Scheduler
// schedules, Cloud Run worker pools (D53), Cloud SQL Postgres databases,
// Cloud Storage buckets (D54), Secret Manager secrets, a global external
// Application Load Balancer for exposed servers, static sites in Cloud
// Storage behind a load balancer with Cloud CDN (D55), memory stores in
// Memorystore for Valkey (D75), and Cloud DNS for their records. It
// registers through the public registry package alone,
// as any extension does (D10), in a Go module of its own (D1), so the GCP
// vocabulary stays out of the core.
//
// Every platform, connector and DNS platform here is a pure function to
// resource graph nodes typed by the Pulumi `gcp` provider, whose schemas
// the target pins and registers (package schemas), so resolution checks
// every node offline. The provisioner that applies them is `pulumi`,
// another extension: a distribution links both.
package gcp

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/gcp/schemas"
)

// The names the extension registers.
const (
	// Name is the extension's name.
	Name = "gcp"

	// Target is the target's name.
	Target = "gcp"

	// Provider is the provider each platform of the target runs on, Google
	// Cloud. A deployable of another provider reaches a Cloud Run server,
	// and a Cloud Run server one of another provider, through the core's
	// generic connector (D63).
	Provider = "gcp"

	// CloudRun runs servers as Cloud Run services; CloudRunJob runs jobs
	// as Cloud Run jobs, each schedule as a Cloud Scheduler job (D52);
	// CloudRunWorker runs workers as Cloud Run worker pools (D53);
	// CloudRunRunner runs an engine's runner as a Cloud Run worker pool of
	// one instance (D58); CloudRunDeriver runs a deriver as a Cloud Run
	// worker pool of one instance (D74); CloudRunApp runs an app as a Cloud
	// Run service, as a server is run (D78); CloudSQL runs Postgres
	// databases on Cloud SQL instances.
	CloudRun        = "gcp.cloudrun"
	CloudRunJob     = "gcp.cloudrunjob"
	CloudRunWorker  = "gcp.cloudrunworker"
	CloudRunRunner  = "gcp.cloudrunrunner"
	CloudRunDeriver = "gcp.cloudrunderiver"
	CloudRunApp     = "gcp.cloudrunapp"
	CloudSQL        = "gcp.cloudsql"

	// Site serves a static site's files from a Cloud Storage bucket,
	// through a load balancer with Cloud CDN (D55).
	Site = "gcp.site"

	// Storage keeps a Bucket service's objects in a Cloud Storage bucket
	// (D54).
	Storage = "gcp.storage"

	// Memorystore keeps a MemoryStore service's channels and keys in a
	// Memorystore for Valkey instance (D75).
	Memorystore = "gcp.memorystore"

	// CloudDNS writes an environment's records into a Cloud DNS managed
	// zone. It is the target's default DNS platform.
	CloudDNS = "gcp.clouddns"

	// SQLConnector connects a Cloud Run server to a Cloud SQL database;
	// HTTPConnector connects a Cloud Run server to one it calls.
	SQLConnector  = "gcp.cloudrun-cloudsql"
	HTTPConnector = "gcp.cloudrun-cloudrun"

	// JobSQLConnector and JobHTTPConnector connect a Cloud Run job, whose
	// edges are its API's, as the server's connectors do (D52).
	JobSQLConnector  = "gcp.cloudrunjob-cloudsql"
	JobHTTPConnector = "gcp.cloudrunjob-cloudrun"

	// SiteConnector connects a site to a Cloud Run server it calls: the
	// site's config holds the server's public address (D55).
	SiteConnector = "gcp.site-cloudrun"

	// BucketConnector and JobBucketConnector connect a Cloud Run server
	// and a Cloud Run job to a bucket its API lists (D54).
	BucketConnector    = "gcp.cloudrun-storage"
	JobBucketConnector = "gcp.cloudrunjob-storage"

	// WorkerSQLConnector, WorkerHTTPConnector and WorkerBucketConnector
	// connect a Cloud Run worker pool, whose edges are its API's, as the
	// server's connectors do (D53).
	WorkerSQLConnector    = "gcp.cloudrunworker-cloudsql"
	WorkerHTTPConnector   = "gcp.cloudrunworker-cloudrun"
	WorkerBucketConnector = "gcp.cloudrunworker-storage"

	// RunnerSQLConnector connects an engine's runner's worker pool to its
	// engine database on Cloud SQL, its one edge (D58).
	RunnerSQLConnector = "gcp.cloudrunrunner-cloudsql"

	// DeriverSQLConnector, DeriverHTTPConnector and DeriverBucketConnector
	// connect a deriver's worker pool, whose edges are its API's, as a
	// worker's connectors do (D74).
	DeriverSQLConnector    = "gcp.cloudrunderiver-cloudsql"
	DeriverHTTPConnector   = "gcp.cloudrunderiver-cloudrun"
	DeriverBucketConnector = "gcp.cloudrunderiver-storage"

	// MemoryStoreConnector, JobMemoryStoreConnector,
	// WorkerMemoryStoreConnector and DeriverMemoryStoreConnector connect a
	// Cloud Run server, job, worker pool and deriver to a memory store its
	// API lists (D75).
	MemoryStoreConnector        = "gcp.cloudrun-memorystore"
	JobMemoryStoreConnector     = "gcp.cloudrunjob-memorystore"
	WorkerMemoryStoreConnector  = "gcp.cloudrunworker-memorystore"
	DeriverMemoryStoreConnector = "gcp.cloudrunderiver-memorystore"
	// AppSQLConnector, AppHTTPConnector and AppBucketConnector connect an
	// app's Cloud Run service, whose edges are its API's, as the server's
	// connectors do (D78).
	AppSQLConnector    = "gcp.cloudrunapp-cloudsql"
	AppHTTPConnector   = "gcp.cloudrunapp-cloudrun"
	AppBucketConnector = "gcp.cloudrunapp-storage"

	// Provisioner is the provisioner the target names. The pulumi
	// extension registers it.
	Provisioner = "pulumi"

	// PolicyHighAvailability refuses, in an environment whose values set
	// production, a Cloud SQL instance that is not highly available.
	PolicyHighAvailability = "production-databases-highly-available"

	// PolicyNothingPublic refuses a resource that admits the public on
	// behalf of anything but an exposed server, an exposed app (D78) or a
	// site.
	PolicyNothingPublic = "nothing-public-unless-exposed"

	// PolicyPrivateBuckets refuses a Bucket service's bucket that is not
	// private, or a grant that admits the public to one, on behalf of any
	// deployable (D54).
	PolicyPrivateBuckets = "buckets-never-public"

	// PolicyMemoryStoresTLSAndIAM refuses a Memorystore instance without
	// transit encryption or without IAM authentication (D75).
	PolicyMemoryStoresTLSAndIAM = "memory-stores-tls-and-iam"
)

// ProviderVersion is the pulumi-gcp release the resource types are pinned
// at (schemas/pulumi-gcp.json). The provisioner installs the provider at
// this version, so what resolution validated is what applies.
const ProviderVersion = "9.37.1"

// Extension is the gcp extension. Its zero value is the one a
// distribution links.
type Extension struct {
	// Cloud is what bootstrap, the secret store and the state store call
	// on Google Cloud. Nil uses NewCloud's, over the client libraries,
	// made on first use; tests pass a fake.
	Cloud Cloud

	// Migrations runs the migration plans of the target's databases
	// between a deploy's steps. Nil runs each phase as an execution of the
	// stack's Cloud Run job, which runs superschematic-migrate on Cloud SQL
	// (section 8.4); tests pass a fake.
	Migrations registry.MigrationRunner

	// MigrateImage, when set, is the image of superschematic-migrate, by
	// digest, that the migration job runs; else SUPERSCHEMATIC_MIGRATE_IMAGE
	// names one, else the job runs the image built from the release
	// MigrateVersion names.
	MigrateImage string

	// MigrateVersion is the release of superschematic-migrate whose image
	// the migration job runs. Empty is the release the binary is part of,
	// none for a binary built from a checkout.
	MigrateVersion string
}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the gcp target, its platforms, connectors and DNS
// platform, the pinned schema of every resource type they and bootstrap
// emit, and the target's deploy seams: the state bucket, Secret Manager,
// bootstrap, image builds with Cloud Build, the migration job, the
// generated CI's sign-in through Workload Identity Federation, a job's
// run on demand as an execution of its Cloud Run job (D52), and a site's
// files' upload to its bucket (D55).
func (e Extension) Register(r *registry.Registry) error {
	for _, spec := range []registry.PlatformSpec{
		{
			Name:      CloudRun,
			Extension: Name,
			Kind:      ir.DeployableServer,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(cloudRunSettings),
			NameOf:    serviceName,
			AddressOf: serviceAddress,
			// Where a browser reaches an exposed server: what a site's
			// config holds for it (D55).
			PublicAddressOf: servicePublicAddress,
			Lower:           lowerService,
		},
		{
			Name:      CloudRunJob,
			Extension: Name,
			Kind:      ir.DeployableJob,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(cloudRunJobSettings),
			NameOf:    serviceName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerJob,
		},
		{
			Name:      CloudRunWorker,
			Extension: Name,
			Kind:      ir.DeployableWorker,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(cloudRunWorkerSettings),
			NameOf:    serviceName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerWorker,
		},
		{
			Name:      CloudRunRunner,
			Extension: Name,
			Kind:      ir.DeployableRunner,
			Languages: []string{registry.APILanguageGo, registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(cloudRunWorkerSettings),
			NameOf:    serviceName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerRunner,
		},
		{
			Name:      CloudRunDeriver,
			Extension: Name,
			Kind:      ir.DeployableDeriver,
			// In its API's language, TypeScript or Rust (D74).
			Languages: []string{registry.APILanguageTypeScript, registry.APILanguageRust},
			Settings:  json.RawMessage(cloudRunWorkerSettings),
			NameOf:    serviceName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerDeriver,
		},
		{
			Name:      CloudRunApp,
			Extension: Name,
			Kind:      ir.DeployableApp,
			// An app runs on a Rust API (D78).
			Languages: []string{registry.APILanguageRust},
			// A Cloud Run service's, as a server's.
			Settings:        json.RawMessage(cloudRunSettings),
			NameOf:          serviceName,
			AddressOf:       appAddress,
			PublicAddressOf: appPublicAddress,
			Lower:           lowerApp,
		},
		{
			Name:      CloudSQL,
			Extension: Name,
			Kind:      ir.DeployableDatabase,
			Dialects:  []string{registry.SQLDialectPostgres},
			Settings:  json.RawMessage(cloudSQLSettings),
			NameOf:    instanceName,
			AddressOf: instanceAddress,
			Lower:     lowerDatabase,
		},
		{
			Name:            Site,
			Extension:       Name,
			Kind:            ir.DeployableSite,
			Settings:        json.RawMessage(siteSettings),
			NameOf:          serviceName,
			AddressOf:       sitePublicAddress,
			PublicAddressOf: sitePublicAddress,
			Lower:           lowerSite,
		},
		{
			Name:      Storage,
			Extension: Name,
			Kind:      ir.DeployableBucket,
			Settings:  json.RawMessage(storageSettings),
			NameOf:    bucketName,
			AddressOf: bucketAddress,
			Lower:     lowerBucket,
		},
		{
			Name:      Memorystore,
			Extension: Name,
			Kind:      ir.DeployableMemoryStore,
			Settings:  json.RawMessage(memoryStoreSettings),
			NameOf:    memoryStoreName,
			AddressOf: memoryStoreAddress,
			Lower:     lowerMemoryStore,
		},
	} {
		spec.Provider = Provider
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	connectors := []registry.ConnectorSpec{
		{Name: SQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRun, To: CloudSQL, Connect: connectSQL},
		{Name: HTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRun, To: CloudRun, Connect: connectHTTP},
		{Name: JobSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunJob, To: CloudSQL, Connect: connectSQL},
		{Name: JobHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunJob, To: CloudRun, Connect: connectHTTP},
		{Name: SiteConnector, Extension: Name, Edge: ir.EdgeSite, From: Site, To: CloudRun, Connect: connectSite},
		{Name: BucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: CloudRun, To: Storage, Connect: connectBucket},
		{Name: JobBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: CloudRunJob, To: Storage, Connect: connectBucket},
		{Name: WorkerSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunWorker, To: CloudSQL, Connect: connectSQL},
		{Name: WorkerHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunWorker, To: CloudRun, Connect: connectHTTP},
		{Name: WorkerBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: CloudRunWorker, To: Storage, Connect: connectBucket},
		{Name: RunnerSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunRunner, To: CloudSQL, Connect: connectSQL},
		{Name: DeriverSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunDeriver, To: CloudSQL, Connect: connectSQL},
		{Name: DeriverHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunDeriver, To: CloudRun, Connect: connectHTTP},
		{Name: DeriverBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: CloudRunDeriver, To: Storage, Connect: connectBucket},
		{Name: AppSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunApp, To: CloudSQL, Connect: connectSQL},
		{Name: AppHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunApp, To: CloudRun, Connect: connectHTTP},
		{Name: AppBucketConnector, Extension: Name, Edge: ir.EdgeBucket, From: CloudRunApp, To: Storage, Connect: connectBucket},
	}
	for _, spec := range append(connectors, memoryStoreConnectors()...) {
		if err := r.RegisterConnector(spec); err != nil {
			return err
		}
	}
	if err := r.RegisterDNSPlatform(registry.DNSPlatformSpec{
		Name: CloudDNS, Extension: Name, Values: json.RawMessage(cloudDNSValues), Lower: lowerRecords,
	}); err != nil {
		return err
	}
	types, err := resourceTypeSchemas()
	if err != nil {
		return err
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      Target,
		Extension: Name,
		Platforms: map[ir.DeployableKind]string{
			ir.DeployableServer:      CloudRun,
			ir.DeployableDatabase:    CloudSQL,
			ir.DeployableJob:         CloudRunJob,
			ir.DeployableSite:        Site,
			ir.DeployableBucket:      Storage,
			ir.DeployableWorker:      CloudRunWorker,
			ir.DeployableRunner:      CloudRunRunner,
			ir.DeployableDeriver:     CloudRunDeriver,
			ir.DeployableMemoryStore: Memorystore,
			ir.DeployableApp:         CloudRunApp,
		},
		Values:        json.RawMessage(targetValues),
		DNS:           CloudDNS,
		Provisioner:   Provisioner,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyHighAvailability, Check: checkHighAvailability},
			{Name: PolicyNothingPublic, Check: checkNothingPublic},
			{Name: PolicyPrivateBuckets, Check: checkPrivateBuckets},
			{Name: PolicyMemoryStoresTLSAndIAM, Check: checkMemoryStores},
		},
		State:      stateStore{ext: e},
		Secrets:    secretStore{ext: e},
		Bootstrap:  bootstrapper{ext: e},
		Migrations: e.migrations(),
		Builder:    imageBuilder{ext: e},
		CI:         ciIdentities{},
		Jobs:       jobRunner{ext: e},
		Queues:     migrationRunner{ext: e},
		Sites:      sitePublisher{ext: e},
	})
}

// migrations is the extension's migration runner: the one it was given, or
// the Cloud Run job.
func (e Extension) migrations() registry.MigrationRunner {
	if e.Migrations != nil {
		return e.Migrations
	}
	return migrationRunner{ext: e}
}

// resourceTypeSchemas returns the JSON Schema of every pinned type, keyed
// by token. It refuses a pin at another version than ProviderVersion.
func resourceTypeSchemas() (map[string]json.RawMessage, error) {
	types, err := schemas.ResourceTypes(ProviderVersion)
	if err != nil {
		return nil, fmt.Errorf("gcp: %w", err)
	}
	return types, nil
}
