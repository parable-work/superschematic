package generator

import (
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// SQLOptions returns the sqlgen options a build passes for the DB service
// cfg configures: its name, its declared dependencies (deps, keyed by
// service name), outputs.sql.viewOwner, and the naming's metadata key
// prefix and history actor setting. The clock is left to the caller.
// `migrate plan` resolves models with the same options, so a plan and
// create.sql agree.
func SQLOptions(cfg *schemaconfig.SchemaConfig, outputs *registry.Outputs, deps map[string]*ir.Schema, n naming.Naming) sqlgen.Options {
	n = n.OrDefault()
	return sqlgen.Options{
		SchemaName:          cfg.Name,
		Dependencies:        deps,
		ViewOwner:           outputs.SQLViewOwner(),
		MetadataKeyPrefix:   n.MetadataKeyPrefix,
		HistoryActorSetting: n.HistoryActorSetting,
	}
}
