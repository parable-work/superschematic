// Package gcp is the gcp target of the stack model (docs/stack-model.md,
// section 7): Cloud Run servers, Cloud SQL Postgres databases, Secret
// Manager secrets, a global external Application Load Balancer for exposed
// servers, and Cloud DNS for their records. It registers through the
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

	// CloudRun runs servers as Cloud Run services; CloudSQL runs Postgres
	// databases on Cloud SQL instances.
	CloudRun = "gcp.cloudrun"
	CloudSQL = "gcp.cloudsql"

	// CloudDNS writes an environment's records into a Cloud DNS managed
	// zone. It is the target's default DNS platform.
	CloudDNS = "gcp.clouddns"

	// SQLConnector connects a Cloud Run server to a Cloud SQL database;
	// HTTPConnector connects a Cloud Run server to one it calls.
	SQLConnector  = "gcp.cloudrun-cloudsql"
	HTTPConnector = "gcp.cloudrun-cloudrun"

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

// Extension is the gcp extension.
type Extension struct{}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the gcp target, its platforms, connectors and DNS
// platform, and the pinned schema of every resource type they emit.
func (Extension) Register(r *registry.Registry) error {
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
		},
		Values:        json.RawMessage(targetValues),
		DNS:           CloudDNS,
		Provisioner:   Provisioner,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyHighAvailability, Check: checkHighAvailability},
			{Name: PolicyNothingPublic, Check: checkNothingPublic},
		},
	})
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
