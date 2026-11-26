package loader

import (
	"sort"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// typesLanguages reads the languages whose types a DB service's config
// enables (outputs.types), sorted, which the verification pass holds a
// feature to where only some languages' repositories read it on a dialect
// (verify.Input.TypesLanguages, D182). The generators own the outputs
// block and its checks (registry.ParseOutputs), so a block they would
// refuse is read as far as it names languages, and a config of another
// kind has none.
func typesLanguages(cfg *schemaconfig.SchemaConfig) []string {
	if cfg == nil || cfg.Kind != ir.SchemaKindDB {
		return nil
	}
	types, _ := cfg.Outputs["types"].(map[string]any)
	languages := []string{}
	for lang, entry := range types {
		if output, ok := entry.(map[string]any); ok && output["enabled"] == true {
			languages = append(languages, lang)
		}
	}
	sort.Strings(languages)
	return languages
}
