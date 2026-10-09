// Package servergen writes the entrypoint of a stack's server
// (docs/stack-model.md, sections 8.1, 8.2 and 8.6). A Go server's is a
// module at `<output-root>/server/<stack>/<server>/` holding main.go,
// go.mod and a Dockerfile. main.go loads each served API's config, connects
// one pool per database, builds one SDK client per API called, builds each
// API's implementation from its Deps and mounts every API's routes on one
// handler beside /healthz and /readyz. cloudsql.go beside it connects a
// database through the Cloud SQL connector, on a server that some
// environment places on Cloud SQL. A TypeScript server's is a package at
// the same place, whose main.ts does the same on Bun (typescript.go). The
// generator package plans what each server serves from the stack and the
// APIs' server outputs; this package turns that plan into files.
package servergen

import (
	"bytes"
	"embed"
	"fmt"
	"go/format"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/goutil"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

//go:embed templates/*.tmpl
var templatesFS embed.FS

// GoVersion is the go directive of the modules this package writes and the
// Go image the Dockerfile builds in: tools.env's GO_VERSION, which every
// go.mod in the repository and every generated one states.
const GoVersion = "1.26.4"

// RustVersion is the Rust image the Dockerfile builds the static archives
// in: tools.env's RUST_VERSION, which scripts/superscalar-dep.sh and
// scripts/versiongraph-archive.sh build them with, since archives two Rust
// releases built do not link into one binary.
const RustVersion = "1.99.0"

// The files of an entrypoint module.
const (
	MainFile         = "main.go"
	CloudSQLFile     = "cloudsql.go"
	ModFile          = "go.mod"
	DockerFile       = "Dockerfile"
	DockerIgnoreFile = "Dockerfile.dockerignore"
)

// The versions of the third-party modules main.go imports, those the
// generated API and ORM modules require, and of the Cloud SQL Go connector
// cloudsql.go imports, whose own pgx requirement is pgxVersion.
const (
	chiVersion          = "v5.3.2"
	pgxVersion          = "v5.11.0"
	zapVersion          = "v1.28.0"
	cloudSQLConnVersion = "v1.25.3"
)

// cloudSQLConnModule is the Cloud SQL Go connector's module.
const cloudSQLConnModule = "cloud.google.com/go/cloudsqlconn"

// ZeroVersion is the version a go.mod requires a module at that a replace
// points at a directory, as the generated modules require one another.
const ZeroVersion = "v0.0.0-00010101000000-000000000000"

// Module is a Go module the entrypoint's go.mod requires, and the directory
// a replace points it at.
type Module struct {
	// Path is the module path.
	Path string

	// Version is the version required; ZeroVersion when empty.
	Version string

	// Dir is the module's directory, absolute, or "" for no replace.
	Dir string

	// Pinned replaces every version of the module with Version, which the
	// module proxy serves: a module of the release that generates the
	// server, which no [paths] key names a checkout of. The generated
	// modules require it at a version only a checkout's replace resolves.
	Pinned bool

	// Direct is true for a module main.go imports a package of.
	Direct bool
}

// Implementation is where an API's implementation package lives
// (docs/stack-model.md, section 8.5).
type Implementation struct {
	// Dir is the package's directory, at the naming file's
	// [implementation_paths] go template.
	Dir string

	// Import is the package's import path.
	Import string

	// Module is the path of the module that holds the package, and
	// ModuleDir its directory.
	Module    string
	ModuleDir string
}

// APIInput is one API a server serves.
type APIInput struct {
	// Output is the API's Go server output, with its config, its Deps and
	// the modules its go.mod reaches.
	Output *apigen.APIOutput

	// Implementation is where its implementation lives.
	Implementation Implementation

	// CORS is true when a site of the stack calls the API: its server then
	// answers CORS for the origins its CORS field lists (D55).
	CORS bool
}

// Input is what a server's entrypoint is planned from.
type Input struct {
	// Stack is the stack's name, the service that declares it; Server is
	// the server's name in the stack.
	Stack  string
	Server string

	// Dir is the module's directory, ServerDir.
	Dir string

	// APIs are the APIs the server serves, sorted by service.
	APIs []APIInput

	// Modules are the modules the server's go.mod requires beside the
	// third-party ones main.go imports: the generated modules, the
	// runtime modules and the implementations' modules.
	Modules []Module

	// Naming names the entrypoint's module and the runtime packages
	// main.go imports.
	Naming naming.Naming

	// RepositoryRoot is the Dockerfile's build context: the repository
	// root, or the naming file's [paths] build_context. Every directory a
	// replace points at must lie under it, or no Dockerfile is written.
	RepositoryRoot string

	// ScalarGo is the directory of the scalar library's Go binding, the
	// naming file's [paths] scalar_go. The Dockerfile builds its static
	// archive from the workspace that holds it. Without it the Dockerfile
	// downloads the static archives Release ships, or, from a binary that
	// names none, no Dockerfile is written.
	ScalarGo string

	// Release is the release of superschematic that generates the
	// entrypoint (D47, amended).
	Release release.Release

	// VersionGraphGo is the directory of the version graph's Go binding,
	// when a database the server connects to declares a version graph. The
	// Dockerfile builds its static archive from the crate beside it.
	VersionGraphGo string

	// CloudSQL are the DB services some environment of the stack connects
	// the server to with a Cloud SQL connector configuration. The server
	// links the Cloud SQL connector when one of them is a database it
	// connects to.
	CloudSQL []string

	// manualRoutes counts a manually registered operation among an API's
	// routes, as a router that mounts it does: a TypeScript one.
	manualRoutes bool
	// Job, when set, plans the entrypoint of a job of the one API in APIs
	// rather than a server's (D52): Server is then the job's deployable.
	Job *JobInput
}

// JobInput is the job a job's entrypoint runs.
type JobInput struct {
	// Name is the job's @job class.
	Name string
}

// ServerDir is where the entrypoint of server in stack is written; a job's
// is beside the servers', under the job's deployable name.
func ServerDir(outputRoot, stack, server string) string {
	return filepath.Join(StackDir(outputRoot, stack), server)
}

// StackDir is where the entrypoints of every server of stack are written.
func StackDir(outputRoot, stack string) string {
	return filepath.Join(outputRoot, "server", stack)
}

// ModulePath is the module path of the entrypoint of server in stack.
func ModulePath(n naming.Naming, stack, server string) string {
	return n.OrDefault().GoModuleRoot + "/server/" + stack + "/" + server
}

// Server is a planned entrypoint, what the templates read: a server's, or
// a job's when Job is set.
type Server struct {
	Stack  string
	Name   string
	Module string
	Naming naming.Naming

	// Kind is "server", or "job" for a job's entrypoint; Binary is the
	// name the image builds the binary under.
	Kind   string
	Binary string

	// Job is what a job's entrypoint runs, nil for a server.
	Job *Job

	// GoVersion and RustVersion are the toolchain pins.
	GoVersion   string
	RustVersion string

	APIs      []*API
	Databases []*Database
	Clients   []*Client

	// CloudSQL are the databases, by service and sorted, that some
	// environment places on Cloud SQL. When there is one, cloudsql.go
	// connects them through the Cloud SQL connector, which go.mod
	// requires; with none the server does not link it.
	CloudSQL []string

	// Requires are the go.mod's direct requires and Indirect its
	// indirect ones, sorted by path; Replaces point each module that has
	// a directory at it.
	Requires []Require
	Indirect []Require
	Replaces []Replace

	// Docker is the Dockerfile's plan, nil when none is written, and
	// NoDocker says why.
	Docker   *Docker
	NoDocker string
}

// Pinned reports whether a replace takes a module of the release from the
// module proxy.
func (s *Server) Pinned() bool {
	return slices.ContainsFunc(s.Replaces, func(r Replace) bool { return r.Version != "" })
}

// Job is the job a job's entrypoint runs: its @job class and the method of
// the API's Jobs interface that runs it.
type Job struct {
	Name   string
	Method string
}

// Require is a go.mod require line.
type Require struct {
	Path    string
	Version string
}

// Replace is a go.mod replace line: the module at its directory, relative
// to the module, or, with a Version, the module at that version.
type Replace struct {
	Path    string
	Dir     string
	Version string
}

// API is a served API in main.go.
type API struct {
	Service string
	// Var is the stem of its variables; Package and Impl are the import
	// names of its API package and its implementation.
	Var     string
	Package string
	Impl    string
	Module  string
	Import  string

	// Config is true when the API has an EnvConfig, which Deps holds;
	// Public when its Config takes the database and the auth middleware;
	// Encrypted when it takes a payload decryptor; ServiceAuth when it
	// takes a service authenticator, which an operation's service clause
	// needs (D37); Identity when it authenticates with the identity
	// runtime over the user model (D50), so its Config takes the identity
	// service, built over its database's identity store, in place of an
	// auth middleware.
	Config      bool
	Public      bool
	Encrypted   bool
	ServiceAuth bool
	Identity    bool

	// CORS is true when a site of the stack calls the API, whose server
	// then answers CORS for the origins its CORS field lists, with
	// CORSMethods, the methods of its operations, sorted (D55).
	CORS        bool
	CORSMethods []string

	// Database is the ORM Deps.DB holds, nil without one.
	Database *Database

	// Calls are Deps' clients.
	Calls []Call
}

// Call is a client in an API's Deps.
type Call struct {
	Field  string
	Client *Client
}

// Database is a database the server connects to once, for every API on it.
type Database struct {
	Service string
	Var     string
	Package string
	Module  string

	// Identity is true when an API on the database authenticates with
	// the identity runtime: the server builds one identity store over
	// the database's pool, from the descriptor constant of its Go types,
	// whose import name and module are Types and TypesModule.
	Identity    bool
	Types       string
	TypesModule string

	// Field is the derived config field the connection is read from, and
	// From the expression that reads it from the first API's config.
	Field string
	From  string
}

// Client is an SDK client of an API called, built once for every API
// that calls it, by the function Constructor names.
type Client struct {
	Service     string
	Var         string
	Constructor string
	Package     string
	Module      string
	Type        string

	// From is the expression that reads the callee's endpoint from the
	// first calling API's config.
	From string
}

// Docker is a Dockerfile's plan. Paths are relative to the repository
// root, slash-separated.
type Docker struct {
	// Dockerfile is the Dockerfile's path, Context the module's directory.
	Dockerfile string
	Context    string

	// ScalarGo is the scalar library's Go binding and ScalarWorkspace the
	// Cargo workspace that holds it.
	ScalarGo        string
	ScalarWorkspace string

	// VersionGraphGo is the version graph's Go binding and
	// VersionGraphCrate its crate, empty when no database declares one.
	VersionGraphGo    string
	VersionGraphCrate string

	// Archives, when set, are the static archives of the release the
	// build stage downloads, in place of the stages that build them from
	// checkouts.
	Archives *Archives

	// Include are the paths the build context holds.
	Include []string
}

// Archives are a release's static archives for the platforms an image
// builds for, which the Dockerfile downloads.
type Archives struct {
	// Version is the release.
	Version string

	// Base is where the release's assets download from.
	Base string

	// Platforms are the image's platforms, by GOARCH.
	Platforms []ArchivePlatform
}

// ArchivePlatform is the archives tarball for linux and one GOARCH, and
// its hex SHA-256.
type ArchivePlatform struct {
	GOARCH string
	Name   string
	SHA256 string
}

// archiveArches are the architectures an image builds for, linux's.
var archiveArches = []string{"amd64", "arm64"}

// reserved are the names main.go and cloudsql.go declare or import beside
// the served APIs' own: an API, database or client never takes one.
var reserved = []string{
	"chi", "chimiddleware", "context", "dependency", "dispatch", "draining", "err", "errors", "fmt", "handler", "http", "json",
	"logger", "main", "net", "newHandler", "os", "pgxpool", "run", "runtimemiddleware", "serve", "served", "serviceCredential",
	"signal", "stackconfig", "stop", "atomic", "syscall", "time", "writeJSON", "zap", "connect", "ctx", "api", "apis",
	"serviceauth", "serviceAuthenticator", "endpoint", "cfg", "token", "headers",
	"connectCloudSQL", "cloudSQLDialer", "cloudSQLDial", "cloudSQLConfig",
	"identity", "identityConfig", "stdlib", "method", "requested",
	"jobs", "started",
}

// names hands out identifiers no other declaration of main.go takes.
type names map[string]bool

func newNames() names {
	n := names{}
	for _, name := range reserved {
		n[name] = true
	}
	return n
}

// take returns want, or want with the lowest number from 2 that makes it
// unique, and records it.
func (n names) take(want string) string {
	name := want
	for i := 2; n[name] || token.IsKeyword(name); i++ {
		name = fmt.Sprintf("%s%d", want, i)
	}
	n[name] = true
	return name
}

// varStem is a service's name as the stem of a Go variable: shop-orders is
// shopOrders.
func varStem(service string) string {
	public := goutil.GoPublicIdentifier(service)
	return strings.ToLower(public[:1]) + public[1:]
}

// packageName is a service's name as a Go package name: shop-orders is
// shoporders.
func packageName(service string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(service) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		}
	}
	name := b.String()
	if name == "" || (name[0] >= '0' && name[0] <= '9') {
		name = "s" + name
	}
	return name
}

// Plan plans the entrypoint of one server. It refuses a server whose APIs
// register one method and path between them, since one router answers
// each once, and an API whose generated Config cannot be filled. With
// in.Job it plans a job's entrypoint instead (D52): one API, the job's,
// whose Deps it builds as a server does, and no routes.
func Plan(in Input) (*Server, error) {
	s := &Server{
		Stack:       in.Stack,
		Name:        in.Server,
		Module:      ModulePath(in.Naming, in.Stack, in.Server),
		Naming:      in.Naming.OrDefault(),
		Kind:        "server",
		Binary:      "server",
		GoVersion:   GoVersion,
		RustVersion: RustVersion,
	}
	switch {
	case in.Job != nil:
		if len(in.APIs) != 1 {
			return nil, fmt.Errorf("stack %s: job %s runs a job of %d APIs; a job belongs to one", in.Stack, in.Server, len(in.APIs))
		}
		o := in.APIs[0].Output
		i := slices.IndexFunc(o.Jobs, func(j apigen.JobInfo) bool { return j.Name == in.Job.Name })
		if i < 0 {
			return nil, fmt.Errorf("stack %s: job %s runs %s of %s, which declares no such job", in.Stack, in.Server, in.Job.Name, o.SchemaName)
		}
		s.Kind, s.Binary = "job", "job"
		s.Job = &Job{Name: o.Jobs[i].Name, Method: o.Jobs[i].Method}
	case len(in.APIs) == 0:
		return nil, fmt.Errorf("stack %s: server %s serves no API", in.Stack, in.Server)
	default:
		if err := checkRoutes(in); err != nil {
			return nil, err
		}
	}
	taken := newNames()
	apis := make([]*API, len(in.APIs))
	for i, a := range in.APIs {
		o := a.Output
		stem := taken.take(varStem(o.SchemaName))
		pkg := taken.take(packageName(o.SchemaName))
		apis[i] = &API{
			Service: o.SchemaName,
			Var:     stem,
			Package: pkg,
			Impl:    taken.take(pkg + "impl"),
			Module:  o.ModulePath,
			Import:  a.Implementation.Import,
			Config:  o.HasEnvConfig(),
		}
		if s.Job == nil {
			// A job serves no request: it mounts no routes, verifies no end
			// user or caller, and decrypts no payload. So it builds no
			// identity service either, though its API's authDb holds the
			// user model (D50): its Deps reach the tables through the ORM.
			apis[i].Public = o.IsPublic
			apis[i].Encrypted = o.HasEncryptedEndpoints
			apis[i].ServiceAuth = o.HasServiceCallers
			apis[i].Identity = o.Auth.Identity
			if a.CORS {
				apis[i].CORS = true
				apis[i].CORSMethods = endpointMethods(o)
			}
		}
	}
	databases := map[string]*Database{}
	clients := map[string]*Client{}
	for i, a := range in.APIs {
		o, api := a.Output, apis[i]
		derived := map[string]string{}
		if o.EnvConfig != nil {
			for _, f := range o.EnvConfig.Derived {
				derived[string(f.Kind)+" "+f.Service] = f.GoName
				if f.Kind == ir.EdgeSQL {
					derived["field "+f.Service] = f.Key
				}
			}
		}
		if o.IsPublic && o.Deps.Database != o.UpstreamSchema {
			return nil, fmt.Errorf("stack %s: server %s serves %s, whose Deps database %q is not its auth database %q", in.Stack, in.Server, o.SchemaName, o.Deps.Database, o.UpstreamSchema)
		}
		if db := o.Deps.Database; db != "" {
			d := databases[db]
			if d == nil {
				goName, ok := derived[string(ir.EdgeSQL)+" "+db]
				if !ok || !api.Config {
					return nil, fmt.Errorf("stack %s: server %s serves %s, whose config has no field for its database %s", in.Stack, in.Server, o.SchemaName, db)
				}
				stem := taken.take(varStem(db))
				d = &Database{
					Service: db,
					Var:     stem,
					Package: taken.take(packageName(db) + "orm"),
					Module:  o.Deps.ORMModule,
					Field:   derived["field "+db],
					From:    api.Var + "Config." + goName,
				}
				databases[db] = d
				s.Databases = append(s.Databases, d)
				if slices.Contains(in.CloudSQL, db) {
					s.CloudSQL = append(s.CloudSQL, db)
				}
			}
			api.Database = d
		}
		if api.Identity {
			d := api.Database
			if d == nil {
				return nil, fmt.Errorf("stack %s: server %s serves %s, which authenticates with the identity runtime and has no database to build its identity store over", in.Stack, in.Server, o.SchemaName)
			}
			if !d.Identity {
				d.Identity = true
				d.Types = taken.take(packageName(d.Service) + "types")
				d.TypesModule = in.Naming.OrDefault().GoTypesModule(d.Service)
			}
		}
		for _, call := range o.Deps.Calls {
			c := clients[call.Service]
			if c == nil {
				goName, ok := derived[string(ir.EdgeHTTP)+" "+call.Service]
				if !ok || !api.Config {
					return nil, fmt.Errorf("stack %s: server %s serves %s, whose config has no field for %s, which it calls", in.Stack, in.Server, o.SchemaName, call.Service)
				}
				c = &Client{
					Service:     call.Service,
					Var:         taken.take(varStem(call.Service) + "Client"),
					Constructor: taken.take("new" + goutil.GoPublicIdentifier(call.Service) + "Client"),
					Package:     taken.take(packageName(call.Service) + "sdk"),
					Module:      call.Module,
					Type:        call.Client,
					From:        api.Var + "Config." + goName,
				}
				clients[call.Service] = c
				s.Clients = append(s.Clients, c)
			}
			api.Calls = append(api.Calls, Call{Field: call.Field, Client: c})
		}
	}
	s.APIs = apis
	slices.Sort(s.CloudSQL)
	dir, err := filepath.Abs(in.Dir)
	if err != nil {
		return nil, err
	}
	if err := s.planModule(in, dir); err != nil {
		return nil, err
	}
	s.Docker, s.NoDocker, err = planDocker(in, dir)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// planModule plans go.mod: the third-party modules main.go and
// cloudsql.go import, then in.Modules, each replaced by its directory.
func (s *Server) planModule(in Input, dir string) error {
	direct := []Require{{"go.uber.org/zap", zapVersion}}
	if s.Job == nil {
		direct = append(direct, Require{"github.com/go-chi/chi/v5", chiVersion})
	}
	if len(s.Databases) > 0 {
		direct = append(direct, Require{"github.com/jackc/pgx/v5", pgxVersion})
	}
	if len(s.CloudSQL) > 0 {
		direct = append(direct, Require{cloudSQLConnModule, cloudSQLConnVersion})
	}
	seen := map[string]bool{}
	for _, r := range direct {
		seen[r.Path] = true
	}
	var indirect []Require
	for _, m := range in.Modules {
		if seen[m.Path] {
			continue
		}
		seen[m.Path] = true
		version := m.Version
		if version == "" {
			version = ZeroVersion
		}
		if m.Direct {
			direct = append(direct, Require{m.Path, version})
		} else {
			indirect = append(indirect, Require{m.Path, version})
		}
		if m.Pinned {
			s.Replaces = append(s.Replaces, Replace{Path: m.Path, Version: version})
			continue
		}
		if m.Dir == "" {
			continue
		}
		rel, err := naming.RelPath(dir, m.Dir)
		if err != nil {
			return err
		}
		s.Replaces = append(s.Replaces, Replace{Path: m.Path, Dir: rel})
	}
	byPath := func(a, b Require) int { return strings.Compare(a.Path, b.Path) }
	slices.SortFunc(direct, byPath)
	slices.SortFunc(indirect, byPath)
	slices.SortFunc(s.Replaces, func(a, b Replace) int { return strings.Compare(a.Path, b.Path) })
	s.Requires, s.Indirect = direct, indirect
	return nil
}

// planDocker plans the Dockerfile, or says why there is none: every
// directory the build reads must lie under the repository root, the build
// context. The static archives come from the checkout of the scalar
// library's Go binding the naming file names, which the image builds
// them from, or, without one, from the release, which ships them.
func planDocker(in Input, dir string) (*Docker, string, error) {
	if in.RepositoryRoot == "" {
		return nil, "no repository root to take as the build context", nil
	}
	var archives *Archives
	if in.ScalarGo == "" {
		var why string
		if archives, why = releaseArchives(in.Release); archives == nil {
			return nil, why, nil
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
	d := &Docker{}
	var ok bool
	if d.Context, ok = rel(dir); !ok {
		return nil, fmt.Sprintf("the output root holding %s lies outside the repository root %s, the build context", dir, root), nil
	}
	d.Dockerfile = path.Join(d.Context, DockerFile)
	include := []string{d.Context}
	if archives != nil {
		d.Archives = archives
		if in.VersionGraphGo != "" {
			if d.VersionGraphGo, ok = rel(in.VersionGraphGo); !ok {
				return nil, fmt.Sprintf("the version graph's Go binding %s lies outside the repository root %s, the build context", in.VersionGraphGo, root), nil
			}
		}
		return finish(d, in, root, include, rel)
	}
	if d.ScalarGo, ok = rel(in.ScalarGo); !ok {
		return nil, fmt.Sprintf("superscalar's Go binding %s lies outside the repository root %s, the build context", in.ScalarGo, root), nil
	}
	d.ScalarWorkspace = path.Dir(d.ScalarGo)
	include = append(include, d.ScalarGo, d.ScalarWorkspace+"/Cargo.toml", d.ScalarWorkspace+"/Cargo.lock", d.ScalarWorkspace+"/crates")
	if in.VersionGraphGo != "" {
		if d.VersionGraphGo, ok = rel(in.VersionGraphGo); !ok {
			return nil, fmt.Sprintf("the version graph's Go binding %s lies outside the repository root %s, the build context", in.VersionGraphGo, root), nil
		}
		d.VersionGraphCrate = path.Join(path.Dir(d.VersionGraphGo), "rust")
		include = append(include, d.VersionGraphGo, d.VersionGraphCrate+"/Cargo.toml", d.VersionGraphCrate+"/Cargo.lock", d.VersionGraphCrate+"/src")
	}
	return finish(d, in, root, include, rel)
}

// finish adds the directory of every module a replace points at to the
// paths the context holds, include, and sets d.Include, or says which
// module lies outside the context, root, and plans no Dockerfile.
func finish(d *Docker, in Input, root string, include []string, rel func(string) (string, bool)) (*Docker, string, error) {
	for _, m := range in.Modules {
		if m.Dir == "" {
			continue
		}
		r, ok := rel(m.Dir)
		if !ok {
			return nil, fmt.Sprintf("module %s, at %s, lies outside the repository root %s, the build context", m.Path, m.Dir, root), nil
		}
		include = append(include, r)
	}
	slices.Sort(include)
	d.Include = slices.Compact(include)
	return d, "", nil
}

// releaseArchives are the static archives of r for the platforms an image
// builds for, or nil and why the image cannot take them: a binary built
// from a checkout is no release, and one its release workflow did not
// build names no digests.
func releaseArchives(r release.Release) (*Archives, string) {
	const unset = "the naming file's [paths] scalar_go is unset, so the image links the static archives superschematic's release ships"
	if r.Version == "" {
		return nil, unset + ", and this superschematic is built from a checkout, which is no release; set [paths] scalar_go to a superscalar checkout to build them from"
	}
	a := &Archives{Version: r.Version, Base: strings.TrimSuffix(release.DownloadURL(r.Version, ""), "/")}
	for _, arch := range archiveArches {
		platform := release.Platform("linux", arch)
		digest, ok := r.Archive(platform)
		if !ok {
			return nil, fmt.Sprintf("%s, and this superschematic %s, which its release workflow did not build, names no digest of them for %s", unset, r.Version, platform)
		}
		a.Platforms = append(a.Platforms, ArchivePlatform{GOARCH: arch, Name: release.ArchiveName(r.Version, platform), SHA256: digest})
	}
	return a, ""
}

// routeParam matches a path parameter, which a router matches whatever its
// name.
var routeParam = regexp.MustCompile(`\{[^}]*\}`)

// checkRoutes refuses two served APIs that register one method and path:
// one router answers each once. A manually registered operation is the
// implementation's to route on a Go server, so it is left out there, as
// apigen leaves it out of its own collision check; a TypeScript router
// mounts it (manualRoutes).
func checkRoutes(in Input) error {
	type route struct{ method, path string }
	owner := map[route]string{}
	declared := map[route]string{}
	var problems []string
	for _, a := range in.APIs {
		for _, ep := range a.Output.Endpoints {
			if ep.ManualRouteRegistration && !in.manualRoutes {
				continue
			}
			key := route{ep.Method, routeParam.ReplaceAllString(ep.Path, "{}")}
			prev, clash := owner[key]
			if !clash {
				owner[key] = a.Output.SchemaName
				declared[key] = ep.Path
				continue
			}
			if prev == a.Output.SchemaName {
				continue
			}
			problems = append(problems, fmt.Sprintf("%s registers %s %s and %s registers %s %s", prev, ep.Method, declared[key], a.Output.SchemaName, ep.Method, ep.Path))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("stack %s: server %s serves APIs that register one route twice, and one router answers each method and path once: %s; serve them from separate servers or change a path", in.Stack, in.Server, strings.Join(problems, "; "))
	}
	return nil
}

// Write writes the entrypoint module into dir: main.go, go.mod,
// cloudsql.go when some environment places a database of the server on
// Cloud SQL, and the Dockerfile with its ignore file when s.Docker is
// planned.
//
// A job's module holds the same files but serviceauth.go and identity.go,
// and its main.go runs the job (job.go.tmpl). Both main.go templates take
// the wiring they share from wiring.tmpl.
func Write(s *Server, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("servergen: %w", err)
	}
	main := "main.go.tmpl"
	if s.Job != nil {
		main = "job.go.tmpl"
	}
	files := []struct{ template, name string }{
		{main, MainFile},
		{"go.mod.tmpl", ModFile},
	}
	if len(s.CloudSQL) > 0 {
		files = append(files, struct{ template, name string }{"cloudsql.go.tmpl", CloudSQLFile})
	}
	if s.Docker != nil {
		files = append(files, struct{ template, name string }{"Dockerfile.tmpl", DockerFile}, struct{ template, name string }{"dockerignore.tmpl", DockerIgnoreFile})
	}
	for _, f := range files {
		if err := render(f.template, filepath.Join(dir, f.name), s); err != nil {
			return fmt.Errorf("servergen: %s %s: %w", s.Kind, s.Name, err)
		}
	}
	if err := writeServiceAuth(s, dir); err != nil {
		return err
	}
	if err := writeIdentity(s, dir); err != nil {
		return err
	}
	return writeCORS(s, dir)
}

// templates is every template of the package parsed into one set, so that
// main.go's templates execute the named templates wiring.tmpl defines.
var templates = sync.OnceValues(func() (*template.Template, error) {
	return template.New("servergen").Funcs(codegen.BaseTemplateFuncs()).Funcs(templateFuncs()).ParseFS(templatesFS, "templates/*.tmpl")
})

// render executes the template named name with data into path, formatting
// a Go file with gofmt.
func render(name, path string, data any) error {
	set, err := templates()
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := set.ExecuteTemplate(&buf, name, data); err != nil {
		return err
	}
	out := buf.Bytes()
	if strings.HasSuffix(path, ".go") {
		formatted, err := format.Source(out)
		if err != nil {
			_ = os.WriteFile(path, out, 0o644)
			return fmt.Errorf("format %s: %w (unformatted output written for debugging)", path, err)
		}
		out = formatted
	}
	return os.WriteFile(path, out, 0o644)
}

// ImplementationModule is the module the scaffold of an implementation
// writes beside it when no go.mod holds the package.
type ImplementationModule struct {
	Service  string
	Path     string
	Requires []Require
	Indirect []Require
	Replaces []Replace
}

// ImplementationModulePath is the module path of the implementation of
// service when its scaffold writes its module.
func ImplementationModulePath(n naming.Naming, service string) string {
	return n.OrDefault().GoModuleRoot + "/implementation/" + service
}

// WriteImplementationModule writes go.mod into the implementation package
// impl.Dir, making it a module of its own whose requires and replaces are
// modules, its API module the direct one, so the package builds and tests
// on its own. The caller writes it once, beside a scaffold it just wrote.
func WriteImplementationModule(n naming.Naming, service string, impl Implementation, apiModule string, modules []Module) error {
	m := ImplementationModule{Service: service, Path: impl.Module}
	seen := map[string]bool{}
	for _, mod := range modules {
		if seen[mod.Path] {
			continue
		}
		seen[mod.Path] = true
		version := mod.Version
		if version == "" {
			version = ZeroVersion
		}
		if mod.Path == apiModule {
			m.Requires = append(m.Requires, Require{mod.Path, version})
		} else {
			m.Indirect = append(m.Indirect, Require{mod.Path, version})
		}
		if mod.Pinned {
			m.Replaces = append(m.Replaces, Replace{Path: mod.Path, Version: version})
			continue
		}
		if mod.Dir == "" {
			continue
		}
		rel, err := naming.RelPath(impl.Dir, mod.Dir)
		if err != nil {
			return err
		}
		m.Replaces = append(m.Replaces, Replace{Path: mod.Path, Dir: rel})
	}
	slices.SortFunc(m.Indirect, func(a, b Require) int { return strings.Compare(a.Path, b.Path) })
	slices.SortFunc(m.Replaces, func(a, b Replace) int { return strings.Compare(a.Path, b.Path) })
	gen := codegen.NewFileGenerator(templatesFS, templateFuncs())
	cfg := codegen.NewFileConfig(templatesFS, "implementation.go.mod.tmpl", filepath.Join(impl.Dir, ModFile), struct {
		ImplementationModule
		GoVersion string
	}{m, GoVersion}, nil)
	if err := gen.GenerateFile(cfg); err != nil {
		return fmt.Errorf("servergen: implementation module of %s: %w", service, err)
	}
	return nil
}

// moduleLine reads a go.mod's module path.
var moduleLine = regexp.MustCompile(`(?m)^\s*module\s+("?)([^\s"]+)("?)\s*(//.*)?$`)

// FindModule returns the module that holds dir: the nearest go.mod at dir
// or above it, up to root and no further. found is false when none does.
func FindModule(dir, root string) (module, moduleDir string, found bool, err error) {
	dir, err = filepath.Abs(dir)
	if err != nil {
		return "", "", false, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", "", false, err
	}
	for cur := dir; ; cur = filepath.Dir(cur) {
		data, err := os.ReadFile(filepath.Join(cur, ModFile))
		if err == nil {
			match := moduleLine.FindSubmatch(data)
			if match == nil {
				return "", "", false, fmt.Errorf("%s names no module", filepath.Join(cur, ModFile))
			}
			return string(match[2]), cur, true, nil
		}
		if !os.IsNotExist(err) {
			return "", "", false, err
		}
		if cur == root || filepath.Dir(cur) == cur {
			return "", "", false, nil
		}
		if rel, err := filepath.Rel(root, cur); err != nil || strings.HasPrefix(rel, "..") {
			return "", "", false, nil
		}
	}
}

// ImportPath is the import path of the package at dir in the module at
// moduleDir named module.
func ImportPath(module, moduleDir, dir string) (string, error) {
	rel, err := filepath.Rel(moduleDir, dir)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return module, nil
	}
	return module + "/" + filepath.ToSlash(rel), nil
}

func templateFuncs() template.FuncMap {
	return template.FuncMap{
		"quote":      func(s string) string { return fmt.Sprintf("%q", s) },
		"capitalize": func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
		"serviceList": func(apis []*API) string {
			names := make([]string, len(apis))
			for i, a := range apis {
				names[i] = a.Service
			}
			switch len(names) {
			case 1:
				return names[0]
			case 2:
				return names[0] + " and " + names[1]
			}
			return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
		},
		"anyPrivate": func(apis []*API) bool {
			return slices.ContainsFunc(apis, func(a *API) bool { return !a.Public })
		},
		"anyServiceAuth": func(apis []*API) bool {
			return slices.ContainsFunc(apis, func(a *API) bool { return a.ServiceAuth })
		},
		"anyIdentity": func(apis []*API) bool {
			return slices.ContainsFunc(apis, func(a *API) bool { return a.Identity })
		},
		"anyCORS": func(apis []*API) bool {
			return slices.ContainsFunc(apis, func(a *API) bool { return a.CORS })
		},
		"tsQuote": func(s string) string {
			return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`, "\n", `\n`).Replace(s) + "'"
		},
		"join": strings.Join,
		"tsServiceList": func(apis []*TypeScriptAPI) string {
			names := make([]string, len(apis))
			for i, a := range apis {
				names[i] = a.Service
			}
			return joinNames(names)
		},
	}
}

// joinNames joins names as a sentence lists them: a, b and c.
func joinNames(names []string) string {
	switch len(names) {
	case 1:
		return names[0]
	case 2:
		return names[0] + " and " + names[1]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}
