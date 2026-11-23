package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The fixture of Calendar (D70 on D103's step 4): fixture-calendar-api
// serves DeskCalendar and BookingHolds, @behaviorOperations of Desk and
// Booking for any signed-in caller, over fixture-calendar-db, its one DB
// dependency. Each is a route of the row under its use's member, run
// through the behaviors' core with run_operation, the contract Supply's
// among Calendar's, and findAvailability a route of the desks as a whole;
// the API runs no worker.
var calendarFixture = behaviorFixture{api: "fixture-calendar-api", db: "fixture-calendar-db"}

// TestWriteRustAPIGoldenCalendar pins the crate of fixture-calendar-api:
// the router mounts each of Calendar's and Holds' operations, and the
// layout it embeds holds each use's sub-tables. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenCalendar -update
func TestWriteRustAPIGoldenCalendar(t *testing.T) {
	_, db, reg := calendarFixture.load(t)
	output := calendarFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = calendarFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(calendarFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + calendarFixture.db
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, endpoint.Namespace+"."+endpoint.Name+" "+strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	if want := []string{
		"booking-holds.holdsListHolds POST /api/booking/{id}/holds/list-holds Booking.Holds.listHolds",
		"booking-holds.holdsRetakeHolds POST /api/booking/{id}/holds/retake-holds Booking.Holds.retakeHolds",
		"booking-holds.statusTransition POST /api/booking/{id}/status/transition Booking.status.transition",
		"desk-calendar.calendarAddClosure POST /api/desk/{id}/calendar/add-closure Desk.Calendar.addClosure",
		"desk-calendar.calendarCheckHold POST /api/desk/{id}/calendar/check-hold Desk.Calendar.checkHold",
		"desk-calendar.calendarCommitFor POST /api/desk/{id}/calendar/commit-for Desk.Calendar.commitFor",
		"desk-calendar.calendarFindAvailability POST /api/desk/calendar/find-availability Desk.Calendar.findAvailability",
		"desk-calendar.calendarHoldFor POST /api/desk/{id}/calendar/hold-for Desk.Calendar.holdFor",
		"desk-calendar.calendarListBookings POST /api/desk/{id}/calendar/list-bookings Desk.Calendar.listBookings",
		"desk-calendar.calendarReleaseFor POST /api/desk/{id}/calendar/release-for Desk.Calendar.releaseFor",
		"desk-calendar.calendarRemoveClosure POST /api/desk/{id}/calendar/remove-closure Desk.Calendar.removeClosure",
		"desk-calendar.calendarSetOpeningHours POST /api/desk/{id}/calendar/set-opening-hours Desk.Calendar.setOpeningHours",
	}; !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, calendarFixture.api, output)
	for file, want := range map[string][]string{
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"desk_calendar_bookings"`,
			`"desk_calendar_closures"`,
			`"desk_calendar_settings"`,
			`"booking_holds_items"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
