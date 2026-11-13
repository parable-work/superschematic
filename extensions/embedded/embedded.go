// Package embedded is the embedded target of the stack model (D116): an
// environment on it is one binary a person installs. Every server and
// worker of the environment runs in one process, each database is a SQLite
// file under one data directory, and a site's built files are compiled
// into the binary. It registers through the public registry package alone,
// as any extension does (D10), in a Go module of its own (D1).
//
// The target has no provisioner, no state and no deploy: what it makes is
// a file. Its process platforms declare ir.ArtifactBinary, by which the
// core's stack build writes the environment's crate at
// `<output-root>/embedded/<stack>/<environment>/`, and `stack build
// <environment>` builds it with cargo and prints the binary's path.
//
// Every platform and connector here is a pure function. The nodes they
// emit describe the binary's parts to a reader of the resolved
// environment: nothing applies them.
package embedded

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The names the extension registers.
const (
	// Name is the extension's name.
	Name = "embedded"

	// Target is the target's name.
	Target = "embedded"

	// Provider is the provider each platform of the target runs on: the
	// environment's one process.
	Provider = "embedded"

	// Process runs a Rust server inside the binary; Worker runs a Rust
	// worker inside it, through the worker's drain. The registry holds a
	// platform to one deployable kind, so a worker has a platform of its
	// own beside the server's.
	Process = "embedded.process"
	Worker  = "embedded.worker"

	// SQLite holds a database whose DB services list sqlite, a file per
	// hosted DB schema under the binary's data directory.
	SQLite = "embedded.sqlite"

	// Assets serves a site's built files, which the binary carries as
	// data, and each API of the binary under its path, on one origin (D91).
	Assets = "embedded.assets"

	// SQLConnector and WorkerSQLConnector connect a server and a worker to
	// a database's file. HTTPConnector and WorkerHTTPConnector connect one
	// to an API another server of the binary serves, which the caller
	// reaches through the callee's handle, in the process. SiteConnector
	// connects the site to an API of the binary, on the site's own origin.
	SQLConnector        = "embedded.process-sqlite"
	WorkerSQLConnector  = "embedded.worker-sqlite"
	HTTPConnector       = "embedded.process-process"
	WorkerHTTPConnector = "embedded.worker-process"
	SiteConnector       = "embedded.assets-process"

	// PolicyNoDomain refuses an environment that sets a domain.
	PolicyNoDomain = "embedded-no-domain"

	// PolicyNoParameters refuses a parameterized environment.
	PolicyNoParameters = "embedded-no-parameters"

	// PolicyOneBinary refuses a deployable a settings platform places
	// outside the binary.
	PolicyOneBinary = "embedded-one-binary"

	// PolicyOneInstance refuses more than one instance of a worker.
	PolicyOneInstance = "embedded-one-instance"

	// PolicyOneSite refuses a second site: the binary serves one origin.
	PolicyOneSite = "embedded-one-site"
)

// The resource types the target's platforms emit: the `embedded`
// provider's. Nothing applies them; they say what the binary holds.
const (
	// TypeProcess is a server or a worker of the binary.
	TypeProcess = "embedded:binary/process:Process"

	// TypeDatabase is a SQLite file under the binary's data directory.
	TypeDatabase = "embedded:sqlite/database:Database"

	// TypeSite is the site whose files the binary carries.
	TypeSite = "embedded:binary/site:Site"

	// TypeLoopback is the key a server of the binary signs its calls to
	// another server of the binary with, which the process generates as it
	// starts and which never leaves it (docs/stack-model.md, section 9.9).
	TypeLoopback = "embedded:binary/loopback:Key"
)

const (
	// Loopback is the address the binary listens on unless it is told
	// another.
	Loopback = "127.0.0.1"

	// DefaultPort is the port the binary listens on when PORT is unset and
	// the environment's values name none.
	DefaultPort = 8080

	// DatabasesDir is the directory, under the binary's data directory,
	// that holds each database's file, as `stack dev` lays them out.
	DatabasesDir = "databases"

	// LoopbackLifetimeSeconds is the longest a token signed with a
	// server's loopback key lives, the HTTP runtime's
	// LOOPBACK_LIFETIME_SECONDS.
	LoopbackLifetimeSeconds = 300
)

// Extension is the embedded extension. Its zero value is the one a
// distribution links.
type Extension struct{}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the embedded target, its platforms and connectors, the
// schema of each resource type they emit, and its policy rules. It
// registers no provisioner and no deploy seam: `stack deploy` refuses an
// environment on the target and names `stack build`.
func (Extension) Register(r *registry.Registry) error {
	none := func(registry.PlatformContext) any { return nil }
	for _, spec := range []registry.PlatformSpec{
		{
			Name:      Process,
			Kind:      ir.DeployableServer,
			Languages: []string{registry.APILanguageRust},
			Artifact:  ir.ArtifactBinary,
			// The binary serves plain HTTP on loopback, where a browser
			// drops a Secure cookie.
			IdentityConfig:  identityConfig,
			NameOf:          deployableName,
			AddressOf:       origin,
			PublicAddressOf: origin,
			Lower:           lowerProcess,
		},
		{
			Name:      Worker,
			Kind:      ir.DeployableWorker,
			Languages: []string{registry.APILanguageRust},
			Artifact:  ir.ArtifactBinary,
			NameOf:    deployableName,
			AddressOf: none,
			Lower:     lowerProcess,
		},
		{
			Name:     SQLite,
			Kind:     ir.DeployableDatabase,
			Dialects: []string{registry.SQLDialectSQLite},
			Settings: json.RawMessage(databaseSettings),
			NameOf:   deployableName,
			// A file has no address: each edge derives its file.
			AddressOf: none,
			Lower:     lowerDatabase,
		},
		{
			Name:            Assets,
			Kind:            ir.DeployableSite,
			NameOf:          deployableName,
			AddressOf:       origin,
			PublicAddressOf: origin,
			ServesOrigin:    true,
			Lower:           lowerSite,
		},
	} {
		spec.Extension, spec.Provider = Name, Provider
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	for _, spec := range []registry.ConnectorSpec{
		{Name: SQLConnector, Edge: ir.EdgeSQL, From: Process, To: SQLite, Connect: connectSQL},
		{Name: WorkerSQLConnector, Edge: ir.EdgeSQL, From: Worker, To: SQLite, Connect: connectSQL},
		{Name: HTTPConnector, Edge: ir.EdgeHTTP, From: Process, To: Process, Connect: connectHTTP},
		{Name: WorkerHTTPConnector, Edge: ir.EdgeHTTP, From: Worker, To: Process, Connect: connectHTTP},
		{Name: SiteConnector, Edge: ir.EdgeSite, From: Assets, To: Process, Connect: connectSite},
	} {
		spec.Extension = Name
		if err := r.RegisterConnector(spec); err != nil {
			return err
		}
	}
	types := make(map[string]json.RawMessage, len(resourceTypes))
	for typ, schema := range resourceTypes {
		types[typ] = json.RawMessage(schema)
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      Target,
		Extension: Name,
		Platforms: map[ir.DeployableKind]string{
			ir.DeployableServer:   Process,
			ir.DeployableWorker:   Worker,
			ir.DeployableDatabase: SQLite,
			ir.DeployableSite:     Assets,
		},
		Unplaced: map[ir.DeployableKind]string{
			ir.DeployableJob:         "the binary is one process a person starts, and it runs no job on a schedule or on demand (D116); run the job from an environment on another target",
			ir.DeployableBucket:      "the binary keeps no bucket: one on a local directory is not built (D116); keep the objects in an environment on another target",
			ir.DeployableMemoryStore: "the binary reaches no memory store server, and the runtime's in-process store is not wired into it yet (D116)",
			ir.DeployableRunner:      "the binary opens no engine, whose runner this is: an engine on a SQLite file is not built (D116)",
			ir.DeployableDeriver:     "the binary opens no engine, whose derived fields a deriver fills: an engine on a SQLite file is not built (D116)",
			ir.DeployableApp:         "the binary serves each API through its handle, and an app is its API's own HTTP program (D78), which it does not link (D116)",
		},
		Values:        json.RawMessage(targetValues),
		ResourceTypes: types,
		Policies: []registry.PolicyRule{
			{Name: PolicyNoDomain, Check: checkNoDomain},
			{Name: PolicyNoParameters, Check: checkNoParameters},
			{Name: PolicyOneBinary, Check: checkOneBinary},
			{Name: PolicyOneInstance, Check: checkOneInstance},
			{Name: PolicyOneSite, Check: checkOneSite},
		},
	})
}

// targetValues is the schema of an embedded environment's values: the
// port the binary listens on when PORT is unset.
const targetValues = `{
  "type": "object",
  "properties": {
    "port": {"type": "integer", "minimum": 1, "maximum": 65535}
  },
  "additionalProperties": false
}`

// databaseSettings is the schema of a database's settings on the target:
// its one adoption (D116), the model a file built by hand is recorded at
// and the statement that says the file holds it. The model is a path,
// from the Stack service's directory, to a model document, as
// `superschematic migrate plan --from-database <file> --print-model`
// prints one. The statement answers one row holding the integer 1.
const databaseSettings = `{
  "type": "object",
  "properties": {
    "adoption": {
      "type": "object",
      "required": ["model", "check"],
      "properties": {
        "model": {"type": "string", "minLength": 1},
        "check": {"type": "string", "minLength": 1}
      },
      "additionalProperties": false
    }
  },
  "additionalProperties": false
}`

// identityConfig is the identity config the binary runs each API over the
// user model with unless its environment sets the API's identity config
// field: a session cookie without Secure, since it serves plain HTTP.
const identityConfig = `{"cookie":{"secure":false}}`

// resourceTypes are the schemas of the embedded provider's resource
// types. Every object is closed.
var resourceTypes = map[string]string{
	TypeProcess: `{
	  "type": "object",
	  "required": ["name", "kind", "serves"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "kind": {"enum": ["server", "worker"]},
	    "serves": {"type": "array", "items": {"type": "string", "minLength": 1}}
	  },
	  "additionalProperties": false
	}`,
	TypeDatabase: `{
	  "type": "object",
	  "required": ["name", "service", "file"],
	  "properties": {
	    "name": {"type": "string", "pattern": "^[a-z_][a-z0-9_]*$"},
	    "service": {"type": "string", "minLength": 1},
	    "file": {"type": "string", "pattern": "^databases/[a-z_][a-z0-9_]*\\.sqlite$"}
	  },
	  "additionalProperties": false
	}`,
	TypeSite: `{
	  "type": "object",
	  "required": ["name", "dir", "build", "output"],
	  "properties": {
	    "name": {"type": "string", "minLength": 1},
	    "dir": {"type": "string", "minLength": 1},
	    "build": {"type": "string", "minLength": 1},
	    "output": {"type": "string", "minLength": 1},
	    "fallback": {"type": "string", "minLength": 1}
	  },
	  "additionalProperties": false
	}`,
	TypeLoopback: `{
	  "type": "object",
	  "required": ["server", "algorithm"],
	  "properties": {
	    "server": {"type": "string", "minLength": 1},
	    "algorithm": {"enum": ["Ed25519"]}
	  },
	  "additionalProperties": false
	}`,
}

// Port returns the port the binary listens on when PORT is unset: the
// environment's port value, or DefaultPort.
func Port(env registry.StackEnvironment) int {
	switch port := env.Values["port"].(type) {
	case int:
		return port
	case int64:
		return int(port)
	case float64:
		return int(port)
	case json.Number:
		if n, err := port.Int64(); err == nil {
			return int(n)
		}
	}
	return DefaultPort
}

// origin is the binary's one origin, which every server and the site
// share: its loopback address at the environment's port.
func origin(ctx registry.PlatformContext) any {
	return "http://" + Loopback + ":" + strconv.Itoa(Port(ctx.Environment))
}

// deployableName names a deployable after itself, in lower case with its
// words joined by hyphens.
func deployableName(ctx registry.PlatformContext) any { return kebab(ctx.Deployable.Name) }

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

// DatabaseName returns the name of a hosted DB schema's file: the
// service's name in lower snake case (`shop-db` is `shop_db`), as the
// local target names it.
func DatabaseName(service string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(service) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "_" + name
	}
	return name
}

// DatabaseFile returns the file of a hosted DB schema, relative to the
// binary's data directory: `databases/<database>.sqlite`.
func DatabaseFile(service string) string {
	return path.Join(DatabasesDir, DatabaseName(service)+".sqlite")
}

// databaseID is the ID of the node of a hosted DB schema's file.
func databaseID(deployable, service string) string {
	return deployable + ".database." + service
}

// loopbackID is the ID of the node of a server's loopback key.
func loopbackID(server string) string { return server + ".loopback" }

// lowerProcess lowers a server or a worker to its node: its name and the
// APIs it serves. The binary reads its configuration when it starts, so
// the node holds no binding.
func lowerProcess(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	serves := make([]any, len(d.Services))
	for i, ref := range d.Services {
		serves[i] = ref.Name
	}
	return registry.Lowered{Resources: []*ir.Resource{{
		ID:   d.Name + ".process",
		Type: TypeProcess,
		Properties: map[string]any{
			"name":   d.ResourceName,
			"kind":   string(d.Kind),
			"serves": serves,
		},
	}}}, nil
}

// lowerDatabase lowers a database to a file per hosted DB schema under
// the binary's data directory. The binary creates and migrates each as it
// starts, from the plans it carries.
func lowerDatabase(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	var out registry.Lowered
	for _, svc := range d.Services {
		out.Resources = append(out.Resources, &ir.Resource{
			ID:   databaseID(d.Name, svc.Name),
			Type: TypeDatabase,
			Properties: map[string]any{
				"name":    DatabaseName(svc.Name),
				"service": svc.Name,
				"file":    DatabaseFile(svc.Name),
			},
			Phase: ir.PhaseInfrastructure,
		})
	}
	return out, nil
}

// lowerSite lowers a site to its node: its package, the script that
// builds it and the directory the build writes, which `stack build`
// compiles into the binary.
func lowerSite(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	if d.Site == nil {
		return registry.Lowered{}, fmt.Errorf("site %s says nothing of how it builds", d.Name)
	}
	props := map[string]any{
		"name":   d.ResourceName,
		"dir":    d.Site.Dir,
		"build":  d.Site.Build,
		"output": d.Site.Output,
	}
	if d.Site.Fallback != "" {
		props["fallback"] = d.Site.Fallback
	}
	return registry.Lowered{Resources: []*ir.Resource{{ID: d.Name + ".site", Type: TypeSite, Properties: props}}}, nil
}

// connectSQL derives the file of the edge's database: a sqlite: URL of
// its path under the data directory, which the binary resolves against
// the directory it is started with. The edge needs no resource: a file
// checks no caller.
func connectSQL(ctx registry.ConnectorContext) (registry.Connected, error) {
	return registry.Connected{Value: ir.DatabaseConnection{URL: "sqlite:" + DatabaseFile(ctx.Edge.Service.Name)}}, nil
}

// apiURL is where the binary's origin serves the API service named
// service: its path under the origin (D91).
func apiURL(origin any, service string) any {
	return ir.SiteOriginURL(origin, service)
}

// connectHTTP derives where the callee API is served on the binary's
// origin. The caller never dials it: its SDK's transport calls the
// callee's handle in the process. A call from one server of the binary to
// another carries the caller's loopback credential (docs/stack-model.md,
// section 9.9), a token signed with a key the process generates as it
// starts, so the callee gets the caller as an issuer of its own, whose
// one caller is the caller's deployable, serving the APIs it serves: a
// service clause holds as it does between two servers. A call within one
// server has none, as on any target.
func connectHTTP(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	endpoint := ir.ServiceEndpoint{URL: apiURL(to.Address, ctx.Edge.Service.Name)}
	if from.Name == to.Name {
		return registry.Connected{Value: endpoint}, nil
	}
	serves := make([]string, len(from.Services))
	for i, ref := range from.Services {
		serves[i] = ref.Name
	}
	key := loopbackID(from.Name)
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:         key,
			Type:       TypeLoopback,
			Properties: map[string]any{"server": from.Name, "algorithm": "Ed25519"},
			Phase:      ir.PhaseInfrastructure,
		}},
		Value: endpoint,
		Callee: ir.ServiceAuthIssuer{
			Issuer:             from.Name,
			Audience:           from.Name,
			Algorithms:         []string{ir.AlgorithmEdDSA},
			Keys:               []ir.ServiceAuthKey{{JWK: ir.Output{Resource: key, Name: "publicJwk"}}},
			MaxLifetimeSeconds: LoopbackLifetimeSeconds,
			Callers:            []ir.ServiceAuthCaller{{Subject: from.Name, Deployable: from.Name, Serves: serves}},
		},
	}, nil
}

// connectSite derives where the site's page reaches an API of the binary:
// under the API's path on the site's own origin, which is the binary's
// (D91). The binary answers the site's config from the address a request
// names, so the page reaches the API wherever the binary listens.
func connectSite(ctx registry.ConnectorContext) (registry.Connected, error) {
	url := apiURL(ctx.From.PublicAddress, ctx.Edge.Service.Name)
	if ctx.From.Site.OriginAPI(ctx.Edge.Service.Name) != nil {
		return registry.Connected{Value: ir.SiteEndpoint{URL: url, Upstream: ctx.To.Address}}, nil
	}
	return registry.Connected{Value: ir.SiteEndpoint{URL: url}}, nil
}

// platforms are the platforms that place a deployable inside the binary.
var platforms = []string{Process, Worker, SQLite, Assets}

// checkNoDomain refuses a domain: the binary is reached on the machine it
// is installed on.
func checkNoDomain(env *ir.ResolvedEnvironment) []string {
	if env.Domain == "" {
		return nil
	}
	return []string{fmt.Sprintf("environment %s sets domain %s, but the embedded target builds a binary a person installs, which listens on %s and writes no DNS records; leave the domain to an environment on a cloud target", env.Environment, env.Domain, Loopback)}
}

// checkNoParameters refuses a parameterized environment: a binary is one
// copy of its environment.
func checkNoParameters(env *ir.ResolvedEnvironment) []string {
	if len(env.Parameters) == 0 {
		return nil
	}
	return []string{fmt.Sprintf("environment %s takes parameters %s, but the embedded target builds one binary of each environment; give a parameterized environment a cloud target", env.Environment, strings.Join(env.Parameters, ", "))}
}

// checkOneBinary refuses a deployable a settings platform places outside
// the binary: an environment on the target is one process.
func checkOneBinary(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, d := range env.Deployables {
		if d.Platform == "" || slices.Contains(platforms, d.Platform) {
			continue
		}
		out = append(out, fmt.Sprintf("%s %s is placed on platform %s, but an environment on the embedded target is one binary, which holds every deployable of it (D116); take the platform out of its settings, or give the environment another target", d.Kind, d.Name, d.Platform))
	}
	return out
}

// checkOneInstance refuses more than one instance of a worker: the
// binary is one process, and runs each worker once.
func checkOneInstance(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, d := range env.Deployables {
		if d.Worker == nil || (d.Worker.Instances <= 1 && d.Worker.MaxInstances <= 1) {
			continue
		}
		out = append(out, fmt.Sprintf("worker %s runs %d instances (at most %d), but an environment on the embedded target is one process, which runs one instance of each worker (D116); set its instances to 1", d.Name, d.Worker.Instances, max(d.Worker.Instances, d.Worker.MaxInstances)))
	}
	return out
}

// checkOneSite refuses a second site: the binary answers every path that
// is no API's from one site, on one origin.
func checkOneSite(env *ir.ResolvedEnvironment) []string {
	var sites []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableSite {
			sites = append(sites, d.Name)
		}
	}
	if len(sites) <= 1 {
		return nil
	}
	return []string{fmt.Sprintf("environment %s has sites %s, but an environment on the embedded target is one binary on one origin, which answers every path that is no API's from one site (D116); deploy one site, or give the environment another target", env.Environment, strings.Join(sites, ", "))}
}
