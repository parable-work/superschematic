package loader

import (
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// sqlDialects reads the dialects a DB service's config lists
// (outputs.sql.dialects), which the verification pass holds a table's
// fences to (D119, verify.Input.Dialects): postgres alone when the config
// lists none, as registry.Outputs.SQLDialects has it. The generators own
// the outputs block and its checks (registry.ParseOutputs), so a list
// they would refuse is read as far as it names dialects, and a config of
// another kind has none.
// searchFoldsDiacritics is a DB service's outputs.sql.search.foldDiacritics
// (#740): whether its search index folds diacritics.
func searchFoldsDiacritics(cfg *schemaconfig.SchemaConfig) bool {
	return searchOption(cfg, "foldDiacritics")
}

// searchEnvironment is a DB service's outputs.sql.search.environment
// (#740): whether its @searchable tables are searched through their
// environment's index, into which its worker copies their index rows.
func searchEnvironment(cfg *schemaconfig.SchemaConfig) bool {
	return searchOption(cfg, "environment")
}

// searchEnvironmentIndex is a DB service's
// outputs.sql.search.environmentIndex (#740): whether it holds its
// environment's search index.
func searchEnvironmentIndex(cfg *schemaconfig.SchemaConfig) bool {
	return searchOption(cfg, "environmentIndex")
}

// searchOption is the boolean outputs.sql.search.<name> of a DB service.
func searchOption(cfg *schemaconfig.SchemaConfig, name string) bool {
	if cfg == nil || cfg.Kind != ir.SchemaKindDB {
		return false
	}
	sql, _ := cfg.Outputs["sql"].(map[string]any)
	search, _ := sql["search"].(map[string]any)
	on, _ := search[name].(bool)
	return on
}

func sqlDialects(cfg *schemaconfig.SchemaConfig) []string {
	if cfg == nil || cfg.Kind != ir.SchemaKindDB {
		return nil
	}
	sql, _ := cfg.Outputs["sql"].(map[string]any)
	list, listed := sql["dialects"].([]any)
	if !listed {
		return []string{registry.SQLDialectPostgres}
	}
	dialects := make([]string, 0, len(list))
	for _, entry := range list {
		if dialect, ok := entry.(string); ok {
			dialects = append(dialects, dialect)
		}
	}
	return dialects
}
