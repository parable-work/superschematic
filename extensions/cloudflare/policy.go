package cloudflare

import (
	"fmt"
	"slices"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The target's policy rules (sections 5.2 and 18.14), each a check over the
// resolved environment and its resource graph. Resolution runs them last,
// after the graph validated, and refuses the environment on any finding.
// buckets-never-public comes with the R2 platform.

// checkDomainOnCloudflare refuses an environment with a domain whose DNS
// platform is not cloudflare: a Worker's custom domain is a hostname in a
// zone of the account, whose record and certificate Cloudflare writes, so
// no other DNS platform can hold it (section 18.11). Whether the token
// reaches the zone only Cloudflare knows, so a deploy finds that out.
func checkDomainOnCloudflare(env *ir.ResolvedEnvironment) []string {
	if env.Domain == "" {
		return nil
	}
	platform := ir.ManualDNS
	if env.DNS != nil {
		platform = env.DNS.Platform
	}
	if platform == DNS {
		return nil
	}
	return []string{fmt.Sprintf("environment %s has domain %s on DNS platform %s; a cloudflare environment's domain is in a zone of its account, on DNS platform %s (dns: { %s: { zone, zoneId } })", env.Environment, env.Domain, platform, DNS, DNS)}
}

// checkNothingPublic refuses whatever reaches a Worker from the internet on
// behalf of a deployable that is not exposed, or of no deployable at all
// (section 18.14): a Worker with workers.dev on, a custom domain, or a
// route. Only a service binding reaches an internal server. A job is never
// exposed, so none of the three may reach its Worker; a site always is
// (D55).
func checkNothingPublic(env *ir.ResolvedEnvironment) []string {
	exposed := func(res *ir.Resource) bool {
		return slices.ContainsFunc(res.Owners, func(owner string) bool {
			d := env.Deployable(owner)
			return d != nil && d.Exposed && d.Kind != ir.DeployableJob
		})
	}
	var out []string
	for _, res := range env.Resources.Resources {
		if exposed(res) {
			continue
		}
		owners := strings.Join(res.Owners, ", ")
		if owners == "" {
			owners = "no deployable"
		}
		switch res.Type {
		case TypeWorker:
			if subdomain, _ := res.Properties["subdomain"].(map[string]any); subdomain["enabled"] == true {
				out = append(out, fmt.Sprintf("Worker %s has workers.dev on for %s, which is not exposed", res.ID, owners))
			}
		case TypeWorkersCustomDomain:
			out = append(out, fmt.Sprintf("custom domain %s reaches a Worker for %s, which is not exposed", res.ID, owners))
		case TypeWorkersRoute:
			out = append(out, fmt.Sprintf("route %s reaches a Worker for %s, which is not exposed", res.ID, owners))
		}
	}
	return out
}
