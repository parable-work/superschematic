package gcp

import (
	"strings"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// Secrets live in Secret Manager (sections 4.2 and 7.2). A secret is
// identified by the type that declares the `Secret<T>` field and the
// field's name, so every server that reads it lowers the same secret node,
// which resolution shares between them, and an accessor grant of its own.
// Nothing here writes a value: `stack secrets set` adds the versions.

// secretName is the Secret Manager id of a stack secret: the stack, the
// declaring type and the field joined with hyphens
// (`Shop-PaymentsSecrets-STRIPE_KEY`). No TypeScript identifier holds a
// hyphen, so the three parts never run together.
func secretName(stack, secret string) string {
	return stack + "-" + strings.ReplaceAll(secret, ".", "-")
}

// secretMemberNodes returns a workload's nodes for the variable of one
// secret member of a derived field (ir.DerivedVariable.Secret), such as
// the generic connector's private key (D63), whose value references an
// output of another node: a Secret Manager secret of the workload's own, a
// version of it whose data is that output, and the workload's accessor
// grant. source is the variable's valueSource: the secret, at the
// version, so a new key rolls out a new revision. The value never reaches
// the graph or the program as a plain value: Pulumi passes the output from
// the key node to the version, and keeps it encrypted in the state.
//
// The secret's id is the stack, the environment, the workload's name and
// the variable joined with hyphens
// (`Shop-Staging-orders-SHOP_EDGE_SERVICE_CREDENTIAL_KEY`), which a
// parameter's value suffixes through the workload's name, so each member
// of a parameterized environment has its own: the key is an edge's, and
// each member's edges are its own. A stack secret's id has two hyphens
// after the stack (secretName), so the two never meet.
func secretMemberNodes(env registry.StackEnvironment, v values, d ir.ResolvedDeployable, variable ir.DerivedVariable, member ir.Output) (nodes []*ir.Resource, source map[string]any) {
	id := d.Name + ".secret." + variable.Name
	version := d.Name + ".secret-version." + variable.Name
	return []*ir.Resource{
			{
				ID:    id,
				Type:  TypeSecret,
				Phase: ir.PhaseInfrastructure,
				Properties: map[string]any{
					"project":     v.project,
					"secretId":    join(env.Stack, "-", env.Name, "-", d.ResourceName, "-", variable.Name),
					"replication": map[string]any{"auto": map[string]any{}},
				},
			},
			{
				ID:    version,
				Type:  TypeSecretVersion,
				Phase: ir.PhaseInfrastructure,
				Properties: map[string]any{
					"secret":     ir.Output{Resource: id, Name: "id"},
					"secretData": variable.Value,
				},
			},
			{
				ID:    d.Name + ".reads." + variable.Name,
				Type:  TypeSecretIAMMember,
				Phase: ir.PhaseInfrastructure,
				Properties: map[string]any{
					"project":  v.project,
					"secretId": ir.Output{Resource: id, Name: "id"},
					"role":     "roles/secretmanager.secretAccessor",
					"member":   member,
				},
			},
		}, map[string]any{"secretKeyRef": map[string]any{
			"secret":  ir.Output{Resource: id, Name: "secretId"},
			"version": ir.Output{Resource: version, Name: "version"},
		}}
}

// secretNodes returns a server's nodes for one secret binding: the secret,
// which a member of a parameterized environment inherits from its parent
// (the preview reads staging's values), and the server's accessor grant.
// ref is what the server's environment variable references.
func secretNodes(env registry.StackEnvironment, v values, server, secret string, member ir.Output) (nodes []*ir.Resource, ref ir.Output) {
	id := "secret." + secret
	return []*ir.Resource{
		{
			ID:        id,
			Type:      TypeSecret,
			Phase:     ir.PhaseInfrastructure,
			Inherited: len(env.Parameters) > 0,
			Properties: map[string]any{
				"project":     v.project,
				"secretId":    secretName(env.Stack, secret),
				"replication": map[string]any{"auto": map[string]any{}},
			},
		},
		{
			ID:    server + ".reads." + secret,
			Type:  TypeSecretIAMMember,
			Phase: ir.PhaseInfrastructure,
			Properties: map[string]any{
				"project":  v.project,
				"secretId": ir.Output{Resource: id, Name: "id"},
				"role":     "roles/secretmanager.secretAccessor",
				"member":   member,
			},
		},
	}, ir.Output{Resource: id, Name: "secretId"}
}
