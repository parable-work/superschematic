package gcp_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

var update = flag.Bool("update", false, "rewrite the golden environment.json files")

// goldenRoot is the output root the golden files live under, laid out as
// a build writes them: stack/<stack>/<environment>/environment.json.
const goldenRoot = "testdata/golden"

// pulumiStub registers a provisioner under the name the gcp target names,
// with the tool the real one declares, the pulumi CLI at its SDK's
// release. The real one is the pulumi extension's; a distribution links
// both.
type pulumiStub struct{}

func (pulumiStub) Name() string { return "pulumi-stub" }

func (pulumiStub) Register(r *registry.Registry) error {
	return r.RegisterProvisioner(registry.ProvisionerSpec{
		Name: gcp.Provisioner, Extension: "pulumi-stub", Provisioner: &stacktest.FakeProvisioner{},
		Tools: []registry.CLITool{{Name: "pulumi", Version: "3.259.0"}},
	})
}

func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), gcp.Extension{}, pulumiStub{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// shop is the stack of docs/stack-model.md, section 4.1, on the gcp
// target, over the acme-shop services of stacktest.AcmeShop: shop-api and
// shop-orders are deployed and shop-api is exposed; the declared server
// Orders serves shop-orders and calls shop-api. Staging writes its records
// into the acme-dev zone; Production takes the target's default DNS
// platform and zone; Preview extends Staging with a parameter. shop-orders'
// job ShipOrders (D52) runs hourly in New York's time in Staging, on its
// decorator's schedule with two CPUs and 1 GiB in Production, and only on
// demand in Preview, whose members run no schedule they do not turn on.
// shop-api's bucket shop-media (D54) deletes objects after 30 days in
// Staging and its Preview members, and keeps versions in Production.
func shop() *ir.Stack {
	return &ir.Stack{
		Name:   "Shop",
		Deploy: []ir.ServiceRef{stacktest.ShopAPI, stacktest.ShopOrders},
		Expose: []ir.DeployableRef{stacktest.Of(stacktest.ShopAPI)},
		Deployables: []*ir.DeployableDecl{{
			Name:   "Orders",
			Kind:   ir.DeployableServer,
			Serves: []ir.ServiceRef{stacktest.ShopOrders},
		}},
		Environments: []*ir.Environment{
			{
				Name:   "Staging",
				Target: gcp.Target,
				Values: map[string]any{"project": "acme-staging", "region": "us-east1"},
				Domain: "staging.acme.dev",
				DNS:    &ir.DNSPlacement{Platform: gcp.CloudDNS, Values: map[string]any{"zone": "acme-dev"}},
				Settings: []*ir.DeployableSettings{
					{Of: ir.DeployableRef{Deployable: "Orders"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}},
					{Of: stacktest.JobOf(stacktest.ShopOrders, "ShipOrders"), Schedule: "0 * * * *", TimeZone: "America/New_York"},
					{Of: stacktest.Of(stacktest.ShopMedia), Values: map[string]any{"deleteAfterDays": float64(30)}},
				},
			},
			{
				Name:   "Production",
				Target: gcp.Target,
				Values: map[string]any{"project": "acme-prod", "region": "us-east1", "production": true},
				Domain: "acme.dev",
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopDB), Values: map[string]any{"tier": "db-custom-2-7680", "highAvailability": true}},
					{Of: stacktest.Of(stacktest.ShopAPI), Values: map[string]any{"minInstances": float64(1)}, Env: map[string]ir.EnvValue{"LOG_LEVEL": {Value: "warn"}}},
					{Of: ir.DeployableRef{Deployable: "Orders"}, Values: map[string]any{"memory": "1Gi"}, Env: map[string]ir.EnvValue{"FULFILLMENT_REGION": {Value: "us"}}},
					{Of: stacktest.JobOf(stacktest.ShopOrders, "ShipOrders"), Values: map[string]any{"cpu": "2", "memory": "1Gi"}},
					{Of: stacktest.Of(stacktest.ShopMedia), Values: map[string]any{"versioning": true}},
				},
			},
			{
				Name:       "Preview",
				Extends:    "Staging",
				Parameters: []string{"pr"},
				Settings: []*ir.DeployableSettings{
					{Of: stacktest.Of(stacktest.ShopAPI), Env: map[string]ir.EnvValue{"PREVIEW_ID": {Parameter: "pr"}}},
				},
			},
		},
	}
}

// resolve resolves an environment of s over services.
func resolve(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) *ir.ResolvedEnvironment {
	t.Helper()
	resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestGolden resolves the shop stack on gcp, with the site shop-web, which
// calls shop-api (D55), in each environment and checks the
// environment.json it writes, resource graph included, against the golden
// file. Resolution validated every node against the pinned schema of its
// type on the way.
func TestGolden(t *testing.T) {
	reg := assemble(t)
	s := stacktest.WithSite(shop())
	for _, env := range s.Environments {
		t.Run(env.Name, func(t *testing.T) {
			checkGolden(t, reg, s, stacktest.SiteShop(), env.Name)
		})
	}
}

// TestServiceAuthGolden resolves the shop with a service clause on
// shop-api, which Orders calls: RequireShop's @requireService and
// AllowShop's @allowService, whose shop-orders also has a clause no server
// calls. shop-api's server gets SHOP_API_CALLERS, Google's issuer with
// Orders's service account as the caller and shop-api's custom audience,
// and AllowShop's Orders gets SHOP_ORDERS_CALLERS with no issuers. Preview
// names the account and the audience under its parameter.
func TestServiceAuthGolden(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		stack    string
		services []stack.Service
		envs     []string
	}{
		{"RequireShop", stacktest.RequireServiceShop(), []string{"Staging", "Preview"}},
		{"AllowShop", stacktest.AllowServiceShop(), []string{"Staging"}},
	} {
		s := shop()
		s.Name = tc.stack
		for _, env := range tc.envs {
			t.Run(tc.stack+"/"+env, func(t *testing.T) {
				checkGolden(t, reg, s, tc.services, env)
			})
		}
	}
}

// checkGolden resolves env of s and checks the environment.json it writes
// against the golden file, which -update rewrites, and that it
// round-trips.
func checkGolden(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) {
	t.Helper()
	resolved := resolve(t, reg, s, services, env)
	path, err := stack.Write(t.TempDir(), resolved)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	golden := stack.EnvironmentPath(goldenRoot, s.Name, env)
	if *update {
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
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from %s; run with -update and review the diff", path, golden)
	}
	back, err := stack.Unmarshal(got)
	if err != nil {
		t.Fatal(err)
	}
	again, err := stack.Marshal(back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, got) {
		t.Error("environment.json does not round-trip")
	}
	if back.Provisioner != gcp.Provisioner {
		t.Errorf("provisioner = %q, want %q", back.Provisioner, gcp.Provisioner)
	}
}

// TestDeployOrder reads the order the graph gives Staging: the network,
// accounts, secrets, grants and database first, shop-api before its
// callers Orders and shop-orders' job, whose schedule comes with it, and
// the load balancer and records last.
func TestDeployOrder(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.AcmeShop(), "Staging")
	var order []string
	for _, step := range env.DeployOrder {
		s := string(step.Step)
		if step.Migration != "" {
			s += " " + string(step.Migration)
		}
		if step.Wave > 0 {
			s += " " + strconv.Itoa(step.Wave)
		}
		order = append(order, s+" ("+strings.Join(append(slices.Clone(step.Deployables), step.Resources...), ", ")+")")
	}
	want := []string{
		"infrastructure (Orders.account, Orders.cloudsql-client.shop-db, Orders.cloudsql-login.shop-db, Orders.database-user.shop-db, " +
			"Orders.reads.PaymentsSecrets.STRIPE_KEY, Orders.trace-agent, network, network.nat, network.router, network.subnet, " +
			"secret.PaymentsSecrets.STRIPE_KEY, shop-api.account, shop-api.cloudsql-client.shop-db, shop-api.cloudsql-login.shop-db, " +
			"shop-api.database-user.shop-db, shop-api.reads.PaymentsSecrets.STRIPE_KEY, shop-api.sign-as-self, shop-api.storage.shop-media, shop-api.trace-agent, " +
			"shop-db.database.shop-db, shop-db.instance, shop-db.migrator, shop-media.bucket, " +
			"shop-orders-ship-orders.account, shop-orders-ship-orders.cloudsql-client.shop-db, shop-orders-ship-orders.cloudsql-login.shop-db, " +
			"shop-orders-ship-orders.database-user.shop-db, shop-orders-ship-orders.reads.PaymentsSecrets.STRIPE_KEY, shop-orders-ship-orders.trace-agent)",
		"migrate expand (shop-db)",
		"rollout 1 (shop-api, Orders.run-invoker.shop-api, shop-api.service, shop-orders-ship-orders.run-invoker.shop-api)",
		"rollout 2 (Orders, shop-orders-ship-orders, Orders.service, shop-orders-ship-orders.job, shop-orders-ship-orders.schedule, shop-orders-ship-orders.schedule-invoker)",
		"migrate contract (shop-db)",
		"exposure (dns.shop-api.a, dns.shop-api.cname, shop-api.address, shop-api.backend, shop-api.certificate, shop-api.certificate-map, " +
			"shop-api.certificate-map-entry, shop-api.dns-authorization, shop-api.endpoint-group, shop-api.forwarding-rule, shop-api.https-proxy, shop-api.url-map)",
	}
	if got := strings.Join(order, "\n"); got != strings.Join(want, "\n") {
		t.Errorf("deploy order:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// TestPreviewInherits checks that a member of the parameterized Preview
// creates its own servers, accounts and databases, named with the
// parameter, and inherits the instance, its migrator's database user, the
// secret and the network from Staging, which no step of its deploy
// applies.
func TestPreviewInherits(t *testing.T) {
	env := resolve(t, assemble(t), shop(), stacktest.AcmeShop(), "Preview")
	var inherited []string
	for _, res := range env.Resources.Resources {
		if res.Inherited {
			inherited = append(inherited, res.ID)
		}
	}
	want := "network, network.nat, network.router, network.subnet, secret.PaymentsSecrets.STRIPE_KEY, shop-db.instance, shop-db.migrator"
	if got := strings.Join(inherited, ", "); got != want {
		t.Errorf("inherited = %s, want %s", got, want)
	}
	for _, step := range env.DeployOrder {
		for _, id := range step.Resources {
			if slices.Contains(inherited, id) {
				t.Errorf("step %s applies inherited resource %s", step.Step, id)
			}
		}
	}
	name := mustJSON(t, env.Resources.Resource("shop-db.database.shop-db").Properties["name"])
	if want := `{"$concat":["shop_db_pr",{"$parameter":"pr"}]}`; name != want {
		t.Errorf("preview database name = %s, want %s", name, want)
	}
	if got := mustJSON(t, env.Deployable("shop-api").ResourceName); got != `{"$concat":["shop-api-pr",{"$parameter":"pr"}]}` {
		t.Errorf("preview server name = %s", got)
	}
}

// TestServiceCPU: a server's Cloud Run service sets cpuIdle beside its
// resource limits, so its CPU is allocated only while it handles a
// request, unless its settings keep the CPU allocated
// (cpuAlwaysAllocated); a job's task, which always has its CPU, takes no
// cpuIdle.
func TestServiceCPU(t *testing.T) {
	reg := assemble(t)
	service := func(env *ir.ResolvedEnvironment, server string) any {
		template := node(t, env, server+".service").Properties["template"].(map[string]any)
		return template["containers"].([]any)[0].(map[string]any)["resources"]
	}
	const (
		idle   = `{"cpuIdle":true,"limits":{"cpu":"1","memory":"512Mi"}}`
		always = `{"limits":{"cpu":"1","memory":"512Mi"}}`
	)

	env := resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	wantJSON(t, "shop-api's resources", service(env, "shop-api"), idle)
	wantJSON(t, "Orders' resources", service(env, "Orders"), idle)
	task := node(t, env, stacktest.ShipOrdersJob+".job").Properties["template"].(map[string]any)["template"].(map[string]any)
	wantJSON(t, "the job's resources", task["containers"].([]any)[0].(map[string]any)["resources"], always)

	for _, keep := range []bool{true, false} {
		s := shop()
		staging := s.Environment("Staging")
		staging.Settings = append(staging.Settings, &ir.DeployableSettings{
			Of: stacktest.Of(stacktest.ShopAPI), Values: map[string]any{"cpuAlwaysAllocated": keep},
		})
		env := resolve(t, reg, s, stacktest.AcmeShop(), "Staging")
		want := idle
		if keep {
			want = always
		}
		wantJSON(t, fmt.Sprintf("shop-api's resources with cpuAlwaysAllocated %t", keep), service(env, "shop-api"), want)
		wantJSON(t, "Orders' resources", service(env, "Orders"), idle)
	}

	s := shop()
	staging := s.Environment("Staging")
	staging.Settings = append(staging.Settings, &ir.DeployableSettings{
		Of: stacktest.Of(stacktest.ShopAPI), Values: map[string]any{"cpuAlwaysAllocated": "yes"},
	})
	_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"})
	if err == nil || !strings.Contains(err.Error(), "cpuAlwaysAllocated") {
		t.Errorf("cpuAlwaysAllocated \"yes\": err = %v, want a refusal naming cpuAlwaysAllocated", err)
	}
}

// TestProjectNumber: projectNumber, which bootstrap records (D47), is an
// optional value of the project's digits, and an environment that extends
// one inherits it.
func TestProjectNumber(t *testing.T) {
	reg := assemble(t)
	s := shop()
	s.Environments[0].Values["projectNumber"] = "123456789012"
	if got := resolve(t, reg, s, stacktest.AcmeShop(), "Preview").Values["projectNumber"]; got != "123456789012" {
		t.Errorf("Preview's projectNumber = %v, want Staging's", got)
	}
	for _, bad := range []any{"acme-staging", "0123456789", "1234", float64(123456789012)} {
		s := shop()
		s.Environments[0].Values["projectNumber"] = bad
		_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Staging"})
		if err == nil || !strings.Contains(err.Error(), "projectNumber") {
			t.Errorf("projectNumber %v: err = %v, want a refusal naming projectNumber", bad, err)
		}
	}
}

// TestPublicImportsOnly holds the extension to the D10 promise: it imports
// the public packages and the IR, never an internal package of the core.
func TestPublicImportsOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	more, err := filepath.Glob("schemas/*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range append(files, more...) {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/parable-work/superschematic/internal") {
				t.Errorf("%s imports %s; an extension uses the public packages only", file, path)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
