package cloudflare_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
)

// behaviorLedger is the ledger whose finances-db keeps LineItem's deadline,
// change and event queues (D103, D104), which finances-api's behavior
// worker LineItemTimers runs.
func behaviorLedger() []stack.Service {
	services := ledgerServices()
	for i := range services {
		switch services[i].Name {
		case "finances-db":
			services[i].Queues = append(services[i].Queues, "LineItemChange", "LineItemDeadline", "LineItemEvent")
			services[i].BehaviorQueues = map[string]ir.BehaviorQueue{
				"LineItemDeadline": {Row: "LineItem", Kind: ir.BehaviorQueueDeadlines},
				"LineItemChange":   {Row: "LineItem", Kind: ir.BehaviorQueueChanges},
				"LineItemEvent":    {Row: "LineItem", Kind: ir.BehaviorQueueEvents},
			}
		case "finances-api":
			services[i].Workers = append(services[i].Workers, ir.Worker{
				Name: "LineItemTimers", Queue: "LineItemDeadline", Queues: []string{"LineItemDeadline", "LineItemChange", "LineItemEvent"},
				BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "LineItem"}, Origin: ir.OriginBehavior,
			})
		}
	}
	return services
}

// TestBehaviorWorkerOnWorkers (D151): a behavior worker resolves on
// cloudflare.drainer, a Worker under a Cron Trigger every minute whose
// window and grace fit in a firing's default CPU time, 30 s, so its window
// is 22 s; and, as every drainer, a Workflow of the class its drain.ts
// exports, QueueWake, which waits for its deployment. The server and the
// job, which may write to its queues, each bind the Workflow of every
// drainer and hold QUEUE_WAKES, which gives each queue, the behavior
// worker's three among them, the binding of the worker that drains it;
// their versions wait for the Workflows. A drainer binds none.
func TestBehaviorWorkerOnWorkers(t *testing.T) {
	env, errs := resolveOver(t, assemble(t), ledgerStack(), behaviorLedger(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	const timers = "finances-api-line-item-timers"
	bindings := func(deployable string) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, b := range node(t, env, deployable+".version").Properties["bindings"].([]any) {
			b := b.(map[string]any)
			out[b["name"].(string)] = b
		}
		return out
	}
	if got := bindings(timers)[ir.WorkerDrainSecondsVariable]["text"]; got != "22" {
		t.Errorf("the behavior worker's window is %v, want 22: the CPU time's 30 s less the 8 s grace", got)
	}
	if got := node(t, env, timers+".schedule").Properties["schedules"]; !strings.Contains(mustJSON(t, got), cloudflare.DrainSchedule) {
		t.Errorf("the behavior worker's Cron Trigger fires %v, want every minute", got)
	}
	workflow := node(t, env, timers+".workflow")
	if workflow.Type != cloudflare.TypeWorkflow || workflow.Properties["className"] != ir.QueueWakeClass || !slices.Contains(workflow.DependsOn, timers+".deployment") {
		t.Errorf("the behavior worker's Workflow is %+v, want one of QueueWake after its deployment", workflow)
	}
	for _, waker := range []string{"finances-api", "finances-api-sync-all-plaid-items"} {
		b := bindings(waker)
		var wakes map[string][]ir.QueueWake
		if err := json.Unmarshal([]byte(b[ir.QueueWakesVariable]["text"].(string)), &wakes); err != nil {
			t.Fatalf("%s's QUEUE_WAKES: %v", waker, err)
		}
		for queue, binding := range map[string]string{
			"LineItemDeadline":   "WAKE_FINANCES_API_LINE_ITEM_TIMERS",
			"LineItemChange":     "WAKE_FINANCES_API_LINE_ITEM_TIMERS",
			"LineItemEvent":      "WAKE_FINANCES_API_LINE_ITEM_TIMERS",
			"PlaidSyncRequested": "WAKE_FINANCES_API_SYNC_PLAID_ITEM",
			"EmbedLineItem":      "WAKE_FINANCES_API_EMBED_LINE_ITEMS",
		} {
			if got := wakes[queue]; len(got) != 1 || got[0].Binding != binding || got[0].URL != "" {
				t.Errorf("%s wakes %s through %+v, want the binding %s", waker, queue, got, binding)
			}
		}
		wf := b["WAKE_FINANCES_API_LINE_ITEM_TIMERS"]
		if wf["type"] != "workflow" || wf["workflowName"] != "ledger-stack-staging-"+timers {
			t.Errorf("%s binds the behavior worker's Workflow as %v", waker, wf)
		}
		if deps := node(t, env, waker+".version").DependsOn; !slices.Contains(deps, timers+".workflow") {
			t.Errorf("%s's version waits for %v, want the behavior worker's Workflow among them", waker, deps)
		}
	}
	if _, ok := bindings(timers)[ir.QueueWakesVariable]; ok {
		t.Errorf("the behavior worker binds QUEUE_WAKES; a drainer wakes no other drainer")
	}
}

// TestBehaviorWorkerWindowFitsItsCPUTime (D151): a behavior worker's
// window and grace fit in the CPU time a firing may take: cpuMs raises the
// window up to its 50 s default, and a drainSeconds past what the CPU time
// leaves does not resolve, naming both.
func TestBehaviorWorkerWindowFitsItsCPUTime(t *testing.T) {
	reg := assemble(t)
	with := func(settings map[string]any) (*ir.ResolvedEnvironment, *stack.Errors) {
		s := ledgerStack()
		s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: worker("LineItemTimers"), Values: settings})
		return resolveOver(t, reg, s, behaviorLedger(), "Staging")
	}
	env, errs := with(map[string]any{"cpuMs": 120000})
	if errs != nil {
		t.Fatal(errs)
	}
	for _, b := range node(t, env, "finances-api-line-item-timers.version").Properties["bindings"].([]any) {
		if b := b.(map[string]any); b["name"] == ir.WorkerDrainSecondsVariable && b["text"] != "50" {
			t.Errorf("with 120 s of CPU time the window is %v, want its default, 50", b["text"])
		}
	}
	_, errs = with(map[string]any{"drainSeconds": 25})
	if errs == nil || !strings.Contains(errs.Error(), "behavior worker finances-api-line-item-timers claims for 25s and lets its handlers finish for 8s at each firing") || !strings.Contains(errs.Error(), "shorten drainSeconds to 22 or raise cpuMs") {
		t.Errorf("a window past the CPU time resolved with %v", errs)
	}
}
