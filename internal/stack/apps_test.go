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

// The app deployable kind (D78): shop-orders, a Rust API here, declares
// Backoffice, the project's own HTTP program over shop-orders' Deps, whose
// build needs the topcoat extension. It is a deployable of its own in
// every environment, named after its API and its class, with its API's
// edges, config and identity, served as a server is.

// appShop is shop with shop-orders a Rust API that declares the app
// Backoffice, which needs the topcoat extension.
func appShop() (*ir.Stack, []stack.Service) {
	s, services := shop()
	orders := service(services, "shop-orders")
	orders.Language = registry.APILanguageRust
	orders.Apps = []ir.App{{Name: "Backoffice", Extensions: []string{"topcoat"}}}
	return s, services
}

const backofficeApp = "shop-orders-backoffice"

var backoffice = stacktest.AppOf(stacktest.ShopOrders, "Backoffice")

// TestAppDeployable: in Staging the app is placed on the target's app
// platform in its API's Rust, serves and calls what its API does, takes
// its API's sql and http edges through the app platform's connectors and
// its API's config, with the env its API's server is given, runs, and
// rolls out after shop-api, which its API calls, and after Orders, its
// API's server (D78). It is not exposed unless the stack says so.
func TestAppDeployable(t *testing.T) {
	s, services := appShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	d := env.Deployable(backofficeApp)
	if d == nil {
		t.Fatalf("no app %s among %v", backofficeApp, names(env))
	}
	if d.Kind != ir.DeployableApp || d.Platform != stacktest.AppPlatform || d.Language != registry.APILanguageRust || d.Declared || d.Exposed || d.Artifact != ir.ArtifactImage {
		t.Errorf("app = %s %s on %s in %s (%s), declared %t, exposed %t", d.Kind, d.Name, d.Platform, d.Language, d.Artifact, d.Declared, d.Exposed)
	}
	if got := refNames(d.Services); got != "shop-orders" {
		t.Errorf("services = %s, want its API, shop-orders", got)
	}
	if got := refNames(d.Calls); got != "shop-api" {
		t.Errorf("calls = %s, want its API's, shop-api", got)
	}
	want := ir.ResolvedApp{API: "shop-orders", Name: "Backoffice", Extensions: []string{"topcoat"}, Enabled: true}
	if d.App == nil || fmt.Sprint(*d.App) != fmt.Sprint(want) {
		t.Errorf("app = %+v, want %+v", d.App, want)
	}
	var edges []string
	for _, e := range env.Edges {
		if e.From == d.Name {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	if got, want := strings.Join(edges, "; "), "http:shop-orders-backoffice->shop-api by fake.app-run into SHOP_API_SERVICE; sql:shop-orders-backoffice->shop-db by fake.app-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s\nwant    %s", got, want)
	}
	if got := bindings(d); got != "FULFILLMENT_REGION=literal:us MAX_LINE_ITEMS=literal:50 SHOP_API_SERVICE=derived:http:shop-orders-backoffice->shop-api SHOP_DB_DATABASE=derived:sql:shop-orders-backoffice->shop-db STRIPE_KEY=secret:PaymentsSecrets.STRIPE_KEY" {
		t.Errorf("bindings = %s, want its API's config, with Orders's env", got)
	}
	if svc := env.Resources.Resource(backofficeApp + ".service"); svc == nil || svc.Properties["public"] != false || svc.Properties["language"] != "rust" {
		t.Errorf("service = %+v, want a private Rust service", svc)
	}
	waves := map[string]int{}
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout {
			for _, name := range step.Deployables {
				waves[name] = step.Wave
			}
		}
	}
	if waves[backofficeApp] != 3 || waves["Orders"] != 2 || waves["shop-api"] != 1 {
		t.Errorf("rollout waves %v: want the app in wave 3, after Orders, its API's server, whose start deploys what the app's engine waits for, and Orders after shop-api, which both call", waves)
	}
}

// TestAppExposed: an app the stack exposes beside its API's handle is
// reachable at its public address, with a route and a record under the
// environment's domain, as an exposed server is.
func TestAppExposed(t *testing.T) {
	s, services := appShop()
	s.Expose = append(s.Expose, backoffice)
	env := mustResolve(t, assemble(t), s, services, "Staging")
	d := env.Deployable(backofficeApp)
	if !d.Exposed || d.PublicAddress == nil {
		t.Errorf("app exposed %t at %v, want exposed at a public address", d.Exposed, d.PublicAddress)
	}
	if env.Resources.Resource(backofficeApp+".route") == nil {
		t.Errorf("no route for the exposed app among %v", env.Resources.Resources)
	}
}

// TestAppServesItsAPIsCallersAndIdentity: an app reads its API's callers
// field, which lists no issuer, since no deployable calls an app, and its
// API's identity config field, from the env its API's server is given;
// and it is a caller of its own in the callers field of an API its API
// calls, as a job is.
func TestAppServesItsAPIsCallersAndIdentity(t *testing.T) {
	s, _ := appShop()
	services := stacktest.AllowServiceShop()
	orders := service(services, "shop-orders")
	orders.Language = registry.APILanguageRust
	orders.Apps = []ir.App{{Name: "Backoffice"}}
	orders.Jobs, orders.Workers = nil, nil
	orders.Identity = true
	settingsFor(s.Environment("Staging"), ir.DeployableRef{Deployable: "Orders"}).Env["SHOP_ORDERS_IDENTITY"] = ir.EnvValue{Value: `{"session":{"ttl":"1h"}}`}
	services = stacktest.WithoutBuckets(services)
	env := mustResolve(t, assemble(t), s, services, "Staging")
	d := env.Deployable(backofficeApp)
	var callers, identity *ir.Binding
	for _, b := range d.Bindings {
		switch b.Field {
		case "SHOP_ORDERS_CALLERS":
			callers = b
		case "SHOP_ORDERS_IDENTITY":
			identity = b
		}
	}
	if callers == nil || callers.CallersOf != "shop-orders" || len(callers.Edges) != 0 {
		t.Errorf("callers = %+v, want shop-orders' callers field with no edge", callers)
	} else if data, _ := json.Marshal(callers.Value); string(data) != `{"issuers":[]}` {
		t.Errorf("callers value = %s, want no issuer", data)
	}
	if identity == nil || identity.IdentityOf != "shop-orders" || identity.Source != ir.BindingLiteral {
		t.Errorf("identity = %+v, want shop-orders' identity field from Orders's env", identity)
	}
	api := env.Deployable("shop-api")
	for _, b := range api.Bindings {
		if b.Field == "SHOP_API_CALLERS" && !slices.Contains(b.Edges, "http:shop-orders-backoffice->shop-api") {
			t.Errorf("shop-api's callers field reaches %v, want the app's edge among them", b.Edges)
		}
	}
}

// TestAppSettings: a setting turns the app off, which is in the
// environment, with its edges, and runs nothing; Preview inherits
// Staging's.
func TestAppSettings(t *testing.T) {
	reg := assemble(t)
	s, services := appShop()
	off := false
	settingsFor(s.Environment("Staging"), backoffice).Enabled = &off
	for _, name := range []string{"Staging", "Preview"} {
		env := mustResolve(t, reg, s, services, name)
		d := env.Deployable(backofficeApp)
		if d.App.Enabled {
			t.Errorf("%s runs the app, want it off", name)
		}
		if env.Resources.Resource(backofficeApp+".service") != nil {
			t.Errorf("%s runs a service for the app turned off", name)
		}
	}
	if d := mustResolve(t, reg, s, services, "Production").Deployable(backofficeApp); !d.App.Enabled {
		t.Errorf("Production turns the app off, want it on")
	}
}

// TestAppsOfTheStack: stack.Apps gives each app with its API, its
// extensions, its calls and its language, whatever the environment.
func TestAppsOfTheStack(t *testing.T) {
	s, services := appShop()
	service(services, "shop-orders").Apps = append(service(services, "shop-orders").Apps, ir.App{Name: "Kiosk"})
	apps, err := stack.Apps(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, d := range apps {
		got = append(got, fmt.Sprintf("%s %s %s %s/%s %v", d.Name, d.Language, refNames(d.Calls), d.App.API, d.App.Name, d.App.Extensions))
	}
	if want := []string{
		"shop-orders-backoffice RUST shop-api shop-orders/Backoffice [topcoat]",
		"shop-orders-kiosk RUST shop-api shop-orders/Kiosk []",
	}; !slices.Equal(got, want) {
		t.Errorf("apps =\n%q\nwant\n%q", got, want)
	}
}

// TestCheckAppExtensions: an app whose build needs an extension the binary
// does not link is refused, naming the binary that links it; one the
// binary links passes.
func TestCheckAppExtensions(t *testing.T) {
	s, services := appShop()
	apps, err := stack.Apps(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.CheckAppExtensions(apps, []string{"fake", "topcoat"}); err != nil {
		t.Errorf("CheckAppExtensions with topcoat linked = %v", err)
	}
	err = stack.CheckAppExtensions(apps, []string{"fake"})
	if want := "app Backoffice of shop-orders needs the topcoat extension, which this superschematic binary does not link; run superschematic-topcoat"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("CheckAppExtensions without topcoat = %v, want %q", err, want)
	}
	// Resolution places the app whatever the binary links.
	mustResolve(t, assemble(t), s, services, "Staging")
}

// noApps is a target that places servers and databases and says why it
// places no app, as cloudflare does (D78).
type noApps struct{}

func (*noApps) Name() string { return "noapps" }
func (*noApps) Register(r *registry.Registry) error {
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "no-apps",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: stacktest.RunPlatform, ir.DeployableDatabase: stacktest.SQLPlatform},
		Unplaced:  map[ir.DeployableKind]string{ir.DeployableApp: "an app is a Rust program, and no-apps runs Go and TypeScript only"},
		Values:    json.RawMessage(`{"type": "object"}`),
	})
}

// TestAppRefusals: what resolution refuses of apps and their settings.
func TestAppRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("an app the API does not declare", func(t *testing.T) {
		s, services := appShop()
		settingsFor(s.Environment("Staging"), stacktest.AppOf(stacktest.ShopOrders, "Back")).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownDeployable, "names app Back of shop-orders, which declares no such @app class (its apps: Backoffice)")
	})
	t.Run("a schedule on an app", func(t *testing.T) {
		s, services := appShop()
		settingsFor(s.Environment("Staging"), backoffice).Schedule = "0 * * * *"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets a schedule or a time zone on app shop-orders-backoffice, which serves until it is stopped")
	})
	t.Run("instances on an app", func(t *testing.T) {
		s, services := appShop()
		settingsFor(s.Environment("Staging"), backoffice).Instances = intp(2)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "sets instances, maxInstances or concurrency on app shop-orders-backoffice; an app takes enabled")
	})
	t.Run("an app and a job", func(t *testing.T) {
		s, services := appShop()
		ref := backoffice
		ref.Job = "ShipOrders"
		settingsFor(s.Environment("Staging"), ref).Enabled = new(bool)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "names app Backoffice and a job, a worker or a deriver; an element names one")
	})
	for _, lang := range []string{registry.APILanguageTypeScript, ""} {
		name := lang
		if name == "" {
			name = registry.APILanguageGo
		}
		t.Run("a "+name+" API", func(t *testing.T) {
			s, services := appShop()
			service(services, "shop-orders").Language = lang
			_, errs := resolve(t, reg, s, services, "Staging")
			want := "app Backoffice of shop-orders: shop-orders is a " + name + " API, and an app runs on a Rust API; Go and TypeScript follow"
			mustFail(t, errs, stack.CodeUnrealizable, want)
			if _, err := stack.Apps(stack.Input{Stack: s, Services: services}); err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Apps = %v, want the %s API's app refused", err, name)
			}
		})
	}
	t.Run("an app named like a job", func(t *testing.T) {
		s, services := appShop()
		service(services, "shop-orders").Jobs = []ir.Job{{Name: "Backoffice"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "app Backoffice of shop-orders would be named shop-orders-backoffice, like the job shop-orders-backoffice")
	})
	t.Run("an extension named twice", func(t *testing.T) {
		s, services := appShop()
		service(services, "shop-orders").Apps[0].Extensions = []string{"topcoat", "topcoat"}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidStack, "app Backoffice of shop-orders: names extension topcoat twice")
	})
	t.Run("a target with no app platform", func(t *testing.T) {
		s, services := appShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noDNS{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "app shop-orders-backoffice: target no-dns has no platform for an app; place it with a settings platform")
	})
	t.Run("a target that places no app", func(t *testing.T) {
		s, services := appShop()
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-apps"
				env.DNS = nil
			}
		}
		_, errs := resolve(t, assemble(t, &noApps{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "app shop-orders-backoffice: target no-apps places no app: an app is a Rust program, and no-apps runs Go and TypeScript only")
	})
}
