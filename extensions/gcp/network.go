package gcp

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The environment's network, for Direct VPC egress (sections 7.2 and
// 7.3). Cloud Run counts a request as internal when it comes from a VPC,
// so a server that calls another sends all its traffic through one: a
// subnet with Private Google Access reaches the callee's run.app URL from
// inside, and Cloud NAT keeps the internet reachable. Every calling server
// lowers the same four nodes, which resolution shares between them, and a
// member of a parameterized environment inherits its parent's.

// The network's node IDs.
const (
	networkID = "network"
	subnetID  = "network.subnet"
	routerID  = "network.router"
	natID     = "network.nat"
)

func networkNodes(env registry.StackEnvironment, v values) []*ir.Resource {
	base := kebab(env.Stack)
	inherited := len(env.Parameters) > 0
	node := func(id, typ string, props map[string]any) *ir.Resource {
		props["project"] = v.project
		return &ir.Resource{ID: id, Type: typ, Phase: ir.PhaseInfrastructure, Inherited: inherited, Properties: props}
	}
	return []*ir.Resource{
		node(networkID, TypeNetwork, map[string]any{
			"name":                  base,
			"autoCreateSubnetworks": false,
		}),
		node(subnetID, TypeSubnetwork, map[string]any{
			"name":                  base + "-egress",
			"region":                v.region,
			"network":               ir.Output{Resource: networkID, Name: "id"},
			"ipCidrRange":           egressRange,
			"privateIpGoogleAccess": true,
		}),
		node(routerID, TypeRouter, map[string]any{
			"name":    base + "-egress",
			"region":  v.region,
			"network": ir.Output{Resource: networkID, Name: "id"},
		}),
		node(natID, TypeRouterNAT, map[string]any{
			"name":                          base + "-egress",
			"region":                        v.region,
			"router":                        ir.Output{Resource: routerID, Name: "name"},
			"natIpAllocateOption":           "AUTO_ONLY",
			"sourceSubnetworkIpRangesToNat": "ALL_SUBNETWORKS_ALL_IP_RANGES",
		}),
	}
}

// The nodes a memory store adds to the network (D75): a subnet the
// Private Service Connect endpoints of the environment's Memorystore
// instances take their addresses from, and the service connection policy
// that lets Memorystore create them on the network in the region. Every
// store of the environment lowers the same nodes, which resolution shares,
// and a member of a parameterized environment inherits its parent's.
const (
	memoryStoreSubnetID = "network.memorystore-subnet"
	memoryStorePolicyID = "network.memorystore"
)

// memoryStoreNetworkNodes are the network's nodes, then the subnet and the
// policy a memory store's endpoints need. The subnet is a /24 of its own
// beside Direct VPC egress's, so neither takes the other's addresses.
func memoryStoreNetworkNodes(env registry.StackEnvironment, v values) []*ir.Resource {
	base := kebab(env.Stack)
	inherited := len(env.Parameters) > 0
	return append(networkNodes(env, v),
		&ir.Resource{ID: memoryStoreSubnetID, Type: TypeSubnetwork, Phase: ir.PhaseInfrastructure, Inherited: inherited, Properties: map[string]any{
			"project":     v.project,
			"name":        base + "-memorystore",
			"region":      v.region,
			"network":     ir.Output{Resource: networkID, Name: "id"},
			"ipCidrRange": memoryStoreRange,
		}},
		&ir.Resource{ID: memoryStorePolicyID, Type: TypeServiceConnectionPolicy, Phase: ir.PhaseInfrastructure, Inherited: inherited, Properties: map[string]any{
			"project":      v.project,
			"name":         base + "-memorystore",
			"location":     v.region,
			"serviceClass": memoryStoreServiceClass,
			"network":      ir.Output{Resource: networkID, Name: "id"},
			"description":  "Private Service Connect endpoints of the Memorystore instances of stack " + env.Stack,
			"pscConfig": map[string]any{
				"subnetworks": []any{ir.Output{Resource: memoryStoreSubnetID, Name: "id"}},
			},
		}},
	)
}
