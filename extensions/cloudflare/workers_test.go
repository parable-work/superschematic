package cloudflare_test

import (
	"bytes"
	"encoding/json"
	"errors"
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

// storeID is the fixture's Secrets Store, as bootstrap records it.
const storeID = "5ec0000000000000000000000000000a"

// shopPricing is an internal TypeScript API of the Workers fixture, which
// only a service binding would reach.
var shopPricing = ir.ServiceRef{Name: "shop-pricing", Kind: ir.SchemaKindAPI}

// workersServices are AcmeShop's services with shop-storefront's config,
// a secret, a setting with a default and one a preview sets from its
// parameter, and shop-pricing, an internal TypeScript API.
func workersServices() []stack.Service {
	def := func(v string) *string { return &v }
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == "shop-storefront" {
			services[i].Config = &stack.Config{Type: "StorefrontConfig", Fields: []stack.ConfigField{
				{Name: "PAYMENTS_KEY", Required: true, Secret: true},
				{Name: "LOG_LEVEL", Required: true, Default: def("info")},
				{Name: "PREVIEW_ID"},
			}}
		}
	}
	return append(services, stack.Service{
		Name:       "shop-pricing",
		Kind:       ir.SchemaKindAPI,
		Language:   registry.APILanguageTypeScript,
		Operations: []stack.Operation{{Name: "Quotes.price"}},
	})
}

// workersStack deploys shop-storefront, exposed, and shop-pricing,
// internal, on Workers: Staging at workers.dev with a CPU limit and Smart
// Placement for the storefront; Production under a domain on Cloudflare
// DNS, with Workers Logs off and a compatibility date of its own; Preview
// extends Staging with a parameter, which the storefront's PREVIEW_ID
// reads.
func workersStack() *ir.Stack {
	staging := map[string]any{"accountId": accountID, "workersSubdomain": "acme", "secretsStoreId": storeID}
	return &ir.Stack{
		Name:   "workers-stack",
		Deploy: []ir.ServiceRef{stacktest.ShopStorefront, shopPricing},
		Expose: []ir.DeployableRef{stacktest.Of(stacktest.ShopStorefront)},
		Environments: []*ir.Environment{
			{
				Name: "Staging", Target: cloudflare.Target, Values: staging,
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopStorefront), Values: map[string]any{"cpuMs": float64(50), "placement": "smart"}},
				},
			},
			{
				Name: "Production", Target: cloudflare.Target, Values: staging,
				Domain: "acme.dev",
				DNS:    &ir.DNSPlacement{Platform: cloudflare.DNS, Values: map[string]any{"zone": "acme.dev", "zoneId": zoneID}},
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopStorefront), Values: map[string]any{"logs": false, "compatibilityDate": "2026-10-01"}},
				},
			},
			{
				Name: "Preview", Extends: "Staging", Parameters: []string{"pr"},
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopStorefront), Env: map[string]ir.EnvValue{"PREVIEW_ID": {Parameter: "pr"}}},
				},
			},
		},
	}
}

// storefrontStack is examples/acme-shop's storefront-stack, its local Dev
// left out: shop-storefront alone, exposed, in Staging on Cloudflare with
// the docs' placeholder account and no domain, at workers.dev under the
// subdomain bootstrap records.
func storefrontStack() *ir.Stack {
	return &ir.Stack{
		Name:   "storefront-stack",
		Deploy: []ir.ServiceRef{stacktest.ShopStorefront},
		Expose: []ir.DeployableRef{stacktest.Of(stacktest.ShopStorefront)},
		Environments: []*ir.Environment{{
			Name: "Staging", Target: cloudflare.Target,
			Values: map[string]any{"accountId": accountID, "workersSubdomain": "acme"},
		}},
	}
}

// resolveOver resolves an environment of s over services, or returns the
// resolution's failures.
func resolveOver(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) (*ir.ResolvedEnvironment, *stack.Errors) {
	t.Helper()
	out, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: env})
	if err == nil {
		return out, nil
	}
	var errs *stack.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Resolve returned %T: %v", err, err)
	}
	return nil, errs
}

// refusedWith checks resolution failed with code in a message that holds
// want.
func refusedWith(t *testing.T, errs *stack.Errors, code stack.Code, want string) {
	t.Helper()
	if errs == nil {
		t.Fatalf("resolution succeeded, want a %s failure saying %q", code, want)
	}
	for _, err := range errs.Of(code) {
		if strings.Contains(err.Message, want) {
			return
		}
	}
	t.Fatalf("no %s failure says %q:\n%v", code, want, errs)
}

// checkEnvironmentGolden writes env's environment.json and compares it with
// the golden file under goldenRoot, which -update rewrites, and checks
// every node against the pinned schema of its type, as resolution did.
func checkEnvironmentGolden(t *testing.T, reg *registry.Registry, env *ir.ResolvedEnvironment) {
	t.Helper()
	for _, res := range env.Resources.Resources {
		ok, err := reg.ValidateResource(res)
		if !ok || err != nil {
			t.Errorf("node %s (%s) is not valid against the pin: %v, %v", res.ID, res.Type, ok, err)
		}
	}
	path, err := stack.Write(t.TempDir(), env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	golden := stack.EnvironmentPath(goldenRoot, env.Stack, env.Environment)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from %s; run with -update and review the diff", path, golden)
	}
}

// TestWorkersGolden resolves the Workers fixture in each environment and
// the storefront's stack in Staging, and checks each environment.json
// against its golden file, every node against its pinned type.
func TestWorkersGolden(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		t.Run(name, func(t *testing.T) {
			env, errs := resolveOver(t, reg, workersStack(), workersServices(), name)
			if errs != nil {
				t.Fatal(errs)
			}
			checkEnvironmentGolden(t, reg, env)
		})
	}
	t.Run("storefront-stack", func(t *testing.T) {
		env, errs := resolveOver(t, reg, storefrontStack(), stacktest.AcmeShop(), "Staging")
		if errs != nil {
			t.Fatal(errs)
		}
		checkEnvironmentGolden(t, reg, env)
	})
}

// node returns env's node id, failing the test without it.
func node(t *testing.T, env *ir.ResolvedEnvironment, id string) *ir.Resource {
	t.Helper()
	res := env.Resources.Resource(id)
	if res == nil {
		var ids []string
		for _, r := range env.Resources.Resources {
			ids = append(ids, r.ID)
		}
		t.Fatalf("environment %s has no node %s: %v", env.Environment, id, ids)
	}
	return res
}

// jsonOf is v in its JSON form, for comparing nodes' properties.
func jsonOf(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestWorkerNodes reads the storefront's and the pricing API's Worker in
// Staging: a Worker in infrastructure, Workers Logs on, workers.dev on for
// the exposed storefront alone and previews off; a version in the rollout
// that holds the bundle's three files under the program's artifacts with
// their pinned hashes, the pinned compatibility date and the settings'
// CPU limit and placement; and a deployment of all traffic to it. The
// storefront's public address is at workers.dev; the pricing API, which
// only a service binding reaches, has none, and no custom domain.
func TestWorkerNodes(t *testing.T) {
	reg := assemble(t)
	env, errs := resolveOver(t, reg, workersStack(), workersServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	store := env.Deployable("shop-storefront")
	if store.Artifact != ir.ArtifactBundle || store.Platform != cloudflare.Workers {
		t.Fatalf("the storefront is placed as %+v", store)
	}
	if store.PublicAddress != "https://shop-storefront.acme.workers.dev" || store.Address != "https://shop-storefront" {
		t.Errorf("the storefront's addresses are %v and %v", store.PublicAddress, store.Address)
	}
	if pricing := env.Deployable("shop-pricing"); pricing.PublicAddress != nil {
		t.Errorf("the internal pricing API has public address %v", pricing.PublicAddress)
	}

	worker := node(t, env, "shop-storefront.worker")
	if worker.Phase != ir.PhaseInfrastructure {
		t.Errorf("the Worker is in %s", worker.Phase)
	}
	if got, want := jsonOf(t, worker.Properties["subdomain"]), `{"enabled":true,"previewsEnabled":false}`; got != want {
		t.Errorf("the storefront's subdomain is %s, want %s", got, want)
	}
	if got, want := jsonOf(t, worker.Properties["observability"]), `{"enabled":true,"logs":{"enabled":true,"invocationLogs":true}}`; got != want {
		t.Errorf("the storefront's observability is %s, want %s", got, want)
	}
	if got := jsonOf(t, node(t, env, "shop-pricing.worker").Properties["subdomain"]); got != `{"enabled":false,"previewsEnabled":false}` {
		t.Errorf("the pricing API's subdomain is %s", got)
	}

	version := node(t, env, "shop-storefront.version")
	if version.Phase != ir.PhaseRollout || version.Properties["mainModule"] != cloudflare.MainModule ||
		version.Properties["compatibilityDate"] != cloudflare.CompatibilityDate {
		t.Errorf("the version is %s with %v", version.Phase, version.Properties)
	}
	if got, want := jsonOf(t, version.Properties["workerId"]), `{"$output":{"resource":"shop-storefront.worker","name":"id"}}`; got != want {
		t.Errorf("the version's workerId is %s, want %s", got, want)
	}
	var modules []string
	for _, m := range version.Properties["modules"].([]any) {
		m := m.(map[string]any)
		modules = append(modules, m["name"].(string))
		if m["contentFile"] != "artifacts/shop-storefront/{bundle-digest}/"+m["name"].(string) || m["contentSha256"] != "{bundle-sha256:"+m["name"].(string)+"}" {
			t.Errorf("module %v does not read its file from the bundle", m)
		}
	}
	if !slices.Equal(modules, []string{"main.js", "main.js.map", "superscalar_wasm_bg.wasm"}) {
		t.Errorf("the version's modules are %v", modules)
	}
	if got := jsonOf(t, version.Properties["limits"]) + jsonOf(t, version.Properties["placement"]); got != `{"cpuMs":50}{"mode":"smart"}` {
		t.Errorf("the version's limits and placement are %s", got)
	}
	deployment := node(t, env, "shop-storefront.deployment")
	if got, want := jsonOf(t, deployment.Properties["versions"]), `[{"percentage":100,"versionId":{"$output":{"resource":"shop-storefront.version","name":"id"}}}]`; got != want {
		t.Errorf("the deployment sends %s, want %s", got, want)
	}
	if env.Resources.Resource("shop-storefront.domain") != nil || env.Resources.Resource("shop-pricing.domain") != nil {
		t.Error("a Worker without a domain has a custom domain")
	}
	if env.DNS != nil && len(env.DNS.Records) > 0 {
		t.Errorf("the environment writes DNS records %v", env.DNS.Records)
	}
}

// TestWorkerBindings reads the storefront's config as its version's
// bindings in Staging and Preview: the literal LOG_LEVEL and, in Preview,
// PREVIEW_ID from the parameter are plain text; PAYMENTS_KEY is the
// Secrets Store's secret of the environment the member belongs to.
func TestWorkerBindings(t *testing.T) {
	reg := assemble(t)
	bindings := func(name string) string {
		env, errs := resolveOver(t, reg, workersStack(), workersServices(), name)
		if errs != nil {
			t.Fatal(errs)
		}
		return jsonOf(t, node(t, env, "shop-storefront.version").Properties["bindings"])
	}
	want := `[{"name":"LOG_LEVEL","text":"info","type":"plain_text"},` +
		`{"name":"PAYMENTS_KEY","secretName":"workers-stack-Staging-StorefrontConfig-PAYMENTS_KEY","storeId":"` + storeID + `","type":"secrets_store_secret"}]`
	if got := bindings("Staging"); got != want {
		t.Errorf("Staging's bindings are\n%s, want\n%s", got, want)
	}
	want = `[{"name":"LOG_LEVEL","text":"info","type":"plain_text"},` +
		`{"name":"PAYMENTS_KEY","secretName":"workers-stack-Preview-StorefrontConfig-PAYMENTS_KEY","storeId":"` + storeID + `","type":"secrets_store_secret"},` +
		`{"name":"PREVIEW_ID","text":{"$parameter":"pr"},"type":"plain_text"}]`
	if got := bindings("Preview"); got != want {
		t.Errorf("Preview's bindings are\n%s, want\n%s", got, want)
	}
}

// TestWorkerExposure: under a domain on Cloudflare DNS, the exposed
// storefront has a custom domain for its host in the exposure step and no
// workers.dev, and the DNS platform writes no record, since Cloudflare
// writes the custom domain's; under a parameter, each member's Worker and
// host carry the parameter's value.
func TestWorkerExposure(t *testing.T) {
	reg := assemble(t)
	env, errs := resolveOver(t, reg, workersStack(), workersServices(), "Production")
	if errs != nil {
		t.Fatal(errs)
	}
	domain := node(t, env, "shop-storefront.domain")
	if domain.Type != cloudflare.TypeWorkersCustomDomain || domain.Phase != ir.PhaseExposure || domain.Properties["hostname"] != "shop-storefront.acme.dev" {
		t.Errorf("the custom domain is %s in %s with %v", domain.Type, domain.Phase, domain.Properties)
	}
	if got := jsonOf(t, domain.Properties["service"]); got != `{"$output":{"resource":"shop-storefront.worker","name":"name"}}` {
		t.Errorf("the custom domain routes to %s", got)
	}
	if got := jsonOf(t, node(t, env, "shop-storefront.worker").Properties["subdomain"]); got != `{"enabled":false,"previewsEnabled":false}` {
		t.Errorf("under a domain the storefront's subdomain is %s", got)
	}
	if got := env.Deployable("shop-storefront").PublicAddress; got != "https://shop-storefront.acme.dev" {
		t.Errorf("the storefront's public address is %v", got)
	}
	if env.DNS == nil || len(env.DNS.Records) != 0 {
		t.Errorf("the environment's DNS is %+v, want cloudflare with no record", env.DNS)
	}
	worker := node(t, env, "shop-storefront.worker")
	if got := jsonOf(t, worker.Properties["observability"]); got != `{"enabled":false,"logs":{"enabled":false,"invocationLogs":false}}` {
		t.Errorf("with logs off, the storefront's observability is %s", got)
	}
	if got := node(t, env, "shop-storefront.version").Properties["compatibilityDate"]; got != "2026-10-01" {
		t.Errorf("the version's compatibility date is %v", got)
	}

	preview, errs := resolveOver(t, reg, workersStack(), workersServices(), "Preview")
	if errs != nil {
		t.Fatal(errs)
	}
	if got := jsonOf(t, preview.Deployable("shop-storefront").ResourceName); got != `{"$concat":["shop-storefront-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's Worker is named %s", got)
	}
	if got := jsonOf(t, preview.Deployable("shop-storefront").PublicAddress); got != `{"$concat":["https://shop-storefront-pr",{"$parameter":"pr"},".acme.workers.dev"]}` {
		t.Errorf("the member's public address is %s", got)
	}
}

// TestWorkersRefusals: Workers runs TypeScript alone, a Worker's name is
// one DNS label, and the identity runtime does not run on Workers, each
// refused as unrealizable; an exposed server without a domain needs the
// workers.dev subdomain, and a secret binding the Secrets Store, that
// bootstrap records.
func TestWorkersRefusals(t *testing.T) {
	reg := assemble(t)
	t.Run("a Go server", func(t *testing.T) {
		s := storefrontStack()
		s.Deploy = []ir.ServiceRef{stacktest.ShopAPI}
		s.Expose = nil
		services := stacktest.WithoutBuckets(stacktest.AcmeShop())
		for i := range services {
			if services[i].Name == "shop-api" {
				services[i].AuthDB = nil
				services[i].Config = nil
			}
		}
		_, errs := resolveOver(t, reg, s, services, "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "server shop-api is a GO server, and platform cloudflare.workers runs only TYPESCRIPT")
	})
	t.Run("a name over 63 characters", func(t *testing.T) {
		s := storefrontStack()
		long := strings.Repeat("storefront", 6) + "-edge"
		s.Deployables = []*ir.DeployableDecl{{Name: long, Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopStorefront}}}
		s.Expose = []ir.DeployableRef{{Deployable: long}}
		_, errs := resolveOver(t, reg, s, stacktest.AcmeShop(), "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "the Worker of server "+long+" would be named \""+long+"\", 65 characters, and a Worker's name is at most 63")
	})
	t.Run("a name under a parameter", func(t *testing.T) {
		s := workersStack()
		name := strings.Repeat("s", 60)
		s.Deployables = []*ir.DeployableDecl{{Name: name, Kind: ir.DeployableServer, Serves: []ir.ServiceRef{shopPricing}}}
		_, errs := resolveOver(t, reg, s, workersServices(), "Preview")
		refusedWith(t, errs, stack.CodeUnrealizable, "would be named with 63 characters before its parameter values")
		if _, errs := resolveOver(t, reg, s, workersServices(), "Staging"); errs != nil {
			t.Errorf("a 60-character name without a parameter: %v", errs)
		}
	})
	t.Run("an API over the user model", func(t *testing.T) {
		services := stacktest.AcmeShop()
		for i := range services {
			if services[i].Name == "shop-storefront" {
				services[i].Identity = true
			}
		}
		_, errs := resolveOver(t, reg, storefrontStack(), services, "Staging")
		refusedWith(t, errs, stack.CodeUnrealizable, "server shop-storefront serves shop-storefront, an API over the user model, which authenticates with the identity runtime (D50), and platform cloudflare.workers runs none: the TypeScript identity runtime hashes passwords with node:crypto's argon2")
	})
	t.Run("no workers.dev subdomain", func(t *testing.T) {
		s := storefrontStack()
		s.Environments[0].Values = map[string]any{"accountId": accountID}
		_, errs := resolveOver(t, reg, s, stacktest.AcmeShop(), "Staging")
		refusedWith(t, errs, stack.CodeLowering, "server shop-storefront is exposed without a domain, at workers.dev, and environment Staging records no workersSubdomain; run stack bootstrap Staging")
		// An internal server needs none.
		s.Expose = nil
		if _, errs := resolveOver(t, reg, s, stacktest.AcmeShop(), "Staging"); errs != nil {
			t.Errorf("an internal server without a subdomain: %v", errs)
		}
	})
	t.Run("no Secrets Store", func(t *testing.T) {
		s := workersStack()
		s.Environments[0].Values = map[string]any{"accountId": accountID, "workersSubdomain": "acme"}
		_, errs := resolveOver(t, reg, s, workersServices(), "Staging")
		refusedWith(t, errs, stack.CodeLowering, "server shop-storefront reads secret StorefrontConfig.PAYMENTS_KEY from the account's Secrets Store, and environment Staging records no secretsStoreId; run stack bootstrap Staging")
	})
	t.Run("unknown settings", func(t *testing.T) {
		s := workersStack()
		s.Environments[0].Settings[0].Values = map[string]any{"minInstances": float64(1)}
		_, errs := resolveOver(t, reg, s, workersServices(), "Staging")
		refusedWith(t, errs, stack.CodeInvalidSettings, "settings of shop-storefront on platform cloudflare.workers")
	})
}

// TestPolicyNothingPublic: a Worker with workers.dev on, a custom domain
// and a route are refused for a deployable that is not exposed, a job's
// or none at all, and pass for an exposed server; the Workers fixture
// passes.
func TestPolicyNothingPublic(t *testing.T) {
	target := newFixture(t).target(t)
	check := target.Policies[slices.IndexFunc(target.Policies, func(r registry.PolicyRule) bool { return r.Name == cloudflare.PolicyNothingPublic })].Check
	env, errs := resolveOver(t, assemble(t), workersStack(), workersServices(), "Production")
	if errs != nil {
		t.Fatal(errs)
	}
	if got := check(env); got != nil {
		t.Errorf("the Workers fixture: %v", got)
	}
	env.Deployables = append(env.Deployables, &ir.ResolvedDeployable{Name: "nightly", Kind: ir.DeployableJob, Exposed: true})
	for _, res := range []*ir.Resource{
		{ID: "shop-pricing.open", Type: cloudflare.TypeWorker, Owners: []string{"shop-pricing"}, Properties: map[string]any{"subdomain": map[string]any{"enabled": true}}},
		{ID: "shop-pricing.domain", Type: cloudflare.TypeWorkersCustomDomain, Owners: []string{"shop-pricing"}},
		{ID: "nightly.route", Type: cloudflare.TypeWorkersRoute, Owners: []string{"nightly"}},
		{ID: "stray.domain", Type: cloudflare.TypeWorkersCustomDomain},
	} {
		env.Resources.Resources = append(env.Resources.Resources, res)
	}
	got := strings.Join(check(env), "\n")
	for _, want := range []string{
		"Worker shop-pricing.open has workers.dev on for shop-pricing, which is not exposed",
		"custom domain shop-pricing.domain reaches a Worker for shop-pricing, which is not exposed",
		"route nightly.route reaches a Worker for nightly, which is not exposed",
		"custom domain stray.domain reaches a Worker for no deployable, which is not exposed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the policy does not say %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "\n") != 3 {
		t.Errorf("the policy found more than the four:\n%s", got)
	}
}

// TestWorkersCIGolden renders the Workers fixture's workflow with the
// core's GitHub Actions renderer and checks it against the golden file,
// which -update rewrites: check type-checks each server and bundles each
// for Workers with bundle.ts --check; each deploy and preview job sets up
// Bun and installs the workspace before it deploys, since the deploy runs
// bundle.ts on the runner, and signs in with the account's token; a plan
// job builds nothing and does neither (D64).
func TestWorkersCIGolden(t *testing.T) {
	reg := assemble(t)
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	var envs []registry.CIEnvironment
	for _, name := range []string{"Staging", "Production", "Preview"} {
		env, errs := resolveOver(t, reg, workersStack(), workersServices(), name)
		if errs != nil {
			t.Fatal(errs)
		}
		envs = append(envs, reg.CIEnvironment(env))
	}
	files, err := renderer.Render(registry.CIRequest{
		Stack: registry.CIStack{
			Name:           "workers-stack",
			Dir:            "schemas/services/workers-stack",
			ServicesRoot:   "schemas/services",
			SchemasRoot:    "schemas",
			OutputRoot:     "schemas/dist",
			PackageManager: registry.PackageManagerBun,
			TypeScriptImplementations: map[string]string{
				"shop-pricing":    "typescript/shop-pricing",
				"shop-storefront": "typescript/shop-storefront",
			},
		},
		Environments: envs,
		Options:      registry.CIOptions{}.WithDefaults(renderer),
		Version:      "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "workers-stack.yml" {
		t.Fatalf("files = %+v", files)
	}
	golden := filepath.Join(goldenRoot, "ci", files[0].Path)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
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
		"name: Bundle server shop-pricing for Workers",
		"name: Bundle server shop-storefront for Workers",
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
