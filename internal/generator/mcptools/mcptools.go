// Package mcptools builds the tool table of an API's MCP endpoint
// (outputs.api.mcp, D81): each visible @mcp tool as tools/list answers it,
// with the operation whose route a tools/call runs through and where each
// argument goes in that route's request. The TypeScript router's mcp.ts and
// the Rust router's src/router/mcp.rs hold the one table (D116), written
// from what tools/schema.json is written from, so a tool is described once
// for both servers and for the SDKs' tool documents.
package mcptools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	ir "github.com/parable-work/superschematic/ir"
)

const (
	// Path is where a router serves the MCP endpoint, the runtimes'
	// MCP_PATH.
	Path = "/api/mcp"
	// ConfirmationArgument is the tools/call argument a confirmation token
	// comes back in (D93), the runtimes' confirmation argument, so a tool
	// that asks takes no argument of its name.
	ConfirmationArgument = "confirmationToken"
)

// Endpoint is an API's MCP endpoint (D81): the router serves its visible
// @mcp tools at POST /api/mcp, and runs each call through the route of the
// tool's operation.
type Endpoint struct {
	// Tools are the tools the endpoint serves, in the order of the tool
	// documents: each visible @mcp tool whose arguments a JSON call can
	// carry.
	Tools []Tool
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

// Tool is one tool as a router's table declares it, the runtimes' route
// tool: what tools/list answers, as the engine lists its tools (the handle
// as the name, the @docs title and description, tools/schema.json's
// parameters as the input schema, whether its @docs replay mode only reads,
// and its _meta with the invocation policy), and the operation whose route
// a call runs through, by its key in the router's operation table
// (Route.Key), with where each argument goes in that route's request, and
// whether a call runs only once the person confirms it: its policy is one
// the build's policy confirms (D93).
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations ToolAnnotations `json:"annotations"`
	Meta        map[string]any  `json:"_meta"`
	Operation   string          `json:"operation"`
	Arguments   []ToolArgument  `json:"arguments"`
	Confirm     bool            `json:"confirm,omitempty"`
}

// ToolAnnotations are a tool's MCP annotations.
type ToolAnnotations struct {
	ReadOnlyHint bool `json:"readOnlyHint"`
}

// ToolArgument is where one tool argument goes: in the path, the query
// string or the JSON body object, under its wire name. An operation's
// filter (D106) goes in "filter": the argument is an object, a member per
// field, which the runtime writes to the query string as the bracket form
// under Name, the parameter the brackets follow.
type ToolArgument struct {
	Key  string `json:"key"`
	In   string `json:"in"`
	Name string `json:"name"`
}

// Route is the route of one operation as a server's router mounts it: what
// a tool's call needs of it.
type Route struct {
	// Key is the operation's key in the router's operation table, which
	// the tool names its operation by.
	Key string
	// Method and Path are the route's, the path with a {name} for each
	// path parameter.
	Method string
	Path   string
	// FilterArgument is the operation's FilterBy argument (D106), by its
	// name in the schema; empty without one.
	FilterArgument string
}

// ToolsLiteral is the tool table as JSON, indented by two spaces, which is
// a TypeScript expression, and what the Rust runtime reads.
func (e *Endpoint) ToolsLiteral() (string, error) {
	return JSONLiteral(e.Tools, true)
}

// JSONLiteral encodes value as JSON without HTML escapes, indented by two
// spaces when indent is set.
func JSONLiteral(value any, indent bool) (string, error) {
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

// Build is the MCP endpoint of the API schema, as generator, the package
// that writes its router, serves it. Its tools come from the tool documents
// the SDK generators write (sdkgen.GenerateTools, tools/schema.json's
// source), so a tool's arguments are those documents' parameters, and from
// the router's own routes, so each argument goes where the route reads it:
// tools/mcp-binding.json's path and query arguments to the path and the
// query string, an input type's fields and a body argument to the JSON body
// object, and a GET operation's scalar arguments to the query string, as
// apigen's ArgumentsInBody places them. An operation's filter (D106), which
// the binding lists among its query arguments, goes to the query string as
// the bracket form the route's reader reads.
//
// routeOf answers the route of the operation whose OpenAPI operation id is
// operationID, apigen's HandlerName; false for one the router does not
// mount. requiresAuth reports whether the endpoint needs a caller. Build
// refuses an operation routed at the endpoint's path, a tool whose
// operation the router does not mount, and a tool that asks and takes an
// argument named ConfirmationArgument.
func Build(generator, schema string, api *apigen.APIOutput, routeOf func(operationID string) (Route, bool), requiresAuth bool, clock codegen.Clock) (*Endpoint, error) {
	info := &Endpoint{RequiresAuth: requiresAuth}
	for _, ep := range api.Endpoints {
		if ep.Path == Path {
			return nil, fmt.Errorf("%s: operation %s.%s is routed at %s %s, where outputs.api.mcp serves the MCP endpoint (D81); give it another @rest path, or turn mcp off", generator, ep.Namespace, ep.Name, strings.ToUpper(ep.Method), Path)
		}
	}
	sdkOutput, err := sdkgen.Generate(api, nil, clock)
	if err != nil {
		return nil, fmt.Errorf("%s: the MCP tools of %s: %w", generator, schema, err)
	}
	tools, err := sdkgen.GenerateTools(sdkOutput, api, clock)
	if err != nil {
		return nil, fmt.Errorf("%s: the MCP tools of %s: %w", generator, schema, err)
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
		route, ok := routeOf(tool.OperationID)
		if !ok {
			return nil, fmt.Errorf("%s: MCP tool %s names operation %s, which the router of %s does not mount", generator, tool.MCP.Handle, tool.OperationID, schema)
		}
		routed, err := RouteTool(tool, route)
		if err != nil {
			return nil, fmt.Errorf("%s: MCP tool %s of %s: %w", generator, tool.MCP.Handle, schema, err)
		}
		// A tool whose policy the build's policy confirms runs once the
		// person confirms the call, whose token comes back in an argument
		// of its own (D93).
		if api.ToolInvocation.OrDefault().Confirms(tool.MCP.Invocation.Value) {
			if slices.ContainsFunc(routed.Arguments, func(a ToolArgument) bool { return a.Key == ConfirmationArgument }) {
				return nil, fmt.Errorf("%s: MCP tool %s of %s asks the person before it runs (%s %q), and takes an argument %s, which carries the call's confirmation token (D93); rename the argument", generator, tool.MCP.Handle, schema, tool.MCP.Invocation.Key, tool.MCP.Invocation.Value, ConfirmationArgument)
			}
			routed.Confirm = true
			info.Confirming = append(info.Confirming, routed.Name)
		}
		info.Tools = append(info.Tools, routed)
	}
	return info, nil
}

// RouteTool is tool as a router serves it, through route.
func RouteTool(tool sdkgen.ToolDefinition, route Route) (Tool, error) {
	parameters, err := json.Marshal(tool.Parameters)
	if err != nil {
		return Tool{}, err
	}
	meta := maps.Clone(tool.MCP.Meta)
	if meta == nil {
		meta = map[string]any{}
	}
	meta[tool.MCP.Invocation.Key] = tool.MCP.Invocation.Value
	out := Tool{
		Name:        tool.MCP.Handle,
		Title:       tool.MCP.Name,
		Description: tool.MCP.Description,
		InputSchema: parameters,
		Annotations: ToolAnnotations{ReadOnlyHint: tool.ReplayMode == "read_only"},
		Meta:        meta,
		Operation:   route.Key,
		Arguments:   []ToolArgument{},
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
		case argument.Kind == "query" && place == "query" && route.FilterArgument != "" && name == route.FilterArgument:
			// The filter is no query parameter of one value: its members
			// travel as filter[field] and filter[field][op], whatever the
			// argument is named.
			in, name = "filter", ir.FilterParam
		case argument.Kind == "query" && place == "query":
			in = "query"
		case argument.Kind == "input" && place == "input":
			in = "body"
		case argument.Kind == "scalar" && place == "input" && strings.EqualFold(route.Method, "GET"):
			in = "query"
		case argument.Kind == "scalar" && place == "input":
			in = "body"
		default:
			return out, fmt.Errorf("argument %s is a %s argument for %s, which a JSON call cannot carry", argument.ToolParameter, argument.Kind, argument.Target)
		}
		out.Arguments = append(out.Arguments, ToolArgument{Key: argument.ToolParameter, In: in, Name: name})
	}
	// Every argument the schema lists reaches the route, and no other.
	keys := make([]string, 0, len(out.Arguments))
	for _, argument := range out.Arguments {
		keys = append(keys, argument.Key)
	}
	for _, property := range slices.Sorted(maps.Keys(tool.Parameters.Properties)) {
		if !slices.Contains(keys, property) {
			return out, fmt.Errorf("its argument %s reaches no parameter of %s %s", property, strings.ToUpper(route.Method), route.Path)
		}
	}
	return out, nil
}
