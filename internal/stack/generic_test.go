package stack_test

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// otherProvider registers the platforms of a second provider beside the
// fake one: other.run, a server platform whose public address is
// https://<name>.other.test, other.dark, one with no public address, and a
// sql connector from each to the fake database platform, as a Workers
// target's Hyperdrive connector would join a database of another cloud.
// No connector joins a fake server and one of its own, so the generic
// connector serves those edges (D63). exact adds one, from fake.run to
// other.run, which wins; literalKey makes it derive its key as a literal.
type otherProvider struct {
	exact      bool
	literalKey bool
}

func (*otherProvider) Name() string { return "other" }

func (o *otherProvider) Register(r *registry.Registry) error {
	for _, spec := range []registry.PlatformSpec{
		{Name: "other.run", PublicAddressOf: func(ctx registry.PlatformContext) any { return "https://" + ctx.Deployable.Name + ".other.test" }},
		{Name: "other.dark"},
	} {
		spec.Provider = "other"
		spec.Kind = ir.DeployableServer
		spec.Languages = []string{registry.APILanguageGo, registry.APILanguageTypeScript}
		spec.NameOf = func(ctx registry.PlatformContext) any { return ctx.Deployable.Name }
		spec.AddressOf = func(ctx registry.PlatformContext) any { return "http://" + ctx.Deployable.Name }
		spec.Lower = func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil }
		if err := r.RegisterPlatform(spec); err != nil {
			return err
		}
	}
	sql := func(registry.ConnectorContext) (registry.Connected, error) {
		return registry.Connected{Value: ir.DatabaseConnection{URL: "postgres://shop-db/shop_db"}}, nil
	}
	connectors := []registry.ConnectorSpec{
		{Name: "other.run-sql", Edge: ir.EdgeSQL, From: "other.run", To: stacktest.SQLPlatform, Connect: sql},
		{Name: "other.dark-sql", Edge: ir.EdgeSQL, From: "other.dark", To: stacktest.SQLPlatform, Connect: sql},
	}
	if o.exact {
		key := any(ir.Output{Resource: "Orders.account", Name: "key"})
		if o.literalKey {
			key = "-----BEGIN PRIVATE KEY-----"
		}
		connectors = append(connectors, registry.ConnectorSpec{
			Name: "other.fake-run", Edge: ir.EdgeHTTP, From: stacktest.RunPlatform, To: "other.run",
			Connect: func(ctx registry.ConnectorContext) (registry.Connected, error) {
				return registry.Connected{
					Value: ir.ServiceEndpoint{URL: "https://exact", Credential: &ir.ServiceCredential{
						Source: ir.CredentialSignedToken, Audience: ctx.To.Name, Issuer: ctx.From.Name, Key: key,
					}},
					Callee: ir.ServiceAuthIssuer{
						Issuer: ctx.From.Name, Audience: ctx.To.Name, Algorithms: []string{ir.AlgorithmEdDSA},
						Keys:    []ir.ServiceAuthKey{{PEM: ir.Output{Resource: "Orders.account", Name: "public"}}},
						Callers: []ir.ServiceAuthCaller{{Subject: ctx.From.Name, Deployable: ctx.From.Name, Serves: []string{"shop-orders"}}},
					},
				}, nil
			},
		})
	}
	for _, spec := range connectors {
		if err := r.RegisterConnector(spec); err != nil {
			return err
		}
	}
	return nil
}

// edgeOf returns the edge of env with id, or nil.
func edgeOf(env *ir.ResolvedEnvironment, id string) *ir.Edge {
	for _, e := range env.Edges {
		if e.ID == id {
			return e
		}
	}
	return nil
}

// mixedShop is the shop whose shop-api the environments place on platform,
// another provider's, over services with RequireServiceShop's
// @requireService on shop-api, which Orders, a fake server, calls.
func mixedShop(platform string) (*ir.Stack, []stack.Service) {
	s, _ := shop()
	for _, env := range s.Environments {
		settingsFor(env, stacktest.Of(stacktest.ShopAPI)).Platform = platform
	}
	return s, stacktest.WithoutBuckets(stacktest.WithoutWorkers(stacktest.WithoutJobs(stacktest.RequireServiceShop())))
}

// TestGenericConnector: a fake server calling an exposed server of another
// provider gets the generic connector (D63): the callee's public address,
// a token signed with the edge's key, whose private key is a secret
// member referencing the key node's PKCS#8 PEM; the key node, a tls
// provider's Ed25519 private key; and the callee's callers field naming
// the caller as an issuer of its own with the key node's public key PEM.
func TestGenericConnector(t *testing.T) {
	s, services := mixedShop("other.run")
	env := mustResolve(t, assemble(t, &otherProvider{}), s, services, "Staging")

	edge := edgeOf(env, "http:Orders->shop-api")
	if edge == nil || edge.Connector != "generic.http" {
		t.Fatalf("edge = %+v, want one the generic connector serves", edge)
	}
	var endpoint *ir.Binding
	for _, b := range env.Deployable("Orders").Bindings {
		if b.Edge == edge.ID {
			endpoint = b
		}
	}
	if got, want := jsonOf(t, endpoint.Value), `{"credential":{"audience":"shop-api","issuer":"Orders","key":{"$output":{"resource":"Orders.calls.shop-api.key","name":"privateKeyPemPkcs8"}},"source":"signed-token"},"url":"https://shop-api.other.test"}`; got != want {
		t.Errorf("SHOP_API_SERVICE =\n  %s\nwant\n  %s", got, want)
	}
	vars, err := ir.DerivedVariables(endpoint.Field, endpoint.Value)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vars {
		if v.Secret != (v.Name == "SHOP_API_SERVICE_CREDENTIAL_KEY") {
			t.Errorf("%s: Secret = %v", v.Name, v.Secret)
		}
	}

	var key *ir.Resource
	for _, res := range env.Resources.Resources {
		if res.ID == "Orders.calls.shop-api.key" {
			key = res
		}
	}
	if key == nil || key.Type != "tls:index/privateKey:PrivateKey" || key.Properties["algorithm"] != "ED25519" ||
		key.Phase != ir.PhaseInfrastructure || strings.Join(key.Owners, ",") != edge.ID {
		t.Fatalf("key node = %s", mustMarshal(t, key))
	}

	callers := callersOf(env, "shop-api", "shop-api")
	if callers == nil {
		t.Fatal("shop-api has no callers field")
	}
	if got, want := jsonOf(t, callers.Value), `{"issuers":[{"algorithms":["EdDSA"],"audience":"shop-api","callers":[{"deployable":"Orders","serves":["shop-orders"],"subject":"Orders"}],"issuer":"Orders","keys":[{"pem":{"$output":{"resource":"Orders.calls.shop-api.key","name":"publicKeyPem"}}}],"maxLifetimeSeconds":300}]}`; got != want {
		t.Errorf("SHOP_API_CALLERS =\n  %s\nwant\n  %s", got, want)
	}

	// The reverse: a server of the other provider calling an exposed fake
	// server.
	s, services = mixedShop(stacktest.RunPlatform)
	for _, e := range s.Environments {
		settingsFor(e, ir.DeployableRef{Deployable: "Orders"}).Platform = "other.run"
	}
	env = mustResolve(t, assemble(t, &otherProvider{}), s, services, "Staging")
	if edge := edgeOf(env, "http:Orders->shop-api"); edge == nil || edge.Connector != "generic.http" {
		t.Errorf("other.run to fake.run: edge = %+v", edge)
	}
}

// TestGenericConnectorRefusals: a callee the stack does not expose
// (cross-provider-unexposed), an exposed one whose platform gives no
// public address, and a key a connector derives as a literal; a connector
// that names both platforms wins over the generic one.
func TestGenericConnectorRefusals(t *testing.T) {
	t.Run("an unexposed callee", func(t *testing.T) {
		s, services := mixedShop("other.run")
		s.Expose = nil
		_, errs := resolve(t, assemble(t, &otherProvider{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeCrossProviderUnexposed,
			"edge http:Orders->shop-api: server Orders on fake.run (fake) calls shop-api, whose server shop-api on other.run (other) the stack does not expose",
			"add shop-api to @stack's expose")
	})
	t.Run("no public address", func(t *testing.T) {
		s, services := mixedShop("other.dark")
		_, errs := resolve(t, assemble(t, &otherProvider{}), s, services, "Staging")
		mustFail(t, errs, stack.CodeLowering, "edge http:Orders->shop-api: server Orders calls shop-api across providers, and platform other.dark gives its server shop-api no public address")
	})
	t.Run("an exact connector wins", func(t *testing.T) {
		s, services := mixedShop("other.run")
		s.Expose = nil
		env := mustResolve(t, assemble(t, &otherProvider{exact: true}), s, services, "Staging")
		if edge := edgeOf(env, "http:Orders->shop-api"); edge == nil || edge.Connector != "other.fake-run" {
			t.Errorf("edge = %+v, want the exact connector's", edge)
		}
	})
	t.Run("a literal secret", func(t *testing.T) {
		s, services := mixedShop("other.run")
		_, errs := resolve(t, assemble(t, &otherProvider{exact: true, literalKey: true}), s, services, "Staging")
		mustFail(t, errs, stack.CodeLowering, "connector other.fake-run on edge http:Orders->shop-api derives SHOP_API_SERVICE_CREDENTIAL_KEY, a secret member, as a literal")
	})
}

// TestGenericSiteConnector: a site calling an exposed server of another
// provider gets the generic site connector, which derives the server's
// public address, and the server's CORS field lists the site's origin.
func TestGenericSiteConnector(t *testing.T) {
	s, services := siteShop()
	services = stacktest.WithoutBuckets(stacktest.WithoutWorkers(services))
	s = stacktest.WithoutBucketSettings(stacktest.WithoutWorkerSettings(s))
	for _, env := range s.Environments {
		settingsFor(env, stacktest.Of(stacktest.ShopAPI)).Platform = "other.run"
	}
	env := mustResolve(t, assemble(t, &otherProvider{}), s, services, "Staging")
	edge := edgeOf(env, "site:shop-web->shop-api")
	if edge == nil || edge.Connector != "generic.site" {
		t.Fatalf("edge = %+v, want one the generic site connector serves", edge)
	}
	site := env.Deployable("shop-web")
	if len(site.Bindings) != 1 || !jsonEqual(t, site.Bindings[0].Value, map[string]any{"url": "https://shop-api.other.test"}) {
		t.Errorf("shop-web's bindings = %s", mustMarshal(t, site.Bindings))
	}
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.CORSOf == "shop-api" && !jsonEqual(t, b.Value, map[string]any{"origins": []any{"https://shop-web.staging.acme.dev"}}) {
			t.Errorf("shop-api's CORS field = %s", mustMarshal(t, b.Value))
		}
	}
}
