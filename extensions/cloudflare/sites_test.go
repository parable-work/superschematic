package cloudflare_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// storefrontWeb is the web fixture's site, which calls shop-storefront from
// the browser.
var storefrontWeb = ir.ServiceRef{Name: "storefront-web", Kind: ir.SchemaKindSite}

// sweepCarts is shop-storefront's job in the web fixture: nightly at three
// in UTC, four minutes a try, three tries, twelve minutes in all, within a
// Cron Trigger's fifteen.
var sweepCarts = ir.Job{Name: "SweepCarts", Schedule: "0 3 * * *", Timeout: "4m", Retries: 2}

// sweepCartsJob is the job's deployable, and sweepCartsRef what an
// environment's settings name it by.
const sweepCartsJob = "shop-storefront-sweep-carts"

var sweepCartsRef = ir.DeployableRef{Service: &stacktest.ShopStorefront, Job: "SweepCarts"}

// webServices are AcmeShop's services with shop-storefront's job
// SweepCarts and a setting with a default, which its job reads too, and
// the site storefront-web, which calls shop-storefront from the browser and
// falls back to its index.html.
func webServices() []stack.Service {
	def := func(v string) *string { return &v }
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == "shop-storefront" {
			services[i].Jobs = []ir.Job{sweepCarts}
			services[i].Config = &stack.Config{Type: "StorefrontConfig", Fields: []stack.ConfigField{
				{Name: "CART_TTL_DAYS", Required: true, Default: def("30")},
			}}
		}
	}
	return append(services, stack.Service{
		Name:  "storefront-web",
		Kind:  ir.SchemaKindSite,
		Calls: []ir.ServiceRef{stacktest.ShopStorefront},
		Site:  &ir.ResolvedSite{Dir: "web/storefront-web", Build: "build", Output: "dist", Fallback: "index.html"},
	})
}

// webStack deploys shop-storefront, exposed, its job SweepCarts, and the
// site storefront-web on Cloudflare: Staging at workers.dev, with a CPU
// limit for the job; Production under a domain on Cloudflare DNS, with the
// job's Workers Logs off and its schedule moved; and Preview, which extends
// Staging with a parameter, so its members run no schedule.
func webStack() *ir.Stack {
	values := map[string]any{"accountId": accountID, "workersSubdomain": "acme"}
	return &ir.Stack{
		Name:   "web-stack",
		Deploy: []ir.ServiceRef{stacktest.ShopStorefront, storefrontWeb},
		Expose: []ir.DeployableRef{stacktest.Of(stacktest.ShopStorefront)},
		Environments: []*ir.Environment{
			{
				Name: "Staging", Target: cloudflare.Target, Values: values,
				Settings: []*ir.DeployableSettings{{Of: sweepCartsRef, Values: map[string]any{"cpuMs": float64(30000)}}},
			},
			{
				Name: "Production", Target: cloudflare.Target, Values: values,
				Domain: "acme.dev",
				DNS:    &ir.DNSPlacement{Platform: cloudflare.DNS, Values: map[string]any{"zone": "acme.dev", "zoneId": zoneID}},
				Settings: []*ir.DeployableSettings{
					{Of: sweepCartsRef, Values: map[string]any{"logs": false}, Schedule: "30 2 * * *"},
				},
			},
			{Name: "Preview", Extends: "Staging", Parameters: []string{"pr"}},
		},
	}
}

// resolveWeb resolves an environment of the web fixture, failing the test
// when it does not resolve.
func resolveWeb(t *testing.T, reg *registry.Registry, env string) *ir.ResolvedEnvironment {
	t.Helper()
	out, errs := resolveOver(t, reg, webStack(), webServices(), env)
	if errs != nil {
		t.Fatal(errs)
	}
	return out
}

// TestWebGolden resolves the web fixture in each environment and checks
// each environment.json against its golden file, every node against its
// pinned type.
func TestWebGolden(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		t.Run(name, func(t *testing.T) {
			checkEnvironmentGolden(t, reg, resolveWeb(t, reg, name))
		})
	}
}

// siteBinding returns the binding named name of a version's bindings, or
// nil.
func siteBinding(version *ir.Resource, name string) map[string]any {
	for _, b := range version.Properties["bindings"].([]any) {
		if b := b.(map[string]any); b["name"] == name {
			return b
		}
	}
	return nil
}

// TestSiteNodes reads storefront-web's Worker in Staging: workers.dev on,
// since a site is always exposed, Workers Logs on and previews off; a
// version whose assets are the program's artifacts under the site's
// digest token, matched exactly, with the script first under
// /__superschematic/; the embedded script as its one module; the assets
// binding, the config naming shop-storefront's public address, and the
// fallback; and a deployment of all traffic to it. The site's origin is
// its workers.dev address, which shop-storefront's CORS field lists.
func TestSiteNodes(t *testing.T) {
	reg := assemble(t)
	env := resolveWeb(t, reg, "Staging")
	site := env.Deployable("storefront-web")
	if site.Platform != cloudflare.Sites || !site.Exposed {
		t.Fatalf("the site is placed as %+v", site)
	}
	if site.PublicAddress != "https://storefront-web.acme.workers.dev" {
		t.Errorf("the site's public address is %v", site.PublicAddress)
	}
	if got := jsonOf(t, node(t, env, "storefront-web.worker").Properties["subdomain"]); got != `{"enabled":true,"previewsEnabled":false}` {
		t.Errorf("the site's subdomain is %s", got)
	}
	version := node(t, env, "storefront-web.version")
	if version.Phase != ir.PhaseRollout || version.Properties["mainModule"] != cloudflare.SiteModule || version.Properties["compatibilityDate"] != cloudflare.CompatibilityDate {
		t.Errorf("the version is %s with %v", version.Phase, version.Properties)
	}
	if got, want := jsonOf(t, version.Properties["assets"]), `{"config":{"htmlHandling":"none","notFoundHandling":"none","runWorkerFirst":["/__superschematic/*"]},"directory":"artifacts/storefront-web/{site-digest}"}`; got != want {
		t.Errorf("the version's assets are\n%s, want\n%s", got, want)
	}
	modules := version.Properties["modules"].([]any)
	if len(modules) != 1 {
		t.Fatalf("the version has modules %v", modules)
	}
	module := modules[0].(map[string]any)
	script, err := base64.StdEncoding.DecodeString(module["contentBase64"].(string))
	if err != nil || module["name"] != cloudflare.SiteModule || module["contentType"] != "application/javascript+module" || string(script) != cloudflare.SiteScript() {
		t.Errorf("the version's module is %v (%v)", module, err)
	}
	if got := jsonOf(t, version.Properties["bindings"]); got != `[{"name":"ASSETS","type":"assets"},`+
		`{"name":"SITE_CONFIG","text":"{\"apis\":{\"shop-storefront\":{\"url\":\"https://shop-storefront.acme.workers.dev\"}}}","type":"plain_text"},`+
		`{"name":"SITE_FALLBACK","text":"index.html","type":"plain_text"}]` {
		t.Errorf("the version's bindings are %s", got)
	}
	if got := jsonOf(t, node(t, env, "storefront-web.deployment").Properties["versions"]); got != `[{"percentage":100,"versionId":{"$output":{"resource":"storefront-web.version","name":"id"}}}]` {
		t.Errorf("the deployment sends %s", got)
	}
	if env.Resources.Resource("storefront-web.domain") != nil {
		t.Error("a site without a domain has a custom domain")
	}
	if got := siteBinding(node(t, env, "shop-storefront.version"), "SHOP_STOREFRONT_CORS_ORIGINS"); got == nil || got["text"] != "https://storefront-web.acme.workers.dev" {
		t.Errorf("shop-storefront's CORS binding is %v", got)
	}
}

// TestSiteExposure: under a domain, the site has a custom domain for its
// host in the exposure step and no workers.dev, its origin there in
// shop-storefront's CORS field, and its config names shop-storefront under
// the domain; under a parameter, the member's config and origin carry the
// parameter's value; a site with no fallback has no SITE_FALLBACK.
func TestSiteExposure(t *testing.T) {
	reg := assemble(t)
	env := resolveWeb(t, reg, "Production")
	domain := node(t, env, "storefront-web.domain")
	if domain.Type != cloudflare.TypeWorkersCustomDomain || domain.Phase != ir.PhaseExposure || domain.Properties["hostname"] != "storefront-web.acme.dev" {
		t.Errorf("the custom domain is %s in %s with %v", domain.Type, domain.Phase, domain.Properties)
	}
	if got := jsonOf(t, node(t, env, "storefront-web.worker").Properties["subdomain"]); got != `{"enabled":false,"previewsEnabled":false}` {
		t.Errorf("under a domain the site's subdomain is %s", got)
	}
	version := node(t, env, "storefront-web.version")
	if got := siteBinding(version, cloudflare.SiteConfigBinding)["text"]; got != `{"apis":{"shop-storefront":{"url":"https://shop-storefront.acme.dev"}}}` {
		t.Errorf("Production's site config is %v", got)
	}
	if got := siteBinding(node(t, env, "shop-storefront.version"), "SHOP_STOREFRONT_CORS_ORIGINS")["text"]; got != "https://storefront-web.acme.dev" {
		t.Errorf("Production's CORS origins are %v", got)
	}
	if env.DNS == nil || len(env.DNS.Records) != 0 {
		t.Errorf("the environment's DNS is %+v, want cloudflare with no record", env.DNS)
	}

	preview := resolveWeb(t, reg, "Preview")
	version = node(t, preview, "storefront-web.version")
	if got := jsonOf(t, siteBinding(version, cloudflare.SiteConfigBinding)["text"]); got != `{"$concat":["{\"apis\":{\"shop-storefront\":{\"url\":\"https://shop-storefront-pr",{"$parameter":"pr"},".acme.workers.dev\"}}}"]}` {
		t.Errorf("Preview's site config is %s", got)
	}
	if got := jsonOf(t, siteBinding(node(t, preview, "shop-storefront.version"), "SHOP_STOREFRONT_CORS_ORIGINS")["text"]); got != `{"$concat":["https://storefront-web-pr",{"$parameter":"pr"},".acme.workers.dev"]}` {
		t.Errorf("Preview's CORS origins are %s", got)
	}

	services := webServices()
	services[len(services)-1].Site.Fallback = ""
	bare, errs := resolveOver(t, reg, webStack(), services, "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	if b := siteBinding(node(t, bare, "storefront-web.version"), cloudflare.SiteFallbackBinding); b != nil {
		t.Errorf("a site with no fallback binds %v", b)
	}
}

// TestSiteRefusals: a site without a domain needs the workers.dev
// subdomain bootstrap records; its Worker's name is one DNS label; and a
// site calls only an exposed API, which the core refuses.
func TestSiteRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("no workers.dev subdomain", func(t *testing.T) {
		s := webStack()
		s.Environments[0].Values = map[string]any{"accountId": accountID}
		_, errs := resolveOver(t, reg, s, webServices(), "Staging")
		// Without the subdomain neither the site nor the API it calls has a
		// public address, and the core says so before the site lowers.
		refusedWith(t, errs, stack.CodeLowering, "platform cloudflare.sites gives site storefront-web no public address, which the CORS field of each API it calls lists")
		refusedWith(t, errs, stack.CodeLowering, "platform cloudflare.workers gives its server shop-storefront no public address to reach it at")
	})
	t.Run("a name over 63 characters", func(t *testing.T) {
		long := strings.Repeat("storefront", 6) + "-web"
		services := webServices()
		services[len(services)-1].Name = long
		s := webStack()
		s.Deploy[1] = ir.ServiceRef{Name: long, Kind: ir.SchemaKindSite}
		_, errs := resolveOver(t, reg, s, services, "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "the Worker of site "+long+" would be named \""+long+"\", 64 characters")
	})
	t.Run("an API that is not exposed", func(t *testing.T) {
		s := webStack()
		s.Expose = nil
		_, errs := resolveOver(t, reg, s, webServices(), "Staging")
		if errs == nil || len(errs.Of(stack.CodeSiteCallsUnexposed)) == 0 {
			t.Errorf("a site calling an internal API: %v", errs)
		}
	})
}

// siteFiles are the files of a site's build, as the fixture's stand-in for
// the build writes them.
func siteFiles(version string) map[string]string {
	return map[string]string{
		"index.html":         "<!doctype html><title>" + version + "</title><script src=/app.js></script>\n",
		"app.js":             "console.log(\"" + version + "\");\n",
		"docs/index.html":    "<!doctype html><title>docs " + version + "</title>\n",
		"assets/logo-01.svg": "<svg/>\n",
	}
}

// webRun is a run of the web fixture's Staging.
func webRun(t *testing.T, f *fixture) registry.Run {
	t.Helper()
	return registry.Run{Environment: resolveWeb(t, f.reg, "Staging")}
}

// TestSiteStore publishes a site's build to the state bucket under the
// digest, each file with its SHA-256 and mode and the marker last, and
// writes no config; a second publish, and one with no files of a digest
// published, put nothing; the files fetch back. A digest never published
// with no files, and a site of another platform, are refused.
func TestSiteStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	store := f.target(t).Sites
	fetcher := store.(registry.ArtifactFetcher)
	run := webRun(t, f)
	dir := t.TempDir()
	files := siteFiles("v1")
	writeFiles(t, dir, files, "")
	digest := "sha256:" + strings.Repeat("5e", 32)
	prefix := cloudflare.SiteObjects("web-stack", "storefront-web", digest)
	if want := "superschematic/sites/web-stack/storefront-web/" + strings.Repeat("5e", 32) + "/"; prefix != want {
		t.Fatalf("the site's prefix is %s, want %s", prefix, want)
	}
	log := &bytes.Buffer{}
	publish := func(dir string, digest string) error {
		return store.Publish(ctx, registry.SitePublishRequest{Run: run, Site: "storefront-web", Digest: digest, Dir: dir, Config: []byte(`{"apis":{}}`), Log: log})
	}

	from := len(f.cloud.Changes())
	if err := publish(dir, digest); err != nil {
		t.Fatal(err)
	}
	changes := f.cloud.Changes()[from:]
	if len(changes) != len(files)+1 || changes[len(changes)-1] != "put "+cloudflare.StateBucket+"/"+prefix+"__superschematic/files" {
		t.Fatalf("the publish changed %q, want the four files and the marker last", changes)
	}
	for name, content := range files {
		data, meta, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, prefix+name)
		if err != nil || string(data) != content || meta["sha256"] != hexSum(content) || meta["mode"] != "644" {
			t.Errorf("%s is %q with %v, %v", name, data, meta, err)
		}
	}
	keys, err := f.cloud.ListObjects(ctx, accountID, cloudflare.StateBucket, cloudflare.SitePrefix)
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(keys, func(key string) bool { return strings.Contains(key, "config.json") }) {
		t.Errorf("the publish wrote a config: %v", keys)
	}
	if !strings.Contains(log.String(), "kept the files of site storefront-web in r2://superschematic-state/"+prefix) {
		t.Errorf("the publish logged %q", log)
	}

	from = len(f.cloud.Changes())
	if err := publish(dir, digest); err != nil {
		t.Fatal(err)
	}
	if err := publish("", digest); err != nil {
		t.Fatal(err)
	}
	if changes := f.cloud.Changes()[from:]; len(changes) != 0 {
		t.Errorf("publishing the digest again changed %q", changes)
	}

	dest := t.TempDir()
	if err := fetcher.Fetch(ctx, run, "storefront-web", digest, dest); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if data, err := os.ReadFile(filepath.Join(dest, filepath.FromSlash(name))); err != nil || string(data) != content {
			t.Errorf("the fetched %s is %q, %v", name, data, err)
		}
	}

	other := "sha256:" + strings.Repeat("cd", 32)
	if err := publish("", other); err == nil || !strings.Contains(err.Error(), "site storefront-web has no files "+other) {
		t.Errorf("a publish of a digest never published, with no files = %v", err)
	}
	if err := fetcher.Fetch(ctx, run, "storefront-web", other, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no files under "+other) {
		t.Errorf("a fetch of a digest never published = %v", err)
	}
	moved := *run.Environment
	moved.Deployables = slices.Clone(moved.Deployables)
	for i, d := range moved.Deployables {
		if d.Name == "storefront-web" {
			c := *d
			c.Platform = stacktest.SitePlatform
			moved.Deployables[i] = &c
		}
	}
	err = store.Publish(ctx, registry.SitePublishRequest{Run: registry.Run{Environment: &moved}, Site: "storefront-web", Digest: digest, Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "is on platform fake.site, not cloudflare.sites") {
		t.Errorf("a site of another platform = %v", err)
	}
}

// siteBuild stands in for bun in a deploy's Sources: the frozen install
// does nothing, and `bun run build` writes the site's files of the current
// version into its output.
type siteBuild struct {
	version string
	ran     []string
}

func (b *siteBuild) run(_ context.Context, dir string, _ io.Writer, name string, args ...string) error {
	b.ran = append(b.ran, filepath.Base(dir)+": "+name+" "+strings.Join(args, " "))
	if len(args) == 2 && args[0] == "run" && args[1] == "build" {
		out := filepath.Join(dir, "dist")
		if err := os.RemoveAll(out); err != nil {
			return err
		}
		for file, content := range siteFiles(b.version) {
			p := filepath.Join(out, filepath.FromSlash(file))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				return err
			}
		}
	}
	if len(args) == 2 && args[0] == "bundle.ts" {
		for file, content := range bundleFiles(b.version) {
			if err := os.WriteFile(filepath.Join(args[1], file), []byte(content), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// TestDeployServesTheSite deploys the web fixture's Staging: the deploy
// builds the site and bundles the server and the job, keeps the site's
// files in the state bucket, pins the Worker's assets to their digest and
// places them under the program, where the provider uploads them; the
// manifest records the site's digest. A deploy with --no-build fetches the
// files back from R2 and places them again.
func TestDeployServesTheSite(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	run := webRun(t, f)
	root := t.TempDir()
	for _, deployable := range []string{"shop-storefront", sweepCartsJob} {
		pkg := filepath.Join(root, "dist", "server", "web-stack", deployable)
		if err := os.MkdirAll(pkg, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "web", "storefront-web"), 0o755); err != nil {
		t.Fatal(err)
	}
	build := &siteBuild{version: "v1"}
	program := t.TempDir()
	log := &bytes.Buffer{}
	deploy := func(sources *stack.Sources) *stack.Manifest {
		t.Helper()
		m, err := stack.Deploy(ctx, stack.DeployOptions{
			Options: stack.Options{Registry: f.reg, Run: run, Dir: program, Log: log},
			Sources: sources,
		})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, log)
		}
		return m
	}

	m := deploy(&stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run})
	digest := m.Sites["storefront-web"]
	if digest == "" || m.Bundles["shop-storefront"] == nil || m.Bundles[sweepCartsJob] == nil {
		t.Fatalf("the manifest records sites %v and bundles %v", m.Sites, m.Bundles)
	}
	if got := strings.Join(build.ran, "; "); !strings.Contains(got, "storefront-web: bun run build") || !strings.Contains(got, sweepCartsJob+": bun bundle.ts") {
		t.Errorf("the deploy ran %s", got)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	prefix := cloudflare.SiteObjects("web-stack", "storefront-web", digest)
	keys, err := f.cloud.ListObjects(ctx, accountID, cloudflare.StateBucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != len(siteFiles("v1"))+1 {
		t.Errorf("the state bucket holds %v under the site's prefix", keys)
	}
	version := f.prov.Rendered().Resources.Resource("storefront-web.version")
	if got := version.Properties["assets"].(map[string]any)["directory"]; got != "artifacts/storefront-web/"+hexDigest {
		t.Errorf("the rendered version's assets are at %v", got)
	}
	for name, content := range siteFiles("v1") {
		placed, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "storefront-web", hexDigest, filepath.FromSlash(name)))
		if err != nil || string(placed) != content {
			t.Errorf("the placed %s holds %q, %v", name, placed, err)
		}
	}

	if err := os.RemoveAll(filepath.Join(program, ir.ArtifactsDir)); err != nil {
		t.Fatal(err)
	}
	from := len(f.cloud.Changes())
	again := deploy(nil)
	if again.Sites["storefront-web"] != digest {
		t.Errorf("--no-build serves %s, want the manifest's %s", again.Sites["storefront-web"], digest)
	}
	for _, change := range f.cloud.Changes()[from:] {
		if strings.Contains(change, "/sites/") {
			t.Errorf("--no-build changed %s", change)
		}
	}
	if placed, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "storefront-web", hexDigest, "docs", "index.html")); err != nil || string(placed) != siteFiles("v1")["docs/index.html"] {
		t.Errorf("--no-build placed docs/index.html as %q, %v", placed, err)
	}
}
