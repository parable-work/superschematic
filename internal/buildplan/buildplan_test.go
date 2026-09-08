package buildplan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
}

func TestDiscoverOrdersDependenciesAndOutputs(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	outputRoot := filepath.Join(root, "dist")

	writeFile(t, filepath.Join(servicesRoot, "leaf", "schema.config.yaml"), `name: leaf
kind: API
dependencies:
  - { name: base, kind: DB }
outputs:
  api: { enabled: true }
  sdk:
    typescript: { enabled: true }
`)
	writeFile(t, filepath.Join(servicesRoot, "base", "schema.config.json"), `{
		"name": "base",
		"kind": "DB",
		"outputs": {"types": {"go": {"enabled": true}}}
	}`)
	writeFile(t, filepath.Join(servicesRoot, "_ignored", "schema.config.json"), `{
		"name": "ignored",
		"kind": "General",
		"outputs": {}
	}`)

	services, err := Discover(servicesRoot, outputRoot)
	require.NoError(t, err)
	require.Len(t, services, 2)
	assert.Equal(t, "base", services[0].Name)
	assert.Equal(t, "leaf", services[1].Name)
	// Output dirs follow the kind's registry pipeline: sql, orm, types for
	// DB; types, api, sdks for API.
	assert.Equal(t, []string{
		filepath.Join(outputRoot, "sql", "base"),
		filepath.Join(outputRoot, "orm", "base"),
		filepath.Join(outputRoot, "types", "go", "base"),
	}, services[0].OutputDirs)
	assert.Equal(t, []string{
		filepath.Join(outputRoot, "api", "leaf"),
		filepath.Join(outputRoot, "sdk", "typescript", "leaf"),
	}, services[1].OutputDirs)
}

func TestTopologicalSortRejectsCycles(t *testing.T) {
	services := []Service{
		{
			Name: "a",
			Config: &schemaconfig.SchemaConfig{
				Name:         "a",
				Kind:         ir.SchemaKindGeneral,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "b", Kind: ir.SchemaKindGeneral}},
			},
		},
		{
			Name: "b",
			Config: &schemaconfig.SchemaConfig{
				Name:         "b",
				Kind:         ir.SchemaKindGeneral,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "a", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	_, err := TopologicalSort(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestGroupIntoPhasesHonorsAlreadyBuilt(t *testing.T) {
	services := []Service{
		{Name: "base", Config: &schemaconfig.SchemaConfig{Name: "base"}},
		{
			Name: "leaf",
			Config: &schemaconfig.SchemaConfig{
				Name:         "leaf",
				Dependencies: []schemaconfig.ServiceDependency{{Name: "base", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	phases, err := GroupIntoPhases(services[1:], map[string]bool{"base": true})
	require.NoError(t, err)
	require.Len(t, phases, 1)
	assert.Equal(t, "leaf", phases[0][0].Name)
}

func TestValidateDependencyKindsRejectsPackagelessDependency(t *testing.T) {
	services := []Service{
		{Name: "platform-deploy", Config: &schemaconfig.SchemaConfig{Name: "platform-deploy", Kind: ir.SchemaKindGeneral}},
		{
			Name: "env",
			Config: &schemaconfig.SchemaConfig{
				Name:         "env",
				Kind:         ir.SchemaKindGeneral,
				Outputs:      map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
				Dependencies: []schemaconfig.ServiceDependency{{Name: "platform-deploy", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	err := validateDependencyKinds(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authoring imports are auto-tracked")

	services[1].Config.Dependencies = nil
	require.NoError(t, validateDependencyKinds(services))
}

func TestCheckConfigPurity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.config.ts")

	clean := `import { defineConfig, SchemaKind, service } from "@psgen/schema-config";
export default defineConfig({ name: "x", kind: SchemaKind.General, outputs: {} });
`
	require.NoError(t, os.WriteFile(path, []byte(clean), 0o644))
	require.NoError(t, checkConfigPurity(path))

	impure := `import { defineConfig } from "@psgen/schema-config";
import { webDb } from "../platform-deploy/model";
export default defineConfig({ name: "x", kind: "General", outputs: {} });
`
	require.NoError(t, os.WriteFile(path, []byte(impure), 0o644))
	err := checkConfigPurity(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may import only @psgen/schema-config")
}
