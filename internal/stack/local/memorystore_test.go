package local_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The local target's memory store (D75): every memoryStore deployable of an
// environment is on one Valkey container, named for the checkout like the
// Postgres and storage containers, and each server, job, worker and deriver
// of an API that lists a store reaches it at the container's redis URL with
// no authentication.

// shopCache is the MemoryStore service the cache shop's APIs list.
var shopCache = ir.ServiceRef{Name: "shop-cache", Kind: ir.SchemaKindMemoryStore}

// withCache adds shop-cache to services: shop-api lists it in its
// memoryStores, and shop-orders names it its rateLimitStore, which lists it
// too, so Orders, its job and its worker reach it.
func withCache(services []stack.Service) []stack.Service {
	out := append(slices.Clone(services), stack.Service{Name: shopCache.Name, Kind: shopCache.Kind})
	for i := range out {
		if out[i].Name == stacktest.ShopAPI.Name || out[i].Name == stacktest.ShopOrders.Name {
			out[i].MemoryStores = []ir.ServiceRef{shopCache}
		}
	}
	return out
}

// cacheStack is the shop stack, named for the cache shop's goldens.
func cacheStack() *ir.Stack {
	s := shop()
	s.Name = "cache-stack"
	return s
}

// cacheDeployables are the deployables that reach shop-cache.
var cacheDeployables = []string{"shop-api", "Orders", stacktest.ShipOrdersJob, stacktest.FulfilOrdersWorker}

// TestMemoryStoreGolden resolves the cache shop in Dev and Pinned: the
// environment.json holds the Valkey container node, the memoryStore
// deployable and edges, and each workload's SHOP_CACHE_MEMORY_STORE
// binding; the program holds the container.
func TestMemoryStoreGolden(t *testing.T) {
	reg := assemble(t)
	for _, env := range []string{"Dev", "Pinned"} {
		t.Run(env, func(t *testing.T) {
			checkGoldenEnvironment(t, reg, cacheStack(), withCache(stacktest.AcmeShop()), env)
		})
	}
}

// TestMemoryStoreOnValkey: shop-cache is placed on local.valkey, named
// after its service, and lowers to the environment's Valkey container: its
// image, its port on loopback, no persistence, its labels and a readiness
// check valkey-cli's PING makes. Each workload reaches the store over a
// memoryStore edge through the connector of its platform, whose value
// names the store, the container's redis URL at database 0 and no
// authentication, and which the program passes on as the field's
// variables.
func TestMemoryStoreOnValkey(t *testing.T) {
	env := resolve(t, assemble(t), cacheStack(), withCache(stacktest.AcmeShop()), "Dev")
	senv := registry.StackEnvironment{Stack: env.Stack, Name: env.Environment, Values: env.Values, SchemasRoot: checkoutRoot}
	port := local.MemoryStorePort(senv)
	if port < 26000 || port >= 26000+2768 {
		t.Errorf("port %d is outside the memory store's range", port)
	}
	url := fmt.Sprintf("redis://127.0.0.1:%d/0", port)
	if got := local.MemoryStoreURL(port); got != url {
		t.Errorf("MemoryStoreURL = %s, want %s", got, url)
	}

	cache := env.Deployable("shop-cache")
	if cache == nil || cache.Kind != ir.DeployableMemoryStore || cache.Platform != local.MemoryStorePlatform {
		t.Fatalf("shop-cache is %+v, want a memoryStore on %s", cache, local.MemoryStorePlatform)
	}
	if cache.ResourceName != "shop-cache" || cache.Address != url {
		t.Errorf("shop-cache is named %v at %v, want shop-cache at %s", cache.ResourceName, cache.Address, url)
	}

	c := env.Resources.Resource(local.MemoryStoreContainer)
	if c == nil {
		t.Fatalf("no %s node", local.MemoryStoreContainer)
	}
	checkout := local.Checkout(checkoutRoot, env.Stack, env.Environment)
	name := local.MemoryStoreContainerName(env.Stack, env.Environment, checkout)
	if name != "superschematic-cache-stack-dev-"+checkout+"-memorystore" {
		t.Errorf("container name %s", name)
	}
	if c.Type != local.TypeContainer || c.Phase != ir.PhaseInfrastructure || c.Properties["name"] != name || c.Properties["image"] != local.DefaultMemoryStoreImage {
		t.Errorf("container node %+v", c)
	}
	if !slices.Contains(c.Owners, "shop-cache") {
		t.Errorf("container owners %v, want shop-cache among them", c.Owners)
	}
	props, err := json.Marshal(c.Properties)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		fmt.Sprintf(`"ports":[{"containerPort":6379,"host":"127.0.0.1","hostPort":%d}]`, port),
		`"args":["valkey-server","--save","","--appendonly","no"]`,
		`"readiness":{"exec":["valkey-cli","-p","6379","ping"]}`,
		`"superschematic.stack":"cache-stack"`,
	} {
		if !strings.Contains(string(props), want) {
			t.Errorf("container properties %s lack %s", props, want)
		}
	}

	connectors := map[string]string{
		"shop-api":                   local.MemoryStoreConnector,
		"Orders":                     local.MemoryStoreConnector,
		stacktest.ShipOrdersJob:      local.JobMemoryStoreConnector,
		stacktest.FulfilOrdersWorker: local.WorkerMemoryStoreConnector,
	}
	for _, d := range cacheDeployables {
		id := "memoryStore:" + d + "->shop-cache"
		i := slices.IndexFunc(env.Edges, func(e *ir.Edge) bool { return e.ID == id })
		if i < 0 {
			t.Errorf("no edge %s", id)
			continue
		}
		if e := env.Edges[i]; e.Connector != connectors[d] || e.Field != "SHOP_CACHE_MEMORY_STORE" {
			t.Errorf("%s by %s into %s, want %s into SHOP_CACHE_MEMORY_STORE", id, e.Connector, e.Field, connectors[d])
		}
		var binding *ir.Binding
		for _, b := range env.Deployable(d).Bindings {
			if b.Field == "SHOP_CACHE_MEMORY_STORE" {
				binding = b
			}
		}
		if binding == nil {
			t.Errorf("%s has no SHOP_CACHE_MEMORY_STORE binding", d)
			continue
		}
		if want := map[string]any{"store": "shop-cache", "url": url, "auth": "none"}; !equalJSON(t, binding.Value, want) {
			t.Errorf("%s's connection = %v, want %v", d, binding.Value, want)
		}
	}

	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(prog.Containers, func(c *local.Container) bool { return c.ID == local.MemoryStoreContainer })
	if i < 0 {
		t.Fatalf("the program holds no %s", local.MemoryStoreContainer)
	}
	if pc := prog.Containers[i]; pc.Name != name || pc.HostPort != port || pc.ContainerPort != 6379 || pc.Readiness == nil || len(pc.Readiness.Exec) == 0 {
		t.Errorf("program container %+v", pc)
	}
	for _, s := range prog.Servers {
		if s.Deployable != "shop-api" {
			continue
		}
		got := map[string]any{}
		for _, v := range s.Env {
			got[v.Name] = v.Value
		}
		if got["SHOP_CACHE_MEMORY_STORE_STORE"] != "shop-cache" || got["SHOP_CACHE_MEMORY_STORE_URL"] != url || got["SHOP_CACHE_MEMORY_STORE_AUTH"] != "none" {
			t.Errorf("shop-api's memory store variables are %v %v %v", got["SHOP_CACHE_MEMORY_STORE_STORE"], got["SHOP_CACHE_MEMORY_STORE_URL"], got["SHOP_CACHE_MEMORY_STORE_AUTH"])
		}
		for _, unset := range []string{"SHOP_CACHE_MEMORY_STORE_CA", "SHOP_CACHE_MEMORY_STORE_MEMBER"} {
			if _, ok := got[unset]; ok {
				t.Errorf("shop-api sets %s", unset)
			}
		}
	}
}

// TestMemoryStoreInEveryLanguage: a TypeScript or Rust server, job or
// worker reaches the store through the same connectors, with the same
// connection, as a Go one: the field is the same in every language.
func TestMemoryStoreInEveryLanguage(t *testing.T) {
	reg := assemble(t)
	goEnv := resolve(t, reg, cacheStack(), withCache(stacktest.AcmeShop()), "Dev")
	for _, lang := range []string{registry.APILanguageTypeScript, registry.APILanguageRust} {
		services := withCache(stacktest.AcmeShop())
		for i := range services {
			if services[i].Name == stacktest.ShopAPI.Name || services[i].Name == stacktest.ShopOrders.Name {
				services[i].Language = lang
			}
		}
		env := resolve(t, reg, cacheStack(), services, "Dev")
		for _, d := range cacheDeployables {
			if got := env.Deployable(d); got == nil || got.Language != lang {
				t.Fatalf("%s: %s is %+v", lang, d, got)
			}
			id := "memoryStore:" + d + "->shop-cache"
			i := slices.IndexFunc(env.Edges, func(e *ir.Edge) bool { return e.ID == id })
			j := slices.IndexFunc(goEnv.Edges, func(e *ir.Edge) bool { return e.ID == id })
			if i < 0 || j < 0 || env.Edges[i].Connector != goEnv.Edges[j].Connector {
				t.Errorf("%s: %s is not connected as a Go one is", lang, id)
				continue
			}
			if !equalJSON(t, cacheBinding(env.Deployable(d)), cacheBinding(goEnv.Deployable(d))) {
				t.Errorf("%s: %s's SHOP_CACHE_MEMORY_STORE differs from a Go one's", lang, d)
			}
		}
	}
}

// cacheBinding is d's SHOP_CACHE_MEMORY_STORE binding's value, or nil.
func cacheBinding(d *ir.ResolvedDeployable) any {
	for _, b := range d.Bindings {
		if b.Field == "SHOP_CACHE_MEMORY_STORE" {
			return b.Value
		}
	}
	return nil
}

// TestMemoryStoreTurnedOff: `enabled: false` creates nothing: no
// deployable, no container, no edge and no field, and the environment
// lists the store as disabled, so every workload opens it disabled.
func TestMemoryStoreTurnedOff(t *testing.T) {
	s := cacheStack()
	off := false
	s.Environments[0].Settings = append(s.Environments[0].Settings, &ir.DeployableSettings{Of: stacktest.Of(shopCache), Enabled: &off})
	env := resolve(t, assemble(t), s, withCache(stacktest.AcmeShop()), "Dev")
	if env.Deployable("shop-cache") != nil {
		t.Error("shop-cache is a deployable")
	}
	if env.Resources.Resource(local.MemoryStoreContainer) != nil {
		t.Error("the environment runs a Valkey container for a store that is off")
	}
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeMemoryStore {
			t.Errorf("edge %s reaches a store that is off", e.ID)
		}
	}
	for _, d := range env.Deployables {
		for _, b := range d.Bindings {
			if strings.HasPrefix(b.Field, "SHOP_CACHE_MEMORY_STORE") {
				t.Errorf("%s binds %s", d.Name, b.Field)
			}
		}
	}
	if len(env.Disabled) != 1 || env.Disabled[0].Name != "shop-cache" || env.Disabled[0].Kind != ir.DeployableMemoryStore {
		t.Errorf("disabled = %+v, want shop-cache", env.Disabled)
	}
	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range prog.Containers {
		if c.ID == local.MemoryStoreContainer {
			t.Error("the program holds the Valkey container")
		}
	}
}

// TestMemoryStoreValues: memoryStoreImage and memoryStorePort set the
// container's image and port, and a port another container takes is
// refused by local-distinct-ports.
func TestMemoryStoreValues(t *testing.T) {
	s := cacheStack()
	s.Environments[0].Values = map[string]any{"memoryStoreImage": "valkey/valkey:8.0.3-alpine", "memoryStorePort": float64(26379)}
	env := resolve(t, assemble(t), s, withCache(stacktest.AcmeShop()), "Dev")
	c := env.Resources.Resource(local.MemoryStoreContainer)
	if c.Properties["image"] != "valkey/valkey:8.0.3-alpine" {
		t.Errorf("image %v", c.Properties["image"])
	}
	if got := env.Deployable("shop-cache").Address; got != "redis://127.0.0.1:26379/0" {
		t.Errorf("address %v", got)
	}

	clash := cacheStack()
	clash.Environments[1].Values["memoryStorePort"] = float64(55432)
	checkout := local.Checkout(checkoutRoot, clash.Name, "Pinned")
	wantFailure(t, resolveErr(t, clash, withCache(stacktest.AcmeShop()), "Pinned"), stack.CodePolicy,
		"container "+local.MemoryStoreContainerName(clash.Name, "Pinned", checkout)+" and container "+local.ContainerName(clash.Name, "Pinned", checkout)+" listen on one port, 55432")
}

// TestApplyMemoryStore: the infrastructure step creates the Valkey
// container with no persistence, waits until valkey-cli's PING answers in
// it, and creates nothing on it; Destroy stops it and Purge removes it,
// with nothing else to remove: the store keeps no data.
func TestApplyMemoryStore(t *testing.T) {
	f := newFixtureWith(t, cacheStack(), withCache(stacktest.AcmeShop()), "Dev")
	port := local.MemoryStorePort(f.senv())
	name := local.MemoryStoreContainerName(f.env.Stack, f.env.Environment, f.checkout)
	f.runner.inUse = append(f.runner.inUse, port)
	f.runner.rules = []rule{
		{prefix: inspectOf(name), times: 1, stderr: "Error response from daemon: No such container: " + name},
		{prefix: "/bin/docker exec " + name + " valkey-cli", times: 1, stderr: "Could not connect to Valkey at 127.0.0.1:6379: Connection refused"},
	}
	if err := f.prov.Apply(context.Background(), f.req, ir.DeployStep{Step: ir.StepInfrastructure, Resources: []string{local.MemoryStoreContainer}}); err != nil {
		t.Fatalf("%v\n%s", err, f.out)
	}
	run := fmt.Sprintf(`/bin/docker run --detach --name %s --label superschematic.checkout=%s --label superschematic.environment=Dev --label superschematic.stack=cache-stack --publish 127.0.0.1:%d:6379 %s valkey-server --save  --appendonly no`, name, f.checkout, port, local.DefaultMemoryStoreImage)
	ping := "/bin/docker exec " + name + " valkey-cli -p 6379 ping"
	lines := f.runner.lines(f.req.Dir)
	if !slices.Contains(lines, run) {
		t.Errorf("no %q among:\n%s", run, strings.Join(lines, "\n"))
	}
	if n := countLines(lines, ping); n != 2 {
		t.Errorf("%d PINGs, want 2 (one refused):\n%s", n, strings.Join(lines, "\n"))
	}
	for _, l := range lines {
		if strings.Contains(l, "createdb") || strings.Contains(l, "POST") {
			t.Errorf("the store created %q", l)
		}
	}
	// The Valkey run's command carries the empty --save argument as its
	// own argument, which no shell sees.
	for _, cmd := range f.runner.commands {
		if slices.Contains(cmd.Args, "valkey-server") {
			if i := slices.Index(cmd.Args, "--save"); i < 0 || cmd.Args[i+1] != "" {
				t.Errorf("valkey-server args %q, want --save \"\"", cmd.Args)
			}
		}
	}

	running := fmt.Sprintf(`true|%s|{"6379/tcp":[{"HostIp":"127.0.0.1","HostPort":"%d"}]}`+"\n", local.DefaultMemoryStoreImage, port)
	f.runner.commands = nil
	f.runner.rules = append(f.runningContainers("postgres:16-alpine"), rule{prefix: inspectOf(name), out: running})
	if err := f.prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if lines := f.runner.lines(f.req.Dir); !slices.Contains(lines, "/bin/docker stop "+name) {
		t.Errorf("Destroy did not stop %s:\n%s", name, strings.Join(lines, "\n"))
	}
	f.runner.commands = nil
	if err := f.prov.Purge(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if lines := f.runner.lines(f.req.Dir); !slices.Contains(lines, "/bin/docker rm --force --volumes "+name) {
		t.Errorf("Purge did not remove %s:\n%s", name, strings.Join(lines, "\n"))
	}
	if !strings.Contains(f.out.String(), "removed container "+name) {
		t.Errorf("Purge's output:\n%s", f.out)
	}
}

func countLines(lines []string, want string) int {
	n := 0
	for _, l := range lines {
		if l == want {
			n++
		}
	}
	return n
}

// TestMemoryStoreContainerRuns starts the Valkey container for real, with
// Docker, through the provisioner's infrastructure step, PINGs it at the
// URL the store's edge derives, finds it keeps nothing across a restart,
// and removes it with Purge.
func TestMemoryStoreContainerRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the memory store runs Docker")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("Docker is not available: %v", err)
	}
	s := cacheStack()
	s.Name = fmt.Sprintf("local-valkey-it-%d", os.Getpid())
	port := freePort(t)
	s.Environments[0].Values = map[string]any{"memoryStorePort": float64(port)}
	f := newFixtureWith(t, s, withCache(stacktest.AcmeShop()), "Dev")
	out := &syncBuffer{}
	prov := &local.Provisioner{Out: out, ReadyTimeout: 2 * time.Minute}
	name := local.MemoryStoreContainerName(f.env.Stack, f.env.Environment, f.checkout)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", "--volumes", name).Run() })
	step := ir.DeployStep{Step: ir.StepInfrastructure, Resources: []string{local.MemoryStoreContainer}}
	if err := prov.Apply(context.Background(), f.req, step); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if got := local.MemoryStoreURL(port); got != "redis://"+addr+"/0" {
		t.Fatalf("URL %s", got)
	}
	if got := valkey(t, addr, "PING"); got != "+PONG" {
		t.Fatalf("PING = %q", got)
	}
	if got := valkey(t, addr, "SET", "shop-cache:probe", "1"); got != "+OK" {
		t.Fatalf("SET = %q", got)
	}
	if got := valkey(t, addr, "CONFIG", "GET", "save"); !strings.Contains(got, "save") {
		t.Errorf("CONFIG GET save = %q", got)
	}
	if err := prov.Destroy(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if err := prov.Apply(context.Background(), f.req, step); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := valkey(t, addr, "EXISTS", "shop-cache:probe"); got != ":0" {
		t.Errorf("after a restart EXISTS = %q, want :0: the store keeps nothing", got)
	}
	if err := prov.Purge(context.Background(), f.req); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("docker", "container", "inspect", name).Run(); err == nil {
		t.Errorf("container %s is still there after Purge", name)
	}
}

// valkey sends one command to the server at addr in RESP and returns the
// first line of its reply, the rest of a multi-line reply joined after it.
func valkey(t *testing.T, addr string, args ...string) string {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	if _, err := conn.Write([]byte(b.String())); err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	reply := strings.TrimRight(line, "\r\n")
	if strings.HasPrefix(reply, "*") {
		var n int
		_, _ = fmt.Sscanf(reply, "*%d", &n)
		for i := 0; i < 2*n; i++ {
			l, err := r.ReadString('\n')
			if err != nil {
				break
			}
			reply += " " + strings.TrimRight(l, "\r\n")
		}
	}
	return reply
}
