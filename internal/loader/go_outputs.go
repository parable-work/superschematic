package loader

import (
	"strings"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// goServer reads whether the build serves an API in Go
// (verify.Input.GoServer, D180): an API whose server is enabled in Go, the
// default language, with its Go types on, which a Go server imports
// (registry.ParseOutputs refuses one without). The generators own the
// outputs block and its checks, so a block they would refuse is read as
// far as it names these.
func goServer(cfg *schemaconfig.SchemaConfig) bool {
	if cfg == nil || cfg.Kind != ir.SchemaKindAPI {
		return false
	}
	api, _ := cfg.Outputs["api"].(map[string]any)
	enabled, _ := api["enabled"].(bool)
	language, _ := api["language"].(string)
	types, _ := cfg.Outputs["types"].(map[string]any)
	goTypes, _ := types[registry.LangGo].(map[string]any)
	withTypes, _ := goTypes["enabled"].(bool)
	return enabled && withTypes && (language == "" || strings.EqualFold(language, registry.APILanguageGo))
}
