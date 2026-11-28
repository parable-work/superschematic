package agents

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/parable-work/superschematic/runtime/http/go/mcp"
)

// MCPCallGate is the gate a generated Go API's MCP endpoint holds each
// call to (mcp.EndpointOptions.CallGate, D158, D183, #621): a call an
// agent run makes with its token, whose principal Authenticator put on the
// endpoint's request, is held to the run's autonomy policy by the tool's
// @autonomy (MCPGate), so an ask answers 428 approval_required until an
// approval of the call's exact parameters is approved, runs once, and a
// call with any parameter changed asks again. A call by any other caller
// runs. TypeScript's agents.mcpGate and Rust's Agents::mcp_gate.
func (a *Agents) MCPCallGate() mcp.CallGate {
	return mcp.CallGateFunc(func(ctx context.Context, tool mcp.RouteTool, args map[string]any) (json.RawMessage, error) {
		var autonomy *ToolAutonomy
		if tool.Autonomy != nil {
			autonomy = &ToolAutonomy{Class: tool.Autonomy.Class, Mode: tool.Autonomy.Mode}
		}
		gated, err := a.MCPGate(context.WithoutCancel(ctx), RunPrincipalOf(ctx), tool.Name, autonomy, args)
		if err != nil || gated == nil {
			return nil, err
		}
		problem := map[string]any{"type": "about:blank", "title": gated.Code, "status": gated.Status, "detail": gated.Detail, "code": gated.Code}
		if gated.Approval != "" {
			problem["approval"] = gated.Approval
		}
		return json.Marshal(problem)
	})
}

// gatedElsewhere reports whether r is held to the run's autonomy by the
// API's MCP endpoint rather than by Authenticator: the endpoint's own
// request, whose tools/call the endpoint holds per tool by its @autonomy
// (MCPCallGate), and the request a call or a check of a tool makes to its
// route, which that gate held already.
func gatedElsewhere(r *http.Request) bool {
	return r.URL.Path == mcp.Path || mcp.ToolCallOf(r.Context()) != nil
}
