package embedded_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/embedded"
)

func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), embedded.Extension{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// The services of the notes app: notes-db lists sqlite first; notes-api,
// a Rust API, is on it and calls audit-api, a Rust API of its own server,
// whose one operation admits notes-api alone; notes-web is a site whose
// page calls notes-api.
var (
	notesDB  = ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB}
	notesAPI = ir.ServiceRef{Name: "notes-api", Kind: ir.SchemaKindAPI}
	auditAPI = ir.ServiceRef{Name: "audit-api", Kind: ir.SchemaKindAPI}
	notesWeb = ir.ServiceRef{Name: "notes-web", Kind: ir.SchemaKindSite}
)

func services() []stack.Service {
	return []stack.Service{
		{Name: notesDB.Name, Kind: ir.SchemaKindDB, Dialects: []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}},
		{
			Name:       notesAPI.Name,
			Kind:       ir.SchemaKindAPI,
			AuthDB:     &notesDB,
			Calls:      []ir.ServiceRef{auditAPI},
			Language:   registry.APILanguageRust,
			Operations: []stack.Operation{{Name: "Notes.list"}},
		},
		{
			Name:     auditAPI.Name,
			Kind:     ir.SchemaKindAPI,
			AuthDB:   &notesDB,
			Language: registry.APILanguageRust,
			Operations: []stack.Operation{{
				Name:           "Audit.record",
				ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{notesAPI.Name}},
			}},
		},
		{
			Name:  notesWeb.Name,
			Kind:  ir.SchemaKindSite,
			Calls: []ir.ServiceRef{notesAPI},
			Site:  &ir.ResolvedSite{Dir: "web/notes-web", Build: "build", Output: "dist", Fallback: "index.html"},
		},
	}
}

// notes is the stack: both APIs and the site, whose origin serves the API
// its page calls (D91), in one environment on the embedded target.
func notes() *ir.Stack {
	return &ir.Stack{
		Name:         "Notes",
		Deploy:       []ir.ServiceRef{notesAPI, auditAPI, notesWeb},
		Origins:      []*ir.Origin{{Site: notesWeb}},
		Environments: []*ir.Environment{{Name: "Desktop", Target: embedded.Target}},
	}
}

func resolve(t *testing.T, s *ir.Stack, services []stack.Service) (*ir.ResolvedEnvironment, *stack.Errors) {
	t.Helper()
	env, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: services, Environment: "Desktop"})
	if err == nil {
		return env, nil
	}
	var errs *stack.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("resolve: %v", err)
	}
	return nil, errs
}

func mustResolve(t *testing.T, s *ir.Stack, services []stack.Service) *ir.ResolvedEnvironment {
	t.Helper()
	env, errs := resolve(t, s, services)
	if errs != nil {
		t.Fatalf("resolve: %v", errs)
	}
	return env
}

// mustFail asserts resolution failed with code, in a message that holds
// every part.
func mustFail(t *testing.T, errs *stack.Errors, code stack.Code, parts ...string) {
	t.Helper()
	if errs == nil {
		t.Fatalf("resolution succeeded, want a %s failure holding %q", code, parts)
	}
	for _, err := range errs.Of(code) {
		matched := true
		for _, part := range parts {
			if !strings.Contains(err.Message, part) {
				matched = false
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("no %s failure holds %q:\n%v", code, parts, errs)
}

func service(services []stack.Service, name string) *stack.Service {
	for i := range services {
		if services[i].Name == name {
			return &services[i]
		}
	}
	return nil
}

// TestAnEnvironmentIsOneBinary: every server of an embedded environment
// runs inside its one binary, each database is a file under the data
// directory, laid out as `stack dev` lays them out, and the site and the
// APIs share one origin (D116, D91).
func TestAnEnvironmentIsOneBinary(t *testing.T) {
	env := mustResolve(t, notes(), services())
	if !env.IsBinary() {
		t.Fatalf("the environment builds no binary")
	}
	if env.Provisioner != "" {
		t.Errorf("provisioner = %q, want none: the target rolls nothing out", env.Provisioner)
	}
	for _, name := range []string{notesAPI.Name, auditAPI.Name} {
		d := env.Deployable(name)
		if d == nil || d.Platform != embedded.Process || d.Artifact != ir.ArtifactBinary || !d.InBinary() || d.HasImage() {
			t.Errorf("server %s = %+v, want it on %s inside the binary, with no image", name, d, embedded.Process)
		}
	}
	db := env.Deployable(notesDB.Name)
	if db == nil || db.Platform != embedded.SQLite || db.Dialect != registry.SQLDialectSQLite {
		t.Fatalf("database = %+v, want it on %s as sqlite", db, embedded.SQLite)
	}
	var file string
	for _, res := range env.Resources.Resources {
		if res.Type == embedded.TypeDatabase {
			file, _ = res.Properties["file"].(string)
		}
	}
	if file != "databases/notes_db.sqlite" {
		t.Errorf("the database's file = %q, want databases/notes_db.sqlite", file)
	}
	site := env.Deployable(notesWeb.Name)
	if site == nil || site.Platform != embedded.Assets {
		t.Fatalf("site = %+v, want it on %s", site, embedded.Assets)
	}
	if got, want := site.PublicAddress, "http://127.0.0.1:8080"; got != want {
		t.Errorf("the site's origin = %v, want %s", got, want)
	}
	var endpoint map[string]any
	for _, b := range site.Bindings {
		if b.Field == notesAPI.Name {
			endpoint, _ = b.Value.(map[string]any)
		}
	}
	if got, want := endpoint["url"], "http://127.0.0.1:8080/api/notes-api"; got != want {
		t.Errorf("the site reaches notes-api at %v, want %s: the API's path on the binary's one origin", got, want)
	}
}

// TestThePortIsTheEnvironments: the binary's origin is at the port the
// environment's values name.
func TestThePortIsTheEnvironments(t *testing.T) {
	s := notes()
	s.Environments[0].Values = map[string]any{"port": 4123}
	env := mustResolve(t, s, services())
	if got, want := env.Deployable(notesWeb.Name).PublicAddress, "http://127.0.0.1:4123"; got != want {
		t.Errorf("the site's origin = %v, want %s", got, want)
	}
}

// TestACallBetweenServersCarriesTheCallersLoopbackKey: a call from one
// server of the binary to another gives the callee the caller as an
// issuer, whose one caller serves the caller's APIs, so the callee's
// service clause holds as it does between two servers (D116; section 9.9
// of docs/stack-model.md).
func TestACallBetweenServersCarriesTheCallersLoopbackKey(t *testing.T) {
	env := mustResolve(t, notes(), services())
	audit := env.Deployable(auditAPI.Name)
	var callers *ir.Binding
	for _, b := range audit.Bindings {
		if b.CallersOf == auditAPI.Name {
			callers = b
		}
	}
	if callers == nil {
		t.Fatalf("audit-api has no callers field: %+v", audit.Bindings)
	}
	data, err := stack.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"issuer": "notes-api"`, `"deployable": "notes-api"`, `"notes-api.loopback"`, `"http://127.0.0.1:8080/api/audit-api"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("the resolved environment lacks %s", want)
		}
	}
	keys := 0
	for _, res := range env.Resources.Resources {
		if res.Type == embedded.TypeLoopback {
			keys++
		}
	}
	if keys != 1 {
		t.Errorf("%d loopback keys, want the caller's one", keys)
	}
}

// TestAWorkerRunsInsideTheBinary: a Rust worker of an API of the
// environment is placed on embedded.worker, inside the binary, and
// reaches its queue's database through the file (D116).
func TestAWorkerRunsInsideTheBinary(t *testing.T) {
	svcs := services()
	service(svcs, notesDB.Name).Queues = []string{"NoteSaved"}
	service(svcs, notesAPI.Name).Workers = []ir.Worker{{Name: "IndexNotes", Queue: "NoteSaved"}}
	env := mustResolve(t, notes(), svcs)
	var worker *ir.ResolvedDeployable
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableWorker {
			worker = d
		}
	}
	if worker == nil || worker.Platform != embedded.Worker || !worker.InBinary() || worker.Worker == nil || worker.Worker.Instances != 1 {
		t.Fatalf("worker = %+v, want one instance on %s inside the binary", worker, embedded.Worker)
	}
	var url any
	for _, b := range worker.Bindings {
		if value, ok := b.Value.(map[string]any); ok && b.Source == ir.BindingDerived && value["url"] != nil {
			url = value["url"]
		}
	}
	if url != "sqlite:databases/notes_db.sqlite" {
		t.Errorf("the worker reaches its queue at %v, want the database's file under the data directory", url)
	}
}

// TestAnAPIOverTheUserModelGetsACookieWithoutSecure: the binary serves
// plain HTTP on the machine it is installed on, so the target binds the
// identity config of an API over the user model to a session cookie
// without Secure unless the environment sets the field (D50, D116).
func TestAnAPIOverTheUserModelGetsACookieWithoutSecure(t *testing.T) {
	svcs := services()
	service(svcs, notesAPI.Name).Identity = true
	env := mustResolve(t, notes(), svcs)
	var identity *ir.Binding
	for _, b := range env.Deployable(notesAPI.Name).Bindings {
		if b.IdentityOf == notesAPI.Name {
			identity = b
		}
	}
	if identity == nil || identity.Source != ir.BindingLiteral {
		t.Fatalf("notes-api's identity config field is not bound to a literal: %+v", identity)
	}
	data, err := json.Marshal(identity.Value)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); !strings.Contains(got, `secure`) || !strings.Contains(got, `false`) {
		t.Errorf("the identity config = %s, want a cookie without Secure", got)
	}
}

// TestResolutionRefusesWhatTheBinaryCannotHold: each thing an embedded
// environment may not hold is refused by name (D116).
func TestResolutionRefusesWhatTheBinaryCannotHold(t *testing.T) {
	t.Run("a Go server", func(t *testing.T) {
		svcs := services()
		service(svcs, auditAPI.Name).Language = registry.APILanguageGo
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "server audit-api is a GO server, and platform embedded.process runs only RUST")
	})
	t.Run("a TypeScript server", func(t *testing.T) {
		svcs := services()
		service(svcs, auditAPI.Name).Language = registry.APILanguageTypeScript
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "server audit-api is a TYPESCRIPT server, and platform embedded.process runs only RUST")
	})
	t.Run("a database with no sqlite dialect", func(t *testing.T) {
		svcs := services()
		service(svcs, notesDB.Name).Dialects = []string{registry.SQLDialectPostgres}
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "database notes-db hosts notes-db (postgres), and platform embedded.sqlite runs only sqlite")
	})
	t.Run("a bucket", func(t *testing.T) {
		svcs := append(services(), stack.Service{Name: "notes-files", Kind: ir.SchemaKindBucket})
		service(svcs, notesAPI.Name).Buckets = []ir.ServiceRef{{Name: "notes-files", Kind: ir.SchemaKindBucket}}
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "bucket notes-files: target embedded places no bucket", "the binary keeps no bucket")
	})
	t.Run("a job", func(t *testing.T) {
		svcs := services()
		service(svcs, notesAPI.Name).Jobs = []ir.Job{{Name: "Tidy", Schedule: "0 * * * *"}}
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "target embedded places no job", "it runs no job")
	})
	t.Run("a memory store", func(t *testing.T) {
		svcs := append(services(), stack.Service{Name: "notes-cache", Kind: ir.SchemaKindMemoryStore})
		service(svcs, notesAPI.Name).MemoryStores = []ir.ServiceRef{{Name: "notes-cache", Kind: ir.SchemaKindMemoryStore}}
		_, errs := resolve(t, notes(), svcs)
		mustFail(t, errs, stack.CodeUnrealizable, "memoryStore notes-cache: target embedded places no memoryStore", "in-process store is not wired into it yet", "enabled: false")
	})
	t.Run("more than one instance of a worker", func(t *testing.T) {
		svcs := services()
		service(svcs, notesDB.Name).Queues = []string{"NoteSaved"}
		service(svcs, notesAPI.Name).Workers = []ir.Worker{{Name: "IndexNotes", Queue: "NoteSaved"}}
		s := notes()
		three := 3
		s.Environments[0].Settings = []*ir.DeployableSettings{{
			Of:        ir.DeployableRef{Service: &notesAPI, Worker: "IndexNotes"},
			Instances: &three,
		}}
		_, errs := resolve(t, s, svcs)
		mustFail(t, errs, stack.CodePolicy, embedded.PolicyOneInstance, "runs 3 instances", "one process")
	})
	t.Run("a deployable placed outside the binary", func(t *testing.T) {
		reg, err := registry.Assemble(registry.DefaultNaming(), embedded.Extension{}, &stacktest.Extension{})
		if err != nil {
			t.Fatal(err)
		}
		echo := ir.ServiceRef{Name: "echo-api", Kind: ir.SchemaKindAPI}
		svcs := append(services(), stack.Service{
			Name: echo.Name, Kind: ir.SchemaKindAPI, Language: registry.APILanguageRust,
			Operations: []stack.Operation{{Name: "Echo.say"}},
		})
		s := notes()
		s.Deploy = append(s.Deploy, echo)
		s.Environments[0].Settings = []*ir.DeployableSettings{{Of: stacktest.Of(echo), Platform: stacktest.RunPlatform}}
		_, err = stack.Resolve(reg, stack.Input{Stack: s, Services: svcs, Environment: "Desktop"})
		var errs *stack.Errors
		if !errors.As(err, &errs) {
			t.Fatalf("resolve: %v", err)
		}
		mustFail(t, errs, stack.CodePolicy, embedded.PolicyOneBinary, "server echo-api is placed on platform "+stacktest.RunPlatform, "one binary")
	})
	t.Run("a second site", func(t *testing.T) {
		svcs := append(services(), stack.Service{
			Name: "admin-web", Kind: ir.SchemaKindSite, Calls: []ir.ServiceRef{notesAPI},
			Site: &ir.ResolvedSite{Dir: "web/admin-web", Build: "build", Output: "dist"},
		})
		s := notes()
		admin := ir.ServiceRef{Name: "admin-web", Kind: ir.SchemaKindSite}
		s.Deploy = append(s.Deploy, admin)
		s.Origins = append(s.Origins, &ir.Origin{Site: admin})
		_, errs := resolve(t, s, svcs)
		mustFail(t, errs, stack.CodePolicy, embedded.PolicyOneSite, "admin-web, notes-web", "one origin")
	})
	t.Run("a domain", func(t *testing.T) {
		s := notes()
		s.Environments[0].Domain = "notes.example.com"
		_, errs := resolve(t, s, services())
		mustFail(t, errs, stack.CodePolicy, embedded.PolicyNoDomain, "notes.example.com")
	})
	t.Run("parameters", func(t *testing.T) {
		s := notes()
		s.Environments[0].Parameters = []string{"pr"}
		_, errs := resolve(t, s, services())
		mustFail(t, errs, stack.CodePolicy, embedded.PolicyNoParameters, "takes parameters pr")
	})
}

// TestADatabaseNamesItsAdoption: a database's settings may name the one
// adoption of a file built by hand (D116), and nothing else.
func TestADatabaseNamesItsAdoption(t *testing.T) {
	s := notes()
	s.Environments[0].Settings = []*ir.DeployableSettings{{
		Of:     stacktest.Of(notesDB),
		Values: map[string]any{"adoption": map[string]any{"model": "plans/Desktop/notes-db/adopted.json", "check": "SELECT user_version = 7 FROM pragma_user_version"}},
	}}
	env := mustResolve(t, s, services())
	adoption, _ := env.Deployable(notesDB.Name).Settings["adoption"].(map[string]any)
	if adoption["model"] != "plans/Desktop/notes-db/adopted.json" {
		t.Errorf("the database's settings = %v, want its adoption", env.Deployable(notesDB.Name).Settings)
	}
	s.Environments[0].Settings[0].Values = map[string]any{"adoption": map[string]any{"model": "adopted.json"}}
	_, errs := resolve(t, s, services())
	mustFail(t, errs, stack.CodeInvalidSettings, "settings of notes-db on platform embedded.sqlite")
}
