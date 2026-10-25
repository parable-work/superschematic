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

// The deriver deployable kind (D74): shop-orders, a TypeScript API here,
// declares TriageTickets, which fills the Derived fields of shop-db's
// Ticket. It is a deployable of its own in every environment, named after
// its API and its class, with its API's edges, one instance, and the
// concurrency each environment's settings give it.

// deriverShop is shop with shop-db an engine database whose Ticket and
// Article compose Derived, and shop-orders a TypeScript API that declares
// the deriver TriageTickets of Ticket, four answers at a time, and
// FillAll, which names no schema.
func deriverShop() (*ir.Stack, []stack.Service) {
	s, services := shop()
	db := service(services, "shop-db")
	db.Engine, db.Derived = true, []string{"Article", "Ticket"}
	orders := service(services, "shop-orders")
	orders.Language = registry.APILanguageTypeScript
	orders.Derivers = []ir.Deriver{{Name: "TriageTickets", Schemas: []string{"Ticket"}, Concurrency: 4}, {Name: "FillAll", BatchSize: 20}}
	return s, services
}

const (
	triageDeriver = "shop-orders-triage-tickets"
	fillDeriver   = "shop-orders-fill-all"
)

var triage = stacktest.DeriverOf(stacktest.ShopOrders, "TriageTickets")

// TestDeriverDeployable: in Staging the deriver is placed on the target's
// deriver platform in its API's TypeScript, serves and calls what its API
// does, takes its API's sql and http edges through the deriver platform's
// connectors and its API's config, fills its schemas, and runs one
// instance at the concurrency its decorator gives, or the defaults.
func TestDeriverDeployable(t *testing.T) {
	s, services := deriverShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	d := env.Deployable(triageDeriver)
	if d == nil {
		t.Fatalf("no deriver %s among %v", triageDeriver, names(env))
	}
	if d.Kind != ir.DeployableDeriver || d.Platform != stacktest.DeriverPlatform || d.Language != registry.APILanguageTypeScript || d.Declared || d.Exposed || d.Artifact != ir.ArtifactImage {
		t.Errorf("deriver = %s %s on %s in %s (%s), declared %t, exposed %t", d.Kind, d.Name, d.Platform, d.Language, d.Artifact, d.Declared, d.Exposed)
	}
	if got := refNames(d.Services); got != "shop-orders" {
		t.Errorf("services = %s, want its API, shop-orders", got)
	}
	if got := refNames(d.Calls); got != "shop-api" {
		t.Errorf("calls = %s, want its API's, shop-api", got)
	}
	want := ir.ResolvedDeriver{API: "shop-orders", Name: "TriageTickets", Database: "shop-db", Schemas: []string{"Ticket"}, Instances: 1, Concurrency: 4, BatchSize: ir.DefaultDeriverBatchSize}
	if d.Deriver == nil || fmt.Sprint(*d.Deriver) != fmt.Sprint(want) {
		t.Errorf("run = %+v, want %+v", d.Deriver, want)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.From == d.Name {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "http:shop-orders-triage-tickets->shop-api by fake.deriver-run into SHOP_API_SERVICE; sql:shop-orders-triage-tickets->shop-db by fake.deriver-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s\nwant    %s", got, want)
	}
	if got := bindings(d); got != "FULFILLMENT_REGION=literal:us MAX_LINE_ITEMS=literal:50 SHOP_API_SERVICE=derived:http:shop-orders-triage-tickets->shop-api SHOP_DB_DATABASE=derived:sql:shop-orders-triage-tickets->shop-db STRIPE_KEY=secret:PaymentsSecrets.STRIPE_KEY" {
		t.Errorf("bindings = %s, want its API's config, with Orders's env", got)
	}
	if pool := env.Resources.Resource(triageDeriver + ".pool"); pool == nil || pool.Properties["instances"] != float64(1) || pool.Properties["concurrency"] != float64(4) {
		t.Errorf("pool = %+v, want one instance, four at a time", pool)
	}
	all := env.Deployable(fillDeriver)
	if want := (ir.ResolvedDeriver{API: "shop-orders", Name: "FillAll", Database: "shop-db", Schemas: []string{"Article", "Ticket"}, Instances: 1, Concurrency: ir.DefaultDeriverConcurrency, BatchSize: 20}); all == nil || fmt.Sprint(*all.Deriver) != fmt.Sprint(want) {
		t.Errorf("FillAll = %+v, want every type that composes Derived: %+v", all, want)
	}
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout && slices.Contains(step.Deployables, triageDeriver) && step.Wave != 2 {
			t.Errorf("the deriver rolls out in %+v, want wave 2, after shop-api, which it calls", step)
		}
	}
}

// TestDeriverSettings: a setting changes the concurrency, turns the
// deriver off, which runs none, or runs none with instances 0; Preview
// inherits Staging's.
func TestDeriverSettings(t *testing.T) {
	reg := assemble(t)
	s, services := deriverShop()
	settingsFor(s.Environment("Staging"), triage).Concurrency = intp(2)
	settingsFor(s.Environment("Production"), triage).Instances = intp(0)
	off := false
	settingsFor(s.Environment("Production"), stacktest.DeriverOf(stacktest.ShopOrders, "FillAll")).Enabled = &off
	if d := mustResolve(t, reg, s, services, "Preview").Deployable(triageDeriver); d.Deriver.Concurrency != 2 || d.Deriver.Instances != 1 {
		t.Errorf("Preview runs %d instances, %d at a time; want one, with Staging's 2", d.Deriver.Instances, d.Deriver.Concurrency)
	}
	production := mustResolve(t, reg, s, services, "Production")
	if d := production.Deployable(triageDeriver); d.Deriver.Instances != 0 || d.Deriver.Concurrency != 4 {
		t.Errorf("Production runs %d instances, %d at a time; want none, at the decorator's 4", d.Deriver.Instances, d.Deriver.Concurrency)
	}
	if d := production.Deployable(fillDeriver); d.Deriver.Instances != 0 {
		t.Errorf("FillAll turned off runs %d instances, want none", d.Deriver.Instances)
	}
}

// TestDeriversOfTheStack: stack.Derivers gives each deriver with its API,
// its database, its schemas, its calls and its language, whatever the
// environment.
func TestDeriversOfTheStack(t *testing.T) {
	s, services := deriverShop()
	derivers, err := stack.Derivers(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range derivers {
		got = append(got, fmt.Sprintf("%s %s %s %s %v %d/%d", d.Name, d.Language, refNames(d.Calls), d.Deriver.Database, d.Deriver.Schemas, d.Deriver.Concurrency, d.Deriver.BatchSize))
	}
	if want := []string{
		"shop-orders-fill-all TYPESCRIPT shop-api shop-db [Article Ticket] 4/20",
		"shop-orders-triage-tickets TYPESCRIPT shop-api shop-db [Ticket] 4/50",
	}; !slices.Equal(got, want) {
		t.Errorf("derivers =\n%q\nwant\n%q", got, want)
	}
}

// TestDeriverRefusals: what resolution refuses of derivers and their
// settings.
func TestDeriverRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("a deriver the API does not declare", func(t *testing.T) {
		s, services := deriverShop()
		settingsFor(s.Environment("Staging"), stacktest.DeriverOf(stacktest.ShopOrders, "Triage")).Concurrency = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names deriver Triage of shop-orders, which declares no such @deriver class (its derivers: TriageTickets, FillAll)")
	})
	t.Run("two instances", func(t *testing.T) {
		s, services := deriverShop()
		settingsFor(s.Environment("Staging"), triage).Instances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "instances is 2; a deriver runs zero instances or one")
	})
	t.Run("a maximum on a deriver", func(t *testing.T) {
		s, services := deriverShop()
		settingsFor(s.Environment("Staging"), triage).MaxInstances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets maxInstances on deriver shop-orders-triage-tickets, which runs one instance or none")
	})
	t.Run("a schedule on a deriver", func(t *testing.T) {
		s, services := deriverShop()
		settingsFor(s.Environment("Staging"), triage).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule or a time zone on deriver shop-orders-triage-tickets, which runs until it is stopped")
	})
	t.Run("a schema that does not compose Derived", func(t *testing.T) {
		s, services := deriverShop()
		service(services, "shop-orders").Derivers[0].Schemas = []string{"Order"}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "deriver TriageTickets of shop-orders fills the derived fields of Order, which is no engine type of shop-db that composes Derived (its types that do: Article, Ticket)")
	})
	t.Run("a database where nothing composes Derived", func(t *testing.T) {
		s, services := deriverShop()
		service(services, "shop-db").Derived = nil
		service(services, "shop-orders").Derivers = []ir.Deriver{{Name: "FillAll"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "deriver FillAll of shop-orders fills the derived fields of shop-db, and no type of shop-db composes Derived")
	})
	t.Run("an API on no engine database", func(t *testing.T) {
		s, services := deriverShop()
		service(services, "shop-db").Engine = false
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "deriver TriageTickets of shop-orders fills Derived fields, and shop-orders connects to no engine database")
	})
	t.Run("a Go API", func(t *testing.T) {
		s, services := deriverShop()
		service(services, "shop-orders").Language = ""
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "deriver TriageTickets of shop-orders: shop-orders is a Go API, and a deriver runs on a TypeScript or Rust API; Go follows")
		if _, err := stack.Derivers(stack.Input{Stack: s, Services: services}); err == nil || !strings.Contains(err.Error(), "a deriver runs on a TypeScript or Rust API; Go follows") {
			t.Errorf("Derivers = %v, want the Go API's deriver refused", err)
		}
	})
	t.Run("an exposed deriver", func(t *testing.T) {
		s, services := deriverShop()
		s.Expose = append(s.Expose, triage)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeExposeNotServer, "exposes shop-orders-triage-tickets, a deriver; only a server, a site or an app is exposed")
	})
	t.Run("a deriver named like a worker", func(t *testing.T) {
		s, services := deriverShop()
		service(services, "shop-orders").Workers = []ir.Worker{{Name: "TriageTickets", Queue: "OrderPlaced"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "deriver TriageTickets of shop-orders would be named shop-orders-triage-tickets, like the worker shop-orders-triage-tickets")
	})
}
