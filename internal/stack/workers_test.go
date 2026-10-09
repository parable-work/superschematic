package stack_test

import (
	"fmt"
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
	if worker.Worker == nil || *worker.Worker != want {
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

	off := false
	s, _ = workerShop()
	settingsFor(s.Environment("Production"), stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders")).Enabled = &off
	if production := run(s, "Production"); production.Worker.Instances != 0 {
		t.Errorf("Production turned off runs %d instances, want none", production.Worker.Instances)
	}
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
		mustFail(t, errs, stack.CodeInvalidSettings, "sets instances or concurrency on server shop-api; only a worker takes them")
	})
	t.Run("a schedule on a worker", func(t *testing.T) {
		s, services := workerShop()
		settingsFor(s.Environment("Staging"), fulfil).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule or a time zone on worker shop-orders-fulfil-orders, which runs until it is stopped")
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
	t.Run("a queue on SQLite", func(t *testing.T) {
		s, services := workerShop()
		service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite}
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopDB)).Platform = stacktest.LitePlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "worker shop-orders-fulfil-orders claims the messages of queue OrderPlaced of shop-db, which environment Staging places on shop-db, a sqlite database; a worker claims on Postgres")
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
		mustFail(t, errs, stack.CodeExposeNotServer, "exposes shop-orders-fulfil-orders, a worker; only a server or a site is exposed")
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
