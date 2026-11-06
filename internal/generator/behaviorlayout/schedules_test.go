package behaviorlayout_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// schedulesFixture is the DB service whose Problem implements Schedules
// beside its status Workflow, whose endOn it reads (D104).
const schedulesFixture = "../../loader/tsreader/testdata/services/fixture-schedules-db"

// The layout document of a table that implements Schedules (D104):
// Problem's first use is Schedules', with no owned field, its six
// operations, listSchedules writing nothing, and its schedules sub-table
// keyed by name with its fourteen fields; then its status. Its deadlines
// are messages of Problem's deadline queue, its heard writes of its change
// queue, and the events it emits of its event queue, whose member names
// the queue's columns exactly. testdata/fixture-schedules-db.layout.json is
// the document the adapters of every language are written against.
func TestLayoutOfSchedules(t *testing.T) {
	layout := layoutOfFixture(t, schedulesFixture, "fixture-schedules-db")
	td := layout.Types["Problem"]
	if td == nil {
		t.Fatalf("the layout has no Problem: %+v", layout.Types)
	}
	if len(td.Uses) != 2 || td.Uses[0].ID != "Schedules" || td.Uses[0].Member != "schedules" || len(td.Uses[0].Fields) != 0 || td.Uses[1].ID != "status" {
		t.Fatalf("Problem's uses = %+v, want Schedules' and its status", td.Uses)
	}
	var operations []string
	for _, op := range td.Uses[0].Operations {
		operations = append(operations, op.Name+map[bool]string{true: " writes", false: ""}[op.Writes])
	}
	if want := "setSchedule writes, pauseSchedule writes, resumeSchedule writes, cancelSchedule writes, reportRun writes, listSchedules"; strings.Join(operations, ", ") != want {
		t.Errorf("Schedules' operations = %s, want %s", strings.Join(operations, ", "), want)
	}
	if len(td.Uses[0].Tables) != 1 {
		t.Fatalf("Schedules' tables = %+v, want one", td.Uses[0].Tables)
	}
	table := td.Uses[0].Tables[0]
	var columns []string
	for _, field := range table.Fields {
		columns = append(columns, field.Name+" "+field.Column.Column)
	}
	if got, want := table.Name+" "+table.Table+" "+table.Row+" "+table.Key.Name+" "+table.Key.Column.Column+" ("+strings.Join(columns, ", ")+")",
		"schedules problem_schedules_schedules problem_id name name (kind kind, spec spec, timeZone time_zone, state state, setAt set_at, nextAt next_at, retryAt retry_at, runs runs, attempt attempt, lastAt last_at, lastOutcome last_outcome, finishedAt finished_at, endedBy ended_by)"; got != want {
		t.Errorf("Schedules' sub-table =\n%s\nwant\n%s", got, want)
	}
	for name, want := range map[string]any{
		"events":    `{"queue":"ProblemEvent","table":"problem_event_queue","rowKey":"row_key","use":"use_id","kind":"kind","at":"at","payload":"payload"}`,
		"deadlines": `{"queue":"ProblemDeadline","table":"problem_deadline_queue","rowKey":"row_key","use":"use_id","name":"name","at":"at","payload":"payload"}`,
		"changes":   `{"queue":"ProblemChange","table":"problem_change_queue","rowKey":"row_key","write":"write","at":"at","before":"before","after":"after"}`,
	} {
		member := map[string]any{"events": td.Events, "deadlines": td.Deadlines, "changes": td.Changes}[name]
		got, err := json.Marshal(member)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("Problem's %s = %s, want %s", name, got, want)
		}
	}
}

// A table whose behaviors emit no events has no events member, so every
// document but an emitting table's reads as it did (D104).
func TestLayoutOfATableThatEmitsNoEventsHasNoEvents(t *testing.T) {
	for _, fixture := range []struct{ dir, name string }{
		{"../../loader/tsreader/testdata/services/fixture-timers-db", "fixture-timers-db"},
		{seatsFixture, "fixture-seats-db"},
	} {
		layout := layoutOfFixture(t, fixture.dir, fixture.name)
		for name, td := range layout.Types {
			if td.Events != nil {
				t.Errorf("%s's %s has events %+v, want none", fixture.name, name, td.Events)
			}
		}
	}
}
