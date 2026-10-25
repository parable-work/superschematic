package registry

import "testing"

// outputs.api.mcp (D81) serves the API's tools over MCP from the
// TypeScript router alone: a Go or Rust API with it fails to load, saying
// what to change and that support follows, and a TypeScript one reads it.
func TestParseOutputsServesMCPOnATypeScriptAPIAlone(t *testing.T) {
	enabled := map[string]any{"enabled": true}
	for _, tc := range []struct {
		language string
		types    string
	}{{"", "go"}, {"GO", "go"}, {"RUST", "rust"}} {
		api := map[string]any{"enabled": true, "mcp": true}
		if tc.language != "" {
			api["language"] = tc.language
		}
		_, err := ParseOutputs(map[string]any{"api": api, "types": map[string]any{tc.types: enabled}}, coreOutputRegistry(t))
		language := tc.language
		if language == "" {
			language = APILanguageGo
		}
		want := "outputs.api.mcp is on and the API's language is " + language + ": a TypeScript API serves the MCP endpoint; Go and Rust follow; set outputs.api.language to TYPESCRIPT, or remove mcp (D81)"
		if err == nil || err.Error() != want {
			t.Errorf("mcp on a %s API: error = %v, want %q", language, err, want)
		}
	}

	outputs, err := ParseOutputs(map[string]any{
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true},
		"types": map[string]any{"typescript": enabled},
	}, coreOutputRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if !outputs.API.MCP {
		t.Error("outputs.api.mcp was not read")
	}
	// A disabled API serves nothing, so its mcp is not checked.
	if _, err := ParseOutputs(map[string]any{"api": map[string]any{"enabled": false, "mcp": true}}, coreOutputRegistry(t)); err != nil {
		t.Errorf("a disabled API with mcp: %v", err)
	}
}
