package cloudflare

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
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
// server and a job take wakes; a drainer takes none, since two drainers
// over one database would each wait for the other's Workflow: what a
// worker's handlers leave another worker, that worker's next firing takes.
//
// A Durable Object per environment holding the earliest due time, whose
// alarm runs the drain, is the alternative not taken: a Durable Object
// class is created by a migration that Cloudflare applies on a deploy of a
// script, not on the upload of a version, and this target deploys every
// Worker as a version and a deployment.

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
// (d.Wakes): a workflow binding to each worker's Workflow, and QUEUE_WAKES,
// a JSON object that gives each queue, by its `@queue` class, the bindings
// of the workers that drain it, sorted. It returns the IDs of the Workflow
// nodes the version waits for, sorted; none for a deployable with no such
// wake. A wake of a worker another platform runs is that platform's.
func addWakes(d ir.ResolvedDeployable, version map[string]any) ([]string, error) {
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
		if err := addBinding(version, map[string]any{"type": bindingWorkflow, "name": wakeBinding(worker), "workflowName": workers[worker]}); err != nil {
			return nil, fmt.Errorf("%s %s: %w", d.Kind, d.Name, err)
		}
		waits = append(waits, workflowID(worker))
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
