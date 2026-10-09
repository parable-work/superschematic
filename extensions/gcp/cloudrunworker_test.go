package gcp_test

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// fulfilOrders is shop-orders' worker's deployable (D53).
const fulfilOrders = stacktest.FulfilOrdersWorker

// workerSettings returns the settings element of shop-orders' worker in
// env of s, adding one when env has none.
func workerSettings(s *ir.Stack, env string) *ir.DeployableSettings {
	e := s.Environment(env)
	for _, set := range e.Settings {
		if set.Of.Worker == "FulfilOrders" {
			return set
		}
	}
	set := &ir.DeployableSettings{Of: stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")}
	e.Settings = append(e.Settings, set)
	return set
}

// TestWorkerPlatform checks the Cloud Run worker pool a worker lowers to
// (D53): manual scaling to the worker's instances, its concurrency in
// WORKER_CONCURRENCY, no port and no probe, the worker's own account and
// its API's Cloud SQL volume and egress; and the connectors from the
// worker platform, which name the worker's account and give it a
// database user the migration job grants privileges to.
func TestWorkerPlatform(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.AcmeShop(), "Staging")
	d := env.Deployable(fulfilOrders)
	if d == nil || d.Kind != ir.DeployableWorker || d.Platform != gcp.CloudRunWorker {
		t.Fatalf("the worker is %+v", d)
	}
	if d.Worker.Instances != 1 || d.Worker.Concurrency != 4 || d.Worker.Queue != "OrderPlaced" {
		t.Errorf("the worker runs %+v", d.Worker)
	}
	if got := strings.Join(ownedBy(env, fulfilOrders), ", "); got != "network, network.nat, network.router, network.subnet, secret.PaymentsSecrets.STRIPE_KEY, "+
		fulfilOrders+".account, "+fulfilOrders+".reads.PaymentsSecrets.STRIPE_KEY, "+fulfilOrders+".trace-agent, "+fulfilOrders+".worker-pool" {
		t.Errorf("the worker lowers to %s", got)
	}
	for _, e := range env.Edges {
		if e.From == fulfilOrders {
			want := gcp.WorkerHTTPConnector
			if e.Kind == ir.EdgeSQL {
				want = gcp.WorkerSQLConnector
			}
			if e.Connector != want {
				t.Errorf("edge %s is connected by %s, want %s", e.ID, e.Connector, want)
			}
		}
	}

	pool := node(t, env, fulfilOrders+".worker-pool")
	if pool.Type != gcp.TypeWorkerPool || pool.Properties["name"] != fulfilOrders || pool.Properties["deletionProtection"] != false {
		t.Errorf("the worker pool node: %s %v", pool.Type, pool.Properties)
	}
	wantJSON(t, "the pool's scaling", pool.Properties["scaling"], `{"manualInstanceCount":1,"scalingMode":"MANUAL"}`)
	template := pool.Properties["template"].(map[string]any)
	wantJSON(t, "the pool's template", map[string]any{"serviceAccount": template["serviceAccount"], "volumes": template["volumes"], "egress": template["vpcAccess"].(map[string]any)["egress"]},
		`{"egress":"ALL_TRAFFIC","serviceAccount":{"$output":{"resource":"shop-orders-fulfil-orders.account","name":"email"}},`+
			`"volumes":[{"cloudSqlInstance":{"instances":[{"$output":{"resource":"shop-db.instance","name":"connectionName"}}]},"name":"cloudsql"}]}`)
	container := template["containers"].([]any)[0].(map[string]any)
	if container["image"] != "us-east1-docker.pkg.dev/acme-staging/shop/shop-orders-fulfil-orders" {
		t.Errorf("the worker runs %v, want its repository path, which the deploy pins", container["image"])
	}
	for _, key := range []string{"ports", "startupProbe", "livenessProbe"} {
		if _, ok := container[key]; ok {
			t.Errorf("the worker's container has %s", key)
		}
	}
	envs := container["envs"].([]any)
	wantJSON(t, "the worker's last variable", envs[len(envs)-1], `{"name":"WORKER_CONCURRENCY","value":"4"}`)
	wantJSON(t, "the worker's account", node(t, env, fulfilOrders+".account").Properties,
		`{"accountId":"shop-orders-fulfil-orders","displayName":"Shop Staging worker shop-orders-fulfil-orders","project":"acme-staging"}`)
	wantJSON(t, "the worker's database user", node(t, env, fulfilOrders+".database-user.shop-db").Properties["name"], `"shop-orders-fulfil-orders@acme-staging.iam"`)
}

// TestWorkerSettings: instances and concurrency come from the
// environment's settings, cpu and memory are the platform's, a worker the
// environment turns off keeps its pool at zero instances, and a preview
// member's pool and account take its parameter.
func TestWorkerSettings(t *testing.T) {
	reg := assemble(t)
	s := shop()
	three, eight := 3, 8
	set := workerSettings(s, "Production")
	set.Instances, set.Concurrency = &three, &eight
	set.Values = map[string]any{"cpu": "2", "memory": "1Gi"}
	env := resolve(t, reg, s, stacktest.AcmeShop(), "Production")
	pool := node(t, env, fulfilOrders+".worker-pool").Properties
	wantJSON(t, "Production's scaling", pool["scaling"], `{"manualInstanceCount":3,"scalingMode":"MANUAL"}`)
	container := pool["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	wantJSON(t, "Production's limits", container["resources"], `{"limits":{"cpu":"2","memory":"1Gi"}}`)
	envs := container["envs"].([]any)
	wantJSON(t, "Production's concurrency", envs[len(envs)-1], `{"name":"WORKER_CONCURRENCY","value":"8"}`)

	s = shop()
	off := false
	workerSettings(s, "Production").Enabled = &off
	env = resolve(t, reg, s, stacktest.AcmeShop(), "Production")
	wantJSON(t, "an off worker's scaling", node(t, env, fulfilOrders+".worker-pool").Properties["scaling"], `{"manualInstanceCount":0,"scalingMode":"MANUAL"}`)

	env = resolve(t, reg, shop(), stacktest.AcmeShop(), "Preview")
	pool = node(t, env, fulfilOrders+".worker-pool").Properties
	wantJSON(t, "the member's pool", map[string]any{"name": pool["name"], "scaling": pool["scaling"]},
		`{"name":{"$concat":["shop-orders-fulfil-orders-pr",{"$parameter":"pr"}]},"scaling":{"manualInstanceCount":1,"scalingMode":"MANUAL"}}`)

	s = shop()
	workerSettings(s, "Staging").Values = map[string]any{"minInstances": float64(1)}
	if _, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"}); err == nil || !strings.Contains(err.Error(), "minInstances") {
		t.Errorf("a server's setting on a worker: err = %v", err)
	}
}

// TestWorkerRefusals: Cloud Run stops a worker pool's instance ten seconds
// after SIGTERM, so a grace of ten seconds or more is refused, and a
// worker's account id fits 30 characters.
func TestWorkerRefusals(t *testing.T) {
	reg := assemble(t)
	withWorker := func(w ir.Worker) []stack.Service {
		services := stacktest.AcmeShop()
		for i := range services {
			if services[i].Name == "shop-orders" {
				services[i].Workers = []ir.Worker{w}
			}
		}
		return services
	}
	refused := func(t *testing.T, services []stack.Service, want string) {
		t.Helper()
		_, err := stack.Resolve(reg, stack.Input{Stack: shop(), Services: services, Environment: "Staging"})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to say %q", err, want)
		}
	}
	fulfil := stacktest.FulfilOrders
	fulfil.Grace = "10s"
	refused(t, withWorker(fulfil), "Cloud Run stops a worker pool's instance 10s after SIGTERM; give its @worker a grace under 10s")
	fulfil.Grace = "9s"
	if _, err := stack.Resolve(reg, stack.Input{Stack: shop(), Services: withWorker(fulfil), Environment: "Staging"}); err != nil {
		t.Errorf("a grace of 9s: %v", err)
	}
	long := stacktest.FulfilOrders
	long.Name = "ReconcileWarehouseStock"
	refused(t, withWorker(long), "give the worker's class or its API a shorter name")
}
