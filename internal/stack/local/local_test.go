package local_test

import (
	"bytes"
	"errors"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden environment.json and local.json files")

// goldenRoot holds the golden files, laid out as a build and a render
// write them: stack/<stack>/<environment>/environment.json and
// program/<stack>/<environment>/local.json.
const goldenRoot = "testdata/golden"

// assemble is the registry a core binary assembles: the core registers the
// local target.
func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// shop is stacktest's shop stack of docs/stack-model.md, section 4.1, with
// local environments: Dev takes every default, and Pinned sets the
// Postgres image and port, each server's port, and runs shop-orders' job
// every minute.
func shop() *ir.Stack {
	s := stacktest.Shop()
	orders := ir.DeployableRef{Deployable: "Orders"}
	s.Environments = []*ir.Environment{
		{
			Name:   "Dev",
			Target: local.Target,
			Settings: []*ir.DeployableSettings{
				{Of: orders, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}},
			},
		},
		{
			Name:   "Pinned",
			Target: local.Target,
			Values: map[string]any{"postgresImage": "postgres:17-alpine", "postgresPort": float64(55432)},
			Settings: []*ir.DeployableSettings{
				{Of: stacktest.Of(stacktest.ShopAPI), Values: map[string]any{"port": float64(8080)}, Env: map[string]ir.EnvValue{"LOG_LEVEL": {Value: "debug"}}},
				{Of: orders, Values: map[string]any{"port": float64(8081)}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "eu"}}},
				{Of: stacktest.JobOf(stacktest.ShopOrders, "ShipOrders"), Schedule: "* * * * *"},
				{Of: stacktest.WorkerOf(stacktest.ShopOrders, "FulfilOrders"), Concurrency: &pinnedConcurrency},
			},
		},
	}
	return s
}

// pinnedConcurrency is the concurrency Pinned gives shop-orders' worker,
// which its process reads from WORKER_CONCURRENCY (D53).
var pinnedConcurrency = 2

// checkoutRoot is the schemas root of the checkout the goldens and most
// tests resolve in: a path no test creates, since resolution only hashes
// it.
const checkoutRoot = "/checkout/schemas"

// resolve resolves env of s in the checkout at checkoutRoot.
func resolve(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) *ir.ResolvedEnvironment {
	t.Helper()
	return resolveIn(t, reg, checkoutRoot, s, services, env)
}

// resolveIn resolves env of s in the checkout whose schemas root is
// schemasRoot, or in none when it is empty.
func resolveIn(t *testing.T, reg *registry.Registry, schemasRoot string, s *ir.Stack, services []stack.Service, env string) *ir.ResolvedEnvironment {
	t.Helper()
	resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: env, SchemasRoot: schemasRoot})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// postgresName and storageName are the names of the containers of a shop
// stack's environment in the checkout at checkoutRoot.
func postgresName(stackName, env string) string {
	return local.ContainerName(stackName, env, local.Checkout(checkoutRoot, stackName, env))
}

func storageName(stackName, env string) string {
	return local.StorageContainerName(stackName, env, local.Checkout(checkoutRoot, stackName, env))
}

// checkGolden compares got with the golden file at path, which -update
// rewrites.
func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("output differs from %s; run with -update and review the diff:\n%s", path, got)
	}
}

// TestGolden resolves the shop stack in each local environment and checks
// the environment.json it writes, resource graph included, and the program
// the provisioner renders from it, against the golden files. Resolution
// validated every node against the schema the target registers.
func TestGolden(t *testing.T) {
	reg := assemble(t)
	s := shop()
	for _, env := range s.Environments {
		t.Run(env.Name, func(t *testing.T) {
			checkGoldenEnvironment(t, reg, s, stacktest.AcmeShop(), env.Name)
		})
	}
}

// TestServiceAuthGolden resolves the shop with a service clause on
// shop-api, which Orders calls: require-stack's @requireService and
// allow-stack's @allowService, whose shop-orders also has a clause no
// server calls. shop-api's server gets SHOP_API_CALLERS, Orders as an
// issuer of its own with the edge's public key, and allow-stack's Orders
// gets SHOP_ORDERS_CALLERS with no issuers.
func TestServiceAuthGolden(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		stack    string
		services []stack.Service
	}{
		{"require-stack", stacktest.RequireServiceShop()},
		{"allow-stack", stacktest.AllowServiceShop()},
	} {
		t.Run(tc.stack, func(t *testing.T) {
			s := shop()
			s.Name = tc.stack
			checkGoldenEnvironment(t, reg, s, tc.services, "Dev")
		})
	}
}

// storefront is the shop with shop-storefront deployed and exposed too: a
// TypeScript API, whose server Bun runs (D51).
func storefront() *ir.Stack {
	s := shop()
	s.Name = "storefront-stack"
	ref := ir.ServiceRef{Name: "shop-storefront", Kind: ir.SchemaKindAPI}
	s.Deploy = append(s.Deploy, ref)
	s.Expose = append(s.Expose, stacktest.Of(ref))
	return s
}

// TestTypeScriptGolden resolves the shop with its TypeScript storefront
// in Dev: the storefront's process is a TypeScript one, which runs its
// entrypoint's main.ts on Bun with no binary to build.
func TestTypeScriptGolden(t *testing.T) {
	checkGoldenEnvironment(t, assemble(t), storefront(), stacktest.AcmeShop(), "Dev")
}

// rustStack is the shop with shop-api written in Rust (D57), over
// rustShop's services.
func rustStack() *ir.Stack {
	s := shop()
	s.Name = "rust-stack"
	return s
}

// rustShop is the acme-shop services with shop-api written in Rust and no
// bucket, which a Rust server does not reach (D57): its server, which
// reaches shop-db through its authDb, is a Rust process.
func rustShop() []stack.Service {
	return stacktest.InRust(stacktest.WithoutBuckets(stacktest.AcmeShop()), "shop-api")
}

// TestRustGolden resolves the shop with shop-api in Rust in Dev: shop-api's
// process is a Rust one, with the sql edge and port a Go server's has, and
// no binary in the program, since the provisioner reads the binary's name
// from the crate as it builds it.
func TestRustGolden(t *testing.T) {
	checkGoldenEnvironment(t, assemble(t), rustStack(), rustShop(), "Dev")
	env := resolve(t, assemble(t), rustStack(), rustShop(), "Dev")
	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range prog.Servers {
		want := local.LanguageGo
		if s.Deployable == "shop-api" {
			want = local.LanguageRust
		}
		if s.Language != want || (want == local.LanguageRust) != (s.Binary == "") {
			t.Errorf("server %s is %s with binary %q, want %s, with a binary when it is Go", s.Deployable, s.Language, s.Binary, want)
		}
	}
}

// rustOrdersStack is the shop with shop-orders written in Rust (D65), over
// rustOrdersShop's services.
func rustOrdersStack() *ir.Stack {
	s := shop()
	s.Name = "rust-orders-stack"
	return s
}

// rustOrdersShop is the acme-shop services with shop-orders written in
// Rust: its server Orders, its job ShipOrders and its worker FulfilOrders
// are Rust processes. shop-api stays in Go with its bucket, and
// shop-orders lists none.
func rustOrdersShop() []stack.Service {
	return stacktest.InRust(stacktest.AcmeShop(), "shop-orders")
}

// TestRustJobAndWorkerGolden resolves the shop with shop-orders in Rust in
// Dev (D65): its job is on local.job and its worker on local.worker, in
// Rust, with the edges and the environment a Go one's has. The program
// holds the job with no binary, as it holds a Rust server, since the
// provisioner reads the binary's name from the crate as it builds it, and
// the worker as a Rust process with no port and no binary.
func TestRustJobAndWorkerGolden(t *testing.T) {
	reg := assemble(t)
	checkGoldenEnvironment(t, reg, rustOrdersStack(), rustOrdersShop(), "Dev")
	env := resolve(t, reg, rustOrdersStack(), rustOrdersShop(), "Dev")
	goEnv := resolve(t, reg, rustOrdersStack(), stacktest.AcmeShop(), "Dev")
	for name, platform := range map[string]string{
		"Orders":                     local.ServerPlatform,
		stacktest.ShipOrdersJob:      local.JobPlatform,
		stacktest.FulfilOrdersWorker: local.WorkerPlatform,
	} {
		d, goD := env.Deployable(name), goEnv.Deployable(name)
		if d == nil || d.Platform != platform || d.Language != registry.APILanguageRust {
			t.Fatalf("%s is %+v, want a Rust deployable on %s", name, d, platform)
		}
		if !equalJSON(t, d.Bindings, goD.Bindings) {
			t.Errorf("%s's bindings differ from a Go %s's", name, d.Kind)
		}
	}
	prog, err := local.ProgramOf(env)
	if err != nil {
		t.Fatal(err)
	}
	if job := prog.JobOf(stacktest.ShipOrdersJob); job == nil || job.Language != local.LanguageRust || job.Binary != "" {
		t.Errorf("the program's job = %+v, want a Rust job with no binary", job)
	}
	var worker *local.Server
	for _, s := range prog.Servers {
		if s.Deployable == stacktest.FulfilOrdersWorker {
			worker = s
		}
	}
	if worker == nil || worker.Language != local.LanguageRust || worker.Kind != "worker" || worker.Binary != "" || worker.Port != 0 {
		t.Errorf("the program's worker = %+v, want a Rust process with no binary and no port", worker)
	}
}

// checkGoldenEnvironment resolves env of s and checks its environment.json
// and the program the provisioner renders from it against the golden
// files.
func checkGoldenEnvironment(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) {
	t.Helper()
	resolved := resolve(t, reg, s, services, env)
	got, err := stack.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, stack.EnvironmentPath(goldenRoot, s.Name, env), got)

	back, err := stack.Unmarshal(got)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := (&local.Provisioner{}).Render(back, dir); err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(filepath.Join(dir, local.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join(goldenRoot, "program", s.Name, env, local.ProgramFile), program)
}

// TestWiring reads the resolved Dev environment for what the local
// platforms and connectors derive: one Postgres container both databases'
// lowering shares, a connection string to it per sql edge, the callee's
// loopback URL per http edge with a token signed by the edge's key, whose
// private half only a reference names, and ports that stay put between
// runs and differ between environments.
func TestWiring(t *testing.T) {
	reg := assemble(t)
	dev := resolve(t, reg, shop(), stacktest.AcmeShop(), "Dev")
	if dev.Provisioner != local.ProvisionerName {
		t.Errorf("provisioner = %q, want %q", dev.Provisioner, local.ProvisionerName)
	}
	env := registry.StackEnvironment{Stack: "shop-stack", Name: "Dev", SchemasRoot: checkoutRoot}
	pgPort := local.PostgresPort(env)
	shopAPI := dev.Deployable("shop-api")
	apiPort := local.ServerPort(env, *shopAPI)
	if apiPort == local.ServerPort(registry.StackEnvironment{Stack: "shop-stack", Name: "Other", SchemasRoot: checkoutRoot}, *shopAPI) {
		t.Errorf("shop-api gets port %d in two environments", apiPort)
	}
	if got, want := shopAPI.Address, local.ServerURL(apiPort); got != want {
		t.Errorf("shop-api address = %v, want %s", got, want)
	}

	values := map[string]any{}
	for _, b := range dev.Deployable("Orders").Bindings {
		values[b.Field] = b.Value
	}
	wantDB := map[string]any{"url": local.DatabaseURL(pgPort, "shop_db")}
	if got := values["SHOP_DB_DATABASE"]; !equalJSON(t, got, wantDB) {
		t.Errorf("Orders SHOP_DB_DATABASE = %v, want %v", got, wantDB)
	}
	wantAPI := map[string]any{"url": local.ServerURL(apiPort), "credential": map[string]any{
		"source":   "signed-token",
		"audience": "shop-api",
		"issuer":   "Orders",
		"key":      ir.Output{Resource: "Orders.calls.shop-api.key", Name: "privateJwk"},
	}}
	if got := values["SHOP_API_SERVICE"]; !equalJSON(t, got, wantAPI) {
		t.Errorf("Orders SHOP_API_SERVICE = %v, want %v", got, wantAPI)
	}

	// shop-orders' job is a caller of its own, with a key pair of its own
	// (D52).
	for _, b := range dev.Deployable(stacktest.ShipOrdersJob).Bindings {
		values[b.Field] = b.Value
	}
	wantAPI["credential"].(map[string]any)["issuer"] = stacktest.ShipOrdersJob
	wantAPI["credential"].(map[string]any)["key"] = ir.Output{Resource: stacktest.ShipOrdersJob + ".calls.shop-api.key", Name: "privateJwk"}
	if got := values["SHOP_API_SERVICE"]; !equalJSON(t, got, wantAPI) {
		t.Errorf("the job's SHOP_API_SERVICE = %v, want %v", got, wantAPI)
	}

	clause := resolve(t, reg, stacktest.WithoutWorkerSettings(shop()), stacktest.WithoutWorkers(stacktest.WithoutJobs(stacktest.RequireServiceShop())), "Dev")
	var callers *ir.Binding
	for _, b := range clause.Deployable("shop-api").Bindings {
		if b.Field == "SHOP_API_CALLERS" {
			callers = b
		}
	}
	wantCallers := map[string]any{"issuers": []any{map[string]any{
		"issuer":             "Orders",
		"audience":           "shop-api",
		"algorithms":         []any{"EdDSA"},
		"keys":               []any{map[string]any{"jwk": ir.Output{Resource: "Orders.calls.shop-api.key", Name: "publicJwk"}}},
		"maxLifetimeSeconds": float64(300),
		"callers":            []any{map[string]any{"subject": "Orders", "deployable": "Orders", "serves": []any{"shop-orders"}}},
	}}}
	if callers == nil || callers.CallersOf != "shop-api" || !equalJSON(t, callers.Value, wantCallers) {
		t.Errorf("shop-api SHOP_API_CALLERS = %+v, want Orders, verified with its edge's public key", callers)
	}

	container := dev.Resources.Resource(local.PostgresContainer)
	if container == nil || strings.Join(container.Owners, ",") != "shop-db" {
		t.Fatalf("container = %+v, want one owned by shop-db", container)
	}
	if got, want := container.Properties["name"], postgresName("shop-stack", "Dev"); got != want {
		t.Errorf("container name = %v, want %s", got, want)
	}

	again := resolve(t, reg, shop(), stacktest.AcmeShop(), "Dev")
	a, _ := stack.Marshal(dev)
	b, _ := stack.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("two resolutions of Dev differ")
	}
}

// listeners are the host ports of an environment's containers and the
// ports of its servers, by node.
func listeners(env *ir.ResolvedEnvironment) map[string]int {
	number := func(v any) (int, bool) {
		switch v := v.(type) {
		case int:
			return v, true
		case float64:
			return int(v), true
		}
		return 0, false
	}
	ports := map[string]int{}
	for _, res := range env.Resources.Resources {
		switch res.Type {
		case local.TypeContainer:
			published := res.Properties["ports"].([]any)[0].(map[string]any)
			ports[res.ID], _ = number(published["hostPort"])
		case local.TypeProcess, local.TypeSite:
			if port, ok := number(res.Properties["port"]); ok {
				ports[res.ID] = port
			}
		}
	}
	return ports
}

// TestCheckouts: two checkouts of a repository resolve an environment with
// containers and default ports of their own, and one checkout resolves it
// with the same ones every time (D30, amended). The checkout shifts every
// default port of the environment by one offset, so two of its ports meet
// in one checkout only where they meet in every one; the ports an
// environment sets stay put; and a resolution in no checkout names the
// containers and derives the ports as before checkouts were told apart.
func TestCheckouts(t *testing.T) {
	reg := assemble(t)
	one := resolveIn(t, reg, "/one/schemas", shop(), stacktest.AcmeShop(), "Dev")
	two := resolveIn(t, reg, "/two/schemas", shop(), stacktest.AcmeShop(), "Dev")
	none := resolveIn(t, reg, "", shop(), stacktest.AcmeShop(), "Dev")

	again := resolveIn(t, reg, "/one/schemas", shop(), stacktest.AcmeShop(), "Dev")
	a, _ := stack.Marshal(one)
	b, _ := stack.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("one checkout resolves Dev twice with different containers or ports")
	}

	for _, tc := range []struct {
		env         *ir.ResolvedEnvironment
		schemasRoot string
		postgres    string
		storage     string
	}{
		{one, "/one/schemas", "superschematic-shop-stack-dev-" + local.Checkout("/one/schemas", "shop-stack", "Dev") + "-postgres", "superschematic-shop-stack-dev-" + local.Checkout("/one/schemas", "shop-stack", "Dev") + "-storage"},
		{two, "/two/schemas", "superschematic-shop-stack-dev-" + local.Checkout("/two/schemas", "shop-stack", "Dev") + "-postgres", "superschematic-shop-stack-dev-" + local.Checkout("/two/schemas", "shop-stack", "Dev") + "-storage"},
		{none, "", "superschematic-shop-stack-dev-postgres", "superschematic-shop-stack-dev-storage"},
	} {
		postgres, storage := tc.env.Resources.Resource(local.PostgresContainer), tc.env.Resources.Resource(local.StorageContainer)
		if got := postgres.Properties["name"]; got != tc.postgres {
			t.Errorf("checkout %q: Postgres container %v, want %s", tc.schemasRoot, got, tc.postgres)
		}
		if got := storage.Properties["name"]; got != tc.storage {
			t.Errorf("checkout %q: storage container %v, want %s", tc.schemasRoot, got, tc.storage)
		}
		for _, c := range []*ir.Resource{postgres, storage} {
			labels := c.Properties["labels"].(map[string]any)
			if labels[local.LabelStack] != "shop-stack" || labels[local.LabelEnvironment] != "Dev" {
				t.Errorf("checkout %q: %s labels %v, want shop-stack's Dev", tc.schemasRoot, c.ID, labels)
			}
			if got, want := labels[local.LabelCheckout], local.Checkout(tc.schemasRoot, "shop-stack", "Dev"); (want == "" && got != nil) || (want != "" && got != want) {
				t.Errorf("checkout %q: %s label %s = %v, want %q", tc.schemasRoot, c.ID, local.LabelCheckout, got, want)
			}
		}
	}
	if c := local.Checkout("/one/schemas", "shop-stack", "Dev"); len(c) != 6 || strings.Trim(c, "0123456789abcdef") != "" {
		t.Errorf("checkout %q is not six hexadecimal digits", c)
	}
	if local.Checkout("/one/schemas", "shop-stack", "Dev") == local.Checkout("/one/schemas", "shop-stack", "Other") {
		t.Error("two environments of one checkout share a discriminator")
	}

	// Every default port differs between the two checkouts, and is the one
	// from before checkouts in none. Within one checkout each lies where
	// it does in another, shifted by the checkout's offset.
	ports := map[string]map[string]int{"one": listeners(one), "two": listeners(two), "none": listeners(none)}
	before := map[string]int{"postgres.container": 32259, "storage.container": 25259, "shop-api.process": 22424, "Orders.process": 21592}
	if !maps.Equal(ports["none"], before) {
		t.Errorf("ports in no checkout = %v, want the ones from before checkouts, %v", ports["none"], before)
	}
	for id := range before {
		if ports["one"][id] == ports["two"][id] {
			t.Errorf("%s listens on %d in both checkouts", id, ports["one"][id])
		}
	}
	const span = 2768
	for name, p := range ports {
		if got, want := (p["shop-api.process"]-p["Orders.process"]+span)%span, (before["shop-api.process"]-before["Orders.process"]+span)%span; got != want {
			t.Errorf("checkout %s: shop-api lies %d ports after Orders, want %d, as in every checkout", name, got, want)
		}
	}

	// What the environment sets wins in every checkout.
	pinned := listeners(resolveIn(t, reg, "/one/schemas", shop(), stacktest.AcmeShop(), "Pinned"))
	if pinned["postgres.container"] != 55432 || pinned["shop-api.process"] != 8080 || pinned["Orders.process"] != 8081 {
		t.Errorf("Pinned's ports in a checkout = %v, want its postgresPort and its servers' port settings", pinned)
	}

	// A name stays a DNS label's 63 bytes, the checkout kept whole.
	long := strings.Repeat("a", 40) + "-stack"
	for _, env := range []string{"Preview", "Preview2"} {
		checkout := local.Checkout("/one/schemas", long, env)
		for _, name := range []string{local.ContainerName(long, env, checkout), local.StorageContainerName(long, env, checkout)} {
			if len(name) > 63 || !strings.HasPrefix(name, "superschematic-aaaa") || !strings.Contains(name, "-"+checkout+"-") {
				t.Errorf("container name %s (%d bytes): want at most 63, the stack's name first and the checkout whole", name, len(name))
			}
		}
	}
	if local.ContainerName(long, "Preview", local.Checkout("/one/schemas", long, "Preview")) == local.ContainerName(long, "Preview2", local.Checkout("/one/schemas", long, "Preview2")) {
		t.Error("two environments whose names differ past the limit share a container")
	}
}

func equalJSON(t *testing.T, got, want any) bool {
	t.Helper()
	g, err := stack.Marshal(&ir.ResolvedEnvironment{Values: map[string]any{"v": got}})
	if err != nil {
		t.Fatal(err)
	}
	w, err := stack.Marshal(&ir.ResolvedEnvironment{Values: map[string]any{"v": want}})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(g, w)
}

// resolveErr resolves env and returns the resolution's failures.
func resolveErr(t *testing.T, s *ir.Stack, services []stack.Service, env string) *stack.Errors {
	t.Helper()
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: services, Environment: env, SchemasRoot: checkoutRoot})
	var errs *stack.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("resolve %s: got %v, want resolution errors", env, err)
	}
	return errs
}

func wantFailure(t *testing.T, errs *stack.Errors, code stack.Code, text string) {
	t.Helper()
	for _, e := range errs.List {
		if e.Code == code && strings.Contains(e.Message, text) {
			return
		}
	}
	t.Errorf("no %s failure containing %q in:\n%v", code, text, errs)
}

// TestRefusals: what a local environment cannot hold fails resolution,
// with the reason.
func TestRefusals(t *testing.T) {
	t.Run("a domain", func(t *testing.T) {
		s := shop()
		s.Environments[0].Domain = "dev.acme.dev"
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Dev"), stack.CodePolicy, "sets domain dev.acme.dev")
	})
	t.Run("parameters", func(t *testing.T) {
		s := shop()
		s.Environments = append(s.Environments, &ir.Environment{Name: "Review", Extends: "Dev", Parameters: []string{"pr"}})
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Review"), stack.CodePolicy, "takes parameters pr")
	})
	t.Run("two servers on one port", func(t *testing.T) {
		s := shop()
		s.Environments[1].Settings[1].Values["port"] = float64(8080)
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Pinned"), stack.CodePolicy, "server Orders and server shop-api listen on one port, 8080")
	})
	t.Run("a server on the Postgres port", func(t *testing.T) {
		s := shop()
		s.Environments[1].Settings[1].Values["port"] = float64(55432)
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Pinned"), stack.CodePolicy, "server Orders and container "+postgresName("shop-stack", "Pinned")+" listen on one port, 55432")
	})
	t.Run("the storage emulator on the Postgres port", func(t *testing.T) {
		s := shop()
		s.Environments[1].Values["storagePort"] = float64(55432)
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Pinned"), stack.CodePolicy, "container "+postgresName("shop-stack", "Pinned")+" and container "+storageName("shop-stack", "Pinned")+" listen on one port, 55432")
	})
	t.Run("a bucket whose name GCS refuses", func(t *testing.T) {
		services := stacktest.AcmeShop()
		for i := range services {
			switch services[i].Name {
			case "shop-media":
				services[i].Name = "m"
			case "shop-api":
				services[i].Buckets = []ir.ServiceRef{{Name: "m", Kind: ir.SchemaKindBucket}}
			}
		}
		wantFailure(t, resolveErr(t, shop(), services, "Dev"), stack.CodeLowering, `the bucket of m would be named "m"`)
	})
	t.Run("a Rust server's bucket", func(t *testing.T) {
		// shop-api lists shop-media; the Rust runtime has no Bucket, so
		// the edge is refused as gcp refuses it, and nothing else is.
		errs := resolveErr(t, shop(), stacktest.InRust(stacktest.AcmeShop(), "shop-api"), "Dev")
		want := "server shop-api is a RUST server, which has no Bucket in its runtime to reach bucket shop-media with: buckets from a Rust server are not built (D57)"
		if len(errs.List) != 1 || errs.List[0].Code != stack.CodeLowering || !strings.Contains(errs.List[0].Message, want) {
			t.Errorf("got %v, want the one lowering error %q", errs, want)
		}
	})
	t.Run("a Rust job's or worker's bucket", func(t *testing.T) {
		// shop-orders lists shop-media in Rust: its job and its worker
		// take its edges (D52, D53), so each bucket edge is refused as
		// its server's is, and nothing else is (D65).
		services := stacktest.InRust(stacktest.AcmeShop(), "shop-orders")
		for i := range services {
			if services[i].Name == stacktest.ShopOrders.Name {
				services[i].Buckets = []ir.ServiceRef{stacktest.ShopMedia}
			}
		}
		errs := resolveErr(t, shop(), services, "Dev")
		var got []string
		for _, e := range errs.List {
			got = append(got, string(e.Code)+": "+e.Message)
		}
		for _, d := range []string{"server Orders", "job " + stacktest.ShipOrdersJob, "worker " + stacktest.FulfilOrdersWorker} {
			kind, _, _ := strings.Cut(d, " ")
			wantFailure(t, errs, stack.CodeLowering, d+" is a RUST "+kind+", which has no Bucket in its runtime to reach bucket shop-media with: buckets from a Rust server are not built (D57)")
		}
		if len(errs.List) != 3 {
			t.Errorf("got %d failures, want the three bucket edges':\n%s", len(errs.List), strings.Join(got, "\n"))
		}
	})
	t.Run("a PORT field", func(t *testing.T) {
		services := stacktest.AcmeShop()
		for i := range services {
			if services[i].Name == "shop-api" {
				def := "9000"
				services[i].Config.Fields = append(services[i].Config.Fields, stack.ConfigField{Name: "PORT", Default: &def})
			}
		}
		wantFailure(t, resolveErr(t, shop(), services, "Dev"), stack.CodeLowering, "config field PORT is the variable the local platform sets")
	})
	t.Run("an unknown value", func(t *testing.T) {
		s := shop()
		s.Environments[0].Values = map[string]any{"project": "acme"}
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Dev"), stack.CodeInvalidValues, "project")
	})
}

// TestDatabaseName: a hosted schema's database is its name in lower snake
// case, and never starts with a digit.
func TestDatabaseName(t *testing.T) {
	for in, want := range map[string]string{"shop-db": "shop_db", "Ledger.DB": "ledger_db", "9db": "_9db"} {
		if got := local.DatabaseName(in); got != want {
			t.Errorf("DatabaseName(%q) = %q, want %q", in, got, want)
		}
	}
}
