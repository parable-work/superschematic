package tsrestgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sdkgen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// graphAPI is the API whose @graphOperations set the loader fills with
// the operations of fixture-version-graph-db's Recipe graph (D138), under
// RecipeOperations, which needs recipes.write, a release recipes.release,
// and keeps a change set to the caller who branched it or a holder of
// recipes.admin; and RecipeQueries.getRecipe, an authored operation.
const graphAPI = "fixture-graph-api"

// graphDB is fixture-graph-api's database, its one DB dependency.
const graphDB = "fixture-version-graph-db"

// graphFixture is fixture-graph-api with its database and apigen's
// endpoints for it; the router serves its MCP tools.
func graphFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, graphDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, graphAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", graphAPI, err)
	}
	deps := map[string]*ir.Schema{graphDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     graphAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: graphAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: graphDeps}
}

// graphDeps is fixture-graph-api's Deps as the dispatch layer sets it: a
// pool of its database, which lists postgres first, and the repositories
// its TypeScript types export.
var graphDeps = DepsInfo{
	Database:             graphDB,
	Dialect:              "postgres",
	ORM:                  "FixtureVersionGraphDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-version-graph-db-types",
}

// generateGraph generates the package for fixture, with its MCP endpoint,
// without setting its Deps.
func generateGraph(t *testing.T, fixture apiFixture) *APIOutput {
	t.Helper()
	output, err := Generate(fixture.schema, fixture.endpoints, Options{SchemaName: fixture.name, Dependencies: fixture.deps, Clock: fixedClock, MCP: fixture.mcp})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return output
}

// generateGraphFixture generates the package for fixture-graph-api, its
// Deps set.
func generateGraphFixture(t *testing.T) *APIOutput {
	t.Helper()
	output := generateGraph(t, graphFixture(t))
	if err := output.SetDeps(graphDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	return output
}

// TestWriteAPIGoldenGraphOperations pins the package for
// fixture-graph-api: its implementation interface has getRecipe alone, and
// its router declares the Recipe graph, mounts each graph operation with
// the runtime's handler of it and takes the graph's facade in
// RouterOptions.graphs. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenGraphOperations -update
func TestWriteAPIGoldenGraphOperations(t *testing.T) {
	checkGolden(t, generateGraphFixture(t), graphAPI)
}

// TestGenerateGraphOperations: a graph operation (D138) is the router's,
// not the implementation's. Its endpoint carries the graph it runs on and
// its row rule; the implementation interfaces and interfaces.ts's imports
// leave it out; the router declares the graph once, with each kind's row
// members; and the MCP endpoint serves each operation's tool through its
// route.
func TestGenerateGraphOperations(t *testing.T) {
	output := generateGraphFixture(t)
	if !output.HasGraphs() || !output.RouterNeedsORM() || output.HasBehaviors() || output.HasTableOperations() {
		t.Errorf("HasGraphs %t, RouterNeedsORM %t", output.HasGraphs(), output.RouterNeedsORM())
	}
	if what, decisions := output.ORMOperations(); what != "the graph operations" || decisions != "D138" {
		t.Errorf("ORMOperations = %q, %q", what, decisions)
	}
	if len(output.GraphSets) != 1 {
		t.Fatalf("GraphSets = %+v", output.GraphSets)
	}
	set := output.GraphSets[0]
	kinds := set.Kinds
	set.Kinds = nil
	if want := (GraphSetInfo{
		Set: "RecipeOperations", Database: graphDB, Graph: "Recipe", Root: "Recipe", Var: "recipeGraph", Member: "recipe", Facade: "RecipeGraph",
		Repository: "recipe", Refs: "recipeRef", Commits: "recipeCommit", Key: "id",
	}); !reflect.DeepEqual(set, want) {
		t.Errorf("the graph = %+v, want %+v", set, want)
	}
	var kindNames []string
	for _, kind := range kinds {
		kindNames = append(kindNames, kind.Kind+":"+kind.Tree)
		if kind.Kind == "step" && !slices.Equal(kind.Fields, []string{"position", "instruction", "timings", "scratch"}) {
			t.Errorf("step's row members = %v", kind.Fields)
		}
	}
	if want := []string{"cover:cover", "ingredient:ingredient", "note:note", "part:part", "step:step", "tasting:tasting", "utensil:utensil"}; !slices.Equal(kindNames, want) {
		t.Errorf("kinds = %v, want %v", kindNames, want)
	}
	if got := output.GraphHandlers(); len(got) != len(ir.GraphOperationNames) || !slices.Contains(got, "graphEntityHistory") || !slices.Contains(got, "graphBranch") {
		t.Errorf("GraphHandlers = %v", got)
	}
	if got := output.GraphFacadeModule(); got != "@schemas/fixture-version-graph-db-types/versiongraph" {
		t.Errorf("GraphFacadeModule = %s", got)
	}

	keys := map[string]EndpointInfo{}
	for _, ep := range output.Endpoints {
		keys[ep.Key] = ep
	}
	for _, name := range ir.GraphOperationNames {
		ep, ok := keys[name]
		if !ok || ep.Graph == nil {
			t.Errorf("no graph endpoint keyed %s", name)
			continue
		}
		want := GraphRoute{Set: "RecipeOperations", Database: graphDB, Graph: "Recipe", Type: "Recipe", Operation: name, Member: "recipe", Var: "recipeGraph"}
		if slices.Contains(ir.GraphOperationsOwned, name) {
			want.By, want.Override = []string{"createdBy"}, "recipes.admin"
		}
		if !reflect.DeepEqual(*ep.Graph, want) {
			t.Errorf("%s's route = %+v, want %+v", name, *ep.Graph, want)
		}
		wantPerms := "recipes.write"
		if name == "release" {
			wantPerms = "recipes.release"
		}
		if strings.Join(ep.RequiredPerms, ",") != wantPerms || !ep.RequiresAuth || ep.Namespace != "recipe" {
			t.Errorf("%s: namespace %s, auth %t, permissions %v", name, ep.Namespace, ep.RequiresAuth, ep.RequiredPerms)
		}
	}
	if save := keys["save"]; save.Method != "PUT" || save.Path != "/api/recipes/{id}/refs/{ref}/edits" || !save.HasInput || save.Graph.Handler() != "graphSave" ||
		save.Graph.Literal() != "{ graph: recipeGraph, operation: 'save', by: ['createdBy'], overridePermission: 'recipes.admin' }" {
		t.Errorf("save = %+v, %s", save, save.Graph.Literal())
	}
	if diff := keys["diff"]; diff.Method != "GET" || len(diff.QueryParams) != 2 || diff.QueryParams[0].Name != "from" || diff.HasInput || diff.Graph.Literal() != "{ graph: recipeGraph, operation: 'diff' }" {
		t.Errorf("diff = %+v", diff)
	}
	if get := keys["getRecipe"]; get.Graph != nil {
		t.Errorf("getRecipe runs a graph operation: %+v", get.Graph)
	}

	// The implementation has getRecipe alone.
	if len(output.Namespaces) != 1 || output.Namespaces[0].Name != "recipe" || len(output.Namespaces[0].Endpoints) != 1 || output.Namespaces[0].Endpoints[0].Name != "getRecipe" {
		t.Errorf("namespaces = %+v", output.Namespaces)
	}
	for _, imp := range output.TypeImports {
		for _, symbol := range imp.Symbols {
			if strings.HasPrefix(symbol, "RecipeGraph") {
				t.Errorf("interfaces.ts imports %s, a graph operation's", symbol)
			}
		}
	}
	var validators []string
	for _, imp := range output.ValidatorImports {
		validators = append(validators, imp.Symbols...)
	}
	if !slices.Contains(validators, "parseRecipeGraphSaveInputJson") || !slices.Contains(validators, "parseRecipeGraphBranchInputFromJSON") {
		t.Errorf("router.ts does not import the inputs' validators: %v", validators)
	}

	// The MCP endpoint serves each operation's tool through its route.
	if output.MCP == nil {
		t.Fatal("no MCP endpoint")
	}
	tools := map[string]MCPTool{}
	for _, tool := range output.MCP.Tools {
		tools[tool.Name] = tool
	}
	if tool, ok := tools["recipe_branch"]; !ok || tool.Operation != "branch" || tool.Annotations.ReadOnlyHint {
		t.Errorf("recipe_branch = %+v among %d tools", tool, len(tools))
	}
	if tool := tools["recipe_released"]; tool.Operation != "released" || !tool.Annotations.ReadOnlyHint {
		t.Errorf("recipe_released = %+v", tool)
	}
	if tool := tools["recipe_diff"]; !slices.Equal(tool.Arguments, []MCPToolArgument{{Key: "id", In: "path", Name: "id"}, {Key: "from", In: "query", Name: "from"}, {Key: "to", In: "query", Name: "to"}}) {
		t.Errorf("recipe_diff's arguments = %+v", tool.Arguments)
	}
}

// TestGraphOperationsNeedTheRepositories: the graph operations read the
// root through Deps.orm and run through the graph's facade, both of the
// database's TypeScript types, so Deps without the repositories is
// refused, saying why there are none, and so is a package written without
// Deps.
func TestGraphOperationsNeedTheRepositories(t *testing.T) {
	const prefix = "tsrestgen: fixture-graph-api serves the operations of Recipe's version graph (@graphOperations on RecipeOperations), which run through Deps.orm and the graph's facade, both of which fixture-version-graph-db's TypeScript types export for Postgres; "
	for name, tc := range map[string]struct {
		deps DepsInfo
		why  string
	}{
		"no TypeScript types": {DepsInfo{Database: graphDB, Dialect: "postgres"}, "enable outputs.types.typescript in fixture-version-graph-db's config"},
		"sqlite first":        {DepsInfo{Database: graphDB, Dialect: SQLiteDialect, ORM: "FixtureVersionGraphDbOrm", DatabaseTypesPackage: "@schemas/fixture-version-graph-db-types"}, "fixture-version-graph-db lists sqlite first, and the graph's facade runs on Postgres alone (D138)"},
	} {
		t.Run(name, func(t *testing.T) {
			err := generateGraph(t, graphFixture(t)).SetDeps(tc.deps)
			if err == nil || err.Error() != prefix+tc.why {
				t.Errorf("SetDeps = %v,\nwant %s", err, prefix+tc.why)
			}
		})
	}
	err := WriteAPI(generateGraph(t, graphFixture(t)), t.TempDir())
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("WriteAPI without Deps = %v", err)
	}
}

// graphCheck is the file tsc checks the package for fixture-graph-api
// against beside it: buildRouter takes the repositories and the Recipe
// graph's generated facade, and refuses options without either; and the
// implementation has no method for a graph operation.
const graphCheck = `import type { FixtureVersionGraphDbOrm } from '@schemas/fixture-version-graph-db-types/orm';
import type { RecipeGraph } from '@schemas/fixture-version-graph-db-types/versiongraph';
import { buildRouter, graphOperations, operationSpecs, recipeGraph, type Implementations } from './index';

declare const implementations: Implementations;
declare const orm: FixtureVersionGraphDbOrm;
declare const recipe: RecipeGraph;

export const router = buildRouter(implementations, { orm, graphs: { recipe }, bodyLimits: { save: 1 << 20 } });
// @ts-expect-error RouterOptions.graphs holds the facade the graph operations run through.
export const without = buildRouter(implementations, { orm });
// @ts-expect-error RouterOptions.orm reads the root and the graph's refs.
export const withoutOrm = buildRouter(implementations, { graphs: { recipe } });
// @ts-expect-error The graph operations have no implementation method.
export const save: Implementations['recipe']['save'] = undefined;
export const spec: 'entityHistory' = operationSpecs.entityHistory.name;
export const owned: readonly ['createdBy'] = graphOperations.save.by;
export const kind: 'cover' = recipeGraph.kinds[0].kind;
`

// TestGeneratedGraphOperationsAPICompiles type-checks the package for
// fixture-graph-api against its type packages, fixture-version-graph-db's
// repositories and its Recipe graph's facade among them, the real runtime
// and Hono, with graphCheck.
func TestGeneratedGraphOperationsAPICompiles(t *testing.T) {
	tree := materializeAPI(t, graphFixture(t))
	if err := os.WriteFile(filepath.Join(tree.apiDir, "graph.check.ts"), []byte(graphCheck), 0o644); err != nil {
		t.Fatal(err)
	}
	tree.typeCheck(t)
}

// graphCases are the cases graph_operations_runtime.test.ts runs, by
// name: D138's end-to-end scenario.
var graphCases = []string{
	"branch creates the primary line of a root that has none, and a change set of it",
	"save writes rows with order keys, and the tree reads them back",
	"arrange puts a parent's children in the order given",
	"commit writes a commit, and a stale version is a version conflict",
	"a change set is its creator's: another caller is not found, and the override permission lifts the rule",
	"merge with a tag commits onto the primary line, and history, diff and materialize read it",
	"release needs its own permission, a second release at the same version conflicts, and released reads the tree",
	"entity history lists the commits that changed an entity",
	"a rebase that conflicts answers the conflicts and writes nothing, and a resolution settles it",
	"revert composes a change set to an earlier commit's tree",
	"a sealed ref refuses a save, and discard frees the name",
	"an input the core refuses is unprocessable",
	"a root, a ref or a commit that is not this root's is not found",
	"a caller without the route's permission is refused by the API's auth, and a write needs a caller who is a UUID",
	"the MCP tools are listed, and calling one runs the operation",
}

// TestGeneratedGraphOperationsEndToEnd serves fixture-graph-api's generated
// router under bun over fixture-version-graph-db's generated repositories
// and its Recipe graph's generated facade on Postgres, in a schema of its
// own at SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL with the database's
// create.sql applied, and drives it through the API's generated TypeScript
// SDK and its MCP endpoint over HTTP
// (testdata/graph_operations_runtime.test.ts): every case of graphCases
// must pass. It skips without the database, and without bun unless
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS says the gate must run.
func TestGeneratedGraphOperationsEndToEnd(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL to run the generated TypeScript server's graph operations against Postgres")
	}
	fixture := graphFixture(t)
	tree := materializeAPI(t, fixture)
	tree.typeCheck(t)

	// The SDK, beside the package, resolving its types package by name.
	names := naming.Default()
	tsOutput, err := tsgen.Generate(fixture.schema, tsgen.Options{
		SchemaName:         graphAPI,
		Dependencies:       fixture.deps,
		DependencyPackages: map[string]string{graphDB: names.NpmTypesPackage(graphDB)},
		Registry:           fixture.registry,
		Clock:              fixedClock,
	})
	if err != nil {
		t.Fatalf("tsgen.Generate: %v", err)
	}
	sdkOutput, err := sdkgen.Generate(fixture.endpoints, tsgen.ParseableTypeNames(tsOutput), fixedClock)
	if err != nil {
		t.Fatalf("sdkgen.Generate: %v", err)
	}
	sdkDir := filepath.Join(tree.root, "sdk", "typescript", graphAPI)
	if err := sdkgen.WriteSDKWithTools(sdkOutput, fixture.endpoints, sdkDir, fixedClock); err != nil {
		t.Fatalf("write the SDK: %v", err)
	}
	typesPackage := names.NpmTypesPackage(graphAPI)
	link(t, tree.typesDirs[typesPackage], filepath.Join(sdkDir, "node_modules", filepath.FromSlash(typesPackage)))

	ddl, err := sqlgen.Generate(fixture.deps[graphDB], sqlgen.Options{SchemaName: graphDB})
	if err != nil {
		t.Fatalf("generate %s's DDL: %v", graphDB, err)
	}
	ddlDir := filepath.Join(tree.root, "sql", graphDB)
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write %s's DDL: %v", graphDB, err)
	}
	createSQL := filepath.Join(ddlDir, "test-create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(ddlDir, "create.sql"), createSQL)

	// The test builds the facade over the version-graph package's Postgres
	// client, which the database's types package depends on.
	paths := testpaths.Local(t)
	link(t, paths.VersionGraphTypeScript, filepath.Join(tree.apiDir, "node_modules", filepath.FromSlash(names.VersionGraphNpmPackage)))

	testFile := tree.stage(t, "graph_operations_runtime.test.ts")
	report := filepath.Join(tree.root, "graph_operations.junit.xml")
	run := exec.Command(tree.bun, "test", testFile, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	run.Dir = tree.apiDir
	run.Env = append(os.Environ(), "API_DIR="+tree.apiDir, "SDK_DIR="+sdkDir, "CREATE_SQL="+createSQL, "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL="+dsn)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("graph_operations_runtime.test.ts failed: %v\n%s", err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range graphCases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q:\n%s", name, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass:\n%s", name, junit)
		}
	}
	if len(cases) != len(graphCases) {
		t.Errorf("bun ran %d cases, and graphCases names %d:\n%s", len(cases), len(graphCases), junit)
	}
	if testing.Verbose() {
		t.Logf("graph_operations_runtime.test.ts:\n%s", out)
	}
}
