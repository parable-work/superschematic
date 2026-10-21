package servergen

import (
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/release"
)

// EngineFile is the file of an entrypoint module that opens the engine of
// each engine database the entrypoint connects to (D58): openEngine opens
// it on the database in the background, retrying until the database
// answers, so the server starts while it is not up yet, as its pools do;
// a server's deploys the database's engine document before it reports the
// engine ready, and mountEngine mounts its routes on the router of the API
// that serves them, behind the session the identity runtime authenticates
// (D50) and the service caller the API's service authenticator verifies
// (D37); a runner's starts the engine's runner. An entrypoint that opens no
// engine has no such file.
//
// Its package-level names, which Plan hands out to no API, database or
// client, are reserved.
const EngineFile = "engine.go"

// runnerTemplate is the main.go of an engine's runner.
const runnerTemplate = "runner.go.tmpl"

// engineMethods are the methods of the engine's routes, which a site's
// preflight to one asks about: what an API that mounts the engine adds to
// the methods its CORS allows.
var engineMethods = []string{"DELETE", "GET", "PATCH", "POST"}

// reservedMounts are the paths a server answers itself, or its APIs' own
// routes live under, which an engine's mount may not be or lie under.
var reservedMounts = []string{"/api", "/healthz", "/readyz"}

// Engine is the engine an entrypoint opens on one engine database (D58).
type Engine struct {
	// Service is the engine database, a DB service; Var is the variable
	// that holds its *engine.Background.
	Service string
	Var     string

	// Namespace, Mount and Policy are what apigen.EngineInfo holds of it;
	// Policy is the access policy's JSON.
	Namespace string
	Mount     string
	Policy    string

	// Types are the engine types, sorted, which the runner's principal
	// holds as its permissions: a permission covers the actions under it.
	Types []string

	// Database is the database it opens on, the pool's.
	Database *Database

	// API is the API whose router mounts the engine's routes, behind its
	// identity and service callers: the first, by name, that connects to
	// the database; nil for a job's, a worker's or a runner's engine, which
	// serve no request.
	API *API
}

// ReadinessName is how /readyz names the engine: its database's name and
// engine.
func (e *Engine) ReadinessName() string {
	return e.Service + " engine"
}

// PolicyLiteral is the policy as a Go string literal: a raw one, unless
// the policy's JSON holds a backquote.
func (e *Engine) PolicyLiteral() string {
	if strings.Contains(e.Policy, "`") {
		return strconv.Quote(e.Policy)
	}
	return "`" + e.Policy + "`"
}

// planEngines gives each database an API of s connects to whose engine
// database it is an Engine, and each such API the engine its Deps holds,
// the first API on it, by name, mounting its routes on a server. It
// refuses an engine database that is not the API's database, a mount a
// server answers itself or its APIs' routes live under, and two engines on
// one mount.
func (s *Server) planEngines(in Input, apis []*API, taken names) error {
	mounts := map[string]string{}
	for i, a := range in.APIs {
		o, api := a.Output, apis[i]
		if o.Engine == nil {
			continue
		}
		d := api.Database
		if d == nil || d.Service != o.Engine.Database {
			return fmt.Errorf("stack %s: %s %s serves %s, whose engine database %s is not the database its Deps holds", in.Stack, s.Kind, in.Server, o.SchemaName, o.Engine.Database)
		}
		if d.Engine == nil {
			d.Engine = newEngine(d, o.Engine, taken)
			if d.TypesModule == "" {
				d.Types = taken.take(packageName(d.Service) + "types")
				d.TypesModule = in.Naming.OrDefault().GoTypesModule(d.Service)
			}
			s.Engines = append(s.Engines, d.Engine)
		}
		api.Engine = d.Engine
		if s.Background() || d.Engine.API != nil {
			continue
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
		if api.CORS {
			api.CORSMethods = slices.Compact(slices.Sorted(slices.Values(append(api.CORSMethods, engineMethods...))))
		}
	}
	return nil
}

// newEngine is the Engine of database d, whose engine info is info.
func newEngine(d *Database, info *apigen.EngineInfo, taken names) *Engine {
	return &Engine{
		Service:   d.Service,
		Var:       taken.take(varStem(d.Service) + "Engine"),
		Namespace: info.Namespace,
		Mount:     info.Mount,
		Policy:    string(info.Policy),
		Types:     slices.Clone(info.Types),
		Database:  d,
	}
}

// engineDocker is where a checkout's Dockerfile builds the engine's
// archive from (D58): the Cargo workspace beside the Go binding, and the
// crates outside it its manifests reach by path, at their places in the
// checkout, each relative to the build context; and, for an engine whose
// database composes a behavior the core's archive does not run, the
// project's engine crate, whose ffi member it builds in the core's place.
type engineDocker struct {
	// Go is the Go binding, Workspace the Cargo workspace, beside it.
	Go        string
	Workspace string
	// Crates are the paths outside the workspace its crates reach: the
	// work queue's crate, a member of it; the schema IR's and the schema
	// runtime's; the version graph's engine and core; and superscalar's
	// core, with the workspace manifest it takes its lints from.
	Crates []string

	// Project is the project's engine crate, empty for the core's
	// archive; ProjectCrates the extensions' crates it reaches by path,
	// and ProjectArchive the static library its ffi member builds.
	Project        string
	ProjectCrates  []string
	ProjectArchive string
}

// EngineProject is the project's engine crate (D58; projectengine) an
// entrypoint's engine is built from, when its engine database composes a
// behavior the core's engine archive does not run, the work queue's or an
// extension's: <output root>/engine/rust/<db>, which the database's own
// build writes.
type EngineProject struct {
	// Database is the engine database.
	Database string

	// Dir is the crate's directory.
	Dir string

	// Crate is the library's package name, and Ident the name Rust source
	// gives it: a Rust server depends on it and opens the engine with its
	// behaviors().
	Crate string
	Ident string

	// Archive is the file name of the static library the crate's ffi
	// member builds, which a Go server links in the core's place, as
	// libsuperschematic_engine.a.
	Archive string

	// Crates are the directories of the extensions' crates the crate
	// reaches by path, which an image builds with it.
	Crates []string

	// Lockfile is the lock file of the engine's Cargo workspace, which a
	// crate with no lock file of its own starts from: the workspace of the
	// engine's crate a [paths] key names, empty when none does.
	Lockfile string
}

// engineCrates are the paths, relative to the checkout's root, that the
// engine's workspace reaches by path: its runtime/engine/rust is the
// binding's ../rust, so the binding is <root>/runtime/engine/go.
// superscalar's core inherits its lints from its workspace's manifest,
// which names every crate under crates/ a member.
var engineCrates = []string{
	"runtime/engine-workqueue/rust",
	"runtime/schema/rust-ir",
	"runtime/schema/rust",
	"runtime/versiongraph/rust-engine",
	"runtime/versiongraph/rust",
	"third_party/superscalar/Cargo.toml",
	"third_party/superscalar/crates",
}

// planEngineDocker plans the engine stage of a checkout's Dockerfile from
// the engine's Go binding at engineGo, and from the project's engine crate
// when project is set, or says why it cannot: the binding, the checkout it
// lies in, the project's crate and the extensions' crates it reaches must
// be under the build context, root. A TypeScript entrypoint's plans it from
// the engine's npm package, which lies beside the binding (what names it):
// its Go field is then the package.
func planEngineDocker(engineGo string, project *EngineProject, root string, rel func(string) (string, bool), what string) (*engineDocker, string) {
	binding, ok := rel(engineGo)
	if !ok {
		return nil, fmt.Sprintf("%s %s lies outside the repository root %s, the build context", what, engineGo, root)
	}
	d := &engineDocker{Go: binding, Workspace: path.Join(path.Dir(binding), "rust")}
	checkout := filepath.Dir(filepath.Dir(filepath.Dir(engineGo)))
	for _, crate := range engineCrates {
		dir, ok := rel(filepath.Join(checkout, filepath.FromSlash(crate)))
		if !ok {
			return nil, fmt.Sprintf("%s, which the engine's workspace reaches, lies outside the repository root %s, the build context", crate, root)
		}
		d.Crates = append(d.Crates, dir)
	}
	if project == nil {
		return d, ""
	}
	if d.Project, ok = rel(project.Dir); !ok {
		return nil, fmt.Sprintf("the project's engine crate of %s, %s, lies outside the repository root %s, the build context", project.Database, project.Dir, root)
	}
	for _, crate := range project.Crates {
		dir, ok := rel(crate)
		if !ok {
			return nil, fmt.Sprintf("%s, which the project's engine crate of %s reaches, lies outside the repository root %s, the build context", crate, project.Database, root)
		}
		d.ProjectCrates = append(d.ProjectCrates, dir)
	}
	d.ProjectArchive = project.Archive
	return d, ""
}

// RunnerInput is what the entrypoint of an engine's runner is planned from
// (D58).
type RunnerInput struct {
	// Stack is the stack's name; Runner is the runner's deployable.
	Stack  string
	Runner string

	// Dir is the module's directory, ServerDir.
	Dir string

	// Database is the engine database, Field the config field its
	// connection is read from, and Engine what apigen.EngineInfo holds of
	// it, read from the first API on it.
	Database string
	Field    string
	Engine   *apigen.EngineInfo

	// Modules are the modules the runner's go.mod requires beside the
	// third-party ones main.go imports: the database's Go types and the
	// runtime modules.
	Modules []Module

	// Naming, RepositoryRoot, ScalarGo, Release and EngineGo are as an
	// Input's; VersionGraphGo too, when the database declares a version
	// graph its Go types link.
	Naming         naming.Naming
	RepositoryRoot string
	ScalarGo       string
	Release        release.Release
	VersionGraphGo string
	EngineGo       string

	// EngineProject is the project's engine crate of the database, nil
	// when the core's archive runs every behavior it composes: the image
	// builds its archive in the core's place.
	EngineProject *EngineProject

	// CloudSQL is true when some environment of the stack places the
	// database on Cloud SQL: cloudsql.go then serves the engine's socket
	// over the Cloud SQL connector.
	CloudSQL bool
}

// PlanRunner plans the entrypoint of an engine's runner (D58): a module
// at ServerDir whose main.go reads its database's connection, opens the
// engine on it, retrying until the database answers, starts the engine's
// runner and runs it until SIGTERM, as the runner's lock lets it.
func PlanRunner(in RunnerInput) (*Server, error) {
	if in.Engine == nil {
		return nil, fmt.Errorf("stack %s: runner %s runs the engine of %s, which holds no engine type", in.Stack, in.Runner, in.Database)
	}
	s := &Server{
		Stack:       in.Stack,
		Name:        in.Runner,
		Module:      ModulePath(in.Naming, in.Stack, in.Runner),
		Naming:      in.Naming.OrDefault(),
		Kind:        "runner",
		Binary:      "runner",
		GoVersion:   GoVersion,
		RustVersion: RustVersion,
		Runner:      &Runner{},
	}
	taken := newNames()
	d := &Database{
		Service:     in.Database,
		Var:         taken.take(varStem(in.Database)),
		Field:       in.Field,
		Types:       taken.take(packageName(in.Database) + "types"),
		TypesModule: s.Naming.GoTypesModule(in.Database),
	}
	d.Engine = newEngine(d, in.Engine, taken)
	s.Runner.Database = d
	s.Engines = []*Engine{d.Engine}
	if in.CloudSQL {
		s.CloudSQL = []string{in.Database}
	}
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	input := Input{
		Stack:          in.Stack,
		Server:         in.Runner,
		Naming:         in.Naming,
		Modules:        in.Modules,
		RepositoryRoot: in.RepositoryRoot,
		ScalarGo:       in.ScalarGo,
		Release:        in.Release,
		VersionGraphGo: in.VersionGraphGo,
		EngineGo:       in.EngineGo,
		EngineProject:  in.EngineProject,
		opensEngine:    true,
	}
	if err := s.planModule(input, dir); err != nil {
		return nil, err
	}
	if s.EngineLink, err = engineLink(in.EngineProject, dir, projectengine.FFIDir); err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planDocker(input, dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// Runner is what an engine's runner's entrypoint runs (D58): the engine
// of its database.
type Runner struct {
	Database *Database
}

// RunnerSubject is the subject the engine's runner acts as, its
// deployable's name: what its reactions' and Timers' events record as
// their actor, and what a rules policy names it by.
func (s *Server) RunnerSubject() string {
	return s.Name
}

// writeEngine writes engine.go into dir when the entrypoint opens an
// engine.
func writeEngine(s *Server, dir string) error {
	if len(s.Engines) == 0 {
		return nil
	}
	if err := render("engine.go.tmpl", filepath.Join(dir, EngineFile), s); err != nil {
		return fmt.Errorf("servergen: %s %s: %w", s.Kind, s.Name, err)
	}
	return nil
}

// engineLink is the projectengine.Link of an entrypoint at dir whose engine
// is built from project, the project's engine crate, nil when project is
// nil: member is the crate's member the entrypoint takes, and the crate
// starts from the engine workspace's lock file the project names.
func engineLink(project *EngineProject, dir, member string) (*projectengine.Link, error) {
	if project == nil {
		return nil, nil
	}
	l, err := projectengine.LinkOf(dir, project.Database, project.Dir, member, project.Lockfile)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// EngineLockfile is the lock file of the engine's Cargo workspace the
// naming file's [paths] keys locate, empty when none does: the workspace
// holding the engine's crate (engine_rust), or the one beside its Go
// binding or npm package (engine_go, engine_typescript), runtime/engine/rust.
func EngineLockfile(paths naming.LocalPaths) string {
	switch {
	case paths.EngineRust != "":
		return filepath.Join(filepath.Dir(paths.EngineRust), "Cargo.lock")
	case paths.EngineGo != "":
		return filepath.Join(filepath.Dir(paths.EngineGo), "rust", "Cargo.lock")
	case paths.EngineTypeScript != "":
		return filepath.Join(filepath.Dir(paths.EngineTypeScript), "rust", "Cargo.lock")
	}
	return ""
}
