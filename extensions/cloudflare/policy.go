package cloudflare

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// The target's policy rules (sections 5.2 and 18.14), each a check over the
// resolved environment and its resource graph. Resolution runs them last,
// after the graph validated, and refuses the environment on any finding.
// The rules over Workers, buckets and exposure come with their platforms.

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
