package generator

import (
	"fmt"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// An API's MCP endpoint runs a tool whose invocation policy asks only once
// the person confirms the call (D93), and signs each confirmation with a
// key every replica holds: an @envVars Secret<string> the API's config
// names, outputs.api.mcpConfirmationKey, by the type that declares the
// field and the field's name, as a stack names the secret and as
// outputs.sql.encryption.key names a DB service's (D85). The API's
// @envVars class holds the field, so each of its servers reads the one
// secret each environment sets, which the stack binds to every deployable
// of the API, and the entrypoint builds the router with it.

// MCPConfirmationDocs is the docs section on the confirmation of a tool
// that asks, which the key's errors point at.
const MCPConfirmationDocs = "https://parable-work.github.io/superschematic/reference/mcp-tools/#confirming-a-tool-that-asks"

// suggestedMCPConfirmationKey is the key an API's config could name, for
// an error that asks for one.
const suggestedMCPConfirmationKey = "McpKeys.MCP_CONFIRMATION_KEY"

// mcpConfirmationKeyField is the field of the API's @envVars class that
// holds the key its MCP endpoint, mcp, signs confirmations with: nil when
// the endpoint serves no tool that asks. It refuses an API whose config
// names no key, or whose @envVars lacks the field the key names, or holds
// a field of its name that is another secret or no Secret<string>.
func (r run) mcpConfirmationKeyField(mcp *tsrestgen.MCPInfo) (*ir.FieldDef, error) {
	if mcp == nil || len(mcp.Confirming) == 0 {
		return nil, nil
	}
	tools := strings.Join(mcp.Confirming, ", ")
	key := r.Outputs.MCPConfirmationKey()
	if key == "" {
		return nil, fmt.Errorf("generator: %s serves MCP tools that run only once the person confirms the call (%s), and its endpoint signs their confirmations with an @envVars Secret<string> its config names; set outputs.api.mcpConfirmationKey in %s's config to the field's declaring type and name, such as %q, and declare that field, a random value of at least 32 bytes in each environment, in %s's @envVars class or a class it extends (see %s)",
			r.Config.Name, tools, r.Config.Name, suggestedMCPConfirmationKey, r.Config.Name, MCPConfirmationDocs)
	}
	typeName, field, _ := registry.SplitEncryptionKey(key)
	fd, problem := envVarsSecret(r.Schema, typeName, field)
	if fd == nil {
		return nil, fmt.Errorf("generator: %s's MCP endpoint signs the confirmations of %s with the secret %s (outputs.api.mcpConfirmationKey in %s's config), and %s; %s",
			r.Config.Name, tools, key, r.Config.Name, problem, r.envVarsSecretFix(typeName, field, MCPConfirmationDocs))
	}
	return fd, nil
}
