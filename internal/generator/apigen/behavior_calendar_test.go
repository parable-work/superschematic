package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// calendarFixtures are the fixtures of Calendar (D70 on D103's step 4):
// fixture-calendar-api serves the operations of fixture-calendar-db's Desk
// and Booking from a @behaviorOperations set each, the contract Supply's
// and findAvailability, an operation of the desks as a whole, among Desk's,
// and runs no worker.
var calendarFixtures = behaviorFixtures{"fixture-calendar-api", "fixture-calendar-db"}

// TestWriteAPIGoldenCalendar generates the Go API module of
// fixture-calendar-api and holds the files the tables' Calendar and Holds
// uses change to their golden copies: the routes, each a handler run on
// the row, findAvailability's on the table as a whole; the behaviors'
// runner; the interfaces; and the OpenAPI document. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenCalendar -update
func TestWriteAPIGoldenCalendar(t *testing.T) {
	api, db := loadFixturePair(t, calendarFixtures)
	output := generatePairAPI(t, calendarFixtures, api, db)

	var calls []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo == nil {
			continue
		}
		calls = append(calls, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall()+" "+ep.BehaviorRunner())
	}
	slices.Sort(calls)
	want := []string{
		"POST /api/booking/{id}/holds/list-holds GetBookingRepository().Holds().ListHolds runBehaviorOperation",
		"POST /api/booking/{id}/holds/retake-holds GetBookingRepository().Holds().RetakeHolds runBehaviorOperation",
		"POST /api/booking/{id}/status/transition GetBookingRepository().Status().Transition runBehaviorOperation",
		"POST /api/desk/calendar/find-availability GetDeskRepository().Calendar().FindAvailability runTableBehaviorOperation",
		"POST /api/desk/{id}/calendar/add-closure GetDeskRepository().Calendar().AddClosure runBehaviorOperation",
		"POST /api/desk/{id}/calendar/check-hold GetDeskRepository().Calendar().CheckHold runBehaviorOperation",
		"POST /api/desk/{id}/calendar/commit-for GetDeskRepository().Calendar().CommitFor runBehaviorOperation",
		"POST /api/desk/{id}/calendar/hold-for GetDeskRepository().Calendar().HoldFor runBehaviorOperation",
		"POST /api/desk/{id}/calendar/list-bookings GetDeskRepository().Calendar().ListBookings runBehaviorOperation",
		"POST /api/desk/{id}/calendar/release-for GetDeskRepository().Calendar().ReleaseFor runBehaviorOperation",
		"POST /api/desk/{id}/calendar/remove-closure GetDeskRepository().Calendar().RemoveClosure runBehaviorOperation",
		"POST /api/desk/{id}/calendar/set-opening-hours GetDeskRepository().Calendar().SetOpeningHours runBehaviorOperation",
	}
	if !slices.Equal(calls, want) {
		t.Fatalf("the endpoints = %q, want %q", calls, want)
	}
	if len(output.Workers) != 0 {
		t.Fatalf("workers = %+v, want none: Calendar and Holds hear no write and arm no deadline", output.Workers)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+calendarFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", calendarFixtures.api)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "openapi.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestCalendarAPICompiles generates fixture-calendar-db's Go types and ORM
// and fixture-calendar-api's Go types and API module into a temporary tree
// shaped as the dist, and builds and vets the API module: its handlers call
// the ORM's operations under each use's member, Calendar's and the
// contract's under calendar, findAvailability on the table as a whole, and
// Holds' under holds. Linking needs the core's archive, which a build does
// not; go vet type-checks the cgo package.
func TestCalendarAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, calendarFixtures)
	apiDir, _ := writeFixtureModulesWith(t, calendarFixtures, api, db, nil)
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"db.GetDeskRepository().Calendar().SetOpeningHours",
		"db.GetBookingRepository().Holds().RetakeHolds",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}
