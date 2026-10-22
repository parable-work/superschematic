package cloudflare_test

import (
	"bytes"
	"maps"
	"os"
	"os/exec"
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

// TestJobNodes reads SweepCarts' Worker in Staging: a Worker of its own,
// named after the stack, the environment and the job's deployable, in
// infrastructure, with workers.dev off, since a job is never exposed, and
// Workers Logs on; a version that holds the job's bundle, the CPU limit
// its settings give and its API's config as bindings; a deployment of all
// traffic to it; and a Cron Trigger on the Worker, on the job's schedule,
// once the deployment is there.
func TestJobNodes(t *testing.T) {
	reg := assemble(t)
	env := resolveWeb(t, reg, "Staging")
	job := env.Deployable(sweepCartsJob)
	if job == nil || job.Kind != ir.DeployableJob || job.Platform != cloudflare.Cron || job.Artifact != ir.ArtifactBundle {
		t.Fatalf("the job is placed as %+v", job)
	}
	if job.Address != nil || job.PublicAddress != nil || job.ResourceName != "web-stack-staging-"+sweepCartsJob {
		t.Errorf("the job's names are %v, %v and %v", job.ResourceName, job.Address, job.PublicAddress)
	}
	worker := node(t, env, sweepCartsJob+".worker")
	if worker.Phase != ir.PhaseInfrastructure || worker.Properties["name"] != "web-stack-staging-"+sweepCartsJob {
		t.Errorf("the Worker is %s with %v", worker.Phase, worker.Properties)
	}
	if got := jsonOf(t, worker.Properties["subdomain"]) + jsonOf(t, worker.Properties["observability"]); got != `{"enabled":false,"previewsEnabled":false}{"enabled":true,"logs":{"enabled":true,"invocationLogs":true}}` {
		t.Errorf("the job's Worker has %s", got)
	}
	version := node(t, env, sweepCartsJob+".version")
	if version.Phase != ir.PhaseRollout || version.Properties["mainModule"] != cloudflare.MainModule {
		t.Errorf("the version is %s with %v", version.Phase, version.Properties)
	}
	var modules []string
	for _, m := range version.Properties["modules"].([]any) {
		m := m.(map[string]any)
		modules = append(modules, m["name"].(string))
		if m["contentFile"] != "artifacts/"+sweepCartsJob+"/{bundle-digest}/"+m["name"].(string) || m["contentSha256"] != "{bundle-sha256:"+m["name"].(string)+"}" {
			t.Errorf("module %v does not read its file from the bundle", m)
		}
	}
	if !slices.Equal(modules, []string{cloudflare.MainModule, cloudflare.SourceMap, cloudflare.WasmModule}) {
		t.Errorf("the version's modules are %v", modules)
	}
	if got := jsonOf(t, version.Properties["limits"]); got != `{"cpuMs":30000}` {
		t.Errorf("the version's limits are %s", got)
	}
	if _, ok := version.Properties["placement"]; ok {
		t.Error("the job's version has a placement")
	}
	if got := jsonOf(t, version.Properties["bindings"]); got != `[{"name":"CART_TTL_DAYS","text":"30","type":"plain_text"},{"name":"SUPERSCHEMATIC_BUNDLE","text":"{bundle-digest}","type":"plain_text"}]` {
		t.Errorf("the job's bindings are %s", got)
	}
	trigger := node(t, env, sweepCartsJob+".schedule")
	if trigger.Type != cloudflare.TypeWorkersCronTrigger || trigger.Phase != ir.PhaseRollout || !slices.Contains(trigger.DependsOn, sweepCartsJob+".deployment") {
		t.Errorf("the trigger is %s in %s after %v", trigger.Type, trigger.Phase, trigger.DependsOn)
	}
	if got := jsonOf(t, trigger.Properties); got != `{"accountId":"`+accountID+`","schedules":[{"cron":"0 3 * * *"}],"scriptName":{"$output":{"resource":"`+sweepCartsJob+`.worker","name":"name"}}}` {
		t.Errorf("the trigger is %s", got)
	}
	for _, res := range env.Resources.Resources {
		if slices.Contains(res.Owners, sweepCartsJob) && (res.Type == cloudflare.TypeWorkersCustomDomain || res.Type == cloudflare.TypeWorkersRoute) {
			t.Errorf("the job has %s %s", res.Type, res.ID)
		}
	}
}

// TestJobSchedules: Production moves the schedule and turns Workers Logs
// off; a member of Preview, which turns no schedule on, has its Worker,
// named with the parameter's value, and no trigger; an environment that
// turns the schedule off has none either.
func TestJobSchedules(t *testing.T) {
	reg := assemble(t)
	production := resolveWeb(t, reg, "Production")
	if got := jsonOf(t, node(t, production, sweepCartsJob+".schedule").Properties["schedules"]); got != `[{"cron":"30 2 * * *"}]` {
		t.Errorf("Production's schedules are %s", got)
	}
	if got := jsonOf(t, node(t, production, sweepCartsJob+".worker").Properties["observability"]); got != `{"enabled":false,"logs":{"enabled":false,"invocationLogs":false}}` {
		t.Errorf("with logs off, the job's observability is %s", got)
	}
	if _, ok := node(t, production, sweepCartsJob+".version").Properties["limits"]; ok {
		t.Error("Production's job has Staging's CPU limit")
	}

	preview := resolveWeb(t, reg, "Preview")
	if got := jsonOf(t, preview.Deployable(sweepCartsJob).ResourceName); got != `{"$concat":["web-stack-preview-shop-storefront-sweep-carts-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's Worker is named %s", got)
	}
	node(t, preview, sweepCartsJob+".worker")
	if preview.Resources.Resource(sweepCartsJob+".schedule") != nil {
		t.Error("a member of Preview, which turns no schedule on, has a trigger")
	}

	s := webStack()
	off := false
	s.Environments[0].Settings[0].Enabled = &off
	staging, errs := resolveOver(t, reg, s, webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	node(t, staging, sweepCartsJob+".version")
	if staging.Resources.Resource(sweepCartsJob+".schedule") != nil {
		t.Error("a job whose schedule is off has a trigger")
	}
}

// TestJobRefusals: a schedule in another time zone than UTC, a timeout
// times tries over Cloudflare's fifteen minutes for a scheduled run, a
// Worker's name over 63 characters, and a Go job are refused as
// unrealizable; a time zone with no schedule, and a job at the limit, are
// not; settings Cron does not take are refused.
func TestJobRefusals(t *testing.T) {
	reg := assemble(t)
	withJob := func(edit func(*ir.Job)) []stack.Service {
		services := webServices()
		for i := range services {
			if services[i].Name == "shop-storefront" {
				job := sweepCarts
				edit(&job)
				services[i].Jobs = []ir.Job{job}
			}
		}
		return services
	}
	t.Run("a time zone other than UTC", func(t *testing.T) {
		services := withJob(func(j *ir.Job) { j.TimeZone = "Europe/Paris" })
		_, errs := resolveOver(t, reg, webStack(), services, "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, `job shop-storefront-sweep-carts runs on "0 3 * * *" in time zone Europe/Paris, and a Cron Trigger reads its schedule in UTC alone`)
		// A member of Preview runs no schedule, so its time zone is moot.
		if _, errs := resolveOver(t, reg, webStack(), services, "Preview"); errs != nil {
			t.Errorf("a time zone with no schedule: %v", errs)
		}
		for _, zone := range []string{"UTC", "Etc/UTC"} {
			if _, errs := resolveOver(t, reg, webStack(), withJob(func(j *ir.Job) { j.TimeZone = zone }), "Staging"); errs != nil {
				t.Errorf("time zone %s: %v", zone, errs)
			}
		}
	})
	t.Run("tries over a scheduled run's limit", func(t *testing.T) {
		_, errs := resolveOver(t, reg, webStack(), withJob(func(j *ir.Job) { j.Timeout = "6m" }), "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "job shop-storefront-sweep-carts runs 3 tries of up to 6m each, 18m in all, within one run of its Cron Trigger, and Cloudflare stops a scheduled run after 15m")
		_, errs = resolveOver(t, reg, webStack(), withJob(func(j *ir.Job) { j.Timeout = ""; j.Retries = 1 }), "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "runs 2 tries of up to 10m each, 20m in all")
		if _, errs := resolveOver(t, reg, webStack(), withJob(func(j *ir.Job) { j.Timeout = "5m" }), "Staging"); errs != nil {
			t.Errorf("three tries of five minutes, fifteen in all: %v", errs)
		}
	})
	t.Run("a name over 63 characters", func(t *testing.T) {
		long := strings.Repeat("Sweep", 10) + "Carts"
		s := webStack()
		s.Environments[0].Settings = nil
		_, errs := resolveOver(t, reg, s, withJob(func(j *ir.Job) { j.Name = long }), "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "the Worker of job shop-storefront-"+ir.Kebab(long)+" would be named")
		refusedWith(t, errs, stack.CodeUnrealizable, "shorten the job's name, the longest")
	})
	t.Run("a Go job", func(t *testing.T) {
		s := webStack()
		s.Deploy = []ir.ServiceRef{stacktest.ShopOrders}
		s.Expose = nil
		s.Environments[0].Settings = nil
		s.Environments[1].Settings = nil
		services := stacktest.WithoutBuckets(stacktest.AcmeShop())
		_, errs := resolveOver(t, reg, s, services, "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "job shop-orders-ship-orders is a GO job, and platform cloudflare.cron runs only TYPESCRIPT")
	})
	t.Run("unknown settings", func(t *testing.T) {
		s := webStack()
		s.Environments[0].Settings[0].Values = map[string]any{"placement": "smart"}
		_, errs := resolveOver(t, reg, s, webServices(), "Staging")
		refusedWith(t, errs, stack.CodeInvalidSettings, "settings of "+sweepCartsJob+" on platform cloudflare.cron")
	})
}

// TestJobEdges: a job's edges are its API's, through the server's
// connectors from Cron. In the calls fixture with a job on shop-storefront,
// the job calls shop-pricing through a service binding with a key of its
// own, which the pricing API's callers field admits as a caller that
// serves shop-storefront; it reaches shop-media through an r2_bucket
// binding and a token of its own; and its Worker depends on the pricing
// Worker, so a destroy removes it first. In the D1 fixture with a job on
// reviews, the job binds reviews-db's D1 database.
func TestJobEdges(t *testing.T) {
	reg := assemble(t)
	services := callsServices()
	for i := range services {
		if services[i].Name == "shop-storefront" {
			services[i].Jobs = []ir.Job{sweepCarts}
		}
	}
	env, errs := resolveOver(t, reg, callsStack(), services, "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	for id, connector := range map[string]string{
		ir.EdgeID(ir.EdgeHTTP, sweepCartsJob, "shop-pricing"): cloudflare.CronHTTPConnector,
		ir.EdgeID(ir.EdgeBucket, sweepCartsJob, "shop-media"): cloudflare.CronR2Connector,
	} {
		i := slices.IndexFunc(env.Edges, func(e *ir.Edge) bool { return e.ID == id })
		if i < 0 || env.Edges[i].Connector != connector {
			t.Errorf("edge %s is not through %s", id, connector)
		}
	}
	bindings := bindingsOf(t, node(t, env, sweepCartsJob+".version"))
	for name, want := range map[string]string{
		"SHOP_PRICING_SERVICE":                `{"name":"SHOP_PRICING_SERVICE","service":{"$output":{"resource":"shop-pricing.worker","name":"name"}},"type":"service"}`,
		"SHOP_PRICING_SERVICE_CREDENTIAL_KEY": `{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_KEY","text":{"$output":{"resource":"` + sweepCartsJob + `.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}},"type":"secret_text"}`,
		"SHOP_MEDIA_BUCKET":                   `{"bucketName":{"$output":{"resource":"shop-media.bucket","name":"name"}},"name":"SHOP_MEDIA_BUCKET","type":"r2_bucket"}`,
		"SHOP_MEDIA_BUCKET_R2_TOKEN":          `{"name":"SHOP_MEDIA_BUCKET_R2_TOKEN","text":{"$output":{"resource":"` + sweepCartsJob + `.r2.shop-media.token","name":"value"}},"type":"secret_text"}`,
	} {
		if bindings[name] != want {
			t.Errorf("the job's binding %s is\n%s, want\n%s", name, bindings[name], want)
		}
	}
	callers := bindingsOf(t, node(t, env, "shop-pricing.version"))
	if got := callers["SHOP_PRICING_CALLERS_ISSUERS"]; !strings.Contains(got, `"text":"2"`) {
		t.Errorf("the pricing API admits %s issuers, want the server's and the job's", got)
	}
	if !slices.ContainsFunc(slices.Collect(maps.Values(callers)), func(b string) bool { return strings.Contains(b, `"text":"`+sweepCartsJob+`"`) }) {
		t.Errorf("the pricing API's callers field does not name the job:\n%v", callers)
	}
	if deps := node(t, env, sweepCartsJob+".worker").DependsOn; !slices.Contains(deps, "shop-pricing.worker") {
		t.Errorf("the job's Worker depends on %v, not on the pricing Worker", deps)
	}

	d1 := d1Services()
	for i := range d1 {
		if d1[i].Name == "reviews" {
			d1[i].Jobs = []ir.Job{{Name: "CompactReviews", Schedule: "0 5 * * *", Timeout: "2m"}}
		}
	}
	reviews, errs := resolveOver(t, reg, d1Stack(), d1, "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	got := bindingsOf(t, node(t, reviews, "reviews-compact-reviews.version"))["REVIEWS_DB_DATABASE"]
	if want := `{"id":{"$output":{"resource":"reviews-db.d1.reviews-db","name":"uuid"}},"name":"REVIEWS_DB_DATABASE","type":"d1"}`; got != want {
		t.Errorf("the job's D1 binding is %s, want %s", got, want)
	}
}

// TestPolicyNothingPublicOnAJob: the web fixture passes; a job's Worker
// with workers.dev on, a custom domain or a route is refused even when the
// job is marked exposed, and a site's Worker, always exposed, passes.
func TestPolicyNothingPublicOnAJob(t *testing.T) {
	target := newFixture(t).target(t)
	check := target.Policies[slices.IndexFunc(target.Policies, func(r registry.PolicyRule) bool { return r.Name == cloudflare.PolicyNothingPublic })].Check
	env := resolveWeb(t, assemble(t), "Production")
	if got := check(env); got != nil {
		t.Errorf("the web fixture: %v", got)
	}
	env.Deployable(sweepCartsJob).Exposed = true
	worker := node(t, env, sweepCartsJob+".worker")
	worker.Properties["subdomain"] = map[string]any{"enabled": true, "previewsEnabled": false}
	env.Resources.Resources = append(env.Resources.Resources,
		&ir.Resource{ID: sweepCartsJob + ".domain", Type: cloudflare.TypeWorkersCustomDomain, Owners: []string{sweepCartsJob}},
		&ir.Resource{ID: sweepCartsJob + ".route", Type: cloudflare.TypeWorkersRoute, Owners: []string{sweepCartsJob}},
	)
	got := strings.Join(check(env), "\n")
	for _, want := range []string{
		"Worker " + sweepCartsJob + ".worker has workers.dev on for " + sweepCartsJob + ", which is not exposed",
		"custom domain " + sweepCartsJob + ".domain reaches a Worker for " + sweepCartsJob + ", which is not exposed",
		"route " + sweepCartsJob + ".route reaches a Worker for " + sweepCartsJob + ", which is not exposed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy does not say %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "storefront-web") {
		t.Errorf("the policy refuses the site:\n%s", got)
	}
}

// TestWebCIGolden renders the web fixture's workflow with the core's
// GitHub Actions renderer and checks it against the golden file, which
// -update rewrites: check bundles the job for Workers beside the server,
// and builds the site; each deploy and preview job sets up Bun, since the
// deploy builds the site and the bundles on the runner (D64, D55).
func TestWebCIGolden(t *testing.T) {
	reg := assemble(t)
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	var envs []registry.CIEnvironment
	for _, name := range []string{"Staging", "Production", "Preview"} {
		envs = append(envs, reg.CIEnvironment(resolveWeb(t, reg, name)))
	}
	files, err := renderer.Render(registry.CIRequest{
		Stack: registry.CIStack{
			Name:                      "web-stack",
			Dir:                       "schemas/services/web-stack",
			ServicesRoot:              "schemas/services",
			SchemasRoot:               "schemas",
			OutputRoot:                "schemas/dist",
			PackageManager:            registry.PackageManagerBun,
			TypeScriptImplementations: map[string]string{"shop-storefront": "typescript/shop-storefront"},
			Sites:                     map[string]registry.CISite{"storefront-web": {Dir: "web/storefront-web", Build: "build"}},
		},
		Environments: envs,
		Options:      registry.CIOptions{}.WithDefaults(renderer),
		Version:      "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "web-stack.yml" {
		t.Fatalf("files = %+v", files)
	}
	golden := filepath.Join(goldenRoot, "ci", files[0].Path)
	if *update {
		if err := os.WriteFile(golden, files[0].Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(files[0].Data, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", golden)
	}
	for _, line := range []string{
		"name: Bundle server shop-storefront for Workers",
		"name: Bundle job " + sweepCartsJob + " for Workers",
		"run: bun bundle.ts --check",
	} {
		if !bytes.Contains(want, []byte(line)) {
			t.Errorf("the workflow has no %q", line)
		}
	}
	if bin, err := exec.LookPath("actionlint"); err == nil {
		if out, err := exec.Command(bin, "-no-color", golden).CombinedOutput(); err != nil {
			t.Errorf("actionlint %s: %v\n%s", golden, err, out)
		}
	}
}
