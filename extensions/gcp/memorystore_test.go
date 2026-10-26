package gcp_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// The gcp target's memory stores (D75): shop-api lists the MemoryStore
// service shop-cache, and so does shop-orders, so shop-api's server, the
// server Orders, the job ShipOrders and the worker FulfilOrders reach it.

var shopCache = ir.ServiceRef{Name: "shop-cache", Kind: ir.SchemaKindMemoryStore}

// withCache returns services with shop-cache, which shop-api and
// shop-orders list.
func withCache(services []stack.Service) []stack.Service {
	services = append(slices.Clone(services), stack.Service{Name: shopCache.Name, Kind: shopCache.Kind})
	for i := range services {
		if services[i].Name == stacktest.ShopAPI.Name || services[i].Name == stacktest.ShopOrders.Name {
			services[i].MemoryStores = append(slices.Clone(services[i].MemoryStores), shopCache)
		}
	}
	return services
}

// withCacheSettings returns s with Production's store on a larger node
// with two replicas.
func withCacheSettings(s *ir.Stack) *ir.Stack {
	prod := s.Environment("Production")
	prod.Settings = append(prod.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Values: map[string]any{"nodeType": "STANDARD_SMALL", "replicas": float64(2)}})
	return s
}

// cacheEnv resolves an environment of the shop with shop-cache.
func cacheEnv(t *testing.T, env string) *ir.ResolvedEnvironment {
	t.Helper()
	return resolve(t, assemble(t), withCacheSettings(shop()), withCache(stacktest.AcmeShop()), env)
}

// TestMemoryStorePlatform checks the Memorystore platform: shop-cache is a
// Memorystore for Valkey instance with cluster mode disabled and one
// shard, TLS and IAM authentication, on the environment's network, named
// after the project, the stack and the store, a SHARED_CORE_NANO node with
// one replica unless its settings say otherwise, as Production's do. The
// deployable's address is the rediss URL of the endpoint the instance
// creates. The network, the endpoints' subnet and the service connection
// policy come with it.
func TestMemoryStorePlatform(t *testing.T) {
	for _, c := range []struct {
		env, project, nodeType string
		replicas               int
	}{
		{"Staging", "acme-staging", "SHARED_CORE_NANO", 1},
		{"Production", "acme-prod", "STANDARD_SMALL", 2},
	} {
		t.Run(c.env, func(t *testing.T) {
			env := cacheEnv(t, c.env)
			d := env.Deployable("shop-cache")
			if d == nil || d.Kind != ir.DeployableMemoryStore || d.Platform != gcp.Memorystore {
				t.Fatalf("shop-cache is %+v, want a memory store on %s", d, gcp.Memorystore)
			}
			if d.ResourceName != c.project+"-shop-shop-cache" {
				t.Errorf("the instance id = %v", d.ResourceName)
			}
			wantJSON(t, "the address", d.Address, `{"$concat":["rediss://",`+
				`{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.ipAddress"}},":",`+
				`{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.port"}}]}`)
			if got, want := strings.Join(ownedBy(env, "shop-cache"), ", "), "network, network.memorystore, network.memorystore-subnet, network.nat, network.router, network.subnet, shop-cache.instance"; got != want {
				t.Errorf("shop-cache produces %s, want %s", got, want)
			}
			instance := node(t, env, "shop-cache.instance")
			if instance.Type != gcp.TypeMemoryStoreInstance || instance.Phase != ir.PhaseInfrastructure || instance.Inherited {
				t.Errorf("the instance is a %s in %s, inherited %v", instance.Type, instance.Phase, instance.Inherited)
			}
			if !slices.Contains(instance.DependsOn, "network.memorystore") {
				t.Errorf("the instance depends on %v, not on the service connection policy", instance.DependsOn)
			}
			wantJSON(t, "the instance", instance.Properties, `{"authorizationMode":"IAM_AUTH","deletionProtectionEnabled":false,`+
				`"desiredAutoCreatedEndpoints":[{"network":{"$output":{"resource":"network","name":"id"}},"projectId":"`+c.project+`"}],`+
				`"engineVersion":"VALKEY_8_0","instanceId":"`+c.project+`-shop-shop-cache","location":"us-east1","mode":"CLUSTER_DISABLED",`+
				`"nodeType":"`+c.nodeType+`","persistenceConfig":{"mode":"DISABLED"},"project":"`+c.project+`",`+
				`"replicaCount":`+mustJSON(t, c.replicas)+`,"shardCount":1,"transitEncryptionMode":"SERVER_AUTHENTICATION"}`)
			policy := node(t, env, "network.memorystore")
			if policy.Type != gcp.TypeServiceConnectionPolicy {
				t.Errorf("the policy is a %s", policy.Type)
			}
			wantJSON(t, "the policy", policy.Properties, `{"description":"Private Service Connect endpoints of the Memorystore instances of stack Shop",`+
				`"location":"us-east1","name":"shop-memorystore","network":{"$output":{"resource":"network","name":"id"}},"project":"`+c.project+`",`+
				`"pscConfig":{"subnetworks":[{"$output":{"resource":"network.memorystore-subnet","name":"id"}}]},"serviceClass":"gcp-memorystore"}`)
			wantJSON(t, "the endpoints' subnet", node(t, env, "network.memorystore-subnet").Properties,
				`{"ipCidrRange":"10.8.16.0/24","name":"shop-memorystore","network":{"$output":{"resource":"network","name":"id"}},"project":"`+c.project+`","region":"us-east1"}`)
		})
	}
}

// TestMemoryStoreSharesOnePolicy checks that two stores of an environment
// share the network's one service connection policy and subnet, and each
// has its own instance.
func TestMemoryStoreSharesOnePolicy(t *testing.T) {
	sessions := ir.ServiceRef{Name: "shop-sessions", Kind: ir.SchemaKindMemoryStore}
	services := append(withCache(stacktest.AcmeShop()), stack.Service{Name: sessions.Name, Kind: sessions.Kind})
	for i := range services {
		if services[i].Name == stacktest.ShopAPI.Name {
			services[i].MemoryStores = append(services[i].MemoryStores, sessions)
		}
	}
	env := resolve(t, assemble(t), shop(), services, "Staging")
	var policies, instances []string
	for _, res := range env.Resources.Resources {
		switch res.Type {
		case gcp.TypeServiceConnectionPolicy:
			policies = append(policies, res.ID)
		case gcp.TypeMemoryStoreInstance:
			instances = append(instances, res.ID)
		}
	}
	if !slices.Equal(policies, []string{"network.memorystore"}) || !slices.Equal(instances, []string{"shop-cache.instance", "shop-sessions.instance"}) {
		t.Errorf("policies %v, instances %v; want one policy and an instance per store", policies, instances)
	}
	if got := node(t, env, "network.memorystore").Owners; !slices.Contains(got, "shop-cache") || !slices.Contains(got, "shop-sessions") {
		t.Errorf("the policy's owners = %v, want both stores", got)
	}
}

// TestMemoryStoreSettingsRefusals checks the settings the platform
// refuses: a node type Memorystore does not have, a replica count outside
// 0 to 5, a fractional one, and a key it does not take; and a name longer
// than an instance id allows.
func TestMemoryStoreSettingsRefusals(t *testing.T) {
	reg := assemble(t)
	refused := func(t *testing.T, s *ir.Stack, code stack.Code, want string) {
		t.Helper()
		_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: withCache(stacktest.AcmeShop()), Environment: "Staging"})
		var errs *stack.Errors
		if !errors.As(err, &errs) || !slices.ContainsFunc(errs.List, func(e stack.Error) bool {
			return e.Code == code && strings.Contains(e.Error(), want)
		}) {
			t.Errorf("err = %v, want a %s error saying %q", err, code, want)
		}
	}
	settings := func(values map[string]any) *ir.Stack {
		s := shop()
		staging := s.Environment("Staging")
		staging.Settings = append(staging.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Values: values})
		return s
	}
	for name, values := range map[string]map[string]any{
		"an unknown node type": {"nodeType": "SHARED_CORE_PICO"},
		"a lowercase type":     {"nodeType": "shared_core_nano"},
		"six replicas":         {"replicas": float64(6)},
		"negative replicas":    {"replicas": float64(-1)},
		"half a replica":       {"replicas": 1.5},
		"replicas as a string": {"replicas": "1"},
		"a tier":               {"tier": "STANDARD_HA"},
	} {
		t.Run(name, func(t *testing.T) {
			refused(t, settings(values), stack.CodeInvalidSettings, "shop-cache")
		})
	}
	t.Run("no replicas", func(t *testing.T) {
		env := resolve(t, reg, settings(map[string]any{"replicas": float64(0)}), withCache(stacktest.AcmeShop()), "Staging")
		if got := node(t, env, "shop-cache.instance").Properties["replicaCount"]; mustJSON(t, got) != "0" {
			t.Errorf("replicaCount = %v, want 0", got)
		}
	})
	t.Run("a long name", func(t *testing.T) {
		s := shop()
		s.Name = "TheShopStackOfTheAcmeCompanyStorefrontAndWarehouses"
		refused(t, s, stack.CodeLowering, "the Memorystore instance id of shop-cache")
	})
}

// TestMemoryStoreInAMember checks that a member of a parameterized
// environment shares its parent's instance, subnet and policy, as it
// shares the network, and creates none of its own: each is inherited,
// under its parent's name. Its workloads still get their own grants, and
// their connection names the member, `pr` and the parameter's value, so
// its names on the parent's instance are its own.
func TestMemoryStoreInAMember(t *testing.T) {
	env := cacheEnv(t, "Preview")
	d := env.Deployable("shop-cache")
	if d == nil || d.ResourceName != "acme-staging-shop-shop-cache" {
		t.Fatalf("shop-cache is %+v, want the parent's instance", d)
	}
	for _, id := range []string{"shop-cache.instance", "network.memorystore", "network.memorystore-subnet", "network", "network.subnet"} {
		if res := node(t, env, id); !res.Inherited {
			t.Errorf("%s is the member's own, not its parent's", id)
		}
	}
	staging := cacheEnv(t, "Staging")
	wantJSON(t, "the inherited instance", node(t, env, "shop-cache.instance").Properties, mustJSON(t, node(t, staging, "shop-cache.instance").Properties))
	grant := node(t, env, "shop-api.memorystore.shop-cache")
	if grant.Inherited {
		t.Error("the member's grant is inherited; its account is its own")
	}
	wantJSON(t, "the member's grant member", grant.Properties["member"], `{"$output":{"resource":"shop-api.account","name":"member"}}`)
	wantJSON(t, "the member's connection", binding(t, env, "shop-api", "SHOP_CACHE_MEMORY_STORE").Value, `{"auth":"iam","ca":{"$output":{"resource":"shop-cache.instance","name":"managedServerCas[0].caCerts[0].certificates[0]"}},`+
		`"member":{"$concat":["pr",{"$parameter":"pr"}]},"store":"shop-cache",`+
		`"url":{"$concat":["rediss://",{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.ipAddress"}},":",`+
		`{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.port"}}]}}`)
}

// TestMemoryStoreConnectorGrants checks what the connector gives a server
// for its memoryStore edge: the connection role on the project, held to
// the edge's instance by a condition, Direct VPC egress on the
// environment's network, and the connection: the rediss URL, the managed
// server CA and IAM authentication, with no member outside a
// parameterized environment, one variable per member.
func TestMemoryStoreConnectorGrants(t *testing.T) {
	env := cacheEnv(t, "Staging")
	edge := "memoryStore:shop-api->shop-cache"
	if got := strings.Join(ownedBy(env, edge), ", "); got != "shop-api.memorystore.shop-cache" {
		t.Errorf("%s produces %s", edge, got)
	}
	for _, e := range env.Edges {
		if e.ID == edge && (e.Connector != gcp.MemoryStoreConnector || e.Field != "SHOP_CACHE_MEMORY_STORE") {
			t.Errorf("%s is connected by %s into %s", edge, e.Connector, e.Field)
		}
	}
	grant := node(t, env, "shop-api.memorystore.shop-cache")
	if grant.Type != gcp.TypeProjectIAMMember {
		t.Errorf("the grant is a %s, want a %s", grant.Type, gcp.TypeProjectIAMMember)
	}
	condition := grant.Properties["condition"].(map[string]any)
	if got, want := condition["expression"], `resource.type == "memorystore.googleapis.com/Instance" && `+
		`resource.name == "projects/acme-staging/locations/us-east1/instances/acme-staging-shop-shop-cache"`; got != want {
		t.Errorf("the grant's condition = %v, want %s", got, want)
	}
	if got := condition["title"]; got != "Memorystore instance acme-staging-shop-shop-cache" {
		t.Errorf("the grant's condition's title = %v", got)
	}
	delete(grant.Properties, "condition")
	wantJSON(t, "the grant", grant.Properties,
		`{"member":{"$output":{"resource":"shop-api.account","name":"member"}},"project":"acme-staging","role":"roles/memorystore.dbConnectionUser"}`)
	wantJSON(t, "the connection", binding(t, env, "shop-api", "SHOP_CACHE_MEMORY_STORE").Value, `{"auth":"iam","ca":{"$output":{"resource":"shop-cache.instance","name":"managedServerCas[0].caCerts[0].certificates[0]"}},"store":"shop-cache",`+
		`"url":{"$concat":["rediss://",{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.ipAddress"}},":",`+
		`{"$output":{"resource":"shop-cache.instance","name":"endpoints[0].connections[0].pscAutoConnection.port"}}]}}`)

	template := node(t, env, "shop-api.service").Properties["template"].(map[string]any)
	wantJSON(t, "shop-api's egress", template["vpcAccess"], `{"egress":"ALL_TRAFFIC","networkInterfaces":[{"network":{"$output":{"resource":"network","name":"name"}},`+
		`"subnetwork":{"$output":{"resource":"network.subnet","name":"name"}}}]}`)
	var names []string
	for _, e := range template["containers"].([]any)[0].(map[string]any)["envs"].([]any) {
		if name := e.(map[string]any)["name"].(string); strings.HasPrefix(name, "SHOP_CACHE_MEMORY_STORE") {
			names = append(names, name)
		}
	}
	if got := strings.Join(names, ", "); got != "SHOP_CACHE_MEMORY_STORE_AUTH, SHOP_CACHE_MEMORY_STORE_CA, SHOP_CACHE_MEMORY_STORE_STORE, SHOP_CACHE_MEMORY_STORE_URL" {
		t.Errorf("memory store variables = %s", got)
	}
}

// TestMemoryStoreEgress checks that a server that calls no other server
// and reaches a memory store joins the environment's network for it:
// shop-api calls no one, so it has no egress without the store.
func TestMemoryStoreEgress(t *testing.T) {
	reg := assemble(t)
	plain := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	template := node(t, plain, "shop-api.service").Properties["template"].(map[string]any)
	if _, ok := template["vpcAccess"]; ok {
		t.Fatal("shop-api has egress without a store; the test needs a server that calls no one")
	}
	env := resolve(t, reg, shop(), withCache(stacktest.AcmeShop()), "Staging")
	template = node(t, env, "shop-api.service").Properties["template"].(map[string]any)
	if _, ok := template["vpcAccess"]; !ok {
		t.Error("shop-api reaches shop-cache with no egress to its network")
	}
	if got := node(t, env, "network").Owners; !slices.Contains(got, "shop-api") || !slices.Contains(got, "shop-cache") {
		t.Errorf("the network's owners = %v, want shop-api and shop-cache among them", got)
	}

	// A store turned off reaches no workload, so it adds no egress.
	off := false
	s := shop()
	staging := s.Environment("Staging")
	staging.Settings = append(staging.Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Enabled: &off})
	env = resolve(t, reg, s, withCache(stacktest.AcmeShop()), "Staging")
	template = node(t, env, "shop-api.service").Properties["template"].(map[string]any)
	if _, ok := template["vpcAccess"]; ok {
		t.Error("shop-api has egress for a store turned off")
	}
	if env.Resources.Resource("network.memorystore") != nil || env.Resources.Resource("shop-cache.instance") != nil {
		t.Error("a store turned off lowers its instance or its policy")
	}
}

// TestMemoryStoreConnectorsOfEachWorkload checks that a job, a worker pool
// and a deriver take their API's memoryStore edges through their own
// platform's connector, with the grant for their own account and egress
// on the network.
func TestMemoryStoreConnectorsOfEachWorkload(t *testing.T) {
	services := withCache(stacktest.AcmeShop())
	for i := range services {
		switch services[i].Name {
		case "shop-db":
			services[i].Engine, services[i].Derived = true, []string{"Ticket"}
		case stacktest.ShopOrders.Name:
			services[i].Language = registry.APILanguageTypeScript
			services[i].Derivers = []ir.Deriver{{Name: "TriageTickets", Concurrency: 3}}
		}
	}
	env := resolve(t, assemble(t), shop(), services, "Staging")
	for _, c := range []struct{ from, connector, resource string }{
		{"Orders", gcp.MemoryStoreConnector, "Orders.service"},
		{stacktest.ShipOrdersJob, gcp.JobMemoryStoreConnector, stacktest.ShipOrdersJob + ".job"},
		{stacktest.FulfilOrdersWorker, gcp.WorkerMemoryStoreConnector, stacktest.FulfilOrdersWorker + ".worker-pool"},
		{"shop-orders-triage-tickets", gcp.DeriverMemoryStoreConnector, "shop-orders-triage-tickets.worker-pool"},
	} {
		t.Run(c.from, func(t *testing.T) {
			edge := "memoryStore:" + c.from + "->shop-cache"
			found := false
			for _, e := range env.Edges {
				if e.ID == edge {
					found = true
					if e.Connector != c.connector {
						t.Errorf("%s is connected by %s, want %s", edge, e.Connector, c.connector)
					}
				}
			}
			if !found {
				t.Fatalf("no edge %s", edge)
			}
			wantJSON(t, "the grant's member", node(t, env, c.from+".memorystore.shop-cache").Properties["member"],
				`{"$output":{"resource":"`+c.from+`.account","name":"member"}}`)
			props := node(t, env, c.resource).Properties
			template, _ := props["template"].(map[string]any)
			if inner, ok := template["template"].(map[string]any); ok {
				template = inner // a job's task template
			}
			if _, ok := template["vpcAccess"]; !ok {
				t.Errorf("%s has no egress to the store's network", c.resource)
			}
		})
	}
}

// TestPolicyMemoryStoresTLSAndIAM runs memory-stores-tls-and-iam over a
// resolved Staging changed one way at a time: an instance without transit
// encryption, or with authorization off, is refused, whatever owns it.
func TestPolicyMemoryStoresTLSAndIAM(t *testing.T) {
	reg := assemble(t)
	rule := policy(t, reg, gcp.PolicyMemoryStoresTLSAndIAM)
	if findings := rule.Check(cacheEnv(t, "Staging")); len(findings) > 0 {
		t.Fatalf("Staging as resolved has findings: %v", findings)
	}
	for _, c := range []struct {
		name   string
		change func(props map[string]any)
		want   string
	}{
		{"no transit encryption", func(p map[string]any) { p["transitEncryptionMode"] = "TRANSIT_ENCRYPTION_DISABLED" },
			`Memorystore instance shop-cache.instance of shop-cache has transit encryption "TRANSIT_ENCRYPTION_DISABLED"; a memory store sets SERVER_AUTHENTICATION`},
		{"transit encryption unset", func(p map[string]any) { delete(p, "transitEncryptionMode") }, "has transit encryption unset"},
		{"no authorization", func(p map[string]any) { p["authorizationMode"] = "AUTH_DISABLED" },
			`has authorization "AUTH_DISABLED"; a memory store sets IAM_AUTH`},
		{"authorization unset", func(p map[string]any) { delete(p, "authorizationMode") }, "has authorization unset"},
	} {
		t.Run(c.name, func(t *testing.T) {
			env := cacheEnv(t, "Staging")
			c.change(env.Resources.Resource("shop-cache.instance").Properties)
			findings := rule.Check(env)
			if len(findings) != 1 || !strings.Contains(findings[0], c.want) {
				t.Errorf("findings = %q, want one containing %q", findings, c.want)
			}
		})
	}

	env := cacheEnv(t, "Staging")
	env.Resources.Resources = append(env.Resources.Resources, &ir.Resource{
		ID: "Orders.cache", Type: gcp.TypeMemoryStoreInstance, Owners: []string{"Orders"},
		Properties: map[string]any{"transitEncryptionMode": "SERVER_AUTHENTICATION", "authorizationMode": "AUTH_DISABLED"},
	})
	if findings := rule.Check(env); len(findings) != 1 || !strings.Contains(findings[0], "Orders.cache of Orders") {
		t.Errorf("an instance a server owns: findings = %q", findings)
	}
}

// TestBootstrapEnablesMemorystore checks that a project whose environment
// has a memory store gets the Memorystore and Network Connectivity APIs,
// and one with none gets neither, and that deployer holds the roles that
// create an instance and a service connection policy in every project.
func TestBootstrapEnablesMemorystore(t *testing.T) {
	for _, c := range []struct {
		name     string
		services []stack.Service
		want     bool
	}{
		{"with a store", withCache(stacktest.AcmeShop()), true},
		{"without", stacktest.AcmeShop(), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newDeployFixture(t, false)
			env := resolve(t, f.reg, shop(), c.services, "Staging")
			target, _ := f.reg.Target(gcp.Target)
			req := registry.BootstrapRequest{Environment: env, Provisioner: f.prov, Dir: t.TempDir()}
			if _, err := target.Bootstrap.Bootstrap(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			changes := f.cloud.Changes()
			for _, api := range []string{"enable memorystore.googleapis.com", "enable networkconnectivity.googleapis.com"} {
				if slices.Contains(changes, api) != c.want {
					t.Errorf("%s: %v, want %v", api, !c.want, c.want)
				}
			}
		})
	}

	reg := assemble(t)
	graph, err := gcp.BootstrapEnvironment(cacheEnv(t, "Staging"), gcp.GitHubCI{})
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"roles/memorystore.admin", "roles/networkconnectivity.consumerNetworkAdmin", "roles/resourcemanager.projectIamAdmin", "roles/compute.networkAdmin"} {
		id := "deployer.role." + strings.ReplaceAll(strings.TrimPrefix(role, "roles/"), ".", "-")
		res := graph.Resources.Resource(id)
		if res == nil || res.Properties["role"] != role {
			t.Errorf("deployer does not hold %s (%s)", role, id)
			continue
		}
		if ok, err := reg.ValidateResource(res); !ok || err != nil {
			t.Errorf("%s: pinned %v, %v", id, ok, err)
		}
	}
	if graph.Resources.Resource("planner.role.memorystore-admin") != nil {
		t.Error("planner holds Memorystore's admin role")
	}
}

// TestMemoryStoreNoDeprecatedProperties walks every property the store's
// nodes set through the pinned schemas, in each environment.
func TestMemoryStoreNoDeprecatedProperties(t *testing.T) {
	var envs []*ir.ResolvedEnvironment
	for _, name := range []string{"Staging", "Production", "Preview"} {
		envs = append(envs, cacheEnv(t, name))
	}
	checkNoDeprecated(t, "", envs)
}
