package tsrestgen

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/mcptools"
)

// The files of the package that serve the API's documents (D81): its
// OpenAPI document as a module, which the router serves at GET
// /api/openapi.json without reading a file, and, with outputs.api.mcp, its
// MCP tools, which the router serves at POST /api/mcp.
const (
	OpenAPIFile = "openapi.ts"
	MCPFile     = "mcp.ts"
	// MCPPath is where the router serves the MCP endpoint, the runtime's
	// MCP_PATH.
	MCPPath = mcptools.Path
	// MCPConfirmationArgument is the tools/call argument a confirmation
	// token comes back in (D93), the runtime's MCP_CONFIRMATION_ARGUMENT,
	// so a tool that asks takes no argument of its name.
	MCPConfirmationArgument = mcptools.ConfirmationArgument
)

// MCPInfo is the API's MCP endpoint (D81): the router serves its visible
// @mcp tools at POST /api/mcp, and runs each call through the route of the
// tool's operation. It is the table a Rust router serves too (D116), which
// mcptools builds for both.
type MCPInfo = mcptools.Endpoint

// MCPTool is one tool as mcp.ts declares it, the runtime's McpRouteTool,
// MCPToolAnnotations its MCP annotations, and MCPToolArgument where one of
// its arguments goes in its route's request.
type (
	MCPTool            = mcptools.Tool
	MCPToolAnnotations = mcptools.ToolAnnotations
	MCPToolArgument    = mcptools.ToolArgument
)

// OpenAPILiteral is openapi.ts's document: openapi.json as a string
// literal.
func (o *APIOutput) OpenAPILiteral() (string, error) {
	return jsonLiteral(o.OpenAPISpecRaw, false)
}

// writeServedDocuments writes openapi.ts, and mcp.ts with outputs.api.mcp
// on; an mcp.ts an earlier build wrote goes when it is off, since nothing
// imports it.
func writeServedDocuments(output *APIOutput, outputDir string) error {
	if err := generateFile("openapi.tmpl", filepath.Join(outputDir, OpenAPIFile), output); err != nil {
		return fmt.Errorf("generate %s: %w", OpenAPIFile, err)
	}
	if output.MCP == nil {
		if err := os.Remove(filepath.Join(outputDir, MCPFile)); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale %s: %w", MCPFile, err)
		}
		return nil
	}
	if err := generateFile("mcp.tmpl", filepath.Join(outputDir, MCPFile), output); err != nil {
		return fmt.Errorf("generate %s: %w", MCPFile, err)
	}
	return nil
}

// jsonLiteral encodes value as JSON without HTML escapes, indented by two
// spaces when indent is set.
func jsonLiteral(value any, indent bool) (string, error) {
	return mcptools.JSONLiteral(value, indent)
}

// mcpInfo builds the MCP endpoint of output from the tool documents the
// SDK generators write (sdkgen.GenerateTools, tools/schema.json's source),
// so a tool's arguments are those documents' parameters, and from the
// router's own endpoints, so each argument goes where the route reads it
// (mcptools.Build). It refuses an operation routed at the endpoint's path.
func mcpInfo(output *APIOutput, apiOutput *apigen.APIOutput, clock codegen.Clock) (*MCPInfo, error) {
	if len(apiOutput.Endpoints) != len(output.Endpoints) {
		return nil, fmt.Errorf("tsrestgen: %s has %d endpoints and its router %d", output.SchemaName, len(apiOutput.Endpoints), len(output.Endpoints))
	}
	byOperation := make(map[string]mcptools.Route, len(output.Endpoints))
	requiresAuth := output.Identity != nil
	for i, ep := range apiOutput.Endpoints {
		endpoint := output.Endpoints[i]
		route := mcptools.Route{Key: endpoint.Key, Method: endpoint.Method, Path: endpoint.Path}
		if endpoint.Filter != nil {
			route.FilterArgument = endpoint.Filter.Argument
		}
		byOperation[ep.HandlerName] = route
		requiresAuth = requiresAuth || (endpoint.RequiresAuth && !endpoint.PublicRoute)
	}
	routeOf := func(operationID string) (mcptools.Route, bool) {
		route, ok := byOperation[operationID]
		return route, ok
	}
	return mcptools.Build("tsrestgen", output.SchemaName, apiOutput, routeOf, requiresAuth, clock)
}
