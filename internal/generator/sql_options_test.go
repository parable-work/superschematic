package generator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSQLOptions: the options a build passes sqlgen, which migrate plan
// passes too, come from the config, the outputs block and the naming.
func TestSQLOptions(t *testing.T) {
	cfg := &schemaconfig.SchemaConfig{
		Name:    "shop-db",
		Kind:    ir.SchemaKindDB,
		Outputs: map[string]any{"sql": map[string]any{"viewOwner": "app_view_owner", "sourceSchema": "ledger"}},
	}
	names := naming.Default()
	names.MetadataKeyPrefix = "acme."
	names.HistoryActorSetting = "acme.actor_id"
	outputs, err := registry.ParseOutputs(cfg.Outputs, CoreRegistry(names))
	require.NoError(t, err)
	deps := map[string]*ir.Schema{"shop-common": ir.NewSchema("shop-common", ir.SchemaKindGeneral)}

	assert.Equal(t, sqlgen.Options{
		SchemaName:          "shop-db",
		Dependencies:        deps,
		ViewOwner:           "app_view_owner",
		SourceSchema:        "ledger",
		MetadataKeyPrefix:   "acme.",
		HistoryActorSetting: "acme.actor_id",
		StateGuardTerminal:  naming.DefaultGuardTerminal,
		StateGuardRefused:   naming.DefaultGuardRefused,
	}, SQLOptions(cfg, outputs, deps, names))

	// The naming's [sql] table reaches the DDL policy and the guard
	// messages.
	names.SQL = naming.SQLConfig{
		ColumnChecks: naming.SQLOff, ColumnDefaults: naming.SQLOff, NowDefaults: naming.SQLNowRequired,
		StateGuardTerminal: "{column} stays at % and not %", StateGuardRefused: "{column} goes from % only, not to %",
	}
	opts := SQLOptions(cfg, outputs, deps, names)
	assert.Equal(t, sqlutil.DDLPolicy{NoColumnChecks: true, NoColumnDefaults: true, NowOnEveryRequiredTime: true}, opts.DDL)
	assert.Equal(t, "{column} stays at % and not %", opts.StateGuardTerminal)
	assert.Equal(t, "{column} goes from % only, not to %", opts.StateGuardRefused)

	// A zero naming is the defaults, as a build's is.
	opts = SQLOptions(&schemaconfig.SchemaConfig{Name: "shop-db"}, nil, nil, naming.Naming{})
	assert.Equal(t, sqlgen.DefaultMetadataKeyPrefix, opts.MetadataKeyPrefix)
	assert.Equal(t, sqlgen.DefaultHistoryActorSetting, opts.HistoryActorSetting)
	assert.Equal(t, sqlutil.DDLPolicy{}, opts.DDL)
	assert.Empty(t, opts.ViewOwner)
	assert.Empty(t, opts.SourceSchema)
}
