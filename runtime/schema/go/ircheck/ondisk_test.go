package ircheck_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectorDataFilePartial is a lightweight struct for extracting embedded
// ParableSchema JSON from connector data files without importing generated types.
type connectorDataFilePartial struct {
	Slug                    string                          `json:"slug"`
	IngestionConfigSchema   json.RawMessage                 `json:"ingestionConfigSchema"`
	SupportedAuthStrategies []authStrategyDefinitionPartial `json:"supportedAuthStrategies"`
	Taps                    []tapDefinitionPartial          `json:"taps"`
}

type authStrategyDefinitionPartial struct {
	Type         string          `json:"type"`
	ConfigSchema json.RawMessage `json:"configSchema"`
}

type tapDefinitionPartial struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

func TestCheck_OnDiskConnectorJSONDataFiles(t *testing.T) {
	dataDir := findConnectorDataDir(t)

	entries, err := os.ReadDir(dataDir)
	require.NoError(t, err)

	var connectorFiles []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Name() == "vendor-manifest.json" || e.Name() == "manifest-schema.json" {
			continue
		}
		connectorFiles = append(connectorFiles, filepath.Join(dataDir, e.Name()))
	}
	require.NotEmpty(t, connectorFiles, "expected at least one connector JSON data file in %s", dataDir)

	var totalSchemasParsed int

	for _, path := range connectorFiles {
		connName := strings.TrimSuffix(filepath.Base(path), ".json")
		t.Run(connName, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			require.NoError(t, err)

			var data connectorDataFilePartial
			require.NoError(t, json.Unmarshal(raw, &data), "failed to unmarshal connector data file")

			if len(data.IngestionConfigSchema) > 0 && string(data.IngestionConfigSchema) != "null" {
				schema, err := readEmbeddedSchema(data.IngestionConfigSchema)
				require.NoError(t, err, "JSON reader rejected ingestionConfigSchema")

				valid, errs := schema.Validate()
				require.True(t, valid, "ingestionConfigSchema is invalid: %v", errs)

				totalSchemasParsed++

				t.Logf("validated ingestionConfigSchema for %s", connName)
			}

			for i, strategy := range data.SupportedAuthStrategies {
				if len(strategy.ConfigSchema) == 0 || string(strategy.ConfigSchema) == "null" {
					continue
				}
				strategyName := strategy.Type
				if strategyName == "" {
					strategyName = "unknown"
				}
				t.Run("auth-"+strategyName, func(t *testing.T) {
					schema, err := readEmbeddedSchema(strategy.ConfigSchema)
					require.NoError(t, err, "JSON reader rejected auth strategy %d configSchema", i)

					valid, errs := schema.Validate()
					require.True(t, valid, "auth configSchema is invalid: %v", errs)

					totalSchemasParsed++

					t.Logf("validated auth configSchema (%s) for %s", strategyName, connName)
				})
			}

			for i, tap := range data.Taps {
				if len(tap.Schema) == 0 || string(tap.Schema) == "null" || string(tap.Schema) == "{}" {
					continue
				}
				tapName := tap.Name
				if tapName == "" {
					tapName = "unknown"
				}
				t.Run("tap-"+tapName, func(t *testing.T) {
					schema, err := readEmbeddedSchema(tap.Schema)
					require.NoError(t, err, "JSON reader rejected tap %d schema", i)

					valid, errs := schema.Validate()
					require.True(t, valid, "tap schema is invalid: %v", errs)

					totalSchemasParsed++

					t.Logf("validated tap schema (%s) for %s", tapName, connName)
				})
			}
		})
	}

	assert.Greater(t, totalSchemasParsed, 0, "expected at least one embedded schema parsed across all connector data files")
}

func readEmbeddedSchema(raw json.RawMessage) (scalars.ParableSchema, error) {
	var schema scalars.ParableSchema
	return schema, json.Unmarshal(raw, &schema)
}

func findConnectorDataDir(t *testing.T) string {
	t.Helper()

	if envDir := os.Getenv("CONNECTOR_DATA_DIR"); envDir != "" {
		info, err := os.Stat(envDir)
		require.NoError(t, err, "CONNECTOR_DATA_DIR %s not found", envDir)
		require.True(t, info.IsDir(), "CONNECTOR_DATA_DIR %s is not a directory", envDir)
		return envDir
	}

	repoRoot := findRepoRoot(t)
	dir := filepath.Join(repoRoot, "platform-schemas", "data")

	info, err := os.Stat(dir)
	require.NoError(t, err, "connector data directory not found at %s", dir)
	require.True(t, info.IsDir(), "%s is not a directory", dir)

	return dir
}

// findRepoRoot walks up from the test file's directory looking for a .git marker.
func findRepoRoot(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller failed")

	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (.git marker) from", thisFile)
		}
		dir = parent
	}
}
