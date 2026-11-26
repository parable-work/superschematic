package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGraphOperationsByServerLanguage: the TypeScript server serves a
// @graphOperations set (D138), so its build warns of nothing, its
// implementation interface has no method for a graph operation and its
// router mounts each with the runtime's handler; the Go and the Rust
// servers do not serve it yet, so each build warns once for the set, and
// each operation stays a method of the implementation's interface, to be
// written by hand, with the same route.
func TestGraphOperationsByServerLanguage(t *testing.T) {
	paths := testpaths.Local(t)
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	withRust := func(cfg *schemaconfig.SchemaConfig) {
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
		types["python"] = map[string]any{"enabled": true}
		if cfg.Name == "fixture-graph-api" {
			// And its SDKs in every language, which name each operation and
			// each generated type.
			enabled := map[string]any{"enabled": true}
			cfg.Outputs["sdk"] = map[string]any{"go": enabled, "typescript": enabled, "python": enabled, "rust": enabled}
		}
	}
	build := func(language string) (*Result, string) {
		t.Helper()
		outputRoot := testpaths.TempDir(t)
		var result *Result
		for _, service := range []string{"fixture-version-graph-db", "fixture-graph-api"} {
			schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
			if err != nil {
				t.Fatalf("load %s: %v", service, err)
			}
			if service == "fixture-graph-api" {
				cfg.Outputs["api"] = map[string]any{"enabled": true, "language": language}
			}
			if language == APILanguageRust {
				withRust(cfg)
			}
			result, err = Run(schema, cfg, Options{
				OutputRoot:     outputRoot,
				Paths:          paths,
				Naming:         naming.Default(),
				Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
				LoadDependency: loadDependency,
				DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
					_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
					if err != nil {
						return nil, false
					}
					if language == APILanguageRust {
						withRust(cfg)
					}
					return cfg, true
				},
			})
			if err != nil {
				t.Fatalf("run %s as %s: %v", service, language, err)
			}
		}
		return result, APIDir(outputRoot, "fixture-graph-api")
	}
	graphWarnings := func(result *Result) []string {
		var out []string
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "@graphOperations") {
				out = append(out, warning)
			}
		}
		return out
	}
	const operations = "branch, save, arrange, commit, seal, merge, rebase, revert, discard, history, compose, release, released, materialize, diff, entityHistory"
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	result, dir := build(APILanguageTypeScript)
	if warnings := graphWarnings(result); len(warnings) != 0 {
		t.Errorf("a TypeScript build warned of the set it serves: %v", warnings)
	}
	router := read(filepath.Join(dir, "router.ts"))
	for _, want := range []string{
		"graphBranch(options.orm, options.graphs.recipe, graphOperations.branch, ctx, request)",
		"graphEntityHistory(options.orm, options.graphs.recipe, graphOperations.entityHistory, ctx, request)",
		"import type { RecipeGraph } from '@schemas/fixture-version-graph-db-types/versiongraph';",
	} {
		if !strings.Contains(router, want) {
			t.Errorf("router.ts lacks %q", want)
		}
	}
	if interfaces := read(filepath.Join(dir, "interfaces.ts")); strings.Contains(interfaces, "branch(") || !strings.Contains(interfaces, "getRecipe(") {
		t.Errorf("the TypeScript interface holds other than getRecipe:\n%s", interfaces)
	}

	// The Go server serves the set from graphs.go: its build warns of
	// nothing, and its interface holds getRecipe alone.
	result, dir = build(APILanguageGo)
	if warnings := graphWarnings(result); len(warnings) != 0 {
		t.Errorf("a Go build warned of the set it serves: %v", warnings)
	}
	goInterfaces := read(filepath.Join(dir, "interfaces.go"))
	if !strings.Contains(goInterfaces, "GetRecipe(") || strings.Contains(goInterfaces, "Branch(") || strings.Contains(goInterfaces, "EntityHistory(") {
		t.Errorf("the Go interface holds other than GetRecipe:\n%s", goInterfaces)
	}
	if routes := read(filepath.Join(dir, "routes.go")); !strings.Contains(routes, "/recipes/{id}/refs/{ref}/edits") || !strings.Contains(routes, "createRecipeSaveHandler(&recipeOperationsRoutes{db: cfg.DB, graph: cfg.Graphs.Recipe})") {
		t.Error("the Go router does not route save at its path to graphs.go")
	}
	goGraphs := read(filepath.Join(dir, "graphs.go"))
	for _, method := range []string{"Branch(", "Save(", "EntityHistory(", "Released("} {
		if !strings.Contains(goGraphs, ") "+method) {
			t.Errorf("graphs.go serves no %s", method)
		}
	}
	want := "fixture-graph-api: the Go server writes no handlers for RecipeOperations, a @graphOperations set of Recipe's version graph (D138): its 16 operations (" + operations + ") each stay a method of the implementation's interface, to be written by hand over the graph's facade, with the same routes and types; the TypeScript server serves them"

	result, dir = build(APILanguageRust)
	want = strings.Replace(want, "the Go server", "the Rust server", 1)
	if warnings := graphWarnings(result); len(warnings) != 1 || warnings[0] != want {
		t.Errorf("the Rust build's warnings = %q,\nwant one, %q", warnings, want)
	}
	for _, done := range []string{"sdk-python", "sdk-rust", "sdk-go", "sdk-typescript"} {
		if result.Outputs[done] == "" {
			t.Errorf("the build wrote no %s: %v", done, result.Outputs)
		}
	}
	rustInterfaces := read(filepath.Join(dir, "src", "interfaces.rs"))
	for _, method := range []string{"fn branch(", "fn save(", "fn entity_history(", "fn get_recipe("} {
		if !strings.Contains(rustInterfaces, method) {
			t.Errorf("the Rust trait lacks %s, a method the project writes", method)
		}
	}
}
