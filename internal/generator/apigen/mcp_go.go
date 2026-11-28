package apigen

import "strings"

// The MCP endpoint of a Go API (outputs.api.mcp, D81, D116, #660): mcp.go
// holds the tool table the TypeScript and Rust routers hold (mcptools),
// each tool with its route's method and path, and mounts the HTTP
// runtime's mcp endpoint over it at POST, GET and DELETE /api/mcp. A call
// of a tool is a request to its route through the API's own router, as
// the caller the endpoint's request authenticated; tools/list offers a
// tool whose route's permission check lets the caller through; and a tool
// that asks runs once the person confirms the call (D93), after its
// route's steps and decoding, stopped before the implementation, admit it
// (D98). The generator package builds GoMCP, since mcptools imports this
// package.

// MCPFile is the file of the API package that holds its MCP endpoint.
const MCPFile = "mcp.go"

// GoMCP is what mcp.go is written from.
type GoMCP struct {
	// ToolsJSON is the tool table, as JSON: the runtime's RouteTool.
	ToolsJSON string
	// RequiresAuth is true when a route of the API needs a caller, so the
	// endpoint does too.
	RequiresAuth bool
	// Tools are the tools, in the table's order.
	Tools []GoMCPTool
	// Confirming are the handles of the tools that ask (D93).
	Confirming []string
	// Left are the visible tools left out, by handle, with why.
	Left []string
	// ConfirmationKey is the field of the API's EnvConfig that holds the
	// key the endpoint signs confirmations with: the @envVars secret
	// outputs.api.mcpConfirmationKey names; empty when no tool asks.
	ConfirmationKey string
	// Instructions is outputs.api.mcpInstructions: domain guidance returned
	// to MCP clients on initialize unless Config.MCP.Instructions sets one.
	Instructions string
}

// GoMCPTool is one tool of mcp.go: its handle and its route's endpoint.
type GoMCPTool struct {
	Name     string
	Endpoint EndpointInfo
}

// Permitted reports whether tools/list asks the tool's route's permission
// check of the caller: its route has one.
func (t GoMCPTool) Permitted() bool { return len(t.Endpoint.RequiredPerms) > 0 }

// Checked reports whether the tool's route stops before its
// implementation for a route's check (D98): a generated handler's, which
// calls CheckContext there. A manual route's and the user model's do not.
func (t GoMCPTool) Checked() bool {
	return !t.Endpoint.ManualRouteRegistration && t.Endpoint.IdentityOperation == ""
}

// ProviderPermits reports whether a tool's permission check is the auth
// provider's routePermissions, whose imports mcp.go then takes.
func (m *GoMCP) ProviderPermits() bool {
	for _, tool := range m.Tools {
		if tool.Permitted() && tool.Endpoint.IdentityOperation == "" {
			return true
		}
	}
	return false
}

// IdentityPermits reports whether a tool's permission check is the
// identity runtime's.
func (m *GoMCP) IdentityPermits() bool {
	for _, tool := range m.Tools {
		if tool.Permitted() && tool.Endpoint.IdentityOperation != "" {
			return true
		}
	}
	return false
}

// HasChecked reports whether a tool stops at its route's check.
func (m *GoMCP) HasChecked() bool {
	for _, tool := range m.Tools {
		if tool.Checked() {
			return true
		}
	}
	return false
}

// ToolsLiteral is the tool table as a Go raw string literal, a backtick
// it holds written as a quoted one between two.
func (m *GoMCP) ToolsLiteral() string {
	return "`" + strings.ReplaceAll(m.ToolsJSON, "`", "` + \"`\" + `") + "`"
}

// ConfirmingList names the tools that ask, for a doc comment.
func (m *GoMCP) ConfirmingList() string { return strings.Join(m.Confirming, ", ") }

// HasMCP reports whether the API package holds mcp.go.
func (o *APIOutput) HasMCP() bool { return o.MCP != nil }
