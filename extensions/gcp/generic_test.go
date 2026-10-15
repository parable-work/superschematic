package gcp_test

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// assembleMixed assembles gcp beside the acceptance extension's fake
// target, whose platforms are another provider's: the core's generic
// connector joins a Cloud Run deployable and a fake server (D63).
func assembleMixed(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), gcp.Extension{}, pulumiStub{}, &stacktest.Extension{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// mixedShop is the shop on gcp with EdgeShop's TypeScript APIs,
// shop-storefront and shop-pricing, on the fake target's edge platform
// (stacktest.EdgePlatform, TypeScript only, as Workers are), without
// shop-orders' worker: Orders, a Cloud Run service, and shop-orders' job,
// a Cloud Run job, call shop-pricing, an exposed fake server; and
// shop-storefront, a fake server, calls shop-api, an exposed Cloud Run
// service. No connector names a gcp platform and a fake one, so the
// generic connector serves each of those edges.
func mixedShop() (*ir.Stack, []stack.Service) {
	s := stacktest.WithEdge(shop(), stacktest.EdgePlatform)
	s.Name = "MixedShop"
	return s, stacktest.WithoutWorkers(stacktest.EdgeShop())
}

// TestMixedGolden resolves MixedShop in Staging and in Preview, whose
// members each have their own edge keys and the secrets that hold them.
func TestMixedGolden(t *testing.T) {
	reg := assembleMixed(t)
	s, services := mixedShop()
	for _, env := range []string{"Staging", "Preview"} {
		t.Run(env, func(t *testing.T) {
			checkGolden(t, reg, s, services, env)
		})
	}
}

// TestGenericConnectorOnGCP reads what the generic connector and the two
// platforms make of MixedShop's Staging (D63):
//
//   - a Cloud Run caller of a fake server: the edge's key node, a tls
//     Ed25519 private key; the callee's public address and a signed-token
//     credential in its variables; its private key, a secret member, from
//     a Secret Manager secret of the caller's own, whose version holds the
//     key node's PKCS#8 PEM, mounted with valueSource.secretKeyRef at that
//     version, and the caller's accessor grant; a Cloud Run job's the same;
//   - a fake caller of a Cloud Run server: the key node, the server's
//     public address, the key from a fake secret version;
//   - each callee's callers field: the caller as an issuer of its own, its
//     key the key node's SubjectPublicKeyInfo PEM, beside Google's issuer
//     on shop-api for the Cloud Run callers the gcp connector serves.
func TestGenericConnectorOnGCP(t *testing.T) {
	s, services := mixedShop()
	env := resolve(t, assembleMixed(t), s, services, "Staging")

	for _, tc := range []struct{ edge, connector string }{
		{"http:Orders->shop-pricing", stack.GenericHTTPConnector},
		{"http:" + stacktest.ShipOrdersJob + "->shop-pricing", stack.GenericHTTPConnector},
		{"http:shop-storefront->shop-api", stack.GenericHTTPConnector},
		{"http:Orders->shop-api", gcp.HTTPConnector},
	} {
		var got string
		for _, e := range env.Edges {
			if e.ID == tc.edge {
				got = e.Connector
			}
		}
		if got != tc.connector {
			t.Errorf("edge %s: connector %q, want %q", tc.edge, got, tc.connector)
		}
	}

	key := node(t, env, "Orders.calls.shop-pricing.key")
	if key.Type != stack.GenericKeyType || key.Properties["algorithm"] != "ED25519" || key.Phase != ir.PhaseInfrastructure {
		t.Errorf("key node = %s", mustJSON(t, key))
	}
	wantJSON(t, "Orders' SHOP_PRICING_SERVICE", binding(t, env, "Orders", "SHOP_PRICING_SERVICE").Value,
		`{"credential":{"audience":"shop-pricing","issuer":"Orders","key":{"$output":{"resource":"Orders.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}},"source":"signed-token"},"url":"https://shop-pricing.staging.acme.dev"}`)

	secret := node(t, env, "Orders.secret.SHOP_PRICING_SERVICE_CREDENTIAL_KEY")
	if secret.Type != gcp.TypeSecret || mustJSON(t, secret.Properties["secretId"]) != `"MixedShop-Staging-orders-SHOP_PRICING_SERVICE_CREDENTIAL_KEY"` {
		t.Errorf("secret = %s", mustJSON(t, secret))
	}
	version := node(t, env, "Orders.secret-version.SHOP_PRICING_SERVICE_CREDENTIAL_KEY")
	wantJSON(t, "secret version", version.Properties,
		`{"secret":{"$output":{"resource":"Orders.secret.SHOP_PRICING_SERVICE_CREDENTIAL_KEY","name":"id"}},"secretData":{"$output":{"resource":"Orders.calls.shop-pricing.key","name":"privateKeyPemPkcs8"}}}`)
	grant := node(t, env, "Orders.reads.SHOP_PRICING_SERVICE_CREDENTIAL_KEY")
	if grant.Type != gcp.TypeSecretIAMMember || grant.Properties["role"] != "roles/secretmanager.secretAccessor" {
		t.Errorf("accessor grant = %s", mustJSON(t, grant))
	}
	wantJSON(t, "Orders' key variable", envVar(t, node(t, env, "Orders.service"), "SHOP_PRICING_SERVICE_CREDENTIAL_KEY"),
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_KEY","valueSource":{"secretKeyRef":{"secret":{"$output":{"resource":"Orders.secret.SHOP_PRICING_SERVICE_CREDENTIAL_KEY","name":"secretId"}},"version":{"$output":{"resource":"Orders.secret-version.SHOP_PRICING_SERVICE_CREDENTIAL_KEY","name":"version"}}}}}`)
	wantJSON(t, "Orders' URL variable", envVar(t, node(t, env, "Orders.service"), "SHOP_PRICING_SERVICE_URL"),
		`{"name":"SHOP_PRICING_SERVICE_URL","value":"https://shop-pricing.staging.acme.dev"}`)
	job := stacktest.ShipOrdersJob
	wantJSON(t, "the job's key variable", envVar(t, node(t, env, job+".job"), "SHOP_PRICING_SERVICE_CREDENTIAL_KEY"),
		`{"name":"SHOP_PRICING_SERVICE_CREDENTIAL_KEY","valueSource":{"secretKeyRef":{"secret":{"$output":{"resource":"`+job+`.secret.SHOP_PRICING_SERVICE_CREDENTIAL_KEY","name":"secretId"}},"version":{"$output":{"resource":"`+job+`.secret-version.SHOP_PRICING_SERVICE_CREDENTIAL_KEY","name":"version"}}}}}`)

	wantJSON(t, "shop-pricing's SHOP_PRICING_CALLERS", binding(t, env, "shop-pricing", "SHOP_PRICING_CALLERS").Value,
		`{"issuers":[{"algorithms":["EdDSA"],"audience":"shop-pricing","callers":[{"deployable":"Orders","serves":["shop-orders"],"subject":"Orders"}],"issuer":"Orders","keys":[{"pem":{"$output":{"resource":"Orders.calls.shop-pricing.key","name":"publicKeyPem"}}}],"maxLifetimeSeconds":300},`+
			`{"algorithms":["EdDSA"],"audience":"shop-pricing","callers":[{"deployable":"`+job+`","serves":["shop-orders"],"subject":"`+job+`"}],"issuer":"`+job+`","keys":[{"pem":{"$output":{"resource":"`+job+`.calls.shop-pricing.key","name":"publicKeyPem"}}}],"maxLifetimeSeconds":300}]}`)

	// The fake caller of a Cloud Run server.
	wantJSON(t, "shop-storefront's SHOP_API_SERVICE", binding(t, env, "shop-storefront", "SHOP_API_SERVICE").Value,
		`{"credential":{"audience":"shop-api","issuer":"shop-storefront","key":{"$output":{"resource":"shop-storefront.calls.shop-api.key","name":"privateKeyPemPkcs8"}},"source":"signed-token"},"url":"https://shop-api.staging.acme.dev"}`)
	wantJSON(t, "shop-storefront's key variable", envVar(t, node(t, env, "shop-storefront.service"), "SHOP_API_SERVICE_CREDENTIAL_KEY"),
		`{"name":"SHOP_API_SERVICE_CREDENTIAL_KEY","secret":{"$output":{"resource":"shop-storefront.secret-version.SHOP_API_SERVICE_CREDENTIAL_KEY","name":"id"}}}`)
	callers := mustJSON(t, binding(t, env, "shop-api", "SHOP_API_CALLERS").Value)
	for _, want := range []string{
		`"issuer":"https://accounts.google.com"`,
		`{"algorithms":["EdDSA"],"audience":"shop-api","callers":[{"deployable":"shop-storefront","serves":["shop-storefront"],"subject":"shop-storefront"}],"issuer":"shop-storefront","keys":[{"pem":{"$output":{"resource":"shop-storefront.calls.shop-api.key","name":"publicKeyPem"}}}],"maxLifetimeSeconds":300}`,
	} {
		if !strings.Contains(callers, want) {
			t.Errorf("SHOP_API_CALLERS lacks %s:\n%s", want, callers)
		}
	}
}

// TestGenericConnectorRefusesAnUnexposedCallee: a Cloud Run server that
// calls a fake server the stack does not expose fails resolution, since
// the generic connector reaches a server only at its public address.
func TestGenericConnectorRefusesAnUnexposedCallee(t *testing.T) {
	s, services := mixedShop()
	s.Expose = []ir.DeployableRef{stacktest.Of(stacktest.ShopAPI)}
	_, err := stack.Resolve(assembleMixed(t), stack.Input{Stack: s, Services: services, Environment: "Staging"})
	var errs *stack.Errors
	if !asErrors(err, &errs) || !errs.Has(stack.CodeCrossProviderUnexposed) {
		t.Fatalf("Resolve = %v, want %s", err, stack.CodeCrossProviderUnexposed)
	}
	if msg := errs.Of(stack.CodeCrossProviderUnexposed)[0].Message; !strings.Contains(msg, "server Orders on gcp.cloudrun (gcp) calls shop-pricing, whose server shop-pricing on fake.edge (fake) the stack does not expose") {
		t.Errorf("message = %s", msg)
	}
}

// envVar returns the environment variable name of a Cloud Run service's,
// job's or fake server's one container.
func envVar(t *testing.T, res *ir.Resource, name string) any {
	t.Helper()
	var envs []any
	if template, ok := res.Properties["template"].(map[string]any); ok {
		if res.Type == gcp.TypeJob {
			template, _ = template["template"].(map[string]any)
		}
		containers, _ := template["containers"].([]any)
		container, _ := containers[0].(map[string]any)
		envs, _ = container["envs"].([]any)
	} else {
		envs, _ = res.Properties["env"].([]any)
	}
	for _, e := range envs {
		if m, _ := e.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("%s has no variable %s", res.ID, name)
	return nil
}

func asErrors(err error, target **stack.Errors) bool {
	errs, ok := err.(*stack.Errors)
	*target = errs
	return ok
}
