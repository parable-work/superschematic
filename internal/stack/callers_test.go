package stack_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// callersOf returns the callers field of api on server, or nil.
func callersOf(env *ir.ResolvedEnvironment, server, api string) *ir.Binding {
	for _, b := range env.Deployable(server).Bindings {
		if b.CallersOf == api {
			return b
		}
	}
	return nil
}

func jsonOf(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// jobEdge is the edge from shop-orders' job to shop-api, which it takes
// from its API (D52).
const jobEdge = "http:" + stacktest.ShipOrdersJob + "->shop-api"

// TestCallersField: shop-api has a service clause, so its server gets
// SHOP_API_CALLERS, derived from the edges from Orders and from
// shop-orders' job, which takes its API's edges (D52): the fake issuer
// with each one's account as a caller that serves shop-orders, so a
// route's from: [ShopOrders] admits both. Orders, whose API has no clause,
// gets no callers field, and the job, which serves no request, gets none.
func TestCallersField(t *testing.T) {
	env := mustResolve(t, assemble(t), stacktest.Shop(), stacktest.RequireServiceShop(), "Staging")
	b := callersOf(env, "shop-api", "shop-api")
	if b == nil {
		t.Fatal("shop-api has no callers field")
	}
	if want := "http:Orders->shop-api," + jobEdge; b.Field != "SHOP_API_CALLERS" || b.Source != ir.BindingDerived || strings.Join(b.Edges, ",") != want {
		t.Errorf("binding = %+v, want SHOP_API_CALLERS derived from %s", b, want)
	}
	if got, want := jsonOf(t, b.Value), `{"issuers":[{"algorithms":["RS256"],"audience":"shop-api","callers":[{"deployable":"Orders","serves":["shop-orders"],"subject":{"$output":{"resource":"Orders.account","name":"email"}}},{"deployable":"shop-orders-ship-orders","serves":["shop-orders"],"subject":{"$output":{"resource":"shop-orders-ship-orders.account","name":"email"}}}],"issuer":"https://issuer.fake.test","jwksUrl":"https://issuer.fake.test/keys","subjectClaim":"email"}]}`; got != want {
		t.Errorf("SHOP_API_CALLERS =\n  %s\nwant\n  %s", got, want)
	}
	for _, b := range env.Deployable(stacktest.ShipOrdersJob).Bindings {
		if b.CallersOf != "" {
			t.Errorf("the job has callers field %s", b.Field)
		}
	}
	if callersOf(env, "Orders", "shop-orders") != nil {
		t.Error("Orders, whose API has no service clause, has a callers field")
	}
}

// TestCallersMergeByIssuer: two servers that call shop-api are two callers
// of the one issuer their connector names.
func TestCallersMergeByIssuer(t *testing.T) {
	s, services := stacktest.Shop(), stacktest.RequireServiceShop()
	billing := ir.ServiceRef{Name: "billing-api", Kind: ir.SchemaKindAPI}
	s.Deploy = append(s.Deploy, billing)
	services = append(services, stack.Service{
		Name: "billing-api", Kind: ir.SchemaKindAPI, Calls: []ir.ServiceRef{stacktest.ShopAPI},
		Operations: []stack.Operation{{Name: "Invoices.issue", UserClause: true}},
	})
	env := mustResolve(t, assemble(t), s, services, "Staging")
	b := callersOf(env, "shop-api", "shop-api")
	if got := strings.Join(b.Edges, ","); got != "http:Orders->shop-api,http:billing-api->shop-api,"+jobEdge {
		t.Errorf("edges = %s", got)
	}
	issuers := b.Value.(map[string]any)["issuers"].([]any)
	if len(issuers) != 1 {
		t.Fatalf("issuers = %s, want the fake issuer once", jsonOf(t, issuers))
	}
	var deployables []string
	for _, c := range issuers[0].(map[string]any)["callers"].([]any) {
		deployables = append(deployables, c.(map[string]any)["deployable"].(string))
	}
	if got := strings.Join(deployables, ","); got != "Orders,billing-api,"+stacktest.ShipOrdersJob {
		t.Errorf("callers = %s, want Orders, billing-api and %s", got, stacktest.ShipOrdersJob)
	}
}

// TestCallersWithoutAnEdge: an API with a service clause that no other
// server calls gets a callers field with no issuers, so its server starts
// and refuses every service credential. A call within one server adds no
// caller: it stays on loopback and carries no credential.
func TestCallersWithoutAnEdge(t *testing.T) {
	env := mustResolve(t, assemble(t), stacktest.Shop(), stacktest.AllowServiceShop(), "Staging")
	b := callersOf(env, "Orders", "shop-orders")
	if b == nil || b.Field != "SHOP_ORDERS_CALLERS" || len(b.Edges) != 0 || jsonOf(t, b.Value) != `{"issuers":[]}` {
		t.Errorf("Orders's callers field = %+v, want SHOP_ORDERS_CALLERS with no edges and no issuers", b)
	}

	s := stacktest.WithoutJobSettings(stacktest.Shop())
	s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders}}}
	s.Expose = []ir.DeployableRef{{Deployable: "Backend"}}
	s.Environments[0].Settings = []*ir.DeployableSettings{{Of: ir.DeployableRef{Deployable: "Backend"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}}}
	env = mustResolve(t, assemble(t), s, stacktest.WithoutJobs(stacktest.RequireServiceShop()), "Staging")
	if b := callersOf(env, "Backend", "shop-api"); b == nil || len(b.Edges) != 0 || jsonOf(t, b.Value) != `{"issuers":[]}` {
		t.Errorf("Backend's SHOP_API_CALLERS = %+v, want no issuers: its call to shop-api carries no credential", b)
	}

	// shop-orders' job is a process of its own, so its call to shop-api on
	// Backend carries a credential and makes it a caller (D52).
	env = mustResolve(t, assemble(t), s, stacktest.RequireServiceShop(), "Staging")
	if b := callersOf(env, "Backend", "shop-api"); b == nil || strings.Join(b.Edges, ",") != jobEdge {
		t.Errorf("Backend's SHOP_API_CALLERS = %+v, want the job's edge", b)
	}
}

// TestCallersFieldCollisions: a setting or an env key that takes the
// callers field's name, or one of its variables', is refused.
func TestCallersFieldCollisions(t *testing.T) {
	services := stacktest.RequireServiceShop()
	api := service(services, "shop-api")
	api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SHOP_API_CALLERS_LIMIT"})
	_, errs := resolve(t, assemble(t), stacktest.Shop(), services, "Staging")
	mustFail(t, errs, stack.CodeFieldCollision, "server shop-api: config field SHOP_API_CALLERS_LIMIT of ShopApiConfig collides with SHOP_API_CALLERS, the callers field of shop-api")

	s := stacktest.Shop()
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{
		Of: stacktest.Of(stacktest.ShopAPI), Env: map[string]ir.EnvValue{"SHOP_API_CALLERS": {Value: "none"}},
	})
	_, errs = resolve(t, assemble(t), s, stacktest.RequireServiceShop(), "Staging")
	mustFail(t, errs, stack.CodeUnknownEnvKey, "environment Staging sets env SHOP_API_CALLERS on server shop-api, the callers field of shop-api")
	if got := len(errs.Of(stack.CodeUnknownEnvKey)); got != 1 {
		t.Errorf("got %d unknown-env-key failures, want one:\n%v", got, errs)
	}
}

// calleeTarget places servers on callee.run, whose http connector gives
// the callee what callee returns.
type calleeTarget struct {
	callee func(registry.ConnectorContext) any
}

func (*calleeTarget) Name() string { return "callee" }

func (c *calleeTarget) Register(r *registry.Registry) error {
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "callee.run", Kind: ir.DeployableServer, Languages: []string{registry.APILanguageGo},
		NameOf:    func(ctx registry.PlatformContext) any { return ctx.Deployable.Name },
		AddressOf: func(ctx registry.PlatformContext) any { return "http://" + ctx.Deployable.Name },
		Lower:     func(registry.PlatformContext) (registry.Lowered, error) { return registry.Lowered{}, nil },
	}); err != nil {
		return err
	}
	if err := r.RegisterConnector(registry.ConnectorSpec{
		Name: "callee.http", Edge: ir.EdgeHTTP, From: "callee.run", To: "callee.run",
		Connect: func(ctx registry.ConnectorContext) (registry.Connected, error) {
			return registry.Connected{Value: ir.ServiceEndpoint{URL: ctx.To.Address}, Callee: c.callee(ctx)}, nil
		},
	}); err != nil {
		return err
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "callee",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "callee.run"},
	})
}

// calleeStack deploys stock-api, whose reindex only a service may call,
// and orders-api, which calls it, on the callee target.
func calleeStack() (*ir.Stack, []stack.Service) {
	stock := ir.ServiceRef{Name: "stock-api", Kind: ir.SchemaKindAPI}
	orders := ir.ServiceRef{Name: "orders-api", Kind: ir.SchemaKindAPI}
	return &ir.Stack{
			Name:         "Callee",
			Deploy:       []ir.ServiceRef{stock, orders},
			Environments: []*ir.Environment{{Name: "Dev", Target: "callee"}},
		}, []stack.Service{
			{Name: "stock-api", Kind: ir.SchemaKindAPI, Operations: []stack.Operation{{
				Name: "Stock.reindex", ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire},
			}}},
			{Name: "orders-api", Kind: ir.SchemaKindAPI, Calls: []ir.ServiceRef{stock}, Operations: []stack.Operation{{Name: "Orders.place", UserClause: true}}},
		}
}

// keyIssuer is the entry a key-pair connector gives the callee for the
// caller of ctx.
func keyIssuer(ctx registry.ConnectorContext) *ir.ServiceAuthIssuer {
	return &ir.ServiceAuthIssuer{
		Issuer: ctx.From.Name, Audience: ctx.To.Name, Algorithms: []string{ir.AlgorithmEdDSA},
		Keys:    []ir.ServiceAuthKey{{JWK: `{"kty":"OKP","crv":"Ed25519","kid":"k1","x":"AAAA"}`}},
		Callers: []ir.ServiceAuthCaller{{Subject: ctx.From.Name, Deployable: ctx.From.Name, Serves: []string{"orders-api"}}},
	}
}

// TestCallerEntryContract: what a connector gives the callee must be an
// issuer listing the edge's caller alone, with the APIs it serves, and an
// edge to an API with a service clause must give one.
func TestCallerEntryContract(t *testing.T) {
	s, services := calleeStack()
	env := mustResolve(t, assemble(t, &calleeTarget{callee: func(ctx registry.ConnectorContext) any { return keyIssuer(ctx) }}), s, services, "Dev")
	if b := callersOf(env, "stock-api", "stock-api"); b == nil || jsonOf(t, b.Value) != `{"issuers":[{"algorithms":["EdDSA"],"audience":"stock-api","callers":[{"deployable":"orders-api","serves":["orders-api"],"subject":"orders-api"}],"issuer":"orders-api","keys":[{"jwk":"{\"kty\":\"OKP\",\"crv\":\"Ed25519\",\"kid\":\"k1\",\"x\":\"AAAA\"}"}]}]}` {
		t.Errorf("stock-api's callers field = %+v", b)
	}

	edit := func(f func(*ir.ServiceAuthIssuer)) func(registry.ConnectorContext) any {
		return func(ctx registry.ConnectorContext) any {
			issuer := keyIssuer(ctx)
			f(issuer)
			return issuer
		}
	}
	for _, tc := range []struct {
		name   string
		callee func(registry.ConnectorContext) any
		want   string
	}{
		{"none", func(registry.ConnectorContext) any { return nil },
			"connector callee.http on edge http:orders-api->stock-api gives stock-api nothing to verify orders-api's service credential with, but stock-api has operations with a service clause, which stock-api checks against its callers field STOCK_API_CALLERS"},
		{"no issuer", func(registry.ConnectorContext) any { return map[string]any{"audience": "stock-api"} },
			"connector callee.http on edge http:orders-api->stock-api gives stock-api a caller to verify that is no issuer of a callers field (ir.ServiceAuthIssuer): issuer is missing"},
		{"another caller", edit(func(i *ir.ServiceAuthIssuer) { i.Callers[0].Deployable = "billing-api" }),
			"its caller is billing-api, not the edge's caller orders-api"},
		{"two callers", edit(func(i *ir.ServiceAuthIssuer) {
			i.Callers = append(i.Callers, ir.ServiceAuthCaller{Subject: "x", Deployable: "x", Serves: []string{"x"}})
		}), "it lists 2 callers; an edge's entry lists the edge's caller, orders-api, alone"},
		{"what the caller serves", edit(func(i *ir.ServiceAuthIssuer) { i.Callers[0].Serves = []string{"stock-api"} }),
			"its caller serves stock-api, but orders-api serves orders-api"},
		{"an undeclared parameter", edit(func(i *ir.ServiceAuthIssuer) { i.Audience = ir.Parameter("pr") }),
			"connector callee.http gives the callee of http:orders-api->stock-api parameter pr, which environment Dev does not declare"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errs := resolve(t, assemble(t, &calleeTarget{callee: tc.callee}), s, services, "Dev")
			code := stack.CodeLowering
			if tc.name == "an undeclared parameter" {
				code = stack.CodeUnknownParameter
			}
			mustFail(t, errs, code, tc.want)
		})
	}

	t.Run("a call within one server", func(t *testing.T) {
		s, services := calleeStack()
		s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{
			{Name: "stock-api", Kind: ir.SchemaKindAPI}, {Name: "orders-api", Kind: ir.SchemaKindAPI},
		}}}
		_, errs := resolve(t, assemble(t, &calleeTarget{callee: func(ctx registry.ConnectorContext) any { return keyIssuer(ctx) }}), s, services, "Dev")
		mustFail(t, errs, stack.CodeLowering, "connector callee.http on edge http:Backend->stock-api gives the callee a caller to verify, but only an http edge between two servers has one")
	})
}
