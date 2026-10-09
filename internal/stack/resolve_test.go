package stack_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

func assemble(t *testing.T, exts ...registry.Extension) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), append([]registry.Extension{&stacktest.Extension{}}, exts...)...)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// shop returns the acceptance stack and services for an edit, without
// shop-orders' job, which jobShop keeps (jobs_test.go).
func shop() (*ir.Stack, []stack.Service) {
	return stacktest.WithoutJobSettings(stacktest.Shop()), stacktest.WithoutJobs(stacktest.AcmeShop())
}

// jobShop returns the acceptance stack and services with shop-orders' job,
// ShipOrders, and the settings of it each environment makes.
func jobShop() (*ir.Stack, []stack.Service) {
	return stacktest.Shop(), stacktest.AcmeShop()
}

func service(services []stack.Service, name string) *stack.Service {
	for i := range services {
		if services[i].Name == name {
			return &services[i]
		}
	}
	panic("no service " + name)
}

func resolve(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) (*ir.ResolvedEnvironment, *stack.Errors) {
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

// mustFail asserts resolution failed with code, in a message that holds
// every part.
func mustFail(t *testing.T, errs *stack.Errors, code stack.Code, parts ...string) {
	t.Helper()
	if errs == nil {
		t.Fatalf("resolution succeeded, want a %s failure", code)
	}
	for _, err := range errs.Of(code) {
		matched := true
		for _, part := range parts {
			if !strings.Contains(err.Message, part) {
				matched = false
			}
		}
		if matched {
			return
		}
	}
	t.Fatalf("no %s failure mentions %q:\n%v", code, parts, errs)
}

func mustResolve(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) *ir.ResolvedEnvironment {
	t.Helper()
	out, errs := resolve(t, reg, s, services, env)
	if errs != nil {
		t.Fatal(errs)
	}
	return out
}

func settingsFor(env *ir.Environment, of ir.DeployableRef) *ir.DeployableSettings {
	for _, s := range env.Settings {
		if s.Of.String() == of.String() {
			return s
		}
	}
	s := &ir.DeployableSettings{Of: of}
	env.Settings = append(env.Settings, s)
	return s
}

func deployableNames(env *ir.ResolvedEnvironment) string {
	var names []string
	for _, d := range env.Deployables {
		var services []string
		for _, s := range d.Services {
			services = append(services, s.Name)
		}
		names = append(names, fmt.Sprintf("%s %s [%s]", d.Kind, d.Name, strings.Join(services, ",")))
	}
	return strings.Join(names, "; ")
}

func edgeIDs(env *ir.ResolvedEnvironment) string {
	var ids []string
	for _, e := range env.Edges {
		ids = append(ids, e.ID+"=>"+e.To)
	}
	return strings.Join(ids, "; ")
}

// TestDefaultDeployables: with nothing declared, each API service the
// stack reaches is one server and each DB service one database; the stack
// reaches shop-db through authDb and shop-api through calls, and leaves
// out the services nothing reaches (section 3.2).
func TestDefaultDeployables(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Deploy = []ir.ServiceRef{stacktest.ShopOrders}
	s.Deployables = nil
	s.Environments[0].Settings = []*ir.DeployableSettings{{Of: stacktest.Of(stacktest.ShopOrders), Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}}}
	env := mustResolve(t, reg, s, services, "Staging")
	if got, want := deployableNames(env), "server shop-api [shop-api]; database shop-db [shop-db]; server shop-orders [shop-orders]"; got != want {
		t.Errorf("deployables = %s, want %s", got, want)
	}
	if got, want := edgeIDs(env), "http:shop-orders->shop-api=>shop-api; sql:shop-api->shop-db=>shop-db; sql:shop-orders->shop-db=>shop-db"; got != want {
		t.Errorf("edges = %s, want %s", got, want)
	}
}

// TestDeclaredDeployables: a declared server runs several APIs in one
// process, a declared database hosts several DB schemas, and each edge
// names the schema it connects to.
func TestDeclaredDeployables(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	ledger := ir.ServiceRef{Name: "shop-ledger", Kind: ir.SchemaKindDB}
	services = append(services, stack.Service{Name: "shop-ledger", Kind: ir.SchemaKindDB})
	service(services, "shop-orders").AuthDB = &ledger
	service(services, "shop-orders").Dependencies = []ir.ServiceRef{ledger}
	service(services, "shop-orders").Calls = nil
	s.Deployables = []*ir.DeployableDecl{
		{Name: "Monolith", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders}},
		{Name: "Main", Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{stacktest.ShopDB, ledger}},
	}
	s.Expose = []ir.DeployableRef{{Deployable: "Monolith"}}
	s.Environments[0].Settings = []*ir.DeployableSettings{{Of: stacktest.Of(stacktest.ShopOrders), Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}}}
	env := mustResolve(t, reg, s, services, "Staging")
	if got, want := deployableNames(env), "database Main [shop-db,shop-ledger]; server Monolith [shop-api,shop-orders]"; got != want {
		t.Errorf("deployables = %s, want %s", got, want)
	}
	if got, want := edgeIDs(env), "sql:Monolith->shop-db=>Main; sql:Monolith->shop-ledger=>Main"; got != want {
		t.Errorf("edges = %s, want %s", got, want)
	}
	if env.Resources.Resource("Main.database.shop-ledger") == nil {
		t.Error("the database platform made no database for the second hosted schema")
	}
	var fields []string
	for _, b := range env.Deployable("Monolith").Bindings {
		fields = append(fields, b.Field+":"+string(b.Source))
	}
	if got, want := strings.Join(fields, " "), "FULFILLMENT_REGION:literal LOG_LEVEL:literal MAX_LINE_ITEMS:literal SHOP_DB_DATABASE:derived SHOP_LEDGER_DATABASE:derived STRIPE_KEY:secret"; got != want {
		t.Errorf("Monolith bindings = %s, want %s", got, want)
	}
}

// TestCallWithinOneServer: when one server serves both the caller and the
// callee, the call stays an http edge, to the server itself, and does not
// order the server after itself (section 3.3).
func TestCallWithinOneServer(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders}}}
	s.Expose = []ir.DeployableRef{{Deployable: "Backend"}}
	s.Environments[0].Settings = []*ir.DeployableSettings{{Of: ir.DeployableRef{Deployable: "Backend"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}}}
	env := mustResolve(t, reg, s, services, "Staging")
	if got, want := edgeIDs(env), "http:Backend->shop-api=>Backend; sql:Backend->shop-db=>shop-db"; got != want {
		t.Errorf("edges = %s, want %s", got, want)
	}
	var rollouts []string
	for _, step := range env.DeployOrder {
		if step.Step == ir.StepRollout {
			rollouts = append(rollouts, fmt.Sprintf("%d:%s", step.Wave, strings.Join(step.Deployables, ",")))
		}
	}
	if got := strings.Join(rollouts, " "); got != "1:Backend" {
		t.Errorf("rollout waves = %s, want Backend alone in wave 1", got)
	}
}

// TestServers: a stack's servers need no environment. Each declared server
// and the default server of every API it reaches that no declared server
// serves come with the APIs they serve, the APIs those call and their
// language, and a server whose APIs are written in two languages fails.
func TestServers(t *testing.T) {
	describe := func(servers []*ir.ResolvedDeployable) string {
		var parts []string
		for _, s := range servers {
			var serves, calls []string
			for _, ref := range s.Services {
				serves = append(serves, ref.Name)
			}
			for _, ref := range s.Calls {
				calls = append(calls, ref.Name)
			}
			parts = append(parts, fmt.Sprintf("%s declared=%t serves=%s calls=%s %s", s.Name, s.Declared, strings.Join(serves, ","), strings.Join(calls, ","), s.Language))
		}
		return strings.Join(parts, "; ")
	}
	t.Run("declared and default", func(t *testing.T) {
		s, services := shop()
		s.Environments = nil
		servers, err := stack.Servers(stack.Input{Stack: s, Services: services})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := describe(servers), "Orders declared=true serves=shop-orders calls=shop-api GO; shop-api declared=false serves=shop-api calls= GO"; got != want {
			t.Errorf("servers = %s, want %s", got, want)
		}
	})
	t.Run("one server for two APIs", func(t *testing.T) {
		s, services := shop()
		s.Deployables = []*ir.DeployableDecl{{Name: "Backend", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopOrders, stacktest.ShopAPI}}}
		servers, err := stack.Servers(stack.Input{Stack: s, Services: services})
		if err != nil {
			t.Fatal(err)
		}
		if got, want := describe(servers), "Backend declared=true serves=shop-api,shop-orders calls=shop-api GO"; got != want {
			t.Errorf("servers = %s, want %s", got, want)
		}
	})
	t.Run("two languages in one process", func(t *testing.T) {
		s, services := shop()
		s.Deployables[0].Serves = append(s.Deployables[0].Serves, ir.ServiceRef{Name: "shop-storefront", Kind: ir.SchemaKindAPI})
		_, err := stack.Servers(stack.Input{Stack: s, Services: services})
		var errs *stack.Errors
		if !errors.As(err, &errs) {
			t.Fatalf("Servers = %v, want *stack.Errors", err)
		}
		mustFail(t, errs, stack.CodeUnrealizable, "server Orders serves shop-orders in GO and shop-storefront in TYPESCRIPT; one process runs one language")
	})
}

// TestSQLEdgeFromTheOneDBDependency: an API with no authDb connects to its
// one DB-kind dependency, and one with only a General dependency connects
// to nothing (section 3.3).
func TestSQLEdgeFromTheOneDBDependency(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	service(services, "shop-orders").AuthDB = nil
	s.Deploy = append(s.Deploy, ir.ServiceRef{Name: "shop-storefront", Kind: ir.SchemaKindAPI})
	env := mustResolve(t, reg, s, services, "Staging")
	if got, want := edgeIDs(env), "http:Orders->shop-api=>shop-api; sql:Orders->shop-db=>shop-db; sql:shop-api->shop-db=>shop-db"; got != want {
		t.Errorf("edges = %s, want %s", got, want)
	}
	if d := env.Deployable("shop-storefront"); d == nil || d.Language != registry.APILanguageTypeScript {
		t.Errorf("shop-storefront = %+v, want a TypeScript server", d)
	}
}

// The checks of section 5.2, one test each.

func TestCheckUnboundField(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Environments[0].Settings = nil
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnboundField, "server Orders", "required config field FULFILLMENT_REGION of OrdersConfig has no binding and no default")
	if len(errs.Of(stack.CodeUnboundField)) != 1 {
		t.Errorf("want only FULFILLMENT_REGION unbound: a default binds MAX_LINE_ITEMS and LOG_LEVEL, and PREVIEW_ID is optional:\n%v", errs)
	}
}

func TestCheckUnknownEnvKey(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	staging := s.Environments[0]
	settingsFor(staging, stacktest.Of(stacktest.ShopAPI)).Env = map[string]ir.EnvValue{
		"LOG_LEVL":         {Value: "warn"},
		"SHOP_DB_DATABASE": {Value: "postgres://"},
	}
	settingsFor(staging, stacktest.Of(stacktest.ShopDB)).Env = map[string]ir.EnvValue{"PGHOST": {Value: "localhost"}}
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env LOG_LEVL on server shop-api, which is not a field of ShopApiConfig")
	mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env SHOP_DB_DATABASE on server shop-api, the field edge sql:shop-api->shop-db derives")
	mustFail(t, errs, stack.CodeUnknownEnvKey, "sets env PGHOST on database shop-db, which has no config")
}

func TestCheckSecretLiteral(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Env = map[string]ir.EnvValue{"STRIPE_KEY": {Value: "sk_test"}}
	settingsFor(s.Environments[2], ir.DeployableRef{Deployable: "Orders"}).Env = map[string]ir.EnvValue{"STRIPE_KEY": {Parameter: "pr"}}
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeSecretLiteral, "environment Staging sets STRIPE_KEY on server shop-api, a Secret<T> field of PaymentsSecrets")
	_, errs = resolve(t, reg, s, services, "Preview")
	mustFail(t, errs, stack.CodeSecretLiteral, "environment Preview sets STRIPE_KEY on server Orders")
}

func TestCheckKindMismatch(t *testing.T) {
	reg := assemble(t)
	t.Run("handle kind differs from the service", func(t *testing.T) {
		s, services := shop()
		s.Deploy = append(s.Deploy, ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindAPI})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "deploy holds a handle to shop-db of kind API, but shop-db is a DB service")
	})
	t.Run("authDb handle", func(t *testing.T) {
		s, services := shop()
		service(services, "shop-api").AuthDB = &ir.ServiceRef{Name: "shop-db", Kind: ir.SchemaKindGeneral}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "service shop-api authDb holds a handle to shop-db of kind General")
	})
	t.Run("settings of", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: ir.DeployableRef{Service: &ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindDB}}})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "settings[1] (of shop-api) holds a handle to shop-api of kind DB, but shop-api is a API service")
	})
	t.Run("expose", func(t *testing.T) {
		s, services := shop()
		s.Expose = []ir.DeployableRef{{Service: &ir.ServiceRef{Name: "shop-api", Kind: ir.SchemaKindGeneral}}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "expose[0] (shop-api) holds a handle to shop-api of kind General")
	})
	t.Run("serves and hosts", func(t *testing.T) {
		s, services := shop()
		s.Deployables = append(s.Deployables,
			&ir.DeployableDecl{Name: "Main", Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{stacktest.ShopAPI}},
			&ir.DeployableDecl{Name: "Common", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{{Name: "shop-common", Kind: ir.SchemaKindGeneral}}},
		)
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "database Main hosts names shop-api, a API service; it takes DB services")
		mustFail(t, errs, stack.CodeKindMismatch, "server Common serves names shop-common, a General service; it takes API services")
	})
	t.Run("a kind the place does not take", func(t *testing.T) {
		s, services := shop()
		service(services, "shop-orders").Calls = []ir.ServiceRef{stacktest.ShopDB}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeKindMismatch, "service shop-orders calls names shop-db, a DB service; it takes API services")
	})
}

func TestCheckUnrealizable(t *testing.T) {
	reg := assemble(t, &serversOnly{})
	t.Run("language", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Platform = stacktest.EdgePlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "server shop-api is a GO server, and platform fake.edge runs only TYPESCRIPT")
	})
	t.Run("dialect", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopDB)).Platform = stacktest.LitePlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "database shop-db hosts shop-db (postgres), and platform fake.lite runs only sqlite")
	})
	t.Run("two languages in one process", func(t *testing.T) {
		s, services := shop()
		s.Deployables[0].Serves = append(s.Deployables[0].Serves, ir.ServiceRef{Name: "shop-storefront", Kind: ir.SchemaKindAPI})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "server Orders serves shop-orders in GO and shop-storefront in TYPESCRIPT; one process runs one language")
	})
	t.Run("platform of another kind", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopDB)).Platform = stacktest.RunPlatform
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "database shop-db is placed on platform fake.run, which realizes a server")
	})
	t.Run("target without a platform for the kind", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].Target = "servers-only"
		s.Environments[0].Values = nil
		s.Environments[0].DNS = nil
		s.Environments[0].Domain = ""
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnrealizable, "database shop-db: target servers-only has no platform for a database")
	})
}

// serversOnly registers a target with a server platform and no database
// platform.
type serversOnly struct{}

func (*serversOnly) Name() string { return "serversonly" }
func (*serversOnly) Register(r *registry.Registry) error {
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "servers-only",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: stacktest.RunPlatform},
	})
}

func TestCheckNoConnector(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	service(services, "shop-api").Language = registry.APILanguageTypeScript
	settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Platform = stacktest.EdgePlatform
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeNoConnector, "edge http:Orders->shop-api: no connector from fake.run to fake.edge over http")
	mustFail(t, errs, stack.CodeNoConnector, "edge sql:shop-api->shop-db: no connector from fake.edge to fake.sql over sql")
}

func TestCheckExposeNotServer(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Expose = append(s.Expose, stacktest.Of(stacktest.ShopDB))
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeExposeNotServer, "stack shop-stack exposes shop-db, a database; only a server or a site is exposed")
}

func TestCheckPolicy(t *testing.T) {
	reg := assemble(t)
	t.Run("production databases are highly available", func(t *testing.T) {
		s, services := shop()
		prod := s.Environments[1]
		settingsFor(prod, stacktest.Of(stacktest.ShopDB)).Values = map[string]any{"tier": "large"}
		_, errs := resolve(t, reg, s, services, "Production")
		mustFail(t, errs, stack.CodePolicy, "target fake policy production-ha: instance shop-db.instance of shop-db is not highly available")
	})
	t.Run("nothing is public unless exposed", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], ir.DeployableRef{Deployable: "Orders"}).Values = map[string]any{"public": true}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodePolicy, "target fake policy public-only-if-exposed: resource Orders.service is public, but Orders is not exposed")
	})
}

// TestCheckUnreachableEdge: the server Orders, which serves shop-orders,
// calls shop-api, and the edge must reach a shop-api operation that admits
// Orders (section 9.3). Orders forwards an end user when shop-orders has an
// operation with a user clause; a clause lists Orders when its from is
// empty or names shop-orders. A from naming a service the stack does not
// deploy is no error.
func TestCheckUnreachableEdge(t *testing.T) {
	reg := assemble(t)
	require := func(from ...string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: from}
	}
	allow := func(from ...string) *ir.ServiceCallers {
		return &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: from}
	}
	noUsers := []stack.Operation{{Name: "ProductReviews.listReviews"}}
	cases := []struct {
		name   string
		orders []stack.Operation // nil keeps shop-orders' own, with user clauses
		api    []stack.Operation
		admits bool
	}{
		{"a user operation, from a server with users", nil,
			[]stack.Operation{{Name: "ProductQueries.getProduct", UserClause: true}}, true},
		{"a user operation, from a server without users", noUsers,
			[]stack.Operation{{Name: "ProductQueries.getProduct", UserClause: true}}, false},
		{"an operation open to anyone", noUsers,
			[]stack.Operation{{Name: "ProductQueries.listProducts"}}, true},
		{"@allowService listing Orders, from a server without users", noUsers,
			[]stack.Operation{{Name: "StockMutations.release", UserClause: true, ServiceCallers: allow("shop-orders")}}, true},
		{"@allowService listing others, from a server with users", nil,
			[]stack.Operation{{Name: "StockMutations.release", UserClause: true, ServiceCallers: allow("billing-api")}}, true},
		{"@allowService listing others, from a server without users", noUsers,
			[]stack.Operation{{Name: "StockMutations.release", UserClause: true, ServiceCallers: allow("billing-api")}}, false},
		{"@requireService listing Orders", noUsers,
			[]stack.Operation{{Name: "StockMutations.reindex", ServiceCallers: require("shop-orders")}}, true},
		{"@requireService listing every edge", noUsers,
			[]stack.Operation{{Name: "StockMutations.reindex", ServiceCallers: require()}}, true},
		{"@requireService listing others", nil,
			[]stack.Operation{{Name: "StockMutations.reindex", ServiceCallers: require("billing-api", "shop-storefront")}}, false},
		{"@requireService with a user clause listing Orders, from a server with users", nil,
			[]stack.Operation{{Name: "StockMutations.reserve", UserClause: true, ServiceCallers: require("shop-orders")}}, true},
		{"@requireService with a user clause listing Orders, from a server without users", noUsers,
			[]stack.Operation{{Name: "StockMutations.reserve", UserClause: true, ServiceCallers: require("shop-orders")}}, false},
		{"no operations", nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, services := shop()
			if tc.orders != nil {
				service(services, "shop-orders").Operations = tc.orders
			}
			service(services, "shop-api").Operations = tc.api
			_, errs := resolve(t, reg, s, services, "Staging")
			if tc.admits {
				if errs != nil {
					t.Fatal(errs)
				}
				return
			}
			want := []string{"Orders calls shop-api, but no shop-api operation admits Orders"}
			if tc.orders != nil {
				want = append(want, "(Orders forwards no end user: no API it serves has an operation with a user clause)")
			}
			mustFail(t, errs, stack.CodeUnreachableEdge, want...)
			if got := len(errs.List); got != 1 {
				t.Errorf("got %d failures, want the one:\n%v", got, errs)
			}
		})
	}
}

// TestOperationsOf: an API's operations carry their user clause and their
// effective service clause, the set's unless the operation declares its
// own or is @publicRoute.
func TestOperationsOf(t *testing.T) {
	setClause := &ir.ServiceCallers{Mode: ir.ServiceCallersRequire, From: []string{"shop-orders"}}
	own := &ir.ServiceCallers{Mode: ir.ServiceCallersAllow}
	schema := ir.NewSchema("shop-api", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{{
		Name:           "StockMutations",
		ServiceCallers: setClause,
		Operations: []*ir.FieldDef{
			{Name: "reserve", Permissions: []string{"stock.reserve"}},
			{Name: "release", Auth: true, ServiceCallers: own},
			{Name: "health", Public: true},
		},
	}}
	got := stack.OperationsOf(schema)
	want := []stack.Operation{
		{Name: "StockMutations.reserve", UserClause: true, ServiceCallers: setClause},
		{Name: "StockMutations.release", UserClause: true, ServiceCallers: own},
		{Name: "StockMutations.health"},
	}
	if !slices.EqualFunc(got, want, func(a, b stack.Operation) bool {
		return a.Name == b.Name && a.UserClause == b.UserClause && a.ServiceCallers == b.ServiceCallers
	}) {
		t.Errorf("OperationsOf = %+v, want %+v", got, want)
	}
}

// The other failures.

func TestUnknownService(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Deploy = append(s.Deploy, ir.ServiceRef{Name: "shop-cart", Kind: ir.SchemaKindAPI})
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnknownService, "deploy names service shop-cart, which is not among the stack's services")
}

func TestUnknownDeployable(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	s.Environments[0].Settings = append(s.Environments[0].Settings,
		&ir.DeployableSettings{Of: ir.DeployableRef{Deployable: "Checkout"}},
		&ir.DeployableSettings{Of: ir.DeployableRef{Service: &ir.ServiceRef{Name: "shop-storefront", Kind: ir.SchemaKindAPI}}},
	)
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnknownDeployable, "names deployable Checkout, which stack shop-stack does not declare")
	mustFail(t, errs, stack.CodeUnknownDeployable, "names service shop-storefront, which is not in stack shop-stack")
}

func TestTargetValuesAndPlatforms(t *testing.T) {
	reg := assemble(t)
	t.Run("no target", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].Target = ""
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownTarget, "environment Staging names no target")
	})
	t.Run("unknown target", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].Target = "gcp"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownTarget, "names target gcp, which is not registered")
	})
	t.Run("values", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].Values = map[string]any{"region": "us-east1", "zone": "b"}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidValues, "values for target fake", "project")
	})
	t.Run("unknown platform", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Platform = "fake.workers"
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownPlatform, "server shop-api is placed on platform fake.workers, which is not registered")
	})
	t.Run("settings", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Values = map[string]any{"minInstances": float64(-1)}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, "settings of shop-api on platform fake.run", "minInstances")
	})
	t.Run("env literal", func(t *testing.T) {
		s, services := shop()
		settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Env = map[string]ir.EnvValue{"LOG_LEVEL": {Value: map[string]any{"level": "warn"}}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidSettings, `sets LOG_LEVEL on server shop-api to {"level":"warn"}`)
	})
	t.Run("DNS platform and values", func(t *testing.T) {
		s, services := shop()
		s.Environments[0].DNS = &ir.DNSPlacement{Platform: "fake.dns", Values: map[string]any{"zone": ""}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeInvalidValues, "values for DNS platform fake.dns")
		s.Environments[0].DNS = &ir.DNSPlacement{Platform: "cloudflare"}
		_, errs = resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeUnknownPlatform, "places DNS on cloudflare, which is not a registered DNS platform")
	})
}

func TestUnknownParameter(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Env = map[string]ir.EnvValue{"PREVIEW_ID": {Parameter: "pr"}}
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnknownParameter, "binds PREVIEW_ID on server shop-api to parameter pr, which environment Staging does not declare")
}

func TestFieldCollision(t *testing.T) {
	reg := assemble(t)
	t.Run("two types declare one field for one server", func(t *testing.T) {
		s, services := shop()
		s.Deployables[0].Serves = append(s.Deployables[0].Serves, stacktest.ShopAPI)
		service(services, "shop-orders").Config.Fields = append(service(services, "shop-orders").Config.Fields, stack.ConfigField{Name: "LOG_LEVEL"})
		s.Expose = []ir.DeployableRef{{Deployable: "Orders"}}
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeFieldCollision, "server Orders: config field LOG_LEVEL is declared by both")
	})
	t.Run("a config field with a derived field's name", func(t *testing.T) {
		s, services := shop()
		cfg := service(services, "shop-api").Config
		cfg.Fields = append(cfg.Fields, stack.ConfigField{Name: "SHOP_DB_DATABASE"})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeFieldCollision, "server shop-api: config field SHOP_DB_DATABASE of ShopApiConfig collides with SHOP_DB_DATABASE, the field edge sql:shop-api->shop-db derives")
	})
	t.Run("a config field with one of a derived field's variables", func(t *testing.T) {
		s, services := shop()
		cfg := service(services, "shop-api").Config
		cfg.Fields = append(cfg.Fields, stack.ConfigField{Name: "SHOP_DB_DATABASE_URL"})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeFieldCollision, "server shop-api: config field SHOP_DB_DATABASE_URL of ShopApiConfig collides with SHOP_DB_DATABASE")
	})
}

// TestFieldNamesFromTheNamingFile: Input.FieldNames names the derived
// fields, and a template that names no service is refused.
func TestFieldNamesFromTheNamingFile(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	in := stack.Input{Stack: s, Services: services, Environment: "Staging",
		FieldNames: ir.DerivedFieldNames{Database: "{SERVICE}_DB_URL", Service: "{SERVICE}_ENDPOINT"}}
	env, err := stack.Resolve(reg, in)
	if err != nil {
		t.Fatal(err)
	}
	var fields []string
	for _, e := range env.Edges {
		fields = append(fields, e.Field)
	}
	if got, want := strings.Join(fields, ","), "SHOP_API_ENDPOINT,SHOP_DB_DB_URL,SHOP_DB_DB_URL"; got != want {
		t.Errorf("edge fields = %s, want %s", got, want)
	}

	in.FieldNames = ir.DerivedFieldNames{Service: "ENDPOINT"}
	_, err = stack.Resolve(reg, in)
	var errs *stack.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("resolve with a template naming no service: %v", err)
	}
	mustFail(t, errs, stack.CodeInvalidStack, "derived_fields.service \"ENDPOINT\" must contain {SERVICE} once")
}

// TestDerivedFieldCollision: two edges of one server whose services' names
// differ only in punctuation derive one field, which fails.
func TestDerivedFieldCollision(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	other := ir.ServiceRef{Name: "shop_db", Kind: ir.SchemaKindDB}
	services = append(services, stack.Service{Name: "shop_db", Kind: ir.SchemaKindDB})
	service(services, "shop-api").AuthDB = &other
	s.Deployables[0].Serves = append(s.Deployables[0].Serves, stacktest.ShopAPI)
	s.Expose = []ir.DeployableRef{{Deployable: "Orders"}}
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeFieldCollision, "server Orders: edges sql:Orders->shop-db and sql:Orders->shop_db derive the same field SHOP_DB_DATABASE")
}

func TestAmbiguousDatabase(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	services = append(services, stack.Service{Name: "shop-ledger", Kind: ir.SchemaKindDB})
	orders := service(services, "shop-orders")
	orders.AuthDB = nil
	orders.Dependencies = append(orders.Dependencies, ir.ServiceRef{Name: "shop-ledger", Kind: ir.SchemaKindDB})
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeAmbiguousDatabase, "service shop-orders depends on DB services shop-db, shop-ledger and sets no authDb")
}

func TestCallCycle(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	service(services, "shop-api").Calls = []ir.ServiceRef{stacktest.ShopOrders}
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeCallCycle, "Orders -> shop-api -> Orders")

	// An edge no connector serves, found in the same stage, does not hide
	// the cycle.
	service(services, "shop-api").Language = registry.APILanguageTypeScript
	settingsFor(s.Environments[0], stacktest.Of(stacktest.ShopAPI)).Platform = stacktest.EdgePlatform
	_, errs = resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeNoConnector, "edge http:Orders->shop-api")
	mustFail(t, errs, stack.CodeCallCycle, "Orders -> shop-api -> Orders")
}

func TestInvalidStack(t *testing.T) {
	reg := assemble(t)
	cases := []struct {
		name string
		edit func(*ir.Stack, []stack.Service) []stack.Service
		want string
	}{
		{"unknown environment", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Environments = s.Environments[1:]
			return sv
		}, "stack shop-stack has no environment Staging"},
		{"unknown parent", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Environments[0].Extends = "Base"
			return sv
		}, "environment Staging extends Base, which stack shop-stack does not declare"},
		{"extends cycle", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Environments[0].Extends = "Preview"
			return sv
		}, "extends itself"},
		{"service claimed twice", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: "Orders2", Kind: ir.DeployableServer, Serves: []ir.ServiceRef{stacktest.ShopOrders}})
			return sv
		}, "shop-orders is claimed by both Orders and Orders2"},
		{"declared deployable named like a default", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: "shop-api", Kind: ir.DeployableDatabase, Hosts: []ir.ServiceRef{stacktest.ShopDB}})
			return sv
		}, "would be named shop-api, like the declared deployable shop-api"},
		{"server without APIs", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Deployables = append(s.Deployables, &ir.DeployableDecl{Name: "Empty", Kind: ir.DeployableServer})
			return sv
		}, "server Empty serves no API"},
		{"DNS without a domain", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Environments[0].Domain = ""
			return sv
		}, "places DNS on fake.dns but sets no domain"},
		{"parameter declared twice", func(s *ir.Stack, sv []stack.Service) []stack.Service {
			s.Environments[0].Parameters = []string{"pr"}
			return sv
		}, "environment Preview declares parameter pr, which it already has"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, services := shop()
			services = tc.edit(s, services)
			env := "Staging"
			if strings.Contains(tc.name, "parameter") {
				env = "Preview"
			}
			_, errs := resolve(t, reg, s, services, env)
			mustFail(t, errs, stack.CodeInvalidStack, tc.want)
		})
	}
}

// TestBindingSources: every config field is bound from one of the four
// sources (section 5.1), and the servers that include one declared secret
// field share one secret (section 4.2).
func TestBindingSources(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	env := mustResolve(t, reg, s, services, "Preview")
	got := map[string]string{}
	for _, d := range env.Deployables {
		for _, b := range d.Bindings {
			got[d.Name+"."+b.Field] = string(b.Source)
		}
	}
	for key, want := range map[string]string{
		"Orders.FULFILLMENT_REGION": "literal",
		"Orders.MAX_LINE_ITEMS":     "literal",
		"Orders.SHOP_API_SERVICE":   "derived",
		"Orders.STRIPE_KEY":         "secret",
		"shop-api.PREVIEW_ID":       "parameter",
		"shop-api.LOG_LEVEL":        "literal",
	} {
		if got[key] != want {
			t.Errorf("%s bound from %q, want %s", key, got[key], want)
		}
	}
	if len(env.Secrets) != 1 || strings.Join(env.Secrets[0].Readers, ",") != "Orders,shop-api" {
		t.Errorf("secrets = %+v, want one secret both servers read", env.Secrets)
	}
	// Preview inherits Staging's literal, which a parameter env cannot drop.
	for _, b := range env.Deployable("Orders").Bindings {
		if b.Field == "FULFILLMENT_REGION" && b.Value != "us" {
			t.Errorf("FULFILLMENT_REGION = %v, want Staging's us", b.Value)
		}
	}
}

// TestEnvironmentInheritance: an environment that extends another merges
// its values, settings and env over the parent's, key by key.
func TestEnvironmentInheritance(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	staging, preview := s.Environments[0], s.Environments[2]
	settingsFor(staging, stacktest.Of(stacktest.ShopAPI)).Values = map[string]any{"minInstances": float64(1)}
	settingsFor(staging, stacktest.Of(stacktest.ShopAPI)).Env = map[string]ir.EnvValue{"LOG_LEVEL": {Value: "debug"}}
	settingsFor(preview, stacktest.Of(stacktest.ShopAPI)).Values = map[string]any{"public": true}
	preview.Values = map[string]any{"region": "eu-west1"}
	env := mustResolve(t, reg, s, services, "Preview")
	if env.Values["project"] != "acme-staging" || env.Values["region"] != "eu-west1" {
		t.Errorf("values = %v, want Staging's project and Preview's region", env.Values)
	}
	api := env.Deployable("shop-api")
	if api.Settings["minInstances"] != float64(1) || api.Settings["public"] != true {
		t.Errorf("shop-api settings = %v, want Staging's minInstances and Preview's public", api.Settings)
	}
	levels := map[string]any{}
	for _, b := range api.Bindings {
		levels[b.Field] = b.Value
	}
	if levels["LOG_LEVEL"] != "debug" || env.Domain != "staging.acme.dev" || env.DNS.Platform != stacktest.DNSPlatform {
		t.Errorf("Preview lost Staging's env, domain or DNS: LOG_LEVEL=%v domain=%s dns=%+v", levels["LOG_LEVEL"], env.Domain, env.DNS)
	}
}

// TestManualDNS: a domain no DNS platform holds gets ir.ManualDNS, whose
// records are listed and lowered to nothing.
func TestManualDNS(t *testing.T) {
	reg := assemble(t, &noDNS{})
	s, services := shop()
	s.Environments[0].Target = "no-dns"
	s.Environments[0].DNS = nil
	env := mustResolve(t, reg, s, services, "Staging")
	if env.DNS == nil || env.DNS.Platform != ir.ManualDNS || len(env.DNS.Records) != 1 {
		t.Fatalf("dns = %+v, want manual with shop-api's record", env.DNS)
	}
	for _, res := range env.Resources.Resources {
		if res.Type == stacktest.TypeRecord {
			t.Errorf("manual DNS lowered record %s", res.ID)
		}
	}
}

// noDNS registers a target with the fake platforms and no DNS platform.
type noDNS struct{}

func (*noDNS) Name() string { return "nodns" }
func (*noDNS) Register(r *registry.Registry) error {
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "no-dns",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: stacktest.RunPlatform, ir.DeployableDatabase: stacktest.SQLPlatform},
		Values:    json.RawMessage(`{"type": "object"}`),
	})
}

// TestDNSPlatformOfAnotherProvider: a DNS platform that brings the schema
// of its own resource type has its records checked against it, and the
// credentials it names reach environment.json, even with no record to
// write; credentials that do not name a secret and a variable fail.
func TestDNSPlatformOfAnotherProvider(t *testing.T) {
	place := func(s *ir.Stack, values map[string]any) {
		s.Environments[0].DNS = &ir.DNSPlacement{Platform: "other.dns", Values: values}
	}
	reg := assemble(t, &otherDNS{})
	s, services := shop()
	place(s, map[string]any{"zone": "acme.dev"})
	env := mustResolve(t, reg, s, services, "Staging")
	record := env.Resources.Resource("dns.shop-api")
	if record == nil || record.Type != "other:dns/record:Record" || record.Properties["zone"] != "acme.dev" {
		t.Fatalf("record = %+v, want the other provider's record in zone acme.dev", record)
	}
	want := []*ir.DNSCredential{{Secret: "shop-stack-other-acme.dev", Env: "OTHER_TOKEN", Description: "a token for acme.dev"}}
	if got, _ := json.Marshal(env.DNS.Credentials); string(got) != string(mustMarshal(t, want)) {
		t.Errorf("credentials = %s, want %s", got, mustMarshal(t, want))
	}

	// No exposed server, so no record: the credential is still named, for
	// bootstrap to ask for before the first server is exposed.
	s, services = shop()
	place(s, map[string]any{"zone": "acme.dev"})
	s.Expose = nil
	env = mustResolve(t, reg, s, services, "Staging")
	if len(env.DNS.Records) != 0 || len(env.DNS.Credentials) != 1 {
		t.Errorf("dns = %+v, want no record and one credential", env.DNS)
	}

	s, services = shop()
	place(s, map[string]any{"zone": 7.0})
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeGraph, "resource dns.shop-api (other:dns/record:Record)", "zone")

	for _, tc := range []struct {
		creds []ir.DNSCredential
		want  string
	}{
		{[]ir.DNSCredential{{Env: "OTHER_TOKEN"}}, "DNS platform other.dns names a credential with no secret"},
		{[]ir.DNSCredential{{Secret: "s", Env: "OTHER-TOKEN"}}, `reads secret s from environment variable "OTHER-TOKEN", which is not a variable name`},
		{[]ir.DNSCredential{{Secret: "s", Env: "A"}, {Secret: "t", Env: "A"}}, "names secret t or environment variable A for two credentials"},
	} {
		reg := assemble(t, &otherDNS{creds: tc.creds})
		s, services := shop()
		place(s, map[string]any{"zone": "acme.dev"})
		_, errs := resolve(t, reg, s, services, "Staging")
		mustFail(t, errs, stack.CodeLowering, tc.want)
	}
}

// otherDNS registers DNS platform other.dns, which writes records of a
// provider no target registers, with the schema of its record type, and
// names creds as its credentials, or by default a token per zone.
type otherDNS struct{ creds []ir.DNSCredential }

func (*otherDNS) Name() string { return "otherdns" }
func (o *otherDNS) Register(r *registry.Registry) error {
	return r.RegisterDNSPlatform(registry.DNSPlatformSpec{
		Name:   "other.dns",
		Values: json.RawMessage(`{"type": "object", "properties": {"zone": {}}, "additionalProperties": false}`),
		Lower: func(ctx registry.DNSContext) ([]*ir.Resource, error) {
			var out []*ir.Resource
			for _, rec := range ctx.Records {
				out = append(out, &ir.Resource{ID: "dns." + rec.Deployable, Type: "other:dns/record:Record", Properties: map[string]any{
					"zone": ctx.Values["zone"], "name": rec.Name, "content": rec.Value,
				}})
			}
			return out, nil
		},
		ResourceTypes: map[string]json.RawMessage{"other:dns/record:Record": json.RawMessage(`{
		  "type": "object", "required": ["zone", "name", "content"], "additionalProperties": false,
		  "properties": {"zone": {"type": "string"}, "name": {"type": "string"}, "content": {"type": "string"}}}`)},
		Credentials: func(ctx registry.DNSContext) []ir.DNSCredential {
			if o.creds != nil {
				return o.creds
			}
			zone, _ := ctx.Values["zone"].(string)
			if len(ctx.Records) > 0 {
				panic("Credentials sees records")
			}
			return []ir.DNSCredential{{Secret: ctx.Environment.Stack + "-other-" + zone, Env: "OTHER_TOKEN", Description: "a token for " + zone}}
		},
	})
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestResolveIsDeterministic: the same stack gives the same bytes,
// whatever order the services and settings come in.
func TestResolveIsDeterministic(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	first, err := stack.Marshal(mustResolve(t, reg, s, services, "Production"))
	if err != nil {
		t.Fatal(err)
	}
	s, services = shop()
	slices.Reverse(services)
	slices.Reverse(s.Environments[1].Settings)
	second, err := stack.Marshal(mustResolve(t, reg, s, services, "Production"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Error("resolution depends on the order of its inputs")
	}
}

// TestResolveDoesNotModifyItsInputs: the stack and services come back as
// they went in.
func TestResolveDoesNotModifyItsInputs(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	before, _ := json.Marshal([]any{s, services})
	mustResolve(t, reg, s, services, "Preview")
	after, _ := json.Marshal([]any{s, services})
	if !bytes.Equal(before, after) {
		t.Error("Resolve modified its input")
	}
}

// TestProducersAndPoliciesGetCopies: a platform that changes the
// deployable it is given, and a policy rule that changes the environment
// it checks, change nothing in the result.
func TestProducersAndPoliciesGetCopies(t *testing.T) {
	var kept *ir.Resource
	ext := &broken{
		lower: func(ctx registry.PlatformContext) (registry.Lowered, error) {
			ctx.Deployable.Services[0].Name = "meddled"
			ctx.Deployable.Bindings[0].Field = "MEDDLED"
			ctx.Deployable.Bindings[0].Value = "meddled"
			kept = &ir.Resource{ID: "svc", Type: "broken:thing", Properties: map[string]any{"size": float64(1)}}
			return registry.Lowered{Resources: []*ir.Resource{kept}}, nil
		},
		policy: func(env *ir.ResolvedEnvironment) []string {
			env.Deployables[0].Platform = "meddled"
			env.Resources.Resources = nil
			return nil
		},
	}
	reg := assemble(t, ext)
	s, services := brokenStack()
	env := mustResolve(t, reg, s, services, "Parent")
	kept.Properties["size"] = float64(99)
	api := env.Deployable("shop-api")
	if api.Services[0].Name != "shop-api" || api.Bindings[0].Field != "LOG_LEVEL" || api.Bindings[0].Value != "info" {
		t.Errorf("the platform changed shop-api: %+v %+v", api.Services, api.Bindings[0])
	}
	if api.Platform != "broken.run" || env.Resources.Resource("svc") == nil {
		t.Errorf("the policy changed the result: platform %s, resources %v", api.Platform, env.Resources.Resources)
	}
	if size := env.Resources.Resource("svc").Properties["size"]; size != float64(1) {
		t.Errorf("the resolver kept the platform's map: size %v", size)
	}
}

// broken is an extension whose server platform lowers what a test says,
// for the graph checks.
type broken struct {
	lower   func(registry.PlatformContext) (registry.Lowered, error)
	lowerDB func(registry.PlatformContext) []*ir.Resource
	name    any
	address any
	policy  func(*ir.ResolvedEnvironment) []string
	// connection is what broken.sql derives; nil is a connection string.
	connection any
}

func (*broken) Name() string { return "broken" }
func (b *broken) Register(r *registry.Registry) error {
	address, name, connection := b.address, b.name, b.connection
	if connection == nil {
		connection = ir.DatabaseConnection{URL: "postgres://shop-db:5432/shop_db"}
	}
	if address == nil {
		address = "https://shop-api"
	}
	if name == nil {
		name = "shop-api"
	}
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "broken.run", Kind: ir.DeployableServer, Languages: []string{registry.APILanguageGo},
		NameOf:    func(registry.PlatformContext) any { return name },
		AddressOf: func(registry.PlatformContext) any { return address },
		Lower:     b.lower,
	}); err != nil {
		return err
	}
	if err := r.RegisterPlatform(registry.PlatformSpec{
		Name: "broken.sql", Kind: ir.DeployableDatabase, Dialects: []string{registry.SQLDialectPostgres},
		NameOf:    func(registry.PlatformContext) any { return "shop-db" },
		AddressOf: func(registry.PlatformContext) any { return "shop-db:5432" },
		Lower: func(ctx registry.PlatformContext) (registry.Lowered, error) {
			if b.lowerDB == nil {
				return registry.Lowered{}, nil
			}
			return registry.Lowered{Resources: b.lowerDB(ctx)}, nil
		},
	}); err != nil {
		return err
	}
	if err := r.RegisterConnector(registry.ConnectorSpec{
		Name: "broken.sql", Edge: ir.EdgeSQL, From: "broken.run", To: "broken.sql",
		Connect: func(registry.ConnectorContext) (registry.Connected, error) {
			return registry.Connected{Value: connection, Resources: []*ir.Resource{{ID: "conflict", Type: "broken:thing", Properties: map[string]any{"size": float64(2)}}}}, nil
		},
	}); err != nil {
		return err
	}
	var policies []registry.PolicyRule
	if b.policy != nil {
		policies = []registry.PolicyRule{{Name: "meddle", Check: b.policy}}
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name:      "broken",
		Platforms: map[ir.DeployableKind]string{ir.DeployableServer: "broken.run", ir.DeployableDatabase: "broken.sql"},
		ResourceTypes: map[string]json.RawMessage{"broken:thing": json.RawMessage(
			`{"type": "object", "properties": {"size": {"type": "integer"}, "parts": {}}, "additionalProperties": false}`)},
		Policies: policies,
	})
}

// brokenStack deploys shop-api alone on the broken target, in Parent and
// in Child, which extends Parent with a parameter.
func brokenStack() (*ir.Stack, []stack.Service) {
	return &ir.Stack{
		Name:   "Broken",
		Deploy: []ir.ServiceRef{stacktest.ShopAPI},
		Environments: []*ir.Environment{
			{Name: "Parent", Target: "broken"},
			{Name: "Child", Extends: "Parent", Parameters: []string{"pr"}},
		},
	}, stacktest.AcmeShop()
}

func TestGraphChecks(t *testing.T) {
	thing := func(id string, props map[string]any, deps ...string) *ir.Resource {
		return &ir.Resource{ID: id, Type: "broken:thing", Properties: props, DependsOn: deps}
	}
	cases := []struct {
		name    string
		env     string
		lower   func(registry.PlatformContext) []*ir.Resource
		lowerDB func(registry.PlatformContext) []*ir.Resource
		nameOf  any
		address any
		code    stack.Code
		want    string
	}{
		{name: "reference inside a typed list", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"parts": []map[string]any{{"v": ir.Output{Resource: "ghost", Name: "id"}}}})}
		}, code: stack.CodeGraph, want: "resource svc depends on ghost"},
		{name: "parameter inside a typed list", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"parts": []ir.Parameter{"undeclared"}})}
		}, code: stack.CodeUnknownParameter, want: "resource svc references parameter undeclared"},
		{name: "concat of a number", nameOf: ir.Concat{"shop-api-", float64(8080)}, lower: func(registry.PlatformContext) []*ir.Resource {
			return nil
		}, code: stack.CodeLowering, want: "platform broken.run names shop-api with a value environment.json cannot hold: $concat[1] must be a string"},
		{name: "nested concat", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"parts": ir.Concat{"a", ir.Concat{"b"}}})}
		}, code: stack.CodeLowering, want: "shop-api gives resource svc properties holding a value environment.json cannot hold"},
		{name: "properties that are a reference", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"$parameter": "pr"})}
		}, code: stack.CodeLowering, want: "shop-api gives resource svc properties that are not an object"},
		{name: "database resource after the migration", lowerDB: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{{ID: "db", Type: "broken:thing", Phase: ir.PhaseExposure}}
		}, lower: func(registry.PlatformContext) []*ir.Resource {
			return nil
		}, code: stack.CodeGraph, want: "resource db of database shop-db lands in exposure, after the migration that needs it"},
		{name: "dangling dependency", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", nil, "ghost")}
		}, code: stack.CodeGraph, want: "resource svc depends on ghost, which no platform, connector or DNS platform produced"},
		{name: "dangling output", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"size": ir.Output{Resource: "ghost", Name: "size"}})}
		}, code: stack.CodeGraph, want: "resource svc depends on ghost"},
		{name: "address names no node", address: ir.Output{Resource: "svc", Name: "url"}, lower: func(registry.PlatformContext) []*ir.Resource {
			return nil
		}, code: stack.CodeGraph, want: "the address of shop-api references output url of resource svc"},
		{name: "unknown type", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{{ID: "svc", Type: "broken:other"}}
		}, code: stack.CodeGraph, want: "resource svc has type broken:other, which no registered target or DNS platform has a schema for"},
		{name: "properties fail the type's schema", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"size": "large"})}
		}, code: stack.CodeGraph, want: "resource svc (broken:thing)"},
		{name: "cycle", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("a", nil, "b"), thing("b", nil, "a")}
		}, code: stack.CodeGraph, want: "cycle: a -> b -> a"},
		{name: "two producers differ", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("conflict", map[string]any{"size": float64(3)})}
		}, code: stack.CodeGraph, want: "resource conflict: sql:shop-api->shop-db and shop-api produce it differently"},

		{name: "inherited without a parent", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{{ID: "shared", Type: "broken:thing", Inherited: true}}
		}, code: stack.CodeGraph, want: "resource shared is inherited, but environment Parent extends no environment"},
		{name: "inherited node the parent lacks", env: "Child", lower: func(ctx registry.PlatformContext) []*ir.Resource {
			if len(ctx.Environment.Parameters) == 0 {
				return nil
			}
			return []*ir.Resource{{ID: "shared", Type: "broken:thing", Inherited: true}}
		}, code: stack.CodeGraph, want: "resource shared is inherited, but environment Parent has no such resource"},
		{name: "undeclared parameter", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{thing("svc", map[string]any{"size": ir.Parameter("build")})}
		}, code: stack.CodeUnknownParameter, want: "resource svc references parameter build"},
		{name: "unknown phase", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{{ID: "svc", Type: "broken:thing", Phase: "later"}}
		}, code: stack.CodeGraph, want: `resource svc has phase "later"`},
		{name: "server resource after its wave", lower: func(registry.PlatformContext) []*ir.Resource {
			return []*ir.Resource{
				{ID: "svc", Type: "broken:thing", DependsOn: []string{"route"}},
				{ID: "route", Type: "broken:thing", Phase: ir.PhaseExposure},
			}
		}, code: stack.CodeGraph, want: "resource svc of server shop-api lands in exposure through its dependencies, after the server's rollout wave 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext := &broken{address: tc.address, name: tc.nameOf, lowerDB: tc.lowerDB, lower: func(ctx registry.PlatformContext) (registry.Lowered, error) {
				return registry.Lowered{Resources: tc.lower(ctx)}, nil
			}}
			reg := assemble(t, ext)
			s, services := brokenStack()
			env := tc.env
			if env == "" {
				env = "Parent"
			}
			_, errs := resolve(t, reg, s, services, env)
			mustFail(t, errs, tc.code, tc.want)
		})
	}
}

// TestLoweringErrors: a platform's error and DNS records from a server
// that is not exposed are reported as lowering failures.
func TestLoweringErrors(t *testing.T) {
	s, services := brokenStack()
	reg := assemble(t, &broken{lower: func(registry.PlatformContext) (registry.Lowered, error) {
		return registry.Lowered{}, errors.New("quota exceeded")
	}})
	_, errs := resolve(t, reg, s, services, "Parent")
	mustFail(t, errs, stack.CodeLowering, "platform broken.run lowering shop-api: quota exceeded")

	reg = assemble(t, &broken{lower: func(registry.PlatformContext) (registry.Lowered, error) {
		return registry.Lowered{Records: []*ir.DNSRecord{{Name: "x", Type: "A", Value: "1.2.3.4"}}}, nil
	}})
	_, errs = resolve(t, reg, s, services, "Parent")
	mustFail(t, errs, stack.CodeLowering, "platform broken.run returned DNS records for shop-api, which is not exposed")
}

// TestDerivedValueContract: a connector's value that is no database
// connection is a lowering failure that names the member at fault, and
// the free-form values connectors returned before the contract are
// refused.
func TestDerivedValueContract(t *testing.T) {
	s, services := brokenStack()
	for _, tc := range []struct {
		connection any
		want       string
	}{
		{"postgres://shop-db:5432/shop_db", `a database connection must be an object, not "postgres://shop-db:5432/shop_db"`},
		{map[string]any{"instance": "shop-db:5432", "database": "shop_db"}, "a database connection has no member database"},
		{ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{Instance: "acme:us:shop", Database: "shop_db"}}, "cloudSql.user is null"},
	} {
		reg := assemble(t, &broken{connection: tc.connection, lower: func(registry.PlatformContext) (registry.Lowered, error) {
			return registry.Lowered{}, nil
		}})
		_, errs := resolve(t, reg, s, services, "Parent")
		mustFail(t, errs, stack.CodeLowering, "connector broken.sql on edge sql:shop-api->shop-db derives a value for SHOP_DB_DATABASE that is no database connection (ir.DatabaseConnection): "+tc.want)
	}
}

// TestParentResolvesForInheritedNodes: the inherited nodes of a member of
// Preview are Staging's.
func TestParentResolvesForInheritedNodes(t *testing.T) {
	reg := assemble(t)
	s, services := shop()
	env := mustResolve(t, reg, s, services, "Preview")
	var inherited []string
	for _, res := range env.Resources.Resources {
		if res.Inherited {
			inherited = append(inherited, res.ID)
		}
	}
	if got, want := strings.Join(inherited, ","), "secret.PaymentsSecrets.STRIPE_KEY,shop-db.instance"; got != want {
		t.Errorf("inherited = %s, want %s", got, want)
	}
	for _, step := range env.DeployOrder {
		for _, id := range step.Resources {
			if slices.Contains(inherited, id) {
				t.Errorf("step %s applies %s, which Staging owns", step.Step, id)
			}
		}
	}
}
