package stack_test

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The worker deployable kind (D53, docs/stack-model.md, section 8.8):
// shop-orders declares FulfilOrders, which handles shop-db's queue
// OrderPlaced. It is a deployable of its own in every environment, named
// after its API and its class, with its API's edges, and the instances and
// concurrency each environment's settings give it.

// TestWorkerDeployable: in Staging the worker is placed on the target's
// worker platform, serves and calls what its API does, takes its API's sql
// and http edges through the worker platform's connectors, claims shop-db's
// queue, and handles the eight messages at a time Staging sets, on one
// instance.
func TestWorkerDeployable(t *testing.T) {
	s, services := workerShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	worker := env.Deployable(stacktest.FulfilOrdersWorker)
	if worker == nil {
		t.Fatalf("no worker %s among %v", stacktest.FulfilOrdersWorker, names(env))
	}
	if worker.Kind != ir.DeployableWorker || worker.Platform != stacktest.WorkerPlatform || worker.Language != registry.APILanguageGo || worker.Declared || worker.Exposed {
		t.Errorf("worker = %s %s on %s in %s, declared %t, exposed %t", worker.Kind, worker.Name, worker.Platform, worker.Language, worker.Declared, worker.Exposed)
	}
	if got := refNames(worker.Services); got != "shop-orders" {
		t.Errorf("services = %s, want its API, shop-orders", got)
	}
	if got := refNames(worker.Calls); got != "shop-api" {
		t.Errorf("calls = %s, want its API's, shop-api", got)
	}
	want := ir.ResolvedWorker{API: "shop-orders", Name: "FulfilOrders", Queue: "OrderPlaced", Database: "shop-db", Instances: 1, Concurrency: 8, GraceSeconds: 8}
	if worker.Worker == nil || !reflect.DeepEqual(*worker.Worker, want) {
		t.Errorf("run = %+v, want %+v", worker.Worker, want)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.From == worker.Name {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "http:shop-orders-fulfil-orders->shop-api by fake.worker-run into SHOP_API_SERVICE; sql:shop-orders-fulfil-orders->shop-db by fake.worker-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s\nwant    %s", got, want)
	}
	if worker.ResourceName != stacktest.FulfilOrdersWorker || worker.Address != nil {
		t.Errorf("name %v and address %v, want %s and none: nothing reaches a worker", worker.ResourceName, worker.Address, stacktest.FulfilOrdersWorker)
	}
	if got := bindings(worker); got != "FULFILLMENT_REGION=literal:us MAX_LINE_ITEMS=literal:50 SHOP_API_SERVICE=derived:http:shop-orders-fulfil-orders->shop-api SHOP_DB_DATABASE=derived:sql:shop-orders-fulfil-orders->shop-db STRIPE_KEY=secret:PaymentsSecrets.STRIPE_KEY" {
		t.Errorf("bindings = %s, want its API's config, with Orders's env", got)
	}
}

// TestWorkerInstances: Production runs the three instances its settings
// give, with the decorator's concurrency and the platform's settings;
// Preview, a parameterized environment that extends Staging, runs one with
// Staging's concurrency; and a setting turns a worker off, which runs none.
func TestWorkerInstances(t *testing.T) {
	reg := assemble(t)
	run := func(s *ir.Stack, environment string) *ir.ResolvedDeployable {
		t.Helper()
		_, services := workerShop()
		return mustResolve(t, reg, s, services, environment).Deployable(stacktest.FulfilOrdersWorker)
	}
	s, _ := workerShop()
	production := run(s, "Production")
	if production.Worker.Instances != 3 || production.Worker.Concurrency != 4 || production.Settings["memory"] != "1Gi" {
		t.Errorf("Production runs %d instances, %d at a time, with %v; want 3, the decorator's 4 and memory 1Gi", production.Worker.Instances, production.Worker.Concurrency, production.Settings)
	}
	if preview := run(s, "Preview"); preview.Worker.Instances != 1 || preview.Worker.Concurrency != 8 {
		t.Errorf("Preview runs %d instances, %d at a time; want one, with Staging's 8", preview.Worker.Instances, preview.Worker.Concurrency)
	}

	// A range: Production's three instances the fewest and ten the most
	// (D53, amended); turned off, it runs none and no range.
	s, _ = workerShop()
	settingsFor(s.Environment("Production"), stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")).MaxInstances = intp(10)
	if production := run(s, "Production"); production.Worker.Instances != 3 || production.Worker.MaxInstances != 10 {
		t.Errorf("Production runs %d to %d instances, want 3 to 10", production.Worker.Instances, production.Worker.MaxInstances)
	}
	if staging := run(s, "Staging"); staging.Worker.MaxInstances != 0 {
		t.Errorf("Staging runs up to %d instances, want a fixed count", staging.Worker.MaxInstances)
	}

	off := false
	s, _ = workerShop()
	settingsFor(s.Environment("Production"), stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")).Enabled = &off
	settingsFor(s.Environment("Production"), stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")).MaxInstances = intp(10)
	if production := run(s, "Production"); production.Worker.Instances != 0 || production.Worker.MaxInstances != 0 {
		t.Errorf("Production turned off runs %d to %d instances, want none", production.Worker.Instances, production.Worker.MaxInstances)
	}
}

// TestWorkerDrainMode (D145): a worker its settings put in drain mode runs
// as a job that empties its queue and exits. Its run says so, with the
// idle time the settings give or thirty seconds, and its instances are how
// many of it may run at once. Each deployable that runs an API's code and
// connects to the queue's database, but the worker itself, gets the worker
// among its wakes, with the queue's class, the worker's platform and the
// name its platform gave it. A member of Preview, which extends Staging,
// drains too, under its own name; Production, whose settings say nothing,
// keeps its pool and wakes nothing; and a drain worker turned off is not
// woken.
func TestWorkerDrainMode(t *testing.T) {
	reg := assemble(t)
	fulfil := stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")
	wakes := func(env *ir.ResolvedEnvironment) string {
		var out []string
		for _, d := range env.Deployables {
			for _, w := range d.Wakes {
				out = append(out, fmt.Sprintf("%s %s wakes %s on %s (%v) for %s of %s", d.Kind, d.Name, w.Worker, w.Platform, w.ResourceName, w.Queue, w.Database))
			}
		}
		return strings.Join(out, "; ")
	}
	s, services := workerShop()
	settingsFor(s.Environment("Staging"), fulfil).Mode = ir.WorkerModeDrain
	env := mustResolve(t, reg, s, services, "Staging")
	run := env.Deployable(stacktest.FulfilOrdersWorker).Worker
	if !run.Drains() || run.Mode != ir.WorkerModeDrain || run.IdleSeconds != ir.DefaultWorkerIdleSeconds || run.Instances != 1 || run.MaxInstances != 0 || run.Concurrency != 8 {
		t.Errorf("Staging's worker runs %+v, want drain mode, idle after 30s, one at a time", run)
	}
	const staging = "server Orders wakes shop-orders-fulfil-orders on fake.worker (shop-orders-fulfil-orders) for OrderPlaced of shop-db; " +
		"server shop-api wakes shop-orders-fulfil-orders on fake.worker (shop-orders-fulfil-orders) for OrderPlaced of shop-db"
	if got := wakes(env); got != staging {
		t.Errorf("Staging's wakes = %s\nwant            %s", got, staging)
	}
	if got := env.Deployable(stacktest.FulfilOrdersWorker).Wakes; len(got) != 0 {
		t.Errorf("the worker wakes itself: %+v", got)
	}

	// The settings' idle time and instances; a job of the API is woken too.
	s, services = stacktest.Shop(), stacktest.AcmeShop()
	set := settingsFor(s.Environment("Staging"), fulfil)
	set.Mode, set.Idle, set.Instances = ir.WorkerModeDrain, "2m", intp(3)
	env = mustResolve(t, reg, s, services, "Staging")
	if run := env.Deployable(stacktest.FulfilOrdersWorker).Worker; run.IdleSeconds != 120 || run.Instances != 3 {
		t.Errorf("the worker idles %ds and runs %d at once, want 120 and 3", run.IdleSeconds, run.Instances)
	}
	if got := wakes(env); !strings.Contains(got, "job "+stacktest.ShipOrdersJob+" wakes shop-orders-fulfil-orders") {
		t.Errorf("the API's job is not given the wake: %s", got)
	}

	// Preview extends Staging; Production says nothing of a mode.
	s, services = workerShop()
	settingsFor(s.Environment("Staging"), fulfil).Mode = ir.WorkerModeDrain
	preview := mustResolve(t, reg, s, services, "Preview")
	if !preview.Deployable(stacktest.FulfilOrdersWorker).Worker.Drains() || !strings.Contains(wakes(preview), "server shop-api wakes shop-orders-fulfil-orders") {
		t.Errorf("Preview's worker runs %+v and its wakes are %s", preview.Deployable(stacktest.FulfilOrdersWorker).Worker, wakes(preview))
	}
	production := mustResolve(t, reg, s, services, "Production")
	if run := production.Deployable(stacktest.FulfilOrdersWorker).Worker; run.Drains() || run.Mode != "" || run.IdleSeconds != 0 || wakes(production) != "" {
		t.Errorf("Production's worker runs %+v and its wakes are %q, want a pool and none", run, wakes(production))
	}

	// pool says what the default is, and a later environment may say it
	// over an earlier one's drain.
	s, services = workerShop()
	settingsFor(s.Environment("Staging"), fulfil).Mode = ir.WorkerModePool
	if run := mustResolve(t, reg, s, services, "Staging").Deployable(stacktest.FulfilOrdersWorker).Worker; run.Drains() || run.Mode != "" {
		t.Errorf("mode pool runs %+v", run)
	}

	// Turned off, it is not woken.
	off := false
	s, services = workerShop()
	set = settingsFor(s.Environment("Staging"), fulfil)
	set.Mode, set.Enabled = ir.WorkerModeDrain, &off
	env = mustResolve(t, reg, s, services, "Staging")
	if run := env.Deployable(stacktest.FulfilOrdersWorker).Worker; !run.Drains() || run.Instances != 0 || wakes(env) != "" {
		t.Errorf("a drain worker turned off runs %+v and its wakes are %q", run, wakes(env))
	}
}

// TestWorkerDrainModeRefusals: what resolution refuses of a worker's mode
// and idle time.
func TestWorkerDrainModeRefusals(t *testing.T) {
	reg := assemble(t)
	fulfil := stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")
	for name, c := range map[string]struct {
		set  func(*ir.DeployableSettings)
		of   ir.DeployableRef
		want string
	}{
		"a range of instances beside drain": {
			set:  func(s *ir.DeployableSettings) { s.Mode, s.MaxInstances = ir.WorkerModeDrain, intp(4) },
			of:   fulfil,
			want: "worker shop-orders-fulfil-orders runs in drain mode (Staging set it) and sets maxInstances (Staging set it); a worker in drain mode runs when its queue has messages and exits when it has none, and instances is how many of it may run at once, so leave maxInstances out (D145)",
		},
		"an idle time on a pool": {
			set:  func(s *ir.DeployableSettings) { s.Idle = "1m" },
			of:   fulfil,
			want: `worker shop-orders-fulfil-orders sets idle (Staging set it) and does not run in drain mode; only a worker with mode "drain" exits when it is idle (D145)`,
		},
		"an unknown mode": {
			set:  func(s *ir.DeployableSettings) { s.Mode = "burst" },
			of:   fulfil,
			want: `mode is "burst"; it is "pool", a worker whose instances keep running, or "drain", one that runs as a job that empties its queue and exits`,
		},
		"an idle time that is no duration": {
			set:  func(s *ir.DeployableSettings) { s.Mode, s.Idle = ir.WorkerModeDrain, "soon" },
			of:   fulfil,
			want: `idle: "soon" is no duration`,
		},
		"a mode on a server": {
			set:  func(s *ir.DeployableSettings) { s.Mode = ir.WorkerModeDrain },
			of:   stacktest.Of(stacktest.ShopAPI),
			want: "sets mode on server shop-api; only a worker takes instances, maxInstances, concurrency, mode and idle",
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, services := workerShop()
			c.set(settingsFor(s.Environment("Staging"), c.of))
			_, errs := resolve(t, reg, s, services, "Staging")
			mustFail(t, errs, stack.CodeInvalidSettings, c.want)
		})
	}
}

// TestABehaviorWorkerDrains (D145): a behavior worker (D103) runs in drain
// mode as a `@worker` does, and each deployable that may write to its
// database is given its wake for each of its queues, the deadline, change
// and event queues the behaviors' core wakes it for; the worker wakes
// none of its own.
func TestABehaviorWorkerDrains(t *testing.T) {
	s, services := behaviorShop(true)
	timers := stacktest.WorkerOf(stacktest.ShopOrders, "OrderTimers")
	settingsFor(s.Environment("Staging"), timers).Mode = ir.WorkerModeDrain
	env := mustResolve(t, assemble(t), s, services, "Staging")
	worker := env.Deployable("shop-orders-order-timers")
	if run := worker.Worker; !run.Drains() || run.IdleSeconds != ir.DefaultWorkerIdleSeconds || run.Instances != 1 || !slices.Equal(run.Queues, []string{"OrderDeadline", "OrderChange", "OrderEvent"}) {
		t.Errorf("the behavior worker runs %+v, want drain mode over its three queues", run)
	}
	var woken []string
	for _, w := range env.Deployable("shop-api").Wakes {
		if w.Worker == "shop-orders-order-timers" {
			woken = append(woken, w.Queue+" of "+w.Database)
		}
	}
	if want := []string{"OrderDeadline of shop-db", "OrderChange of shop-db", "OrderEvent of shop-db"}; !slices.Equal(woken, want) {
		t.Errorf("shop-api wakes the behavior worker for %v, want %v", woken, want)
	}
	for _, w := range worker.Wakes {
		if w.Worker == "shop-orders-order-timers" {
			t.Errorf("the behavior worker wakes itself: %+v", w)
		}
	}
}

// behaviorShop is the worker shop whose database keeps Order's deadline,
// change and event queues (D103, D104): with worker, a behavior worker of
// shop-orders runs them.
func behaviorShop(worker bool) (*ir.Stack, []stack.Service) {
	s, services := workerShop()
	db := service(services, "shop-db")
	db.Queues = append(db.Queues, "OrderChange", "OrderDeadline", "OrderEvent")
	db.BehaviorQueues = map[string]ir.BehaviorQueue{
		"OrderDeadline": {Row: "Order", Kind: ir.BehaviorQueueDeadlines},
		"OrderChange":   {Row: "Order", Kind: ir.BehaviorQueueChanges},
		"OrderEvent":    {Row: "Order", Kind: ir.BehaviorQueueEvents},
	}
	if worker {
		orders := service(services, "shop-orders")
		orders.Workers = append(orders.Workers, ir.Worker{Name: "OrderTimers", Queue: "OrderDeadline", Queues: []string{"OrderDeadline", "OrderChange", "OrderEvent"}, BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Order"}, Origin: ir.OriginBehavior})
	}
	return s, services
}

// TestBehaviorQueuesNeedAWorker (D151): an environment that runs a database
// whose table keeps a deadline, change or event queue refuses it while no
// worker it runs claims the queue, naming the database, the table, the
// queue and what it holds; a behavior worker of the table drains them all,
// and turning it off refuses the environment again.
func TestBehaviorQueuesNeedAWorker(t *testing.T) {
	s, services := behaviorShop(false)
	_, errs := resolve(t, assemble(t), s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable, "environment Staging runs database shop-db, whose table Order keeps its deadline queue in queue OrderDeadline, and no worker the environment runs drains it", "declare @behaviorWorker(Order) on an API over shop-db")
	mustFail(t, errs, stack.CodeUnrealizable, "whose table Order keeps its change queue in queue OrderChange")
	mustFail(t, errs, stack.CodeUnrealizable, "whose table Order keeps its event queue in queue OrderEvent")

	s, services = behaviorShop(true)
	env := mustResolve(t, assemble(t), s, services, "Staging")
	worker := env.Deployable("shop-orders-order-timers")
	if worker == nil || !slices.Equal(worker.Worker.Queues, []string{"OrderDeadline", "OrderChange", "OrderEvent"}) || !slices.Equal(worker.Worker.AllQueues(), worker.Worker.Queues) {
		t.Fatalf("the behavior worker resolved %+v, want it to claim Order's three queues", worker)
	}

	settingsFor(s.Environment("Staging"), stacktest.WorkerOf(stacktest.ShopOrders, "OrderTimers")).Enabled = new(bool)
	_, errs = resolve(t, assemble(t), s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable, "queue OrderDeadline, and no worker the environment runs drains it")
}

// TestWorkerRollsOutAfterItsCallees: the worker calls shop-api, so it rolls
// out in the wave after it, with Orders, after the migrations of its
// database.
func TestWorkerRollsOutAfterItsCallees(t *testing.T) {
	s, services := workerShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	migrated := false
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepMigrate && step.Migration == ir.MigrationExpand {
			migrated = true
		}
		if step.Step == ir.StepRollout && slices.Contains(step.Deployables, stacktest.FulfilOrdersWorker) {
			if !migrated || step.Wave != 2 || !slices.Contains(step.Resources, stacktest.FulfilOrdersWorker+".pool") {
				t.Errorf("the worker rolls out in %+v, want wave 2 with its pool, after the migration", step)
			}
			return
		}
	}
	t.Fatalf("no rollout step rolls the worker out: %+v", env.DeployOrder)
}

// TestWorkersOfTheStack: stack.Workers gives each worker with its API, its
// queue, its calls, its language and its grace, whatever the environment.
func TestWorkersOfTheStack(t *testing.T) {
	s, services := workerShop()
	workers, err := stack.Workers(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %v, want FulfilOrders", workers)
	}
	w := workers[0]
	if w.Name != stacktest.FulfilOrdersWorker || w.Language != registry.APILanguageGo || refNames(w.Calls) != "shop-api" || w.Worker == nil ||
		w.Worker.API != "shop-orders" || w.Worker.Name != "FulfilOrders" || w.Worker.Queue != "OrderPlaced" || w.Worker.GraceSeconds != 8 {
		t.Errorf("worker = %+v (%+v)", w, w.Worker)
	}
	jobs, err := stack.Jobs(stack.Input{Stack: s, Services: services})
	if err != nil || len(jobs) != 0 {
		t.Errorf("Jobs = %v, %v, want none", jobs, err)
	}
}

// TestWorkerRefusals: what resolution refuses of workers and their
// settings.
func TestWorkerRefusals(t *testing.T) {
	reg := assemble(t)
	fulfil := stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")
	t.Run("a worker the API does not declare", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrder")).Concurrency = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names worker FulfilOrder of shop-orders, which declares no such @worker class (its workers: FulfilOrders)")
	})
	t.Run("instances on a server", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopAPI)).Instances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets instances on server shop-api; only a worker takes instances, maxInstances, concurrency, mode and idle")
	})
	t.Run("a schedule on a worker", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), fulfil).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule or a time zone on worker shop-orders-fulfil-orders, which runs until it is stopped")
	})
	t.Run("a maximum under the instances", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Production"), fulfil).MaxInstances = intp(2)
		_, errs := resolve(t, reg, s, services, "Production")
		mustFail(t, errs, stack.CodeInvalidSettings, "worker shop-orders-fulfil-orders may run up to 2 instances (maxInstances), under its 3 (instances), the fewest it runs")
	})
	t.Run("a maximum of none", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), fulfil).MaxInstances = intp(0)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "maxInstances is 0; it is one or more")
	})
	t.Run("a maximum on a server", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopAPI)).MaxInstances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets maxInstances on server shop-api; only a worker takes instances, maxInstances, concurrency, mode and idle")
	})
	t.Run("a concurrency of none", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), fulfil).Concurrency = intp(0)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "concurrency is 0; it is one or more")
	})
	t.Run("a queue the API's database does not declare", func(t *testing.T) {
		s, services := workerShop()
		service(services, "shop-orders").Workers[0].Queue = "OrderShipped"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "worker FulfilOrders of shop-orders handles queue OrderShipped, which is no @queue class of shop-db, the database shop-orders connects to (its queues: OrderPlaced)")
	})
	t.Run("an API with no database", func(t *testing.T) {
		s, services := workerShop()
		orders := service(services, "shop-orders")
		orders.AuthDB, orders.Dependencies = nil, nil
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "worker FulfilOrders of shop-orders handles queue OrderPlaced, and shop-orders connects to no database")
	})
	t.Run("a Go worker's queue on SQLite", func(t *testing.T) {
		// A Go worker claims on its database's SQLite file (#717): its
		// dialect refuses it nothing. (The fake target has no connector to
		// its SQLite platform; TestAJobAndWorkerRunOnTheirDatabasesSQLiteFile
		// resolves one on the local target.)
		s, services := workerShop()
		service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite}
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopDB)).Platform = stacktest.LitePlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		for _, err := range errs.Of(stack.CodeUnrealizable) {
			if strings.Contains(err.Message, "shop-orders-fulfil-orders") {
				t.Errorf("the Go worker on SQLite is refused: %s", err.Message)
			}
		}
	})
	t.Run("a queue on a platform where no worker claims", func(t *testing.T) {
		s, services := workerShop()
		service(services, "shop-orders").Language = registry.APILanguageTypeScript
		service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite}
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopDB)).Platform = noClaimsPlatform
		_, errs := resolve(t, assemble(t, noClaimsTarget{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "worker shop-orders-fulfil-orders claims the messages of queue OrderPlaced of shop-db, which environment Staging places on shop-db, a sqlite database on platform noclaims.d1, where no worker claims: it is reached through a binding alone (D53, amended)")
	})
	t.Run("a worker named like another deployable", func(t *testing.T) {
		s, services := workerShop()
		s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: stacktest.FulfilOrdersWorker, Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{stacktest.ShopDB}})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "worker FulfilOrders of shop-orders would be named shop-orders-fulfil-orders, like the database shop-orders-fulfil-orders")
	})
	t.Run("an exposed worker", func(t *testing.T) {
		s, services := workerShop()
		s.Expose = append(s.Expose, fulfil)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeExposeNotServer, "exposes shop-orders-fulfil-orders, a worker; only a server, a site or an app is exposed")
	})
	t.Run("a target with no worker platform", func(t *testing.T) {
		s, services := workerShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noDNS{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "worker shop-orders-fulfil-orders: target no-dns has no platform for a worker; place it with a settings platform")
	})
}

func intp(n int) *int { return &n }

// noClaimsPlatform is a database platform where no worker claims, as
// cloudflare.d1 is (D53, amended).
const noClaimsPlatform = "noclaims.d1"

// noClaimsTarget registers noClaimsPlatform.
type noClaimsTarget struct{}

func (noClaimsTarget) Name() string { return "noclaims" }

func (noClaimsTarget) Register(r *registry.Registry) error {
	return r.RegisterPlatform(registry.PlatformSpec{
		Name: noClaimsPlatform, Provider: "noclaims", Kind: ir.DeployableDatabase,
		Dialects:      []string{registry.SQLDialectSQLite},
		NoQueueClaims: "it is reached through a binding alone",
		NameOf:        func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf:     func(registry.PlatformContext) any { return nil },
		Lower:         func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	})
}
