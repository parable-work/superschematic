package stacktest

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// AcmeShop returns the facts of the acme-shop services
// (examples/acme-shop/schemas/services) as the resolver reads them. The
// names, kinds, `authDb`, dependencies, `buckets` and languages are those
// of the services' schema configs, and the operations, with their user
// clauses, shop-orders' job, ShipOrders, and worker, FulfilOrders, and
// shop-db's queue, OrderPlaced, those of the services' schema files.
// shop-api lists the Bucket service shop-media, where its product images
// go (D54). The services declare no `calls` and no `@envVars` yet, so the
// fixture adds them: shop-orders calls shop-api, and both APIs' configs
// extend PaymentsSecrets, as in docs/stack-model.md, section 4.2.
func AcmeShop() []stack.Service {
	def := func(v string) *string { return &v }
	user := func(name string) stack.Operation { return stack.Operation{Name: name, UserClause: true} }
	open := func(name string) stack.Operation { return stack.Operation{Name: name} }
	shopDB := ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	return []stack.Service{
		{Name: "shop-common", Kind: ir.SchemaKindGeneral},
		{Name: "shop-db", Kind: ir.SchemaKindDB, Queues: []string{"OrderPlaced"}},
		{Name: "shop-media", Kind: ir.SchemaKindBucket},
		{
			Name:     "shop-api",
			Kind:     ir.SchemaKindAPI,
			AuthDB:   &shopDB,
			Buckets:  []ir.ServiceRef{ShopMedia},
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
			Jobs:    []ir.Job{ShipOrders},
			Workers: []ir.Worker{FulfilOrders},
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

// SiteShop returns AcmeShop with the site shop-web (D55), a Site service
// whose code at web/shop-web calls shop-api from the browser, as
// examples/acme-shop's does, with the single-page fallback. WithSite
// deploys it in a stack.
func SiteShop() []stack.Service {
	return append(AcmeShop(), stack.Service{
		Name:  "shop-web",
		Kind:  ir.SchemaKindSite,
		Calls: []ir.ServiceRef{ShopAPI},
		Site:  &ir.ResolvedSite{Dir: "web/shop-web", Build: "build", Output: "dist", Fallback: "index.html"},
	})
}

// WithSite returns s deploying the site shop-web as well (D55), to resolve
// over SiteShop's services. A site is always exposed, so s need not name
// it in expose; shop-api, which it calls, must be exposed, as Shop's is.
func WithSite(s *ir.Stack) *ir.Stack {
	s.Deploy = append(s.Deploy, ShopWeb)
	return s
}

// WithoutJobs returns services with no jobs: the shop for a target that
// places no job yet, or a test about the rest of the stack.
func WithoutJobs(services []stack.Service) []stack.Service {
	out := make([]stack.Service, len(services))
	for i, svc := range services {
		svc.Jobs = nil
		out[i] = svc
	}
	return out
}

// WithoutWorkers returns services with no workers and no queues: the shop
// for a target that places no worker yet (D53).
func WithoutWorkers(services []stack.Service) []stack.Service {
	out := make([]stack.Service, len(services))
	for i, svc := range services {
		svc.Workers, svc.Queues = nil, nil
		out[i] = svc
	}
	return out
}

// WithoutWorkerSettings returns s with no settings element that names a
// worker, to resolve over services WithoutWorkers returns.
func WithoutWorkerSettings(s *ir.Stack) *ir.Stack {
	for _, env := range s.Environments {
		var kept []*ir.DeployableSettings
		for _, settings := range env.Settings {
			if settings == nil || settings.Of.Worker == "" {
				kept = append(kept, settings)
			}
		}
		env.Settings = kept
	}
	return s
}

// WithoutJobSettings returns s with no settings element that names a job,
// to resolve over services WithoutJobs returns.
func WithoutJobSettings(s *ir.Stack) *ir.Stack {
	for _, env := range s.Environments {
		var kept []*ir.DeployableSettings
		for _, settings := range env.Settings {
			if settings == nil || settings.Of.Job == "" {
				kept = append(kept, settings)
			}
		}
		env.Settings = kept
	}
	return s
}

// WithoutBuckets returns services with no bucket: the shop for a target
// that places no bucket yet, or a test about the rest of the stack. The
// Bucket services stay, and no API lists them, so no stack reaches them.
func WithoutBuckets(services []stack.Service) []stack.Service {
	out := make([]stack.Service, len(services))
	for i, svc := range services {
		svc.Buckets = nil
		out[i] = svc
	}
	return out
}

// WithoutBucketSettings returns s with no settings element that names a
// bucket, to resolve over services WithoutBuckets returns.
func WithoutBucketSettings(s *ir.Stack) *ir.Stack {
	for _, env := range s.Environments {
		var kept []*ir.DeployableSettings
		for _, settings := range env.Settings {
			if settings == nil || settings.Of.Service == nil || settings.Of.Service.Kind != ir.SchemaKindBucket {
				kept = append(kept, settings)
			}
		}
		env.Settings = kept
	}
	return s
}

// Handles to the acme-shop services.
var (
	ShopDB     = ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindDB}
	ShopAPI    = ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindAPI}
	ShopOrders = ir.ServiceRef{Name: "shop-orders", Kind: ir.SchemaKindAPI}
	ShopWeb    = ir.ServiceRef{Name: "shop-web", Kind: ir.SchemaKindSite}
	ShopMedia  = ir.ServiceRef{Name: "shop-media", Kind: ir.SchemaKindBucket}
)

// ShipOrders is shop-orders' job (D52): the warehouse's pick run, which
// ships each placed order every fifteen minutes, in five minutes at most
// and with one retry. ShipOrdersJob is its deployable's name.
var ShipOrders = ir.Job{Name: "ShipOrders", Schedule: "*/15 * * * *", Timeout: "5m", Retries: 1}

const ShipOrdersJob = "shop-orders-ship-orders"

// FulfilOrders is shop-orders' worker (D53): it handles each OrderPlaced
// message of shop-db's queue, four at a time. FulfilOrdersWorker is its
// deployable's name.
var FulfilOrders = ir.Worker{Name: "FulfilOrders", Queue: "OrderPlaced", Concurrency: 4}

const FulfilOrdersWorker = "shop-orders-fulfil-orders"

// intp is a pointer to n, for a setting that takes one.
func intp(n int) *int { return &n }

// Of names the deployable that hosts or serves a service.
func Of(ref ir.ServiceRef) ir.DeployableRef { return ir.DeployableRef{Service: &ref} }

// JobOf names a job of an API service.
func JobOf(ref ir.ServiceRef, job string) ir.DeployableRef {
	return ir.DeployableRef{Service: &ref, Job: job}
}

// WorkerOf names a worker of an API service.
func WorkerOf(ref ir.ServiceRef, worker string) ir.DeployableRef {
	return ir.DeployableRef{Service: &ref, Worker: worker}
}

// Shop returns the stack of docs/stack-model.md, section 4.1, on the fake
// target, named after the service that declares it, shop-stack: shop-api
// and shop-orders are deployed and shop-api is exposed;
// the declared server Orders serves shop-orders in place of its default
// server, and takes its edges from shop-orders' authDb and calls; Staging
// and Production are environments, and Preview extends Staging with a
// parameter. shop-orders' job ShipOrders runs hourly in New York's time in
// Staging, on its decorator's schedule and two CPUs in Production, and on
// none in Preview, whose members run no schedule they do not turn on.
// shop-api's bucket, shop-media, joins the stack through its buckets, and
// keeps its objects' versions in Production (D54). shop-orders' worker
// FulfilOrders handles eight messages at a time in Staging, and in
// Preview, which extends it; Production runs three instances of it with a
// gigabyte each.
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
					{Of: JobOf(ShopOrders, "ShipOrders"), Schedule: "0 * * * *", TimeZone: "America/New_York"},
					{Of: WorkerOf(ShopOrders, "FulfilOrders"), Concurrency: intp(8)},
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
					{Of: JobOf(ShopOrders, "ShipOrders"), Values: map[string]any{"cpu": "2"}},
					{Of: Of(ShopMedia), Values: map[string]any{"versioning": true}},
					{Of: WorkerOf(ShopOrders, "FulfilOrders"), Instances: intp(3), Values: map[string]any{"memory": "1Gi"}},
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
