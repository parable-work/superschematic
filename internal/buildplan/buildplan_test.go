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

func TestTopologicalSortOrdersAuthDBBeforeDependent(t *testing.T) {
	// The API's config names the DB only through authDb; the sort must still
	// put the DB first even though Dir order says otherwise.
	services := []Service{
		{Name: "api", Dir: "/services/api", Config: &schemaconfig.SchemaConfig{Name: "api", Kind: ir.SchemaKindAPI, AuthDB: "db"}},
		{Name: "db", Dir: "/services/db", Config: &schemaconfig.SchemaConfig{Name: "db", Kind: ir.SchemaKindDB}},
	}

	sorted, err := TopologicalSort(services)
	require.NoError(t, err)
	assert.Equal(t, []string{"db", "api"}, serviceNames(sorted))

	phases, err := GroupIntoPhases(sorted, nil)
	require.NoError(t, err)
	require.Len(t, phases, 2)
	assert.Equal(t, []string{"db"}, serviceNames(phases[0]))
	assert.Equal(t, []string{"api"}, serviceNames(phases[1]))
}

func TestClosureKeepsDiscoverOrderAndDropsUnrelated(t *testing.T) {
	general := func(name string, deps ...string) Service {
		cfg := &schemaconfig.SchemaConfig{Name: name, Kind: ir.SchemaKindGeneral}
		for _, dep := range deps {
			cfg.Dependencies = append(cfg.Dependencies, schemaconfig.ServiceDependency{Name: dep, Kind: ir.SchemaKindGeneral})
		}
		return Service{Name: name, Dir: "/services/" + name, Config: cfg}
	}
	leaf := general("leaf", "mid")
	leaf.Config.Kind = ir.SchemaKindAPI
	leaf.Config.AuthDB = "auth"
	auth := general("auth")
	auth.Config.Kind = ir.SchemaKindDB
	// Dir order deliberately puts dependents before their dependencies.
	services := []Service{leaf, general("other", "base"), general("mid", "base"), general("base"), auth}

	sorted, err := TopologicalSort(services)
	require.NoError(t, err)
	closure, err := Closure(sorted, "leaf")
	require.NoError(t, err)

	names := serviceNames(closure)
	assert.ElementsMatch(t, []string{"base", "mid", "auth", "leaf"}, names, "closure is the transitive dependencies plus authDb, root included")
	assert.NotContains(t, names, "other", "a sibling that shares a dependency is not in the closure")
	assert.Less(t, indexOf(names, "base"), indexOf(names, "mid"))
	assert.Less(t, indexOf(names, "mid"), indexOf(names, "leaf"))
	assert.Less(t, indexOf(names, "auth"), indexOf(names, "leaf"))

	var fromSorted []string
	for _, name := range serviceNames(sorted) {
		if name != "other" {
			fromSorted = append(fromSorted, name)
		}
	}
	assert.Equal(t, fromSorted, names, "closure preserves the order the resolver produced")
}

func TestClosureRejectsUnknownRootAndUndiscoveredDependency(t *testing.T) {
	services := []Service{
		{Name: "leaf", Config: &schemaconfig.SchemaConfig{Name: "leaf", Kind: ir.SchemaKindGeneral, Dependencies: []schemaconfig.ServiceDependency{{Name: "missing", Kind: ir.SchemaKindGeneral}}}},
	}

	_, err := Closure(services, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	_, err = Closure(services, "leaf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func serviceNames(services []Service) []string {
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
	}
	return names
}

func indexOf(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

func TestCheckConfigPurity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "schema.config.ts")

	clean := `import { defineConfig, SchemaKind, service } from "@superschematic/schema-config";
export default defineConfig({ name: "x", kind: SchemaKind.General, outputs: {} });
`
	require.NoError(t, os.WriteFile(path, []byte(clean), 0o644))
	require.NoError(t, checkConfigPurity(path))

	impure := `import { defineConfig } from "@superschematic/schema-config";
import { webDb } from "../platform-deploy/model";
export default defineConfig({ name: "x", kind: "General", outputs: {} });
`
	require.NoError(t, os.WriteFile(path, []byte(impure), 0o644))
	err := checkConfigPurity(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "may import only @superschematic/schema-config")
}
