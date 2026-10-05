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
// environment's exposed servers would share.

// acmeChallenge is the label a DNS authorization's record goes under.
const acmeChallenge = "_acme-challenge."

func exposureNodes(env registry.StackEnvironment, v values, d ir.ResolvedDeployable) ([]*ir.Resource, []*ir.DNSRecord) {
	host := join(d.ResourceName, ".", env.Domain)
	id := func(part string) string { return d.Name + "." + part }
	out := func(part, name string) ir.Output { return ir.Output{Resource: id(part), Name: name} }
	node := func(part, typ, suffix string, props map[string]any) *ir.Resource {
		props["project"] = v.project
		props["name"] = join(d.ResourceName, "-", suffix)
		return &ir.Resource{ID: id(part), Type: typ, Phase: ir.PhaseExposure, Properties: props}
	}
	proxy := node("https-proxy", TypeHTTPSProxy, "https", map[string]any{
		"urlMap":         out("url-map", "id"),
		"certificateMap": join("//certificatemanager.googleapis.com/", out("certificate-map", "id")),
	})
	proxy.DependsOn = []string{id("certificate-map-entry")}
	nodes := []*ir.Resource{
		node("address", TypeGlobalAddress, "ip", map[string]any{
			"ipVersion": "IPV4",
		}),
		node("endpoint-group", TypeEndpointGroup, "neg", map[string]any{
			"region":              v.region,
			"networkEndpointType": "SERVERLESS",
			"cloudRun":            map[string]any{"service": out("service", "name")},
		}),
		node("backend", TypeBackendService, "backend", map[string]any{
			"loadBalancingScheme": "EXTERNAL_MANAGED",
			"backends":            []any{map[string]any{"group": out("endpoint-group", "id")}},
		}),
		node("url-map", TypeURLMap, "lb", map[string]any{
			"defaultService": out("backend", "id"),
		}),
		node("dns-authorization", TypeDNSAuthorization, "dns-auth", map[string]any{
			"domain": host,
		}),
		node("certificate", TypeCertificate, "cert", map[string]any{
			"managed": map[string]any{
				"domains":           []any{host},
				"dnsAuthorizations": []any{out("dns-authorization", "id")},
			},
		}),
		node("certificate-map", TypeCertificateMap, "certs", map[string]any{}),
		node("certificate-map-entry", TypeCertificateMapEntry, "certs-host", map[string]any{
			"map":          out("certificate-map", "name"),
			"hostname":     host,
			"certificates": []any{out("certificate", "id")},
		}),
		proxy,
		node("forwarding-rule", TypeForwardingRule, "https", map[string]any{
			"target":              out("https-proxy", "id"),
			"ipAddress":           out("address", "address"),
			"ipProtocol":          "TCP",
			"portRange":           "443",
			"loadBalancingScheme": "EXTERNAL_MANAGED",
		}),
	}
	records := []*ir.DNSRecord{
		{Name: host, Type: "A", Value: out("address", "address")},
		{Name: join(acmeChallenge, host), Type: "CNAME", Value: out("dns-authorization", "dnsResourceRecords[0].data")},
	}
	return nodes, records
}
