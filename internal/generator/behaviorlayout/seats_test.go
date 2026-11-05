package behaviorlayout_test

import (
	"strings"
	"testing"
)

// seatsFixture is the DB service whose Problem implements Seats and
// StateRules beside its status Workflow, and whose Sandbox carries
// Reconcile on its observed field beside its status Workflow (D104).
const seatsFixture = "../../loader/tsreader/testdata/services/fixture-seats-db"

// The layout document of a table that implements Seats and of one whose
// field is a Reconcile (D104): Problem's uses are Seats', with no owned
// field, its three operations, listSeats writing nothing, and its
// occupants sub-table keyed by number, then StateRules' and its status;
// Problem has no queue, since neither behavior arms a deadline or hears a
// change, and no audit, since a seat's by names no createdBy. Sandbox's
// uses are its status and its observed Reconcile, whose value field takes
// the anchor's column and whose other fields take columns named for the
// field (observed_observed_at, observed_drift, observed_reconciled_at),
// with observe; its stale deadline is a message of Sandbox's deadline
// queue. testdata/fixture-seats-db.layout.json is the document the
// adapters of every language are written against.
func TestLayoutOfSeatsAndReconcile(t *testing.T) {
	layout := layoutOfFixture(t, seatsFixture, "fixture-seats-db")
	uses := func(typeName string) string {
		td := layout.Types[typeName]
		if td == nil {
			t.Fatalf("the layout has no %s: %+v", typeName, layout.Types)
		}
		var out []string
		for _, use := range td.Uses {
			var fields []string
			for _, field := range use.Fields {
				fields = append(fields, field.Name+" "+field.Column.Field+" "+field.Column.Column+map[bool]string{true: " value", false: ""}[field.Value])
			}
			var operations []string
			for _, op := range use.Operations {
				operations = append(operations, op.Name+map[bool]string{true: " writes", false: ""}[op.Writes])
			}
			var tables []string
			for _, table := range use.Tables {
				var columns []string
				for _, field := range table.Fields {
					columns = append(columns, field.Name+" "+field.Column.Column)
				}
				tables = append(tables, table.Name+" "+table.Table+" "+table.Row+" "+table.Key.Name+" ("+strings.Join(columns, ", ")+")")
			}
			out = append(out, use.ID+" "+use.Behavior+" "+use.Member+" ["+strings.Join(fields, ", ")+"] ["+strings.Join(operations, ", ")+"] ["+strings.Join(tables, ", ")+"]")
		}
		return strings.Join(out, "\n")
	}
	for _, test := range []struct {
		typeName  string
		want      []string
		deadlines string
	}{
		{"Problem", []string{
			"Seats Seats seats [] [takeSeat writes, leaveSeat writes, listSeats] " +
				"[occupants problem_seats_occupants problem_id number (seat seat, subject subject, seatedAt seated_at, seatedBy seated_by)]",
			"StateRules StateRules staterules [] [] []",
			"status Workflow status [status status status value, enteredAt statusEnteredAt status_entered_at] [transition writes] []",
		}, ""},
		{"Sandbox", []string{
			"status Workflow status [status status status value, enteredAt statusEnteredAt status_entered_at] [transition writes] []",
			"observed Reconcile observed [observed observed observed value, observedAt observedObservedAt observed_observed_at, drift observedDrift observed_drift, reconciledAt observedReconciledAt observed_reconciled_at] [observe writes] []",
		}, "sandbox_deadline_queue"},
	} {
		if got := uses(test.typeName); got != strings.Join(test.want, "\n") {
			t.Errorf("%s's uses =\n%s\nwant\n%s", test.typeName, got, strings.Join(test.want, "\n"))
		}
		td := layout.Types[test.typeName]
		switch {
		case test.deadlines == "" && td.Deadlines != nil:
			t.Errorf("%s's deadlines = %+v, want none", test.typeName, td.Deadlines)
		case test.deadlines != "" && (td.Deadlines == nil || td.Deadlines.Table != test.deadlines):
			t.Errorf("%s's deadlines = %+v, want %s", test.typeName, td.Deadlines, test.deadlines)
		}
		if td.Changes != nil || len(td.Audit) != 0 {
			t.Errorf("%s's changes = %+v, audit = %+v, want neither", test.typeName, td.Changes, td.Audit)
		}
	}
}
