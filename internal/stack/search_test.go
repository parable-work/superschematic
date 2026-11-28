package stack_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// One search index per environment (#740): a stack names its search
// database with @stack({ search }), which holds the environment's index,
// and every database of the stack with a @searchable table copies its
// index rows there.

// searchStack is a stack of two APIs, each on a database of its own with
// a @searchable table that copies its rows into the environment's index,
// and the search database, which holds that index, with a local
// environment, Dev.
func searchStack() (*ir.Stack, []stack.Service) {
	ref := func(name string, kind ir.SchemaKind) ir.ServiceRef { return ir.ServiceRef{Name: name, Kind: kind} }
	search := ref("search-db", ir.SchemaKindDB)
	s := &ir.Stack{
		Name:         "records-stack",
		Deploy:       []ir.ServiceRef{ref("east", ir.SchemaKindAPI), ref("west", ir.SchemaKindAPI)},
		Search:       &search,
		Environments: []*ir.Environment{{Name: "Dev", Target: "local"}},
	}
	services := []stack.Service{
		{Name: "search-db", Kind: ir.SchemaKindDB, EnvironmentIndex: true},
	}
	for _, name := range []string{"east", "west"} {
		db := ref(name+"-db", ir.SchemaKindDB)
		services = append(services,
			stack.Service{Name: db.Name, Kind: ir.SchemaKindDB, Searchable: true, SearchEnvironment: true},
			stack.Service{Name: name, Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript, Dependencies: []ir.ServiceRef{db}, Operations: []stack.Operation{{Name: "Records.search"}}},
		)
	}
	return s, services
}

// TestAStackNamesItsSearchDatabase: the search database joins the stack,
// as a database of its own, and the resolved environment names it as the
// database that holds its search index.
func TestAStackNamesItsSearchDatabase(t *testing.T) {
	reg := assemble(t)
	s, services := searchStack()
	env := mustResolve(t, reg, s, services, "Dev")
	if env.Search != "search-db" {
		t.Errorf("the environment's search index is in %q, want search-db", env.Search)
	}
	if db := env.Deployable("search-db"); db == nil || db.Dialect != registry.SQLDialectPostgres {
		t.Errorf("search-db = %+v, want a database on Postgres", db)
	}
	for _, name := range []string{"east-db", "west-db"} {
		if env.Deployable(name) == nil {
			t.Errorf("the stack has no %s", name)
		}
	}

	// A stack that names none keeps each database's own index.
	s, services = searchStack()
	s.Search = nil
	for i := range services {
		services[i].SearchEnvironment = false
	}
	if env := mustResolve(t, reg, s, services, "Dev"); env.Search != "" {
		t.Errorf("a stack with no search database names %q", env.Search)
	}
}

// TestASearchDatabaseIsCheckedAgainstItsSources: a search database without
// the environment's index, a source that keeps its rows to itself, a
// source that copies them with no search database to copy into, and a
// search database placed on SQLite are each refused.
func TestASearchDatabaseIsCheckedAgainstItsSources(t *testing.T) {
	reg := assemble(t)

	s, services := searchStack()
	service(services, "search-db").EnvironmentIndex = false
	_, errs := resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeInvalidStack, "@stack search names search-db, whose config does not set outputs.sql.search.environmentIndex; set it, so search-db holds the environment's search index (#740)")

	s, services = searchStack()
	service(services, "west-db").SearchEnvironment = false
	_, errs = resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeInvalidStack, "DB service west-db has a @searchable table, and its config does not set outputs.sql.search.environment; set it, so its worker copies its index rows into search-db, the stack's search database (#740)")

	s, services = searchStack()
	s.Search = nil
	_, errs = resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeInvalidStack, "DB service east-db sets outputs.sql.search.environment, and the stack names no search database to copy its index rows into; name it with @stack({ search }) (#740), or leave environment out to keep its own index")

	s, services = searchStack()
	service(services, "search-db").Dialects = []string{registry.SQLDialectSQLite}
	_, errs = resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeUnrealizable, "environment Dev places search-db, the stack's search database, on search-db, a sqlite database; an environment's search index is Postgres's alone (#740)")

	s, services = searchStack()
	s.Search = &ir.ServiceRef{Name: "east", Kind: ir.SchemaKindAPI}
	_, errs = resolve(t, reg, s, services, "Dev")
	mustFail(t, errs, stack.CodeKindMismatch, "search names east, a API service; it takes DB services")
}
