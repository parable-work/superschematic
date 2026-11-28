package registry

import (
	"strings"
	"testing"
)

// outputs.api.mcp (D81) serves the API's tools over MCP from the
// TypeScript router, the Rust one (D116) and the Go one (#660): each
// reads it, and its key.
func TestParseOutputsServesMCPOnEveryAPILanguage(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	for language, types := range map[string]string{"": "go", "GO": "go", "TYPESCRIPT": "typescript", "RUST": "rust"} {
		api := map[string]any{"enabled": true, "mcp": true, "mcpConfirmationKey": "McpKeys.MCP_CONFIRMATION_KEY"}
		if language != "" {
			api["language"] = language
		}
		outputs, err := ParseOutputs(map[string]any{"api": api, "types": map[string]any{types: enabled}}, coreOutputRegistry(t))
		if err != nil {
			t.Fatalf("mcp on a %q API: %v", language, err)
		}
		if !outputs.API.MCP || outputs.MCPConfirmationKey() != "McpKeys.MCP_CONFIRMATION_KEY" {
			t.Errorf("outputs.api.mcp and its key were not read on a %q API: %+v", language, outputs.API)
		}
	}
	// A disabled API serves nothing, so its mcp is not checked.
	if _, err := ParseOutputs(map[string]any{"api": map[string]any{"enabled": false, "mcp": true}}, coreOutputRegistry(t)); err != nil {
		t.Errorf("a disabled API with mcp: %v", err)
	}
}

// outputs.api.mcpConfirmationKey (D93) names the secret the MCP endpoint
// signs its confirmations with as a stack names a secret, its declaring
// type and its field, and only beside mcp.
func TestParseOutputsReadsTheMCPConfirmationKey(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	parse := func(api map[string]any) (*Outputs, error) {
		return ParseOutputs(map[string]any{"api": api, "types": map[string]any{"typescript": enabled}}, coreOutputRegistry(t))
	}
	outputs, err := parse(map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true, "mcpConfirmationKey": "McpKeys.MCP_CONFIRMATION_KEY"})
	if err != nil {
		t.Fatal(err)
	}
	if got := outputs.MCPConfirmationKey(); got != "McpKeys.MCP_CONFIRMATION_KEY" {
		t.Errorf("MCPConfirmationKey() = %q", got)
	}
	if got := (&Outputs{}).MCPConfirmationKey(); got != "" {
		t.Errorf("no API's key = %q", got)
	}
	for key, want := range map[string]string{
		"MCP_CONFIRMATION_KEY":    `outputs.api.mcpConfirmationKey "MCP_CONFIRMATION_KEY" must name an @envVars Secret<string> by its declaring type and field, such as "McpKeys.MCP_CONFIRMATION_KEY" (D93)`,
		"Mcp-Keys.KEY":            `outputs.api.mcpConfirmationKey "Mcp-Keys.KEY" must name an @envVars Secret<string>`,
		"McpKeys.KEY.MORE":        `outputs.api.mcpConfirmationKey "McpKeys.KEY.MORE" must name an @envVars Secret<string>`,
		"McpKeys.MCP_CONFIRM_KEY": "",
	} {
		_, err := parse(map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true, "mcpConfirmationKey": key})
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("key %q: error = %v, want %q", key, err, want)
		}
	}
	_, err = parse(map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcpConfirmationKey": "McpKeys.MCP_CONFIRMATION_KEY"})
	if want := "outputs.api.mcpConfirmationKey is set and outputs.api.mcp is off: only the MCP endpoint signs confirmations; turn mcp on, or remove the key (D93)"; err == nil || err.Error() != want {
		t.Errorf("a key without mcp: error = %v, want %q", err, want)
	}
}
