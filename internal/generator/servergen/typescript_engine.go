package servergen

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/registry"
)

// The engine in a TypeScript entrypoint (D58), the twin of a Go
// entrypoint's engine.go: main.ts opens the engine of each engine database
// it connects to with the engine's ./server entry, through its Node-API
// addon, in the background, retrying until the database answers, so it
// starts while the database is not up yet, as its pools do. A server's
// deploys the database's engine document before it reports the engine
// ready, and mounts the engine's routes, event stream and MCP endpoint on
// its Hono app at the database's engine mount, behind the session the API's
// identity service authenticates (D50) and the service caller its service
// authenticator verifies (D37); a job's and a worker's open it for their
// API's Deps and deploy nothing; a runner's starts the engine's runner.

// TypeScriptEngine is the engine a TypeScript entrypoint opens on one
// engine database (D58).
type TypeScriptEngine struct {
	// Service is the engine database, a DB service; Var is the variable
	// that holds what openEngine returns.
	Service string
	Var     string

	// Document is the name main.ts imports the database's engineDocument
	// as, from DocumentModule, the ./engine entry of its TypeScript types
	// package, DocumentPackage.
	Document        string
	DocumentModule  string
	DocumentPackage string

	// Namespace, Mount and Policy are what apigen.EngineInfo holds of it;
	// Policy is the access policy's JSON, which main.ts writes as it is.
	Namespace string
	Mount     string
	Policy    string

	// Types are the engine types, sorted, which the runner's principal
	// holds as its permissions.
	Types []string

	// Behaviors are the behaviors the engine types compose beside the
	// core's, which the engine registers by name after the core's: the
	// work queue's, sorted, which the core's addon carries, then the
	// extensions', sorted, which the project's engine crate's addon
	// carries (its napi member, EngineProject).
	Behaviors []string

	// Database is the pool's database, whose field the engine opens on.
	Database *TypeScriptDatabase

	// API is the API whose callers the engine's routes admit, the first,
	// by name, that connects to the database, on a server; nil for a
	// job's, a worker's or a runner's engine, which serve no request.
	// Routes is the variable that holds the engine's routes under its
	// mount, which the API's site CORS matches beside its operations;
	// empty when no site calls the API.
	API    *TypeScriptAPI
	Routes string
}

// ReadinessName is how /readyz names the engine: its database's name and
// engine.
func (e *TypeScriptEngine) ReadinessName() string {
	return e.Service + " engine"
}

// PolicyLiteral is the access policy as main.ts writes it: a shorthand
// quoted, or the JSON of its rules as it is.
func (e *TypeScriptEngine) PolicyLiteral() string {
	var shorthand string
	if json.Unmarshal([]byte(e.Policy), &shorthand) == nil {
		return tsQuote(shorthand)
	}
	return e.Policy
}

// OpenOptions are the options main.ts opens the engine with beside its
// document and policy, members (setup, runner) and the work queue's
// behaviors it composes, as the call's last argument with its comma; empty
// when there are none.
func (e *TypeScriptEngine) OpenOptions(members ...string) string {
	if len(e.Behaviors) > 0 {
		quoted := make([]string, len(e.Behaviors))
		for i, b := range e.Behaviors {
			quoted[i] = tsQuote(b)
		}
		members = append([]string{"behaviors: [" + strings.Join(quoted, ", ") + "]"}, members...)
	}
	if len(members) == 0 {
		return ""
	}
	return ", { " + strings.Join(members, ", ") + " }"
}

// TypeScriptRunner is what a TypeScript engine's runner's entrypoint runs
// (D58): the engine of its database.
type TypeScriptRunner struct {
	Database *TypeScriptDatabase
}

// TypeScriptRunnerInput is what the entrypoint of a TypeScript engine's
// runner is planned from (D58): its engine database, the config field its
// connection is read from, and what apigen.EngineInfo holds of it, read
// from the first API on it.
type TypeScriptRunnerInput struct {
	Database string
	Field    string
	Engine   *apigen.EngineInfo
}

// planTypeScriptEngine gives api, which serves o and whose Deps holds the
// pool d, the engine of its engine database, the one engine main.ts opens
// on d for every API on it; on a server, the first API on the database, by
// name, admits the engine's callers and mounts its routes. It refuses an
// engine database that is not the API's database, a mount the server
// answers itself or its APIs' routes live under, and two engines on one
// mount.
func (s *TypeScriptServer) planTypeScriptEngine(in TypeScriptInput, api *TypeScriptAPI, info *apigen.EngineInfo, schema string, taken names, mounts map[string]string) error {
	d := api.Database
	if d == nil || d.Service != info.Database {
		return fmt.Errorf("stack %s: %s %s serves %s, whose engine database %s is not the database its Deps holds", in.Stack, s.Kind, in.Server, schema, info.Database)
	}
	if d.Engine == nil {
		d.Engine = s.newTypeScriptEngine(d, info, taken)
	}
	api.Engine = d.Engine
	if s.Background() || d.Engine.API != nil {
		return nil
	}
	mount := d.Engine.Mount
	for _, reserved := range reservedMounts {
		if mount == reserved || strings.HasPrefix(mount, reserved+"/") {
			return fmt.Errorf("stack %s: server %s mounts the engine of %s at %s, which the server answers itself or its APIs' routes live under (%s); set outputs.engine.mount in %s's config to another path", in.Stack, in.Server, d.Service, mount, strings.Join(reservedMounts, ", "), d.Service)
		}
	}
	if other, clash := mounts[mount]; clash {
		return fmt.Errorf("stack %s: server %s mounts the engines of %s and %s at %s; set outputs.engine.mount in one's config to another path", in.Stack, in.Server, other, d.Service, mount)
	}
	mounts[mount] = d.Service
	d.Engine.API = api
	api.MountsEngine = true
	if api.Callers != "" {
		api.ServiceAuthenticator = taken.take(varStem(api.Service) + "ServiceAuthenticator")
	}
	if api.CORS != "" {
		d.Engine.Routes = taken.take(varStem(d.Service) + "EngineRoutes")
	}
	return nil
}

// newTypeScriptEngine is the TypeScriptEngine of database d, whose engine
// info is info, and records it among the entrypoint's engines.
func (s *TypeScriptServer) newTypeScriptEngine(d *TypeScriptDatabase, info *apigen.EngineInfo, taken names) *TypeScriptEngine {
	stem := varStem(d.Service)
	pkg := s.Naming.NpmTypesPackage(d.Service)
	e := &TypeScriptEngine{
		Service:         d.Service,
		Var:             taken.take(stem + "Engine"),
		Document:        taken.take(stem + "EngineDocument"),
		DocumentModule:  pkg + "/engine",
		DocumentPackage: pkg,
		Namespace:       info.Namespace,
		Mount:           info.Mount,
		Policy:          string(info.Policy),
		Types:           slices.Clone(info.Types),
		Database:        d,
	}
	var extensions []string
	for _, b := range info.Behaviors {
		switch {
		case b.Extension != "":
			extensions = append(extensions, b.Name)
		case b.Package == registry.WorkQueuePackage:
			e.Behaviors = append(e.Behaviors, b.Name)
		}
	}
	slices.Sort(e.Behaviors)
	slices.Sort(extensions)
	e.Behaviors = append(e.Behaviors, extensions...)
	s.Engines = append(s.Engines, e)
	return e
}

// planTypeScriptRunner plans the entrypoint of a TypeScript engine's
// runner (D58): main.ts reads its database's connection, opens the engine
// on it, retrying until the database answers, starts the engine's runner
// and runs it until SIGTERM, as the runner's lock lets it. It reads no
// API's config.
func (s *TypeScriptServer) planTypeScriptRunner(in TypeScriptInput, taken names) error {
	r := in.Runner
	if r.Engine == nil {
		return fmt.Errorf("stack %s: runner %s runs the engine of %s, which holds no engine type", in.Stack, in.Server, r.Database)
	}
	d := &TypeScriptDatabase{
		Service: r.Database,
		Var:     taken.take(varStem(r.Database)),
		Field:   r.Field,
	}
	d.Engine = s.newTypeScriptEngine(d, r.Engine, taken)
	s.Databases = append(s.Databases, d)
	if slices.Contains(in.CloudSQL, r.Database) {
		s.CloudSQL = append(s.CloudSQL, r.Database)
	}
	s.Kind = "runner"
	s.Runner = &TypeScriptRunner{Database: d}
	return nil
}

// RunnerSubject is the subject the engine's runner acts as, its
// deployable's name: what its reactions' and Timers' events record as
// their actor, and what a rules policy names it by.
func (s *TypeScriptServer) RunnerSubject() string {
	return s.Name
}

// EngineImports are the names main.ts imports from the engine's ./server
// entry.
func (s *TypeScriptServer) EngineImports() []string {
	out := []string{"openEngineInBackground"}
	if s.Job == nil && s.Worker == nil && s.Runner == nil {
		out = append(out, "engineApp", "engineMcp")
		if slices.ContainsFunc(s.Engines, func(e *TypeScriptEngine) bool { return e.Routes != "" }) {
			out = append(out, "engineRouteSpecs")
		}
	}
	out = append(out, "type AsyncEngine", "type EngineBackground")
	if s.Job == nil && s.Worker == nil && s.Runner == nil {
		out = append(out, "type EngineHttpOptions")
	}
	return append(out, "type EngineOptions", "type Principal")
}

// tsEngineDocker is where a TypeScript entrypoint's checkout Dockerfile
// builds the engine's Node-API addon from (D58): the engine's npm package,
// the Cargo workspace beside it, and the crates outside it its manifests
// reach by path, at their places in the checkout, each relative to the
// build context; and, for an engine database that composes an extension's
// behavior, the project's engine crate, whose napi member it builds in the
// core's addon's place.
type tsEngineDocker struct {
	// TypeScript is the engine's package, whose native/ the core's addon
	// goes in and whose ./server entry the build emits; Workspace the
	// Cargo workspace beside it.
	TypeScript string
	Workspace  string
	Crates     []string

	// Project is the project's engine crate, empty for the core's addon;
	// ProjectCrates the extensions' crates it reaches by path; Addon the
	// .node file its napi member builds, which the image keeps at
	// AddonPath and names with SUPERSCHEMATIC_ENGINE_ADDON.
	Project       string
	ProjectCrates []string
	ProjectLib    string
	Addon         string
}

// AddonPath is where the image keeps the project's engine addon, which its
// SUPERSCHEMATIC_ENGINE_ADDON names.
func (d *tsEngineDocker) AddonPath() string {
	return "/engine/" + d.Addon
}
