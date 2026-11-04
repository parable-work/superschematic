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

// The fixtures of D104's second step's second batch: a database whose Job
// implements Lease (a fencing token every write presents, an expiry that
// moves it back to queued and escalates at the third) and Retries (a
// timeout's backoff and the move its end makes, a build failure once per
// revision, a time to give up after and a window of attempts), whose
// Session implements Presence (a device's beat, a longer ttl in a turn, a
// miss that expires the device's leases on Job), and whose Workspace and
// Account implement Budget (hourly and rolling daily meters, an account's
// drawn on its workspace's through a to-one relation, and a provider's
// observed usage); and an API that serves Job's, Session's and Account's
// behavior operations and runs Job's and Session's deadlines.
const (
	agentsDBFixture  = "tsreader/testdata/services/fixture-agents-db"
	agentsAPIFixture = "tsreader/testdata/services/fixture-agents-api"
)

// expireHolderLeftOut is the API's one warning: Lease's expireHolder runs
// on the table as a whole, which waits for a command without a row, so
// JobActions leaves it out (D104).
const expireHolderLeftOut = "JobActions: Job's Lease is a Lease, whose operation expireHolder runs on the schema as a whole, not on a row: @behaviorOperations serves the operations a behavior runs on a row and leaves expireHolder out (D103, D104)"

// TestAgentsFixturesLoad: both fixtures load; Job stays a table whose uses
// are Lease and Retries, which it implements, and its status, in that
// order, Session's Presence and its status, Account's Budget and its
// status, and Workspace's Budget; the configs read as the TypeScript form
// wrote them (a backoff object, then, window, keyField, ttlByState,
// releaseLeases a list, an everyMs-free reset and a rolling window); the
// loader adds Lease's and Retries' owned fields and sub-tables and Job's
// deadline queue, Presence's four owned fields and Session's deadline
// queue, Budget's three sub-tables on each of Account and Workspace and no
// queue for either, and no change queue anywhere; the API's sets serve
// each use's operations on a row, Lease's but expireHolder, every Job
// operation's input presenting JobPreconditions, recordAttempt's failure a
// JobRetriesClass and every Budget meter an AccountBudgetMeter; its one
// warning is that expireHolder is left out; and the expanded IR of both is
// held to its golden (D104).
func TestAgentsFixturesLoad(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(agentsDBFixture))
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
		"Job":       {"Lease:Lease", "Retries:Retries", "status:Workflow"},
		"Session":   {"Presence:Presence", "status:Workflow"},
		"Account":   {"Budget:Budget", "status:Workflow"},
		"Workspace": {"Budget:Budget"},
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
	for _, name := range []string{"Job", "Session", "Account", "Workspace"} {
		for _, ref := range db.Types[name].ImplementsBehaviors() {
			configs[name+"."+ref.Name] = string(ref.Config)
		}
	}
	for use, held := range map[string][]string{
		"Job.Lease": {`"onExpiry":{"from":["running"],"transition":"queued"}`, `"maxExpiries":3`, `"requireToken":true`, `"overridePermission":"jobs.override"`},
		"Job.Retries": {`"timeout":{"attempts":5,"backoff":{"firstMs":60000,"maxMs":600000}}`, `"then":{"from":["waiting"],"transition":"queued"}`,
			`"window":{"attempts":4,"ms":3600000}`, `"keyField":"revision"`, `"retryInto":["running"]`, `"giveUpAfterMs":1800000`},
		"Session.Presence": {`"ttlByState":{"turn":900000}`, `"releaseLeases":["Job"]`, `"principalField":"device"`},
		"Account.Budget":   {`"questions":{"limit":40,"reset":"hourly","scope":"workspace"}`, `"window":"rolling"`, `"observePermission":"accounts.observe"`},
		"Workspace.Budget": {`"tokens":{"limit":2000000,"reset":"daily","window":"rolling"}`},
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
				fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
			}
		}
		return strings.Join(fields, ",")
	}
	for name, want := range map[string]string{
		"Job": "leaseHolder:string,leaseToken:Generic.Int64,leaseAcquiredAt:Temporal.DateTime,leaseRenewedAt:Temporal.DateTime,leaseExpiresAt:Temporal.DateTime,leaseExpiries:Generic.Int64,leaseEnded:Generic.JSON," +
			"retriesTotal:Generic.Int64,retriesClassAttempts:Generic.JSON,retriesBestScore:number,retriesExhausted:boolean,retriesStuck:boolean,retriesRetryAt:Temporal.DateTime,retriesFailingSince:Temporal.DateTime,retriesKeyedBy:string," +
			"status:string,statusEnteredAt:Temporal.DateTime",
		"Session":   "presenceDeadline:Temporal.DateTime,presenceLastBeatAt:Temporal.DateTime,presenceMissed:boolean,presenceReleased:Generic.JSON,status:string,statusEnteredAt:Temporal.DateTime",
		"Account":   "status:string,statusEnteredAt:Temporal.DateTime",
		"Workspace": "",
	} {
		if got := owned(name); got != want {
			t.Errorf("%s's owned fields =\n%s\nwant\n%s", name, got, want)
		}
	}
	for name, want := range map[string]*ir.BehaviorTable{
		"JobLeaseDirectives":     {Row: "Job", Use: "Lease", Behavior: "Lease", Table: "directives", Key: "number", RowField: "job"},
		"JobRetriesAttempts":     {Row: "Job", Use: "Retries", Behavior: "Retries", Table: "attempts", Key: "number", RowField: "job"},
		"AccountBudgetMeters":    {Row: "Account", Use: "Budget", Behavior: "Budget", Table: "meters", Key: "meter", RowField: "account"},
		"AccountBudgetHolds":     {Row: "Account", Use: "Budget", Behavior: "Budget", Table: "holds", Key: "hold", RowField: "account"},
		"AccountBudgetBuckets":   {Row: "Account", Use: "Budget", Behavior: "Budget", Table: "buckets", Key: "bucket", RowField: "account"},
		"WorkspaceBudgetMeters":  {Row: "Workspace", Use: "Budget", Behavior: "Budget", Table: "meters", Key: "meter", RowField: "workspace"},
		"WorkspaceBudgetHolds":   {Row: "Workspace", Use: "Budget", Behavior: "Budget", Table: "holds", Key: "hold", RowField: "workspace"},
		"WorkspaceBudgetBuckets": {Row: "Workspace", Use: "Budget", Behavior: "Budget", Table: "buckets", Key: "bucket", RowField: "workspace"},
	} {
		sub := db.Types[name]
		if sub == nil || sub.Origin != ir.OriginBehavior || sub.Role != ir.RoleDBTable || !reflect.DeepEqual(sub.BehaviorTable, want) {
			t.Errorf("%s = %s", name, marshal(t, sub))
		}
	}
	var fields []string
	for _, fd := range db.Types["JobRetriesAttempts"].Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if want := "job:Job,number:string,at:Temporal.DateTime,failure:string,score:number,kept:boolean,signature:string,predicates:Generic.JSON,detail:Generic.JSON,subject:string,keyedBy:string"; strings.Join(fields, ",") != want {
		t.Errorf("JobRetriesAttempts' fields = %s, want %s", strings.Join(fields, ","), want)
	}
	for name, deadlines := range map[string]bool{"Job": true, "Session": true, "Account": false, "Workspace": false} {
		if queue := db.BehaviorQueueOf(name, ir.BehaviorQueueDeadlines); (queue != nil) != deadlines {
			t.Errorf("%s's deadline queue = %v, want one: %v", name, queue, deadlines)
		}
		if queue := db.BehaviorQueueOf(name, ir.BehaviorQueueChanges); queue != nil {
			t.Errorf("%s's change queue = %s, want none", name, queue.Name)
		}
	}
	if queue := db.BehaviorQueueOf("Job", ir.BehaviorQueueDeadlines); queue == nil || !slices.Equal(queue.BehaviorQueue.Behaviors, []string{"Lease", "Retries"}) {
		t.Errorf("Job's deadline queue = %s, want Lease's and Retries'", marshal(t, queue))
	}
	compareExpanded(t, db, "fixture-agents-db.expanded.json")

	var warnings []string
	api, err := LoadService(filepath.FromSlash(agentsAPIFixture), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{expireHolderLeftOut}; !slices.Equal(warnings, want) {
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
		"AccountActions.budgetReserve account/{id}/budget/reserve account_budget_reserve Budget accounts.limits,accounts.observe",
		"AccountActions.budgetCheckReserve account/{id}/budget/check-reserve account_budget_check_reserve Budget accounts.limits,accounts.observe",
		"AccountActions.budgetRecordUsage account/{id}/budget/record-usage account_budget_record_usage Budget accounts.limits,accounts.observe",
		"AccountActions.budgetSettle account/{id}/budget/settle account_budget_settle Budget accounts.limits,accounts.observe",
		"AccountActions.budgetSetLimit account/{id}/budget/set-limit account_budget_set_limit Budget accounts.limits,accounts.observe",
		"AccountActions.budgetReserveFor account/{id}/budget/reserve-for account_budget_reserve_for Budget accounts.limits,accounts.observe",
		"AccountActions.budgetSettleFor account/{id}/budget/settle-for account_budget_settle_for Budget accounts.limits,accounts.observe",
		"AccountActions.budgetRecordUsageFor account/{id}/budget/record-usage-for account_budget_record_usage_for Budget accounts.limits,accounts.observe",
		"AccountActions.budgetObserve account/{id}/budget/observe account_budget_observe Budget accounts.limits,accounts.observe",
		"AccountActions.statusTransition account/{id}/status/transition account_status_transition status ",
		"JobActions.leaseAcquire job/{id}/lease/acquire job_lease_acquire Lease jobs.override",
		"JobActions.leaseHeartbeat job/{id}/lease/heartbeat job_lease_heartbeat Lease jobs.override",
		"JobActions.leaseRelease job/{id}/lease/release job_lease_release Lease jobs.override",
		"JobActions.leaseExpire job/{id}/lease/expire job_lease_expire Lease jobs.override",
		"JobActions.leaseDirect job/{id}/lease/direct job_lease_direct Lease jobs.override",
		"JobActions.leaseAcknowledge job/{id}/lease/acknowledge job_lease_acknowledge Lease jobs.override",
		"JobActions.leaseResetExpiries job/{id}/lease/reset-expiries job_lease_reset_expiries Lease jobs.override",
		"JobActions.retriesRecordAttempt job/{id}/retries/record-attempt job_retries_record_attempt Retries ",
		"JobActions.statusTransition job/{id}/status/transition job_status_transition status ",
		"SessionActions.presenceBeat session/{id}/presence/beat session_presence_beat Presence ",
		"SessionActions.presenceMiss session/{id}/presence/miss session_presence_miss Presence ",
		"SessionActions.statusTransition session/{id}/status/transition session_status_transition status ",
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
		"JobPreconditions":             {"Lease:JobLeasePrecondition:optional"},
		"JobLeasePrecondition":         {"token:Generic.Int64:required"},
		"JobLeaseResetExpiriesInput":   {"preconditions:JobPreconditions:optional"},
		"JobLeaseHeartbeatInput":       {"acknowledge:Generic.Int64:optional", "preconditions:JobPreconditions:optional"},
		"JobStatusTransitionInput":     {"from:JobStatusState:optional", "preconditions:JobPreconditions:optional", "to:JobStatusState:required"},
		"JobRetriesRecordAttemptInput": {"detail:Generic.JSON:optional", "failure:JobRetriesClass:optional", "preconditions:JobPreconditions:optional", "predicates:boolean:optional", "result:Generic.JSON:optional", "score:number:optional", "signature:string:optional"},
		"AccountBudgetObserveInput":    {"limit:Generic.Int64:optional", "meter:AccountBudgetMeter:required", "resetsAt:Generic.Int64:optional", "used:Generic.Int64:required"},
		"SessionStatusTransitionInput": {"from:SessionStatusState:optional", "to:SessionStatusState:required"},
	} {
		if got := inputFields(name); !slices.Equal(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string][]string{
		"JobRetriesClass":    {"build", "rejected", "timeout"},
		"AccountBudgetMeter": {"questions", "tokens"},
	} {
		if enum := api.Enums[name]; enum == nil || !slices.Equal(enumNames(enum), want) {
			t.Errorf("%s = %s, want %v", name, marshal(t, enum), want)
		}
	}
	for _, name := range []string{"SessionPreconditions", "AccountPreconditions", "SessionPresenceBeatInput"} {
		if api.Types[name] != nil {
			t.Errorf("%s was added: no use of its table takes a precondition, or an input", name)
		}
	}
	compareExpanded(t, api, "fixture-agents-api.expanded.json")
}

// TestAgentsFromTheDataForms: a JSON table's `{ name: "Lease", implements:
// true }` and Retries', and Presence's and Budget's, load as the TypeScript
// form's do, with their sub-tables and deadline queues (D104).
func TestAgentsFromTheDataForms(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(agentsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]any{}
	for _, name := range []string{"Job", "Session", "Workspace", "Account"} {
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
			if fd.Relation != nil {
				f["relation"] = fd.Relation
			}
			fields = append(fields, f)
		}
		types[name] = map[string]any{"name": name, "role": "DBTable", "behaviors": behaviors, "fields": fields}
	}
	doc := map[string]any{
		"name": "agents-db", "kind": "DB",
		"scalars": map[string]any{
			"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
			"Generic.Int64": map[string]any{"name": "Generic.Int64", "languagePrimitive": "number"},
		},
		"types": types,
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	schema, err := LoadService(writeService(t, map[string]string{
		"schema.config.json":     `{"name": "agents-db", "kind": "DB", "outputs": {}}`,
		"src/agents.schema.json": string(data),
	}))
	if err != nil {
		t.Fatal(err)
	}
	for name, implemented := range map[string]int{"Job": 2, "Session": 1, "Account": 1, "Workspace": 1} {
		if got := schema.Types[name]; got == nil || !got.Optimistic || len(got.ImplementsBehaviors()) != implemented || got.IsEngineType() {
			t.Errorf("%s = %s", name, marshal(t, got))
		}
	}
	for _, name := range []string{"JobLeaseDirectives", "JobRetriesAttempts", "JobDeadline", "SessionDeadline", "AccountBudgetMeters", "WorkspaceBudgetBuckets"} {
		if schema.Types[name] == nil {
			t.Errorf("types = %v, want %s", keysOf(schema.Types), name)
		}
	}
}
