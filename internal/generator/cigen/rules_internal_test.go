package cigen

import (
	"errors"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRulesOf (D103): the Go and TypeScript deployables with a sql edge to
// a database a table of which has a field that is a behavior are listed
// under the checkout [paths] rules_go and rules_typescript name, each
// language only when its key names that checkout; a stack none of whose
// deployables reaches such a database, and one with no rules key, lists
// none.
func TestRulesOf(t *testing.T) {
	workflow := &ir.Schema{Name: "workflow-db", Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{
		"Order": {Name: "Order", Role: ir.RoleDBTable, Behaviors: []ir.BehaviorRef{{Name: "Workflow", Field: "status"}}},
	}}
	plain := &ir.Schema{Name: "plain-db", Kind: ir.SchemaKindDB}
	load := func(name string) (*ir.Schema, error) {
		switch name {
		case "workflow-db":
			return workflow, nil
		case "plain-db":
			return plain, nil
		}
		return nil, errors.New("no such service")
	}
	envs := []*ir.ResolvedEnvironment{{
		Deployables: []*ir.ResolvedDeployable{
			{Name: "orders", Kind: ir.DeployableServer, Language: registry.APILanguageGo},
			{Name: "orders-expire", Kind: ir.DeployableJob, Language: registry.APILanguageGo},
			{Name: "storefront", Kind: ir.DeployableServer, Language: registry.APILanguageTypeScript},
			{Name: "catalog", Kind: ir.DeployableServer, Language: registry.APILanguageGo},
		},
		Edges: []*ir.Edge{
			{Kind: ir.EdgeSQL, From: "orders", Service: ir.ServiceRef{Name: "workflow-db"}},
			{Kind: ir.EdgeSQL, From: "orders-expire", Service: ir.ServiceRef{Name: "workflow-db"}},
			{Kind: ir.EdgeSQL, From: "storefront", Service: ir.ServiceRef{Name: "workflow-db"}},
			{Kind: ir.EdgeSQL, From: "catalog", Service: ir.ServiceRef{Name: "plain-db"}},
		},
	}}
	rel := func(_, p string) (string, error) { return p[len("/repo/"):], nil }
	for _, tc := range []struct {
		name       string
		paths      naming.LocalPaths
		envs       []*ir.ResolvedEnvironment
		checkout   string
		goNames    []string
		typeScript []string
	}{
		{"both keys", naming.LocalPaths{RulesGo: "/repo/vendor/ss/runtime/rules/go", RulesTypeScript: "/repo/vendor/ss/runtime/rules/typescript"}, envs, "vendor/ss", []string{"orders", "orders-expire"}, []string{"storefront"}},
		{"rules_go alone", naming.LocalPaths{RulesGo: "/repo/vendor/ss/runtime/rules/go"}, envs, "vendor/ss", []string{"orders", "orders-expire"}, nil},
		{"rules_typescript alone", naming.LocalPaths{RulesTypeScript: "/repo/vendor/ss/runtime/rules/typescript"}, envs, "vendor/ss", nil, []string{"storefront"}},
		{"no rules key", naming.LocalPaths{}, envs, "", nil, nil},
		{"no such database", naming.LocalPaths{RulesGo: "/repo/vendor/ss/runtime/rules/go"}, []*ir.ResolvedEnvironment{{Deployables: envs[0].Deployables, Edges: envs[0].Edges[3:]}}, "", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := registry.GenerateContext{LoadDependency: load}
			c.Options.Paths = tc.paths
			var st registry.CIStack
			rulesOf(c, "workflow-stack", &st, tc.envs, rel)
			if st.RulesCheckout != tc.checkout || !slices.Equal(st.RulesGo, tc.goNames) || !slices.Equal(st.RulesTypeScript, tc.typeScript) {
				t.Errorf("rulesOf = %q, Go %q, TypeScript %q; want %q, %q, %q", st.RulesCheckout, st.RulesGo, st.RulesTypeScript, tc.checkout, tc.goNames, tc.typeScript)
			}
		})
	}
}
