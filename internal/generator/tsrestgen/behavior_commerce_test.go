package tsrestgen

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// commerceAPI is the API that serves fixture-commerce-db's Inventory and
// Checkout (D70 on D103's step 4) for any signed-in caller: Stock's
// operations, the contract Supply's among them, and Holds' and the status
// Workflow's transition, routes of the row under each use's member. The
// router serves its MCP tools.
const commerceAPI = "fixture-commerce-api"

// commerceDB is fixture-commerce-api's database, its one DB dependency.
const commerceDB = "fixture-commerce-db"

// commerceFixture is fixture-commerce-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func commerceFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, commerceDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commerceDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, commerceAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", commerceAPI, err)
	}
	deps := map[string]*ir.Schema{commerceDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     commerceAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: commerceAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: commerceDeps}
}

// commerceDeps is fixture-commerce-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var commerceDeps = DepsInfo{
	Database:             commerceDB,
	Dialect:              "postgres",
	ORM:                  "FixtureCommerceDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-commerce-db-types",
}

// TestWriteAPIGoldenBehaviorCommerce pins the package for
// fixture-commerce-api: its router mounts Stock's and Holds' operations on
// the row under each use's member, each calling the repository's operation
// that takes the row's key, and no behavior worker. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorCommerce -update
func TestWriteAPIGoldenBehaviorCommerce(t *testing.T) {
	fixture := commerceFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(commerceDeps); err != nil {
		t.Fatalf("SetDeps: %v", err)
	}
	var paths []string
	for _, ep := range fixture.endpoints.Endpoints {
		if ep.BehaviorOperation() != nil {
			paths = append(paths, ep.Method+" "+ep.Path)
		}
	}
	slices.Sort(paths)
	if want := []string{
		"POST /api/checkout/{id}/holds/list-holds",
		"POST /api/checkout/{id}/holds/retake-holds",
		"POST /api/checkout/{id}/status/transition",
		"POST /api/inventory/{id}/stock/adjust-stock",
		"POST /api/inventory/{id}/stock/check-hold",
		"POST /api/inventory/{id}/stock/commit-for",
		"POST /api/inventory/{id}/stock/count-stock",
		"POST /api/inventory/{id}/stock/hold-for",
		"POST /api/inventory/{id}/stock/list-stock-movements",
		"POST /api/inventory/{id}/stock/receive-stock",
		"POST /api/inventory/{id}/stock/release-for",
		"POST /api/inventory/{id}/stock/return-for",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, commerceAPI)
}
