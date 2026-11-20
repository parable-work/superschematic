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

// numberingAPI is the API that serves fixture-numbering-db's Invoice and
// Ticket (D103's step 4) for any signed-in caller: each Numbering field's
// assignNumber, a route of the row under the field. The router serves its
// MCP tools.
const numberingAPI = "fixture-numbering-api"

// numberingDB is fixture-numbering-api's database, its one DB dependency.
const numberingDB = "fixture-numbering-db"

// numberingFixture is fixture-numbering-api with its database and apigen's
// endpoints for it, read with one registry, which declares the behaviors.
func numberingFixture(t *testing.T) apiFixture {
	t.Helper()
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(fixturesDir, numberingDB), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", numberingDB, err)
	}
	schema, err := loader.LoadService(filepath.Join(fixturesDir, numberingAPI), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", numberingAPI, err)
	}
	deps := map[string]*ir.Schema{numberingDB: db}
	endpoints, err := apigen.Generate(schema, apigen.Options{
		Provider:       sessionauth.Provider{},
		SchemaName:     numberingAPI,
		Dependencies:   deps,
		Clock:          fixedClock,
		ToolInvocation: reg.ToolInvocationPolicy(),
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return apiFixture{name: numberingAPI, schema: schema, deps: deps, endpoints: endpoints, registry: reg, mcp: true, depsInfo: numberingDeps}
}

// numberingDeps is fixture-numbering-api's Deps as the dispatch layer sets
// it: a pool of its database, which lists postgres first, and the
// repositories its TypeScript types export.
var numberingDeps = DepsInfo{
	Database:             numberingDB,
	Dialect:              "postgres",
	ORM:                  "FixtureNumberingDbOrm",
	DatabaseTypesPackage: "@schemas/fixture-numbering-db-types",
}

// TestWriteAPIGoldenBehaviorNumbering pins the package for
// fixture-numbering-api: its router mounts each Numbering field's
// assignNumber under the field, Invoice's number and Ticket's number and
// reference, each calling the repository's operation under that member,
// and no behavior worker. Regenerate with
// go test ./internal/generator/tsrestgen -run TestWriteAPIGoldenBehaviorNumbering -update
func TestWriteAPIGoldenBehaviorNumbering(t *testing.T) {
	fixture := numberingFixture(t)
	output := generateBehavior(t, fixture)
	if err := output.SetDeps(numberingDeps); err != nil {
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
		"POST /api/invoice/{id}/number/assign-number",
		"POST /api/ticket/{id}/number/assign-number",
		"POST /api/ticket/{id}/reference/assign-number",
	}; !slices.Equal(paths, want) {
		t.Errorf("the behavior routes = %q, want %q", paths, want)
	}
	checkGolden(t, output, numberingAPI)
}
