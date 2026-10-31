package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/ormgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The behavior operations fixture (D103): fixture-workflow-api serves the
// operations of fixture-workflow-db's Order and Shipment, whose fields are
// Workflows, from two @behaviorOperations sets, beside one authored
// operation, getOrder.
const (
	behaviorAPI = "fixture-workflow-api"
	behaviorDB  = "fixture-workflow-db"
)

// loadBehaviorFixtures loads fixture-workflow-api, with its behavior
// operations added, and fixture-workflow-db, its one DB dependency.
func loadBehaviorFixtures(t *testing.T) (api, db *ir.Schema) {
	t.Helper()
	api, err := loader.LoadService(filepath.Join(fixturesDir, behaviorAPI))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorAPI, err)
	}
	db, err = loader.LoadService(filepath.Join(fixturesDir, behaviorDB))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorDB, err)
	}
	return api, db
}

// generateBehaviorAPI is the API output of fixture-workflow-api, which
// names no authDb, over fixture-workflow-db, as the dispatch layer
// generates it before it sets Deps.
func generateBehaviorAPI(t *testing.T, api, db *ir.Schema) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(api, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   behaviorAPI,
		ModulePath:   "example.com/schemas/api/" + behaviorAPI,
		TypesModule:  "example.com/schemas/types/go/" + behaviorAPI,
		Dependencies: map[string]*ir.Schema{behaviorDB: db},
		ModuleDependencies: []string{
			"example.com/schemas/types/go/" + behaviorDB,
		},
		Clock: goModuleClock,
	})
	if err != nil {
		t.Fatalf("generate %s: %v", behaviorAPI, err)
	}
	if output == nil {
		t.Fatalf("no API output for %s", behaviorAPI)
	}
	return output
}

// TestWriteAPIGoldenBehaviorOperations generates the Go API module of
// fixture-workflow-api and holds the files the behavior operations change
// to their golden copies: the routes, mounted on handlers that call the
// ORM of fixture-workflow-db; the interfaces, which have no method for
// them and no implementation of shipment, whose every operation is a
// behavior's; behaviors.go; the OpenAPI document; the permission catalog,
// which lists the permissions the Workflows' configs name; and go.mod,
// which requires and replaces the rules module. Regenerate with:
// go test ./internal/generator/apigen -run TestWriteAPIGoldenBehaviorOperations -update
func TestWriteAPIGoldenBehaviorOperations(t *testing.T) {
	api, db := loadBehaviorFixtures(t)
	output := generateBehaviorAPI(t, api, db)

	var behaviors []string
	for _, ep := range output.Endpoints {
		if bo := ep.BehaviorOperation(); bo != nil {
			behaviors = append(behaviors, ep.Method+" "+ep.Path+" "+ep.BehaviorRepositoryCall())
		}
	}
	want := []string{
		"POST /api/order/{id}/status/transition GetOrderRepository().Status().Transition",
		"POST /api/shipment/{id}/payment-status/transition GetShipmentRepository().PaymentStatus().Transition",
		"POST /api/shipment/{id}/status/transition GetShipmentRepository().Status().Transition",
	}
	if !slices.Equal(behaviors, want) {
		t.Fatalf("behavior endpoints = %q, want %q", behaviors, want)
	}
	if output.Deps.Database != behaviorDB || !output.Deps.Rules {
		t.Fatalf("Deps = %+v, want the ORM of %s, which requires the rules module", output.Deps, behaviorDB)
	}

	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(view.Namespaces, []string{"order"}) || len(view.Endpoints) != 1 || len(view.BehaviorEndpoints) != 3 {
		t.Fatalf("the implemented view has namespaces %v, %d endpoints and %d behavior endpoints, want [order], getOrder alone and 3", view.Namespaces, len(view.Endpoints), len(view.BehaviorEndpoints))
	}
	if !view.HasAuth || !view.HasPermissionEndpoints {
		t.Fatalf("the implemented view's router flags: HasAuth %t, HasPermissionEndpoints %t, want both: the behavior routes need a caller and orders.write", view.HasAuth, view.HasPermissionEndpoints)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+behaviorAPI); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}

	goldenDir := filepath.Join("testdata", "golden", behaviorAPI)
	for _, name := range []string{"go.mod", "routes.go", "interfaces.go", "behaviors.go", "deps.go", "openapi.json", "permissions.json"} {
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

// TestBehaviorOperationsAPICompiles generates fixture-workflow-db's Go
// types and ORM and fixture-workflow-api's Go types and API module into a
// temporary tree shaped as the dist, wired to the checkout's runtime
// modules, the rules module among them, and builds and vets the API
// module: its handlers call the ORM's operations with its types, which
// the generated code converts. Linking it needs the behaviors' core's
// archive, which a build does not; go vet type-checks the cgo package.
//
// An API whose every operation is a behavior's has no implementation
// interface at all, and compiles too.
func TestBehaviorOperationsAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadBehaviorFixtures(t)
	apiDir := writeBehaviorModules(t, api, db)
	buildModule := func(t *testing.T, dir string) {
		t.Helper()
		for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
			}
		}
	}
	buildModule(t, apiDir)

	behaviorsOnly, db := loadBehaviorFixtures(t)
	behaviorsOnly.OperationSets = slices.DeleteFunc(behaviorsOnly.OperationSets, func(set *ir.OperationSet) bool { return !set.IsBehaviorOperations() })
	buildModule(t, writeBehaviorModules(t, behaviorsOnly, db))

	routes, err := os.ReadFile(filepath.Join(apiDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func createOrderStatusTransitionHandler(db orm.DatabaseInterface) gohttp.HandlerFunc {",
		"runBehaviorOperation(r.Context(), db.GetOrderRepository().Status().Transition, Id, input, &result)",
		"Handler: createShipmentPaymentStatusTransitionHandler(cfg.DB),",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go holds no %q", want)
		}
	}
}

// writeBehaviorModules writes the modules TestBehaviorOperationsAPICompiles
// builds and returns the API module's directory.
func writeBehaviorModules(t *testing.T, api, db *ir.Schema) string {
	t.Helper()
	paths := testpaths.Local(t)
	root := t.TempDir()
	dbTypesModule := "example.com/schemas/types/go/" + behaviorDB
	apiTypesModule := "example.com/schemas/types/go/" + behaviorAPI
	dbTypesDir := filepath.Join(root, "types", "go", behaviorDB)
	apiTypesDir := filepath.Join(root, "types", "go", behaviorAPI)
	ormDir := filepath.Join(root, "orm", behaviorDB)
	apiDir := filepath.Join(root, "api", behaviorAPI)
	// The core's registry declares Workflow, which the database's types
	// and ORM render.
	reg := registry.New(naming.Default())

	dbTypes, err := typegen.Generate(db, typegen.Options{SchemaName: behaviorDB, ModulePath: dbTypesModule, Registry: reg, Clock: goModuleClock})
	if err != nil {
		t.Fatalf("generate %s types: %v", behaviorDB, err)
	}
	if err := typegen.SetReplacePaths(dbTypes, paths, dbTypesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(dbTypes, dbTypesDir); err != nil {
		t.Fatalf("write %s types: %v", behaviorDB, err)
	}
	apiTypes, err := typegen.Generate(api, typegen.Options{
		SchemaName:        behaviorAPI,
		ModulePath:        apiTypesModule,
		Dependencies:      map[string]*ir.Schema{behaviorDB: db},
		DependencyModules: map[string]string{behaviorDB: dbTypesModule},
		Registry:          reg,
		Clock:             goModuleClock,
	})
	if err != nil {
		t.Fatalf("generate %s types: %v", behaviorAPI, err)
	}
	if err := typegen.SetReplacePaths(apiTypes, paths, apiTypesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(apiTypes, apiTypesDir); err != nil {
		t.Fatalf("write %s types: %v", behaviorAPI, err)
	}
	orm, err := ormgen.Generate(db, ormgen.Options{
		SchemaName:  behaviorDB,
		ModulePath:  "example.com/schemas/orm/" + behaviorDB,
		TypesModule: dbTypesModule,
		Registry:    reg,
		Clock:       goModuleClock,
	})
	if err != nil {
		t.Fatalf("generate %s orm: %v", behaviorDB, err)
	}
	if err := ormgen.SetReplacePaths(orm, paths, ormDir); err != nil {
		t.Fatal(err)
	}
	if err := ormgen.WriteORM(orm, ormDir); err != nil {
		t.Fatalf("write %s orm: %v", behaviorDB, err)
	}

	output := generateBehaviorAPI(t, api, db)
	output.AddIndirectModules([]string{dbTypesModule})
	if err := apigen.SetReplacePaths(output, paths, apiDir); err != nil {
		t.Fatal(err)
	}
	if output.RulesReplacePath == "" {
		t.Fatal("SetReplacePaths gave the rules module no replace path")
	}
	if err := apigen.WriteAPI(output, apiDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	return apiDir
}
