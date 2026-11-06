package verify_test

import (
	"os"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// schedules gives Task an implements Schedules use of config, first in its
// list, as fixture-schedules-db's Problem implements it (D104).
func schedules(config map[string]any) func(doc, task map[string]any) {
	return func(_, task map[string]any) {
		task["behaviors"] = append([]any{map[string]any{"name": "Schedules", "implements": true, "config": config}}, task["behaviors"].([]any)...)
	}
}

// schedulesAlone gives Task the implements Schedules use of config alone,
// so a check that reads its Workflow meets a table with none.
func schedulesAlone(config map[string]any) func(doc, task map[string]any) {
	return func(_, task map[string]any) {
		task["behaviors"] = []any{map[string]any{"name": "Schedules", "implements": true, "config": config}}
	}
}

// schedulesConfig is the fewest members of a Schedules config, with more's
// beside them.
func schedulesConfig(more map[string]any) map[string]any {
	config := map[string]any{"max": 1, "kinds": []any{"every"}, "outcomes": []any{"told"}}
	for key, value := range more {
		config[key] = value
	}
	return config
}

// TestSchedulesConfigIsHeldToTheTable (D104): a table's Schedules config is
// held to itself and the table in the core's words and order, the first
// problem alone: minMs no more than maxMs, a minute and thirty days when
// absent; a timeZone with cron among kinds; a retry outcome of outcomes and
// a retry afterMs within the bounds; a status with an endOn; and endOn's
// Workflow, the table's one or the one status names, holding each state it
// enters. A timeZone the IANA database does not hold is the core's to
// refuse, at the table's first command, and loads here.
func TestSchedulesConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Task: behavior Schedules config: "
	endOn := func(states ...any) map[string]any { return map[string]any{"enters": states} }
	holdRefusals(t, "Schedules", []refusalCase{
		{"minMs over maxMs", schedules(schedulesConfig(map[string]any{"minMs": 600000, "maxMs": 300000})),
			prefix + "minMs (600000) is more than maxMs (300000): no delay or interval lies between them"},
		{"the default minMs over maxMs", schedules(schedulesConfig(map[string]any{"maxMs": 30000})),
			prefix + "minMs (60000) is more than maxMs (30000): no delay or interval lies between them"},
		{"a timeZone with no cron", schedules(schedulesConfig(map[string]any{"timeZone": "UTC"})),
			prefix + "timeZone names the zone a cron line is read in, and kinds leaves out cron"},
		{"a retry outcome outcomes lacks", schedules(schedulesConfig(map[string]any{"outcomes": []any{"told", "missed"}, "retry": map[string]any{"outcome": "lost", "afterMs": 300000}})),
			prefix + `retry.outcome "lost" is not one of outcomes (told, missed)`},
		{"a retry under minMs", schedules(schedulesConfig(map[string]any{"retry": map[string]any{"outcome": "told", "afterMs": 1000}})),
			prefix + "retry.afterMs (1000) is outside minMs..maxMs (60000..2592000000): a retry is held to the bounds a schedule is"},
		{"a retry over maxMs", schedules(schedulesConfig(map[string]any{"maxMs": 600000, "retry": map[string]any{"outcome": "told", "afterMs": 900000}})),
			prefix + "retry.afterMs (900000) is outside minMs..maxMs (60000..600000): a retry is held to the bounds a schedule is"},
		{"a status with no endOn", schedules(schedulesConfig(map[string]any{"status": "status"})),
			prefix + "status names the Workflow endOn reads, and the config gives no endOn"},
		{"an endOn on a table with no Workflow", schedulesAlone(schedulesConfig(map[string]any{"endOn": endOn("done")})),
			prefix + "it reads the status of Workflow, which the type does not list"},
		{"an endOn among several Workflows, none named", edits(func(_, task map[string]any) { review(task) }, schedules(schedulesConfig(map[string]any{"endOn": endOn("done")}))),
			prefix + "it reads the status of Workflow, and the type has several (status, review): status names the field whose Workflow it reads"},
		{"a status that is no Workflow", schedules(schedulesConfig(map[string]any{"endOn": endOn("done"), "status": "phase"})),
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"an endOn state the Workflow lacks", schedules(schedulesConfig(map[string]any{"endOn": endOn("done", "closed")})),
			prefix + `endOn.enters names "closed", which is not a state of the type's Workflow (planned, working, proving, done, cancelled)`},
		{"an endOn state of the Workflow status names", edits(func(_, task map[string]any) { review(task) }, schedules(schedulesConfig(map[string]any{"endOn": endOn("done"), "status": "review"}))),
			prefix + `endOn.enters names "done", which is not a state of the type's Workflow (pending, reviewed)`},
		{"the first problem alone", schedules(schedulesConfig(map[string]any{"minMs": 600000, "maxMs": 300000, "timeZone": "UTC", "status": "status"})),
			prefix + "minMs (600000) is more than maxMs (300000): no delay or interval lies between them"},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"the fewest members": schedules(schedulesConfig(nil)),
		"every member": schedules(map[string]any{
			"max": 10, "minMs": 60000, "maxMs": 2592000000, "kinds": []any{"after", "every", "cron"}, "timeZone": "America/New_York",
			"outcomes": []any{"told", "resumed", "held", "missed"}, "retry": map[string]any{"outcome": "missed", "afterMs": 300000},
			"endOn": endOn("done", "cancelled"), "status": "status", "permission": "tasks.schedule",
		}),
		"a zone the build does not hold to the database": schedules(schedulesConfig(map[string]any{"kinds": []any{"cron"}, "timeZone": "Mars/Olympus"})),
		"an endOn of the Workflow status names":          edits(schedulesAlone(schedulesConfig(map[string]any{"endOn": endOn("reviewed"), "status": "review"})), func(_, task map[string]any) { review(task) }),
		"no endOn on a table with no Workflow":           schedulesAlone(schedulesConfig(nil)),
	})
}

// TestSchedulesConfigShapeIsTheDeclarations (D104): a config of the wrong
// shape is refused by the declaration's configSchema, before the table's
// checks: max is needed and at most 100, a kind is after, every or cron, a
// config names no member but its own.
func TestSchedulesConfigShapeIsTheDeclarations(t *testing.T) {
	for name, config := range map[string]map[string]any{
		"no max":         {"kinds": []any{"every"}, "outcomes": []any{"told"}},
		"a max over 100": schedulesConfig(map[string]any{"max": 101}),
		"a kind weekly":  schedulesConfig(map[string]any{"kinds": []any{"weekly"}}),
		"no kinds":       schedulesConfig(map[string]any{"kinds": []any{}}),
		"an extra every": schedulesConfig(map[string]any{"every": 60000}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadTasks(t, schedules(config))
			if err == nil || !strings.Contains(err.Error(), `type "Task": behavior Schedules config: jsonschema validation failed`) {
				t.Fatalf("LoadService = %v, want Schedules' config refused", err)
			}
		})
	}
}

// TestAnEngineTypeRefusesSchedules (D104): Schedules is the behaviors'
// core's alone, which no engine implements, so an engine type's @behavior
// use of it is refused, naming the table form.
func TestAnEngineTypeRefusesSchedules(t *testing.T) {
	const want = "type Note: behavior Schedules is implemented by the behaviors' core alone, which runs it on a table, and no engine implements it: write implements Schedules<Note, { ... }> on a DB table (D104)"
	_, err := loadTasks(t, func(doc, _ map[string]any) {
		doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": []any{
			map[string]any{"name": "Schedules", "config": schedulesConfig(nil)},
		}, "fields": []any{
			taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
		}}
	})
	if err == nil || !strings.Contains(err.Error(), want) || strings.Count(err.Error(), "behaviors' core alone") != 1 {
		t.Fatalf("LoadService = %v, want one problem, %q", err, want)
	}
}

// TestATableThatEmitsEventsKeepsAnEventQueue (D104): a table whose
// behaviors emit events gets its event queue, whose name no type of the
// schema may take, in the words the deadline and change queues' are.
func TestATableThatEmitsEventsKeepsAnEventQueue(t *testing.T) {
	schema, err := loadTasks(t, schedules(schedulesConfig(nil)))
	if err != nil {
		t.Fatal(err)
	}
	queue := schema.BehaviorQueueOf("Task", ir.BehaviorQueueEvents)
	if queue == nil || queue.Name != "TaskEvent" || queue.Queue == nil || queue.Queue.Key != "rowKey" || !queue.Queue.Serialize || queue.Queue.Retain != "0s" {
		t.Fatalf("Task's event queue = %+v", queue)
	}
	_, err = loadTasks(t, edits(schedules(schedulesConfig(nil)), func(doc, _ map[string]any) {
		doc["types"].(map[string]any)["TaskEvent"] = map[string]any{"name": "TaskEvent", "role": "EmbeddedStruct", "fields": []any{taskField("note", "string", nil)}}
	}))
	if want := "type Task: its behaviors emit events (Schedules), whose queue the build adds as TaskEvent (task_event_queue), which the schema also declares; rename that type (D104)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadService = %v, want %q", err, want)
	}
}

// workerLoad loads an API over tasks-db, edited, that imports Task and
// Ping, serves Task's behavior operations, and declares the types and
// workers given, and returns its warnings and its error.
func workerLoad(t *testing.T, edit func(doc, task map[string]any), types, workers string) ([]string, error) {
	t.Helper()
	db, err := loader.LoadService(writeTasks(t, edits(edit, func(doc, _ map[string]any) {
		doc["types"].(map[string]any)["Ping"] = map[string]any{"name": "Ping", "role": "EmbeddedStruct", "queue": map[string]any{}, "fields": []any{taskField("note", "string", nil)}}
	})))
	if err != nil {
		t.Fatal(err)
	}
	api := writeFiles(t, map[string]string{
		"schema.config.json": `{"name": "tasks-api", "kind": "API", "dependencies": [{"name": "tasks-db", "kind": "DB"}], "outputs": {}}`,
		"src/tasks.schema.json": `{"imports": [{"package": "@schemas/tasks-db", "types": ["Task", "Ping"]}],
		  "scalars": {"Identity.UUID": {"name": "Identity.UUID", "languagePrimitive": "string"}},
		  "types": ` + types + `,
		  "operationSets": [{"name": "TaskActions", "operations": [], "behaviorOperations": {"type": "Task"}, "auth": true}],
		  "workers": ` + workers + `}`,
	})
	var warnings []string
	_, err = loader.LoadService(api, loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, os.ErrNotExist
		}
		return db, nil
	}), loader.WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	return warnings, err
}

// TestABehaviorWorkerOfAnEmittingTableTakesNoNameTheAPIHolds (D104): the
// Workers method a behavior worker hands an event to is no @worker's name,
// and the types the loader adds for an event and its payload are no type
// the API declares; each is refused, naming the worker, the table, the use
// and the event. A worker whose names are free loads with no warning.
func TestABehaviorWorkerOfAnEmittingTableTakesNoNameTheAPIHolds(t *testing.T) {
	const wakeUps = `{"name": "TaskWakeUps", "behaviorWorker": {"type": "Task"}}`
	for _, tc := range []struct {
		name, types, workers, want string
	}{
		{"a @worker of the method's name", `{}`, `[` + wakeUps + `, {"name": "TaskWakeUpsSchedulesRun", "queue": "Ping"}]`,
			"TaskWakeUps: @behaviorWorker(Task) hands Schedules's event run to the Workers method TaskWakeUpsSchedulesRun, which is the name of the @worker TaskWakeUpsSchedulesRun: rename one (D104)"},
		{"a type of the payload's name", `{"TaskSchedulesRunPayload": {"name": "TaskSchedulesRunPayload", "role": "EmbeddedStruct", "fields": [{"name": "note", "typeRef": {"name": "string"}}]}}`, `[` + wakeUps + `]`,
			"TaskWakeUps: @behaviorWorker(Task) adds the type TaskSchedulesRunPayload for Schedules's event run, which tasks-api declares: rename it (D104)"},
		{"a type of the event's name", `{"TaskSchedulesRunEvent": {"name": "TaskSchedulesRunEvent", "role": "EmbeddedStruct", "fields": [{"name": "note", "typeRef": {"name": "string"}}]}}`, `[` + wakeUps + `]`,
			"TaskWakeUps: @behaviorWorker(Task) adds the type TaskSchedulesRunEvent for Schedules's event run, which tasks-api declares: rename it (D104)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := workerLoad(t, schedules(schedulesConfig(nil)), tc.types, tc.workers); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}
	if warnings, err := workerLoad(t, schedules(schedulesConfig(nil)), `{}`, `[`+wakeUps+`]`); err != nil || len(warnings) != 0 {
		t.Fatalf("LoadService = %v, warnings %q, want neither", err, warnings)
	}
}

// TestSchedulesWorkerWarningNamesTheEventsKept (D104): an API that serves the behavior
// operations of a table whose behaviors emit events, and declares no
// @behaviorWorker of it, is warned, naming the events kept beside the
// deadlines and the changes; nothing refuses it, and its messages wait.
func TestSchedulesWorkerWarningNamesTheEventsKept(t *testing.T) {
	warnings, err := workerLoad(t, schedules(schedulesConfig(nil)), `{}`, `[]`)
	if err != nil {
		t.Fatal(err)
	}
	want := "TaskActions: Task's behaviors keep deadlines (Schedules), changes (Schedules) and events (Schedules: run) for a worker to run, and tasks-api declares no @behaviorWorker(Task): nothing runs them until a worker does; declare one on this API or another over tasks-db (D103, D104)"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
}
