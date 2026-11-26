package servergen_test

import (
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
)

// TestTypeScriptRouterHoldsAgentRunsToTheirAutonomy (D183): a server whose
// API's Deps holds the agent dispatcher and serves its MCP endpoint builds
// the router with the dispatcher's mcpGate as the endpoint's call gate,
// beside the endpoint's confirmation key, so a call a run's token makes is
// held to the run's autonomy by the tool's @autonomy; one that serves no
// MCP endpoint gates nothing.
func TestTypeScriptRouterHoldsAgentRunsToTheirAutonomy(t *testing.T) {
	api := &servergen.TypeScriptAPI{Service: "work-api", Deps: "workApiDeps", Agents: true, MCP: true, MCPConfirmationKey: "workApiConfig.MCP_KEY"}
	if got, want := api.RouterOptions(), "mcp: { confirmationKey: workApiConfig.MCP_KEY, callGate: workApiDeps.agents.mcpGate }"; !slices.Contains(got, want) {
		t.Errorf("RouterOptions = %v, want %s", got, want)
	}
	api.MCPConfirmationKey = ""
	if got, want := api.RouterOptions(), "mcp: { callGate: workApiDeps.agents.mcpGate }"; !slices.Contains(got, want) {
		t.Errorf("RouterOptions = %v, want %s", got, want)
	}
	api.MCP = false
	for _, option := range api.RouterOptions() {
		if option != "" && option[:4] == "mcp:" {
			t.Errorf("a server that serves no MCP endpoint gates %s", option)
		}
	}
}
