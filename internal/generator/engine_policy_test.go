package generator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// A build of one stack, as `superschematic build` runs it, reads its
// engine database's config on demand (LoadDependencyConfig) where
// build-all hands it over (DependencyConfig); either way each server opens
// the engine with the policy the database's outputs.engine names, not the
// default "permission".
func TestABuildOfOneStackOpensTheEngineWithItsPolicy(t *testing.T) {
	fixtures := filepath.Join("servergen", "testdata", "engine")
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{"desk-db", "desk-api", "desk-stack"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	configs["desk-db"].Outputs["engine"] = map[string]any{"policy": "allowAll"}

	for _, buildAll := range []bool{true, false} {
		repoRoot := testpaths.TempDir(t)
		outputRoot := filepath.Join(repoRoot, "schemas", "dist")
		options := Options{
			OutputRoot: outputRoot,
			Paths:      testpaths.Local(t),
			Naming:     naming.Default(),
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
				if cfg, ok := configs[name]; ok {
					return cfg, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			Stage:          registry.StageAll,
			RepositoryRoot: repoRoot,
		}
		if buildAll {
			options.DependencyConfig = func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			}
		}
		for _, name := range []string{"desk-db", "desk-api", "desk-stack"} {
			if _, err := Run(schemas[name], configs[name], options); err != nil {
				t.Fatalf("build-all %t: run %s: %v", buildAll, name, err)
			}
		}
		main, err := os.ReadFile(filepath.Join(outputRoot, "server", "desk-stack", "desk-api", "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(main), "EngineDocument, `\"allowAll\"`") {
			t.Errorf("build-all %t: desk-api's server opens the engine with another policy than desk-db's allowAll:\n%s", buildAll, main)
		}
	}
}
