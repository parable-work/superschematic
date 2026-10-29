package cloudflare_test

import (
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// originWebStack is the web fixture with storefront-web serving
// shop-storefront, every API it calls, on its own origin (D91), and
// shop-storefront in no expose.
func originWebStack() *ir.Stack {
	s := webStack()
	s.Name = "origin-web-stack"
	s.Expose = nil
	s.Origins = []*ir.Origin{{Site: storefrontWeb}}
	return s
}

// TestOriginGolden resolves the origin web fixture in each environment and
// checks each environment.json against its golden file, every node against
// its pinned type.
func TestOriginGolden(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		t.Run(name, func(t *testing.T) {
			env, errs := resolveOver(t, reg, originWebStack(), webServices(), name)
			if errs != nil {
				t.Fatal(errs)
			}
			checkEnvironmentGolden(t, reg, env)
		})
	}
}

// TestOriginOnWorkers reads storefront-web serving shop-storefront on its
// origin in Staging, at workers.dev (D91): the site's version runs its
// script first for shop-storefront's path and every path under it, binds
// shop-storefront's Worker as SITE_API_SHOP_STOREFRONT, names that binding
// for the path in SITE_ORIGIN, and gives shop-storefront's URL on the
// site's own origin in SITE_CONFIG; the site's Worker waits for
// shop-storefront's, so a destroy removes the site first. shop-storefront,
// exposed by no one but the origin, has workers.dev off, no custom domain
// and no CORS origins. Under a domain, the config names shop-storefront on
// the site's host, and shop-storefront still has no host of its own.
func TestOriginOnWorkers(t *testing.T) {
	reg := assemble(t)
	env, errs := resolveOver(t, reg, originWebStack(), webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	version := node(t, env, "storefront-web.version")
	if got, want := jsonOf(t, version.Properties["assets"].(map[string]any)["config"]), `{"htmlHandling":"none","notFoundHandling":"none","runWorkerFirst":["/__superschematic/*","/api/shop-storefront","/api/shop-storefront/*"]}`; got != want {
		t.Errorf("the version's assets' config is\n%s, want\n%s", got, want)
	}
	if got, want := jsonOf(t, version.Properties["bindings"]), `[{"name":"ASSETS","type":"assets"},`+
		`{"name":"SITE_CONFIG","text":"{\"apis\":{\"shop-storefront\":{\"url\":\"https://origin-web-stack-staging-storefront-web.acme.workers.dev/api/shop-storefront\"}}}","type":"plain_text"},`+
		`{"name":"SITE_FALLBACK","text":"index.html","type":"plain_text"},`+
		`{"name":"SITE_ORIGIN","text":"[{\"path\":\"/api/shop-storefront\",\"binding\":\"SITE_API_SHOP_STOREFRONT\"}]","type":"plain_text"},`+
		`{"name":"SITE_API_SHOP_STOREFRONT","service":{"$output":{"resource":"shop-storefront.worker","name":"name"}},"type":"service"}]`; got != want {
		t.Errorf("the version's bindings are\n%s, want\n%s", got, want)
	}
	if got := node(t, env, "storefront-web.worker").DependsOn; len(got) != 1 || got[0] != "shop-storefront.worker" {
		t.Errorf("the site's Worker depends on %v, want shop-storefront's", got)
	}
	if got := jsonOf(t, node(t, env, "shop-storefront.worker").Properties["subdomain"]); got != `{"enabled":false,"previewsEnabled":false}` {
		t.Errorf("shop-storefront's subdomain is %s", got)
	}
	if b := siteBinding(node(t, env, "shop-storefront.version"), "SHOP_STOREFRONT_CORS_ORIGINS"); b != nil {
		t.Errorf("shop-storefront answers CORS for %v", b["text"])
	}

	production, errs := resolveOver(t, reg, originWebStack(), webServices(), "Production")
	if errs != nil {
		t.Fatal(errs)
	}
	if got := siteBinding(node(t, production, "storefront-web.version"), cloudflare.SiteConfigBinding)["text"]; got != `{"apis":{"shop-storefront":{"url":"https://storefront-web.acme.dev/api/shop-storefront"}}}` {
		t.Errorf("Production's site config is %v", got)
	}
	if production.Resources.Resource("shop-storefront.domain") != nil {
		t.Error("shop-storefront has a custom domain of its own")
	}
}

// TestOriginAcrossProvidersOnWorkers: an API of the site's origin on
// another provider's platform, exposed there, is reached at its public
// address, which the generic connector derives as the upstream and
// SITE_ORIGIN holds as the API's url, a reference the provider writes in;
// the version binds no Worker for it.
func TestOriginAcrossProvidersOnWorkers(t *testing.T) {
	reg := assemble(t)
	s := originWebStack()
	s.Expose = []ir.DeployableRef{stacktest.Of(stacktest.ShopStorefront)}
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: stacktest.Of(stacktest.ShopStorefront), Platform: stacktest.RunPlatform})
	env, errs := resolveOver(t, reg, s, webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	version := node(t, env, "storefront-web.version")
	if got, want := jsonOf(t, siteBinding(version, cloudflare.SiteOriginBinding)["text"]), `{"$concat":["[{\"path\":\"/api/shop-storefront\",\"url\":\"",{"$output":{"resource":"shop-storefront.service","name":"url"}},"\"}]"]}`; got != want {
		t.Errorf("SITE_ORIGIN is\n%s, want\n%s", got, want)
	}
	if b := siteBinding(version, cloudflare.SiteAPIBinding("shop-storefront")); b != nil {
		t.Errorf("the version binds %v for an API on another provider", b)
	}
	if got := siteBinding(version, cloudflare.SiteConfigBinding)["text"]; got != `{"apis":{"shop-storefront":{"url":"https://origin-web-stack-staging-storefront-web.acme.workers.dev/api/shop-storefront"}}}` {
		t.Errorf("the site's config is %v", got)
	}
}
