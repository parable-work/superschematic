package cloudflare_test

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// callsServices are AcmeShop's services with shop-storefront calling
// shop-pricing, an internal TypeScript API whose one operation only
// shop-storefront may call, and listing the bucket shop-media.
func callsServices() []stack.Service {
	def := func(v string) *string { return &v }
	services := stacktest.AcmeShop()
	for i := range services {
		if services[i].Name == "shop-storefront" {
			services[i].Calls = []ir.ServiceRef{shopPricing}
			services[i].Buckets = []ir.ServiceRef{stacktest.ShopMedia}
			services[i].Config = &stack.Config{Type: "StorefrontConfig", Fields: []stack.ConfigField{
				{Name: "LOG_LEVEL", Required: true, Default: def("info")},
			}}
		}
	}
	return append(services, stack.Service{
		Name:     "shop-pricing",
		Kind:     ir.SchemaKindAPI,
		Language: registry.APILanguageTypeScript,
		Operations: []stack.Operation{{
			Name:           "Quotes.price",
			ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"shop-storefront"}},
		}},
	})
}

// callsStack deploys two Workers, shop-storefront, exposed, and
// shop-pricing, internal, which the storefront calls through a service
// binding, and the bucket shop-media, which the storefront lists, on R2:
// Dev on the local target, where the bucket is the storage emulator's;
// Staging at workers.dev, its bucket in western Europe with objects
// deleted after 30 days; Production under a domain on Cloudflare DNS, its
// objects stored as infrequently accessed; Preview extends Staging with a
// parameter, so each member gets its own Workers and bucket.
func callsStack() *ir.Stack {
	values := map[string]any{"accountId": accountID, "workersSubdomain": "acme"}
	return &ir.Stack{
		Name:   "calls-stack",
		Deploy: []ir.ServiceRef{stacktest.ShopStorefront, shopPricing},
		Expose: []ir.DeployableRef{stacktest.Of(stacktest.ShopStorefront)},
		Environments: []*ir.Environment{
			{Name: "Dev", Target: "local"},
			{
				Name: "Staging", Target: cloudflare.Target, Values: values,
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopMedia), Values: map[string]any{"deleteAfterDays": float64(30), "locationHint": "weur"}},
				},
			},
			{
				Name: "Production", Target: cloudflare.Target, Values: values,
				Domain: "acme.dev",
				DNS:    &ir.DNSPlacement{Platform: cloudflare.DNS, Values: map[string]any{"zone": "acme.dev", "zoneId": zoneID}},
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopMedia), Values: map[string]any{"storageClass": "InfrequentAccess"}},
				},
			},
			{Name: "Preview", Extends: "Staging", Parameters: []string{"pr"}},
		},
	}
}

// resolveCalls resolves an environment of the calls fixture, failing the
// test on a refusal.
func resolveCalls(t *testing.T, reg *registry.Registry, name string) *ir.ResolvedEnvironment {
	t.Helper()
	env, errs := resolveOver(t, reg, callsStack(), callsServices(), name)
	if errs != nil {
		t.Fatal(errs)
	}
	return env
}

// TestCallsGolden resolves the calls fixture on Cloudflare in Staging,
// Production and Preview, and checks each environment.json against its
// golden file, every node against its pinned type.
func TestCallsGolden(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		t.Run(name, func(t *testing.T) {
			checkEnvironmentGolden(t, reg, resolveCalls(t, reg, name))
		})
	}
}

// derivedValue returns the value of the derived binding field of a
// deployable, in its JSON form.
func derivedValue(t *testing.T, env *ir.ResolvedEnvironment, deployable, field string) string {
	t.Helper()
	d := env.Deployable(deployable)
	if d == nil {
		t.Fatalf("environment %s has no deployable %s", env.Environment, deployable)
	}
	for _, b := range d.Bindings {
		if b.Field == field {
			return jsonOf(t, b.Value)
		}
	}
	t.Fatalf("%s has no binding %s", deployable, field)
	return ""
}

// bindingsOf returns a version's bindings by name, each in its JSON form.
func bindingsOf(t *testing.T, version *ir.Resource) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, b := range version.Properties["bindings"].([]any) {
		out[b.(map[string]any)["name"].(string)] = jsonOf(t, b)
	}
	return out
}

// TestWorkersCallWorkers reads the storefront's call to the internal
// pricing API in Staging (section 18.10):
//
//   - the edge's key, the generic connector's tls PrivateKey (D63), in
//     infrastructure;
//   - the storefront's endpoint: the pricing Worker's address, a service
//     binding named after the field to the pricing Worker's name, and a
//     signed token from the storefront to the pricing API with the key's
//     private key;
//   - the storefront's version binds the pricing Worker and holds the
//     private key as secret text, every other member as plain text;
//   - the pricing API's callers field admits the storefront with the key's
//     public key, EdDSA, for 300 seconds;
//   - the pricing Worker has no workers.dev, no custom domain and no public
//     address, so only the binding reaches it;
//   - the storefront's Worker depends on the pricing Worker, so a destroy
//     removes the caller first, and the waves roll the pricing API out
//     before the storefront.
func TestWorkersCallWorkers(t *testing.T) {
	reg := assemble(t)
	env := resolveCalls(t, reg, "Staging")
	key := node(t, env, "shop-storefront.calls.shop-pricing.key")
	if key.Type != stack.GenericKeyType || key.Phase != ir.PhaseInfrastructure || key.Properties["algorithm"] != "ED25519" {
		t.Errorf("the edge's key is %s in %s with %v", key.Type, key.Phase, key.Properties)
	}
	want := `{"binding":{"name":"SHOP_PRICING_SERVICE","worker":{"$output":{"resource":"shop-pricing.worker","name":"name"}}},` +
		`"credential":{"audience":"shop-pricing","issuer":"shop-storefront",` +
		`"key":{"$output":{"resource":"shop-storefront.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}},"source":"signed-token"},` +
		`"url":"https://calls-stack-staging-shop-pricing"}`
	if got := derivedValue(t, env, "shop-storefront", "SHOP_PRICING_SERVICE"); got != want {
		t.Errorf("the storefront's endpoint is\n%s, want\n%s", got, want)
	}

	bindings := bindingsOf(t, node(t, env, "shop-storefront.version"))
	for name, want := range map[string]string{
		"SHOP_PRICING_SERVICE":                     `{"name":"SHOP_PRICING_SERVICE","service":{"$output":{"resource":"shop-pricing.worker","name":"name"}},"type":"service"}`,
		"SHOP_PRICING_SERVICE_URL":                 `{"name":"SHOP_PRICING_SERVICE_URL","text":"https://calls-stack-staging-shop-pricing","type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_BINDING_NAME":        `{"name":"SHOP_PRICING_SERVICE_BINDING_NAME","text":"SHOP_PRICING_SERVICE","type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_BINDING_WORKER":      `{"name":"SHOP_PRICING_SERVICE_BINDING_WORKER","text":{"$output":{"resource":"shop-pricing.worker","name":"name"}},"type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_CREDENTIAL_SOURCE":   `{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_SOURCE","text":"signed-token","type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_CREDENTIAL_AUDIENCE": `{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_AUDIENCE","text":"shop-pricing","type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_CREDENTIAL_ISSUER":   `{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_ISSUER","text":"shop-storefront","type":"plain_text"}`,
		"SHOP_PRICING_SERVICE_CREDENTIAL_KEY":      `{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_KEY","text":{"$output":{"resource":"shop-storefront.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}},"type":"secret_text"}`,
	} {
		if bindings[name] != want {
			t.Errorf("the storefront's binding %s is\n%s, want\n%s", name, bindings[name], want)
		}
	}

	callers := bindingsOf(t, node(t, env, "shop-pricing.version"))
	text := func(value string) string { return `"text":` + value + `,"type":"plain_text"}` }
	for name, want := range map[string]string{
		"SHOP_PRICING_CALLERS_ISSUERS":                        text(`"1"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_ISSUER":               text(`"shop-storefront"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_AUDIENCE":             text(`"shop-pricing"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_ALGORITHMS":           text(`"EdDSA"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_KEYS":                 text(`"1"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_KEYS_0_PEM":           text(`{"$output":{"resource":"shop-storefront.calls.shop-pricing.key","name":"publicKeyPem"}}`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_MAX_LIFETIME_SECONDS": text(`"300"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS":              text(`"1"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_SUBJECT":    text(`"shop-storefront"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_DEPLOYABLE": text(`"shop-storefront"`),
		"SHOP_PRICING_CALLERS_ISSUERS_0_CALLERS_0_SERVES":     text(`"shop-storefront"`),
	} {
		if !strings.HasSuffix(callers[name], want) {
			t.Errorf("the pricing API's binding %s is %s, want it to end %s", name, callers[name], want)
		}
	}
	if len(callers) != 11 {
		t.Errorf("the pricing API has %d bindings, want its callers field's 11", len(callers))
	}

	pricing := env.Deployable("shop-pricing")
	if pricing.Exposed || pricing.PublicAddress != nil {
		t.Errorf("the internal pricing API is exposed at %v", pricing.PublicAddress)
	}
	if got := jsonOf(t, node(t, env, "shop-pricing.worker").Properties["subdomain"]); got != `{"enabled":false,"previewsEnabled":false}` {
		t.Errorf("the pricing Worker's subdomain is %s", got)
	}
	if env.Resources.Resource("shop-pricing.domain") != nil {
		t.Error("the internal pricing Worker has a custom domain")
	}
	if deps := node(t, env, "shop-storefront.worker").DependsOn; !slices.Contains(deps, "shop-pricing.worker") {
		t.Errorf("the storefront's Worker depends on %v, not on the pricing Worker", deps)
	}
	if deps := node(t, env, "shop-storefront.version").DependsOn; !slices.Contains(deps, "shop-pricing.worker") || !slices.Contains(deps, "shop-storefront.calls.shop-pricing.key") {
		t.Errorf("the storefront's version depends on %v", deps)
	}
	if deps := node(t, env, "shop-pricing.worker").DependsOn; len(deps) != 0 {
		t.Errorf("the pricing Worker depends on %v", deps)
	}

	waves := map[string]int{}
	for _, step := range env.DeployOrder {
		switch step.Step {
		case ir.StepInfrastructure:
			for _, id := range []string{"shop-pricing.worker", "shop-storefront.worker", "shop-storefront.calls.shop-pricing.key"} {
				if !slices.Contains(step.Resources, id) {
					t.Errorf("infrastructure does not apply %s: %v", id, step.Resources)
				}
			}
		case ir.StepRollout:
			for _, id := range step.Resources {
				waves[id] = step.Wave
			}
		}
	}
	if waves["shop-pricing.version"] != 1 || waves["shop-pricing.deployment"] != 1 || waves["shop-storefront.version"] != 2 || waves["shop-storefront.deployment"] != 2 {
		t.Errorf("the rollout waves are %v, want the pricing API's in 1 and the storefront's in 2", waves)
	}
}

// TestWorkersCallUnderAParameter: in Preview, the endpoint's address and
// each member's Worker carry the parameter's value, while the binding names
// the callee's Worker through its node's output.
func TestWorkersCallUnderAParameter(t *testing.T) {
	env := resolveCalls(t, assemble(t), "Preview")
	want := `{"binding":{"name":"SHOP_PRICING_SERVICE","worker":{"$output":{"resource":"shop-pricing.worker","name":"name"}}},` +
		`"credential":{"audience":"shop-pricing","issuer":"shop-storefront",` +
		`"key":{"$output":{"resource":"shop-storefront.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}},"source":"signed-token"},` +
		`"url":{"$concat":["https://calls-stack-preview-shop-pricing-pr",{"$parameter":"pr"}]}}`
	if got := derivedValue(t, env, "shop-storefront", "SHOP_PRICING_SERVICE"); got != want {
		t.Errorf("the member's endpoint is\n%s, want\n%s", got, want)
	}
	if got := jsonOf(t, node(t, env, "shop-pricing.worker").Properties["name"]); got != `{"$concat":["calls-stack-preview-shop-pricing-pr",{"$parameter":"pr"}]}` {
		t.Errorf("the member's pricing Worker is named %s", got)
	}
}

// TestWorkersCallInProcess: a server that serves both the storefront and
// the pricing API calls the pricing API in process: its endpoint is its
// own address, with no binding and no credential; the edge has no key,
// and the pricing API's callers field admits no issuer.
func TestWorkersCallInProcess(t *testing.T) {
	s := callsStack()
	s.Deployables = []*ir.DeployableDecl{{Name: "edge", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopStorefront, shopPricing}}}
	s.Expose = []ir.DeployableRef{{Deployable: "edge"}}
	env, errs := resolveOver(t, assemble(t), s, callsServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	if got := derivedValue(t, env, "edge", "SHOP_PRICING_SERVICE"); got != `{"url":"https://calls-stack-staging-edge"}` {
		t.Errorf("the in-process endpoint is %s", got)
	}
	for _, res := range env.Resources.Resources {
		if res.Type == stack.GenericKeyType {
			t.Errorf("a call in process has key %s", res.ID)
		}
	}
	bindings := bindingsOf(t, node(t, env, "edge.version"))
	if _, ok := bindings["SHOP_PRICING_SERVICE"]; ok {
		t.Errorf("a call in process binds a service: %s", bindings["SHOP_PRICING_SERVICE"])
	}
	if got := bindings["SHOP_PRICING_CALLERS_ISSUERS"]; !strings.Contains(got, `"text":"0"`) {
		t.Errorf("the pricing API's callers field admits %s", got)
	}
	if deps := node(t, env, "edge.worker").DependsOn; len(deps) != 0 {
		t.Errorf("the Worker depends on %v", deps)
	}
}
