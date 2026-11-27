package loader

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
)

// refuseEngineOutputs refuses a config whose outputs block sets engine
// (registry.ErrEngineOutputs, D103) as soon as the config is read, in every
// form, so a DB service that still configures the engine its engine types
// ran on is told the table form before anything else of it loads.
func refuseEngineOutputs(cfg *schemaconfig.SchemaConfig) error {
	if cfg == nil {
		return nil
	}
	if _, ok := cfg.Outputs[registry.EngineOutputKey]; ok {
		return fmt.Errorf("schema config for %s: %w", cfg.Name, registry.ErrEngineOutputs)
	}
	return nil
}
