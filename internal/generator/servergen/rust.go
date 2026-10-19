package servergen

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The entrypoint of a Rust server (D57; docs/stack-model.md, sections 8.1
// and 8.6): a crate at `<output-root>/server/<stack>/<server>/` holding
// Cargo.toml and src/main.rs, a member of the output root's Cargo workspace
// (workspace.go), and a Dockerfile. main.rs does what the Go and
// TypeScript entrypoints do, on a tokio runtime: it loads each API's
// EnvConfig, opens one Postgres pool per database, builds one SDK client
// per API called with its credential source, calls each API's `create`
// with its Deps, builds each API's identity service over its authDb's pool
// and its service authenticator from its callers field, joins them with
// the handlers through `Implementations::new`, and serves every API's
// router beside /healthz and /readyz, under the site CORS of the APIs a
// site calls. It logs JSON lines through the HTTP runtime's logging.
//
// A Rust job's and a Rust worker's entrypoint (D65; sections 8.7 and 8.8)
// is a crate beside the servers', `<output-root>/server/<stack>/<job>/`, of
// the same files and a member of the same workspace. Its main.rs builds its
// one API's Deps as a server's does, from its EnvConfig, a pool per
// database and a client per API called, and neither builds an identity
// service nor a router (D52, D53): a job's (job.rs.tmpl) calls the
// implementation's `create_jobs`, then the job's method once with a
// Shutdown that SIGTERM cancels, and exits 1 when it fails; a worker's
// (worker.rs.tmpl) calls `create_workers` and runs the HTTP runtime's claim
// loop over its queue, by the descriptor its database's Rust types carry.

// The files of a Rust entrypoint crate.
const (
	RustManifestFile = "Cargo.toml"
	RustMainFile     = "src/main.rs"
)

// The versions of the crates main.rs names beside the generated ones,
// which the generated API crates and the HTTP runtime pin, so the
// workspace's lockfile holds one of each: axum and tokio as the API
// crates take them, serde_json for the bodies of /healthz and /readyz, and
// tower, whose oneshot hands a request to the router of the API that
// takes it on a server that serves more than one.
const (
	rustAxumVersion      = "0.8.9"
	rustTokioVersion     = "1.52.2"
	rustSerdeJSONVersion = "1.0.149"
	rustTowerVersion     = "0.5"
)

// RustImplementation is where an API's Rust implementation crate lives
// (docs/stack-model.md, section 8.6).
type RustImplementation struct {
	// Dir is the crate's directory, at the naming file's
	// [implementation_paths] rust template.
	Dir string

	// Crate is its package name and Lib the name Rust source gives its
	// library: its Cargo.toml's, or the scaffold's when the scaffold
	// writes it (ReadRustImplementation).
	Crate string
	Lib   string

	// Manifest is its Cargo.toml, nil when the scaffold writes it, which
	// the plan of its mirror checks (D65; mirror.go).
	Manifest []byte
}

// RustAPIInput is one API a Rust server serves.
type RustAPIInput struct {
	// Output is the API's Rust crate, with its Deps.
	Output *rustrestgen.APIOutput

	// Routes is the API's server output, whose routes the build checks
	// against the other APIs' (checkRoutes).
	Routes *apigen.APIOutput

	// Config is the API's EnvConfig: its derived fields, its callers, CORS
	// and identity config fields. Nil when the API has none.
	Config *envgen.ConfigOutput

	// Dir is the API crate's directory, under the output root.
	Dir string

	// Implementation is where its implementation lives.
	Implementation RustImplementation

	// CORS is true when a site of the stack calls the API: its server then
	// answers CORS for the origins its CORS field lists (D55).
	CORS bool
}

// RustInput is what a Rust server's entrypoint is planned from.
type RustInput struct {
	// Stack is the stack's name; Server the server's name in the stack.
	Stack  string
	Server string

	// Dir is the crate's directory, ServerDir.
	Dir string

	// OutputRoot is the root of the Cargo workspace the crate joins.
	OutputRoot string

	// APIs are the APIs the server serves, sorted by service.
	APIs []RustAPIInput

	// Naming names the crate and the generated crates main.rs depends on.
	Naming naming.Naming

	// RepositoryRoot is the Dockerfile's build context: the repository
	// root, or the naming file's [paths] build_context. Every directory the
	// image reads must lie under it, or no Dockerfile is written.
	RepositoryRoot string

	// ImplementationRoot is the root the naming file's
	// [implementation_paths] rust template is relative to: the repository
	// root, the parent of the schemas root.
	ImplementationRoot string

	// SDKDirs are the directories of the Rust SDK of each API the served
	// APIs call, by service.
	SDKDirs map[string]string

	// Paths are the naming file's [paths]: the crate depends on the HTTP
	// runtime's checkout by path, and the image builds the checkouts every
	// generated crate depends on.
	Paths naming.LocalPaths

	// Job, when set, plans the entrypoint of a job of the one API in APIs
	// rather than a server's (D65): Server is then the job's deployable.
	Job *JobInput

	// Worker, when set, plans the entrypoint of a worker of the one API in
	// APIs (D65): Server is then the worker's deployable.
	Worker *WorkerInput

	// EngineTypes are the directories of the Rust types crate of each
	// engine database an API connects to, which holds its engine document
	// (D58), by service.
	EngineTypes map[string]string

	// EngineProject is the project's engine crate the entrypoint's engine
	// runs the behaviors of, nil when the core's behaviors are every
	// behavior its engine databases compose (D58).
	EngineProject *EngineProject
}

// RustServer is a planned Rust entrypoint, what the templates read: a
// server's, a job's when Job is set, or a worker's when Worker is.
type RustServer struct {
	Stack string
	Name  string

	// Kind is "server", "job", "worker" or "runner".
	Kind string

	// Job is what a job's entrypoint runs, nil for a server and a worker
	// (D65).
	Job *Job

	// Worker is what a worker's entrypoint runs, nil for a server and a
	// job (D65).
	Worker *RustWorker

	// Runner is what an engine's runner's entrypoint runs, nil for every
	// other (D58).
	Runner *RustRunner

	// Engines are the engines the entrypoint opens, one per engine
	// database it connects to (D58), and EngineProject the project's engine
	// crate they run the behaviors of, nil for the core's.
	Engines       []*RustEngine
	EngineProject *EngineProject

	// Package is the crate's name, and Binary the name of its one binary,
	// the server's: the local target runs it from the Cargo target
	// directory, and the image as /<Binary>.
	Package string
	Binary  string

	Naming naming.Naming

	// RustVersion is the toolchain pin: the edition's rust-version and the
	// Rust image the Dockerfile builds in.
	RustVersion string

	APIs      []*RustAPI
	Databases []*RustDatabase
	Clients   []*RustClient

	// Dependencies are Cargo.toml's, the registry crates first, then the
	// HTTP runtime and the crates of the workspace, each in name order.
	Dependencies []CargoDependency

	// Mirrors are the mirrors of the served APIs' implementations under
	// the output root, which the crate depends on in their place (D65;
	// mirror.go), and which the build writes beside it.
	Mirrors []*RustMirror

	// Docker is the Dockerfile's plan, nil when none is written, and
	// NoDocker says why.
	Docker   *RustDocker
	NoDocker string
}

// CargoDependency is a Cargo.toml dependency line: the crate's name and
// its table's inside, `version = ...` or `path = ...` with its features.
type CargoDependency struct {
	Name string
	Spec string
}

// RustWorker is the worker a Rust worker's entrypoint runs (D65): its
// @worker class, the method of the API's Workers that handles a message,
// the queue it claims from, a type of the database's Rust types, which the
// API crate re-exports as database_types, and QueueConst, the constant
// that holds its descriptor there; Database, the pool of the API's
// database the queue claims through; and its concurrency unless the
// environment sets one, and its grace.
type RustWorker struct {
	Name         string
	Method       string
	Queue        string
	QueueConst   string
	Database     *RustDatabase
	Concurrency  int
	GraceSeconds int

	// Message and Descriptor are the paths main.rs names the queue's
	// message type and its descriptor constant by, through the API crate.
	Message    string
	Descriptor string
}

// RustAPI is a served API in main.rs.
type RustAPI struct {
	Service string

	// Crate is the API crate's name and Alias the name main.rs uses it as;
	// ImplCrate, ImplAlias and ImplLib are its implementation's, and
	// ImplDir the implementation's directory from the repository root.
	Crate     string
	Alias     string
	ImplCrate string
	ImplLib   string
	ImplAlias string
	ImplDir   string

	// Config is the variable of its EnvConfig, empty when it has none, and
	// ConfigLoader the function of the API crate's config module that
	// loads it: load_env_config, or for a job or a worker of an API with a
	// callers field load_background_env_config, since a stack sets no
	// callers field for either (D65). Deps, Handlers, Implementations and
	// Router are the variables of the rest.
	Config          string
	ConfigLoader    string
	Deps            string
	Handlers        string
	Implementations string
	Router          string

	// Identity is the variable of its identity service when it
	// authenticates with the identity runtime over the user model (D50),
	// empty otherwise, and IdentityField the EnvConfig field its config
	// is. Authenticator is the variable of the identity runtime's
	// authenticator over it when the API mounts its engine's routes, which
	// share the service (D58).
	Identity      string
	IdentityField string
	Authenticator string

	// ServiceAuthenticator is the variable of its service authenticator,
	// and Callers the EnvConfig field it verifies callers against, when an
	// operation has a service clause (D37).
	ServiceAuthenticator string
	Callers              string

	// CORS is the EnvConfig field of the origins a site of the stack calls
	// it from, when one does (D55); empty otherwise.
	CORS string

	// Webhooks is true when an operation is @hmacVerified: main.rs checks
	// the implementation's verifiers before build_router would panic.
	Webhooks bool

	// Database is the pool Deps.db holds, nil without one.
	Database *RustDatabase

	// Engine is the engine Deps.engine holds, of the API's engine database
	// (D58), nil without one.
	Engine *RustEngine

	// Calls are Deps' clients.
	Calls []RustCall
}

// MountsEngine reports whether the API's server mounts the routes of its
// engine (D58): it shares its identity service and its service
// authenticator with them.
func (a *RustAPI) MountsEngine() bool {
	return a.Engine != nil && a.Engine.API == a
}

// RustCall is a client in an API's Deps.
type RustCall struct {
	Field  string
	Client *RustClient
}

// RustDatabase is a database the server opens one pool to, for every API
// on it.
type RustDatabase struct {
	Service string
	Var     string

	// Field is the derived config field the connection is read from, and
	// From the expression that reads it from the first API's config.
	Field string
	From  string

	// Store is the variable of the identity store over the pool when an
	// API on the database authenticates with the identity runtime (D50),
	// empty otherwise; StoreAPI is the alias of the API crate whose
	// identity::store builds it, the first such API's. The store reads the
	// database's descriptor, which every API over it holds alike.
	Store    string
	StoreAPI string
}

// RustClient is an SDK client of an API called, built once for every API
// that calls it, by the function Constructor names.
type RustClient struct {
	Service     string
	Var         string
	Constructor string

	// Crate is the SDK crate's name, Alias the name main.rs uses it as and
	// Client its struct.
	Crate  string
	Alias  string
	Client string

	// From is the expression that reads the callee's endpoint from the
	// first calling API's config.
	From string
}

// RustDocker is a Rust server's Dockerfile plan. Paths are relative to the
// build context, slash-separated.
type RustDocker struct {
	// Dockerfile is the Dockerfile's path, Context the crate's directory
	// and Workspace the output root, the Cargo workspace's root.
	Dockerfile string
	Context    string
	Workspace  string

	// Include are the paths the build context holds: the workspace's root
	// and lockfile, and every crate a member of the workspace reaches by
	// path, which Cargo reads to check the lockfile, whichever stack's
	// server the member is: the generated crates and the implementations'
	// mirrors under the output root, and the runtimes' checkouts.
	Include []string
}

// rustBinaryName is the shape of a binary's name cargo builds, which the
// local target holds a Rust server's crate to too.
var rustBinaryName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// rustReserved are the names main.rs declares or uses beside the served
// APIs' own: an API, database or client never takes one. The crates main.rs
// names by their own names are among them.
var rustReserved = []string{
	"axum", "cors", "logging", "postgres", "stackconfig", "tracing", "serde_json", "tokio", "tower", "std",
	"superschematic_http_runtime", "main", "run", "serve", "configure", "connect", "listen_port",
	"healthz", "readyz", "dispatch", "port", "app", "readiness", "dependencies", "signals", "draining",
	"api", "apis", "request", "router", "endpoint", "credential", "service_credential", "err",
	"jobs", "workers", "worker", "queue", "shutdown", "started", "definition", "concurrency", "options",
	"handle", "message",
}

// rustNames hands out snake_case identifiers no other declaration of
// main.rs takes.
type rustNames map[string]bool

func (n rustNames) take(want string) string {
	name := want
	for i := 2; n[name] || rustutil.IsRustKeyword(name); i++ {
		name = fmt.Sprintf("%s_%d", want, i)
	}
	n[name] = true
	return name
}

// rustStem is a service's name as the stem of a Rust variable: shop-orders
// is shop_orders.
func rustStem(service string) string {
	return rustutil.Identifier(service, "service")
}

// crateIdent is a crate's name as Rust source spells it.
func crateIdent(crate string) string {
	return strings.ReplaceAll(crate, "-", "_")
}

// PlanRust plans the entrypoint of one Rust server. It refuses a server
// whose APIs register one method and path between them, and an API whose
// config holds no field for a database, a callee or a clause its
// entrypoint needs. With in.Job it plans a job's entrypoint instead, and
// with in.Worker a worker's (D65): one API, whose Deps it builds as a
// server does, and no routes, identity service or service authenticator.
func PlanRust(in RustInput) (*RustServer, error) {
	n := in.Naming.OrDefault()
	s := &RustServer{
		Stack:       in.Stack,
		Name:        in.Server,
		Kind:        "server",
		Package:     n.RustServerCrate(in.Stack, in.Server),
		Binary:      in.Server,
		Naming:      n,
		RustVersion: RustVersion,
		// The engine's behaviors are the project's when its crate holds
		// them (D58).
		EngineProject: in.EngineProject,
	}
	switch {
	case in.Job != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: job %s runs a job of %d APIs; a job belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		i := slices.IndexFunc(o.Jobs, func(j rustrestgen.JobInfo) bool { return j.Name == in.Job.Name })
		if i < 0 {
			return nil, fmt.Errorf("stack %s: job %s runs %s of %s, which declares no such job", in.Stack, in.Server, in.Job.Name, o.SchemaName)
		}
		s.Kind = "job"
		s.Job = &Job{Name: o.Jobs[i].Name, Method: o.Jobs[i].Method}
		s.Package = n.RustJobCrate(in.Stack, in.Server)
	case in.Worker != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: worker %s runs a worker of %d APIs; a worker belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		i := slices.IndexFunc(o.Workers, func(w rustrestgen.WorkerInfo) bool { return w.Name == in.Worker.Name })
		if i < 0 {
			return nil, fmt.Errorf("stack %s: worker %s runs %s of %s, which declares no such worker", in.Stack, in.Server, in.Worker.Name, o.SchemaName)
		}
		w := o.Workers[i]
		if o.Deps == nil || o.Deps.Database == "" {
			return nil, fmt.Errorf("stack %s: worker %s of %s claims queue %s, and %s connects to no database", in.Stack, in.Server, o.SchemaName, w.Queue, o.SchemaName)
		}
		if o.Deps.DatabaseTypesCrate == "" {
			return nil, fmt.Errorf("stack %s: worker %s of %s claims queue %s of %s, whose Rust types the API crate does not re-export as %s", in.Stack, in.Server, o.SchemaName, w.Queue, o.Deps.Database, rustrestgen.DatabaseTypesModule)
		}
		grace := in.Worker.GraceSeconds
		if grace <= 0 {
			grace = ir.DefaultWorkerGraceSeconds
		}
		s.Kind = "worker"
		s.Worker = &RustWorker{
			Name:         w.Name,
			Method:       w.Method,
			Queue:        w.Queue,
			QueueConst:   w.QueueConst,
			Concurrency:  max(w.Concurrency, 1),
			GraceSeconds: grace,
		}
		s.Package = n.RustWorkerCrate(in.Stack, in.Server)
	case len(in.APIs) == 0:
		return nil, fmt.Errorf("stack %s: server %s serves no API", in.Stack, in.Server)
	default:
		routes := Input{Stack: in.Stack, Server: in.Server}
		for _, a := range in.APIs {
			routes.APIs = append(routes.APIs, APIInput{Output: a.Routes})
		}
		if err := checkRoutes(routes); err != nil {
			return nil, err
		}
	}
	if !rustBinaryName.MatchString(in.Server) {
		return nil, fmt.Errorf("stack %s: %s %s is a Rust %s, whose binary is its name, and cargo names a binary with letters, digits, '-' and '_' alone; rename the %s", in.Stack, s.Kind, in.Server, s.Kind, s.Kind)
	}
	// A job or a worker serves no request: it answers no CORS, establishes
	// no end user, and verifies no caller (D52, D53).
	background := s.Background()
	taken := rustNames{}
	for _, name := range rustReserved {
		taken[name] = true
	}
	databases := map[string]*RustDatabase{}
	clients := map[string]*RustClient{}
	for _, a := range in.APIs {
		o := a.Output
		stem := rustStem(o.SchemaName)
		api := &RustAPI{
			Service:         o.SchemaName,
			Crate:           o.CrateName,
			Alias:           taken.take(stem + "_api"),
			ImplCrate:       a.Implementation.Crate,
			ImplLib:         a.Implementation.Lib,
			ImplAlias:       taken.take(stem + "_impl"),
			Deps:            taken.take(stem + "_deps"),
			Handlers:        taken.take(stem + "_handlers"),
			Implementations: taken.take(stem + "_implementations"),
			Router:          taken.take(stem + "_router"),
			Webhooks:        len(o.WebhookProviders) > 0 && !background,
			ConfigLoader:    "load_env_config",
		}
		if in.ImplementationRoot != "" {
			if rel, err := filepath.Rel(in.ImplementationRoot, a.Implementation.Dir); err == nil {
				api.ImplDir = filepath.ToSlash(rel)
			}
		}
		derived := map[string]string{}
		if a.Config != nil && o.HasEnvConfig {
			api.Config = taken.take(stem + "_config")
			for _, f := range a.Config.Derived {
				derived[string(f.Kind)+" "+f.Service] = f.Key
			}
			switch {
			case background && a.Config.CallersField != "":
				api.ConfigLoader = "load_background_env_config"
			case a.Config.CallersField != "":
				api.Callers = envgen.RustFieldName(a.Config.CallersField)
			}
			if a.CORS && a.Config.CORSField != "" && !background {
				api.CORS = envgen.RustFieldName(a.Config.CORSField)
			}
			if a.Config.IdentityField != "" && !background {
				api.IdentityField = envgen.RustFieldName(a.Config.IdentityField)
			}
		}
		if o.HasServiceCallers && !background {
			if api.Callers == "" {
				return nil, fmt.Errorf("stack %s: server %s serves %s, whose operations have a service clause, and whose config has no callers field", in.Stack, in.Server, o.SchemaName)
			}
			api.ServiceAuthenticator = taken.take(stem + "_service_authenticator")
		}
		if a.CORS && api.CORS == "" && !background {
			return nil, fmt.Errorf("stack %s: server %s serves %s, which a site calls, and whose config has no CORS field", in.Stack, in.Server, o.SchemaName)
		}
		if o.Deps == nil {
			return nil, fmt.Errorf("stack %s: server %s serves %s, whose crate has no Deps", in.Stack, in.Server, o.SchemaName)
		}
		if db := o.Deps.Database; db != "" {
			d := databases[db]
			if d == nil {
				key, ok := derived[string(ir.EdgeSQL)+" "+db]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for its database %s", in.Stack, s.Kind, in.Server, o.SchemaName, db)
				}
				d = &RustDatabase{
					Service: db,
					Var:     taken.take(rustStem(db) + "_pool"),
					Field:   key,
					From:    api.Config + "." + envgen.RustFieldName(key),
				}
				databases[db] = d
				s.Databases = append(s.Databases, d)
			}
			api.Database = d
		}
		if o.Identity != nil && !background {
			d := api.Database
			if d == nil || d.Service != o.Identity.AuthDB {
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime over %s, its authDb, and opens no pool on it to build its identity store over", in.Stack, in.Server, o.SchemaName, o.Identity.AuthDB)
			}
			if api.IdentityField == "" {
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime, and whose config has no identity config field", in.Stack, in.Server, o.SchemaName)
			}
			if d.Store == "" {
				d.Store = taken.take(rustStem(d.Service) + "_identity_store")
				d.StoreAPI = api.Alias
			}
			api.Identity = taken.take(stem + "_identity")
		}
		for _, call := range o.Deps.Calls {
			c := clients[call.Service]
			if c == nil {
				key, ok := derived[string(ir.EdgeHTTP)+" "+call.Service]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: %s %s serves %s, whose config has no field for %s, which it calls", in.Stack, s.Kind, in.Server, o.SchemaName, call.Service)
				}
				callee := rustStem(call.Service)
				c = &RustClient{
					Service:     call.Service,
					Var:         taken.take(callee + "_client"),
					Constructor: taken.take("new_" + callee + "_client"),
					Crate:       call.Crate,
					Alias:       taken.take(callee + "_sdk"),
					Client:      call.Client,
					From:        api.Config + "." + envgen.RustFieldName(key),
				}
				clients[call.Service] = c
				s.Clients = append(s.Clients, c)
			}
			api.Calls = append(api.Calls, RustCall{Field: call.Field, Client: c})
		}
		if s.Worker != nil {
			if api.Database == nil {
				return nil, fmt.Errorf("stack %s: worker %s of %s opens no pool on %s, whose queue it claims", in.Stack, in.Server, o.SchemaName, o.Deps.Database)
			}
			s.Worker.Database = api.Database
			types := api.Alias + "::" + rustrestgen.DatabaseTypesModule + "::"
			s.Worker.Message, s.Worker.Descriptor = types+s.Worker.Queue, types+s.Worker.QueueConst
		}
		s.APIs = append(s.APIs, api)
	}
	if err := s.planRustEngines(in, taken); err != nil {
		return nil, err
	}
	for _, a := range in.APIs {
		if a.Implementation.Dir == "" {
			continue
		}
		m, err := planRustMirror(in.OutputRoot, a)
		if err != nil {
			return nil, fmt.Errorf("stack %s: %s %s: %w", in.Stack, s.Kind, in.Server, err)
		}
		s.Mirrors = append(s.Mirrors, m)
	}
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	if err := s.planManifest(in, dir); err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planRustDocker(in, s, dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// planManifest plans Cargo.toml's dependencies: axum, serde_json and tokio
// with the features main.rs uses, tower for a server of more than one API,
// the HTTP runtime from its checkout with the features main.rs and the
// served APIs use, each API crate, its implementation's mirror under the
// output root (D65), and each callee's SDK, by paths relative to the
// crate. A job's or a worker's main.rs serves nothing, and takes tokio
// alone of the registry's crates, for its runtime.
func (s *RustServer) planManifest(in RustInput, dir string) error {
	if in.Paths.HTTPRuntimeRust == "" {
		return fmt.Errorf("stack %s: %s %s is a Rust %s, whose crate depends on the HTTP runtime's checkout, and the naming file's [paths] http_runtime_rust is unset", in.Stack, s.Kind, in.Server, s.Kind)
	}
	registry := []CargoDependency{
		{"axum", quoteToml(rustAxumVersion)},
		{"serde_json", quoteToml(rustSerdeJSONVersion)},
		{"tokio", fmt.Sprintf(`{ version = %s, features = ["macros", "net", "rt-multi-thread", "signal", "sync", "time"] }`, quoteToml(rustTokioVersion))},
	}
	if s.Background() {
		registry = []CargoDependency{{"tokio", fmt.Sprintf(`{ version = %s, features = ["macros", "rt-multi-thread"] }`, quoteToml(rustTokioVersion))}}
		if len(s.Engines) > 0 {
			// engine.rs reads the engine document's JSON.
			registry = append([]CargoDependency{{"serde_json", quoteToml(rustSerdeJSONVersion)}}, registry...)
		}
	}
	if len(s.APIs) > 1 {
		registry = append(registry, CargoDependency{"tower", fmt.Sprintf(`{ version = %s, features = ["util"] }`, quoteToml(rustTowerVersion))})
	}
	features := map[string]bool{"logging": true}
	if len(s.Databases) > 0 {
		features["postgres"] = true
	}
	if len(s.Clients) > 0 {
		// A callee's credential may be a Google ID token, which the
		// runtime asks the metadata server for.
		features["http-client"] = true
	}
	for _, a := range in.APIs {
		for _, f := range a.Output.RuntimeFeatures() {
			features[f] = true
		}
	}
	rel := func(target string) (string, error) {
		r, err := naming.RelPath(dir, target)
		if err != nil {
			return "", fmt.Errorf("stack %s: %s %s: %w", in.Stack, s.Kind, in.Server, err)
		}
		return r, nil
	}
	pathDep := func(target string, features ...string) (string, error) {
		r, err := rel(target)
		if err != nil {
			return "", err
		}
		spec := "{ path = " + quoteToml(r)
		if len(features) > 0 {
			quoted := make([]string, len(features))
			for i, f := range features {
				quoted[i] = quoteToml(f)
			}
			spec += ", features = [" + strings.Join(quoted, ", ") + "]"
		}
		return spec + " }", nil
	}
	runtime, err := pathDep(in.Paths.HTTPRuntimeRust, slices.Sorted(maps.Keys(features))...)
	if err != nil {
		return err
	}
	local := map[string]string{s.Naming.HTTPRuntimeRustCrate: runtime}
	engines, err := s.engineDependencies(in, pathDep)
	if err != nil {
		return err
	}
	for crate, spec := range engines {
		local[crate] = spec
	}
	for _, a := range in.APIs {
		if local[a.Output.CrateName], err = pathDep(a.Dir); err != nil {
			return err
		}
		if local[a.Implementation.Crate], err = pathDep(RustMirrorDir(in.OutputRoot, a.Output.SchemaName)); err != nil {
			return err
		}
	}
	for _, c := range s.Clients {
		sdk, ok := in.SDKDirs[c.Service]
		if !ok {
			return fmt.Errorf("stack %s: %s %s: no directory for the Rust SDK of %s", in.Stack, s.Kind, in.Server, c.Service)
		}
		if local[c.Crate], err = pathDep(sdk); err != nil {
			return err
		}
	}
	s.Dependencies = registry
	for _, name := range slices.Sorted(maps.Keys(local)) {
		s.Dependencies = append(s.Dependencies, CargoDependency{name, local[name]})
	}
	return nil
}

// quoteToml is s as a TOML basic string.
func quoteToml(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// planRustDocker plans the Dockerfile, or says why there is none. The image
// builds the server with `cargo build --release --locked` in the output
// root's workspace, which reads every member's manifest and every crate a
// member reaches by path to check the lockfile: the generated crates and
// the implementations' mirrors under the output root (D65), and the
// checkouts of the HTTP runtime, the schema runtime, superscalar and,
// where the naming file names it, the version graph's engine. Every one
// must lie under the build context.
func planRustDocker(in RustInput, s *RustServer, dir string) (*RustDocker, string, error) {
	if in.RepositoryRoot == "" {
		return nil, "no repository root to take as the build context", nil
	}
	for _, p := range []struct{ key, dir string }{
		{"scalar_rust", in.Paths.ScalarRust},
		{"schema_runtime_rust", in.Paths.SchemaRuntimeRust},
	} {
		if p.dir == "" {
			return nil, fmt.Sprintf("the naming file's [paths] %s is unset, and the image builds the generated crates, which depend on its checkout by path; set it", p.key), nil
		}
	}
	root, err := filepath.Abs(in.RepositoryRoot)
	if err != nil {
		return nil, "", err
	}
	rel := func(p string) (string, bool) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", false
		}
		r, err := filepath.Rel(root, abs)
		if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return "", false
		}
		return filepath.ToSlash(r), true
	}
	outside := func(what, p string) string {
		return fmt.Sprintf("%s %s lies outside the repository root %s, the build context", what, p, root)
	}
	d := &RustDocker{}
	var ok bool
	if d.Workspace, ok = rel(in.OutputRoot); !ok {
		return nil, outside("the output root", in.OutputRoot), nil
	}
	if d.Context, ok = rel(dir); !ok {
		return nil, outside("the "+s.Kind+"'s crate", dir), nil
	}
	d.Dockerfile = path.Join(d.Context, DockerFile)
	// Every crate the workspace's members reach by path, by where the
	// build lays it out, so the context does not depend on which stack's
	// servers were built: a member's manifest and its main.rs, each Rust
	// API crate's manifest, sources and the OpenAPI document its source
	// embeds, the Rust SDKs and types, and every implementation's mirror
	// whole, which holds the implementation's files alone. No
	// implementation's own directory goes in.
	d.Include = []string{
		d.Workspace + "/" + RustManifestFile,
		d.Workspace + "/" + RustLockfileName + "*",
		d.Workspace + "/server/*/*/" + RustManifestFile,
		d.Workspace + "/server/*/*/src",
		d.Workspace + "/api/*/" + RustManifestFile,
		d.Workspace + "/api/*/src",
		d.Workspace + "/api/*/openapi.json",
		d.Workspace + "/" + RustMirrorRoot,
		d.Workspace + "/sdk/rust",
		d.Workspace + "/types/rust",
		d.Context,
	}
	for _, p := range []struct{ what, dir string }{
		{"the HTTP runtime's Rust crate", in.Paths.HTTPRuntimeRust},
		{"the schema runtime's Rust crate", in.Paths.SchemaRuntimeRust},
		{"superscalar's Rust crate", in.Paths.ScalarRust},
		{"the version graph's Rust engine", in.Paths.VersionGraphRust},
	} {
		if p.dir == "" {
			continue
		}
		r, ok := rel(p.dir)
		if !ok {
			return nil, outside(p.what, p.dir), nil
		}
		d.Include = append(d.Include, r)
	}
	// superscalar's crate inherits its lints from its workspace's root,
	// which Cargo reads when it loads the crate.
	scalar, _ := rel(in.Paths.ScalarRust)
	if path.Base(path.Dir(scalar)) == "crates" {
		d.Include = append(d.Include, path.Dir(path.Dir(scalar))+"/"+RustManifestFile)
	}
	engines, why := engineDockerIncludes(in, s, rel)
	if why != "" {
		return nil, why, nil
	}
	d.Include = append(d.Include, engines...)
	slices.Sort(d.Include)
	d.Include = slices.Compact(d.Include)
	return d, "", nil
}

// ReadRustImplementation reads the names of the Rust implementation crate
// of service at dir: its Cargo.toml's package name and library name, or
// the scaffold's when dir holds no Cargo.toml yet, and the Cargo.toml
// itself. The server's Cargo.toml depends on the implementation's mirror
// by that name, so a Cargo.toml without one fails.
func ReadRustImplementation(n naming.Naming, service, dir string) (RustImplementation, error) {
	impl := RustImplementation{Dir: dir}
	var manifest struct {
		Package struct {
			Name string `toml:"name"`
		} `toml:"package"`
		Lib struct {
			Name string `toml:"name"`
		} `toml:"lib"`
	}
	file := filepath.Join(dir, RustManifestFile)
	data, err := os.ReadFile(file)
	if err == nil {
		impl.Manifest = data
		_, err = toml.Decode(string(data), &manifest)
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		impl.Crate = n.OrDefault().RustImplementationCrate(service)
	case err != nil:
		return impl, fmt.Errorf("the implementation of %s: %s: %w", service, file, err)
	case manifest.Package.Name == "":
		return impl, fmt.Errorf("the implementation of %s, at %s, has a Cargo.toml with no [package] name, and the server's Cargo.toml depends on it by name; name it, as the scaffold names it %s", service, dir, n.OrDefault().RustImplementationCrate(service))
	default:
		impl.Crate = manifest.Package.Name
	}
	impl.Lib = crateIdent(impl.Crate)
	if manifest.Lib.Name != "" {
		impl.Lib = manifest.Lib.Name
	}
	return impl, nil
}

// Background reports whether the entrypoint is a job's, a worker's or an
// engine runner's, which serves no request: it mounts no routes, listens on
// no port, and verifies no end user or caller.
func (s *RustServer) Background() bool {
	return s.Job != nil || s.Worker != nil || s.Runner != nil
}

// Sections are the sections of docs/stack-model.md the entrypoint's
// comments cite: the Rust servers' and the jobs' or the workers'.
func (s *RustServer) Sections() string {
	switch {
	case s.Job != nil:
		return "sections 8.6 and 8.7"
	case s.Worker != nil:
		return "sections 8.6 and 8.8"
	case s.Runner != nil:
		return "sections 3.1 and 8.6"
	}
	return "sections 8.1 and 8.6"
}

// AnyIdentity reports whether an API of the server authenticates with the
// identity runtime.
func (s *RustServer) AnyIdentity() bool {
	return slices.ContainsFunc(s.APIs, func(a *RustAPI) bool { return a.Identity != "" })
}

// AnyServiceAuth reports whether an API of the server verifies its
// callers' service credentials.
func (s *RustServer) AnyServiceAuth() bool {
	return slices.ContainsFunc(s.APIs, func(a *RustAPI) bool { return a.ServiceAuthenticator != "" })
}

// AnyCORS reports whether a site of the stack calls an API the server
// serves, which then answers CORS (D55).
func (s *RustServer) AnyCORS() bool {
	return slices.ContainsFunc(s.APIs, func(a *RustAPI) bool { return a.CORS != "" })
}

// Dispatches reports whether the server serves more than one API, whose
// routers it dispatches between.
func (s *RustServer) Dispatches() bool {
	return len(s.APIs) > 1
}

// RuntimeUses are the use declarations of main.rs from the HTTP runtime.
func (s *RustServer) RuntimeUses() []string {
	runtime := crateIdent(s.Naming.HTTPRuntimeRustCrate)
	if s.Background() {
		return s.backgroundUses(runtime)
	}
	var uses []string
	if s.AnyIdentity() {
		uses = append(uses, rustutil.UseLine(runtime+"::identity", []string{"PostgresPool", "Store"}))
	}
	uses = append(uses, rustutil.UseLine(runtime+"::logging", []string{"self", "tracing"}))
	if len(s.Databases) > 0 {
		uses = append(uses, rustutil.UseLine(runtime+"::postgres", []string{"self", "Pool"}))
	}
	var stackconfig []string
	if len(s.Clients) > 0 {
		stackconfig = append(stackconfig, "self", "Service")
	}
	if len(s.Databases) > 0 {
		stackconfig = append(stackconfig, "Database")
	}
	if len(stackconfig) > 0 {
		uses = append(uses, rustutil.UseLine(runtime+"::stackconfig", stackconfig))
	}
	var root []string
	if s.AnyCORS() || s.Dispatches() {
		root = append(root, "cors")
	}
	if s.AnyIdentity() {
		root = append(root, "IdentityAuthenticator")
	}
	if s.AnyServiceAuth() {
		root = append(root, "JwtServiceAuthenticator")
	}
	if s.Dispatches() {
		root = append(root, "OperationInfo")
	}
	if len(root) > 0 {
		uses = append(uses, rustutil.UseLine(runtime, root))
	}
	return uses
}

// backgroundUses are the use declarations of a job's or a worker's main.rs
// from the HTTP runtime, in the order rustfmt sorts them: its log, its
// pool and the readers of its fields, the queue and the claim loop for a
// worker, and the Shutdown it stops on.
func (s *RustServer) backgroundUses(runtime string) []string {
	uses := []string{rustutil.UseLine(runtime+"::logging", []string{"self", "tracing"})}
	if len(s.Databases) > 0 {
		uses = append(uses, rustutil.UseLine(runtime+"::postgres", []string{"self", "Pool"}))
	}
	if s.Worker != nil {
		uses = append(uses, rustutil.UseLine(runtime+"::queue", []string{"PostgresQueue", "QueueDefinition"}))
	}
	var stackconfig []string
	if len(s.Clients) > 0 {
		stackconfig = append(stackconfig, "self", "Service")
	}
	if len(s.Databases) > 0 {
		stackconfig = append(stackconfig, "Database")
	}
	if len(stackconfig) > 0 {
		uses = append(uses, rustutil.UseLine(runtime+"::stackconfig", stackconfig))
	}
	if s.Worker != nil {
		uses = append(uses, rustutil.UseLine(runtime+"::worker", []string{"self", "Options"}))
	}
	return append(uses, "use "+runtime+"::Shutdown;")
}

// CrateUses are main.rs's use declarations of the generated crates and the
// implementations, each under its alias, in the order rustfmt sorts them.
func (s *RustServer) CrateUses() []string {
	var uses []string
	for _, a := range s.APIs {
		uses = append(uses, "use "+crateIdent(a.Crate)+" as "+a.Alias+";", "use "+a.ImplLib+" as "+a.ImplAlias+";")
	}
	for _, c := range s.Clients {
		uses = append(uses, "use "+crateIdent(c.Crate)+" as "+c.Alias+";")
	}
	for _, e := range s.Engines {
		uses = append(uses, "use "+crateIdent(e.TypesCrate)+" as "+e.TypesAlias+";")
	}
	slices.Sort(uses)
	return slices.Compact(uses)
}

// ImplementationsArgs are the arguments main.rs builds the API's
// Implementations with, beside its handlers: the identity runtime's
// authenticator over its identity service, and its service
// authenticator.
func (a *RustAPI) ImplementationsArgs() []string {
	args := []string{a.Handlers}
	// An API that mounts its engine's routes holds its identity service
	// and its service authenticator in an Arc, which the routes' callers
	// share (D58).
	switch {
	case a.Authenticator != "":
		args = append(args, "Arc::new("+a.Authenticator+")")
	case a.Identity != "":
		args = append(args, "Arc::new(IdentityAuthenticator::new(Arc::new("+a.Identity+")))")
	}
	if a.ServiceAuthenticator != "" {
		authenticator := "Arc::new(" + a.ServiceAuthenticator + ")"
		if a.MountsEngine() {
			authenticator = "Arc::clone(&" + a.ServiceAuthenticator + ") as _"
		}
		args = append(args, authenticator)
	}
	return args
}

// rustCallWidth is the widest a call's arguments run on one line before
// rustfmt puts each on a line of its own (fn_call_width).
const rustCallWidth = 60

// ImplementationsLet is main.rs's statement that builds the API's
// Implementations, as rustfmt lays it out (rustLetCall).
func (a *RustAPI) ImplementationsLet() string {
	return rustLetCall(a.Implementations, a.Alias+"::Implementations::new", "", a.ImplementationsArgs()...)
}

// WriteRust writes the entrypoint crate into dir: Cargo.toml and
// src/main.rs, and the Dockerfile with its ignore file when s.Docker is
// planned. A job's main.rs runs the job (job.rs.tmpl), and a worker's its
// claim loop (worker.rs.tmpl); every main.rs template takes the functions
// they share from wiring.rs.tmpl.
func WriteRust(s *RustServer, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	main := "main.rs.tmpl"
	switch {
	case s.Job != nil:
		main = "job.rs.tmpl"
	case s.Worker != nil:
		main = "worker.rs.tmpl"
	case s.Runner != nil:
		main = "runner.rs.tmpl"
	}
	files := []struct{ template, name string }{
		{main, RustMainFile},
		{"Cargo.toml.tmpl", RustManifestFile},
	}
	if len(s.Engines) > 0 {
		files = append(files, struct{ template, name string }{"engine.rs.tmpl", RustEngineFile})
	}
	if s.Docker != nil {
		files = append(files, struct{ template, name string }{"Dockerfile.rs.tmpl", DockerFile}, struct{ template, name string }{"dockerignore.rs.tmpl", DockerIgnoreFile})
	}
	for _, f := range files {
		if err := render(f.template, filepath.Join(dir, filepath.FromSlash(f.name)), s); err != nil {
			return fmt.Errorf("servergen: %s %s: %w", s.Kind, s.Name, err)
		}
	}
	return nil
}

// rustString is s as a Rust string literal.
func rustString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s) + `"`
}

// rustWidth is the width rustfmt lays a line out in.
const rustWidth = 100

// rustLet is the statement `let name = root.calls...;` inside a function
// body, indented four spaces, as rustfmt lays it out: on one line when it
// fits; else the root after the `=` and a call to a line below it when
// that line fits; else the root, with its calls, on the line below the
// `=`. Each of calls is a method call or `.await`, without its dot.
func rustLet(name, root string, calls ...string) string {
	const indent = "    "
	chain := root
	for _, c := range calls {
		chain += "." + strings.TrimPrefix(c, ".")
	}
	head := indent + "let " + name + " ="
	if line := head + " " + chain + ";"; len(line) <= rustWidth {
		return line
	}
	broken := func(indent, root string) string {
		var b strings.Builder
		b.WriteString(root)
		for _, c := range calls {
			b.WriteString("\n" + indent + "    ." + strings.TrimPrefix(c, "."))
		}
		return b.String() + ";"
	}
	if len(calls) > 0 && len(head+" "+root) <= rustWidth {
		return head + " " + broken(indent, root)
	}
	if line := indent + "    " + chain + ";"; len(line) <= rustWidth || len(calls) == 0 {
		return head + "\n" + line
	}
	return head + "\n" + indent + "    " + broken(indent+"    ", root)
}

// rustLetCall is the statement `let name = fn(args...)suffix;` inside a
// function body, indented four spaces, as rustfmt lays it out: on one line
// when the arguments take rustCallWidth columns at most and the line fits;
// else the call on the line below the `=` when that fits; else each
// argument on a line of its own. suffix follows the call's parenthesis, a
// `?` or nothing.
func rustLetCall(name, fn, suffix string, args ...string) string {
	const indent = "    "
	list := strings.Join(args, ", ")
	call := fn + "(" + list + ")" + suffix + ";"
	if len(list) <= rustCallWidth {
		if line := indent + "let " + name + " = " + call; len(line) <= rustWidth {
			return line
		}
		if line := indent + indent + call; len(line) <= rustWidth {
			return indent + "let " + name + " =\n" + line
		}
	}
	return indent + "let " + name + " = " + fn + "(\n" + indent + indent + strings.Join(args, ",\n"+indent+indent) + ",\n" + indent + ")" + suffix + ";"
}

// rustMacro is the statement `name!(args...);` inside a function body,
// indented four spaces, as rustfmt lays out a macro whose arguments are
// expressions: on one line when they take rustCallWidth columns at most
// and the line fits, and each on a line of its own otherwise, with no
// trailing comma.
func rustMacro(name string, args ...string) string {
	const indent = "    "
	list := strings.Join(args, ", ")
	if line := indent + name + "!(" + list + ");"; len(list) <= rustCallWidth && len(line) <= rustWidth {
		return line
	}
	return indent + name + "!(\n" + indent + indent + strings.Join(args, ",\n"+indent+indent) + "\n" + indent + ");"
}

// rustChainWidth is the widest a chain of calls runs on one line before
// rustfmt puts each call on a line of its own (chain_width).
const rustChainWidth = 60

// rustLetChain is the statement `let name = root.calls...;` inside a
// function body, indented four spaces, as rustfmt lays out a chain: on one
// line when the chain takes rustChainWidth columns at most and the line
// fits, and else the root after the `=` and each call on a line below it.
// Each of calls is a method call or `.await`, without its dot, and may end
// in `?`.
func rustLetChain(name, root string, calls ...string) string {
	const indent = "    "
	chain := root
	for _, c := range calls {
		chain += "." + c
	}
	if line := indent + "let " + name + " = " + chain + ";"; len(chain) <= rustChainWidth && len(line) <= rustWidth {
		return line
	}
	var b strings.Builder
	b.WriteString(indent + "let " + name + " = " + root)
	for _, c := range calls {
		b.WriteString("\n" + indent + indent + "." + c)
	}
	return b.String() + ";"
}

// SiteCORS is main.rs's statement that layers the site CORS of the APIs a
// site calls around the app, as rustfmt lays it out: the one API's
// arguments overflowing the list, or each API to a line.
func (s *RustServer) SiteCORS() string {
	var apis []string
	var args [][2]string
	for _, a := range s.APIs {
		if a.CORS == "" {
			continue
		}
		args = append(args, [2]string{"&" + a.Config + "." + a.CORS, a.Alias + "::operations::ALL"})
		apis = append(apis, "cors::Api::new("+args[len(args)-1][0]+", "+args[len(args)-1][1]+")")
	}
	// The engine's routes an API mounts answer its site's CORS too (D58).
	for _, a := range s.APIs {
		if a.CORS == "" || !a.MountsEngine() {
			continue
		}
		args = append(args, [2]string{"&" + a.Config + "." + a.CORS, rustString(a.Engine.Mount)})
		apis = append(apis, "engine::site_cors("+args[len(args)-1][0]+", "+args[len(args)-1][1]+")")
	}
	const open, close = "    let app = app.layer(cors::SiteCorsLayer::new(vec![", "]));"
	if line := open + strings.Join(apis, ", ") + close; len(line) <= rustWidth {
		return line
	}
	if len(apis) == 1 {
		return open + "cors::Api::new(\n        " + args[0][0] + ",\n        " + args[0][1] + ",\n    )" + close
	}
	return open + "\n        " + strings.Join(apis, ",\n        ") + ",\n    " + close
}
