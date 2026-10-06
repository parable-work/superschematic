package stackdeploy_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// staging is Staging's deploy order on the fake target, as the call log
// records it: infrastructure, the expand phase, the servers callee first,
// the contract phase, exposure.
var staging = []string{
	"render 14 nodes",
	"apply infrastructure: Orders.account, Orders.reads.PaymentsSecrets.STRIPE_KEY, Orders.sql.shop-db, secret.PaymentsSecrets.STRIPE_KEY, " +
		"shop-api.account, shop-api.reads.PaymentsSecrets.STRIPE_KEY, shop-api.sql.shop-db, shop-db.database.shop-db, shop-db.instance",
	"migrate expand shop-db: shop-db",
	"apply rollout 1: Orders.invokes.shop-api, shop-api.service",
	"apply rollout 2: Orders.service",
	"migrate contract shop-db: shop-db",
	"apply exposure: dns.shop-api.1, shop-api.route",
}

func deployOptions(f *fixture, t *testing.T, env *ir.ResolvedEnvironment, params map[string]string, p *planner, imgs map[string]string) stackdeploy.DeployOptions {
	return stackdeploy.DeployOptions{
		Options:  f.options(t, env, params),
		Images:   imgs,
		Planner:  p.plan,
		Services: map[string]string{"shop-api": "sha256:api", "shop-db": "sha256:db"},
		Gate:     stackdeploy.Gate{Allow: []string{"destructive:table/product/column/name", "destructive:table/product/column/title"}},
		Now:      fixedNow,
	}
}

// TestDeployOrder deploys Staging twice: a first deploy from an empty
// database, which has only expand steps, then a change whose plan has
// both phases, around the rollout.
func TestDeployOrder(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()

	p := &planner{to: 1}
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1)))
	if err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(staging)
	want = slices.DeleteFunc(want, func(call string) bool { return strings.HasPrefix(call, "migrate contract") })
	if got := f.calls(0); !slices.Equal(got, want) {
		t.Fatalf("first deploy ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if m.Status != stackdeploy.StatusDeployed || m.Step != "exposure" || m.Run != "Staging" {
		t.Errorf("manifest: status %s, step %s, run %s", m.Status, m.Step, m.Run)
	}
	if got := m.Databases["shop-db"]["shop-db"].Hash; got != modelHash(t, 1) {
		t.Errorf("shop-db holds %s, want v1 %s", got, modelHash(t, 1))
	}
	if !maps(m.Images, images(1)) {
		t.Errorf("images %v", m.Images)
	}
	if got := f.ext.Provisioner.Env()[dnsToken.Env]; got != "dns-token-value" {
		t.Errorf("the provisioner was handed DNS token %q", got)
	}
	service := f.ext.Provisioner.Rendered().Resources.Resource("shop-api.service")
	if got := service.Properties["image"]; got != "shop-api@"+digest(1) {
		t.Errorf("shop-api.service renders image %v", got)
	}
	if strings.Contains(f.log.String(), "sk_test_value") || strings.Contains(f.log.String(), "dns-token-value") {
		t.Errorf("the log holds a secret value:\n%s", f.log)
	}

	// The change: a new image of shop-api only, and v2 of shop-db, whose
	// plan expands before the rollout and contracts after it.
	p.to = 2
	n := len(f.ext.Provisioner.Calls())
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, map[string]string{"shop-api": "shop-api@" + digest(2)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.calls(n); !slices.Equal(got, staging) {
		t.Fatalf("second deploy ran:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(staging, "\n"))
	}
	if !slices.Equal(p.from, []string{"", modelHash(t, 1)}) {
		t.Errorf("planned from %v, want an empty database then v1", p.from)
	}
	if got := m.Databases["shop-db"]["shop-db"].Hash; got != modelHash(t, 2) {
		t.Errorf("shop-db holds %s, want v2", got)
	}
	if m.Images["shop-api"] != "shop-api@"+digest(2) || m.Images["Orders"] != "orders@"+digest(1) {
		t.Errorf("images %v: want shop-api's new one and Orders' carried forward", m.Images)
	}
}

// TestDeployMember deploys a member of the parameterized Preview: a run of
// its own, with the parent's infrastructure inherited.
func TestDeployMember(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Preview")
	f.ready(t, env)
	m, err := stackdeploy.Deploy(context.Background(), deployOptions(f, t, env, map[string]string{"pr": "7"}, &planner{to: 1}, images(1)))
	if err != nil {
		t.Fatal(err)
	}
	if m.Run != "Preview.pr-7" || m.Parameters["pr"] != "7" {
		t.Errorf("manifest of run %s, parameters %v", m.Run, m.Parameters)
	}
	calls := f.calls(0)
	if !slices.Contains(calls, "migrate expand shop-db: shop-db") || slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, "shop-db.instance") }) {
		t.Errorf("the member's deploy ran:\n%s", strings.Join(calls, "\n"))
	}
	if _, err := stackdeploy.Deploy(context.Background(), deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err == nil || !strings.Contains(err.Error(), "takes parameter pr") {
		t.Errorf("a member without its parameter: %v", err)
	}
}

// TestDeployFailedRollout: a rollout that fails runs no contract step, and
// the manifest records the model between the phases, from which the next
// deploy plans; its plan supersedes the pending contract (D27, amended).
func TestDeployFailedRollout(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	p := &planner{to: 1}
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1))); err != nil {
		t.Fatal(err)
	}

	p.to = 2
	f.ext.Provisioner.Fail = map[string]error{"rollout 2": errors.New("Orders is not ready")}
	n := len(f.ext.Provisioner.Calls())
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2)))
	if err == nil || !strings.Contains(err.Error(), "step rollout 2: Orders is not ready") {
		t.Fatalf("deploy = %v", err)
	}
	if slices.ContainsFunc(f.calls(n), func(c string) bool { return strings.HasPrefix(c, "migrate contract") }) {
		t.Errorf("a failed rollout ran the contract phase:\n%s", strings.Join(f.calls(n), "\n"))
	}
	applied := m.Databases["shop-db"]["shop-db"]
	expanded := expandedHash(t, 1, 2)
	if m.Status != stackdeploy.StatusFailed || m.Step != "rollout 2" || applied.Hash != expanded || applied.Pending != nil {
		t.Fatalf("manifest: status %s, step %s, shop-db at %s (want the expanded %s), pending %+v", m.Status, m.Step, applied.Hash, expanded, applied.Pending)
	}
	if m.Images["shop-api"] != "shop-api@"+digest(2) || m.Images["Orders"] != "orders@"+digest(1) {
		t.Errorf("images %v: want wave 1's new image and Orders' previous one", m.Images)
	}

	// The next deploy plans from the expanded model, and contracts.
	f.ext.Provisioner.Fail = nil
	n = len(f.ext.Provisioner.Calls())
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2)))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.from[len(p.from)-1]; got != expanded {
		t.Errorf("the next deploy planned from %s, want the expanded model %s", got, expanded)
	}
	if !slices.Contains(f.calls(n), "migrate contract shop-db: shop-db") || slices.Contains(f.calls(n), "migrate expand shop-db: shop-db") {
		t.Errorf("the next deploy ran:\n%s\nwant the contract phase alone", strings.Join(f.calls(n), "\n"))
	}
	if m.Databases["shop-db"]["shop-db"].Hash != modelHash(t, 2) || m.Status != stackdeploy.StatusDeployed {
		t.Errorf("after the next deploy: %s, %s", m.Status, m.Databases["shop-db"]["shop-db"].Hash)
	}
}

// expandedHash is the hash of the model between the phases of the plan
// from version from to version to.
func expandedHash(t *testing.T, from, to int) string {
	t.Helper()
	p := &planner{to: to}
	data, err := model(from).CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := p.plan("shop-db", "postgres", data)
	if err != nil {
		t.Fatal(err)
	}
	return plan.Expanded
}

// TestDeployFailedMigration: a migration phase that fails is recorded as
// pending on the schema the database held before it, and the next deploy
// finishes it before it runs anything else.
func TestDeployFailedMigration(t *testing.T) {
	for _, phase := range []ir.MigrationPhase{ir.MigrationExpand, ir.MigrationContract} {
		t.Run(string(phase), func(t *testing.T) {
			f := newFixture(t)
			env := f.env(t, "Staging")
			f.ready(t, env)
			ctx := context.Background()
			p := &planner{to: 1}
			if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(1))); err != nil {
				t.Fatal(err)
			}
			p.to = 2
			f.ext.Migrations.Fail = map[string]error{string(phase) + " shop-db": errors.New("lock timeout")}
			m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(2)))
			if err == nil || !strings.Contains(err.Error(), "lock timeout") {
				t.Fatalf("deploy = %v", err)
			}
			applied := m.Databases["shop-db"]["shop-db"]
			before := modelHash(t, 1)
			if phase == ir.MigrationContract {
				before = expandedHash(t, 1, 2)
			}
			if applied.Hash != before || applied.Pending == nil || applied.Pending.Phase != phase {
				t.Fatalf("shop-db: at %s (want %s), pending %+v", applied.Hash, before, applied.Pending)
			}

			// The next deploy, of a v3 that is v1 again, first finishes
			// the pending phase, then plans from where it ends.
			f.ext.Migrations.Fail = nil
			p.to = 1
			n := len(f.ext.Provisioner.Calls())
			if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, p, images(3))); err != nil {
				t.Fatal(err)
			}
			calls := f.calls(n)
			pending := slices.Index(calls, "migrate "+string(phase)+" shop-db: shop-db")
			expand := slices.IndexFunc(calls, func(c string) bool { return strings.HasPrefix(c, "apply rollout 1") })
			if pending < 0 || pending > expand {
				t.Errorf("the next deploy ran:\n%s\nwant the pending %s phase before the rollout", strings.Join(calls, "\n"), phase)
			}
			after := expandedHash(t, 1, 2)
			if phase == ir.MigrationContract {
				after = modelHash(t, 2)
			}
			if got := p.from[len(p.from)-1]; got != after {
				t.Errorf("planned from %s, want %s, where the pending phase ends", got, after)
			}
		})
	}
}

// TestDeploySecrets: the deploy applies the infrastructure, which creates
// a secret's storage, then needs a value for every secret: it fails
// without a terminal and asks on one.
func TestDeploySecrets(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.setSecret(t, env, dnsToken.Secret, "dns-token-value")
	ctx := context.Background()
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1)))
	if err == nil || !strings.Contains(err.Error(), "secret PaymentsSecrets.STRIPE_KEY has no value: run `stack secrets set Staging`") {
		t.Fatalf("deploy without the secret = %v", err)
	}
	if got := f.calls(0); len(got) != 2 || !strings.HasPrefix(got[1], "apply infrastructure") {
		t.Errorf("ran:\n%s\nwant the infrastructure step alone", strings.Join(got, "\n"))
	}
	if m.Status != stackdeploy.StatusFailed {
		t.Errorf("status %s", m.Status)
	}

	term := &terminal{answers: []string{"sk_live_typed"}}
	o := deployOptions(f, t, env, nil, &planner{to: 1}, images(1))
	o.Prompter = term
	if _, err := stackdeploy.Deploy(ctx, o); err != nil {
		t.Fatal(err)
	}
	if len(term.prompts) != 1 || term.prompts[0] != "Value of PaymentsSecrets.STRIPE_KEY (read by Orders, shop-api): " {
		t.Errorf("prompts %q", term.prompts)
	}
	if got, _ := f.ext.Secrets.Get(ctx, env, "PaymentsSecrets.STRIPE_KEY"); string(got) != "sk_live_typed" {
		t.Errorf("stored %q", got)
	}
	if strings.Contains(f.log.String(), "sk_live_typed") {
		t.Errorf("the log holds the value:\n%s", f.log)
	}
}

// TestDeployRefusals covers what a deploy refuses before it applies
// anything.
func TestDeployRefusals(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	destructive := func(o *stackdeploy.DeployOptions) { o.Gate = stackdeploy.Gate{} }
	for _, tc := range []struct {
		name  string
		first bool
		edit  func(*stackdeploy.DeployOptions)
		want  string
	}{
		{"missing image", true, func(o *stackdeploy.DeployOptions) { delete(o.Images, "Orders") }, "no image for server Orders: the manifest records none"},
		{"image of no server", false, func(o *stackdeploy.DeployOptions) { o.Images["shop-db"] = "shop-db@" + digest(1) }, "--image names shop-db, which is not a server"},
		{"image of another repository", false, func(o *stackdeploy.DeployOptions) { o.Images["Orders"] = "elsewhere/orders@" + digest(1) }, "no property of its nodes holds the image repository elsewhere/orders"},
		{"unacknowledged hazard", false, destructive, "destructive:table/product/column/name"},
		{"plans not the ones shown", false, func(o *stackdeploy.DeployOptions) { o.Expected = map[string]string{"shop-db": "abc"} }, "not the expected abc"},
		{"no planner", false, func(o *stackdeploy.DeployOptions) { o.Planner = nil }, "no migration planner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A previous deploy at v1, so v2's plan drops a column.
			f := newFixture(t)
			f.ready(t, env)
			if !tc.first {
				if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
					t.Fatal(err)
				}
			}
			n := len(f.ext.Provisioner.Calls())
			o := deployOptions(f, t, env, nil, &planner{to: 2}, images(1))
			tc.edit(&o)
			_, err := stackdeploy.Deploy(ctx, o)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("deploy = %v, want an error containing %q", err, tc.want)
			}
			if got := f.calls(n); len(got) > 0 {
				t.Errorf("a refused deploy ran %v", got)
			}
		})
	}
}

// TestPlan plans a first deploy and then a change, with no image given.
func TestPlan(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.setSecret(t, env, dnsToken.Secret, "dns-token-value")
	ctx := context.Background()
	out, err := stackdeploy.Plan(ctx, stackdeploy.PlanOptions{Options: f.options(t, env, nil), Planner: (&planner{to: 1}).plan})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Changes) != 14 || len(out.Databases) != 1 || out.Databases[0].ExpandSteps == 0 {
		t.Errorf("plan: %d changes, databases %+v", len(out.Changes), out.Databases)
	}
	if !slices.Equal(out.Unpinned, []string{"Orders", "shop-api"}) || !slices.Equal(out.MissingSecrets, []string{"PaymentsSecrets.STRIPE_KEY"}) {
		t.Errorf("unpinned %v, missing secrets %v", out.Unpinned, out.MissingSecrets)
	}
	if got := out.Expected(); got["shop-db"] != out.Databases[0].Hash {
		t.Errorf("Expected = %v", got)
	}
	if calls := f.calls(0); slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "apply") || strings.HasPrefix(c, "migrate") }) {
		t.Errorf("plan applied something: %v", calls)
	}

	f.setSecret(t, env, "PaymentsSecrets.STRIPE_KEY", "sk")
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
		t.Fatal(err)
	}
	out, err = stackdeploy.Plan(ctx, stackdeploy.PlanOptions{Options: f.options(t, env, nil), Planner: (&planner{to: 2}).plan})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Unpinned) > 0 || len(out.MissingSecrets) > 0 {
		t.Errorf("after a deploy: unpinned %v, missing %v", out.Unpinned, out.MissingSecrets)
	}
	if len(out.Unallowed) != 1 || out.Unallowed[0].ID != "destructive:table/product/column/name" {
		t.Errorf("unallowed %+v", out.Unallowed)
	}
	if out.Databases[0].From != modelHash(t, 1) || out.Databases[0].ContractSteps == 0 {
		t.Errorf("plan from %s with %d contract steps", out.Databases[0].From, out.Databases[0].ContractSteps)
	}
}

// TestDestroyAndOutputs reads a run's outputs and destroys it, which drops
// its manifest.
func TestDestroyAndOutputs(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	ctx := context.Background()
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, images(1))); err != nil {
		t.Fatal(err)
	}
	out, err := stackdeploy.Outputs(ctx, f.options(t, env, nil))
	if err != nil {
		t.Fatal(err)
	}
	if out["shop-api.service"]["id"] != "shop-api.service" {
		t.Errorf("outputs %v", out)
	}
	if err := stackdeploy.Destroy(ctx, f.options(t, env, nil)); err != nil {
		t.Fatal(err)
	}
	if calls := f.calls(0); calls[len(calls)-1] != "destroy Staging" {
		t.Errorf("last call %s", calls[len(calls)-1])
	}
	run := f.options(t, env, nil).Run
	if _, err := f.ext.State.ReadManifest(ctx, run); err == nil {
		t.Error("destroy left the manifest")
	}
}

func maps(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
