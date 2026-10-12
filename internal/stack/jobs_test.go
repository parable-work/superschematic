package stack_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The job deployable kind (D52, docs/stack-model.md, section 8.7):
// shop-orders declares ShipOrders, which is a deployable of its own in
// every environment, named after its API and its class, with its API's
// edges, and the schedule each environment's settings give it.

// TestJobDeployable: in Staging the job is placed on the target's job
// platform, serves and calls what its API does, takes its API's sql and
// http edges through the job platform's connectors, and runs the schedule
// and time zone Staging sets, with the decorator's timeout and retries.
func TestJobDeployable(t *testing.T) {
	s, services := jobShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	job := env.Deployable(stacktest.ShipOrdersJob)
	if job == nil {
		t.Fatalf("no job %s among %v", stacktest.ShipOrdersJob, names(env))
	}
	if job.Kind != ir.DeployableJob || job.Platform != stacktest.JobPlatform || job.Language != registry.APILanguageGo || job.Declared || job.Exposed {
		t.Errorf("job = %s %s on %s in %s, declared %t, exposed %t", job.Kind, job.Name, job.Platform, job.Language, job.Declared, job.Exposed)
	}
	if got := refNames(job.Services); got != "shop-orders" {
		t.Errorf("services = %s, want its API, shop-orders", got)
	}
	if got := refNames(job.Calls); got != "shop-api" {
		t.Errorf("calls = %s, want its API's, shop-api", got)
	}
	want := ir.ResolvedJob{API: "shop-orders", Name: "ShipOrders", Schedule: "0 * * * *", TimeZone: "America/New_York", TimeoutSeconds: 300, Retries: 1}
	if job.Job == nil || *job.Job != want {
		t.Errorf("run = %+v, want %+v", job.Job, want)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.From == job.Name {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "http:shop-orders-ship-orders->shop-api by fake.job-run into SHOP_API_SERVICE; sql:shop-orders-ship-orders->shop-db by fake.job-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s\nwant    %s", got, want)
	}
	if job.ResourceName != stacktest.ShipOrdersJob || job.Address != nil {
		t.Errorf("name %v and address %v, want %s and none: nothing reaches a job", job.ResourceName, job.Address, stacktest.ShipOrdersJob)
	}
}

// TestJobSchedules: Production runs the decorator's schedule in UTC with
// the job platform's settings; Preview, a parameterized environment, runs
// none, though it inherits Staging's, until its settings turn it on; and a
// setting turns a schedule off.
func TestJobSchedules(t *testing.T) {
	reg := assemble(t)
	run := func(s *ir.Stack, environment string) *ir.ResolvedDeployable {
		t.Helper()
		_, services := jobShop()
		return mustResolve(t, reg, s, services, environment).Deployable(stacktest.ShipOrdersJob)
	}
	s, _ := jobShop()
	production := run(s, "Production")
	if production.Job.Schedule != "*/15 * * * *" || production.Job.TimeZone != "UTC" {
		t.Errorf("Production runs %q in %s, want the decorator's */15 * * * * in UTC", production.Job.Schedule, production.Job.TimeZone)
	}
	if production.Settings["cpu"] != "2" {
		t.Errorf("Production's settings = %v, want cpu 2", production.Settings)
	}
	if preview := run(s, "Preview"); preview.Job.Schedule != "" || preview.Job.TimeZone != "America/New_York" {
		t.Errorf("Preview runs %q in %s, want no schedule, in Staging's time zone", preview.Job.Schedule, preview.Job.TimeZone)
	}

	on := true
	s, _ = jobShop()
	settingsFor(s.Environment("Preview"), stacktest.JobOf(stacktest.ShopOrders, "ShipOrders")).Enabled = &on
	if preview := run(s, "Preview"); preview.Job.Schedule != "0 * * * *" {
		t.Errorf("Preview turned on runs %q, want Staging's 0 * * * *", preview.Job.Schedule)
	}

	off := false
	s, _ = jobShop()
	settingsFor(s.Environment("Production"), stacktest.JobOf(stacktest.ShopOrders, "ShipOrders")).Enabled = &off
	if production := run(s, "Production"); production.Job.Schedule != "" {
		t.Errorf("Production turned off runs %q, want only runs on demand", production.Job.Schedule)
	}
}

// TestJobConfig: a job's config is its API's. It takes the env its API's
// server is given, here Orders's FULFILLMENT_REGION, under its own, reads
// the secrets its API's config declares, and its derived fields come from
// its own edges. A key its API's server takes for another API it serves
// does not reach the job.
func TestJobConfig(t *testing.T) {
	s, services := jobShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	if got := bindings(env.Deployable(stacktest.ShipOrdersJob)); got != "FULFILLMENT_REGION=literal:us MAX_LINE_ITEMS=literal:50 SHOP_API_SERVICE=derived:http:shop-orders-ship-orders->shop-api SHOP_DB_DATABASE=derived:sql:shop-orders-ship-orders->shop-db STRIPE_KEY=secret:PaymentsSecrets.STRIPE_KEY" {
		t.Errorf("bindings = %s", got)
	}
	if len(env.Secrets) != 1 || strings.Join(env.Secrets[0].Readers, ",") != "Orders,shop-api,"+stacktest.ShipOrdersJob {
		t.Errorf("secrets = %+v, want one secret the job reads with the servers", env.Secrets)
	}

	s, services = jobShop()
	settingsFor(s.Environment("Staging"), stacktest.JobOf(stacktest.ShopOrders, "ShipOrders")).Env = map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "eu"}}
	env = mustResolve(t, assemble(t), s, services, "Staging")
	if got := bindings(env.Deployable(stacktest.ShipOrdersJob)); !strings.Contains(got, "FULFILLMENT_REGION=literal:eu") {
		t.Errorf("bindings = %s, want the job's own FULFILLMENT_REGION over its server's", got)
	}
	if got := bindings(env.Deployable("Orders")); !strings.Contains(got, "FULFILLMENT_REGION=literal:us") {
		t.Errorf("Orders's bindings = %s, want its own FULFILLMENT_REGION", got)
	}

	// Backend serves both APIs, and its env sets shop-api's LOG_LEVEL,
	// which shop-orders' config does not have.
	s, services = jobShop()
	s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders}}}
	s.Expose = []ir.DeployableRef{{Deployable: "Backend"}}
	s.Environments[0].Settings = []*ir.DeployableSettings{{Of: ir.DeployableRef{Deployable: "Backend"}, Env: map[string]ir.EnvValue{
		"FULFILLMENT_REGION": {Value: "us"}, "LOG_LEVEL": {Value: "warn"},
	}}}
	env = mustResolve(t, assemble(t), s, services, "Staging")
	if got := bindings(env.Deployable(stacktest.ShipOrdersJob)); strings.Contains(got, "LOG_LEVEL") || !strings.Contains(got, "FULFILLMENT_REGION=literal:us") {
		t.Errorf("bindings = %s, want Backend's FULFILLMENT_REGION and no LOG_LEVEL", got)
	}
}

// TestJobRollsOutAfterItsCallees: the job calls shop-api, so it rolls out
// in the wave after it, with Orders.
func TestJobRollsOutAfterItsCallees(t *testing.T) {
	s, services := jobShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout && slices.Contains(step.Deployables, stacktest.ShipOrdersJob) {
			if step.Wave != 2 || !slices.Contains(step.Resources, stacktest.ShipOrdersJob+".job") || !slices.Contains(step.Resources, stacktest.ShipOrdersJob+".schedule") {
				t.Errorf("the job rolls out in %+v, want wave 2 with its job and its schedule", step)
			}
			return
		}
	}
	t.Fatalf("no rollout step rolls the job out: %+v", env.DeployOrder)
}

// TestJobsOfTheStack: stack.Jobs gives each job with its API, its calls
// and its language, whatever the environment.
func TestJobsOfTheStack(t *testing.T) {
	s, services := jobShop()
	jobs, err := stack.Jobs(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want ShipOrders", jobs)
	}
	j := jobs[0]
	if j.Name != stacktest.ShipOrdersJob || j.Language != registry.APILanguageGo || refNames(j.Calls) != "shop-api" || j.Job == nil || j.Job.API != "shop-orders" || j.Job.Name != "ShipOrders" {
		t.Errorf("job = %+v", j)
	}
	servers, err := stack.Servers(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		if server.Kind != ir.DeployableServer {
			t.Errorf("Servers gives %s %s", server.Kind, server.Name)
		}
	}
}

// TestJobRefusals: what resolution refuses of jobs and their settings.
func TestJobRefusals(t *testing.T) {
	reg := assemble(t)
	ship := stacktest.JobOf(stacktest.ShopOrders, "ShipOrders")
	t.Run("a job the API does not declare", func(t *testing.T) {
		s, services := jobShop()
		settingsFor(s.Environment("Staging"), stacktest.JobOf(stacktest.ShopOrders, "ShipOrder")).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names job ShipOrder of shop-orders, which declares no such @job class (its jobs: ShipOrders)")
	})
	t.Run("a job of a DB service", func(t *testing.T) {
		s, services := jobShop()
		settingsFor(s.Environment("Staging"), stacktest.JobOf(stacktest.ShopDB, "ShipOrders")).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "names shop-db, a DB service; it takes API services")
	})
	t.Run("a schedule on a server", func(t *testing.T) {
		s, services := jobShop()
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopAPI)).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule, a time zone or enabled on server shop-api; only a job takes them")
	})
	t.Run("a schedule that is no cron", func(t *testing.T) {
		s, services := jobShop()
		settingsFor(s.Environment("Staging"), ship).Schedule = "@hourly"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, `schedule: "@hourly" is a descriptor`)
	})
	t.Run("a time zone the IANA database lacks", func(t *testing.T) {
		s, services := jobShop()
		settingsFor(s.Environment("Staging"), ship).TimeZone = "Mars/Olympus"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, `timeZone: "Mars/Olympus" is no IANA time zone`)
	})
	t.Run("turned on with no schedule", func(t *testing.T) {
		s, services := jobShop()
		service(services, "shop-orders").Jobs[0].Schedule = ""
		on := true
		settings := settingsFor(s.Environment("Production"), ship)
		settings.Enabled = &on
		_, errs := resolve(t, reg, s, services, "Production")
		mustFail(t, errs, stack.CodeInvalidSettings, "environment Production turns on the schedule of job ShipOrders of shop-orders, which has none")
	})
	t.Run("a job named like another deployable", func(t *testing.T) {
		s, services := jobShop()
		s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: stacktest.ShipOrdersJob, Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{stacktest.ShopDB}})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "job ShipOrders of shop-orders would be named shop-orders-ship-orders, like the database shop-orders-ship-orders")
	})
	t.Run("an exposed job", func(t *testing.T) {
		s, services := jobShop()
		s.Expose = append(s.Expose, ship)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeExposeNotServer, "exposes shop-orders-ship-orders, a job; only a server or a site is exposed")
	})
	t.Run("a target with no job platform", func(t *testing.T) {
		s, services := jobShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noDNS{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "job shop-orders-ship-orders: target no-dns has no platform for a job; place it with a settings platform")
	})
}

func names(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, d := range env.Deployables {
		out = append(out, d.Name)
	}
	return out
}

func refNames(refs []ir.ServiceRef) string {
	var out []string
	for _, ref := range refs {
		out = append(out, ref.Name)
	}
	return strings.Join(out, ",")
}

// bindings renders a deployable's bindings as FIELD=source:what.
func bindings(d *ir.ResolvedDeployable) string {
	var out []string
	for _, b := range d.Bindings {
		var what string
		switch b.Source {
		case ir.BindingLiteral:
			data, _ := json.Marshal(b.Value)
			what = strings.Trim(string(data), `"`)
		case ir.BindingSecret:
			what = b.Secret
		case ir.BindingDerived:
			what = b.Edge
		case ir.BindingParameter:
			what = b.Parameter
		}
		out = append(out, b.Field+"="+string(b.Source)+":"+what)
	}
	return strings.Join(out, " ")
}
