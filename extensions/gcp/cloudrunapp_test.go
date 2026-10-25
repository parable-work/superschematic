package gcp_test

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// backofficeApp is the deployable of shop-orders' app Backoffice (D78).
const backofficeApp = "shop-orders-backoffice"

// backoffice names the app in settings and in expose.
var backoffice = stacktest.AppOf(stacktest.ShopOrders, "Backoffice")

// appServices is the acme shop with shop-orders in Rust, declaring the app
// Backoffice.
func appServices() []stack.Service {
	services := stacktest.InRust(stacktest.AcmeShop(), stacktest.ShopOrders.Name)
	for i := range services {
		if services[i].Name == stacktest.ShopOrders.Name {
			services[i].Apps = []ir.App{{Name: "Backoffice", Extensions: []string{"topcoat"}}}
		}
	}
	return services
}

// TestAppPlatform checks the Cloud Run service an app lowers to (D78): a
// service of its own named after the app, with an account of its own and a
// database user, its API's Cloud SQL volume and VPC egress, internal
// ingress when it is not exposed, and a TCP startup probe on its port in
// place of the entrypoint's health paths, which the app's program does not
// answer. Its edges are its API's, through the app platform's connectors,
// and its address is the service's run.app URL.
func TestAppPlatform(t *testing.T) {
	env := resolve(t, assemble(t), shop(), appServices(), "Staging")
	d := env.Deployable(backofficeApp)
	if d == nil || d.Kind != ir.DeployableApp || d.Platform != gcp.CloudRunApp || d.Language != registry.APILanguageRust || d.Artifact != ir.ArtifactImage {
		t.Fatalf("the app is %+v", d)
	}
	wantJSON(t, "the app's address", d.Address, `{"$output":{"resource":"`+backofficeApp+`.service","name":"uri"}}`)
	if d.PublicAddress != nil {
		t.Errorf("an app that is not exposed has the public address %v", d.PublicAddress)
	}
	edges := map[ir.EdgeKind]bool{}
	for _, e := range env.Edges {
		if e.From != backofficeApp {
			continue
		}
		edges[e.Kind] = true
		want := map[ir.EdgeKind]string{ir.EdgeSQL: gcp.AppSQLConnector, ir.EdgeHTTP: gcp.AppHTTPConnector, ir.EdgeBucket: gcp.AppBucketConnector}[e.Kind]
		if e.Connector != want {
			t.Errorf("edge %s is connected by %s, want %s", e.ID, e.Connector, want)
		}
	}
	if !edges[ir.EdgeSQL] || !edges[ir.EdgeHTTP] {
		t.Errorf("the app's edges are %v, want its API's sql and http edges", edges)
	}
	svc := node(t, env, backofficeApp+".service")
	if svc.Type != gcp.TypeService || svc.Properties["name"] != backofficeApp || svc.Properties["ingress"] != "INGRESS_TRAFFIC_INTERNAL_ONLY" || svc.Properties["invokerIamDisabled"] != false {
		t.Errorf("the app's service: %s %v", svc.Type, svc.Properties)
	}
	template := svc.Properties["template"].(map[string]any)
	wantJSON(t, "the app's account", template["serviceAccount"], `{"$output":{"resource":"`+backofficeApp+`.account","name":"email"}}`)
	if _, ok := template["volumes"]; !ok {
		t.Errorf("the app's template has no Cloud SQL volume: %v", template)
	}
	if _, ok := template["vpcAccess"]; !ok {
		t.Errorf("the app, which calls shop-api, has no VPC egress: %v", template)
	}
	container := template["containers"].([]any)[0].(map[string]any)
	wantJSON(t, "the app's port", container["ports"], `{"containerPort":8080}`)
	wantJSON(t, "the app's startup probe", container["startupProbe"], `{"failureThreshold":24,"periodSeconds":5,"tcpSocket":{"port":8080},"timeoutSeconds":4}`)
	if probe, ok := container["livenessProbe"]; ok {
		t.Errorf("the app has a liveness probe: %v", probe)
	}
	wantJSON(t, "the app's database user", node(t, env, backofficeApp+".database-user.shop-db").Properties["name"], `"shop-orders-backoffice@acme-staging.iam"`)

	// A server keeps the entrypoint's probes.
	orders := node(t, env, "Orders.service").Properties["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	wantJSON(t, "the server's startup probe", orders["startupProbe"], `{"failureThreshold":24,"httpGet":{"path":"/readyz","port":8080},"periodSeconds":5,"timeoutSeconds":4}`)
}

// TestAppExposed: an exposed app takes traffic from the load balancer
// under the environment's domain, with the invoker check off, as an
// exposed server does, and is reached at its host there; the
// nothing-public policy admits it.
func TestAppExposed(t *testing.T) {
	s := shop()
	s.Expose = append(s.Expose, backoffice)
	env := resolve(t, assemble(t), s, appServices(), "Staging")
	d := env.Deployable(backofficeApp)
	if !d.Exposed {
		t.Fatalf("the app is not exposed")
	}
	wantJSON(t, "the app's public address", d.PublicAddress, `"https://shop-orders-backoffice.staging.acme.dev"`)
	svc := node(t, env, backofficeApp+".service")
	if svc.Properties["ingress"] != "INGRESS_TRAFFIC_INTERNAL_LOAD_BALANCER" || svc.Properties["invokerIamDisabled"] != true {
		t.Errorf("the exposed app's service: %v", svc.Properties)
	}
	if !hasRecord(env, backofficeApp) {
		t.Errorf("the exposed app has no DNS record")
	}
}

// TestAppTurnedOff: an app the environment turns off stays in the
// environment with its edges, lowers to its account and what its bindings
// need but no service, and has no address, exposed or not, so nothing
// references the service it leaves out.
func TestAppTurnedOff(t *testing.T) {
	s := shop()
	s.Expose = append(s.Expose, backoffice)
	off := false
	s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: backoffice, Enabled: &off})
	env := resolve(t, assemble(t), s, appServices(), "Staging")
	d := env.Deployable(backofficeApp)
	if d == nil || d.App == nil || d.App.Enabled {
		t.Fatalf("the app is %+v, want it in the environment and off", d)
	}
	if d.Address != nil || d.PublicAddress != nil {
		t.Errorf("an app turned off has address %v and public address %v", d.Address, d.PublicAddress)
	}
	owned := ownedBy(env, backofficeApp)
	if !slices.Contains(owned, backofficeApp+".account") {
		t.Errorf("an app turned off lowers to %v, want its account among them", owned)
	}
	for _, id := range owned {
		if strings.HasPrefix(id, backofficeApp+".service") {
			t.Errorf("an app turned off has %s", id)
		}
	}
	if hasRecord(env, backofficeApp) {
		t.Errorf("an app turned off has a DNS record")
	}
}

// hasRecord reports whether env writes a DNS record for deployable.
func hasRecord(env *ir.ResolvedEnvironment, deployable string) bool {
	return env.DNS != nil && slices.ContainsFunc(env.DNS.Records, func(r *ir.DNSRecord) bool { return r.Deployable == deployable })
}
