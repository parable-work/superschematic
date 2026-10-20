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
// dialect, a Go or Rust API's Deps reach Postgres alone, and a TypeScript
// API over the user model is refused on a sqlite-first database.

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

// TestAGoOrRustAPIOnSQLiteIsRefused: a Go or Rust API's Deps reach
// Postgres alone, so a sqlite-first database, which the local target keeps
// in a SQLite file, is refused for its server.
func TestAGoOrRustAPIOnSQLiteIsRefused(t *testing.T) {
	reg := assemble(t)
	for _, lang := range []string{registry.APILanguageGo, registry.APILanguageRust} {
		s, services := notesStack(lang, registry.SQLDialectSQLite, registry.SQLDialectPostgres)
		_, errs := resolve(t, reg, s, services, "Dev")
		mustFail(t, errs, stack.CodeUnrealizable, "server notes is a "+lang+" server on notes-db, and environment Dev places notes-db on notes-db, a sqlite database; a Go or Rust API's Deps reach Postgres alone, through the Go ORM or the Rust pool (D64)")
	}
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
