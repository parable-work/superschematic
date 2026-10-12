package gcp

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Exposure under a domain (section 7.2): a global external Application
// Load Balancer in front of the server's service, with a Google-managed
// certificate for the server's host under the environment's domain. The
// certificate is Certificate Manager's, authorized by a DNS record, so it
// is issued before traffic moves to the load balancer. The host's A
// record and the authorization's CNAME go to the environment's DNS
// platform in the neutral record shape (section 6.9).
//
// Each exposed server gets a load balancer of its own: a platform lowers
// one deployable, so it cannot write the host rules of a load balancer the
// environment's exposed servers would share. A site's load balancer
// (site.go, D55) has the same frontend, with a backend bucket behind it.

// acmeChallenge is the label a DNS authorization's record goes under.
const acmeChallenge = "_acme-challenge."

// exposureNodes returns an exposed server's load balancer: a serverless
// network endpoint group for its service, a backend service over it, a
// URL map that sends every request there, and the frontend before them
// (loadBalancer.frontend).
func exposureNodes(env registry.StackEnvironment, v values, d ir.ResolvedDeployable) ([]*ir.Resource, []*ir.DNSRecord) {
	lb := loadBalancer{v: v, d: d}
	return lb.frontend(env, []*ir.Resource{
		lb.node("endpoint-group", TypeEndpointGroup, "neg", map[string]any{
			"region":              v.region,
			"networkEndpointType": "SERVERLESS",
			"cloudRun":            map[string]any{"service": lb.out("service", "name")},
		}),
		lb.node("backend", TypeBackendService, "backend", map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
			"backends":            []any{map[string]any{"group": lb.out("endpoint-group", "id")}},
		}),
		lb.node("url-map", TypeURLMap, "lb", map[string]any{
			"defaultService": lb.out("backend", "id"),
		}),
	})
}

// loadBalancer makes the nodes of a deployable's load balancer, an exposed
// server's or a site's (D55): each node's ID is the deployable's name and
// a part, its name the deployable's resource name and a suffix, and it
// applies in the exposure phase.
type loadBalancer struct {
	v values
	d ir.ResolvedDeployable
}

func (lb loadBalancer) id(part string) string { return lb.d.Name + "." + part }

func (lb loadBalancer) out(part, name string) ir.Output {
	return ir.Output{Resource: lb.id(part), Name: name}
}

func (lb loadBalancer) node(part, typ, suffix string, props map[string]any) *ir.Resource {
	props["project"] = lb.v.project
	props["name"] = join(lb.d.ResourceName, "-", suffix)
	return &ir.Resource{ID: lb.id(part), Type: typ, Phase: ir.PhaseExposure, Properties: props}
}

// frontend returns the nodes of the load balancer around backends, which
// end with its URL map, `url-map`, and the DNS records they need: a global
// address, then backends, then under the environment's domain a
// Certificate Manager certificate for the deployable's host there,
// authorized by a DNS record, an HTTPS proxy and a forwarding rule on port
// 443, with the host's A record and the authorization's CNAME. With no
// domain, which only a site's load balancer has, there is no host to issue
// a certificate for: an HTTP proxy and a forwarding rule on port 80 serve
// the address (sitePublicAddress).
func (lb loadBalancer) frontend(env registry.StackEnvironment, backends []*ir.Resource) ([]*ir.Resource, []*ir.DNSRecord) {
	nodes := []*ir.Resource{lb.node("address", TypeGlobalAddress, "ip", map[string]any{
		"ipVersion": "IPV4",
	})}
	nodes = append(nodes, backends...)
	rule := func(proxy, suffix, port string) *ir.Resource {
		return lb.node("forwarding-rule", TypeForwardingRule, suffix, map[string]any{
			"target":              lb.out(proxy, "id"),
			"ipAddress":           lb.out("address", "address"),
			"ipProtocol":          "TCP",
			"portRange":           port,
			"loadBalancingScheme": "EXTERNAL_MANAGED",
		})
	}
	if env.Domain == "" {
		nodes = append(nodes,
			lb.node("http-proxy", TypeHTTPProxy, "http", map[string]any{
				"urlMap": lb.out("url-map", "id"),
			}),
			rule("http-proxy", "http", "80"),
		)
		return nodes, nil
	}
	host := join(lb.d.ResourceName, ".", env.Domain)
	proxy := lb.node("https-proxy", TypeHTTPSProxy, "https", map[string]any{
		"urlMap":         lb.out("url-map", "id"),
		"certificateMap": join("//certificatemanager.googleapis.com/", lb.out("certificate-map", "id")),
	})
	proxy.DependsOn = []string{lb.id("certificate-map-entry")}
	nodes = append(nodes,
		lb.node("dns-authorization", TypeDNSAuthorization, "dns-auth", map[string]any{
			"domain": host,
		}),
		lb.node("certificate", TypeCertificate, "cert", map[string]any{
			"managed": map[string]any{
				"domains":           []any{host},
				"dnsAuthorizations": []any{lb.out("dns-authorization", "id")},
			},
		}),
		lb.node("certificate-map", TypeCertificateMap, "certs", map[string]any{}),
		lb.node("certificate-map-entry", TypeCertificateMapEntry, "certs-host", map[string]any{
			"map":          lb.out("certificate-map", "name"),
			"hostname":     host,
			"certificates": []any{lb.out("certificate", "id")},
		}),
		proxy,
		rule("https-proxy", "https", "443"),
	)
	records := []*ir.DNSRecord{
		{Name: host, Type: "A", Value: lb.out("address", "address")},
		{Name: join(acmeChallenge, host), Type: "CNAME", Value: lb.out("dns-authorization", "dnsResourceRecords[0].data")},
	}
	return nodes, records
}
