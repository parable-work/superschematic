package stacktest

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// WorkloadIdentity is the kind of the fake target's CI identities unless
// FakeCI names another: gcp's Workload Identity Federation, which the
// core's GitHub Actions renderer signs in with (D47).
const WorkloadIdentity = "gcp-workload-identity"

// FakeCI is the fake target's CI seam, shaped like gcp's. An environment
// whose values set projectNumber gets, for each role, the provider of the
// stack's pool in that project and the role's account; one without it gets
// none, as on gcp before bootstrap records the number.
type FakeCI struct {
	// Kind is the identities' kind; empty is WorkloadIdentity.
	Kind string
}

var _ registry.CIIdentities = (*FakeCI)(nil)

// Identity returns the identity of role in env, or nil without a
// projectNumber.
func (c *FakeCI) Identity(env *ir.ResolvedEnvironment, role registry.CIRole) *registry.CIIdentity {
	number, _ := env.Values["projectNumber"].(string)
	if number == "" {
		return nil
	}
	project, _ := env.Values["project"].(string)
	kind := c.Kind
	if kind == "" {
		kind = WorkloadIdentity
	}
	return &registry.CIIdentity{Kind: kind, Fields: map[string]string{
		"provider": "projects/" + number + "/locations/global/workloadIdentityPools/" + kebab(env.Stack) + "-ci/providers/ci",
		"account":  kebab(env.Stack) + "-" + string(role) + "@" + project + ".fake.test",
	}}
}
