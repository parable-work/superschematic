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

// The engine's runner (D58): shop-db is an engine database, a type of it
// composing a behavior, which shop-api and shop-orders connect to. Its
// runner is a deployable of its own in every environment, named after the
// database, written in the language of shop-api, the first API by name
// that connects to it, with one edge, to its database.

// runnerShop is shop with shop-db an engine database.
func runnerShop() (*ir.Stack, []stack.Service) {
	s, services := shop()
	service(services, "shop-db").Engine = true
	return s, services
}

const shopRunner = "shop-db-engine-runner"

// TestRunnerDeployable: in Staging the runner is placed on the target's
// runner platform in shop-api's Go, serves its database, calls nothing,
// takes one sql edge through the runner platform's connector, whose
// derived field is its one binding, and runs one instance.
func TestRunnerDeployable(t *testing.T) {
	s, services := runnerShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	runner := env.Deployable(shopRunner)
	if runner == nil {
		t.Fatalf("no runner %s among %v", shopRunner, names(env))
	}
	if runner.Kind != ir.DeployableRunner || runner.Platform != stacktest.RunnerPlatform || runner.Language != registry.APILanguageGo || runner.Declared || runner.Exposed || runner.Artifact != ir.ArtifactImage {
		t.Errorf("runner = %s %s on %s in %s (%s), declared %t, exposed %t", runner.Kind, runner.Name, runner.Platform, runner.Language, runner.Artifact, runner.Declared, runner.Exposed)
	}
	if got := refNames(runner.Services); got != "shop-db" || len(runner.Calls) != 0 || len(runner.Buckets) != 0 {
		t.Errorf("services = %s, calls %v, buckets %v; want its database alone", got, runner.Calls, runner.Buckets)
	}
	if want := (ir.ResolvedRunner{Database: "shop-db", API: "shop-api", Instances: 1}); runner.Runner == nil || *runner.Runner != want {
		t.Errorf("run = %+v, want %+v", runner.Runner, want)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.From == runner.Name {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "sql:shop-db-engine-runner->shop-db by fake.runner-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s\nwant    %s", got, want)
	}
	if got := bindings(runner); got != "SHOP_DB_DATABASE=derived:sql:shop-db-engine-runner->shop-db" {
		t.Errorf("bindings = %s, want its database's alone", got)
	}
	if pool := env.Resources.Resource(shopRunner + ".pool"); pool == nil || pool.Properties["instances"] != float64(1) {
		t.Errorf("pool = %+v, want one instance", pool)
	}
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout && slices.Contains(step.Deployables, shopRunner) && step.Wave != 1 {
			t.Errorf("the runner rolls out in %+v, want the first wave: it calls nothing", step)
		}
	}
}

// TestNoRunnerWithoutEngineTypes: a database whose types compose no
// behavior has no runner, nor one no API connects to.
func TestNoRunnerWithoutEngineTypes(t *testing.T) {
	s, services := shop()
	if env := mustResolve(t, assemble(t), s, services, "Staging"); env.Deployable(shopRunner) != nil {
		t.Errorf("a stack without an engine database runs %s", shopRunner)
	}
	s, services = runnerShop()
	for _, name := range []string{"shop-api", "shop-orders"} {
		svc := service(services, name)
		svc.AuthDB, svc.Dependencies = nil, nil
	}
	runners, err := stack.Runners(stack.Input{Stack: s, Services: services})
	if err != nil || len(runners) != 0 {
		t.Errorf("Runners of an engine database no API connects to = %v, %v; want none", runners, err)
	}
}

// TestRunnersOfTheStack: stack.Runners gives each runner with its
// database, its API and its language, whatever the environment: a runner
// of an engine database whose first API is written in TypeScript is
// written in TypeScript.
func TestRunnersOfTheStack(t *testing.T) {
	s, services := runnerShop()
	runners, err := stack.Runners(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if len(runners) != 1 || runners[0].Name != shopRunner || runners[0].Language != registry.APILanguageGo || runners[0].Runner.API != "shop-api" || runners[0].Runner.Database != "shop-db" {
		t.Fatalf("runners = %+v", runners)
	}
	service(services, "shop-api").Language = registry.APILanguageTypeScript
	runners, err = stack.Runners(stack.Input{Stack: s, Services: services})
	if err != nil || len(runners) != 1 || runners[0].Language != registry.APILanguageTypeScript {
		t.Errorf("runners of a TypeScript shop-api = %+v, %v", runners, err)
	}
}

// TestRunnerSettings: a settings element names the runner beside its
// database's handle (`{of: ShopDb, runner: true}`). One that turns it off
// in Staging runs no instance there, nor in Preview, which extends
// Staging, while Production runs its one; a later environment turns it on
// again. A settings platform places it, here on a target with no runner
// platform.
func TestRunnerSettings(t *testing.T) {
	reg := assemble(t)
	instances := func(s *ir.Stack, services []stack.Service, environment string) int {
		t.Helper()
		env := mustResolve(t, reg, s, services, environment)
		pool := env.Resources.Resource(shopRunner + ".pool")
		if pool == nil || pool.Properties["instances"] != float64(env.Deployable(shopRunner).Runner.Instances) {
			t.Fatalf("%s: pool %+v, runner %+v", environment, pool, env.Deployable(shopRunner).Runner)
		}
		return env.Deployable(shopRunner).Runner.Instances
	}
	off, on := false, true
	s, services := runnerShop()
	settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Enabled = &off
	for environment, want := range map[string]int{"Staging": 0, "Preview": 0, "Production": 1} {
		if got := instances(s, services, environment); got != want {
			t.Errorf("%s runs %d instances of the runner, want %d", environment, got, want)
		}
	}
	settingsFor(s.Environment("Preview"), stacktest.RunnerOf(stacktest.ShopDB)).Enabled = &on
	if got := instances(s, services, "Preview"); got != 1 {
		t.Errorf("Preview, turned on again, runs %d instances, want one", got)
	}

	s, services = runnerShop()
	for _, env := range s.Environments {
		if env.Target != "" {
			env.Target = "no-dns"
			env.DNS = nil
		}
	}
	settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Platform = stacktest.RunnerPlatform
	env := mustResolve(t, assemble(t, &noDNS{}), s, services, "Staging")
	if runner := env.Deployable(shopRunner); runner == nil || runner.Platform != stacktest.RunnerPlatform || runner.Runner.Instances != 1 {
		t.Errorf("the runner placed by its settings: %+v", runner)
	}
}

// TestRunnerRefusals: what resolution refuses of runners.
func TestRunnerRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("an engine database on SQLite", func(t *testing.T) {
		s, services := runnerShop()
		service(services, "shop-db").Dialects = []string{registry.SQLDialectSQLite}
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopDB)).Platform = stacktest.LitePlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "runner shop-db-engine-runner runs the engine of shop-db, which environment Staging places on shop-db, a sqlite database; a runner of its own runs on Postgres")
	})
	t.Run("a runner named like another deployable", func(t *testing.T) {
		s, services := runnerShop()
		s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: shopRunner, Kind: ir.DeployableServer, Serves: []ir.ServiceRef{{Name: "shop-orders", Kind: ir.SchemaKindAPI}}})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "the engine's runner of shop-db would be named shop-db-engine-runner, like the server shop-db-engine-runner")
	})
	t.Run("a target with no runner platform", func(t *testing.T) {
		s, services := runnerShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noDNS{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "runner shop-db-engine-runner: target no-dns has no platform for a runner; place it with a settings platform")
	})
	t.Run("the runner of a database that is no engine's", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names the engine's runner of shop-db, which has none: none of its types composes a behavior, so it is no engine database")
	})
	t.Run("the runner of an engine database no API connects to", func(t *testing.T) {
		s, services := runnerShop()
		for _, name := range []string{"shop-api", "shop-orders"} {
			svc := service(services, name)
			svc.AuthDB, svc.Dependencies = nil, nil
		}
		s.Deploy = append(s.Deploy, stacktest.ShopDB)
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names the engine's runner of shop-db, which has none: no API of the stack connects to it")
	})
	t.Run("a runner beside an API's handle", func(t *testing.T) {
		s, services := runnerShop()
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopAPI)).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "names shop-api, a API service; it takes DB")
	})
	t.Run("instances on a runner", func(t *testing.T) {
		s, services := runnerShop()
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Instances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets instances or concurrency on runner shop-db-engine-runner; only a worker takes them")
	})
	t.Run("a schedule on a runner", func(t *testing.T) {
		s, services := runnerShop()
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule or a time zone on runner shop-db-engine-runner, which runs until it is stopped")
	})
	t.Run("env on a runner", func(t *testing.T) {
		s, services := runnerShop()
		settingsFor(s.Environment("Staging"), stacktest.RunnerOf(stacktest.ShopDB)).Env = map[string]ir.EnvValue{"LOG_LEVEL": {Value: "warn"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env LOG_LEVEL on runner shop-db-engine-runner")
	})
	t.Run("enabled on a database", func(t *testing.T) {
		s, services := runnerShop()
		settingsFor(s.Environment("Staging"), stacktest.Of(stacktest.ShopDB)).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule, a time zone, enabled or onDeploy on database shop-db")
	})
}
