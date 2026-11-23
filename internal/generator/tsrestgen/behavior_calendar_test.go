package tsrestgen

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// calendarAPI is the API that serves fixture-calendar-db's Desk and Booking
// (D70 on D103's step 4) for any signed-in caller: Calendar's operations,
// the contract Supply's among them, routes of the row under the use's
// member, findAvailability a route of the desks as a whole, and Holds' and
// the status Workflow's transition. The router serves its MCP tools.
const calendarAPI = "fixture-calendar-api"

// calendarDB is fixture-calendar-api's database, its one DB dependency.
const calendarDB = "fixture-calendar-db"

// calendarFixture is fixture-calendar-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func calendarFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, calendarDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", calendarDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, calendarAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", calendarAPI, err)
	}
	deps := map[string]*ir.Schema{calendarDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     calendarAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: calendarAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: calendarDeps}
}

// calendarDeps is fixture-calendar-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var calendarDeps = DepsInfo{
	Database:             calendarDB,
	Dialect:              "postgres",
	ORM:                  "FixtureCalendarDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-calendar-db-types",
}

// TestWriteAPIGoldenBehaviorCalendar pins the package for
// fixture-calendar-api: its router mounts Calendar's and Holds' operations
// on the row under each use's member, each calling the repository's
// operation that takes the row's key, findAvailability's on the desks as a
// whole, taking none, and no behavior worker. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorCalendar -update
func TestWriteAPIGoldenBehaviorCalendar(t *testing.T) {
	fixture := calendarFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(calendarDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	var paths []string
	for _, ep := range fixture.endpoints.Endpoints {
		if ep.BehaviorOperation() != nil {
			paths = append(paths, ep.Method+" "+ep.Path)
		}
	}
	slices.Sort(paths)
	if want := []string{
		"POST /api/booking/{id}/holds/list-holds",
		"POST /api/booking/{id}/holds/retake-holds",
		"POST /api/booking/{id}/status/transition",
		"POST /api/desk/calendar/find-availability",
		"POST /api/desk/{id}/calendar/add-closure",
		"POST /api/desk/{id}/calendar/check-hold",
		"POST /api/desk/{id}/calendar/commit-for",
		"POST /api/desk/{id}/calendar/hold-for",
		"POST /api/desk/{id}/calendar/list-bookings",
		"POST /api/desk/{id}/calendar/release-for",
		"POST /api/desk/{id}/calendar/remove-closure",
		"POST /api/desk/{id}/calendar/set-opening-hours",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, calendarAPI)
}
