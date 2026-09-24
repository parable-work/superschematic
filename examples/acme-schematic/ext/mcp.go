package ext

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// ToolsAPI is the API schema whose every operation acme requires to declare
// @mcp, visible or hidden. The core publishes only the operations that opt
// in and asks nothing of the rest.
const ToolsAPI = "shop-api"

// The vendor keys acme's SDK tool documents carry in place of the core's.
const (
	ToolScalarKey   = "x-acme-scalar"
	ToolGuidanceKey = "acme/operation-guidance"
	// ToolArgumentsKey versions acme's reading of a tool's arguments; every
	// argument schema carries it.
	ToolArgumentsKey = "x-acme-arguments"
)

// IconFamily and IconStyle name the variant of acme's icon set every tool
// icon is drawn from.
const (
	IconFamily = "acme"
	IconStyle  = "outline"
)

// acme's invocation policy, in place of the core's invocationPolicy: a
// visible tool declares confirm: "always" when a client must ask the person
// before it runs; without the key it is "never".
const (
	ConfirmKey    = "confirm"
	ConfirmNever  = "never"
	ConfirmAlways = "always"
)

// registerMCPPolicy lays acme's policy over the core MCP tools: a check that
// fails the load when an operation of ToolsAPI has no MCP classification, a
// tool hook that writes acme's vendor keys and fills in each tool icon's
// family and style, and acme's invocation policy. None needs a core option.
func registerMCPPolicy(r *registry.Registry) error {
	if err := r.RegisterToolInvocationPolicy(registry.ToolInvocationPolicy{
		Extension: Name,
		Key:       ConfirmKey,
		Values:    []string{ConfirmNever, ConfirmAlways},
		Default:   ConfirmNever,
	}); err != nil {
		return err
	}
	if err := r.RegisterToolHook(registry.ToolHook{
		Name:      "acmeTools",
		Extension: Name,
		Edit: func(_ *ir.Schema, tools *registry.ToolSet) error {
			tools.Keys.Scalar = ToolScalarKey
			tools.Keys.Guidance = ToolGuidanceKey
			tools.Keys.Parameters = append(tools.Keys.Parameters, registry.ToolKeyValue{Key: ToolArgumentsKey, Value: 1})
			for _, tool := range tools.Tools {
				if tool.MCP != nil && tool.MCP.Icon != nil {
					tool.MCP.Icon.Family, tool.MCP.Icon.Style = IconFamily, IconStyle
				}
			}
			return nil
		},
	}); err != nil {
		return err
	}
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
