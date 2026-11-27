package apigen_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// graphFixtures are fixture-graph-api, whose @graphOperations set serves
// the Recipe graph (D138), and fixture-version-graph-db, its one DB
// dependency.
var graphFixtures = behaviorFixtures{"fixture-graph-api", "fixture-version-graph-db"}

// generateGraphAPI is the API output of fixture-graph-api over its
// database.
func generateGraphAPI(t *testing.T) (*apigen.APIOutput, *ir.Schema) {
	t.Helper()
	api, db := loadFixturePair(t, graphFixtures)
	return generatePairAPI(t, graphFixtures, api, db), api
}

// TestOpenAPIGraphOperations: fixture-graph-api's OpenAPI document holds
// each graph operation as any operation (D138), at its route under the
// root's plural and key, with its generated input and result types and
// the problems its route and the graph's engine answer it with beside the
// 400 and 500 of every route; and the permission catalog lists, under an
// operation, the permissions the set names for it and the one that lifts
// its row rule.
func TestOpenAPIGraphOperations(t *testing.T) {
	output, _ := generateGraphAPI(t)
	var doc struct {
		Paths      map[string]map[string]map[string]any `json:"paths"`
		Components struct {
			Schemas map[string]map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal([]byte(output.OpenAPISpecRaw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method, path, input, result string
		params                      []string
		statuses                    []string
		codes                       map[string][]string
	}{
		{"post", "/api/recipes/{id}/refs", "RecipeGraphBranchInput", "RecipeGraphRef", []string{"id"},
			[]string{"200", "400", "403", "404", "409", "500"},
			map[string][]string{"403": {"no_actor"}, "404": {"not_found"}, "409": {"name_taken", "schema_epoch"}}},
		{"put", "/api/recipes/{id}/refs/{ref}/edits", "RecipeGraphSaveInput", "RecipeGraphSaveResult", []string{"id", "ref"},
			[]string{"200", "400", "403", "404", "409", "422", "500"},
			map[string][]string{"409": {"version_conflict", "ref_sealed", "primary_merge_only", "schema_epoch"}, "422": {"invalid_row", "entity_not_found"}}},
		{"post", "/api/recipes/{id}/refs/{ref}/merge", "RecipeGraphMergeInput", "RecipeGraphMergeResult", []string{"id", "ref"},
			[]string{"200", "400", "403", "404", "409", "422", "500"},
			map[string][]string{"409": {"version_conflict", "ref_sealed", "no_parent", "merge_into_itself", "schema_epoch"}, "422": {"invalid_tree", "invalid_resolution", "unmatched_resolution"}}},
		{"post", "/api/recipes/{id}/release", "RecipeGraphReleaseInput", "RecipeGraphRelease", []string{"id"},
			[]string{"200", "400", "403", "404", "409", "500"},
			map[string][]string{"409": {"version_conflict", "not_tagged", "root_mismatch", "schema_epoch"}}},
		{"get", "/api/recipes/{id}/released", "", "RecipeGraphReleased", []string{"id"},
			[]string{"200", "400", "404", "409", "500"}, map[string][]string{"409": {"schema_epoch"}}},
		{"get", "/api/recipes/{id}/entities/{kind}/{key}/history", "", "", []string{"id", "kind", "key"},
			[]string{"200", "400", "404", "409", "500"}, nil},
		{"get", "/api/recipes/{id}/diff", "", "", []string{"id", "?from", "?to"},
			[]string{"200", "400", "404", "409", "500"}, nil},
	} {
		operation := doc.Paths[tc.path][tc.method]
		where := strings.ToUpper(tc.method) + " " + tc.path
		if operation == nil {
			t.Errorf("no %s: %v", where, keys(doc.Paths))
			continue
		}
		var parameters []string
		if list, ok := operation["parameters"].([]any); ok {
			for _, p := range list {
				param := p.(map[string]any)
				switch param["in"] {
				case "path":
					parameters = append(parameters, param["name"].(string))
				case "query":
					parameters = append(parameters, "?"+param["name"].(string))
				}
			}
		}
		if !reflect.DeepEqual(parameters, tc.params) {
			t.Errorf("%s: parameters = %v, want %v", where, parameters, tc.params)
		}
		body := jsonPath(operation, "requestBody", "content", "application/json", "schema", "$ref")
		if tc.input == "" && body != nil || tc.input != "" && body != "#/components/schemas/"+tc.input {
			t.Errorf("%s: request body = %v, want %q", where, body, tc.input)
		}
		responses := operation["responses"].(map[string]any)
		if statuses := keys(responses); !reflect.DeepEqual(statuses, tc.statuses) {
			t.Errorf("%s: responses = %v, want %v", where, statuses, tc.statuses)
		}
		for status, codes := range tc.codes {
			description, _ := jsonPath(responses, status, "description").(string)
			for _, code := range codes {
				if !strings.Contains(description, code+": ") {
					t.Errorf("%s: its %s does not name %s: %q", where, status, code, description)
				}
			}
		}
		if tc.result != "" {
			if text, _ := json.Marshal(responses["200"]); !strings.Contains(string(text), "#/components/schemas/"+tc.result) {
				t.Errorf("%s: its 200 does not answer %s: %s", where, tc.result, text)
			}
		}
	}
	for _, name := range []string{"RecipeGraphRef", "RecipeGraphTree", "RecipeGraphStepRow", "RecipeGraphStepRowInput", "RecipeGraphConflict", "RecipeGraphKind", "RecipeGraphVerdict"} {
		if doc.Components.Schemas[name] == nil {
			t.Errorf("the document has no component %s", name)
		}
	}

	var catalog struct {
		Permissions []struct {
			Name       string   `json:"name"`
			Operations []string `json:"operations"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(output.PermissionCatalogJSON), &catalog); err != nil {
		t.Fatalf("permissions.json: %v\n%s", err, output.PermissionCatalogJSON)
	}
	listed := map[string][]string{}
	for _, permission := range catalog.Permissions {
		listed[permission.Name] = permission.Operations
	}
	if want := []string{"RecipeArrangeHandler", "RecipeCommitHandler", "RecipeDiscardHandler", "RecipeMergeHandler", "RecipeRebaseHandler", "RecipeRevertHandler", "RecipeSaveHandler", "RecipeSealHandler"}; !reflect.DeepEqual(listed["recipes.admin"], want) {
		t.Errorf("the catalog lists recipes.admin under %v, want the operations the row rule holds, %v", listed["recipes.admin"], want)
	}
	if want := []string{"RecipeReleaseHandler"}; !reflect.DeepEqual(listed["recipes.release"], want) {
		t.Errorf("the catalog lists recipes.release under %v, want %v", listed["recipes.release"], want)
	}
	if write := listed["recipes.write"]; slices.Contains(write, "RecipeReleaseHandler") || !slices.Contains(write, "RecipeSaveHandler") || len(write) != len(ir.GraphOperationNames)-1 {
		t.Errorf("the catalog lists recipes.write under %v, want every graph operation but release", write)
	}
}

// TestGraphOperationsRefusedOverHeldRows: a Go server's graph operations
// over a database whose rows keep to an audience or a grant's reach
// (D156, D180) are refused, since the graph's facade reads the graph's
// tables with no caller (D138).
func TestGraphOperationsRefusedOverHeldRows(t *testing.T) {
	output, _ := generateGraphAPI(t)
	output.Deps.Access, output.Deps.AccessHeld = true, true
	_, err := apigen.ImplementedOutput(output)
	if err == nil || !strings.Contains(err.Error(), "apigen: fixture-graph-api serves the operations of a version graph of fixture-version-graph-db, whose rows keep to an audience or a grant's reach (@audience, @accessGrants), and the Go server's graph operations do not keep to them yet") {
		t.Fatalf("ImplementedOutput = %v, want the graph operations refused over held rows", err)
	}
}

// TestGraphOperationsOnAGoServer: the Go server serves the graph
// operations (D138), so the implementation's interface has getRecipe
// alone, and graphs.go serves each operation of the Recipe graph through
// its facade, with the set's row rule and the permission that lifts it.
// The module's files are held to their golden copies. Regenerate with:
// go test ./internal/generator/apigen -run TestGraphOperationsOnAGoServer -update
func TestGraphOperationsOnAGoServer(t *testing.T) {
	output, _ := generateGraphAPI(t)
	if !output.HasGraphOperations() || !output.HasGraphEndpoints() || !output.TakesDatabase() {
		t.Fatal("the output has no graph operation")
	}
	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	var implemented, served []string
	for _, ep := range view.Endpoints {
		implemented = append(implemented, ep.Name)
	}
	for _, ep := range view.GraphEndpoints {
		served = append(served, ep.Name)
		if ep.GraphRoutes != "recipeOperationsRoutes" || ep.GraphMember != "Recipe" {
			t.Errorf("%s is served by %q of %q", ep.Name, ep.GraphRoutes, ep.GraphMember)
		}
	}
	if !slices.Equal(implemented, []string{"getRecipe"}) {
		t.Errorf("the implementation's interface holds %v, want getRecipe alone", implemented)
	}
	slices.Sort(served)
	names := slices.Clone(ir.GraphOperationNames)
	slices.Sort(names)
	if !slices.Equal(served, names) {
		t.Errorf("graphs.go serves %v, want every graph operation", served)
	}
	if len(view.BehaviorEndpoints) != 0 || len(view.TableEndpoints) != 0 || len(view.HandlerEndpoints()) != 1+len(names) {
		t.Errorf("the view routes %d behavior, %d table and %d handler endpoints", len(view.BehaviorEndpoints), len(view.TableEndpoints), len(view.HandlerEndpoints()))
	}
	if view.Graphs == nil || len(view.Graphs.Sets) != 1 || !view.Graphs.Lifts || view.Graphs.DBTypes != "fixtureversiongraphdb" {
		t.Fatalf("Graphs = %+v", view.Graphs)
	}
	set := view.Graphs.Sets[0]
	if set.Set != "RecipeOperations" || set.Facade != "RecipeGraph" || set.RootKeyGo != "Id" || !slices.Equal(set.ByMembers, []string{"CreatedBy"}) || set.Override != "recipes.admin" ||
		set.SnapshotFilter != "ReferencedByRecipeSnapshotEntryCommit" || len(set.Kinds) != 7 || len(set.Endpoints) != len(names) {
		t.Errorf("the set = %+v", set)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		VersionGraphGo:  "/repo/runtime/versiongraph/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+graphFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", graphFixtures.api)
	for _, name := range []string{"routes.go", "interfaces.go", "graphs.go", "go.mod", "openapi.json", "permissions.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}
