package ext

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// ToolsAPI is the API schema whose every operation acme requires to declare
// @mcp, visible or hidden. The core publishes only the operations that opt
// in and asks nothing of the rest.
const ToolsAPI = "shop-api"

// registerMCPPolicy lays acme's policy over the core @mcp decorator: a
// check that fails the load when an operation of ToolsAPI has no MCP
// classification.
func registerMCPPolicy(r *registry.Registry) error {
	return r.RegisterCheck(registry.CheckSpec{
		Name:      "acmeToolsClassified",
		Extension: Name,
		Kinds:     []string{string(ir.SchemaKindAPI)},
		Verify: func(schema *ir.Schema, rep registry.VerifyReporter) {
			if schema.Name != ToolsAPI {
				return
			}
			for _, set := range schema.OperationSets {
				for _, op := range set.Operations {
					if op.MCP == nil {
						rep.Errorf("", "%s.%s: every operation of %s declares @mcp, visible or hidden",
							set.Name, op.Name, ToolsAPI)
					}
				}
			}
		},
	})
}
