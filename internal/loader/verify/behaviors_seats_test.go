package verify_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// seats gives Task an implements Seats use of the seats given, first in its
// list, as fixture-seats-db's Problem implements it (D104).
func seats(seats map[string]any) func(doc, task map[string]any) {
	return func(_, task map[string]any) {
		task["behaviors"] = append([]any{map[string]any{"name": "Seats", "implements": true, "config": map[string]any{"seats": seats}}}, task["behaviors"].([]any)...)
	}
}

// edits applies each edit in turn.
func edits(each ...func(doc, task map[string]any)) func(doc, task map[string]any) {
	return func(doc, task map[string]any) {
		for _, edit := range each {
			edit(doc, task)
		}
	}
}

// TestSeatsConfigIsHeldToTheTable (D104): a table's Seats config is held to
// the table in the core's words, each seat by code point: an onFull with no
// max, and a fromField that is no field of the table or holds no subject;
// and in the build's, as Presence's principalField is, a fromField a
// behavior owns, an audit field, a list and a field encrypted at rest.
// Seats with a fromField that holds a subject, and none, load.
func TestSeatsConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Task: behavior Seats config: "
	from := func(field string) func(doc, task map[string]any) {
		return seats(map[string]any{"responsible": map[string]any{"fromField": field, "take": "tasks.work"}})
	}
	holdRefusals(t, "Seats", []refusalCase{
		{"an onFull with no max", seats(map[string]any{"handles": map[string]any{"onFull": "refuse"}}),
			prefix + "seats.handles.onFull names what a full seat does, and handles has no max to fill"},
		{"a fromField that is no field", from("assignee"),
			prefix + "seats.responsible.fromField: assignee is not a field of Task (its fields: " + taskFields + ")"},
		{"a fromField that holds no subject", from("urgent"),
			prefix + "seats.responsible.fromField names urgent, a boolean field of Task: fromField names a field that holds a subject (string, Identity.UUID or Identity.UserID)"},
		{"a fromField a behavior owns", from("status"),
			prefix + `seats.responsible.fromField "status" is a field Workflow owns, which only its operations change: fromField names the table's own field that holds a subject`},
		{"an audit fromField", from("createdBy"),
			prefix + `seats.responsible.fromField "createdBy" is an audit field the writes stamp: fromField names the table's own field that holds a subject`},
		{"a list", edits(func(_, task map[string]any) {
			addField(task, map[string]any{"name": "helpers", "typeRef": map[string]any{"name": "Identity.UUID", "isArray": true}})
		}, from("helpers")), prefix + `seats.responsible.fromField "helpers" is a list: fromField names a field that holds one subject`},
		{"a field encrypted at rest", edits(func(_, task map[string]any) {
			addField(task, taskField("agent", "string", map[string]any{"encryptedAtRest": true}))
		}, from("agent")), prefix + `seats.responsible.fromField "agent" is encrypted at rest: the core reads no value of it`},
		{"the first seat by code point", seats(map[string]any{
			"zeta":  map[string]any{"onFull": "displace"},
			"alpha": map[string]any{"fromField": "plan2"},
		}), prefix + "seats.alpha.fromField: plan2 is not a field of Task (its fields: " + taskFields + ")"},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"a fromField that holds a subject": from("reviewer"),
		"a seat of every member": seats(map[string]any{
			"accountable": map[string]any{"max": 1, "onFull": "displace", "take": "tasks.own", "assign": "tasks.assign"},
			"responsible": map[string]any{"fromField": "owner", "take": "tasks.work", "assign": "tasks.assign"},
			"handles":     map[string]any{"max": 5, "assign": "tasks.release"},
		}),
	})
}

// TestByNamesASeatOfTheTablesSeats (D104): a Workflow transition's by and a
// StateRules state's by may name a seat of the table's Seats as
// seat:<name>; one of a table that implements no Seats, or that its Seats
// lacks, is refused in the core's words, the seats listed by code point. A
// seat entry is never looked up as a field, so a by of fields alone reads
// as before.
func TestByNamesASeatOfTheTablesSeats(t *testing.T) {
	const (
		workflowPrefix = `type Task: field status: behavior Workflow config: the transition from "planned" to "working" names `
		stateRules     = `type Task: behavior StateRules config: states.working.by names `
	)
	twoSeats := seats(map[string]any{"responsible": map[string]any{"fromField": "reviewer", "take": "tasks.work"}, "accountable": map[string]any{"max": 1, "take": "tasks.own"}})
	transitionBy := func(by ...any) func(doc, task map[string]any) {
		return func(_, task map[string]any) {
			config(task, "Workflow")["transitions"].([]any)[0].(map[string]any)["by"] = by
		}
	}
	stateBy := func(by ...any) func(doc, task map[string]any) {
		return func(_, task map[string]any) {
			config(task, "StateRules")["states"].(map[string]any)["working"] = map[string]any{"frozen": []any{"plan"}, "by": by}
		}
	}
	holdRefusals(t, "Workflow", []refusalCase{
		{"a transition's seat on a table with no Seats", transitionBy("owner", "seat:responsible"),
			workflowPrefix + `"seat:responsible" in by, a seat of Seats, which Task does not implement`},
		{"a transition's seat the table's Seats lacks", edits(twoSeats, transitionBy("seat:owners")),
			workflowPrefix + `"seat:owners" in by, which is not a seat of Task's Seats (its seats: accountable, responsible)`},
	})
	holdRefusals(t, "StateRules", []refusalCase{
		{"a state's seat on a table with no Seats", stateBy("seat:accountable"),
			stateRules + `"seat:accountable", a seat of Seats, which Task does not implement`},
		{"a state's seat the table's Seats lacks", edits(twoSeats, stateBy("reviewer", "seat:owner")),
			stateRules + `"seat:owner", which is not a seat of Task's Seats (its seats: accountable, responsible)`},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"seats a transition and a state name": edits(twoSeats, transitionBy("owner", "seat:responsible"), stateBy("seat:accountable", "reviewer")),
	})
}

// TestAByOnASeatAnyCallerTakesWarns (D104): a by that names a seat whose
// take permission is absent is warned of, never refused, once per entry at
// its first naming: any caller may take the seat and so pass the by. A
// seat that names take draws none.
func TestAByOnASeatAnyCallerTakesWarns(t *testing.T) {
	takeless := seats(map[string]any{"accountable": map[string]any{"max": 1}, "responsible": map[string]any{"take": "tasks.work"}})
	edit := edits(takeless, func(_, task map[string]any) {
		transitions := config(task, "Workflow")["transitions"].([]any)
		transitions[0].(map[string]any)["by"] = []any{"seat:accountable", "seat:responsible"}
		transitions[1].(map[string]any)["by"] = []any{"seat:accountable"}
		config(task, "StateRules")["states"].(map[string]any)["working"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"seat:responsible", "seat:accountable"}}
	})
	var warnings []string
	if _, err := loader.LoadService(writeTasks(t, edit), loader.WithWarnings(func(d *verify.Diagnostic) {
		if strings.Contains(d.Msg, "(D104)") {
			warnings = append(warnings, d.Msg)
		}
	})); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`type Task: behavior StateRules config: states.working.by names "seat:accountable", and Seats' accountable names no take permission: any caller may take it and so change what working freezes; name a take permission for accountable (D104)`,
		`type Task: field status: behavior Workflow config: the transition from "planned" to "working" names "seat:accountable" in by, and Seats' accountable names no take permission: any caller may take it and so make the move; name a take permission for accountable (D104)`,
	}
	if !slices.Equal(warnings, want) {
		t.Errorf("warnings =\n%q\nwant\n%q", warnings, want)
	}
}

// TestWritableByWarnsOfASeatsFromField (D104): the warning of an update
// that takes a field a by names covers the fromField of a seat a by names,
// which seats whom it holds: a caller who may write it seats itself and
// then makes the move or changes what the state freezes. A seat that names
// no fromField warns of nothing, and Constants' fields draw no warning.
func TestWritableByWarnsOfASeatsFromField(t *testing.T) {
	const view = `{"TaskEdit": {"name": "TaskEdit", "role": "APIView", "source": {"target": "tasks-db.Task", "omittedFromSource": ["id", "title", "owner", "plan", "proof", "urgent", "attempts"]}, "fields": [{"name": "reviewer", "typeRef": {"name": "Identity.UUID"}}]}}`
	const sets = `[{"name": "TaskActions", "operations": [], "behaviorOperations": {"type": "Task"}, "auth": true}, {"name": "TaskEdits", "operations": [{"name": "edit", "typeRef": {"name": "Task"}, "required": true, "httpMethod": "PATCH", "restPath": "tasks/{id}",
	  "arguments": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true}, {"name": "input", "typeRef": {"name": "TaskEdit"}, "required": true}]}]}]`
	bySeat := func(seat string) func(doc, task map[string]any) {
		return func(_, task map[string]any) {
			config(task, "StateRules")["states"].(map[string]any)["working"] = map[string]any{"frozen": []any{"plan"}, "by": []any{"seat:" + seat}}
			config(task, "Workflow")["transitions"].([]any)[3].(map[string]any)["by"] = []any{"seat:" + seat}
		}
	}
	const (
		stateRules = "tasks-api: TaskEdits.edit takes Task.reviewer (TaskEdit is @source(Task)), and Task's StateRules names seat:responsible in by for working, whose fromField is reviewer: a caller who may call edit can set reviewer to itself, which seats it in responsible, and then change the fields working freezes. Keep reviewer out of the operation's input, or freeze it: list it in the fields of Task's Constants (D104)"
		workflow   = "tasks-api: TaskEdits.edit takes Task.reviewer (TaskEdit is @source(Task)), and the Workflow on Task.status names seat:responsible in by for proving to done, whose fromField is reviewer: a caller who may call edit can set reviewer to itself, which seats it in responsible, and then make that move. Keep reviewer out of the operation's input, or freeze it: list it in the fields of Task's Constants (D104)"
	)
	for _, tc := range []struct {
		name string
		edit func(doc, task map[string]any)
		want []string
	}{
		{"a seat's fromField", edits(seats(map[string]any{"responsible": map[string]any{"fromField": "reviewer", "take": "tasks.work"}}), bySeat("responsible")), []string{stateRules, workflow}},
		{"a seat with no fromField", edits(seats(map[string]any{"responsible": map[string]any{"take": "tasks.work"}}), bySeat("responsible")), nil},
		{"a fromField Constants lists", edits(seats(map[string]any{"responsible": map[string]any{"fromField": "reviewer", "take": "tasks.work"}}), bySeat("responsible"), func(_, task map[string]any) {
			config(task, "Constants")["fields"] = []any{"owner", "reviewer"}
		}), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := warningsOf(t, tc.edit, view, sets); !slices.Equal(got, tc.want) {
				t.Errorf("warnings =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// sandboxFlow is the Workflow of a sandbox core-reconcile.json writes
// (FLOW): a stop needs sandboxes.stop, and every state but failed reaches
// lost, from which every state is reached.
func sandboxFlow() map[string]any {
	pairs := [][2]string{
		{"pending", "starting"}, {"starting", "running"}, {"running", "stopping"}, {"stopping", "stopped"}, {"stopped", "starting"},
		{"pending", "failed"}, {"starting", "failed"}, {"running", "failed"}, {"stopping", "failed"},
		{"pending", "lost"}, {"starting", "lost"}, {"running", "lost"}, {"stopping", "lost"}, {"stopped", "lost"},
		{"lost", "pending"}, {"lost", "starting"}, {"lost", "running"}, {"lost", "stopping"}, {"lost", "stopped"}, {"lost", "failed"},
	}
	var transitions []any
	for _, pair := range pairs {
		transition := map[string]any{"from": pair[0], "to": pair[1]}
		if pair == [2]string{"running", "stopping"} {
			transition["permission"] = "sandboxes.stop"
		}
		transitions = append(transitions, transition)
	}
	return map[string]any{"states": []any{"pending", "starting", "running", "stopping", "stopped", "failed", "lost"}, "transitions": transitions}
}

// watch is Reconcile's config core-reconcile.json writes (WATCH), with the
// members given replacing its own.
func watch(members map[string]any) map[string]any {
	config := map[string]any{"status": "status", "unreachable": "lost", "staleAfterMs": 60000, "observePermission": "sandboxes.observe"}
	for k, v := range members {
		if v == nil {
			delete(config, k)
			continue
		}
		config[k] = v
	}
	return config
}

// loadSandboxes loads a DB schema of one table, Sandbox, whose behaviors are
// those given, its Workflow on status and its Reconcile on observed.
func loadSandboxes(t *testing.T, behaviors ...any) (*ir.Schema, error) {
	t.Helper()
	doc := map[string]any{
		"name": "sandboxes-db", "kind": "DB",
		"scalars": map[string]any{"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"}},
		"types": map[string]any{"Sandbox": map[string]any{"name": "Sandbox", "role": "DBTable", "behaviors": behaviors, "fields": []any{
			taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true, "autoGenerated": true}),
			taskField("name", "string", map[string]any{"required": true}),
		}}},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return loader.LoadService(writeFiles(t, map[string]string{
		"schema.config.json":        `{"name": "sandboxes-db", "kind": "DB", "outputs": {}}`,
		"src/sandboxes.schema.json": string(data),
	}))
}

// TestReconcileConfigIsHeldToTheTable (D104): a Reconcile field's config is
// held to the table in the core's words: status names a Workflow of the
// table; unreachable and staleAfterMs come together; unreachable is a state
// of that Workflow, which every state but a terminal one reaches along its
// transitions, and from which every other state is reached. Each is `type
// Sandbox: field observed: behavior Reconcile config: <reason>`, the first.
func TestReconcileConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Sandbox: field observed: behavior Reconcile config: "
	workflow := func(config map[string]any) map[string]any {
		return map[string]any{"name": "Workflow", "field": "status", "config": config}
	}
	reconcile := func(config map[string]any) map[string]any {
		return map[string]any{"name": "Reconcile", "field": "observed", "config": config}
	}
	withoutLostToPending := sandboxFlow()
	var kept []any
	for _, transition := range withoutLostToPending["transitions"].([]any) {
		if m := transition.(map[string]any); m["from"] != "lost" || m["to"] != "pending" {
			kept = append(kept, transition)
		}
	}
	withoutLostToPending["transitions"] = kept
	paused := map[string]any{"states": []any{"pending", "running", "paused", "failed", "lost"}, "transitions": []any{
		map[string]any{"from": "pending", "to": "running"}, map[string]any{"from": "running", "to": "paused"}, map[string]any{"from": "paused", "to": "running"},
		map[string]any{"from": "running", "to": "failed"}, map[string]any{"from": "pending", "to": "lost"}, map[string]any{"from": "lost", "to": "pending"},
		map[string]any{"from": "lost", "to": "running"}, map[string]any{"from": "lost", "to": "paused"}, map[string]any{"from": "lost", "to": "failed"},
	}}
	for _, tc := range []struct {
		name      string
		behaviors []any
		want      string
	}{
		{"a status that names no Workflow", []any{workflow(sandboxFlow()), reconcile(watch(map[string]any{"status": "phase"}))},
			prefix + `status "phase" is not a Workflow of the type (its Workflows: status)`},
		{"an unreachable with no staleAfterMs", []any{workflow(sandboxFlow()), reconcile(watch(map[string]any{"staleAfterMs": nil}))},
			prefix + "unreachable names the state the stale deadline moves a row to, and the config gives no staleAfterMs to arm it"},
		{"a staleAfterMs with no unreachable", []any{workflow(sandboxFlow()), reconcile(watch(map[string]any{"unreachable": nil}))},
			prefix + "staleAfterMs arms a deadline that moves a row to unreachable, and the config names no unreachable state"},
		{"an unreachable that is no state", []any{workflow(sandboxFlow()), reconcile(watch(map[string]any{"unreachable": "gone"}))},
			prefix + `unreachable "gone" is not a state of the type's Workflow (pending, starting, running, stopping, stopped, failed, lost)`},
		{"an unreachable a state other than a terminal one cannot reach", []any{workflow(paused), reconcile(watch(nil))},
			prefix + `unreachable "lost" cannot be reached from "running", which is not a terminal state: the stale deadline moves a row there from any state but a terminal one`},
		{"a state the unreachable cannot reach", []any{workflow(withoutLostToPending), reconcile(watch(nil))},
			prefix + `"pending" cannot be reached from unreachable "lost": an observation moves a row from it to any state`},
		{"a table with no Workflow", []any{reconcile(watch(nil))},
			prefix + `status "status" is not a Workflow of the type (its Workflows: none)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSandboxes(t, tc.behaviors...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error holding %q", err, tc.want)
			}
			if got := strings.Count(err.Error(), "behavior Reconcile config"); got != 1 {
				t.Errorf("LoadService = %v, want one problem of Reconcile's config", err)
			}
		})
	}
	for name, behaviors := range map[string][]any{
		"WATCH over FLOW":           {workflow(sandboxFlow()), reconcile(watch(nil))},
		"no stale deadline":         {workflow(sandboxFlow()), reconcile(watch(map[string]any{"unreachable": nil, "staleAfterMs": nil}))},
		"a Workflow it reads alone": {workflow(paused), reconcile(map[string]any{"status": "status", "observePermission": "sandboxes.observe"})},
	} {
		if schema, err := loadSandboxes(t, behaviors...); err != nil {
			t.Errorf("%s: LoadService = %v", name, err)
		} else if observed := schema.Types["Sandbox"].Fields[slices.IndexFunc(schema.Types["Sandbox"].Fields, func(fd *ir.FieldDef) bool { return fd.Name == "observed" })]; observed.Required {
			t.Errorf("%s: observed is required, want it optional, absent before the first observation", name)
		}
	}
	// Reconcile is anchored on a field, never implemented by the table.
	if _, err := loadSandboxes(t, workflow(sandboxFlow()), map[string]any{"name": "Reconcile", "implements": true, "config": watch(nil)}); err == nil ||
		!strings.Contains(err.Error(), "type Sandbox implements Reconcile, whose declaration names its value field observed, so it is anchored on a field: write observed: Reconcile<{ ... }> (D103)") {
		t.Errorf("implements Reconcile: LoadService = %v", err)
	}
}

// TestAnEngineTypeRefusesSeatsAndReconcile (D104): Seats and Reconcile are
// the behaviors' core's alone, which no engine implements
// (registry.EngineImplements), so an engine type's @behavior use of either,
// on a DB table and in a General schema alike, is refused, naming the table
// form: implements Seats<T, { ... }> on a DB table, and observed:
// Reconcile<{ ... }> beside a Workflow field.
func TestAnEngineTypeRefusesSeatsAndReconcile(t *testing.T) {
	const (
		seatsRefused     = "type Note: behavior Seats is implemented by the behaviors' core alone, which runs it on a table, and no engine implements it: write implements Seats<Note, { ... }> on a DB table (D104)"
		reconcileRefused = "type Note: behavior Reconcile is implemented by the behaviors' core alone, which runs it on a field of a table, and no engine implements it: write observed: Reconcile<{ ... }> beside a Workflow field on a DB table (D104)"
	)
	note := func(behavior map[string]any) func(doc, _ map[string]any) {
		return func(doc, _ map[string]any) {
			doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": []any{
				map[string]any{"name": "Workflow", "config": map[string]any{"states": []any{"draft", "sent"}, "transitions": []any{map[string]any{"from": "draft", "to": "sent"}}}},
				behavior,
			}, "fields": []any{
				taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
				taskField("author", "string", nil),
			}}
		}
	}
	for _, tc := range []struct {
		name     string
		behavior map[string]any
		want     string
	}{
		{"Seats", map[string]any{"name": "Seats", "config": map[string]any{"seats": map[string]any{"authors": map[string]any{"fromField": "author"}}}}, seatsRefused},
		{"Reconcile", map[string]any{"name": "Reconcile", "config": map[string]any{"status": "Workflow", "observePermission": "notes.observe"}}, reconcileRefused},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTasks(t, note(tc.behavior))
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Count(err.Error(), "behaviors' core alone") != 1 {
				t.Fatalf("LoadService = %v, want one problem, %q", err, tc.want)
			}
		})
	}
}

// TestAnEngineTypeNamingSeatsIsRefusedBeforeEnginedoc (D104): enginedoc,
// engineclientgen and the engine's project generator read only engine types,
// and registry.EngineImplements is false for Seats, Reconcile and
// Schedules, so the build refuses an engine type of a General schema that
// names one when it loads, before any of them reads the schema: none of
// them names any of these behaviors.
func TestAnEngineTypeNamingSeatsIsRefusedBeforeEnginedoc(t *testing.T) {
	reg := registry.New(naming.Default())
	for _, name := range []string{"Seats", "Reconcile", "Schedules"} {
		behavior, ok := reg.Behavior(name)
		if !ok || registry.EngineImplements(behavior) || behavior.Package != registry.RulesPackage {
			t.Fatalf("%s: package %q, engine implements %v; want %s and false", name, behavior.Package, registry.EngineImplements(behavior), registry.RulesPackage)
		}
	}
	general := `{"name": "notes", "kind": "General", "types": {"Note": {"name": "Note", "role": "EmbeddedStruct",
	  "behaviors": [{"name": "Workflow", "config": {"states": ["draft", "sent"], "transitions": [{"from": "draft", "to": "sent"}]}},
	    {"name": "Seats", "config": {"seats": {"authors": {"max": 3}}}}],
	  "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]}}}`
	_, err := loader.LoadService(writeFiles(t, map[string]string{
		"schema.config.json":    `{"name": "notes", "kind": "General", "outputs": {}}`,
		"src/notes.schema.json": general,
	}))
	if want := "type Note: behavior Seats is implemented by the behaviors' core alone, which runs it on a table, and no engine implements it: write implements Seats<Note, { ... }> on a DB table (D104)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadService = %v, want %q", err, want)
	}
}

// TestAnEngineTypesByOfASeatIsRefusedAsAnyBy (D104): an engine type's
// Workflow transition and StateRules state take no by, whatever its
// entries, so a by of seat:<name> alone is refused in the words a by of
// fields is, naming the table form, as the engine refuses it.
func TestAnEngineTypesByOfASeatIsRefusedAsAnyBy(t *testing.T) {
	note := func(workflowBy, stateBy []any) func(doc, _ map[string]any) {
		return func(doc, _ map[string]any) {
			transition := map[string]any{"from": "draft", "to": "sent"}
			behaviors := []any{map[string]any{"name": "Workflow", "config": map[string]any{"states": []any{"draft", "sent"}, "transitions": []any{transition}}}}
			if workflowBy != nil {
				transition["by"] = workflowBy
			}
			if stateBy != nil {
				behaviors = append(behaviors, map[string]any{"name": "StateRules", "config": map[string]any{"states": map[string]any{"sent": map[string]any{"frozen": []any{"title"}, "by": stateBy}}}})
			}
			doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": behaviors, "fields": []any{
				taskField("id", "Identity.UUID", map[string]any{"required": true, "key": true}),
				taskField("title", "string", map[string]any{"required": true}),
			}}
		}
	}
	for _, tc := range []struct {
		name string
		edit func(doc, _ map[string]any)
		want string
	}{
		{"a transition's seat", note([]any{"seat:owners"}, nil),
			`type Note: @behavior Workflow names by on the transition from "draft" to "sent", which a table's Workflow takes (status: Workflow<{ ... }> on a DB table, D104); an engine type's Workflow takes none of by, overridePermission, repeat and finished, so leave it out`},
		{"a state's seat", note(nil, []any{"seat:owners"}),
			`type Note: @behavior StateRules names by in the rules of "sent", which a table's StateRules takes (implements StateRules<Note, C>, D104); an engine type's StateRules takes required, frozen and permission alone, so leave it out`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadTasks(t, tc.edit); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want %q", err, tc.want)
			}
		})
	}
}
