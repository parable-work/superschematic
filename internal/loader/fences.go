package loader

import (
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// sqlDialects reads the dialects a DB service's config lists
// (outputs.sql.dialects), which the verification pass holds a table's
// fences to (D118, verify.Input.Dialects): postgres alone when the config
// lists none, as registry.Outputs.SQLDialects has it. The generators own
// the outputs block and its checks (registry.ParseOutputs), so a list
// they would refuse is read as far as it names dialects, and a config of
// another kind has none.
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
