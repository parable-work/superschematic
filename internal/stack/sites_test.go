package stack_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// siteShop returns the acceptance stack and services with the site
// shop-web, which calls shop-api (D55), without shop-orders' job.
func siteShop() (*ir.Stack, []stack.Service) {
	return stacktest.WithoutJobSettings(stacktest.WithSite(stacktest.Shop())), stacktest.WithoutJobs(stacktest.SiteShop())
}

// TestSiteDeployable: a Site service the stack deploys is a site of its
// own, exposed without being named in expose, with a site edge to the
// server of the API it calls and that server's public address as its one
// binding; shop-api's server gets SHOP_API_CORS, the site's origin; and the
// site rolls out after the server it calls.
func TestSiteDeployable(t *testing.T) {
	reg := assemble(t)
	s, services := siteShop()
	env := mustResolve(t, reg, s, services, "Staging")
	site := env.Deployable("shop-web")
	if site == nil || site.Kind != ir.DeployableSite || site.Platform != stacktest.SitePlatform || !site.Exposed {
		t.Fatalf("shop-web = %+v, want an exposed site on %s", site, stacktest.SitePlatform)
	}
	if site.Site == nil || site.Site.Dir != "web/shop-web" || site.Site.Fallback != "index.html" {
		t.Errorf("shop-web's site = %+v", site.Site)
	}
	if got, want := site.PublicAddress, "https://shop-web.staging.acme.dev"; got != want {
		t.Errorf("shop-web's public address = %v, want %s", got, want)
	}
	if !strings.Contains(edgeIDs(env), "site:shop-web->shop-api") {
		t.Errorf("edges = %s, want site:shop-web->shop-api", edgeIDs(env))
	}
	if len(site.Bindings) != 1 || site.Bindings[0].Field != "shop-api" || site.Bindings[0].Edge != "site:shop-web->shop-api" ||
		!jsonEqual(t, site.Bindings[0].Value, map[string]any{"url": "https://shop-api.staging.acme.dev"}) {
		t.Errorf("shop-web's bindings = %s", mustMarshal(t, site.Bindings))
	}
	var cors *ir.Binding
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.Field == ir.CORSField("shop-api") {
			cors = b
		}
	}
	if cors == nil || cors.CORSOf != "shop-api" || strings.Join(cors.Edges, ",") != "site:shop-web->shop-api" ||
		!jsonEqual(t, cors.Value, map[string]any{"origins": []any{"https://shop-web.staging.acme.dev"}}) {
		t.Fatalf("shop-api's CORS field = %s", mustMarshal(t, cors))
	}
	for _, b := range env.Deployable("Orders").Bindings {
		if b.CORSOf != "" {
			t.Errorf("Orders, which no site calls, has the CORS field %s", b.Field)
		}
	}
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout && strings.Contains(strings.Join(step.Deployables, ","), "shop-web") && step.Wave != 2 {
			t.Errorf("shop-web rolls out in wave %d, want 2, after shop-api", step.Wave)
		}
	}
}

// TestSiteNamedInExpose: a stack may name a site in expose, which changes
// nothing.
func TestSiteNamedInExpose(t *testing.T) {
	reg := assemble(t)
	s, services := siteShop()
	plain := mustResolve(t, reg, s, services, "Staging")
	s.Expose = append(s.Expose, stacktest.Of(stacktest.ShopWeb))
	named := mustResolve(t, reg, s, services, "Staging")
	if string(mustMarshal(t, plain)) != string(mustMarshal(t, named)) {
		t.Error("naming the site in expose changed the environment")
	}
}

// TestTwoSitesCallOneAPI: the CORS field lists each site's origin once, in
// the order of the site edges, and the server answers both.
func TestTwoSitesCallOneAPI(t *testing.T) {
	reg := assemble(t)
	s, services := siteShop()
	admin := ir.ServiceRef{Name: "shop-admin", Kind: ir.SchemaKindSite}
	services = append(services, stack.Service{
		Name: admin.Name, Kind: ir.SchemaKindSite, Calls: []ir.ServiceRef{stacktest.ShopAPI},
		Site: &ir.ResolvedSite{Dir: "web/shop-admin", Build: "build", Output: "dist"},
	})
	s.Deploy = append(s.Deploy, admin)
	env := mustResolve(t, reg, s, services, "Staging")
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.CORSOf == "" {
			continue
		}
		want := map[string]any{"origins": []any{"https://shop-admin.staging.acme.dev", "https://shop-web.staging.acme.dev"}}
		if !jsonEqual(t, b.Value, want) {
			t.Errorf("CORS field = %s", mustMarshal(t, b.Value))
		}
		return
	}
	t.Fatal("shop-api has no CORS field")
}

// TestSiteRefusals: a site that calls an API the stack does not expose, a
// site edge to a platform with no public address, an env key on a site, a
// handle of the wrong kind in a site's calls, and a config field that the
// CORS field takes.
func TestSiteRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("an unexposed API", func(t *testing.T) {
		s, services := siteShop()
		service(services, "shop-web").Calls = []ir.ServiceRef{stacktest.ShopOrders}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeSiteCallsUnexposed, "site shop-web calls shop-orders, whose server Orders the stack does not expose")
	})
	t.Run("an env key", func(t *testing.T) {
		s, services := siteShop()
		s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{
			Of: stacktest.Of(stacktest.ShopWeb), Env: map[string]ir.EnvValue{"API_URL": {Value: "x"}},
		})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env API_URL on site shop-web")
	})
	t.Run("a DB in calls", func(t *testing.T) {
		s, services := siteShop()
		service(services, "shop-web").Calls = []ir.ServiceRef{stacktest.ShopDB}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "site shop-web calls names shop-db, a DB service")
	})
	t.Run("a config field the CORS field takes", func(t *testing.T) {
		s, services := siteShop()
		api := service(services, "shop-api")
		api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SHOP_API_CORS_ORIGINS"})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeFieldCollision, "config field SHOP_API_CORS_ORIGINS of ShopApiConfig collides with SHOP_API_CORS, the CORS field of shop-api")
	})
	t.Run("a server with no public address", func(t *testing.T) {
		s, services := siteShop()
		reg := assemble(t, &privateRun{})
		for _, env := range s.Environments {
			env.Settings = append(env.Settings, &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopAPI), Platform: "private.run"})
		}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeLowering, "site shop-web calls shop-api, and platform private.run gives its server shop-api no public address")
	})
}

// privateRun registers a server platform with no public address, and a
// connector from the fake site platform to it.
type privateRun struct{}

func (*privateRun) Name() string { return "private" }
func (*privateRun) Register(r *registry.Registry) error {
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "private.run", Kind: ir.DeployableServer, Languages: []string{"GO"},
		NameOf:    func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf: func(ctx registry.PlatformContext) any { return "http://" + ctx.Deployable.Name },
		Lower:     func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	}); err != nil {
		return err
	}
	for _, c := range []registry.ConnectorSpec{
		{Name: "private.site-run", Edge: ir.EdgeSite, From: stacktest.SitePlatform, To: "private.run"},
		{Name: "private.sql", Edge: ir.EdgeSQL, From: "private.run", To: stacktest.SQLPlatform},
		{Name: "private.http", Edge: ir.EdgeHTTP, From: stacktest.RunPlatform, To: "private.run"},
	} {
		c.Connect = func(ctx registry.ConnectorContext) (registry.Connected, error) {
			return registry.Connected{Value: map[string]any{"url": "http://x"}}, nil
		}
		if err := r.RegisterConnector(c); err != nil {
			return err
		}
	}
	return nil
}

// jsonEqual compares two values by their JSON, references included.
func jsonEqual(t *testing.T, got, want any) bool {
	t.Helper()
	return string(mustMarshal(t, got)) == string(mustMarshal(t, want))
}

// TestDerivedVariablesJoinReferencedOrigins: a CORS field whose origins
// hold references is one variable, a concatenation of them with commas.
func TestDerivedVariablesJoinReferencedOrigins(t *testing.T) {
	origin := ir.Concat{"https://shop-web-", ir.Parameter("pr"), ".acme.dev"}
	vars, err := ir.DerivedVariables("SHOP_API_CORS", ir.CORSPolicy{Origins: []any{"http://127.0.0.1:1", origin, ir.Output{Resource: "lb", Name: "ip"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := ir.Concat{"http://127.0.0.1:1", ",", "https://shop-web-", ir.Parameter("pr"), ".acme.dev", ",", ir.Output{Resource: "lb", Name: "ip"}}
	if len(vars) != 1 || vars[0].Name != "SHOP_API_CORS_ORIGINS" || !jsonEqual(t, vars[0].Value, want) {
		t.Fatalf("variables = %+v, want SHOP_API_CORS_ORIGINS = %v", vars, want)
	}
}
