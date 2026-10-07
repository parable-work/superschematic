package stacktest

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// AcmeShop returns the facts of the acme-shop services
// (examples/acme-shop/schemas/services) as the resolver reads them. The
// names, kinds, `authDb`, dependencies and languages are those of the
// services' schema configs, and the operations, with their user clauses,
// those of the services' schema files. The services declare no `calls` and
// no `@envVars` yet, so the fixture adds them: shop-orders calls shop-api,
// and both APIs' configs extend PaymentsSecrets, as in
// docs/stack-model.md, section 4.2.
func AcmeShop() []stack.Service {
	def := func(v string) *string { return &v }
	user := func(name string) stack.Operation { return stack.Operation{Name: name, UserClause: true} }
	open := func(name string) stack.Operation { return stack.Operation{Name: name} }
	shopDB := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	return []stack.Service{
		{Name: "shop-common", Kind: ir.SchemaKindGeneral},
		{Name: "shop-db", Kind: ir.SchemaKindDB},
		{
			Name:     "shop-api",
			Kind:     ir.SchemaKindAPI,
			AuthDB:   &shopDB,
			Language: registry.APILanguageGo,
			Config: &stack.Config{
				Type: "ShopApiConfig",
				Fields: []stack.ConfigField{
					{Name: "STRIPE_KEY", Required: true, Secret: true, InheritedFrom: "PaymentsSecrets"},
					{Name: "LOG_LEVEL", Required: true, Default: def("info")},
					{Name: "PREVIEW_ID"},
				},
			},
			Operations: []stack.Operation{
				user("ProductQueries.getProduct"), user("ProductQueries.listProducts"),
				user("ProductMutations.createProduct"),
			},
		},
		{
			Name:         "shop-orders",
			Kind:         ir.SchemaKindAPI,
			AuthDB:       &shopDB,
			Dependencies: []ir.ServiceRef{shopDB},
			Calls:        []ir.ServiceRef{{Name: "shop-api", Kind: ir.SchemaKindAPI}},
			Config: &stack.Config{
				Type: "OrdersConfig",
				Fields: []stack.ConfigField{
					{Name: "STRIPE_KEY", Required: true, Secret: true, InheritedFrom: "PaymentsSecrets"},
					{Name: "FULFILLMENT_REGION", Required: true},
					{Name: "MAX_LINE_ITEMS", Required: true, Default: def("50")},
				},
			},
			Operations: []stack.Operation{
				user("OrderQueries.getOrder"), user("OrderQueries.listOrders"),
				user("OrderMutations.placeOrder"), user("OrderMutations.cancelOrder"),
				open("ProductReviews.listReviews"), user("ProductReviews.writeReview"),
			},
		},
		{
			Name:         "shop-storefront",
			Kind:         ir.SchemaKindAPI,
			Language:     registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{{Name: "shop-common", Kind: ir.SchemaKindGeneral}},
			Operations: []stack.Operation{
				open("StorefrontProbes.getHealth"),
				user("CartQueries.getCart"), user("CartMutations.addCartLine"),
			},
		},
	}
}

// RequireServiceShop returns AcmeShop with an operation of shop-api that
// only the server of shop-orders may call, with no end user:
// StockMutations.reindex, @requireService({ from: [ShopOrders] }). The
// server of shop-api then verifies its callers against SHOP_API_CALLERS.
func RequireServiceShop() []stack.Service {
	services := AcmeShop()
	for i := range services {
		if services[i].Name == "shop-api" {
			services[i].Operations = append(services[i].Operations, stack.Operation{
				Name:           "StockMutations.reindex",
				ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"shop-orders"}},
			})
		}
	}
	return services
}

// AllowServiceShop returns AcmeShop with an operation of shop-api that an
// end user who may, or the server of shop-orders on its own, may call:
// StockMutations.release, @allowService({ from: [ShopOrders] }) beside a
// user clause. shop-orders gains OrderMutations.refund, which the server
// of shop-api may call on its own, though no server does: its callers
// field has no issuers.
func AllowServiceShop() []stack.Service {
	services := AcmeShop()
	for i := range services {
		switch services[i].Name {
		case "shop-api":
			services[i].Operations = append(services[i].Operations, stack.Operation{
				Name:           "StockMutations.release",
				UserClause:     true,
				ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"shop-orders"}},
			})
		case "shop-orders":
			services[i].Operations = append(services[i].Operations, stack.Operation{
				Name:           "OrderMutations.refund",
				UserClause:     true,
				ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"shop-api"}},
			})
		}
	}
	return services
}

// Handles to the acme-shop services.
var (
	ShopDB     = ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	ShopAPI    = ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	ShopOrders = ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
)

// Of names the deployable that hosts or serves a service.
func Of(ref ir.ServiceRef) ir.DeployableRef { return ir.DeployableRef{Service: &ref} }

// Shop returns the stack of docs/stack-model.md, section 4.1, on the fake
// target, named after the service that declares it, shop-stack: shop-api
// and shop-orders are deployed and shop-api is exposed;
// the declared server Orders serves shop-orders in place of its default
// server, and takes its edges from shop-orders' authDb and calls; Staging
// and Production are environments, and Preview extends Staging with a
// parameter.
func Shop() *ir.Stack {
	return &ir.Stack{
		Name:   "shop-stack",
		Deploy: []ir.ServiceRef{ShopAPI, ShopOrders},
		Expose: []ir.DeployableRef{Of(ShopAPI)},
		Deployables: []*ir.DeployableDecl{{
			Name:   "Orders",
			Kind:   ir.DeployableServer,
			Serves: []ir.ServiceRef{ShopOrders},
		}},
		Environments: []*ir.Environment{
			{
				Name:   "Staging",
				Target: Target,
				Values: map[string]any{"project": "acme-staging", "region": "us-east1"},
				Domain: "staging.acme.dev",
				DNS:    &ir.DNSPlacement{Platform: DNSPlatform, Values: map[string]any{"zone": "acme.dev"}},
				Settings: []*ir.DeployableSettings{
					{Of: ir.DeployableRef{Deployable: "Orders"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}},
				},
			},
			{
				Name:   "Production",
				Target: Target,
				Values: map[string]any{"project": "acme-prod", "region": "us-east1", "production": true},
				Domain: "acme.dev",
				Settings: []*ir.DeployableSettings{
					{Of: Of(ShopDB), Values: map[string]any{"tier": "large", "highAvailability": true}},
					{Of: Of(ShopAPI), Values: map[string]any{"minInstances": float64(1)}, Env: map[string]ir.EnvValue{"LOG_LEVEL": {Value: "warn"}}},
					{Of: Of(ShopOrders), Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}},
				},
			},
			{
				Name:       "Preview",
				Extends:    "Staging",
				Parameters: []string{"pr"},
				Settings: []*ir.DeployableSettings{
					{Of: Of(ShopAPI), Env: map[string]ir.EnvValue{"PREVIEW_ID": {Parameter: "pr"}}},
				},
			},
		},
	}
}
