package gcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"

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
//
// A worker in drain mode (D145) is no worker pool: it is a Cloud Run job,
// lowerDrainWorker's, which runs when something starts it and exits when
// its queue is empty, so a queue that is empty most of the day runs
// nothing.

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
	if run.Drains() {
		return lowerDrainWorker(ctx)
	}
	if _, set := d.Settings["tick"]; set {
		return registry.Lowered{}, fmt.Errorf("worker %s sets tick, how often a worker in drain mode is run; it runs as a worker pool, whose instances keep running, so leave tick out or set its mode to %q (D145)", d.Name, ir.WorkerModeDrain)
	}
	if run.MaxInstances > run.Instances {
		// A worker pool's instance count is manual, and Cloud Run scales
		// none by itself; the scaler that would read the queue's depth
		// and set it is designed, not built (D53, amended).
		return registry.Lowered{}, fmt.Errorf("worker %s may run from %d to %d instances, and scaling a worker pool with its queue's depth is not built: a Cloud Run worker pool runs the instance count it is given; set instances alone (D53, amended)", d.Name, run.Instances, run.MaxInstances)
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
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".worker-pool", Type: TypeWorkerPool, Properties: w.labeled(map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"scaling": map[string]any{
			"scalingMode":         "MANUAL",
			"manualInstanceCount": run.Instances,
		},
		"template": template,
	})})
	return out, nil
}

// The drain worker (D145). A worker whose settings give it mode drain is a
// Cloud Run job with the worker's image, account, config and connections,
// and no worker pool. Each execution is one task, the worker's
// entrypoint, which the environment tells to drain
// (ir.WorkerModeVariable): it claims as a pool's instance does and exits 0
// once no message was ready or held for the worker's idle time. Three
// things start an execution:
//
//   - a wake: each deployable that may enqueue to the queue holds
//     roles/run.invoker on this job alone (wakerNodes) and the job's run
//     method in ir.QueueWakesVariable (wakesEnv), and its queue client
//     calls it after an enqueue, at most once per queue in five seconds
//     per process;
//   - the tick: a Cloud Scheduler job that runs the job every `tick`, five
//     minutes unless the worker's settings say, as the job's own account,
//     as a scheduled job's schedule does (scheduleNodes). It finds a
//     message a trigger enqueued, a retry or a delayed message that is due
//     later, and a message whose wake was lost;
//   - nothing else: Cloud Run retries no task of it, since the next wake
//     or tick runs it again.
//
// `instances` is how many of the worker may run at once, one unless set:
// each task takes one of that many advisory locks named for its queue as
// it starts (ir.WorkerSlotsVariable) and exits at once when none is free,
// so starts that overlap cost a start and nothing else, and the target
// asks Cloud Run nothing before a wake. A worker the environment turns off
// keeps its job, which pins its image, and loses its tick and its wakes,
// so nothing runs it.
//
// A task that outlives drainTaskSeconds is stopped as any instance is, with
// SIGTERM and the worker's grace, and the next wake or tick starts
// another. A Rust worker's entrypoint does not drain yet, so one is
// refused here rather than left to run until that limit.

// drainTaskSeconds is the longest one task of a drain worker runs: a day,
// which a queue that never empties reaches.
const drainTaskSeconds = 24 * 60 * 60

// defaultDrainTick is how often a drain worker's job is run unless its
// settings say.
const defaultDrainTick = "5m"

// drainTick reads a drain worker's tick as the cron schedule Cloud
// Scheduler runs: a whole number of minutes that divides an hour, or of
// hours that divides a day, so the firings are evenly spaced.
func drainTick(tick string) (string, error) {
	d, err := time.ParseDuration(tick)
	if err != nil || d <= 0 {
		return "", fmt.Errorf("tick is %q; it is a duration of whole minutes or hours, such as %q", tick, defaultDrainTick)
	}
	minutes := int(d / time.Minute)
	switch {
	case d%time.Minute != 0:
	case minutes == 1:
		return "* * * * *", nil
	case minutes < 60 && 60%minutes == 0:
		return fmt.Sprintf("*/%d * * * *", minutes), nil
	case minutes == 60:
		return "0 * * * *", nil
	case minutes%60 == 0 && minutes/60 < 24 && 24%(minutes/60) == 0:
		return fmt.Sprintf("0 */%d * * *", minutes/60), nil
	case minutes == 24*60:
		return "0 0 * * *", nil
	}
	return "", fmt.Errorf("tick is %q; Cloud Scheduler runs a schedule of evenly spaced firings, so a tick is 1m, 2m, 3m, 4m, 5m, 6m, 10m, 12m, 15m, 20m or 30m, or 1h, 2h, 3h, 4h, 6h, 8h, 12h or 24h", tick)
}

// lowerDrainWorker lowers a worker in drain mode to what lowerWorkload
// makes of it, the Cloud Run job, named after the deployable as a pool
// would be, and the tick's nodes, unless the environment turns the worker
// off.
func lowerDrainWorker(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	run := d.Worker
	if d.Language == registry.APILanguageRust {
		return registry.Lowered{}, fmt.Errorf("worker %s runs in drain mode and is written in Rust, whose worker entrypoint does not exit when its queue is empty yet: as a Cloud Run job it would run until its task's time limit; leave its mode out, so it runs as a worker pool (D145)", d.Name)
	}
	if run.GraceSeconds >= workerShutdownSeconds {
		return registry.Lowered{}, fmt.Errorf("worker %s lets its handlers finish for %ds when it stops, and Cloud Run stops a job's task %ds after SIGTERM; give its @worker a grace under %ds", d.Name, run.GraceSeconds, workerShutdownSeconds, workerShutdownSeconds)
	}
	if name, ok := d.ResourceName.(string); ok && name == migrationJobName(env.Stack) {
		return registry.Lowered{}, fmt.Errorf("worker %s would be Cloud Run job %s, the stack's migration job (section 8.4); rename the worker's class or its API", d.Name, name)
	}
	tick := defaultDrainTick
	if set, ok := d.Settings["tick"].(string); ok {
		tick = set
	}
	schedule, err := drainTick(tick)
	if err != nil {
		return registry.Lowered{}, fmt.Errorf("worker %s: %w", d.Name, err)
	}
	out, w, err := lowerWorkload(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	slots := max(run.Instances, 1)
	idle := run.IdleSeconds
	if idle <= 0 {
		idle = ir.DefaultWorkerIdleSeconds
	}
	container := w.container
	envs, _ := container["envs"].([]any)
	container["envs"] = append(envs,
		map[string]any{"name": ir.WorkerConcurrencyVariable, "value": strconv.Itoa(run.Concurrency)},
		map[string]any{"name": ir.WorkerModeVariable, "value": ir.WorkerModeDrain},
		map[string]any{"name": ir.WorkerIdleSecondsVariable, "value": strconv.Itoa(idle)},
		map[string]any{"name": ir.WorkerSlotsVariable, "value": strconv.Itoa(slots)},
	)
	task := w.template
	task["containers"] = []any{container}
	task["timeout"] = fmt.Sprintf("%ds", drainTaskSeconds)
	// Cloud Run's default is 3 retries, so none is written as 0.
	task["maxRetries"] = 0
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".job", Type: TypeJob, Properties: w.labeled(map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"template": map[string]any{
			"taskCount": 1,
			"template":  task,
		},
	})})
	if run.Instances > 0 {
		out.Resources = append(out.Resources, tickNodes(env, v, d, tick, schedule)...)
		out.Resources = append(out.Resources, wakerNodes(v, d)...)
	}
	return out, nil
}

// wakerPlatforms are the platforms whose deployables run as an account of
// their own, `<deployable>.account` (lowerWorkload), which a drain
// worker's job can grant the right to run it.
var wakerPlatforms = []string{CloudRun, CloudRunJob, CloudRunWorker, CloudRunApp}

// wakerNodes are the grants that let a drain worker's wakers start it
// (D145): roles/run.invoker on the worker's job alone, for the account of
// each deployable that may enqueue to its queue (the worker's WokenBy).
// They are the worker's nodes, not the wakers': the job rolls out in the
// worker's wave, which may be after a waker's, and a grant on it cannot
// come sooner. A waker that rolled out first sends wakes the job refuses
// until then, which its queue client logs, and the tick finds the
// messages. A waker another target runs gets no grant.
func wakerNodes(v values, d ir.ResolvedDeployable) []*ir.Resource {
	var nodes []*ir.Resource
	for _, waker := range d.WokenBy {
		if !slices.Contains(wakerPlatforms, waker.Platform) {
			continue
		}
		nodes = append(nodes, &ir.Resource{
			ID:   d.Name + ".woken-by." + waker.Deployable,
			Type: TypeJobIAMMember,
			Properties: map[string]any{
				"project":  v.project,
				"location": v.region,
				"name":     ir.Output{Resource: d.Name + ".job", Name: "name"},
				"role":     "roles/run.invoker",
				"member":   ir.Output{Resource: waker.Deployable + ".account", Name: "member"},
			},
		})
	}
	return nodes
}

// tickNodes are the nodes of a drain worker's tick (D145), as a job's
// schedule's are (scheduleNodes): the worker's account's
// roles/run.invoker on its job, and the Cloud Scheduler job, named as the
// job is, that runs it on the schedule, in UTC, as that account.
func tickNodes(env registry.StackEnvironment, v values, d ir.ResolvedDeployable, tick, schedule string) []*ir.Resource {
	job := ir.Output{Resource: d.Name + ".job", Name: "name"}
	account := d.Name + ".account"
	invoker := d.Name + ".tick-invoker"
	return []*ir.Resource{
		{
			ID:   invoker,
			Type: TypeJobIAMMember,
			Properties: map[string]any{
				"project":  v.project,
				"location": v.region,
				"name":     job,
				"role":     "roles/run.invoker",
				"member":   ir.Output{Resource: account, Name: "member"},
			},
		},
		{
			ID:        d.Name + ".tick",
			Type:      TypeSchedulerJob,
			DependsOn: []string{invoker},
			Properties: map[string]any{
				"project":     v.project,
				"region":      v.region,
				"name":        d.ResourceName,
				"description": fmt.Sprintf("Runs worker %s of %s %s every %s, to drain its queue", d.Name, env.Stack, env.Name, tick),
				"schedule":    schedule,
				"timeZone":    "Etc/UTC",
				"httpTarget": map[string]any{
					"httpMethod": "POST",
					"uri":        join("https://run.googleapis.com/v2/projects/", v.project, "/locations/", v.region, "/jobs/", job, ":run"),
					"oauthToken": map[string]any{"serviceAccountEmail": ir.Output{Resource: account, Name: "email"}},
				},
			},
		},
	}
}

// The wake of a drain worker (D145), as the deployables that may enqueue
// to its queue take it (ir.ResolvedDeployable.Wakes): the Cloud Run Admin
// API's run method of the worker's job, with the access token the
// metadata server gives the deployable's own account. The runtimes' queue
// clients read it as an ir.QueueWake and name no provider.
const (
	metadataTokenURL    = "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token"
	metadataFlavor      = "Metadata-Flavor"
	metadataFlavorValue = "Google"
)

// wakesEnv is the environment variable that gives a deployable the wakes
// of the drain workers its enqueues may leave a message for,
// ir.QueueWakesVariable: a JSON object that gives each queue the list of
// its wakes. The job is named by the worker's resource name, not by its
// node's output, so the deployable's Cloud Run resource does not wait for
// the worker's job, which may itself wait for the deployable when the
// worker calls its API. The right to run the job is the worker's node
// (wakerNodes). It returns nil for a deployable with no wake on this
// platform: a worker another target runs is left to its own tick.
func wakesEnv(v values, d ir.ResolvedDeployable) map[string]any {
	byQueue := map[string][]any{}
	for _, wake := range d.Wakes {
		if wake.Platform != CloudRunWorker {
			continue
		}
		byQueue[wake.Queue] = append(byQueue[wake.Queue], wake.ResourceName)
	}
	if len(byQueue) == 0 {
		return nil
	}
	token, _ := json.Marshal(ir.QueueWakeToken{URL: metadataTokenURL, Headers: map[string]string{metadataFlavor: metadataFlavorValue}})
	parts := []any{"{"}
	queues := make([]string, 0, len(byQueue))
	for queue := range byQueue {
		queues = append(queues, queue)
	}
	slices.Sort(queues)
	for i, queue := range queues {
		if i > 0 {
			parts = append(parts, ",")
		}
		name, _ := json.Marshal(queue)
		parts = append(parts, string(name), ":[")
		for j, job := range byQueue[queue] {
			if j > 0 {
				parts = append(parts, ",")
			}
			parts = append(parts, `{"url":"https://run.googleapis.com/v2/projects/`, v.project, "/locations/", v.region, "/jobs/", job, `:run","token":`, string(token), "}")
		}
		parts = append(parts, "]")
	}
	parts = append(parts, "}")
	return map[string]any{"name": ir.QueueWakesVariable, "value": join(parts...)}
}

// lowerRunner lowers an engine's runner (D58) to what lowerWorkload makes
// of it and a worker pool of its own, named after the deployable, with
// manual scaling to the runner's instances: one, which runs the engine's
// runner while it holds the runner's lock on the database, or none when
// the environment turns it off. A Cloud Run service gives an instance CPU
// only while it serves, which would starve the runner between requests;
// a worker pool's CPU is always allocated. Its one edge is its database,
// so its config is the connection alone.
func lowerRunner(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	v := valuesOf(ctx.Environment)
	run := d.Runner
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("runner %s has no engine database", d.Name)
	}
	out, w, err := lowerWorkload(ctx)
	if err != nil {
		return registry.Lowered{}, err
	}
	template := w.template
	template["containers"] = []any{w.container}
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".worker-pool", Type: TypeWorkerPool, Properties: w.labeled(map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"scaling": map[string]any{
			"scalingMode":         "MANUAL",
			"manualInstanceCount": run.Instances,
		},
		"template": template,
	})})
	return out, nil
}

// lowerDeriver lowers a deriver (D74) to what lowerWorkload makes of it and
// a worker pool of its own, named after the deployable, with manual
// scaling to the deriver's instances: one, or none when the environment
// turns it off, which leaves its fields stale. Its concurrency, how many
// answers it runs at a time, reaches the entrypoint as
// ir.WorkerConcurrencyVariable, as a worker's does. A worker pool's CPU is
// always allocated, which a loop that waits for commits needs.
func lowerDeriver(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	v := valuesOf(ctx.Environment)
	run := d.Deriver
	if run == nil {
		return registry.Lowered{}, fmt.Errorf("deriver %s has no run", d.Name)
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
	out.Resources = append(out.Resources, &ir.Resource{ID: d.Name + ".worker-pool", Type: TypeWorkerPool, Properties: w.labeled(map[string]any{
		"project":            v.project,
		"location":           v.region,
		"name":               d.ResourceName,
		"deletionProtection": false,
		"scaling": map[string]any{
			"scalingMode":         "MANUAL",
			"manualInstanceCount": run.Instances,
		},
		"template": template,
	})})
	return out, nil
}
