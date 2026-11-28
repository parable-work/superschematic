package cloudflare

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The drainer platform (D92): a TypeScript queue worker is a Cloudflare
// Worker of its own, named as a server's is (workerName), which runs the
// bundle its package's bundle.ts writes from the generated drain.ts, under
// a Cron Trigger that fires it every minute. A Worker runs per firing, not
// until it is stopped, so each firing runs the worker's claim loop for a
// window, WORKER_DRAIN_SECONDS, then stops it as SIGTERM stops a process:
// it claims no more, lets the running handlers finish within the grace and
// gives back the rest. Its window and grace together fit within the CPU
// time a firing may take (DefaultCPUMilliseconds, or its cpuMs), so a
// firing ends before Cloudflare stops it, whatever its handlers do: the
// window is ir.DefaultWorkerDrainSeconds, or less when the CPU time leaves
// less, and a drainSeconds that does not fit is refused (D151, #600). The
// queue stays its table, so its keys, serialization, backoff,
// retention and dead messages are the table's
// (D53, D68, D80) whoever claims it, an enqueue still commits with the
// write that caused it, and @enqueueOnChange's triggers still feed it. A
// message waits at most the gap between one firing's window and the next
// minute, and the claim loop's idle wait, a second, within a window.
//
// Its Worker is the three nodes a server's is (workerNodes), workers.dev
// off, since a worker serves no request, and its version holds the
// bundle, the API's config as bindings (bundleVersion), and two plain_text
// bindings the runtime reads: WORKER_CONCURRENCY, how many messages a
// firing handles at a time, and WORKER_DRAIN_SECONDS. Its database is on
// Hyperdrive (hyperdrive.go), or on D1 (d1.go), whose queues its drain.ts
// claims over the d1 binding, each claim in the binding's turn with the
// repositories' (D189, #702).
//
// A behavior worker (D103, D151) is a drainer too: its drain.ts runs its
// table's deadline, change and event queues at each firing, each claiming
// batches of up to its concurrency, with the handlers its main.ts runs
// under stack dev, its window held to its CPU time as every drainer's is.
// A firing that leaves a backlog logs a warning naming the queue, and the
// next firing takes the rest.
//
// Every drainer's Worker is also a Workflow of the class its drain.ts
// exports, which a write wakes before the next firing, and a drainer
// another drainer wakes has a wake relay too (wakes.go).

// Drainer runs TypeScript workers as Cloudflare Workers that drain their
// queues at each firing of a Cron Trigger.
const Drainer = "cloudflare.drainer"

// A worker's edges are its API's (D53): its calls to a server on Workers,
// its buckets on R2 and its database on Hyperdrive. Their connectors are a
// server's: a worker calls through a service binding, since it has no app
// of its own to call in process.
const (
	DrainerHTTPConnector = "cloudflare.drainer-workers"
	DrainerR2Connector   = "cloudflare.drainer-r2"
)

// DefaultCPUMilliseconds is the CPU time an invocation of a Worker may
// take unless its version's limits say otherwise: 30 seconds, by the CPU
// time table of Cloudflare's Workers limits (developers.cloudflare.com/
// workers/platform/limits). A behavior worker's window and grace fit
// within it, or within its cpuMs (D151).
const DefaultCPUMilliseconds = 30_000

// DrainSchedule is the Cron Trigger every worker's Worker fires on: every
// minute, the shortest a Cron Trigger takes.
const DrainSchedule = "* * * * *"

// drainerSettings is the schema of a worker's settings on the drainer
// platform: Workers Logs, on by default; cpuMs, the CPU time a firing may
// take, Cloudflare's default when unset, whose handlers' work counts and
// whose waits do not; telemetry, as a job's; and drainSeconds, how long each
// firing claims, ir.DefaultWorkerDrainSeconds unless set.
const drainerSettings = `{
  "type": "object",
  "properties": {
    "logs": {"type": "boolean"},
    "cpuMs": {"type": "integer", "minimum": 1, "maximum": 300000},
    "telemetry": {"type": "boolean"},
    "drainSeconds": {"type": "integer", "minimum": 1, "maximum": 900}
  },
  "additionalProperties": false
}`

// drainerPlatform is the drainer platform's registration.
func drainerPlatform() registry.PlatformSpec {
	return registry.PlatformSpec{
		Name:      Drainer,
		Kind:      ir.DeployableWorker,
		Languages: []string{registry.APILanguageTypeScript},
		Artifact:  ir.ArtifactBundle,
		Settings:  json.RawMessage(drainerSettings),
		NameOf:    workerName,
		AddressOf: func(registry.PlatformContext) any { return nil },
		Lower:     lowerDrainer,
		// A worker here drains at a Cron Trigger's firings, so a message
		// due before the next one wakes it, whatever its mode (D151).
		WakesWorkers: true,
	}
}

// lowerDrainer lowers a worker to its Worker's three nodes, workers.dev
// off, whose version runs its bundle, and, unless the environment runs it
// on no instance, a Cron Trigger every minute once its deployment is there.
// It refuses, as unrealizable: more than one instance, since each firing
// runs one claim loop and a Cron Trigger fires once a minute, so the
// worker's concurrency is what grows; an instance range, which no target
// scales yet (D53, amended); and a window and grace past the CPU time a
// firing may take, which Cloudflare would cut short. The CPU time is at
// most 300 s, so they never reach MaxScheduledRunSeconds.
func lowerDrainer(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := targetValuesOf(env.Values)
	run := d.Worker
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("worker %s has no run", d.Name)
	}
	if err := checkWorkerName("the Worker of worker "+d.Name, d.ResourceName, workerNameParts(env, d, "the worker")); err != nil {
		return registry.Lowered{}, err
	}
	if run.Instances > 1 {
		return registry.Lowered{}, registry.Unrealizable("worker %s runs %d instances in environment %s, and on Workers each firing of its Cron Trigger, once a minute, runs one claim loop; raise its concurrency, %d, which a firing handles at a time, and set instances to 1", d.Name, run.Instances, env.Name, run.Concurrency)
	}
	if run.MaxInstances > 0 {
		return registry.Lowered{}, registry.Unrealizable("worker %s may run up to %d instances (maxInstances) in environment %s, and no target scales a worker with its queue's depth yet (D53, amended); leave maxInstances out", d.Name, run.MaxInstances, env.Name)
	}
	grace := run.GraceSeconds
	if grace <= 0 {
		grace = ir.DefaultWorkerGraceSeconds
	}
	drain := ir.DefaultWorkerDrainSeconds
	set, hasDrain := wholeNumber(d.Settings["drainSeconds"])
	if hasDrain {
		drain = int(set)
	}
	// A firing ends before the invocation's CPU limit (D151, #600): its
	// window and grace, wall time, bound the CPU time it takes, whatever
	// its handlers do, a @worker's as a behavior worker's.
	what, decorator := "worker", "@worker"
	if len(run.Queues) > 0 {
		what, decorator = "behavior worker", "@behaviorWorker"
	}
	cpu := DefaultCPUMilliseconds
	if n, ok := wholeNumber(d.Settings["cpuMs"]); ok {
		cpu = int(n)
	}
	budget := cpu/1000 - grace
	switch {
	case budget < 1:
		return registry.Lowered{}, registry.Unrealizable("%s %s lets its handlers finish for %s, and a firing on Workers may take %dms of CPU time (cpuMs), which leaves it no time to claim; raise cpuMs or shorten its %s's grace (D151)", what, d.Name, seconds(grace), cpu, decorator)
	case hasDrain && drain > budget:
		return registry.Lowered{}, registry.Unrealizable("%s %s claims for %s and lets its handlers finish for %s at each firing, and a firing on Workers may take %dms of CPU time (cpuMs), which the two must fit within so the firing ends before Cloudflare stops it; shorten drainSeconds to %d or raise cpuMs (D151)", what, d.Name, seconds(drain), seconds(grace), cpu, budget)
	case !hasDrain && drain > budget:
		drain = budget
	}
	version, err := bundleVersion(ctx, v)
	if err != nil {
		return registry.Lowered{}, err
	}
	for _, b := range []map[string]any{
		{"type": bindingPlainText, "name": ir.WorkerConcurrencyVariable, "text": strconv.Itoa(run.Concurrency)},
		{"type": bindingPlainText, "name": ir.WorkerDrainSecondsVariable, "text": strconv.Itoa(drain)},
	} {
		if err := addBinding(version, b); err != nil {
			return registry.Lowered{}, fmt.Errorf("worker %s: %w", d.Name, err)
		}
	}
	// A drainer wakes another drainer through the other's wake relay
	// (#600, wakes.go): a service binding to the relay's Worker, which its
	// own Worker waits for, so nothing waits for the other's version.
	relays, err := addWakes(d, version, true)
	if err != nil {
		return registry.Lowered{}, err
	}
	logs := settingBool(d.Settings, "logs", true)
	out := registry.Lowered{Resources: workerNodes(d, v, false, logs, version)}
	// The Worker waits for each Worker its service bindings name, so a
	// destroy removes it, with its bindings, first (calls.go).
	deps := append(bindingDependencies(d), relays...)
	slices.Sort(deps)
	out.Resources[0].DependsOn = slices.Compact(deps)
	if run.Instances > 0 {
		// The Workflow a wake creates an instance of, of the class the
		// deployment's version exports, so it waits for the deployment
		// (D151).
		out.Resources = append(out.Resources, &ir.Resource{
			ID:        workflowID(d.Name),
			Type:      TypeWorkflow,
			DependsOn: []string{deploymentID(d.Name)},
			Properties: map[string]any{
				"accountId":    v.accountID,
				"workflowName": d.ResourceName,
				"scriptName":   ir.Output{Resource: workerID(d.Name), Name: "name"},
				"className":    ir.QueueWakeClass,
			},
		})
		out.Resources = append(out.Resources, &ir.Resource{
			ID:        scheduleID(d.Name),
			Type:      TypeWorkersCronTrigger,
			DependsOn: []string{deploymentID(d.Name)},
			Properties: map[string]any{
				"accountId":  v.accountID,
				"scriptName": ir.Output{Resource: workerID(d.Name), Name: "name"},
				"schedules":  []any{map[string]any{"cron": DrainSchedule}},
			},
		})
		// Another drainer's writes reach this one's queues: it wakes this
		// one through a relay, whose version binds the Workflow (#600).
		if wokenByDrainer(d) {
			relay, err := wakeRelay(ctx, v, logs)
			if err != nil {
				return registry.Lowered{}, err
			}
			out.Resources = append(out.Resources, relay...)
		}
	}
	return out, nil
}
