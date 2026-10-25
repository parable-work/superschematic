package stack_test

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The memoryStore deployable and edge kinds (D75): shop-api lists the
// MemoryStore service shop-cache in its memoryStores and shop-orders names
// it its rateLimitStore, which brings it into the stack as a deployable of
// its own, and gives each server, job and worker of the two a memoryStore
// edge to it whose connector derives how to reach it.

var shopCache = ir.ServiceRef{Name: "shop-cache", Kind: ir.SchemaKindMemoryStore}

// withCache adds shop-cache to services, listed by shop-api and by
// shop-orders.
func withCache(s *ir.Stack, services []stack.Service) (*ir.Stack, []stack.Service) {
	services = append(services, stack.Service{Name: shopCache.Name, Kind: shopCache.Kind})
	service(services, "shop-api").MemoryStores = []ir.ServiceRef{shopCache}
	service(services, "shop-orders").MemoryStores = []ir.ServiceRef{shopCache}
	return s, services
}

// cacheShop is jobShop with shop-cache: shop-orders' job ShipOrders reaches
// the store too.
func cacheShop() (*ir.Stack, []stack.Service) {
	return withCache(jobShop())
}

// memoryStoreEdges renders the memoryStore edges as "ID by connector into
// field".
func memoryStoreEdges(env *ir.ResolvedEnvironment) string {
	var edges []string
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeMemoryStore {
			edges = append(edges, fmt.Sprintf("%s by %s into %s", e.ID, e.Connector, e.Field))
		}
	}
	return strings.Join(edges, "; ")
}

// offIn turns shop-cache off in env.
func offIn(env *ir.Environment) {
	off := false
	env.Settings = append(env.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Enabled: &off})
}

// TestMemoryStoreDeployable: in Staging the store is placed on the
// target's memoryStore platform, named after the project, the stack and
// the store, and each server and job of an API that uses it reaches it
// over a memoryStore edge through the connector of its platform, whose
// derived value, a memory store connection, fills SHOP_CACHE_MEMORY_STORE.
func TestMemoryStoreDeployable(t *testing.T) {
	s, services := cacheShop()
	env := mustResolve(t, assemble(t), s, services, "Staging")
	cache := env.Deployable("shop-cache")
	if cache == nil {
		t.Fatalf("no memory store shop-cache among %v", names(env))
	}
	if cache.Kind != ir.DeployableMemoryStore || cache.Platform != stacktest.MemoryStorePlatform || cache.Declared || cache.Exposed || cache.Language != "" || len(cache.Bindings) != 0 {
		t.Errorf("memory store = %+v", cache)
	}
	if cache.ResourceName != "acme-staging-shop-stack-shop-cache" || refNames(cache.Services) != "shop-cache" {
		t.Errorf("named %v, services %s", cache.ResourceName, refNames(cache.Services))
	}
	for _, name := range []string{"shop-api", "Orders", "shop-orders-ship-orders"} {
		if got := refNames(env.Deployable(name).MemoryStores); got != "shop-cache" {
			t.Errorf("%s's memory stores = %s, want shop-cache", name, got)
		}
	}
	want := "memoryStore:Orders->shop-cache by fake.run-memorystore into SHOP_CACHE_MEMORY_STORE; " +
		"memoryStore:shop-api->shop-cache by fake.run-memorystore into SHOP_CACHE_MEMORY_STORE; " +
		"memoryStore:shop-orders-ship-orders->shop-cache by fake.job-memorystore into SHOP_CACHE_MEMORY_STORE"
	if got := memoryStoreEdges(env); got != want {
		t.Errorf("memoryStore edges = %s\nwant %s", got, want)
	}
	api := env.Deployable("shop-api")
	if got := bindings(api); !strings.Contains(got, "SHOP_CACHE_MEMORY_STORE=derived:memoryStore:shop-api->shop-cache") {
		t.Errorf("shop-api's bindings = %s, want SHOP_CACHE_MEMORY_STORE from its memoryStore edge", got)
	}
	for _, b := range api.Bindings {
		if b.Field != "SHOP_CACHE_MEMORY_STORE" {
			continue
		}
		if err := ir.CheckDerivedValue(ir.EdgeMemoryStore, b.Value); err != nil {
			t.Errorf("SHOP_CACHE_MEMORY_STORE = %v: %v", b.Value, err)
		}
		vars, err := ir.DerivedVariables(b.Field, b.Value)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, v := range vars {
			got = append(got, v.Name)
		}
		if strings.Join(got, ",") != "SHOP_CACHE_MEMORY_STORE_AUTH,SHOP_CACHE_MEMORY_STORE_CA,SHOP_CACHE_MEMORY_STORE_STORE,SHOP_CACHE_MEMORY_STORE_URL" {
			t.Errorf("variables = %v", got)
		}
	}
	// The store and its grants are infrastructure, applied before any
	// server rolls out.
	for _, step := range env.DeployOrder {
		for _, id := range step.Resources {
			if (id == "shop-cache.instance" || strings.Contains(id, ".memorystore.")) && step.Step != ir.StepInfrastructure {
				t.Errorf("%s lands in %s, want infrastructure", id, step.Step)
			}
		}
	}
	if len(env.Disabled) != 0 {
		t.Errorf("disabled = %+v", env.Disabled)
	}
}

// TestMemoryStoreOfAWorker: a worker of an API that uses the store reaches
// it through the worker platform's connector.
func TestMemoryStoreOfAWorker(t *testing.T) {
	s, services := withCache(workerShop())
	env := mustResolve(t, assemble(t), s, services, "Staging")
	if got := memoryStoreEdges(env); !strings.Contains(got, "memoryStore:shop-orders-fulfil-orders->shop-cache by fake.worker-memorystore into SHOP_CACHE_MEMORY_STORE") {
		t.Errorf("memoryStore edges = %s", got)
	}
}

// TestMemoryStoreInAMember: a member of Preview, a parameterized
// environment, shares its parent's store, named with no parameter, and
// its connection names the member.
func TestMemoryStoreInAMember(t *testing.T) {
	s, services := cacheShop()
	env := mustResolve(t, assemble(t), s, services, "Preview")
	cache := env.Deployable("shop-cache")
	if cache == nil || cache.ResourceName != "acme-staging-shop-stack-shop-cache" {
		t.Fatalf("memory store = %+v", cache)
	}
	for _, b := range env.Deployable("shop-api").Bindings {
		if b.Field == "SHOP_CACHE_MEMORY_STORE" {
			value := b.Value.(map[string]any)
			if value["member"] != (ir.Parameter("pr")) {
				t.Errorf("member = %#v, want the parameter pr", value["member"])
			}
		}
	}
}

// TestMemoryStoreTurnedOff: `enabled: false` places no deployable for the
// store, which the environment lists in Disabled with the environment that
// turned it off, derives no edge to it, so no workload's field is set, and
// leaves it out of each workload's memory stores; an environment that
// extends it turns it on again. Servers, which no environment changes,
// still list it, so each entrypoint can open it.
func TestMemoryStoreTurnedOff(t *testing.T) {
	reg := assemble(t)
	s, services := cacheShop()
	offIn(s.Environment("Staging"))
	env := mustResolve(t, reg, s, services, "Staging")
	if d := env.Deployable("shop-cache"); d != nil {
		t.Errorf("shop-cache is placed: %+v", d)
	}
	want := []*ir.DisabledDeployable{{Name: "shop-cache", Kind: ir.DeployableMemoryStore, Services: []ir.ServiceRef{shopCache}, From: "Staging"}}
	if len(env.Disabled) != 1 || fmt.Sprintf("%+v", *env.Disabled[0]) != fmt.Sprintf("%+v", *want[0]) {
		t.Errorf("disabled = %+v, want %+v", env.Disabled, want)
	}
	if got := memoryStoreEdges(env); got != "" {
		t.Errorf("memoryStore edges = %s, want none", got)
	}
	for _, d := range env.Deployables {
		if len(d.MemoryStores) != 0 || strings.Contains(bindings(d), "MEMORY_STORE") {
			t.Errorf("%s reaches %s with bindings %s", d.Name, refNames(d.MemoryStores), bindings(d))
		}
	}
	for _, r := range env.Resources.Resources {
		if strings.Contains(r.ID, "shop-cache") {
			t.Errorf("resource %s for a store that is off", r.ID)
		}
	}

	preview := s.Environment("Preview")
	on := true
	preview.Settings = append(preview.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Enabled: &on})
	member := mustResolve(t, reg, s, services, "Preview")
	if member.Deployable("shop-cache") == nil || len(member.Disabled) != 0 {
		t.Errorf("Preview turns the store on again: deployables %v, disabled %+v", names(member), member.Disabled)
	}

	servers, err := stack.Servers(stack.Input{Stack: s, Services: services})
	if err != nil {
		t.Fatal(err)
	}
	for _, server := range servers {
		if server.Name == "shop-api" && refNames(server.MemoryStores) != "shop-cache" {
			t.Errorf("Servers: shop-api's memory stores = %s, want shop-cache", refNames(server.MemoryStores))
		}
	}
}

// TestMemoryStoreOnATargetWithoutAPlatform: a target with no memoryStore
// platform refuses a stack that places the store there, naming
// `enabled: false` as the way to run without it, and resolves it once the
// environment turns the store off.
func TestMemoryStoreOnATargetWithoutAPlatform(t *testing.T) {
	reg := assemble(t, &noDNS{})
	noTarget := func() (*ir.Stack, []stack.Service) {
		s, services := withCache(shop())
		for _, env := range s.Environments {
			if env.Target != "" {
				env.Target = "no-dns"
				env.DNS = nil
			}
		}
		return s, services
	}
	s, services := noTarget()
	_, errs := resolve(t, reg, s, services, "Staging")
	mustFail(t, errs, stack.CodeUnrealizable, "memoryStore shop-cache: target no-dns has no platform for a memory store; place it with a settings platform; to run without it, turn it off in this environment with `settings: [{ of: ShopCache, enabled: false }]`")

	s, services = noTarget()
	offIn(s.Environment("Staging"))
	env := mustResolve(t, reg, s, services, "Staging")
	if env.Deployable("shop-cache") != nil || len(env.Disabled) != 1 {
		t.Errorf("deployables %v, disabled %+v", names(env), env.Disabled)
	}
}

// TestMemoryStoreSettings: a store's platform settings reach it, and its
// field takes the naming file's template.
func TestMemoryStoreSettings(t *testing.T) {
	s, services := cacheShop()
	s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Values: map[string]any{"nodeType": "SHARED_CORE_NANO"}})
	env, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: services, Environment: "Staging", FieldNames: ir.DerivedFieldNames{MemoryStore: "CACHE_{SERVICE}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := env.Deployable("shop-cache").Settings["nodeType"]; got != "SHARED_CORE_NANO" {
		t.Errorf("settings = %v", env.Deployable("shop-cache").Settings)
	}
	if instance := env.Resources.Resource("shop-cache.instance"); instance == nil || instance.Properties["nodeType"] != "SHARED_CORE_NANO" {
		t.Errorf("instance = %+v", instance)
	}
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeMemoryStore && e.Field != "CACHE_SHOP_CACHE" {
			t.Errorf("edge %s fills %s, want CACHE_SHOP_CACHE", e.ID, e.Field)
		}
	}
}

// TestMemoryStoreRefusals: what resolution refuses of memory stores.
func TestMemoryStoreRefusals(t *testing.T) {
	reg := assemble(t)
	cache := stacktest.Of(shopCache)
	for _, tc := range []struct {
		name string
		edit func(*ir.Stack, []stack.Service)
		code stack.Code
		want string
	}{
		{"a Bucket handle in memoryStores", func(_ *ir.Stack, services []stack.Service) {
			service(services, "shop-api").MemoryStores = []ir.ServiceRef{stacktest.ShopMedia}
		}, stack.CodeKindMismatch, "service shop-api memoryStores names shop-media, a Bucket service; it takes MemoryStore services"},
		{"a handle of the wrong kind", func(_ *ir.Stack, services []stack.Service) {
			service(services, "shop-api").MemoryStores = []ir.ServiceRef{{Name: "shop-cache", Kind: ir.SchemaKindBucket}}
		}, stack.CodeKindMismatch, "holds a handle to shop-cache of kind Bucket, but shop-cache is a MemoryStore service"},
		{"an exposed store", func(s *ir.Stack, _ []stack.Service) {
			s.Expose = append(s.Expose, cache)
		}, stack.CodeExposeNotServer, "exposes shop-cache, a memoryStore; only a server, a site or an app is exposed"},
		{"env on a store", func(s *ir.Stack, _ []stack.Service) {
			s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: cache, Env: map[string]ir.EnvValue{"LOG_LEVEL": {Value: "debug"}}})
		}, stack.CodeUnknownEnvKey, "sets env LOG_LEVEL on memoryStore shop-cache, which has no config"},
		{"a schedule on a store", func(s *ir.Stack, _ []stack.Service) {
			s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: cache, Schedule: "* * * * *"})
		}, stack.CodeInvalidSettings, "sets a job's or a worker's settings on memory store shop-cache"},
		{"a setting the platform lacks", func(s *ir.Stack, _ []stack.Service) {
			s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: cache, Values: map[string]any{"replicas": 2}})
		}, stack.CodeInvalidSettings, "settings of shop-cache on platform fake.memorystore"},
		{"settings on a store that is off", func(s *ir.Stack, _ []stack.Service) {
			off := false
			s.Environment("Staging").Settings = append(s.Environment("Staging").Settings, &ir.DeployableSettings{Of: cache, Enabled: &off, Values: map[string]any{"nodeType": "x"}})
		}, stack.CodeInvalidSettings, "environment Staging turns memory store shop-cache off and gives it a platform or settings"},
		{"a field that claims the store's", func(_ *ir.Stack, services []stack.Service) {
			api := service(services, "shop-api")
			api.Config.Fields = append(api.Config.Fields, stack.ConfigField{Name: "SHOP_CACHE_MEMORY_STORE_URL"})
		}, stack.CodeFieldCollision, "config field SHOP_CACHE_MEMORY_STORE_URL of ShopApiConfig collides with SHOP_CACHE_MEMORY_STORE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, services := cacheShop()
			tc.edit(s, services)
			_, errs := resolve(t, reg, s, services, "Staging")
			mustFail(t, errs, tc.code, tc.want)
		})
	}
	// A store no API uses joins no stack.
	s, services := jobShop()
	services = append(services, stack.Service{Name: shopCache.Name, Kind: shopCache.Kind})
	env := mustResolve(t, reg, s, services, "Staging")
	if slices.Contains(names(env), "shop-cache") {
		t.Errorf("an unused store is a deployable: %v", names(env))
	}
}

// updateMemoryStoreGolden rewrites testdata/golden/memorystore:
//
//	go test ./internal/stack -run TestMemoryStoreEnvironmentGolden -update
var updateMemoryStoreGolden = flag.Bool("update", false, "rewrite the memory store environment goldens")

// TestMemoryStoreEnvironmentGolden: environment.json of the shop with
// shop-cache in Staging, where it is placed and reached, and in
// Production, which turns it off, so it lists it under disabled and holds
// no memoryStore deployable, edge or binding: the shape a target, a
// connector and a runtime read (D75).
func TestMemoryStoreEnvironmentGolden(t *testing.T) {
	s, services := cacheShop()
	offIn(s.Environment("Production"))
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production"} {
		env := mustResolve(t, reg, s, services, name)
		got, err := stack.Marshal(env)
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "golden", "memorystore", name, "environment.json")
		if *updateMemoryStoreGolden {
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
		if string(got) != string(want) {
			t.Errorf("%s differs from %s; run with -update and review the diff", name, golden)
		}
	}
}
