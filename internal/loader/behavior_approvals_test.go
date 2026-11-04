package loader

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D104's second step: a database whose Task implements
// Constants (its owner kept, urgent and attempts moving up), StateRules (its
// plan frozen while planned for its creator alone and while working for
// its reviewer alone, its proof required to enter proving) and Approvals (a
// proving gate whose sign-offs survive edits, resetOn [], and a pinned done
// gate whose sign-offs a change of the proof consumes) beside its status
// Workflow; and an API that serves its behavior operations and an update
// that takes reviewer and owner.
const (
	approvalsDBFixture  = "tsreader/testdata/services/fixture-approvals-db"
	approvalsAPIFixture = "tsreader/testdata/services/fixture-approvals-api"
)

// writableReviewer is the API's one warning: reassignTask takes reviewer,
// which StateRules names in by for working, through a @source(Task) view;
// owner, which the Workflow names in by, draws none, since Constants keeps
// it as its create gave it (D104).
const writableReviewer = "fixture-approvals-api: TaskAdmin.reassignTask takes Task.reviewer (TaskAssignment is @source(Task)), and Task's StateRules names reviewer in by for working: a caller who may call reassignTask can set reviewer to itself and then change the fields working freezes. Keep reviewer out of the operation's input, or freeze it: list it in the fields of Task's Constants (D104)"

// TestApprovalsFixturesLoad: both fixtures load; Task stays a table whose
// uses are Constants, StateRules and Approvals, which it implements, and
// its status, in that order; its configs read as the TypeScript form wrote
// them (resetOn [] a list, pinned true, oneWay an object); the loader adds
// Approvals' signOffs sub-table and neither queue, since none of the three
// arms a deadline or hears a change; the API's set serves Approvals' three
// operations and Workflow's transition, signOff taking the gated state
// (TaskApprovalsGate) and the version its signer read; its one warning is
// that reassignTask can write reviewer; and the expanded IR of both is
// held to its golden (D104).
func TestApprovalsFixturesLoad(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(approvalsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	task := db.Types["Task"]
	if task == nil || task.IsEngineType() || !task.Optimistic {
		t.Fatalf("Task = %s", marshal(t, task))
	}
	var uses []string
	for _, ref := range task.TableBehaviors() {
		uses = append(uses, ref.UseID()+":"+ref.Name)
	}
	if want := []string{"Constants:Constants", "StateRules:StateRules", "Approvals:Approvals", "status:Workflow"}; !slices.Equal(uses, want) {
		t.Errorf("Task's uses = %v, want %v", uses, want)
	}
	configs := map[string]string{}
	for _, ref := range task.ImplementsBehaviors() {
		configs[ref.Name] = string(ref.Config)
	}
	for behavior, held := range map[string][]string{
		"Constants":  {`"oneWay":{"attempts":"up","urgent":"up"}`, `"fields":["owner"]`},
		"StateRules": {`"planned":{"by":["createdBy"],"frozen":["plan"]}`, `"proving":{"required":["proof"]}`},
		"Approvals":  {`"proving":{"count":1,"permission":"tasks.review","resetOn":[]}`, `"pinned":true`, `"resetOn":["proof"]`},
	} {
		for _, text := range held {
			if !strings.Contains(configs[behavior], text) {
				t.Errorf("%s's config = %s, want it to hold %s", behavior, configs[behavior], text)
			}
		}
	}
	sub := db.Types["TaskApprovalsSignOffs"]
	if sub == nil || sub.Origin != ir.OriginBehavior || sub.Role != ir.RoleDBTable ||
		!reflect.DeepEqual(sub.BehaviorTable, &ir.BehaviorTable{Row: "Task", Use: "Approvals", Behavior: "Approvals", Table: "signOffs", Key: "number", RowField: "task"}) {
		t.Errorf("TaskApprovalsSignOffs = %s", marshal(t, sub))
	}
	var fields []string
	for _, fd := range sub.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := "task:Task,number:string,state:string,subject:string,note:string,at:Temporal.DateTime,consumedAt:Temporal.DateTime"; strings.Join(fields, ",") != want {
		t.Errorf("TaskApprovalsSignOffs' fields = %s, want %s", strings.Join(fields, ","), want)
	}
	for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges} {
		if queue := db.BehaviorQueueOf("Task", kind); queue != nil {
			t.Errorf("Task's %s queue = %s, want none", kind, queue.Name)
		}
	}
	compareExpanded(t, db, "fixture-approvals-db.expanded.json")

	var warnings []string
	api, err := LoadService(filepath.FromSlash(approvalsAPIFixture), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{writableReviewer}; !slices.Equal(warnings, want) {
		t.Errorf("the API's warnings =\n%q\nwant\n%q", warnings, want)
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
		"TaskActions.approvalsSignOff task/{id}/approvals/sign-off task_approvals_sign_off Approvals tasks.review",
		"TaskActions.approvalsWithdrawSignOff task/{id}/approvals/withdraw-sign-off task_approvals_withdraw_sign_off Approvals tasks.review",
		"TaskActions.approvalsListSignOffs task/{id}/approvals/list-sign-offs task_approvals_list_sign_offs Approvals tasks.review",
		"TaskActions.statusTransition task/{id}/status/transition task_status_transition status ",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	input := api.Types["TaskApprovalsSignOffInput"]
	if input == nil {
		t.Fatal("TaskApprovalsSignOffInput was not added")
	}
	fields = nil
	for _, fd := range input.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
	}
	if want := []string{"note:string:optional", "state:TaskApprovalsGate:required", "version:Generic.Int64:optional"}; !slices.Equal(fields, want) {
		t.Errorf("TaskApprovalsSignOffInput = %v, want %v", fields, want)
	}
	if gate := api.Enums["TaskApprovalsGate"]; gate == nil || !slices.Equal(enumNames(gate), []string{"done", "proving"}) {
		t.Errorf("TaskApprovalsGate = %s, want done and proving", marshal(t, gate))
	}
	compareExpanded(t, api, "fixture-approvals-api.expanded.json")
}

// enumNames are an enum's values as an instance holds them.
func enumNames(enum *ir.EnumDef) []string {
	var out []string
	for _, value := range enum.Values {
		name := value.Name
		if value.SerializedAs != "" {
			name = value.SerializedAs
		}
		out = append(out, name)
	}
	return out
}

// TestApprovalsFromTheDataForms: a JSON table's `{ name: "Constants",
// implements: true }`, and StateRules' and Approvals', load as the
// TypeScript form's do, with Approvals' sub-table (D104).
func TestApprovalsFromTheDataForms(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(approvalsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	task := db.Types["Task"]
	behaviors := make([]any, 0, len(task.Behaviors))
	for _, ref := range task.Behaviors {
		use := map[string]any{"name": ref.Name, "config": json.RawMessage(ref.Config)}
		if ref.Implements {
			use["implements"] = true
		} else {
			use["field"] = ref.Field
		}
		behaviors = append(behaviors, use)
	}
	field := func(name, typeName string, more map[string]any) map[string]any {
		f := map[string]any{"name": name, "typeRef": map[string]any{"name": typeName}}
		for k, v := range more {
			f[k] = v
		}
		return f
	}
	doc := map[string]any{
		"name": "tasks-db", "kind": "DB",
		"scalars": map[string]any{"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"}},
		"types": map[string]any{"Task": map[string]any{"name": "Task", "role": "DBTable", "behaviors": behaviors, "fields": []any{
			field("id", "Identity.UUID", map[string]any{"required": true, "key": true, "autoGenerated": true}),
			field("title", "string", map[string]any{"required": true}),
			field("owner", "Identity.UUID", map[string]any{"required": true}),
			field("reviewer", "Identity.UUID", nil),
			field("plan", "string", nil),
			field("proof", "string", nil),
			field("urgent", "boolean", map[string]any{"required": true}),
			field("attempts", "number", map[string]any{"required": true}),
			field("createdBy", "Identity.UUID", nil),
		}}},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(writeService(t, map[string]string{
		"schema.config.json":    `{"name": "tasks-db", "kind": "DB", "outputs": {}}`,
		"src/tasks.schema.json": string(data),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if got := schema.Types["Task"]; !got.Optimistic || len(got.ImplementsBehaviors()) != 3 || got.IsEngineType() {
		t.Errorf("Task = %s", marshal(t, got))
	}
	if schema.Types["TaskApprovalsSignOffs"] == nil {
		t.Errorf("types = %v, want the sub-table", keysOf(schema.Types))
	}
}
