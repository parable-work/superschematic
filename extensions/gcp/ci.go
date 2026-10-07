package gcp

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// CIIdentityKind is the kind of the gcp target's CI identities: Workload
// Identity Federation, whose fields are `provider`, the full name of the
// workload identity provider, and `account`, the email of the service
// account the job acts as. The core's GitHub Actions renderer signs in with
// it (D47).
const CIIdentityKind = "gcp-workload-identity"

// workloadIdentityPool is the id of the stack's workload identity pool,
// which bootstrap creates for the GitHub repository (section 7.3), and
// workloadIdentityProvider the id of its one provider.
func workloadIdentityPool(stack string) string { return kebab(stack) + "-github" }

const workloadIdentityProvider = "github"

// ciIdentities is the gcp target's CI seam.
type ciIdentities struct{}

var _ registry.CIIdentities = ciIdentities{}

// Identity returns Workload Identity Federation through the pool bootstrap
// created in env's project, as the role's account. It is nil without the
// project's number, which the provider's name holds and bootstrap records
// as projectNumber; an environment that extends another inherits it.
func (ciIdentities) Identity(env *ir.ResolvedEnvironment, role registry.CIRole) *registry.CIIdentity {
	v := valuesOf(registry.StackEnvironment{Values: env.Values})
	if v.projectNumber == "" || v.project == "" {
		return nil
	}
	return &registry.CIIdentity{Kind: CIIdentityKind, Fields: map[string]string{
		"provider": "projects/" + v.projectNumber + "/locations/global/workloadIdentityPools/" + workloadIdentityPool(env.Stack) + "/providers/" + workloadIdentityProvider,
		"account":  accountEmail(v, env.Stack, string(role)),
	}}
}
