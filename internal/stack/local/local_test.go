package local_test

import (
	"bytes"
	"errors"
	"flag"
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
// Postgres image and port and each server's port.
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
			},
		},
	}
	return s
}

func resolve(t *testing.T, reg *registry.Registry, s *ir.Stack, services []stack.Service, env string) *ir.ResolvedEnvironment {
	t.Helper()
	resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: services, Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
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
	env := registry.StackEnvironment{Stack: "shop-stack", Name: "Dev"}
	pgPort := local.PostgresPort(env)
	shopAPI := dev.Deployable("shop-api")
	apiPort := local.ServerPort(env, *shopAPI)
	if apiPort == local.ServerPort(registry.StackEnvironment{Stack: "shop-stack", Name: "Other"}, *shopAPI) {
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

	clause := resolve(t, reg, shop(), stacktest.RequireServiceShop(), "Dev")
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
	if got := container.Properties["name"]; got != "superschematic-shop-stack-dev-postgres" {
		t.Errorf("container name = %v", got)
	}

	again := resolve(t, reg, shop(), stacktest.AcmeShop(), "Dev")
	a, _ := stack.Marshal(dev)
	b, _ := stack.Marshal(again)
	if !bytes.Equal(a, b) {
		t.Error("two resolutions of Dev differ")
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
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: services, Environment: env})
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
		wantFailure(t, resolveErr(t, s, stacktest.AcmeShop(), "Pinned"), stack.CodePolicy, "server Orders and the Postgres container listen on one port, 55432")
	})
	t.Run("a Rust server", func(t *testing.T) {
		services := stacktest.AcmeShop()
		for i := range services {
			if services[i].Name == "shop-api" {
				services[i].Language = registry.APILanguageRust
			}
		}
		wantFailure(t, resolveErr(t, shop(), services, "Dev"), stack.CodeUnrealizable, "platform local.process runs only GO, TYPESCRIPT")
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
