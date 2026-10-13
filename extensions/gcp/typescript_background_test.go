package gcp_test

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// TestTypeScriptJobAndWorker: with shop-orders in TypeScript, its job is a
// Cloud Run job and its worker a Cloud Run worker pool, as a Go API's are
// (D52, D53): each runs the image its entrypoint's Dockerfile builds, so
// the platforms lower them alike.
func TestTypeScriptJobAndWorker(t *testing.T) {
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Language = registry.APILanguageTypeScript
		}
	}
	env := resolve(t, assemble(t), shop(), services, "Staging")
	for name, want := range map[string]struct {
		kind     ir.DeployableKind
		platform string
		node     string
	}{
		stacktest.ShipOrdersJob:      {ir.DeployableJob, gcp.CloudRunJob, stacktest.ShipOrdersJob + ".job"},
		stacktest.FulfilOrdersWorker: {ir.DeployableWorker, gcp.CloudRunWorker, stacktest.FulfilOrdersWorker + ".worker-pool"},
	} {
		d := env.Deployable(name)
		if d == nil || d.Kind != want.kind || d.Platform != want.platform || d.Language != registry.APILanguageTypeScript {
			t.Fatalf("%s is %+v, want a TypeScript %s on %s", name, d, want.kind, want.platform)
		}
		node(t, env, want.node)
	}
}
