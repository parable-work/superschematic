package stack_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The database of a server, a job or a worker resolves to a dialect its
// generated Deps reach (docs/stack-model.md, section 18.6, D64): a
// TypeScript API's Deps.db is the client of its DB service's first
// dialect, as a Rust API's is (D116), a Go API's Deps reach Postgres
// alone, and a TypeScript API over the user model is refused on a
// sqlite-first database.

// notesStack is a stack of one API, notes, written in lang, on notes-db,
// whose DB service lists dialects in order, with a local environment, Dev.
func notesStack(lang string, dialects ...string) (*ir.Stack, []stack.Service) {
	db := ir.ServiceRef{Name: "notes-db", Kind: ir.SchemaKindDB}
	api := ir.ServiceRef{Name: "notes", Kind: ir.SchemaKindAPI}
	s := &ir.Stack{
		Name:         "notes-stack",
		Deploy:       []ir.ServiceRef{api},
		Environments: []*ir.Environment{{Name: "Dev", Target: "local"}},
	}
	return s, []stack.Service{
		{Name: "notes-db", Kind: ir.SchemaKindDB, Dialects: dialects},
		{Name: "notes", Kind: ir.SchemaKindAPI, Language: lang, Dependencies: []ir.ServiceRef{db}, Operations: []stack.Operation{{Name: "Notes.getNote"}}},
	}
}

// TestADatabaseGoesOnThePlatformOfItsFirstDialect: on the local target a
// database whose DB service lists sqlite first goes on local.sqlite, which
// the target names for the dialect, and the TypeScript server's edge
// connects through the SQLite connector; one that lists postgres first
// stays on the Postgres container, as acme-shop's shop-db does.
func TestADatabaseGoesOnThePlatformOfItsFirstDialect(t *testing.T) {
	reg := assemble(t)
	s, services := notesStack(registry.APILanguageTypeScript, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	env := mustResolve(t, reg, s, services, "Dev")
	db := env.Deployable("notes-db")
	if db == nil || db.Platform != "local.sqlite" || db.Dialect != registry.SQLDialectSQLite {
		t.Fatalf("notes-db = %+v, want it on local.sqlite in sqlite", db)
	}
	id := ir.EdgeID(ir.EdgeSQL, "notes", "notes-db")
	connector := ""
	for _, e := range env.Edges {
		if e.ID == id {
			connector = e.Connector
		}
	}
	if connector != "local.process-sqlite" {
		t.Errorf("the sql edge %s goes through %q, want local.process-sqlite", id, connector)
	}

	s, services = notesStack(registry.APILanguageTypeScript, registry.SQLDialectPostgres, registry.SQLDialectSQLite)
	env = mustResolve(t, reg, s, services, "Dev")
	if db := env.Deployable("notes-db"); db == nil || db.Platform != "local.postgres" || db.Dialect != registry.SQLDialectPostgres {
		t.Fatalf("a postgres-first notes-db = %+v, want it on local.postgres", db)
	}
}

// TestATypeScriptAPIOnAnotherDialectThanItsFirstIsRefused: the fake
// target's sql platform runs Postgres alone, so a TypeScript API whose
// database lists sqlite first, and whose Deps.db is therefore a
// SqlDatabase, is refused there, naming both dialects.
func TestATypeScriptAPIOnAnotherDialectThanItsFirstIsRefused(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}
	service(services, "shop-api").Language = registry.APILanguageTypeScript
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable, "server shop-api is a TypeScript server on shop-db, whose DB service lists sqlite first, and environment Staging places shop-db on shop-db, a postgres database; a TypeScript API's Deps.db is the client of its database's first dialect (D64)")

	// Placed on the fake sqlite platform by a setting, it is not refused
	// for the dialect, only for the connector the fake target lacks.
	settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopDB)).Platform = stacktest.LitePlatform
	_, errs = resolve(t, reg, s, services, "Staging")
	for _, err := range errs.Of(stack.CodeUnrealizable) {
		if strings.Contains(err.Message, "server shop-api is a TypeScript server") {
			t.Errorf("shop-api on its first dialect is refused: %s", err.Message)
		}
	}
}

// TestAGoAPIOnSQLiteIsRefused: a Go API's Deps reach Postgres alone, so a
// sqlite-first database, which the local target keeps in a SQLite file, is
// refused for its server.
func TestAGoAPIOnSQLiteIsRefused(t *testing.T) {
	reg := assemble(t)
	s, services := notesStack(registry.APILanguageGo, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	_, errs := resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeUnrealizable, "server notes is a GO server on notes-db, and environment Dev places notes-db on notes-db, a sqlite database; a Go API's Deps reach Postgres alone, through the Go ORM (D64)")
}

// TestARustAPIRunsOnItsDatabasesFirstDialect: a Rust API's Deps.db is the
// client of its database's first dialect (D116), as a TypeScript one's is:
// a sqlite-first database, which the local target keeps in a SQLite file,
// resolves for its server, and a postgres-first one the environment places
// on a SQLite platform is refused.
func TestARustAPIRunsOnItsDatabasesFirstDialect(t *testing.T) {
	reg := assemble(t)
	s, services := notesStack(registry.APILanguageRust, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	env := mustResolve(t, reg, s, services, "Dev")
	if db := env.Deployable("notes-db"); db == nil || db.Platform != "local.sqlite" || db.Dialect != registry.SQLDialectSQLite {
		t.Fatalf("a Rust server's sqlite-first notes-db = %+v, want it on local.sqlite", db)
	}

	// The fake target's sql platform runs Postgres alone, so there a Rust
	// API whose database lists sqlite first is refused, naming both.
	s, services = shop()
	service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}
	service(services, "shop-api").Language = registry.APILanguageRust
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable, "server shop-api is a Rust server on shop-db, whose DB service lists sqlite first, and environment Staging places shop-db on shop-db, a postgres database; a Rust API's Deps.db is the client of its database's first dialect (D64, D116)")
}

// TestATypeScriptIdentityAPIOnASQLiteFirstDatabaseIsRefused: the identity
// runtime's TypeScript store runs on Postgres, and none over SQLite is
// built, so a TypeScript API over the user model on a sqlite-first
// database is refused (D50).
func TestATypeScriptIdentityAPIOnASQLiteFirstDatabaseIsRefused(t *testing.T) {
	reg := assemble(t)
	s, services := notesStack(registry.APILanguageTypeScript, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	service(services, "notes").Identity = true
	_, errs := resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeUnrealizable, "server notes serves notes, which authenticates its users with the identity runtime over notes-db, whose DB service lists sqlite first; a TypeScript server's identity store runs on Postgres, and one over SQLite is not built (D50, D64)")
}

// TestADatabaseOfTwoFirstDialectsStaysOnTheKindsPlatform: a database that
// hosts a sqlite-first and a postgres-first DB service goes on the
// target's database platform, Postgres, and a TypeScript API on the
// sqlite-first one is refused.
func TestADatabaseOfTwoFirstDialectsStaysOnTheKindsPlatform(t *testing.T) {
	reg := assemble(t)
	s, services := notesStack(registry.APILanguageTypeScript, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	services = append(services, stack.Service{Name: "tags-db", Kind: ir.SchemaKindDB})
	s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: "Main", Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{
		{Name: "notes-db", Kind: ir.SchemaKindDB}, {Name: "tags-db", Kind: ir.SchemaKindDB},
	}})
	_, errs := resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeUnrealizable, "environment Dev places notes-db on Main, a postgres database")
}

// backgroundNotes is the notes stack with a job, a worker of notes-db's
// queue NoteWritten and a behavior worker of its table Note, whose queue
// the loader filled with the table's deadline queue (D103), on notes-db,
// whose DB service lists sqlite first.
func backgroundNotes(lang string) (*ir.Stack, []stack.Service) {
	s, services := notesStack(lang, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
	service(services, "notes-db").Queues = []string{"NoteDeadline", "NoteWritten"}
	notes := service(services, "notes")
	notes.Jobs = []ir.Job{{Name: "PruneNotes"}}
	notes.Workers = []ir.Worker{
		{Name: "IndexNotes", Queue: "NoteWritten"},
		{Name: "NoteTimers", Queue: "NoteDeadline", Queues: []string{"NoteDeadline"}, BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Note"}, Origin: ir.OriginBehavior},
	}
	return s, services
}

// TestARustJobAndWorkerRunOnTheirDatabasesSQLiteFile: a Rust job, a Rust
// worker and a Rust behavior worker of a sqlite-first database resolve on
// the local target (D116), each connected to the database's SQLite file,
// which the worker claims on and the behavior worker drains; the same in
// Go stay refused, since a Go API's Deps reach Postgres alone.
func TestARustJobAndWorkerRunOnTheirDatabasesSQLiteFile(t *testing.T) {
	reg := assemble(t)
	s, services := backgroundNotes(registry.APILanguageRust)
	env := mustResolve(t, reg, s, services, "Dev")
	connectors := map[string]string{}
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeSQL {
			connectors[e.From] = e.Connector
		}
	}
	for deployable, want := range map[string]string{
		"notes":             "local.process-sqlite",
		"notes-prune-notes": "local.job-sqlite",
		"notes-index-notes": "local.worker-sqlite",
		"notes-note-timers": "local.worker-sqlite",
	} {
		d := env.Deployable(deployable)
		if d == nil || d.Language != registry.APILanguageRust {
			t.Fatalf("%s = %+v, want a Rust deployable of the environment", deployable, d)
		}
		if got := connectors[deployable]; got != want {
			t.Errorf("the sql edge of %s goes through %q, want %s", deployable, got, want)
		}
	}
	for worker, queue := range map[string]string{"notes-index-notes": "NoteWritten", "notes-note-timers": "NoteDeadline"} {
		if w := env.Deployable(worker).Worker; w == nil || w.Database != "notes-db" || w.Queue != queue {
			t.Errorf("%s runs %+v, want queue %s of notes-db", worker, w, queue)
		}
	}

	s, services = backgroundNotes(registry.APILanguageGo)
	_, errs := resolve(t, reg, s, services, "Dev")
	for _, refused := range []string{"server notes is a GO server", "job notes-prune-notes is a GO job", "worker notes-index-notes is a GO worker", "worker notes-note-timers is a GO worker"} {
		mustFail(t, errs, stack.CodeUnrealizable, refused+" on notes-db, and environment Dev places notes-db on notes-db, a sqlite database; a Go API's Deps reach Postgres alone, through the Go ORM (D64)")
	}
}
