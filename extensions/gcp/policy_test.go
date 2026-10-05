package gcp_test

import (
	"errors"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// TestPolicyHighAvailability checks that a production database that is
// not highly available fails resolution on the policy rule, and that a
// staging one passes.
func TestPolicyHighAvailability(t *testing.T) {
	reg := assemble(t)
	s := shop()
	prod := s.Environment("Production")
	prod.Settings[0].Values["highAvailability"] = false
	_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Production"})
	var errs *stack.Errors
	if !errors.As(err, &errs) || len(errs.List) != 1 || errs.List[0].Code != stack.CodePolicy ||
		!strings.Contains(err.Error(), gcp.PolicyHighAvailability) || !strings.Contains(err.Error(), "shop-db.instance") {
		t.Fatalf("err = %v, want one %s finding on shop-db.instance", err, gcp.PolicyHighAvailability)
	}

	delete(prod.Settings[0].Values, "highAvailability")
	if _, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.AcmeShop(), Environment: "Production"}); err == nil {
		t.Error("a production database with the default settings passed; the default is not highly available")
	}

	env := resolve(t, reg, s, stacktest.AcmeShop(), "Staging")
	settings := env.Resources.Resource("shop-db.instance").Properties["settings"].(map[string]any)
	if settings["availabilityType"] != "ZONAL" {
		t.Errorf("staging's instance is %v, want ZONAL", settings["availabilityType"])
	}
}

// policy returns the gcp target's rule named name.
func policy(t *testing.T, reg *registry.Registry, name string) registry.PolicyRule {
	t.Helper()
	spec, ok := reg.Target(gcp.Target)
	if !ok {
		t.Fatal("gcp is not registered")
	}
	for _, rule := range spec.Policies {
		if rule.Name == name {
			return rule
		}
	}
	t.Fatalf("gcp has no policy rule %s", name)
	return registry.PolicyRule{}
}

// TestPolicyNothingPublic runs the rule over a resolved Staging changed
// one way at a time: each change opens something to the public on behalf
// of a deployable that is not exposed, and the rule names it. The same
// change on the exposed shop-api is allowed.
func TestPolicyNothingPublic(t *testing.T) {
	reg := assemble(t)
	rule := policy(t, reg, gcp.PolicyNothingPublic)
	resolved := func() *ir.ResolvedEnvironment {
		return resolve(t, reg, shop(), stacktest.AcmeShop(), "Staging")
	}
	if findings := rule.Check(resolved()); len(findings) > 0 {
		t.Fatalf("Staging as resolved has findings: %v", findings)
	}
	publicGrant := func(owner string) *ir.Resource {
		return &ir.Resource{
			ID:         owner + ".public",
			Type:       gcp.TypeServiceIAMMember,
			Properties: map[string]any{"role": "roles/run.invoker", "member": "allUsers"},
			Owners:     []string{owner},
		}
	}
	cases := []struct {
		name   string
		change func(env *ir.ResolvedEnvironment)
		want   string
	}{
		{"ingress", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resource("Orders.service").Properties["ingress"] = "INGRESS_TRAFFIC_ALL"
		}, "Orders.service of Orders takes INGRESS_TRAFFIC_ALL"},
		{"invoker check off", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resource("Orders.service").Properties["invokerIamDisabled"] = true
		}, "without the invoker check"},
		{"public grant", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resources = append(env.Resources.Resources, publicGrant("Orders"))
		}, "grants roles/run.invoker to allUsers"},
		{"public grant from an edge", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resources = append(env.Resources.Resources, publicGrant("http:Orders->shop-api"))
		}, "on behalf of http:Orders->shop-api"},
		{"load balancer", func(env *ir.ResolvedEnvironment) {
			env.Resources.Resources = append(env.Resources.Resources, &ir.Resource{ID: "Orders.address", Type: gcp.TypeGlobalAddress, Owners: []string{"Orders"}})
		}, "load balancer Orders.address of Orders is public"},
		{"open database", func(env *ir.ResolvedEnvironment) {
			settings := env.Resources.Resource("shop-db.instance").Properties["settings"].(map[string]any)
			settings["ipConfiguration"] = map[string]any{"ipv4Enabled": true, "authorizedNetworks": []any{map[string]any{"value": "0.0.0.0/0"}}}
		}, "authorizes connections from 0.0.0.0/0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := resolved()
			c.change(env)
			findings := rule.Check(env)
			if len(findings) != 1 || !strings.Contains(findings[0], c.want) {
				t.Errorf("findings = %q, want one containing %q", findings, c.want)
			}
		})
	}

	env := resolved()
	env.Resources.Resources = append(env.Resources.Resources, publicGrant("shop-api"))
	if findings := rule.Check(env); len(findings) > 0 {
		t.Errorf("a public grant on exposed shop-api has findings: %v", findings)
	}
}
