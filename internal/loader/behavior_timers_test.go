package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's deadlines: a database whose Ticket implements
// Timers beside its status Workflow, and whose Invoice implements Timers
// over the second of its two Workflows; and an API that serves Ticket's
// behavior operations and runs its deadlines.
const (
	timersDBFixture  = "tsreader/testdata/services/fixture-timers-db"
	timersAPIFixture = "tsreader/testdata/services/fixture-timers-api"
)

// A table that implements Timers stays a table: it gets _version, the
// sub-table its deadlines keep, its deadline queue and its change queue,
// and its API's @behaviorOperations set serves Timers' operations under
// timers and its @behaviorWorker becomes an ordinary worker of both queues,
// deadlines first (D103). The expanded IR of both fixtures is held to its
// golden.
func TestTimersFixturesExpand(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(timersDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	ticket := db.Types["Ticket"]
	if !ticket.Optimistic || len(ticket.ImplementsBehaviors()) != 1 || len(ticket.FieldBehaviors()) != 1 || ticket.IsEngineType() {
		t.Errorf("Ticket = optimistic %v, implements %v, fields %v, engine %v", ticket.Optimistic, ticket.ImplementsBehaviors(), ticket.FieldBehaviors(), ticket.IsEngineType())
	}
	sub := db.Types["TicketTimersDeadlines"]
	if sub == nil || sub.Origin != ir.OriginBehavior || sub.Role != ir.RoleDBTable ||
		!reflect.DeepEqual(sub.BehaviorTable, &ir.BehaviorTable{Row: "Ticket", Use: "Timers", Behavior: "Timers", Table: "deadlines", Key: "name", RowField: "ticket"}) {
		t.Errorf("TicketTimersDeadlines = %+v", sub)
	}
	queue := db.BehaviorQueueOf("Ticket", ir.BehaviorQueueDeadlines)
	if queue == nil || queue.Name != "TicketDeadline" || queue.Queue == nil || queue.Queue.Retain != "0s" || queue.Role != ir.RoleEmbeddedStruct {
		t.Errorf("Ticket's deadline queue = %+v", queue)
	}
	if db.Types["InvoiceTimersDeadlines"] == nil || db.Types["InvoiceDeadline"] == nil {
		t.Error("Invoice has no sub-table or no deadline queue")
	}
	// Timers hears its table's writes, so a raw write arms and clears its
	// deadlines when the worker runs the change (D103).
	for _, table := range []string{"Ticket", "Invoice"} {
		changes := db.BehaviorQueueOf(table, ir.BehaviorQueueChanges)
		if changes == nil || changes.Name != table+"Change" || !changes.BehaviorQueue.Same(&ir.BehaviorQueue{Row: table, Kind: ir.BehaviorQueueChanges, Behaviors: []string{"Timers"}}) {
			t.Errorf("%s's change queue = %+v", table, changes)
		}
	}
	compareExpanded(t, db, "fixture-timers-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(timersAPIFixture))
	if err != nil {
		t.Fatal(err)
	}
	worker := api.Worker("TicketTimers")
	if worker == nil || worker.Queue != "TicketDeadline" || !reflect.DeepEqual(worker.Queues, []string{"TicketDeadline", "TicketChange"}) || worker.Origin != ir.OriginBehavior || worker.Concurrency != 2 ||
		!reflect.DeepEqual(worker.BehaviorWorker, &ir.BehaviorWorkerConfig{Type: "Ticket"}) {
		t.Errorf("TicketTimers = %+v", worker)
	}
	if got := ir.WorkerDeployableName(api.Name, worker.Name); got != "fixture-timers-api-ticket-timers" {
		t.Errorf("the worker's deployable = %s", got)
	}
	var names []string
	for _, op := range api.OperationSets[0].Operations {
		names = append(names, op.Name+" "+op.RestPath+" "+op.MCP.Handle+" "+op.BehaviorOperation.Use)
	}
	want := []string{
		"timersFireTimer ticket/{id}/timers/fire-timer ticket_timers_fire_timer Timers",
		"timersResetTimer ticket/{id}/timers/reset-timer ticket_timers_reset_timer Timers",
		"timersSnoozeTimer ticket/{id}/timers/snooze-timer ticket_timers_snooze_timer Timers",
		"statusTransition ticket/{id}/status/transition ticket_status_transition status",
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("TicketActions' operations =\n%v\nwant\n%v", names, want)
	}
	for _, name := range []string{"TicketTimersDeadline", "TicketTimersFireTimerResultOutcome", "TicketStatusState"} {
		if api.Enums[name] == nil {
			t.Errorf("the API has no enum %s", name)
		}
	}
	compareExpanded(t, api, "fixture-timers-api.expanded.json")
}

// compareExpanded holds a loaded schema to its golden under
// testdata/golden, which -update rewrites.
func compareExpanded(t *testing.T, schema *ir.Schema, golden string) {
	t.Helper()
	got, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "golden", golden)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("missing golden file (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("expanded IR differs from %s; run with -update and review the diff", path)
	}
}
