package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's step 4's Calendar (D70): desks whose time Calendar
// opens and bookings that hold a slot of one through the contract Supply,
// and the API that serves both.
const (
	calendarDBFixture  = "tsreader/testdata/services/fixture-calendar-db"
	calendarAPIFixture = "tsreader/testdata/services/fixture-calendar-api"
)

// TestCalendarFixtureLoads: the fixture loads with no warning and no error;
// Desk and Booking stay tables, Desk implementing Calendar and Booking
// Holds beside its status Workflow, each config read as the TypeScript
// form wrote it; Calendar owns no column, its three fields computed at a
// read, and keeps its bookings, closures and settings as sub-tables, each
// keyed and related to its row; no queue, since neither arms a deadline,
// hears a write or emits an event; and the expanded IR is held to its
// golden (D70, D103's step 4).
func TestCalendarFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(calendarDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	desk, booking := db.Types["Desk"], db.Types["Booking"]
	if desk == nil || len(desk.TypeBehaviors()) > 0 || !desk.Optimistic || booking == nil || !booking.Optimistic {
		t.Fatalf("Desk = %s, Booking = %s", marshal(t, desk), marshal(t, booking))
	}
	uses := func(td *ir.TypeDef) []string {
		var out []string
		for _, ref := range td.Behaviors {
			out = append(out, ref.Name+"@"+ref.Field)
		}
		return out
	}
	if got, want := uses(desk), []string{"Calendar@"}; !slices.Equal(got, want) {
		t.Errorf("Desk's uses = %v, want %v", got, want)
	}
	if got, want := uses(booking), []string{"Holds@", "Workflow@status"}; !slices.Equal(got, want) {
		t.Errorf("Booking's uses = %v, want %v", got, want)
	}
	if !strings.Contains(string(desk.Behaviors[0].Config), `"timeZone":"Europe/London"`) || !strings.Contains(string(booking.Behaviors[0].Config), `"link":"desk"`) {
		t.Errorf("the configs = %s and %s", desk.Behaviors[0].Config, booking.Behaviors[0].Config)
	}
	for _, fd := range desk.Fields {
		if fd.Owned() {
			t.Errorf("Desk owns %s, want no column of Calendar's", fd.Name)
		}
	}
	for _, sub := range []string{"DeskCalendarBookings", "DeskCalendarClosures", "DeskCalendarSettings", "BookingHoldsItems"} {
		td := db.Types[sub]
		if td == nil || td.Origin != ir.OriginBehavior || td.BehaviorTable == nil || td.BehaviorTable.RowField == "" {
			t.Fatalf("%s = %s", sub, marshal(t, td))
		}
	}
	for _, name := range []string{"Desk", "Booking"} {
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	compareExpanded(t, db, "fixture-calendar-db.expanded.json")
}

// TestCalendarAPIFixtureLoads: the API over the desks loads with no
// warning, each table's behaviors' operations served by its
// @behaviorOperations class: Calendar's and the contract's on a desk,
// findAvailability on the desks as a whole among them, Holds' and the
// Workflow's on a booking (D70, D103's step 4).
func TestCalendarAPIFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(calendarAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	var sets []string
	for _, set := range api.OperationSets {
		sets = append(sets, set.Name)
	}
	slices.Sort(sets)
	if want := []string{"BookingHolds", "DeskCalendar"}; !slices.Equal(sets, want) {
		t.Errorf("the API's operation sets = %v, want %v", sets, want)
	}
}
