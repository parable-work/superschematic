package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// graphFixture is fixture-graph-api, whose @graphOperations set serves the
// Recipe graph of fixture-version-graph-db, its one DB dependency (D138).
var graphFixture = behaviorFixture{api: "fixture-graph-api", db: "fixture-version-graph-db"}

// graphOutput is fixture-graph-api's crate with its Deps set as the
// dispatch layer sets them: the pool and the Rust types of its database.
func graphOutput(t *testing.T) *APIOutput {
	t.Helper()
	_, db, _ := graphFixture.load(t)
	output := graphFixture.generate(t, db)
	if err := output.SetDeps(DepsInfo{
		Database:           graphFixture.db,
		DatabaseTypesCrate: naming.Default().RustTypesCrate(graphFixture.db),
		DatabaseTypesPath:  "../../types/rust/" + graphFixture.db,
	}); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	return output
}

// TestWriteRustAPIGoldenGraphOperations pins the crate of
// fixture-graph-api: the trait holds get_recipe alone, the router hands
// each graph operation to its function of src/graphs.rs, which runs it
// through the Recipe graph's facade over the pool, and the crate depends
// on the database's Rust types, with the repositories, and on the version
// graph's engine. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenGraphOperations -update
func TestWriteRustAPIGoldenGraphOperations(t *testing.T) {
	output := graphOutput(t)
	if !output.HasGraphOperations() || !output.HasDatabaseTypes() || output.HasTableOperations() || output.HasBehaviorOperations() {
		t.Fatalf("graphs %t, database types %t", output.HasGraphOperations(), output.HasDatabaseTypes())
	}
	var served []string
	for _, endpoint := range output.GraphEndpoints() {
		served = append(served, endpoint.Graph.Operation)
		if !endpoint.RouterServed() || endpoint.Graph.Set != "RecipeOperations" || endpoint.Graph.RootField != "id" {
			t.Errorf("%s: %+v", endpoint.Name, endpoint.Graph)
		}
		if owned := slices.Contains(ir.GraphOperationsOwned, endpoint.Graph.Operation); owned != endpoint.Graph.Owned {
			t.Errorf("%s is owned %t, want %t", endpoint.Name, endpoint.Graph.Owned, owned)
		}
	}
	slices.Sort(served)
	names := slices.Clone(ir.GraphOperationNames)
	slices.Sort(names)
	if !slices.Equal(served, names) {
		t.Errorf("the router serves %v, want every graph operation", served)
	}
	sets := output.GraphSets()
	if len(sets) != 1 || sets[0].Field != "recipe" || sets[0].Facade != "RecipeGraph" || !slices.Equal(sets[0].By, []string{"created_by"}) || sets[0].Override != "recipes.admin" || len(sets[0].Kinds) != 7 {
		t.Fatalf("the sets = %+v", sets)
	}
	generated := writeGoldenAPI(t, graphFixture.api, output)
	for file, wants := range map[string][]string{
		"src/interfaces.rs": {"async fn get_recipe(", "pub graphs: crate::graphs::Graphs,"},
		"src/router.rs":     {"crate::graphs::recipe_save(", "crate::graphs::recipe_entity_history("},
		"src/graphs.rs":     {"pub(crate) async fn recipe_branch(", ".save_values(&actor, &row.id, body.version, &edits)", "impl engine::postgres::Client for GraphPool"},
		"src/deps.rs":       {"graphs: crate::graphs::Graphs::new(db),"},
		"Cargo.toml":        {`superschematic-versiongraph-engine = { path = "../../../../runtime/versiongraph/rust-engine" }`, `features = ["postgres"]`},
	} {
		for _, want := range wants {
			if !strings.Contains(generated[file], want) {
				t.Errorf("%s holds no %q", file, want)
			}
		}
	}
	if strings.Contains(generated["src/interfaces.rs"], "async fn branch(") {
		t.Error("the trait holds a graph operation")
	}
}

// TestGraphOperationsNeedAPostgresDatabase: the graph operations run
// through the graph's facade in the database's Rust types, on its pool, so
// Deps without the types, and a database on a SQLite file, are refused.
func TestGraphOperationsNeedAPostgresDatabase(t *testing.T) {
	_, db, _ := graphFixture.load(t)
	const prefix = "rustrestgen: fixture-graph-api serves the operations of a version graph of fixture-version-graph-db (D138), "
	for name, tc := range map[string]struct {
		deps DepsInfo
		want string
	}{
		"no Rust types": {DepsInfo{Database: graphFixture.db}, "whose facade is in its Rust types, and fixture-version-graph-db generates none; enable outputs.types.rust in fixture-version-graph-db's config"},
		"sqlite":        {DepsInfo{Database: graphFixture.db, SQLite: true, DatabaseTypesCrate: "types"}, "which lists sqlite first, and the Rust server runs a graph's facade on Postgres alone so far"},
		"another db":    {DepsInfo{Database: "shop-db", DatabaseTypesCrate: "types"}, "and its Deps hold shop-db's pool"},
	} {
		t.Run(name, func(t *testing.T) {
			err := graphFixture.generate(t, db).SetDeps(tc.deps)
			if err == nil || err.Error() != prefix+tc.want {
				t.Errorf("SetDeps = %v,\nwant %s", err, prefix+tc.want)
			}
		})
	}
}
