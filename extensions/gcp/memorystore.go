package gcp

import (
	"fmt"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// The Memorystore platform (D75): a memory store is a Memorystore for
// Valkey instance in the environment's region, with cluster mode disabled
// and one shard, which a workload reaches over TLS as its own account.
//
//   - Transit encryption is SERVER_AUTHENTICATION, so the client verifies
//     the instance against its managed server CA, and authorization is
//     IAM_AUTH, so a client sends its account's access token as its
//     password and no AUTH string exists for the deploy to write.
//   - The instance creates its own Private Service Connect endpoints on the
//     environment's network (desiredAutoCreatedEndpoints), which needs a
//     service connection policy for Memorystore on that network in the
//     region. One policy serves every store of the environment, and takes
//     the endpoints' addresses from a subnet of its own, so Direct VPC
//     egress keeps its range for Cloud Run's instances.
//   - A workload reaches the endpoint through Direct VPC egress on that
//     network (lowerWorkload), and the connector grants its account the
//     connection role on the instance alone.
//
// A store holds no fact (D75): nothing protects the instance from a
// destroy and nothing persists it. A member of a parameterized environment
// shares its parent's instance, policy and subnet, as it shares the
// network, and creates none of its own. Its names carry its own name, and
// its destroy deletes nothing, since every key it wrote expires.

// The Memorystore instance's fixed shape: Valkey 8.0 with cluster mode
// disabled, one shard, TLS verified against the managed server CA, and IAM
// authentication.
const (
	memoryStoreEngine         = "VALKEY_8_0"
	memoryStoreMode           = "CLUSTER_DISABLED"
	memoryStoreShards         = 1
	memoryStoreTransit        = "SERVER_AUTHENTICATION"
	memoryStoreAuthorization  = "IAM_AUTH"
	memoryStoreServiceClass   = "gcp-memorystore"
	defaultMemoryStoreNode    = "SHARED_CORE_NANO"
	defaultMemoryStoreReplica = 1

	// memoryStoreConnectionRole is the role a workload's account takes to
	// connect to an instance with IAM authentication.
	memoryStoreConnectionRole = "roles/memorystore.dbConnectionUser"
)

// memoryStoreSettings is the schema of a memory store's settings on
// Memorystore (D75): the node type, SHARED_CORE_NANO unless set, and the
// number of replicas of its one shard, 1 unless set. The node types are
// those the pinned provider lists, and Memorystore for Valkey takes 0 to 5
// replicas per shard.
const memoryStoreSettings = `{
  "type": "object",
  "properties": {
    "nodeType": {"enum": ["SHARED_CORE_NANO", "STANDARD_SMALL", "HIGHCPU_MEDIUM", "HIGHMEM_MEDIUM", "HIGHMEM_XLARGE", "STANDARD_LARGE", "HIGHMEM_2XLARGE", "CUSTOM_PICO", "CUSTOM_MICRO", "CUSTOM_MINI"]},
    "replicas": {"type": "integer", "minimum": 0, "maximum": 5}
  },
  "additionalProperties": false
}`

// memoryStoreName names a store's instance `<project>-<stack>-<store>`
// (`acme-staging-shop-shop-cache`), as a bucket is named. A member of a
// parameterized environment shares its parent's instance, so the name
// holds no parameter.
func memoryStoreName(ctx registry.PlatformContext) any {
	v := valuesOf(ctx.Environment)
	return v.project + "-" + kebab(ctx.Environment.Stack) + "-" + kebab(ctx.Deployable.Name)
}

// The instance's outputs a connection reads: the first Private Service
// Connect connection the instance created, taken as the primary's (with
// replicas it also creates a reader's; the first live run checks the
// order, by its connectionType), and the first certificate of its managed
// server CA's first chain, the CA that issues the server's own
// certificate. The field holds one PEM: the resource graph has no value
// that joins a list, so a client trusts that CA as an anchor of a partial
// chain, as Go's and rustls's do by default.
const (
	memoryStoreIPOutput   = "endpoints[0].connections[0].pscAutoConnection.ipAddress"
	memoryStorePortOutput = "endpoints[0].connections[0].pscAutoConnection.port"
	memoryStoreCAOutput   = "managedServerCas[0].caCerts[0].certificates[0]"
)

// memoryStoreAddress is the store's URL, `rediss://<ip>:<port>` from the
// endpoint the instance created on the environment's network, so each
// workload the store's edges connect waits for the instance.
func memoryStoreAddress(ctx registry.PlatformContext) any {
	instance := ctx.Deployable.Name + ".instance"
	return join(
		"rediss://",
		ir.Output{Resource: instance, Name: memoryStoreIPOutput},
		":",
		ir.Output{Resource: instance, Name: memoryStorePortOutput},
	)
}

// lowerMemoryStore lowers a memory store to a Memorystore for Valkey
// instance on the environment's network, with the network's nodes, the
// subnet its endpoints take their addresses from and the service
// connection policy that lets it create them. Settings choose the node
// type and the replica count. In a member of a parameterized environment
// every node is its parent's.
func lowerMemoryStore(ctx registry.PlatformContext) (registry.Lowered, error) {
	d := ctx.Deployable
	env := ctx.Environment
	v := valuesOf(env)
	if err := checkLength("the Memorystore instance id of "+d.Name, d.ResourceName, 4, 63, "the stack or the MemoryStore service"); err != nil {
		return registry.Lowered{}, err
	}
	replicas := defaultMemoryStoreReplica
	if n, ok := d.Settings["replicas"]; ok {
		count, err := settingInt(n)
		if err != nil {
			return registry.Lowered{}, fmt.Errorf("memory store %s: replicas: %w", d.Name, err)
		}
		replicas = count
	}
	out := registry.Lowered{Resources: memoryStoreNetworkNodes(env, v)}
	out.Resources = append(out.Resources, &ir.Resource{
		ID:        d.Name + ".instance",
		Type:      TypeMemoryStoreInstance,
		Phase:     ir.PhaseInfrastructure,
		Inherited: len(env.Parameters) > 0,
		// The instance creates its endpoints under the policy, which must
		// exist first; no property of the instance names it.
		DependsOn: []string{memoryStorePolicyID},
		Properties: map[string]any{
			"project":               v.project,
			"location":              v.region,
			"instanceId":            d.ResourceName,
			"engineVersion":         memoryStoreEngine,
			"mode":                  memoryStoreMode,
			"shardCount":            memoryStoreShards,
			"replicaCount":          replicas,
			"nodeType":              settingString(d.Settings, "nodeType", defaultMemoryStoreNode),
			"authorizationMode":     memoryStoreAuthorization,
			"transitEncryptionMode": memoryStoreTransit,
			"persistenceConfig":     map[string]any{"mode": "DISABLED"},
			// A store holds no fact (D75), so a destroy may delete it.
			"deletionProtectionEnabled": false,
			"desiredAutoCreatedEndpoints": []any{map[string]any{
				"network":   ir.Output{Resource: networkID, Name: "id"},
				"projectId": v.project,
			}},
		},
	})
	return out, nil
}

// settingInt reads an integer setting, which the settings schema checked:
// a JSON number decodes as a float64.
func settingInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n == float64(int(n)) {
			return int(n), nil
		}
	}
	return 0, fmt.Errorf("%v is not a whole number", v)
}

// connectMemoryStore realizes a memoryStore edge from a Cloud Run server,
// job (D52), worker pool (D53) or deriver (D74) to a memory store (D75).
// Memorystore has no IAM policy of an instance's own, so the workload's
// account gets the connection role on the project, held to the edge's
// instance by an IAM condition on its resource name. The workload reaches
// the instance's endpoint through Direct VPC egress (lowerWorkload).
//
// The derived value is how to reach the store, with no credential: its
// rediss URL, the instance's managed server CA, IAM authentication, which
// each runtime does with an access token for the workload's account from
// the metadata server, and in a member of a parameterized environment the
// member's name, which prefixes its names on the parent's instance. A Go,
// TypeScript or Rust workload's edge lowers alike.
func connectMemoryStore(ctx registry.ConnectorContext) (registry.Connected, error) {
	from, to := ctx.From, ctx.To
	env := ctx.Environment
	v := valuesOf(env)
	value := ir.MemoryStoreConnection{
		Store: ctx.Edge.Service.Name,
		URL:   to.Address,
		CA:    ir.Output{Resource: to.Name + ".instance", Name: memoryStoreCAOutput},
		Auth:  ir.MemoryStoreAuthIAM,
	}
	if member := memoryStoreMember(env); member != nil {
		value.Member = member
	}
	return registry.Connected{
		Resources: []*ir.Resource{{
			ID:   from.Name + ".memorystore." + to.Name,
			Type: TypeProjectIAMMember,
			Properties: map[string]any{
				"project": v.project,
				"role":    memoryStoreConnectionRole,
				"member":  ir.Output{Resource: from.Name + ".account", Name: "member"},
				"condition": map[string]any{
					"title": join("Memorystore instance ", to.ResourceName),
					"expression": join(
						`resource.type == "memorystore.googleapis.com/Instance" && resource.name == "projects/`,
						v.project, "/locations/", v.region, "/instances/", to.ResourceName, `"`,
					),
				},
			},
		}},
		Value: value,
	}, nil
}

// memoryStoreMember is the member's name in a member of a parameterized
// environment, as the target suffixes its other names: each parameter's
// name and value, `pr123`, joined by `-` under two parameters. It is nil
// in any other environment.
func memoryStoreMember(env registry.StackEnvironment) any {
	if len(env.Parameters) == 0 {
		return nil
	}
	var parts []any
	for i, param := range env.Parameters {
		if i > 0 {
			parts = append(parts, "-")
		}
		parts = append(parts, param, ir.Parameter(param))
	}
	return join(parts...)
}

// checkMemoryStores refuses a Memorystore instance that a client could
// reach without TLS, or with a password rather than its account's token
// (D75): transit encryption other than SERVER_AUTHENTICATION, or
// authorization other than IAM_AUTH. A rate limit's key names a client, so
// no store goes without either, whatever deployable owns it.
func checkMemoryStores(env *ir.ResolvedEnvironment) []string {
	var out []string
	for _, res := range env.Resources.Resources {
		if res.Type != TypeMemoryStoreInstance {
			continue
		}
		owners := strings.Join(res.Owners, ", ")
		if mode := res.Properties["transitEncryptionMode"]; mode != memoryStoreTransit {
			out = append(out, fmt.Sprintf("Memorystore instance %s of %s has transit encryption %v; a memory store sets %s, so its clients verify it over TLS", res.ID, owners, describeMode(mode), memoryStoreTransit))
		}
		if mode := res.Properties["authorizationMode"]; mode != memoryStoreAuthorization {
			out = append(out, fmt.Sprintf("Memorystore instance %s of %s has authorization %v; a memory store sets %s, so each client authenticates as its own account", res.ID, owners, describeMode(mode), memoryStoreAuthorization))
		}
	}
	return out
}

// describeMode names a mode property's value in a finding, or says it is
// not set.
func describeMode(v any) string {
	if v == nil {
		return "unset"
	}
	return fmt.Sprintf("%q", v)
}

// memoryStoreFrom are the platforms a memoryStore edge runs from: every
// platform whose deployables serve an API, whose memoryStores they reach.
var memoryStoreFrom = []string{CloudRun, CloudRunJob, CloudRunWorker, CloudRunDeriver}

// memoryStoreConnectors are the connectors from each of memoryStoreFrom to
// the Memorystore platform.
func memoryStoreConnectors() []registry.ConnectorSpec {
	names := map[string]string{
		CloudRun:        MemoryStoreConnector,
		CloudRunJob:     JobMemoryStoreConnector,
		CloudRunWorker:  WorkerMemoryStoreConnector,
		CloudRunDeriver: DeriverMemoryStoreConnector,
	}
	var out []registry.ConnectorSpec
	for _, from := range memoryStoreFrom {
		out = append(out, registry.ConnectorSpec{Name: names[from], Extension: Name, Edge: ir.EdgeMemoryStore, From: from, To: Memorystore, Connect: connectMemoryStore})
	}
	return out
}
