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

// goTableAPI is fixture-tracker-api's Go server output as the dispatch
// layer hands it to the server's plan, made public, so its Config takes
// the implementation's auth middleware, which authenticates the caller a
// table operation's route requires: one table operation among its
// endpoints (D106), its Deps holding the ORM of fixture-tracker-db, and its
// EnvConfig the database's field.
func goTableAPI() *apigen.APIOutput {
	n := naming.Default()
	return &apigen.APIOutput{
		SchemaName:     "fixture-tracker-api",
		ModulePath:     n.GoAPIModule("fixture-tracker-api"),
		TypesModule:    n.GoTypesModule("fixture-tracker-api"),
		Naming:         n,
		IsPublic:       true,
		UpstreamSchema: "fixture-tracker-db",
		Endpoints: []apigen.EndpointInfo{{
			Name:         "getTeam",
			Path:         "/api/teams/{id}",
			RoutePath:    "/teams/{id}",
			Method:       "GET",
			Namespace:    "teams",
			RequiresAuth: true,
			Operation: &ir.FieldDef{Name: "getTeam", TableOperation: &ir.TableOperation{
				Kind: ir.TableOperationRead, Database: "fixture-tracker-db", Type: "Team", Key: "id",
			}},
		}},
		Deps: apigen.DepsInfo{Database: "fixture-tracker-db", ORMModule: n.GoORMModule("fixture-tracker-db")},
		EnvConfig: &envgen.ConfigOutput{
			SchemaName: "fixture-tracker-api",
			EnvConfig:  true,
			Derived:    []envgen.DerivedField{{Key: "FIXTURE_TRACKER_DB_DATABASE", GoName: "FixtureTrackerDbDatabase", Kind: ir.EdgeSQL, Service: "fixture-tracker-db"}},
		},
	}
}

// goTableInput is the plan's input of a server of goTableAPI in the
// repository repoRoot.
func goTableInput(repoRoot string, paths naming.LocalPaths) servergen.Input {
	n := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	o := goTableAPI()
	impl := filepath.Join(repoRoot, "services", "fixture-tracker-api", "go")
	return servergen.Input{
		Stack:  "tracker-stack",
		Server: "tracker",
		Dir:    servergen.ServerDir(out, "tracker-stack", "tracker"),
		APIs: []servergen.APIInput{{
			Output:         o,
			Implementation: servergen.Implementation{Dir: impl, Import: "example.com/services/fixture-tracker-api/go", Module: "example.com/services/fixture-tracker-api/go", ModuleDir: impl},
		}},
		Modules: []servergen.Module{
			{Path: o.ModulePath, Dir: filepath.Join(out, "api", "fixture-tracker-api"), Direct: true},
			{Path: o.TypesModule, Dir: filepath.Join(out, "types", "go", "fixture-tracker-api")},
			{Path: n.GoTypesModule("fixture-tracker-db"), Dir: filepath.Join(out, "types", "go", "fixture-tracker-db")},
			{Path: o.Deps.ORMModule, Dir: filepath.Join(out, "orm", "fixture-tracker-db"), Direct: true},
			{Path: n.HTTPRuntimeGoModule, Dir: paths.HTTPRuntimeGo, Direct: true},
			{Path: n.ScalarGoModule, Version: "v1.0.0", Dir: paths.ScalarGo},
			{Path: "example.com/services/fixture-tracker-api/go", Dir: impl, Direct: true},
		},
		Naming:         n,
		RepositoryRoot: repoRoot,
		ScalarGo:       paths.ScalarGo,
	}
}

// TestGoServerOfTableOperations: the Go server of an API with table
// operations (D106) passes its database's ORM to RegisterRoutes, which
// serves them from its tables, as it does for an API with behavior
// operations; one whose API has no database is refused.
func TestGoServerOfTableOperations(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
	}
	in := goTableInput(repoRoot, paths)
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
	if !regexp.MustCompile(`RegisterRoutes\(fixtureTrackerApiRouter, fixturetrackerapi\.Config\{\n\s+DB: +fixtureTrackerDb,`).Match(main) {
		t.Errorf("main.go passes RegisterRoutes no DB:\n%s", main)
	}

	// An API that is not public takes the database for its table
	// operations alone.
	in = goTableInput(repoRoot, paths)
	o := in.APIs[0].Output
	o.IsPublic, o.UpstreamSchema = false, ""
	o.Endpoints[0].RequiresAuth, o.Endpoints[0].PublicRoute = false, true
	if s, err = servergen.Plan(in); err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	if main, err = os.ReadFile(filepath.Join(in.Dir, servergen.MainFile)); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`RegisterRoutes\(fixtureTrackerApiRouter, fixturetrackerapi\.Config\{\n\s+DB: +fixtureTrackerDb,`).Match(main) {
		t.Errorf("main.go of an API that is not public passes RegisterRoutes no DB:\n%s", main)
	}

	in = goTableInput(repoRoot, paths)
	o = in.APIs[0].Output
	o.IsPublic, o.UpstreamSchema, o.Deps, o.EnvConfig = false, "", apigen.DepsInfo{}, nil
	o.Endpoints[0].RequiresAuth = false
	const want = "stack tracker-stack: server tracker serves fixture-tracker-api, whose table operations are served from the tables of its database through its ORM (D106), and it has no database"
	if _, err := servergen.Plan(in); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Plan without a database = %v, want an error containing %q", err, want)
	}
}

// TestGoServerOfTableOperationsNeedsACaller: the Go server of an API
// neither public nor over a user model authenticates no caller, so one
// whose table operation's route requires a caller is refused, naming the
// route, the kind and the table (D106); one whose table operations are
// @publicRoute, or only services call, is planned.
func TestGoServerOfTableOperationsNeedsACaller(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
	}
	in := goTableInput(repoRoot, paths)
	o := in.APIs[0].Output
	o.IsPublic, o.UpstreamSchema = false, ""
	_, err := servergen.Plan(in)
	const want = "stack tracker-stack: server tracker serves fixture-tracker-api, whose route GET /api/teams/{id} serves the read of Team, a table of fixture-tracker-db (D106), for the caller the operation requires, and the Go server authenticates no caller"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Plan = %v, want an error containing %q", err, want)
	}

	o.Endpoints[0].RequiresAuth, o.Endpoints[0].PublicRoute = false, true
	if _, err := servergen.Plan(in); err != nil {
		t.Errorf("Plan of a @publicRoute table operation = %v", err)
	}
	o.Endpoints[0].PublicRoute, o.Endpoints[0].ServiceCallers = false, &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}
	o.HasServiceCallers = true
	if _, err := servergen.Plan(in); err != nil {
		t.Errorf("Plan of a table operation only services call = %v", err)
	}
}
