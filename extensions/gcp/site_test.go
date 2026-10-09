package gcp_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// siteDigest names the files of shop-web the site tests publish.
const siteDigest = "sha256:" + "ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12cd34ef56ab12"

// siteHex is siteDigest's hex, the prefix of its files in the bucket.
var siteHex = strings.TrimPrefix(siteDigest, "sha256:")

// siteShop is the shop with shop-web (D55), and the services it resolves
// over, with shop-web's fallback as fallback gives it.
func siteShop(fallback string) (*ir.Stack, []stack.Service) {
	services := stacktest.WithoutWorkers(stacktest.SiteShop())
	for i, svc := range services {
		if svc.Site != nil {
			site := *svc.Site
			site.Fallback = fallback
			services[i].Site = &site
		}
	}
	return stacktest.WithSite(shop()), services
}

// TestSitePlatform reads shop-web in Staging (D55): a bucket named after
// the project, readable by allUsers, which the policy allows a site; a
// backend bucket with Cloud CDN; a URL map in the site's rollout step,
// after shop-api's, that rewrites every path to the build's prefix and
// serves the fallback for a 404 through an anchor; and the load balancer's
// frontend at the site's host, whose records go to Cloud DNS. The site's
// origin is in shop-api's CORS field, and the site's binding holds
// shop-api's public address, its host under the domain.
func TestSitePlatform(t *testing.T) {
	s, services := siteShop("index.html")
	env := resolve(t, assemble(t), s, services, "Staging")
	site := env.Deployable("shop-web")
	if site.Platform != gcp.Site || site.PublicAddress != "https://shop-web.staging.acme.dev" {
		t.Errorf("shop-web is on %s at %v", site.Platform, site.PublicAddress)
	}
	want := []string{
		"shop-web.address", "shop-web.anchor", "shop-web.backend", "shop-web.bucket", "shop-web.certificate",
		"shop-web.certificate-map", "shop-web.certificate-map-entry", "shop-web.dns-authorization",
		"shop-web.forwarding-rule", "shop-web.https-proxy", "shop-web.public-read", "shop-web.url-map",
	}
	got := ownedBy(env, "shop-web")
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("shop-web's nodes = %v, want %v", got, want)
	}
	bucket := node(t, env, "shop-web.bucket")
	if bucket.Properties["name"] != "acme-staging-shop-web" || bucket.Phase != ir.PhaseInfrastructure {
		t.Errorf("bucket %v in phase %s", bucket.Properties["name"], bucket.Phase)
	}
	read := node(t, env, "shop-web.public-read").Properties
	if read["member"] != "allUsers" || read["role"] != "roles/storage.objectViewer" {
		t.Errorf("public read grants %v to %v", read["role"], read["member"])
	}
	wantJSON(t, "the backend bucket's CDN", node(t, env, "shop-web.backend").Properties["cdnPolicy"], `{"cacheMode":"USE_ORIGIN_HEADERS"}`)
	urlMap := node(t, env, "shop-web.url-map")
	if urlMap.Phase != ir.PhaseRollout {
		t.Errorf("the URL map applies in phase %s", urlMap.Phase)
	}
	matcher := urlMap.Properties["pathMatchers"].([]any)[0].(map[string]any)
	wantJSON(t, "the fallback", matcher["defaultCustomErrorResponsePolicy"].(map[string]any)["errorResponseRules"],
		`[{"matchResponseCodes":["404"],"overrideResponseCode":200,"path":"/{site-digest}/index.html"}]`)
	rules := matcher["routeRules"].([]any)
	wantJSON(t, "the last route", rules[len(rules)-1].(map[string]any)["routeAction"], `{"urlRewrite":{"pathPrefixRewrite":"/{site-digest}/"}}`)
	var wave int
	for _, step := range env.DeployOrder {
		if slices.Contains(step.Resources, "shop-web.url-map") {
			wave = step.Wave
			if !slices.Contains(step.Deployables, "shop-web") {
				t.Errorf("the URL map applies in step %s %d, without shop-web", step.Step, step.Wave)
			}
		}
		if slices.Contains(step.Deployables, "shop-api") && step.Wave >= wave && wave > 0 {
			t.Errorf("shop-api rolls out in wave %d, not before shop-web's %d", step.Wave, wave)
		}
	}
	if wave < 2 {
		t.Errorf("shop-web's URL map applies in wave %d, want after shop-api's", wave)
	}
	wantJSON(t, "shop-web's binding", binding(t, env, "shop-web", "shop-api").Value, `{"url":"https://shop-api.staging.acme.dev"}`)
	wantJSON(t, "shop-api's CORS field", binding(t, env, "shop-api", "SHOP_API_CORS").Value, `{"origins":["https://shop-web.staging.acme.dev"]}`)
	if !slices.ContainsFunc(serviceEnvs(t, env, "shop-api.service"), func(e map[string]any) bool {
		return e["name"] == "SHOP_API_CORS_ORIGINS" && e["value"] == "https://shop-web.staging.acme.dev"
	}) {
		t.Errorf("shop-api's service has no SHOP_API_CORS_ORIGINS for shop-web: %v", serviceEnvs(t, env, "shop-api.service"))
	}
	var records []string
	for _, rec := range env.DNS.Records {
		if rec.Deployable == "shop-web" {
			records = append(records, rec.Type+" "+mustJSON(t, rec.Name))
		}
	}
	if want := []string{`A "shop-web.staging.acme.dev"`, `CNAME "_acme-challenge.shop-web.staging.acme.dev"`}; !slices.Equal(records, want) {
		t.Errorf("shop-web's records = %v, want %v", records, want)
	}
}

// serviceEnvs returns the environment variables of a Cloud Run service's
// container.
func serviceEnvs(t *testing.T, env *ir.ResolvedEnvironment, id string) []map[string]any {
	t.Helper()
	container := node(t, env, id).Properties["template"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	var out []map[string]any
	for _, e := range container["envs"].([]any) {
		out = append(out, e.(map[string]any))
	}
	return out
}

// TestSiteWithoutDomain checks a site in an environment with no domain:
// its load balancer serves its address over HTTP, on port 80, with no
// certificate and no record, and that address is its origin in shop-api's
// CORS field; shop-api is reached at its run.app URL.
func TestSiteWithoutDomain(t *testing.T) {
	s, services := siteShop("index.html")
	for _, env := range s.Environments {
		env.Domain, env.DNS = "", nil
	}
	env := resolve(t, assemble(t), s, services, "Staging")
	wantJSON(t, "shop-web's public address", env.Deployable("shop-web").PublicAddress,
		`{"$concat":["http://",{"$output":{"resource":"shop-web.address","name":"address"}}]}`)
	got := ownedBy(env, "shop-web")
	for _, id := range []string{"shop-web.http-proxy", "shop-web.forwarding-rule"} {
		if !slices.Contains(got, id) {
			t.Errorf("shop-web has no %s: %v", id, got)
		}
	}
	for _, id := range []string{"shop-web.https-proxy", "shop-web.certificate"} {
		if slices.Contains(got, id) {
			t.Errorf("shop-web has %s with no domain", id)
		}
	}
	if port := node(t, env, "shop-web.forwarding-rule").Properties["portRange"]; port != "80" {
		t.Errorf("shop-web's forwarding rule takes port %v", port)
	}
	if env.DNS != nil {
		t.Errorf("records with no domain: %+v", env.DNS)
	}
	wantJSON(t, "shop-web's binding", binding(t, env, "shop-web", "shop-api").Value,
		`{"url":{"$output":{"resource":"shop-api.service","name":"uri"}}}`)
	wantJSON(t, "shop-api's CORS field", binding(t, env, "shop-api", "SHOP_API_CORS").Value,
		`{"origins":[{"$concat":["http://",{"$output":{"resource":"shop-web.address","name":"address"}}]}]}`)
}

// TestSiteWithoutFallback checks that a site with no fallback has no
// anchor and no custom error response: a path with no file is Cloud
// Storage's 404.
func TestSiteWithoutFallback(t *testing.T) {
	s, services := siteShop("")
	env := resolve(t, assemble(t), s, services, "Staging")
	if slices.Contains(ownedBy(env, "shop-web"), "shop-web.anchor") {
		t.Error("shop-web has an anchor with no fallback")
	}
	matcher := node(t, env, "shop-web.url-map").Properties["pathMatchers"].([]any)[0].(map[string]any)
	if _, ok := matcher["defaultCustomErrorResponsePolicy"]; ok || len(matcher["routeRules"].([]any)) != 2 {
		t.Errorf("path matcher with no fallback: %s", mustJSON(t, matcher))
	}
}

// writeSite writes a site's build output: a page, a script and a nested
// asset.
func writeSite(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range map[string]string{
		"index.html":           "<!doctype html><script src=/index-dq37ra8d.js></script>",
		"index-dq37ra8d.js":    "console.log('shop')",
		"assets/logo-1a2b.svg": "<svg/>",
	} {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestSitePublisher publishes shop-web's files to its bucket in Staging:
// under the digest's hex, each with its content type and no-cache, the
// marker after them, and the config no-store. A second publish of the same
// digest puts no file again; a digest with no files and no directory, and
// a bucket the deploy has not created, are refused.
func TestSitePublisher(t *testing.T) {
	f := newDeployFixture(t, false)
	s, services := siteShop("index.html")
	env := resolve(t, f.reg, s, services, "Staging")
	target, _ := f.reg.Target(gcp.Target)
	ctx := context.Background()
	dir := writeSite(t)
	config := []byte(`{"apis":{"shop-api":{"url":"https://shop-api.staging.acme.dev"}}}`)
	req := registry.SitePublishRequest{Run: registry.Run{Environment: env}, Site: "shop-web", Digest: siteDigest, Dir: dir, Config: config}
	if err := target.Sites.Publish(ctx, req); err != nil {
		t.Fatal(err)
	}
	bucket := "acme-staging-shop-web/" + siteHex + "/"
	for object, want := range map[string][2]string{
		"index.html":                   {"text/html; charset=utf-8", "no-cache"},
		"index-dq37ra8d.js":            {"text/javascript; charset=utf-8", "no-cache"},
		"assets/logo-1a2b.svg":         {"image/svg+xml", "no-cache"},
		"__superschematic/files":       {"text/plain; charset=utf-8", "no-store"},
		"__superschematic/config.json": {"application/json", "no-store"},
	} {
		if got := f.cloud.served[bucket+object]; got != want {
			t.Errorf("%s is served as %q, want %q", object, got, want)
		}
	}
	if got := string(f.cloud.objects[bucket+"__superschematic/config.json"]); got != string(config) {
		t.Errorf("config = %s", got)
	}
	delete(f.cloud.objects, bucket+"index.html")
	if err := target.Sites.Publish(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.cloud.objects[bucket+"index.html"]; ok {
		t.Error("a second publish of the same files put them again")
	}
	req.Digest, req.Dir = "sha256:"+strings.Repeat("0", 64), ""
	if err := target.Sites.Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "has no files") {
		t.Errorf("publish of a digest with no files: %v", err)
	}
	f.cloud.absent["acme-staging-shop-web"] = true
	req.Dir = dir
	if err := target.Sites.Publish(ctx, req); err == nil || !strings.Contains(err.Error(), "creates its bucket") {
		t.Errorf("publish to a bucket that does not exist: %v", err)
	}
}

// TestPublishToAMember publishes to the bucket of a member of Preview,
// named with the parameter's value.
func TestPublishToAMember(t *testing.T) {
	f := newDeployFixture(t, false)
	s, services := siteShop("index.html")
	env := resolve(t, f.reg, s, services, "Preview")
	target, _ := f.reg.Target(gcp.Target)
	run := registry.Run{Environment: env, Parameters: map[string]string{"pr": "12"}}
	if err := target.Sites.Publish(context.Background(), registry.SitePublishRequest{Run: run, Site: "shop-web", Digest: siteDigest, Dir: writeSite(t)}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.cloud.objects["acme-staging-shop-web-pr12/"+siteHex+"/index.html"]; !ok {
		t.Error("the member's files are not in acme-staging-shop-web-pr12")
	}
}

// TestDeploySiteOnGCP deploys Staging with shop-web's files from an
// earlier publish (`--site`): the deploy writes the site's config, with
// shop-api's public address, before the rollout step that switches the
// URL map to the files' prefix, and the manifest records the digest.
func TestDeploySiteOnGCP(t *testing.T) {
	f := newDeployFixture(t, true)
	f.cloud.log = f.prov
	s, services := siteShop("index.html")
	env := resolve(t, f.reg, s, services, "Staging")
	ctx := context.Background()
	if _, err := f.cloud.EnsureSecret(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY"); err != nil {
		t.Fatal(err)
	}
	if err := f.cloud.AddSecretVersion(ctx, "acme-staging", "Shop-PaymentsSecrets-STRIPE_KEY", []byte("sk")); err != nil {
		t.Fatal(err)
	}
	target, _ := f.reg.Target(gcp.Target)
	if err := target.Sites.Publish(ctx, registry.SitePublishRequest{Run: registry.Run{Environment: env}, Site: "shop-web", Digest: siteDigest, Dir: writeSite(t)}); err != nil {
		t.Fatal(err)
	}
	m, err := stack.Deploy(ctx, stack.DeployOptions{
		Options: stack.Options{Registry: f.reg, Run: registry.Run{Environment: env}, Dir: t.TempDir()},
		Images:  shopImages("acme-staging", 1),
		Sites:   map[string]string{"shop-web": siteDigest},
		Planner: shopPlanner,
	})
	if err != nil {
		t.Fatal(err)
	}
	var steps []string
	for _, call := range f.prov.Calls() {
		step := strings.SplitN(call, ":", 2)[0]
		if strings.HasPrefix(call, "write ") {
			step = call
		}
		if !strings.HasPrefix(step, "outputs") && !strings.HasPrefix(step, "render") {
			steps = append(steps, step)
		}
	}
	config := "write gs://acme-staging-shop-web/" + siteHex + "/__superschematic/config.json"
	want := []string{"write gs://acme-staging-shop-web/" + siteHex + "/__superschematic/files",
		"apply infrastructure", "migrate expand shop-db", "apply rollout 1", config, "apply rollout 2", "migrate contract shop-db", "apply exposure"}
	if !slices.Equal(steps, want) {
		t.Errorf("ran %q, want %q", steps, want)
	}
	urlMap := f.prov.Rendered().Resources.Resource("shop-web.url-map")
	if got := mustJSON(t, urlMap.Properties); strings.Contains(got, ir.SiteDigestToken) || !strings.Contains(got, `"pathPrefixRewrite":"/`+siteHex+`/"`) {
		t.Errorf("the URL map is not pinned to %s: %s", siteHex, got)
	}
	var cfg struct {
		APIs map[string]struct{ URL string } `json:"apis"`
	}
	if err := json.Unmarshal(f.cloud.objects["acme-staging-shop-web/"+siteHex+"/__superschematic/config.json"], &cfg); err != nil {
		t.Fatal(err)
	}
	if got := cfg.APIs["shop-api"].URL; got != "https://shop-api.staging.acme.dev" {
		t.Errorf("the config gives shop-api at %q", got)
	}
	if m.Sites["shop-web"] != siteDigest {
		t.Errorf("manifest sites = %v", m.Sites)
	}
}
