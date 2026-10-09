// Package gcp is the gcp target of the stack model (docs/stack-model.md,
// section 7): Cloud Run servers, Cloud Run jobs and their Cloud Scheduler
// schedules, Cloud Run worker pools, Cloud SQL Postgres databases, Secret
// Manager secrets, a global
// external Application Load Balancer for exposed servers, and Cloud DNS for
// their records. It registers through the
// public registry package alone, as any extension does (D10), in a Go
// module of its own (D1), so the GCP vocabulary stays out of the core.
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

	// CloudRun runs servers as Cloud Run services; CloudRunJob runs jobs
	// as Cloud Run jobs, each schedule as a Cloud Scheduler job (D52);
	// CloudRunWorker runs workers as Cloud Run worker pools (D53);
	// CloudSQL runs Postgres databases on Cloud SQL instances.
	CloudRun       = "gcp.cloudrun"
	CloudRunJob    = "gcp.cloudrunjob"
	CloudRunWorker = "gcp.cloudrunworker"
	CloudSQL       = "gcp.cloudsql"

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

	// WorkerSQLConnector and WorkerHTTPConnector connect a Cloud Run
	// worker pool, whose edges are its API's, as the server's connectors
	// do (D53).
	WorkerSQLConnector  = "gcp.cloudrunworker-cloudsql"
	WorkerHTTPConnector = "gcp.cloudrunworker-cloudrun"

	// Provisioner is the provisioner the target names. The pulumi
	// extension registers it.
	Provisioner = "pulumi"

	// PolicyHighAvailability refuses, in an environment whose values set
	// production, a Cloud SQL instance that is not highly available.
	PolicyHighAvailability = "production-databases-highly-available"

	// PolicyNothingPublic refuses a resource that admits the public on
	// behalf of anything but an exposed server.
	PolicyNothingPublic = "nothing-public-unless-exposed"
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
// generated CI's sign-in through Workload Identity Federation, and a job's
// run on demand as an execution of its Cloud Run job (D52).
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
			Lower:     lowerService,
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
			Languages: []string{registry.APILanguageGo},
			Settings:  json.RawMessage(cloudRunWorkerSettings),
			NameOf:    serviceName,
			AddressOf: func(registry.PlatformContext) any { return nil },
			Lower:     lowerWorker,
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
	} {
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	for _, spec := range []registry.ConnectorSpec{
		{Name: SQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRun, To: CloudSQL, Connect: connectSQL},
		{Name: HTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRun, To: CloudRun, Connect: connectHTTP},
		{Name: JobSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunJob, To: CloudSQL, Connect: connectSQL},
		{Name: JobHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunJob, To: CloudRun, Connect: connectHTTP},
		{Name: WorkerSQLConnector, Extension: Name, Edge: ir.EdgeSQL, From: CloudRunWorker, To: CloudSQL, Connect: connectSQL},
		{Name: WorkerHTTPConnector, Extension: Name, Edge: ir.EdgeHTTP, From: CloudRunWorker, To: CloudRun, Connect: connectHTTP},
	} {
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
			ir.DeployableServer:   CloudRun,
			ir.DeployableDatabase: CloudSQL,
			ir.DeployableJob:      CloudRunJob,
			ir.DeployableWorker:   CloudRunWorker,
		},
		Values:        json.RawMessage(targetValues),
		DNS:           CloudDNS,
		Provisioner:   Provisioner,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyHighAvailability, Check: checkHighAvailability},
			{Name: PolicyNothingPublic, Check: checkNothingPublic},
		},
		State:      stateStore{ext: e},
		Secrets:    secretStore{ext: e},
		Bootstrap:  bootstrapper{ext: e},
		Migrations: e.migrations(),
		Builder:    imageBuilder{ext: e},
		CI:         ciIdentities{},
		Jobs:       jobRunner{ext: e},
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
