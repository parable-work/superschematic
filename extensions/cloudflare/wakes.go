package cloudflare

import (
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The wake of a worker on Workers (D151). A worker's Worker drains its
// queues at each firing of a Cron Trigger, a minute apart (drainer.go), so a
// message due before the next firing, an urgent deadline, an event or a
// short delay, would wait for it. Each drainer's Worker is also a
// Workflow, `<worker>.workflow`, of the class its drain.ts exports
// (ir.QueueWakeClass), named as the Worker is: an instance sleeps until the
// message it was created for is due and runs one firing of the drain. A
// deployable that may enqueue to one of the worker's queues
// (ir.ResolvedDeployable.Wakes, which resolution fills for every worker on
// a platform that takes wakes, registry.PlatformSpec.WakesWorkers) holds a
// workflow binding to that Workflow and QUEUE_WAKES, a plain_text binding
// that gives each queue the bindings that wake its workers; its runtime's
// queue client, rules adapter and entrypoint create the instance
// (installQueueWakes in the TypeScript runtime's ./workers).
//
// The binding names the Workflow by the worker's resource name, and the
// deployable's version waits for the Workflow's node, so the Workflow
// exists before a version binds to it. The Workflow waits for the worker's
// deployment, which waits for its version and its Worker, whose service
// bindings wait for its callees' Worker nodes alone (calls.go), never a
// version: a server the worker calls may wake it without a cycle. A
// server and a job bind the Workflow itself.
//
// A drainer wakes another drainer through the other's wake relay (#600):
// two drainers over one database each wake the other, and a version that
// bound the other's Workflow would wait for it, which waits for the other's
// version, which would wait for the first's Workflow. The relay is a Worker
// of the woken drainer's own, `<worker>.wake-relay`, named as the worker's
// Worker with `-wake` after it, which runs the script the extension embeds
// (wake-relay.js): its version binds the drainer's Workflow as WORKFLOW and
// waits for it, and its default entrypoint answers the waker's create with
// the Workflow's. A waking drainer holds a service binding of the name a
// server's workflow binding has, WAKE_<WORKER>, to the relay's Worker node,
// which sits in infrastructure and waits for nothing, and its own Worker
// node waits for the relay's, so a destroy removes the binding first, as a
// service binding's caller goes before its callee (calls.go). Nothing of
// the waking drainer's waits for the woken one's version, so the graph
// stays acyclic however the drainers wake each other. A drainer no other
// drainer wakes has no relay.
//
// A Durable Object per environment holding the earliest due time, whose
// alarm runs the drain, is the alternative not taken: a Durable Object
// class is created by a migration that Cloudflare applies on a deploy of a
// script, not on the upload of a version, and this target deploys every
// Worker as a version and a deployment. Nor a workflow binding of the
// other drainer's with no wait, which a first deploy may upload before the
// Workflow exists; nor a Cloudflare Queue between them, resources of
// another kind for a hint the relay carries.

// bindingWorkflow is the binding type of a Workflow.
const bindingWorkflow = "workflow"

// wakeBinding names the workflow binding through which a deployable wakes
// worker, a worker deployable: WAKE_ and its name in upper snake case.
func wakeBinding(worker string) string {
	var b strings.Builder
	b.WriteString("WAKE_")
	for _, r := range worker {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// addWakes adds to version, a deployable's version on Workers, the wakes of
// the workers on the drainer platform its writes may leave a message for
// (d.Wakes): a binding through which it wakes each worker, and QUEUE_WAKES,
// a JSON object that gives each queue, by its `@queue` class, the bindings
// of the workers that drain it, sorted. A server's or a job's binding is a
// workflow binding to the worker's Workflow, and addWakes returns the IDs
// of the Workflow nodes its version waits for; a drainer's (relayed) is a
// service binding to the worker's wake relay, and it returns the IDs of
// the relays' Worker nodes, which the drainer's Worker node waits for.
// Both sorted; none for a deployable with no such wake. A wake of a worker
// another platform runs is that platform's.
func addWakes(d ir.ResolvedDeployable, version map[string]any, relayed bool) ([]string, error) {
	byQueue := map[string][]string{}
	workers := map[string]any{}
	for _, wake := range d.Wakes {
		if wake.Platform != Drainer {
			continue
		}
		name := wakeBinding(wake.Worker)
		if !slices.Contains(byQueue[wake.Queue], name) {
			byQueue[wake.Queue] = append(byQueue[wake.Queue], name)
		}
		workers[wake.Worker] = wake.ResourceName
	}
	if len(byQueue) == 0 {
		return nil, nil
	}
	wakes := map[string][]ir.QueueWake{}
	for queue, names := range byQueue {
		slices.Sort(names)
		for _, name := range names {
			wakes[queue] = append(wakes[queue], ir.QueueWake{Binding: name})
		}
	}
	text, err := json.Marshal(wakes)
	if err != nil {
		return nil, fmt.Errorf("the wakes of %s: %w", d.Name, err)
	}
	if err := addBinding(version, map[string]any{"type": bindingPlainText, "name": ir.QueueWakesVariable, "text": string(text)}); err != nil {
		return nil, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
	}
	var waits []string
	for _, worker := range slices.Sorted(maps.Keys(workers)) {
		binding := map[string]any{"type": bindingWorkflow, "name": wakeBinding(worker), "workflowName": workers[worker]}
		wait := workflowID(worker)
		if relayed {
			wait = workerID(relayID(worker))
			binding = map[string]any{"type": bindingService, "name": wakeBinding(worker), "service": ir.Output{Resource: wait, Name: "name"}}
		}
		if err := addBinding(version, binding); err != nil {
			return nil, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
		}
		waits = append(waits, wait)
	}
	return waits, nil
}

// waitForWakes makes the version node of a deployable's Worker nodes
// (workerNodes) wait for the Workflows it binds to wake workers.
func waitForWakes(resources []*ir.Resource, waits []string) {
	if len(waits) == 0 {
		return
	}
	version := resources[1]
	version.DependsOn = append(version.DependsOn, waits...)
	slices.Sort(version.DependsOn)
	version.DependsOn = slices.Compact(version.DependsOn)
}

// The script of a wake relay's Worker, and the binding through which it
// creates the woken drainer's runs (wake-relay.js).
const (
	WakeRelayModule          = "wake-relay.js"
	WakeRelayWorkflowBinding = "WORKFLOW"
)

//go:embed wake-relay.js
var wakeRelayScript string

// WakeRelayScript returns the script every wake relay's Worker runs: an ES
// module whose default entrypoint answers a waker's create with its
// WORKFLOW binding's (#600).
func WakeRelayScript() string { return wakeRelayScript }

// relayID names a worker's wake relay as its nodes' IDs are made from it:
// `<worker>.wake-relay.worker`, `.version` and `.deployment`.
func relayID(worker string) string { return worker + ".wake-relay" }

// wokenByDrainer reports whether a drainer may write to one of d's queues,
// so d, a drainer, needs a wake relay.
func wokenByDrainer(d ir.ResolvedDeployable) bool {
	return slices.ContainsFunc(d.WokenBy, func(w ir.ResolvedWaker) bool { return w.Platform == Drainer })
}

// wakeRelay returns the three nodes of the wake relay of the context's
// deployable, a drainer another drainer wakes: a Worker named as the
// drainer's with `-wake` after it, workers.dev off, logs as the drainer's,
// whose version runs the embedded script with the drainer's Workflow bound
// as WORKFLOW and waits for that Workflow's node.
func wakeRelay(ctx registry.PlatformContext, v accountValues, logs bool) ([]*ir.Resource, error) {
	d := ctx.Deployable
	env := ctx.Environment
	name := accountName(env, d.Name, "wake")
	if err := checkWorkerName("the wake relay of worker "+d.Name, name, partsOf(env, "the worker", d.Name, "wake")); err != nil {
		return nil, err
	}
	version := map[string]any{
		"mainModule": WakeRelayModule,
		"modules": []any{map[string]any{
			"name":          WakeRelayModule,
			"contentType":   "application/javascript+module",
			"contentBase64": base64.StdEncoding.EncodeToString([]byte(wakeRelayScript)),
		}},
		"compatibilityDate": CompatibilityDate,
		"bindings": []any{
			map[string]any{"type": bindingWorkflow, "name": WakeRelayWorkflowBinding, "workflowName": d.ResourceName},
		},
	}
	nodes := workerNodes(ir.ResolvedDeployable{Name: relayID(d.Name), ResourceName: name}, v, false, logs, version)
	nodes[1].DependsOn = []string{workflowID(d.Name)}
	return nodes, nil
}
