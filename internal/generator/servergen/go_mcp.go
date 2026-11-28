package servergen

import "github.com/parable-work/superschematic/internal/generator/apigen"

// The MCP endpoint a Go server's API serves (outputs.api.mcp, D81, #660;
// apigen's mcp.go). The entrypoint builds the API's router with the
// endpoint's options: the key its config holds, which a tool that asks
// signs its confirmations with (D93); the dispatcher's MCPCallGate, which
// holds an agent run's call to the run's autonomy by the tool's @autonomy
// (D158, D183, #621); and the calendar's and search's tools the API
// package's MCPTools serves beside its routes' (D152, D155, #616).

// planMCP gives api, a server's, the endpoint output's API serves. A job
// or a worker serves no request.
func planMCP(api *API, o *apigen.APIOutput, s *Server) {
	if o.MCP == nil || s.Background() {
		return
	}
	api.MCP = true
	if o.MCP.ConfirmationKey != "" && api.Config {
		api.MCPConfirmationKey = api.Var + "Config." + o.MCP.ConfirmationKey
	}
}

// MCPGate reports whether the endpoint holds a call to an agent run's
// autonomy: Deps holds the dispatcher.
func (a *API) MCPGate() bool { return a.MCP && a.Wiring != nil && a.Wiring.Agents != "" }

// MCPTools reports whether the endpoint serves the calendar's and
// search's tools beside its routes': the API's database has a calendar
// source or a @searchable table, which the wiring reads.
func (a *API) MCPTools() bool { return a.MCP && a.Wiring.CrossTables() }

// MCPOptions are the members of the API's Config.MCP the entrypoint sets.
func (a *API) MCPOptions() []string {
	var out []string
	if a.MCPConfirmationKey != "" {
		out = append(out, "ConfirmationKey: []byte("+a.MCPConfirmationKey+")")
	}
	if a.MCPGate() {
		out = append(out, "CallGate: "+a.Var+"Deps.Agents.MCPCallGate()")
	}
	if a.MCPTools() {
		out = append(out, "Tools: "+a.Package+".MCPTools("+a.Database.Var+".CrossTables(), "+a.WiringVar()+")")
	}
	return out
}
