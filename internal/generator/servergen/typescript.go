package servergen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// The entrypoint of a TypeScript server (D51; docs/stack-model.md,
// sections 8.1 and 8.6): a package at `<output-root>/server/<stack>/<server>/`
// holding main.ts, package.json and tsconfig.json, a member of the output
// root's Bun workspace, and a Dockerfile. Bun runs main.ts as it is, as it
// runs every generated package, so nothing is built. main.ts does what the
// Go entrypoint's main does: it loads each API's config, opens one pg Pool
// per database, builds one SDK client per API called, builds each API's
// implementation from its Deps and mounts every API's router on one Hono
// app beside /healthz and /readyz. An API whose authDb has a User table
// authenticates with the identity runtime (D50), as on a Go server: main.ts
// builds one identity store over the pool of that database, from the
// descriptor its TypeScript types export, and the API's identity service
// from its identity config field.

// BunVersion is the oven/bun image a TypeScript server's Dockerfile runs it
// on: tools.env's BUN_VERSION, the Bun the repository's checks run on.
const BunVersion = "1.4.0"

// The files of a TypeScript entrypoint package.
const (
	TypeScriptMainFile    = "main.ts"
	TypeScriptPackageFile = "package.json"
	TypeScriptConfigFile  = "tsconfig.json"
)

// The versions of the third-party packages main.ts imports beside Hono,
// which the API packages pin: pg, within the HTTP runtime's and the API
// packages' peer range, and the Cloud SQL Node connector the runtime's
// ./postgres entry loads, which only a server some environment places on
// Cloud SQL depends on, as only such a Go server links the Go connector.
const (
	pgVersion                = "8.23.0"
	cloudSQLConnectorPackage = "@google-cloud/cloud-sql-connector"
	cloudSQLConnectorVersion = "1.12.0"
)

// TypeScriptImplementation is where an API's TypeScript implementation
// lives (docs/stack-model.md, section 8.6).
type TypeScriptImplementation struct {
	// Dir is the package's directory, at the naming file's
	// [implementation_paths] typescript template.
	Dir string

	// Package is its npm name: its package.json's, or the scaffold's when
	// the scaffold writes it (ImplementationPackage).
	Package string
}

// TypeScriptAPIInput is one API a TypeScript server serves.
type TypeScriptAPIInput struct {
	// Output is the API's TypeScript package, with its Deps.
	Output *tsrestgen.APIOutput

	// Routes is the API's server output, whose routes the build checks
	// against the other APIs' (checkRoutes).
	Routes *apigen.APIOutput

	// Config is the API's EnvConfig: its derived fields and its callers
	// field. Nil when the API has none.
	Config *envgen.ConfigOutput

	// Implementation is where its implementation lives.
	Implementation TypeScriptImplementation
}

// TypeScriptInput is what a TypeScript server's entrypoint is planned
// from.
type TypeScriptInput struct {
	// Stack is the stack's name; Server the server's name in the stack.
	Stack  string
	Server string

	// Dir is the package's directory, ServerDir.
	Dir string

	// OutputRoot is the root of the Bun workspace the package joins.
	OutputRoot string

	// APIs are the APIs the server serves, sorted by service.
	APIs []TypeScriptAPIInput

	// Naming names the package and the runtime packages main.ts imports.
	Naming naming.Naming

	// RepositoryRoot is the Dockerfile's build context: the repository
	// root, or the naming file's [paths] build_context. Every directory the
	// image reads must lie under it, or no Dockerfile is written.
	RepositoryRoot string

	// ImplementationRoot is the root the naming file's
	// [implementation_paths] typescript template is relative to: the
	// repository root, the parent of the schemas root.
	ImplementationRoot string

	// PackageDirs are the directories of the workspace packages the
	// server depends on, by npm name: each API package, implementation
	// and SDK, which its image holds whole.
	PackageDirs map[string]string

	// Paths are the naming file's [paths]: the image builds superscalar's
	// Node addon and the HTTP runtime's package from the checkouts they
	// name, which the workspace's root overrides the packages with.
	Paths naming.LocalPaths

	// CloudSQL are the DB services some environment of the stack connects
	// the server to with a Cloud SQL connector configuration. The server
	// depends on the Cloud SQL Node connector when one of them is a
	// database it connects to.
	CloudSQL []string
}

// TypeScriptServer is a planned TypeScript entrypoint, what the templates
// read.
type TypeScriptServer struct {
	Stack   string
	Name    string
	Package string
	Naming  naming.Naming

	// RustVersion and BunVersion are the toolchain pins.
	RustVersion string
	BunVersion  string

	APIs      []*TypeScriptAPI
	Databases []*TypeScriptDatabase
	Clients   []*TypeScriptClient

	// CloudSQL are the databases, by service and sorted, that some
	// environment places on Cloud SQL. With one, the package depends on
	// the Cloud SQL Node connector; with none main.ts refuses a Cloud SQL
	// configuration.
	CloudSQL []string

	// Dependencies and DevDependencies are package.json's, sorted by name.
	Dependencies    []NpmDependency
	DevDependencies []NpmDependency

	// Docker is the Dockerfile's plan, nil when none is written, and
	// NoDocker says why.
	Docker   *TypeScriptDocker
	NoDocker string
}

// NpmDependency is a package.json dependency.
type NpmDependency struct {
	Name string
	Spec string
}

// TypeScriptAPI is a served API in main.ts.
type TypeScriptAPI struct {
	Service string

	// Module and Impl are the namespaces main.ts imports the API package
	// and its implementation as; Package and ImplPackage their npm names,
	// and ImplDir the implementation's directory from the repository root.
	Module      string
	Impl        string
	Package     string
	ImplPackage string
	ImplDir     string

	// Config, Deps, Implementations and Authenticate are the variables of
	// its config, its Deps, its implementations and its authenticator.
	// Config is empty when the API has no EnvConfig, and Authenticate when
	// no route needs an end user or the identity service establishes it.
	Config          string
	Deps            string
	Implementations string
	Authenticate    string

	// Identity is the variable of its identity service when it
	// authenticates with the identity runtime over the user model (D50),
	// empty otherwise, and IdentityField the identity config field the
	// service's config is read from (ir.IdentityConfigField).
	Identity      string
	IdentityField string

	// Callers is its callers field, which serviceAuthenticator reads, when
	// an operation has a service clause (D37).
	Callers string

	// Database is the pool Deps.db holds, nil without one.
	Database *TypeScriptDatabase

	// Calls are Deps' clients.
	Calls []TypeScriptCall
}

// TypeScriptCall is a client in an API's Deps.
type TypeScriptCall struct {
	Field  string
	Client *TypeScriptClient
}

// TypeScriptDatabase is a database the server opens one pool to, for every
// API on it.
type TypeScriptDatabase struct {
	Service string
	Var     string

	// Field is the derived config field the connection is read from, and
	// From the expression that reads it from the first API's config.
	Field string
	From  string

	// Store is the variable of the identity store over the pool when an
	// API on the database authenticates with the identity runtime (D50),
	// empty otherwise. Descriptor is the name main.ts imports the
	// database's identity descriptor as, from DescriptorModule, an entry of
	// its TypeScript types package, DescriptorPackage.
	Store             string
	Descriptor        string
	DescriptorModule  string
	DescriptorPackage string
}

// TypeScriptClient is an SDK client of an API called, built once for
// every API that calls it, by the function Constructor names.
type TypeScriptClient struct {
	Service     string
	Var         string
	Constructor string
	Module      string
	Package     string
	Class       string

	// From is the expression that reads the callee's endpoint from the
	// first calling API's config.
	From string
}

// TypeScriptDocker is a TypeScript server's Dockerfile plan. Paths are
// relative to the build context, slash-separated.
type TypeScriptDocker struct {
	// Dockerfile is the Dockerfile's path, Context the package's directory
	// and Workspace the output root, the Bun workspace's root.
	Dockerfile string
	Context    string
	Workspace  string

	// ScalarTypeScript is superscalar's TypeScript binding, whose native/
	// directory the addon goes in, and ScalarWorkspace the Cargo
	// workspace that holds the binding and its napi crate.
	ScalarTypeScript string
	ScalarWorkspace  string

	// HTTPRuntime is the HTTP runtime's TypeScript package, and
	// ScalarLink the binding's path from its node_modules, where the
	// runtime's own build resolves superscalar.
	HTTPRuntime string
	ScalarLink  string

	// Runtime are the directories the image copies from the build stage:
	// the workspace's root, then every package the server reaches outside
	// it, each with the node_modules the install linked into it.
	Runtime []string

	// Include are the paths the build context holds, and Exclude the
	// paths under them it leaves out: what a checkout builds, which the
	// image builds again.
	Include []string
	Exclude []string
}

// tsReserved are the names main.ts declares or imports beside the served
// APIs' own: an API, database or client never takes one.
var tsReserved = []string{
	"Hono", "createLogger", "serviceAuthenticator", "serviceCredentialFor", "errorHandler", "notFoundHandler",
	"connectPostgres", "ping", "Pool", "Database", "Service", "DEFAULT_PORT", "SHUTDOWN_TIMEOUT_MS",
	"READINESS_TIMEOUT_MS", "BunServer", "BunRuntime", "Dependency", "logger", "exitCodes", "main",
	"listenPort", "configure", "construct", "connect", "drain", "app", "draining", "dependencies", "bun",
	"server", "stopping", "shutdown", "port", "process",
	"parseIdentityConfigJSON", "postgresIdentityStore", "IdentityConfig", "identityConfig",
}

// PlanTypeScript plans the entrypoint of one TypeScript server. It refuses
// a server whose APIs register one method and path between them, since one
// app answers each once, and an API whose config holds no field for a
// database or a callee its Deps needs.
func PlanTypeScript(in TypeScriptInput) (*TypeScriptServer, error) {
	if len(in.APIs) == 0 {
		return nil, fmt.Errorf("stack %s: server %s serves no API", in.Stack, in.Server)
	}
	// A TypeScript router mounts every operation, a manually routed one
	// included, whose handler the implementation hands it.
	routes := Input{Stack: in.Stack, Server: in.Server, manualRoutes: true}
	for _, a := range in.APIs {
		routes.APIs = append(routes.APIs, APIInput{Output: a.Routes})
	}
	if err := checkRoutes(routes); err != nil {
		return nil, err
	}
	n := in.Naming.OrDefault()
	s := &TypeScriptServer{
		Stack:       in.Stack,
		Name:        in.Server,
		Package:     n.NpmServerPackage(in.Stack, in.Server),
		Naming:      n,
		RustVersion: RustVersion,
		BunVersion:  BunVersion,
	}
	taken := names{}
	for _, name := range tsReserved {
		taken[name] = true
	}
	databases := map[string]*TypeScriptDatabase{}
	clients := map[string]*TypeScriptClient{}
	for _, a := range in.APIs {
		o := a.Output
		stem := varStem(o.SchemaName)
		api := &TypeScriptAPI{
			Service:         o.SchemaName,
			Module:          taken.take(stem + "Api"),
			Impl:            taken.take(stem + "Impl"),
			Package:         o.PackageName,
			ImplPackage:     a.Implementation.Package,
			Deps:            taken.take(stem + "Deps"),
			Implementations: taken.take(stem + "Implementations"),
		}
		if in.ImplementationRoot != "" {
			if rel, err := filepath.Rel(in.ImplementationRoot, a.Implementation.Dir); err == nil {
				api.ImplDir = filepath.ToSlash(rel)
			}
		}
		derived := map[string]string{}
		if a.Config != nil {
			for _, f := range a.Config.Derived {
				derived[string(f.Kind)+" "+f.Service] = f.Key
			}
			api.Callers = a.Config.CallersField
		}
		if o.HasEnvConfig {
			api.Config = taken.take(stem + "Config")
		}
		if o.ChecksEndUsers() {
			api.Authenticate = taken.take(stem + "Authenticate")
		}
		if o.Identity != nil {
			api.Identity = taken.take(stem + "Identity")
			api.IdentityField = ir.IdentityConfigField(o.SchemaName)
		}
		if o.HasServiceCallers && (api.Callers == "" || api.Config == "") {
			return nil, fmt.Errorf("stack %s: server %s serves %s, whose operations have a service clause, and whose config has no callers field", in.Stack, in.Server, o.SchemaName)
		}
		if db := o.Deps.Database; db != "" {
			d := databases[db]
			if d == nil {
				key, ok := derived[string(ir.EdgeSQL)+" "+db]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: server %s serves %s, whose config has no field for its database %s", in.Stack, in.Server, o.SchemaName, db)
				}
				d = &TypeScriptDatabase{
					Service: db,
					Var:     taken.take(varStem(db) + "Pool"),
					Field:   key,
					From:    api.Config + "." + key,
				}
				databases[db] = d
				s.Databases = append(s.Databases, d)
				if slices.Contains(in.CloudSQL, db) {
					s.CloudSQL = append(s.CloudSQL, db)
				}
			}
			api.Database = d
		}
		if o.Identity != nil {
			d := api.Database
			if d == nil || d.Service != o.Identity.AuthDB {
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime over %s, its authDb, and opens no pool on it to build its identity store over", in.Stack, in.Server, o.SchemaName, o.Identity.AuthDB)
			}
			if d.Store == "" {
				stem := varStem(d.Service)
				d.Store = taken.take(stem + "IdentityStore")
				d.Descriptor = taken.take(stem + "IdentityDescriptor")
				d.DescriptorModule = o.Identity.DescriptorModule
				d.DescriptorPackage = n.NpmTypesPackage(d.Service)
			}
		}
		for _, call := range o.Deps.Calls {
			c := clients[call.Service]
			if c == nil {
				key, ok := derived[string(ir.EdgeHTTP)+" "+call.Service]
				if !ok || api.Config == "" {
					return nil, fmt.Errorf("stack %s: server %s serves %s, whose config has no field for %s, which it calls", in.Stack, in.Server, o.SchemaName, call.Service)
				}
				callee := varStem(call.Service)
				c = &TypeScriptClient{
					Service:     call.Service,
					Var:         taken.take(callee + "Client"),
					Constructor: taken.take("new" + strings.ToUpper(callee[:1]) + callee[1:] + "Client"),
					Module:      taken.take(callee + "Sdk"),
					Package:     call.Package,
					Class:       call.Client,
					From:        api.Config + "." + key,
				}
				clients[call.Service] = c
				s.Clients = append(s.Clients, c)
			}
			api.Calls = append(api.Calls, TypeScriptCall{Field: call.Field, Client: c})
		}
		s.APIs = append(s.APIs, api)
	}
	slices.Sort(s.CloudSQL)
	s.planPackage()
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planTypeScriptDocker(in, s, dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// RouterOptions are the members of the options main.ts builds the API's
// router with: its authenticator or its identity service, and the service
// authenticator over its callers field.
func (a *TypeScriptAPI) RouterOptions() []string {
	var out []string
	if a.Authenticate != "" {
		out = append(out, "authenticate: "+a.Authenticate)
	}
	if a.Identity != "" {
		out = append(out, "identity: "+a.Identity)
	}
	if a.Callers != "" {
		out = append(out, "authenticateService: serviceAuthenticator("+a.Config+"."+a.Callers+")")
	}
	return out
}

// Identity reports whether an API of the server authenticates with the
// identity runtime, so main.ts imports its ./identity entry.
func (s *TypeScriptServer) Identity() bool {
	return slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.Identity != "" })
}

// RuntimeImports are the names main.ts imports from the HTTP runtime's
// main entry.
func (s *TypeScriptServer) RuntimeImports() []string {
	out := []string{"createLogger"}
	if slices.ContainsFunc(s.APIs, func(a *TypeScriptAPI) bool { return a.Callers != "" }) {
		out = append(out, "serviceAuthenticator")
	}
	if len(s.Clients) > 0 {
		out = append(out, "serviceCredentialFor")
	}
	if len(s.Databases) > 0 {
		out = append(out, "type Database")
	}
	if len(s.Clients) > 0 {
		out = append(out, "type Service")
	}
	return out
}

// planPackage plans package.json's dependencies: each served API package,
// its implementation, each callee's SDK and the TypeScript types of each
// database an identity store reads from the workspace; the HTTP
// runtime, which the workspace's root overrides with a checkout or the
// registry serves; and the third-party packages main.ts imports.
func (s *TypeScriptServer) planPackage() {
	deps := map[string]string{
		s.Naming.HTTPRuntimeNpmPackage: "*",
		"hono":                         tsrestgen.HonoVersion,
	}
	for _, a := range s.APIs {
		deps[a.Package] = "workspace:*"
		deps[a.ImplPackage] = "workspace:*"
	}
	for _, c := range s.Clients {
		deps[c.Package] = "workspace:*"
	}
	for _, d := range s.Databases {
		if d.Store != "" {
			// main.ts imports the database's identity descriptor.
			deps[d.DescriptorPackage] = "workspace:*"
		}
	}
	dev := map[string]string{
		"@types/node": tsrestgen.NodeTypesVersion,
		"typescript":  tsrestgen.TypeScriptVersion,
	}
	if len(s.Databases) > 0 {
		deps["pg"] = pgVersion
		dev["@types/pg"] = tsrestgen.PGTypesVersion
	}
	if len(s.CloudSQL) > 0 {
		deps[cloudSQLConnectorPackage] = cloudSQLConnectorVersion
	}
	sorted := func(m map[string]string) []NpmDependency {
		out := make([]NpmDependency, 0, len(m))
		for _, name := range slices.Sorted(maps.Keys(m)) {
			out = append(out, NpmDependency{Name: name, Spec: m[name]})
		}
		return out
	}
	s.Dependencies, s.DevDependencies = sorted(deps), sorted(dev)
}

// planTypeScriptDocker plans the Dockerfile, or says why there is none.
// The image builds superscalar's Node addon from the checkout the naming
// file's [paths] scalar_typescript names, which no published package ships
// yet, and the HTTP runtime's package from the one [paths]
// http_runtime_typescript names, and installs the workspace, whose root
// overrides both packages with their checkouts. Every directory it reads
// must lie under the build context.
func planTypeScriptDocker(in TypeScriptInput, s *TypeScriptServer, dir string) (*TypeScriptDocker, string, error) {
	if in.RepositoryRoot == "" {
		return nil, "no repository root to take as the build context", nil
	}
	if in.Paths.ScalarTypeScript == "" {
		return nil, "the naming file's [paths] scalar_typescript is unset, and the image builds superscalar's Node addon from a checkout, since no published package ships it yet; set it to a superscalar checkout's bindings/typescript", nil
	}
	if in.Paths.HTTPRuntimeTypeScript == "" {
		return nil, "the naming file's [paths] http_runtime_typescript is unset, and the image builds the HTTP runtime's package from a checkout, since none is published yet; set it to a superschematic checkout's runtime/http/typescript", nil
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
	d := &TypeScriptDocker{}
	var ok bool
	if d.Workspace, ok = rel(in.OutputRoot); !ok {
		return nil, outside("the output root", in.OutputRoot), nil
	}
	if d.Context, ok = rel(dir); !ok {
		return nil, outside("the server's package", dir), nil
	}
	d.Dockerfile = path.Join(d.Context, DockerFile)
	if d.ScalarTypeScript, ok = rel(in.Paths.ScalarTypeScript); !ok {
		return nil, outside("superscalar's TypeScript binding", in.Paths.ScalarTypeScript), nil
	}
	d.ScalarWorkspace = path.Dir(path.Dir(d.ScalarTypeScript))
	if d.HTTPRuntime, ok = rel(in.Paths.HTTPRuntimeTypeScript); !ok {
		return nil, outside("the HTTP runtime's TypeScript package", in.Paths.HTTPRuntimeTypeScript), nil
	}
	link, err := filepath.Rel(filepath.Join(in.Paths.HTTPRuntimeTypeScript, "node_modules"), in.Paths.ScalarTypeScript)
	if err != nil {
		return nil, "", err
	}
	d.ScalarLink = filepath.ToSlash(link)

	// An install reads the workspace's root, its lockfile when the output
	// root's install wrote one, and the manifest of every member, which
	// patterns name, so the context does not depend on which services were
	// built before the stack. The server runs the packages it imports,
	// whole: every types package, whichever the generated packages it
	// imports reach, and each API package, SDK and implementation it
	// depends on.
	if in.ImplementationRoot == "" {
		return nil, "no repository root to find the implementations under", nil
	}
	implementations, ok := rel(filepath.Join(in.ImplementationRoot, filepath.FromSlash(in.Naming.OrDefault().TypeScriptImplementationGlob())))
	if !ok {
		return nil, outside("the TypeScript implementations at", in.Naming.OrDefault().TypeScriptImplementationGlob()), nil
	}
	d.Include = []string{
		d.Workspace + "/package.json",
		d.Workspace + "/bun.lock*",
		d.Workspace + "/types/typescript",
		d.Workspace + "/sdk/typescript/*/package.json",
		d.Workspace + "/api/*/package.json",
		d.Workspace + "/server/*/*/package.json",
		implementations + "/package.json",
		d.Context,
	}
	var runtime []string
	for _, name := range slices.Sorted(maps.Keys(in.PackageDirs)) {
		r, ok := rel(in.PackageDirs[name])
		if !ok {
			return nil, outside("package "+name+", at", in.PackageDirs[name]), nil
		}
		d.Include = append(d.Include, r)
		if r != d.Workspace && !strings.HasPrefix(r, d.Workspace+"/") {
			runtime = append(runtime, r)
		}
	}
	d.Include = append(d.Include, d.ScalarTypeScript, d.HTTPRuntime,
		d.ScalarWorkspace+"/Cargo.toml", d.ScalarWorkspace+"/Cargo.lock", d.ScalarWorkspace+"/crates")
	if in.Paths.VersionGraphTypeScript != "" {
		// The workspace's root overrides the version-graph runtime too, so
		// an install reads its manifest, though no server imports it.
		vg, ok := rel(in.Paths.VersionGraphTypeScript)
		if !ok {
			return nil, outside("the version-graph runtime's TypeScript package", in.Paths.VersionGraphTypeScript), nil
		}
		d.Include = append(d.Include, vg+"/package.json")
	}
	slices.Sort(d.Include)
	d.Include = slices.Compact(d.Include)
	slices.Sort(runtime)
	d.Runtime = append([]string{d.Workspace}, slices.Compact(runtime)...)
	// What a checkout builds, which the image builds again.
	d.Exclude = []string{d.ScalarTypeScript + "/dist", d.ScalarTypeScript + "/native", d.HTTPRuntime + "/dist"}
	return d, "", nil
}

// manifestName reads the name of the manifest at dir.
func manifestName(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, TypeScriptPackageFile))
	if err != nil {
		return "", err
	}
	var manifest struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return "", fmt.Errorf("%s: %w", filepath.Join(dir, TypeScriptPackageFile), err)
	}
	return manifest.Name, nil
}

// ImplementationPackage is the npm name of the TypeScript implementation
// of service at dir: its package.json's name, or the scaffold's when dir
// holds no package.json yet. The server's package.json depends on the
// implementation by that name, so a package.json without one fails.
func ImplementationPackage(n naming.Naming, service, dir string) (string, error) {
	name, err := manifestName(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return n.OrDefault().NpmImplementationPackage(service), nil
	}
	if err != nil {
		return "", fmt.Errorf("the implementation of %s: %w", service, err)
	}
	if name == "" {
		return "", fmt.Errorf("the implementation of %s, at %s, has a package.json with no name, and the server's package.json depends on it by name; name it, as the scaffold names it %s", service, dir, n.OrDefault().NpmImplementationPackage(service))
	}
	return name, nil
}

// WriteTypeScript writes the entrypoint package into dir: main.ts,
// package.json and tsconfig.json, and the Dockerfile with its ignore file
// when s.Docker is planned.
func WriteTypeScript(s *TypeScriptServer, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	gen := codegen.NewFileGenerator(templatesFS, templateFuncs())
	files := []struct{ template, name string }{
		{"main.ts.tmpl", TypeScriptMainFile},
		{"package.json.tmpl", TypeScriptPackageFile},
		{"tsconfig.json.tmpl", TypeScriptConfigFile},
	}
	if s.Docker != nil {
		files = append(files, struct{ template, name string }{"Dockerfile.ts.tmpl", DockerFile}, struct{ template, name string }{"dockerignore.ts.tmpl", DockerIgnoreFile})
	}
	for _, f := range files {
		if err := gen.GenerateFile(codegen.NewFileConfig(templatesFS, f.template, filepath.Join(dir, f.name), s, nil)); err != nil {
			return fmt.Errorf("servergen: server %s: %w", s.Name, err)
		}
	}
	return nil
}
