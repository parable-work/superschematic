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

// The fixtures of D104's third step's first part: a database whose Problem
// implements Seats (one accountable person the next to take the seat
// displaces, any number of responsible agents with the problem's own agent
// among them through fromField, and at most five handles another releases
// only with problems.release) and StateRules, whose review freezes the
// title for all but the accountable seat, beside a status Workflow whose
// transitions' by name seats; and whose Sandbox's status follows its
// provider's reports through a Reconcile on its observed field, through
// unreachable when no report reaches it for five minutes; and an API that
// serves both tables' behavior operations and runs Sandbox's deadlines.
const (
	seatsDBFixture  = "tsreader/testdata/services/fixture-seats-db"
	seatsAPIFixture = "tsreader/testdata/services/fixture-seats-api"
)

// TestSeatsFixturesLoad: both fixtures load with no warning; Problem stays
// a table whose uses are Seats and StateRules, which it implements, and its
// status, in that order, and Sandbox's are its status and its observed
// Reconcile; the configs read as the TypeScript form wrote them (a seat's
// max, onFull, take, assign and fromField, by naming seats, Reconcile's
// unreachable and staleAfterMs); the loader adds Seats' occupants
// sub-table and no owned field to Problem, and Sandbox's Workflow's two and
// Reconcile's four owned fields, observed not required, the anchor's value
// field though it is, with Sandbox's deadline queue and no change queue
// anywhere; the API's sets serve Seats' three operations, each Workflow's
// transition and Reconcile's observe on a row, every seat a ProblemSeatsSeat
// and every observed state a SandboxObservedState; and the expanded IR of
// both is held to its golden (D104).
func TestSeatsFixturesLoad(t *testing.T) {
	var warnings []string
	db, err := LoadService(filepath.FromSlash(seatsDBFixture), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	usesOf := func(td *ir.TypeDef) []string {
		var uses []string
		for _, ref := range td.TableBehaviors() {
			uses = append(uses, ref.UseID()+":"+ref.Name)
		}
		return uses
	}
	for name, want := range map[string][]string{
		"Problem": {"Seats:Seats", "StateRules:StateRules", "status:Workflow"},
		"Sandbox": {"status:Workflow", "observed:Reconcile"},
	} {
		td := db.Types[name]
		if td == nil || td.IsEngineType() || !td.Optimistic {
			t.Fatalf("%s = %s", name, marshal(t, td))
		}
		if got := usesOf(td); !slices.Equal(got, want) {
			t.Errorf("%s's uses = %v, want %v", name, got, want)
		}
	}
	configs := map[string]string{}
	for _, name := range []string{"Problem", "Sandbox"} {
		for _, ref := range db.Types[name].TableBehaviors() {
			configs[name+"."+ref.UseID()] = string(ref.Config)
		}
	}
	for use, held := range map[string][]string{
		"Problem.Seats": {`"accountable":{"assign":"problems.assign","max":1,"onFull":"displace","take":"problems.own"}`, `"handles":{"assign":"problems.release","max":5}`,
			`"responsible":{"assign":"problems.assign","fromField":"agent","take":"problems.work"}`},
		"Problem.StateRules": {`"review":{"by":["seat:accountable"],"frozen":["title"]}`},
		"Problem.status":     {`{"by":["seat:responsible"],"from":"open","to":"working"}`, `{"by":["seat:accountable"],"from":"review","to":"done"}`, `"overridePermission":"problems.override"`},
		"Sandbox.observed":   {`"observePermission":"sandboxes.observe"`, `"staleAfterMs":300000`, `"status":"status"`, `"unreachable":"unreachable"`},
	} {
		for _, text := range held {
			if !strings.Contains(configs[use], text) {
				t.Errorf("%s's config = %s, want it to hold %s", use, configs[use], text)
			}
		}
	}
	owned := func(name string) string {
		var fields []string
		for _, fd := range db.Types[name].Fields {
			if fd.Owned() {
				fields = append(fields, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
			}
		}
		return strings.Join(fields, ",")
	}
	for name, want := range map[string]string{
		"Problem": "status:string:required,statusEnteredAt:Temporal.DateTime:optional",
		"Sandbox": "status:string:required,statusEnteredAt:Temporal.DateTime:optional," +
			"observed:string:optional,observedObservedAt:Temporal.DateTime:optional,observedDrift:Generic.JSON:optional,observedReconciledAt:Temporal.DateTime:optional",
	} {
		if got := owned(name); got != want {
			t.Errorf("%s's owned fields =\n%s\nwant\n%s", name, got, want)
		}
	}
	if observed := db.Types["Sandbox"].Fields[slices.IndexFunc(db.Types["Sandbox"].Fields, func(fd *ir.FieldDef) bool { return fd.Name == "observed" })]; !observed.Behavior.Value {
		t.Errorf("Sandbox.observed = %s, want Reconcile's value field", marshal(t, observed))
	}
	sub := db.Types["ProblemSeatsOccupants"]
	if want := (&ir.BehaviorTable{Row: "Problem", Use: "Seats", Behavior: "Seats", Table: "occupants", Key: "number", RowField: "problem"}); sub == nil || sub.Origin != ir.OriginBehavior || sub.Role != ir.RoleDBTable || !reflect.DeepEqual(sub.BehaviorTable, want) {
		t.Errorf("ProblemSeatsOccupants = %s", marshal(t, sub))
	}
	var fields []string
	for _, fd := range sub.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := "problem:Problem,number:string,seat:string,subject:string,seatedAt:Temporal.DateTime,seatedBy:string"; strings.Join(fields, ",") != want {
		t.Errorf("ProblemSeatsOccupants' fields = %s, want %s", strings.Join(fields, ","), want)
	}
	for name, deadlines := range map[string]bool{"Problem": false, "Sandbox": true} {
		if queue := db.BehaviorQueueOf(name, ir.BehaviorQueueDeadlines); (queue != nil) != deadlines {
			t.Errorf("%s's deadline queue = %v, want one: %v", name, queue, deadlines)
		}
		if queue := db.BehaviorQueueOf(name, ir.BehaviorQueueChanges); queue != nil {
			t.Errorf("%s's change queue = %s, want none", name, queue.Name)
		}
	}
	if queue := db.BehaviorQueueOf("Sandbox", ir.BehaviorQueueDeadlines); queue == nil || queue.Name != "SandboxDeadline" || !slices.Equal(queue.BehaviorQueue.Behaviors, []string{"Reconcile"}) {
		t.Errorf("Sandbox's deadline queue = %s, want SandboxDeadline, Reconcile's", marshal(t, queue))
	}
	compareExpanded(t, db, "fixture-seats-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(seatsAPIFixture), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
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
		"ProblemActions.seatsTakeSeat problem/{id}/seats/take-seat problem_seats_take_seat Seats problems.assign,problems.own,problems.release,problems.work",
		"ProblemActions.seatsLeaveSeat problem/{id}/seats/leave-seat problem_seats_leave_seat Seats problems.assign,problems.own,problems.release,problems.work",
		"ProblemActions.seatsListSeats problem/{id}/seats/list-seats problem_seats_list_seats Seats problems.assign,problems.own,problems.release,problems.work",
		"ProblemActions.statusTransition problem/{id}/status/transition problem_status_transition status problems.override",
		"SandboxActions.statusTransition sandbox/{id}/status/transition sandbox_status_transition status sandboxes.stop",
		"SandboxActions.observedObserve sandbox/{id}/observed/observe sandbox_observed_observe observed sandboxes.observe",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	inputFields := func(name string) []string {
		input := api.Types[name]
		if input == nil {
			t.Fatalf("%s was not added", name)
		}
		var out []string
		for _, fd := range input.Fields {
			out = append(out, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
		}
		return out
	}
	for name, want := range map[string][]string{
		"ProblemSeatsTakeSeatInput":         {"seat:ProblemSeatsSeat:required", "subject:string:optional"},
		"ProblemSeatsLeaveSeatInput":        {"seat:ProblemSeatsSeat:required", "subject:string:optional"},
		"ProblemSeatsListSeatsInput":        {"seat:ProblemSeatsSeat:optional"},
		"ProblemStatusTransitionInput":      {"from:ProblemStatusState:optional", "to:ProblemStatusState:required"},
		"SandboxObservedObserveInput":       {"at:Generic.Int64:required", "state:SandboxObservedState:required"},
		"SandboxObservedObserveResult":      {"drift:SandboxObservedObserveResultDrift:optional", "moves:SandboxObservedObserveResultMoves:required", "observed:SandboxObservedState:required", "observedAt:Generic.Int64:required", "status:SandboxObservedState:optional", "unchanged:boolean:optional"},
		"SandboxObservedObserveResultDrift": {"code:string:required", "observed:SandboxObservedState:required", "status:SandboxObservedState:optional"},
	} {
		if got := inputFields(name); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string][]string{
		"ProblemSeatsSeat":     {"accountable", "handles", "responsible"},
		"SandboxObservedState": {"pending", "starting", "running", "stopping", "stopped", "failed", "unreachable"},
	} {
		if enum := api.Enums[name]; enum == nil || !slices.Equal(enumNames(enum), want) {
			t.Errorf("%s = %s, want %v", name, marshal(t, enum), want)
		}
	}
	for _, name := range []string{"ProblemPreconditions", "SandboxPreconditions"} {
		if api.Types[name] != nil {
			t.Errorf("%s was added: no use of its table takes a precondition", name)
		}
	}
	compareExpanded(t, api, "fixture-seats-api.expanded.json")
}

// TestSeatsOperationsListTakeAndAssign: an operation of a Seats use lists
// the permissions its seats' take and assign name, which no member's name
// says is a permission, beside those its config names so (D104), sorted
// and each once; a Reconcile use's observe lists its observePermission.
func TestSeatsOperationsListTakeAndAssign(t *testing.T) {
	api, err := LoadService(filepath.FromSlash(seatsAPIFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.BehaviorOperation == nil {
				continue
			}
			var want []string
			switch op.BehaviorOperation.Use {
			case "Seats":
				want = []string{"problems.assign", "problems.own", "problems.release", "problems.work"}
			case "observed":
				want = []string{"sandboxes.observe"}
			default:
				continue
			}
			if got := op.BehaviorOperation.Permissions; !slices.Equal(got, want) {
				t.Errorf("%s.%s lists %v, want %v", set.Name, op.Name, got, want)
			}
		}
	}
}

// TestSeatsFromTheDataForms: a JSON table's `{ name: "Seats", implements:
// true }` and a field's `{ name: "Reconcile", field: "observed" }` load as
// the TypeScript form's do, with Seats' occupants sub-table, Reconcile's
// owned fields, observed optional, and Sandbox's deadline queue (D104).
func TestSeatsFromTheDataForms(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(seatsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]any{}
	for _, name := range []string{"Problem", "Sandbox"} {
		td := db.Types[name]
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
		types[name] = map[string]any{"name": name, "role": "DBTable", "behaviors": behaviors, "fields": fields}
	}
	doc := map[string]any{
		"name": "seats-db", "kind": "DB",
		"scalars": map[string]any{"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"}},
		"types":   types,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(writeService(t, map[string]string{
		"schema.config.json":    `{"name": "seats-db", "kind": "DB", "outputs": {}}`,
		"src/seats.schema.json": string(data),
	}))
	if err != nil {
		t.Fatal(err)
	}
	for name, implemented := range map[string]int{"Problem": 2, "Sandbox": 0} {
		if got := schema.Types[name]; got == nil || !got.Optimistic || len(got.ImplementsBehaviors()) != implemented || got.IsEngineType() {
			t.Errorf("%s = %s", name, marshal(t, got))
		}
	}
	for _, name := range []string{"ProblemSeatsOccupants", "SandboxDeadline"} {
		if schema.Types[name] == nil {
			t.Errorf("types = %v, want %s", keysOf(schema.Types), name)
		}
	}
	for _, fd := range schema.Types["Sandbox"].Fields {
		if fd.Name == "observed" && (fd.Required || !fd.Owned() || !fd.Behavior.Value) {
			t.Errorf("Sandbox.observed = %s, want Reconcile's optional value field", marshal(t, fd))
		}
	}
}
