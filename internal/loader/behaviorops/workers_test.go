package behaviorops

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// schedulesWorkerSchemas are a database whose Problem implements Schedules
// of the kinds given, with the event queue the loader adds for it, and an
// API whose ProblemWakeUps is a behavior worker of Problem (D104).
func schedulesWorkerSchemas(kinds ...string) (api, db *ir.Schema) {
	db = ir.NewSchema("problems-db", ir.SchemaKindDB)
	config, _ := json.Marshal(map[string]any{"max": 1, "kinds": kinds, "outcomes": []string{"told"}})
	db.Types["Problem"] = &ir.TypeDef{Name: "Problem", Role: ir.RoleDBTable,
		Behaviors: []ir.BehaviorRef{{Name: "Schedules", Implements: true, Config: config}},
		Fields:    []*ir.FieldDef{{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Key: true, Required: true}}}
	db.Types["ProblemEvent"] = &ir.TypeDef{Name: "ProblemEvent", Role: ir.RoleEmbeddedStruct, Origin: ir.OriginBehavior,
		BehaviorQueue: &ir.BehaviorQueue{Row: "Problem", Kind: ir.BehaviorQueueEvents, Behaviors: []string{"Schedules"}}, Queue: &ir.QueueDef{Key: "rowKey", Serialize: true}}
	api = ir.NewSchema("problems-api", ir.SchemaKindAPI)
	api.Workers = []*ir.Worker{{Name: "ProblemWakeUps", BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Problem"}}}
	return api, db
}

// ExpandWorkers gives a behavior worker of a table whose behaviors emit
// events its event queue, the events it hands to the API's Workers, and
// the API the types of each event and its payload, the payload's enums
// named by the narrowing's aliases, a payload's kind every kind whatever
// the config takes, since a schedule of a dropped kind keeps firing; it
// returns the scalars they name, and running it again on its output
// changes nothing (D104).
func TestExpandWorkersAddsTheEventsAndTheirTypes(t *testing.T) {
	reg := registry.New(naming.Default())
	api, db := schedulesWorkerSchemas("every", "cron")
	scalars, err := ExpandWorkers(api, db, reg)
	if err != nil {
		t.Fatal(err)
	}
	worker := api.Workers[0]
	if worker.Queue != "ProblemEvent" || !slices.Equal(worker.Queues, []string{"ProblemEvent"}) || worker.Origin != ir.OriginBehavior || len(worker.Events) != 1 {
		t.Fatalf("ProblemWakeUps = %+v", worker)
	}
	if got := worker.Events[0]; got.Method != "ProblemWakeUpsSchedulesRun" || got.Event != "ProblemSchedulesRunEvent" || got.Payload != "ProblemSchedulesRunPayload" || got.Use != "Schedules" || got.Member != "schedules" {
		t.Errorf("ProblemWakeUps' event = %+v", got)
	}
	slices.Sort(scalars)
	if want := []string{"Generic.Int64", "Identity.UUID"}; !slices.Equal(scalars, want) {
		t.Errorf("scalars = %v, want %v", scalars, want)
	}
	if kind := api.Enums["ProblemSchedulesKind"]; kind == nil || !slices.Equal(enumValues(kind), []string{"after", "every", "cron"}) || kind.Owner != api.Name || kind.Comment != "The kind of a schedule of Problem's Schedules: after, every or cron, whichever its config took when the schedule was set." {
		t.Errorf("ProblemSchedulesKind = %+v", kind)
	}
	for _, name := range []string{"ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload"} {
		if td := api.Types[name]; td == nil || td.Owner != api.Name || td.Origin != ir.OriginBehavior || td.Role != ir.RoleEmbeddedStruct {
			t.Errorf("%s = %+v", name, td)
		}
	}
	before, _ := json.Marshal(api)
	if _, err := ExpandWorkers(api, db, reg); err != nil {
		t.Fatal(err)
	}
	if after, _ := json.Marshal(api); string(after) != string(before) {
		t.Errorf("a second expansion changed the API:\n%s\nwas\n%s", after, before)
	}
}

// An enum the API holds under the name of an alias the payload names, with
// other values, is refused, naming the worker, the table, the enum and both
// lists of values (D104); one of the same values is kept.
func TestExpandWorkersRefusesAnEnumOfOtherValues(t *testing.T) {
	reg := registry.New(naming.Default())
	api, db := schedulesWorkerSchemas("every")
	api.Enums["ProblemSchedulesKind"] = &ir.EnumDef{Name: "ProblemSchedulesKind", Origin: ir.OriginBehavior, Values: []ir.EnumValueDef{{Name: "After", SerializedAs: "after"}}}
	_, err := ExpandWorkers(api, db, reg)
	if want := "behaviorops: @behaviorWorker ProblemWakeUps: the payload of an event of Problem names the enum ProblemSchedulesKind (after, every, cron), which problems-api holds with the values after (D104)"; err == nil || err.Error() != want {
		t.Fatalf("ExpandWorkers = %v, want %q", err, want)
	}
	api, db = schedulesWorkerSchemas("every")
	held := &ir.EnumDef{Name: "ProblemSchedulesKind", Origin: ir.OriginBehavior, Comment: "kept", Values: []ir.EnumValueDef{{Name: "After", SerializedAs: "after"}, {Name: "Every", SerializedAs: "every"}, {Name: "Cron", SerializedAs: "cron"}}}
	api.Enums["ProblemSchedulesKind"] = held
	if _, err := ExpandWorkers(api, db, reg); err != nil || api.Enums["ProblemSchedulesKind"] != held {
		t.Fatalf("ExpandWorkers = %v, enum %+v, want the held enum kept", err, api.Enums["ProblemSchedulesKind"])
	}
}

// A behavior worker of a table whose behaviors emit none gets no events
// and adds no type, so its API reads as it did before D104.
func TestExpandWorkersOfATableThatEmitsNothingAddsNothing(t *testing.T) {
	reg := registry.New(naming.Default())
	api, db := schedulesWorkerSchemas("every")
	delete(db.Types, "ProblemEvent")
	db.Types["ProblemDeadline"] = &ir.TypeDef{Name: "ProblemDeadline", Role: ir.RoleEmbeddedStruct, Origin: ir.OriginBehavior,
		BehaviorQueue: &ir.BehaviorQueue{Row: "Problem", Kind: ir.BehaviorQueueDeadlines, Behaviors: []string{"Schedules"}}, Queue: &ir.QueueDef{}}
	scalars, err := ExpandWorkers(api, db, reg)
	if err != nil || len(scalars) != 0 {
		t.Fatalf("ExpandWorkers = %v, %v, want no scalars", scalars, err)
	}
	if worker := api.Workers[0]; worker.Queue != "ProblemDeadline" || worker.Events != nil || len(api.Types) != 0 || len(api.Enums) != 0 {
		t.Errorf("ProblemWakeUps = %+v, types %v, enums %v", worker, api.Types, api.Enums)
	}
	if !strings.HasPrefix(EventMethod("ProblemWakeUps", "schedules", "run"), "ProblemWakeUps") || EventType("Order", "status", "moved") != "OrderStatusMovedEvent" || EventPayloadType("Order", "status", "moved") != "OrderStatusMovedPayload" {
		t.Errorf("names: %s, %s, %s", EventMethod("ProblemWakeUps", "schedules", "run"), EventType("Order", "status", "moved"), EventPayloadType("Order", "status", "moved"))
	}
}
