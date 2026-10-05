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
