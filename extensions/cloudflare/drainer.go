package cloudflare

import (
	"encoding/json"
	"fmt"
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
// gives back the rest. The queue stays its Postgres table, so its keys,
// serialization, backoff, retention and dead messages are the table's
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
// Hyperdrive (hyperdrive.go): D1 refuses a worker's claims
// (PlatformSpec.NoQueueClaims).

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
	}
}

// lowerDrainer lowers a worker to its Worker's three nodes, workers.dev
// off, whose version runs its bundle, and, unless the environment runs it
// on no instance, a Cron Trigger every minute once its deployment is there.
// It refuses, as unrealizable: more than one instance, since each firing
// runs one claim loop and a Cron Trigger fires once a minute, so the
// worker's concurrency is what grows; an instance range, which no target
// scales yet (D53, amended); and a window and grace over
// MaxScheduledRunSeconds, which Cloudflare would cut short.
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
	drain := ir.DefaultWorkerDrainSeconds
	if n, ok := wholeNumber(d.Settings["drainSeconds"]); ok {
		drain = int(n)
	}
	grace := run.GraceSeconds
	if grace <= 0 {
		grace = ir.DefaultWorkerGraceSeconds
	}
	if drain+grace > MaxScheduledRunSeconds {
		return registry.Lowered{}, registry.Unrealizable("worker %s claims for %s and lets its handlers finish for %s at each firing, %s in all, and Cloudflare stops a scheduled run after %s; shorten drainSeconds or its @worker's grace", d.Name, seconds(drain), seconds(grace), seconds(drain+grace), seconds(MaxScheduledRunSeconds))
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
	out := registry.Lowered{Resources: workerNodes(d, v, false, settingBool(d.Settings, "logs", true), version)}
	// The Worker waits for each Worker its service bindings name, so a
	// destroy removes it, with its bindings, first (calls.go).
	out.Resources[0].DependsOn = bindingDependencies(d)
	if run.Instances > 0 {
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
	}
	return out, nil
}
