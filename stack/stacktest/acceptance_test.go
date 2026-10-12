package stacktest_test

import (
	"bytes"
	"context"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden environment.json files")

// goldenRoot is the output root the golden files live under, laid out as
// a build writes them: stack/<stack>/<environment>/environment.json.
const goldenRoot = "testdata/golden"

func assemble(t *testing.T) (*registry.Registry, *stacktest.Extension) {
	t.Helper()
	ext := &stacktest.Extension{}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg, ext
}

// TestAcceptance is the stack model's D10 acceptance test (docs/stack-model.md,
// section 6.7): an extension that imports only the public packages adds a
// target with its platforms, connectors, DNS platform and provisioner, and
// the core resolves a stack over the acme-shop services on it and writes
// each environment's environment.json.
func TestAcceptance(t *testing.T) {
	reg, ext := assemble(t)
	shop := stacktest.WithSite(stacktest.Shop())
	out := t.TempDir()
	for _, env := range shop.Environments {
		t.Run(env.Name, func(t *testing.T) {
			resolved, err := stack.Resolve(reg, stack.Input{Stack: shop, Services: stacktest.SiteShop(), Environment: env.Name})
			if err != nil {
				t.Fatal(err)
			}
			path, err := stack.Write(out, resolved)
			if err != nil {
				t.Fatal(err)
			}
			if want := stack.EnvironmentPath(out, "shop-stack", env.Name); path != want {
				t.Fatalf("written to %s, want %s", path, want)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			golden := stack.EnvironmentPath(goldenRoot, "shop-stack", env.Name)
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

			// The file reads back to the same environment, references
			// included.
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

			// The provisioner the target names runs each step.
			spec, ok := reg.Provisioner(resolved.Provisioner)
			if !ok {
				t.Fatalf("provisioner %q is not registered", resolved.Provisioner)
			}
			params := map[string]string{}
			for _, p := range resolved.Parameters {
				params[p] = "123"
			}
			req := registry.ProvisionRequest{Environment: back, Parameters: params, Dir: filepath.Join(out, "program", env.Name)}
			if err := spec.Provisioner.Render(back, req.Dir); err != nil {
				t.Fatal(err)
			}
			if _, err := spec.Provisioner.Plan(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			for _, step := range back.DeployOrder {
				if len(step.Resources) == 0 {
					continue
				}
				if err := spec.Provisioner.Apply(context.Background(), req, *step); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := spec.Provisioner.Outputs(context.Background(), req); err != nil {
				t.Fatal(err)
			}
		})
	}
	calls := strings.Join(ext.Provisioner.Calls(), "\n")
	for _, want := range []string{"render", "plan Staging", "apply infrastructure", "apply rollout 1", "apply rollout 2", "apply exposure", "outputs Preview"} {
		if !strings.Contains(calls, want) {
			t.Errorf("provisioner calls lack %q:\n%s", want, calls)
		}
	}
}

// TestAcceptanceWiring reads the resolved Staging environment for the
// facts section 3 derives: the defaults, the declared server, shop-orders'
// job with its API's edges (D52), shop-api's bucket (D54), the edges with
// their connectors, the shared secret and the callee-first order.
func TestAcceptanceWiring(t *testing.T) {
	reg, _ := assemble(t)
	env, err := stack.Resolve(reg, stack.Input{Stack: stacktest.Shop(), Services: stacktest.AcmeShop(), Environment: "Staging"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, d := range env.Deployables {
		names = append(names, string(d.Kind)+" "+d.Name+" on "+d.Platform)
	}
	if got, want := strings.Join(names, "; "), "server Orders on fake.run; server shop-api on fake.run; database shop-db on fake.sql; bucket shop-media on fake.storage; worker shop-orders-fulfil-orders on fake.worker; job shop-orders-ship-orders on fake.job"; got != want {
		t.Errorf("deployables = %s, want %s", got, want)
	}
	var edges []string
	for _, e := range env.Edges {
		edges = append(edges, e.ID+" by "+e.Connector+" into "+e.Field)
	}
	if got, want := strings.Join(edges, "; "), "bucket:shop-api->shop-media by fake.run-storage into SHOP_MEDIA_BUCKET; "+
		"http:Orders->shop-api by fake.run-run into SHOP_API_SERVICE; "+
		"http:shop-orders-fulfil-orders->shop-api by fake.worker-run into SHOP_API_SERVICE; "+
		"http:shop-orders-ship-orders->shop-api by fake.job-run into SHOP_API_SERVICE; "+
		"sql:Orders->shop-db by fake.run-sql into SHOP_DB_DATABASE; sql:shop-api->shop-db by fake.run-sql into SHOP_DB_DATABASE; "+
		"sql:shop-orders-fulfil-orders->shop-db by fake.worker-sql into SHOP_DB_DATABASE; "+
		"sql:shop-orders-ship-orders->shop-db by fake.job-sql into SHOP_DB_DATABASE"; got != want {
		t.Errorf("edges = %s, want %s", got, want)
	}
	if len(env.Secrets) != 1 || env.Secrets[0].ID != "PaymentsSecrets.STRIPE_KEY" || strings.Join(env.Secrets[0].Readers, ",") != "Orders,shop-api,shop-orders-fulfil-orders,shop-orders-ship-orders" {
		t.Errorf("secrets = %+v, want PaymentsSecrets.STRIPE_KEY read by Orders, shop-api, the worker and the job", env.Secrets)
	}
	var order []string
	for _, step := range env.DeployOrder {
		s := string(step.Step)
		if step.Migration != "" {
			s += " " + string(step.Migration)
		}
		if step.Wave > 0 {
			s += " " + strconv.Itoa(step.Wave)
		}
		if len(step.Deployables) > 0 {
			s += " (" + strings.Join(step.Deployables, ", ") + ")"
		}
		order = append(order, s)
	}
	if got, want := strings.Join(order, "; "), "infrastructure; migrate expand (shop-db); rollout 1 (shop-api); rollout 2 (Orders, shop-orders-fulfil-orders, shop-orders-ship-orders); migrate contract (shop-db); exposure"; got != want {
		t.Errorf("deploy order = %s, want %s", got, want)
	}
	if env.DNS == nil || env.DNS.Platform != stacktest.DNSPlatform || len(env.DNS.Records) != 1 || env.DNS.Records[0].Deployable != "shop-api" {
		t.Errorf("dns = %+v, want one record for shop-api on %s", env.DNS, stacktest.DNSPlatform)
	}
}

// TestPublicImportsOnly holds the extension to the D10 promise: it imports
// the public packages and the IR, never an internal package.
func TestPublicImportsOnly(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.Contains(path, "/internal/") || strings.HasSuffix(path, "/internal") {
				t.Errorf("%s imports %s; the acceptance extension uses the public packages only", file, path)
			}
		}
	}
}
