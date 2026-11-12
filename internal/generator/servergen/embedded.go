package servergen

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	ir "github.com/parable-work/superschematic/ir"
)

// The crate of an environment that builds as one binary (D116): a library
// with a short main.rs over it, at
// `<output-root>/embedded/<stack>/<environment>/`, a member of the output
// root's Cargo workspace beside the servers' crates (D57).
//
// The library holds every server of the environment in one process. It
// opens the data directory, migrates each database's SQLite file with the
// plans the binary carries (superschematic-migrate's start), loads each
// API's EnvConfig, opens each file once for every API on it, builds each
// API's Deps and calls its `create`, and gives its host `handle`, which
// sends a request under `/api/<service>` to that API's `handle` and
// answers any other path from the site, on one origin (D91), and `close`.
// A call from one API of the binary to another is the SDK's, over the
// HTTP runtime's in-process transport, with the loopback credential of
// the caller's server (docs/stack-model.md, section 9.9). main.rs serves
// `handle` on 127.0.0.1 at $PORT.
//
// The stack's build writes Cargo.toml, src/lib.rs and src/main.rs. What a
// binary carries, its plans and its site's files, is `stack build
// <environment>`'s to write (WriteEmbeddedCarried): the stack's build
// writes src/carried.rs only when it is missing, with nothing carried, so
// the workspace resolves and a binary cargo builds without `stack build`
// refuses to start, naming the command.

// EmbeddedRoot is the directory of the output root that holds the crates
// of the environments that build as one binary.
const EmbeddedRoot = "embedded"

// The files of an embedded crate beside Cargo.toml and src/main.rs.
const (
	EmbeddedLibFile       = "src/lib.rs"
	EmbeddedCarriedSource = "src/carried.rs"

	// EmbeddedCarriedDir holds what `stack build` compiles into the
	// binary: each database's plans and adoption, and the site's files.
	EmbeddedCarriedDir = "carried"
)

// EmbeddedDir returns the directory of the crate of environment of stack:
// `<output-root>/embedded/<stack>/<environment>`.
func EmbeddedDir(outputRoot, stack, environment string) string {
	return filepath.Join(outputRoot, EmbeddedRoot, stack, environment)
}

// EmbeddedBinary returns the name of the binary of environment of stack:
// the two names in lower case, joined by a hyphen (`notes-desktop`).
func EmbeddedBinary(stack, environment string) string {
	kebab := func(name string) string {
		return strings.ReplaceAll(rustutil.ToSnakeCase(name), "_", "-")
	}
	return kebab(stack) + "-" + kebab(environment)
}

// EmbeddedServerInput is one server of the environment: its deployable
// and the APIs it serves.
type EmbeddedServerInput struct {
	Name string
	APIs []RustAPIInput
}

// EmbeddedDatabaseInput is one DB schema a database of the environment
// hosts: its service and its file, relative to the binary's data
// directory.
type EmbeddedDatabaseInput struct {
	Service string
	File    string
}

// EmbeddedVariable is an environment variable an environment binds: a
// setting's literal, or a member of a derived field.
type EmbeddedVariable struct {
	Name  string
	Value string

	// Server is the server whose binding it is, which a refusal names.
	Server string
}

// EmbeddedSiteInput is the environment's site: its deployable, the file a
// path that names no file is answered with, or empty, and the APIs its
// config names, sorted.
type EmbeddedSiteInput struct {
	Name     string
	Fallback string
	APIs     []string
}

// EmbeddedInput is what the crate of an environment's binary is planned
// from.
type EmbeddedInput struct {
	Stack       string
	Environment string

	// Dir is the crate's directory, EmbeddedDir, and OutputRoot the root
	// of the Cargo workspace it joins.
	Dir        string
	OutputRoot string

	// Port is the port the binary listens on when PORT is unset.
	Port int

	// Servers are the environment's servers, by name, each with the APIs
	// it serves, sorted by service. Workers names the environment's
	// workers that run, which the binary does not hold yet.
	Servers []EmbeddedServerInput
	Workers []string

	// Databases are the DB schemas the environment's databases host, by
	// service.
	Databases []EmbeddedDatabaseInput

	// Bound are the variables the environment binds for its servers. The
	// binary sets each the process's environment leaves unset before it
	// loads an API's configuration, and sets a database field's to its
	// file under the data directory.
	Bound []EmbeddedVariable

	// Site is the environment's site, nil without one.
	Site *EmbeddedSiteInput

	Naming             naming.Naming
	RepositoryRoot     string
	ImplementationRoot string
	SDKDirs            map[string]string
	Paths              naming.LocalPaths
}

// EmbeddedCrate is a planned embedded crate, what its templates read: the
// wiring of every API of the environment (RustServer), and what joins
// them into one binary.
type EmbeddedCrate struct {
	*RustServer

	Environment string
	Port        int

	// Lib is the name Rust source gives the crate's library.
	Lib string

	// Servers are the environment's servers, and Wired its APIs, in the
	// order RustServer.APIs holds them.
	Servers []*EmbeddedServer
	Wired   []*EmbeddedAPI

	// Callees are the APIs of the binary another calls, by service: each
	// has one client constructor. Built are the clients, one per server
	// that calls an API.
	Callees []*EmbeddedAPI
	Built   []*EmbeddedClient

	// Stores are the databases' files, by service.
	Stores []*EmbeddedStore

	// Bound are the variables the binary sets when the process's
	// environment leaves them unset, by name.
	Bound []EmbeddedVariable

	// Site is the environment's site, nil without one.
	Site *EmbeddedSiteInput

	// Notes are what the build says of the crate: an operation the binary
	// does not serve.
	Notes []string
}

// EmbeddedServer is a server of the environment inside the binary.
type EmbeddedServer struct {
	Name   string
	Serves []string

	// Loopback is the variable of the server's loopback key, empty when
	// it calls no API of the binary whose operations have a service
	// clause (docs/stack-model.md, section 9.9).
	Loopback string
}

// LoopbackLet is the statement that generates the server's loopback key.
func (s *EmbeddedServer) LoopbackLet() string {
	serves := make([]string, len(s.Serves))
	for i, api := range s.Serves {
		serves[i] = rustString(api)
	}
	return rustLetCall(s.Loopback, "loopback", "?", rustString(s.Name), "&["+strings.Join(serves, ", ")+"]")
}

// EmbeddedAPI is an API of the binary.
type EmbeddedAPI struct {
	*RustAPI

	// Server is the server that serves it.
	Server *EmbeddedServer

	// Slot is the field of the binary's APIs that holds it, Path the
	// constant of the path the binary's origin serves it under, and Base
	// that path (`/api/shop-api`).
	Slot string
	Path string
	Base string

	// Holds are the clients its Deps hold, each of an API of the binary.
	Holds []EmbeddedCall

	// Constructor, SDK and Client are the function that builds a client of
	// it, the alias of its SDK crate and the SDK's client struct, set when
	// another API of the binary calls it.
	Constructor string
	SDK         string
	Client      string

	// Accepts are the variables of the loopback keys its service
	// authenticator accepts: those of the servers that call it.
	Accepts []string

	// CallersVar is the variable of its callers field as its service
	// authenticator reads it, the loopback issuers added.
	CallersVar string

	// Disabled are the memory stores its Deps hold, each off in the
	// environment (D75).
	Disabled []RustMemoryStoreUse
}

// EmbeddedCall is a client in an API's Deps.
type EmbeddedCall struct {
	Field  string
	Client *EmbeddedClient
}

// EmbeddedClient is a client of Callee, an API of the binary, built once
// for every API of one server that calls it.
type EmbeddedClient struct {
	Var    string
	Callee *EmbeddedAPI

	// From is the expression of the callee's endpoint in the calling API's
	// config, and Loopback the expression of the credential the client
	// signs with: Some of the caller's server's loopback key when the
	// callee's operations have a service clause, None otherwise.
	From     string
	Loopback string
}

// ClientLet is the statement that builds the client.
func (c *EmbeddedClient) ClientLet() string {
	return rustLetCall(c.Var, c.Callee.Constructor, "?", "&"+c.From, "&apis", c.Loopback)
}

// EmbeddedStore is a database's file: the variable its open file is held
// in, the constant of its path under the data directory, and the
// variables of its field, which the binary sets to its file.
type EmbeddedStore struct {
	*RustDatabase

	File      string
	FileConst string
	Variables []string
}

// PlanEmbedded plans the crate of an environment's one binary. It refuses
// by name what the binary does not hold: a worker, a database that is no
// SQLite file, an engine, a bucket, a memory store that is on, an API over
// the user model, and two servers that bind one variable to two values.
func PlanEmbedded(in EmbeddedInput) (*EmbeddedCrate, error) {
	where := fmt.Sprintf("stack %s: environment %s builds as one binary (D116)", in.Stack, in.Environment)
	if in.Paths.MigrateRust == "" {
		return nil, fmt.Errorf("%s, whose crate depends on the migration runner's checkout, superschematic-migrate, and the naming file's [paths] migrate_rust is unset", where)
	}
	if len(in.Workers) > 0 {
		return nil, fmt.Errorf("%s, and runs %s %s, which it does not hold yet: the binary runs a worker through its drain, which the embedded crate does not call; turn the worker off in the environment with `enabled: false`", where, plural(len(in.Workers), "worker"), strings.Join(in.Workers, ", "))
	}
	if len(in.Servers) == 0 {
		return nil, fmt.Errorf("%s, and has no server: a binary serves at least one API", where)
	}
	binary := EmbeddedBinary(in.Stack, in.Environment)
	plan := RustInput{
		Stack:              in.Stack,
		Server:             binary,
		Dir:                in.Dir,
		OutputRoot:         in.OutputRoot,
		Naming:             in.Naming,
		RepositoryRoot:     in.RepositoryRoot,
		ImplementationRoot: in.ImplementationRoot,
		SDKDirs:            in.SDKDirs,
		Paths:              in.Paths,
		Embedded:           true,
	}
	serverOf := map[string]*EmbeddedServer{}
	var servers []*EmbeddedServer
	for _, s := range in.Servers {
		routes := Input{Stack: in.Stack, Server: s.Name}
		server := &EmbeddedServer{Name: s.Name}
		for _, a := range s.APIs {
			routes.APIs = append(routes.APIs, APIInput{Output: a.Routes})
			if other, twice := serverOf[a.Output.SchemaName]; twice {
				return nil, fmt.Errorf("%s, and servers %s and %s both serve %s: one process holds one implementation of an API; serve it from one server", where, other.Name, s.Name, a.Output.SchemaName)
			}
			serverOf[a.Output.SchemaName] = server
			server.Serves = append(server.Serves, a.Output.SchemaName)
			// One origin serves the site and every API, so no API answers
			// CORS for the site.
			a.CORS = false
			plan.APIs = append(plan.APIs, a)
		}
		if err := checkRoutes(routes); err != nil {
			return nil, err
		}
		servers = append(servers, server)
	}
	slices.SortFunc(plan.APIs, func(a, b RustAPIInput) int {
		return strings.Compare(a.Output.SchemaName, b.Output.SchemaName)
	})
	server, err := PlanRust(plan)
	if err != nil {
		return nil, err
	}
	n := in.Naming.OrDefault()
	server.Package = strings.TrimSuffix(n.RustServerCrate(in.Stack, in.Environment), "-server") + "-embedded"
	c := &EmbeddedCrate{
		RustServer:  server,
		Environment: in.Environment,
		Port:        in.Port,
		Lib:         crateIdent(server.Package),
		Servers:     servers,
		Site:        in.Site,
	}
	switch {
	case len(server.Databases) > 0:
		return nil, fmt.Errorf("%s, and %s is on %s, which is no SQLite file: its DB service lists another dialect first", where, server.APIs[0].Service, server.Databases[0].Service)
	case len(server.Engines) > 0:
		return nil, fmt.Errorf("%s, and %s connects to %s, an engine database, whose engine the binary does not open: an engine on a SQLite file is not built", where, server.Engines[0].API.Service, server.Engines[0].Service)
	case len(server.Buckets) > 0:
		return nil, fmt.Errorf("%s, and an API of it lists bucket %s, which the binary does not keep: a bucket on a local directory is not built", where, server.Buckets[0].Service)
	}
	// The names the library declares beside the plan's.
	taken := rustNames{}
	for _, name := range append(slices.Clone(rustReserved), "apis", "data", "options", "request", "site") {
		taken[name] = true
	}
	for _, a := range server.APIs {
		for _, name := range []string{a.Alias, a.ImplAlias, a.Config, a.Deps, a.Handlers, a.Implementations, a.Router, a.ServiceAuthenticator, a.RouterOptions, a.Identity} {
			taken[name] = true
		}
	}
	for _, d := range server.Files {
		taken[d.Var] = true
	}
	for _, m := range server.MemoryStores {
		taken[m.Var] = true
	}
	wired := map[string]*EmbeddedAPI{}
	for i, a := range server.APIs {
		if a.Identity != "" {
			return nil, fmt.Errorf("%s, and %s authenticates its users with the identity runtime, whose store on a SQLite file a Rust API does not open yet (D116)", where, a.Service)
		}
		if a.RateLimitStore != nil {
			return nil, fmt.Errorf("%s, and %s counts its rate limits in %s, a memory store, which the binary does not reach", where, a.Service, a.RateLimitStore.Service)
		}
		stem := rustStem(a.Service)
		api := &EmbeddedAPI{
			RustAPI:  a,
			Server:   serverOf[a.Service],
			Slot:     stem,
			Path:     strings.ToUpper(stem) + "_PATH",
			Base:     ir.SiteDevAPIPath(a.Service),
			Disabled: a.MemoryStores,
		}
		if unrouted := plan.APIs[i].Output.ManualEndpoints; len(unrouted) > 0 {
			names := make([]string, len(unrouted))
			for j, ep := range unrouted {
				names[j] = ep.Namespace + "." + ep.Name
			}
			c.Notes = append(c.Notes, fmt.Sprintf("%s: the binary does not serve %s, which the implementation mounts on a router of its own (@manualRouteRegistration): a request in the process is answered by handle, which routes none", a.Service, strings.Join(names, ", ")))
		}
		wired[a.Service] = api
		c.Wired = append(c.Wired, api)
	}
	built := map[string]*EmbeddedClient{}
	for _, api := range c.Wired {
		for _, call := range api.Calls {
			callee := wired[call.Client.Service]
			if callee == nil {
				return nil, fmt.Errorf("%s, and %s calls %s, which no server of the environment serves: a binary reaches the APIs it holds", where, api.Service, call.Client.Service)
			}
			if callee.Constructor == "" {
				callee.Constructor = call.Client.Constructor
				callee.SDK, callee.Client = call.Client.Alias, call.Client.Client
				c.Callees = append(c.Callees, callee)
			}
			// Every API of one server shares its client of the callee, which
			// signs as that server.
			key := api.Server.Name + "\x00" + callee.Service
			client := built[key]
			if client == nil {
				client = &EmbeddedClient{
					Var:      taken.take(rustStem(api.Server.Name) + "_" + rustStem(callee.Service) + "_client"),
					Callee:   callee,
					From:     call.Client.From,
					Loopback: "None",
				}
				if callee.ServiceAuthenticator != "" {
					if api.Server.Loopback == "" {
						api.Server.Loopback = taken.take(rustStem(api.Server.Name) + "_loopback")
					}
					client.Loopback = "Some(&" + api.Server.Loopback + ")"
					if !slices.Contains(callee.Accepts, api.Server.Loopback) {
						callee.Accepts = append(callee.Accepts, api.Server.Loopback)
					}
				}
				built[key] = client
				c.Built = append(c.Built, client)
			}
			api.Holds = append(api.Holds, EmbeddedCall{Field: call.Field, Client: client})
		}
	}
	for _, api := range c.Wired {
		if api.ServiceAuthenticator != "" {
			api.CallersVar = taken.take(rustStem(api.Service) + "_callers")
		}
	}
	files := map[string]string{}
	for _, d := range in.Databases {
		files[d.Service] = d.File
	}
	for _, d := range server.Files {
		file, ok := files[d.Service]
		if !ok {
			return nil, fmt.Errorf("%s, and no database of it hosts %s, which an API of it is on", where, d.Service)
		}
		c.Stores = append(c.Stores, &EmbeddedStore{
			RustDatabase: d,
			File:         file,
			FileConst:    strings.ToUpper(rustStem(d.Service)) + "_FILE",
		})
	}
	// A database no API of the binary is on has a file all the same, which
	// the binary migrates as it starts.
	for _, d := range in.Databases {
		if !slices.ContainsFunc(c.Stores, func(s *EmbeddedStore) bool { return s.Service == d.Service }) {
			c.Stores = append(c.Stores, &EmbeddedStore{
				RustDatabase: &RustDatabase{Service: d.Service, SQLite: true},
				File:         d.File,
				FileConst:    strings.ToUpper(rustStem(d.Service)) + "_FILE",
			})
		}
	}
	slices.SortFunc(c.Stores, func(a, b *EmbeddedStore) int { return strings.Compare(a.Service, b.Service) })
	if err := c.planBound(in, where); err != nil {
		return nil, err
	}
	if err := c.planEmbeddedManifest(in); err != nil {
		return nil, err
	}
	return c, nil
}

// planBound sorts the variables the environment binds into those the
// binary sets to a database's file and those it sets as they are, and
// refuses two servers that bind one variable to two values: one process
// has one environment.
func (c *EmbeddedCrate) planBound(in EmbeddedInput, where string) error {
	byFile := map[string]*EmbeddedStore{}
	for _, s := range c.Stores {
		byFile["sqlite:"+s.File] = s
	}
	seen := map[string]EmbeddedVariable{}
	for _, v := range in.Bound {
		if prev, twice := seen[v.Name]; twice {
			if prev.Value != v.Value {
				return fmt.Errorf("%s, and servers %s and %s bind %s to two values: one process has one environment; bind them alike, or name the settings apart", where, prev.Server, v.Server, v.Name)
			}
			continue
		}
		seen[v.Name] = v
		if store := byFile[v.Value]; store != nil {
			store.Variables = append(store.Variables, v.Name)
			continue
		}
		c.Bound = append(c.Bound, v)
	}
	slices.SortFunc(c.Bound, func(a, b EmbeddedVariable) int { return strings.Compare(a.Name, b.Name) })
	for _, s := range c.Stores {
		slices.Sort(s.Variables)
		if s.Var != "" && len(s.Variables) == 0 {
			return fmt.Errorf("%s, and it binds no field of %s to its file %s: the environment's sql edges derive it", where, s.Service, s.File)
		}
	}
	return nil
}

// planEmbeddedManifest plans Cargo.toml's dependencies from the plan's:
// the registry crates main.rs and the library name, the HTTP runtime with
// the features they use, the migration runner, each API crate, its
// implementation's mirror and each callee's SDK.
func (c *EmbeddedCrate) planEmbeddedManifest(in EmbeddedInput) error {
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return err
	}
	rel, err := naming.RelPath(dir, in.Paths.MigrateRust)
	if err != nil {
		return fmt.Errorf("stack %s: environment %s: %w", in.Stack, in.Environment, err)
	}
	var deps []CargoDependency
	for _, d := range c.Dependencies {
		// The library dispatches by path itself, with no tower.
		if d.Name != "tower" {
			deps = append(deps, d)
		}
	}
	deps = append(deps, CargoDependency{EmbeddedMigrateCrate, "{ path = " + quoteToml(rel) + " }"})
	// The registry crates stay first, then the path crates by name.
	registry := slices.IndexFunc(deps, func(d CargoDependency) bool { return strings.Contains(d.Spec, "path = ") })
	if registry < 0 {
		registry = len(deps)
	}
	local := deps[registry:]
	slices.SortFunc(local, func(a, b CargoDependency) int { return strings.Compare(a.Name, b.Name) })
	c.Dependencies = deps
	return nil
}

// EmbeddedMigrateCrate is the migration runner a binary links
// (runtime/migrate/rust).
const EmbeddedMigrateCrate = "superschematic-migrate"

// Runtime is the name Rust source gives the HTTP runtime's crate.
func (c *EmbeddedCrate) Runtime() string { return crateIdent(c.Naming.HTTPRuntimeRustCrate) }

// Calls reports whether an API of the binary calls another.
func (c *EmbeddedCrate) Calls() bool { return len(c.Callees) > 0 }

// Loopbacks reports whether a server of the binary has a loopback key.
func (c *EmbeddedCrate) Loopbacks() bool {
	return slices.ContainsFunc(c.Servers, func(s *EmbeddedServer) bool { return s.Loopback != "" })
}

// ServiceAuth reports whether an API of the binary verifies a calling
// service.
func (c *EmbeddedCrate) ServiceAuth() bool {
	return slices.ContainsFunc(c.Wired, func(a *EmbeddedAPI) bool { return a.ServiceAuthenticator != "" })
}

// DisabledStores reports whether an API of the binary holds a memory
// store, which is off.
func (c *EmbeddedCrate) DisabledStores() bool {
	return slices.ContainsFunc(c.Wired, func(a *EmbeddedAPI) bool { return len(a.Disabled) > 0 })
}

// Configured reports whether an API of the binary has an EnvConfig.
func (c *EmbeddedCrate) Configured() bool {
	return slices.ContainsFunc(c.Wired, func(a *EmbeddedAPI) bool { return a.Config != "" })
}

// OpensFiles reports whether an API of the binary is on a database.
func (c *EmbeddedCrate) OpensFiles() bool { return len(c.Files) > 0 }

// LibUses are the library's use declarations of the HTTP runtime, in the
// order rustfmt sorts them.
func (c *EmbeddedCrate) LibUses() []string {
	runtime := c.Runtime()
	var uses []string
	if c.Calls() {
		uses = append(uses, "use "+runtime+"::in_process;")
	}
	uses = append(uses, rustutil.UseLine(runtime+"::logging", []string{"self", "tracing"}))
	if slices.ContainsFunc(c.Wired, func(a *EmbeddedAPI) bool { return a.MCPConfirmationKey != "" }) {
		uses = append(uses, rustutil.UseLine(runtime+"::mcp", []string{"EndpointOptions"}))
	}
	if c.DisabledStores() {
		uses = append(uses, rustutil.UseLine(runtime+"::memorystore", []string{"DisabledMemoryStore", "MemoryStore"}))
	}
	if c.OpensFiles() {
		uses = append(uses, "use "+runtime+"::orm::sqlite::{self, Database as SqliteFile};")
	}
	var stackconfig []string
	if c.OpensFiles() {
		stackconfig = append(stackconfig, "Database")
	}
	if c.Calls() {
		stackconfig = append(stackconfig, "Service")
	}
	// rustfmt sorts a constant after the types.
	if c.Loopbacks() {
		stackconfig = append(stackconfig, "SERVICE_AUTHORIZATION_HEADER")
	}
	switch len(stackconfig) {
	case 0:
	case 1:
		uses = append(uses, "use "+runtime+"::stackconfig::"+stackconfig[0]+";")
	default:
		uses = append(uses, "use "+runtime+"::stackconfig::{"+strings.Join(stackconfig, ", ")+"};")
	}
	var root []string
	if c.ServiceAuth() {
		root = append(root, "JwtServiceAuthenticator")
	}
	if c.Loopbacks() || c.Calls() {
		root = append(root, "Loopback")
	}
	if c.ServiceAuth() {
		root = append(root, "ServiceAuthConfig")
	}
	if len(root) > 0 {
		uses = append(uses, rustutil.UseLine(runtime, root))
	}
	return uses
}

// CrateUses are the library's use declarations of the API crates, their
// implementations and the SDKs of the APIs called, in the order rustfmt
// sorts them.
func (c *EmbeddedCrate) CrateUses() []string {
	var uses []string
	for _, a := range c.Wired {
		uses = append(uses, "use "+crateIdent(a.Crate)+" as "+a.Alias+";", "use "+a.ImplLib+" as "+a.ImplAlias+";")
	}
	for _, cl := range c.Clients {
		uses = append(uses, "use "+crateIdent(cl.Crate)+" as "+cl.Alias+";")
	}
	slices.Sort(uses)
	return slices.Compact(uses)
}

// rustCallStatement is the statement `fn(args...)suffix;` inside a
// function body, indented four spaces, as rustfmt lays it out: on one line
// when the arguments take rustCallWidth columns at most and the line
// fits, and each argument on a line of its own otherwise.
func rustCallStatement(fn, suffix string, args ...string) string {
	const indent = "    "
	list := strings.Join(args, ", ")
	if line := indent + fn + "(" + list + ")" + suffix + ";"; len(list) <= rustCallWidth && len(line) <= rustWidth {
		return line
	}
	return indent + fn + "(\n" + indent + indent + strings.Join(args, ",\n"+indent+indent) + ",\n" + indent + ")" + suffix + ";"
}

// MigrateCall is the statement that migrates the database's file.
func (s *EmbeddedStore) MigrateCall() string {
	return rustCallStatement("migrate_file", "?", "&data", rustString(s.Service), s.FileConst)
}

// BindCalls are the statements that set each variable of the database's
// field to its file under the data directory.
func (s *EmbeddedStore) BindCalls() []string {
	calls := make([]string, len(s.Variables))
	for i, name := range s.Variables {
		calls[i] = rustCallStatement("bind_file", "", "&data", rustString(name), s.FileConst)
	}
	return calls
}

// HoldCall is the statement that puts the API, once built, where a
// request and a call from another API reach it.
func (a *EmbeddedAPI) HoldCall() string {
	return rustCallStatement("hold", "", "&apis."+a.Slot, "Some("+a.Router+")")
}

// ApiLet is the statement that builds an API over its implementations:
// with its router options when its MCP endpoint confirms a tool, whose
// confirmations the options' key signs (D93), and with the defaults
// otherwise.
func (a *EmbeddedAPI) ApiLet() string {
	if a.RouterOptions != "" {
		return rustLetCall(a.Router, a.Alias+"::Api::with", "", a.Implementations, a.RouterOptions)
	}
	return rustLetCall(a.Router, a.Alias+"::Api::new", "", a.Implementations)
}

// ServiceAuthenticatorLets are the statements that build the API's service
// authenticator: its callers field, each calling server's loopback issuer
// added to it, then the authenticator over them.
func (a *EmbeddedAPI) ServiceAuthenticatorLets() []string {
	lets := []string{rustLet(a.CallersVar, a.Config+"."+a.Callers+".clone()")}
	for _, loopback := range a.Accepts {
		lets = append(lets, rustLetCall(a.CallersVar, "accept", "?", rustString(a.Service), "&"+loopback, a.CallersVar))
	}
	return append(lets, rustLetCall(a.ServiceAuthenticator, "service_authenticator", "?", rustString(a.Service), a.CallersVar))
}

// WriteEmbedded writes the crate into dir: Cargo.toml, src/lib.rs and
// src/main.rs, and src/carried.rs, with nothing carried, when it is
// missing. What `stack build` wrote there, and under carried/, stays.
func WriteEmbedded(c *EmbeddedCrate, dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	for _, f := range []struct{ template, name string }{
		{"embedded_lib.rs.tmpl", EmbeddedLibFile},
		{"embedded_main.rs.tmpl", RustMainFile},
		{"embedded_Cargo.toml.tmpl", RustManifestFile},
	} {
		if err := render(f.template, filepath.Join(dir, filepath.FromSlash(f.name)), c); err != nil {
			return fmt.Errorf("servergen: environment %s: %w", c.Environment, err)
		}
	}
	carried := filepath.Join(dir, filepath.FromSlash(EmbeddedCarriedSource))
	if _, err := os.Stat(carried); errors.Is(err, fs.ErrNotExist) {
		return WriteEmbeddedCarried(dir, EmbeddedCarried{Stack: c.Stack, Environment: c.Environment})
	} else if err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	return nil
}

// PruneEmbedded removes the crate of each environment of stack that is
// not in keep, and the stack's directory once it is empty: an environment
// that was renamed, removed or moved to another target leaves none.
func PruneEmbedded(outputRoot, stack string, keep []string) error {
	root := filepath.Join(outputRoot, EmbeddedRoot, stack)
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	left := 0
	for _, e := range entries {
		if e.IsDir() && !slices.Contains(keep, e.Name()) {
			if err := os.RemoveAll(filepath.Join(root, e.Name())); err != nil {
				return fmt.Errorf("servergen: %w", err)
			}
			continue
		}
		left++
	}
	if left == 0 {
		if err := os.Remove(root); err != nil {
			return fmt.Errorf("servergen: %w", err)
		}
		// The directory of every stack's crates goes with the last.
		_ = os.Remove(filepath.Join(outputRoot, EmbeddedRoot))
	}
	return nil
}

// EmbeddedCarried is what `stack build` compiles into a binary (D116):
// each database's plans, in order, and its adoption, and the site's
// files. Build says which build it is, for the binary's refusal.
type EmbeddedCarried struct {
	Stack       string
	Environment string

	// Release is true for a build whose plans a release recorded, and
	// false for one that carries the plan of its own build.
	Release bool

	Databases []EmbeddedCarriedDatabase
	Site      []EmbeddedCarriedFile
}

// EmbeddedCarriedDatabase is the plans a binary carries for one database,
// each a plan document, in order, and its adoption, nil without one.
type EmbeddedCarriedDatabase struct {
	Service  string
	Plans    [][]byte
	Adoption *EmbeddedCarriedAdoption
}

// EmbeddedCarriedAdoption is a database's one adoption: the model
// document a file built by hand is recorded at, and the statement that
// says the file holds it.
type EmbeddedCarriedAdoption struct {
	Model []byte
	Check string
}

// EmbeddedCarriedFile is one file of the site: its path under the site's
// output, slash-separated, its content type, the hex SHA-256 of its
// content, which is its ETag, and the content.
type EmbeddedCarriedFile struct {
	Path        string
	ContentType string
	Digest      string
	Content     []byte
}

// WriteEmbeddedCarried writes what a binary carries into the crate at
// dir: the documents and the site's files under carried/, which it
// replaces whole, and src/carried.rs, which names each for the compiler
// to read into the binary.
func WriteEmbeddedCarried(dir string, carried EmbeddedCarried) error {
	fail := func(err error) error {
		return fmt.Errorf("servergen: what the binary of environment %s carries: %w", carried.Environment, err)
	}
	root := filepath.Join(dir, EmbeddedCarriedDir)
	if err := os.RemoveAll(root); err != nil {
		return fail(err)
	}
	write := func(rel string, data []byte) error {
		file := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		return os.WriteFile(file, data, 0o644)
	}
	include := func(rel string) string {
		return `include_bytes!(concat!(env!("CARGO_MANIFEST_DIR"), ` + rustString("/"+path.Join(EmbeddedCarriedDir, rel)) + `))`
	}
	adoption := ""
	if slices.ContainsFunc(carried.Databases, func(d EmbeddedCarriedDatabase) bool { return d.Adoption != nil }) {
		adoption = "Adoption, "
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, `//! Code generated by superschematic. DO NOT EDIT.
//!
//! What the binary of environment %s of stack %s carries (D116): the plans
//! of each database, in order, and the site's files. superschematic stack
//! build %s writes it, with the files it names under carried/, before it
//! builds the crate; the stack's build writes it only when it is missing,
//! with nothing carried.

use crate::{%sCarriedDatabase, SiteFile};

/// True when a release recorded the plans the binary carries, and false
/// when it carries the plan of its own build.
pub(crate) const RELEASE: bool = %t;

/// Each database's plans, in order, and its adoption.
`, carried.Environment, carried.Stack, carried.Environment, adoption, carried.Release)
	if len(carried.Databases) == 0 {
		b.WriteString("pub(crate) const DATABASES: &[CarriedDatabase] = &[];\n")
	} else {
		b.WriteString("pub(crate) const DATABASES: &[CarriedDatabase] = &[\n")
		for _, d := range carried.Databases {
			fmt.Fprintf(&b, "    CarriedDatabase {\n        service: %s,\n", rustString(d.Service))
			if len(d.Plans) == 0 {
				b.WriteString("        plans: &[],\n")
			} else {
				b.WriteString("        plans: &[\n")
				for i, plan := range d.Plans {
					rel := path.Join("plans", d.Service, fmt.Sprintf("%04d.json", i+1))
					if err := write(rel, plan); err != nil {
						return fail(err)
					}
					fmt.Fprintf(&b, "            %s,\n", include(rel))
				}
				b.WriteString("        ],\n")
			}
			if d.Adoption == nil {
				b.WriteString("        adoption: None,\n")
			} else {
				rel := path.Join("adoption", d.Service+".json")
				if err := write(rel, d.Adoption.Model); err != nil {
					return fail(err)
				}
				fmt.Fprintf(&b, "        adoption: Some(Adoption {\n            model: %s,\n            check: %s,\n        }),\n", include(rel), rustString(d.Adoption.Check))
			}
			b.WriteString("    },\n")
		}
		b.WriteString("];\n")
	}
	b.WriteString("\n/// The site's files, by path.\n")
	if len(carried.Site) == 0 {
		b.WriteString("pub(crate) const SITE: &[SiteFile] = &[];\n")
	} else {
		b.WriteString("pub(crate) const SITE: &[SiteFile] = &[\n")
		files := slices.Clone(carried.Site)
		slices.SortFunc(files, func(a, b EmbeddedCarriedFile) int { return strings.Compare(a.Path, b.Path) })
		for _, f := range files {
			rel := path.Join("site", f.Path)
			if err := write(rel, f.Content); err != nil {
				return fail(err)
			}
			fmt.Fprintf(&b, "    SiteFile {\n        path: %s,\n        content_type: %s,\n        etag: %s,\n        body: %s,\n    },\n",
				rustString(f.Path), rustString(f.ContentType), rustString(`"`+f.Digest+`"`), include(rel))
		}
		b.WriteString("];\n")
	}
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		return fail(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(EmbeddedCarriedSource)), b.Bytes(), 0o644); err != nil {
		return fail(err)
	}
	return nil
}

// siteContentTypes are the content types the binary answers a site's
// file with, by its extension in lower case. The table is the build's
// own, so a binary is the same whichever machine built it.
var siteContentTypes = map[string]string{
	".avif":        "image/avif",
	".css":         "text/css; charset=utf-8",
	".csv":         "text/csv; charset=utf-8",
	".gif":         "image/gif",
	".htm":         "text/html; charset=utf-8",
	".html":        "text/html; charset=utf-8",
	".ico":         "image/x-icon",
	".jpeg":        "image/jpeg",
	".jpg":         "image/jpeg",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".md":          "text/markdown; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".mp3":         "audio/mpeg",
	".mp4":         "video/mp4",
	".otf":         "font/otf",
	".pdf":         "application/pdf",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".ttf":         "font/ttf",
	".txt":         "text/plain; charset=utf-8",
	".wasm":        "application/wasm",
	".webm":        "video/webm",
	".webmanifest": "application/manifest+json",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
	".xml":         "application/xml",
}

// SiteContentType returns the content type the binary answers the site's
// file at name with: its extension's, or application/octet-stream.
func SiteContentType(name string) string {
	if t, ok := siteContentTypes[strings.ToLower(path.Ext(name))]; ok {
		return t
	}
	return "application/octet-stream"
}

// plural is n and noun, in the plural unless n is one.
func plural(n int, noun string) string {
	if n == 1 {
		return "its " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}
