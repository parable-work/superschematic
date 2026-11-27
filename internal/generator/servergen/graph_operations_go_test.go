package servergen_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// goGraphInput is the plan's input of a Go server of fixture-graph-api, as
// the dispatch layer hands it over: one graph operation among its
// endpoints (D138), its Deps holding the ORM of fixture-version-graph-db,
// and its EnvConfig the database's field.
func goGraphInput(repoRoot string, paths naming.LocalPaths) servergen.Input {
	n := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	o := &apigen.APIOutput{
		SchemaName:  "fixture-graph-api",
		ModulePath:  n.GoAPIModule("fixture-graph-api"),
		TypesModule: n.GoTypesModule("fixture-graph-api"),
		Naming:      n,
		Endpoints: []apigen.EndpointInfo{{
			Name:          "compose",
			Path:          "/api/recipes/{id}/refs/{ref}/tree",
			RoutePath:     "/recipes/{id}/refs/{ref}/tree",
			Method:        "GET",
			Namespace:     "recipe",
			RequiresAuth:  true,
			RequiredPerms: []string{"recipes.write"},
			Operation: &ir.FieldDef{Name: "compose", GraphOperation: &ir.GraphOperation{
				Database: "fixture-version-graph-db", Graph: "Recipe", Type: "Recipe", Key: "id", Operation: ir.GraphOpCompose,
			}},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-version-graph-db", ORMModule: n.GoORMModule("fixture-version-graph-db"), VersionGraph: true},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-graph-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_VERSION_GRAPH_DB_DATABASE", GoName: "FixtureVersionGraphDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-version-graph-db"}},
		},
	}
	impl := filepath.Join(repoRoot, "services", "fixture-graph-api", "go")
	return servergen.Input{
		Stack:  "graph-stack",
		Server: "recipes",
		Dir:    servergen.ServerDir(out, "graph-stack", "recipes"),
		APIs: []servergen.APIInput{{
			Output:         o,
			Implementation: servergen.Implementation{Dir: impl, Import: "example.com/services/fixture-graph-api/go", Module: "example.com/services/fixture-graph-api/go", ModuleDir: impl},
		}},
		Modules: []servergen.Module{
			{Path: o.ModulePath, Dir: filepath.Join(out, "api", "fixture-graph-api"), Direct: true},
			{Path: o.TypesModule, Dir: filepath.Join(out, "types", "go", "fixture-graph-api")},
			{Path: n.GoTypesModule("fixture-version-graph-db"), Dir: filepath.Join(out, "types", "go", "fixture-version-graph-db")},
			{Path: o.Deps.ORMModule, Dir: filepath.Join(out, "orm", "fixture-version-graph-db"), Direct: true},
			{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
			{Path: n.ScalarGoModule, Version: "v1.0.0", Dir: paths.ScalarGo},
			{Path: "example.com/services/fixture-graph-api/go", Dir: impl, Direct: true},
		},
		Naming:         n,
		RepositoryRoot: repoRoot,
		ScalarGo:       paths.ScalarGo,
	}
}

// TestGoServerOfGraphOperations: the Go server of an API that serves a
// version graph's operations (D138) passes RegisterRoutes its database's
// ORM, which the operations read through, and the facades of its graphs,
// GraphsOf the ORM; one whose API has no database is refused.
func TestGoServerOfGraphOperations(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
	}
	in := goGraphInput(repoRoot, paths)
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`RegisterRoutes\(fixtureGraphApiRouter, fixturegraphapi\.Config\{\n\s+DB: +fixtureVersionGraphDb,\n(\s+//.*\n)?\s+Graphs: +fixturegraphapi\.GraphsOf\(fixtureVersionGraphDb\),`).Match(main) {
		t.Errorf("main.go passes RegisterRoutes no DB and no Graphs:\n%s", main)
	}

	in = goGraphInput(repoRoot, paths)
	o := in.APIs[0].Output
	o.Deps, o.EnvConfig = apigen.DepsInfo{}, nil
	const want = "stack graph-stack: server recipes serves fixture-graph-api, whose graph operations run through the ORM of its database (D138), and it has no database"
	if _, err := servergen.Plan(in); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Plan without a database = %v, want an error containing %q", err, want)
	}
}
