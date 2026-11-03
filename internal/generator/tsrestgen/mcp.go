package tsrestgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
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
	MCPPath = "/api/mcp"
	// MCPConfirmationArgument is the tools/call argument a confirmation
	// token comes back in (D93), the runtime's MCP_CONFIRMATION_ARGUMENT,
	// so a tool that asks takes no argument of its name.
	MCPConfirmationArgument = "confirmationToken"
)

// MCPInfo is the API's MCP endpoint (D81): the router serves its visible
// @mcp tools at POST /api/mcp, and runs each call through the route of the
// tool's operation.
type MCPInfo struct {
	// Tools are the tools the endpoint serves, in the order of the tool
	// documents: each visible @mcp tool whose arguments a JSON call can
	// carry.
	Tools []MCPTool
	// Left are the visible tools it leaves out, by handle, with why: a file
	// upload, which tool arguments cannot carry, and an encrypted payload,
	// which only the SDK seals.
	Left []string
	// RequiresAuth reports whether the endpoint needs a caller: the
	// identity runtime authenticates the API, or a route of it needs one.
	RequiresAuth bool
	// Confirming are the handles of the tools it runs only once the person
	// confirms the call (D93), whose confirmations it signs with the
	// secret outputs.api.mcpConfirmationKey names.
	Confirming []string
}

// MCPTool is one tool as mcp.ts declares it, the runtime's McpRouteTool:
// what tools/list answers, as the engine lists its tools (the handle as the
// name, the @docs title and description, tools/schema.json's parameters as
// the input schema, whether its @docs replay mode only reads, and its _meta
// with the invocation policy), and the operation whose route a call runs
// through, by its key in the router's operation table (EndpointInfo.Key),
// with where each argument goes in that route's request, and
// whether a call runs only once the person confirms it: its policy is one
// the build's policy confirms (D93).
type MCPTool struct {
	Name        string             `json:"name"`
	Title       string             `json:"title"`
	Description string             `json:"description"`
	InputSchema json.RawMessage    `json:"inputSchema"`
	Annotations MCPToolAnnotations `json:"annotations"`
	Meta        map[string]any     `json:"_meta"`
	Operation   string             `json:"operation"`
	Arguments   []MCPToolArgument  `json:"arguments"`
	Confirm     bool               `json:"confirm,omitempty"`
}

// MCPToolAnnotations are a tool's MCP annotations.
type MCPToolAnnotations struct {
	ReadOnlyHint bool `json:"readOnlyHint"`
}

// MCPToolArgument is where one tool argument goes: in the path, the query
// string or the JSON body object, under its wire name.
type MCPToolArgument struct {
	Key  string `json:"key"`
	In   string `json:"in"`
	Name string `json:"name"`
}

// ToolsLiteral is mcp.ts's tool table, as JSON, which is a TypeScript
// expression.
func (m *MCPInfo) ToolsLiteral() (string, error) {
	return jsonLiteral(m.Tools, true)
}

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
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if indent {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}

// mcpInfo builds the MCP endpoint of output from the tool documents the
// SDK generators write (sdkgen.GenerateTools, tools/schema.json's source),
// so a tool's arguments are those documents' parameters, and from the
// router's own endpoints, so each argument goes where the route reads it:
// tools/mcp-binding.json's path and query arguments to the path and the
// query string, an input type's fields and a body argument to the JSON body
// object, and a GET operation's scalar arguments to the query string, as
// apigen's ArgumentsInBody places them. It refuses an operation routed at
// the endpoint's path.
func mcpInfo(output *APIOutput, apiOutput *apigen.APIOutput, clock codegen.Clock) (*MCPInfo, error) {
	if len(apiOutput.Endpoints) != len(output.Endpoints) {
		return nil, fmt.Errorf("tsrestgen: %s has %d endpoints and its router %d", output.SchemaName, len(apiOutput.Endpoints), len(output.Endpoints))
	}
	byOperation := make(map[string]EndpointInfo, len(output.Endpoints))
	info := &MCPInfo{RequiresAuth: output.Identity != nil}
	for i, ep := range apiOutput.Endpoints {
		endpoint := output.Endpoints[i]
		if endpoint.Path == MCPPath {
			return nil, fmt.Errorf("tsrestgen: operation %s.%s is routed at %s %s, where outputs.api.mcp serves the MCP endpoint (D81); give it another @rest path, or turn mcp off", ep.Namespace, ep.Name, endpoint.Method, MCPPath)
		}
		byOperation[ep.HandlerName] = endpoint
		info.RequiresAuth = info.RequiresAuth || (endpoint.RequiresAuth && !endpoint.PublicRoute)
	}
	sdkOutput, err := sdkgen.Generate(apiOutput, nil, clock)
	if err != nil {
		return nil, fmt.Errorf("tsrestgen: the MCP tools of %s: %w", output.SchemaName, err)
	}
	tools, err := sdkgen.GenerateTools(sdkOutput, apiOutput, clock)
	if err != nil {
		return nil, fmt.Errorf("tsrestgen: the MCP tools of %s: %w", output.SchemaName, err)
	}
	if tools == nil {
		return info, nil
	}
	for _, tool := range tools.VisibleTools {
		switch {
		case tool.BindingStatus != "ready":
			info.Left = append(info.Left, tool.MCP.Handle+": it uploads a file, which tool arguments cannot carry")
			continue
		case tool.Encrypted:
			info.Left = append(info.Left, tool.MCP.Handle+": its payload is encrypted, which only the SDK seals")
			continue
		}
		endpoint, ok := byOperation[tool.OperationID]
		if !ok {
			return nil, fmt.Errorf("tsrestgen: MCP tool %s names operation %s, which the router of %s does not mount", tool.MCP.Handle, tool.OperationID, output.SchemaName)
		}
		mcpTool, err := routeTool(tool, endpoint)
		if err != nil {
			return nil, fmt.Errorf("tsrestgen: MCP tool %s of %s: %w", tool.MCP.Handle, output.SchemaName, err)
		}
		// A tool whose policy the build's policy confirms runs once the
		// person confirms the call, whose token comes back in an argument
		// of its own (D93).
		if apiOutput.ToolInvocation.OrDefault().Confirms(tool.MCP.Invocation.Value) {
			if slices.ContainsFunc(mcpTool.Arguments, func(a MCPToolArgument) bool { return a.Key == MCPConfirmationArgument }) {
				return nil, fmt.Errorf("tsrestgen: MCP tool %s of %s asks the person before it runs (%s %q), and takes an argument %s, which carries the call's confirmation token (D93); rename the argument", tool.MCP.Handle, output.SchemaName, tool.MCP.Invocation.Key, tool.MCP.Invocation.Value, MCPConfirmationArgument)
			}
			mcpTool.Confirm = true
			info.Confirming = append(info.Confirming, mcpTool.Name)
		}
		info.Tools = append(info.Tools, mcpTool)
	}
	return info, nil
}

// routeTool is tool as the router serves it, through endpoint's route.
func routeTool(tool sdkgen.ToolDefinition, endpoint EndpointInfo) (MCPTool, error) {
	parameters, err := json.Marshal(tool.Parameters)
	if err != nil {
		return MCPTool{}, err
	}
	meta := maps.Clone(tool.MCP.Meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[tool.MCP.Invocation.Key] = tool.MCP.Invocation.Value
	out := MCPTool{
		Name:        tool.MCP.Handle,
		Title:       tool.MCP.Name,
		Description: tool.MCP.Description,
		InputSchema: parameters,
		Annotations: MCPToolAnnotations{ReadOnlyHint: tool.ReplayMode == "read_only"},
		Meta:        meta,
		Operation:   endpoint.Key,
		Arguments:   []MCPToolArgument{},
	}
	for _, argument := range tool.MCPBinding.Arguments {
		place, name, ok := strings.Cut(argument.Target, ".")
		if !ok {
			return out, fmt.Errorf("argument %s has target %q", argument.ToolParameter, argument.Target)
		}
		var in string
		switch {
		case argument.Kind == "path" && place == "path":
			in = "path"
		case argument.Kind == "query" && place == "query":
			in = "query"
		case argument.Kind == "input" && place == "input":
			in = "body"
		case argument.Kind == "scalar" && place == "input" && endpoint.Method == "GET":
			in = "query"
		case argument.Kind == "scalar" && place == "input":
			in = "body"
		default:
			return out, fmt.Errorf("argument %s is a %s argument for %s, which a JSON call cannot carry", argument.ToolParameter, argument.Kind, argument.Target)
		}
		out.Arguments = append(out.Arguments, MCPToolArgument{Key: argument.ToolParameter, In: in, Name: name})
	}
	// Every argument the schema lists reaches the route, and no other.
	keys := make([]string, 0, len(out.Arguments))
	for _, argument := range out.Arguments {
		keys = append(keys, argument.Key)
	}
	for _, property := range slices.Sorted(maps.Keys(tool.Parameters.Properties)) {
		if !slices.Contains(keys, property) {
			return out, fmt.Errorf("its argument %s reaches no parameter of %s %s", property, endpoint.Method, endpoint.Path)
		}
	}
	return out, nil
}
