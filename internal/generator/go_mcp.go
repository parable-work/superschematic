package generator

import (
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/mcptools"
)

// A Go API's MCP endpoint (outputs.api.mcp, D81, D116, #660): the tool
// table the TypeScript and Rust routers hold (mcptools.Build), each tool
// with its route's method and path, which the Go runtime's mcp.Mount sends
// a call to through the API's router, and its operation's @idempotent
// (D140), as the Rust table carries it. apigen writes mcp.go from it.

// goRouteTool is a tool as the Go runtime's mcp.RouteTool reads it.
type goRouteTool struct {
	mcptools.Tool
	Method     string             `json:"method"`
	Path       string             `json:"path"`
	Idempotent *goToolIdempotency `json:"idempotent,omitempty"`
}

type goToolIdempotency struct {
	Required bool `json:"required,omitempty"`
}

// goMCP is the MCP endpoint of output, the Go server's output of the API:
// nil with outputs.api.mcp off.
func (r run) goMCP(output *apigen.APIOutput) (*apigen.GoMCP, error) {
	if !r.Outputs.APIEnabled() || !r.Outputs.API.MCP {
		return nil, nil
	}
	endpoints := map[string]apigen.EndpointInfo{}
	byOperation := map[string]mcptools.Route{}
	requiresAuth := output.Auth.Identity
	for _, endpoint := range output.Endpoints {
		key := endpoint.Namespace + "." + endpoint.Name
		route := mcptools.Route{Key: key, Method: endpoint.Method, Path: endpoint.Path}
		if endpoint.Filter != nil {
			route.FilterArgument = endpoint.Filter.Argument
		}
		byOperation[endpoint.HandlerName] = route
		endpoints[key] = endpoint
		requiresAuth = requiresAuth || (endpoint.RequiresAuth && !endpoint.PublicRoute)
	}
	routeOf := func(operationID string) (mcptools.Route, bool) {
		route, ok := byOperation[operationID]
		return route, ok
	}
	built, err := mcptools.Build("apigen", r.Config.Name, output, routeOf, requiresAuth, r.Options.Clock)
	if err != nil {
		return nil, err
	}
	mcp := &apigen.GoMCP{RequiresAuth: built.RequiresAuth, Confirming: built.Confirming, Left: built.Left}
	tools := make([]goRouteTool, 0, len(built.Tools))
	for _, tool := range built.Tools {
		endpoint := endpoints[tool.Operation]
		routed := goRouteTool{Tool: tool, Method: endpoint.Method, Path: endpoint.Path}
		if endpoint.Idempotent != nil {
			routed.Idempotent = &goToolIdempotency{Required: endpoint.Idempotent.Required}
		}
		tools = append(tools, routed)
		mcp.Tools = append(mcp.Tools, apigen.GoMCPTool{Name: tool.Name, Endpoint: endpoint})
	}
	if mcp.ToolsJSON, err = mcptools.JSONLiteral(tools, true); err != nil {
		return nil, err
	}
	// The endpoint signs the confirmations of the tools that ask with the
	// secret the API's config names, which its @envVars holds (D93).
	field, err := r.mcpConfirmationKeyField(built)
	if err != nil {
		return nil, err
	}
	if field != nil {
		mcp.ConfirmationKey = envgen.GoFieldName(field.Name)
	}
	return mcp, nil
}
