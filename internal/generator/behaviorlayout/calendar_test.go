package behaviorlayout_test

import (
	"strings"
	"testing"
)

// calendarFixture is the DB service whose Desk Calendar opens and whose
// Booking holds a slot of one through Supply (D70, D103's step 4).
const calendarFixture = "../../loader/tsreader/testdata/services/fixture-calendar-db"

// The layout document of a table that implements Calendar (D70 on D103's
// step 4): Desk's one use is Calendar's, which owns no column (its
// timeZone, hours and closures are computed at a read), with its
// sub-tables bookings (keyed by the record), closures (by the closure's
// name) and settings (by the setting's name), and its operations, the
// contract's among them and findAvailability on the table as a whole;
// Booking's are Holds' and the status Workflow's, its relation desk the
// own field a link hold reads; neither keeps a queue.
// testdata/fixture-calendar-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfCalendar(t *testing.T) {
	layout := layoutOfFixture(t, calendarFixture, "fixture-calendar-db")
	desk := layout.Types["Desk"]
	if desk == nil || len(desk.Uses) != 1 {
		t.Fatalf("Desk = %+v", desk)
	}
	calendar := desk.Uses[0]
	if len(calendar.Fields) != 0 {
		t.Errorf("Calendar's fields = %+v, want none: its three are computed at a read", calendar.Fields)
	}
	var tables []string
	for _, table := range calendar.Tables {
		tables = append(tables, table.Name+"="+table.Table+"/"+table.Key.Column.Column)
	}
	if want := "bookings=desk_calendar_bookings/record, closures=desk_calendar_closures/closure, settings=desk_calendar_settings/name"; strings.Join(tables, ", ") != want {
		t.Errorf("Calendar's tables = %s, want %s", strings.Join(tables, ", "), want)
	}
	var operations []string
	for _, op := range calendar.Operations {
		name := op.Name
		if op.Table {
			name += "(table)"
		}
		operations = append(operations, name)
	}
	if want := "setOpeningHours, addClosure, removeClosure, holdFor, commitFor, releaseFor, checkHold, listBookings, findAvailability(table)"; strings.Join(operations, ", ") != want {
		t.Errorf("Calendar's operations = %s, want %s", strings.Join(operations, ", "), want)
	}
	booking := layout.Types["Booking"]
	if booking == nil || len(booking.Uses) != 2 {
		t.Fatalf("Booking = %+v", booking)
	}
	relation := ""
	for _, field := range booking.Spec.Fields {
		if field.Relation != nil {
			relation = field.Name + "->" + field.Relation.Name + ":" + field.Relation.Type
		}
	}
	if want := "deskId->desk:Desk"; relation != want {
		t.Errorf("Booking's relation = %s, want %s", relation, want)
	}
	for _, td := range []string{"Desk", "Booking"} {
		if table := layout.Types[td]; table.Deadlines != nil || table.Changes != nil || table.Events != nil {
			t.Errorf("%s's queues = %+v %+v %+v, want none", td, table.Deadlines, table.Changes, table.Events)
		}
	}
}
