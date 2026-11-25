package gcp_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// The drain worker on gcp (D145): shop-orders' worker FulfilOrders, which
// Staging's settings put in drain mode, is a Cloud Run job its enqueuers
// wake and a Cloud Scheduler job ticks, and no worker pool.

// drainShop is the shop whose worker FulfilOrders runs in drain mode in
// Staging, and so in Preview, which extends it.
func drainShop() *ir.Stack {
	s := shop()
	s.Name = "DrainShop"
	workerSettings(s, "Staging").Mode = ir.WorkerModeDrain
	return s
}

// TestDrainGolden resolves DrainShop in Staging and in Preview and checks
// each environment.json against its golden file: the worker's job, its
// tick and the tick's grant, and, on shop-api's server, Orders and the job
// ShipOrders, which connect to the queue's database, the grant to run the
// worker's job and QUEUE_WAKES. Preview's member names the job under its
// parameter.
func TestDrainGolden(t *testing.T) {
	reg := assemble(t)
	for _, env := range []string{"Staging", "Preview"} {
		t.Run(env, func(t *testing.T) {
			checkGolden(t, reg, drainShop(), stacktest.AcmeShop(), env)
		})
	}
}

// TestDrainWorker checks what a worker in drain mode lowers to: a Cloud
// Run job of one task with no retries and a day to run, the worker's
// account, config and connections, and the variables that tell its
// entrypoint to drain; the tick, a Cloud Scheduler job that runs it every
// five minutes as the worker's own account, which holds roles/run.invoker
// on the job; and no worker pool.
func TestDrainWorker(t *testing.T) {
	env := resolve(t, assemble(t), drainShop(), stacktest.AcmeShop(), "Staging")
	d := env.Deployable(fulfilOrders)
	if d == nil || d.Platform != gcp.CloudRunWorker || !d.Worker.Drains() || d.Worker.IdleSeconds != 30 || d.Worker.Instances != 1 {
		t.Fatalf("the worker is %+v, running %+v", d, d.Worker)
	}
	if got := strings.Join(ownedBy(env, fulfilOrders), ", "); got != "network, network.nat, network.router, network.subnet, secret.PaymentsSecrets.STRIPE_KEY, "+
		fulfilOrders+".account, "+fulfilOrders+".job, "+fulfilOrders+".reads.PaymentsSecrets.STRIPE_KEY, "+fulfilOrders+".tick, "+fulfilOrders+".tick-invoker, "+
		fulfilOrders+".woken-by.Orders, "+fulfilOrders+".woken-by.shop-api, "+fulfilOrders+".woken-by."+stacktest.ShipOrdersJob {
		t.Errorf("the drain worker lowers to %s", got)
	}
	job := node(t, env, fulfilOrders+".job")
	if job.Type != gcp.TypeJob || job.Properties["name"] != fulfilOrders || job.Properties["deletionProtection"] != false {
		t.Errorf("the job node: %s %v", job.Type, job.Properties)
	}
	execution := job.Properties["template"].(map[string]any)
	task := execution["template"].(map[string]any)
	wantJSON(t, "the job's task", map[string]any{"taskCount": execution["taskCount"], "timeout": task["timeout"], "maxRetries": task["maxRetries"], "serviceAccount": task["serviceAccount"], "volumes": task["volumes"]},
		`{"maxRetries":0,"serviceAccount":{"$output":{"resource":"shop-orders-fulfil-orders.account","name":"email"}},"taskCount":1,"timeout":"86400s",`+
			`"volumes":[{"cloudSqlInstance":{"instances":[{"$output":{"resource":"shop-db.instance","name":"connectionName"}}]},"name":"cloudsql"}]}`)
	container := task["containers"].([]any)[0].(map[string]any)
	if container["image"] != "us-east1-docker.pkg.dev/acme-staging/drain-shop/shop-orders-fulfil-orders" {
		t.Errorf("the job runs %v, want the worker's repository path, which the deploy pins", container["image"])
	}
	envs := container["envs"].([]any)
	wantJSON(t, "the worker's last variables", envs[len(envs)-4:],
		`[{"name":"WORKER_CONCURRENCY","value":"4"},{"name":"WORKER_MODE","value":"drain"},{"name":"WORKER_IDLE_SECONDS","value":"30"},{"name":"WORKER_SLOTS","value":"1"}]`)
	for _, v := range envs {
		if name := v.(map[string]any)["name"]; name == ir.QueueWakesVariable {
			t.Errorf("the worker is given a wake of itself: %v", v)
		}
	}
	wantJSON(t, "the tick's grant", node(t, env, fulfilOrders+".tick-invoker").Properties,
		`{"location":"us-east1","member":{"$output":{"resource":"shop-orders-fulfil-orders.account","name":"member"}},`+
			`"name":{"$output":{"resource":"shop-orders-fulfil-orders.job","name":"name"}},"project":"acme-staging","role":"roles/run.invoker"}`)
	tick := node(t, env, fulfilOrders+".tick")
	if tick.Type != gcp.TypeSchedulerJob || !slices.Contains(tick.DependsOn, fulfilOrders+".tick-invoker") {
		t.Errorf("the tick node: %s, after %v", tick.Type, tick.DependsOn)
	}
	wantJSON(t, "the tick", tick.Properties,
		`{"description":"Runs worker shop-orders-fulfil-orders of DrainShop Staging every 5m, to drain its queue",`+
			`"httpTarget":{"httpMethod":"POST","oauthToken":{"serviceAccountEmail":{"$output":{"resource":"shop-orders-fulfil-orders.account","name":"email"}}},`+
			`"uri":{"$concat":["https://run.googleapis.com/v2/projects/acme-staging/locations/us-east1/jobs/",{"$output":{"resource":"shop-orders-fulfil-orders.job","name":"name"}},":run"]}},`+
			`"name":"shop-orders-fulfil-orders","project":"acme-staging","region":"us-east1","schedule":"*/5 * * * *","timeZone":"Etc/UTC"}`)
	for _, r := range env.Resources.Resources {
		if r.ID == fulfilOrders+".worker-pool" {
			t.Errorf("the drain worker has a worker pool: %v", r.Properties)
		}
	}
}

// TestDrainWorkerWakes: each deployable that may enqueue to the drain
// worker's queue, shop-api's server, Orders and the job ShipOrders, holds
// roles/run.invoker on the worker's job alone, a node of the worker's,
// and, in QUEUE_WAKES, the job's run method keyed by the queue, with the
// metadata server as where its token comes from. The job is named there by
// its resource name, not by its node's output, so no server waits for the
// worker's job, which rolls out after the servers it calls.
func TestDrainWorkerWakes(t *testing.T) {
	env := resolve(t, assemble(t), drainShop(), stacktest.AcmeShop(), "Staging")
	const wakes = `{"OrderPlaced":[{"url":"https://run.googleapis.com/v2/projects/acme-staging/locations/us-east1/jobs/shop-orders-fulfil-orders:run",` +
		`"token":{"url":"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token","headers":{"Metadata-Flavor":"Google"}}}]}`
	containerOf := map[string]func(*ir.Resource) map[string]any{
		"shop-api.service": func(r *ir.Resource) map[string]any { return r.Properties["template"].(map[string]any) },
		"Orders.service":   func(r *ir.Resource) map[string]any { return r.Properties["template"].(map[string]any) },
		stacktest.ShipOrdersJob + ".job": func(r *ir.Resource) map[string]any {
			return r.Properties["template"].(map[string]any)["template"].(map[string]any)
		},
	}
	for id, template := range containerOf {
		deployable := strings.TrimSuffix(strings.TrimSuffix(id, ".service"), ".job")
		container := template(node(t, env, id))["containers"].([]any)[0].(map[string]any)
		var got any
		for _, v := range container["envs"].([]any) {
			if v.(map[string]any)["name"] == ir.QueueWakesVariable {
				got = v.(map[string]any)["value"]
			}
		}
		if got != wakes {
			t.Errorf("%s's QUEUE_WAKES = %v\nwant %s", deployable, got, wakes)
		}
		wantJSON(t, deployable+"'s grant", node(t, env, fulfilOrders+".woken-by."+deployable).Properties,
			`{"location":"us-east1","member":{"$output":{"resource":"`+deployable+`.account","name":"member"}},`+
				`"name":{"$output":{"resource":"shop-orders-fulfil-orders.job","name":"name"}},"project":"acme-staging","role":"roles/run.invoker"}`)
	}
	if n := len(wakeGrants(env)); n != 3 {
		t.Errorf("%d deployables hold a grant to wake the worker, want shop-api, Orders and the job: %v", n, wakeGrants(env))
	}

	// A member of Preview names the job under its parameter.
	preview := resolve(t, assemble(t), drainShop(), stacktest.AcmeShop(), "Preview")
	container := node(t, preview, "shop-api.service").Properties["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	for _, v := range container["envs"].([]any) {
		if v.(map[string]any)["name"] == ir.QueueWakesVariable {
			wantJSON(t, "a member's QUEUE_WAKES", v.(map[string]any)["value"],
				`{"$concat":["{\"OrderPlaced\":[{\"url\":\"https://run.googleapis.com/v2/projects/acme-staging/locations/us-east1/jobs/shop-orders-fulfil-orders-pr",{"$parameter":"pr"},`+
					`":run\",\"token\":{\"url\":\"http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token\",\"headers\":{\"Metadata-Flavor\":\"Google\"}}}]}"]}`)
		}
	}

	// A pool's worker is woken by nothing.
	pool := resolve(t, assemble(t), shop(), stacktest.AcmeShop(), "Staging")
	if got := wakeGrants(pool); len(got) != 0 {
		t.Errorf("a pool's worker has wake grants: %v", got)
	}
}

// wakeGrants are the IDs of the grants to run a drain worker's job.
func wakeGrants(env *ir.ResolvedEnvironment) []string {
	var ids []string
	for _, r := range env.Resources.Resources {
		if strings.Contains(r.ID, ".woken-by.") {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// TestDrainWorkerSettings: instances are how many of the worker may run at
// once, which its entrypoint reads as WORKER_SLOTS, and every execution is
// still one task; idle reaches it as WORKER_IDLE_SECONDS; tick sets the
// schedule; a drain worker the environment turns off keeps its job and
// loses its tick; and a tick that Cloud Scheduler cannot space evenly, a
// tick on a pool's worker, a Rust worker and a grace of ten seconds are
// refused.
func TestDrainWorkerSettings(t *testing.T) {
	reg := assemble(t)
	s := drainShop()
	three := 3
	set := workerSettings(s, "Staging")
	set.Instances, set.Idle = &three, "2m"
	set.Values = map[string]any{"tick": "15m", "memory": "1Gi"}
	env := resolve(t, reg, s, stacktest.AcmeShop(), "Staging")
	execution := node(t, env, fulfilOrders+".job").Properties["template"].(map[string]any)
	if fmt.Sprint(execution["taskCount"]) != "1" {
		t.Errorf("an execution runs %v tasks, want one: each start takes one of the worker's slots", execution["taskCount"])
	}
	container := execution["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	envs := container["envs"].([]any)
	wantJSON(t, "the worker's last variables", envs[len(envs)-3:],
		`[{"name":"WORKER_MODE","value":"drain"},{"name":"WORKER_IDLE_SECONDS","value":"120"},{"name":"WORKER_SLOTS","value":"3"}]`)
	wantJSON(t, "the worker's limits", container["resources"], `{"limits":{"cpu":"1","memory":"1Gi"}}`)
	tick := node(t, env, fulfilOrders+".tick").Properties
	if tick["schedule"] != "*/15 * * * *" || !strings.Contains(tick["description"].(string), "every 15m") {
		t.Errorf("the tick runs on %v (%v), want every 15 minutes", tick["schedule"], tick["description"])
	}

	for tick, want := range map[string]string{"1m": "* * * * *", "30m": "*/30 * * * *", "60m": "0 * * * *", "1h": "0 * * * *", "6h": "0 */6 * * *", "24h": "0 0 * * *"} {
		s := drainShop()
		workerSettings(s, "Staging").Values = map[string]any{"tick": tick}
		env := resolve(t, reg, s, stacktest.AcmeShop(), "Staging")
		if got := node(t, env, fulfilOrders+".tick").Properties["schedule"]; got != want {
			t.Errorf("a tick of %s runs on %v, want %s", tick, got, want)
		}
	}

	off := false
	s = drainShop()
	workerSettings(s, "Staging").Enabled = &off
	env = resolve(t, reg, s, stacktest.AcmeShop(), "Staging")
	if job := node(t, env, fulfilOrders+".job"); job.Type != gcp.TypeJob {
		t.Errorf("a drain worker turned off has no job: %v", job)
	}
	for _, r := range env.Resources.Resources {
		if strings.HasPrefix(r.ID, fulfilOrders+".tick") || strings.Contains(r.ID, ".woken-by.") {
			t.Errorf("a drain worker turned off keeps %s", r.ID)
		}
	}

	refused := func(what string, s *ir.Stack, services []stack.Service, want string) {
		t.Helper()
		_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: "Staging"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want it to say %q", what, err, want)
		}
	}
	for _, tick := range []string{"7m", "90m", "5h", "48h"} {
		s := drainShop()
		workerSettings(s, "Staging").Values = map[string]any{"tick": tick}
		refused("a tick of "+tick, s, stacktest.AcmeShop(), `tick is "`+tick+`"; Cloud Scheduler runs a schedule of evenly spaced firings`)
	}
	s = drainShop()
	workerSettings(s, "Staging").Values = map[string]any{"tick": "30s"}
	refused("a tick of seconds", s, stacktest.AcmeShop(), "tick")
	s = shop()
	workerSettings(s, "Staging").Values = map[string]any{"tick": "5m"}
	refused("a tick on a pool's worker", s, stacktest.AcmeShop(), `worker shop-orders-fulfil-orders sets tick, how often a worker in drain mode is run; it runs as a worker pool`)
	refused("a Rust worker", drainShop(), rustOrdersShop(), "worker shop-orders-fulfil-orders runs in drain mode and is written in Rust, whose worker entrypoint does not exit when its queue is empty yet")
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == "shop-orders" {
			fulfil := stacktest.FulfilOrders
			fulfil.Grace = "10s"
			services[i].Workers = []ir.Worker{fulfil}
		}
	}
	refused("a grace of ten seconds", drainShop(), services, "Cloud Run stops a job's task 10s after SIGTERM; give its @worker a grace under 10s")

	// A TypeScript worker drains as a Go one does.
	services = stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == "shop-orders" {
			services[i].Language = registry.APILanguageTypeScript
		}
	}
	if _, err := stack.Resolve(reg, stack.Input{Stack: drainShop(), Services: services, Environment: "Staging"}); err != nil {
		t.Errorf("a TypeScript worker in drain mode: %v", err)
	}
}
