package loader

import (
	"strings"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// goOutputs reads whether the build generates Go for a service
// (verify.Input.GoORM and GoServer, D180): a DB service whose config
// enables its Go types, which the Go ORM is generated from, and an API
// whose server is enabled in Go, the default language. The generators own
// the outputs block and its checks (registry.ParseOutputs), so a block
// they would refuse is read as far as it names these.
func goOutputs(cfg *schemaconfig.SchemaConfig) (orm, server bool) {
	if cfg == nil {
		return false, false
	}
	switch cfg.Kind {
	case ir.SchemaKindDB:
		types, _ := cfg.Outputs["types"].(map[string]any)
		goTypes, _ := types[registry.LangGo].(map[string]any)
		enabled, _ := goTypes["enabled"].(bool)
		return enabled, false
	case ir.SchemaKindAPI:
		api, _ := cfg.Outputs["api"].(map[string]any)
		enabled, _ := api["enabled"].(bool)
		language, _ := api["language"].(string)
		types, _ := cfg.Outputs["types"].(map[string]any)
		goTypes, _ := types[registry.LangGo].(map[string]any)
		withTypes, _ := goTypes["enabled"].(bool)
		// A Go server imports the Go types: an API without them is no Go
		// server, whatever its language says (registry.ParseOutputs).
		return false, enabled && withTypes && (language == "" || strings.EqualFold(language, registry.APILanguageGo))
	}
	return false, false
}
