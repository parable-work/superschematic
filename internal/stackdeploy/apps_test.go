package stackdeploy_test

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// backofficeApp is the deployable of shop-orders' app Backoffice (D78).
const backofficeApp = "shop-orders-backoffice"

// appEnv resolves Staging with shop-orders in Rust and its app Backoffice
// (D78), which Staging's settings turn off unless on is set.
func (f *fixture) appEnv(t *testing.T, on bool) *ir.ResolvedEnvironment {
	t.Helper()
	services := stacktest.WithoutWorkers(stacktest.InRust(stacktest.AcmeShop(), stacktest.ShopOrders.Name))
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			// The fake target runs no Rust job.
			services[i].Apps = []ir.App{{Name: "Backoffice", Extensions: []string{"topcoat"}}}
			services[i].Jobs = nil
		}
	}
	s := stacktest.WithoutWorkerSettings(stacktest.Shop())
	for _, e := range s.Environments {
		e.Settings = slices.DeleteFunc(e.Settings, func(set *ir.DeployableSettings) bool { return set.Of.Job != "" })
	}
	if !on {
		enabled := false
		staging := s.Environment("Staging")
		staging.Settings = append(staging.Settings, &ir.DeployableSettings{Of: stacktest.AppOf(stacktest.ShopOrders, "Backoffice"), Enabled: &enabled})
	}
	return f.resolve(t, s, services, "Staging")
}

// TestDeployPinsAnAppsImage deploys Staging with shop-orders in Rust and
// its app Backoffice (D78): the app's image is pinned in the manifest as a
// server's is, rendered into its service, carried forward by a deploy that
// builds only shop-api, and kept by a deploy that fails before its
// rollout.
func TestDeployPinsAnAppsImage(t *testing.T) {
	f := newFixture(t)
	env := f.appEnv(t, true)
	if d := env.Deployable(backofficeApp); d == nil || !d.HasImage() {
		t.Fatalf("the app is %+v, want a deployable with an image", d)
	}
	f.ready(t, env)
	ctx := context.Background()
	imgs := images(1)
	delete(imgs, stacktest.ShipOrdersJob)
	imgs[backofficeApp] = image(backofficeApp, 1)
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, imgs))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Images[backofficeApp]; got != imgs[backofficeApp] {
		t.Errorf("the manifest pins the app at %q, want %q", got, imgs[backofficeApp])
	}
	if svc := f.ext.Provisioner.Rendered().Resources.Resource(backofficeApp + ".service"); svc == nil || svc.Properties["image"] != imgs[backofficeApp] {
		t.Errorf("the app's service renders %+v, want its image", svc)
	}

	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, map[string]string{"shop-api": image("shop-api", 2)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Images[backofficeApp]; got != imgs[backofficeApp] {
		t.Errorf("a deploy that builds only shop-api pins the app at %q, want its image carried forward, %q", got, imgs[backofficeApp])
	}

	// A deploy that fails before its rollout records the app's previous
	// image, which still runs, not the new one.
	f.ext.Provisioner.Fail = map[string]error{"infrastructure": errors.New("quota exceeded")}
	next := map[string]string{backofficeApp: image(backofficeApp, 3)}
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, next))
	if err == nil {
		t.Fatal("the deploy succeeded, want it to fail at infrastructure")
	}
	if m == nil || m.Status != stackdeploy.StatusFailed || m.Images[backofficeApp] != imgs[backofficeApp] {
		t.Errorf("a failed deploy's manifest = %+v, want the app's previous image %q", m, imgs[backofficeApp])
	}
}

// TestDeployOfAnAppTurnedOff: an app the environment turns off runs no
// image (D78). A deploy neither needs one nor builds one, pins none, and
// refuses --image for it; the manifest keeps the image the app last ran.
func TestDeployOfAnAppTurnedOff(t *testing.T) {
	f := newFixture(t)
	off := f.appEnv(t, false)
	if d := off.Deployable(backofficeApp); d == nil || d.App.Enabled {
		t.Fatalf("the app is %+v, want it off", d)
	}
	f.ready(t, off)
	ctx := context.Background()
	servers := images(1)
	delete(servers, stacktest.ShipOrdersJob)

	// A first deploy, with no image of the app.
	m, err := stackdeploy.Deploy(ctx, deployOptions(f, t, off, nil, &planner{to: 1}, servers))
	if err != nil {
		t.Fatalf("a deploy of the app turned off: %v", err)
	}
	if image, ok := m.Images[backofficeApp]; ok {
		t.Errorf("the manifest records image %q for the app turned off", image)
	}

	// The app runs, then is turned off again: its image is kept.
	on := f.appEnv(t, true)
	withApp := map[string]string{}
	for k, v := range servers {
		withApp[k] = v
	}
	withApp[backofficeApp] = image(backofficeApp, 1)
	if _, err := stackdeploy.Deploy(ctx, deployOptions(f, t, on, nil, &planner{to: 1}, withApp)); err != nil {
		t.Fatal(err)
	}
	n := len(f.calls(0))
	o := deployOptions(f, t, off, nil, &planner{to: 1}, nil)
	o.Sources = sources(t, "shop-api", "Orders", backofficeApp)
	m, err = stackdeploy.Deploy(ctx, o)
	if err != nil {
		t.Fatalf("a deploy that turns the app off: %v", err)
	}
	for _, b := range builds(f.calls(n)) {
		if strings.HasPrefix(b, "build "+backofficeApp+":") {
			t.Errorf("the deploy built the app turned off: %s", b)
		}
	}
	if got := m.Images[backofficeApp]; got != withApp[backofficeApp] {
		t.Errorf("the manifest records the app's image as %q, want the one it last ran, %q", got, withApp[backofficeApp])
	}
	if svc := f.ext.Provisioner.Rendered().Resources.Resource(backofficeApp + ".service"); svc != nil {
		t.Errorf("the app turned off renders a service: %+v", svc)
	}

	// A plan reads the manifest's image of the app and pins nothing.
	if _, err := stackdeploy.Plan(ctx, stackdeploy.PlanOptions{Options: f.options(t, off, nil), Planner: (&planner{to: 1}).plan}); err != nil {
		t.Errorf("a plan of the app turned off: %v", err)
	}

	// A build of the stack builds no image of it, and one that names it
	// is refused.
	src := sources(t, "shop-api", "Orders", backofficeApp)
	r, err := stackdeploy.Build(ctx, stackdeploy.BuildOptions{Options: f.options(t, off, nil), Sources: *src, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Images[backofficeApp]; ok || slices.Contains(r.Built, backofficeApp) {
		t.Errorf("a build of the stack built the app turned off: %+v", r)
	}
	_, err = stackdeploy.Build(ctx, stackdeploy.BuildOptions{Options: f.options(t, off, nil), Sources: *src, Deployables: []string{backofficeApp}})
	if err == nil || !strings.Contains(err.Error(), "turns app "+backofficeApp+" off") {
		t.Errorf("a build of the app turned off: %v", err)
	}

	// stack prepare writes no context of it.
	dir := filepath.Join(t.TempDir(), "prepared")
	if _, err := stackdeploy.Prepare(ctx, stackdeploy.PrepareOptions{
		Stack: off.Stack, Environments: []*ir.ResolvedEnvironment{off}, Sources: src, Dir: dir, Log: io.Discard,
	}); err != nil {
		t.Fatal(err)
	}
	p, err := stackdeploy.ReadPrepared(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Contexts[backofficeApp]; ok || p.Contexts["shop-api"] == nil {
		t.Errorf("prepare wrote the contexts %v, want shop-api's and not the app's", p.Contexts)
	}

	// --image for the app turned off is refused.
	_, err = stackdeploy.Deploy(ctx, deployOptions(f, t, off, nil, &planner{to: 1}, withApp))
	if err == nil || !strings.Contains(err.Error(), "app "+backofficeApp+", which environment Staging turns off") {
		t.Errorf("--image for the app turned off: %v", err)
	}
}
