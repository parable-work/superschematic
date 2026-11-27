package loader

import (
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// typeScriptServer reads whether the build serves an API in TypeScript
// (verify.Input.TypeScriptServer): an API whose server is enabled with the
// language TYPESCRIPT. The generators own the outputs block and its checks,
// so a block they would refuse is read as far as it names these.
func typeScriptServer(cfg *schemaconfig.SchemaConfig) bool {
	if cfg == nil || cfg.Kind != ir.SchemaKindAPI {
		return false
	}
	api, _ := cfg.Outputs["api"].(map[string]any)
	enabled, _ := api["enabled"].(bool)
	language, _ := api["language"].(string)
	return enabled && strings.EqualFold(language, registry.APILanguageTypeScript)
}

// databaseDialects reads the dialects the config of a TypeScript API's
// database lists (verify.Input.DatabaseDialects), from the DB service
// beside the API's, where the build reads a database's config too (its
// LoadDependencyConfig): the first is the database the server opens (D64).
// It reads nothing for an API that is no TypeScript server or serves no
// @tableOperation, whose database the loader did not read, or whose
// database has no config beside it, which leaves the rules over the
// dialects unchecked.
func databaseDialects(servicePath string, schema, database *ir.Schema, typeScript bool, reg *registry.Registry) []string {
	if !typeScript || database == nil || !schema.HasTableOperations() {
		return nil
	}
	db, _, ok := schema.Database()
	if !ok {
		return nil
	}
	dir := filepath.Join(servicePath, "..", db)
	cfg, err := schemaconfig.ReadFile(dir, reg)
	if err != nil {
		if cfg, err = tsreader.ReadServiceConfig(dir, reg); err != nil {
			return nil
		}
	}
	return sqlDialects(cfg)
}
