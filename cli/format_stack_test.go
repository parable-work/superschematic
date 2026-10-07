package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// formatStackTestdata holds stackgen's shop-stack, authored in
// TypeScript, and the services whose sentinels it imports.
const formatStackTestdata = "../internal/generator/stackgen/testdata"

// formatEnvironmentOrders reads each environment's order from a stack's
// JSON form.
func formatEnvironmentOrders(t *testing.T, doc string) map[string]int {
	t.Helper()
	var parsed struct {
		Types map[string]struct {
			Environment *struct {
				Order int `json:"order"`
			} `json:"environment"`
		} `json:"types"`
	}
	require.NoError(t, json.Unmarshal([]byte(doc), &parsed))
	orders := map[string]int{}
	for name, td := range parsed.Types {
		if td.Environment != nil {
			orders[name] = td.Environment.Order
		}
	}
	return orders
}

// formatStackService lays out a temp copy of shop-stack whose schema file
// is ts, and returns the file's path. Its tsconfig extends stackgen's
// fixtures', which resolves @superschematic/stack and the shop services'
// sentinels, and the fake target's authoring types come along.
func formatStackService(t *testing.T, ts string) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join(formatStackTestdata, "tsconfig.base.json"))
	require.NoError(t, err)
	tsconfig, err := json.Marshal(map[string]any{"extends": filepath.ToSlash(base), "include": []string{"src/**/*.ts"}})
	require.NoError(t, err)
	fakeTarget, err := os.ReadFile(filepath.Join(formatStackTestdata, "services", "shop-stack", "src", "fake-target.ts"))
	require.NoError(t, err)
	service := t.TempDir()
	for name, body := range map[string]string{
		"schema.config.json":  `{"name": "shop-stack", "kind": "Stack", "outputs": {}}`,
		"tsconfig.json":       string(tsconfig),
		"src/fake-target.ts":  string(fakeTarget),
		"src/stack.schema.ts": ts,
	} {
		path := filepath.Join(service, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	return filepath.Join(service, "src", "stack.schema.ts")
}

// TestFormatCommand_RoundTripsAStacksEnvironmentOrder: the TypeScript form
// numbers the environments in source order and the JSON form writes the
// order, which YAML carries as data. Written back to TypeScript, the
// environment classes come in their order, which is their place, so the
// file has no order property, and read back it gives the same JSON.
func TestFormatCommand_RoundTripsAStacksEnvironmentOrder(t *testing.T) {
	asJSON, err := runFormatCommand(t, "--to=json", "--stdout", filepath.Join(formatStackTestdata, "services", "shop-stack", "src", "stack.schema.ts"))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"Staging": 1, "Production": 2, "Preview": 3}, formatEnvironmentOrders(t, asJSON))

	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "stack.schema.json")
	require.NoError(t, os.WriteFile(jsonPath, []byte(asJSON), 0o644))
	asYAML, err := runFormatCommand(t, "--to=yaml", "--stdout", jsonPath)
	require.NoError(t, err)
	assert.Contains(t, asYAML, "order: 3")
	yamlPath := filepath.Join(t.TempDir(), "stack.schema.yaml")
	require.NoError(t, os.WriteFile(yamlPath, []byte(asYAML), 0o644))
	fromYAML, err := runFormatCommand(t, "--to=json", "--stdout", yamlPath)
	require.NoError(t, err)
	assert.Equal(t, asJSON, fromYAML)

	ts, err := runFormatCommand(t, "--to=ts", "--stdout", jsonPath)
	require.NoError(t, err)
	assert.NotContains(t, ts, "order:")
	assert.Contains(t, ts, `import { environment, server, stack } from "@superschematic/stack";`)
	staging, production, preview := strings.Index(ts, "class Staging"), strings.Index(ts, "class Production"), strings.Index(ts, "class Preview")
	assert.True(t, staging < production && production < preview, "the environment classes are written out of order:\n%s", ts)
	back, err := runFormatCommand(t, "--to=json", "--stdout", formatStackService(t, ts))
	require.NoError(t, err)
	assert.Equal(t, asJSON, back)
}

// TestFormatCommand_WritesTheEnvironmentsInTheirOrder: an order a data form
// writes, here Production's before Staging's, is the order the TypeScript
// classes are written in, which the reader numbers from 1. An environment
// ordered before the class it extends has no TypeScript form.
func TestFormatCommand_WritesTheEnvironmentsInTheirOrder(t *testing.T) {
	asJSON, err := runFormatCommand(t, "--to=json", "--stdout", filepath.Join(formatStackTestdata, "services", "shop-stack", "src", "stack.schema.ts"))
	require.NoError(t, err)
	reorder := func(orders map[string]int) string {
		var doc map[string]any
		require.NoError(t, json.Unmarshal([]byte(asJSON), &doc))
		types := doc["types"].(map[string]any)
		for name, order := range orders {
			types[name].(map[string]any)["environment"].(map[string]any)["order"] = order
		}
		out, err := json.Marshal(doc)
		require.NoError(t, err)
		path := filepath.Join(t.TempDir(), "stack.schema.json")
		require.NoError(t, os.WriteFile(path, out, 0o644))
		return path
	}

	ts, err := runFormatCommand(t, "--to=ts", "--stdout", reorder(map[string]int{"Production": 4, "Staging": 7, "Preview": 9}))
	require.NoError(t, err)
	production, staging, preview := strings.Index(ts, "class Production"), strings.Index(ts, "class Staging"), strings.Index(ts, "class Preview")
	assert.True(t, production < staging && staging < preview, "the environment classes are written out of order:\n%s", ts)
	back, err := runFormatCommand(t, "--to=json", "--stdout", formatStackService(t, ts))
	require.NoError(t, err)
	assert.Equal(t, map[string]int{"Production": 1, "Staging": 2, "Preview": 3}, formatEnvironmentOrders(t, back))

	_, err = runFormatCommand(t, "--to=ts", "--stdout", reorder(map[string]int{"Preview": 1, "Staging": 2, "Production": 3}))
	require.ErrorContains(t, err, "the @environment classes in their order, Preview, Staging, Production, have no TypeScript form: a class is declared after the class it extends, so the reader would number them Staging, Preview, Production")
}
