package rustrestgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/mcptools"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
)

// MCPFile is the module of the crate that holds its MCP endpoint, under
// src, with outputs.api.mcp on (D81, D116): a child of the router's
// module, since its checks decode a request by the router's own functions.
const MCPFile = "router/mcp.rs"

// MCPInfo is the API's MCP endpoint (D81, D116): the router serves its
// visible @mcp tools at POST /api/mcp, as the TypeScript router does, and
// runs each call as a request to the route of the tool's operation. Its
// tools are the table the TypeScript router's mcp.ts holds (mcptools), each
// naming its operation as the crate's operation table does,
// <namespace>.<name> (OperationInfo).
type MCPInfo struct {
	*mcptools.Endpoint
	// ConfirmationKey is the field of the API's env config that holds the
	// key the endpoint signs confirmations with, which a server's
	// entrypoint builds the router with: the @envVars secret
	// outputs.api.mcpConfirmationKey names. Empty for an endpoint that
	// confirms no tool. The dispatch layer sets it.
	ConfirmationKey string
}

// ToolsRust is the tool table as a Rust raw string literal of its JSON,
// which the runtime's Mount reads.
func (m *MCPInfo) ToolsRust() (string, error) {
	literal, err := m.ToolsLiteral()
	if err != nil {
		return "", err
	}
	return rustutil.RawString(literal), nil
}

// Checks reports whether the router holds its routes' checks (D98): a tool
// asks, so its call is checked through its route before the person is
// asked.
func (m *MCPInfo) Checks() bool {
	return len(m.Confirming) > 0
}

// ConfirmingList names the tools that ask, for a doc comment.
func (m *MCPInfo) ConfirmingList() string {
	return strings.Join(m.Confirming, ", ")
}

// OperationKey is the operation's key in the crate's operation table, by
// which an MCP tool names it: its OperationInfo's namespace and name.
func (e EndpointInfo) OperationKey() string {
	return e.Namespace + "." + e.Name
}

// CheckControlCalls are the RouteControls builder calls of the route's
// check (D98): ControlCalls but the rate limit, which counts the runs
// alone, so a call that is checked, confirmed and run takes one token, as a
// request does.
func (e EndpointInfo) CheckControlCalls(authenticator, serviceAuthenticator string) []string {
	e.RateLimit = 0
	return e.ControlCalls("", authenticator, serviceAuthenticator)
}

// IdentityCheckControlCalls are CheckControlCalls of a user model's route,
// whose permission the identity runtime's handler checks itself
// (IdentityControlCalls).
func (e EndpointInfo) IdentityCheckControlCalls(authenticator, serviceAuthenticator string) []string {
	e.RequiredPerms = nil
	return e.CheckControlCalls(authenticator, serviceAuthenticator)
}

// HasCheckControls reports whether the route's check has a control: any of
// the route's but its rate limit.
func (e EndpointInfo) HasCheckControls() bool {
	return e.RequiresAuth || e.ServiceStep || e.BodyLimit > 0 || e.Timeout > 0
}

// mcpInfo builds the MCP endpoint of output from the tool documents the
// SDK generators write, tools/schema.json's source, and the crate's own
// routes, mounted, manual and the user model's alike (mcptools.Build). The
// endpoint needs a caller when a route of the crate does.
func mcpInfo(output *APIOutput, api *apigen.APIOutput, clock codegen.Clock) (*MCPInfo, error) {
	byOperation := make(map[string]mcptools.Route)
	for _, endpoint := range output.AllEndpoints() {
		route := mcptools.Route{Key: endpoint.OperationKey(), Method: strings.ToUpper(endpoint.Method), Path: endpoint.Path}
		if endpoint.Filter != nil {
			route.FilterArgument = endpoint.Filter.Argument
		}
		byOperation[endpoint.OperationID] = route
	}
	routeOf := func(operationID string) (mcptools.Route, bool) {
		route, ok := byOperation[operationID]
		return route, ok
	}
	endpoint, err := mcptools.Build("rustrestgen", output.SchemaName, api, routeOf, output.HasAuth, clock)
	if err != nil {
		return nil, err
	}
	return &MCPInfo{Endpoint: endpoint}, nil
}

// writeMCP writes src/router/mcp.rs with outputs.api.mcp on; one an
// earlier build wrote goes when it is off, since the router declares no
// such module then.
func writeMCP(output *APIOutput, outputDir string) error {
	path := filepath.Join(outputDir, "src", filepath.FromSlash(MCPFile))
	if output.MCP == nil {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove stale %s: %w", MCPFile, err)
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := generateFile("mcp.tmpl", path, output); err != nil {
		return fmt.Errorf("generate %s: %w", MCPFile, err)
	}
	return nil
}
