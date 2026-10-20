package stackdeploy_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// A job that runs on every deploy (D52, amended), on the fake target.

// onDeployEnv resolves an environment of the shop stack whose Staging runs
// shop-orders' job ShipOrders on every deploy.
func (f *fixture) onDeployEnv(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	s := stacktest.WithJobOnDeploy(stacktest.WithoutWorkerSettings(stacktest.Shop()), "Staging")
	return f.resolve(t, s, stacktest.WithoutWorkers(stacktest.AcmeShop()), name)
}

// runShipOrders is the fake job runner's line for a run of ShipOrders at
// the image of images(n).
func runShipOrders(n int) string {
	return "run job " + stacktest.ShipOrdersJob + ": " + images(n)[stacktest.ShipOrdersJob]
}

// TestDeployRunsAJobOnEveryDeploy: a deploy of Staging runs ShipOrders
// through the target's job runner once, after the last rollout wave and
// before the contract phase, with the image the rollout gave it, and runs
// it again on the next deploy, which changes nothing else. A plan lists it.
func TestDeployRunsAJobOnEveryDeploy(t *testing.T) {
	f := newFixture(t)
	env := f.onDeployEnv(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	p := &planner{to: 1}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1))); err != nil {
		t.Fatal(err)
	}
	p.to = 2
	n := len(f.ext.Provisioner.Calls())
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2)))
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(staging)
	contract := slices.Index(want, "migrate contract shop-db: shop-db")
	want = slices.Insert(want, contract, runShipOrders(2))
	if got := f.calls(n); !slices.Equal(got, want) {
		t.Fatalf("the deploy ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if m.Status != stackdeploy.StatusDeployed || m.Step != "exposure" {
		t.Errorf("manifest: %s at %s", m.Status, m.Step)
	}
	if !strings.Contains(f.log.String(), "step 5/7: jobs\n  run job "+stacktest.ShipOrdersJob+" (image "+images(2)[stacktest.ShipOrdersJob]+") and wait for it to end\n") {
		t.Errorf("the log:\n%s", f.log)
	}

	// A deploy that changes nothing runs the job again.
	n = len(f.ext.Provisioner.Calls())
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, nil)); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(n); !slices.Contains(got, runShipOrders(2)) {
		t.Errorf("a deploy that changes nothing ran:\n%s", strings.Join(got, "\n"))
	}

	plan, err := stackdeploy.Plan(ctx, stackdeploy.PlanOptions{Options: f.options(t, env, nil), Planner: p.plan})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(plan.Jobs, []string{stacktest.ShipOrdersJob}) {
		t.Errorf("the plan's jobs: %v", plan.Jobs)
	}
	production := f.onDeployEnv(t, "Production")
	f.ready(t, production)
	n = len(f.ext.Provisioner.Calls())
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, production, nil, &planner{to: 1}, images(1))); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(n); slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "run job") }) {
		t.Errorf("Production, which runs no job on deploy, ran:\n%s", strings.Join(got, "\n"))
	}
}

// TestDeployReportsAFailedJob: a job whose last try fails stops the deploy
// at the jobs step, with the runner's error, before the contract phase, as
// a failed rollout does: the manifest records the failure at the jobs step,
// the rollout's images and the model between the phases. The next deploy
// runs the job again, then contracts.
func TestDeployReportsAFailedJob(t *testing.T) {
	f := newFixture(t)
	env := f.onDeployEnv(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	p := &planner{to: 1}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1))); err != nil {
		t.Fatal(err)
	}

	p.to = 2
	f.ext.Jobs.Fail = map[string]error{stacktest.ShipOrdersJob: errors.New("the backfill failed at order 7 (logs: https://logs.fake.test/7)")}
	n := len(f.ext.Provisioner.Calls())
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2)))
	if err == nil || err.Error() != "deploy Staging, step jobs: job "+stacktest.ShipOrdersJob+": the backfill failed at order 7 (logs: https://logs.fake.test/7)" {
		t.Fatalf("a deploy whose job fails: %v", err)
	}
	calls := f.calls(n)
	if !slices.Contains(calls, runShipOrders(2)) || slices.ContainsFunc(calls, func(c string) bool {
		return strings.HasPrefix(c, "migrate contract") || strings.HasPrefix(c, "apply exposure")
	}) {
		t.Errorf("a deploy whose job fails ran:\n%s", strings.Join(calls, "\n"))
	}
	expanded := expandedHash(t, 1, 2)
	if m.Status != stackdeploy.StatusFailed || m.Step != "jobs" || !strings.Contains(m.Error, "the backfill failed at order 7") {
		t.Errorf("manifest: %s at %s: %s", m.Status, m.Step, m.Error)
	}
	if got := m.Databases["shop-db"]["shop-db"].Hash; got != expanded {
		t.Errorf("shop-db at %s, want the expanded %s", got, expanded)
	}
	if !maps(m.Images, images(2)) {
		t.Errorf("images %v: want the rollout's", m.Images)
	}

	f.ext.Jobs.Fail = nil
	n = len(f.ext.Provisioner.Calls())
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2))); err != nil {
		t.Fatal(err)
	}
	calls = f.calls(n)
	job, contract := slices.Index(calls, runShipOrders(2)), slices.Index(calls, "migrate contract shop-db: shop-db")
	if job < 0 || contract < job {
		t.Errorf("the next deploy ran:\n%s\nwant the job again, then the contract phase", strings.Join(calls, "\n"))
	}
}

// TestDeployRefusesAJobItCannotRun: a target with no job runner cannot run
// a job on each deploy, so its deploy is refused before anything runs.
func TestDeployRefusesAJobItCannotRun(t *testing.T) {
	ext := &stacktest.Extension{NoJobRunner: true}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{reg: reg, ext: ext, log: newFixture(t).log}
	env := f.onDeployEnv(t, "Staging")
	f.ready(t, env)
	_, err = stackdeploy.Deploy(context.Background(), deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	if err == nil || err.Error() != "environment Staging runs job "+stacktest.ShipOrdersJob+" on each deploy, and target fake runs no job on demand; turn its onDeploy off" {
		t.Errorf("a deploy on a target with no job runner: %v", err)
	}
	if calls := f.calls(0); len(calls) > 0 {
		t.Errorf("the refused deploy ran %v", calls)
	}
	if held, _ := ext.State.ReadLock(context.Background(), registry.Run{Environment: env}); held != nil {
		t.Errorf("the refused deploy left its lock: %+v", held)
	}
}
