package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry/registrytest"
)

// referenceProbe writes a General service under servicesRoot whose one
// schema file has the given decorators, importing what imports names.
func referenceProbe(t *testing.T, servicesRoot, name, imports, decorators string) {
	t.Helper()
	dir := filepath.Join(servicesRoot, name)
	for rel, content := range map[string]string{
		"package.json":  `{ "name": "@schemas/` + name + `", "private": true }`,
		"tsconfig.json": `{ "extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"] }`,
		"schema.config.ts": `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
export default defineConfig({ name: "` + name + `", kind: SchemaKind.General, outputs: {} });
`,
		"src/index.ts": "export * from \"./service.generated\";\n",
		"src/probe.schema.ts": `import { admits, meta } from "@acme/schematic";
` + imports + `

` + decorators + `
export abstract class Probe {
  name: string;
}
`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
}

// appendTo appends a line to a file, as an edit to a service's sources.
func appendTo(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, []byte("\n// edited\n")...), 0o644))
}

// TestBuildAllRebuildsWhatReferencesAChangedService: a handle in a
// decorator argument is a cache edge (D41). probe-ref's @meta references
// fixture-db, so editing fixture-db rebuilds it; probe-identity's @admits
// only names fixture-db, so it stays up to date. probe-a and probe-b name
// each other through @admits: no cycle, and an edit to one leaves the other
// alone. No sentinel of a probe is on disk at first: build-all's sweep
// writes them before the probes' programs import them.
func TestBuildAllRebuildsWhatReferencesAChangedService(t *testing.T) {
	servicesRoot := prepareTSServicesRoot(t, "fixture-db")
	schematic, err := filepath.Abs("../internal/registry/registrytest/testdata/packages/schematic/src/index.ts")
	require.NoError(t, err)
	basePath := filepath.Join(filepath.Dir(servicesRoot), "tsconfig.base.json")
	data, err := os.ReadFile(basePath)
	require.NoError(t, err)
	var base map[string]any
	require.NoError(t, json.Unmarshal(data, &base))
	base["compilerOptions"].(map[string]any)["paths"].(map[string]any)["@acme/schematic"] = []string{filepath.ToSlash(schematic)}
	data, err = json.Marshal(base)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(basePath, data, 0o644))

	referenceProbe(t, servicesRoot, "probe-ref", `import { FixtureDb } from "@schemas/fixture-db";`, `@meta({ deploy: [FixtureDb] })`)
	referenceProbe(t, servicesRoot, "probe-identity", `import { FixtureDb } from "@schemas/fixture-db";`, `@admits({ from: [FixtureDb] })`)
	referenceProbe(t, servicesRoot, "probe-a", `import { ProbeB } from "@schemas/probe-b";`, `@admits({ from: [ProbeB] })`)
	referenceProbe(t, servicesRoot, "probe-b", `import { ProbeA } from "@schemas/probe-a";`, `@admits({ from: [ProbeA] })`)

	outDir := t.TempDir()
	cacheRoot := t.TempDir()
	run := func() string {
		t.Helper()
		out := new(bytes.Buffer)
		root := New(Config{}, registrytest.Acme{}, registrytest.Admits{})
		root.SetOut(out)
		root.SetErr(new(bytes.Buffer))
		root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})
		require.NoError(t, root.Execute(), out.String())
		return out.String()
	}

	first := run()
	for _, name := range []string{"fixture-db", "probe-ref", "probe-identity", "probe-a", "probe-b"} {
		assert.Contains(t, first, "OK: "+name+" (built", name)
	}
	assert.Contains(t, first, "sentinel written for probe-a")

	second := run()
	for _, name := range []string{"fixture-db", "probe-ref", "probe-identity", "probe-a", "probe-b"} {
		assert.Contains(t, second, "OK: "+name+" (up to date", name)
	}

	appendTo(t, filepath.Join(servicesRoot, "fixture-db", "src", "tenant.schema.ts"))
	appendTo(t, filepath.Join(servicesRoot, "probe-a", "src", "probe.schema.ts"))
	third := run()
	assert.Contains(t, third, "OK: fixture-db (built")
	assert.Contains(t, third, "OK: probe-ref (built", "a service that references fixture-db was not rebuilt")
	assert.Contains(t, third, "OK: probe-identity (up to date", "an identity made probe-identity depend on fixture-db")
	assert.Contains(t, third, "OK: probe-a (built")
	assert.Contains(t, third, "OK: probe-b (up to date", "an identity made probe-b depend on probe-a")
}
