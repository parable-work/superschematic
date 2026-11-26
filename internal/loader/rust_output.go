package loader

import (
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// rustOutput names the output of a service's config that builds Rust
// (verify.Input.RustOutput, D181): a DB service's Rust types
// (outputs.types.rust enabled), an API's Rust server (outputs.api.language
// rust); empty for a config that builds neither. The generators own the
// outputs block and its checks (registry.ParseOutputs), so this reads it
// as far as it names Rust.
func rustOutput(cfg *schemaconfig.SchemaConfig) string {
	if cfg == nil {
		return ""
	}
	switch cfg.Kind {
	case ir.SchemaKindDB:
		types, _ := cfg.Outputs["types"].(map[string]any)
		rust, _ := types["rust"].(map[string]any)
		if enabled, _ := rust["enabled"].(bool); enabled {
			return "outputs.types.rust"
		}
	case ir.SchemaKindAPI:
		api, _ := cfg.Outputs["api"].(map[string]any)
		if language, _ := api["language"].(string); language == "rust" {
			return "outputs.api.language: rust"
		}
	}
	return ""
}
