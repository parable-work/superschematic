package stack_test

import (
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// originShop returns siteShop with shop-web serving shop-api on its own
// origin (D91), and shop-api in no expose of the stack's.
func originShop() (*ir.Stack, []stack.Service) {
	s, services := siteShop()
	s.Expose = nil
	s.Origins = []*ir.Origin{{Site: stacktest.ShopWeb, APIs: []ir.ServiceRef{stacktest.ShopAPI}}}
	return s, services
}

// TestOriginServesAnAPIOnTheSitesOrigin: an origin puts shop-api on
// shop-web's origin (D91). The site records it under /api/shop-api with
// its server, the server records the site, the site edge derives the
// same-origin URL with the server's address as the upstream the fake site
// forwards to, shop-api needs no exposure and answers no CORS for the
// site, and the site still rolls out after it.
func TestOriginServesAnAPIOnTheSitesOrigin(t *testing.T) {
	reg := assemble(t)
	s, services := originShop()
	env := mustResolve(t, reg, s, services, "Staging")
	site, api := env.Deployable("shop-web"), env.Deployable("shop-api")
	if want := []*ir.SiteOriginAPI{{Service: "shop-api", Server: "shop-api", Path: "/api/shop-api"}}; !jsonEqual(t, site.Site.Origin, want) {
		t.Errorf("shop-web's origin = %s", mustMarshal(t, site.Site.Origin))
	}
	if api.Exposed || api.PublicAddress != nil || len(api.Origins) != 1 || api.Origins[0] != "shop-web" {
		t.Errorf("shop-api: exposed %t, public address %v, origins %v; want unexposed, none, [shop-web]", api.Exposed, api.PublicAddress, api.Origins)
	}
	want := map[string]any{
		"url":      "https://shop-web.staging.acme.dev/api/shop-api",
		"upstream": ir.Output{Resource: "shop-api.service", Name: "url"},
	}
	if len(site.Bindings) != 1 || site.Bindings[0].Edge != "site:shop-web->shop-api" || !jsonEqual(t, site.Bindings[0].Value, want) {
		t.Errorf("shop-web's bindings = %s, want %s", mustMarshal(t, site.Bindings), mustMarshal(t, want))
	}
	for _, b := range api.Bindings {
		if b.CORSOf != "" {
			t.Errorf("shop-api answers CORS for %s through %s, though shop-web calls it from its own origin", mustMarshal(t, b.Value), b.Field)
		}
	}
	routes := env.Resources.Resource("shop-web.site").Properties["routes"]
	if want := []any{map[string]any{"path": "/api/shop-api", "upstream": ir.Output{Resource: "shop-api.service", Name: "url"}}}; !jsonEqual(t, routes, want) {
		t.Errorf("shop-web's site routes = %s", mustMarshal(t, routes))
	}
	waves := map[string]int{}
	for _, step := range env.DeployOrder {
		for _, d := range step.Deployables {
			if step.Step == ir.StepRollout {
				waves[d] = step.Wave
			}
		}
	}
	if waves["shop-web"] <= waves["shop-api"] {
		t.Errorf("shop-web rolls out in wave %d, shop-api in %d; want the site after the server", waves["shop-web"], waves["shop-api"])
	}
}

// TestOriginWithoutAPIsServesEveryCall: an origin that lists no API serves
// every API its site calls, and a site with no domain gives each the
// same-origin URL on its own public address, a reference.
func TestOriginWithoutAPIsServesEveryCall(t *testing.T) {
	reg := assemble(t)
	s, services := originShop()
	s.Origins[0].APIs = nil
	staging := s.Environment("Staging")
	staging.Domain, staging.DNS = "", nil
	env := mustResolve(t, reg, s, services, "Staging")
	site := env.Deployable("shop-web")
	if len(site.Site.Origin) != 1 || site.Site.Origin[0].Service != "shop-api" {
		t.Fatalf("shop-web's origin = %s, want shop-api", mustMarshal(t, site.Site.Origin))
	}
	want := ir.Concat{ir.Output{Resource: "shop-web.site", Name: "url"}, "/api/shop-api"}
	if value, _ := site.Bindings[0].Value.(map[string]any); !jsonEqual(t, value["url"], want) {
		t.Errorf("shop-api's URL = %s, want %s", mustMarshal(t, value["url"]), mustMarshal(t, want))
	}
}

// TestOriginKeepsAnotherSitesCORS: shop-admin calls shop-api from its own
// origin, beside shop-web's origin, so shop-api, exposed again, answers
// CORS for shop-admin alone, and shop-admin's config names its public
// address.
func TestOriginKeepsAnotherSitesCORS(t *testing.T) {
	reg := assemble(t)
	s, services := originShop()
	s.Expose = []ir.DeployableRef{stacktest.Of(stacktest.ShopAPI)}
	admin := ir.ServiceRef{Name: "shop-admin", Kind: ir.SchemaKindSite}
	services = append(services, stack.Service{
		Name: admin.Name, Kind: ir.SchemaKindSite, Calls: []ir.ServiceRef{stacktest.ShopAPI},
		Site: &ir.ResolvedSite{Dir: "web/shop-admin", Build: "build", Output: "dist"},
	})
	s.Deploy = append(s.Deploy, admin)
	env := mustResolve(t, reg, s, services, "Staging")
	var cors *ir.Binding
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.CORSOf != "" {
			cors = b
		}
	}
	if cors == nil || len(cors.Edges) != 1 || cors.Edges[0] != "site:shop-admin->shop-api" ||
		!jsonEqual(t, cors.Value, map[string]any{"origins": []any{"https://shop-admin.staging.acme.dev"}}) {
		t.Fatalf("shop-api's CORS field = %s, want shop-admin's origin alone", mustMarshal(t, cors))
	}
	if value, _ := env.Deployable("shop-admin").Bindings[0].Value.(map[string]any); !jsonEqual(t, value, map[string]any{"url": "https://shop-api.staging.acme.dev"}) {
		t.Errorf("shop-admin's binding = %s, want shop-api's public address", mustMarshal(t, value))
	}
}

// TestOriginAcrossProviders: an API of a site's origin on another
// provider's platform is reached at its public address through the
// generic connector, which derives the same-origin URL with the public
// address as the upstream, so it must be exposed.
func TestOriginAcrossProviders(t *testing.T) {
	reg := assemble(t, &otherProvider{})
	// other.run reaches no fake bucket, so shop-api lists none here.
	place := func(s *ir.Stack, services []stack.Service) (*ir.Stack, []stack.Service) {
		s = stacktest.WithoutBucketSettings(s)
		for _, env := range s.Environments {
			env.Settings = append(env.Settings, &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopAPI), Platform: "other.run"})
		}
		return s, stacktest.WithoutBuckets(services)
	}
	s, services := place(originShop())
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeCrossProviderUnexposed, "site shop-web on fake.site (fake) serves shop-api on its origin, whose server shop-api on other.run (other) the stack does not expose")

	s, services = place(originShop())
	s.Expose = []ir.DeployableRef{stacktest.Of(stacktest.ShopAPI)}
	env := mustResolve(t, reg, s, services, "Staging")
	edge := edgeOf(env, "site:shop-web->shop-api")
	if edge == nil || edge.Connector != "generic.site" {
		t.Fatalf("edge site:shop-web->shop-api = %+v, want one the generic connector serves", edge)
	}
	want := map[string]any{"url": "https://shop-web.staging.acme.dev/api/shop-api", "upstream": "https://shop-api.other.test"}
	if !jsonEqual(t, env.Deployable("shop-web").Bindings[0].Value, want) {
		t.Errorf("shop-web's binding = %s, want %s", mustMarshal(t, env.Deployable("shop-web").Bindings[0].Value), mustMarshal(t, want))
	}
}

// TestOriginRefusals: an origin of a site the stack does not deploy, of a
// site another origin names, of an API the site does not call, of an API
// twice, of a handle of another kind, on a site platform that serves no
// origin, and a connector that derives another URL than the same-origin
// one.
func TestOriginRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("a site the stack does not deploy", func(t *testing.T) {
		s, services := originShop()
		s.Deploy = s.Deploy[:len(s.Deploy)-1]
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeOrigin, "origins[0] names site shop-web, which the stack does not deploy")
	})
	t.Run("two origins of one site", func(t *testing.T) {
		s, services := originShop()
		s.Origins = append(s.Origins, &ir.Origin{Site: stacktest.ShopWeb})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeOrigin, "origins[0] and origins[1] both name site shop-web")
	})
	t.Run("an API the site does not call", func(t *testing.T) {
		s, services := originShop()
		s.Origins[0].APIs = append(s.Origins[0].APIs, stacktest.ShopOrders)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeOrigin, "origins[0] serves shop-orders on the origin of site shop-web, which does not call it")
	})
	t.Run("an API twice", func(t *testing.T) {
		s, services := originShop()
		s.Origins[0].APIs = append(s.Origins[0].APIs, stacktest.ShopAPI)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeOrigin, "origins[0] lists shop-api twice")
	})
	t.Run("a DB where an API goes", func(t *testing.T) {
		s, services := originShop()
		s.Origins[0].APIs = []ir.ServiceRef{stacktest.ShopDB}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "origins[0] apis names shop-db, a DB service")
	})
	t.Run("a site platform that serves no origin", func(t *testing.T) {
		s, services := originShop()
		reg := assemble(t, &plainSite{})
		for _, env := range s.Environments {
			env.Settings = append(env.Settings, &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopWeb), Platform: "plain.site"})
		}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "site shop-web: platform plain.site serves no site's origin")
	})
	t.Run("a connector that derives another URL", func(t *testing.T) {
		s, services := originShop()
		reg := assemble(t, &plainSite{servesOrigin: true})
		for _, env := range s.Environments {
			env.Settings = append(env.Settings, &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopWeb), Platform: "plain.site"})
		}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeLowering, `connector plain.site-run on edge site:shop-web->shop-api derives the url "https://elsewhere" for shop-api`, `"https://shop-web.plain.test/api/shop-api"`)
	})
}

// plainSite registers a site platform of the fake provider, plain.site,
// which serves an origin when servesOrigin is set, with a connector to the
// fake server platform that derives one fixed URL.
type plainSite struct{ servesOrigin bool }

func (*plainSite) Name() string { return "plain" }

func (p *plainSite) Register(r *registry.Registry) error {
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "plain.site", Provider: stacktest.Provider, Kind: ir.DeployableSite, ServesOrigin: p.servesOrigin,
		NameOf:          func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf:       func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name + ".plain.test" },
		PublicAddressOf: func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name + ".plain.test" },
		Lower:           func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	}); err != nil {
		return err
	}
	return r.RegisterConnector(registry.ConnectorSpec{
		Name: "plain.site-run", Edge: ir.EdgeSite, From: "plain.site", To: stacktest.RunPlatform,
		Connect: func(registry.ConnectorContext) (registry.Connected, error) {
			return registry.Connected{Value: ir.SiteEndpoint{URL: "https://elsewhere"}}, nil
		},
	})
}

// TestSiteOriginURL: the same-origin URL of an API joins the site's public
// address, a string or a reference, and the API's path.
func TestSiteOriginURL(t *testing.T) {
	for _, tc := range []struct {
		site any
		want any
	}{
		{"https://shop-web.acme.dev", "https://shop-web.acme.dev/api/shop-api"},
		{"https://shop-web.acme.dev/", "https://shop-web.acme.dev/api/shop-api"},
		{ir.Concat{"https://shop-web-", ir.Parameter("pr"), ".acme.dev"}, ir.Concat{"https://shop-web-", ir.Parameter("pr"), ".acme.dev/api/shop-api"}},
		{ir.Concat{"http://", ir.Output{Resource: "lb", Name: "address"}}, ir.Concat{"http://", ir.Output{Resource: "lb", Name: "address"}, "/api/shop-api"}},
		{ir.Output{Resource: "site", Name: "url"}, ir.Concat{ir.Output{Resource: "site", Name: "url"}, "/api/shop-api"}},
	} {
		if got := ir.SiteOriginURL(tc.site, "shop-api"); !jsonEqual(t, got, tc.want) {
			t.Errorf("SiteOriginURL(%v) = %s, want %s", tc.site, mustMarshal(t, got), mustMarshal(t, tc.want))
		}
	}
}
