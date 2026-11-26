package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGraphOperationsRoundTrip: fixture-graph-api written in each format
// and read back over its database is the same expanded IR (D138). Every
// format writes the @graphOperations set with its root, its options and
// its clause, and none writes the operations, types or enums the loader
// fills it with.
func TestGraphOperationsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-graph-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	db, err := loader.LoadService(tsFixtures + "/fixture-version-graph-db")
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Recipe } from "@schemas/fixture-version-graph-db";`,
			"@graphOperations(Recipe, { permissions: { release: [\"recipes.release\"] }, by: [\"createdBy\"], overridePermission: \"recipes.admin\" })\n@requirePermission([\"recipes.write\"])\nexport abstract class RecipeOperations {}\n",
		},
		FormatJSON: {`"graphOperations": {`, `"type": "Recipe"`, `"recipes.release"`, `"overridePermission": "recipes.admin"`, `"recipes.write"`, `"operations": []`},
		FormatYAML: {"graphOperations:\n", "type: Recipe", "- recipes.release", "overridePermission: recipes.admin", "- recipes.write", "operations: []"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-version-graph-db", "kind": "DB"}], "outputs": {}}`+"\n", schema.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, schema)
				// The database fixture's index exports its sentinel alone,
				// so the written import of Recipe from its package is
				// pointed at the file that declares the class.
				tsconfig := filepath.Join(dir, "tsconfig.json")
				data, err := os.ReadFile(tsconfig)
				if err != nil {
					t.Fatal(err)
				}
				pointed := strings.Replace(string(data), "fixture-version-graph-db/src/index.ts", "fixture-version-graph-db/src/recipe.schema.ts", 1)
				if pointed == string(data) {
					t.Fatalf("tsconfig.json names no path of fixture-version-graph-db:\n%s", data)
				}
				if err := os.WriteFile(tsconfig, []byte(pointed), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"RecipeGraphRef", "RecipeGraphStepRow", "RecipeGraphKind", "recipe_branch", "entityHistory", "graphOperation\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
				if name != db.Name {
					return nil, fmt.Errorf("no service %s", name)
				}
				return db, nil
			}))
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}

// TestWriterRefusesAFilledGraphOperationsSet: a set that holds an
// operation the loader did not add has no TypeScript form, and a set
// whose root no import covers has none either.
func TestWriterRefusesAFilledGraphOperationsSet(t *testing.T) {
	for _, tc := range []struct {
		name    string
		set     *ir.OperationSet
		imports []ir.Import
		want    string
	}{
		{
			"an authored operation",
			&ir.OperationSet{Name: "RecipeOperations", GraphOperations: &ir.GraphOperationsConfig{Type: "Recipe"}, Operations: []*ir.FieldDef{
				{Name: "ping", TypeRef: ir.TypeRef{Name: "boolean"}, HTTPMethod: "GET"},
			}},
			[]ir.Import{{Package: "@schemas/recipes-db", Types: []string{"Recipe"}}},
			"operation set RecipeOperations: a @graphOperations class has no members; write the set the loader has not filled",
		},
		{
			"a root no import covers",
			&ir.OperationSet{Name: "RecipeOperations", GraphOperations: &ir.GraphOperationsConfig{Type: "Recipe"}, Operations: []*ir.FieldDef{}},
			nil,
			"operation set RecipeOperations: the root Recipe is not covered by the document's imports",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := ir.NewSchema("recipes-api", ir.SchemaKindAPI)
			schema.Imports = tc.imports
			schema.OperationSets = []*ir.OperationSet{tc.set}
			_, err := WriteService(schema, FormatTS, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("WriteService = %v, want an error holding %q", err, tc.want)
			}
		})
	}
}
