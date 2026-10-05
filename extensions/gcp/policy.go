package gcp

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The target's policy rules (section 5.2), each a check over the resolved
// environment and its resource graph. Resolution runs them last, after the
// graph validated, and refuses the environment on any finding.

// checkHighAvailability refuses, in an environment whose values set
// production, a Cloud SQL instance that is not regional, so a zone's
// outage does not take a production database down. A member of a
// parameterized environment inherits its instance and is not checked here.
func checkHighAvailability(env *ir.ResolvedEnvironment) []string {
	if env.Values["production"] != true {
		return nil
	}
	var out []string
	for _, res := range env.Resources.Resources {
		if res.Type != TypeInstance || res.Inherited {
			continue
		}
		settings, _ := res.Properties["settings"].(map[string]any)
		if settings["availabilityType"] != "REGIONAL" {
			out = append(out, fmt.Sprintf("Cloud SQL instance %s of %s is not highly available; a production database sets highAvailability: true", res.ID, strings.Join(res.Owners, ", ")))
		}
	}
	return out
}

// publicMembers are the IAM members that stand for anyone.
var publicMembers = []string{"allUsers", "allAuthenticatedUsers"}

// checkNothingPublic refuses anything that admits the public on behalf of
// a deployable that is not exposed, or of no deployable at all:
//
//   - a Cloud Run service of an internal server that takes traffic from
//     outside, or whose invoker check is off;
//   - a load balancer's address or forwarding rule;
//   - an IAM grant to allUsers or allAuthenticatedUsers;
//   - a Cloud SQL instance that authorizes connections from anywhere.
func checkNothingPublic(env *ir.ResolvedEnvironment) []string {
	exposed := func(res *ir.Resource) bool {
		return slices.ContainsFunc(res.Owners, func(owner string) bool {
			d := env.Deployable(owner)
			return d != nil && d.Exposed
		})
	}
	var out []string
	for _, res := range env.Resources.Resources {
		if exposed(res) {
			continue
		}
		owners := strings.Join(res.Owners, ", ")
		switch res.Type {
		case TypeService:
			if ingress := res.Properties["ingress"]; ingress != ingressInternal {
				out = append(out, fmt.Sprintf("Cloud Run service %s of %s takes %v, but %s is not exposed", res.ID, owners, ingress, owners))
			}
			if res.Properties["invokerIamDisabled"] == true {
				out = append(out, fmt.Sprintf("Cloud Run service %s of %s admits callers without the invoker check, but %s is not exposed", res.ID, owners, owners))
			}
		case TypeGlobalAddress, TypeForwardingRule:
			out = append(out, fmt.Sprintf("load balancer %s of %s is public, but %s is not an exposed server", res.ID, owners, owners))
		case TypeInstance:
			settings, _ := res.Properties["settings"].(map[string]any)
			ip, _ := settings["ipConfiguration"].(map[string]any)
			networks, _ := ip["authorizedNetworks"].([]any)
			for _, n := range networks {
				if network, _ := n.(map[string]any); network["value"] == "0.0.0.0/0" {
					out = append(out, fmt.Sprintf("Cloud SQL instance %s of %s authorizes connections from 0.0.0.0/0", res.ID, owners))
				}
			}
		}
		if member, ok := res.Properties["member"].(string); ok && slices.Contains(publicMembers, member) {
			out = append(out, fmt.Sprintf("%s grants %v to %s on behalf of %s, which is not an exposed server", res.ID, res.Properties["role"], member, owners))
		}
		members, _ := res.Properties["members"].([]any)
		for _, m := range members {
			if member, ok := m.(string); ok && slices.Contains(publicMembers, member) {
				out = append(out, fmt.Sprintf("%s grants %v to %s on behalf of %s, which is not an exposed server", res.ID, res.Properties["role"], member, owners))
			}
		}
	}
	return out
}
