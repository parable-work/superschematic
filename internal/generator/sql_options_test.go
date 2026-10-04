package generator

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
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
		Outputs: map[string]any{"sql": map[string]any{"viewOwner": "app_view_owner"}},
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
		MetadataKeyPrefix:   "acme.",
		HistoryActorSetting: "acme.actor_id",
	}, SQLOptions(cfg, outputs, deps, names))

	// A zero naming is the defaults, as a build's is.
	opts := SQLOptions(&schemaconfig.SchemaConfig{Name: "shop-db"}, nil, nil, naming.Naming{})
	assert.Equal(t, sqlgen.DefaultMetadataKeyPrefix, opts.MetadataKeyPrefix)
	assert.Equal(t, sqlgen.DefaultHistoryActorSetting, opts.HistoryActorSetting)
	assert.Empty(t, opts.ViewOwner)
}
