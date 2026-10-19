package servergen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The engine of a Rust entrypoint (D58; docs/stack-model.md, section 8.6):
// a Rust server, job, worker or runner whose API's database is an engine
// database opens the engine on it through the engine's crate,
// superschematic-engine, linked into its binary. src/engine.rs, beside
// main.rs, opens each engine in the background (superschematic_engine::
// background::Background), on the blocking pool, retrying until the
// database answers, so the entrypoint starts while it is not up yet, as
// its pools do; a server's deploys the database's engine document (its Rust
// types' ENGINE_DOCUMENT) before it reports the engine ready, and mounts the
// engine's routes (superschematic_engine::axum::background_router) beside
// the API's, behind the session the identity runtime authenticates (D50)
// and the service caller the API's service authenticator verifies (D37); a
// runner's starts the engine's runner. The engine runs the behaviors of the
// core (superschematic-engine-behaviors) or, when the database composes the
// work queue's or an extension's, of the project's engine crate.

// RustEngineFile is the module of a Rust entrypoint that opens its engines.
const RustEngineFile = "src/engine.rs"

// The features of the engine's crate a Rust entrypoint turns on: a
// server's mounts the engine's axum router, and every one opens the engine
// on Postgres in the background.
var (
	rustServerEngineFeatures     = []string{"axum", "postgres"}
	rustBackgroundEngineFeatures = []string{"background", "postgres"}
)

// The engine's crates a Rust entrypoint depends on by path.
const (
	rustEngineCrate          = "superschematic-engine"
	rustEngineBehaviorsCrate = "superschematic-engine-behaviors"
)

// RustEngine is the engine a Rust entrypoint opens on one engine database.
type RustEngine struct {
	// Service is the engine database, a DB service; Var is the variable
	// that holds its Background.
	Service string
	Var     string

	// Namespace, Mount and Policy are what apigen.EngineInfo holds of it;
	// Policy is the access policy's JSON.
	Namespace string
	Mount     string
	Policy    string

	// Types are the engine types, sorted: the runner's principal holds
	// each as a permission.
	Types []string

	// TypesCrate is the database's Rust types crate, which holds its
	// ENGINE_DOCUMENT, and TypesAlias the name main.rs uses it as.
	TypesCrate string
	TypesAlias string

	// Database is the pool's database, whose connection field the engine
	// opens on.
	Database *RustDatabase

	// API is the API whose server mounts the engine's routes, and Routes
	// the variable of the router of them: the first API, by name, on the
	// database, nil for a job's, a worker's or a runner's engine.
	API    *RustAPI
	Routes string
}

// ReadinessName is how /readyz names the engine: its database's name and
// engine.
func (e *RustEngine) ReadinessName() string {
	return e.Service + " engine"
}

// PolicyLiteral is the policy as a Rust string literal: a raw one, unless
// its JSON holds the raw string's end.
func (e *RustEngine) PolicyLiteral() string {
	if strings.Contains(e.Policy, `"#`) {
		return rustString(e.Policy)
	}
	return `r#"` + e.Policy + `"#`
}

// OpenLet is main.rs's statement that opens e in the background (engine.rs
// sets up what the entrypoint's kind runs once it opens: a server deploys
// the engine document, a runner starts the runner).
func (s *RustServer) OpenLet(e *RustEngine) string {
	return rustLetCall(e.Var, "engine::open", "?",
		rustString(e.Service), rustString(e.Database.Field), "&"+e.Database.From,
		e.TypesAlias+"::ENGINE_DOCUMENT", e.PolicyLiteral())
}

// RoutesLet is main.rs's statement that builds the router of e's routes,
// with the callers of the API that mounts it, laid out as rustfmt lays it
// out: the callers' struct a block of its own.
func (s *RustServer) RoutesLet(e *RustEngine) string {
	a := e.API
	var fields []string
	if s.EngineIdentity() {
		users := "None"
		if a.Identity != "" {
			users = "Some(Arc::clone(&" + a.Identity + "))"
		}
		fields = append(fields, "users: "+users)
	}
	if s.EngineServiceAuth() {
		services := "None"
		if a.ServiceAuthenticator != "" {
			services = "Some(Arc::clone(&" + a.ServiceAuthenticator + ") as _)"
		}
		fields = append(fields, "services: "+services)
	}
	const indent = "    "
	callers := "engine::Callers {}"
	if len(fields) > 0 {
		callers = "engine::Callers {\n" + indent + indent + indent + strings.Join(fields, ",\n"+indent+indent+indent) + ",\n" + indent + indent + "}"
	}
	return indent + "let " + e.Routes + " = engine::routes(\n" +
		indent + indent + "&" + e.Var + ",\n" +
		indent + indent + rustString(e.Mount) + ",\n" +
		indent + indent + callers + ",\n" +
		indent + indent + "&streams,\n" +
		indent + ")?;"
}

// EngineRefuses reports whether an API that mounts an engine's routes
// verifies a credential, whose refusal the engine answers.
func (s *RustServer) EngineRefuses() bool {
	return s.EngineIdentity() || s.EngineServiceAuth()
}

// RuntimeIdent is the HTTP runtime's crate as Rust source spells it.
func (s *RustServer) RuntimeIdent() string {
	return crateIdent(s.Naming.HTTPRuntimeRustCrate)
}

// EngineBehaviors is the expression of the behaviors the engines run: the
// project's engine crate's, or the core's.
func (s *RustServer) EngineBehaviors() string {
	if s.EngineProject != nil {
		return s.EngineProject.Ident + "::behaviors()"
	}
	return crateIdent(rustEngineBehaviorsCrate) + "::core()"
}

// EngineServices lists the engine databases, for comments.
func (s *RustServer) EngineServices() string {
	names := make([]string, len(s.Engines))
	for i, e := range s.Engines {
		names[i] = e.Service
	}
	return joinNames(names)
}

// MountsEngines reports whether the entrypoint mounts an engine's routes.
func (s *RustServer) MountsEngines() bool {
	return slices.ContainsFunc(s.Engines, func(e *RustEngine) bool { return e.API != nil })
}

// EngineIdentity reports whether an API that mounts an engine's routes
// authenticates its end users with the identity runtime.
func (s *RustServer) EngineIdentity() bool {
	return slices.ContainsFunc(s.Engines, func(e *RustEngine) bool { return e.API != nil && e.API.Identity != "" })
}

// EngineServiceAuth reports whether an API that mounts an engine's routes
// verifies its callers' service credentials.
func (s *RustServer) EngineServiceAuth() bool {
	return slices.ContainsFunc(s.Engines, func(e *RustEngine) bool { return e.API != nil && e.API.ServiceAuthenticator != "" })
}

// EngineSiteCORS reports whether a site calls an API that mounts an
// engine's routes, which then answer the site's CORS too.
func (s *RustServer) EngineSiteCORS() bool {
	return slices.ContainsFunc(s.Engines, func(e *RustEngine) bool { return e.API != nil && e.API.CORS != "" })
}

// planRustEngines gives each database an API of s connects to whose engine
// database it is a RustEngine, and each such API the engine its Deps holds,
// the first API on it, by name, mounting its routes on a server. It refuses
// an engine database that is not the API's database or has no Rust types,
// a mount a server answers itself or its APIs' routes live under, and two
// engines on one mount.
func (s *RustServer) planRustEngines(in RustInput, taken rustNames) error {
	mounts := map[string]string{}
	for i, a := range in.APIs {
		if a.Routes == nil || a.Routes.Engine == nil {
			continue
		}
		info, api := a.Routes.Engine, s.APIs[i]
		d := api.Database
		if d == nil || d.Service != info.Database {
			return fmt.Errorf("stack %s: %s %s serves %s, whose engine database %s is not the database its Deps holds", in.Stack, s.Kind, in.Server, api.Service, info.Database)
		}
		e := s.engineOf(d)
		if e == nil {
			var err error
			if e, err = s.newRustEngine(in, d, info, taken); err != nil {
				return err
			}
		}
		api.Engine = e
		if s.Background() || e.API != nil {
			continue
		}
		for _, reserved := range reservedMounts {
			if e.Mount == reserved || strings.HasPrefix(e.Mount, reserved+"/") {
				return fmt.Errorf("stack %s: server %s mounts the engine of %s at %s, which the server answers itself or its APIs' routes live under (%s); set outputs.engine.mount in %s's config to another path", in.Stack, in.Server, d.Service, e.Mount, strings.Join(reservedMounts, ", "), d.Service)
			}
		}
		if other, clash := mounts[e.Mount]; clash {
			return fmt.Errorf("stack %s: server %s mounts the engines of %s and %s at %s; set outputs.engine.mount in one's config to another path", in.Stack, in.Server, other, d.Service, e.Mount)
		}
		mounts[e.Mount] = d.Service
		e.API = api
		e.Routes = taken.take(rustStem(d.Service) + "_engine_routes")
		if api.Identity != "" {
			api.Authenticator = taken.take(rustStem(api.Service) + "_authenticator")
		}
	}
	return nil
}

// engineOf is the engine s opens on d, nil when it opens none yet.
func (s *RustServer) engineOf(d *RustDatabase) *RustEngine {
	for _, e := range s.Engines {
		if e.Database == d {
			return e
		}
	}
	return nil
}

// newRustEngine is the engine of database d, whose engine info is info,
// added to s's.
func (s *RustServer) newRustEngine(in RustInput, d *RustDatabase, info *apigen.EngineInfo, taken rustNames) (*RustEngine, error) {
	if in.EngineTypes[d.Service] == "" {
		return nil, fmt.Errorf("stack %s: %s %s opens the engine of %s (D58), whose engine document its Rust types hold, and %s generates none; enable outputs.types.rust in %s's config", in.Stack, s.Kind, in.Server, d.Service, d.Service, d.Service)
	}
	e := &RustEngine{
		Service:    d.Service,
		Var:        taken.take(rustStem(d.Service) + "_engine"),
		Namespace:  info.Namespace,
		Mount:      info.Mount,
		Policy:     string(info.Policy),
		Types:      slices.Clone(info.Types),
		TypesCrate: s.Naming.RustTypesCrate(d.Service),
		TypesAlias: taken.take(rustStem(d.Service) + "_types"),
		Database:   d,
	}
	s.Engines = append(s.Engines, e)
	return e, nil
}

// engineDependencies are the Cargo.toml dependencies of the entrypoint's
// engines, by crate, each a path dependency relative to the crate's
// directory: the engine's crate with the features the kind uses, the
// project's engine crate or the core's behaviors, and each engine
// database's Rust types.
func (s *RustServer) engineDependencies(in RustInput, pathDep func(string, ...string) (string, error)) (map[string]string, error) {
	if len(s.Engines) == 0 {
		return nil, nil
	}
	if in.Paths.EngineRust == "" {
		return nil, fmt.Errorf("stack %s: %s %s opens the engine of %s (D58), whose crate the naming file's [paths] engine_rust names, and it is unset; set it to a checkout's runtime/engine/rust/engine", in.Stack, s.Kind, in.Server, s.Engines[0].Service)
	}
	features := rustServerEngineFeatures
	if s.Background() {
		features = rustBackgroundEngineFeatures
	}
	deps := map[string]string{}
	var err error
	if deps[rustEngineCrate], err = pathDep(in.Paths.EngineRust, features...); err != nil {
		return nil, err
	}
	deps[rustEngineCrate] = strings.Replace(deps[rustEngineCrate], ", features", ", default-features = false, features", 1)
	switch {
	case s.EngineProject != nil:
		spec, err := pathDep(s.EngineProject.Dir, "postgres")
		if err != nil {
			return nil, err
		}
		deps[s.EngineProject.Crate] = strings.Replace(spec, ", features", ", default-features = false, features", 1)
	case in.Paths.EngineBehaviorsRust == "":
		return nil, fmt.Errorf("stack %s: %s %s opens the engine of %s (D58), which runs the core's behaviors from the crate the naming file's [paths] engine_behaviors_rust names, and it is unset; set it to a checkout's runtime/engine/rust/behaviors", in.Stack, s.Kind, in.Server, s.Engines[0].Service)
	default:
		spec, err := pathDep(in.Paths.EngineBehaviorsRust, "postgres")
		if err != nil {
			return nil, err
		}
		deps[rustEngineBehaviorsCrate] = strings.Replace(spec, ", features", ", default-features = false, features", 1)
	}
	for _, e := range s.Engines {
		if deps[e.TypesCrate], err = pathDep(in.EngineTypes[e.Service]); err != nil {
			return nil, err
		}
	}
	return deps, nil
}

// engineDockerIncludes are the paths, relative to the build context, of
// the engine's crates a Rust image builds when a member of the workspace
// reaches them, as any may, whichever stack's: the engine's Cargo
// workspace and the crates outside it its manifests reach (engineCrates),
// the project's engine crates under the output root, and the extensions'
// crates the entrypoint's project's reaches. Empty when [paths]
// engine_rust is unset, so no member depends on the engine.
func engineDockerIncludes(in RustInput, s *RustServer, workspace string, rel func(string) (string, bool)) ([]string, string) {
	if in.Paths.EngineRust == "" {
		return nil, ""
	}
	// The engine's crate is <checkout>/runtime/engine/rust/engine, in the
	// workspace beside it.
	engineWorkspace := filepath.Dir(in.Paths.EngineRust)
	checkout := filepath.Dir(filepath.Dir(filepath.Dir(engineWorkspace)))
	dir, ok := rel(engineWorkspace)
	if !ok {
		return nil, fmt.Sprintf("the engine's Cargo workspace %s lies outside the repository root, the build context", engineWorkspace)
	}
	include := []string{dir, path.Join(workspace, "engine", "rust")}
	for _, crate := range engineCrates {
		r, ok := rel(filepath.Join(checkout, filepath.FromSlash(crate)))
		if !ok {
			return nil, fmt.Sprintf("%s, which the engine's workspace reaches, lies outside the repository root, the build context", crate)
		}
		include = append(include, r)
	}
	if s.EngineProject != nil {
		for _, crate := range s.EngineProject.Crates {
			r, ok := rel(crate)
			if !ok {
				return nil, fmt.Sprintf("%s, which the project's engine crate of %s reaches, lies outside the repository root, the build context", crate, s.EngineProject.Database)
			}
			include = append(include, r)
		}
	}
	return include, ""
}

// RustRunnerInput is what the entrypoint of a Rust engine's runner is
// planned from (D58).
type RustRunnerInput struct {
	// Stack is the stack's name; Runner is the runner's deployable.
	Stack  string
	Runner string

	// Dir is the crate's directory, ServerDir, and OutputRoot the root of
	// the Cargo workspace it joins.
	Dir        string
	OutputRoot string

	// Database is the engine database, Field the config field its
	// connection is read from, and Engine what apigen.EngineInfo holds of
	// it, read from the first API on it.
	Database string
	Field    string
	Engine   *apigen.EngineInfo

	// TypesDir is the database's Rust types crate, which holds its engine
	// document.
	TypesDir string

	// Naming, RepositoryRoot, Paths and EngineProject are as a RustInput's.
	Naming         naming.Naming
	RepositoryRoot string
	Paths          naming.LocalPaths
	EngineProject  *EngineProject
}

// RustRunner is what a Rust runner's entrypoint runs: the engine of its
// database.
type RustRunner struct {
	Database *RustDatabase
}

// PlanRustRunner plans the entrypoint of a Rust engine's runner (D58): a
// crate at ServerDir, a member of the output root's Cargo workspace, whose
// main.rs reads its database's connection, opens the engine on it in the
// background, retrying until the database answers, starts the engine's
// runner as the deployable, holding each engine type, and runs it until
// SIGTERM, as the runner's lock lets it.
func PlanRustRunner(in RustRunnerInput) (*RustServer, error) {
	if in.Engine == nil {
		return nil, fmt.Errorf("stack %s: runner %s runs the engine of %s, which holds no engine type", in.Stack, in.Runner, in.Database)
	}
	n := in.Naming.OrDefault()
	s := &RustServer{
		Stack:         in.Stack,
		Name:          in.Runner,
		Kind:          "runner",
		Package:       n.RustRunnerCrate(in.Stack, in.Runner),
		Binary:        in.Runner,
		Naming:        n,
		RustVersion:   RustVersion,
		EngineProject: in.EngineProject,
	}
	if !rustBinaryName.MatchString(in.Runner) {
		return nil, fmt.Errorf("stack %s: runner %s is a Rust runner, whose binary is its name, and cargo names a binary with letters, digits, '-' and '_' alone", in.Stack, in.Runner)
	}
	taken := rustNames{}
	for _, name := range rustReserved {
		taken[name] = true
	}
	d := &RustDatabase{
		Service: in.Database,
		Var:     taken.take(rustStem(in.Database) + "_database"),
		Field:   in.Field,
	}
	d.From = d.Var
	s.Databases = []*RustDatabase{d}
	s.Runner = &RustRunner{Database: d}
	rin := RustInput{
		Stack:          in.Stack,
		Server:         in.Runner,
		Dir:            in.Dir,
		OutputRoot:     in.OutputRoot,
		Naming:         in.Naming,
		RepositoryRoot: in.RepositoryRoot,
		Paths:          in.Paths,
		EngineTypes:    map[string]string{in.Database: in.TypesDir},
		EngineProject:  in.EngineProject,
	}
	if _, err := s.newRustEngine(rin, d, in.Engine, taken); err != nil {
		return nil, err
	}
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	if err := s.planManifest(rin, dir); err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planRustDocker(rin, s, dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}
