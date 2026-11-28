package cloudflare_test

import (
	"encoding/base64"
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
// their versions wait for the Workflows. A drainer binds no wake of its
// own.
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
	if b := bindings(timers)["WAKE_FINANCES_API_LINE_ITEM_TIMERS"]; b != nil {
		t.Errorf("the behavior worker binds a wake of its own: %v", b)
	}
}

// TestADrainerWakesAnotherThroughItsRelay (#600): drainers over one
// database wake each other without a cycle in the graph. Each drainer
// another drainer may write to has a wake relay, a Worker named as its own
// with -wake after it, workers.dev off, running the script the extension
// embeds, whose version binds the drainer's Workflow as WORKFLOW and waits
// for it. A drainer's QUEUE_WAKES gives each other drainer's queues the
// binding WAKE_<WORKER>, a service binding to that worker's relay, and its
// Worker node waits for each relay's Worker node; its version waits for no
// other drainer's Workflow. The server and the job keep their workflow
// bindings, and nothing but a drainer has a relay. The graph resolves,
// which refuses a cycle.
func TestADrainerWakesAnotherThroughItsRelay(t *testing.T) {
	env, errs := resolveOver(t, assemble(t), ledgerStack(), behaviorLedger(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	drainers := map[string]string{
		"finances-api-line-item-timers": "WAKE_FINANCES_API_LINE_ITEM_TIMERS",
		"finances-api-sync-plaid-item":  "WAKE_FINANCES_API_SYNC_PLAID_ITEM",
		"finances-api-embed-line-items": "WAKE_FINANCES_API_EMBED_LINE_ITEMS",
	}
	queues := map[string][]string{
		"finances-api-line-item-timers": {"LineItemDeadline", "LineItemChange", "LineItemEvent"},
		"finances-api-sync-plaid-item":  {"PlaidSyncRequested"},
		"finances-api-embed-line-items": {"EmbedLineItem"},
	}
	for drainer := range drainers {
		relay := drainer + ".wake-relay"
		worker := node(t, env, relay+".worker")
		if worker.Type != cloudflare.TypeWorker || worker.Properties["name"] != "ledger-stack-staging-"+drainer+"-wake" || worker.Phase != ir.PhaseInfrastructure || len(worker.DependsOn) != 0 {
			t.Errorf("%s's relay Worker is %+v, want ledger-stack-staging-%s-wake in infrastructure, waiting for nothing", drainer, worker, drainer)
		}
		if sub := worker.Properties["subdomain"].(map[string]any); sub["enabled"] != false || sub["previewsEnabled"] != false {
			t.Errorf("%s's relay turns workers.dev on: %v", drainer, sub)
		}
		version := node(t, env, relay+".version")
		if !slices.Equal(version.DependsOn, []string{relay + ".worker", drainer + ".workflow"}) {
			t.Errorf("%s's relay version waits for %v, want its Worker and its drainer's Workflow", drainer, version.DependsOn)
		}
		if version.Properties["mainModule"] != cloudflare.WakeRelayModule {
			t.Errorf("%s's relay runs %v, want %s", drainer, version.Properties["mainModule"], cloudflare.WakeRelayModule)
		}
		modules := version.Properties["modules"].([]any)
		if content, _ := modules[0].(map[string]any)["contentBase64"].(string); len(modules) != 1 || content != base64.StdEncoding.EncodeToString([]byte(cloudflare.WakeRelayScript())) {
			t.Errorf("%s's relay holds modules %v, want the embedded script alone", drainer, modules)
		}
		if b := versionBindings(t, version)[cloudflare.WakeRelayWorkflowBinding]; b["type"] != "workflow" || b["workflowName"] != "ledger-stack-staging-"+drainer {
			t.Errorf("%s's relay binds WORKFLOW as %v, want its Workflow", drainer, b)
		}
		node(t, env, relay+".deployment")

		bindings := versionBindings(t, node(t, env, drainer+".version"))
		var wakes map[string][]ir.QueueWake
		if err := json.Unmarshal([]byte(bindings[ir.QueueWakesVariable]["text"].(string)), &wakes); err != nil {
			t.Fatalf("%s's QUEUE_WAKES: %v", drainer, err)
		}
		var relays []string
		for other, name := range drainers {
			for _, queue := range queues[other] {
				got, woken := wakes[queue]
				if other == drainer {
					if woken {
						t.Errorf("%s wakes its own queue %s through %v", drainer, queue, got)
					}
					continue
				}
				if len(got) != 1 || got[0].Binding != name {
					t.Errorf("%s wakes %s through %+v, want the binding %s", drainer, queue, got, name)
				}
			}
			if other == drainer {
				continue
			}
			b := bindings[name]
			if out, _ := b["service"].(ir.Output); b["type"] != "service" || out != (ir.Output{Resource: other + ".wake-relay.worker", Name: "name"}) {
				t.Errorf("%s binds %s as %v, want a service binding to %s's relay", drainer, name, b, other)
			}
			relays = append(relays, other+".wake-relay.worker")
		}
		if deps := node(t, env, drainer+".worker").DependsOn; !containsAll(deps, relays) {
			t.Errorf("%s's Worker waits for %v, want the relays %v among them", drainer, deps, relays)
		}
		for _, dep := range node(t, env, drainer+".version").DependsOn {
			if strings.HasSuffix(dep, ".workflow") {
				t.Errorf("%s's version waits for the Workflow %s", drainer, dep)
			}
		}
	}
	for _, waker := range []string{"finances-api", "finances-api-sync-all-plaid-items"} {
		if b := versionBindings(t, node(t, env, waker+".version"))["WAKE_FINANCES_API_SYNC_PLAID_ITEM"]; b["type"] != "workflow" {
			t.Errorf("%s wakes the sync worker through %v, want its Workflow", waker, b)
		}
		if env.Resources.Resource(waker+".wake-relay.worker") != nil {
			t.Errorf("%s, no drainer, has a wake relay", waker)
		}
	}
}

// containsAll reports whether every one of want is in have.
func containsAll(have, want []string) bool {
	for _, w := range want {
		if !slices.Contains(have, w) {
			return false
		}
	}
	return true
}

// TestAWorkerWindowFitsItsCPUTime (#600): a @worker's window and grace fit
// in the CPU time a firing may take, as a behavior worker's do (D151): its
// window is 22 s by default, 30 s of CPU time less the 8 s grace, cpuMs
// raises it to its 50 s default (D92), and a drainSeconds past what the CPU
// time leaves does not resolve, naming both.
func TestAWorkerWindowFitsItsCPUTime(t *testing.T) {
	reg := assemble(t)
	with := func(settings map[string]any) (*ir.ResolvedEnvironment, *stack.Errors) {
		s := ledgerStack()
		s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: worker("SyncPlaidItem"), Values: settings})
		return resolveOver(t, reg, s, ledgerServices(), "Staging")
	}
	window := func(env *ir.ResolvedEnvironment) any {
		return versionBindings(t, node(t, env, "finances-api-sync-plaid-item.version"))[ir.WorkerDrainSecondsVariable]["text"]
	}
	env, errs := with(map[string]any{})
	if errs != nil {
		t.Fatal(errs)
	}
	if got := window(env); got != "22" {
		t.Errorf("by default the @worker's window is %v, want 22", got)
	}
	if env, errs = with(map[string]any{"cpuMs": 120000}); errs != nil {
		t.Fatal(errs)
	}
	if got := window(env); got != "50" {
		t.Errorf("with 120 s of CPU time the @worker's window is %v, want its default, 50", got)
	}
	_, errs = with(map[string]any{"drainSeconds": 25})
	if errs == nil || !strings.Contains(errs.Error(), "worker finances-api-sync-plaid-item claims for 25s and lets its handlers finish for 8s at each firing, and a firing on Workers may take 30000ms of CPU time") || !strings.Contains(errs.Error(), "shorten drainSeconds to 22 or raise cpuMs") {
		t.Errorf("a @worker's window past the CPU time resolved with %v", errs)
	}
	_, errs = with(map[string]any{"cpuMs": 8000})
	if errs == nil || !strings.Contains(errs.Error(), "worker finances-api-sync-plaid-item lets its handlers finish for 8s, and a firing on Workers may take 8000ms of CPU time (cpuMs), which leaves it no time to claim; raise cpuMs or shorten its @worker's grace") {
		t.Errorf("a @worker's CPU time within its grace resolved with %v", errs)
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
