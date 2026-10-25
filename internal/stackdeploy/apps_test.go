package stackdeploy_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// backofficeApp is the deployable of shop-orders' app Backoffice (D78).
const backofficeApp = "shop-orders-backoffice"

// TestDeployPinsAnAppsImage deploys Staging with shop-orders in Rust and
// its app Backoffice (D78): the app's image is pinned in the manifest as a
// server's is, rendered into its service, carried forward by a deploy that
// builds only shop-api, and kept by a deploy that fails before its
// rollout.
func TestDeployPinsAnAppsImage(t *testing.T) {
	f := newFixture(t)
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
	env := f.resolve(t, s, services, "Staging")
	if d := env.Deployable(backofficeApp); d == nil || !d.HasImage() {
		t.Fatalf("the app is %+v, want a deployable with an image", d)
	}
	f.ready(t, env)
	ctx := context.Background()
	imgs := images(1)
	delete(imgs, stacktest.ShipOrdersJob)
	imgs[backofficeApp] = backofficeApp + "@" + digest(1)
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

	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, map[string]string{"shop-api": "shop-api@" + digest(2)}))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Images[backofficeApp]; got != imgs[backofficeApp] {
		t.Errorf("a deploy that builds only shop-api pins the app at %q, want its image carried forward, %q", got, imgs[backofficeApp])
	}

	// A deploy that fails before its rollout records the app's previous
	// image, which still runs, not the new one.
	f.ext.Provisioner.Fail = map[string]error{"infrastructure": errors.New("quota exceeded")}
	next := map[string]string{backofficeApp: backofficeApp + "@" + digest(3)}
	m, err = stackdeploy.Deploy(ctx, deployOptions(f, t, env, nil, &planner{to: 1}, next))
	if err == nil {
		t.Fatal("the deploy succeeded, want it to fail at infrastructure")
	}
	if m == nil || m.Status != stackdeploy.StatusFailed || m.Images[backofficeApp] != imgs[backofficeApp] {
		t.Errorf("a failed deploy's manifest = %+v, want the app's previous image %q", m, imgs[backofficeApp])
	}
}
