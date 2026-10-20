package cloudflare_test

import (
	"encoding/json"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// accountNames are the names an environment's nodes take in the account:
// each node's `name`, and each custom domain's `hostname`, by node.
func accountNames(t *testing.T, env *ir.ResolvedEnvironment) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, res := range env.Resources.Resources {
		for _, property := range []string{"name", "hostname"} {
			value, ok := res.Properties[property]
			if !ok {
				continue
			}
			if _, isOutput := value.(ir.Output); isOutput {
				continue
			}
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			out[string(data)] = res.ID
		}
	}
	return out
}

// TestPlainEnvironmentsOfOneAccountNameApart: Staging and Production of
// each fixture are plain environments in one account, and no Worker, D1
// database, R2 bucket, bucket token or custom domain of one takes a name
// of the other's: each name holds its stack and its environment (custom
// domains, under each environment's domain).
func TestPlainEnvironmentsOfOneAccountNameApart(t *testing.T) {
	reg := assemble(t)
	resolveD1 := func(t *testing.T, reg *registry.Registry, env string) *ir.ResolvedEnvironment {
		t.Helper()
		resolved, err := stack.Resolve(reg, stack.Input{Stack: d1Stack(), Services: d1Services(), Environment: env})
		if err != nil {
			t.Fatal(err)
		}
		return resolved
	}
	resolveWorkers := func(t *testing.T, reg *registry.Registry, env string) *ir.ResolvedEnvironment {
		t.Helper()
		resolved, errs := resolveOver(t, reg, workersStack(), workersServices(), env)
		if errs != nil {
			t.Fatal(errs)
		}
		return resolved
	}
	for name, resolve := range map[string]func(*testing.T, *registry.Registry, string) *ir.ResolvedEnvironment{
		"calls-stack":   resolveCalls,
		"reviews-stack": resolveD1,
		"web-stack":     resolveWeb,
		"workers-stack": resolveWorkers,
	} {
		t.Run(name, func(t *testing.T) {
			staging, production := resolve(t, reg, "Staging"), resolve(t, reg, "Production")
			if staging.Values["accountId"] != production.Values["accountId"] {
				t.Fatalf("Staging and Production are in accounts %v and %v, not one", staging.Values["accountId"], production.Values["accountId"])
			}
			stagingNames, productionNames := accountNames(t, staging), accountNames(t, production)
			if len(stagingNames) == 0 || len(productionNames) == 0 {
				t.Fatalf("no names: %v, %v", stagingNames, productionNames)
			}
			for n, id := range stagingNames {
				if other, ok := productionNames[n]; ok {
					t.Errorf("Staging's %s and Production's %s are both named %s", id, other, n)
				}
			}
		})
	}
}
