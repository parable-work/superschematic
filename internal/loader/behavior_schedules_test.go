package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D104's third step's second part: a database whose
// Problem implements Schedules beside its status Workflow (an agent's
// wake-ups after a delay, at an interval or on a cron line in a time zone,
// a minute to thirty days apart, at most ten a record, a missed run tried
// again five minutes on, every wake-up finished when the record is done or
// dropped), and an API that serves the table's behavior operations and runs
// its deadlines, its heard writes and its events in a @behaviorWorker,
// which hands each event to the project's Workers method.
const (
	schedulesDBFixture  = "tsreader/testdata/services/fixture-schedules-db"
	schedulesAPIFixture = "tsreader/testdata/services/fixture-schedules-api"
)

// TestSchedulesFixturesLoad: both fixtures load with no warning and no
// error; Problem stays a table whose uses are Schedules, which it
// implements, and its status, its config read as the TypeScript form wrote
// it; the loader adds Schedules' schedules sub-table and no owned field,
// Problem's deadline queue, its change queue and its event queue, a D80
// queue keyed by the row's key, serialized and pruned at once, of the
// fields rowKey, useId, kind, at and payload; the API's set serves
// Schedules' six operations and the Workflow's transition, each Schedules
// one listing problems.schedule; ProblemWakeUps' queues are the deadline,
// change and event queues, in that order, and its events the one run, which
// it hands to ProblemWakeUpsSchedulesRun; the API holds the types of the
// event and its payload, whose kind is ProblemSchedulesKind; and the
// expanded IR of both is held to its golden (D104).
func TestSchedulesFixturesLoad(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(schedulesDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	problem := db.Types["Problem"]
	if problem == nil || problem.IsEngineType() || !problem.Optimistic {
		t.Fatalf("Problem = %s", marshal(t, problem))
	}
	var uses []string
	for _, ref := range problem.TableBehaviors() {
		uses = append(uses, ref.UseID()+":"+ref.Name)
	}
	if want := []string{"Schedules:Schedules", "status:Workflow"}; !slices.Equal(uses, want) {
		t.Errorf("Problem's uses = %v, want %v", uses, want)
	}
	config := string(problem.TableBehaviors()[0].Config)
	for _, text := range []string{`"max":10`, `"minMs":60000`, `"maxMs":2592000000`, `"kinds":["after","every","cron"]`, `"timeZone":"America/New_York"`,
		`"outcomes":["told","resumed","held","missed"]`, `"retry":{"afterMs":300000,"outcome":"missed"}`, `"endOn":{"enters":["done","dropped"]}`, `"permission":"problems.schedule"`} {
		if !strings.Contains(config, text) {
			t.Errorf("Schedules' config = %s, want it to hold %s", config, text)
		}
	}
	var owned []string
	for _, fd := range problem.Fields {
		if fd.Owned() {
			owned = append(owned, fd.Name)
		}
	}
	if want := []string{"status", "statusEnteredAt"}; !slices.Equal(owned, want) {
		t.Errorf("Problem's owned fields = %v, want %v", owned, want)
	}
	sub := db.Types["ProblemSchedulesSchedules"]
	if want := (&ir.BehaviorTable{Row: "Problem", Use: "Schedules", Behavior: "Schedules", Table: "schedules", Key: "name", RowField: "problem"}); sub == nil || sub.Origin != ir.OriginBehavior || sub.Role != ir.RoleDBTable || !reflect.DeepEqual(sub.BehaviorTable, want) {
		t.Errorf("ProblemSchedulesSchedules = %s", marshal(t, sub))
	}
	var fields []string
	for _, fd := range sub.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := "problem:Problem,name:string,kind:string,spec:string,timeZone:string,state:string,setAt:Temporal.DateTime,nextAt:Temporal.DateTime,retryAt:Temporal.DateTime," +
		"runs:Generic.Int64,attempt:Generic.Int64,lastAt:Temporal.DateTime,lastOutcome:string,finishedAt:Temporal.DateTime,endedBy:string"; strings.Join(fields, ",") != want {
		t.Errorf("ProblemSchedulesSchedules' fields =\n%s\nwant\n%s", strings.Join(fields, ","), want)
	}
	for kind, name := range map[string]string{ir.BehaviorQueueDeadlines: "ProblemDeadline", ir.BehaviorQueueChanges: "ProblemChange", ir.BehaviorQueueEvents: "ProblemEvent"} {
		if queue := db.BehaviorQueueOf("Problem", kind); queue == nil || queue.Name != name || !slices.Equal(queue.BehaviorQueue.Behaviors, []string{"Schedules"}) {
			t.Errorf("Problem's %s queue = %s, want %s, Schedules'", kind, marshal(t, queue), name)
		}
	}
	events := db.Types["ProblemEvent"]
	if events.Role != ir.RoleEmbeddedStruct || events.Origin != ir.OriginBehavior || !reflect.DeepEqual(events.Queue, &ir.QueueDef{Key: "rowKey", Serialize: true, Retain: "0s"}) || len(events.Indexes) != 0 {
		t.Errorf("ProblemEvent = %s, want a keyed, serialized queue pruned at once, with no indexes of its own", marshal(t, events))
	}
	fields = nil
	for _, fd := range events.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
	}
	if want := "rowKey:Identity.UUID:required,useId:string:required,kind:string:required,at:Generic.Int64:required,payload:Generic.JSON:optional"; strings.Join(fields, ",") != want {
		t.Errorf("ProblemEvent's fields =\n%s\nwant\n%s", strings.Join(fields, ","), want)
	}
	compareExpanded(t, db, "fixture-schedules-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(schedulesAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixtures' warnings =\n%q\nwant none", warnings)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.BehaviorOperation != nil {
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" "+op.MCP.Handle+" "+op.BehaviorOperation.Use+" "+strings.Join(op.BehaviorOperation.Permissions, ","))
			}
		}
	}
	if want := []string{
		"ProblemActions.schedulesSetSchedule problem/{id}/schedules/set-schedule problem_schedules_set_schedule Schedules problems.schedule",
		"ProblemActions.schedulesPauseSchedule problem/{id}/schedules/pause-schedule problem_schedules_pause_schedule Schedules problems.schedule",
		"ProblemActions.schedulesResumeSchedule problem/{id}/schedules/resume-schedule problem_schedules_resume_schedule Schedules problems.schedule",
		"ProblemActions.schedulesCancelSchedule problem/{id}/schedules/cancel-schedule problem_schedules_cancel_schedule Schedules problems.schedule",
		"ProblemActions.schedulesReportRun problem/{id}/schedules/report-run problem_schedules_report_run Schedules problems.schedule",
		"ProblemActions.schedulesListSchedules problem/{id}/schedules/list-schedules problem_schedules_list_schedules Schedules problems.schedule",
		"ProblemActions.statusTransition problem/{id}/status/transition problem_status_transition status ",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	var worker *ir.Worker
	for _, w := range api.Workers {
		if w.Name == "ProblemWakeUps" {
			worker = w
		}
	}
	if worker == nil || worker.Origin != ir.OriginBehavior || worker.Queue != "ProblemDeadline" || !slices.Equal(worker.Queues, []string{"ProblemDeadline", "ProblemChange", "ProblemEvent"}) ||
		!slices.Equal(worker.BehaviorWorker.Permissions, []string{"problems.schedule"}) {
		t.Fatalf("ProblemWakeUps = %s", marshal(t, worker))
	}
	if len(worker.Events) != 1 {
		t.Fatalf("ProblemWakeUps' events = %s, want run alone", marshal(t, worker.Events))
	}
	run := worker.Events[0]
	run.Description = ""
	if want := (ir.BehaviorWorkerEvent{Use: "Schedules", Member: "schedules", Behavior: "Schedules", Kind: "run", Method: "ProblemWakeUpsSchedulesRun", Event: "ProblemSchedulesRunEvent", Payload: "ProblemSchedulesRunPayload"}); run != want {
		t.Errorf("ProblemWakeUps' run = %+v, want %+v", run, want)
	}
	typeFields := func(name string) []string {
		td := api.Types[name]
		if td == nil {
			t.Fatalf("%s was not added", name)
		}
		if td.Origin != ir.OriginBehavior || td.Owner != api.Name || (strings.HasSuffix(name, "Input") != (td.Role == ir.RoleAPIInput)) {
			t.Errorf("%s = %s, want the behavior's of %s, an input or an embedded struct", name, marshal(t, td), api.Name)
		}
		var out []string
		for _, fd := range td.Fields {
			out = append(out, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
		}
		return out
	}
	for name, want := range map[string][]string{
		"ProblemSchedulesRunEvent": {"key:Identity.UUID:required", "at:Generic.Int64:required", "payload:ProblemSchedulesRunPayload:required"},
		"ProblemSchedulesRunPayload": {"attempt:Generic.Int64:required", "dueAt:Generic.Int64:required", "firedAt:Generic.Int64:required", "kind:ProblemSchedulesKind:required",
			"name:string:required", "nextAt:Generic.Int64:optional", "run:Generic.Int64:required", "setAt:Generic.Int64:required", "spec:string:required", "timeZone:string:optional"},
		"ProblemSchedulesSetScheduleInput": {"after:Generic.Int64:optional", "cron:string:optional", "every:Generic.Int64:optional", "name:string:required", "timeZone:string:optional"},
		"ProblemSchedulesReportRunInput":   {"attempt:Generic.Int64:optional", "name:string:required", "outcome:ProblemSchedulesOutcome:required", "run:Generic.Int64:required"},
		// An output keeps the declaration's types, so it reads an outcome a
		// later config dropped.
		"ProblemSchedulesReportRunResult": {"name:string:required", "outcome:string:required", "retryAt:Generic.Int64:optional", "run:Generic.Int64:required", "unchanged:boolean:optional"},
	} {
		if got := typeFields(name); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string][]string{
		"ProblemSchedulesKind":    {"after", "every", "cron"},
		"ProblemSchedulesOutcome": {"told", "resumed", "held", "missed"},
		"ProblemSchedulesState":   {"active", "paused", "finished"},
		"ProblemSchedulesEndedBy": {"run", "cancel", "endOn"},
	} {
		if enum := api.Enums[name]; enum == nil || !slices.Equal(enumNames(enum), want) {
			t.Errorf("%s = %s, want %v", name, marshal(t, enum), want)
		}
	}
	compareExpanded(t, api, "fixture-schedules-api.expanded.json")
}

// TestSchedulesWorkerWarning: the API of fixture-schedules-api's
// ProblemActions alone, which declares no @behaviorWorker(Problem), loads
// with the warning that Problem's deadlines, changes and events wait for a
// worker, naming Schedules' event run (D104); nothing refuses it.
func TestSchedulesWorkerWarning(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(schedulesDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "fixture-schedules-api", "kind": "API", "dependencies": [{"name": "fixture-schedules-db", "kind": "DB"}], "outputs": {}}`,
		"src/problems.schema.json": `{"imports": [{"package": "@schemas/fixture-schedules-db", "types": ["Problem"]}],
		  "operationSets": [{"name": "ProblemActions", "operations": [], "behaviorOperations": {"type": "Problem"}, "auth": true}]}`,
	})
	var warnings []string
	api, err := LoadService(dir, WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, os.ErrNotExist
		}
		return db, nil
	}), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	want := "ProblemActions: Problem's behaviors keep deadlines (Schedules), changes (Schedules) and events (Schedules: run) for a worker to run, and fixture-schedules-api declares no @behaviorWorker(Problem): nothing runs them until a worker does; declare one on this API or another over fixture-schedules-db (D103, D104)"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
	if api.Types["ProblemSchedulesRunEvent"] != nil {
		t.Errorf("an API with no behavior worker holds ProblemSchedulesRunEvent: only a worker hands an event to a handler")
	}
}

// TestSchedulesFromTheDataForms: a JSON table's `{ name: "Schedules",
// implements: true }` loads as the TypeScript form's does, with Schedules'
// sub-table and Problem's deadline, change and event queues (D104).
func TestSchedulesFromTheDataForms(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(schedulesDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	td := db.Types["Problem"]
	behaviors := make([]any, 0, len(td.Behaviors))
	for _, ref := range td.Behaviors {
		use := map[string]any{"name": ref.Name, "config": json.RawMessage(ref.Config)}
		if ref.Implements {
			use["implements"] = true
		} else {
			use["field"] = ref.Field
		}
		behaviors = append(behaviors, use)
	}
	var fields []any
	for _, fd := range td.Fields {
		if fd.Owned() {
			continue
		}
		f := map[string]any{"name": fd.Name, "typeRef": map[string]any{"name": fd.TypeRef.Name}, "required": fd.Required}
		if fd.Key {
			f["key"], f["autoGenerated"] = true, true
		}
		fields = append(fields, f)
	}
	data, err := json.MarshalIndent(map[string]any{
		"name": "schedules-db", "kind": "DB",
		"scalars": map[string]any{"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"}},
		"types":   map[string]any{"Problem": map[string]any{"name": "Problem", "role": "DBTable", "behaviors": behaviors, "fields": fields}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(writeService(t, map[string]string{
		"schema.config.json":        `{"name": "schedules-db", "kind": "DB", "outputs": {}}`,
		"src/schedules.schema.json": string(data),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Types["Problem"]; got == nil || !got.Optimistic || len(got.ImplementsBehaviors()) != 1 || got.IsEngineType() {
		t.Errorf("Problem = %s", marshal(t, got))
	}
	for _, name := range []string{"ProblemSchedulesSchedules", "ProblemDeadline", "ProblemChange", "ProblemEvent"} {
		if schema.Types[name] == nil {
			t.Errorf("types = %v, want %s", keysOf(schema.Types), name)
		}
	}
}
