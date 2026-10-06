// Package local is the core's `local` target (docs/stack-model.md,
// sections 6.3 and 8.3): it runs an environment of a stack on the machine
// at hand. A server is a process built from its generated entrypoint
// module, every database deployable of the environment shares one Postgres
// container, with a database per hosted DB schema, a sql edge derives a
// connection string to that container, and an http edge the callee's
// loopback URL with a service credential the caller signs with the edge's
// Ed25519 key (D37). Giving the callee the public key waits for the
// service-auth field connectors and the entrypoint will share.
//
// The target registers like any other (Register), but the core registers
// it, so a binary with no extension linked runs `stack dev`. Its resource
// vocabulary is the core's own, the `local` provider's four types
// (TypeContainer, TypeDatabase, TypeKeyPair, TypeProcess), not a Pulumi
// package's: no published provider schema describes a local process, and
// nothing but the local provisioner applies them (D30, amended: the local
// target).
//
// The platforms and connectors are pure. Provisioner applies their graph:
// it runs Docker, the migration runner, `go build` and the servers.
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

	// ServerPlatform runs a server as a process; DatabasePlatform runs a
	// database as databases on the environment's Postgres container.
	ServerPlatform   = "local.process"
	DatabasePlatform = "local.postgres"

	// SQLConnector connects a process to a database on the container;
	// HTTPConnector connects a process to one it calls.
	SQLConnector  = "local.process-postgres"
	HTTPConnector = "local.process-process"

	// ProvisionerName is the provisioner the target names.
	ProvisionerName = "local"

	// PolicyNoDomain refuses an environment that sets a domain: a local
	// server is reached on loopback, and no DNS platform writes records
	// for it.
	PolicyNoDomain = "local-no-domain"

	// PolicyNoParameters refuses a parameterized environment: the local
	// target runs one copy of each environment.
	PolicyNoParameters = "local-no-parameters"

	// PolicyDistinctPorts refuses two processes, or a process and the
	// Postgres container, that listen on one port.
	PolicyDistinctPorts = "local-distinct-ports"
)

// The resource types the target's platforms emit: the `local` provider's.
const (
	// TypeContainer is a Docker container: the environment's Postgres.
	TypeContainer = "local:docker/container:Container"

	// TypeDatabase is a database on a Postgres container.
	TypeDatabase = "local:postgres/database:Database"

	// TypeKeyPair is an http edge's Ed25519 key pair. The provisioner
	// generates it into the environment's state directory, never the
	// output root, and the caller signs its service credential with the
	// private key. Its public key is an output, for the callee's
	// service-auth field once there is one.
	TypeKeyPair = "local:serviceauth/keyPair:KeyPair"

	// TypeProcess is a server process built from its entrypoint module.
	TypeProcess = "local:process/process:Process"
)

// Register adds the local target, its two platforms and two connectors, the
// schema of each resource type they emit, and its provisioner. The
// provisioner is a new Provisioner with its defaults.
func Register(r *registry.Registry) error {
	for _, spec := range []registry.PlatformSpec{
		{
			Name:      ServerPlatform,
			Kind:      ir.DeployableServer,
			Languages: []string{registry.APILanguageGo},
			Settings:  json.RawMessage(serverSettings),
			NameOf:    processName,
			AddressOf: processAddress,
			Lower:     lowerProcess,
		},
		{
			Name:      DatabasePlatform,
			Kind:      ir.DeployableDatabase,
			Dialects:  []string{registry.SQLDialectPostgres},
			NameOf:    databaseDeployableName,
			AddressOf: databaseAddress,
			Lower:     lowerDatabase,
		},
	} {
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	for _, spec := range []registry.ConnectorSpec{
		{Name: SQLConnector, Edge: ir.EdgeSQL, From: ServerPlatform, To: DatabasePlatform, Connect: connectSQL},
		{Name: HTTPConnector, Edge: ir.EdgeHTTP, From: ServerPlatform, To: ServerPlatform, Connect: connectHTTP},
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
		},
		Values:        json.RawMessage(targetValues),
		Provisioner:   ProvisionerName,
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyNoDomain, Check: checkNoDomain},
			{Name: PolicyNoParameters, Check: checkNoParameters},
			{Name: PolicyDistinctPorts, Check: checkDistinctPorts},
		},
	})
}

// targetValues is the schema of a local environment's values: the
// Postgres image and the host port its container publishes.
const targetValues = `{
  "type": "object",
  "properties": {
    "postgresImage": {"type": "string", "minLength": 1},
    "postgresPort": {"type": "integer", "minimum": 1, "maximum": 65535}
  },
  "additionalProperties": false
}`

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
	    "labels": {"type": "object", "additionalProperties": {"type": "string"}}
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
	  "required": ["name", "module", "language", "port", "readiness"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "module": {"type": "string", "minLength": 1},
	    "language": {"enum": ["go"]},
	    "port": {"type": "integer", "minimum": 1, "maximum": 65535},
	    "readiness": {"type": "string", "pattern": "^/"},
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
