package gcp

import (
	"fmt"
	"strconv"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Cloud Run worker pool platform (docs/stack-model.md, sections 7.2
// and 8.8, D53): a worker is a Cloud Run worker pool, which runs its image
// with no port and no URL, as many instances as the environment says,
// with its own service account and what a server of its API takes
// (lowerWorkload): the API's secrets, its Cloud SQL volume, its VPC egress
// when it calls a server, and its config. Nothing calls a worker, so the
// pool has no invoker and no IAM of its own.
//
// A worker pool scales manually: its instance count is the worker's
// `instances` setting, one unless set. A worker the environment turns off
// keeps its pool at zero instances, so its messages wait, its image stays
// pinned in the graph, and a later deploy turns it on again. Each instance
// handles as many messages at a time as the worker's concurrency, which
// reaches the entrypoint as ir.WorkerConcurrencyVariable. A worker pool's
// CPU is always allocated: it bills for its instances' whole lives, as a
// process that claims between messages needs.
//
// Cloud Run sends SIGTERM to an instance it stops and SIGKILL ten seconds
// later. The entrypoint gives its running handlers the worker's grace,
// then releases what has not finished, so a grace of ten seconds or more
// would be cut short and is refused.

// workerShutdownSeconds is how long Cloud Run lets an instance run after
// SIGTERM before it kills it.
const workerShutdownSeconds = 10

// lowerWorker lowers a worker to what lowerWorkload makes of it and the
// worker pool, named after the deployable as a server's service is
// (serviceName), with manual scaling to the worker's instances.
func lowerWorker(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	v := valuesOf(ctx.Environment)
	run := d.Worker
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("worker %s has no run", d.Name)
	}
	if run.GraceSeconds >= workerShutdownSeconds {
		return registry.Lowered{}, fmt.Errorf("worker %s lets its handlers finish for %ds when it stops, and Cloud Run stops a worker pool's instance %ds after SIGTERM; give its @worker a grace under %ds", d.Name, run.GraceSeconds, workerShutdownSeconds, workerShutdownSeconds)
	}
	out, w, err := lowerWorkload(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	container := w.container
	envs, _ := container["envs"].([]any)
	container["envs"] = append(envs, map[string]any{"name": ir.WorkerConcurrencyVariable, "value": strconv.Itoa(run.Concurrency)})
	template := w.template
	template["containers"] = []any{container}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".worker-pool", Type: TypeWorkerPool, Properties: map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"scaling": map[string]any{
			"scalingMode":         "MANUAL",
			"manualInstanceCount": run.Instances,
		},
		"template": template,
	}})
	return out, nil
}
