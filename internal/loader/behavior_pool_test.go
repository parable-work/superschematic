package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of the rest of D104's third step: a database whose Account
// implements Pool and Budget on one table (a person's accounts at two
// vendors and paid keys, ranked by rank, out for thirty minutes when a
// provider names no time and rechecked every two minutes, its tokens
// metered by the hour), and an API that serves the table's behavior
// operations, pick among them as a route of the table, and runs its
// deadlines and its events in a @behaviorWorker, which hands each recheck
// to the project's Workers method.
const (
	poolDBFixture  = "tsreader/testdata/services/fixture-pool-db"
	poolAPIFixture = "tsreader/testdata/services/fixture-pool-api"
)

// TestPoolFixturesLoad: both fixtures load with no warning and no error;
// Account stays a table whose uses are Pool and Budget, which it
// implements, their configs read as the TypeScript form wrote them; the
// loader adds Pool's four owned fields, Budget's three sub-tables, and
// Account's deadline queue and event queue, Pool's, and no change queue;
// the API's set serves Pool's markOut and reportRoom on a row and pick on
// the table as a whole, at account/pool/pick with no key, each listing
// accounts.pool, then Budget's nine; AccountRechecks' queues are the
// deadline and event queues, and its events the one recheck, which it
// hands to AccountRechecksPoolRecheck; the API holds the types of pick's
// input and result, match and prefer one optional member per own field the
// core compares, typed by the JSON value it holds, of the event and its
// payload, and the availability enum; and the expanded IR of both is held
// to its golden (D104).
func TestPoolFixturesLoad(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(poolDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	account := db.Types["Account"]
	if account == nil || account.IsEngineType() || !account.Optimistic {
		t.Fatalf("Account = %s", marshal(t, account))
	}
	var uses []string
	for _, ref := range account.TableBehaviors() {
		uses = append(uses, ref.UseID()+":"+ref.Name)
	}
	if want := []string{"Pool:Pool", "Budget:Budget"}; !slices.Equal(uses, want) {
		t.Errorf("Account's uses = %v, want %v", uses, want)
	}
	configs := map[string]string{}
	for _, ref := range account.ImplementsBehaviors() {
		configs[ref.Name] = string(ref.Config)
	}
	for use, held := range map[string][]string{
		"Pool":   {`"order":"rank"`, `"outForMs":1800000`, `"recheckAfterMs":120000`, `"permission":"accounts.pool"`},
		"Budget": {`"meters":{"tokens":{"limit":1000,"reset":"hourly"}}`, `"observePermission":"accounts.observe"`},
	} {
		for _, text := range held {
			if !strings.Contains(configs[use], text) {
				t.Errorf("%s's config = %s, want it to hold %s", use, configs[use], text)
			}
		}
	}
	var owned []string
	for _, fd := range account.Fields {
		if fd.Owned() {
			owned = append(owned, fd.Name+":"+fd.TypeRef.Name)
		}
	}
	if want := []string{"poolAvailability:string", "poolOutUntil:Temporal.DateTime", "poolOutReason:string", "poolCheckedAt:Temporal.DateTime"}; !slices.Equal(owned, want) {
		t.Errorf("Account's owned fields = %v, want %v", owned, want)
	}
	for _, name := range []string{"AccountBudgetMeters", "AccountBudgetHolds", "AccountBudgetBuckets"} {
		if sub := db.Types[name]; sub == nil || sub.Origin != ir.OriginBehavior || sub.BehaviorTable == nil || sub.BehaviorTable.Use != "Budget" {
			t.Errorf("%s = %s", name, marshal(t, sub))
		}
	}
	for kind, name := range map[string]string{ir.BehaviorQueueDeadlines: "AccountDeadline", ir.BehaviorQueueEvents: "AccountEvent"} {
		if queue := db.BehaviorQueueOf("Account", kind); queue == nil || queue.Name != name || !slices.Equal(queue.BehaviorQueue.Behaviors, []string{"Pool"}) {
			t.Errorf("Account's %s queue = %s, want %s, Pool's", kind, marshal(t, queue), name)
		}
	}
	if queue := db.BehaviorQueueOf("Account", ir.BehaviorQueueChanges); queue != nil {
		t.Errorf("Account's change queue = %s, want none: neither Pool nor Budget hears a write", queue.Name)
	}
	compareExpanded(t, db, "fixture-pool-db.expanded.json")

	api, err := LoadService(filepath.FromSlash(poolAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixtures' warnings =\n%q\nwant none", warnings)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.BehaviorOperation; marker != nil {
				var args []string
				for _, arg := range op.Arguments {
					args = append(args, arg.Name)
				}
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" ("+strings.Join(args, ",")+") "+op.MCP.Handle+" "+marker.Use+" "+strings.Join(marker.Permissions, ",")+map[bool]string{true: " table", false: ""}[marker.Table])
			}
		}
	}
	if want := []string{
		"AccountActions.poolMarkOut account/{id}/pool/mark-out (id,input) account_pool_mark_out Pool accounts.pool",
		"AccountActions.poolReportRoom account/{id}/pool/report-room (id,input) account_pool_report_room Pool accounts.pool",
		"AccountActions.poolPick account/pool/pick (input) account_pool_pick Pool accounts.pool table",
		"AccountActions.budgetReserve account/{id}/budget/reserve (id,input) account_budget_reserve Budget accounts.observe",
		"AccountActions.budgetCheckReserve account/{id}/budget/check-reserve (id,input) account_budget_check_reserve Budget accounts.observe",
		"AccountActions.budgetRecordUsage account/{id}/budget/record-usage (id,input) account_budget_record_usage Budget accounts.observe",
		"AccountActions.budgetSettle account/{id}/budget/settle (id,input) account_budget_settle Budget accounts.observe",
		"AccountActions.budgetSetLimit account/{id}/budget/set-limit (id,input) account_budget_set_limit Budget accounts.observe",
		"AccountActions.budgetReserveFor account/{id}/budget/reserve-for (id,input) account_budget_reserve_for Budget accounts.observe",
		"AccountActions.budgetSettleFor account/{id}/budget/settle-for (id,input) account_budget_settle_for Budget accounts.observe",
		"AccountActions.budgetRecordUsageFor account/{id}/budget/record-usage-for (id,input) account_budget_record_usage_for Budget accounts.observe",
		"AccountActions.budgetObserve account/{id}/budget/observe (id,input) account_budget_observe Budget accounts.observe",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	var worker *ir.Worker
	for _, w := range api.Workers {
		if w.Name == "AccountRechecks" {
			worker = w
		}
	}
	if worker == nil || worker.Origin != ir.OriginBehavior || worker.Queue != "AccountDeadline" || !slices.Equal(worker.Queues, []string{"AccountDeadline", "AccountEvent"}) ||
		!slices.Equal(worker.BehaviorWorker.Permissions, []string{"accounts.pool"}) {
		t.Fatalf("AccountRechecks = %s", marshal(t, worker))
	}
	if len(worker.Events) != 1 {
		t.Fatalf("AccountRechecks' events = %s, want recheck alone", marshal(t, worker.Events))
	}
	recheck := worker.Events[0]
	recheck.Description = ""
	if want := (ir.BehaviorWorkerEvent{Use: "Pool", Member: "pool", Behavior: "Pool", Kind: "recheck", Method: "AccountRechecksPoolRecheck", Event: "AccountPoolRecheckEvent", Payload: "AccountPoolRecheckPayload"}); recheck != want {
		t.Errorf("AccountRechecks' recheck = %+v, want %+v", recheck, want)
	}
	typeFields := func(name string) []string {
		td := api.Types[name]
		if td == nil {
			t.Fatalf("%s was not added", name)
		}
		// pick's match and prefer are a part of its input.
		input := strings.HasSuffix(name, "Input") || name == "AccountPoolMatch"
		if td.Origin != ir.OriginBehavior || td.Owner != api.Name || input != (td.Role == ir.RoleAPIInput) {
			t.Errorf("%s = %s, want the behavior's of %s, an input or an embedded struct", name, marshal(t, td), api.Name)
		}
		var out []string
		for _, fd := range td.Fields {
			out = append(out, fd.Name+":"+typeName(fd.TypeRef)+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
		}
		return out
	}
	for name, want := range map[string][]string{
		"AccountPoolRecheckEvent":    {"key:Identity.UUID:required", "at:Generic.Int64:required", "payload:AccountPoolRecheckPayload:required"},
		"AccountPoolRecheckPayload":  {"checkedAt:Generic.Int64:optional", "dueAt:Generic.Int64:required", "firedAt:Generic.Int64:required", "outReason:string:optional", "outUntil:Generic.Int64:required"},
		"AccountPoolMarkOutInput":    {"reason:string:required", "until:Generic.Int64:optional"},
		"AccountPoolMarkOutResult":   {"availability:AccountPoolAvailability:required", "outReason:string:required", "outUntil:Generic.Int64:required", "unchanged:boolean:optional"},
		"AccountPoolReportRoomInput": {"near:boolean:optional", "reason:string:optional", "resetsAt:Generic.Int64:optional", "room:boolean:required"},
		"AccountPoolReportRoomResult": {"availability:AccountPoolAvailability:required", "checkedAt:Generic.Int64:required", "opened:boolean:optional", "outReason:string:optional",
			"outUntil:Generic.Int64:optional"},
		"AccountPoolPickInput":  {"exclude:[string]:optional", "match:AccountPoolMatch:optional", "prefer:AccountPoolMatch:optional"},
		"AccountPoolPickResult": {"member:AccountPoolPickMember:optional", "until:Generic.Int64:optional"},
		"AccountPoolPickMember": {"availability:AccountPoolAvailability:required", "key:string:required", "order:Generic.Int64:optional", "preferred:boolean:required"},
		// Each member is typed by the JSON value the field holds: an enum's
		// and a UUID's a string, its wire form.
		"AccountPoolMatch": {"kind:string:optional", "name:string:optional", "owner:string:optional", "rank:Generic.Int64:optional", "vendor:string:optional"},
	} {
		if got := typeFields(name); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	if enum := api.Enums["AccountPoolAvailability"]; enum == nil || !slices.Equal(enumNames(enum), []string{"open", "near", "out"}) {
		t.Errorf("AccountPoolAvailability = %s, want open, near and out", marshal(t, enum))
	}
	compareExpanded(t, api, "fixture-pool-api.expanded.json")
}

// typeName is a field's type as TestPoolFixturesLoad lists it: a list's
// element in brackets.
func typeName(ref ir.TypeRef) string {
	if ref.IsArray {
		return "[" + ref.Name + "]"
	}
	return ref.Name
}

// TestPoolWorkerWarning: the API of fixture-pool-api's AccountActions
// alone, which declares no @behaviorWorker(Account), loads with the warning
// that Account's deadlines and events wait for a worker, naming Pool's
// event recheck (D104); nothing refuses it.
func TestPoolWorkerWarning(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(poolDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "fixture-pool-api", "kind": "API", "dependencies": [{"name": "fixture-pool-db", "kind": "DB"}], "outputs": {}}`,
		"src/accounts.schema.json": `{"imports": [{"package": "@schemas/fixture-pool-db", "types": ["Account"]}],
		  "operationSets": [{"name": "AccountActions", "operations": [], "behaviorOperations": {"type": "Account"}, "auth": true}]}`,
	})
	var warnings []string
	if _, err := LoadService(dir, WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, os.ErrNotExist
		}
		return db, nil
	}), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })); err != nil {
		t.Fatal(err)
	}
	want := "AccountActions: Account's behaviors keep deadlines (Pool) and events (Pool: recheck) for a worker to run, and fixture-pool-api declares no @behaviorWorker(Account): nothing runs them until a worker does; declare one on this API or another over fixture-pool-db (D103, D104)"
	if len(warnings) != 1 || warnings[0] != want {
		t.Errorf("warnings = %q, want %q", warnings, want)
	}
}

// TestPoolFromTheDataForms: a JSON table's `{ name: "Pool", implements:
// true, config }` beside Budget's loads as the TypeScript form's does, with
// Pool's owned fields and Account's deadline and event queues (D104);
// internal/writer's TestPoolRoundTrip writes it in every format.
func TestPoolFromTheDataForms(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(poolDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	td := db.Types["Account"]
	behaviors := make([]any, 0, len(td.Behaviors))
	for _, ref := range td.Behaviors {
		behaviors = append(behaviors, map[string]any{"name": ref.Name, "implements": true, "config": json.RawMessage(ref.Config)})
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
		"name": "pool-db", "kind": "DB",
		"scalars": map[string]any{
			"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
			"Generic.Int64": map[string]any{"name": "Generic.Int64", "languagePrimitive": "number"},
		},
		"enums": map[string]any{"AccountKind": map[string]any{"name": "AccountKind", "values": []any{
			map[string]any{"name": "Account", "serializedAs": "account"}, map[string]any{"name": "Key", "serializedAs": "key"},
		}}},
		"types": map[string]any{"Account": map[string]any{"name": "Account", "role": "DBTable", "behaviors": behaviors, "fields": fields}},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(writeService(t, map[string]string{
		"schema.config.json":       `{"name": "pool-db", "kind": "DB", "outputs": {}}`,
		"src/accounts.schema.json": string(data),
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := schema.Types["Account"]
	if got == nil || !got.Optimistic || len(got.ImplementsBehaviors()) != 2 || got.IsEngineType() {
		t.Fatalf("Account = %s", marshal(t, got))
	}
	for i, ref := range got.ImplementsBehaviors() {
		if want := td.Behaviors[i]; ref.Name != want.Name || string(ref.Config) != string(want.Config) {
			t.Errorf("Account's use %d = %s %s, want %s %s", i, ref.Name, ref.Config, want.Name, want.Config)
		}
	}
	var owned []string
	for _, fd := range got.Fields {
		if fd.Owned() {
			owned = append(owned, fd.Name)
		}
	}
	if want := []string{"poolAvailability", "poolOutUntil", "poolOutReason", "poolCheckedAt"}; !slices.Equal(owned, want) {
		t.Errorf("Account's owned fields = %v, want %v", owned, want)
	}
	for _, name := range []string{"AccountDeadline", "AccountEvent", "AccountBudgetMeters", "AccountBudgetHolds", "AccountBudgetBuckets"} {
		if schema.Types[name] == nil {
			t.Errorf("types = %v, want %s", keysOf(schema.Types), name)
		}
	}
	if schema.Types["AccountChange"] != nil {
		t.Errorf("pool-db holds AccountChange: neither Pool nor Budget hears a write")
	}
}
