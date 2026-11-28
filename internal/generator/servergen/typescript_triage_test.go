package servergen

import (
	"slices"
	"testing"
)

// TestTheRouterRunsTriageThroughTheInboundPath: an API that serves the
// inbound path's triage (#746) hands the router Deps.inbound, which the
// runtime builds (#702), and one that serves none hands it nothing.
func TestTheRouterRunsTriageThroughTheInboundPath(t *testing.T) {
	api := &TypeScriptAPI{Deps: "deps", Triage: true}
	if got := api.RouterOptions(); !slices.Contains(got, "inbound: deps.inbound") {
		t.Errorf("RouterOptions of an API with triage = %v, want inbound: deps.inbound", got)
	}
	api.Triage = false
	if got := api.RouterOptions(); slices.ContainsFunc(got, func(option string) bool { return option == "inbound: deps.inbound" }) {
		t.Errorf("RouterOptions of an API without triage = %v", got)
	}
}
